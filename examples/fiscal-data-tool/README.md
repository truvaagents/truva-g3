# Fiscal Data Tool

A TruvaG3 tool that provides U.S. federal fiscal data and global government fiscal indicators using the [U.S. Treasury Fiscal Data API](https://fiscaldata.treasury.gov/api-documentation/) and the [World Bank Open Data API](https://datahelpdesk.worldbank.org/knowledgebase/articles/889392-about-the-indicators-api-documentation). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides fiscal data capabilities that agents can discover and use. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

### Prerequisites

Before running this example, you need to install the following tools. Choose the instructions for your operating system.

> **Note:** No API key is required. Both the [U.S. Treasury Fiscal Data API](https://fiscaldata.treasury.gov/api-documentation/) and the [World Bank Open Data API](https://datahelpdesk.worldbank.org/knowledgebase/articles/889392-about-the-indicators-api-documentation) are free, public, and unauthenticated.

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
sudo apt-get update && sudo apt-get install -y kubectl
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

**Verify installation:**
```bash
go version
# Expected: go version go1.27.x darwin/arm64 (or darwin/amd64)
```

</details>

<details>
<summary><strong>Windows Installation</strong></summary>

**Using Chocolatey:**
```powershell
choco install golang
```

**Verify installation:**
```powershell
go version
# Expected: go version go1.27.x windows/amd64
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Manual installation (recommended for latest version):**
```bash
curl -LO https://go.dev/dl/go1.27.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.27.0.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

**Verify installation:**
```bash
go version
# Expected: go version go1.27.x linux/amd64
```

</details>

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

The fastest way to get this tool running:

```bash
cd examples/fiscal-data-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env

# 2. Deploy to Kubernetes (requires cluster and Redis to be running)
./setup.sh deploy
```

> **Note:** This tool uses free public APIs and does not require any API keys.

**What `./setup.sh deploy` does:**
1. Builds the Docker image
2. Loads it into the Kind cluster
3. Deploys the tool to Kubernetes
4. Registers capabilities with Redis for agent discovery

Once complete, the tool is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **Fiscal Data API** | http://localhost:8364 | Treasury + World Bank fiscal data API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The fiscal data tool requires Redis for service discovery. If you haven't already set up infrastructure, run these from this directory:

```bash
cd examples/fiscal-data-tool

./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack (Prometheus, Grafana, Jaeger)
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

#### Step 2: Build and Deploy

`setup.sh` handles the Docker build, Kind image load, namespace + ConfigMap creation, and manifest apply. Use these subcommands instead of raw `kubectl`:

```bash
cd examples/fiscal-data-tool

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
# Port forward the tool service to localhost:8364
./setup.sh forward

# Or run a built-in smoke test against the deployed tool
./setup.sh test
```

In a second terminal (while `./setup.sh forward` is running):

```bash
curl -X POST http://localhost:8364/api/capabilities/national_debt \
  -H "Content-Type: application/json" \
  -d '{}'
```

---

## Features

- **U.S. National Debt** - Historical debt-to-the-penny totals with public vs. intragovernmental breakdown
- **Treasury Securities Rates** - Average interest rates on Bills, Notes, Bonds, and TIPS
- **Treasury Exchange Rates** - Official U.S. Treasury reporting rates for foreign currencies
- **Federal Spending** - Monthly receipts, outlays, and surplus/deficit from the Monthly Treasury Statement
- **Global Fiscal Indicators** - Debt-to-GDP, revenue-to-GDP, and expenditure-to-GDP for 200+ countries via the World Bank
- **Cross-Country Comparison** - Side-by-side fiscal health comparison across multiple economies
- **No API Key Required** - Both upstream APIs are free and unauthenticated
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. National Debt (`national_debt`)

**Endpoint:** `/api/capabilities/national_debt`

Gets the U.S. national debt from the Treasury Department, broken down into debt held by the public and intragovernmental holdings.

**Request:**
```json
{
  "limit": 10,
  "start_date": "2024-01-01"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "records": [
      {
        "date": "2024-12-31",
        "total_public_debt": 36219234567890.12,
        "debt_held_by_public": 28456789012345.67,
        "intragovernmental_holdings": 7762445555544.45,
        "fiscal_year": "2025",
        "fiscal_quarter": "1"
      }
    ],
    "source": "U.S. Treasury Fiscal Data API"
  }
}
```

### 2. Treasury Rates (`treasury_rates`)

**Endpoint:** `/api/capabilities/treasury_rates`

Gets average interest rates on U.S. Treasury securities including Treasury Bills, Notes, Bonds, and TIPS.

**Request:**
```json
{
  "security_type": "Treasury Bonds",
  "limit": 10,
  "start_date": "2024-01-01"
}
```

**Valid `security_type` values:** `Treasury Bills`, `Treasury Notes`, `Treasury Bonds`, `Treasury Inflation-Protected Securities`. Omit to retrieve all security types.

**Response:**
```json
{
  "success": true,
  "data": {
    "records": [
      {
        "date": "2024-12-31",
        "security_type": "Treasury Bonds",
        "security_desc": "Treasury Bonds",
        "avg_interest_rate": 3.245,
        "fiscal_year": "2025"
      }
    ],
    "source": "U.S. Treasury Fiscal Data API"
  }
}
```

### 3. Exchange Rates (`exchange_rates`)

**Endpoint:** `/api/capabilities/exchange_rates`

Gets official U.S. Treasury exchange rates for foreign currencies. These are quarterly Treasury reporting rates used for federal government reporting — not real-time market rates.

**Request:**
```json
{
  "currencies": "Euro Zone-Euro,Japan-Yen,United Kingdom-Pound",
  "limit": 10,
  "start_date": "2024-01-01"
}
```

> Use Treasury currency format (e.g., `Euro Zone-Euro`, `Japan-Yen`, `Canada-Dollar`). Omit to retrieve all currencies.

**Response:**
```json
{
  "success": true,
  "data": {
    "records": [
      {
        "date": "2024-12-31",
        "country_currency": "Euro Zone-Euro",
        "exchange_rate": 0.962,
        "effective_date": "2024-12-31"
      }
    ],
    "source": "U.S. Treasury Fiscal Data API"
  }
}
```

### 4. Federal Spending (`federal_spending`)

**Endpoint:** `/api/capabilities/federal_spending`

Gets a summary of federal government receipts (revenue) and outlays (spending) from the Monthly Treasury Statement.

**Request:**
```json
{
  "limit": 12,
  "start_date": "2024-01-01"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "records": [
      {
        "date": "2024-12-31",
        "fiscal_year": "2025",
        "fiscal_month": "December",
        "receipts": 458123456789.00,
        "outlays": 612987654321.00,
        "surplus_or_deficit": -154864197532.00
      }
    ],
    "source": "U.S. Treasury Fiscal Data API"
  }
}
```

> The `fiscal_month` field is populated from the Treasury API's `record_calendar_month` value (e.g., `"December"`), not the fiscal-calendar month number.

### 5. Global Fiscal Data (`global_fiscal_data`)

**Endpoint:** `/api/capabilities/global_fiscal_data`

Gets government fiscal data (debt, revenue, expenditure as percentage of GDP) for any country worldwide from the World Bank. Covers 200+ countries.

> For detailed U.S. Treasury data, use [`national_debt`](#1-national-debt-national_debt) or [`federal_spending`](#4-federal-spending-federal_spending) instead.

**Request:**
```json
{
  "country": "Japan",
  "year": "2022"
}
```

Accepts either a country name (`Japan`, `Germany`) or an ISO3 code (`JPN`, `DEU`). World Bank data may lag 1-2 years; omit `year` to get the latest available.

**Response:**
```json
{
  "success": true,
  "data": {
    "country": "Japan",
    "country_code": "JPN",
    "region": "East Asia & Pacific",
    "income_level": "High income",
    "debt_to_gdp_pct": 219.95,
    "revenue_to_gdp_pct": 21.43,
    "expenditure_to_gdp_pct": 27.18,
    "data_year": "2022",
    "source": "World Bank Open Data"
  }
}
```

> Indicator fields (`debt_to_gdp_pct`, `revenue_to_gdp_pct`, `expenditure_to_gdp_pct`) are omitted when the World Bank has no value for that country/year combination.

### 6. Compare Country Fiscal (`compare_country_fiscal`)

**Endpoint:** `/api/capabilities/compare_country_fiscal`

Compares government fiscal health across multiple countries (2-10) using World Bank data, covering debt-to-GDP, revenue-to-GDP, and expenditure-to-GDP ratios.

**Request:**
```json
{
  "countries": "JPN,DEU,USA",
  "year": "2022"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "countries": [
      {
        "country": "Japan",
        "country_code": "JPN",
        "region": "East Asia & Pacific",
        "income_level": "High income",
        "debt_to_gdp_pct": 219.95,
        "revenue_to_gdp_pct": 21.43,
        "expenditure_to_gdp_pct": 27.18,
        "data_year": "2022",
        "source": "World Bank Open Data"
      }
    ],
    "data_year": "2022",
    "source": "World Bank Open Data"
  }
}
```

> Countries that fail to resolve (unknown code) or return no World Bank data are silently skipped. If none of the requested countries resolve, the request returns `500 SERVICE_UNAVAILABLE`.

---

## Architecture

```
Fiscal Data Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Calls U.S. Treasury Fiscal Data API (no auth)
    +-- Calls World Bank Open Data API (no auth)
    +-- Returns standardized responses

Agents (Active)
    |
    +-- Discover fiscal data tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the fiscal data tool is automatically discovered by agents via Redis. You can query fiscal data through natural language:

```bash
# Query through an orchestrating agent
curl -X POST http://localhost:8091/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "Compare debt-to-GDP between Japan, Germany, and the U.S.",
    "ai_synthesis": true
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8364` | No |
| `NAMESPACE` | Kubernetes namespace passed to the framework | — (set to `truvag3-examples` via the ConfigMap that `setup.sh` builds from `.env`) | No |
| `APP_ENV` | Environment (development/staging/production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

### .env File

Copy `.env.example` to `.env` and adjust as needed:

```bash
cp .env.example .env
# Defaults work for local development
```

---

## API Rate Limits

Both upstream APIs are free and unauthenticated, with generous limits for typical usage:

| API | Rate Limit |
|-----|------------|
| **U.S. Treasury Fiscal Data API** | No published hard limit; the API is designed for public open-data access |
| **World Bank Open Data API** | No published hard limit; intended for public research and analytics |

The tool implements:
- Traced HTTP client for all API calls (OpenTelemetry spans)
- 30-second per-request timeout with HTTP/2 keep-alive pooling
- Structured error logging with retryable error classification
- Per-capability latency histograms and request counters

---

## Project Structure

```
fiscal-data-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── fiscal_data_tool.go     # Tool definition, capability registration
├── treasury_client.go      # U.S. Treasury Fiscal Data API client
├── worldbank_client.go     # World Bank Open Data API client
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

Ensure the tool is registered with Redis:
```bash
# List all registered services
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:services:*"

# Confirm this tool is registered by name (resolves to the service ID)
kubectl exec -n truvag3-examples deploy/redis -- redis-cli GET "truvag3:names:fiscal-data-tool"
```

**2. "REDIS_URL is required" error**

Ensure Redis is running and `REDIS_URL` is set:
```bash
# Confirm the tool pod sees Redis; status output includes the Redis pod
./setup.sh status

# For local (non-Kubernetes) development
redis-cli ping
```

**3. Treasury API returns no data**

```bash
# Stream logs and grep for upstream errors
./setup.sh logs | grep -i "treasury"

# Common issues:
# - Invalid start_date: must be YYYY-MM-DD format
# - Invalid security_type: use exact Treasury labels (Treasury Bills, Treasury Notes, ...)
# - Invalid currencies: use Treasury currency format (e.g., 'Euro Zone-Euro')
```

**4. World Bank returns null indicators**

```bash
# Stream logs and grep for World Bank errors
./setup.sh logs | grep -i "world bank"

# Common issues:
# - Country code not recognized: try ISO3 (JPN, DEU) instead of country names
# - Recent year requested: World Bank fiscal data typically lags 1-2 years; omit 'year' for latest
# - Indicator unavailable for that country: some economies don't report all three indicators
```

**5. `compare_country_fiscal` rejects request**

```bash
# Common validation errors:
# - "at least 2 countries required": pass at least 2 comma-separated values
# - "maximum 10 countries allowed": limit to 10 per request
# - "countries field is required": ensure the 'countries' string is non-empty
```

**6. Docker build fails**

```bash
# Ensure Docker is running
docker info
```

**7. Kind cluster not found**

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

# Port forward the tool to localhost:8364
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
# National debt (latest record)
curl -X POST http://localhost:8364/api/capabilities/national_debt \
  -H "Content-Type: application/json" \
  -d '{}'

# Treasury rates filtered by security type
curl -X POST http://localhost:8364/api/capabilities/treasury_rates \
  -H "Content-Type: application/json" \
  -d '{"security_type": "Treasury Bonds", "limit": 5}'

# Global fiscal data for a country
curl -X POST http://localhost:8364/api/capabilities/global_fiscal_data \
  -H "Content-Type: application/json" \
  -d '{"country": "Japan"}'

# Cross-country comparison
curl -X POST http://localhost:8364/api/capabilities/compare_country_fiscal \
  -H "Content-Type: application/json" \
  -d '{"countries": "JPN,DEU,USA"}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export REDIS_URL="redis://localhost:6379"
export PORT=8364

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `fiscal_data_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add upstream client method in `treasury_client.go` or `worldbank_client.go` if needed

---

## Related Examples

- [economic-data-tool](../economic-data-tool/) - Macroeconomic indicators (BEA, BLS, FRED)
- [stock-market-tool](../stock-market-tool/) - Real-time stock quotes and company data
- [currency-tool](../currency-tool/) - Currency exchange rates
- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can orchestrate tools

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
