package main

// Публикация плана в HTML.
//
// Имя файла строит сервер из города и дат -- модель путь не задаёт. Файл
// самодостаточный: стили внутри, скриптов и внешних ресурсов нет. Все тексты
// проходят через html/template: часть из них пересказана из Википедии.

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t",
	'у': "u", 'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "",
	'э': "e", 'ю': "yu", 'я': "ya",
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug -- имя файла латиницей: «Стамбул» → stambul.
func slug(city string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(city) {
		if t, ok := translit[r]; ok {
			b.WriteString(t)
		} else {
			b.WriteRune(r)
		}
	}
	s := strings.Trim(notSlug.ReplaceAllString(b.String(), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "trip"
	}
	return s
}

// Publish рендерит план в файл в каталоге out.
func (s *Store) Publish(id, summary, outDir string) (Trip, error) {
	summary = text(summary, maxSummaryRunes)
	return s.update(id, func(t *Trip) error {
		if missing := t.Missing(); len(missing) > 0 {
			return fmt.Errorf("%w: в плане нет дней %s -- добавь их через trip_add_day", ErrInvalid, strings.Join(missing, ", "))
		}
		t.Summary = summary
		t.PublishedAt = s.now().UTC().Format(time.RFC3339)

		var buf bytes.Buffer
		if err := pageTemplate.Execute(&buf, *t); err != nil {
			return err
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return err
		}
		name := fmt.Sprintf("%s-%s-%s.html", slug(t.City), t.StartDate, strings.TrimPrefix(t.ID, "trip_"))
		if err := os.WriteFile(filepath.Join(outDir, name), buf.Bytes(), 0o644); err != nil {
			return err
		}
		t.File = name
		return nil
	})
}

var months = []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

var funcs = template.FuncMap{
	"day": func(iso string) string {
		d, err := time.Parse(time.DateOnly, iso)
		if err != nil {
			return iso
		}
		weekdays := []string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}
		return fmt.Sprintf("%d %s, %s", d.Day(), months[d.Month()-1], weekdays[d.Weekday()])
	},
	"temp": func(v float64) string {
		if v > 0 {
			return fmt.Sprintf("+%.0f°", v)
		}
		return fmt.Sprintf("%.0f°", v)
	},
	"money": func(v float64) string {
		s := fmt.Sprintf("%.0f", v)
		var out []byte
		for i := range s {
			if i > 0 && (len(s)-i)%3 == 0 {
				out = append(out, ' ')
			}
			out = append(out, s[i])
		}
		return string(out)
	},
	"safeURL": func(u string) string {
		if httpURL(u) {
			return u
		}
		return ""
	},
}

var pageTemplate = template.Must(template.New("page").Funcs(funcs).Parse(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.City}} — план поездки</title>
<style>
:root{--bg:#f6f5f1;--panel:#fff;--text:#1d1d1b;--muted:#6b6b66;--border:#e3e2dd;--accent:#0f7b8a;--soft:#e6f3f4}
@media (prefers-color-scheme:dark){:root{--bg:#131514;--panel:#1c1f1e;--text:#e8e9e6;--muted:#9aa09c;--border:#2c302e;--accent:#4fb8c4;--soft:#172a2c}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:16px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:860px;margin:0 auto;padding:32px 16px 48px}
h1{font-size:28px;margin:0 0 4px}
h2{font-size:19px;margin:0 0 8px}
.meta{color:var(--muted);font-size:14px}
.summary{background:var(--soft);border-left:3px solid var(--accent);padding:14px 18px;margin:20px 0}
.card{background:var(--panel);border:1px solid var(--border);border-radius:10px;padding:16px 20px;margin-bottom:14px}
.weather{color:var(--muted);font-size:14px;margin-bottom:8px}
.tag{display:inline-block;font-size:12px;border:1px solid var(--border);border-radius:10px;padding:0 8px;margin-left:6px}
ul{margin:6px 0 0;padding-left:20px}
li{margin-bottom:8px}
.time{color:var(--muted);font-size:13px;margin-right:6px}
a{color:var(--accent)}
table{border-collapse:collapse;width:100%;font-size:14px}
td{padding:6px 0;border-bottom:1px solid var(--border)}
td:last-child{text-align:right}
.notes{color:var(--muted);font-size:14px}
</style>
</head>
<body>
<main>
<h1>{{.City}}{{if .Country}}, {{.Country}}{{end}}</h1>
<div class="meta">{{day .StartDate}} — {{day .EndDate}} · путешественников: {{.Travelers}}</div>
{{if .Summary}}<div class="summary">{{.Summary}}</div>{{end}}

{{range .Days}}
<section class="card">
  <h2>{{day .Date}}</h2>
  <div class="weather">{{.Weather.Summary}}, {{temp .Weather.Min}}…{{temp .Weather.Max}}, осадки {{.Weather.PrecipChance}}%
    <span class="tag">{{if eq .Weather.Source "forecast"}}прогноз{{else}}климатическая норма{{end}}</span></div>
  <ul>{{range .Activities}}
    <li>{{if .Time}}<span class="time">{{.Time}}</span>{{end}}{{if safeURL .URL}}<a href="{{safeURL .URL}}" rel="noopener noreferrer">{{.Title}}</a>{{else}}<b>{{.Title}}</b>{{end}}{{if .Description}} — {{.Description}}{{end}}</li>{{end}}
  </ul>
  {{if .Notes}}<p class="notes">{{.Notes}}</p>{{end}}
</section>
{{end}}

{{with .Budget}}
<section class="card">
  <h2>Бюджет</h2>
  <p>{{money .Total}} {{.Currency}}{{if .LocalTotal}} ≈ {{money .LocalTotal}} {{.LocalCurrency}}{{end}}{{if .RateDate}} <span class="notes">(курс ЦБ РФ на {{.RateDate}})</span>{{end}}</p>
  {{if .PerPersonPerDay}}<p>На человека в день: {{money .PerPersonPerDay}} {{if .LocalCurrency}}{{.LocalCurrency}}{{else}}{{.Currency}}{{end}}</p>{{end}}
  {{if .Categories}}<table>{{range .Categories}}<tr><td>{{.Name}}</td><td>{{money .Amount}}</td></tr>{{end}}</table>{{end}}
</section>
{{end}}

{{if .Notes}}<section class="card"><h2>Допущения</h2><ul>{{range .Notes}}<li>{{.}}</li>{{end}}</ul></section>{{end}}
<p class="meta">План собран автоматически по открытым данным: Open-Meteo, Википедия, курсы ЦБ РФ.</p>
</main>
</body>
</html>
`))
