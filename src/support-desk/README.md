# Support Desk

Ассистент поддержки пользователей продукта (по умолчанию — pyRevit). Отвечает
по-русски по документации и исходникам продукта и по тикетам поддержки; тикеты
лежат в SQLite за MCP-сервером [mcp-tickets](../mcp-tickets).

## Как отвечает

Каждый вопрос — отдельно, истории переписки модель не видит.

1. **Защита.** Jev (TypeSafe) одним запросом оценивает две вероятности: попытка
   prompt injection и вопрос по теме поддержки. Отказ — готовым текстом, без вызова модели.
2. **Поиск.** Шесть фрагментов документации: векторы bge-m3 и BM25, слитые RRF.
3. **Тикет.** Прикреплённый в интерфейсе тикет код добавляет в запрос сам.
4. **Модель** (`deepseek-flash`) отвечает; похожие тикеты ищет инструментами
   MCP — только чтение, до 4 раундов.

## Запуск

```bash
docker compose up -d --build
```

Интерфейс — http://localhost:5181. При первом запуске скачивается bge-m3 и строится
индекс (несколько минут, ход виден в шапке). Дальше индекс перестраивается, только
если поменялись файлы документации.

Остановить: `docker compose down`.

## Настройки

В корневом `.env`:

| Переменная | По умолчанию | |
|---|---|---|
| `SUPPORT_DOCS_PATH` | `../../../pyRevit` | проект с документацией на хосте |
| `SUPPORT_PRODUCT` | `pyRevit` | название в промпте и интерфейсе |
| `SUPPORT_MODEL` | `deepseek-flash` | модель ответов |
| `SUPPORT_EMBED_MODEL` | `bge-m3` | модель эмбеддингов |
| `SUPPORT_DOCS_INCLUDE` | `.md,.rst,.txt,.py,.yaml` | какие файлы индексировать |
| `SUPPORT_DOCS_EXCLUDE` | `.git,node_modules,site-packages,dev,bin,release` | какие каталоги пропускать |
| `TYPESAFE_API_KEY` | — | ключ Jev; без него защита выключена |
| `SUPPORT_GUARD_INJECTION` / `SUPPORT_GUARD_TOPIC` | `0.7` / `0.3` | пороги защиты |

## Разработка без Docker

```bash
cd ../mcp-tickets && PORT=8785 go run .
cd backend && DATA_DIR=../data DOCS_PATH=../../../../pyRevit OLLAMA_URL=http://localhost:11434 TICKETS_URL=http://localhost:8785/mcp go run ./cmd/server
cd frontend && npm install && npm run dev   # http://localhost:5182
```

## Тесты

```bash
cd backend && go vet ./... && go test ./...
cd frontend && npm test && npm run build
```

Живая проверка защиты на русских сообщениях — `go test ./internal/guard -run TestLiveRussian -v`
с `TYPESAFE_API_KEY` в окружении.
