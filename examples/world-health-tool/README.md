# World Health Tool

A TruvaG3 tool that provides global health indicator data from the [WHO Global Health Observatory API](https://ghoapi.azureedge.net/api/) with automatic fallback to the World Bank API. This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides global health indicator capabilities that agents can discover and use. It requires **no API keys** - both the WHO Global Health Observatory API and the World Bank API are completely free and open. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

**No API keys required!** Both the WHO Global Health Observatory API and the World Bank API are completely free, open, and require no authentication or registration.

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

The fastest way to get the world health tool running. **No API keys needed!**

```bash
cd examples/world-health-tool

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
| **Health API** | http://localhost:8368 | World health indicator API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The world health tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/world-health-tool

# Build Docker image
docker build -t world-health-tool:latest .

# Load into Kind
kind load docker-image world-health-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=world-health-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/world-health-service 8368:80

# Test health indicator lookup
curl -X POST http://localhost:8368/api/capabilities/get_health_indicator \
  -H "Content-Type: application/json" \
  -d '{"indicator": "life_expectancy", "country": "JPN"}'
```

---

## Features

- **Health Indicator Lookup** - Get specific health metrics for any country (life expectancy, mortality rates, immunization coverage, etc.)
- **Indicator Catalog** - Browse and search the WHO Global Health Observatory indicator catalog
- **Country Comparison** - Compare a health indicator across multiple countries side by side with parallel API calls
- **Dual-Source Fallback** - Tries WHO GHO API first, automatically falls back to World Bank API if data is unavailable
- **15 Built-in Indicators** - Pre-mapped friendly names for common indicators (life_expectancy, infant_mortality, etc.)
- **No API Keys** - Free, open WHO GHO and World Bank APIs
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Get Health Indicator (`get_health_indicator`)

**Endpoint:** `/api/capabilities/get_health_indicator`

Gets a health indicator value for a specific country from the WHO Global Health Observatory, with automatic World Bank fallback.

**Request:**
```json
{
  "indicator": "life_expectancy",
  "country": "JPN",
  "year": 2020,
  "sex": "BTSX"
}
```

**Response:**
```json
{
  "indicator": "WHOSIS_000001",
  "friendly_name": "life_expectancy",
  "country": "JPN",
  "country_name": "Japan",
  "year": 2020,
  "value": 84.3,
  "unit": "years",
  "sex": "BTSX",
  "source": "WHO GHO"
}
```

**Available Friendly Names:**

| Friendly Name | WHO Code | Unit |
|--------------|----------|------|
| `life_expectancy` | WHOSIS_000001 | years |
| `neonatal_mortality` | MDG_0000000001 | per 1000 live births |
| `infant_mortality` | MDG_0000000003 | per 1000 live births |
| `under5_mortality` | MDG_0000000007 | per 1000 live births |
| `maternal_mortality` | MDG_0000000025 | per 100000 live births |
| `immunization_dpt3` | WHS4_100 | percent |
| `immunization_measles` | WHS4_117 | percent |
| `tuberculosis_incidence` | MDG_0000000020 | per 100000 population |
| `hiv_prevalence` | MDG_0000000029 | percent (15-49 age group) |
| `malaria_incidence` | MALARIA_EST_INCIDENCE | per 1000 population at risk |
| `health_expenditure` | GHED_CHE_pc_PPP_SHA2011 | PPP international dollars per capita |
| `physicians_density` | HWF_0001 | per 10000 population |
| `hospital_beds` | HWF_0006 | per 10000 population |
| `tobacco_use` | M_Est_tob_curr_std | percent |
| `obesity_prevalence` | NCD_BMI_30A | percent |

**Sex Filter Options:**

| Value | Meaning |
|-------|---------|
| `BTSX` | Both sexes (default) |
| `MLE` | Male only |
| `FMLE` | Female only |

### 2. List Indicators (`list_indicators`)

**Endpoint:** `/api/capabilities/list_indicators`

Lists available health indicators from the WHO Global Health Observatory catalog, with optional keyword search.

**Request (all indicators):**
```json
{}
```

**Request (search by keyword):**
```json
{
  "search": "mortality",
  "limit": 10
}
```

**Response:**
```json
{
  "indicators": [
    {
      "code": "MDG_0000000001",
      "name": "Neonatal mortality rate (per 1000 live births)",
      "description": "Neonatal mortality rate (per 1000 live births)"
    },
    {
      "code": "MDG_0000000003",
      "name": "Infant mortality rate (per 1000 live births)",
      "description": "Infant mortality rate (per 1000 live births)"
    }
  ],
  "count": 2,
  "source": "WHO GHO"
}
```

### 3. Compare Countries (`compare_countries`)

**Endpoint:** `/api/capabilities/compare_countries`

Compares a health indicator across multiple countries side by side. Requires at least 2 countries. Uses parallel API calls for fast multi-country lookups.

**Request:**
```json
{
  "indicator": "life_expectancy",
  "countries": "USA,JPN,GBR,DEU",
  "year": 2020
}
```

**Response:**
```json
{
  "indicator": "life_expectancy",
  "friendly_name": "life_expectancy",
  "unit": "years",
  "countries": [
    {
      "country": "USA",
      "country_name": "United States",
      "value": 77.0,
      "year": 2020
    },
    {
      "country": "JPN",
      "country_name": "Japan",
      "value": 84.3,
      "year": 2020
    },
    {
      "country": "GBR",
      "country_name": "United Kingdom",
      "value": 80.4,
      "year": 2020
    },
    {
      "country": "DEU",
      "country_name": "Germany",
      "value": 80.9,
      "year": 2020
    }
  ],
  "source": "WHO GHO"
}
```

---

## Architecture

```
World Health Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Queries WHO GHO API (primary source)
    +-- Falls back to World Bank API if WHO returns no data
    +-- Returns standardized responses

Dual-Source Fallback Strategy:
    Request → Resolve indicator code
    ├── WHO GHO API → Data found? → Return WHO data
    └── No data / error
        └── World Bank API → Data found? → Return World Bank data
                          └── No data   → Return error

Country Comparison (parallel):
    Request → Parse country codes
    ├── Country 1 → WHO/WorldBank → Result
    ├── Country 2 → WHO/WorldBank → Result
    └── Country N → WHO/WorldBank → Result
    └── Merge results → Return comparison

Agents (Active)
    |
    +-- Discover health tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the world health tool is automatically discovered by agents via Redis. Agents can then invoke its capabilities through orchestration. You can also query the tool directly:

```bash
# Direct tool access
curl -X POST http://localhost:8368/api/capabilities/compare_countries \
  -H "Content-Type: application/json" \
  -d '{
    "indicator": "life_expectancy",
    "countries": ["Japan", "United States"]
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8368` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `APP_ENV` | Application environment (development\|staging\|production) | `development` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry Collector endpoint | - | No |

**No API keys required** - both the WHO GHO and World Bank APIs are free and open.

---

## Project Structure

```
world-health-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── world_health_tool.go    # Tool definition, capability registration, indicator maps
├── who_client.go           # WHO GHO + World Bank API client with dual-source fallback
├── handlers.go             # HTTP handlers for each capability
├── go.mod                  # Go module definition
├── Dockerfile              # Standalone container image
├── Dockerfile.workspace    # Development build from workspace root
├── k8-deployment.yaml      # Kubernetes manifests
├── setup.sh                # Full lifecycle management script
├── .env.example            # Example environment variables
└── README.md               # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"
# Should show: truvag3:service:world-health-service
```

**2. API errors**

```bash
# Check logs
kubectl logs -n truvag3-examples -l app=world-health-tool | grep -i "api\|error"

# Common issues:
# - Network connectivity: Ensure pod can reach ghoapi.azureedge.net and api.worldbank.org
# - DNS resolution: Check cluster DNS is working
# - WHO GHO API may occasionally be slow; tool has 30s timeout configured
# - If WHO fails, World Bank fallback should activate automatically
```

**3. Country not found**

The tool uses ISO 3166-1 alpha-3 country codes. Use standard codes:
- `USA` for United States
- `GBR` for United Kingdom
- `JPN` for Japan
- `DEU` for Germany

Use the `list_indicators` capability to discover available indicator codes.

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
kubectl logs -n truvag3-examples -l app=world-health-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=world-health-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/world-health-service 8368:80

# Test health indicator lookup
curl -X POST http://localhost:8368/api/capabilities/get_health_indicator \
  -H "Content-Type: application/json" \
  -d '{"indicator": "life_expectancy", "country": "JPN"}'

# List indicators matching "mortality"
curl -X POST http://localhost:8368/api/capabilities/list_indicators \
  -H "Content-Type: application/json" \
  -d '{"search": "mortality", "limit": 10}'

# Compare countries on infant mortality
curl -X POST http://localhost:8368/api/capabilities/compare_countries \
  -H "Content-Type: application/json" \
  -d '{"indicator": "infant_mortality", "countries": "USA,JPN,GBR,SWE"}'
```

---

## Development

### Local Development

```bash
# Set environment variables (no API keys needed!)
export REDIS_URL="redis://localhost:6379"
export PORT=8368

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `world_health_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add WHO/World Bank client method in `who_client.go` if needed
5. Add indicator mappings to `whoIndicatorMap` and `worldBankIndicatorMap`

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can use this tool
- [demographics-tool](../demographics-tool/) - Population and demographics data tool
- [country-info-tool](../country-info-tool/) - Country information tool
- [economic-data-tool](../economic-data-tool/) - Economic indicators and data
- [clinical-trials-tool](../clinical-trials-tool/) - Clinical trials search tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
