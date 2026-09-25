// Command mcp-weather -- MCP-сервер, который отдаёт погоду в месте работ.
//
// MCP -- это JSON-RPC 2.0 поверх транспорта. Транспорт здесь Streamable HTTP:
// сервер слушает порт, клиент шлёт POST на /mcp. Обмен выглядит так:
//
//	клиент                                      mcp-weather        Open-Meteo
//	   │  initialize (кто я, что умею) ────────────►│
//	   │◄──────────── имя, версия, возможности      │
//	   │  tools/list ──────────────────────────────►│
//	   │◄─── [find_place, get_forecast] со схемами  │
//	   │  tools/call get_forecast{place:"Москва"} ─►│
//	   │                                            │  геокодер ──►│
//	   │                                            │◄── координаты│
//	   │                                            │  прогноз ───►│
//	   │                                            │◄──── погода  │
//	   │◄─── текст для модели + structuredContent   │
//
// Сервер поднят без сессий (Stateless) и без SSE (JSONResponse): каждый запрос
// самодостаточен. Отсюда два следствия -- перезапуск контейнера ничего не ломает
// у подключённых клиентов, и любой вызов воспроизводится одним curl.
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
const version = "0.1.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("mcp-weather ")

	if err := run(); err != nil {
		log.Fatalf("остановка: %v", err)
	}
}

func run() error {
	port := env("PORT", "8080")
	// потолок на поход к Open-Meteo. Короче общего таймаута вызывающего:
	// инструмент, который думает минуту, хуже инструмента, который честно сдался
	apiTimeout := durationEnv("API_TIMEOUT", 15*time.Second)

	server := newServer(&http.Client{Timeout: apiTimeout})

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	// health отдельно от протокола: docker-compose должен уметь спросить
	// «ты живой?», не зная ничего про MCP
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
		log.Printf("слушаю :%s, MCP на /mcp, таймаут запроса к Open-Meteo %s", port, apiTimeout)
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

// durationEnv читает таймаут: принимает и число секунд, и запись вида "90s".
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
