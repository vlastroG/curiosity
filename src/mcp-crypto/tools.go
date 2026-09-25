package main

// Инструменты сервера.
//
// Сбор идёт сам по себе, инструменты его только читают и настраивают. Главный
// инструмент для модели -- crypto_summary: он отдаёт не сырые точки, а готовый
// агрегат за окно, и модели не приходится считать минимумы и проценты в уме,
// где она ошибается чаще всего. Сырые точки тоже есть -- crypto_series, -- но
// в первую очередь они нужны интерфейсу для графиков.
//
// Описания инструментов читает модель, по ним она решает, что звать. Схемы
// выводятся из Go-структур, описания полей -- из тегов jsonschema.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Границы параметров.
const (
	defaultWindow   = 60
	maxWindow       = 24 * 60
	minInterval     = 1
	maxInterval     = 15
	defaultLimit    = 10
	maxLimit        = 50
	maxSeriesPoints = 300
)

// WindowInput -- окно анализа.
type WindowInput struct {
	Minutes int `json:"minutes,omitempty" jsonschema:"за сколько последних минут нужны данные, от 1 до 1440; по умолчанию 60"`
}

// SummaryOutput -- агрегат за окно.
type SummaryOutput struct {
	Minutes    int         `json:"minutes" jsonschema:"ширина окна в минутах"`
	From       string      `json:"from" jsonschema:"начало окна, RFC 3339"`
	To         string      `json:"to" jsonschema:"конец окна, RFC 3339"`
	Runs       int         `json:"runs" jsonschema:"сколько сборов планировщика было в окне"`
	FailedRuns int         `json:"failedRuns" jsonschema:"сколько из них не удалось"`
	Coins      []CoinStats `json:"coins" jsonschema:"агрегаты по монетам"`
	Leader     string      `json:"leader,omitempty" jsonschema:"монета с наибольшим ростом за окно"`
	Laggard    string      `json:"laggard,omitempty" jsonschema:"монета с наибольшим падением за окно"`
}

// SeriesLine -- ряд цен одной монеты.
type SeriesLine struct {
	Symbol string  `json:"symbol" jsonschema:"тикер монеты"`
	Points []Point `json:"points" jsonschema:"замеры по возрастанию времени"`
}

// SeriesOutput -- ряды цен за окно.
type SeriesOutput struct {
	Minutes int          `json:"minutes" jsonschema:"ширина окна в минутах"`
	Lines   []SeriesLine `json:"lines" jsonschema:"ряды по монетам"`
}

// NowOutput -- свежие цены.
type NowOutput struct {
	At     string  `json:"at" jsonschema:"момент сбора, RFC 3339"`
	Quotes []Quote `json:"quotes" jsonschema:"цены монет"`
}

// ScheduleInput -- смена интервала.
type ScheduleInput struct {
	IntervalMinutes int `json:"interval_minutes,omitempty" jsonschema:"новый интервал сбора в минутах, от 1 до 15; не указывай, если нужен только статус"`
}

// ScheduleOutput -- состояние планировщика.
type ScheduleOutput struct {
	IntervalMinutes int      `json:"intervalMinutes" jsonschema:"интервал сбора в минутах"`
	Symbols         []string `json:"symbols" jsonschema:"какие монеты собираются"`
	LastRunAt       string   `json:"lastRunAt,omitempty" jsonschema:"последний сбор, RFC 3339"`
	LastSuccessAt   string   `json:"lastSuccessAt,omitempty" jsonschema:"последний удачный сбор, RFC 3339"`
	NextRunAt       string   `json:"nextRunAt,omitempty" jsonschema:"следующий сбор по расписанию, RFC 3339"`
	LastError       string   `json:"lastError,omitempty" jsonschema:"ошибка последнего сбора, если он не удался"`
	RunsLastHour    int      `json:"runsLastHour" jsonschema:"сколько сборов было за последний час"`
}

// SaveForecastInput -- прогноз агента на сохранение.
type SaveForecastInput struct {
	Model          string `json:"model" jsonschema:"модель, которая делала прогноз"`
	WindowMinutes  int    `json:"window_minutes" jsonschema:"сколько минут истории учитывалось"`
	HorizonMinutes int    `json:"horizon_minutes" jsonschema:"на сколько минут вперёд прогноз"`
	Body           string `json:"body" jsonschema:"прогноз json-строкой"`
}

// SaveForecastOutput -- номер сохранённого прогноза.
type SaveForecastOutput struct {
	ID int64 `json:"id" jsonschema:"номер прогноза"`
}

// ListInput -- сколько записей вернуть.
type ListInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"сколько последних прогнозов вернуть, от 1 до 50; по умолчанию 10"`
}

// ListForecastsOutput -- история прогнозов.
type ListForecastsOutput struct {
	Forecasts []Forecast `json:"forecasts" jsonschema:"прогнозы, свежие первыми"`
}

// newServer собирает MCP-сервер поверх хранилища и планировщика.
func newServer(store *Store, sched *Scheduler, symbols []string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-crypto", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "crypto_summary",
		Description: "Сводка по курсам криптовалют за последние N минут: первая и последняя цена, минимум, " +
			"максимум, средняя, изменение в процентах, волатильность и самый резкий скачок по каждой монете. " +
			"Данные собирает фоновый планировщик сервера, это настоящие цены с биржи. " +
			"Вызывай первым, когда нужно понять, что происходит на рынке, или сделать прогноз.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in WindowInput) (*mcp.CallToolResult, SummaryOutput, error) {
		out, err := summary(ctx, store, symbols, clamp(in.Minutes, defaultWindow, 1, maxWindow), sched.now())
		if err != nil {
			return nil, SummaryOutput{}, err
		}
		return textResult(renderSummary(out)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "crypto_series",
		Description: "Сырые замеры цен по монетам за последние N минут, по возрастанию времени. " +
			"Нужны, когда агрегата мало и важна форма движения: разворот, ускорение, плато. " +
			"Длинные ряды прорежены до 300 точек.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in WindowInput) (*mcp.CallToolResult, SeriesOutput, error) {
		minutes := clamp(in.Minutes, defaultWindow, 1, maxWindow)
		lines, err := store.series(ctx, sched.now().Add(-time.Duration(minutes)*time.Minute))
		if err != nil {
			return nil, SeriesOutput{}, err
		}
		out := SeriesOutput{Minutes: minutes, Lines: []SeriesLine{}}
		for _, symbol := range symbols {
			if points := lines[symbol]; len(points) > 0 {
				out.Lines = append(out.Lines, SeriesLine{Symbol: symbol, Points: thin(points, maxSeriesPoints)})
			}
		}
		return textResult(renderSeries(out)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "crypto_now",
		Description: "Собрать цены прямо сейчас, не дожидаясь планировщика, и вернуть их. " +
			"Замер попадает в историю так же, как плановый.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, NowOutput, error) {
		at := sched.now()
		quotes, err := sched.RunOnce(ctx)
		if err != nil {
			return nil, NowOutput{}, err
		}
		out := NowOutput{At: at.UTC().Format(time.RFC3339), Quotes: quotes}
		var text strings.Builder
		fmt.Fprintf(&text, "Цены на %s:\n", out.At)
		for _, quote := range quotes {
			fmt.Fprintf(&text, "  %s: %s $\n", quote.Symbol, money(quote.Price))
		}
		return textResult(text.String()), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "crypto_schedule",
		Description: "Состояние фонового планировщика: интервал сбора, последний и следующий сбор, ошибки. " +
			"С аргументом interval_minutes меняет интервал сбора (от 1 до 15 минут); новый интервал " +
			"сохраняется и переживает перезапуск сервера.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ScheduleInput) (*mcp.CallToolResult, ScheduleOutput, error) {
		if in.IntervalMinutes != 0 {
			minutes := clamp(in.IntervalMinutes, minInterval, minInterval, maxInterval)
			if err := sched.SetInterval(ctx, time.Duration(minutes)*time.Minute); err != nil {
				return nil, ScheduleOutput{}, err
			}
		}
		out, err := status(ctx, store, sched, symbols)
		if err != nil {
			return nil, ScheduleOutput{}, err
		}
		return textResult(renderStatus(out)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "save_forecast",
		Description: "Сохранить прогноз агента в историю. Тело -- json-строка.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SaveForecastInput) (*mcp.CallToolResult, SaveForecastOutput, error) {
		if !json.Valid([]byte(in.Body)) {
			return nil, SaveForecastOutput{}, fmt.Errorf("тело прогноза не json")
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, SaveForecastOutput{}, fmt.Errorf("не указана модель")
		}
		id, err := store.SaveForecast(ctx, Forecast{
			Model:          in.Model,
			WindowMinutes:  in.WindowMinutes,
			HorizonMinutes: in.HorizonMinutes,
			Body:           in.Body,
		}, sched.now())
		if err != nil {
			return nil, SaveForecastOutput{}, err
		}
		return textResult(fmt.Sprintf("Прогноз сохранён под номером %d", id)), SaveForecastOutput{ID: id}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_forecasts",
		Description: "Последние сохранённые прогнозы, свежие первыми.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListInput) (*mcp.CallToolResult, ListForecastsOutput, error) {
		forecasts, err := store.Forecasts(ctx, clamp(in.Limit, defaultLimit, 1, maxLimit))
		if err != nil {
			return nil, ListForecastsOutput{}, err
		}
		var text strings.Builder
		fmt.Fprintf(&text, "Прогнозов: %d\n", len(forecasts))
		for _, f := range forecasts {
			fmt.Fprintf(&text, "\n#%d %s, %s, окно %d мин, горизонт %d мин:\n%s\n",
				f.ID, f.CreatedAt, f.Model, f.WindowMinutes, f.HorizonMinutes, f.Body)
		}
		return textResult(text.String()), ListForecastsOutput{Forecasts: forecasts}, nil
	})

	return server
}

// summary считает агрегат за окно по всем монетам.
func summary(ctx context.Context, store *Store, symbols []string, minutes int, now time.Time) (SummaryOutput, error) {
	from := now.Add(-time.Duration(minutes) * time.Minute)
	lines, err := store.series(ctx, from)
	if err != nil {
		return SummaryOutput{}, err
	}
	runs, err := store.Runs(ctx, from)
	if err != nil {
		return SummaryOutput{}, err
	}

	out := SummaryOutput{
		Minutes:    minutes,
		From:       from.UTC().Format(time.RFC3339),
		To:         now.UTC().Format(time.RFC3339),
		Runs:       runs.Total,
		FailedRuns: runs.Failed,
		Coins:      []CoinStats{},
	}
	for _, symbol := range symbols {
		points := lines[symbol]
		if len(points) == 0 {
			continue
		}
		coin := aggregate(symbol, points)
		out.Coins = append(out.Coins, coin)
	}

	// лидер и аутсайдер имеют смысл, только когда есть с чем сравнивать
	if len(out.Coins) > 1 {
		best, worst := out.Coins[0], out.Coins[0]
		for _, coin := range out.Coins[1:] {
			if coin.ChangePct > best.ChangePct {
				best = coin
			}
			if coin.ChangePct < worst.ChangePct {
				worst = coin
			}
		}
		if best.ChangePct > 0 {
			out.Leader = best.Symbol
		}
		if worst.ChangePct < 0 {
			out.Laggard = worst.Symbol
		}
	}
	return out, nil
}

// status собирает состояние планировщика.
func status(ctx context.Context, store *Store, sched *Scheduler, symbols []string) (ScheduleOutput, error) {
	now := sched.now()
	runs, err := store.Runs(ctx, now.Add(-time.Hour))
	if err != nil {
		return ScheduleOutput{}, err
	}
	out := ScheduleOutput{
		IntervalMinutes: int(sched.Interval() / time.Minute),
		Symbols:         symbols,
		LastError:       runs.LastError,
		RunsLastHour:    runs.Total,
	}
	if !runs.LastAt.IsZero() {
		out.LastRunAt = runs.LastAt.UTC().Format(time.RFC3339)
	}
	if !runs.LastOK.IsZero() {
		out.LastSuccessAt = runs.LastOK.UTC().Format(time.RFC3339)
	}
	if next := sched.NextRun(); !next.IsZero() {
		out.NextRunAt = next.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// thin прореживает ряд до limit точек, сохраняя первую и последнюю.
func thin(points []Point, limit int) []Point {
	if len(points) <= limit {
		return points
	}
	out := make([]Point, 0, limit)
	step := float64(len(points)-1) / float64(limit-1)
	for i := 0; i < limit; i++ {
		out = append(out, points[int(float64(i)*step+0.5)])
	}
	return out
}

func renderSummary(out SummaryOutput) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Сводка за последние %d мин (%s — %s), сборов %d, неудачных %d\n",
		out.Minutes, out.From, out.To, out.Runs, out.FailedRuns)
	if len(out.Coins) == 0 {
		text.WriteString("\nДанных за это окно нет: планировщик ещё не успел ничего собрать.\n")
		return text.String()
	}
	for _, coin := range out.Coins {
		fmt.Fprintf(&text, "\n%s: сейчас %s $, за окно %s (с %s), мин %s, макс %s, средняя %s; "+
			"волатильность %.3f%% за шаг, резче всего %s в %s; замеров %d",
			coin.Symbol, money(coin.Last), signedPct(coin.ChangePct), money(coin.First),
			money(coin.Min), money(coin.Max), money(coin.Avg),
			coin.Volatility, signedPct(coin.MaxJumpPct), coin.MaxJumpAt, coin.Points)
	}
	text.WriteString("\n")
	if out.Leader != "" {
		fmt.Fprintf(&text, "\nЛидер роста: %s", out.Leader)
	}
	if out.Laggard != "" {
		fmt.Fprintf(&text, "\nСильнее всех упала: %s", out.Laggard)
	}
	return text.String()
}

func renderSeries(out SeriesOutput) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Замеры за последние %d мин (время UTC, цена в $)\n", out.Minutes)
	for _, line := range out.Lines {
		fmt.Fprintf(&text, "\n%s:", line.Symbol)
		for _, point := range line.Points {
			fmt.Fprintf(&text, " %s=%s", time.Unix(point.T, 0).UTC().Format("15:04"), money(point.P))
		}
	}
	return text.String()
}

func renderStatus(out ScheduleOutput) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Планировщик: сбор каждые %d мин, монеты %s\n", out.IntervalMinutes, strings.Join(out.Symbols, ", "))
	fmt.Fprintf(&text, "Последний сбор: %s, последний удачный: %s, следующий: %s\n",
		orDash(out.LastRunAt), orDash(out.LastSuccessAt), orDash(out.NextRunAt))
	fmt.Fprintf(&text, "Сборов за час: %d", out.RunsLastHour)
	if out.LastError != "" {
		fmt.Fprintf(&text, "\nПоследний сбор не удался: %s", out.LastError)
	}
	return text.String()
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// money -- цена с разумным числом знаков: у биткоина центы, у мелких монет -- шесть знаков.
func money(value float64) string {
	switch {
	case value >= 1000:
		return fmt.Sprintf("%.2f", value)
	case value >= 1:
		return fmt.Sprintf("%.4f", value)
	default:
		return fmt.Sprintf("%.6f", value)
	}
}

func signedPct(value float64) string {
	if value > 0 {
		return fmt.Sprintf("+%.3f%%", value)
	}
	return fmt.Sprintf("%.3f%%", value)
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

// clamp -- значение в границах, ноль означает «по умолчанию».
func clamp(value, fallback, low, high int) int {
	if value == 0 {
		return fallback
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
