package main

// Каталог моделей и выбор модели при старте.
//
// Модель задаётся один раз переменной PIPELINE_MODEL. Провайдер из id: всё, что
// начинается с deepseek-, идёт в DeepSeek, остальное -- в OpenRouter. Неизвестная
// модель или модель без ключа -- ошибка старта: лучше упасть сразу с понятной
// причиной, чем принять запрос и провалить его на середине цепочки.

import (
	"fmt"
	"strings"
)

// DefaultModel -- модель по умолчанию: бесплатная и умеет вызывать инструменты.
const DefaultModel = "nvidia/nemotron-3-super-120b-a12b:free"

// Model -- модель каталога.
type Model struct {
	ID    string
	Title string
	Free  bool
}

// Models -- допустимые значения PIPELINE_MODEL.
var Models = []Model{
	{ID: DefaultModel, Title: "Nemotron 3 Super (free)", Free: true},
	{ID: "google/gemma-4-31b-it:free", Title: "Gemma 4 31B (free)", Free: true},
	{ID: "qwen/qwen3.8-27b:free", Title: "Qwen 3.8 27B (free)", Free: true},
	{ID: "deepseek-flash", Title: "DeepSeek Flash"},
	{ID: "deepseek-v4-pro", Title: "DeepSeek V4 Pro"},
}

// resolveModel выбирает модель и провайдера по значению переменной окружения.
// getenv -- os.Getenv; параметр нужен тестам.
func resolveModel(id string, getenv func(string) string, appTitle string) (Model, Provider, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = DefaultModel
	}

	var model Model
	for _, known := range Models {
		if known.ID == id {
			model = known
		}
	}
	if model.ID == "" {
		ids := make([]string, len(Models))
		for i, known := range Models {
			ids[i] = known.ID
		}
		return Model{}, Provider{}, fmt.Errorf("неизвестная модель %q; допустимые: %s", id, strings.Join(ids, ", "))
	}

	// адрес приложения OpenRouter берёт для атрибуции запросов
	appURL := getenv("APP_URL")
	if appURL == "" {
		appURL = "http://localhost"
	}
	provider := OpenRouter(getenv("OPENROUTER_API_KEY"), appURL, appTitle)
	keyName := "OPENROUTER_API_KEY"
	if strings.HasPrefix(id, "deepseek-") {
		provider = DeepSeek(getenv("DEEPSEEK_API_KEY"))
		keyName = "DEEPSEEK_API_KEY"
	}
	if !provider.Available() {
		return Model{}, Provider{}, fmt.Errorf("для модели %s нужен ключ %s", id, keyName)
	}
	return model, provider, nil
}
