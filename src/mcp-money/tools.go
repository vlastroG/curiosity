package main

// Инструменты сервера денег.

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CountryInput -- аргументы country_currency.
type CountryInput struct {
	CountryCode string `json:"country_code" jsonschema:"код страны ISO из двух букв -- из find_city сервера мест"`
}

// CountryOutput -- результат country_currency.
type CountryOutput struct {
	CountryCode string `json:"countryCode" jsonschema:"код страны"`
	Currency    string `json:"currency" jsonschema:"код валюты ISO 4217 -- передавай в convert"`
	Name        string `json:"name,omitempty" jsonschema:"название валюты"`
	HasRate     bool   `json:"hasRate" jsonschema:"публикует ли ЦБ РФ курс этой валюты"`
	Note        string `json:"note,omitempty" jsonschema:"что делать, если курса нет"`
}

// ConvertInput -- аргументы convert.
type ConvertInput struct {
	Amount float64 `json:"amount" jsonschema:"сумма"`
	From   string  `json:"from" jsonschema:"код исходной валюты, например RUB"`
	To     string  `json:"to" jsonschema:"код целевой валюты -- из country_currency"`
}

// BudgetInput -- аргументы budget_split.
type BudgetInput struct {
	Total     float64 `json:"total" jsonschema:"весь бюджет поездки"`
	Currency  string  `json:"currency" jsonschema:"валюта бюджета"`
	Days      int     `json:"days" jsonschema:"сколько дней поездка"`
	Travelers int     `json:"travelers" jsonschema:"сколько человек едет"`
}

func newServer(rates *Rates) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-money", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "country_currency",
		Description: "Валюта страны по её коду ISO (код даёт find_city). Нужна, чтобы перевести бюджет в местные деньги. " +
			"Говорит и то, есть ли у ЦБ РФ курс этой валюты.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CountryInput) (*mcp.CallToolResult, CountryOutput, error) {
		code := strings.ToUpper(strings.TrimSpace(in.CountryCode))
		currency, ok := countryCurrency[code]
		if !ok {
			return nil, CountryOutput{}, fmt.Errorf("не знаю валюту страны %q -- проверь код ISO из двух букв", in.CountryCode)
		}
		out := CountryOutput{CountryCode: code, Currency: currency, Name: currencyNames[currency]}
		if rates, _, err := rates.load(ctx); err == nil {
			_, out.HasRate = rates[currency]
		}
		text := fmt.Sprintf("Валюта страны %s: %s", code, currency)
		if out.Name != "" {
			text += " (" + out.Name + ")"
		}
		if !out.HasRate {
			out.Note = "ЦБ РФ не публикует курс этой валюты -- считай бюджет в USD или EUR и укажи это в плане."
			text += ". " + out.Note
		}
		return textResult(text + "."), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "convert",
		Description: "Перевести сумму из одной валюты в другую по официальному курсу ЦБ РФ на сегодня. " +
			"Код целевой валюты бери из country_currency, не угадывай.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ConvertInput) (*mcp.CallToolResult, Conversion, error) {
		c, err := rates.convert(ctx, in.Amount, in.From, in.To)
		if err != nil {
			return nil, Conversion{}, err
		}
		return textResult(fmt.Sprintf("%s %s = %s %s (курс ЦБ РФ на %s: 1 %s = %s %s).",
			money(c.Amount), c.From, money(c.Result), c.To, c.Date, c.From, trimZeros(c.Rate), c.To)), c, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "budget_split",
		Description: "Разложить бюджет на дни, людей и статьи расходов (жильё, еда, транспорт, музеи, запас). " +
			"Считает сервер, не модель. Вызывай с суммой в той валюте, в которой хочешь видеть разбивку.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in BudgetInput) (*mcp.CallToolResult, Budget, error) {
		b, err := budgetSplit(in.Total, in.Currency, in.Days, in.Travelers)
		if err != nil {
			return nil, Budget{}, err
		}
		var text strings.Builder
		fmt.Fprintf(&text, "Бюджет %s %s на %d дн. и %d чел.: в день на всех %s, на человека %s, на человека в день %s.\nРазбивка (ориентир): ",
			money(b.Total), b.Currency, b.Days, b.Travelers, money(b.PerDay), money(b.PerPerson), money(b.PerPersonPerDay))
		parts := []string{}
		for _, c := range b.Categories {
			parts = append(parts, fmt.Sprintf("%s %d%% — %s", c.Name, c.Share, money(c.Amount)))
		}
		text.WriteString(strings.Join(parts, "; "))
		return textResult(text.String()), b, nil
	})

	return server
}

// money -- сумма с пробелами между тысячами: 31 400, 12.5.
func money(v float64) string {
	if v != float64(int64(v)) {
		return fmt.Sprintf("%.2f", v)
	}
	s := fmt.Sprintf("%d", int64(v))
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 && s[i-1] != '-' {
			out = append(out, ' ')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func trimZeros(v float64) string {
	s := fmt.Sprintf("%.4f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
