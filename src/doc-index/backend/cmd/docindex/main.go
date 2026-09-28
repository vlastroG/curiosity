// Command docindex -- индекс книг с эмбеддингами и поиск по нему.
//
//	docindex serve              веб-интерфейс и API (по умолчанию)
//	docindex index [--rebuild]  построить индекс: книги → чанки → эмбеддинги → SQLite
//	docindex search "вопрос"    поиск из терминала по всем вариантам
//	docindex stats              что лежит в индексе
//
// Эмбеддинги считает Ollama (OLLAMA_URL), только на видеокарте.
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
	"doc-index/internal/embed"
	"doc-index/internal/httpapi"
	"doc-index/internal/index"
	"doc-index/internal/search"
	"doc-index/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("doc-index ")

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
	case "search":
		err = searchCmd(ctx, args)
	case "stats":
		err = statsCmd(ctx)
	default:
		err = fmt.Errorf("неизвестная команда %q: serve, index, search, stats", cmd)
	}
	if err != nil {
		log.Fatalf("ошибка: %v", err)
	}
}

// app -- всё, что собирается из окружения.
type app struct {
	store    *store.Store
	ollama   *embed.Client
	variants []index.Variant
	books    []int
	fetcher  *book.Fetcher
	params   chunk.Params
	evalPath string
}

func setup() (*app, error) {
	dataDir := env("DATA_DIR", "./data")
	st, err := store.Open(filepath.Join(dataDir, "index.db"))
	if err != nil {
		return nil, err
	}
	books, err := parseBooks(env("BOOKS", "74,76"))
	if err != nil {
		return nil, err
	}
	return &app{
		store:    st,
		ollama:   embed.New(env("OLLAMA_URL", "http://localhost:11434"), durationEnv("OLLAMA_TIMEOUT", 5*time.Minute)),
		variants: index.Variants(env("EMBED_MODEL", "bge-m3"), env("COMPARE_MODEL", "nomic-embed-text")),
		books:    books,
		fetcher: &book.Fetcher{
			Mirror: env("GUTENBERG_MIRROR", "https://gutenberg.pglaf.org"),
			Dir:    filepath.Join(dataDir, "sources"),
			Pause:  2 * time.Second,
			Client: &http.Client{Timeout: 2 * time.Minute},
		},
		params:   chunk.DefaultParams,
		evalPath: env("EVAL_FILE", "../eval/questions.json"),
	}, nil
}

func (a *app) indexer() *index.Indexer {
	return &index.Indexer{Store: a.store, Ollama: a.ollama, Fetcher: a.fetcher, Books: a.books,
		Variants: a.variants, Params: a.params, Batch: 32}
}

func (a *app) searcher() *search.Searcher {
	return &search.Searcher{Store: a.store, Embedder: a.ollama, Variants: a.variants}
}

func serve(ctx context.Context) error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	jobs := &index.Jobs{}
	defer jobs.Stop()

	handler := httpapi.New(httpapi.Config{
		Store: a.store, Searcher: a.searcher(), Ollama: a.ollama, OllamaURL: a.ollama.URL, Jobs: jobs,
		Index: func(ctx context.Context, rebuild bool, emit func(index.Event)) error {
			_, err := a.indexer().Run(ctx, rebuild, emit)
			return err
		},
		EvalPath: a.evalPath, Params: a.params, StaticDir: env("STATIC_DIR", "./web"),
	})
	addr := ":" + env("PORT", "8080")
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	names := make([]string, len(a.variants))
	for i, v := range a.variants {
		names[i] = v.ID + " (" + v.Model + ")"
	}
	log.Printf("слушаю %s; Ollama %s; книги %v; варианты: %s", addr, a.ollama.URL, a.books, strings.Join(names, ", "))
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

func searchCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	k := fs.Int("k", 3, "сколько мест показать")
	variants := fs.String("variants", "", "варианты через запятую (по умолчанию все)")
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
	ids := strings.Split(*variants, ",")
	if *variants == "" {
		ids = nil
		for _, v := range a.variants {
			ids = append(ids, v.ID)
		}
	}
	resp, err := a.searcher().Search(ctx, query, ids, "", *k)
	if err != nil {
		return err
	}
	fmt.Printf("Вопрос: %s\n", query)
	for _, id := range ids {
		v, _ := index.Find(a.variants, id)
		fmt.Printf("\n── %s (%s)\n", v.Title, v.Model)
		for _, h := range resp.Results[id] {
			fmt.Printf("  %d. %.3f  %s · %s\n     %s\n", h.Rank, h.Score, h.BookTitle, h.Section, h.Snippet)
		}
	}
	return nil
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
	fmt.Printf("%-18s %-6s %-18s %6s %8s %6s %8s\n", "вариант", "книга", "модель", "чанков", "токенов", "dims", "секунд")
	for _, st := range states {
		fmt.Printf("%-18s %-6s %-18s %6d %8d %6d %8.1f\n", st.Variant, st.Book, st.Model, st.Chunks, st.Tokens,
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

func durationEnv(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil && d > 0 {
		return d
	}
	return def
}
