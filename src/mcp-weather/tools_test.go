package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// session поднимает сервер и подключает к нему настоящего клиента по транспорту
// в памяти.
//
// Дёргать хендлеры напрямую было бы проще, но тогда мимо проверки прошло бы всё
// интересное: регистрация инструментов, выведенные из структур схемы, валидация
// аргументов и превращение ошибки в результат с isError. Всё это делает SDK между
// клиентом и хендлером, и проверять надо именно это.
func session(t *testing.T, client *http.Client) *mcp.ClientSession {
	t.Helper()

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	server := newServer(client)
	go func() {
		if err := server.Run(ctx, serverTransport); err != nil {
			t.Errorf("сервер остановился: %v", err)
		}
	}()

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("соединение не установилось: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	return session
}

// call -- вызов инструмента с разбором того, что вернулось.
func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	result, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("вызов %s не прошёл по протоколу: %v", name, err)
	}
	return result
}

func text(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatal("в результате нет ни одного блока содержимого")
	}
	block, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("первый блок результата не текст, а %T", result.Content[0])
	}
	return block.Text
}

// Инструменты и их схемы -- то, что увидит модель, и то, по чему она решит,
// какой вызов собрать.
func TestListTools(t *testing.T) {
	client, _, _ := openMeteo(t, geocodeMoscow, forecastMoscow, http.StatusOK)

	list, err := session(t, client).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("список инструментов не пришёл: %v", err)
	}
	if len(list.Tools) != 2 {
		t.Fatalf("инструментов %d, ожидалось 2", len(list.Tools))
	}

	required := map[string][]string{
		"find_place":   {"name"},
		"get_forecast": {"place"},
	}
	for _, tool := range list.Tools {
		want, known := required[tool.Name]
		if !known {
			t.Fatalf("незнакомый инструмент %q", tool.Name)
		}
		if tool.Description == "" {
			t.Fatalf("у %s пустое описание: по нему модель решает, звать ли его", tool.Name)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("схема входа %s пришла как %T", tool.Name, tool.InputSchema)
		}
		if got := names(schema["required"]); !equal(got, want) {
			t.Fatalf("обязательные параметры %s: %v, ожидалось %v", tool.Name, got, want)
		}
		if _, ok := schema["properties"].(map[string]any); !ok {
			t.Fatalf("в схеме %s нет описания параметров", tool.Name)
		}
	}
}

func TestGetForecast(t *testing.T) {
	client, _, query := openMeteo(t, geocodeMoscow, forecastMoscow, http.StatusOK)

	result := call(t, session(t, client), "get_forecast", map[string]any{"place": "Москва", "days": 2})
	if result.IsError {
		t.Fatalf("вызов вернул ошибку: %s", text(t, result))
	}

	body := text(t, result)
	for _, want := range []string{"Москва", "+16.1 °C", "слабый дождь", "влажность 90%", "Для наружных работ"} {
		if !strings.Contains(body, want) {
			t.Fatalf("в тексте результата нет %q:\n%s", want, body)
		}
	}
	// текст пишет сервер, и он не должен превращаться в дамп json
	if strings.Contains(body, `"temperature"`) {
		t.Fatalf("в текстовый блок утёк json:\n%s", body)
	}
	if result.StructuredContent == nil {
		t.Fatal("нет structuredContent: числа должны приходить и машинно")
	}
	if got := query.Get("forecast_days"); got != "2" {
		t.Fatalf("forecast_days = %q, а просили 2", got)
	}
}

// Ноль в необязательном параметре означает «по умолчанию», а не «ноль суток».
func TestGetForecastDefaultDays(t *testing.T) {
	client, _, query := openMeteo(t, geocodeMoscow, forecastMoscow, http.StatusOK)

	call(t, session(t, client), "get_forecast", map[string]any{"place": "Москва"})

	if got := query.Get("forecast_days"); got != "3" {
		t.Fatalf("forecast_days = %q, ожидалось 3 по умолчанию", got)
	}
}

// Запрос за пределами разумного не отклоняется, а подрезается: семь суток
// полезнее отказа.
func TestGetForecastClampsDays(t *testing.T) {
	client, _, query := openMeteo(t, geocodeMoscow, forecastMoscow, http.StatusOK)

	call(t, session(t, client), "get_forecast", map[string]any{"place": "Москва", "days": 30})

	if got := query.Get("forecast_days"); got != "7" {
		t.Fatalf("forecast_days = %q, ожидалось 7", got)
	}
}

// Ненайденное место -- это результат с isError, а не сбой протокола: модель
// должна прочитать причину и переспросить человека.
func TestGetForecastPlaceNotFound(t *testing.T) {
	client, _, _ := openMeteo(t, geocodeNothing, forecastMoscow, http.StatusOK)

	result := call(t, session(t, client), "get_forecast", map[string]any{"place": "Тарабарск"})
	if !result.IsError {
		t.Fatal("ожидался результат с isError")
	}
	if body := text(t, result); !strings.Contains(body, "место не найдено") {
		t.Fatalf("причина не названа текстом: %q", body)
	}
}

func TestGetForecastEmptyPlace(t *testing.T) {
	client, _, _ := openMeteo(t, geocodeMoscow, forecastMoscow, http.StatusOK)

	result := call(t, session(t, client), "get_forecast", map[string]any{"place": "   "})
	if !result.IsError {
		t.Fatal("пустое место должно давать isError, а не запрос к геокодеру")
	}
}

// Обязательный параметр проверяет SDK по схеме -- до нашего кода дело не доходит.
func TestGetForecastMissingPlace(t *testing.T) {
	client, _, _ := openMeteo(t, geocodeMoscow, forecastMoscow, http.StatusOK)

	result := call(t, session(t, client), "get_forecast", map[string]any{"days": 3})
	if !result.IsError {
		t.Fatal("вызов без обязательного place должен отклоняться")
	}
}

func TestFindPlace(t *testing.T) {
	client, query, _ := openMeteo(t, geocodeMoscow, forecastMoscow, http.StatusOK)

	result := call(t, session(t, client), "find_place", map[string]any{"name": "Москва"})
	if result.IsError {
		t.Fatalf("вызов вернул ошибку: %s", text(t, result))
	}
	if body := text(t, result); !strings.Contains(body, "Москва, Россия") {
		t.Fatalf("в тексте нет разобранного места:\n%s", body)
	}
	if got := query.Get("count"); got != "5" {
		t.Fatalf("count = %q, ожидалось 5 по умолчанию", got)
	}
}

func TestFindPlaceClampsLimit(t *testing.T) {
	client, query, _ := openMeteo(t, geocodeMoscow, forecastMoscow, http.StatusOK)

	call(t, session(t, client), "find_place", map[string]any{"name": "Москва", "limit": 99})

	if got := query.Get("count"); got != "10" {
		t.Fatalf("count = %q, ожидалось 10", got)
	}
}

func names(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if name, ok := item.(string); ok {
			out = append(out, name)
		}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
