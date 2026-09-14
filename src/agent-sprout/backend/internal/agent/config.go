package agent

import "fmt"

// Config -- настройки одного чата. Ровно этот набор редактируется в интерфейсе
// и целиком хранится вместе с чатом, так что у каждого чата свой характер.
//
// Набор намеренно короткий: в нём остались настройки, которые соответствуют нынешней
// логике приложения. Формат ответа и лимит слов противоречили бы детальному плану
// работ, штрафы и top_p на него не влияют, а сжатие истории теперь безусловное.
type Config struct {
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	// MaxTokens не редактируется руками: он выводится из модели и пересчитывается
	// при её смене. Наружу отдаётся, чтобы интерфейс показывал бюджет вывода
	MaxTokens int `json:"maxTokens"`
	// HistoryDepth -- размер окна истории: сколько сообщений уезжает в модель как есть.
	// Когда окно заполняется, оно закрывается -- сворачивается в саммари, -- и отсчёт
	// начинается заново. 0 -- памяти нет вовсе, каждый запрос уходит без контекста.
	HistoryDepth int `json:"historyDepth"`
	// MaxInputChars -- потолок длины вопроса, проверяет входная политика.
	MaxInputChars int `json:"maxInputChars"`
}

// Значения по умолчанию для нового чата. Бюджета вывода здесь нет: он приходит
// из каталога моделей, см. DefaultConfig.
const (
	defaultTemperature   = 0.7
	defaultHistoryDepth  = 10
	defaultMaxInputChars = 4000
)

// Пределы, внутри которых значение имеет смысл. Всё, что вне, отклоняется с внятным
// текстом: молча подправлять настройку, которую пользователь выставил руками, хуже.
const (
	maxTemperature   = 2.0
	maxHistoryDepth  = 100
	maxInputCharsCap = 100_000
)

// DefaultConfig -- настройки нового чата. Модель по умолчанию приходит из окружения
// сервера, чтобы переключение "прогон на бесплатной <-> работа на DeepSeek" не требовало
// правки кода.
func DefaultConfig(defaultModel string) Config {
	return Config{
		Model:         defaultModel,
		Temperature:   defaultTemperature,
		MaxTokens:     MaxTokensFor(defaultModel),
		HistoryDepth:  defaultHistoryDepth,
		MaxInputChars: defaultMaxInputChars,
	}
}

// MaxTokensFor -- бюджет вывода для модели. Неизвестная модель не должна ронять
// создание чата: Validate скажет о ней внятнее, чем паника на нулевом бюджете.
func MaxTokensFor(modelID string) int {
	model, ok := FindModel(modelID)
	if !ok {
		return 4096
	}
	return model.DefaultMaxTokens
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
	if c.HistoryDepth < 0 || c.HistoryDepth > maxHistoryDepth {
		return fmt.Errorf("окно истории должно быть от 0 до %d сообщений", maxHistoryDepth)
	}
	if c.MaxInputChars < 1 || c.MaxInputChars > maxInputCharsCap {
		return fmt.Errorf("max_input_chars должен быть от 1 до %d", maxInputCharsCap)
	}
	return nil
}
