package retrieve_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/chunk"
	"doc-index/internal/embed"
	"doc-index/internal/index"
	"doc-index/internal/retrieve"
	"doc-index/internal/search"
	"doc-index/internal/store"
	"doc-index/internal/testkit"
)

func TestWordsAndBM25(t *testing.T) {
	if got := retrieve.Words("Tom’s fence -- and the WHITEWASH, it's 1876!"); !reflect.DeepEqual(got,
		[]string{"tom", "fence", "whitewash", "1876"}) {
		t.Fatalf("слова: %v", got)
	}
	x := retrieve.NewBM25([]string{
		"the cave was dark and the candle burned low",
		"whitewash the fence, said Aunt Polly",
		"the fence the fence the fence and a cat",
	})
	got := x.Rank("fence whitewash")
	if len(got) != 2 || got[0].I != 1 {
		t.Fatalf("редкое слово должно перевесить повторы частого: %+v", got)
	}
	if len(x.Rank("elephant")) != 0 {
		t.Error("незнакомое слово что-то нашло")
	}
}

func TestRRF(t *testing.T) {
	a := []search.Scored{{I: 1}, {I: 2}, {I: 3}}
	b := []search.Scored{{I: 3}, {I: 1}}
	got := retrieve.RRF([][]search.Scored{a, b}, 60)
	var order []int
	for _, s := range got {
		order = append(order, s.I)
	}
	// 1: 1/61+1/62, 3: 1/63+1/61, 2: 1/62
	if !reflect.DeepEqual(order, []int{1, 3, 2}) {
		t.Fatalf("порядок %v", order)
	}
}

// setup -- индекс синтетической книги: крупные и мелкие чанки.
func setup(t *testing.T) *retrieve.Retriever {
	t.Helper()
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "sources"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sources", "pg74.txt"), testkit.GutenbergFile("A Test Novel", testkit.Novel()), 0o644)
	o := testkit.NewOllama("bge-m3")
	t.Cleanup(o.Close)
	st, err := store.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	small := chunk.Params{Target: 220, Min: 120, Max: 300, Overlap: 40}
	variants := []index.Variant{
		{ID: "structure", Strategy: index.Structure, Model: "bge-m3"},
		{ID: "small", Strategy: index.Structure, Model: "bge-m3", Params: &small},
	}
	client := embed.New(o.URL, 10*time.Second)
	ix := &index.Indexer{Store: st, Ollama: client, Fetcher: &book.Fetcher{Dir: filepath.Join(dir, "sources")},
		Books: []int{74}, Variants: variants, Params: chunk.DefaultParams, Batch: 16}
	if _, err := ix.Run(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	return &retrieve.Retriever{Searcher: &search.Searcher{Store: st, Embedder: client, Variants: variants}}
}

func TestRetrieveModes(t *testing.T) {
	r := setup(t)
	ctx := context.Background()
	rw := retrieve.Rewrite{EN: "the cave candle darkness", HyDE: "In the cave the candle burned and the bats flew in darkness."}

	for _, cfg := range []retrieve.Config{
		{Variant: "structure", Query: retrieve.QueryEnglish},
		{Variant: "structure", Query: retrieve.QueryHyDE},
		{Variant: "structure", Query: retrieve.QueryFuse},
		{Variant: "structure", Query: retrieve.QueryEnglish, Hybrid: true},
		{Variant: "small", Parent: "structure", Query: retrieve.QueryEnglish, Hybrid: true},
	} {
		res, err := r.Retrieve(ctx, cfg, "lost in the cave with a candle", rw, "", 3)
		if err != nil {
			t.Fatalf("%+v: %v", cfg, err)
		}
		if len(res.Hits) == 0 || res.Hits[0].Sections[0].Key != "III" {
			t.Errorf("%+v: первое место %+v", cfg, res.Hits)
		}
		if res.Variant != "structure" {
			t.Errorf("%+v: отрывки из варианта %s, ожидались родительские", cfg, res.Variant)
		}
		seen := map[string]bool{}
		for _, h := range res.Hits {
			if seen[h.ChunkID] {
				t.Errorf("%+v: повтор отрывка %s", cfg, h.ChunkID)
			}
			seen[h.ChunkID] = true
			if h.Score <= 0.5 || h.Score > 1 {
				t.Errorf("%+v: сходство %.3f вне (0.5, 1]", cfg, h.Score)
			}
		}
		if cfg.Hybrid && res.Lexical != rw.EN {
			t.Errorf("BM25 должен искать по переводу: %q", res.Lexical)
		}
	}
	fuse, _ := r.Retrieve(ctx, retrieve.Config{Variant: "structure", Query: retrieve.QueryFuse}, "вопрос", rw, "", 3)
	if !reflect.DeepEqual(fuse.Queries, []string{"вопрос", rw.EN}) {
		t.Errorf("слияние: %v", fuse.Queries)
	}
	if _, err := r.Retrieve(ctx, retrieve.Config{Variant: "structure", Query: retrieve.QueryEnglish}, "q", retrieve.Rewrite{}, "", 3); err != retrieve.ErrNoRewrite {
		t.Errorf("без перевода: %v", err)
	}
	if _, err := r.Retrieve(ctx, retrieve.Config{Variant: "nope", Query: retrieve.QueryRaw}, "q", rw, "", 3); err == nil {
		t.Error("неизвестный вариант")
	}
}
