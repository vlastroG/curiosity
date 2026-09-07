package agent

import "fmt"

// Форматы ответа, которые понимает конвейер.
const (
	FormatText = "text"
	FormatJSON = "json_object"
)

// Config -- настройки одного чата. Ровно этот набор редактируется в интерфейсе
// и целиком хранится вместе с чатом, так что у каждого чата свой характер.
type Config struct {
	Model            string  `json:"model"`
	SystemPrompt     string  `json:"systemPrompt"`
	Temperature      float64 `json:"temperature"`
	MaxTokens        int     `json:"maxTokens"`
	TopP             float64 `json:"topP"`
	FrequencyPenalty float64 `json:"frequencyPenalty"`
	PresencePenalty  float64 `json:"presencePenalty"`
	ResponseFormat   string  `json:"responseFormat"`
	// MaxWords -- мягкий лимит длины ответа: уходит в промпт и проверяется
	// выходной политикой. 0 -- без лимита.
	MaxWords int `json:"maxWords"`
	// HistoryDepth -- сколько последних сообщений диалога уходит в контекст.
	// 0 -- только текущий вопрос, каждый запрос без памяти.
	HistoryDepth int  `json:"historyDepth"`
	JudgeEnabled bool `json:"judgeEnabled"`
	// MaxInputChars -- потолок длины вопроса, проверяет входная политика.
	MaxInputChars int `json:"maxInputChars"`
}

// Значения по умолчанию для нового чата.
const (
	defaultTemperature   = 0.7
	defaultMaxTokens     = 4096
	defaultTopP          = 1.0
	defaultHistoryDepth  = 10
	defaultMaxInputChars = 4000
)

// Пределы, внутри которых значение имеет смысл. Всё, что вне, отклоняется с внятным
// текстом: молча подправлять настройку, которую пользователь выставил руками, хуже.
const (
	maxTemperature   = 2.0
	maxHistoryDepth  = 100
	maxInputCharsCap = 100_000
	maxPenalty       = 2.0
)

// DefaultConfig -- настройки нового чата. Модель по умолчанию приходит из окружения
// сервера, чтобы переключение "прогон на бесплатной ↔ работа на DeepSeek" не требовало
// правки кода.
func DefaultConfig(defaultModel string) Config {
	return Config{
		Model:          defaultModel,
		SystemPrompt:   Presets[0].Prompt,
		Temperature:    defaultTemperature,
		MaxTokens:      defaultMaxTokens,
		TopP:           defaultTopP,
		ResponseFormat: FormatText,
		HistoryDepth:   defaultHistoryDepth,
		MaxInputChars:  defaultMaxInputChars,
	}
}

// Validate проверяет настройки целиком. Вызывается при создании чата и при каждом
// изменении настроек, чтобы в хранилище не попала конфигурация, на которой упадёт вызов.
func (c Config) Validate() error {
	model, ok := FindModel(c.Model)
	if !ok {
		return fmt.Errorf("неизвестная модель %q", c.Model)
	}
	if c.Temperature < 0 || c.Temperature > maxTemperature {
		return fmt.Errorf("temperature должна быть от 0 до %g", maxTemperature)
	}
	if c.MaxTokens < 1 || c.MaxTokens > model.MaxOutputTokens {
		return fmt.Errorf("max_tokens должен быть от 1 до %d для модели %s", model.MaxOutputTokens, model.ID)
	}
	if c.TopP <= 0 || c.TopP > 1 {
		return fmt.Errorf("top_p должен быть больше 0 и не больше 1")
	}
	if c.FrequencyPenalty < -maxPenalty || c.FrequencyPenalty > maxPenalty {
		return fmt.Errorf("frequency_penalty должен быть от -%g до %g", maxPenalty, maxPenalty)
	}
	if c.PresencePenalty < -maxPenalty || c.PresencePenalty > maxPenalty {
		return fmt.Errorf("presence_penalty должен быть от -%g до %g", maxPenalty, maxPenalty)
	}
	if c.ResponseFormat != FormatText && c.ResponseFormat != FormatJSON {
		return fmt.Errorf("response_format должен быть %q или %q", FormatText, FormatJSON)
	}
	if c.MaxWords < 0 {
		return fmt.Errorf("max_words не может быть отрицательным")
	}
	if c.HistoryDepth < 0 || c.HistoryDepth > maxHistoryDepth {
		return fmt.Errorf("history_depth должен быть от 0 до %d", maxHistoryDepth)
	}
	if c.MaxInputChars < 1 || c.MaxInputChars > maxInputCharsCap {
		return fmt.Errorf("max_input_chars должен быть от 1 до %d", maxInputCharsCap)
	}
	return nil
}
