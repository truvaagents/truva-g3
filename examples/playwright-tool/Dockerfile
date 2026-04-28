# Standalone Dockerfile for playwright-tool
# This Dockerfile builds the tool independently without workspace dependencies
# Usage (from playwright-tool directory): docker build -t playwright-tool:latest .

FROM golang:1.25.7-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

# Copy only the playwright-tool directory (standalone build)
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
    -o playwright-tool .

# Verify the binary was created
RUN ls -lh playwright-tool

# Runtime stage — Debian for full glibc compatibility + Node.js + Playwright
FROM debian:bookworm-slim

# Install runtime dependencies including Node.js and Chromium deps
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    nodejs \
    npm \
    libnss3 \
    libatk1.0-0 \
    libatk-bridge2.0-0 \
    libcups2 \
    libdrm2 \
    libxkbcommon0 \
    libxcomposite1 \
    libxdamage1 \
    libxrandr2 \
    libgbm1 \
    libpango-1.0-0 \
    libcairo2 \
    libasound2 \
    libxshmfence1 \
    libgtk-3-0 \
    libx11-xcb1 \
    fonts-liberation \
    fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/*

# Install Playwright with bundled Chromium
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/playwright-browsers
RUN npm install -g playwright @playwright/test playwright-extra puppeteer-extra-plugin-stealth \
    puppeteer-extra-plugin-user-preferences puppeteer-extra-plugin-user-data-dir \
    && npx playwright install chromium

# Ensure Node.js can find globally-installed modules
ENV NODE_PATH=/usr/local/lib/node_modules

WORKDIR /app

# Create non-root user for security
RUN groupadd -g 1001 appgroup && \
    useradd -u 1001 -g appgroup -m -s /bin/bash appuser

# Grant the non-root user read+execute access to Playwright browsers
RUN chmod -R o+rX /opt/playwright-browsers

# Copy binary from builder stage
COPY --from=builder /app/playwright-tool .

# Copy explore.js script
COPY scripts/ /app/scripts/

RUN chown -R appuser:appgroup /app && \
    chmod +x /app/playwright-tool

# Create working directories for browser runs
RUN mkdir -p /tmp/playwright-runs && chown appuser:appgroup /tmp/playwright-runs

USER appuser

# Default script directory
ENV PLAYWRIGHT_SCRIPT_DIR=/app/scripts
ENV PORT=8349

EXPOSE ${PORT}
ENTRYPOINT ["./playwright-tool"]
