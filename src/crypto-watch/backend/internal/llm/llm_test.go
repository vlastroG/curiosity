package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func provider(url string) Provider {
	return Provider{Title: "test", Endpoint: url, APIKey: "k", ExtraHeaders: map[string]string{"X-Title": "t"}}
}

func TestChatToolCalls(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("X-Title") != "t" {
			t.Errorf("заголовки: %v", r.Header)
		}
		json.NewDecoder(r.Body).Decode(&got)
		// OpenRouter шлёт пробелы до тела, пока модель думает
		w.Write([]byte("\n  \n" + `{"choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"crypto_summary","arguments":"{\"minutes\":60}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer server.Close()

	resp, err := New(0).Chat(context.Background(), provider(server.URL), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "привет"}},
		Tools:    []Tool{{Name: "crypto_summary", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Arguments != `{"minutes":60}` || resp.Usage.CompletionTokens != 5 {
		t.Errorf("ответ: %+v", resp)
	}
	tools := got["tools"].([]any)
	if fn := tools[0].(map[string]any)["function"].(map[string]any); fn["name"] != "crypto_summary" {
		t.Errorf("инструменты ушли как %v", tools)
	}
}

func TestChatRetries(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if calls == 2 {
			// ошибка с кодом 200 в теле -- так умеет OpenRouter
			w.Write([]byte(`{"error":{"code":429,"message":"rate-limited upstream"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	client := New(0)
	client.backoff = 0
	resp, err := client.Chat(context.Background(), provider(server.URL), Request{Model: "m"})
	if err != nil || resp.Text != "ok" || calls != 3 {
		t.Errorf("ответ %q, ошибка %v, попыток %d", resp.Text, err, calls)
	}
}

func TestChatNoRetryOnClientError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer server.Close()

	client := New(0)
	client.backoff = 0
	_, err := client.Chat(context.Background(), provider(server.URL), Request{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "401") || calls != 1 {
		t.Errorf("ошибка %v, попыток %d", err, calls)
	}
}

func TestChatNoKey(t *testing.T) {
	_, err := New(0).Chat(context.Background(), Provider{Title: "x"}, Request{})
	if err == nil {
		t.Error("запрос без ключа ушёл")
	}
}
