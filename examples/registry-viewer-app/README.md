# Registry Viewer App

Request lifecycle badges use the framework's optional `lifecycle` evidence,
independently of individual step success. Older records without it are labelled
unknown. A completed request can have failed steps or a negative application
disposition. The completed-request percentage uses all listed requests as its
denominator; unknown records are not counted as completed. Execution-list
refreshes do not reload selected evidence. Select an execution, open a detail
tab, or use the explicit Refresh action to read current evidence.
LLM and hook records can advance independently of the execution lifecycle.

An independently deployable, real-time web dashboard for inspecting the TruvaG3
service registry, execution records, LLM debug data, HITL checkpoints, skills,
memory, and traces. The backend currently imports TruvaG3 framework modules;
moving its source to another repository therefore requires the dependency and
container-build changes described below.

## Table of Contents

- [Features](#features)
- [Bounded Execution Browsing](#bounded-execution-browsing)
- [Quick Start](#-quick-start)
- [Setup Script Commands](#setup-script-commands)
- [Configuration](#configuration)
- [Live Data Verification](#live-data-verification)
- [API Endpoints](#api-endpoints)
- [Skills Management](#skills-management)
- [Service Data Structure](#service-data-structure)
- [Docker](#docker)
- [Kubernetes Deployment](#kubernetes-deployment)
- [Project Structure](#project-structure)
- [Extracting to Standalone Repository](#extracting-to-standalone-repository)
- [Port Allocation](#port-allocation)
- [Build Environment Variables](#build-environment-variables)

---

## Features

### Service Registry View
- Real-time view of all registered tools and agents
- Visual distinction between tools (green) and agents (purple)
- Health status indicators with pulse animation
- Last heartbeat timestamp for each service
- Expandable cards showing full service details:
  - Connection details (ID, address, port)
  - Capabilities list with descriptions
  - Metadata (JSON format)
- Auto-refresh every 5 seconds (configurable)
- Statistics bar showing total/agents/tools/healthy counts

### LLM Debug View
- Browse LLM debug records captured during orchestration
- View complete prompts and responses (no truncation)
- Filter by request ID or recording site
- Expandable interaction cards showing:
  - Recording site (plan_generation, synthesis, etc.)
  - Model and provider information
  - Token usage statistics
  - Full prompt and response content
  - Duration and timestamp
  - Interaction-specific trace ID when recorded; legacy absence stays unknown
- The **Debug record trace** is a record-level reference, not proof that every
  call used it. New built-in writers keep the first valid observed trace;
  linked background calls retain their own IDs. Older aggregates may contain
  the last writer's trace. **Open execution DAG** follows the same request to
  its recorded execution trace without altering historical data.
- Requires `TRUVAG3_LLM_DEBUG_ENABLED=true` on the orchestration agent

### UI Features
- Dark theme optimized for monitoring
- Navigation for Registry, LLM Debug, HITL, Execution DAG, Skills, and Memory
- Vanilla HTML, CSS, and JavaScript frontend with no browser framework dependency
- Execution DAG calls to registered agents use the starting agent's purple fill
  and a robot marker in both Full Flow and Steps Only. Ordinary tool colors stay
  unchanged; agent borders retain failure, pending and HITL status indicators.
  Classification uses current registry component types and matching capabilities,
  not service-name guesses. Missing or ambiguous registrations retain the normal
  status styling. This decoration is not historical execution/type evidence.

### Delegated Execution Navigation

Recorded clarification is shown as an expandable outcome in the existing DAG
metadata and response-node details, with the full question, missing fields and
partial progress. It supplements the execution lifecycle: failed or cancelled
executions retain those statuses and label the question **Recorded
clarification**. Phase-plan questions are separately labelled **planner
observations** and do not prove delivery to a caller. The execution and unified
APIs preserve optional `plan.needs_user_input`, phase-plan equivalents and
`result.clarification_needed`. Older records with no field have no indicator;
the Viewer does not infer questions from response prose or descendants.

The Execution DAG lists nested agents using optional `parent_request_id` and
`parent_step_id` recorded on each child. Root `original_request_id` groups the
family; it does not prove immediate parentage. Grouped, conversation and legacy
lists preserve records with unknown, missing or inconsistent parentage. Parents
outside a conversation are navigation links and do not change turn membership.

The left list uses compact depth labels and colored arrows: **L1** cyan,
**L2** purple, **L3** amber and **L4** green (the palette repeats for deeper
levels; the number remains exact). **Root** and **L?** distinguish recorded
roots from incomplete ancestry. Filtering or missing ancestors can make the
depth unknown; the Viewer does not guess it. Indentation is capped visually,
not the supported delegation depth. Parent links identify the invoking step.

Select an agent step to open its child execution(s), including multiple children
from one step. Click the starting agent node for its immediate parent and an
**Open parent at invoking step** link. A **Parent Execution** tab, immediately
after **DAG Visualization**, appears only when an immediate parent is recorded.
It loads the exact parent record on demand and shows the parent request,
lifecycle, invoking step and family-lookup diagnostics. Retained checkpoint
plans/results fill missing step evidence, with current execution evidence
taking precedence. Missing records and
missing steps are reported separately. No invocation panel occupies the DAG
canvas. A request that was not recorded is labelled **Request text unavailable**,
not replaced with its parent's request. Each
execution retains its own graph, lifecycle, LLM interactions, skills, hooks and
statistics. Durations/tokens are not summed across descendants. Siblings are
ordered by **last snapshot**, oldest first, then request ID; that timestamp can
advance on a later write and does not describe invocation order.

To open a retained execution directly, use
`http://registry.localhost/?view=dag&request_id=orch-EXAMPLE` (replace
`orch-EXAMPLE` with its URL-encoded request ID). This explicitly opens the
execution overview without first finding its row. Related-execution discovery
runs only when requested from its navigation controls. The URL follows successful
selections while this execution link is active; switching to another screen
removes the execution target. An
absent or invalid target keeps the normal Registry start screen. A missing
execution does not select a different record as a substitute.

`GET /api/executions/{request_id}/lineage` returns retained execution summaries,
parent lookup status and coverage. It is strictly read-only: stale entries are
reported without pruning, repair or TTL updates. It uses existing bounded
recent/conversation candidate reads plus exact ancestor reads with a shared
10-second request deadline and the existing ordinary/exact-owner budgets. It
does not fetch relatives' separate LLM debug payloads or enumerate via the
single-record trace index. Candidate records are summarized/released in batches
to limit heap use; full selected/ancestor records retain their existing read
semantics. Missing/expired/unindexed work can leave coverage incomplete.

Family discovery is explicit. **Refresh related executions** in the agent-node
popup, or **Refresh** in the Parent Execution tab's related-executions section,
performs one coalesced bounded lookup; simply selecting a row does not scan the
execution history. Step popups show a compact **View agent execution** link only
for known child records, without a generic family-lookup control. Exact recorded
parent links stay available without family discovery. The read inspects at most
120 recent global candidates
and 120 recent conversation candidates, in batches of eight, plus at most 20
exact selected/ancestor records. Bound and backend-failure reasons remain visible;
this is not an exhaustive family-history query. Search the request-family ID to
continue looking through older pages. The separate parent read fetches only its
overview and does not read the parent's LLM store.

### Skills Management

For the complete authoring, binding, runtime, and operational workflow, see the
[Agent Skills Guide](../../docs/orchestration/AGENT_SKILLS_GUIDE.md).

- Browse published skill metadata and exact version history
- Inspect the complete current package; use the exact-version API for an older
  retained package body (the UI history cards remain body-free)
- Create or edit complete packages with schema and deterministic validation
- Publish with optimistic ETag preconditions and idempotency keys
- Delete one version or a bounded inclusive range with protected-version checks
- Inspect each execution's body-free candidate, activation, resource-load,
  projection, and diagnostic evidence in its **Skills** tab

## Bounded Execution Browsing

The Viewer is a read layer over the existing framework records. It does not add
execution-storage keys, rewrite records, or change framework retention.

- **Initial load:** the Execution DAG opens with a flat page of at most **30
  execution summaries**. No execution detail, LLM payload, conversation history
  or related-family scan is loaded automatically.
- **Conversation:** open one explicitly to read at most **five top-level turns**.
  Grouped entries use the **first retained query** as their title, independent
  of the current search or browse page. Titles use one oldest-turn lookup per
  visible conversation, at most two lookups at once, with a bounded short-lived
  cache; the group order still reflects the latest matching activity.
  Page controls request the next small window. Delegated children remain separate
  execution links and are discovered on demand. Expanding a grouped search result
  shows its matching summaries, five turns at a time, from the current bounded
  browse page. **View timeline** separately opens the broader conversation history.
- **Selected execution:** clicking a row reads a projected overview, then loads
  Full Flow for that execution. Steps Only uses the lightweight overview;
  the other evidence tabs load their own sections. The title and horizontally
  scrolling tabs share one row to preserve canvas height.
  The cyan refresh icon immediately before **Steps Only** reloads the selected
  execution snapshot and its displayed evidence.
  Resumed executions use **Resume step N** for invocation-local numbering until
  their offset is observed. A matching stored checkpoint or an explicit HITL
  read supplies the completed-step count and the offset, excluding completed
  steps already present in the displayed plan. The small numbering fact remains
  available across evidence tabs for that selected execution.
- **Evidence:** Steps, LLM Calls, Pre-Execution, Post-Execution, HITL, Skills and
  Raw JSON each load their own requested section. Full Flow loads on execution
  selection. Raw JSON is indented without changing number or string literals;
  its Copy button copies the original stored JSON text.
  LLM node popups link to the matching call card in LLM Calls. Hook calls link
  to their cards in Pre-Execution or Post-Execution. The target card is revealed
  and focused, including calls beyond the initial ten-card batch.
  Pipeline hook popups show compact invocation summaries; full errors, effects,
  and decision details stay in the linked hook card. Hooks without a recorded
  stage link to Raw JSON instead of guessing a destination.
  Full Flow also shows recorded **Skill Activation** and **Skill Resources**
  calls in a shared lime color, before their observed planning or synthesis
  consumer. When phase or timing evidence is insufficient, a dotted association
  keeps the call visible without asserting execution order.
  The floating **Legend** button opens a glass panel over the canvas, with
  larger symbols taken from the displayed graph's actual styles. It includes
  only the node types, indicators and connections present in that view; opening
  it does not resize the graph or fetch more data. Close it with Escape, the
  close button, or an outside click. Step-layout statistics are inside the panel.
  Warm-amber dotted arrows indicate **Earlier-step data** (data provenance),
  distinct from blue flow and purple agent/model activity. Memory and approval
  links use darker amber with dashed lines. The corresponding provenance popup
  and step-detail labels use the same warm-amber color.
  The canvas fits the complete diagram on load, including deep or wide flows;
  zoom-out is no longer capped at 30%. Use the floating **Fit** button to return
  to the whole diagram after zooming or panning. Fit centers the diagram within
  the visible canvas, including after resizing the window or left pane.
  Zoom in for readable node text.
  Switching tabs releases the previous heavy section rather than retaining an
  unbounded collection of execution payloads.
- **Refresh:** the timer refreshes the current summary page only. An explicit
  Refresh reads the selected view again, including late hook/debug updates.
- **Search:** request text, execution/family/conversation IDs, trace IDs and agent
  names are matched on the server. Exact execution IDs can be retrieved directly.
  An exact hit appears first; remaining matching family records continue through
  bounded pages without repeating that hit.
  Use a time window to narrow a text search. A sparse search can return an empty
  page with a continuation; **Continue search** examines the next bounded window.
  A page count or rate describes the displayed page, not the entire backend.

Pagination replaces the current page instead of appending every visited record.
The footer highlights the current page and provides clickable visited-page
numbers. Its **Page** picker jumps directly to any of up to 100 visited pages
retained for that query; **Next** discovers another page.
These jumps fetch only the selected page. Changing filters or the time window
clears that navigation history; an exact total page count is not calculated.
Cursors are bound to the query and preserve equal-score positions. They follow
last-snapshot ordering; concurrent writes can move a record between pages, so a
cursor is not an immutable historical snapshot. Changed filters start a new
query. A moved or expired cursor anchor returns HTTP 409 and clears the expired
navigation state; Refresh starts again from the latest page in that query.
Old or expired records and bounded omissions are reported; they do not
silently stop pagination.

Browser response caching is limited to 40 entries, 4 MiB of serialized JSON in
total, 512 KiB per entry and 60 seconds per entry. These bounds do not describe
exact JavaScript heap usage. Heavy detail requests bypass that cache. Protected
responses retain their `private, no-store` behavior. Conversation-page caches
retain at most ten entries; graph instances retain their existing three-entry
bound. Superseded requests are cancelled and stale responses cannot replace the
current selection.

The backend admits at most four simultaneous execution/conversation/LLM data
reads per process. Additional inspection requests receive HTTP 429 with
`Retry-After: 1`; health checks and mutations use their existing paths. This
keeps a burst of browser tabs from creating an unbounded decode workload. The
inspection routes reject unsupported methods before reading records. The
slot covers response buffering, compression and writing; the HTTP server has
one-minute write and idle timeouts so a stalled client cannot hold a slot
indefinitely.

## 🚀 Quick Start

### Prerequisites

- **Infrastructure deployed**: This is an add-on app that connects to existing TruvaG3 infrastructure
- Run any tool/agent example first (e.g., `cd examples/tool-example && ./setup.sh full-deploy`)

### Deploy to Kubernetes (Recommended)

```bash
cd examples/registry-viewer-app

# Deploy to existing Kind cluster
./setup.sh deploy

# Set up port forwarding
./setup.sh forward
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image with Go backend and embedded static files
2. Loads the image into the Kind cluster
3. Creates a ConfigMap for non-secret topology plus Secrets for URL/credentials and an optional CA
4. Deploys the app to Kubernetes

Once complete, the dashboard is available at:

| Service | URL | Description |
|---------|-----|-------------|
| **Registry Viewer** | http://localhost:8361 | Web dashboard for service registry |
| **API** | http://localhost:8361/api/services | JSON list of registered services |
| **Health** | http://localhost:8361/api/health | Health check endpoint |
| **Readiness** | http://localhost:8361/api/readiness | Redis topology and reachability check |

### Run Locally with Mock Data

For quick UI preview without Kubernetes:

```bash
cd examples/registry-viewer-app

# Build and run with mock data
./setup.sh run
```

Open http://localhost:8361 in your browser.

## Setup Script Commands

```bash
./setup.sh <command>

Local Development:
  build         Build the application locally
  run           Run locally with mock data (default)
  run-redis     Run locally connected to Redis
  verify        Read-only verification of every Viewer data path
  verify-all    Strict verification with representative framework-owned data
  status        Show status of local/docker/k8s resources

Docker:
  docker             Build image from local framework source
  docker-standalone  Build released-module image only (no deployment)
  docker-run         Run Docker container locally with mock data
  docker-run redis   Run Docker container with URL or structured Redis configuration

Kubernetes Deployment:
  deploy        Build, load to Kind, and deploy to K8s
  rebuild       Rebuild with --no-cache and redeploy
  rollout       Apply configuration/resources using the existing cluster image
  forward       Port forward from K8s to localhost:8361
  logs          Stream logs from K8s pod
  cleanup       Remove deployed resources
```

## Configuration

Memory domains are configured independently of Kubernetes namespaces. For a
shared local deployment showing maintenance alongside infrastructure, run:

```bash
TRUVAG3_VIEWER_MEMORY_DOMAINS=infrastructure,software-maintenance ./setup.sh deploy
```

For persistent local setup, copy `.env.example` to `.env` and set the domain list
there. This file accepts only `TRUVAG3_VIEWER_MEMORY_DOMAINS`; it is parsed as
data, not sourced as shell code. Explicit process environment takes precedence
over the file, then the default `infrastructure`. Blank entries, duplicate or
unknown keys fail before deployment. Shared infrastructure redeploys use the
same local file. The file is excluded from Git and workspace Docker builds.

The commented Redis settings in `.env.example` are reference examples for the
**process environment**, not additional supported file keys. Keep them commented
in `.env`; provide overrides in the shell, for example:

```bash
REDIS_URL=redis://custom-redis:6379/0 ./setup.sh deploy
```

See [Environment Variables](#environment-variables) and
[Kubernetes Configuration](#kubernetes-configuration) for URL and structured-topology
options. The loader continues to reject Redis assignments placed in `.env`.

Optional application-record reads use process-environment settings (not additional
`.env` keys): `TRUVAG3_VIEWER_APPLICATION_READ_TIMEOUT` defaults to `15s` (1s–1m),
and `TRUVAG3_VIEWER_APPLICATION_READ_MAX_BYTES` to `1048576` (4096–8388608 bytes).
`setup.sh` carries supplied values into the ConfigMap; explicit empty/invalid
values fail application startup rather than silently using a default.

Components can opt in through an internal `inspect_application_records_v1`
capability returning a JSON ToolResponse with `data.schema_version: 1`, `records`,
`execution_refs`, `truncated` and `limitations`. The Registry service detail then
offers **Application records**. Supply a bounded JSON object and the component's
authorized read credential for each read. Credentials are forwarded in the
Authorization header only, cleared from the form, and not persisted. Redirects
are rejected; responses are uncached and failures clear the displayed record.
The component owns authorization, schemas and interpretation. The viewer reads
no application-specific Redis keys and does not label these records as new DAGs
or launch an investigation. This is an opt-in example protocol, not a framework API.

The setup script generates the ConfigMap and overrides manifest values. Match
the producer's Redis keyspace. Inspect Live Activity during a run: signals can expire before a long
execution finishes, and an empty fail-open backend response alone does not prove
no activity. Historical events and execution/debug records are separate views.

### Command Line Options

| Flag | Default | Description |
|------|---------|-------------|
| `-mock` | `true` | Use mock data instead of Redis |
| `-redis-url` | unset | Deprecated DB-0 standalone URL override; use connection environment for new deployments |
| `-namespace` | `default` | Deployment namespace shared by all versioned DB-0 keys |
| `-port` | `8361` | HTTP server port |

### Optional protected Viewer integration

Ordinary local/Kind behavior remains the default. Protected mode is an integration
for a Viewer behind a non-bypassable gateway/mesh that already verifies JWT
signatures, issuer/audience and authorization. That boundary must strip caller
identity headers, inject the verified requester's bearer credential, restrict
direct access, and enforce same-origin/CSRF and JSON-content checks for cookie-backed
writes. The Viewer parses claims only to reject invalid inputs; it does not
validate signatures or supply an authentication/authorization service.

Supply these through the **process environment**, not additional `.env` keys:

| Setting | Required protected-mode value |
|---|---|
| `TRUVAG3_VIEWER_PROTECTED_MODE` | `true` enables; unset or `false` retains ordinary mode |
| `TRUVAG3_VIEWER_REQUESTER_ISSUER` | Exact expected HTTPS issuer URL |
| `TRUVAG3_VIEWER_REQUESTER_AUDIENCE` | Exact nonempty audience |
| `TRUVAG3_VIEWER_RELAY_ORIGINS` | Nonempty JSON array of exact HTTP(S) origins with explicit ports, e.g. `["https://review.team.svc.cluster.local:8443"]` |
| `TRUVAG3_VIEWER_JAEGER_PATH` | Canonical same-origin base path, e.g. `/observability/tracing` or `/` |

Malformed or incomplete settings fail startup. Protection-specific settings while
mode is disabled also fail, as do legacy `TRUVAG3_SECURITY_SHARED` and
`TRUVAG3_SECURITY_SHARED_RELAYS` settings; migrate those explicitly. The normal
setup script does not provision this gateway or carry these settings into a
protected deployment. Its local `.env` parser intentionally remains restricted
to memory domains.

Every `/api/` request is gated except exact GET health/readiness probes. API
responses use `private, no-store`. HITL requests carry only the current requester's
bearer token to an exact admitted discovery origin and finite HITL route; redirects
and environment proxies are disabled, and expiry is checked again before forwarding.
HTTP recipients are suitable only inside the externally enforced boundary. Tokens
are not placed in browser storage, URLs or public responses. Trace redirects use
the configured same-origin path; ordinary mode retains `JAEGER_UI_URL`.

**Application-record inspection is unavailable in protected mode.** The ordinary
endpoint uses a separate component-read credential protocol. After requester
admission, protected inspection returns HTTP503 with
`APPLICATION_INSPECTION_UNAVAILABLE_IN_PROTECTED_MODE` before inspector lookup.
`/api/services` advertises `viewer_capabilities.application_records=false`, and the
UI replaces the credential form with an explanation. Ordinary inspection remains
available and advertises `true`. This feature flag is not an authorization grant.

### Environment Variables

Environment variables override command-line flags, making the app easy to configure in Kubernetes:

| Variable | Description |
|----------|-------------|
| `REDIS_URL` | Standard DB-0 standalone Redis/Valkey shorthand |
| `TRUVAG3_REDIS_MODE` / `TRUVAG3_REDIS_ADDRS` | Structured standalone, Sentinel, or cluster topology; mutually exclusive with `REDIS_URL` |
| `TRUVAG3_REDIS_MASTER_NAME` | Sentinel master name |
| `TRUVAG3_REDIS_DB` | Logical database; must be `0` for every Registry Viewer topology |
| `TRUVAG3_REDIS_USERNAME` / `TRUVAG3_REDIS_PASSWORD` | Data-node ACL credentials; deployed through a Kubernetes Secret |
| `TRUVAG3_REDIS_SENTINEL_USERNAME` / `TRUVAG3_REDIS_SENTINEL_PASSWORD` | Sentinel ACL credentials; deployed through a Kubernetes Secret |
| `TRUVAG3_REDIS_TLS_ENABLED` / `TRUVAG3_REDIS_TLS_SERVER_NAME` | Structured-topology TLS settings |
| `TRUVAG3_REDIS_CA_FILE` | Host PEM CA path copied to a read-only Secret volume for Kubernetes deployment |
| `TRUVAG3_REDIS_POOL_SIZE` / `TRUVAG3_REDIS_MIN_IDLE_CONNS` | Optional shared client pool settings |
| `TRUVAG3_REDIS_DIAL_TIMEOUT` / `TRUVAG3_REDIS_READ_TIMEOUT` / `TRUVAG3_REDIS_WRITE_TIMEOUT` | Optional shared client timeouts |
| `TRUVAG3_REDIS_MAX_RETRIES` | Optional shared client retry limit |
| `TRUVAG3_REDIS_NAMESPACE` | Deployment key namespace (overrides `REDIS_NAMESPACE` and `-namespace`) |
| `REDIS_NAMESPACE` | Deprecated Viewer-only namespace alias (overrides `-namespace`) |
| `USE_MOCK` | Set to `false` to use Redis (overrides `-mock`) |
| `PORT` | HTTP server port (overrides `-port`) |
| `APP_ENV` | Selects the `development` (default), `staging`, or `production` telemetry profile |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/HTTP endpoint used by Registry Viewer and framework telemetry |
| `JAEGER_UI_URL` | Browser-facing Jaeger UI base URL used by trace redirects (default `http://jaeger.localhost`) |
| `TRUVAG3_SKILL_AUTHORING_MAX_NAME_CHARS` | Maximum namespace/name/domain/tag/resource slug length (default `64`) |
| `TRUVAG3_SKILL_AUTHORING_MAX_DESCRIPTION_CHARS` | Maximum main skill catalog-description length (default `1024`) |
| `TRUVAG3_SKILL_AUTHORING_MAX_MANIFEST_TOKENS` | Maximum estimated tokens in one main manifest (default `5000`) |
| `TRUVAG3_SKILL_AUTHORING_MAX_MANIFEST_BYTES` | Maximum bytes in the main normative guidance (default `24576`) |
| `TRUVAG3_SKILL_AUTHORING_MAX_RESOURCE_TOKENS` | Maximum estimated tokens in one resource (default `8000`) |
| `TRUVAG3_SKILL_AUTHORING_MAX_RESOURCE_BYTES` | Maximum bytes in one resource (default `32768`) |
| `TRUVAG3_SKILL_AUTHORING_MAX_RESOURCES` | Maximum resources in one package (default `32`) |
| `TRUVAG3_SKILL_AUTHORING_MAX_PACKAGE_BYTES` | Maximum HTTP package body size (default `1048576`) |
| `TRUVAG3_SKILL_ADMIN_MAX_DELETE_VERSIONS` | Maximum versions in one inclusive deletion range (default `100`) |
| `TRUVAG3_SKILL_AUTHORING_ADVICE_MAX_OUTPUT_TOKENS` | Advisor output ceiling if an advisor is configured (default `1024`) |

The `TRUVAG3_SKILL_AUTHORING_*` and admin variables above are Registry Viewer
host settings, not framework-global environment variables. Invalid or
non-positive values fail application startup. See the
[central environment reference](../../docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md#agent-skills-configuration)
for the runtime variables consumed by skill-enabled agents.

The binary reads these settings from its process environment. The bundled
`setup.sh` forwards the shared connection form and DB-0 deployment namespace,
but does not copy authoring/admin overrides from the invoking shell into the
Deployment. For a persistent Kubernetes override, add the chosen keys to your
deployment configuration (and to the example ConfigMap/env mapping if you
retain the bundled manifest).

### Kubernetes Configuration

When deployed to Kubernetes, the app reads non-secret configuration from
`registry-viewer-config`, URL/ACL values from
`registry-viewer-redis-credentials`, and an optional PEM CA from
`registry-viewer-redis-ca`. The `setup.sh deploy` command automatically:

1. Extracts Redis service info from `../k8-deployment/redis.yaml`
2. Creates or updates the topology ConfigMap and credential/CA Secrets
3. Deploys the app with environment variables and the optional CA mount from those objects

To override the Redis URL during deployment:

```bash
REDIS_URL=redis://custom-redis:6379 ./setup.sh deploy
```

`REDIS_URL` is stored in `registry-viewer-redis-credentials`, not the
ConfigMap, because the URL may contain credentials. Structured ACL credentials
use the same Secret. Non-secret topology, namespace, pool, timeout, and retry
settings remain in `registry-viewer-config`.

## Live Data Verification

After forwarding the Viewer port, run the read-only API contract check:

```bash
./setup.sh verify
```

To prove representative service, execution, conversation, LLM, HITL, skill,
memory, and trace evidence, supply the documented expected identifiers and run
`./setup.sh verify-all`. The complete producer workflow, environment variables,
manual UI checklist, and cluster acceptance rules are in
[LIVE_DATA_VERIFICATION.md](LIVE_DATA_VERIFICATION.md).

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /` | Web UI |
| `GET /api/services` | JSON list of all registered services |
| `GET /api/health` | Health check endpoint |
| `GET /api/readiness` | Credential-safe Redis reachability, topology, DB-0, and deployment-namespace status |
| `GET /api/llm-debug?summary_only=true&limit=30` | Page LLM record headers without loading interaction bodies |
| `GET /api/llm-debug/summaries?request_id={id}` | Read compact deduplicated totals for up to five distinct repeated `request_id` values |
| `GET /api/llm-debug` | Legacy full-summary listing; may read complete debug records |
| `GET /api/llm-debug/{request_id}` | Get full debug record by request ID |
| `GET /api/executions?limit=30` | Page execution summaries; accepts cursor, q, status, agent and from/to time bounds |
| `GET /api/executions?group_conversations=true` | Group only the current bounded page; counts describe that window |
| `GET /api/executions/{request_id}/unified?section=overview` | Project the selected execution without optional-store or evidence-body reads |
| `GET /api/executions/{request_id}/unified?section=steps` | Load requested step evidence; other sections are llm, pre, post, hitl, skills, raw and flow |
| `GET /api/executions/{request_id}/unified` | Legacy complete unified view, retained for explicit API compatibility |
| `GET /api/traces/{trace_id}` | Temporarily redirect the browser to the trace page under the configured `JAEGER_UI_URL` |
| `GET /api/conversations?conversation_id={id}&limit=5` | Read one five-turn conversation page; next_cursor continues history |
| `GET /api/conversations?conversation_id={id}&direction=asc&limit=1` | Read the earliest retained top-level query for a grouped conversation title |
| `GET /api/v1/skills` | List body-free published skill metadata; optional `namespace`, `domain`, `tag`, and `limit` filters |
| `GET /api/v1/skills/schema` | Get the complete authoring JSON Schema with its active constraints |
| `POST /api/v1/skills/{namespace}/{name}/validate` | Deterministically normalize and validate a complete package without mutation |
| `PUT /api/v1/skills/{namespace}/{name}` | Publish using `If-None-Match: *` or the current `If-Match` ETag |
| `GET /api/v1/skills/{namespace}/{name}` | Read the complete published representation and ETag |
| `GET /api/v1/skills/{namespace}/{name}/versions` | List bounded body-free version history |
| `GET /api/v1/skills/{namespace}/{name}/versions/{version}` | Read one retained complete version |
| `DELETE /api/v1/skills/{namespace}/{name}/versions/{version}` | Guarded single-version deletion |
| `DELETE /api/v1/skills/{namespace}/{name}/versions?from=N&to=M` | Guarded inclusive-range deletion |

`POST .../analyze` is registered only when the host supplies an optional
`SkillAuthoringAdvisor`; the bundled Registry Viewer does not configure one by
default. The framework does not add authentication or authorization—the host,
ingress, service mesh, and platform policy own endpoint protection.

This applies to the complete Viewer, not only Skills. When LLM or execution
debugging is enabled, the UI can expose full prompts, model responses, tool
results, errors, metadata, and user context that actually reached the recorder.
Built-in stores preserve those payloads rather than inferring redaction. Do not
expose the bundled local ingress to an untrusted network without an application-
owned authentication, authorization, and data-protection layer.

### Browse safety bounds and coverage

The browser's bounded list and conversation routes read the existing versioned
DB-0 keys. They do not write an index, remove stale entries, extend TTLs or
maintain their own durable database. Legacy API paths retain their existing
framework-store behavior, including any confirmed-missing index cleanup.

These are named Viewer implementation limits, not framework defaults:

| Bound | Value |
|------|------:|
| Default executions per page | 30 |
| Maximum execution page size | 100 |
| Conversation turns per page | 5 |
| Index candidates examined per browse request | 200 |
| Index batch | 32 candidates plus one lookahead |
| Stored or decompressed execution bytes per browse record | 16 MiB |
| Stored / decompressed bytes per browse request | 32 MiB each |
| Maximum encoded browse cursor | 2048 bytes |
| List request-text preview | 1000 bytes, with a truncation flag |
| Retained list metadata | Only `resume_checkpoint_id`, up to 512 bytes |
| Browse request deadline | 10 seconds |
| Simultaneous execution/conversation/LLM reads | 4 per Viewer process |
| Simultaneous grouped-title lookups | 2 per browser tab |
| Grouped-title cache | 100 request-text previews, 60-second maximum age |
| LLM automatic-summary batch | 5 records |
| Simultaneous cold LLM-summary calculations | 2 per Viewer process |
| Compact LLM-summary cache | 256 records, 60-second maximum age |
| Automatic LLM-summary record bounds | 512 stored interactions and 16 MiB of encoded interaction data |
| LLM-summary batch/work deadline | 5 seconds each |

Execution browse records are read and decoded one at a time. Initial list/header
generation does not read LLM interaction bodies or expand all members of each
conversation. A byte, scan or
record-format bound sets coverage diagnostics while allowing cursor progress;
complete payloads remain available through explicit detail reads. A missing
index entry cannot be reconstructed by browsing, so retained-index coverage is
not proof of every historical execution ever recorded.

The LLM Debug list fills interaction count, tokens and error status automatically
for its current page after the headers appear. It sends one summary batch at a
time and cancels obsolete page loads. The backend reuses the framework's existing
deduplication function, shares concurrent requests for the same record, and
caches only compact derived values. Cache reuse checks the existing creation and
update metadata plus stored-list length, so same-second appends invalidate totals.
A cold calculation reads bounded interaction chunks; prompts and responses are
not sent to the browser until the operator opens a record. The read-only
projection leaves records and TTLs unchanged.

An expired, malformed, changing or oversized record shows **Unavailable**, rather
than a partial count or a successful zero. A later refresh can retry it; the
explicit full-record endpoint remains available. **No Errors** describes recorded
interaction outcomes, not overall execution success. Page token totals identify
how many records contributed and are not backend-wide totals.

Server ordering is `created_at` ascending or descending, after any exact-ID hit
placed first in search results. Optional `from` and
`to` bounds use RFC3339 timestamps. Unsupported global sorts are rejected rather
than presented as globally accurate. Stored execution duration and observed step
timestamps remain available in the summary; independent LLM duration and call
counts are loaded with the requested evidence, not reconstructed for every row.

For HITL resume records, elapsed observation ignores restored pre-checkpoint
steps and uses resume-owned steps/phases plus later LLM evidence. This prevents
the human approval wait from being presented as active compute time.

### Full Flow when a plan is rejected

Full Flow renders recorded planning and hook activity even when no plan was
admitted. If a later phase is rejected, the graph keeps earlier steps and shows
the rejected phase's recorded planning calls and hook invocation. It does not
invent tool steps from the rejected candidate plan. Stored hooks remain visible
when LLM debug data is unavailable; their popups show invocation status, errors,
and reported effects. A failed hook invocation alone does not change the
execution's recorded outcome.

Cards and hook-node popups show **Pipeline consequence** separately: an optional
hook can fail while the request continues; a required hook can stop the request
even if it was skipped because its input could not be cloned. **Decision
details** shows the stored failure policy, action, and reason. Older records
without decisions say **not recorded**, never an inferred success.

A hook panic shows **Panic propagated · request stopped**, even for an optional
hook. Completed earlier steps remain visible. For native streaming, the
successful synthesis call remains in LLM Calls if a later hook panics; that is
model evidence, not a successful final application response.

A unique User Memory invocation attaches to its existing detailed group at its
recorded stage position. Repeated registrations stay separate. Hooks without
usable phase placement remain visible on a non-directional **Placement not
recorded** evidence branch; this is not an execution-order or dependency claim.

Steps Only shows admitted plan steps. When none exist, it displays an explicit
empty state and points readers to Full Flow.

### Raw synthesis and terminal application response

The LLM Calls tab shows synthesis as model evidence. Its labels depend on the
recorded interaction type:

- Buffered synthesis (`synthesis`): **Pre-hook LLM synthesis output**, with
  **View Pre-hook Output**.
- Native streaming (`synthesis_streaming`): **LLM synthesis output streamed to
  client**, with **View Streamed Output**.

Both contain model output produced before application `AfterSynthesis` hooks.
Those hooks may change the final application response, but cannot retract tokens
already streamed. Neither label proves that the request completed successfully.
When the execution record contains `final_response` with source
`after_synthesis_hooks`, the Post-Execution tab shows that post-hook application
response separately.

Workflow-mode `ExecutePlanWithSynthesis` does not run pipeline hooks and does
not currently store a post-hook final response. Older records also predate this
field. In both cases, the absence message means only that no governed terminal
response was stored; it does not convert the synthesis draft into proof of the
business outcome.

### Unified-detail enrichment cost

The browser uses the section-specific unified route. Overview reads one selected
execution and projects graph/status information; it does not fetch LLM debug,
HITL families or other executions. Requested LLM/hook sections read only that
execution's debug record. Requested HITL state queries only the recorded owning
agent and reports unavailable live evidence separately from the stored snapshot.
Selected-record reads and optional section reads each have a five-second
timeout. Request cancellation is propagated to backend reads. Raw JSON is
transported as exact decompressed JSON text, so browser display and Copy preserve
unknown fields and large integer literals without parsing them into JavaScript
numbers.

The no-section `/unified` API is retained for older clients. It can assemble the
full LLM/HITL view and perform additional bounded related reads; it should not be
used for periodic summary polling. Pipeline-hook evidence continues to come from
the stored execution, not from Jaeger. Trace IDs link to the tracing view without
fetching it into the Viewer.

Hook-card status is explicitly labeled as an **invocation outcome**. A
successful invocation means the hook callback returned normally; it does not
assert that fail-open or asynchronously scheduled side effects completed.
Each card renders separately statused **Reported effects** from the stored
execution record. Expanding **View captured values** shows the exact versioned
JSON retained by the execution store—for example the activity signal and
selected coordination context, injected memory context, submitted episodic
event envelopes, extracted knowledge fragments, or cleanup call. Effect status
is labeled as the producer's report, not an independent durability guarantee;
fail-open memory providers may expose stronger internal failure detail only in
their logs. Pending asynchronous knowledge extraction is replaced by its
terminal result through a later provider-neutral execution-record write; the
Viewer still performs no Jaeger query.

## Service Data Structure

The app reads services through Core's registry adapter. Canonical service keys
match `truvag3:v1:<deployment>:registry:{<deployment>:registry}:service:*` and
each value is a JSON object:

```json
{
  "id": "weather-tool-abc123",
  "name": "weather-tool",
  "type": "tool",
  "description": "Provides current weather information",
  "address": "weather-tool-service.example",
  "port": 80,
  "capabilities": [
    {
      "name": "get-weather",
      "description": "Get current weather for a location",
      "version": "1.0.0"
    }
  ],
  "metadata": {
    "provider": "openweathermap",
    "version": "2.1.0"
  },
  "health": "healthy",
  "lastSeen": "2024-01-15T10:30:00Z"
}
```

## Docker

### Build and Run

```bash
# Build from the local framework checkout
./setup.sh docker

# Independently verify the released-module Dockerfile (build only)
./setup.sh docker-standalone

# Run with mock data
./setup.sh docker-run

# Run with Redis
./setup.sh docker-run redis
```

### Docker Container (via setup.sh)

```bash
# Build the image
./setup.sh docker

# Run the container with mock data
./setup.sh docker-run

# Run the container connected to Redis (host network)
./setup.sh docker-run redis
```

## Kubernetes Deployment

```bash
# Deploy to existing Kind cluster (build + load into Kind + apply manifest)
./setup.sh deploy

# Port forward the app to localhost:8361
./setup.sh forward
```

> **Note:** This app assumes the Kind cluster and Redis are already running — it has no `cluster` / `infra` / `full-deploy` subcommands. Bring those up via any sibling agent first (e.g., `cd ../travel-chat-agent && ./setup.sh full-deploy`) or via `examples/k8-deployment/` directly.

## Project Structure

```
registry-viewer-app/
├── main.go                  # HTTP server, registry, execution, HITL, and memory APIs
├── execution_browse.go      # Bounded summary/search pages over existing indexes
├── execution_detail.go      # Explicit overview and evidence-section projections
├── llm_summary_browse.go    # Metadata-only LLM record pages
├── read_budget.go           # Per-process data-read admission
├── conversation_api.go      # Conversation grouping and timeline API
├── jaeger_trace.go          # Optional browser redirect for stored trace IDs
├── skills_api.go            # Skills management API host
├── redis_storage_provider.go # Redis adapter used by viewer APIs
├── LIVE_DATA_VERIFICATION.md # Standalone/cluster API and UI acceptance runbook
├── scripts/                  # Read-only live-data verification
├── go.mod                   # Module with released framework dependencies
├── go.sum                   # Dependency checksums
├── static/                  # HTML, CSS, JavaScript, and image assets
├── tests/                   # Frontend Node.js tests
├── Dockerfile.workspace     # Supported in-repository container build
├── Dockerfile               # Prepared standalone-checkout container build
├── k8-deployment.yaml       # Kubernetes manifests
├── setup.sh                 # Local and Kind deployment commands
└── README.md                # This file
```

## Extracting to Standalone Repository

The Viewer Go module pins released TruvaG3 versions and has no local `replace`
directives. `Dockerfile` builds with those pins and `GOWORK=off`.
`./setup.sh docker-standalone` verifies that path using only this directory as
the build context, producing `registry-viewer-standalone:local` without deployment.
The normal `./setup.sh docker` and deployment commands use `Dockerfile.workspace`
and build the framework code from the current checkout.

For a separate repository, copy the complete directory, including `static/`,
its vendor scripts and licenses, and the tests. Update the module path and replace
the repository-specific setup wrapper with your deployment integration. Run the
Go and frontend tests before building. The Go application and standalone Dockerfile
do not require sibling framework source directories.

### Browser assets and failure behavior

The DAG uses the local scripts under `static/js/vendor/`; `packages.json` records
the pinned versions and hashes, alongside their license files. The Viewer needs
no third-party script request for graph layout. First-party controls, including
prompt and response controls, use CSP-compatible event listeners with no inline
JavaScript event handlers. An enforcing gateway may use `script-src 'self'`;
the current UI still requires inline styles.

HITL and LLM Debug show API failures explicitly and clear inaccessible data.
Sample data is available through the backend's explicit mock mode. GET caching
honors `no-store`; denial invalidates cached access, and late responses cannot
restore invalidated data. Body ETags save transfer bytes while handlers still
construct their responses. Skills revision ETags are semantic concurrency tokens:
`X-TruvaG3-Skill-ETag` mirrors the exact token for proxies that omit `ETag`, and
browser/setup consumers prefer the standard header when available.

## Port Allocation

Port `8361` is reserved for Registry Viewer in the central
[examples port catalog](../README.md#ports). Keep the binary, container,
Kubernetes manifest, setup script, and documentation on that value unless the
catalog allocation is deliberately changed. Shared infrastructure such as
Redis, Grafana, Prometheus, and Jaeger retains its own ports and ingress routes.

## Build Environment Variables

| Variable | Description |
|----------|-------------|
| `REDIS_URL` | Override the DB-0 standalone shorthand (default: extracted from `redis.yaml`) |
| `TRUVAG3_REDIS_MODE` / `TRUVAG3_REDIS_ADDRS` | Select structured standalone, Sentinel, or cluster topology instead of `REDIS_URL` |
| `TRUVAG3_REDIS_NAMESPACE` | Versioned DB-0 deployment namespace (default: `default`) |
| `JAEGER_UI_URL` | Browser-facing Jaeger UI base URL supplied to the Deployment (default: `http://jaeger.localhost`) |
| `DOCKER_NO_CACHE` | Set to `true` for fresh Docker build |

## Related Orchestration Environment Variables

The Registry Viewer displays data from the orchestration layer. Configure these variables on your **orchestration agent** (not the viewer itself) to enable features.

### LLM Debug Storage

Enable LLM debug storage to capture full request/response payloads for troubleshooting:

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_LLM_DEBUG_ENABLED` | `false` | Enable LLM debug payload storage |
| `TRUVAG3_LLM_DEBUG_TTL` | `24h` | Base retention for successful debug records; lineage promotion may extend it |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | `168h` (7 days) | Base retention for error debug records; HITL or investigation retention may extend it |

**Example:**
```bash
export TRUVAG3_LLM_DEBUG_ENABLED=true
export TRUVAG3_LLM_DEBUG_TTL=48h
```

### Human-in-the-Loop (HITL) Configuration

Configure HITL checkpoints for human oversight of AI-generated plans:

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_HITL_ENABLED` | `false` | Enable HITL globally |
| `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | `false` | Require human approval for all plans |
| `TRUVAG3_HITL_SENSITIVE_CAPABILITIES` | (empty) | Capabilities requiring plan + step approval (comma-separated) |
| `TRUVAG3_HITL_SENSITIVE_AGENTS` | (empty) | Agents requiring plan + step approval (comma-separated) |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | (empty) | Capabilities requiring step-only approval (comma-separated) |
| `TRUVAG3_HITL_STEP_SENSITIVE_AGENTS` | (empty) | Agents requiring step-only approval (comma-separated) |
| `TRUVAG3_HITL_DEFAULT_TIMEOUT` | `5m` | Timeout for human response |
| `TRUVAG3_REDIS_NAMESPACE` | `default` | Deployment namespace shared with agents for all versioned Redis/Valkey DB 0 data |

**Example:**
```bash
# Enable HITL with plan approval for sensitive operations
export TRUVAG3_HITL_ENABLED=true
export TRUVAG3_HITL_SENSITIVE_CAPABILITIES=transfer_funds,delete_account
export TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=get_balance,view_orders
export TRUVAG3_HITL_DEFAULT_TIMEOUT=10m
```

See [ENVIRONMENT_VARIABLES_GUIDE.md](../../docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md) for complete documentation of all framework variables.

### Optional LLM enrichment diagnostics

The interactive execution/conversation lists defer LLM enrichment, so missing
list totals are not reported as zero. Open the requested evidence to inspect
canonical deduplicated calls and token totals. Section reads carry visible
warnings when optional debug or live HITL data cannot be loaded.

Legacy enrichment helpers retain their existing `llm_enrichment_reasons` codes
and bounds for compatibility. Those diagnostics describe incomplete derived
views, not lost execution records or failed agent work.
