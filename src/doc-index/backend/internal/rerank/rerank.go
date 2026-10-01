// Package rerank -- второй этап поиска: отбор кандидатов по порогу косинуса,
// оценка каждого кросс-энкодером (Qwen3-Reranker в Ollama) и отсечение по
// порогу релевантности.
//
// Эмбеддинг вопроса и эмбеддинг отрывка считаются порознь -- поэтому поиск
// быстрый, но грубый. Кросс-энкодер читает вопрос и отрывок вместе и отвечает
// «yes/no»: подходит ли отрывок. Оценка -- вероятность «yes».
package rerank

import (
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

// Scorer оценивает релевантность отрывков запросу: числа от 0 до 1.
type Scorer interface {
	Score(ctx context.Context, query string, docs []string) ([]float64, error)
	Name() string
}

// Ollama -- Qwen3-Reranker через /api/generate: один токен, logprobs.
type Ollama struct {
	URL    string
	Model  string
	HTTP   *http.Client
	MaxDoc int // символов отрывка, остальное обрезается; 0 -- 6000
}

// NewOllama -- клиент с таймаутом на один отрывок.
func NewOllama(url, model string, timeout time.Duration) *Ollama {
	return &Ollama{URL: strings.TrimRight(url, "/"), Model: model, HTTP: &http.Client{Timeout: timeout}}
}

// Name -- модель реранкера.
func (o *Ollama) Name() string { return o.Model }

// Instruction -- задача для кросс-энкодера (Qwen3-Reranker обучен с инструкцией).
const Instruction = "Given a question about a novel, retrieve relevant passages that answer the question"

// Prompt -- шаблон Qwen3-Reranker: ответ ассистента начинается с пустых
// рассуждений, следующий токен -- «yes» или «no».
func Prompt(query, doc string) string {
	return "<|im_start|>system\nJudge whether the Document meets the requirements based on the Query and the Instruct " +
		"provided. Note that the answer can only be \"yes\" or \"no\".<|im_end|>\n<|im_start|>user\n" +
		"<Instruct>: " + Instruction + "\n<Query>: " + query + "\n<Document>: " + doc +
		"<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
}

// ErrNoVerdict -- среди вероятных токенов нет ни «yes», ни «no»: модель
// не справилась с форматом (или это не реранкер).
var ErrNoVerdict = errors.New("реранкер не ответил yes/no: проверьте, что RERANK_MODEL -- Qwen3-Reranker")

// Score -- оценка каждого отрывка по отдельности. Ollama всё равно
// обслуживает запросы к одной модели по очереди.
func (o *Ollama) Score(ctx context.Context, query string, docs []string) ([]float64, error) {
	out := make([]float64, len(docs))
	for i, d := range docs {
		s, err := o.one(ctx, query, d)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func (o *Ollama) one(ctx context.Context, query, doc string) (float64, error) {
	max := o.MaxDoc
	if max <= 0 {
		max = 6000
	}
	if r := []rune(doc); len(r) > max {
		doc = string(r[:max])
	}
	body, _ := json.Marshal(map[string]any{
		"model": o.Model, "prompt": Prompt(query, doc), "raw": true, "stream": false,
		"logprobs": true, "top_logprobs": 20,
		"options": map[string]any{"num_predict": 1, "temperature": 0},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("реранкер недоступен: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("реранкер: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Logprobs []struct {
			Top []struct {
				Token   string  `json:"token"`
				Logprob float64 `json:"logprob"`
			} `json:"top_logprobs"`
		} `json:"logprobs"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("реранкер: %w", err)
	}
	if len(out.Logprobs) == 0 {
		return 0, errors.New("реранкер: Ollama не вернула logprobs (нужна версия с поддержкой logprobs)")
	}
	return Verdict(func(yield func(string, float64)) {
		for _, t := range out.Logprobs[0].Top {
			yield(t.Token, t.Logprob)
		}
	})
}

// Verdict -- P(yes) / (P(yes) + P(no)) по вероятным первым токенам.
// Варианты одного слова («Yes», «yes», « yes») складываются.
func Verdict(each func(yield func(token string, logprob float64))) (float64, error) {
	var yes, no float64
	seen := false
	each(func(tok string, lp float64) {
		switch strings.ToLower(strings.TrimSpace(tok)) {
		case "yes":
			yes += math.Exp(lp)
			seen = true
		case "no":
			no += math.Exp(lp)
			seen = true
		}
	})
	if !seen || yes+no == 0 {
		return 0, ErrNoVerdict
	}
	return yes / (yes + no), nil
}
