// Command mcp-probe подключается к MCP-серверу и печатает список его инструментов.
//
// MCP -- это JSON-RPC 2.0 поверх транспорта. Транспорт здесь stdio: клиент сам
// запускает сервер дочерним процессом и говорит с ним через его stdin/stdout,
// без сетевого порта и без ключей.
//
//	mcp-probe                                   сервер
//	    │  запуск процесса + пара труб  ───────────►│
//	    │  initialize (кто я, что умею)  ──────────►│
//	    │◄────────  имя, версия, возможности        │
//	    │  notifications/initialized  ─────────────►│  соединение установлено
//	    │  tools/list  ────────────────────────────►│
//	    │◄──────  [{name, description, inputSchema}]│
//
// Рукопожатие -- это именно обмен initialize/initialized, а не «процесс запустился»:
// пока он не прошёл, спрашивать инструменты нельзя. В SDK за него отвечает Connect.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultServer -- официальный демо-сервер протокола. Ключей не требует
// и отдаёт инструменты разных видов, поэтому список выходит наглядным.
//
// В образе он уже установлен, и там команда короче -- см. MCP_SERVER_CMD.
const defaultServer = "npx -y @modelcontextprotocol/server-everything"

// timeout -- потолок на всё вместе. Без него запуск, ушедший в сеть за пакетом,
// может висеть молча, и непонятно, сломалось что-то или просто качается.
const timeout = 2 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "не получилось:", err)
		os.Exit(1)
	}
}

func run() error {
	command := os.Getenv("MCP_SERVER_CMD")
	if command == "" {
		command = defaultServer
	}

	parts := strings.Fields(command)
	if len(parts) == 0 {
		return fmt.Errorf("пустая команда запуска сервера")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fmt.Printf("запускаю сервер: %s\n\n", command)

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-probe", Version: "0.1.0"}, nil)
	transport := &mcp.CommandTransport{Command: exec.Command(parts[0], parts[1:]...)}

	// Connect делает рукопожатие целиком: initialize, разбор ответа
	// и уведомление initialized
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("соединение не установилось: %w", err)
	}
	defer session.Close()

	info := session.InitializeResult()
	fmt.Printf("соединение установлено: %s %s (протокол %s)\n\n",
		info.ServerInfo.Name, info.ServerInfo.Version, info.ProtocolVersion)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("список инструментов не пришёл: %w", err)
	}

	printTools(tools.Tools)
	return nil
}

func printTools(tools []*mcp.Tool) {
	if len(tools) == 0 {
		fmt.Println("сервер не отдал ни одного инструмента")
		return
	}

	fmt.Printf("инструменты (%d):\n", len(tools))
	for _, tool := range tools {
		fmt.Printf("\n  %s\n", tool.Name)
		if tool.Description != "" {
			fmt.Printf("    %s\n", firstLine(tool.Description))
		}
		if required := requiredParams(tool.InputSchema); len(required) > 0 {
			fmt.Printf("    обязательные: %s\n", strings.Join(required, ", "))
		}
	}
}

// requiredParams достаёт обязательные поля из json-схемы инструмента.
//
// Со стороны клиента схема приходит как обычный разобранный json (map[string]any),
// а не как типизированная структура: сервер вправе прислать любую валидную схему.
// Печатаем именно её содержимое -- так видно, что ответ действительно разобран,
// а не показан строкой.
func requiredParams(schema any) []string {
	object, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := object["required"].([]any)
	if !ok {
		return nil
	}

	names := make([]string, 0, len(list))
	for _, item := range list {
		if name, ok := item.(string); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// firstLine -- описания у инструментов бывают многострочными, а в списке нужна
// одна строка на инструмент.
func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return strings.TrimSpace(text)
}
