# Standalone Dockerfile for travel-chat-agent
# This Dockerfile builds the agent independently without workspace dependencies
# Usage (from travel-chat-agent directory): docker build -t travel-chat-agent:latest .

FROM golang:1.26.4-alpine AS builder

# Install build dependencies (CGO disabled, so no C toolchain needed)
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy only the travel-chat-agent directory (standalone build)
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
    -o travel-chat-agent .

# Runtime stage
FROM alpine:3.21

# Install runtime deps and create non-root user in one layer
RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

WORKDIR /app

# Copy binary from builder stage (ownership set at copy time)
COPY --from=builder --chown=appuser:appgroup /app/travel-chat-agent .

USER appuser

# Set default PORT if not provided
ENV PORT=8095

# Note: Health checks handled by Kubernetes probes (livenessProbe/readinessProbe)
# See k8-deployment.yaml for probe configuration

# Expose port (default 8095)
EXPOSE ${PORT}

# Run the binary
ENTRYPOINT ["./travel-chat-agent"]
