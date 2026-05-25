# Slack Tool

A TruvaG3 tool that provides Slack workspace messaging and search capabilities using the [Slack Web API](https://api.slack.com/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides Slack messaging and search capabilities that agents can discover and use. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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
# Expected: go version go1.26.x darwin/arm64 (or darwin/amd64)
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
# Expected: go version go1.26.x windows/amd64
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Manual installation (recommended for latest version):**
```bash
curl -LO https://go.dev/dl/go1.26.2.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.2.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

**Verify installation:**
```bash
go version
# Expected: go version go1.26.x linux/amd64
```

</details>

---

#### 5. Slack Bot Token

A Slack Bot User OAuth Token is **required** for this tool. The tool connects to your Slack workspace via the Slack Web API.

1. Visit [api.slack.com/apps](https://api.slack.com/apps) and click "Create New App"
2. Choose "From scratch", give it a name (e.g., "TruvaG3 Slack Tool"), and select your workspace
3. Navigate to **OAuth & Permissions** in the sidebar
4. Under **Bot Token Scopes**, add the following scopes:
   - `chat:write` - Send messages as the bot
   - `chat:write.public` - Send messages to channels the bot isn't a member of
   - `channels:read` - List public channels
   - `search:read` - Search messages in the workspace
5. Click **Install to Workspace** at the top of the page and authorize
6. Copy the **Bot User OAuth Token** (starts with `xoxb-`)

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

The fastest way to get the Slack tool running:

```bash
cd examples/slack-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**Edit `.env`** with your Slack credentials:

```bash
nano .env    # or: code .env / vim .env
```

- `SLACK_BOT_TOKEN=your-slack-bot-token` (Get from [api.slack.com/apps](https://api.slack.com/apps) -> OAuth & Permissions)

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
| **Slack API** | http://localhost:8363 | Slack messaging and search API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The Slack tool requires Redis for service discovery. If you haven't already set up infrastructure, run these from this directory:

```bash
cd examples/slack-tool

./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

#### Step 2: Build and Deploy

`setup.sh` handles the Docker build, Kind image load, namespace + Secret creation, and manifest apply. Configure your credentials in `.env` (see Quick Start above), then `./setup.sh deploy` creates the Kubernetes Secret from `.env` automatically.

```bash
cd examples/slack-tool

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
# Port forward the tool service to localhost:8363
./setup.sh forward

# Or run a built-in smoke test against the deployed tool
./setup.sh test
```

In a second terminal (while `./setup.sh forward` is running):

```bash
curl -X POST http://localhost:8363/api/capabilities/list_channels \
  -H "Content-Type: application/json" \
  -d '{"limit": 10, "exclude_archived": true}'
```

---

## Features

- **Send Messages** - Post text messages to any public channel with optional thread reply support
- **Rich Messages** - Send Block Kit formatted messages with headers, sections, and dividers
- **List Channels** - List public channels in the workspace with pagination support
- **Search Messages** - Search message history across the workspace with sorting options
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext
- **Slack Error Mapping** - Maps Slack's JSON-body errors to proper HTTP status codes for orchestrator error routing

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Send Message (`send_message`)

**Endpoint:** `/api/capabilities/send_message`

Posts a text message to a Slack channel. Supports thread replies via `thread_ts`.

**Request:**
```json
{
  "channel": "C123ABC456",
  "text": "Incident resolved - all systems operational",
  "thread_ts": "1503435956.000247"
}
```

**Response:**
```json
{
  "channel": "C123ABC456",
  "timestamp": "1503435956.000247",
  "text": "Incident resolved - all systems operational",
  "thread_ts": "1503435956.000100",
  "source": "Slack Web API"
}
```

### 2. Send Rich Message (`send_rich_message`)

**Endpoint:** `/api/capabilities/send_rich_message`

Posts a Block Kit formatted message to a Slack channel. Requires fallback text even when blocks are provided.

**Request:**
```json
{
  "channel": "C123ABC456",
  "text": "Deployment notification",
  "blocks": [
    {
      "type": "header",
      "text": {"type": "plain_text", "text": "Deployment Complete"}
    },
    {
      "type": "section",
      "text": {"type": "mrkdwn", "text": "*Service:* api-gateway\n*Version:* v2.3.1\n*Status:* Healthy"}
    },
    {
      "type": "divider"
    },
    {
      "type": "section",
      "text": {"type": "mrkdwn", "text": "All health checks passing. No rollback needed."}
    }
  ]
}
```

**Response:**
```json
{
  "channel": "C123ABC456",
  "timestamp": "1503435960.000300",
  "text": "Deployment notification",
  "source": "Slack Web API"
}
```

### 3. List Channels (`list_channels`)

**Endpoint:** `/api/capabilities/list_channels`

Lists public channels in the Slack workspace with pagination.

**Request:**
```json
{
  "limit": 10,
  "exclude_archived": true
}
```

**Response:**
```json
{
  "channels": [
    {
      "id": "C123ABC456",
      "name": "general",
      "is_archived": false,
      "is_private": false,
      "topic": "Company-wide announcements",
      "purpose": "General discussion",
      "num_members": 150,
      "created": 1449252889,
      "updated": 1678901234
    }
  ],
  "total_count": 1,
  "has_more": false,
  "next_cursor": "",
  "source": "Slack Web API"
}
```

### 4. Search Messages (`search_messages`)

**Endpoint:** `/api/capabilities/search_messages`

Searches message history across the Slack workspace.

**Request:**
```json
{
  "query": "incident deploy",
  "count": 20,
  "sort": "timestamp"
}
```

**Response:**
```json
{
  "query": "incident deploy",
  "matches": [
    {
      "channel": "ops-alerts",
      "text": "Incident during deploy of api-gateway v2.3.0 - rolling back",
      "username": "jane.smith",
      "timestamp": "1503435956.000247",
      "permalink": "https://myworkspace.slack.com/archives/C123/p1503435956000247"
    }
  ],
  "total_count": 42,
  "source": "Slack Web API"
}
```

---

## Architecture

```
Slack Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Authenticates via Bot User OAuth Token (Bearer token)
    +-- Calls Slack Web API (chat.postMessage, conversations.list, search.messages)
    +-- Maps Slack JSON-body errors to HTTP status codes
    +-- Returns standardized responses

Agents (Active)
    |
    +-- Discover Slack tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the Slack tool is automatically discovered by agents via Redis. Agents can then invoke its capabilities through orchestration. You can also query the tool directly:

```bash
# Direct tool access
curl -X POST http://localhost:8363/api/capabilities/search_messages \
  -H "Content-Type: application/json" \
  -d '{
    "query": "incident postmortem",
    "count": 5
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `SLACK_BOT_TOKEN` | Slack Bot User OAuth Token (starts with `xoxb-`) | - | Yes |
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8363` | No |
| `NAMESPACE` | Kubernetes namespace | — (set to the pod's namespace by the manifest's downward API) | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `APP_ENV` | Environment profile (development\|staging\|production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

### Required Bot Token Scopes

| Scope | Purpose |
|-------|---------|
| `chat:write` | Send messages as the bot |
| `chat:write.public` | Send messages to channels the bot isn't a member of |
| `channels:read` | List public channels in the workspace |
| `search:read` | Search messages across the workspace |

---

## API Rate Limits

Slack Web API rate limits:

| Tier | Rate Limit | Applies To |
|------|-----------|------------|
| **Tier 2** | ~20 requests/minute | `chat.postMessage` |
| **Tier 2** | ~20 requests/minute | `conversations.list` |
| **Tier 2** | ~20 requests/minute | `search.messages` |

The tool implements:
- Bearer token authentication with Bot User OAuth Token
- Traced HTTP client for all API calls (OpenTelemetry spans)
- Custom Slack error-to-HTTP-status mapping (Slack always returns HTTP 200; errors are in the JSON body)
- Structured error logging for rate limit tracking
- Graceful error responses on API failures

---

## Project Structure

```
slack-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── slack_tool.go            # Tool definition, capability registration, types
├── slack_client.go          # Slack Web API client (4 endpoints)
├── handlers.go             # HTTP handlers for each capability
├── go.mod                  # Go module definition
├── Dockerfile              # Standalone container image
├── Dockerfile.workspace    # Development build from workspace root
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

# Should show: truvag3:service:slack-tool-service
```

**2. Authentication errors (401 / `invalid_auth`)**

```bash
# Check logs for auth issues
./setup.sh logs | grep -i "auth\|invalid_auth\|not_authed"

# Common issues:
# - Invalid bot token: Regenerate at https://api.slack.com/apps -> OAuth & Permissions
# - Token doesn't start with xoxb-: Ensure you're using the Bot User OAuth Token, not a User Token
# - Token revoked: Reinstall the app to your workspace
```

**3. Permission errors (403 / `missing_scope`)**

```bash
# Check logs for permission issues
./setup.sh logs | grep -i "missing_scope\|not_in_channel\|ekm_access_denied"

# Common issues:
# - Missing bot token scopes: Add chat:write, chat:write.public, channels:read, search:read
# - Channel is archived: Cannot post to archived channels
# - EKM restrictions: Enterprise Key Management may block API access
```

**4. Channel not found errors (404 / `channel_not_found`)**

```bash
# Common issues:
# - Wrong channel ID: Use the channel ID (C123ABC456), not the channel name
# - Private channel: Bot can only access public channels unless invited to private ones
# - Channel deleted: Verify the channel exists in your workspace
```

**5. Docker build fails**

```bash
# Ensure Docker is running
docker info
```

**6. Kind cluster not found**

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

# Port forward the tool to localhost:8363
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
# Test send message
curl -X POST http://localhost:8363/api/capabilities/send_message \
  -H "Content-Type: application/json" \
  -d '{"channel": "C123ABC456", "text": "Hello from TruvaG3!"}'

# Test list channels
curl -X POST http://localhost:8363/api/capabilities/list_channels \
  -H "Content-Type: application/json" \
  -d '{"limit": 10, "exclude_archived": true}'

# Test search messages
curl -X POST http://localhost:8363/api/capabilities/search_messages \
  -H "Content-Type: application/json" \
  -d '{"query": "incident deploy", "count": 5}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export SLACK_BOT_TOKEN="your-slack-bot-token"
export REDIS_URL="redis://localhost:6379"
export PORT=8363

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `slack_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add Slack client method in `slack_client.go` if needed

---

## Related Examples

- [devops-chat-agent](../devops-chat-agent/) - DevOps chat agent that can use this tool
- [agent-with-orchestration](../agent-with-orchestration/) - Orchestration example
- [jira-tool](../jira-tool/) - JIRA issue management tool (similar pattern)
- [web-search-tool](../web-search-tool/) - Web search tool
- [news-tool](../news-tool/) - News aggregation tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
