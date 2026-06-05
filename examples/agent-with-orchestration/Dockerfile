# Standalone Dockerfile for travel-research-orchestration-example
# This Dockerfile builds the agent independently without workspace dependencies
# Usage (from agent-with-orchestration directory): docker build -t travel-research-agent:latest .

FROM golang:1.26.4-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

# Copy only the agent-with-orchestration directory (standalone build)
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
    -o travel-research-agent .

# Verify the binary was created
RUN ls -lh travel-research-agent

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (no curl - use K8s probes for health checks)
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Create non-root user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Copy binary from builder stage
COPY --from=builder /app/travel-research-agent .

# Change ownership to non-root user
RUN chown appuser:appgroup /app/travel-research-agent && \
    chmod +x /app/travel-research-agent
USER appuser

# Set default PORT if not provided
ENV PORT=8094

# Note: Health checks handled by Kubernetes probes (livenessProbe/readinessProbe)
# See k8-deployment.yaml for probe configuration

# Expose port (default 8094, can be overridden by PORT env var)
EXPOSE ${PORT}

# Run the binary
ENTRYPOINT ["./travel-research-agent"]
