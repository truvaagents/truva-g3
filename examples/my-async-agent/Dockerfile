# Standalone Dockerfile for my-async-agent
# Usage (from my-async-agent directory): docker build -t my-async-agent:latest .

FROM golang:1.26.4-alpine AS builder

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
    -o my-async-agent .

RUN ls -lh my-async-agent

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /app/my-async-agent .

RUN chown appuser:appgroup /app/my-async-agent && \
    chmod +x /app/my-async-agent
USER appuser

ENV PORT=8392
EXPOSE ${PORT}

ENTRYPOINT ["./my-async-agent"]
