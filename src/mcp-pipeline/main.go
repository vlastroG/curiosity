// Command mcp-pipeline -- MCP-сервер, из инструментов которого складывается
// конвейер «найти на Hacker News → законспектировать по-русски → сохранить в HTML».
//
//	hn_search ──► артефакт выдачи ──► summarize ──► артефакт конспекта ──► save_to_file ──► out/*.html
//
// Сервер не решает, в каком порядке звать инструменты, -- это делает клиент
// (агент с моделью). Сервер отвечает за то, чтобы данные между шагами дошли
// целыми: шаги связаны id артефактов, каждый артефакт хранит sha256 содержимого.
//
// Транспорт -- Streamable HTTP без сессий и без SSE: любой вызов воспроизводится
// одним curl, перезапуск контейнера клиентам не мешает.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version -- версия сервера, её клиент видит в ответе на initialize.
const version = "1.0.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("mcp-pipeline ")

	if err := run(); err != nil {
		log.Fatalf("остановка: %v", err)
	}
}

func run() error {
	port := env("PORT", "8080")
	dataDir := env("DATA_DIR", "./data")
	outDir := env("OUT_DIR", filepath.Join(dataDir, "out"))
	hnBase := env("HN_API_URL", "https://hn.algolia.com")
	hnTimeout := durationEnv("HN_TIMEOUT", 20*time.Second)
	// щедро: бесплатная рассуждающая модель на длинной выдаче думает минуту и дольше
	llmTimeout := durationEnv("LLM_TIMEOUT", 4*time.Minute)

	model, provider, err := resolveModel(os.Getenv("PIPELINE_MODEL"), os.Getenv, "mcp-pipeline")
	if err != nil {
		return err
	}
	log.Printf("модель конспектов: %s (%s)", model.ID, provider.Title)

	artifacts, err := newArtifacts(filepath.Join(dataDir, "artifacts"))
	if err != nil {
		return err
	}
	output, err := newOutput(outDir)
	if err != nil {
		return err
	}

	pipeline := &Pipeline{
		hn:         &HN{client: &http.Client{Timeout: hnTimeout}, base: hnBase},
		artifacts:  artifacts,
		summarizer: &Summarizer{chat: newChatClient(llmTimeout), provider: provider, model: model.ID},
		output:     output,
		now:        time.Now,
	}
	server := newServer(pipeline)

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Printf("слушаю :%s, MCP на /mcp, данные %s, файлы %s", port, dataDir, outDir)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Print("получен сигнал, закрываюсь")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// durationEnv читает длительность: принимает и число секунд, и запись вида "90s".
func durationEnv(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		log.Printf("не понял %s=%q, беру %s", name, value, fallback)
		return fallback
	}
	return parsed
}
