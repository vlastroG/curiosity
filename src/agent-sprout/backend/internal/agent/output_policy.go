package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-sprout/internal/llm"
)

// checkOutput -- этап после вызова модели.
//
// Различает два вида нарушений. Мягкие возвращаются предупреждениями: ответ всё равно
// показывается, но рядом написано, чем он плох. Жёсткие -- это PolicyError: показывать
// нечего, и вместо пустого пузыря пользователь видит объяснение.
func checkOutput(text string, resp llm.Response, cfg Config) ([]string, error) {
	trimmed := strings.TrimSpace(text)

	if trimmed == "" {
		reason := "модель вернула пустой ответ"
		if resp.FinishReason == "length" {
			// частый случай у рассуждающих моделей: весь бюджет ушёл во внутреннее
			// рассуждение, до видимого текста дело не дошло
			reason += ": весь бюджет max_tokens ушёл на внутреннее рассуждение, увеличьте лимит"
		}
		return nil, &PolicyError{Stage: StageOutput, Reason: reason}
	}

	if cfg.ResponseFormat == FormatJSON && !json.Valid([]byte(trimmed)) {
		return nil, &PolicyError{
			Stage:  StageOutput,
			Reason: "чат настроен на json, но ответ не разбирается как валидный json",
		}
	}

	var warnings []string

	if resp.FinishReason == "length" {
		warnings = append(warnings, "ответ обрезан по лимиту max_tokens")
	}

	if cfg.MaxWords > 0 {
		if words := len(strings.Fields(trimmed)); words > cfg.MaxWords {
			warnings = append(warnings, fmt.Sprintf("в ответе %d слов при лимите %d", words, cfg.MaxWords))
		}
	}

	return warnings, nil
}
