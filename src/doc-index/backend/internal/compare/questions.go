// Package compare -- сравнение вариантов индекса: форма чанков, их
// целостность и качество поиска по контрольным вопросам.
package compare

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"doc-index/internal/book"
)

// Question -- контрольный вопрос: где в книгах лежит ответ.
//
// Evidence -- слова, которые обязаны встречаться в ожидаемой главе. По ним
// файл проверяет сам себя: если автор вопроса ошибся главой, вопрос помечается
// «не подтверждён текстом» и в метрики не идёт.
type Question struct {
	ID       string   `json:"id"`
	Q        string   `json:"q"`
	Lang     string   `json:"lang"` // ru, en
	Book     string   `json:"book"` // tom, huck
	Chapters []string `json:"chapters"`
	Evidence []string `json:"evidence"`

	Valid   bool   `json:"valid"`
	Problem string `json:"problem,omitempty"`
}

// OffTopic -- вопрос, ответа на который в книгах нет.
func (q Question) OffTopic() bool { return q.Book == "" }

// Load читает файл вопросов.
func Load(path string) ([]Question, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs []Question
	if err := json.Unmarshal(raw, &qs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return qs, nil
}

// Validate проверяет каждый вопрос по тексту книг.
func Validate(qs []Question, books map[string]*book.Book) []Question {
	out := make([]Question, len(qs))
	for i, q := range qs {
		q.Valid, q.Problem = true, ""
		b, ok := books[q.Book]
		switch {
		case q.Q != "" && q.OffTopic():
			// вопрос вне книг: проверять нечего, хороший поиск вернёт пустой контекст
		case q.Q == "" || len(q.Chapters) == 0 || len(q.Evidence) == 0:
			q.Valid, q.Problem = false, "нужны q, chapters и evidence"
		case !ok:
			q.Valid, q.Problem = false, fmt.Sprintf("книги %q нет в индексе", q.Book)
		default:
			var text strings.Builder
			for _, key := range q.Chapters {
				sec, ok := b.FindSection(key)
				if !ok {
					q.Valid, q.Problem = false, fmt.Sprintf("в книге нет главы %q", key)
					break
				}
				text.WriteString(b.Text[sec.Start:sec.End])
			}
			if q.Valid {
				hay := fold(text.String())
				for _, e := range q.Evidence {
					if !strings.Contains(hay, fold(e)) {
						q.Valid, q.Problem = false, fmt.Sprintf("ответ не подтверждён текстом: «%s» нет в главе %s",
							e, strings.Join(q.Chapters, ", "))
						break
					}
				}
			}
		}
		out[i] = q
	}
	return out
}

// fold -- регистр и типографские апострофы не важны.
func fold(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer("’", "'", "‘", "'", "“", "\"", "”", "\"").Replace(s)
}
