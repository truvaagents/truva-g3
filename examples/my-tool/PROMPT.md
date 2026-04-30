# PROMPT — Build a TruvaG3 Tool (step-by-step)

This guide walks you through building a TruvaG3 tool with a coding agent.
Each step below is a **self-contained prompt** — paste it to your coding
agent, wait for the work to complete, review it, then move to the next.

The agent accumulates findings in `plan.md` (you'll have it create that in
Step 2) and source files as it progresses. You stay in the loop between
steps so you can course-correct early instead of late.

> **API-backed tools** follow all 12 steps as written.
> **Stdlib-only tools** (no external HTTP API): in Step 1 substitute
> *"explore the Go stdlib packages you'll wrap"* for the API exploration,
> and skip the API-key bits in `.env.example`. Steps 2–12 apply unchanged.

Reference example for this template: [`examples/stock-market-tool/`](../stock-market-tool/)
(API-backed) or [`examples/system-utilities-tool/`](../system-utilities-tool/)
(stdlib-only).

---

## Step 1 — Explore the API

Before pasting: replace `<DOMAIN>` with what you want this tool to do,
`<API_DOCS_URL>` with the official docs link, and `<API_KEY>` with a real
test/dev key (or remove that line if the API needs no key).

```
We're going to build a TruvaG3 tool. Domain: <DOMAIN>.

Step 1 of 12: explore the external API this tool will wrap.

- Read the official documentation here: <API_DOCS_URL>
- Test API key (use only for verification, do not commit anywhere): <API_KEY>
- For each operation we might want to expose as a capability, find out:
  * HTTP method + URL pattern (path/query params)
  * Authentication scheme (header? query string? bearer? per-request signature?)
  * Required vs optional request fields, with types and constraints
  * Success response shape (status code + body schema)
  * Error response shape (which status codes, error body schema)
  * Rate-limit headers, pagination shape, retry semantics
- If anything is ambiguous in the docs, make real `curl` calls against the
  live API and capture the raw request and response.

Do not write any Go code yet. When you're done exploring, summarize the
endpoints you'd expose as capabilities and pause for my review.
```

**What to expect:** the agent has read the docs, possibly made test calls,
and presents a list of capabilities ↔ endpoints. Confirm you agree with
the scope before moving on.

---

## Step 2 — Capture findings in `plan.md`

```
Step 2 of 12: write everything you learned in Step 1 to a new file
`plan.md` in this folder.

Structure plan.md as:

# Plan: <tool-name>

## Overview
- One paragraph: what this tool does and why an agent would call it.

## Capabilities
For each capability we'll expose, a section like:

### <capability_name>
- **Endpoint:** <METHOD> <URL>
- **Auth:** <scheme>
- **Request:**
  - field_a (string, required) — description
  - field_b (number, optional) — description
- **Response (200):**
  ```json
  { ... shape ... }
  ```
- **Errors:**
  - 4xx: <when, what body>
  - 5xx: <when, what body>
- **Sample curl:**
  ```bash
  curl ...
  ```
- **Sample response:**
  ```json
  { ... real captured output ... }
  ```

## Service identity
- Tool name (used in core.NewTool, k8 image, ConfigMap, OTEL_SERVICE_NAME): <name>

## Required env vars
- KEY_NAME — purpose, source

Pause for my review before writing any Go code.
```

**What to expect:** a `plan.md` file you can read top-to-bottom. Verify
the capability list matches your intent and the request/response shapes
match the API docs. **Code that comes later flows directly from this
file** — get it right before approving.

---

## Step 3 — Read the Tool Development Guide

```
Step 3 of 12: read `docs/TOOL_DEVELOPMENT_GUIDE.md` end-to-end.

Pay particular attention to:
- §3 Tool struct shape
- §4 External API client with distributed tracing
- §5 Capability registration — the 3-phase AI payload generation and
  InputSummary / OutputSummary shape
- §6 Handler implementation pattern
- §7 Main entry point
- §10 Best practices, especially "Common Anti-Pattern: Data Extraction
  Capabilities"

After reading, summarize in 5–10 bullets the patterns from this guide that
will apply to our tool, given the plan in plan.md. Do not write code yet.
```

**What to expect:** a short summary that names the patterns the agent will
use (traced HTTP client, capability registration shape, handler error
mapping, observability identity, etc.). If the summary feels generic,
push back and ask for guide-specific call-outs.

---

## Step 4 — Study the reference example

```
Step 4 of 12: read `examples/stock-market-tool/` end-to-end.

This is the canonical reference for an API-backed tool — same pattern we
need. Read every Go file (main.go, stock_tool.go, finnhub_client.go,
handlers.go), plus setup.sh, k8-deployment.yaml, Dockerfile.workspace,
and .env.example.

After reading, list:
- File layout we'll mirror
- Where API client lives and how tracing is wired in
- How capabilities are registered (Description, InputSummary,
  OutputSummary patterns)
- Anything in stock-market-tool's setup that we should keep identical
- Anything in stock-market-tool that's specific to Finnhub and we'll
  replace with our API

Do not write code yet.
```

**What to expect:** a concrete mapping from stock-market-tool's structure
to what the agent plans to write. If the agent says "we'll figure it out
when coding", push back — it should commit to a layout now.

---

## Step 5 — Implement

```
Step 5 of 12: implement the tool.

- Mirror examples/stock-market-tool/ file layout: main.go, <tool>_tool.go,
  <service>_client.go, handlers.go.
- Use the deployment skeleton already in this folder (setup.sh,
  k8-deployment.yaml, Dockerfile.workspace, Dockerfile, .env.example,
  go.mod, README.md). Rename "my-tool" everywhere to the actual tool name
  from plan.md. Keep port 8390 for now — Step 10 will replace it with
  the registry-allocated port.
- Every capability gets Description + InputSummary + OutputSummary that
  match plan.md exactly.
- The HTTP client must use otelhttp.NewTransport for distributed tracing.
- Use core.WithCORS([]string{"*"}, true) — this is server-to-server, not
  browser-facing.
- After writing all files, run:
  GOWORK=off go build ./...
  go build ./...

Stop and report when both builds pass. Do not deploy yet.
```

**What to expect:** clean compiles in both workspace and standalone modes.
If errors, debug with the agent before moving on.

---

## Step 6 — Review against `TOOL_DEVELOPMENT_GUIDE.md`

```
Step 6 of 12: review the implementation against docs/TOOL_DEVELOPMENT_GUIDE.md.

Go through each numbered section of the guide and check:
- Does our implementation follow it?
- Are there deviations? If so, are they justified?
- Are we missing anything the guide treats as required?

Report findings as a checklist:
  ✓ §3 Tool struct: <how we comply>
  ⚠ §4 External API client: <deviation + reason or fix>
  ...

Fix any non-justified deviations before proceeding.
```

**What to expect:** a section-by-section pass. If everything is ✓, ask
the agent to push back on its own work — guides are dense and a
genuinely-clean impl is rare on first pass.

---

## Step 7 — Vet against `TOOL_SCHEMA_DISCOVERY_GUIDE.md`

```
Step 7 of 12: vet the implementation against docs/TOOL_SCHEMA_DISCOVERY_GUIDE.md.

Verify each capability adheres to the 3-Phase Progressive Enhancement
model (the guide's actual framing):
- Phase 1: clear, AI-orchestrator-friendly Description (always present)
- Phase 2: complete InputSummary with FieldHint examples + OutputSummary
  (recommended for all tools — ~95% AI payload accuracy)
- Phase 3: SchemaEndpoint resolves and returns valid JSON Schema
  (auto-generated when InputSummary is provided; required for complex
  nested structures)

Report any gaps as a checklist and fix them.
```

**What to expect:** capability-by-capability schema audit. AI orchestrators
rely on this — gaps here mean the LLM can't generate good payloads.

---

## Step 8 — Vet against `DISTRIBUTED_TRACING_GUIDE.md`

```
Step 8 of 12: vet the implementation against docs/DISTRIBUTED_TRACING_GUIDE.md.

Verify:
- Every external HTTP call goes through otelhttp.Transport (or equivalent)
- r.Context() is propagated end-to-end (handler → client → upstream)
- Span names follow the convention in the guide
- Span attributes include the required fields (capability, service, etc.)
- No span leaks (every span ends; defer span.End() where appropriate)

Report findings and fix gaps.
```

**What to expect:** trace propagation is easy to get subtly wrong (a single
unwrapped client call breaks the chain). Don't accept "looks fine" — ask
the agent to point at the exact lines.

---

## Step 9 — Vet against `LOGGING_IMPLEMENTATION_GUIDE.md`

```
Step 9 of 12: vet the implementation against docs/LOGGING_IMPLEMENTATION_GUIDE.md.

Verify:
- Logs use the framework's logger (not standard log/fmt.Println)
- JSON format in production
- Standard fields present: request_id, trace_id, capability, service
- Log levels used correctly (debug for verbose path data, info for
  significant events, warn for recoverable problems, error for failures)
- No sensitive data in logs (API keys, full request bodies with PII)

Report findings and fix gaps.
```

**What to expect:** consistent structured logs that join Jaeger spans in
Grafana via the trace_id field.

---

## Step 10 — Allocate a port from the registry

```
Step 10 of 12: allocate a port for this tool.

1. Open examples/README.md and find the port-allocation table (search for
   "Port allocation:" or the "Host Port | NodePort | Type" header).
2. Find the HIGHEST port currently allocated to any tool/agent/UI in the
   table.
3. Pick the next port number (highest + 1) — NOT the lowest free port.
   If the highest is 8410, pick 8411. NodePort follows the pattern
   3<lastfourdigits>, so port 8411 → NodePort 30411.
4. Add a new row to the table for this tool with the chosen port and
   NodePort. This blocks the port for future allocations.
5. Update every place in this folder that references the placeholder
   port 8390:
   - setup.sh (PORT=${PORT:-...})
   - k8-deployment.yaml (containerPort, env PORT, livenessProbe.port,
     readinessProbe.port, Service targetPort)
   - Dockerfile and Dockerfile.workspace (ENV PORT=...)
   - .env.example (PORT=...)
   - README.md if it references the port

Report the chosen port + NodePort so I can confirm.
```

**What to expect:** sequential, registry-tracked allocation that won't
collide with other examples.

---

## Step 11 — Final review against `stock-market-tool` for deviations

```
Step 11 of 12: do a final side-by-side review against
examples/stock-market-tool/.

For every file we wrote (main.go, <tool>_tool.go, <service>_client.go,
handlers.go, setup.sh, k8-deployment.yaml, Dockerfile.workspace,
Dockerfile, go.mod, .env.example, README.md), compare to the equivalent
in stock-market-tool and list every deliberate or accidental deviation.

For each deviation, decide: keep it (with a one-line justification) or
revert to the reference pattern.

Aim for "as similar as possible to stock-market-tool, except where the
domain forces a difference."
```

**What to expect:** any drift from the canonical reference is named and
justified. If lots of deviations are unjustified, revert and retry.

---

## Step 12 — Deploy and verify

```
Step 12 of 12: deploy into the local Kind cluster and verify.

1. Make sure the cluster + infra are up (a previous example's full-deploy
   should have done this; if not, run an example like
   travel-chat-agent's `./setup.sh full-deploy`).
2. From this folder: `./setup.sh deploy`
3. Wait for the rollout to complete.
4. Verify the pod is Running 1/1:
     kubectl get pod -n truvag3-examples -l app=<tool-name>
5. Verify the tool is registered in the framework's service registry —
   open http://registry.localhost in a browser and confirm <tool-name>
   appears with all its capabilities and schemas.
6. Verify each capability responds with real data:
     curl -s -X POST http://<tool-name>-service.truvag3-examples/api/capabilities/<cap_name> \
       -H "Content-Type: application/json" -d '<sample request from plan.md>'
   (Use kubectl port-forward if accessing from your host:
     kubectl port-forward -n truvag3-examples svc/<tool-name>-service <local>:80)
7. Open http://jaeger.localhost and confirm a trace shows up for the call
   with spans: inbound HTTP → handler → external API call → response.
8. Open http://grafana.localhost (admin/admin) → Explore → Loki, query
   {app="<tool-name>"} and confirm logs are JSON with the standard fields
   (request_id, trace_id, capability, service).

Report what you saw at each verification step. If anything is missing or
broken, fix it and re-deploy with `./setup.sh rollout` (config-only
changes) or `./setup.sh rebuild` (code changes).
```

**What to expect:** a clean end-to-end verification — pod running,
registry shows it, capability returns real data, traces present, logs
structured. If any step fails, iterate before declaring the tool done.

---

## Done

Your tool is ready. Drop the agent's `plan.md` into source control if you
want a record of the API contract decisions, or delete it if it's purely
working memory.
