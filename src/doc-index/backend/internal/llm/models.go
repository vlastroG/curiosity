package llm

// Модель задаётся один раз переменной RAG_MODEL. Провайдер -- из id:
// local -- локальная модель через llmcli (по умолчанию qwen3.5:9b), deepseek-* идёт в DeepSeek,
// остальное -- в OpenRouter.

import (
	"fmt"
	"strings"
)

// DefaultModel -- бесплатная модель OpenRouter.
const DefaultModel = "nvidia/nemotron-3-super-120b-a12b:free"

// MaxTokens -- бюджет вывода, как у чатов помощника строителя. Рассуждающие
// модели тратят его и на рассуждения, так что он с большим запасом.
const MaxTokens = 100_000

// Model -- модель каталога.
type Model struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Free  bool   `json:"free"`
	// MaxOutput -- потолок вывода модели (OpenRouter /api/v1/models); 0 -- не меньше MaxTokens
	MaxOutput int `json:"maxOutput,omitempty"`
}

// Budget -- бюджет вывода для модели: MaxTokens, но не выше её потолка.
func (m Model) Budget() int {
	if m.MaxOutput > 0 && m.MaxOutput < MaxTokens {
		return m.MaxOutput
	}
	return MaxTokens
}

// Models -- допустимые значения RAG_MODEL.
var Models = []Model{
	{ID: DefaultModel, Title: "Nemotron 3 Super (free)", Free: true},
	{ID: "google/gemma-4-31b-it:free", Title: "Gemma 4 31B (free)", Free: true, MaxOutput: 32_768},
	{ID: "qwen/qwen3.8-27b:free", Title: "Qwen 3.8 27B (free)", Free: true},
	{ID: "deepseek-flash", Title: "DeepSeek Flash"},
	{ID: "deepseek-v4-pro", Title: "DeepSeek V4 Pro"},
	// бюджет не урезан: llmcli сам ограничивает ответ контекстом модели
	{ID: LocalModel, Title: "Локальная модель (llmcli)", Free: true},
}

// LocalModel -- id локальной модели в RAG_MODEL.
const LocalModel = "local"

// ResolveModel выбирает модель и провайдера. getenv -- os.Getenv.
func ResolveModel(id string, getenv func(string) string) (Model, Provider, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = DefaultModel
	}
	var model Model
	for _, m := range Models {
		if m.ID == id {
			model = m
		}
	}
	if model.ID == "" {
		ids := make([]string, len(Models))
		for i, m := range Models {
			ids[i] = m.ID
		}
		return Model{}, Provider{}, fmt.Errorf("неизвестная модель %q; допустимые: %s", id, strings.Join(ids, ", "))
	}
	if id == LocalModel {
		url := getenv("LLMCLI_URL")
		if url == "" {
			url = "http://llmcli:8080"
		}
		return model, Local(url), nil
	}
	appURL := getenv("APP_URL")
	if appURL == "" {
		appURL = "http://localhost"
	}
	provider, key := OpenRouter(getenv("OPENROUTER_API_KEY"), appURL, "Twain Expert"), "OPENROUTER_API_KEY"
	if strings.HasPrefix(id, "deepseek-") {
		provider, key = DeepSeek(getenv("DEEPSEEK_API_KEY")), "DEEPSEEK_API_KEY"
	}
	if !provider.Available() {
		return Model{}, Provider{}, fmt.Errorf("для модели %s нужен ключ %s", id, key)
	}
	return model, provider, nil
}
