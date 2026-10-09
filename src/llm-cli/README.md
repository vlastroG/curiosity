# llm-cli

CLI на Go для локальной LLM. Модель работает в Ollama, оба компонента -- в Docker.

**Модель:** `qwen3.5:9b-q4_K_M` -- бесплатная, квантованная (Q4_K_M, ~6.6 ГБ), целиком помещается в 8 ГБ видеопамяти (RTX 3060 Ti). Другая модель Ollama -- переменная `LLMCLI_MODEL`.

## Запуск

```bash
docker compose up -d ollama
docker compose run --rm llmcli ask "Что такое горутина?"
docker compose run --rm llmcli help
```

Первый `ask` скачивает модель. `help` показывает все флаги (`--temperature`, `--max-tokens`, `--seed`, ...), сведения о модели и её ограничения.

`llmcli serve` -- HTTP-сервер `/v1/chat/completions` в формате OpenAI; через него модель использует doc-index (`RAG_MODEL=local`).

Чтобы передать текст через конвейер, нужен флаг `-T`: `cat main.go | docker compose run --rm -T llmcli ask "Найди ошибки"`.
