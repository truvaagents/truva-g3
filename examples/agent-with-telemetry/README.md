# Research Agent with Telemetry

A production-ready intelligent research agent with comprehensive observability through OpenTelemetry integration. This example demonstrates the complete telemetry capabilities of the TruvaG3 framework, including metrics and distributed tracing.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Overview](#overview)
- [What You'll Learn](#what-youll-learn)
- [Architecture](#architecture)
- [Metrics Collected](#metrics-collected)
- [Environment Profiles](#environment-profiles)
- [AI Module Distributed Tracing](#ai-module-distributed-tracing)
- [Migration Guide](#migration-guide)
- [Telemetry Best Practices](#telemetry-best-practices)
- [Key Implementation Patterns](#key-implementation-patterns)
- [Configuration Reference](#configuration-reference)
- [Troubleshooting](#troubleshooting)
- [Related Examples](#related-examples)

---

## How to Run This Example

Running this example locally is the best way to understand how the TruvaG3 telemetry module provides comprehensive observability for your agents. Follow the steps below to get this example running.

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
# Expected: go version go1.26.x darwin/arm64 (or darwin/amd64)
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
# Expected: go version go1.26.x windows/amd64
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Manual installation (recommended for latest version):**
```bash
# Download Go (replace version as needed)
curl -LO https://go.dev/dl/go1.26.4.linux-amd64.tar.gz

# Remove any previous installation and extract
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.4.linux-amd64.tar.gz

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
# Expected: go version go1.26.x linux/amd64
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

> **Important:** This example deploys a complete telemetry stack including OTEL Collector, Prometheus, Jaeger, and Grafana. The setup script handles all infrastructure deployment automatically.

### Quick Start (Recommended)

The fastest way to get everything running in your local:

```bash
cd examples/agent-with-telemetry

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**⚠️ STOP HERE** - Open `.env` in your editor and configure your API key(s):

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
2. Deploys complete monitoring infrastructure (Redis, OTEL Collector, Prometheus, Jaeger, Grafana)
3. Builds and deploys the research agent with telemetry
4. Sets up port forwarding automatically
5. Tests the deployment

Once complete, access the services at:

| Service | URL | Description |
|---------|-----|-------------|
| **Agent API** | http://localhost:8092 | Research agent REST API |
| **Grafana** | http://localhost:3000 | Metrics dashboard (admin/admin) |
| **Prometheus** | http://localhost:9090 | Metrics queries |
| **Jaeger** | http://localhost:16686 | Distributed tracing |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Configure Environment

```bash
cd examples/agent-with-telemetry

# Create .env from example (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
# Edit .env and uncomment/set your AI provider key(s)
```

#### Step 2: Ensure Infrastructure is Running

```bash
cd examples/agent-with-telemetry

./setup.sh cluster   # Create Kind cluster (skip if already up)
./setup.sh infra     # Deploy Redis + OTEL Collector + Prometheus + Jaeger + Grafana (skip if already up)
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

This brings up the shared infrastructure components:
- **Redis** - Service discovery and caching
- **OTEL Collector** - Telemetry aggregation
- **Prometheus** - Metrics storage
- **Jaeger** - Distributed tracing
- **Grafana** - Visualization dashboards

#### Step 3: Build and Deploy the Agent

`setup.sh` handles the Docker build, Kind image load, namespace + ConfigMap creation, and manifest apply. Use these subcommands instead of raw `kubectl`:

```bash
cd examples/agent-with-telemetry

# Build the Docker image only (does not deploy)
./setup.sh docker-build

# Full deploy: build + load into Kind + create namespace + ConfigMap from .env + apply manifest
./setup.sh deploy
```

> **Tip:** If you don't already have a cluster and infrastructure, `./setup.sh full-deploy` does everything from scratch in one shot — cluster, monitoring, agent, and port forwards.

#### Step 4: Verify Deployment

```bash
./setup.sh status   # Check pod / service status
./setup.sh logs     # Stream agent logs
```

#### Local Development (Without Kubernetes)

If you prefer to run without Kubernetes (ensure `.env` is configured):

```bash
cd examples/agent-with-telemetry

# For local dev, telemetry will be disabled unless OTEL_EXPORTER_OTLP_ENDPOINT is set
export APP_ENV=development
go run .
```

Test the agent:
```bash
curl -X POST http://localhost:8092/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic": "latest AI developments", "ai_synthesis": true}'
```

---

## API Reference

The agent exposes the following endpoints:

### Health & Discovery

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check with telemetry status |
| `/api/capabilities` | GET | List all registered capabilities |

### Capabilities

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/capabilities/research_topic` | POST | Research a topic using discovered tools |
| `/api/capabilities/discover_tools` | POST | Discover available tools in the registry |
| `/api/capabilities/analyze_data` | POST | Analyze data with AI synthesis |
| `/api/capabilities/orchestrate_workflow` | POST | Orchestrate multi-step workflows |

### Example Requests

**Health Check:**
```bash
curl http://localhost:8092/health
```

**Sample Response:**
```json
{
  "ai_provider": "connected",
  "redis": "healthy",
  "status": "healthy",
  "telemetry": {
    "circuit_state": "closed",
    "initialized": true,
    "metrics_emitted": 2668
  }
}
```

**Research Topic:**
```bash
curl -X POST http://localhost:8092/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic": "current weather in Paris", "ai_synthesis": true}'
```

**Sample Response:**
```json
{
  "topic": "current weather in Paris",
  "summary": "The current weather in Paris is overcast with a humidity level of 84%. The temperature is 10.7°C with moderate wind speed at 11.4 km/h.",
  "tools_used": ["weather-tool-v2"],
  "results": [
    {
      "tool_name": "weather-tool-v2",
      "capability": "get_current_weather",
      "data": {
        "condition": "Overcast",
        "humidity": 84,
        "temperature_current": 10.7,
        "wind_speed": 11.4
      },
      "success": true,
      "duration": "839.285251ms"
    }
  ],
  "confidence": 1,
  "processing_time": "5.167065752s"
}
```

**List Capabilities:**
```bash
curl http://localhost:8092/api/capabilities
```

**Discover Tools:**
```bash
curl -X POST http://localhost:8092/api/capabilities/discover_tools \
  -H "Content-Type: application/json" \
  -d '{"category": "weather"}'
```

---

## Overview

This example extends the basic [agent-example](../agent-example) with:

- **Comprehensive Metrics**: 10+ metric types tracking research operations, tool calls, AI synthesis, and discovery
- **Distributed Tracing**: End-to-end request tracing across the agent and tool ecosystem
- **Multi-Environment Profiles**: Development (100% sampling), Staging (10%), Production (0.1%)
- **Production-Ready Configuration**: Environment-based telemetry with graceful degradation

> **Scope**: This example focuses on **telemetry and observability**. For intelligent error handling with AI-powered retry and parameter correction, see [agent-with-orchestration](../agent-with-orchestration/) which uses the orchestration module.

---

## What You'll Learn

- How to integrate the TruvaG3 telemetry module into your agents
- Best practices for declaring and emitting metrics
- Configuring environment-specific telemetry profiles
- Debugging performance issues with distributed tracing
- Production deployment with Kubernetes

---

## Architecture

```
┌─────────────────────┐
│  Research Agent     │
│  (Port 8092)        │
│  ┌───────────────┐  │     ┌────────────────────┐
│  │ Telemetry     │──┼────>│ OTEL Collector     │
│  │ Module        │  │     │ (Kubernetes)       │
│  └───────────────┘  │     └─────────┬──────────┘
└─────────────────────┘               │
                                      │
                         ┌────────────┼──────────────┐
                         │            │              │
                         v            v              v
                  ┌──────────┐ ┌──────────┐  ┌──────────┐
                  │Prometheus│ │  Jaeger  │  │ Logging  │
                  │          │ │          │  │          │
                  └────┬─────┘ └────┬─────┘  └──────────┘
                       │            │
                       └─────┬──────┘
                             v
                      ┌──────────┐
                      │ Grafana  │
                      │          │
                      └──────────┘
```

---

## Metrics Collected

| Metric | Type | Description | Labels |
|--------|------|-------------|--------|
| `agent.research.duration_ms` | Histogram | Research operation duration | topic, status |
| `agent.research.requests` | Counter | Total research requests | status |
| `agent.research.tools_called` | Counter | Tools called during research | tool_name |
| `agent.tool_call.duration_ms` | Histogram | Individual tool call duration | tool |
| `agent.tool_call.success` | Counter | Successful tool calls | tool |
| `agent.tool_call.errors` | Counter | Tool call errors | tool, error_type |
| `agent.discovery.duration_ms` | Histogram | Tool discovery duration | - |
| `agent.tools.discovered` | Gauge | Number of tools discovered | - |
| `agent.ai_synthesis.duration_ms` | Histogram | AI synthesis duration | - |
| `agent.ai.requests` | Counter | AI API requests | provider, operation |
| `agent.ai.tokens.prompt` | Counter | Prompt tokens used | provider |
| `agent.ai.tokens.completion` | Counter | Completion tokens used | provider |

---

## Environment Profiles

The telemetry module supports three built-in profiles:

| Profile | Trace Sampling | Metric Interval | Use Case |
|---------|----------------|-----------------|----------|
| Development | 100% | 1s | Local development, debugging |
| Staging | 10% | 5s | Pre-production testing |
| Production | 0.1% | 15s | Production workloads |

Set via `APP_ENV` environment variable:
```bash
APP_ENV=development  # 100% sampling
APP_ENV=staging      # 10% sampling
APP_ENV=production   # 0.1% sampling
```

---

## AI Module Distributed Tracing

This example includes distributed tracing for AI operations. When you view traces in Jaeger, you'll see `ai.generate_response` and `ai.http_attempt` spans with full details including:

- **Token usage**: `ai.prompt_tokens`, `ai.completion_tokens`, `ai.total_tokens`
- **Model info**: `ai.provider`, `ai.model`
- **HTTP details**: `http.status_code`, `ai.attempt_duration_ms`
- **Retry tracking**: `ai.attempt`, `ai.max_retries`, `ai.is_retry`

### Critical: Initialization Order

**The telemetry module MUST be initialized BEFORE creating the AI client.** This example demonstrates the correct order in `main.go`:

```go
func main() {
    // 1. Set component type for service_type labeling
    core.SetCurrentComponentType(core.ComponentTypeAgent)

    // 2. Initialize telemetry BEFORE creating agent
    initTelemetry(serviceName)
    defer telemetry.Shutdown(context.Background())

    // 3. Create agent AFTER telemetry - AI client gets the provider
    agent, err := NewResearchAgent()
    // ...
}
```

If you reverse this order (creating the agent before telemetry), `telemetry.GetTelemetryProvider()` returns `nil` and no AI spans will appear in your traces.

---

## Migration Guide

If you have an existing agent based on [agent-example](../agent-example), follow these steps to add telemetry:

### Step 1: Add Telemetry Dependency

**go.mod**:
```go
require (
    github.com/truvaagents/truva-g3/core v0.6.5
    github.com/truvaagents/truva-g3/ai v0.6.5
    github.com/truvaagents/truva-g3/telemetry v0.6.5  // Add this
)
```

Run: `go mod tidy`

### Step 2: Initialize Telemetry in main.go

**Before**:
```go
func main() {
    agent := NewResearchAgent(aiClient)
    agent.Start()
}
```

**After**:
```go
import "github.com/truvaagents/truva-g3/telemetry"

func main() {
    // Initialize telemetry BEFORE creating agent
    initTelemetry("research-assistant-telemetry")
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := telemetry.Shutdown(ctx); err != nil {
            log.Printf("Telemetry shutdown error: %v", err)
        }
    }()

    agent := NewResearchAgent(aiClient)
    agent.Start()
}

func initTelemetry(serviceName string) {
    env := os.Getenv("APP_ENV")
    if env == "" {
        env = "development"
    }

    var profile telemetry.Profile
    switch env {
    case "production", "prod":
        profile = telemetry.ProfileProduction
    case "staging", "stage":
        profile = telemetry.ProfileStaging
    default:
        profile = telemetry.ProfileDevelopment
    }

    config := telemetry.UseProfile(profile)
    config.ServiceName = serviceName

    if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
        config.Endpoint = endpoint
    }

    if err := telemetry.Initialize(config); err != nil {
        log.Printf("Telemetry initialization failed: %v", err)
        log.Printf("Application will continue without telemetry")
    }
}
```

### Step 3: Declare Metrics in NewAgent

**Add to your agent constructor**:
```go
func NewResearchAgent(aiClient core.AIClient) *ResearchAgent {
    agent := &ResearchAgent{
        BaseAgent:  core.NewBaseAgent("research-assistant-telemetry"),
        aiClient:   aiClient,
        httpClient: &http.Client{Timeout: 30 * time.Second},
    }

    // NEW: Declare metrics this agent will emit
    telemetry.DeclareMetrics("research-agent", telemetry.ModuleConfig{
        Metrics: []telemetry.MetricDefinition{
            {
                Name:    "agent.research.duration_ms",
                Type:    "histogram",
                Help:    "Research operation duration in milliseconds",
                Labels:  []string{"topic", "status"},
                Unit:    "milliseconds",
                Buckets: []float64{100, 500, 1000, 5000, 10000, 30000},
            },
            {
                Name:   "agent.research.requests",
                Type:   "counter",
                Help:   "Total research requests",
                Labels: []string{"status"},
            },
            // Add more metrics as needed
        },
    })

    agent.RegisterCapability(/* ... */)
    return agent
}
```

### Step 4: Emit Metrics in Handlers

**Before**:
```go
func (r *ResearchAgent) handleResearchTopic(rw http.ResponseWriter, req *http.Request) {
    // ... process request ...

    results := r.orchestrateResearch(ctx, request)

    // ... return response ...
}
```

**After**:
```go
import "github.com/truvaagents/truva-g3/telemetry"

func (r *ResearchAgent) handleResearchTopic(rw http.ResponseWriter, req *http.Request) {
    startTime := time.Now()

    // Track overall operation duration
    defer func() {
        telemetry.Histogram("agent.research.duration_ms",
            float64(time.Since(startTime).Milliseconds()),
            "topic", request.Topic,
            "status", "completed")
    }()

    // Track request count
    telemetry.Counter("agent.research.requests", "status", "started")

    // ... process request ...

    results := r.orchestrateResearch(ctx, request)

    // ... return response ...
}
```

### Step 5: Add Tool Call Telemetry

Track individual tool invocations with timing and success/failure metrics:

```go
func (r *ResearchAgent) callToolWithCapability(ctx context.Context, tool *core.ServiceInfo, capability *core.Capability, topic string) *ToolResult {
    startTime := time.Now()

    // Track individual tool call duration
    defer func() {
        telemetry.Histogram("agent.tool_call.duration_ms",
            float64(time.Since(startTime).Milliseconds()),
            "tool", tool.Name)
    }()

    // ... make tool call ...

    if err != nil {
        telemetry.Counter("agent.tool_call.errors",
            "tool", tool.Name,
            "error_type", classifyError(err))
        return &ToolResult{Success: false, Error: err.Error()}
    }

    telemetry.Counter("agent.tool_call.success", "tool", tool.Name)
    return result
}
```

### Step 6: Add Environment Configuration

**Add to .env**:
```bash
# Telemetry Configuration
APP_ENV=development                           # development|staging|production
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
```

**Add to Kubernetes deployment**:
```yaml
env:
  - name: APP_ENV
    value: "production"
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://otel-collector.monitoring:4318"
```

---

## Telemetry Best Practices

### DO

- **Initialize early**: Set up telemetry at the start of main()
- **Use profiles**: Leverage pre-configured profiles for different environments
- **Add context**: Use labels to make metrics meaningful
- **Handle failures gracefully**: Don't let telemetry crash your app
- **Use bounded labels**: Only use labels with limited value sets

### DON'T

- **Don't use high-cardinality labels**: No user IDs, timestamps, or UUIDs
- **Don't emit sensitive data**: No passwords, tokens, or PII in metrics
- **Don't over-instrument**: Start simple, add more as needed
- **Don't block on telemetry**: Use appropriate timeouts

---

## Key Implementation Patterns

### 1. Metric Declaration (Before Use)

Declare all metrics upfront in your constructor:

```go
telemetry.DeclareMetrics("component-name", telemetry.ModuleConfig{
    Metrics: []telemetry.MetricDefinition{
        {
            Name:    "metric.name",
            Type:    "histogram",  // or "counter", "gauge"
            Help:    "What this metric measures",
            Labels:  []string{"label1", "label2"},
            Buckets: []float64{...},  // For histograms
        },
    },
})
```

### 2. Duration Tracking Pattern

Use defer for automatic duration tracking:

```go
func operation() {
    startTime := time.Now()
    defer func() {
        telemetry.Histogram("operation.duration_ms",
            float64(time.Since(startTime).Milliseconds()),
            "status", "completed")
    }()

    // Your operation code here
}
```

### 3. Error Classification Pattern

Classify errors for better metrics grouping:

```go
func classifyError(err error) string {
    errStr := err.Error()
    switch {
    case strings.Contains(errStr, "timeout"):
        return "timeout"
    case strings.Contains(errStr, "connection refused"):
        return "connection_refused"
    case strings.Contains(errStr, "context canceled"):
        return "canceled"
    default:
        return "unknown"
    }
}
```

> **See Also**: For advanced error handling patterns including AI-powered error correction and intelligent retry strategies, see the [Intelligent Error Handling Guide](https://github.com/truvaagents/truva-g3/blob/main/docs/orchestration/INTELLIGENT_ERROR_HANDLING.md).

---

## Configuration Reference

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 8092 | HTTP server port |
| `REDIS_URL` | redis://localhost:6379 | Redis connection URL |
| `OPENAI_API_KEY` | - | OpenAI API key (required) |
| `OPENAI_BASE_URL` | - | Custom OpenAI endpoint (optional) |
| `APP_ENV` | development | Telemetry profile (development/staging/production) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | http://localhost:4318 | OTEL Collector endpoint |

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
# Safe copy - won't overwrite existing .env
[ ! -f .env ] && cp .env.example .env
```

The `.env.example` file contains comprehensive documentation for all options including:

- **AI Provider Keys** - Supports provider chain for failover
- **Telemetry Configuration** - Environment profiles and OTLP endpoints

### Makefile Targets

```bash
make build           # Build the binary
make run             # Run locally
make test            # Run tests
make docker-build    # Build Docker image
make k8s-deploy      # Deploy to Kubernetes
make clean           # Clean build artifacts
```

---

## Troubleshooting

### No metrics appearing in monitoring system

1. **Check OTEL Collector endpoint**:
   ```bash
   echo $OTEL_EXPORTER_OTLP_ENDPOINT  # Should be configured
   ```

2. **Verify environment profile**:
   ```bash
   # Check logs for telemetry initialization
   ./setup.sh logs | grep -i telemetry
   ```

3. **Test with development profile locally**:
   ```bash
   export APP_ENV=development
   export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
   go run .
   ```

### Traces not appearing in Jaeger

1. **Verify sampling rate**:
   - Check `APP_ENV` is set to "development" for 100% sampling
   - Production uses 0.1% sampling by design

2. **Check OTEL Collector configuration**:
   - Ensure trace pipeline is configured in your cluster
   - Verify Jaeger endpoint is reachable

### High memory usage

For production deployments:

1. **Use Production profile**:
   ```bash
   export APP_ENV=production  # 0.1% sampling
   ```

2. **Monitor cardinality**:
   - Check telemetry health: `telemetry.GetHealth()`
   - Avoid high-cardinality labels (user IDs, timestamps)

### Useful Commands

All day-to-day operations go through `setup.sh`. Run `./setup.sh help` to see every subcommand.

```bash
# Stream agent logs
./setup.sh logs

# Check pod / service status
./setup.sh status

# Port forward the agent to localhost:8092
./setup.sh forward

# Port forward agent + monitoring dashboards (Grafana, Prometheus, Jaeger)
./setup.sh forward-all

# Restart the deployment (e.g., to pick up a new ConfigMap from .env)
./setup.sh rollout

# Run the built-in smoke test against the deployed agent
./setup.sh test

# Remove only the agent (keeps cluster + infra)
./setup.sh clean

# Tear down the entire Kind cluster
./setup.sh clean-all
```

While `./setup.sh forward` is running, test the API directly:

```bash
curl -X POST http://localhost:8092/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic": "latest AI developments", "ai_synthesis": true}'
```

---

## Related Examples

- [agent-example](../agent-example) - Basic agent without telemetry
- [tool-example](../tool-example) - Tool with telemetry integration
- [agent-with-orchestration](../agent-with-orchestration/) - Advanced orchestration patterns
- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent with full observability

---

## Learn More

- [TruvaG3 Telemetry Module](../../telemetry/README.md) - Complete telemetry documentation
- [Distributed Tracing Guide](../../docs/observability/DISTRIBUTED_TRACING_GUIDE.md) - End-to-end request tracing, log correlation, and multi-service examples
- [OpenTelemetry Go Documentation](https://opentelemetry.io/docs/languages/go/)
- [Prometheus Best Practices](https://prometheus.io/docs/practices/naming/)

---

## License

This example is part of the TruvaG3 framework and is licensed under the same terms.
