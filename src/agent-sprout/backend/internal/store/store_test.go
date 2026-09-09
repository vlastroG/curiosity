package store

import (
	"errors"
	"path/filepath"
	"testing"

	"agent-sprout/internal/agent"
	"agent-sprout/internal/llm"
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

func TestHistoryKeepsOnlyCompletedExchanges(t *testing.T) {
	chat := Chat{Messages: []Message{
		{Role: "user", Kind: KindQuestion, Content: "вопрос"},
		{Role: "assistant", Kind: KindAnswer, Content: "ответ"},
		{Role: "user", Kind: KindQuestion, Content: "плохой вопрос"},
		{Role: "assistant", Kind: KindBlocked, Content: "запрос похож на подмену инструкций"},
		{Role: "assistant", Kind: KindFailed, Content: "провайдер вернул 500"},
	}}

	history := chat.History()
	if len(history) != 2 {
		t.Fatalf("в контекст уходит только состоявшийся обмен, получено %+v", history)
	}
	if history[0].Content != "вопрос" || history[1].Content != "ответ" {
		t.Fatalf("не тот обмен: %+v", history)
	}
}

func TestHistoryDropsQuestionThatOverflowedTheWindow(t *testing.T) {
	// иначе получается ловушка: не влезший запрос остаётся в истории, и каждая
	// следующая попытка тяжелее предыдущей
	chat := Chat{Messages: []Message{
		{Role: "user", Kind: KindQuestion, Content: "короткий вопрос"},
		{Role: "assistant", Kind: KindAnswer, Content: "короткий ответ"},
		{Role: "user", Kind: KindQuestion, Content: "простыня на сто тысяч символов"},
		{Role: "assistant", Kind: KindOverflow, Content: "не влез в окно контекста"},
	}}

	history := chat.History()
	if len(history) != 2 {
		t.Fatalf("непрошедший вопрос не должен утяжелять следующий запрос: %+v", history)
	}
	for _, message := range history {
		if message.Content == "простыня на сто тысяч символов" {
			t.Fatal("вопрос без ответа остался в контексте")
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

func TestFinishTurnAttachesInputToTheQuestion(t *testing.T) {
	s, _ := Open("")
	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-v4-flash"))

	if _, err := s.Append(chat.ID, Message{Role: "user", Kind: KindQuestion, Content: "вопрос"}); err != nil {
		t.Fatalf("добавление вопроса: %v", err)
	}

	updated, err := s.FinishTurn(chat.ID,
		&InputMeta{Tokens: 144, USD: 0.0001},
		Message{Role: "assistant", Kind: KindAnswer, Content: "ответ", Meta: &Meta{TotalUSD: 0.0005}},
	)
	if err != nil {
		t.Fatalf("закрытие хода: %v", err)
	}

	if len(updated.Messages) != 2 {
		t.Fatalf("в ленте должны быть вопрос и ответ, получено %d", len(updated.Messages))
	}
	// вход относится к вопросу, выход -- к ответу: в этом весь смысл разделения
	if updated.Messages[0].Input == nil || updated.Messages[0].Input.Tokens != 144 {
		t.Fatalf("метрики входа должны попасть вопросу: %+v", updated.Messages[0].Input)
	}
	if updated.Messages[0].Meta != nil {
		t.Fatal("у вопроса не должно быть метрик выхода")
	}
	if updated.Messages[1].Input != nil {
		t.Fatal("у ответа не должно быть метрик входа")
	}
}

func TestLastTurnUsesTheLatestRealCall(t *testing.T) {
	chat := Chat{Messages: []Message{
		{Kind: KindQuestion, Content: "первый"},
		{Kind: KindAnswer, Meta: &Meta{
			Usage:     llm.Usage{PromptTokens: 100, CompletionTokens: 50},
			Reasoning: 20,
		}},
		{Kind: KindQuestion, Content: "второй"},
		{Kind: KindAnswer, Meta: &Meta{
			Usage:     llm.Usage{PromptTokens: 180, CompletionTokens: 60},
			Reasoning: 40,
		}},
	}}

	last := chat.LastTurn()
	if !last.Present || last.PromptTokens != 180 {
		t.Fatalf("брать надо последний вызов: %+v", last)
	}
	// рассуждение в историю не уезжает, видимая часть -- 60-40
	if last.Visible() != 20 {
		t.Fatalf("видимая часть ответа 20 токенов, получено %d", last.Visible())
	}
}

func TestLastTurnIgnoresFailedCalls(t *testing.T) {
	// сбой провайдера токенов не потратил: usage пустой, окно контекста не сдвинулось
	chat := Chat{Messages: []Message{
		{Kind: KindQuestion, Content: "первый"},
		{Kind: KindAnswer, Meta: &Meta{Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 50}}},
		{Kind: KindQuestion, Content: "второй"},
		{Kind: KindFailed, Meta: &Meta{}},
	}}

	if last := chat.LastTurn(); last.PromptTokens != 100 {
		t.Fatalf("сбой не должен перебивать последний состоявшийся вызов: %+v", last)
	}
}

func TestLastTurnBlockedAnswerDoesNotCarryText(t *testing.T) {
	// выходная политика отклонила ответ: вызов состоялся и токены потрачены,
	// но текст в историю следующего запроса не уедет
	chat := Chat{Messages: []Message{
		{Kind: KindQuestion, Content: "вопрос"},
		{Kind: KindBlocked, Meta: &Meta{
			Usage:     llm.Usage{PromptTokens: 150, CompletionTokens: 400},
			Reasoning: 400,
		}},
	}}

	last := chat.LastTurn()
	if last.PromptTokens != 150 {
		t.Fatalf("вход состоявшегося вызова учитывается: %+v", last)
	}
	if last.Visible() != 0 {
		t.Fatalf("отклонённый ответ ничего не переносит, получено %d", last.Visible())
	}
}

func TestSummaryCountsInputAndOutputSeparately(t *testing.T) {
	s, _ := Open("")
	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-v4-flash"))

	if _, err := s.Append(chat.ID,
		Message{Kind: KindQuestion, Input: &InputMeta{Tokens: 144}},
		Message{Kind: KindAnswer, Meta: &Meta{Usage: llm.Usage{CompletionTokens: 329}, TotalUSD: 0.001}},
		Message{Kind: KindQuestion, Input: &InputMeta{Tokens: 169}},
		Message{Kind: KindAnswer, Meta: &Meta{Usage: llm.Usage{CompletionTokens: 121}, TotalUSD: 0.002}},
	); err != nil {
		t.Fatalf("добавление: %v", err)
	}

	list := s.List()[0]
	if list.TotalIn != 313 || list.TotalOut != 450 {
		t.Fatalf("суммы входа и выхода считаются раздельно: %+v", list)
	}
}
