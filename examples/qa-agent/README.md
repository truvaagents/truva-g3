# QA Agent

A backend autonomous agent that explores websites, generates Playwright test scripts using AI, executes them in a real Chromium browser, files JIRA tickets with results, and sends Slack notifications. Unlike chat agents, the qa-agent is designed for event-driven triggers (deployment webhooks, CI/CD pipelines, scheduled runs) — not interactive user sessions.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Required Tools](#required-tools)
- [Agent Skills](#agent-skills)
- [Architecture](#architecture)
  - [Workflow](#workflow)
  - [Data Flow](#data-flow)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [Telemetry](#telemetry)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

### Prerequisites

| Requirement | Version | macOS | Windows |
|-------------|---------|-------|---------|
| **Docker Desktop** | Latest | [Download](https://www.docker.com/products/docker-desktop/) | [Download](https://www.docker.com/products/docker-desktop/) |
| **Kind** | v0.20+ | `brew install kind` | `choco install kind` |
| **kubectl** | v1.28+ | `brew install kubectl` | `choco install kubernetes-cli` |
| **Go** | 1.25+ | `brew install go` | `choco install golang` |
| **AI Provider API Key** | - | At least one: [OpenAI](https://platform.openai.com/api-keys), [Anthropic](https://console.anthropic.com/), [Groq](https://console.groq.com/keys) | Same as macOS |

> **Note:** The qa-agent is a backend agent — it does not have a chat UI. Interact with it via its REST API or trigger it through event-driven integrations.

> **Important:** The qa-agent requires [tools to be deployed](#required-tools) (playwright-tool, jira-tool, slack-tool) to function. The agent discovers tools automatically via Redis service registry.

### Quick Start (Recommended)

This assumes a Kind cluster and shared infrastructure (Redis, Jaeger, Prometheus, Grafana, OTEL Collector) are already running. If not, use any agent's `full-deploy` command first (e.g., `cd ../travel-chat-agent && ./setup.sh full-deploy`).

```bash
cd examples/qa-agent

# 1. Create .env from the example file (safe - won't overwrite existing)
[ ! -f .env ] && cp .env.example .env
```

**Open `.env`** and configure your API key(s):

```bash
nano .env    # or: code .env / vim .env
```

At minimum, uncomment and set ONE of these in your `.env` file:
- `OPENAI_API_KEY=sk-your-key`
- `ANTHROPIC_API_KEY=sk-ant-your-key`

```bash
# 2. Build and deploy the qa-agent
./setup.sh deploy

# 3. Deploy the required tools (from the examples directory)
cd ../playwright-tool && ./setup.sh deploy
cd ../jira-tool && ./setup.sh deploy
cd ../slack-tool && ./setup.sh deploy

# 4. Port forward the qa-agent
cd ../qa-agent && ./setup.sh forward
```

Once complete:

| Service | URL | Description |
|---------|-----|-------------|
| **QA Agent API** | http://localhost:8358 | Backend REST API |
| **Registry Viewer** | http://localhost:8361 | Execution DAGs, LLM debug, HITL |
| **Jaeger** | http://localhost:16686 | Distributed tracing |
| **Grafana** | http://localhost:3000 | Metrics dashboard (admin/admin) |

### Step-by-Step Deployment

#### Step 1: Ensure Cluster and Infrastructure Are Running

The qa-agent needs a Kind cluster with shared infrastructure. If not already running:

```bash
# Use any agent with a full-deploy command to bootstrap the cluster
cd examples/travel-chat-agent
./setup.sh full-deploy
```

Or deploy infrastructure manually — see [k8-deployment/README.md](../k8-deployment/README.md).

#### Step 2: Deploy the Required Tools

```bash
cd examples/playwright-tool && ./setup.sh deploy
cd ../jira-tool && ./setup.sh deploy
cd ../slack-tool && ./setup.sh deploy
```

#### Step 3: Deploy the QA Agent

```bash
cd examples/qa-agent
cp .env.example .env
# Edit .env and set your AI provider key(s)

./setup.sh deploy
```

#### Step 4: Set Up Port Forwarding

```bash
./setup.sh forward
```

---

## Required Tools

The qa-agent orchestrates 3 tools in a fixed 4-step workflow. **All tools must be deployed for end-to-end operation.**

### Core Tools

| Tool | Purpose | Port | Required |
|------|---------|------|----------|
| **playwright-tool** | Browser automation (explore pages, run tests, upload artifacts to S3) | 8349 | Yes |
| **jira-tool** | File bug/task tickets with test results | 8347 | Yes |
| **slack-tool** | Send notifications to Slack channels | 8348 | Yes |

### How Tools Are Discovered

The qa-agent uses Redis-based service discovery. When a tool starts, it registers its capabilities in Redis (DB 0). The orchestrator's AI planner selects tools based on capability descriptions — no hardcoded tool addresses.

---

## Agent Skills

The agent binds the required `qa/web-application-testing` skill. Its reviewable
source package is stored at
`skills/packages/qa/web-application-testing.json`.

Normal setup is automatic. `deploy`, `rebuild`, and `rollout` validate and
synchronize every package under `skills/packages/<namespace>/<name>.json`
before starting or restarting the agent. This automatic step is best-effort:
failure produces a warning but does not block deployment. An unchanged package
is skipped; a changed package is published as the next version and read back
for verification.

Use these commands only when you want to inspect or repair skill state without
rebuilding the agent:

```bash
./setup.sh skills-check  # Read-only comparison with Git
./setup.sh skills-sync   # Reconcile packages and verify the result
```

These explicit commands remain strict and return a non-zero status on drift or
failure. Set `TRUVAG3_SKILLS_API_URL` for another management host, or set the
setup-only `TRUVAG3_SKIP_SKILLS_SYNC=true` when automatic deployment sync is
intentionally unavailable.

If the local Kind cluster is deleted, the next infrastructure bootstrap and
`./setup.sh deploy` recreate this package from Git. The agent pod does not
publish skills during startup.

---

## Architecture

### Workflow

The qa-agent executes a fixed 4-step sequential pipeline for each QA request. Each step depends on the previous step's output:

```
                         "Test https://cisco.com"
                                    │
                                    ▼
              ┌──────────────────────────────────────────┐
              │              qa-agent                      │
              │         AI Orchestrator                     │
              │    (Plan → Execute → Synthesize)           │
              └─────────────────┬────────────────────────┘
                                │
                                ▼
                   ┌────────────────────────┐
                   │  Step 1: explore_page  │
                   │    (playwright-tool)   │
                   │                        │
                   │  Launch Chromium,      │
                   │  crawl page, extract   │
                   │  selectors, detect SPA │
                   └───────────┬────────────┘
                               │ page structure + selectors
                               ▼
                   ┌────────────────────────┐
                   │   Step 2: run_tests    │
                   │    (playwright-tool)   │
                   │                        │
                   │  LLM generates script  │
                   │  from explore data,    │
                   │  tool executes tests,  │
                   │  uploads artifacts     │
                   │  to S3 (screenshots,   │
                   │  traces, scripts)      │
                   └───────────┬────────────┘
                               │ test results + S3 artifact URLs
                               ▼
                   ┌────────────────────────┐
                   │  Step 3: create_issue  │
                   │     (jira-tool)        │
                   │                        │
                   │  File JIRA ticket with │
                   │  pass/fail summary +   │
                   │  S3 artifact links     │
                   └───────────┬────────────┘
                               │ JIRA ticket key
                               ▼
                   ┌────────────────────────┐
                   │  Step 4: send_message  │
                   │    (slack-tool)        │
                   │                        │
                   │  Notify #qa-tests with │
                   │  summary + JIRA key    │
                   └────────────────────────┘
```

### Step Details

| Step | Tool | Capability | What happens |
|------|------|-----------|--------------|
| **1. Explore** | playwright-tool | `explore_page` | Launches headless Chromium, crawls target URL, extracts selectors, forms, navigation links, detects SPA framework (React, Vue, Angular) |
| **2. Test** | playwright-tool | `run_tests` | The qa-agent's AI orchestrator generates a Playwright test script from explore data and passes it as a parameter. The playwright-tool saves the script, executes it via `npx playwright test`, and uploads artifacts (screenshots, traces, scripts) to S3 |
| **3. Report** | jira-tool | `create_issue` | Creates a JIRA ticket in project TRUV with pass/fail summary, per-test details, failure analysis, and S3 artifact links |
| **4. Notify** | slack-tool | `send_message` | Posts to #qa-tests with pass/fail counts, target URL, and JIRA ticket key |

### Data Flow

```
qa-agent
   │
   ├──► playwright-tool ──► S3 Bucket
   │    (explore + test)     ├── scripts/<run-id>/test.spec.ts
   │                         ├── screenshots/<run-id>/*.png
   │                         └── traces/<run-id>/*.zip
   │
   ├──► jira-tool ──► JIRA (project TRUV)
   │    (create_issue)   ticket with S3 artifact URLs
   │
   └──► slack-tool ──► Slack (#qa-tests)
        (send_message)   summary + JIRA ticket key
```

### Observability

| Layer | What's captured | Where to look |
|-------|----------------|---------------|
| **Distributed traces** | End-to-end request flow across all tools | Jaeger (http://localhost:16686) |
| **Execution DAGs** | Plan steps, dependencies, per-step results and timing | Registry Viewer → Execution DAG tab |
| **LLM debug** | Full prompts/responses for plan generation, synthesis | Registry Viewer → LLM Debug tab |
| **Metrics** | Request counts, duration histograms, tool call counts | Grafana (http://localhost:3000) |
| **Structured logs** | Trace-correlated JSON logs with component attribution | `./setup.sh logs` or log aggregator |

---

## API Reference

### `POST /query`

Submit a QA testing request. The agent runs the full 4-step workflow and returns a synthesized report.

**Request:**
```json
{
  "data": {
    "query": "Explore and test https://cisco.com — check navigation, forms, and accessibility"
  }
}
```

**Response:**
```json
{
  "success": true,
  "request_id": "orch-1773551175051973366",
  "request": "Explore and test https://cisco.com...",
  "response": "## QA Report\n\n**Target:** https://cisco.com\n**Result:** 18/20 passed...",
  "tools_used": ["playwright-tool", "jira-tool", "slack-tool"],
  "execution_time": "2m34s",
  "confidence": 0.92,
  "usage": {
    "prompt_tokens": 4200,
    "completion_tokens": 1800,
    "total_tokens": 6000
  }
}
```

**Example with curl:**
```bash
curl -X POST http://localhost:8358/query \
  -H "Content-Type: application/json" \
  -d '{"data": {"query": "Test https://example.com"}}'
```

> **Note:** This is a synchronous endpoint — the response is returned after the full workflow completes (typically 2–5 minutes). For production use, see [PRODUCTION_OBSERVABILITY_PLAN.md](PRODUCTION_OBSERVABILITY_PLAN.md) for the planned event-driven intake architecture.

### `GET /health`

Health check with orchestrator status.

**Response:**
```json
{
  "status": "healthy",
  "redis": "healthy",
  "ai_provider": "connected",
  "orchestrator": {
    "status": "active",
    "total_requests": 10,
    "successful_requests": 9,
    "average_latency_ms": 145000
  }
}
```

### `GET /discover`

List available tools discovered via Redis service registry.

### `GET /api/capabilities`

Returns the agent's registered capabilities (used by other agents for delegation).

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `OPENAI_API_KEY` | OpenAI API key | - | Yes* |
| `ANTHROPIC_API_KEY` | Anthropic API key | - | Yes* |
| `PORT` | HTTP server port | - | Yes |
| `APP_ENV` | Environment (development/staging/production) | `development` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP endpoint for telemetry | - | No |
| `NAMESPACE` | Kubernetes namespace | `truvag3-examples` | No |
| `TRUVAG3_LLM_DEBUG_ENABLED` | Enable LLM debug payload capture | `false` | No |
| `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED` | Enable execution DAG storage | `false` | No |
| `TRUVAG3_ORCHESTRATION_TIMEOUT` | Overall orchestration timeout | `10m` | No |
| `TRUVAG3_EXECUTION_STEP_TIMEOUT` | Per-step execution timeout | `5m` | No |

*At least one AI provider key is required. PORT is set to `8358` in `.env.example` and `k8-deployment.yaml`.

### .env File

Copy `.env.example` to `.env` and configure your settings:

```bash
cp .env.example .env
```

The `.env.example` file contains comprehensive documentation for all options including:

- **AI Provider Keys** — Supports provider chain for failover (OpenAI → Anthropic → Groq)
- **Model Aliases** — Override default/smart/fast model mappings per provider
- **Orchestration Model Overrides** — Route plan generation to "smart", micro-resolution to "fast"
- **Timeout Configuration** — Extended for browser automation workflows
- **Result Trimming** — Increased budgets for large Playwright explore data
- **Telemetry Configuration** — Environment profiles and OTLP endpoints

---

## Telemetry

### Tracing (Jaeger)

All requests are traced end-to-end across the qa-agent and all downstream tools. Each handler emits span events (`request_received`, `orchestration_started`, completion) with the request_id for searchability.

Access at http://localhost:16686 — search by service `qa-agent` or `playwright-tool`.

### LLM Debug Payload Store

Enable to capture complete LLM prompts and responses (Jaeger truncates large payloads):

```bash
export TRUVAG3_LLM_DEBUG_ENABLED=true
```

Records are stored in Redis DB 7 with configurable TTL. View in Registry Viewer (http://localhost:8361).

### Execution DAG Store

Enable to store orchestration plans and step results for DAG visualization:

```bash
export TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true
```

Records are stored in Redis DB 8. View in Registry Viewer → Execution DAG tab.

### Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `qa.requests` | Counter | Total QA requests by status |
| `qa.request.duration_ms` | Histogram | End-to-end request duration |
| `qa.orchestration.tool_calls` | Counter | Tool calls by tool name |

All metrics flow through the OTEL SDK → OTEL Collector → configured backend (Prometheus in dev, Datadog/New Relic in production).

### Logging

Structured JSON logs with component attribution and trace context:

```json
{
  "component": "agent/qa-agent",
  "level": "INFO",
  "message": "Processing QA query",
  "operation": "process_query",
  "service": "qa-agent",
  "query_len": 45,
  "timestamp": "2026-03-15T10:30:00Z",
  "trace.span_id": "0b319744acd226d5",
  "trace.trace_id": "445c352173a351de293d4d27416b0eb2"
}
```

---

## Project Structure

```
qa-agent/
├── main.go                           # Entry point, telemetry init, framework setup
├── skills.go                         # Skills registry construction
├── qa_agent.go                       # Agent with orchestrator, prompt config, capabilities
├── handlers.go                       # HTTP handlers (query, health, discover)
├── go.mod                            # Go module definition
├── k8-deployment.yaml                # Kubernetes deployment manifest
├── setup.sh                          # Build and deployment script
├── .env.example                      # Environment configuration template
├── Dockerfile.workspace              # Development container with local modules
├── skills/
│   └── packages/qa/                  # Git-authored QA skill packages
├── PRODUCTION_OBSERVABILITY_PLAN.md  # Grafana dashboard + QA UI roadmap
└── README.md                         # This file
```

---

## Troubleshooting

### Common Issues

**1. "REDIS_URL is required" error**

Ensure Redis is running and `REDIS_URL` is set:
```bash
kubectl get pods -n truvag3-examples -l app=redis
```

**2. "Orchestrator initializing" error**

The orchestrator needs time to discover tools. Wait a few seconds and retry. Check the qa-agent itself, then each required tool, from their own directories:
```bash
./setup.sh status
# Then, from each tool's directory:
cd ../playwright-tool && ./setup.sh status
cd ../jira-tool && ./setup.sh status
cd ../slack-tool && ./setup.sh status
```

**3. No tools discovered**

Ensure tools are registered with Redis:
```bash
kubectl exec -n truvag3-examples deploy/redis -- redis-cli -n 0 KEYS 'truvag3:services:*'
```

**4. Requests timing out**

QA workflows involve browser automation and can take 2–5 minutes. Ensure timeouts are configured:
```bash
# In .env:
TRUVAG3_ORCHESTRATION_TIMEOUT=10m
TRUVAG3_EXECUTION_STEP_TIMEOUT=5m
TRUVAG3_HTTP_WRITE_TIMEOUT=12m
```

**5. S3 artifacts not uploading**

The playwright-tool handles S3 uploads. Check its logs from its own directory:
```bash
cd ../playwright-tool && ./setup.sh logs | grep -i s3
```

### Useful Commands

All day-to-day operations go through `setup.sh`. Run `./setup.sh help` to see every subcommand.

```bash
# View agent logs (streams)
./setup.sh logs

# Check pod / service status
./setup.sh status

# Port forward the qa-agent to localhost:8358
./setup.sh forward

# Restart the deployment (e.g., to pick up a new ConfigMap from .env)
./setup.sh rollout

# Inspect or repair agent-owned skill packages without restarting
./setup.sh skills-check
./setup.sh skills-sync

# Rebuild image and restart (use after changing Go code)
./setup.sh rollout --build

# Or full no-cache rebuild + redeploy
./setup.sh rebuild

# Run the built-in smoke test suite against the deployed agent
./setup.sh test

# Remove only the qa-agent (keeps cluster + infra)
./setup.sh clean
```

While `./setup.sh forward` is running, send queries with `curl`:

```bash
# Submit a QA request
curl -X POST http://localhost:8358/query \
  -H "Content-Type: application/json" \
  -d '{"data": {"query": "Test https://example.com"}}'

# Check orchestrator health
curl http://localhost:8358/health | python3 -m json.tool
```

---

## Related Examples

- [playwright-tool](../playwright-tool/) — Browser automation tool (explore + test execution + S3 artifacts)
- [jira-tool](../jira-tool/) — JIRA issue creation tool
- [slack-tool](../slack-tool/) — Slack messaging tool
- [registry-viewer-app](../registry-viewer-app/) — Operator UI for execution DAGs and LLM debug
- [travel-chat-agent](../travel-chat-agent/) — Streaming chat agent (different pattern — interactive, SSE-based)
- [devops-chat-agent](../devops-chat-agent/) — DevOps chat agent with HITL support

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
