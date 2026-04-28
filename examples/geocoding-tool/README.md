# Geocoding Tool

A Truva-G3 tool that provides location geocoding capabilities using the [Nominatim](https://nominatim.org/) API (OpenStreetMap). This tool converts location names to geographic coordinates and vice versa.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Capabilities](#capabilities)
  - [geocode_location](#geocode_location)
  - [reverse_geocode](#reverse_geocode)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [Rate Limiting](#rate-limiting)
- [Distributed Tracing](#distributed-tracing)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

This tool is independent and only requires Redis for service discovery. No API keys are needed as it uses the free Nominatim API.

### Prerequisites

Before running this example, you need to install the following tools. Choose the instructions for your operating system.

> **Note:** No API key is required. This tool uses the free [Nominatim](https://nominatim.org/) API from OpenStreetMap.

> **Important:** Nominatim API has a strict rate limit of 1 request per second. See [Rate Limiting](#rate-limiting) for details.

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

**Set up Go workspace (if not using modules):**
```bash
# Add to ~/.zshrc or ~/.bash_profile
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin
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

**Using apt (may not have latest version):**
```bash
sudo apt update
sudo apt install golang-go
```

**Using snap:**
```bash
sudo snap install go --classic
```

**Verify installation:**
```bash
go version
# Expected: go version go1.25.x linux/amd64
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

The fastest way to get the geocoding tool running:

```bash
cd examples/geocoding-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env

# 2. Deploy the tool to Kubernetes
./setup.sh deploy
```

> **Note:** This tool uses free public APIs and does not require any API keys.

**What `./setup.sh deploy` does:**
1. Builds the Docker image
2. Loads the image into the Kind cluster
3. Deploys the tool to Kubernetes
4. Sets up service discovery with Redis

Once complete, the tool is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **Geocoding API** | http://localhost:8095 | Geocoding REST API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Local Development

```bash
cd examples/geocoding-tool

# Start Redis (if not running)
docker run -d --name redis -p 6379:6379 redis:alpine

# Create .env from example
cp .env.example .env

# Build and run locally
./setup.sh run
```

#### Step 2: Docker Build

```bash
# Build the Docker image
./setup.sh docker-build

# Run the container
docker run -p 8095:8095 \
  -e REDIS_URL=redis://host.docker.internal:6379 \
  geocoding-tool:latest
```

#### Step 3: Kubernetes Deployment

```bash
# Deploy to the cluster
./setup.sh deploy

# Check status
kubectl get pods -n truvag3-examples -l app=geocoding-tool
```

---

## Features

- **Forward Geocoding**: Convert location names to geographic coordinates
- **Reverse Geocoding**: Convert coordinates to location names
- **Distributed Tracing**: Built-in trace context propagation
- **Service Discovery**: Automatic registration with Redis

---

## Capabilities

### geocode_location

Converts a location name to geographic coordinates.

**Request:**
```json
{
  "location": "Tokyo, Japan"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "lat": 35.6762,
    "lon": 139.6503,
    "display_name": "Tokyo, Japan",
    "country_code": "jp",
    "country": "Japan",
    "city": "Tokyo"
  }
}
```

### reverse_geocode

Converts coordinates to a location name.

**Request:**
```json
{
  "lat": 35.6762,
  "lon": 139.6503
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "lat": 35.6762,
    "lon": 139.6503,
    "display_name": "Tokyo, Kanto Region, Japan",
    "country_code": "jp",
    "country": "Japan",
    "city": "Tokyo"
  }
}
```

---

## API Reference

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/api/capabilities` | GET | List all capabilities |
| `/api/capabilities/geocode_location` | POST | Forward geocoding |
| `/api/capabilities/geocode_location/schema` | GET | Input schema |
| `/api/capabilities/reverse_geocode` | POST | Reverse geocoding |
| `/api/capabilities/reverse_geocode/schema` | GET | Input schema |

### Example Requests

```bash
# Forward geocoding
curl -X POST http://localhost:8095/api/capabilities/geocode_location \
  -H "Content-Type: application/json" \
  -d '{"location": "New York, USA"}'

# Reverse geocoding
curl -X POST http://localhost:8095/api/capabilities/reverse_geocode \
  -H "Content-Type: application/json" \
  -d '{"lat": 40.7128, "lon": -74.0060}'

# Health check
curl http://localhost:8095/health

# List capabilities
curl http://localhost:8095/api/capabilities
```

---

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_URL` | (required) | Redis connection URL |
| `PORT` | `8095` | HTTP server port |
| `APP_ENV` | `development` | Environment (development/staging/production) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | - | OpenTelemetry collector endpoint |
| `DEV_MODE` | `false` | Enable development mode |
| `TRUVAG3_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

---

## Rate Limiting

Nominatim API has a strict rate limit of **1 request per second**. This tool includes a built-in delay to respect this limit.

For production use, consider:

- Implementing a proper rate limiter
- Caching geocoding results
- Using a commercial geocoding service

> **Warning:** Exceeding the rate limit may result in your IP being temporarily blocked by Nominatim.

---

## Distributed Tracing

This tool uses `telemetry.NewTracedHTTPClient` to propagate trace context to the Nominatim API. When called from an orchestrator:

```
orchestrator (parent span)
  └── geocoding-tool (child span)
        └── nominatim-api-call (grandchild span)
```

Access traces at http://localhost:16686 (Jaeger) when running with full infrastructure.

---

## Troubleshooting

### Common Issues

**1. "REDIS_URL is required" error**

Ensure Redis is running and `REDIS_URL` is set:
```bash
# Check if Redis is running
kubectl get pods -n truvag3-examples -l app=redis

# Or for local development
redis-cli ping
```

**2. Rate limit exceeded**

The Nominatim API allows only 1 request per second. Wait a moment and retry. Consider implementing caching for repeated queries.

**3. Pod not starting**

Check pod status and logs:
```bash
kubectl get pods -n truvag3-examples -l app=geocoding-tool
kubectl logs -n truvag3-examples -l app=geocoding-tool
```

### Useful Commands

```bash
# View tool logs
./setup.sh logs

# Check pod status
kubectl get pods -n truvag3-examples -l app=geocoding-tool

# Test the API
curl http://localhost:8095/health

# Full cleanup
./setup.sh cleanup
```

---

## Part of Truva-G3 Examples

This tool is designed to work with the Smart Travel Research Assistant example. It provides geocoding for travel destinations, which is used by the weather-tool-v2 to fetch weather data.

### Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent that orchestrates tools
- [weather-tool-v2](../weather-tool-v2/) - Weather data tool
- [currency-tool](../currency-tool/) - Currency exchange tool
- [country-info-tool](../country-info-tool/) - Country information tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
