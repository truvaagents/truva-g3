# Confluence Tool

A TruvaG3 tool that provides Confluence Cloud page management capabilities using the [Atlassian Confluence REST API v2](https://developer.atlassian.com/cloud/confluence/rest/v2/intro/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

> **Heads-up: this is a write-capable tool.** `create_page` and `update_page` mutate Confluence data. The API token you provide governs what the tool can change — scope it to a workspace where unintended writes are recoverable, especially during development.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Registered Capabilities](#registered-capabilities)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [API Rate Limits](#api-rate-limits)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

This tool provides Confluence page management capabilities that agents can discover and use. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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
- At least 4 GB RAM
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
- At least 4 GB RAM
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
sudo apt-get update && sudo apt-get install -y kubectl
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

**Using Chocolatey:**
```powershell
choco install golang
```

**Verify installation:**
```powershell
go version
# Expected: go version go1.27.x windows/amd64
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Manual installation (recommended for latest version):**
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

#### 5. Confluence API Token

A Confluence (Atlassian) API token is **required** for this tool. The tool connects to your Atlassian Cloud instance and uses the same token format as JIRA — if you already have a JIRA API token from [jira-tool](../jira-tool/README.md), the same token works here.

1. Visit [id.atlassian.com/manage-profile/security/api-tokens](https://id.atlassian.com/manage-profile/security/api-tokens)
2. Click "Create API token"
3. Give it a label (e.g., "TruvaG3 Confluence Tool")
4. Copy the generated token

**You'll also need:**
- Your Atlassian instance URL (e.g., `https://mycompany.atlassian.net`)
- Your Atlassian account email
- Confluence enabled on your Atlassian Cloud workspace, with at least one space the token's user can read

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

The fastest way to get the Confluence tool running:

```bash
cd examples/confluence-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**Edit `.env`** with your Confluence credentials:

```bash
nano .env    # or: code .env / vim .env
```

- `CONFLUENCE_BASE_URL=https://mycompany.atlassian.net`
- `CONFLUENCE_USER_EMAIL=you@example.com`
- `CONFLUENCE_API_TOKEN=your-api-token` (Get from [id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens))

After configuring your credentials, continue with deployment:

```bash
# 2. Deploy to Kubernetes (requires cluster and Redis to be running)
./setup.sh deploy
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image
2. Loads it into the Kind cluster
3. Deploys the tool to Kubernetes
4. Registers capabilities with Redis for agent discovery

Once complete, the tool is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **Confluence API** | http://localhost:8376 | Confluence page management API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The Confluence tool requires Redis for service discovery. If you haven't already set up infrastructure, run these from this directory:

```bash
cd examples/confluence-tool

./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack (Prometheus, Grafana, Jaeger)
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

#### Step 2: Build and Deploy

Configure your credentials in `.env` (see Quick Start above), then `./setup.sh deploy` creates the Kubernetes Secret from `.env` automatically. `setup.sh` also handles the Docker build, Kind image load, namespace creation, and manifest apply.

```bash
cd examples/confluence-tool

# Build the Docker image only (does not deploy)
./setup.sh docker-build

# Full deploy: build + load into Kind + create namespace + Secret from .env + apply manifest
./setup.sh deploy

# Verify deployment
./setup.sh status
```

> **Tip:** If you don't already have a cluster and infrastructure, `./setup.sh full-deploy` does everything from scratch in one shot — cluster, monitoring, tool, and port forwards.

#### Step 3: Test the Tool

```bash
# Port forward the tool service to localhost:8376
./setup.sh forward

# Or run a built-in smoke test against the deployed tool
./setup.sh test
```

In a second terminal (while `./setup.sh forward` is running):

```bash
# Test list spaces (no required input — fetch this first to get space IDs)
curl -X POST http://localhost:8376/api/capabilities/list_spaces \
  -H "Content-Type: application/json" \
  -d '{"limit": 10}'
```

---

## Features

- **Space Discovery** - List Confluence spaces with their numeric IDs (required input for page creation)
- **Page Retrieval** - Get a page by ID with optional body content
- **CQL Search** - Search pages by free text or raw Confluence Query Language (e.g., `type=page AND title~"outage"`)
- **Page Creation** - Create new pages from markdown-like text (headings, bullets, paragraphs auto-convert to Confluence storage format)
- **Page Updates** - Update title and/or body of an existing page (handles version increment automatically)
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. List Spaces (`list_spaces`)

**Endpoint:** `/api/capabilities/list_spaces`

Lists Confluence spaces with their numeric IDs. Run this first — `create_page` needs the numeric space ID (e.g. `"360452"`), not the human-friendly space key.

**Request:**
```json
{
  "limit": 25
}
```

**Response:**
```json
{
  "spaces": [
    {
      "id": "360452",
      "key": "OPS",
      "name": "Operations",
      "type": "global",
      "status": "current",
      "homepage_id": "98305",
      "url": "https://mycompany.atlassian.net/wiki/spaces/OPS",
      "description": "Runbooks, post-mortems, on-call docs"
    }
  ],
  "total": 1,
  "source": "Confluence API"
}
```

### 2. Search Pages (`search_pages`)

**Endpoint:** `/api/capabilities/search_pages`

Searches Confluence pages by free text or raw CQL. Free-text queries are auto-wrapped as `type=page AND (title~"<query>" OR text~"<query>")`. Raw CQL is detected by the presence of `=` or `~` in the query string and passed through unchanged.

**Request:**
```json
{
  "query": "post-mortem",
  "space_key": "OPS",
  "limit": 10
}
```

**Response:**
```json
{
  "query": "post-mortem",
  "results": [
    {
      "page_id": "458753",
      "title": "Post-Mortem: Stock Tool Outage 2026-03-05",
      "space_key": "OPS",
      "space_name": "Operations",
      "url": "/spaces/OPS/pages/458753/Post-Mortem-Stock-Tool-Outage",
      "excerpt": "The stock-market-tool experienced a 15-minute outage...",
      "version": 3,
      "updated_at": "2026-03-06T14:22:00.000Z"
    }
  ],
  "total": 1,
  "source": "Confluence API"
}
```

### 3. Get Page (`get_page`)

**Endpoint:** `/api/capabilities/get_page`

Retrieves a single page by ID. Set `include_body: true` to fetch the storage-format (XHTML) body alongside metadata.

**Request:**
```json
{
  "page_id": "458753",
  "include_body": true
}
```

**Response:**
```json
{
  "page_id": "458753",
  "title": "Post-Mortem: Stock Tool Outage 2026-03-05",
  "space_id": "360452",
  "url": "https://mycompany.atlassian.net/wiki/spaces/OPS/pages/458753",
  "version": 3,
  "status": "current",
  "content": "<h2>Summary</h2><p>The stock-market-tool experienced a 15-minute outage...</p>",
  "created_at": "2026-03-05T14:30:00.000Z",
  "source": "Confluence API"
}
```

### 4. Create Page (`create_page`)

**Endpoint:** `/api/capabilities/create_page`

Creates a new page in a Confluence space. The `content` field accepts a markdown-like dialect (`##` headings, `-` / `*` bullets, blank-line paragraph breaks) which the tool converts to Confluence storage format (XHTML) before posting.

**Request:**
```json
{
  "space_id": "360452",
  "title": "Post-Mortem: Stock Tool Outage 2026-03-05",
  "content": "## Summary\nThe stock-market-tool experienced a 15-minute outage.\n\n## Timeline\n- 14:00 Alert triggered\n- 14:15 Service restored",
  "parent_id": "98305"
}
```

**Response:**
```json
{
  "page_id": "458753",
  "url": "https://mycompany.atlassian.net/wiki/spaces/OPS/pages/458753",
  "title": "Post-Mortem: Stock Tool Outage 2026-03-05",
  "space_id": "360452",
  "version": 1,
  "created_at": "2026-03-05T14:30:00.000Z",
  "source": "Confluence API"
}
```

### 5. Update Page (`update_page`)

**Endpoint:** `/api/capabilities/update_page`

Updates the title and/or body of an existing page. The tool transparently handles Confluence's required version-increment dance: it fetches the current version, increments it, and submits the update — callers don't need to track versions. At least one of `title` or `content` must be supplied; an empty `title` keeps the existing title.

**Request:**
```json
{
  "page_id": "458753",
  "title": "Post-Mortem: Stock Tool Outage (Resolved)",
  "content": "## Resolution\nRoot cause identified as API rate limiting..."
}
```

**Response:**
```json
{
  "page_id": "458753",
  "url": "https://mycompany.atlassian.net/wiki/spaces/OPS/pages/458753",
  "title": "Post-Mortem: Stock Tool Outage (Resolved)",
  "version": 4,
  "source": "Confluence API"
}
```

---

## Architecture

```
Confluence Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Authenticates via Atlassian Basic Auth (email + API token)
    +-- Calls Confluence REST API v2 (v1 for CQL search)
    +-- Converts markdown-like content to storage-format XHTML
    +-- Returns standardized responses

Agents (Active)
    |
    +-- Discover Confluence tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows (e.g., JIRA outage -> Confluence post-mortem)
```

### Integration with Agents

Once deployed, the Confluence tool is automatically discovered by agents via Redis. A common workflow is pairing it with [jira-tool](../jira-tool/README.md) — when an incident closes in JIRA, an orchestrating agent can fetch the issue, draft a post-mortem, and publish it to Confluence in a single conversation:

```bash
# Query through an orchestrating agent
curl -X POST http://localhost:8350/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "Draft a post-mortem in the OPS space for JIRA issue PROJ-456 and link the resolved ticket.",
    "ai_synthesis": true
  }'
```

### Markdown-like Content Conversion

`create_page` and `update_page` accept a lightweight markdown subset that gets converted to Confluence storage format (XHTML) before posting:

| Input | Storage format |
|-------|----------------|
| `## Heading` | `<h2>Heading</h2>` |
| `### Heading` | `<h3>Heading</h3>` |
| `- Item` or `* Item` | `<ul><li>Item</li></ul>` |
| Plain text line | `<p>Plain text line</p>` |
| Blank line | Paragraph break (closes open list) |

Special characters (`&`, `<`, `>`, `"`) are HTML-escaped. The converter is intentionally minimal — for richer formatting (tables, code blocks, links), pass storage-format XHTML directly in `content`.

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `CONFLUENCE_BASE_URL` | Atlassian Cloud instance URL (e.g., `https://mycompany.atlassian.net`) | - | Yes |
| `CONFLUENCE_USER_EMAIL` | Atlassian account email | - | Yes |
| `CONFLUENCE_API_TOKEN` | Atlassian API token (same as JIRA) | - | Yes |
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8376` | No |
| `NAMESPACE` | Kubernetes namespace | — (set to the pod's namespace by the manifest's downward API) | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `APP_ENV` | Environment profile (development\|staging\|production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

### Required Confluence Permissions

The Atlassian account that owns the API token needs the following per-space permissions for the spaces it will operate on:

| Capability | Required Permission |
|------------|--------------------|
| `list_spaces` | "View" on at least one space (only spaces the user can see are returned) |
| `search_pages`, `get_page` | "View" on the target space(s) |
| `create_page` | "Add pages" on the target space |
| `update_page` | "Add pages" + page is not under restricted editing |

---

## API Rate Limits

Atlassian Cloud rate limits:

| Limit | Value |
|-------|-------|
| **REST API** | Varies by endpoint (typically generous; see [Atlassian rate-limiting docs](https://developer.atlassian.com/cloud/confluence/rate-limiting/)) |
| **Concurrent requests** | Based on Atlassian plan |

The tool implements:
- HTTP Basic Auth with API token (no OAuth complexity)
- Traced HTTP client for all API calls (OpenTelemetry spans)
- Structured upstream-error classification (maps 4xx/5xx + retryability)
- Structured error logging for rate limit tracking
- Graceful error responses on API failures

---

## Project Structure

```
confluence-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── confluence_tool.go      # Tool definition, capability registration, types
├── confluence_client.go    # Confluence REST API v2 client + markdown-to-storage converter
├── handlers.go             # HTTP handlers for each capability
├── go.mod                  # Go module definition
├── Dockerfile.workspace    # Container build from workspace root
├── k8-deployment.yaml      # Kubernetes manifests
├── setup.sh                # Full lifecycle management script
├── .env.example            # Environment variable template
└── README.md               # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

Ensure the tool is registered with Redis:
```bash
# Check Redis connection
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"

# Should show: truvag3:service:confluence-tool-service
```

**2. Authentication errors (401)**

```bash
# Stream logs and grep for auth issues
./setup.sh logs | grep -i "auth\|401"

# Common issues:
# - Invalid API token: Regenerate at https://id.atlassian.com/manage-profile/security/api-tokens
# - Wrong email: Must match the Atlassian account that created the token
# - Wrong base URL: Must be https://your-domain.atlassian.net (no trailing slash, no /wiki suffix)
```

**3. Permission errors (403)**

```bash
# Stream logs and grep for permission issues
./setup.sh logs | grep -i "403\|permission"

# Common issues:
# - Token user lacks "View" on the target space
# - Token user lacks "Add pages" permission for create_page/update_page
# - Page is under restricted editing (page restrictions override space permissions)
```

**4. `create_page` fails with "space not found" or 400**

```bash
# Common issue: passing the space KEY (e.g. "OPS") instead of the numeric space ID.
# Confluence REST API v2 requires the numeric ID for page creation.
# Fix: run list_spaces first and use the `id` field, not `key`.
curl -X POST http://localhost:8376/api/capabilities/list_spaces \
  -H "Content-Type: application/json" -d '{"limit": 25}'
```

**5. Search returns no results**

```bash
# Common issues:
# - Free-text queries match against title and text only — page macros and attachments aren't indexed
# - space_key is case-sensitive (typically uppercase like "OPS", "ENG")
# - CQL detection: queries containing "=" or "~" are treated as raw CQL — escape these in plain searches
# - Indexing lag: newly created pages may not appear in search results immediately
```

**6. `update_page` returns version conflict**

The tool fetches the current version before writing, so version conflicts only happen if another writer updates the page between the GET and the PUT. Retry the call — the tool re-fetches the current version on every invocation.

**7. Docker build fails**

```bash
# Ensure Docker is running
docker info
```

**8. Kind cluster not found**

```bash
# List existing clusters
kind get clusters

# Create a new cluster if none exists (handles the right name + port mappings)
./setup.sh cluster
```

### Useful Commands

All day-to-day operations go through `setup.sh`. Run `./setup.sh help` to see every subcommand.

```bash
# View tool logs (streams)
./setup.sh logs

# Check pod / service status
./setup.sh status

# Port forward the tool to localhost:8376
./setup.sh forward

# Port forward tool + monitoring dashboards (Grafana, Prometheus, Jaeger)
./setup.sh forward-all

# Restart the deployment (e.g., to pick up a new Secret from .env)
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

While `./setup.sh forward` is running, send capability requests with `curl`:

```bash
# List spaces (no required input — start here to get space IDs)
curl -X POST http://localhost:8376/api/capabilities/list_spaces \
  -H "Content-Type: application/json" \
  -d '{"limit": 10}'

# Search pages
curl -X POST http://localhost:8376/api/capabilities/search_pages \
  -H "Content-Type: application/json" \
  -d '{"query": "post-mortem", "space_key": "OPS", "limit": 5}'

# Create a page (replace space_id with a real ID from list_spaces)
curl -X POST http://localhost:8376/api/capabilities/create_page \
  -H "Content-Type: application/json" \
  -d '{
    "space_id": "360452",
    "title": "Test Page from TruvaG3",
    "content": "## Hello\nThis page was created by the confluence-tool."
  }'
```

---

## Development

### Local Development

```bash
# Set environment variables
export CONFLUENCE_BASE_URL="https://mycompany.atlassian.net"
export CONFLUENCE_USER_EMAIL="you@example.com"
export CONFLUENCE_API_TOKEN="your-api-token"
export REDIS_URL="redis://localhost:6379"
export PORT=8376

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `confluence_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add Confluence client method in `confluence_client.go` if needed

---

## Related Examples

- [jira-tool](../jira-tool/) - JIRA issue management tool (same Atlassian auth — token is reusable)
- [devops-chat-agent](../devops-chat-agent/) - DevOps chat agent that pairs well with this tool for post-mortems
- [agent-with-orchestration](../agent-with-orchestration/) - Orchestration example
- [slack-tool](../slack-tool/) - Slack messaging tool (announce new Confluence pages)
- [web-search-tool](../web-search-tool/) - Web search tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
