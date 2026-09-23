# mcp-crypto

MCP-сервер с фоновым планировщиком. Сам, без запросов снаружи, раз в N минут снимает курсы
криптовалют с Binance и пишет их в SQLite. Инструменты отдают агрегаты за любое окно.

Написан под [Crypto Watch](../crypto-watch) (день 18: «планировщик и фоновые задачи»),
но это самостоятельный сервер: подключить его можно к любому MCP-клиенту.

## Чем отличается от обычного MCP-сервера

Обычный инструмент живёт от вызова до вызова. Пока модель его не позвала, ничего не происходит.
Здесь наоборот: сбор идёт по таймеру в фоне, а инструменты читают то, что уже накоплено.
Поэтому на вопрос «что было с ценой за последний час» сервер ответит в любой момент, а не только
через час после первого вопроса.

```
таймер ──► Binance ──► SQLite ◄── crypto_summary / crypto_series   (чтение)
  ▲                                crypto_schedule(interval_minutes) (смена расписания)
  └──────── интервал из SQLite ◄──┘
```

- **Хранение** — SQLite (`modernc.org/sqlite`, чистый Go, без cgo). Таблицы: `runs` (журнал
  сборов, включая неудачные), `prices`, `forecasts`, `settings`.
- **Расписание** — интервал от 1 до 15 минут. Меняется на ходу инструментом `crypto_schedule`,
  хранится в базе и переживает перезапуск. Первый сбор идёт сразу после старта.
- **Агрегат** — `crypto_summary` считает по каждой монете первую и последнюю цену, минимум,
  максимум, среднюю, изменение в процентах, волатильность (стандартное отклонение шаговых
  изменений) и самый резкий скачок. Модели не приходится считать это самой: в арифметике
  она ошибается чаще всего.

## Инструменты

| Инструмент | Что делает |
|---|---|
| `crypto_summary(minutes=60)` | агрегат за окно по всем монетам, лидер роста и падения |
| `crypto_series(minutes=60)` | сырые замеры для графиков, не больше 300 точек на монету |
| `crypto_now()` | внеочередной сбор прямо сейчас |
| `crypto_schedule(interval_minutes?)` | статус планировщика; с аргументом меняет интервал |
| `save_forecast(model, window_minutes, horizon_minutes, body)` | сохранить прогноз агента |
| `list_forecasts(limit=10)` | последние прогнозы |

## Запуск

```bash
docker build -t mcp-crypto .
docker run --rm -p 8766:8080 -v crypto-data:/data mcp-crypto
```

Без контейнера (Go 1.25+):

```bash
PORT=8766 DB_PATH=./crypto.db go run .
```

Переменные окружения: `SYMBOLS` (по умолчанию `BTC,ETH,SOL,TON,DOGE`), `COLLECT_INTERVAL`
(стартовый интервал, `1m`), `RETENTION` (сколько хранить цены, `168h`), `BINANCE_URL`,
`API_TIMEOUT`.

Биржа — Binance (`data-api.binance.vision`, публичные рыночные данные, без ключа). Сначала
был Coinbase, но отсюда он не отвечает.

## Проверка curl'ом

Транспорт — Streamable HTTP без сессий, ответ приходит обычным JSON:

```bash
curl -s -X POST http://localhost:8766/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"crypto_summary","arguments":{"minutes":15}}}'
```

Смена интервала:

```bash
curl -s -X POST http://localhost:8766/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"crypto_schedule","arguments":{"interval_minutes":2}}}'
```

## Тесты

```bash
go test ./...
```

Сеть в тестах не нужна: биржу заменяет `httptest`, SQLite настоящий, во временном каталоге.
Инструменты проверяются настоящим MCP-клиентом через транспорт в памяти.
