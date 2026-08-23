# GitHub PR Review Agent

> **Work in progress.** This example is incomplete and has not been validated end-to-end. Contributions to verify the flow are welcome.

An async TruvaG3 agent that reviews GitHub pull requests. Receives `pull_request`
webhooks, fetches PR bundles through the [`github-tool`](../github-tool/), runs
sharded code review against exact source (no lossy distillation), and optionally
posts a grouped review back to GitHub.

Design proposal: [`PROPOSAL.md`](./PROPOSAL.md).
Implementation plan: [`AGENT_PLAN.md`](./AGENT_PLAN.md).

## Table of Contents

1. [Quickstart](#quickstart)
2. [Prerequisites](#prerequisites)
3. [Architecture](#architecture)
4. [Deployment Modes](#deployment-modes)
5. [Configuration](#configuration)
6. [Smoke Testing](#smoke-testing)
7. [API Reference](#api-reference)
8. [Observability](#observability)
9. [Safety: Posting Policy](#safety-posting-policy)
10. [Troubleshooting](#troubleshooting)

## Quickstart

```bash
cd examples/github-pr-review-agent
cp .env.example .env
# edit .env: set OPENAI_API_KEY (or another provider) and GITHUB_WEBHOOK_SECRET

# One-shot K8s deployment (cluster + monitoring + agent, embedded mode)
./setup.sh full-deploy

# Send a signed fake webhook to verify end-to-end wiring
./setup.sh mock-webhook
```

Local dev without Kubernetes:

```bash
./setup.sh run-all   # starts Redis container, builds, runs the agent on :8382
```

## Prerequisites

- **Go 1.26.4** (both workspace and standalone Docker builds use it).
- **Docker** for the local Redis container and for image builds.
- **Kind** + **kubectl** for the Kubernetes path.
- An **AI provider key** (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
  `OPENROUTER_API_KEY`, `GEMINI_API_KEY`, or `GROQ_API_KEY`).
  Multiple keys enable provider chain failover.
- A **GitHub webhook secret** (`openssl rand -hex 32`) to sign webhook deliveries.
- The companion **[`github-tool`](../github-tool/)** deployed in the same cluster
  for the review pipeline to actually fetch PR bundles. Without it, webhooks
  enqueue tasks that fail at the `get_pr_bundle` step.

## Architecture

The agent is async-first. Webhook handlers do only signature verification +
deduplication + enqueue, then return `202 Accepted`. Workers dequeue
`review_pr` tasks and run a deterministic pipeline.

```
GitHub  →  /webhook/github   →  Redis queue  →  Worker  →  github-tool calls
   ↑                                              │              │
   │                                              ▼              ▼
   └────────── (optional) create_pr_review  ←  policy gate  ←  shard review
```

**Pipeline stages** (per task):

1. **Bundle fetch** — `github-tool.get_pr_bundle` returns a compact manifest
   with artifact handles for raw patches/files (raw code never enters
   orchestration state).
2. **Shard planning** — files filtered (skip generated/lockfiles), grouped by
   directory, packed into token-bounded shards using a `chars/3.5` heuristic.
3. **Shard review** — bounded-parallel goroutines fetch exact bounded code
   per shard, build a JSON-output prompt, call the AI chain, parse findings.
4. **Merge & dedup** — collapses findings keyed by `(path, line, normalized claim)`,
   keeping highest-confidence wins.
5. **Evidence verification** — re-fetches ±10 lines of context around each
   finding and drops any whose evidence snippet doesn't ground in current code.
6. **Decision** — `REQUEST_CHANGES` if any blocking finding clears the
   confidence threshold and policy allows it; otherwise `COMMENT`. `APPROVE`
   is out of scope for the MVP.
7. **Post (optional)** — every gate must pass: kill-switch off, dry-run off,
   `post_review=true`, repo allowlisted, head SHA still current, all findings
   have valid diff positions, and the per-(owner, repo, head SHA) throttle
   acquires a Redis SETNX with TTL.

Trace context is captured at the webhook and restored at the handler via
`telemetry.StartLinkedSpanWithOptions`, so webhook → worker is one chain in
Jaeger.

## Deployment Modes

The agent supports three modes via `TRUVAG3_MODE`:

| Mode | env | Manifest | Use case |
|------|-----|----------|----------|
| Embedded | unset | [`k8-deployment.yaml`](./k8-deployment.yaml) | Local/demo: single pod runs API + worker. |
| API | `api` | [`k8-deployment-api.yaml`](./k8-deployment-api.yaml) | Production: receives webhooks + serves task API. Scale on request volume. |
| Worker | `worker` | [`k8-deployment-worker.yaml`](./k8-deployment-worker.yaml) | Production: dequeues + runs review pipeline. Scale on queue depth / CPU. |

```bash
./setup.sh deploy-embedded   # one pod
./setup.sh deploy-split      # api + worker pods
```

The two modes are mutually exclusive — `setup.sh` removes the other set of
manifests when you switch.

## Configuration

All knobs live in [`.env.example`](./.env.example). Notable groups:

- **AI providers** — OpenAI / Anthropic / OpenRouter / Gemini / Groq + OpenAI-compatible
  drop-ins (DeepSeek, xAI, Together, Qwen, Mistral, Ollama). Configure one;
  more enables chain failover.
- **Model aliases** — `TRUVAG3_PR_REVIEW_MANIFEST_MODEL=fast`,
  `TRUVAG3_PR_REVIEW_SHARD_MODEL=smart`, `TRUVAG3_PR_REVIEW_MERGE_MODEL=fast`.
  Aliases resolve via `TRUVAG3_OPENAI_MODEL_DEFAULT` etc.
- **Posting policy** — defaults to dry-run. Enable per-repo via
  `TRUVAG3_PR_REVIEW_ALLOWED_REPOS`. Global kill-switch:
  `TRUVAG3_PR_REVIEW_POSTING_DISABLED=true`. Per-(owner,repo,head SHA) throttle:
  `TRUVAG3_PR_REVIEW_POST_MIN_INTERVAL=1h`.
- **Review strategy** — token thresholds for shard size, max parallel shards,
  skip generated/lockfile.
- **Distillation OFF** — `TRUVAG3_RESULT_DISTILL_ENABLED=false` is mandatory for
  this agent. Lossy compression of raw code defeats the point of evidence-based review.

## Smoke Testing

`mock-webhook` signs a synthetic `pull_request` payload with your
`GITHUB_WEBHOOK_SECRET` and posts it to `/webhook/github` — no GitHub round-trip
needed.

```bash
./setup.sh mock-webhook
# 202 Accepted, returns {"task_id":"..."} → poll it:
curl http://github-pr-review-agent.localhost/api/v1/tasks/<task_id>
```

The pipeline will fail at `get_pr_bundle` until `github-tool` is deployed —
that's expected. The point of this test is to verify HMAC verify, dedup,
enqueue, and worker pickup are wired correctly.

## API Reference

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/health` | GET | Liveness/readiness for K8s probes |
| `/webhook/github` | POST | Receives GitHub `pull_request` events; HMAC-verified |
| `/api/v1/tasks` | POST | Submit a manual review task |
| `/api/v1/tasks/{id}` | GET | Poll task status / result |
| `/api/v1/tasks/{id}/cancel` | POST | Cancel a queued/running task |

**Manual task submission:**

```bash
curl -X POST http://github-pr-review-agent.localhost/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "type": "review_pr",
    "input": {
      "owner": "acme",
      "repo": "payments",
      "pull_number": 42,
      "post_review": false
    }
  }'
```

## Observability

The agent declares these custom metrics (in addition to framework defaults):

- `github_pr_review.tasks_processed` — counter, labels: `status, decision`
- `github_pr_review.task_duration_ms` — histogram
- `github_pr_review.shards_reviewed` — counter, labels: `status`
- `github_pr_review.findings` — counter, labels: `severity, stage` (`merged`/`verified`)
- `github_pr_review.skipped_files` — counter, labels: `reason`
- `github_pr_review.posts_attempted` — counter, labels: `outcome, decision`
- `github_pr_review.provider_errors` — counter, labels: `provider, status, transient`

All emitted via OTLP push to the cluster's OTEL Collector. Dashboards live in
Grafana at `http://grafana.localhost`. Traces (webhook → worker → tool calls
→ AI calls, end-to-end) are in Jaeger at `http://jaeger.localhost`.

## Safety: Posting Policy

Writing comments back to GitHub is policy-controlled. Defaults are conservative:

- `TRUVAG3_PR_REVIEW_DRY_RUN=true` — produces results, doesn't post.
- `TRUVAG3_PR_REVIEW_DEFAULT_POST=false` — webhooks enqueue tasks with `post_review=false`.
- `TRUVAG3_PR_REVIEW_ALLOWED_REPOS=""` — even with posting enabled, no repos
  are allowlisted by default.
- `TRUVAG3_PR_REVIEW_POSTING_DISABLED=false` — flip to `true` to halt all
  posting without redeploy.
- `TRUVAG3_PR_REVIEW_POST_MIN_INTERVAL=1h` — at most one posted review per
  `(owner, repo, head SHA)` per hour, enforced by Redis SETNX with TTL.

`APPROVE` is rejected at the `github-tool` boundary regardless of agent
configuration — the agent only emits `COMMENT` or `REQUEST_CHANGES`.

## Troubleshooting

**Webhook returns 401 invalid signature.** `GITHUB_WEBHOOK_SECRET` is empty in
the agent's environment, or the signature header is missing/malformed. Check
`./setup.sh logs` for the structured warning that includes the delivery ID.

**Webhook returns 202 but nothing happens.** The task is queued, but the
worker is missing or `github-tool` isn't registered in discovery. Check:
`kubectl get pods -n truvag3-examples -l app=github-tool` and verify it's `Running`.

**Tasks stay `running` forever.** AI provider key invalid, or `github-tool`
unreachable. Check task result via `GET /api/v1/tasks/{id}`; check Jaeger for
the failing span. `github_pr_review.provider_errors` will tick up if it's an
AI provider issue.

**`duplicate_delivery` / `duplicate_head` responses.** Working as intended —
GitHub retried a webhook or the same head SHA was already enqueued. TTLs are
24h (delivery) and 1h (head SHA).

**Posts not appearing on PRs.** Walk the gate stack: kill-switch (`posting_disabled`),
dry-run, `post_review` flag on the task, repo allowlist, head SHA freshness,
all findings grounded with valid `LEFT`/`RIGHT` side, throttle cleared. Each
gate logs structured deny reasons.

## Related

- [`examples/github-tool/`](../github-tool/) — the GitHub API wrapper this agent calls
- [`examples/event-driven-agent/`](../event-driven-agent/) — reference async-webhook pattern
- [`examples/travel-chat-agent/`](../travel-chat-agent/) — reference deployment conventions
- [`docs/building/AGENT_DEVELOPMENT_GUIDE.md`](../../docs/building/AGENT_DEVELOPMENT_GUIDE.md)
- [`docs/orchestration/ASYNC_ORCHESTRATION_GUIDE.md`](../../docs/orchestration/ASYNC_ORCHESTRATION_GUIDE.md)
