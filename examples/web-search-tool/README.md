# Web Search Tool

A TruvaG3 tool that provides web search capabilities using the [Tavily Search API](https://tavily.com/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components. It features a pluggable provider architecture with Tavily for real results and a mock provider for development.

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

This tool provides web search capabilities that agents can discover and use. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

#### 5. Tavily Search API Key (Optional)

The Tavily API key is **optional**. Without it, the tool uses a mock provider that returns realistic sample data, which is useful for development and testing.

**To get real web search results:**

1. Visit [tavily.com](https://tavily.com/)
2. Sign up for a free account
3. Navigate to your dashboard
4. Copy your API key

**Free tier includes:**
- 1,000 API credits per month
- Basic search depth (1 credit per request)
- Web and news search types
- Relevance-scored results with snippets

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

The fastest way to get the web search tool running:

```bash
cd examples/web-search-tool

# Deploy to Kubernetes (requires cluster and Redis to be running)
./setup.sh deploy
```

**Optional:** If you want real web search results, set your Tavily API key before deploying:

```bash
# Create a .env file with your API key
echo "SEARCH_API_KEY=your-tavily-api-key-here" > .env
echo "SEARCH_PROVIDER=tavily" >> .env
```

- `SEARCH_API_KEY=your-api-key-here` (Get free key at [tavily.com](https://tavily.com/))
- `SEARCH_PROVIDER=tavily` (Set provider to tavily; defaults to mock)
- Without an API key, the tool returns realistic mock data (useful for development)

After reviewing your configuration, continue with deployment:

```bash
# Deploy to Kubernetes (requires cluster and Redis to be running)
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
| **Web Search API** | http://localhost:8341 | Web search data API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The web search tool requires Redis for service discovery. If you haven't already set up infrastructure, run these from this directory:

```bash
cd examples/web-search-tool

./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

#### Step 2: Build and Deploy

`setup.sh` handles the Docker build, Kind image load, namespace + ConfigMap creation, and manifest apply. Use these subcommands instead of raw `kubectl`:

```bash
cd examples/web-search-tool

# Build the Docker image only (does not deploy)
./setup.sh docker-build

# Full deploy: build + load into Kind + create namespace + ConfigMap from .env + apply manifest
./setup.sh deploy

# Verify deployment
./setup.sh status
```

> **Tip:** If you don't already have a cluster and infrastructure, `./setup.sh full-deploy` does everything from scratch in one shot — cluster, monitoring, tool, and port forwards.

#### Step 3: Test the Tool

```bash
# Port forward the tool service to localhost:8341
./setup.sh forward

# Test web search
curl -X POST http://localhost:8341/api/capabilities/web_search \
  -H "Content-Type: application/json" \
  -d '{"query": "best beach destinations Caribbean"}'
```

---

## Features

- **Web Search** - Search the web for general information with relevance-scored results
- **News Search** - Fetch recent news articles on any topic
- **Pluggable Providers** - Tavily for production, mock provider for development
- **Result Caching** - 15-minute cache to reduce API calls and improve response times
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Graceful Fallback** - Uses mock data when API key is not configured
- **Distributed Tracing** - Full OpenTelemetry integration with trace context propagation

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Web Search (`web_search`)

**Endpoint:** `/api/capabilities/web_search`

Searches the web for general information when no specialized tool exists. Returns relevance-scored results with titles, snippets, and URLs.

**Request:**
```json
{
  "query": "best beach destinations Caribbean",
  "max_results": 5,
  "search_type": "web"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `query` | string | Yes | Search terms - be specific for better results |
| `max_results` | integer | No | Number of results to return (1-10, default 5) |
| `search_type` | string | No | Type of search: `web` or `news` (default `web`) |

**Response:**
```json
{
  "success": true,
  "data": {
    "query": "best beach destinations Caribbean",
    "results": [
      {
        "title": "10 Best Caribbean Beach Destinations for 2026",
        "snippet": "Discover the top Caribbean beach destinations including Turks and Caicos, Aruba, and the Bahamas...",
        "url": "https://example.com/caribbean-beaches",
        "score": 0.95
      },
      {
        "title": "Turks and Caicos: Ultimate Beach Paradise",
        "snippet": "Grace Bay Beach in Turks and Caicos consistently ranks as one of the world's best beaches...",
        "url": "https://example.com/turks-caicos-guide",
        "score": 0.92
      }
    ],
    "search_time": "245.3ms",
    "provider": "tavily"
  }
}
```

**News search example:**
```json
{
  "query": "latest AI developments",
  "max_results": 5,
  "search_type": "news"
}
```

**News response includes `published_at` dates:**
```json
{
  "success": true,
  "data": {
    "query": "latest AI developments",
    "results": [
      {
        "title": "OpenAI Announces GPT-5 with Enhanced Reasoning Capabilities",
        "snippet": "OpenAI has unveiled GPT-5, featuring breakthrough improvements in mathematical reasoning...",
        "url": "https://example.com/news/gpt5-announcement",
        "published_at": "2026-01-30T10:30:00Z",
        "score": 0.96
      }
    ],
    "search_time": "312.1ms",
    "provider": "tavily"
  }
}
```

---

## Architecture

```
Web Search Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Routes to configured search provider
    |       |
    |       +-- Tavily Provider (production - real web results)
    |       +-- Mock Provider (development - sample data)
    |
    +-- Caches results for 15 minutes
    +-- Returns standardized responses

Agents (Active)
    |
    +-- Discover web search tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Provider Architecture

The tool uses a pluggable provider interface (`providers.SearchProvider`) that allows swapping search backends without changing handler logic:

- **Tavily Provider** (`tavily_client.go`) - Calls the Tavily Search API with Bearer token auth, supports web and news search types, uses traced HTTP client for distributed tracing
- **Mock Provider** (`providers/mock.go`) - Returns contextual sample results based on query keywords (beach, travel, news, weather, etc.), useful for development and testing

### Integration with Agents

Once deployed, the web search tool is automatically discovered by agents via Redis. You can also query the tool directly:

```bash
# Direct tool access
curl -X POST http://localhost:8341/api/capabilities/web_search \
  -H "Content-Type: application/json" \
  -d '{
    "query": "best Caribbean beach destinations for families",
    "max_results": 5
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `SEARCH_API_KEY` | Tavily API key for real search results | - | No* |
| `SEARCH_PROVIDER` | Search provider to use (`tavily` or `mock`) | `mock` | No |
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8341` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

*Tool works without API key but returns mock data. If `SEARCH_PROVIDER` is set to `tavily` but `SEARCH_API_KEY` is empty, the tool automatically falls back to the mock provider.

---

## API Rate Limits

Free tier limits (Tavily):

| Limit | Value |
|-------|-------|
| **Monthly credits** | 1,000 |
| **Basic search** | 1 credit per request |
| **Advanced search** | 2 credits per request |

The tool implements:
- 15-minute result caching to reduce API calls
- Graceful fallback to mock data on errors
- Structured error logging for rate limit tracking
- Semantic HTTP status codes (400 for parameter errors, 502 for upstream failures)

---

## Project Structure

```
web-search-tool/
├── main.go                 # Entry point, framework setup, provider creation
├── search_handler.go       # HTTP handler for web_search capability
├── tavily_client.go        # Tavily Search API client with tracing
├── providers/
│   ├── provider.go         # SearchProvider interface and SearchResult type
│   └── mock.go             # Mock provider for development/testing
├── go.mod                  # Go module definition
├── Dockerfile              # Container image definition
├── k8-deployment.yaml      # Kubernetes manifests
├── setup.sh                # Build, deploy, and management script
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

# Should show: truvag3:service:web-search-service
```

**2. API errors**

```bash
# Stream logs and grep for API key issues
./setup.sh logs | grep -i "api"

# Common issues:
# - Invalid API key: Check secret configuration
# - Rate limit: Wait or upgrade Tavily plan
# - Empty query: Ensure query field is non-empty
```

**3. Mock data being used when API key is configured**

If you see `"provider": "mock"` in responses:
1. Verify `SEARCH_API_KEY` is set in the secret
2. Verify `SEARCH_PROVIDER` is set to `tavily`
3. Check pod environment: `kubectl exec -n truvag3-examples <pod-name> -- env | grep SEARCH`
4. Restart deployment: `./setup.sh rollout`

**4. Docker build fails**

```bash
# Ensure Docker is running
docker info

# If using Docker Desktop, ensure it's started
# Check Docker Desktop icon in system tray (Windows) or menu bar (macOS)
```

**5. Kind cluster not found**

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

# Port forward the tool to localhost:8341
./setup.sh forward

# Port forward tool + monitoring dashboards (Grafana, Prometheus, Jaeger)
./setup.sh forward-all

# Restart the deployment (e.g., to pick up a new ConfigMap from .env)
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
# General web search
curl -X POST http://localhost:8341/api/capabilities/web_search \
  -H "Content-Type: application/json" \
  -d '{"query": "best beach destinations Caribbean"}'

# News search
curl -X POST http://localhost:8341/api/capabilities/web_search \
  -H "Content-Type: application/json" \
  -d '{"query": "latest AI developments", "search_type": "news", "max_results": 5}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export SEARCH_API_KEY="your-tavily-api-key-here"
export SEARCH_PROVIDER="tavily"
export REDIS_URL="redis://localhost:6379"
export PORT=8341

# Run the tool
go run .
```

### Adding New Providers

1. Implement the `providers.SearchProvider` interface in a new file under `providers/`
2. Add the provider to the `createProvider()` switch in `main.go`
3. The provider must implement `Name()` and `Search()` methods

### Adding New Capabilities

1. Add request/response types in `search_handler.go`
2. Register capability in `registerCapabilities()` in `main.go`
3. Implement handler method on `WebSearchTool`

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can use this tool
- [agent-with-orchestration](../agent-with-orchestration/) - Basic orchestration example
- [stock-market-tool](../stock-market-tool/) - Stock market data tool
- [news-tool](../news-tool/) - News aggregation tool
- [weather-tool-v2](../weather-tool-v2/) - Weather data tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
