# Один образ на всё приложение: Go-бэкенд раздаёт и API, и собранный интерфейс.
# Отдельный nginx не нужен, CORS не нужен, порт один.

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
# статический бинарь без cgo: в финальном образе нет ни go, ни libc-зависимостей
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server

FROM alpine:3.21
RUN adduser -D -u 10001 sprout && mkdir -p /data && chown sprout /data
WORKDIR /app
COPY --from=api /out/server /app/server
COPY --from=web /app/dist /app/web
USER sprout
ENV STATIC_DIR=/app/web \
    DATA_FILE=/data/chats.json \
    PORT=8080
EXPOSE 8080
CMD ["/app/server"]
