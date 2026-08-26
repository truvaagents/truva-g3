# Standalone Dockerfile for openclaw-tool
# Use this when building OUTSIDE the truvag3 workspace (e.g., from a published version of core/telemetry).
# For local development, prefer Dockerfile.workspace which uses local module sources.

FROM golang:1.27.0-alpine AS builder
RUN apk add --no-cache git make gcc musl-dev ca-certificates
WORKDIR /app
COPY go.mod go.sum* ./
COPY *.go ./
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux GONOSUMDB=github.com/truvaagents/truva-g3/*
RUN go mod download && go mod verify
RUN go build -a -installsuffix cgo -ldflags '-w -s -extldflags "-static"' -o openclaw-tool .

FROM alpine:3.23
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
RUN addgroup -g 1001 -S appgroup && adduser -u 1001 -S appuser -G appgroup
COPY --from=builder /app/openclaw-tool .
RUN chown appuser:appgroup /app/openclaw-tool && chmod +x /app/openclaw-tool
USER appuser
ENV PORT=8393
EXPOSE ${PORT}
ENTRYPOINT ["./openclaw-tool"]
