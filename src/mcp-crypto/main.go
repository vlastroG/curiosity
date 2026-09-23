// Command mcp-crypto -- MCP-сервер с фоновым планировщиком: сам собирает курсы
// криптовалют и отдаёт агрегаты за любое окно.
//
// Два процесса в одном бинаре, и они не ждут друг друга:
//
//	планировщик (горутина)                MCP на /mcp (HTTP)
//	  каждые N минут:                       crypto_summary ─► агрегат из SQLite
//	    Binance ─► цены ─► SQLite           crypto_series  ─► точки из SQLite
//	                                        crypto_schedule ─► статус / новый N
//	                                        crypto_now     ─► внеочередной сбор
//	                                        save_forecast, list_forecasts
//
// Транспорт -- Streamable HTTP без сессий и без SSE, как у mcp-weather: любой
// вызов воспроизводится одним curl, перезапуск контейнера клиентам не мешает.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version -- версия сервера, её клиент видит в ответе на initialize.
const version = "0.1.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("mcp-crypto ")

	if err := run(); err != nil {
		log.Fatalf("остановка: %v", err)
	}
}

func run() error {
	port := env("PORT", "8080")
	dbPath := env("DB_PATH", "./crypto.db")
	base := env("BINANCE_URL", "https://data-api.binance.vision")
	symbols := parseSymbols(env("SYMBOLS", "BTC,ETH,SOL,TON,DOGE"))
	// интервал из окружения -- только стартовый: как только его сменят
	// инструментом, он сохранится в базе и будет браться оттуда
	interval := durationEnv("COLLECT_INTERVAL", time.Minute)
	retention := durationEnv("RETENTION", 7*24*time.Hour)
	apiTimeout := durationEnv("API_TIMEOUT", 15*time.Second)

	store, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	client := &http.Client{Timeout: apiTimeout}
	sched := newScheduler(store, func(ctx context.Context) ([]Quote, error) {
		return fetchPrices(ctx, client, base, symbols)
	}, interval, retention)

	server := newServer(store, sched, symbols)

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

	go sched.Run(ctx)

	errc := make(chan error, 1)
	go func() {
		log.Printf("слушаю :%s, MCP на /mcp, база %s, монеты %s", port, dbPath, strings.Join(symbols, ","))
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

// parseSymbols -- «btc, eth» → [BTC ETH], без пустых и повторов.
func parseSymbols(value string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(value, ",") {
		symbol := strings.ToUpper(strings.TrimSpace(part))
		if symbol != "" && !seen[symbol] {
			seen[symbol] = true
			out = append(out, symbol)
		}
	}
	return out
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
