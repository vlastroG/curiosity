// Command docindex -- Twain Expert: беседа о книгах Марка Твена, ответы по
// найденным отрывкам (RAG с переписыванием запроса, фильтром и реранкером)
// и с памятью задачи.
//
//	docindex serve              веб-интерфейс и API (по умолчанию)
//	docindex index [--rebuild]  построить индекс: книги → чанки → эмбеддинги → SQLite
//	docindex chat               беседа в терминале
//	docindex scenario           прогон длинных сценариев (eval/scenarios.json)
//	docindex search "вопрос"    найденные отрывки
//	docindex experiment         подбор порогов и top-K, сравнение режимов
//	docindex stats              что лежит в индексе
//
// Эмбеддинги и реранкер -- Ollama (OLLAMA_URL), только на видеокарте.
// Ответы -- модель RAG_MODEL (OpenRouter или DeepSeek).
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
	"doc-index/internal/chat"
	"doc-index/internal/chunk"
	"doc-index/internal/compare"
	"doc-index/internal/embed"
	"doc-index/internal/experiment"
	"doc-index/internal/httpapi"
	"doc-index/internal/index"
	"doc-index/internal/llm"
	"doc-index/internal/rag"
	"doc-index/internal/rerank"
	"doc-index/internal/retrieve"
	"doc-index/internal/search"
	"doc-index/internal/store"
)

// defaultBooks -- 13 главных книг Твена.
const defaultBooks = "74,76,91,93,1837,86,102,245,3177,3176,119,2895,3186"

// defaultReranker -- Qwen3-Reranker-0.6B в сборке, у которой в Ollama есть
// рабочий выходной слой (у части сборок вероятности всех токенов одинаковы).
const defaultReranker = "B-A-M-N/qwen3-reranker-0.6b-fp16"

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
	case "chat":
		err = chatCmd(ctx)
	case "scenario":
		err = scenarioCmd(ctx, args)
	case "search":
		err = searchCmd(ctx, args)
	case "experiment":
		err = experimentCmd(ctx)
	case "stats":
		err = statsCmd(ctx)
	default:
		err = fmt.Errorf("неизвестная команда %q: serve, index, chat, scenario, search, experiment, stats", cmd)
	}
	if err != nil {
		log.Fatalf("ошибка: %v", err)
	}
}

// app -- всё, что собирается из окружения.
type app struct {
	dataDir  string
	store    *store.Store
	ollama   *embed.Client
	reranker *rerank.Ollama
	variants []index.Variant
	books    []int
	fetcher  *book.Fetcher
	params   chunk.Params
	evalPath string // вопросы для подбора порогов

	settings chat.Settings // настройки нового чата
	tuned    bool          // настройки поиска взяты из подбора experiment
	chats    *chat.Store
	users    []httpapi.User // логины интерфейса; первый -- владелец чатов из CLI
	noRAG    bool           // RAG=off: чат с моделью без поиска по книгам

	llm      rag.LLM
	llmErr   error
	llmModel llm.Model
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
	ollamaURL := env("OLLAMA_URL", "http://localhost:11434")
	a := &app{
		dataDir:  dataDir,
		store:    st,
		ollama:   embed.New(ollamaURL, durationEnv("OLLAMA_TIMEOUT", 5*time.Minute)),
		reranker: rerank.NewOllama(ollamaURL, env("RERANK_MODEL", defaultReranker), time.Minute),
		variants: experiment.Variants(),
		books:    books,
		fetcher: &book.Fetcher{
			Mirror: env("GUTENBERG_MIRROR", "https://gutenberg.pglaf.org"),
			Dir:    filepath.Join(dataDir, "sources"),
			Pause:  2 * time.Second,
			Client: &http.Client{Timeout: 2 * time.Minute},
		},
		params:   chunk.DefaultParams,
		evalPath: env("EVAL_FILE", "../eval/questions.json"),
	}
	if err := a.loadSettings(); err != nil {
		return nil, err
	}
	if a.chats, err = chat.Open(st.DB()); err != nil {
		return nil, err
	}
	a.noRAG = strings.EqualFold(os.Getenv("RAG"), "off")
	a.users, err = httpapi.ParseUsers(os.Getenv("DOCINDEX_USERS"), env("DOCINDEX_USER", "twain"), os.Getenv("DOCINDEX_PASSWORD"))
	if err != nil {
		return nil, err
	}
	// чаты, созданные до разделения по пользователям, -- первому
	if err := a.chats.Adopt(context.Background(), a.owner()); err != nil {
		return nil, err
	}
	m, provider, err := llm.ResolveModel(os.Getenv("RAG_MODEL"), os.Getenv)
	a.llm = rag.LLM{Client: llm.New(durationEnv("LLM_TIMEOUT", 3*time.Minute)), Provider: provider, Model: m.ID,
		MaxTokens: m.Budget()}
	a.llmErr, a.llmModel = err, m
	return a, nil
}

// loadSettings -- настройки по умолчанию: подобранные командой experiment,
// поверх -- переменные окружения.
func (a *app) loadSettings() error {
	s := experiment.Defaults
	rep, ok, err := experiment.Load(filepath.Join(a.dataDir, "experiments", "report.json"))
	if err != nil {
		log.Printf("отчёт подбора не прочитан, беру значения по умолчанию: %v", err)
	} else if ok && rep.Chosen.Validate() == nil {
		s, a.tuned = rep.Chosen, true
	}
	s.Query = env("RETRIEVAL_QUERY", s.Query)
	s.KBefore = intEnv("TOP_K_BEFORE", s.KBefore)
	s.KAfter = intEnv("TOP_K_AFTER", s.KAfter)
	s.SimMin = floatEnv("SIM_MIN", s.SimMin)
	s.RelMin = floatEnv("REL_MIN", s.RelMin)
	s.Order = env("RERANK_ORDER", s.Order)
	cs := chat.Settings{Settings: s, CompressAfter: intEnv("COMPRESS_AFTER", 12)}
	if err := cs.Validate(); err != nil {
		return fmt.Errorf("настройки чата: %w", err)
	}
	a.settings = cs
	return nil
}

func (a *app) indexer() *index.Indexer {
	return &index.Indexer{Store: a.store, Ollama: a.ollama, Fetcher: a.fetcher, Books: a.books,
		Variants: a.variants, Params: a.params, Batch: 32}
}

func (a *app) searcher() *search.Searcher {
	return &search.Searcher{Store: a.store, Embedder: a.ollama, Variants: a.variants}
}

func (a *app) rewriter() *rag.Rewriter {
	return &rag.Rewriter{LLM: a.llm, Path: filepath.Join(a.dataDir, "rewrites.json")}
}

func (a *app) agent(s *search.Searcher) *rag.Agent {
	return &rag.Agent{
		LLM:       a.llm,
		Retriever: &retrieve.Retriever{Searcher: s},
		Variant:   experiment.VariantMain,
		Reranker:  a.reranker,
		Defaults:  a.settings.Settings,
	}
}

// owner -- пользователь, которому достаются чаты из CLI (chat, scenario).
func (a *app) owner() string { return a.users[0].Name }

func (a *app) service(ag *rag.Agent) *chat.Service {
	if a.noRAG {
		return &chat.Service{Store: a.chats, Agent: rag.Plain{LLM: a.llm},
			Compressor: rag.Compressor{LLM: a.llm, System: rag.PlainCompressSystem}}
	}
	return &chat.Service{Store: a.chats, Agent: ag, Compressor: rag.Compressor{LLM: a.llm}}
}

// llmContext -- контекстное окно облачной модели для настроек чата: из каталога
// или у OpenRouter. 0 -- неизвестно (или модель локальная: окно знает llmcli).
func (a *app) llmContext(ctx context.Context) int {
	if a.llmErr != nil || a.llm.Provider.ID != llm.ProviderOpenRouter || a.llmModel.Context > 0 {
		return a.llmModel.Context
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := llm.OpenRouterContext(ctx, a.llmModel.ID)
	if err != nil {
		log.Printf("контекст модели не узнан: %v", err)
	}
	return n
}

// localInfo -- сведения о локальной модели от llmcli; nil, если модель облачная.
func (a *app) localInfo() func(context.Context) (map[string]any, error) {
	if a.llm.Provider.ID != llm.ProviderLocal {
		return nil
	}
	base := a.llm.Provider.BaseURL
	return func(ctx context.Context) (map[string]any, error) { return llm.LocalInfo(ctx, base) }
}

func serve(ctx context.Context) error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	jobs := &index.Jobs{}
	defer jobs.Stop()
	if a.llmErr != nil {
		log.Printf("модель ответов недоступна: %v", a.llmErr)
	}

	s := a.searcher()
	ag := a.agent(s)
	svc := a.service(ag)
	if a.noRAG {
		ag = nil // без поиска API не трогает индекс, эмбеддинги и реранкер
	}
	handler := httpapi.New(httpapi.Config{
		Store: a.store, Searcher: s, Ollama: a.ollama, OllamaURL: a.ollama.URL, Jobs: jobs,
		Index: func(ctx context.Context, rebuild bool, emit func(index.Event)) error {
			_, err := a.indexer().Run(ctx, rebuild, emit)
			return err
		},
		EvalPath: a.evalPath, Params: a.params, StaticDir: env("STATIC_DIR", "./web"),
		Agent: ag, Chats: svc, NoRAG: a.noRAG, Model: a.llm.Model, ChatDefaults: a.settings, LLMBudget: a.llm.MaxTokens,
		LLMError: a.llmErr, Tuned: a.tuned, LLMContext: a.llmContext(ctx), LocalInfo: a.localInfo(),
	})
	// в домашней сети (docker-compose.lan.yml) без пароля не стартуем
	if a.users[0].Password == "" && os.Getenv("DOCINDEX_LAN") != "" {
		return errors.New("доступ из сети без пароля: задайте DOCINDEX_PASSWORD или DOCINDEX_USERS в корневом .env")
	}
	handler = httpapi.Auth(a.users, handler)
	names := make([]string, len(a.users))
	for i, u := range a.users {
		names[i] = u.Name
	}
	log.Printf("пользователи: %s; пароль: %v", strings.Join(names, ", "), a.users[0].Password != "")
	addr := ":" + env("PORT", "8080")
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	st := a.settings
	if a.noRAG {
		log.Printf("слушаю %s; RAG выключен (RAG=off): чат с моделью без поиска; сжатие после %d; ответы: %s",
			addr, st.CompressAfter, a.llm.Model)
	} else {
		log.Printf("слушаю %s; книг %d; эмбеддинги %s, реранкер %s; top-K до %d, косинус ≥ %.2f, реранкер ≥ %.2f, top-K после %d; сжатие после %d; ответы: %s",
			addr, len(a.books), experiment.Model, a.reranker.Model, st.KBefore, st.SimMin, st.RelMin, st.KAfter, st.CompressAfter, a.llm.Model)
	}
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

func orError(a rag.Answer) string {
	if a.Error != "" {
		return "ошибка: " + a.Error
	}
	return a.Text
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
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
	r := &retrieve.Retriever{Searcher: a.searcher()}
	res, err := r.Retrieve(ctx, retrieve.Config{Variant: experiment.VariantMain, Query: retrieve.QueryRaw}, query,
		retrieve.Rewrite{}, "", *k)
	if err != nil {
		return err
	}
	fmt.Printf("Вопрос: %s\n\n", query)
	for _, h := range res.Hits {
		fmt.Printf("  %d. cos %.3f  %s · %s\n     %s\n", h.Rank, h.Cosine, h.BookTitle, h.Section, h.Snippet)
	}
	return nil
}

func experimentCmd(ctx context.Context) error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	if a.llmErr != nil {
		return a.llmErr
	}
	questions, err := compare.Load(a.evalPath)
	if err != nil {
		return err
	}
	r := &experiment.Runner{Dir: filepath.Join(a.dataDir, "experiments"),
		Retriever: &retrieve.Retriever{Searcher: a.searcher()}, Reranker: a.reranker, Rewriter: a.rewriter(),
		Log: log.Printf}
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

func intEnv(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

func floatEnv(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
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
