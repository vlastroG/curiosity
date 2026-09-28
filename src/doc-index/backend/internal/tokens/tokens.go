// Package tokens -- оценка числа токенов по длине текста.
//
// Точный токенизатор модели живёт внутри Ollama и наружу его не видно. Зато
// Ollama честно говорит, сколько токенов было во входе (prompt_eval_count).
// Этого хватает: перед индексацией на нескольких десятках образцов считаем,
// сколько символов в среднем приходится на токен, и дальше режем текст по
// этому коэффициенту. После индексации оценка сверяется с фактом.
package tokens

import (
	"math"
	"unicode/utf8"
)

// DefaultCharsPerToken -- английская проза в токенизаторе XLM-R (bge-m3),
// пока нет калибровки.
const DefaultCharsPerToken = 4.2

// Estimator переводит символы в токены и обратно.
type Estimator struct {
	CharsPerToken float64
}

// Default -- оценка без калибровки.
func Default() Estimator { return Estimator{CharsPerToken: DefaultCharsPerToken} }

// Count -- сколько токенов в тексте.
func (e Estimator) Count(s string) int {
	return e.FromRunes(utf8.RuneCountInString(s))
}

// FromRunes -- сколько токенов в тексте из n символов.
func (e Estimator) FromRunes(n int) int {
	if n <= 0 {
		return 0
	}
	return int(math.Ceil(float64(n) / e.cpt()))
}

// Runes -- сколько символов занимают n токенов.
func (e Estimator) Runes(n int) int {
	return int(math.Round(float64(n) * e.cpt()))
}

func (e Estimator) cpt() float64 {
	if e.CharsPerToken <= 0 {
		return DefaultCharsPerToken
	}
	return e.CharsPerToken
}

// Sample -- образец для калибровки: длина в символах и сколько токенов
// насчитала модель.
type Sample struct {
	Runes  int
	Tokens int
}

// Calibrate выводит коэффициент из образцов. Служебные токены начала и конца
// (у bge-m3 это <s> и </s>) из счёта модели вычитаются.
func Calibrate(samples []Sample, special int) Estimator {
	var runes, toks int
	for _, s := range samples {
		if t := s.Tokens - special; t > 0 {
			runes += s.Runes
			toks += t
		}
	}
	if toks == 0 {
		return Default()
	}
	return Estimator{CharsPerToken: float64(runes) / float64(toks)}
}
