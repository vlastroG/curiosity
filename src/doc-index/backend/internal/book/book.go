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
	"strconv"
	"strings"
	"unicode"
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
	74:   {"tom", "The Adventures of Tom Sawyer", "Приключения Тома Сойера", "Mark Twain"},
	76:   {"huck", "Adventures of Huckleberry Finn", "Приключения Гекльберри Финна", "Mark Twain"},
	91:   {"tom-abroad", "Tom Sawyer Abroad", "Том Сойер за границей", "Mark Twain"},
	93:   {"tom-detective", "Tom Sawyer, Detective", "Том Сойер — сыщик", "Mark Twain"},
	1837: {"prince", "The Prince and the Pauper", "Принц и нищий", "Mark Twain"},
	86:   {"yankee", "A Connecticut Yankee in King Arthur's Court", "Янки из Коннектикута при дворе короля Артура", "Mark Twain"},
	102:  {"wilson", "The Tragedy of Pudd'nhead Wilson", "Простофиля Вильсон", "Mark Twain"},
	245:  {"mississippi", "Life on the Mississippi", "Жизнь на Миссисипи", "Mark Twain"},
	3177: {"roughing", "Roughing It", "Налегке", "Mark Twain"},
	3176: {"innocents", "The Innocents Abroad", "Простаки за границей", "Mark Twain"},
	119:  {"tramp", "A Tramp Abroad", "Пешком по Европе", "Mark Twain"},
	2895: {"equator", "Following the Equator", "По экватору", "Mark Twain"},
	3186: {"stranger", "The Mysterious Stranger", "Таинственный незнакомец", "Mark Twain"},
}

var (
	startRe = regexp.MustCompile(`(?m)^\*\*\* ?START OF (THE|THIS) PROJECT GUTENBERG EBOOK.*$`)
	endRe   = regexp.MustCompile(`(?m)^\*\*\* ?END OF (THE|THIS) PROJECT GUTENBERG EBOOK.*$`)

	// «CHAPTER XII.», «CHAPTER 20», «Chapter 1», «CHAPTER I. TOM SEEKS NEW ADVENTURES»
	chapterRe = regexp.MustCompile(`^(CHAPTER|Chapter) ([IVXLC]+|\d+|THE LAST|the Last)\b\.?(.*)$`)
	// предисловие и прочее -- отдельной строкой между пустыми
	specialRe = regexp.MustCompile(`^(PREFACE|CONCLUSION|NOTICE|EXPLANATORY)\.?$`)
	italicRe  = regexp.MustCompile(`_([^_\n]+)_`)
	spacesRe  = regexp.MustCompile(`[ \t]+`)
	authorRe  = regexp.MustCompile(`(?m)^Author: (.+)$`)
	titleRe   = regexp.MustCompile(`(?m)^Title: (.+)$`)
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
	heads, tocTitles := bodyHeadings(lines)
	if len(heads) == 0 {
		return nil, fmt.Errorf("книга %d: не найдено ни одного заголовка главы", gutenberg)
	}

	// тело книги начинается с первого заголовка; всё выше -- титул,
	// оглавление, иллюстрации
	var sb strings.Builder
	for h, c := range heads {
		end := len(lines)
		if h+1 < len(heads) {
			end = heads[h+1].line
		}
		paras := paragraphs(lines[c.line+1+c.extra : end])
		title := tocTitles[c.id]
		if title == "" {
			title = c.desc
		}
		// название отдельным абзацем: забираем, если другого нет или оно набрано
		// капсом (тогда это точно не текст главы)
		if len(paras) > 1 && looksLikeTitle(paras[0]) && (title == "" || paras[0] == strings.ToUpper(paras[0])) {
			if title == "" {
				title = paras[0]
			}
			paras = paras[1:]
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sec := Section{N: len(b.Sections), Key: c.key, Label: c.label, Title: tidyTitle(title), Start: sb.Len()}
		sb.WriteString(strings.ToUpper(c.label))
		sec.BodyStart = -1

		for _, para := range paras {
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

// FindSection -- раздел по ключу («II», «2», «the last»). Римские и арабские
// номера равны: в одной книге оглавление бывает римским, а тело -- арабским.
func (b *Book) FindSection(key string) (Section, bool) {
	for _, s := range b.Sections {
		if SameKey(s.Key, key) {
			return s, true
		}
	}
	return Section{}, false
}

// SameKey -- один и тот же раздел: «II» и «2», «the last» и «THE LAST».
func SameKey(a, b string) bool {
	a, b = strings.ToUpper(strings.TrimSpace(a)), strings.ToUpper(strings.TrimSpace(b))
	if a == b {
		return true
	}
	na, nb := chapterNum(a), chapterNum(b)
	return na > 0 && na == nb
}

// heading -- строка, похожая на заголовок главы.
type heading struct {
	line    int
	id      string // «C12» для главы 12, «PREFACE» для предисловия
	key     string // как в тексте: «XII», «12», «THE LAST»
	label   string // «Chapter XII»
	desc    string // название в той же строке и строках сразу под ней
	extra   int    // сколько строк под заголовком ушло в название
	chapter bool   // глава, а не предисловие и т. п.
}

// bodyHeadings находит заголовки тела книги и названия глав из оглавления.
//
// Оглавление у Gutenberg записано теми же «CHAPTER I. …», что и тело, и стоит
// то в начале, то в конце, а в двухтомниках -- посередине. Поэтому сначала
// собираются все строки-кандидаты. Строка оглавления узнаётся по тому, что
// сразу за ней (и за её названием) идёт следующая строка оглавления, а не
// текст. Если глава с одним номером встретилась несколько раз, заголовком
// считается последнее вхождение с текстом: оглавление обычно впереди.
// Из строк оглавления берутся названия глав.
func bodyHeadings(lines []string) ([]heading, map[string]string) {
	var all []heading
	for i := range lines {
		afterHeading := len(all) > 0 && all[len(all)-1].line+all[len(all)-1].extra == i-1
		if h, ok := headingAt(lines, i, afterHeading); ok {
			all = append(all, h)
		}
	}
	gaps := make([]int, len(all))
	maxGap := 0
	for k, h := range all {
		end := len(lines)
		if k+1 < len(all) {
			end = all[k+1].line
		}
		for _, l := range lines[min(h.line+1+h.extra, end):end] {
			gaps[k] += len(strings.TrimSpace(l))
		}
		maxGap = max(maxGap, gaps[k])
	}
	// порог текста под заголовком: 400 символов, для совсем коротких книг меньше
	threshold := min(400, maxGap/4)
	passes := func(k int) bool {
		if !all[k].chapter {
			return gaps[k] > 0 // предисловие бывает в одну фразу
		}
		return gaps[k] > 0 && gaps[k] >= threshold
	}
	// строка оглавления, за которой случайно оказался текст (список иллюстраций
	// после последней главы оглавления): предыдущий кандидат был без текста,
	// а у той же главы есть другое вхождение с текстом
	tocLike := func(k int) bool {
		if k == 0 || gaps[k-1] >= threshold {
			return false
		}
		for j, x := range all {
			if j != k && x.id == all[k].id && passes(j) {
				return true
			}
		}
		return false
	}
	chosen := map[string]int{}
	for k, h := range all {
		if passes(k) && !tocLike(k) {
			chosen[h.id] = k // последнее вхождение с текстом
		}
	}
	titles := map[string]string{}
	var body []heading
	for k, h := range all {
		if c, ok := chosen[h.id]; ok && c == k {
			body = append(body, h)
			continue
		}
		if h.desc != "" && titles[h.id] == "" {
			titles[h.id] = h.desc
		}
	}
	return body, titles
}

// headingAt -- строка i как заголовок. Над заголовком -- пустая строка
// (или другой заголовок: сплошное оглавление без пустых строк).
func headingAt(lines []string, i int, afterHeading bool) (heading, bool) {
	line := strings.TrimSpace(lines[i])
	blankBefore := i == 0 || strings.TrimSpace(lines[i-1]) == "" || afterHeading
	if !blankBefore {
		return heading{}, false
	}
	if m := chapterRe.FindStringSubmatch(line); m != nil {
		key := strings.ToUpper(m[2])
		h := heading{line: i, key: key, label: "Chapter " + key, chapter: true}
		if key == "THE LAST" {
			h.label = "Chapter the Last"
			h.id = "LAST"
		} else {
			h.id = fmt.Sprintf("C%d", chapterNum(key))
		}
		var desc []string
		if rest := strings.TrimSpace(strings.TrimLeft(m[3], ".")); rest != "" {
			desc = append(desc, rest)
		}
		// название может продолжаться строками сразу под заголовком
		for j := i + 1; j < len(lines) && j <= i+6; j++ {
			next := strings.TrimSpace(lines[j])
			if next == "" || chapterRe.MatchString(next) {
				break
			}
			desc = append(desc, next)
			h.extra++
		}
		h.desc = strings.Join(desc, " ")
		if len([]rune(h.desc)) > 400 { // это уже текст, а не название
			h.desc, h.extra = "", 0
		}
		return h, true
	}
	if specialRe.MatchString(line) {
		blankAfter := i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) == ""
		if !blankAfter {
			return heading{}, false
		}
		word := strings.TrimSuffix(line, ".")
		return heading{line: i, id: word, key: word, label: word[:1] + strings.ToLower(word[1:])}, true
	}
	return heading{}, false
}

// chapterNum -- номер главы из «XII» или «12»; «THE LAST» -- после всех.
func chapterNum(key string) int {
	if key == "THE LAST" {
		return 100000
	}
	if n, err := strconv.Atoi(key); err == nil {
		return n
	}
	vals := map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100}
	n, prev := 0, 0
	for i := len(key) - 1; i >= 0; i-- {
		v, ok := vals[key[i]]
		if !ok {
			return 0
		}
		if v < prev {
			n -= v
		} else {
			n += v
			prev = v
		}
	}
	return n
}

// looksLikeTitle -- короткий первый абзац без точки в конце: название главы,
// набранное отдельной строкой («KING ARTHUR'S COURT», «A Catastrophe»).
func looksLikeTitle(p string) bool {
	r := []rune(p)
	if len(r) == 0 || len(r) > 60 {
		return false
	}
	return !strings.ContainsRune(".!?,;:”\"’)—", r[len(r)-1]) && !strings.ContainsRune("“\"‘(", r[0])
}

// tidyTitle -- название без лишних пробелов; КАПСЛОК -- в обычный вид.
func tidyTitle(t string) string {
	t = strings.TrimSpace(spacesRe.ReplaceAllString(t, " "))
	t = strings.ReplaceAll(t, "--", "—")
	t = strings.ReplaceAll(t, " —", "—")
	if t != strings.ToUpper(t) {
		return t
	}
	words := strings.Fields(strings.ToLower(t))
	for i, w := range words {
		r := []rune(w)
		for j, c := range r {
			if unicode.IsLetter(c) {
				r[j] = unicode.ToUpper(c)
				break
			}
		}
		words[i] = string(r)
	}
	return strings.Join(words, " ")
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
