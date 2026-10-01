// Package chunk -- три стратегии нарезки книги на чанки.
//
//   - fixed      -- окно фиксированного размера с перекрытием, текст не читает;
//   - structure  -- по главам, внутри главы -- по абзацам;
//   - semantic   -- по смысловым переходам: там, где соседние предложения
//     перестают быть похожими по эмбеддингам.
//
// Чанк -- всегда непрерывный кусок очищенного текста книги [Start, End).
// Перекрытие с предыдущим чанком -- это просто более раннее начало:
// [Start, BodyStart) уже был в конце предыдущего чанка.
package chunk

import (
	"fmt"
	"math"
	"unicode"
	"unicode/utf8"

	"doc-index/internal/book"
	"doc-index/internal/tokens"
)

// Params -- размеры в токенах.
type Params struct {
	Target  int // желаемый размер чанка
	Min     int // меньше -- только если раздел или книга короче
	Max     int
	Overlap int // сколько токенов предыдущего чанка повторяется в начале следующего
}

// DefaultParams -- по рекомендации: чанки 500–1000 токенов, перекрытие 50–100.
var DefaultParams = Params{Target: 800, Min: 500, Max: 1000, Overlap: 80}

// Fingerprint -- параметры одной строкой; входят в отпечаток индекса.
func (p Params) Fingerprint() string {
	return fmt.Sprintf("t%d-min%d-max%d-ov%d", p.Target, p.Min, p.Max, p.Overlap)
}

// Chunk -- кусок текста книги.
type Chunk struct {
	Book      string
	Ordinal   int
	Start     int // байты в book.Text, с перекрытием
	BodyStart int // конец перекрытия с предыдущим чанком
	End       int
	Tokens    int   // оценка по калибровке
	Sections  []int // главы, текст которых захвачен
}

// ID -- стабильный идентификатор: вариант, книга, номер. Одинаковые параметры
// и текст дают те же id при каждой индексации.
func ID(variant, bookID string, ordinal int) string {
	return fmt.Sprintf("%s-%s-%04d", variant, bookID, ordinal)
}

func finish(b *book.Book, est tokens.Estimator, cs []Chunk) []Chunk {
	for i := range cs {
		cs[i].Book = b.ID
		cs[i].Ordinal = i
		cs[i].Tokens = est.Count(b.Text[cs[i].Start:cs[i].End])
		cs[i].Sections = b.SectionsIn(cs[i].Start, cs[i].End)
	}
	return cs
}

// Fixed -- окно Target токенов с перекрытием Overlap. Граница -- ближайший
// пробел, слово пополам не режется; всё остальное -- заголовки, конец главы,
// середина предложения -- стратегии безразлично.
func Fixed(b *book.Book, est tokens.Estimator, p Params) []Chunk {
	text := b.Text
	window, overlap := est.Runes(p.Target), est.Runes(p.Overlap)

	var cs []Chunk
	pos := skipSpace(text, 0)
	prevEnd := pos
	for pos < len(text) {
		end := advance(text, pos, window)
		if end < len(text) {
			end = backToSpace(text, pos, end)
		}
		end = trimSpaceLeft(text, pos, end)
		cs = append(cs, Chunk{Start: pos, BodyStart: max(pos, prevEnd), End: end})
		if end >= len(text) || skipSpace(text, end) >= len(text) {
			break
		}
		next := wordStart(text, retreat(text, end, overlap))
		if next <= pos {
			next = skipSpace(text, end)
		}
		prevEnd = skipSpace(text, end)
		pos = next
	}
	return finish(b, est, cs)
}

// unit -- неделимый кусок для structure: короткий абзац целиком или одно
// предложение длинного.
type unit struct {
	start, end int
	tokens     int
}

// Structure -- главы не пересекаются. Внутри главы абзацы набираются в чанки
// примерно поровну, около Target токенов; длинный абзац делится по предложениям.
// Перекрытие -- хвост предыдущего чанка, выровненный по началу предложения.
func Structure(b *book.Book, est tokens.Estimator, p Params) []Chunk {
	sents := Sentences(b)
	bodyTarget, bodyMax := p.Target-p.Overlap, p.Max-p.Overlap

	var cs []Chunk
	for _, sec := range b.Sections {
		ss := sectionSentences(sents, sec.N)
		if len(ss) == 0 {
			continue
		}
		units := structureUnits(b, est, ss, bodyTarget/2)

		total := 0
		for _, u := range units {
			total += u.tokens
		}
		n := max(1, int(math.Round(float64(total)/float64(bodyTarget))))
		target := total / n

		var groups [][]unit
		var cur []unit
		acc := 0
		for _, u := range units {
			if len(cur) > 0 && (acc+u.tokens > bodyMax || acc >= target) {
				groups = append(groups, cur)
				cur, acc = nil, 0
			}
			cur = append(cur, u)
			acc += u.tokens
		}
		groups = append(groups, cur)
		// короткий хвост главы приклеивается к предыдущему чанку, если влезает
		if k := len(groups); k >= 2 {
			last, prev := sumTokens(groups[k-1]), sumTokens(groups[k-2])
			if last < p.Min-p.Overlap && last+prev <= bodyMax {
				groups[k-2] = append(groups[k-2], groups[k-1]...)
				groups = groups[:k-1]
			}
		}

		for gi, g := range groups {
			body := g[0].start
			start := body
			if gi > 0 {
				start = overlapStart(b.Text, ss, body, sec.BodyStart, est, p)
			}
			cs = append(cs, Chunk{Start: start, BodyStart: body, End: g[len(g)-1].end})
		}
	}
	return finish(b, est, cs)
}

// Windows -- тексты для эмбеддингов предложений в semantic: предложение
// с соседями слева и справа. Одиночное предложение («No answer.») слишком
// короткое, чтобы по нему судить о теме; окно сглаживает шум.
// Тексты склеиваются через пробел, а не вырезаются из книги: так в окно
// не попадает заголовок главы, и стратегия правда не видит структуру.
func Windows(b *book.Book, sents []Sentence) []string {
	out := make([]string, len(sents))
	for i := range sents {
		lo, hi := max(i-1, 0), min(i+1, len(sents)-1)
		s := ""
		for j := lo; j <= hi; j++ {
			if s != "" {
				s += " "
			}
			s += b.Text[sents[j].Start:sents[j].End]
		}
		out[i] = s
	}
	return out
}

// Semantic -- разрез там, где смысл меняется сильнее всего. vectors --
// нормализованные эмбеддинги Windows(b, sents) в том же порядке.
//
// Расстояние между соседними окнами -- 1 − cos. Среди всех допустимых мест
// разреза (тело чанка от Min−Overlap до Max−Overlap токенов) выбирается то,
// где расстояние максимально. Главы стратегия не видит: чанк может начаться
// в одной главе и закончиться в другой -- насколько её разрезы совпадают
// с настоящими главами, показывает сравнение.
func Semantic(b *book.Book, est tokens.Estimator, p Params, sents []Sentence, vectors [][]float32) []Chunk {
	n := len(sents)
	if n == 0 {
		return nil
	}
	tok := make([]int, n)
	for i, s := range sents {
		tok[i] = est.Count(b.Text[s.Start:s.End])
	}
	dist := Distances(vectors)
	bodyMin, bodyMax := p.Min-p.Overlap, p.Max-p.Overlap

	var cs []Chunk
	for start := 0; start < n; {
		rest := 0
		for _, t := range tok[start:] {
			rest += t
		}
		// у первого чанка перекрытия нет -- тело может быть длиннее
		lo, hi := bodyMin, bodyMax
		if start == 0 {
			lo, hi = p.Min, p.Max
		}
		cut := n - 1
		if rest > hi {
			best, bestD, acc := -1, -1.0, 0
			for j := start; j < n-1; j++ {
				acc += tok[j]
				if acc > hi {
					break
				}
				if acc >= lo && rest-acc >= bodyMin && dist[j] > bestD {
					best, bestD = j, dist[j]
				}
			}
			if best < 0 { // одно огромное предложение -- режем после него
				best = start
			}
			cut = best
		}
		body := sents[start].Start
		begin := body
		if start > 0 {
			begin = overlapStart(b.Text, sents[:start], body, 0, est, p)
		}
		cs = append(cs, Chunk{Start: begin, BodyStart: body, End: sents[cut].End})
		start = cut + 1
	}
	return finish(b, est, cs)
}

// Distances -- 1 − cos между соседними векторами: dist[i] -- между i и i+1.
func Distances(vectors [][]float32) []float64 {
	if len(vectors) < 2 {
		return nil
	}
	out := make([]float64, len(vectors)-1)
	for i := range out {
		out[i] = 1 - Dot(vectors[i], vectors[i+1])
	}
	return out
}

// Dot -- скалярное произведение; у нормализованных векторов это косинус.
func Dot(a, b []float32) float64 {
	var s float64
	for i := range min(len(a), len(b)) {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// overlapStart ищет начало перекрытия: самое позднее начало предложения
// перед body, с которого до body набирается хотя бы 5/8 Overlap токенов
// (при Overlap 80 -- 50). Если это предложение длиннее 100 токенов,
// перекрытие режется по слову ровно на Overlap.
func overlapStart(text string, sents []Sentence, body, floor int, est tokens.Estimator, p Params) int {
	lo, hi := p.Overlap*5/8, p.Overlap*5/4
	for i := len(sents) - 1; i >= 0; i-- {
		st := sents[i].Start
		if st >= body {
			continue
		}
		if st < floor {
			break
		}
		t := est.Count(text[st:body])
		if t < lo {
			continue
		}
		if t <= hi {
			return st
		}
		break
	}
	start := wordStart(text, retreat(text, body, est.Runes(p.Overlap)))
	return max(start, floor)
}

func structureUnits(b *book.Book, est tokens.Estimator, ss []Sentence, long int) []unit {
	var units []unit
	for i := 0; i < len(ss); {
		j := i
		for j+1 < len(ss) && ss[j+1].Para == ss[i].Para {
			j++
		}
		para := unit{start: ss[i].Start, end: ss[j].End}
		para.tokens = est.Count(b.Text[para.start:para.end])
		if para.tokens <= long || i == j {
			units = append(units, para)
		} else {
			for k := i; k <= j; k++ {
				units = append(units, unit{start: ss[k].Start, end: ss[k].End,
					tokens: est.Count(b.Text[ss[k].Start:ss[k].End])})
			}
		}
		i = j + 1
	}
	return units
}

func sectionSentences(sents []Sentence, section int) []Sentence {
	var out []Sentence
	for _, s := range sents {
		if s.Section == section {
			out = append(out, s)
		}
	}
	return out
}

func sumTokens(us []unit) int {
	n := 0
	for _, u := range us {
		n += u.tokens
	}
	return n
}

// advance -- смещение на n символов вперёд от pos.
func advance(s string, pos, n int) int {
	for n > 0 && pos < len(s) {
		_, size := utf8.DecodeRuneInString(s[pos:])
		pos += size
		n--
	}
	return pos
}

// retreat -- смещение на n символов назад от pos.
func retreat(s string, pos, n int) int {
	for n > 0 && pos > 0 {
		_, size := utf8.DecodeLastRuneInString(s[:pos])
		pos -= size
		n--
	}
	return pos
}

// backToSpace отступает от end к последнему пробелу, но не дальше середины окна.
func backToSpace(s string, pos, end int) int {
	for i := end; i > pos+(end-pos)/2; i-- {
		if i < len(s) && isSpace(s[i]) {
			return i
		}
	}
	return end
}

// wordStart -- начало слова, в котором (или перед которым) стоит pos.
func wordStart(s string, pos int) int {
	for pos < len(s) && !isSpace(s[pos]) && pos > 0 && !isSpace(s[pos-1]) {
		_, size := utf8.DecodeRuneInString(s[pos:])
		pos += size
	}
	return skipSpace(s, pos)
}

func skipSpace(s string, pos int) int {
	for pos < len(s) && isSpace(s[pos]) {
		pos++
	}
	return pos
}

func trimSpaceLeft(s string, floor, end int) int {
	for end > floor && isSpace(s[end-1]) {
		end--
	}
	return end
}

func isSpace(c byte) bool { return c < utf8.RuneSelf && unicode.IsSpace(rune(c)) }
