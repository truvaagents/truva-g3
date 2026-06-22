# OpenClaw as a TruvaG3 Tool — Design & Analysis

> **Status:** ✅ **Working on Kind — the contained autonomous task-solver (`run_task`) is built
> and verified** (multi-step shell + file tasks, headless, sequential calls, plus the
> summarize/answer presets). ⚠️ **Open security gap: egress is wide open on Kind (kindnet
> ignores NetworkPolicy) — the agent reaches any external host; not for untrusted tasks until a
> real CNI enforces egress (confirmed — see §13 "KNOWN GAP").** See ⭐ **§13** for the design,
> the *validated* config, and the security posture; §13 supersedes the summarizer narrowing in
> §0/§4/§12.
> **Capability suite ✅:** `run_task` is joined by **11 typed real-world capabilities** (data,
> code, extract/secure) — all built, conformance-reviewed against the tool guide, and verified on
> Kind. See **§14** for the catalog, test results, and the deferred shared-filesystem step.
> **Goal:** Run [OpenClaw](https://github.com/openclaw/openclaw) as a TruvaG3 tool so a
> TruvaG3 agent can hand it a task and get one answer back — leveraging a "smart process"
> to solve tasks without writing another agent harness.

## Table of Contents

- [0. Framing: OpenClaw is a black-box transactional process](#0-framing-openclaw-is-a-black-box-transactional-process)
- [1. How TruvaG3 tools are built and run](#1-how-truvag3-tools-are-built-and-run)
- [2. What OpenClaw is, and the integration seam](#2-what-openclaw-is-and-the-integration-seam)
- [3. Architecture — adapter as the membrane](#3-architecture--adapter-as-the-membrane)
- [4. The transaction contract](#4-the-transaction-contract)
- [5. Why containment is non-negotiable (threat context)](#5-why-containment-is-non-negotiable-threat-context)
- [6. The cocoon — containment layers](#6-the-cocoon--containment-layers)
- [7. Concurrency — serialize at the adapter, scale with replicas](#7-concurrency--serialize-at-the-adapter-scale-with-replicas)
- [8. Statelessness by per-transaction reset](#8-statelessness-by-per-transaction-reset)
- [9. Resource footprint](#9-resource-footprint)
- [10. Open decisions before build](#10-open-decisions-before-build)
- [11. If we build it — file inventory](#11-if-we-build-it--file-inventory)
- [12. Real OpenClaw — verified contract & reconciliation (June 2026)](#12-real-openclaw--verified-contract--reconciliation-june-2026)
- [13. ⭐ TARGET DESIGN — autonomous task-solver (`run_task`), contained](#13--target-design--autonomous-task-solver-run_task-contained)
- [14. Capability expansion — from one `run_task` to a curated suite (June 2026)](#14-capability-expansion--from-one-run_task-to-a-curated-suite-june-2026)
- [Sources](#sources)

> **How to read this document.** This is a *living design record*, written chronologically as the
> tool evolved. **§13–§14 describe the current, authoritative tool** (the autonomous, contained
> task-solver and its capability suite); **§0–§12 are the design journey** — still valuable for the
> *why*, but partially superseded (each section notes where; e.g. §13 supersedes the summarizer
> framing in §0/§4/§12). For a practical, operational overview, start with the
> [README](README.md); read this for the rationale.

---

## 0. Framing: OpenClaw is a black-box transactional process

The guiding lens for this whole design: **treat OpenClaw as a process, not as an agent.**
We give it a request and a timeout, it works in its own cocoon, it returns a response. It
*happens* to use an LLM internally, but from our side the contract is a transaction:
request in → response out (or timeout). We are not orchestrating it, conversing with it,
or letting it discover anything. We give it the resources a task needs and nothing more.

Two consequences drive every decision below:

1. **No discovery / cocoon.** OpenClaw must not join the TruvaG3 mesh, must not reach into
   the cluster, and must not reach the network except for the one dependency a task
   genuinely needs (the LLM). The Go adapter is the only TruvaG3 component; OpenClaw lives
   behind it and knows nothing about the framework.
2. **Stateless across transactions.** Each request starts from a clean slate. OpenClaw gets
   **full memory freedom *while* working on a task**, and the workspace is reset between
   tasks — so nothing carries from one request to the next.

---

## 1. How TruvaG3 tools are built and run

A **tool** is a stateless HTTP microservice, not a library.

- Embed `*core.BaseTool` (`core/tool.go`) and register **Capabilities** —
  `{Name, Description, InputSummary, OutputSummary, Handler}`, where `Handler` is a plain
  `http.HandlerFunc`. Canonical example: `examples/news-tool/news_tool.go`.
- `main.go`: create the tool → `initTelemetry(...)` →
  `core.NewFramework(tool, core.WithName(...), core.WithPort(...), core.WithRedisURL(...), core.WithDiscovery(true,"redis"))`
  → `framework.Run(ctx)`.
- Tools **register** in Redis but **cannot discover** (Go-type-enforced: tools get
  `Registry`, agents get `Discovery`). Agents find tools by capability and call
  `POST /api/capabilities/<name>`, getting back `core.ToolResponse{Success, Data, Error}`.
- **Run in Kind** via each example's `setup.sh` (build → `kind load` → apply
  `k8-deployment.yaml` → wait rollout). Per `AGENTS.md`, **always drive examples through
  `./setup.sh`** — never hand-run docker/kind/kubectl.

The closest existing analogs: `examples/devops-tool/` (wraps `kubectl`) and
`examples/playwright-tool/` (wraps the Playwright/Node engine) — both are
"wrap an external engine behind capabilities." OpenClaw slots into the same role.

---

## 2. What OpenClaw is, and the integration seam

OpenClaw (formerly "Moltbot"/"Molty") is a **TypeScript/Node.js** assistant that runs as a
**Gateway daemon** (default `127.0.0.1:18789`), normally connecting messaging channels and
driving its own LLM. For our purposes we ignore all of that and use one thing: its
**OpenAI-compatible HTTP API** ([OpenResponses docs](https://docs.openclaw.ai/gateway/openresponses-http-api)).

- `POST /v1/responses` — body `{"model":"openclaw","input":"…"}`; headers
  `Authorization: Bearer <token>`, `x-openclaw-agent-id: <agent>`. **Non-streaming = one
  request, one JSON reply** — exactly the transaction we want.
- (`/v1/chat/completions` and `/tools/invoke` also exist; not needed for the base design.)

Caveats baked into the design:
- HTTP endpoints are **OFF by default** — must be enabled in `openclaw.json`.
- Gateway binds **loopback** by default (good — keep it that way).
- Needs **Node 22.19+/24** and its **own LLM key**, independent of any TruvaG3 agent key.

---

## 3. Architecture — adapter as the membrane

A thin Go adapter (`*core.BaseTool`) and the OpenClaw Gateway run as **two containers in one
pod** (sidecar), talking over `localhost:18789`. The adapter is the membrane: the only
TruvaG3 component, the only thing on the Service, and the controller of OpenClaw's
lifecycle and workspace.

```
┌─ Pod: openclaw-tool ─────────────────────────────────────┐
│  openclaw-adapter (Go, *core.BaseTool)   → registers in Redis
│      ↕ localhost:18789      ↕ shared workspace volume      │
│  openclaw gateway (node, :18789, loopback, LLM key)        │
└────────────────────────────────────────────────────────────┘
        ↑ ClusterIP Service :80  ← TruvaG3 agents call the adapter only
```

**Note on topology vs. network isolation:** the sidecar (one pod) keeps OpenClaw on
loopback and is simplest. If you want NetworkPolicy to distinguish OpenClaw's egress from
the adapter's, that requires **separate pods** (a policy is pod-wide; containers in one pod
share an IP). See §6.

### Relationship to the `playwright-tool` pattern

Same shape ("framework outer layer + engine underneath"), but two divergences:

| | playwright-tool | openclaw-tool |
|---|---|---|
| Inner engine | deterministic mechanism — does exactly what it's told | autonomous — its own LLM + tool loop |
| Where the LLM lives | the **outer** TruvaG3 agent | the **inner** engine (we treat it as a black box) |
| Coupling | engine bundled in image, run as **subprocess** (`exec.Command npx playwright`) | engine in a **separate, sandboxed container** |
| Trust | trusted (Microsoft) | **untrusted** (see §5) |

Keep the *pattern*; **do not** inherit playwright-tool's single-container subprocess
coupling — OpenClaw must be isolated across a container/pod boundary, not exec'd in-process.

---

## 4. The transaction contract

One synchronous capability, bounded by a timeout the adapter owns, serialized to one
in-flight request per pod (see §7).

```go
tool.RegisterCapability(core.Capability{
    Name:        "run_task",
    Description: "Hand a self-contained task to the OpenClaw smart-process and return its result.",
    InputTypes:  []string{"json"}, OutputTypes: []string{"json"},
    Handler:     t.handleRunTask,
    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{{Name:"task", Type:"string", Example:"research X and summarize"}},
        OptionalFields: []core.FieldHint{{Name:"timeout_seconds", Type:"number", Example:"120"}},
    },
})

func (t *Tool) handleRunTask(w http.ResponseWriter, r *http.Request) {
    // ... decode req ...
    t.sem <- struct{}{}; defer func(){ <-t.sem }()          // serialize: one task per pod (§7)
    if err := t.resetWorkspace(); err != nil { /* fail */ } // reset BEFORE (§8) — crash-safe
    ctx, cancel := context.WithTimeout(r.Context(), req.Timeout()); defer cancel()
    resp, err := t.post(ctx, "/v1/responses", payload{Model:"openclaw", Input:req.Task, Session:newSessionID()})
    _ = t.cleanupWorkspace()                                // best-effort wipe AFTER (§8)
    // ... map resp/err → core.ToolResponse (timeout → Retryable) ...
}
```

No streaming, no session reuse, no discovery. The timeout is the transaction bound; on
`DeadlineExceeded` the adapter returns a `core.ToolResponse` error and reclaims control.

---

## 5. Why containment is non-negotiable (threat context)

OpenClaw is **untrusted code we happen to run**:

- **CVE-2026-25253** (CVSS 8.8): one-click **RCE** via the Control UI; unauthenticated
  host-level code execution in <90s on unpatched deploys. Fixed in **2026.1.29**.
  ([Hacker News](https://thehackernews.com/2026/02/openclaw-bug-enables-one-click-remote.html))
- **Prompt injection**: skill-marketplace audit found a **36% injection rate**;
  indirect injection can silently rewrite the agent's memory. ([HiddenLayer](https://www.hiddenlayer.com/research/exploring-the-security-risks-of-ai-assistants-like-openclaw))
- **Default config is wide open**: arbitrary shell, full filesystem, network, env secrets.
  ([Nebius](https://nebius.com/blog/posts/openclaw-security))

Assume it *will* be injected and *may* be RCE'd. Containment must hold even then — never
rely on OpenClaw's own config to behave. The "cocoon" is both a security boundary and the
realization of "a process gets exactly the resources its job needs, no ambient authority."

---

## 6. The cocoon — containment layers

### Layer 1 — Shrink OpenClaw's own surface (config; reduces noise, not the boundary)
In `openclaw.json`: `exec.security="deny"`, browser off, **all channels off**, **Control
UI/dashboard not exposed** (the RCE vector), `bind:"loopback"`, token auth, only the
`responses` HTTP endpoint enabled, version pinned **≥ 2026.1.29**.

### Layer 2 — Pod/container hardening (the real boundary)
```yaml
automountServiceAccountToken: false          # no K8s API token to steal even after RCE
securityContext:                              # pod
  runAsNonRoot: true
  runAsUser: 10001
  seccompProfile: { type: RuntimeDefault }
containers:
- name: openclaw
  securityContext:
    allowPrivilegeEscalation: false
    readOnlyRootFilesystem: true              # writable bits via emptyDir only (§8)
    capabilities: { drop: ["ALL"] }
  resources: { requests: {cpu:"100m", memory:"512Mi"}, limits: {cpu:"1", memory:"1Gi"} }
```
Plus a **dedicated ServiceAccount with zero RBAC**, and **never mount the Docker socket**
(OpenClaw's own sandbox uses Docker; in-cluster we disable that and let the pod be the
sandbox — the socket would be an instant host escape).

### Layer 3 — Network egress: one hole, sized to the task
Default-deny egress; allow only what a task needs.

- **Reasoning-only tasks:** the only dependency is the LLM. Point OpenClaw's model
  `baseURL` at the **adapter** on localhost so the adapter is the sole internet egress —
  OpenClaw then has **zero direct egress** (the tightest cocoon).
- **Tasks needing web/exec:** that is a deliberately wider hole — allowlist those
  destinations explicitly; the cocoon grows to fit.

⚠️ **Kind caveat:** the default CNI (**kindnet) does NOT enforce NetworkPolicy** — policies
are silently ignored. Use **Cilium or Calico** in the cluster. Plain NetworkPolicy is
**IP/CIDR-based, not hostname-based**, so matching `api.anthropic.com` (rotating CDN IPs)
needs **Cilium `toFQDNs`**, an **egress proxy** with a hostname allowlist, or the
route-LLM-through-the-adapter trick above. For per-pod egress *granularity*, use the
**separate-pod** topology so the OpenClaw pod's policy is independent of the adapter's.

### Layer 4 — Kernel isolation (optional, strong)
Given the RCE history, run OpenClaw under **gVisor** (`RuntimeClass: gvisor`) so a container
escape hits gVisor's user-space kernel, not the host.

### Layer 5 — Don't expose it
The Service publishes **only the adapter**, never `:18789`. **No ingress host, no dashboard.**

---

## 7. Concurrency — serialize at the adapter, scale with replicas

**The tension with the framework's threading model.** The framework is multi-threaded: the
Go HTTP server admits every inbound request on its own goroutine — exactly what a normal tool
wants. But OpenClaw is the opposite of a normal tool. A *stateless* API wrapper (e.g. the
Finnhub-backed stock tool) can field unlimited concurrent goroutines because each upstream
call is independent and the client holds no per-request state. OpenClaw is **stateful and
single-tenant**: its "memory" is plain files in one shared workspace (§8). And we run **one
long-lived OpenClaw per pod** — we do *not* spawn an instance per request (that would cost
~1Gi and seconds of boot each, §9). So the framework's free-concurrency assumption does not
hold here, and the adapter has to put it back.

**Why unguarded concurrency corrupts it.** If two transactions reach the same OpenClaw at
once they collide on disk: A's `MEMORY.md`/section notes interleave with B's; the
pre-transaction reset for B (§8) **wipes A's working files mid-flight**; and the
memory-search index bleeds A's content into B's answers. Left unguarded, OpenClaw *will* be
confused — not because the model is weak but because two tasks are sharing one filesystem.

**The fix — serialize at the adapter, not inside OpenClaw.** The adapter holds a **semaphore
of 1**: every handler goroutine blocks on it before touching OpenClaw, so at most one
transaction is ever in flight and concurrent requests **queue** rather than interleave. The
whole transaction — `reset → POST /v1/responses → cleanup` — sits inside the semaphore, so a
reset can never race a live task. OpenClaw only ever sees *clean slate → one task → done*.
(Same bound already applied to the kubectl tool in commit `a91025b`.) This is the mental
model made literal: *a process that does one task at a time from a clean slate.*

```
many concurrent requests
        │  (Go goroutines, all admitted by the HTTP server)
        ▼
   ┌─ adapter ──────────────────────────┐
   │   sem(1) ──── queue ────            │   ← serialized HERE, not inside OpenClaw
   │      ▼                              │
   │   reset → OpenClaw → cleanup        │   ← exactly one task at a time
   └─────────────────────────────────────┘
```

**A fresh session id is necessary but NOT sufficient.** OpenClaw has two independent state
axes: chat/session context and the on-disk workspace. A new session id per call (§8) isolates
the *chat* axis only — it does **not** give two concurrent tasks separate *workspaces*; they
would still share `MEMORY.md` and the index. So only the semaphore (or separate pods)
delivers true isolation; the session id just rides along for the chat axis. This is why the
semaphore is load-bearing, and why we never rely on OpenClaw's gateway merely being *able* to
accept concurrent HTTP requests — that ability is the footgun, not the feature.

**Bound the wait — don't let a backlog pile up.** Because a large map-reduce summary can hold
the semaphore for many seconds, the adapter should **fail fast** when it can't acquire the
slot within a short budget: return `503` + `Retryable` rather than queueing unboundedly, so
callers back off instead of stacking latency. (This reuses the timeout→retryable mapping in
§4; a full queue and a slow task are both "come back later.")

**Scale by pods, not threads.** Within one pod, throughput is one task at a time — *by
design*. Real concurrency comes from **horizontal replicas**: each replica is its own pod =
its own adapter + its own OpenClaw + its own isolated workspace, load-balanced by the
ClusterIP Service. So **max concurrency = replica count**, and every slot is a clean,
fully-isolated OpenClaw — but each replica carries its own ~1Gi OpenClaw, so horizontal scale
is real cost (§9). Fine for a "smart process called occasionally." (The alternative — in-pod
per-session sandbox isolation — fights OpenClaw's agent-global `MEMORY.md` and is easy to get
subtly wrong; avoid unless measured need.)

---

## 8. Statelessness by per-transaction reset

OpenClaw's own docs state the key fact: *"the model only remembers what gets saved to disk
— there is no hidden state."* Memory is **plain files** in `~/.openclaw/workspace`
(`MEMORY.md`, `memory/YYYY-MM-DD.md`), plus the injected `AGENTS.md`/`SOUL.md`/`TOOLS.md`.
So **statelessness is a storage problem**, fully under the adapter's control.

**Design: full freedom during the task, guaranteed amnesia across tasks.**

- **Do NOT restrict memory during a task.** Memory tools stay **enabled**;
  `compaction.memoryFlush` stays **on**. OpenClaw thinks by writing markdown — read-only
  would fight its grain and could degrade or break tasks. We trade *spatial* lockdown for
  *temporal* isolation.
- **The guarantee is "reset *before* each transaction," not "clean up after."** After-only
  cleanup fails on crash/timeout/OOM, and **emptyDir survives container restarts** (it's
  wiped only when the pod leaves the node). A pre-reset is idempotent and crash-safe: no
  matter how the last request ended, the next starts from a known seed. Keep an after-wipe
  too (disk + shrinks residency of sensitive scratch), but the pre-reset is load-bearing.
- **Reset must wipe more than `MEMORY.md`:** the `memory/` notes, the **memory-search
  index** (default SQLite DB — otherwise search bleeds across requests), `sandboxes/`, and
  `/tmp` scratch. Then restore the seed snapshot.
- **Fresh session per call** — conversation/session context is a separate axis from the
  files; use a new session id each transaction so chat context doesn't carry either.

**Ownership & shape.** The adapter owns the transaction lifecycle, so it owns the reset. The
adapter and OpenClaw share the workspace volume (a shared `emptyDir` mounted in both
containers); a **read-only ConfigMap** holds the seed.

```
acquire(sem=1)
resetWorkspace()                 // rm -rf workspace/* sandboxes/* (incl. index db); cp -r /seed/* workspace/
ctx := withTimeout(req.Timeout)
resp := POST /v1/responses (fresh session)   // OpenClaw writes freely here — full memory freedom
cleanupWorkspace()               // best-effort wipe
release(sem)
return resp
```

Bonus: per-transaction reset also closes the documented "injection rewrites your memory"
**persistence** attack — an injected payload can churn within one task's blast radius but
cannot survive to poison the next call. Combined with the §6 network cocoon (no exfil),
the blast radius is "one transaction, no outbound."

**The constant-state choice.** "Stateless" = *identical initial state every call*; you pick
what that constant is:
- **Amnesiac** — empty/minimal seed `MEMORY.md`. Pure function: task in, answer out.
- **Frozen seed** — a curated, read-only `MEMORY.md`/`SOUL.md` (persona, standing
  instructions, domain facts) identical on every call, never drifting. Still stateless.

---

## 9. Resource footprint

Consensus from deployment guides ([Cherry Servers](https://www.cherryservers.com/blog/openclaw-hardware-requirements),
[SFAI Labs](https://sfailabs.com/guides/openclaw-hardware-requirements)):

| | Idle | Floor | Comfortable | Browser on |
|---|---|---|---|---|
| **RAM** | 300–800 MB (Node gateway) | 2 GB host | 4 GB host | +200–400 MB/instance → 8 GB |
| **CPU** | near-zero (I/O-bound) | 2 cores | 2 cores | CPU-hungry during render |

Per-pod (channels + browser off): `requests {cpu:100m, memory:512Mi}` / `limits {cpu:1,
memory:1Gi}`. Memory is the real constraint (Node heap crashes near 1Gi under load); CPU is
cheap (idles waiting on the LLM). For perspective, `news-tool` requests **16Mi/64Mi** — an
OpenClaw sidecar is ~8–16× the memory of a normal tool. That is the cost of embedding a full
Node agent; it only earns its keep if you need *its* capabilities, not just LLM completion
(which `core.AIClient` already does at ~64Mi).

---

## 10. Open decisions before build

1. **What does a task need beyond the LLM?** Reasoning-only → one egress hole (or zero, via
   the adapter). Needs web/exec/files → a deliberately wider, allowlisted hole (§6).
2. **Amnesiac vs frozen seed?** Decides the contents of the `openclaw-workspace` ConfigMap (§8).

## 11. If we build it — file inventory

New `examples/openclaw-tool/`, patterned on `news-tool`:

| File | Notes |
|---|---|
| `main.go`, `openclaw_tool.go`, `handlers.go` | name `openclaw-tool`, port e.g. `8393`, capability `run_task`, semaphore=1, reset-before/after, client → `OPENCLAW_URL` (`http://127.0.0.1:18789`) |
| `go.mod` | module path + `replace` → `../../core`, `../../telemetry` |
| `Dockerfile` | adapter binary (standard pattern) |
| `k8-deployment.yaml` | **two containers**: adapter + `image: openclaw:local`; shared `emptyDir` workspace; read-only seed ConfigMap; §6 hardening (no SA token, readonly rootfs, drop caps, seccomp); Secrets for `OPENCLAW_GATEWAY_PASSWORD` + LLM key |
| `openclaw.json` (ConfigMap) | §6 Layer 1 config; only `responses` endpoint enabled; channels off; dashboard off |
| `openclaw-workspace/` (ConfigMap) | seed files (amnesiac or frozen) |
| `networkpolicy.yaml` | default-deny egress + the one allowed hole (requires Cilium/Calico) |
| `setup.sh` | copy news-tool's; add build + `kind load` for the OpenClaw image |

---

## 12. Real OpenClaw — verified contract & reconciliation (June 2026)

> §0–§11 were written before OpenClaw was available to verify against. OpenClaw is a real,
> actively-developed open-source assistant (Clawdbot → Moltbot → OpenClaw) shipped as an
> official container image. This section records the live-doc findings and the concrete deltas
> from the assumptions above; where they conflict, **§12 wins**.

### The image — the "build it" dependency is solved
- **`ghcr.io/openclaw/openclaw:latest`** (GHCR; version tags e.g. `2026.2.26`). Pin
  **≥ 2026.1.29** (the CVE-2026-25253 fix, §5). Local builds use the tag **`openclaw:local`** —
  exactly the placeholder this folder already targets.
- Runs as **non-root `node` (uid 1000)**; gateway on **:18789**; state under `~/.openclaw`.
- **Sandbox is OFF by default**; it uses the **Docker socket** only when sandboxing is enabled.
  We keep it off and never mount the socket — the pod is the sandbox (§6). ✅ matches the cocoon.

### OpenResponses API — confirms §2/§4, with one fix
- `POST /v1/responses` · `Content-Type: application/json` · `Authorization: Bearer <token>` ·
  `x-openclaw-agent-id: <agentId>` · model `"openclaw"`. ✅ exactly what the adapter sends.
- **Fix:** there is **no `session` field**. Fresh-conversation-per-call (§8) is achieved via the
  `user` field (stable session routing) or the `x-openclaw-session-key` header, and by *not*
  sending `previous_response_id`. The adapter now sends a unique `user` per call.
- The non-streaming response is OpenAI-Responses-shaped — text at `output[].content[].text`
  (plus an `output_text` convenience field); the adapter's existing extractor already handles
  both.

### openclaw.json — the real schema (supersedes the §6-Layer-1 / §11 placeholder)
Headless, responses-only, locked down:
```json5
{
  gateway: {
    mode: "local", bind: "loopback", port: 18789,
    auth: { mode: "token", token: "${OPENCLAW_GATEWAY_TOKEN}" },
    http: { endpoints: { responses: { enabled: true } } },
    controlUi: { enabled: false },
    tools: { deny: ["browser", "shell"] }
  },
  channels: { slack:{enabled:false}, discord:{enabled:false}, telegram:{enabled:false},
              whatsapp:{enabled:false}, matrix:{enabled:false}, imessage:{enabled:false} },
  agents: { defaults: { sandbox: { mode: "off" },
                        model: { primary: "openai/gpt-5.4-mini" } } },
  models: { providers: { openai: { apiKey: "${OPENAI_API_KEY}" } } },
  plugins: { slots: { memory: "none" } }
}
```
- **Provider key via env reference** — `models.providers.openai.apiKey: "${OPENAI_API_KEY}"`
  (OpenClaw also auto-reads `OPENAI_API_KEY`). The existing envFrom-secret injection works
  unchanged; **no interactive onboarding for API-key providers**. Switch provider by editing the
  `openai` block + `model.primary` (e.g. `anthropic` / `${ANTHROPIC_API_KEY}` /
  `anthropic/<model>`).
- State/config dirs come from env (`OPENCLAW_STATE_DIR`, `OPENCLAW_CONFIG_PATH`; the image also
  honors `OPENCLAW_CONFIG_DIR` / `OPENCLAW_WORKSPACE_DIR`).

### Statelessness — refined (supersedes part of §8)
The real config exposes `plugins.slots.memory: "none"`. **We take it.** Disabling the persistent
memory plugin makes statelessness largely automatic — there is no cross-session `MEMORY.md` or
search index to bleed between requests. OpenClaw still performs in-task map-reduce over
**workspace files** (read the input, write scratch, synthesize), so the value proposition is
intact; the adapter's per-transaction reset then only has to clear the workspace scratch (the
input file + anything the task wrote), not a memory DB. This is simpler and lower-risk than §8's
"memory on + reset the index" design — revisit only if summarization quality measurably needs
persistent scratch. The semaphore-of-1 (§7) and reset-before-each (§8) still hold; the reset
target is now just OpenClaw's workspace dir.

### Pod reconciliation
- **uid 1000:** the image expects `node` (uid 1000). Override the OpenClaw container's
  `securityContext.runAsUser: 1000` (the adapter stays 10001) and use `fsGroup` so the shared
  volumes are writable by both containers.
- **Writable paths** (keep `readOnlyRootFilesystem`; grant via emptyDir): `~/.openclaw`
  (state + workspace), `~/.config/openclaw` (auth), `/tmp/openclaw` (logs). The dir the adapter
  writes `input.txt` into and resets must be OpenClaw's workspace dir (align
  `OPENCLAW_WORKSPACE_DIR` with the adapter's `WORKSPACE_DIR`).
- **Config delivery:** ship `openclaw.json` via ConfigMap, but because OpenClaw's state dir must
  be writable, copy it into a writable state dir at startup (init step) rather than mounting the
  ConfigMap read-only over the whole dir.
- **Readiness:** add a readiness probe on the OpenClaw container (real gateway boot is slower
  than the mock) so the Service doesn't route before the gateway is listening.

### First real deploy — outcome (June 2026) ✅
Deployed `ghcr.io/openclaw/openclaw:latest` into Kind. The integration is correct end-to-end;
the config snippet above already incorporates the fixes that deploying surfaced:

- `gateway.mode: "local"` is **required** — the gateway refuses to start without it.
- `agents.defaults.sandbox.mode` enum is `off` | `non-main` | `all` (the doc's `"none"` is wrong).
- the config is **strict** — an unknown root key like `_comment` fails as `<root>: Invalid input`.
  Validate offline with `docker run … openclaw:local node openclaw.mjs config validate`.
- the built-in default agent id is **`main`** (not `default`); its workspace is
  `~/.openclaw/workspace` = the shared volume the adapter writes — so set `OPENCLAW_AGENT_ID=main`.
- OpenClaw **refuses to boot when `OPENAI_API_KEY` is empty** (`SecretRefResolutionError`); a
  non-empty value is required even to start.
- **model:** `openai/gpt-4.1-2025-04-14` is listed as the static-catalog "default" but is **not
  usable for inference** in OpenClaw 2026.6.8 (`FailoverError: Unknown model`, even with a valid
  key) — it predates the current `gpt-5.x` line. Use a current id, e.g. **`openai/gpt-5.4-mini`**
  (cost-effective for summarization; `node openclaw.mjs models list --provider openai`).
- Pod runs as `node`/uid 1000 with `readOnlyRootFilesystem`; writable state confirmed under the
  `oc-home` emptyDir (`/home/node/.openclaw/{logs,agents,…}`) and `/tmp`. The init-copy of
  `openclaw.json` into the writable home works.

### The agent is a *coding* agent — drive it as a pure summarizer (supersedes the §4/§8 file flow)
OpenClaw's built-in `main` agent is the **`codex` coding agent**: handed a prompt to "read the
file at PATH and summarize," it tried to run **git/shell** (which we deny) instead of summarizing.
The fix is to stop treating it as a file-processing agent and drive it as a plain LLM:

- **Pass the document inline** in the OpenResponses `input` (no workspace file, no "read the file"
  instruction), and set **`tool_choice: "none"`** so the codex tools (shell/git/browser) are
  suppressed and it just returns the summary.
- This makes the elaborate **workspace reset (§8) unnecessary** — there is no input file and the
  agent writes no task files. Statelessness now comes purely from **a fresh `user` per call +
  `plugins.slots.memory: "none"` + `tool_choice: "none"`**. (We also removed the reset because the
  adapter (uid 10001) couldn't delete files OpenClaw (uid 1000) left in the shared workspace —
  another reason the file approach was the wrong shape.)
- **Context, not map-reduce:** `gpt-5.4-mini` has a ~391k-token window (~1.5M chars), which covers
  the `MAX_INPUT_CHARS=1,000,000` cap — so a single inline call suffices and no map-reduce is
  needed at current limits. (For inputs beyond a model's window you'd reintroduce chunking — but
  note the codex agent is not obviously good at that; this is where "just use `core.AIClient`"
  from §9 deserves reconsideration.)

**Working end-to-end ✅** (real `OPENAI_API_KEY`): pod **2/2** → `summarize_text` returns accurate
summaries and `answer_over_text` returns grounded JSON (`answer` / `found` / `supporting_excerpts`)
through adapter → OpenClaw `main` → `gpt-5.4-mini`. Switch provider/model by editing
`models.providers` + `agents.defaults.model.primary` in `openclaw.json` and the matching key in `.env`.

---

## 13. ⭐ TARGET DESIGN — autonomous task-solver (`run_task`), contained

> **This is the current, intended direction** (decided June 2026). It reframes the tool: not a
> summarizer but a **general, LLM-driven autonomous agent that solves arbitrary user tasks,
> securely contained.** The summarizer (§12) is just one narrow use case. Where this conflicts
> with the summarizer-specific narrowing in §0 / §4 / §12, **§13 wins.**

### Why this is the right shape (resolves the recurring "why not `core.AIClient`?" tension)
- All the hard machinery already built — contained sidecar, adapter membrane, transaction
  contract, the cocoon — only earns its keep for **autonomy**. A summarizer is a glorified LLM
  call, which is exactly why "just use `core.AIClient` at ~64Mi" (§9) kept resurfacing. For
  **autonomous, multi-step, tool-using task-solving there is no `core.AIClient` shortcut** — the
  agentic loop *is* the product, and that justifies the ~1Gi sidecar.
- It also dissolves the 1–10 MB concern: an autonomous agent reads large inputs **as files with
  its own tools** and works incrementally ("here's a file, do X") instead of cramming them into
  one prompt. The context window stops being the bottleneck.

### Capability — one generic `run_task`
- Input: `task` (the instruction) [+ optional inline data / file ref, `timeout_seconds`].
  Output: `result` [+ optional artifacts / transcript]. **The agent decides *how* to solve it.**
- **Drop `tool_choice:"none"`** (that was the neutering) — give it real tools; raise the
  timeouts (multi-step tasks run longer). `summarize_text` / `answer_over_text` become thin
  presets over `run_task`.

### Decisions locked (June 2026)
- **Tool scope (v1) = exec + files only.** Shell/exec + file I/O in the sandbox — compute, data,
  code, log-analysis tasks. **No internet** ⇒ tightest egress (LLM endpoint only), smallest
  attack surface. Web/browser is a later addition behind an allowlisted egress hole.
- **Isolation = pod hardening now, gVisor for production.** Build + demo on Kind with the cocoon
  + egress + statelessness; design for gVisor and enable it in prod. *Honest dev gap:* no
  kernel-escape protection on Kind — blast radius = the Kind node container on the dev laptop.

### Containment stack (defense-in-depth, two control planes)
Assume OpenClaw **will** be prompt-injected and **may** be RCE'd (§5). Goal: bound any compromise
to *one throwaway, network-limited, host-isolated task.*

**OpenClaw's own knobs** (the "more contained" advancements that motivate this pivot):
- **runtime → `openclaw`** general-purpose, NOT `codex` (the coding harness that reaches for
  git/shell — see §12). Set via the model-scoped `agentRuntime.id` (model-scoped policy wins),
  e.g. `models.providers.openai.agentRuntime: { id: "openclaw" }`.
- **tools** → enable **exec + file only**; deny browser/web/etc.
- **exec policy / approvals** → auto-exec *inside* the sandbox (the pod is the boundary, not
  OpenClaw's approval gate). *Exact config keys to be pinned during build — see execution plan.*
- **per-task workspace reset** + fresh `user` → no bleed between tasks. (Genuinely needed again
  now that the agent writes files — requires the uid fix below.)

**The K8s cocoon** (mostly in place, §6):
- non-root, read-only rootfs (writable via emptyDir workspace), drop **ALL** caps, seccomp,
  **no SA token, zero-RBAC ServiceAccount**; the Service exposes only the adapter.
- **default-deny egress + allowlist** (Cilium/Calico; kindnet ignores it). For v1 (exec+files, no
  web) the only hole is the LLM endpoint — this is the **exfil control**.
- **gVisor `RuntimeClass`** (§6 Layer 4) — *the* key addition for an agent with a shell: a
  container escape hits gVisor's user-space kernel, not the host. Production; optional on Kind.

### The sandbox-boundary decision
- **v1 — pod-as-sandbox** *(chosen)*: OpenClaw's Docker sandbox stays **off** (mounting the host
  Docker socket = instant host escape, §6). The agent runs its tools *inside* the OpenClaw
  container, contained by the cocoon + egress (+ gVisor in prod). Simplest; aligns with §6.
- **Future — rootless-DinD sidecar**: OpenClaw's *native* per-task container sandbox backed by a
  pod-local **rootless** `dockerd` (never the host socket) — stronger per-task isolation, but
  fiddly under our hardening and on Kind. An upgrade, not the start.

### Concrete changes from the summarizer build
- **`openclaw.json`:** runtime → `openclaw`; `tools` allow exec+file (deny the rest); exec policy
  for headless auto-exec; sandbox `off`; memory as needed (reset between tasks).
- **Adapter:** add `run_task`; **remove `tool_choice:"none"`**; longer default/max timeouts;
  **run the adapter container as uid 1000** (matching OpenClaw) so the per-task workspace reset
  can actually delete the agent's files — the uid-mismatch that forced us to drop the reset in §12.
- **Manifest:** gVisor `RuntimeClass` (commented/optional on Kind); egress NetworkPolicy = LLM
  only; resources sized for exec workloads.

### Residual risk (eyes open)
- **No gVisor on Kind** → a kernel 0-day isn't stopped in dev; prod needs gVisor / Kata / Firecracker.
- **Egress is the exfil channel** → default-deny + allowlist only what a task class needs.
- **Injection is assumed** → per-task reset + zero-RBAC + egress-limit ⇒ "one task, no
  persistence, no lateral movement."

### Execution plan (after sign-off)
1. **Pin OpenClaw's exact knobs** — `exec-policy` / `approvals` / `sandbox` / tool-enable config
   for secure headless auto-exec (research, don't guess — same discipline as §12).
2. **Reconfigure** config + adapter + manifest (above).
3. **Deploy + test a real autonomous task** end-to-end (e.g. "analyze this 5 MB log and report
   the top 3 error patterns" — exercises file tools + multi-step reasoning + exec).
4. **Harden + document** the security model back into this section.

### Built & working ✅ (June 2026)
Deployed and verified on Kind — the autonomous tool is live. Validated config + the findings
that getting there surfaced:

- **Runtime = the general `openclaw` runtime**, set via `models.providers.openai.agentRuntime:
  { id: "openclaw" }`. The default `codex` coding runtime made tool calls fail flakily (it
  splits commands and errors out); the `openclaw` runtime runs shell cleanly
  (`echo … && uname -s && pwd` → exact stdout). Confirms the §13 runtime decision.
- **Headless exec = `tools.exec { host:"gateway", security:"full", ask:"off" }`** plus a host
  `~/.openclaw/exec-approvals.json` (`security:full, ask:off, askFallback:full`). Both are
  provisioned by the **initContainer running `node openclaw.mjs exec-policy preset yolo`** after
  it copies the config. Effective policy = config `tools.exec` ∩ the approvals file (both gates
  must permit).
- **Confinement:** `tools.fs.workspaceOnly:true`, `gateway.tools.deny:["browser"]` (no web),
  `agents.defaults.sandbox.mode:"off"` (pod-as-sandbox: exec runs *in* the OpenClaw container,
  bounded by `readOnlyRootFilesystem` so the agent can only write the workspace + `/tmp`
  emptyDirs), all under the cocoon (non-root uid 1000, drop ALL caps, seccomp, no SA token,
  zero-RBAC SA).
- **Statelessness = fresh `user` per call + `plugins.slots.memory:"none"`.** ⚠️ **No per-task
  workspace reset** — clearing `~/.openclaw/workspace` between tasks **corrupts OpenClaw's live
  agent/session state** (the first task works, the next returns `500 internal error`). So
  conversation/memory isolation holds, but *file* isolation across tasks does **not** yet (files
  a task writes persist into later tasks). Proper per-task file isolation is a follow-up via
  OpenClaw's own sandbox (`mode:"all"` + a rootless-DinD sidecar) or ephemeral per-task agents.

**Verified end-to-end:** pod 2/2; `run_task` solves multi-step exec + file tasks (write a file
and sum it → "Sum: 15"; factorial 6 → 720; `date` → correct), **sequential calls succeed**, and
the `summarize_text` / `answer_over_text` presets still return clean structured results — all
through adapter → OpenClaw `main` (openclaw runtime) → `gpt-5.4-mini`.

**Residual risk (eyes open):** (1) no gVisor on Kind — a kernel-escape is unprotected in dev
(prod needs gVisor/Kata/Firecracker); (2) **egress is wide open on Kind — confirmed; see the
⚠️ KNOWN GAP subsection below** (this contradicts the "no internet" decision); (3) per-task
*file* isolation pending (above).

### ⚠️ KNOWN GAP — egress is wide open (empirically confirmed, June 2026)
This **contradicts the "exec + files only, no internet" decision** above and is the most
important open security item.

**Confirmed by test.** A `run_task` asked the agent to reach the internet — it fetched
`https://example.com` → **HTTP 200** and `https://api.github.com/zen` → **HTTP 200** ("Speak like
a human."). The agent has the public internet.

**Why — three compounding causes:**
1. The agent has a full **shell** (`curl`/`wget` work) — the autonomy we deliberately enabled.
2. The cluster CNI is **kindnet, which does NOT enforce NetworkPolicy** — so `networkpolicy.yaml`
   (default-deny + LLM allowlist) is **silently ignored**; *all* pod egress is unrestricted (any
   host, any port — not just `:443`). Confirmed: `kubectl get pods -n kube-system` shows `kindnet`.
3. OpenClaw also reaches `api.openai.com` by design (every LLM call).

**Why it's sharp, not cosmetic.** The OpenClaw container's env holds **`OPENAI_API_KEY` and
`OPENCLAW_GATEWAY_TOKEN`** (injected via the Secret). Shell + open egress ⇒ a prompt-injected or
hostile task (§5 says *assume* it) can read those from its own environment and exfiltrate them to
any host. **Open egress + secrets-in-env = the exfiltration path is live.** So on Kind the tool is
fit only for **trusted/local use**, not untrusted tasks.

**To actually close it** (a cluster-level fix — on kindnet, no egress control is possible at all):
1. **Cilium/Calico + `toFQDNs`** — enforce default-deny egress, allow only `api.openai.com`. Closes
   arbitrary egress; the key is still in-container.
2. **Route the LLM through the adapter** *(recommended — §6 Layer 3 "tightest cocoon")*: the adapter
   holds the key and proxies the LLM; OpenClaw's model `baseURL` → `127.0.0.1:<adapter>`; **deny all
   pod egress**. The agent's shell then reaches **nothing external**, and the OpenAI key leaves the
   untrusted container entirely — directly delivering the "exec + files, no internet" intent. (Still
   needs a real CNI to enforce the deny-all.)

gVisor does **not** help here — it is kernel isolation, not network.

---

## 14. Capability expansion — from one `run_task` to a curated suite (June 2026)

> **Decision (June 2026):** keep `run_task` as the generic escape hatch, and expose a **curated set
> of typed, real-world capabilities** on top of the same contained engine §13 already built and
> hardened. Each new capability is a thin **prompt + schema + `tool_choice`** — no new infra. This
> section records the locked "build now" scope so the doc stays live with what we're executing.

### The filter — execution-verified, not just text
OpenClaw's differentiator is **not** "it's an LLM" — it's that the agent **runs shell/scripts over
files in the sandbox**, so it returns a **verified** answer instead of a plausible one. The
high-value capabilities are therefore the ones where *running code* is the point (compute,
transform, test, parse). Pure text-in/text-out capabilities work too, but a plain direct-LLM tool
would do them more cheaply — those are **convenience presets**, not the reason this sidecar exists.

### Locked scope — the capability catalog (build now)
Every capability accepts **inline text payloads** in JSON (≤ ~1 MB, the `MaxInputChars` cap) and
returns structured JSON, over the same contained engine. They live on OpenClaw (rather than a plain
direct-LLM tool) because each benefits from the agent **running shell/scripts** to compute or
self-validate; the exact `tool_choice` per capability is tuned at build time. Each entry lists its
In → Out contract. Optional fields end with `?`.

#### Tier 1 — execution-verified (the differentiated set)

**`analyze_dataset`** — compute an answer / statistics over a pasted dataset; the agent writes and
**runs** the analysis, so the numbers are real, not guessed.
- **In** `{ data, question, format?, timeout_seconds? }` → **Out** `{ answer, method, result_table? }`
- e.g. "Which 3 SKUs dropped hardest week-over-week?" over a CSV.

**`transform_data`** — apply a transformation to pasted data and return the result; deterministic ETL
via scripts, not token-by-token guessing.
- **In** `{ data, instruction, in_format?, out_format?, timeout_seconds? }` → **Out** `{ result, out_format }`
- e.g. "pivot this CSV by month"; "join these two JSON arrays on `id`".

**`convert_format`** — convert structured data between formats (YAML ↔ JSON ↔ TOML ↔ CSV), tool-based
so it's lossless. *(A narrow specialization of `transform_data`; shares the engine, exposed as its
own typed contract.)*
- **In** `{ data, from, to }` → **Out** `{ result }`

**`parse_logs`** — forensic analysis of a log blob: error classes, a failure timeline, the likely
root cause, and the top offenders — all counted from the text.
- **In** `{ logs, focus?, timeout_seconds? }` → **Out** `{ error_classes[], timeline?, likely_root_cause?, top_offenders[] }`
- e.g. "find the first error and what cascaded from it."

**`synthesize_regex`** — derive a regex from positive/negative examples and **test it against them**
before returning (a checked answer, not a guess).
- **In** `{ should_match[], should_not_match[], flavor? }` → **Out** `{ regex, passed, failures[] }`

**`generate_tests`** — generate unit tests for a function/file, and **run them** when the language
toolchain is present in the image so they're known to pass.
- **In** `{ code, language?, framework?, timeout_seconds? }` → **Out** `{ tests, ran, results? }`

#### Tier 2 — extract & secure (agentic, lighter execution)

**`extract_structured`** — pull strict JSON from unstructured text against a target schema; the agent
validates/self-corrects the output against the schema.
- **In** `{ text, schema, instructions? }` → **Out** `{ data, confidence, unmapped[] }`
- e.g. invoice → line items; résumé → fields; contract → clauses.

**`redact_pii`** — find and mask PII in text/code/config (deterministic patterns + reasoning).
- **In** `{ text, categories?, mask? }` → **Out** `{ redacted_text, findings[] }`

**`scan_secrets`** — detect hardcoded secrets/keys/tokens in code/config (pattern + entropy scan).
- **In** `{ content, timeout_seconds? }` → **Out** `{ findings[] (type, location, severity, evidence) }`
- *(`redact_pii` and `scan_secrets` are kept as distinct intents — masked-text vs. a findings list —
  but share scaffolding; could merge into one `scan_sensitive`.)*

**`review_config`** — review IaC/config (Dockerfile / k8s / Terraform) for misconfigurations and
risky defaults; can run available linters (e.g. hadolint) when present.
- **In** `{ content, kind? }` → **Out** `{ findings[] (severity, rule, location, claim, suggestion) }`

**`review_code`** — structured review of a diff/file; the agentic reviewer that doubles as the brain
for the github-pr-review-agent (cross-ref below). Output matches the agent's `ReviewFinding` schema
for zero-friction integration.
- **In** `{ code | diff, language?, focus? }` → **Out** `{ findings[] (severity, path, line, side, claim, evidence, suggestion, confidence) }`
- Validated informally: caught SQL-injection / divide-by-zero / file-handle leak in a planted snippet.

#### Deferred (not in this build)
- **`compare_documents`** — semantic diff of two large documents (**In** `{ doc_a, doc_b, focus? }` →
  **Out** `{ differences[], summary }`). Two large docs are awkward as inline JSON → waits for the
  **shared-filesystem step** (below).
- **Tier-3 pure-LLM** — `classify_text`, `translate_text`, `rewrite_text`, plus the existing
  `summarize_text` / `answer_over_text`. Convenience presets; expose if useful, but they don't need
  OpenClaw's sandbox.

### Build approach — a data-driven capability table
The capabilities share one shape: a typed request/response + a prompt template + a `tool_choice`.
So `registerCapabilities` becomes a loop over a **capability spec table** —
`{name, description, inputSummary, outputSummary, promptTemplate, toolChoice, defaultTimeout}`.
Adding a capability = **one row + one prompt**. `run_task` stays as-is, the generic fallthrough.

### Built & verified — all 11 capabilities ✅ (June 2026)
The scaffold is a data-driven `capabilitySpec` table with a generic `handleStructured` (telemetry →
validate → `runTransaction` → parse → structured response); each capability is a `...Spec()` function
+ a prompt. `capabilities.go` holds the scaffold + the first three (the spectrum-spanning batch we
validated first: `analyze_dataset` for verified-compute, `extract_structured` for structured output,
`review_code` for the code path); `capabilities_more.go` holds the eight fan-out capabilities.
`run_task` and the summarize/answer presets are unchanged. **14 endpoints registered** (11 typed
capabilities + `run_task` + 2 presets).

**Conformance** (reviewed against TOOL_DEVELOPMENT_GUIDE §5/§6/§10): `core.ToolResponse` wrapping;
`sendError`/`sendUpstreamError` split with `WriteHeader` before encode; `ClassifyUpstreamError` via
`runTransaction`; full telemetry/log-field checklist; `InputSummary`/`OutputSummary` matching response
JSON tags; decode-vs-validation `error_type` categorization; descriptions worded so the auto-summary
(`extractFirstSentences`) isn't truncated; security (logs `input_chars`, never payload content).
**Go gates green:** build, vet, test, gofmt, golangci-lint + gosec — 0 issues.

Deployed to Kind and validated end-to-end (malformed body → 400, missing field → 400):

| Capability | Test | Result |
|---|---|---|
| `analyze_dataset` | sum / max+avg / JSON input / count-filter | 12 · alice,6.67 · p=b,total 10 · 3 — **all computed in Python** |
| `extract_structured` | invoice; person w/ missing phone | typed, confidence 0.99 / 0.98; `phone` → `unmapped` |
| `review_code` | buggy / clean / diff | SQLi+div0 · empty findings · removed-guard div0 (semantic) |
| `transform_data` | filter sales>4, sort desc | `thu,12 / tue,9 / mon,5` (wed dropped) |
| `convert_format` | YAML → JSON | `{"name":"app","port":8080,"replicas":3}` |
| `parse_logs` | "db timeout" ×3 | `error_classes [timeout:3, cache:1]` + correct root cause |
| `synthesize_regex` | AB-12 / CD-34 samples | `^[A-Z]{2}-\d{2}$`, **independently re-verified** against the examples |
| `generate_tests` | `add(a,b)` | `ran=true`, pytest tests with asserts (executed) |
| `redact_pii` | email/phone/SSN/name | all masked; findings `{type,count}`, **no raw values leaked** |
| `scan_secrets` | API key + password | both found with line refs; **`match` masked** (`AKIA…`, `hu…`) — full secret never returned |
| `review_config` | insecure Dockerfile | 4 findings: `latest` tag, `ADD`, **`curl\|sh` critical**, root user |

**`scan_secrets` masking (resolved ✅):** the secret value is returned in a `match` field that the
adapter **always masks** (`maskSecret` — a few leading characters + "…"), a Go-side guarantee
independent of whether the model masked. Verified that the full secret never appears in the response
(e.g. `AKIA…`, `hu…`).

### Cross-reference — `review_code` and the PR-review pipeline
`review_code` is the **reasoning brain** for a 3-layer GitHub PR-review pipeline already partly
built in this repo (a separate workstream; recorded here so the consumer is on the books):
- **`github-tool`** (6 capabilities, complete) — GitHub I/O + Redis artifact store: fetch PR
  bundle/diff/exact-line context, post inline reviews.
- **`github-pr-review-agent`** (MVP) — orchestration + safe-posting policy: webhook → shard →
  **review** → merge → evidence-verify → post. Its per-shard review is currently a **single direct
  LLM call**; `review_code` upgrades that into an agentic, tool-using reviewer (the agent's
  evidence-verify stage defends against any bad line refs).
- **`openclaw-tool`** — the brain. Integration is additive: a reviewer-strategy branch in the agent
  + a typed `review_code` that emits the agent's existing `ReviewFinding` schema → zero parser change.

### Next step (deferred) — shared filesystem for pass-by-reference
To move past inline JSON payloads (binary/huge files, cross-tool handoff), adopt the
**staged-artifact + handle** pattern that **`github-tool` already implements** (Redis-backed
artifact store, `bundle_id`/`artifact_id` handles, TTL, `get_artifact_slice`). Design notes for
when we get there:
- **Pluggable backend behind a handle/URI contract** — Redis for small/medium; **S3-compatible
  object store (in-cluster MinIO) for large/binary** (parquet, PDFs, images, archives).
- **Prefer in-cluster MinIO over real AWS S3** — reachable without reopening WAN egress (ties to the
  §13 egress gap), and no standing cloud credentials inside the untrusted container.
- **Hand OpenClaw a scoped, short-TTL reference, not bucket creds** — a per-task prefix / presigned
  handle it can't escape; it fetches only from the trusted in-cluster store via a handed reference
  (keeps "no arbitrary internet fetching" intact).
- **Return an output handle for large results** — so a long fetch+process job isn't bound by the
  sync HTTP timeout.
- Dovetails with the **per-task file isolation** follow-up (§13 "Built & working"): clean staged
  files per task.

---

## Sources

- [openclaw/openclaw (GitHub)](https://github.com/openclaw/openclaw) ·
  [OpenResponses HTTP API](https://docs.openclaw.ai/gateway/openresponses-http-api) ·
  [Security docs](https://docs.openclaw.ai/gateway/security) ·
  [Sandboxing docs](https://docs.openclaw.ai/gateway/sandboxing) ·
  [Memory overview](https://docs.openclaw.ai/concepts/memory) ·
  [Memory config](https://docs.openclaw.ai/reference/memory-config)
- [CVE-2026-25253 — ProArch](https://www.proarch.com/blog/threats-vulnerabilities/openclaw-rce-vulnerability-cve-2026-25253) ·
  [The Hacker News](https://thehackernews.com/2026/02/openclaw-bug-enables-one-click-remote.html) ·
  [HiddenLayer](https://www.hiddenlayer.com/research/exploring-the-security-risks-of-ai-assistants-like-openclaw) ·
  [Nebius hardening](https://nebius.com/blog/posts/openclaw-security) ·
  [arXiv security analysis](https://arxiv.org/abs/2603.27517)
- [Hardware reqs — Cherry Servers](https://www.cherryservers.com/blog/openclaw-hardware-requirements) ·
  [SFAI Labs](https://sfailabs.com/guides/openclaw-hardware-requirements)
