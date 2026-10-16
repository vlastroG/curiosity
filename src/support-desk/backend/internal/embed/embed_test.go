package embed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedNormalizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("запрос на %s", r.URL.Path)
		}
		w.Write([]byte(`{"embeddings":[[3,4]],"prompt_eval_count":5}`))
	}))
	defer server.Close()

	res, err := New(server.URL, 0).Embed(context.Background(), "bge-m3", []string{"текст"})
	if err != nil {
		t.Fatal(err)
	}
	if v := res.Vectors[0]; v[0] != 0.6 || v[1] != 0.8 || res.PromptTokens != 5 {
		t.Errorf("вектор %v, токенов %d", v, res.PromptTokens)
	}
}
