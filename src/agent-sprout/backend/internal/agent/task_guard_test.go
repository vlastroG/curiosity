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
		Phase:        PhaseCollecting,
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
	if full.Closing {
		t.Fatalf("на сверке задача ещё не закрывается: %+v", full.Task)
	}
	if full.Task.Phase != PhaseConfirming {
		t.Fatalf("фаза должна стать сверкой, получено %q", full.Task.Phase)
	}

	// следующий ход с полным чеклистом -- уже план
	planned := Guard(full.Task, nil, nil, Routing{Decision: DecisionCollect}, fixedID())
	if planned.Decision != DecisionPlan {
		t.Fatalf("после сверки идёт план, получено %q", planned.Decision)
	}
	if !planned.Closing || planned.Task.Phase != PhaseDone {
		t.Fatalf("задача должна закрываться: %+v", planned.Task)
	}
}

func TestGuardReturnsToCollectingIfConfirmationBrokeData(t *testing.T) {
	// пользователь на сверке сказал «нет, температуру я не называл» -- значит пункт
	// снова пуст, и план откладывается
	active := collecting(checklist(3))
	active.Phase = PhaseConfirming
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
	solved := []Task{{ID: "t-старая", Title: "кладка", Phase: PhaseDone, Summary: "итог"}}

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

func TestGuardIgnoresPlaceholderAnswersOnStart(t *testing.T) {
	// слабая модель на первом же ходе помечает весь чеклист как «не знаю»,
	// хотя человека ещё ни о чём не спрашивали
	claim := Routing{
		Decision:  DecisionStart,
		TaskTitle: "стяжка пола",
		Requirements: []Requirement{
			{Key: "основание", Question: "какое основание?"},
			{Key: "площадь", Question: "сколько квадратов?"},
			{Key: "толщина слоя", Question: "какая толщина?"},
			{Key: "температура", Question: "какая температура?"},
			{Key: "инструмент", Question: "чем работаете?"},
		},
		Answers: []Answer{
			{Key: "площадь", Value: "24 квадрата"},
			{Key: "основание", Value: "не знаю"},
			{Key: "толщина слоя", Value: "не применимо"},
		},
	}

	verdict := Guard(nil, nil, nil, claim, func() string { return "id" })

	if verdict.Task == nil {
		t.Fatal("задача должна завестись")
	}
	filled := map[string]string{}
	for _, req := range verdict.Task.Requirements {
		if req.Value != "" {
			filled[req.Key] = req.Value
		}
	}
	if len(filled) != 1 || filled["площадь"] != "24 квадрата" {
		t.Fatalf("заполненным должно остаться только названное человеком: %+v", filled)
	}
	if len(verdict.Overrides) == 0 {
		t.Fatal("понижение должно попасть в предупреждения -- недетерминированность видна")
	}
}

func TestGuardKeepsPlaceholderAnswersWhileCollecting(t *testing.T) {
	// а вот на опросе «не знаю» -- законный ответ: вопрос задан, ответа нет,
	// и в план это уедет отдельным допущением
	task := &Task{
		ID:    "t1",
		Title: "стяжка пола",
		Phase: PhaseCollecting,
		Requirements: []Requirement{
			{Key: "основание", Question: "какое основание?"},
			{Key: "площадь", Question: "сколько квадратов?", Value: "24 квадрата"},
		},
	}

	claim := Routing{
		Decision:     DecisionCollect,
		TaskTitle:    "стяжка пола",
		Requirements: task.Requirements,
		Answers:      []Answer{{Key: "основание", Value: "не знаю"}},
	}

	verdict := Guard(task, nil, nil, claim, func() string { return "id" })

	for _, req := range verdict.Task.Requirements {
		if req.Key == "основание" && req.Value != "не знаю" {
			t.Fatalf("на опросе «не знаю» -- заполненный пункт, получено %q", req.Value)
		}
	}
}

func TestGuardKeepsKnowledgeAttachedToTheTask(t *testing.T) {
	// диспетчер выбирает знания заново на каждом ходе, и один пропуск не должен
	// вымывать их из контекста посреди задачи
	knowledge := []KnowledgeItem{{ID: "k1", Title: "Штукатурные работы", Text: "..."}}
	task := &Task{
		ID:           "t1",
		Title:        "штукатурка стен",
		Phase:        PhaseCollecting,
		Requirements: checklist(5),
		KnowledgeIDs: []string{"k1"},
	}

	verdict := Guard(task, nil, knowledge, Routing{
		Decision:     DecisionCollect,
		TaskTitle:    "штукатурка стен",
		Requirements: task.Requirements,
		// про знание диспетчер на этом ходе промолчал
	}, fixedID())

	if len(verdict.Knowledge) != 1 || verdict.Knowledge[0].ID != "k1" {
		t.Fatalf("прикреплённое к задаче знание должно уехать в запрос: %+v", verdict.Knowledge)
	}
	if len(verdict.Overrides) != 0 {
		t.Fatalf("это не понижение заявки, предупреждать не о чем: %v", verdict.Overrides)
	}
}

func TestGuardForgetsKnowledgeDeletedFromTheReference(t *testing.T) {
	// знание могли удалить из справочника посреди задачи. Диспетчер тут ни при чём,
	// и жаловаться на «несуществующие знания» не за что
	task := &Task{
		ID:           "t1",
		Title:        "штукатурка стен",
		Phase:        PhaseCollecting,
		Requirements: checklist(5),
		KnowledgeIDs: []string{"k-удалённое"},
	}

	verdict := Guard(task, nil, []KnowledgeItem{{ID: "k1", Title: "другое"}}, Routing{
		Decision:     DecisionCollect,
		TaskTitle:    "штукатурка стен",
		Requirements: task.Requirements,
	}, fixedID())

	if len(verdict.Knowledge) != 0 {
		t.Fatalf("удалённое знание подставлять неоткуда: %+v", verdict.Knowledge)
	}
	if len(verdict.Overrides) != 0 {
		t.Fatalf("удаление из справочника -- не вина диспетчера: %v", verdict.Overrides)
	}
}

// confirming -- задача на сверке: чеклист заполнен, собранное уже показано.
func confirming(n int) *Task {
	task := collecting(checklist(n))
	task.Phase = PhaseConfirming
	for i := range task.Requirements {
		task.Requirements[i].Value = "значение"
	}
	return task
}

func TestGuardCorrectionOnReviewPostponesThePlan(t *testing.T) {
	// «нет, площадь 20, а не 18» на сверке не должно немедленно давать план:
	// иначе сверка -- формальность, где ответ один и тот же, подтверждай или нет
	verdict := Guard(confirming(3), nil, nil, Routing{
		Decision: DecisionCollect,
		Answers:  []Answer{{Key: "а-пункт", Value: "другое значение"}},
	}, fixedID())

	if verdict.Decision != DecisionConfirm {
		t.Fatalf("после правки собранное показывается заново, получено %q", verdict.Decision)
	}
	if verdict.Closing || verdict.Task.Phase != PhaseConfirming {
		t.Fatalf("задача должна остаться на сверке: %+v", verdict.Task)
	}
	if verdict.Task.Requirements[0].Value != "другое значение" {
		t.Fatalf("правка обязана примениться: %+v", verdict.Task.Requirements[0])
	}
	if len(verdict.Overrides) == 0 {
		t.Fatal("откладывание плана должно быть видно в предупреждениях")
	}
}

func TestGuardConfirmationWithoutChangesClosesTheTask(t *testing.T) {
	// «да, всё верно» -- диспетчер повторяет те же значения, правки нет
	verdict := Guard(confirming(3), nil, nil, Routing{
		Decision: DecisionCollect,
		Answers:  []Answer{{Key: "а-пункт", Value: "значение"}},
	}, fixedID())

	if verdict.Decision != DecisionPlan || !verdict.Closing {
		t.Fatalf("подтверждение без правок закрывает задачу планом, получено %q", verdict.Decision)
	}
	if verdict.Task.Phase != PhaseDone {
		t.Fatalf("фаза должна стать done, получено %q", verdict.Task.Phase)
	}
}

func TestGuardAllowsMoreThanOneCorrection(t *testing.T) {
	// шанс поправить не одноразовый: после первой правки можно сделать вторую
	first := Guard(confirming(3), nil, nil, Routing{
		Decision: DecisionCollect,
		Answers:  []Answer{{Key: "а-пункт", Value: "первая правка"}},
	}, fixedID())

	second := Guard(first.Task, nil, nil, Routing{
		Decision: DecisionCollect,
		Answers:  []Answer{{Key: "б-пункт", Value: "вторая правка"}},
	}, fixedID())

	if second.Decision != DecisionConfirm || second.Closing {
		t.Fatalf("вторая правка тоже откладывает план, получено %q", second.Decision)
	}
	if second.Task.Phase != PhaseConfirming {
		t.Fatalf("задача должна остаться на сверке: %q", second.Task.Phase)
	}
}

func TestGuardStopsPostponingWhenReviewDragsOn(t *testing.T) {
	// предохранитель: бесконечная правка не должна запирать задачу на сверке
	task := confirming(3)
	task.CollectTurns = maxCollectTurns

	verdict := Guard(task, nil, nil, Routing{
		Decision: DecisionCollect,
		Answers:  []Answer{{Key: "а-пункт", Value: "очередная правка"}},
	}, fixedID())

	if verdict.Decision != DecisionPlan || verdict.Task.Phase != PhaseDone {
		t.Fatalf("после затянувшейся сверки план выдаётся, получено %q → %q",
			verdict.Decision, verdict.Task.Phase)
	}
}

func TestGuardFillingAnEmptyItemIsNotACorrection(t *testing.T) {
	// заполнение пустого пункта -- это продолжение сбора, а не спор с собранным:
	// чеклист становится полным, и машина ведёт на сверку, а не откладывает её
	task := collecting(checklist(2))
	task.Requirements[0].Value = "значение"

	verdict := Guard(task, nil, nil, Routing{
		Decision: DecisionCollect,
		Answers:  []Answer{{Key: "б-пункт", Value: "теперь заполнено"}},
	}, fixedID())

	if verdict.Decision != DecisionConfirm || verdict.Task.Phase != PhaseConfirming {
		t.Fatalf("полный чеклист ведёт на сверку, получено %q → %q",
			verdict.Decision, verdict.Task.Phase)
	}
}

// Пересказ собранного другими буквами -- не правка.
//
// Ради этого теста всё и чинилось: диспетчер, разбирая «всё верно», охотно повторяет
// уже собранные значения, меняя регистр первой буквы. Побайтовое сравнение засчитывало
// это правкой, сверка начиналась заново, и план приходилось подтверждать дважды.
func TestGuardRetellingIsNotACorrection(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"другой регистр первой буквы", "Значение"},
		{"верхний регистр целиком", "ЗНАЧЕНИЕ"},
		{"лишние пробелы по краям", "  значение  "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := Guard(confirming(3), nil, nil, Routing{
				Decision: DecisionCollect,
				Answers:  []Answer{{Key: "а-пункт", Value: tc.value}},
			}, fixedID())

			if verdict.Decision != DecisionPlan || !verdict.Closing {
				t.Fatalf("пересказ того же значения обязан закрывать задачу планом, получено %q",
					verdict.Decision)
			}
			// записанное не трогаем: иначе снимок памяти дёргается там, где ничего не менялось
			if verdict.Task.Requirements[0].Value != "значение" {
				t.Fatalf("значение переписано пересказом: %q", verdict.Task.Requirements[0].Value)
			}
		})
	}
}

// Обратная сторона: содержательное расхождение по-прежнему откладывает план,
// даже если отличие невелико.
func TestGuardRealCorrectionStillPostponesThePlan(t *testing.T) {
	verdict := Guard(confirming(3), nil, nil, Routing{
		Decision: DecisionCollect,
		Answers:  []Answer{{Key: "а-пункт", Value: "значение другое"}},
	}, fixedID())

	if verdict.Decision != DecisionConfirm || verdict.Closing {
		t.Fatalf("правка обязана откладывать план, получено %q", verdict.Decision)
	}
	if verdict.Task.Requirements[0].Value != "значение другое" {
		t.Fatalf("правка не применилась: %q", verdict.Task.Requirements[0].Value)
	}
}

// Правка на ходе, которым задача только входит на сверку, ничего не откладывает:
// это обычный путь, и говорить про отложенный план здесь неправда.
func TestGuardEnteringReviewNeverClaimsPostponement(t *testing.T) {
	task := collecting(checklist(3))
	for i := range task.Requirements {
		task.Requirements[i].Value = "значение"
	}

	verdict := Guard(task, nil, nil, Routing{
		Decision: DecisionCollect,
		Answers:  []Answer{{Key: "а-пункт", Value: "значение другое"}},
	}, fixedID())

	if verdict.Decision != DecisionConfirm || verdict.Task.Phase != PhaseConfirming {
		t.Fatalf("полный чеклист ведёт на сверку, получено %q → %q",
			verdict.Decision, verdict.Task.Phase)
	}
	for _, note := range verdict.Overrides {
		if strings.Contains(note, "откладывается") {
			t.Fatalf("вход на сверку выдан за отложенный план: %q", note)
		}
	}
	if !hasNote(verdict.Overrides, "перед планом обязательна сверка") {
		t.Fatalf("не названа причина перехода: %v", verdict.Overrides)
	}
}

func hasNote(notes []string, substring string) bool {
	for _, note := range notes {
		if strings.Contains(note, substring) {
			return true
		}
	}
	return false
}

// Тот самый разговор, на котором приёмка плана сломалась.
//
// Чеклист и ответы диспетчера взяты из хранилища как есть: человек написал
// «всё верно», а диспетчер повторил четыре собранных значения со строчной буквы
// вместо прописной. Побайтовое сравнение засчитывало это правкой, и сверка
// начиналась заново -- «всё верно» приходилось писать дважды.
func TestGuardRealConfirmationFromTheBrokenChat(t *testing.T) {
	task := &Task{
		ID:           "task-1",
		Title:        "отмостка вокруг дома",
		Phase:        PhaseConfirming,
		CollectTurns: 4,
		Requirements: []Requirement{
			{Key: "основание", Question: "вопрос?", Value: "старая отмостка под демонтаж, под ней предположительно трамбованный песок"},
			{Key: "размеры", Question: "вопрос?", Value: "толщина 200 мм, ширина от стены 1 м, длина по периметру 100 м"},
			{Key: "уклон", Question: "вопрос?", Value: "5%"},
			{Key: "тип конструкции", Question: "вопрос?", Value: "жёсткая (бетон)"},
			{Key: "пирог", Question: "вопрос?", Value: "щебень и бетон"},
			{Key: "материалы", Question: "вопрос?", Value: "армирование минимальное, нужна подсказка по сетке"},
			{Key: "инструмент", Question: "вопрос?", Value: "Самосвал для вывоза демонтируемой отмостки, миксер заедет, подъезд есть"},
			{Key: "погода", Question: "вопрос?", Value: "Москва, нужен прогноз на дни работ"},
			{Key: "сроки", Question: "вопрос?", Value: "При сильном дожде готов отложить на следующую неделю"},
			{Key: "опыт", Question: "вопрос?", Value: "Есть опыт, работает руками не первый год (из профиля)"},
			{Key: "примыкание", Question: "вопрос?", Value: "герметик со шнуром Вилатерм"},
			{Key: "дренаж", Question: "вопрос?", Value: "Нужна ливнёвка по краю: лоток до ливневой канализации"},
		},
	}

	verdict := Guard(task, nil, nil, Routing{
		Decision: DecisionCollect,
		Answers: []Answer{
			{Key: "инструмент", Value: "самосвал для вывоза демонтируемой отмостки, миксер заедет, подъезд есть"},
			{Key: "сроки", Value: "при сильном дожде готов отложить на следующую неделю"},
			{Key: "опыт", Value: "есть опыт, работает руками не первый год (из профиля)"},
			{Key: "дренаж", Value: "нужна ливнёвка по краю: лоток до ливневой канализации"},
		},
	}, fixedID())

	if verdict.Decision != DecisionPlan || !verdict.Closing {
		t.Fatalf("«всё верно» обязано закрывать задачу с первого раза, получено %q", verdict.Decision)
	}
	if verdict.Task.Phase != PhaseDone {
		t.Fatalf("фаза должна стать done, получено %q", verdict.Task.Phase)
	}
	if hasNote(verdict.Overrides, "откладывается") {
		t.Fatalf("план отложен на пустом месте: %v", verdict.Overrides)
	}
}
