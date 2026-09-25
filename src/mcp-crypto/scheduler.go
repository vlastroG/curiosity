package main

// Планировщик -- то, ради чего сервер существует: он работает, даже когда
// его никто не спрашивает.
//
// Обычный MCP-инструмент живёт от вызова до вызова: пока модель не позвала,
// ничего не происходит. Здесь наоборот -- сбор идёт по таймеру в фоне, а инструменты
// только читают накопленное. Поэтому «что было с ценой за последний час» сервер
// знает в любой момент, а не только с того момента, как его впервые спросили.
//
//	таймер ──► collect ──► биржа ──► SQLite
//	  ▲                                  │
//	  └── интервал из settings ◄── crypto_schedule(interval_minutes)
//
// Интервал меняется на ходу: новое значение приходит в цикл через канал, таймер
// перезаводится, и следующий сбор случается через новый интервал от последнего.

import (
	"context"
	"log"
	"sync"
	"time"
)

// Collector -- один поход за ценами. Вынесен в функцию, чтобы тест подставил свою.
type Collector func(ctx context.Context) ([]Quote, error)

// Scheduler -- фоновый сборщик цен.
type Scheduler struct {
	store     *Store
	collect   Collector
	retention time.Duration
	now       func() time.Time

	// runMu -- сбор идёт строго по одному: таймерный и ручной не должны
	// записать две строки за одну секунду
	runMu sync.Mutex

	// mu защищает поля состояния ниже
	mu       sync.Mutex
	interval time.Duration
	lastRun  time.Time
	nextRun  time.Time

	reset chan time.Duration
}

// newScheduler собирает планировщик. Интервал берётся из базы: если его меняли
// до перезапуска, он сохранится.
func newScheduler(store *Store, collect Collector, fallback, retention time.Duration) *Scheduler {
	return &Scheduler{
		store:     store,
		collect:   collect,
		retention: retention,
		now:       time.Now,
		interval:  store.Interval(context.Background(), fallback),
		reset:     make(chan time.Duration, 1),
	}
}

// Run крутит цикл до отмены контекста. Первый сбор -- сразу: сервер, который
// после старта минуту молчит, выглядит сломанным.
func (s *Scheduler) Run(ctx context.Context) {
	log.Printf("планировщик: сбор каждые %s", s.Interval())
	s.RunOnce(ctx)

	timer := time.NewTimer(s.arm())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case interval := <-s.reset:
			log.Printf("планировщик: новый интервал %s", interval)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.arm())
		case <-timer.C:
			s.RunOnce(ctx)
			timer.Reset(s.arm())
		}
	}
}

// arm запоминает время следующего сбора и возвращает, сколько до него ждать.
//
// Отсчёт -- от начала последнего сбора, а не от текущего момента: после смены
// интервала с пяти минут на одну следующий сбор должен случиться через минуту
// после прошлого, а не через минуту после смены. Если этот момент уже прошёл --
// собираем сразу.
func (s *Scheduler) arm() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	next := s.lastRun.Add(s.interval)
	if next.Before(now) {
		next = now
	}
	s.nextRun = next
	return next.Sub(now)
}

// RunOnce -- один сбор с записью результата. Ошибка биржи не останавливает цикл:
// она записывается в журнал прогонов и видна в статусе, а следующий сбор
// пойдёт по расписанию.
func (s *Scheduler) RunOnce(ctx context.Context) ([]Quote, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	at := s.now()
	s.mu.Lock()
	s.lastRun = at
	s.mu.Unlock()

	quotes, err := s.collect(ctx)
	if saveErr := s.store.SaveRun(context.WithoutCancel(ctx), at, quotes, err); saveErr != nil {
		log.Printf("планировщик: запись прогона не удалась: %v", saveErr)
	}
	if err != nil {
		log.Printf("планировщик: сбор не удался: %v", err)
		return nil, err
	}
	if s.retention > 0 {
		if err := s.store.Prune(ctx, at.Add(-s.retention)); err != nil {
			log.Printf("планировщик: чистка старых цен не удалась: %v", err)
		}
	}
	log.Printf("планировщик: собрано %d цен", len(quotes))
	return quotes, nil
}

// SetInterval меняет интервал, сохраняет его и перезаводит таймер.
//
// Тот же интервал -- не смена: клиент может выставлять его при каждом своём
// старте, и таймер от этого сбиваться не должен.
func (s *Scheduler) SetInterval(ctx context.Context, interval time.Duration) error {
	s.mu.Lock()
	same := s.interval == interval
	s.mu.Unlock()
	if same {
		return nil
	}

	if err := s.store.SetInterval(ctx, interval); err != nil {
		return err
	}
	s.mu.Lock()
	s.interval = interval
	s.mu.Unlock()

	// канал на одно значение, и отправка не ждёт: если сигнал уже лежит, цикл его
	// заберёт и всё равно возьмёт свежий интервал из s.interval
	select {
	case s.reset <- interval:
	default:
	}
	return nil
}

// Interval -- текущий интервал сбора.
func (s *Scheduler) Interval() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interval
}

// NextRun -- когда будет следующий сбор; нулевое время, если цикл ещё не запущен.
func (s *Scheduler) NextRun() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextRun
}
