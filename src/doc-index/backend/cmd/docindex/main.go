// Command docindex -- Twain Expert: индекс книг Марка Твена и ответы на
// вопросы о них в двух режимах -- по памяти модели и с RAG.
//
//	docindex serve              веб-интерфейс и API (по умолчанию)
//	docindex index [--rebuild]  построить индекс: книги → чанки → эмбеддинги → SQLite
//	docindex ask "вопрос"       ответ без RAG и с RAG из терминала
//	docindex search "вопрос"    найденные отрывки
//	docindex rag-eval [--force] прогон контрольных вопросов с оценкой судьи
//	docindex experiment         сравнение стратегий поиска на 44 вопросах
//	docindex stats              что лежит в индексе
//
// Эмбеддинги считает Ollama (OLLAMA_URL), только на видеокарте. Ответы --
// модель RAG_MODEL (OpenRouter или DeepSeek).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/chunk"
	"doc-index/internal/compare"
	"doc-index/internal/embed"
	"doc-index/internal/experiment"
	"doc-index/internal/httpapi"
	"doc-index/internal/index"
	"doc-index/internal/llm"
	"doc-index/internal/rag"
	"doc-index/internal/retrieve"
	"doc-index/internal/search"
	"doc-index/internal/store"
)

// Стратегия поиска по умолчанию -- победитель experiment (см. README).
// Любую часть можно переопределить переменными окружения.
const (
	defaultEmbedModel = "qwen3-embedding:4b"
	defaultQuery      = retrieve.QueryHyDE
	defaultHybrid     = false
	defaultSmallToBig = false
)

// defaultBooks -- 13 главных книг Твена.
const defaultBooks = "74,76,91,93,1837,86,102,245,3177,3176,119,2895,3186"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("twain ")

	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx)
	case "index":
		err = indexCmd(ctx, args)
	case "ask":
		err = askCmd(ctx, args)
	case "search":
		err = searchCmd(ctx, args)
	case "rag-eval":
		err = ragEvalCmd(ctx, args)
	case "experiment":
		err = experimentCmd(ctx)
	case "stats":
		err = statsCmd(ctx)
	default:
		err = fmt.Errorf("неизвестная команда %q: serve, index, ask, search, rag-eval, experiment, stats", cmd)
	}
	if err != nil {
		log.Fatalf("ошибка: %v", err)
	}
}

// app -- всё, что собирается из окружения.
type app struct {
	dataDir   string
	store     *store.Store
	ollama    *embed.Client
	variants  []index.Variant
	retrieval retrieve.Config
	books     []int
	fetcher   *book.Fetcher
	params    chunk.Params

	evalPath     string // 44 вопроса для выбора стратегии поиска
	controlsPath string // 10 контрольных вопросов RAG

	llm    rag.LLM
	llmErr error
}

func setup() (*app, error) {
	dataDir := env("DATA_DIR", "./data")
	st, err := store.Open(filepath.Join(dataDir, "index.db"))
	if err != nil {
		return nil, err
	}
	books, err := parseBooks(env("BOOKS", defaultBooks))
	if err != nil {
		return nil, err
	}
	model := env("EMBED_MODEL", defaultEmbedModel)
	small := boolEnv("SMALL_TO_BIG", defaultSmallToBig)
	cfg := retrieve.Config{Variant: experiment.VariantMain, Query: env("RETRIEVAL_QUERY", defaultQuery),
		Hybrid: boolEnv("RETRIEVAL_HYBRID", defaultHybrid)}
	if small {
		cfg.Variant, cfg.Parent = experiment.VariantSmall, experiment.VariantMain
	}
	cfg.ID = experiment.ConfigID(model, cfg)
	cfg.Title = retrievalTitle(cfg)

	a := &app{
		dataDir:   dataDir,
		store:     st,
		ollama:    embed.New(env("OLLAMA_URL", "http://localhost:11434"), durationEnv("OLLAMA_TIMEOUT", 5*time.Minute)),
		variants:  experiment.Variants(model, small),
		retrieval: cfg,
		books:     books,
		fetcher: &book.Fetcher{
			Mirror: env("GUTENBERG_MIRROR", "https://gutenberg.pglaf.org"),
			Dir:    filepath.Join(dataDir, "sources"),
			Pause:  2 * time.Second,
			Client: &http.Client{Timeout: 2 * time.Minute},
		},
		params:       chunk.DefaultParams,
		evalPath:     env("EVAL_FILE", "../eval/questions.json"),
		controlsPath: env("RAG_EVAL_FILE", "../eval/rag.json"),
	}
	m, provider, err := llm.ResolveModel(os.Getenv("RAG_MODEL"), os.Getenv)
	a.llm = rag.LLM{Client: llm.New(durationEnv("LLM_TIMEOUT", 3*time.Minute)), Provider: provider, Model: m.ID,
		MaxTokens: m.Budget()}
	a.llmErr = err
	return a, nil
}

func retrievalTitle(c retrieve.Config) string {
	t := map[string]string{
		retrieve.QueryRaw: "вопрос как есть", retrieve.QueryEnglish: "перевод на английский",
		retrieve.QueryHyDE: "HyDE", retrieve.QueryFuse: "вопрос + перевод",
	}[c.Query]
	if c.Hybrid {
		t += " + BM25"
	}
	if c.Parent != "" {
		t = "small-to-big · " + t
	}
	return t
}

func (a *app) indexer() *index.Indexer {
	return &index.Indexer{Store: a.store, Ollama: a.ollama, Fetcher: a.fetcher, Books: a.books,
		Variants: a.variants, Params: a.params, Batch: 32}
}

func (a *app) searcher() *search.Searcher {
	return &search.Searcher{Store: a.store, Embedder: a.ollama, Variants: a.variants}
}

func (a *app) agent(s *search.Searcher) *rag.Agent {
	return &rag.Agent{
		LLM:       a.llm,
		Rewriter:  &rag.Rewriter{LLM: a.llm, Path: filepath.Join(a.dataDir, "rewrites.json")},
		Retriever: &retrieve.Retriever{Searcher: s},
		Config:    a.retrieval,
		K:         intEnv("RAG_TOP_K", 5),
	}
}

func serve(ctx context.Context) error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	jobs, evalJobs := &index.Jobs{}, &index.Jobs{}
	defer jobs.Stop()
	defer evalJobs.Stop()
	if a.llmErr != nil {
		log.Printf("модель ответов недоступна: %v", a.llmErr)
	}

	s := a.searcher()
	handler := httpapi.New(httpapi.Config{
		Store: a.store, Searcher: s, Ollama: a.ollama, OllamaURL: a.ollama.URL, Jobs: jobs,
		Index: func(ctx context.Context, rebuild bool, emit func(index.Event)) error {
			_, err := a.indexer().Run(ctx, rebuild, emit)
			return err
		},
		EvalPath: a.evalPath, Params: a.params, StaticDir: env("STATIC_DIR", "./web"),
		Agent: a.agent(s), LLMError: a.llmErr, EvalJobs: evalJobs,
		ControlsPath:   a.controlsPath,
		RagEvalPath:    filepath.Join(a.dataDir, "rag-eval.json"),
		ExperimentPath: filepath.Join(a.dataDir, "experiments", "report.json"),
	})
	addr := ":" + env("PORT", "8080")
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("слушаю %s; Ollama %s; книг %d; поиск: %s (%s); модель ответов: %s",
		addr, a.ollama.URL, len(a.books), a.retrieval.ID, a.retrieval.Title, a.llm.Model)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func indexCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	rebuild := fs.Bool("rebuild", false, "пересчитать всё заново")
	books := fs.String("books", "", "номера книг Gutenberg через запятую (по умолчанию BOOKS)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	if *books != "" {
		if a.books, err = parseBooks(*books); err != nil {
			return err
		}
	}
	_, err = a.indexer().Run(ctx, *rebuild, func(e index.Event) {
		log.Printf("[%s] %s", e.Stage, e.Message)
	})
	return err
}

func askCmd(ctx context.Context, args []string) error {
	question := strings.TrimSpace(strings.Join(args, " "))
	if question == "" {
		return errors.New(`нужен вопрос: docindex ask "кто покрасил забор вместо Тома"`)
	}
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	if a.llmErr != nil {
		return a.llmErr
	}
	res := a.agent(a.searcher()).Ask(ctx, question)
	fmt.Printf("Вопрос: %s\nМодель: %s\n", question, res.Model)
	fmt.Printf("\n── Без RAG (%.1f с)\n%s\n", res.NoRAG.Ms/1000, orError(res.NoRAG))
	fmt.Printf("\n── С RAG (%.1f с; поиск: %s)\n", res.RAG.Ms/1000, strings.Join(res.RAG.Queries, " | "))
	fmt.Println(orError(res.RAG.Answer))
	for _, s := range res.RAG.Sources {
		fmt.Printf("  [%d] %.3f %s · %s\n", s.N, s.Score, s.BookTitle, s.Section)
	}
	return nil
}

func orError(a rag.Answer) string {
	if a.Error != "" {
		return "ошибка: " + a.Error
	}
	return a.Text
}

func searchCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	k := fs.Int("k", 5, "сколько отрывков показать")
	if err := fs.Parse(args); err != nil {
		return err
	}
	query := strings.Join(fs.Args(), " ")
	if query == "" {
		return errors.New(`нужен вопрос: docindex search "как Том покрасил забор"`)
	}
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	ag := a.agent(a.searcher())
	var rw retrieve.Rewrite
	if a.retrieval.NeedsRewrite() {
		if a.llmErr != nil {
			return a.llmErr
		}
		if rw, _, err = ag.Rewriter.Rewrite(ctx, query); err != nil {
			return err
		}
	}
	res, err := ag.Retriever.Retrieve(ctx, a.retrieval, query, rw, "", *k)
	if err != nil {
		return err
	}
	fmt.Printf("Вопрос: %s\nПоиск (%s): %s\n\n", query, a.retrieval.Title, strings.Join(res.Queries, " | "))
	for _, h := range res.Hits {
		fmt.Printf("  %d. %.3f  %s · %s\n     %s\n", h.Rank, h.Score, h.BookTitle, h.Section, h.Snippet)
	}
	return nil
}

func ragEvalCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rag-eval", flag.ContinueOnError)
	force := fs.Bool("force", false, "пересчитать и уже оценённые вопросы")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	if a.llmErr != nil {
		return a.llmErr
	}
	s := a.searcher()
	if err := s.Refresh(ctx); err != nil {
		return err
	}
	controls, err := rag.LoadControls(a.controlsPath)
	if err != nil {
		return err
	}
	books := map[string]*book.Book{}
	for id, info := range s.Books() {
		books[id] = info.Book
	}
	controls = rag.ValidateControls(controls, books)
	for _, c := range controls {
		if !c.Valid {
			log.Printf("[rag-eval] %s пропущен: %s", c.ID, c.Problem)
		}
	}
	rep, err := a.agent(s).Evaluate(ctx, controls, filepath.Join(a.dataDir, "rag-eval.json"), *force,
		func(i int, row rag.EvalRow, skipped bool) {
			if skipped {
				log.Printf("[rag-eval] %d/%d %s: уже оценён", i+1, len(controls), row.Control.ID)
				return
			}
			src := "—"
			if row.SourcesHit != nil {
				src = map[bool]string{true: "да", false: "нет"}[*row.SourcesHit]
			}
			log.Printf("[rag-eval] %d/%d %s: без RAG — %s, с RAG — %s, источники в контексте — %s %s",
				i+1, len(controls), row.Control.ID, row.NoRAG.Verdict, row.RAG.Verdict, src, row.JudgeError)
		})
	if err != nil {
		return err
	}
	log.Printf("[rag-eval] модель %s, поиск %s", rep.Model, rep.Config)
	log.Printf("[rag-eval] без RAG: верно %d, частично %d, неверно %d", rep.NoRAG.Correct, rep.NoRAG.Partial, rep.NoRAG.Wrong)
	log.Printf("[rag-eval] с RAG:   верно %d, частично %d, неверно %d", rep.RAG.Correct, rep.RAG.Partial, rep.RAG.Wrong)
	log.Printf("[rag-eval] нужные источники в контексте: %d из %d", rep.SourcesHit, rep.WithSource)
	return nil
}

func experimentCmd(ctx context.Context) error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	questions, err := compare.Load(a.evalPath)
	if err != nil {
		return err
	}
	var rw *rag.Rewriter
	if a.llmErr == nil {
		rw = &rag.Rewriter{LLM: a.llm, Path: filepath.Join(a.dataDir, "rewrites.json")}
	} else {
		log.Printf("[experiment] без переписывания вопросов: %v", a.llmErr)
	}
	models := strings.Split(env("EXPERIMENT_MODELS", "bge-m3,qwen3-embedding:0.6b,qwen3-embedding:4b"), ",")
	r := &experiment.Runner{Dir: filepath.Join(a.dataDir, "experiments"), Ollama: a.ollama, Fetcher: a.fetcher,
		Books: a.books, Params: a.params, Models: models, Rewriter: rw, Log: log.Printf}
	_, err = r.Run(ctx, questions)
	return err
}

func statsCmd(ctx context.Context) error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	states, err := a.store.States(ctx)
	if err != nil {
		return err
	}
	if len(states) == 0 {
		fmt.Println("индекс пуст: docindex index")
		return nil
	}
	fmt.Printf("%-12s %-14s %-22s %6s %8s %6s %8s\n", "вариант", "книга", "модель", "чанков", "токенов", "dims", "секунд")
	for _, st := range states {
		fmt.Printf("%-12s %-14s %-22s %6d %8d %6d %8.1f\n", st.Variant, st.Book, st.Model, st.Chunks, st.Tokens,
			st.Dims, st.TotalSeconds)
	}
	return nil
}

func parseBooks(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("номер книги Gutenberg: %q", part)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, errors.New("не задано ни одной книги (BOOKS)")
	}
	return out, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func boolEnv(key string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func intEnv(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

func durationEnv(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil && d > 0 {
		return d
	}
	return def
}
