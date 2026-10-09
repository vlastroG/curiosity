package rag

import (
	"context"
	"strings"
	"testing"

	"doc-index/internal/llm"
)

// countingChat считает вызовы модели и запоминает последний запрос.
type countingChat struct {
	calls int
	req   llm.Request
}

func (c *countingChat) Chat(_ context.Context, _ llm.Provider, req llm.Request) (llm.Response, error) {
	c.calls++
	c.req = req
	return llm.Response{Text: "<think>хм</think>Привет!"}, nil
}

func TestPlainReply(t *testing.T) {
	rec := &countingChat{}
	temp := 0.3
	st := Settings{Gen: Gen{Temperature: &temp, Ctx: 8192}}
	c := Conversation{
		State:   TaskState{Topic: "не трогать"},
		Summary: "Говорили о погоде.",
		Recent:  []Turn{{Role: "user", Text: "  как дела? "}, {Role: "assistant", Text: "Хорошо."}},
	}
	res := Plain{LLM: LLM{Client: rec, Provider: llm.Local("x"), Model: "qwen"}}.Reply(context.Background(), c, " а у тебя? ", st)

	if rec.calls != 1 {
		t.Fatalf("вызовов модели: %d, ждали 1", rec.calls)
	}
	if res.Text != "Привет!" || res.Error != "" || res.Model != "qwen" || len(res.Sources) != 0 || res.State.Topic != "не трогать" {
		t.Errorf("ответ: %+v", res)
	}
	if rec.req.NumCtx != 8192 || rec.req.Temperature == nil || *rec.req.Temperature != 0.3 {
		t.Errorf("параметры чата не дошли: %+v", rec.req)
	}
	m := rec.req.Messages
	roles := []string{}
	for _, x := range m {
		roles = append(roles, x.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" {
		t.Fatalf("роли: %v", roles)
	}
	if !strings.Contains(m[0].Content, "Говорили о погоде.") || m[1].Content != "как дела?" || m[3].Content != "а у тебя?" {
		t.Errorf("сообщения: %+v", m)
	}
}

func TestCompressorSystem(t *testing.T) {
	rec := &countingChat{}
	c := Compressor{LLM: LLM{Client: rec, Model: "m"}, System: PlainCompressSystem}
	if _, err := c.Compress(context.Background(), "", []Turn{{Role: "user", Text: "x"}}, Gen{}); err != nil {
		t.Fatal(err)
	}
	if rec.req.Messages[0].Content != PlainCompressSystem {
		t.Errorf("промпт сжатия: %.60s", rec.req.Messages[0].Content)
	}
	Compressor{LLM: LLM{Client: rec, Model: "m"}}.Compress(context.Background(), "", nil, Gen{})
	if !strings.Contains(rec.req.Messages[0].Content, "Марка Твена") {
		t.Errorf("по умолчанию -- промпт о книгах: %.60s", rec.req.Messages[0].Content)
	}
}
