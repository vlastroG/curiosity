package rag

import (
	"context"
	"fmt"
	"strings"
	"time"

	"doc-index/internal/rerank"
	"doc-index/internal/retrieve"
	"doc-index/internal/search"
)

// Settings -- настройки поиска; хранятся у чата.
type Settings struct {
	Query string `json:"query"` // raw | en | hyde -- что превращать в вектор
	rerank.Params
}

// Validate проверяет диапазоны.
func (s Settings) Validate() error {
	switch s.Query {
	case retrieve.QueryRaw, retrieve.QueryEnglish, retrieve.QueryHyDE:
	default:
		return fmt.Errorf("запрос для поиска: raw, en или hyde, а не %q", s.Query)
	}
	return s.Params.Validate()
}

// Agent ведёт беседу по найденным отрывкам: на каждую реплику -- запрос
// и память задачи от планировщика, векторный поиск, фильтр и реранкер, ответ
// модели с учётом диалога.
type Agent struct {
	LLM       LLM
	Retriever *retrieve.Retriever
	Variant   string // вариант индекса
	Reranker  rerank.Scorer
	Defaults  Settings
}

// Answer -- текст ответа.
type Answer struct {
	Text  string  `json:"text"`
	Ms    float64 `json:"ms"`
	Error string  `json:"error,omitempty"`
}

// Source -- отрывок, который получила модель.
type Source struct {
	N int `json:"n"`
	search.Hit
	Rel  *float64 `json:"rel,omitempty"` // оценка реранкера
	Text string   `json:"-"`             // полный текст чанка
}

// Result -- ответ и всё, что к нему привело.
type Result struct {
	Model    string   `json:"model"`
	Settings Settings `json:"settings"`
	Answer
	Rewrite     retrieve.Rewrite `json:"rewrite"`
	PlanMs      float64          `json:"planMs"`
	PlanError   string           `json:"planError,omitempty"` // планировщик не справился: поиск по реплике как есть
	SearchQuery string           `json:"searchQuery"`         // что превращалось в вектор
	RerankQuery string           `json:"rerankQuery"`         // что видел реранкер
	SearchMs    float64          `json:"searchMs"`
	Funnel      rerank.Funnel    `json:"funnel"`
	Sources     []Source         `json:"sources"`
	State       TaskState        `json:"-"` // память задачи после реплики
}

// Reply отвечает на реплику в беседе. Ошибка поиска или ответа попадает
// в Error; ошибка планировщика -- в PlanError, и поиск идёт по реплике как
// есть, а память задачи не меняется.
func (a *Agent) Reply(ctx context.Context, c Conversation, text string, st Settings) Result {
	text = strings.TrimSpace(text)
	started := time.Now()
	out := Result{Model: a.LLM.Model, Settings: st, Sources: []Source{}, State: c.State}

	plan, err := Planner{LLM: a.LLM}.Plan(ctx, c, text)
	out.PlanMs = ms(started)
	query := st.Query
	if err != nil {
		out.PlanError = err.Error()
		query = retrieve.QueryRaw
	} else {
		out.Rewrite, out.State = plan.Rewrite, plan.State
	}

	searchStarted := time.Now()
	cfg := retrieve.Config{Variant: a.Variant, Query: query}
	found, err := a.Retriever.Retrieve(ctx, cfg, text, out.Rewrite, "", st.KBefore)
	out.SearchMs = ms(searchStarted)
	if err != nil {
		out.Error = "поиск не удался: " + err.Error()
		out.Ms = ms(started)
		return out
	}
	if len(found.Queries) > 0 {
		out.SearchQuery = found.Queries[0]
	}

	// реранкер судит соответствие вопросу, а не выдуманному HyDE-абзацу:
	// ему -- английский запрос, если он есть
	out.RerankQuery = text
	if out.Rewrite.EN != "" {
		out.RerankQuery = out.Rewrite.EN
	}
	texts := a.texts(found.Variant)
	cands := make([]rerank.Candidate, len(found.Hits))
	for i, h := range found.Hits {
		cands[i] = rerank.Candidate{Hit: h, Text: texts[h.ChunkID]}
	}
	out.Funnel, err = rerank.Run(ctx, a.Reranker, out.RerankQuery, cands, st.Params)
	if err != nil {
		out.Error = "реранкер: " + err.Error()
		out.Ms = ms(started)
		return out
	}
	for _, c := range out.Funnel.Final() {
		out.Sources = append(out.Sources, Source{N: c.Final, Hit: c.Hit, Rel: c.Rel, Text: c.Text})
	}

	reply, err := a.LLM.ask(ctx, chatSystem, ChatPrompt(c, out.Sources, text))
	out.Answer = finish(reply, err, started)
	return out
}

func (a *Agent) texts(variant string) map[string]string {
	texts := map[string]string{}
	for _, c := range a.Retriever.Searcher.Chunks(variant) {
		texts[c.ChunkID] = c.Text
	}
	return texts
}

// Prompt собирает сообщение модели: отрывки с номерами, книгой и главой,
// затем реплика. Текст отрывка не может закрыть свой тег. Отрывков может не
// быть -- тогда блок пустой, и модель должна честно сказать, что ответа нет.
func Prompt(question string, sources []Source) string {
	var b strings.Builder
	b.WriteString("<sources>\n")
	for _, src := range sources {
		text := src.Text
		if text == "" {
			text = src.Snippet
		}
		text = strings.NewReplacer("</source", "</ source", "<source", "< source", "</sources", "</ sources").Replace(text)
		fmt.Fprintf(&b, "<source id=\"%d\" book=\"%s\" chapter=\"%s\">\n%s\n</source>\n",
			src.N, attr(src.BookTitle), attr(src.Section), strings.TrimSpace(text))
	}
	b.WriteString("</sources>\n\nРеплика пользователя: ")
	b.WriteString(question)
	return b.String()
}

func attr(s string) string {
	return strings.NewReplacer(`"`, "'", "<", "‹", ">", "›", "\n", " ").Replace(s)
}

func finish(text string, err error, started time.Time) Answer {
	a := Answer{Text: text, Ms: ms(started)}
	switch {
	case err != nil:
		a.Error = err.Error()
	case strings.TrimSpace(text) == "":
		a.Error = ErrEmptyAnswer.Error()
	}
	return a
}

func ms(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }
