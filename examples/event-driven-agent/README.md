# Event-Driven Agent

A production-ready event-driven incident response agent that receives Prometheus AlertManager webhooks and orchestrates autonomous investigation and remediation using AI-driven DAG planning. This example demonstrates the event-driven architecture pattern in the Truva-G3 framework, including async event queues, severity-based routing, deduplication, and human-in-the-loop approval for critical write operations.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Overview](#overview)
- [What You'll Learn](#what-youll-learn)
- [Architecture](#architecture)
- [Event Processing Pipeline](#event-processing-pipeline)
- [Deployment Modes](#deployment-modes)
- [Metrics Collected](#metrics-collected)
- [AlertManager Integration](#alertmanager-integration)
- [Human-in-the-Loop (HITL)](#human-in-the-loop-hitl)
- [Configuration Reference](#configuration-reference)
- [E2E Stress Test (HITL Demo)](#e2e-stress-test-hitl-demo)
  - [Test Scenario](#test-scenario)
  - [Running the Stress Test](#running-the-stress-test)
  - [Dedup and Flood Prevention](#dedup-and-flood-prevention)
- [Troubleshooting](#troubleshooting)
- [Related Examples](#related-examples)

---

## How to Run This Example

Running this example locally is the best way to understand how the Truva-G3 framework supports event-driven agent patterns with AlertManager webhook integration, async task processing, and AI-powered incident investigation. Follow the steps below to get this example running.

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

**Using MacPorts:**
```bash
sudo port selfupdate && sudo port install kind
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

**Using Scoop:**
```powershell
scoop bucket add main
scoop install main/kind
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

**Manual binary installation (Apple Silicon):**
```bash
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/darwin/arm64/kubectl"
chmod +x ./kubectl
sudo mv ./kubectl /usr/local/bin/kubectl
```

**Manual binary installation (Intel):**
```bash
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/darwin/amd64/kubectl"
chmod +x ./kubectl
sudo mv ./kubectl /usr/local/bin/kubectl
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

**Using Winget:**
```powershell
winget install -e --id Kubernetes.kubectl
```

**Manual binary installation:**
```powershell
# Download kubectl
curl.exe -LO "https://dl.k8s.io/release/v1.31.0/bin/windows/amd64/kubectl.exe"

# Move to a directory in your PATH
Move-Item .\kubectl.exe C:\Windows\System32\kubectl.exe
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
# Add Kubernetes apt repository
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates curl gnupg

curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.31/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
sudo chmod 644 /etc/apt/keyrings/kubernetes-apt-keyring.gpg

echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.31/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list
sudo chmod 644 /etc/apt/sources.list.d/kubernetes.list

sudo apt-get update
sudo apt-get install -y kubectl
```

**Using snap:**
```bash
sudo snap install kubectl --classic
```

**Manual binary installation:**
```bash
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x ./kubectl
sudo mv ./kubectl /usr/local/bin/kubectl
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

**Manual installation:**
1. Download the macOS installer from [go.dev/dl](https://go.dev/dl/)
2. Open the downloaded `.pkg` file
3. Follow the installation prompts

**Verify installation:**
```bash
go version
# Expected: go version go1.25.x darwin/arm64 (or darwin/amd64)
```

**Set up Go workspace (if not using modules):**
```bash
# Add to ~/.zshrc or ~/.bash_profile
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin
```

</details>

<details>
<summary><strong>Windows Installation</strong></summary>

**Using the MSI installer (recommended):**
1. Download the Windows installer from [go.dev/dl](https://go.dev/dl/)
2. Run the `.msi` installer
3. Follow the installation wizard
4. The installer sets PATH automatically

**Using Chocolatey:**
```powershell
choco install golang
```

**Verify installation:**
```powershell
go version
# Expected: go version go1.25.x windows/amd64
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Manual installation (recommended for latest version):**
```bash
# Download Go (replace version as needed)
curl -LO https://go.dev/dl/go1.25.linux-amd64.tar.gz

# Remove any previous installation and extract
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.linux-amd64.tar.gz

# Add to PATH (add to ~/.bashrc or ~/.profile for persistence)
export PATH=$PATH:/usr/local/go/bin
```

**Using apt (may not have latest version):**
```bash
sudo apt update
sudo apt install golang-go
```

**Using snap:**
```bash
sudo snap install go --classic
```

**Verify installation:**
```bash
go version
# Expected: go version go1.25.x linux/amd64
```

</details>

---

#### 5. AI Provider API Key (Required)

This agent requires at least one AI provider API key for intelligent orchestration and analysis.

| Provider | Get API Key | Notes |
|----------|-------------|-------|
| **OpenAI** | [platform.openai.com/api-keys](https://platform.openai.com/api-keys) | GPT-4o recommended |
| **Anthropic** | [console.anthropic.com](https://console.anthropic.com/) | Claude models |
| **Groq** | [console.groq.com/keys](https://console.groq.com/keys) | Fast inference, free tier |

**Auto-detection priority:** The agent automatically detects and uses the first available provider.

**Multiple providers enable automatic failover** - if one provider fails, the agent tries the next.

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

> **Important:** This example deploys the agent along with Prometheus AlertManager for real webhook-driven alert processing. The setup script handles all infrastructure deployment automatically, including AlertManager configuration.

### Quick Start (Recommended)

The fastest way to get everything running in your local:

```bash
cd examples/event-driven-agent

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**Stop here** - Open `.env` in your editor and configure your API key(s):

```bash
# Open .env in your preferred editor
nano .env    # or: code .env / vim .env
```

At minimum, uncomment and set ONE of these in your `.env` file:
- `OPENAI_API_KEY=sk-your-key`
- `ANTHROPIC_API_KEY=sk-ant-your-key`
- `GROQ_API_KEY=gsk_your-key`

> **Note:** Multiple providers enable automatic failover.

```bash
# 2. Run the automated setup script (full deployment)
./setup.sh full-deploy
```

**What `./setup.sh full-deploy` does:**
1. Creates a Kind Kubernetes cluster with proper port mappings
2. Deploys shared monitoring infrastructure (Redis, OTEL Collector, Prometheus, Jaeger, Grafana)
3. Deploys Prometheus AlertManager with routing rules for Truva-G3 alerts
4. Builds and deploys the event-driven agent
5. Deploys the stress-test-api mock service (for E2E HITL testing)
6. Sets up port forwarding automatically

Once complete, access the services at:

| Service | URL | Description |
|---------|-----|-------------|
| **Agent API** | http://localhost:8372 | Event-driven agent REST API |
| **AlertManager** | http://localhost:9093 | AlertManager UI |
| **Grafana** | http://localhost:3000 | Metrics dashboard (admin/admin) |
| **Prometheus** | http://localhost:9090 | Metrics queries |
| **Jaeger** | http://localhost:16686 | Distributed tracing |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Configure Environment

```bash
cd examples/event-driven-agent

# Create .env from example (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
# Edit .env and uncomment/set your AI provider key(s)
```

#### Step 2: Deploy Infrastructure

```bash
cd examples/k8-deployment
./setup-infrastructure.sh

# The script will:
# - Check if infrastructure components already exist
# - Skip deployment if they're healthy
# - Deploy only what's missing
# - Never delete existing resources
```

This deploys the shared infrastructure components:
- **Redis** - Service discovery, event queue, and deduplication
- **OTEL Collector** - Telemetry aggregation
- **Prometheus** - Metrics storage
- **Jaeger** - Distributed tracing
- **Grafana** - Visualization dashboards

#### Step 3: Build and Deploy the Agent

```bash
cd examples/event-driven-agent

# Build Docker image
./setup.sh docker-build

# Deploy to Kubernetes (includes AlertManager)
./setup.sh deploy
```

#### Step 4: Verify Deployment

```bash
kubectl get pods -n truvag3-examples
kubectl logs -f deployment/event-driven-agent -n truvag3-examples
```

#### Local Development (Without Kubernetes)

If you prefer to run without Kubernetes (ensure `.env` is configured and Redis is running):

```bash
cd examples/event-driven-agent

# Ensure Redis is running locally
export REDIS_URL=redis://localhost:6379
export APP_ENV=development
go run .
```

Test the agent:
```bash
# Trigger a manual alert investigation
curl -X POST http://localhost:8372/trigger \
  -H "Content-Type: application/json" \
  -d '{
    "alertname": "HighCPU",
    "severity": "critical",
    "instance": "web-server-01",
    "summary": "CPU usage above 90% for 5 minutes"
  }'
```

---

## API Reference

The agent exposes the following endpoints:

### Health & Discovery

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check with Redis, orchestrator, AI provider, HITL, and queue status |
| `/api/capabilities` | GET | List all registered capabilities |

### Capabilities

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/webhook/alertmanager` | POST | Receives AlertManager webhook payloads for automated incident response |
| `/trigger` | POST | Manually triggers an alert investigation for testing or CLI integration |
| `/events` | GET | Returns recent alert event history with queue status |

### Async Task API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/tasks` | POST | Submit a new async task (e.g., `alert_investigation`) |
| `/api/v1/tasks/{id}` | GET | Get task status and results |
| `/api/v1/tasks/{id}/cancel` | POST | Cancel a running task |

### Example Requests

**Health Check:**
```bash
curl http://localhost:8372/health
```

**Sample Response:**
```json
{
  "status": "healthy",
  "timestamp": 1709568000,
  "service": "event-driven-agent",
  "redis": "healthy",
  "queue_depth": 0,
  "orchestrator": {
    "status": "active",
    "total_requests": 12,
    "successful_requests": 10,
    "failed_requests": 2
  },
  "ai_provider": "connected",
  "hitl": "active"
}
```

**Manual Alert Trigger (Critical):**
```bash
curl -X POST http://localhost:8372/trigger \
  -H "Content-Type: application/json" \
  -d '{
    "alertname": "TruvaG3ComponentDown",
    "severity": "critical",
    "instance": "stock-market-tool-xyz:8348",
    "summary": "Truva-G3 component truvag3-tools is down"
  }'
```

**Sample Response:**
```json
{
  "status": "ok",
  "alertname": "TruvaG3ComponentDown",
  "severity": "critical",
  "enqueued": true,
  "fingerprint": "manual-TruvaG3ComponentDown-1709568000000000000"
}
```

**AlertManager Webhook (simulating AlertManager):**
```bash
curl -X POST http://localhost:8372/webhook/alertmanager \
  -H "Content-Type: application/json" \
  -d '{
    "version": "4",
    "status": "firing",
    "alerts": [
      {
        "status": "firing",
        "labels": {
          "alertname": "HighCPU",
          "severity": "critical",
          "instance": "web-server-01"
        },
        "annotations": {
          "summary": "CPU usage above 90% for 5 minutes"
        },
        "startsAt": "2026-03-04T10:00:00Z",
        "fingerprint": "abc123"
      }
    ]
  }'
```

**Event History:**
```bash
curl http://localhost:8372/events
```

**List Capabilities:**
```bash
curl http://localhost:8372/api/capabilities
```

---

## Overview

This example demonstrates an event-driven agent pattern that is fundamentally different from request-response agents:

- **Webhook-Driven**: Receives Prometheus AlertManager webhooks instead of user-initiated requests
- **Async Event Queue**: Critical alerts are enqueued in Redis and processed by background workers
- **Severity-Based Routing**: Critical alerts trigger AI investigation, warnings send Slack notifications, info alerts are logged
- **Fingerprint Deduplication**: Prevents duplicate investigations for the same alert within a configurable TTL window
- **AI-Powered Investigation**: Uses the Truva-G3 orchestration module with DAG planning to investigate incidents
- **Human-in-the-Loop**: Write operations (pod restarts, scaling, deletions) require human approval before execution
- **Multi-Mode Deployment**: Supports embedded (single process), split API/worker, or standalone worker modes

> **Scope**: This example focuses on **event-driven patterns and incident response**. For basic agent patterns, see [agent-example](../agent-example). For telemetry-focused patterns, see [agent-with-telemetry](../agent-with-telemetry/).

---

## What You'll Learn

- How to build an event-driven agent that processes AlertManager webhooks
- Implementing async event queues with Redis (LPUSH/BRPOP) for reliable alert processing
- Severity-based routing: critical (AI investigation), warning (Slack notification), info (log only)
- Fingerprint-based deduplication to avoid redundant investigations
- Configuring the AI orchestrator with incident-response domain prompts
- Human-in-the-loop approval workflows for write operations (pod restarts, scaling)
- Deploying agents in different modes (embedded, API-only, worker-only)
- Integrating Prometheus AlertManager with a Truva-G3 agent via webhook receivers

---

## Architecture

```
                           AlertManager Webhook
                                   |
                                   v
┌──────────────────────────────────────────────────────────────┐
│  Event-Driven Agent (Port 8372)                              │
│                                                              │
│  ┌─────────────────┐    ┌──────────────────────────────────┐ │
│  │ Webhook Receiver │    │  Deterministic Pipeline          │ │
│  │ /webhook/        │───>│  parse -> severity -> dedup      │ │
│  │   alertmanager   │    │      |          |         |      │ │
│  └─────────────────┘    │  critical   warning     info     │ │
│                          │      |          |         |      │ │
│  ┌─────────────────┐    │  enqueue    Slack      log       │ │
│  │ Manual Trigger   │    │      |     notify     only      │ │
│  │ /trigger         │───>│      v                          │ │
│  └─────────────────┘    └──────|───────────────────────────┘ │
│                                 |                             │
│                                 v                             │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │  Redis Alert Queue (LPUSH/BRPOP)                         │ │
│  └───────────────────────────|──────────────────────────────┘ │
│                              |                                │
│                              v                                │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │  Worker Pool (3 workers)                                  │ │
│  │  ┌────────────────────────────────────────────────────┐  │ │
│  │  │  AI Pipeline                                       │  │ │
│  │  │  context enrichment -> orchestrator -> HITL        │  │ │
│  │  │       |                    |              |        │  │ │
│  │  │  build query        DAG planning    human approval │  │ │
│  │  │                     tool execution  (write ops)    │  │ │
│  │  │                     AI synthesis                   │  │ │
│  │  └────────────────────────────────────────────────────┘  │ │
│  └──────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
         |                    |                    |
         v                    v                    v
  ┌──────────┐        ┌──────────┐         ┌──────────┐
  │  Redis   │        │  Tools   │         │  Slack   │
  │  (dedup, │        │  (DevOps,│         │  Webhook │
  │   queue) │        │   Jira,  │         │          │
  └──────────┘        │  Metrics)│         └──────────┘
                      └──────────┘
```

---

## Event Processing Pipeline

The agent uses a two-stage pipeline that separates deterministic routing from AI-driven investigation:

### Stage 1: Deterministic Pipeline (Webhook Receiver)

This stage is fast, predictable, and handles all incoming alerts synchronously:

1. **Parse**: Decode the AlertManager JSON payload
2. **Filter**: Skip `resolved` alerts (informational only)
3. **Severity Route**:
   - `critical` -- proceed to dedup and enqueue
   - `warning` -- send Slack notification directly (no AI)
   - `info` -- log only
4. **Dedup Check**: Redis `SET NX` with fingerprint key and configurable TTL (default 5 minutes)
5. **Enqueue**: `LPUSH` the serialized alert onto the Redis queue

### Stage 2: AI Pipeline (Worker Pool)

Background workers `BRPOP` alerts from the queue and run AI-driven investigation:

1. **Context Enrichment**: Build a natural language query from alert labels and annotations
2. **Orchestrator**: The AI planner creates a DAG of tool calls (check metrics, get logs, check pod status)
3. **Tool Execution**: Execute the DAG steps, calling discovered tools in the registry
4. **HITL Gate**: Write operations (restart, scale, delete) pause for human approval
5. **Synthesis**: AI summarizes findings and remediation actions
6. **Cleanup**: Remove the dedup key so the alert can be re-investigated if it fires again

---

## Deployment Modes

The agent supports three deployment modes controlled by the `TRUVAG3_MODE` environment variable. All modes use the same Docker image — the mode is determined at runtime.

| Mode | `TRUVAG3_MODE` | Description | Use Case |
|------|---------------|-------------|----------|
| **Embedded** | `""` (empty/unset) | API server + workers in one process | Local development, simple deployments |
| **API** | `"api"` | HTTP server only, no workers | Scale API and workers independently |
| **Worker** | `"worker"` | Workers only, minimal health endpoint | Scale workers horizontally |

### Embedded Mode (Default)

Best for local development. Runs the HTTP server and worker pool in the same process:

```bash
./setup.sh deploy    # or: TRUVAG3_MODE= ./event-driven-agent
```

### Split Mode (Production)

Deploys the API and workers as separate Kubernetes pods for independent scaling:

```bash
./setup.sh deploy-split     # Deploy API + Worker as separate pods
./setup.sh deploy-embedded  # Switch back to single-pod embedded mode
```

**What each pod does:**

| Pod | Responsibilities |
|-----|-----------------|
| **API** (`event-driven-agent-api`) | Receives AlertManager webhooks, enqueues tasks to Redis, handles HITL resume requests, serves health/readiness probes. No AI processing. |
| **Worker** (`event-driven-agent-worker`) | Dequeues tasks from Redis, runs the full AI orchestration pipeline (LLM planning, DAG execution, tool calls, iterative phases, HITL checkpoints). This is where all compute happens. |

**How they connect:**

```
AlertManager ──webhook──▶ API pod ──Redis queue──▶ Worker pod
                              ▲                        │
                              │                        │ (tool calls)
                    HITL resume (HTTP)            ▼
                              │               devops-tool, jira-tool,
                    Registry Viewer UI         slack-tool, prometheus, ...
```

- The K8s Service (`event-driven-agent`) routes to the **API pod** — AlertManager and HITL resume webhooks always reach the API.
- The Worker pod has its own Service (`event-driven-agent-worker-service`) for health checks and metrics scraping only.
- Both pods share the same ConfigMap (`event-driven-agent-env-config`) for API keys, timeouts, and HITL settings.
- The Worker uses `TRUVAG3_HITL_WEBHOOK_URL` pointing to the API Service so that HITL checkpoint notifications reach the API pod.

**Manifests:**

| File | Contents |
|------|----------|
| `k8-deployment.yaml` | Embedded mode (single pod) |
| `k8-deployment-api.yaml` | API pod for split mode |
| `k8-deployment-worker.yaml` | Worker pod for split mode |

---

## Metrics Collected

| Metric | Type | Description | Labels |
|--------|------|-------------|--------|
| `event_agent.alerts_received` | Counter | Total alerts received from AlertManager | severity, alertname |
| `event_agent.alerts_deduplicated` | Counter | Alerts skipped due to deduplication | alertname |
| `event_agent.alerts_enqueued` | Counter | Alerts enqueued for AI investigation | severity |
| `event_agent.alerts_processed` | Counter | Alerts processed by worker pool | status |
| `event_agent.processing_duration_ms` | Histogram | End-to-end investigation duration | - |
| `event_agent.slack_notifications` | Counter | Slack notifications sent for warnings | status |

---

## AlertManager Integration

The agent includes Kubernetes manifests for deploying Prometheus AlertManager pre-configured to route Truva-G3 alerts to the agent's webhook endpoint.

### Included Files

- **`alertmanager-config.yaml`**: ConfigMap with AlertManager routing rules. Routes alerts matching `^Truva-G3.*` to the agent's webhook at `http://event-driven-agent.truvag3-examples:80/webhook/alertmanager`.
- **`alertmanager.yaml`**: Deployment, Service, and NodePort for the AlertManager instance.

### Routing Rules

```yaml
routes:
  - match_re:
      alertname: '^Truva-G3.*'
    receiver: 'truvag3-event-agent'
    group_wait: 10s
    group_interval: 1m
    repeat_interval: 5m
```

Alerts with names matching `Truva-G3*` are grouped by `alertname` and `severity`, with a 10-second initial wait before the first notification. Non-matching alerts go to a default no-op receiver.

### Testing Without AlertManager

You can test the agent without AlertManager deployed by using the manual trigger endpoint:

```bash
curl -X POST http://localhost:8372/trigger \
  -H "Content-Type: application/json" \
  -d '{"alertname": "HighCPU", "severity": "critical", "instance": "web-01", "summary": "CPU above 90%"}'
```

Or by sending an AlertManager-formatted payload directly to the webhook:

```bash
curl -X POST http://localhost:8372/webhook/alertmanager \
  -H "Content-Type: application/json" \
  -d '{"version":"4","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"HighCPU","severity":"critical"},"annotations":{"summary":"CPU above 90%"},"fingerprint":"test123"}]}'
```

---

## Human-in-the-Loop (HITL)

Write operations (pod restarts, scaling, deletions, Slack messages, JIRA ticket creation) are gated by human-in-the-loop approval. When the orchestrator encounters a sensitive capability, execution pauses and returns a checkpoint ID for approval.

### Configuration

HITL is configured via environment variables (loaded by `orchestration.DefaultConfig()`):

```bash
# Enable HITL
TRUVAG3_HITL_ENABLED=true

# Plan-level approval (approve the entire DAG before execution)
TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=false

# Step-level approval for these capabilities
TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=rollout_restart,scale_deployment,delete_pod,send_message,create_ticket

# Approval timeout
TRUVAG3_HITL_DEFAULT_TIMEOUT=5m
```

### Approval Flow

1. Worker begins alert investigation
2. AI planner creates a DAG (e.g., check metrics -> get logs -> restart pod -> create JIRA)
3. When the `restart pod` step is reached, HITL pauses execution
4. A checkpoint is stored in Redis (DB 6) with the execution state
5. The task result includes `"status": "pending_approval"` and a `checkpoint_id`
6. A human approves or rejects via the registry-viewer-app or API
7. On approval, execution resumes from the checkpoint; on rejection or timeout, the investigation completes without the write operation

---

## Configuration Reference

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 8372 | HTTP server port |
| `REDIS_URL` | - | Redis connection URL (required) |
| `TRUVAG3_MODE` | `""` (embedded) | Deployment mode: `api`, `worker`, or empty for embedded |
| `WORKER_COUNT` | 3 | Number of background workers |
| `OPENAI_API_KEY` | - | OpenAI API key |
| `ANTHROPIC_API_KEY` | - | Anthropic API key |
| `GROQ_API_KEY` | - | Groq API key |
| `APP_ENV` | development | Telemetry profile (development/staging/production) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | http://localhost:4318 | OTEL Collector endpoint |
| `SLACK_WEBHOOK_URL` | - | Slack webhook URL for warning-level notifications |
| `EVENT_AGENT_DEDUP_TTL` | 300 | Deduplication TTL in seconds |
| `TRUVAG3_HITL_ENABLED` | true | Enable human-in-the-loop approval |
| `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | false | Require approval for entire execution plan |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | - | Comma-separated list of capabilities requiring step-level approval |
| `TRUVAG3_HITL_DEFAULT_TIMEOUT` | 5m | Timeout for approval requests |
| `TRUVAG3_LLM_DEBUG_ENABLED` | true | Enable LLM debug store (Redis DB 7) |
| `TRUVAG3_LOG_LEVEL` | info | Log level (debug/info/warn/error) |

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
# Safe copy - won't overwrite existing .env
[ ! -f .env ] && cp .env.example .env
```

The `.env.example` file contains comprehensive documentation for all options including:

- **AI Provider Keys** - Supports provider chain for failover
- **Service Configuration** - Port, Redis URL, deployment mode, worker count
- **Event Agent Configuration** - Dedup TTL, Slack webhook
- **HITL Configuration** - Approval settings for write operations
- **Telemetry Configuration** - Environment profiles and OTLP endpoints

### Setup Script Commands

```bash
# Local Development
./setup.sh build          # Build the agent binary
./setup.sh run            # Build and run the agent locally

# Kubernetes Cluster
./setup.sh cluster        # Create Kind cluster with port mappings
./setup.sh infra          # Setup monitoring infrastructure + AlertManager
./setup.sh full-deploy    # Complete deployment: cluster + infra + agent + stress-test-api + port forwards

# Kubernetes Deployment
./setup.sh docker-build     # Build Docker image using local workspace modules
./setup.sh deploy           # Build, load, and deploy to Kubernetes (embedded mode)
./setup.sh rebuild          # Rebuild with --no-cache and redeploy
./setup.sh deploy-split     # Deploy in split mode (separate API + Worker pods)
./setup.sh deploy-embedded  # Switch back to embedded mode (single pod)
./setup.sh test             # Run test requests against deployed agent
./setup.sh forward          # Port forward agent service only
./setup.sh forward-all      # Port forward agent + AlertManager + monitoring
./setup.sh logs             # View agent logs
./setup.sh status           # Check deployment status (agent + AlertManager + stress-test-api + monitoring)
./setup.sh rollout          # Restart deployment to pick up new secrets/config

# Stress Test (E2E HITL)
./setup.sh stress-test deploy      # Deploy stress-test-api mock service
./setup.sh stress-test rebuild     # Rebuild stress-test-api from scratch
./setup.sh stress-test stress-on   # Enable stress mode (triggers alert → HITL flow)
./setup.sh stress-test stress-off  # Disable stress mode
./setup.sh stress-test status      # Show stress-test-api pod and stress state
./setup.sh stress-test logs        # Tail stress-test-api logs
./setup.sh stress-test clean       # Remove stress-test-api deployment

# Cleanup
./setup.sh clean          # Remove agent, AlertManager, and stress-test-api deployments
./setup.sh clean-all      # Delete Kind cluster and all resources
```

---

## E2E Stress Test (HITL Demo)

This section describes how to run a realistic end-to-end test that exercises the full alert pipeline: baseline traffic, performance degradation, gradual memory leak, Prometheus alert firing, AI-driven investigation, and human-in-the-loop approval for destructive remediation.

### Overview

A mock microservice ([product-catalog-api](../mock-services/product-catalog-api/)) runs in the cluster with Prometheus metrics, simulating a realistic production service. It serves business endpoints (`/api/v1/products`, `/api/v1/categories`) with ~100-200ms baseline latency (simulating DB queries). On command, it degrades: response times spike to 1.2-2.0s and memory leaks gradually (~2MB/sec up to 40MB). Prometheus detects the elevated P90 latency and fires the `TruvaG3HighLatency` alert, which AlertManager routes to the event-driven-agent for AI investigation.

### Test Scenario

The test is designed in phases to produce realistic Grafana dashboards:

| Phase | Duration | Traffic | Service State | What Grafana Shows |
|-------|----------|---------|---------------|--------------------|
| **1. Baseline** | ~30 min | Normal (~3 req/s) | Healthy (100-200ms) | Flat, stable response time and memory |
| **2. Load spike** | ~2 min | Heavy (~30 req/s) | Healthy (100-200ms) | Request rate spike, latency stays flat |
| **3. Degradation** | ongoing | Heavy (~30 req/s) | Degraded (1.2-2.0s latency, memory leak ~2MB/s) | Latency jumps 8-10x, memory ramp in Grafana |
| **4. Alert fires** | T+90s | Heavy | Degraded | Prometheus alert transitions to `firing` |
| **5. AI investigation** | ~30-60s | Heavy | Degraded | Agent queries metrics, pods, logs |
| **6. HITL gate** | until approved | Heavy | Degraded | `rollout_restart` awaits human approval |

### Pipeline

```
product-catalog-api         Prometheus              AlertManager         event-driven-agent
     |                           |                        |                       |
     |-- normal load (3 req/s)-->|                        |                       |
     |   baseline: 100-200ms     |                        |                       |
     |   (30 min)                |                        |                       |
     |                           |                        |                       |
     |-- heavy load (30 req/s)-->|                        |                       |
     |                           |                        |                       |
     |-- POST /admin/simulate/ ->|                        |                       |
     |        degrade            |                        |                       |
     |   (latency: 1.2-2.0s,    |                        |                       |
     |    memory: +2MB/sec)      |                        |                       |
     |                           |                        |                       |
     |<-- scrape /metrics -------|                        |                       |
     |   P90 latency > 0.8s      |                        |                       |
     |                           |-- after 1m for ------->|                       |
     |                           |   TruvaG3HighLatency    |                       |
     |                           |   severity: critical   |                       |
     |                           |                        |-- webhook POST ------>|
     |                           |                        |   /webhook/alertmanager
     |                           |                        |                       |
     |                           |                        |      AI investigation |
     |                           |                        |      query_metrics    |
     |                           |                        |      get_pods         |
     |                           |                        |      get_pod_logs     |
     |                           |                        |      rollout_restart  |
     |                           |                        |          |            |
     |                           |                        |      HITL gate        |
     |                           |                        |      (awaiting        |
     |                           |                        |       approval)       |
```

### What Makes This Realistic

- **Realistic service identity**: The service is named `product-catalog-api` with business endpoints (`/api/v1/products`, `/api/v1/categories`). Log messages show "Connection pool degradation detected" and "GC pressure increasing". The AI agent cannot infer the degradation is intentional.
- **Realistic baseline latency**: Normal requests take 100-200ms (simulating DB queries), not instant. The degradation to 1.2-2.0s is an 8-10x increase — believable for connection pool exhaustion.
- **Gradual memory leak**: Memory grows at ~2MB/sec (step-wise allocation, page-touched) up to 40MB total. This produces a realistic ramp in Grafana, not a single spike.
- **Real Prometheus alert**: The `TruvaG3HighLatency` rule evaluates `histogram_quantile(0.90, ...)` with `sum by (le, pod, namespace, app, job, instance)` to produce exactly one alert per pod.
- **Real AlertManager routing**: Alerts matching `alertname: TruvaG3HighLatency` (exact match, not regex) are routed to the event-driven-agent's `/webhook/alertmanager` endpoint with `repeat_interval: 15m`.
- **Dedup TTL safety**: `TRUVAG3_EVENT_DEDUP_TTL=1800` (30 min) >> AlertManager `repeat_interval` (15 min), preventing duplicate investigations from AlertManager resends.
- **Real AI investigation**: The agent queries actual Prometheus metrics, inspects real pods, reads real logs, and plans remedial actions.
- **Real HITL gate**: Only destructive operations (`rollout_restart`, `scale_deployment`, `delete_pod`) require human approval.

### Running the Stress Test

**Prerequisites:** A running cluster with `./setup.sh full-deploy` completed.

```bash
cd examples/event-driven-agent

# ── Phase 1: Establish baseline (30 minutes) ──────────────────────
# Start normal traffic (~3 req/s) to establish a flat baseline in Grafana.
./setup.sh mock-service normal-load
# Monitor Grafana at http://localhost:3000 — look for stable ~150ms response time.
# Let this run for ~30 minutes to build a clear baseline.

# ── Phase 2: Increase load ────────────────────────────────────────
# In a second terminal, start heavy traffic (~30 req/s).
# Stop the normal-load first (Ctrl+C), then:
./setup.sh mock-service heavy-load

# ── Phase 3: Trigger degradation ──────────────────────────────────
# After ~2 minutes of heavy load, trigger the degradation.
# This injects 1.2-2.0s latency and starts a gradual memory leak (~2MB/sec).
./setup.sh mock-service degrade

# ── Phase 4: Wait for alert (~90 seconds) ─────────────────────────
# Monitor in Prometheus: http://localhost:9090/alerts
# Monitor in Grafana:    http://localhost:3000 (Product Catalog API dashboards)
# The TruvaG3HighLatency alert will transition: inactive → pending → firing

# ── Phase 5: Watch the AI investigation ───────────────────────────
./setup.sh logs
# The agent will: query_metrics → get_pods → get_pod_logs → plan rollout_restart

# ── Phase 6: Approve or reject the HITL checkpoint ────────────────
# Via Registry Viewer App UI: http://localhost:8361
# Or via CLI:
#   curl -X POST http://localhost:8372/hitl/command \
#     -H "Content-Type: application/json" \
#     -d '{"checkpoint_id": "<id>", "type": "approve"}'

# ── Cleanup ───────────────────────────────────────────────────────
# Stop the heavy load (Ctrl+C in that terminal), then recover the service:
./setup.sh mock-service recover
```

### Grafana Dashboards

Two dashboards are provisioned for monitoring the test:

| Dashboard | Description |
|-----------|-------------|
| **Product Catalog API - Response Time** | P50/P90/P95 response time (ms), request rate, process RSS, alert status |
| **Product Catalog API - Resources** | CPU usage (cAdvisor), container memory vs process RSS, combined CPU/memory view, alert status |

### Memory Safety

The product-catalog-api container has an 80Mi memory limit. The Go binary uses ~13MB RSS at rest. The gradual memory leak allocates 2MB chunks per second up to 40MB total (~53MB peak RSS), safely under the 80Mi (84MB) limit. No OOM kills will occur.

When recovery is triggered (`/admin/simulate/recover`), the ballast is released and `runtime.GC()` is called to return memory to the OS.

### Alert Rule

The `TruvaG3HighLatency` rule is defined in `examples/k8-deployment/prometheus.yaml`:

```yaml
- alert: TruvaG3HighLatency
  expr: histogram_quantile(0.90, sum by (le, pod, namespace, app, job, instance) (rate(http_request_duration_seconds_bucket{job="truvag3-mock-services"}[2m]))) > 0.8
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: "High P90 latency on {{ $labels.app }} ({{ $labels.pod }})"
    description: "P90 response time is {{ printf \"%.2f\" $value }}s on pod {{ $labels.pod }} in namespace {{ $labels.namespace }}. Normal baseline is ~150ms."
```

### AlertManager Configuration

Alerts are routed with exact match (not regex) to prevent unrelated alerts from flooding the agent:

```yaml
routes:
  - match:
      alertname: TruvaG3HighLatency
    receiver: 'truvag3-event-agent'
    group_wait: 10s
    group_interval: 1m
    repeat_interval: 15m
```

### Dedup and Flood Prevention

| Setting | Value | Purpose |
|---------|-------|---------|
| `TRUVAG3_EVENT_DEDUP_TTL` | 1800s (30 min) | Must be >> `repeat_interval` to prevent duplicate investigations |
| AlertManager `repeat_interval` | 15m | How often AlertManager resends a firing alert |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | `rollout_restart,scale_deployment,delete_pod` | Only destructive K8s operations require HITL approval |

### Expected Timeline

| Time | Event |
|------|-------|
| T-30m | Normal load started, baseline established in Grafana |
| T+0s | Heavy load started (~30 req/s) |
| T+2m | `degrade` called — latency jumps to 1.2-2.0s, memory leak begins |
| T+2m15s | First Prometheus scrape sees P90 > 0.8s, alert enters `pending` |
| T+3m15s | Alert has been pending for 1 minute, transitions to `firing` |
| T+3m25s | AlertManager `group_wait` (10s) expires, webhook sent to event-driven-agent |
| T+3m30s-4m | AI investigation begins: query_metrics, get_pods, get_pod_logs |
| T+4m-4m30s | AI plans `rollout_restart`, HITL gate pauses for approval |
| T+4m+ | Memory in Grafana shows ~4-8MB above baseline (still growing at 2MB/s) |

---

## Troubleshooting

### Alerts not being processed

1. **Check the alert queue depth**:
   ```bash
   curl http://localhost:8372/health | jq .queue_depth
   ```

2. **Verify the orchestrator is initialized**:
   ```bash
   curl http://localhost:8372/health | jq .orchestrator
   # Should show "status": "active", not "initializing"
   ```

3. **Check worker logs for errors**:
   ```bash
   kubectl logs -f deployment/event-driven-agent -n truvag3-examples | grep -i "alert_investigation"
   ```

### Orchestrator stuck in "initializing"

The orchestrator waits for Redis Discovery to become available. Check:

1. **Redis connectivity**:
   ```bash
   kubectl exec -n truvag3-examples deployment/event-driven-agent -- env | grep REDIS_URL
   ```

2. **Discovery availability** (the orchestrator polls until Discovery is ready):
   ```bash
   kubectl logs deployment/event-driven-agent -n truvag3-examples | grep -i discovery
   ```

### Alerts being deduplicated unexpectedly

The agent deduplicates alerts by fingerprint with a default 5-minute TTL:

1. **Check dedup TTL setting**:
   ```bash
   # Default is 300 seconds (5 minutes)
   echo $EVENT_AGENT_DEDUP_TTL
   ```

2. **Lower the TTL for testing**:
   ```bash
   EVENT_AGENT_DEDUP_TTL=30  # 30 seconds
   ```

### HITL checkpoint not appearing

1. **Verify HITL is enabled**:
   ```bash
   curl http://localhost:8372/health | jq .hitl
   # Should show "active"
   ```

2. **Check sensitive capabilities are configured**:
   ```bash
   echo $TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES
   ```

3. **Ensure the investigation reaches a write operation** -- read-only investigations (check metrics, get logs) will complete without HITL.

### No AI provider available

1. **Check health endpoint**:
   ```bash
   curl http://localhost:8372/health | jq .ai_provider
   # Should show "connected", not "not configured"
   ```

2. **Verify API key is set**:
   ```bash
   # At least one must be set
   echo $OPENAI_API_KEY
   echo $ANTHROPIC_API_KEY
   echo $GROQ_API_KEY
   ```

### Useful Commands

```bash
# View agent logs
kubectl logs -f deployment/event-driven-agent -n truvag3-examples

# Check pod status
kubectl get pods -n truvag3-examples -l app=event-driven-agent

# Check AlertManager status
kubectl get pods -n truvag3-examples -l app=alertmanager

# Test manual alert trigger
curl -X POST http://localhost:8372/trigger \
  -H "Content-Type: application/json" \
  -d '{"alertname": "TruvaG3ComponentDown", "severity": "critical", "instance": "test:8080", "summary": "Test alert"}'

# Check event history and queue depth
curl http://localhost:8372/events
```

---

## Related Examples

- [agent-example](../agent-example) - Basic agent without event-driven patterns
- [agent-with-async](../agent-with-async/) - Async task processing patterns
- [agent-with-human-approval](../agent-with-human-approval/) - Human-in-the-loop approval workflows
- [agent-with-orchestration](../agent-with-orchestration/) - AI orchestration with DAG planning
- [agent-with-telemetry](../agent-with-telemetry/) - Comprehensive telemetry and observability
- [agent-with-resilience](../agent-with-resilience/) - Resilience patterns (circuit breakers, retries)
- [devops-tool](../devops-tool/) - DevOps tool for pod management (used by this agent)
- [jira-tool](../jira-tool/) - JIRA integration tool (used by this agent)
- [slack-tool](../slack-tool/) - Slack integration tool (used by this agent)

---

## Learn More

- [Truva-G3 Orchestration Module](../../orchestration/README.md) - AI orchestration and DAG planning
- [Distributed Tracing Guide](../../docs/DISTRIBUTED_TRACING_GUIDE.md) - End-to-end request tracing and log correlation
- [Agent Development Guide](../../docs/AGENT_DEVELOPMENT_GUIDE.md) - Building agents with Truva-G3
- [Prometheus AlertManager Documentation](https://prometheus.io/docs/alerting/latest/alertmanager/)
- [OpenTelemetry Go Documentation](https://opentelemetry.io/docs/languages/go/)

---

## License

This example is part of the Truva-G3 framework and is licensed under the same terms.
