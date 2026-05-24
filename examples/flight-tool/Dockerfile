# Standalone Dockerfile for flight-tool
# This Dockerfile builds the tool independently without workspace dependencies
# Usage (from flight-tool directory): docker build -t flight-tool:latest .

FROM golang:1.26.3-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

# Copy only the flight-tool directory (standalone build)
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
    -o flight-tool .

# Verify the binary was created
RUN ls -lh flight-tool

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (no curl - use K8s probes for health checks)
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Create non-root user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Copy binary from builder stage
COPY --from=builder /app/flight-tool .

# Change ownership to non-root user
RUN chown appuser:appgroup /app/flight-tool
USER appuser

# Set default PORT if not provided
ENV PORT=8342

# Expose port
EXPOSE ${PORT}

# Run the binary
ENTRYPOINT ["./flight-tool"]
