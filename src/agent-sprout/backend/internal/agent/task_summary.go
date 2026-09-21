package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"agent-sprout/internal/llm"
)

// Закрытие задачи пересказом.
//
// Рабочая память задачи после плана больше не нужна — таскать десяток пунктов
// чеклиста через весь диалог дорого и бессмысленно. Но совсем терять решённую
// задачу нельзя: если человек вернётся к той же теме, агент должен вспомнить,
// что уже делал. Поэтому итог задачи сжимается в пересказ и переезжает
// в краткосрочную память диалога.

// taskSummaryAnswerTokens -- 3-5 строк текста; запас на рассуждение добавит модель.
const taskSummaryAnswerTokens = 1024

const taskSummarySystem = "Ты сжимаешь только что решённую задачу в короткий пересказ " +
	"для памяти диалога. Сохрани: какой вид работ, ключевые исходные данные " +
	"(объёмы, основание, условия, материалы) и что было выдано в плане. " +
	"Пиши по-русски, от третьего лица, 3–5 строк, без вступлений и без пересказа " +
	"самого плана по шагам. Верни только текст пересказа."

// summarizeTask собирает пересказ закрытой задачи.
func (a *Agent) summarizeTask(
	ctx context.Context,
	model Model,
	provider llm.Provider,
	task Task,
	plan string,
) (string, llm.Response, error) {
	// свой дедлайн на служебный вызов, короче общего: см. serviceTimeout
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, serviceTimeout)
	defer cancel()

	var payload strings.Builder
	fmt.Fprintf(&payload, "Вид работ: %s\n\nИсходные данные:\n", task.Title)
	payload.WriteString(renderRequirements(task.Requirements, false))
	payload.WriteString("\nВыданный план:\n")
	payload.WriteString(plan)

	resp, err := a.llm.Chat(ctx, provider, llm.Request{
		Model: model.ID,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: taskSummarySystem},
			{Role: llm.RoleUser, Content: payload.String()},
		},
		Temperature: 0,
		MaxTokens:   model.ServiceTokens(taskSummaryAnswerTokens),
		Thinking:    model.ServiceThinking(),
	})
	if err != nil {
		return "", resp, serviceDeadline(parent, "пересказ задачи", err)
	}

	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return "", resp, fmt.Errorf("модель вернула пустой пересказ (finish_reason=%s)", resp.FinishReason)
	}
	return text, resp, nil
}

// newTaskID -- идентификатор задачи.
//
// Задачи живут внутри чата, но идентификатор глобально случайный: при клонировании
// ветки он не перевыпускается и, наоборот, показывает общее происхождение веток.
func newTaskID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("генератор случайных чисел недоступен: %v", err))
	}
	return hex.EncodeToString(buf)
}
