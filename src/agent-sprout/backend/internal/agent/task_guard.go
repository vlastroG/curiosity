package agent

import (
	"fmt"
	"slices"
	"strings"
)

// Страж переходов: код решает, что можно, а что нет.
//
// Маршрутизатор -- это текст от модели, а значит он недетерминирован: на одной
// и той же реплике он сегодня вернёт «собираем», завтра «выдаём план». Поэтому его
// ответ считается заявкой, а не решением. Что произойдёт на самом деле, решает эта
// функция -- чистая, без вызовов модели, целиком покрываемая таблицей тестов.
//
// Сами разрешённые переходы страж не хранит: они лежат таблицей в task_machine.go.
// Здесь остаётся вторая половина работы -- привести заявку в порядок (заголовок
// неизменяем, чеклист не сокращается, выдуманные ссылки отбрасываются) и спросить
// у машины, что из заявленного вообще допустимо в текущей фазе.

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

	verdict.Knowledge = pickKnowledge(knowledge, attachedIDs(active), claim.KnowledgeIDs, &verdict)
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
		Phase:        PhaseCollecting,
		Requirements: requirements,
		CollectTurns: 1,
	}
	// на инициализации в чеклист уезжает только то, что человек действительно назвал
	// в первом сообщении. Отговорки «не знаю» и «не применимо» на этом ходе взяться
	// неоткуда: вопросов ему ещё не задавали. Слабая модель заполняет ими весь чеклист
	// разом, и без этой отсечки задача уходила бы в план, не спросив ни о чём
	applyAnswers(task, withoutPlaceholders(claim.Answers, &verdict), &verdict)
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

	applyPhase(&task, &verdict)

	verdict.Task = &task
	return verdict
}

// applyPhase -- собственно переход. Сперва спрашиваем машину, не обязана ли она
// сменить фазу сама; если нет -- проверяем заявку диспетчера по той же таблице.
//
// Порядок именно такой: forced-переходы существуют затем, чтобы модель не могла
// перепрыгнуть этап, поэтому её мнение на них не спрашивается вовсе.
func applyPhase(task *Task, verdict *Verdict) {
	if event, to, ok := forcedStep(task.Phase, *task); ok {
		if event != verdict.Decision {
			verdict.note("%s", forcedNote(task.Phase, event, *task))
		}
		verdict.Decision = event
		task.Phase = to
		verdict.Closing = to == PhaseDone
		return
	}

	to, ok := nextPhase(task.Phase, verdict.Decision, *task)
	if !ok {
		// заявка в этой фазе недопустима: продолжаем сбор, а расхождение пишем
		// в предупреждения -- недетерминированность модели должна быть видна
		verdict.note("решение %q недопустимо в фазе %q — продолжаю сбор",
			verdict.Decision, task.Phase)
		verdict.Decision = DecisionCollect
		to, _ = nextPhase(task.Phase, DecisionCollect, *task)
	}

	task.Phase = to
	verdict.Closing = to == PhaseDone
}

// forcedNote объясняет в предупреждениях, почему машина решила за диспетчера.
func forcedNote(from TaskPhase, event Decision, task Task) string {
	switch {
	case event == DecisionConfirm:
		// диспетчер способен пометить пункт собранным, когда пользователь о нём
		// и не заикался, и без показа данных такая выдумка уедет прямо в план
		return "чеклист заполнен — перед планом обязательна сверка"
	case from == PhaseCollecting:
		return fmt.Sprintf("опрос идёт %d %s, а чеклист не полон — перехожу к плану с допущениями",
			task.CollectTurns, Plural(task.CollectTurns, "ход", "хода", "ходов"))
	default:
		return "данные сверены — выдаю план"
	}
}

// applyAnswers заполняет пункты чеклиста.
//
// Ключи, которых в чеклисте нет, дописываются в конец уже заполненными: терять
// добровольно отданные данные нельзя, даже если диспетчер не спрашивал о них.
// placeholderAnswers -- значения, которыми диспетчер помечает пункт закрытым,
// не получив ответа. На ходе инициализации они означают выдумку.
var placeholderAnswers = []string{"не знаю", "не применимо"}

func withoutPlaceholders(answers []Answer, verdict *Verdict) []Answer {
	kept := make([]Answer, 0, len(answers))
	dropped := 0
	for _, answer := range answers {
		value := strings.ToLower(strings.TrimSpace(answer.Value))
		if slices.Contains(placeholderAnswers, value) {
			dropped++
			continue
		}
		kept = append(kept, answer)
	}
	if dropped > 0 {
		verdict.note("на инициализации отброшено %d %s вида «не знаю»: пользователя ещё ни о чём не спрашивали",
			dropped, Plural(dropped, "значение", "значения", "значений"))
	}
	return kept
}

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

// attachedIDs -- знания, уже прикреплённые к активной задаче.
func attachedIDs(active *Task) []string {
	if active == nil || !active.Active() {
		return nil
	}
	return active.KnowledgeIDs
}

// pickKnowledge собирает знания, которые уедут в запрос: уже прикреплённые к задаче
// плюс названные диспетчером на этом ходе. Оставляет только те, что действительно
// есть в справочнике.
//
// Прикреплённые тянутся отдельно от заявки не для порядка: выбор знаний
// переигрывается каждый ход, и стоило модели один раз не повторить id, как знание
// молча исчезало из контекста посреди задачи. Раз уж оно однажды подошло виду работ,
// держим его до закрытия задачи -- решать это заново на каждой реплике незачем.
func pickKnowledge(knowledge []KnowledgeItem, attached, claimed []string, verdict *Verdict) []KnowledgeItem {
	if len(knowledge) == 0 || (len(attached) == 0 && len(claimed) == 0) {
		return nil
	}

	byID := make(map[string]KnowledgeItem, len(knowledge))
	for _, item := range knowledge {
		byID[item.ID] = item
	}

	picked := make([]KnowledgeItem, 0, len(attached)+len(claimed))
	seen := map[string]bool{}

	take := func(id string) bool {
		item, ok := byID[strings.TrimSpace(id)]
		if !ok {
			return false
		}
		if !seen[item.ID] {
			seen[item.ID] = true
			picked = append(picked, item)
		}
		return true
	}

	// прикреплённое молча пропускаем, если его больше нет: знание могли удалить
	// из справочника посреди задачи, и диспетчер тут ни при чём
	for _, id := range attached {
		take(id)
	}

	invented := 0
	for _, id := range claimed {
		if !take(id) {
			invented++
		}
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
