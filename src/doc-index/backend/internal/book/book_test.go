package book_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/testkit"
)

func parseNovel(t *testing.T) *book.Book {
	t.Helper()
	b, err := book.Parse(74, testkit.GutenbergFile("A Test Novel", testkit.Novel()))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseStripsGutenbergAndFrontMatter(t *testing.T) {
	b := parseNovel(t)
	for _, junk := range []string{"Project Gutenberg", "LICENSE", "CONTENTS", "ILLUSTRATIONS", "A Picture", "By Somebody", "\r"} {
		if strings.Contains(b.Text, junk) {
			t.Errorf("в тексте остался служебный %q", junk)
		}
	}
	if !strings.HasPrefix(b.Text, "PREFACE\n\nMost of the adventures") {
		t.Errorf("текст начинается не с предисловия: %.60q", b.Text)
	}
	if !strings.HasSuffix(b.Text, "it must stop here.") {
		t.Errorf("текст кончается не заключением: %q", b.Text[len(b.Text)-60:])
	}
	if b.ID != "tom" || b.TitleRu == "" || b.Source() != "gutenberg:74" {
		t.Errorf("метаданные известной книги: %+v", b)
	}
	if b.URL() != "https://www.gutenberg.org/ebooks/74" {
		t.Errorf("ссылка на страницу книги: %s", b.URL())
	}
}

func TestParseSectionsAndContents(t *testing.T) {
	b := parseNovel(t)
	var keys []string
	for _, s := range b.Sections {
		keys = append(keys, s.Key)
	}
	if got := strings.Join(keys, ","); got != "PREFACE,I,II,III,CONCLUSION" {
		t.Fatalf("разделы: %s", got)
	}
	ch2 := b.Sections[2]
	// описание склеено из двух строк оглавления
	if ch2.Title != "The Graveyard—Midnight—A Murder" || ch2.Label != "Chapter II" {
		t.Errorf("глава II: %+v", ch2)
	}
	if ch2.Name() != "Chapter II. The Graveyard—Midnight—A Murder" {
		t.Errorf("имя главы: %s", ch2.Name())
	}
	if b.Sections[0].Title != "" || b.Sections[0].Label != "Preface" {
		t.Errorf("предисловие: %+v", b.Sections[0])
	}
	if got := b.Text[ch2.Start:ch2.BodyStart]; got != "CHAPTER II\n\n" {
		t.Errorf("заголовок главы в тексте: %q", got)
	}
	if s, ok := b.FindSection("ii"); !ok || s.N != 2 {
		t.Errorf("поиск главы по ключу: %+v %v", s, ok)
	}
}

func TestParseParagraphs(t *testing.T) {
	b := parseNovel(t)
	for _, p := range b.Paragraphs {
		text := b.Text[p.Start:p.End]
		if strings.Contains(text, "\n") {
			t.Fatalf("перенос строки внутри абзаца: %q", text)
		}
		if s := b.Sections[p.Section]; p.Start < s.BodyStart || p.End > s.End {
			t.Fatalf("абзац вне своего раздела")
		}
	}
	if len(b.Paragraphs) != 122 {
		t.Errorf("абзацев %d, ожидалось 122", len(b.Paragraphs))
	}
}

func TestItalicsAndHucksContents(t *testing.T) {
	// оглавление в стиле «Гекльберри Финна»: «CHAPTER I.» отдельной строкой
	raw := "*** START OF THE PROJECT GUTENBERG EBOOK 76 ***\n\nCONTENTS.\n\nCHAPTER I.\nCivilizing Huck.—Miss Watson.\n\n" +
		"CHAPTER THE LAST.\nOut of Bondage.\n\n\nILLUSTRATIONS.\n\n Old Mrs. Hotchkiss\n\n\nNOTICE.\n\nPersons will be shot.\n\n\n" +
		"CHAPTER I.\n\n\nYou don’t know about me _without_ you have read\na book.\n\n\nCHAPTER THE LAST\n\n\nThe first time I catched Tom private I asked him what was his idea.\n\nTHE END.\n\n" +
		"*** END OF THE PROJECT GUTENBERG EBOOK 76 ***\n"
	b, err := book.Parse(76, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sections) != 3 || b.Sections[1].Title != "Civilizing Huck.—Miss Watson." ||
		b.Sections[2].Key != "THE LAST" || b.Sections[2].Title != "Out of Bondage." {
		t.Fatalf("разделы: %+v", b.Sections)
	}
	p := b.Paragraphs[1]
	if got := b.Text[p.Start:p.End]; got != "You don’t know about me without you have read a book." {
		t.Errorf("абзац: %q", got)
	}
}

func TestRuneOffsets(t *testing.T) {
	b, err := book.Parse(1, []byte("*** START OF THE PROJECT GUTENBERG EBOOK X ***\n\nPREFACE\n\n“Tom!” — ab\n\n*** END OF THE PROJECT GUTENBERG EBOOK X ***"))
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(b.Text, "ab")
	if want := len([]rune(b.Text[:i])); b.Rune(i) != want {
		t.Errorf("символ %d, ожидался %d", b.Rune(i), want)
	}
	if b.Rune(len(b.Text)) != len([]rune(b.Text)) {
		t.Errorf("конец текста")
	}
	if b.ID != "pg1" {
		t.Errorf("id неизвестной книги: %s", b.ID)
	}
}

func TestParseRejectsNonBook(t *testing.T) {
	if _, err := book.Parse(1, []byte("just some text")); err == nil {
		t.Fatal("текст без глав принят")
	}
}

func TestMirrorURLs(t *testing.T) {
	cases := map[int]string{
		74:   "https://m/7/74/74-0.txt",
		1342: "https://m/1/3/4/1342/1342-0.txt",
		5:    "https://m/0/5/5-0.txt",
	}
	for n, want := range cases {
		if got := book.MirrorURLs("https://m/", n)[0]; got != want {
			t.Errorf("%d: %s, ожидался %s", n, got, want)
		}
	}
}

func TestFetcherCachesAndPauses(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "-0.txt") {
			http.NotFound(w, r)
			return
		}
		w.Write(testkit.GutenbergFile("Mirror Book", testkit.Novel()))
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := &book.Fetcher{Mirror: srv.URL, Dir: dir, Pause: 50 * time.Millisecond}
	started := time.Now()
	raw, cached, err := f.Get(context.Background(), 74)
	if err != nil || cached || len(raw) == 0 {
		t.Fatalf("первое скачивание: cached=%v err=%v", cached, err)
	}
	if len(hits) != 2 || time.Since(started) < 50*time.Millisecond {
		t.Errorf("ожидались две попытки с паузой между ними: %v за %v", hits, time.Since(started))
	}
	if _, err := os.Stat(filepath.Join(dir, "pg74.txt")); err != nil {
		t.Errorf("файл не лёг в кэш: %v", err)
	}
	if _, cached, err := f.Get(context.Background(), 74); err != nil || !cached || len(hits) != 2 {
		t.Errorf("второй раз должна быть из кэша без запросов: cached=%v hits=%d", cached, len(hits))
	}
}

func TestFetcherRejectsNonGutenberg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<html>captcha</html>"))
	}))
	defer srv.Close()
	f := &book.Fetcher{Mirror: srv.URL, Dir: t.TempDir()}
	if _, _, err := f.Get(context.Background(), 74); err == nil || !strings.Contains(err.Error(), "положить файл вручную") {
		t.Fatalf("ожидалась понятная ошибка, получено %v", err)
	}
}

// chapterText -- абзац, которого хватает, чтобы глава считалась главой.
func chapterText(word string) string {
	return strings.Repeat("The "+word+" went on and on along the river bank. ", 20) + "\n\n"
}

func TestHeadingFormats(t *testing.T) {
	raw := "*** START OF THE PROJECT GUTENBERG EBOOK X ***\n\n" +
		// оглавление римскими цифрами, сплошным блоком
		"CONTENTS\n\nCHAPTER I. The Start\nCHAPTER II. The Middle\nCHAPTER III. The End\n\n\n" +
		// тело: арабские цифры, название на следующей строке или отдельным абзацем
		"CHAPTER 1\n\nTHE START OF IT ALL\n\n" + chapterText("start") +
		"Chapter 2.\n" + "A QUIET MIDDLE\n\n" + chapterText("middle") +
		"CHAPTER 3\n\n" + chapterText("end") +
		// указатель в конце книги -- тоже «Chapter N.», но без текста
		"NOTES\n\nChapter 1. see above\n\nChapter 3. and here\n\n" +
		"*** END OF THE PROJECT GUTENBERG EBOOK X ***"
	b, err := book.Parse(9, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range b.Sections {
		got = append(got, s.Key+"="+s.Title)
	}
	want := "1=The Start|2=The Middle|3=The End"
	if strings.Join(got, "|") != want {
		t.Fatalf("разделы %q, ожидалось %q", strings.Join(got, "|"), want)
	}
	if strings.Contains(b.Text, "THE START OF IT ALL") {
		t.Errorf("название главы попало в текст: %.200q", b.Text)
	}
	// указатель остаётся текстом последней главы, но главой не становится
	if !strings.Contains(b.Text, "NOTES") || len(b.Sections) != 3 {
		t.Errorf("хвост после последней главы: %d разделов", len(b.Sections))
	}
	if s, ok := b.FindSection("II"); !ok || s.Key != "2" || !book.SameKey("THE LAST", "the last") {
		t.Errorf("римский ключ к арабской главе: %+v %v", s, ok)
	}
}

func TestTitleFromOwnLine(t *testing.T) {
	raw := "*** START OF THE PROJECT GUTENBERG EBOOK X ***\n\nCHAPTER I\n\nKING ARTHUR'S COURT\n\n" + chapterText("court") +
		"CHAPTER II\n\n“Tom!” said she.\n\n" + chapterText("house") + "*** END OF THE PROJECT GUTENBERG EBOOK X ***"
	b, err := book.Parse(9, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b.Sections[0].Title != "King Arthur's Court" || b.Sections[1].Title != "" {
		t.Fatalf("названия: %q, %q", b.Sections[0].Title, b.Sections[1].Title)
	}
}
