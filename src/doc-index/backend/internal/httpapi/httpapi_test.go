package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/chunk"
	"doc-index/internal/embed"
	"doc-index/internal/httpapi"
	"doc-index/internal/index"
	"doc-index/internal/search"
	"doc-index/internal/store"
	"doc-index/internal/testkit"
)

type fixture struct {
	srv    *httptest.Server
	ollama *testkit.Ollama
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "sources"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sources", "pg74.txt"), testkit.GutenbergFile("A Test Novel", testkit.Novel()), 0o644)
	questions := `[{"id":"a","lang":"en","book":"tom","chapters":["I"],"evidence":["whitewash"],"q":"whitewash fence brush"}]`
	_ = os.WriteFile(filepath.Join(dir, "questions.json"), []byte(questions), 0o644)
	web := filepath.Join(dir, "web")
	_ = os.MkdirAll(web, 0o755)
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>ui</title>"), 0o644)

	o := testkit.NewOllama("bge-m3", "nomic-embed-text")
	t.Cleanup(o.Close)
	st, err := store.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	client := embed.New(o.URL, 10*time.Second)
	variants := index.Variants("bge-m3", "nomic-embed-text")
	ix := &index.Indexer{Store: st, Ollama: client, Books: []int{74}, Variants: variants,
		Fetcher: &book.Fetcher{Dir: filepath.Join(dir, "sources")}, Params: chunk.DefaultParams, Batch: 16}

	h := httpapi.New(httpapi.Config{
		Store: st, Searcher: &search.Searcher{Store: st, Embedder: client, Variants: variants},
		Ollama: client, OllamaURL: o.URL, Jobs: &index.Jobs{},
		Index: func(ctx context.Context, rebuild bool, emit func(index.Event)) error {
			_, err := ix.Run(ctx, rebuild, emit)
			return err
		},
		EvalPath: filepath.Join(dir, "questions.json"), Params: chunk.DefaultParams, StaticDir: web,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, ollama: o}
}

func (f *fixture) get(t *testing.T, path string, out any) int {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (f *fixture) post(t *testing.T, path, body string, out any) int {
	t.Helper()
	resp, err := http.Post(f.srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// buildIndex запускает индексацию через API и читает поток событий до конца.
func (f *fixture) buildIndex(t *testing.T) []index.Event {
	t.Helper()
	if code := f.post(t, "/api/index", `{}`, nil); code != http.StatusAccepted {
		t.Fatalf("старт индексации: %d", code)
	}
	resp, err := http.Get(f.srv.URL + "/api/index/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type %s", ct)
	}
	var events []index.Event
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if line == "event: end" {
			break
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var e index.Event
			if json.Unmarshal([]byte(data), &e) == nil && e.Seq > 0 {
				events = append(events, e)
			}
		}
	}
	return events
}

func TestIndexOverAPIAndStatus(t *testing.T) {
	f := setup(t)
	var st map[string]any
	f.get(t, "/api/status", &st)
	for _, v := range st["variants"].([]any) {
		if v.(map[string]any)["ready"].(bool) {
			t.Fatal("до индексации вариант готов")
		}
	}

	events := f.buildIndex(t)
	if len(events) == 0 || events[len(events)-1].Stage != "done" {
		t.Fatalf("последнее событие: %+v", events[len(events)-1])
	}
	for i, e := range events {
		if e.Seq != i+1 {
			t.Fatalf("события не по порядку: %d на месте %d", e.Seq, i+1)
		}
	}
	// переподключение с since отдаёт только хвост
	resp, _ := http.Get(f.srv.URL + "/api/index/events?since=" + "3")
	sc := bufio.NewScanner(resp.Body)
	sc.Scan()
	if !strings.HasPrefix(sc.Text(), "id: 4") {
		t.Errorf("since=3 начал с %q", sc.Text())
	}
	resp.Body.Close()

	f.get(t, "/api/status", &st)
	ready := 0
	for _, v := range st["variants"].([]any) {
		if v.(map[string]any)["ready"].(bool) {
			ready++
		}
	}
	if ready != 4 || st["sample"] == nil {
		t.Errorf("готово вариантов %d, пример чанка %v", ready, st["sample"] != nil)
	}
	gpu := st["ollama"].(map[string]any)["models"].(map[string]any)["bge-m3"].(map[string]any)
	if gpu["sizeVram"].(float64) == 0 {
		t.Errorf("статус GPU: %v", gpu)
	}
}

func TestSearchReadAndCompare(t *testing.T) {
	f := setup(t)

	// до индексации -- понятная ошибка
	var e map[string]string
	if code := f.post(t, "/api/search", `{"query":"fence","variants":["structure"]}`, &e); code != http.StatusConflict {
		t.Errorf("поиск без индекса: %d %v", code, e)
	}
	f.buildIndex(t)

	for body, want := range map[string]int{
		`{"query":"  ","variants":["structure"]}`: http.StatusBadRequest,
		`{"query":"x","variants":[]}`:             http.StatusBadRequest,
		`{"query":"x","variants":["nope"]}`:       http.StatusBadRequest,
		`not json`:                                http.StatusBadRequest,
	} {
		if code := f.post(t, "/api/search", body, nil); code != want {
			t.Errorf("%s: %d, ожидалось %d", body, code, want)
		}
	}

	var resp search.Response
	code := f.post(t, "/api/search", `{"query":"the cave candle darkness","variants":["fixed","structure","semantic"],"k":3}`, &resp)
	if code != http.StatusOK || len(resp.Results["structure"]) != 3 {
		t.Fatalf("поиск: %d %+v", code, resp)
	}
	top := resp.Results["structure"][0]
	if top.Sections[0].Key != "III" {
		t.Errorf("первое место не из главы III: %+v", top)
	}

	// «открыть в книге»: чанк → глава → абзацы и границы чанков
	var ch struct {
		Chunk search.Hit `json:"chunk"`
	}
	if code := f.get(t, "/api/chunks/structure/"+top.ChunkID, &ch); code != 200 || ch.Chunk.ChunkID != top.ChunkID {
		t.Fatalf("чанк: %d", code)
	}
	n := top.Sections[0].N
	var sec struct {
		Paras []struct {
			Start, End int
			Text       string
		} `json:"paras"`
		Heading string `json:"heading"`
	}
	f.get(t, "/api/books/tom/sections/"+strconv.Itoa(n), &sec)
	if len(sec.Paras) != 40 || !strings.HasPrefix(sec.Heading, "CHAPTER III") {
		t.Fatalf("глава: %d абзацев, заголовок %q", len(sec.Paras), sec.Heading)
	}
	if top.Start < sec.Paras[0].Start-20 || top.End > sec.Paras[len(sec.Paras)-1].End {
		t.Errorf("чанк %d–%d вне главы %d–%d", top.Start, top.End, sec.Paras[0].Start, sec.Paras[len(sec.Paras)-1].End)
	}
	var cs struct {
		Chunks []search.Hit `json:"chunks"`
	}
	f.get(t, "/api/books/tom/sections/"+strconv.Itoa(n)+"/chunks?variant=fixed", &cs)
	if len(cs.Chunks) < 3 {
		t.Errorf("нарезка fixed для главы: %d чанков", len(cs.Chunks))
	}
	if code := f.get(t, "/api/books/tom/sections/99", nil); code != http.StatusNotFound {
		t.Errorf("нет такой главы: %d", code)
	}
	if code := f.get(t, "/api/books/nope", nil); code != http.StatusNotFound {
		t.Errorf("нет такой книги: %d", code)
	}

	var qs struct {
		Questions []map[string]any `json:"questions"`
	}
	f.get(t, "/api/questions", &qs)
	if len(qs.Questions) != 1 || qs.Questions[0]["valid"] != true {
		t.Errorf("вопросы: %+v", qs)
	}
	var rep map[string]any
	if code := f.get(t, "/api/compare", &rep); code != 200 || len(rep["variants"].([]any)) != 4 {
		t.Fatalf("сравнение: %d", code)
	}
}

func TestSearchRefusesCPU(t *testing.T) {
	f := setup(t)
	f.buildIndex(t)
	f.ollama.SetVRAM(0)
	var e map[string]string
	code := f.post(t, "/api/search", `{"query":"a brand new question","variants":["structure-nomic"]}`, &e)
	if code != http.StatusServiceUnavailable || !strings.Contains(e["error"], "на процессоре") {
		t.Fatalf("поиск на CPU: %d %v", code, e)
	}
}

func TestSPAAndHeaders(t *testing.T) {
	f := setup(t)
	resp, err := http.Get(f.srv.URL + "/some/route")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("SPA: %d, CSP %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
	if code := f.post(t, "/api/index", `{}`, nil); code != http.StatusAccepted {
		t.Fatalf("старт: %d", code)
	}
	if code := f.post(t, "/api/index", `{}`, nil); code != http.StatusConflict && code != http.StatusAccepted {
		t.Errorf("второй старт: %d", code)
	}
}
