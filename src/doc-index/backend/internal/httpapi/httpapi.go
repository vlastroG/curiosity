// Package httpapi -- REST API интерфейса и раздача собранного фронтенда.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/chat"
	"doc-index/internal/chunk"
	"doc-index/internal/compare"
	"doc-index/internal/embed"
	"doc-index/internal/index"
	"doc-index/internal/rag"
	"doc-index/internal/search"
	"doc-index/internal/store"
)

// Ollama -- то, что API нужно от Ollama.
type Ollama interface {
	Version(ctx context.Context) (string, error)
	Where(ctx context.Context, model string) (embed.Placement, error)
	RequireGPU(ctx context.Context, model string) (embed.Placement, error)
}

// Config -- зависимости API.
type Config struct {
	Store     *store.Store
	Searcher  *search.Searcher
	Ollama    Ollama
	OllamaURL string
	Jobs      *index.Jobs
	// Index запускает индексацию; вызывается внутри Jobs.
	Index     func(ctx context.Context, rebuild bool, emit func(index.Event)) error
	EvalPath  string
	Params    chunk.Params
	StaticDir string

	// RAG и чат
	Agent        *rag.Agent
	Chats        *chat.Service
	ChatDefaults chat.Settings // настройки нового чата
	LLMBudget    int           // бюджет вывода модели ответов
	LLMError     error         // модель ответов недоступна (нет ключа и т. п.)
	LLMContext   int           // контекстное окно облачной модели; 0 -- неизвестно
	// LocalInfo -- сведения о локальной модели от llmcli; nil -- модель облачная
	LocalInfo func(ctx context.Context) (map[string]any, error)
	Tuned     bool // настройки поиска подобраны командой experiment
	// NoRAG -- RAG=off: чат без поиска, Agent не нужен; поиск и индексация выключены
	NoRAG bool
	Model string // модель ответов -- для статуса при NoRAG
}

// API -- обработчики.
type API struct {
	Config

	gpuMu sync.Mutex
	gpuOK map[string]bool

	cmpMu  sync.Mutex
	cmpKey string
	cmp    *compare.Report
}

// New собирает маршрутизатор: API под /api, остальное -- интерфейс.
func New(cfg Config) http.Handler {
	a := &API{Config: cfg, gpuOK: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", a.status)
	// без RAG поиск и индексация не нужны: эмбеддинги и реранкер не загружаются в видеопамять
	if !cfg.NoRAG {
		mux.HandleFunc("POST /api/search", a.search)
		mux.HandleFunc("GET /api/questions", a.questions)
		mux.HandleFunc("POST /api/index", a.startIndex)
		mux.HandleFunc("GET /api/index/events", a.indexEvents)
	}
	mux.HandleFunc("GET /api/books/{book}", a.book)
	mux.HandleFunc("GET /api/books/{book}/sections/{n}", a.section)
	mux.HandleFunc("GET /api/books/{book}/sections/{n}/chunks", a.sectionChunks)
	mux.HandleFunc("GET /api/chunks/{variant}/{id}", a.chunk)
	mux.HandleFunc("GET /api/chats", a.listChats)
	mux.HandleFunc("POST /api/chats", a.createChat)
	mux.HandleFunc("GET /api/chats/{id}", a.getChat)
	mux.HandleFunc("PATCH /api/chats/{id}", a.patchChat)
	mux.HandleFunc("DELETE /api/chats/{id}", a.deleteChat)
	mux.HandleFunc("PUT /api/chats/{id}/state", a.putState)
	mux.HandleFunc("POST /api/chats/{id}/messages", a.sendMessage)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/", spa(cfg.StaticDir))
	return securityHeaders(mux)
}

type bookJSON struct {
	ID        string `json:"id"`
	Gutenberg int    `json:"gutenberg"`
	Title     string `json:"title"`
	TitleRu   string `json:"titleRu"`
	Author    string `json:"author"`
	URL       string `json:"url"`
	Sections  int    `json:"sections"`
	Chars     int    `json:"chars"`
}

func bookInfo(b *book.Book) bookJSON {
	return bookJSON{ID: b.ID, Gutenberg: b.Gutenberg, Title: b.Title, TitleRu: b.TitleRu, Author: b.Author,
		URL: b.URL(), Sections: len(b.Sections), Chars: b.Rune(len(b.Text))}
}

type variantJSON struct {
	index.Variant
	Ready  bool          `json:"ready"`
	Chunks int           `json:"chunks"`
	Tokens int           `json:"tokens"`
	Dims   int           `json:"dims"`
	Books  []store.State `json:"books"`
}

// status -- всё для шапки и вкладки «Индекс» за один запрос.
func (a *API) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := a.Searcher.Refresh(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := map[string]any{"params": a.Params, "job": a.Jobs.State()}

	ol := map[string]any{"url": a.OllamaURL}
	if ver, err := a.Ollama.Version(ctx); err != nil {
		ol["error"] = err.Error()
	} else {
		ol["version"] = ver
		gpu := map[string]embed.Placement{}
		for _, v := range a.Searcher.Variants {
			if a.NoRAG {
				break
			}
			if p, err := a.Ollama.Where(ctx, v.Model); err == nil {
				gpu[v.Model] = p
			}
		}
		ol["models"] = gpu
	}
	out["ollama"] = ol

	var books []bookJSON
	for _, info := range sortedBooks(a.Searcher.Books()) {
		books = append(books, bookInfo(info.Book))
	}
	out["books"] = books

	states, err := a.Store.States(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var vs []variantJSON
	for _, v := range a.Searcher.Variants {
		vj := variantJSON{Variant: v, Books: []store.State{}}
		for _, st := range states {
			if st.Variant == v.ID {
				vj.Books = append(vj.Books, st)
				vj.Chunks += st.Chunks
				vj.Tokens += st.Tokens
				vj.Dims = st.Dims
			}
		}
		vj.Ready = len(vj.Books) > 0 && len(vj.Books) == len(books)
		vs = append(vs, vj)
	}
	out["variants"] = vs

	ragInfo := map[string]any{"embedModel": a.Searcher.Variants[0].Model}
	if a.NoRAG {
		ragInfo = map[string]any{"off": true, "model": a.Model}
	}
	if a.Agent != nil {
		ragInfo["model"] = a.Agent.LLM.Model
		ragInfo["reranker"] = a.Agent.Reranker.Name()
	}
	if a.Agent != nil || a.NoRAG {
		ragInfo["defaults"] = a.ChatDefaults
		ragInfo["budget"] = a.LLMBudget
		ragInfo["tuned"] = a.Tuned
		ragInfo["context"] = a.LLMContext
	}
	if a.LocalInfo != nil {
		ragInfo["local"] = true
		lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		info, err := a.LocalInfo(lctx)
		cancel()
		if err != nil {
			info = map[string]any{"error": "llmcli недоступен: " + err.Error()}
		}
		ragInfo["localInfo"] = info
	}
	if a.LLMError != nil {
		ragInfo["error"] = a.LLMError.Error()
	}
	out["rag"] = ragInfo

	// пример чанка с метаданными -- для вкладки «Индекс»
	for _, v := range a.Searcher.Variants {
		if v.Strategy == index.Structure {
			if cs := a.Searcher.Chunks(v.ID); len(cs) > 12 {
				c := cs[12]
				c.Text = shorten(c.Text, 400)
				out["sample"] = map[string]any{"variant": v.ID, "chunk": c, "vectorHead": head(c.Vector, 8),
					"vectorDims": len(c.Vector)}
			}
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type searchRequest struct {
	Query    string   `json:"query"`
	Variants []string `json:"variants"`
	Book     string   `json:"book"`
	K        int      `json:"k"`
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("неверный запрос"))
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	switch {
	case req.Query == "":
		writeError(w, http.StatusBadRequest, errors.New("введите вопрос"))
		return
	case len([]rune(req.Query)) > 500:
		writeError(w, http.StatusBadRequest, errors.New("вопрос длиннее 500 символов"))
		return
	case len(req.Variants) == 0 || len(req.Variants) > 4:
		writeError(w, http.StatusBadRequest, errors.New("выберите от одного до четырёх вариантов"))
		return
	}
	if req.K <= 0 || req.K > 10 {
		req.K = 5
	}
	resp, err := a.Searcher.Search(r.Context(), req.Query, req.Variants, req.Book, req.K)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, search.ErrEmptyIndex) {
			status = http.StatusConflict
		} else if strings.Contains(err.Error(), "неизвестный вариант") {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	// только GPU: первая же загрузка модели проверяется
	for model := range resp.EmbedMs {
		if err := a.requireGPU(r.Context(), model); err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) requireGPU(ctx context.Context, model string) error {
	a.gpuMu.Lock()
	ok := a.gpuOK[model]
	a.gpuMu.Unlock()
	if ok {
		return nil
	}
	// с локальной моделью ответов Ollama выгружает эмбеддинги и реранкер,
	// чтобы поместить её: выгруженная модель -- не ошибка, проверим в другой раз
	if a.LocalInfo != nil {
		if p, err := a.Ollama.Where(ctx, model); err == nil && !p.Loaded {
			return nil
		}
	}
	if _, err := a.Ollama.RequireGPU(ctx, model); err != nil {
		return err
	}
	a.gpuMu.Lock()
	a.gpuOK[model] = true
	a.gpuMu.Unlock()
	return nil
}

func (a *API) loadQuestions() ([]compare.Question, error) {
	qs, err := compare.Load(a.EvalPath)
	if err != nil {
		return nil, err
	}
	books := map[string]*book.Book{}
	for id, info := range a.Searcher.Books() {
		books[id] = info.Book
	}
	return compare.Validate(qs, books), nil
}

func (a *API) questions(w http.ResponseWriter, r *http.Request) {
	if err := a.Searcher.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	qs, err := a.loadQuestions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": qs})
}

type sectionJSON struct {
	N     int    `json:"n"`
	Key   string `json:"key"`
	Label string `json:"label"`
	Title string `json:"title"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

func (a *API) bookByID(w http.ResponseWriter, r *http.Request) (*search.BookInfo, bool) {
	if err := a.Searcher.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return nil, false
	}
	info := a.Searcher.Books()[r.PathValue("book")]
	if info == nil {
		writeError(w, http.StatusNotFound, errors.New("книги нет в индексе"))
		return nil, false
	}
	return info, true
}

func (a *API) book(w http.ResponseWriter, r *http.Request) {
	info, ok := a.bookByID(w, r)
	if !ok {
		return
	}
	b := info.Book
	secs := make([]sectionJSON, len(b.Sections))
	for i, s := range b.Sections {
		secs[i] = sectionJSON{s.N, s.Key, s.Label, s.Title, b.Rune(s.Start), b.Rune(s.End)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"book": bookInfo(b), "sections": secs})
}

func (a *API) sectionOf(w http.ResponseWriter, r *http.Request) (*search.BookInfo, book.Section, bool) {
	info, ok := a.bookByID(w, r)
	if !ok {
		return nil, book.Section{}, false
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n >= len(info.Book.Sections) {
		writeError(w, http.StatusNotFound, errors.New("нет такой главы"))
		return nil, book.Section{}, false
	}
	return info, info.Book.Sections[n], true
}

type paragraphJSON struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// section -- текст главы из локальной копии, абзацами, со смещениями
// в символах от начала книги: по ним интерфейс подсвечивает чанки.
func (a *API) section(w http.ResponseWriter, r *http.Request) {
	info, sec, ok := a.sectionOf(w, r)
	if !ok {
		return
	}
	b := info.Book
	var paras []paragraphJSON
	for _, p := range b.Paragraphs {
		if p.Section == sec.N {
			paras = append(paras, paragraphJSON{b.Rune(p.Start), b.Rune(p.End), b.Text[p.Start:p.End]})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"book":    bookInfo(b),
		"section": sectionJSON{sec.N, sec.Key, sec.Label, sec.Title, b.Rune(sec.Start), b.Rune(sec.End)},
		"heading": b.Text[sec.Start:sec.BodyStart],
		"paras":   paras,
		"total":   len(b.Sections),
	})
}

// sectionChunks -- чанки варианта, которые задевают главу: для разметки
// «как вариант нарезал этот текст».
func (a *API) sectionChunks(w http.ResponseWriter, r *http.Request) {
	info, sec, ok := a.sectionOf(w, r)
	if !ok {
		return
	}
	variant := r.URL.Query().Get("variant")
	var out []search.Hit
	for _, c := range a.Searcher.Chunks(variant) {
		if c.Book == info.Book.ID && c.Start < sec.End && sec.Start < c.End {
			h := search.MakeHit(c, info, c.Ordinal, 0)
			h.Snippet = ""
			out = append(out, h)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chunks": out})
}

// chunk -- один чанк с метаданными: куда вести ссылку «открыть в книге».
func (a *API) chunk(w http.ResponseWriter, r *http.Request) {
	if err := a.Searcher.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	variant, id := r.PathValue("variant"), r.PathValue("id")
	for _, c := range a.Searcher.Chunks(variant) {
		if c.ChunkID == id {
			h := search.MakeHit(c, a.Searcher.Books()[c.Book], c.Ordinal, 0)
			writeJSON(w, http.StatusOK, map[string]any{"chunk": h, "variant": variant})
			return
		}
	}
	writeError(w, http.StatusNotFound, errors.New("чанк не найден — возможно, индекс перестроен"))
}

func (a *API) startIndex(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rebuild bool `json:"rebuild"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req)
	err := a.Jobs.Start(context.WithoutCancel(r.Context()), func(ctx context.Context, emit func(index.Event)) error {
		return a.Index(ctx, req.Rebuild, func(e index.Event) {
			log.Printf("[%s] %s", e.Stage, e.Message)
			emit(e)
		})
	})
	if errors.Is(err, index.ErrBusy) {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, a.Jobs.State())
}

// indexEvents -- события индексации потоком (SSE). ?since=N -- продолжить
// с события N после переподключения.
func (a *API) indexEvents(w http.ResponseWriter, r *http.Request) {
	streamJob(w, r, a.Jobs)
}

// streamJob -- события фоновой работы потоком (SSE).
func streamJob(w http.ResponseWriter, r *http.Request, jobs *index.Jobs) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("поток не поддерживается"))
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	past, ch, cancel := jobs.Subscribe(since)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(e index.Event) {
		raw, _ := json.Marshal(e)
		fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, raw)
	}
	for _, e := range past {
		send(e)
	}
	flusher.Flush()

	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				fmt.Fprint(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			send(e)
			flusher.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func sortedBooks(m map[string]*search.BookInfo) []*search.BookInfo {
	out := make([]*search.BookInfo, 0, len(m))
	for _, info := range m {
		out = append(out, info)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Book.Gutenberg < out[j-1].Book.Gutenberg; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func head(v []float32, n int) []float32 {
	if len(v) < n {
		return v
	}
	return v[:n]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// securityHeaders -- страница без сторонних скриптов и встраиваний. Текст книг
// интерфейс выводит только как текст.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
			"font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// spa раздаёт собранный интерфейс; неизвестные пути -- index.html.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, errors.New("нет такого API"))
			return
		}
		path := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}
