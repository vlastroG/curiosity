# Обе стадии на Go: один статический бинарь. SQLite -- modernc.org/sqlite на чистом Go,
# поэтому cgo не нужен и финальный образ остаётся голым alpine.

FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/mcp-crypto .

FROM alpine:3.21
RUN adduser -D -u 10001 crypto && mkdir -p /data && chown crypto /data
COPY --from=build /out/mcp-crypto /usr/local/bin/mcp-crypto
USER crypto
ENV PORT=8080 \
    DB_PATH=/data/crypto.db
EXPOSE 8080
ENTRYPOINT ["mcp-crypto"]
