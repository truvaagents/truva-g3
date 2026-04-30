# PROMPT — Build a Truva-G3 Async (Event-Driven) Agent (step-by-step)

This guide walks you through building an event-driven async agent — one
that consumes events from webhooks, queues, or scheduled triggers, and
runs autonomous orchestration per event (typically with optional HITL
gating for write actions). Each step is a **self-contained prompt** —
paste it to your coding agent, wait for the work to complete, review it,
then move to the next.

The agent accumulates findings in `plan.md` (created in Step 2) and
source files as it progresses. You stay in the loop between steps so you
can course-correct early.

**Reference examples — pick the one that matches your event source:**

- **Pull (HTTP 202 + polling)**:
  [`examples/agent-with-async/`](../agent-with-async/) — clients submit
  tasks via `POST /api/v1/tasks`, get a `202 Accepted` with a task ID,
  poll `GET /api/v1/tasks/{id}` until completion. This is the **canonical pattern
  documented in `ASYNC_ORCHESTRATION_GUIDE.md`** (every code reference
  in that guide points at this example). Use this if your async work is
  *initiated by callers* (other agents, dashboards, scripts).
- **Push (webhook → queue → worker)**:
  [`examples/event-driven-agent/`](../event-driven-agent/) — AlertManager
  (or any upstream) `POST`s an event to `/webhook`; the agent dedupes,
  enqueues, and a worker pool consumes the queue with the same task-handler
  pattern from the guide. Adds HITL approval for critical write actions
  and split api/worker manifests as an optional shape. Use this if your
  async work is *triggered by external events*.

Both reference examples sit on top of the same underlying task-queue /
worker-pool / tracing / HITL mechanics that `ASYNC_ORCHESTRATION_GUIDE.md`
describes — `event-driven-agent` just adds a webhook receiver in front
of that pipeline.

---

## Step 1 — Define the event source and processing model

Before pasting: replace `<DOMAIN>` with the agent's purpose, name the
event source (webhook, queue, scheduler), and decide whether HITL is
needed.

```
We're going to build a Truva-G3 async (event-driven) agent.
Domain: <DOMAIN>.

Step 1 of 12: define the async shape, event source, and processing model.

- **Async shape — pick one (this drives which reference example to mirror):**
  * **Pull (HTTP 202 + polling)** — callers submit tasks and poll for
    results. Mirror examples/agent-with-async/.
  * **Push (webhook → queue → worker)** — an upstream system pushes
    events; the agent processes them autonomously. Mirror
    examples/event-driven-agent/.
- What's the event source?
  * Pull: who calls `POST /api/v1/tasks`? (other agents, dashboard, CLI)
  * Push: HTTP webhook from <upstream system>? (e.g., AlertManager,
    GitHub, Stripe, custom). Or Redis queue / Kafka topic / NATS
    subject? Or scheduled trigger (cron / scheduled-executor)?
- What does the event payload look like? Get a real sample if possible —
  if it's a published webhook, find the upstream's docs (e.g.,
  AlertManager webhook spec, GitHub Events API, Stripe event types) and
  capture the payload schema.
- What's the per-event processing flow?
  * Deduplication key (so retried events are idempotent)
  * Investigation/diagnostic steps (which tools the agent will call)
  * Decision logic (when to remediate, when to escalate)
  * Write-action policy (which actions require HITL approval, which are
    auto-applied)
- What's the deployment shape?
  * Single deployment (combined webhook receiver + worker) — simplest,
    matches this template's k8-deployment.yaml
  * Split api/worker (separate deployments for ingestion vs processing)
    — see event-driven-agent/k8-deployment-api.yaml and
    k8-deployment-worker.yaml. Choose this if the queue depth or worker
    concurrency profile differs significantly from inbound rate.

Do not write any Go code yet. Summarize what you found and pause for my
review.
```

**What to expect:** clear scope on event source, payload, processing
flow, HITL boundary, and deployment topology.

---

## Step 2 — Capture findings in `plan.md`

```
Step 2 of 12: write everything from Step 1 to a new file `plan.md` in
this folder.

Structure:

# Plan: <agent-name>

## Overview
- One paragraph: what this agent does in response to events.

## Async shape
- [ ] Pull (HTTP 202 + polling) — mirror examples/agent-with-async/
- [ ] Push (webhook → queue → worker) — mirror examples/event-driven-agent/

## Event source
- Source: <pull-from-callers | webhook | queue | scheduled>
- Inbound endpoint or queue: <URL or queue key>
  - Pull: `POST /api/v1/tasks` (returns 202 + task_id) and
    `GET /api/v1/tasks/{id}` for polling
  - Push: `POST /webhook` (or your custom path)
- Auth on inbound: <none | shared secret in header | mTLS | …>
- Sample raw event/task payload (real, not mocked):
  ```json
  { ... }
  ```

## Idempotency
- Dedup key: <e.g., alert.fingerprint, github.delivery_id, event.uuid>
- TTL window: <e.g., 1h>
- Rationale: why this key uniquely identifies the event for our purposes

## Processing flow (per event)
1. <step 1 — typically: extract context, identify entity>
2. <step 2 — investigate using tools T1, T2>
3. <step 3 — decide: remediate / escalate / ignore>
4. <step 4 — execute remediation (with HITL gate if applicable)>
5. <step 5 — emit metrics, write postmortem note>

## Tool dependencies
| Tool | Capability used | When called |
|------|-----------------|-------------|
| ...

## HITL policy
- Auto-apply (no approval): <list>
- Requires approval: <list of operations>
- Approver path: <Slack channel, web UI, email>
- Approval timeout behavior: <reject / retry / escalate>

## Deployment shape
- [x] Combined (single deployment in k8-deployment.yaml)
- [ ] Split api/worker

## Service identity
- Agent name (used in core.NewBaseAgent, k8 image, ConfigMap, OTEL_SERVICE_NAME): <name>
- Required env vars

Pause for my review before writing any Go code.
```

**What to expect:** an event contract that drives the rest of the
implementation. Code, tracing, logs, retries — all flow from this file.

---

## Step 3 — Read the Agent Development Guide and Async Orchestration Guide

```
Step 3 of 12: read these two guides end-to-end:

1. docs/AGENT_DEVELOPMENT_GUIDE.md — focus on:
   - §1 Understanding Agents in Truva-G3
   - §3 Project Structure (focus on the "Non-Streaming Agent" subsection)
   - §4 Step 1: Create the Agent Struct (focus on the "Non-Streaming
     Agent" subsection)
   - §5 Step 2: Configure the Orchestrator (InitializeOrchestrator method
     and PromptConfig)
   - §6 Step 3: Register Capabilities (likely all Internal:true for an
     async agent — the inbound webhook is the trigger, not a capability)
   - §10 Step 7: Create the Main Entry Point — pay close attention to
     the "Background Jobs: `core.Runnable` and `framework.RegisterRunnable`"
     subsection (around line 1547). This is how the worker loop is
     registered with the framework.
   - §12 Logging and Observability
   - §13 Distributed Tracing
   - §15 Adding Human-in-the-Loop (HITL) Approval — only if plan.md
     said HITL is required for write actions

2. docs/ASYNC_ORCHESTRATION_GUIDE.md — the canonical async patterns
   doc. Cover all sections.

Summarize in 8–12 bullets the patterns from these guides that apply to
our agent given plan.md. Do not write code yet.
```

**What to expect:** a concrete summary that names the patterns the agent
will use (Runnable for the worker loop, queue protocol, HITL plumbing,
trace context across the queue boundary).

---

## Step 4 — Study the reference example

```
Step 4 of 12: read the matching reference example end-to-end.

**If plan.md chose Pull (HTTP 202 + polling):**
- Read examples/agent-with-async/ end-to-end. The
  ASYNC_ORCHESTRATION_GUIDE.md you read in Step 3 references this
  example throughout, with explicit line numbers — keep both open
  side-by-side.
- Files: main.go, travel_research_agent.go, handlers.go, setup.sh,
  k8-deployment.yaml, Dockerfile.workspace, .env.example, README.md.
- After reading, list:
  * File layout we'll mirror
  * How the task API is exposed (POST /api/v1/tasks → 202 Accepted with
    task_id + status_url; GET /api/v1/tasks/{id} for polling)
  * How task handlers are registered with the worker pool
  * How progress is reported back to pollers (§7 of the guide)
  * How trace context is propagated from API request through the worker
    pool to the handler

**If plan.md chose Push (webhook → queue → worker):**
- Read examples/event-driven-agent/ end-to-end.
- Files in order: main.go, event_agent.go, webhook_receiver.go,
  queue_consumer.go, event_processor.go, hitl_setup.go, handlers.go,
  setup.sh, k8-deployment.yaml (combined). Skim k8-deployment-api.yaml
  and k8-deployment-worker.yaml only if plan.md chose the split
  deployment shape. Skim alertmanager-config.yaml only if your event
  source is AlertManager.
- After reading, list:
  * File layout we'll mirror (typical: webhook_receiver,
    queue_consumer, event_processor as distinct files)
  * How the webhook receiver dedupes + enqueues events (Redis LPUSH?
    Streams? key pattern?)
  * How the queue consumer is registered as a core.Runnable
  * How trace context is propagated from webhook → queue → worker (this
    is the trickiest part — context doesn't flow through Redis natively;
    serialize as W3C `traceparent` and reconstruct on dequeue)
  * How HITL is wired in (only if plan.md uses it)
  * Anything specific to AlertManager that we'll replace with our event
    source

Do not write code yet.
```

**What to expect:** concrete file-by-file mapping. Trace propagation
across the queue boundary should be explicitly addressed.

---

## Step 5 — Implement

```
Step 5 of 12: implement the agent.

- Mirror the file layout from the reference you chose in Step 4:
  * Pull pattern → mirror examples/agent-with-async/: main.go,
    <domain>_agent.go (analog of travel_research_agent.go), handlers.go.
  * Push pattern → mirror examples/event-driven-agent/: main.go,
    <domain>_agent.go (analog of event_agent.go), webhook_receiver.go,
    queue_consumer.go, event_processor.go, plus hitl_setup.go and
    handlers.go if applicable.
- Use the deployment skeleton already in this folder. Rename
  "my-async-agent" everywhere to the actual agent name from plan.md.
  Keep port 8392 for now — Step 10 will replace it with the
  registry-allocated port.
- Use core.WithCORS([]string{"*"}, true) — async agents are typically
  server-to-server (the upstream system posts events). Use
  core.WithCORSDefaults() ONLY if a browser will hit /webhook directly,
  which is uncommon.
- Worker loop registration: use framework.RegisterRunnable() in main.go.
  See AGENT_DEVELOPMENT_GUIDE.md §10 "Background Jobs".
- Idempotency: implement the dedup key check from plan.md before any
  processing work begins. Use Redis SETNX with the dedup TTL.
- Trace context propagation across the queue: the webhook handler must
  serialize (W3C traceparent) the trace context into the queued payload;
  the worker must deserialize it into a new span when it dequeues.
- After writing all files, run:
  GOWORK=off go build ./...
  go build ./...

Stop and report when both builds pass. Do not deploy yet.
```

**What to expect:** clean compiles. If errors, debug with the agent
before moving on. Especially watch for trace-context serialization (it's
a common miss).

---

## Step 6 — Review against `AGENT_DEVELOPMENT_GUIDE.md`

```
Step 6 of 12: review the implementation against docs/AGENT_DEVELOPMENT_GUIDE.md.

Go through each section that applies to non-streaming/async agents (§1,
3, 4, 5, 6, 10, 12, 13) and check:
- Does our implementation follow the guide?
- Are there deviations? If so, are they justified?
- Are we missing anything required?

Pay particular attention to:
- §10 Background Jobs: is core.Runnable wired correctly?
- §13 Distributed Tracing: does context propagate through the queue?

Report findings as a checklist with ✓ / ⚠ markers and fix non-justified
deviations.
```

**What to expect:** a section-by-section pass on agent semantics.

---

## Step 7 — Vet against `ASYNC_ORCHESTRATION_GUIDE.md`

```
Step 7 of 12: vet the implementation against
docs/ASYNC_ORCHESTRATION_GUIDE.md.

Pay particular attention to these sections of the guide:
- §3 Understanding the Architecture — task pattern + HTTP 202 polling
- §6 Writing Task Handlers — handler signature, error returns,
  context cancellation
- §7 Progress Reporting — how a long-running task reports incremental
  state back to callers
- §8 Distributed Tracing Across Async Boundaries — read this carefully;
  trace propagation through a queue is the trickiest correctness concern
  in async agents
- §11 Configuration Reference — env vars and tuning knobs
- §12 Combining Async Tasks with HITL Approval — only if plan.md said
  HITL is required

Verify our implementation:
- Queue protocol matches the guide's recommendation
- Worker concurrency model is correct (single-flight per event ID, or
  parallel with idempotency, depending on the guide's pattern)
- Event idempotency is enforced before any side effect
- Backpressure / retry / dead-letter handling matches the guide
- HITL approval gating follows §12's pattern (task pause, approval
  callback, resume or escalate on timeout)
- Long-running operations report progress (§7) and don't block the
  worker pool

Report findings and fix gaps.
```

**What to expect:** async-specific patterns are correct. This is where
subtle bugs (event loss, double-processing, stuck approvals) come from.

---

## Step 8 — Vet against `DISTRIBUTED_TRACING_GUIDE.md`

```
Step 8 of 12: vet the implementation against docs/DISTRIBUTED_TRACING_GUIDE.md.

Verify:
- Inbound webhook is traced (spans for receive → enqueue)
- Trace context is serialized into the queued payload (W3C traceparent)
- Worker deserializes the trace context and starts the span tree from
  the upstream parent
- Every external HTTP call from the worker uses otelhttp.NewTransport
- HITL pause and resume are captured as span events (the trace shows
  the approval gap)
- Span names follow the convention
- No orphan spans

The worker-side span tree should look like:
  [webhook handler] → enqueue
                       (trace pauses here briefly)
  [worker dequeue] → [event processor]
                        ├─ [tool call: T1]
                        ├─ [tool call: T2]
                        └─ [HITL approval (if applicable)]

All sharing one trace_id from the original webhook.

Report findings and fix gaps.
```

**What to expect:** trace propagation across async boundaries — the
hardest tracing concern in this architecture.

---

## Step 9 — Vet against `LOGGING_IMPLEMENTATION_GUIDE.md`

```
Step 9 of 12: vet the implementation against docs/LOGGING_IMPLEMENTATION_GUIDE.md.

Verify:
- Logs use the framework's logger
- JSON format
- Standard fields: request_id (event_id), trace_id, capability,
  service, plus async-specific: event_source, dedup_key, queue_depth,
  worker_id
- Each event has a clear log trail: receive → enqueue → dequeue →
  process → complete (or error / HITL waiting / dropped as duplicate)
- No sensitive data leaked

Report findings and fix gaps.
```

**What to expect:** event-correlated logs that join Jaeger spans via
trace_id.

---

## Step 10 — Allocate a port from the registry

```
Step 10 of 12: allocate a port for this agent.

1. Open examples/README.md and find the port-allocation table.
2. Find the HIGHEST port currently allocated to any tool/agent/UI.
3. Pick the next port number (highest + 1) — NOT the lowest free port.
   NodePort follows the pattern 3<lastfourdigits>.
4. Add a new row to the table for this agent with the chosen port and
   NodePort and "agent" type.
5. Update every place in this folder that references the placeholder
   port 8392:
   - setup.sh
   - k8-deployment.yaml (containerPort, env PORT, service targetPort)
   - Dockerfile and Dockerfile.workspace (ENV PORT)
   - .env.example (PORT=)
   - README.md if it references the port

Report the chosen port + NodePort so I can confirm.
```

**What to expect:** sequential, registry-tracked allocation.

---

## Step 11 — Final review against the chosen reference for deviations

```
Step 11 of 12: do a final side-by-side review against the reference we
chose in plan.md.

- Pull (HTTP 202) → compare against examples/agent-with-async/.
  Particular places to scan:
  * Task API shape (POST /api/v1/tasks request/response, GET /api/v1/tasks/{id})
  * Worker pool registration and handler signatures
  * Progress reporting calls
  * Trace context propagation from API through worker

- Push (webhook → queue → worker) → compare against
  examples/event-driven-agent/. Particular places to scan:
  * Worker loop structure (how core.Runnable is wired)
  * Queue protocol (key naming, payload schema)
  * Trace context serialization (W3C `traceparent` into payload) and
    deserialization on dequeue
  * HITL setup
  * Metric names
  * If we chose the split api/worker deployment, compare the two split
    manifests against k8-deployment-api.yaml and k8-deployment-worker.yaml.

For EACH file we wrote, list every deliberate or accidental deviation
from the matching reference. For each, decide: keep it (with a one-line
justification) or revert to the reference pattern.

Aim for "as similar as possible to the reference, except where the
domain or event source forces a difference."
```

**What to expect:** all drift from the canonical reference is named and
justified.

---

## Step 12 — Deploy and verify

```
Step 12 of 12: deploy into the local Kind cluster and verify.

1. Cluster + infra must be up. If not, run an example's full-deploy
   first.
2. From this folder: `./setup.sh full-deploy` (cold-start) or
   `./setup.sh deploy` (cluster + infra already up).
3. Wait for rollout.
4. Pod is Running 1/1:
     kubectl get pod -n truvag3-examples -l app=<agent-name>
5. Agent registered in Redis — open http://registry.localhost and
   confirm <agent-name> appears.
6. End-to-end flow — branch on the async shape from plan.md:

   **Pull (HTTP 202 + polling):**
   - Submit a task:
       TASK_ID=$(curl -sS -X POST http://<agent-name>.localhost/api/v1/tasks \
         -H "Content-Type: application/json" \
         -d '<sample task JSON from plan.md>' | jq -r .task_id)
     Expect: HTTP 202; body contains task_id, status="queued",
     and status_url.
   - Poll until done (terminal statuses are: completed, failed, cancelled):
       while true; do
         STATUS=$(curl -sS http://<agent-name>.localhost/api/v1/tasks/$TASK_ID | jq -r .status)
         echo "$STATUS"; case "$STATUS" in completed|failed|cancelled) break;; esac
         sleep 2
       done
   - Watch logs in parallel:
       kubectl logs -n truvag3-examples deployment/<agent-name> --follow

   **Push (webhook → queue → worker):**
   - Send a real test event (use the sample payload from plan.md):
       curl -i -X POST http://<agent-name>.localhost/webhook \
         -H "Content-Type: application/json" \
         -d '<sample event JSON from plan.md>'
     Expect: 2xx (event accepted into queue).
   - Watch logs follow the event through:
       kubectl logs -n truvag3-examples deployment/<agent-name> --follow
     Confirm: receive → enqueue → dequeue → process → complete (or
     HITL-waiting → approve → complete).
   - Idempotency check: send the SAME event payload twice; verify the
     second is dropped as a duplicate (logs say so, only one trace tree
     in Jaeger).

7. Jaeger trace at http://jaeger.localhost shows the full async tree:
   - Pull: API request → submit task → (worker dequeue) → handler →
     tool calls → completion. All sharing one trace_id.
   - Push: webhook receive → enqueue → (gap) → worker dequeue → tool
     calls → completion. All sharing one trace_id.
8. Logs at http://grafana.localhost (admin/admin) → Explore → Loki:
   {app="<agent-name>"} returns JSON logs with request_id (task_id /
   event_id), trace_id, dedup_key (push only), capability, service.
9. (Optional, if HITL is enabled) Trigger a HITL-gated task/event,
   approve via the approver path, verify the worker resumes and the
   action completes. Verify trace shows the HITL pause/resume.

Report what you saw at each step. If anything is missing or broken,
fix it and re-deploy with `./setup.sh rollout` (config-only changes) or
`./setup.sh rebuild` (code changes).
```

**What to expect:** clean end-to-end async flow — pod running, event
accepted, processed exactly once, traces span the queue boundary, HITL
(if enabled) gates correctly, logs are structured.

---

## Done

Your async agent is ready. Drop the agent's `plan.md` into source
control if you want a record of the event-contract decisions, or delete
it if it's purely working memory.
