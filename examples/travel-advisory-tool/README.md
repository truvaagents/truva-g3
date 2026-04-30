# Travel Advisory Tool

A TruvaG3 tool that provides official US State Department travel safety advisories using the free [Travel Advisories API](https://cadataapi.state.gov/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Registered Capabilities](#registered-capabilities)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

This tool provides travel safety advisory capabilities that agents can discover and use. It requires **no API keys** - the US State Department API is completely free and open. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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
```

</details>

---

#### 3. kubectl (Kubernetes CLI)

| Platform | Recommended Method | Alternative |
|----------|-------------------|-------------|
| **macOS** | `brew install kubectl` | Binary download |
| **Windows** | `choco install kubernetes-cli` | Binary download |
| **Linux** | `apt install kubectl` | Binary download |

<details>
<summary><strong>macOS Installation</strong></summary>

```bash
brew install kubectl
kubectl version --client
```

</details>

<details>
<summary><strong>Windows Installation</strong></summary>

```powershell
choco install kubernetes-cli
kubectl version --client
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

```bash
sudo apt-get update && sudo apt-get install -y kubectl
kubectl version --client
```

</details>

---

#### 4. Go Programming Language

| Platform | Recommended Method | Alternative |
|----------|-------------------|-------------|
| **macOS** | `brew install go` | Download from go.dev |
| **Windows** | Download MSI from go.dev | `choco install golang` |
| **Linux** | Download tarball from go.dev | Package manager |

<details>
<summary><strong>macOS Installation</strong></summary>

```bash
brew install go
go version
# Expected: go version go1.25.x darwin/arm64 (or darwin/amd64)
```

</details>

<details>
<summary><strong>Windows Installation</strong></summary>

```powershell
choco install golang
go version
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

```bash
curl -LO https://go.dev/dl/go1.25.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
go version
```

</details>

---

#### 5. API Keys

**No API keys required!** The US State Department Travel Advisories API is completely free, open, and requires no authentication or registration.

---

### Verify All Prerequisites

```bash
echo "Checking prerequisites..."
echo ""
echo "Docker:"; docker --version || echo "  ERROR: Docker not found"
echo "Kind:"; kind --version || echo "  ERROR: Kind not found"
echo "kubectl:"; kubectl version --client --short 2>/dev/null || kubectl version --client || echo "  ERROR: kubectl not found"
echo "Go:"; go version || echo "  ERROR: Go not found"
echo ""
echo "All checks complete!"
```

---

### Quick Start (Recommended)

The fastest way to get the travel advisory tool running. **No API keys needed!**

```bash
cd examples/travel-advisory-tool

# 1. Deploy to Kubernetes (requires cluster and Redis to be running)
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
| **Advisory API** | http://localhost:8345 | Travel advisory API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The advisory tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/travel-advisory-tool

# Build Docker image
docker build -t travel-advisory-tool:latest .

# Load into Kind
kind load docker-image travel-advisory-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=travel-advisory-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/travel-advisory-service 8345:80

# Test travel advisory
curl -X POST http://localhost:8345/api/capabilities/get_travel_advisory \
  -H "Content-Type: application/json" \
  -d '{"country": "Thailand"}'
```

---

## Features

- **Country Advisory Lookup** - Get official risk level and description for any country
- **Advisory Listing** - Browse all advisories or filter by risk level
- **In-Memory Caching** - 1-hour cache reduces external API calls to ~24/day
- **No API Keys** - Free, open US State Department API
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Get Travel Advisory (`get_travel_advisory`)

**Endpoint:** `/api/capabilities/get_travel_advisory`

Gets the official US State Department travel safety advisory for a specific country.

**Request:**
```json
{
  "country": "Thailand"
}
```

**Response:**
```json
{
  "country": "Thailand",
  "iso_code": "TH",
  "level": 1,
  "level_text": "Exercise Normal Precautions",
  "description": "Exercise normal precautions in Thailand. Read the country information page for additional information on travel to Thailand.",
  "last_updated": "2024-07-15T00:00:00Z",
  "source": "US State Department Travel Advisories"
}
```

**Risk Levels:**

| Level | Text | Meaning |
|-------|------|---------|
| 1 | Exercise Normal Precautions | Standard travel safety applies |
| 2 | Exercise Increased Caution | Be aware of heightened risks |
| 3 | Reconsider Travel | Serious risks, consider alternatives |
| 4 | Do Not Travel | Very high risk, avoid all travel |

### 2. List Advisories (`list_advisories`)

**Endpoint:** `/api/capabilities/list_advisories`

Lists all country travel advisories, optionally filtered by risk level.

**Request (all advisories):**
```json
{}
```

**Request (filter by level):**
```json
{
  "level": 4
}
```

**Response:**
```json
{
  "advisories": [
    {
      "country": "Afghanistan",
      "iso_code": "AF",
      "level": 4,
      "level_text": "Do Not Travel",
      "last_updated": "2024-01-10T00:00:00Z"
    },
    {
      "country": "Iraq",
      "iso_code": "IQ",
      "level": 4,
      "level_text": "Do Not Travel",
      "last_updated": "2024-03-22T00:00:00Z"
    }
  ],
  "count": 2,
  "level": 4,
  "source": "US State Department Travel Advisories"
}
```

---

## Architecture

```
Travel Advisory Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Checks in-memory cache (1-hour TTL)
    +-- Calls State Dept API if cache expired
    +-- Returns standardized responses

Caching Strategy:
    Request → Cache fresh?
    ├── Yes → Return cached data (no API call)
    └── No  → Fetch from State Dept API
              └── Update cache (1-hour TTL)
              └── Return fresh data

Agents (Active)
    |
    +-- Discover advisory tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the advisory tool is automatically discovered by agents via Redis. You can query safety information through natural language:

```bash
# Query through the travel chat agent
curl -X POST http://localhost:8356/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "Is it safe to travel to Thailand right now?",
    "ai_synthesis": true
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8345` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

**No API keys required** - the State Department API is free and open.

---

## Project Structure

```
travel-advisory-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── advisory_tool.go        # Tool definition, capability registration
├── stategov_client.go      # State Dept API client with in-memory cache
├── handlers.go             # HTTP handlers for each capability
├── go.mod                  # Go module definition
├── Dockerfile              # Standalone container image
├── Dockerfile.workspace    # Development build from workspace root
├── k8-deployment.yaml      # Kubernetes manifests
├── setup.sh                # Full lifecycle management script
└── README.md               # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"
# Should show: truvag3:service:travel-advisory-service
```

**2. API errors**

```bash
# Check logs
kubectl logs -n truvag3-examples -l app=travel-advisory-tool | grep -i "api\|error"

# Common issues:
# - Network connectivity: Ensure pod can reach cadataapi.state.gov
# - DNS resolution: Check cluster DNS is working
# - The State Dept API may occasionally be slow; tool has timeouts configured
```

**3. Country not found**

The tool uses case-insensitive matching with substring support. Try:
- Full country name: "Thailand" (not "TH")
- Common name: "United States" (not "USA")
- Partial match: "Thai" will match "Thailand"

**4. Docker build fails**

```bash
docker info
# Ensure Docker is running
```

**5. Kind cluster not found**

```bash
kind get clusters
kind create cluster --name truvag3-demo
```

### Useful Commands

```bash
# View tool logs
kubectl logs -n truvag3-examples -l app=travel-advisory-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=travel-advisory-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/travel-advisory-service 8345:80

# Test travel advisory
curl -X POST http://localhost:8345/api/capabilities/get_travel_advisory \
  -H "Content-Type: application/json" \
  -d '{"country": "Thailand"}'

# List Level 4 (Do Not Travel) advisories
curl -X POST http://localhost:8345/api/capabilities/list_advisories \
  -H "Content-Type: application/json" \
  -d '{"level": 4}'
```

---

## Development

### Local Development

```bash
# Set environment variables (no API keys needed!)
export REDIS_URL="redis://localhost:6379"
export PORT=8345

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `advisory_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add State Dept client method in `stategov_client.go` if needed

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can use this tool
- [flight-tool](../flight-tool/) - Flight search tool
- [hotel-tool](../hotel-tool/) - Hotel search tool
- [places-tool](../places-tool/) - Local places and restaurants search
- [country-info-tool](../country-info-tool/) - Country information tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
