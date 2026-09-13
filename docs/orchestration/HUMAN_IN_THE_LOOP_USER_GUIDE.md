# Human-in-the-Loop (HITL) User Guide

This guide shows how to pause an agent for human approval and continue safely
after that decision. Start with the shared setup, then choose a JSON, streaming,
or queued transport. The framework owns checkpoint claims and completion;
your application supplies the code that processes the approved request.

> **Working Example**
>
> Runnable applications and transport implementations:
> - **Agent**: [`examples/agent-with-human-approval/`](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-human-approval)
> - **Frontend**: [`examples/chat-ui/hitl.html`](https://github.com/truvaagents/truva-g3/blob/main/examples/chat-ui/hitl.html)
> - **Queued worker**: [`examples/event-driven-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/event-driven-agent)
>
> The small recipes below use public framework APIs. They are application code,
> not extra framework helpers. The shipped chat example also adds sessions,
> custom response envelopes, and a retained store-owned expiry setup.

---

## Table of Contents

- [What is HITL?](#what-is-hitl)
- [Quick Start](#quick-start)
- [Core Concepts](#core-concepts)
- [Agent Implementation](#agent-implementation)
- [Frontend Integration](#frontend-integration)
- [Non-Streaming API](#non-streaming-api)
- [API Reference](#api-reference)
- [Resume Recipes](#resume-recipes)
  - [JSON resume](#recipe-json-resume)
  - [SSE resume](#recipe-sse-resume)
  - [Queued resume](#recipe-queued-resume)
- [Configuration](#configuration)
- [Expiry Processor Setup](#expiry-processor-setup)
- [Troubleshooting](#troubleshooting)
- [Auto-Resume](#auto-resume-timeout-auto-approval)
- [Registry Viewer](#registry-viewer-monitoring-hitl-checkpoints)
- [Production Deployment](#production-deployment)
- [Testing HITL Flows](#testing-hitl-flows)
  - [Manual verification checklist](#manual-verification-checklist)

---

## What is HITL?

Imagine you're building an AI assistant that can execute stock trades. The AI is smart, but you probably don't want it buying 10,000 shares of a meme stock without someone checking first. That's where HITL comes in.

HITL creates "checkpoints" in your AI workflow. When the AI reaches a checkpoint, it pauses and waits for a human to say "yes, go ahead" or "no, stop." Only after approval does execution continue.

```
User: "Buy 100 shares of AAPL"
         │
         ▼
    AI creates plan
         │
         ▼
    ┌─────────────────┐
    │ HITL Checkpoint │  ← Execution pauses here
    │   "Do you want  │
    │   to proceed?"  │
    └────────┬────────┘
             │
    ┌────────┴────────┐
    │                 │
  Approve          Reject
    │                 │
    ▼                 ▼
 Execute          Stop and
  trade           notify user
```

### When Should You Use HITL?

HITL adds overhead (the human has to respond), so use it when:

- **Financial operations**: Trades, payments, refunds - anything involving money
- **Data modifications**: Deleting records, updating databases, sending emails
- **External API calls with real effects**: Creating accounts, posting to social media
- **Compliance requirements**: When you need an audit trail of human approvals
- **Learning phase**: While you're still building confidence in your AI's decisions

Don't use HITL for everything - that defeats the purpose of automation. Use it strategically for high-stakes operations.

---

## Quick Start

### Prerequisites

Before adding HITL, you should have:
- A working TruvaG3 agent with an orchestrator
  - For **streaming agents** (SSE/WebSocket): See the [Chat Agent Guide](../memory-and-chat/CHAT_AGENT_GUIDE.md)
  - For **non-streaming agents** (JSON request/response): See [`examples/agent-with-orchestration/`](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-orchestration)
  - For **long-running operations** (HTTP 202 + polling): See the [Async Orchestration Guide](ASYNC_ORCHESTRATION_GUIDE.md)
- Redis running (HITL uses Redis to persist checkpoint state)
- An AI provider API key

> **Note**: HITL works with both streaming and non-streaming agents. The core pattern is the same - the difference is how checkpoints are delivered to the client (SSE events vs JSON responses) and how resume is handled.

### Environment Setup

Here's a minimal `.env` file to get started:

```bash
# Required infrastructure
REDIS_URL=redis://localhost:6379
PORT=8352

# Agent identity — required when multiple HITL agents share Redis (see "Agent Isolation")
TRUVAG3_AGENT_NAME=my-agent

# AI provider (at least one)
OPENAI_API_KEY=your-key
# or ANTHROPIC_API_KEY=your-key

# HITL settings
TRUVAG3_HITL_ENABLED=true                    # Turn HITL on
TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true      # Pause after plan generation
TRUVAG3_HITL_DEFAULT_TIMEOUT=5m              # 5 minute approval window
```

**What do these settings mean?**

- `TRUVAG3_AGENT_NAME`: Scopes this agent's HITL state to the cluster-tagged `truvag3:v1:<deployment>:hitl:{<deployment>:hitl:<agent_name>}:*` key family. Without it, unscoped agents in the same deployment share one pending index, so a fan-out can receive the same checkpoint from several agents. The current Registry Viewer deduplicates that response by `checkpoint_id`, but identity is still required for correct ownership, routing, and isolation. K8s manifests typically set it; for local development with more than one HITL agent on the same Redis/Valkey deployment, set it explicitly. The framework logs a startup Warn if it detects the shared-prefix configuration. See [Agent Isolation](#agent-isolation) for the full mechanism.
- `TRUVAG3_HITL_ENABLED`: The master switch. Without this, nothing HITL-related happens.
- `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL`: When true, the AI pauses after generating a plan but before executing anything. This lets you review the entire plan at once.
- `TRUVAG3_HITL_DEFAULT_TIMEOUT`: How long to wait for human response. After this, the system takes a default action (usually reject for safety).

### Adding HITL to Your Agent

Use this startup order:

```text
create framework host/logger
    -> compose backend adapters and expiry processor
    -> create the application's orchestrator
    -> wire policy, controller, and resume coordinator
    -> register routes and background runnables
    -> start serving requests
```

The host and orchestrator come from your existing agent. This section adds
HITL; it does not replace the agent bootstrap. With `BaseAgent`, call
`core.NewFramework` before using `agent.Logger` to create long-lived components.
The host installs the configured logger; the earlier logger is a no-op.

First, include HITL in your application's backend composition. If you already
use `redisprovider.NewDefaultBackends`, add the HITL role and expiry option to
that call rather than creating another bundle. For a new HITL-only bundle, the
following startup block uses the included provider. It needs the `os`,
`orchestration`, and `orchestration/redisprovider` imports:

```go
expiryRuntime, err := orchestration.LoadCheckpointExpiryRuntimeConfigFromEnvironment(
	orchestration.DefaultCheckpointExpiryRuntimeConfig(), os.LookupEnv,
)
if err != nil {
	return err
}
owned, err := redisprovider.NewDefaultBackends(logger,
	redisprovider.WithDefaultBackendRoles(redisprovider.ClientRoleHITL),
	redisprovider.WithDefaultBackendProviderOptions(redisprovider.WithCheckpointExpiry(
		orchestration.ExpiryProcessorConfigFromEnv(), nil,
		orchestration.WithCheckpointExpiryRuntimeConfig(expiryRuntime),
	)),
)
if err != nil {
	return err
}
defer owned.Close() // Keep this startup function alive until the host stops.
backends := owned.Backends()
```

This reads the application's Redis/Valkey topology configuration, including
cluster mode. Keep the same deployment namespace and agent scope across the
agent's replicas. All Redis roles use DB 0 with separate key families. The
`nil` expiry callback means "update durable expiry status without an application
notification"; it does not disable expiry. Auto-approval does not itself run
the approved work. A UI or worker must subsequently request resume.

If your process also needs execution or LLM-debug storage, include those roles
in the existing bundle and pass it through `deps.Backends` to the orchestrator.
See the [Backend Portability Guide](ORCHESTRATION_BACKEND_PORTABILITY_GUIDE.md)
for connection ownership, injected clients, and non-Redis providers.

Next, put this application helper in `hitl_runtime.go`. It deliberately accepts
the provider-neutral bundle; JSON, SSE, and worker paths share the coordinator
it returns. The ordinary processing adapter returns the **actual execution
result**, not a success value reconstructed from the response text.

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

type HITLRuntime struct {
	Coordinator *orchestration.ResumeCoordinator
	Handler     *orchestration.HITLHandler
}

func NewHITLRuntime(
	backends *orchestration.OrchestrationBackends,
	orch *orchestration.AIOrchestrator,
	config orchestration.HITLConfig,
	logger core.Logger,
) (*HITLRuntime, error) {
	if backends == nil || orch == nil || !config.Enabled {
		return nil, fmt.Errorf("HITL requires backends, an orchestrator, and enabled configuration")
	}
	requirements, err := orchestration.RequirementsForFeatures(nil,
		orchestration.BackendFeatureHITLResume,
		orchestration.BackendFeatureCheckpointExpiry,
	)
	if err != nil {
		return nil, err
	}
	if err := backends.ValidateFor(requirements); err != nil {
		return nil, err
	}

	policy := orchestration.NewRuleBasedPolicy(config, orchestration.WithPolicyLogger(logger))
	controller := orchestration.NewInterruptController(
		policy, backends.Checkpoints(), orchestration.NewNoOpInterruptHandler(),
		orchestration.WithControllerLogger(logger),
	)
	resumeConfig, err := orchestration.LoadResumeCoordinatorRuntimeConfigFromEnvironment(
		orchestration.DefaultResumeCoordinatorRuntimeConfig(), os.LookupEnv,
	)
	if err != nil {
		return nil, err
	}
	executor := orchestration.ResumeExecutorFunc(func(
		ctx context.Context, cp *orchestration.ExecutionCheckpoint,
	) (*orchestration.ExecutionResult, error) {
		_, execution, err := orch.ProcessRequestWithExecution(ctx, cp.OriginalRequest, cp.UserContext)
		return execution, err
	})
	coordinator, err := orchestration.NewResumeCoordinator(
		backends.CheckpointResume(), executor, resumeConfig,
		orchestration.WithResumeLogger(logger),
	)
	if err != nil {
		return nil, err
	}
	handler, err := orchestration.NewHITLHandler(controller, backends.Checkpoints(),
		orchestration.WithHITLResumer(coordinator),
		orchestration.WithHITLHandlerLogger(logger),
	)
	if err != nil {
		return nil, err
	}
	orch.SetInterruptController(controller)
	return &HITLRuntime{Coordinator: coordinator, Handler: handler}, nil
}

// Application helper for the BaseAgent capability router.
func RegisterHITLRoutes(agent *core.BaseAgent, handler *orchestration.HITLHandler, resume http.HandlerFunc) {
	for _, capability := range []core.Capability{
		{Name: "hitl_command", Endpoint: "/hitl/command", Handler: handler.HandleCommand, Internal: true},
		{Name: "hitl_checkpoints", Endpoint: "/hitl/checkpoints", Handler: handler.HandleListCheckpoints, Internal: true},
		{Name: "hitl_checkpoint", Endpoint: "/hitl/checkpoints/{id}", Handler: handler.HandleGetCheckpoint, Internal: true},
		{Name: "hitl_resume", Endpoint: "/hitl/resume/{id}", Handler: resume, Internal: true},
	} {
		agent.RegisterCapability(capability)
	}
}
```

The no-op interrupt **notification handler** only disables webhook notification;
the real policy/controller still creates interruptions. This recipe delivers
checkpoints through the request response and the query routes. If your app needs
webhooks or command Pub/Sub, supply its notification handler and command store
as in the shipped example. Neither is needed to make a direct HTTP approval
followed by a coordinator resume work.

Finish wiring in the startup function. Here `orch` was constructed using the
same effective configuration, `framework` is your host, and `baseAgent` is the
`*core.BaseAgent` embedded in your application's agent. `config` is the effective
orchestrator configuration (for example, from `orchestration.DefaultConfig()`),
and `ctx` is the application's lifetime context, canceled during shutdown,
not an HTTP request context:

```go
hitl, err := NewHITLRuntime(backends, orch, config.HITL, logger)
if err != nil {
	return err
}
RegisterHITLRoutes(baseAgent, hitl.Handler, hitl.Handler.HandleResume)
for _, runnable := range backends.Runnables() {
	framework.RegisterRunnable(runnable)
}
return framework.Run(ctx)
```

Register each runnable only once. `owned.Close()` runs after the host drains
requests and runnables; the coordinator itself needs no `Close()`. If using your
own `net/http` server instead of BaseAgent routing, call
`hitl.Handler.RegisterRoutes(mux)` on that server's `http.ServeMux`. Do not
register both a JSON and SSE implementation at the same path; the recipes below
show the two choices.

`SetupHITL` and `RegisterHITLCapabilities` in the shipped example are application
helpers, not framework methods. The example's current helper takes
`SetupHITL(redisClient, keyspace, agentScope, logger, hitlConfig)`, and its custom
resume transports use the coordinator assigned to `hitl.Resumer` during startup.
Copying only its route registration is not enough. See
[main.go](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/main.go)
and [hitl_setup.go](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/hitl_setup.go)
for that complete application. For a provider-neutral expiry setup without the
preset, follow [Expiry Processor Setup](#expiry-processor-setup)
and register the provider-neutral processor with the framework.

---

## Core Concepts

### Checkpoints: Saving Execution State

When HITL pauses execution, it creates a "checkpoint" - a snapshot of everything needed to resume later. Think of it like a save game in a video game.

The checkpoint includes:
- **The plan**: What steps the AI decided to take
- **Completed steps**: What's already been done (so we don't redo work)
- **Current step**: What we're pausing before (for step-level approval)
- **Resolved parameters**: The actual values that will be sent to tools
- **Original request**: The user's query (needed to re-process on resume)
- **User context**: Application metadata needed by the resume handler, including
  `session_id` and the canonical `conversation_id` when the request belongs to
  a multi-turn conversation
- **Trace lineage**: Original request, trace, and span identifiers used to link
  the resumed execution
- **Expiration time**: When the checkpoint times out

The included provider stores checkpoints in Redis/Valkey. The decision deadline
and storage TTL are different: the expiry processor changes an overdue decision's
status, while the storage TTL eventually removes its data. A resume claim has a
third timer, its lease; renewing that lease extends neither of the other timers.

### Interrupt Points: Where Can HITL Pause?

HITL currently supports three interrupt points:

**1. After Plan Generation (`plan_generated`)**

The AI looks at the user's request, figures out what tools to use and in what order, and creates a plan. HITL can pause here so you can review the entire plan before any tools execute.

```
User: "What's the weather in Tokyo and the stock price of AAPL?"
         │
         ▼
    AI generates plan:
    1. Call weather-tool for Tokyo
    2. Call stock-tool for AAPL
    3. Synthesize response
         │
         ▼
    [HITL PAUSES HERE - "plan_generated"]
         │
         ▼
    Human reviews plan → Approves
         │
         ▼
    Execute all steps
```

Enable with: `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true`

**2. Before a Specific Step (`before_step`)**

Sometimes you only care about certain tool calls. For example, "reading data is fine, but pause before any writes." HITL can pause just before executing a specific step.

```
Plan approved, executing:
    Step 1: Read user data → OK, executes
    Step 2: Delete user account (sensitive!)
         │
         ▼
    [HITL PAUSES HERE - "before_step"]
         │
         ▼
    Human reviews step → Approves
         │
         ▼
    Step 2 executes
```

Enable with: `TRUVAG3_HITL_SENSITIVE_CAPABILITIES=delete_user,transfer_funds`

**3. Error Escalation (`on_error`)**

When a tool call fails repeatedly, you may want human intervention rather than silent failure. HITL can pause after a configured number of retry failures, escalating to a human for decision.

```
Executing plan:
    Step 1: Call external API
         │
         ▼
    Attempt 1: Failed (timeout)
    Attempt 2: Failed (timeout)
    Attempt 3: Failed (timeout)
         │
         ▼
    [HITL PAUSES HERE - "on_error"]
         │
         ▼
    Human reviews: Approve another attempt? Reject? Abort?
```

Enable with: `TRUVAG3_HITL_ESCALATE_AFTER_RETRIES=3`

When the configured number of retries is exceeded, the checkpoint includes the error details and allows the human to:
- **Approve**: Permit another attempt at the interrupted step
- **Reject**: Stop the execution
- **Abort**: Stop the entire workflow

**4. Reserved Interrupt Points**

The following interrupt points are defined but not yet implemented:

| Interrupt Point | Purpose | Status |
|-----------------|---------|--------|
| `after_step` | Validate step output before proceeding | Reserved for future use |
| `context_gathering` | Review context before planning | Reserved for future use |

These are placeholders for future scenarios like output validation (checking tool results before continuing) and pre-planning review (letting humans add context before the AI plans).

### Policies: Deciding When to Pause

The policy component decides whether to pause at each potential interrupt point. You configure it via environment variables:

| Variable | Example Value | What it Does |
|----------|---------------|--------------|
| `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | `true` | Pause after every plan |
| `TRUVAG3_HITL_SENSITIVE_CAPABILITIES` | `stock_quote,execute_trade` | Pause for these capabilities (plan + step approval) |
| `TRUVAG3_HITL_SENSITIVE_AGENTS` | `payment-service,trading-bot` | Pause for these agents (plan + step approval) |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | `delete_user,transfer_funds` | Pause only at the step level (skip plan approval) |
| `TRUVAG3_HITL_STEP_SENSITIVE_AGENTS` | `admin-service` | Pause only at the step level (skip plan approval) |

The difference between `SENSITIVE_*` and `STEP_SENSITIVE_*`:
- `SENSITIVE_*`: Requires plan approval first, then step approval when that capability is used
- `STEP_SENSITIVE_*`: Skips plan approval, only pauses right before the sensitive step

Use `STEP_SENSITIVE_*` when you want to let the AI start working, but pause before the risky part.

### The Two-Phase Approval Pattern

This is important: approval happens in **two HTTP requests**, not one.

```
Phase 1: POST /hitl/command     →  "I approve checkpoint cp-abc123"
                                    Response: {"should_resume": true}

Phase 2: POST /hitl/resume/{id} →  "Continue execution from cp-abc123"
                                    Response: SSE stream with results
```

**Why two phases?**

Phase 1 is lightweight - it just records your decision in Redis and returns immediately. This is important because:
- The decision needs to be recorded quickly (before timeout)
- Multiple clients might be checking the checkpoint status

Phase 2 claims the checkpoint and processes its approved plan. JSON and SSE
routes run this work during the resume HTTP request. Only an application that
explicitly submits a queue task runs it independently of that connection.
Separating approval from resume records the decision before starting this work;
it does not by itself create a background job.

---

## Agent Implementation

### Handling HITL Interrupts

Here's the key insight: when HITL pauses, the orchestrator returns an error. But it's not really an error - it's a signal that says "I stopped on purpose, here's the checkpoint."

```go
result, err := t.orchestrator.ProcessRequestStreaming(ctx, query, nil, streamCallback)

if err != nil {
    // Is this a HITL interrupt?
    if orchestration.IsInterrupted(err) {
        // Yes! Get the checkpoint and send it to the frontend
        checkpoint := orchestration.GetCheckpoint(err)
        callback.SendCheckpoint(checkpoint)
        return err  // Return the "error" - it's expected
    }
    // This is an actual error
    return fmt.Errorf("orchestration failed: %w", err)
}

// No interrupt, no error - we have a result
callback.SendDone(result.RequestID, result.AgentsInvolved, result.ExecutionTime.Milliseconds())
```

The `IsInterrupted()` function checks if the error is a HITL pause. If it is, `GetCheckpoint()` extracts the checkpoint data, which you send to the frontend.

See [chat_agent.go:ProcessWithStreaming](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/chat_agent.go) for the complete implementation.

### Setting Request Mode (Important!)

HITL behaves differently for streaming vs non-streaming requests. You need to tell it which mode you're using:

```go
// In your SSE streaming handler:
ctx = orchestration.WithRequestMode(ctx, orchestration.RequestModeStreaming)

// In your JSON/async handler:
ctx = orchestration.WithRequestMode(ctx, orchestration.RequestModeNonStreaming)
```

**Why does this matter?**

When a checkpoint times out (user didn't respond), what should happen?

- **Streaming**: User was watching the approval dialog but walked away. They saw it, they chose not to respond. Default: do nothing (implicit deny).
- **Non-streaming**: User submitted a request and closed the browser. They might not have seen the dialog. Default: apply the configured action (usually reject).

If you forget to set the mode, HITL logs a warning and uses the default from `TRUVAG3_HITL_DEFAULT_REQUEST_MODE`.

### The Resume Handler

The application route calls `ResumeCoordinator.ResumeExecution`, or
`ResumeWithExecutor` for a request-local SSE callback. The coordinator claims
the saved approval and passes the complete restored context to the adapter.

The adapter calls the existing result-bearing processing method. It does not
replan independently or write a completed status. The framework reuses the
stored plan and completed steps, preserving approved parameters and skills.

A second interruption becomes a new checkpoint only after the parent is saved
as `continued`. An error during that save must remain an error, even if it
wraps an interruption. See [Implementing Resume Handlers](#implementing-resume-handlers-agent-specific)
for construction, transport contracts, and cancellation behavior.

---

## Frontend Integration

### Handling Checkpoint Events

When the backend pauses for HITL, it sends a `checkpoint` event via SSE:

```javascript
function handleSSEEvent(eventType, data) {
    switch (eventType) {
        case 'chunk':
            // Normal response text, append to UI
            appendToResponse(data.text);
            break;

        case 'checkpoint':
            // HITL pause! Show approval dialog
            showApprovalDialog(data);

            // Important: Save request_id for trace correlation
            if (data.request_id && !originalRequestId) {
                originalRequestId = data.request_id;
            }
            break;

        case 'done':
            // Execution completed
            showComplete(data);
            break;

        case 'error':
            // Something went wrong
            showError(data.message);
            break;
    }
}
```

### Building an Approval Dialog

The checkpoint data tells you everything you need to show the user:

```javascript
function showApprovalDialog(checkpoint) {
    // What type of approval is this?
    const isStepLevel = checkpoint.interrupt_point === 'before_step';

    if (isStepLevel) {
        // Show the specific step awaiting approval
        displayStep(checkpoint.current_step);
        displayParameters(checkpoint.resolved_parameters);
    } else {
        // Show the full plan
        displayPlan(checkpoint.plan);
    }

    // Show expiration countdown
    startExpiryTimer(checkpoint.expires_at);

    // Show approve/reject buttons
    showButtons();
}
```

The `resolved_parameters` field is particularly useful for step-level approval - it shows the actual values that will be passed to the tool, so the user can verify them.

> **SSE Event Formats by Interrupt Point**
>
> The checkpoint data structure varies depending on the interrupt point:
> - **`plan_generated`**: Includes the full plan with all proposed steps
> - **`before_step`**: Includes current step details, resolved parameters, and completed steps
> - **`on_error`**: Includes error context with retry attempts and recoverability flag
>
> For the complete JSON schemas, see [Interrupt Points](../reference/API_REFERENCE.md#interrupt-points) in the API Reference.

### Submitting the Approval

Remember the two-phase pattern? Here's how it works in the frontend:

```javascript
async function submitApproval(decision) {
    const checkpointId = pendingCheckpoint.checkpoint_id;
    hideApprovalDialog();

    // Phase 1: Submit the decision (instant)
    const response = await fetch(`${backendUrl}/hitl/command`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            checkpoint_id: checkpointId,
            type: decision  // 'approve' or 'reject'
        })
    });

    const result = await response.json();

    // Phase 2: If approved, resume execution
    if (decision === 'approve' && result.should_resume) {
        await resumeExecution(checkpointId);
    } else if (decision === 'reject') {
        showMessage('Request was rejected');
    }
}
```

### Resuming Execution

The resume call opens a new SSE stream:

```javascript
async function resumeExecution(checkpointId) {
    const headers = {
        'Accept': 'text/event-stream'
    };

    // The coordinator restores lineage from the saved checkpoint.

    const response = await fetch(`${backendUrl}/hitl/resume/${checkpointId}`, {
        method: 'POST',
        headers
    });

    if (!response.ok) {
        const failure = await response.json();
        throw new Error(failure.code + ': ' + failure.error);
    }
    // A 200 stream handshake is not completion. Require a terminal event.
    await processSSEStream(response);
}
```

The resume stream uses the same event format as the initial request, so you can reuse your SSE handler. This is nice because you don't need separate logic - chunks, steps, checkpoints (for chained approvals), and done events all work the same way.

See [hitl.html](https://github.com/truvaagents/truva-g3/blob/main/examples/chat-ui/hitl.html) for the complete frontend implementation.

### Supported human commands

The default controller and HTTP handler support **approve**, **reject**, and
**abort**. They reject **edit**, **skip**, **retry**, and **respond** with HTTP
400 and code `unsupported_command`, before changing state or publishing a
command. Their enum values and data fields do not imply an implemented workflow.

An approval continues the stored plan and approved parameters unchanged.
An `on_error` approval permits another attempt at the interrupted step;
it does not grant a separate edit/skip/retry protocol. Applications needing
those protocols must design and validate them explicitly.

## Non-Streaming API

Not everyone uses SSE. If you're building a CLI tool, a backend service, or just prefer REST APIs, HITL works with JSON request/response too.

### How It Differs from Streaming

In streaming mode, checkpoints arrive as SSE events. In non-streaming mode, checkpoints are returned in the JSON response with `interrupted: true`.

### Complete Example: Plan + Step Approval (Chained)

This example shows the full flow when both plan approval and step approval are required. The request involves selling TESLA shares, which triggers:
1. Plan approval (because `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true`)
2. Step approval (because `stock_quote` is in `TRUVAG3_HITL_SENSITIVE_CAPABILITIES`)

**Step 1: Send Initial Request**

```bash
curl -X POST http://localhost:8352/chat \
  -H "Content-Type: application/json" \
  -d '{
    "request": "I want to sell 100 TESLA shares to fund my trip to London for a week. Will I be able to afford it?"
  }'
```

**Response (Plan Checkpoint):**

```json
{
  "request_id": "orch-1769372315637984136",
  "session_id": "aca6d5f6-fc8d-4724-a2d6-d4a58d2fd9cf",
  "interrupted": true,
  "checkpoint": {
    "checkpoint_id": "cp-dff21578-e9e1-40",
    "interrupt_point": "plan_generated",
    "decision": {
      "reason": "plan_approval",
      "message": "Plan approval required for request: I want to sell 100 TESLA shares...",
      "priority": "normal",
      "default_action": "reject"
    },
    "plan": {
      "plan_id": "trip-to-london-001",
      "steps": [
        {
          "step_id": "step-1",
          "agent_name": "stock-service",
          "instruction": "Get the current stock quote for TESLA",
          "metadata": {"capability": "stock_quote", "parameters": {"symbol": "TSLA"}}
        },
        {
          "step_id": "step-2",
          "agent_name": "geocoding-tool",
          "instruction": "Geocode London to get coordinates"
        },
        {
          "step_id": "step-3",
          "agent_name": "currency-tool",
          "instruction": "Get USD to GBP exchange rates"
        },
        {
          "step_id": "step-4",
          "agent_name": "weather-tool-v2",
          "instruction": "Get weather forecast for London"
        }
      ]
    },
    "status": "pending",
    "expires_at": "2026-01-25T20:19:45.056924126Z"
  },
  "duration_ms": 9781
}
```

**Step 2: Approve the Plan**

```bash
curl -X POST http://localhost:8352/hitl/command \
  -H "Content-Type: application/json" \
  -d '{"checkpoint_id": "cp-dff21578-e9e1-40", "type": "approve"}'
```

**Response:**

```json
{
  "checkpoint_id": "cp-dff21578-e9e1-40",
  "action": "approve",
  "should_resume": true
}
```

**Step 3: Resume Execution**

```bash
curl -X POST http://localhost:8352/hitl/resume-sync/cp-dff21578-e9e1-40
```

**Response (Step Checkpoint - Chained Approval):**

Because `stock_quote` is a sensitive capability, execution pauses again before calling the stock service:

```json
{
  "request_id": "orch-1769372352581198666",
  "session_id": "aca6d5f6-fc8d-4724-a2d6-d4a58d2fd9cf",
  "interrupted": true,
  "checkpoint": {
    "checkpoint_id": "cp-6b38d8c6-d2b4-46",
    "interrupt_point": "before_step",
    "decision": {
      "reason": "sensitive_operation",
      "message": "Step approval required for operation: stock-service.stock_quote",
      "priority": "high",
      "metadata": {
        "agent_name": "stock-service",
        "capability": "stock_quote",
        "trigger": "step_sensitive_capability"
      }
    },
    "current_step": {
      "step_id": "step-1",
      "agent_name": "stock-service",
      "instruction": "Get the current stock quote for TESLA"
    },
    "resolved_parameters": {
      "symbol": "TSLA"
    },
    "status": "pending"
  },
  "duration_ms": 966
}
```

Notice the key differences from the plan checkpoint:
- `interrupt_point` is `"before_step"` instead of `"plan_generated"`
- `current_step` shows which step is awaiting approval
- `resolved_parameters` shows the actual values that will be sent to the tool

**Step 4: Approve the Step**

```bash
curl -X POST http://localhost:8352/hitl/command \
  -H "Content-Type: application/json" \
  -d '{"checkpoint_id": "cp-6b38d8c6-d2b4-46", "type": "approve"}'
```

**Step 5: Resume to Completion**

```bash
curl -X POST http://localhost:8352/hitl/resume-sync/cp-6b38d8c6-d2b4-46
```

**Response (Completed):**

```json
{
  "request_id": "orch-1769372362628519713",
  "session_id": "aca6d5f6-fc8d-4724-a2d6-d4a58d2fd9cf",
  "response": "To determine if selling 100 Tesla shares will fund your trip... Based on the current value of $449.07 per share, you would receive $44,907 USD...",
  "tools_used": ["geocoding-tool", "currency-tool", "weather-tool-v2", "stock-service"],
  "confidence": 0.95,
  "interrupted": false,
  "duration_ms": 10740
}
```

See [handlers.go:handleResumeSyncJSON](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/handlers.go) for the implementation.

---

## API Reference

> **Complete API Details**: For SSE event schemas, command type matrices, helper functions, and type definitions, see the [HITL section in API_REFERENCE.md](../reference/API_REFERENCE.md#human-in-the-loop-hitl).

HITL endpoints are split between framework-provided and agent-specific:

| Endpoint | Provider | Why |
|----------|----------|-----|
| `POST /hitl/command` | **Framework** | Generic approval logic |
| `GET /hitl/checkpoints` | **Framework** | Generic checkpoint listing |
| `GET /hitl/checkpoints/{id}` | **Framework** | Generic checkpoint retrieval |
| `POST /hitl/resume/{id}` | **Framework or agent transport** | The framework JSON handler uses an injected resumer; an SSE adapter uses the same coordinator |
| `POST /hitl/resume-sync/{id}` | **Agent** | Uses the coordinator with the agent's buffered processing adapter |

Resume lifecycle ownership belongs to the framework. Applications supply their processing adapter and optional streaming or queued transport; they do not write completion statuses themselves.

### Registering Framework Endpoints

Create an `HITLHandler` and register its routes:

```go
// Create the handler with controller and checkpoint store
hitlHandler, err := orchestration.NewHITLHandler(
	hitl.Controller,
	hitl.CheckpointStore,
	orchestration.WithHITLHandlerLogger(logger),       // Optional
	orchestration.WithHITLHandlerTelemetry(telemetry), // Optional
	orchestration.WithHITLResumer(coordinator),        // Optional resume route
)
if err != nil {
	return err
}

// Option 1: Use RegisterRoutes for automatic registration
hitlHandler.RegisterRoutes(mux) // Includes /hitl/resume/ only when a resumer is supplied

// Option 2: Register handlers individually for more control
mux.HandleFunc("/hitl/command", hitlHandler.HandleCommand)
mux.HandleFunc("/hitl/checkpoints", hitlHandler.HandleListCheckpoints)
mux.HandleFunc("/hitl/checkpoints/", hitlHandler.HandleGetCheckpoint) // Note: trailing slash for path param
```

Then add your agent-specific resume handlers (see [Implementing Resume Handlers](#implementing-resume-handlers-agent-specific) below).

For a complete example, see [chat_agent.go:RegisterHITLCapabilities](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/chat_agent.go).

### Implementing Resume Handlers (Agent-Specific)

Use `ResumeCoordinator` for synchronous HTTP, streaming, delegation, and
queued work. It borrows a `CheckpointResumePersistence` and a
`ResumeExecutor`; the latter calls your normal application processing method.
With the included Redis provider, the checkpoint adapter implements both the
ordinary persistence and resume contracts.

```go
executor := orchestration.ResumeExecutorFunc(func(
	ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint,
) (*orchestration.ExecutionResult, error) {
	// Use the real terminal result; successful tool steps alone are not success.
	_, execution, err := orchestrator.ProcessRequestWithExecution(
		ctx, checkpoint.OriginalRequest, checkpoint.UserContext,
	)
	return execution, err
})
config, err := orchestration.LoadResumeCoordinatorRuntimeConfigFromEnvironment(
	orchestration.DefaultResumeCoordinatorRuntimeConfig(), os.LookupEnv,
)
if err != nil {
	return err
}
coordinator, err := orchestration.NewResumeCoordinator(
	checkpointStore, executor, config, orchestration.WithResumeLogger(logger),
)
if err != nil {
	return err
}

// At the request boundary:
_, err = coordinator.ResumeExecution(ctx, checkpointID)
return err
```

If using composed backends, validate `BackendFeatureHITLResume` and obtain
`backends.CheckpointResume()`. Do not construct a second Redis client or use a
different DB for resumption.

#### What the coordinator owns

When using `BaseAgent`, call `core.NewFramework` before passing
`agent.Logger` to the coordinator or other long-lived HITL dependencies.
`NewBaseAgent` initially supplies a no-op logger; host construction installs
the configured logger. Dependencies retain the logger they receive.

```text
pending --approve--> approved --claim--> resuming --success--> completed
                                               |
                                               +--new checkpoint--> continued
                                               |                    |
                                               |                    +--> successor checkpoint
                                               +--failure, no child--> approved
```

- Only `approved` and `expired_approved` may start an attempt. An existing
  `resuming` record may be recovered after its lease expires.
- A concurrent owner receives `resume_in_progress`; it runs no application
  work. Each acquired attempt has an internal identity and a renewable lease.
- The coordinator calls `BuildResumeContext` once. The adapter receives the
  stored plan, completed results, approved parameters, pinned skill state,
  request mode, metadata, and linked trace context.
- Already completed steps are reused. The checkpoint-approved plan does not
  rerun `AfterPlanning`; adopters must revalidate time-sensitive authority at
  the action boundary when governance can change during an approval window.
- Success means a real successful **terminal** `ExecutionResult`, no error,
  and successful persistence. Synthesis, hooks, and in-execution stream
  callback delivery are part of that result. Never infer it from `err == nil`
  or tool results alone.
- A second interruption reserves and saves one successor. The parent becomes
  terminal `continued`, not `completed`. Recovery returns the saved child
  without replaying the parent's work.
- Ordinary failure without a child releases the claim to its previous approval
  status. Ownership loss, ambiguous persistence failure, and panic do not
  authorize immediate replay. Tools may already have acted.
- Generic saves, stale command decisions, and deletion cannot overwrite an
  owned attempt. Deletion of a `resuming` checkpoint is rejected even after
  lease expiry; recover the attempt first.
- Claim/renew/finalize preserve the record's remaining storage TTL. Lease
  renewal does **not** extend record retention or the human decision window.

#### HTTP and SSE outcomes

| Result | JSON transport | Streaming transport |
|---|---|---|
| Persisted completion | 200 with result | One `done` event, after finalization |
| Persisted continuation | 202 with `interrupted: true` and public checkpoint | One `checkpoint` event with the authoritative successor |
| Another live owner | 409 `resume_in_progress` | Same JSON rejection before stream headers |
| Unapproved or terminal | 409 `resume_not_resumable` | Same JSON rejection before stream headers |
| Ownership lost after claim | 409 `resume_claim_lost`; work may have executed | JSON before headers, otherwise terminal `error` with `retryable: false` |
| Other post-claim failure | 500 `resume_failed`; inspect execution evidence | JSON before headers, otherwise terminal `error` with `retryable: false` |

An SSE HTTP 200 only confirms that streaming started. Progress chunks and
`finish` are not final business outcomes. A stream that ends without `done`,
`checkpoint`, or `error` has an unknown outcome; do not resubmit automatically.
Use `ResumeWithExecutor` with a request-local callback for SSE, forwarding
progress but holding terminal events until the coordinator returns.

The final JSON response or SSE `done` acknowledgement is sent **after** durable
completion. If that acknowledgement is lost, the checkpoint may already be
`completed`. Reload its state and inspect execution evidence before retrying;
failure to receive the acknowledgement does not undo completed work.

A lifecycle error can wrap an interruption. Check
`errors.As(err, &lifecycle)` for `*ErrCheckpointResumeLifecycle` **before**
using `IsInterrupted`; failed finalization is not a successful continuation.
`HITLErrorResponse` supplies the shared stable error mapping.

#### Cancellation, ownership, and public data

HTTP/SSE work uses the caller's request context. A disconnect cancels active
execution cooperatively. Queued work uses the worker's independent task
context, so closing the submitting HTTP request does not cancel an accepted
task; its own deadline and shutdown still apply. The coordinator joins its
per-call renewal worker before returning or propagating a panic.

After execution settles, cleanup uses a bounded detached context to persist
the outcome. A disconnected client may not receive it: inspect the stored
checkpoint and trace before deciding to retry. The coordinator owns no
background resources between calls and needs no `Close()`; hosts drain
requests before closing borrowed dependencies.

`CheckpointResponseFrom` projects checkpoints at the HTTP/SSE boundary.
It preserves application data and exposes parent/successor navigation, but
does not expose attempt IDs, lease deadlines, process owners, or reservations.
Full ownership remains in persistence and internal observability. This is
explicit protocol field selection, not payload redaction.

#### Resume configuration

Load these settings explicitly at application startup with
`LoadResumeCoordinatorRuntimeConfigFromEnvironment`. Constructing the
coordinator itself never reads environment variables.

| Setting | Default | Validation |
|---|---|---|
| `TRUVAG3_HITL_RESUME_CLAIM_LEASE` | `30s` | 3 seconds–24 hours |
| `TRUVAG3_HITL_RESUME_CLEANUP_TIMEOUT` | `5s` | Positive, at most 1 minute and at most one third of the lease |

Renewal runs every one third of the lease while executing, plus once before
finalization. These settings do not replace `TRUVAG3_HITL_DEFAULT_TIMEOUT`
or the checkpoint storage TTL. Constructors reject nil/typed-nil dependencies
and invalid explicit options. Omitting `WithHITLResumer` leaves command/query
routes available but registers no resume route.

**Framework Helpers Reference:**

*Resume Context Helpers* ([hitl_helpers.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_helpers.go), [orchestrator.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/orchestrator.go))

| Helper | Purpose |
|--------|---------|
| `BuildResumeContext(ctx, checkpoint)` | Lower-level context/link helper called by the coordinator; does not claim or finalize |
| `WithResumeMode(ctx, checkpointID)` | Marks context as resume, prevents re-interrupt |
| `WithPlanOverride(ctx, plan)` | Injects stored plan (critical for step ID matching) |
| `WithCompletedSteps(ctx, results)` | Skips already-executed steps |
| `WithPreResolvedParams(ctx, params, stepID)` | Uses approved parameter values |
| `WithRequestMode(ctx, mode)` | Sets streaming/non-streaming for expiry behavior |

*Metadata & Tracing Helpers* ([orchestrator.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/orchestrator.go))

| Helper | Purpose |
|--------|---------|
| `WithRequestID(ctx, requestID)` | Sets request ID for trace correlation |
| `GetRequestID(ctx)` | Retrieves request ID from context |
| `WithMetadata(ctx, metadata)` | Attaches application user context such as `session_id` and `user_id`; canonical multi-turn identity uses `conversation_id` |
| `GetMetadata(ctx)` | Retrieves metadata from context |

*Error Handling Helpers* ([hitl_errors.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_errors.go))

| Helper | Purpose |
|--------|---------|
| `IsInterrupted(err)` | Checks if error is a HITL pause |
| `GetCheckpoint(err)` | Extracts full checkpoint from interrupt error |
| `GetCheckpointID(err)` | Extracts just the checkpoint ID from interrupt error |
| `IsCheckpointNotFound(err)` | Checks if checkpoint doesn't exist or expired from storage |
| `IsCheckpointExpired(err)` | Checks if checkpoint decision timeout has passed |
| `IsInvalidCommand(err)` | Checks if command type is invalid for checkpoint state |
| `IsHITLDisabled(err)` | Checks if HITL is disabled in configuration |

*Status Checking Helpers* ([hitl_helpers.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_helpers.go))

| Helper | Purpose |
|--------|---------|
| `IsResumableStatus(status)` | Returns true for: approved, expired_approved |
| `IsTerminalStatus(status)` | Returns true for: continued, completed, rejected, aborted, expired, expired_rejected, expired_aborted |
| `IsPendingStatus(status)` | Returns true for: pending (awaiting human response) |

*Key Types & Constants* ([hitl_interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_interfaces.go))

| Type | Values | Purpose |
|------|--------|---------|
| `CheckpointStatus` | `preparing`, `pending`, `approved`, `rejected`, `edited` (reserved), `resuming`, `continued`, `completed`, `aborted`, `expired`, `expired_approved`, `expired_rejected`, `expired_aborted` | Checkpoint lifecycle states; `preparing` is framework-owned and transient |
| `InterruptPoint` | `plan_generated`, `before_step`, `on_error` (implemented); `after_step`, `context_gathering` (reserved) | Where HITL can pause |
| `RequestMode` | `streaming`, `non_streaming` | Determines expiry behavior |
| `CommandType` | Default controller: `approve`, `reject`, `abort`; other declared values are unsupported | Human decision types |

### POST /hitl/command (Framework)

Submit an approval decision.

**Request:**
```bash
curl -X POST http://localhost:8352/hitl/command \
  -H "Content-Type: application/json" \
  -d '{"checkpoint_id": "cp-dff21578-e9e1-40", "type": "approve"}'
```

**Command Types:**

| Type | Description |
|------|-------------|
| `approve` | Proceed with the plan/step as-is |
| `reject` | Stop execution |
| `abort` | Stop entire workflow immediately |
| `edit`, `skip`, `retry`, `respond` | Unsupported; HTTP 400 before any state change |

> **Note:** Only approve/reject/abort are implemented by the default controller. See [Supported default commands](../reference/API_REFERENCE.md#supported-default-commands).

**Response:**
```json
{
  "checkpoint_id": "cp-dff21578-e9e1-40",
  "action": "approve",
  "should_resume": true
}
```

### POST /hitl/resume/`{id}` (Agent)

Resume execution after approval. Returns SSE stream. See [Implementing Resume Handlers](#implementing-resume-handlers-agent-specific) above for the framework APIs to use.

**Example implementation:** `handleResumeSSE` in [handlers.go](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/handlers.go).

```go
func (t *HITLChatAgent) handleResumeSSE(w http.ResponseWriter, r *http.Request)
```

**Request:**
```bash
curl -X POST http://localhost:8352/hitl/resume/cp-dff21578-e9e1-40 \
  -H "Accept: text/event-stream"
```

Stored checkpoint lineage supplies trace correlation; the resume route does not use a client header override.

### POST /hitl/resume-sync/`{id}` (Agent)

Resume execution after approval. Returns JSON response (for non-streaming clients). See [Implementing Resume Handlers](#implementing-resume-handlers-agent-specific) above for the framework APIs to use.

**Example implementation:** `handleResumeSyncJSON` in [handlers.go](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/handlers.go).

```go
func (t *HITLChatAgent) handleResumeSyncJSON(w http.ResponseWriter, r *http.Request)
```

**Request:**
```bash
curl -X POST http://localhost:8352/hitl/resume-sync/cp-6b38d8c6-d2b4-46
```

**Response (Completed):**
```json
{
  "request_id": "orch-1769372362628519713",
  "session_id": "aca6d5f6-fc8d-4724-a2d6-d4a58d2fd9cf",
  "response": "To determine if selling 100 Tesla shares will fund your trip...",
  "tools_used": ["geocoding-tool", "currency-tool", "weather-tool-v2", "stock-service"],
  "confidence": 0.95,
  "interrupted": false,
  "duration_ms": 10740
}
```

**Response (Chained Checkpoint):**

If there are more sensitive steps, the response may contain another checkpoint:

```json
{
  "request_id": "orch-1769372352581198666",
  "session_id": "aca6d5f6-fc8d-4724-a2d6-d4a58d2fd9cf",
  "interrupted": true,
  "checkpoint": {
    "checkpoint_id": "cp-6b38d8c6-d2b4-46",
    "interrupt_point": "before_step",
    "current_step": {
      "step_id": "step-1",
      "agent_name": "stock-service",
      "instruction": "Get the current stock quote for TESLA"
    },
    "resolved_parameters": {
      "symbol": "TSLA"
    },
    "status": "pending"
  },
  "duration_ms": 966
}
```

When `interrupted: true`, repeat the approve-resume cycle with the new checkpoint ID.

### GET /hitl/checkpoints (Framework)

List pending checkpoints. Useful for building an admin dashboard. Provided by `hitlHandler.HandleListCheckpoints`.

**Request:**
```bash
curl http://localhost:8352/hitl/checkpoints
```

**Response:**
```json
{
  "checkpoints": [
    {
      "checkpoint_id": "cp-996d6126-173d-4f",
      "interrupt_point": "plan_generated",
      "decision": {
        "reason": "plan_approval",
        "message": "Plan approval required for request: What is the current price of AAPL stock?"
      },
      "plan": {
        "plan_id": "plan-001",
        "steps": [
          {"step_id": "step-1", "agent_name": "stock-service", "instruction": "Get the current stock quote for AAPL."}
        ]
      },
      "status": "pending",
      "expires_at": "2026-01-25T20:20:48.831795628Z"
    }
  ],
  "count": 1,
  "limit": 50,
  "offset": 0
}
```

**Query Parameters:** `status` (pending/approved/rejected/expired/completed), `limit`, `offset`

### GET /hitl/checkpoints/`{id}` (Framework)

Get full checkpoint details including plan, steps, and resolved parameters. Provided by `hitlHandler.HandleGetCheckpoint`.

---

## Resume Recipes

Choose the transport that fits your application. These recipes share the
coordinator from [Quick Start](#adding-hitl-to-your-agent); they differ in how
the client submits work and receives the final outcome.

### Recipe: JSON resume

Choose this path when a client can wait for one HTTP response. The shared setup
already registers `POST /hitl/resume/{id}` using `HITLHandler.HandleResume`.
It blocks while the coordinator runs the approved request and saves the result.
It does **not** submit a background job. The coordinator's default adapter in
the quick start returns `ExecutionResult`, so the successful JSON body contains
execution fields, not the chat example's `SyncResponse` envelope.

Your initial request handler must return the first interruption to the client.
For an existing handler with `request` and `metadata` already decoded, the
essential processing block is:

```go
ctx := orchestration.WithRequestMode(r.Context(), orchestration.RequestModeNonStreaming)
response, err := orch.ProcessRequest(ctx, request, metadata)
if orchestration.IsInterrupted(err) {
	checkpoint := orchestration.CheckpointResponseFrom(orchestration.GetCheckpoint(err))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	return json.NewEncoder(w).Encode(orchestration.ResumeInterruptedResponse{
		Interrupted: true, Checkpoint: checkpoint,
	})
}
if err != nil {
	return err // Your HTTP adapter must map this to an error response.
}
w.Header().Set("Content-Type", "application/json")
return json.NewEncoder(w).Encode(response)
```

This is a block inside an error-returning application handler, not a complete
`http.HandlerFunc`. Use your normal decoding, authentication, authorization,
logging, and HTTP error wrapper around it. Authorize both checkpoint reads and
commands/resume for the current caller; knowing a checkpoint ID is not permission.

After the initial response provides `checkpoint.checkpoint_id`, approve and
resume. `AGENT_URL` must address the same logical agent for all three requests;
replicas share its durable state. Replace the checkpoint placeholder with the
ID you actually received:

```bash
AGENT_URL=http://your-agent.localhost
CHECKPOINT_ID=cp-replace-with-returned-id

curl --fail-with-body -sS "$AGENT_URL/hitl/checkpoints/$CHECKPOINT_ID"
curl --fail-with-body -sS -X POST "$AGENT_URL/hitl/command" \
  -H 'Content-Type: application/json' \
  -d "{\"checkpoint_id\":\"$CHECKPOINT_ID\",\"type\":\"approve\"}"
curl -i -X POST "$AGENT_URL/hitl/resume/$CHECKPOINT_ID"
```

The last URL is the **generic JSON route** in the quick-start wiring. The
shipped `agent-with-human-approval` instead installs an SSE adapter there: when
running that example, use `/hitl/resume-sync/$CHECKPOINT_ID` for JSON. Do not
choose the response parser only from the URL name; check which handler you
registered. The [non-streaming walkthrough](#non-streaming-api) shows that
example's application-specific response envelope.

| Response from generic JSON resume | What the client should do |
|---|---|
| 200, successful `ExecutionResult` | Display completion; the parent checkpoint is `completed`. |
| 202, `interrupted: true` | Display the returned successor for a new decision; the parent is `continued`. |
| 409, `resume_in_progress` | Another attempt owns it. Reload state; do not start another operation. |
| 409, `resume_not_resumable` | Inspect its current status: it may be pending, rejected, or already terminal. |
| 409, `resume_claim_lost`, or 500, `resume_failed` | Work may have happened. Inspect checkpoint, execution, and tool evidence before deciding whether replay is safe. |

The default handler already performs this mapping. If you need a custom JSON
envelope, wrap a request-local processing adapter with `ResumeWithExecutor` as
in [hitl_resume.go](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/hitl_resume.go).
Keep its real execution result and error; do not construct `Success: true`
because the application produced text.

### Recipe: SSE resume

Choose SSE when the client needs progress or model output while work runs.
Use the **same coordinator**, with a request-local streaming adapter. Never
replace a shared executor field for one request, and never call
`BuildResumeContext` yourself inside the adapter.

This complete handler uses a small SSE protocol: `chunk` carries
`core.StreamChunk`; `done`, `checkpoint`, and `error` are terminal events.
It is not the chat example's richer session/status protocol. Put it in
`resume_sse.go` in your application:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

func ResumeSSE(
	coordinator *orchestration.ResumeCoordinator,
	orch *orchestration.AIOrchestrator,
	logger core.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deliveryError := func(err error) {
			if err != nil {
				logger.WarnWithContext(r.Context(), "Resume response delivery failed", map[string]interface{}{
					"operation": "hitl_sse_delivery", "error": err.Error(),
				})
			}
		}
		writeError := func(status int, code, message string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			deliveryError(json.NewEncoder(w).Encode(map[string]string{"code": code, "error": message}))
		}
		if r.Method != http.MethodPost {
			writeError(http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
			return
		}
		if _, ok := w.(http.Flusher); !ok {
			writeError(http.StatusInternalServerError, "stream_unavailable", "SSE is not supported")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/hitl/resume/")
		if id == r.URL.Path || id == "" || strings.Contains(id, "/") {
			writeError(http.StatusBadRequest, "invalid_resume_request", "checkpoint ID is required")
			return
		}

		var mu sync.Mutex
		started := false
		emit := func(event string, value interface{}) error {
			mu.Lock()
			defer mu.Unlock()
			if err := r.Context().Err(); err != nil {
				return err
			}
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if !started {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("X-Accel-Buffering", "no")
				started = true
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
				return err
			}
			return http.NewResponseController(w).Flush()
		}
		result, err := coordinator.ResumeWithExecutor(r.Context(), id,
			orchestration.ResumeExecutorFunc(func(ctx context.Context, cp *orchestration.ExecutionCheckpoint) (*orchestration.ExecutionResult, error) {
				_, execution, err := orch.ProcessRequestStreamingWithExecution(
					ctx, cp.OriginalRequest, cp.UserContext,
					func(chunk core.StreamChunk) error { return emit("chunk", chunk) },
				)
				return execution, err
			}),
		)
		// A lifecycle error can wrap an interruption: check it first.
		var lifecycle *orchestration.ErrCheckpointResumeLifecycle
		if err != nil && !errors.As(err, &lifecycle) && orchestration.IsInterrupted(err) {
			deliveryError(emit("checkpoint", orchestration.CheckpointResponseFrom(orchestration.GetCheckpoint(err))))
			return
		}
		if err != nil {
			status, code, message := orchestration.HITLErrorResponse(err)
			mu.Lock()
			streamStarted := started
			mu.Unlock()
			if !streamStarted {
				writeError(status, code, message)
			} else {
				deliveryError(emit("error", map[string]interface{}{
					"code": code, "error": message, "retryable": false,
				}))
			}
			return
		}
		// Only the coordinator can establish durable completion.
		deliveryError(emit("done", map[string]interface{}{"execution": result}))
	}
}
```

In startup, choose this **instead of** registering the generic JSON resume
route. Call the shared application registration helper only once:

```go
RegisterHITLRoutes(baseAgent, hitl.Handler, ResumeSSE(hitl.Coordinator, orch, logger))
```

For your own `net/http` server, register command/query handler methods
individually and `mux.HandleFunc("/hitl/resume/", ResumeSSE(hitl.Coordinator, orch, logger))`.
Do not also call `hitl.Handler.RegisterRoutes(mux)`: that would register a second
resume handler at the same route.

Set the initial streaming request's mode to `RequestModeStreaming` before
processing it; resume restores the stored mode. Retain your host's CORS setup
(`core.WithCORSDefaults()` for browser-facing examples), authentication, request
tracing, and write timeouts. A production stream may also need heartbeats and
proxy timeout configuration; this small handler focuses on correct resume
ordering, not a complete browser application.

Try an **approved** checkpoint with:

```bash
curl -N -i -X POST "$AGENT_URL/hitl/resume/$CHECKPOINT_ID" \
  -H 'Accept: text/event-stream'
```

The important order is:

```text
claim checkpoint -> stream chunks -> finish execution -> persist final status
                                                        |
                           send done/checkpoint/error <--+
```

No `done` or `checkpoint` is emitted inside the processing adapter. Even a
chunk with `finish_reason` only describes the model stream, not the durable
business result. A recovered successor may produce `checkpoint` without any
chunks. Before stream headers, rejections are ordinary JSON HTTP errors; after
headers, errors are SSE events. The client must handle both. If it receives no
terminal event, treat the outcome as unknown and reload state rather than
automatically repeating the operation.

### Recipe: queued resume

Choose a queue when the submitting HTTP request should end before work runs.
There are two separate results: accepting a task, and completing the approved
workflow. HTTP 202 from **task admission** means only "queued". It is different
from HTTP 202 with an **interruption checkpoint** from a synchronous resume.

Use the existing task system; do not create an untracked goroutine in an HTTP
handler. The API process persists a task and enqueues its ID/input. The worker
claims the checkpoint only when it begins processing that task:

```text
HTTP: authorize -> persist task -> enqueue -> return task ID (202)
worker: dequeue -> coordinator claim -> execute -> persist outcome -> save task result
```

Here is the worker-side handler in `resume_task.go`. It receives the same
coordinator as JSON/SSE. The worker pool supplies `ctx`, independently of the
submitting connection:

```go
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

func ResumeTaskHandler(coordinator *orchestration.ResumeCoordinator) core.TaskHandler {
	return func(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
		id, _ := task.Input["checkpoint_id"].(string)
		if id == "" {
			return &orchestration.ErrInvalidResumeRequest{Field: "checkpoint_id"}
		}
		execution, err := coordinator.ResumeExecution(ctx, id)
		var lifecycle *orchestration.ErrCheckpointResumeLifecycle
		if err != nil && !errors.As(err, &lifecycle) && orchestration.IsInterrupted(err) {
			child := orchestration.CheckpointResponseFrom(orchestration.GetCheckpoint(err))
			task.Result = map[string]interface{}{
				"status": "pending_approval", "checkpoint": child, "resumed_from": id,
			}
			// This task finished its attempt; the business workflow is not complete.
			return nil
		}
		if err != nil {
			_, code, _ := orchestration.HITLErrorResponse(err)
			task.Result = map[string]interface{}{
				"status": "failed", "code": code, "retryable": false,
			}
			return fmt.Errorf("resume task failed: %w", err)
		}
		task.Result = map[string]interface{}{"status": "completed", "execution": execution}
		return nil
	}
}
```

Register it before starting the worker pool:

```go
if err := workerPool.RegisterHandler("hitl_resume", ResumeTaskHandler(hitl.Coordinator)); err != nil {
	return err
}
```

The input is `{"checkpoint_id":"cp-..."}`. Keep task-consumer tracing in your
normal worker adapter; the coordinator creates the separate linked
`hitl.resume` span. Do not add a second resume span or restore the original
checkpoint context manually.

**Retry configuration matters:** `retryable: false` above is result data for a
client, not a setting interpreted by every queue. Disable automatic processing
retries for this task type in your worker/transport policy, or explicitly route
failures for review. Claims prevent concurrent owners; they do not make tool
side effects exactly-once after a crash or lease loss. Reconcile a duplicate
delivery that finds `completed`/`continued` from durable evidence, rather than
calling it a new successful execution.

For the complete admission and worker wiring, see
[hitl_task_admission.go](https://github.com/truvaagents/truva-g3/blob/main/examples/event-driven-agent/hitl_task_admission.go),
[event_processor.go](https://github.com/truvaagents/truva-g3/blob/main/examples/event-driven-agent/event_processor.go),
and [main.go](https://github.com/truvaagents/truva-g3/blob/main/examples/event-driven-agent/main.go).
The [Async Orchestration Guide](ASYNC_ORCHESTRATION_GUIDE.md) covers creating the
queue, worker pool, polling routes, and task storage. If your application has
session preparation or response mapping, put that in the supplied executor as
the examples do; return its actual result-bearing processing outcome.

---

## Configuration

### Core Settings

| Variable | Default | What It Does |
|----------|---------|--------------|
| `TRUVAG3_HITL_ENABLED` | `false` | Master switch for HITL |
| `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | `false` | Pause after every plan generation |
| `TRUVAG3_HITL_SENSITIVE_CAPABILITIES` | `""` | Comma-separated capabilities needing plan + step approval |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | `""` | Comma-separated capabilities needing step-only approval |
| `TRUVAG3_HITL_SENSITIVE_AGENTS` | `""` | Comma-separated agents needing plan + step approval |
| `TRUVAG3_HITL_STEP_SENSITIVE_AGENTS` | `""` | Comma-separated agents needing step-only approval |
| `TRUVAG3_HITL_DEFAULT_TIMEOUT` | `5m` | How long to wait for human response |
| `TRUVAG3_HITL_ESCALATE_AFTER_RETRIES` | `3` | Escalate to human after N failures |

### Understanding Timeouts

HITL has two different timeout concepts - this trips people up, so pay attention:

**Decision Timeout** (default: 5 minutes)
- How long the human has to respond
- Shown to the user as the countdown in the approval dialog
- After expiry, the default action is taken

**Storage TTL** (default: 24 hours)
- How long checkpoint data stays in Redis
- Allows resuming interrupted sessions
- Provides audit trail

The decision timeout is what the user sees. The storage TTL is how long the data persists (for resume and auditing).

### Expiry Behavior

When a checkpoint times out (decision timeout expires), what happens?

| Variable | Default | What It Does |
|----------|---------|--------------|
| `TRUVAG3_HITL_DEFAULT_ACTION` | `reject` | Action to take on timeout |
| `TRUVAG3_HITL_STREAMING_EXPIRY` | `implicit_deny` | Behavior for streaming requests |
| `TRUVAG3_HITL_NON_STREAMING_EXPIRY` | `apply_default` | Behavior for non-streaming requests |

**Expiry behaviors explained:**

- `implicit_deny`: Just set status to "expired", don't take any action. The checkpoint sits there until someone manually handles it.
- `apply_default`: Apply the `DEFAULT_ACTION` automatically. If `DEFAULT_ACTION=reject`, the request is auto-rejected.

**Why the defaults?**

- **Streaming + implicit_deny**: User was watching the dialog (SSE connection was open). They saw it but didn't respond. Maybe they walked away, maybe they're thinking. Don't take action on their behalf.
- **Non-streaming + apply_default**: User submitted and closed the browser. They're not watching. Apply the policy automatically.

### Storage Settings

| Variable | Default | What It Does |
|----------|---------|--------------|
| `TRUVAG3_REDIS_NAMESPACE` | `default` | Deployment namespace used by the canonical versioned DB 0 HITL keyspace |
| `TRUVAG3_AGENT_NAME` | `""` | Agent name appended to the base prefix for multi-agent isolation |
| `TRUVAG3_K8S_SERVICE_NAME` | `""` | K8s Service name; used as `TRUVAG3_AGENT_NAME` fallback when unset |

**The agent identity is structural, not optional.** Canonical provider
composition derives the prefix with
`keyspace.Tagged("hitl", agentScope)`. For deployment `production` and agent
`my-agent`, the pending index is
`truvag3:v1:production:hitl:{production:hitl:my-agent}:pending`. Omitting the
agent scope shares the deployment-wide HITL slot and pending index with every
other unscoped agent using that namespace.

When the framework detects the shared-prefix configuration at construction, it emits a startup Warn:

```
HITL checkpoint store using shared key prefix — set TRUVAG3_AGENT_NAME
(or TRUVAG3_K8S_SERVICE_NAME) to isolate per-agent state
```

The warning suppresses when isolation has been supplied through
`TRUVAG3_AGENT_NAME`, `TRUVAG3_K8S_SERVICE_NAME`, or an explicit
`WithCheckpointKeyspace` option. K8s deployments using the
framework's standard manifests already set the service identity; for local
development with more than one HITL-enabled agent, set `TRUVAG3_AGENT_NAME`.

### Example Configurations

**Plan approval only (review before any execution):**
```bash
TRUVAG3_HITL_ENABLED=true
TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true
```

**Step-only approval (let AI work, pause at risky operations):**
```bash
TRUVAG3_HITL_ENABLED=true
TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=transfer_funds,delete_account
```

**Full HITL for financial operations:**
```bash
TRUVAG3_HITL_ENABLED=true
TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true
TRUVAG3_HITL_SENSITIVE_CAPABILITIES=stock_quote,execute_trade
TRUVAG3_HITL_DEFAULT_TIMEOUT=10m  # Longer review window
```

**Auto-approve for internal tooling (dev environment):**
```bash
TRUVAG3_HITL_ENABLED=true
TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true
TRUVAG3_HITL_DEFAULT_ACTION=approve
TRUVAG3_HITL_STREAMING_EXPIRY=apply_default
```

See [ENVIRONMENT_VARIABLES_GUIDE.md](../reference/ENVIRONMENT_VARIABLES_GUIDE.md) for the complete reference.

---

## Expiry Processor Setup

The expiry processor is a provider-neutral `core.Runnable` that handles checkpoint timeouts. **Without a registered processor, checkpoints will never expire and auto-resume won't work.** The application owns its lifecycle through `Framework.RegisterRunnable`; the checkpoint store only persists checkpoints and exposes atomic expiry claims.

### Why You Need It

When a human doesn't respond within `DEFAULT_TIMEOUT`:
1. The expiry processor detects the expired checkpoint
2. It updates the status based on your expiry configuration
3. It calls your callback so you can take action (e.g., notify the user, auto-resume)

### Setting Up the Expiry Processor

```go
// 1. Validate the processor's inputs before constructing it.
// BackendFeatureCheckpointExpiry also requires a composed processor, so do
// not require that complete feature before this manual construction step.
requirements, err := orchestration.NewBackendRequirements(
    orchestration.BackendCheckpoints,
    orchestration.BackendCheckpointExpiry,
)
if err != nil {
    return err
}
if err := backends.ValidateFor(requirements); err != nil {
    return err
}

// 2. Define expiry behavior independently of the storage provider.
callback := func(ctx context.Context, cp *orchestration.ExecutionCheckpoint, action orchestration.CommandType) {
    // action is "" for implicit_deny, or "approve"/"reject"/"abort" for apply_default

    if action == "" {
        // Streaming request expired with implicit_deny
        // Status is now "expired" - frontend can detect via polling
        logger.Info("Checkpoint expired (implicit deny)", map[string]interface{}{
            "checkpoint_id": cp.CheckpointID,
            "request_mode":  cp.RequestMode,
        })
        return
    }

    // Non-streaming or apply_default configured
    // Status is now "expired_approved", "expired_rejected", or "expired_aborted"
    logger.Info("Checkpoint expired with action", map[string]interface{}{
        "checkpoint_id": cp.CheckpointID,
        "action":        action,
        "status":        cp.Status,
    })

    // This is a notification, not permission to bypass durable resume ownership.
    if action == orchestration.CommandApprove {
        // Schedule a later check of durable expired_approved status.
        // at_least_once delivery may run this callback before the status CAS.
        // Do not call the coordinator with this notification snapshot.
        // See Auto-Resume section below
    }
}

// 3. Create the processor from provider-neutral contracts.
runtimeConfig, err := orchestration.LoadCheckpointExpiryRuntimeConfigFromEnvironment(
    orchestration.DefaultCheckpointExpiryRuntimeConfig(),
    os.LookupEnv,
)
if err != nil {
    return err
}
processorOptions := []orchestration.CheckpointExpiryProcessorOption{
    orchestration.WithCheckpointExpiryRuntimeConfig(runtimeConfig),
    orchestration.WithCheckpointExpiryLogger(logger),
    orchestration.WithCheckpointExpiryTelemetry(telemetryProvider),
}
if owner := os.Getenv("HOSTNAME"); owner != "" {
    // Kubernetes supplies the pod name; this makes claim diagnostics readable.
    processorOptions = append(processorOptions, orchestration.WithCheckpointExpiryOwner(owner))
}
processor, err := orchestration.NewCheckpointExpiryProcessor(
    backends.Checkpoints(),
    backends.CheckpointExpiry(),
    callback,
    orchestration.ExpiryProcessorConfigFromEnv(),
    processorOptions...,
)
if err != nil {
    return err
}

// 4. Register before Framework.Run(); framework shutdown cancels it.
framework.RegisterRunnable(processor)
```

`backends` may come from any provider. The included Redis preset can create the
checkpoint adapters and processor together with
`redisprovider.WithCheckpointExpiry(...)`; register every value returned by
`backends.Runnables()`. The older `RedisCheckpointStore.SetExpiryCallback`,
`StartExpiryProcessor`, and `StopExpiryProcessor` methods remain compatibility
APIs, but new application wiring should use the lifecycle-owned processor above.

### Expiry Processor Configuration

| Field | Default | Description |
|-------|---------|-------------|
| `Enabled` | `true` | Set to `false` to disable automatic expiry processing |
| `ScanInterval` | `10s` | How often to scan Redis for expired checkpoints |
| `BatchSize` | `100` | Maximum checkpoints processed per scan cycle |
| `DeliverySemantics` | `at_most_once` | `at_most_once` (no retry, safer) or `at_least_once` (may retry, callback must be idempotent) |

Environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_HITL_EXPIRY_ENABLED` | `true` | Enable/disable the processor |
| `TRUVAG3_HITL_EXPIRY_INTERVAL` | `10s` | Scan interval |
| `TRUVAG3_HITL_EXPIRY_BATCH_SIZE` | `100` | Max checkpoints per scan |
| `TRUVAG3_HITL_EXPIRY_DELIVERY` | `at_most_once` | Delivery semantics |
| `TRUVAG3_HITL_EXPIRY_CLAIM_LEASE` | `30s` | Lease held by one processor while it handles a claimed checkpoint |

### Graceful Shutdown

Register the processor before `Framework.Run()`. Framework shutdown cancels and
drains registered runnables before the application closes injected backend
clients:

```go
// processor was registered once during setup, before Run.
if err := framework.Run(ctx); err != nil {
    return err
}

// The Redis preset does not own an injected client. Close it after Run returns.
if err := redisClient.Close(); err != nil {
    logger.Warn("Failed to close Redis client", map[string]interface{}{
        "operation":  "redis_close",
        "error_type": "backend_close",
        "error":      core.RedactSensitiveText(err.Error()),
    })
}
```

The current
[hitl_setup.go](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/hitl_setup.go)
is a runnable compatibility example. It uses the older store-owned lifecycle;
the composition and shutdown sequence above are the preferred framework path.

---

## Troubleshooting

### Checkpoint Not Found After Approval

**What you see:** Resume returns 404 even though approval succeeded.

**Why it happens:** Either the storage TTL expired (default 24 hours), or there's a Redis connection issue.

**How to fix:**
- Check if too much time passed since the checkpoint was created
- Verify Redis connectivity: `redis-cli ping`
- For long-running workflows, increase storage TTL programmatically

### Steps Re-executing After Resume

Use the coordinator and the same result-bearing processing path. Confirm the
checkpoint contains the expected plan, completed results, and resolved
parameters. An application calling only `WithResumeMode` or
`BuildResumeContext` bypasses ownership and is not a valid resume route.

Resume fencing prevents stale checkpoint mutations; it does not make external
tools exactly-once. If a process died after a tool acted but before progress
was saved, inspect tool evidence and use application idempotency before replay.

### Trace Correlation Not Working

Check the stored original request/trace/span fields and the single
`hitl.resume` span. The coordinator restores these fields; callers need not
send an original-request header. A missing original span produces an unlinked
resume span, not an invented trace relationship.

### Approval Dialog Not Appearing

**What you see:** Execution seems to pause, but no dialog shows.

**Why it happens:** The frontend isn't handling the `checkpoint` event.

**How to fix:** Verify your SSE handler processes it:
```javascript
case 'checkpoint':
    showApprovalDialog(data);
    break;
```

### Redis Connection Refused

**What you see:** HITL setup fails with "connection refused".

**How to fix:**
```bash
# Start Redis locally
docker run -p 6379:6379 redis:alpine

# Or on Mac with Homebrew
brew services start redis

# Verify it's running
redis-cli ping
```

### Auto-Resume Not Working

**What you see:** Checkpoints expire but nothing happens, even with `DEFAULT_ACTION=approve`.

**Why it happens:** The expiry processor isn't running, or the callback isn't set.

**How to fix:**
1. Verify the expiry processor is registered before `Framework.Run()`:
```go
framework.RegisterRunnable(expiryProcessor)
```

2. Verify the callback was passed when constructing the processor:
```go
expiryProcessor, err := orchestration.NewCheckpointExpiryProcessor(
    backends.Checkpoints(), backends.CheckpointExpiry(), callback, config,
)
```

3. Check logs for `hitl_expiry_processor_start` to confirm it's running.

### Duplicate Checkpoint Processing in Multi-Pod

**What you see:** The same checkpoint is processed by multiple pods, causing duplicate actions.

**Why it happens:** Replicas were given the same claim owner, the expiry source
does not implement atomic leased claims correctly, or the backend is unavailable.

**How to fix:**
```go
processor, err := orchestration.NewCheckpointExpiryProcessor(
    backends.Checkpoints(),
    backends.CheckpointExpiry(),
    callback,
    config,
    orchestration.WithCheckpointExpiryOwner(os.Getenv("HOSTNAME")),
)
```

### Shared-prefix duplicates at the aggregation boundary

**What happens:** Multiple unscoped agents that share one deployment HITL
pending index can each
return the same checkpoint to a fan-out caller. The current Registry Viewer
deduplicates its merged list by `checkpoint_id`, so this condition should not
create duplicate rows in the UI.

This remains a configuration defect even when the UI hides its visual symptom:
checkpoint ownership and command routing are ambiguous, and another management
client may not deduplicate.

**Diagnostic:** Inspect the Viewer API response and agent configuration:

```bash
curl -s http://<viewer>/api/hitl/checkpoints | jq '.checkpoints | length, (map(.checkpoint_id) | unique | length)'
```

For the current Viewer, total and distinct should match. If they do not, the
deployed Viewer is older than this implementation or its deduplication has
regressed. Matching counts do not prove correct isolation; confirm every
logical agent has its own resolved prefix.

**How to fix:** Give each agent a distinct identity so their Redis prefixes don't collide. Any of these works:

```bash
# Most common — set the env var
TRUVAG3_AGENT_NAME=my-agent

# K8s standard — set via Service name (falls back if TRUVAG3_AGENT_NAME is unset)
TRUVAG3_K8S_SERVICE_NAME=my-agent

# Keep every collaborating process in the same deployment keyspace
TRUVAG3_REDIS_NAMESPACE=production
```

Or pass the same typed deployment keyspace and agent scope to both HITL stores:

```go
keyspace, _ := core.NewRedisKeyspace("production")
checkpointStore, _ := orchestration.NewRedisCheckpointStoreWithClient(
    redisClient,
    orchestration.WithCheckpointKeyspace(keyspace, "my-agent"),
)
commandStore, _ := orchestration.NewRedisCommandStoreWithClient(
    redisClient,
    orchestration.WithCommandStoreKeyspace(keyspace, "my-agent"),
)
```

The framework logs a startup Warn pointing at missing identity; check agent
logs for "using shared key prefix" even when the Viewer shows only one
deduplicated row.

See [Agent Isolation](#agent-isolation) for the full mechanism and why each path works.

### Checkpoint Expires Immediately

**What you see:** Checkpoints expire within seconds instead of the configured timeout.

**Why it happens:** Mismatch between `DEFAULT_TIMEOUT` format and actual value.

**How to fix:**
- Ensure the format is correct: `5m` (5 minutes), not `5` or `300`
- Check logs for the actual `expires_at` value when checkpoint is created

### Request Mode Warning in Logs

**What you see:** `"HITL: request_mode not set, using default"` warning.

**Why it happens:** Your handler isn't setting the request mode.

**How to fix:**
```go
// In SSE handlers:
ctx = orchestration.WithRequestMode(ctx, orchestration.RequestModeStreaming)

// In JSON/async handlers:
ctx = orchestration.WithRequestMode(ctx, orchestration.RequestModeNonStreaming)
```

### HITL Not Triggering (No Checkpoints Created)

**What you see:** Requests complete without pausing, even with sensitive capabilities configured.

**Why it happens:** Policy isn't matching your request.

**How to fix:**
1. Verify HITL is enabled: `TRUVAG3_HITL_ENABLED=true`
2. Check capability names match exactly (case-sensitive):
   - Your tool registers: `stock_quote`
   - Config must use: `TRUVAG3_HITL_SENSITIVE_CAPABILITIES=stock_quote`
3. For plan approval, verify: `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true`
4. Add debug logging to see policy decisions

---

## Auto-Resume (Timeout Auto-Approval)

By default, when a streaming checkpoint expires, nothing happens (implicit deny). But you can configure auto-resume - the system automatically continues execution when the timeout hits.

**When would you want this?**

- Internal tooling where you trust the AI
- Development/testing environments
- Low-risk operations where speed matters more than oversight

### Configuration

```bash
TRUVAG3_HITL_DEFAULT_ACTION=approve           # Auto-approve on timeout
TRUVAG3_HITL_STREAMING_EXPIRY=apply_default   # Apply the default action
```

### How It Works

1. Checkpoint expires without human response
2. Expiry processor sets status to `expired_approved`
3. Frontend detects the status change (via polling or callback)
4. Frontend calls `/hitl/auto-resume/{id}/stream`
5. Execution continues via new SSE stream

The frontend is responsible for detecting the expiry and initiating the resume. The backend just updates the status.

### Frontend Detection

```javascript
// Poll for status changes
async function checkForAutoResume(checkpointId) {
    const response = await fetch(`/hitl/checkpoints/${checkpointId}`);
    const checkpoint = await response.json();

    if (checkpoint.status === 'expired_approved') {
        // Auto-approved! Resume execution
        // The shipped chat example's timeout-resume route uses GET.
        const resume = await fetch(`/hitl/auto-resume/${checkpointId}/stream`, {
            headers: {Accept: 'text/event-stream'}
        });
        if (!resume.ok) {
            const error = await resume.json();
            throw new Error(error.error || error.code);
        }
        await processSSEStream(resume); // Consume through done/checkpoint/error.
    }
}
```

`processSSEStream` is your frontend's SSE parser, not a framework JavaScript
function. See the linked `hitl.html` for a complete client. The fetch completing
only means response headers arrived; consume the stream to determine the
outcome. Prevent overlapping polls from starting multiple resume requests.

This GET route is specific to the shipped chat UI. A new application can use
the normal POST coordinator resume route for `expired_approved` too. The
auto-resume handler uses the same ownership APIs as regular resume handlers.
See [Implementing Resume Handlers](#implementing-resume-handlers-agent-specific).

**Example implementation:** [handlers_auto_resume.go](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/handlers_auto_resume.go).

```go
func (t *HITLChatAgent) handleAutoResumeSSE(w http.ResponseWriter, r *http.Request)
```

---

## Registry Viewer: Monitoring HITL Checkpoints

The Registry Viewer app provides a web UI for monitoring and debugging HITL checkpoints. It shows pending checkpoints, their status, and allows inspection of checkpoint details.

**Location:** [`examples/registry-viewer-app/`](https://github.com/truvaagents/truva-g3/tree/main/examples/registry-viewer-app)

### Current Capabilities

| Feature | Description |
|---------|-------------|
| **List pending checkpoints** | Shows all checkpoints awaiting human approval |
| **View checkpoint details** | Full plan, steps, resolved parameters, decision info |
| **Status badges** | Visual indicators for priority, reason, and default action |
| **Agent filtering** | Multi-agent support with agent name display |

### Searching for Expired Checkpoints

By default, the HITL Interrupted tab only shows **pending** checkpoints. Expired checkpoints (those that timed out) are not listed. This is intentional - the default view focuses on actionable items.

However, you may need to investigate expired checkpoints for debugging or auditing. The registry viewer supports searching for checkpoints by ID or request ID, which returns results regardless of status.

#### Search API

**Endpoint:** `GET /api/hitl/checkpoints/search?q={query}`

| Parameter | Description | Example |
|-----------|-------------|---------|
| `q` | Search query | `cp-abc123` or `orch-1769307789589489094` |

**Behavior:**

| Query Pattern | Search Strategy |
|---------------|-----------------|
| Starts with `cp-` | Exact checkpoint ID match |
| Other values | Search by request_id |

**Example:**

```bash
# Search by checkpoint ID
curl "http://localhost:8361/api/hitl/checkpoints/search?q=cp-dff21578-e9e1-40"

# Search by request ID
curl "http://localhost:8361/api/hitl/checkpoints/search?q=orch-1769307789589489094"
```

**Response:**

```json
{
  "checkpoints": [
    {
      "checkpoint_id": "cp-dff21578-e9e1-40",
      "request_id": "orch-1769307789589489094",
      "interrupt_point": "plan_generated",
      "status": "expired",
      "reason": "plan_approval",
      "created_at": "2026-01-25T20:14:45Z",
      "expires_at": "2026-01-25T20:19:45Z",
      "agent_name": "agent-with-human-approval"
    }
  ],
  "total": 1,
  "query": "orch-1769307789589489094"
}
```

#### Checkpoint Status Values

When viewing search results, checkpoints may have different statuses:

| Status | Category | Description |
|--------|----------|-------------|
| `preparing` | Internal/transient | Durable run state is still being attached; never resumable or listed as pending human work |
| `pending` | Active | Awaiting human response |
| `approved` | Human-initiated | Human clicked "Approve" |
| `rejected` | Human-initiated | Human clicked "Reject" |
| `edited` | Reserved | Not resumable; the default edit command is unsupported |
| `resuming` | Framework-owned | An attempt owns the checkpoint; recovery requires lease expiry |
| `continued` | Finished parent | Parent points to a new interruption checkpoint |
| `aborted` | Human-initiated | Human clicked "Abort" |
| `completed` | Finished | Execution completed after approval |
| `expired` | Streaming expiry | Timed out, no action applied (implicit deny) |
| `expired_approved` | Non-streaming expiry | Timed out, auto-approved per policy |
| `expired_rejected` | Non-streaming expiry | Timed out, auto-rejected per policy |
| `expired_aborted` | Non-streaming expiry | Timed out, auto-aborted per policy |

#### UI Behavior

The search feature works as follows:

1. **Default view**: Shows only pending checkpoints (fast, uses pending index)
2. **Search active**: Shows ALL matching checkpoints regardless of status
3. **Clear search**: Returns to pending-only view

Search results display status badges to distinguish between active and expired checkpoints:

| Status | Badge Color |
|--------|-------------|
| `pending` | Blue |
| `approved`, `expired_approved` | Green |
| `rejected`, `expired_rejected` | Red |
| `aborted`, `expired_aborted` | Orange |
| `expired` | Orange |
| `completed` | Gray |

#### Performance Considerations

The list endpoint reads the agent-scoped pending index. Direct checkpoint lookup
uses the checkpoint ID and does not scan the cluster keyspace.

For typical usage (dozens to hundreds of checkpoints), this is fine. The registry viewer is primarily a development and debugging tool.

**Note:** Checkpoint storage has a configurable TTL (default 24 hours). This
is not a promise of 24 hours after the decision expires. Resume transitions
preserve the record's remaining retention; very old checkpoints may be gone.

---

## Production Deployment

### Multi-Pod Considerations

When running multiple replicas of your HITL-enabled agent, you need to ensure only ONE pod processes each expired checkpoint. The framework handles this automatically using Redis-based distributed locking.

**Set a unique claim owner for each pod:**

```go
// In Kubernetes, use the pod name
instanceID := os.Getenv("HOSTNAME")  // K8s sets this to the pod name

processor, err := orchestration.NewCheckpointExpiryProcessor(
    backends.Checkpoints(),
    backends.CheckpointExpiry(),
    callback,
    expiryConfig,
    orchestration.WithCheckpointExpiryOwner(instanceID),
    orchestration.WithCheckpointExpiryLogger(logger),
)
```

If you do not set `WithCheckpointExpiryOwner`, a random UUID is generated. This
is safe, but a stable pod name makes claim diagnostics easier to correlate.

**How the claim mechanism works:**
1. Pod A finds expired checkpoint
2. Pod A atomically acquires an expiry-processing lease (default 30 seconds)
3. If successful, Pod A processes the checkpoint
4. Pod B cannot acquire that same active lease and skips the checkpoint
5. Pod A releases the claim after processing

This ensures a single active claimant for a lease. It does not promise global
exactly-once delivery: choose `at_most_once` or `at_least_once` explicitly, and
make callbacks idempotent when retries are enabled.

### Metrics for Monitoring

HITL emits Prometheus metrics you should monitor in production:

| Metric | Alert Threshold | What It Means |
|--------|-----------------|---------------|
| `orchestration.hitl.checkpoint_created_total` | - | Track checkpoint volume |
| `orchestration.hitl.checkpoint_expired_total` | High rate | Users not responding in time |
| `orchestration.hitl.approval_latency_seconds` | p99 > 4m | Approaching timeout |
| `orchestration.hitl.expiry_scan_duration_seconds` | > 5s | Redis may be slow |
| `orchestration.hitl.claim_skipped_total` | - | Normal in multi-pod; high = many pods competing |
| `orchestration.hitl.callback_panic_total` | > 0 | Bug in your expiry callback |

**Grafana dashboard queries:**

```promql
# Checkpoint creation rate
rate(orchestration_hitl_checkpoint_created_total[5m])

# Approval latency p99
histogram_quantile(0.99, rate(orchestration_hitl_approval_latency_seconds_bucket[5m]))

# Expiry rate by action
rate(orchestration_hitl_checkpoint_expired_total[5m]) by (action)
```

### Health Checks

The expiry processor runs under the framework lifecycle. Add a provider-specific
health check for the backend client where the application composes that client:

```go
func (h *HITLInfrastructure) HealthCheck() error {
    // Check Redis connectivity by attempting a read operation
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    // Try to load a non-existent checkpoint - this tests Redis connectivity
    // without side effects. ErrCheckpointNotFound is expected and OK.
    _, err := h.CheckpointStore.LoadCheckpoint(ctx, "health-check-probe")
    if err != nil && !orchestration.IsCheckpointNotFound(err) {
        return fmt.Errorf("redis connectivity check failed: %w", err)
    }
    return nil
}
```

### Agent Isolation

When multiple HITL-enabled agents share Redis/Valkey, each logical agent must
have a distinct identity while every replica of that logical agent must share
one identity. Without it, all unscoped agents in a deployment write to the same
pending-checkpoint index. The Registry Viewer's HITL list can then show each
checkpoint once per running logical agent—N agents reporting the same M
checkpoints produces N × M rows. Approvals still route correctly because the
owning agent is stamped inside the checkpoint blob, but diagnosis becomes
unusable.

#### How identity resolves

Canonical provider composition builds the Redis key prefix from two structural
inputs.

**Part 1 — the deployment namespace** comes from
`TRUVAG3_REDIS_NAMESPACE`, defaulting to `default`. It produces the outer
operational prefix `truvag3:v1:<deployment>:hitl`.

**Part 2 — the agent scope** is resolved in priority order:

1. `TRUVAG3_AGENT_NAME` (preferred — explicit agent identity)
2. `TRUVAG3_K8S_SERVICE_NAME` (fallback — K8s manifests typically set this on the Service)
3. Empty — the deployment-wide HITL scope is shared

The resulting canonical base is
`truvag3:v1:<deployment>:hitl:{<deployment>:hitl:<agent>}`. The braces are a
Redis Cluster hash tag: checkpoint, pending-index, claim, and command keys for
that agent share one slot. `WithCheckpointKeyspace` and
`WithCommandStoreKeyspace` override the resolved identity and must receive the
same `RedisKeyspace` and `agentScope`. The shared-prefix Warn fires when no agent
identity or explicit keyspace was supplied. `TRUVAG3_HITL_KEY_PREFIX` and
`TRUVAG3_HITL_REDIS_DB` are removed; selected owning Redis constructors reject
non-empty values with an actionable configuration error.

#### Configure isolation through any of these paths

```bash
# Path 1 — explicit env var (most common, K8s and local)
TRUVAG3_AGENT_NAME=trading-agent

# Path 2 — Service name fallback (K8s sets this on the Deployment manifest)
TRUVAG3_K8S_SERVICE_NAME=trading-agent

# Path 3 — deployment isolation for environments sharing one Redis/Valkey
TRUVAG3_REDIS_NAMESPACE=staging
```

```go
// Path 4 — explicit composition with an application-owned topology-aware client
keyspace, _ := core.NewRedisKeyspace("staging")
checkpointStore, _ := orchestration.NewRedisCheckpointStoreWithClient(
    redisClient,
    orchestration.WithCheckpointKeyspace(keyspace, "trading-agent"),
)
commandStore, _ := orchestration.NewRedisCommandStoreWithClient(
    redisClient,
    orchestration.WithCommandStoreKeyspace(keyspace, "trading-agent"),
)
```

The supplied client remains application-owned and is left open when either
store closes. Redis routing and DB 0 selection belong to that client.

Whichever canonical path you choose, this agent's checkpoints are stored at
`{prefix}:checkpoint:{id}` and the pending index at `{prefix}:pending`—disjoint
from every other agent and co-located for atomic HITL operations.

#### What the framework does on its own

K8s replicas of one Deployment naturally share `TRUVAG3_K8S_SERVICE_NAME`, but
`applyConfigToComponent` collapses them to a single service-discovery entry
(`base.ID = config.Name`), so the Registry Viewer hits only one replica per
logical agent. Replica sharing is intentional. Duplicate upstream checkpoint
responses can arise when multiple **logical** agents share a prefix or when
operators force instance-scoped registration via per-pod
`TRUVAG3_AGENT_ID`; the current Viewer deduplicates those responses for display
but cannot repair the underlying ownership configuration.

If isolation isn't configured, you'll see this in the agent's startup logs:

```
WARN HITL checkpoint store using shared key prefix —
     set TRUVAG3_AGENT_NAME (or TRUVAG3_K8S_SERVICE_NAME)
     to isolate per-agent state
     key_prefix=truvag3:v1:default:hitl:{default:hitl}
```

The Warn is the framework's signal that something downstream — typically the registry viewer — will misbehave. Address it at the source rather than working around the symptom.

---

## Testing HITL Flows

### Manual verification checklist

Use a read-only tool or a harmless mock. These are instructions and expected
results, not evidence that a new live test has already run.

For the shipped `agent-with-human-approval`, configure its own `.env`:

```bash
TRUVAG3_HITL_ENABLED=true
TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true
TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=stock_quote
TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true
TRUVAG3_LLM_DEBUG_ENABLED=true
```

Keep its AI key and Redis topology settings in that file. For an existing Kind
cluster, deploy the needed stock tool and agent with their `./setup.sh deploy`
commands. Use `rollout` for configuration-only updates to an existing deployment,
or `rebuild` after code changes. Run `./setup.sh help` to check available verbs.
Do not rebuild images or apply manifests by hand. Ensure Registry Viewer uses
the same deployment's Redis topology and namespace.

Start a request against the shipped agent's JSON chat endpoint:

```bash
AGENT_URL=http://agent-with-human-approval.localhost
initial=$(curl --fail-with-body -sS -X POST "$AGENT_URL/chat" \
  -H 'Content-Type: application/json' \
  -d '{"request":"Get the current AAPL stock quote."}')
printf '%s\n' "$initial" | jq .
CHECKPOINT_ID=$(printf '%s\n' "$initial" | jq -er '.checkpoint.checkpoint_id')
```

Stop and inspect the response if it has no checkpoint. An AI answer without a
tool plan, a planning error, or a failed tool-discovery setup does not prove
HITL ran. Do not substitute an invented checkpoint ID.

Use the command and resume calls in the JSON recipe, but use
`/hitl/resume-sync/$CHECKPOINT_ID` for this shipped agent. With step-sensitive
approval enabled, expect a second `before_step` checkpoint. Approve and resume
that **new ID**, then read both parent and child with
`GET /hitl/checkpoints/{id}`.

| Exercise | Expected evidence |
|---|---|
| Approve and complete | Parent ends `completed`; returned execution succeeds; approved tool runs. |
| Plan approval followed by step approval | First parent ends `continued` with `successor_checkpoint_id`; child has `parent_checkpoint_id`, approved parameters, and later completes. |
| Reject a fresh checkpoint | Status becomes `rejected`; resume returns 409 `resume_not_resumable`; no new tool call. |
| Two resumes while one owns the checkpoint | Second caller gets 409 `resume_in_progress`; only the owner executes. Use a harmless slow mock to make this overlap observable. If the first already finished, expect `resume_not_resumable` instead. |
| Repeat resume after completion | 409 `resume_not_resumable`; no second execution of the approved action. |
| SSE success or another interruption | Progress may stream, but exactly one terminal `done` or `checkpoint` follows durable finalization. |
| Safe injected tool/delivery failure | Error outcome, not `done` success; inspect the stored state before retrying. Ordinary failure without a successor releases approval, but this alone does not prove replay is safe. |
| Disconnect during HTTP/SSE | Execution observes cancellation cooperatively. Reload state: completion may have won the race before disconnect. Never infer rollback from a closed connection. |
| Queued resume | Admission returns a task ID. Closing that HTTP request does not cancel the worker. Poll task result and checkpoint independently; `pending_approval` is not completed business work. |

Use a fresh request for each independent decision/failure exercise. Do not
mutate checkpoint records directly to manufacture these states. The queue row
uses the event-driven example, not the chat example's synchronous endpoint.
Lease-loss and storage-outage tests need a controlled failure environment;
they are not proved by an ordinary happy-path run.

For every run, save the actual request IDs, checkpoint IDs, HTTP status/body or
SSE terminal event, and the final checkpoint reads. Then inspect:

1. **Registry Viewer → Execution DAG:** find the initial request and resume.
   The initial request should be interrupted, not a completed business action.
   Check the HITL lifecycle, parent/successor IDs, reused versus newly executed
   steps, and the terminal application outcome. If hooks are registered, inspect
   **Pre-Execution** and **Post-Execution** too. An approved checkpoint resume
   does not rerun `AfterPlanning`.
2. **Viewer API:** read `/api/executions/{request_id}/unified` to compare the
   stored data with the screen. Allow asynchronous debug writes to finish;
   a checkpoint record can appear before its execution-debug record.
3. **Jaeger:** follow the Trace link. Look for the coordinator's `hitl.resume`
   span linked to the original execution, not a second copy created by the
   handler. Compare checkpoint/request correlation and outcome events. A claim
   rejected before execution is recorded on the caller span, not a new resume
   span.
4. **Tool evidence:** confirm the expected number of actual calls and their
   results. Reused step results in a resumed DAG are not proof of repeated tool
   calls. Model synthesis success alone is not proof of workflow completion.

The debug stores and Jaeger complement the durable checkpoint; they do not
replace it. Missing observability must be investigated, not interpreted as
proof that nothing executed.

### Unit Testing with NoOp Components

The framework provides NoOp implementations for testing without real infrastructure:

```go
func TestMyAgent_WithHITL(t *testing.T) {
    // Use NoOp policy - never triggers HITL
    policy := orchestration.NewNoOpPolicy()

    // Use NoOp handler - doesn't send webhooks
    handler := orchestration.NewNoOpInterruptHandler()

    // Create controller with test components
    controller := orchestration.NewInterruptController(
        policy,
        testCheckpointStore,  // Your mock or in-memory store
        handler,
    )

    // Wire into orchestrator
    orch.SetInterruptController(controller)

    // Test your agent - no HITL interrupts
    result, err := orch.ProcessRequest(ctx, "test query", nil)
    assert.NoError(t, err)
}
```

### Integration Testing with Real HITL

For optional, manually run integration tests, use a real DB-0 Redis with a
test-specific namespace. These checks are not part of the unit-test CI gate:

```go
func TestHITL_PlanApproval(t *testing.T) {
    // Use test Redis (e.g., testcontainers or local)
    keyspace, _ := core.NewRedisKeyspace("test")
    store, _ := orchestration.NewRedisCheckpointStore(
        orchestration.WithCheckpointRedisURL("redis://localhost:6379"),
        orchestration.WithCheckpointKeyspace(keyspace, "test-agent"),
    )
    defer store.Close()

    // Configure HITL to always require plan approval
    config := orchestration.HITLConfig{
        Enabled:             true,
        RequirePlanApproval: true,
        DefaultTimeout:      10 * time.Second,  // Short timeout for tests
    }

    // ... test your flow
}
```

### Testing Expiry Behavior

```go
func TestHITL_ExpiryCallback(t *testing.T) {
    actions := make(chan orchestration.CommandType, 1)

    callback := func(ctx context.Context, cp *orchestration.ExecutionCheckpoint, action orchestration.CommandType) {
        actions <- action
    }

    // A Redis checkpoint store satisfies both provider-neutral contracts.
    processor, err := orchestration.NewCheckpointExpiryProcessor(store, store, callback, orchestration.ExpiryProcessorConfig{
        Enabled:      true,
        ScanInterval: 100 * time.Millisecond,
    })
    require.NoError(t, err)
    go func() { _ = processor.Start(ctx) }()

    // Create a checkpoint that expires quickly
    // ... create checkpoint with short timeout

    select {
    case action := <-actions:
        assert.Equal(t, orchestration.CommandApprove, action) // or "" for implicit_deny
    case <-time.After(500 * time.Millisecond):
        t.Fatal("expiry callback was not called")
    }
}
```

---

## See Also

- [API Reference - HITL Section](../reference/API_REFERENCE.md#human-in-the-loop-hitl) - Complete API documentation with types, interfaces, and constructors
- [Chat Agent Guide](../memory-and-chat/CHAT_AGENT_GUIDE.md) - How to build a streaming chat agent
- [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md) - Complete configuration reference
- [Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md) - Trace correlation across HITL flows
- [agent-with-human-approval/](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-human-approval) - Complete working example with all the code
- [registry-viewer-app/](https://github.com/truvaagents/truva-g3/tree/main/examples/registry-viewer-app) - Registry viewer for monitoring HITL checkpoints
