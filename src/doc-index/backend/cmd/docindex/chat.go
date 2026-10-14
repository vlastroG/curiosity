package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"doc-index/internal/chat"
	"doc-index/internal/rag"
)

// chatCmd -- беседа в терминале. Чат сохраняется и виден в веб-интерфейсе
// первому пользователю.
func chatCmd(ctx context.Context) error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	if a.llmErr != nil {
		return a.llmErr
	}
	svc := a.service(a.agent(a.searcher()))
	c, err := a.chats.Create(ctx, a.owner(), a.settings)
	if err != nil {
		return err
	}
	fmt.Printf("Чат %d. Модель %s. Команды: /topic тема -- закрепить тему, /state -- память задачи, /exit.\n", c.ID, a.llm.Model)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 64<<10)
	for {
		fmt.Print("\n> ")
		if !in.Scan() {
			return in.Err()
		}
		line := strings.TrimSpace(in.Text())
		switch {
		case line == "":
			continue
		case line == "/exit":
			return nil
		case line == "/state":
			printState(c)
			continue
		case strings.HasPrefix(line, "/topic"):
			st := c.State
			st.Topic = strings.TrimSpace(strings.TrimPrefix(line, "/topic"))
			st.TopicLocked = st.Topic != ""
			if err := a.chats.SetState(ctx, c.ID, st); err != nil {
				fmt.Println("ошибка:", err)
				continue
			}
			c.State = st
			fmt.Println("тема закреплена:", st.Topic)
			continue
		}
		if c, err = svc.Send(ctx, c.ID, line); err != nil {
			fmt.Println("ошибка:", err)
			continue
		}
		printAnswer(c.Messages[len(c.Messages)-1])
		fmt.Printf("  · тема: %s · тезисов %d · открытых вопросов %d", c.State.Topic, len(c.State.Theses), len(c.State.Open))
		if c.SummarizedUpto > 0 {
			fmt.Printf(" · в сводке %d сообщений", c.SummarizedUpto)
		}
		fmt.Println()
	}
}

func printAnswer(m chat.Message) {
	if m.Error != "" {
		fmt.Println("\nошибка:", m.Error)
		return
	}
	fmt.Printf("\n%s\n", m.Text)
	if m.Result == nil || len(m.Result.Sources) == 0 {
		fmt.Println("\n  Источники: в книгах не нашлось")
		return
	}
	fmt.Println("\n  Источники:")
	for _, s := range m.Result.Sources {
		fmt.Printf("  [%d] %s · %s (%s)\n", s.N, s.BookTitle, s.Section, s.ChunkID)
	}
}

func printState(c *chat.Chat) {
	lock := ""
	if c.State.TopicLocked {
		lock = " (закреплена)"
	}
	fmt.Printf("Тема: %s%s\n", c.State.Topic, lock)
	for _, it := range c.State.Theses {
		fmt.Printf("  тезис [%s, после #%d]: %s\n", it.By, it.Since, it.Text)
	}
	for _, it := range c.State.Open {
		fmt.Printf("  открыт [%s]: %s\n", it.By, it.Text)
	}
	if c.Summary != "" {
		fmt.Printf("Сводка (сообщения 1–%d): %s\n", c.SummarizedUpto, c.Summary)
	}
}

// Scenario -- длинная беседа для проверки памяти.
type Scenario struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Turns []struct {
		Text  string `json:"text"`
		Drift bool   `json:"drift,omitempty"` // реплика уводит в сторону
	} `json:"turns"`
}

// TurnReport -- что проверяется на каждом ходу.
type TurnReport struct {
	N          int     `json:"n"`
	Text       string  `json:"text"`
	Drift      bool    `json:"drift,omitempty"`
	Sources    int     `json:"sources"`
	Cited      bool    `json:"cited"` // в ответе есть ссылки [n]
	Answer     string  `json:"answer"`
	Error      string  `json:"error,omitempty"`
	Topic      string  `json:"topic"`
	Theses     int     `json:"theses"`
	Open       int     `json:"open"`
	Summarized int     `json:"summarized"` // сообщений в сводке
	Seconds    float64 `json:"seconds"`
}

// ScenarioReport -- итог сценария.
type ScenarioReport struct {
	ID         string       `json:"id"`
	Title      string       `json:"title"`
	ChatID     int64        `json:"chatId"`
	Turns      []TurnReport `json:"turns"`
	WithSource int          `json:"withSources"` // ответов с источниками
	Cited      int          `json:"cited"`
	Errors     int          `json:"errors"`
	FirstTopic string       `json:"firstTopic"`
	LastTopic  string       `json:"lastTopic"`
	SameTopic  bool         `json:"sameTopic"`
	Why        string       `json:"why,omitempty"`
	Compressed bool         `json:"compressed"`
	Theses     []rag.Item   `json:"theses"`
}

var citeRe = regexp.MustCompile(`\[\d+(?:\s*[,;]\s*\d+)*\]`)

// scenarioCmd прогоняет сценарии, чаты остаются в базе -- их можно открыть
// в интерфейсе.
func scenarioCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("scenario", flag.ContinueOnError)
	file := fs.String("file", env("SCENARIOS_FILE", "../eval/scenarios.json"), "сценарии")
	only := fs.String("only", "", "id одного сценария")
	compress := fs.Int("compress", 8, "сжимать после N сообщений")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var scenarios []Scenario
	if err := json.Unmarshal(raw, &scenarios); err != nil {
		return fmt.Errorf("%s: %w", *file, err)
	}
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.store.Close()
	if a.llmErr != nil {
		return a.llmErr
	}
	svc := a.service(a.agent(a.searcher()))
	judge := rag.Judge{LLM: a.llm}
	st := a.settings
	st.CompressAfter = *compress

	var reports []ScenarioReport
	for _, sc := range scenarios {
		if *only != "" && sc.ID != *only {
			continue
		}
		c, err := a.chats.Create(ctx, a.owner(), st)
		if err != nil {
			return err
		}
		if err := a.chats.Rename(ctx, c.ID, "Сценарий: "+sc.Title); err != nil {
			return err
		}
		rep := ScenarioReport{ID: sc.ID, Title: sc.Title, ChatID: c.ID}
		log.Printf("сценарий %s: %d реплик, чат %d", sc.ID, len(sc.Turns), c.ID)
		for i, t := range sc.Turns {
			started := time.Now()
			c, err = svc.Send(ctx, c.ID, t.Text)
			if err != nil {
				return err
			}
			m := c.Messages[len(c.Messages)-1]
			tr := TurnReport{N: i + 1, Text: t.Text, Drift: t.Drift, Answer: m.Text, Error: m.Error, Topic: c.State.Topic,
				Theses: len(c.State.Theses), Open: len(c.State.Open), Summarized: c.SummarizedUpto,
				Seconds: time.Since(started).Seconds(), Cited: citeRe.MatchString(m.Text)}
			if m.Result != nil {
				tr.Sources = len(m.Result.Sources)
			}
			if tr.Sources > 0 {
				rep.WithSource++
			}
			if tr.Cited {
				rep.Cited++
			}
			if tr.Error != "" {
				rep.Errors++
			}
			if rep.FirstTopic == "" {
				rep.FirstTopic = tr.Topic
			}
			rep.Turns = append(rep.Turns, tr)
			log.Printf("  %2d. отрывков %d, ссылки %v, тезисов %d, в сводке %d, %.0f с · %s", tr.N, tr.Sources, tr.Cited,
				tr.Theses, tr.Summarized, tr.Seconds, shorten(tr.Topic, 60))
		}
		rep.LastTopic, rep.Compressed, rep.Theses = c.State.Topic, c.SummarizedUpto > 0, c.State.Theses
		if rep.FirstTopic == rep.LastTopic {
			rep.SameTopic = rep.FirstTopic != ""
		} else if rep.SameTopic, rep.Why, err = judge.SameTopic(ctx, rep.FirstTopic, rep.LastTopic); err != nil {
			rep.Why = "судья: " + err.Error()
		}
		reports = append(reports, rep)
	}
	if len(reports) == 0 {
		return errors.New("сценариев не найдено")
	}

	fmt.Printf("\n%-14s %6s %10s %8s %7s %6s  %s\n", "сценарий", "ходов", "источники", "ссылки", "ошибки", "сжато", "тема сохранена")
	for _, r := range reports {
		fmt.Printf("%-14s %6d %7d/%-2d %5d/%-2d %7d %6v  %v\n", r.ID, len(r.Turns), r.WithSource, len(r.Turns), r.Cited,
			len(r.Turns), r.Errors, r.Compressed, r.SameTopic)
		fmt.Printf("  в начале: %s\n  в конце:  %s\n", r.FirstTopic, r.LastTopic)
		if r.Why != "" {
			fmt.Printf("  %s\n", r.Why)
		}
	}
	path := filepath.Join(a.dataDir, "scenarios-report.json")
	b, _ := json.MarshalIndent(reports, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	log.Printf("отчёт: %s; чаты сценариев -- во вкладке «Чат»", path)
	return nil
}
