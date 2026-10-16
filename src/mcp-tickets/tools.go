package main

// Инструменты сервера тикетов. Модели ассистента достаются только чтение
// (ticket_search, ticket_get, ticket_list); создаёт и меняет тикеты интерфейс.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CreateInput -- аргументы ticket_create.
type CreateInput struct {
	Title       string `json:"title" jsonschema:"короткий заголовок, до 200 символов"`
	Description string `json:"description" jsonschema:"описание проблемы, до 5000 символов"`
}

// IDInput -- номер тикета.
type IDInput struct {
	ID int64 `json:"id" jsonschema:"номер тикета"`
}

// ListInput -- аргументы ticket_list.
type ListInput struct {
	Status string `json:"status,omitempty" jsonschema:"open, in_progress или resolved; пусто -- все"`
	Limit  int    `json:"limit,omitempty" jsonschema:"сколько тикетов вернуть, по умолчанию 20, не больше 100"`
}

// SearchInput -- аргументы ticket_search.
type SearchInput struct {
	Query string `json:"query" jsonschema:"слова для поиска по заголовку, описанию и решению"`
	Limit int    `json:"limit,omitempty" jsonschema:"сколько тикетов вернуть, по умолчанию 5, не больше 20"`
}

// UpdateInput -- аргументы ticket_update.
type UpdateInput struct {
	ID         int64  `json:"id" jsonschema:"номер тикета"`
	Status     string `json:"status,omitempty" jsonschema:"open, in_progress или resolved; пусто -- не менять"`
	Resolution string `json:"resolution,omitempty" jsonschema:"как решили проблему; пусто -- не менять"`
}

// ListOutput -- список тикетов.
type ListOutput struct {
	Tickets []Ticket `json:"tickets"`
}

func newServer(store *Store) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-tickets", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ticket_create",
		Description: "Создать тикет поддержки со статусом open.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateInput) (*mcp.CallToolResult, Ticket, error) {
		title, description := strings.TrimSpace(in.Title), strings.TrimSpace(in.Description)
		switch {
		case title == "" || description == "":
			return nil, Ticket{}, fmt.Errorf("нужны и заголовок, и описание")
		case utf8.RuneCountInString(title) > 200:
			return nil, Ticket{}, fmt.Errorf("заголовок длиннее 200 символов")
		case utf8.RuneCountInString(description) > 5000:
			return nil, Ticket{}, fmt.Errorf("описание длиннее 5000 символов")
		}
		tk, err := store.Create(ctx, title, description)
		return nil, tk, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ticket_get",
		Description: "Тикет по номеру: заголовок, описание, статус, решение, даты.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in IDInput) (*mcp.CallToolResult, Ticket, error) {
		tk, err := store.Get(ctx, in.ID)
		return nil, tk, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ticket_list",
		Description: "Последние тикеты, свежие первыми; можно отфильтровать по статусу.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListInput) (*mcp.CallToolResult, ListOutput, error) {
		if in.Status != "" && !slices.Contains(Statuses, in.Status) {
			return nil, ListOutput{}, fmt.Errorf("неизвестный статус %q", in.Status)
		}
		tickets, err := store.List(ctx, in.Status, clamp(in.Limit, 20, 100))
		return nil, ListOutput{Tickets: tickets}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "ticket_search",
		Description: "Найти тикеты по словам: похожие проблемы и как их решили. " +
			"Ищет по заголовку, описанию и решению, самые подходящие первыми.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, ListOutput, error) {
		tickets, err := store.Search(ctx, in.Query, clamp(in.Limit, 5, 20))
		if tickets == nil {
			tickets = []Ticket{}
		}
		return nil, ListOutput{Tickets: tickets}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ticket_update",
		Description: "Сменить статус тикета и/или записать решение.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateInput) (*mcp.CallToolResult, Ticket, error) {
		if in.Status != "" && !slices.Contains(Statuses, in.Status) {
			return nil, Ticket{}, fmt.Errorf("неизвестный статус %q; допустимы: %s", in.Status, strings.Join(Statuses, ", "))
		}
		tk, err := store.Update(ctx, in.ID, in.Status, strings.TrimSpace(in.Resolution))
		return nil, tk, err
	})

	return server
}

// clamp -- n в пределах (0, max], 0 и меньше -- fallback.
func clamp(n, fallback, max int) int {
	if n <= 0 {
		return fallback
	}
	return min(n, max)
}
