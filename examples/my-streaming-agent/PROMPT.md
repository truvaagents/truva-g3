# PROMPT — Build a Truva-G3 Streaming Agent (step-by-step)

This guide walks you through building a streaming chat agent (SSE-based)
with a coding agent. Each step is a **self-contained prompt** — paste it
to your coding agent, wait for the work to complete, review it, then move
to the next.

The agent accumulates findings in `plan.md` (created in Step 2) and
source files as it progresses. You stay in the loop between steps so you
can course-correct early.

**Reference examples:**

- Canonical streaming chat agent: [`examples/travel-chat-agent/`](../travel-chat-agent/) — multi-tool orchestration over SSE, internal-only capabilities, chat-ui front-end.
- Streaming + public capability (agent-as-tool): [`examples/devops-chat-agent/`](../devops-chat-agent/) — same pattern PLUS exposes `devops_operations` as a public `Internal:false` capability so other agents can delegate to it.

If your agent should also be callable by other agents (an *agent-as-tool*),
read both. Otherwise, travel-chat-agent is enough.

---

## Step 1 — Define the agent's domain and tool dependencies

Before pasting: replace `<DOMAIN>` with what this chat agent helps users
do, list the existing tools in the cluster it will orchestrate, and
declare whether it needs to be callable by other agents.

```
We're going to build a Truva-G3 streaming chat agent. Domain: <DOMAIN>.

Step 1 of 12: define the agent's responsibilities and dependencies.

- What problem does this agent solve for an end user? Describe the
  conversation patterns it should support (greeting, follow-ups,
  clarification, structured queries).
- Which tools in the existing cluster will this agent orchestrate?
  Run `curl -s http://travel-chat-agent.localhost/discover | jq '.tools[].name'`
  to see what's deployed. Pick the ones relevant to <DOMAIN>; if a needed
  tool doesn't exist, note it as "must be built first" — building tools
  is a separate workflow (see examples/my-tool/PROMPT.md).
- Does this agent need to be callable by OTHER agents (agent-as-tool)?
  - If yes: study examples/devops-chat-agent/'s `devops_operations`
    capability (Internal:false, Type:CapabilityOrchestrator). One public
    capability per agent is the convention.
  - If no: all capabilities are Internal:true (they're for the chat-ui only).

Do not write any Go code yet. Summarize what you found and pause for my
review.
```

**What to expect:** a clear scope — the user-facing problem, the tools
this agent will discover and call, and the public-vs-internal capability
decision.

---

## Step 2 — Capture findings in `plan.md`

```
Step 2 of 12: write everything from Step 1 to a new file `plan.md` in
this folder.

Structure:

# Plan: <agent-name>

## Overview
- One paragraph: what this agent helps users do, and the conversation flow.

## Tool dependencies
| Tool | Capability used | What we get from it |
|------|-----------------|---------------------|
| weather-tool-v2 | get_current_weather | current temp + conditions for a location |
| ...

For any tool that does NOT yet exist in the cluster, mark it as
"REQUIRED — must be built first" and stop the workflow until it's
deployed.

## Public capability (agent-as-tool)?
- [ ] Yes — exposes <capability_name> with Internal:false
- [x] No — all capabilities Internal:true (humans-only via chat-ui)

If yes:
- Capability name: <name>
- Description (this is what other agents' orchestrators see):
- Endpoint: /<path>
- Type: core.CapabilityOrchestrator
- InputSummary: required + optional fields
- OutputSummary: response shape

## Session lifecycle
- TTL: <e.g., 24h>
- What's stored per session (user_id, conversation history, scratch state)

## SSE event protocol
The canonical reference (travel-chat-agent/sse_handler.go) emits these
events:

- `event: session`  data: {session_id, ...}        # session created (one-shot)
- `event: status`   data: {message, ...}           # high-level status, e.g. "Analyzing your request..."
- `event: step`     data: {name, status, ...}      # orchestration step lifecycle
- `event: chunk`    data: {text}                   # streaming text token (most frequent)
- `event: usage`    data: {prompt_tokens, ...}     # token accounting
- `event: finish`   data: {reason, ...}            # finish reason from the LLM
- `event: done`     data: {}                       # stream complete (always last)
- `event: error`    data: {error}                  # something failed (terminal)

If your UI needs a different protocol, declare your event set here
explicitly so the implementation matches and the chat-ui can be wired up
to it. Stick to the canonical set above unless you have a reason not to.

## Service identity
- Agent name (used in core.NewBaseAgent, k8 image, ConfigMap, OTEL_SERVICE_NAME): <name>
- Required env vars: REDIS_URL, NAMESPACE, PORT, OPENAI_API_KEY (or other provider), …

Pause for my review before writing any Go code.
```

**What to expect:** a concrete `plan.md` describing tool dependencies,
session model, SSE event contract, and public-capability decision. The
implementation flows directly from this file.

---

## Step 3 — Read the Agent Development Guide

```
Step 3 of 12: read `docs/AGENT_DEVELOPMENT_GUIDE.md` end-to-end, focused
on the Streaming Agent path.

Pay particular attention to:
- §1 Understanding Agents in Truva-G3 — agent vs tool distinction
- §3 Project Structure (focus on the "Streaming Agent (Chat)" subsection)
- §4 Step 1: Create the Agent Struct (focus on the "Streaming Chat Agent"
  subsection)
- §5 Step 2: Configure the Orchestrator (InitializeOrchestrator method
  and PromptConfig — this is where the AI orchestrator is wired in)
- §6 Step 3: Register Capabilities — especially "Why `Internal: true` is
  Critical" and "When to Use `Internal: false` (Agent-as-Tool)" if
  plan.md said we expose a public capability
- §8 Step 5: Add SSE Streaming — StreamCallback, SSECallback, SSE
  handler, SSE event protocol
- §9 Step 6: Add Session Management — Session/Message types, SessionStore
- §10 Step 7: Create the Main Entry Point
- §12 Logging and Observability
- §13 Distributed Tracing

Summarize in 8–12 bullets the patterns from this guide that apply to our
agent given plan.md. Do not write code yet.
```

**What to expect:** a concrete summary that names the patterns the agent
will use (StreamCallback wiring, session store, orchestrator init, SSE
event emission). If the summary is generic, push back and ask for
guide-specific call-outs.

---

## Step 4 — Study the reference example(s)

Adapt this prompt based on whether your agent exposes a public capability
(plan.md decision):

```
Step 4 of 12: read examples/travel-chat-agent/ end-to-end.

Files to read in order: main.go, chat_agent.go, sse_handler.go,
session.go, handlers.go, setup.sh, k8-deployment.yaml,
Dockerfile.workspace, .env.example, README.md.

[ONLY IF plan.md said this agent exposes a public capability:]
ALSO read examples/devops-chat-agent/ — specifically how the
devops_operations capability is registered (chat_agent.go around line
807, with the comment "Internal: false (omitted) so other agents'
orchestrators can discover and call this"). The key idea: ONE public
orchestrator capability + several Internal:true endpoints for the
chat-ui only.

After reading, list:
- File layout we'll mirror
- Where the orchestrator is initialized
- How the SSE handler is structured (parsing request, emitting events,
  closing on completion or error)
- How session state is loaded, mutated, persisted
- How the agent registers its capabilities (which Internal:true, which
  Internal:false)
- Anything specific to travel-chat-agent (or devops-chat-agent) that's
  domain-specific and we'll replace

Do not write code yet.
```

**What to expect:** a concrete mapping from the reference's structure to
the new agent. The agent should commit to a file layout now.

---

## Step 5 — Implement

```
Step 5 of 12: implement the agent.

- Mirror examples/travel-chat-agent/ file layout: main.go, chat_agent.go
  (or <domain>_agent.go), sse_handler.go, session.go, handlers.go.
- Use the deployment skeleton already in this folder (setup.sh,
  k8-deployment.yaml, Dockerfile.workspace, Dockerfile, .env.example,
  go.mod, README.md). Rename "my-streaming-agent" everywhere to the
  actual agent name from plan.md. Keep port 8391 for now — Step 10 will
  replace it with the registry-allocated port.
- Use core.WithCORSDefaults() (NOT WithCORS) so the browser chat-ui can
  call /chat/stream — see GETTING_STARTED.md §5 "CORS choice".
- Capability registration follows plan.md exactly:
  - chat_stream / create_session / get_history etc. → Internal:true
  - The public agent-as-tool capability (if any) → Internal omitted
    (false), Type:core.CapabilityOrchestrator
- The orchestrator must use the AI client; tool discovery is via
  agent.Discovery.Discover().
- After writing all files, run:
  GOWORK=off go build ./...
  go build ./...

Stop and report when both builds pass. Do not deploy yet.
```

**What to expect:** clean compiles in both modes. If errors, debug with
the agent before moving on.

---

## Step 6 — Review against `AGENT_DEVELOPMENT_GUIDE.md`

```
Step 6 of 12: review the implementation against docs/AGENT_DEVELOPMENT_GUIDE.md.

Go through each numbered section that applies to streaming agents (§1, 3,
4, 5, 6, 8, 9, 10, 12, 13) and check:
- Does our implementation follow the guide?
- Are there deviations? If so, are they justified?
- Are we missing anything the guide treats as required?

Report findings as a checklist:
  ✓ §3 Project Structure: <how we comply>
  ⚠ §6 Capability registration: <deviation + reason or fix>
  ...

Fix any non-justified deviations before proceeding.
```

**What to expect:** a section-by-section pass. If everything is ✓, ask
the agent to push back on its own work.

---

## Step 7 — Vet against `TOOL_SCHEMA_DISCOVERY_GUIDE.md` (only if exposing a public capability)

```
Step 7 of 12: vet the public capability against
docs/TOOL_SCHEMA_DISCOVERY_GUIDE.md.

[Only if plan.md decided this agent exposes a public capability for
other agents to call.]

Verify the public capability adheres to the 3-Phase Progressive
Enhancement model (the guide's actual framing):
- Phase 1: clear Description that other agents' AI orchestrators can
  understand at first glance
- Phase 2: complete InputSummary with FieldHint examples + OutputSummary
- Phase 3: SchemaEndpoint resolves and returns valid JSON Schema

Report any gaps and fix them.

[If your agent has no public capability, skip this step — say so and
move to Step 8.]
```

**What to expect:** capability-level schema audit (or a clean skip if
the agent is humans-only via chat-ui).

---

## Step 8 — Vet against `DISTRIBUTED_TRACING_GUIDE.md`

```
Step 8 of 12: vet the implementation against docs/DISTRIBUTED_TRACING_GUIDE.md.

Verify:
- Inbound HTTP requests have a trace context (auto-injected by the
  framework's TracingMiddleware)
- The orchestrator's tool calls propagate context — every outbound
  HTTP call uses otelhttp.NewTransport
- Span names follow the convention in the guide
- Span attributes include capability, service, session_id, request_id
- The SSE stream's lifecycle is captured as a span (start when the
  request arrives, end when the stream closes)
- No orphan spans (every span ends; defer span.End() where appropriate)

Report findings and fix gaps. Test by deploying briefly and inspecting
Jaeger after a /chat/stream request.
```

**What to expect:** trace propagation correctness; span tree shows
inbound → orchestrator → each tool call → response.

---

## Step 9 — Vet against `LOGGING_IMPLEMENTATION_GUIDE.md`

```
Step 9 of 12: vet the implementation against docs/LOGGING_IMPLEMENTATION_GUIDE.md.

Verify:
- Logs use the framework's logger (not standard log/fmt.Println)
- JSON format in production
- Standard fields: request_id, trace_id, session_id, capability, service
- Log levels used correctly (debug for verbose, info for significant
  events like session create / tool call, warn for recoverable issues,
  error for failures)
- No sensitive data (API keys, full conversation bodies if PII risk)

Report findings and fix gaps.
```

**What to expect:** structured logs that join Jaeger spans in Grafana
via the trace_id field.

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
   port 8391:
   - setup.sh
   - k8-deployment.yaml (containerPort, env PORT, service targetPort)
   - Dockerfile and Dockerfile.workspace (ENV PORT)
   - .env.example (PORT=)
   - README.md if it references the port

Report the chosen port + NodePort so I can confirm.
```

**What to expect:** sequential, registry-tracked allocation.

---

## Step 11 — Final review against the reference for deviations

```
Step 11 of 12: do a final side-by-side review against the reference.

For EACH file we wrote, compare to its equivalent in
examples/travel-chat-agent/ (or examples/devops-chat-agent/ if we have a
public capability) and list every deliberate or accidental deviation.

For each deviation, decide: keep it (with a one-line justification) or
revert to the reference pattern.

Aim for "as similar as possible to the reference, except where the
domain forces a difference."
```

**What to expect:** drift from the canonical reference is named and
justified. If lots of deviations are unjustified, revert and retry.

---

## Step 12 — Deploy and verify

```
Step 12 of 12: deploy into the local Kind cluster and verify.

1. Cluster + infra must be up. If they aren't, run an example's full-deploy
   first (e.g., examples/travel-chat-agent/setup.sh full-deploy).
2. From this folder: `./setup.sh full-deploy` (cold-start) or
   `./setup.sh deploy` (cluster + infra already up).
3. Wait for rollout to complete.
4. Pod is Running 1/1:
     kubectl get pod -n truvag3-examples -l app=<agent-name>
5. Agent registered in Redis — open http://registry.localhost and
   confirm <agent-name> appears with all capabilities. If we exposed a
   public capability, it should be discoverable from other agents:
     curl -s http://travel-chat-agent.localhost/discover | jq '.agents[] | select(.name=="<agent-name>")'
6. CORS preflight allows the browser:
     curl -i -X OPTIONS http://<agent-name>.localhost/chat/stream \
       -H "Origin: http://chat.localhost" \
       -H "Access-Control-Request-Method: POST" \
       -H "Access-Control-Request-Headers: Content-Type, Accept, X-User-ID"
   Expect: Access-Control-Allow-Headers: *
7. Streaming end-to-end (curl):
     SESSION=$(curl -sS -X POST http://<agent-name>.localhost/chat/session | jq -r .session_id)
     curl -N -X POST http://<agent-name>.localhost/chat/stream \
       -H "Content-Type: application/json" \
       -d "{\"session_id\":\"$SESSION\",\"message\":\"<a representative test query>\"}"
   Expect: HTTP 200, text/event-stream, a sequence of `event: status`,
   `event: chunk`, `event: done` lines.
8. Browser flow:
   - Open http://chat.localhost
   - Click your agent's card on the dashboard (you may need to add a card
     to examples/chat-ui/dashboard.html — see how travel-chat-agent's
     card is wired)
   - Send a query, watch the stream render
9. Jaeger trace at http://jaeger.localhost shows:
   inbound → orchestrator → tool calls → response. All sharing one trace_id.
10. Logs at http://grafana.localhost (admin/admin) → Explore → Loki:
    {app="<agent-name>"} returns JSON logs with request_id, trace_id,
    session_id, capability, service.

Report what you saw at each step. If anything is missing or broken,
fix it and re-deploy with `./setup.sh rollout` (config-only changes) or
`./setup.sh rebuild` (code changes).
```

**What to expect:** clean end-to-end verification — pod running,
registry shows it, CORS works, SSE stream responds with real data,
traces present, logs structured. If any step fails, iterate before
declaring the agent done.

---

## Done

Your streaming agent is ready. Drop the agent's `plan.md` into source
control if you want a record of the design decisions, or delete it.
