# Demographics Tool

A TruvaG3 tool that provides U.S. and global demographic data by combining the [U.S. Census Bureau API](https://www.census.gov/data/developers/data-sets.html) (American Community Survey 5-Year Estimates) with the [World Bank Open Data API](https://data.worldbank.org/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides demographic data capabilities that agents can discover and use. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

#### 5. Census API Key (Optional)

The Census Bureau API key is **optional**. Without it, the tool still works but is rate-limited to **500 requests per day** per IP. With a key, the limit jumps to the standard authenticated tier — more than enough for any realistic agent workload.

The World Bank API does **not** require a key, so `global_demographics` and `compare_countries_demographics` work out of the box.

**To get a free Census API key:**

1. Visit [api.census.gov/data/key_signup.html](https://api.census.gov/data/key_signup.html)
2. Fill in the short form (name, email, organization)
3. Check your email for the key (usually arrives within a minute)
4. Add it to `.env` as `CENSUS_API_KEY=your-key-here`

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

The fastest way to get the demographics tool running:

```bash
cd examples/demographics-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**⚠️ STOP HERE (Optional)** - If you want to lift the 500 req/day Census limit, open `.env` and add your API key:

```bash
nano .env    # or: code .env / vim .env
```

- `CENSUS_API_KEY=your-key-here` (Get a free key at [api.census.gov/data/key_signup.html](https://api.census.gov/data/key_signup.html))
- Without a key, the tool still works against both Census and World Bank — only the Census endpoints share the 500 req/day cap

After reviewing your configuration, continue with deployment:

```bash
# 2. Deploy to Kubernetes (requires cluster and Redis to be running)
./setup.sh deploy
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image
2. Loads it into the Kind cluster
3. Deploys the tool to Kubernetes
4. Registers capabilities with Redis for agent discovery

Once complete, port forward and access the tool locally:

```bash
./setup.sh forward    # forwards demographics-tool-service to localhost:8365
```

| Service | URL | Description |
|---------|-----|-------------|
| **Demographics API** | http://localhost:8365 | Demographics data API (Census + World Bank) |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The demographics tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

All build and deploy lifecycle steps are wrapped by [`setup.sh`](setup.sh). The most common command is:

```bash
cd examples/demographics-tool
./setup.sh deploy
```

`./setup.sh deploy` builds the Docker image, loads it into Kind, applies the namespace/secret/ConfigMap (sourced from `.env`), and rolls out the deployment.

Other useful subcommands:

| Command | Purpose |
|---------|---------|
| `./setup.sh build` | Build the Go binary only (no Docker) |
| `./setup.sh docker-build` | Build the Docker image without deploying |
| `./setup.sh full-deploy` | Create cluster + infrastructure + deploy in one shot |
| `./setup.sh rebuild` | Rebuild with `--no-cache` and redeploy (fresh dependencies) |
| `./setup.sh rollout` | Restart the deployment to pick up new secrets/config |
| `./setup.sh rollout --build` | Rebuild the image, then restart the deployment |
| `./setup.sh status` | Show pod and service status |
| `./setup.sh logs` | Tail the tool logs |
| `./setup.sh clean` | Remove the tool deployment only |
| `./setup.sh clean-all` | Delete the entire Kind cluster |
| `./setup.sh help` | Show all commands |

#### Step 3: Test the Tool

```bash
# Port forward to access locally
./setup.sh forward

# Or port forward tool + Grafana + Prometheus + Jaeger dashboards
./setup.sh forward-all

# Run the built-in smoke tests against the deployed tool
./setup.sh test
```

Once a port forward is active, hit the capabilities directly:

```bash
# Area statistics for a U.S. state
curl -X POST http://localhost:8365/api/capabilities/area_statistics \
  -H "Content-Type: application/json" \
  -d '{"location": "Texas"}'

# Global demographics for any country
curl -X POST http://localhost:8365/api/capabilities/global_demographics \
  -H "Content-Type: application/json" \
  -d '{"country": "India"}'
```

---

## Features

- **U.S. Area Statistics** - Population, income, housing, education, and employment for any U.S. state, county, or zip code
- **Multi-Area Comparison** - Compare up to 10 U.S. areas side by side
- **State Rankings** - Rank all 50 states (+ DC + PR) by population, median income, home value, rent, poverty rate, unemployment rate, or median age
- **Global Demographics** - Country-level data (population, life expectancy, literacy, urbanization, growth) for any country worldwide
- **Multi-Country Comparison** - Compare up to 10 countries side by side using World Bank data
- **Built-in Geographic Resolution** - Accepts state names/abbreviations, zip codes, `state:county` pairs (U.S.) and country names, ISO3 codes, or ISO2 codes (global)
- **No Key Required for World Bank Data** - Census key is optional; World Bank works key-free
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Area Statistics (`area_statistics`)

**Endpoint:** `/api/capabilities/area_statistics`

Gets comprehensive demographic and socioeconomic statistics for a U.S. geographic area from the Census Bureau's American Community Survey (5-Year Estimates).

**Request:**
```json
{
  "location": "Texas"
}
```

Accepted location formats:
- State name or abbreviation — `Texas`, `TX`, `california`, `CA`
- 5-digit zip code — `78701`
- State + county — `TX:Travis`, `CA:Los Angeles`, `NY:Kings`

**Response:**
```json
{
  "location": {
    "name": "Texas",
    "type": "state",
    "fips": "48"
  },
  "population": {
    "total": 29527941,
    "median_age": 35.5
  },
  "income": {
    "median_household": 73035
  },
  "housing": {
    "median_home_value": 238000,
    "median_rent": 1287,
    "total_units": 11502149,
    "vacancy_rate": 10.42
  },
  "education": {
    "bachelors_degree_pct": 21.83,
    "graduate_degree_pct": 11.45
  },
  "employment": {
    "unemployment_rate": 4.92,
    "labor_force": 15124882,
    "unemployed": 744419,
    "poverty_rate": 13.74
  },
  "data_year": "2023 (ACS 5-Year)",
  "source": "U.S. Census Bureau - American Community Survey 5-Year Estimates"
}
```

### 2. Compare Areas (`compare_areas`)

**Endpoint:** `/api/capabilities/compare_areas`

Compares demographic statistics across 2-10 U.S. geographic areas side by side. Areas that fail to resolve are silently skipped; if all fail, an error is returned.

**Request:**
```json
{
  "locations": "TX,CA,NY"
}
```

**Response:**
```json
{
  "areas": [
    { "location": { "name": "Texas", "type": "state", "fips": "48" }, "population": { "total": 29527941, "median_age": 35.5 }, "...": "..." },
    { "location": { "name": "California", "type": "state", "fips": "06" }, "population": { "total": 39103890, "median_age": 37.6 }, "...": "..." },
    { "location": { "name": "New York", "type": "state", "fips": "36" }, "population": { "total": 19571216, "median_age": 39.6 }, "...": "..." }
  ],
  "source": "U.S. Census Bureau - American Community Survey 5-Year Estimates"
}
```

### 3. Population Ranking (`population_ranking`)

**Endpoint:** `/api/capabilities/population_ranking`

Ranks U.S. states (plus DC and PR) by a single demographic metric.

**Request:**
```json
{
  "metric": "median_income",
  "order": "desc",
  "limit": 10
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `metric` | Yes | One of: `population`, `median_income`, `home_value`, `median_rent`, `poverty_rate`, `unemployment_rate`, `median_age` |
| `order` | No | `desc` (highest first, default) or `asc` (lowest first) |
| `limit` | No | Number of states to return (default 10, max 52) |

**Response:**
```json
{
  "metric": "median_income",
  "order": "desc",
  "rankings": [
    { "rank": 1, "state": "District of Columbia", "fips": "11", "value": 101722, "units": "dollars" },
    { "rank": 2, "state": "Maryland", "fips": "24", "value": 98678, "units": "dollars" },
    { "rank": 3, "state": "Massachusetts", "fips": "25", "value": 96505, "units": "dollars" }
  ],
  "data_year": "2023 (ACS 5-Year)",
  "source": "U.S. Census Bureau - American Community Survey 5-Year Estimates"
}
```

### 4. Global Demographics (`global_demographics`)

**Endpoint:** `/api/capabilities/global_demographics`

Gets country-level demographic data from the World Bank for any country worldwide. For U.S.-specific detailed data (housing, education, employment), use `area_statistics` instead.

**Request:**
```json
{
  "country": "India",
  "year": "2022"
}
```

`country` accepts a country name (`India`, `Brazil`, `Japan`), an ISO3 code (`IND`, `BRA`, `JPN`), or an ISO2 code (`IN`, `BR`, `JP`). Common aliases like `UK`, `USA`, `america`, `holland`, and `world` are also resolved. `year` is optional; if omitted the most recent non-null value per indicator is returned (World Bank data typically lags 1-2 years).

**Response:**
```json
{
  "country": "India",
  "country_code": "IND",
  "region": "South Asia",
  "income_level": "Lower middle income",
  "population": 1417173173,
  "life_expectancy": 70.19,
  "literacy_rate": 76.32,
  "urbanization_rate": 35.87,
  "population_growth": 0.79,
  "data_year": "2022",
  "source": "World Bank Open Data"
}
```

### 5. Compare Countries Demographics (`compare_countries_demographics`)

**Endpoint:** `/api/capabilities/compare_countries_demographics`

Compares 2-10 countries side by side using World Bank data. Countries that fail to resolve are silently skipped; if all fail, an error is returned.

**Request:**
```json
{
  "countries": "IND,CHN,USA",
  "year": "2022"
}
```

**Response:**
```json
{
  "countries": [
    { "country": "India", "country_code": "IND", "population": 1417173173, "life_expectancy": 70.19, "...": "..." },
    { "country": "China", "country_code": "CHN", "population": 1412175000, "life_expectancy": 78.21, "...": "..." },
    { "country": "United States", "country_code": "USA", "population": 333287557, "life_expectancy": 77.43, "...": "..." }
  ],
  "data_year": "2022",
  "source": "World Bank Open Data"
}
```

---

## Architecture

```
Demographics Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Routes U.S. queries to Census Bureau API (ACS 5-Year)
    +-- Routes global queries to World Bank Open Data API
    +-- Resolves states/counties/countries via built-in mappings
    +-- Returns standardized responses with data_year + source

Agents (Active)
    |
    +-- Discover demographics tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the demographics tool is automatically discovered by agents via Redis. You can query demographic data through natural language:

```bash
# Query through an orchestrating agent (e.g. travel-chat-agent on 8356)
curl -X POST http://localhost:8356/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "How does median household income in Texas compare to California and New York?",
    "ai_synthesis": true
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `CENSUS_API_KEY` | Census Bureau API key | - | No* |
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8365` | No |
| `NAMESPACE` | Kubernetes namespace (the supplied [k8-deployment.yaml](k8-deployment.yaml) does not set this; the framework relies on `TRUVAG3_NAMESPACE` instead) | _empty_ | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `APP_ENV` | Telemetry profile (`development`/`staging`/`production`) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

*Tool works without a Census key but is limited to 500 Census requests per day. World Bank capabilities are unaffected.

---

## API Rate Limits

### U.S. Census Bureau API

| Limit | Without key | With key |
|-------|-------------|----------|
| **Daily requests** | 500 per IP | Standard authenticated tier (effectively unrestricted for normal use) |

### World Bank Open Data API

| Limit | Value |
|-------|-------|
| **Rate limit** | No published per-client limit; subject to server-side fair-use enforcement |
| **Authentication** | None required |

The tool implements:
- Source attribution on every response (`source` + `data_year`)
- Graceful skipping in comparison endpoints — partial failures don't abort the whole call
- Sentinel-value handling for Census null markers (`-666666666`, `-999999999`, `null`)
- Traced HTTP client for all API calls (OpenTelemetry spans)
- Structured error logging with upstream HTTP status codes (so 429s and 5xx are easy to grep in logs)

---

## Project Structure

```
demographics-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── demographics_tool.go    # Tool definition, capability registration, FIPS mappings
├── census_client.go        # U.S. Census Bureau API client
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

First, confirm the tool pod and Redis are both running:

```bash
./setup.sh status
```

Then tail the tool logs and look for a Redis registration message at startup:

```bash
./setup.sh logs
```

If you see Redis connection errors, the in-cluster Redis isn't reachable. The deployment manifest hardcodes the in-cluster Redis URL (`redis://redis.truvag3-examples:6379`), so the fix is to bring up the infrastructure stack from any agent example (e.g. `cd ../travel-chat-agent && ./setup.sh infra`), then re-roll the tool:

```bash
./setup.sh rollout
```

**2. Census API returns 429 (rate limit exceeded)**

Tail the logs and grep for the upstream status:

```bash
./setup.sh logs | grep -i "429\|rate"
```

Common cause: no `CENSUS_API_KEY`, so you hit the 500 req/day per-IP cap. Get a free key at https://api.census.gov/data/key_signup.html, add it to `.env` as `CENSUS_API_KEY=...`, then:

```bash
./setup.sh rollout
```

**3. "unrecognized location format" error**

The `location` field must match one of these patterns:
- State name or abbreviation: `Texas`, `TX`
- 5-digit zip code: `78701`
- State:county pair: `TX:Travis`

County names not in the built-in popular-counties map will fall back to a Census API lookup. If even that fails, the county name may be misspelled or not exist in that state.

**4. Country not found in `global_demographics`**

```bash
# Common causes:
# - Misspelled country name (e.g., "Indea" instead of "India")
# - Country not in the built-in name/ISO2 alias maps in worldbank_client.go
#   Workaround: pass the ISO3 code directly (e.g., "ETH" for Ethiopia, "VNM" for Vietnam)
# - World Bank does not publish data for the requested entity (some micro-states,
#   territories, or historical aggregates return empty data)
```

The resolver passes unrecognized 3-letter inputs through to the World Bank API unchanged, so any valid ISO3 code — including aggregates like `WLD` (World), `EUU` (European Union), or income-group codes — will work even if not in the alias map.

**5. Some indicators missing in `global_demographics` response**

The response uses pointer fields for indicators (`population`, `life_expectancy`, etc.), so missing fields are omitted entirely. World Bank coverage varies by country and year — particularly `literacy_rate`, which is sparsely reported.

**6. Docker build fails**

```bash
# Ensure Docker is running
docker info
```

**7. Kind cluster not found**

Use `./setup.sh cluster` to create the Kind cluster with the right port mappings, or `./setup.sh full-deploy` to also bring up infrastructure and deploy the tool in one shot.

### Useful Commands

Lifecycle is driven entirely through [`setup.sh`](setup.sh):

```bash
# View tool logs
./setup.sh logs

# Check deployment status
./setup.sh status

# Port forward the tool only
./setup.sh forward

# Port forward tool + Grafana + Prometheus + Jaeger
./setup.sh forward-all

# Restart the deployment after editing .env
./setup.sh rollout

# Rebuild image and redeploy with fresh dependencies
./setup.sh rebuild

# Run the built-in smoke tests
./setup.sh test
```

Once a port forward is active, hit the capabilities directly:

```bash
# Area statistics (state)
curl -X POST http://localhost:8365/api/capabilities/area_statistics \
  -H "Content-Type: application/json" \
  -d '{"location": "TX"}'

# Area statistics (county)
curl -X POST http://localhost:8365/api/capabilities/area_statistics \
  -H "Content-Type: application/json" \
  -d '{"location": "TX:Travis"}'

# Area statistics (zip)
curl -X POST http://localhost:8365/api/capabilities/area_statistics \
  -H "Content-Type: application/json" \
  -d '{"location": "78701"}'

# Population ranking
curl -X POST http://localhost:8365/api/capabilities/population_ranking \
  -H "Content-Type: application/json" \
  -d '{"metric": "median_income", "order": "desc", "limit": 5}'

# Global demographics
curl -X POST http://localhost:8365/api/capabilities/global_demographics \
  -H "Content-Type: application/json" \
  -d '{"country": "Japan"}'

# Country comparison
curl -X POST http://localhost:8365/api/capabilities/compare_countries_demographics \
  -H "Content-Type: application/json" \
  -d '{"countries": "IND,CHN,USA"}'
```

---

## Development

### Local Development

Edit `.env` with your local `REDIS_URL` (and optionally `CENSUS_API_KEY`), then:

```bash
# Build the Go binary
./setup.sh build

# Build and run the tool locally (reads .env automatically)
./setup.sh run
```

`./setup.sh run` invokes `./setup.sh build` first and then runs the binary against the `REDIS_URL` from `.env`. Make sure Redis is reachable from your machine before running.

### Adding New Capabilities

1. Add request/response types in `demographics_tool.go`
2. Register the capability in `registerCapabilities()`
3. Implement the handler in `handlers.go`
4. Add a client method in `census_client.go` or `worldbank_client.go` if needed

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can use this tool
- [agent-with-orchestration](../agent-with-orchestration/) - Orchestration example
- [country-info-tool](../country-info-tool/) - Country metadata (capital, currency, languages) — complements `global_demographics`
- [geocoding-tool](../geocoding-tool/) - Coordinate lookup for places
- [world-health-tool](../world-health-tool/) - WHO health indicators by country

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
