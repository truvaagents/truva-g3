# Travel Chat Agent

A streaming chat agent that demonstrates AI-powered orchestration using the Truva-G3 framework. It provides real-time Server-Sent Events (SSE) responses by intelligently coordinating multiple travel-related tools to answer user queries about weather, locations, currencies, and country information.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Required Tools and Agents](#required-tools-and-agents)
- [Architecture](#architecture)
- [API Reference](#api-reference)
- [Configuration](#configuration)
  - [OpenAI-Compatible Providers](#openai-compatible-providers)
- [Session Management](#session-management)
- [Telemetry](#telemetry)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

Running this example locally is the best way to understand how the Truva-G3 framework orchestrates tools and agents. Follow the steps below to get this example running.

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

> **Important:** The travel-chat-agent requires [tools to be deployed](#required-tools-and-agents) (weather-tool-v2, geocoding-tool, currency-tool, country-info-tool, news-tool) to function. You can deploy tools before or after the agent, but the agent won't be able to answer queries until tools are running.

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

At minimum, uncomment and set ONE of these in your `.env` file:
- `OPENAI_API_KEY=sk-your-key`
- `ANTHROPIC_API_KEY=sk-ant-your-key`
- `GROQ_API_KEY=gsk_your-key`

> **Note:** Multiple providers enable automatic failover.

```bash
# 2. Deploy cluster, infrastructure, and the chat agent
./setup.sh full-deploy

# 3. Deploy the required tools (each tool has its own setup script)
cd ../weather-tool-v2 && ./setup.sh deploy && cd ..
cd ../geocoding-tool && ./setup.sh deploy && cd ..
cd ../currency-tool && ./setup.sh deploy && cd ..
cd ../country-info-tool && ./setup.sh deploy && cd ..
```

**What `./setup.sh full-deploy` does:**
1. Creates a Kind Kubernetes cluster with NGINX Ingress Controller
2. Deploys infrastructure (Redis, Prometheus, Grafana, Jaeger, OTEL Collector)
3. Builds and deploys the travel-chat-agent and chat-ui
4. Configures Ingress routes and verifies all services are reachable
5. Prints a summary with all service URLs

> **Note:** All services are accessible via `*.localhost` hostnames through the NGINX Ingress Controller. No port-forwarding is needed. On macOS/Linux, `*.localhost` resolves to `127.0.0.1` automatically ([RFC 6761](https://tools.ietf.org/html/rfc6761)). This is the same pattern used in production with real DNS on EKS/GKE/AKS.

**What you need to do separately:**
- Deploy tools using each tool's setup script (Step 4 above)

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
# Deploy tools (from the examples directory)
cd ../weather-tool-v2 && ./setup.sh deploy
cd ../geocoding-tool && ./setup.sh deploy
cd ../currency-tool && ./setup.sh deploy
cd ../country-info-tool && ./setup.sh deploy
```

> **Note:** The `k8-deployment` directory contains shared infrastructure (Redis, Prometheus, etc.), not tools.

#### Step 4: Deploy the Chat Agent

```bash
cd examples/travel-chat-agent

# Create .env from example and configure your API key
cp .env.example .env
# Edit .env and uncomment/set your AI provider key(s)

# Build and deploy
./setup.sh docker
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

| Tool | Purpose | Port | Documentation |
|------|---------|------|---------------|
| **weather-tool-v2** | Weather data (current, forecast) | 8096 | [README](../weather-tool-v2/README.md) |
| **geocoding-tool** | Location coordinates lookup | 8095 | [README](../geocoding-tool/README.md) |
| **currency-tool** | Currency exchange rates | 8097 | [README](../currency-tool/README.md) |
| **country-info-tool** | Country information | 8098 | [README](../country-info-tool/README.md) |
| **news-tool** | News articles | 8099 | [README](../news-tool/README.md) |

### Optional Tools

These provide additional capabilities:

| Tool | Purpose | Documentation |
|------|---------|---------------|
| **stock-market-tool** | Stock prices | [README](../stock-market-tool/README.md) |
| **grocery-tool** | Grocery store API | [README](../grocery-tool/README.md) |

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
│                          http://localhost:8096                           │
└────────────────────────────────┬────────────────────────────────────────┘
                                 │ SSE Stream
                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                         travel-chat-agent                                │
│                          (Port 8095)                                     │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────────────────────┐  │
│  │   Session    │  │     SSE      │  │        AI Orchestrator        │  │
│  │    Store     │  │   Handler    │  │   (Plan → Execute → Synth)    │  │
│  │  (Redis DB2) │  │              │  │                               │  │
│  └──────────────┘  └──────────────┘  └───────────────┬───────────────┘  │
└──────────────────────────────────────────────────────┼──────────────────┘
                                                       │
                    ┌──────────────────────────────────┼──────────────────────────────────┐
                    │                                  │                                  │
                    ▼                                  ▼                                  ▼
          ┌─────────────────┐              ┌─────────────────┐              ┌─────────────────┐
          │ weather-tool-v2 │              │  geocoding-tool │              │  currency-tool  │
          │   (Port 8091)   │              │   (Port 8094)   │              │   (Port 8090)   │
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

| Data Type | Redis Database | Key Pattern |
|-----------|----------------|-------------|
| Service Registry | DB 0 | `truvag3:services:*` |
| Chat Sessions | DB 2 | `truvag3:sessions:*` |
| LLM Debug Records | DB 7 | `llm_debug:*` |

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
| `session` | New session created | `{"id": "uuid"}` |
| `status` | Progress update | `{"step": "planning", "message": "..."}` |
| `step` | Tool execution complete | `{"tool": "weather-tool", "success": true, "duration_ms": 234}` |
| `chunk` | Response text chunk | `{"text": "The weather..."}` |
| `usage` | Token usage stats | `{"prompt_tokens": 100, "completion_tokens": 50}` |
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
  "expires_at": "2024-01-01T00:30:00Z"
}
```

### `GET /chat/session/{id}/history`

Get conversation history for a session.

### `GET /health`

Health check endpoint.

**Response:**
```json
{
  "status": "healthy",
  "redis": "healthy",
  "ai_provider": "connected",
  "orchestrator": {
    "status": "active",
    "total_requests": 10,
    "successful_requests": 9
  },
  "active_sessions": 3
}
```

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
| `PORT` | HTTP server port | `8095` | No |
| `APP_ENV` | Environment (development/staging/production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP endpoint for telemetry | - | No |
| `NAMESPACE` | Kubernetes namespace | `truvag3-examples` | No |
| `TRUVAG3_LLM_DEBUG_ENABLED` | Enable LLM debug payload capture | `false` | No |
| `TRUVAG3_LLM_DEBUG_TTL` | TTL for successful debug records | `24h` | No |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | TTL for error debug records | `168h` | No |

*At least one AI provider key is required.

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

At minimum, uncomment and set one AI provider API key.

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

## Session Management

Sessions are stored in Redis (DB 2) with the following characteristics:

| Property | Value |
|----------|-------|
| **TTL** | 30 minutes of inactivity |
| **Max Messages** | 50 per session (sliding window) |
| **Storage** | Redis DB 2 (`truvag3:sessions:*`) |
| **Multi-pod Support** | Yes (shared Redis) |

### Session Flow

1. Client sends first message without `session_id`
2. Server creates session, stores in Redis, returns `session_id` via SSE
3. Client includes `session_id` in subsequent requests
4. Server retrieves history from Redis for conversation context
5. Session expires after 30 minutes of inactivity

---

## Telemetry

The agent includes comprehensive observability:

### Tracing (Jaeger)

- All requests traced with span events
- Tool execution timing
- Error tracking and debugging
- Access at http://localhost:16686

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

Access Grafana at http://localhost:3000 (admin/admin)

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
├── chat_agent.go        # Agent with orchestration integration
├── sse_handler.go       # SSE streaming handler
├── session.go           # Redis-backed session management
├── handlers.go          # HTTP handlers (health, session, discover)
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

The orchestrator needs time to discover tools. Wait a few seconds and retry. Check if tools are deployed:
```bash
kubectl get pods -n truvag3-examples
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

```bash
# View agent logs
./setup.sh logs

# Check pod status
kubectl get pods -n truvag3-examples -l app=travel-chat-agent

# Check ingress routes
kubectl get ingress -n truvag3-examples

# Check Redis session data
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 2 KEYS 'truvag3:sessions:*'

# Test the API
./setup.sh test

# Verify all ingress routes
./setup.sh verify

# Full cleanup
./setup.sh cleanup-all
```

---

## Related Examples

- [chat-ui](../chat-ui/) - Web frontend for this agent
- [agent-with-orchestration](../agent-with-orchestration/) - Basic orchestration example
- [agent-with-telemetry](../agent-with-telemetry/) - Full observability example
- [weather-tool-v2](../weather-tool-v2/) - Weather data tool
- [geocoding-tool](../geocoding-tool/) - Location geocoding tool
- [currency-tool](../currency-tool/) - Currency exchange tool
- [country-info-tool](../country-info-tool/) - Country information tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
