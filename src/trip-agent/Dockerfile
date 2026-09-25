# Один образ: Go-бэкенд раздаёт API, SSE, собранный интерфейс и опубликованные планы.

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
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server

FROM alpine:3.21
RUN adduser -D -u 10001 app && mkdir -p /data && chown app /data
WORKDIR /app
COPY --from=api /out/server /app/server
COPY --from=web /app/dist /app/web
USER app
ENV STATIC_DIR=/app/web \
    DATA_DIR=/data \
    PORT=8080
EXPOSE 8080
CMD ["/app/server"]
