# Places Tool

A TruvaG3 tool that provides local place search capabilities using [Foursquare Places API](https://location.foursquare.com/developer/) and [Geoapify Places API](https://www.geoapify.com/places-api). This dual-provider tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides local place search capabilities that agents can discover and use. It supports two providers - Foursquare (primary) and Geoapify (fallback) - selectable per-request. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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
curl -LO https://go.dev/dl/go1.26.4.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.4.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
go version
```

</details>

---

#### 5. API Keys

At least one provider API key is required. You can configure both for dual-provider support.

**Foursquare (Primary Provider):**

1. Visit [foursquare.com/developers](https://foursquare.com/developers/signup)
2. Sign up for a free account and create a new project
3. Go to **Settings → Service API Keys** and click **Generate Service API Key**
4. Copy the key immediately (it is only shown once)

> **Important:** The key is a **Service API Key** (alphanumeric string like `34H3JNNI...`). This is different from the legacy `fsq3...` API keys. See [Foursquare API Details](#foursquare-api-details) below for how authentication works.

**Free tier:** 10,000 regular calls/month

**Geoapify (Fallback Provider):**

1. Visit [myprojects.geoapify.com](https://myprojects.geoapify.com/)
2. Sign up for a free account
3. Create a new project
4. Copy your API key

**Free tier:** ~90,000 calls/month (3,000/day)

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

The fastest way to get the places tool running:

```bash
cd examples/places-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**Edit `.env`** with your API keys (at least one provider required):

```bash
nano .env    # or: code .env / vim .env
```

- `FOURSQUARE_API_KEY=your-key` (Get from [foursquare.com/developers](https://foursquare.com/developers/signup))
- `GEOAPIFY_API_KEY=your-key` (Get from [myprojects.geoapify.com](https://myprojects.geoapify.com/register))

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
| **Places API** | http://localhost:8344 | Places search API |

### Step-by-Step Deployment

#### Step 1: Ensure Infrastructure is Running

The places tool requires Redis for service discovery. If you haven't already set up infrastructure, run these from this directory:

```bash
cd examples/places-tool

./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

#### Step 2: Build and Deploy

Configure your credentials in `.env` (see Quick Start above), then `./setup.sh deploy` creates the Kubernetes Secret from `.env` automatically.

```bash
cd examples/places-tool

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
# Port forward the tool service to localhost:8344
./setup.sh forward

# Or run a built-in smoke test against the deployed tool
./setup.sh test
```

In a second terminal (while `./setup.sh forward` is running):

```bash
curl -X POST http://localhost:8344/api/capabilities/search_places \
  -H "Content-Type: application/json" \
  -d '{"query": "sushi restaurants", "lat": 35.6762, "lon": 139.6503}'
```

---

## Features

- **Place Search** - Search for restaurants, attractions, cafes, and more near any location
- **Place Details** - Get detailed information about a specific place
- **Nearby Places** - Find places by category near specific coordinates
- **Dual Provider** - Foursquare (primary) and Geoapify (fallback), selectable per-request
- **Normalized Results** - Consistent response format across both providers
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Search Places (`search_places`)

**Endpoint:** `/api/capabilities/search_places`

Searches for restaurants, attractions, cafes, nightlife, and other places near a location.

**Request:**
```json
{
  "query": "sushi restaurants",
  "lat": 35.6762,
  "lon": 139.6503,
  "radius": 1000,
  "limit": 10,
  "provider": "foursquare"
}
```

**Response:**
```json
{
  "query": "sushi restaurants",
  "places": [
    {
      "id": "4b0587daf964a520baa222e3",
      "name": "Sukiyabashi Jiro",
      "address": "4-2-15 Ginza, Chuo-ku, Tokyo",
      "lat": 35.6721,
      "lon": 139.7636,
      "categories": ["Sushi Restaurant", "Japanese Restaurant"],
      "distance_meters": 450,
      "provider": "foursquare"
    }
  ],
  "provider": "foursquare",
  "source": "Foursquare Places API"
}
```

### 2. Place Details (`place_details`)

**Endpoint:** `/api/capabilities/place_details`

Gets detailed information about a specific place by its provider ID.

**Request:**
```json
{
  "place_id": "4b0587daf964a520baa222e3",
  "provider": "foursquare"
}
```

**Response:**
```json
{
  "id": "4b0587daf964a520baa222e3",
  "name": "Sukiyabashi Jiro",
  "address": "4-2-15 Ginza, Chuo-ku, Tokyo 104-0061",
  "lat": 35.6721,
  "lon": 139.7636,
  "categories": ["Sushi Restaurant"],
  "phone": "+81-3-3535-3600",
  "website": "https://www.sushi-jiro.jp",
  "hours": "Mon-Sat 11:30-14:00, 17:00-20:30",
  "rating": 9.4,
  "provider": "foursquare",
  "source": "Foursquare Places API"
}
```

### 3. Nearby Places (`nearby_places`)

**Endpoint:** `/api/capabilities/nearby_places`

Finds places by category near specific coordinates.

**Request:**
```json
{
  "lat": 48.8584,
  "lon": 2.2945,
  "categories": "restaurant,cafe",
  "radius": 500,
  "limit": 5
}
```

**Response:**
```json
{
  "lat": 48.8584,
  "lon": 2.2945,
  "places": [
    {
      "id": "5a6e8c2d1f0e4b001c3a7890",
      "name": "Le Jules Verne",
      "address": "Avenue Gustave Eiffel, 75007 Paris",
      "lat": 48.8583,
      "lon": 2.2944,
      "categories": ["French Restaurant"],
      "distance_meters": 15,
      "provider": "foursquare"
    }
  ],
  "provider": "foursquare",
  "source": "Foursquare Places API"
}
```

---

## Architecture

```
Places Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Routes to Foursquare or Geoapify
    +-- Normalizes responses across providers
    +-- Returns standardized PlaceResult format

Provider Routing:
    Request → provider field?
    ├── "foursquare" → Foursquare Places API (places-api.foursquare.com)
    ├── "geoapify"   → Geoapify Places v2 API
    └── (empty)      → Default provider (configurable)
```

### Integration with Agents

Once deployed, the places tool is automatically discovered by agents via Redis. You can query places through natural language:

```bash
# Query through the travel chat agent
curl -X POST http://localhost:8356/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "What restaurants are near the Eiffel Tower?",
    "ai_synthesis": true
  }'
```

---

## Foursquare API Details

The Foursquare Places API has migrated to a new infrastructure (as of 2025). This tool uses the **new endpoint** — not the legacy `api.foursquare.com/v3` which is no longer supported.

### New vs Legacy Endpoint

| | Legacy (deprecated) | Current |
|---|---|---|
| **Base URL** | `https://api.foursquare.com/v3` | `https://places-api.foursquare.com` |
| **Auth header** | `Authorization: <api_key>` | `Authorization: Bearer <service_api_key>` |
| **Version** | In URL path (`/v3/`) | Header: `X-Places-Api-Version: 2025-06-17` |
| **Place ID field** | `fsq_id` | `fsq_place_id` |
| **Coordinates** | Nested in `location` object | Top-level `latitude`/`longitude` |

### Required Headers

Every request to the Foursquare API must include these headers:

```
Authorization: Bearer <YOUR_SERVICE_API_KEY>
Accept: application/json
X-Places-Api-Version: 2025-06-17
```

### Sample Direct curl Request

You can test your Foursquare Service API Key directly (outside of the places-tool) to verify it works:

```bash
# Search for coffee shops near Manhattan
curl -s -X GET \
  "https://places-api.foursquare.com/places/search?query=coffee&ll=40.7128,-74.0060&radius=1000&limit=3" \
  -H "Authorization: Bearer YOUR_SERVICE_API_KEY" \
  -H "Accept: application/json" \
  -H "X-Places-Api-Version: 2025-06-17" | python3 -m json.tool
```

Expected response:
```json
{
  "results": [
    {
      "fsq_place_id": "4b475390f964a520f12e26e3",
      "name": "Mary's Coffee Shop",
      "latitude": 40.7127,
      "longitude": -74.0059,
      "location": {
        "formatted_address": "25-15 Queens Plz N, Long Island City, NY 11101"
      },
      "categories": [
        { "fsq_category_id": "4bf58dd8d48988d1e0931735", "name": "Coffee Shop" }
      ],
      "distance": 6
    }
  ]
}
```

### Migration Reference

For full details on the API migration, see the [Foursquare Migration Guide](https://docs.foursquare.com/fsq-developers-places/reference/migration-guide).

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `FOURSQUARE_API_KEY` | Foursquare Places API key | - | No* |
| `GEOAPIFY_API_KEY` | Geoapify Places API key | - | No* |
| `PLACES_DEFAULT_PROVIDER` | Default provider (foursquare\|geoapify) | `foursquare` | No |
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8344` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

*At least one provider API key is required.

---

## API Rate Limits

| Provider | Free Tier |
|----------|-----------|
| **Foursquare** | 10,000 regular calls/month |
| **Geoapify** | ~90,000 calls/month (3,000/day) |

The tool implements:
- Provider routing for load distribution
- Structured error logging for rate limit tracking
- Graceful error responses on API failures

---

## Project Structure

```
places-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── places_tool.go          # Tool definition, capability registration
├── foursquare_client.go    # Foursquare Places API client (places-api.foursquare.com)
├── geoapify_client.go      # Geoapify Places v2 API client
├── handlers.go             # HTTP handlers with provider routing
├── go.mod                  # Go module definition
├── Dockerfile              # Standalone container image
├── Dockerfile.workspace    # Development build from workspace root
├── k8-deployment.yaml      # Kubernetes manifests
├── setup.sh                # Full lifecycle management script
└── README.md               # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"
# Should show: truvag3:service:places-service
```

**2. API key errors**

```bash
# Check logs
./setup.sh logs | grep -i "api\|key\|auth"

# Common issues:
# - "Invalid request token": Ensure you're using a Service API Key (not a legacy fsq3... key)
# - Missing API key for the selected provider
# - Invalid API key: Re-generate from Settings → Service API Keys in the developer console
# - Wrong provider: Ensure provider field matches configured keys
```

**3. No results returned**

```bash
# Common issues:
# - Coordinates outside valid range (-90 to 90 lat, -180 to 180 lon)
# - Query too specific: Try broader search terms
# - Radius too small: Increase radius parameter
```

**4. Docker build fails**

```bash
docker info
# Ensure Docker is running
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

# Port forward the tool to localhost:8344
./setup.sh forward

# Port forward tool + monitoring dashboards (Grafana, Prometheus, Jaeger)
./setup.sh forward-all

# Restart the deployment (e.g., to pick up new API keys from .env)
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
# Test place search (Foursquare)
curl -X POST http://localhost:8344/api/capabilities/search_places \
  -H "Content-Type: application/json" \
  -d '{"query": "sushi restaurants", "lat": 35.6762, "lon": 139.6503}'

# Test place search (Geoapify)
curl -X POST http://localhost:8344/api/capabilities/search_places \
  -H "Content-Type: application/json" \
  -d '{"query": "museums", "lat": 48.8566, "lon": 2.3522, "provider": "geoapify"}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export FOURSQUARE_API_KEY="your-foursquare-key"
export GEOAPIFY_API_KEY="your-geoapify-key"
export PLACES_DEFAULT_PROVIDER="foursquare"
export REDIS_URL="redis://localhost:6379"
export PORT=8344

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `places_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler with provider routing in `handlers.go`
4. Add methods to both `foursquare_client.go` and `geoapify_client.go`

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can use this tool
- [flight-tool](../flight-tool/) - Flight search tool
- [hotel-tool](../hotel-tool/) - Hotel search tool
- [travel-advisory-tool](../travel-advisory-tool/) - Travel safety advisories
- [geocoding-tool](../geocoding-tool/) - Geocoding and location resolution

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
