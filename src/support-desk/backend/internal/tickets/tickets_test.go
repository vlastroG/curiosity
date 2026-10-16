package tickets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type idInput struct {
	ID int64 `json:"id"`
}

type anyInput struct {
	Query  string `json:"query,omitempty"`
	Status string `json:"status,omitempty"`
}

// stub -- MCP-сервер с теми же именами инструментов, что у mcp-tickets; calls -- кто вызывался.
func stub(t *testing.T, calls *[]string) *Client {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "stub", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ticket_get", Description: "get"},
		func(_ context.Context, _ *mcp.CallToolRequest, in idInput) (*mcp.CallToolResult, Ticket, error) {
			*calls = append(*calls, "ticket_get")
			return nil, Ticket{ID: in.ID, Title: "Вход", Status: "open"}, nil
		})
	for _, name := range []string{"ticket_search", "ticket_list", "ticket_create", "ticket_update"} {
		mcp.AddTool(server, &mcp.Tool{Name: name, Description: name},
			func(_ context.Context, _ *mcp.CallToolRequest, _ anyInput) (*mcp.CallToolResult, ListOutput, error) {
				*calls = append(*calls, name)
				return nil, ListOutput{Tickets: []Ticket{{ID: 1, Status: "open"}}}, nil
			})
	}
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(srv.Close)
	return New(srv.URL, 5*time.Second)
}

func TestModelToolsReadOnly(t *testing.T) {
	var calls []string
	tools, err := stub(t, &calls).ModelTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "ticket_get,ticket_list,ticket_search" {
		t.Errorf("модели отданы %v", names)
	}
}

func TestTextRefusesWrite(t *testing.T) {
	var calls []string
	text, err := stub(t, &calls).Text(context.Background(), "ticket_create", "{}")
	if err != nil || !strings.Contains(text, "инструмент недоступен") || len(calls) != 0 {
		t.Errorf("ответ %q, ошибка %v, вызовы %v", text, err, calls)
	}
}

func TestGetDecodes(t *testing.T) {
	var calls []string
	tk, err := stub(t, &calls).Get(context.Background(), 7)
	if err != nil || tk.ID != 7 || tk.Status != "open" {
		t.Errorf("%+v %v", tk, err)
	}
}
