package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-sprout/internal/llm"
)

// fakeLLM -- подставной клиент моделей. Благодаря ему весь конвейер агента
// прогоняется без сети, без ключей и без денег.
type fakeLLM struct {
	calls     []llm.Request
	responses []llm.Response
	err       error
}

func (f *fakeLLM) Chat(_ context.Context, _ llm.Provider, req llm.Request) (llm.Response, error) {
	f.calls = append(f.calls, req)
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

func newTestAgent(fake *fakeLLM) *Agent {
	brain := New(fake, map[string]llm.Provider{
		llm.ProviderDeepSeek: llm.DeepSeek("test-key"),
	})
	// фиксированное время: расчёт стоимости не должен зависеть от того,
	// когда запускаются тесты
	brain.now = func() time.Time { return time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC) }
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
		t.Fatalf("без судьи должен быть ровно один вызов, получено %d", out.Calls)
	}
	if len(out.Trace) != 4 {
		t.Fatalf("ожидались четыре шага трейса, получено %d: %+v", len(out.Trace), out.Trace)
	}
	// 1000*0.014 + 2000*0.44 + 500*1.32 = 1554 за миллион токенов, время пиковое
	if diff := out.Cost.USD - 0.001554; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("стоимость посчитана неверно: %v", out.Cost.USD)
	}
	if out.Cost.OffPeak {
		t.Fatal("02:00 UTC в понедельник -- пиковый тариф")
	}
}

func TestRunSendsSystemPromptAndTrimsHistory(t *testing.T) {
	fake := &fakeLLM{}
	cfg := testConfig()
	cfg.HistoryDepth = 2

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

	sent := fake.calls[0].Messages
	// system + два последних сообщения истории + текущий вопрос
	if len(sent) != 4 {
		t.Fatalf("ожидались 4 сообщения, отправлено %d: %+v", len(sent), sent)
	}
	if sent[0].Role != llm.RoleSystem {
		t.Fatalf("первым должен идти system, получено %q", sent[0].Role)
	}
	if sent[1].Content != "второй" || sent[2].Content != "второй ответ" {
		t.Fatalf("в контекст должен попасть хвост истории, получено %+v", sent[1:3])
	}
	if sent[3].Content != "третий" {
		t.Fatalf("последним должен идти текущий вопрос, получено %q", sent[3].Content)
	}
}

func TestRunWithoutHistoryDepthSendsOnlyQuestion(t *testing.T) {
	fake := &fakeLLM{}
	cfg := testConfig()
	cfg.HistoryDepth = 0

	if _, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "третий",
		History:  []Message{{Role: llm.RoleUser, Content: "первый"}},
		Config:   cfg,
	}); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if sent := fake.calls[0].Messages; len(sent) != 2 {
		t.Fatalf("при нулевой глубине истории должны уйти system и вопрос, получено %+v", sent)
	}
}

func TestRunWithJudgeMakesTwoCalls(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		{Text: "четыре", FinishReason: "stop"},
		{Text: `{"score": 5, "verdict": "точный ответ"}`, FinishReason: "stop"},
	}}

	cfg := testConfig()
	cfg.JudgeEnabled = true

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "сколько будет два плюс два?",
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if len(fake.calls) != 2 || out.Calls != 2 {
		t.Fatalf("с судьёй должно быть два вызова, получено %d", len(fake.calls))
	}
	if out.Judge == nil || out.Judge.Score != 5 {
		t.Fatalf("вердикт судьи не разобрался: %+v", out.Judge)
	}
	if !fake.calls[1].JSONObject {
		t.Fatal("судья должен запрашивать json_object")
	}
	if fake.calls[1].Temperature != 0 {
		t.Fatalf("судья должен работать на нулевой температуре, получено %v", fake.calls[1].Temperature)
	}
	if len(out.Trace) != 5 {
		t.Fatalf("с судьёй в трейсе пять шагов, получено %d", len(out.Trace))
	}
}

func TestRunJudgeFailureKeepsAnswer(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		{Text: "четыре", FinishReason: "stop"},
		{Text: "оценка: отлично", FinishReason: "stop"}, // не json
	}}

	cfg := testConfig()
	cfg.JudgeEnabled = true

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{Question: "вопрос", Config: cfg})
	if err != nil {
		t.Fatalf("сбой судьи не должен ронять ответ: %v", err)
	}
	if out.Answer != "четыре" {
		t.Fatalf("ответ должен сохраниться, получено %q", out.Answer)
	}
	if out.Judge != nil {
		t.Fatal("вердикта быть не должно")
	}
	if len(out.Warnings) == 0 {
		t.Fatal("сбой судьи должен попасть в предупреждения")
	}
}

func TestRunUnavailableModel(t *testing.T) {
	fake := &fakeLLM{}
	// провайдер OpenRouter не передан вовсе
	brain := New(fake, map[string]llm.Provider{llm.ProviderDeepSeek: llm.DeepSeek("test-key")})

	cfg := testConfig()
	cfg.Model = "liquid/lfm-2.5-2.6b:free"

	_, err := brain.Run(context.Background(), RunInput{Question: "вопрос", Config: cfg})

	var unavailable *ModelUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("ожидалась ModelUnavailableError, получено %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatal("недоступная модель не должна доходить до вызова")
	}
}

func TestParseVerdictExtractsObjectFromNoise(t *testing.T) {
	verdict, err := parseVerdict("Вот моя оценка:\n```json\n{\"score\": 4, \"verdict\": \"неплохо\"}\n```")
	if err != nil {
		t.Fatalf("объект должен вырезаться из обёртки: %v", err)
	}
	if verdict.Score != 4 || verdict.Verdict != "неплохо" {
		t.Fatalf("вердикт разобран неверно: %+v", verdict)
	}
}

func TestParseVerdictClampsScore(t *testing.T) {
	verdict, err := parseVerdict(`{"score": 9, "verdict": "восторг"}`)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if verdict.Score != 5 {
		t.Fatalf("оценка должна прижиматься к пятёрке, получено %d", verdict.Score)
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
