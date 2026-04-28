# Scheduled Executor

A centralized `BaseAgent` that consumes scheduled tasks from the shared scheduling queue and forwards them to target agents over HTTP. This is the consumer-side counterpart to `scheduler-tool` -- the scheduler creates schedules and promotes due tasks, the executor drains the queue, resolves target agents, and dispatches via HTTP POST.

This is an internal coordinator, not an LLM-facing agent. It does not expose domain capabilities and is not intended to be called directly by users.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Architecture](#architecture)
- [Configuration](#configuration)
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

> **Note:** The scheduled-executor has no API keys -- it only needs Redis for task consumption and service discovery. No AI provider required.

> **Important:** The scheduled-executor requires [scheduler-tool](../scheduler-tool/) to be deployed (the producer side). Target agents must have `RegisterScheduledEndpoint` wired to receive tasks.

### Quick Start (Recommended)

This assumes a Kind cluster and shared infrastructure (Redis, Jaeger, Prometheus, Grafana, OTEL Collector) are already running. If not, use any agent's `full-deploy` command first (e.g., `cd ../devops-chat-agent && ./setup.sh full-deploy`).

```bash
cd examples/scheduled-executor

# 1. Create .env from the example file (safe -- won't overwrite existing)
[ ! -f .env ] && cp .env.example .env

# 2. Build and deploy
./setup.sh deploy
```

Once complete:

| Service | URL | Description |
|---------|-----|-------------|
| **Health endpoint** | `http://localhost:8380/health` (via port-forward) | Executor health check |
| **Registry Viewer** | http://registry.localhost | Verify executor is registered |

### Step-by-Step Deployment

```bash
# 1. Build the Docker image
./setup.sh docker-build

# 2. Deploy to Kind cluster
./setup.sh deploy

# 3. Check status
./setup.sh status

# 4. View logs
./setup.sh logs

# 5. Run smoke tests
./setup.sh test
```

---

## Architecture

```
scheduler-tool                scheduled-executor              Target Agent
(producer)                    (consumer)                      (receiver)
                              
Creates schedules   ------>   Consumes tasks via BRPOP  --->  /api/v1/scheduled
in Redis                      Resolves agent via registry      Orchestrator processes
                              HTTP POST with retry + DLQ       the instruction
```

The executor:

1. Runs a background worker pool that consumes `core.TaskHandle`s from the fixed `scheduled-executor` queue
2. Resolves `task.TargetAgent` via the service registry
3. Forwards the task payload to the target agent via traced HTTP POST
4. Retries transient failures with exponential backoff
5. Dead-letters terminal failures via `TaskHandle.Nack`
6. Periodically refreshes its cached agent catalog (10s cadence)

---

## Configuration

### Required Variables

| Variable | Purpose |
|---|---|
| `REDIS_URL` | Redis connection string for task consumption and discovery |

### Optional Variables

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8380` | HTTP server port |
| `NAMESPACE` | `truvag3-examples` | Discovery namespace |
| `TRUVAG3_EXECUTOR_WORKER_COUNT` | `5` | Concurrent dispatch goroutines |
| `TRUVAG3_EXECUTOR_MAX_RETRIES` | `3` | Max retry attempts per task |
| `TRUVAG3_EXECUTOR_RETRY_BASE_DELAY` | `5s` | Base for exponential backoff |
| `TRUVAG3_EXECUTOR_RETRY_MAX_DELAY` | `60s` | Backoff cap |
| `TRUVAG3_EXECUTOR_DISPATCH_TIMEOUT` | `15m` | Per-request timeout for HTTP POST to agent. Must be ≥ the target agent's `TRUVAG3_ORCHESTRATION_TIMEOUT` |
| `DEV_MODE` | `false` | Relaxes some framework validation locally |
| `APP_ENV` | `development` | Telemetry profile (`development` / `staging` / `production`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | -- | OTEL collector endpoint |

---

## Project Structure

```
examples/scheduled-executor/
  main.go                  Wiring: Redis, backends, catalog, worker, framework
  worker.go                Vendor-neutral dispatch logic (no Redis imports)
  worker_test.go           Unit tests for all dispatch branches
  refresher_test.go        Catalog refresh Runnable tests
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
| Executor not consuming tasks | Redis unreachable or queue key wrong | Check `REDIS_URL` and `truvag3:tasks:queue:scheduled-executor` |
| DLQ entry with `unknown_target_agent` | Target agent not registered | Check target agent is running via registry viewer |
| DLQ entry with `max_retries_exhausted` | Target agent returning 5xx | Check target agent logs |
| `catalog_agents_known` gauge = 0 | Registry empty | Check Redis and agent registrations |
| Tasks dispatched but agent returns 404 | `/api/v1/scheduled` not registered | Ensure `RegisterScheduledEndpoint` is called before `framework.Run()` |

For detailed troubleshooting, see the [Scheduled Tasks Guide](../../docs/SCHEDULED_TASKS_GUIDE.md).

---

## Related Components

- [`examples/scheduler-tool/`](../scheduler-tool/) -- producer side of scheduled execution
- [Scheduled Tasks Guide](../../docs/SCHEDULED_TASKS_GUIDE.md) -- full architecture, delivery semantics, and troubleshooting
