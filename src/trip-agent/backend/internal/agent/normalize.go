package agent

// Нормализация аргументов по схеме инструмента.
//
// Модели, особенно небольшие, часто передают массив или объект JSON-строкой:
// "notes": "[\"…\"]" вместо "notes": ["…"]. Сервер честно отвечает «не тот тип»,
// модель пробует снова тем же способом -- и тратит ходы. Оркестратор знает схему
// каждого инструмента, поэтому исправляет такие аргументы до отправки: строка,
// которая по схеме должна быть массивом или объектом и разбирается как JSON,
// становится массивом или объектом. «None», «null» и пустая строка на месте
// массива или объекта -- это отсутствие значения.
//
// Больше ничего не меняется: значения, которые не лезут в схему иначе как
// догадкой, уходят как есть, и сервер откажет с понятной причиной.

import (
	"encoding/json"
	"strings"
)

// normalizeArgs приводит аргументы к схеме. Возвращает исходные байты, если
// менять нечего или аргументы не объект.
func normalizeArgs(schema map[string]any, raw json.RawMessage) json.RawMessage {
	var args map[string]any
	if schema == nil || json.Unmarshal(raw, &args) != nil {
		return raw
	}
	if !normalizeObject(schema, args) {
		return raw
	}
	fixed, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	return fixed
}

// normalizeObject правит поля объекта по его схеме; true -- что-то поменялось.
func normalizeObject(schema map[string]any, obj map[string]any) bool {
	props, _ := schema["properties"].(map[string]any)
	changed := false
	for name, value := range obj {
		prop, _ := props[name].(map[string]any)
		if prop == nil {
			continue
		}
		fixed, ok := normalizeValue(prop, value)
		if ok {
			changed = true
			if fixed == nil {
				delete(obj, name)
			} else {
				obj[name] = fixed
			}
		}
	}
	return changed
}

// normalizeValue -- одно значение по схеме. ok -- значение изменено (nil -- удалить).
func normalizeValue(prop map[string]any, value any) (any, bool) {
	wantArray, wantObject := hasType(prop, "array"), hasType(prop, "object")
	changed := false

	if s, isString := value.(string); isString && (wantArray || wantObject) {
		trimmed := strings.TrimSpace(s)
		switch {
		case trimmed == "" || strings.EqualFold(trimmed, "none") || strings.EqualFold(trimmed, "null"):
			return nil, true
		case wantArray && strings.HasPrefix(trimmed, "["), wantObject && strings.HasPrefix(trimmed, "{"):
			var parsed any
			if json.Unmarshal([]byte(trimmed), &parsed) != nil {
				return value, false
			}
			value, changed = parsed, true
		default:
			return value, false
		}
	}

	switch v := value.(type) {
	case []any:
		items, _ := prop["items"].(map[string]any)
		if items == nil {
			return value, changed
		}
		for i, item := range v {
			if fixed, ok := normalizeValue(items, item); ok && fixed != nil {
				v[i], changed = fixed, true
			}
		}
		return v, changed
	case map[string]any:
		if normalizeObject(prop, v) {
			changed = true
		}
		return v, changed
	}
	return value, changed
}

func hasType(prop map[string]any, want string) bool {
	switch t := prop["type"].(type) {
	case string:
		return t == want
	case []any:
		for _, x := range t {
			if x == want {
				return true
			}
		}
	}
	return false
}
