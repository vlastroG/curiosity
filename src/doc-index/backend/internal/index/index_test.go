package index_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/chunk"
	"doc-index/internal/compare"
	"doc-index/internal/embed"
	"doc-index/internal/index"
	"doc-index/internal/search"
	"doc-index/internal/store"
	"doc-index/internal/testkit"
)

type env struct {
	ollama *testkit.Ollama
	store  *store.Store
	ix     *index.Indexer
	dir    string
}

// setup -- индекс на синтетической книге. Книга кладётся прямо в кэш:
// сеть тестам не нужна.
func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := testkit.GutenbergFile("A Test Novel", testkit.Novel())
	if err := os.WriteFile(filepath.Join(dir, "sources", "pg74.txt"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	o := testkit.NewOllama("bge-m3") // nomic ещё не скачана -- индексатор скачает сам
	t.Cleanup(o.Close)
	st, err := store.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix := &index.Indexer{
		Store: st, Ollama: embed.New(o.URL, 10*time.Second),
		Fetcher:  &book.Fetcher{Mirror: "http://mirror.invalid", Dir: filepath.Join(dir, "sources")},
		Books:    []int{74},
		Variants: index.Variants("bge-m3", "nomic-embed-text"),
		Params:   chunk.DefaultParams, Batch: 8,
	}
	return &env{ollama: o, store: st, ix: ix, dir: dir}
}

func collect(events *[]index.Event) func(index.Event) {
	return func(e index.Event) { *events = append(*events, e) }
}

func stages(events []index.Event) string {
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		if !seen[e.Stage] {
			seen[e.Stage] = true
			out = append(out, e.Stage)
		}
	}
	return strings.Join(out, ",")
}

func TestIndexBuildsAllVariantsThenSkips(t *testing.T) {
	e, ctx := setup(t), context.Background()
	var events []index.Event
	sum, err := e.ix.Run(ctx, false, collect(&events))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Built != 4 || sum.Skipped != 0 {
		t.Fatalf("построено %d, пропущено %d", sum.Built, sum.Skipped)
	}
	got := stages(events)
	for _, want := range []string{"ollama", "pull", "fetch", "parse", "calibrate", "gpu", "chunk", "sentences", "embed", "store", "done"} {
		if !strings.Contains(got, want) {
			t.Errorf("нет шага %q в %s", want, got)
		}
	}
	states, _ := e.store.States(ctx)
	if len(states) != 4 {
		t.Fatalf("готовых вариантов %d", len(states))
	}
	for _, st := range states {
		if st.Dims != testkit.Dims || st.Chunks == 0 || st.RealTokens == 0 {
			t.Errorf("%s: %+v", st.Variant, st)
		}
	}
	// метаданные чанка
	cs, _ := e.store.Chunks(ctx, "structure", "tom", true)
	c := cs[3]
	if c.Source != "gutenberg:74" || c.Title != "The Adventures of Tom Sawyer" || c.Author == "" ||
		!strings.HasPrefix(c.Section, "Chapter I. ") || c.ChunkID != "structure-tom-0003" || len(c.Vector) != testkit.Dims {
		t.Errorf("метаданные: %+v", c)
	}

	callsBefore, _ := e.ollama.Stats()
	events = nil
	sum, err = e.ix.Run(ctx, false, collect(&events))
	if err != nil {
		t.Fatal(err)
	}
	callsAfter, _ := e.ollama.Stats()
	if sum.Built != 0 || sum.Skipped != 4 || callsAfter != callsBefore {
		t.Fatalf("повторный запуск что-то пересчитал: built=%d, вызовов embed %d → %d", sum.Built, callsBefore, callsAfter)
	}
}

func TestIndexResumesAfterFailure(t *testing.T) {
	e, ctx := setup(t), context.Background()
	e.ix.Variants = e.ix.Variants[:1] // fixed
	if _, err := e.ix.Run(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	full, _ := e.store.Chunks(ctx, "fixed", "", false)

	// перестраиваем с нуля и падаем на третьей пачке
	e.ollama.SetFailAt(30 + 3) // 30 образцов калибровки + 3 пачки
	if _, err := e.ix.Run(ctx, true, nil); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	partial, _ := e.store.Chunks(ctx, "fixed", "", false)
	if len(partial) == 0 || len(partial) >= len(full) {
		t.Fatalf("после сбоя в индексе %d из %d чанков", len(partial), len(full))
	}

	_, inputsBefore := e.ollama.Stats()
	var events []index.Event
	if _, err := e.ix.Run(ctx, false, collect(&events)); err != nil {
		t.Fatal(err)
	}
	_, inputsAfter := e.ollama.Stats()
	if inputsAfter-inputsBefore != len(full)-len(partial) {
		t.Errorf("досчитано %d входов, ожидалось %d", inputsAfter-inputsBefore, len(full)-len(partial))
	}
	resumed := false
	for _, ev := range events {
		resumed = resumed || strings.Contains(ev.Message, "продолжаю прерванную")
	}
	if !resumed {
		t.Error("нет сообщения о продолжении")
	}
}

func TestIndexRequiresGPU(t *testing.T) {
	e, ctx := setup(t), context.Background()
	e.ollama.SetVRAM(0)
	_, err := e.ix.Run(ctx, false, nil)
	if err == nil || !strings.Contains(err.Error(), "на процессоре") {
		t.Fatalf("без GPU индексация должна остановиться: %v", err)
	}
}

func TestSearchAndCompare(t *testing.T) {
	e, ctx := setup(t), context.Background()
	if _, err := e.ix.Run(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	s := &search.Searcher{Store: e.store, Embedder: e.ix.Ollama, Variants: e.ix.Variants}
	ids := []string{"fixed", "structure", "semantic", "structure-nomic"}
	resp, err := s.Search(ctx, "the whitewash on the fence and the brush", ids, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		hits := resp.Results[id]
		if len(hits) == 0 {
			t.Fatalf("%s: пусто", id)
		}
		top := hits[0]
		inI := false
		for _, s := range top.Sections {
			inI = inI || s.Key == "I"
		}
		if top.Book != "tom" || !inI || top.Score <= 0.5 || top.Score > 1 {
			t.Errorf("%s: первое место %+v", id, top)
		}
		if top.Start > top.BodyStart || top.BodyStart >= top.End || top.Snippet == "" {
			t.Errorf("%s: границы и фрагмент %+v", id, top)
		}
	}
	if resp.EmbedMs["bge-m3"] <= 0 {
		t.Error("время эмбеддинга вопроса")
	}
	if _, err := s.Search(ctx, "x", []string{"nope"}, "", 5); err == nil {
		t.Error("неизвестный вариант")
	}
	if r, _ := s.Search(ctx, "the whitewash on the fence", []string{"structure"}, "huck", 5); len(r.Results["structure"]) != 0 {
		t.Error("фильтр по книге")
	}

	qs := []compare.Question{
		{ID: "a", Lang: "en", Book: "tom", Chapters: []string{"I"}, Evidence: []string{"whitewash"}, Q: "whitewash fence brush paint"},
		{ID: "b", Lang: "en", Book: "tom", Chapters: []string{"III"}, Evidence: []string{"candle"}, Q: "the cave candle darkness bats"},
		{ID: "c", Lang: "ru", Book: "tom", Chapters: []string{"II"}, Evidence: []string{"graveyard"}, Q: "что было на кладбище"},
		{ID: "bad", Lang: "en", Book: "tom", Chapters: []string{"II"}, Evidence: []string{"whitewash"}, Q: "wrong chapter"},
	}
	rep, err := compare.Build(ctx, s, qs, chunk.DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Invalid) != 1 || rep.Invalid[0].ID != "bad" || len(rep.Questions) != 3 {
		t.Fatalf("проверка вопросов: invalid=%v, вопросов %d", rep.Invalid, len(rep.Questions))
	}
	byID := map[string]compare.VariantReport{}
	for _, v := range rep.Variants {
		byID[v.Variant.ID] = v
	}
	st := byID["structure"]
	if !st.Ready || st.ByLang["en"].Hit1 != 1 || st.All.N != 3 || st.Shape.CrossChapter != 0 || st.Shape.CutStart != 0 {
		t.Errorf("structure: %+v", st)
	}
	if f := byID["fixed"]; f.Shape.CrossChapter == 0 || f.Shape.CutStart == 0 {
		t.Errorf("fixed должен резать главы и предложения: %+v", f.Shape)
	}
	if sem := byID["semantic"]; sem.Boundaries == nil || sem.Boundaries.Chapters != 4 {
		t.Errorf("semantic: границы %+v", sem.Boundaries)
	}
	if len(rep.Conclusion) == 0 {
		t.Error("нет вывода")
	}
}
