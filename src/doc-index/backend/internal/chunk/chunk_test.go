package chunk_test

import (
	"reflect"
	"strings"
	"testing"

	"doc-index/internal/book"
	"doc-index/internal/chunk"
	"doc-index/internal/testkit"
	"doc-index/internal/tokens"
)

var est = tokens.Estimator{CharsPerToken: 4}

func novel(t *testing.T) *book.Book {
	t.Helper()
	b, err := book.Parse(74, testkit.GutenbergFile("A Test Novel", testkit.Novel()))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSentences(t *testing.T) {
	raw := "*** START OF THE PROJECT GUTENBERG EBOOK X ***\n\nPREFACE\n\n" +
		"“Tom!” No answer. Mr. Jones came in, says I. Then PER G. G., CHIEF spoke—well? “Yes,” said Huck. It ended...\n\n" +
		"Second paragraph without a stop\n\n*** END OF THE PROJECT GUTENBERG EBOOK X ***"
	b, err := book.Parse(1, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range chunk.Sentences(b) {
		got = append(got, b.Text[s.Start:s.End])
	}
	want := []string{
		"“Tom!”", "No answer.", "Mr. Jones came in, says I.", "Then PER G. G., CHIEF spoke—well?",
		"“Yes,” said Huck.", "It ended...", "Second paragraph without a stop",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("предложения:\n%q\nожидалось\n%q", got, want)
	}
}

func contiguous(t *testing.T, b *book.Book, cs []chunk.Chunk) {
	t.Helper()
	for i, c := range cs {
		if c.Start > c.BodyStart || c.BodyStart >= c.End || c.End > len(b.Text) {
			t.Fatalf("чанк %d: неверные границы %d/%d/%d", i, c.Start, c.BodyStart, c.End)
		}
		if c.Ordinal != i || c.Book != b.ID {
			t.Fatalf("чанк %d: номер %d, книга %s", i, c.Ordinal, c.Book)
		}
		if i > 0 && c.BodyStart < cs[i-1].End-1 && c.Start > cs[i-1].End {
			t.Fatalf("чанк %d: разрыв с предыдущим", i)
		}
	}
}

// overlapTokens -- сколько токенов чанк повторяет из предыдущего.
func overlapTokens(b *book.Book, c chunk.Chunk) int { return est.Count(b.Text[c.Start:c.BodyStart]) }

func TestFixed(t *testing.T) {
	b := novel(t)
	p := chunk.DefaultParams
	cs := chunk.Fixed(b, est, p)
	contiguous(t, b, cs)
	if len(cs) < 10 {
		t.Fatalf("чанков %d", len(cs))
	}
	crosses := 0
	for i, c := range cs {
		if i < len(cs)-1 && (c.Tokens < p.Target-10 || c.Tokens > p.Target) {
			t.Errorf("чанк %d: %d токенов, окно %d", i, c.Tokens, p.Target)
		}
		if i > 0 {
			if ov := overlapTokens(b, c); ov < p.Overlap-5 || ov > p.Overlap+5 {
				t.Errorf("чанк %d: перекрытие %d токенов", i, ov)
			}
			// перекрытие -- ровно хвост предыдущего чанка
			if !strings.HasSuffix(b.Text[:cs[i-1].End], b.Text[c.Start:c.BodyStart]) &&
				!strings.HasPrefix(b.Text[c.Start:], strings.TrimSpace(b.Text[c.Start:cs[i-1].End])) {
				t.Errorf("чанк %d: перекрытие не совпадает с концом предыдущего", i)
			}
		}
		if len(c.Sections) > 1 {
			crosses++
		}
		if text := b.Text[c.Start:c.End]; strings.TrimSpace(text) != text {
			t.Errorf("чанк %d: пробелы по краям", i)
		}
	}
	if crosses == 0 {
		t.Error("фиксированное окно должно хоть раз захватить две главы")
	}
}

func TestStructure(t *testing.T) {
	b := novel(t)
	p := chunk.DefaultParams
	cs := chunk.Structure(b, est, p)
	contiguous(t, b, cs)
	sents := chunk.Sentences(b)
	starts := map[int]bool{}
	for _, s := range sents {
		starts[s.Start] = true
	}
	perSection := map[int]int{}
	for i, c := range cs {
		if len(c.Sections) != 1 {
			t.Fatalf("чанк %d захватил главы %v", i, c.Sections)
		}
		sec := b.Sections[c.Sections[0]]
		if c.Start < sec.BodyStart || c.End > sec.End {
			t.Fatalf("чанк %d вышел за границы главы", i)
		}
		if c.Tokens > p.Max {
			t.Errorf("чанк %d: %d токенов > %d", i, c.Tokens, p.Max)
		}
		// главы по ~3400 токенов: каждый чанк в диапазоне
		if sec.Key != "PREFACE" && sec.Key != "CONCLUSION" && c.Tokens < p.Min {
			t.Errorf("чанк %d: %d токенов < %d", i, c.Tokens, p.Min)
		}
		if !starts[c.Start] {
			t.Errorf("чанк %d начинается не с начала предложения: %.40q", i, b.Text[c.Start:])
		}
		if perSection[sec.N] > 0 {
			if ov := overlapTokens(b, c); ov < 50 || ov > 100 {
				t.Errorf("чанк %d: перекрытие %d токенов, нужно 50–100", i, ov)
			}
		} else if c.Start != c.BodyStart {
			t.Errorf("первый чанк главы %s с перекрытием из предыдущей главы", sec.Key)
		}
		perSection[sec.N]++
	}
	if perSection[1] < 3 {
		t.Errorf("глава I порезана на %d чанков", perSection[1])
	}
}

func TestSemanticCutsAtTopicChange(t *testing.T) {
	b := novel(t)
	sents := chunk.Sentences(b)
	// синтетические векторы: две «темы», смена ровно на предложении 60
	vecs := make([][]float32, len(sents))
	for i := range vecs {
		if i < 60 {
			vecs[i] = []float32{1, 0}
		} else {
			vecs[i] = []float32{0, 1}
		}
	}
	cs := chunk.Semantic(b, est, chunk.DefaultParams, sents, vecs)
	contiguous(t, b, cs)
	found := false
	for _, c := range cs {
		if c.BodyStart == sents[60].Start {
			found = true
		}
		if c.Tokens > chunk.DefaultParams.Max {
			t.Errorf("чанк %d: %d токенов", c.Ordinal, c.Tokens)
		}
	}
	if !found {
		t.Fatal("разрез не пришёлся на смену темы")
	}
}

func TestSemanticWithRealWindows(t *testing.T) {
	b := novel(t)
	sents := chunk.Sentences(b)
	windows := chunk.Windows(b, sents)
	if len(windows) != len(sents) {
		t.Fatal("окон не столько, сколько предложений")
	}
	if strings.Contains(strings.Join(windows, " "), "CHAPTER") {
		t.Error("в окна попал заголовок главы")
	}
	vecs := make([][]float32, len(windows))
	for i, w := range windows {
		v := testkit.Vector(w)
		var n float64
		for _, x := range v {
			n += float64(x * x)
		}
		for j := range v {
			v[j] /= float32(sqrt(n))
		}
		vecs[i] = v
	}
	cs := chunk.Semantic(b, est, chunk.DefaultParams, sents, vecs)
	contiguous(t, b, cs)
	for _, c := range cs[:len(cs)-1] {
		if c.Tokens < chunk.DefaultParams.Min-5 || c.Tokens > chunk.DefaultParams.Max {
			t.Errorf("чанк %d: %d токенов", c.Ordinal, c.Tokens)
		}
	}
}

func sqrt(x float64) float64 {
	z := x
	for i := 0; i < 50; i++ {
		z = (z + x/z) / 2
	}
	return z
}

func TestStableIDs(t *testing.T) {
	b := novel(t)
	a1 := chunk.Structure(b, est, chunk.DefaultParams)
	a2 := chunk.Structure(b, est, chunk.DefaultParams)
	if !reflect.DeepEqual(a1, a2) {
		t.Fatal("одинаковый вход дал разные чанки")
	}
	if id := chunk.ID("structure", "tom", 7); id != "structure-tom-0007" {
		t.Errorf("id: %s", id)
	}
}

func TestCalibrate(t *testing.T) {
	e := tokens.Calibrate([]tokens.Sample{{Runes: 400, Tokens: 102}, {Runes: 800, Tokens: 202}}, 2)
	if e.CharsPerToken != 4 {
		t.Fatalf("символов на токен %.2f", e.CharsPerToken)
	}
	if e.Count("abcdefgh") != 2 || e.Runes(10) != 40 {
		t.Error("перевод символов и токенов")
	}
	if tokens.Calibrate(nil, 2).CharsPerToken != tokens.DefaultCharsPerToken {
		t.Error("без образцов -- коэффициент по умолчанию")
	}
}
