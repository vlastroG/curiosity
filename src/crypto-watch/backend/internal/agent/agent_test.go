package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"crypto-watch/internal/llm"
)

// fakeTools -- MCP-сервер в памяти: отвечает сводкой и запоминает вызовы.
type fakeTools struct {
	coins []map[string]any
	calls []string
	saved map[string]any
}

func (f *fakeTools) Tools(context.Context, ...string) ([]llm.Tool, error) {
	return []llm.Tool{{Name: "crypto_summary", Parameters: map[string]any{"type": "object"}}}, nil
}

func (f *fakeTools) Text(_ context.Context, name, args string) (string, error) {
	f.calls = append(f.calls, name+" "+args)
	return "BTC: сейчас 100 $, за окно +1%", nil
}

func (f *fakeTools) Call(_ context.Context, name string, args any, out any) error {
	f.calls = append(f.calls, name)
	var payload any = map[string]any{"coins": f.coins}
	if name == "save_forecast" {
		f.saved = args.(map[string]any)
		payload = map[string]any{"id": 7}
	}
	raw, _ := json.Marshal(payload)
	return json.Unmarshal(raw, out)
}

// fakeChat отдаёт заготовленные ответы по очереди и запоминает запросы.
type fakeChat struct {
	answers  []llm.Response
	requests []llm.Request
}

func (f *fakeChat) Chat(_ context.Context, _ llm.Provider, req llm.Request) (llm.Response, error) {
	f.requests = append(f.requests, req)
	answer := f.answers[0]
	f.answers = f.answers[1:]
	return answer, nil
}

func toolCall(name, args string) llm.Response {
	var call llm.ToolCall
	call.ID = "call_1"
	call.Type = "function"
	call.Function.Name = name
	call.Function.Arguments = args
	return llm.Response{ToolCalls: []llm.ToolCall{call}}
}

const answer = "Вот прогноз:\n```json\n" +
	`{"overview":"BTC растёт.","coins":[{"symbol":"btc","trend":"UP","low":105,"high":99,"confidence":1.7,"reason":"рост"},{"symbol":"XRP","trend":"up","low":1,"high":2,"confidence":0.5,"reason":"выдумка"}]}` +
	"\n```"

func forecaster(chat *fakeChat, tools *fakeTools) *Forecaster {
	providers := map[string]llm.Provider{llm.ProviderOpenRouter: {Title: "OR", APIKey: "k"}}
	return New(chat, providers, tools, 30)
}

var toolModel = Model{ID: "free", Provider: llm.ProviderOpenRouter, Tools: true}

func TestRunWithTools(t *testing.T) {
	tools := &fakeTools{coins: []map[string]any{{"symbol": "BTC", "last": 101.5, "points": 10}}}
	chat := &fakeChat{answers: []llm.Response{
		toolCall("crypto_summary", `{"minutes":60}`),
		{Text: answer},
	}}
	f := forecaster(chat, tools)

	result, err := f.Run(context.Background(), toolModel, 60)
	if err != nil {
		t.Fatal(err)
	}

	if len(chat.requests[0].Tools) != 1 {
		t.Error("модели не отдали инструменты")
	}
	// второй запрос несёт заявку модели и ответ инструмента
	second := chat.requests[1].Messages
	if last := second[len(second)-1]; last.Role != llm.RoleTool || last.ToolCallID != "call_1" || !strings.Contains(last.Content, "BTC") {
		t.Errorf("ответ инструмента ушёл модели как %+v", last)
	}
	if len(result.Steps) != 1 || result.Steps[0].Tool != "crypto_summary" || result.Mode != "tools" {
		t.Errorf("шаги: %+v, режим %s", result.Steps, result.Mode)
	}

	// выдуманная монета отброшена, остальное приведено в порядок
	if len(result.Coins) != 1 {
		t.Fatalf("монеты: %+v", result.Coins)
	}
	coin := result.Coins[0]
	if coin.Symbol != "BTC" || coin.Trend != "up" || coin.Low != 99 || coin.High != 105 || coin.Confidence != 1 || coin.Price != 101.5 {
		t.Errorf("монета: %+v", coin)
	}
	if result.Overview != "BTC растёт." || result.HorizonMinutes != 30 || result.WindowMinutes != 60 {
		t.Errorf("прогноз: %+v", result)
	}

	id, err := f.Save(context.Background(), result)
	if err != nil || id != 7 {
		t.Fatalf("сохранение: %d, %v", id, err)
	}
	var body Result
	if err := json.Unmarshal([]byte(tools.saved["body"].(string)), &body); err != nil || body.Coins[0].Symbol != "BTC" {
		t.Errorf("в save_forecast ушло %v", tools.saved)
	}
}

// Модель ответила, не заглянув в данные, -- её просят посмотреть.
func TestRunNudgesToData(t *testing.T) {
	tools := &fakeTools{coins: []map[string]any{{"symbol": "BTC", "last": 100}}}
	chat := &fakeChat{answers: []llm.Response{
		{Text: answer},
		toolCall("crypto_summary", `{"minutes":60}`),
		{Text: answer},
	}}
	result, err := forecaster(chat, tools).Run(context.Background(), toolModel, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.requests) != 3 || len(result.Steps) != 1 {
		t.Errorf("запросов %d, шагов %d", len(chat.requests), len(result.Steps))
	}
}

// Ответ не по формату -- одна повторная попытка, потом ошибка.
func TestRunBadFormat(t *testing.T) {
	tools := &fakeTools{coins: []map[string]any{{"symbol": "BTC", "last": 100}}}
	chat := &fakeChat{answers: []llm.Response{{Text: "не знаю"}, {Text: answer}}}
	model := Model{ID: "plain", Provider: llm.ProviderOpenRouter}

	result, err := forecaster(chat, tools).Run(context.Background(), model, 60)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "prompt" || len(chat.requests[0].Tools) != 0 {
		t.Errorf("модель без инструментов: режим %s", result.Mode)
	}
	if !strings.Contains(chat.requests[0].Messages[1].Content, "BTC: сейчас 100 $") {
		t.Error("данные не попали в промпт")
	}

	chat = &fakeChat{answers: []llm.Response{{Text: "не знаю"}, {Text: "{плохо}"}}}
	if _, err := forecaster(chat, tools).Run(context.Background(), model, 60); err == nil {
		t.Error("два ответа не по формату прошли")
	}
}

func TestRunNoData(t *testing.T) {
	chat := &fakeChat{}
	_, err := forecaster(chat, &fakeTools{}).Run(context.Background(), toolModel, 60)
	if err != ErrNoData || len(chat.requests) != 0 {
		t.Errorf("без данных: %v, запросов к модели %d", err, len(chat.requests))
	}
}

func TestRunNoKey(t *testing.T) {
	deepseek := Model{ID: "deepseek-flash", Provider: llm.ProviderDeepSeek}
	if _, err := forecaster(&fakeChat{}, &fakeTools{}).Run(context.Background(), deepseek, 60); err == nil {
		t.Error("модель без ключа запустилась")
	}
}

func TestCatalog(t *testing.T) {
	model, ok := FindModel(DefaultModel)
	if !ok || !model.Free {
		t.Errorf("модель по умолчанию должна быть в каталоге и бесплатной: %+v", model)
	}
}
