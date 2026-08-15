# DevOps Chat Agent

A streaming chat agent that demonstrates AI-powered orchestration of Kubernetes and infrastructure operations using the TruvaG3 framework. It provides real-time Server-Sent Events (SSE) responses by intelligently coordinating DevOps tools — `kubectl`, observability backends (logs/traces/metrics), incident tooling (Jira, Slack), and utility services — to investigate, diagnose, and remediate cluster issues. It also includes Human-in-the-Loop (HITL) gates for sensitive operations and cross-agent shared memory for episodic event recall.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Required Tools and Agents](#required-tools-and-agents)
- [Architecture](#architecture)
- [API Reference](#api-reference)
- [Configuration](#configuration)
  - [Agent Skills](#agent-skills)
  - [OpenAI-Compatible Providers](#openai-compatible-providers)
- [Human-in-the-Loop (HITL)](#human-in-the-loop-hitl)
- [Shared Agent Memory](#shared-agent-memory)
- [Session Management](#session-management)
- [Telemetry](#telemetry)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

Running this example locally is the best way to understand how the TruvaG3 framework orchestrates DevOps tools and coordinates multi-step infrastructure workflows. Follow the steps below to get this example running.

### Prerequisites

Before running this example in your local machine, ensure you have the following installed:

| Requirement | Version | macOS | Windows |
|-------------|---------|-------|---------|
| **Docker Desktop** | Latest | [Download](https://www.docker.com/products/docker-desktop/) | [Download](https://www.docker.com/products/docker-desktop/) |
| **Kind** | v0.20+ | `brew install kind` | `choco install kind` or [Download](https://kind.sigs.k8s.io/docs/user/quick-start/) |
| **kubectl** | v1.28+ | `brew install kubectl` | `choco install kubernetes-cli` or [Download](https://kubernetes.io/docs/tasks/tools/) |
| **Go** | 1.25+ | `brew install go` | `choco install golang` or [Download](https://golang.org/dl/) |
| **AI Provider API Key** | - | At least one: [OpenAI](https://platform.openai.com/api-keys), [Anthropic](https://console.anthropic.com/), [Groq](https://console.groq.com/keys), [Gemini](https://aistudio.google.com/apikey), or any [OpenAI-compatible](#openai-compatible-providers) provider | Same as macOS |

> **Note:** This agent serves as the backend for the [chat-ui](../chat-ui/) example (the DevOps view is at `http://chat.localhost/devops.html`). The chat-ui provides a web interface that connects to this agent's SSE streaming API. While the agent can be used standalone via its REST API, the chat-ui offers a convenient way to interact with it.

> **Important:** The devops-chat-agent requires [tools to be deployed](#required-tools-and-agents) (devops-tool, devops-observability-tool, prometheus-query-tool, jira-tool, slack-tool, etc.) to function. You can deploy tools before or after the agent, but the agent won't be able to answer queries until tools are running.

### Quick Start (Recommended)

The fastest way to get everything running in your local:

```bash
cd examples/devops-chat-agent

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**⚠️ STOP HERE** - Open `.env` in your editor and configure your API key(s):

```bash
# Open .env in your preferred editor
nano .env    # or: code .env / vim .env
```

At minimum, set ONE of these in your `.env` file (all three keys are already present and uncommented in `.env.example`, just blank — paste your value after the `=`):
- `OPENAI_API_KEY=sk-your-key`
- `ANTHROPIC_API_KEY=sk-ant-your-key`
- `GROQ_API_KEY=gsk_your-key`

> **Note:** Multiple providers enable automatic failover.

```bash
# 2. Deploy cluster, infrastructure, and the chat agent
./setup.sh full-deploy

# 3. Deploy the required tools (each tool has its own setup script).
#    Each line returns to examples/devops-chat-agent so the next `cd ../...`
#    resolves correctly. Run from the examples/devops-chat-agent directory.
cd ../devops-tool && ./setup.sh deploy && cd ../devops-chat-agent
cd ../devops-observability-tool && ./setup.sh deploy && cd ../devops-chat-agent
cd ../prometheus-query-tool && ./setup.sh deploy && cd ../devops-chat-agent
cd ../jira-tool && ./setup.sh deploy && cd ../devops-chat-agent
cd ../slack-tool && ./setup.sh deploy && cd ../devops-chat-agent
cd ../system-utilities-tool && ./setup.sh deploy && cd ../devops-chat-agent
```

**What `./setup.sh full-deploy` does:**
1. Creates a Kind Kubernetes cluster with NGINX Ingress Controller
2. Deploys infrastructure (Redis, Prometheus, Grafana, Jaeger, OTEL Collector)
3. Builds and loads the devops-chat-agent image
4. Recreates or updates agent-owned skills, then deploys the agent (this script does **not** deploy `chat-ui`)
5. Configures Ingress routes, verifies services, and prints their URLs

> **Note:** All services are accessible via `*.localhost` hostnames through the NGINX Ingress Controller. No port-forwarding is needed. On macOS/Linux, `*.localhost` resolves to `127.0.0.1` automatically ([RFC 6761](https://tools.ietf.org/html/rfc6761)). This is the same pattern used in production with real DNS on EKS/GKE/AKS.

**What you need to do separately:**
- Deploy tools using each tool's setup script (Step 3 above).
- Deploy the Chat UI if you want the web interface — on a fresh cluster run `cd ../chat-ui && ./setup.sh` (no arg = build + load image into Kind + apply manifest). `./setup.sh deploy` alone only re-applies the manifest and will leave the pod stuck pulling a missing image; use `./setup.sh rebuild` to refresh after code changes. Alternatively, running `cd ../travel-chat-agent && ./setup.sh full-deploy` first deploys `chat-ui` into the same cluster; then `chat.localhost/devops.html` and `chat.localhost/hitl.html` resolve.

Once complete, access the application at:

| Service | URL | Description |
|---------|-----|-------------|
| **Chat UI (DevOps)** | http://chat.localhost/devops.html | Web interface for DevOps chat |
| **Chat API** | http://devops-chat-agent.localhost | Backend REST/SSE API |
| **HITL Console** | http://chat.localhost/hitl.html | Approve / reject pending operations |
| **Jaeger** | http://jaeger.localhost | Distributed tracing |
| **Grafana** | http://grafana.localhost | Metrics dashboard (admin/admin) |
| **Prometheus** | http://prometheus.localhost | Metrics queries |

> **Note:** `chat.localhost/devops.html` and `chat.localhost/hitl.html` only resolve after the separate chat-ui deployment (see the bullet above). Running just `./setup.sh full-deploy` + the tools gives you a working Chat API at `devops-chat-agent.localhost` and the Jaeger / Grafana / Prometheus dashboards, but no web UI.

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Create the Kubernetes Cluster

```bash
cd examples/devops-chat-agent
./setup.sh cluster
```

This creates a Kind cluster named `truvag3-demo-<username>` with NGINX Ingress Controller support (ports 80/443 only).

#### Step 2: Deploy Infrastructure

```bash
./setup.sh infra
```

This deploys the shared infrastructure components:
- **Redis** - Service discovery, session storage, HITL checkpoints, and shared agent memory
- **OTEL Collector** - Telemetry aggregation
- **Prometheus** - Metrics storage
- **Jaeger** - Distributed tracing
- **Grafana** - Visualization dashboards
- **Loki** - Log backend queried by `devops-observability-tool` (Jaeger above doubles as the trace backend)

#### Step 3: Deploy the Tools (Important!)

**The devops-chat-agent requires tools to be deployed first.** Without tools, the agent has no capabilities to orchestrate.

Each tool has its own setup script:

```bash
# Run from examples/devops-chat-agent. Each line returns there before the next.
cd ../devops-tool && ./setup.sh deploy && cd ../devops-chat-agent                  # kubectl operations
cd ../devops-observability-tool && ./setup.sh deploy && cd ../devops-chat-agent    # query_logs / find_traces / get_trace
cd ../prometheus-query-tool && ./setup.sh deploy && cd ../devops-chat-agent        # metrics queries
cd ../jira-tool && ./setup.sh deploy && cd ../devops-chat-agent                    # ticket creation / comments
cd ../slack-tool && ./setup.sh deploy && cd ../devops-chat-agent                   # incident notifications
cd ../system-utilities-tool && ./setup.sh deploy && cd ../devops-chat-agent        # time, sleep, command execution
cd ../scheduler-tool && ./setup.sh deploy && cd ../devops-chat-agent               # scheduled investigations
cd ../agentic-memory-tool && ./setup.sh deploy && cd ../devops-chat-agent          # cross-agent memory access
```

> **Note:** The `k8-deployment` directory contains shared infrastructure (Redis, Prometheus, etc.), not tools.

#### Step 4: Deploy the Chat Agent

```bash
cd examples/devops-chat-agent

# Create .env from example and configure your API key
cp .env.example .env
# Edit .env and set your AI provider key(s) — the variables are already
# uncommented in .env.example, just paste values after the `=`.

# Build the image and deploy in one step.
# (`./setup.sh deploy` runs build_docker → load_to_kind → deploy_k8s;
#  there is no separate "deploy only, skip build" subcommand.)
./setup.sh deploy
```

#### Step 5: Verify Ingress Routes

```bash
# Health check the agent
curl -s http://devops-chat-agent.localhost/health | jq .

# Sanity-check the other dashboards
curl -sI http://grafana.localhost | head -1
curl -sI http://jaeger.localhost | head -1
```

`./setup.sh full-deploy` runs `truvag3_verify_ingress` for these hosts automatically at the end of a full deploy. All services should be accessible via the `*.localhost` URLs listed above.

---

## Required Tools and Agents

The devops-chat-agent orchestrates multiple tools to answer operational queries. **These tools must be running for the agent to function.**

### Core Tools

| Tool | Purpose | Documentation |
|------|---------|---------------|
| **devops-tool** | `kubectl` operations: get/describe pods, deployments, services, nodes, configmaps; rollout restart; scale; delete | [README](../devops-tool/README.md) |
| **devops-observability-tool** | Loki log queries (`query_logs`) and Jaeger trace lookups (`find_traces`, `get_trace`) | [README](../devops-observability-tool/README.md) |
| **prometheus-query-tool** | PromQL queries against the in-cluster Prometheus | [README](../prometheus-query-tool/README.md) |
| **jira-tool** | Create tickets, add comments, search issues for incident documentation | [README](../jira-tool/README.md) |
| **slack-tool** | Post incident notifications to channels | [README](../slack-tool/README.md) |
| **system-utilities-tool** | `get_current_time`, `sleep`, `execute_command` for inspection and verification pauses | [README](../system-utilities-tool/README.md) |

### Optional Tools

These extend the agent's reach beyond the cluster:

| Tool | Purpose | Documentation |
|------|---------|---------------|
| **scheduler-tool** | Run scheduled DevOps queries (e.g., periodic health checks) | [README](../scheduler-tool/README.md) |
| **agentic-memory-tool** | Search cross-agent episodic events and long-term knowledge | [README](../agentic-memory-tool/README.md) |
| **weather-tool-v2** / **stock-market-tool** / **news-tool** | Demonstrate the agent's ability to handle general-utility queries alongside ops tasks | See respective READMEs |

### Related Agents

| Agent | Purpose | Documentation |
|-------|---------|---------------|
| **agent-with-human-approval** | Minimal HITL example | [README](../agent-with-human-approval/README.md) |
| **agent-with-orchestration** | Basic orchestration example | [README](../agent-with-orchestration/README.md) |
| **agent-with-telemetry** | Full observability example | [README](../agent-with-telemetry/README.md) |
| **event-driven-agent** | Agent that writes episodic events read by this agent via shared memory | [README](../event-driven-agent/README.md) |
| **qa-agent** | Sibling agent that delegates DevOps work to this one via the `devops_operations` capability | [README](../qa-agent/README.md) |

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

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              User Browser                                │
│                  http://chat.localhost/devops.html                       │
└────────────────────────────────┬────────────────────────────────────────┘
                                 │ SSE Stream
                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                         devops-chat-agent                                │
│                          (Port 8357)                                     │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────────────────────┐  │
│  │   Session    │  │     SSE      │  │        AI Orchestrator        │  │
│  │    Store     │  │   Handler    │  │   (Plan → Execute → Synth)    │  │
│  │  (Redis DB2) │  │              │  │       + HITL gates            │  │
│  └──────────────┘  └──────────────┘  └───────────────┬───────────────┘  │
│  ┌──────────────────────────────────┐                │                  │
│  │      Shared Agent Memory         │                │                  │
│  │  (episodic / coordination /      │                │                  │
│  │   knowledge — Redis + Qdrant)    │                │                  │
│  └──────────────────────────────────┘                │                  │
└──────────────────────────────────────────────────────┼──────────────────┘
                                                       │
        ┌──────────────────┬──────────────────┬────────┴────────┬──────────────────┐
        ▼                  ▼                  ▼                 ▼                  ▼
┌───────────────┐ ┌─────────────────────┐ ┌──────────────┐ ┌────────────┐ ┌──────────────┐
│  devops-tool  │ │ devops-observa-     │ │ prometheus-  │ │ jira-tool  │ │  slack-tool  │
│   (kubectl)   │ │ bility-tool         │ │ query-tool   │ │            │ │              │
│               │ │ (Loki + Jaeger)     │ │              │ │            │ │              │
└───────────────┘ └─────────────────────┘ └──────────────┘ └────────────┘ └──────────────┘
```

### How It Works

1. **User sends a message** via the Chat UI or API
2. **Session store** retrieves conversation history from Redis (DB 2)
3. **Shared memory hooks** enrich the request with relevant episodic events (e.g., recent JIRA ticket for the same entity)
4. **AI Orchestrator** analyzes the query and plans which tools to call
5. **HITL gate** intercepts step-sensitive capabilities (rollout restart, scale deployment, delete pod by default) and pauses execution for human approval. Raw `kubectl_command` is **not** gated by default — add it to `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` if you want the same protection.
6. **Tools are executed** (potentially in parallel) to gather data or take action
7. **AI synthesizes** a natural-language response from tool results
8. **Response streams** back to the user via SSE in real-time
9. **Conversation is saved** to the session and episodic events are persisted for future recall

### Data Isolation

| Data Type | Redis Database | Key Pattern |
|-----------|----------------|-------------|
| Service Registry | DB 0 | `truvag3:services:*` |
| Shared Agent Memory (episodic + coordination) | DB 0 (shares the default DB with the registry, isolated by key prefix) | `truvag3:memory:*` |
| Chat Sessions | DB 2 | `truvag3:sessions:*` |
| HITL Checkpoints & Commands | DB 6 (override with `TRUVAG3_HITL_REDIS_DB`) | `truvag3:hitl:*` (scoped per agent name) |
| LLM Debug Records | DB 7 | `llm_debug:*` |

---

## API Reference

### `POST /chat/stream`

Main streaming chat endpoint using Server-Sent Events.

**Request:**
```json
{
  "session_id": "optional-existing-session-id",
  "message": "Which pods in truvag3-examples are not Ready and what do their logs say?"
}
```

**SSE Events:**

| Event | Description | Data |
|-------|-------------|------|
| `session` | New session created (emitted when the caller omitted `session_id` or sent an expired one) | `{"id": "uuid"}` |
| `status` | Progress update | `{"step": "planning", "message": "..."}` |
| `step` | Tool execution complete | `{"step_id": "step_1", "tool": "devops-tool", "success": true, "duration_ms": 234}` |
| `chunk` | Response text chunk | `{"text": "The cluster..."}` |
| `checkpoint` | HITL gate hit — execution paused | `{"checkpoint_id": "...", "interrupt_point": "...", "current_step": {...}}` |
| `usage` | Token usage stats | `{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}` |
| `finish` | LLM finish reason | `{"reason": "stop"}` |
| `done` | Request complete | `{"request_id": "...", "tools_used": [...], "total_duration_ms": 1234}` |
| `error` | Error occurred | `{"code": "...", "message": "...", "retryable": true}` |

**Example with curl:**
```bash
curl -N -X POST http://devops-chat-agent.localhost/chat/stream \
  -H "Content-Type: application/json" \
  -d '{"message": "What is the cluster status?"}'
```

### `POST /query`

Non-streaming agent-as-tool endpoint (`devops_operations` capability). Other agents can discover this capability via Redis and delegate end-to-end DevOps tasks to this agent.

**Request:**
```json
{
  "query": "Restart the currency-tool deployment and verify the new pods come up Ready",
  "session_id": "optional-session-id"
}
```

**Response:** Full `OrchestratorResponse` — `request_id`, `response`, `tools_used`, `confidence`, `execution_time`, `steps`, `usage`, `usage_by_phase`.

### HITL Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `GET /hitl/checkpoints` | List pending checkpoints |
| `GET /hitl/checkpoints/{id}` | Inspect a single checkpoint |
| `POST /hitl/command` | Submit `approve` / `reject` / `edit` / `abort` decision |
| `POST /hitl/resume/{id}` | Resume execution after approval (SSE response) |
| `POST /hitl/auto-resume/{id}/stream` | Auto-resume for `expired_approved` checkpoints (SSE) |

### Session Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `POST /chat/session` | Create a new chat session |
| `GET /chat/session/{id}` | Get session metadata |
| `GET /chat/session/{id}/history` | Retrieve conversation history |
| `GET /chat/sessions` | List the caller's sessions |
| `POST /chat/session/{id}/title` | Update session title |
| `POST /chat/session/delete` | Delete a session |

### `POST /api/v1/scheduled`

Stateless one-shot orchestration endpoint mounted by `orchestration.RegisterScheduledEndpoint`. This is what the [scheduler-tool](../scheduler-tool/README.md) calls when a cron entry fires — it submits the saved natural-language query and the agent runs the full plan / execute / synthesize pipeline against the current cluster state. The response shape matches `POST /query`.

### `GET /health`

Health check endpoint.

**Response:**
```json
{
  "status": "healthy",
  "timestamp": 1716220800,
  "service": "devops-chat-agent",
  "redis": "healthy",
  "ai_provider": "connected",
  "orchestrator": {
    "status": "active",
    "total_requests": 10,
    "successful_requests": 9,
    "failed_requests": 1,
    "average_latency_ms": 842
  },
  "active_sessions": 3
}
```

The handler downgrades `status` to `degraded` and returns `503` when Redis discovery is unavailable. `orchestrator` is the string `"initializing"` (not an object) until the background goroutine in `main.go` finishes wiring Discovery → orchestrator. HITL state is **not** reported here; check `/hitl/checkpoints` if you need to see whether the controller has pending decisions.

### `GET /discover`

List available tools discovered by the orchestrator.

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8357` | Yes |
| `OPENAI_API_KEY` | OpenAI API key | - | Yes* |
| `ANTHROPIC_API_KEY` | Anthropic API key | - | Yes* |
| `GROQ_API_KEY` | Groq API key | - | Yes* |
| `APP_ENV` | Environment (development/staging/production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP endpoint for telemetry | - | No |
| `NAMESPACE` | Kubernetes namespace | `truvag3-examples` | No |
| `TRUVAG3_HITL_ENABLED` | Enable Human-in-the-Loop gates | `true` | No |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | Comma-separated capability names that require approval | `rollout_restart,scale_deployment,delete_pod` | No |
| `TRUVAG3_HITL_DEFAULT_TIMEOUT` | How long to wait for a human decision | `5m` | No |
| `TRUVAG3_HITL_DEFAULT_ACTION` | What to do if the timer fires (`reject` / `approve`) | `reject` | No |
| `TRUVAG3_HITL_WEBHOOK_URL` | Internal webhook URL the controller calls when an interrupt fires | (derived from `PORT`) | No |
| `TRUVAG3_VECTOR_DB_URL` | Qdrant URL (enables long-term knowledge memory) | - | No |
| `TRUVAG3_EMBEDDING_BASE_URL` | Embedding endpoint (Ollama / OpenAI-compatible) | - | No |
| `TRUVAG3_LLM_DEBUG_ENABLED` | Enable LLM debug payload capture | `false` | No |
| `TRUVAG3_LLM_DEBUG_TTL` | TTL for successful debug records | `24h` | No |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | TTL for error debug records | `168h` | No |
| `TRUVAG3_SKILLS_ENABLED` | Enable the framework skill runtime | `true` in this example | No |
| `TRUVAG3_SKILL_BINDINGS_JSON` | Complete replacement for the code binding list | (code bindings) | No |
| `TRUVAG3_SKILLS_REDIS_DB` | Included skill-registry Redis database | `9` | No |

*At least one AI provider key is required.

### Agent Skills

The agent code explicitly binds three reusable packages:

- `devops/kubernetes-safety` — `always` and required; reinforces evidence-first
  Kubernetes investigation and preserves HITL authority for mutations.
- `devops/observability-investigation` — `auto`; adds log, trace, and metric
  correlation guidance only when the request needs it.
- `devops/infrastructure-change-documentation` — `auto`; produce concise
  change records and include links returned by incident systems.

`setup.sh deploy`, `rebuild`, and `rollout` validate and conditionally publish
the JSON packages under `skills/` through
`http://registry.localhost/api/v1/skills` before restarting the agent. Existing
content is updated with its current ETag, so repeated setup runs are safe.
The same reconciliation is part of `full-deploy`, so an empty registry in a
new Kind cluster is rebuilt from the Git-authored packages.

To inspect or reconcile skills without building an image or restarting the
agent:

```bash
./setup.sh skills-check  # Read-only comparison with Git
./setup.sh skills-sync   # Create/update packages, then verify them
```

`skills-sync` skips equivalent content and publishes changed behavior as the
next immutable version. It never clears the shared skill store.
If the management host uses another address, set the setup-only
`TRUVAG3_SKILLS_API_URL` to the full `/api/v1/skills` base URL.
Code owns the default bindings; `TRUVAG3_SKILL_BINDINGS_JSON` is an explicit complete
deployment replacement, never a replica-local merge.

Every request performs one batched binding resolution and pins exact versions.
Later phases re-evaluate activation/resource relevance but never switch the
pinned revision. Skills add guidance only: they do not grant a Kubernetes
capability or bypass plan/step approval. Inspect management state and body-free
execution evidence in the Registry Viewer **Skills** views.

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

The `.env.example` file contains comprehensive documentation for all options including:

- **AI Provider Keys** - Supports provider chain for failover (OpenAI → Anthropic → Groq)
- **Model Aliases** - Override default/smart/fast model mappings per provider
- **Orchestration Settings** - Mode, capability matching thresholds
- **HITL Settings** - Sensitive capability list, timeouts, default action
- **Shared Memory** - Vector DB + embedding endpoint for Phase 2 knowledge retention
- **Telemetry Configuration** - Environment profiles and OTLP endpoints

At minimum, set one AI provider API key (the variables are already uncommented in `.env.example`).

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

---

## Human-in-the-Loop (HITL)

This agent is HITL-enabled by default. Any plan step whose capability matches `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` is intercepted before execution; the orchestrator emits a `checkpoint` SSE event, persists the checkpoint to Redis (DB 6), and waits for a decision via the HITL endpoints.

### Default Sensitive Capabilities

| Capability | Why it's gated |
|------------|----------------|
| `rollout_restart` | Causes pod churn — capacity dips while new pods come up |
| `scale_deployment` | Resource impact, possible service degradation if scaled down |
| `delete_pod` | Forces eviction, can trigger cascading restarts |

> **Heads-up:** `kubectl_command` (raw `kubectl` execution via devops-tool) has the broadest blast radius of any capability the agent can call, but is **not** in the default list. If you're running this against a real cluster, consider adding it to `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` so arbitrary `kubectl` commands require human approval.

Adjust the list via `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` (comma-separated, no spaces). Set `TRUVAG3_HITL_ENABLED=false` to disable HITL entirely.

### Approval Flow

1. Plan reaches a sensitive step → orchestrator returns `ErrInterrupted`
2. SSE callback emits a `checkpoint` event with the `checkpoint_id`, the step about to run, and resolved parameters
3. Operator opens the HITL Console (`http://chat.localhost/hitl.html`), reviews the step, and clicks **Approve** or **Reject**
4. The decision is posted to `/hitl/command`; if approved, the UI calls `/hitl/resume/{id}` and the rest of the plan streams back in the same SSE channel
5. If no decision arrives within `TRUVAG3_HITL_DEFAULT_TIMEOUT`, the checkpoint expires and the configured `TRUVAG3_HITL_DEFAULT_ACTION` is applied

Checkpoints carry `original_request_id` and `original_trace_id` so a multi-gate plan stitches together cleanly in Jaeger.

---

## Shared Agent Memory

The agent participates in a cross-agent memory system that's useful for incident workflows:

| Layer | Backend | What it stores |
|-------|---------|----------------|
| **Episodic events** | Redis (`truvag3:memory:{domain}:events:*`) | Every significant action (rollout, scaling, JIRA ticket creation, Slack notification) is recorded with the entity it touched |
| **Coordination signals** | Redis (`truvag3:memory:{domain}:investigating:*`) | Real-time "currently investigating X" signals so two agents don't duplicate work |
| **Activity digest cache** | Redis (`truvag3:memory:{domain}:digest`) | Short-TTL summary of recent activity per entity |
| **Long-term knowledge** (Phase 2) | Qdrant + embeddings | Curated lessons extracted by the Reflection Job — failure patterns, runbooks, post-incident notes |

The agent runs with `domain = "infrastructure"` (set in `main.go`), so its memory keys live under `truvag3:memory:infrastructure:*` in the same Redis DB used for service discovery.

Phase 1 (episodic + coordination) is always on when Redis is configured. Phase 2 (knowledge search and the Reflection Job) activates automatically when both `TRUVAG3_VECTOR_DB_URL` and `TRUVAG3_EMBEDDING_BASE_URL` are set in `.env`.

Memory hooks inject relevant prior events into the prompt as `<agent_memory>` so the planner doesn't recreate a JIRA ticket that already exists for the same pod. See [agentic-memory-tool](../agentic-memory-tool/README.md) for a tool view of the same data.

---

## Session Management

Sessions are stored in Redis (DB 2) with the following characteristics:

| Property | Value |
|----------|-------|
| **TTL** | 48 hours of inactivity |
| **Max Messages** | 50 per session (sliding window) |
| **Storage** | Redis DB 2 (`truvag3:sessions:*`) |
| **Multi-pod Support** | Yes (shared Redis) |

### Session Flow

1. Client sends first message without `session_id`
2. Server creates session, stores in Redis, returns `session_id` via SSE
3. Client includes `session_id` in subsequent requests
4. Server retrieves history from Redis for conversation context
5. Session expires after 48 hours of inactivity (longer than travel-chat-agent's 30 minutes — DevOps investigations span hours or days, often paused for HITL approval)

---

## Telemetry

The agent includes comprehensive observability:

### Tracing (Jaeger)

- All requests traced with span events
- Tool execution timing
- HITL wait spans (`hitl.delegation.wait_started` → `hitl.delegation.wait_completed`)
- Error tracking and debugging
- Access at http://jaeger.localhost

### LLM Debug Payload Store

For debugging orchestration issues, enable the LLM Debug Store to capture complete prompts and responses (Jaeger truncates large payloads):

```bash
export TRUVAG3_LLM_DEBUG_ENABLED=true
```

This captures all LLM interactions at 6 recording sites (`plan_generation`, `correction`, `synthesis`, `synthesis_streaming`, `micro_resolution`, `semantic_retry`), plus background calls from the Reflection Job and Knowledge Extraction hooks. Records are stored in Redis DB 7 with configurable TTL.

### Metrics (Prometheus/Grafana)

| Metric | Type | Description |
|--------|------|-------------|
| `chat.request.duration_ms` | Histogram | Request duration |
| `chat.requests` | Counter | Total requests |
| `chat.sessions.active` | Gauge | Active sessions |
| `chat.orchestration.tool_calls` | Counter | Tool calls by tool name |
| `devops_chat_agent.startup` | Counter | Startup events |

Access Grafana at http://grafana.localhost (admin/admin)

### Logging

Structured JSON logs with component attribution and trace context. Request-scoped logs include `trace.trace_id` and `trace.span_id` for distributed tracing:

```json
{
  "component": "agent/devops-chat-agent",
  "level": "INFO",
  "message": "Processing chat request",
  "operation": "process_chat",
  "service": "devops-chat-agent",
  "session_id": "f2fac72e-2691-4dd5-a57d-709582879663",
  "query_len": 62,
  "history_turns": 1,
  "timestamp": "2026-05-20T16:31:51Z",
  "trace.span_id": "0b319744acd226d5",
  "trace.trace_id": "445c352173a351de293d4d27416b0eb2"
}
```

---

## Project Structure

```
devops-chat-agent/
├── main.go              # Entry point and initialization (memory + HITL wiring)
├── chat_agent.go        # Agent with orchestration integration + DevOps prompt
├── sse_handler.go       # SSE streaming handler
├── session.go           # Redis-backed session management
├── handlers.go          # HTTP handlers (health, session, discover, query)
├── handlers_hitl.go     # HITL resume / auto-resume SSE handlers
├── hitl_setup.go        # HITL infrastructure (checkpoint store, controller, policy)
├── go.mod               # Go module definition
├── Dockerfile           # Production container image
├── Dockerfile.workspace # Development container with local modules
├── k8-deployment.yaml   # Kubernetes deployment manifest
├── setup.sh             # Build and deployment script
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

**2. "Orchestrator is initializing" error**

The orchestrator needs time to discover tools. Wait a few seconds and retry, then confirm tools are discoverable:
```bash
curl -s http://devops-chat-agent.localhost/discover | jq .
```

**3. No tools discovered**

Ensure tools are registered with Redis:
```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 0 KEYS 'truvag3:services:*'
```

**4. Plan hangs without responding**

It's probably waiting on HITL approval. Check pending checkpoints:
```bash
curl -s http://devops-chat-agent.localhost/hitl/checkpoints | jq .
# Or open the console:
open http://chat.localhost/hitl.html
```

Then either approve via the UI or:
```bash
curl -X POST http://devops-chat-agent.localhost/hitl/command \
  -H "Content-Type: application/json" \
  -d '{"checkpoint_id":"<ID>","type":"approve"}'
```

**5. Ingress route not reachable**

Check the ingress controller is running and the ingress resource exists:
```bash
# Check ingress controller
kubectl get pods -n ingress-nginx

# Check ingress routes
kubectl get ingress -n truvag3-examples

# Verify *.localhost resolves
curl -v http://devops-chat-agent.localhost/health
```

### Useful Commands

```bash
# View agent logs (also the quickest way to verify the pod is up)
./setup.sh logs

# Check ingress routes
kubectl get ingress -n truvag3-examples

# Check Redis session data
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 2 KEYS 'truvag3:sessions:*'

# Check pending HITL checkpoints
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 6 KEYS 'truvag3:hitl:*'

# Inspect shared episodic memory (same DB as the service registry)
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 0 KEYS 'truvag3:memory:infrastructure:*'

# Test the API
./setup.sh test

# Pick up new .env / ConfigMap changes (restart only — no rebuild)
./setup.sh rollout

# Rebuild image with --no-cache and redeploy (use for code changes)
./setup.sh rebuild

# Full cleanup
./setup.sh cleanup-all
```

---

## Related Examples

- [chat-ui](../chat-ui/) - Web frontend (DevOps view at `/devops.html`, HITL console at `/hitl.html`)
- [agent-with-human-approval](../agent-with-human-approval/) - Minimal HITL example
- [agent-with-orchestration](../agent-with-orchestration/) - Basic orchestration example
- [agent-with-telemetry](../agent-with-telemetry/) - Full observability example
- [travel-chat-agent](../travel-chat-agent/) - Sister agent for travel / general-utility queries
- [devops-tool](../devops-tool/) - `kubectl` operations
- [devops-observability-tool](../devops-observability-tool/) - Loki / Jaeger queries
- [prometheus-query-tool](../prometheus-query-tool/) - PromQL queries
- [jira-tool](../jira-tool/) - Ticket management
- [slack-tool](../slack-tool/) - Incident notifications
- [agentic-memory-tool](../agentic-memory-tool/) - Cross-agent memory access

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
