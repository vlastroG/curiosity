package main

// Клиент MCP-сервера mcp-pipeline.
//
// Сессия поднимается на каждый вызов и закрывается следом: сервер без сессий,
// терять на той стороне нечего, зато перезапуск контейнера ничего не ломает.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP -- доступ к одному MCP-серверу.
type MCP struct {
	endpoint string
	timeout  time.Duration
}

// ToolResult -- результат вызова: текст для модели и structuredContent для проверки цепочки.
type ToolResult struct {
	Text       string
	Structured json.RawMessage
	IsError    bool
}

// Tools -- инструменты сервера в виде для модели, только из списка allowed.
func (c *MCP) Tools(ctx context.Context, allowed []string) ([]Tool, error) {
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
	var tools []Tool
	for _, tool := range list.Tools {
		if !contains(allowed, tool.Name) {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok || schema == nil {
			schema = map[string]any{"type": "object"}
		}
		tools = append(tools, Tool{Name: tool.Name, Description: tool.Description, Parameters: schema})
	}
	return tools, nil
}

// Call вызывает инструмент с аргументами модели (json-строкой). Отказ инструмента --
// не ошибка вызова: он возвращается в IsError, и его текст читает модель.
func (c *MCP) Call(ctx context.Context, name, arguments string) (ToolResult, error) {
	var args any
	if trimmed := strings.TrimSpace(arguments); trimmed != "" && trimmed != "null" {
		if !json.Valid([]byte(trimmed)) {
			return ToolResult{Text: "Аргументы вызова не разобрались как JSON: " + trimmed, IsError: true}, nil
		}
		args = json.RawMessage(trimmed)
	}

	session, ctx, cancel, err := c.connect(ctx)
	if err != nil {
		return ToolResult{}, err
	}
	defer cancel()
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return ToolResult{}, fmt.Errorf("вызов %s не прошёл: %w", name, err)
	}

	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if text, ok := block.(*mcp.TextContent); ok && text.Text != "" {
			parts = append(parts, text.Text)
		}
	}
	out := ToolResult{Text: strings.Join(parts, "\n"), IsError: result.IsError}
	if result.StructuredContent != nil {
		out.Structured, _ = json.Marshal(result.StructuredContent)
	}
	return out, nil
}

// connect поднимает сессию и возвращает контекст с таймаутом на весь поход.
func (c *MCP) connect(ctx context.Context) (*mcp.ClientSession, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	client := mcp.NewClient(&mcp.Implementation{Name: "pipeline-agent", Version: version}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   c.endpoint,
		HTTPClient: &http.Client{Timeout: c.timeout},
		// сервер без сессий, GET-поток событий там не заработает, да и не нужен
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("MCP-сервер %s недоступен: %w", c.endpoint, err)
	}
	return session, ctx, cancel, nil
}

func contains(list []string, item string) bool {
	for _, x := range list {
		if x == item {
			return true
		}
	}
	return false
}
