package main

// План поездки и его хранилище.
//
// План собирается по частям: создали, записали бюджет, добавили дни, опубликовали.
// Каждая запись проверяется на входе -- даты внутри поездки, длины текстов,
// ссылки только http(s), -- потому что пишет её модель, а текст частично взят из
// Википедии. Сервер доверяет форме данных, а не тому, кто их прислал.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Ограничения плана.
const (
	maxTripDays       = 14
	maxActivities     = 6
	maxNotes          = 8
	maxTitleRunes     = 150
	maxTextRunes      = 700
	maxNoteRunes      = 300
	maxSummaryRunes   = 1200
	maxWeatherRunes   = 200
	maxCategoryShares = 8
)

// Trip -- план поездки.
type Trip struct {
	ID          string   `json:"id"`
	City        string   `json:"city"`
	Country     string   `json:"country"`
	CountryCode string   `json:"countryCode"`
	Latitude    float64  `json:"latitude"`
	Longitude   float64  `json:"longitude"`
	StartDate   string   `json:"startDate"`
	EndDate     string   `json:"endDate"`
	Travelers   int      `json:"travelers"`
	Notes       []string `json:"notes"`
	Budget      *Budget  `json:"budget,omitempty"`
	Days        []Day    `json:"days"`
	Summary     string   `json:"summary,omitempty"`
	File        string   `json:"file,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	PublishedAt string   `json:"publishedAt,omitempty"`
}

// Budget -- бюджет в валюте пользователя и в местной.
type Budget struct {
	Total           float64    `json:"total" jsonschema:"бюджет в валюте пользователя"`
	Currency        string     `json:"currency" jsonschema:"валюта пользователя, например RUB"`
	LocalTotal      float64    `json:"local_total,omitempty" jsonschema:"тот же бюджет в местной валюте -- из convert сервера денег"`
	LocalCurrency   string     `json:"local_currency,omitempty" jsonschema:"местная валюта -- из country_currency"`
	Rate            float64    `json:"rate,omitempty" jsonschema:"курс: единиц местной валюты за единицу валюты пользователя -- из convert"`
	RateDate        string     `json:"rate_date,omitempty" jsonschema:"дата курса -- из convert"`
	PerPersonPerDay float64    `json:"per_person_per_day,omitempty" jsonschema:"на человека в день в местной валюте -- из budget_split"`
	Categories      []Category `json:"categories,omitempty" jsonschema:"статьи расходов -- из budget_split"`
}

// Category -- статья бюджета.
type Category struct {
	Name   string  `json:"name" jsonschema:"статья"`
	Amount float64 `json:"amount" jsonschema:"сумма в местной валюте"`
}

// Day -- день поездки.
type Day struct {
	Date       string     `json:"date" jsonschema:"дата, ГГГГ-ММ-ДД, внутри поездки"`
	Weather    Weather    `json:"weather" jsonschema:"погода этого дня -- из trip_weather сервера погоды"`
	Activities []Activity `json:"activities" jsonschema:"что делать в этот день, 1–6 пунктов"`
	Notes      string     `json:"notes,omitempty" jsonschema:"совет на день: что взять, как одеться"`
}

// Weather -- погода дня в плане.
type Weather struct {
	Summary      string  `json:"summary" jsonschema:"погода словами"`
	Min          float64 `json:"min" jsonschema:"минимум, °C"`
	Max          float64 `json:"max" jsonschema:"максимум, °C"`
	PrecipChance int     `json:"precip_chance" jsonschema:"вероятность осадков, %"`
	Source       string  `json:"source" jsonschema:"forecast или climate -- как вернул trip_weather"`
}

// Activity -- пункт дня.
type Activity struct {
	Time        string `json:"time,omitempty" jsonschema:"утро, день или вечер"`
	Title       string `json:"title" jsonschema:"что: место или занятие"`
	Description string `json:"description,omitempty" jsonschema:"коротко, почему стоит сходить -- по описанию из sight_info"`
	URL         string `json:"url,omitempty" jsonschema:"ссылка на статью о месте -- из sight_info"`
}

// Store -- планы в JSON-файлах, по файлу на план.
type Store struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

// ErrInvalid -- запись не прошла проверку.
var ErrInvalid = errors.New("запись не принята")

var idPattern = regexp.MustCompile(`^trip_[0-9a-f]{10}$`)

func newStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, now: time.Now}, nil
}

func (s *Store) path(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("%w: %q не похоже на trip_id -- бери его из trip_create", ErrInvalid, id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Get читает план.
func (s *Store) Get(id string) (Trip, error) {
	path, err := s.path(id)
	if err != nil {
		return Trip{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Trip{}, fmt.Errorf("%w: плана %s нет", ErrInvalid, id)
	}
	if err != nil {
		return Trip{}, err
	}
	var trip Trip
	if err := json.Unmarshal(raw, &trip); err != nil {
		return Trip{}, fmt.Errorf("план %s повреждён: %w", id, err)
	}
	return trip, nil
}

func (s *Store) save(trip Trip) error {
	path, err := s.path(trip.ID)
	if err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(trip, "", "  ")
	if err := os.WriteFile(path+".tmp", raw, 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// update читает план, меняет его и сохраняет -- под общей блокировкой.
func (s *Store) update(id string, change func(*Trip) error) (Trip, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	trip, err := s.Get(id)
	if err != nil {
		return Trip{}, err
	}
	if trip.File != "" {
		return Trip{}, fmt.Errorf("%w: план %s уже опубликован, менять его нельзя", ErrInvalid, id)
	}
	if err := change(&trip); err != nil {
		return Trip{}, err
	}
	return trip, s.save(trip)
}

// CreateInput -- аргументы trip_create.
type CreateInput struct {
	City        string   `json:"city" jsonschema:"город -- как вернул find_city"`
	Country     string   `json:"country" jsonschema:"страна -- из find_city"`
	CountryCode string   `json:"country_code" jsonschema:"код страны -- из find_city"`
	Latitude    float64  `json:"latitude" jsonschema:"широта -- из find_city"`
	Longitude   float64  `json:"longitude" jsonschema:"долгота -- из find_city"`
	StartDate   string   `json:"start_date" jsonschema:"первый день, ГГГГ-ММ-ДД"`
	EndDate     string   `json:"end_date" jsonschema:"последний день, ГГГГ-ММ-ДД"`
	Travelers   int      `json:"travelers" jsonschema:"сколько человек, от 1 до 10"`
	Notes       []string `json:"notes,omitempty" jsonschema:"допущения, которые пришлось сделать, например «даты не указаны -- взяты через неделю»"`
}

// Create заводит план.
func (s *Store) Create(in CreateInput) (Trip, error) {
	start, end, err := parseRange(in.StartDate, in.EndDate)
	if err != nil {
		return Trip{}, err
	}
	if in.Travelers < 1 || in.Travelers > 10 {
		return Trip{}, fmt.Errorf("%w: путешественников от 1 до 10, а не %d", ErrInvalid, in.Travelers)
	}
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		return Trip{}, fmt.Errorf("%w: координаты вне Земли", ErrInvalid)
	}
	city := text(in.City, 100)
	if city == "" {
		return Trip{}, fmt.Errorf("%w: не указан город", ErrInvalid)
	}
	notes, err := notesList(in.Notes)
	if err != nil {
		return Trip{}, err
	}

	random := make([]byte, 5)
	rand.Read(random)
	trip := Trip{
		ID: "trip_" + hex.EncodeToString(random), City: city, Country: text(in.Country, 100),
		CountryCode: strings.ToUpper(text(in.CountryCode, 2)), Latitude: in.Latitude, Longitude: in.Longitude,
		StartDate: start.Format(time.DateOnly), EndDate: end.Format(time.DateOnly), Travelers: in.Travelers,
		Notes: notes, Days: []Day{}, CreatedAt: s.now().UTC().Format(time.RFC3339),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return trip, s.save(trip)
}

// SetBudget записывает бюджет.
func (s *Store) SetBudget(id string, b Budget) (Trip, error) {
	if b.Total <= 0 || b.Total > 1e12 || b.LocalTotal < 0 || b.PerPersonPerDay < 0 || b.Rate < 0 {
		return Trip{}, fmt.Errorf("%w: суммы должны быть положительными", ErrInvalid)
	}
	if !currencyCode(b.Currency) || (b.LocalCurrency != "" && !currencyCode(b.LocalCurrency)) {
		return Trip{}, fmt.Errorf("%w: код валюты -- три латинские буквы", ErrInvalid)
	}
	if len(b.Categories) > maxCategoryShares {
		return Trip{}, fmt.Errorf("%w: статей расходов не больше %d", ErrInvalid, maxCategoryShares)
	}
	b.Currency, b.LocalCurrency = strings.ToUpper(b.Currency), strings.ToUpper(b.LocalCurrency)
	b.RateDate = text(b.RateDate, 10)
	for i := range b.Categories {
		b.Categories[i].Name = text(b.Categories[i].Name, 60)
	}
	return s.update(id, func(t *Trip) error {
		t.Budget = &b
		return nil
	})
}

// AddDay записывает день; повторная запись той же даты заменяет её.
func (s *Store) AddDay(id string, day Day) (Trip, error) {
	date, err := time.Parse(time.DateOnly, strings.TrimSpace(day.Date))
	if err != nil {
		return Trip{}, fmt.Errorf("%w: дата %q не в формате ГГГГ-ММ-ДД", ErrInvalid, day.Date)
	}
	if len(day.Activities) == 0 || len(day.Activities) > maxActivities {
		return Trip{}, fmt.Errorf("%w: в дне от 1 до %d пунктов", ErrInvalid, maxActivities)
	}
	day.Date = date.Format(time.DateOnly)
	day.Notes = text(day.Notes, maxNoteRunes)
	day.Weather.Summary = text(day.Weather.Summary, maxWeatherRunes)
	if day.Weather.Source != "forecast" && day.Weather.Source != "climate" {
		day.Weather.Source = "climate"
	}
	for i := range day.Activities {
		a := &day.Activities[i]
		a.Title = text(a.Title, maxTitleRunes)
		a.Description = text(a.Description, maxTextRunes)
		a.Time = text(a.Time, 20)
		if a.Title == "" {
			return Trip{}, fmt.Errorf("%w: у пункта %d нет названия", ErrInvalid, i+1)
		}
		if a.URL != "" && !httpURL(a.URL) {
			return Trip{}, fmt.Errorf("%w: ссылка должна начинаться с http:// или https://", ErrInvalid)
		}
	}
	return s.update(id, func(t *Trip) error {
		start, _ := time.Parse(time.DateOnly, t.StartDate)
		end, _ := time.Parse(time.DateOnly, t.EndDate)
		if date.Before(start) || date.After(end) {
			return fmt.Errorf("%w: %s вне поездки %s — %s", ErrInvalid, day.Date, t.StartDate, t.EndDate)
		}
		kept := t.Days[:0]
		for _, d := range t.Days {
			if d.Date != day.Date {
				kept = append(kept, d)
			}
		}
		t.Days = append(kept, day)
		sort.Slice(t.Days, func(i, j int) bool { return t.Days[i].Date < t.Days[j].Date })
		return nil
	})
}

// Missing -- дни поездки, которых ещё нет в плане.
func (t Trip) Missing() []string {
	start, _ := time.Parse(time.DateOnly, t.StartDate)
	end, _ := time.Parse(time.DateOnly, t.EndDate)
	have := map[string]bool{}
	for _, d := range t.Days {
		have[d.Date] = true
	}
	var missing []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if key := d.Format(time.DateOnly); !have[key] {
			missing = append(missing, key)
		}
	}
	return missing
}

func parseRange(from, to string) (time.Time, time.Time, error) {
	start, err := time.Parse(time.DateOnly, strings.TrimSpace(from))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: start_date %q не в формате ГГГГ-ММ-ДД", ErrInvalid, from)
	}
	end, err := time.Parse(time.DateOnly, strings.TrimSpace(to))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: end_date %q не в формате ГГГГ-ММ-ДД", ErrInvalid, to)
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: поездка кончается раньше, чем начинается", ErrInvalid)
	}
	if days := int(end.Sub(start).Hours()/24) + 1; days > maxTripDays {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: поездка на %d дней, план -- не больше чем на %d", ErrInvalid, days, maxTripDays)
	}
	return start, end, nil
}

func notesList(in []string) ([]string, error) {
	if len(in) > maxNotes {
		return nil, fmt.Errorf("%w: заметок не больше %d", ErrInvalid, maxNotes)
	}
	out := []string{}
	for _, n := range in {
		if n = text(n, maxNoteRunes); n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

var currencyPattern = regexp.MustCompile(`^[A-Za-z]{3}$`)

func currencyCode(s string) bool { return currencyPattern.MatchString(strings.TrimSpace(s)) }

func httpURL(s string) bool {
	return (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")) && !strings.ContainsAny(s, " \"'<>")
}

// text -- строка без управляющих символов и лишних пробелов, не длиннее n символов.
func text(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r < 0x20 || r == ' ' }), " ")
	if utf8.RuneCountInString(s) > n {
		s = strings.TrimSpace(string([]rune(s)[:n])) + "…"
	}
	return s
}
