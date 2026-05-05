# Gateway Development Guide (DRAFT)

> **Status:** Draft, kept alongside the first gateway example (`slack-gateway`). Promote to `docs/` once a second gateway (WhatsApp, Teams, CLI, etc.) validates the patterns below.

A complete walkthrough for building **Gateways** in TruvaG3 — the third architectural category alongside Tools and Agents. Gateways bridge external messaging/eventing platforms (Slack, WhatsApp, Teams, Discord, email, SMS, webhooks) into TruvaG3 agents without joining the service mesh.

## Table of Contents

1. [What is a Gateway?](#1-what-is-a-gateway)
2. [Gateway vs Tool vs Agent](#2-gateway-vs-tool-vs-agent)
3. [When to Build a Gateway](#3-when-to-build-a-gateway)
4. [Architecture Overview](#4-architecture-overview)
5. [Project Structure](#5-project-structure)
6. [Step 1: Event Listener](#6-step-1-event-listener)
7. [Step 2: Session Mapping](#7-step-2-session-mapping)
8. [Step 3: Agent Client (SSE Consumer)](#8-step-3-agent-client-sse-consumer)
9. [Step 4: Response Writer](#9-step-4-response-writer)
10. [Step 5: Async Execution Model](#10-step-5-async-execution-model)
11. [Step 6: HITL Bridging (Optional)](#11-step-6-hitl-bridging-optional)
12. [Step 7: Main Entry Point](#12-step-7-main-entry-point)
13. [Deployment Considerations](#13-deployment-considerations)
14. [Observability](#14-observability)
15. [Authorization (What Adopters Must Add)](#15-authorization-what-adopters-must-add)
16. [Testing](#16-testing)
17. [Best Practices](#17-best-practices)
18. [What This Guide Does Not Cover](#18-what-this-guide-does-not-cover)
19. [References](#19-references)

---

## 1. What is a Gateway?

In TruvaG3, a **Gateway** is a standalone service that:

- Receives events from an **external messaging or eventing platform** (Slack, WhatsApp, Teams, Discord, email, webhooks, CLI, etc.)
- Forwards each event to a **configured TruvaG3 agent's existing HTTP/SSE interface** (`/chat/stream`, `/chat/session`, `/hitl/command`)
- Formats the agent's response for the platform's native conventions and delivers it back (inline reply, DM, thread message, SMS, etc.)
- Bridges platform-native interactive elements (buttons, reactions, quick replies) into TruvaG3's HITL approval flow when present

A gateway is a **client of agents**, not part of the service mesh. It does not register capabilities and is not discovered by other components.

## 2. Gateway vs Tool vs Agent

TruvaG3's two-type discipline is enforced at compile time: a component is a Tool or an Agent. Gateways sit **outside** that type system as plain Go services that *consume* the mesh.

| Dimension | Tool | Agent | Gateway |
|---|---|---|---|
| Registers in Redis registry? | Yes (capabilities) | Yes (agent identity) | No |
| Discovered by other components? | Yes | Yes | No |
| Can discover/call others? | No (passive) | Yes | Yes, but via **fixed URL**, not discovery |
| Primary inbound protocol | HTTP `POST /api/capabilities/*` | HTTP `POST /chat/stream` | Platform-native (WebSocket, webhooks, IMAP, etc.) |
| Primary outbound protocol | External REST API | Other agents/tools via discovery | TruvaG3 agent's existing HTTP/SSE endpoints |
| Scale model | Stateless, horizontal | Stateless, horizontal | Often **single-replica or leader-elected** (persistent external connections) |
| State ownership | None (stateless) | Conversation, memory | None (forwards to agent) |
| Base struct | `*core.BaseTool` | `*core.BaseAgent` | Plain struct; may use `core.NewFramework` for lifecycle only |
| Discovery-enabled? | `WithDiscovery(true)` | `WithDiscovery(true)` | `WithDiscovery(false)` or not set |
| Example | [slack-tool](../slack-tool) (outbound Slack messages) | [devops-chat-agent](../devops-chat-agent) | [slack-gateway](./) (inbound Slack DMs) |

**Useful mental model:** if the platform has a persistent outbound arrow (platform → your code), you are probably building a gateway, not a tool.

## 3. When to Build a Gateway

Build a gateway when:

- A **platform pushes events to you** (webhook, WebSocket, email poll, queue) that should trigger an agent conversation.
- The event flow is **user-initiated** ("user DMs the bot", "user replies in a thread", "user texts a number") rather than agent-initiated ("agent posts a status update").
- You want to **reuse an agent's existing HTTP/SSE contract** without every platform integration becoming a special case inside the agent itself.

Do **not** build a gateway when:

- You only need to **send** messages from agents (build a Tool — see [TOOL_DEVELOPMENT_GUIDE.md](../../docs/building/TOOL_DEVELOPMENT_GUIDE.md)).
- The external system is a **stateless REST API** and you just call it during a task (Tool).
- You want to **orchestrate multiple tools** or reason over inputs (Agent).

**One platform can warrant both** — for example, [slack-tool](../slack-tool) handles outbound posting while [slack-gateway](./) handles inbound DMs. Same platform, opposite arrows, different architectural role.

## 4. Architecture Overview

```
┌─────────────────────────┐
│    External Platform    │   Examples: Slack, WhatsApp, Teams,
│ (push events → gateway) │   Discord, email (IMAP), SMS webhooks
└────────────┬────────────┘
             │
             ▼
┌─────────────────────────────────────────────────────────────┐
│                      Your Gateway                            │
│                                                              │
│  ┌────────────────────────────────────────────────────┐    │
│  │  Event Listener (platform-specific)                 │    │
│  │  • Accept WebSocket frames / signed webhooks        │    │
│  │  • Ack within platform deadline (e.g., 3s for Slack)│    │
│  │  • Filter event types                               │    │
│  └───────────────────┬────────────────────────────────┘    │
│                      │                                       │
│                      ▼                                       │
│  ┌────────────────────────────────────────────────────┐    │
│  │  Session Mapper (Redis DB 2 or stateless)          │    │
│  │  • Derive stable session_id from platform identity │    │
│  └───────────────────┬────────────────────────────────┘    │
│                      │                                       │
│                      ▼                                       │
│  ┌────────────────────────────────────────────────────┐    │
│  │  Agent Client (SSE consumer)                        │    │
│  │  • POST to <AGENT>/chat/stream with session_id     │    │
│  │  • Consume SSE events                              │    │
│  │  • Propagate trace context                         │    │
│  └───────────────────┬────────────────────────────────┘    │
│                      │                                       │
│                      ▼                                       │
│  ┌────────────────────────────────────────────────────┐    │
│  │  Response Writer (platform-specific)                │    │
│  │  • Format for platform (markdown, Block Kit, TwiML)│    │
│  │  • Send via platform's outbound API                │    │
│  └───────────────────┬────────────────────────────────┘    │
│                      │                                       │
│                      ▼  (optional)                           │
│  ┌────────────────────────────────────────────────────┐    │
│  │  HITL Bridge                                        │    │
│  │  • Detect checkpoint.paused events                 │    │
│  │  • Render platform-native approval UI              │    │
│  │  • Route responses to <AGENT>/hitl/command         │    │
│  └────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
             │
             │ HTTP / SSE (no Discovery; fixed URL via env var)
             ▼
┌─────────────────────────┐
│    Target TruvaG3 Agent  │   Owns conversation state,
│  (unchanged by gateway) │   memory, tool orchestration
└─────────────────────────┘
```

## 5. Project Structure

```
examples/your-gateway/
├── main.go               # Entry point, config validation, telemetry, graceful shutdown
├── gateway.go            # Gateway struct, listener wiring, event loop
├── events.go             # Platform event type definitions + dispatch
├── session.go            # Session ID derivation + (optional) Redis mapping cache
├── agent_client.go       # SSE consumer for <AGENT>/chat/stream
├── response_writer.go    # Platform-specific formatting + outbound send
├── hitl_bridge.go        # Optional: checkpoint → platform UI bridging
├── go.mod / go.sum
├── Dockerfile            # Published image
├── Dockerfile.workspace  # Local dev build
├── k8-deployment.yaml    # K8s manifests (Deployment + Service + optional Ingress)
├── setup.sh              # Lifecycle script (build / run / deploy / forward / rebuild)
├── .env.example          # Documented env vars
└── README.md             # Adopter-facing docs
```

### What differs from the Tool structure

| Tool file | Gateway equivalent | Notes |
|---|---|---|
| `your_tool.go` (capability registration) | `gateway.go` (event loop wiring) | No capabilities; event dispatch instead |
| `handlers.go` (HTTP handler per capability) | `events.go` (platform event type dispatch) | Keyed by event type, not URL |
| `api_client.go` (external REST client) | `agent_client.go` (internal SSE consumer) | Talks to a *TruvaG3 agent*, not a third party |
| *(none)* | `response_writer.go` | Outbound side to the platform (not present in Tools) |
| *(none)* | `session.go` | Stable mapping from platform identity to session_id |
| *(none)* | `hitl_bridge.go` | Optional — only if supporting HITL checkpoints |

## 6. Step 1: Event Listener

Every gateway needs a listener that accepts events from the platform and dispatches them. Keep this layer thin — just transport and filtering.

**Responsibilities:**
- Accept the platform's native transport (WebSocket, HTTPS webhook, IMAP polling, etc.).
- **Verify signatures** if the platform signs payloads (Slack Events API signing secret, Stripe/GitHub webhook HMACs, etc.).
- **Ack within the platform's deadline.** Many platforms expect a response in 3 seconds or less; they retry otherwise. Ack immediately, do real work in a goroutine or task queue.
- **Filter out noise:** bot messages (prevent loops), edits, deletes, ephemeral messages, events outside the bot's channels.
- Extract a canonical `PlatformEvent` struct with the fields downstream layers need: user ID, channel/conversation ID, message text, timestamp, optional thread/reply context, and any attachments.

```go
type PlatformEvent struct {
    EventID       string   // Platform-unique ID, used for dedup
    UserID        string   // Platform user identifier
    ChannelID     string   // Channel / conversation / phone number
    ThreadID      string   // Optional: thread/conversation anchor
    Text          string   // User's message body
    TimestampUnix int64
    Metadata      map[string]string  // Platform-specific extras
}
```

**Dedup:** if the platform retries (Slack retries after 3s), keep a short-lived Redis set of processed `EventID`s so retries no-op. 60-second TTL is usually enough.

## 7. Step 2: Session Mapping

The agent stores conversation history by `session_id` (see framework session store, typically Redis DB 2). The gateway must derive a stable `session_id` for every inbound event so follow-up messages continue the same conversation.

**Recommended derivation (stateless):**
```
session_id = sha256("<platform>:" + workspace_or_tenant + ":" + user_id + ":" + channel_id + optional(":" + thread_id))[:32]
```

Properties:
- **Deterministic** — no Redis writes needed for the mapping itself.
- **Stable** — same user in the same channel always resumes the same conversation.
- **Scoped** — DM vs channel vs thread are distinct sessions. DMs stay private; threads stay isolated.
- **Multi-tenant safe** — include `team_id` / `workspace_id` / `tenant_id` so the same user across tenants is distinct.

**When to use Redis (DB 2) for mapping:**
- You need **session aliases** (e.g., a human-friendly command `/switch-session X` that rebinds).
- You want to **migrate** a session when a user changes phone numbers / Slack handles.
- You need **cross-platform linking** (same human, different platform identities).

For a minimum-viable gateway, deterministic derivation is enough. Add the Redis mapping only when a feature requires it.

## 8. Step 3: Agent Client (SSE Consumer)

The gateway's outbound side calls the target agent's existing chat endpoint. The agent does not know or care that the caller is a gateway.

**Contract (as of current framework):**
```
POST <AGENT>/chat/stream
Content-Type: application/json
{
  "session_id": "<derived_session_id>",
  "message":    "<user_text>",
  "user_id":    "<platform_user_id>",
  "metadata":   { "source": "slack-gateway", "channel": "<channel_id>", ... }
}
```

Response is Server-Sent Events. Each event has a `type` field. Consume them:

| SSE event type | What to do |
|---|---|
| `stream.start` | Note the `request_id` for logs/traces |
| `partial` | Optional: progressive update to the platform (Phase 3) |
| `tool_call` | Optional: emit a short "using tool X" status to the platform |
| `checkpoint.paused` | **Stop streaming to user; trigger HITL bridge (§11)** |
| `final` | Buffer the final text; this is what you post back |
| `error` | Surface a user-readable error; log full detail |
| `stream.end` | Close the SSE connection cleanly |

**Important:** use `telemetry.NewTracedHTTPClientWithTransport()` (from the framework's `telemetry` module) so the trace context propagates from the platform event into the agent run. See [DISTRIBUTED_TRACING_GUIDE.md](../../docs/observability/DISTRIBUTED_TRACING_GUIDE.md) for details.

**Resilience:** wrap the call in retries with exponential backoff for transient errors (connection refused, 5xx). Do **not** retry on user-level errors (4xx), and do not retry if a partial response has already been delivered to the user.

## 9. Step 4: Response Writer

Converts the agent's text/markdown into platform-native conventions.

**Typical transformations:**
- Slack: Markdown → `mrkdwn` (asterisks for bold, not double-asterisks; `<url|text>` for links). Use Block Kit for rich structure.
- WhatsApp: plain text, single-asterisk bold, no links in some contexts, length caps around 4096 chars.
- Email: HTML or plain text; subject line from first sentence; consider length/threading.
- SMS: plain text, <160 chars ideally, split into multi-part SMS if needed.

**Delivery channel selection:** reply in the **same channel/thread the event arrived on**. Do not DM a user who sent a channel message and vice versa — it surprises users.

**Length handling:** if the agent produces 10,000 chars and the platform caps at 4,000, split into multiple messages with a leading `(1/3)` marker, or attach as a file if the platform supports it.

**Progressive updates (Phase 3):** for long-running agent responses, post a "thinking..." placeholder immediately, then use the platform's message-edit API (`chat.update` for Slack) to progressively reveal tokens. Watch platform rate limits — Slack throttles `chat.update` per channel; batch updates.

## 10. Step 5: Async Execution Model

Platforms require fast acknowledgment; agents may take seconds to minutes. The gateway must be async internally.

Two patterns:

### Pattern A — Goroutine-per-event (default)

```
event arrives → ack platform → spawn goroutine:
    derive session → call agent SSE → buffer response → send to platform
```

**Pros:** simple, no extra infrastructure, easy to reason about.

**Cons:** in-flight work is **lost on pod crash**. Recovery is "user resends message" and, ideally, platform event retries from the dedup window.

**Use when:** agent runs complete in seconds, single-replica deployment is acceptable, occasional lost messages are tolerable.

### Pattern B — Durable task queue

Reuses `TaskQueue` / `TaskStore` / `TaskWorkerPool` from the async orchestration module:

```
event arrives → ack platform → enqueue Task (payload = PlatformEvent):
    → worker pool consumes → call agent SSE → send to platform via platform outbound API
```

**Pros:** tasks survive pod crashes (Redis-backed); worker pool gives natural concurrency control; HITL checkpoints can pause for hours without blocking a goroutine; multi-replica HA works out of the box.

**Cons:** more moving parts, trace context must cross the queue boundary (use `StartLinkedSpan` per [ASYNC_ORCHESTRATION_GUIDE.md §8](../../docs/orchestration/ASYNC_ORCHESTRATION_GUIDE.md)).

**Use when:** agent runs may take minutes to hours, you run multiple replicas, HITL checkpoints may sit pending, or you need durability guarantees.

**Recommendation:** ship Pattern A first. Add Pattern B as an opt-in mode (`<GATEWAY>_ASYNC_MODE=queue`) when adopters hit the limits of A.

## 11. Step 6: HITL Bridging (Optional)

If the target agent uses HITL approval checkpoints, the gateway must surface the prompt in the platform and route the response to `/hitl/command`.

**Shape of the bridge:**

1. SSE consumer detects a `checkpoint.paused` event with a `checkpoint_id` and a human-readable prompt.
2. Response writer renders a **platform-native approval UI**:
   - Slack: Block Kit message with Approve / Reject buttons (or numbered options).
   - WhatsApp: text prompt + "reply YES or NO".
   - SMS: same as WhatsApp.
   - Email: reply-with-keyword or a signed action link.
3. Platform delivers the user's response as a new event (interactivity webhook for Slack buttons, plain message for keyword replies).
4. Gateway recognizes it as a HITL reply (based on checkpoint_id stored in Redis DB 6 or in the Block Kit payload) and `POST`s to `<AGENT>/hitl/command` with the decision.
5. Agent resumes, SSE continues, gateway continues normal message flow.

**Key detail:** HITL checkpoints can sit pending for hours. If using Pattern A (goroutine), the checkpoint waits block a goroutine indefinitely — not a crisis for low volume, but this is the strongest argument for Pattern B.

## 12. Step 7: Main Entry Point

The `main.go` pattern largely mirrors [TOOL_DEVELOPMENT_GUIDE.md §7](../../docs/building/TOOL_DEVELOPMENT_GUIDE.md) for the **shared** parts (config validation, telemetry init, graceful shutdown, signal handling). What differs:

- **Component type:** call `core.SetCurrentComponentType(core.ComponentTypeGateway)` if/when the framework adds this enum; otherwise use `ComponentTypeAgent` or a custom label — flag this as a future framework enhancement.
- **Framework wiring:** use `core.NewFramework` only if you need its HTTP server, middleware, and graceful shutdown. You likely want:
  - `WithPort(...)` — for health checks and Events API webhooks
  - `WithMiddleware(telemetry.TracingMiddleware(...))` — for observability
  - `WithDiscovery(false)` or omit — gateways do not register
  - Skip `WithRedisURL(...)` at framework level; open your own Redis client for session/dedup storage if needed
- **Listener startup:** start the platform listener (WebSocket connection, IMAP poll loop, etc.) as a separate goroutine or registered `framework.RegisterRunnable(...)` so graceful shutdown can stop it cleanly.
- **Health checks:** expose `/health` and `/ready`. `/ready` should reflect the platform connection state — if the Slack WebSocket is disconnected, the pod is not ready to receive traffic.

Minimum env vars (adapt per platform):

| Variable | Purpose | Required |
|---|---|---|
| `TRUVAG3_TARGET_AGENT_URL` | Base URL of the agent to forward to | Yes |
| `REDIS_URL` | Dedup cache, session aliases, task queue (if Pattern B) | Usually |
| `PORT` | HTTP port for health + webhooks | Yes |
| `<PLATFORM>_AUTH_TOKEN` | Platform API credentials | Yes |
| `<GATEWAY>_ASYNC_MODE` | `goroutine` (default) or `queue` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Tracing collector | No |
| `TRUVAG3_LOG_LEVEL`, `TRUVAG3_LOG_FORMAT` | Logging | No |

## 13. Deployment Considerations

### Single-replica vs horizontal

- **Persistent-connection gateways** (Slack Socket Mode, Discord WebSocket, IMAP IDLE): usually **single-replica** per deployment. A second replica either double-consumes events or sits idle. If you need HA, use a leader-election primitive (Kubernetes lease) and make the follower a warm standby.
- **Webhook-only gateways** (Slack Events API, Twilio webhooks, Stripe webhooks): scale horizontally behind a standard Service/Ingress. Events arrive on any replica; dedup handles retries.
- **Hybrid gateways** (support both transports): pick a primary transport per deployment via env var; don't try to do both in the same process.

### Ingress

Webhook-based gateways need a **public HTTPS URL**:
- Production: your existing ingress controller + DNS + TLS cert.
- Dev: `ngrok` or `cloudflared` tunneling to localhost.

Note any existing ingress scope rules your framework documents — in TruvaG3's case, ingress is reserved for agents and infra UIs, so adding a gateway to the ingress list is a conscious policy decision.

### Secrets

Platform tokens should be **Kubernetes Secrets**, not ConfigMap entries. Rotate them regularly; Slack app tokens, Twilio auth tokens, and webhook signing secrets all leak the same way plain API keys do.

## 14. Observability

Reuse the framework's telemetry module — same patterns as Tools and Agents:

- `telemetry.TracingMiddleware` on the HTTP handler (for webhook-based gateways)
- `telemetry.StartSpan` per inbound event
- `telemetry.NewTracedHTTPClientWithTransport` for outbound calls to the agent
- `StartLinkedSpan` across the task-queue boundary if using Pattern B

**Useful metrics (declare via `telemetry.DeclareMetrics`):**
- `gateway_events_received_total{platform, event_type}`
- `gateway_agent_calls_total{agent, status}`
- `gateway_responses_sent_total{platform, status}`
- `gateway_event_duration_seconds{platform, event_type}` (histogram)
- `gateway_hitl_checkpoints_total{agent, decision}`
- `gateway_dedup_skipped_total{platform}` (counter)

**Structured log fields:**
- `gateway.platform` — "slack" / "whatsapp" / "email"
- `gateway.event_id` — for correlation with platform logs
- `gateway.user_id`, `gateway.channel_id`, `gateway.session_id`
- `gateway.agent_url`
- `gateway.async_mode` — "goroutine" / "queue"

## 15. Authorization (What Adopters Must Add)

**This is the single most important thing the reference implementation should NOT do for you.**

Platforms authenticate *who sent the event* (signing secret, token). They do **not** tell you *whether that person is allowed to talk to your agent*. Adopters must add:

- **Allowlist of platform user IDs** (for internal bots)
- **IdP lookup** (Okta, Google Workspace) from platform identity → internal employee record → role check
- **Per-agent authorization** ("only on-call engineers can DM the devops agent")
- **Rate limits** (per-user, per-channel) to prevent abuse
- **Content filtering** (PII detection, sensitive keywords) if regulatory

The reference gateway should pass rich metadata (platform user, email if available, channel type) to the agent so the agent can also make authorization decisions if it wants to. But the **first gate** belongs in the gateway. Call this out prominently in the README.

## 16. Testing

Gateway testing differs substantially from Tool testing:

| Layer | Tool test | Gateway test |
|---|---|---|
| Inbound | HTTP POST fixture → assert response JSON | Platform event fixture (e.g., Slack event payload) → assert platform outbound call |
| External | Mock one REST API | Mock **two** sides: platform SDK + agent SSE stream |
| State | Usually stateless | Session derivation determinism, dedup correctness, HITL state machine |
| Contract | JSON schema in `InputSummary` | Platform event schema (shape you can't change) + agent chat stream schema (shape you depend on) |

**Test types to cover:**
- Unit: session_id derivation (same user → same session; different tenant → different session).
- Unit: event filter (bot messages skipped, edits skipped, empty messages skipped).
- Unit: dedup (same event_id twice → second is no-op).
- Integration: full event → agent SSE mock → platform outbound mock, assert message content and channel.
- Integration: HITL flow — pause event from agent → platform interactivity event → resume command to agent.
- Chaos: agent returns 500, agent times out mid-stream, platform disconnects WebSocket — assert gateway recovers without leaking goroutines.

## 17. Best Practices

- **Do one platform per gateway.** Do not build "messaging-gateway" that speaks Slack and WhatsApp and Teams. Each platform has enough quirks that abstractions will leak.
- **Do not modify the agent for gateway concerns.** If you find yourself patching the agent to add a Slack-specific field, step back — the abstraction is the `/chat/stream` contract, and it should stay platform-agnostic.
- **Do map sessions deterministically** unless you have a specific reason to store the mapping.
- **Do dedup aggressively.** Platforms retry; duplicate agent runs are worse than a dropped message.
- **Do ack within the platform deadline.** If you can't, the platform will retry and your goroutine will spawn a second identical agent run.
- **Do not buffer responses indefinitely.** Cap response timeout; if the agent hasn't produced a `final` in N seconds, post what you have with a "(response truncated)" note.
- **Do log the event_id and session_id on every log line.** When a user complains, you need to reconstruct the full trace.
- **Do not expose gateway internals as a TruvaG3 capability.** The gateway is not in the mesh; adding a `/api/capabilities/*` endpoint here would be a category error.

## 18. What This Guide Does Not Cover

- Platform-specific setup (Slack app manifest, Twilio number provisioning, Teams app registration) — belongs in each gateway example's README.
- Token rotation and secret management — deployment-specific.
- Multi-tenant gateways (one gateway serving many Slack workspaces) — non-goal for the reference; adopters build this themselves.
- Translation / i18n — adopters handle.
- Outbound-only patterns (agent posts to Slack without receiving DMs) — that's a [Tool](../../docs/building/TOOL_DEVELOPMENT_GUIDE.md), not a gateway.

## 19. References

- [TOOL_DEVELOPMENT_GUIDE.md](../../docs/building/TOOL_DEVELOPMENT_GUIDE.md) — shared infrastructure patterns (telemetry, config, deployment scaffolding)
- [ASYNC_ORCHESTRATION_GUIDE.md](../../docs/orchestration/ASYNC_ORCHESTRATION_GUIDE.md) — Pattern B task queue reference; HITL + async combination
- [DISTRIBUTED_TRACING_GUIDE.md](../../docs/observability/DISTRIBUTED_TRACING_GUIDE.md) — trace context propagation, `StartLinkedSpan` for cross-boundary spans
- [FRAMEWORK_DESIGN_PRINCIPLES.md](../../FRAMEWORK_DESIGN_PRINCIPLES.md) — why gateways are reference implementations, not framework modules
- [slack-gateway/PLAN.md](./PLAN.md) — concrete first gateway implementation
- [chat-ui](../chat-ui) — closest architectural sibling (client of agents, same non-mesh category)
- [slack-tool](../slack-tool) — outbound Slack counterpart; complements but does not replace the gateway

---

**Next steps for this draft:**
- Validate patterns against the first full Slack Gateway implementation (Phase 1 of [PLAN.md](./PLAN.md)).
- Once a second gateway (WhatsApp or Teams) ships, extract any genuinely duplicated code into a small `truvag3/gateway` helper package and promote this guide to `docs/`.
- Consider whether `core` should gain a `ComponentTypeGateway` enum for telemetry labeling — low priority until multiple gateways exist.
