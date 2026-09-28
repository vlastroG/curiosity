package compare

import (
	"math"
	"testing"

	"doc-index/internal/search"
)

func TestScoresByHand(t *testing.T) {
	qs := []Question{
		{Lang: "ru", Book: "tom"}, {Lang: "ru", Book: "tom"}, {Lang: "en", Book: "huck"}, {Lang: "en", Book: "huck"},
	}
	ranks := []Place{{Rank: 1, TopBook: "tom"}, {Rank: 3, TopBook: "huck"}, {Rank: 0, TopBook: "tom"}, {Rank: 5, TopBook: "huck"}}
	all := scores(qs, ranks, "")
	// MRR = (1 + 1/3 + 0 + 1/5) / 4
	want := Scores{N: 4, Hit1: 0.25, Hit3: 0.5, Hit5: 0.75, MRR: (1 + 1.0/3 + 0.2) / 4, Book1: 0.5}
	if all.N != want.N || all.Hit1 != want.Hit1 || all.Hit3 != want.Hit3 || all.Hit5 != want.Hit5 ||
		math.Abs(all.MRR-want.MRR) > 1e-12 || all.Book1 != want.Book1 {
		t.Fatalf("%+v, ожидалось %+v", all, want)
	}
	if ru := scores(qs, ranks, "ru"); ru.N != 2 || ru.Hit1 != 0.5 || ru.Book1 != 0.5 {
		t.Errorf("русские: %+v", ru)
	}
}

func TestPlaceNeedsBookAndChapter(t *testing.T) {
	q := Question{Book: "tom", Chapters: []string{"ii"}}
	hits := []search.Hit{
		{Rank: 1, Book: "huck", Sections: []search.SectionRef{{Key: "II"}}}, // та же глава, другая книга
		{Rank: 2, Book: "tom", Sections: []search.SectionRef{{Key: "I"}}},
		{Rank: 3, Book: "tom", Sections: []search.SectionRef{{Key: "I"}, {Key: "II"}}}, // захватил нужную
	}
	if p := place(q, hits); p.Rank != 3 || p.TopBook != "huck" {
		t.Fatalf("%+v", p)
	}
	if p := place(q, nil); p.Rank != 0 {
		t.Fatalf("пустая выдача: %+v", p)
	}
}

func TestValidateReportsProblems(t *testing.T) {
	qs := Validate([]Question{{ID: "x", Q: "q", Book: "nope", Chapters: []string{"I"}, Evidence: []string{"a"}}, {ID: "y"}}, nil)
	if qs[0].Valid || qs[0].Problem == "" || qs[1].Valid {
		t.Fatalf("%+v", qs)
	}
}
