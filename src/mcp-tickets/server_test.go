package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// session -- настоящий сервер на настоящем SQLite и клиент по транспорту в памяти.
func session(t *testing.T) *mcp.ClientSession {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatalf("база не открылась: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go newServer(store).Run(ctx, serverTransport)

	s, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("соединение не установилось: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	result, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("вызов %s не прошёл: %v", name, err)
	}
	if out != nil && !result.IsError {
		raw, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("structuredContent %s не разобрался: %v", name, err)
		}
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	if text, ok := result.Content[0].(*mcp.TextContent); ok {
		return text.Text
	}
	return ""
}

func create(t *testing.T, s *mcp.ClientSession, title, description string) Ticket {
	t.Helper()
	var tk Ticket
	if r := call(t, s, "ticket_create", map[string]any{"title": title, "description": description}, &tk); r.IsError {
		t.Fatalf("тикет не создался: %s", resultText(r))
	}
	return tk
}

func TestCreateGetList(t *testing.T) {
	s := session(t)
	first := create(t, s, "Первый", "описание первого")
	second := create(t, s, "Второй", "описание второго")

	var got Ticket
	call(t, s, "ticket_get", map[string]any{"id": first.ID}, &got)
	if got.Title != "Первый" || got.Status != "open" || got.CreatedAt == "" {
		t.Errorf("ticket_get вернул %+v", got)
	}

	var list ListOutput
	call(t, s, "ticket_list", map[string]any{}, &list)
	if len(list.Tickets) != 2 || list.Tickets[0].ID != second.ID {
		t.Errorf("ticket_list: ждали свежий первым, получили %+v", list.Tickets)
	}
}

func TestSearchRussianPrefix(t *testing.T) {
	s := session(t)
	tk := create(t, s, "Не работает вход", "После обновления не могу войти в аккаунт")
	create(t, s, "Медленно открывается проект", "Revit думает минуту")

	var found ListOutput
	call(t, s, "ticket_search", map[string]any{"query": "почему вход не работает"}, &found)
	if len(found.Tickets) == 0 || found.Tickets[0].ID != tk.ID {
		t.Errorf("поиск не нашёл тикет про вход: %+v", found.Tickets)
	}

	var none ListOutput
	r := call(t, s, "ticket_search", map[string]any{"query": `"; DROP`}, &none)
	if r.IsError || len(none.Tickets) != 0 {
		t.Errorf("синтаксис FTS не должен ни ломать поиск, ни находить: %s %+v", resultText(r), none.Tickets)
	}
}

func TestUpdate(t *testing.T) {
	s := session(t)
	tk := create(t, s, "Вход", "не входит")

	var updated Ticket
	call(t, s, "ticket_update", map[string]any{"id": tk.ID, "status": "resolved", "resolution": "переустановить"}, &updated)
	var got Ticket
	call(t, s, "ticket_get", map[string]any{"id": tk.ID}, &got)
	if got.Status != "resolved" || got.Resolution != "переустановить" {
		t.Errorf("после обновления: %+v", got)
	}

	if r := call(t, s, "ticket_update", map[string]any{"id": tk.ID, "status": "done"}, nil); !r.IsError {
		t.Error("неизвестный статус должен быть ошибкой")
	}
}

func TestValidation(t *testing.T) {
	s := session(t)
	if r := call(t, s, "ticket_create", map[string]any{"title": "  ", "description": "x"}, nil); !r.IsError {
		t.Error("пустой заголовок должен быть ошибкой")
	}
	r := call(t, s, "ticket_get", map[string]any{"id": 999}, nil)
	if !r.IsError || !strings.Contains(resultText(r), "не найден") {
		t.Errorf("ждали «не найден», получили %q", resultText(r))
	}
}
