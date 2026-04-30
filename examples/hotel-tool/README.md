# Hotel Tool

A TruvaG3 tool that provides hotel search and booking capabilities using the [Amadeus Self-Service API](https://developers.amadeus.com/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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
# Expected: go version go1.25.x darwin/arm64 (or darwin/amd64)
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
# Expected: go version go1.25.x windows/amd64
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Manual installation (recommended for latest version):**
```bash
curl -LO https://go.dev/dl/go1.25.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

**Verify installation:**
```bash
go version
# Expected: go version go1.25.x linux/amd64
```

</details>

---

#### 5. Amadeus API Key

The Amadeus API key is **required** for hotel data. The free test environment provides realistic data for development.

1. Visit [developers.amadeus.com](https://developers.amadeus.com/)
2. Sign up for a free account
3. Create a new app in your dashboard
4. Copy your **API Key** (Client ID) and **API Secret** (Client Secret)

**Free test tier includes:**
- Hotel search and offers
- Hotel reference data
- Hotel sentiment analysis
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

**Edit `.env`** with your Amadeus credentials:

```bash
nano .env    # or: code .env / vim .env
```

- `AMADEUS_CLIENT_ID=your-client-id` (Get from [developers.amadeus.com](https://developers.amadeus.com/))
- `AMADEUS_CLIENT_SECRET=your-client-secret`

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

The hotel tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/hotel-tool

# Build Docker image
docker build -t hotel-tool:latest .

# Load into Kind
kind load docker-image hotel-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=hotel-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/hotel-service 8343:80

# Test hotel search
curl -X POST http://localhost:8343/api/capabilities/search_hotels \
  -H "Content-Type: application/json" \
  -d '{"city_code": "PAR", "check_in": "2026-04-15", "check_out": "2026-04-18"}'
```

---

## Features

- **Hotel Search** - Search for available hotels with real-time pricing in any city
- **Hotel Listing** - Browse all known hotels in a city with coordinates and ratings
- **Hotel Ratings** - Get guest sentiment analysis from review data
- **OAuth2 Token Management** - Automatic Amadeus token refresh with thread-safe caching
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Search Hotels (`search_hotels`)

**Endpoint:** `/api/capabilities/search_hotels`

Searches for available hotels with real-time pricing in a city.

**Request:**
```json
{
  "city_code": "PAR",
  "check_in": "2026-04-15",
  "check_out": "2026-04-18",
  "adults": 2,
  "rooms": 1,
  "max_results": 5,
  "currency": "EUR"
}
```

**Response:**
```json
{
  "city_code": "PAR",
  "check_in": "2026-04-15",
  "check_out": "2026-04-18",
  "hotels": [
    {
      "hotel_id": "MSPARIDC",
      "name": "Mercure Paris Centre",
      "rating": "4",
      "latitude": 48.8566,
      "longitude": 2.3522,
      "distance": "0.5 km",
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
  "source": "Amadeus API (test)"
}
```

### 2. List Hotels by City (`list_hotels_by_city`)

**Endpoint:** `/api/capabilities/list_hotels_by_city`

Lists all known hotels in a city for browsing and discovery.

**Request:**
```json
{
  "city_code": "NYC",
  "radius": 5,
  "ratings": "4,5"
}
```

**Response:**
```json
{
  "city_code": "NYC",
  "hotels": [
    {
      "hotel_id": "MCLONGHM",
      "name": "The Langham New York",
      "chain_code": "MC",
      "latitude": 40.7128,
      "longitude": -74.006,
      "distance": "1.2 km",
      "country_code": "US"
    }
  ],
  "source": "Amadeus API (test)"
}
```

### 3. Hotel Ratings (`hotel_ratings`)

**Endpoint:** `/api/capabilities/hotel_ratings`

Gets guest sentiment analysis for hotels from review data.

**Request:**
```json
{
  "hotel_ids": "MCLONGHM,MSPARIDC"
}
```

**Response:**
```json
{
  "hotels": [
    {
      "hotel_id": "MCLONGHM",
      "overall_rating": 88.5,
      "number_of_reviews": 1250,
      "number_of_ratings": 980,
      "sentiments": {
        "location": 95,
        "comfort": 88,
        "staff": 92,
        "value": 78,
        "cleanliness": 90
      }
    }
  ],
  "source": "Amadeus API (test)"
}
```

---

## Architecture

```
Hotel Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Authenticates via Amadeus OAuth2
    +-- Calls Amadeus Self-Service API
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
# Query through the travel chat agent
curl -X POST http://localhost:8356/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "Find hotels in Paris for 3 nights next month",
    "ai_synthesis": true
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `AMADEUS_CLIENT_ID` | Amadeus API client ID | - | Yes |
| `AMADEUS_CLIENT_SECRET` | Amadeus API client secret | - | Yes |
| `AMADEUS_ENV` | Amadeus environment (test\|production) | `test` | No |
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8343` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

---

## API Rate Limits

Free tier limits (Amadeus Test Environment):

| Limit | Value |
|-------|-------|
| **Hotel Offers** | Generous test limits |
| **Hotel Reference Data** | Generous test limits |
| **Hotel Sentiment** | Generous test limits |

The tool implements:
- OAuth2 token caching with automatic refresh
- Two-step hotel search (list hotels by city, then get offers)
- Structured error logging for rate limit tracking
- Graceful error responses on API failures

---

## Project Structure

```
hotel-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── hotel_tool.go           # Tool definition, capability registration
├── amadeus_auth.go         # OAuth2 token manager (mutex + expiry caching)
├── amadeus_client.go       # Amadeus API client (3 endpoints)
├── handlers.go             # HTTP handlers for each capability
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

Ensure the tool is registered with Redis:
```bash
# Check Redis connection
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*"

# Should show: truvag3:service:hotel-service
```

**2. OAuth2 authentication errors**

```bash
# Check logs for auth issues
kubectl logs -n truvag3-examples -l app=hotel-tool | grep -i "auth\|token"

# Common issues:
# - Invalid credentials: Check AMADEUS_CLIENT_ID and AMADEUS_CLIENT_SECRET
# - Wrong environment: Ensure AMADEUS_ENV matches your credentials (test vs production)
```

**3. API errors or empty results**

```bash
# Check logs for API issues
kubectl logs -n truvag3-examples -l app=hotel-tool | grep -i "api\|error"

# Common issues:
# - Invalid city code: Use IATA city codes (PAR, NYC, LON), not airport codes
# - Past dates: Ensure check_in is in the future
# - Rate limit: Wait or check your Amadeus dashboard
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

# Create a new cluster if none exists
kind create cluster --name truvag3-demo
```

### Useful Commands

```bash
# View tool logs
kubectl logs -n truvag3-examples -l app=hotel-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=hotel-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/hotel-service 8343:80

# Test hotel search
curl -X POST http://localhost:8343/api/capabilities/search_hotels \
  -H "Content-Type: application/json" \
  -d '{"city_code": "PAR", "check_in": "2026-04-15", "check_out": "2026-04-18"}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export AMADEUS_CLIENT_ID="your-client-id"
export AMADEUS_CLIENT_SECRET="your-client-secret"
export AMADEUS_ENV="test"
export REDIS_URL="redis://localhost:6379"
export PORT=8343

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `hotel_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add Amadeus client method in `amadeus_client.go` if needed

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can use this tool
- [flight-tool](../flight-tool/) - Flight search tool (also uses Amadeus)
- [places-tool](../places-tool/) - Local places and restaurants search
- [travel-advisory-tool](../travel-advisory-tool/) - Travel safety advisories
- [stock-market-tool](../stock-market-tool/) - Stock market data tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
