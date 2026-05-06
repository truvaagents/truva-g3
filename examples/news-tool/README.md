# News Tool

A TruvaG3 tool that provides news search capabilities using the [GNews.io](https://gnews.io/) API. This tool integrates with the TruvaG3 framework for service discovery and distributed tracing.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Capabilities](#capabilities)
- [Configuration](#configuration)
- [Rate Limiting](#rate-limiting)
- [Telemetry](#telemetry)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

Running this example locally demonstrates how TruvaG3 tools expose capabilities that can be discovered and orchestrated by agents. Follow the steps below to get this tool running.

### Prerequisites

Before running this example, you need to install the following tools. Choose the instructions for your operating system.

> **Important:** This tool requires a [GNews.io](https://gnews.io/) API key to function. The free tier provides 100 requests per day, which is sufficient for development and testing.

> **Note:** This tool is independent and only requires Redis for service discovery. It does not depend on any other tools or agents.

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

#### 5. GNews.io API Key (Required)

This tool requires a GNews.io API key for news data.

**To get your API key:**

1. Visit [gnews.io](https://gnews.io/)
2. Sign up for a free account
3. Navigate to your dashboard
4. Copy your API key

**Free tier includes:**
- 100 API calls per day
- Access to news from multiple sources
- Search by keyword, topic, or language

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

The fastest way to get the news tool running:

```bash
cd examples/news-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**⚠️ STOP HERE** - Open `.env` in your editor and configure your API key:

```bash
nano .env    # or: code .env / vim .env
```

**Required:** Set your GNews.io API key in `.env`:
- `GNEWS_API_KEY=your-api-key-here` (Get free key at [gnews.io](https://gnews.io/))

After configuring your API key, continue with deployment:

```bash
# 2. Deploy the tool (includes building the Docker image)
./setup.sh deploy
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image with Go modules
2. Loads the image into the Kind cluster
3. Deploys the tool to Kubernetes with your API key
4. Sets up port forwarding automatically

Once complete, access the tool at:

| Service | URL | Description |
|---------|-----|-------------|
| **News Tool API** | http://localhost:8099 | REST API for news search |
| **Health Check** | http://localhost:8099/health | Health status endpoint |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The news tool requires Redis for service discovery. If you have not already deployed infrastructure:

```bash
cd examples/k8-deployment
./setup.sh cluster    # Create Kind cluster (if not exists)
./setup.sh infra      # Deploy Redis and observability stack
```

#### Step 2: Configure Your API Key

```bash
cd examples/news-tool
cp .env.example .env
# Edit .env and add your GNEWS_API_KEY
```

To get your API key:
1. Sign up at https://gnews.io/
2. Get your API key from the dashboard
3. Add it to your `.env` file

#### Step 3: Build and Deploy

```bash
# Build the Docker image
./setup.sh docker

# Deploy to Kubernetes
./setup.sh deploy
```

#### Step 4: Set Up Port Forwarding

```bash
./setup.sh forward
```

#### Step 5: Verify Deployment

```bash
# Check the health endpoint
curl http://localhost:8099/health

# Test the search capability
curl -X POST http://localhost:8099/api/capabilities/search_news \
  -H "Content-Type: application/json" \
  -d '{"query": "technology", "max_results": 3}'
```

---

## Features

- **News Search**: Search for articles by topic or keyword
- **Multi-Language Support**: Support for multiple languages in search results
- **Configurable Results**: Control the number of results returned
- **Distributed Tracing**: Built-in trace context propagation for observability

---

## Capabilities

### search_news

Searches for news articles related to a query.

**Endpoint:** `POST /api/capabilities/search_news`

**Request:**
```json
{
  "query": "Tokyo travel",
  "max_results": 5
}
```

**Example:**
```bash
curl -X POST http://localhost:8099/api/capabilities/search_news \
  -H "Content-Type: application/json" \
  -d '{"query": "Tokyo travel", "max_results": 5}'
```

**Response:**
```json
{
  "success": true,
  "data": {
    "total_articles": 100,
    "articles": [
      {
        "title": "Best Time to Visit Tokyo",
        "description": "A guide to Tokyo's seasons...",
        "url": "https://...",
        "source": "Travel Weekly",
        "published_at": "2024-12-01T10:00:00Z"
      }
    ]
  }
}
```

---

## Configuration

### Environment Variables

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `GNEWS_API_KEY` | - | Yes | GNews.io API key for news data |
| `REDIS_URL` | - | Yes | Redis connection URL for service discovery |
| `PORT` | `8099` | No | HTTP server port |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | - | No | OTLP endpoint for telemetry |

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

At minimum, you must set the `GNEWS_API_KEY` variable.

---

## Rate Limiting

The GNews.io free tier allows 100 requests per day. When the limit is exceeded, the tool returns a clear error response:

```json
{
  "success": false,
  "error": "API rate limit exceeded. Please try again tomorrow or upgrade your plan."
}
```

For higher request volumes, consider upgrading to a paid GNews.io plan.

---

## Telemetry

The tool includes comprehensive observability:

### Tracing

- All requests traced with span events
- Trace context propagation from calling agents
- Integration with Jaeger for visualization

### Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `news_tool.requests` | Counter | Total requests received |
| `news_tool.request.duration_ms` | Histogram | Request duration |
| `news_tool.api.calls` | Counter | GNews API calls made |

### Health Check

```bash
curl http://localhost:8099/health
```

---

## Project Structure

```
news-tool/
├── main.go              # Entry point and initialization
├── go.mod               # Go module definition
├── Dockerfile           # Production container image
├── Dockerfile.workspace # Development container with local modules
├── k8-deployment.yaml   # Kubernetes deployment manifest
├── setup.sh             # Build and deployment script
├── .env.example         # Environment variable template
└── README.md            # This file
```

---

## Troubleshooting

### Common Issues

**1. "GNEWS_API_KEY is required" error**

Ensure your API key is set in the `.env` file:
```bash
cat .env | grep GNEWS_API_KEY
```

**2. "REDIS_URL is required" error**

Ensure Redis is running and accessible:
```bash
# Check if Redis is running
kubectl get pods -n truvag3-examples -l app=redis
```

**3. Rate limit exceeded**

The free tier allows 100 requests per day. Either:
- Wait until the next day for the limit to reset
- Upgrade to a paid GNews.io plan

**4. Tool not discovered by agents**

Verify the tool is registered with Redis:
```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 0 KEYS 'truvag3:services:*'
```

### Useful Commands

```bash
# View tool logs
./setup.sh logs

# Check pod status
kubectl get pods -n truvag3-examples -l app=news-tool

# Restart the tool
./setup.sh restart

# Run locally (without Kubernetes)
./setup.sh run

# Full cleanup
./setup.sh cleanup
```

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Chat agent that orchestrates this tool
- [weather-tool-v2](../weather-tool-v2/) - Weather data tool
- [geocoding-tool](../geocoding-tool/) - Location geocoding tool
- [currency-tool](../currency-tool/) - Currency exchange tool
- [country-info-tool](../country-info-tool/) - Country information tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
