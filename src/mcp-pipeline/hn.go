package main

// Клиент Hacker News через Algolia HN Search API: без ключа и без регистрации.
//
// Два эндпоинта. search отдаёт истории по запросу, items/{id} -- историю целиком
// с деревом комментариев. Комментарии нужны конспекту: сама история на HN -- это
// чаще всего ссылка и заголовок, а суть -- в обсуждении.
//
// Всё, что приходит отсюда, написано пользователями HN, то есть кем угодно. Текст
// очищается от разметки и режется по длине ещё здесь, до того как попасть к модели.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Границы того, сколько текста уходит модели с одной истории.
const (
	maxComments     = 8
	maxCommentRunes = 600
	maxStoryRunes   = 1500
	maxItemBytes    = 8 << 20
)

// Story -- история HN с тем, что нужно для конспекта.
type Story struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	HNURL     string    `json:"hnUrl"`
	Author    string    `json:"author"`
	Points    int       `json:"points"`
	Comments  int       `json:"comments"`
	CreatedAt string    `json:"createdAt"`
	Text      string    `json:"text,omitempty"`
	Top       []Comment `json:"topComments"`
}

// Comment -- комментарий верхнего уровня.
type Comment struct {
	Author string `json:"author"`
	Text   string `json:"text"`
}

// HN -- клиент API.
type HN struct {
	client *http.Client
	base   string
}

// search ищет истории. sort: "relevance" -- по релевантности, "date" -- свежие первыми.
// days > 0 ограничивает выдачу последними днями.
func (h *HN) search(ctx context.Context, query string, limit, days int, sort string, now time.Time) ([]Story, error) {
	endpoint := "/api/v1/search"
	if sort == "date" {
		endpoint = "/api/v1/search_by_date"
	}
	params := url.Values{
		"query":       {query},
		"tags":        {"story"},
		"hitsPerPage": {strconv.Itoa(limit)},
	}
	if days > 0 {
		params.Set("numericFilters", fmt.Sprintf("created_at_i>%d", now.Add(-time.Duration(days)*24*time.Hour).Unix()))
	}

	var resp struct {
		Hits []struct {
			ObjectID    string `json:"objectID"`
			Title       string `json:"title"`
			URL         string `json:"url"`
			Author      string `json:"author"`
			Points      int    `json:"points"`
			NumComments int    `json:"num_comments"`
			CreatedAt   string `json:"created_at"`
			StoryText   string `json:"story_text"`
		} `json:"hits"`
	}
	if err := h.get(ctx, endpoint+"?"+params.Encode(), 1<<20, &resp); err != nil {
		return nil, err
	}

	stories := make([]Story, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		hnURL := "https://news.ycombinator.com/item?id=" + hit.ObjectID
		link := hit.URL
		if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			link = hnURL
		}
		stories = append(stories, Story{
			ID:        hit.ObjectID,
			Title:     clip(cleanText(hit.Title), 300),
			URL:       link,
			HNURL:     hnURL,
			Author:    hit.Author,
			Points:    hit.Points,
			Comments:  hit.NumComments,
			CreatedAt: hit.CreatedAt,
			Text:      clip(cleanText(hit.StoryText), maxStoryRunes),
			Top:       []Comment{},
		})
	}
	return stories, nil
}

// withComments дотягивает к историям комментарии верхнего уровня, до четырёх
// историй одновременно. История без комментариев -- не ошибка; упавший запрос --
// тоже нет: конспект по заголовку лучше, чем провал всего поиска.
func (h *HN) withComments(ctx context.Context, stories []Story) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range stories {
		wg.Add(1)
		go func(story *Story) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			comments, err := h.comments(ctx, story.ID)
			if err == nil {
				story.Top = comments
			}
		}(&stories[i])
	}
	wg.Wait()
}

func (h *HN) comments(ctx context.Context, id string) ([]Comment, error) {
	var item struct {
		Children []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
			Type   string `json:"type"`
		} `json:"children"`
	}
	if err := h.get(ctx, "/api/v1/items/"+url.PathEscape(id), maxItemBytes, &item); err != nil {
		return nil, err
	}
	out := []Comment{}
	for _, child := range item.Children {
		text := clip(cleanText(child.Text), maxCommentRunes)
		if child.Type != "comment" || text == "" {
			continue
		}
		out = append(out, Comment{Author: child.Author, Text: text})
		if len(out) == maxComments {
			break
		}
	}
	return out, nil
}

func (h *HN) get(ctx context.Context, path string, limit int64, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.base, "/")+path, nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("Hacker News не ответил: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return fmt.Errorf("ответ Hacker News не дочитался: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Hacker News вернул %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("ответ Hacker News не разобрался: %w", err)
	}
	return nil
}

var (
	paragraph = regexp.MustCompile(`(?i)<p\s*/?>`)
	tag       = regexp.MustCompile(`<[^>]*>`)
	spaces    = regexp.MustCompile(`[ \t\r\f\v]+`)
	newlines  = regexp.MustCompile(`\n{3,}`)
)

// cleanText -- HTML комментария HN в простой текст.
func cleanText(s string) string {
	s = paragraph.ReplaceAllString(s, "\n\n")
	s = tag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.Map(func(r rune) rune {
		// управляющие символы не нужны никому, кроме того, кто хочет что-то спрятать
		if r < 0x20 && r != '\n' {
			return ' '
		}
		return r
	}, s)
	s = spaces.ReplaceAllString(s, " ")
	s = newlines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// clip режет текст до n символов по границе слова.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)[:n]
	cut := string(runes)
	if i := strings.LastIndexAny(cut, " \n"); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}
