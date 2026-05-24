# Standalone Dockerfile for grocery-tool
# This tool provides grocery store capabilities with error injection for resilience testing
# Usage (from grocery-tool directory): docker build -t grocery-tool:latest .

FROM golang:1.26.3-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

# Copy only the grocery-tool directory (standalone build)
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
RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o grocery-tool .

# Verify the binary was created
RUN ls -lh grocery-tool

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata curl

WORKDIR /app

# Create non-root user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Copy binary from builder stage
COPY --from=builder /app/grocery-tool .

# Change ownership and set executable permissions
RUN chown appuser:appgroup /app/grocery-tool && \
    chmod +x /app/grocery-tool
USER appuser

# Set default PORT if not provided
ENV PORT=8083

# Health check - uses PORT environment variable
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:${PORT}/health || exit 1

# Expose port (default 8083, can be overridden by PORT env var)
EXPOSE ${PORT}

# Run the binary
ENTRYPOINT ["./grocery-tool"]
