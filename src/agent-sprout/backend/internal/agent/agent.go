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
	"strings"
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
	// History -- сообщения текущего окна, уже отрезанные хранилищем по последней
	// границе. Резать их ещё раз агенту не нужно
	History []Message
	// Summary -- пересказ свёрнутой части диалога. Пусто, если сжатия ещё не было
	Summary string
	// Facts -- key-value память чата, накопленная за весь диалог
	Facts  []Fact
	Config Config
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
	// Compaction -- заполнено, если на этом ходе окно истории закрылось
	Compaction *Compaction `json:"compaction,omitempty"`
	// Facts -- заполнено, если на этом ходе обновлялась key-value память
	Facts *FactsUpdate `json:"facts,omitempty"`
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

	// 2. Управление контекстом: если окно истории заполнилось, оно закрывается.
	history, summary, err := a.manageContext(ctx, &out, trace, model, provider, in)
	if err != nil {
		out.Trace = trace.steps
		return out, err
	}

	// 3. Сборка контекста: system prompt, пересказ и сообщения окна.
	stepStart = time.Now()
	messages := buildMessages(question, summary, in.Facts, history, in.Config)
	out.HistoryMessages = len(history)
	trace.record(StepBuildContext, stepStart, true, fmt.Sprintf(
		"%d %s в запросе, из них %d истории при окне %d%s",
		len(messages), Plural(len(messages), "сообщение", "сообщения", "сообщений"),
		out.HistoryMessages, in.Config.HistoryDepth, summaryNote(summary)))

	// 4. Вызов модели.
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
	out.Calls++
	out.Usage = resp.Usage
	out.FinishReason = resp.FinishReason
	out.Cost = model.Cost(resp.Usage, a.now())
	out.TotalUSD += out.Cost.USD
	trace.record(StepLLM, stepStart, true,
		fmt.Sprintf("%d -> %d токенов, finish_reason=%s",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.FinishReason))

	// 5. Выходная политика.
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

	// 6. Обновление key-value памяти. Как и судья, этап необязательный и не должен
	// стоить пользователю уже полученного ответа: сбой уходит в предупреждение.
	if in.Config.StickyFacts {
		stepStart = time.Now()
		update, factsErr := a.updateFacts(ctx, model, provider, in.Facts, question, resp.Text)
		if factsErr != nil {
			trace.record(StepFacts, stepStart, false, factsErr.Error())
			out.Warnings = append(out.Warnings, "память фактов не обновилась: "+factsErr.Error())
		} else {
			out.Facts = update
			out.Calls++
			out.TotalUSD += update.Cost.USD
			trace.record(StepFacts, stepStart, true, fmt.Sprintf(
				"%d %s в памяти, из них новых %d, обновлённых %d",
				len(update.Facts), Plural(len(update.Facts), "факт", "факта", "фактов"),
				update.Added, update.Changed))
		}
	}

	// 7. Судья -- необязательный этап. Его сбой не должен стоить пользователю ответа,
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
func buildMessages(question, summary string, facts []Fact, history []Message, cfg Config) []llm.Message {
	messages := []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt(cfg)}}

	// пересказ уезжает отдельным system-сообщением сразу после промпта чата:
	// это память агента, а не реплика собеседника
	if strings.TrimSpace(summary) != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: summaryPreamble + summary})
	}

	// следом key-value память: она собрана по всему диалогу, в том числе по той части,
	// которую пересказ уже не покрывает
	if cfg.StickyFacts && len(facts) > 0 {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: factsPreamble + renderFacts(facts)})
	}

	for _, message := range history {
		messages = append(messages, llm.Message{Role: message.Role, Content: message.Content})
	}

	return append(messages, llm.Message{Role: llm.RoleUser, Content: question})
}

// manageContext закрывает окно истории, если оно заполнилось.
//
// Возвращает то, что реально уедет в запрос: сообщения окна и пересказ. После
// закрытия окна сообщений не остаётся -- их заменяет пересказ (или не заменяет
// ничего, если сжатие выключено).
func (a *Agent) manageContext(
	ctx context.Context,
	out *RunOutput,
	trace *tracer,
	model Model,
	provider llm.Provider,
	in RunInput,
) ([]Message, string, error) {
	// нулевое окно -- памяти нет вовсе: ни сообщений, ни пересказа. Отметку в ленте
	// при этом не ставим, иначе она появлялась бы на каждом ходе
	if in.Config.HistoryDepth <= 0 {
		return nil, "", nil
	}

	// окно ещё не заполнилось -- ничего не трогаем
	if len(in.History) < in.Config.HistoryDepth {
		return in.History, in.Summary, nil
	}

	stepStart := time.Now()

	if !in.Config.SummarizeHistory {
		// сжатие выключено: окно теряется. Пересказ, накопленный раньше, при этом
		// остаётся -- выбрасывать уже оплаченную память было бы вредно
		out.Compaction = &Compaction{Dropped: true, Covered: len(in.History)}
		trace.record(StepCompact, stepStart, true, fmt.Sprintf(
			"окно заполнено, %d %s отброшено без сжатия",
			len(in.History), Plural(len(in.History), "сообщение", "сообщения", "сообщений")))
		return nil, in.Summary, nil
	}

	compaction, err := a.compact(ctx, model, provider, in.Summary, in.History)
	if err != nil {
		// без пересказа окно уже нельзя выбросить, а отправлять его целиком значит
		// делать вид, что сжатия не было. Отменяем ход: пользователь повторит запрос,
		// и сжатие попробует собраться заново
		trace.record(StepCompact, stepStart, false, err.Error())
		return nil, "", fmt.Errorf("сжатие истории не удалось: %w", err)
	}

	// токены и деньги сжатия НЕ приплюсовываются к ходу: у отметки о сжатии
	// в ленте свои метрики, и складывать их дважды -- значит завысить сумму по чату
	out.Compaction = compaction
	trace.record(StepCompact, stepStart, true, fmt.Sprintf(
		"%d %s свёрнуто в пересказ на %d %s%s",
		compaction.Covered, Plural(compaction.Covered, "сообщение", "сообщения", "сообщений"),
		compaction.Usage.CompletionTokens,
		Plural(compaction.Usage.CompletionTokens, "токен", "токена", "токенов"),
		recursiveNote(compaction.Recursive)))

	return nil, compaction.Text, nil
}

func recursiveNote(recursive bool) string {
	if recursive {
		return ", вместе с прошлым пересказом"
	}
	return ""
}

// renderFacts превращает память в текст для запроса.
func renderFacts(facts []Fact) string {
	var out strings.Builder
	for _, fact := range facts {
		out.WriteString("- ")
		out.WriteString(fact.Key)
		out.WriteString(": ")
		out.WriteString(fact.Value)
		out.WriteString("\n")
	}
	return out.String()
}

func summaryNote(summary string) string {
	if strings.TrimSpace(summary) != "" {
		return ", плюс пересказ свёрнутой части"
	}
	return ""
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
