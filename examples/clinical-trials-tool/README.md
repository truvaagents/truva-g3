# Clinical Trials Tool

A TruvaG3 tool that searches clinical trials from the free [ClinicalTrials.gov](https://clinicaltrials.gov/) v2 API. This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides clinical trial search capabilities that agents can discover and use. It requires **no API keys** - the ClinicalTrials.gov v2 API is completely free and open. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

**No API keys required!** The ClinicalTrials.gov v2 API is completely free, open, and requires no authentication or registration. Rate limit is approximately 50 requests/minute per IP.

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

The fastest way to get the clinical trials tool running. **No API keys needed!**

```bash
cd examples/clinical-trials-tool

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
| **Clinical Trials API** | http://localhost:8367 | Clinical trials search API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The clinical trials tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/clinical-trials-tool

# Build Docker image
docker build -t clinical-trials-tool:latest .

# Load into Kind
kind load docker-image clinical-trials-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=clinical-trials-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/clinical-trials-service 8367:80

# Test clinical trial search
curl -X POST http://localhost:8367/api/capabilities/search_trials \
  -H "Content-Type: application/json" \
  -d '{"condition": "lung cancer", "status": "RECRUITING"}'
```

---

## Features

- **Condition-Based Search** - Search clinical trials by disease, condition, intervention, phase, and status
- **Individual Trial Lookup** - Retrieve detailed information for any trial by NCT identifier
- **Location-Based Search** - Find trials near a geographic location by country and city
- **No API Keys** - Free, open ClinicalTrials.gov v2 API with no authentication required
- **Response Flattening** - Converts deeply nested ClinicalTrials.gov responses into clean, flat JSON
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Search Trials (`search_trials`)

**Endpoint:** `/api/capabilities/search_trials`

Searches clinical trials on ClinicalTrials.gov by condition, intervention, phase, and status.

**Request:**
```json
{
  "condition": "lung cancer",
  "intervention": "pembrolizumab",
  "phase": "PHASE3",
  "status": "RECRUITING",
  "max_results": 5
}
```

**Response:**
```json
{
  "condition": "lung cancer",
  "trials": [
    {
      "nct_id": "NCT04280705",
      "title": "A Study of Pembrolizumab in Participants With Advanced Non-Small Cell Lung Cancer",
      "status": "RECRUITING",
      "phase": "PHASE3",
      "conditions": ["Non-Small Cell Lung Cancer"],
      "interventions": ["Pembrolizumab", "Placebo"],
      "locations": ["MD Anderson Cancer Center, Houston, United States"],
      "start_date": "2020-03",
      "completion_date": "2026-12",
      "enrollment": 750,
      "sponsor": "Merck Sharp & Dohme LLC",
      "source": "ClinicalTrials.gov"
    }
  ],
  "total_count": 42,
  "source": "ClinicalTrials.gov"
}
```

**Phase Values:**

| Phase | Description |
|-------|-------------|
| `EARLY_PHASE1` | Early Phase 1 (formerly Phase 0) |
| `PHASE1` | Phase 1 - Safety and dosage testing |
| `PHASE2` | Phase 2 - Efficacy and side effects |
| `PHASE3` | Phase 3 - Large-scale efficacy confirmation |
| `PHASE4` | Phase 4 - Post-market surveillance |

**Status Values:**

| Status | Description |
|--------|-------------|
| `RECRUITING` | Currently enrolling participants |
| `ACTIVE_NOT_RECRUITING` | Ongoing but not enrolling |
| `COMPLETED` | Study has concluded |
| `NOT_YET_RECRUITING` | Approved but not yet enrolling |
| `ENROLLING_BY_INVITATION` | Enrolling by invitation only |

### 2. Get Trial (`get_trial`)

**Endpoint:** `/api/capabilities/get_trial`

Retrieves detailed information for a specific clinical trial by its NCT identifier.

**Request:**
```json
{
  "nct_id": "NCT04280705"
}
```

**Response:**
```json
{
  "trial": {
    "nct_id": "NCT04280705",
    "title": "A Study of Pembrolizumab in Participants With Advanced Non-Small Cell Lung Cancer",
    "status": "RECRUITING",
    "phase": "PHASE3",
    "conditions": ["Non-Small Cell Lung Cancer"],
    "interventions": ["Pembrolizumab", "Placebo"],
    "locations": ["MD Anderson Cancer Center, Houston, United States"],
    "start_date": "2020-03",
    "completion_date": "2026-12",
    "enrollment": 750,
    "sponsor": "Merck Sharp & Dohme LLC",
    "source": "ClinicalTrials.gov"
  },
  "source": "ClinicalTrials.gov"
}
```

### 3. Search by Location (`search_by_location`)

**Endpoint:** `/api/capabilities/search_by_location`

Finds clinical trials near a geographic location by country and optionally city.

**Request:**
```json
{
  "condition": "diabetes",
  "country": "Japan",
  "city": "Tokyo",
  "status": "RECRUITING",
  "max_results": 10
}
```

**Response:**
```json
{
  "condition": "diabetes",
  "country": "Japan",
  "city": "Tokyo",
  "trials": [
    {
      "nct_id": "NCT05123456",
      "title": "Phase 3 Study of Novel Insulin Therapy in Type 2 Diabetes",
      "status": "RECRUITING",
      "phase": "PHASE3",
      "conditions": ["Type 2 Diabetes Mellitus"],
      "interventions": ["Insulin Icodec"],
      "locations": ["University of Tokyo Hospital, Tokyo, Japan"],
      "start_date": "2024-01",
      "completion_date": "2027-06",
      "enrollment": 500,
      "sponsor": "Novo Nordisk A/S",
      "source": "ClinicalTrials.gov"
    }
  ],
  "source": "ClinicalTrials.gov"
}
```

---

## Architecture

```
Clinical Trials Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Calls ClinicalTrials.gov v2 API
    +-- Flattens nested API responses
    +-- Returns standardized JSON responses

Request Flow:
    Request → Validate input
    ├── Invalid → Return 400 with structured error
    └── Valid   → Call ClinicalTrials.gov v2 API
                  ├── Success → Flatten nested response
                  │             └── Return clean JSON
                  └── Failure → Map error to HTTP status
                                └── Return structured error

Agents (Active)
    |
    +-- Discover clinical trials tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the clinical trials tool is automatically discovered by agents via Redis. Agents can then invoke its capabilities through orchestration. You can also query the tool directly:

```bash
# Direct tool access
curl -X POST http://localhost:8367/api/capabilities/search_by_location \
  -H "Content-Type: application/json" \
  -d '{
    "condition": "lung cancer",
    "country": "Japan",
    "status": "RECRUITING"
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8367` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |
| `TRUVAG3_K8S_SERVICE_NAME` | Kubernetes service name for discovery | `clinical-trials-service` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |

**No API keys required** - the ClinicalTrials.gov v2 API is free and open.

---

## Project Structure

```
clinical-trials-tool/
├── main.go                     # Entry point, framework setup, telemetry init
├── clinical_trials_tool.go     # Tool definition, types, capability registration
├── clinicaltrials_client.go    # ClinicalTrials.gov v2 API client with response flattening
├── handlers.go                 # HTTP handlers for each capability
├── go.mod                      # Go module definition
├── Dockerfile                  # Standalone container image
├── Dockerfile.workspace        # Development build from workspace root
├── k8-deployment.yaml          # Kubernetes manifests
├── setup.sh                    # Full lifecycle management script
├── .env.example                # Environment variable template
└── README.md                   # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"
# Should show: truvag3:service:clinical-trials-service
```

**2. API errors**

```bash
# Check logs
kubectl logs -n truvag3-examples -l app=clinical-trials-tool | grep -i "api\|error"

# Common issues:
# - Network connectivity: Ensure pod can reach clinicaltrials.gov
# - DNS resolution: Check cluster DNS is working
# - Rate limiting: API allows ~50 requests/minute per IP
# - Timeout: Tool uses a 30s timeout for API calls (larger responses)
```

**3. No results returned**

The ClinicalTrials.gov API uses exact matching for enum fields. Ensure:
- `phase` values are uppercase: `PHASE1`, `PHASE2`, `PHASE3`, `PHASE4`, `EARLY_PHASE1`
- `status` values are uppercase: `RECRUITING`, `COMPLETED`, `ACTIVE_NOT_RECRUITING`
- `condition` is a recognizable medical term (e.g., "lung cancer", not "LC")

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
kubectl logs -n truvag3-examples -l app=clinical-trials-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=clinical-trials-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/clinical-trials-service 8367:80

# Search for recruiting lung cancer trials
curl -X POST http://localhost:8367/api/capabilities/search_trials \
  -H "Content-Type: application/json" \
  -d '{"condition": "lung cancer", "status": "RECRUITING"}'

# Get a specific trial by NCT ID
curl -X POST http://localhost:8367/api/capabilities/get_trial \
  -H "Content-Type: application/json" \
  -d '{"nct_id": "NCT04280705"}'

# Search trials by location
curl -X POST http://localhost:8367/api/capabilities/search_by_location \
  -H "Content-Type: application/json" \
  -d '{"condition": "diabetes", "country": "Japan", "city": "Tokyo"}'
```

---

## Development

### Local Development

```bash
# Set environment variables (no API keys needed!)
export REDIS_URL="redis://localhost:6379"
export PORT=8367

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `clinical_trials_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add ClinicalTrials.gov client method in `clinicaltrials_client.go` if needed

---

## Related Examples

- [pubmed-tool](../pubmed-tool/) - PubMed biomedical literature search tool
- [openfda-tool](../openfda-tool/) - FDA drug and adverse event data tool
- [world-health-tool](../world-health-tool/) - WHO global health statistics tool
- [semantic-scholar-tool](../semantic-scholar-tool/) - Academic paper search tool
- [arxiv-tool](../arxiv-tool/) - ArXiv preprint search tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
