// Package httpapi -- HTTP-слой: вопрос ассистенту, тикеты, состояние, статика.
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
	"strings"

	"support-desk/internal/assistant"
	"support-desk/internal/tickets"
)

// TicketAPI -- то, что интерфейс делает с тикетами; в работе -- tickets.Client.
type TicketAPI interface {
	List(ctx context.Context, status string, limit int) ([]tickets.Ticket, error)
	Create(ctx context.Context, title, description string) (tickets.Ticket, error)
	Update(ctx context.Context, id int64, status, resolution string) (tickets.Ticket, error)
}

// Status -- состояние сервиса для шапки интерфейса.
type Status struct {
	Product string      `json:"product"`
	Model   string      `json:"model"`
	Guard   GuardStatus `json:"guard"`
	Index   IndexStatus `json:"index"`
}

type GuardStatus struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model"`
}

// IndexStatus -- индексация: indexing, ready или error.
type IndexStatus struct {
	State  string `json:"state"`
	Done   int    `json:"done"`
	Total  int    `json:"total"`
	Files  int    `json:"files"`
	Chunks int    `json:"chunks"`
	Error  string `json:"error,omitempty"`
}

// Config -- зависимости HTTP-слоя.
type Config struct {
	Ask       func(ctx context.Context, q string, ticketID int64) (assistant.Reply, error)
	Tickets   TicketAPI
	Status    func() Status
	Ready     func() bool
	StaticDir string
}

// New собирает маршруты.
func New(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, cfg.Status())
	})

	mux.HandleFunc("POST /api/ask", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Question string `json:"question"`
			TicketID int64  `json:"ticketId"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if !cfg.Ready() {
			writeError(w, http.StatusServiceUnavailable, "Индекс строится, попробуйте через минуту")
			return
		}
		reply, err := cfg.Ask(r.Context(), in.Question, in.TicketID)
		var tool *tickets.ToolError
		switch {
		case errors.Is(err, assistant.ErrInput):
			writeError(w, http.StatusBadRequest, "Сообщение должно быть от 1 до 2000 символов")
		case errors.As(err, &tool):
			writeError(w, http.StatusBadRequest, tool.Message)
		case err != nil:
			log.Printf("ответ не получен: %v", err)
			writeError(w, http.StatusBadGateway, "Не удалось получить ответ: "+err.Error())
		default:
			writeJSON(w, http.StatusOK, reply)
		}
	})

	mux.HandleFunc("GET /api/tickets", func(w http.ResponseWriter, r *http.Request) {
		list, err := cfg.Tickets.List(r.Context(), r.URL.Query().Get("status"), 100)
		if list == nil {
			list = []tickets.Ticket{}
		}
		respond(w, map[string]any{"tickets": list}, err)
	})
	mux.HandleFunc("POST /api/tickets", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		tk, err := cfg.Tickets.Create(r.Context(), in.Title, in.Description)
		respond(w, tk, err)
	})
	mux.HandleFunc("PATCH /api/tickets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "неверный номер тикета")
			return
		}
		var in struct {
			Status     string `json:"status"`
			Resolution string `json:"resolution"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		tk, err := cfg.Tickets.Update(r.Context(), id, in.Status, in.Resolution)
		respond(w, tk, err)
	})

	mux.Handle("/", staticHandler(cfg.StaticDir))
	return mux
}

// respond -- ответ инструмента тикетов: отказ инструмента -- 400 с его текстом.
func respond(w http.ResponseWriter, out any, err error) {
	var tool *tickets.ToolError
	switch {
	case errors.As(err, &tool):
		writeError(w, http.StatusBadRequest, tool.Message)
	case err != nil:
		log.Printf("тикеты: %v", err)
		writeError(w, http.StatusBadGateway, "Сервер тикетов недоступен: "+err.Error())
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "тело запроса не разобралось как json")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

// staticHandler раздаёт собранный интерфейс; любой несуществующий путь -- index.html.
// index.html -- no-cache: иначе браузер держит старую страницу со ссылкой на старый бандл.
func staticHandler(dir string) http.Handler {
	if dir == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "статика не собрана: STATIC_DIR не задан", http.StatusNotFound)
		})
	}
	files := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Join(dir, filepath.Clean("/"+strings.TrimPrefix(r.URL.Path, "/")))
		if info, err := os.Stat(clean); err == nil && !info.IsDir() {
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}
