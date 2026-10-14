// Command server -- ассистент поддержки: веб-интерфейс, индекс документации
// продукта, защита Jev, модель ответов и тикеты через MCP.
//
// Индекс строится в фоне при старте, если файлы документации поменялись;
// до готовности вопросы получают 503, а интерфейс показывает ход индексации.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"support-desk/internal/assistant"
	"support-desk/internal/docs"
	"support-desk/internal/embed"
	"support-desk/internal/guard"
	"support-desk/internal/httpapi"
	"support-desk/internal/llm"
	"support-desk/internal/tickets"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("support-desk ")
	if err := run(); err != nil {
		log.Fatalf("остановка: %v", err)
	}
}

func run() error {
	port := env("PORT", "8080")
	dataDir := env("DATA_DIR", "/data")
	product := env("SUPPORT_PRODUCT", "pyRevit")
	embedModel := env("SUPPORT_EMBED_MODEL", "bge-m3")
	opts := docs.Options{
		Root:    env("DOCS_PATH", "/docs"),
		Include: list(env("SUPPORT_DOCS_INCLUDE", ".md,.rst,.txt,.py,.yaml")),
		Exclude: list(env("SUPPORT_DOCS_EXCLUDE", ".git,node_modules,site-packages,dev,bin,release")),
		Model:   embedModel,
	}

	model, provider, err := llm.ResolveModel(os.Getenv("SUPPORT_MODEL"), os.Getenv)
	if err != nil {
		return err
	}
	g := &guard.Guard{
		URL:          env("TYPESAFE_URL", "https://api.typesafe.ai"),
		APIKey:       os.Getenv("TYPESAFE_API_KEY"),
		Model:        env("SUPPORT_GUARD_MODEL", "jev-latest"),
		Product:      product,
		InjectionMax: number("SUPPORT_GUARD_INJECTION", 0.7),
		TopicMin:     number("SUPPORT_GUARD_TOPIC", 0.3),
	}
	if g.Enabled() {
		log.Printf("защита Jev: включена (%s)", g.Model)
	} else {
		log.Print("защита Jev: выключена — нет TYPESAFE_API_KEY")
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	index, err := docs.Open(filepath.Join(dataDir, "index.db"))
	if err != nil {
		return err
	}
	defer index.Close()

	embedder := embed.New(env("OLLAMA_URL", "http://ollama:11434"), 5*time.Minute)
	ticketsClient := tickets.New(env("TICKETS_URL", "http://mcp-tickets:8080/mcp"), 30*time.Second)
	chat := llm.New(3 * time.Minute)

	a := &assistant.Assistant{
		Search:  searcher{index: index, embed: embedder, model: embedModel},
		Guard:   g,
		Tickets: ticketsClient,
		LLM:     chatter{client: chat, provider: provider, model: model.ID},
		Model:   model,
		Product: product,
	}

	st := &indexState{s: httpapi.IndexStatus{State: "indexing"}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go buildIndex(ctx, index, embedder, opts, st)

	handler := httpapi.New(httpapi.Config{
		Ask:     a.Ask,
		Tickets: ticketsClient,
		Ready:   func() bool { return st.get().State == "ready" },
		Status: func() httpapi.Status {
			return httpapi.Status{Product: product, Model: model.ID,
				Guard: httpapi.GuardStatus{Enabled: g.Enabled(), Model: g.Model}, Index: st.get()}
		},
		StaticDir: os.Getenv("STATIC_DIR"),
	})
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() {
		log.Printf("слушаю :%s; продукт %s, модель %s, документация %s", port, product, model.ID, opts.Root)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Print("получен сигнал, закрываюсь")
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

// buildIndex скачивает модель эмбеддингов, если её нет, и приводит индекс в порядок.
func buildIndex(ctx context.Context, index *docs.Index, e *embed.Client, opts docs.Options, st *indexState) {
	fail := func(err error) {
		log.Printf("индекс не построен: %v", err)
		st.set(httpapi.IndexStatus{State: "error", Error: err.Error()})
	}
	has, err := e.Has(ctx, opts.Model)
	if err != nil {
		fail(err)
		return
	}
	if !has {
		log.Printf("скачиваю модель эмбеддингов %s", opts.Model)
		if err := e.Pull(ctx, opts.Model, nil); err != nil {
			fail(fmt.Errorf("модель %s не скачалась: %w", opts.Model, err))
			return
		}
	}
	started := time.Now()
	rebuilt, err := index.Ensure(ctx, opts, e, func(done, total int) {
		st.set(httpapi.IndexStatus{State: "indexing", Done: done, Total: total})
	})
	if err != nil {
		fail(err)
		return
	}
	files, chunks := index.Stats()
	if rebuilt {
		log.Printf("индекс построен за %s: %d файлов, %d фрагментов", time.Since(started).Round(time.Second), files, chunks)
	} else {
		log.Printf("индекс актуален: %d файлов, %d фрагментов", files, chunks)
	}
	st.set(httpapi.IndexStatus{State: "ready", Done: chunks, Total: chunks, Files: files, Chunks: chunks})
}

type indexState struct {
	mu sync.Mutex
	s  httpapi.IndexStatus
}

func (x *indexState) get() httpapi.IndexStatus {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.s
}

func (x *indexState) set(s httpapi.IndexStatus) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.s = s
}

// searcher -- индекс с клиентом эмбеддингов, в форме assistant.Searcher.
type searcher struct {
	index *docs.Index
	embed *embed.Client
	model string
}

func (s searcher) Search(ctx context.Context, query string, k int) ([]docs.Hit, error) {
	return s.index.Search(ctx, s.embed, s.model, query, k)
}

// chatter -- клиент модели с провайдером, в форме assistant.Chatter.
type chatter struct {
	client   *llm.Client
	provider llm.Provider
	model    string
}

func (c chatter) Chat(ctx context.Context, req llm.Request) (llm.Response, error) {
	req.Model = c.model
	return c.client.Chat(ctx, c.provider, req)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func number(name string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return v
	}
	return fallback
}

func list(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
