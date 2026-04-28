# Flight Tool

A Truva-G3 tool that provides flight search capabilities using the [Amadeus Self-Service API](https://developers.amadeus.com/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides flight search capabilities that agents can discover and use. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

**Using MacPorts:**
```bash
sudo port selfupdate && sudo port install kind
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

**Using Scoop:**
```powershell
scoop bucket add main
scoop install main/kind
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

**Using Go (if Go is installed):**
```bash
go install sigs.k8s.io/kind@v0.31.0
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

**Manual binary installation (Apple Silicon):**
```bash
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/darwin/arm64/kubectl"
chmod +x ./kubectl
sudo mv ./kubectl /usr/local/bin/kubectl
```

**Manual binary installation (Intel):**
```bash
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/darwin/amd64/kubectl"
chmod +x ./kubectl
sudo mv ./kubectl /usr/local/bin/kubectl
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

**Using Winget:**
```powershell
winget install -e --id Kubernetes.kubectl
```

**Manual binary installation:**
```powershell
# Download kubectl
curl.exe -LO "https://dl.k8s.io/release/v1.31.0/bin/windows/amd64/kubectl.exe"

# Move to a directory in your PATH
Move-Item .\kubectl.exe C:\Windows\System32\kubectl.exe
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
# Add Kubernetes apt repository
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates curl gnupg

curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.31/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
sudo chmod 644 /etc/apt/keyrings/kubernetes-apt-keyring.gpg

echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.31/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list
sudo chmod 644 /etc/apt/sources.list.d/kubernetes.list

sudo apt-get update
sudo apt-get install -y kubectl
```

**Using snap:**
```bash
sudo snap install kubectl --classic
```

**Manual binary installation:**
```bash
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x ./kubectl
sudo mv ./kubectl /usr/local/bin/kubectl
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

**Manual installation:**
1. Download the macOS installer from [go.dev/dl](https://go.dev/dl/)
2. Open the downloaded `.pkg` file
3. Follow the installation prompts

**Verify installation:**
```bash
go version
# Expected: go version go1.25.x darwin/arm64 (or darwin/amd64)
```

</details>

<details>
<summary><strong>Windows Installation</strong></summary>

**Using the MSI installer (recommended):**
1. Download the Windows installer from [go.dev/dl](https://go.dev/dl/)
2. Run the `.msi` installer
3. Follow the installation wizard
4. The installer sets PATH automatically

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
# Download Go (replace version as needed)
curl -LO https://go.dev/dl/go1.25.linux-amd64.tar.gz

# Remove any previous installation and extract
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.linux-amd64.tar.gz

# Add to PATH (add to ~/.bashrc or ~/.profile for persistence)
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

The Amadeus API key is **required** for flight data. The free test environment provides realistic data for development.

1. Visit [developers.amadeus.com](https://developers.amadeus.com/)
2. Sign up for a free account
3. Create a new app in your dashboard
4. Copy your **API Key** (Client ID) and **API Secret** (Client Secret)

**Free test tier includes:**
- ~2,000 flight-offers calls/month
- ~3,000 pricing calls/month
- Test environment with realistic data
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

The fastest way to get the flight tool running:

```bash
cd examples/flight-tool

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
| **Flight API** | http://localhost:8342 | Flight search API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The flight tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/flight-tool

# Build Docker image
docker build -t flight-tool:latest .

# Load into Kind
kind load docker-image flight-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=flight-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/flight-service 8342:80

# Test flight search
curl -X POST http://localhost:8342/api/capabilities/search_flights \
  -H "Content-Type: application/json" \
  -d '{"origin": "JFK", "destination": "NRT", "departure_date": "2026-04-15", "adults": 1}'
```

---

## Features

- **Flight Search** - Search for available flights with pricing between any two airports
- **Airport Search** - Resolve city names to IATA airport codes for autocomplete
- **Cheapest Dates** - Find the cheapest travel dates for flexible planning
- **Airport Routes** - Discover all direct destinations from any airport
- **OAuth2 Token Management** - Automatic Amadeus token refresh with thread-safe caching
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Search Flights (`search_flights`)

**Endpoint:** `/api/capabilities/search_flights`

Searches for available flights between two airports with pricing.

**Request:**
```json
{
  "origin": "JFK",
  "destination": "NRT",
  "departure_date": "2026-04-15",
  "return_date": "2026-04-22",
  "adults": 1,
  "max_results": 5,
  "travel_class": "ECONOMY"
}
```

**Response:**
```json
{
  "origin": "JFK",
  "destination": "NRT",
  "departure_date": "2026-04-15",
  "return_date": "2026-04-22",
  "offers": [
    {
      "price": "845.00",
      "currency": "USD",
      "airlines": ["JL"],
      "total_duration": "13h 55m",
      "stops": 0,
      "segments": [
        {
          "departure_airport": "JFK",
          "departure_time": "2026-04-15T11:00:00",
          "arrival_airport": "NRT",
          "arrival_time": "2026-04-16T14:55:00",
          "airline": "JL",
          "flight_number": "JL5",
          "duration": "13h 55m",
          "cabin_class": "ECONOMY"
        }
      ]
    }
  ],
  "currency": "USD",
  "source": "Amadeus API (test)"
}
```

### 2. Search Airports (`search_airports`)

**Endpoint:** `/api/capabilities/search_airports`

Searches for airports and cities by keyword for autocomplete and IATA code resolution.

**Request:**
```json
{
  "keyword": "Tokyo"
}
```

**Response:**
```json
{
  "keyword": "Tokyo",
  "airports": [
    {
      "name": "Narita International Airport",
      "iata_code": "NRT",
      "type": "airport",
      "city": "Tokyo",
      "country": "JP",
      "latitude": 35.7647,
      "longitude": 140.3864
    },
    {
      "name": "Haneda Airport",
      "iata_code": "HND",
      "type": "airport",
      "city": "Tokyo",
      "country": "JP",
      "latitude": 35.5494,
      "longitude": 139.7798
    }
  ],
  "source": "Amadeus API (test)"
}
```

### 3. Cheapest Dates (`cheapest_dates`)

**Endpoint:** `/api/capabilities/cheapest_dates`

Finds the cheapest travel dates between two airports for flexible date planning.

**Request:**
```json
{
  "origin": "JFK",
  "destination": "NRT",
  "departure_date": "2026-04-01"
}
```

**Response:**
```json
{
  "origin": "JFK",
  "destination": "NRT",
  "dates": [
    {
      "departure_date": "2026-04-08",
      "return_date": "2026-04-15",
      "price": "720.00",
      "currency": "USD"
    },
    {
      "departure_date": "2026-04-12",
      "return_date": "2026-04-19",
      "price": "785.00",
      "currency": "USD"
    }
  ],
  "source": "Amadeus API (test)"
}
```

### 4. Airport Routes (`airport_routes`)

**Endpoint:** `/api/capabilities/airport_routes`

Lists all direct flight destinations from an airport.

**Request:**
```json
{
  "departure_airport": "JFK"
}
```

**Response:**
```json
{
  "departure_airport": "JFK",
  "destinations": [
    {
      "name": "London Heathrow",
      "iata_code": "LHR",
      "type": "airport"
    },
    {
      "name": "Narita International",
      "iata_code": "NRT",
      "type": "airport"
    }
  ],
  "source": "Amadeus API (test)"
}
```

---

## Architecture

```
Flight Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Authenticates via Amadeus OAuth2
    +-- Calls Amadeus Self-Service API
    +-- Returns standardized responses

Agents (Active)
    |
    +-- Discover flight tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the flight tool is automatically discovered by agents via Redis. You can query flight data through natural language:

```bash
# Query through the travel chat agent
curl -X POST http://localhost:8356/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "Find me flights from New York to Tokyo next month",
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
| `PORT` | HTTP server port | `8342` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

---

## API Rate Limits

Free tier limits (Amadeus Test Environment):

| Limit | Value |
|-------|-------|
| **Flight Offers** | ~2,000 calls/month |
| **Pricing** | ~3,000 calls/month |
| **Airport Search** | Generous limits |

The tool implements:
- OAuth2 token caching with automatic refresh
- Structured error logging for rate limit tracking
- Graceful error responses on API failures

---

## Project Structure

```
flight-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── flight_tool.go          # Tool definition, capability registration
├── amadeus_auth.go         # OAuth2 token manager (mutex + expiry caching)
├── amadeus_client.go       # Amadeus API client (4 endpoints)
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

# Should show: truvag3:service:flight-service
```

**2. OAuth2 authentication errors**

```bash
# Check logs for auth issues
kubectl logs -n truvag3-examples -l app=flight-tool | grep -i "auth\|token"

# Common issues:
# - Invalid credentials: Check AMADEUS_CLIENT_ID and AMADEUS_CLIENT_SECRET
# - Wrong environment: Ensure AMADEUS_ENV matches your credentials (test vs production)
```

**3. API errors or empty results**

```bash
# Check logs for API issues
kubectl logs -n truvag3-examples -l app=flight-tool | grep -i "api\|error"

# Common issues:
# - Invalid IATA code: Use valid 3-letter codes (JFK, NRT, LHR)
# - Past dates: Ensure departure_date is in the future
# - Rate limit: Wait or check your Amadeus dashboard
```

**4. Docker build fails**

```bash
# Ensure Docker is running
docker info

# If using Docker Desktop, ensure it's started
# Check Docker Desktop icon in system tray (Windows) or menu bar (macOS)
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
kubectl logs -n truvag3-examples -l app=flight-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=flight-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/flight-service 8342:80

# Test flight search
curl -X POST http://localhost:8342/api/capabilities/search_flights \
  -H "Content-Type: application/json" \
  -d '{"origin": "JFK", "destination": "NRT", "departure_date": "2026-04-15"}'
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
export PORT=8342

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `flight_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add Amadeus client method in `amadeus_client.go` if needed

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that can use this tool
- [hotel-tool](../hotel-tool/) - Hotel search tool (also uses Amadeus)
- [places-tool](../places-tool/) - Local places and restaurants search
- [travel-advisory-tool](../travel-advisory-tool/) - Travel safety advisories
- [stock-market-tool](../stock-market-tool/) - Stock market data tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
