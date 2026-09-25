# Обе стадии на Go: один статический бинарь, в финальном образе -- голый alpine.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/mcp-pipeline .

FROM alpine:3.21
# uid общий с агентом: оба пишут на один том /data
RUN adduser -D -u 10001 app && mkdir -p /data && chown app /data
COPY --from=build /out/mcp-pipeline /usr/local/bin/mcp-pipeline
USER app
ENV PORT=8080 \
    DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["mcp-pipeline"]
