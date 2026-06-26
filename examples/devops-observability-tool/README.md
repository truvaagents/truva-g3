# DevOps Observability Tool

A TruvaG3 tool that wraps **Loki** (logs) and **Jaeger** (distributed traces) HTTP APIs as capabilities for DevOps troubleshooting. Agents can query logs, search traces, and correlate requests across services — all as DAG steps in their execution plans.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start](#quick-start)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Registered Capabilities](#registered-capabilities)
  - [Loki (Logs)](#loki-logs)
  - [Jaeger (Traces)](#jaeger-traces)
- [Logs-Traces Correlation](#logs-traces-correlation)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

This tool queries Loki and Jaeger which are deployed as part of the shared infrastructure. No API keys required.

### Prerequisites

- Docker, Kind, kubectl, Go (see [examples/README.md](../README.md) for installation guides)
- A running Kind cluster with infrastructure (Loki, Jaeger, Redis)

### Quick Start

```bash
cd examples/devops-observability-tool

# Deploy (requires cluster with infrastructure running)
./setup.sh deploy
```

If you don't have a cluster yet:

```bash
./setup.sh full-deploy
```

### Step-by-Step Deployment

```bash
# 1. Ensure infrastructure is running (skip if already up from another example —
#    the cluster + infra are shared across all TruvaG3 examples).
cd examples/devops-observability-tool
./setup.sh cluster
./setup.sh infra

# 2. Deploy the tool (builds image, loads into Kind, creates namespace, applies manifest)
./setup.sh deploy

# 3. Verify status
./setup.sh status

# 4. Run the built-in smoke test
./setup.sh test
```

---

## Features

- **Log Queries (Loki)** — Query logs by service, keyword, request_id, or time range using LogQL
- **Distributed Trace Search (Jaeger)** — Find traces by service, operation, duration; get full span trees
- **Cross-Service Request Tracing** — Search logs for a request_id, extract trace_id, then fetch the full distributed trace
- **Field Discovery** — Detect available log fields and their cardinality before constructing queries
- **Label Discovery** — List available Loki labels and their values
- **9 Capabilities** — 5 for logs (Loki) + 4 for traces (Jaeger)
- **CRI Prefix Stripping** — Log lines are cleaned of CRI format prefixes before returning
- **Automatic Service Discovery** — Registers with Redis for agent discovery

---

## Registered Capabilities

### Loki (Logs)

#### 1. Query Logs (`query_logs`)

**Endpoint:** `/api/capabilities/query_logs`

Queries recent log lines using LogQL with relative time window.

**Request:**
```json
{
  "query": "{service_name=\"devops-chat-agent\"} |= \"ERROR\"",
  "limit": 50,
  "since": "2h"
}
```

**Cross-service request trace:**
```json
{
  "query": "{service_name=~\".+\"} |= \"orch-1774585774005748968\"",
  "limit": 100,
  "since": "6h"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "streams": [
      {
        "labels": "{\"service_name\":\"devops-chat-agent\",\"trace_id\":\"abc123\"}",
        "entries": [
          {
            "timestamp": "2026-03-26T03:14:20.123Z",
            "line": "{\"level\":\"ERROR\",\"message\":\"connection refused\"}"
          }
        ]
      }
    ],
    "total_entries": 1,
    "query": "{service_name=\"devops-chat-agent\"} |= \"ERROR\"",
    "source": "loki"
  }
}
```

#### 2. Query Logs Range (`query_logs_range`)

**Endpoint:** `/api/capabilities/query_logs_range`

Queries logs within an explicit time window (for incident investigation).

**Request:**
```json
{
  "query": "{service_name=\"product-catalog-api\"}",
  "start": "2026-03-26T14:00:00Z",
  "end": "2026-03-26T14:30:00Z",
  "limit": 100
}
```

#### 3. Get Labels (`get_labels`)

**Endpoint:** `/api/capabilities/get_labels`

Lists available Loki stream labels (e.g., `service_name`, `deployment_environment`).

#### 4. Get Label Values (`get_label_values`)

**Endpoint:** `/api/capabilities/get_label_values`

Gets all values for a label (e.g., all service names with logs).

**Request:**
```json
{
  "label": "service_name"
}
```

#### 5. Get Detected Fields (`get_detected_fields`)

**Endpoint:** `/api/capabilities/get_detected_fields`

Detects structured fields in JSON log lines (e.g., `level`, `operation`, `error_type`).

**Request:**
```json
{
  "query": "{service_name=\"devops-chat-agent\"}",
  "limit": 10
}
```

### Jaeger (Traces)

#### 6. Get Trace Services (`get_trace_services`)

**Endpoint:** `/api/capabilities/get_trace_services`

Lists all services with distributed trace data in Jaeger.

#### 7. Get Trace Operations (`get_trace_operations`)

**Endpoint:** `/api/capabilities/get_trace_operations`

Lists operations (span names) for a service — HTTP endpoints, orchestrator phases, AI calls.

**Request:**
```json
{
  "service": "devops-chat-agent"
}
```

#### 8. Find Traces (`find_traces`)

**Endpoint:** `/api/capabilities/find_traces`

Searches traces by service, operation, and duration filters.

**Request:**
```json
{
  "service": "devops-chat-agent",
  "min_duration": "1s",
  "lookback": "24h",
  "limit": 10
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "traces": [
      {
        "trace_id": "ce0e25d4f3587008f9f98d857ec8288d",
        "span_count": 116,
        "services": ["travel-chat-agent", "flight-tool", "hotel-tool"],
        "duration_ms": 45230,
        "root_operation": "HTTP POST /chat/stream",
        "request_id": "orch-1774582274001422040"
      }
    ],
    "total_traces": 1,
    "source": "jaeger"
  }
}
```

#### 9. Get Trace (`get_trace`)

**Endpoint:** `/api/capabilities/get_trace`

Gets the full distributed trace with all spans, errors, durations, and tags.

**Request:**
```json
{
  "trace_id": "ce0e25d4f3587008f9f98d857ec8288d"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "trace_id": "ce0e25d4f3587008f9f98d857ec8288d",
    "span_count": 116,
    "services": ["travel-chat-agent", "flight-tool", "hotel-tool"],
    "request_id": "orch-1774582274001422040",
    "spans": [
      {
        "span_id": "7ce7c1c3e6b9c4f8",
        "operation": "HTTP POST /chat/stream",
        "service": "travel-chat-agent",
        "duration_ms": 45230,
        "status": "ok",
        "tags": {"http.method": "POST", "http.status_code": "200"}
      }
    ],
    "error_spans": [
      {
        "operation": "HTTP POST /api/capabilities/search_flights",
        "service": "flight-tool",
        "duration_ms": 1200,
        "error": "upstream API timeout"
      }
    ],
    "source": "jaeger"
  }
}
```

---

## Logs-Traces Correlation

Logs and traces are connected via `request_id` and `trace_id`:

1. **Find trace_id from request_id:** Use `query_logs` with `|= "request-id"` — the `trace_id` appears in stream labels
2. **Get full trace:** Pass the `trace_id` to `get_trace` for the complete span tree

**Example workflow:**
```
query_logs: {service_name=~".+"} |= "orch-1774585774005748968"
  → stream labels contain trace_id: "71267aad5557d01f4a8fd06bd3f1be77"

get_trace: trace_id = "71267aad5557d01f4a8fd06bd3f1be77"
  → 115 spans across 6 services, with errors and durations
```

---

## Architecture

```
devops-observability-tool
    |
    +-- Loki Client (HTTP/LogQL)
    |       +-- query_logs, query_logs_range
    |       +-- get_labels, get_label_values
    |       +-- get_detected_fields
    |
    +-- Jaeger Client (HTTP v2 API)
    |       +-- get_trace_services, get_trace_operations
    |       +-- find_traces, get_trace
    |
    +-- Registers 9 capabilities via Redis
    +-- Agents discover and invoke as DAG steps
```

### Infrastructure Pipeline

```
Pod logs → OTEL Collector DaemonSet → OTEL Collector → Loki (/otlp)
App traces → OTEL SDK → OTEL Collector → Jaeger Collector (gRPC)
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `LOKI_URL` | Loki query endpoint | `http://loki.truvag3-examples:3100` | No |
| `LOKI_HTTP_TIMEOUT` | Per-request timeout for the outbound Loki HTTP client (Go duration, e.g. `60s`, `2m`). Keep below the orchestrator's 120s per-step deadline so the agent gets a clean error rather than a step timeout. | `90s` | No |
| `JAEGER_URL` | Jaeger query endpoint | `http://jaeger-query.truvag3-examples:80` | No |
| `PORT` | HTTP server port | `8378` | No |

**Note:** Loki labels use OTEL resource attributes. The primary stream label is `service_name` (not `app` or `namespace`). LogQL queries use `{service_name="myapp"}`.

---

## Project Structure

```
devops-observability-tool/
├── main.go              # Entry point, framework setup
├── logs_tool.go         # Tool struct, 9 capability registrations
├── loki_client.go       # Loki HTTP API client (traced)
├── jaeger_client.go     # Jaeger v2 HTTP API client (traced)
├── handlers.go          # HTTP handlers for all capabilities
├── go.mod               # Go module definition
├── .env.example         # Configuration template
├── setup.sh             # Build/deploy/test lifecycle
├── Dockerfile.workspace # Container build (workspace mode)
├── k8-deployment.yaml   # Kubernetes manifests
└── PLAN.md              # Implementation plan
```

---

## Troubleshooting

### Common Issues

**1. query_logs returns empty results**

Loki has a 30-hour query window limit. Use `since: "24h"` or shorter.
```bash
# Verify Loki is running (Loki is part of the shared infra stack)
kubectl get pods -n truvag3-examples -l app=loki
```

**2. get_trace returns "trace not found"**

Jaeger uses in-memory storage with 50,000 trace limit. Older traces may have been evicted.

**3. Jaeger capabilities not selected by agents**

Check that the tool is registered with all 9 capabilities:
```bash
# Via setup.sh test (handles port-forward automatically)
./setup.sh test

# Or manually: in one terminal run `./setup.sh forward`, then in another:
curl -s http://localhost:8378/api/capabilities | jq '.[].name'
```

**4. Labels show only service_name and deployment_environment**

This is expected. The OTEL pipeline maps pod logs to these two indexed stream labels. Other fields (level, operation, component) are in the log line body — use `get_detected_fields` to discover them, then filter with `| json | level="ERROR"` in LogQL.

### Useful Commands

```bash
./setup.sh status       # Check deployment status
./setup.sh logs         # View tool logs
./setup.sh test         # Run API tests
./setup.sh forward-all  # Port forward with monitoring
```

---

## Related Examples

- [devops-chat-agent](../devops-chat-agent/) — Agent that uses this tool for log/trace investigation
- [agentic-memory-tool](../agentic-memory-tool/) — Shared memory query tool
- [prometheus-query-tool](../prometheus-query-tool/) — Prometheus metrics query tool
- [devops-tool](../devops-tool/) — Kubernetes cluster management tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
