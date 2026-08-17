# Travel Chat Agent

A streaming chat agent that demonstrates AI-powered orchestration using the TruvaG3 framework. It provides real-time Server-Sent Events (SSE) responses by intelligently coordinating multiple travel-related tools to answer user queries about weather, locations, currencies, and country information.

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
- [User Memory](#user-memory)
- [Session Management](#session-management)
- [Telemetry](#telemetry)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

Running this example locally is the best way to understand how the TruvaG3 framework orchestrates tools and agents. Follow the steps below to get this example running.

### Prerequisites

Before running this example in your local machine, ensure you have the following installed:

| Requirement | Version | macOS | Windows |
|-------------|---------|-------|---------|
| **Docker Desktop** | Latest | [Download](https://www.docker.com/products/docker-desktop/) | [Download](https://www.docker.com/products/docker-desktop/) |
| **Kind** | v0.20+ | `brew install kind` | `choco install kind` or [Download](https://kind.sigs.k8s.io/docs/user/quick-start/) |
| **kubectl** | v1.28+ | `brew install kubectl` | `choco install kubernetes-cli` or [Download](https://kubernetes.io/docs/tasks/tools/) |
| **Go** | 1.25+ | `brew install go` | `choco install golang` or [Download](https://golang.org/dl/) |
| **AI Provider API Key** | - | At least one: [OpenAI](https://platform.openai.com/api-keys), [Anthropic](https://console.anthropic.com/), [Groq](https://console.groq.com/keys), [Gemini](https://aistudio.google.com/apikey), or any [OpenAI-compatible](#openai-compatible-providers) provider | Same as macOS |

> **Note:** This agent serves as the backend for the [chat-ui](../chat-ui/) example. The chat-ui provides a web interface that connects to this agent's SSE streaming API. While the agent can be used standalone via its REST API, the chat-ui offers a convenient way to interact with it.

> **Important:** The travel-chat-agent requires [tools to be deployed](#required-tools-and-agents) to function. The Core Tools list has eight tools the system prompt names directly (weather, geocoding, currency, country-info, flight, hotel, travel-advisory, places); the bare minimum to demo a "what's the weather in Tokyo?" query is weather-tool-v2 + geocoding-tool. You can deploy tools before or after the agent — the agent simply can't answer queries that need a tool until that tool is running.

### Quick Start (Recommended)

The fastest way to get everything running in your local:

```bash
cd examples/travel-chat-agent

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**⚠️ STOP HERE** - Open `.env` in your editor and configure your API key(s):

```bash
# Open .env in your preferred editor
nano .env    # or: code .env / vim .env
```

At minimum, set ONE provider key in your `.env` file. In `.env.example`, `OPENAI_API_KEY=` and `GROQ_API_KEY=` are already uncommented (blank value — paste your key after the `=`). `ANTHROPIC_API_KEY=` is present but commented out by default; remove the leading `#` if you want to use Anthropic.
- `OPENAI_API_KEY=sk-your-key`
- `ANTHROPIC_API_KEY=sk-ant-your-key`
- `GROQ_API_KEY=gsk_your-key`

> **Note:** Multiple providers enable automatic failover.
>
> **Important:** Leave unused providers blank — do **not** paste placeholder strings like `your-key`. The AI module treats any non-empty value as "this provider is configured" and will route calls to it, which fails the request when the key isn't real.

```bash
# 2. Deploy cluster, infrastructure, and the chat agent
./setup.sh full-deploy

# 3. Deploy the required tools (each tool has its own setup script).
#    Each line returns to examples/travel-chat-agent so the next `cd ../...`
#    resolves correctly. Run from the examples/travel-chat-agent directory.
#    Start with these five to cover weather + country/currency lookups
#    and time-aware queries (the agent has no built-in clock):
cd ../weather-tool-v2 && ./setup.sh deploy && cd ../travel-chat-agent
cd ../geocoding-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../currency-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../country-info-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../system-utilities-tool && ./setup.sh deploy && cd ../travel-chat-agent

#    Add these to unlock flight, hotel, advisory, and places queries:
cd ../flight-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../hotel-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../travel-advisory-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../places-tool && ./setup.sh deploy && cd ../travel-chat-agent
```

**What `./setup.sh full-deploy` does:**
1. Creates a Kind Kubernetes cluster with NGINX Ingress Controller
2. Deploys infrastructure (Redis, Prometheus, Grafana, Jaeger, OTEL Collector)
3. Builds and loads the travel-chat-agent and chat-ui images
4. Recreates or updates agent-owned skills, then deploys the agent and UI
5. Configures Ingress routes, verifies services, and prints their URLs

> **Note:** All services are accessible via `*.localhost` hostnames through the NGINX Ingress Controller. No port-forwarding is needed. On macOS/Linux, `*.localhost` resolves to `127.0.0.1` automatically ([RFC 6761](https://tools.ietf.org/html/rfc6761)). This is the same pattern used in production with real DNS on EKS/GKE/AKS.

**What you need to do separately:**
- Deploy tools using each tool's setup script (Step 3 above)

Once complete, access the application at:

| Service | URL | Description |
|---------|-----|-------------|
| **Chat UI** | http://chat.localhost | Web interface for chatting |
| **Chat API** | http://travel-chat-agent.localhost | Backend REST/SSE API |
| **Jaeger** | http://jaeger.localhost | Distributed tracing |
| **Grafana** | http://grafana.localhost | Metrics dashboard (admin/admin) |
| **Prometheus** | http://prometheus.localhost | Metrics queries |

### Step-by-Step Deployment

If you prefer to understand each step or need more control:

#### Step 1: Create the Kubernetes Cluster

```bash
cd examples/travel-chat-agent
./setup.sh cluster
```

This creates a Kind cluster named `truvag3-demo-<username>` with NGINX Ingress Controller support (ports 80/443 only).

#### Step 2: Deploy Infrastructure

```bash
./setup.sh infra
```

This deploys the shared infrastructure components:
- **Redis** - Service discovery and session storage
- **OTEL Collector** - Telemetry aggregation
- **Prometheus** - Metrics storage
- **Jaeger** - Distributed tracing
- **Grafana** - Visualization dashboards

#### Step 3: Deploy the Tools (Important!)

**The travel-chat-agent requires tools to be deployed first.** Without tools, the agent has no capabilities to orchestrate.

Each tool has its own setup script:

```bash
# Run from examples/travel-chat-agent. Each line returns there so the next
# `cd ../...` resolves correctly.
cd ../weather-tool-v2 && ./setup.sh deploy && cd ../travel-chat-agent
cd ../geocoding-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../currency-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../country-info-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../system-utilities-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../flight-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../hotel-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../travel-advisory-tool && ./setup.sh deploy && cd ../travel-chat-agent
cd ../places-tool && ./setup.sh deploy && cd ../travel-chat-agent
```

> **Note:** The `k8-deployment` directory contains shared infrastructure (Redis, Prometheus, etc.), not tools.

#### Step 4: Deploy the Chat Agent

```bash
cd examples/travel-chat-agent

# Create .env from example and configure your API key
cp .env.example .env
# Edit .env: set ONE provider key, leave the other providers blank. Do not paste
# placeholder strings — any non-empty value activates that provider, even if the
# key isn't real, which will fail your request. OPENAI_API_KEY and GROQ_API_KEY
# are already uncommented; uncomment ANTHROPIC_API_KEY only if you want to use it.

# Build the image and deploy in one step.
# (`./setup.sh deploy` runs build_docker → load_to_kind → deploy_k8s;
#  there is no separate "deploy only, skip build" subcommand.)
./setup.sh deploy
```

#### Step 5: Verify Ingress Routes

```bash
./setup.sh verify
```

All services should be accessible via `*.localhost` URLs listed above.

---

## Required Tools and Agents

The travel-chat-agent orchestrates multiple tools to answer user queries. **These tools must be running for the agent to function.**

### Core Tools

These tools are referenced by name in the agent's system prompt — the agent expects them to be deployed for full functionality.

| Tool | Purpose | Port | Documentation |
|------|---------|------|---------------|
| **weather-tool-v2** | Weather data (current, forecast) | 8339 | [README](../weather-tool-v2/README.md) |
| **geocoding-tool** | Location coordinates lookup (used before any weather query) | 8335 | [README](../geocoding-tool/README.md) |
| **currency-tool** | Currency exchange rates | 8334 | [README](../currency-tool/README.md) |
| **country-info-tool** | Country metadata (capital, languages, calling code, currency code) | 8333 | [README](../country-info-tool/README.md) |
| **flight-tool** | `search_airports` (IATA resolution) and flight search | 8342 | [README](../flight-tool/README.md) |
| **hotel-tool** | Hotel search by city IATA code | 8343 | [README](../hotel-tool/README.md) |
| **travel-advisory-tool** | `get_travel_advisory` — country safety information | 8345 | [README](../travel-advisory-tool/README.md) |
| **places-tool** | `search_places` and `nearby_places` for dining and activities at a destination | 8344 | [README](../places-tool/README.md) |

### Optional Tools

These extend the agent's reach beyond the prompt's explicit instructions.

| Tool | Purpose | Documentation |
|------|---------|---------------|
| **system-utilities-tool** | Current time, timezone conversion, date math (e.g., "what time is it in Tokyo?", "if I leave NYC at 9am, what time is that in London?"). Recommended — the agent has no built-in clock. | [README](../system-utilities-tool/README.md) |
| **news-tool** | News articles (e.g., "any news about Tokyo before I travel?") | [README](../news-tool/README.md) |
| **stock-market-tool** | Stock prices | [README](../stock-market-tool/README.md) |
| **grocery-tool** | Grocery store API | [README](../grocery-tool/README.md) |

### Sibling Agent (DevOps Delegation)

The travel-chat-agent's prompt instructs it to delegate any Kubernetes / cluster / DevOps query to the `devops_operations` capability exposed by [devops-chat-agent](../devops-chat-agent/README.md). This is purely an opportunistic delegation — if the devops-chat-agent isn't deployed, those queries simply fail; travel-related queries are unaffected.

### Related Agents

| Agent | Purpose | Documentation |
|-------|---------|---------------|
| **agent-with-telemetry** | Example with full observability | [README](../agent-with-telemetry/README.md) |
| **agent-with-orchestration** | Basic orchestration example | [README](../agent-with-orchestration/README.md) |
| **agent-with-resilience** | Resilience patterns example | [README](../agent-with-resilience/README.md) |

### Deploying Tools

Each tool has its own `setup.sh` script with similar commands:

```bash
# Example: Deploy weather-tool-v2
cd examples/weather-tool-v2
./setup.sh deploy       # Deploy to Kubernetes
./setup.sh run          # Run locally
./setup.sh help         # See all options
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              User Browser                                │
│                          http://chat.localhost                           │
└────────────────────────────────┬────────────────────────────────────────┘
                                 │ SSE Stream
                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                         travel-chat-agent                                │
│                          (Port 8356)                                     │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────────────────────┐  │
│  │   Session    │  │     SSE      │  │        AI Orchestrator        │  │
│  │    Store     │  │   Handler    │  │   (Plan → Execute → Synth)    │  │
│  │  (Redis DB2) │  │              │  │                               │  │
│  └──────────────┘  └──────────────┘  └───────────────┬───────────────┘  │
│  ┌──────────────────────────────────┐                │                  │
│  │        User Memory               │                │                  │
│  │   (per-user facts injected as    │                │                  │
│  │    <user_profile> — Qdrant)      │                │                  │
│  └──────────────────────────────────┘                │                  │
└──────────────────────────────────────────────────────┼──────────────────┘
                                                       │
                    ┌──────────────────────────────────┼──────────────────────────────────┐
                    │                                  │                                  │
                    ▼                                  ▼                                  ▼
          ┌─────────────────┐              ┌─────────────────┐              ┌─────────────────┐
          │ weather-tool-v2 │              │  geocoding-tool │              │  currency-tool  │
          │   (Port 8339)   │              │   (Port 8335)   │              │   (Port 8334)   │
          └─────────────────┘              └─────────────────┘              └─────────────────┘
```

### How It Works

1. **User sends a message** via the Chat UI or API
2. **Session store** retrieves conversation history from Redis (DB 2)
3. **AI Orchestrator** analyzes the query and plans which tools to call
4. **Tools are executed** (potentially in parallel) to gather data
5. **AI synthesizes** a natural language response from tool results
6. **Response streams** back to the user via SSE in real-time
7. **Conversation is saved** to the session for context continuity

### Data Isolation

| Data Type | Backend | Location |
|-----------|---------|----------|
| Service Registry | Redis | DB 0, keys `truvag3:services:*` |
| Chat Sessions | Redis | DB 2, keys `truvag3:sessions:*` |
| LLM Debug Records | Redis | DB 7, keys `llm_debug:*` |
| User Memory (per-user facts) | Qdrant | Collection `truvag3_user_memory` (overridable via `TRUVAG3_USER_MEMORY_COLLECTION`); falls back to in-memory when Qdrant isn't configured |

---

## API Reference

### `POST /chat/stream`

Main streaming chat endpoint using Server-Sent Events.

**Request:**
```json
{
  "session_id": "optional-existing-session-id",
  "message": "What is the weather in Tokyo?"
}
```

**SSE Events:**

| Event | Description | Data |
|-------|-------------|------|
| `session` | New session created (emitted when the caller omitted `session_id` or sent an expired one) | `{"id": "uuid"}` |
| `status` | Progress update | `{"step": "planning", "message": "..."}` |
| `step` | Tool execution complete | `{"step_id": "step_1", "tool": "weather-tool-v2", "success": true, "duration_ms": 234}` |
| `chunk` | Response text chunk | `{"text": "The weather..."}` |
| `usage` | Token usage stats (`by_phase` is included only when the orchestrator tracked per-phase usage) | `{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150, "by_phase": {...}}` |
| `finish` | LLM finish reason | `{"reason": "stop"}` |
| `done` | Request complete | `{"request_id": "...", "tools_used": [...], "total_duration_ms": 1234}` |
| `error` | Error occurred | `{"code": "...", "message": "...", "retryable": true}` |

**Example with curl:**
```bash
curl -N -X POST http://travel-chat-agent.localhost/chat/stream \
  -H "Content-Type: application/json" \
  -d '{"message": "What is the weather in Tokyo?"}'
```

### `POST /chat/session`

Create a new chat session.

**Response:**
```json
{
  "session_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "created_at": "2024-01-01T00:00:00Z",
  "expires_at": "2024-01-03T00:00:00Z"
}
```

### `GET /chat/session/{id}/history`

Get conversation history for a session.

### `POST /query`

Non-streaming orchestration endpoint registered as the `travel_query` capability ([chat_agent.go:607-617](chat_agent.go#L607)). Marked `Internal: true`, so it is **not** advertised to other agents' orchestrators for discovery-based delegation (the travel-chat-agent is intended as a leaf, not as a delegated sub-agent); however, any external client can still call this endpoint directly via HTTP.

**Request:**
```json
{
  "query": "Find flights from NYC to Tokyo next month, check the weather there, and convert 2000 USD to JPY"
}
```

**Response:** Full `OrchestratorResponse` — `request_id`, `response`, `tools_used`, `confidence`, `execution_time`, `steps`, `usage`.

### `POST /api/v1/scheduled`

Stateless one-shot orchestration endpoint, mounted by `orchestration.RegisterScheduledEndpoint` in [main.go:107](main.go#L107). The [scheduler-tool](../scheduler-tool/README.md) calls this when a cron entry fires — useful for periodic itinerary refreshes, recurring price checks, or "tell me the weather in my saved destinations every Monday" workflows. Request and response shapes match `POST /query` above (no SSE).

### `GET /health`

Health check endpoint.

**Response:**
```json
{
  "status": "healthy",
  "timestamp": 1716220800,
  "service": "travel-chat-agent",
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

The handler downgrades `status` to `degraded` and returns `503` when Redis discovery is unavailable. `orchestrator` is the string `"initializing"` (not an object) until the background goroutine in `main.go` finishes wiring Discovery → orchestrator.

### `GET /discover`

List available tools discovered by the orchestrator.

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `OPENAI_API_KEY` | OpenAI API key | - | Yes* |
| `ANTHROPIC_API_KEY` | Anthropic API key | - | Yes* |
| `GROQ_API_KEY` | Groq API key | - | Yes* |
| `PORT` | HTTP server port | `8356` | No |
| `APP_ENV` | Environment (development/staging/production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP endpoint for telemetry | - | No |
| `NAMESPACE` | Kubernetes namespace | `truvag3-examples` | No |
| `TRUVAG3_LLM_DEBUG_ENABLED` | Enable LLM debug payload capture | `false` | No |
| `TRUVAG3_LLM_DEBUG_TTL` | TTL for successful debug records | `24h` | No |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | TTL for error debug records | `168h` | No |
| `TRUVAG3_SKILLS_ENABLED` | Enable the framework skill runtime | `true` in this example | No |
| `TRUVAG3_SKILL_BINDINGS_JSON` | Complete replacement for the code binding list | (code bindings) | No |
| `TRUVAG3_SKILLS_REDIS_DB` | Included skill-registry Redis database | `9` | No |

*At least one AI provider key is required.

### Agent Skills

The agent code explicitly binds five reusable packages:

- `travel/action-verification` — `always`; verify live sources before acting
  on travel information.
- `travel/travel-search-preparation` — `auto`; prepare live travel searches
  without hardcoding capability names.
- `travel/currency-conversion` — `auto`; guide exchange-rate and conversion
  requests toward current data and clear assumptions.
- `travel/travel-readiness-assessment` — `auto`; combine material trip risks
  and readiness checks when the request needs them.
- `travel/weather-assessment` — `auto`; add weather-risk planning and response
  guidance only when relevant.

`setup.sh deploy`, `rebuild`, and `rollout` discover, validate, and
conditionally publish the JSON packages under
`skills/packages/<namespace>/<name>.json` through
`http://registry.localhost/api/v1/skills` before restarting the agent. This
automatic step is best-effort: an unavailable API or invalid package produces
a warning, but deployment continues. Existing content is updated using its
current ETag, so repeated setup runs are safe.
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
Set `TRUVAG3_SKIP_SKILLS_SYNC=true` when the setup host intentionally cannot
reach that API. This skips only automatic deployment synchronization;
`skills-sync` and `skills-check` remain strict and return a non-zero status on
failure or drift.
Agent replicas do not receive mutable binding API calls: code owns the default
list, and `TRUVAG3_SKILL_BINDINGS_JSON` can replace it for the whole deployment.

At request start, all bindings resolve in one Redis-backed batch and exact
versions are pinned. A newly published version therefore appears on the next
request without Pub/Sub, while an in-flight multi-phase execution stays on its
pinned version. Inspect packages in the Registry Viewer **Skills** view and
execution decisions in an execution's **Skills** tab.

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

The `.env.example` file contains comprehensive documentation for all options including:

- **AI Provider Keys** - Supports provider chain for failover (OpenAI → Anthropic → Groq)
- **Model Aliases** - Override default/smart/fast model mappings per provider
- **Orchestration Settings** - Mode, capability matching thresholds
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

## User Memory

The agent wires per-user private memory via `BuildUserMemoryHooks` ([chat_agent.go:239](chat_agent.go#L239)). Facts mentioned in a conversation (preferred airlines, dietary restrictions, home airport, past destinations, etc.) are extracted by a background LLM call and recalled in future sessions, so the user doesn't have to repeat themselves.

| Aspect | Details |
|--------|---------|
| **Hook** | `UserMemoryEnrichmentHook` (`BeforePlanning`) — injects relevant facts into the planning prompt as `<user_profile>` |
| **Namespace** | `travel` (set via `memory.WithUserMemoryNamespace("travel")`) |
| **Backend** | Qdrant when `TRUVAG3_VECTOR_DB_URL` + `TRUVAG3_EMBEDDING_BASE_URL` are set; falls back to in-memory otherwise |
| **Extraction** | Background LLM call after each turn — uses the model named by `TRUVAG3_USER_MEMORY_EXTRACTION_MODEL` (default: the agent's main model; set to `fast` to save cost) |
| **Recall** | Up to `TRUVAG3_USER_MEMORY_MAX_FACTS_IN_PROMPT` facts (default 15) ranked by semantic similarity to the current query |
| **Collection** | `TRUVAG3_USER_MEMORY_COLLECTION` (default `truvag3_user_memory`) — separate from the shared knowledge collection |

### Setup

The active `.env.example` already enables user memory's prerequisites: `TRUVAG3_DEPLOY_QDRANT=true` makes `./setup.sh infra` provision Qdrant alongside Redis/Prometheus, and `TRUVAG3_EMBEDDING_BASE_URL` points at a local Ollama. To opt out of personalized memory entirely, comment those out — the agent falls back to a stateless in-memory backend that doesn't persist anything across pod restarts.

> **Note:** `TRUVAG3_AGENT_DOMAIN=travel` is left commented out in `.env.example` because travel-chat-agent only wires user memory (`BuildUserMemoryHooks`), not shared cross-agent episodic memory (`BuildMemoryHooks`). Uncomment the variable only after adding `SharedBackends` wiring in `main.go` — until then it would be read by the framework but have no observable effect.

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
5. Session expires after 48 hours of inactivity (long enough that a planning conversation can pick back up across multiple work sessions)

---

## Telemetry

The agent includes comprehensive observability:

### Tracing (Jaeger)

- All requests traced with span events
- Tool execution timing
- Error tracking and debugging
- Access at http://jaeger.localhost

### LLM Debug Payload Store

For debugging orchestration issues, enable the LLM Debug Store to capture complete prompts and responses (Jaeger truncates large payloads):

```bash
export TRUVAG3_LLM_DEBUG_ENABLED=true
```

This captures all LLM interactions at 6 recording sites (`plan_generation`, `correction`, `synthesis`, `synthesis_streaming`, `micro_resolution`, `semantic_retry`) with full payload visibility. Records are stored in Redis DB 7 with configurable TTL.

### Metrics (Prometheus/Grafana)

| Metric | Type | Description |
|--------|------|-------------|
| `chat.request.duration_ms` | Histogram | Request duration |
| `chat.requests` | Counter | Total requests |
| `chat.sessions.active` | Gauge | Active sessions |
| `chat.orchestration.tool_calls` | Counter | Tool calls by tool name |

Access Grafana at http://grafana.localhost (admin/admin)

### Logging

Structured JSON logs with component attribution and trace context. Request-scoped logs include `trace.trace_id` and `trace.span_id` for distributed tracing:

```json
{
  "component": "agent/travel-chat-agent",
  "level": "INFO",
  "message": "Processing chat request",
  "operation": "process_chat",
  "service": "travel-chat-agent",
  "session_id": "f2fac72e-2691-4dd5-a57d-709582879663",
  "query_len": 29,
  "history_turns": 1,
  "timestamp": "2026-01-09T16:31:51Z",
  "trace.span_id": "0b319744acd226d5",
  "trace.trace_id": "445c352173a351de293d4d27416b0eb2"
}
```

```json
{
  "component": "framework/ai",
  "level": "INFO",
  "message": "AI response received",
  "operation": "ai_response",
  "service": "travel-chat-agent",
  "provider": "openai",
  "model": "gpt-4o-mini-2024-07-18",
  "status": "success",
  "prompt_tokens": 4366,
  "completion_tokens": 240,
  "total_tokens": 4606,
  "duration_ms": 7302,
  "tokens_per_second": 630.77,
  "timestamp": "2026-01-09T16:31:59Z",
  "trace.request_id": "orch-1767976311901042840",
  "trace.span_id": "e59c0a1c74b3f996",
  "trace.trace_id": "445c352173a351de293d4d27416b0eb2"
}
```

---

## Project Structure

```
travel-chat-agent/
├── main.go              # Entry point and initialization
├── skills.go            # Skills registry construction
├── chat_agent.go        # Agent with orchestration integration
├── sse_handler.go       # SSE streaming handler
├── session.go           # Redis-backed session management
├── handlers.go          # HTTP handlers (health, session, discover)
├── go.mod               # Go module definition
├── Dockerfile           # Production container image
├── Dockerfile.workspace # Development container with local modules
├── k8-deployment.yaml   # Kubernetes deployment manifest
├── skills/
│   └── packages/travel/ # Git-authored Travel skill packages
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

The orchestrator needs time to discover tools. Wait a few seconds and retry. Stream the agent logs to watch tool discovery, and run `./setup.sh status` from each tool's directory (e.g., `cd ../weather-tool-v2 && ./setup.sh status`):
```bash
./setup.sh logs
```

**3. No tools discovered**

Ensure tools are registered with Redis:
```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 0 KEYS 'truvag3:services:*'
```

**4. Ingress route not reachable**

Check the ingress controller is running and the ingress resource exists:
```bash
# Check ingress controller
kubectl get pods -n ingress-nginx

# Check ingress routes
kubectl get ingress -n truvag3-examples

# Verify *.localhost resolves
curl -v http://travel-chat-agent.localhost/health
```

### Useful Commands

All day-to-day operations go through `setup.sh`. Run `./setup.sh help` to see every subcommand. Travel-chat-agent's `setup.sh` does not expose a `status` subcommand — stream logs to check startup instead.

```bash
# Stream agent logs (use this to verify startup, tool discovery, and request handling)
./setup.sh logs

# Verify all ingress routes (Chat UI, agent API, Grafana, Jaeger, Prometheus)
./setup.sh verify

# Port forward the agent + monitoring dashboards
./setup.sh forward-all

# Restart the deployment (e.g., to pick up a new ConfigMap from .env)
./setup.sh rollout

# Rebuild image + redeploy (use after changing Go code)
./setup.sh rebuild

# Run the built-in smoke test suite against the deployed agent
./setup.sh test

# Remove only the agent (keeps cluster + infra)
./setup.sh cleanup

# Tear down the entire Kind cluster
./setup.sh cleanup-all
```

For low-level introspection that `setup.sh` doesn't wrap:

```bash
# Check ingress routes
kubectl get ingress -n truvag3-examples

# Check Redis session data
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 2 KEYS 'truvag3:sessions:*'
```

---

## Related Examples

- [chat-ui](../chat-ui/) - Web frontend for this agent
- [agent-with-orchestration](../agent-with-orchestration/) - Basic orchestration example
- [agent-with-telemetry](../agent-with-telemetry/) - Full observability example
- [devops-chat-agent](../devops-chat-agent/) - Sibling agent this one delegates DevOps queries to
- [weather-tool-v2](../weather-tool-v2/) - Weather data tool
- [geocoding-tool](../geocoding-tool/) - Location geocoding tool
- [currency-tool](../currency-tool/) - Currency exchange tool
- [country-info-tool](../country-info-tool/) - Country information tool
- [flight-tool](../flight-tool/) - Flight and airport search
- [hotel-tool](../hotel-tool/) - Hotel search by city IATA code
- [travel-advisory-tool](../travel-advisory-tool/) - Country safety advisories
- [places-tool](../places-tool/) - Places of interest and nearby search
- [scheduler-tool](../scheduler-tool/) - Schedules recurring queries via `/api/v1/scheduled`

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
