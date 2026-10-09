// Package rag -- ответы на вопросы о книгах Твена в двух режимах: по памяти
// модели и с найденными отрывками из книг (RAG), а также оценка ответов.
package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"doc-index/internal/llm"
	"doc-index/internal/retrieve"
)

// Chatter -- то, что умеет разговаривать с моделью (llm.Client или заглушка).
type Chatter interface {
	Chat(ctx context.Context, p llm.Provider, req llm.Request) (llm.Response, error)
}

// Gen -- параметры генерации чата. Пустые (0, null) -- значения модели.
type Gen struct {
	Temperature *float64 `json:"temperature"`
	MaxTokens   int      `json:"maxTokens"` // предел ответа; 0 -- бюджет модели
	Ctx         int      `json:"ctx"`       // контекстное окно; только у локальной модели
}

// Пределы параметров генерации.
const (
	MinMaxTokens = 16
	MinCtx       = 2048
	MaxCtx       = 262_144
)

// Validate проверяет диапазоны.
func (g Gen) Validate() error {
	if t := g.Temperature; t != nil && (*t < 0 || *t > 2) {
		return errors.New("температура: 0–2")
	}
	if g.MaxTokens != 0 && (g.MaxTokens < MinMaxTokens || g.MaxTokens > llm.MaxTokens) {
		return fmt.Errorf("предел ответа: %d–%d токенов", MinMaxTokens, llm.MaxTokens)
	}
	if g.Ctx != 0 && (g.Ctx < MinCtx || g.Ctx > MaxCtx) {
		return fmt.Errorf("контекстное окно: %d–%d токенов", MinCtx, MaxCtx)
	}
	return nil
}

// LLM -- модель и провайдер.
type LLM struct {
	Client    Chatter
	Provider  llm.Provider
	Model     string
	MaxTokens int // бюджет вывода; 0 -- llm.MaxTokens
	Gen       Gen // параметры чата
}

// With -- та же модель с параметрами чата.
func (m LLM) With(g Gen) LLM {
	m.Gen = g
	return m
}

func (m LLM) ask(ctx context.Context, system, user string) (string, error) {
	return m.chat(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: system},
		{Role: llm.RoleUser, Content: user},
	})
}

// chat -- запрос с готовыми сообщениями, с бюджетом и параметрами чата.
func (m LLM) chat(ctx context.Context, msgs []llm.Message) (string, error) {
	budget := m.MaxTokens
	if budget <= 0 {
		budget = llm.MaxTokens
	}
	if m.Gen.MaxTokens > 0 {
		budget = min(budget, m.Gen.MaxTokens)
	}
	numCtx := 0
	// у облачных провайдеров окно фиксировано, и поля num_ctx они не знают
	if m.Provider.ID == llm.ProviderLocal {
		numCtx = m.Gen.Ctx
	}
	resp, err := m.Client.Chat(ctx, m.Provider, llm.Request{
		Model:       m.Model,
		MaxTokens:   budget,
		Temperature: m.Gen.Temperature,
		NumCtx:      numCtx,
		Messages:    msgs,
	})
	if err != nil {
		return "", err
	}
	return stripThinking(resp.Text), nil
}

const rewriteSystem = `You prepare user questions about Mark Twain's books for a search engine that searches the original English texts.
For every question return an object with:
- "id": the id you were given;
- "en": a faithful, concise English translation of the question. Use the English forms of names as Twain wrote them (Том Сойер → Tom Sawyer, Гек → Huck, тётя Полли → Aunt Polly, индеец Джо → Injun Joe, Джим → Jim).
- "hyde": two or three sentences in English, written as a passage from the book that would answer the question, in the book's own style and vocabulary.
Return only a JSON array of these objects, without any commentary or code fences.`

// Rewriter переписывает вопросы для поиска и помнит результат: один и тот же
// вопрос второй раз к модели не уходит. Кэш лежит в файле и переживает
// перезапуск.
type Rewriter struct {
	LLM  LLM
	Path string // data/rewrites.json

	mu    sync.Mutex
	cache map[string]retrieve.Rewrite
}

func (r *Rewriter) key(q string) string { return r.LLM.Model + "\x00" + strings.TrimSpace(q) }

func (r *Rewriter) load() {
	if r.cache != nil {
		return
	}
	r.cache = map[string]retrieve.Rewrite{}
	raw, err := os.ReadFile(r.Path)
	if err != nil {
		return
	}
	var stored map[string]retrieve.Rewrite
	if json.Unmarshal(raw, &stored) == nil {
		r.cache = stored
	}
}

func (r *Rewriter) save() error {
	if r.Path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(r.cache, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(r.Path, raw, 0o644)
}

// Cached -- переписанный вопрос из кэша.
func (r *Rewriter) Cached(q string) (retrieve.Rewrite, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.load()
	rw, ok := r.cache[r.key(q)]
	return rw, ok
}

// Rewrite -- один вопрос.
func (r *Rewriter) Rewrite(ctx context.Context, q string) (retrieve.Rewrite, bool, error) {
	if rw, ok := r.Cached(q); ok {
		return rw, true, nil
	}
	out, err := r.Batch(ctx, []string{q})
	if err != nil {
		return retrieve.Rewrite{}, false, err
	}
	return out[q], false, nil
}

// batchSize -- вопросов в одном вызове модели: меньше вызовов -- бережнее
// к суточному лимиту бесплатной модели, но и ответ не должен быть огромным.
const batchSize = 8

// Batch переписывает все вопросы, которых нет в кэше, пачками.
func (r *Rewriter) Batch(ctx context.Context, questions []string) (map[string]retrieve.Rewrite, error) {
	out := map[string]retrieve.Rewrite{}
	var todo []string
	for _, q := range questions {
		if rw, ok := r.Cached(q); ok {
			out[q] = rw
		} else {
			todo = append(todo, q)
		}
	}
	var errs []error
	for i := 0; i < len(todo); i += batchSize {
		part := todo[i:min(i+batchSize, len(todo))]
		got, callErr := r.call(ctx, part)
		// что модель успела переписать -- сохраняем даже при ошибке
		r.mu.Lock()
		for q, rw := range got {
			r.cache[r.key(q)] = rw
			out[q] = rw
		}
		err := r.save()
		r.mu.Unlock()
		if err != nil {
			return out, err
		}
		if callErr != nil {
			// следующая пачка может пройти -- ошибки копим
			errs = append(errs, callErr)
			if ctx.Err() != nil {
				break
			}
		}
	}
	return out, errors.Join(errs...)
}

func (r *Rewriter) call(ctx context.Context, questions []string) (map[string]retrieve.Rewrite, error) {
	type item struct {
		ID string `json:"id"`
		Q  string `json:"q"`
	}
	items := make([]item, len(questions))
	for i, q := range questions {
		items[i] = item{ID: fmt.Sprintf("q%d", i+1), Q: q}
	}
	user, _ := json.Marshal(items)
	text, err := r.LLM.ask(ctx, rewriteSystem, string(user))
	if err != nil {
		return nil, err
	}
	var got []struct {
		ID   string `json:"id"`
		EN   string `json:"en"`
		HyDE string `json:"hyde"`
	}
	if err := decodeJSON(text, '[', &got); err != nil {
		// массив оборвался или испорчен -- спасаем целые объекты по одному
		got = got[:0]
		eachObject(text, func(raw []byte) {
			var one struct {
				ID   string `json:"id"`
				EN   string `json:"en"`
				HyDE string `json:"hyde"`
			}
			if json.Unmarshal(raw, &one) == nil && one.ID != "" {
				got = append(got, one)
			}
		})
		if len(got) == 0 {
			return nil, fmt.Errorf("модель вернула не JSON-массив: %.200s", text)
		}
	}
	byID := map[string]retrieve.Rewrite{}
	for _, g := range got {
		byID[g.ID] = retrieve.Rewrite{EN: strings.TrimSpace(g.EN), HyDE: strings.TrimSpace(g.HyDE)}
	}
	out := map[string]retrieve.Rewrite{}
	var missing []string
	for i, q := range questions {
		rw := byID[items[i].ID]
		if rw.EN == "" || rw.HyDE == "" {
			missing = append(missing, items[i].ID)
			continue
		}
		out[q] = rw
	}
	if len(missing) > 0 {
		return out, fmt.Errorf("модель не переписала вопросы %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// decodeJSON ищет в ответе модели JSON-значение, которое начинается с open
// ('[' или '{'), и берёт последнее удачно разобранное: модели любят
// обернуть JSON в пояснения, ```json или рассуждения с черновиками.
func decodeJSON(s string, open byte, v any) error {
	var last []byte
	for i := 0; i < len(s); i++ {
		if s[i] != open {
			continue
		}
		var raw json.RawMessage
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		if err := dec.Decode(&raw); err == nil {
			last = raw
			i += int(dec.InputOffset()) - 1 // вложенные значения не в счёт
		}
	}
	if last == nil {
		return errors.New("в ответе нет JSON")
	}
	return json.Unmarshal(last, v)
}

// eachObject -- все JSON-объекты верхнего уровня, которые удалось разобрать.
func eachObject(s string, fn func(raw []byte)) {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		var raw json.RawMessage
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		if dec.Decode(&raw) == nil {
			fn(raw)
			i += int(dec.InputOffset()) - 1
		}
	}
}

// stripThinking убирает рассуждения <think>…</think>, которые некоторые
// бесплатные модели кладут прямо в ответ.
func stripThinking(s string) string {
	for {
		i := strings.Index(s, "<think>")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "</think>")
		if j < 0 {
			s = s[:i]
			break
		}
		s = s[:i] + s[i+j+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// ErrEmptyAnswer -- модель вернула пустой ответ.
var ErrEmptyAnswer = errors.New("модель вернула пустой ответ")
