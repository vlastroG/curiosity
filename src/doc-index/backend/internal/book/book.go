// Package book -- книга Project Gutenberg: скачивание с зеркала, очистка
// от служебного текста и разбор на разделы и абзацы.
//
// Из файла Gutenberg остаётся только текст романа: шапка, лицензия, оглавление
// и список иллюстраций вырезаются. Оглавление не пропадает зря -- из него берутся
// описания глав («Strong Temptations—Strategic Movements…»), они попадают
// в метаданные чанков и в читалку.
//
// Очищенный текст -- единственная система координат: смещения абзацев,
// разделов и чанков отсчитываются от его начала.
package book

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Book -- очищенная и размеченная книга.
type Book struct {
	ID        string `json:"id"`        // короткий id: tom, huck
	Gutenberg int    `json:"gutenberg"` // номер на gutenberg.org
	Title     string `json:"title"`
	TitleRu   string `json:"titleRu"`
	Author    string `json:"author"`
	SHA256    string `json:"sha256"` // исходного файла

	Text       string      `json:"-"`
	Sections   []Section   `json:"sections"`
	Paragraphs []Paragraph `json:"-"`

	runes []int32 // байтовое смещение → номер символа, для интерфейса
}

// Source -- «gutenberg:74», поле source в метаданных чанка.
func (b *Book) Source() string { return fmt.Sprintf("gutenberg:%d", b.Gutenberg) }

// URL -- страница книги на gutenberg.org. Прямые ссылки на файлы их правила
// просят не давать.
func (b *Book) URL() string { return fmt.Sprintf("https://www.gutenberg.org/ebooks/%d", b.Gutenberg) }

// Section -- раздел книги: предисловие, глава, заключение.
type Section struct {
	N         int    `json:"n"`     // порядковый номер, с нуля
	Key       string `json:"key"`   // «II», «THE LAST», «PREFACE»
	Label     string `json:"label"` // «Chapter II», «Preface»
	Title     string `json:"title"` // описание из оглавления, может быть пустым
	Start     int    `json:"-"`     // начало заголовка в Text, байты
	BodyStart int    `json:"-"`     // начало первого абзаца
	End       int    `json:"-"`
}

// Name -- «Chapter II. Strong Temptations—…», как раздел подписан в выдаче.
func (s Section) Name() string {
	if s.Title == "" {
		return s.Label
	}
	return s.Label + ". " + s.Title
}

// Paragraph -- абзац: диапазон в Text и номер раздела.
type Paragraph struct {
	Start, End int
	Section    int
}

// Known -- книги, у которых известны id и русское название. Остальные номера
// Gutenberg тоже индексируются, с id вида pg1342.
var Known = map[int]struct{ ID, Title, TitleRu, Author string }{
	74: {"tom", "The Adventures of Tom Sawyer", "Приключения Тома Сойера", "Mark Twain"},
	76: {"huck", "Adventures of Huckleberry Finn", "Приключения Гекльберри Финна", "Mark Twain"},
}

var (
	startRe = regexp.MustCompile(`(?m)^\*\*\* ?START OF (THE|THIS) PROJECT GUTENBERG EBOOK.*$`)
	endRe   = regexp.MustCompile(`(?m)^\*\*\* ?END OF (THE|THIS) PROJECT GUTENBERG EBOOK.*$`)

	// заголовок раздела в теле книги -- отдельная строка между пустыми
	headingRe = regexp.MustCompile(`^(CHAPTER ([IVXLC]+|THE LAST)|PREFACE|CONCLUSION|NOTICE|EXPLANATORY)\.?$`)
	// строка оглавления: «CHAPTER II. Strong Temptations…» или «CHAPTER II.» + описание ниже
	tocRe      = regexp.MustCompile(`^CHAPTER ([IVXLC]+|THE LAST)\.?\s*(.*)$`)
	contentsRe = regexp.MustCompile(`^CONTENTS\.?$`)
	italicRe   = regexp.MustCompile(`_([^_\n]+)_`)
	spacesRe   = regexp.MustCompile(`[ \t]+`)
	authorRe   = regexp.MustCompile(`(?m)^Author: (.+)$`)
	titleRe    = regexp.MustCompile(`(?m)^Title: (.+)$`)
)

// Parse очищает файл Gutenberg и размечает книгу.
func Parse(gutenberg int, raw []byte) (*Book, error) {
	sum := sha256.Sum256(raw)
	text := strings.TrimPrefix(string(raw), string(rune(0xFEFF)))
	text = strings.ReplaceAll(text, "\r\n", "\n")

	b := &Book{Gutenberg: gutenberg, SHA256: hex.EncodeToString(sum[:])}
	if k, ok := Known[gutenberg]; ok {
		b.ID, b.Title, b.TitleRu, b.Author = k.ID, k.Title, k.TitleRu, k.Author
	} else {
		b.ID = fmt.Sprintf("pg%d", gutenberg)
		b.Title, b.Author = headerField(titleRe, text, b.ID), headerField(authorRe, text, "")
	}

	// шапка до START и лицензия после END
	if m := startRe.FindStringIndex(text); m != nil {
		text = text[m[1]:]
	}
	if m := endRe.FindStringIndex(text); m != nil {
		text = text[:m[0]]
	}

	lines := strings.Split(text, "\n")
	toc := parseContents(lines)

	// тело книги начинается с первого заголовка; всё выше -- титул,
	// оглавление, иллюстрации
	var heads []int
	for i := range lines {
		if isHeading(lines, i) {
			heads = append(heads, i)
		}
	}
	if len(heads) == 0 {
		return nil, fmt.Errorf("книга %d: не найдено ни одного заголовка главы", gutenberg)
	}

	var sb strings.Builder
	for h, start := range heads {
		end := len(lines)
		if h+1 < len(heads) {
			end = heads[h+1]
		}
		key, label := headingKey(strings.TrimSpace(lines[start]))
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sec := Section{N: len(b.Sections), Key: key, Label: label, Title: toc[key], Start: sb.Len()}
		sb.WriteString(strings.ToUpper(label))
		sec.BodyStart = -1

		for _, para := range paragraphs(lines[start+1 : end]) {
			sb.WriteString("\n\n")
			p := Paragraph{Start: sb.Len(), Section: sec.N}
			sb.WriteString(para)
			p.End = sb.Len()
			if sec.BodyStart < 0 {
				sec.BodyStart = p.Start
			}
			b.Paragraphs = append(b.Paragraphs, p)
		}
		if sec.BodyStart < 0 {
			sec.BodyStart = sb.Len()
		}
		sec.End = sb.Len()
		b.Sections = append(b.Sections, sec)
	}
	b.Text = sb.String()
	b.index()
	return b, nil
}

// FromStored восстанавливает книгу из индекса: текст и разметку не нужно
// разбирать заново.
func FromStored(b *Book) *Book {
	b.index()
	return b
}

func (b *Book) index() {
	b.runes = make([]int32, len(b.Text)+1)
	n := int32(-1)
	for i := 0; i < len(b.Text); i++ {
		if utf8.RuneStart(b.Text[i]) {
			n++
		}
		b.runes[i] = n
	}
	b.runes[len(b.Text)] = n + 1
}

// Rune переводит байтовое смещение в номер символа. Интерфейс считает
// в символах JavaScript (UTF-16); в тексте Твена нет символов за пределами
// BMP, поэтому для него это одно и то же.
func (b *Book) Rune(offset int) int {
	if offset < 0 {
		return 0
	}
	if offset >= len(b.runes) {
		return int(b.runes[len(b.runes)-1])
	}
	return int(b.runes[offset])
}

// SectionAt -- номер раздела, в котором лежит смещение.
func (b *Book) SectionAt(offset int) int {
	for i := len(b.Sections) - 1; i >= 0; i-- {
		if offset >= b.Sections[i].Start {
			return i
		}
	}
	return 0
}

// SectionsIn -- разделы, текст которых захватывает диапазон [start, end).
// Заголовок главы в конце диапазона главу не захватывает: считается текст.
func (b *Book) SectionsIn(start, end int) []int {
	var out []int
	for _, s := range b.Sections {
		if s.BodyStart < end && start < s.End {
			out = append(out, s.N)
		}
	}
	if len(out) == 0 {
		out = []int{b.SectionAt(start)}
	}
	return out
}

// FindSection -- раздел по ключу («II», «the last»).
func (b *Book) FindSection(key string) (Section, bool) {
	key = strings.ToUpper(strings.TrimSpace(key))
	for _, s := range b.Sections {
		if s.Key == key {
			return s, true
		}
	}
	return Section{}, false
}

func isHeading(lines []string, i int) bool {
	line := strings.TrimSpace(lines[i])
	if !headingRe.MatchString(line) {
		return false
	}
	// в оглавлении «CHAPTER I.» стоит прямо над описанием -- это не заголовок
	blankBefore := i == 0 || strings.TrimSpace(lines[i-1]) == ""
	blankAfter := i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) == ""
	return blankBefore && blankAfter
}

func headingKey(line string) (key, label string) {
	line = strings.TrimSuffix(line, ".")
	if rest, ok := strings.CutPrefix(line, "CHAPTER "); ok {
		if rest == "THE LAST" {
			return rest, "Chapter the Last"
		}
		return rest, "Chapter " + rest
	}
	return line, strings.ToUpper(line[:1]) + strings.ToLower(line[1:])
}

// parseContents вытаскивает описания глав из оглавления.
func parseContents(lines []string) map[string]string {
	out := map[string]string{}
	begin := -1
	for i, l := range lines {
		if contentsRe.MatchString(strings.TrimSpace(l)) {
			begin = i + 1
			break
		}
	}
	if begin < 0 {
		return out
	}
	var key string
	var desc []string
	flush := func() {
		if key != "" {
			d := strings.Join(desc, " ")
			d = strings.ReplaceAll(d, " —", "—")
			out[key] = strings.TrimSpace(spacesRe.ReplaceAllString(d, " "))
		}
		key, desc = "", nil
	}
	for i := begin; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if isHeading(lines, i) {
			break // началось тело книги
		}
		if m := tocRe.FindStringSubmatch(l); m != nil {
			flush()
			key = m[1]
			if m[2] != "" {
				desc = append(desc, m[2])
			}
			continue
		}
		if l == "" {
			if key != "" && len(desc) > 0 {
				flush()
			}
			continue
		}
		if key != "" {
			desc = append(desc, l)
		} else if len(out) > 0 {
			break // оглавление кончилось: дальше иллюстрации
		}
	}
	flush()
	return out
}

// paragraphs склеивает строки абзацев: переносы строк внутри абзаца --
// артефакт вёрстки, а не текста.
func paragraphs(lines []string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		p := strings.Join(cur, " ")
		p = italicRe.ReplaceAllString(p, "$1")
		p = strings.TrimSpace(spacesRe.ReplaceAllString(p, " "))
		if p != "" {
			out = append(out, p)
		}
		cur = nil
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		cur = append(cur, strings.TrimSpace(l))
	}
	flush()
	return out
}

func headerField(re *regexp.Regexp, text, fallback string) string {
	if m := re.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return fallback
}
