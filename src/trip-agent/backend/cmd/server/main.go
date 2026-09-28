// Command server -- Trip Planner: веб-интерфейс и агент-оркестратор, который
// собирает план поездки из инструментов нескольких MCP-серверов.
//
//	браузер ──► /api/runs (форма) ──► очередь ──► агент ──► модель
//	   ▲                                            │
//	   └──── SSE: ход работы ◄──── события ◄────────┴──► places / weather / money / trip
//
// Ключи провайдеров читает только этот процесс, в браузер они не попадают.
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

	"trip-agent/internal/agent"
	"trip-agent/internal/httpapi"
	"trip-agent/internal/llm"
	"trip-agent/internal/registry"
	"trip-agent/internal/runs"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("trip-agent ")
	if err := run(); err != nil {
		log.Fatalf("остановка: %v", err)
	}
}

func run() error {
	port := env("PORT", "8080")
	dataDir := env("DATA_DIR", "./data")
	outDir := env("OUT_DIR", filepath.Join(dataDir, "out"))
	staticDir := env("STATIC_DIR", "./web")

	model, provider, err := agent.ResolveModel(os.Getenv("TRIP_MODEL"), os.Getenv)
	if err != nil {
		return err
	}
	log.Printf("модель: %s (%s)", model.ID, provider.Title)

	servers, err := registry.Parse(env("TRIP_SERVERS",
		"places=http://localhost:8781/mcp,weather=http://localhost:8782/mcp/travel,money=http://localhost:8783/mcp,trip=http://localhost:8784/mcp"))
	if err != nil {
		return err
	}
	reg := registry.New(servers, durationEnv("MCP_TIMEOUT", 60*time.Second))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg.Discover(ctx)
	for _, s := range reg.Servers() {
		if s.Available {
			log.Printf("сервер %s (%s): %v", s.Name, s.Endpoint, s.Tools)
		} else {
			log.Printf("сервер %s (%s) недоступен: %s", s.Name, s.Endpoint, s.Error)
		}
	}

	a := &agent.Agent{
		Chat:     llm.New(durationEnv("LLM_TIMEOUT", 3*time.Minute)),
		Provider: provider,
		Model:    model.ID,
		Tools:    reg,
		Now:      time.Now,
	}
	manager, err := runs.New(filepath.Join(dataDir, "runs"), a, reg, time.Now, durationEnv("RUN_TIMEOUT", 15*time.Minute))
	if err != nil {
		return err
	}
	go manager.Work(ctx)

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           httpapi.New(manager, reg, model, outDir, staticDir, time.Now),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Printf("слушаю :%s", port)
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
	if parsed, err := time.ParseDuration(value); err == nil {
		return parsed
	}
	log.Printf("не понял %s=%q, беру %s", name, value, fallback)
	return fallback
}
