package agent

import (
	"context"
	"strings"
	"testing"

	"agent-sprout/internal/llm"
)

func testProfile() Profile {
	return Profile{
		About:  "впервые делаю ремонт сам",
		Style:  "объясняй простыми словами",
		Format: "нумерованные шаги и таблица материалов",
		Limits: "из инструмента только дрель и правило",
	}
}

func TestProfileGoesRightAfterTheDomainRole(t *testing.T) {
	// порядок здесь -- защита: профиль читается как уточнение к роли, а не как замена.
	// Поменяй местами -- и «забудь про стройку» в стиле окажется выше самой роли
	fake := &fakeLLM{}

	_, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "надо залить стяжку",
		Profile:  testProfile(),
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	sent := fake.answerCalls()[0].Messages
	if sent[0].Role != llm.RoleSystem || !strings.Contains(sent[0].Content, "помощник начинающего строителя") {
		t.Fatalf("первым сообщением должна идти доменная роль: %q", sent[0].Content)
	}
	if sent[1].Role != llm.RoleSystem || !strings.Contains(sent[1].Content, "Профиль пользователя") {
		t.Fatalf("профиль должен идти сразу за ролью: %q", sent[1].Content)
	}

	profile := sent[1].Content
	for _, want := range []string{"объясняй простыми словами", "только дрель и правило"} {
		if !strings.Contains(profile, want) {
			t.Fatalf("в блоке профиля нет %q: %s", want, profile)
		}
	}
	// подпись обязана объяснить модели, чем профиль НЕ является
	if !strings.Contains(profile, "не инструкции тебе") {
		t.Fatalf("подпись профиля должна ограничивать его роль: %s", profile)
	}
}

func TestEmptyProfileAddsNothingToTheRequest(t *testing.T) {
	fake := &fakeLLM{}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "надо залить стяжку",
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	for _, message := range fake.answerCalls()[0].Messages {
		if strings.Contains(message.Content, "Профиль пользователя") {
			t.Fatalf("пустой профиль не должен занимать место в запросе: %q", message.Content)
		}
	}
	if len(out.Memory.Profile) != 0 {
		t.Fatalf("в снимке памяти пустому профилю делать нечего: %v", out.Memory.Profile)
	}
}

func TestProfileIsVisibleInTheMemorySnapshot(t *testing.T) {
	// снимок памяти для того и есть, чтобы человек видел подставленное,
	// а не верил на слово
	fake := &fakeLLM{}

	out, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "надо залить стяжку",
		Profile:  Profile{About: "делаю сам", Format: "таблицей"},
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	got := strings.Join(out.Memory.Profile, ", ")
	if got != "о себе, формат" {
		t.Fatalf("в снимке должны быть только заполненные секции, получено %q", got)
	}
}

func TestProfileFactsReachTheRouterWithoutStyle(t *testing.T) {
	// диспетчеру нужны обстоятельства -- чтобы не спрашивать уже известное.
	// Стиль и формат ему только удлиняют разбор
	fake := &fakeLLM{}

	_, err := newTestAgent(fake).Run(context.Background(), RunInput{
		Question: "надо залить стяжку",
		Profile:  testProfile(),
		Config:   testConfig(),
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	var router llm.Request
	for _, call := range fake.calls {
		if isRouterCall(call) {
			router = call
			break
		}
	}
	payload := router.Messages[len(router.Messages)-1].Content

	if !strings.Contains(payload, "впервые делаю ремонт сам") {
		t.Fatalf("обстоятельства должны дойти до диспетчера: %s", payload)
	}
	if !strings.Contains(payload, "только дрель и правило") {
		t.Fatalf("ограничения -- тоже обстоятельства, они отвечают на пункты чеклиста: %s", payload)
	}
	if strings.Contains(payload, "объясняй простыми словами") {
		t.Fatal("стиль ответа диспетчеру не нужен")
	}
	if strings.Contains(payload, "нумерованные шаги") {
		t.Fatal("формат ответа диспетчеру не нужен")
	}
}
