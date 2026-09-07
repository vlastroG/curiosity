// Command server поднимает бэкенд Agent Sprout: REST API поверх агента и раздачу
// собранного интерфейса.
//
// Ключи провайдеров читает только этот процесс. В браузер они не попадают ни в каком
// виде -- в этом и смысл прокси: в проектах прошлых дней ключ уезжал в статику
// и был виден любому, кто открыл страницу.
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

	"agent-sprout/internal/agent"
	"agent-sprout/internal/httpapi"
	"agent-sprout/internal/llm"
	"agent-sprout/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("agent-sprout ")

	if err := run(); err != nil {
		log.Fatalf("остановка: %v", err)
	}
}

func run() error {
	port := env("PORT", "8080")
	staticDir := env("STATIC_DIR", "./web")
	dataFile := env("DATA_FILE", "./data/chats.json")
	appURL := env("APP_URL", "http://localhost:5173")
	timeout := durationEnv("LLM_TIMEOUT", 3*time.Minute)

	providers := map[string]llm.Provider{
		llm.ProviderDeepSeek:   llm.DeepSeek(os.Getenv("DEEPSEEK_API_KEY")),
		llm.ProviderOpenRouter: llm.OpenRouter(os.Getenv("OPENROUTER_API_KEY"), appURL, "Agent Sprout"),
	}

	brain := agent.New(llm.New(timeout), providers)

	defaultModel, err := pickDefaultModel(brain, os.Getenv("DEFAULT_MODEL"))
	if err != nil {
		return err
	}
	log.Printf("модель по умолчанию: %s", defaultModel)

	chats, err := store.Open(dataFile)
	if err != nil {
		return err
	}
	log.Printf("хранилище: %s (%d чатов)", dataFile, len(chats.List()))

	handler := httpapi.New(httpapi.Deps{
		Agent:        brain,
		Store:        chats,
		DefaultModel: defaultModel,
		StaticDir:    staticDir,
	})

	server := &http.Server{
		Addr:    ":" + port,
		Handler: handler,
		// ReadHeaderTimeout защищает от медленных клиентов; общий WriteTimeout
		// не ставим намеренно -- ответ рассуждающей модели может идти минуты
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Printf("слушаю :%s, статика из %s", port, staticDir)
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// pickDefaultModel выбирает модель для новых чатов.
//
// Если названная в DEFAULT_MODEL модель недоступна (не задан ключ её провайдера),
// берётся первая доступная из каталога: обидно не запуститься целиком из-за того,
// что забыт один из двух ключей.
func pickDefaultModel(brain *agent.Agent, requested string) (string, error) {
	if requested != "" {
		model, ok := agent.FindModel(requested)
		if !ok {
			return "", errors.New("DEFAULT_MODEL указывает на модель, которой нет в каталоге: " + requested)
		}
		if brain.Available(model) {
			return model.ID, nil
		}
		log.Printf("DEFAULT_MODEL=%s недоступна: не задан ключ провайдера %s", model.ID, model.Provider)
	}

	for _, model := range agent.Models {
		if brain.Available(model) {
			return model.ID, nil
		}
	}

	return "", errors.New("не задан ни один ключ провайдера: нужен DEEPSEEK_API_KEY или OPENROUTER_API_KEY")
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
