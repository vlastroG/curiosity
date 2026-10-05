package rerank_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"doc-index/internal/rerank"
	"doc-index/internal/search"
	"doc-index/internal/testkit"
)

func TestVerdictSumsVariants(t *testing.T) {
	got, err := rerank.Verdict(func(yield func(string, float64)) {
		yield("No", math.Log(0.5))
		yield("no", math.Log(0.2))
		yield(" Yes", math.Log(0.2))
		yield("yes", math.Log(0.1))
		yield("Not", math.Log(0.05))
	})
	if err != nil || math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := rerank.Verdict(func(yield func(string, float64)) { yield("3", -1) }); !errors.Is(err, rerank.ErrNoVerdict) {
		t.Fatalf("без yes/no: %v", err)
	}
}

func TestPromptFormat(t *testing.T) {
	p := rerank.Prompt("who painted the fence", "Ben painted it")
	for _, want := range []string{"<|im_start|>system", "<Query>: who painted the fence\n", "<Document>: Ben painted it<|im_end|>",
		"<|im_start|>assistant\n<think>\n\n</think>\n\n"} {
		if !strings.Contains(p, want) {
			t.Errorf("нет %q в промпте", want)
		}
	}
}

func TestOllamaScorer(t *testing.T) {
	o := testkit.NewOllama("reranker")
	defer o.Close()
	s := rerank.NewOllama(o.URL, "reranker", 5*time.Second)
	got, err := s.Score(context.Background(), "whitewash fence brush", []string{
		"Tom took the brush and began to whitewash the fence.",
		"The cave was dark.",
	})
	if err != nil || len(got) != 2 || got[0] < 0.9 || got[1] > 0.1 {
		t.Fatalf("%v %v", got, err)
	}
	if o.RerankCalls() != 2 {
		t.Errorf("по отрывку на вызов: %d", o.RerankCalls())
	}
	if _, err := rerank.NewOllama(o.URL, "absent", time.Second).Score(context.Background(), "q", []string{"d"}); err == nil {
		t.Error("неизвестная модель")
	}
}

// fixed -- реранкер-заглушка с заранее заданными оценками.
type fixed []float64

func (f fixed) Name() string { return "fixed" }
func (f fixed) Score(_ context.Context, _ string, docs []string) ([]float64, error) {
	out := make([]float64, len(docs))
	for i, d := range docs {
		out[i] = f[int(d[0]-'0')]
	}
	return out, nil
}

func cands(cos ...float64) []rerank.Candidate {
	out := make([]rerank.Candidate, len(cos))
	for i, c := range cos {
		out[i] = rerank.Candidate{Hit: search.Hit{ChunkID: string(rune('a' + i)), Cosine: c}, Text: string(rune('0' + i))}
	}
	return out
}

func TestFunnel(t *testing.T) {
	scores := fixed{0.2, 0.9, 0.6, 0.95, 0.7}
	f, err := rerank.Run(context.Background(), scores, "q", cands(0.8, 0.7, 0.6, 0.2, 0.5),
		rerank.Params{KBefore: 5, SimMin: 0.4, RelMin: 0.5, KAfter: 2, Order: rerank.OrderRerank})
	if err != nil {
		t.Fatal(err)
	}
	stages := ""
	for _, c := range f.Candidates {
		stages += c.Stage + " "
	}
	// a -- реранкер 0.2; b 0.9 -- 1-й; c 0.6 -- не влез; d -- косинус; e 0.7 -- 2-й
	if stages != "rel kept top sim kept " {
		t.Fatalf("стадии: %s", stages)
	}
	if f.Total != 5 || f.PassedSim != 4 || f.PassedRel != 3 || f.Kept != 2 {
		t.Fatalf("счётчики: %+v", f)
	}
	final := f.Final()
	if len(final) != 2 || final[0].ChunkID != "b" || final[1].ChunkID != "e" || final[0].Final != 1 {
		t.Fatalf("итог: %+v", final)
	}
	if f.Candidates[3].Rel != nil || !strings.Contains(f.Candidates[3].Reason, "косинус") ||
		!strings.Contains(f.Candidates[0].Reason, "0.20") || !strings.Contains(f.Candidates[2].Reason, "top-2") {
		t.Errorf("причины: %+v", f.Candidates)
	}
}

func TestFunnelCanBeEmpty(t *testing.T) {
	f, err := rerank.Run(context.Background(), fixed{0.1, 0.1}, "q", cands(0.9, 0.9),
		rerank.Params{KBefore: 2, RelMin: 0.5, KAfter: 5})
	if err != nil || f.Kept != 0 || len(f.Final()) != 0 {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestParamsValidate(t *testing.T) {
	ok := rerank.Params{KBefore: 20, SimMin: 0.3, RelMin: 0.5, KAfter: 5, Order: rerank.OrderFused}
	if ok.Validate() != nil {
		t.Fatal("допустимые значения")
	}
	for _, p := range []rerank.Params{
		{KBefore: 0, KAfter: 5}, {KBefore: 51, KAfter: 5}, {KBefore: 5, KAfter: 0}, {KBefore: 5, KAfter: 11},
		{KBefore: 5, KAfter: 5, SimMin: -0.1, Order: "cosine"}, {KBefore: 5, KAfter: 5, RelMin: 1.1, Order: "cosine"},
		{KBefore: 5, KAfter: 5, Order: "random"},
	} {
		if p.Validate() == nil {
			t.Errorf("принято %+v", p)
		}
	}
}

func TestSortOrders(t *testing.T) {
	rel := []float64{0.1, 0.9, 0.5}
	for order, want := range map[string]string{
		rerank.OrderCosine: "012", rerank.OrderRerank: "120", rerank.OrderFused: "102",
	} {
		idx := []int{0, 1, 2}
		rerank.Sort(idx, rel, order)
		got := ""
		for _, i := range idx {
			got += string(rune('0' + i))
		}
		if got != want {
			t.Errorf("%s: %s, ожидалось %s", order, got, want)
		}
	}
}
