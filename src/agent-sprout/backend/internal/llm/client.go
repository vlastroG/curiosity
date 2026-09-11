package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client -- HTTP-клиент к чат-API. Один на всё приложение: net/http.Client потокобезопасен
// и переиспользует соединения.
type Client struct {
	http *http.Client
}

// New создаёт клиент с общим таймаутом на запрос. Таймаут должен быть щедрым:
// рассуждающие модели молчат десятки секунд до первого токена, а стриминга здесь нет.
func New(timeout time.Duration) *Client {
	return &Client{http: &http.Client{Timeout: timeout}}
}

// APIError -- провайдер ответил, но ответ не является успешным.
// Отделён от сетевых ошибок, чтобы HTTP-слой мог отдать наружу 502 с внятным текстом.
type APIError struct {
	Provider string
	Status   int
	Body     string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s: %s", e.Provider, e.Body)
	}
	return fmt.Sprintf("%s вернул %d: %s", e.Provider, e.Status, e.Body)
}

// maxRetries -- сколько раз повторить запрос при 429 и 5xx. Ошибки клиента (400, 401, 422)
// не повторяются: они не станут другими от повтора.
const maxRetries = 2

// Chat выполняет один вызов модели и замеряет время. Стриминга нет намеренно:
// метрики (usage, latency) нужны целиком и сразу, а интерфейс показывает ответ разом.
func (c *Client) Chat(ctx context.Context, p Provider, req Request) (Response, error) {
	if !p.Available() {
		return Response{}, &APIError{
			Provider: p.Title,
			Body:     "ключ провайдера не задан в окружении сервера",
		}
	}

	payload, err := json.Marshal(wireRequest{
		Model:            req.Model,
		Messages:         req.Messages,
		Stream:           false,
		Temperature:      req.Temperature,
		MaxTokens:        req.MaxTokens,
		TopP:             neutralTopP(req.TopP),
		FrequencyPenalty: req.FrequencyPenalty,
		PresencePenalty:  req.PresencePenalty,
		ResponseFormat:   responseFormat(req.JSONObject),
	})
	if err != nil {
		return Response{}, fmt.Errorf("сборка тела запроса: %w", err)
	}

	startedAt := time.Now()

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// линейная пауза: 0.5 с, затем 1 с -- достаточно, чтобы пережить всплеск 429
			select {
			case <-ctx.Done():
				return Response{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}

		resp, err := c.do(ctx, p, payload)
		if err == nil {
			resp.LatencyMs = int(time.Since(startedAt).Milliseconds())
			return resp, nil
		}
		lastErr = err
		if !retryable(err) {
			break
		}
	}

	return Response{}, lastErr
}

func (c *Client) do(ctx context.Context, p Provider, payload []byte) (Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("сборка запроса: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	for name, value := range p.ExtraHeaders {
		if value != "" {
			httpReq.Header.Set(name, value)
		}
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("запрос к %s: %w", p.Title, err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("чтение ответа %s: %w", p.Title, err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return Response{}, &APIError{Provider: p.Title, Status: httpResp.StatusCode, Body: trim(string(body))}
	}

	var parsed wireResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Response{}, &APIError{Provider: p.Title, Body: "ответ не разобрался как JSON: " + trim(string(body))}
	}

	// OpenRouter кладёт ошибку в тело при коде 200 -- без этой проверки она превратится
	// в пустой ответ без объяснений
	if parsed.Error != nil {
		return Response{}, &APIError{Provider: p.Title, Body: parsed.Error.Message}
	}
	if len(parsed.Choices) == 0 {
		return Response{}, &APIError{Provider: p.Title, Body: "ответ без choices: " + trim(string(body))}
	}

	choice := parsed.Choices[0]
	return Response{
		Text:         choice.Message.Content,
		FinishReason: choice.FinishReason,
		Usage:        parsed.Usage,
	}, nil
}

// neutralTopP выбрасывает top_p из запроса, когда он ничего не меняет.
//
// Единица -- это «фильтра нет», то есть поведение по умолчанию. Слать такой параметр
// незачем, а некоторые провайдеры (например Liquid за OpenRouter) на него отвечают
// 400 «The top_p parameter is not supported». Ноль в wireRequest помечен omitempty
// и в тело не попадает.
func neutralTopP(topP float64) float64 {
	if topP >= 1 {
		return 0
	}
	return topP
}

func responseFormat(jsonObject bool) *wireRespFmt {
	if !jsonObject {
		return nil
	}
	return &wireRespFmt{Type: "json_object"}
}

func retryable(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		// сетевая ошибка или таймаут соединения -- повтор имеет смысл
		return true
	}
	return apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500
}

// trim укорачивает тело ошибки: целиком оно в интерфейсе не нужно и ломает вёрстку.
func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
