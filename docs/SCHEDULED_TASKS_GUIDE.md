# Scheduled Tasks Guide

If you've ever wanted an agent to "do this in 10 minutes" or "check that service every hour," you're in the right place. This guide covers Truva-G3's scheduled-execution system -- how to schedule tasks, how they fire, how to observe them, and how to extend the system to non-Redis backends.

The scheduling system is three cooperating components that together give any agent delayed and recurring task execution with zero per-agent plumbing.

> **Working Example**
>
> Everything in this guide comes from fully working, production-tested implementations:
> - **Scheduler (producer)**: [`examples/scheduler-tool/`](../examples/scheduler-tool/)
> - **Executor (consumer)**: [`examples/scheduled-executor/`](../examples/scheduled-executor/)
> - **Agent wiring**: [`examples/devops-chat-agent/main.go`](../examples/devops-chat-agent/main.go) (search for `RegisterScheduledEndpoint`)
> - **Core interfaces**: [`core/async_task.go`](../core/async_task.go) (search for `TaskConsumer`, `TaskHandle`)
> - **Conformance tests**: [`core/conformance/`](../core/conformance/)

## Table of Contents

- [What is Scheduled Execution?](#what-is-scheduled-execution)
- [Quick Start](#quick-start)
- [Architecture](#architecture)
  - [The Three Components](#the-three-components)
  - [End-to-End Flow](#end-to-end-flow)
- [Scheduling a Task](#scheduling-a-task)
  - [One-Shot Delay](#one-shot-delay)
  - [Absolute Time](#absolute-time)
  - [Recurring (Cron)](#recurring-cron)
- [Receiving Scheduled Tasks](#receiving-scheduled-tasks)
  - [One-Line Wiring](#one-line-wiring)
  - [Custom Behaviour](#custom-behaviour)
  - [The Request Contract](#the-request-contract)
- [Configuration](#configuration)
  - [Scheduler-Tool Variables](#scheduler-tool-variables)
  - [Scheduled-Executor Variables](#scheduled-executor-variables)
- [Delivery Semantics](#delivery-semantics)
  - [At-Most-Once (Default)](#at-most-once-default)
  - [At-Least-Once (Streams)](#at-least-once-streams)
  - [Which One Should I Pick?](#which-one-should-i-pick)
- [Observability](#observability)
  - [Metrics](#metrics)
  - [Tracing](#tracing)
  - [Logging](#logging)
  - [Dead-Letter Queue Inspection](#dead-letter-queue-inspection)
- [Troubleshooting](#troubleshooting)
- [Extending: Writing Your Own Backend](#extending-writing-your-own-backend)
  - [What Interfaces to Satisfy](#what-interfaces-to-satisfy)
  - [The Borrow-Then-Settle Contract](#the-borrow-then-settle-contract)
  - [Testing: The Conformance Helper](#testing-the-conformance-helper)
  - [Postgres Example](#postgres-example)
- [FAQ](#faq)
- [See Also](#see-also)

---

## What is Scheduled Execution?

Scheduled execution lets any Truva-G3 agent defer work to the future. An LLM can say "remind me in 10 minutes" or "check this service every hour," and the framework handles the timing, dispatch, retry, and dead-lettering -- the agent itself gains one HTTP endpoint via one line of code.

Think of it like a cron job that an AI can create on the fly, except:
- The agent's LLM creates schedules naturally (via the `scheduler-tool` capabilities)
- A centralized executor dispatches scheduled tasks to agents as HTTP requests
- The target agent's orchestrator plans and executes the instruction using whatever tools it has access to
- Retry, dead-lettering, and observability are handled in one place

---

## Quick Start

### Prerequisites

- A running Truva-G3 cluster with Redis (see [Getting Started](../examples/README.md))
- `scheduler-tool` deployed (the producer side)
- `scheduled-executor` deployed (the consumer side)
- At least one agent with an orchestrator (e.g., `devops-chat-agent`)

### 1. Wire the Agent (One Line)

In your agent's `main.go`, **before** `framework.Run()`:

```go
// Register the scheduled endpoint. The orchestratorFn resolves lazily --
// returns nil until the async orchestrator init completes, producing 503.
if err := orchestration.RegisterScheduledEndpoint(agent.BaseAgent, func() orchestration.Orchestrator {
    if o := agent.GetOrchestrator(); o != nil {
        return o
    }
    return nil
}); err != nil {
    agent.Logger.Warn("Failed to register scheduled endpoint", map[string]interface{}{"error": err.Error()})
}
```

That's it. Your agent now accepts scheduled tasks on `/api/v1/scheduled`.

### 2. Schedule a Task

From the chat UI, tell your agent:

> "In 5 minutes, check the health of the api-gateway service"

The agent's LLM plans a `scheduler-tool/schedule_task` step. The scheduler-tool creates a schedule with a 5-minute delay. When it fires, the executor POSTs to your agent's `/api/v1/scheduled` endpoint, the orchestrator plans and executes the health check.

### 3. Watch It Work

```bash
# Scheduler fires
kubectl logs -n truvag3-examples -l app=scheduler-tool -f --tail=20

# Executor dispatches
kubectl logs -n truvag3-examples -l app=scheduled-executor -f --tail=20

# Agent processes
kubectl logs -n truvag3-examples -l app=devops-chat-agent -f --tail=20
```

---

## Architecture

### The Three Components

```
 +----------------------+         +----------------------+         +--------------------+
 |   scheduler-tool     |         |  scheduled-executor  |         |   Target Agent     |
 |   (existing)         |         |  (NEW)               |         |   (1 line wiring)  |
 |                      |         |                      |         |                    |
 |  - 5 capabilities    |         |  - Consume loop      |         |  - HTTP server     |
 |  - Scheduler tick    |         |  - HTTP forwarder    |         |  - Orchestrator    |
 |  - Leader elect      |         |  - Retry / DLQ       |         |  - /api/v1/        |
 |  - Dispatch via      |  Redis  |  - Result logging    |  HTTP   |    scheduled       |
 |  TaskDispatcher ---->| List/   |  via TaskConsumer    |  POST   |  ----------------->|
 |  (interface)         | Stream  |  (interface)         |         |                    |
 +----------------------+         +----------------------+         +--------------------+
```

Three pods total in the cluster:

- **scheduler-tool** (1+ replicas, leader-elected) -- already exists. Exposes 5 capabilities to LLMs: `schedule_task`, `list_schedules`, `get_schedule`, `update_schedule`, `cancel_schedule`.
- **scheduled-executor** (1+ replicas, all-active) -- new. Consumes tasks from a single queue, resolves the target agent, and HTTP POSTs to its `/api/v1/scheduled` endpoint.
- **Each agent** -- already exists. Gains the endpoint via `RegisterScheduledEndpoint`.

### End-to-End Flow

```
1. User asks agent: "In 1 hour, recheck the api-gateway service health."
   |
2. Agent's LLM generates a schedule_task step:
   { "delay": "1h", "input": {"instruction": "recheck api-gateway health"} }
   |
3. Agent's orchestrator calls POST /api/capabilities/schedule_task on scheduler-tool
   |
4. scheduler-tool persists the schedule in Redis (data key + due sorted set)
   |
5. ~1 hour later, the scheduler tick loop fires the schedule:
   - Creates a Task with deterministic ID {schedule_id}:{fire_unix}
   - Stamps task.TargetAgent = the calling agent (from X-TruvaG3-Agent-Name header)
   - Stamps task.TraceID + task.ParentSpanID from the tick span
   - Dispatches to the fixed "scheduled-executor" queue
   |
6. scheduled-executor replica consumes the task via BRPOP
   |
7. Executor resolves target agent via service registry (AgentCatalog)
   |
8. HTTP POST to http://{agent-address}:{port}/api/v1/scheduled
   |
9. Agent's /api/v1/scheduled endpoint hands the instruction to the orchestrator
   |
10. Orchestrator plans and executes (may call tools like slack-tool, etc.)
   |
11. Done. One-shot schedules are deleted; recurring schedules advance.
```

---

## Scheduling a Task

The LLM schedules tasks by calling the `scheduler-tool/schedule_task` capability. You don't need to do anything special -- if `scheduler-tool` is registered in the cluster, the LLM discovers it automatically.

### One-Shot Delay

```json
{
  "delay": "10m",
  "input": {"instruction": "check api-gateway service health"}
}
```

The `target_agent` field is automatically set to the calling agent by the handler -- the LLM doesn't need to specify it.

### Absolute Time

```json
{
  "run_at": "2026-04-10T18:00:00Z",
  "input": {"instruction": "generate daily report"}
}
```

### Recurring (Cron)

```json
{
  "cron_expr": "*/5 * * * *",
  "input": {"instruction": "poll incident queue for new alerts"}
}
```

Standard 5-field cron syntax (minute, hour, day-of-month, month, day-of-week). The schedule persists until cancelled or disabled.

> **Note**: The `target_agent` field is always set server-side to the calling agent via the `X-TruvaG3-Agent-Name` header. The LLM does not need to specify it, and any value it provides is overridden.

---

## Receiving Scheduled Tasks

### One-Line Wiring

Every agent that wants to receive scheduled tasks needs one call in `main.go`, placed **before** `framework.Run()`:

```go
orchestration.RegisterScheduledEndpoint(agent.BaseAgent, func() orchestration.Orchestrator {
    if o := agent.GetOrchestrator(); o != nil {
        return o
    }
    return nil
})
```

The `OrchestratorFunc` pattern supports agents that initialize their orchestrator asynchronously (in a goroutine after Discovery is available). The endpoint returns 503 until the orchestrator is ready.

### Custom Behaviour

For agents that need non-default behaviour, pass functional options:

```go
orchestration.RegisterScheduledEndpoint(agent.BaseAgent, orchestratorFn,
    // Only process tasks for active sessions
    orchestration.WithScheduledFilter(func(req *orchestration.ScheduledRequest) bool {
        return mySessionStore.IsActive(req.Input["session_id"].(string))
    }),
    // Override how the query string is built
    orchestration.WithScheduledQueryBuilder(func(req *orchestration.ScheduledRequest) string {
        return fmt.Sprintf("Execute scheduled task: %s", req.Instruction)
    }),
)
```

Available options:
- `WithScheduledQueryBuilder` -- how the user-query string is extracted from the request
- `WithScheduledMetadataBuilder` -- what metadata is passed to the orchestrator
- `WithScheduledFilter` -- predicate that skips requests (returns 200 with `status: filtered`)
- `WithScheduledEndpointLogger` -- override the logger

Or bypass the helper entirely for full control (Layer 3):

```go
agent.HandleFunc("/api/v1/scheduled", myCustomHandler)
```

### The Request Contract

**Request** (POSTed by scheduled-executor):

```json
{
  "schedule_id": "sch-abc123def456",
  "task_id": "sch-abc123def456:1743870600",
  "instruction": "recheck api-gateway service health",
  "input": {
    "instruction": "recheck api-gateway service health",
    "service": "api-gateway"
  }
}
```

**Response** (returned by the agent):

```json
{
  "success": true,
  "data": {
    "response": "Service api-gateway is now healthy. Latency p99: 120ms.",
    "request_id": "exec-a1b2c3d4e5f6"
  }
}
```

On agent-side failure:

```json
{
  "success": false,
  "error": {
    "code": "ORCHESTRATOR_ERROR",
    "message": "LLM call failed: rate limit exceeded",
    "category": "service_error"
  }
}
```

---

## Configuration

### Scheduler-Tool Variables

| Variable | Type | Default | Purpose |
|---|---|---|---|
| `REDIS_URL` | string | (required) | Redis connection string |
| `TRUVAG3_SCHEDULER_TICK_INTERVAL` | duration | `5s` | How often the scheduler polls for due schedules |
| `TRUVAG3_SCHEDULER_LOCK_TTL` | duration | `30s` | Distributed lock TTL (must be > tick interval) |

### Scheduled-Executor Variables

| Variable | Type | Default | Purpose |
|---|---|---|---|
| `REDIS_URL` | string | (required) | Redis connection + Discovery backend |
| `TRUVAG3_EXECUTOR_WORKER_COUNT` | int | `5` | Concurrent dispatch goroutines |
| `TRUVAG3_EXECUTOR_MAX_RETRIES` | int | `3` | Max retry attempts per task |
| `TRUVAG3_EXECUTOR_RETRY_BASE_DELAY` | duration | `5s` | Base for exponential backoff |
| `TRUVAG3_EXECUTOR_RETRY_MAX_DELAY` | duration | `60s` | Backoff cap |
| `TRUVAG3_EXECUTOR_DISPATCH_TIMEOUT` | duration | `15m` | Per-request timeout for HTTP POST to agent. Must be ≥ the target agent's `TRUVAG3_ORCHESTRATION_TIMEOUT` |

---

## Delivery Semantics

### At-Most-Once (Default)

The default backend uses Redis `BRPOP` -- the task is atomically removed from the queue when consumed. If the executor crashes mid-dispatch, the task is lost.

**When this is fine**: monitoring checks, periodic refreshes, notifications -- workloads where losing a few tasks per pod crash is acceptable.

**Wiring** (default, no change needed):

```go
backends, err := orchestration.NewRedisSchedulerBackends(redisClient)
```

### At-Least-Once (Streams)

The alternative backend uses Redis Streams (`XREADGROUP` + `XACK`). The task stays in the pending-entries list until explicitly acknowledged. A companion reaper Runnable reclaims stuck tasks from crashed replicas.

**When you need this**: billing events, compliance tasks, data pipeline triggers -- workloads where task loss is unacceptable.

**Wiring**:

```go
backends, reaper, err := orchestration.NewRedisStreamsSchedulerBackends(redisClient)
// MUST register the reaper -- without it, crashed replicas leak tasks
framework.RegisterRunnable(reaper)
```

### Which One Should I Pick?

```
Is your workload OK with ~5 lost tasks per pod crash?
  |
  +-- Yes --> BRPOP (default). Simpler, no reaper, no consumer groups.
  |
  +-- No  --> Streams. Add the reaper Runnable, get at-least-once delivery.
```

---

## Observability

### Metrics

All metrics carry `"module", "scheduled-executor"`. Query with `{module="scheduled-executor"}` in Prometheus.

**Executor-side** (emitted by the scheduled-executor):

| Metric | Type | Labels |
|---|---|---|
| `truvag3.scheduled_executor.tasks_received` | Counter | `target_agent` |
| `truvag3.scheduled_executor.tasks_dispatched` | Counter | `target_agent`, `status` (success/error/dead_letter) |
| `truvag3.scheduled_executor.dispatch_duration_ms` | Histogram | `target_agent`, `status` |
| `truvag3.scheduled_executor.retry_attempts` | Counter | `target_agent`, `attempt` |
| `truvag3.scheduled_executor.consume_errors` | Counter | `error_type` |
| `truvag3.scheduled_executor.dlq_writes_total` | Counter | `status` (success/failure) -- **alert on any failure** |
| `truvag3.scheduled_executor.ack_errors_total` | Counter | |
| `truvag3.scheduled_executor.catalog_refresh_total` | Counter | `status`, `trigger` (periodic/cache_miss) |
| `truvag3.scheduled_executor.catalog_agents_known` | Gauge | |

**Agent-side** (emitted by the `/api/v1/scheduled` handler):

| Metric | Type | Labels |
|---|---|---|
| `truvag3.scheduled_executor.tasks_handled_total` | Counter | `status` (success/failure) |
| `truvag3.scheduled_executor.tasks_handled_duration_ms` | Histogram | `status` |

### Tracing

The scheduling system creates linked traces across the three components:

```
Trace A (scheduler.tick)  <--linked--  Trace B (executor.dispatch --> agent.handler)
```

- The scheduler's tick span stamps `task.TraceID` and `task.ParentSpanID`
- The executor creates a **linked span** (not a child) pointing at the scheduler's trace
- The executor's `TracedHTTPClient` propagates `traceparent` to the agent
- The agent's handler continues the executor's trace as a child span

In Jaeger, search for `service=scheduled-executor` to see dispatch spans, then follow the link to the scheduler's firing span.

### Logging

All log lines include structured fields:

- `operation`: one of `executor_start`, `executor_stop`, `executor_consume`, `executor_dispatch`, `scheduled_task_handle`
- `request_id`: correlation key propagated via OTel baggage
- `schedule_id`: links back to the schedule that created this task
- `target_agent`: which agent the task is dispatched to
- `error_type`: classification on error paths (for alerting)

Cross-service Loki query:

```
{namespace="truvag3-examples"} |= "schedule_id=sch-abc123"
```

### Dead-Letter Queue Inspection

Tasks that fail permanently land in the DLQ. Inspect via:

**Redis (BRPOP and Streams):**

```bash
kubectl exec -n truvag3-examples deploy/redis -- \
    redis-cli LRANGE truvag3:tasks:dead:scheduled-executor 0 -1
```

Each entry is JSON with `task`, `reason`, and `failed_at` fields.

---

## Troubleshooting

| Symptom | Likely Cause | Fix |
|---|---|---|
| Schedule created but never fires | Scheduler not acquiring lock (another instance holds it) | Check `kubectl logs -l app=scheduler-tool` for lock errors |
| Task dispatched but agent returns 404 | `/api/v1/scheduled` not registered | Ensure `RegisterScheduledEndpoint` is called **before** `framework.Run()` |
| Task dispatched but agent returns 503 | Orchestrator not initialized yet | Wait for the async init goroutine to complete; check agent logs |
| `target_agent` wrong in DLQ | Old scheduler-tool version (pre-header-default fix) | Rebuild and redeploy `scheduler-tool` |
| DLQ entry with `unknown_target_agent` | Target agent not registered in service registry | Check agent is running and registered via `registry.localhost` |
| DLQ entry with `target_not_agent` | `target_agent` resolved to a tool, not an agent | Rebuild scheduler-tool to pick up the server-side `target_agent` default |
| DLQ entry with `max_retries_exhausted` | Agent's `/api/v1/scheduled` returning 5xx | Check agent logs for orchestrator errors |
| `dlq_writes_total{status=failure}` | Redis transport error during DLQ persistence | **Page on this** -- tasks are permanently lost |
| Executor not consuming tasks | BRPOP not returning | Check Redis connectivity and queue key `truvag3:tasks:queue:scheduled-executor` |
| Duplicate task execution (Streams backend) | Ack failed, task redelivered | Check `ack_errors_total` metric; expected at-least-once behavior |
| `catalog_agents_known` drops to 0 | Registry empty or Redis unreachable | Check Redis connectivity and agent registration |

---

## Extending: Writing Your Own Backend

### What Interfaces to Satisfy

To replace Redis with Postgres, NATS, SQS, or any other transport, implement these interfaces from `core/async_task.go`:

- **`core.TaskConsumer`** -- single method: `Consume(ctx, queueName) (TaskHandle, error)`. Blocks until a task is available or ctx is cancelled.
- **`core.TaskHandle`** -- three methods: `Task()` (accessor), `Ack(ctx)` (success), `Nack(ctx, reason)` (terminal failure with dead-letter persistence).
- **`core.TaskDispatcher`** -- single method: `Dispatch(ctx, queueName, task) error`. Enqueues a task.

### The Borrow-Then-Settle Contract

`TaskConsumer.Consume` returns a `TaskHandle` -- an opaque leased reference the worker must settle via exactly one `Ack` or `Nack` call before discarding it. This is the same pattern as `database/sql.Rows` (cursor + Close), `net.Conn` (Read/Write + Close), and AMQP basic.deliver/basic.ack.

- **At-most-once backends** (BRPOP, `DELETE...RETURNING`): the task leaves the queue on `Consume`. Ack is a no-op. Nack writes the dead-letter entry.
- **At-least-once backends** (Streams, `SELECT FOR UPDATE`): the task stays claimed. Ack removes it from the pending set. Nack writes the DLQ entry AND acknowledges the message.

### Testing: The Conformance Helper

The framework ships a contract test suite at `github.com/truvaagents/truva-g3/core/conformance` that verifies any `TaskConsumer` implementation against the full contract. Your primary test file is 5 lines:

```go
func TestMyBackendConformance(t *testing.T) {
    conformance.RunTaskConsumerConformance(t, func(t *testing.T) (core.TaskConsumer, core.TaskDispatcher, func()) {
        return myConsumer, myDispatcher, func() { cleanup() }
    })
}
```

The suite runs 10 sub-tests covering roundtrip, settlement, idempotency, cancellation, concurrency, field preservation, and double-settlement safety. If all pass, your backend is compliant.

The framework's own reference backends (`RedisTaskConsumer`, `RedisStreamsTaskConsumer`, `InMemoryTaskConsumer`) all use this suite as their primary test. When the framework adds a new behavioral requirement in a future version, your backend's CI picks it up on `go get -u`.

### Postgres Example

A Postgres-backed consumer uses `DELETE ... RETURNING` for at-most-once delivery:

```sql
CREATE TABLE truvag3_scheduled_tasks (
    id          TEXT PRIMARY KEY,
    queue_name  TEXT NOT NULL,
    payload     JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE truvag3_scheduled_dead_letters (
    id            BIGSERIAL PRIMARY KEY,
    task_id       TEXT NOT NULL,
    reason        TEXT NOT NULL,
    payload       JSONB NOT NULL,
    failed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

The consumer calls `DELETE FROM ... WHERE id = (SELECT id ... FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING ...` -- a single statement that atomically pops and returns the next task. The handle's `Nack` does an `INSERT INTO truvag3_scheduled_dead_letters`. No in-flight state, no leaks.

Wire it in `main.go`:

```go
consumer := pgscheduler.NewPostgresTaskConsumer(db)
worker, _ := executor.NewWorker(executor.ExecutorDeps{
    Consumer:   consumer,
    HTTPClient: tracedClient,
    Catalog:    catalog,
})
```

Zero framework changes required. Run the conformance suite to prove correctness.

---

## FAQ

**Do I need to deploy scheduler-tool and scheduled-executor separately?**

Yes. scheduler-tool is the producer (LLM-facing capabilities). scheduled-executor is the consumer (BRPOP + HTTP forwarder). They're separate pods with different scaling profiles -- scheduler-tool is leader-elected (1 active), executor scales horizontally.

**Can I have multiple scheduled-executor replicas?**

Yes. They load-balance naturally via `BRPOP` (Redis distributes across consumers) or `XREADGROUP` (Streams consumer groups). No coordination needed.

**What happens if my agent is down when the task fires?**

The executor retries with exponential backoff (default: 3 attempts, base 5s, max 60s). After max retries, the task is dead-lettered. Check the DLQ.

**Can I schedule tasks across clusters?**

Same cluster only in v1. Cross-cluster routing is a v2 feature.

**How do I cancel a scheduled task?**

The LLM can call `scheduler-tool/cancel_schedule` with the `schedule_id`. Or manually:

```bash
curl -X POST http://scheduler-tool.localhost/api/capabilities/cancel_schedule \
     -H 'Content-Type: application/json' \
     -d '{"schedule_id": "sch-abc123def456"}'
```

**Why does `target_agent` default to the calling agent?**

Because the scheduled task fires back to the agent's orchestrator, which plans and executes the instruction using whatever tools are needed. Setting it to a tool name (like `slack-tool`) would fail -- tools don't have the `/api/v1/scheduled` endpoint or an orchestrator.

---

## See Also

- [Async Orchestration Guide](ASYNC_ORCHESTRATION_GUIDE.md) -- broader async task system
- [Agent Development Guide](AGENT_DEVELOPMENT_GUIDE.md) -- how agents are built
- [Tool Development Guide](TOOL_DEVELOPMENT_GUIDE.md) -- how tools are built (scheduler-tool is the exemplar)
- [Environment Variables Guide](ENVIRONMENT_VARIABLES_GUIDE.md) -- full env var reference
- [API Reference](API_REFERENCE.md) -- `core.TaskConsumer`, `core.TaskHandle`, `core/conformance`
- [Distributed Tracing Guide](DISTRIBUTED_TRACING_GUIDE.md) -- trace propagation across the scheduling flow
- [Logging Implementation Guide](LOGGING_IMPLEMENTATION_GUIDE.md) -- `operation` field values for the executor
