package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agent-sprout/internal/llm"
)

// Цикл вызова инструментов.
//
// Проверяется три вещи. Первая -- выключатель: инструменты не должны уезжать
// в запрос, если их выключили в чате, если модель им не обучена или если сервера
// нет вовсе. Вторая -- сам круг: заявка модели превращается в вызов, результат
// возвращается сообщением с ролью tool, и следующий запрос его видит. Третья --
// что ни одна неудача не отменяет ход: без погоды план хуже, но он есть.

// fakeTools -- подставной набор инструментов.
type fakeTools struct {
	// calls -- что у нас попросили вызвать: имя и аргументы
	calls [][2]string
	// result -- чем отвечать на вызов
	result string
	// err -- сбой вызова; definitionsErr -- сбой получения списка
	err            error
	definitionsErr error
}

func (f *fakeTools) Definitions(context.Context) ([]llm.Tool, error) {
	if f.definitionsErr != nil {
		return nil, f.definitionsErr
	}
	return []llm.Tool{{
		Name:        "get_forecast",
		Description: "погода в месте работ",
		Parameters:  map[string]any{"type": "object"},
	}}, nil
}

func (f *fakeTools) Call(_ context.Context, name, arguments string) (string, error) {
	f.calls = append(f.calls, [2]string{name, arguments})
	if f.err != nil {
		return "", f.err
	}
	if f.result == "" {
		return "Погода: Москва, +16.1 °C, слабый дождь", nil
	}
	return f.result, nil
}

// toolCall -- ответ модели с заявкой на вызов.
func toolCall(id, name, arguments string) llm.Response {
	return llm.Response{
		FinishReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID:       id,
			Type:     "function",
			Function: llm.ToolFunction{Name: name, Arguments: arguments},
		}},
	}
}

// toolRequests -- содержательные вызовы модели, в которых были инструменты.
func toolRequests(fake *fakeLLM) []llm.Request {
	var out []llm.Request
	for _, call := range fake.answerCalls() {
		if len(call.Tools) > 0 {
			out = append(out, call)
		}
	}
	return out
}

func TestToolsOfferedToModel(t *testing.T) {
	fake := &fakeLLM{}
	tools := &fakeTools{}

	out, err := newToolAgent(fake, tools).Run(context.Background(), RunInput{
		Question: "штукатурю фасад",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("ход: %v", err)
	}
	// правки стража в предупреждениях бывают и без инструментов -- нас
	// интересуют только те, что про них
	if hasWarning(out.Warnings, "инструмент") {
		t.Fatalf("предупреждение про инструменты на ровном месте: %v", out.Warnings)
	}

	calls := toolRequests(fake)
	if len(calls) != 1 {
		t.Fatalf("вызовов с инструментами %d, ожидался один", len(calls))
	}
	if calls[0].Tools[0].Name != "get_forecast" {
		t.Fatalf("инструмент уехал неверно: %+v", calls[0].Tools[0])
	}
	// диспетчеру инструменты не нужны: он занят разбором состояния
	for _, call := range fake.calls {
		if isRouterCall(call) && len(call.Tools) > 0 {
			t.Fatal("инструменты уехали диспетчеру")
		}
	}
}

// Выключатель. Все три условия равноправны, и каждое в одиночку отменяет
// инструменты целиком.
func TestToolsSwitchedOff(t *testing.T) {
	cases := []struct {
		name    string
		config  func(Config) Config
		tools   ToolBox
		refuses bool // ожидаем ли обращение к набору инструментов
	}{
		{
			name:   "выключено в настройках чата",
			config: func(c Config) Config { c.Weather = false; return c },
			tools:  &fakeTools{},
		},
		{
			name:   "модель не умеет звать инструменты",
			config: func(c Config) Config { c.Model = "liquid/lfm-2.5-2.6b:free"; return c },
			tools:  &fakeTools{},
		},
		{
			name:   "сервер инструментов не настроен",
			config: func(c Config) Config { return c },
			tools:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeLLM{}
			cfg := tc.config(testConfig())
			// у бесплатной модели свой бюджет вывода, иначе запрос упрётся в окно
			cfg.MaxTokens = MaxTokensFor(cfg.Model)

			brain := New(fake, map[string]llm.Provider{
				llm.ProviderDeepSeek:   llm.DeepSeek("test-key"),
				llm.ProviderOpenRouter: llm.OpenRouter("test-key", "", ""),
			}, tc.tools)

			if _, err := brain.Run(context.Background(), RunInput{
				Question: "штукатурю фасад",
				Config:   cfg,
			}); err != nil {
				t.Fatalf("ход: %v", err)
			}

			if calls := toolRequests(fake); len(calls) != 0 {
				t.Fatalf("инструменты уехали в запрос, хотя выключены: %d вызовов", len(calls))
			}
		})
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		toolCall("call_1", "get_forecast", `{"place":"Москва"}`),
		{Text: "план с учётом дождя", FinishReason: "stop"},
	}}
	tools := &fakeTools{result: "Погода: Москва, +16.1 °C, слабый дождь"}

	out, err := newToolAgent(fake, tools).Run(context.Background(), RunInput{
		Question: "штукатурю фасад",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("ход: %v", err)
	}
	if out.Answer != "план с учётом дождя" {
		t.Fatalf("ответ %q -- должен быть из вызова после инструмента", out.Answer)
	}

	// инструмент вызван ровно тем, что попросила модель
	if len(tools.calls) != 1 {
		t.Fatalf("вызовов инструмента %d, ожидался один", len(tools.calls))
	}
	if tools.calls[0][0] != "get_forecast" || tools.calls[0][1] != `{"place":"Москва"}` {
		t.Fatalf("заявка доехала искажённой: %v", tools.calls[0])
	}

	// второй запрос к модели несёт и заявку, и результат
	answers := fake.answerCalls()
	if len(answers) != 2 {
		t.Fatalf("содержательных вызовов %d, ожидалось два", len(answers))
	}
	messages := answers[1].Messages
	assistant := messages[len(messages)-2]
	result := messages[len(messages)-1]

	if assistant.Role != llm.RoleAssistant || len(assistant.ToolCalls) != 1 {
		t.Fatalf("заявка модели не уехала обратно: %+v", assistant)
	}
	if result.Role != llm.RoleTool {
		t.Fatalf("роль сообщения с результатом %q", result.Role)
	}
	if result.ToolCallID != "call_1" {
		t.Fatalf("результат не привязан к заявке: %q", result.ToolCallID)
	}
	if !strings.Contains(result.Content, "+16.1 °C") {
		t.Fatalf("результат инструмента потерян: %q", result.Content)
	}
}

// Вызов инструмента обязан быть виден так же, как вызов модели: это прямое
// обещание README про «видно, что происходит».
func TestToolCallInTrace(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		toolCall("call_1", "get_forecast", `{"place":"Москва"}`),
		{Text: "план", FinishReason: "stop"},
	}}

	out, err := newToolAgent(fake, &fakeTools{}).Run(context.Background(), RunInput{
		Question: "штукатурю фасад",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("ход: %v", err)
	}

	var step *Step
	llmSteps := 0
	for i := range out.Trace {
		switch out.Trace[i].Name {
		case StepTool:
			step = &out.Trace[i]
		case StepLLM:
			llmSteps++
		}
	}

	if step == nil {
		t.Fatal("в трейсе нет шага вызова инструмента")
	}
	if !step.OK {
		t.Fatalf("удачный вызов помечен неуспешным: %s", step.Detail)
	}
	for _, want := range []string{"get_forecast", "Москва"} {
		if !strings.Contains(step.Detail, want) {
			t.Fatalf("в детали шага нет %q: %s", want, step.Detail)
		}
	}
	if llmSteps != 2 {
		t.Fatalf("шагов вызова модели %d, ожидалось два -- до инструмента и после", llmSteps)
	}
}

// Токены. Круг с заявкой -- такой же служебный вызов, как диспетчер: в деньги
// входит, в числа хода нет. Сложить их значило бы показать вдвое больше занятого
// окна, чем занято на самом деле.
func TestToolRoundsAccounting(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		func() llm.Response {
			resp := toolCall("call_1", "get_forecast", `{"place":"Москва"}`)
			resp.Usage = llm.Usage{PromptTokens: 1000, CompletionTokens: 20}
			return resp
		}(),
		{Text: "план", FinishReason: "stop", Usage: llm.Usage{PromptTokens: 1300, CompletionTokens: 500}},
	}}

	out, err := newToolAgent(fake, &fakeTools{}).Run(context.Background(), RunInput{
		Question: "штукатурю фасад",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("ход: %v", err)
	}

	if out.Usage.PromptTokens != 1300 {
		t.Fatalf("prompt_tokens хода = %d, ожидались числа последнего вызова (1300)", out.Usage.PromptTokens)
	}
	if len(out.ToolRounds) != 1 {
		t.Fatalf("кругов с инструментами %d, ожидался один", len(out.ToolRounds))
	}
	if out.ToolRounds[0].Usage.PromptTokens != 1000 {
		t.Fatalf("числа круга потеряны: %+v", out.ToolRounds[0].Usage)
	}
	if out.Calls != 2 {
		t.Fatalf("вызовов модели в метриках %d, ожидалось два", out.Calls)
	}

	model, _ := FindModel(testConfig().Model)
	want := model.Cost(out.ToolRounds[0].Usage, timeFixture()).USD + out.Cost.USD
	if out.TotalUSD != want {
		t.Fatalf("сумма %v, ожидалась %v: круги оплачены, их нельзя терять", out.TotalUSD, want)
	}
}

// Сбой инструмента ход не отменяет: текст ошибки уходит модели, и дальше решает она.
func TestToolCallFailureDoesNotCancelTurn(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		toolCall("call_1", "get_forecast", `{"place":"Москва"}`),
		{Text: "план без погоды", FinishReason: "stop"},
	}}
	tools := &fakeTools{err: errors.New("сервер инструментов недоступен")}

	out, err := newToolAgent(fake, tools).Run(context.Background(), RunInput{
		Question: "штукатурю фасад",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("сбой инструмента отменил ход: %v", err)
	}
	if out.Answer != "план без погоды" {
		t.Fatalf("ответ %q", out.Answer)
	}

	answers := fake.answerCalls()
	result := answers[1].Messages[len(answers[1].Messages)-1]
	if !strings.Contains(result.Content, "инструмент не сработал") {
		t.Fatalf("модель не узнала о сбое: %q", result.Content)
	}
	if !strings.Contains(result.Content, "недоступен") {
		t.Fatalf("причина сбоя не доехала до модели: %q", result.Content)
	}

	for _, step := range out.Trace {
		if step.Name == StepTool && step.OK {
			t.Fatal("неудачный вызов помечен успешным")
		}
	}
}

// Отказ инструмента -- не сбой: «место не найдено» уходит модели слово в слово,
// без нашей приписки про поломку.
func TestToolRefusalPassedVerbatim(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		toolCall("call_1", "get_forecast", `{"place":"Тарабарск"}`),
		{Text: "уточните город", FinishReason: "stop"},
	}}
	tools := &fakeTools{err: &ToolRefusal{Text: `место не найдено: "Тарабарск"`}}

	out, err := newToolAgent(fake, tools).Run(context.Background(), RunInput{
		Question: "штукатурю фасад",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("отказ инструмента отменил ход: %v", err)
	}

	answers := fake.answerCalls()
	result := answers[1].Messages[len(answers[1].Messages)-1]
	if result.Content != `место не найдено: "Тарабарск"` {
		t.Fatalf("отказ доехал изменённым: %q", result.Content)
	}
	// шаг всё равно неуспешный: ответа по существу не получено
	for _, step := range out.Trace {
		if step.Name == StepTool && step.OK {
			t.Fatal("отказ помечен успешным вызовом")
		}
	}
	_ = out
}

// Недоступный сервер инструментов ход не отменяет: агент работает как до погоды.
func TestToolDefinitionsFailureDoesNotCancelTurn(t *testing.T) {
	fake := &fakeLLM{}
	tools := &fakeTools{definitionsErr: errors.New("соединение отклонено")}

	out, err := newToolAgent(fake, tools).Run(context.Background(), RunInput{
		Question: "штукатурю фасад",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("недоступный сервер отменил ход: %v", err)
	}
	if out.Answer == "" {
		t.Fatal("ответа нет")
	}
	if calls := toolRequests(fake); len(calls) != 0 {
		t.Fatal("инструменты уехали, хотя список не пришёл")
	}
	if !hasWarning(out.Warnings, "инструменты недоступны") {
		t.Fatalf("причина не попала в предупреждения: %v", out.Warnings)
	}
}

// Предохранитель: зациклившаяся на вызовах модель должна упереться в потолок,
// а последний вызов пройти без инструментов -- иначе ход закончится заявкой
// и пустым текстом, который отклонит выходная политика.
func TestToolRoundsCapped(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		toolCall("call_1", "get_forecast", `{"place":"Москва"}`),
		toolCall("call_2", "get_forecast", `{"place":"Москва"}`),
		toolCall("call_3", "get_forecast", `{"place":"Москва"}`),
		// четвёртого круга не будет: последний вызов уйдёт без инструментов,
		// и модели останется только ответить
		{Text: "всё-таки план", FinishReason: "stop"},
	}}
	tools := &fakeTools{}

	out, err := newToolAgent(fake, tools).Run(context.Background(), RunInput{
		Question: "штукатурю фасад",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("ход: %v", err)
	}

	if len(tools.calls) != maxToolRounds {
		t.Fatalf("вызовов инструмента %d, ожидалось %d", len(tools.calls), maxToolRounds)
	}

	answers := fake.answerCalls()
	if len(answers) != maxToolRounds+1 {
		t.Fatalf("вызовов модели %d, ожидалось %d", len(answers), maxToolRounds+1)
	}
	if len(answers[len(answers)-1].Tools) != 0 {
		t.Fatal("последний вызов ушёл с инструментами -- ход рискует закончиться заявкой")
	}
	if out.Answer != "всё-таки план" {
		t.Fatalf("ответ %q", out.Answer)
	}
	if !hasWarning(out.Warnings, "ходила к инструментам") {
		t.Fatalf("упор в потолок не попал в предупреждения: %v", out.Warnings)
	}
}

// Заявка на инструменты, которых мы не давали, не должна вешать ход в цикле.
func TestUnexpectedToolCallsDoNotLoop(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		toolCall("call_1", "get_forecast", `{"place":"Москва"}`),
	}}

	// инструментов у агента нет вовсе, а модель всё равно прислала заявку
	out, err := newToolAgent(fake, nil).Run(context.Background(), RunInput{
		Question: "штукатурю фасад",
		Config:   testConfig(),
	})
	// заявку исполнять нечем, ответа нет -- ход честно отклоняется выходной
	// политикой, а не крутится по кругу
	if err == nil {
		t.Fatalf("ожидалась ошибка, получен ответ %q", out.Answer)
	}
	if len(fake.answerCalls()) != 1 {
		t.Fatalf("вызовов модели %d, ожидался один", len(fake.answerCalls()))
	}
}

func hasWarning(warnings []string, substring string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}
