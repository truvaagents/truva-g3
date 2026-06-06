# Async AI-Driven Agent

An asynchronous task processing agent that demonstrates the TruvaG3 async task system combined with AI orchestration for autonomous multi-tool coordination. The agent accepts natural language queries and dynamically decides which tools to call, using a DAG-based execution engine for parallel processing.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Required Tools](#required-tools)
- [Architecture](#architecture)
- [Features](#features)
- [API Reference](#api-reference)
  - [Submit a Task](#submit-a-task)
  - [Poll Task Status](#poll-task-status)
  - [Cancel a Task](#cancel-a-task)
- [Configuration](#configuration)
- [Deployment Modes](#deployment-modes)
  - [Embedded Mode (Development)](#embedded-mode-development)
  - [Split Mode (Production)](#split-mode-production)
- [Observability](#observability)
  - [Distributed Tracing with Jaeger](#distributed-tracing-with-jaeger)
  - [Metrics](#metrics)
  - [Worker Logs](#worker-logs)
- [Task Input Schema](#task-input-schema)
- [Project Structure](#project-structure)
- [Key Benefits vs. Hardcoded Workflows](#key-benefits-vs-hardcoded-workflows)

---

## How to Run This Example

Running this example locally is the best way to understand how the TruvaG3 async task system and AI orchestration work together. Follow the steps below to get this example running.

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

This agent requires at least one AI provider API key for AI-driven orchestration.

| Provider | Get API Key | Notes |
|----------|-------------|-------|
| **OpenAI** | [platform.openai.com/api-keys](https://platform.openai.com/api-keys) | GPT-4o recommended |
| **Anthropic** | [console.anthropic.com](https://console.anthropic.com/) | Claude models |
| **Groq** | [console.groq.com/keys](https://console.groq.com/keys) | Fast inference, free tier |
| **Google Gemini** | [aistudio.google.com/apikey](https://aistudio.google.com/apikey) | Gemini models |
| **DeepSeek** | [platform.deepseek.com](https://platform.deepseek.com/) | Advanced reasoning |

**Chain Client:** The agent uses Chain Client for automatic failover between AI providers. Configure multiple providers for higher availability.

**Failover Order:** OpenAI -> Anthropic -> Groq (configurable)

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

> **Important:** This agent requires [tools to be deployed](#required-tools) to function. The agent dynamically discovers tools via Redis service discovery - without tools deployed, the agent has nothing to call! You can deploy tools before or after the agent, but the agent will not be able to answer queries until tools are running.

### Quick Start (Recommended)

The fastest way to get everything running in your local:

```bash
cd examples/agent-with-async

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
# 2. Start all services (Redis, tools, and agent)
./setup.sh run-all
```

**What `./setup.sh run-all` does:**
1. Starts Redis for task queue and service discovery
2. Deploys available tools
3. Builds and runs the async agent

Once complete, access the agent at:

| Service | URL | Description |
|---------|-----|-------------|
| **Async Agent API** | http://localhost:8098 | Task submission and polling API |
| **Jaeger** | http://localhost:16686 | Distributed tracing |
| **Grafana** | http://localhost:3000 | Metrics dashboard (admin/admin) |
| **Prometheus** | http://localhost:9090 | Metrics queries |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Create the Kubernetes Cluster

```bash
cd examples/agent-with-async
./setup.sh cluster
```

This creates a Kind cluster with proper port mappings for all services.

#### Step 2: Deploy Infrastructure

```bash
./setup.sh setup
```

This sets up the shared infrastructure components:
- **Redis** - Task queue and service discovery
- **OTEL Collector** - Telemetry aggregation
- **Prometheus** - Metrics storage
- **Jaeger** - Distributed tracing

#### Step 3: Deploy the Tools (Important!)

**The async agent requires tools to be deployed.** Without tools, the agent has no capabilities to orchestrate.

```bash
# Deploy all tools at once (from the truvag3 root directory)
for tool in weather-tool-v2 geocoding-tool currency-tool news-tool stock-market-tool country-info-tool; do
  ./examples/$tool/setup.sh deploy
done
```

Or deploy individually:

```bash
cd ../weather-tool-v2 && ./setup.sh deploy
cd ../geocoding-tool && ./setup.sh deploy
cd ../currency-tool && ./setup.sh deploy
cd ../news-tool && ./setup.sh deploy
cd ../stock-market-tool && ./setup.sh deploy
cd ../country-info-tool && ./setup.sh deploy
```

#### Step 4: Build and Deploy the Agent

```bash
cd examples/agent-with-async

# Create .env from example (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
# Edit .env and uncomment/set your AI provider key(s)

# Build and run
./setup.sh build
./setup.sh run
```

#### Step 5: Kubernetes Deployment

```bash
# Deploy embedded mode (API + workers in same pod)
./setup.sh deploy

# Port forward to access locally
./setup.sh forward
```

---

## Required Tools

The async agent orchestrates multiple tools to answer user queries. **These tools must be running for the agent to function.**

| Tool | Purpose | Deploy Command |
|------|---------|----------------|
| **weather-tool-v2** | Weather forecasts for any location | `./examples/weather-tool-v2/setup.sh deploy` |
| **geocoding-tool** | Convert location names to coordinates | `./examples/geocoding-tool/setup.sh deploy` |
| **currency-tool** | Currency conversion and exchange rates | `./examples/currency-tool/setup.sh deploy` |
| **news-tool** | Search news articles by topic | `./examples/news-tool/setup.sh deploy` |
| **stock-market-tool** | Stock prices and market news | `./examples/stock-market-tool/setup.sh deploy` |
| **country-info-tool** | Country information and facts | `./examples/country-info-tool/setup.sh deploy` |

**How tool discovery works:**
1. Tools register their capabilities in Redis when they start
2. The agent queries Redis to discover available tools
3. AI orchestrator selects tools based on user query intent
4. **Adding new tools** = Just deploy them - the agent discovers them automatically!

---

## Architecture

```
+---------------------------------------------------------------------+
|                      Async AI Agent                                  |
+---------------------------------------------------------------------+
|                                                                      |
|  HTTP API (Task Submission)                                          |
|  POST /api/v1/tasks        -> Submit natural language query          |
|  GET  /api/v1/tasks/:id    -> Poll task status/result                |
|  POST /api/v1/tasks/:id/cancel -> Cancel running task                |
|                                                                      |
|  AI Orchestration Flow                                               |
|  1. Parse natural language query                                     |
|  2. AI generates execution plan based on available tools             |
|  3. DAG executor runs steps (parallel where possible)                |
|  4. OnStepComplete callback reports per-tool progress                |
|  5. AI synthesizes final response                                    |
|                                                                      |
|  Available Tools (auto-discovered via Redis):                        |
|  - weather-tool-v2   -> Weather forecasts for any location           |
|  - geocoding-tool    -> Convert location names to coordinates        |
|  - currency-tool     -> Currency conversion and exchange rates       |
|  - news-tool         -> Search news articles by topic                |
|  - stock-market-tool -> Stock prices and market news                 |
|  - country-info-tool -> Country information and facts                |
|                                                                      |
|  Adding new tools = Just deploy them (no agent code changes!)        |
+---------------------------------------------------------------------+
```

### How It Works

1. **Task Submission**: Client submits a natural language query via POST /api/v1/tasks
2. **Queue**: Task is added to Redis queue with status "queued"
3. **Worker Pickup**: Background worker dequeues the task
4. **AI Planning**: LLM analyzes the query and determines which tools to call
5. **DAG Execution**: Tools are executed (parallel where dependencies allow)
6. **Progress Reporting**: `OnStepComplete` callback reports each tool completion
7. **AI Synthesis**: LLM combines tool outputs into a coherent response
8. **Completion**: Results stored, status set to "completed"
9. **Polling**: Client polls GET /api/v1/tasks/:id for status

---

## Features

- **AI-Driven Orchestration**: LLM dynamically decides which tools to call based on query intent
- **Natural Language Input**: No structured input required - just describe what you need
- **DAG-Based Parallel Execution**: Independent tools run concurrently for faster results
- **Per-Tool Progress Reporting**: Real-time updates as each tool completes via `OnStepComplete` callback
- **4-Layer Intelligent Error Recovery**: Semantic retry with LLM-based error analysis
- **Async Task Execution**: HTTP 202 + polling pattern for long-running operations
- **Zero Code Changes for New Tools**: Just deploy new tools - the agent discovers them automatically

---

## API Reference

### Submit a Task

```bash
curl -X POST http://localhost:8098/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "type": "query",
    "input": {
      "query": "What is the weather in Paris?"
    }
  }'
```

**Response (Task Queued):**
```json
{
  "task_id": "b4bdca9e-85e6-4a26-be68-7d86432b0c62",
  "status": "queued",
  "status_url": "/api/v1/tasks/b4bdca9e-85e6-4a26-be68-7d86432b0c62"
}
```

### Poll Task Status

```bash
curl http://localhost:8098/api/v1/tasks/b4bdca9e-85e6-4a26-be68-7d86432b0c62
```

**Response (In Progress):**
```json
{
  "task_id": "b4bdca9e-85e6-4a26-be68-7d86432b0c62",
  "type": "query",
  "status": "running",
  "progress": {
    "current_step": 2,
    "total_steps": 3,
    "step_name": "completed: weather-tool-v2",
    "percentage": 75,
    "message": "Tool 1/1 completed"
  }
}
```

**Response (Completed):**
```json
{
  "task_id": "b4bdca9e-85e6-4a26-be68-7d86432b0c62",
  "type": "query",
  "status": "completed",
  "progress": {
    "current_step": 3,
    "total_steps": 3,
    "step_name": "Complete",
    "percentage": 100,
    "message": "Executed 1 tools successfully"
  },
  "result": {
    "query": "What is the weather in Paris?",
    "response": "The current weather in Paris is characterized by slight snowfall, with a temperature of -0.6C. The humidity level is quite high at 93%, and there is a light wind blowing at a speed of 5.1 km/h.",
    "tools_used": ["weather-tool-v2"],
    "step_results": [
      {
        "tool_name": "weather-tool-v2",
        "success": true,
        "duration": "609.030792ms"
      }
    ],
    "execution_time": "9.259515754s",
    "confidence": 0.95,
    "metadata": {
      "duration_ms": 9259,
      "mode": "ai_orchestrated",
      "request_id": "1767631206964317921-964318004"
    }
  },
  "created_at": "2026-01-05T16:40:06.956579004Z",
  "started_at": "2026-01-05T16:40:06.958070462Z",
  "completed_at": "2026-01-05T16:40:16.2178858Z"
}
```

### Multi-Tool Query Example

The AI orchestrator automatically selects multiple tools based on query complexity:

```bash
curl -X POST http://localhost:8098/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "type": "query",
    "input": {
      "query": "I am planning to visit Tokyo next week. What is the weather forecast and current exchange rate from USD to JPY?"
    }
  }'
```

**Response (Completed with Multiple Tools):**
```json
{
  "task_id": "abc123-multi-tool",
  "type": "query",
  "status": "completed",
  "progress": {
    "current_step": 5,
    "total_steps": 5,
    "step_name": "Complete",
    "percentage": 100,
    "message": "Executed 3 tools successfully"
  },
  "result": {
    "query": "I am planning to visit Tokyo next week...",
    "response": "Based on my research for your Tokyo trip next week: The weather forecast shows temperatures around 8-12C with partly cloudy skies. The current exchange rate is 1 USD = 149.50 JPY. I recommend bringing layers for the variable weather.",
    "tools_used": ["geocoding-tool", "weather-tool-v2", "currency-tool"],
    "step_results": [
      {"tool_name": "geocoding-tool", "success": true, "duration": "120.5ms"},
      {"tool_name": "weather-tool-v2", "success": true, "duration": "650.2ms"},
      {"tool_name": "currency-tool", "success": true, "duration": "180.8ms"}
    ],
    "execution_time": "12.5s",
    "confidence": 0.95,
    "metadata": {
      "mode": "ai_orchestrated"
    }
  }
}
```

### Cancel a Task

```bash
curl -X POST http://localhost:8098/api/v1/tasks/b4bdca9e-85e6-4a26-be68-7d86432b0c62/cancel
```

**Response:**
```json
{
  "task_id": "b4bdca9e-85e6-4a26-be68-7d86432b0c62",
  "status": "cancelled",
  "message": "Task cancelled successfully"
}
```

---

## Configuration

### Environment Variables

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_URL` | Required | Redis connection URL |
| `PORT` | 8098 | HTTP server port |
| `WORKER_COUNT` | 3 | Number of background workers |
| `NAMESPACE` | - | Kubernetes namespace for discovery |
| `OPENAI_API_KEY` | - | OpenAI API key for AI orchestration |
| `ANTHROPIC_API_KEY` | - | Anthropic API key for AI orchestration |
| `GROQ_API_KEY` | - | Groq API key for AI orchestration |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | - | OpenTelemetry collector endpoint |
| `APP_ENV` | development | Environment (development/staging/production) |
| `DEV_MODE` | false | Enable development mode |

### AI Provider Setup

The agent uses **Chain Client** for automatic failover between AI providers. You only need **one** provider configured, but multiple providers give you higher availability.

| Provider | Environment Variable | Get API Key | Notes |
|----------|---------------------|-------------|-------|
| **OpenAI** (recommended) | `OPENAI_API_KEY` | [platform.openai.com](https://platform.openai.com/api-keys) | Best quality, most features |
| **Anthropic** | `ANTHROPIC_API_KEY` | [console.anthropic.com](https://console.anthropic.com/) | Excellent reasoning |
| **Groq** | `GROQ_API_KEY` | [console.groq.com](https://console.groq.com/keys) | Ultra-fast, free tier available |
| **Google Gemini** | `GEMINI_API_KEY` | [aistudio.google.com](https://aistudio.google.com/apikey) | Good multimodal |
| **DeepSeek** | `DEEPSEEK_API_KEY` | [platform.deepseek.com](https://platform.deepseek.com/) | Advanced reasoning |

**Failover Order**: OpenAI -> Anthropic -> Groq (configurable)

**Model Aliases**: Use `TRUVAG3_{PROVIDER}_MODEL_DEFAULT` to override models without code changes:
```bash
# Use cheaper models in development
TRUVAG3_OPENAI_MODEL_DEFAULT=gpt-4o-mini
TRUVAG3_ANTHROPIC_MODEL_DEFAULT=claude-3-haiku-20240307
```

See [AI Module Documentation](../../ai/README.md) for full provider configuration details.

---

## Deployment Modes

The agent supports three deployment modes via the `TRUVAG3_MODE` environment variable:

| Mode | `TRUVAG3_MODE` | Description | Use Case |
|------|---------------|-------------|----------|
| **Embedded** | `""` (unset) | API + Workers in same process | Local development |
| **API** | `api` | HTTP server only, enqueues tasks | Production (scale by request load) |
| **Worker** | `worker` | Task processing only, minimal /health | Production (scale by queue depth) |

### Embedded Mode (Development)

```bash
# Run with both API and workers in same process
./setup.sh run

# Or explicitly
TRUVAG3_MODE= go run .
```

### Split Mode (Production)

For production, deploy API and Worker separately using the same Docker image:

```
+-----------------------------+     +-----------------------------+
| async-travel-agent-api      |     | async-travel-agent-worker   |
| (TRUVAG3_MODE=api)           |     | (TRUVAG3_MODE=worker)        |
+-----------------------------+     +-----------------------------+
| - POST /api/v1/tasks        |     | - GET /health               |
| - GET /api/v1/tasks/:id     |     | - BRPOP from Redis queue    |
| - Scale: HTTP request rate  |     | - Scale: Redis queue depth  |
+--------------+--------------+     +--------------+--------------+
               |         +-----------------+       |
               +-------->|     Redis       |<------+
                         |  Task Queue     |
                         +-----------------+
```

**Benefits of Split Deployment:**
- Scale API and Workers independently
- API pods are lightweight (100m CPU, 128Mi memory)
- Worker pods are compute-heavy (500m-1000m CPU, 256Mi-1Gi memory)
- Isolate task processing logs from HTTP logs

### Production Kubernetes Deployment

```bash
# Deploy separate API and Worker deployments
./setup.sh deploy-prod

# This deploys:
#   - async-travel-agent-api (2 replicas, TRUVAG3_MODE=api)
#   - async-travel-agent-worker (2 replicas, TRUVAG3_MODE=worker)
```

### K8s Deployment Files

| File | Mode | Description |
|------|------|-------------|
| `k8-deployment.yaml` | Embedded | Single deployment with API + Workers |
| `k8-deployment-api.yaml` | API | HTTP server only (production) |
| `k8-deployment-worker.yaml` | Worker | Task processing only (production) |

### Prerequisites for K8s

Ensure these are deployed first:
1. Redis (for task queue and service discovery)
2. OpenTelemetry Collector (for telemetry)
3. Jaeger (for distributed tracing)
4. Any tools you want the agent to use (auto-discovered via Redis)

---

## Observability

The agent integrates with OpenTelemetry for comprehensive observability:

- **Traces**: Full request journey including async task processing and per-tool calls
- **Metrics**: Task counts, durations, tool call counts, tools per query
- **Logs**: Structured JSON logging with correlation IDs

### Accessing Observability Stack

```bash
# Port forward agent + Jaeger + Prometheus + Grafana in one shot
./setup.sh forward-all
```

Once running, open:

- **Jaeger** (distributed tracing): http://localhost:16686
- **Prometheus** (metrics): http://localhost:9090
- **Grafana** (dashboards, admin/admin): http://localhost:3000

### Distributed Tracing with Jaeger

The async task system uses **linked spans** (OpenTelemetry standard) to connect API and Worker traces across the async boundary.

#### Understanding the Trace Architecture

```
+-------------------------------------------------------------------------+
|                    DISTRIBUTED TRACE FLOW                                |
+-------------------------------------------------------------------------+
|                                                                          |
|  Client Request                                                          |
|       |                                                                  |
|       v                                                                  |
|  +-------------------------------------------------------------+        |
|  | API Trace (async-travel-agent-api)                          |        |
|  | TraceID: abcd1234...                                        |        |
|  |                                                             |        |
|  |   HTTP POST /api/v1/tasks [2ms]                             |        |
|  |       +-- Store trace context in task                       |        |
|  +-------------------------------------------------------------+        |
|                            |                                             |
|                            | Task stored in Redis with                   |
|                            | trace_id + parent_span_id                   |
|                            v                                             |
|  +-------------------------------------------------------------+        |
|  | Worker Trace (async-travel-agent-worker)                    |        |
|  | TraceID: xyz789... (NEW trace)                              |        |
|  |                                                             |        |
|  |   task.process [6.6s]                                       |        |
|  |   |  +-- FOLLOWS_FROM: abcd1234... (link to API trace)     |        |
|  |   |                                                         |        |
|  |   +-- orchestrator.process_request [6.5s]                   |        |
|  |   |   +-- orchestrator.build_prompt [1ms]                   |        |
|  |   |   +-- ai.chain.generate_response [3.7s]                 |        |
|  |   |   |   +-- ai.http_attempt (OpenAI API call)             |        |
|  |   |   |                                                     |        |
|  |   |   +-- HTTP POST /api/capabilities/get_weather [620ms]   |        |
|  |   |   |   +-- HTTP GET api.open-meteo.com [618ms]           |        |
|  |   |   |                                                     |        |
|  |   |   +-- ai.chain.generate_response [2.9s] (synthesis)     |        |
|  +-------------------------------------------------------------+        |
|                                                                          |
+-------------------------------------------------------------------------+
```

#### How to Navigate Traces in Jaeger

**Step 1: View Worker Traces**
1. Open Jaeger UI: http://localhost:16686
2. Select Service: `async-travel-agent-worker`
3. Click "Find Traces"
4. Click on a trace to expand

**Step 2: Find the Linked API Trace**
1. Expand the `task.process` span
2. Look at the **"References"** section
3. You will see: `FOLLOWS_FROM -> [original trace ID]`
4. Click the trace ID to jump to the API trace

**Step 3: Understand Span Hierarchy**

| Span | Description | Typical Duration |
|------|-------------|------------------|
| `task.process` | Root worker span, contains FOLLOWS_FROM link | 5-30s |
| `orchestrator.process_request` | AI orchestration | 5-30s |
| `orchestrator.build_prompt` | Building LLM prompt | 1-5ms |
| `ai.chain.generate_response` | LLM API call | 2-10s |
| `ai.http_attempt` | Actual HTTP to OpenAI/Anthropic | 2-10s |
| `HTTP POST /api/capabilities/*` | Tool call to another service | 100ms-2s |

#### Why Separate Traces?

The API and Worker use **linked spans** instead of child spans because:

1. **Async Boundary**: The worker may process the task seconds/minutes later
2. **Independent Lifecycles**: API trace completes immediately (HTTP 202), worker trace can be long-running
3. **OpenTelemetry Standard**: `FOLLOWS_FROM` is the standard for async/queued operations

#### Viewing Tool Call Details

Tool calls appear as HTTP spans with logs showing key data:

```
Span: HTTP POST /api/capabilities/get_current_weather
  Tags:
    http.status_code: 200
    http.response_content_length: 267
  Logs:
    event: request_received
    event: calling_external_api (api: open-meteo, lat: 48.8566, lon: 2.3522)
    event: weather_retrieved (condition: "Slight snow fall", temperature: -0.6)
```

> **Note**: Full response bodies are not captured in traces (for performance/security). Key data points are logged instead.

### Metrics

The agent emits the following Prometheus metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `truvag3_async_orchestration_tasks` | Counter | `status` | Task completions (completed/failed) |
| `truvag3_async_orchestration_tool_calls` | Counter | `tool`, `status` | Per-tool call counts |
| `truvag3_async_orchestration_duration_ms` | Histogram | - | Task execution duration |
| `truvag3_async_orchestration_tools_per_query` | Histogram | - | Number of tools called per query |

#### Example Prometheus Queries

```promql
# Task success rate
sum(rate(truvag3_async_orchestration_tasks{status="completed"}[5m])) /
sum(rate(truvag3_async_orchestration_tasks[5m]))

# Average task duration
rate(truvag3_async_orchestration_duration_ms_sum[5m]) /
rate(truvag3_async_orchestration_duration_ms_count[5m])

# Tool call failure rate by tool
sum(rate(truvag3_async_orchestration_tool_calls{status="failed"}[5m])) by (tool)
```

#### Submitting Tasks with Trace Context

To correlate client-side traces with the async task, include the W3C `traceparent` header:

```bash
# Submit task with trace context (for end-to-end tracing)
curl -X POST http://localhost:8098/api/v1/tasks \
  -H "Content-Type: application/json" \
  -H "traceparent: 00-abcd1234567890abcdef1234567890ab-1234567890abcdef-01" \
  -d '{"type":"query","input":{"query":"What is the weather in Paris?"}}'
```

The trace ID (`abcd1234...`) will be stored with the task and linked in the worker trace.

### Worker Logs

Worker logs are structured JSON for easy parsing. Example log output during task processing:

```json
{
  "component": "telemetry",
  "endpoint": "otel-collector.truvag3-examples:4318",
  "level": "INFO",
  "message": "OpenTelemetry provider created successfully",
  "service": "async-travel-agent-worker",
  "service_name": "orchestrator",
  "timestamp": "2026-01-05T16:40:06Z"
}
```

**View Worker Logs:**

For most debugging, prefer the observability stack — Jaeger gives you per-task spans (including orchestration events and task IDs), Prometheus gives you metric queries (success rate, durations, tool calls):

```bash
# Port forward agent + Jaeger + Prometheus + Grafana
./setup.sh forward-all
```

- **Jaeger** (per-task spans): http://localhost:16686 → Service `async-travel-agent-worker`
- **Prometheus** (metrics): http://localhost:9090

For raw container output, the agent's `setup.sh` does not currently expose a `logs` wrapper, so use `kubectl logs` directly (this is a documented exception — like the `kubectl exec ... redis-cli` Redis introspection pattern used elsewhere):

```bash
# Tail worker logs
kubectl logs -f -n truvag3-examples -l app=async-travel-agent-worker

# Filter for a specific task
kubectl logs -n truvag3-examples -l app=async-travel-agent-worker | grep "task_id"

# Filter for orchestration events
kubectl logs -n truvag3-examples -l app=async-travel-agent-worker | grep "TRUVAG3-ORCH"
```

---

## Task Input Schema

### Natural Language Query

```json
{
  "type": "query",
  "input": {
    "query": "string (required) - Natural language description of what you need"
  }
}
```

---

## Project Structure

```
agent-with-async/
+-- main.go                    # Entry point with TRUVAG3_MODE support (api/worker/embedded)
+-- travel_research_agent.go   # Agent definition, types, AI orchestrator initialization
+-- handlers.go                # Task handlers (HandleQuery) with OnStepComplete callback
+-- go.mod                     # Go module dependencies
+-- .env.example               # Environment configuration template (copy to .env)
+-- Dockerfile                 # Container build configuration
+-- Dockerfile.workspace       # Build using go workspace (for local development)
+-- k8-deployment.yaml         # Kubernetes manifests (embedded mode - development)
+-- k8-deployment-api.yaml     # Kubernetes manifests (API mode - production)
+-- k8-deployment-worker.yaml  # Kubernetes manifests (Worker mode - production)
+-- setup.sh                   # Local development and deployment script
+-- README.md                  # This file
```

---

## Key Benefits vs. Hardcoded Workflows

| Aspect | Old (Hardcoded) | New (AI-Driven) |
|--------|-----------------|-----------------|
| Input | Structured fields | Natural language |
| Workflow | Fixed in Go code | LLM-generated plan |
| Tool selection | Predetermined sequence | AI decides dynamically |
| Parallelization | Manual/sequential | Automatic DAG analysis |
| Error recovery | Basic logging | 4-layer intelligent retry |
| Adding features | Code change + redeploy | Just deploy new tools |

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent with SSE
- [chat-ui](../chat-ui/) - Web frontend for travel-chat-agent
- [agent-with-orchestration](../agent-with-orchestration/) - Basic orchestration example
- [agent-with-telemetry](../agent-with-telemetry/) - Full observability example
- [weather-tool-v2](../weather-tool-v2/) - Weather data tool
- [geocoding-tool](../geocoding-tool/) - Location geocoding tool
- [currency-tool](../currency-tool/) - Currency exchange tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
