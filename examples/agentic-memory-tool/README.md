# Agentic Memory Tool

A TruvaG3 tool that exposes the framework's shared memory interfaces (episodic events, institutional knowledge, investigation coordination) as HTTP capabilities. This is the **pull layer** of the layered memory architecture — agents invoke it as a DAG step when they need to drill into memory details beyond the compact `<agent_memory>` digest.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start](#quick-start)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Registered Capabilities](#registered-capabilities)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

This tool reads from the same Redis and Qdrant backends that agents write to via pipeline hooks. No API keys required.

### Prerequisites

- Docker, Kind, kubectl, Go (see [examples/README.md](../README.md) for installation guides)
- A running Kind cluster with infrastructure (Redis and Qdrant — both deployed by default; opt out of Qdrant with `TRUVAG3_DEPLOY_QDRANT=false`)

### Quick Start

```bash
cd examples/agentic-memory-tool

# Deploy (requires cluster and Redis to be running)
./setup.sh deploy
```

If you don't have a cluster yet:

```bash
./setup.sh full-deploy
```

### Step-by-Step Deployment

```bash
# 1. Ensure infrastructure is running
./setup.sh cluster
./setup.sh infra

# 2. Deploy the tool
./setup.sh deploy

# 3. Test
./setup.sh test
```

---

## Features

- **Episodic Event Queries** — Query what any agent did about any entity, filtered by agent, action type, entity, and time range
- **Semantic Knowledge Search** — Search institutional knowledge fragments using vector similarity (requires Qdrant + Ollama)
- **Investigation Coordination** — Check which agents are currently investigating which entities
- **Interface-Only Design** — Depends on `core.EpisodicMemory`, `core.SharedKnowledge`, `core.InvestigationCoordinator` interfaces; backends can be swapped without changing the tool
- **Graceful Degradation** — If Qdrant or embedding endpoint is unavailable, `query_knowledge` returns empty results instead of failing
- **Automatic Service Discovery** — Registers with Redis for agent discovery

---

## Registered Capabilities

### 1. Query Events (`query_events`)

**Endpoint:** `/api/capabilities/query_events`

Queries episodic memory for recent agent activity.

**Request:**
```json
{
  "entity_id": "product-catalog-api",
  "action_type": "create_issue",
  "since_hours": 24,
  "limit": 10
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "events": [
      {
        "event_id": "abc-123",
        "timestamp": "2026-03-25T18:10:12Z",
        "agent_name": "devops-chat-agent",
        "action_type": "create_issue",
        "entity_type": "service",
        "entity_id": "product-catalog-api",
        "summary": "Created JIRA ticket DEVOPS-49 for rollout restart",
        "outcome": "success",
        "importance": 6.0
      }
    ],
    "total_count": 1,
    "domain": "infrastructure"
  }
}
```

### 2. Query Knowledge (`query_knowledge`)

**Endpoint:** `/api/capabilities/query_knowledge`

Semantic search over institutional knowledge fragments derived from prior agent executions.

**Request:**
```json
{
  "query": "pod restart remediation patterns",
  "namespace": "incidents",
  "limit": 5
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "fragments": [
      {
        "content": "When a pod triggering a high latency alert is no longer found, perform a rolling restart of the parent deployment.",
        "namespace": "runbooks",
        "importance": 8.5,
        "confidence": 0.71
      }
    ],
    "total_count": 1,
    "domain": "infrastructure"
  }
}
```

### 3. Query Investigations (`query_investigations`)

**Endpoint:** `/api/capabilities/query_investigations`

Lists active investigations to prevent duplicate work across agents.

**Request:**
```json
{
  "entity_id": "product-catalog-api-78c468fc8b-q8v2s"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "investigations": [
      {
        "entity_id": "product-catalog-api-78c468fc8b-q8v2s",
        "holder": "event-driven-agent",
        "status": "active"
      }
    ],
    "domain": "infrastructure"
  }
}
```

---

## Architecture

```
Agents write events via pipeline hooks
    ↓
Redis Streams (episodic) + Qdrant (knowledge)
    ↓
agentic-memory-tool reads via core interfaces
    ↓
LLM invokes as DAG step when <agent_memory> digest needs investigation
```

- **Push layer (framework):** Compact `<agent_memory>` digest in every planning prompt
- **Pull layer (this tool):** On-demand queries when the LLM spots something worth investigating

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `TRUVAG3_AGENT_DOMAIN` | Domain scope for memory queries | `infrastructure` | No |
| `TRUVAG3_VECTOR_DB_URL` | Qdrant vector DB URL | `qdrant.truvag3-examples:6334` | No |
| `TRUVAG3_EMBEDDING_BASE_URL` | Embedding API endpoint (OpenAI-compatible) | `http://host.docker.internal:11434/v1` | No |
| `TRUVAG3_EMBEDDING_MODEL` | Embedding model name | `nomic-embed-text` | No |
| `PORT` | HTTP server port | `8377` | No |

---

## Project Structure

```
agentic-memory-tool/
├── main.go              # Entry point, framework setup
├── memory_tool.go       # Tool struct, capability registration
├── memory_backends.go   # Redis + Qdrant backend initialization
├── handlers.go          # HTTP handlers for each capability
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

**1. query_knowledge returns empty results**

This means Qdrant or the embedding endpoint is unavailable (graceful degradation). Check:
```bash
# Verify Qdrant is running
kubectl get pods -n truvag3-examples -l app=qdrant

# Verify Ollama is running on host
curl http://localhost:11434/v1/models
```

**2. Tool not appearing in discovery**

```bash
./setup.sh logs | grep -i "register"
```

**3. No events returned**

Events are written by agent pipeline hooks. Ensure agents (devops-chat-agent, event-driven-agent) have processed requests with shared memory enabled.

### Useful Commands

```bash
./setup.sh deploy       # Build and deploy to an existing cluster
./setup.sh rebuild      # No-cache image rebuild and guaranteed pod replacement
./setup.sh rollout      # Refresh .env-backed config and restart; no image build
./setup.sh status       # Check deployment status
./setup.sh logs         # View tool logs
./setup.sh test         # Run API tests
./setup.sh forward-all  # Port forward with monitoring
```

---

## Related Examples

- [devops-chat-agent](../devops-chat-agent/) — Agent that writes episodic events and reads them via this tool
- [event-driven-agent](../event-driven-agent/) — Event-driven agent with shared memory
- [devops-observability-tool](../devops-observability-tool/) — Logs and traces observability tool
- [prometheus-query-tool](../prometheus-query-tool/) — Prometheus metrics query tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
