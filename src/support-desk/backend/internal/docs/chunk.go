package docs

import (
	"path"
	"regexp"
	"strings"
)

// chunkerVersion входит в отпечаток: поменяли нарезку -- индекс перестроится.
const chunkerVersion = "1"

const (
	maxRunes = 1500
	overlap  = 200
)

// Piece -- фрагмент файла для поиска.
type Piece struct {
	Path  string
	Title string
	Text  string // первая строка -- путь файла: модели и BM25 он тоже подсказка
}

type section struct {
	title string
	lines []string
}

var (
	mdHeading  = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	pyTopLevel = regexp.MustCompile(`^(?:async\s+)?(?:def|class)\s+([A-Za-z_][A-Za-z0-9_]*)`)
)

// Chunk режет файл: markdown и rst -- по заголовкам, python -- по функциям и
// классам верхнего уровня, остальное -- окнами. Длинные куски -- окнами
// по maxRunes с перекрытием overlap.
func Chunk(p, text string) []Piece {
	text = strings.ToValidUTF8(strings.ReplaceAll(text, "\r\n", "\n"), "")
	lines := strings.Split(text, "\n")
	var sections []section
	switch strings.ToLower(path.Ext(p)) {
	case ".md":
		sections = splitMarkdown(lines, path.Base(p))
	case ".rst":
		sections = splitRST(lines, path.Base(p))
	case ".py":
		sections = splitPython(lines, p)
	default:
		sections = []section{{title: p, lines: lines}}
	}

	var out []Piece
	for _, s := range sections {
		body := strings.TrimSpace(strings.Join(s.lines, "\n"))
		if body == "" {
			continue
		}
		for _, w := range windows(body) {
			out = append(out, Piece{Path: p, Title: s.title, Text: p + "\n" + w})
		}
	}
	return out
}

func splitMarkdown(lines []string, first string) []section {
	cur := section{title: first}
	var out []section
	fence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
		}
		if m := mdHeading.FindStringSubmatch(line); m != nil && !fence {
			out = append(out, cur)
			cur = section{title: m[1]}
		}
		cur.lines = append(cur.lines, line)
	}
	return append(out, cur)
}

func splitRST(lines []string, first string) []section {
	cur := section{title: first}
	var out []section
	for i := 0; i < len(lines); i++ {
		title := strings.TrimSpace(lines[i])
		if title != "" && i+1 < len(lines) && isUnderline(lines[i+1]) && !isUnderline(lines[i]) {
			out = append(out, cur)
			cur = section{title: title}
		}
		cur.lines = append(cur.lines, lines[i])
	}
	return append(out, cur)
}

// isUnderline -- строка подчёркивания заголовка rst: «=====», «-----» и т. п.
func isUnderline(line string) bool {
	line = strings.TrimRight(line, " \t")
	if len(line) < 3 || !strings.ContainsRune(`=-~^"'#*+`+"`", rune(line[0])) {
		return false
	}
	return strings.Count(line, line[:1]) == len(line)
}

// splitPython режет по def/class в нулевой колонке; декораторы над ними уходят
// в тот же кусок.
func splitPython(lines []string, p string) []section {
	cur := section{title: p}
	var out []section
	for i := 0; i < len(lines); i++ {
		m := pyTopLevel.FindStringSubmatch(lines[i])
		if m == nil {
			cur.lines = append(cur.lines, lines[i])
			continue
		}
		// декораторы уже попали в текущий кусок -- переносим их в новый
		start := len(cur.lines)
		for start > 0 && strings.HasPrefix(cur.lines[start-1], "@") {
			start--
		}
		decorators := append([]string(nil), cur.lines[start:]...)
		cur.lines = cur.lines[:start]
		out = append(out, cur)
		cur = section{title: p + ":" + m[1], lines: append(decorators, lines[i])}
	}
	return append(out, cur)
}

// windows режет текст на куски не длиннее maxRunes с перекрытием overlap.
func windows(text string) []string {
	r := []rune(text)
	if len(r) <= maxRunes {
		return []string{text}
	}
	var out []string
	for start := 0; start < len(r); start += maxRunes - overlap {
		end := min(start+maxRunes, len(r))
		out = append(out, string(r[start:end]))
		if end == len(r) {
			break
		}
	}
	return out
}
