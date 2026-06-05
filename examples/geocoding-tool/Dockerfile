# Standalone Dockerfile for geocoding-tool
# Builds the tool independently without workspace dependencies
# Usage: docker build -t geocoding-tool:latest .

FROM golang:1.26.4-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

# Copy go module files
COPY go.mod go.sum* ./
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
    -o geocoding-tool .

# Verify the binary was created
RUN ls -lh geocoding-tool

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (no curl - use K8s probes for health checks)
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Create non-root user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Copy binary from builder stage
COPY --from=builder /app/geocoding-tool .

# Change ownership and set executable permissions
RUN chown appuser:appgroup /app/geocoding-tool && \
    chmod +x /app/geocoding-tool
USER appuser

# Set default PORT if not provided
ENV PORT=8095

# Note: Health checks handled by Kubernetes probes (livenessProbe/readinessProbe)
# See k8-deployment.yaml for probe configuration

# Expose port
EXPOSE ${PORT}

# Run the binary
ENTRYPOINT ["./geocoding-tool"]
