// Package embed -- клиент Ollama: эмбеддинги и загрузка моделей.
//
// Ollama отдаёт векторы через POST /api/embed пачками.
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
