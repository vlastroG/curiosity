package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"doc-index/internal/chat"
	"doc-index/internal/embed"
	"doc-index/internal/httpapi"
	"doc-index/internal/index"
	"doc-index/internal/llm"
	"doc-index/internal/rag"
	"doc-index/internal/search"
	"doc-index/internal/store"
	"doc-index/internal/testkit"
)

// echoLLM отвечает последней репликой и считает вызовы.
type echoLLM struct{ calls atomic.Int32 }

func (e *echoLLM) Chat(_ context.Context, _ llm.Provider, req llm.Request) (llm.Response, error) {
	e.calls.Add(1)
	return llm.Response{Text: "эхо: " + req.Messages[len(req.Messages)-1].Content}, nil
}

// Без RAG чат отвечает без индекса, одним вызовом модели на реплику, а поиск
// и индексация выключены.
func TestChatWithoutRAG(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	chats, err := chat.Open(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	o := testkit.NewOllama("bge-m3", "nomic-embed-text")
	defer o.Close()
	client := embed.New(o.URL, 10*time.Second)
	model := &echoLLM{}
	plain := rag.LLM{Client: model, Model: "qwen"}
	h := httpapi.New(httpapi.Config{
		Store: st, Ollama: client, OllamaURL: o.URL, Jobs: &index.Jobs{},
		Searcher:     &search.Searcher{Store: st, Embedder: client, Variants: index.Variants("bge-m3", "nomic-embed-text")},
		Chats:        &chat.Service{Store: chats, Agent: rag.Plain{LLM: plain}, Compressor: rag.Compressor{LLM: plain, System: rag.PlainCompressSystem}},
		ChatDefaults: chat.Settings{Settings: rag.Settings{Query: "raw"}, CompressAfter: 12},
		StaticDir:    t.TempDir(), NoRAG: true, Model: "qwen",
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	do := func(method, path, body string, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	var status struct {
		RAG map[string]any `json:"rag"`
	}
	do(http.MethodGet, "/api/status", "", &status)
	if status.RAG["off"] != true || status.RAG["model"] != "qwen" || status.RAG["defaults"] == nil {
		t.Errorf("статус: %+v", status.RAG)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/search"}, {http.MethodPost, "/api/index"}, {http.MethodGet, "/api/questions"},
	} {
		if code := do(c.method, c.path, `{}`, nil); code != http.StatusNotFound {
			t.Errorf("%s %s без RAG: %d", c.method, c.path, code)
		}
	}

	var cr chatResp
	do(http.MethodPost, "/api/chats", "", &cr)
	path := fmt.Sprintf("/api/chats/%d/messages", cr.Chat.ID)
	for i, text := range []string{"привет", "как дела?"} {
		var got chatResp
		if code := do(http.MethodPost, path, `{"text":"`+text+`"}`, &got); code != http.StatusOK {
			t.Fatalf("реплика %d: %d", i+1, code)
		}
		last := got.Chat.Messages[len(got.Chat.Messages)-1]
		if last.Text != "эхо: "+text || last.Error != "" {
			t.Errorf("ответ %d: %+v", i+1, last)
		}
		if n := model.calls.Load(); n != int32(i+1) {
			t.Errorf("после реплики %d вызовов модели: %d", i+1, n)
		}
	}
}
