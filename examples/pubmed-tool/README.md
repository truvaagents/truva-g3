# PubMed Tool

A Truva-G3 tool that searches biomedical literature and retrieves article metadata using the free [NCBI PubMed E-utilities API](https://pubmed.ncbi.nlm.nih.gov/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides biomedical literature search capabilities that agents can discover and use. It requires **no API keys** to function (though an optional NCBI API key increases rate limits from 3 to 10 requests/second). Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

**No API keys required to run!** The NCBI PubMed E-utilities API is free and open. However, NCBI policy requires you to provide an email address and tool name (pre-configured in the deployment manifests).

**Optional:** An NCBI API key increases rate limits from 3 to 10 requests/second. Get one free at [ncbi.nlm.nih.gov/account](https://www.ncbi.nlm.nih.gov/account/) under Settings > API Key Management.

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

The fastest way to get the PubMed tool running. **No API keys needed!**

```bash
cd examples/pubmed-tool

# 1. Deploy to Kubernetes (requires cluster and Redis to be running)
./setup.sh deploy
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image
2. Loads it into the Kind cluster
3. Creates NCBI API key secret (if set in `.env`)
4. Deploys the tool to Kubernetes
5. Registers capabilities with Redis for agent discovery

Once complete, the tool is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **PubMed API** | http://localhost:8366 | PubMed literature search API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The PubMed tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/pubmed-tool

# Build Docker image
docker build -t pubmed-tool:latest .

# Load into Kind
kind load docker-image pubmed-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=pubmed-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/pubmed-service 8366:80

# Test article search
curl -X POST http://localhost:8366/api/capabilities/search_articles \
  -H "Content-Type: application/json" \
  -d '{"query": "cancer immunotherapy"}'
```

---

## Features

- **Article Search** - Search 36M+ biomedical articles by keyword, MeSH term, or advanced query syntax
- **Article Details** - Retrieve full metadata (authors, journal, DOI, volume, issue, pages) for specific PMIDs
- **Citation Lookup** - Find articles that cite a given paper via PubMed Central cross-references
- **Rate Limiting** - Built-in client-side rate limiter (3 req/sec without key, 10 with key)
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Search Articles (`search_articles`)

**Endpoint:** `/api/capabilities/search_articles`

Searches PubMed for biomedical articles by keyword or MeSH term. Uses a two-step process: `esearch` to find matching PMIDs, then `esummary` to retrieve metadata.

**Request:**
```json
{
  "query": "diabetes type 2 treatment",
  "max_results": 10,
  "sort": "relevance"
}
```

**Response:**
```json
{
  "query": "diabetes type 2 treatment",
  "total_count": 142857,
  "articles": [
    {
      "pmid": "38000000",
      "title": "Novel GLP-1 receptor agonists in type 2 diabetes management",
      "authors": ["Smith J", "Johnson A", "Williams B"],
      "journal": "The Lancet Diabetes & Endocrinology",
      "pub_date": "2024 Jan 15",
      "doi": "10.1016/S2213-8587(24)00001-1",
      "pmc_ref_count": 42,
      "has_abstract": true
    }
  ],
  "source": "NCBI PubMed E-utilities"
}
```

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | Yes | - | PubMed search query (supports MeSH terms) |
| `max_results` | number | No | 10 | Max results to return (1-100) |
| `sort` | string | No | `"relevance"` | Sort order: `"relevance"` or `"date"` |

### 2. Get Article Details (`get_article_details`)

**Endpoint:** `/api/capabilities/get_article_details`

Retrieves detailed metadata for specific PubMed articles by their PMIDs. Returns full author information, journal details, and all article identifiers.

**Request:**
```json
{
  "pmids": "38000000,37999999"
}
```

**Response:**
```json
{
  "articles": [
    {
      "pmid": "38000000",
      "title": "Novel GLP-1 receptor agonists in type 2 diabetes management",
      "authors": [
        {"name": "Smith J", "auth_type": "Author"},
        {"name": "Johnson A", "auth_type": "Author"}
      ],
      "journal": "The Lancet Diabetes & Endocrinology",
      "pub_date": "2024 Jan 15",
      "doi": "10.1016/S2213-8587(24)00001-1",
      "volume": "12",
      "issue": "1",
      "pages": "45-58",
      "pmc_ref_count": 42,
      "has_abstract": true,
      "article_ids": [
        {"id_type": "pubmed", "value": "38000000"},
        {"id_type": "doi", "value": "10.1016/S2213-8587(24)00001-1"},
        {"id_type": "pmc", "value": "PMC10800001"}
      ]
    }
  ],
  "source": "NCBI PubMed E-utilities"
}
```

### 3. Get Citations (`get_citations`)

**Endpoint:** `/api/capabilities/get_citations`

Finds articles that cite a given PubMed article. Uses `elink` to find citing PMIDs via the `pubmed_pubmed_citedin` link, then `esummary` to retrieve their metadata.

**Request:**
```json
{
  "pmid": "38000000"
}
```

**Response:**
```json
{
  "pmid": "38000000",
  "citation_count": 3,
  "citations": [
    {
      "pmid": "38500001",
      "title": "Comparative effectiveness of GLP-1 agonists: a systematic review",
      "authors": ["Lee C", "Park D"],
      "journal": "Diabetes Care",
      "pub_date": "2024 Mar 1",
      "doi": "10.2337/dc24-0001",
      "pmc_ref_count": 8,
      "has_abstract": true
    }
  ],
  "source": "NCBI PubMed E-utilities"
}
```

---

## Architecture

```
PubMed Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Enforces client-side rate limiting (3 or 10 req/sec)
    +-- Calls NCBI E-utilities API (esearch, esummary, elink)
    +-- Returns standardized responses

API Call Patterns:
    search_articles  -> esearch.fcgi (get PMIDs)
                     -> esummary.fcgi (get metadata)

    get_article_details -> esummary.fcgi (get metadata)

    get_citations    -> elink.fcgi (get citing PMIDs)
                     -> esummary.fcgi (get metadata)

Agents (Active)
    |
    +-- Discover PubMed tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the PubMed tool is automatically discovered by agents via Redis. Agents can then invoke its capabilities through orchestration. You can also query the tool directly:

```bash
# Direct tool access
curl -X POST http://localhost:8366/api/capabilities/search_articles \
  -H "Content-Type: application/json" \
  -d '{
    "query": "type 2 diabetes latest treatments",
    "max_results": 5
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `NCBI_EMAIL` | Contact email (NCBI policy requirement) | - | Yes |
| `NCBI_TOOL_NAME` | Tool identifier (NCBI policy requirement) | - | Yes |
| `NCBI_API_KEY` | NCBI API key (increases rate from 3 to 10 req/sec) | - | No |
| `PORT` | HTTP server port | `8366` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |
| `APP_ENV` | Environment profile (development\|staging\|production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |

**NCBI API key is optional** - the tool works without one at a lower rate limit. Get a free key at [ncbi.nlm.nih.gov/account](https://www.ncbi.nlm.nih.gov/account/) under Settings > API Key Management.

---

## Project Structure

```
pubmed-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── pubmed_tool.go          # Tool definition, types, capability registration
├── pubmed_client.go        # NCBI E-utilities API client with rate limiting
├── handlers.go             # HTTP handlers for each capability
├── go.mod                  # Go module definition
├── Dockerfile              # Standalone container image
├── Dockerfile.workspace    # Development build from workspace root
├── k8-deployment.yaml      # Kubernetes manifests
├── setup.sh                # Full lifecycle management script
├── .env.example            # Example environment configuration
└── README.md               # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"
# Should show: truvag3:service:pubmed-service
```

**2. API errors**

```bash
# Check logs
kubectl logs -n truvag3-examples -l app=pubmed-tool | grep -i "api\|error"

# Common issues:
# - Network connectivity: Ensure pod can reach eutils.ncbi.nlm.nih.gov
# - DNS resolution: Check cluster DNS is working
# - Rate limiting: Without API key, limited to 3 req/sec (429 errors)
# - NCBI maintenance: E-utilities may have scheduled downtime
```

**3. Rate limit errors (HTTP 429)**

The tool enforces client-side rate limiting, but NCBI may still reject requests during high-traffic periods. Solutions:
- Set `NCBI_API_KEY` to increase limit from 3 to 10 req/sec
- Get a free API key at [ncbi.nlm.nih.gov/account](https://www.ncbi.nlm.nih.gov/account/)

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
kubectl logs -n truvag3-examples -l app=pubmed-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=pubmed-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/pubmed-service 8366:80

# Search for articles
curl -X POST http://localhost:8366/api/capabilities/search_articles \
  -H "Content-Type: application/json" \
  -d '{"query": "cancer immunotherapy", "max_results": 5}'

# Get article details
curl -X POST http://localhost:8366/api/capabilities/get_article_details \
  -H "Content-Type: application/json" \
  -d '{"pmids": "38000000,37999999"}'

# Find citing articles
curl -X POST http://localhost:8366/api/capabilities/get_citations \
  -H "Content-Type: application/json" \
  -d '{"pmid": "38000000"}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export REDIS_URL="redis://localhost:6379"
export NCBI_EMAIL="your-email@example.com"
export NCBI_TOOL_NAME="truvag3-pubmed-tool"
export PORT=8366

# Optional: Set NCBI API key for higher rate limits
export NCBI_API_KEY="your-api-key"

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `pubmed_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add NCBI client method in `pubmed_client.go` if needed

---

## Related Examples

- [semantic-scholar-tool](../semantic-scholar-tool/) - Academic paper search tool (Semantic Scholar API)
- [arxiv-tool](../arxiv-tool/) - Preprint search tool (arXiv API)
- [clinical-trials-tool](../clinical-trials-tool/) - Clinical trials search tool
- [world-health-tool](../world-health-tool/) - World health data tool
- [web-search-tool](../web-search-tool/) - General web search tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
