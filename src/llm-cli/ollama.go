package main

// Минимальный клиент Ollama: чат с потоковым ответом, сведения о модели, загрузка.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// errNotFound -- модели нет на сервере Ollama.
var errNotFound = errors.New("модель не найдена")

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []message      `json:"messages"`
	Think    bool           `json:"think"`
	Options  map[string]any `json:"options,omitempty"`
}

// stats -- счётчики из последнего фрагмента ответа (done=true).
type stats struct {
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	EvalDuration    int64  `json:"eval_duration"` // наносекунды
	DoneReason      string `json:"done_reason"`   // stop или length
}

// modelInfo -- то, что help показывает о модели.
type modelInfo struct {
	ParameterSize string
	Quantization  string
	ContextLength int
}

type client struct {
	url  string
	http *http.Client
}

func newClient(url string) *client {
	return &client{url: strings.TrimRight(url, "/"), http: &http.Client{}}
}

// chat отправляет запрос и пишет токены ответа в out по мере генерации.
func (c *client) chat(ctx context.Context, req chatRequest, out io.Writer) (stats, error) {
	var st stats
	err := c.stream(ctx, "/api/chat", req, func(line []byte) error {
		var chunk struct {
			Message message `json:"message"`
			Done    bool    `json:"done"`
			Error   string  `json:"error"`
			stats
		}
		if err := json.Unmarshal(line, &chunk); err != nil {
			return err
		}
		if chunk.Error != "" {
			return errors.New(chunk.Error)
		}
		if _, err := io.WriteString(out, chunk.Message.Content); err != nil {
			return err
		}
		if chunk.Done {
			st = chunk.stats
		}
		return nil
	})
	return st, err
}

// show возвращает сведения о модели или errNotFound.
func (c *client) show(ctx context.Context, model string) (modelInfo, error) {
	var resp struct {
		Details struct {
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
		ModelInfo map[string]any `json:"model_info"`
	}
	var body []byte
	err := c.stream(ctx, "/api/show", map[string]string{"model": model}, func(line []byte) error {
		body = append(body, line...)
		return nil
	})
	if err != nil {
		return modelInfo{}, err
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return modelInfo{}, err
	}
	info := modelInfo{ParameterSize: resp.Details.ParameterSize, Quantization: resp.Details.QuantizationLevel}
	// ключ зависит от архитектуры: qwen35.context_length, llama.context_length...
	for k, v := range resp.ModelInfo {
		if n, ok := v.(float64); ok && strings.HasSuffix(k, ".context_length") {
			info.ContextLength = int(n)
		}
	}
	return info, nil
}

// pull скачивает модель; ждёт, пока Ollama не сообщит об успехе.
func (c *client) pull(ctx context.Context, model string) error {
	return c.stream(ctx, "/api/pull", map[string]string{"model": model}, func(line []byte) error {
		var p struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(line, &p) == nil && p.Error != "" {
			return errors.New(p.Error)
		}
		return nil
	})
}

// stream делает POST и передаёт каждую строку ответа (NDJSON) в onLine.
func (c *client) stream(ctx context.Context, path string, body any, onLine func([]byte) error) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("ollama %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := onLine(sc.Bytes()); err != nil {
			return err
		}
	}
	return sc.Err()
}
