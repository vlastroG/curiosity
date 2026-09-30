package experiment

import (
	"path/filepath"
	"testing"

	"doc-index/internal/compare"
	"doc-index/internal/rerank"
	"doc-index/internal/search"
)

func hit(book, key string, cos float64) search.Hit {
	return search.Hit{Book: book, Cosine: cos, Sections: []search.SectionRef{{Key: key}}}
}

// Один вопрос по книге (нужная глава II) и один вне книг.
func sample() []prepared {
	return []prepared{
		{
			q:    compare.Question{ID: "a", Book: "tom", Chapters: []string{"II"}},
			hyde: []search.Hit{hit("tom", "I", 0.6), hit("tom", "II", 0.5), hit("huck", "II", 0.45), hit("tom", "II", 0.2)},
			rel:  []float64{0.1, 0.95, 0.3, 0.9},
		},
		{
			q:    compare.Question{ID: "off"},
			hyde: []search.Hit{hit("tom", "I", 0.35), hit("huck", "V", 0.3)},
			rel:  []float64{0.02, 0.01},
		},
	}
}

func TestFullFunnel(t *testing.T) {
	d := sample()[0]
	got := full(d, rerank.Params{KBefore: 4, SimMin: 0.3, RelMin: 0.5, KAfter: 5, Order: rerank.OrderRerank})
	// косинус 0.2 отсечён, 0.1 и 0.3 -- реранкером; остался один
	if len(got) != 1 || got[0].Sections[0].Key != "II" || got[0].Cosine != 0.5 {
		t.Fatalf("%+v", got)
	}
	// без порогов -- порядок по реранкеру
	got = full(d, rerank.Params{KBefore: 4, KAfter: 2, Order: rerank.OrderRerank})
	if len(got) != 2 || got[0].Cosine != 0.5 || got[1].Cosine != 0.2 {
		t.Fatalf("порядок: %+v", got)
	}
	// реранкер только фильтрует -- порядок косинуса
	got = full(d, rerank.Params{KBefore: 4, RelMin: 0.5, KAfter: 2, Order: rerank.OrderCosine})
	if len(got) != 2 || got[0].Cosine != 0.5 || got[1].Cosine != 0.2 {
		t.Fatalf("порядок косинуса: %+v", got)
	}
}

func TestEvaluateMetrics(t *testing.T) {
	m := evaluate(sample(), func(d prepared) []search.Hit {
		return full(d, rerank.Params{KBefore: 4, SimMin: 0.3, RelMin: 0.5, KAfter: 5})
	})
	if m.Hit1 != 1 || m.Precision != 1 || m.OffTopicEmpty != 1 || m.AvgKept != 0.5 {
		t.Fatalf("%+v", m)
	}
	m = evaluate(sample(), func(d prepared) []search.Hit { return d.hyde[:2] })
	if m.Hit1 != 0 || m.Hit5 != 1 || m.Precision != 0.5 || m.OffTopicEmpty != 0 {
		t.Fatalf("без фильтра: %+v", m)
	}
}

func TestChoose(t *testing.T) {
	sweep := []Sweep{
		// всё в контексте, но и вне книг всегда есть отрывки
		{Params: rerank.Params{KBefore: 30}, Metrics: Metrics{Scores: compare.Scores{Hit5: 1, Hit1: 0.8}}},
		// теряет немного по книгам, зато вне книг пусто
		{Params: rerank.Params{KBefore: 20, RelMin: 0.05}, Metrics: Metrics{Scores: compare.Scores{Hit5: 0.95, Hit1: 0.8}, OffTopicEmpty: 1}},
		{Params: rerank.Params{KBefore: 10, RelMin: 0.05}, Metrics: Metrics{Scores: compare.Scores{Hit5: 0.95, Hit1: 0.7}, OffTopicEmpty: 1}},
	}
	// 40 вопросов по книгам, 8 вне: 0.95*40+8 = 46 > 40
	if got := choose(sweep, 40, 8); got.KBefore != 20 {
		t.Fatalf("выбрано %+v", got)
	}
	if c := Correct(sweep[1].Metrics, 40, 8); c < 0.958 || c > 0.959 {
		t.Fatalf("доля верного контекста %.4f", c)
	}
	if got := choose(nil, 1, 1); got != Defaults.Params {
		t.Fatalf("без перебора -- значения по умолчанию: %+v", got)
	}
}

func TestReportRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x", "report.json")
	if _, ok, err := Load(path); ok || err != nil {
		t.Fatalf("нет файла: %v %v", ok, err)
	}
	rep := Report{Questions: 44, Chosen: Defaults}
	if err := Save(path, rep); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(path)
	if !ok || err != nil || got.Questions != 44 || got.Chosen != Defaults {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
}
