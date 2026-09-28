// Package search -- поиск по индексу: вопрос → вектор → ближайшие чанки.
//
// Все векторы варианта держатся в памяти; вопрос превращается в вектор той же
// моделью, что и чанки варианта, и сравнивается со всеми чанками всех книг
// сразу. Книгу указывать не нужно: наверх поднимаются ближайшие по смыслу
// места, из какой бы книги они ни были.
package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"doc-index/internal/book"
	"doc-index/internal/chunk"
	"doc-index/internal/embed"
	"doc-index/internal/index"
	"doc-index/internal/store"
)

// Embedder -- то, что умеет превращать текст в векторы (Ollama или заглушка в тестах).
type Embedder interface {
	Embed(ctx context.Context, model string, inputs []string) (embed.Result, error)
}

// Searcher держит индекс в памяти и перечитывает его, когда индекс изменился.
type Searcher struct {
	Store    *store.Store
	Embedder Embedder
	Variants []index.Variant

	mu      sync.RWMutex
	version string
	chunks  map[string][]store.Chunk // вариант → чанки с векторами
	books   map[string]*BookInfo

	cacheMu sync.Mutex
	cache   map[string][]float32 // модель + вопрос → вектор
}

// BookInfo -- книга и то, что про неё нужно поиску и сравнению.
type BookInfo struct {
	Book           *book.Book
	SentenceStarts map[int]bool
	SentenceEnds   map[int]bool
	Sentences      []chunk.Sentence
}

// Refresh перечитывает индекс, если он поменялся с прошлого раза.
func (s *Searcher) Refresh(ctx context.Context) error {
	ver, err := s.Store.Meta(ctx, "index_version")
	if err != nil {
		return err
	}
	s.mu.RLock()
	fresh := s.chunks != nil && ver == s.version
	s.mu.RUnlock()
	if fresh {
		return nil
	}

	books, err := s.Store.Books(ctx)
	if err != nil {
		return err
	}
	infos := map[string]*BookInfo{}
	for _, b := range books {
		info := &BookInfo{Book: b, SentenceStarts: map[int]bool{}, SentenceEnds: map[int]bool{}}
		info.Sentences = chunk.Sentences(b)
		for _, st := range info.Sentences {
			info.SentenceStarts[st.Start] = true
			info.SentenceEnds[st.End] = true
		}
		for _, sec := range b.Sections {
			info.SentenceStarts[sec.Start] = true // заголовок главы -- тоже законное начало
		}
		infos[b.ID] = info
	}
	all := map[string][]store.Chunk{}
	for _, v := range s.Variants {
		cs, err := s.Store.Chunks(ctx, v.ID, "", true)
		if err != nil {
			return err
		}
		all[v.ID] = cs
	}

	s.mu.Lock()
	s.version, s.chunks, s.books = ver, all, infos
	s.mu.Unlock()
	return nil
}

// Books -- книги индекса.
func (s *Searcher) Books() map[string]*BookInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.books
}

// Chunks -- чанки варианта в памяти.
func (s *Searcher) Chunks(variant string) []store.Chunk {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chunks[variant]
}

// SectionRef -- глава, которую захватил чанк.
type SectionRef struct {
	N     int    `json:"n"`
	Key   string `json:"key"`
	Label string `json:"label"`
	Title string `json:"title"`
}

// Hit -- найденное место.
type Hit struct {
	Rank      int          `json:"rank"`
	ChunkID   string       `json:"chunkId"`
	Book      string       `json:"book"`
	BookTitle string       `json:"bookTitle"`
	Section   string       `json:"section"`
	Sections  []SectionRef `json:"sections"`
	Score     float64      `json:"score"`  // сходство в [0, 1]: (1 + cos) / 2
	Cosine    float64      `json:"cosine"` // как есть, от −1 до 1
	Snippet   string       `json:"snippet"`
	Tokens    int          `json:"tokens"`
	// в символах (для интерфейса), от начала текста книги
	Start     int  `json:"start"`
	BodyStart int  `json:"bodyStart"`
	End       int  `json:"end"`
	Main      int  `json:"main"`     // глава, где лежит середина чанка: туда ведёт «открыть в книге»
	CutStart  bool `json:"cutStart"` // начинается посреди предложения
	CutEnd    bool `json:"cutEnd"`   // обрывается посреди предложения
}

// Response -- выдача по всем запрошенным вариантам.
type Response struct {
	Query   string             `json:"query"`
	Results map[string][]Hit   `json:"results"`
	EmbedMs map[string]float64 `json:"embedMs"` // модель → время эмбеддинга вопроса
	RankMs  float64            `json:"rankMs"`
}

// ErrEmptyIndex -- индекс не построен.
var ErrEmptyIndex = errors.New("индекс не построен")

// Search ищет вопрос в вариантах. bookID -- фильтр по книге, "" -- все книги.
func (s *Searcher) Search(ctx context.Context, query string, variants []string, bookID string, k int) (Response, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Response{}, errors.New("пустой вопрос")
	}
	if err := s.Refresh(ctx); err != nil {
		return Response{}, err
	}
	resp := Response{Query: query, Results: map[string][]Hit{}, EmbedMs: map[string]float64{}}
	var rankDur time.Duration
	for _, id := range variants {
		v, ok := index.Find(s.Variants, id)
		if !ok {
			return Response{}, fmt.Errorf("неизвестный вариант %q", id)
		}
		qv, dur, err := s.QueryVector(ctx, v.Model, query)
		if err != nil {
			return Response{}, err
		}
		if dur >= 0 {
			resp.EmbedMs[v.Model] = math.Max(0.01, float64(dur.Microseconds())/1000)
		}
		started := time.Now()
		hits, err := s.Rank(v.ID, qv, bookID, k)
		if err != nil {
			return Response{}, err
		}
		rankDur += time.Since(started)
		resp.Results[v.ID] = hits
	}
	resp.RankMs = float64(rankDur.Microseconds()) / 1000
	return resp, nil
}

// QueryVector -- вектор вопроса для модели. Повторный вопрос берётся из кэша
// (время тогда -1).
func (s *Searcher) QueryVector(ctx context.Context, model, query string) ([]float32, time.Duration, error) {
	key := model + "\x00" + query
	s.cacheMu.Lock()
	if v, ok := s.cache[key]; ok {
		s.cacheMu.Unlock()
		return v, -1, nil
	}
	s.cacheMu.Unlock()

	started := time.Now()
	res, err := s.Embedder.Embed(ctx, model, []string{embed.Lookup(model).QueryPrefix + query})
	if err != nil {
		return nil, 0, err
	}
	dur := time.Since(started)
	s.cacheMu.Lock()
	if s.cache == nil || len(s.cache) > 1000 {
		s.cache = map[string][]float32{}
	}
	s.cache[key] = res.Vectors[0]
	s.cacheMu.Unlock()
	return res.Vectors[0], dur, nil
}

// Rank -- top-k чанков варианта по готовому вектору вопроса.
func (s *Searcher) Rank(variant string, qv []float32, bookID string, k int) ([]Hit, error) {
	s.mu.RLock()
	cs, books := s.chunks[variant], s.books
	s.mu.RUnlock()
	if len(cs) == 0 {
		return nil, ErrEmptyIndex
	}
	type scored struct {
		i   int
		cos float64
	}
	all := make([]scored, 0, len(cs))
	for i, c := range cs {
		if bookID != "" && c.Book != bookID {
			continue
		}
		all = append(all, scored{i, chunk.Dot(qv, c.Vector)})
	}
	sort.Slice(all, func(a, b int) bool { return all[a].cos > all[b].cos })
	if len(all) > k {
		all = all[:k]
	}
	hits := make([]Hit, len(all))
	for r, x := range all {
		hits[r] = MakeHit(cs[x.i], books[cs[x.i].Book], r+1, x.cos)
	}
	return hits, nil
}

// MakeHit -- карточка найденного места.
func MakeHit(c store.Chunk, info *BookInfo, rank int, cos float64) Hit {
	h := Hit{Rank: rank, ChunkID: c.ChunkID, Book: c.Book, BookTitle: c.Title, Section: c.Section,
		Score: (1 + cos) / 2, Cosine: cos, Tokens: c.Tokens}
	if info == nil {
		h.Snippet = snippet(c.Text, 320)
		return h
	}
	b := info.Book
	for _, n := range c.Sections {
		if n < len(b.Sections) {
			sec := b.Sections[n]
			h.Sections = append(h.Sections, SectionRef{N: n, Key: sec.Key, Label: sec.Label, Title: sec.Title})
		}
	}
	h.Start, h.BodyStart, h.End = b.Rune(c.Start), b.Rune(c.BodyStart), b.Rune(c.End)
	h.Main = b.SectionAt((c.BodyStart + c.End) / 2)
	h.CutStart = !info.SentenceStarts[c.Start]
	h.CutEnd = !info.SentenceEnds[c.End]
	h.Snippet = snippet(b.Text[c.BodyStart:c.End], 320)
	return h
}

// snippet -- начало текста, не длиннее n символов, по границе слова.
func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := string(r)
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}
