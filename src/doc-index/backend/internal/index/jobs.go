package index

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrBusy -- индексация уже идёт.
var ErrBusy = errors.New("индексация уже идёт")

// Jobs -- не больше одной индексации за раз и её события для подписчиков.
// События последнего запуска хранятся в памяти: открыв страницу посреди
// индексации, интерфейс получает всё, что уже было, и дальше -- вживую.
type Jobs struct {
	mu      sync.Mutex
	running bool
	events  []Event
	subs    map[chan Event]struct{}
	cancel  context.CancelFunc
	lastErr string
	started time.Time
}

// JobState -- что сейчас с индексацией.
type JobState struct {
	Running bool       `json:"running"`
	Started *time.Time `json:"started,omitempty"`
	Error   string     `json:"error,omitempty"`
	Events  int        `json:"events"`
}

// State -- снимок состояния.
func (j *Jobs) State() JobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := JobState{Running: j.running, Error: j.lastErr, Events: len(j.events)}
	if !j.started.IsZero() {
		started := j.started
		st.Started = &started
	}
	return st
}

// Start запускает работу в фоне. Работа получает функцию для событий.
func (j *Jobs) Start(parent context.Context, work func(ctx context.Context, emit func(Event)) error) error {
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(parent)
	j.running, j.events, j.lastErr, j.cancel, j.started = true, nil, "", cancel, time.Now()
	j.mu.Unlock()

	go func() {
		defer cancel()
		err := work(ctx, j.Emit)
		if err != nil {
			j.Emit(Event{Stage: "error", Level: "error", Message: err.Error(), Time: time.Now()})
		}
		j.mu.Lock()
		j.running = false
		if err != nil {
			j.lastErr = err.Error()
		}
		for ch := range j.subs {
			close(ch)
		}
		j.subs = nil
		j.mu.Unlock()
	}()
	return nil
}

// Emit добавляет событие и рассылает подписчикам.
func (j *Jobs) Emit(e Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e.Seq = len(j.events) + 1
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	j.events = append(j.events, e)
	for ch := range j.subs {
		select {
		case ch <- e:
		default: // медленный подписчик догонит по since при переподключении
		}
	}
}

// Subscribe -- события после since и канал новых. Если индексация не идёт,
// канал закрыт сразу.
func (j *Jobs) Subscribe(since int) ([]Event, <-chan Event, func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var past []Event
	if since < len(j.events) {
		past = append(past, j.events[since:]...)
	}
	ch := make(chan Event, 256)
	if !j.running {
		close(ch)
		return past, ch, func() {}
	}
	if j.subs == nil {
		j.subs = map[chan Event]struct{}{}
	}
	j.subs[ch] = struct{}{}
	return past, ch, func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		if _, ok := j.subs[ch]; ok {
			delete(j.subs, ch)
			close(ch)
		}
	}
}

// Stop прерывает текущую индексацию (при остановке сервера).
func (j *Jobs) Stop() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancel != nil {
		j.cancel()
	}
}
