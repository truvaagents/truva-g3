# Standalone Dockerfile for event-driven-agent
# Usage (from event-driven-agent directory): docker build -t event-driven-agent:latest .

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

ARG TARGETARCH=arm64
RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o event-driven-agent .

RUN ls -lh event-driven-agent

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /app/event-driven-agent .

RUN chown appuser:appgroup /app/event-driven-agent && \
    chmod +x /app/event-driven-agent
USER appuser

ENV PORT=8372
EXPOSE ${PORT}

ENTRYPOINT ["./event-driven-agent"]
