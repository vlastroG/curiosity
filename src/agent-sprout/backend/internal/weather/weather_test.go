package weather

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"agent-sprout/internal/agent"
)

// На той стороне поднимается настоящий MCP-сервер в той же конфигурации, что
// и в бою: без сессий и с json-ответами. Подставной json по проводу проверял бы
// наши же представления о протоколе, а не совместимость с ним -- а именно
// совместимость здесь и хрупкая: клиент ходит без постоянного потока событий
// именно потому, что stateless-сервер отвечает на GET отказом.

type forecastInput struct {
	Place string `json:"place" jsonschema:"место работ"`
	Days  int    `json:"days,omitempty" jsonschema:"на сколько суток вперёд"`
}

type forecastOutput struct {
	Place string `json:"place"`
}

// server поднимает MCP-сервер с одним инструментом и возвращает его адрес
// вместе со счётчиком HTTP-запросов.
func server(t *testing.T) (string, *atomic.Int64) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "тестовый", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_forecast",
		Description: "погода в месте работ",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in forecastInput) (*mcp.CallToolResult, forecastOutput, error) {
		if in.Place == "Тарабарск" {
			return nil, forecastOutput{}, fmt.Errorf("место не найдено: %q", in.Place)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("Погода: %s, дней %d", in.Place, in.Days),
			}},
		}, forecastOutput{Place: in.Place}, nil
	})

	var requests atomic.Int64
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)

	return httpServer.URL, &requests
}

func TestDefinitions(t *testing.T) {
	endpoint, _ := server(t)

	tools, err := New(endpoint, 5*time.Second).Definitions(context.Background())
	if err != nil {
		t.Fatalf("список инструментов: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("инструментов %d, ожидался один", len(tools))
	}

	tool := tools[0]
	if tool.Name != "get_forecast" || tool.Description != "погода в месте работ" {
		t.Fatalf("инструмент переведён неверно: %+v", tool)
	}
	// схема доезжает целиком: её писал сервер, а читать будет модель
	if tool.Parameters["type"] != "object" {
		t.Fatalf("схема аргументов: %#v", tool.Parameters)
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok || properties["place"] == nil {
		t.Fatalf("описание параметров потеряно: %#v", tool.Parameters)
	}
}

// Список инструментов не меняется, а лишний поход к серверу на каждом ходе --
// это секунда ожидания человеком.
func TestDefinitionsCached(t *testing.T) {
	endpoint, requests := server(t)
	box := New(endpoint, 5*time.Second)

	if _, err := box.Definitions(context.Background()); err != nil {
		t.Fatalf("первый запрос: %v", err)
	}
	after := requests.Load()

	if _, err := box.Definitions(context.Background()); err != nil {
		t.Fatalf("второй запрос: %v", err)
	}
	if requests.Load() != after {
		t.Fatalf("второй вызов сходил на сервер: запросов было %d, стало %d", after, requests.Load())
	}
}

func TestCall(t *testing.T) {
	endpoint, _ := server(t)

	text, err := New(endpoint, 5*time.Second).
		Call(context.Background(), "get_forecast", `{"place":"Москва","days":3}`)
	if err != nil {
		t.Fatalf("вызов: %v", err)
	}
	if text != "Погода: Москва, дней 3" {
		t.Fatalf("результат доехал искажённым: %q", text)
	}
}

// Отказ инструмента -- содержательный ответ, а не поломка: агент должен получить
// его отдельным типом, чтобы отдать модели текст без приписки про сбой.
func TestCallRefusal(t *testing.T) {
	endpoint, _ := server(t)

	_, err := New(endpoint, 5*time.Second).
		Call(context.Background(), "get_forecast", `{"place":"Тарабарск"}`)

	var refusal *agent.ToolRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("ошибка %v (%T), ожидался ToolRefusal", err, err)
	}
	if !strings.Contains(refusal.Text, "место не найдено") {
		t.Fatalf("причина отказа потеряна: %q", refusal.Text)
	}
}

// Несуществующий инструмент -- это отказ протокольного уровня, и он тоже должен
// доехать до модели текстом, а не уронить ход.
func TestCallUnknownTool(t *testing.T) {
	endpoint, _ := server(t)

	_, err := New(endpoint, 5*time.Second).Call(context.Background(), "get_gold_price", `{}`)
	if err == nil {
		t.Fatal("вызов несуществующего инструмента прошёл успешно")
	}
}

// Модель вправе прислать не json. Причина должна быть внятной и не стоить похода
// к серверу: модель прочитает её и соберёт вызов заново.
func TestCallBrokenArguments(t *testing.T) {
	endpoint, _ := server(t)

	_, err := New(endpoint, 5*time.Second).Call(context.Background(), "get_forecast", `{place: Москва`)
	if err == nil {
		t.Fatal("битые аргументы уехали на сервер")
	}
	if !strings.Contains(err.Error(), "не разобрались как json") {
		t.Fatalf("причина невнятная: %v", err)
	}
}

// Недоступный сервер -- обычное дело: контейнер перезапускается, сеть моргает.
// Обе операции обязаны вернуть ошибку, а не подвиснуть и не отдать пустоту.
func TestServerUnavailable(t *testing.T) {
	// поднимаем и сразу гасим: адрес есть, слушать его некому
	closed := httptest.NewServer(http.NotFoundHandler())
	endpoint := closed.URL
	closed.Close()

	box := New(endpoint, time.Second)

	if _, err := box.Definitions(context.Background()); err == nil {
		t.Fatal("ожидалась ошибка соединения")
	}
	if _, err := box.Call(context.Background(), "get_forecast", `{"place":"Москва"}`); err == nil {
		t.Fatal("ожидалась ошибка соединения")
	}
}

// Сервер жив, но по этому адресу не MCP. Ошибка должна быть внятной, а не
// молчаливым пустым списком инструментов.
func TestEndpointIsNotMCP(t *testing.T) {
	stranger := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(stranger.Close)

	if _, err := New(stranger.URL, time.Second).Definitions(context.Background()); err == nil {
		t.Fatal("чужой сервер принят за сервер инструментов")
	}
}
