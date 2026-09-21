package agent

import "testing"

// свободная модель: контекст 65 000, резерв под ответ по умолчанию 4 096
func windowConfig(maxTokens int) Config {
	cfg := DefaultConfig("liquid/lfm-2.5-2.6b:free")
	cfg.MaxTokens = maxTokens
	return cfg
}

func TestContextEmptyChat(t *testing.T) {
	state := ContextFor(windowConfig(4096), LastTurn{})

	if state.Carried != 0 || state.LastPrompt != 0 {
		t.Fatalf("в пустом чате занимать нечего: %+v", state)
	}
	// окно занимает только резерв под будущий ответ
	if state.Used != 4096 || state.Available != 65_000-4096 {
		t.Fatalf("резерв должен считаться занятым: %+v", state)
	}
	if state.Full || state.Warning {
		t.Fatalf("пустой чат не должен предупреждать: %+v", state)
	}
}

func TestContextCountsVisibleAnswerOnly(t *testing.T) {
	// рассуждение оплачено, но в историю следующего запроса не уезжает
	state := ContextFor(windowConfig(4096), LastTurn{
		Present:          true,
		PromptTokens:     144,
		CompletionTokens: 329,
		ReasoningTokens:  324,
	})

	if state.LastPrompt != 144 {
		t.Fatalf("lastPrompt должен быть точным числом API, получено %d", state.LastPrompt)
	}
	// 144 прошлого входа + 5 видимых токенов ответа
	if state.Carried != 149 {
		t.Fatalf("в следующий запрос уедет 149 токенов, посчитано %d", state.Carried)
	}
	if state.Used != 149+4096 {
		t.Fatalf("занято = перенос + резерв, получено %d", state.Used)
	}
}

func TestContextReasoningLargerThanCompletionIsClamped(t *testing.T) {
	// провайдер может прислать несогласованные числа; уходить в минус нельзя
	state := ContextFor(windowConfig(4096), LastTurn{
		Present:          true,
		PromptTokens:     100,
		CompletionTokens: 10,
		ReasoningTokens:  50,
	})

	if state.Carried != 100 {
		t.Fatalf("видимая часть не может быть отрицательной, получено %d", state.Carried)
	}
}

func TestContextWarningAndFull(t *testing.T) {
	cases := []struct {
		name    string
		last    LastTurn
		warning bool
		full    bool
	}{
		{"пусто", LastTurn{Present: true, PromptTokens: 1000}, false, false},
		// 48 000 + 4 096 = 52 096 из 65 000 -- это 80.1%
		{"порог предупреждения", LastTurn{Present: true, PromptTokens: 48_000}, true, false},
		{"переполнение", LastTurn{Present: true, PromptTokens: 61_000}, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := ContextFor(windowConfig(4096), tc.last)
			if state.Warning != tc.warning || state.Full != tc.full {
				t.Fatalf("ожидалось warning=%v full=%v, получено %+v", tc.warning, tc.full, state)
			}
		})
	}
}

func TestContextReserveEatsTheWindow(t *testing.T) {
	// способ упереться в лимит руками: max_tokens резервирует почти всё окно,
	// и на вход остаётся тысяча токенов
	state := ContextFor(windowConfig(64_000), LastTurn{Present: true, PromptTokens: 900})

	if state.Available != 100 {
		t.Fatalf("на новый вопрос должно остаться 100 токенов, получено %d", state.Available)
	}
	if !state.Warning {
		t.Fatalf("при 98%% заполненности нужно предупреждение: %+v", state)
	}
}

func TestContextUnknownModelDoesNotBlock(t *testing.T) {
	cfg := windowConfig(4096)
	cfg.Model = "нет такой модели"

	// настройки валидируются при сохранении, но если модель всё же исчезла из каталога,
	// агент не должен запрещать отправку из-за нулевого лимита
	if state := ContextFor(cfg, LastTurn{}); state.Full {
		t.Fatalf("неизвестная модель не повод блокировать чат: %+v", state)
	}
}
