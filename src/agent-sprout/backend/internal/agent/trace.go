package agent

import "time"

// Step -- один этап работы агента. Трейс показывается в интерфейсе под ответом:
// по нему видно, что именно коробка сделала с запросом и сколько это заняло.
type Step struct {
	Name       string `json:"name"`
	DurationMs int    `json:"durationMs"`
	OK         bool   `json:"ok"`
	Detail     string `json:"detail,omitempty"`
}

// Названия этапов конвейера.
const (
	StepInputPolicy  = "input policy"
	StepBuildContext = "сборка контекста"
	StepLLM          = "вызов модели"
	StepOutputPolicy = "output policy"
	StepJudge        = "судья"
)

// tracer накапливает этапы по ходу конвейера.
type tracer struct {
	steps []Step
}

// record закрывает этап, замеряя время от startedAt.
func (t *tracer) record(name string, startedAt time.Time, ok bool, detail string) {
	t.steps = append(t.steps, Step{
		Name:       name,
		DurationMs: int(time.Since(startedAt).Milliseconds()),
		OK:         ok,
		Detail:     detail,
	})
}
