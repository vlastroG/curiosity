package agent

import (
	"time"

	"agent-sprout/internal/llm"
)

// Model -- запись каталога: что за модель, у какого провайдера и сколько стоит.
//
// Цены -- доллары за 1 млн токенов по пиковому тарифу, источник:
// https://api-docs.deepseek.com/quick_start/pricing/ (снято 2026-09-07).
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
}

// Models -- весь набор моделей, доступных чату.
//
// Бесплатная модель OpenRouter нужна не как "слабый класс" из дня 5, а как рабочая
// лошадка для прогонов при разработке: на ней гоняются проверки, не тратя деньги.
// Боевые ответы даёт DeepSeek.
var Models = []Model{
	{
		ID:              "liquid/lfm-2.5-2.6b:free",
		Title:           "LFM2.5-2.6B (free)",
		Subtitle:        "бесплатная, для прогонов и отладки",
		Provider:        llm.ProviderOpenRouter,
		PriceCacheHit:   0,
		PriceCacheMiss:  0,
		PriceOut:        0,
		ContextTokens:   65_000,
		MaxOutputTokens: 4096,
	},
	{
		ID:              "deepseek-v4-flash",
		Title:           "DeepSeek V4 Flash",
		Subtitle:        "быстрая рассуждающая, рабочий вариант",
		Provider:        llm.ProviderDeepSeek,
		PriceCacheHit:   0.014,
		PriceCacheMiss:  0.44,
		PriceOut:        1.32,
		ContextTokens:   1_000_000,
		MaxOutputTokens: 384_000,
	},
	{
		ID:              "deepseek-v4-pro",
		Title:           "DeepSeek V4 Pro",
		Subtitle:        "флагманская рассуждающая, втрое дороже вывода",
		Provider:        llm.ProviderDeepSeek,
		PriceCacheHit:   0.044,
		PriceCacheMiss:  1.32,
		PriceOut:        3.96,
		ContextTokens:   1_000_000,
		MaxOutputTokens: 384_000,
	},
}

// FindModel ищет модель в каталоге по её id.
func FindModel(id string) (Model, bool) {
	for _, model := range Models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

// Cost -- стоимость одного вызова и по какому тарифу она посчитана.
type Cost struct {
	USD     float64 `json:"usd"`
	OffPeak bool    `json:"offPeak"`
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

	usd := float64(hit)*m.PriceCacheHit +
		float64(miss)*m.PriceCacheMiss +
		float64(usage.CompletionTokens)*m.PriceOut
	usd /= 1e6

	offPeak := IsOffPeak(at)
	if offPeak {
		usd /= 2
	}

	return Cost{USD: usd, OffPeak: offPeak}
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

// Preset -- заготовка system prompt. Набор перенесён из режимов дня 3: там сравнивались
// способы промптинга, здесь тот же набор стал стартовыми настройками чата.
type Preset struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Hint   string `json:"hint"`
	Prompt string `json:"prompt"`
}

// formatRules -- требования к оформлению, общие для всех пресетов. Без них модель
// сыплет LaTeX и широкими таблицами, которые в ленте чата нечитаемы.
const formatRules = "Отвечай на русском языке в простом Markdown. " +
	"Разрешены заголовки уровня ### и ниже, списки, **жирный**, *курсив*, " +
	"`моноширинный` и блоки кода в тройных апострофах. " +
	"Формулы записывай обычным текстом в одну строку (например: t = 1500 / 20 = 75 с). " +
	"Не используй LaTeX."

// Presets -- готовые system prompt, которые можно подставить в настройках чата
// одним кликом и дальше править руками.
var Presets = []Preset{
	{
		ID:     "assistant",
		Title:  "Ассистент",
		Hint:   "обычный помощник, никаких инструкций по способу решения",
		Prompt: "Ты — полезный ассистент. Отвечай по существу и без воды. " + formatRules,
	},
	{
		ID:    "stepwise",
		Title: "Пошагово",
		Hint:  "разбить решение на шаги и проверить его",
		Prompt: "Ты — решатель задач. Решай строго пошагово: сначала выпиши, что дано " +
			"и что требуется найти, затем пронумерованные шаги с промежуточным результатом " +
			"после каждого. Перед финалом проверь решение подстановкой или прикидкой. " +
			"Последней строкой напиши \"Ответ: ...\" — одна строка с итогом. " + formatRules,
	},
	{
		ID:    "analyst",
		Title: "Аналитик",
		Hint:  "сначала разбор условия и допущений, потом решение",
		Prompt: "Ты — аналитик. Начни с разбора условия: выпиши все данные, явные и неявные " +
			"допущения, что именно требуется найти и чего в условии не хватает. " +
			"Только после разбора дай решение и итоговый ответ. Пиши сжато. " + formatRules,
	},
	{
		ID:    "engineer",
		Title: "Инженер",
		Hint:  "работающая процедура вместо рассуждений вокруг задачи",
		Prompt: "Ты — инженер. Тебя интересует работающая процедура, а не рассуждения вокруг " +
			"задачи. Дай конкретный алгоритм или расчёт и доведи его до числа, формулы или " +
			"готового ответа. Если задача алгоритмическая — покажи алгоритм и его сложность. " +
			formatRules,
	},
	{
		ID:    "critic",
		Title: "Критик",
		Hint:  "сначала ловушки задачи, потом решение в обход них",
		Prompt: "Ты — критик и скептик. Сначала перечисли ловушки этой задачи и типичные " +
			"ошибки: неверная трактовка условия, подмена вопроса, ошибки в арифметике, " +
			"забытые граничные случаи. Затем реши задачу сам, обходя перечисленные ловушки. " +
			formatRules,
	},
}

// jsonInstruction добавляется к system prompt при responseFormat=json_object.
// Слово "json" в промпте -- требование провайдеров: без него запрос с
// response_format=json_object отклоняется.
const jsonInstruction = "Ответ верни одним валидным json-объектом. " +
	"Никакого текста до или после объекта, никаких markdown-ограждений: " +
	"весь ответ целиком должен разбираться как json."
