package agent

import (
	"time"

	"agent-sprout/internal/llm"
)

// Model -- запись каталога: что за модель, у какого провайдера и сколько стоит.
//
// Цены -- доллары за 1 млн токенов по пиковому тарифу, источник:
// https://api-docs.deepseek.com/quick_start/pricing/ (снято 2026-09-14).
// Вход тарифицируется по двум ставкам: попадание в кеш промпта дешевле промаха
// в десятки раз, и провайдер присылает разбивку в usage.
type Model struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Subtitle        string  `json:"subtitle"`
	Provider        string  `json:"provider"`
	PriceCacheHit   float64 `json:"priceCacheHit"`
	PriceCacheMiss  float64 `json:"priceCacheMiss"`
	PriceOut        float64 `json:"priceOut"`
	ContextTokens   int     `json:"contextTokens"`
	MaxOutputTokens int     `json:"maxOutputTokens"`
	// DefaultMaxTokens -- бюджет вывода для нового чата и для чата, в котором
	// переключили модель. Один потолок на все модели не годится: у рассуждающей
	// его съедает рассуждение, и до ответа дело не доходит
	DefaultMaxTokens int `json:"defaultMaxTokens"`
	// Reasoning -- модель думает перед ответом, причём по умолчанию и в полную силу.
	// Отсюда два следствия: служебному вызову нужен запас токенов сверх самого
	// ответа, и на служебном вызове рассуждение надо выключать
	Reasoning bool `json:"reasoning"`
	// Tools -- модели можно доверить вызов внешних инструментов.
	//
	// Это не только «провайдер принимает поле tools». Выбрать инструмент, собрать
	// аргументы и не забыть про них на следующем ходе -- работа, на которой маленькая
	// модель ломается тише, чем отказывает: она просто не зовёт инструмент и отвечает
	// по памяти. Поэтому флаг выставлен вручную по каждой модели, а не выведен
	// из ответа провайдера. Интерфейс по нему гасит выключатель погоды
	Tools bool `json:"tools"`
	// ReasoningReserve -- запас токенов на рассуждение сверх длины ответа
	ReasoningReserve int `json:"-"`
}

// ServiceTokens -- бюджет служебного вызова (диспетчер, сжатие, пересказ задачи).
// answer -- сколько нужно самому ответу; остальное запас на рассуждение.
//
// max_tokens это потолок, а не счёт: за неизрасходованное не платят, а обрыв ответа
// на середине стоит целого повторного вызова.
func (m Model) ServiceTokens(answer int) int {
	budget := answer + m.ReasoningReserve
	if budget > m.MaxOutputTokens {
		return m.MaxOutputTokens
	}
	return budget
}

// ServiceThinking -- режим рассуждения для служебного вызова.
//
// Диспетчер, сжатие и пересказ заняты классификацией и извлечением: размышлять там
// не над чем, а ждём и платим мы именно за размышление. У DeepSeek рассуждение
// включено по умолчанию с максимальным усилием -- то есть по умолчанию мы платим
// за него всегда, даже когда просим модель разложить одну фразу по полям.
// У ответа пользователю рассуждение остаётся полным: детальный план работ -- ровно
// то место, где думать есть над чем.
func (m Model) ServiceThinking() string {
	if !m.Reasoning {
		return ""
	}
	return llm.ThinkingOff
}

// Models -- весь набор моделей, доступных чату.
//
// Бесплатная модель OpenRouter нужна не как "слабый класс" из дня 5, а как рабочая
// лошадка для прогонов при разработке: на ней гоняются проверки, не тратя деньги.
// Боевые ответы даёт DeepSeek.
var Models = []Model{
	{
		ID:             "liquid/lfm-2.5-2.6b:free",
		Title:          "LFM2.5-2.6B (free)",
		Subtitle:       "бесплатная, для прогонов и отладки",
		Provider:       llm.ProviderOpenRouter,
		PriceCacheHit:  0,
		PriceCacheMiss: 0,
		PriceOut:       0,
		ContextTokens:  65_000,
		// потолок вывода взят из GET https://openrouter.ai/api/v1/models
		MaxOutputTokens:  8192,
		DefaultMaxTokens: 8192,
		// инструментов не даём: 2.6B параметров -- это про скорость и нулевую цену
		// прогона, а не про выбор инструмента и сборку аргументов
		Tools: false,
	},
	{
		ID:               "deepseek-flash",
		Title:            "DeepSeek Flash",
		Subtitle:         "DeepSeek-V4.1-Flash, рассуждающая, рабочий вариант",
		Provider:         llm.ProviderDeepSeek,
		PriceCacheHit:    0.006,
		PriceCacheMiss:   0.3,
		PriceOut:         1.2,
		ContextTokens:    1_000_000,
		MaxOutputTokens:  384_000,
		DefaultMaxTokens: 100_000,
		Reasoning:        true,
		ReasoningReserve: 8192,
		Tools:            true,
	},
	{
		ID:               "deepseek-v4-pro",
		Title:            "DeepSeek V4 Pro",
		Subtitle:         "DeepSeek-V4-Pro-0813, флагманская, дороже втрое",
		Provider:         llm.ProviderDeepSeek,
		PriceCacheHit:    0.044,
		PriceCacheMiss:   1.32,
		PriceOut:         3.96,
		ContextTokens:    1_000_000,
		MaxOutputTokens:  384_000,
		DefaultMaxTokens: 100_000,
		Reasoning:        true,
		ReasoningReserve: 8192,
		Tools:            true,
	},
}

// modelAliases -- прежние id моделей.
//
// DeepSeek переименовал flash и оставил старое имя работающим: запросы обслуживает
// новая модель. Держим ту же уступку у себя, чтобы чаты, заведённые до переименования,
// не встречали пользователя словами «неизвестная модель».
var modelAliases = map[string]string{
	"deepseek-v4-flash": "deepseek-flash",
}

// FindModel ищет модель в каталоге по её id.
func FindModel(id string) (Model, bool) {
	if alias, ok := modelAliases[id]; ok {
		id = alias
	}

	for _, model := range Models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

// Cost -- стоимость одного вызова, разложенная на вход и выход,
// и по какому тарифу она посчитана.
//
// Разложение нужно интерфейсу: вход и выход показываются под разными сообщениями --
// вход под вопросом, который его вызвал, выход под ответом модели.
type Cost struct {
	USD       float64 `json:"usd"`
	InputUSD  float64 `json:"inputUsd"`
	OutputUSD float64 `json:"outputUsd"`
	OffPeak   bool    `json:"offPeak"`
}

// Cost считает стоимость вызова по фактическому расходу токенов.
//
// Два множителя, которых нет у большинства провайдеров:
//   - вход разбит на попадания и промахи кеша промпта (ставки отличаются в десятки раз);
//   - вне пиковых часов действует половинная цена.
//
// Если провайдер не прислал разбивку кеша (так делает OpenRouter), весь вход считается
// промахом -- это верхняя оценка, занизить стоимость хуже, чем завысить.
func (m Model) Cost(usage llm.Usage, at time.Time) Cost {
	hit := usage.PromptCacheHitTokens
	miss := usage.PromptCacheMissTokens
	if hit == 0 && miss == 0 {
		miss = usage.PromptTokens
	}

	input := (float64(hit)*m.PriceCacheHit + float64(miss)*m.PriceCacheMiss) / 1e6
	output := float64(usage.CompletionTokens) * m.PriceOut / 1e6

	offPeak := IsOffPeak(at)
	if offPeak {
		input /= 2
		output /= 2
	}

	return Cost{USD: input + output, InputUSD: input, OutputUSD: output, OffPeak: offPeak}
}

// peakWindows -- пиковые часы в UTC: [начало, конец) в часах.
var peakWindows = [][2]int{{1, 4}, {6, 10}}

// IsOffPeak -- действует ли половинная цена в указанный момент.
// Пик: 01:00-04:00 и 06:00-10:00 UTC по будням; всё остальное время -- off-peak.
func IsOffPeak(at time.Time) bool {
	utc := at.UTC()

	switch utc.Weekday() {
	case time.Saturday, time.Sunday:
		return true
	}

	hour := utc.Hour()
	for _, window := range peakWindows {
		if hour >= window[0] && hour < window[1] {
			return false
		}
	}
	return true
}
