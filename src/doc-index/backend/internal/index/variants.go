package index

import (
	"strings"

	"doc-index/internal/book"
	"doc-index/internal/embed"
	"doc-index/internal/store"
)

// Стратегии нарезки.
const (
	Fixed     = "fixed"
	Structure = "structure"
	Semantic  = "semantic"
)

// Variant -- вариант индекса: стратегия нарезки + модель эмбеддингов.
type Variant struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Hint     string `json:"hint"`
	Strategy string `json:"strategy"`
	Model    string `json:"model"`
}

// Variants -- четыре варианта: два обязательных и два, каждый из которых
// меняет ровно одну вещь.
func Variants(mainModel, compareModel string) []Variant {
	vs := []Variant{
		{ID: "fixed", Title: "Фиксированный размер", Strategy: Fixed, Model: mainModel,
			Hint: "окно 800 токенов с перекрытием 80; текст не читает, режет где придётся"},
		{ID: "structure", Title: "По структуре", Strategy: Structure, Model: mainModel,
			Hint: "главы не пересекает, внутри главы — по абзацам, 500–1000 токенов"},
		{ID: "semantic", Title: "По смыслу", Strategy: Semantic, Model: mainModel,
			Hint: "режет там, где соседние предложения меньше всего похожи; глав не видит"},
	}
	if compareModel != "" && compareModel != mainModel {
		m := embed.Lookup(compareModel)
		vs = append(vs, Variant{ID: "structure-" + slug(compareModel), Title: "По структуре · " + m.Title,
			Strategy: Structure, Model: compareModel,
			Hint: "те же чанки, что «По структуре», но векторы считает другая модель"})
	}
	return vs
}

// Find -- вариант по id.
func Find(vs []Variant, id string) (Variant, bool) {
	for _, v := range vs {
		if v.ID == id {
			return v, true
		}
	}
	return Variant{}, false
}

func slug(model string) string {
	name, _, _ := strings.Cut(model, ":")
	name, _, _ = strings.Cut(name, "-")
	return name
}

// SectionLabel -- как подписать чанк: глава с описанием, а если чанк
// захватил несколько глав -- диапазон.
func SectionLabel(b *book.Book, sections []int) string {
	if len(sections) == 0 {
		return ""
	}
	first := b.Sections[sections[0]]
	if len(sections) == 1 {
		return first.Name()
	}
	last := b.Sections[sections[len(sections)-1]]
	return first.Label + " → " + last.Label
}

// embedFormat -- версия того, как собирается текст для эмбеддинга. Меняется --
// меняются отпечатки, и индекс пересчитывается.
const embedFormat = "v1"

// EmbedText -- что уходит в модель: префикс модели, заголовок и текст чанка.
//
// У structure заголовок -- книга и глава: это часть того, чем стратегия
// отличается. У fixed и semantic глав нет, заголовок -- только книга.
func EmbedText(v Variant, b *book.Book, c store.Chunk) string {
	head := b.Title
	if v.Strategy == Structure && len(c.Sections) > 0 {
		head += " — " + b.Sections[c.Sections[0]].Name()
	}
	return embed.Lookup(v.Model).DocPrefix + head + "\n\n" + c.Text
}
