package store_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/store"
	"doc-index/internal/testkit"
)

func open(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "sub", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBookRoundTrip(t *testing.T) {
	s, ctx := open(t), context.Background()
	b, err := book.Parse(74, testkit.GutenbergFile("A Test Novel", testkit.Novel()))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBook(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBook(ctx, b); err != nil { // повторно -- обновление, не дубль
		t.Fatal(err)
	}
	got, err := s.Books(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("книги: %d %v", len(got), err)
	}
	g := got[0]
	if g.Text != b.Text || !reflect.DeepEqual(g.Sections, b.Sections) || !reflect.DeepEqual(g.Paragraphs, b.Paragraphs) {
		t.Fatal("книга вернулась не такой, какой сохранялась")
	}
	if g.Rune(len(g.Text)) != b.Rune(len(b.Text)) {
		t.Error("смещения в символах не восстановлены")
	}
}

func chunkRow(variant, id, fp string, ordinal int) store.Chunk {
	return store.Chunk{Variant: variant, ChunkID: id, Book: "tom", Source: "gutenberg:74", Title: "T", Author: "A",
		Section: "Chapter I", Sections: []int{1, 2}, Ordinal: ordinal, Start: 10, BodyStart: 20, End: 99, Tokens: 25,
		Fingerprint: fp, Text: "text", Vector: []float32{0.6, -0.8, 1e-7}}
}

func TestChunksAndResume(t *testing.T) {
	s, ctx := open(t), context.Background()
	if err := s.Insert(ctx, []store.Chunk{chunkRow("fixed", "a", "fp1", 0), chunkRow("fixed", "b", "fp1", 1),
		chunkRow("fixed", "c", "old", 2), chunkRow("structure", "a", "fp1", 0)}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.Chunks(ctx, "fixed", "tom", true)
	if err != nil || len(cs) != 3 {
		t.Fatalf("чанки: %d %v", len(cs), err)
	}
	want := chunkRow("fixed", "a", "fp1", 0)
	if !reflect.DeepEqual(cs[0], want) {
		t.Fatalf("чанк вернулся другим:\n%+v\n%+v", cs[0], want)
	}

	ex, _ := s.Existing(ctx, "fixed", "tom", "fp1")
	if len(ex) != 2 || !ex["a"] || !ex["b"] {
		t.Errorf("уже посчитанные: %v", ex)
	}
	if err := s.DropStale(ctx, "fixed", "tom", "fp1"); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.Chunks(ctx, "fixed", "", false); len(cs) != 2 || cs[0].Vector != nil {
		t.Errorf("после чистки: %d чанков", len(cs))
	}
	if c, err := s.Chunk(ctx, "structure", "a"); err != nil || c.ChunkID != "a" {
		t.Errorf("чанк по id: %v", err)
	}
	if _, err := s.Chunk(ctx, "structure", "zzz"); err != store.ErrNotFound {
		t.Errorf("нет такого: %v", err)
	}
}

func TestStatesMetaReset(t *testing.T) {
	s, ctx := open(t), context.Background()
	st := store.State{Variant: "fixed", Book: "tom", Fingerprint: "fp", Model: "bge-m3", Dims: 1024, Chunks: 3,
		Tokens: 100, RealTokens: 98, EmbedSeconds: 1.5, TotalSeconds: 2, BuiltAt: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)}
	if err := s.SaveState(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, _ := s.States(ctx)
	if len(got) != 1 || !reflect.DeepEqual(got[0], st) {
		t.Fatalf("состояние: %+v", got)
	}
	if v, _ := s.Meta(ctx, "nope"); v != "" {
		t.Error("пустой ключ")
	}
	_ = s.SetMeta(ctx, "k", "v")
	if v, _ := s.Meta(ctx, "k"); v != "v" {
		t.Error("ключ не записался")
	}
	if err := s.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.States(ctx); len(got) != 0 {
		t.Error("после сброса остались варианты")
	}
}

func TestVectorEncoding(t *testing.T) {
	v := []float32{0, 1, -1, 0.123456, 3.4e38}
	if got := store.DecodeVector(store.EncodeVector(v)); !reflect.DeepEqual(got, v) {
		t.Fatalf("%v != %v", got, v)
	}
	if len(store.EncodeVector(make([]float32, 1024))) != 4096 {
		t.Error("float32 -- 4 байта")
	}
}
