package main

// Клиент к OpenAI-совместимым чат-API с вызовом инструментов.
//
// DeepSeek и OpenRouter говорят на одном протоколе, поэтому провайдер -- это данные
// (адрес, ключ, пара заголовков), а не отдельный код на каждого. Клиент ничего не
// знает ни про Hacker News, ни про MCP: отправить диалог, получить ответ или заявки
// на инструменты -- и всё.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Идентификаторы провайдеров.
const (
	ProviderDeepSeek   = "deepseek"
	ProviderOpenRouter = "openrouter"
)

// Provider -- куда и с каким ключом идти.
type Provider struct {
	ID           string
	Title        string
	Endpoint     string
	APIKey       string
	ExtraHeaders map[string]string
}

// Available -- есть ли ключ. Модели провайдера без ключа видны в интерфейсе,
// но выбрать их нельзя.
func (p Provider) Available() bool { return p.APIKey != "" }

// DeepSeek -- описание провайдера DeepSeek.
func DeepSeek(apiKey string) Provider {
	return Provider{
		ID:       ProviderDeepSeek,
		Title:    "DeepSeek API",
		Endpoint: "https://api.deepseek.com/chat/completions",
		APIKey:   apiKey,
	}
}

// OpenRouter -- описание провайдера OpenRouter. Referer и X-Title нужны ему
// для атрибуции запросов.
func OpenRouter(apiKey, appURL, appTitle string) Provider {
	return Provider{
		ID:       ProviderOpenRouter,
		Title:    "OpenRouter",
		Endpoint: "https://openrouter.ai/api/v1/chat/completions",
		APIKey:   apiKey,
		ExtraHeaders: map[string]string{
			"HTTP-Referer": appURL,
			"X-Title":      appTitle,
		},
	}
}

// Роли сообщений.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message -- сообщение диалога в терминах API.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall -- заявка модели на вызов инструмента.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool -- инструмент, который видит модель.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Request -- один вызов модели.
type Request struct {
	Model     string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

// Usage -- расход токенов.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// Response -- ответ модели: либо текст, либо заявки на инструменты.
type Response struct {
	Text         string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
}

// APIError -- провайдер ответил, но не успехом.
type APIError struct {
	Provider string
	Status   int
	Body     string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s: %s", e.Provider, e.Body)
	}
	return fmt.Sprintf("%s вернул %d: %s", e.Provider, e.Status, e.Body)
}

// ChatClient -- HTTP-клиент к чат-API.
type ChatClient struct {
	http *http.Client
	// backoff -- пауза перед повтором; в тестах обнуляется
	backoff time.Duration
}

// newChatClient создаёт клиент с таймаутом на один запрос.
func newChatClient(timeout time.Duration) *ChatClient {
	return &ChatClient{http: &http.Client{Timeout: timeout}, backoff: 3 * time.Second}
}

// maxRetries -- повторы на 429 и 5xx. У бесплатных моделей 429 -- обычное дело:
// общий пул лимитов, который освобождается через секунды.
const maxRetries = 3

// Chat выполняет один вызов модели.
func (c *ChatClient) Chat(ctx context.Context, p Provider, req Request) (Response, error) {
	if !p.Available() {
		return Response{}, &APIError{Provider: p.Title, Body: "ключ провайдера не задан в окружении"}
	}

	body, err := json.Marshal(wire(req))
	if err != nil {
		return Response{}, err
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Response{}, ctx.Err()
			case <-time.After(c.backoff * time.Duration(attempt)):
			}
		}
		resp, retry, err := c.once(ctx, p, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return Response{}, lastErr
}

func (c *ChatClient) once(ctx context.Context, p Provider, body []byte) (Response, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, false, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	for name, value := range p.ExtraHeaders {
		httpReq.Header.Set(name, value)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// сетевой сбой повторяем, отмену контекста -- нет
		return Response{}, ctx.Err() == nil, fmt.Errorf("%s недоступен: %w", p.Title, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Response{}, true, fmt.Errorf("ответ %s не дочитался: %w", p.Title, err)
	}
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return Response{}, retry, &APIError{Provider: p.Title, Status: resp.StatusCode, Body: short(raw)}
	}

	var parsed wireResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, false, fmt.Errorf("ответ %s не разобрался: %w", p.Title, err)
	}
	// OpenRouter умеет вернуть ошибку с кодом 200, положив её в тело
	if parsed.Error != nil {
		retry := parsed.Error.Code == http.StatusTooManyRequests || parsed.Error.Code >= 500
		return Response{}, retry, &APIError{Provider: p.Title, Status: parsed.Error.Code, Body: parsed.Error.Message}
	}
	if len(parsed.Choices) == 0 {
		return Response{}, true, &APIError{Provider: p.Title, Body: "ответ без вариантов"}
	}

	choice := parsed.Choices[0]
	return Response{
		Text:         choice.Message.Content,
		ToolCalls:    choice.Message.ToolCalls,
		FinishReason: choice.FinishReason,
		Usage:        parsed.Usage,
	}, false, nil
}

func short(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len([]rune(text)) > 300 {
		text = string([]rune(text)[:300]) + "…"
	}
	return text
}

type wireRequest struct {
	Model     string     `json:"model"`
	Messages  []Message  `json:"messages"`
	Tools     []wireTool `json:"tools,omitempty"`
	MaxTokens int        `json:"max_tokens,omitempty"`
	Stream    bool       `json:"stream"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

func wire(req Request) wireRequest {
	out := wireRequest{Model: req.Model, Messages: req.Messages, MaxTokens: req.MaxTokens}
	for _, tool := range req.Tools {
		var w wireTool
		w.Type = "function"
		w.Function.Name = tool.Name
		w.Function.Description = tool.Description
		w.Function.Parameters = tool.Parameters
		out.Tools = append(out.Tools, w)
	}
	return out
}

type wireResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
