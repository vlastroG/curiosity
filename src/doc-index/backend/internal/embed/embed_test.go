package embed_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"doc-index/internal/embed"
	"doc-index/internal/testkit"
)

func TestEmbedNormalizes(t *testing.T) {
	o := testkit.NewOllama("bge-m3")
	defer o.Close()
	c := embed.New(o.URL, 5*time.Second)
	res, err := c.Embed(context.Background(), "bge-m3", []string{"the fence and the whitewash", "a dark cave"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Vectors) != 2 || len(res.Vectors[0]) != testkit.Dims || res.PromptTokens == 0 {
		t.Fatalf("ответ: %d векторов, %d токенов", len(res.Vectors), res.PromptTokens)
	}
	for _, v := range res.Vectors {
		var sum float64
		for _, x := range v {
			sum += float64(x * x)
		}
		if math.Abs(math.Sqrt(sum)-1) > 1e-5 {
			t.Errorf("длина вектора %.6f, ожидалась 1", math.Sqrt(sum))
		}
	}
}

func TestNormalizeRejectsZero(t *testing.T) {
	if err := embed.Normalize([]float32{0, 0}); err == nil {
		t.Fatal("нулевой вектор принят")
	}
	v := []float32{3, 4}
	if err := embed.Normalize(v); err != nil || v[0] != 0.6 || v[1] != 0.8 {
		t.Fatalf("нормализация: %v %v", v, err)
	}
}

func TestHasAndPull(t *testing.T) {
	o := testkit.NewOllama()
	defer o.Close()
	c := embed.New(o.URL, 5*time.Second)
	ctx := context.Background()
	if has, err := c.Has(ctx, "bge-m3"); err != nil || has {
		t.Fatalf("модели ещё нет: has=%v err=%v", has, err)
	}
	var statuses []string
	var lastDone, lastTotal int64
	if err := c.Pull(ctx, "bge-m3", func(s string, done, total int64) {
		statuses = append(statuses, s)
		lastDone, lastTotal = done, total
	}); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 4 || statuses[3] != "success" || lastTotal != 0 {
		t.Errorf("ход загрузки: %v (%d/%d)", statuses, lastDone, lastTotal)
	}
	if has, _ := c.Has(ctx, "bge-m3"); !has {
		t.Error("после загрузки модели нет")
	}
}

func TestRequireGPU(t *testing.T) {
	o := testkit.NewOllama("bge-m3")
	defer o.Close()
	c := embed.New(o.URL, 5*time.Second)
	ctx := context.Background()

	if _, err := c.RequireGPU(ctx, "bge-m3"); err == nil || !strings.Contains(err.Error(), "не загружена") {
		t.Fatalf("до первого запроса модель не загружена: %v", err)
	}
	if _, err := c.Embed(ctx, "bge-m3", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if p, err := c.RequireGPU(ctx, "bge-m3"); err != nil || !p.OnGPU() {
		t.Fatalf("на GPU: %+v %v", p, err)
	}
	o.SetVRAM(0)
	if _, err := c.RequireGPU(ctx, "bge-m3"); err == nil || !strings.Contains(err.Error(), "на процессоре") {
		t.Fatalf("CPU должен быть ошибкой: %v", err)
	}
	o.SetVRAM(1 << 29)
	if _, err := c.RequireGPU(ctx, "bge-m3"); err == nil || !strings.Contains(err.Error(), "50%") {
		t.Fatalf("частично на CPU -- тоже ошибка: %v", err)
	}
}

func TestErrors(t *testing.T) {
	o := testkit.NewOllama("bge-m3")
	c := embed.New(o.URL, 5*time.Second)
	if _, err := c.Embed(context.Background(), "unknown-model", []string{"x"}); err == nil {
		t.Error("неизвестная модель")
	}
	o.Close()
	if _, err := c.Version(context.Background()); err == nil || !strings.Contains(err.Error(), "Ollama недоступна") {
		t.Errorf("выключенная Ollama: %v", err)
	}
}

func TestModelPrefixes(t *testing.T) {
	n := embed.Lookup("nomic-embed-text")
	if n.DocPrefix != "search_document: " || n.QueryPrefix != "search_query: " || n.Multilingual {
		t.Errorf("nomic: %+v", n)
	}
	if b := embed.Lookup("bge-m3"); b.DocPrefix != "" || b.QueryPrefix != "" || !b.Multilingual {
		t.Errorf("bge-m3: %+v", b)
	}
	if u := embed.Lookup("something"); u.Name != "something" || u.DocPrefix != "" {
		t.Errorf("неизвестная модель: %+v", u)
	}
}
