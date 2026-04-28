# Semantic Scholar Tool

A Truva-G3 tool that provides academic paper search, citation analysis, and author profile capabilities using the free [Semantic Scholar Academic Graph API](https://www.semanticscholar.org/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides academic paper search and citation analysis capabilities that agents can discover and use. An API key is **optional** but strongly recommended - without one, requests share a global rate limit pool and will frequently hit 429 errors. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

**No API key is required**, but one is **strongly recommended**. Without a key, requests share a global rate limit pool and will frequently receive 429 (Too Many Requests) errors.

To get a free API key, visit the [Semantic Scholar API key request form](https://www.semanticscholar.org/product/api#api-key-form).

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

The fastest way to get the semantic scholar tool running.

```bash
cd examples/semantic-scholar-tool

# 1. (Optional) Set your API key for higher rate limits
export S2_API_KEY="your-api-key-here"

# 2. Deploy to Kubernetes (requires cluster and Redis to be running)
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
| **Semantic Scholar API** | http://localhost:8370 | Academic paper search and citation API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The semantic scholar tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/semantic-scholar-tool

# Build Docker image
docker build -t semantic-scholar-tool:latest .

# Load into Kind
kind load docker-image semantic-scholar-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=semantic-scholar-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/semantic-scholar-service 8370:80

# Test paper search
curl -X POST http://localhost:8370/api/capabilities/search_papers \
  -H "Content-Type: application/json" \
  -d '{"query": "graph neural networks", "max_results": 5}'
```

---

## Features

- **Paper Search** - Search 200M+ academic papers by keyword with year and field-of-study filters
- **Paper Details** - Get full metadata including abstract, TLDR, open access PDF, references, and citations
- **Author Profiles** - Look up researcher profiles with h-index, affiliations, and recent papers
- **Citation Analysis** - Discover which papers cite a given work
- **Client-Side Rate Limiting** - Built-in 1 req/sec rate limiter prevents upstream 429 errors
- **Optional API Key** - Works without authentication; API key unlocks higher rate limits
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Search Papers (`search_papers`)

**Endpoint:** `/api/capabilities/search_papers`

Searches academic papers on Semantic Scholar by keyword query.

**Request:**
```json
{
  "query": "graph neural networks",
  "max_results": 5,
  "year": "2023-2026",
  "fields_of_study": "Computer Science"
}
```

**Response:**
```json
{
  "query": "graph neural networks",
  "total": 523847,
  "papers": [
    {
      "paper_id": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
      "title": "Graph Neural Networks: A Review of Methods and Applications",
      "authors": [
        {"author_id": "1234567", "name": "J. Zhou"},
        {"author_id": "2345678", "name": "G. Cui"}
      ],
      "year": 2024,
      "citation_count": 1250,
      "abstract": "Graph neural networks (GNNs) have been widely used...",
      "url": "https://www.semanticscholar.org/paper/a1b2c3d4e5f6...",
      "publication_date": "2024-03-15"
    }
  ],
  "source": "Semantic Scholar API"
}
```

**Optional Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `max_results` | number | Number of results, 1-100 (default: 10) |
| `year` | string | Year range filter, e.g. `"2023-2026"` |
| `fields_of_study` | string | Field filter, e.g. `"Computer Science"` |

### 2. Get Paper Details (`get_paper_details`)

**Endpoint:** `/api/capabilities/get_paper_details`

Gets detailed information about a specific academic paper including abstract, citations, references, TLDR, and open access PDF link.

**Request:**
```json
{
  "paper_id": "ARXIV:2301.07041"
}
```

**Response:**
```json
{
  "paper_id": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
  "title": "Attention Is All You Need",
  "authors": [
    {"author_id": "1234567", "name": "A. Vaswani"},
    {"author_id": "2345678", "name": "N. Shazeer"}
  ],
  "year": 2017,
  "abstract": "The dominant sequence transduction models are based on...",
  "url": "https://www.semanticscholar.org/paper/a1b2c3d4e5f6...",
  "citation_count": 95000,
  "reference_count": 40,
  "influential_citation_count": 8500,
  "tldr": "A new network architecture based solely on attention mechanisms...",
  "open_access_pdf": "https://arxiv.org/pdf/1706.03762.pdf",
  "publication_date": "2017-06-12",
  "references": [],
  "citations": [],
  "source": "Semantic Scholar API"
}
```

**Supported Paper ID Formats:**

| Format | Example |
|--------|---------|
| Semantic Scholar ID | `a1b2c3d4e5f6...` (40-char hex) |
| DOI | `DOI:10.1234/example` |
| ArXiv | `ARXIV:2301.07041` |
| PubMed | `PMID:12345678` |
| Corpus ID | `CorpusId:12345678` |

### 3. Get Author (`get_author`)

**Endpoint:** `/api/capabilities/get_author`

Gets an author's profile including affiliations, h-index, citation count, and up to 20 recent papers.

**Request:**
```json
{
  "author_id": "1741101"
}
```

**Response:**
```json
{
  "author_id": "1741101",
  "name": "Yoshua Bengio",
  "affiliations": ["Mila - Quebec AI Institute", "University of Montreal"],
  "paper_count": 1150,
  "citation_count": 650000,
  "h_index": 182,
  "papers": [
    {
      "paper_id": "a1b2c3d4e5f6...",
      "title": "Deep Learning",
      "authors": [{"author_id": "1741101", "name": "Yoshua Bengio"}],
      "year": 2016,
      "citation_count": 45000,
      "url": "https://www.semanticscholar.org/paper/a1b2c3d4e5f6..."
    }
  ],
  "url": "https://www.semanticscholar.org/author/1741101",
  "source": "Semantic Scholar API"
}
```

### 4. Get Citations (`get_citations`)

**Endpoint:** `/api/capabilities/get_citations`

Gets papers that cite a given paper.

**Request:**
```json
{
  "paper_id": "ARXIV:2301.07041",
  "max_results": 10
}
```

**Response:**
```json
{
  "paper_id": "ARXIV:2301.07041",
  "total": 10,
  "citations": [
    {
      "paper_id": "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3",
      "title": "A Survey of Graph Neural Networks for Recommender Systems",
      "authors": [
        {"author_id": "3456789", "name": "S. Wu"}
      ],
      "year": 2024,
      "citation_count": 85,
      "abstract": "Graph neural networks have shown great promise...",
      "url": "https://www.semanticscholar.org/paper/b2c3d4e5f6...",
      "publication_date": "2024-01-20"
    }
  ],
  "source": "Semantic Scholar API"
}
```

**Optional Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `max_results` | number | Max citing papers, 1-100 (default: 20) |

---

## Architecture

```
Semantic Scholar Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Enforces client-side rate limiting (1 req/sec)
    +-- Calls Semantic Scholar Academic Graph API
    +-- Returns standardized responses

Rate Limiting Strategy:
    Request → Client-side rate limiter (1 req/sec)
              └── Send to S2 API
                  ├── 200 → Parse and return data
                  ├── 429 → Return retryable error
                  └── 4xx → Return structured error

Agents (Active)
    |
    +-- Discover semantic scholar tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the semantic scholar tool is automatically discovered by agents via Redis. Agents can then invoke its capabilities through orchestration. You can also query the tool directly:

```bash
# Direct tool access
curl -X POST http://localhost:8370/api/capabilities/search_papers \
  -H "Content-Type: application/json" \
  -d '{
    "query": "transformer architectures",
    "max_results": 5
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `S2_API_KEY` | Semantic Scholar API key for higher rate limits | - | No (strongly recommended) |
| `PORT` | HTTP server port | `8370` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |

**API key is optional** - the Semantic Scholar API works without authentication but with significantly lower rate limits. Request a free key at [semanticscholar.org/product/api](https://www.semanticscholar.org/product/api#api-key-form).

---

## Project Structure

```
semantic-scholar-tool/
├── main.go                    # Entry point, framework setup, telemetry init
├── semantic_scholar_tool.go   # Tool definition, types, capability registration
├── s2_client.go               # Semantic Scholar API client with rate limiting
├── handlers.go                # HTTP handlers for each capability
├── go.mod                     # Go module definition
├── Dockerfile                 # Standalone container image
├── Dockerfile.workspace       # Development build from workspace root
├── k8-deployment.yaml         # Kubernetes manifests
├── setup.sh                   # Full lifecycle management script
├── .env.example               # Environment variable template
└── README.md                  # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"
# Should show: truvag3:service:semantic-scholar-service
```

**2. API errors (429 Too Many Requests)**

```bash
# Check logs for rate limiting
kubectl logs -n truvag3-examples -l app=semantic-scholar-tool | grep -i "429\|rate"

# Solution: Set an API key for higher rate limits
# Without a key, requests share a global rate limit pool
# Request a key at: https://www.semanticscholar.org/product/api#api-key-form
```

**3. API errors (other)**

```bash
# Check logs
kubectl logs -n truvag3-examples -l app=semantic-scholar-tool | grep -i "api\|error"

# Common issues:
# - Network connectivity: Ensure pod can reach api.semanticscholar.org
# - DNS resolution: Check cluster DNS is working
# - Invalid paper/author IDs: Verify the ID format is supported
```

**4. Paper or author not found**

The tool supports multiple paper ID formats:
- Semantic Scholar 40-char hex ID: `a1b2c3d4e5f6...`
- DOI: `DOI:10.1234/example`
- ArXiv: `ARXIV:2301.07041`
- PubMed: `PMID:12345678`
- Corpus ID: `CorpusId:12345678`

For authors, use the numeric Semantic Scholar author ID (found in author profile URLs).

**5. Docker build fails**

```bash
docker info
# Ensure Docker is running
```

**6. Kind cluster not found**

```bash
kind get clusters
kind create cluster --name truvag3-demo
```

### Useful Commands

```bash
# View tool logs
kubectl logs -n truvag3-examples -l app=semantic-scholar-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=semantic-scholar-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/semantic-scholar-service 8370:80

# Test paper search
curl -X POST http://localhost:8370/api/capabilities/search_papers \
  -H "Content-Type: application/json" \
  -d '{"query": "large language models", "max_results": 5}'

# Test paper details
curl -X POST http://localhost:8370/api/capabilities/get_paper_details \
  -H "Content-Type: application/json" \
  -d '{"paper_id": "ARXIV:2301.07041"}'

# Test author profile
curl -X POST http://localhost:8370/api/capabilities/get_author \
  -H "Content-Type: application/json" \
  -d '{"author_id": "1741101"}'

# Test citations
curl -X POST http://localhost:8370/api/capabilities/get_citations \
  -H "Content-Type: application/json" \
  -d '{"paper_id": "ARXIV:2301.07041", "max_results": 10}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export REDIS_URL="redis://localhost:6379"
export PORT=8370
export S2_API_KEY="your-api-key-here"  # Optional but recommended

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `semantic_scholar_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add S2 client method in `s2_client.go` if needed

---

## Related Examples

- [arxiv-tool](../arxiv-tool/) - ArXiv preprint search tool
- [pubmed-tool](../pubmed-tool/) - PubMed biomedical literature search
- [web-search-tool](../web-search-tool/) - General web search tool
- [news-tool](../news-tool/) - News article search tool
- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent example

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
