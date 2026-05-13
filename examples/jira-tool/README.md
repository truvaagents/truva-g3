# JIRA Tool

A TruvaG3 tool that provides JIRA Cloud issue management capabilities using the [Atlassian JIRA REST API v3](https://developer.atlassian.com/cloud/jira/platform/rest/v3/). This tool demonstrates the passive tool pattern - it registers capabilities with the service mesh but does not discover other components.

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

This tool provides JIRA issue management capabilities that agents can discover and use. Unlike agents, tools are independent - they only need Redis for service discovery and don't orchestrate other components.

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

#### 5. JIRA API Token

A JIRA API token is **required** for this tool. The tool connects to your Atlassian Cloud instance.

1. Visit [id.atlassian.com/manage-profile/security/api-tokens](https://id.atlassian.com/manage-profile/security/api-tokens)
2. Click "Create API token"
3. Give it a label (e.g., "TruvaG3 JIRA Tool")
4. Copy the generated token

**You'll also need:**
- Your Atlassian instance URL (e.g., `https://mycompany.atlassian.net`)
- Your Atlassian account email

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

The fastest way to get the JIRA tool running:

```bash
cd examples/jira-tool

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**Edit `.env`** with your JIRA credentials:

```bash
nano .env    # or: code .env / vim .env
```

- `JIRA_BASE_URL=https://mycompany.atlassian.net`
- `JIRA_USER_EMAIL=you@example.com`
- `JIRA_API_TOKEN=your-api-token` (Get from [id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens))

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
| **JIRA API** | http://localhost:8366 | JIRA issue management API |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Ensure Infrastructure is Running

The JIRA tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any agent example (e.g., travel-chat-agent)
cd examples/travel-chat-agent
./setup.sh cluster   # Create Kind cluster
./setup.sh infra     # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/jira-tool

# Build Docker image
docker build -t jira-tool:latest .

# Load into Kind
kind load docker-image jira-tool:latest

# Deploy to Kubernetes
kubectl apply -f k8-deployment.yaml

# Verify deployment
kubectl get pods -n truvag3-examples -l app=jira-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/jira-tool-service 8366:80

# Test get issue
curl -X POST http://localhost:8366/api/capabilities/get_issue \
  -H "Content-Type: application/json" \
  -d '{"issue_key": "PROJ-123"}'
```

---

## Features

- **Issue Retrieval** - Get full issue details by key with optional field filtering
- **JQL Search** - Search issues using JIRA Query Language with pagination
- **Issue Creation** - Create bugs, tasks, stories, and epics with full field support
- **Issue Updates** - Modify summary, description, priority, and labels
- **Comments** - Add comments to issues with automatic ADF conversion
- **Workflow Transitions** - Move issues through workflow states (e.g., To Do -> In Progress -> Done)
- **Assignment** - Assign or unassign issues to team members
- **Automatic Service Discovery** - Registers with Redis for agent discovery
- **Distributed Tracing** - Full OpenTelemetry integration with W3C TraceContext

---

## Registered Capabilities

The tool registers these capabilities with the service mesh:

### 1. Get Issue (`get_issue`)

**Endpoint:** `/api/capabilities/get_issue`

Gets a single JIRA issue by key with full details.

**Request:**
```json
{
  "issue_key": "PROJ-123",
  "fields": "summary,status,assignee"
}
```

**Response:**
```json
{
  "key": "PROJ-123",
  "summary": "Login page returns 500 error",
  "status": "In Progress",
  "assignee": "Jane Smith",
  "priority": "High",
  "labels": ["backend", "urgent"],
  "created": "2026-01-15T10:30:00.000+0000",
  "updated": "2026-02-20T14:22:00.000+0000",
  "description": "Steps to reproduce: 1. Go to /login..."
}
```

### 2. Search Issues (`search_issues`)

**Endpoint:** `/api/capabilities/search_issues`

Searches JIRA issues using JQL (JIRA Query Language).

**Request:**
```json
{
  "jql": "project = MYPROJ AND status = 'To Do'",
  "fields": "summary,status,assignee,priority",
  "max_results": 10
}
```

**Response:**
```json
{
  "total": 42,
  "issues": [
    {
      "key": "MYPROJ-101",
      "summary": "Add dark mode support",
      "status": "To Do",
      "assignee": "John Doe",
      "priority": "Medium"
    }
  ]
}
```

### 3. Create Issue (`create_issue`)

**Endpoint:** `/api/capabilities/create_issue`

Creates a new JIRA issue in a project.

**Request:**
```json
{
  "project_key": "PROJ",
  "summary": "Login page returns 500 error",
  "issue_type": "Bug",
  "description": "Steps to reproduce: 1. Go to /login 2. Click submit",
  "priority": "High",
  "labels": "backend,urgent"
}
```

**Response:**
```json
{
  "key": "PROJ-456",
  "id": "10042",
  "self": "https://mycompany.atlassian.net/rest/api/3/issue/10042"
}
```

### 4. Update Issue (`update_issue`)

**Endpoint:** `/api/capabilities/update_issue`

Updates fields on an existing JIRA issue.

**Request:**
```json
{
  "issue_key": "PROJ-123",
  "summary": "Updated issue title",
  "priority": "High",
  "add_labels": "critical,p0",
  "remove_labels": "backlog"
}
```

### 5. Add Comment (`add_comment`)

**Endpoint:** `/api/capabilities/add_comment`

Adds a comment to a JIRA issue.

**Request:**
```json
{
  "issue_key": "PROJ-123",
  "body": "Fixed in commit abc123. Ready for QA."
}
```

**Response:**
```json
{
  "comment_id": "10023",
  "author": "jane.smith@example.com",
  "created": "2026-02-25T09:15:00.000+0000"
}
```

### 6. Transition Issue (`transition_issue`)

**Endpoint:** `/api/capabilities/transition_issue`

Changes a JIRA issue's workflow status. Automatically fetches available transitions and matches by name.

**Request:**
```json
{
  "issue_key": "PROJ-123",
  "transition_name": "In Progress"
}
```

### 7. Assign Issue (`assign_issue`)

**Endpoint:** `/api/capabilities/assign_issue`

Assigns or unassigns a JIRA issue.

**Request:**
```json
{
  "issue_key": "PROJ-123",
  "account_id": "5b10ac8d82e05b22cc7d4ef5"
}
```

---

## Architecture

```
JIRA Tool (Passive)
    |
    +-- Registers capabilities in Redis
    +-- Receives requests from agents
    +-- Authenticates via JIRA Basic Auth (email + API token)
    +-- Calls Atlassian JIRA REST API v3
    +-- Returns standardized responses

Agents (Active)
    |
    +-- Discover JIRA tool via Redis
    +-- Use AI for tool selection
    +-- Generate payloads automatically
    +-- Orchestrate multi-tool workflows
```

### Integration with Agents

Once deployed, the JIRA tool is automatically discovered by agents via Redis. You can manage JIRA issues through natural language:

```bash
# Query through an orchestrating agent
curl -X POST http://localhost:8350/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "What are the open bugs in the PROJ project?",
    "ai_synthesis": true
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `JIRA_BASE_URL` | Atlassian Cloud instance URL | - | Yes |
| `JIRA_USER_EMAIL` | Atlassian account email | - | Yes |
| `JIRA_API_TOKEN` | JIRA API token | - | Yes |
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8366` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (error\|warn\|info\|debug) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (json\|text) | `json` | No |

---

## API Rate Limits

Atlassian Cloud rate limits:

| Limit | Value |
|-------|-------|
| **REST API** | Varies by endpoint (typically generous) |
| **Concurrent requests** | Based on Atlassian plan |

The tool implements:
- HTTP Basic Auth with API token (no OAuth complexity)
- Traced HTTP client for all API calls (OpenTelemetry spans)
- Structured error logging for rate limit tracking
- Graceful error responses on API failures

---

## Project Structure

```
jira-tool/
├── main.go                 # Entry point, framework setup, telemetry init
├── jira_tool.go            # Tool definition, capability registration
├── jira_client.go          # JIRA REST API v3 client (7 endpoints)
├── handlers.go             # HTTP handlers for each capability
├── go.mod                  # Go module definition
├── Dockerfile              # Standalone container image
├── Dockerfile.workspace    # Development build from workspace root
├── k8-deployment.yaml      # Kubernetes manifests
├── setup.sh                # Full lifecycle management script
├── .env.example            # Environment variable template
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

# Should show: truvag3:service:jira-tool-service
```

**2. Authentication errors (401)**

```bash
# Check logs for auth issues
kubectl logs -n truvag3-examples -l app=jira-tool | grep -i "auth\|401"

# Common issues:
# - Invalid API token: Regenerate at https://id.atlassian.com/manage-profile/security/api-tokens
# - Wrong email: Must match the Atlassian account that created the token
# - Wrong base URL: Must be https://your-domain.atlassian.net (no trailing slash)
```

**3. Permission errors (403)**

```bash
# Check logs for permission issues
kubectl logs -n truvag3-examples -l app=jira-tool | grep -i "403\|permission"

# Common issues:
# - API token user doesn't have access to the project
# - Insufficient project permissions for the operation (e.g., create/transition)
# - Issue-level security restrictions
```

**4. JQL search returns no results**

```bash
# Common issues:
# - Invalid project key: Verify the project exists in your JIRA instance
# - Escaped quotes: Use single quotes in JQL (project = 'My Project')
# - Field names: Use JIRA field names, not display names
```

**5. Docker build fails**

```bash
# Ensure Docker is running
docker info
```

**6. Kind cluster not found**

```bash
# List existing clusters
kind get clusters

# Create a new cluster if none exists
kind create cluster --name truvag3-demo
```

### Useful Commands

```bash
# View tool logs
kubectl logs -n truvag3-examples -l app=jira-tool

# Check pod status
kubectl get pods -n truvag3-examples -l app=jira-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/jira-tool-service 8366:80

# Test get issue
curl -X POST http://localhost:8366/api/capabilities/get_issue \
  -H "Content-Type: application/json" \
  -d '{"issue_key": "PROJ-123"}'

# Test search issues
curl -X POST http://localhost:8366/api/capabilities/search_issues \
  -H "Content-Type: application/json" \
  -d '{"jql": "project = PROJ AND status = \"To Do\"", "max_results": 5}'
```

---

## Development

### Local Development

```bash
# Set environment variables
export JIRA_BASE_URL="https://mycompany.atlassian.net"
export JIRA_USER_EMAIL="you@example.com"
export JIRA_API_TOKEN="your-api-token"
export REDIS_URL="redis://localhost:6379"
export PORT=8366

# Run the tool
go run .
```

### Adding New Capabilities

1. Add request/response types in `jira_tool.go`
2. Register capability in `registerCapabilities()`
3. Implement handler in `handlers.go`
4. Add JIRA client method in `jira_client.go` if needed

---

## Related Examples

- [devops-chat-agent](../devops-chat-agent/) - DevOps chat agent that can use this tool
- [agent-with-orchestration](../agent-with-orchestration/) - Orchestration example
- [stock-market-tool](../stock-market-tool/) - Stock market data tool
- [hotel-tool](../hotel-tool/) - Hotel search tool (LiteAPI)
- [web-search-tool](../web-search-tool/) - Web search tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
