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

func TestWindowKeepsOnlyCompletedExchanges(t *testing.T) {
	chat := Chat{Messages: []Message{
		{Role: "user", Kind: KindQuestion, Content: "вопрос"},
		{Role: "assistant", Kind: KindAnswer, Content: "ответ"},
		{Role: "user", Kind: KindQuestion, Content: "плохой вопрос"},
		{Role: "assistant", Kind: KindBlocked, Content: "запрос похож на подмену инструкций"},
		{Role: "assistant", Kind: KindFailed, Content: "провайдер вернул 500"},
	}}

	history := chat.Window().Messages
	if len(history) != 2 {
		t.Fatalf("в контекст уходит только состоявшийся обмен, получено %+v", history)
	}
	if history[0].Content != "вопрос" || history[1].Content != "ответ" {
		t.Fatalf("не тот обмен: %+v", history)
	}
}

func TestWindowDropsQuestionThatOverflowedTheWindow(t *testing.T) {
	// иначе получается ловушка: не влезший запрос остаётся в истории, и каждая
	// следующая попытка тяжелее предыдущей
	chat := Chat{Messages: []Message{
		{Role: "user", Kind: KindQuestion, Content: "короткий вопрос"},
		{Role: "assistant", Kind: KindAnswer, Content: "короткий ответ"},
		{Role: "user", Kind: KindQuestion, Content: "простыня на сто тысяч символов"},
		{Role: "assistant", Kind: KindOverflow, Content: "не влез в окно контекста"},
	}}

	history := chat.Window().Messages
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

	updated, err := s.FinishTurn(chat.ID, Turn{
		Input:  &InputMeta{Tokens: 144, USD: 0.0001},
		Answer: Message{Role: "assistant", Kind: KindAnswer, Content: "ответ", Meta: &Meta{TotalUSD: 0.0005}},
	})
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

func TestWindowStartsAfterTheLastBoundary(t *testing.T) {
	chat := Chat{Messages: []Message{
		{Role: "user", Kind: KindQuestion, Content: "старый вопрос"},
		{Role: "assistant", Kind: KindAnswer, Content: "старый ответ"},
		{Role: "system", Kind: KindSummary, Content: "пересказ первого окна"},
		{Role: "user", Kind: KindQuestion, Content: "новый вопрос"},
		{Role: "assistant", Kind: KindAnswer, Content: "новый ответ"},
	}}

	window := chat.Window()
	if window.Summary != "пересказ первого окна" {
		t.Fatalf("пересказ должен приехать из отметки: %q", window.Summary)
	}
	if len(window.Messages) != 2 || window.Messages[0].Content != "новый вопрос" {
		t.Fatalf("в окно попадает только хвост после отметки: %+v", window.Messages)
	}
	if window.Compactions != 1 {
		t.Fatalf("сжатие было одно, посчитано %d", window.Compactions)
	}
}

func TestWindowKeepsSummaryAcrossDroppedBoundary(t *testing.T) {
	// сжатие выключили посреди чата: окно закрылось отбрасыванием, но память,
	// накопленную раньше, это стирать не должно
	chat := Chat{Messages: []Message{
		{Role: "system", Kind: KindSummary, Content: "пересказ первого окна"},
		{Role: "user", Kind: KindQuestion, Content: "вопрос второго окна"},
		{Role: "assistant", Kind: KindAnswer, Content: "ответ второго окна"},
		{Role: "system", Kind: KindDropped},
		{Role: "user", Kind: KindQuestion, Content: "вопрос третьего окна"},
		{Role: "assistant", Kind: KindAnswer, Content: "ответ третьего окна"},
	}}

	window := chat.Window()
	if window.Summary != "пересказ первого окна" {
		t.Fatalf("отбрасывание не должно стирать пересказ: %q", window.Summary)
	}
	if len(window.Messages) != 2 || window.Messages[0].Content != "вопрос третьего окна" {
		t.Fatalf("окно должно начинаться после отбрасывания: %+v", window.Messages)
	}
	if window.Compactions != 2 {
		t.Fatalf("границ было две, посчитано %d", window.Compactions)
	}
}

func TestFinishTurnPutsBoundaryBeforeTheQuestion(t *testing.T) {
	s, _ := Open("")
	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-v4-flash"))

	if _, err := s.Append(chat.ID,
		Message{Role: "user", Kind: KindQuestion, Content: "старый вопрос"},
		Message{Role: "assistant", Kind: KindAnswer, Content: "старый ответ"},
		Message{Role: "user", Kind: KindQuestion, Content: "вопрос, вызвавший сжатие"},
	); err != nil {
		t.Fatalf("подготовка: %v", err)
	}

	updated, err := s.FinishTurn(chat.ID, Turn{
		Boundary: &Message{Role: "system", Kind: KindSummary, Content: "пересказ"},
		Input:    &InputMeta{Tokens: 100},
		Answer:   Message{Role: "assistant", Kind: KindAnswer, Content: "ответ"},
	})
	if err != nil {
		t.Fatalf("закрытие хода: %v", err)
	}

	kinds := make([]string, 0, len(updated.Messages))
	for _, message := range updated.Messages {
		kinds = append(kinds, message.Kind)
	}
	want := []string{KindQuestion, KindAnswer, KindSummary, KindQuestion, KindAnswer}
	if len(kinds) != len(want) {
		t.Fatalf("ожидалось %v, получено %v", want, kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("порядок сообщений: ожидалось %v, получено %v", want, kinds)
		}
	}

	// граница проходит перед вопросом, значит в окно попадает сам вопрос и ответ
	if window := updated.Window(); len(window.Messages) != 2 {
		t.Fatalf("после сжатия окно -- это текущий обмен, получено %+v", window.Messages)
	}
}

func TestLastTurnSkipsCompactionCalls(t *testing.T) {
	// служебный вызов сжатия не должен подменять собой размер диалога
	chat := Chat{Messages: []Message{
		{Kind: KindQuestion},
		{Kind: KindAnswer, Meta: &Meta{Usage: llm.Usage{PromptTokens: 900, CompletionTokens: 100}}},
		{Kind: KindSummary, Meta: &Meta{Usage: llm.Usage{PromptTokens: 1200, CompletionTokens: 200}}},
	}}

	if last := chat.LastTurn(); last.PromptTokens != 900 {
		t.Fatalf("окно контекста считается по основному вызову: %+v", last)
	}
}

func TestCloneMakesAnIndependentBranch(t *testing.T) {
	s, _ := Open("")

	cfg := agent.DefaultConfig("deepseek-v4-flash")
	cfg.StickyFacts = true
	source, _ := s.Create("исходный", cfg)

	if _, err := s.Append(source.ID,
		Message{Role: "user", Kind: KindQuestion, Content: "вопрос"},
		Message{Role: "assistant", Kind: KindAnswer, Content: "ответ"},
	); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if _, err := s.Update(source.ID, func(chat *Chat) error {
		chat.Facts = []agent.Fact{{Key: "имя", Value: "Влад"}}
		return nil
	}); err != nil {
		t.Fatalf("подготовка памяти: %v", err)
	}

	branch, err := s.Clone(source.ID, "ветка-а")
	if err != nil {
		t.Fatalf("клонирование: %v", err)
	}

	if branch.ID == source.ID {
		t.Fatal("у ветки должен быть свой идентификатор")
	}
	if branch.Title != "исходный" {
		t.Fatalf("название наследуется от родителя, получено %q", branch.Title)
	}
	if branch.Tag != "ветка-а" || branch.ClonedAt == nil || branch.ParentID != source.ID {
		t.Fatalf("метки ветки не проставлены: %+v", branch)
	}
	if len(branch.Messages) != 2 || len(branch.Facts) != 1 {
		t.Fatalf("история и память должны переехать целиком: %+v", branch)
	}
	if !branch.Config.StickyFacts {
		t.Fatal("настройки должны переехать целиком")
	}

	// ветки независимы: дописанное в одну не появляется в другой
	if _, err := s.Append(branch.ID, Message{Role: "user", Kind: KindQuestion, Content: "только в ветке"}); err != nil {
		t.Fatalf("дописывание в ветку: %v", err)
	}

	original, _ := s.Get(source.ID)
	if len(original.Messages) != 2 {
		t.Fatalf("исходный чат не должен меняться: %+v", original.Messages)
	}
	if original.Tag != "" || original.ClonedAt != nil {
		t.Fatalf("родитель не становится веткой: %+v", original)
	}
}

func TestCloneRejectsDuplicateTag(t *testing.T) {
	s, _ := Open("")
	source, _ := s.Create("чат", agent.DefaultConfig("deepseek-v4-flash"))

	if _, err := s.Clone(source.ID, "ветка"); err != nil {
		t.Fatalf("первый чекпоинт: %v", err)
	}

	// регистр не спасает: тэги различают глазами, «Ветка» и «ветка» перепутаются
	if _, err := s.Clone(source.ID, "Ветка"); !errors.Is(err, ErrTagTaken) {
		t.Fatalf("ожидалась ErrTagTaken, получено %v", err)
	}
	if list := s.List(); len(list) != 2 {
		t.Fatalf("занятый тэг не должен создавать чат, чатов: %d", len(list))
	}
}

func TestCloneOfCloneKeepsLineage(t *testing.T) {
	s, _ := Open("")
	root, _ := s.Create("чат", agent.DefaultConfig("deepseek-v4-flash"))

	first, _ := s.Clone(root.ID, "ветка-1")
	second, err := s.Clone(first.ID, "ветка-1-1")
	if err != nil {
		t.Fatalf("ветка от ветки: %v", err)
	}

	if second.ParentID != first.ID {
		t.Fatalf("родителем должна быть ветка, а не корень: %+v", second)
	}
}

func TestApplyConfigClearsFactsWhenTurnedOff(t *testing.T) {
	cfg := agent.DefaultConfig("deepseek-v4-flash")
	cfg.StickyFacts = true

	chat := Chat{Config: cfg, Facts: []agent.Fact{{Key: "имя", Value: "Влад"}}}

	// правка, не касающаяся фактов, память не трогает
	other := cfg
	other.Temperature = 1.2
	chat.ApplyConfig(other)
	if len(chat.Facts) != 1 {
		t.Fatalf("посторонняя настройка не должна стирать память: %+v", chat.Facts)
	}

	// снятая галочка -- это и есть способ сбросить память
	off := other
	off.StickyFacts = false
	chat.ApplyConfig(off)
	if chat.Facts != nil {
		t.Fatalf("снятая галочка должна стереть память: %+v", chat.Facts)
	}
}

func TestClearMessagesWipesMemory(t *testing.T) {
	s, _ := Open("")

	cfg := agent.DefaultConfig("deepseek-v4-flash")
	cfg.StickyFacts = true
	chat, _ := s.Create("чат", cfg)

	if _, err := s.Append(chat.ID,
		Message{Role: "system", Kind: KindSummary, Content: "пересказ"},
		Message{Role: "user", Kind: KindQuestion, Content: "вопрос"},
	); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if _, err := s.Update(chat.ID, func(c *Chat) error {
		c.Facts = []agent.Fact{{Key: "имя", Value: "Влад"}}
		return nil
	}); err != nil {
		t.Fatalf("подготовка памяти: %v", err)
	}

	cleared, err := s.ClearMessages(chat.ID)
	if err != nil {
		t.Fatalf("очистка: %v", err)
	}

	if len(cleared.Messages) != 0 || cleared.Facts != nil {
		t.Fatalf("очистка должна стирать и ленту, и память: %+v", cleared)
	}
	// саммари уходит вместе с лентой, потому что хранится отметкой среди сообщений
	if window := cleared.Window(); window.Summary != "" {
		t.Fatalf("саммари должно уйти вместе с лентой: %q", window.Summary)
	}
	if !cleared.Config.StickyFacts {
		t.Fatal("настройки при очистке сохраняются")
	}
}
