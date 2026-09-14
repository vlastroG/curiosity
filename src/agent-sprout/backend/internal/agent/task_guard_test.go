package agent

import (
	"strings"
	"testing"
)

// Таблица переходов машины состояний.
//
// Страж -- чистая функция, поэтому здесь нет ни сети, ни модели, ни подставного
// клиента: только состояние на входе, заявка диспетчера и разрешённый переход
// на выходе. Именно эти тесты и держат гарантию «перескочить между задачами нельзя».

func fixedID() func() string {
	return func() string { return "task-1" }
}

func checklist(n int) []Requirement {
	reqs := make([]Requirement, 0, n)
	for i := 0; i < n; i++ {
		reqs = append(reqs, Requirement{
			Key:      string(rune('а'+i)) + "-пункт",
			Question: "вопрос?",
		})
	}
	return reqs
}

func collecting(reqs []Requirement) *Task {
	return &Task{
		ID:           "task-1",
		Title:        "штукатурные работы",
		Status:       TaskCollecting,
		Requirements: reqs,
		CollectTurns: 1,
	}
}

func TestGuardWithoutTask(t *testing.T) {
	cases := []struct {
		name  string
		claim Routing
		want  Decision
	}{
		{
			// собирать нечего: задачи нет
			name:  "сбор без задачи невозможен",
			claim: Routing{Decision: DecisionCollect, TaskTitle: "кладка", Requirements: checklist(5)},
			want:  DecisionStart,
		},
		{
			// конфликтовать не с чем: второй задачи нет
			name:  "отказ по второй задаче без задачи",
			claim: Routing{Decision: DecisionRefuseSecond, TaskTitle: "кладка", Requirements: checklist(5)},
			want:  DecisionStart,
		},
		{
			name:  "задача без вида работ не заводится",
			claim: Routing{Decision: DecisionStart, Requirements: checklist(5)},
			want:  DecisionAmbiguous,
		},
		{
			name:  "задача без чеклиста не заводится",
			claim: Routing{Decision: DecisionStart, TaskTitle: "кладка"},
			want:  DecisionAmbiguous,
		},
		{
			name:  "вне домена проходит как есть",
			claim: Routing{Decision: DecisionRefuseOffTopic},
			want:  DecisionRefuseOffTopic,
		},
		{
			name:  "нормальная инициализация",
			claim: Routing{Decision: DecisionStart, TaskTitle: "кладка", Requirements: checklist(6)},
			want:  DecisionStart,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := Guard(nil, nil, nil, tc.claim, fixedID())
			if verdict.Decision != tc.want {
				t.Fatalf("ожидалось %q, получено %q (%v)", tc.want, verdict.Decision, verdict.Overrides)
			}
		})
	}
}

func TestGuardNeverPlansOnTheTurnTaskWasCreated(t *testing.T) {
	// между постановкой задачи и планом обязан быть хотя бы один ход опроса,
	// иначе «собери исходные данные» превращается в пустую формальность
	claim := Routing{
		Decision:     DecisionStart,
		TaskTitle:    "кладка",
		Requirements: checklist(5),
		Answers: []Answer{
			{Key: "а-пункт", Value: "1"}, {Key: "б-пункт", Value: "2"}, {Key: "в-пункт", Value: "3"},
			{Key: "г-пункт", Value: "4"}, {Key: "д-пункт", Value: "5"},
		},
	}

	verdict := Guard(nil, nil, nil, claim, fixedID())
	if verdict.Decision != DecisionStart {
		t.Fatalf("на ходе инициализации плана быть не может, получено %q", verdict.Decision)
	}
	if verdict.Closing {
		t.Fatal("задача не должна закрываться тем же ходом, каким заведена")
	}
}

func TestGuardBlocksSecondTask(t *testing.T) {
	active := collecting(checklist(5))

	verdict := Guard(active, nil, nil, Routing{
		Decision:     DecisionStart,
		TaskTitle:    "монолитные работы",
		Requirements: checklist(6),
	}, fixedID())

	if verdict.Decision != DecisionRefuseSecond {
		t.Fatalf("вторая задача при активной должна отклоняться, получено %q", verdict.Decision)
	}
	if verdict.Task.Title != "штукатурные работы" {
		t.Fatalf("активная задача не должна подменяться: %q", verdict.Task.Title)
	}
	if len(verdict.Task.Requirements) != 5 {
		t.Fatalf("чеклист активной задачи не должен подменяться: %+v", verdict.Task.Requirements)
	}
	if len(verdict.Overrides) == 0 {
		t.Fatal("подмену задачи надо объяснить, а не замять")
	}
}

func TestGuardKeepsTaskTitleStable(t *testing.T) {
	// модель любит переформулировать вид работ, и без фиксации он поплывёт за пару ходов
	active := collecting(checklist(5))

	verdict := Guard(active, nil, nil, Routing{
		Decision:  DecisionCollect,
		TaskTitle: "оштукатуривание стен",
	}, fixedID())

	if verdict.Task.Title != "штукатурные работы" {
		t.Fatalf("заголовок активной задачи неизменяем, получено %q", verdict.Task.Title)
	}
	if !strings.Contains(strings.Join(verdict.Overrides, " "), "переименовал") {
		t.Fatalf("переименование должно быть отмечено: %v", verdict.Overrides)
	}
}

func TestGuardRestoresRemovedRequirements(t *testing.T) {
	// сократить чеклист -- самый дешёвый способ объявить сбор законченным досрочно
	active := collecting(checklist(6))

	verdict := Guard(active, nil, nil, Routing{
		Decision:     DecisionCollect,
		Requirements: checklist(2),
	}, fixedID())

	if len(verdict.Task.Requirements) != 6 {
		t.Fatalf("пункты чеклиста не должны исчезать: %+v", verdict.Task.Requirements)
	}
}

func TestGuardLimitsNewRequirementsPerTurn(t *testing.T) {
	active := collecting(checklist(5))

	verdict := Guard(active, nil, nil, Routing{
		Decision: DecisionCollect,
		Requirements: []Requirement{
			{Key: "новый-1", Question: "?"},
			{Key: "новый-2", Question: "?"},
			{Key: "новый-3", Question: "?"},
			{Key: "новый-4", Question: "?"},
		},
	}, fixedID())

	if len(verdict.Task.Requirements) != 5+maxNewPerTurn {
		t.Fatalf("за ход можно добавить не больше %d пунктов: %+v", maxNewPerTurn, verdict.Task.Requirements)
	}
	if len(verdict.Overrides) == 0 {
		t.Fatal("отброшенные пункты надо объяснить")
	}
}

func TestGuardTrimsOversizedChecklist(t *testing.T) {
	verdict := Guard(nil, nil, nil, Routing{
		Decision:     DecisionStart,
		TaskTitle:    "кладка",
		Requirements: checklist(20),
	}, fixedID())

	if len(verdict.Task.Requirements) != maxRequirements {
		t.Fatalf("чеклист должен обрезаться до %d, получено %d", maxRequirements, len(verdict.Task.Requirements))
	}
}

func TestGuardConfirmsBeforePlanning(t *testing.T) {
	// критерий окончания сбора: заполненность чеклиста. Мнение модели не спрашивают --
	// в заявке стоит «собираем», а переход всё равно дальше. Но сразу в план нельзя:
	// диспетчер способен пометить пункт собранным, когда пользователь о нём не заикался,
	// и без сверки такая выдумка уедет прямо в план
	active := collecting(checklist(3))

	full := Guard(active, nil, nil, Routing{
		Decision: DecisionCollect,
		Answers: []Answer{
			{Key: "а-пункт", Value: "кирпич"},
			{Key: "б-пункт", Value: "40 м2"},
			{Key: "в-пункт", Value: "не знаю"},
		},
	}, fixedID())

	if full.Decision != DecisionConfirm {
		t.Fatalf("заполненный чеклист ведёт на сверку, получено %q", full.Decision)
	}
	if full.Closing || full.Task.Status != TaskCollecting {
		t.Fatalf("на сверке задача ещё не закрывается: %+v", full.Task)
	}
	if !full.Task.Confirmed {
		t.Fatal("после сверки задача должна быть помечена подтверждённой")
	}

	// следующий ход с полным чеклистом -- уже план
	planned := Guard(full.Task, nil, nil, Routing{Decision: DecisionCollect}, fixedID())
	if planned.Decision != DecisionPlan {
		t.Fatalf("после сверки идёт план, получено %q", planned.Decision)
	}
	if !planned.Closing || planned.Task.Status != TaskDone {
		t.Fatalf("задача должна закрываться: %+v", planned.Task)
	}
}

func TestGuardReturnsToCollectingIfConfirmationBrokeData(t *testing.T) {
	// пользователь на сверке сказал «нет, температуру я не называл» -- значит пункт
	// снова пуст, и план откладывается
	active := collecting(checklist(3))
	active.Confirmed = true
	for i := range active.Requirements {
		active.Requirements[i].Value = "значение"
	}

	verdict := Guard(active, nil, nil, Routing{
		Decision:     DecisionCollect,
		Requirements: []Requirement{{Key: "новый", Question: "а это?"}},
	}, fixedID())

	if verdict.Decision != DecisionCollect {
		t.Fatalf("новый незаполненный пункт возвращает в сбор, получено %q", verdict.Decision)
	}
}

func TestGuardSafetyValveStopsEndlessInterview(t *testing.T) {
	active := collecting(checklist(6))
	active.CollectTurns = maxCollectTurns

	verdict := Guard(active, nil, nil, Routing{Decision: DecisionCollect}, fixedID())

	if verdict.Decision != DecisionPlan {
		t.Fatalf("предохранитель должен перевести в план, получено %q", verdict.Decision)
	}
	if len(verdict.Task.Missing()) == 0 {
		t.Fatal("план по предохранителю выдаётся именно с незаполненными пунктами")
	}
	if !strings.Contains(strings.Join(verdict.Overrides, " "), "допущениями") {
		t.Fatalf("переход по предохранителю надо объяснить: %v", verdict.Overrides)
	}
}

func TestGuardKeepsVolunteeredAnswers(t *testing.T) {
	// пользователь сказал то, о чём не спрашивали: терять это нельзя
	active := collecting(checklist(5))

	verdict := Guard(active, nil, nil, Routing{
		Decision: DecisionCollect,
		Answers:  []Answer{{Key: "сроки", Value: "до пятницы"}},
	}, fixedID())

	found := false
	for _, req := range verdict.Task.Requirements {
		if req.Key == "сроки" && req.Value == "до пятницы" {
			found = true
		}
	}
	if !found {
		t.Fatalf("добровольно названные данные должны сохраняться: %+v", verdict.Task.Requirements)
	}
}

func TestGuardDropsInventedKnowledge(t *testing.T) {
	knowledge := []KnowledgeItem{{ID: "k1", Title: "СП 63", Text: "..."}}

	verdict := Guard(nil, nil, knowledge, Routing{
		Decision:     DecisionStart,
		TaskTitle:    "монолит",
		Requirements: checklist(5),
		KnowledgeIDs: []string{"k1", "k-выдуманный"},
	}, fixedID())

	if len(verdict.Knowledge) != 1 || verdict.Knowledge[0].ID != "k1" {
		t.Fatalf("несуществующие знания должны отбрасываться: %+v", verdict.Knowledge)
	}
	if !strings.Contains(strings.Join(verdict.Overrides, " "), "несуществующих") {
		t.Fatalf("выдуманную ссылку надо отметить: %v", verdict.Overrides)
	}
}

func TestGuardDropsForeignRelatedTask(t *testing.T) {
	solved := []Task{{ID: "t-старая", Title: "кладка", Status: TaskDone, Summary: "итог"}}

	verdict := Guard(nil, solved, nil, Routing{
		Decision:      DecisionStart,
		TaskTitle:     "кладка",
		Requirements:  checklist(5),
		RelatedTaskID: "t-чужая",
	}, fixedID())

	if verdict.RelatedTaskID != "" {
		t.Fatalf("ссылка на чужую задачу должна отбрасываться: %q", verdict.RelatedTaskID)
	}

	ok := Guard(nil, solved, nil, Routing{
		Decision:      DecisionStart,
		TaskTitle:     "кладка",
		Requirements:  checklist(5),
		RelatedTaskID: "t-старая",
	}, fixedID())
	if ok.RelatedTaskID != "t-старая" {
		t.Fatalf("ссылка на решённую задачу этого чата должна сохраняться: %q", ok.RelatedTaskID)
	}
}

func TestGuardAllowsOffTopicDuringTask(t *testing.T) {
	// спросить про постороннее посреди задачи можно: задача от этого не страдает
	active := collecting(checklist(5))

	verdict := Guard(active, nil, nil, Routing{Decision: DecisionRefuseOffTopic}, fixedID())

	if verdict.Decision != DecisionRefuseOffTopic {
		t.Fatalf("отказ по домену должен проходить как есть, получено %q", verdict.Decision)
	}
	if verdict.Task == nil || verdict.Task.CollectTurns != 1 {
		t.Fatalf("посторонний вопрос не должен тратить ход опроса: %+v", verdict.Task)
	}
}
