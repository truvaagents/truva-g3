# GitHub Tool

A passive TruvaG3 tool that wraps the GitHub REST API for the
[`github-pr-review-agent`](../github-pr-review-agent/). Owns GitHub auth,
pagination, artifact storage for raw PR patches/files, exact-line file context
retrieval, and write calls for review comments.

This tool does **not** decide whether a finding is valid or whether a review
should be posted — those decisions live in the agent.

Implementation plan: [`PLAN.md`](./PLAN.md).

## Table of Contents

1. [Quickstart](#quickstart)
2. [Capabilities](#capabilities)
3. [Configuration](#configuration)
4. [Architecture](#architecture)
5. [Smoke Testing](#smoke-testing)
6. [Observability](#observability)
7. [Safety](#safety)
8. [Troubleshooting](#troubleshooting)

## Quickstart

```bash
cd examples/github-tool
cp .env.example .env
# edit .env: set GITHUB_TOKEN

# One-shot K8s deployment (cluster + monitoring + tool)
./setup.sh full-deploy

# Verify capabilities are registered
./setup.sh capabilities
```

Local dev without Kubernetes:

```bash
./setup.sh run-all   # starts Redis, builds, runs the tool on :8381
```

## Capabilities

| Name | Purpose | Large-payload behavior |
|------|---------|-----------------------|
| `get_pr_bundle` | Fetch PR metadata + changed files. | Stores raw patches (and optionally full files) as artifacts; returns a compact manifest with handle IDs. |
| `get_file_context` | Return exact bounded code for a path + line range. | Enforces `MaxContextLines` and `MaxFileBytes`. |
| `get_artifact_slice` | Return exact bounded bytes from an artifact by byte offset. | Enforces `MaxSliceBytes`. |
| `list_existing_review_comments` | Return existing review + issue comments on a PR. | Compact metadata only. |
| `create_pr_review` | Post a grouped review with inline comments. | Validates events; **`APPROVE` rejected at the tool boundary**. |
| `create_issue_comment` | Post a top-level PR comment (fallback when inline positioning fails). | Validates body. |

`create_pr_review` and `create_issue_comment` both honor `dry_run=true` —
when set, the handler runs full validation and returns a simulated success
response without calling GitHub. This is how local dev, CI, and the agent's
own dry-run mode exercise the post path safely.

## Configuration

All knobs live in [`.env.example`](./.env.example). Notable groups:

- **GitHub API** — `GITHUB_TOKEN` (PAT with `pull_requests: read/write`,
  `contents: read`); `GITHUB_API_BASE_URL` for GitHub Enterprise.
- **Artifact store** — MVP only supports `redis`. `filesystem` and `s3` are
  reserved placeholders that fail loudly at startup if selected.
  - `GITHUB_TOOL_ARTIFACT_TTL=24h` — bound by the longest review window you expect.
  - `GITHUB_TOOL_MAX_PATCH_BYTES=2097152`, `GITHUB_TOOL_MAX_FILE_BYTES=1048576`,
    `GITHUB_TOOL_MAX_SLICE_BYTES=131072` — per-payload caps.
- **Bounded ranges** — `GITHUB_TOOL_MAX_CONTEXT_LINES=400` defends against
  unbounded `get_file_context` requests.
- **Discovery resilience** — `TRUVAG3_DISCOVERY_RETRY=true` for Redis startup races.

## Architecture

```
github-pr-review-agent  →  github-tool  →  GitHub REST API
                                       ↘  Redis (artifact store)
```

PR bundles flow through the tool as **handles, not bytes**. Raw patches and
optional full files are written to Redis-backed artifact storage on
`get_pr_bundle`; the manifest returned to the agent contains `patch_artifact_id`
and (optionally) `file_artifact_id` references. The agent fetches exact code
on demand via `get_file_context` or `get_artifact_slice` for the specific
shards it's reviewing — raw code never enters orchestration state.

Pagination is built in for changed files and existing comments (`per_page=100`).

Generated/lockfile detection runs in `get_pr_bundle` to populate
`is_generated` / `is_lockfile` / `risk_hints` on each `ChangedFileEntry`.
The agent's shard planner uses these to skip noise and prioritize sensitive
areas (auth, crypto, permissions, schema, input parsing, public APIs).

## Smoke Testing

The tool has no Ingress — it's reachable only by other components in the
cluster. Use `setup.sh capabilities` for a one-shot smoke test that
port-forwards, fetches `/api/capabilities`, and tears the forward down:

```bash
./setup.sh capabilities
# → JSON listing all 6 capabilities with their schemas
```

For interactive testing, leave a port-forward running:

```bash
./setup.sh forward
# in another shell:
curl -sS http://localhost:8381/api/capabilities | jq .
curl -sS -X POST http://localhost:8381/api/capabilities/get_pr_bundle \
  -H "Content-Type: application/json" \
  -d '{"owner":"truvaagents","repo":"truva-g3","pull_number":1,"include_existing_comments":true}'
```

For posting tests without writing to a real PR, use `dry_run`:

```bash
curl -sS -X POST http://localhost:8381/api/capabilities/create_pr_review \
  -H "Content-Type: application/json" \
  -d '{
    "owner":"acme","repo":"payments","pull_number":42,
    "commit_id":"abc123","event":"COMMENT","body":"smoke",
    "dry_run":true
  }'
# → {"success":true,"data":{"state":"DRY_RUN","dry_run":true}}
```

## Observability

The tool declares these metrics:

- `github_tool.requests` — counter, labels: `capability, status`
- `github_tool.request_duration_ms` — histogram
- `github_tool.github_api_latency_ms` — histogram (per upstream call)
- `github_tool.artifact_bytes` — histogram (bytes per stored/read artifact)
- `github_tool.rate_limit_errors` — counter
- `github_tool.review_posts` — counter, labels: `event, outcome` (`posted`/`dry_run`/`failed`)

Span events fired:

- `request_received` (per capability)
- `artifact_stored`, `artifact_read`
- `review_posted`

All emitted via OTLP push to the cluster's OTEL Collector.

## Safety

- **`APPROVE` is rejected at the tool boundary.** The agent cannot post an
  approving review through this tool regardless of its own configuration. If
  approval becomes a needed feature later, gating happens here, not in the agent.
- **`dry_run` is a first-class mode** for both write capabilities. It runs
  full validation but skips the GitHub call; the response carries `dry_run: true`
  so callers can detect it.
- **All inline review comments are validated** for non-empty path/body and
  positive line; `side` (when set) must be `LEFT` or `RIGHT`.
- **Bounded ranges everywhere:** `get_file_context` rejects ranges over
  `MaxContextLines`; `get_artifact_slice` rejects byte limits over `MaxSliceBytes`.
- **Token scope minimization:** the tool requires `pull_requests: write` (for
  posting) and `contents: read` (for file fetches). Nothing more.

## Troubleshooting

**`github API error status 401: Bad credentials`** — `GITHUB_TOKEN` is unset
or expired. `setup.sh` warns at deploy time but doesn't fail; the failure
shows up on the first capability call.

**`artifact not found for path` from `get_file_context`** — the bundle has
expired (TTL = `GITHUB_TOOL_ARTIFACT_TTL`, default 24h) or the requested path
wasn't in the original PR diff. Re-run `get_pr_bundle` to refresh.

**`artifact %q too large`** — patch or file exceeded `GITHUB_TOOL_MAX_PATCH_BYTES`
or `GITHUB_TOOL_MAX_FILE_BYTES`. Bump the limits or skip the file (the agent
already skips generated/vendor by default).

**`invalid review event; allowed: COMMENT, REQUEST_CHANGES`** — the agent
sent `APPROVE`. This is intentional rejection; the agent should be emitting
only `COMMENT` or `REQUEST_CHANGES` per its own posting policy.

**Capabilities not in `/api/capabilities`** — the tool's pod isn't ready
yet. The readiness probe gates on `/api/capabilities`, so traffic only reaches
pods that have completed registration. Check `kubectl get pods -n truvag3-examples`.

## Related

- [`examples/github-pr-review-agent/`](../github-pr-review-agent/) — the agent that calls this tool
- [`docs/TOOL_DEVELOPMENT_GUIDE.md`](../../docs/TOOL_DEVELOPMENT_GUIDE.md)
