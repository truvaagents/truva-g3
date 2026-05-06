# DevOps Tool

A TruvaG3 tool that provides Kubernetes cluster management capabilities via kubectl. This tool demonstrates the passive tool pattern with RBAC-secured in-cluster access — it requires **no external API keys** and uses the pod's ServiceAccount for Kubernetes API access.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Capabilities](#capabilities)
- [API Reference](#api-reference)
  - [Test with curl](#test-with-curl)
- [Security](#security)
- [Configuration](#configuration)
- [RBAC](#rbac)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

This tool provides Kubernetes management capabilities that agents can discover and use. Unlike agents, tools are independent — they only need Redis for service discovery and don't orchestrate other components.

> **Note:** No API key is required. This tool uses in-cluster kubectl with a ServiceAccount for Kubernetes API access.

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
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

# Install Docker Engine
sudo apt-get update
sudo apt-get install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# Verify
docker --version
```

</details>

#### 2. Kind (Kubernetes in Docker)

Kind creates a local Kubernetes cluster using Docker containers.

```bash
# macOS (Homebrew)
brew install kind

# Linux / WSL
curl -Lo ./kind https://kind.sigs.k8s.io/dl/latest/kind-linux-amd64
chmod +x ./kind
sudo mv ./kind /usr/local/bin/kind

# Verify
kind --version
```

#### 3. kubectl

```bash
# macOS (Homebrew)
brew install kubectl

# Linux
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x kubectl
sudo mv kubectl /usr/local/bin/

# Verify
kubectl version --client
```

#### 4. Go 1.26+

```bash
# macOS (Homebrew)
brew install go

# Linux — see https://go.dev/dl/

# Verify
go version
```

### Verify All Prerequisites

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

The fastest way to get the devops tool running:

```bash
cd examples/devops-tool

# 1. Create .env from the example file (safe — won't overwrite existing)
[ ! -f .env ] && cp .env.example .env

# 2. Deploy to Kubernetes (requires cluster and Redis to be running)
./setup.sh deploy
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image
2. Loads it into the Kind cluster
3. Creates ConfigMap from `.env` values
4. Deploys RBAC resources (ServiceAccount, ClusterRole, ClusterRoleBinding)
5. Deploys the tool to Kubernetes
6. Registers capabilities with Redis for agent discovery

Once complete, port-forward to access locally:

```bash
# Forward to localhost:8347
./setup.sh forward

# Or manually:
kubectl port-forward -n truvag3-examples svc/devops-tool-service 8347:80
```

| Service | URL | Description |
|---------|-----|-------------|
| **DevOps API** | http://localhost:8347 | Kubernetes management API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The devops tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/devops-tool

# Build Docker image
docker build -t devops-tool:latest .

# Load into Kind
kind load docker-image devops-tool:latest --name truvag3-demo-$(whoami)

# Deploy to Kubernetes (includes RBAC)
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=devops-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/devops-tool-service 8347:80

# Health check
curl http://localhost:8347/health

# List capabilities
curl http://localhost:8347/api/capabilities
```

---

## Features

- **Cluster Status** — Get cluster health, node conditions, and component status
- **Pod Management** — List pods with namespace, label, and field-selector filtering
- **Log Retrieval** — Fetch pod logs with tail lines, container selection, and previous instance support
- **Resource Description** — Full `kubectl describe` for any resource type
- **Deployment Scaling** — Scale deployments with validated replica bounds (0-10)
- **Rolling Restarts** — Trigger rollout restarts for deployments
- **Arbitrary kubectl** — Flexible kubectl access with only `delete` blocked
- **Automatic Service Discovery** — Registers with Redis for agent discovery
- **Distributed Tracing** — Full OpenTelemetry integration with trace propagation
- **Structured Logging** — JSON logs with operation, request_id, and trace context

---

## Capabilities

### 1. Get Cluster Status (`get_cluster_status`)

**Endpoint:** `POST /api/capabilities/get_cluster_status`

Returns cluster info, node details, and component status.

**Request:**
```json
{}
```

**Request (without nodes):**
```json
{
  "include_nodes": false
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "command": "kubectl cluster-info",
    "stdout": "Kubernetes control plane is running at https://10.96.0.1:443\nCoreDNS is running at ...\n---\nNodes:\nNAME                                       STATUS   ROLES           AGE   VERSION   ...\ntruvag3-demo-user-control-plane             Ready    control-plane   10d   v1.32.2   ...\n---\nComponent Status:\nscheduler            Healthy   ok\ncontroller-manager   Healthy   ok\netcd-0               Healthy   ok",
    "stderr": "",
    "exit_code": 0,
    "duration_ms": 150
  }
}
```

### 2. Get Pods (`get_pods`)

**Endpoint:** `POST /api/capabilities/get_pods`

Lists pods with optional filtering by namespace, labels, and field selectors.

**Request (all pods in a namespace):**
```json
{
  "namespace": "truvag3-examples"
}
```

**Request (filter by label):**
```json
{
  "namespace": "truvag3-examples",
  "label_filter": "app=weather-tool-v2"
}
```

**Request (running pods only):**
```json
{
  "field_filter": "status.phase=Running"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "command": "kubectl get pods -n truvag3-examples -o wide",
    "stdout": "NAME                               READY   STATUS    RESTARTS   AGE   IP           NODE\ndevops-tool-5bd78f7fb-b47tx        1/1     Running   0          5m    10.244.0.5   ...\nweather-tool-v2-7c9f8d6b4d-k2m9   1/1     Running   0          2d    10.244.0.8   ...",
    "stderr": "",
    "exit_code": 0,
    "duration_ms": 85
  }
}
```

### 3. Get Pod Logs (`get_pod_logs`)

**Endpoint:** `POST /api/capabilities/get_pod_logs`

Retrieves logs from a specific pod. Requires `pod_name`.

**Request:**
```json
{
  "pod_name": "devops-tool-5bd78f7fb-b47tx",
  "tail_lines": 10
}
```

**Request (specific container and namespace):**
```json
{
  "pod_name": "multi-container-pod-abc123",
  "namespace": "default",
  "container": "sidecar",
  "tail_lines": 50
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "command": "kubectl logs devops-tool-5bd78f7fb-b47tx -n truvag3-examples --tail 10",
    "stdout": "{\"level\":\"info\",\"msg\":\"Processing get_cluster_status request\",...}\n{\"level\":\"info\",\"msg\":\"get_cluster_status completed\",...}",
    "stderr": "",
    "exit_code": 0,
    "duration_ms": 45
  }
}
```

### 4. Describe Resource (`describe_resource`)

**Endpoint:** `POST /api/capabilities/describe_resource`

Describes any Kubernetes resource. Requires `resource_type` and `resource_name`.

**Request:**
```json
{
  "resource_type": "deployment",
  "resource_name": "devops-tool"
}
```

**Request (describe a node — cluster-scoped, no namespace):**
```json
{
  "resource_type": "node",
  "resource_name": "truvag3-demo-user-control-plane"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "command": "kubectl describe deployment devops-tool -n truvag3-examples",
    "stdout": "Name:                   devops-tool\nNamespace:              truvag3-examples\nLabels:                 app=devops-tool\n                        component=tool\nReplicas:               1 desired | 1 updated | 1 total | 1 available | 0 unavailable\n...",
    "stderr": "",
    "exit_code": 0,
    "duration_ms": 60
  }
}
```

### 5. Scale Deployment (`scale_deployment`)

**Endpoint:** `POST /api/capabilities/scale_deployment`

Scales a deployment. Requires `deployment_name` and `replicas` (0-10).

**Request:**
```json
{
  "deployment_name": "weather-tool-v2",
  "replicas": 2
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "command": "kubectl scale deployment weather-tool-v2 --replicas 2 -n truvag3-examples",
    "stdout": "deployment.apps/weather-tool-v2 scaled",
    "stderr": "",
    "exit_code": 0,
    "duration_ms": 120
  }
}
```

### 6. Rollout Restart (`rollout_restart`)

**Endpoint:** `POST /api/capabilities/rollout_restart`

Performs a rolling restart of a deployment. Requires `deployment_name`.

**Request:**
```json
{
  "deployment_name": "weather-tool-v2"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "command": "kubectl rollout restart deployment/weather-tool-v2 -n truvag3-examples",
    "stdout": "deployment.apps/weather-tool-v2 restarted",
    "stderr": "",
    "exit_code": 0,
    "duration_ms": 95
  }
}
```

### 7. Kubectl Command (`kubectl_command`)

**Endpoint:** `POST /api/capabilities/kubectl_command`

Executes an arbitrary kubectl command. Requires `args`. Blocked commands return `FORBIDDEN_COMMAND`.

**Request:**
```json
{
  "args": "get nodes -o wide"
}
```

**Request (with timeout and namespace override):**
```json
{
  "args": "get events --sort-by=.lastTimestamp",
  "timeout": 60,
  "namespace": "truvag3-examples"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "command": "kubectl get nodes -o wide",
    "stdout": "NAME                                       STATUS   ROLES           AGE   VERSION   INTERNAL-IP   ...\ntruvag3-demo-user-control-plane             Ready    control-plane   10d   v1.32.2   172.18.0.2    ...",
    "stderr": "",
    "exit_code": 0,
    "duration_ms": 55
  }
}
```

**Blocked command response:**
```json
{
  "success": false,
  "error": {
    "code": "FORBIDDEN_COMMAND",
    "message": "kubectl delete is not allowed — delete operations are blocked to prevent data loss",
    "retryable": false
  }
}
```

---

## API Reference

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/api/capabilities` | GET | List all capabilities with input schemas |
| `/api/capabilities/get_cluster_status` | POST | Cluster status, nodes, components |
| `/api/capabilities/get_cluster_status/schema` | GET | Input schema |
| `/api/capabilities/get_pods` | POST | List pods with filtering |
| `/api/capabilities/get_pods/schema` | GET | Input schema |
| `/api/capabilities/get_pod_logs` | POST | Pod log retrieval |
| `/api/capabilities/get_pod_logs/schema` | GET | Input schema |
| `/api/capabilities/describe_resource` | POST | Describe any K8s resource |
| `/api/capabilities/describe_resource/schema` | GET | Input schema |
| `/api/capabilities/scale_deployment` | POST | Scale a deployment (0-10 replicas) |
| `/api/capabilities/scale_deployment/schema` | GET | Input schema |
| `/api/capabilities/rollout_restart` | POST | Rolling restart a deployment |
| `/api/capabilities/rollout_restart/schema` | GET | Input schema |
| `/api/capabilities/kubectl_command` | POST | Arbitrary kubectl command |
| `/api/capabilities/kubectl_command/schema` | GET | Input schema |

### Test with curl

After deploying and port-forwarding (`./setup.sh forward` or `kubectl port-forward -n truvag3-examples svc/devops-tool-service 8347:80`):

```bash
# 1. Health check
curl -s http://localhost:8347/health | jq .

# 2. List all capabilities (verify all 7 are registered)
curl -s http://localhost:8347/api/capabilities | jq .

# 3. Get cluster status
curl -s -X POST http://localhost:8347/api/capabilities/get_cluster_status \
  -H "Content-Type: application/json" \
  -d '{}' | jq .

# 4. Get pods in truvag3-examples namespace
curl -s -X POST http://localhost:8347/api/capabilities/get_pods \
  -H "Content-Type: application/json" \
  -d '{"namespace": "truvag3-examples"}' | jq .

# 5. Get pods filtered by label
curl -s -X POST http://localhost:8347/api/capabilities/get_pods \
  -H "Content-Type: application/json" \
  -d '{"namespace": "truvag3-examples", "label_filter": "app=devops-tool"}' | jq .

# 6. Run a kubectl command (get nodes)
curl -s -X POST http://localhost:8347/api/capabilities/kubectl_command \
  -H "Content-Type: application/json" \
  -d '{"args": "get nodes -o wide"}' | jq .

# 7. Describe a deployment
curl -s -X POST http://localhost:8347/api/capabilities/describe_resource \
  -H "Content-Type: application/json" \
  -d '{"resource_type": "deployment", "resource_name": "devops-tool"}' | jq .

# 8. Get pod logs (replace pod name with actual pod name from step 4)
curl -s -X POST http://localhost:8347/api/capabilities/get_pod_logs \
  -H "Content-Type: application/json" \
  -d '{"pod_name": "devops-tool-REPLACE-WITH-ACTUAL-POD", "tail_lines": 5}' | jq .

# 9. Scale a deployment (scales weather-tool-v2 to 2 replicas)
curl -s -X POST http://localhost:8347/api/capabilities/scale_deployment \
  -H "Content-Type: application/json" \
  -d '{"deployment_name": "weather-tool-v2", "replicas": 2}' | jq .

# 10. Rollout restart a deployment
curl -s -X POST http://localhost:8347/api/capabilities/rollout_restart \
  -H "Content-Type: application/json" \
  -d '{"deployment_name": "weather-tool-v2"}' | jq .

# 11. Verify blocked command (should return FORBIDDEN_COMMAND)
curl -s -X POST http://localhost:8347/api/capabilities/kubectl_command \
  -H "Content-Type: application/json" \
  -d '{"args": "delete pod some-pod"}' | jq .

# 12. Scale back to original (cleanup after test)
curl -s -X POST http://localhost:8347/api/capabilities/scale_deployment \
  -H "Content-Type: application/json" \
  -d '{"deployment_name": "weather-tool-v2", "replicas": 1}' | jq .
```

**Automated test (all capabilities):**

```bash
./setup.sh test
```

---

## Security

### Blocked Commands

The `kubectl_command` capability blocks only destructive commands that could cause data loss. All other kubectl subcommands are allowed, with RBAC providing the underlying access control:

| Blocked Subcommand | Reason |
|--------------------|--------|
| `delete` | Could cause data loss |

All other commands (including `apply`, `create`, `exec`, `scale`, `rollout`, etc.) are permitted. The tool's RBAC ServiceAccount limits what can actually succeed — for example, write operations will only work on resources the ServiceAccount has permission to modify.

---

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_URL` | (required) | Redis connection URL for service discovery |
| `PORT` | `8347` | HTTP server port |
| `NAMESPACE` | `truvag3-examples` | Kubernetes namespace for service discovery |
| `DEV_MODE` | `false` | Enable development mode (detailed logging) |
| `TRUVAG3_LOG_LEVEL` | `info` | Log level: debug, info, warn, error |
| `APP_ENV` | `development` | Environment: development, staging, production |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | - | OpenTelemetry collector endpoint |

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
# Edit .env if needed (defaults work for local development)
```

---

## RBAC

This tool uses a dedicated ServiceAccount with a ClusterRole configured for broad operational access on local/testing clusters.

**Read access (cluster-wide):**
- Core: pods, pods/log, pods/status, services, endpoints, configmaps, events, namespaces, nodes, PVCs, PVs, resource quotas, limit ranges, serviceaccounts, component statuses
- Apps: deployments, replicasets, statefulsets, daemonsets
- Batch: jobs, cronjobs
- Networking: ingresses, network policies
- Autoscaling: horizontal pod autoscalers
- Metrics: pods, nodes (for `kubectl top`)
- Storage: storageclasses, volumeattachments, CSI resources
- RBAC: roles, rolebindings, clusterroles, clusterrolebindings (audit only)
- Policy: pod disruption budgets, pod security policies
- Discovery: endpoint slices, leases
- CRDs: custom resource definitions
- API discovery: non-resource URLs (`/api`, `/apis`, `/version`, etc.)

**Write access:**
- Core: create/update/patch on pods, services, configmaps, endpoints, serviceaccounts, PVCs, replication controllers
- Pod operations: exec, port-forward, attach, eviction (for `kubectl exec`, `kubectl cp`, `kubectl drain`)
- Node operations: patch/update (for cordon, uncordon, taint)
- Apps: full CRUD on deployments, statefulsets, daemonsets, replicasets + scale subresources + rollback
- Batch: full CRUD on jobs, cronjobs
- Networking: full CRUD on ingresses, network policies
- Autoscaling: full CRUD on HPAs
- Auth: selfsubjectaccessreviews (for `kubectl auth can-i`)

**Explicitly excluded:**
- **Secrets** — no read or write access to prevent exposure of sensitive data

The RBAC resources are defined in `k8-deployment.yaml` and deployed automatically by `./setup.sh deploy`.

---

## Project Structure

```
devops-tool/
  main.go              # Entry point, framework setup, telemetry init
  devops_tool.go       # Tool struct, request/response types, capability registration
  handlers.go          # HTTP handlers for all 7 capabilities
  kubectl_executor.go  # kubectl execution, command validation, blocked list
  go.mod               # Go module dependencies
  go.sum               # Dependency checksums
  Dockerfile           # Production container image
  Dockerfile.workspace # Go workspace-aware build
  k8-deployment.yaml   # Kubernetes manifests (RBAC + Service + Deployment)
  setup.sh             # Lifecycle management script
  .env.example         # Environment variable template
  README.md            # This file
```

---

## Distributed Tracing

This tool uses `otelhttp` to propagate trace context. When called from an orchestrator:

```
orchestrator (parent span)
  └── devops-tool (child span)
        └── kubectl execution (span event)
```

All handlers emit span events with `request_id`, and kubectl executions are tracked with `calling_kubectl` span events. Traces can be viewed in Jaeger at http://localhost:16686 when running with the full infrastructure stack.

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

**3. Connection refused on port 8347**

Check if the pod is running and port forwarding is active:
```bash
kubectl get pods -n truvag3-examples -l app=devops-tool
kubectl port-forward -n truvag3-examples svc/devops-tool-service 8347:80
```

**4. "FORBIDDEN_COMMAND" error**

Only `delete` is blocked via `kubectl_command`. If you see this error, use a different approach:
```bash
# delete is blocked — use kubectl directly if you truly need it:
kubectl delete pod some-pod -n truvag3-examples
```

**5. kubectl permissions error**

The tool's ServiceAccount may lack permissions. Check RBAC:
```bash
kubectl auth can-i --list --as=system:serviceaccount:truvag3-examples:devops-tool-sa
```

### Useful Commands

```bash
# View tool logs
kubectl logs -n truvag3-examples -l app=devops-tool -f

# Check pod status
kubectl get pods -n truvag3-examples -l app=devops-tool

# Restart the tool
kubectl rollout restart -n truvag3-examples deployment/devops-tool

# Full cleanup
./setup.sh clean
```

---

## Related Examples

- [agent-example](../agent-example/) — Agent that discovers and uses tools
- [agent-with-orchestration](../agent-with-orchestration/) — Orchestration example
- [weather-tool-v2](../weather-tool-v2/) — Another passive tool example (weather data)
- [stock-market-tool](../stock-market-tool/) — Tool with external API integration

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
