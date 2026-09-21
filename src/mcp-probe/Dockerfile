# Две стадии, потому что нужны два разных рантайма: Go — чтобы собрать клиент,
# Node — чтобы запустить сам MCP-сервер. В образ попадает только второй.

FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
# статический бинарь: в итоговом образе Go нет
RUN CGO_ENABLED=0 go build -o /out/mcp-probe .

FROM node:22-alpine

# сервер кладём в образ заранее -- тогда запуск не ходит в сеть и не ждёт npx.
# Пакет весит около 250 КБ, так что образ от этого не толстеет
RUN npm install -g @modelcontextprotocol/server-everything@2026.8.31

COPY --from=build /out/mcp-probe /usr/local/bin/mcp-probe

# в образе сервер уже установлен, и команда короче, чем npx на машине
ENV MCP_SERVER_CMD=mcp-server-everything

ENTRYPOINT ["mcp-probe"]
