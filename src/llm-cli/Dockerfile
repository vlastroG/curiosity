FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/llmcli .

FROM alpine:3.21
RUN adduser -D -u 10001 llmcli
COPY --from=build /out/llmcli /app/llmcli
USER llmcli
ENTRYPOINT ["/app/llmcli"]
CMD ["help"]
