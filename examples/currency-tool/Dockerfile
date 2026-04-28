FROM golang:1.25.7-alpine AS builder
RUN apk add --no-cache git make gcc musl-dev ca-certificates
WORKDIR /app
COPY go.mod go.sum* ./
COPY *.go ./
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux GONOSUMDB=github.com/truvaagents/truva-g3/*
RUN go mod download && go mod verify
RUN go build -a -installsuffix cgo -ldflags '-w -s -extldflags "-static"' -o currency-tool .

FROM alpine:latest
# No curl - use K8s probes for health checks
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
RUN addgroup -g 1001 -S appgroup && adduser -u 1001 -S appuser -G appgroup
COPY --from=builder /app/currency-tool .
RUN chown appuser:appgroup /app/currency-tool && chmod +x /app/currency-tool
USER appuser
ENV PORT=8097
# Health checks handled by Kubernetes probes (livenessProbe/readinessProbe)
EXPOSE ${PORT}
ENTRYPOINT ["./currency-tool"]
