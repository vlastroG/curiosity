package agent

import "crypto-watch/internal/llm"

// Model -- модель, которой можно поручить прогноз.
type Model struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Provider string `json:"provider"`
	Free     bool   `json:"free"`
	// Tools -- модель умеет вызывать инструменты. Если нет, агент сам берёт
	// сводку у MCP-сервера и кладёт её в промпт
	Tools bool `json:"tools"`
}

// DefaultModel -- модель по умолчанию. Бесплатная: планировщик будит её каждую
// минуту, и на ней гоняются все проверки. DeepSeek включают руками в интерфейсе.
const DefaultModel = "nvidia/nemotron-3-super-120b-a12b:free"

// Models -- каталог. Бесплатные модели выбраны по GET https://openrouter.ai/api/v1/models:
// суффикс :free и tools в supported_parameters (снято 2026-09-23). У бесплатных
// общий пул лимитов, поэтому их несколько: если одна упёрлась в 429, берут другую.
var Models = []Model{
	{
		ID:       DefaultModel,
		Title:    "Nemotron 3 Super (free)",
		Subtitle: "NVIDIA, 120B, бесплатная — по умолчанию",
		Provider: llm.ProviderOpenRouter,
		Free:     true,
		Tools:    true,
	},
	{
		ID:       "google/gemma-4-31b-it:free",
		Title:    "Gemma 4 31B (free)",
		Subtitle: "Google, бесплатная, часто упирается в лимит",
		Provider: llm.ProviderOpenRouter,
		Free:     true,
		Tools:    true,
	},
	{
		ID:       "qwen/qwen3.8-27b:free",
		Title:    "Qwen 3.8 27B (free)",
		Subtitle: "Alibaba, бесплатная, часто упирается в лимит",
		Provider: llm.ProviderOpenRouter,
		Free:     true,
		Tools:    true,
	},
	{
		ID:       "deepseek-flash",
		Title:    "DeepSeek Flash",
		Subtitle: "DeepSeek-V4.1-Flash, платная",
		Provider: llm.ProviderDeepSeek,
		Tools:    true,
	},
	{
		ID:       "deepseek-v4-pro",
		Title:    "DeepSeek V4 Pro",
		Subtitle: "флагманская, дороже втрое",
		Provider: llm.ProviderDeepSeek,
		Tools:    true,
	},
}

// FindModel ищет модель по id.
func FindModel(id string) (Model, bool) {
	for _, model := range Models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}
