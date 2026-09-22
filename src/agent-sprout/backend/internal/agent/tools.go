package agent

// Внешние инструменты: что агент показывает модели и что делает с её заявками.
//
// Это единственное место во всём конвейере, где решение принимает модель, а не код.
// Везде остальное наоборот: страж решает, что делать на этом ходе, машина состояний
// решает, разрешён ли переход, арифметика решает, собраны ли данные. Здесь уступка
// осознанная -- знать, нужна ли погода этой задаче, может только тот, кто читает
// разговор. Код решает другое, и не менее важное: доступны ли инструменты вообще
// (Definitions), сколько раз подряд можно к ним сходить (maxToolRounds) и что
// делать, когда вызов не удался.
//
// Ход с инструментами выглядит так:
//
//	модель ──► «вызови get_forecast{place:"Москва"}»
//	           агент ──► MCP-сервер ──► ответ текстом
//	модель ◄── результат вызова сообщением с ролью tool
//	модель ──► ответ человеку, уже с учётом погоды

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agent-sprout/internal/llm"
)

// ToolRefusal -- инструмент отработал, но по существу ответил отказом: не нашёл
// место, не смог посчитать.
//
// Отдельный тип, потому что это не то же самое, что недоступный сервер. Отказ --
// содержательный ответ, и модели он уходит слово в слово: «место не найдено» --
// повод переспросить человека, а не повод считать инструмент сломанным. В трейсе
// шаг при этом всё равно помечен неуспешным: ответа по существу не получено.
type ToolRefusal struct{ Text string }

func (e *ToolRefusal) Error() string { return e.Text }

// ToolBox -- внешние инструменты, доступные модели.
//
// Интерфейс, а не конкретный клиент, ровно по той же причине, что и Completer:
// тесты гоняются без сети и без поднятого MCP-сервера. Про MCP этот пакет
// не знает ничего -- ни про транспорт, ни про протокол.
type ToolBox interface {
	// Definitions -- что показать модели. Имена, описания и схемы придумывает
	// не агент: они приходят от сервера инструментов и уезжают модели как есть
	Definitions(ctx context.Context) ([]llm.Tool, error)
	// Call исполняет одну заявку и возвращает результат текстом -- в том виде,
	// в каком он уедет модели обратно
	Call(ctx context.Context, name, arguments string) (string, error)
}

// maxToolRounds -- сколько раз за один ход модель может сходить к инструментам.
//
// Предохранитель того же рода, что maxCollectTurns у опроса: модель, зациклившаяся
// на вызовах, это не дотошность, а зависание -- за которое к тому же платят,
// потому что каждый круг это отдельный вызов модели с растущим контекстом.
// Трёх кругов хватает на «найти место, уточнить место, спросить погоду».
const maxToolRounds = 3

// toolDefinitions -- инструменты, которые уедут модели на этом ходе.
//
// Здесь сходится весь выключатель, и все три условия равноправны: сервер
// инструментов настроен в окружении, чат его не выключил, и модель умеет их звать.
// Не выполнено хоть одно -- инструментов нет, и ход идёт ровно как раньше.
func (a *Agent) toolDefinitions(ctx context.Context, model Model, cfg Config) ([]llm.Tool, error) {
	if a.tools == nil || !cfg.Weather || !model.Tools {
		return nil, nil
	}
	return a.tools.Definitions(ctx)
}

// callTools исполняет заявки модели и возвращает сообщения с результатами.
//
// На каждую заявку ровно один ответ с её tool_call_id, в том же порядке:
// пропущенная заявка ломает следующий запрос, провайдер требует ответ на все.
//
// Неудача вызова ход не отменяет -- в отличие от сбоя диспетчера или сжатия.
// Текст ошибки уходит модели результатом, и дальше решает она: переспросить
// человека или обойтись без погоды. План без погоды хуже плана с погодой,
// но лучше отсутствия плана.
func (a *Agent) callTools(ctx context.Context, trace *tracer, calls []llm.ToolCall) []llm.Message {
	messages := make([]llm.Message, 0, len(calls))

	for _, call := range calls {
		stepStart := time.Now()

		result, err := a.tools.Call(ctx, call.Function.Name, call.Function.Arguments)
		var refusal *ToolRefusal
		switch {
		case err == nil:
			trace.record(StepTool, stepStart, true, toolDetail(call, result))
		case errors.As(err, &refusal):
			result = refusal.Text
			trace.record(StepTool, stepStart, false, toolDetail(call, result))
		default:
			result = "инструмент не сработал: " + err.Error()
			trace.record(StepTool, stepStart, false, toolDetail(call, result))
		}

		messages = append(messages, llm.Message{
			Role:       llm.RoleTool,
			ToolCallID: call.ID,
			Content:    result,
		})
	}

	return messages
}

// toolDetail -- строка трейса про один вызов: что спросили и что ответили.
//
// Ради неё половина работы и затевалась. «Видно, что происходит» -- обещание
// README, и поход агента в чужой сервис обязан быть виден так же, как вызов
// модели: с именем инструмента, аргументами и началом ответа.
func toolDetail(call llm.ToolCall, result string) string {
	return fmt.Sprintf("%s(%s) → %s",
		call.Function.Name,
		shorten(call.Function.Arguments, 120),
		shorten(firstLine(result), 160))
}

// firstLine -- результаты инструментов бывают многострочными, а в трейсе на шаг
// отведена одна строка.
func firstLine(text string) string {
	for i, r := range text {
		if r == '\n' {
			return text[:i]
		}
	}
	return text
}

// shorten режет по символам, а не по байтам: обрыв посреди кириллической буквы
// превращает хвост строки в мусор.
func shorten(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
