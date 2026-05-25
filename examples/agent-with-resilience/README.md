# Agent with Resilience

This example demonstrates how to add fault tolerance to a TruvaG3 agent using the `resilience` module. It showcases circuit breakers, automatic retries with exponential backoff, timeout management, and graceful degradation with partial results when some tools fail.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [What This Example Adds](#what-this-example-adds)
- [Framework APIs Used](#framework-apis-used)
- [Circuit Breaker Behavior](#circuit-breaker-behavior)
- [API Endpoints](#api-endpoints)
- [Changes from agent-example](#changes-from-agent-example)
- [Graceful Degradation](#graceful-degradation)
- [Kubernetes Deployment](#kubernetes-deployment)
- [Testing Resilience](#testing-resilience)
  - [AI-Driven vs Direct Testing](#ai-driven-vs-direct-testing)
  - [Error Injection Modes](#error-injection-modes)
  - [Test Scenarios](#test-scenarios)
  - [Step-by-Step Walkthrough: What Happens Internally](#step-by-step-walkthrough-what-happens-internally)
- [Project Structure](#project-structure)
- [What You Will Learn](#what-you-will-learn)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

Running this example locally is the best way to understand how the TruvaG3 resilience module protects against cascading failures. Follow the steps below to get this example running.

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
curl -LO https://go.dev/dl/go1.26.2.linux-amd64.tar.gz

# Remove any previous installation and extract
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.2.linux-amd64.tar.gz

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

#### 5. AI Provider API Key (Optional)

AI features are **optional** for this example. Resilience testing works without AI, but AI enables intelligent tool selection and synthesis.

| Provider | Get API Key | Notes |
|----------|-------------|-------|
| **OpenAI** | [platform.openai.com/api-keys](https://platform.openai.com/api-keys) | GPT-4o recommended |
| **Anthropic** | [console.anthropic.com](https://console.anthropic.com/) | Claude models |

**Without an AI key:** The agent can still test resilience features using direct tool calls with the `sources` parameter.

**With an AI key:** The agent uses AI for intelligent tool selection, query analysis, and response synthesis.

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

> **Important:** This example includes the `grocery-store-api` mock service for testing resilience features. The mock service provides error injection capabilities to simulate failures, rate limiting, and server errors.

### Quick Start (Recommended)

The fastest way to get everything running in your local:

```bash
cd examples/agent-with-resilience

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**⚠️ Configure API Keys (Optional)** - Open `.env` if you want AI features:

```bash
# Open .env in your preferred editor
nano .env    # or: code .env / vim .env
```

AI keys are optional for this example - resilience patterns work without AI. If you want AI synthesis, set ONE of these:
- `OPENAI_API_KEY=sk-your-key`
- `ANTHROPIC_API_KEY=sk-ant-your-key`
- `GROQ_API_KEY=gsk_your-key`

```bash
# 2. Run everything locally
./setup.sh run-all
```

**What `./setup.sh run-all` does:**
1. Detects and reuses existing infrastructure (Redis, services)
2. Only starts what is missing
3. Builds and runs all components:
   - `grocery-store-api` (port 8081) - Mock API with error injection
   - `grocery-tool` (port 8083) - TruvaG3 tool wrapper
   - `research-agent-resilience` (port 8093) - The resilient agent

**Smart Infrastructure Detection:**

The `run-all` command intelligently detects existing services:

```bash
# If Redis is already running (local, Docker, or K8s port-forward):
[SUCCESS] Redis available (Docker: truvag3-redis)

# If grocery-store-api is already running:
[SUCCESS] grocery-store-api already available on port 8081
[INFO] Using existing grocery-store-api on port 8081
```

This means you can:
- Run multiple examples sharing the same Redis
- Use `./setup.sh forward` first, then `run-all` reuses the K8s services
- Mix local and K8s deployments seamlessly

Once complete, access the application at:

| Service | URL | Description |
|---------|-----|-------------|
| **Resilient Agent** | http://localhost:8093 | Research agent with circuit breakers |
| **Health Check** | http://localhost:8093/health | Circuit breaker states |
| **Grocery Store API** | http://localhost:8081 | Mock service with error injection |
| **Grocery Tool** | http://localhost:8083 | TruvaG3 tool wrapper |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Configure Environment

```bash
cd examples/agent-with-resilience

# Create .env from example (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env

# Edit .env and set your API key (optional for AI features)
```

#### Step 2: Start Redis (if not running)

```bash
docker run -d --name truvag3-redis -p 6379:6379 redis:7-alpine
```

#### Step 3: Build and Run

```bash
go build -o research-agent-resilience .
./research-agent-resilience
```

#### All Setup Commands

| Command | Description |
|---------|-------------|
| `./setup.sh run-all` | **Recommended** - Build and run everything locally |
| `./setup.sh run` | Setup and run agent only (assumes dependencies running) |
| `./setup.sh deploy` | Full Kubernetes deployment |
| `./setup.sh forward` | Port-forward K8s services to localhost |
| `./setup.sh test` | Run resilience test scenario |
| `./setup.sh build-all` | Build all components without running |
| `./setup.sh cleanup` | Remove all K8s resources |

---

## What This Example Adds

Building on the foundation of `agent-example`, this example adds:

| Feature | Description |
|---------|-------------|
| **Circuit Breakers** | Per-tool circuit breakers that protect against cascading failures |
| **Automatic Retries** | Exponential backoff with jitter using `resilience.RetryWithCircuitBreaker` |
| **Timeout Management** | Per-call timeouts using `cb.ExecuteWithTimeout` |
| **Graceful Degradation** | Returns partial results when some tools fail |
| **Health Monitoring** | Circuit breaker states exposed via `/health` endpoint |

---

## Framework APIs Used

This example demonstrates proper usage of the TruvaG3 `resilience` module:

```go
// 1. Create circuit breakers using the factory (auto-detects telemetry)
cb, err := resilience.CreateCircuitBreaker("tool-name", resilience.ResilienceDependencies{
    Logger: agent.Logger,
})

// 2. Use combined retry + circuit breaker pattern
err := resilience.RetryWithCircuitBreaker(ctx, resilience.DefaultRetryConfig(), cb, func() error {
    return makeToolCall()
})

// 3. Use timeout with circuit breaker
err := cb.ExecuteWithTimeout(ctx, 10*time.Second, func() error {
    return makeToolCall()
})

// 4. Monitor circuit breaker state
state := cb.GetState()     // "closed", "open", or "half-open"
metrics := cb.GetMetrics() // Comprehensive metrics map

// 5. Listen to state changes
cb.AddStateChangeListener(func(name string, from, to resilience.CircuitState) {
    log.Printf("Circuit %s: %s -> %s", name, from, to)
})
```

---

## Circuit Breaker Behavior

```
   CLOSED ────────────────────────────────────> OPEN
     │     (Error rate > 50% with 10+ requests)   │
     │                                             │
     │                                      (Wait 30s)
     │                                             │
     │                                             v
     │                                        HALF-OPEN
     │                                             │
     │     <───────────────────────────────────────┤
     │     (60% success in test requests)          │
     │                                             │
     └─────────────────────────────────────────────┘
                    (Success rate < 60%)
```

**Default Configuration** (from `resilience.DefaultConfig()`):

| Setting | Value | Description |
|---------|-------|-------------|
| ErrorThreshold | 0.5 | Open circuit at 50% error rate |
| VolumeThreshold | 10 | Minimum requests before evaluation |
| SleepWindow | 30s | Wait before entering half-open |
| HalfOpenRequests | 5 | Test requests in half-open state |
| SuccessThreshold | 0.6 | 60% success to close circuit |
| WindowSize | 60s | Sliding window for metrics |

---

## API Endpoints

### POST /api/capabilities/research_topic

Resilient research with automatic circuit breaker protection.

```bash
curl -X POST http://localhost:8093/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "weather in New York",
    "ai_synthesis": true
  }'
```

**Response with Partial Success:**
```json
{
  "topic": "weather in New York",
  "summary": "Found 2 results (1 successful, 1 failed)",
  "tools_used": ["weather-service"],
  "results": [...],
  "partial": true,
  "failed_tools": ["stock-service"],
  "success_rate": 0.5,
  "processing_time": "1.234s"
}
```

### GET /health

Health check with circuit breaker states.

```bash
curl http://localhost:8093/health
```

**Response:**
```json
{
  "status": "healthy",
  "timestamp": 1700000000,
  "redis": "healthy",
  "ai_provider": "connected",
  "circuit_breakers": {
    "weather-service": {
      "name": "weather-service",
      "state": "closed",
      "error_rate": 0.1,
      "total": 50,
      "success": 45,
      "failure": 5
    }
  },
  "resilience": {
    "enabled": true,
    "circuit_breakers": 2,
    "retry_config": {
      "max_attempts": 3,
      "initial_delay": "100ms",
      "max_delay": "5s",
      "backoff_factor": 2,
      "jitter_enabled": true
    }
  }
}
```

### GET /api/capabilities/discover_tools

Discover tools with circuit breaker status.

```bash
curl http://localhost:8093/api/capabilities/discover_tools
```

---

## Changes from agent-example

### research_agent.go

```diff
type ResearchAgent struct {
    *core.BaseAgent
    aiClient        core.AIClient
    httpClient      *http.Client
+   circuitBreakers map[string]*resilience.CircuitBreaker
+   retryConfig     *resilience.RetryConfig
+   cbMutex         sync.RWMutex
}

func NewResearchAgent() (*ResearchAgent, error) {
    // ... existing code ...
+   circuitBreakers: make(map[string]*resilience.CircuitBreaker),
+   retryConfig:     resilience.DefaultRetryConfig(),
}

+ // Use framework factory for CB creation
+ func (r *ResearchAgent) getOrCreateCircuitBreaker(toolName string) *resilience.CircuitBreaker {
+     cb, err := resilience.CreateCircuitBreaker(toolName, resilience.ResilienceDependencies{
+         Logger: r.Logger,
+     })
+     // ...
+ }
```

### orchestration.go

```diff
- func (r *ResearchAgent) callToolWithIntelligentRetry(...) *ToolResult {
-     // Custom retry logic with manual backoff
- }

+ func (r *ResearchAgent) callToolWithResilience(...) *ToolResult {
+     cb := r.getOrCreateCircuitBreaker(tool.Name)
+
+     // Use framework's combined retry + circuit breaker
+     err := resilience.RetryWithCircuitBreaker(ctx, r.retryConfig, cb, func() error {
+         result, callErr = r.callToolDirect(ctx, tool, capability, topic)
+         return callErr
+     })
+ }
```

### handlers.go

```diff
func (r *ResearchAgent) handleHealth(w http.ResponseWriter, req *http.Request) {
    health := map[string]interface{}{
        "status": "healthy",
+       "circuit_breakers": r.getCircuitBreakerStates(),
+       "resilience": map[string]interface{}{
+           "enabled": true,
+           "retry_config": r.retryConfig,
+       },
    }

+   // Check if any circuit is open
+   for name, cb := range r.circuitBreakers {
+       if cb.GetState() == "open" {
+           health["status"] = "degraded"
+           health["degraded_reason"] = fmt.Sprintf("Circuit '%s' is open", name)
+       }
+   }
}
```

---

## Graceful Degradation

When some tools fail, the agent continues with partial results:

```go
for _, tool := range tools {
    result := r.callToolWithResilience(ctx, tool, capability, topic)
    if result.Success {
        successfulResults = append(successfulResults, *result)
    } else {
        failedTools = append(failedTools, tool.Name)
        // Continue processing - don't fail the entire request
    }
}

// Return partial results with metadata
response := ResearchResponse{
    Results:     successfulResults,
    Partial:     len(failedTools) > 0,
    FailedTools: failedTools,
    SuccessRate: float64(len(successfulResults)) / float64(totalTools),
}
```

---

## Kubernetes Deployment

```bash
# Deploy to Kubernetes (builds image, loads into Kind, applies manifests)
./setup.sh deploy

# Check status
./setup.sh status

# View logs
./setup.sh logs

# Port forward for local access
./setup.sh forward
```

---

## Testing Resilience

This section demonstrates how to test the resilience features using the `grocery-store-api` with error injection capabilities.

### AI-Driven vs Direct Testing

The agent supports two modes for calling tools:

**Production Mode (AI-Driven)**
```bash
# AI analyzes the query and decides which tools to use
curl -X POST http://localhost:8093/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic":"What grocery products are available?","ai_synthesis":true}'
```

The AI will:
1. Discover available tools (weather-service, stock-service, grocery-service)
2. Analyze the query to determine which tools are relevant
3. Call the appropriate tools with resilience protection
4. Generate an intelligent summary with recommendations

**Example Response:**
```json
{
  "topic": "What grocery products are available?",
  "tools_used": ["grocery-service"],
  "ai_analysis": "**Analysis:** 20 products available across 5 categories. Recommendations: Consider diversifying manufacturers...",
  "metadata": {
    "ai_enabled": true,
    "tools_discovered": 3,
    "tools_called": 1,
    "tools_succeeded": 1
  }
}
```

**Testing Mode (Direct)**
```bash
# Explicitly target specific tools - bypasses AI decision-making
curl -X POST http://localhost:8093/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic":"grocery","sources":["grocery-service"],"ai_synthesis":false}'
```

This mode is used in the test scenarios below to:
- Isolate resilience testing from AI variability
- Ensure consistent, reproducible test results
- Directly test circuit breakers and retry logic

### Testing Prerequisites

This example is fully self-contained. All required components are included in the TruvaG3 repository:

| Component | Location | Description |
|-----------|----------|-------------|
| `grocery-store-api` | [`../mock-services/grocery-store-api/`](../mock-services/grocery-store-api/) | Mock API with error injection |
| `grocery-tool` | [`../grocery-tool/`](../grocery-tool/) | TruvaG3 tool that proxies to the API |
| `research-agent-resilience` | This directory | The resilient research agent |

**Deploy to Kubernetes:**
```bash
# This builds and deploys everything (agent + grocery-tool + grocery-store-api):
./setup.sh deploy
```

> The `deploy` subcommand handles Docker builds, the Kind image load, namespace/ConfigMap setup, and applying all required manifests in the correct order. See `./setup.sh help` for the full list of subcommands.

### Error Injection Modes

The `grocery-store-api` supports three error injection modes:

| Mode | Description | Use Case |
|------|-------------|----------|
| `normal` | All requests succeed | Baseline testing |
| `rate_limit` | Returns 429 after N requests | Test rate limit handling and retries |
| `server_error` | Returns 500 with probability | Test circuit breaker opening |

### Admin Endpoints (grocery-store-api)

```bash
# Set error injection mode
curl -X POST http://localhost:8081/admin/inject-error \
  -H "Content-Type: application/json" \
  -d '{"mode":"rate_limit","rate_limit_after":2,"retry_after_secs":5}'

# Check current status
curl http://localhost:8081/admin/status

# Reset to normal mode
curl -X POST http://localhost:8081/admin/reset
```

### Test Scenarios

#### Scenario 1: Normal Operation
```bash
# Ensure normal mode
curl -X POST http://localhost:8081/admin/reset

# Make request through agent
curl -X POST http://localhost:8093/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic":"grocery","sources":["grocery-service"],"ai_synthesis":false}'
```

**Expected Response:**
```json
{
  "success_rate": 1,
  "metadata": {
    "tools_succeeded": 1,
    "resilience_enabled": true
  }
}
```

#### Scenario 2: Rate Limiting and Retries
```bash
# Enable rate limiting (429 after 1 request)
curl -X POST http://localhost:8081/admin/inject-error \
  -H "Content-Type: application/json" \
  -d '{"mode":"rate_limit","rate_limit_after":1}'

# Make multiple requests - observe retries and failures
for i in 1 2 3 4; do
  echo "Request $i:"
  curl -s -X POST http://localhost:8093/api/capabilities/research_topic \
    -H "Content-Type: application/json" \
    -d '{"topic":"grocery","sources":["grocery-service"],"ai_synthesis":false}' | jq '{success_rate, partial, metadata}'
done
```

**Expected Behavior:**
- Request 1: Succeeds (`tools_succeeded: 1`)
- Requests 2-4: Fail after 3 retries (`partial: true`, `tools_succeeded: 0`)

**Agent Logs Show:**
```json
{"error":"max retry attempts (3) exceeded for HTTP 429: RATE_LIMIT_EXCEEDED",
 "message":"Tool call failed after resilience attempts",
 "tool":"grocery-service"}
```

#### Scenario 3: Circuit Breaker Opens
After enough failures, the circuit breaker opens:

```bash
# Check circuit breaker status
curl http://localhost:8093/health | jq '.circuit_breakers["grocery-service"]'
```

**Expected Response:**
```json
{
  "state": "half-open",
  "error_rate": 0.9,
  "success": 1,
  "failure": 9,
  "total": 10
}
```

#### Scenario 4: Recovery
```bash
# Reset to normal mode
curl -X POST http://localhost:8081/admin/reset

# Make recovery request
curl -X POST http://localhost:8093/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic":"grocery","sources":["grocery-service"],"ai_synthesis":false}'
```

**Expected Response:**
```json
{
  "success_rate": 1,
  "metadata": {
    "tools_succeeded": 1
  }
}
```

**Agent Logs Show:**
```json
{"message":"Resilient tool call succeeded",
 "circuit_state":"half-open",
 "tool":"grocery-service"}
```

### Step-by-Step Walkthrough: What Happens Internally

This section explains exactly what happens during a resilience test, helping developers understand the internal mechanics.

#### Step 1: Normal Operation (Circuit Closed)

```bash
# Reset API to normal mode
curl -X POST http://localhost:8081/admin/reset

# Make a request
curl -X POST http://localhost:8093/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic":"What groceries are available?","ai_synthesis":true}'
```

**What happens internally:**
1. Agent receives request - AI analyzes topic - selects `grocery-service`
2. Agent calls `getOrCreateCircuitBreaker("grocery-service")` - returns circuit in `closed` state
3. `RetryWithCircuitBreaker()` wraps the HTTP call
4. HTTP call to grocery-tool succeeds - grocery-tool proxies to grocery-store-api - returns products
5. Circuit breaker records: `success++`, `total++`
6. Response returns with `success_rate: 1`

**Agent log:**
```json
{"message":"Resilient tool call succeeded","tool":"grocery-service","circuit_state":"closed"}
```

#### Step 2: Rate Limiting Triggers Retries

```bash
# Enable rate limiting (429 after 1 request)
curl -X POST http://localhost:8081/admin/inject-error \
  -H "Content-Type: application/json" \
  -d '{"mode":"rate_limit","rate_limit_after":1}'

# Make second request
curl -X POST http://localhost:8093/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic":"List grocery items","ai_synthesis":true}'
```

**What happens internally:**
1. Agent receives request - AI selects `grocery-service`
2. Circuit is `closed` - allows the call
3. **Attempt 1**: HTTP call - grocery-store-api returns `429 Too Many Requests`
4. `RetryWithCircuitBreaker` catches error - waits `100ms` (initial delay)
5. **Attempt 2**: Retry - still `429` - waits `200ms` (backoff x 2)
6. **Attempt 3**: Final retry - still `429` - waits `400ms` (backoff x 2)
7. All 3 attempts exhausted - records failure in circuit breaker
8. Response returns with `partial: true`, `failed_tools: ["grocery-service"]`

**Agent log:**
```json
{"error":"max retry attempts (3) exceeded for HTTP 429: RATE_LIMIT_EXCEEDED",
 "message":"Tool call failed after resilience attempts",
 "tool":"grocery-service",
 "attempts":3,
 "total_duration":"700ms"}
```

#### Step 3: Failures Accumulate, Circuit Opens

After approximately 10 requests with greater than 50% failure rate:

```bash
# Check circuit breaker status
curl http://localhost:8093/health | jq '.circuit_breakers["grocery-service"]'
```

**What happens internally:**
1. Circuit breaker evaluates: `total >= VolumeThreshold (10)` - check passes
2. Calculates error rate: `failures / total = 9/10 = 90%`
3. Error rate `90% > ErrorThreshold (50%)` - **Circuit OPENS**
4. Next requests are immediately rejected without attempting HTTP call
5. Circuit starts `SleepWindow` timer (30 seconds)

**Circuit state:**
```json
{
  "state": "open",
  "error_rate": 0.9,
  "success": 1,
  "failure": 9,
  "total": 10
}
```

#### Step 4: Half-Open State (Recovery Window)

After 30 seconds, circuit enters `half-open`:

**What happens internally:**
1. `SleepWindow` expires - circuit transitions to `half-open`
2. Circuit allows limited test requests (`HalfOpenRequests: 5`)
3. If test requests succeed - circuit closes
4. If test requests fail - circuit re-opens

**Agent log (state change):**
```json
{"message":"Circuit state changed","circuit":"grocery-service","from":"open","to":"half-open"}
```

#### Step 5: Recovery (Circuit Closes)

```bash
# Reset API to normal
curl -X POST http://localhost:8081/admin/reset

# Make recovery request
curl -X POST http://localhost:8093/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic":"What groceries can I buy?","ai_synthesis":true}'
```

**What happens internally:**
1. Circuit is `half-open` - allows the test request
2. HTTP call succeeds - grocery-store-api returns products
3. Circuit evaluates: success rate in half-open = 100%
4. `100% >= SuccessThreshold (60%)` - **Circuit CLOSES**
5. Normal operation resumes

**Agent log:**
```json
{"message":"Circuit state changed","circuit":"grocery-service","from":"half-open","to":"closed"}
{"message":"Resilient tool call succeeded","tool":"grocery-service","circuit_state":"closed"}
```

#### Timeline Summary

| Time | Event | Circuit State | Error Rate |
|------|-------|---------------|------------|
| 0:00 | First request succeeds | closed | 0% |
| 0:01 | Rate limiting enabled | closed | 0% |
| 0:02 | Request 2 fails (3 retries) | closed | 50% |
| 0:03 | Request 3 fails (3 retries) | closed | 67% |
| 0:04 | Request 4 fails (3 retries) | **open** | 75% |
| 0:05 | Requests rejected immediately | open | - |
| 0:35 | Sleep window expires | **half-open** | - |
| 0:36 | Rate limiting disabled | half-open | - |
| 0:37 | Recovery request succeeds | **closed** | 0% |

### Circuit Breaker Lifecycle Proof

The complete lifecycle demonstrates:

| Phase | Circuit State | Error Rate | Behavior |
|-------|---------------|------------|----------|
| Initial | closed | 0% | Normal operation |
| Under Load | closed | <50% | Requests processed, failures recorded |
| Threshold Exceeded | open | >50% | Requests rejected immediately |
| Recovery Window | half-open | - | Limited test requests allowed |
| Recovery Success | closed | reset | Normal operation resumes |

### Viewing Resilience Logs

```bash
# Filter for resilience-related logs
./setup.sh logs | grep -E "(circuit|retry|error|fail|succeed)"
```

**Key Log Messages:**
- `"Resilient tool call succeeded"` - Successful call through circuit breaker
- `"Tool call failed after resilience attempts"` - All retries exhausted
- `"max retry attempts (3) exceeded"` - Retry limit reached
- `"circuit_state":"open"` - Circuit breaker is protecting the system

### Testing Configuration

The circuit breaker uses these defaults (from `resilience.DefaultConfig()`):

| Parameter | Value | Effect on Testing |
|-----------|-------|-------------------|
| `VolumeThreshold` | 10 | Need 10+ requests before circuit evaluates |
| `ErrorThreshold` | 0.5 | Circuit opens at 50% error rate |
| `SleepWindow` | 30s | Wait 30s before recovery attempt |
| `MaxRetries` | 3 | Each request retries 3 times internally |

**Tip:** With `rate_limit_after: 1` and 3 retries per request, you need approximately 4 requests to accumulate 10+ attempts and trigger the circuit breaker.

---

## Project Structure

```
agent-with-resilience/
├── main.go              # Entry point, framework initialization
├── research_agent.go    # Agent with circuit breaker map
├── handlers.go          # HTTP handlers with resilience
├── orchestration.go     # Tool calls using RetryWithCircuitBreaker
├── go.mod               # Dependencies (includes resilience module)
├── .env.example         # Environment configuration template
├── Dockerfile           # Multi-stage Docker build
├── Makefile             # Build automation
├── k8-deployment.yaml   # Kubernetes manifests
├── setup.sh             # One-click setup script
└── README.md            # This file
```

---

## What You Will Learn

1. **How to use `resilience.CreateCircuitBreaker()`** for proper dependency injection
2. **How to use `resilience.RetryWithCircuitBreaker()`** for combined retry and circuit breaker patterns
3. **How to use `cb.ExecuteWithTimeout()`** for timeout management
4. **How to implement graceful degradation** with partial results
5. **How to monitor circuit states** via `cb.GetMetrics()` and `cb.GetState()`
6. **How to react to state changes** via `cb.AddStateChangeListener()`

---

## Troubleshooting

### Circuit breaker keeps opening

- Check the error rate threshold (default 50%)
- Increase `VolumeThreshold` if you have low traffic
- Review logs for the actual errors causing failures

### Retries not working

- Ensure the error is retryable (not 401/403)
- Check if circuit breaker is open (will reject immediately)
- Verify timeout is not too short

### Health endpoint shows "degraded"

- One or more circuit breakers are in "open" state
- Check which service is failing and why
- Wait for `SleepWindow` (30s) for half-open recovery attempt

### Useful Commands

All day-to-day operations go through `setup.sh`. Run `./setup.sh help` to see every subcommand.

```bash
# Stream agent logs
./setup.sh logs

# Check pod / service status
./setup.sh status

# Port forward the agent to localhost:8093
./setup.sh forward

# Port forward agent + monitoring dashboards (Grafana, Prometheus, Jaeger)
./setup.sh forward-all

# Restart the deployment (e.g., to pick up a new ConfigMap from .env)
./setup.sh rollout

# Run the built-in resilience smoke test against the deployed agent
./setup.sh test

# Remove only the agent (keeps cluster + infra)
./setup.sh cleanup

# Tear down the entire Kind cluster
./setup.sh cleanup-all
```

---

## Next Steps

- **Add Telemetry**: Check out [agent-with-telemetry](../agent-with-telemetry/) for observability
- **Combine Both**: Create a production-grade agent with resilience and telemetry
- **Customize Configuration**: Tune circuit breaker thresholds for your use case

---

## License

Apache License 2.0 — see [LICENSE](../../LICENSE) and [NOTICE](../../NOTICE) for details.
