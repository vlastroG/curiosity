# Обе стадии на Go: собирать и запускать тут нечего, кроме одного бинаря.
# Ключей сервер не требует, поэтому и передавать в него нечего.

FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
# статический бинарь без cgo: в финальном образе нет ни go, ни libc-зависимостей
RUN CGO_ENABLED=0 go build -trimpath -o /out/mcp-weather .

FROM alpine:3.21
RUN adduser -D -u 10001 weather
COPY --from=build /out/mcp-weather /usr/local/bin/mcp-weather
USER weather
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["mcp-weather"]
