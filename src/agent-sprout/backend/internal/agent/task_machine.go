package agent

// Машина состояний задачи: какие фазы бывают и какие переходы между ними разрешены.
//
// Словарь: **фаза** -- это состояние задачи, **решение** (Decision) -- событие,
// которое пытается её сменить. События приходят от диспетчера, то есть от модели,
// то есть недетерминированы. Поэтому переходы описаны здесь таблицей, а не ветками
// switch: порядок case-ов легко переставить и тихо отменить правило, а строку
// в таблице надо удалить осознанно, и по ней же гоняются тесты.
//
// Главное правило домена читается прямо из таблицы: в phaseDone ведёт только событие
// plan, и только из phaseConfirming. Единственное исключение выписано отдельной
// строкой -- предохранитель от бесконечного опроса.

// TaskPhase -- состояние задачи.
type TaskPhase string

const (
	// PhaseNone -- активной задачи нет. В самой задаче не хранится: это состояние
	// чата, а не записи
	PhaseNone       TaskPhase = ""
	PhaseCollecting TaskPhase = "collecting" // собираем исходные данные
	PhaseConfirming TaskPhase = "confirming" // собранное показано, ждём реакции
	PhaseDone       TaskPhase = "done"       // план выдан, задача закрыта
	PhaseCancelled  TaskPhase = "cancelled"  // пользователь прервал
)

// Active -- задача ещё в работе, то есть принимает события от диспетчера.
func (p TaskPhase) Active() bool {
	return p == PhaseCollecting || p == PhaseConfirming
}

// Change -- что случилось с данными задачи на этом ходе.
//
// Часть переходов зависит не от состояния задачи, а от того, что человек только что
// сделал: на сверке одно и то же полное состояние означает разное в зависимости
// от того, поправил он данные или согласился с ними.
type Change struct {
	// Corrected -- на этом ходе поправлено значение уже собранного пункта
	Corrected bool
}

// transition -- разрешённый переход: из фазы по событию в фазу.
type transition struct {
	From  TaskPhase
	Event Decision
	To    TaskPhase
	// When -- условие перехода. nil означает «разрешён всегда»
	When func(Task, Change) bool
	// Forced -- переход, который машина делает сама, не спрашивая диспетчера.
	// Именно на них держится «нельзя перепрыгнуть этап»: заявка модели
	// на forced-переходах не спрашивается вовсе
	Forced bool
}

// ready -- чеклист исходных данных заполнен целиком.
func ready(task Task, _ Change) bool { return task.Ready() }

// interviewDraggedOn -- опрос идёт дольше, чем имеет смысл.
func interviewDraggedOn(task Task, _ Change) bool { return task.CollectTurns > maxCollectTurns }

// correctedInTime -- человек правит данные на сверке, и время на это ещё есть.
//
// Предохранитель тот же, что у сбора: бесконечная правка не должна запирать задачу
// на сверке навсегда.
func correctedInTime(task Task, change Change) bool {
	return change.Corrected && !interviewDraggedOn(task, change)
}

// machine -- полная таблица переходов. Других переходов у задачи нет.
var machine = []transition{
	// задачи нет: её можно только завести, а на всё постороннее ответить отказом
	{From: PhaseNone, Event: DecisionStart, To: PhaseCollecting},
	{From: PhaseNone, Event: DecisionRefuseOffTopic, To: PhaseNone},
	{From: PhaseNone, Event: DecisionAmbiguous, To: PhaseNone},

	// сбор исходных данных
	{From: PhaseCollecting, Event: DecisionCollect, To: PhaseCollecting},
	// чеклист заполнен -- сверка обязательна и не обсуждается с моделью
	{From: PhaseCollecting, Event: DecisionConfirm, To: PhaseConfirming, When: ready, Forced: true},
	// предохранитель: единственный путь в план мимо сверки, и он ограничен по ходам.
	// Бесконечный опрос -- это не дотошность, а зависание
	{From: PhaseCollecting, Event: DecisionPlan, To: PhaseDone, When: interviewDraggedOn, Forced: true},
	{From: PhaseCollecting, Event: DecisionRefuseSecond, To: PhaseCollecting},
	{From: PhaseCollecting, Event: DecisionRefuseOffTopic, To: PhaseCollecting},
	{From: PhaseCollecting, Event: DecisionAmbiguous, To: PhaseCollecting},

	// сверка: собранное показано, следующий ход решает судьбу задачи.
	//
	// Правка идёт выше плана: forcedStep берёт первое совпадение, а «нет, площадь
	// другая» обязано откладывать план, иначе сверка -- формальность, где ответ
	// один и тот же, подтверждай или нет
	{From: PhaseConfirming, Event: DecisionConfirm, To: PhaseConfirming, When: correctedInTime, Forced: true},
	{From: PhaseConfirming, Event: DecisionPlan, To: PhaseDone, When: ready, Forced: true},
	// на сверке пользователь может стереть значение -- тогда возвращаемся к сбору
	{From: PhaseConfirming, Event: DecisionCollect, To: PhaseCollecting},
	{From: PhaseConfirming, Event: DecisionRefuseSecond, To: PhaseConfirming},
	{From: PhaseConfirming, Event: DecisionRefuseOffTopic, To: PhaseConfirming},
	{From: PhaseConfirming, Event: DecisionAmbiguous, To: PhaseConfirming},

	// прерывание приходит не от модели, а от кнопки. Живёт в таблице затем,
	// чтобы все переходы задачи были в одном месте
	{From: PhaseCollecting, Event: DecisionCancel, To: PhaseCancelled},
	{From: PhaseConfirming, Event: DecisionCancel, To: PhaseCancelled},
}

// nextPhase -- куда ведёт событие из этой фазы. ok=false означает, что такого
// перехода нет: событие в этой фазе недопустимо.
func nextPhase(from TaskPhase, event Decision, task Task, change Change) (TaskPhase, bool) {
	for _, step := range machine {
		if step.From != from || step.Event != event {
			continue
		}
		if step.When != nil && !step.When(task, change) {
			continue
		}
		return step.To, true
	}
	return from, false
}

// forcedStep -- переход, который машина делает сама, что бы ни заявил диспетчер.
//
// Проверяется раньше заявки: именно здесь держится правило «сначала сверка,
// потом план». Модель на этих переходах не спрашивают.
func forcedStep(from TaskPhase, task Task, change Change) (Decision, TaskPhase, bool) {
	for _, step := range machine {
		if !step.Forced || step.From != from {
			continue
		}
		if step.When != nil && !step.When(task, change) {
			continue
		}
		return step.Event, step.To, true
	}
	return "", from, false
}

// allowedEvents -- какие события фаза принимает. Нужен тестам и трейсу.
func allowedEvents(from TaskPhase) []Decision {
	events := make([]Decision, 0, len(machine))
	seen := map[Decision]bool{}
	for _, step := range machine {
		if step.From != from || seen[step.Event] {
			continue
		}
		seen[step.Event] = true
		events = append(events, step.Event)
	}
	return events
}

// CancelPhase -- во что переходит задача при прерывании и можно ли её прервать.
//
// Отдельная функция, потому что событие приходит не от модели, а от кнопки,
// и спрашивает её хранилище. Таблица переходов при этом одна на всех.
func CancelPhase(task Task) (TaskPhase, bool) {
	return nextPhase(task.Phase, DecisionCancel, task, Change{})
}
