// Command server -- Crypto Watch: агент, который работает 24/7 и раз в N минут
// выдаёт прогноз по криптовалютам, и веб-интерфейс к нему.
//
//	браузер ◄── /api ──► этот процесс ──(каждые N мин)──► модель ◄── tool calls ──┐
//	                         │                                                       │
//	                         └──────── MCP: summary / series / schedule / forecasts ─► mcp-crypto
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
	"strconv"
	"syscall"
	"time"

	"crypto-watch/internal/agent"
	"crypto-watch/internal/httpapi"
	"crypto-watch/internal/llm"
	"crypto-watch/internal/mcpc"
	"crypto-watch/internal/watch"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("crypto-watch ")

	if err := run(); err != nil {
		log.Fatalf("остановка: %v", err)
	}
}

func run() error {
	port := env("PORT", "8080")
	staticDir := env("STATIC_DIR", "./web")
	dataDir := env("DATA_DIR", "./data")
	mcpURL := env("MCP_CRYPTO_URL", "http://localhost:8766/mcp")
	appURL := env("APP_URL", "http://localhost:5175")
	horizon := intEnv("HORIZON_MINUTES", 30)
	firstDelay := durationEnv("FIRST_DELAY", 20*time.Second)
	// щедро: бесплатная рассуждающая модель в очереди молчит десятки секунд
	llmTimeout := durationEnv("LLM_TIMEOUT", 3*time.Minute)

	providers := map[string]llm.Provider{
		llm.ProviderDeepSeek:   llm.DeepSeek(os.Getenv("DEEPSEEK_API_KEY")),
		llm.ProviderOpenRouter: llm.OpenRouter(os.Getenv("OPENROUTER_API_KEY"), appURL, "Crypto Watch"),
	}
	for _, p := range providers {
		if !p.Available() {
			log.Printf("нет ключа %s: его модели в интерфейсе будут недоступны", p.Title)
		}
	}

	tools := mcpc.New(mcpURL, 30*time.Second)
	forecaster := agent.New(llm.New(llmTimeout), providers, tools, horizon)

	service, err := watch.New(forecaster, tools, dataDir, firstDelay)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go service.Run(ctx)

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           httpapi.New(service, forecaster, tools, staticDir),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		set := service.Settings()
		log.Printf("слушаю :%s, MCP %s, прогноз каждые %d мин моделью %s", port, mcpURL, set.IntervalMinutes, set.Model)
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

func intEnv(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
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
