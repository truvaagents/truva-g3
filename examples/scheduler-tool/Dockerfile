# Standalone Dockerfile for scheduler-tool
# Builds the tool independently from a published truvag3 release — no
# workspace dependencies. The replace directives in go.mod must be removed
# (or the framework modules must be available on GitHub at the pinned
# version) for this build to succeed.
#
# Usage (from this directory):
#   docker build -t scheduler-tool:latest .

FROM golang:1.26.3-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

# Copy only the scheduler-tool directory (standalone build)
COPY go.mod go.sum ./
COPY *.go ./

# Configure Go to fetch from GitHub and ignore workspace
ENV GOWORK=off
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GONOSUMDB=github.com/truvaagents/truva-g3/*

# Download dependencies
RUN go mod download && go mod verify

# Build the binary with optimizations
RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o scheduler-tool .

# Verify the binary was created
RUN ls -lh scheduler-tool

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (no curl - use K8s probes for health checks)
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Create non-root user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Copy binary from builder stage
COPY --from=builder /app/scheduler-tool .

# Change ownership to non-root user
RUN chown appuser:appgroup /app/scheduler-tool
USER appuser

# Set default PORT if not provided
ENV PORT=8379

# Note: Health checks handled by Kubernetes probes (livenessProbe/readinessProbe)
# See k8-deployment.yaml for probe configuration

# Expose port (default 8379, can be overridden by PORT env var)
EXPOSE ${PORT}

# Run the binary
ENTRYPOINT ["./scheduler-tool"]
