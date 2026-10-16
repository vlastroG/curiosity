# Один статический бинарь: SQLite -- modernc.org/sqlite на чистом Go, cgo не нужен.

FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/mcp-tickets .

FROM alpine:3.21
RUN adduser -D -u 10001 tickets && mkdir -p /data && chown tickets /data
COPY --from=build /out/mcp-tickets /usr/local/bin/mcp-tickets
USER tickets
ENV PORT=8080 \
    DB_PATH=/data/tickets.db
EXPOSE 8080
ENTRYPOINT ["mcp-tickets"]
