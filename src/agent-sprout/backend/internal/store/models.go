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
	// KindOverflow -- запрос не влез в окно контекста модели. Отделён от прочих сбоев:
	// это не авария провайдера, а прямое следствие размера диалога
	KindOverflow = "overflow"
)

// Message -- одно сообщение ленты.
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	// Meta заполнена только у ответов модели: всё, что относится к выходу вызова.
	Meta *Meta `json:"meta,omitempty"`
	// Input заполнен только у вопросов пользователя: во что обошёлся вход запроса,
	// который этот вопрос вызвал. У вопросов, отклонённых политикой, пуст --
	// вызова не было, платить не за что.
	Input *InputMeta `json:"input,omitempty"`
}

// InputMeta -- метрики входа: сколько токенов и денег стоил запрос целиком.
//
// Отделены от Meta, потому что относятся к разным сообщениям ленты. prompt_tokens --
// это весь вызов (system prompt + история + вопрос), поэтому число показывается под
// вопросом, который этот вызов породил, а не под ответом модели.
type InputMeta struct {
	Model     string `json:"model"`
	Tokens    int    `json:"tokens"`
	CacheHit  int    `json:"cacheHit"`
	CacheMiss int    `json:"cacheMiss"`
	// HistoryMessages -- сколько сообщений истории уехало вместе с вопросом
	HistoryMessages int `json:"historyMessages"`
	// Delta -- насколько вход вырос против прошлого запроса этого чата.
	// Точная разница двух чисел API, а не оценка. Бывает отрицательной, когда
	// историю обрезало по глубине
	Delta    int     `json:"delta"`
	HasDelta bool    `json:"hasDelta"`
	USD      float64 `json:"usd"`
	OffPeak  bool    `json:"offPeak"`
}

// InputFrom собирает метрики входа из результата прохода агента.
func InputFrom(out agent.RunOutput) *InputMeta {
	// prompt_tokens прошлого вызова известен, только если вызов был
	hasDelta := out.Context.LastPrompt > 0

	delta := 0
	if hasDelta {
		delta = out.Usage.PromptTokens - out.Context.LastPrompt
	}

	return &InputMeta{
		Model:           out.Model,
		Tokens:          out.Usage.PromptTokens,
		CacheHit:        out.Usage.PromptCacheHitTokens,
		CacheMiss:       out.Usage.PromptCacheMissTokens,
		HistoryMessages: out.HistoryMessages,
		Delta:           delta,
		HasDelta:        hasDelta,
		USD:             out.Cost.InputUSD,
		OffPeak:         out.Cost.OffPeak,
	}
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
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Config   agent.Config `json:"config"`
	Messages int          `json:"messages"`
	// Вход и выход считаются раздельно: вход растёт с каждым ходом, выход нет,
	// и на суммах эта разница особенно заметна
	TotalIn   int       `json:"totalIn"`
	TotalOut  int       `json:"totalOut"`
	TotalUSD  float64   `json:"totalUsd"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// History отдаёт историю в виде, который понимает агент.
//
// В контекст уходят только состоявшиеся обмены. Отказы политики и сбои провайдера
// отбрасываются: они остаются в ленте для человека, но модели их показывать
// бессмысленно и вредно.
//
// Вопрос без ответа отбрасывается вместе со своим сбоем. Иначе получается ловушка:
// запрос, не влезший в окно контекста, оставляет свой огромный текст в истории,
// каждая следующая попытка становится ещё тяжелее, и выбраться из переполнения
// можно только очисткой чата.
func (c Chat) History() []agent.Message {
	history := make([]agent.Message, 0, len(c.Messages))

	for i, message := range c.Messages {
		switch message.Kind {
		case KindAnswer:
			history = append(history, agent.Message{Role: message.Role, Content: message.Content})
		case KindQuestion:
			answered := i+1 < len(c.Messages) && c.Messages[i+1].Kind == KindAnswer
			if answered {
				history = append(history, agent.Message{Role: message.Role, Content: message.Content})
			}
		}
	}

	return history
}

// LastTurn -- числа последнего состоявшегося вызова модели в этом чате.
//
// Нужны агенту, чтобы посчитать заполненность окна контекста по факту. Ищем последнее
// сообщение, у которого есть реальный prompt_tokens: это либо ответ модели, либо ответ,
// отклонённый выходной политикой (вызов состоялся, токены потрачены). У второго видимая
// часть не считается -- в историю следующего запроса он не уезжает.
func (c Chat) LastTurn() agent.LastTurn {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		message := c.Messages[i]
		if message.Meta == nil || message.Meta.Usage.PromptTokens == 0 {
			continue
		}

		turn := agent.LastTurn{Present: true, PromptTokens: message.Meta.Usage.PromptTokens}
		if message.Kind == KindAnswer {
			turn.CompletionTokens = message.Meta.Usage.CompletionTokens
			turn.ReasoningTokens = message.Meta.Reasoning
		}
		return turn
	}
	return agent.LastTurn{}
}

// summary считает сводку по чату для списка.
func (c Chat) summary() Summary {
	summary := Summary{
		ID:        c.ID,
		Title:     c.Title,
		Config:    c.Config,
		Messages:  len(c.Messages),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}

	for _, message := range c.Messages {
		if message.Input != nil {
			summary.TotalIn += message.Input.Tokens
		}
		if message.Meta != nil {
			summary.TotalOut += message.Meta.Usage.CompletionTokens
			summary.TotalUSD += message.Meta.TotalUSD
		}
	}
	return summary
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
