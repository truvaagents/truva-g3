# Standalone Dockerfile for research-agent-resilience-example
# This Dockerfile builds the agent independently without workspace dependencies
# Usage (from agent-with-resilience directory): docker build -t research-agent-resilience:latest .

FROM golang:1.25.7-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

# Copy only the agent-with-resilience directory (standalone build)
COPY go.mod go.sum ./
COPY *.go ./

# Configure Go to fetch from GitHub and ignore workspace
ENV GOWORK=off
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GONOSUMDB=github.com/truvaagents/truva-g3/*

# Download dependencies from GitHub
RUN go mod download && go mod verify

# Build the binary with optimizations
ARG TARGETARCH=arm64
RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o research-agent-resilience .

# Verify the binary was created
RUN ls -lh research-agent-resilience

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (no curl - use K8s probes for health checks)
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Create non-root user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Copy binary from builder stage
COPY --from=builder /app/research-agent-resilience .

# Change ownership to non-root user
RUN chown appuser:appgroup /app/research-agent-resilience && \
    chmod +x /app/research-agent-resilience
USER appuser

# Set default PORT if not provided
ENV PORT=8093

# Note: Health checks handled by Kubernetes probes (livenessProbe/readinessProbe)
# See k8-deployment.yaml for probe configuration

# Expose port (default 8093, can be overridden by PORT env var)
EXPOSE ${PORT}

# Run the binary
ENTRYPOINT ["./research-agent-resilience"]
