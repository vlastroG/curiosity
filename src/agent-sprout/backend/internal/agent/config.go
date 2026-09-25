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
	// MaxTokens -- бюджет вывода. Значение по умолчанию приходит из модели,
	// но человек вправе с ним спорить: длину плана он знает лучше нас
	MaxTokens int `json:"maxTokens"`
	// HistoryDepth -- размер окна истории: сколько сообщений уезжает в модель как есть.
	// Когда окно заполняется, оно закрывается -- сворачивается в саммари, -- и отсчёт
	// начинается заново.
	//
	// В интерфейс не выведен и снаружи не меняется: это не настройка, а предохранитель
	// на случай, когда переписка по одной задаче разрастается. Ноль здесь означает
	// "памяти нет вовсе" -- режим для тестов, а не для пользователя.
	HistoryDepth int `json:"historyDepth"`
	// MaxInputChars -- потолок длины вопроса, проверяет входная политика.
	MaxInputChars int `json:"maxInputChars"`
	// Weather -- разрешено ли модели узнавать погоду внешним инструментом.
	//
	// Выключатель именно у чата, а не у приложения: в одном разговоре работы
	// на улице и погода решает всё, в другом -- санузел, и лишний вызов там
	// только тратит время хода.
	//
	// На модели без вызова инструментов (Model.Tools == false) включённый
	// выключатель просто ничего не делает: это не ошибка настройки, а отсутствие
	// возможности, и Validate его не трогает.
	//
	// Чаты, заведённые до появления поля, читаются из json без него и получают
	// false. Чинится одним щелчком в настройках -- городить ради этого миграцию
	// хранилища дороже, чем оно стоит
	Weather bool `json:"weather"`
}

// Значения по умолчанию для нового чата. Бюджета вывода здесь нет: он приходит
// из каталога моделей, см. DefaultConfig.
const (
	defaultTemperature   = 0.7
	defaultHistoryDepth  = 20
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
		// по умолчанию включено: домен агента про наружные работы, и чаще
		// погода нужна, чем мешает
		Weather: true,
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

// MaxTokensAfterSwitch -- каким станет бюджет вывода после смены модели.
//
// Значение, которое пользователь не трогал, следует за моделью: иначе бюджет
// бесплатной модели уехал бы на рассуждающую, где его съедает рассуждение,
// и чат снова молчал бы из коробки. Введённое руками сохраняется -- но обрезается
// по потолку новой модели, иначе настройки просто не прошли бы проверку.
func MaxTokensAfterSwitch(from, to string, current int) int {
	if current == MaxTokensFor(from) {
		return MaxTokensFor(to)
	}
	if model, ok := FindModel(to); ok && current > model.MaxOutputTokens {
		return model.MaxOutputTokens
	}
	return current
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
