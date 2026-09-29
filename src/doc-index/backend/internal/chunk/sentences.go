package chunk

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"doc-index/internal/book"
)

// Sentence -- предложение: диапазон в тексте книги, абзац и раздел.
type Sentence struct {
	Start, End int
	Para       int
	Section    int
}

// сокращения, после которых точка не кончает предложение
var abbreviations = map[string]bool{
	"Mr": true, "Mrs": true, "Dr": true, "St": true, "Mt": true, "Jr": true, "Sr": true,
	"Col": true, "Gen": true, "Capt": true, "Rev": true, "Prof": true, "No": true,
}

// Sentences режет абзацы книги на предложения. Заголовки глав абзацами
// не считаются и в список не попадают.
//
// Правило простое и для прозы Твена достаточное: конец предложения -- «.», «!»
// или «?», за ними, возможно, закрывающие кавычки, потом пробел и заглавная
// буква или открывающая кавычка. Конец абзаца -- всегда конец предложения.
func Sentences(b *book.Book) []Sentence {
	var out []Sentence
	for pi, p := range b.Paragraphs {
		start := p.Start
		for _, cut := range sentenceCuts(b.Text[p.Start:p.End]) {
			out = append(out, Sentence{Start: start, End: p.Start + cut, Para: pi, Section: p.Section})
			start = p.Start + cut
			for start < p.End && b.Text[start] == ' ' {
				start++
			}
		}
		if start < p.End {
			out = append(out, Sentence{Start: start, End: p.End, Para: pi, Section: p.Section})
		}
	}
	return out
}

// sentenceCuts -- смещения концов предложений внутри абзаца (кроме последнего).
func sentenceCuts(s string) []int {
	var cuts []int
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		punct := i
		i += size
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		// «?!», «...»
		for i < len(s) && strings.ContainsRune(".!?", rune(s[i])) {
			i++
		}
		end := i
		for end < len(s) {
			r, size := utf8.DecodeRuneInString(s[end:])
			if !strings.ContainsRune("”’\"')]", r) {
				break
			}
			end += size
		}
		if end >= len(s) || s[end] != ' ' {
			continue
		}
		next := end
		for next < len(s) && s[next] == ' ' {
			next++
		}
		if next >= len(s) {
			continue
		}
		nr, _ := utf8.DecodeRuneInString(s[next:])
		if !unicode.IsUpper(nr) && !strings.ContainsRune("“‘\"(", nr) {
			continue
		}
		if w := lastWord(s[:punct]); r == '.' && (abbreviations[w] || isInitial(w)) {
			continue
		}
		cuts = append(cuts, end)
		i = end
	}
	return cuts
}

// isInitial -- инициал вроде «G.» в «PER G. G., CHIEF». «I» -- не инициал:
// «…says I.» у Гека кончает предложение сплошь и рядом.
func isInitial(w string) bool {
	r, size := utf8.DecodeRuneInString(w)
	return size == len(w) && unicode.IsUpper(r) && r != 'I'
}

func lastWord(s string) string {
	i := strings.LastIndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	return s[i+1:]
}
