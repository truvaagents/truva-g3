# Standalone Dockerfile for weather-tool-v2
# Builds the tool independently without workspace dependencies
# Usage: docker build -t weather-tool-v2:latest .

FROM golang:1.27.0-alpine AS builder

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
    -o weather-tool-v2 .

# Verify the binary was created
RUN ls -lh weather-tool-v2

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (no curl - use K8s probes for health checks)
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Create non-root user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Copy binary from builder stage
COPY --from=builder /app/weather-tool-v2 .

# Change ownership and set executable permissions
RUN chown appuser:appgroup /app/weather-tool-v2 && \
    chmod +x /app/weather-tool-v2
USER appuser

# Set default PORT if not provided
ENV PORT=8096

# Note: Health checks handled by Kubernetes probes (livenessProbe/readinessProbe)
# See k8-deployment.yaml for probe configuration

# Expose port
EXPOSE ${PORT}

# Run the binary
ENTRYPOINT ["./weather-tool-v2"]
