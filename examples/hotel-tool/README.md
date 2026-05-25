# Hotel Tool

A TruvaG3 tool that provides hotel search and review-sentiment capabilities using the [LiteAPI](https://www.liteapi.travel/) hotel data API. This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

> **Note:** LiteAPI takes **ISO country code + city name** (e.g., `FR` + `Paris`) for hotel search, not IATA city codes. Hotel IDs returned by the search are LiteAPI internal IDs.

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

This tool provides hotel search capabilities that agents can discover and use. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

#### 5. LiteAPI Key

A LiteAPI sandbox key is **required** for hotel data. The free sandbox tier hits real hotel inventory and is sufficient for examples and agent demos.

1. Sign up at [dashboard.liteapi.travel](https://dashboard.liteapi.travel)
2. In the dashboard, copy your **Sandbox API Key** (sandbox keys are not prefixed; production keys are prefixed with `prod_` and require a paid plan)

The key authenticates a single header (`X-API-Key`) — no OAuth flow, no client-id/secret pair.

**Free sandbox tier includes:**
- ~1,000 requests/month
- Real hotel inventory (sandbox pricing, not live booking quotes)
- No credit card required

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

The fastest way to get the hotel tool running:

```bash
cd examples/hotel-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**Edit `.env`** with your LiteAPI key:

```bash
nano .env    # or: code .env / vim .env
```

- `LITEAPI_KEY=your-sandbox-key` (Get from [dashboard.liteapi.travel](https://dashboard.liteapi.travel))

After configuring your credentials, continue with deployment:

```bash
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
| **Hotel API** | http://localhost:8343 | Hotel search API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The hotel tool requires Redis for service discovery. If you haven't already set up infrastructure, run these from this directory:

```bash
cd examples/hotel-tool

./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

#### Step 2: Build and Deploy

`setup.sh` handles the Docker build, Kind image load, namespace + Secret creation, and manifest apply. Use these subcommands instead of raw `kubectl`:

```bash
cd examples/hotel-tool

# Build the Docker image only (does not deploy)
./setup.sh docker-build

# Full deploy: build + load into Kind + create namespace + Secret from .env + apply manifest
./setup.sh deploy

# Verify deployment
./setup.sh status
```

> **Tip:** If you don't already have a cluster and infrastructure, `./setup.sh full-deploy` does everything from scratch in one shot — cluster, monitoring, tool, and port forwards.

#### Step 3: Test the Tool

```bash
# Port forward the tool service to localhost:8343
./setup.sh forward

# Or run a built-in smoke test against the deployed tool
./setup.sh test
```

In a second terminal (while `./setup.sh forward` is running):

```bash
# Test hotel search (LiteAPI uses city_name + country_code, not IATA codes)
curl -X POST http://localhost:8343/api/capabilities/search_hotels \
  -H "Content-Type: application/json" \
  -d '{"city_name": "Paris", "country_code": "FR", "check_in": "2026-04-15", "check_out": "2026-04-18"}'
```

---

## Features

- **Hotel Search** - Available hotels with rates in a city (by ISO country + city name)
- **Hotel Listing** - Browse all known hotels in a city with coordinates and chain info
- **Hotel Ratings** - Aggregate review sentiment for a single hotel ID
- **Single-Key Auth** - One `X-API-Key` header; no OAuth flow or token refresh
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Search Hotels (`search_hotels`)

**Endpoint:** `/api/capabilities/search_hotels`

Searches for available hotels with rates in a city.

**Request:**
```json
{
  "city_name": "Paris",
  "country_code": "FR",
  "check_in": "2026-04-15",
  "check_out": "2026-04-18",
  "adults": 2,
  "max_results": 5,
  "currency": "EUR",
  "guest_nationality": "US"
}
```

Required: `city_name`, `country_code` (ISO-2), `check_in`, `check_out` (`YYYY-MM-DD`).
Optional: `adults` (default 2), `max_results` (default 10), `currency` (ISO 4217, default `USD`), `guest_nationality` (ISO-2, default `US`).

**Response:**
```json
{
  "city_name": "Paris",
  "country_code": "FR",
  "check_in": "2026-04-15",
  "check_out": "2026-04-18",
  "hotels": [
    {
      "hotel_id": "lp1a2b3",
      "name": "Mercure Paris Centre",
      "rating": "4",
      "latitude": 48.8566,
      "longitude": 2.3522,
      "rooms": [
        {
          "type": "STANDARD",
          "description": "Standard Room with City View",
          "price": "185.00",
          "currency": "EUR",
          "board_type": "ROOM_ONLY"
        }
      ]
    }
  ],
  "source": "LiteAPI"
}
```

### 2. List Hotels by City (`list_hotels_by_city`)

**Endpoint:** `/api/capabilities/list_hotels_by_city`

Lists known hotels in a city for browsing and discovery (no live pricing).

**Request:**
```json
{
  "city_name": "New York",
  "country_code": "US",
  "limit": 25
}
```

Required: `city_name`, `country_code` (ISO-2).
Optional: `limit` (max hotels returned).

**Response:**
```json
{
  "city_name": "New York",
  "country_code": "US",
  "hotels": [
    {
      "hotel_id": "lp9x8y7",
      "name": "The Langham New York",
      "chain_code": "LH",
      "latitude": 40.7128,
      "longitude": -74.006,
      "country_code": "US"
    }
  ],
  "source": "LiteAPI"
}
```

### 3. Hotel Ratings (`hotel_ratings`)

**Endpoint:** `/api/capabilities/hotel_ratings`

Returns aggregate review sentiment for a single hotel ID. LiteAPI accepts one hotel ID per call (not a comma-separated list).

**Request:**
```json
{
  "hotel_id": "lp9x8y7"
}
```

**Response:**
```json
{
  "hotels": [
    {
      "hotel_id": "lp9x8y7",
      "overall_rating": 8.85,
      "number_of_reviews": 32,
      "number_of_ratings": 1250,
      "sentiments": {
        "average_score": 8.85
      }
    }
  ],
  "source": "LiteAPI"
}
```

`overall_rating` is a 0–10 average. `number_of_reviews` is what came back in this call; `number_of_ratings` is the total on file for the hotel.

---

## Architecture

```
Hotel Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Sends X-API-Key header with each request
    +-- Calls LiteAPI v3.0 hotel + review endpoints
    +-- Returns standardized responses

Agents (Active)
    |
    +-- Discover hotel tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the hotel tool is automatically discovered by agents via Redis. You can query hotel data through natural language:

```bash
# Query through the travel-chat-agent (streaming SSE response).
# The agent's LLM picks tools dynamically based on the query.
curl -N -X POST http://travel-chat-agent.localhost/chat/stream \
  -H "Content-Type: application/json" \
  -d '{"message": "Find hotels in Paris for 3 nights next month"}'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `LITEAPI_KEY` | LiteAPI key (single-header `X-API-Key`; sandbox keys unprefixed, production keys prefixed with `prod_`) | - | Yes |
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8343` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

---

## API Rate Limits

LiteAPI's free sandbox tier allows ~1,000 requests/month against real hotel inventory. Production keys (prefixed `prod_`) require a paid plan with separate rate-limit terms. See [liteapi.travel](https://www.liteapi.travel/) for the current pricing and limits.

The tool implements:
- Two-step hotel search (`/data/hotels` to resolve hotel IDs, then `/hotels/rates` for rates)
- Structured error logging for API failures
- Graceful error responses on upstream errors and timeouts

---

## Project Structure

```
hotel-tool/
├── main.go                  # Entry point, framework setup, telemetry init
├── hotel_tool.go            # Tool definition, capability registration
├── liteapi_client.go        # LiteAPI v3.0 client (hotels + rates + reviews)
├── handlers.go              # HTTP handlers for each capability
├── go.mod                   # Go module definition
├── Dockerfile               # Standalone container image
├── Dockerfile.workspace     # Development build from workspace root
├── k8-deployment.yaml       # Kubernetes manifests
├── setup.sh                 # Full lifecycle management script
└── README.md                # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

Ensure the tool is registered with Redis:
```bash
# Check Redis connection
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"

# Should show a key containing "hotel-tool"
```

**2. Authentication / 401 errors**

```bash
# Stream logs and grep for key issues
./setup.sh logs | grep -i "key\|401\|unauthorized"

# Common issues:
# - Empty LITEAPI_KEY: confirm your .env has the key set, then re-run ./setup.sh deploy
# - Wrong key: re-copy from https://dashboard.liteapi.travel
# - Production key (prod_*) used without a paid plan: switch back to a sandbox key
```

**3. API errors or empty results**

```bash
# Stream logs and grep for API issues
./setup.sh logs | grep -i "api\|error"

# Common issues:
# - Wrong city/country: LiteAPI takes ISO country code + city name
#   (e.g. country_code="FR", city_name="Paris") — not IATA codes like "PAR"
# - Past dates: Ensure check_in is in the future
# - Empty sandbox result: niche city pairs may have no sandbox inventory.
#   Try a major city (e.g. Paris/FR or London/GB) to confirm the tool works.
```

**4. Docker build fails**

```bash
# Ensure Docker is running
docker info
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

# Port forward the tool to localhost:8343
./setup.sh forward

# Port forward tool + monitoring dashboards (Grafana, Prometheus, Jaeger)
./setup.sh forward-all

# Restart the deployment (e.g., to pick up a new Secret from .env)
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
# Hotel search
curl -X POST http://localhost:8343/api/capabilities/search_hotels \
  -H "Content-Type: application/json" \
  -d '{"city_name": "Paris", "country_code": "FR", "check_in": "2026-04-15", "check_out": "2026-04-18"}'

# List hotels by city
curl -X POST http://localhost:8343/api/capabilities/list_hotels_by_city \
  -H "Content-Type: application/json" \
  -d '{"city_name": "New York", "country_code": "US", "limit": 25}'

# Hotel ratings (use a hotel_id from a prior search response)
curl -X POST http://localhost:8343/api/capabilities/hotel_ratings \
  -H "Content-Type: application/json" \
  -d '{"hotel_id": "lp9x8y7"}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export LITEAPI_KEY="your-sandbox-key"
export REDIS_URL="redis://localhost:6379"
export PORT=8343

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `hotel_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add a LiteAPI client method in `liteapi_client.go` if needed

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can use this tool
- [flight-tool](../flight-tool/) - Flight search tool (uses Travelpayouts)
- [places-tool](../places-tool/) - Local places and restaurants search
- [travel-advisory-tool](../travel-advisory-tool/) - Travel safety advisories
- [stock-market-tool](../stock-market-tool/) - Stock market data tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
