package agent

// Модель задаётся один раз переменной TRIP_MODEL. Провайдер -- из id:
// deepseek-* идёт в DeepSeek, остальное -- в OpenRouter.

import (
	"fmt"
	"strings"

	"trip-agent/internal/llm"
)

// DefaultModel -- бесплатная и умеет вызывать инструменты.
const DefaultModel = "nvidia/nemotron-3-super-120b-a12b:free"

// Model -- модель каталога.
type Model struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Free  bool   `json:"free"`
}

// Models -- допустимые значения TRIP_MODEL.
var Models = []Model{
	{ID: DefaultModel, Title: "Nemotron 3 Super (free)", Free: true},
	{ID: "google/gemma-4-31b-it:free", Title: "Gemma 4 31B (free)", Free: true},
	{ID: "qwen/qwen3.8-27b:free", Title: "Qwen 3.8 27B (free)", Free: true},
	{ID: "deepseek-flash", Title: "DeepSeek Flash"},
	{ID: "deepseek-v4-pro", Title: "DeepSeek V4 Pro"},
}

// ResolveModel выбирает модель и провайдера. getenv -- os.Getenv.
func ResolveModel(id string, getenv func(string) string) (Model, llm.Provider, error) {
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
		return Model{}, llm.Provider{}, fmt.Errorf("неизвестная модель %q; допустимые: %s", id, strings.Join(ids, ", "))
	}
	appURL := getenv("APP_URL")
	if appURL == "" {
		appURL = "http://localhost"
	}
	provider, key := llm.OpenRouter(getenv("OPENROUTER_API_KEY"), appURL, "Trip Planner"), "OPENROUTER_API_KEY"
	if strings.HasPrefix(id, "deepseek-") {
		provider, key = llm.DeepSeek(getenv("DEEPSEEK_API_KEY")), "DEEPSEEK_API_KEY"
	}
	if !provider.Available() {
		return Model{}, llm.Provider{}, fmt.Errorf("для модели %s нужен ключ %s", id, key)
	}
	return model, provider, nil
}
