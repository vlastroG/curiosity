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
	"sync"
	"time"
)

// Client -- HTTP-клиент к чат-API. Один на всё приложение: net/http.Client потокобезопасен
// и переиспользует соединения.
type Client struct {
	http *http.Client
	// downgraded -- пары "провайдер+модель", которые уже отказались от ускоряющих
	// параметров. Без этой памяти каждый вызов к такому провайдеру платил бы лишним
	// кругом: запрос, 400, повтор. Ключ включает модель: поддержка параметров
	// у одного провайдера от модели к модели разная
	downgraded sync.Map
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

	startedAt := time.Now()
	key := p.Endpoint + "|" + req.Model

	drop := degradation{}
	if known, ok := c.downgraded.Load(key); ok {
		drop = known.(degradation)
	}

	resp, err := c.attempt(ctx, p, req, drop)
	if err == nil {
		resp.LatencyMs = int(time.Since(startedAt).Milliseconds())
		resp.Downgraded = drop.touches(req)
		return resp, nil
	}

	// провайдер не понял ускоряющий параметр -- один повтор без него. Набор
	// поддержанных параметров у провайдеров разный и меняется без предупреждения:
	// вместо догадки пробуем и честно откатываемся. Откат точечный -- отказ
	// от json-схемы не должен заодно возвращать нам дорогое рассуждение
	// в отказе нас интересует только то, что в этом запросе вообще было
	rejected := rejectedParameter(err).onlyPresent(req)
	if rejected.any() && !drop.covers(rejected) {
		drop = drop.with(rejected)
		c.downgraded.Store(key, drop)

		resp, retryErr := c.attempt(ctx, p, req, drop)
		if retryErr == nil {
			resp.LatencyMs = int(time.Since(startedAt).Milliseconds())
			resp.Downgraded = drop.touches(req)
			return resp, nil
		}
		return Response{}, retryErr
	}

	return Response{}, err
}

// attempt -- серия попыток с одним и тем же телом. Повторы здесь только за 429 и 5xx:
// ошибка клиента от повтора другой не станет.
func (c *Client) attempt(ctx context.Context, p Provider, req Request, drop degradation) (Response, error) {
	payload, err := json.Marshal(wire(req, drop))
	if err != nil {
		return Response{}, fmt.Errorf("сборка тела запроса: %w", err)
	}

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
		ToolCalls:    choice.Message.ToolCalls,
		FinishReason: choice.FinishReason,
		Usage:        parsed.Usage,
	}, nil
}

// degradation -- какие необязательные параметры этот провайдер не принимает.
//
// Поля отдельные, а не один флаг, ровно из-за DeepSeek: json-схему он отвергает,
// а выключатель рассуждения принимает. Скинуть их вместе значило бы вернуть себе
// то самое рассуждение на служебных вызовах, ради которого всё и затевалось.
//
// Инструменты здесь третьей осью по той же причине: отказ провайдера от tools
// не должен заодно отбирать схему у диспетчера. Цена отказа при этом больше,
// чем у остальных: без инструментов модель не узнает погоду и спросит про неё
// человека -- ход пройдёт, но беднее.
type degradation struct {
	schema   bool
	thinking bool
	tools    bool
}

func (d degradation) any() bool { return d.schema || d.thinking || d.tools }

func (d degradation) with(other degradation) degradation {
	return degradation{
		schema:   d.schema || other.schema,
		thinking: d.thinking || other.thinking,
		tools:    d.tools || other.tools,
	}
}

// covers -- всё из other уже отброшено, повторять нечего
func (d degradation) covers(other degradation) bool {
	return (!other.schema || d.schema) &&
		(!other.thinking || d.thinking) &&
		(!other.tools || d.tools)
}

// onlyPresent оставляет то, что в этом запросе действительно отправлялось
func (d degradation) onlyPresent(req Request) degradation {
	return degradation{
		schema:   d.schema && req.Schema != nil,
		thinking: d.thinking && req.Thinking != "",
		tools:    d.tools && len(req.Tools) > 0,
	}
}

// touches -- отбросили ли мы что-то, что в этом запросе было
func (d degradation) touches(req Request) bool {
	return (d.schema && req.Schema != nil) ||
		(d.thinking && req.Thinking != "") ||
		(d.tools && len(req.Tools) > 0)
}

// wire переводит запрос в тело провайдера. drop -- что провайдер уже отвергал:
// схема заменяется свободным json, выключатель рассуждения просто не отправляется.
// Контракт ответа при этом не теряется -- вызывающий всё равно получит json,
// просто без гарантии формы.
func wire(req Request, drop degradation) wireRequest {
	out := wireRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Stream:      false,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if req.Thinking != "" && !drop.thinking {
		out.Thinking = &wireThinking{Type: req.Thinking}
	}
	switch {
	case req.Schema != nil && !drop.schema:
		out.ResponseFormat = &wireRespFmt{
			Type: "json_schema",
			JSONSchema: &wireJSONSchema{
				Name:   req.Schema.Name,
				Strict: true,
				Schema: req.Schema.Definition,
			},
		}
	case req.Schema != nil:
		// провайдеры требуют, чтобы в промпте при этом встречалось слово "json",
		// и оно там есть
		out.ResponseFormat = &wireRespFmt{Type: "json_object"}
	}

	if len(req.Tools) > 0 && !drop.tools {
		out.Tools = make([]wireTool, 0, len(req.Tools))
		for _, tool := range req.Tools {
			out.Tools = append(out.Tools, wireTool{
				Type: "function",
				Function: wireToolFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.Parameters,
				},
			})
		}
		out.ToolChoice = req.ToolChoice
		if out.ToolChoice == "" {
			out.ToolChoice = ToolChoiceAuto
		}
	}

	return out
}

// rejectedParameter разбирает 400 про неизвестное поле запроса.
//
// Общего кода ошибки у провайдеров нет, формулировки разные -- смотрим текст.
// Если из текста не видно, какой именно парамет не понравился, отбрасываем оба:
// лучше потерять ускорение, чем зациклиться на отказах.
func rejectedParameter(err error) degradation {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		return degradation{}
	}
	body := strings.ToLower(apiErr.Body)

	schema := containsAny(body, "response_format", "json_schema", "structured_output")
	thinking := containsAny(body, "thinking", "reasoning")
	tools := containsAny(body, "tool", "function call", "function_call")
	if schema || thinking || tools {
		return degradation{schema: schema, thinking: thinking, tools: tools}
	}

	if containsAny(body, "not supported", "unsupported", "unrecognized",
		"unknown parameter", "unknown field", "extra inputs") {
		return degradation{schema: true, thinking: true, tools: true}
	}
	return degradation{}
}

func containsAny(text string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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
