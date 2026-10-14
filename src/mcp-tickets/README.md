# mcp-tickets

MCP-сервер тикетов поддержки: SQLite в одном файле, полнотекстовый поиск FTS5.
Им пользуется [support-desk](../support-desk): модель ассистента читает тикеты,
интерфейс их создаёт и меняет.

## Инструменты

| Инструмент | Аргументы | Что делает |
|---|---|---|
| `ticket_create` | `title`, `description` | новый тикет со статусом `open` |
| `ticket_get` | `id` | тикет по номеру |
| `ticket_list` | `status?`, `limit?` (20, до 100) | последние тикеты, свежие первыми |
| `ticket_search` | `query`, `limit?` (5, до 20) | поиск по заголовку, описанию и решению; слова по префиксу: «вход» найдёт «входа» |
| `ticket_update` | `id`, `status?`, `resolution?` | сменить статус (`open`, `in_progress`, `resolved`), записать решение |

## Запуск

```bash
PORT=8785 DB_PATH=./tickets.db go run .
```

Проверка:

```bash
curl -s localhost:8785/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ticket_search","arguments":{"query":"вход"}}}'
```

## Тесты

```bash
go test ./...
```
