// Package assistant -- ответ на одно сообщение пользователя, без истории:
// защита → поиск по документации → модель с инструментами чтения тикетов.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"support-desk/internal/docs"
	"support-desk/internal/guard"
	"support-desk/internal/llm"
	"support-desk/internal/tickets"
)

const (
	maxQuestion = 2000
	topK        = 6
	maxRounds   = 4
	maxAnswer   = 4000
)

// ErrInput -- пустое или слишком длинное сообщение.
var ErrInput = errors.New("сообщение должно быть от 1 до 2000 символов")

type Searcher interface {
	Search(ctx context.Context, query string, k int) ([]docs.Hit, error)
}

type Checker interface {
	Check(ctx context.Context, message string) (guard.Verdict, error)
}

type Tickets interface {
	ModelTools(ctx context.Context) ([]llm.Tool, error)
	Text(ctx context.Context, name, arguments string) (string, error)
	Get(ctx context.Context, id int64) (tickets.Ticket, error)
}

// Chatter -- модель ответов: llm.Client с выбранным провайдером.
type Chatter interface {
	Chat(ctx context.Context, req llm.Request) (llm.Response, error)
}

// Assistant -- всё, что нужно для ответа.
type Assistant struct {
	Search  Searcher
	Guard   Checker
	Tickets Tickets
	LLM     Chatter
	Model   llm.Model
	Product string
}

// Source -- фрагмент документации, на который опирался ответ.
type Source struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// Reply -- ответ на сообщение.
type Reply struct {
	Answer  string        `json:"answer"`
	Blocked string        `json:"blocked"`
	Sources []Source      `json:"sources"`
	Tickets []int64       `json:"tickets"`
	Guard   guard.Verdict `json:"guard"`
}

var idPattern = regexp.MustCompile(`"id"\s*:\s*(\d+)`)

// Ask отвечает на один вопрос; ticketID != 0 -- прикреплённый тикет.
func (a *Assistant) Ask(ctx context.Context, question string, ticketID int64) (Reply, error) {
	question = strings.TrimSpace(question)
	if question == "" || utf8.RuneCountInString(question) > maxQuestion {
		return Reply{}, ErrInput
	}

	verdict, err := a.Guard.Check(ctx, question)
	if err != nil {
		return Reply{}, fmt.Errorf("проверка сообщения не удалась: %w", err)
	}
	reply := Reply{Guard: verdict, Blocked: verdict.Blocked, Sources: []Source{}, Tickets: []int64{}}
	switch verdict.Blocked {
	case guard.Injection:
		reply.Answer = fmt.Sprintf(refuseInjection, a.Product)
		return reply, nil
	case guard.OffTopic:
		reply.Answer = fmt.Sprintf(refuseOffTopic, a.Product)
		return reply, nil
	}

	hits, err := a.Search.Search(ctx, question, topK)
	if err != nil {
		return Reply{}, err
	}
	for _, h := range hits {
		reply.Sources = append(reply.Sources, Source{Path: h.Path, Title: h.Title})
	}

	var attached *tickets.Ticket
	if ticketID != 0 {
		tk, err := a.Tickets.Get(ctx, ticketID)
		if err != nil {
			return Reply{}, fmt.Errorf("тикет #%d: %w", ticketID, err)
		}
		attached = &tk
		reply.addTicket(ticketID)
	}

	tools, err := a.Tickets.ModelTools(ctx)
	if err != nil {
		return Reply{}, err
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: system(a.Product)},
		{Role: llm.RoleUser, Content: userMessage(question, attached, hits)},
	}
	for round := 0; ; round++ {
		req := llm.Request{Messages: messages, MaxTokens: min(a.Model.Budget(), maxAnswer)}
		// после maxRounds раундов инструментов -- последний вызов без них: пора отвечать
		if round < maxRounds {
			req.Tools = tools
		}
		resp, err := a.LLM.Chat(ctx, req)
		if err != nil {
			return Reply{}, err
		}
		if len(resp.ToolCalls) == 0 || req.Tools == nil {
			reply.Answer = strings.TrimSpace(resp.Text)
			if reply.Answer == "" {
				reply.Answer = emptyAnswer
			}
			return reply, nil
		}
		messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls})
		for _, call := range resp.ToolCalls {
			text, err := a.Tickets.Text(ctx, call.Function.Name, call.Function.Arguments)
			if err != nil {
				text = "Ошибка: " + err.Error()
			}
			reply.collect(call, text)
			messages = append(messages, llm.Message{Role: llm.RoleTool, Content: text, ToolCallID: call.ID})
		}
	}
}

// collect запоминает тикеты, которые видела модель: из аргументов ticket_get и
// из результатов поиска и списка.
func (r *Reply) collect(call llm.ToolCall, result string) {
	if call.Function.Name == "ticket_get" {
		var args struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil && args.ID != 0 {
			r.addTicket(args.ID)
		}
		return
	}
	for _, m := range idPattern.FindAllStringSubmatch(result, -1) {
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			r.addTicket(id)
		}
	}
}

func (r *Reply) addTicket(id int64) {
	if !slices.Contains(r.Tickets, id) {
		r.Tickets = append(r.Tickets, id)
	}
}
