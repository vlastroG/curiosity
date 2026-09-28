package main

// Источники: геокодер Open-Meteo и API Википедии.
//
// Достопримечательности берутся из Википедии, а не из OpenStreetMap: у статьи
// есть описание, и модель может выбрать место по интересам путешественника.
// Всё, что приходит из Википедии, написано кем угодно, поэтому текст очищается
// от управляющих символов и режется по длине ещё здесь.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// userAgent -- Википедия просит представляться.
const userAgent = "trip-planner-mcp-places/1.0 (https://github.com/modelcontextprotocol)"

// Places -- клиенты внешних API.
type Places struct {
	client     *http.Client
	geocodeURL string
	// wikiURL -- шаблон с %s на месте языка: https://%s.wikipedia.org/w/api.php
	wikiURL string
}

// ErrNotFound -- город или статья не найдены.
var ErrNotFound = errors.New("не найдено")

// City -- город, как его знает геокодер.
type City struct {
	Name        string  `json:"name" jsonschema:"название"`
	Country     string  `json:"country" jsonschema:"страна"`
	CountryCode string  `json:"countryCode" jsonschema:"код страны ISO из двух букв -- нужен серверу денег, чтобы узнать валюту"`
	Region      string  `json:"region,omitempty" jsonschema:"регион"`
	Latitude    float64 `json:"latitude" jsonschema:"широта -- передавай в погоду и в sights без изменений"`
	Longitude   float64 `json:"longitude" jsonschema:"долгота"`
	Timezone    string  `json:"timezone" jsonschema:"часовой пояс"`
	Population  int     `json:"population,omitempty" jsonschema:"население"`
}

// findCity ищет город; первым идёт самый крупный.
func (p *Places) findCity(ctx context.Context, name, countryCode string) ([]City, error) {
	query := url.Values{"name": {name}, "count": {"5"}, "language": {"ru"}, "format": {"json"}}
	if countryCode != "" {
		query.Set("countryCode", strings.ToUpper(countryCode))
	}
	var body struct {
		Results []struct {
			Name        string  `json:"name"`
			Country     string  `json:"country"`
			CountryCode string  `json:"country_code"`
			Admin1      string  `json:"admin1"`
			Latitude    float64 `json:"latitude"`
			Longitude   float64 `json:"longitude"`
			Timezone    string  `json:"timezone"`
			Population  int     `json:"population"`
		} `json:"results"`
	}
	if err := p.get(ctx, p.geocodeURL, query, &body); err != nil {
		return nil, err
	}
	if len(body.Results) == 0 {
		return nil, fmt.Errorf("%w: город %q", ErrNotFound, name)
	}
	cities := make([]City, 0, len(body.Results))
	for _, r := range body.Results {
		cities = append(cities, City{
			Name: clean(r.Name, 100), Country: clean(r.Country, 100), CountryCode: strings.ToUpper(r.CountryCode),
			Region: clean(r.Admin1, 100), Latitude: r.Latitude, Longitude: r.Longitude,
			Timezone: r.Timezone, Population: r.Population,
		})
	}
	return cities, nil
}

// Sight -- место рядом с точкой.
type Sight struct {
	PageID      int     `json:"page_id" jsonschema:"id статьи -- передавай в sight_info"`
	Lang        string  `json:"lang" jsonschema:"язык статьи: ru или en"`
	Title       string  `json:"title" jsonschema:"название"`
	Description string  `json:"description,omitempty" jsonschema:"короткое описание: что это за место"`
	Latitude    float64 `json:"latitude" jsonschema:"широта"`
	Longitude   float64 `json:"longitude" jsonschema:"долгота"`
	DistanceM   int     `json:"distanceM" jsonschema:"расстояние от центра поиска, м"`
}

// sights -- статьи Википедии о местах рядом, ближайшие первыми.
func (p *Places) sights(ctx context.Context, lang string, lat, lon float64, radiusM, limit int) ([]Sight, error) {
	query := url.Values{
		"action":        {"query"},
		"generator":     {"geosearch"},
		"ggscoord":      {fmt.Sprintf("%s|%s", coord(lat), coord(lon))},
		"ggsradius":     {strconv.Itoa(radiusM)},
		"ggslimit":      {strconv.Itoa(limit)},
		"prop":          {"description|coordinates"},
		"format":        {"json"},
		"formatversion": {"2"},
	}
	var body struct {
		Query struct {
			Pages []struct {
				PageID      int    `json:"pageid"`
				Title       string `json:"title"`
				Index       int    `json:"index"`
				Description string `json:"description"`
				Coordinates []struct {
					Lat float64 `json:"lat"`
					Lon float64 `json:"lon"`
				} `json:"coordinates"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := p.get(ctx, fmt.Sprintf(p.wikiURL, lang), query, &body); err != nil {
		return nil, err
	}
	out := make([]Sight, 0, len(body.Query.Pages))
	for _, page := range body.Query.Pages {
		if len(page.Coordinates) == 0 {
			continue
		}
		c := page.Coordinates[0]
		out = append(out, Sight{
			PageID: page.PageID, Lang: lang, Title: clean(page.Title, 200), Description: clean(page.Description, 200),
			Latitude: c.Lat, Longitude: c.Lon, DistanceM: distanceM(lat, lon, c.Lat, c.Lon),
		})
	}
	// порядок страниц в ответе генератора не гарантирован -- сортируем по расстоянию
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].DistanceM < out[j-1].DistanceM; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// SightInfo -- описание места.
type SightInfo struct {
	PageID  int    `json:"page_id" jsonschema:"id статьи"`
	Lang    string `json:"lang" jsonschema:"язык статьи"`
	Title   string `json:"title" jsonschema:"название"`
	Extract string `json:"extract" jsonschema:"вступление статьи, до 1200 символов"`
	URL     string `json:"url" jsonschema:"ссылка на статью"`
}

func (p *Places) sightInfo(ctx context.Context, lang string, pageID int) (SightInfo, error) {
	query := url.Values{
		"action":        {"query"},
		"pageids":       {strconv.Itoa(pageID)},
		"prop":          {"extracts|info"},
		"exintro":       {"1"},
		"explaintext":   {"1"},
		"inprop":        {"url"},
		"format":        {"json"},
		"formatversion": {"2"},
	}
	var body struct {
		Query struct {
			Pages []struct {
				PageID  int    `json:"pageid"`
				Title   string `json:"title"`
				Extract string `json:"extract"`
				FullURL string `json:"fullurl"`
				Missing bool   `json:"missing"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := p.get(ctx, fmt.Sprintf(p.wikiURL, lang), query, &body); err != nil {
		return SightInfo{}, err
	}
	if len(body.Query.Pages) == 0 || body.Query.Pages[0].Missing || body.Query.Pages[0].PageID == 0 {
		return SightInfo{}, fmt.Errorf("%w: статья %d в %s-Википедии", ErrNotFound, pageID, lang)
	}
	page := body.Query.Pages[0]
	link := page.FullURL
	if !strings.HasPrefix(link, "https://") {
		link = fmt.Sprintf("https://%s.wikipedia.org/?curid=%d", lang, page.PageID)
	}
	return SightInfo{
		PageID: page.PageID, Lang: lang, Title: clean(page.Title, 200),
		Extract: clean(dropParens(page.Extract), 1200), URL: link,
	}, nil
}

func (p *Places) get(ctx context.Context, base string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("источник не ответил: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("источник вернул %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("ответ источника не разобрался: %w", err)
	}
	return nil
}

var (
	spaces = regexp.MustCompile(`[ \t\r\f\v]+`)
	lines  = regexp.MustCompile(`\n{2,}`)
	// dropParens убирает транскрипции и даты в скобках в начале статьи:
	// «Айя-София (греч. …; тур. …) — …» читается проще без них
	parens = regexp.MustCompile(`\s*\([^()]{0,300}\)`)
)

func dropParens(s string) string {
	// ударения в русской Википедии -- комбинируемый символ U+0301; в плане они мешают
	s = strings.ReplaceAll(s, "́", "")
	runes := []rune(s)
	if len(runes) > 600 {
		return parens.ReplaceAllString(string(runes[:600]), "") + string(runes[600:])
	}
	return parens.ReplaceAllString(s, "")
}

// clean -- текст без управляющих символов и лишних пробелов, не длиннее n символов.
func clean(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(lines.ReplaceAllString(spaces.ReplaceAllString(s, " "), "\n"))
	if utf8.RuneCountInString(s) > n {
		cut := string([]rune(s)[:n])
		if i := strings.LastIndexAny(cut, " \n"); i > n/2 {
			cut = cut[:i]
		}
		s = strings.TrimSpace(cut) + "…"
	}
	return s
}

func coord(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
