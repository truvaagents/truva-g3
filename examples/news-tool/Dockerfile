FROM golang:1.27.0-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum* ./
COPY *.go ./
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux GONOSUMDB=github.com/truvaagents/truva-g3/*
RUN go mod download && go build -ldflags '-w -s' -o news-tool .

FROM alpine:latest
# No curl - use K8s probes for health checks
RUN apk --no-cache add ca-certificates
WORKDIR /app
RUN adduser -u 1001 -S appuser
COPY --from=builder /app/news-tool .
USER appuser
ENV PORT=8099
# Health checks handled by Kubernetes probes (livenessProbe/readinessProbe)
EXPOSE ${PORT}
ENTRYPOINT ["./news-tool"]
