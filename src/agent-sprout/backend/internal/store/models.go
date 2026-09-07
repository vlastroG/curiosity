// Package store хранит чаты: настройки и историю сообщений.
//
// Хранилище держит всё в памяти и после каждой мутации переписывает снапшот в JSON-файл.
// Для учебного проекта этого достаточно: объёмы такие, что полная перезапись дешевле
// любой инкрементальной схемы, зато чаты переживают перезапуск контейнера.
package store

import (
	"time"

	"agent-sprout/internal/agent"
	"agent-sprout/internal/llm"
)

// Виды сообщений в ленте. Отказ политики и сбой провайдера остаются в истории, чтобы
// было видно, что произошло, но в модель они больше не отправляются.
const (
	KindQuestion = "question"
	KindAnswer   = "answer"
	KindBlocked  = "blocked"
	KindFailed   = "failed"
)

// Message -- одно сообщение ленты.
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	// Meta заполнена только у ответов модели.
	Meta *Meta `json:"meta,omitempty"`
}

// Meta -- метрики одного прохода агента, которые показываются под ответом.
type Meta struct {
	Model        string              `json:"model"`
	Usage        llm.Usage           `json:"usage"`
	Reasoning    int                 `json:"reasoningTokens"`
	Cost         agent.Cost          `json:"cost"`
	TotalUSD     float64             `json:"totalUsd"`
	LatencyMs    int                 `json:"latencyMs"`
	FinishReason string              `json:"finishReason"`
	Calls        int                 `json:"calls"`
	Judge        *agent.JudgeVerdict `json:"judge,omitempty"`
	Warnings     []string            `json:"warnings,omitempty"`
	Trace        []agent.Step        `json:"trace,omitempty"`
}

// MetaFrom переносит результат прохода агента в метрики сообщения.
func MetaFrom(out agent.RunOutput) *Meta {
	return &Meta{
		Model:        out.Model,
		Usage:        out.Usage,
		Reasoning:    out.Usage.ReasoningTokens(),
		Cost:         out.Cost,
		TotalUSD:     out.TotalUSD,
		LatencyMs:    out.LatencyMs,
		FinishReason: out.FinishReason,
		Calls:        out.Calls,
		Judge:        out.Judge,
		Warnings:     out.Warnings,
		Trace:        out.Trace,
	}
}

// Chat -- чат целиком: настройки агента и вся история.
type Chat struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Config    agent.Config `json:"config"`
	Messages  []Message    `json:"messages"`
	CreatedAt time.Time    `json:"createdAt"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// Summary -- строка списка чатов: без истории, но со сводкой по ней.
type Summary struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Config    agent.Config `json:"config"`
	Messages  int          `json:"messages"`
	TotalUSD  float64      `json:"totalUsd"`
	CreatedAt time.Time    `json:"createdAt"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// History отдаёт историю в виде, который понимает агент.
//
// Отказы политики и сбои провайдера отбрасываются: они остаются в ленте для человека,
// но модели их показывать бессмысленно и вредно -- это не часть диалога.
func (c Chat) History() []agent.Message {
	history := make([]agent.Message, 0, len(c.Messages))
	for _, message := range c.Messages {
		if message.Kind != KindQuestion && message.Kind != KindAnswer {
			continue
		}
		history = append(history, agent.Message{Role: message.Role, Content: message.Content})
	}
	return history
}

// summary считает сводку по чату для списка.
func (c Chat) summary() Summary {
	total := 0.0
	for _, message := range c.Messages {
		if message.Meta != nil {
			total += message.Meta.TotalUSD
		}
	}
	return Summary{
		ID:        c.ID,
		Title:     c.Title,
		Config:    c.Config,
		Messages:  len(c.Messages),
		TotalUSD:  total,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// clone копирует чат перед выдачей наружу: вызывающий не должен уметь править
// то, что лежит под замком хранилища.
//
// Копия поверхностная по содержимому Meta: трейс и вердикт судьи после создания
// не меняются, поэтому делить их между копиями безопасно.
func (c Chat) clone() Chat {
	copied := c
	copied.Messages = make([]Message, len(c.Messages))
	copy(copied.Messages, c.Messages)
	return copied
}
