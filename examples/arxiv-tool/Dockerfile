# Standalone Dockerfile for arxiv-tool
# This Dockerfile builds the tool independently without workspace dependencies
# Usage (from arxiv-tool directory): docker build -t arxiv-tool:latest .

FROM golang:1.27.0-alpine AS builder

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
    -o arxiv-tool .

RUN ls -lh arxiv-tool

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /app/arxiv-tool .

RUN chown appuser:appgroup /app/arxiv-tool
USER appuser

ENV PORT=8369

EXPOSE ${PORT}

ENTRYPOINT ["./arxiv-tool"]
