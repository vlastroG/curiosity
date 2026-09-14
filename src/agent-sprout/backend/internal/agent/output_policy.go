package agent

import (
	"strings"

	"agent-sprout/internal/llm"
)

// checkOutput -- этап после вызова модели.
//
// Различает два вида нарушений. Мягкие возвращаются предупреждениями: ответ всё равно
// показывается, но рядом написано, чем он плох. Жёсткие -- это PolicyError: показывать
// нечего, и вместо пустого пузыря пользователь видит объяснение.
func checkOutput(text string, resp llm.Response) ([]string, error) {
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

	var warnings []string

	if resp.FinishReason == "length" {
		warnings = append(warnings, "ответ обрезан по лимиту max_tokens")
	}

	return warnings, nil
}

// trimForError укорачивает чужой текст в сообщении об ошибке: целиком он ломает
// вёрстку, а для понимания причины хватает начала.
func trimForError(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
