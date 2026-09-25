// Command pipeline-agent -- CLI-агент, который по теме на русском находит обсуждения
// на Hacker News, конспектирует их по-русски и сохраняет в HTML. Всё это -- через
// инструменты MCP-сервера mcp-pipeline; порядок вызовов выбирает модель.
//
//	pipeline-agent "что обсуждают про Rust в вебе"   -- один запрос
//	pipeline-agent                                    -- интерактивно: запрос> …
//
// Каждый шаг пишется в stdout и в файл DATA_DIR/logs/agent-<время>.log.
// Код выхода в режиме одного запроса: 0 -- файл сохранён и цепочка проверена,
// 1 -- ошибка или цепочка нарушена, 2 -- запрос отклонён как не относящийся к делу.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// version -- версия агента, её видит MCP-сервер.
const version = "1.0.0"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger, closeLog := openLog(env("DATA_DIR", "./data"))
	defer closeLog()

	model, provider, err := resolveModel(os.Getenv("PIPELINE_MODEL"), os.Getenv, "pipeline-agent")
	if err != nil {
		logger.Printf("остановка: %v", err)
		return 1
	}
	endpoint := env("MCP_URL", "http://localhost:8765/mcp")
	logger.Printf("[агент] модель %s (%s), MCP-сервер %s", model.ID, provider.Title, endpoint)

	agent := &Agent{
		chat:     newChatClient(durationEnv("LLM_TIMEOUT", 3*time.Minute)),
		provider: provider,
		model:    model.ID,
		// summarize на бесплатной модели думает долго -- таймаут вызова щедрый
		tools: &MCP{endpoint: endpoint, timeout: durationEnv("MCP_TIMEOUT", 6*time.Minute)},
		log:   logger,
	}

	if len(os.Args) > 1 {
		return handle(ctx, agent, logger, strings.Join(os.Args[1:], " "))
	}

	// интерактивный режим: запросы по одному, пустая строка или Ctrl+D -- выход.
	// Исход каждого запроса виден в логе; штатно закрытая сессия -- код 0
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\nзапрос> ")
		if !in.Scan() {
			fmt.Println()
			return 0
		}
		request := strings.TrimSpace(in.Text())
		if request == "" {
			return 0
		}
		handle(ctx, agent, logger, request)
		if ctx.Err() != nil {
			return 1
		}
	}
}

// handle -- один запрос: прогон агента, проверка цепочки, итог.
func handle(ctx context.Context, agent *Agent, logger *log.Logger, request string) int {
	started := time.Now()
	logger.Printf("[агент] ── запрос: %q", request)

	outcome, err := agent.Run(ctx, request)
	if err != nil {
		logger.Printf("[агент] ✘ прогон не удался: %v", err)
		return 1
	}
	if outcome.Refused {
		logger.Printf("[агент] запрос отклонён: %s", strings.TrimSpace(strings.TrimPrefix(outcome.Answer, refusalPrefix)))
		return 2
	}

	files, err := fetchFiles(ctx, agent.tools)
	if err != nil {
		logger.Printf("[агент] ✘ опись файлов не получена: %v", err)
		return 1
	}
	report := verify(outcome.Trace, files)
	for _, link := range report.Links {
		logger.Printf("[проверка] %s ✔", link)
	}
	if !report.OK() {
		for _, v := range report.Violations {
			logger.Printf("[проверка] ✘ %s", v)
		}
		logger.Printf("[агент] ✘ цепочка нарушена, шагов %d, %s", len(outcome.Trace), since(started))
		return 1
	}
	logger.Printf("[проверка] ✔ цепочка корректна: данные дошли от поиска до файла out/%s без подмены", report.File)
	logger.Printf("[агент] ответ модели: %s", outcome.Answer)
	logger.Printf("[агент] ✔ готово за %s, шагов %d, файл out/%s", since(started), len(outcome.Trace), report.File)
	return 0
}

// openLog -- лог в stdout и в файл на томе. Если файл не открылся, пишем только в stdout.
func openLog(dataDir string) (*log.Logger, func()) {
	var out io.Writer = os.Stdout
	closer := func() {}
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err == nil {
		name := filepath.Join(dir, "agent-"+time.Now().UTC().Format("20060102-150405")+".log")
		if f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			out = io.MultiWriter(os.Stdout, f)
			closer = func() { f.Close() }
		}
	}
	return log.New(out, "", log.LstdFlags), closer
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// durationEnv читает длительность: принимает и число секунд, и запись вида "90s".
func durationEnv(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
