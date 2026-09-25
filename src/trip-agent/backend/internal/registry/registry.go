// Package registry -- реестр MCP-серверов и маршрутизатор вызовов.
//
// Агент работает не с одним сервером, а с несколькими. Реестр опрашивает каждый
// (tools/list) и показывает модели общий список инструментов с префиксом
// сервера: places__find_city, weather__trip_weather. Префикс решает две задачи:
// одинаковые имена на разных серверах не конфликтуют, и маршрутизатор по имени
// сразу знает, куда отправить вызов.
//
//	модель: «вызови money__convert{…}»
//	   │
//	   ▼
//	Route("money__convert") ──► сервер money, инструмент convert ──► http://mcp-money:8080/mcp
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"trip-agent/internal/llm"
)

// Separator -- разделитель сервера и инструмента в имени для модели.
const Separator = "__"

// Server -- один зарегистрированный MCP-сервер.
type Server struct {
	Name      string   `json:"name"`
	Endpoint  string   `json:"endpoint"`
	Available bool     `json:"available"`
	Error     string   `json:"error,omitempty"`
	Tools     []string `json:"tools"`

	tools []*mcp.Tool
}

// Result -- ответ инструмента.
type Result struct {
	Text       string
	Structured json.RawMessage
	IsError    bool
}

// Registry -- все серверы и их инструменты.
type Registry struct {
	timeout time.Duration

	mu      sync.RWMutex
	servers []*Server
}

// Parse разбирает TRIP_SERVERS: "places=http://…/mcp,weather=http://…/mcp/travel".
func Parse(spec string) ([]*Server, error) {
	var servers []*Server
	seen := map[string]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, endpoint, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.Contains(name, Separator) || !strings.HasPrefix(strings.TrimSpace(endpoint), "http") {
			return nil, fmt.Errorf("не понял сервер %q: нужен вид имя=http://адрес", part)
		}
		if seen[name] {
			return nil, fmt.Errorf("сервер %q указан дважды", name)
		}
		seen[name] = true
		servers = append(servers, &Server{Name: name, Endpoint: strings.TrimSpace(endpoint), Tools: []string{}})
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("не указано ни одного MCP-сервера")
	}
	return servers, nil
}

// New собирает реестр. Опрос серверов -- Discover.
func New(servers []*Server, timeout time.Duration) *Registry {
	return &Registry{servers: servers, timeout: timeout}
}

// Discover опрашивает все серверы параллельно. Недоступный сервер -- не ошибка
// реестра: его инструменты просто не попадут к модели, а причина будет видна.
func (r *Registry) Discover(ctx context.Context) {
	var wg sync.WaitGroup
	for _, server := range r.snapshot() {
		wg.Add(1)
		go func(s *Server) {
			defer wg.Done()
			tools, err := r.listTools(ctx, s.Endpoint)
			r.mu.Lock()
			defer r.mu.Unlock()
			if err != nil {
				s.Available, s.Error, s.tools, s.Tools = false, err.Error(), nil, []string{}
				return
			}
			s.Available, s.Error, s.tools = true, "", tools
			s.Tools = make([]string, 0, len(tools))
			for _, t := range tools {
				s.Tools = append(s.Tools, t.Name)
			}
			sort.Strings(s.Tools)
		}(server)
	}
	wg.Wait()
}

func (r *Registry) snapshot() []*Server {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*Server(nil), r.servers...)
}

// Servers -- состояние реестра для интерфейса.
func (r *Registry) Servers() []Server {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Server, 0, len(r.servers))
	for _, s := range r.servers {
		out = append(out, Server{Name: s.Name, Endpoint: s.Endpoint, Available: s.Available, Error: s.Error, Tools: append([]string(nil), s.Tools...)})
	}
	return out
}

// ModelTools -- инструменты всех доступных серверов в виде для модели.
//
// Схемы не переписываются: их писал сервер. К описанию добавляется, какой сервер
// отвечает за инструмент, -- модели проще выбрать, когда видно, из какой области вопрос.
func (r *Registry) ModelTools() []llm.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []llm.Tool
	for _, s := range r.servers {
		for _, t := range s.tools {
			schema, ok := t.InputSchema.(map[string]any)
			if !ok || schema == nil {
				schema = map[string]any{"type": "object"}
			}
			out = append(out, llm.Tool{
				Name:        s.Name + Separator + t.Name,
				Description: "[сервер " + s.Name + "] " + t.Description,
				Parameters:  schema,
			})
		}
	}
	return out
}

// Route находит сервер и инструмент по имени от модели.
func (r *Registry) Route(name string) (server *Server, tool string, err error) {
	serverName, tool, ok := strings.Cut(name, Separator)
	if !ok {
		return nil, "", fmt.Errorf("имя %q без префикса сервера: нужен вид сервер%sинструмент", name, Separator)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.servers {
		if s.Name != serverName {
			continue
		}
		if !s.Available {
			return nil, "", fmt.Errorf("сервер %s недоступен", serverName)
		}
		for _, t := range s.tools {
			if t.Name == tool {
				return s, tool, nil
			}
		}
		return nil, "", fmt.Errorf("у сервера %s нет инструмента %s", serverName, tool)
	}
	return nil, "", fmt.Errorf("сервера %s нет в реестре", serverName)
}

// Call маршрутизирует вызов: имя от модели → сервер → инструмент.
// Возвращает имя сервера и инструмента -- по ним проверяется маршрутизация.
func (r *Registry) Call(ctx context.Context, name string, arguments json.RawMessage) (string, string, Result, error) {
	server, tool, err := r.Route(name)
	if err != nil {
		return "", "", Result{}, err
	}
	var args any
	if len(arguments) > 0 && string(arguments) != "null" {
		args = arguments
	}
	session, ctx, cancel, err := r.connect(ctx, server.Endpoint)
	if err != nil {
		return server.Name, tool, Result{}, err
	}
	defer cancel()
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return server.Name, tool, Result{}, fmt.Errorf("вызов %s на сервере %s не прошёл: %w", tool, server.Name, err)
	}
	parts := []string{}
	for _, block := range res.Content {
		if t, ok := block.(*mcp.TextContent); ok && t.Text != "" {
			parts = append(parts, t.Text)
		}
	}
	out := Result{Text: strings.Join(parts, "\n"), IsError: res.IsError}
	if res.StructuredContent != nil {
		out.Structured, _ = json.Marshal(res.StructuredContent)
	}
	return server.Name, tool, out, nil
}

// CallDirect -- вызов из кода (не от модели), например trip_get для интерфейса.
func (r *Registry) CallDirect(ctx context.Context, server, tool string, args any, out any) error {
	raw, _ := json.Marshal(args)
	_, _, res, err := r.Call(ctx, server+Separator+tool, raw)
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("%s: %s", tool, res.Text)
	}
	return json.Unmarshal(res.Structured, out)
}

func (r *Registry) listTools(ctx context.Context, endpoint string) ([]*mcp.Tool, error) {
	session, ctx, cancel, err := r.connect(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	return list.Tools, nil
}

func (r *Registry) connect(ctx context.Context, endpoint string) (*mcp.ClientSession, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	client := mcp.NewClient(&mcp.Implementation{Name: "trip-agent", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Timeout: r.timeout},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("сервер %s недоступен: %w", endpoint, err)
	}
	return session, ctx, cancel, nil
}

// ServersJSON -- состояние реестра для события интерфейса.
func (r *Registry) ServersJSON() json.RawMessage {
	raw, _ := json.Marshal(r.Servers())
	return raw
}
