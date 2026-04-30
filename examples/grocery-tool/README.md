# Grocery Tool

A proxy tool to the Grocery Store API with error injection capabilities for testing circuit breaker and rate limiting scenarios. This tool demonstrates resilience patterns in the TruvaG3 framework by providing configurable failure modes for integration testing.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Capabilities](#capabilities)
- [Architecture](#architecture)
- [Error Injection Testing](#error-injection-testing)
  - [Error Injection Modes](#error-injection-modes)
  - [Admin Endpoints](#admin-endpoints)
  - [Testing Scenarios](#testing-scenarios)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [Telemetry](#telemetry)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

Running this example locally is the best way to understand how the TruvaG3 framework handles resilience patterns like circuit breakers and rate limiting. Follow the steps below to get this example running.

### Prerequisites

Before running this example, you need to install the following tools. Choose the instructions for your operating system.

> **Note:** No API keys are required. This tool uses a local mock backend (`grocery-store-api`) for testing resilience patterns.

> **Important:** The grocery-tool requires the `grocery-store-api` backend service to be running. The API service handles actual product data and error injection configuration.

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

The fastest way to get everything running in your local:

```bash
cd examples/grocery-tool

# 1. Create .env from the example file
cp .env.example .env

# 2. Edit .env and set your configuration
#    - REDIS_URL: Redis connection for service discovery
#    - GROCERY_API_URL: Backend API URL (default works for K8s deployment)

# 3. Deploy cluster, infrastructure, and the grocery tool
./setup.sh full-deploy
```

**What `./setup.sh full-deploy` does:**
1. Creates a Kind Kubernetes cluster with proper port mappings
2. Deploys infrastructure (Redis, Prometheus, Grafana, Jaeger, OTEL Collector)
3. Builds and deploys the grocery-tool
4. Sets up port forwarding automatically

Once complete, access the application at:

| Service | URL | Description |
|---------|-----|-------------|
| **Grocery Tool** | http://localhost:8091 | Tool API endpoint |
| **Health Check** | http://localhost:8091/health | Service health status |
| **Capabilities** | http://localhost:8091/api/capabilities | Available capabilities |
| **Jaeger** | http://localhost:16686 | Distributed tracing |
| **Grafana** | http://localhost:3000 | Metrics dashboard (admin/admin) |
| **Prometheus** | http://localhost:9090 | Metrics queries |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Create the Kubernetes Cluster

```bash
cd examples/grocery-tool
./setup.sh cluster
```

This creates a Kind cluster named `truvag3-demo-<username>` with port mappings for all services.

#### Step 2: Deploy Infrastructure

```bash
./setup.sh infra
```

This deploys the shared infrastructure components:
- **Redis** - Service discovery and registration
- **OTEL Collector** - Telemetry aggregation
- **Prometheus** - Metrics storage
- **Jaeger** - Distributed tracing
- **Grafana** - Visualization dashboards

#### Step 3: Deploy the Grocery Tool

```bash
cd examples/grocery-tool

# Create .env from example and configure
cp .env.example .env
# Edit .env if needed (defaults work for K8s deployment)

# Build and deploy
./setup.sh docker-build
./setup.sh deploy
```

#### Step 4: Set Up Port Forwarding

```bash
./setup.sh forward-all
```

---

## Capabilities

The grocery-tool provides four capabilities for interacting with a grocery store:

| Capability | Description | Required Fields | Optional Fields |
|------------|-------------|-----------------|-----------------|
| **list_products** | Lists available grocery products | - | `category`, `limit` |
| **get_product** | Gets details for a specific product | `product_id` | - |
| **create_cart** | Creates a new shopping cart | - | - |
| **add_to_cart** | Adds a product to an existing cart | `cart_id`, `product_id`, `quantity` | - |

### Product Categories

The store organizes products into the following categories:

- `coffee`
- `dairy`
- `meat-seafood`
- `fresh-produce`
- `candy`

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           Agent / Client                                 │
│                                                                          │
└────────────────────────────────┬────────────────────────────────────────┘
                                 │ API Requests
                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                          grocery-tool                                    │
│                          (Port 8091)                                     │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────────────────────┐  │
│  │   Service    │  │  Capability  │  │       Proxy Handler           │  │
│  │  Discovery   │  │   Registry   │  │   (HTTP to grocery-store-api) │  │
│  │   (Redis)    │  │              │  │                               │  │
│  └──────────────┘  └──────────────┘  └───────────────┬───────────────┘  │
└──────────────────────────────────────────────────────┼──────────────────┘
                                                       │
                                                       ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                        grocery-store-api                                 │
│                                                                          │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────────────────────┐  │
│  │   Product    │  │    Cart      │  │       Error Injection         │  │
│  │    Data      │  │  Management  │  │         Engine                │  │
│  └──────────────┘  └──────────────┘  └───────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```

### How It Works

1. **Client sends a capability request** to the grocery-tool
2. **Tool proxies the request** to the grocery-store-api backend
3. **Backend processes the request** (may inject errors based on configuration)
4. **Response returns through the tool** with proper error categorization
5. **Agents can test resilience** by configuring error injection modes

### Data Isolation

| Data Type | Redis Database | Key Pattern |
|-----------|----------------|-------------|
| Service Registry | DB 0 | `truvag3:services:*` |

---

## Error Injection Testing

The primary use case for this tool is testing resilience patterns. Error injection is controlled via the grocery-store-api admin endpoints.

### Error Injection Modes

| Mode | HTTP Status | Description |
|------|-------------|-------------|
| **normal** | 200 | All requests succeed normally |
| **rate_limit** | 429 | Returns rate limit error after N requests |
| **server_error** | 500 | Returns server error with configurable probability |

### Admin Endpoints

Error injection is configured on the **grocery-store-api**, not the grocery-tool itself:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/admin/inject-error` | POST | Configure error injection mode |
| `/admin/status` | GET | Get current injection configuration |
| `/admin/reset` | POST | Reset to normal mode |

### Testing Scenarios

#### Scenario 1: Test Rate Limiting

```bash
# Configure rate limit mode (on grocery-store-api)
curl -X POST http://localhost:8080/admin/inject-error \
  -H "Content-Type: application/json" \
  -d '{
    "mode": "rate_limit",
    "requests_limit": 5,
    "retry_after": "5s"
  }'

# Make requests through grocery-tool - will fail after 5 requests
for i in {1..10}; do
  curl -X POST http://localhost:8091/api/capabilities/list_products \
    -H "Content-Type: application/json" \
    -d '{}'
  echo ""
done
```

#### Scenario 2: Test Server Errors

```bash
# Configure server error mode with 50% failure rate
curl -X POST http://localhost:8080/admin/inject-error \
  -H "Content-Type: application/json" \
  -d '{
    "mode": "server_error",
    "error_rate": 0.5
  }'

# Make requests - approximately 50% will fail with 500
curl -X POST http://localhost:8091/api/capabilities/list_products \
  -H "Content-Type: application/json" \
  -d '{}'
```

#### Scenario 3: Reset to Normal

```bash
# Reset to normal operation
curl -X POST http://localhost:8080/admin/reset

# Verify status
curl http://localhost:8080/admin/status
```

---

## API Reference

### `POST /api/capabilities/list_products`

Lists available grocery products.

**Request:**
```json
{
  "category": "coffee",
  "limit": 10
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "products": [
      {
        "id": 1,
        "name": "Organic Coffee Beans",
        "category": "coffee",
        "price": 12.99,
        "inStock": true
      }
    ],
    "count": 1,
    "category": "coffee"
  }
}
```

### `POST /api/capabilities/get_product`

Gets details for a specific product.

**Request:**
```json
{
  "product_id": 1
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "product": {
      "id": 1,
      "name": "Organic Coffee Beans",
      "category": "coffee",
      "manufacturer": "Mountain Roasters",
      "price": 12.99,
      "current-stock": 50,
      "inStock": true
    }
  }
}
```

### `POST /api/capabilities/create_cart`

Creates a new shopping cart.

**Request:**
```json
{}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "cart_id": "cart-abc123",
    "message": "Cart created successfully",
    "created_at": "2024-01-01T00:00:00Z"
  }
}
```

### `POST /api/capabilities/add_to_cart`

Adds a product to an existing cart.

**Request:**
```json
{
  "cart_id": "cart-abc123",
  "product_id": 1,
  "quantity": 2
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "cart_id": "cart-abc123",
    "item_id": 1,
    "message": "Added 2 item(s) to cart"
  }
}
```

### `GET /health`

Health check endpoint.

**Response:**
```json
{
  "status": "healthy",
  "service": "grocery-service",
  "timestamp": "2024-01-01T00:00:00Z"
}
```

### `GET /api/capabilities`

Lists available tool capabilities.

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `GROCERY_API_URL` | Backend grocery store API URL | K8s internal URL | Yes |
| `PORT` | HTTP server port | `8083` | No |
| `NAMESPACE` | Kubernetes namespace | `truvag3-examples` | No |
| `DEV_MODE` | Enable development mode | `false` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP endpoint for telemetry | - | No |
| `APP_ENV` | Environment (development/staging/production) | `development` | No |

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

The `.env.example` file contains comprehensive documentation for all options including:

- **Core Tool Configuration** - Port, namespace, development mode
- **Discovery Configuration** - Redis URL for service registration
- **Application Configuration** - Backend API URL
- **Logging Configuration** - Log level and format
- **Telemetry Configuration** - OTLP endpoints

---

## Telemetry

The tool includes comprehensive observability:

### Tracing (Jaeger)

- All requests traced with span events
- API call timing and error tracking
- Error injection visibility
- Access at http://localhost:16686

### Metrics (Prometheus/Grafana)

| Metric | Type | Description |
|--------|------|-------------|
| `http.request.duration_ms` | Histogram | Request duration |
| `discovery.registrations` | Counter | Service registrations |
| `discovery.health_checks` | Counter | Health check calls |

Access Grafana at http://localhost:3000 (admin/admin)

### Logging

Structured JSON logs with component attribution and trace context:

```json
{
  "component": "tool/grocery-service",
  "level": "INFO",
  "message": "list_products completed",
  "operation": "list_products",
  "service": "grocery-service",
  "count": 10,
  "category": "coffee",
  "timestamp": "2024-01-01T00:00:00Z",
  "trace.span_id": "0b319744acd226d5",
  "trace.trace_id": "445c352173a351de293d4d27416b0eb2"
}
```

---

## Project Structure

```
grocery-tool/
├── main.go              # Entry point and initialization
├── grocery_tool.go      # Tool definition and capability registration
├── handlers.go          # HTTP handlers for proxying to API
├── go.mod               # Go module definition
├── go.sum               # Go module checksums
├── Dockerfile           # Production container image
├── Dockerfile.workspace # Development container with local modules
├── k8-deployment.yaml   # Kubernetes deployment manifest
├── setup.sh             # Build and deployment script
├── .env.example         # Example environment configuration
└── README.md            # This file
```

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

**2. "API_UNAVAILABLE" error**

The grocery-store-api backend is not reachable:
```bash
# Check if grocery-store-api is running
kubectl get pods -n truvag3-examples -l app=grocery-store-api

# Check the API URL configuration
kubectl logs -n truvag3-examples -l app=grocery-tool | grep GROCERY_API_URL
```

**3. Rate limit errors (429)**

Error injection may be enabled. Check and reset:
```bash
# Check current injection status
curl http://localhost:8080/admin/status

# Reset to normal mode
curl -X POST http://localhost:8080/admin/reset
```

**4. Port forward not working**

Kill existing port forwards and restart:
```bash
pkill -f 'kubectl.*port-forward.*truvag3-examples'
./setup.sh forward-all
```

### Useful Commands

```bash
# View tool logs
./setup.sh logs

# Check pod status
kubectl get pods -n truvag3-examples -l app=grocery-tool

# Check Redis service registry
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 0 KEYS 'truvag3:services:*'

# Test the API
./setup.sh test

# Full cleanup
./setup.sh clean-all
```

---

## Related Examples

- [agent-with-resilience](../agent-with-resilience/) - Agent demonstrating resilience patterns
- [agent-with-orchestration](../agent-with-orchestration/) - Basic orchestration example
- [agent-with-telemetry](../agent-with-telemetry/) - Full observability example
- [travel-chat-agent](../travel-chat-agent/) - Streaming chat agent with tool orchestration

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
