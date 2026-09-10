package agent

import (
	"context"
	"fmt"
	"strings"

	"agent-sprout/internal/llm"
)

// Управление контекстом: окно истории и его сжатие.
//
// Диалога «на сервере» у модели нет, каждый запрос уезжает целиком заново, поэтому
// память агента приходится строить самому. Схема здесь такая: последние HistoryDepth
// сообщений уходят как есть, а когда окно заполняется, оно закрывается -- сворачивается
// в короткий пересказ, который дальше подставляется в запрос вместо самих сообщений.
//
// Каждое следующее сжатие складывает прошлый пересказ с новым окном, поэтому память
// накапливается рекурсивно и не растёт линейно вместе с диалогом.

// Compaction -- что случилось с окном на переходе.
//
// Один тип на оба режима тумблера: отметка в ленте нужна и когда история сжата,
// и когда потеряна, -- иначе пользователь не понимает, почему агент вдруг забыл
// начало разговора.
type Compaction struct {
	// Dropped -- сжатие выключено, окно просто выброшено. Тогда Text пуст,
	// а вызова модели не было
	Dropped bool   `json:"dropped"`
	Text    string `json:"text,omitempty"`
	// Covered -- сколько сообщений окна перестали уезжать в модель
	Covered int `json:"covered"`
	// Recursive -- в сжатие вошёл предыдущий пересказ
	Recursive bool      `json:"recursive"`
	Usage     llm.Usage `json:"usage"`
	Cost      Cost      `json:"cost"`
	LatencyMs int       `json:"latencyMs"`
}

// compactSystem -- промпт сжатия.
//
// Задача редкая, но дорогая по последствиям: всё, что не попало в пересказ, агент
// забудет навсегда. Поэтому упор на факты и договорённости, а не на связность текста.
const compactSystem = "Ты сжимаешь историю диалога, чтобы она поместилась в контекст. " +
	"Верни пересказ той части разговора, которую тебе дали. " +
	"Обязательно сохрани: факты и числа, имена и названия, поставленные задачи, " +
	"принятые решения, договорённости о формате работы и вопросы, оставшиеся без ответа. " +
	"Выброси приветствия, благодарности, повторы и рассуждения, не повлиявшие на результат. " +
	"Если тебе дали пересказ более ранней части диалога, перенеси все факты из него " +
	"в новый пересказ целиком: терять их нельзя, второго шанса вспомнить не будет. " +
	"Пиши по-русски, сжато, короткими пунктами или предложениями, от третьего лица. " +
	"Не добавляй вступлений вроде «вот пересказ» и не комментируй свою работу: " +
	"верни только сам пересказ."

// compactMaxTokens -- потолок на пересказ. Он должен быть заметно меньше окна,
// иначе сжатие теряет смысл, но у рассуждающих моделей часть бюджета уходит
// во внутреннее рассуждение, поэтому впритык ставить нельзя.
const compactMaxTokens = 2048

// summaryPreamble -- под каким видом пересказ уезжает в запрос.
const summaryPreamble = "Краткое содержание предыдущей части этого диалога. " +
	"Сами сообщения уже не отправляются -- опирайся на этот пересказ как на свою память:\n\n"

// compact сворачивает окно истории в пересказ отдельным вызовом модели.
//
// Модель берётся та же, что отвечает в чате: сравнивать поведение агента имеет смысл
// при одном исполнителе, да и отдельная настройка ради этого не окупается.
func (a *Agent) compact(
	ctx context.Context,
	model Model,
	provider llm.Provider,
	summary string,
	window []Message,
) (*Compaction, error) {
	recursive := strings.TrimSpace(summary) != ""

	resp, err := a.llm.Chat(ctx, provider, llm.Request{
		Model: model.ID,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: compactSystem},
			{Role: llm.RoleUser, Content: compactPayload(summary, window)},
		},
		// нулевая температура: пересказ должен быть точным, а не разнообразным
		Temperature: 0,
		MaxTokens:   compactMaxTokens,
		TopP:        1,
	})
	if err != nil {
		return nil, err
	}

	text := strings.TrimSpace(resp.Text)
	if text == "" {
		// пустой пересказ хуже отсутствия сжатия: окно уже было бы выброшено,
		// а замены ему нет -- значит ход надо отменить и дать повторить
		return nil, fmt.Errorf("модель вернула пустой пересказ (finish_reason=%s)", resp.FinishReason)
	}

	return &Compaction{
		Text:      text,
		Covered:   len(window),
		Recursive: recursive,
		Usage:     resp.Usage,
		Cost:      model.Cost(resp.Usage, a.now()),
		LatencyMs: resp.LatencyMs,
	}, nil
}

// compactPayload собирает то, что надо пересказать: прошлый пересказ и окно.
func compactPayload(summary string, window []Message) string {
	var payload strings.Builder

	if strings.TrimSpace(summary) != "" {
		payload.WriteString("Пересказ более ранней части диалога (уже сжатой):\n")
		payload.WriteString(summary)
		payload.WriteString("\n\nПродолжение диалога, которое нужно сжать вместе с ним:\n")
	} else {
		payload.WriteString("Диалог, который нужно сжать:\n")
	}

	for _, message := range window {
		speaker := "Пользователь"
		if message.Role == llm.RoleAssistant {
			speaker = "Ассистент"
		}
		payload.WriteString(speaker)
		payload.WriteString(": ")
		payload.WriteString(message.Content)
		payload.WriteString("\n")
	}

	return payload.String()
}
