package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/search"
)

// Control -- контрольный вопрос: что должно быть в ответе и где это в книгах.
type Control struct {
	ID       string   `json:"id"`
	Q        string   `json:"q"`
	Expected string   `json:"expected"`           // суть правильного ответа
	Book     string   `json:"book,omitempty"`     // пусто -- ответа в корпусе нет
	Chapters []string `json:"chapters,omitempty"` // главы с ответом
	Evidence []string `json:"evidence,omitempty"` // слова, которые обязаны быть в этих главах

	Valid   bool   `json:"valid"`
	Problem string `json:"problem,omitempty"`
}

// InCorpus -- ответ есть в книгах.
func (c Control) InCorpus() bool { return c.Book != "" }

// LoadControls читает контрольные вопросы.
func LoadControls(path string) ([]Control, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cs []Control
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cs, nil
}

// ValidateControls проверяет источники по тексту книг: слова evidence должны
// найтись в указанных главах.
func ValidateControls(cs []Control, books map[string]*book.Book) []Control {
	out := make([]Control, len(cs))
	for i, c := range cs {
		c.Valid, c.Problem = true, ""
		switch {
		case c.Q == "" || c.Expected == "":
			c.Valid, c.Problem = false, "нужны q и expected"
		case !c.InCorpus():
		case books[c.Book] == nil:
			c.Valid, c.Problem = false, fmt.Sprintf("книги %q нет в индексе", c.Book)
		default:
			b := books[c.Book]
			var text strings.Builder
			for _, key := range c.Chapters {
				sec, ok := b.FindSection(key)
				if !ok {
					c.Valid, c.Problem = false, fmt.Sprintf("в книге нет главы %q", key)
					break
				}
				text.WriteString(b.Text[sec.Start:sec.End])
			}
			hay := fold(text.String())
			for _, e := range c.Evidence {
				if c.Valid && !strings.Contains(hay, fold(e)) {
					c.Valid, c.Problem = false, fmt.Sprintf("источник не подтверждён текстом: «%s» нет в главе %s",
						e, strings.Join(c.Chapters, ", "))
				}
			}
		}
		out[i] = c
	}
	return out
}

func fold(s string) string {
	return strings.NewReplacer("’", "'", "‘", "'", "“", "\"", "”", "\"").Replace(strings.ToLower(s))
}

// SourcesHit -- попала ли в найденные отрывки хоть одна глава с ответом.
func SourcesHit(c Control, hits []search.Hit) bool {
	for _, h := range hits {
		if h.Book != c.Book {
			continue
		}
		for _, s := range h.Sections {
			for _, want := range c.Chapters {
				if book.SameKey(s.Key, want) {
					return true
				}
			}
		}
	}
	return false
}

// Verdict -- оценка судьи.
type Verdict struct {
	Verdict string `json:"verdict"` // correct, partial, wrong
	Reason  string `json:"reason"`
}

const judgeSystem = `Ты проверяешь ответы на вопрос о книгах Марка Твена. Тебе дано ожидание — что должно быть в правильном ответе.
Оцени ответы A и B независимо друг от друга:
- correct — ответ передаёт суть ожидания и не противоречит ему;
- partial — есть часть ожидаемого, или ответ верен по сути, но содержит заметную ошибку;
- wrong — ожидаемого нет, ответ ему противоречит или по существу не отвечает.
Если ожидание — честно признать, что в книгах ответа нет, то correct — это прямой отказ без выдумок, а уверенный выдуманный ответ — wrong.
Ссылки вида [1] и длина ответа на оценку не влияют.
Верни только JSON: {"a":{"verdict":"...","reason":"одна короткая фраза"},"b":{"verdict":"...","reason":"..."}}`

// Judge оценивает оба ответа на вопрос одним вызовом модели.
func Judge(ctx context.Context, m LLM, c Control, a, b string) (Verdict, Verdict, error) {
	user := fmt.Sprintf("Вопрос: %s\n\nОжидание: %s\n\nОтвет A:\n%s\n\nОтвет B:\n%s", c.Q, c.Expected, orDash(a), orDash(b))
	text, err := m.ask(ctx, judgeSystem, user)
	if err != nil {
		return Verdict{}, Verdict{}, err
	}
	var got struct {
		A Verdict `json:"a"`
		B Verdict `json:"b"`
	}
	if err := decodeJSON(text, '{', &got); err != nil || (got.A.Verdict == "" && got.B.Verdict == "") {
		return Verdict{}, Verdict{}, fmt.Errorf("судья вернул не JSON: %.200s", text)
	}
	for _, v := range []*Verdict{&got.A, &got.B} {
		v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
		if v.Verdict != "correct" && v.Verdict != "partial" && v.Verdict != "wrong" {
			return Verdict{}, Verdict{}, fmt.Errorf("судья вернул неизвестный вердикт %q", v.Verdict)
		}
	}
	return got.A, got.B, nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "— (ответа нет)"
	}
	return s
}

// EvalRow -- контрольный вопрос, оба ответа и оценки.
type EvalRow struct {
	Control    Control `json:"control"`
	Result     Result  `json:"result"`
	SourcesHit *bool   `json:"sourcesHit,omitempty"` // nil -- у вопроса нет источников
	NoRAG      Verdict `json:"noRagVerdict"`
	RAG        Verdict `json:"ragVerdict"`
	JudgeError string  `json:"judgeError,omitempty"`
	At         string  `json:"at"`
}

// Tally -- сводка вердиктов режима.
type Tally struct {
	Correct int `json:"correct"`
	Partial int `json:"partial"`
	Wrong   int `json:"wrong"`
}

// EvalReport -- прогон контрольных вопросов.
type EvalReport struct {
	Model      string    `json:"model"`
	Config     string    `json:"config"`
	Rows       []EvalRow `json:"rows"`
	NoRAG      Tally     `json:"noRag"`
	RAG        Tally     `json:"rag"`
	SourcesHit int       `json:"sourcesHit"`
	WithSource int       `json:"withSource"`
	Updated    string    `json:"updated"`
}

func (r *EvalReport) tally() {
	r.NoRAG, r.RAG, r.SourcesHit, r.WithSource = Tally{}, Tally{}, 0, 0
	add := func(t *Tally, v string) {
		switch v {
		case "correct":
			t.Correct++
		case "partial":
			t.Partial++
		case "wrong":
			t.Wrong++
		}
	}
	for _, row := range r.Rows {
		add(&r.NoRAG, row.NoRAG.Verdict)
		add(&r.RAG, row.RAG.Verdict)
		if row.SourcesHit != nil {
			r.WithSource++
			if *row.SourcesHit {
				r.SourcesHit++
			}
		}
	}
}

// LoadReport читает сохранённый прогон (пустой, если его нет).
func LoadReport(path string) (EvalReport, error) {
	var rep EvalReport
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rep, nil
	}
	if err != nil {
		return rep, err
	}
	err = json.Unmarshal(raw, &rep)
	return rep, err
}

func saveReport(path string, rep EvalReport) error {
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// Evaluate прогоняет контрольные вопросы: оба ответа, попадание источников,
// оценка судьи. Каждая строка сохраняется сразу; строки, уже посчитанные
// той же моделью с той же конфигурацией поиска, пропускаются (force --
// пересчитать всё). Так прерванный прогон не тратит лимит модели заново.
func (a *Agent) Evaluate(ctx context.Context, controls []Control, path string, force bool, progress func(i int, row EvalRow, skipped bool)) (EvalReport, error) {
	rep, err := LoadReport(path)
	if err != nil {
		return rep, err
	}
	if force || rep.Model != a.LLM.Model || rep.Config != a.Config.ID {
		rep = EvalReport{Model: a.LLM.Model, Config: a.Config.ID}
	}
	done := map[string]EvalRow{}
	for _, row := range rep.Rows {
		if row.JudgeError == "" && row.Result.NoRAG.Error == "" && row.Result.RAG.Error == "" {
			done[row.Control.ID+"\x00"+row.Control.Q] = row
		}
	}
	var rows []EvalRow
	for i, c := range controls {
		if !c.Valid {
			continue
		}
		if row, ok := done[c.ID+"\x00"+c.Q]; ok {
			row.Control = c
			rows = append(rows, row)
			if progress != nil {
				progress(i, row, true)
			}
			continue
		}
		row := EvalRow{Control: c, At: time.Now().UTC().Format(time.RFC3339)}
		row.Result = a.Ask(ctx, c.Q)
		if c.InCorpus() {
			hits := make([]search.Hit, len(row.Result.RAG.Sources))
			for j, s := range row.Result.RAG.Sources {
				hits[j] = s.Hit
			}
			hit := SourcesHit(c, hits)
			row.SourcesHit = &hit
		}
		if row.Result.NoRAG.Error == "" && row.Result.RAG.Error == "" {
			row.NoRAG, row.RAG, err = Judge(ctx, a.LLM, c, row.Result.NoRAG.Text, row.Result.RAG.Text)
			if err != nil {
				row.JudgeError = err.Error()
			}
		} else {
			row.JudgeError = "не оценено: " + strings.TrimSpace(row.Result.NoRAG.Error+" "+row.Result.RAG.Error)
		}
		rows = append(rows, row)
		if progress != nil {
			progress(i, row, false)
		}
		rep.Rows = mergeRows(rep.Rows, rows)
		rep.tally()
		rep.Updated = time.Now().UTC().Format(time.RFC3339)
		if err := saveReport(path, rep); err != nil {
			return rep, err
		}
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
	}
	rep.Rows = rows
	rep.tally()
	rep.Updated = time.Now().UTC().Format(time.RFC3339)
	return rep, saveReport(path, rep)
}

// mergeRows -- новые строки поверх старых, по id вопроса.
func mergeRows(old, fresh []EvalRow) []EvalRow {
	idx := map[string]int{}
	out := append([]EvalRow(nil), old...)
	for i, r := range out {
		idx[r.Control.ID] = i
	}
	for _, r := range fresh {
		if i, ok := idx[r.Control.ID]; ok {
			out[i] = r
		} else {
			idx[r.Control.ID] = len(out)
			out = append(out, r)
		}
	}
	return out
}
