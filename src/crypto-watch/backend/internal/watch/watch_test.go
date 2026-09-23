package watch

import (
	"context"
	"errors"
	"testing"
	"time"

	"crypto-watch/internal/agent"
	"crypto-watch/internal/llm"
)

type fakeMCP struct {
	intervals []any
	fail      bool
}

func (f *fakeMCP) Call(_ context.Context, name string, args any, _ any) error {
	if f.fail {
		return errors.New("сервер недоступен")
	}
	if name == "crypto_schedule" {
		f.intervals = append(f.intervals, args.(map[string]any)["interval_minutes"])
	}
	return nil
}

func service(t *testing.T, dir string, mcp *fakeMCP) *Service {
	t.Helper()
	providers := map[string]llm.Provider{llm.ProviderOpenRouter: {APIKey: "k"}}
	s, err := New(agent.New(nil, providers, nil, 30), mcp, dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSettings(t *testing.T) {
	dir := t.TempDir()
	mcp := &fakeMCP{}
	s := service(t, dir, mcp)

	if s.Settings() != Defaults() || Defaults().IntervalMinutes != 1 {
		t.Errorf("по умолчанию: %+v", s.Settings())
	}

	bad := []Settings{
		{IntervalMinutes: 0, WindowMinutes: 60, Model: agent.DefaultModel},
		{IntervalMinutes: 16, WindowMinutes: 60, Model: agent.DefaultModel},
		{IntervalMinutes: 5, WindowMinutes: 10, Model: agent.DefaultModel},
		{IntervalMinutes: 5, WindowMinutes: 60, Model: "gpt"},
		// ключа DeepSeek в окружении теста нет
		{IntervalMinutes: 5, WindowMinutes: 60, Model: "deepseek-flash"},
	}
	for _, set := range bad {
		if _, err := s.Update(context.Background(), set); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v прошли проверку: %v", set, err)
		}
	}

	good := Settings{IntervalMinutes: 5, WindowMinutes: 120, Model: agent.DefaultModel}
	if _, err := s.Update(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	if len(mcp.intervals) != 1 || mcp.intervals[0] != 5 {
		t.Errorf("частота на MCP-сервер ушла как %v", mcp.intervals)
	}
	if !s.Status().ScheduleSet {
		t.Error("расписание не отмечено выставленным")
	}

	// окно поменялось, частота нет -- MCP-сервер не трогаем
	good.WindowMinutes = 30
	s.Update(context.Background(), good)
	if len(mcp.intervals) != 1 {
		t.Errorf("лишний вызов crypto_schedule: %v", mcp.intervals)
	}

	// настройки переживают перезапуск
	if again := service(t, dir, mcp); again.Settings() != good {
		t.Errorf("после перезапуска: %+v", again.Settings())
	}
}

func TestScheduleSyncFailure(t *testing.T) {
	s := service(t, t.TempDir(), &fakeMCP{fail: true})
	set := Settings{IntervalMinutes: 3, WindowMinutes: 60, Model: agent.DefaultModel}
	if _, err := s.Update(context.Background(), set); err != nil {
		t.Fatalf("недоступный MCP-сервер не должен мешать сохранить настройки: %v", err)
	}
	if s.Status().ScheduleSet {
		t.Error("расписание отмечено выставленным, хотя сервер недоступен")
	}
}
