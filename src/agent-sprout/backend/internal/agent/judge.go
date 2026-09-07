package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-sprout/internal/llm"
)

// JudgeVerdict -- оценка ответа вторым вызовом модели.
//
// Судья -- необязательный слой коробки: он включается тумблером в настройках чата
// и удваивает число вызовов. Его ошибка никогда не роняет основной ответ.
type JudgeVerdict struct {
	Score     int       `json:"score"`
	Verdict   string    `json:"verdict"`
	Usage     llm.Usage `json:"usage"`
	Cost      Cost      `json:"cost"`
	LatencyMs int       `json:"latencyMs"`
}

// judgeSystem -- промпт судьи. Слово "json" обязано присутствовать: запрос уходит
// с response_format=json_object, и провайдеры это проверяют.
const judgeSystem = "Ты — строгий оценщик ответов языковой модели. " +
	"Тебе дают вопрос пользователя и ответ модели. Сам на вопрос не отвечай. " +
	"Оцени ответ по шкале от 1 до 5: 1 — не отвечает на вопрос или содержит грубые ошибки, " +
	"3 — отвечает, но поверхностно или с неточностями, 5 — точный, полный и по делу. " +
	"Верни ровно один json-объект вида {\"score\": число, \"verdict\": \"одно предложение\"} " +
	"без текста до и после."

// judgeMaxTokens -- вердикт занимает одну строку, большой бюджет ему не нужен.
// У рассуждающих моделей часть уйдёт на внутреннее рассуждение, поэтому не совсем впритык.
const judgeMaxTokens = 1024

// judge оценивает ответ вторым вызовом той же модели: сравнивать оценки между чатами
// имеет смысл только при одинаковом оценщике, а "той же моделью" -- самый предсказуемый
// вариант, который к тому же работает и на бесплатной модели при отладке.
func (a *Agent) judge(ctx context.Context, model Model, provider llm.Provider, question, answer string) (*JudgeVerdict, error) {
	resp, err := a.llm.Chat(ctx, provider, llm.Request{
		Model: model.ID,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: judgeSystem},
			{Role: llm.RoleUser, Content: "Вопрос пользователя:\n" + question + "\n\nОтвет модели:\n" + answer},
		},
		// нулевая температура: оценка должна быть воспроизводимой
		Temperature: 0,
		MaxTokens:   judgeMaxTokens,
		TopP:        1,
		JSONObject:  true,
	})
	if err != nil {
		return nil, err
	}

	verdict, err := parseVerdict(resp.Text)
	if err != nil {
		return nil, err
	}

	verdict.Usage = resp.Usage
	verdict.Cost = model.Cost(resp.Usage, a.now())
	verdict.LatencyMs = resp.LatencyMs
	return verdict, nil
}

// parseVerdict разбирает ответ судьи.
//
// Слабые модели любят обернуть json в пояснения или в markdown-ограждение, поэтому
// сначала пробуем разобрать целиком, а затем вырезаем первый объект по фигурным скобкам.
func parseVerdict(text string) (*JudgeVerdict, error) {
	candidate := strings.TrimSpace(text)

	var parsed struct {
		Score   json.Number `json:"score"`
		Verdict string      `json:"verdict"`
	}

	if err := json.Unmarshal([]byte(candidate), &parsed); err != nil {
		start := strings.Index(candidate, "{")
		end := strings.LastIndex(candidate, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("вердикт судьи не разобрался как json: %s", trimForError(candidate))
		}
		if err := json.Unmarshal([]byte(candidate[start:end+1]), &parsed); err != nil {
			return nil, fmt.Errorf("вердикт судьи не разобрался как json: %s", trimForError(candidate))
		}
	}

	score, err := parsed.Score.Float64()
	if err != nil {
		return nil, fmt.Errorf("оценка судьи не число: %q", parsed.Score.String())
	}

	rounded := int(score + 0.5)
	if rounded < 1 {
		rounded = 1
	}
	if rounded > 5 {
		rounded = 5
	}

	return &JudgeVerdict{Score: rounded, Verdict: strings.TrimSpace(parsed.Verdict)}, nil
}

func trimForError(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
