package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-sprout/internal/llm"
)

// fakeLLM -- подставной клиент моделей. Благодаря ему весь конвейер агента
// прогоняется без сети, без ключей и без денег.
//
// Вызов диспетчера отвечается отдельно от очереди responses: он случается на каждом
// ходе, и заставлять каждый тест про сжатие или судью выдумывать решение машины
// состояний значило бы утопить суть теста в подготовке.
type fakeLLM struct {
	calls     []llm.Request
	responses []llm.Response
	// routing -- чем отвечать на вызов диспетчера; пусто -- канонический сбор данных
	routing string
	// routerErr -- сбой именно диспетчера; err роняет только содержательные вызовы
	routerErr error
	err       error
}

// defaultRouting -- заявка «продолжаем сбор», которая ничего не меняет в состоянии.
const defaultRouting = `{"decision":"collect","answers":[],"reason":"тест"}`

func (f *fakeLLM) Chat(_ context.Context, _ llm.Provider, req llm.Request) (llm.Response, error) {
	f.calls = append(f.calls, req)

	if isRouterCall(req) {
		if f.routerErr != nil {
			return llm.Response{}, f.routerErr
		}
		routing := f.routing
		if routing == "" {
			routing = defaultRouting
		}
		return llm.Response{Text: routing, FinishReason: "stop"}, nil
	}

	if f.err != nil {
		return llm.Response{}, f.err
	}

	if len(f.responses) == 0 {
		return llm.Response{Text: "ответ", FinishReason: "stop"}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

// isRouterCall -- узнаём служебный вызов диспетчера по его промпту.
func isRouterCall(req llm.Request) bool {
	return len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "Ты — диспетчер")
}

// answerCalls -- вызовы, которые не были обращением к диспетчеру.
//
// Тестам про сжатие, судью и политики важен порядок содержательных вызовов,
// а диспетчер в нём только мешает.
func (f *fakeLLM) answerCalls() []llm.Request {
	calls := make([]llm.Request, 0, len(f.calls))
	for _, call := range f.calls {
		if !isRouterCall(call) {
			calls = append(calls, call)
		}
	}
	return calls
}

// timeFixture -- момент, на который считается стоимость во всех тестах:
// будний день, пиковый тариф.
func timeFixture() time.Time { return time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC) }

func newTestAgent(fake *fakeLLM) *Agent {
	return newToolAgent(fake, nil)
}

// newToolAgent -- агент с набором инструментов. Отдельный конструктор, чтобы
// десятки тестов, которым инструменты не нужны, не перечисляли nil.
func newToolAgent(fake *fakeLLM, tools ToolBox) *Agent {
	brain := New(fake, map[string]llm.Provider{
		llm.ProviderDeepSeek: llm.DeepSeek("test-key"),
	}, tools)
	// фиксированное время: расчёт стоимости не должен зависеть от того,
	// когда запускаются тесты
	brain.now = timeFixture
	return brain
}

func TestRunBlockedInputNeverCallsModel(t *testing.T) {
	fake := &fakeLLM{}
	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "",
		Config:   testConfig(),
	})

	var policyErr *PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("ожидалась PolicyError, получено %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("модель не должна вызываться при отказе входной политики, вызовов: %d", len(fake.calls))
	}
	if len(out.Trace) != 1 || out.Trace[0].Name != StepInputPolicy || out.Trace[0].OK {
		t.Fatalf("в трейсе должен быть один неуспешный шаг входной политики, получено %+v", out.Trace)
	}
}

func TestRunHappyPath(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{{
		Text:         "четыре",
		FinishReason: "stop",
		Usage: llm.Usage{
			PromptTokens:          3000,
			PromptCacheHitTokens:  1000,
			PromptCacheMissTokens: 2000,
			CompletionTokens:      500,
		},
	}}}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "сколько будет два плюс два?",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if out.Answer != "четыре" {
		t.Fatalf("ответ %q", out.Answer)
	}
	if out.Calls != 1 {
		t.Fatalf("содержательный вызов на ходе один, получено %d", out.Calls)
	}
	// входная политика, машина состояний, сборка контекста, вызов, выходная политика
	if len(out.Trace) != 5 {
		t.Fatalf("ожидались пять шагов трейса, получено %d: %+v", len(out.Trace), out.Trace)
	}
	// 1000*0.006 + 2000*0.3 + 500*1.2 = 1206 за миллион токенов, время пиковое
	if diff := out.Cost.USD - 0.001206; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("стоимость посчитана неверно: %v", out.Cost.USD)
	}
	if out.Cost.OffPeak {
		t.Fatal("02:00 UTC в понедельник -- пиковый тариф")
	}
}

func TestRunSendsDomainPromptAndWholeWindow(t *testing.T) {
	fake := &fakeLLM{}
	cfg := testConfig()
	cfg.HistoryDepth = 10

	history := []Message{
		{Role: llm.RoleUser, Content: "первый"},
		{Role: llm.RoleAssistant, Content: "первый ответ"},
		{Role: llm.RoleUser, Content: "второй"},
		{Role: llm.RoleAssistant, Content: "второй ответ"},
	}

	if _, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "третий",
		History:  history,
		Config:   cfg,
	}); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	sent := fake.answerCalls()[0].Messages

	// доменная роль всегда первая и не редактируется из настроек
	if sent[0].Role != llm.RoleSystem || !strings.Contains(sent[0].Content, "помощник начинающего строителя") {
		t.Fatalf("первым должен идти доменный промпт, получено %+v", sent[0])
	}

	// окно уезжает целиком: резать хвост агенту больше не нужно, оно приезжает
	// уже отрезанным по границе
	if sent[len(sent)-1].Content != "третий" {
		t.Fatalf("последним должен идти текущий вопрос, получено %q", sent[len(sent)-1].Content)
	}
	window := sent[len(sent)-5 : len(sent)-1]
	if window[0].Content != "первый" || window[3].Content != "второй ответ" {
		t.Fatalf("окно должно уехать целиком, получено %+v", window)
	}
}

func TestRunWithoutHistoryDepthSendsOnlyQuestion(t *testing.T) {
	fake := &fakeLLM{}
	cfg := testConfig()
	cfg.HistoryDepth = 0

	// нулевое окно -- памяти нет вовсе: ни сообщений, ни накопленного пересказа
	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "третий",
		History:  []Message{{Role: llm.RoleUser, Content: "первый"}},
		Summary:  "пересказ из прошлой жизни этого чата",
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	sent := fake.answerCalls()[0].Messages
	for _, message := range sent {
		if message.Role == llm.RoleUser && message.Content == "первый" {
			t.Fatalf("при нулевом окне истории быть не должно: %+v", sent)
		}
	}
	if out.Compaction != nil {
		t.Fatal("при нулевом окне сжимать нечего, отметка не нужна")
	}
}

func TestRunUnavailableModel(t *testing.T) {
	fake := &fakeLLM{}
	// провайдер OpenRouter не передан вовсе
	brain := New(fake, map[string]llm.Provider{llm.ProviderDeepSeek: llm.DeepSeek("test-key")}, nil)

	// конфиг берём целиком от этой модели: с чужим бюджетом вывода запрос упёрся бы
	// в окно контекста раньше, чем дошёл до провайдера
	cfg := DefaultConfig("liquid/lfm-2.5-2.6b:free")

	_, err := brain.Run(context.Background(), RunInput{Question: "вопрос", Config: cfg})

	var unavailable *ModelUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("ожидалась ModelUnavailableError, получено %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatal("недоступная модель не должна доходить до вызова")
	}
}

func TestRunReportsContextAndHistorySize(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{{
		Text:         "ответ",
		FinishReason: "stop",
		Usage:        llm.Usage{PromptTokens: 169, CompletionTokens: 121},
	}}}

	cfg := testConfig()
	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "третий вопрос",
		History: []Message{
			{Role: llm.RoleUser, Content: "первый"},
			{Role: llm.RoleAssistant, Content: "первый ответ"},
		},
		Config: cfg,
		Last:   LastTurn{Present: true, PromptTokens: 144, CompletionTokens: 329, ReasoningTokens: 324},
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if out.HistoryMessages != 2 {
		t.Fatalf("в запрос уехали два сообщения истории, посчитано %d", out.HistoryMessages)
	}
	// окно считается по числам прошлого вызова: 144 входа + 5 видимых токенов ответа
	if out.Context.Carried != 149 {
		t.Fatalf("перенос контекста посчитан неверно: %+v", out.Context)
	}
	if out.Context.LastPrompt != 144 {
		t.Fatalf("lastPrompt должен приехать из прошлого вызова, получено %d", out.Context.LastPrompt)
	}
}

func TestRunBlocksWhenContextWindowIsFull(t *testing.T) {
	fake := &fakeLLM{}
	cfg := testConfig()
	model, _ := FindModel(cfg.Model)

	_, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "ещё вопрос",
		Config:   cfg,
		Last:     LastTurn{Present: true, PromptTokens: model.ContextTokens},
	})

	var policyErr *PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("ожидалась PolicyError, получено %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatal("при переполненном окне вызова быть не должно")
	}
}
