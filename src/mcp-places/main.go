// Command mcp-places -- MCP-сервер «где и что посмотреть»: находит город,
// достопримечательности рядом и их описания.
//
//	find_city  ──► Open-Meteo Geocoding ──► координаты, страна, часовой пояс
//	sights     ──► Википедия geosearch  ──► статьи о местах рядом с точкой
//	sight_info ──► Википедия extracts   ──► описание и ссылка
//
// Ключей не нужно. Транспорт -- Streamable HTTP без сессий и без SSE: любой
// вызов воспроизводится одним curl.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version -- версия сервера, её клиент видит в ответе на initialize.
const version = "1.0.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("mcp-places ")
	if err := run(); err != nil {
		log.Fatalf("остановка: %v", err)
	}
}

func run() error {
	port := env("PORT", "8080")
	timeout := durationEnv("API_TIMEOUT", 15*time.Second)

	server := newServer(&Places{
		client:     &http.Client{Timeout: timeout},
		geocodeURL: env("GEOCODE_URL", "https://geocoding-api.open-meteo.com/v1/search"),
		wikiURL:    env("WIKI_URL", "https://%s.wikipedia.org/w/api.php"),
	})

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
