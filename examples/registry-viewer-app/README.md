# Registry Viewer App

A standalone, real-time web dashboard for viewing services registered in a Redis-based service registry. This app is designed to be fully independent and can be extracted to its own repository without any modifications.

## Table of Contents

- [Features](#features)
- [Quick Start](#quick-start)
- [Setup Script Commands](#setup-script-commands)
- [Configuration](#configuration)
- [API Endpoints](#api-endpoints)
- [Service Data Structure](#service-data-structure)
- [Docker](#docker)
- [Kubernetes Deployment](#kubernetes-deployment)
- [Project Structure](#project-structure)
- [Extracting to Standalone Repository](#extracting-to-standalone-repository)
- [Port Allocation](#port-allocation)
- [Build Environment Variables](#build-environment-variables)

---

## Features

### Service Registry View
- Real-time view of all registered tools and agents
- Visual distinction between tools (green) and agents (purple)
- Health status indicators with pulse animation
- Last heartbeat timestamp for each service
- Expandable cards showing full service details:
  - Connection details (ID, address, port)
  - Capabilities list with descriptions
  - Metadata (JSON format)
- Auto-refresh every 5 seconds (configurable)
- Statistics bar showing total/agents/tools/healthy counts

### LLM Debug View
- Browse LLM debug records captured during orchestration
- View complete prompts and responses (no truncation)
- Filter by request ID or recording site
- Expandable interaction cards showing:
  - Recording site (plan_generation, synthesis, etc.)
  - Model and provider information
  - Token usage statistics
  - Full prompt and response content
  - Duration and timestamp
- Requires `TRUVAG3_LLM_DEBUG_ENABLED=true` on the orchestration agent

### UI Features
- Dark theme optimized for monitoring
- Tab-based navigation (Services / LLM Debug)
- Zero external framework dependencies

## 🚀 Quick Start

### Prerequisites

- **Infrastructure deployed**: This is an add-on app that connects to existing TruvaG3 infrastructure
- Run any tool/agent example first (e.g., `cd examples/tool-example && ./setup.sh full-deploy`)

### Deploy to Kubernetes (Recommended)

```bash
cd examples/registry-viewer-app

# Deploy to existing Kind cluster
./setup.sh deploy

# Set up port forwarding
./setup.sh forward
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image with Go backend and embedded static files
2. Loads the image into the Kind cluster
3. Creates ConfigMap with Redis connection info (auto-extracted from k8-deployment/redis.yaml)
4. Deploys the app to Kubernetes

Once complete, the dashboard is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **Registry Viewer** | http://localhost:8100 | Web dashboard for service registry |
| **API** | http://localhost:8100/api/services | JSON list of registered services |
| **Health** | http://localhost:8100/api/health | Health check endpoint |

### Run Locally with Mock Data

For quick UI preview without Kubernetes:

```bash
cd examples/registry-viewer-app

# Build and run with mock data
./setup.sh run
```

Open http://localhost:8100 in your browser.

## Setup Script Commands

```bash
./setup.sh <command>

Local Development:
  build         Build the application locally
  run           Run locally with mock data (default)
  run-redis     Run locally connected to Redis
  status        Show status of local/docker/k8s resources

Docker:
  docker        Build Docker image
  docker-run    Run Docker container locally

Kubernetes Deployment:
  deploy        Build, load to Kind, and deploy to K8s
  rebuild       Rebuild with --no-cache and redeploy
  forward       Port forward from K8s to localhost:8100
  logs          Stream logs from K8s pod
  cleanup       Remove deployed resources
```

## Configuration

### Command Line Options

| Flag | Default | Description |
|------|---------|-------------|
| `-mock` | `true` | Use mock data instead of Redis |
| `-redis-url` | `redis://localhost:6379` | Redis connection URL |
| `-namespace` | `truvag3` | Redis key namespace for service discovery |
| `-port` | `8100` | HTTP server port |

### Environment Variables

Environment variables override command-line flags, making the app easy to configure in Kubernetes:

| Variable | Description |
|----------|-------------|
| `REDIS_URL` | Redis connection URL (overrides `-redis-url`) |
| `REDIS_NAMESPACE` | Redis key namespace (overrides `-namespace`) |
| `USE_MOCK` | Set to `false` to use Redis (overrides `-mock`) |
| `PORT` | HTTP server port (overrides `-port`) |

### Kubernetes ConfigMap

When deployed to Kubernetes, the app reads configuration from a ConfigMap named `registry-viewer-config`. The `setup.sh deploy` command automatically:

1. Extracts Redis service info from `../k8-deployment/redis.yaml`
2. Creates/updates the ConfigMap with the correct Redis URL
3. Deploys the app with environment variables from the ConfigMap

To override the Redis URL during deployment:

```bash
REDIS_URL=redis://custom-redis:6379 ./setup.sh deploy
```

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /` | Web UI |
| `GET /api/services` | JSON list of all registered services |
| `GET /api/health` | Health check endpoint |
| `GET /api/llm-debug` | List recent LLM debug records |
| `GET /api/llm-debug/{request_id}` | Get full debug record by request ID |

## Service Data Structure

The app expects services to be stored in Redis with keys matching the pattern `{namespace}:services:*`. Each service should be a JSON object:

```json
{
  "id": "weather-tool-abc123",
  "name": "weather-tool",
  "type": "tool",
  "description": "Provides current weather information",
  "address": "weather-tool-service.example",
  "port": 80,
  "capabilities": [
    {
      "name": "get-weather",
      "description": "Get current weather for a location",
      "version": "1.0.0"
    }
  ],
  "metadata": {
    "provider": "openweathermap",
    "version": "2.1.0"
  },
  "health": "healthy",
  "lastSeen": "2024-01-15T10:30:00Z"
}
```

## Docker

### Build and Run

```bash
# Build
./setup.sh docker

# Run with mock data
./setup.sh docker-run

# Run with Redis
./setup.sh docker-run redis
```

### Docker Container (via setup.sh)

```bash
# Build the image
./setup.sh docker

# Run the container with mock data
./setup.sh docker-run

# Run the container connected to Redis (host network)
./setup.sh docker-run redis
```

## Kubernetes Deployment

```bash
# Deploy to existing Kind cluster (build + load into Kind + apply manifest)
./setup.sh deploy

# Port forward the app to localhost:8100
./setup.sh forward
```

> **Note:** This app assumes the Kind cluster and Redis are already running — it has no `cluster` / `infra` / `full-deploy` subcommands. Bring those up via any sibling agent first (e.g., `cd ../travel-chat-agent && ./setup.sh full-deploy`) or via `examples/k8-deployment/` directly.

## Project Structure

```
registry-viewer-app/
├── main.go              # Go backend with embedded static files
├── go.mod               # Go module (standalone, no framework deps)
├── go.sum               # Dependency checksums
├── static/
│   └── index.html       # Single-page frontend (HTML/CSS/JS)
├── Dockerfile           # Container build
├── k8-deployment.yaml   # Kubernetes manifests
├── setup.sh             # Setup and deployment script
└── README.md            # This file
```

## Extracting to Standalone Repository

This app is designed to be fully portable. To use it as a standalone project:

1. Copy the entire `registry-viewer-app` folder
2. No modifications needed - just build and run
3. The module path is generic (`registry-viewer-app`)
4. Only dependency is `go-redis/redis/v8`

```bash
# In a new location/repo
go build -o registry-viewer .
./registry-viewer
```

## Port Allocation

Default port `8100` was chosen to avoid conflicts with common service ports:
- 8080-8099: Reserved for example tools and agents
- 3000: Grafana
- 6379: Redis
- 9090: Prometheus
- 16686: Jaeger

## Build Environment Variables

| Variable | Description |
|----------|-------------|
| `REDIS_URL` | Override Redis URL for deployment (default: extracted from redis.yaml) |
| `REDIS_NAMESPACE` | Redis key namespace (default: `truvag3`) |
| `DOCKER_NO_CACHE` | Set to `true` for fresh Docker build |

## Related Orchestration Environment Variables

The Registry Viewer displays data from the orchestration layer. Configure these variables on your **orchestration agent** (not the viewer itself) to enable features.

### LLM Debug Storage

Enable LLM debug storage to capture full request/response payloads for troubleshooting:

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_LLM_DEBUG_ENABLED` | `false` | Enable LLM debug payload storage |
| `TRUVAG3_LLM_DEBUG_TTL` | `24h` | Retention period for successful debug records |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | `168h` (7 days) | Retention period for error debug records |
| `TRUVAG3_LLM_DEBUG_REDIS_DB` | `7` | Redis database number for debug storage |

**Example:**
```bash
export TRUVAG3_LLM_DEBUG_ENABLED=true
export TRUVAG3_LLM_DEBUG_TTL=48h
```

### Human-in-the-Loop (HITL) Configuration

Configure HITL checkpoints for human oversight of AI-generated plans:

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_HITL_ENABLED` | `false` | Enable HITL globally |
| `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | `false` | Require human approval for all plans |
| `TRUVAG3_HITL_SENSITIVE_CAPABILITIES` | (empty) | Capabilities requiring plan + step approval (comma-separated) |
| `TRUVAG3_HITL_SENSITIVE_AGENTS` | (empty) | Agents requiring plan + step approval (comma-separated) |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | (empty) | Capabilities requiring step-only approval (comma-separated) |
| `TRUVAG3_HITL_STEP_SENSITIVE_AGENTS` | (empty) | Agents requiring step-only approval (comma-separated) |
| `TRUVAG3_HITL_DEFAULT_TIMEOUT` | `5m` | Timeout for human response |
| `TRUVAG3_HITL_REDIS_DB` | `6` | Redis database number for HITL data |
| `TRUVAG3_HITL_KEY_PREFIX` | `truvag3:hitl` | Redis key prefix for HITL data |

**Example:**
```bash
# Enable HITL with plan approval for sensitive operations
export TRUVAG3_HITL_ENABLED=true
export TRUVAG3_HITL_SENSITIVE_CAPABILITIES=transfer_funds,delete_account
export TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=get_balance,view_orders
export TRUVAG3_HITL_DEFAULT_TIMEOUT=10m
```

See [ENVIRONMENT_VARIABLES_GUIDE.md](../../docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md) for complete documentation of all framework variables.
