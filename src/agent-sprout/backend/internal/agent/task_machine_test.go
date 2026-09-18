package agent

import (
	"testing"
)

// Тесты самой таблицы переходов.
//
// Тесты стража проверяют поведение на сценариях, а эти -- свойства машины как
// таковой: что в таблице нет дыр и противоречий и что правило «нельзя перепрыгнуть
// этап» держится структурой, а не порядком строк.

var allPhases = []TaskPhase{PhaseNone, PhaseCollecting, PhaseConfirming, PhaseDone, PhaseCancelled}

var allEvents = []Decision{
	DecisionStart, DecisionCollect, DecisionConfirm, DecisionPlan,
	DecisionRefuseOffTopic, DecisionRefuseSecond, DecisionAmbiguous, DecisionCancel,
}

func TestMachineHasNoContradictoryRows(t *testing.T) {
	// два безусловных перехода из одной фазы по одному событию -- это спор о том,
	// куда идти, и выигрывает тот, кто выше в списке. Такого быть не должно
	seen := map[TaskPhase]map[Decision]bool{}

	for _, step := range machine {
		if step.When != nil {
			continue
		}
		if seen[step.From] == nil {
			seen[step.From] = map[Decision]bool{}
		}
		if seen[step.From][step.Event] {
			t.Fatalf("из фазы %q по событию %q два безусловных перехода", step.From, step.Event)
		}
		seen[step.From][step.Event] = true
	}
}

func TestMachineTerminalPhasesAreTerminal(t *testing.T) {
	// закрытая и прерванная задача событий не принимает: следующая заявка
	// относится уже к новой задаче
	for _, phase := range []TaskPhase{PhaseDone, PhaseCancelled} {
		if events := allowedEvents(phase); len(events) != 0 {
			t.Fatalf("из фазы %q не должно быть переходов, есть %v", phase, events)
		}
		if phase.Active() {
			t.Fatalf("фаза %q не может считаться активной", phase)
		}
	}
}

func TestMachineReachesEveryPhase(t *testing.T) {
	// фаза, в которую нельзя попасть, -- это мёртвая строка в таблице
	reached := map[TaskPhase]bool{PhaseNone: true}
	for _, step := range machine {
		reached[step.To] = true
	}

	for _, phase := range allPhases {
		if !reached[phase] {
			t.Fatalf("в фазу %q не ведёт ни один переход", phase)
		}
	}
}

func TestMachineClosesTaskOnlyThroughConfirmation(t *testing.T) {
	// главное правило домена: план -- это финал, и попасть в него можно только
	// после сверки. Единственное исключение -- предохранитель от бесконечного
	// опроса, и оно обязано быть ровно одно
	valves := 0

	for _, step := range machine {
		if step.To != PhaseDone {
			continue
		}
		if step.Event != DecisionPlan {
			t.Fatalf("задачу закрывает событие %q, а закрывать её должен только план", step.Event)
		}
		switch step.From {
		case PhaseConfirming:
			// нормальный путь
		case PhaseCollecting:
			valves++
			if step.When == nil {
				t.Fatal("путь в план мимо сверки обязан быть ограничен условием")
			}
		default:
			t.Fatalf("в план из фазы %q ходить нельзя", step.From)
		}
	}

	if valves != 1 {
		t.Fatalf("предохранитель должен быть ровно один, найдено %d", valves)
	}
}

func TestMachineForcedStepsOutrankTheDispatcher(t *testing.T) {
	// forced-переходы существуют затем, чтобы модель не могла перепрыгнуть этап,
	// поэтому в тех же условиях они обязаны находиться раньше заявки
	full := *collecting(checklist(3))
	for i := range full.Requirements {
		full.Requirements[i].Value = "значение"
	}

	event, to, ok := forcedStep(PhaseCollecting, full, Change{})
	if !ok || event != DecisionConfirm || to != PhaseConfirming {
		t.Fatalf("полный чеклист обязан вести на сверку, получено %q → %q (ok=%v)", event, to, ok)
	}

	full.Phase = PhaseConfirming
	event, to, ok = forcedStep(PhaseConfirming, full, Change{})
	if !ok || event != DecisionPlan || to != PhaseDone {
		t.Fatalf("после сверки обязан идти план, получено %q → %q (ok=%v)", event, to, ok)
	}

	// незаполненный чеклист принуждения не даёт: идёт обычный сбор
	half := *collecting(checklist(3))
	if _, _, ok := forcedStep(PhaseCollecting, half, Change{}); ok {
		t.Fatal("с неполным чеклистом машина ничего не навязывает")
	}
}

func TestMachineRejectsEverythingOutsideTheTable(t *testing.T) {
	// прямая проверка пункта «попытки перейти в недопустимое состояние»:
	// для каждой пары (фаза, событие) вне таблицы перехода быть не должно
	task := *collecting(checklist(3))

	allowed := map[TaskPhase]map[Decision]bool{}
	for _, step := range machine {
		if allowed[step.From] == nil {
			allowed[step.From] = map[Decision]bool{}
		}
		allowed[step.From][step.Event] = true
	}

	for _, phase := range allPhases {
		for _, event := range allEvents {
			task.Phase = phase
			_, ok := nextPhase(phase, event, task, Change{})
			if ok && !allowed[phase][event] {
				t.Fatalf("разрешён переход вне таблицы: %q по %q", phase, event)
			}
			if !ok && allowed[phase][event] {
				// строка есть, но условие не выполнено -- это законно;
				// незаконно, если условия нет вовсе
				for _, step := range machine {
					if step.From == phase && step.Event == event && step.When == nil {
						t.Fatalf("безусловный переход %q по %q не сработал", phase, event)
					}
				}
			}
		}
	}
}

func TestGuardRefusesEventsForbiddenInThePhase(t *testing.T) {
	// заявка «выдаю план» на первом же ходе сбора -- недопустимый переход.
	// Страж понижает её до сбора и пишет расхождение в предупреждения
	verdict := Guard(collecting(checklist(5)), nil, nil, Routing{
		Decision:  DecisionPlan,
		TaskTitle: "стяжка пола",
	}, fixedID())

	if verdict.Decision != DecisionCollect {
		t.Fatalf("план мимо сверки должен понижаться до сбора, получено %q", verdict.Decision)
	}
	if verdict.Closing || verdict.Task.Phase != PhaseCollecting {
		t.Fatalf("задача должна остаться в сборе: %+v", verdict.Task)
	}
	if len(verdict.Overrides) == 0 {
		t.Fatal("понижение обязано попасть в предупреждения -- иначе оно невидимо")
	}
}

func TestCancelPhaseOnlyFromActivePhases(t *testing.T) {
	task := *collecting(checklist(3))

	for _, phase := range []TaskPhase{PhaseCollecting, PhaseConfirming} {
		task.Phase = phase
		to, ok := CancelPhase(task)
		if !ok || to != PhaseCancelled {
			t.Fatalf("из фазы %q задачу должно быть можно прервать, получено %q (ok=%v)", phase, to, ok)
		}
	}

	for _, phase := range []TaskPhase{PhaseNone, PhaseDone, PhaseCancelled} {
		task.Phase = phase
		if _, ok := CancelPhase(task); ok {
			t.Fatalf("прерывать нечего: фаза %q", phase)
		}
	}
}
