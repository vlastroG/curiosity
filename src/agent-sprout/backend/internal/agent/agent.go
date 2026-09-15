// Package agent -- «коробка» агента: всё, что происходит между запросом пользователя
// и ответом модели.
//
// Пакет намеренно не знает ни про HTTP, ни про хранилище чатов, ни про интерфейс.
// Его единственная точка входа -- Agent.Run: на вход вопрос, история и настройки,
// на выход ответ с метриками и трейсом. Поэтому агента можно вызвать из теста
// с подставным LLM-клиентом, не поднимая сервер.
//
// Конвейер: входная политика -> машина состояний -> сборка контекста -> вызов модели
// -> выходная политика -> закрытие задачи. Каждый этап пишет строку в трейс.
package agent

import (
	"context"
	"errors"
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
	// Task -- активная задача чата, если она есть. Рабочая память живёт в ней
	Task *Task
	// SolvedTasks -- решённые задачи этого диалога: краткосрочная память
	SolvedTasks []Task
	// Knowledge -- справочник долговременной памяти целиком; диспетчер отбирает нужное
	Knowledge []KnowledgeItem
	// Profile -- персонализация: кто пользователь и как с ним разговаривать.
	// Один на все чаты, уезжает в каждый запрос
	Profile Profile
	Config  Config
	// Last -- числа последнего состоявшегося вызова в этом чате. Нужны, чтобы
	// посчитать заполненность окна контекста по факту, а не по оценке
	Last LastTurn
}

// RunOutput -- результат прохода. Возвращается и при ошибке политики: трейс в этом
// случае показывает, на каком этапе агент остановился.
type RunOutput struct {
	Answer       string    `json:"answer"`
	Model        string    `json:"model"`
	Usage        llm.Usage `json:"usage"`
	Cost         Cost      `json:"cost"`
	TotalUSD     float64   `json:"totalUsd"`
	LatencyMs    int       `json:"latencyMs"`
	FinishReason string    `json:"finishReason"`
	Calls        int       `json:"calls"`
	Warnings     []string  `json:"warnings,omitempty"`
	Trace        []Step    `json:"trace"`
	// Context -- состояние окна контекста перед этим запросом
	Context ContextState `json:"context"`
	// HistoryMessages -- сколько сообщений истории уехало в запрос вместе с вопросом
	HistoryMessages int `json:"historyMessages"`
	// Compaction -- заполнено, если на этом ходе окно истории закрылось
	Compaction *Compaction `json:"compaction,omitempty"`
	// Decision -- что агент сделал на этом ходе после проверки стражем
	Decision Decision `json:"decision"`
	// Task -- состояние задачи после хода. nil, если задача так и не завелась
	Task *Task `json:"task,omitempty"`
	// Overrides -- что страж поправил в заявке диспетчера
	Overrides []string `json:"overrides,omitempty"`
	// Memory -- снимок трёх слоёв памяти, ушедших в этот запрос
	Memory MemorySnapshot `json:"memory"`
	// Routing -- метрики служебного вызова диспетчера
	Routing *ServiceCall `json:"routing,omitempty"`
	// TaskSummary -- метрики вызова, закрывшего задачу пересказом
	TaskSummary *ServiceCall `json:"taskSummary,omitempty"`
}

// ServiceCall -- метрики служебного вызова модели (диспетчер, пересказ задачи).
type ServiceCall struct {
	Usage     llm.Usage `json:"usage"`
	Cost      Cost      `json:"cost"`
	LatencyMs int       `json:"latencyMs"`
}

// MemorySnapshot -- что именно уехало в запрос из каждого слоя памяти.
//
// Ради этого снимка всё и затевалось: пользователь должен видеть, что попало
// в долговременную, рабочую и краткосрочную память, а не верить на слово.
type MemorySnapshot struct {
	// долговременная: отобранные знания
	Knowledge []KnowledgeRef `json:"knowledge,omitempty"`
	// рабочая: чеклист задачи на момент ответа
	TaskTitle    string        `json:"taskTitle,omitempty"`
	TaskStatus   TaskStatus    `json:"taskStatus,omitempty"`
	Requirements []Requirement `json:"requirements,omitempty"`
	// персонализация: какие секции профиля уехали в запрос
	Profile []string `json:"profile,omitempty"`
	// краткосрочная: память диалога
	SolvedTasks     []SolvedRef `json:"solvedTasks,omitempty"`
	WindowSummary   bool        `json:"windowSummary"`
	HistoryMessages int         `json:"historyMessages"`
}

// SolvedRef -- решённая задача в снимке памяти.
type SolvedRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
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

	// 2. Машина состояний: диспетчер разбирает сообщение, страж разрешает переход.
	//
	// Сбой здесь отменяет ход, как и сбой сжатия: без состояния нечего собирать
	// и нечего планировать, а отвечать наугад в домене, где ошибка стоит денег
	// и материалов, нельзя.
	stepStart = time.Now()
	claim, routerResp, err := a.route(ctx, model, provider, in, question)
	if err != nil {
		trace.record(StepRouting, stepStart, false, err.Error())
		out.Trace = trace.steps
		return out, fmt.Errorf("разбор состояния задачи не удался: %w", err)
	}
	out.Routing = &ServiceCall{
		Usage:     routerResp.Usage,
		Cost:      model.Cost(routerResp.Usage, a.now()),
		LatencyMs: routerResp.LatencyMs,
	}

	verdict := Guard(in.Task, in.SolvedTasks, in.Knowledge, claim, newTaskID)
	out.Decision = verdict.Decision
	out.Task = verdict.Task
	out.Overrides = verdict.Overrides
	out.Warnings = append(out.Warnings, verdict.Overrides...)
	trace.record(StepRouting, stepStart, true, routingDetail(claim, verdict)+downgradeNote(routerResp.Downgraded))

	// 3. Управление контекстом: если окно истории заполнилось, оно закрывается.
	history, summary, err := a.manageContext(ctx, &out, trace, model, provider, in)
	if err != nil {
		out.Trace = trace.steps
		return out, err
	}

	// 4. Сборка контекста: три слоя памяти плюс инструкция под решение стража.
	stepStart = time.Now()
	parts := contextParts{
		Profile:       in.Profile,
		Summary:       summary,
		Knowledge:     verdict.Knowledge,
		Task:          verdict.Task,
		Solved:        in.SolvedTasks,
		Decision:      verdict.Decision,
		RelatedTaskID: verdict.RelatedTaskID,
		History:       history,
	}
	messages := buildMessages(question, parts, in.Config)
	out.HistoryMessages = len(history)
	out.Memory = snapshotMemory(parts, summary, len(history))
	trace.record(StepBuildContext, stepStart, true, fmt.Sprintf(
		"%d %s в запросе: знаний %d, истории %d, решённых задач %d%s%s",
		len(messages), Plural(len(messages), "сообщение", "сообщения", "сообщений"),
		len(verdict.Knowledge), out.HistoryMessages, len(in.SolvedTasks),
		summaryNote(summary), profileNote(in.Profile)))

	// 5. Вызов модели.
	stepStart = time.Now()
	// рассуждение здесь не ограничивается: детальный план работ -- ровно то место,
	// где думать есть над чем
	resp, err := a.llm.Chat(ctx, provider, llm.Request{
		Model:       model.ID,
		Messages:    messages,
		Temperature: in.Config.Temperature,
		MaxTokens:   in.Config.MaxTokens,
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
		fmt.Sprintf("%d -> %d токенов, finish_reason=%s%s",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.FinishReason,
			downgradeNote(resp.Downgraded)))

	// 6. Выходная политика.
	stepStart = time.Now()
	warnings, err := checkOutput(resp.Text, resp)
	if err != nil {
		trace.record(StepOutputPolicy, stepStart, false, err.Error())
		out.LatencyMs = int(time.Since(startedAt).Milliseconds())
		out.Trace = trace.steps
		return out, err
	}
	out.Answer = resp.Text
	out.Warnings = warnings
	trace.record(StepOutputPolicy, stepStart, true, outputDetail(warnings))

	// 7. Закрытие задачи пересказом. Сбой не отменяет ход: план уже выдан и оплачен,
	// а без пересказа диалог просто потеряет память об этой задаче.
	if verdict.Closing && verdict.Task != nil {
		stepStart = time.Now()
		summaryText, summaryResp, summaryErr := a.summarizeTask(ctx, model, provider, *verdict.Task, resp.Text)
		if summaryErr != nil {
			trace.record(StepTaskSummary, stepStart, false, summaryErr.Error())
			out.Warnings = append(out.Warnings, "пересказ задачи не собрался: "+summaryErr.Error())
		} else {
			out.Task.Summary = summaryText
			out.Calls++
			cost := model.Cost(summaryResp.Usage, a.now())
			out.TotalUSD += cost.USD
			out.TaskSummary = &ServiceCall{
				Usage:     summaryResp.Usage,
				Cost:      cost,
				LatencyMs: summaryResp.LatencyMs,
			}
			trace.record(StepTaskSummary, stepStart, true, fmt.Sprintf(
				"задача %q закрыта, пересказ на %d токенов",
				verdict.Task.Title, summaryResp.Usage.CompletionTokens))
		}
	}

	out.LatencyMs = int(time.Since(startedAt).Milliseconds())
	out.Trace = trace.steps
	return out, nil
}

// serviceTimeout -- потолок на один служебный вызов: диспетчер, сжатие, пересказ.
//
// Он короче общего дедлайна хода намеренно. Зависший диспетчер не должен съесть
// весь бюджет и оставить пользователя без ответа: лучше честно сказать, что разбор
// не сложился, чем молчать до последней секунды.
const serviceTimeout = 4 * time.Minute

// serviceDeadline отличает наш собственный дедлайн от чужого.
//
// Иначе в ленте оказывался таймаут хода целиком, хотя сдался служебный вызов
// на своём, вчетверо меньшем сроке -- и цифра в сообщении не сходилась ни с чем.
func serviceDeadline(parent context.Context, what string, err error) error {
	if !errors.Is(err, context.DeadlineExceeded) || parent.Err() != nil {
		return err
	}
	return fmt.Errorf("%s не уложился в %.0f секунд", what, serviceTimeout.Seconds())
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

// manageContext закрывает окно истории, если оно заполнилось.
//
// Возвращает то, что реально уедет в запрос: сообщения окна и пересказ. После
// закрытия окна сообщений не остаётся -- их заменяет пересказ.
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

// downgradeNote -- отметка об откате ускоряющих параметров.
//
// В предупреждения она не идёт: для пользователя ничего не сломалось, ход прошёл
// как надо. Но в трейсе это видно -- иначе молчаливая потеря ускорения выглядела бы
// просто как «почему-то медленно».
func downgradeNote(downgraded bool) string {
	if !downgraded {
		return ""
	}
	return " (провайдер не принимает часть ускоряющих параметров — запрос ушёл без них)"
}

// profileNote -- отметка о персонализации в трейсе.
func profileNote(profile Profile) string {
	filled := profile.Filled()
	if len(filled) == 0 {
		return ""
	}
	return ", профиль (" + strings.Join(filled, ", ") + ")"
}

func summaryNote(summary string) string {
	if strings.TrimSpace(summary) != "" {
		return ", плюс пересказ свёрнутой части"
	}
	return ""
}

func outputDetail(warnings []string) string {
	if len(warnings) == 0 {
		return "ответ принят"
	}
	return fmt.Sprintf("ответ принят с замечаниями (%d)", len(warnings))
}
