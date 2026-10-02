package retrieve

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"doc-index/internal/search"
)

// BM25 -- лексический поиск по словам: классическая формула Okapi BM25.
// Нужен как второй голос рядом с векторами: точные имена и редкие слова
// («Muff Potter», «whitewash») он находит лучше, чем эмбеддинги.
type BM25 struct {
	docs  []map[string]int
	lens  []int
	avg   float64
	df    map[string]int
	k1, b float64
}

// NewBM25 строит индекс по текстам; номер текста -- номер чанка варианта.
func NewBM25(texts []string) *BM25 {
	x := &BM25{df: map[string]int{}, k1: 1.2, b: 0.75}
	total := 0
	for _, t := range texts {
		tf := map[string]int{}
		n := 0
		for _, w := range Words(t) {
			tf[w]++
			n++
		}
		for w := range tf {
			x.df[w]++
		}
		x.docs = append(x.docs, tf)
		x.lens = append(x.lens, n)
		total += n
	}
	if len(texts) > 0 {
		x.avg = float64(total) / float64(len(texts))
	}
	return x
}

// Rank -- документы с ненулевой оценкой по убыванию.
func (x *BM25) Rank(query string) []search.Scored {
	terms := map[string]bool{}
	for _, w := range Words(query) {
		terms[w] = true
	}
	n := float64(len(x.docs))
	var out []search.Scored
	for i, tf := range x.docs {
		var score float64
		for w := range terms {
			f := float64(tf[w])
			if f == 0 {
				continue
			}
			df := float64(x.df[w])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += idf * f * (x.k1 + 1) / (f + x.k1*(1-x.b+x.b*float64(x.lens[i])/x.avg))
		}
		if score > 0 {
			out = append(out, search.Scored{I: i, Score: score})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	return out
}

// Words -- слова текста в нижнем регистре без служебных. Апострофы внутри
// слова («Tom’s») отрезают окончание: tom’s → tom.
func Words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '’'
	}) {
		if i := strings.IndexAny(w, "'’"); i >= 0 {
			w = w[:i]
		}
		if len([]rune(w)) < 2 || stopWords[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

var stopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an and are as at be but by did do does for from had has have he her him his how
		i if in into is it its me my no not of on or our she so than that the their them then there these they
		this to up us was we were what when where which who whom why will with would you your about after all also
		any been before being can could each few more most other out over same should some such through too under
		until very while`) {
		m[w] = true
	}
	return m
}()

// RRF -- слияние ранжированных списков (Reciprocal Rank Fusion): у каждого
// документа сумма 1/(k + место) по всем спискам. Шкалы оценок разных
// списков (косинус, BM25) не сравниваются -- сравниваются только места.
func RRF(lists [][]search.Scored, k int) []search.Scored {
	score := map[int]float64{}
	var order []int
	for _, list := range lists {
		for rank, x := range list {
			if _, seen := score[x.I]; !seen {
				order = append(order, x.I)
			}
			score[x.I] += 1 / float64(k+rank+1)
		}
	}
	out := make([]search.Scored, len(order))
	for i, d := range order {
		out[i] = search.Scored{I: d, Score: score[d]}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	return out
}
