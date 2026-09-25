package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Text string `json:"text"`
}

type echoOut struct {
	From string `json:"from"`
	Text string `json:"text"`
}

// mcpServer -- настоящий MCP-сервер по HTTP с инструментом echo.
// У двух серверов одинаковое имя инструмента -- реестр должен их различать.
func mcpServer(t *testing.T, name string) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: name, Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "повторить"}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name + ": " + in.Text}}}, echoOut{From: name, Text: in.Text}, nil
	})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestParse(t *testing.T) {
	servers, err := Parse(" a=http://x/mcp , b=https://y/mcp/travel,")
	if err != nil || len(servers) != 2 || servers[1].Endpoint != "https://y/mcp/travel" {
		t.Fatalf("%+v %v", servers, err)
	}
	for _, bad := range []string{"", "a", "a=ftp://x", "a__b=http://x", "a=http://x,a=http://y"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q принято", bad)
		}
	}
}

func TestRouting(t *testing.T) {
	servers, _ := Parse("one=" + mcpServer(t, "one") + ",two=" + mcpServer(t, "two") + ",down=http://127.0.0.1:1/mcp")
	reg := New(servers, 5*time.Second)
	reg.Discover(context.Background())

	state := map[string]Server{}
	for _, s := range reg.Servers() {
		state[s.Name] = s
	}
	if !state["one"].Available || state["down"].Available || state["down"].Error == "" {
		t.Errorf("доступность: %+v", state)
	}

	tools := reg.ModelTools()
	names := []string{}
	for _, tool := range tools {
		names = append(names, tool.Name)
		if !strings.HasPrefix(tool.Description, "[сервер ") {
			t.Errorf("в описании нет сервера: %s", tool.Description)
		}
	}
	if strings.Join(names, ",") != "one__echo,two__echo" {
		t.Errorf("инструменты для модели: %v", names)
	}

	// одинаковое имя инструмента -- разные серверы
	for _, target := range []string{"one", "two"} {
		server, tool, res, err := reg.Call(context.Background(), target+"__echo", json.RawMessage(`{"text":"привет"}`))
		var out echoOut
		json.Unmarshal(res.Structured, &out)
		if err != nil || server != target || tool != "echo" || out.From != target || res.Text != target+": привет" {
			t.Errorf("%s: %s %s %+v %v", target, server, tool, res, err)
		}
	}

	for _, bad := range []string{"echo", "three__echo", "one__missing", "down__echo"} {
		if _, _, _, err := reg.Call(context.Background(), bad, nil); err == nil {
			t.Errorf("%s маршрутизирован", bad)
		}
	}

	var out echoOut
	if err := reg.CallDirect(context.Background(), "two", "echo", map[string]string{"text": "x"}, &out); err != nil || out.From != "two" {
		t.Errorf("CallDirect: %+v %v", out, err)
	}
}
