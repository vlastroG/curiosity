# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

A study collection of proof-of-concept AI tools. Each directory under `src/` is a self-contained project with its own Go module (no `go.work`), its own README and, for web apps, its own Docker image and compose file. Projects share nothing at the code level: LLM clients, model catalogs and MCP helpers are deliberately duplicated per project. Fix something in one project and leave the others alone unless asked.

Docs, UI text, log messages and code comments are written in Russian. Keep that.

## Projects

- **Web apps** (Go backend + React 18/Vite frontend): `agent-sprout`, `crypto-watch`, `trip-agent`, `doc-index`, `support-desk`.
- **MCP servers** (Go, `github.com/modelcontextprotocol/go-sdk`, streamable HTTP on `/mcp`): `mcp-weather`, `mcp-money`, `mcp-places`, `mcp-trip`, `mcp-crypto`, `mcp-pipeline`, `mcp-tickets`.
- **CLI agents:**
  - `pipeline-agent`, a CLI agent driving `mcp-pipeline`.
  - `llm-cli`, a local model through Ollama. It is a CLI plus an OpenAI-compatible `serve` mode.

Which app uses which MCP server:
- `agent-sprout` → `mcp-weather`
- `trip-agent` → places, weather, money, trip
- `crypto-watch` → `mcp-crypto`
- `pipeline-agent` → `mcp-pipeline`
- `support-desk` → `mcp-tickets` (plus Ollama bge-m3 and a TypeSafe Jev guard)

Each app's compose file builds the MCP servers it needs from sibling directories.

## Shared layout of the web apps

- **Image:** one image per app. A multi-stage Dockerfile builds the frontend with `npm ci && npm run build`, then a static `CGO_ENABLED=0` Go binary that serves both `/api` and the built UI (`STATIC_DIR`). There's no nginx and no CORS.
- **Backend:** `backend/cmd/<name>` for entrypoints, plus `backend/internal/{llm,agent,httpapi,store,…}`.
- **SQLite:** apps that use it use `modernc.org/sqlite` (pure Go).
- **Frontend dev server:** proxies `/api` to the backend on `:8080`.
- **Config:** compose files read the repo-root `.env` (`env_file: ../../.env`). `.env.example` at the root documents every variable (API keys, `DEFAULT_MODEL`, `PIPELINE_MODEL`, `TRIP_MODEL`, `RAG_MODEL`, doc-index settings). `.env` is git-ignored.
- **LLM providers:** DeepSeek and OpenRouter (free models for development). doc-index can also use a local model via llmcli/Ollama (`RAG_MODEL=local`, compose profile `local`).

## doc-index (largest project)

"Twain Expert" is a RAG chat over Mark Twain's books.

- **RAG pipeline:** query planning (HyDE + task memory) → vector search → reranker → answer. Long histories are compressed into a summary.
- **Code:** packages live in `backend/internal/` (`rag`, `retrieve`, `rerank`, `embed`, `index`, `chat`, …).
- **Modes:**
  - `RAG=off` turns it into a plain multi-user chat with one LLM call per reply (`rag.Plain`).
  - `DOCINDEX_USERS=login:pass,…` enables HTTP Basic auth with per-user chats.
- **LAN access:** `docker-compose.lan.yml` publishes the port beyond localhost; open the Windows firewall with `lan-firewall.ps1`.
- **CLI subcommands** (`index`, `chat`, `scenario`, `search`, `experiment`, `stats`) run through `docker compose run --rm doc-index <cmd>`.
- **Data:** stored in `data/`, which is git-ignored.

## Commands

Run these from the project directory, for example `src/doc-index`.

```bash
docker compose up -d --build                  # run an app (doc-index local model: --profile local)
cd backend && go vet ./... && go test ./...   # Go tests (MCP servers / CLI agents: from the project root)
go test ./internal/rag -run TestPlainReply    # single test
cd frontend && npm install && npm run dev     # UI dev server
cd frontend && npm test && npm run build      # frontend tests (node --test "src/**/*.test.js") and build
```

Each project's README lists the exact ports and env vars for running it without Docker. MCP servers run standalone with `PORT=<port> go run .`.

## Conventions

- **Environment:** Windows with Git Bash. Don't redirect to `/dev/null`.
- **Checkouts:** files are checked out with CRLF, so `gofmt -l` flags almost everything; that noise is pre-existing.
- **Comments and READMEs:** keep comments short and skip obvious ones, and keep READMEs short.
