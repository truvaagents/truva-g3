# Event-Driven Agent

A production-ready event-driven incident response agent that receives Prometheus AlertManager webhooks and orchestrates autonomous investigation and remediation using AI-driven DAG planning. This example demonstrates the event-driven architecture pattern in the TruvaG3 framework, including async event queues, severity-based routing, deduplication, and human-in-the-loop approval for critical write operations.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Required Tools and Agents](#required-tools-and-agents)
- [Overview](#overview)
- [What You'll Learn](#what-youll-learn)
- [Architecture](#architecture)
  - [How It Works](#how-it-works)
  - [Data Isolation](#data-isolation)
- [Event Processing Pipeline](#event-processing-pipeline)
- [Deployment Modes](#deployment-modes)
- [AlertManager Integration](#alertmanager-integration)
- [API Reference](#api-reference)
- [Human-in-the-Loop (HITL)](#human-in-the-loop-hitl)
- [Configuration Reference](#configuration-reference)
  - [OpenAI-Compatible Providers](#openai-compatible-providers)
- [E2E Stress Test (HITL Demo)](#e2e-stress-test-hitl-demo)
  - [Test Scenario](#test-scenario)
  - [Running the Stress Test](#running-the-stress-test)
  - [Dedup and Flood Prevention](#dedup-and-flood-prevention)
- [Telemetry](#telemetry)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)
- [Related Examples](#related-examples)

---

## How to Run This Example

Running this example locally is the best way to understand how the TruvaG3 framework supports event-driven agent patterns with AlertManager webhook integration, async task processing, and AI-powered incident investigation. Follow the steps below to get this example running.

### Prerequisites

This agent needs the standard TruvaG3 local-dev toolchain (Docker, Kind, kubectl, Go ≥ 1.26) plus an AI provider API key. If you haven't set these up yet, follow **[Prerequisites in GETTING_STARTED.md](../../GETTING_STARTED.md#1-prerequisites)** — it has full macOS/Linux install instructions, a Podman alternative, a verification script, and an optional Kubernetes UI section.

**Quick install (macOS):**

```bash
brew install go kind kubectl
brew install --cask docker
```

**Quick install (Linux):** see [GETTING_STARTED.md §1](../../GETTING_STARTED.md#1-prerequisites) — install steps are distribution-specific (apt/dnf/snap/manual binary).

**Verify:**

```bash
docker --version && kind --version && kubectl version --client && go version
```

#### AI Provider API Key (Required)

At least one AI provider API key is required for the agent's intelligent orchestration and analysis.

| Provider | Get API Key | Notes |
|----------|-------------|-------|
| **OpenAI** | [platform.openai.com/api-keys](https://platform.openai.com/api-keys) | GPT-4o recommended |
| **Anthropic** | [console.anthropic.com](https://console.anthropic.com/) | Claude models |
| **Groq** | [console.groq.com/keys](https://console.groq.com/keys) | Fast inference, free tier |

The agent auto-detects available providers and fails over between them — configure multiple keys for automatic failover.

> **Important:** This example deploys the agent along with Prometheus AlertManager for real webhook-driven alert processing. The setup script handles all infrastructure deployment automatically, including AlertManager configuration.

### Quick Start (Recommended)

The fastest way to get everything running in your local:

```bash
cd examples/event-driven-agent

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**Stop here** - Open `.env` in your editor and configure your API key(s):

```bash
# Open .env in your preferred editor
nano .env    # or: code .env / vim .env
```

At minimum, uncomment and set ONE of these in your `.env` file:
- `OPENAI_API_KEY=sk-your-key`
- `ANTHROPIC_API_KEY=sk-ant-your-key`
- `GROQ_API_KEY=gsk_your-key`

> **Note:** Multiple providers enable automatic failover.

```bash
# 2. Run the automated setup script (full deployment)
./setup.sh full-deploy
```

**What `./setup.sh full-deploy` does:**
1. Creates a Kind Kubernetes cluster with proper port mappings
2. Deploys shared monitoring infrastructure (Redis, OTEL Collector, Prometheus, Jaeger, Grafana)
3. Deploys Prometheus AlertManager with routing rules for TruvaG3 alerts
4. Builds and deploys the event-driven agent
5. Deploys the product-catalog-api mock service (for E2E HITL testing — see [E2E Stress Test](#e2e-stress-test-hitl-demo))
6. Sets up port forwarding automatically

Once complete, access the services at:

| Service | URL | Description |
|---------|-----|-------------|
| **Agent API** | http://localhost:8372 | Event-driven agent REST API |
| **AlertManager** | http://localhost:9093 | AlertManager UI |
| **Grafana** | http://localhost:3000 | Metrics dashboard (admin/admin) |
| **Prometheus** | http://localhost:9090 | Metrics queries |
| **Jaeger** | http://localhost:16686 | Distributed tracing |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Configure Environment

```bash
cd examples/event-driven-agent

# Create .env from example (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
# Edit .env and uncomment/set your AI provider key(s)
```

#### Step 2: Ensure Infrastructure is Running

```bash
cd examples/event-driven-agent

./setup.sh cluster   # Create Kind cluster (skip if already up)
./setup.sh infra     # Deploy Redis + OTEL Collector + Prometheus + Jaeger + Grafana + AlertManager (skip if already up)
```

> Skip these if you've already brought up the cluster + infra from another example — they're shared across all TruvaG3 examples in the `truvag3-examples` namespace.

This brings up the shared infrastructure components:
- **Redis** - Service discovery, event queue, and deduplication
- **OTEL Collector** - Telemetry aggregation
- **Prometheus** - Metrics storage
- **Jaeger** - Distributed tracing
- **Grafana** - Visualization dashboards

#### Step 3: Build and Deploy the Agent

```bash
cd examples/event-driven-agent

# Build Docker image only (does not deploy)
./setup.sh docker-build

# Full deploy: build + load into Kind + create namespace + ConfigMap from .env + apply manifest (includes AlertManager)
./setup.sh deploy
```

#### Step 4: Verify Deployment

```bash
./setup.sh status   # Check pod / service status
./setup.sh logs     # Stream agent logs
```

#### Local Development (Without Kubernetes)

If you prefer to run without Kubernetes (ensure `.env` is configured and Redis is running):

```bash
cd examples/event-driven-agent

# Ensure Redis is running locally
export REDIS_URL=redis://localhost:6379
export APP_ENV=development
go run .
```

Test the agent:
```bash
# Trigger a manual alert investigation
curl -X POST http://localhost:8372/trigger \
  -H "Content-Type: application/json" \
  -d '{
    "alertname": "HighCPU",
    "severity": "critical",
    "instance": "web-server-01",
    "summary": "CPU usage above 90% for 5 minutes"
  }'
```

---

## Required Tools and Agents

The event-driven-agent's AI investigation calls capabilities exposed by other tool services discovered via Redis. **These tools must be running for the agent to investigate alerts end-to-end.**

### Core Tools (used by the shipped HITL stress test)

These tools are referenced directly by the E2E Stress Test scenario (latency alert → rollout_restart). Without them, the AI investigation will fall back to a degraded "manual investigation required" response.

| Tool | Purpose | Port | Documentation |
|------|---------|------|---------------|
| **devops-tool** | `kubectl` operations: get/describe pods, deployments, services, nodes; `rollout_restart`, `scale_deployment`, `delete_pod` (HITL-gated) | 8347 | [README](../devops-tool/README.md) |
| **prometheus-query-tool** | `query_metrics` — PromQL queries against the in-cluster Prometheus (used during AI investigation to confirm alert conditions) | 8371 | [README](../prometheus-query-tool/README.md) |
| **devops-observability-tool** | `query_logs` (Loki) and `find_traces` / `get_trace` (Jaeger) — for log/trace inspection during investigation | 8378 | [README](../devops-observability-tool/README.md) |
| **slack-tool** | `send_message` — used **both** directly by the webhook receiver for `warning`-severity alerts ([webhook_receiver.go:293](webhook_receiver.go#L293)) and by the AI investigation for incident notifications | 8373 | [README](../slack-tool/README.md) |

### Optional Tools

These extend the agent's investigation toolkit but aren't required for the shipped stress test.

| Tool | Purpose | Documentation |
|------|---------|---------------|
| **jira-tool** | `create_ticket`, `add_comment`, `search_issues` — for incident documentation. Not HITL-gated by default | [README](../jira-tool/README.md) |
| **system-utilities-tool** | `get_current_time`, `sleep`, `execute_command` — used by some AI plans for inspection delays | [README](../system-utilities-tool/README.md) |

### Mock Service (for E2E demo)

| Service | Purpose | Documentation |
|---------|---------|---------------|
| **product-catalog-api** | Simulates a production microservice with `/api/v1/products` + `/api/v1/categories` endpoints. Includes `/admin/simulate/degrade` and `/admin/simulate/recover` for triggering the alert pipeline. Deployed automatically by `./setup.sh full-deploy` and driven by `./setup.sh mock-service ...` | [mock-services README](../mock-services/README.md) · [source](../mock-services/product-catalog-api/) |

### Related Agents

| Agent | Purpose | Documentation |
|-------|---------|---------------|
| **devops-chat-agent** | Conversational sibling — same HITL infrastructure and tool family, but driven by a chat UI instead of webhooks | [README](../devops-chat-agent/README.md) |
| **agent-with-human-approval** | Minimal HITL demo agent (synchronous chat) — useful for understanding the checkpoint/resume flow in isolation | [README](../agent-with-human-approval/README.md) |
| **agent-with-orchestration** | Basic orchestration example without HITL or event-driven patterns | [README](../agent-with-orchestration/README.md) |
| **agent-with-telemetry** | Telemetry-focused agent | [README](../agent-with-telemetry/README.md) |

### Deploying Tools

Each tool has its own `setup.sh` script with similar commands:

```bash
# Example: Deploy devops-tool
cd examples/devops-tool
./setup.sh deploy       # Deploy to Kubernetes
./setup.sh run          # Run locally
./setup.sh help         # See all options
```

---

## Overview

This example demonstrates an event-driven agent pattern that is fundamentally different from request-response agents:

- **Webhook-Driven**: Receives Prometheus AlertManager webhooks instead of user-initiated requests
- **Async Event Queue**: Critical alerts are enqueued in Redis and processed by background workers
- **Severity-Based Routing**: Critical alerts trigger AI investigation, warnings send Slack notifications, info alerts are logged
- **Fingerprint Deduplication**: Prevents duplicate investigations for the same alert within a configurable TTL window
- **AI-Powered Investigation**: Uses the TruvaG3 orchestration module with DAG planning to investigate incidents
- **Human-in-the-Loop**: Write operations (pod restarts, scaling, deletions) require human approval before execution
- **Multi-Mode Deployment**: Supports embedded (single process), split API/worker, or standalone worker modes

> **Scope**: This example focuses on **event-driven patterns and incident response**. For basic agent patterns, see [agent-example](../agent-example). For telemetry-focused patterns, see [agent-with-telemetry](../agent-with-telemetry/).

---

## What You'll Learn

- How to build an event-driven agent that processes AlertManager webhooks
- Implementing async event queues with Redis (LPUSH/BRPOP) for reliable alert processing
- Severity-based routing: critical (AI investigation), warning (Slack notification), info (log only)
- Fingerprint-based deduplication to avoid redundant investigations
- Configuring the AI orchestrator with incident-response domain prompts
- Human-in-the-loop approval workflows for write operations (pod restarts, scaling)
- Deploying agents in different modes (embedded, API-only, worker-only)
- Integrating Prometheus AlertManager with a TruvaG3 agent via webhook receivers

---

## Architecture

```
                           AlertManager Webhook
                                   |
                                   v
┌──────────────────────────────────────────────────────────────┐
│  Event-Driven Agent (Port 8372)                              │
│                                                              │
│  ┌─────────────────┐    ┌──────────────────────────────────┐ │
│  │ Webhook Receiver │    │  Deterministic Pipeline          │ │
│  │ /webhook/        │───>│  parse -> severity -> dedup      │ │
│  │   alertmanager   │    │      |          |         |      │ │
│  └─────────────────┘    │  critical   warning     info     │ │
│                          │      |          |         |      │ │
│  ┌─────────────────┐    │  enqueue    Slack      log       │ │
│  │ Manual Trigger   │    │      |     notify     only      │ │
│  │ /trigger         │───>│      v                          │ │
│  └─────────────────┘    └──────|───────────────────────────┘ │
│                                 |                             │
│                                 v                             │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │  Redis Alert Queue (LPUSH/BRPOP)                         │ │
│  └───────────────────────────|──────────────────────────────┘ │
│                              |                                │
│                              v                                │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │  Worker Pool (3 workers)                                  │ │
│  │  ┌────────────────────────────────────────────────────┐  │ │
│  │  │  AI Pipeline                                       │  │ │
│  │  │  context enrichment -> orchestrator -> HITL        │  │ │
│  │  │       |                    |              |        │  │ │
│  │  │  build query        DAG planning    human approval │  │ │
│  │  │                     tool execution  (write ops)    │  │ │
│  │  │                     AI synthesis                   │  │ │
│  │  └────────────────────────────────────────────────────┘  │ │
│  └──────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
         |                    |                    |
         v                    v                    v
  ┌──────────┐        ┌──────────┐         ┌──────────┐
  │  Redis   │        │  Tools   │         │  Slack   │
  │  (dedup, │        │  (DevOps,│         │  Webhook │
  │   queue) │        │   Jira,  │         │          │
  └──────────┘        │  Metrics)│         └──────────┘
                      └──────────┘
```

### How It Works

1. **AlertManager fires a webhook** (or a developer hits `/trigger` manually) to the agent's API endpoint
2. **Webhook receiver parses** the AlertManager payload and runs the **deterministic pipeline** (fast, predictable, synchronous):
   - Skip `resolved` alerts (informational only)
   - Severity route: `critical` → enqueue, `warning` → Slack notify directly, `info` → log only
   - Dedup check via Redis `SET NX` with fingerprint key + TTL (`TRUVAG3_EVENT_DEDUP_TTL`, default 5 min)
3. **Critical alerts are `LPUSH`-ed** onto the Redis queue and the HTTP response returns immediately (the caller doesn't wait for investigation)
4. **Worker pool** (`WORKER_COUNT` background goroutines, default 3) **`BRPOP`-s** alerts from the queue
5. **Context enrichment** builds a natural-language query from alert labels and annotations
6. **AI orchestrator** plans a DAG of tool calls (typically: `query_metrics` → `get_pods` → `get_pod_logs` → `rollout_restart`)
7. **HITL gate** intercepts any step whose capability is in `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` (`rollout_restart`, `scale_deployment`, `delete_pod` by default) and pauses execution. A checkpoint is persisted to Redis (DB 6) and an `expired_approved` / `approved` / `rejected` decision drives resumption via `/hitl/resume/{id}`
8. **AI synthesizes** a natural-language incident report and the worker releases the dedup key so the same alert can be re-investigated if it fires again
9. **Side effects** (`create_ticket`, `send_message`, etc.) run as part of the DAG. By default these are **not** HITL-gated — only destructive K8s writes are

See [Event Processing Pipeline](#event-processing-pipeline) for the per-stage breakdown.

### Data Isolation

| Data Type | Backend | Location |
|-----------|---------|----------|
| Service Registry | Redis | DB 0, keys `truvag3:services:*` |
| Alert Event Queue | Redis | DB 0, list key `truvag3:event:alert_queue` ([queue_consumer.go:36](queue_consumer.go#L36)), LPUSH'd by the webhook receiver, BRPOP'd by workers |
| Alert Dedup Fingerprints | Redis | DB 0, keys `truvag3:event:dedup:<fingerprint>` ([webhook_receiver.go:195](webhook_receiver.go#L195)) with TTL from `TRUVAG3_EVENT_DEDUP_TTL` (default 5 min) |
| Shared Agent Memory — Episodic Events | Redis | DB 0, keys `truvag3:memory:infrastructure:event:<uuid>` (per-record string) and stream `truvag3:memory:infrastructure:events:stream`. Domain (`infrastructure`) is set via `TRUVAG3_AGENT_DOMAIN`. Shared with sister agents (e.g., devops-chat-agent) in the same domain |
| Shared Agent Memory — Entity Records | Redis | DB 0, keys `truvag3:memory:infrastructure:entity:<type>:<id>` — per-entity rollups built from events |
| Activity Coordination Signals | Redis | DB 0 (separate top-level prefix), keys `truvag3:activity:infrastructure:{requestID}` with 5-min TTL (`TRUVAG3_ACTIVITY_SIGNAL_TTL`). Each in-flight investigation announces itself here; other agents in the domain see active signals in their `<agent_coordination>` planning context — **advisory, not blocking** |
| HITL Checkpoints & Commands | Redis | DB 6 (override via `TRUVAG3_HITL_REDIS_DB`), keys `truvag3:hitl:<agent-name>:*` |
| LLM Debug Records | Redis | DB 7, keys `llm_debug:*` (active when `TRUVAG3_LLM_DEBUG_ENABLED=true`) |
| Execution Debug DAGs | Redis | DB 8 (when `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true`) |

---

## Event Processing Pipeline

The agent uses a two-stage pipeline that separates deterministic routing from AI-driven investigation:

### Stage 1: Deterministic Pipeline (Webhook Receiver)

This stage is fast, predictable, and handles all incoming alerts synchronously:

1. **Parse**: Decode the AlertManager JSON payload
2. **Filter**: Skip `resolved` alerts (informational only)
3. **Severity Route**:
   - `critical` -- proceed to dedup and enqueue
   - `warning` -- send Slack notification directly (no AI)
   - `info` -- log only
4. **Dedup Check**: Redis `SET NX` with fingerprint key and configurable TTL (default 5 minutes)
5. **Enqueue**: `LPUSH` the serialized alert onto the Redis queue

### Stage 2: AI Pipeline (Worker Pool)

Background workers `BRPOP` alerts from the queue and run AI-driven investigation:

1. **Context Enrichment**: Build a natural language query from alert labels and annotations
2. **Orchestrator**: The AI planner creates a DAG of tool calls (check metrics, get logs, check pod status)
3. **Tool Execution**: Execute the DAG steps, calling discovered tools in the registry
4. **HITL Gate**: Write operations (restart, scale, delete) pause for human approval
5. **Synthesis**: AI summarizes findings and remediation actions
6. **Cleanup**: Remove the dedup key so the alert can be re-investigated if it fires again

---

## Deployment Modes

The agent supports three deployment modes controlled by the `TRUVAG3_MODE` environment variable. All modes use the same Docker image — the mode is determined at runtime.

| Mode | `TRUVAG3_MODE` | Description | Use Case |
|------|---------------|-------------|----------|
| **Embedded** | `""` (empty/unset) | API server + workers in one process | Local development, simple deployments |
| **API** | `"api"` | HTTP server only, no workers | Scale API and workers independently |
| **Worker** | `"worker"` | Workers only, minimal health endpoint | Scale workers horizontally |

### Embedded Mode (Default)

Best for local development. Runs the HTTP server and worker pool in the same process:

```bash
./setup.sh deploy    # or: TRUVAG3_MODE= ./event-driven-agent
```

### Split Mode (Production)

Deploys the API and workers as separate Kubernetes pods for independent scaling:

```bash
./setup.sh deploy-split     # Deploy API + Worker as separate pods
./setup.sh deploy-embedded  # Switch back to single-pod embedded mode
```

**What each pod does:**

| Pod | Responsibilities |
|-----|-----------------|
| **API** (`event-driven-agent-api`) | Receives AlertManager webhooks, enqueues tasks to Redis, handles HITL resume requests, serves health/readiness probes. No AI processing. |
| **Worker** (`event-driven-agent-worker`) | Dequeues tasks from Redis, runs the full AI orchestration pipeline (LLM planning, DAG execution, tool calls, iterative phases, HITL checkpoints). This is where all compute happens. |

**How they connect:**

```
AlertManager ──webhook──▶ API pod ──Redis queue──▶ Worker pod
                              ▲                        │
                              │                        │ (tool calls)
                    HITL resume (HTTP)            ▼
                              │               devops-tool, jira-tool,
                    Registry Viewer UI         slack-tool, prometheus, ...
```

- The K8s Service (`event-driven-agent`) routes to the **API pod** — AlertManager and HITL resume webhooks always reach the API.
- The Worker pod has its own Service (`event-driven-agent-worker-service`) for health checks and metrics scraping only.
- Both pods share the same ConfigMap (`event-driven-agent-env-config`) for API keys, timeouts, and HITL settings.
- The Worker uses `TRUVAG3_HITL_WEBHOOK_URL` pointing to the API Service so that HITL checkpoint notifications reach the API pod.

**Manifests:**

| File | Contents |
|------|----------|
| `k8-deployment.yaml` | Embedded mode (single pod) |
| `k8-deployment-api.yaml` | API pod for split mode |
| `k8-deployment-worker.yaml` | Worker pod for split mode |

---

## AlertManager Integration

The agent includes Kubernetes manifests for deploying Prometheus AlertManager pre-configured to route TruvaG3 alerts to the agent's webhook endpoint.

### Included Files

- **`alertmanager-config.yaml`**: ConfigMap with AlertManager routing rules. Routes alerts matching `^TruvaG3.*` to the agent's webhook at `http://event-driven-agent.truvag3-examples:80/webhook/alertmanager`.
- **`alertmanager.yaml`**: Deployment, Service, and NodePort for the AlertManager instance.

### Routing Rules

```yaml
routes:
  - match_re:
      alertname: '^TruvaG3.*'
    receiver: 'truvag3-event-agent'
    group_wait: 10s
    group_interval: 1m
    repeat_interval: 5m
```

Alerts with names matching `TruvaG3*` are grouped by `alertname` and `severity`, with a 10-second initial wait before the first notification. Non-matching alerts go to a default no-op receiver.

### Testing Without AlertManager

You can test the agent without AlertManager deployed by using the manual trigger endpoint:

```bash
curl -X POST http://localhost:8372/trigger \
  -H "Content-Type: application/json" \
  -d '{"alertname": "HighCPU", "severity": "critical", "instance": "web-01", "summary": "CPU above 90%"}'
```

Or by sending an AlertManager-formatted payload directly to the webhook:

```bash
curl -X POST http://localhost:8372/webhook/alertmanager \
  -H "Content-Type: application/json" \
  -d '{"version":"4","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"HighCPU","severity":"critical"},"annotations":{"summary":"CPU above 90%"},"fingerprint":"test123"}]}'
```

---

## API Reference

The agent exposes the following endpoints:

### Health & Discovery

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check with Redis, orchestrator, AI provider, HITL, and queue status |
| `/api/capabilities` | GET | List all registered capabilities |

### Capabilities

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/webhook/alertmanager` | POST | Receives AlertManager webhook payloads for automated incident response |
| `/trigger` | POST | Manually triggers an alert investigation for testing or CLI integration |
| `/events` | GET | Returns recent alert event history with queue status |

### Async Task API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/tasks` | POST | Submit a new async task (e.g., `alert_investigation`) |
| `/api/v1/tasks/{id}` | GET | Get task status and results |
| `/api/v1/tasks/{id}/cancel` | POST | Cancel a running task |

### HITL Endpoints

These endpoints manage Human-in-the-Loop checkpoints when the AI investigation reaches a step that's gated by `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` (default: `rollout_restart`, `scale_deployment`, `delete_pod`). See [Human-in-the-Loop (HITL)](#human-in-the-loop-hitl) for the approval-flow walkthrough and [E2E Stress Test](#e2e-stress-test-hitl-demo) for an end-to-end demo.

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/hitl/checkpoints` | GET | List pending checkpoints (query params: `status`, `limit`, `offset`) |
| `/hitl/checkpoints/{id}` | GET | Get full details for a single checkpoint (plan, current step, completed steps, decision) |
| `/hitl/command` | POST | Submit `approve` / `reject` / `edit` / `abort` decision |
| `/hitl/resume/{id}` | POST | Resume execution after approval — re-enqueues a `hitl_resume` task to the worker pool |
| `/internal/hitl-webhook` | POST | **API mode only.** Worker pod POSTs here when a checkpoint fires; the API pod fans out the notification to the registry-viewer-app and any subscribed clients |

> **Split-mode note:** In split mode (separate API + Worker pods, see [Deployment Modes](#deployment-modes)), the K8s Service routes all `/hitl/*` requests to the **API pod**, so external clients (Registry Viewer App, curl, the chat-ui) always reach the API. The worker pod registers only `/internal/hitl-webhook` (as a receiver) and the standard `/hitl/*` endpoints serve from the API pod. `TRUVAG3_HITL_WEBHOOK_URL` on the worker points back at the API Service so checkpoint notifications cross the pod boundary.

### Example Requests

**Health Check:**
```bash
curl http://localhost:8372/health
```

**Sample Response:**
```json
{
  "status": "healthy",
  "timestamp": 1709568000,
  "service": "event-driven-agent",
  "redis": "healthy",
  "queue_depth": 0,
  "orchestrator": {
    "status": "active",
    "total_requests": 12,
    "successful_requests": 10,
    "failed_requests": 2
  },
  "ai_provider": "connected",
  "hitl": "active"
}
```

**Manual Alert Trigger (Critical):**
```bash
curl -X POST http://localhost:8372/trigger \
  -H "Content-Type: application/json" \
  -d '{
    "alertname": "TruvaG3ComponentDown",
    "severity": "critical",
    "instance": "stock-market-tool-xyz:8348",
    "summary": "TruvaG3 component truvag3-tools is down"
  }'
```

**Sample Response:**
```json
{
  "status": "ok",
  "alertname": "TruvaG3ComponentDown",
  "severity": "critical",
  "enqueued": true,
  "fingerprint": "manual-TruvaG3ComponentDown-1709568000000000000"
}
```

**AlertManager Webhook (simulating AlertManager):**
```bash
curl -X POST http://localhost:8372/webhook/alertmanager \
  -H "Content-Type: application/json" \
  -d '{
    "version": "4",
    "status": "firing",
    "alerts": [
      {
        "status": "firing",
        "labels": {
          "alertname": "HighCPU",
          "severity": "critical",
          "instance": "web-server-01"
        },
        "annotations": {
          "summary": "CPU usage above 90% for 5 minutes"
        },
        "startsAt": "2026-03-04T10:00:00Z",
        "fingerprint": "abc123"
      }
    ]
  }'
```

**Event History:**
```bash
curl http://localhost:8372/events
```

**List Capabilities:**
```bash
curl http://localhost:8372/api/capabilities
```

---

## Human-in-the-Loop (HITL)

By default, only **destructive Kubernetes write operations** (`rollout_restart`, `scale_deployment`, `delete_pod`) are gated by human-in-the-loop approval — these are the capabilities listed in the shipped `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES`. Notification capabilities (`create_ticket`, `send_message`, `send_rich_message`) and read-only investigation calls (`query_metrics`, `query_logs`, `get_pods`) run autonomously. When the orchestrator encounters a gated capability, execution pauses and a checkpoint is persisted to Redis for approval.

### Configuration

HITL is configured via environment variables (loaded by `orchestration.DefaultConfig()`):

```bash
# Enable HITL
TRUVAG3_HITL_ENABLED=true

# Plan-level approval (approve the entire DAG before execution)
TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=false

# Step-level approval for destructive K8s write operations.
# The shipped .env.example gates only these three. Notification capabilities
# (create_ticket, send_message, send_rich_message) are deliberately NOT gated
# so the agent can autonomously file Jira tickets / post Slack messages
# during investigation. Add them here only if you want human approval before
# every notification too.
TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=rollout_restart,scale_deployment,delete_pod

# Approval timeout
TRUVAG3_HITL_DEFAULT_TIMEOUT=5m
```

### Approval Flow

1. Worker begins alert investigation
2. AI planner creates a DAG (e.g., check metrics -> get logs -> restart pod -> create JIRA)
3. When the `restart pod` step is reached, HITL pauses execution
4. A checkpoint is stored in Redis (DB 6) with the execution state
5. The task result includes `"status": "pending_approval"` and a `checkpoint_id`
6. A human approves or rejects via the registry-viewer-app or API
7. On approval, execution resumes from the checkpoint; on rejection or timeout, the investigation completes without the write operation

---

## Configuration Reference

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 8372 | HTTP server port |
| `REDIS_URL` | - | Redis connection URL (required) |
| `TRUVAG3_MODE` | `""` (embedded) | Deployment mode: `api`, `worker`, or empty for embedded |
| `WORKER_COUNT` | 3 | Number of background workers |
| `OPENAI_API_KEY` | - | OpenAI API key |
| `ANTHROPIC_API_KEY` | - | Anthropic API key |
| `GROQ_API_KEY` | - | Groq API key |
| `APP_ENV` | development | Telemetry profile (development/staging/production) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | http://localhost:4318 | OTEL Collector endpoint |
| `SLACK_WEBHOOK_URL` | - | Slack webhook URL for warning-level notifications |
| `TRUVAG3_EVENT_DEDUP_TTL` | 300 | Deduplication TTL in seconds |
| `TRUVAG3_HITL_ENABLED` | true | Enable human-in-the-loop approval |
| `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | false | Require approval for entire execution plan |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | - | Comma-separated list of capabilities requiring step-level approval |
| `TRUVAG3_HITL_DEFAULT_TIMEOUT` | 5m | Timeout for approval requests |
| `TRUVAG3_LLM_DEBUG_ENABLED` | true | Enable LLM debug store (Redis DB 7) |
| `TRUVAG3_LOG_LEVEL` | info | Log level (debug/info/warn/error) |

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
# Safe copy - won't overwrite existing .env
[ ! -f .env ] && cp .env.example .env
```

The `.env.example` file contains comprehensive documentation for all options including:

- **AI Provider Keys** - Supports provider chain for failover (OpenAI → Anthropic → Groq)
- **Model Aliases** - Override default/smart/fast model mappings per provider
- **Service Configuration** - Port, Redis URL, deployment mode, worker count
- **Event Agent Configuration** - Dedup TTL, Slack webhook
- **HITL Configuration** - Sensitive capability list, approval timeouts, default action
- **Telemetry Configuration** - Environment profiles and OTLP endpoints

### OpenAI-Compatible Providers

You can use any OpenAI-compatible API (DeepSeek, Together AI, xAI, Qwen, local Ollama, etc.) as a drop-in replacement for OpenAI.

**Option 1: Override OpenAI endpoint** (simplest)

```bash
# Use DeepSeek as the "OpenAI" provider
OPENAI_API_KEY=your-deepseek-api-key
OPENAI_BASE_URL=https://api.deepseek.com/v1
TRUVAG3_OPENAI_MODEL_DEFAULT=deepseek-chat
```

**Option 2: Use dedicated environment variables**

```bash
# DeepSeek
DEEPSEEK_API_KEY=your-key
DEEPSEEK_BASE_URL=https://api.deepseek.com  # optional, this is the default

# xAI (Grok)
XAI_API_KEY=your-key

# Together AI
TOGETHER_API_KEY=your-key

# Qwen (Alibaba)
QWEN_API_KEY=your-key

# Ollama (local, no API key needed)
OLLAMA_BASE_URL=http://localhost:11434/v1
```

See `.env.example` for complete documentation of all supported providers.

### Setup Script Commands

```bash
# Local Development
./setup.sh build          # Build the agent binary
./setup.sh run            # Build and run the agent locally

# Kubernetes Cluster
./setup.sh cluster        # Create Kind cluster with port mappings
./setup.sh infra          # Setup monitoring infrastructure + AlertManager
./setup.sh full-deploy    # Complete deployment: cluster + infra + agent + product-catalog-api + port forwards

# Kubernetes Deployment
./setup.sh docker-build     # Build Docker image using local workspace modules
./setup.sh deploy           # Build, load, and deploy to Kubernetes (embedded mode)
./setup.sh rebuild          # Rebuild with --no-cache and redeploy
./setup.sh deploy-split     # Deploy in split mode (separate API + Worker pods)
./setup.sh deploy-embedded  # Switch back to embedded mode (single pod)
./setup.sh test             # Run test requests against deployed agent
./setup.sh forward          # Port forward agent service only
./setup.sh forward-all      # Port forward agent + AlertManager + monitoring
./setup.sh logs             # View agent logs
./setup.sh status           # Check deployment status (agent + AlertManager + product-catalog-api + monitoring)
./setup.sh rollout          # Restart deployment to pick up new secrets/config

# Mock Service (E2E HITL stress test driver)
./setup.sh mock-service deploy       # Deploy product-catalog-api mock service
./setup.sh mock-service rebuild      # Rebuild product-catalog-api from scratch
./setup.sh mock-service normal-load  # Start ~3 req/s baseline load
./setup.sh mock-service heavy-load   # Start ~30 req/s heavy load
./setup.sh mock-service degrade      # Inject 1.2-2.0s latency + memory leak (triggers alert → HITL)
./setup.sh mock-service recover      # Release ballast and recover to healthy
./setup.sh mock-service status       # Show product-catalog-api pod and degradation state
./setup.sh mock-service logs         # Tail product-catalog-api logs
./setup.sh mock-service clean        # Remove product-catalog-api deployment

# Cleanup
./setup.sh clean          # Remove agent, AlertManager, and product-catalog-api deployments
./setup.sh clean-all      # Delete Kind cluster and all resources
```

---

## E2E Stress Test (HITL Demo)

This section describes how to run a realistic end-to-end test that exercises the full alert pipeline: baseline traffic, performance degradation, gradual memory leak, Prometheus alert firing, AI-driven investigation, and human-in-the-loop approval for destructive remediation.

### Overview

A mock microservice ([product-catalog-api](../mock-services/product-catalog-api/)) runs in the cluster with Prometheus metrics, simulating a realistic production service. It serves business endpoints (`/api/v1/products`, `/api/v1/categories`) with ~100-200ms baseline latency (simulating DB queries). On command, it degrades: response times spike to 1.2-2.0s and memory leaks gradually (~2MB/sec up to 40MB). Prometheus detects the elevated P90 latency and fires the `TruvaG3HighLatency` alert, which AlertManager routes to the event-driven-agent for AI investigation.

### Test Scenario

The test is designed in phases to produce realistic Grafana dashboards:

| Phase | Duration | Traffic | Service State | What Grafana Shows |
|-------|----------|---------|---------------|--------------------|
| **1. Baseline** | ~30 min | Normal (~3 req/s) | Healthy (100-200ms) | Flat, stable response time and memory |
| **2. Load spike** | ~2 min | Heavy (~30 req/s) | Healthy (100-200ms) | Request rate spike, latency stays flat |
| **3. Degradation** | ongoing | Heavy (~30 req/s) | Degraded (1.2-2.0s latency, memory leak ~2MB/s) | Latency jumps 8-10x, memory ramp in Grafana |
| **4. Alert fires** | T+90s | Heavy | Degraded | Prometheus alert transitions to `firing` |
| **5. AI investigation** | ~30-60s | Heavy | Degraded | Agent queries metrics, pods, logs |
| **6. HITL gate** | until approved | Heavy | Degraded | `rollout_restart` awaits human approval |

### Pipeline

```
product-catalog-api         Prometheus              AlertManager         event-driven-agent
     |                           |                        |                       |
     |-- normal load (3 req/s)-->|                        |                       |
     |   baseline: 100-200ms     |                        |                       |
     |   (30 min)                |                        |                       |
     |                           |                        |                       |
     |-- heavy load (30 req/s)-->|                        |                       |
     |                           |                        |                       |
     |-- POST /admin/simulate/ ->|                        |                       |
     |        degrade            |                        |                       |
     |   (latency: 1.2-2.0s,    |                        |                       |
     |    memory: +2MB/sec)      |                        |                       |
     |                           |                        |                       |
     |<-- scrape /metrics -------|                        |                       |
     |   P90 latency > 0.8s      |                        |                       |
     |                           |-- after 1m for ------->|                       |
     |                           |   TruvaG3HighLatency    |                       |
     |                           |   severity: critical   |                       |
     |                           |                        |-- webhook POST ------>|
     |                           |                        |   /webhook/alertmanager
     |                           |                        |                       |
     |                           |                        |      AI investigation |
     |                           |                        |      query_metrics    |
     |                           |                        |      get_pods         |
     |                           |                        |      get_pod_logs     |
     |                           |                        |      rollout_restart  |
     |                           |                        |          |            |
     |                           |                        |      HITL gate        |
     |                           |                        |      (awaiting        |
     |                           |                        |       approval)       |
```

### What Makes This Realistic

- **Realistic service identity**: The service is named `product-catalog-api` with business endpoints (`/api/v1/products`, `/api/v1/categories`). Log messages show "Connection pool degradation detected" and "GC pressure increasing". The AI agent cannot infer the degradation is intentional.
- **Realistic baseline latency**: Normal requests take 100-200ms (simulating DB queries), not instant. The degradation to 1.2-2.0s is an 8-10x increase — believable for connection pool exhaustion.
- **Gradual memory leak**: Memory grows at ~2MB/sec (step-wise allocation, page-touched) up to 40MB total. This produces a realistic ramp in Grafana, not a single spike.
- **Real Prometheus alert**: The `TruvaG3HighLatency` rule evaluates `histogram_quantile(0.90, ...)` with `sum by (le, pod, namespace, app, job, instance)` to produce exactly one alert per pod.
- **Real AlertManager routing**: Alerts matching `alertname: TruvaG3HighLatency` (exact match, not regex) are routed to the event-driven-agent's `/webhook/alertmanager` endpoint with `repeat_interval: 15m`.
- **Dedup TTL safety**: `TRUVAG3_EVENT_DEDUP_TTL=1800` (30 min) >> AlertManager `repeat_interval` (15 min), preventing duplicate investigations from AlertManager resends.
- **Real AI investigation**: The agent queries actual Prometheus metrics, inspects real pods, reads real logs, and plans remedial actions.
- **Real HITL gate**: Only destructive operations (`rollout_restart`, `scale_deployment`, `delete_pod`) require human approval.

### Running the Stress Test

**Prerequisites:** A running cluster with `./setup.sh full-deploy` completed.

```bash
cd examples/event-driven-agent

# ── Phase 1: Establish baseline (30 minutes) ──────────────────────
# Start normal traffic (~3 req/s) to establish a flat baseline in Grafana.
./setup.sh mock-service normal-load
# Monitor Grafana at http://localhost:3000 — look for stable ~150ms response time.
# Let this run for ~30 minutes to build a clear baseline.

# ── Phase 2: Increase load ────────────────────────────────────────
# In a second terminal, start heavy traffic (~30 req/s).
# Stop the normal-load first (Ctrl+C), then:
./setup.sh mock-service heavy-load

# ── Phase 3: Trigger degradation ──────────────────────────────────
# After ~2 minutes of heavy load, trigger the degradation.
# This injects 1.2-2.0s latency and starts a gradual memory leak (~2MB/sec).
./setup.sh mock-service degrade

# ── Phase 4: Wait for alert (~90 seconds) ─────────────────────────
# Monitor in Prometheus: http://localhost:9090/alerts
# Monitor in Grafana:    http://localhost:3000 (Product Catalog API dashboards)
# The TruvaG3HighLatency alert will transition: inactive → pending → firing

# ── Phase 5: Watch the AI investigation ───────────────────────────
./setup.sh logs
# The agent will: query_metrics → get_pods → get_pod_logs → plan rollout_restart

# ── Phase 6: Approve or reject the HITL checkpoint ────────────────
# Via Registry Viewer App UI: http://localhost:8361
# Or via CLI:
#   curl -X POST http://localhost:8372/hitl/command \
#     -H "Content-Type: application/json" \
#     -d '{"checkpoint_id": "<id>", "type": "approve"}'

# ── Cleanup ───────────────────────────────────────────────────────
# Stop the heavy load (Ctrl+C in that terminal), then recover the service:
./setup.sh mock-service recover
```

### Grafana Dashboards

Two dashboards are provisioned for monitoring the test:

| Dashboard | Description |
|-----------|-------------|
| **Product Catalog API - Response Time** | P50/P90/P95 response time (ms), request rate, process RSS, alert status |
| **Product Catalog API - Resources** | CPU usage (cAdvisor), container memory vs process RSS, combined CPU/memory view, alert status |

### Memory Safety

The product-catalog-api container has an 80Mi memory limit. The Go binary uses ~13MB RSS at rest. The gradual memory leak allocates 2MB chunks per second up to 40MB total (~53MB peak RSS), safely under the 80Mi (84MB) limit. No OOM kills will occur.

When recovery is triggered (`/admin/simulate/recover`), the ballast is released and `runtime.GC()` is called to return memory to the OS.

### Alert Rule

The `TruvaG3HighLatency` rule is defined in `examples/k8-deployment/prometheus.yaml`:

```yaml
- alert: TruvaG3HighLatency
  expr: histogram_quantile(0.90, sum by (le, pod, namespace, app, job, instance) (rate(http_request_duration_seconds_bucket{job="truvag3-mock-services"}[2m]))) > 0.8
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: "High P90 latency on {{ $labels.app }} ({{ $labels.pod }})"
    description: "P90 response time is {{ printf \"%.2f\" $value }}s on pod {{ $labels.pod }} in namespace {{ $labels.namespace }}. Normal baseline is ~150ms."
```

### AlertManager Configuration

Alerts are routed with exact match (not regex) to prevent unrelated alerts from flooding the agent:

```yaml
routes:
  - match:
      alertname: TruvaG3HighLatency
    receiver: 'truvag3-event-agent'
    group_wait: 10s
    group_interval: 1m
    repeat_interval: 15m
```

### Dedup and Flood Prevention

| Setting | Value | Purpose |
|---------|-------|---------|
| `TRUVAG3_EVENT_DEDUP_TTL` | 1800s (30 min) | Must be >> `repeat_interval` to prevent duplicate investigations |
| AlertManager `repeat_interval` | 15m | How often AlertManager resends a firing alert |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | `rollout_restart,scale_deployment,delete_pod` | Only destructive K8s operations require HITL approval |

### Expected Timeline

| Time | Event |
|------|-------|
| T-30m | Normal load started, baseline established in Grafana |
| T+0s | Heavy load started (~30 req/s) |
| T+2m | `degrade` called — latency jumps to 1.2-2.0s, memory leak begins |
| T+2m15s | First Prometheus scrape sees P90 > 0.8s, alert enters `pending` |
| T+3m15s | Alert has been pending for 1 minute, transitions to `firing` |
| T+3m25s | AlertManager `group_wait` (10s) expires, webhook sent to event-driven-agent |
| T+3m30s-4m | AI investigation begins: query_metrics, get_pods, get_pod_logs |
| T+4m-4m30s | AI plans `rollout_restart`, HITL gate pauses for approval |
| T+4m+ | Memory in Grafana shows ~4-8MB above baseline (still growing at 2MB/s) |

---

## Telemetry

The agent emits comprehensive observability data — every alert, every investigation step, every HITL pause is traced end-to-end.

### Tracing (Jaeger)

- Every webhook arrival starts a root span; the worker pool's investigation runs link back to the original webhook trace
- Tool execution timing is captured per step in the DAG
- HITL wait spans (`hitl.checkpoint.loaded`, `hitl.resume_started`, `hitl.resume_completed`) — linked to the original investigation span so a multi-checkpoint workflow stitches together cleanly
- Worker-to-API webhook delivery is traced across the pod boundary in split mode (worker writes `original_trace_id` into the checkpoint, API attaches it on resume)
- Access at http://jaeger.localhost (or http://localhost:16686 via port-forward)

### LLM Debug Payload Store

For debugging orchestration issues, the LLM Debug Store captures complete prompts and responses (Jaeger truncates large payloads). The shipped `.env.example` enables it by default:

```bash
TRUVAG3_LLM_DEBUG_ENABLED=true
```

This captures all LLM interactions at the standard recording sites (`plan_generation`, `correction`, `synthesis`, `synthesis_streaming`, `micro_resolution`, `semantic_retry`) with full payload visibility. Records are stored in Redis DB 7 with configurable TTL.

### Metrics (Prometheus/Grafana)

| Metric | Type | Description | Labels |
|--------|------|-------------|--------|
| `event_agent.alerts_received` | Counter | Total alerts received from AlertManager | severity, alertname |
| `event_agent.alerts_deduplicated` | Counter | Alerts skipped due to deduplication | alertname |
| `event_agent.alerts_enqueued` | Counter | Alerts enqueued for AI investigation | severity |
| `event_agent.alerts_processed` | Counter | Alerts processed by worker pool | status |
| `event_agent.processing_duration_ms` | Histogram | End-to-end investigation duration (queue dequeue → AI synthesis complete) | - |
| `event_agent.slack_notifications` | Counter | Slack notifications sent for warnings | status |

Access Grafana at http://grafana.localhost (admin/admin). The E2E stress test ships two pre-provisioned dashboards — see [Grafana Dashboards](#grafana-dashboards) under the stress test.

### Logging

Structured JSON logs with component attribution and trace context. Request-scoped logs include `trace.trace_id` and `trace.span_id` for distributed tracing:

```json
{
  "component": "agent/event-driven-agent",
  "level": "INFO",
  "message": "Alert enqueued for AI investigation",
  "operation": "webhook_alertmanager",
  "service": "event-driven-agent",
  "alertname": "TruvaG3HighLatency",
  "severity": "critical",
  "fingerprint": "abc123def456",
  "queue_depth": 1,
  "timestamp": "2026-05-22T16:31:51Z",
  "trace.span_id": "0b319744acd226d5",
  "trace.trace_id": "445c352173a351de293d4d27416b0eb2"
}
```

```json
{
  "component": "agent/event-driven-agent",
  "level": "INFO",
  "message": "AI investigation completed",
  "operation": "alert_investigation",
  "service": "event-driven-agent",
  "alertname": "TruvaG3HighLatency",
  "tools_used": ["query_metrics", "get_pods", "get_pod_logs"],
  "interrupted": true,
  "checkpoint_id": "cp-d6c2b787-292c-4f",
  "duration_ms": 24380,
  "timestamp": "2026-05-22T16:32:15Z",
  "trace.span_id": "e59c0a1c74b3f996",
  "trace.trace_id": "445c352173a351de293d4d27416b0eb2"
}
```

---

## Project Structure

```
event-driven-agent/
├── main.go                    # Entry point; mode dispatch (embedded / api / worker)
├── event_agent.go             # Agent type + AI chain client wiring + capability registration
├── event_processor.go         # AI investigation pipeline (context enrichment → orchestrator → synthesis)
├── webhook_receiver.go        # AlertManager webhook parser + deterministic pipeline (sev route, dedup, enqueue)
├── queue_consumer.go          # Worker pool that BRPOPs from Redis and invokes event_processor
├── handlers.go                # HTTP handlers for /trigger, /events, /health, /webhook/alertmanager
├── hitl_setup.go              # HITL infrastructure (checkpoint store, controller, policy)
├── alertmanager-config.yaml   # AlertManager routing rules ConfigMap
├── alertmanager.yaml          # AlertManager Deployment + Service
├── k8-deployment.yaml         # Kubernetes manifest (embedded mode — single pod)
├── k8-deployment-api.yaml     # API pod manifest (split mode)
├── k8-deployment-worker.yaml  # Worker pod manifest (split mode)
├── go.mod                     # Go module definition
├── Dockerfile                 # Production container image
├── Dockerfile.workspace       # Development container with local modules
├── setup.sh                   # Build and deployment script (cluster, infra, deploy, mock-service driver)
└── README.md                  # This file
```

---

## Troubleshooting

### Alerts not being processed

1. **Check the alert queue depth**:
   ```bash
   curl http://localhost:8372/health | jq .queue_depth
   ```

2. **Verify the orchestrator is initialized**:
   ```bash
   curl http://localhost:8372/health | jq .orchestrator
   # Should show "status": "active", not "initializing"
   ```

3. **Check worker logs for errors**:
   ```bash
   ./setup.sh logs | grep -i "alert_investigation"
   ```

### Orchestrator stuck in "initializing"

The orchestrator waits for Redis Discovery to become available. Check:

1. **Redis connectivity** — confirm `REDIS_URL` is set in `.env` (the value is folded into the ConfigMap that `setup.sh deploy` builds). Re-run `./setup.sh rollout` after editing `.env` so the pod picks up the change.

2. **Discovery availability** (the orchestrator polls until Discovery is ready):
   ```bash
   ./setup.sh logs | grep -i discovery
   ```

### Alerts being deduplicated unexpectedly

The agent deduplicates alerts by fingerprint with a default 5-minute TTL:

1. **Check dedup TTL setting**:
   ```bash
   # Default is 300 seconds (5 minutes)
   echo $TRUVAG3_EVENT_DEDUP_TTL
   ```

2. **Lower the TTL for testing**:
   ```bash
   TRUVAG3_EVENT_DEDUP_TTL=30  # 30 seconds
   ```

### HITL checkpoint not appearing

1. **Verify HITL is enabled**:
   ```bash
   curl http://localhost:8372/health | jq .hitl
   # Should show "active"
   ```

2. **Check sensitive capabilities are configured**:
   ```bash
   echo $TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES
   ```

3. **Ensure the investigation reaches a write operation** -- read-only investigations (check metrics, get logs) will complete without HITL.

### No AI provider available

1. **Check health endpoint**:
   ```bash
   curl http://localhost:8372/health | jq .ai_provider
   # Should show "connected", not "not configured"
   ```

2. **Verify API key is set**:
   ```bash
   # At least one must be set
   echo $OPENAI_API_KEY
   echo $ANTHROPIC_API_KEY
   echo $GROQ_API_KEY
   ```

### Useful Commands

All day-to-day operations go through `setup.sh`. Run `./setup.sh help` to see every subcommand (including the `mock-service` driver and split-mode toggles).

```bash
# Stream agent logs
./setup.sh logs

# Check pod / service status (agent + AlertManager + product-catalog-api + monitoring)
./setup.sh status

# Port forward the agent to localhost:8372
./setup.sh forward

# Port forward agent + AlertManager + monitoring dashboards
./setup.sh forward-all

# Restart the deployment (e.g., to pick up a new ConfigMap from .env)
./setup.sh rollout

# Run the built-in smoke test against the deployed agent
./setup.sh test

# Remove only the agent (keeps cluster + infra)
./setup.sh clean

# Tear down the entire Kind cluster
./setup.sh clean-all
```

Check AlertManager and the agent's runtime state by querying the agent directly (with `./setup.sh forward` running):

```bash
# Test manual alert trigger
curl -X POST http://localhost:8372/trigger \
  -H "Content-Type: application/json" \
  -d '{"alertname": "TruvaG3ComponentDown", "severity": "critical", "instance": "test:8080", "summary": "Test alert"}'

# Check event history and queue depth
curl http://localhost:8372/events
```

---

## Related Examples

**Sibling agents:**
- [devops-chat-agent](../devops-chat-agent/) - Conversational sibling using the same HITL infrastructure + tool family
- [agent-with-human-approval](../agent-with-human-approval/) - Minimal HITL demo (synchronous chat) — best place to understand checkpoints in isolation
- [agent-with-orchestration](../agent-with-orchestration/) - AI orchestration with DAG planning, no HITL or event-driven patterns
- [agent-with-async](../agent-with-async/) - Async task processing patterns (background workers without webhooks)
- [agent-with-telemetry](../agent-with-telemetry/) - Telemetry-focused observability example
- [agent-with-resilience](../agent-with-resilience/) - Resilience patterns (circuit breakers, retries)
- [agent-example](../agent-example/) - Basic agent without event-driven patterns

**Tools called by this agent:**
- [devops-tool](../devops-tool/) - `kubectl` operations including HITL-gated `rollout_restart` / `scale_deployment` / `delete_pod`
- [devops-observability-tool](../devops-observability-tool/) - Loki + Jaeger queries for investigation
- [prometheus-query-tool](../prometheus-query-tool/) - PromQL queries
- [jira-tool](../jira-tool/) - Ticket management
- [slack-tool](../slack-tool/) - Incident notifications (direct call for `warning` severity, AI-orchestrated for `critical`)

**Further reading:**
- [TruvaG3 Orchestration Module](../../orchestration/README.md) - AI orchestration and DAG planning
- [Distributed Tracing Guide](../../docs/observability/DISTRIBUTED_TRACING_GUIDE.md) - End-to-end request tracing and log correlation
- [Agent Development Guide](../../docs/building/AGENT_DEVELOPMENT_GUIDE.md) - Building agents with TruvaG3
- [Prometheus AlertManager Documentation](https://prometheus.io/docs/alerting/latest/alertmanager/) - Upstream AlertManager reference
- [OpenTelemetry Go Documentation](https://opentelemetry.io/docs/languages/go/) - OTel SDK reference

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
