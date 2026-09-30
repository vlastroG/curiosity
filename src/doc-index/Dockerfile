# Один образ: Go-бэкенд раздаёт и API, и собранный интерфейс, и умеет CLI
# (docker compose run --rm doc-index index). Видеокарта этому образу не нужна --
# эмбеддинги считает контейнер Ollama.

FROM node:22-alpine AS web
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM golang:1.25-alpine AS api
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 go build -trimpath -o /out/docindex ./cmd/docindex

FROM alpine:3.21
RUN adduser -D -u 10001 docindex && mkdir -p /data && chown docindex /data
WORKDIR /app
COPY --from=api /out/docindex /app/docindex
COPY --from=web /app/dist /app/web
COPY eval/ /app/eval/
USER docindex
ENV STATIC_DIR=/app/web \
    DATA_DIR=/data \
    EVAL_FILE=/app/eval/questions.json \
    RAG_EVAL_FILE=/app/eval/rag.json \
    PORT=8080
EXPOSE 8080
ENTRYPOINT ["/app/docindex"]
CMD ["serve"]
