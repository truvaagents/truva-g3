# Prometheus Query Tool

A TruvaG3 tool that provides PromQL query, alerting, and scrape target capabilities using the [Prometheus HTTP API](https://prometheus.io/docs/prometheus/latest/querying/api/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides Prometheus query capabilities that agents can discover and use. It requires **no API keys** - it connects to a self-hosted Prometheus instance running in the same Kubernetes cluster. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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
curl -LO https://go.dev/dl/go1.26.2.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.2.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
go version
```

</details>

---

#### 5. API Keys

**No API keys required!** This tool connects to a self-hosted Prometheus instance running inside the Kubernetes cluster. Prometheus does not require authentication.

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

The fastest way to get the Prometheus query tool running. **No API keys needed!**

```bash
cd examples/prometheus-query-tool

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
| **Prometheus Query API** | http://localhost:8371 | Prometheus query tool API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The Prometheus query tool requires Redis for service discovery and a Prometheus server for querying metrics. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., devops-chat-agent)
cd examples/devops-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis, Prometheus, and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/prometheus-query-tool

# Build Docker image
docker build -t prometheus-query-tool:latest .

# Load into Kind
kind load docker-image prometheus-query-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=prometheus-query-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/prometheus-query-tool-service 8371:80

# Test instant PromQL query
curl -X POST http://localhost:8371/api/capabilities/query_metrics \
  -H "Content-Type: application/json" \
  -d '{"query": "up"}'
```

---

## Features

- **Instant PromQL Queries** - Execute point-in-time PromQL expressions and get current metric values
- **Range PromQL Queries** - Query time series data over a time window with configurable step resolution
- **Alert Listing** - List all currently firing Prometheus alerts grouped by rule
- **Scrape Target Health** - View active and dropped scrape targets with health status
- **No API Keys** - Connects to in-cluster Prometheus with no authentication required
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext and traced HTTP client

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Query Metrics (`query_metrics`)

**Endpoint:** `/api/capabilities/query_metrics`

Executes an instant PromQL query against Prometheus and returns current values of matching time series.

**Request:**
```json
{
  "query": "up{job=\"kubernetes-pods\"}",
  "time": "2026-03-03T12:00:00Z"
}
```

**Response:**
```json
{
  "query": "up{job=\"kubernetes-pods\"}",
  "result_type": "vector",
  "samples": [
    {
      "labels": {
        "__name__": "up",
        "instance": "10.244.0.5:8349",
        "job": "kubernetes-pods"
      },
      "timestamp": 1709467200,
      "value": 1
    }
  ],
  "source": "Prometheus API"
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `query` | string | Yes | PromQL expression for instant query |
| `time` | string | No | RFC3339 timestamp or unix epoch; defaults to current server time |

### 2. Query Range (`query_range`)

**Endpoint:** `/api/capabilities/query_range`

Executes a range PromQL query returning time series data over a specified time window.

**Request:**
```json
{
  "query": "rate(http_requests_total[5m])",
  "start": "2026-03-03T00:00:00Z",
  "end": "2026-03-03T12:00:00Z",
  "step": "15s"
}
```

**Response:**
```json
{
  "query": "rate(http_requests_total[5m])",
  "result_type": "matrix",
  "series": [
    {
      "labels": {
        "__name__": "http_requests_total",
        "method": "GET",
        "status": "200"
      },
      "values": [
        {"timestamp": 1709424000, "value": 12.5},
        {"timestamp": 1709424015, "value": 13.1},
        {"timestamp": 1709424030, "value": 11.8}
      ]
    }
  ],
  "start": "2026-03-03T00:00:00Z",
  "end": "2026-03-03T12:00:00Z",
  "step": "15s",
  "source": "Prometheus API"
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `query` | string | Yes | PromQL expression for range query |
| `start` | string | Yes | Range start as RFC3339 or unix timestamp |
| `end` | string | Yes | Range end as RFC3339 or unix timestamp |
| `step` | string | Yes | Query resolution step duration (e.g., `15s`, `1m`, `5m`) |

### 3. Get Alerts (`get_alerts`)

**Endpoint:** `/api/capabilities/get_alerts`

Lists currently firing Prometheus alerts grouped by alerting rule.

**Request:**
```json
{}
```

**Response:**
```json
{
  "groups": [
    {
      "name": "all",
      "file": "",
      "alerts": [
        {
          "labels": {
            "alertname": "HighMemoryUsage",
            "severity": "warning",
            "instance": "10.244.0.5:8349"
          },
          "annotations": {
            "summary": "High memory usage detected",
            "description": "Memory usage is above 90%"
          },
          "state": "firing",
          "active_at": "2026-03-03T10:15:00Z",
          "value": "0.92"
        }
      ]
    }
  ],
  "total_alerts": 1,
  "source": "Prometheus API"
}
```

### 4. Get Targets (`get_targets`)

**Endpoint:** `/api/capabilities/get_targets`

Lists Prometheus scrape targets with health status and scrape metrics.

**Request (all targets):**
```json
{}
```

**Request (filter by state):**
```json
{
  "state": "active"
}
```

**Response:**
```json
{
  "active_targets": [
    {
      "labels": {
        "instance": "10.244.0.5:8349",
        "job": "kubernetes-pods"
      },
      "scrape_url": "http://10.244.0.5:8349/metrics",
      "health": "up",
      "last_scrape": "2026-03-03T12:00:00Z",
      "last_scrape_duration_seconds": 0.0045
    }
  ],
  "dropped_targets": 0,
  "state": "active",
  "source": "Prometheus API"
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `state` | string | No | Filter targets: `active`, `dropped`, or `any` |

---

## Architecture

```
Prometheus Query Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Forwards PromQL queries to in-cluster Prometheus
    +-- Parses vector/matrix results into structured JSON
    +-- Returns standardized responses

Request Flow:
    Agent Request → Tool Handler
    ├── Validate input (query, time range, etc.)
    ├── Forward to Prometheus HTTP API (/api/v1/query, /query_range, /alerts, /targets)
    ├── Parse response (vector → samples, matrix → series)
    └── Return structured ToolResponse

Agents (Active)
    |
    +-- Discover prometheus query tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the Prometheus query tool is automatically discovered by agents via Redis (e.g., the devops-chat-agent). You can also query the tool directly:

```bash
# Direct tool access
curl -X POST http://localhost:8371/api/capabilities/get_targets \
  -H "Content-Type: application/json" \
  -d '{
    "state": "active"
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PROMETHEUS_URL` | Prometheus server URL | `http://prometheus-server.truvag3-examples:9090` | No |
| `PORT` | HTTP server port | `8371` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |

**No API keys required** - Prometheus runs in-cluster with no authentication.

---

## Project Structure

```
prometheus-query-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── prometheus_tool.go      # Tool definition, capability registration, request/response types
├── prometheus_client.go    # Prometheus HTTP API client with traced HTTP transport
├── handlers.go             # HTTP handlers for each capability with full telemetry
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
# Should show: truvag3:service:prometheus-query-tool-service
```

**2. Prometheus API errors**

```bash
# Check logs
kubectl logs -n truvag3-examples -l app=prometheus-query-tool | grep -i "api\|error"

# Common issues:
# - Prometheus not deployed: Ensure prometheus-server is running in truvag3-examples
# - Network connectivity: Ensure pod can reach prometheus-server.truvag3-examples:9090
# - Invalid PromQL: 400 errors indicate syntax issues in the query expression
# - Query timeout: Long-range queries may exceed the 30s client timeout
```

**3. Empty query results**

The tool returns an empty `samples` or `series` array when no time series match. Verify:
- The metric name exists: try `up` as a basic connectivity check
- Label selectors are correct: use `{job="kubernetes-pods"}` format
- The time range contains data: for `query_range`, ensure `start` < `end`

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
kubectl logs -n truvag3-examples -l app=prometheus-query-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=prometheus-query-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/prometheus-query-tool-service 8371:80

# Test instant query
curl -X POST http://localhost:8371/api/capabilities/query_metrics \
  -H "Content-Type: application/json" \
  -d '{"query": "up"}'

# Test range query
curl -X POST http://localhost:8371/api/capabilities/query_range \
  -H "Content-Type: application/json" \
  -d '{"query": "rate(http_requests_total[5m])", "start": "2026-03-03T00:00:00Z", "end": "2026-03-03T12:00:00Z", "step": "1m"}'

# Test get alerts
curl -X POST http://localhost:8371/api/capabilities/get_alerts \
  -H "Content-Type: application/json" \
  -d '{}'

# Test get targets (active only)
curl -X POST http://localhost:8371/api/capabilities/get_targets \
  -H "Content-Type: application/json" \
  -d '{"state": "active"}'
```

---

## Development

### Local Development

```bash
# Set environment variables (no API keys needed!)
export REDIS_URL="redis://localhost:6379"
export PROMETHEUS_URL="http://localhost:9090"
export PORT=8371

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `prometheus_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add Prometheus client method in `prometheus_client.go` if needed

---

## Related Examples

- [devops-chat-agent](../devops-chat-agent/) - DevOps chat agent that can use this tool for infrastructure monitoring
- [devops-tool](../devops-tool/) - Kubernetes cluster management tool
- [system-utilities-tool](../system-utilities-tool/) - System resource monitoring tool
- [agent-with-telemetry](../agent-with-telemetry/) - Agent with full observability stack

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
