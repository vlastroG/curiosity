package rag

import (
	"context"
	"testing"

	"doc-index/internal/llm"
)

// lastRequest запоминает последний запрос к модели.
type lastRequest struct{ req llm.Request }

func (l *lastRequest) Chat(_ context.Context, _ llm.Provider, req llm.Request) (llm.Response, error) {
	l.req = req
	return llm.Response{Text: "ок"}, nil
}

func TestGenApplied(t *testing.T) {
	temp := 0.2
	g := Gen{Temperature: &temp, MaxTokens: 300, Ctx: 16384}
	for _, tc := range []struct {
		provider llm.Provider
		ctx      int
	}{
		{llm.Local("http://llmcli:8080"), 16384},
		// облачному провайдеру окно не передаётся
		{llm.OpenRouter("k", "http://x", "t"), 0},
	} {
		rec := &lastRequest{}
		m := LLM{Client: rec, Provider: tc.provider, Model: "m", MaxTokens: 32_768}.With(g)
		if _, err := m.ask(context.Background(), "s", "u"); err != nil {
			t.Fatal(err)
		}
		r := rec.req
		if r.Temperature == nil || *r.Temperature != 0.2 || r.MaxTokens != 300 || r.NumCtx != tc.ctx {
			t.Errorf("%s: %+v", tc.provider.ID, r)
		}
	}

	// без параметров -- бюджет модели, температура модели
	rec := &lastRequest{}
	LLM{Client: rec, Provider: llm.Local("x"), MaxTokens: 32_768}.ask(context.Background(), "s", "u")
	if rec.req.Temperature != nil || rec.req.MaxTokens != 32_768 || rec.req.NumCtx != 0 {
		t.Errorf("по умолчанию: %+v", rec.req)
	}
}

func TestGenValidate(t *testing.T) {
	bad, ok := 2.5, 1.0
	for _, g := range []Gen{{Temperature: &bad}, {MaxTokens: 5}, {MaxTokens: llm.MaxTokens + 1}, {Ctx: 1000}, {Ctx: MaxCtx + 1}} {
		if g.Validate() == nil {
			t.Errorf("%+v прошёл проверку", g)
		}
	}
	for _, g := range []Gen{{}, {Temperature: &ok, MaxTokens: 16, Ctx: MinCtx}, {MaxTokens: llm.MaxTokens, Ctx: MaxCtx}} {
		if err := g.Validate(); err != nil {
			t.Errorf("%+v: %v", g, err)
		}
	}
}
