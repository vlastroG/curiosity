package main

// Курсы ЦБ РФ и арифметика бюджета.
//
// Всё, что можно посчитать, считает сервер, а не модель: кросс-курс через рубль,
// округление, деление бюджета на дни и людей. Модель в арифметике ошибается
// чаще всего, а план с неверной суммой хуже плана без суммы.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Rates -- курсы ЦБ РФ с кешем на час: курс публикуется раз в сутки.
type Rates struct {
	client *http.Client
	url    string
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	fetched time.Time
	date    string
	// perUnit -- сколько рублей стоит одна единица валюты
	perUnit map[string]float64
}

// ErrUnknownCurrency -- ЦБ не публикует курс этой валюты.
var ErrUnknownCurrency = errors.New("курс валюты не публикуется ЦБ РФ")

// load возвращает курсы, при необходимости обновляя кеш.
func (r *Rates) load(ctx context.Context) (map[string]float64, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.perUnit != nil && r.now().Sub(r.fetched) < r.ttl {
		return r.perUnit, r.date, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("сервис курсов не ответил: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("сервис курсов вернул %d", resp.StatusCode)
	}
	var body struct {
		Date   string `json:"Date"`
		Valute map[string]struct {
			Nominal float64 `json:"Nominal"`
			Value   float64 `json:"Value"`
		} `json:"Valute"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, "", fmt.Errorf("ответ сервиса курсов не разобрался: %w", err)
	}
	perUnit := map[string]float64{"RUB": 1}
	for code, v := range body.Valute {
		if v.Nominal > 0 && v.Value > 0 {
			perUnit[code] = v.Value / v.Nominal
		}
	}
	if len(perUnit) < 2 {
		return nil, "", errors.New("сервис курсов прислал пустой список")
	}
	r.perUnit, r.fetched = perUnit, r.now()
	r.date = body.Date
	if len(r.date) >= 10 {
		r.date = r.date[:10]
	}
	return r.perUnit, r.date, nil
}

// Conversion -- результат перевода суммы.
type Conversion struct {
	Amount float64 `json:"amount" jsonschema:"исходная сумма"`
	From   string  `json:"from" jsonschema:"из какой валюты"`
	To     string  `json:"to" jsonschema:"в какую валюту"`
	Result float64 `json:"result" jsonschema:"сумма в целевой валюте, округлена"`
	Rate   float64 `json:"rate" jsonschema:"сколько единиц целевой валюты за одну единицу исходной"`
	Date   string  `json:"date" jsonschema:"дата курса ЦБ РФ"`
}

// convert переводит сумму через рубль: ЦБ публикует курсы только к рублю.
func (r *Rates) convert(ctx context.Context, amount float64, from, to string) (Conversion, error) {
	from, to = strings.ToUpper(strings.TrimSpace(from)), strings.ToUpper(strings.TrimSpace(to))
	if amount <= 0 || amount > 1e12 {
		return Conversion{}, fmt.Errorf("сумма должна быть больше нуля, а не %v", amount)
	}
	rates, date, err := r.load(ctx)
	if err != nil {
		return Conversion{}, err
	}
	fromRub, ok := rates[from]
	if !ok {
		return Conversion{}, fmt.Errorf("%w: %s", ErrUnknownCurrency, from)
	}
	toRub, ok := rates[to]
	if !ok {
		return Conversion{}, fmt.Errorf("%w: %s", ErrUnknownCurrency, to)
	}
	rate := fromRub / toRub
	return Conversion{
		Amount: amount, From: from, To: to,
		Result: roundMoney(amount * rate),
		Rate:   roundRate(rate),
		Date:   date,
	}, nil
}

// Budget -- бюджет, разложенный на дни, людей и статьи.
type Budget struct {
	Total           float64       `json:"total" jsonschema:"весь бюджет"`
	Currency        string        `json:"currency" jsonschema:"валюта"`
	Days            int           `json:"days" jsonschema:"дней поездки"`
	Travelers       int           `json:"travelers" jsonschema:"сколько человек"`
	PerDay          float64       `json:"perDay" jsonschema:"на всех в день"`
	PerPerson       float64       `json:"perPerson" jsonschema:"на человека за поездку"`
	PerPersonPerDay float64       `json:"perPersonPerDay" jsonschema:"на человека в день"`
	Categories      []BudgetShare `json:"categories" jsonschema:"ориентировочная разбивка по статьям на всю поездку"`
}

// BudgetShare -- статья бюджета.
type BudgetShare struct {
	Name   string  `json:"name" jsonschema:"статья"`
	Share  int     `json:"share" jsonschema:"доля, %"`
	Amount float64 `json:"amount" jsonschema:"сумма"`
}

// shares -- ориентир распределения бюджета поездки без перелёта.
var shares = []struct {
	name  string
	share int
}{
	{"жильё", 40}, {"еда", 30}, {"транспорт по городу", 10}, {"музеи и экскурсии", 10}, {"запас", 10},
}

func budgetSplit(total float64, currency string, days, travelers int) (Budget, error) {
	if total <= 0 || total > 1e12 {
		return Budget{}, fmt.Errorf("бюджет должен быть больше нуля, а не %v", total)
	}
	if days < 1 || days > 60 {
		return Budget{}, fmt.Errorf("дней должно быть от 1 до 60, а не %d", days)
	}
	if travelers < 1 || travelers > 20 {
		return Budget{}, fmt.Errorf("путешественников должно быть от 1 до 20, а не %d", travelers)
	}
	b := Budget{
		Total: roundMoney(total), Currency: strings.ToUpper(currency), Days: days, Travelers: travelers,
		PerDay:          roundMoney(total / float64(days)),
		PerPerson:       roundMoney(total / float64(travelers)),
		PerPersonPerDay: roundMoney(total / float64(days*travelers)),
	}
	for _, s := range shares {
		b.Categories = append(b.Categories, BudgetShare{Name: s.name, Share: s.share, Amount: roundMoney(total * float64(s.share) / 100)})
	}
	return b, nil
}

// roundMoney -- до целых для крупных сумм, до копеек -- для мелких.
func roundMoney(v float64) float64 {
	if math.Abs(v) >= 100 {
		return math.Round(v)
	}
	return math.Round(v*100) / 100
}

// roundRate -- курс с четырьмя значащими знаками после запятой.
func roundRate(v float64) float64 {
	return math.Round(v*10000) / 10000
}
