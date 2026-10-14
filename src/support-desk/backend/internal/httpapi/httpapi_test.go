package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"support-desk/internal/assistant"
	"support-desk/internal/guard"
	"support-desk/internal/tickets"
)

type fakeTickets struct{ items []tickets.Ticket }

func (f *fakeTickets) List(_ context.Context, status string, _ int) ([]tickets.Ticket, error) {
	var out []tickets.Ticket
	for _, tk := range f.items {
		if status == "" || tk.Status == status {
			out = append(out, tk)
		}
	}
	return out, nil
}

func (f *fakeTickets) Create(_ context.Context, title, description string) (tickets.Ticket, error) {
	if strings.TrimSpace(title) == "" {
		return tickets.Ticket{}, &tickets.ToolError{Message: "нужны и заголовок, и описание"}
	}
	tk := tickets.Ticket{ID: int64(len(f.items) + 1), Title: title, Description: description, Status: "open"}
	f.items = append(f.items, tk)
	return tk, nil
}

func (f *fakeTickets) Update(_ context.Context, id int64, status, resolution string) (tickets.Ticket, error) {
	for i := range f.items {
		if f.items[i].ID == id {
			f.items[i].Status, f.items[i].Resolution = status, resolution
			return f.items[i], nil
		}
	}
	return tickets.Ticket{}, &tickets.ToolError{Message: "не найден"}
}

func newServer(t *testing.T, ready bool, cfg Config) *httptest.Server {
	t.Helper()
	if cfg.Ask == nil {
		cfg.Ask = func(_ context.Context, q string, id int64) (assistant.Reply, error) {
			if q == "" {
				return assistant.Reply{}, assistant.ErrInput
			}
			return assistant.Reply{Answer: "Ответ", Sources: []assistant.Source{{Path: "README.md", Title: "pyRevit"}},
				Tickets: []int64{}, Guard: guard.Verdict{Injection: 0.01, OnTopic: 0.97}}, nil
		}
	}
	if cfg.Tickets == nil {
		cfg.Tickets = &fakeTickets{}
	}
	if cfg.Status == nil {
		cfg.Status = func() Status { return Status{Product: "pyRevit", Index: IndexStatus{State: "ready"}} }
	}
	cfg.Ready = func() bool { return ready }
	server := httptest.NewServer(New(cfg))
	t.Cleanup(server.Close)
	return server
}

func do(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestAskNotReady(t *testing.T) {
	s := newServer(t, false, Config{})
	code, body := do(t, "POST", s.URL+"/api/ask", `{"question":"Почему не работает вход?"}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "Индекс строится") {
		t.Errorf("%d %s", code, body)
	}
}

func TestAskInputError(t *testing.T) {
	s := newServer(t, true, Config{})
	if code, body := do(t, "POST", s.URL+"/api/ask", `{"question":""}`); code != http.StatusBadRequest {
		t.Errorf("%d %s", code, body)
	}
}

func TestAskOK(t *testing.T) {
	s := newServer(t, true, Config{})
	code, body := do(t, "POST", s.URL+"/api/ask", `{"question":"Как установить?","ticketId":3}`)
	var r struct {
		Answer  string `json:"answer"`
		Sources []struct {
			Path string `json:"path"`
		} `json:"sources"`
		Guard struct {
			OnTopic float64 `json:"onTopic"`
		} `json:"guard"`
	}
	json.Unmarshal([]byte(body), &r)
	if code != http.StatusOK || r.Answer != "Ответ" || len(r.Sources) != 1 || r.Sources[0].Path != "README.md" || r.Guard.OnTopic != 0.97 {
		t.Errorf("%d %s", code, body)
	}
}

func TestTicketsCreateAndPatch(t *testing.T) {
	s := newServer(t, true, Config{})
	code, body := do(t, "POST", s.URL+"/api/tickets", `{"title":"Вход","description":"не входит"}`)
	if code != http.StatusOK || !strings.Contains(body, `"id":1`) {
		t.Fatalf("создание: %d %s", code, body)
	}
	if code, body := do(t, "POST", s.URL+"/api/tickets", `{"title":"","description":"x"}`); code != http.StatusBadRequest ||
		!strings.Contains(body, "заголовок") {
		t.Errorf("пустой заголовок: %d %s", code, body)
	}
	code, body = do(t, "PATCH", s.URL+"/api/tickets/1", `{"status":"resolved","resolution":"переустановить"}`)
	if code != http.StatusOK || !strings.Contains(body, `"resolved"`) {
		t.Errorf("смена статуса: %d %s", code, body)
	}
	code, body = do(t, "GET", s.URL+"/api/tickets?status=resolved", "")
	if code != http.StatusOK || !strings.Contains(body, "переустановить") {
		t.Errorf("список: %d %s", code, body)
	}
}

func TestStatusIndexError(t *testing.T) {
	s := newServer(t, false, Config{Status: func() Status {
		return Status{Index: IndexStatus{State: "error", Error: "в DOCS_PATH /docs нет подходящих файлов"}}
	}})
	code, body := do(t, "GET", s.URL+"/api/status", "")
	if code != http.StatusOK || !strings.Contains(body, `"state":"error"`) || !strings.Contains(body, "нет подходящих файлов") {
		t.Errorf("%d %s", code, body)
	}
}

func TestSPAFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>чат</html>"), 0o644)
	s := newServer(t, true, Config{StaticDir: dir})
	if code, body := do(t, "GET", s.URL+"/some/route", ""); code != http.StatusOK || !strings.Contains(body, "чат") {
		t.Errorf("%d %s", code, body)
	}
}
