package experiment

import (
	"path/filepath"
	"testing"

	"doc-index/internal/compare"
	"doc-index/internal/retrieve"
)

func TestPickPrefersHit5ThenMRR(t *testing.T) {
	rows := []Row{
		{Model: "a", All: compare.Scores{Hit5: 0.8, MRR: 0.9}},
		{Model: "b", All: compare.Scores{Hit5: 0.9, MRR: 0.5}},
		{Model: "c", All: compare.Scores{Hit5: 0.9, MRR: 0.6}},
	}
	if got := pick(rows); got.Model != "c" {
		t.Fatalf("победитель %s", got.Model)
	}
	if pick(nil).Model != "" {
		t.Fatal("пустой список")
	}
}

func TestConfigIDAndSlug(t *testing.T) {
	c := retrieve.Config{Query: retrieve.QueryHyDE, Parent: VariantMain, Hybrid: true}
	if id := ConfigID("qwen3-embedding:0.6b", c); id != "qwen3-embedding:0.6b/hyde+bm25/small-to-big" {
		t.Fatalf("id %s", id)
	}
	if s := Slug("qwen3-embedding:0.6b"); s != "qwen3-embedding-0_6b" {
		t.Fatalf("slug %s", s)
	}
	vs := Variants("bge-m3", true)
	if len(vs) != 2 || vs[1].Params == nil || vs[1].Params.Max >= vs[0].ParamsOr(SmallParams).Max && vs[0].Params != nil {
		t.Fatalf("варианты %+v", vs)
	}
}

func TestReportRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x", "report.json")
	if _, ok, err := Load(path); ok || err != nil {
		t.Fatalf("нет файла: %v %v", ok, err)
	}
	rep := Report{Books: 13, Winner: Row{Model: "m"}}
	if err := Save(path, rep); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(path)
	if !ok || err != nil || got.Books != 13 || got.Winner.Model != "m" {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
}
