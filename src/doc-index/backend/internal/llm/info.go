package llm

// Сведения о модели для настроек чата: контекстное окно облачной модели
// и параметры локальной (квантование, размер, окно).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// LocalInfo -- ответ llmcli GET /v1/info: model, ready, parameterSize,
// quantization, contextLength, numCtx.
func LocalInfo(ctx context.Context, baseURL string) (map[string]any, error) {
	var out map[string]any
	err := getJSON(ctx, baseURL+"/v1/info", &out)
	return out, err
}

// OpenRouterContext -- контекстное окно модели по публичному каталогу OpenRouter.
func OpenRouterContext(ctx context.Context, id string) (int, error) {
	var catalog struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := getJSON(ctx, "https://openrouter.ai/api/v1/models", &catalog); err != nil {
		return 0, err
	}
	for _, m := range catalog.Data {
		if m.ID == id {
			return m.ContextLength, nil
		}
	}
	return 0, fmt.Errorf("модели %s нет в каталоге OpenRouter", id)
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
