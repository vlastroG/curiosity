package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agent-sprout/internal/llm"
)

// exchange собирает окно из n сообщений: вопрос, ответ, вопрос, ответ…
func exchange(n int) []Message {
	window := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		window = append(window, Message{Role: role, Content: "сообщение окна"})
	}
	return window
}

func compactConfig(depth int, summarize bool) Config {
	cfg := testConfig()
	cfg.HistoryDepth = depth
	cfg.SummarizeHistory = summarize
	return cfg
}

func TestCompactionDoesNotHappenBeforeWindowIsFull(t *testing.T) {
	fake := &fakeLLM{}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "вопрос",
		History:  exchange(3),
		Config:   compactConfig(4, true),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if out.Compaction != nil {
		t.Fatalf("окно не заполнено, сжимать рано: %+v", out.Compaction)
	}
	if calls := fake.answerCalls(); len(calls) != 1 {
		t.Fatalf("должен быть один содержательный вызов, получено %d", len(calls))
	}
	if out.HistoryMessages != 3 {
		t.Fatalf("окно должно уехать целиком, отправлено %d", out.HistoryMessages)
	}
}

func TestCompactionReplacesWindowWithSummary(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		{Text: "Пересказ: пользователя зовут Влад, обсуждали Go.", FinishReason: "stop",
			Usage: llm.Usage{PromptTokens: 400, CompletionTokens: 60}},
		{Text: "ответ", FinishReason: "stop", Usage: llm.Usage{PromptTokens: 120, CompletionTokens: 30}},
	}}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "новый вопрос",
		History:  exchange(4),
		Config:   compactConfig(4, true),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if calls := fake.answerCalls(); len(calls) != 2 {
		t.Fatalf("сжатие плюс основной вызов -- это два обращения, получено %d", len(calls))
	}
	if out.Compaction == nil || out.Compaction.Covered != 4 {
		t.Fatalf("отметка о сжатии не заполнена: %+v", out.Compaction)
	}
	if out.Compaction.Recursive {
		t.Fatal("первое сжатие не рекурсивное: прошлого пересказа не было")
	}

	// в основной запрос уехал пересказ, а самих сообщений окна нет
	sent := fake.answerCalls()[1].Messages
	if !containsContent(sent, "Влад") {
		t.Fatalf("пересказ должен уехать в запрос: %+v", sent)
	}
	for _, message := range sent {
		if message.Content == "сообщение окна" {
			t.Fatalf("после сжатия сообщений окна в запросе быть не должно: %+v", sent)
		}
	}
	if out.HistoryMessages != 0 {
		t.Fatalf("после сжатия сообщений окна не остаётся, отправлено %d", out.HistoryMessages)
	}

	// деньги сжатия живут на своей отметке и в ход не приплюсовываются
	if out.Calls != 1 {
		t.Fatalf("ход -- это один основной вызов, посчитано %d", out.Calls)
	}
}

func TestCompactionIsRecursiveOnSecondPass(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		{Text: "общий пересказ", FinishReason: "stop"},
		{Text: "ответ", FinishReason: "stop"},
	}}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "вопрос",
		History:  exchange(4),
		Summary:  "пересказ первого окна",
		Config:   compactConfig(4, true),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if out.Compaction == nil || !out.Compaction.Recursive {
		t.Fatalf("второе сжатие должно быть помечено рекурсивным: %+v", out.Compaction)
	}

	// прошлый пересказ обязан попасть на вход сжатия, иначе память первого окна теряется
	payload := fake.answerCalls()[0].Messages[1].Content
	if !strings.Contains(payload, "пересказ первого окна") {
		t.Fatalf("в сжатие не попал прошлый пересказ: %q", payload)
	}
	if fake.answerCalls()[0].Temperature != 0 {
		t.Fatalf("пересказ должен собираться на нулевой температуре, получено %v",
			fake.answerCalls()[0].Temperature)
	}
}

func TestCompactionDisabledDropsWindowButKeepsSummary(t *testing.T) {
	fake := &fakeLLM{}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "вопрос",
		History:  exchange(4),
		Summary:  "пересказ, накопленный раньше",
		Config:   compactConfig(4, false),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if calls := fake.answerCalls(); len(calls) != 1 {
		t.Fatalf("без сжатия лишнего вызова быть не должно, получено %d", len(calls))
	}
	if out.Compaction == nil || !out.Compaction.Dropped || out.Compaction.Covered != 4 {
		t.Fatalf("отметка об отбрасывании не заполнена: %+v", out.Compaction)
	}
	if out.HistoryMessages != 0 {
		t.Fatalf("окно должно быть отброшено, отправлено %d", out.HistoryMessages)
	}

	// уже оплаченная память при этом остаётся: тумблер решает судьбу окна,
	// а не судьбу накопленного пересказа
	if sent := fake.answerCalls()[0].Messages; !containsContent(sent, "накопленный раньше") {
		t.Fatalf("сохранённый пересказ должен уехать в запрос: %+v", sent)
	}
}

func TestCompactionFailureCancelsTheTurn(t *testing.T) {
	fake := &fakeLLM{err: &llm.APIError{Provider: "тест", Status: 500, Body: "упал"}}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "вопрос",
		History:  exchange(4),
		Config:   compactConfig(4, true),
	})

	if err == nil {
		t.Fatal("сбой сжатия должен отменять ход")
	}
	if !strings.Contains(err.Error(), "сжатие истории") {
		t.Fatalf("ошибка должна называть этап: %v", err)
	}

	// тип ошибки провайдера обязан пережить обёртку, иначе HTTP-слой отдаст 500
	// вместо 502 и потеряет распознавание переполнения контекста
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("ошибка провайдера должна оставаться распознаваемой: %v", err)
	}

	if calls := fake.answerCalls(); len(calls) != 1 {
		t.Fatalf("основного вызова быть не должно, обращений: %d", len(calls))
	}
	if out.Compaction != nil {
		t.Fatalf("несостоявшееся сжатие нечего записывать: %+v", out.Compaction)
	}
}

func TestCompactionRejectsEmptySummary(t *testing.T) {
	// пустой пересказ хуже отсутствия сжатия: окно уже нельзя было бы вернуть
	fake := &fakeLLM{responses: []llm.Response{{Text: "   ", FinishReason: "length"}}}

	_, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "вопрос",
		History:  exchange(4),
		Config:   compactConfig(4, true),
	})

	if err == nil {
		t.Fatal("пустой пересказ должен отменять ход")
	}
	if calls := fake.answerCalls(); len(calls) != 1 {
		t.Fatalf("основного вызова быть не должно, обращений: %d", len(calls))
	}
}

// containsContent -- есть ли среди сообщений запроса подстрока.
func containsContent(messages []llm.Message, needle string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}
