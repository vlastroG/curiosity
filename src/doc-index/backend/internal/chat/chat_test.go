package chat_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"doc-index/internal/chat"
	"doc-index/internal/rag"
	"doc-index/internal/rerank"
	"doc-index/internal/retrieve"
	"doc-index/internal/store"
)

// fakeAgent отвечает «ответ N [1]» с одним отрывком и ведёт тему.
type fakeAgent struct {
	mu    sync.Mutex
	convs []rag.Conversation
	topic string
	fail  bool
}

func (f *fakeAgent) Reply(_ context.Context, c rag.Conversation, text string, st rag.Settings) rag.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convs = append(f.convs, c)
	if f.fail {
		return rag.Result{Answer: rag.Answer{Error: "модель недоступна"}, State: c.State, Sources: []rag.Source{}}
	}
	state := rag.Merge(c.State, f.topic, []string{"тезис о " + text}, nil, c.Seq)
	return rag.Result{
		Answer:   rag.Answer{Text: fmt.Sprintf("ответ %d [1]", c.Seq)},
		Settings: st,
		Sources:  []rag.Source{{N: 1}},
		State:    state,
	}
}

type fakeSummarizer struct {
	calls [][]rag.Turn
	gens  []rag.Gen
	err   error
}

func (f *fakeSummarizer) Compress(_ context.Context, summary string, turns []rag.Turn, g rag.Gen) (string, error) {
	f.calls = append(f.calls, turns)
	f.gens = append(f.gens, g)
	if f.err != nil {
		return "", f.err
	}
	return fmt.Sprintf("сводка %d сообщений", len(turns)), nil
}

func settings(n int) chat.Settings {
	return chat.Settings{CompressAfter: n, Settings: rag.Settings{Query: retrieve.QueryHyDE,
		Params: rerank.Params{KBefore: 10, KAfter: 3, RelMin: 0.1, Order: rerank.OrderCosine}}}
}

func open(t *testing.T) *chat.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cs, err := chat.Open(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestCRUDAndDelete(t *testing.T) {
	ctx := context.Background()
	cs := open(t)
	svc := &chat.Service{Store: cs, Agent: &fakeAgent{topic: "Добро и зло"}, Compressor: &fakeSummarizer{}}
	a, err := cs.Create(ctx, settings(12))
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != chat.DefaultTitle || a.Named || len(a.Messages) != 0 || a.State.Theses == nil {
		t.Fatalf("новый чат: %+v", a)
	}
	b, _ := cs.Create(ctx, settings(12))
	if _, err := svc.Send(ctx, a.ID, "Гек и совесть"); err != nil {
		t.Fatal(err)
	}
	list, _ := cs.List(ctx)
	if len(list) != 2 || list[0].ID != a.ID || list[0].Title != "Добро и зло" || list[0].Count != 2 || list[0].Last != "ответ 1 [1]" {
		t.Fatalf("список: %+v", list[0])
	}
	if err := cs.Rename(ctx, a.ID, "  Мой   чат "); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.Get(ctx, a.ID)
	if got.Title != "Мой чат" || !got.Named {
		t.Fatalf("переименование: %q", got.Title)
	}
	bad := settings(2)
	if err := cs.SetSettings(ctx, a.ID, bad); err == nil {
		t.Error("сжатие после 2 сообщений принято")
	}
	if err := cs.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Get(ctx, a.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("удалённый чат: %v", err)
	}
	if err := cs.Delete(ctx, a.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("повторное удаление: %v", err)
	}
	// сообщения удалённого чата не попадают в новый с тем же номером
	if got, _ := cs.Get(ctx, b.ID); len(got.Messages) != 0 {
		t.Fatal("сообщения перепутались")
	}
}

func TestSendStoresAnswerSourcesAndState(t *testing.T) {
	ctx := context.Background()
	cs := open(t)
	agent := &fakeAgent{topic: "Добро и зло"}
	svc := &chat.Service{Store: cs, Agent: agent, Compressor: &fakeSummarizer{}}
	c, _ := cs.Create(ctx, settings(12))
	if _, err := svc.Send(ctx, c.ID, "  "); err == nil {
		t.Error("пустое сообщение принято")
	}
	svc.Send(ctx, c.ID, "Гек и совесть")
	got, err := svc.Send(ctx, c.ID, "а Джим?")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("сообщений %d", len(got.Messages))
	}
	ans := got.Messages[3]
	if ans.Role != chat.RoleAssistant || ans.Text != "ответ 3 [1]" || ans.Result == nil || len(ans.Result.Sources) != 1 ||
		ans.Result.Text != "" {
		t.Fatalf("ответ: %+v", ans)
	}
	if got.State.Topic != "Добро и зло" || len(got.State.Theses) != 1 || got.State.Theses[0].Since != 3 {
		t.Fatalf("память: %+v", got.State)
	}
	// второй ход видел историю первого
	last := agent.convs[1]
	if last.Seq != 3 || len(last.Recent) != 2 || last.Recent[0].Text != "Гек и совесть" || last.Recent[1].Role != "assistant" {
		t.Fatalf("история: %+v", last)
	}
}

func TestLockedTopicAndUserItemsSurvive(t *testing.T) {
	ctx := context.Background()
	cs := open(t)
	svc := &chat.Service{Store: cs, Agent: &fakeAgent{topic: "Другая тема"}, Compressor: &fakeSummarizer{}}
	c, _ := cs.Create(ctx, settings(12))
	if err := cs.SetState(ctx, c.ID, rag.TaskState{Topic: "", TopicLocked: true}); err == nil {
		t.Error("закреплена пустая тема")
	}
	st := rag.TaskState{Topic: "Твен о прогрессе", TopicLocked: true,
		Theses: []rag.Item{{Text: "мой тезис", By: rag.ByUser}}, Open: []rag.Item{}}
	if err := cs.SetState(ctx, c.ID, st); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Send(ctx, c.ID, "Янки")
	if got.State.Topic != "Твен о прогрессе" || got.Title != "Твен о прогрессе" {
		t.Fatalf("закреплённая тема изменилась: %+v", got.State)
	}
	if len(got.State.Theses) != 2 || got.State.Theses[0].Text != "мой тезис" || got.State.Theses[1].By != rag.ByModel {
		t.Fatalf("тезисы: %+v", got.State.Theses)
	}
	long := rag.TaskState{Topic: strings.Repeat("я", rag.MaxTopic+1)}
	if err := cs.SetState(ctx, c.ID, long); err == nil {
		t.Error("длинная тема принята")
	}
}

func TestCompressionAfterN(t *testing.T) {
	ctx := context.Background()
	cs := open(t)
	agent := &fakeAgent{topic: "т"}
	sum := &fakeSummarizer{}
	svc := &chat.Service{Store: cs, Agent: agent, Compressor: sum}
	st := settings(6)
	st.Gen = rag.Gen{Ctx: 8192}
	c, _ := cs.Create(ctx, st)
	var got *chat.Chat
	for i := 1; i <= 3; i++ { // 6 сообщений -- ещё не больше порога
		got, _ = svc.Send(ctx, c.ID, fmt.Sprintf("реплика %d", i))
	}
	if len(sum.calls) != 0 || got.SummarizedUpto != 0 {
		t.Fatal("сжатие раньше порога")
	}
	got, _ = svc.Send(ctx, c.ID, "реплика 4") // 8 > 6: сжимаются первые 4
	if len(sum.calls) != 1 || len(sum.calls[0]) != 4 || got.SummarizedUpto != 4 || got.Summary != "сводка 4 сообщений" {
		t.Fatalf("сжатие: вызовов %d, upto %d, %q", len(sum.calls), got.SummarizedUpto, got.Summary)
	}
	if sum.gens[0].Ctx != 8192 {
		t.Errorf("сжатие без параметров чата: %+v", sum.gens[0])
	}
	if len(got.Messages) != 8 {
		t.Fatal("история в интерфейсе должна остаться целиком")
	}
	svc.Send(ctx, c.ID, "реплика 5")
	conv := agent.convs[4]
	if conv.Summary != "сводка 4 сообщений" || len(conv.Recent) != 4 || conv.Recent[0].Text != "реплика 3" {
		t.Fatalf("после сжатия модель видит сводку и последние сообщения: %+v", conv)
	}
}

func TestFailedReplyIsStoredButNotSentBack(t *testing.T) {
	ctx := context.Background()
	cs := open(t)
	agent := &fakeAgent{fail: true}
	svc := &chat.Service{Store: cs, Agent: agent, Compressor: &fakeSummarizer{err: errors.New("x")}}
	c, _ := cs.Create(ctx, settings(12))
	got, err := svc.Send(ctx, c.ID, "вопрос")
	if err != nil || got.Messages[1].Error == "" {
		t.Fatalf("ошибка ответа: %v %+v", err, got.Messages)
	}
	agent.fail = false
	svc.Send(ctx, c.ID, "ещё раз")
	if r := agent.convs[1].Recent; len(r) != 1 || r[0].Text != "вопрос" {
		t.Fatalf("неудавшийся ответ попал в историю: %+v", r)
	}
	if _, err := svc.Send(ctx, 999, "x"); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("нет чата: %v", err)
	}
}
