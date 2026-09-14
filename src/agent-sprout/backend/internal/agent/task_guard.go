package agent

import (
	"fmt"
	"strings"
)

// Страж переходов: код решает, что можно, а что нет.
//
// Маршрутизатор -- это текст от модели, а значит он недетерминирован: на одной
// и той же реплике он сегодня вернёт «собираем», завтра «выдаём план». Поэтому его
// ответ считается заявкой, а не решением. Что произойдёт на самом деле, решает эта
// функция -- чистая, без вызовов модели, целиком покрываемая таблицей тестов.

// Verdict -- разрешённое решение вместе с состоянием задачи после перехода.
type Verdict struct {
	Decision Decision
	// Task -- задача после применения заявки. nil, если задача не заводится
	Task *Task
	// RelatedTaskID -- ссылка на решённую ранее задачу той же темы, если она есть
	RelatedTaskID string
	// Knowledge -- отобранные записи долговременной памяти
	Knowledge []KnowledgeItem
	// Overrides -- что страж поправил в заявке. Пишется в трейс и в предупреждения:
	// недетерминированность должна быть видна глазами, а не прятаться
	Overrides []string
	// Closing -- этим ходом задача закрывается планом
	Closing bool
}

// Guard проверяет заявку маршрутизатора против текущего состояния.
func Guard(active *Task, solved []Task, knowledge []KnowledgeItem, claim Routing, newID func() string) Verdict {
	verdict := Verdict{Decision: claim.Decision}

	verdict.Knowledge = pickKnowledge(knowledge, claim.KnowledgeIDs, &verdict)
	verdict.RelatedTaskID = pickRelatedTask(solved, claim.RelatedTaskID, &verdict)

	if active == nil || !active.Active() {
		return guardWithoutTask(claim, verdict, newID)
	}
	return guardWithTask(*active, claim, verdict)
}

// guardWithoutTask -- активной задачи нет: собирать нечего и конфликтовать не с чем.
func guardWithoutTask(claim Routing, verdict Verdict, newID func() string) Verdict {
	switch claim.Decision {
	case DecisionRefuseOffTopic, DecisionAmbiguous:
		return verdict

	case DecisionCollect, DecisionPlan, DecisionRefuseSecond:
		verdict.note("заявлено %q, но активной задачи нет", claim.Decision)
		verdict.Decision = DecisionStart
	}

	title := strings.TrimSpace(claim.TaskTitle)
	if title == "" {
		// задача без предмета не заводится: непонятно, что собирать и что планировать
		verdict.note("вид работ не назван — задачу не завожу")
		verdict.Decision = DecisionAmbiguous
		return verdict
	}

	requirements := normalizeChecklist(claim.Requirements, &verdict)
	if len(requirements) == 0 {
		verdict.note("чеклист исходных данных пуст — задачу не завожу")
		verdict.Decision = DecisionAmbiguous
		return verdict
	}

	verdict.Decision = DecisionStart
	task := &Task{
		ID:           newID(),
		Title:        title,
		Status:       TaskCollecting,
		Requirements: requirements,
		CollectTurns: 1,
	}
	applyAnswers(task, claim.Answers, &verdict)
	task.KnowledgeIDs = refIDs(verdict.Knowledge)
	verdict.Task = task

	// план на том же ходе, что и инициализация, невозможен: между постановкой
	// задачи и планом обязан быть хотя бы один ход опроса
	return verdict
}

// guardWithTask -- задача идёт: защищаем её от подмены и от досрочного плана.
func guardWithTask(active Task, claim Routing, verdict Verdict) Verdict {
	task := active
	task.Requirements = append([]Requirement(nil), active.Requirements...)

	switch claim.Decision {
	case DecisionRefuseOffTopic:
		// спросить про кино посреди задачи можно: задача от этого не страдает
		verdict.Task = &task
		return verdict

	case DecisionRefuseSecond, DecisionAmbiguous:
		verdict.Task = &task
		return verdict

	case DecisionStart:
		if sameWork(claim.TaskTitle, active.Title) {
			verdict.note("повторная постановка той же задачи — продолжаю сбор")
			verdict.Decision = DecisionCollect
		} else {
			// молча начать вторую задачу нельзя: правило «один вид работ за раз»
			// держится кодом, а не уговорами в промпте
			verdict.note("заявлена новая задача %q при незакрытой %q", claim.TaskTitle, active.Title)
			verdict.Decision = DecisionRefuseSecond
			verdict.Task = &task
			return verdict
		}

	default:
		verdict.Decision = DecisionCollect
	}

	task.CollectTurns++
	applyAnswers(&task, claim.Answers, &verdict)
	addRequirements(&task, claim.Requirements, &verdict)
	task.KnowledgeIDs = mergeIDs(task.KnowledgeIDs, refIDs(verdict.Knowledge))

	// заголовок активной задачи неизменяем: модель любит переформулировать
	// «штукатурные работы» в «оштукатуривание стен», и вид работ поплывёт за пару ходов
	if title := strings.TrimSpace(claim.TaskTitle); title != "" && !sameWork(title, active.Title) {
		verdict.note("диспетчер переименовал задачу в %q — оставляю %q", title, active.Title)
	}
	task.Title = active.Title

	switch {
	case task.Ready() && !task.Confirmed:
		// чеклист заполнен, но между сбором и планом обязателен ход сверки:
		// диспетчер способен пометить пункт собранным, когда пользователь о нём
		// и не заикался, и без показа данных такая выдумка уедет прямо в план
		verdict.Decision = DecisionConfirm
		task.Confirmed = true

	case task.Ready():
		// критерий окончания сбора: чеклист заполнен и данные сверены. Считает код
		verdict.Decision = DecisionPlan
		verdict.Closing = true

	case task.CollectTurns > maxCollectTurns:
		// предохранитель от бесконечного опроса
		verdict.note("опрос идёт %d %s, а чеклист не полон — перехожу к плану с допущениями",
			task.CollectTurns, Plural(task.CollectTurns, "ход", "хода", "ходов"))
		verdict.Decision = DecisionPlan
		verdict.Closing = true
	}

	if verdict.Closing {
		task.Status = TaskDone
	}

	verdict.Task = &task
	return verdict
}

// applyAnswers заполняет пункты чеклиста.
//
// Ключи, которых в чеклисте нет, дописываются в конец уже заполненными: терять
// добровольно отданные данные нельзя, даже если диспетчер не спрашивал о них.
func applyAnswers(task *Task, answers []Answer, verdict *Verdict) {
	for _, answer := range answers {
		key := strings.TrimSpace(answer.Key)
		value := strings.TrimSpace(answer.Value)
		if key == "" || value == "" {
			continue
		}

		found := false
		for i := range task.Requirements {
			if sameWork(task.Requirements[i].Key, key) {
				task.Requirements[i].Value = value
				found = true
				break
			}
		}

		if !found && len(task.Requirements) < maxRequirements {
			task.Requirements = append(task.Requirements, Requirement{
				Key:      key,
				Question: "уточнение от пользователя",
				Value:    value,
			})
		}
	}
}

// addRequirements дописывает новые пункты чеклиста.
//
// Не больше maxNewPerTurn за ход и не выше общего потолка: иначе диспетчер
// добавляет по пункту на каждый ответ, и опрос не кончается никогда.
func addRequirements(task *Task, incoming []Requirement, verdict *Verdict) {
	added := 0

	for _, req := range incoming {
		key := strings.TrimSpace(req.Key)
		if key == "" || hasRequirement(task.Requirements, key) {
			continue
		}
		if added == maxNewPerTurn {
			verdict.note("диспетчер добавил больше %d пунктов за ход — лишние отброшены", maxNewPerTurn)
			return
		}
		if len(task.Requirements) >= maxRequirements {
			verdict.note("чеклист уже на потолке в %d пунктов — новые отброшены", maxRequirements)
			return
		}

		question := strings.TrimSpace(req.Question)
		if question == "" {
			question = key + "?"
		}
		task.Requirements = append(task.Requirements, Requirement{
			Key:      key,
			Question: question,
			Value:    strings.TrimSpace(req.Value),
		})
		added++
	}
}

// normalizeChecklist приводит присланный чеклист к границам.
func normalizeChecklist(incoming []Requirement, verdict *Verdict) []Requirement {
	checklist := make([]Requirement, 0, len(incoming))

	for _, req := range incoming {
		key := strings.TrimSpace(req.Key)
		if key == "" || hasRequirement(checklist, key) {
			continue
		}
		question := strings.TrimSpace(req.Question)
		if question == "" {
			question = key + "?"
		}
		checklist = append(checklist, Requirement{Key: key, Question: question, Value: strings.TrimSpace(req.Value)})
	}

	if len(checklist) > maxRequirements {
		verdict.note("чеклист из %d пунктов обрезан до %d — иначе это анкета, а не разговор",
			len(checklist), maxRequirements)
		checklist = checklist[:maxRequirements]
	}
	if len(checklist) > 0 && len(checklist) < minRequirements {
		// короткий чеклист не повод отказывать: опрос всё равно состоится,
		// просто он будет поверхностнее, чем задумано
		verdict.note("чеклист всего из %d %s — опрос выйдет поверхностным",
			len(checklist), Plural(len(checklist), "пункта", "пунктов", "пунктов"))
	}

	return checklist
}

// pickKnowledge оставляет только те записи, которые действительно есть в справочнике.
func pickKnowledge(knowledge []KnowledgeItem, ids []string, verdict *Verdict) []KnowledgeItem {
	if len(ids) == 0 || len(knowledge) == 0 {
		return nil
	}

	byID := make(map[string]KnowledgeItem, len(knowledge))
	for _, item := range knowledge {
		byID[item.ID] = item
	}

	picked := make([]KnowledgeItem, 0, len(ids))
	invented := 0
	seen := map[string]bool{}

	for _, id := range ids {
		item, ok := byID[strings.TrimSpace(id)]
		if !ok {
			invented++
			continue
		}
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		picked = append(picked, item)
	}

	if invented > 0 {
		verdict.note("диспетчер сослался на %d несуществующих %s", invented,
			Plural(invented, "знание", "знания", "знаний"))
	}
	return picked
}

// pickRelatedTask проверяет ссылку на ранее решённую задачу этого чата.
func pickRelatedTask(solved []Task, id string, verdict *Verdict) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	for _, task := range solved {
		if task.ID == id {
			return id
		}
	}
	verdict.note("ссылка на решённую задачу %q не нашлась в этом диалоге", id)
	return ""
}

func (v *Verdict) note(format string, args ...any) {
	v.Overrides = append(v.Overrides, fmt.Sprintf(format, args...))
}

func hasRequirement(reqs []Requirement, key string) bool {
	for _, req := range reqs {
		if sameWork(req.Key, key) {
			return true
		}
	}
	return false
}

// sameWork сравнивает названия без оглядки на регистр и пробелы.
func sameWork(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func refIDs(items []KnowledgeItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func mergeIDs(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing))
	merged := make([]string, 0, len(existing)+len(incoming))

	for _, id := range append(append([]string{}, existing...), incoming...) {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		merged = append(merged, id)
	}
	return merged
}
