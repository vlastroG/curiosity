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

func TestResolveModel(t *testing.T) {
	env := map[string]string{"OPENROUTER_API_KEY": "k"}
	get := func(k string) string { return env[k] }
	if m, p, err := ResolveModel("", get); err != nil || m.ID != DefaultModel || p.ID != ProviderOpenRouter {
		t.Fatalf("%+v %+v %v", m, p, err)
	}
	if _, _, err := ResolveModel("deepseek-flash", get); err == nil {
		t.Error("без ключа DeepSeek модель выбрана")
	}
	if m, p, err := ResolveModel("local", get); err != nil || m.Budget() != MaxTokens ||
		p.Endpoint != "http://llmcli:8080/v1/chat/completions" || !p.Available() {
		t.Errorf("local: %+v %+v %v", m, p, err)
	}
	env["LLMCLI_URL"] = "http://localhost:9000/"
	if _, p, _ := ResolveModel("local", get); p.Endpoint != "http://localhost:9000/v1/chat/completions" {
		t.Errorf("LLMCLI_URL не учтён: %s", p.Endpoint)
	}
	if _, _, err := ResolveModel("gpt-9", get); err == nil {
		t.Error("неизвестная модель выбрана")
	}
}
