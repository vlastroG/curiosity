package agent

import (
	"context"
	"strings"
	"testing"

	"agent-sprout/internal/llm"
)

func factsConfig(enabled bool) Config {
	cfg := testConfig()
	cfg.StickyFacts = enabled
	return cfg
}

func TestFactsDisabledCostsNothing(t *testing.T) {
	fake := &fakeLLM{}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "вопрос",
		Facts:    []Fact{{Key: "имя", Value: "Влад"}},
		Config:   factsConfig(false),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("с выключенными фактами вызов один, получено %d", len(fake.calls))
	}
	if out.Facts != nil {
		t.Fatalf("память не должна обновляться: %+v", out.Facts)
	}

	// выключенные факты не уезжают в запрос, даже если они сохранились на чате
	for _, message := range fake.calls[0].Messages {
		if strings.Contains(message.Content, "Влад") {
			t.Fatalf("выключенная память попала в запрос: %+v", message)
		}
	}
}

func TestFactsUpdatedAfterTheAnswer(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		{Text: "ответ модели", FinishReason: "stop"},
		{Text: `{"facts":[{"key":"имя","value":"Влад"},{"key":"язык","value":"Go"}]}`, FinishReason: "stop"},
	}}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "Меня зовут Влад, пишу на Go",
		Facts:    []Fact{{Key: "имя", Value: "Влад"}},
		Config:   factsConfig(true),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if len(fake.calls) != 2 {
		t.Fatalf("ответ плюс обновление памяти -- два вызова, получено %d", len(fake.calls))
	}

	// накопленная память уезжает в основной запрос отдельным system-сообщением
	sent := fake.calls[0].Messages
	if len(sent) != 3 || sent[1].Role != llm.RoleSystem || !strings.Contains(sent[1].Content, "имя: Влад") {
		t.Fatalf("память должна уехать вторым system-сообщением: %+v", sent)
	}

	// обновление идёт ПОСЛЕ ответа и видит его текст
	if !strings.Contains(fake.calls[1].Messages[1].Content, "ответ модели") {
		t.Fatalf("в обновление памяти должен попасть ответ хода: %q", fake.calls[1].Messages[1].Content)
	}
	if !fake.calls[1].JSONObject || fake.calls[1].Temperature != 0 {
		t.Fatalf("память собирается json-объектом на нулевой температуре: %+v", fake.calls[1])
	}

	if out.Facts == nil || len(out.Facts.Facts) != 2 {
		t.Fatalf("память не обновилась: %+v", out.Facts)
	}
	if out.Facts.Added != 1 || out.Facts.Changed != 0 {
		t.Fatalf("ожидался один новый ключ, получено added=%d changed=%d", out.Facts.Added, out.Facts.Changed)
	}
	if out.Calls != 2 {
		t.Fatalf("вызов памяти считается в ход, получено %d", out.Calls)
	}
}

func TestFactsFailureDoesNotCostTheAnswer(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		{Text: "ответ модели", FinishReason: "stop"},
		{Text: "я не умею в json", FinishReason: "stop"},
	}}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "вопрос",
		Config:   factsConfig(true),
	})
	if err != nil {
		t.Fatalf("сбой памяти не должен ронять ход: %v", err)
	}

	if out.Answer != "ответ модели" {
		t.Fatalf("ответ должен сохраниться, получено %q", out.Answer)
	}
	if out.Facts != nil {
		t.Fatal("несостоявшееся обновление нечего записывать")
	}
	if len(out.Warnings) == 0 {
		t.Fatal("сбой памяти должен попасть в предупреждения")
	}
}

func TestFactsAndCompactionWorkTogether(t *testing.T) {
	fake := &fakeLLM{responses: []llm.Response{
		{Text: "пересказ окна", FinishReason: "stop"},
		{Text: "ответ модели", FinishReason: "stop"},
		{Text: `{"facts":[{"key":"тема","value":"контекст"}]}`, FinishReason: "stop"},
	}}

	cfg := compactConfig(4, true)
	cfg.StickyFacts = true

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "вопрос",
		History:  exchange(4),
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if len(fake.calls) != 3 {
		t.Fatalf("сжатие, ответ и память -- три вызова, получено %d", len(fake.calls))
	}
	if out.Compaction == nil || out.Facts == nil {
		t.Fatalf("на ходе должны были сработать обе стратегии: %+v %+v", out.Compaction, out.Facts)
	}

	// порядок важен: сжатие идёт до ответа, память -- после
	if !strings.Contains(fake.calls[0].Messages[0].Content, "сжимаешь историю") {
		t.Fatalf("первым должен идти вызов сжатия: %q", fake.calls[0].Messages[0].Content)
	}
	if !strings.Contains(fake.calls[2].Messages[0].Content, "память диалога") {
		t.Fatalf("последним должен идти вызов памяти: %q", fake.calls[2].Messages[0].Content)
	}
}

func TestParseFactsHandlesNoiseAndDuplicates(t *testing.T) {
	facts, err := parseFacts("Вот память:\n```json\n" +
		`{"facts":[{"key":"цель","value":"сдать день 10"},{"key":"Цель","value":"дубль"},` +
		`{"key":"","value":"пусто"},{"key":"срок","value":"пятница"}]}` + "\n```")
	if err != nil {
		t.Fatalf("объект должен вырезаться из обёртки: %v", err)
	}

	if len(facts) != 2 {
		t.Fatalf("дубль и пустой ключ должны отсеяться: %+v", facts)
	}
	if facts[0].Key != "цель" || facts[1].Key != "срок" {
		t.Fatalf("порядок и содержимое нарушены: %+v", facts)
	}
}

func TestParseFactsCapsTheMemory(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`{"facts":[`)
	for i := 0; i < maxFacts+15; i++ {
		if i > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`{"key":"ключ`)
		builder.WriteString(string(rune('а' + i%30)))
		builder.WriteString(`","value":"значение"}`)
	}
	builder.WriteString(`]}`)

	facts, err := parseFacts(builder.String())
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(facts) > maxFacts {
		t.Fatalf("память должна обрезаться до %d записей, получено %d", maxFacts, len(facts))
	}
}

func TestParseFactsRejectsEmptySet(t *testing.T) {
	if _, err := parseFacts(`{"facts":[]}`); err == nil {
		t.Fatal("пустой набор -- это не обновление памяти, а потеря")
	}
}

func TestDiffFactsCountsAddedAndChanged(t *testing.T) {
	before := []Fact{{Key: "имя", Value: "Влад"}, {Key: "язык", Value: "Go"}}
	after := []Fact{
		{Key: "имя", Value: "Влад"},         // без изменений
		{Key: "язык", Value: "Go и Python"}, // изменилось
		{Key: "срок", Value: "пятница"},     // новое
	}

	added, changed := diffFacts(before, after)
	if added != 1 || changed != 1 {
		t.Fatalf("ожидалось added=1 changed=1, получено added=%d changed=%d", added, changed)
	}
}
