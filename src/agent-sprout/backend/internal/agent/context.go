package agent

// Окно контекста модели -- сколько токенов помещается в один вызов.
//
// Считается только по фактическим числам из API: ничего не оценивается и не
// угадывается. Провайдер проверяет ровно то же неравенство, что и мы:
// вход запроса + max_tokens на ответ должны помещаться в окно модели.

// warnPercent -- с какой заполненности окна интерфейс начинает предупреждать.
const warnPercent = 80.0

// LastTurn -- фактические числа последнего состоявшегося вызова модели в чате.
// Заполняется вызывающим из метрик последнего ответа.
type LastTurn struct {
	Present          bool
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
}

// Visible -- часть ответа, которая уедет в историю следующего запроса.
//
// Рассуждение оплачивается, но в видимый текст не попадает, значит и в контекст
// следующего вызова не переносится.
func (t LastTurn) Visible() int {
	visible := t.CompletionTokens - t.ReasoningTokens
	if visible < 0 {
		return 0
	}
	return visible
}

// ContextState -- сколько окна модели уже расписано и сколько осталось на новый вопрос.
type ContextState struct {
	Model      string  `json:"model"`
	ModelLimit int     `json:"modelLimit"`
	// Reserve -- max_tokens: место, которое надо оставить под будущий ответ
	Reserve int `json:"reserve"`
	// LastPrompt -- prompt_tokens последнего запроса, точное число из API
	LastPrompt int `json:"lastPrompt"`
	// Carried -- что уедет в следующий запрос: прошлый вход плюс видимая часть ответа
	Carried   int     `json:"carried"`
	Used      int     `json:"used"`
	Available int     `json:"available"`
	Percent   float64 `json:"percent"`
	Warning   bool    `json:"warning"`
	Full      bool    `json:"full"`
}

// ContextFor считает состояние окна перед следующим запросом.
//
// Оговорка: при historyDepth меньше длины диалога старые сообщения из запроса
// выпадают, и следующий вызов может оказаться меньше Carried. Тогда индикатор
// завышает занятое -- это осознанный выбор: завысить безопаснее, чем занизить
// и упереться в отказ провайдера.
func ContextFor(cfg Config, last LastTurn) ContextState {
	state := ContextState{Model: cfg.Model, Reserve: cfg.MaxTokens}

	model, ok := FindModel(cfg.Model)
	if !ok {
		// настройки чата валидируются при сохранении, сюда попасть нельзя;
		// на всякий случай отдаём пустое состояние, а не блокировку
		return state
	}
	state.ModelLimit = model.ContextTokens

	if last.Present {
		state.LastPrompt = last.PromptTokens
		state.Carried = last.PromptTokens + last.Visible()
	}

	state.Used = state.Carried + state.Reserve
	state.Available = state.ModelLimit - state.Used
	if state.ModelLimit > 0 {
		state.Percent = float64(state.Used) / float64(state.ModelLimit) * 100
	}

	state.Full = state.Available <= 0
	state.Warning = !state.Full && state.Percent >= warnPercent

	return state
}
