# OpenClaw Tool

Wraps **[OpenClaw](https://github.com/openclaw/openclaw)** — an autonomous, LLM-driven agent — as a
TruvaG3 tool. Hand it a self-contained task and it solves it **end-to-end with its own tools
(shell/exec + file I/O) inside a hardened sandbox**, returning the result. On top of the generic
`run_task` capability, the tool exposes **12 typed, real-world capabilities** (data analysis, code
review, extraction, PII detection, security scanning, …) over the same contained engine.

The design lens (full detail in [ANALYSIS.md](ANALYSIS.md)): **treat OpenClaw as a black-box
process, not an agent in the mesh** — request in → result out (or timeout). It never joins discovery,
never reaches into the cluster, and is confined to a cocoon that must hold even if it is
prompt-injected or RCE'd.

> ⚠️ **Security posture — read first.** OpenClaw runs *untrusted, autonomous code with a shell*. The
> pod-level cocoon contains it, **but on Kind egress is wide open** (the default `kindnet` CNI ignores
> NetworkPolicy) and the container holds the LLM key — a hostile task could exfiltrate secrets. **On
> Kind this tool is for trusted/local use only.** See [Security](#security).

## Table of Contents

- [Quickstart](#quickstart)
- [Capabilities](#capabilities)
- [Architecture](#architecture)
- [Security](#security)
- [Statelessness & Concurrency](#statelessness--concurrency)
- [Configuration](#configuration)
- [Observability](#observability)
- [Resource Footprint](#resource-footprint)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)
- [Related](#related)

---

## Quickstart

> **Prerequisites:** a running Kind cluster with shared infra (Redis, OTEL Collector, …) — see
> [k8-deployment/](../k8-deployment/) for the one-time setup. **An OpenClaw gateway image** tagged
> `openclaw:local` (pin **≥ 2026.1.29**, the CVE-2026-25253 fix) is required for the sidecar; without
> it the adapter still deploys and registers, but the `openclaw` container stays NotReady.

```bash
# 1. Configure secrets — set one AI provider key (OPENAI_API_KEY by default) matching
#    config/openclaw.json; OPENCLAW_GATEWAY_TOKEN auto-generates if left blank.
cp .env.example .env

# 2. Deploy into the existing cluster
./setup.sh deploy          # cold start: ./setup.sh full-deploy   |   after code changes: ./setup.sh rebuild

# 3. Confirm registration — open http://registry.localhost and look for "openclaw-tool"

# 4. Smoke-test a capability
kubectl port-forward -n truvag3-examples svc/openclaw-tool-service 8088:80 &
curl -s -X POST http://localhost:8088/api/capabilities/analyze_dataset \
  -H 'Content-Type: application/json' \
  -d '{"data":"name,score\nalice,9\nbob,4","question":"Who scored highest and what is the average?","format":"csv"}' | jq
```

Build/verify the adapter without a cluster (standalone Go module → `GOWORK=off`):

```bash
GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...
```

## Capabilities

`run_task` is the generic autonomous capability; the rest are typed wrappers over the same engine.
All accept inline-text JSON (≤ `MAX_INPUT_CHARS`, default 1,000,000) and return structured JSON.
`timeout_seconds` is an optional input on every capability (clamped to the adapter ceiling).

**Autonomous**

| Capability | What it does | Required | Returns |
|---|---|---|---|
| `run_task` | Solve an open-ended task end-to-end with shell/exec + file I/O | `task` | `result` |

**Tier 1 — execution-verified** (the agent *runs code*, so answers are computed, not guessed)

| Capability | What it does | Required | Returns |
|---|---|---|---|
| `analyze_dataset` | Compute an answer/statistic over a dataset (runs the analysis) | `data`, `question` | `answer`, `method`, `result_table?` |
| `transform_data` | Filter / pivot / reshape / aggregate data via a script | `data`, `instruction` | `result`, `out_format?` |
| `convert_format` | Lossless YAML ↔ JSON ↔ TOML ↔ CSV conversion | `data`, `from`, `to` | `result` |
| `parse_logs` | Classify errors, build a timeline, find root cause + top offenders | `logs` | `error_classes`, `timeline?`, `likely_root_cause?`, `top_offenders?` |
| `synthesize_regex` | Derive a regex from examples and **test it** before returning | `should_match` | `regex`, `passed`, `failures?` |
| `generate_tests` | Write unit tests and **run them** when the toolchain is present | `code` | `tests`, `ran`, `results?` |

**Tier 2 — extract & secure**

| Capability | What it does | Required | Returns |
|---|---|---|---|
| `extract_structured` | Unstructured text → strict JSON conforming to your schema | `text`, `schema` | `data`, `confidence`, `unmapped?` |
| `redact_pii` | Find & mask PII; summarize by type/count (no raw values returned) | `text` | `redacted_text`, `findings` |
| `detect_pii` | Detect PII and report what was found; values **masked** unless `reveal=true` | `text` | `pii_found`, `findings` (`type`, `value`, `count`), `parsed` |
| `scan_secrets` | Detect hardcoded secrets; the value is **masked** in the response | `content` | `findings` (`type`, `location`, `severity`, `match` masked) |
| `review_config` | Review Dockerfile / k8s / Terraform for misconfig & risky defaults | `content` | `findings` |
| `review_code` | Structured review of a code snippet or unified diff | `code` **or** `diff` | `findings` (severity, path, line, claim, evidence, suggestion, confidence) |

**Presets (pure-LLM, no sandbox)** — `summarize_text` (`text` → `summary`, …) and `answer_over_text`
(`text`, `question` → `answer`, `found`, `supporting_excerpts`). Convenience wrappers; they don't
need OpenClaw's autonomy.

Per-capability In→Out contracts and validated test results: **[ANALYSIS.md §14](ANALYSIS.md).**

## Architecture

A thin **Go adapter** (`*core.BaseTool`) and the **OpenClaw gateway** run as **two containers in one
pod** (sidecar), talking over `localhost:18789`. The adapter is the membrane — the only TruvaG3
component and the only thing on the Service.

```
┌─ Pod: openclaw-tool ─────────────────────────────────────┐
│  adapter (Go, *core.BaseTool)   → registers in Redis      │
│      ↕ localhost:18789                                     │
│  openclaw gateway (node, :18789, loopback, LLM key)        │
└────────────────────────────────────────────────────────────┘
        ↑ ClusterIP Service :80 → :8393  ← agents call the adapter only
```

Each capability is registered from a data-driven `capabilitySpec` table
([`capabilities.go`](capabilities.go) / [`capabilities_more.go`](capabilities_more.go)) and served by
one generic `handleStructured`: **validate → `runTransaction` (semaphore → POST `/v1/responses` →
error-map) → parse → `core.ToolResponse`.** Adding a capability is *one spec + one prompt*.

The engine: OpenClaw's **`openclaw` general runtime** (not the `codex` coding runtime) on
**`gpt-5.4-mini`**, with headless auto-exec (`exec-policy preset yolo`), `sandbox.mode: off` (the pod
*is* the sandbox), `tools.fs.workspaceOnly`, and the browser denied. Validated config:
[`config/openclaw.json`](config/openclaw.json) · [ANALYSIS.md §13](ANALYSIS.md).

## Security

OpenClaw is **untrusted code we run** — assume it *will* be prompt-injected and *may* be RCE'd
(CVE-2026-25253 was a one-click RCE; injection rates in the ecosystem are high). Containment must
hold regardless.

**The cocoon (in place):**

- Non-root (uid 1000 for both containers), `readOnlyRootFilesystem` (writable only via emptyDir
  workspace + `/tmp`), `capabilities: drop [ALL]`, `seccompProfile: RuntimeDefault`.
- **No ServiceAccount token** (`automountServiceAccountToken: false`) + a **zero-RBAC
  ServiceAccount** — nothing to reach the K8s API with, even after RCE.
- **Pod-as-sandbox** — OpenClaw's own Docker sandbox stays **off** and the host Docker socket is
  never mounted (it would be an instant host escape).
- The Service exposes **only the adapter** (`:80 → :8393`); the gateway (`:18789`) is loopback-only —
  no ingress, no Control UI.

**⚠️ The open gap — egress (on Kind).** The default `kindnet` CNI **does not enforce NetworkPolicy**,
so the bundled [`networkpolicy.yaml`](networkpolicy.yaml) (default-deny + LLM allowlist) is silently
ignored — the agent's shell can reach *any* host. Because the container env holds `OPENAI_API_KEY` +
`OPENCLAW_GATEWAY_TOKEN`, a hostile task could exfiltrate them. **Confirmed by test** (the agent
fetched `example.com` and `api.github.com`). **So on Kind: trusted/local use only.**

**To harden for production:**

1. A real CNI (**Cilium/Calico**) enforcing default-deny egress + a `toFQDNs` allowlist (e.g. only
   `api.openai.com`); **or** route the LLM through the adapter so OpenClaw has *zero* direct egress
   and the key leaves the untrusted container entirely.
2. **gVisor** (`RuntimeClass`) so a container escape hits a user-space kernel, not the host.
3. An **in-cluster model** (e.g. Ollama as a Service) removes the provider key + WAN egress for
   inference altogether.

Full threat model and remediations: [ANALYSIS.md §5 / §6 / §13](ANALYSIS.md).

## Statelessness & Concurrency

- **Stateless across calls** via a **fresh OpenClaw `user` per call** + `plugins.slots.memory:
  "none"` — no cross-task conversation/memory bleed. ⚠️ **Per-task *file* isolation is not yet
  enforced**: clearing the workspace mid-session corrupts OpenClaw, so the reset was removed — files
  one task writes can persist into the next. Don't run mutually-distrusting tasks against one
  replica; a follow-up adds true per-task isolation (OpenClaw `sandbox: all` + rootless-DinD, or
  ephemeral per-task agents). [ANALYSIS.md §13].
- **One task at a time per pod** — a **semaphore of 1** serializes transactions (OpenClaw is
  single-tenant on a shared workspace); excess requests fail fast with `503 BUSY` rather than queueing
  unboundedly. **Scale by replicas** (max concurrency = replica count); each replica carries its own
  ~1Gi OpenClaw. [ANALYSIS.md §7 / §9].

## Configuration

Secrets and ConfigMaps are generated by `setup.sh` from `.env` and `config/`. Adapter env
(`k8-deployment.yaml` sets these inline):

| Env var | Purpose | Default |
|---|---|---|
| `PORT` | adapter HTTP port | `8393` |
| `REDIS_URL` | Redis URL for service discovery | `redis://redis.truvag3-examples:6379` |
| `NAMESPACE` | framework namespace (downward API) | `truvag3-examples` |
| `TRUVAG3_K8S_NAMESPACE` | **service-FQDN namespace** (downward API) — drives the address agents resolve | `truvag3-examples` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector | cluster default |
| `TRUVAG3_LOG_LEVEL` / `TRUVAG3_LOG_FORMAT` | logging | `info` / `json` |
| `OPENCLAW_URL` | sidecar base URL (loopback) | `http://127.0.0.1:18789` |
| `OPENCLAW_GATEWAY_TOKEN` | bearer token for the gateway (Secret) | auto-generated |
| `OPENCLAW_AGENT_ID` | `x-openclaw-agent-id` header (OpenClaw's built-in agent) | `main` |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` / … | the OpenClaw sidecar's LLM key — set the one matching `openclaw.json` (Secret) | — |
| `MAX_INPUT_CHARS` | input size cap (protects the Node heap) | `1000000` |
| `DEFAULT_TIMEOUT_SECONDS` / `MAX_TIMEOUT_SECONDS` | transaction default / ceiling | `300` / `900` |
| `SEM_ACQUIRE_TIMEOUT_SECONDS` | bounded wait before `503 BUSY` | `5` |

> **`TRUVAG3_K8S_NAMESPACE` is load-bearing:** the framework builds the tool's service FQDN from it
> and **defaults to the `default` namespace if it's unset**, which makes agents resolve a non-existent
> `…default.svc.cluster.local` host. The manifest supplies it via `fieldRef: metadata.namespace`.

Sidecar gateway config: [`config/openclaw.json`](config/openclaw.json) (headless, responses-only,
`openclaw` runtime, sandbox off). Switch provider/model by editing `models.providers` +
`agents.defaults.model.primary` there and the matching key in `.env`. Full env list:
[`.env.example`](.env.example).

## Observability

The adapter is fully instrumented via TruvaG3 telemetry:

- **Tracing** — a trace-propagating HTTP client wraps every call to the OpenClaw gateway, so each
  transaction shows up as a client span in **Jaeger** (`jaeger.localhost`), with span events at
  `request_received` / `calling_openclaw` / completion and `api_latency` recorded.
- **Metrics** — `RecordToolCall("openclaw-tool", <capability>, duration_ms, status)` is emitted for
  every call (success or error), exported via OTLP for Prometheus/Grafana.
- **Logs** — structured JSON with the standard fields (`operation`, `request_id`, `status`,
  `duration_ms`, `error_type`); `input_chars` is logged, never payload content. Set `DEV_MODE=true`
  in `.env` for human-readable text logs locally.

All exported to `OTEL_EXPORTER_OTLP_ENDPOINT`.

## Resource Footprint

The OpenClaw sidecar embeds a full Node agent — `requests {cpu:100m, memory:512Mi}` /
`limits {cpu:1, memory:1Gi}` (the adapter itself is tiny: `10m`/`32Mi` → `100m`/`128Mi`). That's
roughly 8–16× a normal tool's memory, justified only by **autonomy** (multi-step, tool-using tasks);
for plain LLM completion, `core.AIClient` is far cheaper. [ANALYSIS.md §9].

## Project Structure

```
openclaw-tool/
├── main.go               # entry point: framework wiring + telemetry init
├── openclaw_tool.go      # tool struct, config, run_task + preset registration
├── handlers.go           # run_task / summarize / answer handlers + runTransaction + error helpers
├── capabilities.go       # capabilitySpec scaffold + generic handler + first 3 typed capabilities
├── capabilities_more.go  # the 9 fan-out typed capabilities
├── openclaw_client.go    # traced client for OpenClaw's /v1/responses
├── workspace.go          # fresh per-call session id (statelessness)
├── config/openclaw.json  # OpenClaw gateway config (headless, responses-only, sandbox off)
├── seed/MEMORY.md        # seed workspace file
├── k8-deployment.yaml    # two-container sidecar + cocoon hardening + zero-RBAC SA
├── networkpolicy.yaml    # default-deny egress + LLM allowlist (advisory on kindnet)
├── setup.sh              # build + deploy + smoke-test
├── Dockerfile            # adapter image
├── Dockerfile.workspace  # adapter image built from the repo root (local module replaces)
├── mock-openclaw/        # small Go test double for the unavailable gateway image
├── .env.example          # environment template
├── ANALYSIS.md           # full design & analysis
└── README.md             # this file
```

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `openclaw` sidecar stuck `NotReady` | The `openclaw:local` image isn't loaded — supply a real OpenClaw gateway image (`OPENCLAW_IMAGE`). The adapter still registers without it. |
| Agents get DNS `no such host` for `…default.svc.cluster.local` | `TRUVAG3_K8S_NAMESPACE` unset → FQDN defaults to `default`. The manifest sets it via the downward API; ensure your deploy didn't drop it. |
| `503 BUSY` | The semaphore-of-1 is busy with another task; retry (bounded by `SEM_ACQUIRE_TIMEOUT_SECONDS`). Scale with replicas. |
| `400` from a capability | Malformed JSON body or a missing required field. |
| Gateway refuses to boot | A provider key (e.g. `OPENAI_API_KEY`) is empty — OpenClaw requires a non-empty value even to start. |

Useful commands: `./setup.sh logs` · `status` · `rollout` (restart) · `rebuild` (after code changes) ·
`clean` (remove this deployment).

## Related

- **[ANALYSIS.md](ANALYSIS.md)** — full design & analysis: black-box framing, the cocoon and threat
  model, concurrency, statelessness, the validated OpenClaw config, the capability catalog with test
  results, the egress gap, and the deferred shared-filesystem step.
- Orchestrating agents that discover & call this tool: [travel-chat-agent](../travel-chat-agent/),
  [devops-chat-agent](../devops-chat-agent/).
- [github-pr-review-agent](../github-pr-review-agent/) + [github-tool](../github-tool/) — `review_code`
  is the reasoning brain for that PR-review pipeline ([ANALYSIS.md §14](ANALYSIS.md)).
- Guides: [`TOOL_DEVELOPMENT_GUIDE.md`](../../docs/building/TOOL_DEVELOPMENT_GUIDE.md) ·
  [`AI_PROVIDERS_SETUP_GUIDE.md`](../../docs/building/AI_PROVIDERS_SETUP_GUIDE.md) ·
  [`DISTRIBUTED_TRACING_GUIDE.md`](../../docs/observability/DISTRIBUTED_TRACING_GUIDE.md) ·
  [`LOGGING_IMPLEMENTATION_GUIDE.md`](../../docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md)
