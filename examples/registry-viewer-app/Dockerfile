# Dockerfile for registry-viewer-app
# This is a standalone app - no truvag3 framework dependencies required
# Usage: docker build -t registry-viewer:latest .

FROM golang:1.25.7-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Configure Go build
ENV CGO_ENABLED=0
ENV GOOS=linux

# Build the binary with optimizations
ARG TARGETARCH=arm64
RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o registry-viewer .

# Verify the binary was created
RUN ls -lh registry-viewer

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Create non-root user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Copy binary from builder stage
COPY --from=builder /app/registry-viewer .

# Change ownership to non-root user
RUN chown appuser:appgroup /app/registry-viewer && \
    chmod +x /app/registry-viewer

USER appuser

# Set default PORT
ENV PORT=8100

# Expose port
EXPOSE ${PORT}

# Defaults: mock mode if no REDIS_URL is provided, real mode otherwise.
# The flag default is -mock=true so a bare container start still serves the UI
# without a Redis dependency; main.go auto-disables mock when REDIS_URL is set
# and the operator hasn't passed -mock or USE_MOCK explicitly. Pass -mock=true
# (or USE_MOCK=true) to force mock mode even when REDIS_URL is configured.
ENTRYPOINT ["./registry-viewer"]
