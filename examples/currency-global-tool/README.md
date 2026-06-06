# Currency Global Tool

A TruvaG3 tool that provides global currency conversion using the [CurrencyBeacon](https://currencybeacon.com/) API. This tool supports 170+ currencies (including all major, minor, and exotic currencies), unlike the standard currency-tool which is limited to 31 ECB currencies. It is independent and can be deployed standalone - it only requires Redis for service discovery.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Capabilities](#capabilities)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

Running this tool locally demonstrates how TruvaG3 tools register with Redis for service discovery and expose capabilities via a REST API.

### Prerequisites

Before running this example, you need to install the following tools. Choose the instructions for your operating system.

> **Note:** This tool requires a free API key from [CurrencyBeacon](https://currencybeacon.com/register). The free tier includes 5,000 API calls per month.

> **Note:** This tool is independent and only requires Redis for service discovery. It can be deployed standalone or as part of the full travel-chat-agent example.

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
# Expected: go version go1.26.x darwin/arm64 (or darwin/amd64)
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
# Expected: go version go1.26.x windows/amd64
```

</details>

<details>
<summary><strong>Linux Installation</strong></summary>

**Manual installation (recommended for latest version):**
```bash
# Download Go (replace version as needed)
curl -LO https://go.dev/dl/go1.26.4.linux-amd64.tar.gz

# Remove any previous installation and extract
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.4.linux-amd64.tar.gz

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
# Expected: go version go1.26.x linux/amd64
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

The fastest way to get the tool running:

```bash
cd examples/currency-global-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env

# 2. Add your CurrencyBeacon API key
#    Get a free key from: https://currencybeacon.com/register
#    Edit .env and set CURRENCYBEACON_API_KEY=your-key

# 3. Full deploy (creates cluster + infrastructure + tool in one step)
./setup.sh full-deploy
```

> **Note:** You must obtain a free API key from [CurrencyBeacon](https://currencybeacon.com/register) and set it in your `.env` file before deploying.

Once complete, the tool is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **Currency Global Tool API** | http://localhost:8346 | REST API for global currency conversion (170+ currencies) |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Create the Kubernetes Cluster

If you don't already have a cluster running:

```bash
cd examples/k8-deployment
./setup.sh cluster
```

This creates a Kind cluster named `truvag3-demo-<username>` with port mappings for all services.

#### Step 2: Deploy Infrastructure

```bash
cd examples/k8-deployment
./setup.sh infra
```

This deploys the shared infrastructure components:
- **Redis** - Service discovery (required)
- **OTEL Collector** - Telemetry aggregation
- **Prometheus** - Metrics storage
- **Jaeger** - Distributed tracing
- **Grafana** - Visualization dashboards

#### Step 3: Deploy the Tool

```bash
cd examples/currency-global-tool

# Create .env from example
cp .env.example .env

# Set your CurrencyBeacon API key in .env
# CURRENCYBEACON_API_KEY=your-key-here

# Build and deploy
./setup.sh docker-build
./setup.sh deploy
```

#### Step 4: Set Up Port Forwarding

```bash
./setup.sh forward
```

---

## Features

- **170+ Currencies** - Supports all major, minor, and exotic global currencies (USD, EUR, GBP, JPY, CNY, INR, BRL, MXN, AED, NGN, KES, VND, and many more)
- **Currency Conversion** - Convert amounts between any supported currencies with real-time rates
- **Exchange Rates** - Get current rates for any base currency against multiple targets
- **Hourly Updates** - Exchange rates updated hourly for accuracy
- **Distributed Tracing** - Built-in trace context propagation via OpenTelemetry

---

## Capabilities

This tool exposes the following capabilities that can be discovered and invoked by agents:

### convert_currency

Converts an amount from one currency to another using real-time exchange rates.

**Request:**
```bash
curl -X POST http://localhost:8346/api/capabilities/convert_currency \
  -H "Content-Type: application/json" \
  -d '{"from": "USD", "to": "INR", "amount": 1000}'
```

**Response:**
```json
{
  "from": "USD",
  "to": "INR",
  "amount": 1000,
  "result": 83250.00,
  "rate": 83.25,
  "date": "2024-01-15"
}
```

### get_exchange_rates

Gets current exchange rates for a base currency against multiple targets.

**Request:**
```bash
curl -X POST http://localhost:8346/api/capabilities/get_exchange_rates \
  -H "Content-Type: application/json" \
  -d '{"base": "USD", "currencies": ["EUR", "GBP", "JPY", "INR", "CNY"]}'
```

**Response:**
```json
{
  "base": "USD",
  "date": "2024-01-15",
  "rates": {
    "EUR": 0.92,
    "GBP": 0.79,
    "JPY": 149.85,
    "INR": 83.25,
    "CNY": 7.18
  }
}
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                            Agent / Client                                │
└────────────────────────────────┬────────────────────────────────────────┘
                                 │ HTTP Request
                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                       currency-global-tool                               │
│                            (Port 8346)                                   │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────────┐   │
│  │  Service         │  │   Capability     │  │   CurrencyBeacon     │   │
│  │  Registration    │  │   Handlers       │  │   API Client         │   │
│  │  (Redis)         │  │                  │  │                      │   │
│  └──────────────────┘  └──────────────────┘  └──────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
                                 │
                                 ▼
                    ┌─────────────────────────┐
                    │   CurrencyBeacon API    │
                    │ (currencybeacon.com)    │
                    └─────────────────────────┘
```

### How It Works

1. **Tool registers** with Redis on startup, advertising its capabilities
2. **Agents discover** the tool via Redis service registry
3. **Agent sends request** to the tool's capability endpoint
4. **Tool calls** the CurrencyBeacon API for currency data
5. **Response returns** to the agent with conversion results
6. **Traces propagate** through the entire request chain

---

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_URL` | (required) | Redis connection URL for service discovery |
| `CURRENCYBEACON_API_KEY` | (required) | API key from [CurrencyBeacon](https://currencybeacon.com/register) |
| `PORT` | `8346` | HTTP server port |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | - | OTLP endpoint for telemetry (optional) |
| `APP_ENV` | `development` | Environment profile (`development`, `staging`, `production`) |
| `DEV_MODE` | `false` | Enable detailed logging |

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

You must set `CURRENCYBEACON_API_KEY` with a valid key. Get a free key from [currencybeacon.com/register](https://currencybeacon.com/register).

---

## Troubleshooting

### Common Issues

**1. "REDIS_URL environment variable required" error**

Ensure Redis is running and `REDIS_URL` is set:
```bash
# Check if Redis is running
kubectl get pods -n truvag3-examples -l app=redis

# Check Redis connectivity
kubectl exec -n truvag3-examples deploy/redis -- redis-cli ping
```

**2. "CurrencyBeacon API key not configured" error**

Ensure `CURRENCYBEACON_API_KEY` is set in your `.env` file:
```bash
# Verify the key is set
grep CURRENCYBEACON_API_KEY .env

# Get a free key from: https://currencybeacon.com/register
```

**3. Tool not discovered by agents**

Verify the tool is registered with Redis:
```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 0 KEYS 'truvag3:services:*'
```

**4. Port forward not working**

Kill existing port forwards and restart:
```bash
pkill -f 'kubectl.*port-forward.*currency-global-tool'
./setup.sh forward
```

### Useful Commands

```bash
# View tool logs
./setup.sh logs

# Check pod status
./setup.sh status

# Test the API directly
curl -X POST http://localhost:8346/api/capabilities/convert_currency \
  -H "Content-Type: application/json" \
  -d '{"from": "USD", "to": "EUR", "amount": 100}'

# Full cleanup
./setup.sh clean-all
```

---

## Related Examples

- [currency-tool](../currency-tool/) - Basic currency tool using free Frankfurter API (31 ECB currencies)
- [travel-chat-agent](../travel-chat-agent/) - Chat agent that orchestrates this tool
- [weather-tool-v2](../weather-tool-v2/) - Weather data tool
- [geocoding-tool](../geocoding-tool/) - Location geocoding tool
- [country-info-tool](../country-info-tool/) - Country information tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
