# `<AGENT NAME>` — a TruvaG3 Async Agent

> **Template scaffold.** This README is a placeholder. After your coding
> agent populates the implementation from [`PROMPT.md`](PROMPT.md), replace
> this file. Use whichever reference matches your async shape:
>
> - Pull (HTTP 202 + polling): [`examples/agent-with-async/README.md`](../agent-with-async/README.md)
> - Push (webhook → queue → worker): [`examples/event-driven-agent/README.md`](../event-driven-agent/README.md)

---

## What this agent does

`<one-paragraph description — what events does it consume (webhooks, queue
messages, scheduled triggers), and what investigation/remediation does it
perform per event?>`

## Event source

| Field | Value |
|-------|-------|
| Source | `<e.g., AlertManager webhook, GitHub events, internal queue>` |
| Inbound endpoint | `<e.g., POST /webhook>` |
| Payload schema | `<reference to plan.md or upstream docs>` |
| Idempotency key | `<e.g., alertname + fingerprint, or event UUID>` |

## Tools this agent orchestrates per event

| Tool | When called | What we get |
|------|-------------|-------------|
| `<tool_1>` | `<condition>` | `<info or action>` |
| `<tool_2>` | `<condition>` | `<info or action>` |

## Human-in-the-loop (HITL)

`<Describe which actions require approval, who approves, and the timeout.
Or "Not used — fully autonomous."`

## How to run

```bash
# 1. Configure an AI provider key (REQUIRED)
cp .env.example .env
# Edit .env, uncomment + fill ONE of OPENAI_API_KEY / ANTHROPIC_API_KEY / GROQ_API_KEY / GEMINI_API_KEY

# 2. Cold-start full deployment (cluster + infra + agent)
./setup.sh full-deploy
# Or, if cluster + infra are already up:
# ./setup.sh deploy

# 3. Trigger a test event (adapt to your event source)
curl -X POST http://<agent-name>.localhost/webhook \
  -H "Content-Type: application/json" \
  -d '<sample event payload from plan.md>'
```

## Endpoints

Depending on the async shape chosen:

**Pull (HTTP 202 + polling):**

| Endpoint | Purpose |
|----------|---------|
| `POST /api/v1/tasks` | Submit a task; returns 202 + `{task_id, status, status_url}` |
| `GET /api/v1/tasks/{id}` | Poll for task status / result; terminal statuses: `completed`, `failed`, `cancelled` |
| `GET /health` | Liveness probe |
| `GET /api/capabilities` | Capability discovery |

**Push (webhook → queue → worker):**

| Endpoint | Purpose |
|----------|---------|
| `POST /webhook` | Inbound event ingestion |
| `GET /health` | Liveness probe |
| `GET /api/capabilities` | Capability discovery |

## Required reading for contributors

- [`docs/building/AGENT_DEVELOPMENT_GUIDE.md`](../../docs/building/AGENT_DEVELOPMENT_GUIDE.md) (esp. §10 "Background Jobs: `core.Runnable`")
- [`docs/orchestration/ASYNC_ORCHESTRATION_GUIDE.md`](../../docs/orchestration/ASYNC_ORCHESTRATION_GUIDE.md)
- [`docs/observability/DISTRIBUTED_TRACING_GUIDE.md`](../../docs/observability/DISTRIBUTED_TRACING_GUIDE.md)
- [`docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md`](../../docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md)
