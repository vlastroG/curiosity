package store

import (
	"errors"
	"path/filepath"
	"testing"

	"agent-sprout/internal/agent"
)

func TestSnapshotSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "chats.json")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("открытие пустого хранилища: %v", err)
	}

	cfg := agent.DefaultConfig("deepseek-v4-flash")
	cfg.Temperature = 1.4

	chat, err := first.Create("проверка", cfg)
	if err != nil {
		t.Fatalf("создание чата: %v", err)
	}
	if _, err := first.Append(chat.ID, Message{Role: "user", Kind: KindQuestion, Content: "привет"}); err != nil {
		t.Fatalf("добавление сообщения: %v", err)
	}

	// второе открытие того же файла -- это и есть перезапуск контейнера
	second, err := Open(path)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}

	restored, err := second.Get(chat.ID)
	if err != nil {
		t.Fatalf("чат не восстановился: %v", err)
	}
	if restored.Title != "проверка" || restored.Config.Temperature != 1.4 {
		t.Fatalf("настройки не восстановились: %+v", restored.Config)
	}
	if len(restored.Messages) != 1 || restored.Messages[0].Content != "привет" {
		t.Fatalf("история не восстановилась: %+v", restored.Messages)
	}
}

func TestDeleteRemovesChat(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("открытие: %v", err)
	}

	chat, err := s.Create("на удаление", agent.DefaultConfig("deepseek-v4-flash"))
	if err != nil {
		t.Fatalf("создание: %v", err)
	}

	if err := s.Delete(chat.ID); err != nil {
		t.Fatalf("удаление: %v", err)
	}
	if _, err := s.Get(chat.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ожидалась ErrNotFound, получено %v", err)
	}
	if err := s.Delete(chat.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("повторное удаление должно давать ErrNotFound, получено %v", err)
	}
	if list := s.List(); len(list) != 0 {
		t.Fatalf("список должен опустеть, получено %+v", list)
	}
}

func TestClearMessagesKeepsConfig(t *testing.T) {
	s, _ := Open("")

	cfg := agent.DefaultConfig("deepseek-v4-flash")
	cfg.JudgeEnabled = true
	chat, _ := s.Create("чат", cfg)
	if _, err := s.Append(chat.ID, Message{Role: "user", Kind: KindQuestion, Content: "вопрос"}); err != nil {
		t.Fatalf("добавление: %v", err)
	}

	cleared, err := s.ClearMessages(chat.ID)
	if err != nil {
		t.Fatalf("очистка: %v", err)
	}
	if len(cleared.Messages) != 0 {
		t.Fatalf("история должна опустеть, получено %+v", cleared.Messages)
	}
	if !cleared.Config.JudgeEnabled {
		t.Fatal("настройки должны сохраниться при очистке истории")
	}
}

func TestHistorySkipsBlockedAndFailed(t *testing.T) {
	chat := Chat{Messages: []Message{
		{Role: "user", Kind: KindQuestion, Content: "вопрос"},
		{Role: "assistant", Kind: KindAnswer, Content: "ответ"},
		{Role: "user", Kind: KindQuestion, Content: "плохой вопрос"},
		{Role: "assistant", Kind: KindBlocked, Content: "запрос похож на подмену инструкций"},
		{Role: "assistant", Kind: KindFailed, Content: "провайдер вернул 500"},
	}}

	history := chat.History()
	if len(history) != 3 {
		t.Fatalf("в модель должны уходить только вопросы и ответы, получено %+v", history)
	}
	for _, message := range history {
		if message.Content == "провайдер вернул 500" {
			t.Fatal("сбои провайдера не должны попадать в контекст")
		}
	}
}

func TestSummaryCountsTotalCost(t *testing.T) {
	s, _ := Open("")
	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-v4-flash"))

	if _, err := s.Append(chat.ID,
		Message{Role: "assistant", Kind: KindAnswer, Meta: &Meta{TotalUSD: 0.001}},
		Message{Role: "assistant", Kind: KindAnswer, Meta: &Meta{TotalUSD: 0.002}},
	); err != nil {
		t.Fatalf("добавление: %v", err)
	}

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("ожидался один чат, получено %d", len(list))
	}
	if diff := list[0].TotalUSD - 0.003; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("сумма по чату посчитана неверно: %v", list[0].TotalUSD)
	}
}
