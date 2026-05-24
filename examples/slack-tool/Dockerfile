# Standalone Dockerfile for slack-tool
# Usage (from slack-tool directory): docker build -t slack-tool:latest .

FROM golang:1.26.3-alpine AS builder

RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
COPY *.go ./

ENV GOWORK=off
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GONOSUMDB=github.com/truvaagents/truva-g3/*

RUN go mod download && go mod verify

RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o slack-tool .

RUN ls -lh slack-tool

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /app/slack-tool .

RUN chown appuser:appgroup /app/slack-tool
USER appuser

ENV PORT=8363

EXPOSE ${PORT}

ENTRYPOINT ["./slack-tool"]
