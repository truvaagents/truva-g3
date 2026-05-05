# Research Assistant Agent Example

A comprehensive example demonstrating how to build an **Agent** (active orchestrator) using the TruvaG3 framework. This agent can discover other components, orchestrate complex workflows, and use AI to intelligently coordinate multiple tools.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [What This Example Demonstrates](#what-this-example-demonstrates)
- [3-Phase AI-Powered Payload Generation](#3-phase-ai-powered-payload-generation)
- [Architecture](#architecture)
- [Agent vs Tool Differences](#agent-vs-tool-differences)
- [Testing the Agent](#testing-the-agent)
- [Understanding the Code](#understanding-the-code)
- [AI Integration Deep Dive](#ai-integration-deep-dive)
- [Configuration](#configuration)
- [Production Best Practices](#production-best-practices)
- [Docker Usage](#docker-usage)
- [Kubernetes Deployment](#kubernetes-deployment)
- [Monitoring and Observability](#monitoring-and-observability)
- [Advanced Usage and Customization](#advanced-usage-and-customization)
- [Common Issues and Solutions](#common-issues-and-solutions)
- [Redis Service Registry Example](#redis-service-registry-example)
- [Next Steps and Extensions](#next-steps-and-extensions)
- [Key Learnings](#key-learnings)

---

## How to Run This Example

Running this example locally is the best way to understand how the TruvaG3 framework enables agents to discover and orchestrate tools. Follow the steps below to get this example running.

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
| **Google Gemini** | [aistudio.google.com/apikey](https://aistudio.google.com/apikey) | Gemini models |
| **DeepSeek** | [platform.deepseek.com](https://platform.deepseek.com/) | Advanced reasoning |

**Auto-detection priority:** The agent automatically detects and uses the first available provider in this order:
1. OpenAI (OPENAI_API_KEY)
2. Groq (GROQ_API_KEY)
3. DeepSeek (DEEPSEEK_API_KEY)
4. Anthropic (ANTHROPIC_API_KEY)
5. Gemini (GEMINI_API_KEY)
6. Custom OpenAI-compatible (OPENAI_BASE_URL)

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

> **Important:** This agent requires tools to be deployed to function. The research-agent discovers and orchestrates other tools (weather-tool, geocoding-tool, etc.) to answer queries. You can deploy tools before or after the agent, but the agent won't be able to orchestrate workflows until tools are running.

### Quick Start (Recommended)

The fastest way to get everything running in your local:

```bash
cd examples/agent-example

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
# 2. Deploy cluster, infrastructure, and the agent
./setup.sh full-deploy

# 3. Deploy tools for the agent to discover (each tool has its own setup script)
cd ../weather-tool-v2 && ./setup.sh deploy && cd ..
cd ../geocoding-tool && ./setup.sh deploy && cd ..
cd ../currency-tool && ./setup.sh deploy && cd ..
```

**What `./setup.sh full-deploy` does:**
1. Creates a Kind Kubernetes cluster with proper port mappings
2. Deploys infrastructure (Redis, monitoring)
3. Builds and deploys the research-agent
4. Creates AI secrets from environment variables
5. Sets up port forwarding automatically

**What you need to do separately:**
- Deploy tools using each tool's setup script (Step 3 above)

Once complete, access the agent at:

| Service | URL | Description |
|---------|-----|-------------|
| **Agent API** | http://localhost:8090 | REST API for orchestration |
| **Health Check** | http://localhost:8090/health | Agent health status |
| **Capabilities** | http://localhost:8090/api/capabilities | List agent capabilities |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Set Up Kind Cluster and Redis

```bash
cd examples/agent-example
make setup
```

This creates a Kind cluster and deploys Redis for service discovery.

#### Step 2: Configure AI Provider

```bash
# Create .env from example (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
# Edit .env and uncomment/set your AI provider key(s)

# Load environment and create secrets
source .env
make create-secrets
```

#### Step 3: Build and Deploy the Agent

```bash
make deploy
```

#### Step 4: Deploy Tools for Discovery

Each tool has its own setup script:

```bash
# Deploy tools (from the examples directory)
cd ../weather-tool-v2 && ./setup.sh deploy
cd ../geocoding-tool && ./setup.sh deploy
cd ../currency-tool && ./setup.sh deploy
```

> **Note:** The `k8-deployment` directory contains shared infrastructure (Redis, Prometheus, etc.), not tools.

#### Step 5: Test the Deployment

```bash
make test
```

#### Step 6: Access the Agent

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/research-agent-service 8090:80

# In another terminal, test it
curl http://localhost:8090/health
curl http://localhost:8090/api/capabilities
```

### Available Make Commands

```bash
make setup      # Create Kind cluster, install Redis
make deploy     # Build and deploy the agent
make test       # Run automated tests
make logs       # View agent logs
make status     # Check deployment status
make clean      # Delete everything
make help       # Show all commands
```

---

## What This Example Demonstrates

- **Agent Pattern**: Active component that can discover and orchestrate other components
- **AI Integration**: Auto-detecting AI providers (OpenAI, Anthropic, Groq, etc.)
- **Service Discovery**: Finding and calling available tools dynamically
- **Intelligent Orchestration**: Using AI to plan and execute multi-step workflows
- **3-Phase Schema Discovery**: AI-powered payload generation with progressive enhancement
- **Production Patterns**: Resilient HTTP calls, error handling, caching
- **Kubernetes Deployment**: Complete K8s configuration with AI secrets

---

## 3-Phase AI-Powered Payload Generation

This example implements the complete 3-phase approach for AI-powered tool payload generation:

### Phase 1: Description-Based (Always Active)
- AI generates payloads from natural language capability descriptions
- ~85-90% accuracy baseline
- Works for all capabilities automatically

### Phase 2: Field-Hint-Based (Implemented)
- All capabilities include structured field hints (`InputSummary`)
- AI uses exact field names, types, and examples
- ~95% accuracy for tool calls
- See [research_agent.go:93-238](research_agent.go#L93-L238) for implementation

### Phase 3: Schema Validation (Optional)
- Full JSON Schema v7 validation before sending payloads
- Cached in Redis (0ms overhead after first fetch)
- Enable with `TRUVAG3_VALIDATE_PAYLOADS=true`
- See [orchestration.go:93-121](orchestration.go#L93-L121) for validation logic

**Example Capability with Phase 2:**
```go
r.RegisterCapability(core.Capability{
    Name: "research_topic",
    Description: "Researches a topic by discovering and coordinating relevant tools",
    // Phase 2: Field hints guide AI generation
    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {
                Name: "topic",
                Type: "string",
                Example: "latest developments in renewable energy",
                Description: "The research topic or question to investigate",
            },
        },
        // ... optional fields
    },
    Handler: r.handleResearchTopic,
})
```

**Learn More**: See [Tool Schema Discovery Guide](../../docs/building/TOOL_SCHEMA_DISCOVERY_GUIDE.md) for complete documentation.

---

## Architecture

```
+---------------------------------------------------+
|            Research Assistant Agent               |
|            (Active Orchestrator)                  |
+---------------------------------------------------+
| AI Capabilities:                                  |
| - Auto-detect providers (OpenAI/Anthropic/etc)   |
| - Intelligent analysis and synthesis             |
| - Workflow planning                              |
+---------------------------------------------------+
| Orchestration Capabilities:                       |
| - research_topic (AI + tool coordination)        |
| - discover_tools (service discovery)             |
| - analyze_data (AI-powered analysis)             |
| - orchestrate_workflow (multi-step coordination) |
+---------------------------------------------------+
| Framework Features:                               |
| - Full Discovery powers                           |
| - Redis registry + discovery                     |
| - AI client auto-injection                       |
| - Context propagation                            |
+---------------------------------------------------+
```

**Key Principle**: Agents are **active** - they can discover other components and orchestrate complex workflows.

---

## Agent vs Tool Differences

| Aspect | Tools (Passive) | Agents (Active) |
|--------|----------------|----------------|
| Discovery | Cannot discover others, can only register | Full discovery powers |
| Orchestration | Cannot call others | Can coordinate workflows |
| AI Integration | Can use AI internally | Can use AI for orchestration |
| Framework Injection | Registry, Logger, Memory | Discovery, Logger, Memory, AI |

---

## Testing the Agent

### Understanding the `ai_synthesis` Parameter

The research agent supports **hybrid operation** - combining tool orchestration with AI intelligence:

| Scenario | `ai_synthesis` Setting | Behavior | Use Case |
|----------|------------------|----------|----------|
| **Tools Available** | `false` | Tools only, basic text summary | Fast, deterministic results |
| **Tools Available** | `true` | Tools + AI synthesis | Intelligent analysis of tool results |
| **No Tools Available** | `false` | Empty results | N/A - need relevant tools |
| **No Tools Available** | `true` | **AI answers directly** | General knowledge questions |

**Key Discovery**: When `ai_synthesis: true` and no relevant tools are found, the agent automatically falls back to direct AI responses. This enables the agent to answer general questions:

```bash
# Example: Product recommendation (no tools available)
curl -X POST http://localhost:8090/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "Recommend top 3 wifi routers supporting 2 Gbps for home use",
    "ai_synthesis": true,
    "max_results": 1
  }'
# Works - AI provides recommendations directly

# Same query without AI
curl -X POST http://localhost:8090/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "Recommend top 3 wifi routers supporting 2 Gbps for home use",
    "ai_synthesis": false
  }'
# Returns empty results - no relevant tools
```

### Basic Testing

```bash
# Start port forwarding
kubectl port-forward -n truvag3-examples svc/research-agent-service 8090:80

# Test health endpoint
curl http://localhost:8090/health

# Discover available tools and agents
curl -X POST http://localhost:8090/api/capabilities/discover_tools \
  -H "Content-Type: application/json" -d '{}'

# Research a topic (orchestrates multiple tools)
curl -X POST http://localhost:8090/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "weather in New York",
    "ai_synthesis": true,
    "max_results": 5
  }'

# AI-powered data analysis (if AI provider configured)
curl -X POST http://localhost:8090/api/capabilities/analyze_data \
  -H "Content-Type: application/json" \
  -d '{
    "data": "Temperature: 22C, Humidity: 65%, Wind: 10 km/h, Condition: Partly cloudy"
  }'
```

### Expected Response (Research Topic)

```json
{
  "topic": "weather in New York",
  "summary": "Research completed successfully. The current weather in New York shows partly cloudy conditions with moderate temperature and humidity levels.",
  "tools_used": ["weather-service"],
  "results": [
    {
      "tool_name": "weather-service",
      "capability": "current_weather",
      "data": {
        "location": "New York",
        "temperature": 22.5,
        "humidity": 65,
        "condition": "partly cloudy"
      },
      "success": true,
      "duration": "120ms"
    }
  ],
  "ai_analysis": "Based on the weather data, New York is experiencing pleasant conditions with partly cloudy skies and comfortable temperatures around 22C...",
  "confidence": 1.0,
  "processing_time": "342ms",
  "metadata": {
    "tools_discovered": 2,
    "tools_used": 1,
    "ai_enabled": true
  }
}
```

---

## Understanding the Code

### Core Agent Pattern

```go
// 1. Create agent (active orchestrator)
agent := core.NewBaseAgent("research-assistant")

// 2. Add AI capabilities (optional but powerful)
aiClient, _ := ai.NewClient() // Auto-detects best provider
agent.AI = aiClient

// 3. Register orchestration capabilities
agent.RegisterCapability(core.Capability{
    Name:        "research_topic",
    Description: "Orchestrates research across multiple tools",
    Handler:     r.handleResearchTopic,
})

// 4. Framework auto-injects Discovery (unlike tools)
framework, _ := core.NewFramework(agent,
    core.WithRedisURL("redis://localhost:6379"),
    core.WithDiscovery(true), // Agents get full discovery
)

// 5. Use Discovery in handlers
func (r *ResearchAgent) handleResearchTopic(w http.ResponseWriter, req *http.Request) {
    // Discover available tools
    tools, _ := r.Discovery.Discover(ctx, core.DiscoveryFilter{
        Type: core.ComponentTypeTool,
    })

    // Orchestrate calls to multiple tools
    for _, tool := range tools {
        result := r.callTool(ctx, tool, query)
        // Process results...
    }

    // Use AI to synthesize results
    if r.aiClient != nil {
        analysis := r.generateAIAnalysis(ctx, topic, results)
    }
}
```

### AI Provider Auto-Detection

The agent automatically detects and configures the best available AI provider:

```go
// Auto-detection priority (in order):
// 1. OpenAI (OPENAI_API_KEY)
// 2. Groq (GROQ_API_KEY) - Fast inference, free tier
// 3. DeepSeek (DEEPSEEK_API_KEY) - Advanced reasoning
// 4. Anthropic (ANTHROPIC_API_KEY)
// 5. Gemini (GEMINI_API_KEY)
// 6. Custom OpenAI-compatible (OPENAI_BASE_URL)

aiClient, err := ai.NewClient() // Picks best available
if err != nil {
    // Agent works without AI, just less intelligent
    log.Printf("No AI provider available: %v", err)
}
```

### Dynamic Tool Discovery

```go
func (r *ResearchAgent) handleResearchTopic(w http.ResponseWriter, req *http.Request) {
    // Step 1: Discover all available tools
    tools, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{
        Type: core.ComponentTypeTool, // Only tools
    })

    // Step 2: Filter relevant tools for the topic
    var relevantTools []*core.ServiceInfo
    for _, tool := range tools {
        if r.isToolRelevant(tool, request.Topic) {
            relevantTools = append(relevantTools, tool)
        }
    }

    // Step 3: Call each relevant tool
    var results []ToolResult
    for _, tool := range relevantTools {
        result := r.callTool(ctx, tool, request.Topic)
        results = append(results, result)
    }

    // Step 4: Use AI to synthesize (if available)
    if r.aiClient != nil && request.AISynthesis {
        analysis := r.generateAIAnalysis(ctx, request.Topic, results)
    }
}
```

### Resilient Tool Calling

```go
func (r *ResearchAgent) callTool(ctx context.Context, tool *core.ServiceInfo, query string) *ToolResult {
    // Build endpoint URL
    capability := tool.Capabilities[0] // Use first capability
    endpoint := capability.Endpoint
    if endpoint == "" {
        endpoint = fmt.Sprintf("/api/capabilities/%s", capability.Name)
    }
    url := fmt.Sprintf("http://%s:%d%s", tool.Address, tool.Port, endpoint)

    // Create HTTP request with timeout
    httpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
    defer cancel()

    req, _ := http.NewRequestWithContext(httpCtx, "POST", url, body)
    req.Header.Set("Content-Type", "application/json")

    // Make resilient HTTP call
    client := &http.Client{Timeout: 10 * time.Second}
    resp, err := client.Do(req)
    if err != nil {
        return &ToolResult{Success: false, Error: err.Error()}
    }
    defer resp.Body.Close()

    // Handle response
    body, _ := io.ReadAll(resp.Body)
    var data interface{}
    json.Unmarshal(body, &data)

    return &ToolResult{
        ToolName:   tool.Name,
        Data:       data,
        Success:    resp.StatusCode < 400,
        Duration:   time.Since(startTime).String(),
    }
}
```

---

## AI Integration Deep Dive

### Supported Providers

The agent automatically works with 20+ AI providers:

```bash
# Native providers (optimized implementations)
export OPENAI_API_KEY="sk-..."      # OpenAI GPT models
export ANTHROPIC_API_KEY="sk-ant-..." # Claude models
export GEMINI_API_KEY="..."         # Google Gemini

# OpenAI-compatible providers (one implementation, many services)
export GROQ_API_KEY="gsk-..."       # Ultra-fast inference
export DEEPSEEK_API_KEY="..."       # Advanced reasoning
export XAI_API_KEY="..."            # Elon's xAI Grok

# Custom OpenAI-compatible endpoints
export OPENAI_BASE_URL="https://your-llm.company.com/v1"
export OPENAI_API_KEY="your-key"

# Local models
export OPENAI_BASE_URL="http://localhost:11434/v1" # Ollama
export OPENAI_BASE_URL="http://localhost:8000/v1"  # vLLM
```

### AI Usage Patterns

#### 1. Intelligent Topic Analysis
```go
func (r *ResearchAgent) generateAIAnalysis(ctx context.Context, topic string, results []ToolResult) string {
    prompt := fmt.Sprintf(`Analyze research results for: "%s"

Results from tools:
%s

Provide a comprehensive summary with key insights.`, topic, formatResults(results))

    response, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
        Temperature: 0.4, // More focused analysis
        MaxTokens:   800,
    })
    return response.Content
}
```

#### 2. Dynamic Workflow Planning
```go
// Future enhancement: AI plans which tools to call
prompt := fmt.Sprintf(`I need to research "%s".
Available tools: %s
Plan the optimal sequence of tool calls.`, topic, availableTools)

plan, _ := r.aiClient.GenerateResponse(ctx, prompt, options)
// Execute the AI-generated plan
```

#### 3. Data Synthesis
```go
func (r *ResearchAgent) handleAnalyzeData(w http.ResponseWriter, req *http.Request) {
    if r.aiClient == nil {
        http.Error(w, "AI not available", http.StatusServiceUnavailable)
        return
    }

    prompt := fmt.Sprintf(`Analyze this data and provide insights:
%s

Provide:
1. Key findings
2. Patterns/trends
3. Recommendations
4. Confidence level`, requestData["data"])

    analysis, _ := r.aiClient.GenerateResponse(req.Context(), prompt, &core.AIOptions{
        Temperature: 0.3, // Analytical mode
        MaxTokens:   1000,
    })

    response := map[string]interface{}{
        "analysis":    analysis.Content,
        "model":       analysis.Model,
        "tokens_used": analysis.Usage.TotalTokens,
    }

    json.NewEncoder(w).Encode(response)
}
```

---

## Configuration

### Environment Variables

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

The `.env.example` file contains comprehensive documentation for all options.

### Core Configuration

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8090` | No |
| `NAMESPACE` | Service namespace | `examples` | No |
| `DEV_MODE` | Development mode | `false` | No |

### AI Provider Configuration

| Variable | Description | Required |
|----------|-------------|----------|
| `OPENAI_API_KEY` | OpenAI API key | Yes* |
| `ANTHROPIC_API_KEY` | Anthropic API key | Yes* |
| `GROQ_API_KEY` | Groq API key | Yes* |
| `GEMINI_API_KEY` | Gemini API key | Yes* |
| `DEEPSEEK_API_KEY` | DeepSeek API key | Yes* |

*At least one AI provider key is required for full functionality.

### Production Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `TRUVAG3_LOG_LEVEL` | Log level (info, debug, error) | `info` |
| `TRUVAG3_LOG_FORMAT` | Log format (json, text) | `json` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP endpoint for telemetry | - |

### Framework Options (v0.6.4+ Pattern)

```go
// Production pattern: ALL configuration from environment
framework, _ := core.NewFramework(agent,
    // Read from environment - no hardcoded values
    core.WithRedisURL(os.Getenv("REDIS_URL")),     // Required
    core.WithNamespace(os.Getenv("NAMESPACE")),    // Optional

    // Port from environment with proper parsing
    core.WithPort(getPortFromEnv()),

    // Development mode from environment
    core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),

    // Discovery enabled for agents
    core.WithDiscovery(true, "redis"),

    // CORS configuration (can be environment-based too)
    core.WithCORS(getCORSFromEnv(), true),
)

// Helper functions for production
func getPortFromEnv() int {
    portStr := os.Getenv("PORT")
    if portStr == "" {
        return 8090 // Default only if not set
    }
    port, err := strconv.Atoi(portStr)
    if err != nil {
        log.Fatalf("Invalid PORT: %v", err)
    }
    return port
}

func getCORSFromEnv() []string {
    origins := os.Getenv("CORS_ORIGINS")
    if origins == "" {
        return []string{"*"} // Default for development
    }
    return strings.Split(origins, ",")
}
```

### AI Configuration Options

```go
// Auto-detection (recommended)
aiClient, err := ai.NewClient()

// Explicit provider
aiClient, err := ai.NewClient(
    ai.WithProvider("anthropic"),
    ai.WithAPIKey("sk-ant-..."),
    ai.WithModel("claude-3-sonnet-20240229"),
)

// Multiple providers with fallback
primary, _ := ai.NewClient(ai.WithProvider("openai"))
fallback, _ := ai.NewClient(ai.WithProvider("groq"))
```

---

## Production Best Practices

### 1. Environment-Based Configuration
**NEVER hardcode configuration values** in production code:

```go
// BAD - Hardcoded values
core.WithRedisURL("redis://localhost:6379")
core.WithPort(8090)

// GOOD - Environment-based
core.WithRedisURL(os.Getenv("REDIS_URL"))
core.WithPort(getPortFromEnv())
```

### 2. Health Checks and Readiness Probes
Always implement comprehensive health checks:

```go
// Liveness probe - is the service running?
func (r *ResearchAgent) handleHealth(w http.ResponseWriter, req *http.Request) {
    health := map[string]interface{}{
        "status": "healthy",
        "timestamp": time.Now().Unix(),
    }

    // Check critical dependencies
    if err := r.checkRedis(); err != nil {
        health["status"] = "unhealthy"
        health["redis"] = err.Error()
        w.WriteHeader(http.StatusServiceUnavailable)
    }

    json.NewEncoder(w).Encode(health)
}

// Readiness probe - is the service ready to handle requests?
func (r *ResearchAgent) handleReady(w http.ResponseWriter, req *http.Request) {
    // Check if discovery is initialized
    if r.Discovery == nil {
        http.Error(w, "Discovery not ready", http.StatusServiceUnavailable)
        return
    }

    // Check if we can discover services
    ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
    defer cancel()

    _, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{})
    if err != nil {
        http.Error(w, "Discovery check failed", http.StatusServiceUnavailable)
        return
    }

    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}
```

### 3. Graceful Shutdown
Implement proper shutdown handling:

```go
func main() {
    // ... framework setup ...

    // Graceful shutdown
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        log.Println("Shutting down gracefully...")

        // Give ongoing requests time to complete
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        if err := framework.Shutdown(ctx); err != nil {
            log.Printf("Shutdown error: %v", err)
        }
        os.Exit(0)
    }()

    framework.Run()
}
```

### 4. Error Handling and Retries
Implement resilient error handling with retries:

```go
func (r *ResearchAgent) callToolWithRetry(ctx context.Context, tool *core.ServiceInfo, query string) (*ToolResult, error) {
    var lastErr error

    for attempt := 0; attempt < 3; attempt++ {
        if attempt > 0 {
            // Exponential backoff
            time.Sleep(time.Duration(math.Pow(2, float64(attempt))) * time.Second)
        }

        result, err := r.callTool(ctx, tool, query)
        if err == nil {
            return result, nil
        }

        lastErr = err
        log.Printf("Tool call attempt %d failed: %v", attempt+1, err)

        // Don't retry on context cancellation
        if errors.Is(err, context.Canceled) {
            return nil, err
        }
    }

    return nil, fmt.Errorf("all retry attempts failed: %w", lastErr)
}
```

> **See Also**: For advanced error handling patterns including AI-powered error correction and intelligent retry strategies, see the [Intelligent Error Handling Guide](https://github.com/truvaagents/truva-g3/blob/main/docs/orchestration/INTELLIGENT_ERROR_HANDLING.md).

### 5. Structured Logging
Use structured logging for better observability:

```go
import "github.com/sirupsen/logrus"

func setupLogging() {
    // Configure based on environment
    if os.Getenv("TRUVAG3_LOG_FORMAT") == "json" {
        logrus.SetFormatter(&logrus.JSONFormatter{})
    }

    level, err := logrus.ParseLevel(os.Getenv("TRUVAG3_LOG_LEVEL"))
    if err == nil {
        logrus.SetLevel(level)
    }

    // Add context to all logs
    logrus.WithFields(logrus.Fields{
        "service": "research-agent",
        "version": "v0.6.4",
        "namespace": os.Getenv("NAMESPACE"),
    }).Info("Agent starting")
}
```

### 6. Resource Limits and Monitoring
Set appropriate resource limits and monitor usage:

```yaml
# In k8-deployment.yaml
resources:
  requests:
    memory: "256Mi"  # Baseline memory
    cpu: "200m"      # Baseline CPU
  limits:
    memory: "512Mi"  # Prevent memory leaks from affecting cluster
    cpu: "1000m"     # Prevent CPU spikes
```

### 7. Security Best Practices

```go
// Input validation
func (r *ResearchAgent) validateRequest(req *ResearchRequest) error {
    if len(req.Topic) > 500 {
        return fmt.Errorf("topic too long")
    }

    // Sanitize input to prevent injection
    req.Topic = strings.TrimSpace(req.Topic)

    // Validate rate limits
    if !r.checkRateLimit(req.ClientID) {
        return fmt.Errorf("rate limit exceeded")
    }

    return nil
}

// Secure AI prompts
func (r *ResearchAgent) sanitizePrompt(userInput string) string {
    // Remove potential injection patterns
    sanitized := strings.ReplaceAll(userInput, "```", "")
    sanitized = strings.ReplaceAll(sanitized, "system:", "")
    return sanitized
}
```

### 8. Circuit Breaker Pattern
Prevent cascade failures:

```go
import "github.com/sony/gobreaker"

func setupCircuitBreaker() *gobreaker.CircuitBreaker {
    settings := gobreaker.Settings{
        Name:        "ToolCall",
        MaxRequests: 3,
        Interval:    10 * time.Second,
        Timeout:     30 * time.Second,
        OnStateChange: func(name string, from, to gobreaker.State) {
            log.Printf("Circuit breaker %s: %s -> %s", name, from, to)
        },
    }
    return gobreaker.NewCircuitBreaker(settings)
}
```

### 9. Distributed Tracing
Implement proper tracing for debugging:

```go
import "go.opentelemetry.io/otel"

func (r *ResearchAgent) handleWithTracing(w http.ResponseWriter, req *http.Request) {
    ctx, span := otel.Tracer("research-agent").Start(req.Context(), "research_topic")
    defer span.End()

    // Add attributes
    span.SetAttributes(
        attribute.String("topic", request.Topic),
        attribute.Bool("ai_synthesis", request.AISynthesis),
    )

    // Pass context through call chain
    result, err := r.orchestrateResearch(ctx, request)
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
    }
}
```

### 10. Configuration Validation
Validate all configuration at startup:

```go
func validateConfig() error {
    required := []string{"REDIS_URL", "PORT"}

    for _, env := range required {
        if os.Getenv(env) == "" {
            return fmt.Errorf("required environment variable %s not set", env)
        }
    }

    // Validate Redis URL format
    redisURL := os.Getenv("REDIS_URL")
    if !strings.HasPrefix(redisURL, "redis://") {
        return fmt.Errorf("invalid REDIS_URL format")
    }

    // Validate port is numeric
    if _, err := strconv.Atoi(os.Getenv("PORT")); err != nil {
        return fmt.Errorf("invalid PORT: %v", err)
    }

    return nil
}

func main() {
    if err := validateConfig(); err != nil {
        log.Fatalf("Configuration error: %v", err)
    }
    // ... rest of setup ...
}
```

---

## Docker Usage

### Build and Run

```bash
# Build
docker build -t research-agent:latest .

# Run with AI (requires API key)
docker run -p 8090:8090 \
  -e REDIS_URL=redis://host.docker.internal:6379 \
  -e OPENAI_API_KEY=$OPENAI_API_KEY \
  research-agent:latest

# Run complete stack
cd examples/
docker-compose up
```

---

## Kubernetes Deployment

### Quick Deploy

```bash
# 1. Deploy infrastructure
kubectl apply -f k8-deployment/namespace.yaml
kubectl apply -f k8-deployment/redis.yaml

# 2. Create AI secrets
kubectl create secret generic ai-keys \
  --from-literal=openai-api-key=$OPENAI_API_KEY \
  --from-literal=groq-api-key=$GROQ_API_KEY \
  -n truvag3-examples

# 3. Deploy agent
kubectl apply -f agent-example/k8-deployment.yaml

# 4. Check status
kubectl get pods -n truvag3-examples -l app=research-agent
kubectl logs -f deployment/research-agent -n truvag3-examples
```

### Complete Stack with Monitoring

```bash
# Deploy everything including monitoring
kubectl apply -k k8-deployment/

# Access services
kubectl port-forward svc/research-agent-service 8090:80 -n truvag3-examples
kubectl port-forward svc/grafana 3000:80 -n truvag3-examples
kubectl port-forward svc/jaeger-query 16686:80 -n truvag3-examples
```

### Scaling Configuration

```bash
# Agents typically run as singletons for coordination
# But can be scaled if stateless

kubectl scale deployment research-agent --replicas=2 -n truvag3-examples

# Configure load balancing for stateless operations
kubectl annotate service research-agent-service \
  service.kubernetes.io/load-balancer-class=nlb -n truvag3-examples
```

---

## Monitoring and Observability

### Metrics (Prometheus)

Key metrics to monitor:
- `up{job="truvag3-agents"}` - Agent availability
- `discovery_requests_total` - Service discovery calls
- `orchestration_requests_total` - Workflow orchestrations
- `ai_requests_total` - AI API calls
- `tool_calls_total` - Tool invocations

### Distributed Tracing (Jaeger)

The agent creates spans for:
- Service discovery operations
- Tool calls with latency
- AI API requests
- Workflow orchestration steps

### Structured Logs

```json
{
  "level": "info",
  "msg": "Starting research topic orchestration",
  "topic": "weather in New York",
  "tools_discovered": 2,
  "ai_enabled": true,
  "timestamp": "2024-01-15T10:30:00Z"
}
```

---

## Advanced Usage and Customization

### Multi-Provider AI Strategy

```go
type ResilientAIAgent struct {
    primary   core.AIClient
    fallback  core.AIClient
    local     core.AIClient
}

func (a *ResilientAIAgent) ProcessWithAI(ctx context.Context, prompt string, sensitive bool) (*core.AIResponse, error) {
    if sensitive {
        return a.local.GenerateResponse(ctx, prompt, options)
    }

    // Try primary first
    response, err := a.primary.GenerateResponse(ctx, prompt, options)
    if err != nil {
        // Fallback on error
        return a.fallback.GenerateResponse(ctx, prompt, options)
    }
    return response, nil
}
```

### Custom Orchestration Logic

```go
func (r *ResearchAgent) orchestrateWeatherAnalysis(ctx context.Context, params map[string]interface{}) (interface{}, error) {
    location := params["location"].(string)

    // Step 1: Get current weather
    currentWeather := r.callWeatherTool(ctx, "current_weather", location)

    // Step 2: Get forecast if requested
    var forecast interface{}
    if params["include_forecast"].(bool) {
        forecast = r.callWeatherTool(ctx, "forecast", location)
    }

    // Step 3: Use AI to analyze and correlate
    if r.aiClient != nil {
        analysis := r.generateWeatherInsights(ctx, currentWeather, forecast)
        return map[string]interface{}{
            "current": currentWeather,
            "forecast": forecast,
            "insights": analysis,
        }, nil
    }

    return map[string]interface{}{
        "current": currentWeather,
        "forecast": forecast,
    }, nil
}
```

### AI Coding Assistant Prompts

Ask your AI assistant:

```
"Help me add a new capability that discovers and orchestrates financial data tools"

"Convert this research agent to specialize in scientific literature analysis"

"Add memory/conversation context to this agent for multi-turn interactions"

"Help me implement a circuit breaker pattern for tool calls in this agent"

"Add streaming responses for real-time orchestration updates"
```

---

## Common Issues and Solutions

### Issue: Agent can't discover any tools
```bash
# Check Redis connection
redis-cli -u $REDIS_URL ping

# Check if tools are registered
redis-cli KEYS "truvag3:services:*"

# Verify agent has Discovery permissions
kubectl logs deployment/research-agent -n truvag3-examples | grep -i discovery
```

### Issue: AI requests failing
```bash
# Check API key configuration
kubectl get secret ai-keys -o yaml -n truvag3-examples

# Test API key manually
curl -H "Authorization: Bearer $OPENAI_API_KEY" \
  https://api.openai.com/v1/models

# Check agent logs for AI errors
kubectl logs deployment/research-agent -n truvag3-examples | grep -i "ai\|openai\|anthropic"
```

### Issue: Tool calls timing out
```go
// Increase timeout in tool calls
httpCtx, cancel := context.WithTimeout(ctx, 30*time.Second) // Increased from 10s

// Add retry logic
for attempts := 0; attempts < 3; attempts++ {
    result, err := r.callTool(ctx, tool, query)
    if err == nil {
        return result
    }
    time.Sleep(time.Duration(attempts) * time.Second)
}
```

### Issue: High resource usage
```yaml
# In k8-deployment.yaml, adjust resource limits
resources:
  requests:
    memory: "256Mi"  # Increased from 128Mi
    cpu: "300m"      # Increased from 200m
  limits:
    memory: "512Mi"  # Increased from 256Mi
    cpu: "1000m"     # Increased from 500m
```

---

## Redis Service Registry Example

When the research-agent is deployed and running, it automatically registers itself in Redis. Here's what the agent's service entry looks like:

```json
{
  "name": "research-assistant",
  "type": "agent",
  "address": "research-agent-service.truvag3-examples.svc.cluster.local",
  "port": 80,
  "namespace": "truvag3-examples",
  "capabilities": [
    {
      "name": "research_topic",
      "description": "Researches a topic by discovering and coordinating relevant tools, optionally using AI for intelligent analysis",
      "endpoint": "/api/capabilities/research_topic",
      "input_types": ["application/json"],
      "output_types": ["application/json"],
      "input_summary": {
        "required_fields": [
          {
            "name": "topic",
            "type": "string",
            "example": "weather in New York",
            "description": "Research topic or question to investigate"
          }
        ],
        "optional_fields": [
          {
            "name": "ai_synthesis",
            "type": "boolean",
            "example": true,
            "description": "Enable AI-powered analysis and synthesis"
          },
          {
            "name": "max_results",
            "type": "integer",
            "example": 5,
            "description": "Maximum number of tool results to collect"
          }
        ]
      }
    },
    {
      "name": "discover_tools",
      "description": "Discovers available tools and services in the system",
      "endpoint": "/api/capabilities/discover_tools",
      "input_types": ["application/json"],
      "output_types": ["application/json"],
      "input_summary": {
        "optional_fields": [
          {
            "name": "type",
            "type": "string",
            "example": "tool",
            "description": "Filter by component type (tool/agent)"
          },
          {
            "name": "capability",
            "type": "string",
            "example": "current_weather",
            "description": "Filter by capability name"
          }
        ]
      }
    },
    {
      "name": "analyze_data",
      "description": "Uses AI to analyze and provide insights on provided data",
      "endpoint": "/api/capabilities/analyze_data",
      "input_types": ["application/json"],
      "output_types": ["application/json"],
      "input_summary": {
        "required_fields": [
          {
            "name": "data",
            "type": "string",
            "example": "Temperature: 22C, Humidity: 65%",
            "description": "Data to analyze"
          }
        ],
        "optional_fields": [
          {
            "name": "analysis_type",
            "type": "string",
            "example": "summary",
            "description": "Type of analysis (summary/detailed/trends)"
          }
        ]
      }
    },
    {
      "name": "orchestrate_workflow",
      "description": "Orchestrates complex multi-step workflows across discovered tools",
      "endpoint": "/api/capabilities/orchestrate_workflow",
      "input_types": ["application/json"],
      "output_types": ["application/json"],
      "input_summary": {
        "required_fields": [
          {
            "name": "workflow_type",
            "type": "string",
            "example": "weather_analysis",
            "description": "Type of workflow to execute"
          }
        ],
        "optional_fields": [
          {
            "name": "parameters",
            "type": "object",
            "example": {"location": "New York"},
            "description": "Workflow-specific parameters"
          }
        ]
      }
    }
  ],
  "metadata": {
    "version": "v1.0.0",
    "framework_version": "0.6.4",
    "discovery_enabled": true,
    "ai_enabled": true,
    "ai_provider": "openai",
    "last_heartbeat": "2025-11-10T05:15:25Z"
  },
  "health_endpoint": "/health",
  "registered_at": "2025-11-10T04:45:17Z",
  "last_seen": "2025-11-10T05:15:25Z",
  "ttl": 30
}
```

### Understanding the Agent Registry Data

**Key Differences from Tools:**
- **type**: Set to `agent` (active orchestrator with discovery powers)
- **ai_enabled**: Indicates AI integration is available
- **ai_provider**: Shows which AI provider is configured (openai, anthropic, groq, etc.)
- **capabilities**: Orchestration-focused capabilities that coordinate multiple tools

**Agent-Specific Features:**
- Agents can **discover** other services using the Discovery API
- Agents can **orchestrate** complex workflows across multiple tools
- Agents can use **AI** for intelligent analysis and decision-making
- Agents typically run as singletons or small replicas for coordination

**Service Discovery Pattern:**
- Both pod replicas send heartbeats to the same Redis key: `truvag3:services:research-assistant`
- Kubernetes Service (`research-agent-service`) load-balances traffic across pods
- Other agents/tools discover one service entry, Kubernetes handles routing
- Heartbeat keeps TTL fresh - service auto-expires if pods stop

**Redis Index Structure:**
```
truvag3:services:research-assistant       -> Full service data (30s TTL)
truvag3:types:agent                       -> Set of all agents (60s TTL)
truvag3:names:research-assistant          -> Name index (60s TTL)
truvag3:capabilities:research_topic       -> Capability index (60s TTL)
truvag3:capabilities:discover_tools       -> Capability index (60s TTL)
truvag3:capabilities:analyze_data         -> Capability index (60s TTL)
truvag3:capabilities:orchestrate_workflow -> Capability index (60s TTL)
```

**Phase 2 Input Summary:**
All capabilities include `input_summary` with field hints:
- **required_fields**: Fields that must be provided
- **optional_fields**: Fields that enhance functionality
- Each field includes: name, type, example, description
- AI uses these hints for 95% accurate payload generation

You can inspect this data in your cluster:
```bash
# Get the full agent entry
kubectl exec -it deployment/redis -n default -- \
  redis-cli GET "truvag3:services:research-assistant"

# List all registered services
kubectl exec -it deployment/redis -n default -- \
  redis-cli KEYS "truvag3:services:*"

# See all agents
kubectl exec -it deployment/redis -n default -- \
  redis-cli SMEMBERS "truvag3:types:agent"

# Check which services have specific capabilities
kubectl exec -it deployment/redis -n default -- \
  redis-cli SMEMBERS "truvag3:capabilities:research_topic"
```

**How Agents Use Discovery:**
```go
// Agent discovers tools at runtime
tools, err := agent.Discovery.Discover(ctx, core.DiscoveryFilter{
    Type: core.ComponentTypeTool,
    Capability: "current_weather",
})

// Agent calls discovered tool
for _, tool := range tools {
    result := agent.callTool(ctx, tool, payload)
    // Process results...
}

// Agent uses AI to synthesize
if agent.AI != nil {
    analysis := agent.AI.GenerateResponse(ctx, prompt, options)
}
```

---

## Next Steps and Extensions

### 1. Add More AI Capabilities
- **Conversation Memory**: Multi-turn interactions
- **Streaming Responses**: Real-time updates
- **Vision Analysis**: Image processing capabilities
- **Code Generation**: Dynamic tool creation

### 2. Advanced Orchestration
- **Workflow Engine**: Complex multi-step processes
- **Event-Driven**: React to tool/agent events
- **Parallel Execution**: Concurrent tool calls
- **Circuit Breakers**: Resilience patterns

### 3. Production Enhancements
- **Authentication**: Secure agent APIs
- **Rate Limiting**: Protect downstream tools
- **Caching**: Intelligent response caching
- **Load Balancing**: Scale across regions

### 4. Domain Specialization
- **Financial Agent**: Market analysis and trading
- **Healthcare Agent**: Medical data orchestration
- **DevOps Agent**: Infrastructure coordination
- **Scientific Agent**: Research paper analysis

---

## Key Learnings

- **Agents are Active**: They can discover and coordinate other components
- **AI Amplifies Intelligence**: Auto-detection makes AI integration seamless
- **Discovery Powers Orchestration**: Dynamic tool calling enables flexible workflows
- **Resilience is Key**: Handle failures gracefully in distributed systems
- **Context Propagation**: Maintain request context across tool calls
- **Framework Does the Heavy Lifting**: Focus on business logic, not infrastructure

This agent can now discover your tools and create intelligent workflows combining multiple services!

---

**Next**: Try running both examples together to see the full tool + agent orchestration in action.
