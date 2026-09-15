// Package httpapi -- HTTP-обвязка вокруг агента и хранилища.
//
// Здесь нет ни одной строки логики агента: пакет разбирает запрос, достаёт чат
// из хранилища, зовёт agent.Run и раскладывает результат обратно. Границу держим
// специально -- агента должно быть можно вызвать без HTTP.
package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"agent-sprout/internal/agent"
	"agent-sprout/internal/llm"
)

// Коды ошибок. Приходят в интерфейс и определяют, как показать проблему.
const (
	codeBadRequest   = "bad_request"
	codeNotFound     = "not_found"
	codeInputPolicy  = "input_policy"
	codeOutputPolicy = "output_policy"
	codeProvider     = "provider"
	codeOverflow     = "context_overflow"
	codeTagTaken     = "tag_taken"
	codeTitleTaken   = "title_taken"
	codeNoActiveTask = "no_active_task"
	codeInternal     = "internal"
)

// errorBody -- единый формат ошибки во всём API.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("не удалось записать ответ: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

// writeErrorWithChat отдаёт ошибку вместе с состоянием чата.
//
// Нужно там, где лента успела измениться до ошибки: вопрос уже записан, метрики входа
// проставлены, и интерфейсу нужна актуальная версия чата вместе с окном контекста.
func writeErrorWithChat(w http.ResponseWriter, status int, code, message string, payload map[string]any) {
	body := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		body[key] = value
	}
	body["error"] = map[string]string{"code": code, "message": message}
	writeJSON(w, status, body)
}

// classify переводит ошибку агента в код и HTTP-статус.
//
// Разделение важно для интерфейса: отказ политики -- нормальная работа коробки,
// а сбой провайдера -- внешняя авария, и показываются они по-разному.
func classify(err error) (status int, code string) {
	var policyErr *agent.PolicyError
	if errors.As(err, &policyErr) {
		if policyErr.Stage == agent.StageOutput {
			return http.StatusUnprocessableEntity, codeOutputPolicy
		}
		return http.StatusUnprocessableEntity, codeInputPolicy
	}

	var unavailable *agent.ModelUnavailableError
	if errors.As(err, &unavailable) {
		return http.StatusBadRequest, codeBadRequest
	}

	var apiErr *llm.APIError
	if errors.As(err, &apiErr) {
		// наша проверка окна идёт по фактическим числам прошлого вызова и не знает,
		// насколько велик новый вопрос -- поэтому провайдер всё ещё может отбить
		// запрос по длине. Отдаём это отдельным кодом, а не сырым сбоем провайдера
		if mentionsContextLimit(apiErr.Body) {
			return http.StatusUnprocessableEntity, codeOverflow
		}
		return http.StatusBadGateway, codeProvider
	}

	return http.StatusBadGateway, codeProvider
}

// contextLimitMarkers -- по каким словам в ответе провайдера видно, что запрос
// не влез в окно модели. Формулировки у DeepSeek и OpenRouter разные, общего кода нет.
var contextLimitMarkers = []string{
	"context length",
	"context_length",
	"maximum context",
	"context window",
	"too long",
	"exceeds the maximum",
}

func mentionsContextLimit(body string) bool {
	lowered := strings.ToLower(body)
	for _, marker := range contextLimitMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// decodeJSON читает тело запроса с потолком по размеру: без него один запрос
// может съесть память сервера.
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
