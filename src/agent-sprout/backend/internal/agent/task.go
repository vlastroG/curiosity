package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-sprout/internal/llm"
)

// Машина состояний задачи.
//
// Задача -- это один вид строительных работ от инициализации до плана. Она нужна
// не сама по себе: без неё слово «рабочая память» не к чему привязать. Жизненный
// цикл задаёт слои: рабочая память живёт ровно столько, сколько задача, а её итог
// переезжает в краткосрочную память диалога.

type TaskStatus string

const (
	TaskCollecting TaskStatus = "collecting" // собираем исходные данные
	TaskDone       TaskStatus = "done"       // план выдан, задача закрыта
	TaskCancelled  TaskStatus = "cancelled"  // пользователь прервал
)

// Decision -- что агент делает на этом ходе.
type Decision string

const (
	DecisionStart          Decision = "start"
	DecisionCollect        Decision = "collect"
	DecisionConfirm        Decision = "confirm"
	DecisionPlan           Decision = "plan"
	DecisionRefuseOffTopic Decision = "refuse_offtopic"
	DecisionRefuseSecond   Decision = "refuse_second"
	DecisionAmbiguous      Decision = "ambiguous"
)

// Requirement -- один пункт исходных данных: что нужно узнать и что уже узнали.
//
// Рабочая память -- это чеклист, а не мешок фактов: одна структура отвечает сразу
// и на «что собрано», и на «чего не хватает», а значит готовность задачи считается
// арифметикой, а не мнением модели.
type Requirement struct {
	Key      string `json:"key"`
	Question string `json:"question"`
	Value    string `json:"value,omitempty"`
}

// Answer -- заполнение одного пункта чеклиста.
type Answer struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Task -- один вид работ от инициализации до плана.
type Task struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status TaskStatus `json:"status"`
	// Requirements -- рабочая память задачи
	Requirements []Requirement `json:"requirements"`
	// KnowledgeIDs -- какие знания долговременной памяти отобраны под эту задачу
	KnowledgeIDs []string `json:"knowledgeIds,omitempty"`
	// Summary -- пересказ решённой задачи, дальше живёт как память диалога
	Summary string `json:"summary,omitempty"`
	// CollectTurns -- сколько ходов уже идёт опрос; нужен предохранителю
	CollectTurns int `json:"collectTurns"`
	// Confirmed -- собранные данные показаны пользователю и он их подтвердил.
	// Обязательный шаг между заполненным чеклистом и планом: диспетчер способен
	// пометить пункт собранным, когда пользователь о нём и не заикался, и без
	// сверки такая выдумка уедет прямо в план
	Confirmed bool       `json:"confirmed"`
	StartedAt time.Time  `json:"startedAt"`
	ClosedAt  *time.Time `json:"closedAt,omitempty"`
}

// Filled -- сколько пунктов чеклиста уже заполнено.
func (t Task) Filled() int {
	filled := 0
	for _, req := range t.Requirements {
		if strings.TrimSpace(req.Value) != "" {
			filled++
		}
	}
	return filled
}

// Missing -- незаполненные пункты чеклиста.
func (t Task) Missing() []Requirement {
	missing := make([]Requirement, 0, len(t.Requirements))
	for _, req := range t.Requirements {
		if strings.TrimSpace(req.Value) == "" {
			missing = append(missing, req)
		}
	}
	return missing
}

// Ready -- собраны ли все исходные данные.
//
// Это и есть критерий окончания сбора, и считает его код: у модели мнения
// на этот счёт не спрашивают.
func (t Task) Ready() bool {
	return len(t.Requirements) > 0 && t.Filled() == len(t.Requirements)
}

// Active -- задача ещё в работе.
func (t Task) Active() bool { return t.Status == TaskCollecting }

// KnowledgeItem -- запись долговременной памяти в том виде, в каком её видит агент.
type KnowledgeItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// KnowledgeRef -- ссылка на знание без текста: для метрик и для интерфейса.
type KnowledgeRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Routing -- заявка маршрутизатора: что модель предлагает сделать на этом ходе.
//
// Именно заявка, а не решение: разрешает переход страж (task_guard.go).
type Routing struct {
	Decision      Decision      `json:"decision"`
	TaskTitle     string        `json:"taskTitle"`
	RelatedTaskID string        `json:"relatedTaskId"`
	Requirements  []Requirement `json:"requirements"`
	Answers       []Answer      `json:"answers"`
	KnowledgeIDs  []string      `json:"knowledgeIds"`
	Reason        string        `json:"reason"`
}

// Границы чеклиста и опроса.
const (
	// minRequirements -- меньше пяти пунктов не дают опросить по-настоящему
	minRequirements = 5
	// maxRequirements -- больше двенадцати превращают разговор в анкету
	maxRequirements = 12
	// maxNewPerTurn -- сколько пунктов можно дописать за один ход
	maxNewPerTurn = 2
	// maxCollectTurns -- предохранитель: после этого числа ходов опроса план
	// выдаётся в любом случае. Бесконечный опрос -- это не дотошность, а зависание
	maxCollectTurns = 8
)

// routerAnswerTokens -- сколько нужно самому json диспетчера. Щедро: чеклист
// из двенадцати пунктов с вопросами по-русски -- это уже тысячи токенов, а обрыв
// на середине даёт невалидный json и отменяет весь ход. Запас на рассуждение
// добавит Model.ServiceTokens.
const routerAnswerTokens = 4096

// routerSchema -- строгая форма ответа диспетчера.
//
// Свободный json_object означал, что модель каждый раз заново придумывает форму:
// слабая выдавала английские ключи и лишние поля, рассуждающая тратила на угадывание
// формы то самое время, из-за которого ход не укладывался в таймаут. Провайдер,
// не принявший схему, получит обычный json_object -- откат в llm.Client.
var routerSchema = &llm.Schema{
	Name: "routing",
	Definition: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"decision", "taskTitle", "relatedTaskId",
			"requirements", "answers", "knowledgeIds", "reason",
		},
		"properties": map[string]any{
			"decision": map[string]any{
				"type": "string",
				"enum": []string{
					string(DecisionStart), string(DecisionCollect),
					string(DecisionRefuseOffTopic), string(DecisionRefuseSecond),
					string(DecisionAmbiguous),
				},
			},
			"taskTitle":     map[string]any{"type": "string"},
			"relatedTaskId": map[string]any{"type": "string"},
			// maxItems обязателен: без верхней границы слабая модель уходит
			// в бесконечный список и обрывается по бюджету на середине json
			"requirements": map[string]any{
				"type":     "array",
				"maxItems": maxRequirements,
				"items":    pairSchema("key", "question"),
			},
			"answers": map[string]any{
				"type":     "array",
				"maxItems": maxRequirements,
				"items":    pairSchema("key", "value"),
			},
			"knowledgeIds": map[string]any{
				"type":     "array",
				"maxItems": maxRequirements,
				"items":    map[string]any{"type": "string"},
			},
			"reason": map[string]any{"type": "string"},
		},
	},
}

func pairSchema(first, second string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{first, second},
		"properties": map[string]any{
			first:  map[string]any{"type": "string"},
			second: map[string]any{"type": "string"},
		},
	}
}

// routerSystem -- промпт маршрутизатора.
var routerSystem = fmt.Sprintf(
	"Ты — диспетчер помощника строителя. Ты не разговариваешь с пользователем: "+
		"ты разбираешь его сообщение и возвращаешь служебный json.\n\n"+
		"Реши, что происходит:\n"+
		"- \"refuse_offtopic\" — сообщение не про строительные работы;\n"+
		"- \"ambiguous\" — вид работ не понятен либо названо сразу несколько;\n"+
		"- \"start\" — активной задачи нет, пользователь просит помочь с конкретным видом работ;\n"+
		"- \"collect\" — задача уже идёт, пользователь отвечает на вопросы или уточняет;\n"+
		"- \"refuse_second\" — задача уже идёт, а речь пошла о ДРУГОМ виде работ.\n\n"+
		"При \"start\" составь чеклист исходных данных под этот вид работ: "+
		"от %d до %d пунктов, каждый с коротким ключом и вопросом, который надо задать "+
		"пользователю. Думай как прораб: основание, объёмы, условия, материалы, "+
		"инструмент, сроки, опыт исполнителя.\n"+
		"КЛЮЧИ ПИШИ ПО-РУССКИ, в нижнем регистре, одно-два слова: "+
		"\"основание\", \"площадь\", \"толщина слоя\", \"температура\", \"инструмент\".\n\n"+
		"При \"collect\" главное — заполнить чеклист. Перечитай сообщение пользователя "+
		"и для КАЖДОГО пункта, о котором он сказал хоть что-то, добавь запись в answers. "+
		"Ключи бери СЛОВО В СЛОВО из списка ключей ниже, новых формулировок не придумывай. "+
		"Пустой answers при содержательном ответе пользователя — ошибка.\n"+
		"Если пользователь назвал что-то, чего в чеклисте нет, всё равно добавь это "+
		"в answers со своим ключом: данные терять нельзя.\n"+
		"Если пункт пользователю не подходит — ставь значение \"не применимо\", "+
		"если он не знает ответа — \"не знаю\".\n"+
		"Новые пункты (не больше %d за ход) добавляй, только если всплыла новая сложность. "+
		"Удалять пункты нельзя.\n\n"+
		"Из списка знаний выбери номера тех, что относятся к этому виду работ.\n"+
		"Если тема совпадает с уже решённой задачей этого диалога, укажи её id.\n\n"+
		"Ответ — ровно один json без текста до и после:\n"+
		"{\"decision\":\"...\",\"taskTitle\":\"...\",\"relatedTaskId\":\"\","+
		"\"requirements\":[{\"key\":\"...\",\"question\":\"...\"}],"+
		"\"answers\":[{\"key\":\"...\",\"value\":\"...\"}],"+
		"\"knowledgeIds\":[\"...\"],\"reason\":\"одна фраза\"}",
	minRequirements, maxRequirements, maxNewPerTurn)

// route -- служебный вызов, который разбирает сообщение пользователя.
//
// Занимает место прежнего обновления фактов: извлечение данных никуда не делось,
// но теперь идёт вместе с разбором состояния, поэтому вызовов на ход не прибавилось.
func (a *Agent) route(
	ctx context.Context,
	model Model,
	provider llm.Provider,
	in RunInput,
	question string,
) (Routing, llm.Response, error) {
	// свой дедлайн на служебный вызов, короче общего: см. serviceTimeout
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, serviceTimeout)
	defer cancel()

	resp, err := a.llm.Chat(ctx, provider, llm.Request{
		Model: model.ID,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: DomainPrompt + "\n\n" + routerSystem},
			{Role: llm.RoleUser, Content: routerPayload(in, question)},
		},
		// нулевая температура: разбор состояния должен быть воспроизводимым
		Temperature: 0,
		MaxTokens:   model.ServiceTokens(routerAnswerTokens),
		Schema:      routerSchema,
		// диспетчер занят классификацией и извлечением, а не размышлением:
		// полное рассуждение здесь только стоит денег и времени
		Thinking: model.ServiceThinking(),
	})
	if err != nil {
		return Routing{}, resp, serviceDeadline(parent, "диспетчер", err)
	}

	claim, err := parseRouting(resp.Text)
	if err != nil {
		// finish_reason отвечает на первый вопрос при разборе: модель сказала глупость
		// или ей не хватило бюджета и ответ оборвался на середине
		return Routing{}, resp, fmt.Errorf("%w (finish_reason=%s)", err, resp.FinishReason)
	}
	return claim, resp, nil
}

// routerPayload собирает состояние, по которому диспетчер принимает решение.
func routerPayload(in RunInput, question string) string {
	var payload strings.Builder

	payload.WriteString("Активная задача: ")
	if in.Task == nil {
		payload.WriteString("нет\n")
	} else {
		fmt.Fprintf(&payload, "%q\n", in.Task.Title)
		payload.WriteString("Чеклист исходных данных:\n")
		keys := make([]string, 0, len(in.Task.Requirements))
		for _, req := range in.Task.Requirements {
			keys = append(keys, req.Key)
			value := req.Value
			if strings.TrimSpace(value) == "" {
				value = "НЕ СОБРАНО, вопрос: " + req.Question
			}
			fmt.Fprintf(&payload, "- %s: %s\n", req.Key, value)
		}
		// список ключей отдельной строкой: слабая модель охотнее копирует готовое,
		// чем выискивает ключи в разметке выше
		fmt.Fprintf(&payload, "\nКлючи для answers (копируй точно): %s\n", strings.Join(keys, ", "))
	}

	payload.WriteString("\nРешённые задачи этого диалога:\n")
	if len(in.SolvedTasks) == 0 {
		payload.WriteString("(нет)\n")
	} else {
		for _, task := range in.SolvedTasks {
			fmt.Fprintf(&payload, "- id=%s %q: %s\n", task.ID, task.Title, task.Summary)
		}
	}

	payload.WriteString("\nЗнания в справочнике:\n")
	if len(in.Knowledge) == 0 {
		payload.WriteString("(нет)\n")
	} else {
		for _, item := range in.Knowledge {
			fmt.Fprintf(&payload, "- id=%s %q\n", item.ID, item.Title)
		}
	}

	payload.WriteString("\nСообщение пользователя:\n")
	payload.WriteString(question)

	return payload.String()
}

// parseRouting разбирает ответ диспетчера.
//
// Приём тот же, что у судьи и у фактов: разобрать целиком, иначе вырезать первый
// объект по фигурным скобкам -- слабые модели любят обернуть json в пояснения.
func parseRouting(text string) (Routing, error) {
	candidate := strings.TrimSpace(text)

	var claim Routing
	if err := json.Unmarshal([]byte(candidate), &claim); err != nil {
		start := strings.Index(candidate, "{")
		end := strings.LastIndex(candidate, "}")
		if start < 0 || end <= start {
			return Routing{}, fmt.Errorf("решение диспетчера не разобралось как json: %s", trimForError(candidate))
		}
		if err := json.Unmarshal([]byte(candidate[start:end+1]), &claim); err != nil {
			return Routing{}, fmt.Errorf("решение диспетчера не разобралось как json: %s", trimForError(candidate))
		}
	}

	if claim.Decision == "" {
		return Routing{}, fmt.Errorf("диспетчер не назвал решение: %s", trimForError(candidate))
	}
	return claim, nil
}

// renderRequirements -- чеклист в текст для запроса к модели.
func renderRequirements(reqs []Requirement, onlyMissing bool) string {
	var out strings.Builder
	for _, req := range reqs {
		empty := strings.TrimSpace(req.Value) == ""
		if onlyMissing && !empty {
			continue
		}
		if empty {
			fmt.Fprintf(&out, "- %s — %s\n", req.Key, req.Question)
		} else {
			fmt.Fprintf(&out, "- %s: %s\n", req.Key, req.Value)
		}
	}
	return out.String()
}
