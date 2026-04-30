# OpenFDA Tool

A TruvaG3 tool that provides FDA drug and device safety data using the free [OpenFDA API](https://open.fda.gov/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides FDA drug and device safety capabilities that agents can discover and use. It requires **no API keys** - the OpenFDA API is free and open (an optional API key increases rate limits). Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

**No API keys required!** The OpenFDA API is free and open. An optional API key increases your daily request limit from 1,000 to 120,000. You can register for a free key at [open.fda.gov/apis/authentication](https://open.fda.gov/apis/authentication/).

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

The fastest way to get the OpenFDA tool running. **No API keys needed!**

```bash
cd examples/openfda-tool

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
| **OpenFDA API** | http://localhost:8365 | FDA drug and device safety API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The OpenFDA tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/openfda-tool

# Build Docker image
docker build -t openfda-tool:latest .

# Load into Kind
kind load docker-image openfda-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=openfda-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/openfda-service 8365:80

# Test adverse event search
curl -X POST http://localhost:8365/api/capabilities/search_adverse_events \
  -H "Content-Type: application/json" \
  -d '{"drug_name": "aspirin", "limit": 3}'
```

---

## Features

- **Drug Adverse Event Search** - Query the FAERS database for drug safety reports
- **Drug Label Lookup** - Search FDA-approved labeling including dosage, warnings, and indications
- **Drug Recall Search** - Find enforcement and recall reports by drug, severity, or status
- **Medical Device Event Search** - Query the MAUDE database for device adverse events
- **No API Keys Required** - Free, open OpenFDA API (optional key for higher rate limits)
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Search Adverse Events (`search_adverse_events`)

**Endpoint:** `/api/capabilities/search_adverse_events`

Searches FDA drug adverse event reports (FAERS database) by drug name.

**Request:**
```json
{
  "drug_name": "aspirin",
  "serious": true,
  "limit": 5
}
```

**Response:**
```json
{
  "drug_name": "aspirin",
  "total": 48532,
  "events": [
    {
      "safety_report_id": "10125634",
      "receive_date": "20240315",
      "serious": "1",
      "reactions": ["Gastrointestinal haemorrhage", "Nausea"],
      "drug_names": ["ASPIRIN", "LISINOPRIL"],
      "patient_sex": "2",
      "patient_onset_age": "65",
      "source": "OpenFDA API"
    }
  ],
  "source": "OpenFDA API"
}
```

**Field Reference:**

| Field | Type | Description |
|-------|------|-------------|
| `drug_name` | string | **Required.** Drug brand name or generic name |
| `serious` | boolean | Optional. Filter to serious events only |
| `limit` | number | Optional. Max results, 1-100 (default: 10) |

### 2. Search Drug Labels (`search_drug_labels`)

**Endpoint:** `/api/capabilities/search_drug_labels`

Searches FDA-approved drug labeling including dosage, warnings, and indications.

**Request:**
```json
{
  "query": "ibuprofen",
  "limit": 3
}
```

**Response:**
```json
{
  "query": "ibuprofen",
  "total": 156,
  "labels": [
    {
      "brand_name": "ADVIL",
      "generic_name": "IBUPROFEN",
      "manufacturer": "Pfizer Consumer Healthcare",
      "purpose": "Pain reliever/fever reducer",
      "warnings": "Allergy alert: Ibuprofen may cause a severe allergic reaction...",
      "indications_and_usage": "For the temporary relief of minor aches and pains...",
      "dosage_and_administration": "Adults and children 12 years and over: take 1 tablet every 4 to 6 hours...",
      "active_ingredient": "Ibuprofen 200 mg",
      "route": ["ORAL"],
      "source": "OpenFDA API"
    }
  ],
  "source": "OpenFDA API"
}
```

**Field Reference:**

| Field | Type | Description |
|-------|------|-------------|
| `query` | string | **Required.** Drug name or active ingredient |
| `limit` | number | Optional. Max results, 1-100 (default: 5) |

### 3. Search Drug Recalls (`search_drug_recalls`)

**Endpoint:** `/api/capabilities/search_drug_recalls`

Searches FDA drug enforcement and recall reports.

**Request:**
```json
{
  "drug_name": "metformin",
  "classification": "Class I",
  "limit": 5
}
```

**Response:**
```json
{
  "drug_name": "metformin",
  "total": 23,
  "recalls": [
    {
      "recall_number": "D-0123-2024",
      "reason_for_recall": "CGMP Deviations: Product failed impurity/degradation specifications",
      "classification": "Class I",
      "status": "Ongoing",
      "product_description": "Metformin HCl Extended-Release Tablets, 500 mg",
      "recalling_firm": "Example Pharma Inc",
      "city": "Philadelphia",
      "state": "PA",
      "report_date": "20240201",
      "source": "OpenFDA API"
    }
  ],
  "source": "OpenFDA API"
}
```

**Field Reference:**

| Field | Type | Description |
|-------|------|-------------|
| `drug_name` | string | Optional. Drug name to filter recalls |
| `classification` | string | Optional. Recall severity: "Class I" (most severe), "Class II", or "Class III" |
| `status` | string | Optional. Recall status: "Ongoing", "Terminated", etc. |
| `limit` | number | Optional. Max results, 1-100 (default: 10) |

**Recall Classifications:**

| Classification | Severity | Meaning |
|----------------|----------|---------|
| Class I | Most severe | Reasonable probability of serious adverse health consequences or death |
| Class II | Moderate | May cause temporary or medically reversible adverse health consequences |
| Class III | Least severe | Not likely to cause adverse health consequences |

### 4. Search Device Events (`search_device_events`)

**Endpoint:** `/api/capabilities/search_device_events`

Searches FDA medical device adverse event reports (MAUDE database).

**Request:**
```json
{
  "device_name": "pacemaker",
  "limit": 5
}
```

**Response:**
```json
{
  "device_name": "pacemaker",
  "total": 89421,
  "events": [
    {
      "report_number": "1234567-2024-00001",
      "date_received": "20240310",
      "event_type": "Malfunction",
      "device_name": "Pacemaker, Dual Chamber",
      "manufacturer": "Example Medical Devices Inc",
      "brand_name": "CardioSync 500",
      "product_code": "DXY",
      "event_description": "The device was reported to have intermittent loss of pacing output...",
      "source": "OpenFDA API"
    }
  ],
  "source": "OpenFDA API"
}
```

**Field Reference:**

| Field | Type | Description |
|-------|------|-------------|
| `device_name` | string | **Required.** Medical device name |
| `limit` | number | Optional. Max results, 1-100 (default: 10) |

---

## Architecture

```
OpenFDA Tool (Passive)
    |
    +-- Registers 4 capabilities in Redis
    +-- Receives requests from agents
    +-- Calls OpenFDA API (api.fda.gov)
    +-- Transforms raw FDA responses to structured format
    +-- Returns standardized responses with ToolResponse wrapper

API Endpoints:
    /drug/event.json        -> search_adverse_events (FAERS database)
    /drug/label.json        -> search_drug_labels
    /drug/enforcement.json  -> search_drug_recalls
    /device/event.json      -> search_device_events (MAUDE database)

Agents (Active)
    |
    +-- Discover OpenFDA tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the OpenFDA tool is automatically discovered by agents via Redis. Agents can then invoke its capabilities through orchestration. You can also query the tool directly:

```bash
# Direct tool access
curl -X POST http://localhost:8365/api/capabilities/search_adverse_events \
  -H "Content-Type: application/json" \
  -d '{
    "drug_name": "aspirin",
    "limit": 5
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `OPENFDA_API_KEY` | OpenFDA API key (increases limit from 1K to 120K/day) | - | No |
| `PORT` | HTTP server port | `8365` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `APP_ENV` | Environment profile (development\|staging\|production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

**No API keys required** - the OpenFDA API is free and open. An optional key from [open.fda.gov/apis/authentication](https://open.fda.gov/apis/authentication/) increases the daily limit from 1,000 to 120,000 requests.

---

## Project Structure

```
openfda-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── openfda_tool.go         # Tool definition, types, capability registration
├── openfda_client.go       # OpenFDA API client with traced HTTP transport
├── handlers.go             # HTTP handlers for each capability (16-step pattern)
├── go.mod                  # Go module definition
├── Dockerfile              # Standalone container image
├── Dockerfile.workspace    # Development build from workspace root
├── k8-deployment.yaml      # Kubernetes manifests (Service + Deployment)
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
# Should show: truvag3:service:openfda-service
```

**2. API errors**

```bash
# Check logs
kubectl logs -n truvag3-examples -l app=openfda-tool | grep -i "api\|error"

# Common issues:
# - Network connectivity: Ensure pod can reach api.fda.gov
# - DNS resolution: Check cluster DNS is working
# - Rate limits: Without API key, limited to 1,000 requests/day (240/min)
# - The OpenFDA API may occasionally be slow; tool has 30s timeout configured
```

**3. Rate limit exceeded (429 errors)**

Without an API key, the OpenFDA API limits requests to 1,000/day and 240/minute. To increase limits:
- Register for a free API key at [open.fda.gov/apis/authentication](https://open.fda.gov/apis/authentication/)
- Set `OPENFDA_API_KEY` in your `.env` file or Kubernetes secret
- With a key: 120,000 requests/day

**4. No results returned**

The tool searches specific FDA fields. Try:
- Brand name: "ADVIL" (not "ibuprofen" for adverse events)
- Generic name: "aspirin" for broader searches
- For device events: use generic device names like "pacemaker", "insulin pump"

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
kubectl logs -n truvag3-examples -l app=openfda-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=openfda-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/openfda-service 8365:80

# Test adverse event search
curl -X POST http://localhost:8365/api/capabilities/search_adverse_events \
  -H "Content-Type: application/json" \
  -d '{"drug_name": "aspirin", "limit": 3}'

# Test drug label search
curl -X POST http://localhost:8365/api/capabilities/search_drug_labels \
  -H "Content-Type: application/json" \
  -d '{"query": "ibuprofen", "limit": 2}'

# Test drug recall search
curl -X POST http://localhost:8365/api/capabilities/search_drug_recalls \
  -H "Content-Type: application/json" \
  -d '{"classification": "Class I", "limit": 3}'

# Test device event search
curl -X POST http://localhost:8365/api/capabilities/search_device_events \
  -H "Content-Type: application/json" \
  -d '{"device_name": "pacemaker", "limit": 3}'
```

---

## Development

### Local Development

```bash
# Set environment variables (no API keys needed!)
export REDIS_URL="redis://localhost:6379"
export PORT=8365

# Optional: set API key for higher rate limits
# export OPENFDA_API_KEY="your-key-here"

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `openfda_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go` (follow the 16-step telemetry pattern)
4. Add API client method in `openfda_client.go`

---

## Related Examples

- [clinical-trials-tool](../clinical-trials-tool/) - Clinical trials search tool
- [pubmed-tool](../pubmed-tool/) - PubMed biomedical literature search
- [world-health-tool](../world-health-tool/) - World health statistics tool
- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can use tools
- [devops-chat-agent](../devops-chat-agent/) - DevOps chat agent with tool orchestration

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
