package rerank

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"doc-index/internal/search"
)

// Params -- настройки второго этапа.
type Params struct {
	KBefore int     `json:"kBefore"` // кандидатов из векторного поиска
	SimMin  float64 `json:"simMin"`  // фильтр 1: косинус не ниже
	RelMin  float64 `json:"relMin"`  // фильтр 2: оценка реранкера не ниже
	KAfter  int     `json:"kAfter"`  // сколько отрывков максимум уходит в модель
	Order   string  `json:"order"`   // порядок прошедших: rerank | cosine | fused
}

// Порядок отрывков, прошедших фильтры.
const (
	OrderRerank = "rerank" // по оценке реранкера
	OrderCosine = "cosine" // по косинусу: реранкер только отсекает
	OrderFused  = "fused"  // RRF мест по косинусу и по реранкеру
)

// Validate проверяет диапазоны.
func (p Params) Validate() error {
	switch {
	case p.KBefore < 1 || p.KBefore > 50:
		return errors.New("top-K до фильтра: от 1 до 50")
	case p.KAfter < 1 || p.KAfter > 10:
		return errors.New("top-K после фильтра: от 1 до 10")
	case p.SimMin < 0 || p.SimMin > 1:
		return errors.New("порог косинуса: от 0 до 1")
	case p.RelMin < 0 || p.RelMin > 1:
		return errors.New("порог реранкера: от 0 до 1")
	case p.Order != OrderRerank && p.Order != OrderCosine && p.Order != OrderFused:
		return errors.New("порядок: rerank, cosine или fused")
	}
	return nil
}

// Стадии, на которых кандидат выбыл (или остался).
const (
	StageKept = "kept" // ушёл в модель
	StageSim  = "sim"  // ниже порога косинуса
	StageRel  = "rel"  // реранкер счёл нерелевантным
	StageTop  = "top"  // прошёл пороги, но не влез в top-K после
)

// Candidate -- кандидат и его путь через воронку.
type Candidate struct {
	search.Hit
	Text   string   `json:"-"`
	Rel    *float64 `json:"rel,omitempty"` // оценка реранкера; nil -- не оценивался
	Stage  string   `json:"stage"`
	Reason string   `json:"reason"`
	Final  int      `json:"final,omitempty"` // номер в контексте модели, с 1
}

// Funnel -- итог второго этапа.
type Funnel struct {
	Params     Params      `json:"params"`
	Scorer     string      `json:"scorer"`
	Candidates []Candidate `json:"candidates"` // в порядке векторного поиска
	Total      int         `json:"total"`
	PassedSim  int         `json:"passedSim"`
	PassedRel  int         `json:"passedRel"`
	Kept       int         `json:"kept"`
	RerankMs   float64     `json:"rerankMs"`
}

// Final -- отрывки, ушедшие в модель, в порядке оценки реранкера.
func (f Funnel) Final() []Candidate {
	var out []Candidate
	for _, c := range f.Candidates {
		if c.Stage == StageKept {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Final < out[j].Final })
	return out
}

// Run прогоняет кандидатов (упорядоченных по косинусу) через воронку:
// порог косинуса → реранкер → порог релевантности → top-K после.
func Run(ctx context.Context, scorer Scorer, query string, cands []Candidate, p Params) (Funnel, error) {
	f := Funnel{Params: p, Scorer: scorer.Name(), Candidates: cands, Total: len(cands)}
	var toScore []int
	for i := range f.Candidates {
		c := &f.Candidates[i]
		if c.Cosine < p.SimMin {
			c.Stage, c.Reason = StageSim, fmt.Sprintf("косинус %.3f ниже порога %.2f", c.Cosine, p.SimMin)
			continue
		}
		toScore = append(toScore, i)
	}
	f.PassedSim = len(toScore)

	if len(toScore) > 0 {
		docs := make([]string, len(toScore))
		for j, i := range toScore {
			docs[j] = f.Candidates[i].Text
		}
		started := time.Now()
		scores, err := scorer.Score(ctx, query, docs)
		f.RerankMs = float64(time.Since(started).Microseconds()) / 1000
		if err != nil {
			return f, err
		}
		for j, i := range toScore {
			s := scores[j]
			f.Candidates[i].Rel = &s
		}
	}

	var passed []int
	for _, i := range toScore {
		c := &f.Candidates[i]
		if *c.Rel < p.RelMin {
			c.Stage, c.Reason = StageRel, fmt.Sprintf("реранкер: %.2f ниже порога %.2f", *c.Rel, p.RelMin)
			continue
		}
		passed = append(passed, i)
	}
	f.PassedRel = len(passed)
	rel := make([]float64, len(f.Candidates))
	for _, i := range passed {
		rel[i] = *f.Candidates[i].Rel
	}
	Sort(passed, rel, p.Order)
	for n, i := range passed {
		c := &f.Candidates[i]
		if n < p.KAfter {
			c.Stage, c.Final, c.Reason = StageKept, n+1, fmt.Sprintf("реранкер: %.2f", *c.Rel)
			f.Kept++
		} else {
			c.Stage, c.Reason = StageTop, fmt.Sprintf("реранкер: %.2f, но не вошёл в top-%d", *c.Rel, p.KAfter)
		}
	}
	return f, nil
}

// Sort упорядочивает номера кандидатов (они уже по убыванию косинуса):
// по оценке реранкера, оставляет порядок косинуса или сливает оба через RRF.
func Sort(idx []int, rel []float64, order string) {
	switch order {
	case OrderCosine:
		return
	case OrderFused:
		byRel := append([]int(nil), idx...)
		sort.SliceStable(byRel, func(a, b int) bool { return rel[byRel[a]] > rel[byRel[b]] })
		score := map[int]float64{}
		for r, i := range idx {
			score[i] += 1 / float64(60+r+1)
		}
		for r, i := range byRel {
			score[i] += 1 / float64(60+r+1)
		}
		sort.SliceStable(idx, func(a, b int) bool { return score[idx[a]] > score[idx[b]] })
	default:
		sort.SliceStable(idx, func(a, b int) bool { return rel[idx[a]] > rel[idx[b]] })
	}
}
