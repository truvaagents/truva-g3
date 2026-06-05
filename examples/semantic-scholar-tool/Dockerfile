# Standalone Dockerfile for semantic-scholar-tool
# Usage (from semantic-scholar-tool directory): docker build -t semantic-scholar-tool:latest .

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

RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o semantic-scholar-tool .

RUN ls -lh semantic-scholar-tool

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /app/semantic-scholar-tool .

RUN chown appuser:appgroup /app/semantic-scholar-tool
USER appuser

ENV PORT=8370

EXPOSE ${PORT}

ENTRYPOINT ["./semantic-scholar-tool"]
