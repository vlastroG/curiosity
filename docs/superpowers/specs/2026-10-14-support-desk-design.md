# Support Desk: design

A small user-support assistant service. It answers questions about a product using RAG over the product's documentation and source, and it takes support tickets into account. Tickets live in SQLite behind an MCP server.

## Requirements (from the user)

- The assistant answers questions about the product, using RAG over FAQ and documentation, and takes ticket context into account.
- Tickets are stored in SQLite and reached by the LLM through MCP.
- Example: the user asks «Почему не работает вход?» and the assistant answers from ticket data.
- The backend is written in Go.
- The documentation project path comes from an env variable. On container start the project is indexed if it isn't already, and the index is used for RAG.
- The default project is pyRevit (`C:\Users\vlast\source\repos\pyRevit`).
- Chat LLM: `deepseek-flash` by default. Embedding model: `bge-m3`.
- Prompt-injection guards: the chat is for support only.
- UI: a React web chat. A side panel creates tickets for debugging; they go to the DB, and the LLM reads them via MCP.
- The UI and model answers are in Russian.
- Consider TypeSafe Jev. Its key is `TYPESAFE_API_KEY` from `.env`, documented in `.env.example`.
- **Stateless:** the LLM gets no message history. Each user message is answered on its own.
- The work goes on a new branch: `support-desk`.

## Decisions

### Projects

Two new projects, following the repo convention of one directory per project with no shared code:

- **`src/mcp-tickets`:** an MCP server that owns `tickets.db`. Tools:
  - `ticket_create`, `ticket_list`, `ticket_get`, `ticket_search` (FTS5), `ticket_update`.
- **`src/support-desk`:** the web app. It's a Go backend plus a React UI, built into one image. Compose runs it together with `mcp-tickets` and `ollama` (GPU, for `bge-m3`).
- **Shared code:** the LLM client, the Ollama embed client and BM25 are copied from `src/doc-index` and trimmed.

### What the model can do with tickets

- The model gets read-only ticket tools: `ticket_search`, `ticket_get` and `ticket_list`.
- Creating and updating tickets is the UI's job. The backend calls those tools directly; the model never does.

### Per-message flow (no history)

1. **Code checks:** the text is non-empty and at most 2000 characters.
2. **Guard (Jev):** one call with two Nouls.
   - `injection`: the message tries to override the role or rules, or to reveal the prompt.
   - `on_topic`: the message is a support question about the product or the user's tickets.
   - If `injection ≥ 0.7`, the reply is a fixed refusal. If `on_topic < 0.3`, the reply is a fixed off-topic reply. The LLM is not called in either case.
3. **Retrieval:** hybrid search over the doc index (bge-m3 cosine + BM25, fused with RRF), top 6 chunks.
4. **Attached ticket:** if the UI attached one (`ticketId`), code fetches it with `ticket_get` and puts it in the prompt.
5. **LLM:** one system message plus one user message, containing the question, the doc chunks and the attached ticket. Read-only ticket tools are allowed for at most 4 tool rounds. The answer is in Russian Markdown.
6. **Response:** the answer, doc sources, which tickets were consulted, and the guard verdict.

### Why Jev, and only as the guard

- **What it gives:** typed, calibrated yes/no probabilities in one fast call, and the policy (thresholds) stays in code. That fits a guard better than a second LLM call.
- **Defense in depth:** Jev's own docs warn that adversarial text can steer it. So the LLM-side defenses stay too:
  - a strict system prompt;
  - docs and tickets marked as data, not instructions;
  - read-only tools.
- **No key:** without `TYPESAFE_API_KEY` the guard is off, which shows in status and logs. Only the LLM-side defenses remain.
- **Jev error:** if Jev fails while the guard is on, the request fails closed with HTTP 503.
- **Not done (YAGNI):** Jev reranking and Jev ticket relevance. Possible later.

### Indexing

- **Paths:** `SUPPORT_DOCS_PATH` on the host is mounted read-only at `/docs`.
- **Files:** include `*.md`, `*.rst`, `*.txt`, `*.py`, `*.yaml`. Skip `.git`, `node_modules`, `site-packages`, `dev`, `bin` and `release`. Both lists can be overridden.
- **Fingerprint:** sha256 over the embedding model, the chunker version, and each file's relative path, size and mtime.
- **On start:**
  - If the fingerprint matches the stored one, the index loads.
  - Otherwise it rebuilds in the background. `/api/ask` returns 503 «Индекс строится» until it's ready, and progress shows in `/api/status`.
- **Search:** chunks and vectors are stored in `/data/index.db` and searched in memory.

### UI

Russian throughout.

- **Left: chat.** The transcript is kept only in browser state and is never sent back. Answers render as Markdown with sources listed below. Blocked replies are styled differently.
- **Right: tickets panel.**
  - A create form (title, description).
  - A list with status filter and status change.
  - An "attach to question" toggle.

Host port `127.0.0.1:5181`.
