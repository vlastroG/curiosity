package book_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"doc-index/internal/book"
)

// TestRealCorpus разбирает настоящие файлы Gutenberg, если они есть в кэше
// (CORPUS_DIR, например data/sources). В репозитории текстов нет -- без
// переменной тест пропускается.
func TestRealCorpus(t *testing.T) {
	dir := os.Getenv("CORPUS_DIR")
	if dir == "" {
		t.Skip("CORPUS_DIR не задан")
	}
	for n := range book.Known {
		raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("pg%d.txt", n)))
		if err != nil {
			t.Logf("%d: нет файла", n)
			continue
		}
		b, err := book.Parse(n, raw)
		if err != nil {
			t.Errorf("%d: %v", n, err)
			continue
		}
		titled := 0
		for _, s := range b.Sections {
			if s.Title != "" {
				titled++
			}
		}
		first, last := b.Sections[0], b.Sections[len(b.Sections)-1]
		t.Logf("%-14s разделов %3d (с названием %3d), %5d тыс. символов; первый %q; последний %q",
			b.ID, len(b.Sections), titled, len([]rune(b.Text))/1000, first.Name(), last.Name())
		if strings.Contains(b.Text, "PROJECT GUTENBERG") {
			t.Errorf("%s: в тексте остался служебный текст Gutenberg", b.ID)
		}
		// раздел в тысячи раз больше среднего -- признак пропущенных глав
		for _, s := range b.Sections {
			if s.End-s.Start > 20*len(b.Text)/len(b.Sections)+50000 {
				t.Errorf("%s: подозрительно большой раздел %s", b.ID, s.Name())
			}
		}
	}
}
