// Package experiment -- стенд выбора стратегии поиска для RAG.
//
// Для каждой модели эмбеддингов строится свой индекс по всем книгам
// (data/experiments/<модель>.db), и 44 контрольных вопроса прогоняются через
// разные способы поиска. Нарезка у всех моделей одна и та же (один
// коэффициент «символов на токен»), так что сравниваются именно модели
// и способы запроса.
//
//	этап 1: все модели × {вопрос как есть, перевод, HyDE, слияние, + BM25}
//	этап 2: лучшая модель × small-to-big (поиск по мелким чанкам, в ответ -- родители)
//
// Победитель -- наибольший hit@5 (нужная глава среди пяти отрывков, которые
// получит модель), при равенстве -- MRR.
package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/chunk"
	"doc-index/internal/compare"
	"doc-index/internal/embed"
	"doc-index/internal/index"
	"doc-index/internal/rag"
	"doc-index/internal/retrieve"
	"doc-index/internal/search"
	"doc-index/internal/store"
)

// SmallParams -- мелкие чанки для small-to-big.
var SmallParams = chunk.Params{Target: 220, Min: 120, Max: 300, Overlap: 40}

// Variant ids в индексах стенда и продукта.
const (
	VariantMain  = "structure"
	VariantSmall = "small"
)

// Queries -- способы запроса этапа 1; в каждом этапе они же с BM25.
var Queries = []struct {
	Query  string
	Hybrid bool
	Title  string
}{
	{retrieve.QueryRaw, false, "вопрос как есть"},
	{retrieve.QueryEnglish, false, "перевод на английский"},
	{retrieve.QueryHyDE, false, "HyDE"},
	{retrieve.QueryFuse, false, "вопрос + перевод (RRF)"},
	{retrieve.QueryEnglish, true, "перевод + BM25 (RRF)"},
	{retrieve.QueryFuse, true, "вопрос + перевод + BM25 (RRF)"},
}

// Row -- одна конфигурация и её метрики.
type Row struct {
	Model   string                    `json:"model"`
	Config  retrieve.Config           `json:"config"`
	Phase   int                       `json:"phase"`
	All     compare.Scores            `json:"all"`
	ByLang  map[string]compare.Scores `json:"byLang"`
	Places  map[string]int            `json:"places"` // вопрос → место нужной главы
	QueryMs float64                   `json:"queryMs"`
}

// ModelInfo -- индекс модели.
type ModelInfo struct {
	Model        string  `json:"model"`
	Dims         int     `json:"dims"`
	Chunks       int     `json:"chunks"`
	IndexSeconds float64 `json:"indexSeconds"`
	VRAM         int64   `json:"vram"`
}

// Report -- итог стенда.
type Report struct {
	GeneratedAt   time.Time   `json:"generatedAt"`
	Books         int         `json:"books"`
	Questions     int         `json:"questions"`
	RewriteModel  string      `json:"rewriteModel"`
	CharsPerToken float64     `json:"charsPerToken"`
	Models        []ModelInfo `json:"models"`
	Rows          []Row       `json:"rows"`
	Winner        Row         `json:"winner"`
	Notes         []string    `json:"notes,omitempty"`
}

// Runner -- стенд.
type Runner struct {
	Dir      string // data/experiments
	Ollama   *embed.Client
	Fetcher  *book.Fetcher
	Books    []int
	Params   chunk.Params
	Models   []string
	Rewriter *rag.Rewriter
	Log      func(format string, args ...any)
}

// Run прогоняет стенд и сохраняет отчёт в Dir/report.json.
func (r *Runner) Run(ctx context.Context, questions []compare.Question) (Report, error) {
	rep := Report{GeneratedAt: time.Now(), Books: len(r.Books), Questions: len(questions)}

	// переписанные вопросы: одним-двумя вызовами модели, дальше из кэша
	texts := make([]string, len(questions))
	for i, q := range questions {
		texts[i] = q.Q
	}
	rewrites := map[string]retrieve.Rewrite{}
	if r.Rewriter != nil {
		rep.RewriteModel = r.Rewriter.LLM.Model
		got, err := r.Rewriter.Batch(ctx, texts)
		rewrites = got
		if err != nil {
			rep.Notes = append(rep.Notes, "переписаны не все вопросы: "+err.Error())
			r.Log("[rewrite] %v", err)
		}
		r.Log("[rewrite] переписано вопросов: %d из %d (%s)", len(got), len(texts), r.Rewriter.LLM.Model)
	}

	// этап 1: модели
	for i, model := range r.Models {
		st, info, err := r.build(ctx, model, &rep, false)
		if err != nil {
			return rep, fmt.Errorf("индекс %s: %w", model, err)
		}
		rep.Models = append(rep.Models, info)
		if i == 0 && rep.CharsPerToken == 0 {
			rep.CharsPerToken = r.charsPerToken(ctx, st, model)
		}
		rows, err := r.evaluate(ctx, st, model, questions, rewrites, 1, false)
		st.Close()
		if err != nil {
			return rep, err
		}
		rep.Rows = append(rep.Rows, rows...)
	}
	best := pick(rep.Rows)
	r.Log("[experiment] лучшая модель этапа 1: %s (%s)", best.Model, best.Config.Title)

	// этап 2: small-to-big на лучшей модели
	st, _, err := r.build(ctx, best.Model, &rep, true)
	if err != nil {
		return rep, fmt.Errorf("small-to-big %s: %w", best.Model, err)
	}
	rows, err := r.evaluate(ctx, st, best.Model, questions, rewrites, 2, true)
	st.Close()
	if err != nil {
		return rep, err
	}
	rep.Rows = append(rep.Rows, rows...)
	rep.Winner = pick(rep.Rows)
	r.Log("[experiment] победитель: %s, %s — hit@5 %.0f%%, MRR %.2f", rep.Winner.Model, rep.Winner.Config.Title,
		100*rep.Winner.All.Hit5, rep.Winner.All.MRR)
	return rep, Save(filepath.Join(r.Dir, "report.json"), rep)
}

// pick -- лучшая строка: hit@5, затем MRR, затем hit@1.
func pick(rows []Row) Row {
	sorted := append([]Row(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].All, sorted[j].All
		if a.Hit5 != b.Hit5 {
			return a.Hit5 > b.Hit5
		}
		if a.MRR != b.MRR {
			return a.MRR > b.MRR
		}
		return a.Hit1 > b.Hit1
	})
	if len(sorted) == 0 {
		return Row{}
	}
	return sorted[0]
}

// Slug -- имя файла индекса модели.
func Slug(model string) string {
	return strings.NewReplacer(":", "-", "/", "-", ".", "_").Replace(model)
}

// Variants -- варианты индекса для модели: основной и, для small-to-big, мелкий.
func Variants(model string, small bool) []index.Variant {
	vs := []index.Variant{{ID: VariantMain, Title: "По структуре · " + embed.Lookup(model).Title, Strategy: index.Structure,
		Model: model, Hint: "главы → абзацы, 500–1000 токенов"}}
	if small {
		p := SmallParams
		vs = append(vs, index.Variant{ID: VariantSmall, Title: "Мелкие чанки · " + embed.Lookup(model).Title,
			Strategy: index.Structure, Model: model, Params: &p,
			Hint: fmt.Sprintf("по структуре, %d–%d токенов: для поиска, модели уходят родители", p.Min, p.Max)})
	}
	return vs
}

func (r *Runner) build(ctx context.Context, model string, rep *Report, small bool) (*store.Store, ModelInfo, error) {
	info := ModelInfo{Model: model}
	st, err := store.Open(filepath.Join(r.Dir, Slug(model)+".db"))
	if err != nil {
		return nil, info, err
	}
	ix := &index.Indexer{Store: st, Ollama: r.Ollama, Fetcher: r.Fetcher, Books: r.Books,
		Variants: Variants(model, small), Params: r.Params, Batch: 16, CharsPerToken: rep.CharsPerToken}
	_, err = ix.Run(ctx, false, func(e index.Event) {
		if e.Stage != "embed" && e.Stage != "sentences" || e.Done == e.Total {
			r.Log("[%s] %s", e.Stage, e.Message)
		}
	})
	if err != nil {
		st.Close()
		return nil, info, err
	}
	// время индекса -- из состояния вариантов: при повторном прогоне индекс
	// не строится заново, а время первой постройки остаётся
	states, _ := st.States(ctx)
	for _, s := range states {
		if s.Variant == VariantMain {
			info.Chunks += s.Chunks
			info.Dims = s.Dims
			info.IndexSeconds += s.TotalSeconds
		}
	}
	if p, err := r.Ollama.Where(ctx, model); err == nil {
		info.VRAM = p.SizeVRAM
	}
	return st, info, nil
}

func (r *Runner) charsPerToken(ctx context.Context, st *store.Store, model string) float64 {
	v, _ := st.Meta(ctx, "chars_per_token:"+model)
	f, _ := strconv.ParseFloat(v, 64)
	return f
}

// evaluate -- все способы запроса на одном индексе.
func (r *Runner) evaluate(ctx context.Context, st *store.Store, model string, questions []compare.Question,
	rewrites map[string]retrieve.Rewrite, phase int, small bool) ([]Row, error) {
	s := &search.Searcher{Store: st, Embedder: r.Ollama, Variants: Variants(model, small)}
	ret := &retrieve.Retriever{Searcher: s}
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	books := map[string]*book.Book{}
	for id, info := range s.Books() {
		books[id] = info.Book
	}
	valid := compare.Validate(questions, books)

	var rows []Row
	for _, q := range Queries {
		cfg := retrieve.Config{Variant: VariantMain, Query: q.Query, Hybrid: q.Hybrid, Title: q.Title}
		if small {
			cfg.Variant, cfg.Parent = VariantSmall, VariantMain
			cfg.Title = "small-to-big · " + q.Title
		}
		cfg.ID = ConfigID(model, cfg)
		row := Row{Model: model, Config: cfg, Phase: phase, ByLang: map[string]compare.Scores{}, Places: map[string]int{}}
		var used []compare.Question
		var places []compare.Place
		started := time.Now()
		for _, q := range valid {
			if !q.Valid {
				continue
			}
			rw := rewrites[q.Q]
			res, err := ret.Retrieve(ctx, cfg, q.Q, rw, "", compare.TopK)
			if err == retrieve.ErrNoRewrite {
				continue // вопрос не переписан -- в этой конфигурации его нет
			}
			if err != nil {
				return nil, err
			}
			pl := compare.Locate(q, res.Hits)
			used = append(used, q)
			places = append(places, pl)
			row.Places[q.ID] = pl.Rank
		}
		if len(used) == 0 {
			r.Log("[experiment] %s / %s: нет вопросов (нет переписываний)", model, cfg.Title)
			continue
		}
		row.QueryMs = float64(time.Since(started).Milliseconds()) / float64(len(used))
		row.All = compare.ScoresOf(used, places, "")
		for _, lang := range []string{"ru", "en"} {
			if sc := compare.ScoresOf(used, places, lang); sc.N > 0 {
				row.ByLang[lang] = sc
			}
		}
		r.Log("[experiment] %-22s %-40s hit@1 %3.0f%%  hit@5 %3.0f%%  MRR %.2f  (ru hit@5 %3.0f%%, en %3.0f%%)",
			model, cfg.Title, 100*row.All.Hit1, 100*row.All.Hit5, row.All.MRR,
			100*row.ByLang["ru"].Hit5, 100*row.ByLang["en"].Hit5)
		rows = append(rows, row)
	}
	return rows, nil
}

// ConfigID -- «qwen3-embedding:4b/en+bm25/small».
func ConfigID(model string, c retrieve.Config) string {
	id := model + "/" + c.Query
	if c.Hybrid {
		id += "+bm25"
	}
	if c.Parent != "" {
		id += "/small-to-big"
	}
	return id
}

// Save пишет отчёт.
func Save(path string, rep Report) error {
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// Load читает отчёт; нет файла -- пустой отчёт без ошибки.
func Load(path string) (Report, bool, error) {
	var rep Report
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rep, false, nil
	}
	if err != nil {
		return rep, false, err
	}
	return rep, true, json.Unmarshal(raw, &rep)
}
