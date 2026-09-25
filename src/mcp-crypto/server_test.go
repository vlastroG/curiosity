package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tempStore -- база во временном каталоге теста: настоящий SQLite, а не заглушка.
func tempStore(t *testing.T) *Store {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("база не открылась: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// frozen -- часы, которые стоят, пока их не подвинут.
type frozen struct{ at time.Time }

func (f *frozen) now() time.Time { return f.at }

var epoch = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// fill пишет ряды цен: по точке в минуту, начиная с start.
func fill(t *testing.T, store *Store, start time.Time, rows map[string][]float64) {
	t.Helper()
	n := 0
	for _, prices := range rows {
		n = len(prices)
	}
	for i := 0; i < n; i++ {
		var quotes []Quote
		for _, symbol := range []string{"BTC", "ETH", "DOGE"} {
			if prices, ok := rows[symbol]; ok {
				quotes = append(quotes, Quote{Symbol: symbol, Price: prices[i]})
			}
		}
		if err := store.SaveRun(context.Background(), start.Add(time.Duration(i)*time.Minute), quotes, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFetchPrices(t *testing.T) {
	var query string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("symbols")
		// биржа отдаёт в своём порядке -- наш порядок должен сохраниться
		w.Write([]byte(`[{"symbol":"ETHUSDT","price":"2677.9"},{"symbol":"BTCUSDT","price":"84600.00000000"}]`))
	}))
	defer api.Close()

	quotes, err := fetchPrices(context.Background(), api.Client(), api.URL, []string{"BTC", "ETH"})
	if err != nil {
		t.Fatal(err)
	}
	if query != `["BTCUSDT","ETHUSDT"]` {
		t.Errorf("список пар ушёл как %s", query)
	}
	if len(quotes) != 2 || quotes[0] != (Quote{"BTC", 84600}) || quotes[1] != (Quote{"ETH", 2677.9}) {
		t.Errorf("цены разобрались как %+v", quotes)
	}
}

func TestFetchPricesErrors(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"код ошибки":   {http.StatusTooManyRequests, `{"msg":"limit"}`, "429"},
		"не та монета": {http.StatusOK, `[{"symbol":"ETHUSDT","price":"1"}]`, "не прислала цену BTC"},
		"мусор в цене": {http.StatusOK, `[{"symbol":"BTCUSDT","price":"abc"}]`, "странная цена"},
		"не json":      {http.StatusOK, `<html>`, "не разобрался"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				w.Write([]byte(c.body))
			}))
			defer api.Close()

			_, err := fetchPrices(context.Background(), api.Client(), api.URL, []string{"BTC"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("ждали ошибку с %q, получили %v", c.want, err)
			}
		})
	}
}

func TestAggregate(t *testing.T) {
	points := []Point{{T: 0, P: 100}, {T: 60, P: 102}, {T: 120, P: 101}, {T: 180, P: 110}}
	stats := aggregate("BTC", points)

	if stats.First != 100 || stats.Last != 110 || stats.Min != 100 || stats.Max != 110 {
		t.Errorf("границы: %+v", stats)
	}
	if stats.Avg != 103.25 {
		t.Errorf("средняя %v", stats.Avg)
	}
	if stats.ChangePct != 10 {
		t.Errorf("изменение %v", stats.ChangePct)
	}
	// самый резкий шаг -- 101 → 110
	if stats.MaxJumpPct != round((110.0-101)/101*100, 4) || stats.MaxJumpAt != stamp(180) {
		t.Errorf("скачок %v в %s", stats.MaxJumpPct, stats.MaxJumpAt)
	}
	if stats.Volatility <= 0 {
		t.Errorf("волатильность %v", stats.Volatility)
	}

	flat := aggregate("ETH", []Point{{T: 0, P: 5}})
	if flat.Volatility != 0 || flat.MaxJumpPct != 0 || flat.ChangePct != 0 {
		t.Errorf("одна точка: %+v", flat)
	}
}

func TestSummaryWindow(t *testing.T) {
	store := tempStore(t)
	// 10 минут истории; в окно 5 минут попадают только последние точки
	fill(t, store, epoch, map[string][]float64{
		"BTC":  {100, 100, 100, 100, 100, 100, 101, 102, 103, 104},
		"ETH":  {50, 50, 50, 50, 50, 50, 49, 48, 48, 47},
		"DOGE": {1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
	})
	if err := store.SaveRun(context.Background(), epoch.Add(9*time.Minute+30*time.Second), nil, errors.New("биржа молчит")); err != nil {
		t.Fatal(err)
	}

	now := epoch.Add(10 * time.Minute)
	out, err := summary(context.Background(), store, []string{"BTC", "ETH", "DOGE"}, 5, now)
	if err != nil {
		t.Fatal(err)
	}

	if len(out.Coins) != 3 || out.Coins[0].Symbol != "BTC" {
		t.Fatalf("монеты: %+v", out.Coins)
	}
	btc := out.Coins[0]
	if btc.Points != 5 || btc.First != 100 || btc.Last != 104 {
		t.Errorf("BTC в окне: %+v", btc)
	}
	if out.Leader != "BTC" || out.Laggard != "ETH" {
		t.Errorf("лидер %q, аутсайдер %q", out.Leader, out.Laggard)
	}
	if out.Runs != 6 || out.FailedRuns != 1 {
		t.Errorf("прогонов %d, неудачных %d", out.Runs, out.FailedRuns)
	}
}

func TestThin(t *testing.T) {
	points := make([]Point, 1000)
	for i := range points {
		points[i] = Point{T: int64(i)}
	}
	out := thin(points, 300)
	if len(out) != 300 || out[0].T != 0 || out[299].T != 999 {
		t.Errorf("прорежено до %d точек, края %d..%d", len(out), out[0].T, out[len(out)-1].T)
	}
	if got := thin(points[:10], 300); len(got) != 10 {
		t.Errorf("короткий ряд тронут: %d", len(got))
	}
}

// Планировщик собирает сразу при старте, потом по таймеру; смена интервала
// перезаводит таймер и переживает перезапуск.
func TestSchedulerRuns(t *testing.T) {
	store := tempStore(t)
	var calls atomic.Int32
	collect := func(context.Context) ([]Quote, error) {
		calls.Add(1)
		return []Quote{{"BTC", 1}}, nil
	}

	sched := newScheduler(store, collect, 30*time.Millisecond, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	waitFor(t, func() bool { return calls.Load() >= 3 })

	// длинный интервал -- таймер перезаведён, новых сборов нет
	if err := sched.SetInterval(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	settled := calls.Load()
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != settled {
		t.Errorf("после смены интервала на час сборы продолжились: %d → %d", settled, calls.Load())
	}
	if next := sched.NextRun(); time.Until(next) < 59*time.Minute {
		t.Errorf("следующий сбор назначен на %s", next)
	}

	// тот же интервал ещё раз -- таймер не трогаем
	before := sched.NextRun()
	sched.SetInterval(context.Background(), time.Hour)
	time.Sleep(20 * time.Millisecond)
	if sched.NextRun() != before {
		t.Errorf("повтор того же интервала перезавёл таймер: %s → %s", before, sched.NextRun())
	}

	// короткий интервал отсчитывается от прошлого сбора: он уже прошёл -- сбор сразу
	sched.SetInterval(context.Background(), time.Second)
	waitFor(t, func() bool { return calls.Load() > settled })

	cancel()
	<-done

	again := newScheduler(store, collect, time.Minute, 0)
	if again.Interval() != time.Second {
		t.Errorf("после перезапуска интервал %s, а сохраняли секунду", again.Interval())
	}
}

// Неудачный сбор записывается и не валит цикл.
func TestSchedulerFailure(t *testing.T) {
	store := tempStore(t)
	sched := newScheduler(store, func(context.Context) ([]Quote, error) {
		return nil, errors.New("биржа недоступна")
	}, time.Minute, 0)

	if _, err := sched.RunOnce(context.Background()); err == nil {
		t.Fatal("ошибка сбора потерялась")
	}
	runs, err := store.Runs(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if runs.Total != 1 || runs.Failed != 1 || runs.LastError != "биржа недоступна" {
		t.Errorf("журнал прогонов: %+v", runs)
	}
}

func TestPrune(t *testing.T) {
	store := tempStore(t)
	fill(t, store, epoch, map[string][]float64{"BTC": {1, 2, 3}})
	if err := store.Prune(context.Background(), epoch.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	lines, err := store.series(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines["BTC"]) != 1 || lines["BTC"][0].P != 3 {
		t.Errorf("после чистки осталось %+v", lines["BTC"])
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("не дождались")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// session поднимает сервер и подключает настоящего клиента по транспорту в памяти:
// так проверяется и регистрация инструментов, и схемы, и превращение ошибок в isError.
func session(t *testing.T, store *Store, sched *Scheduler) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	server := newServer(store, sched, []string{"BTC", "ETH", "DOGE"})
	go server.Run(ctx, serverTransport)

	s, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("соединение не установилось: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	result, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("вызов %s не прошёл: %v", name, err)
	}
	if out != nil && !result.IsError {
		raw, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("structuredContent %s не разобрался: %v", name, err)
		}
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	if text, ok := result.Content[0].(*mcp.TextContent); ok {
		return text.Text
	}
	return ""
}

func TestTools(t *testing.T) {
	store := tempStore(t)
	clock := &frozen{at: epoch.Add(10 * time.Minute)}
	var price atomic.Int64
	price.Store(200)
	sched := newScheduler(store, func(context.Context) ([]Quote, error) {
		return []Quote{{"BTC", float64(price.Load())}, {"ETH", 10}, {"DOGE", 0.1}}, nil
	}, time.Minute, 0)
	sched.now = clock.now
	fill(t, store, epoch, map[string][]float64{
		"BTC": {100, 101, 102, 103, 104, 105, 106, 107, 108, 109},
		"ETH": {10, 10, 10, 10, 10, 10, 10, 10, 10, 10},
	})
	s := session(t, store, sched)

	list, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "crypto_now,crypto_schedule,crypto_series,crypto_summary,list_forecasts,save_forecast" {
		t.Errorf("инструменты: %v", names)
	}

	t.Run("summary", func(t *testing.T) {
		var out SummaryOutput
		result := call(t, s, "crypto_summary", map[string]any{"minutes": 5}, &out)
		if len(out.Coins) != 2 || out.Coins[0].Last != 109 || out.Coins[0].First != 105 {
			t.Errorf("сводка: %+v", out.Coins)
		}
		if text := resultText(result); !strings.Contains(text, "BTC: сейчас 109.0000 $") {
			t.Errorf("текст сводки:\n%s", text)
		}
	})

	t.Run("series", func(t *testing.T) {
		var out SeriesOutput
		call(t, s, "crypto_series", map[string]any{"minutes": 3}, &out)
		if len(out.Lines) != 2 || len(out.Lines[0].Points) != 3 {
			t.Errorf("ряды: %+v", out.Lines)
		}
	})

	t.Run("now", func(t *testing.T) {
		clock.at = clock.at.Add(30 * time.Second)
		var out NowOutput
		call(t, s, "crypto_now", nil, &out)
		if len(out.Quotes) != 3 || out.Quotes[0].Price != 200 {
			t.Errorf("свежие цены: %+v", out.Quotes)
		}
		var sum SummaryOutput
		call(t, s, "crypto_summary", map[string]any{"minutes": 1}, &sum)
		if len(sum.Coins) == 0 || sum.Coins[0].Last != 200 {
			t.Errorf("внеочередной замер не попал в историю: %+v", sum.Coins)
		}
	})

	t.Run("schedule", func(t *testing.T) {
		var out ScheduleOutput
		call(t, s, "crypto_schedule", map[string]any{"interval_minutes": 40}, &out)
		if out.IntervalMinutes != maxInterval {
			t.Errorf("интервал сверх потолка не обрезан: %d", out.IntervalMinutes)
		}
		if out.LastSuccessAt == "" || len(out.Symbols) != 3 {
			t.Errorf("статус: %+v", out)
		}
		call(t, s, "crypto_schedule", nil, &out)
		if out.IntervalMinutes != maxInterval {
			t.Errorf("статус без аргумента поменял интервал: %d", out.IntervalMinutes)
		}
	})

	t.Run("forecasts", func(t *testing.T) {
		bad := call(t, s, "save_forecast", map[string]any{
			"model": "m", "window_minutes": 60, "horizon_minutes": 30, "body": "не json",
		}, nil)
		if !bad.IsError {
			t.Error("прогноз не-json сохранился")
		}

		for i := 1; i <= 2; i++ {
			var saved SaveForecastOutput
			call(t, s, "save_forecast", map[string]any{
				"model": "free", "window_minutes": 60, "horizon_minutes": 30, "body": `{"overview":"n` + string(rune('0'+i)) + `"}`,
			}, &saved)
			if saved.ID != int64(i) {
				t.Errorf("номер прогноза %d", saved.ID)
			}
		}

		var out ListForecastsOutput
		call(t, s, "list_forecasts", map[string]any{"limit": 1}, &out)
		if len(out.Forecasts) != 1 || out.Forecasts[0].ID != 2 || out.Forecasts[0].Model != "free" {
			t.Errorf("история: %+v", out.Forecasts)
		}
	})
}
