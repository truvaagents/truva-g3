# Standalone Dockerfile for system-utilities-tool
# For publishing to a container registry. Copies the tool directory only.
# Usage: docker build -t system-utilities-tool:latest .

FROM golang:1.25.7-alpine AS builder

RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
COPY *.go ./

# Configure Go to fetch from GitHub and ignore workspace
ENV GOWORK=off
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GONOSUMDB=github.com/truvaagents/truva-g3/*

RUN go mod download && go mod verify

RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o system-utilities-tool .

# Runtime stage — python:3.12-slim (python3 + pip3 already bundled, ~125 MB base)
FROM python:3.12-slim

# Install shell/text/network utilities for the execute_command capability.
# Browser dependencies (nodejs, chromium libs, fonts) were removed when the
# stealth_browser and browser_test capabilities moved to playwright-tool.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    bash \
    coreutils \
    grep \
    sed \
    gawk \
    jq \
    bc \
    curl \
    openssl \
    git \
    dnsutils \
    iputils-ping \
    traceroute \
    netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Create non-root user for security
RUN groupadd -g 1001 appgroup && \
    useradd -u 1001 -g appgroup -m -s /bin/bash appuser

COPY --from=builder /app/system-utilities-tool .
RUN chown appuser:appgroup /app/system-utilities-tool && \
    chmod +x /app/system-utilities-tool

# Create a working directory for command execution
RUN mkdir -p /app/workspace && chown appuser:appgroup /app/workspace

# Pre-install commonly requested Python packages and enable --user installs
RUN pip3 install --no-cache-dir numpy requests && \
    mkdir -p /home/appuser/.local && chown -R appuser:appgroup /home/appuser/.local

USER appuser

ENV PORT=8100
EXPOSE ${PORT}
ENTRYPOINT ["./system-utilities-tool"]
