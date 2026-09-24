package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// fakeServer -- MCP-сервер в памяти с настоящей логикой хендлов: поиск выдаёт
// src_…, конспект принимает только известный src_…, сохранение -- только sum_….
type fakeServer struct {
	calls []string
	n     int
}

func (f *fakeServer) Tools(context.Context, []string) ([]Tool, error) {
	return []Tool{{Name: "hn_search"}, {Name: "summarize"}, {Name: "save_to_file"}, {Name: "list_files"}}, nil
}

func (f *fakeServer) Call(_ context.Context, name, args string) (ToolResult, error) {
	f.calls = append(f.calls, name+" "+args)
	var in map[string]any
	json.Unmarshal([]byte(args), &in)
	switch name {
	case "hn_search":
		f.n++
		count := 5
		if strings.Contains(fmt.Sprint(in["query"]), "nothing") {
			count = 0
		}
		return ok(map[string]any{"search_id": fmt.Sprintf("src_%010d", f.n), "sha256": fmt.Sprintf("sha-src-%d", f.n), "count": count})
	case "summarize":
		id := fmt.Sprint(in["search_id"])
		if !strings.HasPrefix(id, "src_") {
			return ToolResult{Text: "не похоже на id", IsError: true}, nil
		}
		n := strings.TrimLeft(strings.TrimPrefix(id, "src_"), "0")
		return ok(map[string]any{"summary_id": "sum_000000000" + n, "sha256": "sha-sum-" + n, "source_id": id, "source_sha256": "sha-src-" + n, "stories": 5, "missing": 0})
	case "save_to_file":
		id := fmt.Sprint(in["summary_id"])
		n := strings.TrimLeft(strings.TrimPrefix(id, "sum_"), "0")
		return ok(map[string]any{"file": "rust-web.html", "sha256": "sha-file", "summaryId": id, "summarySha256": "sha-sum-" + n,
			"sourceId": fmt.Sprintf("src_%010s", n), "sourceSha256": "sha-src-" + n, "bytes": 100})
	case "list_files":
		return ok(map[string]any{"files": []map[string]any{{"file": "rust-web.html", "sha256": "sha-file"}}})
	}
	return ToolResult{Text: "unknown", IsError: true}, nil
}

func ok(v any) (ToolResult, error) {
	raw, _ := json.Marshal(v)
	return ToolResult{Text: string(raw), Structured: raw}, nil
}

// script -- модель по сценарию.
type script struct {
	turns    []Response
	requests []Request
}

func (s *script) Chat(_ context.Context, _ Provider, req Request) (Response, error) {
	s.requests = append(s.requests, req)
	if len(s.turns) == 0 {
		return Response{Text: "готово"}, nil
	}
	turn := s.turns[0]
	s.turns = s.turns[1:]
	return turn, nil
}

func calls(pairs ...string) Response {
	var resp Response
	for i := 0; i < len(pairs); i += 2 {
		var call ToolCall
		call.ID = fmt.Sprintf("c%d", i)
		call.Function.Name = pairs[i]
		call.Function.Arguments = pairs[i+1]
		resp.ToolCalls = append(resp.ToolCalls, call)
	}
	return resp
}

type sink struct{ lines []string }

func (s *sink) Printf(format string, args ...any) {
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
}

func newAgent(model *script, server *fakeServer) (*Agent, *sink) {
	log := &sink{}
	return &Agent{chat: model, provider: Provider{APIKey: "k"}, model: "m", tools: server, log: log}, log
}

// Модель переискивает, конспектирует вторую выдачу, сохраняет -- проверка цепочки проходит.
func TestAgentChain(t *testing.T) {
	server := &fakeServer{}
	model := &script{turns: []Response{
		calls("hn_search", `{"query":"nothing here"}`),
		calls("hn_search", `{"query":"rust web framework","limit":5}`),
		calls("summarize", `{"search_id":"src_0000000002"}`),
		calls("save_to_file", `{"summary_id":"sum_0000000002","filename":"rust-web"}`),
		{Text: "Нашёл 5 историй, сохранил в rust-web.html"},
	}}
	agent, log := newAgent(model, server)

	outcome, err := agent.Run(context.Background(), "что обсуждают про Rust в вебе")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Trace) != 4 || outcome.Refused {
		t.Fatalf("трасса: %+v", outcome.Trace)
	}
	// запрос пользователя -- в отдельном блоке
	if user := model.requests[0].Messages[1].Content; !strings.HasPrefix(user, "<user_request>") {
		t.Errorf("запрос не обёрнут: %q", user)
	}

	files, _ := fetchFiles(context.Background(), server)
	report := verify(outcome.Trace, files)
	if !report.OK() || report.File != "rust-web.html" || len(report.Links) != 2 {
		t.Errorf("проверка: %+v", report)
	}
	if !strings.Contains(strings.Join(log.lines, "\n"), "[агент] шаг 3: summarize") {
		t.Errorf("шаги не в логе:\n%s", strings.Join(log.lines, "\n"))
	}
}

// Модель конспектирует не ту выдачу, что потом сохраняет, -- проверка ловит.
func TestVerifyBroken(t *testing.T) {
	step := func(n int, tool, args string, out map[string]any) Step {
		raw, _ := json.Marshal(out)
		return Step{N: n, Tool: tool, Arguments: json.RawMessage(args), OK: true, Output: raw}
	}
	files := []savedOut{{File: "f.html", SHA256: "file"}}

	good := []Step{
		step(1, "hn_search", `{}`, map[string]any{"search_id": "src_1", "sha256": "s1", "count": 3}),
		step(2, "summarize", `{"search_id":"src_1"}`, map[string]any{"summary_id": "sum_1", "sha256": "m1", "source_id": "src_1", "source_sha256": "s1"}),
		step(3, "save_to_file", `{"summary_id":"sum_1"}`, map[string]any{"file": "f.html", "sha256": "file", "summaryId": "sum_1", "summarySha256": "m1", "sourceId": "src_1", "sourceSha256": "s1"}),
	}
	if r := verify(good, files); !r.OK() {
		t.Fatalf("правильная цепочка не прошла: %v", r.Violations)
	}

	cases := map[string]func([]Step) []Step{
		"нет сохранения": func(s []Step) []Step { return s[:2] },
		"сохранён чужой конспект": func(s []Step) []Step {
			s[2] = step(3, "save_to_file", `{"summary_id":"sum_9"}`, map[string]any{"file": "f.html", "sha256": "file", "summaryId": "sum_9"})
			return s
		},
		"конспект выдуманной выдачи": func(s []Step) []Step {
			s[1] = step(2, "summarize", `{"search_id":"src_9"}`, map[string]any{"summary_id": "sum_1", "sha256": "m1", "source_id": "src_9", "source_sha256": "x"})
			return s
		},
		"хеш выдачи разошёлся": func(s []Step) []Step {
			s[1] = step(2, "summarize", `{"search_id":"src_1"}`, map[string]any{"summary_id": "sum_1", "sha256": "m1", "source_id": "src_1", "source_sha256": "other"})
			return s
		},
		"файл изменился на сервере": func(s []Step) []Step { return s },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			trace := breakIt(append([]Step(nil), good...))
			list := files
			if name == "файл изменился на сервере" {
				list = []savedOut{{File: "f.html", SHA256: "changed"}}
			}
			if r := verify(trace, list); r.OK() {
				t.Error("нарушение не поймано")
			}
		})
	}
}

// Запрос не по делу -- отказ без единого вызова инструментов.
func TestAgentRefusal(t *testing.T) {
	server := &fakeServer{}
	model := &script{turns: []Response{{Text: "ОТКАЗ: это просьба написать код, а не конспект HN"}}}
	agent, _ := newAgent(model, server)

	outcome, err := agent.Run(context.Background(), "напиши мне сортировку пузырьком")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Refused || len(server.calls) != 0 {
		t.Errorf("отказ: %+v, вызовов %d", outcome, len(server.calls))
	}
}

// Белый список и лимит поисков держит код, а не промпт.
func TestAgentGuards(t *testing.T) {
	server := &fakeServer{}
	model := &script{turns: []Response{
		calls("delete_everything", `{}`),
		calls("hn_search", `{"query":"a"}`, "hn_search", `{"query":"b"}`, "hn_search", `{"query":"c"}`, "hn_search", `{"query":"d"}`),
		calls("summarize", `{"search_id":"src_0000000003"}`),
		calls("save_to_file", `{"summary_id":"sum_0000000003"}`),
		{Text: "готово"},
	}}
	agent, log := newAgent(model, server)

	outcome, err := agent.Run(context.Background(), "rust")
	if err != nil {
		t.Fatal(err)
	}
	searches := 0
	for _, call := range server.calls {
		if strings.HasPrefix(call, "delete_everything") {
			t.Error("инструмент вне белого списка дошёл до сервера")
		}
		if strings.HasPrefix(call, "hn_search") {
			searches++
		}
	}
	if searches != maxSearches {
		t.Errorf("поисков на сервере %d, лимит %d", searches, maxSearches)
	}
	joined := strings.Join(log.lines, "\n")
	if !strings.Contains(joined, "нет в белом списке") || !strings.Contains(joined, "лимит поисков") {
		t.Errorf("ограничения не в логе:\n%s", joined)
	}
	if !saved(outcome.Trace) {
		t.Error("цепочка не дошла до сохранения")
	}
}

// Модель остановилась на полпути -- одно напоминание, потом она доделывает.
func TestAgentNudge(t *testing.T) {
	server := &fakeServer{}
	model := &script{turns: []Response{
		calls("hn_search", `{"query":"rust"}`),
		{Text: "Вот что нашлось: ..."},
		calls("summarize", `{"search_id":"src_0000000001"}`),
		calls("save_to_file", `{"summary_id":"sum_0000000001"}`),
		{Text: "готово"},
	}}
	agent, _ := newAgent(model, server)
	outcome, err := agent.Run(context.Background(), "rust")
	if err != nil || !saved(outcome.Trace) {
		t.Errorf("после напоминания цепочка не завершена: %v", err)
	}
}

func TestAgentLongRequest(t *testing.T) {
	agent, _ := newAgent(&script{}, &fakeServer{})
	if _, err := agent.Run(context.Background(), strings.Repeat("я", maxRequestRune+1)); err == nil {
		t.Error("слишком длинный запрос принят")
	}
}
