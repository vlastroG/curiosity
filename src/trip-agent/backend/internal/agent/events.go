package agent

// События прогона -- то, что видит интерфейс.
//
// Интерфейс показывает не лог, а «что происходит сейчас». Для этого агент
// сообщает о каждом шаге отдельным событием: модель думает, модель решила,
// инструмент вызван, инструмент ответил. События сохраняются вместе с прогоном,
// поэтому ход работы старого плана открывается так же, как живой.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Типы событий.
const (
	EventQueued       = "queued"
	EventStarted      = "started"
	EventServers      = "servers"
	EventThinking     = "thinking"
	EventDecided      = "decided"
	EventToolStarted  = "tool_started"
	EventToolFinished = "tool_finished"
	EventInjection    = "injection"
	EventNudge        = "nudge"
	EventAnswer       = "answer"
	EventVerify       = "verify"
	EventPlan         = "plan"
	EventFinished     = "finished"
	EventFailed       = "failed"
)

// Event -- одно событие. Поля заполняются по типу.
type Event struct {
	Seq  int    `json:"seq"`
	At   string `json:"at"`
	Type string `json:"type"`

	Turn       int             `json:"turn,omitempty"`
	CallID     string          `json:"callId,omitempty"`
	Server     string          `json:"server,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	OK         *bool           `json:"ok,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	DurationMs int64           `json:"durationMs,omitempty"`
	Text       string          `json:"text,omitempty"`
	Quote      string          `json:"quote,omitempty"`
	Calls      []PlannedCall   `json:"calls,omitempty"`
	Checks     []Check         `json:"checks,omitempty"`
	Servers    json.RawMessage `json:"servers,omitempty"`
	Plan       json.RawMessage `json:"plan,omitempty"`
	Status     string          `json:"status,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// PlannedCall -- вызов, который выбрала модель.
type PlannedCall struct {
	CallID string          `json:"callId"`
	Server string          `json:"server"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args"`
}

// maxResultBytes -- сколько сырого результата уходит в событие: интерфейсу
// хватит, а история не раздуется.
const maxResultBytes = 12 << 10

func clipResult(raw json.RawMessage) json.RawMessage {
	if len(raw) <= maxResultBytes {
		return raw
	}
	quoted, _ := json.Marshal(string(raw[:maxResultBytes]) + "…")
	return quoted
}

// summarize -- итог шага одной строкой, по structuredContent.
func summarize(server, tool string, raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	str := func(v any) string { return strings.TrimSpace(fmt.Sprint(v)) }
	num := func(v any) float64 { f, _ := v.(float64); return f }
	switch tool {
	case "find_city":
		c, _ := m["city"].(map[string]any)
		return fmt.Sprintf("%s, %s · %.4f, %.4f · %s", str(c["name"]), str(c["country"]), num(c["latitude"]), num(c["longitude"]), str(c["timezone"]))
	case "sights":
		list, _ := m["sights"].([]any)
		names := []string{}
		for i, s := range list {
			if i == 4 {
				names = append(names, "…")
				break
			}
			if sm, ok := s.(map[string]any); ok {
				names = append(names, str(sm["title"]))
			}
		}
		return fmt.Sprintf("мест: %d — %s", len(list), strings.Join(names, ", "))
	case "sight_info":
		return str(m["title"])
	case "trip_weather":
		days, _ := m["days"].([]any)
		kind := "прогноз"
		if str(m["source"]) == "climate" {
			kind = "климатическая норма"
		}
		if len(days) == 0 {
			return kind
		}
		lo, hi := 100.0, -100.0
		for _, d := range days {
			if dm, ok := d.(map[string]any); ok {
				lo, hi = min(lo, num(dm["min"])), max(hi, num(dm["max"]))
			}
		}
		return fmt.Sprintf("%s на %d дн. · от %+.0f° до %+.0f°", kind, len(days), lo, hi)
	case "find_place":
		return "места найдены"
	case "get_forecast":
		return "прогноз получен"
	case "country_currency":
		return fmt.Sprintf("%s → %s", str(m["countryCode"]), str(m["currency"]))
	case "convert":
		return fmt.Sprintf("%s %s = %s %s (курс на %s)", money(num(m["amount"])), str(m["from"]), money(num(m["result"])), str(m["to"]), str(m["date"]))
	case "budget_split":
		return fmt.Sprintf("на человека в день %s %s", money(num(m["perPersonPerDay"])), str(m["currency"]))
	case "trip_create":
		return fmt.Sprintf("план %s, нужно дней: %d", str(m["trip_id"]), len(asList(m["missing"])))
	case "trip_set_budget":
		return "бюджет записан"
	case "trip_add_day":
		missing := asList(m["missing"])
		if len(missing) == 0 {
			return "все дни на месте"
		}
		return fmt.Sprintf("осталось дней: %d", len(missing))
	case "trip_publish":
		return "опубликован: " + str(m["file"])
	}
	return ""
}

func asList(v any) []any {
	list, _ := v.([]any)
	return list
}

func money(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 && s[i-1] != '-' {
			out = append(out, ' ')
		}
		out = append(out, s[i])
	}
	return string(out)
}
