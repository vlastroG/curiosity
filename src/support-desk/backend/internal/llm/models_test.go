package llm

import "testing"

func TestBudget(t *testing.T) {
	for _, m := range Models {
		b := m.Budget()
		if b <= 0 || b > MaxTokens || (m.MaxOutput > 0 && b > m.MaxOutput) {
			t.Errorf("%s: бюджет %d", m.ID, b)
		}
	}
	if (Model{}).Budget() != 100_000 || (Model{MaxOutput: 32_768}).Budget() != 32_768 {
		t.Error("бюджет по умолчанию и срез по потолку")
	}
}

func TestResolveDefault(t *testing.T) {
	env := map[string]string{"DEEPSEEK_API_KEY": "k"}
	get := func(k string) string { return env[k] }
	if m, p, err := ResolveModel("", get); err != nil || m.ID != "deepseek-flash" || p.ID != ProviderDeepSeek {
		t.Fatalf("%+v %+v %v", m, p, err)
	}
	if _, _, err := ResolveModel("local", get); err == nil {
		t.Error("локальная модель в этом сервисе не предусмотрена")
	}
	if _, _, err := ResolveModel("google/gemma-4-31b-it:free", get); err == nil {
		t.Error("без ключа OpenRouter модель выбрана")
	}
}
