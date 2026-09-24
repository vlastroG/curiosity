package main

// Сохранение конспекта в HTML.
//
// Файл самодостаточный: стили внутри, скриптов нет, внешних ресурсов нет -- его
// можно открыть с диска, переслать почтой или положить на любой статический хостинг.
// Все данные проходят через html/template: заголовки и конспекты родом из чужих
// комментариев, и сырым HTML они в файл не попадут. Ссылки -- только http(s).
//
// Рядом лежит index.json -- опись сохранённого, и index.html, пересобираемый из неё
// при каждом сохранении.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SavedFile -- запись описи.
type SavedFile struct {
	File       string `json:"file"`
	Query      string `json:"query"`
	Title      string `json:"title"`
	Stories    int    `json:"stories"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
	SummaryID  string `json:"summaryId"`
	SummarySHA string `json:"summarySha256"`
	SourceID   string `json:"sourceId"`
	SourceSHA  string `json:"sourceSha256"`
	Model      string `json:"model"`
	CreatedAt  string `json:"createdAt"`
}

// Output -- каталог с готовыми файлами.
type Output struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

func newOutput(dir string) (*Output, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Output{dir: dir, now: time.Now}, nil
}

var (
	notSlug   = regexp.MustCompile(`[^a-z0-9]+`)
	maxSlug   = 60
	indexJSON = "index.json"
)

// slug -- безопасное имя файла из того, что предложила модель: только [a-z0-9-],
// не длиннее 60 символов. Ни каталогов, ни расширений, ни точек -- путь всегда
// внутри каталога вывода.
func slug(name string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".html")
	name = strings.Trim(notSlug.ReplaceAllString(name, "-"), "-")
	if len(name) > maxSlug {
		name = strings.Trim(name[:maxSlug], "-")
	}
	if name == "" {
		name = "hn-digest"
	}
	return name
}

// Save рендерит конспект в файл и обновляет опись и index.html.
func (o *Output) Save(summary Summary, summaryArt Artifact, name string) (SavedFile, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := o.now()
	// время в имени: одинаковые запросы в разные дни не затирают друг друга
	file := fmt.Sprintf("%s-%s.html", slug(name), now.UTC().Format("20060102-150405"))
	for i := 2; exists(filepath.Join(o.dir, file)); i++ {
		file = fmt.Sprintf("%s-%s-%d.html", slug(name), now.UTC().Format("20060102-150405"), i)
	}

	var buf bytes.Buffer
	err := pageTemplate.Execute(&buf, pageData{
		Summary:    summary,
		SummaryID:  summaryArt.ID,
		SummarySHA: summaryArt.SHA256,
		CreatedAt:  now.UTC().Format("2006-01-02 15:04 UTC"),
	})
	if err != nil {
		return SavedFile{}, err
	}
	if err := os.WriteFile(filepath.Join(o.dir, file), buf.Bytes(), 0o644); err != nil {
		return SavedFile{}, err
	}

	saved := SavedFile{
		File:       file,
		Query:      summary.Query,
		Title:      summary.Title(),
		Stories:    len(summary.Stories),
		Bytes:      buf.Len(),
		SHA256:     digest(buf.Bytes()),
		SummaryID:  summaryArt.ID,
		SummarySHA: summaryArt.SHA256,
		SourceID:   summary.SourceID,
		SourceSHA:  summary.SourceSHA,
		Model:      summary.Model,
		CreatedAt:  now.UTC().Format(time.RFC3339),
	}

	files, err := o.list()
	if err != nil {
		return SavedFile{}, err
	}
	files = append([]SavedFile{saved}, files...)
	raw, _ := json.MarshalIndent(files, "", "  ")
	if err := os.WriteFile(filepath.Join(o.dir, indexJSON), raw, 0o644); err != nil {
		return SavedFile{}, err
	}
	var index bytes.Buffer
	if err := indexTemplate.Execute(&index, files); err != nil {
		return SavedFile{}, err
	}
	return saved, os.WriteFile(filepath.Join(o.dir, "index.html"), index.Bytes(), 0o644)
}

// List -- опись сохранённого, свежие первыми.
func (o *Output) List() ([]SavedFile, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.list()
}

func (o *Output) list() ([]SavedFile, error) {
	raw, err := os.ReadFile(filepath.Join(o.dir, indexJSON))
	if errors.Is(err, os.ErrNotExist) {
		return []SavedFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	var files []SavedFile
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, fmt.Errorf("опись %s повреждена: %w", indexJSON, err)
	}
	// файл, удалённый руками, из описи тоже уходит
	kept := files[:0]
	for _, f := range files {
		if exists(filepath.Join(o.dir, f.File)) {
			kept = append(kept, f)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].CreatedAt > kept[j].CreatedAt })
	return kept, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

type pageData struct {
	Summary    Summary
	SummaryID  string
	SummarySHA string
	CreatedAt  string
}

// safeURL -- только http(s). Остальное (javascript:, data:) заменяется ссылкой на HN.
func safeURL(link, fallback string) string {
	if strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "http://") {
		return link
	}
	return fallback
}

var funcs = template.FuncMap{
	"safeURL": safeURL,
	"short":   shortHash,
	"date": func(iso string) string {
		t, err := time.Parse(time.RFC3339, iso)
		if err != nil {
			return iso
		}
		return t.UTC().Format("02.01.2006")
	},
	"inc": func(i int) int { return i + 1 },
}

const styles = `
:root{--bg:#f7f7f5;--panel:#fff;--text:#1d1d1b;--muted:#6b6b66;--border:#e3e2dd;--accent:#e2641d;--soft:#fdf0e8}
@media (prefers-color-scheme:dark){:root{--bg:#141413;--panel:#1d1d1b;--text:#e9e8e3;--muted:#9a9990;--border:#2e2d2a;--accent:#f08a4b;--soft:#2a1f18}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:16px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:860px;margin:0 auto;padding:32px 16px 48px}
h1{font-size:26px;line-height:1.25;margin:0 0 6px}
.meta{color:var(--muted);font-size:14px}
.overview{background:var(--soft);border-left:3px solid var(--accent);padding:14px 18px;border-radius:6px;margin:22px 0 28px}
.story{background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:18px 20px;margin-bottom:14px}
.story h2{font-size:19px;line-height:1.35;margin:0}
.story h2 a{color:inherit;text-decoration:none}
.story h2 a:hover{color:var(--accent)}
.orig{color:var(--muted);font-size:13px;margin:2px 0 8px;overflow-wrap:anywhere}
.orig a{color:var(--muted)}
.stats{color:var(--muted);font-size:13px;margin-bottom:10px}
.stats a{color:var(--accent)}
.story ul{margin:10px 0 0;padding-left:20px}
.missing{color:var(--muted);font-style:italic}
.provenance{margin-top:36px;padding-top:14px;border-top:1px solid var(--border);color:var(--muted);font-size:13px}
.provenance code{font:12px ui-monospace,Consolas,monospace;background:var(--panel);border:1px solid var(--border);border-radius:4px;padding:1px 5px}
table{border-collapse:collapse;width:100%}
td,th{text-align:left;padding:10px 8px;border-bottom:1px solid var(--border);vertical-align:top}
th{color:var(--muted);font-weight:500;font-size:13px}
td a{color:var(--accent)}
`

var pageTemplate = template.Must(template.New("page").Funcs(funcs).Parse(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Summary.Title}} — конспект Hacker News</title>
<style>` + styles + `</style>
</head>
<body>
<main>
<h1>{{.Summary.Title}}</h1>
<div class="meta">Конспект обсуждений Hacker News · запрос «{{.Summary.Query}}» · {{.CreatedAt}} · историй: {{len .Summary.Stories}} · модель {{.Summary.Model}}</div>

<section class="overview">{{.Summary.Overview}}</section>

{{range $i, $s := .Summary.Stories}}
<article class="story">
  <h2><a href="{{safeURL $s.URL $s.HNURL}}" rel="noopener noreferrer">{{inc $i}}. {{$s.TitleRu}}</a></h2>
  <div class="orig">{{$s.Title}} · <a href="{{safeURL $s.URL $s.HNURL}}" rel="noopener noreferrer">{{safeURL $s.URL $s.HNURL}}</a></div>
  <div class="stats">{{$s.Points}} очков · <a href="{{$s.HNURL}}" rel="noopener noreferrer">{{$s.Comments}} комментариев</a> · {{date $s.CreatedAt}}</div>
  {{if $s.Missing}}<p class="missing">{{$s.Summary}}</p>{{else}}<p>{{$s.Summary}}</p>
  {{if $s.Takeaways}}<ul>{{range $s.Takeaways}}<li>{{.}}</li>{{end}}</ul>{{end}}{{end}}
</article>
{{end}}

<footer class="provenance">
  Происхождение: запрос к HN «{{.Summary.Query}}» →
  выдача <code>{{.Summary.SourceID}}</code> (sha256 <code>{{short .Summary.SourceSHA}}</code>) →
  конспект <code>{{.SummaryID}}</code> (sha256 <code>{{short .SummarySHA}}</code>) → этот файл.
  <a href="index.html">Все конспекты</a>
</footer>
</main>
</body>
</html>
`))

var indexTemplate = template.Must(template.New("index").Funcs(funcs).Parse(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Конспекты Hacker News</title>
<style>` + styles + `</style>
</head>
<body>
<main>
<h1>Конспекты Hacker News</h1>
<div class="meta">Всего: {{len .}}</div>
<table>
<tr><th>Запрос</th><th>Историй</th><th>Дата</th><th>Модель</th></tr>
{{range .}}<tr><td><a href="{{.File}}">{{if .Title}}{{.Title}}{{else}}{{.Query}}{{end}}</a></td><td>{{.Stories}}</td><td>{{date .CreatedAt}}</td><td>{{.Model}}</td></tr>
{{end}}
</table>
</main>
</body>
</html>
`))
