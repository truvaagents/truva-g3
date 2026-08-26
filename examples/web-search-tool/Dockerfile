# Production Dockerfile for web-search-tool
# Uses published modules from GitHub
FROM golang:1.27.0-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags '-w -s' \
    -o web-search-tool .

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /app/web-search-tool .

RUN chown appuser:appgroup /app/web-search-tool && \
    chmod +x /app/web-search-tool
USER appuser

ENV PORT=8341
EXPOSE ${PORT}

ENTRYPOINT ["./web-search-tool"]
