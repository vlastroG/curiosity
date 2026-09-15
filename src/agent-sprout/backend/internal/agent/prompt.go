package agent

import (
	"fmt"
	"strings"

	"agent-sprout/internal/llm"
)

// Сборка запроса из трёх слоёв памяти.
//
// Слои не свалены в один текст: у каждого своё system-сообщение со своей подписью.
// Так модель понимает, что перед ней разные вещи (устойчивое знание, итог прошлой
// задачи, собранные исходные данные), а человек в снимке памяти видит, что именно
// из какого слоя уехало в запрос.

// Подписи слоёв в запросе.
const (
	knowledgePreamble = "Знания, которые обязательно учесть в этой задаче. " +
		"Это требования и правила, заданные пользователем заранее; они важнее " +
		"твоих общих представлений о работах:\n"

	solvedPreamble = "Задачи, уже решённые в этом диалоге. Если новый вопрос про то же, " +
		"сошлись на них, а не начинай с нуля:\n"

	taskPreamble = "Рабочая память текущей задачи — исходные данные, собранные у пользователя.\n\n"
)

// contextParts -- всё, что уезжает в запрос, разложенное по слоям памяти.
type contextParts struct {
	Profile       Profile         // персонализация: кто пользователь и как ему удобнее
	Summary       string          // краткосрочная: пересказ закрытого окна
	Knowledge     []KnowledgeItem // долговременная: отобранные знания
	Task          *Task           // рабочая: чеклист активной задачи
	Solved        []Task          // краткосрочная: решённые задачи диалога
	Decision      Decision
	RelatedTaskID string
	History       []Message // краткосрочная: окно сообщений
}

// buildMessages собирает запрос из трёх слоёв памяти.
//
// Порядок не случайный: сначала неизменяемая роль, затем самый устойчивый слой
// (знания), затем память диалога, затем рабочая память задачи и только потом
// инструкция хода — она должна стоять ближе всего к вопросу, чтобы не потеряться
// за простынёй контекста.
func buildMessages(question string, parts contextParts, cfg Config) []llm.Message {
	// доменная роль скрыта и неизменяема: редактора system prompt в настройках нет,
	// иначе и машина состояний, и слои памяти обесценились бы одной репликой
	messages := []llm.Message{{Role: llm.RoleSystem, Content: DomainPrompt}}

	system := func(content string) {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: content})
	}

	// персонализация: сразу после роли, чтобы читаться как уточнение к ней,
	// а не как замена. Пустой профиль не добавляет в запрос ничего
	if block := parts.Profile.Render(); block != "" {
		system(block)
	}

	// долговременная память
	if len(parts.Knowledge) > 0 {
		var block strings.Builder
		block.WriteString(knowledgePreamble)
		for _, item := range parts.Knowledge {
			fmt.Fprintf(&block, "\n### %s\n%s\n", item.Title, item.Text)
		}
		system(block.String())
	}

	// краткосрочная: итоги решённых задач диалога
	if len(parts.Solved) > 0 {
		var block strings.Builder
		block.WriteString(solvedPreamble)
		for _, task := range parts.Solved {
			fmt.Fprintf(&block, "\n- %s: %s\n", task.Title, task.Summary)
		}
		system(block.String())
	}

	// краткосрочная: пересказ закрытого окна
	if strings.TrimSpace(parts.Summary) != "" {
		system(summaryPreamble + parts.Summary)
	}

	// рабочая память задачи
	if parts.Task != nil && len(parts.Task.Requirements) > 0 {
		var block strings.Builder
		fmt.Fprintf(&block, "%sВид работ: %s\n\nСобрано:\n", taskPreamble, parts.Task.Title)
		block.WriteString(renderRequirements(parts.Task.Requirements, false))

		if missing := parts.Task.Missing(); len(missing) > 0 {
			block.WriteString("\nЕщё не собрано — об этом и спрашивай:\n")
			block.WriteString(renderRequirements(missing, true))
		}
		system(block.String())
	}

	// инструкция хода: что именно делать, уже решил страж, а не модель
	instruction := instructionFor(parts.Decision)
	if parts.Decision == DecisionPlan && parts.Task != nil {
		instruction += assumptionsNote(parts.Task.Missing())
	}
	if note := relatedNote(parts.Solved, parts.RelatedTaskID); note != "" {
		instruction += "\n\n" + note
	}
	system(instruction)

	for _, message := range parts.History {
		messages = append(messages, llm.Message{Role: message.Role, Content: message.Content})
	}

	return append(messages, llm.Message{Role: llm.RoleUser, Content: question})
}

// relatedNote -- напоминание сослаться на ранее решённую задачу той же темы.
func relatedNote(solved []Task, id string) string {
	if id == "" {
		return ""
	}
	for _, task := range solved {
		if task.ID == id {
			return fmt.Sprintf(
				"В этом диалоге уже решалась задача «%s». Сошлись на неё, коротко напомни "+
					"её итог и спроси, брать ли те же исходные данные или собирать заново.",
				task.Title)
		}
	}
	return ""
}

// snapshotMemory собирает снимок того, что реально уехало в запрос.
//
// Ради него всё и затевалось: пользователь должен видеть содержимое каждого слоя,
// а не верить на слово, что память работает.
func snapshotMemory(parts contextParts, summary string, history int) MemorySnapshot {
	snapshot := MemorySnapshot{
		Profile:         parts.Profile.Filled(),
		WindowSummary:   strings.TrimSpace(summary) != "",
		HistoryMessages: history,
	}

	for _, item := range parts.Knowledge {
		snapshot.Knowledge = append(snapshot.Knowledge, KnowledgeRef{ID: item.ID, Title: item.Title})
	}
	for _, task := range parts.Solved {
		snapshot.SolvedTasks = append(snapshot.SolvedTasks, SolvedRef{ID: task.ID, Title: task.Title})
	}
	if parts.Task != nil {
		snapshot.TaskTitle = parts.Task.Title
		snapshot.TaskStatus = parts.Task.Status
		snapshot.Requirements = parts.Task.Requirements
	}

	return snapshot
}

// routingDetail -- строка трейса про решение машины состояний.
//
// Расхождение заявки диспетчера и вердикта стража пишется явно: недетерминированность
// модели должна быть видна глазами, а не прятаться за гладким ответом.
func routingDetail(claim Routing, verdict Verdict) string {
	detail := fmt.Sprintf("диспетчер: %s", claim.Decision)
	if verdict.Decision != claim.Decision {
		detail += fmt.Sprintf(" → страж: %s", verdict.Decision)
	}
	if verdict.Task != nil {
		detail += fmt.Sprintf(", задача «%s», собрано %d из %d",
			verdict.Task.Title, verdict.Task.Filled(), len(verdict.Task.Requirements))
	}
	if len(verdict.Overrides) > 0 {
		detail += "; " + strings.Join(verdict.Overrides, "; ")
	}
	return detail
}
