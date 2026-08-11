# Human-in-the-Loop (HITL) User Guide

Hey there! This guide will teach you how to add human oversight to your TruvaG3 AI agents. If you've ever worried about an AI making decisions without supervision, HITL is your solution - it pauses execution at critical points so a human can review and approve before proceeding.

> **Working Example**
>
> Everything in this guide comes from a fully working implementation at:
> - **Agent**: [`examples/agent-with-human-approval/`](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-human-approval)
> - **Frontend**: [`examples/chat-ui/hitl.html`](https://github.com/truvaagents/truva-g3/blob/main/examples/chat-ui/hitl.html)
>
> We recommend running the example alongside reading this guide. It makes everything click faster.

---

## Table of Contents

- [What is HITL?](#what-is-hitl)
- [Quick Start](#quick-start)
- [Core Concepts](#core-concepts)
- [Agent Implementation](#agent-implementation)
- [Frontend Integration](#frontend-integration)
- [Non-Streaming API](#non-streaming-api)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [Expiry Processor Setup](#expiry-processor-setup)
- [Troubleshooting](#troubleshooting)
- [Auto-Resume](#auto-resume-timeout-auto-approval)
- [Registry Viewer](#registry-viewer-monitoring-hitl-checkpoints)
- [Production Deployment](#production-deployment)
- [Testing HITL Flows](#testing-hitl-flows)

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

- `TRUVAG3_AGENT_NAME`: Scopes this agent's HITL state in Redis to `truvag3:hitl:<agent_name>:*`. Without it, all agents pointed at the same Redis share `truvag3:hitl:pending`, and anything that fans out across agents (such as the registry viewer's HITL list) will see duplicates. K8s manifests typically set this; for local dev with more than one HITL agent on the same Redis, set it explicitly. The framework logs a startup Warn if it detects the shared-prefix configuration. See [Agent Isolation](#agent-isolation) for the full mechanism.
- `TRUVAG3_HITL_ENABLED`: The master switch. Without this, nothing HITL-related happens.
- `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL`: When true, the AI pauses after generating a plan but before executing anything. This lets you review the entire plan at once.
- `TRUVAG3_HITL_DEFAULT_TIMEOUT`: How long to wait for human response. After this, the system takes a default action (usually reject for safety).

### Adding HITL to Your Agent

Here's the minimal code to enable HITL. Don't worry about understanding every line - we'll break it down in later sections.

```go
// 1. Load HITL configuration (reads from environment variables)
hitlConfig := orchestration.DefaultConfig().HITL

// 2. Setup the HITL infrastructure (checkpoint store, command store, controller)
hitl, err := SetupHITL(agent.Logger, hitlConfig)
if err != nil {
    log.Fatalf("HITL setup failed: %v", err)
}
defer hitl.Close()  // Clean up on shutdown

// 3. After creating your orchestrator, wire in the HITL controller
orch.SetInterruptController(hitl.Controller)

// 4. Register the HTTP endpoints for approval commands
hitlHandler := orchestration.NewHITLHandler(hitl.Controller, hitl.CheckpointStore)
agent.RegisterHITLCapabilities(hitlHandler)
```

The `SetupHITL` function creates four components that work together:
1. **CheckpointStore**: Saves execution state to Redis when HITL pauses
2. **CommandStore**: Receives approval/rejection decisions via Pub/Sub
3. **Policy**: Decides when to pause (based on your configuration)
4. **Controller**: Coordinates everything

For a runnable compatibility example, see
[hitl_setup.go](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/hitl_setup.go).
That example still uses the retained store-owned expiry lifecycle; new
framework integrations should follow [Expiry Processor Setup](#expiry-processor-setup)
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

Checkpoints are stored in Redis and automatically expire after the configured timeout.

### Interrupt Points: Where Can HITL Pause?

HITL can pause at two points in execution:

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
    Human reviews: Retry? Skip? Abort?
```

Enable with: `TRUVAG3_HITL_ESCALATE_AFTER_RETRIES=3`

When the configured number of retries is exceeded, the checkpoint includes the error details and allows the human to:
- **Retry**: Try the step again
- **Skip**: Skip this step and continue with the plan
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

Phase 2 does the heavy lifting - it loads the checkpoint, re-runs the orchestrator, and streams back results. This can take seconds or minutes depending on what tools are being called.

Separating them means the approval is recorded instantly, and the (possibly slow) execution happens in the background.

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

When the user approves, the frontend calls your resume endpoint. This handler needs to:

1. Load the checkpoint from Redis
2. Set up the context with the saved state
3. Re-run the orchestrator

Here's the critical part - setting up the context:

```go
// 1. Mark this as a resume (so HITL doesn't pause at the same point again)
ctx = orchestration.WithResumeMode(ctx, checkpointID)

// 2. Use the stored plan (critical - step IDs must match)
ctx = orchestration.WithPlanOverride(ctx, checkpoint.Plan)

// 3. Inject completed steps (so we don't redo work)
if len(checkpoint.StepResults) > 0 {
    ctx = orchestration.WithCompletedSteps(ctx, checkpoint.StepResults)
}

// 4. For step-level approval, inject the approved parameters
if checkpoint.ResolvedParameters != nil && checkpoint.CurrentStep != nil {
    ctx = orchestration.WithPreResolvedParams(
        ctx,
        checkpoint.ResolvedParameters,
        checkpoint.CurrentStep.StepID,
    )
}

// 5. Re-process the original request with this enriched context
err = t.ProcessWithStreaming(ctx, sessionID, checkpoint.OriginalRequest, callback)
```

**Why is `WithPlanOverride` critical?**

Without it, the orchestrator would generate a NEW plan. The step IDs would be different, and `WithCompletedSteps` wouldn't know which steps to skip. By injecting the original plan, step IDs stay stable.

**What happens if HITL pauses again?**

If there are multiple sensitive steps, resuming might trigger another checkpoint. That's fine! The handler will return `IsInterrupted(err) == true` again, and the frontend can show another approval dialog. This is called "chained approvals."

See [handlers.go:handleResumeSSE](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/handlers.go) for the complete implementation.

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

    // Important: Send the original request_id for trace correlation
    // This lets you find all related traces in Jaeger
    if (originalRequestId) {
        headers['X-Truvag3-Original-Request-ID'] = originalRequestId;
    }

    const response = await fetch(`${backendUrl}/hitl/resume/${checkpointId}`, {
        method: 'POST',
        headers
    });

    // Process SSE stream (use the same handler as initial request!)
    await processSSEStream(response);
}
```

The resume stream uses the same event format as the initial request, so you can reuse your SSE handler. This is nice because you don't need separate logic - chunks, steps, checkpoints (for chained approvals), and done events all work the same way.

See [hitl.html](https://github.com/truvaagents/truva-g3/blob/main/examples/chat-ui/hitl.html) for the complete frontend implementation.

### Editing Plans and Parameters

Beyond simple approve/reject, users can modify plans or step parameters before proceeding. The `edit` command lets them do this.

**Frontend: Editing a Plan**

```javascript
async function submitEditedPlan(checkpointId, editedPlan) {
    const response = await fetch(`${backendUrl}/hitl/command`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            checkpoint_id: checkpointId,
            type: 'edit',
            edited_plan: editedPlan  // Modified plan structure
        })
    });

    const result = await response.json();
    if (result.should_resume) {
        await resumeExecution(checkpointId);
    }
}
```

**Frontend: Editing Step Parameters**

For step-level approval, users might want to modify parameters:

```javascript
async function submitEditedParams(checkpointId, editedParams) {
    const response = await fetch(`${backendUrl}/hitl/command`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            checkpoint_id: checkpointId,
            type: 'edit',
            edited_params: editedParams  // e.g., { "symbol": "AAPL", "quantity": 50 }
        })
    });

    const result = await response.json();
    if (result.should_resume) {
        await resumeExecution(checkpointId);
    }
}
```

**Backend: Handling Edited Plans**

The controller automatically handles edited plans. When you call `ProcessCommand` with an `edit` type:

1. Status changes to `edited`
2. `result.ModifiedPlan` contains the edited plan
3. On resume, `BuildResumeContext` uses the edited plan

For edited parameters, the modified values are stored in the checkpoint and used during resume via `WithPreResolvedParams`.

**UI Example: Parameter Editor**

```javascript
function showEditDialog(checkpoint) {
    const params = checkpoint.resolved_parameters;

    // Build edit form
    const form = document.createElement('form');
    for (const [key, value] of Object.entries(params)) {
        form.innerHTML += `
            <label>${key}:</label>
            <input name="${key}" value="${value}" />
        `;
    }

    // On submit
    form.onsubmit = (e) => {
        e.preventDefault();
        const editedParams = {};
        new FormData(form).forEach((v, k) => editedParams[k] = v);
        submitEditedParams(checkpoint.checkpoint_id, editedParams);
    };
}
```

---

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
    "request": "I want to sell 100 TESLA shares to fund my trip to London for a week. Will I be able to afford it?",
    "use_ai": true
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
| `POST /hitl/resume/{id}` | **Agent** | Needs to call agent's `ProcessWithStreaming` |
| `POST /hitl/resume-sync/{id}` | **Agent** | Needs to call agent's `ProcessSync` |

The resume endpoints are agent-specific because they need to call your agent's processing method to continue execution. The framework provides the handlers for command and checkpoint operations via `orchestration.HITLHandler`.

### Registering Framework Endpoints

Create an `HITLHandler` and register its routes:

```go
// Create the handler with controller and checkpoint store
hitlHandler := orchestration.NewHITLHandler(
    hitl.Controller,
    hitl.CheckpointStore,
    orchestration.WithHITLHandlerLogger(logger),      // Optional
    orchestration.WithHITLHandlerTelemetry(telemetry), // Optional
)

// Option 1: Use RegisterRoutes for automatic registration
hitlHandler.RegisterRoutes(mux)  // Registers /hitl/command, /hitl/checkpoints, /hitl/checkpoints/{id}

// Option 2: Register handlers individually for more control
mux.HandleFunc("/hitl/command", hitlHandler.HandleCommand)
mux.HandleFunc("/hitl/checkpoints", hitlHandler.HandleListCheckpoints)
mux.HandleFunc("/hitl/checkpoints/", hitlHandler.HandleGetCheckpoint)  // Note: trailing slash for path param
```

Then add your agent-specific resume handlers (see [Implementing Resume Handlers](#implementing-resume-handlers-agent-specific) below).

For a complete example, see [chat_agent.go:RegisterHITLCapabilities](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/chat_agent.go).

### Implementing Resume Handlers (Agent-Specific)

Your resume handler needs to: load the checkpoint, build a context with the saved state, and call your agent's processing method. The framework provides helpers to make this straightforward.

**Option 1: Use `BuildResumeContext` (Recommended)**

The simplest approach - one function call handles all context setup:

```go
func (a *MyAgent) handleResume(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    checkpointID := extractCheckpointID(r)  // Your URL parsing

    // 1. Load checkpoint from store
    checkpoint, err := a.checkpointStore.LoadCheckpoint(ctx, checkpointID)
    if err != nil {
        http.Error(w, "Checkpoint not found", http.StatusNotFound)
        return
    }

    // 2. Build resume context (handles all the plumbing)
    resumeCtx, endResumeSpan, err := orchestration.BuildResumeContext(ctx, checkpoint)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    defer endResumeSpan()

    // 3. Re-process the original request
    err = a.ProcessRequest(resumeCtx, checkpoint.OriginalRequest, callback)

    // 4. Handle potential chained interrupts
    if orchestration.IsInterrupted(err) {
        // Another HITL checkpoint - send it to frontend
        newCheckpoint := orchestration.GetCheckpoint(err)
        sendCheckpointResponse(w, newCheckpoint)
        return
    }
    // ... handle success or error
}
```

`BuildResumeContext` ([hitl_helpers.go:141](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_helpers.go#L141)) automatically:
- Validates the checkpoint has a resumable status
- Sets resume mode (`WithResumeMode`)
- Injects the stored plan (`WithPlanOverride`)
- Injects completed step results (`WithCompletedSteps`)
- Injects pre-resolved parameters for step-level resume (`WithPreResolvedParams`)
- Preserves request mode and user context
- Validates and restores canonical `conversation_id` to core context,
  checkpoint metadata, and metric-ineligible W3C Baggage when present
- Starts a `hitl.resume` span linked to the original trace; the caller must
  invoke the returned cleanup function

**Option 2: Manual Context Building**

For more control, use the individual helpers:

```go
// Mark as resume (prevents re-triggering the same checkpoint)
ctx = orchestration.WithResumeMode(ctx, checkpointID)

// CRITICAL: Use stored plan (step IDs must match for skip logic)
ctx = orchestration.WithPlanOverride(ctx, checkpoint.Plan)

// Skip already-completed steps
if len(checkpoint.StepResults) > 0 {
    ctx = orchestration.WithCompletedSteps(ctx, checkpoint.StepResults)
}

// For step-level approval: use the approved parameter values
if checkpoint.ResolvedParameters != nil && checkpoint.CurrentStep != nil {
    ctx = orchestration.WithPreResolvedParams(
        ctx,
        checkpoint.ResolvedParameters,
        checkpoint.CurrentStep.StepID,
    )
}

// Preserve request mode for expiry behavior
if checkpoint.RequestMode != "" {
    ctx = orchestration.WithRequestMode(ctx, checkpoint.RequestMode)
}
```

**Framework Helpers Reference:**

*Resume Context Helpers* ([hitl_helpers.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_helpers.go), [orchestrator.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/orchestrator.go))

| Helper | Purpose |
|--------|---------|
| `BuildResumeContext(ctx, checkpoint)` | Returns the resume context, linked-span cleanup function, and error (recommended) |
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
| `IsResumableStatus(status)` | Returns true for: approved, edited, expired_approved |
| `IsTerminalStatus(status)` | Returns true for: completed, rejected, aborted, expired, expired_rejected, expired_aborted |
| `IsPendingStatus(status)` | Returns true for: pending (awaiting human response) |

*Key Types & Constants* ([hitl_interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_interfaces.go))

| Type | Values | Purpose |
|------|--------|---------|
| `CheckpointStatus` | `preparing`, `pending`, `approved`, `rejected`, `edited`, `completed`, `aborted`, `expired`, `expired_approved`, `expired_rejected`, `expired_aborted` | Checkpoint lifecycle states; `preparing` is framework-owned and transient |
| `InterruptPoint` | `plan_generated`, `before_step`, `on_error` (implemented); `after_step`, `context_gathering` (reserved) | Where HITL can pause |
| `RequestMode` | `streaming`, `non_streaming` | Determines expiry behavior |
| `CommandType` | `approve`, `reject`, `edit`, `skip`, `abort`, `retry` | Human decision types |

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
| `edit` | Proceed with modifications (provide `edited_plan`) |
| `skip` | Skip current step, continue with next |
| `abort` | Stop entire workflow immediately |
| `retry` | Retry with new parameters |

> **Note:** Not all commands are valid for all interrupt points. For example, `skip` and `retry` are not available for `plan_generated`. See the [Command Types by Interrupt Point](../reference/API_REFERENCE.md#command-types-by-interrupt-point) table for the complete matrix.

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

**Example implementation:** [handlers.go:238](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/handlers.go#L238)

```go
func (t *HITLChatAgent) handleResumeSSE(w http.ResponseWriter, r *http.Request)
```

**Request:**
```bash
curl -X POST http://localhost:8352/hitl/resume/cp-dff21578-e9e1-40 \
  -H "Accept: text/event-stream" \
  -H "X-Truvag3-Original-Request-ID: orch-1769372315637984136"
```

The `X-Truvag3-Original-Request-ID` header is optional but recommended for trace correlation in Jaeger.

### POST /hitl/resume-sync/`{id}` (Agent)

Resume execution after approval. Returns JSON response (for non-streaming clients). See [Implementing Resume Handlers](#implementing-resume-handlers-agent-specific) above for the framework APIs to use.

**Example implementation:** [handlers.go:793](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/handlers.go#L793)

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
| `TRUVAG3_HITL_REDIS_DB` | `6` | Redis database number for HITL data |
| `TRUVAG3_HITL_KEY_PREFIX` | `truvag3:hitl` | Base Redis key prefix. Setting this explicitly counts as an isolation choice and suppresses the shared-prefix Warn |
| `TRUVAG3_AGENT_NAME` | `""` | Agent name appended to the base prefix for multi-agent isolation |
| `TRUVAG3_K8S_SERVICE_NAME` | `""` | K8s Service name; used as `TRUVAG3_AGENT_NAME` fallback when unset |

**The agent identity is structural, not optional.** At construction, the HITL checkpoint store builds its Redis key prefix as `{TRUVAG3_HITL_KEY_PREFIX}:{TRUVAG3_AGENT_NAME}` — defaulting to `truvag3:hitl` for the base and falling back to `TRUVAG3_K8S_SERVICE_NAME` for the agent-name segment. With an agent name set, this agent's pending index is `truvag3:hitl:<agent_name>:pending`. Without one, the agent-name suffix is omitted entirely and the pending index becomes `truvag3:hitl:pending` — shared by every other agent in the same regime pointed at the same Redis. (Substitute your custom value for `truvag3:hitl` if you've set `TRUVAG3_HITL_KEY_PREFIX`.)

When the framework detects the shared-prefix configuration at construction, it emits a startup Warn:

```
HITL checkpoint store using shared key prefix — set TRUVAG3_AGENT_NAME
(or TRUVAG3_K8S_SERVICE_NAME) to isolate per-agent state
```

The warning suppresses when isolation has been supplied through any of the available paths: `TRUVAG3_AGENT_NAME`, `TRUVAG3_K8S_SERVICE_NAME`, an explicit `TRUVAG3_HITL_KEY_PREFIX`, or the `WithCheckpointKeyPrefix` option. K8s deployments using the framework's standard manifests already set the env var; for local dev with more than one HITL-enabled agent, set it explicitly.

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
// 1. Validate the capabilities supplied by your application's backend bundle.
requirements, err := orchestration.RequirementsForFeatures(
    nil,
    orchestration.BackendFeatureCheckpointExpiry,
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

    // For auto-resume: trigger execution continuation
    if action == orchestration.CommandApprove {
        // Your auto-resume logic here
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
if err := framework.Run(); err != nil {
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

**What you see:** Steps that already completed are running again.

**Why it happens:** The resume handler is missing one of the context helpers.

**How to fix:** Make sure you're calling ALL of these:
```go
ctx = orchestration.WithResumeMode(ctx, checkpointID)
ctx = orchestration.WithPlanOverride(ctx, checkpoint.Plan)  // Critical!
ctx = orchestration.WithCompletedSteps(ctx, checkpoint.StepResults)
ctx = orchestration.WithPreResolvedParams(ctx, checkpoint.ResolvedParameters, stepID)
```

The most common mistake is forgetting `WithPlanOverride`. Without it, the orchestrator generates a new plan with different step IDs.

### Trace Correlation Not Working

**What you see:** Related traces show up as separate, unlinked entries in Jaeger.

**Why it happens:** The `X-Truvag3-Original-Request-ID` header isn't being sent on resume.

**How to fix:** Store the request_id from the first checkpoint and include it on all resumes:
```javascript
// On first checkpoint
if (data.request_id && !originalRequestId) {
    originalRequestId = data.request_id;
}

// On all resumes
headers['X-Truvag3-Original-Request-ID'] = originalRequestId;
```

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

### Duplicate Checkpoints in the Registry Viewer (Visual Only)

**What you see:** The viewer's "HITL interrupted" screen shows several rows that all have the same `checkpoint_id`, `request_id`, and timestamps. Approving one row doesn't make the others disappear until the next refresh.

**Why it happens:** Multiple agents are sharing the same Redis pending index (`truvag3:hitl:pending`) because none of them have an agent identity configured. Each agent's `GET /hitl/checkpoints` endpoint legitimately returns the full shared list, and the viewer's fan-out concatenates the responses. This is distinct from the multi-pod processing issue above — the underlying checkpoint is processed once correctly; only the *display* duplicates.

**Diagnostic:** Inspect the viewer API response directly and count distinct vs. total `checkpoint_id` values:

```bash
curl -s http://<viewer>/api/hitl/checkpoints | jq '.checkpoints | length, (map(.checkpoint_id) | unique | length)'
```

If total > distinct, you're hitting this case. If total == distinct and growing, you're seeing genuine checkpoint accumulation (tune `TRUVAG3_HITL_DEFAULT_TIMEOUT`).

**How to fix:** Give each agent a distinct identity so their Redis prefixes don't collide. Any of these works:

```bash
# Most common — set the env var
TRUVAG3_AGENT_NAME=my-agent

# K8s standard — set via Service name (falls back if TRUVAG3_AGENT_NAME is unset)
TRUVAG3_K8S_SERVICE_NAME=my-agent

# Custom prefix path — set a fully-isolated key prefix
TRUVAG3_HITL_KEY_PREFIX=truvag3:hitl:my-agent
```

Or pass `orchestration.WithCheckpointKeyPrefix("…")` to `NewRedisCheckpointStore`. The framework logs a startup Warn pointing at this — check agent logs for "using shared key prefix" before the duplication appears in the UI.

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
        await fetch(`/hitl/auto-resume/${checkpointId}/stream`, {
            method: 'POST'
        });
    }
}
```

The auto-resume handler uses the same framework APIs as the regular resume handlers. See [Implementing Resume Handlers](#implementing-resume-handlers-agent-specific) for details.

**Example implementation:** [handlers_auto_resume.go:22](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-human-approval/handlers_auto_resume.go#L22)

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
| `edited` | Human-initiated | Human modified plan and approved |
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

The list endpoint uses the pending index (`truvag3:hitl:*:pending`) which is fast. The search endpoint scans checkpoint keys directly, which is slower but necessary to find non-pending checkpoints.

For typical usage (dozens to hundreds of checkpoints), this is fine. The registry viewer is primarily a development and debugging tool.

**Note:** Checkpoints have a TTL in Redis (default 24 hours after expiry). Very old checkpoints may no longer exist.

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
2. Pod A attempts to claim it via Redis `SETNX` with 30s TTL
3. If successful, Pod A processes the checkpoint
4. If Pod B tries to claim the same checkpoint, `SETNX` fails - it skips
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

When multiple HITL-enabled agents share the same Redis, each one must have a distinct identity. Without it, all agents write to the **same** pending-checkpoint index (`truvag3:hitl:pending`), and the registry viewer's HITL list ends up showing each checkpoint once per agent that's running — N agents reporting the same M checkpoints produces N × M rows in the UI. Approvals still route correctly (the owning agent is stamped inside the checkpoint blob), but the screen becomes unusable for diagnosis.

#### How identity resolves

At `NewRedisCheckpointStore` or `NewRedisCheckpointStoreWithClient`
construction, the framework builds the Redis key prefix in two parts.

**Part 1 — the base prefix** comes from `TRUVAG3_HITL_KEY_PREFIX`, defaulting to `truvag3:hitl`. Without an in-code override, the framework combines this base with the agent suffix described below. The `WithCheckpointKeyPrefix` option then overrides the fully resolved prefix: its value is used as-is, and no agent suffix is appended to it.

**Part 2 — the agent-name suffix** is appended to the base. Resolved in priority order:

1. `TRUVAG3_AGENT_NAME` (preferred — explicit agent identity)
2. `TRUVAG3_K8S_SERVICE_NAME` (fallback — K8s manifests typically set this on the Service)
3. Empty — no suffix appended; the prefix stays at the base

Final prefix = `{base}:{agent_name}` if an agent name was resolved, otherwise just `{base}`. The shared-prefix Warn fires only when (a) no agent name was supplied, **and** (b) `TRUVAG3_HITL_KEY_PREFIX` wasn't set explicitly, **and** (c) `WithCheckpointKeyPrefix` didn't override. Any one of those paths suppresses the Warn — the framework treats them all as explicit operator choices.

#### Configure isolation through any of these paths

```bash
# Path 1 — explicit env var (most common, K8s and local)
TRUVAG3_AGENT_NAME=trading-agent

# Path 2 — Service name fallback (K8s sets this on the Deployment manifest)
TRUVAG3_K8S_SERVICE_NAME=trading-agent

# Path 3 — custom base prefix (e.g., separate staging from prod sharing one Redis,
# or scope a multi-tenant Redis to a single team)
TRUVAG3_HITL_KEY_PREFIX=truvag3:hitl:staging
```

```go
// Path 4 — in-code override
checkpointStore, _ := orchestration.NewRedisCheckpointStore(
    orchestration.WithCheckpointRedisURL(redisURL),
    orchestration.WithCheckpointKeyPrefix("truvag3:hitl:trading-agent"),
)
```

If application bootstrap already owns Redis routing and credentials, inject
that client instead:

```go
checkpointStore, err := orchestration.NewRedisCheckpointStoreWithClient(
    redisClient,
    orchestration.WithCheckpointKeyPrefix("truvag3:hitl:trading-agent"),
)
```

The supplied client remains application-owned and is left open when the store
is closed. This constructor still uses the identity resolution described above:
`TRUVAG3_HITL_KEY_PREFIX` establishes the base, and the agent/service identity
is appended unless `WithCheckpointKeyPrefix` provides the final prefix. Redis
connection and database selection belong to the injected client.

Whichever path you choose, this agent's checkpoints are stored at `{prefix}:checkpoint:{id}` and the pending index at `{prefix}:pending` — disjoint from every other agent.

#### What the framework does on its own

K8s replicas of one Deployment naturally share `TRUVAG3_K8S_SERVICE_NAME`, but `applyConfigToComponent` collapses them to a single service-discovery entry (`base.ID = config.Name`), so the registry viewer hits only one replica per logical agent. Replica sharing is intentional and doesn't produce duplicates. The duplication surfaces only when (a) multiple **logical** agents share a prefix, or (b) operators force instance-scoped registration via per-pod `TRUVAG3_AGENT_ID`.

If isolation isn't configured, you'll see this in the agent's startup logs:

```
WARN HITL checkpoint store using shared key prefix —
     set TRUVAG3_AGENT_NAME (or TRUVAG3_K8S_SERVICE_NAME)
     to isolate per-agent state
     key_prefix=truvag3:hitl
```

The Warn is the framework's signal that something downstream — typically the registry viewer — will misbehave. Address it at the source rather than working around the symptom.

---

## Testing HITL Flows

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

For integration tests, use a real Redis but with test-specific configuration:

```go
func TestHITL_PlanApproval(t *testing.T) {
    // Use test Redis (e.g., testcontainers or local)
    store, _ := orchestration.NewRedisCheckpointStore(
        orchestration.WithCheckpointRedisURL("redis://localhost:6379"),
        orchestration.WithCheckpointKeyPrefix("test:hitl"),  // Isolated prefix
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
