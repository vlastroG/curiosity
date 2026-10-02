// Package retrieve -- поиск контекста для RAG: какой текст превращать
// в вектор, чем дополнять плотный поиск и какие чанки отдавать модели.
//
// Конфигурация описывает четыре решения:
//   - запрос: вопрос как есть, перевод на английский (язык книг), HyDE --
//     гипотетический английский абзац-ответ, или слияние вопроса и перевода;
//   - гибрид: к векторному поиску добавляется BM25 по английскому запросу;
//   - small-to-big: ищем по мелким чанкам, а модели отдаём их родителей;
//   - вариант индекса и его модель эмбеддингов.
//
// Несколько ранжированных списков сливаются через RRF.
package retrieve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"doc-index/internal/chunk"
	"doc-index/internal/index"
	"doc-index/internal/search"
	"doc-index/internal/store"
)

// Режимы запроса.
const (
	QueryRaw     = "raw"  // вопрос как есть
	QueryEnglish = "en"   // перевод на английский
	QueryHyDE    = "hyde" // гипотетический абзац-ответ
	QueryFuse    = "fuse" // вопрос + перевод, слияние RRF
)

// Config -- конфигурация поиска.
type Config struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Variant string `json:"variant"`          // где идёт векторный поиск
	Parent  string `json:"parent,omitempty"` // small-to-big: чьи чанки отдаём модели
	Query   string `json:"query"`
	Hybrid  bool   `json:"hybrid"`
}

// NeedsRewrite -- нужен ли конфигурации вызов LLM перед поиском.
func (c Config) NeedsRewrite() bool {
	return c.Query != QueryRaw || c.Hybrid
}

// Rewrite -- вопрос, переписанный для поиска.
type Rewrite struct {
	EN   string `json:"en"`
	HyDE string `json:"hyde"`
}

// Result -- найденный контекст.
type Result struct {
	Hits    []search.Hit `json:"hits"`
	Variant string       `json:"variant"` // из какого варианта чанки
	Queries []string     `json:"queries"` // что превращалось в векторы
	Lexical string       `json:"lexical,omitempty"`
	EmbedMs float64      `json:"embedMs"`
	RankMs  float64      `json:"rankMs"`
}

// Retriever ищет по индексу в памяти поисковика.
type Retriever struct {
	Searcher *search.Searcher

	mu      sync.Mutex
	version string
	bm25    map[string]*BM25
	parents map[string][]int // "дочерний>родитель" → номер родителя для каждого дочернего чанка
}

// rrfK -- сглаживание RRF; 60 -- общепринятое значение.
const rrfK = 60

// ErrNoRewrite -- конфигурации нужен перевод или HyDE, а их нет.
var ErrNoRewrite = errors.New("для этого режима поиска нужен переписанный вопрос")

// Retrieve возвращает k чанков для вопроса.
func (r *Retriever) Retrieve(ctx context.Context, cfg Config, question string, rw Rewrite, bookID string, k int) (Result, error) {
	if err := r.Searcher.Refresh(ctx); err != nil {
		return Result{}, err
	}
	v, ok := index.Find(r.Searcher.Variants, cfg.Variant)
	if !ok {
		return Result{}, fmt.Errorf("нет варианта индекса %q", cfg.Variant)
	}

	var queries []string
	switch cfg.Query {
	case QueryRaw:
		queries = []string{question}
	case QueryEnglish:
		queries = []string{rw.EN}
	case QueryHyDE:
		queries = []string{rw.HyDE}
	case QueryFuse:
		queries = []string{question, rw.EN}
	default:
		return Result{}, fmt.Errorf("неизвестный режим запроса %q", cfg.Query)
	}
	for _, q := range queries {
		if q == "" {
			return Result{}, ErrNoRewrite
		}
	}
	res := Result{Queries: queries}

	var lists [][]search.Scored
	var first []float32
	for _, q := range queries {
		qv, dur, err := r.Searcher.QueryVector(ctx, v.Model, q)
		if err != nil {
			return Result{}, err
		}
		if first == nil {
			first = qv
		}
		if dur > 0 {
			res.EmbedMs += float64(dur.Microseconds()) / 1000
		}
		started := time.Now()
		list, err := r.Searcher.Dense(v.ID, qv, bookID)
		if err != nil {
			return Result{}, err
		}
		lists = append(lists, list)
		res.RankMs += float64(time.Since(started).Microseconds()) / 1000
	}

	started := time.Now()
	if cfg.Hybrid {
		lexical := rw.EN
		if lexical == "" {
			lexical = question
		}
		res.Lexical = lexical
		lists = append(lists, r.lexical(v.ID, lexical, bookID))
	}
	ranked := lists[0]
	if len(lists) > 1 {
		ranked = RRF(lists, rrfK)
	}

	target := v.ID
	if cfg.Parent != "" {
		ranked = r.toParents(v.ID, cfg.Parent, ranked)
		target = cfg.Parent
	}
	if len(ranked) > k {
		ranked = ranked[:k]
	}
	// в карточке -- понятное сходство: косинус к вектору первого запроса,
	// а не служебная оценка RRF
	cs := r.Searcher.Chunks(target)
	for i := range ranked {
		ranked[i].Score = chunk.Dot(first, cs[ranked[i].I].Vector)
	}
	res.Hits, res.Variant = r.Searcher.Hits(target, ranked, k), target
	res.RankMs += float64(time.Since(started).Microseconds()) / 1000
	return res, nil
}

// lexical -- BM25 по чанкам варианта; индекс строится при первом запросе
// и перестраивается, когда меняется индекс.
func (r *Retriever) lexical(variant, query, bookID string) []search.Scored {
	r.mu.Lock()
	r.resetIfStale()
	x := r.bm25[variant]
	if x == nil {
		cs := r.Searcher.Chunks(variant)
		texts := make([]string, len(cs))
		for i, c := range cs {
			texts[i] = c.Text
		}
		x = NewBM25(texts)
		r.bm25[variant] = x
	}
	r.mu.Unlock()
	list := x.Rank(query)
	if bookID == "" {
		return list
	}
	cs := r.Searcher.Chunks(variant)
	out := list[:0:0]
	for _, s := range list {
		if cs[s.I].Book == bookID {
			out = append(out, s)
		}
	}
	return out
}

// toParents заменяет мелкие чанки на родительские: родитель -- чанк
// крупного варианта той же книги, в который попадает середина мелкого.
// Порядок -- по первому появлению, дубликаты схлопываются.
func (r *Retriever) toParents(child, parent string, ranked []search.Scored) []search.Scored {
	r.mu.Lock()
	r.resetIfStale()
	key := child + ">" + parent
	m, ok := r.parents[key]
	if !ok {
		m = parentMap(r.Searcher.Chunks(child), r.Searcher.Chunks(parent))
		r.parents[key] = m
	}
	r.mu.Unlock()

	seen := map[int]bool{}
	var out []search.Scored
	for _, x := range ranked {
		p := m[x.I]
		if p < 0 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, search.Scored{I: p, Score: x.Score})
	}
	return out
}

func parentMap(children, parents []store.Chunk) []int {
	byBook := map[string][]int{}
	for i, p := range parents {
		byBook[p.Book] = append(byBook[p.Book], i)
	}
	for _, list := range byBook {
		sort.Slice(list, func(a, b int) bool { return parents[list[a]].BodyStart < parents[list[b]].BodyStart })
	}
	out := make([]int, len(children))
	for i, c := range children {
		mid := (c.BodyStart + c.End) / 2
		out[i] = -1
		list := byBook[c.Book]
		// последний родитель, чьё тело начинается не позже середины
		j := sort.Search(len(list), func(j int) bool { return parents[list[j]].BodyStart > mid }) - 1
		if j >= 0 {
			out[i] = list[j]
		}
	}
	return out
}

func (r *Retriever) resetIfStale() {
	ver := r.Searcher.Version()
	if r.bm25 == nil || ver != r.version {
		r.bm25, r.parents, r.version = map[string]*BM25{}, map[string][]int{}, ver
	}
}
