// Package experiment -- подбор второго этапа поиска и сравнение режимов.
//
// На каждый контрольный вопрос (44 по «Тому Сойеру» и «Геку» и несколько
// вопросов, ответа на которые в книгах нет) один раз считаются кандидаты
// векторного поиска и оценки реранкера. Дальше режимы и пороги перебираются
// без новых вызовов моделей: это просто разные правила отбора по уже
// посчитанным числам.
//
//   - rewrite                    HyDE, top-5 по косинусу
//   - rewrite + порог косинуса   HyDE, top-K до, косинус ≥ SIM_MIN, top-5
//   - rewrite + реранкер         HyDE, top-20, реранкер, top-5
//   - rewrite + реранкер + порог HyDE, top-K до, SIM_MIN, реранкер, REL_MIN, top-K после
package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/compare"
	"doc-index/internal/embed"
	"doc-index/internal/index"
	"doc-index/internal/rag"
	"doc-index/internal/rerank"
	"doc-index/internal/retrieve"
	"doc-index/internal/search"
)

// Model -- единственная модель эмбеддингов.
const Model = "qwen3-embedding:4b"

// VariantMain -- вариант индекса: нарезка по структуре.
const VariantMain = "structure"

// Variants -- варианты индекса продукта.
func Variants() []index.Variant {
	return []index.Variant{{ID: VariantMain, Title: "По структуре · " + embed.Lookup(Model).Title,
		Strategy: index.Structure, Model: Model, Hint: "главы → абзацы, 500–1000 токенов"}}
}

// Defaults -- настройки, если подбора ещё не было.
var Defaults = rag.Settings{Query: retrieve.QueryHyDE,
	Params: rerank.Params{KBefore: 20, SimMin: 0.3, RelMin: 0.05, KAfter: 5, Order: rerank.OrderCosine}}

// maxCandidates -- сколько кандидатов считается на вопрос (верхняя граница перебора top-K до).
const maxCandidates = 30

// Metrics -- качество режима.
type Metrics struct {
	compare.Scores
	Precision     float64 `json:"precision"`     // доля отрывков из нужной главы среди ушедших в модель
	AvgKept       float64 `json:"avgKept"`       // сколько отрывков в среднем уходит в модель
	OffTopicEmpty float64 `json:"offTopicEmpty"` // доля вопросов вне книг с пустым контекстом
}

// Mode -- режим и его метрики.
type Mode struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Query    string        `json:"query"`
	Rerank   bool          `json:"rerank"`
	Params   rerank.Params `json:"params"`
	Metrics  Metrics       `json:"metrics"`
	RerankMs float64       `json:"rerankMs,omitempty"` // реранкер на вопрос
}

// Sweep -- одна точка перебора порогов.
type Sweep struct {
	Params  rerank.Params `json:"params"`
	Metrics Metrics       `json:"metrics"`
}

// Report -- итог стенда.
type Report struct {
	GeneratedAt  time.Time    `json:"generatedAt"`
	Model        string       `json:"model"`
	Reranker     string       `json:"reranker"`
	RewriteModel string       `json:"rewriteModel"`
	Questions    int          `json:"questions"`
	OffTopic     int          `json:"offTopic"`
	Modes        []Mode       `json:"modes"`
	Sweep        []Sweep      `json:"sweep"`
	Chosen       rag.Settings `json:"chosen"`
	Notes        []string     `json:"notes,omitempty"`
}

// Runner -- стенд.
type Runner struct {
	Dir       string // data/experiments
	Retriever *retrieve.Retriever
	Reranker  rerank.Scorer
	Rewriter  *rag.Rewriter
	Log       func(format string, args ...any)
}

// prepared -- всё посчитанное для одного вопроса.
type prepared struct {
	q     compare.Question
	raw   []search.Hit // вопрос как есть, по косинусу
	hyde  []search.Hit // HyDE, по косинусу
	rel   []float64    // оценки реранкера для hyde
	relMs float64
}

// Run считает кандидатов и оценки, сравнивает режимы, перебирает пороги
// и сохраняет отчёт в Dir/report.json.
func (r *Runner) Run(ctx context.Context, questions []compare.Question) (Report, error) {
	rep := Report{GeneratedAt: time.Now(), Model: Model, Reranker: r.Reranker.Name()}
	s := r.Retriever.Searcher
	if err := s.Refresh(ctx); err != nil {
		return rep, err
	}
	books := map[string]*book.Book{}
	for id, info := range s.Books() {
		books[id] = info.Book
	}
	var valid []compare.Question
	for _, q := range compare.Validate(questions, books) {
		if !q.Valid {
			r.Log("[experiment] вопрос %s пропущен: %s", q.ID, q.Problem)
			continue
		}
		valid = append(valid, q)
		if q.OffTopic() {
			rep.OffTopic++
		} else {
			rep.Questions++
		}
	}

	texts := make([]string, len(valid))
	for i, q := range valid {
		texts[i] = q.Q
	}
	rep.RewriteModel = r.Rewriter.LLM.Model
	rewrites, err := r.Rewriter.Batch(ctx, texts)
	if err != nil {
		rep.Notes = append(rep.Notes, "переписаны не все вопросы: "+err.Error())
		r.Log("[rewrite] %v", err)
	}
	r.Log("[rewrite] переписано %d из %d вопросов (%s)", len(rewrites), len(texts), rep.RewriteModel)

	chunkText := map[string]string{}
	for _, c := range s.Chunks(VariantMain) {
		chunkText[c.ChunkID] = c.Text
	}
	var data []prepared
	for i, q := range valid {
		rw, ok := rewrites[q.Q]
		if !ok {
			continue
		}
		p := prepared{q: q}
		raw, err := r.Retriever.Retrieve(ctx, retrieve.Config{Variant: VariantMain, Query: retrieve.QueryRaw}, q.Q, rw, "", maxCandidates)
		if err != nil {
			return rep, err
		}
		hyde, err := r.Retriever.Retrieve(ctx, retrieve.Config{Variant: VariantMain, Query: retrieve.QueryHyDE}, q.Q, rw, "", maxCandidates)
		if err != nil {
			return rep, err
		}
		p.raw, p.hyde = raw.Hits, hyde.Hits
		docs := make([]string, len(p.hyde))
		for j, h := range p.hyde {
			docs[j] = chunkText[h.ChunkID]
		}
		started := time.Now()
		p.rel, err = r.Reranker.Score(ctx, rw.EN, docs)
		if err != nil {
			return rep, err
		}
		p.relMs = float64(time.Since(started).Milliseconds())
		data = append(data, p)
		r.Log("[experiment] %d/%d %s: кандидаты и оценки реранкера за %.1f с", i+1, len(valid), q.ID, p.relMs/1000)
	}

	// перебор порогов полного режима
	for _, order := range []string{rerank.OrderRerank, rerank.OrderCosine, rerank.OrderFused} {
		for _, kb := range []int{10, 20, 30} {
			for _, sim := range []float64{0, 0.3, 0.35, 0.4, 0.45, 0.5} {
				for _, rel := range []float64{0, 0.01, 0.02, 0.05, 0.1, 0.2, 0.3, 0.5, 0.7, 0.9} {
					p := rerank.Params{KBefore: kb, SimMin: sim, RelMin: rel, KAfter: 5, Order: order}
					m := evaluate(data, func(d prepared) []search.Hit { return full(d, p) })
					rep.Sweep = append(rep.Sweep, Sweep{Params: p, Metrics: m})
				}
			}
		}
	}
	chosen := choose(rep.Sweep, rep.Questions, rep.OffTopic)
	rep.Chosen = rag.Settings{Query: retrieve.QueryHyDE, Params: chosen}

	top := func(hits []search.Hit, k int) []search.Hit { return hits[:min(k, len(hits))] }
	var relMs float64
	for _, d := range data {
		relMs += d.relMs * float64(chosen.KBefore) / float64(max(1, len(d.hyde)))
	}
	rep.Modes = []Mode{
		{ID: "rewrite", Title: "+ rewrite (HyDE), top-5", Query: retrieve.QueryHyDE,
			Params:  rerank.Params{KBefore: 5, KAfter: 5},
			Metrics: evaluate(data, func(d prepared) []search.Hit { return top(d.hyde, 5) })},
		{ID: "rewrite-sim", Title: "+ rewrite + порог косинуса", Query: retrieve.QueryHyDE,
			Params: rerank.Params{KBefore: chosen.KBefore, SimMin: chosen.SimMin, KAfter: 5},
			Metrics: evaluate(data, func(d prepared) []search.Hit {
				var out []search.Hit
				for _, h := range top(d.hyde, chosen.KBefore) {
					if h.Cosine >= chosen.SimMin && len(out) < 5 {
						out = append(out, h)
					}
				}
				return out
			})},
		{ID: "rewrite-rerank", Title: "+ rewrite + реранкер (top-20 → top-5)", Query: retrieve.QueryHyDE, Rerank: true,
			Params: rerank.Params{KBefore: 20, KAfter: 5, Order: rerank.OrderRerank},
			Metrics: evaluate(data, func(d prepared) []search.Hit {
				return full(d, rerank.Params{KBefore: 20, KAfter: 5, Order: rerank.OrderRerank})
			})},
		{ID: "full", Title: "+ rewrite + реранкер + пороги", Query: retrieve.QueryHyDE, Rerank: true, Params: chosen,
			Metrics:  evaluate(data, func(d prepared) []search.Hit { return full(d, chosen) }),
			RerankMs: relMs / float64(max(1, len(data)))},
	}
	for _, m := range rep.Modes {
		r.Log("[experiment] %-40s верный контекст %3.0f%%  hit@1 %3.0f%%  hit@5 %3.0f%%  MRR %.2f  точность %3.0f%%  отрывков %.1f  вне книг пусто %3.0f%%",
			m.Title, 100*Correct(m.Metrics, rep.Questions, rep.OffTopic), 100*m.Metrics.Hit1, 100*m.Metrics.Hit5,
			m.Metrics.MRR, 100*m.Metrics.Precision, m.Metrics.AvgKept, 100*m.Metrics.OffTopicEmpty)
	}
	r.Log("[experiment] выбрано: top-K до %d, косинус ≥ %.2f, реранкер ≥ %.2f, top-K после %d, порядок %s",
		chosen.KBefore, chosen.SimMin, chosen.RelMin, chosen.KAfter, chosen.Order)
	return rep, Save(filepath.Join(r.Dir, "report.json"), rep)
}

// full -- отбор полного режима по уже посчитанным оценкам.
func full(d prepared, p rerank.Params) []search.Hit {
	var passed []int
	for j, h := range d.hyde {
		if j >= p.KBefore {
			break
		}
		if h.Cosine < p.SimMin || d.rel[j] < p.RelMin {
			continue
		}
		passed = append(passed, j)
	}
	rerank.Sort(passed, d.rel, p.Order)
	var out []search.Hit
	for n, j := range passed {
		if n >= p.KAfter {
			break
		}
		out = append(out, d.hyde[j])
	}
	return out
}

// evaluate -- метрики режима: pick отдаёт отрывки, которые уйдут в модель.
func evaluate(data []prepared, pick func(prepared) []search.Hit) Metrics {
	var m Metrics
	var qs []compare.Question
	var places []compare.Place
	var prec float64
	kept, off, offEmpty := 0, 0, 0
	for _, d := range data {
		hits := pick(d)
		kept += len(hits)
		if d.q.OffTopic() {
			off++
			if len(hits) == 0 {
				offEmpty++
			}
			continue
		}
		for i := range hits {
			hits[i].Rank = i + 1
		}
		qs = append(qs, d.q)
		places = append(places, compare.Locate(d.q, hits))
		if len(hits) > 0 {
			good := 0
			for _, h := range hits {
				if compare.Locate(d.q, []search.Hit{h}).Rank > 0 {
					good++
				}
			}
			prec += float64(good) / float64(len(hits))
		}
	}
	m.Scores = compare.ScoresOf(qs, places, "")
	if len(qs) > 0 {
		m.Precision = prec / float64(len(qs))
	}
	if len(data) > 0 {
		m.AvgKept = float64(kept) / float64(len(data))
	}
	if off > 0 {
		m.OffTopicEmpty = float64(offEmpty) / float64(off)
	}
	return m
}

// Correct -- доля вопросов с правильным контекстом: у вопроса по книгам
// нужная глава среди отрывков, у вопроса вне книг -- ни одного отрывка.
func Correct(m Metrics, in, off int) float64 {
	if in+off == 0 {
		return 0
	}
	return (m.Hit5*float64(in) + m.OffTopicEmpty*float64(off)) / float64(in+off)
}

// choose -- настройки с наибольшей долей правильного контекста; при равенстве
// -- выше hit@1 (нужное на первом месте), выше точность, меньше кандидатов
// (реранкер быстрее).
func choose(sweep []Sweep, in, off int) rerank.Params {
	if len(sweep) == 0 {
		return Defaults.Params
	}
	ok := append([]Sweep(nil), sweep...)
	sort.SliceStable(ok, func(i, j int) bool {
		a, b := ok[i].Metrics, ok[j].Metrics
		if ca, cb := Correct(a, in, off), Correct(b, in, off); ca != cb {
			return ca > cb
		}
		if a.Hit1 != b.Hit1 {
			return a.Hit1 > b.Hit1
		}
		if a.Precision != b.Precision {
			return a.Precision > b.Precision
		}
		return ok[i].Params.KBefore < ok[j].Params.KBefore
	})
	return ok[0].Params
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

// Load читает отчёт; нет файла -- ok=false без ошибки.
func Load(path string) (Report, bool, error) {
	var rep Report
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rep, false, nil
	}
	if err != nil {
		return rep, false, err
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return rep, false, fmt.Errorf("%s: %w", path, err)
	}
	return rep, true, nil
}
