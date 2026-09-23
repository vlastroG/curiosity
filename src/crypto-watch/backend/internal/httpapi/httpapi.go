// Package httpapi -- REST API для интерфейса и раздача собранного фронтенда.
//
// Цены и графики интерфейс получает тем же путём, что и модель, -- через
// инструменты MCP-сервера. Своей копии данных у бэкенда нет: единственный
// источник правды -- SQLite сервера.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"crypto-watch/internal/agent"
	"crypto-watch/internal/watch"
)

// Tools -- то, что умеет звать MCP-сервер.
type Tools interface {
	Call(ctx context.Context, name string, args any, out any) error
}

// API -- обработчики.
type API struct {
	service    *watch.Service
	forecaster *agent.Forecaster
	tools      Tools
}

// New собирает маршрутизатор: API под /api, всё остальное -- статика из staticDir.
func New(service *watch.Service, forecaster *agent.Forecaster, tools Tools, staticDir string) http.Handler {
	api := &API{service: service, forecaster: forecaster, tools: tools}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", api.state)
	mux.HandleFunc("GET /api/models", api.models)
	mux.HandleFunc("GET /api/forecasts", api.forecasts)
	mux.HandleFunc("PUT /api/settings", api.updateSettings)
	mux.HandleFunc("POST /api/forecast", api.runForecast)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/", spa(staticDir))
	return mux
}

// forecastRecord -- прогноз, как его хранит сервер, с разобранным телом.
type forecastRecord struct {
	ID        int64           `json:"id"`
	CreatedAt string          `json:"createdAt"`
	Model     string          `json:"model"`
	Body      json.RawMessage `json:"body"`
}

// state -- всё, что нужно странице за один запрос.
//
// Три похода к MCP-серверу идут параллельно; упавший поход не роняет ответ --
// страница покажет то, что есть, и причину того, чего нет.
func (a *API) state(w http.ResponseWriter, r *http.Request) {
	settings := a.service.Settings()
	ctx := r.Context()
	window := map[string]any{"minutes": settings.WindowMinutes}

	var (
		wg                        sync.WaitGroup
		summary, series, schedule json.RawMessage
		forecasts                 []forecastRecord
		errs                      = make([]error, 4)
	)
	wg.Add(4)
	go func() { defer wg.Done(); errs[0] = a.tools.Call(ctx, "crypto_summary", window, &summary) }()
	go func() { defer wg.Done(); errs[1] = a.tools.Call(ctx, "crypto_series", window, &series) }()
	go func() { defer wg.Done(); errs[2] = a.tools.Call(ctx, "crypto_schedule", nil, &schedule) }()
	go func() { defer wg.Done(); forecasts, errs[3] = a.list(ctx, 1) }()
	wg.Wait()

	var latest *forecastRecord
	if len(forecasts) > 0 {
		latest = &forecasts[0]
	}
	mcpError := ""
	if err := errors.Join(errs...); err != nil {
		mcpError = err.Error()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"settings":       settings,
		"horizonMinutes": a.forecaster.Horizon(),
		"status":         a.service.Status(),
		"summary":        summary,
		"series":         series,
		"schedule":       schedule,
		"forecast":       latest,
		"mcpError":       mcpError,
		"serverTime":     time.Now().UTC().Format(time.RFC3339),
	})
}

func (a *API) list(ctx context.Context, limit int) ([]forecastRecord, error) {
	var out struct {
		Forecasts []struct {
			ID        int64  `json:"id"`
			CreatedAt string `json:"createdAt"`
			Model     string `json:"model"`
			Body      string `json:"body"`
		} `json:"forecasts"`
	}
	if err := a.tools.Call(ctx, "list_forecasts", map[string]any{"limit": limit}, &out); err != nil {
		return nil, err
	}
	records := make([]forecastRecord, 0, len(out.Forecasts))
	for _, f := range out.Forecasts {
		body := json.RawMessage(f.Body)
		if !json.Valid(body) {
			body = json.RawMessage("null")
		}
		records = append(records, forecastRecord{ID: f.ID, CreatedAt: f.CreatedAt, Model: f.Model, Body: body})
	}
	return records, nil
}

func (a *API) forecasts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	records, err := a.list(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, records)
}

// modelInfo -- модель каталога с признаком «можно выбрать».
type modelInfo struct {
	agent.Model
	Available bool `json:"available"`
}

func (a *API) models(w http.ResponseWriter, _ *http.Request) {
	out := make([]modelInfo, 0, len(agent.Models))
	for _, model := range agent.Models {
		out = append(out, modelInfo{Model: model, Available: a.forecaster.Available(model)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) updateSettings(w http.ResponseWriter, r *http.Request) {
	var set watch.Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&set); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("тело запроса не разобралось"))
		return
	}
	saved, err := a.service.Update(r.Context(), set)
	if errors.Is(err, watch.ErrInvalid) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *API) runForecast(w http.ResponseWriter, _ *http.Request) {
	a.service.Trigger()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// spa раздаёт собранный фронтенд; неизвестные пути отдают index.html.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("ответ не записался: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
