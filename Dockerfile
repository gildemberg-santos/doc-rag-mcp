FROM golang:1.25-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/mcp-server ./cmd/server && \
    CGO_ENABLED=0 go build -o /bin/mcp-indexer ./cmd/indexer

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/mcp-server /usr/local/bin/mcp-server
COPY --from=builder /bin/mcp-indexer /usr/local/bin/mcp-indexer
EXPOSE 8080
ENTRYPOINT ["mcp-server"]
CMD ["--transport", "http"]
