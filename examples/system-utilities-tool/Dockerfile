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

# Runtime stage — Debian for full glibc compatibility and standard Linux tooling
FROM debian:bookworm-slim

# Install runtime dependencies including useful command-line tools
# for the execute_command capability
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    # --- Shell & scripting ---
    bash \
    python3 \
    python3-pip \
    python3-venv \
    # --- Text processing ---
    grep \
    sed \
    gawk \
    jq \
    less \
    file \
    # --- Core utilities ---
    coreutils \
    tar \
    gzip \
    unzip \
    bc \
    # --- Networking ---
    curl \
    wget \
    dnsutils \
    iputils-ping \
    traceroute \
    iproute2 \
    netcat-openbsd \
    tcpdump \
    nmap \
    openssl \
    openssh-client \
    net-tools \
    # --- Process & system inspection ---
    procps \
    htop \
    strace \
    lsof \
    sysstat \
    # --- Version control ---
    git \
    # --- Node.js / Playwright prerequisites ---
    nodejs \
    npm \
    # Chromium shared-library dependencies (Playwright downloads its own binary)
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

# Install Playwright with bundled Chromium, plus stealth plugin.
# Store browsers in a shared path so the non-root runtime user can access them.
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/playwright-browsers
RUN npm install -g playwright playwright-extra puppeteer-extra-plugin-stealth \
    puppeteer-extra-plugin-user-preferences puppeteer-extra-plugin-user-data-dir \
    && npx playwright install chromium

# Ensure Node.js can find globally-installed modules when invoked via `node -e`
ENV NODE_PATH=/usr/local/lib/node_modules

WORKDIR /app

# Create non-root user for security
RUN groupadd -g 1001 appgroup && \
    useradd -u 1001 -g appgroup -m -s /bin/bash appuser

# Grant the non-root user read+execute access to Playwright browsers
RUN chmod -R o+rX /opt/playwright-browsers

COPY --from=builder /app/system-utilities-tool .
RUN chown appuser:appgroup /app/system-utilities-tool && \
    chmod +x /app/system-utilities-tool

# Create a working directory for command execution
RUN mkdir -p /app/workspace && chown appuser:appgroup /app/workspace

# Pre-install commonly requested Python packages and enable --user installs
RUN pip3 install --break-system-packages numpy requests && \
    mkdir -p /home/appuser/.local && chown -R appuser:appgroup /home/appuser/.local

USER appuser

ENV PORT=8100
EXPOSE ${PORT}
ENTRYPOINT ["./system-utilities-tool"]
