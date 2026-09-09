// Package agent -- «коробка» агента: всё, что происходит между запросом пользователя
// и ответом модели.
//
// Пакет намеренно не знает ни про HTTP, ни про хранилище чатов, ни про интерфейс.
// Его единственная точка входа -- Agent.Run: на вход вопрос, история и настройки,
// на выход ответ с метриками и трейсом. Поэтому агента можно вызвать из теста
// с подставным LLM-клиентом, не поднимая сервер.
//
// Конвейер: входная политика -> сборка контекста -> вызов модели -> выходная политика
// -> (необязательно) судья. Каждый этап пишет строку в трейс.
package agent

import (
	"context"
	"fmt"
	"time"

	"agent-sprout/internal/llm"
)

// Completer -- то, что умеет вызвать модель. Интерфейс, а не *llm.Client, ради тестов:
// подставная реализация позволяет прогнать весь конвейер без сети и без денег.
type Completer interface {
	Chat(ctx context.Context, provider llm.Provider, req llm.Request) (llm.Response, error)
}

// Agent -- собственно коробка. Один экземпляр на приложение, потокобезопасен:
// собственного изменяемого состояния у него нет, всё приходит в RunInput.
type Agent struct {
	llm       Completer
	providers map[string]llm.Provider
	// now вынесено полем, чтобы тесты могли зафиксировать время и проверить
	// переключение тарифа peak/off-peak
	now func() time.Time
}

// New собирает агента поверх клиента моделей и набора провайдеров.
func New(completer Completer, providers map[string]llm.Provider) *Agent {
	return &Agent{llm: completer, providers: providers, now: time.Now}
}

// Message -- сообщение истории чата в том виде, в каком его отдаёт хранилище.
type Message struct {
	Role    string
	Content string
}

// RunInput -- всё, что нужно агенту для одного прохода.
type RunInput struct {
	Question string
	History  []Message
	Config   Config
	// Last -- числа последнего состоявшегося вызова в этом чате. Нужны, чтобы
	// посчитать заполненность окна контекста по факту, а не по оценке
	Last LastTurn
}

// RunOutput -- результат прохода. Возвращается и при ошибке политики: трейс в этом
// случае показывает, на каком этапе агент остановился.
type RunOutput struct {
	Answer       string        `json:"answer"`
	Model        string        `json:"model"`
	Usage        llm.Usage     `json:"usage"`
	Cost         Cost          `json:"cost"`
	TotalUSD     float64       `json:"totalUsd"`
	LatencyMs    int           `json:"latencyMs"`
	FinishReason string        `json:"finishReason"`
	Calls        int           `json:"calls"`
	Judge        *JudgeVerdict `json:"judge,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
	Trace        []Step        `json:"trace"`
	// Context -- состояние окна контекста перед этим запросом
	Context ContextState `json:"context"`
	// HistoryMessages -- сколько сообщений истории уехало в запрос вместе с вопросом
	HistoryMessages int `json:"historyMessages"`
}

// ModelUnavailableError -- модель есть в каталоге, но её провайдеру не задан ключ.
// Отдельный тип, чтобы HTTP-слой отдал внятные 400 с подсказкой, а не 401 от провайдера.
type ModelUnavailableError struct {
	Model    string
	Provider string
}

func (e *ModelUnavailableError) Error() string {
	return fmt.Sprintf("модель %s недоступна: не задан ключ провайдера %s", e.Model, e.Provider)
}

// Run -- единственная точка входа в агента.
func (a *Agent) Run(ctx context.Context, in RunInput) (RunOutput, error) {
	startedAt := time.Now()
	trace := &tracer{}
	window := ContextFor(in.Config, in.Last)
	out := RunOutput{Model: in.Config.Model, Context: window}

	// 1. Входная политика. Отрабатывает до любого обращения к модели: заблокированный
	// запрос не стоит ни одного токена.
	stepStart := time.Now()
	question, err := checkInput(in.Question, in.Config, window)
	if err != nil {
		trace.record(StepInputPolicy, stepStart, false, err.Error())
		out.Trace = trace.steps
		return out, err
	}
	trace.record(StepInputPolicy, stepStart, true, fmt.Sprintf(
		"запрос принят, окно контекста занято на %.0f%% (%d из %d)",
		window.Percent, window.Used, window.ModelLimit))

	model, provider, err := a.resolve(in.Config.Model)
	if err != nil {
		out.Trace = trace.steps
		return out, err
	}

	// 2. Сборка контекста: system prompt плюс ограниченный хвост истории.
	stepStart = time.Now()
	messages := buildMessages(question, in.History, in.Config)
	// из отправленного вычитаем system prompt и сам вопрос -- остаётся история
	out.HistoryMessages = len(messages) - 2
	trace.record(StepBuildContext, stepStart, true,
		fmt.Sprintf("%d сообщений в запросе, из них %d истории при глубине %d",
			len(messages), out.HistoryMessages, in.Config.HistoryDepth))

	// 3. Вызов модели.
	stepStart = time.Now()
	resp, err := a.llm.Chat(ctx, provider, llm.Request{
		Model:            model.ID,
		Messages:         messages,
		Temperature:      in.Config.Temperature,
		MaxTokens:        in.Config.MaxTokens,
		TopP:             in.Config.TopP,
		FrequencyPenalty: in.Config.FrequencyPenalty,
		PresencePenalty:  in.Config.PresencePenalty,
		JSONObject:       in.Config.ResponseFormat == FormatJSON,
	})
	if err != nil {
		trace.record(StepLLM, stepStart, false, err.Error())
		out.Trace = trace.steps
		return out, err
	}
	out.Calls = 1
	out.Usage = resp.Usage
	out.FinishReason = resp.FinishReason
	out.Cost = model.Cost(resp.Usage, a.now())
	out.TotalUSD = out.Cost.USD
	trace.record(StepLLM, stepStart, true,
		fmt.Sprintf("%d -> %d токенов, finish_reason=%s",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.FinishReason))

	// 4. Выходная политика.
	stepStart = time.Now()
	warnings, err := checkOutput(resp.Text, resp, in.Config)
	if err != nil {
		trace.record(StepOutputPolicy, stepStart, false, err.Error())
		out.LatencyMs = int(time.Since(startedAt).Milliseconds())
		out.Trace = trace.steps
		return out, err
	}
	out.Answer = resp.Text
	out.Warnings = warnings
	trace.record(StepOutputPolicy, stepStart, true, outputDetail(warnings))

	// 5. Судья -- необязательный этап. Его сбой не должен стоить пользователю ответа,
	// который уже получен и оплачен, поэтому ошибка уходит в трейс, а не наверх.
	if in.Config.JudgeEnabled {
		stepStart = time.Now()
		verdict, judgeErr := a.judge(ctx, model, provider, question, resp.Text)
		if judgeErr != nil {
			trace.record(StepJudge, stepStart, false, judgeErr.Error())
			out.Warnings = append(out.Warnings, "судья не смог оценить ответ: "+judgeErr.Error())
		} else {
			out.Judge = verdict
			out.Calls++
			out.TotalUSD += verdict.Cost.USD
			trace.record(StepJudge, stepStart, true, fmt.Sprintf("оценка %d из 5", verdict.Score))
		}
	}

	out.LatencyMs = int(time.Since(startedAt).Milliseconds())
	out.Trace = trace.steps
	return out, nil
}

// resolve находит модель в каталоге и провайдера с ключом.
func (a *Agent) resolve(modelID string) (Model, llm.Provider, error) {
	model, ok := FindModel(modelID)
	if !ok {
		return Model{}, llm.Provider{}, fmt.Errorf("неизвестная модель %q", modelID)
	}
	provider, ok := a.providers[model.Provider]
	if !ok || !provider.Available() {
		return Model{}, llm.Provider{}, &ModelUnavailableError{Model: model.ID, Provider: model.Provider}
	}
	return model, provider, nil
}

// Available сообщает, задан ли ключ провайдера этой модели. Нужно каталогу в API,
// чтобы интерфейс мог погасить недоступные варианты.
func (a *Agent) Available(model Model) bool {
	provider, ok := a.providers[model.Provider]
	return ok && provider.Available()
}

// buildMessages собирает тело диалога: system prompt, хвост истории и текущий вопрос.
//
// История обрезается по HistoryDepth -- это и есть память агента. При нуле каждый
// запрос уходит без контекста, и разницу хорошо видно на уточняющих вопросах.
func buildMessages(question string, history []Message, cfg Config) []llm.Message {
	messages := []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt(cfg)}}

	tail := history
	if cfg.HistoryDepth < len(tail) {
		tail = tail[len(tail)-cfg.HistoryDepth:]
	}
	for _, message := range tail {
		messages = append(messages, llm.Message{Role: message.Role, Content: message.Content})
	}

	return append(messages, llm.Message{Role: llm.RoleUser, Content: question})
}

// systemPrompt дополняет промпт чата требованиями, которые следуют из настроек.
func systemPrompt(cfg Config) string {
	prompt := cfg.SystemPrompt

	if cfg.MaxWords > 0 {
		prompt += fmt.Sprintf("\n\nУложись в %d слов.", cfg.MaxWords)
	}
	if cfg.ResponseFormat == FormatJSON {
		prompt += "\n\n" + jsonInstruction
	}

	return prompt
}

func outputDetail(warnings []string) string {
	if len(warnings) == 0 {
		return "ответ принят"
	}
	return fmt.Sprintf("ответ принят с замечаниями (%d)", len(warnings))
}
