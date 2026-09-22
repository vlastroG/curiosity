// Package weather -- клиент MCP-сервера погоды.
//
// Прослойка между агентом и протоколом. Агент знает только интерфейс ToolBox:
// «дай список инструментов», «исполни вызов». Что за ними MCP, HTTP и чужой
// сервис -- его не касается, как не касается его и то, какие именно инструменты
// там окажутся: имена, описания и схемы приходят от сервера и уезжают модели
// как есть. Захочет сервер завтра отдавать третий инструмент -- здесь править
// нечего.
//
//	agent.ToolBox
//	    │  Definitions ──► initialize + tools/list ──► mcp-weather
//	    │◄─────────────── имена, описания, json-схемы
//	    │  Call ─────────► initialize + tools/call  ──► mcp-weather
//	    │◄─────────────── текст результата
//
// Сессия поднимается на каждый поход и закрывается следом. Сервер поднят без
// сессий, терять на той стороне нечего, зато не нужен ни присмотр за живучестью
// соединения, ни переподключение после перезапуска контейнера. Список инструментов
// при этом кешируется: он не меняется, а лишний round-trip на каждом ходе -- это
// секунда ожидания человеком.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"agent-sprout/internal/agent"
	"agent-sprout/internal/llm"
)

// Box -- набор инструментов одного MCP-сервера.
type Box struct {
	endpoint string
	timeout  time.Duration

	// mu защищает кеш определений. Агент один на приложение и вызывается
	// из нескольких запросов сразу
	mu    sync.Mutex
	tools []llm.Tool
}

// New собирает клиент к серверу по адресу его конечной точки MCP.
func New(endpoint string, timeout time.Duration) *Box {
	return &Box{endpoint: endpoint, timeout: timeout}
}

// Definitions -- инструменты сервера в том виде, в каком их увидит модель.
func (b *Box) Definitions(ctx context.Context) ([]llm.Tool, error) {
	b.mu.Lock()
	cached := b.tools
	b.mu.Unlock()
	if cached != nil {
		return cached, nil
	}

	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	session, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("список инструментов не пришёл: %w", err)
	}
	if len(list.Tools) == 0 {
		return nil, fmt.Errorf("сервер инструментов не отдал ни одного инструмента")
	}

	tools := make([]llm.Tool, 0, len(list.Tools))
	for _, tool := range list.Tools {
		tools = append(tools, definition(tool))
	}

	b.mu.Lock()
	b.tools = tools
	b.mu.Unlock()

	return tools, nil
}

// Call исполняет одну заявку модели и возвращает результат текстом.
//
// Аргументы уезжают такими, какими их собрала модель: проверяем только то, что
// это вообще json. Соответствие схеме проверит сервер -- схему писал он.
func (b *Box) Call(ctx context.Context, name, arguments string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	session, err := b.connect(ctx)
	if err != nil {
		return "", err
	}
	defer session.Close()

	args, err := rawArguments(arguments)
	if err != nil {
		return "", err
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("вызов %s не прошёл: %w", name, err)
	}

	text := resultText(result)
	if result.IsError {
		// отказ инструмента -- содержательный ответ, а не поломка: пусть модель
		// прочитает причину и решит, что с ней делать
		return "", &agent.ToolRefusal{Text: text}
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("инструмент %s вернул пустой ответ", name)
	}
	return text, nil
}

// connect поднимает сессию к серверу.
func (b *Box) connect(ctx context.Context) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "agent-sprout", Version: "0.1.0"}, nil)

	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   b.endpoint,
		HTTPClient: &http.Client{Timeout: b.timeout},
		// сервер поднят без сессий, и GET на нём отдаёт 405 -- постоянный поток
		// событий, который SDK открывает по умолчанию, там просто не заработает.
		// Нам он и не нужен: сервер сам по себе ничего не присылает
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("сервер инструментов недоступен: %w", err)
	}
	return session, nil
}

// definition переводит инструмент MCP в форму, понятную клиенту моделей.
//
// Схему не переписываем и не проверяем: её писал сервер, а читать будет модель.
// Единственная страховка -- на случай, если схемы не окажется вовсе: провайдеры
// не принимают инструмент без parameters.
func definition(tool *mcp.Tool) llm.Tool {
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok || schema == nil {
		schema = map[string]any{"type": "object"}
	}
	return llm.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		Parameters:  schema,
	}
}

// resultText склеивает текстовые блоки результата.
//
// Блоков бывает несколько, и бывают нетекстовые -- картинки, ссылки на ресурсы.
// Модели мы отдаём только текст: всё остальное ей в диалоге не показать.
func resultText(result *mcp.CallToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if text, ok := block.(*mcp.TextContent); ok && text.Text != "" {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// rawArguments -- аргументы модели в виде, который примет протокол.
//
// Аргументы приходят строкой, и строка эта не всегда json: модель вправе
// ошибиться. Проверяем здесь, а не надеемся на сборку запроса, ради внятной
// причины -- её прочитает модель и соберёт вызов заново.
//
// Пустая строка -- не ошибка: так выглядит вызов инструмента без обязательных
// параметров. Отправляем отсутствие аргументов, а не пустую строку, иначе
// разбор сломается на той стороне.
func rawArguments(arguments string) (any, error) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return nil, nil
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, fmt.Errorf("аргументы вызова не разобрались как json: %s", trimmed)
	}
	return json.RawMessage(trimmed), nil
}
