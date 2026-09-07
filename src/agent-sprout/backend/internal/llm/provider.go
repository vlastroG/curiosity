package llm

// Provider -- куда и с каким ключом идти. DeepSeek и OpenRouter говорят на одном
// OpenAI-совместимом протоколе, поэтому различия сводятся к адресу, ключу и паре
// заголовков -- всё это описывается данными, а не отдельным кодом на каждого.
type Provider struct {
	ID       string
	Title    string
	Endpoint string
	APIKey   string
	// ExtraHeaders -- заголовки сверх Authorization и Content-Type.
	// OpenRouter просит HTTP-Referer и X-Title для атрибуции запроса.
	ExtraHeaders map[string]string
}

// Идентификаторы провайдеров. Используются как ключ в каталоге моделей.
const (
	ProviderDeepSeek   = "deepseek"
	ProviderOpenRouter = "openrouter"
)

// Available -- есть ли ключ. Модели провайдера без ключа показываются в каталоге,
// но помечаются недоступными: так в интерфейсе видно, чего не хватает.
func (p Provider) Available() bool {
	return p.APIKey != ""
}

// DeepSeek возвращает описание провайдера с подставленным ключом.
func DeepSeek(apiKey string) Provider {
	return Provider{
		ID:       ProviderDeepSeek,
		Title:    "DeepSeek API",
		Endpoint: "https://api.deepseek.com/chat/completions",
		APIKey:   apiKey,
	}
}

// OpenRouter возвращает описание провайдера с подставленным ключом.
func OpenRouter(apiKey, appURL, appTitle string) Provider {
	return Provider{
		ID:       ProviderOpenRouter,
		Title:    "OpenRouter",
		Endpoint: "https://openrouter.ai/api/v1/chat/completions",
		APIKey:   apiKey,
		ExtraHeaders: map[string]string{
			"HTTP-Referer": appURL,
			"X-Title":      appTitle,
		},
	}
}
