package rag

import (
	"context"
	"strings"
	"time"

	"doc-index/internal/llm"
)

const plainSystem = `Ты — полезный ассистент. Отвечай на языке пользователя, по существу и без лишних вступлений.`

// PlainCompressSystem -- промпт сжатия для чата без поиска.
const PlainCompressSystem = `Ты сжимаешь старую часть беседы пользователя с ассистентом, чтобы её можно было продолжить без полного текста.
Напиши сводку до 1200 символов на языке беседы: о чём спрашивал пользователь, к каким выводам пришли, какие факты, решения и договорённости важны дальше.
Прежнюю сводку, если она есть, включи в новую. Верни только текст сводки.`

// Plain -- чат без поиска (RAG=off): один вызов модели на реплику. История
// уходит модели обычными сообщениями, сжатая часть -- сводкой в системном
// промпте. Память задачи не ведётся, источников нет.
type Plain struct{ LLM LLM }

// Reply отвечает на реплику; ошибка модели попадает в Error.
func (p Plain) Reply(ctx context.Context, c Conversation, text string, st Settings) Result {
	started := time.Now()
	out := Result{Model: p.LLM.Model, Settings: st, Sources: []Source{}, State: c.State}
	reply, err := p.LLM.With(st.Gen).chat(ctx, PlainMessages(c, text))
	out.Answer = finish(reply, err, started)
	return out
}

// PlainMessages -- системный промпт со сводкой, несжатые сообщения и новая реплика.
func PlainMessages(c Conversation, text string) []llm.Message {
	system := plainSystem
	if c.Summary != "" {
		system += "\n\nСводка более ранней части беседы:\n" + c.Summary
	}
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: system}}
	for _, t := range c.Recent {
		role := llm.RoleUser
		if t.Role == "assistant" {
			role = llm.RoleAssistant
		}
		msgs = append(msgs, llm.Message{Role: role, Content: strings.TrimSpace(t.Text)})
	}
	return append(msgs, llm.Message{Role: llm.RoleUser, Content: strings.TrimSpace(text)})
}
