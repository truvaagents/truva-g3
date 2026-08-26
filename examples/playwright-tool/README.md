# Playwright Tool

A TruvaG3 tool that provides headless Chromium browser automation for QA testing, ad-hoc web scraping, and SPA UI verification. Built on [Playwright](https://playwright.dev/) with the [puppeteer-extra-plugin-stealth](https://github.com/berstend/puppeteer-extra/tree/master/packages/puppeteer-extra-plugin-stealth) anti-detection plugin, it can execute test suites, explore pages, run multi-step UI flows, and scrape JavaScript-rendered content. Test artifacts (screenshots, traces, scripts) are optionally persisted to S3-compatible storage with pre-signed URLs for retrieval.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Registered Capabilities](#registered-capabilities)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [S3 Artifact Storage](#s3-artifact-storage)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

This tool provides Chromium-based browser capabilities that agents can discover and use. Unlike agents, tools are independent — they only need Redis for service discovery and (optionally) S3-compatible storage for test artifact persistence.

### Prerequisites

Before running this example, you need to install the following tools. Choose the instructions for your operating system.

#### 1. Docker Desktop

Docker is required to build and run containers.

| Platform | Installation Method |
|----------|---------------------|
| **macOS** | Download from [docker.com/products/docker-desktop](https://www.docker.com/products/docker-desktop/) and drag to Applications |
| **Windows** | Download from [docker.com/products/docker-desktop](https://www.docker.com/products/docker-desktop/) and run the installer |
| **Linux** | See [docs.docker.com/engine/install](https://docs.docker.com/engine/install/) for your distribution |

<details>
<summary><strong>macOS Installation Steps</strong></summary>

1. Download Docker Desktop from [docker.com/products/docker-desktop](https://www.docker.com/products/docker-desktop/)
2. Double-click `Docker.dmg` to open the installer
3. Drag the Docker icon to the Applications folder
4. Double-click `Docker.app` in Applications to start Docker
5. Follow the onboarding tutorial (optional)

**Verify installation:**
```bash
docker --version
# Expected: Docker version 24.x.x or later
```

**System Requirements:**
- macOS 12 (Monterey) or later
- At least 4 GB RAM (8 GB recommended — Chromium browsers in the container need headroom)
- Apple Silicon (M1/M2/M3) or Intel processor

</details>

<details>
<summary><strong>Windows Installation Steps</strong></summary>

1. Download Docker Desktop from [docker.com/products/docker-desktop](https://www.docker.com/products/docker-desktop/)
2. Run the `Docker Desktop Installer.exe`
3. Follow the installation wizard
4. Restart your computer when prompted
5. Start Docker Desktop from the Start menu

**Verify installation:**
```powershell
docker --version
# Expected: Docker version 24.x.x or later
```

**System Requirements:**
- Windows 10 64-bit (Build 19041+) or Windows 11
- WSL 2 backend (recommended) or Hyper-V
- At least 4 GB RAM (8 GB recommended)
- BIOS virtualization enabled

**Enable WSL 2 (if not already enabled):**
```powershell
wsl --install
```

</details>

<details>
<summary><strong>Linux Installation Steps (Ubuntu/Debian)</strong></summary>

```bash
# Remove old versions
sudo apt-get remove docker docker-engine docker.io containerd runc

# Install prerequisites
sudo apt-get update
sudo apt-get install ca-certificates curl gnupg

# Add Docker's official GPG key
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg

# Add the repository
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
  $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

# Install Docker Engine
sudo apt-get update
sudo apt-get install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# Add your user to the docker group (to run without sudo)
sudo usermod -aG docker $USER
newgrp docker
```

**Verify installation:**
```bash
docker --version
docker run hello-world
```

</details>

<details>
<summary><strong>Linux Installation Steps (Fedora/RHEL)</strong></summary>

```bash
# Remove old versions
sudo dnf remove docker docker-client docker-client-latest docker-common docker-latest

# Install Docker
sudo dnf -y install dnf-plugins-core
sudo dnf config-manager --add-repo https://download.docker.com/linux/fedora/docker-ce.repo
sudo dnf install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# Start Docker
sudo systemctl start docker
sudo systemctl enable docker

# Add your user to the docker group
sudo usermod -aG docker $USER
newgrp docker
```

**Verify installation:**
```bash
docker --version
docker run hello-world
```

</details>

---

#### 2. Kind (Kubernetes in Docker)

Kind runs local Kubernetes clusters using Docker containers.

| Platform | Recommended Method | Alternative |
|----------|-------------------|-------------|
| **macOS** | `brew install kind` | Binary download |
| **Windows** | `choco install kind` | `winget install Kubernetes.kind` |
| **Linux** | Binary download | Package manager |

<details>
<summary><strong>macOS Installation</strong></summary>

**Using Homebrew (recommended):**
```bash
brew install kind
```

**Manual binary installation (Apple Silicon):**
```bash
curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.31.0/kind-darwin-arm64
chmod +x ./kind
sudo mv ./kind /usr/local/bin/kind
```

**Manual binary installation (Intel):**
```bash
curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.31.0/kind-darwin-amd64
chmod +x ./kind
sudo mv ./kind /usr/local/bin/kind
```

**Verify installation:**
```bash
kind --version
# Expected: kind version 0.31.0 or later
```

</details>

<details>
<summary><strong>Windows Installation</strong></summary>

**Using Chocolatey (recommended):**
```powershell
choco install kind
```

**Using Winget:**
```powershell
winget install Kubernetes.kind
```

**Manual binary installation:**
```powershell
curl.exe -Lo kind-windows-amd64.exe https://kind.sigs.k8s.io/dl/v0.31.0/kind-windows-amd64
Move-Item .\kind-windows-amd64.exe C:\Windows\System32\kind.exe
```

**Verify installation:**
```powershell
kind --version
# Expected: kind version 0.31.0 or later
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Binary installation (AMD64/x86_64):**
```bash
curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.31.0/kind-linux-amd64
chmod +x ./kind
sudo mv ./kind /usr/local/bin/kind
```

**Binary installation (ARM64):**
```bash
curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.31.0/kind-linux-arm64
chmod +x ./kind
sudo mv ./kind /usr/local/bin/kind
```

**Using Go (if Go is installed):**
```bash
go install sigs.k8s.io/kind@v0.31.0
```

**Verify installation:**
```bash
kind --version
# Expected: kind version 0.31.0 or later
```

</details>

---

#### 3. kubectl (Kubernetes CLI)

kubectl is the command-line tool for interacting with Kubernetes clusters.

| Platform | Recommended Method | Alternative |
|----------|-------------------|-------------|
| **macOS** | `brew install kubectl` | Binary download |
| **Windows** | `choco install kubernetes-cli` | Binary download |
| **Linux** | `apt install kubectl` | Binary download |

<details>
<summary><strong>macOS Installation</strong></summary>

**Using Homebrew (recommended):**
```bash
brew install kubectl
```

**Verify installation:**
```bash
kubectl version --client
# Expected: Client Version: v1.31.x or later
```

</details>

<details>
<summary><strong>Windows Installation</strong></summary>

**Using Chocolatey (recommended):**
```powershell
choco install kubernetes-cli
```

**Verify installation:**
```powershell
kubectl version --client
# Expected: Client Version: v1.31.x or later
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Using apt (Ubuntu/Debian):**
```bash
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates curl gnupg
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.31/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
sudo chmod 644 /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.31/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list
sudo chmod 644 /etc/apt/sources.list.d/kubernetes.list
sudo apt-get update
sudo apt-get install -y kubectl
```

**Verify installation:**
```bash
kubectl version --client
# Expected: Client Version: v1.31.x or later
```

</details>

---

#### 4. Go Programming Language

Go is required for local development and running without Docker.

| Platform | Recommended Method | Alternative |
|----------|-------------------|-------------|
| **macOS** | `brew install go` | Download from go.dev |
| **Windows** | Download MSI from go.dev | `choco install golang` |
| **Linux** | Download tarball from go.dev | Package manager |

<details>
<summary><strong>macOS Installation</strong></summary>

**Using Homebrew (recommended):**
```bash
brew install go
```

**Verify installation:**
```bash
go version
# Expected: go version go1.27.x darwin/arm64 (or darwin/amd64)
```

</details>

<details>
<summary><strong>Windows Installation</strong></summary>

**Using the MSI installer (recommended):**
1. Download the Windows installer from [go.dev/dl](https://go.dev/dl/)
2. Run the `.msi` installer
3. Follow the installation wizard
4. The installer sets PATH automatically

**Verify installation:**
```powershell
go version
# Expected: go version go1.27.x windows/amd64
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

```bash
curl -LO https://go.dev/dl/go1.27.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.27.0.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

**Verify installation:**
```bash
go version
# Expected: go version go1.27.x linux/amd64
```

</details>

---

#### 5. S3-Compatible Storage (Optional)

S3 is **optional** but degrades several capabilities. With S3 unconfigured:
- `stealth_browser`, `browser_test`, `explore_page` — fully functional (no S3 dependency)
- `run_tests` — still executes tests; screenshots/traces/scripts are not persisted to S3
- `get_artifacts` — returns **HTTP 503 `S3_UNAVAILABLE`**
- `get_results` — depends on the Redis store, not S3; returns indexed runs if Redis is available, empty otherwise
- `lookup_scripts` — depends on the Redis script store, **not** S3; returns HTTP 503 `STORE_UNAVAILABLE` when Redis is missing, otherwise lists script metadata from Redis

**Local development with MinIO:**
- See the [infrastructure README](../k8-deployment/README.md) for the local MinIO setup. Set `S3_ENDPOINT=http://minio.truvag3-examples:9000`.

**Real AWS S3:**
- Create a bucket (e.g. `truvag3-qa-playwright`)
- Create an IAM user with `s3:PutObject`, `s3:GetObject`, `s3:ListBucket` on that bucket
- Leave `S3_ENDPOINT` unset (the SDK uses the standard AWS endpoint)
- Provide `S3_ACCESS_KEY` and `S3_SECRET_KEY` (or an IAM role on the pod)

---

### Verify All Prerequisites

Run this script to verify all tools are installed correctly:

```bash
echo "Checking prerequisites..."
echo ""

echo "Docker:"
docker --version || echo "  ERROR: Docker not found"
echo ""

echo "Kind:"
kind --version || echo "  ERROR: Kind not found"
echo ""

echo "kubectl:"
kubectl version --client --short 2>/dev/null || kubectl version --client || echo "  ERROR: kubectl not found"
echo ""

echo "Go:"
go version || echo "  ERROR: Go not found"
echo ""

echo "All checks complete!"
```

---

### Quick Start (Recommended)

The fastest way to get the playwright tool running:

```bash
cd examples/playwright-tool

# 1. Create .env from the example file (safe — won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**⚠️ STOP HERE (Optional)** — If you want test artifacts persisted to S3, open `.env` and configure your storage:

```bash
nano .env    # or: code .env / vim .env
```

**Optional:** Set your S3 credentials in `.env`:
- `S3_ACCESS_KEY` / `S3_SECRET_KEY` — credentials (the only S3 values copied into the K8s pod from `.env`, via the `playwright-s3-credentials` Secret)
- `S3_BUCKET` / `S3_REGION` — used by `go run .` local-dev only; for K8s, edit the hardcoded values in [`k8-deployment.yaml`](k8-deployment.yaml)
- `S3_ENDPOINT` — leave empty for real AWS S3; for local MinIO during `go run .`, set to your laptop MinIO URL; for in-cluster MinIO targeting from the K8s pod, add an explicit `S3_ENDPOINT=http://minio.truvag3-examples:9000` entry to `k8-deployment.yaml`
- Without S3 configured, the tool still runs all capabilities but won't persist artifacts (see degraded-mode list below)

After reviewing your configuration, continue with deployment:

```bash
# 2. Deploy to Kubernetes (requires cluster and Redis to be running)
./setup.sh deploy
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image (Debian Bookworm with Node.js + Playwright + Chromium + stealth plugin)
2. Loads it into the Kind cluster
3. Deploys the tool to Kubernetes
4. Registers 7 capabilities with Redis for agent discovery

Once complete, the tool is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **Playwright API** | http://localhost:8349 | Browser automation, page exploration, test execution |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The tool requires Redis for service discovery. If you haven't already set up infrastructure, run these from this directory:

```bash
cd examples/playwright-tool

./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

#### Step 2: Build and Deploy

Configure your S3 credentials in `.env` (see Quick Start above), then `./setup.sh deploy` creates the ConfigMap and S3 credentials Secret from `.env` automatically. A plain manifest apply would leave the pod in `CreateContainerConfigError` because the manifest references `playwright-tool-env-config` and `playwright-s3-credentials` that don't exist until `setup.sh` builds them.

```bash
cd examples/playwright-tool

# Build the Docker image only (does not deploy)
./setup.sh docker-build

# Full deploy: build + load into Kind + create namespace + ConfigMap + Secret from .env + apply manifest
./setup.sh deploy

# Verify deployment
./setup.sh status
```

> **Tip:** If you don't already have a cluster and infrastructure, `./setup.sh full-deploy` does everything from scratch in one shot — cluster, monitoring, tool, and port forwards.

#### Step 3: Test the Tool

```bash
# Port forward the tool service to localhost:8349
./setup.sh forward

# Or run a built-in smoke test against the deployed tool
./setup.sh test
```

In a second terminal (while `./setup.sh forward` is running):

```bash
curl -X POST http://localhost:8349/api/capabilities/stealth_browser \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com", "extract_content": "text"}'
```

---

## Features

- **Page Exploration** — Real Chromium navigation that extracts navigation links, forms, buttons, and SPA framework info; works with React/Vue/Angular/Next.js
- **Playwright Test Execution** — Run Playwright TypeScript test suites against any target URL with full screenshot + trace artifacts
- **Stealth Web Scraping** — Single-shot page loads with anti-bot-detection (`puppeteer-extra-plugin-stealth`) for JavaScript-rendered pages and CAPTCHA-protected sites
- **Multi-Step UI Tests** — Action-sequence browser tests (click, fill, assert, navigate, screenshot) ideal for SPA login flows and CRUD verification
- **Script Reuse** — Saves test scripts to S3 by hostname; agents can list and re-run scripts for regression testing
- **S3 Artifact Persistence** — Screenshots, traces, and test scripts are uploaded with pre-signed URLs (24h default expiry, configurable up to 168h)
- **Result Indexing** — Test runs are indexed in Redis for fast query by site, status, and date range
- **Full Telemetry** — OpenTelemetry traces, metrics, and structured logging
- **Automatic Service Discovery** — Registers with Redis for agent discovery

---

## Registered Capabilities

The tool registers 7 capabilities with the service mesh:

### 1. Explore Page (`explore_page`)

**Endpoint:** `/api/capabilities/explore_page`

Explores a web page using a real Chromium browser and extracts all testable elements — navigation links, forms, buttons, images, interactive components. Use before generating test scripts to discover actual selectors. Handles SPAs (React, Vue, Angular, Next.js) with framework detection and hydration waits.

**Request:**
```json
{
  "url": "https://example.com",
  "depth": 1,
  "follow_links": false,
  "viewport": "1280x720",
  "wait_for_spa": true,
  "spa_timeout_ms": 15000
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `url` | string | Yes | Target page URL (must include protocol) |
| `depth` | integer | No | Levels of links to follow (default 1, max 3) |
| `follow_links` | boolean | No | Follow same-origin links (default `false`) |
| `viewport` | string | No | Browser viewport as WxH (default `1280x720`) |
| `wait_for_spa` | boolean | No | Enable SPA detection + hydration wait (default `true`) |
| `spa_timeout_ms` | integer | No | Max ms to wait for SPA hydration (default 15000) |

**Response:**
```json
{
  "success": true,
  "data": {
    "url": "https://example.com/",
    "title": "Example Domain",
    "navigation": [{"text": "More information...", "href": "https://www.iana.org/...", "selector": "body > div > p:nth-child(3) > a"}],
    "forms": [],
    "interactive_elements": [],
    "spa_info": {"detected": false, "framework": ""},
    "duration_ms": 1025
  }
}
```

### 2. Run Tests (`run_tests`)

**Endpoint:** `/api/capabilities/run_tests`

Executes Playwright test scripts against a target URL in real Chromium. Uploads screenshots and traces to S3, saves scripts for regression re-runs, indexes results in Redis. Supports script reuse — provide `reuse_script_name` to fetch a stored script from S3 instead of inline script.

**Request:**
```json
{
  "target_url": "https://example.com",
  "script": "import { test, expect } from '@playwright/test';\n\ntest('page loads', async ({page}) => {\n  await page.goto('https://example.com');\n  await expect(page).toHaveTitle(/Example/);\n});",
  "script_name": "homepage-title",
  "timeout_ms": 60000,
  "browser": "chromium",
  "viewport": {"width": 1280, "height": 720}
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `target_url` | string | Yes | Website URL being tested (used for S3 path organization) |
| `script` | string | Conditional | Inline Playwright TypeScript test code (required unless `reuse_script_name` is provided) |
| `reuse_script_name` | string | Conditional | Fetch a stored script from S3 by name |
| `script_name` | string | No | Name for the test script (used for S3 storage and regression re-runs) |
| `timeout_ms` | integer | No | Overall test timeout in ms (default 60000, max 300000) |
| `browser` | string | No | Browser engine: `chromium`, `firefox`, `webkit` (default `chromium`) |
| `viewport` | object | No | Viewport dimensions (default `{width: 1280, height: 720}`) |

**Response:**
```json
{
  "success": true,
  "data": {
    "run_id": "run-abc123",
    "target_url": "https://example.com",
    "script_name": "homepage-title",
    "summary": {"total": 1, "passed": 1, "failed": 0, "skipped": 0, "duration_ms": 1234},
    "results": [{"test": "page loads", "status": "passed", "duration_ms": 1234, "screenshot_url": "https://s3..."}],
    "artifacts": {"base_path": "s3://bucket/runs/run-abc123/", "screenshot_count": 1, "trace_count": 1, "urls_expire_at": "2026-05-23T03:00:00Z"}
  }
}
```

### 3. Get Results (`get_results`)

**Endpoint:** `/api/capabilities/get_results`

Queries past test run results from Redis with optional filters by site, status, and date range. Use to check previous test outcomes before re-running or to build regression reports.

**Request:**
```json
{
  "site": "example.com",
  "status": "failed",
  "from_date": "2026-05-01",
  "to_date": "2026-05-22",
  "limit": 20
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `site` | string | No | Filter by site domain |
| `status` | string | No | Filter by overall status: `passed`, `failed`, `mixed` |
| `from_date` | string | No | Start date (`YYYY-MM-DD`) |
| `to_date` | string | No | End date (`YYYY-MM-DD`) |
| `limit` | integer | No | Max results (default 20) |

**Response:** Array of run metadata objects (indexed in Redis at storage time — artifact URLs in the response are whatever was stored at that point and may have expired). To refresh artifact URLs for a known `run_id`, use [`get_artifacts`](#4-get-artifacts-get_artifacts).

### 4. Get Artifacts (`get_artifacts`)

**Endpoint:** `/api/capabilities/get_artifacts`

Regenerates time-limited pre-signed S3 URLs for a test run's screenshots and traces. Use when previously returned artifact URLs have expired (default 24h expiry).

**Request:**
```json
{
  "run_id": "run-abc123",
  "expiry_hours": 24
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `run_id` | string | Yes | Test run ID to get artifacts for |
| `expiry_hours` | integer | No | URL validity in hours (default 24, max 168) |

**Response:**
```json
{
  "success": true,
  "data": {
    "run_id": "run-abc123",
    "target_url": "https://example.com",
    "artifacts": [{"type": "screenshot", "name": "page-loads-1.png", "size_bytes": 124003, "url": "https://s3...", "s3_key": "runs/run-abc123/page-loads-1.png"}],
    "urls_expire_at": "2026-05-23T03:00:00Z"
  }
}
```

### 5. Lookup Scripts (`lookup_scripts`)

**Endpoint:** `/api/capabilities/lookup_scripts`

Lists reusable Playwright test scripts stored for a hostname (subdomain). Returns script names, test names they cover, version, and last run status. Use before `run_tests` to check if a reusable script already exists. Does not return script content — only metadata for the LLM to judge relevance.

**Request:**
```json
{
  "hostname": "developer.cisco.com"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `hostname` | string | Yes | Subdomain to look up scripts for (e.g. `developer.cisco.com`, not `cisco.com`) |

**Response:**
```json
{
  "success": true,
  "data": {
    "hostname": "developer.cisco.com",
    "scripts": [
      {"name": "homepage-nav", "version": 3, "test_names": ["nav loads", "search works"], "test_count": 2, "last_run_status": "passed", "last_run_date": "2026-05-20"}
    ]
  }
}
```

### 6. Stealth Browser (`stealth_browser`)

**Endpoint:** `/api/capabilities/stealth_browser`

Opens a URL in a headless Chromium browser with anti-detection stealth plugin and extracts page content. Uses [Playwright](https://playwright.dev/) with [puppeteer-extra-plugin-stealth](https://github.com/berstend/puppeteer-extra/tree/master/packages/puppeteer-extra-plugin-stealth) to bypass bot-detection, CAPTCHAs, and fingerprinting. Use for single-page content extraction; for multi-step interactions use `browser_test` instead.

**Request:**
```json
{
  "url": "https://example.com",
  "extract_content": "text",
  "screenshot": true,
  "timeout": 60
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `url` | string | Yes | Full URL to navigate to (must include `http://` or `https://`) |
| `wait_for` | string | No | CSS selector to wait for before extracting content (useful for JS-rendered pages) |
| `extract_content` | string | No | `text` (default), `html`, or `both` |
| `screenshot` | boolean | No | Capture a full-page screenshot as base64 PNG (default `false`) |
| `timeout` | integer | No | Navigation timeout in seconds (default 60, max 120) |
| `javascript` | string | No | JavaScript code to execute on the page after load; must include explicit `return` statement |
| `user_agent` | string | No | Custom User-Agent string to override the default browser UA |

**Response:**
```json
{
  "success": true,
  "data": {
    "url": "https://example.com/",
    "title": "Example Domain",
    "text_content": "Example Domain\nThis domain is for use in illustrative examples...",
    "screenshot_base64": "iVBORw0KGgoAAAANSUhEUg...",
    "status_code": 200,
    "duration_ms": 1259
  }
}
```

**Resource note:** Each call launches a full Chromium process. To avoid resource exhaustion, chain calls sequentially using `depends_on` in agent workflows rather than running them all in parallel. Use at most 2 concurrent calls.

### 7. Browser Test (`browser_test`)

**Endpoint:** `/api/capabilities/browser_test`

Executes a multi-step UI test in headless Chromium using Playwright with stealth anti-detection. Designed for testing Single Page Applications (Vue.js, React, Angular) — login flows, form submissions, navigation testing, CRUD operations, responsive layout verification, SPA route transitions.

**Request:**
```json
{
  "url": "https://myapp.com/login",
  "actions": [
    {"action": "wait_for_selector", "selector": "[data-testid='email-input']"},
    {"action": "fill", "selector": "[data-testid='email-input']", "value": "user@test.com"},
    {"action": "click", "selector": "[data-testid='login-button']"},
    {"action": "wait_for_url", "value": "**/dashboard**"},
    {"action": "wait_for_network_idle"},
    {"action": "assert", "assertion": "visible", "selector": "[data-testid='welcome-message']"},
    {"action": "screenshot"}
  ],
  "timeout": 120,
  "viewport": {"width": 1280, "height": 720}
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `url` | string | Yes | Full starting URL (must include protocol) |
| `actions` | array | Yes | Ordered test steps — see Action Types below |
| `timeout` | integer | No | Overall test timeout in seconds (default 120, max 300) |
| `viewport` | object | No | Viewport dimensions for responsive testing. Default `1280x720`. Use `{"width": 375, "height": 812}` for mobile (iPhone) |

**Action types:**

| Action | Required Fields | Description |
|--------|----------------|-------------|
| `click` | `selector` | Click an element |
| `fill` | `selector`, `value` | Type text into an input |
| `select` | `selector`, `value` | Pick a `<select>` option |
| `check` / `uncheck` | `selector` | Tick / untick a checkbox |
| `hover` | `selector` | Hover over an element |
| `press` | `selector`, `value` | Press a key (e.g. `Enter`) |
| `navigate` | `value` (URL) | Navigate to a new URL |
| `wait_for_selector` | `selector` | Wait for an element to be visible |
| `wait_for_url` | `value` (glob pattern) | Wait for the URL to match |
| `wait_for_network_idle` | — | Wait for network to settle |
| `screenshot` | — | Capture a base64 PNG, keyed by step index |
| `assert` | `assertion`, plus assertion-specific fields | See Assertion Types below |

**Assertion types** (used with `action: "assert"`):

| Assertion | Required | Description |
|-----------|----------|-------------|
| `visible` / `hidden` | `selector` | Element is visible / hidden |
| `text_contains` / `text_equals` | `selector`, `expected` | Element text contains / equals |
| `url_contains` / `url_equals` | `expected` | Current URL contains / equals |
| `count_equals` | `selector`, `expected` (integer as string) | N matching elements |
| `has_attribute` | `selector`, `expected` (format: `"attr=value"`) | Attribute equals value |
| `has_class` | `selector`, `expected` | Element has class |

**SPA testing tips:**
- After clicks that trigger route changes, add `wait_for_url` before assertions
- After navigation, add `wait_for_network_idle` to let async data fetches complete
- Prefer `[data-testid='...']` selectors over CSS class selectors for stability
- Use `wait_for_selector` before interacting with dynamically rendered components

**Response:**
```json
{
  "success": true,
  "data": {
    "url": "https://myapp.com/dashboard",
    "passed": true,
    "total_steps": 7,
    "passed_steps": 7,
    "failed_steps": 0,
    "steps": [
      {"step": 0, "action": "wait_for_selector", "selector": "[data-testid='email-input']", "passed": true, "duration_ms": 124},
      {"step": 1, "action": "fill", "passed": true, "duration_ms": 89}
    ],
    "screenshots": {"6": "iVBORw0KGgoAAAANSUhEUg..."},
    "console_log": ["[log] Login successful"],
    "duration_ms": 3200
  }
}
```

---

## Architecture

```
Playwright Tool (Passive)
    |
    +-- Registers 7 capabilities in Redis
    +-- Receives requests from agents
    +-- Spawns Node.js subprocesses for Chromium automation
    +-- Uploads artifacts to S3 (optional)
    +-- Indexes results in Redis (DB 9 by default)
    +-- Returns standardized responses
    |
    +-- Capabilities:
        +-- explore_page      (Chromium + node script → page DOM analysis)
        +-- run_tests         (Playwright Test runner → JSON reporter → S3 upload)
        +-- get_results       (Redis ZRANGE on indexed runs)
        +-- get_artifacts     (S3 ListObjectsV2 + pre-signed URL generation)
        +-- lookup_scripts    (Redis HSCAN on script metadata keyed by hostname)
        +-- stealth_browser   (Node.js → playwright-extra + stealth plugin → Chromium)
        +-- browser_test      (Node.js → playwright-extra + stealth plugin → Chromium → action loop)

Agents (Active)
    |
    +-- Discover playwright tool via Redis
    +-- Use AI for tool + script selection
    +-- Generate payloads (URL, actions, assertions) automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the playwright tool is automatically discovered by agents via Redis. Agents query by capability name, so callers don't need to know this tool's name:

```bash
# Query through an orchestrating agent
curl -X POST http://localhost:8091/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "what does playwright.dev look like — give me a screenshot",
    "ai_synthesis": true
  }'
```

### Why Two Browser Capabilities?

`stealth_browser` and `browser_test` both wrap Chromium + stealth plugin but serve different shapes of work:

| | `stealth_browser` | `browser_test` |
|---|---|---|
| **Use case** | Single-page content extraction | Multi-step UI flows |
| **Input** | URL + optional JS snippet | URL + ordered action array |
| **Output** | text/HTML/screenshot/JS result | per-step pass/fail + screenshots |
| **Multi-step?** | No (one navigation) | Yes (click → fill → assert chains) |
| **Use when** | "Scrape this page" / "What does X say?" | "Test that login works" / "Verify the checkout flow" |

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8349` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `S3_BUCKET` | S3 bucket for artifact storage | - | No* |
| `S3_REGION` | S3 region | `us-east-1` | No |
| `S3_ENDPOINT` | Custom S3 endpoint (MinIO, etc.); empty for AWS | - | No |
| `S3_ACCESS_KEY` | S3 credential | - | No* |
| `S3_SECRET_KEY` | S3 credential | - | No* |
| `PLAYWRIGHT_SCRIPT_DIR` | Directory for built-in scripts | `/app/scripts` | No |
| `REDIS_QA_DB` | Redis DB number for test result indexing | `9` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `APP_ENV` | Environment profile (`development`/`staging`/`production`) | `development` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (`error`\|`warn`\|`info`\|`debug`) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (`json`\|`text`) | `json` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |

*Tool runs without S3 but with degraded capabilities: `stealth_browser`, `browser_test`, `explore_page` are fully functional; `run_tests` executes but artifacts aren't persisted; `get_artifacts` returns HTTP 503 `S3_UNAVAILABLE`. `get_results` and `lookup_scripts` depend on the Redis store (independent of S3); `lookup_scripts` returns HTTP 503 `STORE_UNAVAILABLE` if Redis is missing.

---

## S3 Artifact Storage

When configured, the tool uploads test artifacts to S3 organized as:

```
s3://<bucket>/
├── runs/
│   └── <run-id>/
│       ├── <test-name>-screenshot.png
│       ├── <test-name>-trace.zip
│       └── script.spec.ts                 # snapshot of the exact script executed (audit trail)
└── <hostname>/
    └── scripts/
        └── <script-name>.spec.ts          # reusable copy (overwritten each save; version tracked in Redis)
```

**Pre-signed URLs:** Returned in `run_tests` responses (24h expiry) and refreshable via `get_artifacts` (configurable expiry up to 168h / 7 days).

**Script reuse:** When `run_tests` is called with `script_name`, the inline script is also uploaded under `<hostname>/scripts/<script-name>.spec.ts`. The S3 key is overwritten on each save; the version counter is stored in Redis (see `lookup_scripts` response). Subsequent runs can pass `reuse_script_name` instead of `script` to re-execute the latest stored version.

**Result indexing:** Each run's metadata (target URL, status, duration, artifact URLs) is indexed in Redis (DB 9 by default) keyed by hostname and date for fast querying via `get_results`. Script reuse metadata (name, version, test names, last run status) also lives in Redis — `lookup_scripts` reads from this store.

---

## Project Structure

```
playwright-tool/
├── main.go                 # Entry point, config validation, telemetry, framework setup
├── playwright_tool.go      # Tool struct, request/response types, capability registration
├── handlers.go             # HTTP handlers for all 7 capabilities with full telemetry
├── browser_client.go       # Node.js subprocess management for Chromium automation
├── s3_client.go            # S3 client (AWS SDK v2) for artifact upload + pre-signed URLs
├── store.go                # Redis-backed test result indexing
├── scripts/                # Built-in Playwright scripts (explore.js, etc.)
├── go.mod                  # Go module definition
├── .env.example            # Environment variable documentation
├── Dockerfile              # Standalone container (Debian Bookworm + Node.js + Playwright + Chromium)
├── Dockerfile.workspace    # Dev container build from truvag3 root
├── k8-deployment.yaml      # Kubernetes Service + Deployment manifests
├── setup.sh                # Full lifecycle script (build, run, deploy, test, clean)
└── README.md               # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

```bash
# Check Redis registration
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*" | grep playwright

# Check tool logs
./setup.sh logs
```

**2. Browser timeouts on JS-heavy SPAs**

```bash
# Increase the timeout field (max 120s for stealth_browser, 300s for browser_test + run_tests)
curl -X POST http://localhost:8349/api/capabilities/stealth_browser \
  -H "Content-Type: application/json" \
  -d '{"url": "https://heavy-spa.com", "wait_for": "#app-loaded", "timeout": 120}'
```

For SPAs, also add `wait_for` (CSS selector for a known late-rendering element) so the tool waits for hydration before extracting content.

**3. `stealth_browser` returns `BROWSER_ERROR`**

```bash
# Verify Node.js and Playwright are installed in the container
kubectl exec -n truvag3-examples deploy/playwright-tool -- node -e "console.log('ok')"
kubectl exec -n truvag3-examples deploy/playwright-tool -- node -e "require('playwright-extra'); console.log('playwright-extra ok')"

# Check if Chromium binary is present
kubectl exec -n truvag3-examples deploy/playwright-tool -- npx playwright install --dry-run
```

Common causes:
- **"Browser timed out"** — Increase the `timeout` field. Some sites load slowly.
- **"Cannot find module 'playwright-extra'"** — Rebuild the Docker image; npm packages may not have installed correctly.
- **"Chromium sandbox error"** — The container runs as non-root; `--no-sandbox` is passed automatically. If you see sandbox errors, check Chromium shared-library deps (`libnss3`, `libgbm1`, etc.) are installed.

**4. `browser_test` failing on dynamic elements**

If steps fail with "selector not found" on a SPA:
- Add `wait_for_selector` before interacting with dynamically rendered components
- Use `[data-testid='...']` selectors instead of CSS class selectors
- After route-changing clicks, add `wait_for_url` before subsequent assertions
- After navigation, add `wait_for_network_idle` to let async data fetches complete

**5. S3 upload failures (`run_tests` succeeds but artifacts missing)**

```bash
# Check pod environment
kubectl exec -n truvag3-examples deploy/playwright-tool -- env | grep S3_

# Check logs for S3 errors
./setup.sh logs | grep -i s3
```

Common causes:
- `S3_BUCKET` not set → tool runs in degraded mode (warns at startup); add to deployment env
- `S3_ACCESS_KEY` / `S3_SECRET_KEY` missing → secret not created; run `./setup.sh deploy` again to refresh from `.env`
- Wrong endpoint → for MinIO, set `S3_ENDPOINT=http://minio.truvag3-examples:9000`; for AWS S3, leave empty
- Bucket permissions → IAM user needs `s3:PutObject`, `s3:GetObject`, `s3:ListBucket`

**6. Pre-signed URLs expired**

URLs from `run_tests` expire 24h by default. To regenerate:

```bash
curl -X POST http://localhost:8349/api/capabilities/get_artifacts \
  -H "Content-Type: application/json" \
  -d '{"run_id": "run-abc123", "expiry_hours": 168}'   # up to 7 days
```

**7. Pod not starting**

```bash
# Check pod / service status
./setup.sh status

# Stream logs
./setup.sh logs
```

### Useful Commands

All day-to-day operations go through `setup.sh`. Run `./setup.sh help` to see every subcommand.

```bash
# View tool logs (streams)
./setup.sh logs

# Check pod / service status
./setup.sh status

# Port forward the tool to localhost:8349
./setup.sh forward

# Port forward tool + monitoring dashboards (Grafana, Prometheus, Jaeger)
./setup.sh forward-all

# Restart the deployment (e.g., to pick up new S3 creds from .env)
./setup.sh rollout

# Rebuild image and restart (use after changing Go code)
./setup.sh rollout --build

# Run the built-in smoke test suite against the deployed tool
./setup.sh test

# Remove only the tool (keeps cluster + infra)
./setup.sh clean

# Tear down the entire Kind cluster
./setup.sh clean-all
```

While `./setup.sh forward` is running, you can query the tool with `curl`:

```bash
# List registered capabilities
curl -s http://localhost:8349/api/capabilities | jq '[.[].name]'
```

---

## Development

### Local Development

```bash
# Set environment variables
export REDIS_URL="redis://localhost:6379"
export S3_BUCKET="truvag3-qa"
export S3_ENDPOINT="http://localhost:9000"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export PORT=8349

# Install Playwright + stealth plugin locally (matches Dockerfile install set)
# @playwright/test is required by run_tests scripts that import from '@playwright/test'
npm install -g playwright @playwright/test playwright-extra puppeteer-extra-plugin-stealth
npx playwright install chromium

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `playwright_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. For Node.js-based capabilities, add script builder helpers (see `buildPlaywrightScript`, `buildPlaywrightTestScript`)

---

## Related Examples

- [system-utilities-tool](../system-utilities-tool/) — Companion tool for shell + Python execution, date/time, IDs (this tool absorbed `stealth_browser` and `browser_test` from there to keep the system-utilities-tool image lightweight)
- [travel-chat-agent](../travel-chat-agent/) — Streaming chat agent that can use this tool
- [agent-with-orchestration](../agent-with-orchestration/) — Basic orchestration example
- [stock-market-tool](../stock-market-tool/) — Stock market data tool (similar passive pattern)
- [weather-tool-v2](../weather-tool-v2/) — Weather data tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
