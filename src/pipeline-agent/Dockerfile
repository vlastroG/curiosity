# CLI-агент. Запрос -- аргументом команды или интерактивно, если аргумента нет.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/pipeline-agent .

FROM alpine:3.21
# uid общий с сервером: оба пишут на один том /data
RUN adduser -D -u 10001 app && mkdir -p /data && chown app /data
COPY --from=build /out/pipeline-agent /usr/local/bin/pipeline-agent
USER app
ENV DATA_DIR=/data
ENTRYPOINT ["pipeline-agent"]
