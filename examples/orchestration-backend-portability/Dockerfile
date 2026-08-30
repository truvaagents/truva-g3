# Standalone image. Framework modules are resolved from the versions in go.mod.
FROM golang:1.27.0-alpine AS builder

RUN apk add --no-cache ca-certificates git
WORKDIR /app

COPY go.mod go.sum ./
# Local replace directives are for monorepo development; standalone builds use
# the released module versions declared above them.
RUN go mod edit -dropreplace=github.com/truvaagents/truva-g3/core \
    -dropreplace=github.com/truvaagents/truva-g3/orchestration \
    -dropreplace=github.com/truvaagents/truva-g3/telemetry && \
    go mod download && go mod verify

COPY *.go ./
COPY internal/ ./internal/

ENV GOWORK=off CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/orchestration-backend-portability .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

WORKDIR /app
COPY --from=builder /out/orchestration-backend-portability ./orchestration-backend-portability
RUN chown appuser:appgroup /app/orchestration-backend-portability && \
    chmod 0555 /app/orchestration-backend-portability

USER appuser
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/app/orchestration-backend-portability"]
