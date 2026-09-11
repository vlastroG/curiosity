package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-sprout/internal/llm"
)

// Sticky facts -- key-value память чата.
//
// Вторая стратегия управления контекстом. В отличие от саммари, которое живёт
// от одного закрытия окна до другого и каждый раз переписывается заново, факты
// копятся через весь чат: окно может закрыться десять раз, а запись «дедлайн: пятница»
// как лежала, так и лежит.
//
// Цена у этого прямая: факты обновляются после каждой пары вопрос-ответ, то есть
// стоят дополнительного вызова модели на каждом ходе, а не раз в N сообщений.

// Fact -- одна запись key-value памяти.
type Fact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// FactsUpdate -- результат обновления памяти после хода.
type FactsUpdate struct {
	Facts []Fact `json:"facts"`
	// Added и Changed нужны интерфейсу: по ним видно, что ход вообще дал памяти
	Added     int       `json:"added"`
	Changed   int       `json:"changed"`
	Usage     llm.Usage `json:"usage"`
	Cost      Cost      `json:"cost"`
	LatencyMs int       `json:"latencyMs"`
}

// maxFacts -- потолок на размер памяти.
//
// Факты копятся через весь чат, и без потолка они однажды станут больше диалога,
// который должны были заменить. Тридцать записей -- это примерно страница текста.
const maxFacts = 30

// factsMaxTokens -- бюджет на ответ. Модель возвращает весь набор целиком,
// поэтому потолок должен вмещать все maxFacts записей плюс рассуждение.
const factsMaxTokens = 2048

// factsSystem -- промпт обновления памяти. Не const только потому, что потолок
// записей подставляется из maxFacts: дублировать число в тексте -- значит однажды
// поменять его в одном месте и забыть в другом.
//
// Ключи свободные: модель придумывает их по ходу разговора. Фиксированные рубрики
// выглядели бы аккуратнее, но всё, что в них не влезает, модель либо теряет,
// либо впихивает в чужую рубрику.
var factsSystem = "Ты ведёшь краткую память диалога в виде пар «ключ — значение». " +
	"Тебе дают текущую память и последний обмен репликами. " +
	"Верни ВЕСЬ обновлённый набор фактов целиком, а не только изменения. " +
	"Записывай то, что понадобится дальше по разговору: цели, ограничения, " +
	"предпочтения, принятые решения, договорённости, имена, названия, числа и сроки. " +
	"Ключ -- короткое существительное или словосочетание по-русски в нижнем регистре; " +
	"значение -- одна короткая фраза. " +
	"Уже записанные факты не теряй: переноси их в ответ без изменений. " +
	"Если факт уточнился или устарел -- обнови его значение, не создавая второй ключ. " +
	"Не дублируй одно и то же разными словами и не записывай сиюминутное " +
	"(вежливость, ход рассуждения, содержание самого ответа). " +
	fmt.Sprintf("Больше %d записей не возвращай: оставь самые важные. ", maxFacts) +
	"Ответ -- ровно один json-объект вида " +
	"{\"facts\": [{\"key\": \"ключ\", \"value\": \"значение\"}]}, без текста до и после."

// factsPreamble -- под каким видом память уезжает в запрос.
const factsPreamble = "Факты, накопленные в этом диалоге. Они собраны из всего разговора, " +
	"в том числе из той части, которая уже не отправляется целиком:\n\n"

// updateFacts пересобирает память по итогам хода.
//
// Вызывается после ответа, а не до: обновлять память просят по паре «вопрос-ответ»,
// а до вызова ответа ещё нет. Отсюда следствие -- в запрос хода N уезжает память,
// собранная по ходам до N-1 включительно.
func (a *Agent) updateFacts(
	ctx context.Context,
	model Model,
	provider llm.Provider,
	facts []Fact,
	question, answer string,
) (*FactsUpdate, error) {
	resp, err := a.llm.Chat(ctx, provider, llm.Request{
		Model: model.ID,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: factsSystem},
			{Role: llm.RoleUser, Content: factsPayload(facts, question, answer)},
		},
		// нулевая температура: память должна быть воспроизводимой, а не творческой
		Temperature: 0,
		MaxTokens:   factsMaxTokens,
		TopP:        1,
		JSONObject:  true,
	})
	if err != nil {
		return nil, err
	}

	updated, err := parseFacts(resp.Text)
	if err != nil {
		return nil, err
	}

	added, changed := diffFacts(facts, updated)

	return &FactsUpdate{
		Facts:     updated,
		Added:     added,
		Changed:   changed,
		Usage:     resp.Usage,
		Cost:      model.Cost(resp.Usage, a.now()),
		LatencyMs: resp.LatencyMs,
	}, nil
}

// factsPayload собирает вход обновления: текущая память и последний обмен.
func factsPayload(facts []Fact, question, answer string) string {
	var payload strings.Builder

	payload.WriteString("Текущая память:\n")
	if len(facts) == 0 {
		payload.WriteString("(пусто, это начало диалога)\n")
	} else {
		for _, fact := range facts {
			payload.WriteString("- ")
			payload.WriteString(fact.Key)
			payload.WriteString(": ")
			payload.WriteString(fact.Value)
			payload.WriteString("\n")
		}
	}

	payload.WriteString("\nПоследний обмен:\nПользователь: ")
	payload.WriteString(question)
	payload.WriteString("\nАссистент: ")
	payload.WriteString(answer)

	return payload.String()
}

// parseFacts разбирает ответ модели.
//
// Приём тот же, что у судьи в parseVerdict: сначала пробуем разобрать целиком,
// потом вырезаем первый объект по фигурным скобкам -- слабые модели любят обернуть
// json в пояснения или в markdown-ограждение.
func parseFacts(text string) ([]Fact, error) {
	candidate := strings.TrimSpace(text)

	var parsed struct {
		Facts []Fact `json:"facts"`
	}

	if err := json.Unmarshal([]byte(candidate), &parsed); err != nil {
		start := strings.Index(candidate, "{")
		end := strings.LastIndex(candidate, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("факты не разобрались как json: %s", trimForError(candidate))
		}
		if err := json.Unmarshal([]byte(candidate[start:end+1]), &parsed); err != nil {
			return nil, fmt.Errorf("факты не разобрались как json: %s", trimForError(candidate))
		}
	}

	facts := make([]Fact, 0, len(parsed.Facts))
	seen := make(map[string]bool, len(parsed.Facts))

	for _, fact := range parsed.Facts {
		key := strings.TrimSpace(fact.Key)
		value := strings.TrimSpace(fact.Value)
		if key == "" || value == "" {
			continue
		}
		// модель иногда возвращает один ключ дважды; берём первое вхождение
		if seen[strings.ToLower(key)] {
			continue
		}
		seen[strings.ToLower(key)] = true

		facts = append(facts, Fact{Key: key, Value: value})
		if len(facts) == maxFacts {
			break
		}
	}

	if len(facts) == 0 {
		return nil, fmt.Errorf("модель вернула пустой набор фактов")
	}

	return facts, nil
}

// diffFacts считает, что ход дал памяти: новые ключи и изменившиеся значения.
func diffFacts(before, after []Fact) (added, changed int) {
	previous := make(map[string]string, len(before))
	for _, fact := range before {
		previous[strings.ToLower(fact.Key)] = fact.Value
	}

	for _, fact := range after {
		value, ok := previous[strings.ToLower(fact.Key)]
		switch {
		case !ok:
			added++
		case value != fact.Value:
			changed++
		}
	}

	return added, changed
}
