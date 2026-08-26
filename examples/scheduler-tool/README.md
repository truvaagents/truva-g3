# Scheduler Tool

A `BaseTool` that exposes **delayed and recurring task scheduling** to any TruvaG3 agent via 5 discoverable capabilities. Once deployed, every agent's LLM sees `scheduler-tool/schedule_task` in its service catalog and can include it in a plan -- just like `slack-tool/send_message` or `stock-market-tool/get_stock_quote`.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Registered Capabilities](#registered-capabilities)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Manual Testing via curl](#manual-testing-via-curl)
- [How Target Agents Receive Scheduled Tasks](#how-target-agents-receive-scheduled-tasks)
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
| **Go** | 1.27+ | `brew install go` | `choco install golang` |

> **Note:** The scheduler-tool has no API keys -- it only needs Redis for schedule persistence, task dispatch, and service discovery.

> **Important:** The scheduler-tool works with the [scheduled-executor](../scheduled-executor/) which dispatches fired tasks to target agents. Deploy both for end-to-end scheduling.

### Quick Start (Recommended)

This assumes a Kind cluster and shared infrastructure (Redis, Jaeger, Prometheus, Grafana, OTEL Collector) are already running.

```bash
cd examples/scheduler-tool

# 1. Create .env from the example file (safe -- won't overwrite existing)
[ ! -f .env ] && cp .env.example .env

# 2. Build and deploy
./setup.sh deploy

# 3. Also deploy the scheduled-executor (consumer side)
cd ../scheduled-executor && ./setup.sh deploy
```

### Step-by-Step Deployment

```bash
# 1. Build the Docker image
./setup.sh docker-build

# 2. Deploy to Kind cluster
./setup.sh deploy

# 3. Check status
./setup.sh status

# 4. Run smoke tests
./setup.sh test

# 5. View logs
./setup.sh logs
```

To run multiple replicas for HA (the distributed lock handles leader election automatically):

```bash
kubectl scale deploy scheduler-tool --replicas=3 -n truvag3-examples
```

---

## Registered Capabilities

| Capability | Purpose |
|---|---|
| `schedule_task` | Create a one-shot (delay / run_at) or recurring (cron_expr) schedule |
| `list_schedules` | List all active schedules, optionally filtered by target agent |
| `get_schedule` | Fetch one schedule by ID |
| `update_schedule` | Partial-merge update of timing, payload, target, or enabled state |
| `cancel_schedule` | Delete a schedule |

---

## Architecture

scheduler-tool is a **thin composition layer**. All the real logic lives in framework modules:

```
scheduler-tool/main.go  ──┬──>  orchestration (SchedulerBackends, Scheduler Runnable, capability handlers)
                           |
                           ├──>  orchestration (RedisScheduleStore + RedisTaskDispatcher)
                           |
                           └──>  memory        (RedisDistributedLock for leader election)
                                    └──>  core (interfaces + types)
```

The Scheduler Runnable:
- Acquires a distributed lock (so only one replica runs the tick loop)
- Polls the ScheduleStore for due schedules every 5s (configurable)
- Creates tasks with deterministic IDs and dispatches them to the fixed `scheduled-executor` queue
- `target_agent` is always set to the calling agent via the `X-TruvaG3-Agent-Name` header
- Advances recurring schedules to their next fire time or deletes one-shot schedules after firing

---

## Configuration

### Required Variables

| Variable | Purpose |
|---|---|
| `REDIS_URL` | Redis connection string (`redis://host:port`) |
| `TRUVAG3_K8S_SERVICE_NAME` | This tool's service identity -- used for discovery |

### Optional Variables

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8379` | HTTP server port |
| `TRUVAG3_SCHEDULER_TICK_INTERVAL` | `5s` | How often the Scheduler polls for due schedules |
| `TRUVAG3_SCHEDULER_LOCK_TTL` | `30s` | Distributed lock TTL (must be > tick interval) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | -- | Telemetry collector endpoint |
| `APP_ENV` | `development` | `development` / `staging` / `production` -- selects telemetry profile |
| `NAMESPACE` | -- | Framework-level namespace for discovery |
| `DEV_MODE` | `false` | Relaxes some framework validation for local development |

---

## Manual Testing via curl

Once deployed, call the capabilities directly:

```bash
# Create a schedule that fires in 30 seconds
curl -X POST http://scheduler-tool.localhost/api/capabilities/schedule_task \
     -H 'Content-Type: application/json' \
     -d '{
       "delay": "30s",
       "input": {
         "instruction": "Test scheduled task -- verify the handler runs"
       }
     }'

# List all schedules
curl -X POST http://scheduler-tool.localhost/api/capabilities/list_schedules \
     -H 'Content-Type: application/json' \
     -d '{}'

# Cancel a schedule
curl -X POST http://scheduler-tool.localhost/api/capabilities/cancel_schedule \
     -H 'Content-Type: application/json' \
     -d '{"schedule_id": "sch-abc123def456"}'
```

> **Note:** `target_agent` is not needed in curl examples -- the handler defaults it from the `X-TruvaG3-Agent-Name` header. When calling via curl (no header), the field must be set explicitly.

---

## How Target Agents Receive Scheduled Tasks

Agents need one line in `main.go`, placed **before** `framework.Run()`:

```go
orchestration.RegisterScheduledEndpoint(agent.BaseAgent, orchestratorFn)
```

This registers `/api/v1/scheduled` on the agent. When a scheduled task fires, the `scheduled-executor` POSTs to this endpoint with the instruction, and the agent's orchestrator plans and executes it.

For customised behaviour (custom query builders, metadata enrichment, filtering), see the Layer 2 options in [orchestration/scheduled_endpoint.go](../../orchestration/scheduled_endpoint.go).

For the full architecture and delivery semantics, see the [Scheduled Tasks Guide](../../docs/orchestration/SCHEDULED_TASKS_GUIDE.md).

---

## Project Structure

```
examples/scheduler-tool/
  main.go                  Wiring: Redis, backends, lock, scheduler, framework
  go.mod / go.sum          Module definition
  Dockerfile               Standalone image build
  Dockerfile.workspace     Workspace-local image build
  setup.sh                 Build, deploy, forward, test, cleanup helpers
  k8-deployment.yaml       Kubernetes Service + Deployment
  .env.example             Local development configuration
```

---

## Troubleshooting

| Symptom | Likely Cause | Fix |
|---|---|---|
| Scheduler not firing schedules | Lock not acquired (another instance holds it) | Check logs for lock errors; only one replica runs the tick loop |
| `schedule_task` returns 400 | Missing `delay`/`run_at`/`cron_expr` or invalid format | Check the capability schema via `/api/capabilities/schedule_task/schema` |
| `schedule_task` returns 400 with `MISSING_TARGET_AGENT` | `X-TruvaG3-Agent-Name` header not set and no `target_agent` in body | When calling via curl, set `target_agent` explicitly |
| Schedule created but task never dispatched | Scheduler tick not reaching the schedule | Check `TRUVAG3_SCHEDULER_TICK_INTERVAL` and Redis connectivity |
| Task dispatched but agent returns 404 | Agent's `/api/v1/scheduled` not registered | Ensure `RegisterScheduledEndpoint` is called before `framework.Run()` |

For detailed troubleshooting, see the [Scheduled Tasks Guide](../../docs/orchestration/SCHEDULED_TASKS_GUIDE.md).
