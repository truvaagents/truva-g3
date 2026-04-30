# Travel Research Agent with Orchestration

A travel research agent that demonstrates the TruvaG3 **orchestration module** through intelligent coordination of multiple travel-related tools. It supports both predefined DAG-based workflows and dynamic AI-powered orchestration from natural language queries.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [What This Example Teaches](#what-this-example-teaches)
- [Architecture](#architecture)
- [Two Orchestration Modes](#two-orchestration-modes)
  - [Workflow Mode (Predefined DAGs)](#1-workflow-mode-predefined-dags)
  - [Dynamic Mode (AI-Powered)](#2-dynamic-mode-ai-powered)
- [Complex Query Examples](#complex-query-examples)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [How It Works](#how-it-works)
- [AI Module Distributed Tracing](#ai-module-distributed-tracing)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)
- [Related Examples](#related-examples)

---

## How to Run This Example

Running this example locally is the best way to understand how the TruvaG3 framework orchestrates tools using both predefined workflows and dynamic AI-powered planning. Follow the steps below to get this example running.

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

This agent requires at least one AI provider API key for AI-powered orchestration and natural language processing.

| Provider | Get API Key | Notes |
|----------|-------------|-------|
| **OpenAI** | [platform.openai.com/api-keys](https://platform.openai.com/api-keys) | GPT-4o recommended |
| **Anthropic** | [console.anthropic.com](https://console.anthropic.com/) | Claude models |
| **Groq** | [console.groq.com/keys](https://console.groq.com/keys) | Fast inference, free tier |

**Provider Chain:** Supports automatic failover (OpenAI -> Anthropic -> Groq). Configure multiple providers for higher availability.

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

> **Important:** This agent orchestrates multiple travel tools (weather-tool-v2, geocoding-tool, currency-tool, country-info-tool, news-tool) to function. These tools must be deployed and registered with Redis for the orchestrator to discover and invoke them. You can deploy tools before or after the agent, but the agent will not be able to execute workflows until tools are running.

### Quick Start (Recommended)

The fastest way to get everything running in your local:

```bash
cd examples/agent-with-orchestration

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

> **Note:** Multiple providers enable automatic failover. The tools (geocoding, weather, etc.) don't require AI keys - only the orchestration agent does for natural language processing.

```bash
# 2. Deploy the required tools (each tool has its own setup script)
cd ../geocoding-tool && ./setup.sh deploy && cd ..
cd ../weather-tool-v2 && ./setup.sh deploy && cd ..
cd ../currency-tool && ./setup.sh deploy && cd ..
cd ../country-info-tool && ./setup.sh deploy && cd ..
cd ../news-tool && ./setup.sh deploy && cd ..

# 3. Run the orchestration agent
cd agent-with-orchestration
./setup.sh run-all
```

**What `./setup.sh run-all` does:**
1. Ensures Redis is running (starts Docker container if needed)
2. Builds and runs the orchestration agent locally
3. Checks for available tools (does NOT deploy them)

Once complete, test the endpoints:

| Endpoint | URL | Description |
|----------|-----|-------------|
| **Health Check** | http://localhost:8094/health | Agent health status |
| **Workflows** | http://localhost:8094/orchestrate/workflows | List available workflows |
| **Discover** | http://localhost:8094/discover | List discovered tools |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Configure Environment

```bash
cd examples/agent-with-orchestration

# Create .env from example (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env

# Edit .env and uncomment/set your AI provider key(s)
nano .env    # or: code .env / vim .env
```

> **Important:** You must configure at least one AI provider API key in `.env` before the agent can perform natural language orchestration.

#### Step 2: Deploy the Required Tools

The orchestrator discovers tools via Redis. Each tool has its own setup script:

```bash
# Deploy each tool (from the examples directory)
cd ../geocoding-tool && ./setup.sh deploy
cd ../weather-tool-v2 && ./setup.sh deploy
cd ../currency-tool && ./setup.sh deploy
cd ../country-info-tool && ./setup.sh deploy
cd ../news-tool && ./setup.sh deploy
```

> **Note:** The `k8-deployment` directory contains shared infrastructure (Redis, Prometheus, Jaeger, Grafana, OTEL Collector), not tools. Each tool manages its own Kubernetes deployment via its `setup.sh` script.

#### Step 3: Build and Deploy the Agent

```bash
cd examples/agent-with-orchestration

# Build and deploy to Kubernetes
./setup.sh deploy

# Set up port forwarding
./setup.sh forward

# Test the deployment
./setup.sh test
```

#### Step 4: Verify Tool Discovery

```bash
# Check that tools are registered in Redis
redis-cli keys "truvag3:*"

# Or via the agent's discover endpoint
curl http://localhost:8094/discover
```

---

## What This Example Teaches

1. **AI-Powered Orchestration** - How to use `orchestration.AIOrchestrator` for dynamic request routing
2. **DAG-Based Workflows** - Predefined workflows with parallel/sequential dependencies
3. **Dynamic Tool Discovery** - How the orchestrator discovers and invokes tools at runtime
4. **Natural Language Processing** - Convert user requests into execution plans using LLM
5. **AI Synthesis** - Combine multi-tool results into coherent responses
6. **Distributed Tracing** - Track requests across tool boundaries

---

## Architecture

```
                    +-------------------------------------------------------------+
                    |           Travel Research Agent (Port 8094)                  |
                    |                                                             |
                    |  +-----------------------------------------------------+   |
                    |  |              AI Orchestrator                          |   |
                    |  |  +---------+  +---------+  +--------------+          |   |
                    |  |  | Catalog |  | Executor|  |  Synthesizer |          |   |
                    |  |  +---------+  +---------+  +--------------+          |   |
                    |  +-----------------------------------------------------+   |
                    |                                                             |
                    |  +-----------------------------------------------------+   |
                    |  |           Predefined Workflows                        |   |
                    |  |  - travel-research (5 steps)                         |   |
                    |  |  - quick-weather (2 steps)                           |   |
                    |  |  - currency-check (1 step)                           |   |
                    |  +-----------------------------------------------------+   |
                    +-------------------------------------------------------------+
                                              |
                    +-------------------------+-------------------------+
                    |                   Redis Discovery                   |
                    +-----------------------------------------------------+
                                              |
        +-------------+-------------+-------------+-------------+-------------+
        v             v             v             v             v
   +---------+  +---------+  +---------+  +---------+  +---------+
   |Geocoding|  |Weather  |  |Currency |  |Country  |  |  News   |
   |  Tool   |  |Tool v2  |  |  Tool   |  |  Info   |  |  Tool   |
   | (8085)  |  | (8086)  |  | (8087)  |  | (8088)  |  | (8089)  |
   +---------+  +---------+  +---------+  +---------+  +---------+
```

---

## Two Orchestration Modes

### 1. Workflow Mode (Predefined DAGs)

Execute predefined workflows with explicit step dependencies:

```bash
curl -X POST http://localhost:8094/orchestrate/travel-research \
  -H "Content-Type: application/json" \
  -d '{
    "destination": "Tokyo, Japan",
    "country": "Japan",
    "base_currency": "USD",
    "amount": 1000,
    "ai_synthesis": true
  }'
```

#### Travel Research Workflow Parameters

| Parameter | Required | Type | Description | Example |
|-----------|----------|------|-------------|---------|
| `destination` | Yes | string | City/location for geocoding and news | `"Tokyo, Japan"` |
| `country` | Yes | string | Country name for country-info lookup | `"Japan"` |
| `base_currency` | No | string | Your home currency (default: USD) | `"USD"` |
| `amount` | No | number | Amount to convert (default: 100) | `1000` |
| `ai_synthesis` | No | boolean | Enable AI synthesis and DAG visualization (default: false) | `true` |

**Note:** The `ai_synthesis` parameter controls which orchestrator method is used:

| `ai_synthesis` | Method Used | Behavior |
|----------------|-------------|----------|
| `true` | `ExecutePlanWithSynthesis()` | AI synthesis, DAG storage, request_id propagation |
| `false` (default) | `ExecutePlan()` | Raw results only, no synthesis, no DAG storage |

When `ai_synthesis: true`:
- Synthesizes results using the orchestrator's AI client
- Stores execution to the Execution Store for DAG visualization in the Registry Viewer
- Propagates request_id for distributed tracing correlation

When `ai_synthesis: false` (or omitted):
- Returns raw tool results without AI synthesis
- Faster execution (no LLM call for synthesis)
- Use this when you need raw data for custom processing

The `travel-research` workflow executes:

1. **geocode** - Get coordinates for `{{destination}}`
2. **weather** (depends on geocode) - Get weather using `{{geocode.data.lat}}`, `{{geocode.data.lon}}`
3. **country-info** (parallel) - Get country information for `{{country}}`
4. **currency** (depends on country-info) - Convert `{{amount}}` from `{{base_currency}}` to `{{country-info.data.currency.code}}`
5. **news** (parallel) - Get travel news for `{{destination}}`

#### Workflow DAG Execution

The `travel-research` workflow defines dependencies:

```
geocode --+---> weather
          |
          |    country-info ---> currency
          |
          +---> news (parallel)
```

Steps without dependencies run in parallel for faster execution.

#### Example Response (travel-research)

```json
{
  "request_id": "travel-research-1234567890",
  "workflow_used": "travel-research",
  "execution_time": "1.885s",
  "confidence": 1,
  "step_results": [
    {"step_id": "geocode", "tool_name": "geocoding-tool", "success": true},
    {"step_id": "weather", "tool_name": "weather-tool-v2", "success": true},
    {"step_id": "country-info", "tool_name": "country-info-tool", "success": true},
    {"step_id": "currency", "tool_name": "currency-tool", "success": true},
    {"step_id": "news", "tool_name": "news-tool", "success": true}
  ]
}
```

### 2. Dynamic Mode (AI-Powered)

Let the AI generate execution plans from natural language:

```bash
curl -X POST http://localhost:8094/orchestrate/natural \
  -H "Content-Type: application/json" \
  -d '{
    "request": "I am planning a trip to Paris next week. What is the weather like and what currency do they use?",
    "ai_synthesis": true
  }'
```

#### Natural Language Parameters

| Parameter | Required | Type | Description | Example |
|-----------|----------|------|-------------|---------|
| `request` | Yes | string | Natural language travel research request | `"What's the weather in Tokyo?"` |
| `ai_synthesis` | No | boolean | Enable AI synthesis (default: true) | `true` |
| `metadata` | No | object | Additional context and preferences | `{"user_preferences": {...}}` |

The orchestrator:

1. Discovers available tools from Redis
2. Uses LLM to generate an execution plan
3. Validates the plan against available capabilities
4. Executes steps (parallel when possible)
5. Synthesizes results into a coherent response

#### Example Response (natural language)

```json
{
  "request_id": "natural-1733660421234567890",
  "original_request": "I am planning a trip to Paris next week. What is the weather like and what currency do they use?",
  "execution_plan": {
    "steps": [
      {"step_id": "geocode", "tool_name": "geocoding-tool", "capability": "geocode"},
      {"step_id": "weather", "tool_name": "weather-tool-v2", "capability": "get_weather", "depends_on": ["geocode"]},
      {"step_id": "country-info", "tool_name": "country-info-tool", "capability": "get_country_info"}
    ]
  },
  "step_results": [
    {"step_id": "geocode", "tool_name": "geocoding-tool", "success": true, "data": {"lat": 48.8566, "lon": 2.3522}},
    {"step_id": "weather", "tool_name": "weather-tool-v2", "success": true, "data": {"temp": 8.5, "condition": "Cloudy"}},
    {"step_id": "country-info", "tool_name": "country-info-tool", "success": true, "data": {"currency": {"code": "EUR", "name": "Euro"}}}
  ],
  "synthesized_response": "For your trip to Paris next week: The current weather shows 8.5C with cloudy conditions. France uses the Euro (EUR) as its currency.",
  "execution_time": "2.341s",
  "tools_used": ["geocoding-tool", "weather-tool-v2", "country-info-tool"]
}
```

**Key Differences from Workflow Mode:**

- `execution_plan`: Shows the AI-generated plan with inferred dependencies
- `synthesized_response`: Natural language summary combining all tool results
- The AI determines which tools to call based on the request

### 3. Custom Workflow Mode (Inline Steps)

Define workflow steps inline without using predefined workflows or LLM planning. This gives you full control over exactly which tools to call and in what order:

```bash
curl -X POST http://localhost:8094/orchestrate/custom \
  -H "Content-Type: application/json" \
  -d '{
    "steps": [
      {
        "tool": "geocoding-tool",
        "capability": "geocode_location",
        "params": {"location": "Paris, France"}
      },
      {
        "tool": "weather-tool-v2",
        "capability": "get_current_weather",
        "params": {"lat": 48.8566, "lon": 2.3522}
      }
    ]
  }'
```

#### Custom Workflow Step Parameters

Each step in the `steps` array accepts:

| Parameter | Required | Type | Description | Example |
|-----------|----------|------|-------------|---------|
| `tool` | Yes | string | Tool name as registered in discovery | `"weather-tool-v2"` |
| `capability` | Yes | string | Capability/action to invoke | `"get_current_weather"` |
| `params` | No | object | Parameters to pass to the capability | `{"lat": 48.85, "lon": 2.35}` |

#### Example Response (custom workflow)

```json
{
  "plan_id": "custom-1733660421234567890",
  "success": true,
  "steps": [
    {
      "step_id": "step-1",
      "agent_name": "geocoding-tool",
      "success": true,
      "duration": "245.123ms",
      "response": {"lat": 48.8566, "lon": 2.3522, "display_name": "Paris, France"}
    },
    {
      "step_id": "step-2",
      "agent_name": "weather-tool-v2",
      "success": true,
      "duration": "312.456ms",
      "response": {"temp": 12.5, "condition": "Partly cloudy", "humidity": 65}
    }
  ],
  "duration": "557.579ms",
  "execution_time": "560.123ms"
}
```

**Key Differences from Other Modes:**

| Aspect | Workflow Mode | Dynamic Mode | Custom Mode |
|--------|--------------|--------------|-------------|
| Plan Source | Predefined in code | LLM-generated | Inline in request |
| LLM Planning | No | Yes | No |
| AI Synthesis | Optional | Yes | No |
| Dependencies | Predefined DAG | LLM-inferred | Sequential (order in array) |
| Use Case | Repeatable workflows | Natural language | Testing, debugging, precise control |

**When to Use Custom Mode:**

- **Testing individual tools** - Verify a tool works before using in workflows
- **Debugging** - Isolate issues by calling specific tools with known parameters
- **Precise control** - When you need exact tool invocation without LLM interpretation
- **Integration testing** - Test tool chains with specific parameter values

---

## Complex Query Examples

These examples demonstrate the orchestrator's ability to handle sophisticated natural language requests that invoke all 5 tools.

### Example 1: Single Destination (All 5 Tools)

```bash
curl -X POST http://localhost:8094/orchestrate/natural \
  -H "Content-Type: application/json" \
  -d '{
    "request": "I am planning a 2-week vacation to Tokyo, Japan starting next month. Can you help me with: 1) What is the current weather like in Tokyo? 2) What currency do they use in Japan and how much would 5000 USD convert to? 3) Tell me about Japan - population, languages spoken, and capital city. 4) Are there any recent travel news or advisories about Tokyo I should know about?",
    "ai_synthesis": true
  }'
```

**Expected Results:**

| Tool | Data Returned |
|------|---------------|
| geocoding-tool | Tokyo coordinates (lat/lon) |
| weather-tool-v2 | Temperature, conditions, humidity, wind |
| country-info-tool | Population: 123M, Language: Japanese, Capital: Tokyo |
| currency-tool | 5000 USD -> ~781,749 JPY |
| news-tool | Current travel advisories |

**Metrics:** ~13 seconds execution, 0.95 confidence

### Example 2: Multi-Destination Comparison (All 5 Tools x 2 Cities)

```bash
curl -X POST http://localhost:8094/orchestrate/natural \
  -H "Content-Type: application/json" \
  -d '{
    "request": "I want to compare traveling to Paris, France versus Berlin, Germany for a winter holiday. For each city, I need: the current weather conditions, currency information and how much 2000 USD would convert to their local currency, population and what languages are spoken, and any travel news about these cities.",
    "ai_synthesis": true
  }'
```

**Expected Results:**

| Aspect | Paris, France | Berlin, Germany |
|--------|--------------|-----------------|
| Weather | ~13C, overcast | ~10C, overcast |
| Currency | Euro (EUR) | Euro (EUR) |
| 2000 USD | ~1,718 EUR | ~1,718 EUR |
| Population | 66.35 million | 83.49 million |
| Language | French | German |
| Travel News | Current events/advisories | Current events/advisories |

**Metrics:** ~24 seconds execution, 0.95 confidence

### Why These Queries Work

The orchestrator's **schema-based type coercion** (Layer 2) automatically converts LLM-generated string parameters to the correct types expected by each tool:

- `"35.6897"` (string) -> `35.6897` (float64) for coordinates
- `"5000"` (string) -> `5000` (number) for currency amounts
- `"true"` (string) -> `true` (boolean) for flags

This eliminates type mismatch errors that would otherwise cause tool invocations to fail.

---

## API Reference

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/orchestrate/natural` | POST | Natural language orchestration |
| `/orchestrate/travel-research` | POST | Execute travel research workflow |
| `/orchestrate/custom` | POST | Execute custom workflow |
| `/orchestrate/workflows` | GET | List available workflows |
| `/orchestrate/history` | GET | Get execution history |
| `/api/capabilities` | GET | List agent capabilities with schemas |
| `/discover` | GET | Discover available tools |
| `/health` | GET | Health check with metrics |

### Example Requests

**Natural Language Orchestration:**
```bash
curl -X POST http://localhost:8094/orchestrate/natural \
  -H "Content-Type: application/json" \
  -d '{"request": "What is the weather in London?"}'
```

**Sample Response:**
```json
{
  "request_id": "1768435163723543676-723543801",
  "request": "What is the weather in London?",
  "response": "The current weather in London is characterized by slight rain, with a temperature of approximately 10.1°C. The humidity level is quite high at 88%, and there is a light wind blowing at 14 km/h.",
  "tools_used": ["weather-tool-v2", "geocoding-tool"],
  "execution_time": "10.972543713s",
  "confidence": 0.95
}
```

**Predefined Workflow (No LLM Planning):**
```bash
curl -X POST http://localhost:8094/orchestrate/travel-research \
  -H "Content-Type: application/json" \
  -d '{
    "destination": "Tokyo, Japan",
    "country": "Japan",
    "base_currency": "USD",
    "amount": 1000,
    "ai_synthesis": true
  }'
```

> **Note:** Set `ai_synthesis: true` to enable AI-powered response synthesis and DAG visualization storage.

**Custom Workflow (Inline Steps, No LLM):**
```bash
curl -X POST http://localhost:8094/orchestrate/custom \
  -H "Content-Type: application/json" \
  -d '{
    "steps": [
      {"tool": "geocoding-tool", "capability": "geocode_location", "params": {"location": "Berlin"}},
      {"tool": "weather-tool-v2", "capability": "get_current_weather", "params": {"lat": 52.52, "lon": 13.405}}
    ]
  }'
```

**Health Check:**
```bash
curl http://localhost:8094/health
```

**Sample Response:**
```json
{
  "ai_provider": "connected",
  "orchestrator": {
    "average_latency_ms": 10856,
    "status": "active",
    "successful_requests": 1,
    "total_requests": 1
  },
  "redis": "healthy",
  "status": "healthy",
  "workflows_available": 3
}
```

---

## Configuration

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `REDIS_URL` | Yes | - | Redis connection URL |
| `PORT` | No | 8094 | HTTP server port |
| `OPENAI_API_KEY` | No* | - | OpenAI API key for AI features |
| `ANTHROPIC_API_KEY` | No* | - | Anthropic API key |
| `GROQ_API_KEY` | No* | - | Groq API key |
| `DEV_MODE` | No | false | Enable development mode |
| `TRUVAG3_ORCHESTRATOR_MODE` | No | autonomous | Orchestration mode |
| `TRUVAG3_LLM_DEBUG_ENABLED` | No | false | Enable LLM debug payload capture |
| `TRUVAG3_LLM_DEBUG_TTL` | No | 24h | Retention for successful records |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | No | 168h | Retention for error records (7 days) |
| `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED` | No | false | Enable execution storage for DAG visualization |

*At least one AI provider key is required for natural language orchestration and synthesis.

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

The `.env.example` file contains comprehensive documentation for all options including:

- **AI Provider Keys** - Supports provider chain for failover (OpenAI -> Anthropic -> Groq)
- **Model Aliases** - Override default/smart/fast model mappings per provider
- **Orchestration Settings** - Mode, capability matching thresholds
- **Telemetry Configuration** - Environment profiles and OTLP endpoints

At minimum, uncomment and set one AI provider API key.

---

## How It Works

### Dynamic Request Flow

```
User Request: "What's the weather in Tokyo?"
                        |
                        v
              +-----------------+
              | 1. ProcessRequest|
              +--------+--------+
                       |
                       v
              +-----------------+
              | 2. Get Capabilities| <-- AgentCatalog.FormatForLLM()
              +--------+--------+
                       |
                       v
              +-----------------+
              | 3. Generate Plan | <-- LLM creates execution plan
              +--------+--------+
                       |
                       v
              +-----------------+
              | 4. Validate Plan | <-- Check tools exist
              +--------+--------+
                       |
                       v
              +-----------------+
              | 5. Execute Plan  | <-- SmartExecutor runs steps
              +--------+--------+
                       |
                       v
              +-----------------+
              | 6. Synthesize    | <-- AISynthesizer combines results
              +--------+--------+
                       |
                       v
              Response: Weather summary
```

---

## AI Module Distributed Tracing

This example demonstrates full AI telemetry integration. When you view traces in Jaeger, you will see:

- **`ai.generate_response`** spans for each AI call with token usage and model info
- **`ai.http_attempt`** spans showing HTTP-level details and retry behavior

### Critical: Initialization Order

The telemetry module MUST be initialized BEFORE creating the AI client. This example follows the correct order in `main.go`:

```go
func main() {
    // 1. Set component type
    core.SetCurrentComponentType(core.ComponentTypeAgent)

    // 2. Initialize telemetry BEFORE agent creation
    initTelemetry("travel-research-orchestration")
    defer telemetry.Shutdown(context.Background())

    // 3. Create agent AFTER telemetry
    agent, err := NewTravelResearchAgent()
}
```

### Viewing AI Traces

1. Port-forward Jaeger: `kubectl port-forward -n truvag3-examples svc/jaeger-query 16686:80`
2. Open: `http://localhost:16686`
3. Select service: `travel-research-orchestration`
4. Find a trace and expand it to see `ai.generate_response` and `ai.http_attempt` spans

### Observability

The agent integrates with TruvaG3 telemetry:

- **Traces**: Follow requests across tool boundaries
- **Metrics**: Track orchestrator performance
- **Logs**: Structured logging with correlation IDs

**LLM Debug Payload Store:**

For debugging orchestration issues, enable the LLM Debug Store to capture complete prompts and responses at all 6 recording sites (`plan_generation`, `correction`, `synthesis`, `synthesis_streaming`, `micro_resolution`, `semantic_retry`):

```bash
export TRUVAG3_LLM_DEBUG_ENABLED=true
```

Unlike Jaeger spans which truncate large payloads, this stores the full content.

View in Grafana:

```bash
kubectl port-forward -n truvag3-examples svc/grafana 3000:80
# Open http://localhost:3000
```

---

## Project Structure

| File | Description |
|------|-------------|
| `main.go` | Entry point with framework setup |
| `research_agent.go` | TravelResearchAgent with workflows |
| `handlers.go` | HTTP handlers for API endpoints |
| `go.mod` | Go module dependencies |
| `.env.example` | Environment variable template |
| `Dockerfile` | Container build configuration |
| `k8-deployment.yaml` | Kubernetes deployment manifest |
| `setup.sh` | One-click setup script |

---

## Troubleshooting

### Common Issues

**1. "Orchestrator not initialized"**

Ensure Redis is running and tools are discovered:

```bash
redis-cli keys "truvag3:*"
```

**2. "AI client not configured"**

Set an AI provider API key:

```bash
export OPENAI_API_KEY=sk-your-key
```

Or configure in your `.env` file.

**3. "Tool not found in catalog"**

Deploy the required travel tools using their setup scripts:

```bash
cd ../geocoding-tool && ./setup.sh deploy
cd ../weather-tool-v2 && ./setup.sh deploy
cd ../currency-tool && ./setup.sh deploy
cd ../country-info-tool && ./setup.sh deploy
cd ../news-tool && ./setup.sh deploy
```

### Useful Commands

```bash
# View agent logs
./setup.sh logs

# Check pod status
kubectl get pods -n truvag3-examples -l app=travel-research-orchestration

# Check Redis tool registration
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 0 KEYS 'truvag3:services:*'

# Test the API
./setup.sh test

# Full cleanup
./setup.sh cleanup
```

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent with SSE
- [agent-example](../agent-example/) - Basic agent with tool discovery
- [agent-with-resilience](../agent-with-resilience/) - Resilience patterns (circuit breakers, retries)
- [agent-with-telemetry](../agent-with-telemetry/) - Distributed tracing and metrics

---

## TODO

- [ ] **YAML Workflow Support** - Replace hardcoded Go workflow definitions with YAML files using the framework's `WorkflowEngine.ParseWorkflowYAML()`. This would demonstrate the declarative workflow feature documented in the orchestration module.
