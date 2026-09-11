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
	// KindSummary -- саммари свёрнутой части диалога. Не реплика собеседника,
	// а служебная отметка: с этого места история заменена коротким пересказом
	KindSummary = "summary"
	// KindDropped -- история отброшена без сжатия, потому что тумблер выключен.
	// Тоже граница окна, но памяти после неё не остаётся
	KindDropped = "dropped"
)

// IsBoundary -- закрывает ли сообщение окно истории. Всё, что до границы,
// в модель больше не уезжает.
func IsBoundary(kind string) bool {
	return kind == KindSummary || kind == KindDropped
}

// Message -- одно сообщение ленты.
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	// Compaction заполнен у отметок о границе окна: сколько сообщений выпало
	// из контекста и вошло ли в сжатие предыдущее саммари.
	Compaction *Compaction `json:"compaction,omitempty"`
	// Meta заполнена только у ответов модели: всё, что относится к выходу вызова.
	Meta *Meta `json:"meta,omitempty"`
	// Input заполнен только у вопросов пользователя: во что обошёлся вход запроса,
	// который этот вопрос вызвал. У вопросов, отклонённых политикой, пуст --
	// вызова не было, платить не за что.
	Input *InputMeta `json:"input,omitempty"`
}

// Compaction -- служебные данные отметки о границе окна.
type Compaction struct {
	// Covered -- сколько сообщений окна перестали уезжать в модель
	Covered int `json:"covered"`
	// Recursive -- в сжатие вошло предыдущее саммари, то есть это уже не первый
	// переход и пересказ склеен из пересказа
	Recursive bool `json:"recursive"`
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

// CompactionMessage превращает результат сжатия в отметку ленты.
//
// Метрики служебного вызова живут на самой отметке, а не в метриках хода: так каждая
// цифра в ленте принадлежит ровно одному сообщению, и сумма по чату сходится.
func CompactionMessage(c *agent.Compaction, model string) *Message {
	if c == nil {
		return nil
	}

	message := &Message{
		Role:       llm.RoleSystem,
		Kind:       KindDropped,
		Compaction: &Compaction{Covered: c.Covered, Recursive: c.Recursive},
	}

	// отбрасывание не стоит ни одного вызова: отмечать нечего, кроме самого факта
	if c.Dropped {
		return message
	}

	message.Kind = KindSummary
	message.Content = c.Text
	message.Input = &InputMeta{
		Model:     model,
		Tokens:    c.Usage.PromptTokens,
		CacheHit:  c.Usage.PromptCacheHitTokens,
		CacheMiss: c.Usage.PromptCacheMissTokens,
		USD:       c.Cost.InputUSD,
		OffPeak:   c.Cost.OffPeak,
	}
	message.Meta = &Meta{
		Model:     model,
		Usage:     c.Usage,
		Reasoning: c.Usage.ReasoningTokens(),
		Cost:      c.Cost,
		TotalUSD:  c.Cost.USD,
		LatencyMs: c.LatencyMs,
		Calls:     1,
	}
	return message
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
	// Facts -- метрики обновления памяти на этом ходе. Сам набор фактов сюда
	// не копируется: он лежит на чате, и дублировать его в каждом сообщении незачем
	Facts    *agent.FactsUpdate `json:"facts,omitempty"`
	Warnings []string           `json:"warnings,omitempty"`
	Trace    []agent.Step       `json:"trace,omitempty"`
}

// MetaFrom переносит результат прохода агента в метрики сообщения.
func MetaFrom(out agent.RunOutput) *Meta {
	var facts *agent.FactsUpdate
	if out.Facts != nil {
		trimmed := *out.Facts
		trimmed.Facts = nil
		facts = &trimmed
	}

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
		Facts:        facts,
		Warnings:     out.Warnings,
		Trace:        out.Trace,
	}
}

// Chat -- чат целиком: настройки агента, память и вся история.
type Chat struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Config   agent.Config `json:"config"`
	Messages []Message    `json:"messages"`
	// Facts -- key-value память, накопленная за весь диалог. В отличие от саммари
	// живёт не в ленте, а на чате: она переживает закрытие окна и копится дальше
	Facts []agent.Fact `json:"facts,omitempty"`
	// Tag -- метка ветки, задаётся при чекпоинте и дальше не меняется.
	// Пусто у обычных чатов: клона от обычного чата отличают только метки ветки
	Tag string `json:"tag,omitempty"`
	// ClonedAt -- когда ветка отпочковалась от родителя
	ClonedAt *time.Time `json:"clonedAt,omitempty"`
	// ParentID -- из какого чата сделан клон. Нужен, чтобы показать дерево веток:
	// без ссылки на родителя две ветки от одной точки не нарисовать
	ParentID  string    `json:"parentId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ApplyConfig меняет настройки чата, обнуляя память там, где она перестала быть
// осмысленной.
//
// Снятая галочка фактов стирает накопленное: пользователь именно так их и сбрасывает,
// а держать невидимую память, которая никуда не уезжает, но ждёт своего часа, --
// верный способ однажды удивиться.
func (c *Chat) ApplyConfig(cfg agent.Config) {
	if c.Config.StickyFacts && !cfg.StickyFacts {
		c.Facts = nil
	}
	c.Config = cfg
}

// Summary -- строка списка чатов: без истории, но со сводкой по ней.
type Summary struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Config   agent.Config `json:"config"`
	Messages int          `json:"messages"`
	// метки ветки: по ним список строит дерево и показывает, откуда чат взялся
	Tag      string     `json:"tag,omitempty"`
	ClonedAt *time.Time `json:"clonedAt,omitempty"`
	ParentID string     `json:"parentId,omitempty"`
	Facts    int        `json:"facts"`
	// Вход и выход считаются раздельно: вход растёт с каждым ходом, выход нет,
	// и на суммах эта разница особенно заметна
	TotalIn   int       `json:"totalIn"`
	TotalOut  int       `json:"totalOut"`
	TotalUSD  float64   `json:"totalUsd"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Window -- то, что уедет в модель вместо всего диалога: текущее саммари
// и хвост сообщений после него.
type Window struct {
	// Summary -- пересказ свёрнутой части диалога. Пусто, если сжатия ещё не было
	Summary string
	// Messages -- сообщения после последнего сжатия, они идут в модель как есть
	Messages []agent.Message
	// Compactions -- сколько раз история этого чата уже сворачивалась
	Compactions int
}

// Window собирает контекст чата: саммари плюс сообщения после него.
//
// Граница окна -- последнее сообщение вида KindSummary. Всё, что до него, в модель
// не уезжает: оно заменено пересказом. Так саммари хранится ровно в одном месте
// и одновременно видно человеку в ленте.
//
// В окно попадают только состоявшиеся обмены. Отказы политики и сбои провайдера
// отбрасываются: они остаются в ленте для человека, но модели их показывать
// бессмысленно и вредно.
//
// Вопрос без ответа отбрасывается вместе со своим сбоем. Иначе получается ловушка:
// запрос, не влезший в окно контекста, оставляет свой огромный текст в истории,
// каждая следующая попытка становится ещё тяжелее, и выбраться из переполнения
// можно только очисткой чата.
func (c Chat) Window() Window {
	window := Window{Messages: make([]agent.Message, 0, len(c.Messages))}

	// граница окна -- последняя служебная отметка. Текст пересказа при этом берётся
	// у последнего саммари: если сжатие выключили посреди чата, отметки об отбрасывании
	// закрывают окно, но уже накопленную память не стирают
	start := 0
	for i, message := range c.Messages {
		if !IsBoundary(message.Kind) {
			continue
		}
		if message.Kind == KindSummary {
			window.Summary = message.Content
		}
		window.Compactions++
		start = i + 1
	}

	for i := start; i < len(c.Messages); i++ {
		message := c.Messages[i]
		switch message.Kind {
		case KindAnswer:
			window.Messages = append(window.Messages, agent.Message{Role: message.Role, Content: message.Content})
		case KindQuestion:
			answered := i+1 < len(c.Messages) && c.Messages[i+1].Kind == KindAnswer
			if answered {
				window.Messages = append(window.Messages, agent.Message{Role: message.Role, Content: message.Content})
			}
		}
	}

	return window
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
		// сжатие -- служебный вызов: его размер ничего не говорит о том,
		// сколько места в окне занимает сам диалог
		if IsBoundary(message.Kind) {
			continue
		}
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
		Tag:       c.Tag,
		ClonedAt:  c.ClonedAt,
		ParentID:  c.ParentID,
		Facts:     len(c.Facts),
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

	if c.Facts != nil {
		copied.Facts = make([]agent.Fact, len(c.Facts))
		copy(copied.Facts, c.Facts)
	}

	return copied
}
