package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Ответ ЦБ урезан: у лиры номинал 10 -- проверяется деление на номинал.
const cbr = `{"Date":"2026-09-25T11:30:00+03:00","Valute":{
	"USD":{"Nominal":1,"Value":80},
	"EUR":{"Nominal":1,"Value":90},
	"TRY":{"Nominal":10,"Value":20},
	"BAD":{"Nominal":0,"Value":5}}}`

func fakeRates(t *testing.T) (*Rates, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte(cbr))
	}))
	t.Cleanup(server.Close)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	return &Rates{client: server.Client(), url: server.URL, ttl: time.Hour, now: func() time.Time { return now }}, &hits
}

func TestConvert(t *testing.T) {
	rates, hits := fakeRates(t)
	ctx := context.Background()

	// 80 000 ₽ → лиры: 1 TRY = 2 ₽
	c, err := rates.convert(ctx, 80000, "rub", "TRY")
	if err != nil || c.Result != 40000 || c.Rate != 0.5 || c.Date != "2026-09-25" {
		t.Fatalf("RUB→TRY: %+v %v", c, err)
	}
	// кросс-курс через рубль: 100 $ = 8000 ₽ = 4000 лир
	c, _ = rates.convert(ctx, 100, "USD", "TRY")
	if c.Result != 4000 || c.Rate != 40 {
		t.Errorf("USD→TRY: %+v", c)
	}
	c, _ = rates.convert(ctx, 10, "EUR", "USD")
	if c.Result != 11.25 {
		t.Errorf("мелкая сумма до копеек: %+v", c)
	}
	if hits.Load() != 1 {
		t.Errorf("курсы запрошены %d раз, кеш не работает", hits.Load())
	}

	if _, err := rates.convert(ctx, 10, "RUB", "BAD"); !errors.Is(err, ErrUnknownCurrency) {
		t.Errorf("валюта с нулевым номиналом: %v", err)
	}
	if _, err := rates.convert(ctx, 10, "RUB", "XYZ"); !errors.Is(err, ErrUnknownCurrency) {
		t.Errorf("неизвестная валюта: %v", err)
	}
	if _, err := rates.convert(ctx, -5, "RUB", "USD"); err == nil {
		t.Error("отрицательная сумма принята")
	}
}

func TestBudgetSplit(t *testing.T) {
	b, err := budgetSplit(80000, "rub", 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if b.PerDay != 20000 || b.PerPerson != 40000 || b.PerPersonPerDay != 10000 || b.Currency != "RUB" {
		t.Errorf("разбивка: %+v", b)
	}
	total, sharesSum := 0.0, 0
	for _, c := range b.Categories {
		total += c.Amount
		sharesSum += c.Share
	}
	if total != 80000 || sharesSum != 100 {
		t.Errorf("статьи не сходятся: %v, %d%%", total, sharesSum)
	}
	for _, bad := range [][3]float64{{0, 3, 1}, {100, 0, 1}, {100, 3, 0}, {100, 61, 1}} {
		if _, err := budgetSplit(bad[0], "RUB", int(bad[1]), int(bad[2])); err == nil {
			t.Errorf("%v принято", bad)
		}
	}
}

func TestTools(t *testing.T) {
	rates, _ := fakeRates(t)
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	go newServer(rates).Run(ctx, st)
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	call := func(name string, args map[string]any, out any) *mcp.CallToolResult {
		r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if out != nil && !r.IsError {
			raw, _ := json.Marshal(r.StructuredContent)
			json.Unmarshal(raw, out)
		}
		return r
	}
	text := func(r *mcp.CallToolResult) string { return r.Content[0].(*mcp.TextContent).Text }

	var cur CountryOutput
	call("country_currency", map[string]any{"country_code": "tr"}, &cur)
	if cur.Currency != "TRY" || !cur.HasRate || cur.Name != "турецкая лира" {
		t.Errorf("Турция: %+v", cur)
	}
	call("country_currency", map[string]any{"country_code": "MA"}, &cur)
	if cur.Currency != "MAD" || cur.HasRate || cur.Note == "" {
		t.Errorf("Марокко без курса ЦБ: %+v", cur)
	}
	if r := call("country_currency", map[string]any{"country_code": "ZZ"}, nil); !r.IsError {
		t.Error("выдуманная страна")
	}

	r := call("convert", map[string]any{"amount": 80000, "from": "RUB", "to": "TRY"}, nil)
	if !strings.Contains(text(r), "80 000 RUB = 40 000 TRY") {
		t.Errorf("текст convert: %s", text(r))
	}
	r = call("budget_split", map[string]any{"total": 40000, "currency": "TRY", "days": 4, "travelers": 2}, nil)
	if !strings.Contains(text(r), "на человека в день 5 000") {
		t.Errorf("текст budget_split: %s", text(r))
	}
}

func TestMoney(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 999: "999", 1000: "1 000", 1234567: "1 234 567", 12.5: "12.50", -4000: "-4 000"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, ждали %q", in, got, want)
		}
	}
}
