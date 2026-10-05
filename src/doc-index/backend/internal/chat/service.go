package chat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"doc-index/internal/rag"
)

// MaxText -- длина реплики.
const MaxText = 2000

// Keep -- сколько последних сообщений остаётся несжатыми.
const Keep = 4

// Replier отвечает на реплику (rag.Agent или заглушка).
type Replier interface {
	Reply(ctx context.Context, c rag.Conversation, text string, st rag.Settings) rag.Result
}

// Summarizer сворачивает старые сообщения в сводку (rag.Compressor или заглушка).
type Summarizer interface {
	Compress(ctx context.Context, summary string, turns []rag.Turn) (string, error)
}

// Service -- ход беседы.
type Service struct {
	Store      *Store
	Agent      Replier
	Compressor Summarizer
	Timeout    time.Duration // на одно сообщение; 0 -- 5 минут

	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

func (s *Service) lock(id int64) func() {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[int64]*sync.Mutex{}
	}
	l, ok := s.locks[id]
	if !ok {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Send -- реплика пользователя: поиск, ответ, новая память задачи и, если
// несжатых сообщений стало больше порога, сжатие. Сообщения в один чат
// обрабатываются по очереди. Ответ доводится до конца, даже если клиент
// отключился: иначе реплика пропала бы вместе с ответом.
func (s *Service) Send(ctx context.Context, id int64, text string) (*Chat, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return nil, errors.New("пустое сообщение")
	case len([]rune(text)) > MaxText:
		return nil, fmt.Errorf("сообщение длиннее %d символов", MaxText)
	}
	unlock := s.lock(id)
	defer unlock()

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	c, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	seq := len(c.Messages) + 1
	conv := rag.Conversation{State: c.State, Summary: c.Summary, Recent: turns(c.Messages, c.SummarizedUpto), Seq: seq}
	res := s.Agent.Reply(ctx, conv, text, c.Settings.Settings)

	reply := Message{Seq: seq + 1, Role: RoleAssistant, Text: res.Text, Error: res.Error}
	stored := res
	stored.Answer = rag.Answer{Ms: res.Ms}
	reply.Result = &stored
	if err := s.Store.appendTurn(ctx, id, Message{Seq: seq, Role: RoleUser, Text: text}, reply, res.State); err != nil {
		return nil, err
	}

	all := append(c.Messages, Message{Seq: seq, Role: RoleUser, Text: text}, reply)
	if err := s.compress(ctx, c, all); err != nil {
		// сжатие не удалось -- не беда: попробуем на следующей реплике
		log.Printf("чат %d: сжатие: %v", id, err)
	}
	return s.Store.Get(ctx, id)
}

// compress сворачивает всё, кроме последних Keep сообщений, если несжатых
// больше порога чата.
func (s *Service) compress(ctx context.Context, c *Chat, all []Message) error {
	var fresh []Message
	for _, m := range all {
		if m.Seq > c.SummarizedUpto {
			fresh = append(fresh, m)
		}
	}
	if len(fresh) <= c.Settings.CompressAfter || len(fresh) <= Keep {
		return nil
	}
	old := fresh[:len(fresh)-Keep]
	upto := old[len(old)-1].Seq
	summary, err := s.Compressor.Compress(ctx, c.Summary, turns(old, 0))
	if err != nil {
		return err
	}
	return s.Store.setSummary(ctx, c.ID, summary, upto)
}

// turns -- несжатые сообщения для промпта. Неудавшиеся ответы пропускаются.
func turns(ms []Message, after int) []rag.Turn {
	out := []rag.Turn{}
	for _, m := range ms {
		if m.Seq <= after || m.Error != "" || strings.TrimSpace(m.Text) == "" {
			continue
		}
		out = append(out, rag.Turn{Role: m.Role, Text: m.Text})
	}
	return out
}
