// Package agent -- один прогон прогноза: модель берёт данные у MCP-сервера
// через инструменты и возвращает прогноз по монетам.
//
//	агент ──► модель: «окно 60 мин, горизонт 30 мин, сделай прогноз»
//	      ◄── заявка crypto_summary{minutes:60}
//	      ──► MCP ──► агрегат ──► модели
//	      ◄── (может попросить crypto_series, чтобы увидеть форму движения)
//	      ◄── json: overview + прогноз по каждой монете
//
// Модель не видит ни сохранения прогноза, ни смены расписания: это делает код.
// Модели отдаём только то, что нужно для решения, -- чтение данных.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"crypto-watch/internal/llm"
)

// modelTools -- инструменты MCP-сервера, которые видит модель.
var modelTools = []string{"crypto_summary", "crypto_series"}

// maxSteps -- сколько раз модель может сходить за инструментами. Двух хватает:
// агрегат и, если хочется, ряды. Больше -- признак того, что модель ходит по кругу.
const maxSteps = 5

// Chatter -- то, что умеет звать модель.
type Chatter interface {
	Chat(ctx context.Context, p llm.Provider, req llm.Request) (llm.Response, error)
}

// ToolBox -- то, что умеет звать MCP-сервер.
type ToolBox interface {
	Tools(ctx context.Context, names ...string) ([]llm.Tool, error)
	Text(ctx context.Context, name, arguments string) (string, error)
	Call(ctx context.Context, name string, args any, out any) error
}

// Forecaster делает прогнозы.
type Forecaster struct {
	chat      Chatter
	providers map[string]llm.Provider
	tools     ToolBox
	horizon   int
	now       func() time.Time
}

// New собирает прогнозиста. horizon -- на сколько минут вперёд прогноз.
func New(chat Chatter, providers map[string]llm.Provider, tools ToolBox, horizon int) *Forecaster {
	return &Forecaster{chat: chat, providers: providers, tools: tools, horizon: horizon, now: time.Now}
}

// Horizon -- горизонт прогноза в минутах.
func (f *Forecaster) Horizon() int { return f.horizon }

// Available -- есть ли ключ у провайдера модели.
func (f *Forecaster) Available(model Model) bool {
	return f.providers[model.Provider].Available()
}

// CoinForecast -- прогноз по одной монете.
type CoinForecast struct {
	Symbol     string  `json:"symbol"`
	Trend      string  `json:"trend"`
	Price      float64 `json:"price"`
	Low        float64 `json:"low"`
	High       float64 `json:"high"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// Step -- что модель делала по дороге к ответу. Видно в интерфейсе.
type Step struct {
	Tool      string `json:"tool"`
	Arguments string `json:"arguments"`
}

// Result -- готовый прогноз. Он же тело, которое уходит в save_forecast.
type Result struct {
	CreatedAt      string         `json:"createdAt"`
	Model          string         `json:"model"`
	WindowMinutes  int            `json:"windowMinutes"`
	HorizonMinutes int            `json:"horizonMinutes"`
	Overview       string         `json:"overview"`
	Coins          []CoinForecast `json:"coins"`
	Steps          []Step         `json:"steps"`
	Mode           string         `json:"mode"`
	Tokens         int            `json:"tokens"`
	DurationMs     int64          `json:"durationMs"`
}

// summary -- та часть crypto_summary, которая нужна агенту.
type summary struct {
	Coins []struct {
		Symbol string  `json:"symbol"`
		Last   float64 `json:"last"`
		Points int     `json:"points"`
	} `json:"coins"`
}

// ErrNoData -- у сервера ещё нет замеров за окно, прогнозировать не по чему.
var ErrNoData = errors.New("у MCP-сервера пока нет данных за окно")

// Run делает один прогноз.
func (f *Forecaster) Run(ctx context.Context, model Model, window int) (Result, error) {
	started := f.now()
	provider, ok := f.providers[model.Provider]
	if !ok || !provider.Available() {
		return Result{}, fmt.Errorf("у модели %s нет ключа провайдера", model.ID)
	}

	// сводку берёт и сам код: из неё цены на момент прогноза, по ней же проверяется
	// ответ модели -- монет, которых нет в данных, в прогнозе быть не должно
	var basis summary
	if err := f.tools.Call(ctx, "crypto_summary", map[string]any{"minutes": window}, &basis); err != nil {
		return Result{}, err
	}
	if len(basis.Coins) == 0 {
		return Result{}, ErrNoData
	}
	symbols := make([]string, len(basis.Coins))
	for i, coin := range basis.Coins {
		symbols[i] = coin.Symbol
	}

	result := Result{
		Model:          model.ID,
		WindowMinutes:  window,
		HorizonMinutes: f.horizon,
		Steps:          []Step{},
		Mode:           "tools",
	}

	var tools []llm.Tool
	if model.Tools {
		var err error
		if tools, err = f.tools.Tools(ctx, modelTools...); err != nil {
			return Result{}, err
		}
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt(f.horizon)},
		{Role: llm.RoleUser, Content: fmt.Sprintf(
			"Сейчас %s UTC. Монеты: %s. Окно анализа — последние %d мин, горизонт прогноза — %d мин. "+
				"Возьми данные инструментами и верни прогноз.",
			f.now().UTC().Format("2006-01-02 15:04"), strings.Join(symbols, ", "), window, f.horizon)},
	}

	// без инструментов данные приходят в промпте -- модель всё равно должна опираться на факты
	if len(tools) == 0 {
		result.Mode = "prompt"
		data, err := f.tools.Text(ctx, "crypto_summary", fmt.Sprintf(`{"minutes":%d}`, window))
		if err != nil {
			return Result{}, err
		}
		messages[1].Content = fmt.Sprintf(
			"Сейчас %s UTC. Окно анализа — последние %d мин, горизонт прогноза — %d мин. Данные:\n\n%s\n\nВерни прогноз.",
			f.now().UTC().Format("2006-01-02 15:04"), window, f.horizon, data)
	}

	var parsed forecastJSON
	nudged, retried := false, false
	for step := 0; ; step++ {
		req := llm.Request{Model: model.ID, Messages: messages}
		// на последнем шаге инструменты не даём: пора отвечать
		if step < maxSteps {
			req.Tools = tools
		}
		resp, err := f.chat.Chat(ctx, provider, req)
		if err != nil {
			return Result{}, err
		}
		result.Tokens += resp.Usage.PromptTokens + resp.Usage.CompletionTokens

		if len(resp.ToolCalls) > 0 && step < maxSteps {
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls})
			for _, call := range resp.ToolCalls {
				result.Steps = append(result.Steps, Step{Tool: call.Function.Name, Arguments: call.Function.Arguments})
				answer := "Такого инструмента нет. Доступны: " + strings.Join(modelTools, ", ")
				if allowed(call.Function.Name) {
					if answer, err = f.tools.Text(ctx, call.Function.Name, call.Function.Arguments); err != nil {
						return Result{}, err
					}
				}
				messages = append(messages, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: answer})
			}
			continue
		}

		// модель ответила, не заглянув в данные, -- прогноз из головы нам не нужен
		if len(tools) > 0 && len(result.Steps) == 0 && !nudged {
			nudged = true
			messages = append(messages,
				llm.Message{Role: llm.RoleAssistant, Content: resp.Text},
				llm.Message{Role: llm.RoleUser, Content: "Ты не посмотрел данные. Вызови crypto_summary и только потом отвечай."})
			continue
		}

		parsed, err = parseForecast(resp.Text)
		if err == nil {
			break
		}
		if retried {
			return Result{}, fmt.Errorf("модель дважды ответила не по формату: %w", err)
		}
		retried = true
		messages = append(messages,
			llm.Message{Role: llm.RoleAssistant, Content: resp.Text},
			llm.Message{Role: llm.RoleUser, Content: "Ответ не разобрался: " + err.Error() + ". Верни только JSON по формату из инструкции, без текста вокруг."})
	}

	result.Overview = strings.TrimSpace(parsed.Overview)
	result.Coins = normalize(parsed.Coins, basis)
	if len(result.Coins) == 0 {
		return Result{}, fmt.Errorf("в прогнозе модели нет ни одной известной монеты")
	}
	result.CreatedAt = f.now().UTC().Format(time.RFC3339)
	result.DurationMs = f.now().Sub(started).Milliseconds()
	return result, nil
}

// Save отдаёт прогноз MCP-серверу на хранение.
func (f *Forecaster) Save(ctx context.Context, result Result) (int64, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	err = f.tools.Call(ctx, "save_forecast", map[string]any{
		"model":           result.Model,
		"window_minutes":  result.WindowMinutes,
		"horizon_minutes": result.HorizonMinutes,
		"body":            string(body),
	}, &out)
	return out.ID, err
}

func allowed(name string) bool {
	for _, tool := range modelTools {
		if tool == name {
			return true
		}
	}
	return false
}

func systemPrompt(horizon int) string {
	return fmt.Sprintf(`Ты аналитик криптовалютного рынка в учебном проекте. Твоя задача — короткий прогноз цен на ближайшие %d минут по свежим данным с биржи.

Как работать:
1. Вызови crypto_summary с окном, которое назвал пользователь. Там по каждой монете цена, изменение, минимум, максимум, волатильность и самый резкий скачок.
2. Если нужна форма движения (разворот, ускорение, плато), вызови crypto_series с тем же окном.
3. Сделай прогноз диапазона цены через %d минут для каждой монеты из данных.

Правила:
- Опирайся только на данные инструментов. Не выдумывай новости и события.
- Диапазон low..high реалистичный: ширину оценивай по волатильности и размаху за окно.
- confidence от 0 до 1. Данных мало или движение рваное — уверенность низкая, так и пиши.
- Это учебный прогноз, не инвестиционный совет. Советов купить или продать не давай.
- Пиши по-русски, коротко и по делу.

Ответ — только JSON, без markdown и текста вокруг:
{"overview":"2–4 предложения: что происходило за окно и чего ждать","coins":[{"symbol":"BTC","trend":"up|down|flat","low":0,"high":0,"confidence":0.5,"reason":"одно предложение, почему"}]}`, horizon, horizon)
}

type forecastJSON struct {
	Overview string    `json:"overview"`
	Coins    []rawCoin `json:"coins"`
}

// rawCoin -- прогноз по монете, как его вернула модель.
type rawCoin struct {
	Symbol     string  `json:"symbol"`
	Trend      string  `json:"trend"`
	Low        float64 `json:"low"`
	High       float64 `json:"high"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// parseForecast достаёт JSON из ответа модели. Модели любят обернуть его в ```json
// или предварить фразой -- берём от первой { до последней }.
func parseForecast(text string) (forecastJSON, error) {
	var out forecastJSON
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return out, errors.New("в ответе нет JSON")
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return out, fmt.Errorf("JSON не разобрался: %v", err)
	}
	if strings.TrimSpace(out.Overview) == "" {
		return out, errors.New("нет поля overview")
	}
	if len(out.Coins) == 0 {
		return out, errors.New("пустой список coins")
	}
	return out, nil
}

// normalize приводит прогноз модели в порядок: только монеты из данных, в порядке
// данных, тренд из трёх значений, уверенность в [0, 1], low не выше high,
// цена на момент прогноза -- из данных, а не со слов модели.
func normalize(coins []rawCoin, basis summary) []CoinForecast {
	bySymbol := map[string]int{}
	for i, coin := range coins {
		bySymbol[strings.ToUpper(strings.TrimSpace(coin.Symbol))] = i
	}

	out := []CoinForecast{}
	for _, known := range basis.Coins {
		i, ok := bySymbol[known.Symbol]
		if !ok {
			continue
		}
		coin := coins[i]
		trend := strings.ToLower(strings.TrimSpace(coin.Trend))
		if trend != "up" && trend != "down" {
			trend = "flat"
		}
		low, high := coin.Low, coin.High
		if low > high {
			low, high = high, low
		}
		out = append(out, CoinForecast{
			Symbol:     known.Symbol,
			Trend:      trend,
			Price:      known.Last,
			Low:        low,
			High:       high,
			Confidence: math.Max(0, math.Min(1, coin.Confidence)),
			Reason:     strings.TrimSpace(coin.Reason),
		})
	}
	return out
}
