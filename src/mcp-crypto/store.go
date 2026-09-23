package main

// Хранилище -- SQLite в одном файле.
//
// Планировщик пишет сюда каждую минуту, инструменты читают отсюда агрегаты. Файл
// лежит на томе контейнера, поэтому перезапуск не обнуляет ни историю цен, ни
// выбранный интервал, ни прогнозы.
//
// Драйвер modernc.org/sqlite -- SQLite, переписанный на Go. С ним бинарь остаётся
// статическим, без cgo, как и у остальных серверов проекта.
//
// Время хранится секундами Unix: окна «последние N минут» -- это сравнение чисел,
// и никаких разночтений с часовыми поясами.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS runs (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	taken_at INTEGER NOT NULL,
	ok       INTEGER NOT NULL,
	error    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS prices (
	run_id   INTEGER NOT NULL REFERENCES runs(id),
	symbol   TEXT    NOT NULL,
	price    REAL    NOT NULL,
	taken_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS prices_symbol_time ON prices(symbol, taken_at);
CREATE INDEX IF NOT EXISTS runs_time ON runs(taken_at);
CREATE TABLE IF NOT EXISTS forecasts (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	created_at      INTEGER NOT NULL,
	model           TEXT    NOT NULL,
	window_minutes  INTEGER NOT NULL,
	horizon_minutes INTEGER NOT NULL,
	body            TEXT    NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// Store -- доступ к базе.
type Store struct {
	db *sql.DB
}

// openStore открывает файл базы и создаёт таблицы, если их нет.
func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// писатель у SQLite всё равно один; одно соединение избавляет от «database is locked»
	// между планировщиком и инструментами
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("схема базы не создалась: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// SaveRun записывает прогон планировщика: удачный с ценами или неудачный с причиной.
func (s *Store) SaveRun(ctx context.Context, at time.Time, quotes []Quote, runErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ok, reason := 1, ""
	if runErr != nil {
		ok, reason = 0, runErr.Error()
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO runs(taken_at, ok, error) VALUES (?, ?, ?)`, at.Unix(), ok, reason)
	if err != nil {
		return err
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	for _, quote := range quotes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO prices(run_id, symbol, price, taken_at) VALUES (?, ?, ?, ?)`,
			runID, quote.Symbol, quote.Price, at.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Prune удаляет цены и прогоны старше границы. Прогнозы не трогает: их мало,
// и история прогнозов ценнее истории цен.
func (s *Store) Prune(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM prices WHERE taken_at < ?`, before.Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE taken_at < ?`, before.Unix())
	return err
}

// Setting читает настройку; ok=false, если её ещё не задавали.
func (s *Store) Setting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetSetting сохраняет настройку.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// Interval -- интервал сбора из настроек или fallback, если его не задавали.
func (s *Store) Interval(ctx context.Context, fallback time.Duration) time.Duration {
	value, ok, err := s.Setting(ctx, "interval_seconds")
	if err != nil || !ok {
		return fallback
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// SetInterval сохраняет интервал сбора.
func (s *Store) SetInterval(ctx context.Context, interval time.Duration) error {
	return s.SetSetting(ctx, "interval_seconds", strconv.Itoa(int(interval.Seconds())))
}

// RunStats -- что известно о прогонах планировщика.
type RunStats struct {
	Total     int
	Failed    int
	LastOK    time.Time
	LastError string
	LastAt    time.Time
}

// Runs -- статистика прогонов с момента since.
func (s *Store) Runs(ctx context.Context, since time.Time) (RunStats, error) {
	var stats RunStats
	var failed sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(ok = 0) FROM runs WHERE taken_at >= ?`, since.Unix()).Scan(&stats.Total, &failed)
	if err != nil {
		return stats, err
	}
	stats.Failed = int(failed.Int64)

	var lastOK sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(taken_at) FROM runs WHERE ok = 1`).Scan(&lastOK); err != nil {
		return stats, err
	}
	if lastOK.Valid {
		stats.LastOK = time.Unix(lastOK.Int64, 0)
	}

	var lastAt int64
	var lastErr string
	var lastOKFlag int
	err = s.db.QueryRowContext(ctx, `SELECT taken_at, ok, error FROM runs ORDER BY id DESC LIMIT 1`).
		Scan(&lastAt, &lastOKFlag, &lastErr)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return stats, err
	}
	if err == nil {
		stats.LastAt = time.Unix(lastAt, 0)
		if lastOKFlag == 0 {
			stats.LastError = lastErr
		}
	}
	return stats, nil
}

// Point -- одна точка ряда цен.
type Point struct {
	T int64   `json:"t" jsonschema:"момент сбора, секунды Unix"`
	P float64 `json:"p" jsonschema:"цена в долларах"`
}

// series -- ряды цен по монетам с момента since, по возрастанию времени.
func (s *Store) series(ctx context.Context, since time.Time) (map[string][]Point, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT symbol, taken_at, price FROM prices WHERE taken_at >= ? ORDER BY taken_at, rowid`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]Point{}
	for rows.Next() {
		var symbol string
		var point Point
		if err := rows.Scan(&symbol, &point.T, &point.P); err != nil {
			return nil, err
		}
		out[symbol] = append(out[symbol], point)
	}
	return out, rows.Err()
}

// CoinStats -- агрегат по одной монете за окно.
type CoinStats struct {
	Symbol     string  `json:"symbol" jsonschema:"тикер монеты"`
	Points     int     `json:"points" jsonschema:"сколько замеров попало в окно"`
	First      float64 `json:"first" jsonschema:"цена в начале окна"`
	Last       float64 `json:"last" jsonschema:"последняя цена"`
	Min        float64 `json:"min" jsonschema:"минимум за окно"`
	Max        float64 `json:"max" jsonschema:"максимум за окно"`
	Avg        float64 `json:"avg" jsonschema:"средняя цена за окно"`
	ChangePct  float64 `json:"changePct" jsonschema:"изменение от первой цены до последней, в процентах"`
	RangePct   float64 `json:"rangePct" jsonschema:"размах (макс - мин) относительно средней, в процентах"`
	Volatility float64 `json:"volatility" jsonschema:"стандартное отклонение изменений между соседними замерами, в процентах"`
	MaxJumpPct float64 `json:"maxJumpPct" jsonschema:"самое резкое изменение между соседними замерами, в процентах, со знаком"`
	MaxJumpAt  string  `json:"maxJumpAt,omitempty" jsonschema:"когда был самый резкий скачок, RFC 3339"`
	FirstAt    string  `json:"firstAt" jsonschema:"время первого замера в окне, RFC 3339"`
	LastAt     string  `json:"lastAt" jsonschema:"время последнего замера, RFC 3339"`
}

// aggregate считает агрегат по ряду одной монеты. Ряд не пустой и отсортирован.
func aggregate(symbol string, points []Point) CoinStats {
	stats := CoinStats{
		Symbol:  symbol,
		Points:  len(points),
		First:   points[0].P,
		Last:    points[len(points)-1].P,
		Min:     points[0].P,
		Max:     points[0].P,
		FirstAt: stamp(points[0].T),
		LastAt:  stamp(points[len(points)-1].T),
	}

	sum := 0.0
	for _, point := range points {
		sum += point.P
		stats.Min = math.Min(stats.Min, point.P)
		stats.Max = math.Max(stats.Max, point.P)
	}
	stats.Avg = sum / float64(len(points))
	stats.ChangePct = pct(stats.Last, stats.First)
	stats.RangePct = (stats.Max - stats.Min) / stats.Avg * 100

	// волатильность считаем по шагам, а не по ценам: разброс цен вокруг средней
	// у растущей монеты большой, даже если она растёт ровно, как по линейке
	if len(points) > 1 {
		steps := make([]float64, 0, len(points)-1)
		for i := 1; i < len(points); i++ {
			step := pct(points[i].P, points[i-1].P)
			steps = append(steps, step)
			if math.Abs(step) > math.Abs(stats.MaxJumpPct) {
				stats.MaxJumpPct = step
				stats.MaxJumpAt = stamp(points[i].T)
			}
		}
		mean := 0.0
		for _, step := range steps {
			mean += step
		}
		mean /= float64(len(steps))
		variance := 0.0
		for _, step := range steps {
			variance += (step - mean) * (step - mean)
		}
		stats.Volatility = math.Sqrt(variance / float64(len(steps)))
	}

	stats.ChangePct = round(stats.ChangePct, 4)
	stats.RangePct = round(stats.RangePct, 4)
	stats.Volatility = round(stats.Volatility, 4)
	stats.MaxJumpPct = round(stats.MaxJumpPct, 4)
	return stats
}

func pct(now, before float64) float64 {
	if before == 0 {
		return 0
	}
	return (now - before) / before * 100
}

func round(value float64, digits int) float64 {
	scale := math.Pow(10, float64(digits))
	return math.Round(value*scale) / scale
}

func stamp(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

// Forecast -- сохранённый прогноз агента.
type Forecast struct {
	ID             int64  `json:"id" jsonschema:"номер прогноза"`
	CreatedAt      string `json:"createdAt" jsonschema:"когда сделан, RFC 3339"`
	Model          string `json:"model" jsonschema:"какая модель делала прогноз"`
	WindowMinutes  int    `json:"windowMinutes" jsonschema:"сколько минут истории учитывалось"`
	HorizonMinutes int    `json:"horizonMinutes" jsonschema:"на сколько минут вперёд прогноз"`
	Body           string `json:"body" jsonschema:"сам прогноз, json-строка в формате агента"`
}

// SaveForecast сохраняет прогноз и возвращает его номер.
func (s *Store) SaveForecast(ctx context.Context, f Forecast, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO forecasts(created_at, model, window_minutes, horizon_minutes, body) VALUES (?, ?, ?, ?, ?)`,
		at.Unix(), f.Model, f.WindowMinutes, f.HorizonMinutes, f.Body)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Forecasts -- последние прогнозы, свежие первыми.
func (s *Store) Forecasts(ctx context.Context, limit int) ([]Forecast, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, model, window_minutes, horizon_minutes, body FROM forecasts ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Forecast{}
	for rows.Next() {
		var f Forecast
		var created int64
		if err := rows.Scan(&f.ID, &created, &f.Model, &f.WindowMinutes, &f.HorizonMinutes, &f.Body); err != nil {
			return nil, err
		}
		f.CreatedAt = stamp(created)
		out = append(out, f)
	}
	return out, rows.Err()
}
