// Package tickets -- клиент MCP-сервера mcp-tickets.
//
// Им пользуются двое. Ассистент -- чтобы отдать модели инструменты чтения и
// исполнить её заявки. HTTP-слой -- чтобы интерфейс создавал и менял тикеты:
// это делает код, а не модель.
//
// Сессия поднимается на каждый вызов и закрывается следом: сервер без сессий.
package tickets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"support-desk/internal/llm"
)

// ModelToolNames -- что модели можно: только чтение.
var ModelToolNames = []string{"ticket_search", "ticket_get", "ticket_list"}

// Ticket -- тикет, как его отдаёт mcp-tickets.
type Ticket struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Resolution  string `json:"resolution"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// ListOutput -- список тикетов.
type ListOutput struct {
	Tickets []Ticket `json:"tickets"`
}

// Client -- доступ к mcp-tickets.
type Client struct {
	endpoint string
	timeout  time.Duration
}

// New собирает клиент по адресу конечной точки MCP.
func New(endpoint string, timeout time.Duration) *Client {
	return &Client{endpoint: endpoint, timeout: timeout}
}

// ModelTools -- инструменты чтения в виде для модели; схемы -- как их описал сервер.
func (c *Client) ModelTools(ctx context.Context) ([]llm.Tool, error) {
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
	var tools []llm.Tool
	for _, tool := range list.Tools {
		if !slices.Contains(ModelToolNames, tool.Name) {
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

// Text -- вызов по заявке модели (аргументы json-строкой), результат текстом.
// Отказы -- тоже текстом: пусть модель прочитает причину.
func (c *Client) Text(ctx context.Context, name, arguments string) (string, error) {
	if !slices.Contains(ModelToolNames, name) {
		return "Ошибка: инструмент недоступен: " + name, nil
	}
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

// Get -- тикет по номеру.
func (c *Client) Get(ctx context.Context, id int64) (Ticket, error) {
	var tk Ticket
	err := c.Call(ctx, "ticket_get", map[string]any{"id": id}, &tk)
	return tk, err
}

// List -- последние тикеты; status пустой -- все.
func (c *Client) List(ctx context.Context, status string, limit int) ([]Ticket, error) {
	var out ListOutput
	err := c.Call(ctx, "ticket_list", map[string]any{"status": status, "limit": limit}, &out)
	return out.Tickets, err
}

// Create -- новый тикет.
func (c *Client) Create(ctx context.Context, title, description string) (Ticket, error) {
	var tk Ticket
	err := c.Call(ctx, "ticket_create", map[string]any{"title": title, "description": description}, &tk)
	return tk, err
}

// Update -- статус и/или решение тикета.
func (c *Client) Update(ctx context.Context, id int64, status, resolution string) (Ticket, error) {
	var tk Ticket
	err := c.Call(ctx, "ticket_update", map[string]any{"id": id, "status": status, "resolution": resolution}, &tk)
	return tk, err
}

// ToolError -- инструмент отказал; текст -- для пользователя.
type ToolError struct{ Message string }

func (e *ToolError) Error() string { return e.Message }

// Call -- вызов из кода: результат -- structuredContent в out.
func (c *Client) Call(ctx context.Context, name string, args any, out any) error {
	result, err := c.call(ctx, name, args)
	if err != nil {
		return err
	}
	if result.IsError {
		return &ToolError{Message: joinText(result)}
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

func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	client := mcp.NewClient(&mcp.Implementation{Name: "support-desk", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             c.endpoint,
		HTTPClient:           &http.Client{Timeout: c.timeout},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("сервер тикетов недоступен: %w", err)
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
