# Standalone Dockerfile for places-tool
FROM golang:1.26.3-alpine AS builder
RUN apk add --no-cache git make gcc musl-dev ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
COPY *.go ./
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux GONOSUMDB=github.com/truvaagents/truva-g3/*
RUN go mod download && go mod verify
RUN go build -a -installsuffix cgo -ldflags '-w -s -extldflags "-static"' -o places-tool .
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
RUN addgroup -g 1001 -S appgroup && adduser -u 1001 -S appuser -G appgroup
COPY --from=builder /app/places-tool .
RUN chown appuser:appgroup /app/places-tool
USER appuser
ENV PORT=8344
EXPOSE ${PORT}
ENTRYPOINT ["./places-tool"]
