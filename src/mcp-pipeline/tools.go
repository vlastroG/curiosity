package main

// Инструменты сервера -- звенья цепочки.
//
//	hn_search(query) ──► src_… ──► summarize(search_id) ──► sum_… ──► save_to_file(summary_id) ──► out/….html
//
// Порядок никто не зашивает: его выстраивает тот, кто зовёт инструменты. Связывают
// звенья id артефактов, и каждое звено проверяет вход -- тип и хеш. Подсунуть
// summarize свой текст вместо выдачи или сохранить конспект к подменённой выдаче
// нельзя: инструмент откажет с понятной причиной.
//
// Каждый вызов оставляет в логе строку «шаг …»: что пришло, что ушло, сколько заняло.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Границы параметров поиска.
const (
	defaultLimit = 5
	maxLimit     = 10
	maxDays      = 3650
	maxQuery     = 200
	maxTopic     = 200
)

// Pipeline -- зависимости инструментов.
type Pipeline struct {
	hn         *HN
	artifacts  *Artifacts
	summarizer *Summarizer
	output     *Output
	now        func() time.Time
}

// SearchInput -- аргументы hn_search.
type SearchInput struct {
	Query string `json:"query" jsonschema:"поисковый запрос к Hacker News на английском: 2–5 ключевых слов, например «rust web framework»"`
	Topic string `json:"topic,omitempty" jsonschema:"тема пользователя его словами, по-русски -- станет заголовком конспекта"`
	Limit int    `json:"limit,omitempty" jsonschema:"сколько историй взять, от 1 до 10; по умолчанию 5"`
	Days  int    `json:"days,omitempty" jsonschema:"только истории за последние N дней; 0 или не указано -- за всё время"`
	Sort  string `json:"sort,omitempty" jsonschema:"relevance -- самые подходящие (по умолчанию), date -- самые свежие"`
}

// StoryBrief -- история в ответе поиска.
type StoryBrief struct {
	ID        string `json:"id" jsonschema:"id истории на HN"`
	Title     string `json:"title" jsonschema:"заголовок"`
	URL       string `json:"url" jsonschema:"ссылка"`
	Points    int    `json:"points" jsonschema:"очки"`
	Comments  int    `json:"comments" jsonschema:"число комментариев"`
	CreatedAt string `json:"createdAt" jsonschema:"дата публикации"`
}

// SearchOutput -- результат hn_search.
type SearchOutput struct {
	SearchID string       `json:"search_id" jsonschema:"id выдачи -- передай его в summarize"`
	SHA256   string       `json:"sha256" jsonschema:"хеш содержимого выдачи"`
	Query    string       `json:"query" jsonschema:"запрос, по которому искали"`
	Count    int          `json:"count" jsonschema:"сколько историй найдено"`
	Stories  []StoryBrief `json:"stories" jsonschema:"найденные истории"`
}

// SummarizeInput -- аргументы summarize.
type SummarizeInput struct {
	SearchID string `json:"search_id" jsonschema:"id выдачи из результата hn_search, вида src_0123456789"`
}

// SummarizeOutput -- результат summarize.
type SummarizeOutput struct {
	SummaryID    string `json:"summary_id" jsonschema:"id конспекта -- передай его в save_to_file"`
	SHA256       string `json:"sha256" jsonschema:"хеш содержимого конспекта"`
	SourceID     string `json:"source_id" jsonschema:"по какой выдаче сделан конспект"`
	SourceSHA256 string `json:"source_sha256" jsonschema:"хеш выдачи на момент конспекта"`
	Model        string `json:"model" jsonschema:"модель, сделавшая конспект"`
	Stories      int    `json:"stories" jsonschema:"сколько историй в конспекте"`
	Missing      int    `json:"missing" jsonschema:"сколько историй модель пропустила"`
	Overview     string `json:"overview" jsonschema:"общий обзор на русском"`
}

// SaveInput -- аргументы save_to_file.
type SaveInput struct {
	SummaryID string `json:"summary_id" jsonschema:"id конспекта из результата summarize, вида sum_0123456789"`
	Filename  string `json:"filename,omitempty" jsonschema:"короткое имя файла латиницей через дефис, например rust-web; расширение и дата добавятся сами"`
}

// ListOutput -- результат list_files.
type ListOutput struct {
	Files []SavedFile `json:"files" jsonschema:"сохранённые конспекты, свежие первыми"`
}

// newServer собирает MCP-сервер.
func newServer(p *Pipeline) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-pipeline", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "hn_search",
		Description: "Шаг 1. Найти истории на Hacker News по запросу и сохранить выдачу вместе с главными комментариями. " +
			"Запрос -- английские ключевые слова: HN англоязычный. Возвращает search_id для summarize. " +
			"Если историй нет или они не по теме -- переформулируй запрос и поищи снова.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		started := time.Now()
		out, err := p.search(ctx, in)
		if err != nil {
			log.Printf("шаг hn_search ← query=%q ✘ %v", in.Query, err)
			return nil, SearchOutput{}, err
		}
		log.Printf("шаг hn_search ← query=%q limit=%d days=%d sort=%s → %s (%d историй, sha %s) %s",
			out.Query, clamp(in.Limit, defaultLimit, 1, maxLimit), in.Days, sortOrDefault(in.Sort), out.SearchID, out.Count, shortHash(out.SHA256), elapsed(started))
		return textResult(renderSearch(out)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "summarize",
		Description: "Шаг 2. Сделать русскоязычный конспект выдачи: обзор и по каждой истории перевод заголовка, " +
			"суть обсуждения и тезисы. Принимает только search_id из hn_search. Возвращает summary_id для save_to_file.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SummarizeInput) (*mcp.CallToolResult, SummarizeOutput, error) {
		started := time.Now()
		out, err := p.summarize(ctx, strings.TrimSpace(in.SearchID))
		if err != nil {
			log.Printf("шаг summarize ← search_id=%q ✘ %v", in.SearchID, err)
			return nil, SummarizeOutput{}, err
		}
		log.Printf("шаг summarize ← %s (sha %s) → %s (%d историй, пропущено %d, sha %s, модель %s) %s",
			out.SourceID, shortHash(out.SourceSHA256), out.SummaryID, out.Stories, out.Missing, shortHash(out.SHA256), out.Model, elapsed(started))
		return textResult(fmt.Sprintf(
			"Конспект готов: summary_id=%s (sha256 %s), по выдаче %s (sha256 %s), историй %d, пропущено %d.\n\nОбзор: %s\n\nСледующий шаг: save_to_file с summary_id=%s.",
			out.SummaryID, shortHash(out.SHA256), out.SourceID, shortHash(out.SourceSHA256), out.Stories, out.Missing, out.Overview, out.SummaryID)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "save_to_file",
		Description: "Шаг 3. Сохранить конспект в HTML-файл. Принимает только summary_id из summarize. " +
			"Возвращает имя файла, размер и хеш.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SaveInput) (*mcp.CallToolResult, SavedFile, error) {
		started := time.Now()
		out, err := p.save(strings.TrimSpace(in.SummaryID), in.Filename)
		if err != nil {
			log.Printf("шаг save_to_file ← summary_id=%q ✘ %v", in.SummaryID, err)
			return nil, SavedFile{}, err
		}
		log.Printf("шаг save_to_file ← %s (sha %s) → out/%s (%d байт, sha %s) %s",
			out.SummaryID, shortHash(out.SummarySHA), out.File, out.Bytes, shortHash(out.SHA256), elapsed(started))
		return textResult(fmt.Sprintf("Сохранено: %s, %d байт, sha256 %s. Конспект %s по выдаче %s.",
			out.File, out.Bytes, shortHash(out.SHA256), out.SummaryID, out.SourceID)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_files",
		Description: "Список сохранённых HTML-конспектов, свежие первыми.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListOutput, error) {
		files, err := p.output.List()
		if err != nil {
			return nil, ListOutput{}, err
		}
		var text strings.Builder
		fmt.Fprintf(&text, "Сохранено конспектов: %d\n", len(files))
		for _, f := range files {
			fmt.Fprintf(&text, "- %s: «%s», историй %d, %s\n", f.File, f.Title, f.Stories, f.CreatedAt)
		}
		return textResult(text.String()), ListOutput{Files: files}, nil
	})

	return server
}

func (p *Pipeline) search(ctx context.Context, in SearchInput) (SearchOutput, error) {
	query := strings.Join(strings.Fields(in.Query), " ")
	topic := clip(strings.Join(strings.Fields(in.Topic), " "), maxTopic)
	if query == "" {
		return SearchOutput{}, errors.New("пустой запрос")
	}
	if len([]rune(query)) > maxQuery {
		return SearchOutput{}, fmt.Errorf("запрос длиннее %d символов -- нужны ключевые слова, а не текст", maxQuery)
	}
	sort := sortOrDefault(in.Sort)
	if sort != "relevance" && sort != "date" {
		return SearchOutput{}, fmt.Errorf("sort может быть relevance или date, а не %q", in.Sort)
	}
	limit := clamp(in.Limit, defaultLimit, 1, maxLimit)
	days := clamp(in.Days, 0, 0, maxDays)

	stories, err := p.hn.search(ctx, query, limit, days, sort, p.now())
	if err != nil {
		return SearchOutput{}, err
	}
	p.hn.withComments(ctx, stories)

	art, err := p.artifacts.Put(KindSearch, SearchResult{Query: query, Topic: topic, Sort: sort, Days: days, Stories: stories})
	if err != nil {
		return SearchOutput{}, err
	}
	out := SearchOutput{SearchID: art.ID, SHA256: art.SHA256, Query: query, Count: len(stories), Stories: []StoryBrief{}}
	for _, s := range stories {
		out.Stories = append(out.Stories, StoryBrief{ID: s.ID, Title: s.Title, URL: s.URL, Points: s.Points, Comments: s.Comments, CreatedAt: s.CreatedAt})
	}
	return out, nil
}

func (p *Pipeline) summarize(ctx context.Context, searchID string) (SummarizeOutput, error) {
	src, err := p.artifacts.Get(searchID, KindSearch)
	if err != nil {
		return SummarizeOutput{}, err
	}
	var result SearchResult
	if err := json.Unmarshal(src.Payload, &result); err != nil {
		return SummarizeOutput{}, err
	}

	summary, err := p.summarizer.Summarize(ctx, result)
	if err != nil {
		return SummarizeOutput{}, err
	}
	summary.SourceID = src.ID
	summary.SourceSHA = src.SHA256

	art, err := p.artifacts.Put(KindSummary, summary)
	if err != nil {
		return SummarizeOutput{}, err
	}
	missing := 0
	for _, s := range summary.Stories {
		if s.Missing {
			missing++
		}
	}
	return SummarizeOutput{
		SummaryID:    art.ID,
		SHA256:       art.SHA256,
		SourceID:     src.ID,
		SourceSHA256: src.SHA256,
		Model:        summary.Model,
		Stories:      len(summary.Stories),
		Missing:      missing,
		Overview:     summary.Overview,
	}, nil
}

func (p *Pipeline) save(summaryID, filename string) (SavedFile, error) {
	art, err := p.artifacts.Get(summaryID, KindSummary)
	if err != nil {
		return SavedFile{}, err
	}
	var summary Summary
	if err := json.Unmarshal(art.Payload, &summary); err != nil {
		return SavedFile{}, err
	}

	// конспект сделан по конкретной выдаче: она должна быть на месте и не измениться
	src, err := p.artifacts.Get(summary.SourceID, KindSearch)
	if err != nil {
		return SavedFile{}, fmt.Errorf("выдача, по которой сделан конспект, недоступна: %w", err)
	}
	if src.SHA256 != summary.SourceSHA {
		return SavedFile{}, fmt.Errorf("выдача %s изменилась после конспекта: было %s, стало %s",
			src.ID, shortHash(summary.SourceSHA), shortHash(src.SHA256))
	}
	return p.output.Save(summary, art, filename)
}

func renderSearch(out SearchOutput) string {
	var b strings.Builder
	if out.Count == 0 {
		fmt.Fprintf(&b, "По запросу «%s» ничего не найдено (search_id=%s). Переформулируй запрос: другие ключевые слова или шире период (больше days или без него).", out.Query, out.SearchID)
		return b.String()
	}
	fmt.Fprintf(&b, "Найдено историй: %d по запросу «%s». search_id=%s (sha256 %s).\n", out.Count, out.Query, out.SearchID, shortHash(out.SHA256))
	for i, s := range out.Stories {
		fmt.Fprintf(&b, "%d. %s — %d очков, %d комментариев, %s\n", i+1, s.Title, s.Points, s.Comments, dateOnly(s.CreatedAt))
	}
	fmt.Fprintf(&b, "\nЕсли истории по теме, следующий шаг: summarize с search_id=%s.", out.SearchID)
	return b.String()
}

func sortOrDefault(sort string) string {
	sort = strings.ToLower(strings.TrimSpace(sort))
	if sort == "" {
		return "relevance"
	}
	return sort
}

func dateOnly(iso string) string {
	if len(iso) >= 10 {
		return iso[:10]
	}
	return iso
}

func elapsed(started time.Time) string {
	return fmt.Sprintf("%.1fs", time.Since(started).Seconds())
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// clamp -- значение в границах, ноль означает «по умолчанию».
func clamp(value, fallback, low, high int) int {
	if value == 0 {
		return fallback
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
