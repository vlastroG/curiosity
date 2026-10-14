package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// fakeOllama -- сервер Ollama в памяти. pulled=false -- модели пока нет.
func fakeOllama(t *testing.T, pulled bool) (*httptest.Server, *chatRequest) {
	t.Helper()
	got := &chatRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			if !pulled {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			io.WriteString(w, `{"details":{"parameter_size":"9.7B","quantization_level":"Q4_K_M"},"model_info":{"qwen35.context_length":262144}}`)
		case "/api/pull":
			pulled = true
			io.WriteString(w, "{\"status\":\"pulling manifest\"}\n{\"status\":\"success\"}\n")
		case "/api/chat":
			*got = chatRequest{}
			json.NewDecoder(r.Body).Decode(got)
			io.WriteString(w, "{\"message\":{\"content\":\"При\"},\"done\":false}\n"+
				"{\"message\":{\"content\":\"вет\"},\"done\":false}\n"+
				"{\"message\":{\"content\":\"\"},\"done\":true,\"prompt_eval_count\":5,\"eval_count\":2,\"eval_duration\":1000000000}\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestBuildRequestOnlySetFlags(t *testing.T) {
	req, _, err := buildRequest([]string{"--temperature", "0", "--max-tokens", "20", "--stop", "a", "--stop", "b", "--system", "кратко", "вопрос"}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"temperature": 0.0, "num_predict": 20, "stop": []string{"a", "b"}}
	if !reflect.DeepEqual(req.Options, want) {
		t.Errorf("options = %#v, want %#v", req.Options, want)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Content != "вопрос" {
		t.Errorf("messages = %+v", req.Messages)
	}
}

func TestBuildRequestStdin(t *testing.T) {
	req, _, err := buildRequest([]string{"суммируй"}, strings.NewReader("текст"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Messages[0].Content; got != "суммируй\n\nтекст" {
		t.Errorf("prompt = %q", got)
	}
	if _, _, err := buildRequest(nil, nil, io.Discard); err == nil {
		t.Error("пустой запрос должен быть ошибкой")
	}
}

func TestAskPullsAndStreams(t *testing.T) {
	srv, got := fakeOllama(t, false)
	var out, errOut bytes.Buffer
	code := run(context.Background(), newClient(srv.URL), []string{"ask", "--stats", "привет"}, nil, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if out.String() != "Привет\n" {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "скачиваю") || !strings.Contains(errOut.String(), "2.0 ток/с") {
		t.Errorf("stderr = %q", errOut.String())
	}
	if got.Model != model || got.Options != nil {
		t.Errorf("request = %+v", got)
	}
}

func TestServeCompletions(t *testing.T) {
	ollama, got := fakeOllama(t, true)
	s := &server{c: newClient(ollama.URL)}
	s.prepare(context.Background())
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)

	body := `{"model":"local","max_tokens":300000,"messages":[{"role":"system","content":"кратко"},{"role":"user","content":"привет"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message      message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || len(out.Choices) != 1 || out.Choices[0].Message.Content != "Привет" {
		t.Fatalf("status %d, ответ %+v", resp.StatusCode, out)
	}
	if out.Usage.PromptTokens != 5 || out.Usage.CompletionTokens != 2 {
		t.Errorf("usage = %+v", out.Usage)
	}
	// контекст -- максимум модели, длина ответа им ограничена; JSON-числа -- float64
	if got.Options["num_ctx"] != 262144.0 || got.Options["num_predict"] != 262144.0 || got.Think || len(got.Messages) != 2 {
		t.Errorf("запрос к Ollama = %+v", got)
	}

	// параметры запроса: температура и своё окно, ответ ограничен этим окном
	body = `{"max_tokens":9000,"temperature":0.2,"num_ctx":8192,"messages":[{"role":"user","content":"привет"}]}`
	resp2, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if got.Options["num_ctx"] != 8192.0 || got.Options["num_predict"] != 8192.0 || got.Options["temperature"] != 0.2 {
		t.Errorf("options = %+v", got.Options)
	}

	// окно больше максимума модели срезается
	body = `{"num_ctx":999999,"messages":[{"role":"user","content":"привет"}]}`
	resp3, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if got.Options["num_ctx"] != 262144.0 || got.Options["temperature"] != nil {
		t.Errorf("options = %+v", got.Options)
	}

	resp4, err := http.Get(srv.URL + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	var info map[string]any
	json.NewDecoder(resp4.Body).Decode(&info)
	if info["model"] != model || info["quantization"] != "Q4_K_M" || info["contextLength"] != 262144.0 || info["ready"] != true {
		t.Errorf("info = %+v", info)
	}
}

func TestModelFromEnv(t *testing.T) {
	if m := modelName(func(string) string { return "" }); m != defaultModel {
		t.Errorf("по умолчанию %q", m)
	}
	t.Cleanup(func() { model = defaultModel })
	model = modelName(func(string) string { return "llama3.1:8b" })
	srv, got := fakeOllama(t, true)
	if code := run(context.Background(), newClient(srv.URL), []string{"ask", "привет"}, nil, io.Discard, io.Discard); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if got.Model != "llama3.1:8b" {
		t.Errorf("модель в запросе = %q", got.Model)
	}
}

func TestServeNotReady(t *testing.T) {
	srv := httptest.NewServer((&server{}).handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("health до готовности = %d", resp.StatusCode)
	}
}

func TestHelp(t *testing.T) {
	srv, _ := fakeOllama(t, true)
	var out bytes.Buffer
	if code := run(context.Background(), newClient(srv.URL), []string{"help"}, nil, &out, io.Discard); code != 0 {
		t.Fatalf("code = %d", code)
	}
	for _, s := range []string{model, "9.7B", "262144", "Ограничения", "-max-tokens"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("в справке нет %q", s)
		}
	}

	out.Reset()
	run(context.Background(), newClient("http://127.0.0.1:1"), nil, nil, &out, io.Discard)
	if !strings.Contains(out.String(), "Ollama недоступна") || !strings.Contains(out.String(), "Ограничения") {
		t.Errorf("справка без Ollama:\n%s", out.String())
	}

	if code := run(context.Background(), newClient(srv.URL), []string{"nope"}, nil, io.Discard, io.Discard); code != 2 {
		t.Errorf("неизвестная команда: code = %d", code)
	}
}
