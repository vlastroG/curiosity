package main

// Клиент рыночных данных Binance.
//
// Нужны только цены, и за ними не нужен ни ключ, ни аккаунт: публичный эндпоинт
// ticker/price отдаёт последние сделки по списку пар одним запросом. Один запрос
// на весь сбор важен: планировщик ходит раз в минуту, и пять запросов вместо
// одного -- это пять шансов упереться в лимит и пять мест, где сбор может
// развалиться наполовину.
//
// Coinbase был бы привычнее, но отсюда он не отвечает; Binance отвечает, а цена
// в USDT от цены в долларах для сводки не отличается.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// quoteAsset -- валюта котировки. Все пары вида BTCUSDT.
const quoteAsset = "USDT"

// Quote -- цена одной монеты в момент сбора.
type Quote struct {
	Symbol string  `json:"symbol" jsonschema:"тикер монеты, например BTC"`
	Price  float64 `json:"price" jsonschema:"цена в долларах (USDT)"`
}

// fetchPrices -- текущие цены монет одним запросом.
//
// Порядок ответа совпадает с порядком symbols, а не с тем, как отдала биржа:
// в сводке и на графиках монеты должны стоять на своих местах.
func fetchPrices(ctx context.Context, client *http.Client, base string, symbols []string) ([]Quote, error) {
	pairs := make([]string, len(symbols))
	for i, symbol := range symbols {
		pairs[i] = symbol + quoteAsset
	}
	list, _ := json.Marshal(pairs)

	endpoint := strings.TrimRight(base, "/") + "/api/v3/ticker/price?symbols=" + url.QueryEscape(string(list))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("биржа не ответила: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("ответ биржи не дочитался: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("биржа вернула %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var wire []struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("ответ биржи не разобрался: %w", err)
	}

	prices := make(map[string]float64, len(wire))
	for _, item := range wire {
		// цена приходит строкой, чтобы не терять знаки у мелких монет
		value, err := strconv.ParseFloat(item.Price, 64)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("странная цена %s: %q", item.Symbol, item.Price)
		}
		prices[strings.TrimSuffix(item.Symbol, quoteAsset)] = value
	}

	quotes := make([]Quote, 0, len(symbols))
	for _, symbol := range symbols {
		price, ok := prices[symbol]
		if !ok {
			return nil, fmt.Errorf("биржа не прислала цену %s", symbol)
		}
		quotes = append(quotes, Quote{Symbol: symbol, Price: price})
	}
	return quotes, nil
}
