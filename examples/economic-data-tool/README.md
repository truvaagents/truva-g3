# Economic Data Tool

A TruvaG3 tool that exposes U.S. and global macroeconomic data via two upstream APIs: the [Federal Reserve Economic Data (FRED) API](https://fred.stlouisfed.org/) for U.S. series (mortgage rates, CPI, unemployment, GDP, treasuries, etc.) and the [World Bank API v2](https://datahelpdesk.worldbank.org/knowledgebase/topics/125589-developer-information) for cross-country indicators (GDP, GDP per capita, inflation, unemployment). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

> **API key behavior is graceful.** The tool starts without `FRED_API_KEY` and the World Bank capabilities work key-free. FRED-backed capabilities return a configuration error when called without a key, so partial deployments are valid — provision a key only if you need U.S.-specific FRED data.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Registered Capabilities](#registered-capabilities)
- [Series Shortcuts](#series-shortcuts)
- [Country Resolution](#country-resolution)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [API Rate Limits](#api-rate-limits)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

This tool provides economic data capabilities that agents can discover and use. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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
# Expected: go version go1.26.x darwin/arm64 (or darwin/amd64)
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
# Expected: go version go1.26.x windows/amd64
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Manual installation (recommended for latest version):**
```bash
curl -LO https://go.dev/dl/go1.26.2.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.2.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

**Verify installation:**
```bash
go version
# Expected: go version go1.26.x linux/amd64
```

</details>

---

#### 5. FRED API Key (Optional)

A FRED API key is **optional**. Without it, FRED-backed capabilities (`economic_indicator`, `compare_indicators`, `search_indicators`, `indicator_info`) return a configuration error when called. World Bank-backed capabilities (`global_economic_indicator`, `compare_country_economies`) work without any key.

**To get U.S. economic data via FRED:**

1. Visit [fred.stlouisfed.org/docs/api/api_key.html](https://fred.stlouisfed.org/docs/api/api_key.html)
2. Click "Request API Key" and sign in with a FRED account (free signup)
3. Copy the API key

**Free tier includes:**
- 120 requests per minute per API key
- Full FRED catalog (800,000+ economic time series)

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

The fastest way to get the economic data tool running:

```bash
cd examples/economic-data-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**⚠️ STOP HERE (Optional)** - If you want U.S. data via FRED, open `.env` and set your API key:

```bash
nano .env    # or: code .env / vim .env
```

**Optional:** Set your FRED API key in `.env`:
- `FRED_API_KEY=your-fred-api-key` (Get free key at [fred.stlouisfed.org/docs/api/api_key.html](https://fred.stlouisfed.org/docs/api/api_key.html))
- Without a key, World Bank capabilities still work; FRED capabilities return an error

After reviewing your configuration, continue with deployment:

```bash
# 2. Deploy to Kubernetes (requires cluster and Redis to be running)
./setup.sh deploy
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image
2. Loads it into the Kind cluster
3. Creates the namespace and writes a Kubernetes Secret (`FRED_API_KEY`) + ConfigMap from your `.env` values
4. Applies the Kubernetes manifests
5. The pod starts and registers its capabilities with Redis for agent discovery

Once complete, the tool is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **Economic Data API** | http://localhost:8363 | FRED + World Bank economic data API |

> **Port note:** This tool defaults to port 8363, which is the same default used by the [slack-tool](../slack-tool/). They don't conflict inside the cluster (different ClusterIPs), but if you port-forward both at once you'll need to change one tool's `PORT` in its `.env` and re-run `./setup.sh rollout` before forwarding.

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Cluster and Infrastructure Are Running

The economic data tool needs a Kind cluster and Redis. If you haven't bootstrapped these from another example, run:

```bash
cd examples/economic-data-tool
./setup.sh cluster   # Create the Kind cluster
./setup.sh infra     # Deploy Redis + observability stack (Prometheus, Grafana, Jaeger)
```

The `cluster` and `infra` subcommands are shared across every tool's `setup.sh` — they bootstrap the same cluster regardless of which tool you run them from.

#### Step 2: Build and Deploy

```bash
cd examples/economic-data-tool

# (Optional) Build the Docker image as a separate step — useful if you want to
# inspect or test the image before deploying. Skip this and `./setup.sh deploy`
# will run the same build as its first step.
./setup.sh docker-build

# Build the Docker image, load it into Kind, create the namespace, create the
# Secret and ConfigMap from .env (FRED_API_KEY goes in the Secret — World Bank
# capabilities need no key), then apply the Kubernetes manifests.
./setup.sh deploy

# Verify the rollout
./setup.sh status
```

> **Provisioning the FRED key:** put `FRED_API_KEY=<your-key>` in `.env` before running `./setup.sh deploy`. If you later edit `.env` to add or change the key, run `./setup.sh rollout` to regenerate the secret and restart the pod.

#### Step 3: Test the Tool

```bash
# Port forward the tool service to localhost:8363 (runs in foreground; Ctrl-C to stop)
./setup.sh forward
```

In another terminal:

```bash
# Test a World Bank capability (works without any API key)
curl -X POST http://localhost:8363/api/capabilities/global_economic_indicator \
  -H "Content-Type: application/json" \
  -d '{"country": "Brazil"}'

# Test a FRED capability (requires FRED_API_KEY in the deployment)
curl -X POST http://localhost:8363/api/capabilities/economic_indicator \
  -H "Content-Type: application/json" \
  -d '{"indicator": "mortgage_30y", "limit": 4}'
```

> **Shortcut:** `./setup.sh test` runs a built-in smoke test against the deployed tool — it hits `/health`, `/api/capabilities`, plus sample `economic_indicator` (mortgage rate) and `search_indicators` (unemployment) calls. The test starts and stops its own port-forward, so you don't need `./setup.sh forward` running first.

---

## Features

- **U.S. Economic Indicators (FRED)** - Mortgage rates, CPI, unemployment, GDP, treasury yields, housing data, and 800,000+ other FRED series
- **Multi-Indicator Comparison** - Pull multiple FRED series for the same period in a single request
- **Series Search** - Discover FRED series IDs by keyword
- **Series Metadata** - Get full descriptions, frequency, units, and observation ranges for any FRED series
- **Global Country Data (World Bank)** - GDP, GDP per capita, inflation, unemployment for 200+ countries
- **Cross-Country Comparison** - Compare economic indicators across multiple countries side-by-side
- **Friendly Shortcuts** - Use `mortgage_30y` instead of `MORTGAGE30US`; pass `Brazil` or `BRA` instead of looking up ISO codes
- **Graceful Degradation** - Boots without `FRED_API_KEY`; only FRED-backed capabilities fail when the key is missing
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers six capabilities — four backed by FRED (U.S. only, requires API key) and two backed by the World Bank (global, no key required).

### 1. Economic Indicator (`economic_indicator`) — FRED

**Endpoint:** `/api/capabilities/economic_indicator`

Gets current and historical values for a specific U.S. FRED series. Accepts either a [shortcut name](#series-shortcuts) (e.g., `mortgage_30y`) or a raw FRED series ID (e.g., `MORTGAGE30US`).

**Request:**
```json
{
  "indicator": "mortgage_30y",
  "limit": 4,
  "start_date": "2025-01-01",
  "end_date": "2026-01-31"
}
```

**Response:**
```json
{
  "indicator": "MORTGAGE30US",
  "title": "30-Year Fixed Rate Mortgage Average",
  "frequency": "Weekly",
  "units": "Percent",
  "last_updated": "2026-01-30 11:01:23-06",
  "observations": [
    {"date": "2026-01-29", "value": "6.95"},
    {"date": "2026-01-22", "value": "6.96"},
    {"date": "2026-01-15", "value": "7.04"},
    {"date": "2026-01-08", "value": "6.91"}
  ],
  "source": "FRED API"
}
```

### 2. Compare Indicators (`compare_indicators`) — FRED

**Endpoint:** `/api/capabilities/compare_indicators`

Pulls multiple FRED series over the same time period in a single call. Useful for tracking spreads (10Y treasury vs Fed funds rate), correlations (CPI vs unemployment), or building multi-series dashboards.

**Request:**
```json
{
  "indicators": "mortgage_30y,treasury_10y,fed_funds_rate",
  "start_date": "2025-01-01",
  "end_date": "2026-01-31",
  "limit": 12
}
```

**Response:**
```json
{
  "indicators": [
    {
      "series_id": "MORTGAGE30US",
      "title": "30-Year Fixed Rate Mortgage Average",
      "units": "Percent",
      "frequency": "Weekly",
      "observations": [{"date": "2026-01-29", "value": "6.95"}]
    },
    {
      "series_id": "DGS10",
      "title": "10-Year Treasury Constant Maturity Rate",
      "units": "Percent",
      "frequency": "Daily",
      "observations": [{"date": "2026-01-30", "value": "4.51"}]
    },
    {
      "series_id": "FEDFUNDS",
      "title": "Federal Funds Effective Rate",
      "units": "Percent",
      "frequency": "Monthly",
      "observations": [{"date": "2025-12-01", "value": "4.33"}]
    }
  ],
  "period": {"start": "2025-01-01", "end": "2026-01-31"},
  "source": "FRED API"
}
```

### 3. Search Indicators (`search_indicators`) — FRED

**Endpoint:** `/api/capabilities/search_indicators`

Searches the FRED catalog by keyword. Use this to discover series IDs before calling `economic_indicator`.

**Request:**
```json
{
  "query": "housing prices",
  "limit": 5
}
```

**Response:**
```json
{
  "query": "housing prices",
  "count": 5,
  "results": [
    {
      "series_id": "CSUSHPINSA",
      "title": "S&P/Case-Shiller U.S. National Home Price Index",
      "frequency": "Monthly",
      "units": "Index Jan 2000=100",
      "seasonal_adjustment": "Not Seasonally Adjusted",
      "last_updated": "2026-01-28 09:00:00-06",
      "notes": "For more information regarding the index, please visit..."
    }
  ],
  "source": "FRED API"
}
```

### 4. Indicator Info (`indicator_info`) — FRED

**Endpoint:** `/api/capabilities/indicator_info`

Returns the full metadata record for a single FRED series — observation date range, frequency, units, seasonal adjustment, and the series description notes.

**Request:**
```json
{
  "indicator": "unemployment"
}
```

**Response:**
```json
{
  "series_id": "UNRATE",
  "title": "Civilian Unemployment Rate",
  "observation_start": "1948-01-01",
  "observation_end": "2025-12-01",
  "frequency": "Monthly",
  "units": "Percent",
  "seasonal_adjustment": "Seasonally Adjusted",
  "last_updated": "2026-01-10 07:45:00-06",
  "notes": "The unemployment rate represents the number of unemployed as a percentage of the labor force...",
  "source": "FRED API"
}
```

### 5. Global Economic Indicator (`global_economic_indicator`) — World Bank

**Endpoint:** `/api/capabilities/global_economic_indicator`

Fetches GDP, GDP per capita, inflation, and unemployment for any country. Accepts full country names (`Brazil`, `United Kingdom`), ISO3 codes (`BRA`, `GBR`), or ISO2 codes (`BR`, `GB`). See [Country Resolution](#country-resolution) for details.

**Request:**
```json
{
  "country": "Brazil",
  "year": "2022"
}
```

**Response:**
```json
{
  "country": "Brazil",
  "country_code": "BRA",
  "region": "Latin America & Caribbean",
  "income_level": "Upper middle income",
  "gdp": 1920095821517.66,
  "gdp_per_capita": 8918.07,
  "inflation_rate": 9.28,
  "unemployment_rate": 9.23,
  "data_year": "2022",
  "source": "World Bank Open Data"
}
```

When `year` is omitted, the tool returns the latest available value per indicator (which may differ across indicators — World Bank data typically lags 1-2 years).

### 6. Compare Country Economies (`compare_country_economies`) — World Bank

**Endpoint:** `/api/capabilities/compare_country_economies`

Side-by-side comparison of the same four indicators across multiple countries.

**Request:**
```json
{
  "countries": "BRA,IND,CHN",
  "year": "2022"
}
```

**Response:**
```json
{
  "countries": [
    {
      "country": "Brazil",
      "country_code": "BRA",
      "region": "Latin America & Caribbean",
      "income_level": "Upper middle income",
      "gdp": 1920095821517.66,
      "gdp_per_capita": 8918.07,
      "inflation_rate": 9.28,
      "unemployment_rate": 9.23,
      "data_year": "2022",
      "source": "World Bank Open Data"
    }
  ],
  "data_year": "2022",
  "source": "World Bank Open Data"
}
```

---

## Series Shortcuts

The tool ships with shortcut names for the most frequently requested U.S. series. Pass either form to any FRED capability:

| Shortcut | FRED Series ID | Title | Frequency |
|----------|----------------|-------|-----------|
| `mortgage_30y` | `MORTGAGE30US` | 30-Year Fixed Rate Mortgage Average | Weekly |
| `mortgage_15y` | `MORTGAGE15US` | 15-Year Fixed Rate Mortgage Average | Weekly |
| `fed_funds_rate` | `FEDFUNDS` | Federal Funds Effective Rate | Monthly |
| `inflation_cpi` | `CPIAUCSL` | Consumer Price Index for All Urban Consumers | Monthly |
| `unemployment` | `UNRATE` | Civilian Unemployment Rate | Monthly |
| `gdp` | `GDP` | Gross Domestic Product | Quarterly |
| `real_gdp` | `GDPC1` | Real Gross Domestic Product | Quarterly |
| `treasury_10y` | `DGS10` | 10-Year Treasury Constant Maturity Rate | Daily |
| `treasury_2y` | `DGS2` | 2-Year Treasury Constant Maturity Rate | Daily |
| `sp500` | `SP500` | S&P 500 Index | Daily |
| `prime_rate` | `DPRIME` | Bank Prime Loan Rate | Daily |
| `housing_starts` | `HOUST` | Housing Starts: Total | Monthly |
| `home_price_index` | `CSUSHPINSA` | S&P/Case-Shiller U.S. National Home Price Index | Monthly |
| `personal_income` | `PI` | Personal Income | Monthly |
| `consumer_sentiment` | `UMCSENT` | University of Michigan Consumer Sentiment | Monthly |

Any other FRED series ID can be passed directly (e.g., `M2SL`, `PCEPI`, `INDPRO`) — the tool resolves shortcuts case-insensitively and falls through to the raw series ID for unknown inputs.

---

## Country Resolution

World Bank capabilities accept three input forms for the `country` / `countries` fields:

| Input form | Example | Notes |
|------------|---------|-------|
| Full country name (case-insensitive) | `Brazil`, `united kingdom`, `south korea` | ~120 names recognized including common aliases (`uk` → GBR, `usa` → USA, `holland` → NLD, `czechia` → CZE) |
| ISO 3166-1 alpha-3 code | `BRA`, `GBR`, `JPN` | Passed through unchanged |
| ISO 3166-1 alpha-2 code | `BR`, `GB`, `JP` | Mapped to ISO3 (~50 common codes mapped) |

Unrecognized names are upper-cased and forwarded to the World Bank API, which will return a 200 with empty data if the code is invalid. To list every recognized name, grep [worldbank_client.go:237-279](worldbank_client.go#L237-L279).

For `compare_country_economies`, pass a comma-separated list — any mix of forms works, e.g. `countries: "Brazil,IND,CHN,uk"`.

---

## Architecture

```
Economic Data Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Routes FRED capabilities to api.stlouisfed.org (auth via api_key query parameter)
    +-- Routes World Bank capabilities to api.worldbank.org/v2 (no auth)
    +-- Resolves shortcuts (mortgage_30y -> MORTGAGE30US, Brazil -> BRA)
    +-- Returns standardized responses

Agents (Active)
    |
    +-- Discover Economic Data tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows (e.g., FRED rates + currency-tool + news-tool)
```

### Dual-Backend Split

| Capability | Backend | Auth | Scope |
|------------|---------|------|-------|
| `economic_indicator` | FRED | API key | U.S. only |
| `compare_indicators` | FRED | API key | U.S. only |
| `search_indicators` | FRED | API key | U.S. only |
| `indicator_info` | FRED | API key | U.S. only |
| `global_economic_indicator` | World Bank | None | 200+ countries |
| `compare_country_economies` | World Bank | None | 200+ countries |

If the agent's prompt needs U.S.-specific detail (mortgage rates, treasury yields, CPI), it should target FRED capabilities. For cross-country GDP / inflation / unemployment comparisons, World Bank capabilities are the right tool — and they work even when `FRED_API_KEY` isn't provisioned.

### Integration with Agents

Once deployed, the tool is automatically discovered by agents via Redis. A common pattern pairs it with `currency-tool` for "what's the Brazilian inflation rate, converted to a real-rate-adjusted USD return?" style queries:

```bash
# Query through an orchestrating agent
curl -X POST http://localhost:8350/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "Compare 30-year mortgage rate trends with the Fed funds rate over the last 12 months.",
    "ai_synthesis": true
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `FRED_API_KEY` | FRED API key (enables FRED-backed capabilities) | - | No |
| `PORT` | HTTP server port | `8363` | No |
| `NAMESPACE` | Kubernetes namespace | — (set via the `.env` ConfigMap that `setup.sh` creates; empty if you deploy the manifest manually without that ConfigMap) | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `APP_ENV` | Environment profile (development\|staging\|production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

The `FRED_API_KEY` is referenced as `optional: true` in [k8-deployment.yaml](k8-deployment.yaml) — the pod boots even when the secret is absent. Calls to FRED-backed capabilities will return HTTP 403 with `code: "AUTH_ERROR"` and message `"FRED API key not configured"` until the key is provisioned.

---

## API Rate Limits

| Backend | Rate Limit | Notes |
|---------|-----------|-------|
| **FRED** | 120 requests / minute / API key | Free tier; resets per minute |
| **World Bank** | No published hard limit | Free; the v2 API is rate-limited at the IP level but is generous in practice |

The tool implements:
- Traced HTTP client for all API calls (OpenTelemetry spans on both backends)
- 30-second HTTP timeout per upstream call
- HTTP/2 keep-alive connection pooling
- Structured error logging for rate-limit tracking
- Graceful error responses on API failures (with backend identified in the error)

---

## Project Structure

```
economic-data-tool/
├── main.go                  # Entry point, framework setup, telemetry init
├── economic_data_tool.go    # Tool definition, capability registration, shortcut tables
├── fred_client.go           # FRED API client (observations, search, series info)
├── worldbank_client.go      # World Bank API client + country-name -> ISO3 resolver
├── handlers.go              # HTTP handlers for each capability
├── go.mod                   # Go module definition
├── Dockerfile               # Standalone container image
├── Dockerfile.workspace     # Development build from workspace root
├── k8-deployment.yaml       # Kubernetes manifests (FRED_API_KEY is optional)
├── setup.sh                 # Full lifecycle management script
├── .env.example             # Environment variable template
└── README.md                # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

Confirm the pod is up and registered with Redis:

```bash
# Is the pod running?
./setup.sh status

# Look for "registered" or "discovery" messages in the startup logs
./setup.sh logs | grep -iE "register|discovery"
```

You can also browse the live registry in your browser at **http://registry.localhost** (deployed by `./setup.sh infra` alongside Grafana/Prometheus/Jaeger). The `economic-data-tool-service` entry should be visible once the pod is healthy.

**2. FRED capabilities return "FRED API key not configured"**

The deployed pod doesn't have a `FRED_API_KEY` value. To fix:

```bash
# 1. Set FRED_API_KEY in .env (get a free key from
#    https://fred.stlouisfed.org/docs/api/api_key.html)
nano .env

# 2. Regenerate the secret from .env and restart the pod
./setup.sh rollout
```

`./setup.sh rollout` re-reads `.env`, rewrites the `economic-data-tool-secrets` Secret, and restarts the deployment so the new value is picked up.

**3. FRED API returns 400 / "Bad Request"**

```bash
# Common issues:
# - Invalid series ID: pass a valid shortcut (see Series Shortcuts) or a real FRED ID
# - Invalid date format: dates must be YYYY-MM-DD
# - Date range too narrow: a daily series with a 1-day range may return zero observations
# - limit > 100: the tool clamps `limit` to 100 (FRED itself supports higher; this is a tool-side cap)
```

**4. World Bank returns empty data for a country**

```bash
# Common issues:
# - Country name not in the resolver: the request still goes through with the upper-cased
#   input as the country code, which the World Bank API silently ignores.
#   Pass an ISO3 code (e.g., "BRA") to disambiguate.
# - Year is too recent: World Bank data lags 1-2 years; omit `year` to get the latest value
# - Indicator not collected for that country/year: response will have nulls for that field
```

**5. Both backends slow or timing out**

```bash
# Tail tool logs and look for upstream latency
./setup.sh logs | grep -i "duration\|timeout"
```

HTTP timeout is 30s; if either upstream is slow, the tool returns a 5xx with the backend named in the error. Retry once on transient 5xxs — both APIs occasionally degrade.

**6. Docker build fails**

```bash
# Ensure Docker is running
docker info
```

**7. Kind cluster not found**

```bash
# List existing clusters
kind get clusters

# Create a new cluster if none exists
./setup.sh cluster
```

### Useful Commands

```bash
# Tail tool logs
./setup.sh logs

# Check pod/deployment status
./setup.sh status

# Port forward the tool service to localhost:8363
# (note: 8363 also defaults for slack-tool — change one tool's PORT in .env and `./setup.sh rollout` if both are forwarded)
./setup.sh forward

# Run the built-in smoke test (health + capabilities + sample economic_indicator and search_indicators calls)
./setup.sh test

# Rebuild the image with --no-cache and redeploy (use after dependency changes)
./setup.sh rebuild

# Restart the deployment to pick up .env changes (re-reads .env, rewrites Secret/ConfigMap)
./setup.sh rollout

# Remove the tool deployment only (keep cluster + infra for other tools)
./setup.sh clean

# Tear down the entire Kind cluster (prompts y/N before destroying)
./setup.sh clean-all
```

Once `./setup.sh forward` is running, hit the capabilities directly:

```bash
# World Bank capability — no API key needed
curl -X POST http://localhost:8363/api/capabilities/global_economic_indicator \
  -H "Content-Type: application/json" \
  -d '{"country": "Brazil"}'

# FRED capability — requires FRED_API_KEY in the deployment
curl -X POST http://localhost:8363/api/capabilities/economic_indicator \
  -H "Content-Type: application/json" \
  -d '{"indicator": "mortgage_30y", "limit": 4}'

# Search FRED for a topic
curl -X POST http://localhost:8363/api/capabilities/search_indicators \
  -H "Content-Type: application/json" \
  -d '{"query": "housing prices", "limit": 5}'

# Cross-country comparison
curl -X POST http://localhost:8363/api/capabilities/compare_country_economies \
  -H "Content-Type: application/json" \
  -d '{"countries": "BRA,IND,CHN", "year": "2022"}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export REDIS_URL="redis://localhost:6379"
export FRED_API_KEY="your-fred-api-key"   # optional; World Bank capabilities work without it
export PORT=8363

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `economic_data_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add backend client method in `fred_client.go` or `worldbank_client.go` if needed
5. If adding a new FRED series shortcut, update `seriesShortcuts` and `seriesMetadata` in `economic_data_tool.go`

---

## Related Examples

- [stock-market-tool](../stock-market-tool/) - Stock prices and market data (similar optional-API-key pattern)
- [currency-tool](../currency-tool/) - Currency conversion (pairs well for cross-country economic queries)
- [fiscal-data-tool](../fiscal-data-tool/) - U.S. Treasury fiscal data (complements FRED's monetary series)
- [agent-with-orchestration](../agent-with-orchestration/) - Orchestration example
- [news-tool](../news-tool/) - News aggregation (pairs with economic indicators for context)

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
