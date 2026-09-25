// Package runs -- прогоны: очередь, история на диске и живые события для интерфейса.
//
// Прогон -- одна заявка на план: форма, события хода работы, проверки флоу и
// готовый план. Прогоны выполняются по одному: бесплатная модель ограничена по
// частоте, и два прогона сразу только мешали бы друг другу. Каждый прогон --
// JSON-файл в DATA_DIR/runs, история переживает перезапуск.
package runs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"trip-agent/internal/agent"
)

// Статусы прогона.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusWarn    = "done_with_errors"
	StatusFailed  = "failed"
	StatusRefused = "refused"
)

// Run -- прогон.
type Run struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	CreatedAt  string          `json:"createdAt"`
	StartedAt  string          `json:"startedAt,omitempty"`
	FinishedAt string          `json:"finishedAt,omitempty"`
	Model      string          `json:"model"`
	Form       agent.Form      `json:"form"`
	Request    agent.Request   `json:"request"`
	Events     []agent.Event   `json:"events"`
	Checks     []agent.Check   `json:"checks"`
	TripID     string          `json:"tripId,omitempty"`
	File       string          `json:"file,omitempty"`
	Plan       json.RawMessage `json:"plan,omitempty"`
	Answer     string          `json:"answer,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// Summary -- прогон для списка истории.
type Summary struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	City      string `json:"city"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	CreatedAt string `json:"createdAt"`
	Position  int    `json:"position,omitempty"`
}

// Tools -- то, что нужно менеджеру от реестра.
type Tools interface {
	agent.ToolBox
	Discover(ctx context.Context)
	CallDirect(ctx context.Context, server, tool string, args any, out any) error
	ServersJSON() json.RawMessage
}

// Manager -- очередь и история.
type Manager struct {
	dir   string
	agent *agent.Agent
	tools Tools
	now   func() time.Time
	// runTimeout -- потолок на весь прогон
	runTimeout time.Duration

	mu    sync.Mutex
	runs  map[string]*Run
	queue []string
	subs  map[string]map[chan agent.Event]bool
	wake  chan struct{}
}

// ErrNotFound -- такого прогона нет.
var ErrNotFound = errors.New("прогон не найден")

var idPattern = regexp.MustCompile(`^run_[0-9a-f]{10}$`)

// New загружает историю. Прогоны, прерванные перезапуском, помечаются неудачными.
func New(dir string, a *agent.Agent, tools Tools, now func() time.Time, runTimeout time.Duration) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m := &Manager{dir: dir, agent: a, tools: tools, now: now, runTimeout: runTimeout,
		runs: map[string]*Run{}, subs: map[string]map[chan agent.Event]bool{}, wake: make(chan struct{}, 1)}

	files, _ := filepath.Glob(filepath.Join(dir, "run_*.json"))
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var run Run
		if json.Unmarshal(raw, &run) != nil || !idPattern.MatchString(run.ID) {
			log.Printf("пропускаю повреждённый прогон %s", path)
			continue
		}
		if run.Status == StatusQueued || run.Status == StatusRunning {
			run.Status, run.Error = StatusFailed, "прервано перезапуском сервиса"
			run.FinishedAt = now().UTC().Format(time.RFC3339)
			m.save(&run)
		}
		m.runs[run.ID] = &run
	}
	return m, nil
}

// Submit проверяет форму и ставит прогон в очередь.
func (m *Manager) Submit(form agent.Form) (Run, error) {
	req, err := agent.Prepare(form, m.now())
	if err != nil {
		return Run{}, err
	}
	random := make([]byte, 5)
	rand.Read(random)
	run := &Run{
		ID: "run_" + hex.EncodeToString(random), Status: StatusQueued, CreatedAt: m.now().UTC().Format(time.RFC3339),
		Model: m.agent.Model, Form: form, Request: req, Events: []agent.Event{}, Checks: []agent.Check{},
	}
	m.mu.Lock()
	m.runs[run.ID] = run
	m.queue = append(m.queue, run.ID)
	position := len(m.queue)
	m.mu.Unlock()

	m.emit(run.ID, agent.Event{Type: agent.EventQueued, Text: fmt.Sprintf("в очереди: %d", position)})
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return m.Get(run.ID)
}

// Work -- исполнитель очереди, по одному прогону.
func (m *Manager) Work(ctx context.Context) {
	for {
		m.mu.Lock()
		var id string
		if len(m.queue) > 0 {
			id, m.queue = m.queue[0], m.queue[1:]
		}
		m.mu.Unlock()
		if id == "" {
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
				continue
			}
		}
		m.execute(ctx, id)
	}
}

func (m *Manager) execute(parent context.Context, id string) {
	ctx, cancel := context.WithTimeout(parent, m.runTimeout)
	defer cancel()

	m.update(id, func(r *Run) {
		r.Status, r.StartedAt = StatusRunning, m.now().UTC().Format(time.RFC3339)
	})
	m.emit(id, agent.Event{Type: agent.EventStarted})

	m.tools.Discover(ctx)
	m.emit(id, agent.Event{Type: agent.EventServers, Servers: m.tools.ServersJSON()})

	run, _ := m.Get(id)
	log.Printf("[%s] план: %s, %s — %s", id, run.Request.City, run.Request.StartDate, run.Request.EndDate)
	out, err := m.agent.Run(ctx, run.Request, func(e agent.Event) { m.emit(id, e) })

	switch {
	case err != nil:
		log.Printf("[%s] ✘ %v", id, err)
		m.finish(id, StatusFailed, func(r *Run) { r.Error = err.Error() })
		return
	case out.Refused:
		m.finish(id, StatusRefused, func(r *Run) { r.Answer = out.Answer })
		return
	}

	var plan map[string]any
	var planRaw json.RawMessage
	if out.TripID != "" {
		if err := m.tools.CallDirect(ctx, "trip", "trip_get", map[string]any{"trip_id": out.TripID}, &plan); err == nil {
			planRaw, _ = json.Marshal(plan)
		}
	}
	checks := agent.Verify(run.Request, out, plan)
	status := StatusDone
	for _, c := range checks {
		log.Printf("[%s] проверка %s: %v — %s", id, c.Name, c.OK, c.Detail)
		if !c.OK {
			status = StatusWarn
		}
	}
	m.emit(id, agent.Event{Type: agent.EventVerify, Checks: checks})
	if planRaw != nil {
		m.emit(id, agent.Event{Type: agent.EventPlan, Plan: planRaw})
	}
	m.finish(id, status, func(r *Run) {
		r.Checks, r.TripID, r.File, r.Plan, r.Answer = checks, out.TripID, out.File, planRaw, out.Answer
	})
}

func (m *Manager) finish(id, status string, change func(*Run)) {
	m.update(id, func(r *Run) {
		change(r)
		r.Status, r.FinishedAt = status, m.now().UTC().Format(time.RFC3339)
	})
	run, _ := m.Get(id)
	m.emit(id, agent.Event{Type: agent.EventFinished, Status: status, Error: run.Error})
	log.Printf("[%s] итог: %s", id, status)
}

// emit дописывает событие в прогон, сохраняет и раздаёт подписчикам.
func (m *Manager) emit(id string, e agent.Event) {
	m.mu.Lock()
	run, ok := m.runs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	e.Seq = len(run.Events) + 1
	e.At = m.now().UTC().Format(time.RFC3339Nano)
	run.Events = append(run.Events, e)
	copyRun := *run
	subs := make([]chan agent.Event, 0, len(m.subs[id]))
	for ch := range m.subs[id] {
		subs = append(subs, ch)
	}
	m.mu.Unlock()

	m.save(&copyRun)
	logEvent(id, e)
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// подписчик не успевает -- пропустит событие и дочитает историю при переподключении
		}
	}
}

func (m *Manager) update(id string, change func(*Run)) {
	m.mu.Lock()
	run, ok := m.runs[id]
	if ok {
		change(run)
	}
	var copyRun Run
	if ok {
		copyRun = *run
	}
	m.mu.Unlock()
	if ok {
		m.save(&copyRun)
	}
}

func (m *Manager) save(run *Run) {
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(m.dir, run.ID+".json")
	if err := os.WriteFile(path+".tmp", raw, 0o644); err == nil {
		os.Rename(path+".tmp", path)
	}
}

// Get -- прогон целиком.
func (m *Manager) Get(id string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	copyRun := *run
	copyRun.Events = append([]agent.Event(nil), run.Events...)
	return copyRun, nil
}

// List -- история, свежие первыми.
func (m *Manager) List() []Summary {
	m.mu.Lock()
	defer m.mu.Unlock()
	position := map[string]int{}
	for i, id := range m.queue {
		position[id] = i + 1
	}
	out := make([]Summary, 0, len(m.runs))
	for _, r := range m.runs {
		out = append(out, Summary{ID: r.ID, Status: r.Status, City: r.Request.City, StartDate: r.Request.StartDate,
			EndDate: r.Request.EndDate, CreatedAt: r.CreatedAt, Position: position[r.ID]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// Subscribe -- события после since и канал живых. Канал закрывать не нужно:
// cancel отписывает.
func (m *Manager) Subscribe(id string, since int) ([]agent.Event, <-chan agent.Event, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[id]
	if !ok {
		return nil, nil, nil, ErrNotFound
	}
	past := []agent.Event{}
	for _, e := range run.Events {
		if e.Seq > since {
			past = append(past, e)
		}
	}
	ch := make(chan agent.Event, 256)
	if m.subs[id] == nil {
		m.subs[id] = map[chan agent.Event]bool{}
	}
	m.subs[id][ch] = true
	cancel := func() {
		m.mu.Lock()
		delete(m.subs[id], ch)
		m.mu.Unlock()
	}
	return past, ch, cancel, nil
}

// Finished -- прогон завершён и новых событий не будет.
func Finished(status string) bool {
	return status != StatusQueued && status != StatusRunning
}

// logEvent -- строка в лог контейнера для каждого значимого события.
func logEvent(id string, e agent.Event) {
	switch e.Type {
	case agent.EventDecided:
		names := []string{}
		for _, c := range e.Calls {
			names = append(names, c.Server+"."+c.Tool)
		}
		log.Printf("[%s] ход %d: модель выбрала %v", id, e.Turn, names)
	case agent.EventToolFinished:
		mark := "✔"
		if e.OK != nil && !*e.OK {
			mark = "✘"
		}
		log.Printf("[%s] %s %s → mcp-%s: %s (%dms)", id, mark, e.Tool, e.Server, e.Summary, e.DurationMs)
	case agent.EventInjection:
		log.Printf("[%s] ⚠ в ответе %s.%s похоже на инъекцию: %s", id, e.Server, e.Tool, e.Quote)
	case agent.EventNudge:
		log.Printf("[%s] напоминание модели: %s", id, e.Text)
	}
}
