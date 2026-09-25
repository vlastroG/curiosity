// Package llm -- тонкий клиент к OpenAI-совместимым чат-API.
//
// Пакет намеренно ничего не знает про агента, чаты и цены: он умеет ровно одно --
// отправить запрос выбранному провайдеру и разобрать ответ. Всё остальное живёт
// в internal/agent.
package llm

// Message -- одно сообщение в диалоге в терминах API провайдера.
//
// Два последних поля появляются только в разговоре с инструментами: ответ модели
// с заявкой на вызов несёт ToolCalls, результат вызова -- ToolCallID. Оба уезжают
// в следующий запрос как есть, поэтому форма полей повторяет форму провайдера.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls -- чего модель хочет от инструментов. Только у роли assistant
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID -- на какую заявку отвечает это сообщение. Только у роли tool
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// Роли сообщений в запросе к модели.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	// RoleTool -- результат вызова инструмента, который модель заказала прошлым ходом
	RoleTool = "tool"
)

// Tool -- инструмент, который модель вправе вызвать.
//
// Ни имени, ни описания, ни схемы здесь не придумывают: всё это приходит от
// MCP-сервера, а пакет только перекладывает их в форму провайдера. Читает их
// тоже не человек, а модель -- по ним она решает, звать инструмент или обойтись.
type Tool struct {
	Name        string
	Description string
	// Parameters -- json-схема аргументов, как её отдал сервер инструмента
	Parameters map[string]any
}

// ToolCall -- заявка модели на вызов инструмента.
//
// Структура повторяет тело провайдера дословно, потому что уезжает обратно
// в следующий запрос нетронутой: переписывать то, что мы всё равно вернём
// как есть, значит наживать расхождение на пустом месте.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction -- что именно вызвать и с чем.
type ToolFunction struct {
	Name string `json:"name"`
	// Arguments -- json-строка, а не разобранный объект: так её присылает провайдер,
	// и так она уезжает исполнителю. Модель вправе прислать сюда невалидный json --
	// разбирать его дело того, кто исполняет вызов
	Arguments string `json:"arguments"`
}

// Значение ToolChoice. Другие режимы (принудительный вызов, запрет) пока не нужны:
// решение звать или не звать -- ровно то, ради чего инструменты и отдаются модели.
const ToolChoiceAuto = "auto"

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
	// Tools -- инструменты, доступные модели на этом вызове. Пусто -- ни поле tools,
	// ни tool_choice в тело не попадают: провайдер, который их не знает, считает
	// лишний ключ ошибкой
	Tools []Tool
	// ToolChoice -- насколько модель свободна в решении звать инструмент.
	// Пусто при непустых Tools означает ToolChoiceAuto
	ToolChoice string
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
	// ToolCalls -- модель просит вызвать инструменты и ждёт результатов.
	// Непустое поле означает, что ход не закончен: текста ответа ещё нет
	ToolCalls []ToolCall
	Usage     Usage
	LatencyMs int
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
	// tools тоже принимают не все: у маленьких моделей вызова инструментов
	// просто нет, и лишний ключ они считают ошибкой
	Tools      []wireTool `json:"tools,omitempty"`
	ToolChoice string     `json:"tool_choice,omitempty"`
}

// wireTool -- инструмент в форме OpenAI: вложенность на ровном месте, но формат
// чужой, и спорить с ним негде.
type wireTool struct {
	Type     string           `json:"type"`
	Function wireToolFunction `json:"function"`
}

type wireToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
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
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}
