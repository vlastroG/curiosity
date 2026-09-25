// Package httpapi -- REST и SSE для интерфейса, раздача собранного фронтенда
// и опубликованных HTML-планов.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"trip-agent/internal/agent"
	"trip-agent/internal/registry"
	"trip-agent/internal/runs"
)

// Registry -- то, что API нужно от реестра серверов.
type Registry interface {
	Discover(ctx context.Context)
	Servers() []registry.Server
}

// API -- обработчики.
type API struct {
	runs     *runs.Manager
	registry Registry
	model    agent.Model
	outDir   string
	now      func() time.Time
}

// New собирает маршрутизатор.
func New(m *runs.Manager, reg Registry, model agent.Model, outDir, staticDir string, now func() time.Time) http.Handler {
	api := &API{runs: m, registry: reg, model: model, outDir: outDir, now: now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/meta", api.meta)
	mux.HandleFunc("GET /api/servers", api.servers)
	mux.HandleFunc("GET /api/runs", api.list)
	mux.HandleFunc("POST /api/runs", api.submit)
	mux.HandleFunc("GET /api/runs/{id}", api.get)
	mux.HandleFunc("GET /api/runs/{id}/events", api.events)
	mux.HandleFunc("GET /plans/{file}", api.plan)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/", spa(staticDir))
	return mux
}

func (a *API) meta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"model":        a.model,
		"interests":    agent.Interests,
		"paces":        agent.Paces,
		"currencies":   []string{"RUB", "USD", "EUR"},
		"maxTripDays":  agent.MaxTripDays,
		"maxTravelers": agent.MaxTravelers,
		"today":        a.now().Format(time.DateOnly),
	})
}

func (a *API) servers(w http.ResponseWriter, r *http.Request) {
	a.registry.Discover(r.Context())
	writeJSON(w, http.StatusOK, a.registry.Servers())
}

func (a *API) list(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.runs.List())
}

func (a *API) submit(w http.ResponseWriter, r *http.Request) {
	var form agent.Form
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&form); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("форма не разобралась"))
		return
	}
	run, err := a.runs.Submit(form)
	if errors.Is(err, agent.ErrInvalid) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	run, err := a.runs.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// events -- поток событий прогона (Server-Sent Events). Сначала отдаёт то, что
// уже было после since, потом живые события до конца прогона.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	past, live, cancel, err := a.runs.Subscribe(id, since)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	defer cancel()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("поток не поддерживается"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func(e agent.Event) bool {
		raw, _ := json.Marshal(e)
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, raw); err != nil {
			return false
		}
		flusher.Flush()
		return e.Type != agent.EventFinished
	}
	for _, e := range past {
		if !send(e) {
			return
		}
	}
	if run, _ := a.runs.Get(id); runs.Finished(run.Status) {
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-live:
			if !send(e) {
				return
			}
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

var planFile = regexp.MustCompile(`^[a-z0-9-]+\.html$`)

// plan -- опубликованный HTML-план. Имя строго по шаблону: путь из запроса не
// должен выводить за каталог планов.
func (a *API) plan(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !planFile.MatchString(name) {
		writeError(w, http.StatusNotFound, errors.New("нет такого плана"))
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src https:")
	http.ServeFile(w, r, filepath.Join(a.outDir, name))
}

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
