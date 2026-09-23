// Package watch -- агент, который работает 24/7: по таймеру делает прогноз,
// сохраняет его и ждёт следующего раза.
//
// Здесь же настройки, которые меняются из интерфейса: частота, окно анализа и
// модель. Частота одна на двоих -- с ней и MCP-сервер собирает цены, и агент
// делает прогноз. Поэтому смена частоты уходит на сервер инструментом
// crypto_schedule: одна ручка в интерфейсе -- два расписания.
package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"crypto-watch/internal/agent"
)

// Границы настроек.
const (
	MinInterval = 1
	MaxInterval = 15
	MinWindow   = 15
	MaxWindow   = 240
)

// Settings -- то, что меняется из интерфейса.
type Settings struct {
	IntervalMinutes int    `json:"intervalMinutes"`
	WindowMinutes   int    `json:"windowMinutes"`
	Model           string `json:"model"`
}

// Defaults -- настройки при первом запуске.
func Defaults() Settings {
	return Settings{IntervalMinutes: 1, WindowMinutes: 60, Model: agent.DefaultModel}
}

// Scheduler -- то, что умеет менять расписание сбора на MCP-сервере.
type Scheduler interface {
	Call(ctx context.Context, name string, args any, out any) error
}

// Status -- что агент делает сейчас.
type Status struct {
	Running     bool   `json:"running"`
	LastRunAt   string `json:"lastRunAt,omitempty"`
	LastOKAt    string `json:"lastOkAt,omitempty"`
	NextRunAt   string `json:"nextRunAt,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	LastModel   string `json:"lastModel,omitempty"`
	ScheduleSet bool   `json:"scheduleSynced"`
}

// Service -- агент с расписанием.
type Service struct {
	forecaster *agent.Forecaster
	mcp        Scheduler
	path       string
	firstDelay time.Duration

	mu       sync.Mutex
	settings Settings
	status   Status

	reset   chan struct{}
	trigger chan struct{}
}

// New поднимает сервис и читает настройки с диска.
func New(forecaster *agent.Forecaster, mcp Scheduler, dataDir string, firstDelay time.Duration) (*Service, error) {
	s := &Service{
		forecaster: forecaster,
		mcp:        mcp,
		path:       filepath.Join(dataDir, "settings.json"),
		firstDelay: firstDelay,
		settings:   Defaults(),
		reset:      make(chan struct{}, 1),
		trigger:    make(chan struct{}, 1),
	}
	raw, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// первый запуск -- настройки по умолчанию
	case err != nil:
		return nil, err
	default:
		var saved Settings
		if err := json.Unmarshal(raw, &saved); err != nil {
			log.Printf("настройки %s не разобрались, беру по умолчанию: %v", s.path, err)
		} else if validate(saved) == nil {
			s.settings = saved
		}
	}
	return s, nil
}

// Settings -- текущие настройки.
func (s *Service) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// Status -- текущее состояние.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// ErrInvalid -- настройки вне допустимых границ.
var ErrInvalid = errors.New("недопустимые настройки")

func validate(set Settings) error {
	if set.IntervalMinutes < MinInterval || set.IntervalMinutes > MaxInterval {
		return fmt.Errorf("%w: частота от %d до %d минут", ErrInvalid, MinInterval, MaxInterval)
	}
	if set.WindowMinutes < MinWindow || set.WindowMinutes > MaxWindow {
		return fmt.Errorf("%w: окно от %d до %d минут", ErrInvalid, MinWindow, MaxWindow)
	}
	if _, ok := agent.FindModel(set.Model); !ok {
		return fmt.Errorf("%w: неизвестная модель %q", ErrInvalid, set.Model)
	}
	return nil
}

// Update меняет настройки, сохраняет их и перезаводит расписание.
func (s *Service) Update(ctx context.Context, set Settings) (Settings, error) {
	if err := validate(set); err != nil {
		return Settings{}, err
	}
	model, _ := agent.FindModel(set.Model)
	if !s.forecaster.Available(model) {
		return Settings{}, fmt.Errorf("%w: у модели %s нет ключа провайдера в окружении", ErrInvalid, model.Title)
	}

	raw, _ := json.MarshalIndent(set, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return Settings{}, err
	}
	if err := os.WriteFile(s.path, raw, 0o644); err != nil {
		return Settings{}, err
	}

	s.mu.Lock()
	changed := s.settings.IntervalMinutes != set.IntervalMinutes
	s.settings = set
	if changed {
		s.status.ScheduleSet = false
	}
	s.mu.Unlock()

	if changed {
		// сразу, а не в цикле: пользователь должен увидеть, что сервер принял частоту
		s.syncSchedule(ctx)
		select {
		case s.reset <- struct{}{}:
		default:
		}
	}
	return set, nil
}

// Trigger просит сделать прогноз вне очереди. Если прогноз уже идёт, второй
// встанет за ним, третий -- нет.
func (s *Service) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Run -- вечный цикл агента.
func (s *Service) Run(ctx context.Context) {
	s.syncSchedule(ctx)

	// первый прогноз -- не сразу: MCP-сервер только что поднялся, пусть соберёт точки
	timer := time.NewTimer(s.firstDelay)
	s.setNext(s.firstDelay)
	defer timer.Stop()

	for {
		var started time.Time
		select {
		case <-ctx.Done():
			return
		case <-s.reset:
			stopTimer(timer)
			started = time.Now()
		case <-s.trigger:
			stopTimer(timer)
			started = time.Now()
			s.runOnce(ctx)
		case <-timer.C:
			started = time.Now()
			s.runOnce(ctx)
		}
		// интервал считается от начала прогона, а не от конца: бесплатная модель
		// думает до минуты, и «раз в минуту» иначе превратилось бы в «раз в две»
		wait := max(time.Duration(s.Settings().IntervalMinutes)*time.Minute-time.Since(started), minGap)
		timer.Reset(wait)
		s.setNext(wait)
	}
}

// minGap -- минимальная пауза между прогонами, если прогон шёл дольше интервала.
const minGap = 5 * time.Second

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (s *Service) setNext(wait time.Duration) {
	s.mu.Lock()
	s.status.NextRunAt = time.Now().Add(wait).UTC().Format(time.RFC3339)
	s.mu.Unlock()
}

// syncSchedule сообщает MCP-серверу частоту сбора. Если сервер недоступен --
// не беда, попытка повторится перед следующим прогнозом.
func (s *Service) syncSchedule(ctx context.Context) {
	interval := s.Settings().IntervalMinutes
	err := s.mcp.Call(ctx, "crypto_schedule", map[string]any{"interval_minutes": interval}, nil)
	s.mu.Lock()
	s.status.ScheduleSet = err == nil
	s.mu.Unlock()
	if err != nil {
		log.Printf("частота сбора на MCP-сервере не выставлена: %v", err)
		return
	}
	log.Printf("MCP-сервер собирает цены каждые %d мин", interval)
}

// runOnce -- один прогноз с сохранением. Ошибка не останавливает агента: она
// видна в статусе, а следующий прогноз пойдёт по расписанию.
func (s *Service) runOnce(ctx context.Context) {
	if !s.Status().ScheduleSet {
		s.syncSchedule(ctx)
	}

	set := s.Settings()
	model, _ := agent.FindModel(set.Model)

	s.mu.Lock()
	s.status.Running = true
	s.status.LastRunAt = time.Now().UTC().Format(time.RFC3339)
	s.status.LastModel = model.ID
	s.mu.Unlock()

	// потолок на весь прогноз: бесплатная модель в очереди может думать долго,
	// но не дольше, чем до следующего прогноза плюс запас
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	log.Printf("прогноз: модель %s, окно %d мин", model.ID, set.WindowMinutes)
	result, err := s.forecaster.Run(runCtx, model, set.WindowMinutes)
	if err == nil {
		var id int64
		id, err = s.forecaster.Save(runCtx, result)
		if err == nil {
			log.Printf("прогноз #%d готов за %.1f с, шагов %d: %s", id, float64(result.DurationMs)/1000, len(result.Steps), result.Overview)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Running = false
	if err != nil {
		log.Printf("прогноз не удался: %v", err)
		s.status.LastError = err.Error()
		return
	}
	s.status.LastError = ""
	s.status.LastOKAt = time.Now().UTC().Format(time.RFC3339)
}
