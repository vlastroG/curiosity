// Package embed -- клиент Ollama: эмбеддинги, загрузка моделей, проверка GPU.
//
// Ollama отдаёт векторы через POST /api/embed пачками. Сервис требует, чтобы
// модель считала на видеокарте: после первого запроса /api/ps показывает,
// сколько модели лежит в видеопамяти (size_vram). Если меньше, чем вся модель, --
// Ollama считает на процессоре, и работа останавливается с понятной ошибкой.
package embed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Model -- модель эмбеддингов и её соглашения о входе.
type Model struct {
	Name         string `json:"name"`
	Title        string `json:"title"`
	Multilingual bool   `json:"multilingual"`
	// префиксы входа: некоторые модели обучены различать документ и запрос
	DocPrefix   string `json:"-"`
	QueryPrefix string `json:"-"`
	// служебные токены, которые модель добавляет к любому входу
	SpecialTokens int `json:"-"`
}

// Models -- известные модели. Остальные тоже работают, но без префиксов.
var Models = map[string]Model{
	"bge-m3": {Name: "bge-m3", Title: "BGE-M3", Multilingual: true, SpecialTokens: 2},
	"nomic-embed-text": {Name: "nomic-embed-text", Title: "Nomic Embed",
		DocPrefix: "search_document: ", QueryPrefix: "search_query: ", SpecialTokens: 2},
	"qwen3-embedding:0.6b": {Name: "qwen3-embedding:0.6b", Title: "Qwen3 Embedding 0.6B", Multilingual: true,
		QueryPrefix:   "Instruct: Given a question about a novel, retrieve passages that answer it\nQuery: ",
		SpecialTokens: 1},
}

// Lookup -- модель по имени; неизвестная получает пустые префиксы.
func Lookup(name string) Model {
	if m, ok := Models[name]; ok {
		return m
	}
	return Model{Name: name, Title: name}
}

// Client -- HTTP-клиент Ollama.
type Client struct {
	URL  string
	HTTP *http.Client
}

// New -- клиент с таймаутом на один запрос.
func New(url string, timeout time.Duration) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), HTTP: &http.Client{Timeout: timeout}}
}

// Result -- векторы пачки и сколько токенов насчитала модель на весь вход.
type Result struct {
	Vectors      [][]float32
	PromptTokens int
}

// Embed возвращает нормализованные векторы входов в том же порядке.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) (Result, error) {
	var resp struct {
		Embeddings      [][]float32 `json:"embeddings"`
		PromptEvalCount int         `json:"prompt_eval_count"`
	}
	req := map[string]any{"model": model, "input": inputs, "truncate": true}
	if err := c.post(ctx, "/api/embed", req, &resp); err != nil {
		return Result{}, err
	}
	if len(resp.Embeddings) != len(inputs) {
		return Result{}, fmt.Errorf("ollama вернула %d векторов на %d входов", len(resp.Embeddings), len(inputs))
	}
	for i, v := range resp.Embeddings {
		if err := Normalize(v); err != nil {
			return Result{}, fmt.Errorf("вектор %d: %w", i, err)
		}
	}
	return Result{Vectors: resp.Embeddings, PromptTokens: resp.PromptEvalCount}, nil
}

// Normalize приводит вектор к единичной длине (L2). После этого косинусное
// сходство двух векторов -- просто их скалярное произведение.
func Normalize(v []float32) error {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
		return errors.New("нулевой или испорченный вектор")
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return nil
}

// Has -- скачана ли модель.
func (c *Client) Has(ctx context.Context, model string) (bool, error) {
	err := c.post(ctx, "/api/show", map[string]string{"model": model}, &struct{}{})
	var he *HTTPError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

// Pull скачивает модель, сообщая о ходе загрузки.
func (c *Client) Pull(ctx context.Context, model string, progress func(status string, done, total int64)) error {
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	// загрузка идёт минутами -- общий таймаут клиента тут не подходит
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return c.unreachable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev struct {
			Status    string `json:"status"`
			Completed int64  `json:"completed"`
			Total     int64  `json:"total"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Error != "" {
			return fmt.Errorf("загрузка %s: %s", model, ev.Error)
		}
		if progress != nil {
			progress(ev.Status, ev.Completed, ev.Total)
		}
	}
	return sc.Err()
}

// Placement -- где сейчас лежит загруженная модель.
type Placement struct {
	Loaded   bool  `json:"loaded"`
	Size     int64 `json:"size"`
	SizeVRAM int64 `json:"sizeVram"`
}

// OnGPU -- модель целиком в видеопамяти.
func (p Placement) OnGPU() bool { return p.Loaded && p.SizeVRAM > 0 && p.SizeVRAM >= p.Size }

// Where -- размещение модели по /api/ps. Незагруженная модель -- Loaded=false.
func (c *Client) Where(ctx context.Context, model string) (Placement, error) {
	var resp struct {
		Models []struct {
			Name     string `json:"name"`
			Model    string `json:"model"`
			Size     int64  `json:"size"`
			SizeVRAM int64  `json:"size_vram"`
		} `json:"models"`
	}
	if err := c.get(ctx, "/api/ps", &resp); err != nil {
		return Placement{}, err
	}
	for _, m := range resp.Models {
		if sameModel(m.Name, model) || sameModel(m.Model, model) {
			return Placement{Loaded: true, Size: m.Size, SizeVRAM: m.SizeVRAM}, nil
		}
	}
	return Placement{}, nil
}

// RequireGPU проверяет, что модель загружена и считает на видеокарте.
// Вызывать после хотя бы одного Embed: до него модель не загружена.
func (c *Client) RequireGPU(ctx context.Context, model string) (Placement, error) {
	p, err := c.Where(ctx, model)
	if err != nil {
		return p, err
	}
	switch {
	case !p.Loaded:
		return p, fmt.Errorf("модель %s не загружена в Ollama", model)
	case p.SizeVRAM == 0:
		return p, fmt.Errorf("Ollama считает %s на процессоре: видеокарта недоступна. "+
			"Проверьте GPU в Docker Desktop (nvidia-smi в контейнере ollama) или укажите OLLAMA_URL "+
			"на Ollama, которая видит видеокарту", model)
	case p.SizeVRAM < p.Size:
		return p, fmt.Errorf("модель %s поместилась в видеопамять только на %.0f%%, остальное считает процессор. "+
			"Освободите видеопамять и повторите", model, 100*float64(p.SizeVRAM)/float64(p.Size))
	}
	return p, nil
}

// Version -- версия Ollama; заодно проверка, что она отвечает.
func (c *Client) Version(ctx context.Context) (string, error) {
	var resp struct {
		Version string `json:"version"`
	}
	err := c.get(ctx, "/api/version", &resp)
	return resp.Version, err
}

func sameModel(a, b string) bool {
	norm := func(s string) string {
		if !strings.Contains(s, ":") {
			return s + ":latest"
		}
		return s
	}
	return a != "" && norm(a) == norm(b)
}

// HTTPError -- ответ Ollama с ошибкой.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("ollama: HTTP %d: %s", e.Status, e.Message) }

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return c.unreachable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) unreachable(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("Ollama недоступна по адресу %s: %w", c.URL, err)
}

func readError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		msg = body.Error
	}
	return &HTTPError{Status: resp.StatusCode, Message: msg}
}
