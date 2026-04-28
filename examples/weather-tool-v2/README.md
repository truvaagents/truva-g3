# Weather Tool v2

A Truva-G3 tool that provides weather forecast capabilities using the [Open-Meteo](https://open-meteo.com/) API. This tool delivers current weather conditions and multi-day forecasts without requiring any API keys.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Why v2?](#why-v2)
- [Capabilities](#capabilities)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [Weather Codes](#weather-codes)
- [Distributed Tracing](#distributed-tracing)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

Running this tool locally is the best way to understand how Truva-G3 tools work. This is an **independent tool** - it only requires Redis for service discovery and does not depend on any other tools or agents.

### Prerequisites

Before running this example, you need to install the following tools. Choose the instructions for your operating system.

> **Note:** No API key is required. This tool uses the free [Open-Meteo](https://open-meteo.com/) API which has no authentication requirements.

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

The fastest way to get this tool running:

```bash
cd examples/weather-tool-v2

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env

# 2. Deploy to Kubernetes (handles cluster, infrastructure, and tool deployment)
./setup.sh deploy
```

> **Note:** This tool uses free public APIs and does not require any API keys.

**What `./setup.sh deploy` does:**
1. Builds the Docker image
2. Loads it into the Kind cluster
3. Deploys the tool to Kubernetes
4. Registers with Redis for service discovery

Once complete, the tool is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **Weather API** | http://localhost:8096 | Weather tool REST API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Local Development

```bash
cd examples/weather-tool-v2

# Start Redis (if not running)
docker run -d --name redis -p 6379:6379 redis:alpine

# Create .env from example and configure if needed
cp .env.example .env

# Build and run locally
./setup.sh run
```

#### Step 2: Docker

```bash
# Build the Docker image
./setup.sh docker-build

# Run the container
docker run -p 8096:8096 \
  -e REDIS_URL=redis://host.docker.internal:6379 \
  weather-tool-v2:latest
```

#### Step 3: Kubernetes

```bash
# Deploy to cluster
./setup.sh deploy

# Check status
kubectl get pods -n truvag3-examples -l app=weather-tool-v2
```

---

## Features

| Feature | Description |
|---------|-------------|
| **Current Weather** | Get current temperature, humidity, wind, and conditions |
| **Multi-Day Forecast** | Up to 16-day weather forecast |
| **Free API** | No API key required |
| **Distributed Tracing** | Built-in trace context propagation |
| **Coordinate-Based** | Works with any lat/lon coordinates |

---

## Why v2?

This tool uses Open-Meteo instead of commercial weather APIs:

| Benefit | Description |
|---------|-------------|
| **Free forever** | No API key, no credit card |
| **Unlimited requests** | No rate limiting for reasonable use |
| **Accurate data** | Uses NOAA, DWD, and other meteorological sources |
| **Works with geocoding** | Accepts coordinates from geocoding-tool |

---

## Capabilities

### get_weather_forecast

Gets current weather and multi-day forecast for coordinates.

**Request:**
```json
{
  "lat": 35.6762,
  "lon": 139.6503,
  "days": 7
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "lat": 35.6762,
    "lon": 139.6503,
    "timezone": "Asia/Tokyo",
    "temperature_current": 12.5,
    "temperature_min": 5,
    "temperature_max": 15,
    "temperature_avg": 10.5,
    "condition": "Partly cloudy",
    "weather_code": 2,
    "humidity": 55,
    "wind_speed": 12.3,
    "forecast": [
      {
        "date": "2024-12-03",
        "temperature_min": 5,
        "temperature_max": 15,
        "condition": "Partly cloudy",
        "precipitation": 0,
        "wind_speed_max": 15
      }
    ]
  }
}
```

### get_current_weather

Gets current weather conditions only (no forecast).

**Request:**
```json
{
  "lat": 40.7128,
  "lon": -74.0060
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "lat": 40.7128,
    "lon": -74.006,
    "timezone": "America/New_York",
    "temperature_current": 8.5,
    "condition": "Clear sky",
    "humidity": 45,
    "wind_speed": 8.2
  }
}
```

---

## API Reference

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/api/capabilities` | GET | List all capabilities |
| `/api/capabilities/get_weather_forecast` | POST | Weather forecast |
| `/api/capabilities/get_weather_forecast/schema` | GET | Input schema |
| `/api/capabilities/get_current_weather` | POST | Current weather |
| `/api/capabilities/get_current_weather/schema` | GET | Input schema |

### Test with curl

```bash
# Weather forecast (with coordinates from geocoding)
curl -X POST http://localhost:8096/api/capabilities/get_weather_forecast \
  -H "Content-Type: application/json" \
  -d '{"lat": 35.6762, "lon": 139.6503, "days": 7}'

# Current weather only
curl -X POST http://localhost:8096/api/capabilities/get_current_weather \
  -H "Content-Type: application/json" \
  -d '{"lat": 40.7128, "lon": -74.0060}'

# Health check
curl http://localhost:8096/health

# List capabilities
curl http://localhost:8096/api/capabilities
```

---

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_URL` | (required) | Redis connection URL |
| `PORT` | `8096` | HTTP server port |
| `APP_ENV` | `development` | Environment (development/staging/production) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | - | OpenTelemetry collector endpoint |
| `DEV_MODE` | `false` | Enable development mode |
| `TRUVAG3_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
# Edit .env if needed (defaults work for local development)
```

---

## Weather Codes

The tool converts WMO weather codes to human-readable conditions:

| Code | Condition |
|------|-----------|
| 0 | Clear sky |
| 1-3 | Mainly clear to Overcast |
| 45-48 | Fog |
| 51-57 | Drizzle |
| 61-67 | Rain |
| 71-77 | Snow |
| 80-82 | Rain showers |
| 85-86 | Snow showers |
| 95-99 | Thunderstorm |

---

## Distributed Tracing

This tool uses `otelhttp` to propagate trace context. When called from an orchestrator:

```
orchestrator (parent span)
  └── geocoding-tool (child span)
        └── weather-tool-v2 (sibling span)
              └── open-meteo-api-call (grandchild span)
```

Traces can be viewed in Jaeger at http://localhost:16686 when running with the full infrastructure stack.

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

**2. Tool not discovered by orchestrator**

Ensure the tool is registered with Redis:
```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 0 KEYS 'truvag3:services:*'
```

**3. Connection refused on port 8096**

Check if the pod is running and port forwarding is active:
```bash
kubectl get pods -n truvag3-examples -l app=weather-tool-v2
kubectl port-forward -n truvag3-examples svc/weather-tool-v2 8096:8096
```

### Useful Commands

```bash
# View tool logs
kubectl logs -n truvag3-examples -l app=weather-tool-v2 -f

# Check pod status
kubectl get pods -n truvag3-examples -l app=weather-tool-v2

# Restart the tool
kubectl rollout restart -n truvag3-examples deployment/weather-tool-v2

# Full cleanup
./setup.sh cleanup
```

---

## Related Examples

- [travel-chat-agent](../travel-chat-agent/) - Chat agent that orchestrates this tool
- [geocoding-tool](../geocoding-tool/) - Location geocoding (provides coordinates for weather lookups)
- [agent-with-orchestration](../agent-with-orchestration/) - Basic orchestration example

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
