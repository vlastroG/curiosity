// Package mcpc -- клиент MCP-сервера mcp-crypto.
//
// Им пользуются двое. Агент -- чтобы отдать модели инструменты сервера и исполнить
// её заявки. HTTP-слой -- чтобы показать в интерфейсе цены и графики: для этого
// модель не нужна, те же инструменты вызываются напрямую.
//
// Сессия поднимается на каждый вызов и закрывается следом: сервер без сессий,
// терять на той стороне нечего, зато перезапуск контейнера ничего не ломает.
package mcpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"crypto-watch/internal/llm"
)

// Client -- доступ к одному MCP-серверу.
type Client struct {
	endpoint string
	timeout  time.Duration
}

// New собирает клиент по адресу конечной точки MCP.
func New(endpoint string, timeout time.Duration) *Client {
	return &Client{endpoint: endpoint, timeout: timeout}
}

// Tools -- инструменты сервера в виде для модели, только перечисленные в names.
//
// Модели отдаём не всё: сохранять прогнозы и менять расписание -- дело кода,
// а не модели. Схемы при этом не переписываем: их писал сервер.
func (c *Client) Tools(ctx context.Context, names ...string) ([]llm.Tool, error) {
	session, ctx, cancel, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer session.Close()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("список инструментов не пришёл: %w", err)
	}

	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	var tools []llm.Tool
	for _, tool := range list.Tools {
		if !wanted[tool.Name] {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok || schema == nil {
			schema = map[string]any{"type": "object"}
		}
		tools = append(tools, llm.Tool{Name: tool.Name, Description: tool.Description, Parameters: schema})
	}
	return tools, nil
}

// Text -- вызов с аргументами модели (json-строкой), результат текстом для модели.
// Отказ инструмента возвращается текстом же: пусть модель прочитает причину.
func (c *Client) Text(ctx context.Context, name, arguments string) (string, error) {
	var args any
	if trimmed := strings.TrimSpace(arguments); trimmed != "" && trimmed != "null" {
		if !json.Valid([]byte(trimmed)) {
			return "Аргументы вызова не разобрались как json: " + trimmed, nil
		}
		args = json.RawMessage(trimmed)
	}

	result, err := c.call(ctx, name, args)
	if err != nil {
		return "", err
	}
	text := joinText(result)
	if result.IsError {
		return "Инструмент отказал: " + text, nil
	}
	return text, nil
}

// Call -- вызов из кода: аргументы структурой, результат -- structuredContent в out.
func (c *Client) Call(ctx context.Context, name string, args any, out any) error {
	result, err := c.call(ctx, name, args)
	if err != nil {
		return err
	}
	if result.IsError {
		return fmt.Errorf("%s: %s", name, joinText(result))
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) call(ctx context.Context, name string, args any) (*mcp.CallToolResult, error) {
	session, ctx, cancel, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("вызов %s не прошёл: %w", name, err)
	}
	return result, nil
}

// connect поднимает сессию и возвращает контекст с таймаутом на весь поход.
func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	client := mcp.NewClient(&mcp.Implementation{Name: "crypto-watch", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   c.endpoint,
		HTTPClient: &http.Client{Timeout: c.timeout},
		// сервер без сессий, GET-поток событий там не заработает, да и не нужен
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("MCP-сервер недоступен: %w", err)
	}
	return session, ctx, cancel, nil
}

func joinText(result *mcp.CallToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if text, ok := block.(*mcp.TextContent); ok && text.Text != "" {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
