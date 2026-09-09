package agent

import (
	"testing"
	"time"

	"agent-sprout/internal/llm"
)

func mustModel(t *testing.T, id string) Model {
	t.Helper()
	model, ok := FindModel(id)
	if !ok {
		t.Fatalf("модели %s нет в каталоге", id)
	}
	return model
}

func TestCostSplitsCacheHitAndMiss(t *testing.T) {
	model := mustModel(t, "deepseek-v4-flash")
	usage := llm.Usage{
		PromptTokens:          3000,
		PromptCacheHitTokens:  1000,
		PromptCacheMissTokens: 2000,
		CompletionTokens:      500,
	}

	// понедельник, 02:00 UTC -- пиковый тариф
	peak := model.Cost(usage, time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC))
	// 1000*0.014 + 2000*0.44 + 500*1.32 = 1554 долларов за миллион токенов
	assertUSD(t, peak.USD, 0.001554)
	if peak.OffPeak {
		t.Fatal("02:00 в понедельник -- пик")
	}

	// то же самое днём стоит вдвое дешевле
	offPeak := model.Cost(usage, time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC))
	assertUSD(t, offPeak.USD, 0.000777)
	if !offPeak.OffPeak {
		t.Fatal("12:00 -- вне пика")
	}
}

func TestCostSplitsInputAndOutput(t *testing.T) {
	model := mustModel(t, "deepseek-v4-flash")
	usage := llm.Usage{
		PromptTokens:          3000,
		PromptCacheHitTokens:  1000,
		PromptCacheMissTokens: 2000,
		CompletionTokens:      500,
	}

	cost := model.Cost(usage, time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC))

	// вход: 1000*0.014 + 2000*0.44 = 894; выход: 500*1.32 = 660
	assertUSD(t, cost.InputUSD, 0.000894)
	assertUSD(t, cost.OutputUSD, 0.00066)
	// части обязаны складываться в итог: интерфейс показывает их под разными
	// сообщениями, и сумма по чату должна сходиться с тем, что видно в ленте
	assertUSD(t, cost.InputUSD+cost.OutputUSD, cost.USD)
}

func TestCostOffPeakHalvesBothParts(t *testing.T) {
	model := mustModel(t, "deepseek-v4-flash")
	usage := llm.Usage{PromptTokens: 3000, CompletionTokens: 500}

	peak := model.Cost(usage, time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC))
	offPeak := model.Cost(usage, time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC))

	assertUSD(t, offPeak.InputUSD, peak.InputUSD/2)
	assertUSD(t, offPeak.OutputUSD, peak.OutputUSD/2)
	assertUSD(t, offPeak.InputUSD+offPeak.OutputUSD, offPeak.USD)
}

func TestCostWithoutCacheBreakdownCountsEverythingAsMiss(t *testing.T) {
	model := mustModel(t, "deepseek-v4-flash")
	// OpenRouter и подобные не присылают разбивку кеша -- весь вход считается промахом
	usage := llm.Usage{PromptTokens: 3000, CompletionTokens: 500}

	cost := model.Cost(usage, time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC))
	// 3000*0.44 + 500*1.32 = 1980
	assertUSD(t, cost.USD, 0.00198)
}

func TestCostOfFreeModelIsZero(t *testing.T) {
	model := mustModel(t, "liquid/lfm-2.5-2.6b:free")
	usage := llm.Usage{PromptTokens: 100000, CompletionTokens: 50000}

	if cost := model.Cost(usage, time.Now()); cost.USD != 0 {
		t.Fatalf("бесплатная модель должна стоить 0, получено %v", cost.USD)
	}
}

func TestIsOffPeakBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		at      time.Time
		offPeak bool
	}{
		{"понедельник 00:59", time.Date(2026, 9, 7, 0, 59, 0, 0, time.UTC), true},
		{"понедельник 01:00", time.Date(2026, 9, 7, 1, 0, 0, 0, time.UTC), false},
		{"понедельник 03:59", time.Date(2026, 9, 7, 3, 59, 0, 0, time.UTC), false},
		{"понедельник 04:00", time.Date(2026, 9, 7, 4, 0, 0, 0, time.UTC), true},
		{"понедельник 06:00", time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC), false},
		{"понедельник 10:00", time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC), true},
		{"пятница 02:00", time.Date(2026, 9, 11, 2, 0, 0, 0, time.UTC), false},
		{"суббота 02:00", time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC), true},
		{"воскресенье 07:00", time.Date(2026, 9, 6, 7, 0, 0, 0, time.UTC), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOffPeak(tc.at); got != tc.offPeak {
				t.Fatalf("ожидалось offPeak=%v, получено %v", tc.offPeak, got)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	valid := DefaultConfig("deepseek-v4-flash")
	if err := valid.Validate(); err != nil {
		t.Fatalf("настройки по умолчанию должны быть валидны: %v", err)
	}

	cases := []struct {
		name  string
		spoil func(*Config)
	}{
		{"неизвестная модель", func(c *Config) { c.Model = "gpt-9" }},
		{"температура вне диапазона", func(c *Config) { c.Temperature = 3 }},
		{"max_tokens больше лимита модели", func(c *Config) { c.MaxTokens = 999_999_999 }},
		{"нулевой top_p", func(c *Config) { c.TopP = 0 }},
		{"неизвестный формат ответа", func(c *Config) { c.ResponseFormat = "xml" }},
		{"отрицательная глубина истории", func(c *Config) { c.HistoryDepth = -1 }},
		{"нулевой лимит ввода", func(c *Config) { c.MaxInputChars = 0 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig("deepseek-v4-flash")
			tc.spoil(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
		})
	}
}

func assertUSD(t *testing.T, got, want float64) {
	t.Helper()
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("ожидалось %.9f, получено %.9f", want, got)
	}
}
