// Package llm -- тонкий клиент к OpenAI-совместимым чат-API.
//
// Пакет намеренно ничего не знает про агента, чаты и цены: он умеет ровно одно --
// отправить запрос выбранному провайдеру и разобрать ответ. Всё остальное живёт
// в internal/agent.
package llm

// Message -- одно сообщение в диалоге в терминах API провайдера.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Роли сообщений в запросе к модели.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Request -- параметры одного вызова модели. Поля повторяют тело OpenAI-совместимого
// запроса, но собираются из настроек чата в internal/agent.
type Request struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
	// Schema -- строгая схема ответа. Со схемой модель не тратит рассуждение
	// на угадывание формы и не может вернуть лишних полей
	Schema *Schema
	// Thinking -- режим рассуждения рассуждающей модели. Пусто -- параметр
	// не отправляется, и решает провайдер (у DeepSeek по умолчанию рассуждение
	// включено с максимальным усилием)
	Thinking string
}

// Schema -- json-схема ответа.
type Schema struct {
	Name       string
	Definition map[string]any
}

// Значения Thinking. Совпадают с параметром thinking.type в API DeepSeek:
// https://api-docs.deepseek.com/guides/thinking_mode (снято 2026-09-14).
const (
	ThinkingOn  = "enabled"
	ThinkingOff = "disabled"
)

// Usage -- расход токенов, как его отдаёт провайдер.
//
// Поля prompt_cache_* есть у DeepSeek и нужны для расчёта стоимости: вход с попаданием
// в кеш промпта стоит в десятки раз дешевле промаха. У провайдеров, которые их не
// присылают, оба поля остаются нулями -- считающая сторона обязана это учитывать.
type Usage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`

	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// ReasoningTokens -- часть completion_tokens, ушедшая во внутреннее рассуждение модели
// и не попавшая в видимый текст. У нерассуждающих моделей всегда 0.
func (u Usage) ReasoningTokens() int {
	if u.CompletionTokensDetails == nil {
		return 0
	}
	return u.CompletionTokensDetails.ReasoningTokens
}

// Response -- результат одного вызова модели.
type Response struct {
	Text         string
	FinishReason string
	Usage        Usage
	LatencyMs    int
	// Downgraded -- провайдер не принял ускоряющие параметры, и запрос прошёл
	// со второй попытки без них. Видно в трейсе
	Downgraded bool
}

// wireRequest -- тело POST-запроса. Отдельный тип, потому что часть полей опускается,
// когда значение не задано: лишний ключ в теле некоторые провайдеры считают ошибкой.
type wireRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	// thinking принимают не все провайдеры -- отсюда откат в client.go
	Thinking       *wireThinking `json:"thinking,omitempty"`
	ResponseFormat *wireRespFmt  `json:"response_format,omitempty"`
}

type wireThinking struct {
	Type string `json:"type"`
}

type wireRespFmt struct {
	Type       string          `json:"type"`
	JSONSchema *wireJSONSchema `json:"json_schema,omitempty"`
}

type wireJSONSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

// wireResponse -- ответ провайдера. Поле error здесь не случайно: OpenRouter умеет
// вернуть ошибку с HTTP-кодом 200, положив её в тело.
type wireResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}
