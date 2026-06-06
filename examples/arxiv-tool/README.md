# arXiv Tool

A TruvaG3 tool that provides academic paper search capabilities using the free [arXiv.org API](https://arxiv.org/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides academic paper search capabilities that agents can discover and use. It requires **no API keys** - the arXiv API is completely free and open. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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
# Expected: go version go1.26.x darwin/arm64 (or darwin/amd64)
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
curl -LO https://go.dev/dl/go1.26.4.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.4.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
go version
```

</details>

---

#### 5. API Keys

**No API keys required!** The arXiv API is completely free, open, and requires no authentication or registration.

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

The fastest way to get the arXiv tool running. **No API keys needed!**

```bash
cd examples/arxiv-tool

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
| **arXiv API** | http://localhost:8369 | arXiv paper search API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The arXiv tool requires Redis for service discovery. If you haven't already set up infrastructure, run these from this directory:

```bash
cd examples/arxiv-tool

./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack (Prometheus, Grafana, Jaeger)
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

#### Step 2: Build and Deploy

`setup.sh` handles the Docker build, Kind image load, namespace creation, and manifest apply. Use these subcommands instead of raw `kubectl`:

```bash
cd examples/arxiv-tool

# Build the Docker image only (does not deploy)
./setup.sh docker-build

# Full deploy: build + load into Kind + create namespace + apply manifest
./setup.sh deploy

# Verify deployment
./setup.sh status
```

> **Tip:** If you don't already have a cluster and infrastructure, `./setup.sh full-deploy` does everything from scratch in one shot — cluster, monitoring, tool, and port forwards.

#### Step 3: Test the Tool

```bash
# Port forward the tool service to localhost:8369
./setup.sh forward

# Or run a built-in smoke test against the deployed tool
./setup.sh test
```

In a second terminal (while `./setup.sh forward` is running):

```bash
curl -X POST http://localhost:8369/api/capabilities/search_papers \
  -H "Content-Type: application/json" \
  -d '{"query": "attention is all you need"}'
```

---

## Features

- **Paper Search** - Search arXiv preprints by keyword with optional category filter and sort order
- **Paper Lookup** - Get detailed metadata for a specific paper by arXiv ID
- **Recent Papers** - Browse the most recently submitted papers in any arXiv category
- **XML-to-JSON Conversion** - Transparently converts arXiv Atom 1.0 XML responses to JSON
- **Client-Side Rate Limiting** - Enforces 1 request per 3 seconds to comply with arXiv usage policy
- **No API Keys** - Free, open arXiv API with no authentication required
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Search Papers (`search_papers`)

**Endpoint:** `/api/capabilities/search_papers`

Searches arXiv preprints by query with optional category filter and sorting.

**Request:**
```json
{
  "query": "transformer attention mechanism",
  "category": "cs.AI",
  "max_results": 5,
  "sort_by": "relevance"
}
```

**Response:**
```json
{
  "query": "transformer attention mechanism",
  "total_results": 18302,
  "papers": [
    {
      "arxiv_id": "2301.07041v2",
      "title": "A Survey of Transformer-based Attention Models in Computer Vision",
      "authors": ["Author One", "Author Two"],
      "abstract": "Transformer-based models have achieved remarkable success...",
      "categories": ["cs.AI", "cs.CV"],
      "primary_category": "cs.AI",
      "published_date": "2023-01-17T12:00:00Z",
      "updated_date": "2023-03-05T08:30:00Z",
      "pdf_url": "https://arxiv.org/pdf/2301.07041v2",
      "abs_url": "https://arxiv.org/abs/2301.07041v2",
      "comment": "15 pages, 3 figures",
      "doi": "10.1234/example.2023"
    }
  ],
  "source": "arXiv API"
}
```

**Sort Options:**

| Value | Description |
|-------|-------------|
| `relevance` | Sort by relevance to query (default) |
| `lastUpdatedDate` | Sort by most recently updated |
| `submittedDate` | Sort by most recently submitted |

**Common arXiv Categories:**

| Category | Field |
|----------|-------|
| `cs.AI` | Artificial Intelligence |
| `cs.CL` | Computation and Language (NLP) |
| `cs.CV` | Computer Vision |
| `cs.LG` | Machine Learning |
| `math.CO` | Combinatorics |
| `physics.hep-th` | High Energy Physics - Theory |
| `stat.ML` | Machine Learning (Statistics) |

### 2. Get Paper (`get_paper`)

**Endpoint:** `/api/capabilities/get_paper`

Gets detailed information for a specific arXiv paper by its ID.

**Request:**
```json
{
  "arxiv_id": "2301.07041"
}
```

**Response:**
```json
{
  "paper": {
    "arxiv_id": "2301.07041v2",
    "title": "A Survey of Transformer-based Attention Models in Computer Vision",
    "authors": ["Author One", "Author Two"],
    "abstract": "Transformer-based models have achieved remarkable success...",
    "categories": ["cs.AI", "cs.CV"],
    "primary_category": "cs.AI",
    "published_date": "2023-01-17T12:00:00Z",
    "updated_date": "2023-03-05T08:30:00Z",
    "pdf_url": "https://arxiv.org/pdf/2301.07041v2",
    "abs_url": "https://arxiv.org/abs/2301.07041v2",
    "comment": "15 pages, 3 figures",
    "journal_ref": "IEEE Trans. on Pattern Analysis 2023",
    "doi": "10.1234/example.2023"
  },
  "source": "arXiv API"
}
```

### 3. Recent Papers (`recent_papers`)

**Endpoint:** `/api/capabilities/recent_papers`

Gets the most recently submitted papers in an arXiv category.

**Request:**
```json
{
  "category": "cs.AI",
  "max_results": 5
}
```

**Response:**
```json
{
  "category": "cs.AI",
  "total_results": 54231,
  "papers": [
    {
      "arxiv_id": "2603.01234v1",
      "title": "Recent Advances in Neural Architecture Search",
      "authors": ["Author One", "Author Two", "Author Three"],
      "abstract": "We present a comprehensive overview of recent advances...",
      "categories": ["cs.AI", "cs.LG"],
      "primary_category": "cs.AI",
      "published_date": "2026-03-01T18:00:00Z",
      "updated_date": "2026-03-01T18:00:00Z",
      "pdf_url": "https://arxiv.org/pdf/2603.01234v1",
      "abs_url": "https://arxiv.org/abs/2603.01234v1"
    }
  ],
  "source": "arXiv API"
}
```

---

## Architecture

```
arXiv Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Enforces rate limit (1 req / 3 seconds)
    +-- Calls arXiv API (Atom XML)
    +-- Parses XML, converts to JSON
    +-- Returns standardized ToolResponse

Request Flow:
    Request → Rate limiter
    ├── Blocked → Wait until 3s elapsed
    └── Allowed → Build query URL
                  └── GET export.arxiv.org/api/query
                  └── Parse Atom 1.0 XML
                  └── Convert entries to PaperResult JSON
                  └── Wrap in ToolResponse

Agents (Active)
    |
    +-- Discover arXiv tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the arXiv tool is automatically discovered by agents via Redis. Agents can then invoke its capabilities through orchestration. You can also query the tool directly:

```bash
# Direct tool access
curl -X POST http://localhost:8369/api/capabilities/search_papers \
  -H "Content-Type: application/json" \
  -d '{
    "query": "large language model alignment",
    "max_results": 5
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8369` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

**No API keys required** - the arXiv API is free and open.

---

## Project Structure

```
arxiv-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── arxiv_tool.go           # Tool definition, types, capability registration
├── arxiv_client.go         # arXiv API client with rate limiting and XML parsing
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

```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"
# Should show: truvag3:service:arxiv-service
```

**2. API errors**

```bash
# Stream logs and grep for upstream errors
./setup.sh logs | grep -i "api\|error"

# Common issues:
# - 403 Forbidden: Rate limit exceeded (tool enforces 3s between requests,
#   but concurrent pods may trigger arXiv's server-side limit)
# - Network connectivity: Ensure pod can reach export.arxiv.org
# - DNS resolution: Check cluster DNS is working
# - arXiv API can be slow for large queries; tool has 30s timeout configured
```

**3. Paper not found**

The `get_paper` capability requires a valid arXiv ID. Try:
- Numeric format: `"2301.07041"` (not a URL)
- With version: `"2301.07041v2"`
- Old format: `"hep-th/9901001"`

**4. Docker build fails**

```bash
docker info
# Ensure Docker is running
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

# Port forward the tool to localhost:8369
./setup.sh forward

# Port forward tool + monitoring dashboards (Grafana, Prometheus, Jaeger)
./setup.sh forward-all

# Restart the deployment
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
# Search for papers
curl -X POST http://localhost:8369/api/capabilities/search_papers \
  -H "Content-Type: application/json" \
  -d '{"query": "attention is all you need"}'

# Get a specific paper
curl -X POST http://localhost:8369/api/capabilities/get_paper \
  -H "Content-Type: application/json" \
  -d '{"arxiv_id": "1706.03762"}'

# Get recent AI papers
curl -X POST http://localhost:8369/api/capabilities/recent_papers \
  -H "Content-Type: application/json" \
  -d '{"category": "cs.AI", "max_results": 5}'
```

---

## Development

### Local Development

```bash
# Set environment variables (no API keys needed!)
export REDIS_URL="redis://localhost:6379"
export PORT=8369

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `arxiv_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add arXiv client method in `arxiv_client.go` if needed

---

## Related Examples

- [semantic-scholar-tool](../semantic-scholar-tool/) - Semantic Scholar academic paper search
- [pubmed-tool](../pubmed-tool/) - PubMed biomedical literature search
- [web-search-tool](../web-search-tool/) - General web search tool
- [news-tool](../news-tool/) - News search tool
- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent example

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
