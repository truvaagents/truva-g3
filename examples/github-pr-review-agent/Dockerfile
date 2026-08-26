# Standalone Dockerfile for github-pr-review-agent
# Builds the agent independently without workspace dependencies.
# Usage (from github-pr-review-agent directory):
#   docker build -t github-pr-review-agent:latest .

FROM golang:1.27.0-alpine AS builder

# Build dependencies (CGO disabled, so no C toolchain needed)
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy only the agent directory (standalone build)
COPY go.mod go.sum ./
COPY *.go ./

# Configure Go to fetch from GitHub and ignore workspace
ENV GOWORK=off
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GONOSUMDB=github.com/truvaagents/truva-g3/*

# Download dependencies from GitHub
RUN go mod download && go mod verify

# Build the binary
ARG TARGETARCH=arm64
RUN go build -trimpath -buildvcs=false \
    -ldflags '-w -s' \
    -o github-pr-review-agent .

# Runtime stage
FROM alpine:3.21

# Install runtime deps and create non-root user in one layer
RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

WORKDIR /app

# Copy binary from builder stage (ownership set at copy time)
COPY --from=builder --chown=appuser:appgroup /app/github-pr-review-agent .

USER appuser

# Set default PORT if not provided
ENV PORT=8382

# Note: Health checks handled by Kubernetes probes (livenessProbe/readinessProbe).
# See k8-deployment.yaml for probe configuration.

# Expose port (default 8382)
EXPOSE ${PORT}

# Run the binary
ENTRYPOINT ["./github-pr-review-agent"]
