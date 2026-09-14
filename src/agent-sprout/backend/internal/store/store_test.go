package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"agent-sprout/internal/agent"
	"agent-sprout/internal/llm"
)

func TestSnapshotSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "chats.json")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("открытие пустого хранилища: %v", err)
	}

	cfg := agent.DefaultConfig("deepseek-flash")
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

	chat, err := s.Create("на удаление", agent.DefaultConfig("deepseek-flash"))
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

	cfg := agent.DefaultConfig("deepseek-flash")
	cfg.MaxInputChars = 1234
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
	if cleared.Config.MaxInputChars != 1234 {
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
	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-flash"))

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
	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-flash"))

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
	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-flash"))

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

func TestFinishTurnPutsBoundaryBeforeTheQuestion(t *testing.T) {
	s, _ := Open("")
	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-flash"))

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

	cfg := agent.DefaultConfig("deepseek-flash")
	source, _ := s.Create("исходный", cfg)

	if _, err := s.Append(source.ID,
		Message{Role: "user", Kind: KindQuestion, Content: "вопрос"},
		Message{Role: "assistant", Kind: KindAnswer, Content: "ответ"},
	); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if _, err := s.Update(source.ID, func(chat *Chat) error {
		chat.Tasks = []agent.Task{{
			ID:     "t1",
			Title:  "штукатурные работы",
			Status: agent.TaskCollecting,
			Requirements: []agent.Requirement{
				{Key: "основание", Question: "из чего стены?", Value: "кирпич"},
				{Key: "площадь", Question: "сколько квадратов?"},
			},
		}}
		return nil
	}); err != nil {
		t.Fatalf("подготовка рабочей памяти: %v", err)
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
	if len(branch.Messages) != 2 || len(branch.Tasks) != 1 {
		t.Fatalf("история и задачи должны переехать целиком: %+v", branch)
	}
	// рабочая память ветки -- своя копия: уточнения в одной ветке не видны в другой
	if _, err := s.Update(branch.ID, func(chat *Chat) error {
		chat.Tasks[0].Requirements[1].Value = "40 м2"
		return nil
	}); err != nil {
		t.Fatalf("уточнение в ветке: %v", err)
	}
	if origin, _ := s.Get(source.ID); origin.Tasks[0].Requirements[1].Value != "" {
		t.Fatalf("рабочая память веток не должна быть общей: %+v", origin.Tasks[0].Requirements)
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
	source, _ := s.Create("чат", agent.DefaultConfig("deepseek-flash"))

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
	root, _ := s.Create("чат", agent.DefaultConfig("deepseek-flash"))

	first, _ := s.Clone(root.ID, "ветка-1")
	second, err := s.Clone(first.ID, "ветка-1-1")
	if err != nil {
		t.Fatalf("ветка от ветки: %v", err)
	}

	if second.ParentID != first.ID {
		t.Fatalf("родителем должна быть ветка, а не корень: %+v", second)
	}
}

func TestActiveAndSolvedTasksAreSeparated(t *testing.T) {
	closed := time.Now()
	chat := Chat{Tasks: []agent.Task{
		{ID: "t1", Title: "монолит", Status: agent.TaskDone, Summary: "итог монолита", ClosedAt: &closed},
		{ID: "t2", Title: "брошенная", Status: agent.TaskCancelled, ClosedAt: &closed},
		{ID: "t3", Title: "штукатурка", Status: agent.TaskCollecting},
	}}

	active := chat.ActiveTask()
	if active == nil || active.ID != "t3" {
		t.Fatalf("активной должна быть незакрытая задача: %+v", active)
	}

	solved := chat.SolvedTasks()
	if len(solved) != 1 || solved[0].ID != "t1" {
		t.Fatalf("в память диалога идут только закрытые планом задачи: %+v", solved)
	}
}

func TestCancelTaskFreesWorkingMemory(t *testing.T) {
	s, _ := Open("")
	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-flash"))

	if _, err := s.Append(chat.ID, Message{Role: "user", Kind: KindQuestion, Content: "вопрос"}); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if _, err := s.Update(chat.ID, func(c *Chat) error {
		c.Tasks = []agent.Task{{ID: "t1", Title: "штукатурка", Status: agent.TaskCollecting}}
		return nil
	}); err != nil {
		t.Fatalf("подготовка задачи: %v", err)
	}

	updated, err := s.Update(chat.ID, func(c *Chat) error {
		if !c.CancelTask(time.Now()) {
			t.Fatal("активная задача должна была найтись")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("прерывание: %v", err)
	}

	if updated.ActiveTask() != nil {
		t.Fatal("после прерывания активной задачи быть не должно")
	}
	// история диалога при этом не страдает: прерывается задача, а не разговор
	if len(updated.Messages) != 1 {
		t.Fatalf("история должна остаться: %+v", updated.Messages)
	}
}

func TestClearMessagesWipesMemory(t *testing.T) {
	s, _ := Open("")

	cfg := agent.DefaultConfig("deepseek-flash")
	chat, _ := s.Create("чат", cfg)

	if _, err := s.Append(chat.ID,
		Message{Role: "system", Kind: KindSummary, Content: "пересказ"},
		Message{Role: "user", Kind: KindQuestion, Content: "вопрос"},
	); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if _, err := s.Update(chat.ID, func(c *Chat) error {
		c.Tasks = []agent.Task{{ID: "t1", Title: "штукатурка", Status: agent.TaskDone, Summary: "итог"}}
		return nil
	}); err != nil {
		t.Fatalf("подготовка памяти: %v", err)
	}

	cleared, err := s.ClearMessages(chat.ID)
	if err != nil {
		t.Fatalf("очистка: %v", err)
	}

	if len(cleared.Messages) != 0 || cleared.Tasks != nil {
		t.Fatalf("очистка должна стирать и ленту, и задачи: %+v", cleared)
	}
	// саммари уходит вместе с лентой, потому что хранится отметкой среди сообщений
	if window := cleared.Window(); window.Summary != "" {
		t.Fatalf("саммари должно уйти вместе с лентой: %q", window.Summary)
	}
	if cleared.Config.Model != cfg.Model {
		t.Fatal("настройки при очистке сохраняются")
	}
}

func TestApplyConfigRecomputesBudgetOnModelSwitch(t *testing.T) {
	// ровно тот случай, ради которого правило живёт в ApplyConfig: пользователь
	// переключает модель и больше ничего не трогает
	chat := Chat{Config: agent.DefaultConfig("deepseek-flash")}

	cfg := chat.Config
	cfg.Model = "liquid/lfm-2.5-2.6b:free"
	if err := chat.ApplyConfig(cfg); err != nil {
		t.Fatalf("смена модели не должна упираться в проверку старого бюджета: %v", err)
	}
	if want := agent.MaxTokensFor("liquid/lfm-2.5-2.6b:free"); chat.Config.MaxTokens != want {
		t.Fatalf("бюджет должен пересчитаться под новую модель: %d вместо %d",
			chat.Config.MaxTokens, want)
	}
}

func TestApplyConfigKeepsBudgetWhenModelStays(t *testing.T) {
	chat := Chat{Config: agent.DefaultConfig("deepseek-flash")}
	chat.Config.MaxTokens = 4096

	cfg := chat.Config
	cfg.Temperature = 0.2
	if err := chat.ApplyConfig(cfg); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if chat.Config.MaxTokens != 4096 {
		t.Fatalf("правка соседней настройки не должна трогать бюджет: %d", chat.Config.MaxTokens)
	}
}

func TestApplyConfigRejectsInvalid(t *testing.T) {
	chat := Chat{Config: agent.DefaultConfig("deepseek-flash")}

	cfg := chat.Config
	cfg.Temperature = 9
	if err := chat.ApplyConfig(cfg); err == nil {
		t.Fatal("невалидные настройки не должны попадать в чат")
	}
	if chat.Config.Temperature == 9 {
		t.Fatal("отклонённые настройки не должны присваиваться")
	}
}

func TestApplyConfigKeepsHandPickedBudgetAcrossModelSwitch(t *testing.T) {
	// бюджет теперь редактируемый, и молча выбрасывать введённое число нельзя:
	// переключение модели -- не повод забыть, что человек выставил руками
	chat := Chat{Config: agent.DefaultConfig("deepseek-flash")}
	chat.Config.MaxTokens = 40_000

	cfg := chat.Config
	cfg.Model = "deepseek-v4-pro"
	if err := chat.ApplyConfig(cfg); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if chat.Config.MaxTokens != 40_000 {
		t.Fatalf("введённое руками число должно пережить смену модели: %d", chat.Config.MaxTokens)
	}
}

func TestApplyConfigClampsHandPickedBudgetToNewModelCeiling(t *testing.T) {
	// а вот если оно в новую модель не влезает, выбор один: обрезать по её потолку,
	// иначе настройки не прошли бы проверку и смена модели просто не состоялась бы
	chat := Chat{Config: agent.DefaultConfig("deepseek-flash")}
	chat.Config.MaxTokens = 200_000

	cfg := chat.Config
	cfg.Model = "liquid/lfm-2.5-2.6b:free"
	if err := chat.ApplyConfig(cfg); err != nil {
		t.Fatalf("смена модели не должна упираться в чужой бюджет: %v", err)
	}
	free, _ := agent.FindModel("liquid/lfm-2.5-2.6b:free")
	if chat.Config.MaxTokens != free.MaxOutputTokens {
		t.Fatalf("бюджет должен обрезаться по потолку новой модели: %d", chat.Config.MaxTokens)
	}
}
