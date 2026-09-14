package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Ускоряющие параметры -- reasoning_effort и json-схему -- мы шлём, не зная наверняка,
// принимает ли их конкретный провайдер: документация DeepSeek из этой сети недоступна,
// а выдумывать поддержку нельзя. Поэтому поведение проверяется здесь: один повтор
// без ускорителей, если провайдер на них ругнулся, и никакого повтора в остальных случаях.

type recorder struct {
	bodies  []map[string]any
	statuse []int
	reply   func(attempt int) (int, string)
}

func (r *recorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("тело запроса не разобралось: %v", err)
		}
		r.bodies = append(r.bodies, body)

		status, text := r.reply(len(r.bodies) - 1)
		r.statuse = append(r.statuse, status)
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Write([]byte(`{"choices":[{"message":{"content":"` + text + `"},"finish_reason":"stop"}]}`))
			return
		}
		w.Write([]byte(`{"error":{"message":"` + text + `"}}`))
	}))
}

func acceleratedRequest() Request {
	return Request{
		Model:     "тестовая",
		Messages:  []Message{{Role: RoleUser, Content: "вопрос"}},
		MaxTokens: 100,
		Thinking:  ThinkingOff,
		Schema:    &Schema{Name: "routing", Definition: map[string]any{"type": "object"}},
	}
}

func testProvider(url string) Provider {
	return Provider{Title: "тест", Endpoint: url, APIKey: "ключ"}
}

func TestChatRetriesWithoutAcceleratorsOnRejectedParameter(t *testing.T) {
	rec := &recorder{reply: func(attempt int) (int, string) {
		if attempt == 0 {
			return http.StatusBadRequest, "This response_format type is unavailable now"
		}
		return http.StatusOK, "ответ"
	}}
	server := rec.server(t)
	defer server.Close()

	resp, err := New(5*time.Second).Chat(context.Background(), testProvider(server.URL), acceleratedRequest())
	if err != nil {
		t.Fatalf("откат должен пройти незаметно для вызывающего: %v", err)
	}
	if resp.Text != "ответ" {
		t.Fatalf("ответ второй попытки потерян: %q", resp.Text)
	}
	if !resp.Downgraded {
		t.Fatal("откат должен быть виден в ответе -- иначе он не попадёт в трейс")
	}
	if len(rec.bodies) != 2 {
		t.Fatalf("ожидались две попытки, было %d", len(rec.bodies))
	}

	if _, ok := rec.bodies[0]["thinking"]; !ok {
		t.Fatal("первая попытка должна идти с ускорителями")
	}
	if format, _ := rec.bodies[0]["response_format"].(map[string]any); format["type"] != "json_schema" {
		t.Fatalf("первая попытка должна идти со схемой, получено %v", rec.bodies[0]["response_format"])
	}

	// выключатель рассуждения провайдер не трогал -- он обязан остаться:
	// отказ от схемы не повод возвращать себе дорогое рассуждение
	if _, ok := rec.bodies[1]["thinking"]; !ok {
		t.Fatal("повтор должен сохранить thinking: провайдер жаловался не на него")
	}
	// контракт ответа при откате не теряется: json остаётся, пропадает только форма
	if format, _ := rec.bodies[1]["response_format"].(map[string]any); format["type"] != "json_object" {
		t.Fatalf("повтор должен просить обычный json, получено %v", rec.bodies[1]["response_format"])
	}
}

func TestChatDoesNotRetryOnUnrelatedBadRequest(t *testing.T) {
	rec := &recorder{reply: func(int) (int, string) {
		return http.StatusBadRequest, "context length exceeded"
	}}
	server := rec.server(t)
	defer server.Close()

	if _, err := New(5*time.Second).Chat(context.Background(), testProvider(server.URL), acceleratedRequest()); err == nil {
		t.Fatal("ошибка провайдера должна дойти до вызывающего")
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("400 не про параметр повторять незачем, попыток было %d", len(rec.bodies))
	}
}

func TestChatDoesNotDowngradeRequestWithoutAccelerators(t *testing.T) {
	rec := &recorder{reply: func(int) (int, string) {
		return http.StatusBadRequest, "unsupported parameter"
	}}
	server := rec.server(t)
	defer server.Close()

	plain := Request{Model: "тестовая", Messages: []Message{{Role: RoleUser, Content: "вопрос"}}}
	if _, err := New(5*time.Second).Chat(context.Background(), testProvider(server.URL), plain); err == nil {
		t.Fatal("ошибка должна дойти до вызывающего")
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("откатывать нечего, попыток должно быть одна, было %d", len(rec.bodies))
	}
}

func TestChatRemembersRejectionAndSkipsTheExtraRound(t *testing.T) {
	rec := &recorder{reply: func(attempt int) (int, string) {
		if attempt == 0 {
			return http.StatusBadRequest, "This response_format type is unavailable now"
		}
		return http.StatusOK, "ответ"
	}}
	server := rec.server(t)
	defer server.Close()

	client := New(5 * time.Second)
	provider := testProvider(server.URL)

	if _, err := client.Chat(context.Background(), provider, acceleratedRequest()); err != nil {
		t.Fatalf("первый вызов должен пройти через откат: %v", err)
	}
	resp, err := client.Chat(context.Background(), provider, acceleratedRequest())
	if err != nil {
		t.Fatalf("второй вызов: %v", err)
	}
	if !resp.Downgraded {
		t.Fatal("второй вызов тоже идёт без ускорителей -- это должно быть видно")
	}
	// три запроса на два вызова: отказ, повтор, и сразу правильный второй
	if len(rec.bodies) != 3 {
		t.Fatalf("отказ должен запоминаться, запросов было %d", len(rec.bodies))
	}
	if format, _ := rec.bodies[2]["response_format"].(map[string]any); format["type"] != "json_object" {
		t.Fatalf("третий запрос должен сразу идти без схемы, получено %v", rec.bodies[2]["response_format"])
	}
}

func TestChatDropsOnlyTheParameterProviderNamed(t *testing.T) {
	rec := &recorder{reply: func(attempt int) (int, string) {
		if attempt == 0 {
			return http.StatusBadRequest, "The thinking parameter is not supported"
		}
		return http.StatusOK, "ответ"
	}}
	server := rec.server(t)
	defer server.Close()

	if _, err := New(5*time.Second).Chat(context.Background(), testProvider(server.URL), acceleratedRequest()); err != nil {
		t.Fatalf("откат должен пройти незаметно: %v", err)
	}
	if _, ok := rec.bodies[1]["thinking"]; ok {
		t.Fatal("повтор не должен слать thinking -- на него и жаловались")
	}
	if format, _ := rec.bodies[1]["response_format"].(map[string]any); format["type"] != "json_schema" {
		t.Fatalf("схему при этом терять незачем, получено %v", rec.bodies[1]["response_format"])
	}
}

func TestChatDropsEverythingWhenProviderIsVague(t *testing.T) {
	rec := &recorder{reply: func(attempt int) (int, string) {
		if attempt == 0 {
			return http.StatusBadRequest, "extra inputs are not permitted"
		}
		return http.StatusOK, "ответ"
	}}
	server := rec.server(t)
	defer server.Close()

	if _, err := New(5*time.Second).Chat(context.Background(), testProvider(server.URL), acceleratedRequest()); err != nil {
		t.Fatalf("откат должен пройти незаметно: %v", err)
	}
	if _, ok := rec.bodies[1]["thinking"]; ok {
		t.Fatal("из невнятного отказа понятно только одно: слать нечего")
	}
	if format, _ := rec.bodies[1]["response_format"].(map[string]any); format["type"] != "json_object" {
		t.Fatalf("схема тоже должна уйти, получено %v", rec.bodies[1]["response_format"])
	}
}
