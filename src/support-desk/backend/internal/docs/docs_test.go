package docs

import (
	"context"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"support-desk/internal/embed"
)

// fakeEmbedder -- детерминированные векторы по словам текста, без Ollama.
type fakeEmbedder struct{ calls int }

func (f *fakeEmbedder) Embed(_ context.Context, _ string, inputs []string) (embed.Result, error) {
	f.calls++
	out := make([][]float32, len(inputs))
	for i, text := range inputs {
		v := make([]float32, 8)
		for _, w := range Words(text) {
			h := fnv.New32a()
			h.Write([]byte(w))
			v[h.Sum32()%8]++
		}
		var sum float64
		for _, x := range v {
			sum += float64(x * x)
		}
		if sum == 0 {
			v[0], sum = 1, 1
		}
		for j := range v {
			v[j] = float32(float64(v[j]) / math.Sqrt(sum))
		}
		out[i] = v
	}
	return embed.Result{Vectors: out}, nil
}

func write(t *testing.T, root, rel, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func options(root string) Options {
	return Options{
		Root:    root,
		Include: []string{".md", ".rst", ".txt", ".py", ".yaml"},
		Exclude: []string{".git", "node_modules", "site-packages", "dev", "bin", "release"},
		Model:   "bge-m3",
	}
}

func TestWalkFilters(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "# A")
	write(t, root, "x/b.py", "x = 1")
	write(t, root, ".git/c.md", "# C")
	write(t, root, "dev/d.md", "# D")
	write(t, root, "e.png", "png")

	files, err := Walk(options(root))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != "a.md,x/b.py" {
		t.Errorf("отобраны %v", paths)
	}

	if _, err := Walk(options(filepath.Join(root, "нет"))); err == nil {
		t.Error("несуществующий каталог должен быть ошибкой")
	}
	// в Docker каталог -- /docs; в ошибке нужен путь, который задавал пользователь
	labeled := options(t.TempDir())
	labeled.Label = `SUPPORT_DOCS_PATH=C:\repos\pyRevt`
	if _, err := Walk(labeled); err == nil || !strings.Contains(err.Error(), `SUPPORT_DOCS_PATH=C:\repos\pyRevt`) {
		t.Errorf("в ошибке нет пути пользователя: %v", err)
	}
	if _, err := Walk(options(t.TempDir())); err == nil || !strings.Contains(err.Error(), "нет подходящих файлов") {
		t.Errorf("пустой каталог: %v", err)
	}
}

func TestChunkPython(t *testing.T) {
	src := "\"\"\"Модуль про формы.\"\"\"\nimport os\n\n\ndef a():\n    return 1\n\n\nclass B:\n    pass\n"
	chunks := Chunk("lib/forms.py", src)
	if len(chunks) != 3 {
		t.Fatalf("ждали 3 чанка, получили %d: %+v", len(chunks), chunks)
	}
	if !strings.HasSuffix(chunks[1].Title, ":a") || !strings.HasSuffix(chunks[2].Title, ":B") {
		t.Errorf("заголовки %q %q", chunks[1].Title, chunks[2].Title)
	}
	if !strings.HasPrefix(chunks[0].Text, "lib/forms.py\n") || !strings.Contains(chunks[0].Text, "Модуль про формы") {
		t.Errorf("первый чанк: %q", chunks[0].Text)
	}
}

func TestChunkMarkdownLong(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Установка\n")
	for i := 0; b.Len() < 9000; i++ {
		b.WriteString("Слово номер ")
		b.WriteString(strings.Repeat("ж", i%7+1))
		b.WriteString(". ")
	}
	chunks := Chunk("docs/install.md", b.String())
	if len(chunks) < 3 {
		t.Fatalf("ждали ≥3 чанков, получили %d", len(chunks))
	}
	for i, c := range chunks {
		body := strings.TrimPrefix(c.Text, "docs/install.md\n")
		if n := len([]rune(body)); n > maxRunes {
			t.Errorf("чанк %d длиной %d", i, n)
		}
		if c.Title != "Установка" {
			t.Errorf("заголовок чанка %d: %q", i, c.Title)
		}
	}
	tail := []rune(strings.TrimPrefix(chunks[0].Text, "docs/install.md\n"))
	if !strings.Contains(chunks[1].Text, string(tail[len(tail)-50:])) {
		t.Error("соседние чанки не перекрываются")
	}
}

func TestEnsureSkipsUnchanged(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "# Вход\nКак войти в аккаунт.")
	write(t, root, "b.py", "def login():\n    pass\n")

	x, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	e := &fakeEmbedder{}
	ctx := context.Background()

	if rebuilt, err := x.Ensure(ctx, options(root), e, nil); err != nil || !rebuilt {
		t.Fatalf("первый запуск: rebuilt=%v err=%v", rebuilt, err)
	}
	calls := e.calls
	if rebuilt, err := x.Ensure(ctx, options(root), e, nil); err != nil || rebuilt || e.calls != calls {
		t.Fatalf("без изменений: rebuilt=%v err=%v вызовов %d→%d", rebuilt, err, calls, e.calls)
	}
	if files, chunks := x.Stats(); files != 2 || chunks < 2 {
		t.Errorf("статистика: %d файлов, %d чанков", files, chunks)
	}

	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(root, "a.md"), later, later)
	if rebuilt, err := x.Ensure(ctx, options(root), e, nil); err != nil || !rebuilt {
		t.Fatalf("после правки: rebuilt=%v err=%v", rebuilt, err)
	}
}

func TestSearchHybrid(t *testing.T) {
	root := t.TempDir()
	write(t, root, "telemetry.md", "# Телеметрия\nВключается переменной PYREVIT_TELEMETRY в настройках.")
	write(t, root, "other.md", "# Другое\nКнопки на ленте можно перемещать.")

	x, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	e := &fakeEmbedder{}
	if _, err := x.Ensure(context.Background(), options(root), e, nil); err != nil {
		t.Fatal(err)
	}
	hits, err := x.Search(context.Background(), e, "bge-m3", "PYREVIT_TELEMETRY", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "telemetry.md" {
		t.Errorf("первым должен быть telemetry.md: %+v", hits)
	}
}
