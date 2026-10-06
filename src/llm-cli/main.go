// llmcli -- CLI для локальной модели qwen3.5:9b, запущенной в Ollama.
//
//	llmcli ask [флаги] "вопрос"   -- спросить модель, ответ печатается по мере генерации
//	llmcli serve                  -- HTTP-сервер в формате OpenAI для других сервисов
//	llmcli help                   -- справка, сведения о модели и её ограничения
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

const model = "qwen3.5:9b"

// optionNames -- флаги ask, которые уходят в options Ollama, и их имена там.
var optionNames = map[string]string{
	"temperature":    "temperature",
	"top-p":          "top_p",
	"top-k":          "top_k",
	"repeat-penalty": "repeat_penalty",
	"ctx":            "num_ctx",
	"max-tokens":     "num_predict",
	"seed":           "seed",
	"stop":           "stop",
}

const limitations = `Ограничения:
  - небольшая модель (9B): уступает облачным моделям, может выдумывать факты
  - нет доступа к интернету; знания заканчиваются датой обучения
  - каждый ask независим: модель не помнит прошлые вопросы
  - контекстное окно ограничено (--ctx); длинный ввод обрезается
  - без видеокарты в Docker работает на процессоре -- во много раз медленнее`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var stdin io.Reader
	// из терминала запрос не читаем -- только из конвейера или файла
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		stdin = os.Stdin
	}
	url := os.Getenv("OLLAMA_URL")
	if url == "" {
		url = "http://ollama:11434"
	}
	os.Exit(run(ctx, newClient(url), os.Args[1:], stdin, os.Stdout, os.Stderr))
}

// run выполняет команду и возвращает код выхода.
func run(ctx context.Context, c *client, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := "help"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "ask":
		if err := ask(ctx, c, args[1:], stdin, stdout, stderr); err != nil {
			if !errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(stderr, "ошибка:", err)
			}
			return 1
		}
		return 0
	case "serve":
		if err := serve(ctx, c, args[1:], stderr); err != nil {
			if !errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(stderr, "ошибка:", err)
			}
			return 1
		}
		return 0
	case "help", "-h", "--help":
		help(ctx, c, stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "неизвестная команда %q\n\n", cmd)
		help(ctx, c, stderr)
		return 2
	}
}

// stopList -- флаг --stop, который можно повторять.
type stopList []string

func (s *stopList) String() string     { return strings.Join(*s, ",") }
func (s *stopList) Set(v string) error { *s = append(*s, v); return nil }
func (s *stopList) Get() any           { return []string(*s) }

type askFlags struct {
	system string
	think  bool
	stats  bool
}

func newAskFlags(out io.Writer) (*flag.FlagSet, *askFlags) {
	f := &askFlags{}
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&f.system, "system", "", "системный промпт")
	fs.Float64("temperature", 0, "случайность ответа, 0..2 (по умолчанию -- значение модели)")
	fs.Float64("top-p", 0, "nucleus sampling, 0..1")
	fs.Int("top-k", 0, "выбор из k самых вероятных токенов")
	fs.Float64("repeat-penalty", 0, "штраф за повторы, напр. 1.1")
	fs.Int("ctx", 0, "контекстное окно в токенах (num_ctx)")
	fs.Int("max-tokens", 0, "предел длины ответа в токенах (num_predict)")
	fs.Int("seed", 0, "зерно случайности для воспроизводимых ответов")
	fs.Var(&stopList{}, "stop", "стоп-последовательность (можно повторять)")
	fs.BoolVar(&f.think, "think", false, "режим рассуждений")
	fs.BoolVar(&f.stats, "stats", false, "напечатать в stderr число токенов и скорость")
	return fs, f
}

// buildRequest разбирает флаги и аргументы ask. В options попадают только заданные флаги.
func buildRequest(args []string, stdin io.Reader, stderr io.Writer) (chatRequest, *askFlags, error) {
	fs, f := newAskFlags(stderr)
	if err := fs.Parse(args); err != nil {
		return chatRequest{}, nil, err
	}
	prompt := strings.Join(fs.Args(), " ")
	if stdin != nil {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return chatRequest{}, nil, err
		}
		// аргумент -- инструкция, stdin -- материал к ней
		prompt = strings.TrimSpace(prompt + "\n\n" + string(data))
	}
	if strings.TrimSpace(prompt) == "" {
		return chatRequest{}, nil, errors.New(`нет запроса: llmcli ask "вопрос" или echo вопрос | llmcli ask`)
	}

	req := chatRequest{Model: model, Think: f.think, Options: map[string]any{}}
	if f.system != "" {
		req.Messages = append(req.Messages, message{Role: "system", Content: f.system})
	}
	req.Messages = append(req.Messages, message{Role: "user", Content: prompt})
	fs.Visit(func(fl *flag.Flag) {
		if name, ok := optionNames[fl.Name]; ok {
			req.Options[name] = fl.Value.(flag.Getter).Get()
		}
	})
	return req, f, nil
}

func ask(ctx context.Context, c *client, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	req, f, err := buildRequest(args, stdin, stderr)
	if err != nil {
		return err
	}
	if _, err := c.show(ctx, model); errors.Is(err, errNotFound) {
		fmt.Fprintf(stderr, "скачиваю %s (один раз, ~6.6 ГБ)...\n", model)
		if err := c.pull(ctx, model); err != nil {
			return fmt.Errorf("загрузка модели: %w", err)
		}
	} else if err != nil {
		return err
	}
	st, err := c.chat(ctx, req, stdout)
	fmt.Fprintln(stdout)
	if err != nil {
		return err
	}
	if f.stats {
		tps := 0.0
		if st.EvalDuration > 0 {
			tps = float64(st.EvalCount) / (float64(st.EvalDuration) / 1e9)
		}
		fmt.Fprintf(stderr, "токены: запрос %d, ответ %d, %.1f ток/с\n", st.PromptEvalCount, st.EvalCount, tps)
	}
	return nil
}

func help(ctx context.Context, c *client, out io.Writer) {
	fmt.Fprint(out, `llmcli -- CLI для локальной LLM в Ollama

Использование:
  llmcli ask [флаги] "вопрос"     спросить модель (флаги -- до вопроса)
  llmcli serve [--addr :8080] [--ctx N]
                                  HTTP-сервер /v1/chat/completions (формат OpenAI) для других сервисов
  llmcli help                     эта справка

Примеры:
  llmcli ask "Что такое горутина?"
  llmcli ask --temperature 0 --seed 1 --max-tokens 200 "Придумай название кофейни"
  cat main.go | llmcli ask "Найди ошибки в коде"
  docker compose run --rm llmcli ask "Привет"

Флаги ask (не заданные -- значения модели по умолчанию):
`)
	fs, _ := newAskFlags(out)
	fs.PrintDefaults()

	fmt.Fprintf(out, "\nМодель: %s\n", model)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	switch info, err := c.show(ctx, model); {
	case errors.Is(err, errNotFound):
		fmt.Fprintln(out, "  ещё не скачана -- скачается при первом ask")
	case err != nil:
		fmt.Fprintln(out, "  Ollama недоступна -- живые данные модели не получены")
	default:
		fmt.Fprintf(out, "  параметров: %s, квантование: %s, контекст: %d токенов\n",
			info.ParameterSize, info.Quantization, info.ContextLength)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, limitations)
}
