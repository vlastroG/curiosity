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
	StepCompact      = "сжатие истории"
	StepBuildContext = "сборка контекста"
	StepLLM          = "вызов модели"
	StepOutputPolicy = "output policy"
	StepJudge        = "судья"
)

// Plural согласует существительное с числом: 1 сообщение, 2 сообщения, 5 сообщений.
// Нужен трейсу и деталям шагов -- «4 сообщений» читается как опечатка.
func Plural(count int, one, few, many string) string {
	if count < 0 {
		count = -count
	}

	if mod100 := count % 100; mod100 >= 11 && mod100 <= 14 {
		return many
	}

	switch count % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	default:
		return many
	}
}

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
