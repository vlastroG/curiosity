// Command mcp-trip -- MCP-сервер, который собирает план поездки по частям и
// публикует его в HTML.
//
//	trip_create ──► trip_set_budget ──► trip_add_day × N ──► trip_publish ──► out/*.html
//	trip_get    ──► план целиком (для интерфейса)
//
// Планы -- JSON-файлы в DATA_DIR/trips, HTML -- в DATA_DIR/out. Транспорт --
// Streamable HTTP без сессий и без SSE.
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
	log.SetPrefix("mcp-trip ")
	if err := run(); err != nil {
		log.Fatalf("остановка: %v", err)
	}
}

func run() error {
	port := env("PORT", "8080")

	dataDir := env("DATA_DIR", "./data")
	store, err := newStore(filepath.Join(dataDir, "trips"))
	if err != nil {
		return err
	}
	server := newServer(store, env("OUT_DIR", filepath.Join(dataDir, "out")))

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	httpServer := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Printf("слушаю :%s, MCP на /mcp", port)
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
