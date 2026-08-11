# Developer Tools Guide

Hey there! This guide covers the two developer-facing tools TruvaG3 ships for building, exploring, and debugging agent networks: **Swagger UI** for discovering what each component can do (its API contract), and the **Registry Viewer** for seeing what the running system is actually doing right now (live observability). Between them, they answer almost every "what is this thing?" and "what just happened?" question you'll hit while developing.

If you've ever had to keep a hand-written spec file in sync with your handlers, tail five different log streams to trace one request, or guess why the LLM made the plan it did — all three of those problems get solved here.

> **Working Examples**
>
> Everything in this guide is backed by working code in the repo:
>
> **Swagger UI (API exploration)**
> - **Spec generator**: [`core/openapi.go`](https://github.com/truvaagents/truva-g3/blob/main/core/openapi.go)
> - **Swagger UI deployment**: [`examples/k8-deployment/swagger-ui.yaml`](https://github.com/truvaagents/truva-g3/blob/main/examples/k8-deployment/swagger-ui.yaml)
> - **Auto-discovery feed**: [`examples/registry-viewer-app/main.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/main.go) (`handleSwaggerURLs`)
> - **Live at**: [http://swagger.localhost](http://swagger.localhost)
>
> **Registry Viewer (runtime observability)**
> - **App**: [`examples/registry-viewer-app/`](https://github.com/truvaagents/truva-g3/tree/main/examples/registry-viewer-app)
> - **Views**: [`examples/registry-viewer-app/static/js/views/`](https://github.com/truvaagents/truva-g3/tree/main/examples/registry-viewer-app/static/js/views) — `registry.js`, `llm-debug.js`, `hitl.js`, `dag.js`, `memory.js`
> - **Live at**: [http://registry.localhost](http://registry.localhost)
>
> Both URLs are available after running `examples/k8-deployment/setup-infrastructure.sh` against the local kind cluster. We recommend having the cluster running alongside this guide so you can click through while you read.

---

## Table of Contents

1. [Overview](#1-overview)
   - [The Two Tools at a Glance](#the-two-tools-at-a-glance)
   - [Why This Matters](#why-this-matters)

**Swagger UI — API Exploration**

2. [The `/openapi.json` Contract](#2-the-openapijson-contract)
   - [What the Spec Contains](#what-the-spec-contains)
   - [How Field-Level Schemas Are Built](#how-field-level-schemas-are-built)
   - [Capability Filtering](#capability-filtering)
3. [Enabling the Endpoint](#3-enabling-the-endpoint)
   - [Option 1: Explicit framework option](#option-1-explicit-framework-option-highest-priority)
   - [Option 2: Environment variable](#option-2-environment-variable)
   - [Option 3: Default](#option-3-default)
   - [Precedence](#precedence)
   - [Verifying](#verifying)
4. [Local Kind Cluster Setup](#4-local-kind-cluster-setup)
   - [What Gets Deployed](#what-gets-deployed)
   - [How Auto-Discovery Works](#how-auto-discovery-works)
   - [Dev Default: `TRUVAG3_ENABLE_OPENAPI=true`](#dev-default-truvag3_enable_openapitrue)
   - [Overriding the Dev Default](#overriding-the-dev-default)
   - [Deploying Swagger UI to the Local Cluster](#deploying-swagger-ui-to-the-local-cluster)
5. [Enterprise Integration Patterns](#5-enterprise-integration-patterns)
   - [Pattern 1: Centralized Swagger UI / Redocly](#pattern-1-centralized-swagger-ui--redocly-most-common)
   - [Pattern 2: Backstage (service catalog with auto-discovery)](#pattern-2-backstage-service-catalog-with-auto-discovery)
   - [Pattern 3: API Gateway Ingestion (Kong, Apigee, AWS API Gateway)](#pattern-3-api-gateway-ingestion-kong-apigee-aws-api-gateway)
   - [Pattern 4: CI/CD Spec Publishing (SwaggerHub, Stoplight, Git)](#pattern-4-cicd-spec-publishing-swaggerhub-stoplight-git)
   - [Pattern 5: Dynamic Multi-Service Aggregation (the pattern we ship)](#pattern-5-dynamic-multi-service-aggregation-the-pattern-we-ship)
6. [Security Considerations](#6-security-considerations)
   - [Production Defaults](#production-defaults)
   - [What the Endpoint Reveals](#what-the-endpoint-reveals)
   - [When to Enable in Production](#when-to-enable-in-production)
   - [When NOT to Enable in Production](#when-not-to-enable-in-production)

**Registry Viewer — Runtime Observability**

7. [Registry Viewer Overview](#7-registry-viewer-overview)
   - [What It Shows You](#what-it-shows-you)
   - [How It Gets Its Data](#how-it-gets-its-data)
   - [Prerequisites](#prerequisites)
8. [The Five Views](#8-the-five-views)
   - [8.1 Registry View — Service Catalog](#81-registry-view--service-catalog)
   - [8.2 LLM Debug View — Raw LLM Call Stream](#82-llm-debug-view--raw-llm-call-stream)
   - [8.3 HITL Interrupted View — Pending Approvals](#83-hitl-interrupted-view--pending-approvals)
   - [8.4 Execution DAG View — The Flagship Debugging View](#84-execution-dag-view--the-flagship-debugging-view)
   - [8.5 Memory View — Shared Memory Inspector](#85-memory-view--shared-memory-inspector)
9. [Which Tool for What: A Decision Table](#9-which-tool-for-what-a-decision-table)
10. [See Also](#10-see-also)

---

## 1. Overview

Building an agent network gives you two kinds of questions that developer tools need to answer. The first is **"what can this thing do?"** — a design-time and integration-time question about contracts and capabilities. The second is **"what did this thing just do?"** — a runtime question about behavior, decisions, and state. TruvaG3 ships one tool for each.

### The Two Tools at a Glance

| Tool | Answers | Think of it as | URL (local kind) |
|------|---------|----------------|------------------|
| **Swagger UI** | What can this service do? What's the request shape? What does it return? Can I try calling it? | A self-updating API catalog — every service publishes its own OpenAPI spec at `/openapi.json`, no hand-written YAML | [http://swagger.localhost](http://swagger.localhost) |
| **Registry Viewer** | Which services are running? What LLM calls did the orchestrator make and why? Is there a human approval waiting? For this specific execution, what was the plan, how did each step run, and which memory hooks fired? What's in shared memory right now? | A runtime dashboard for a living system — reads directly from Redis and vector DB, refreshes on demand | [http://registry.localhost](http://registry.localhost) |

Both are optional, opt-in, and developer-facing. Neither is required to run TruvaG3 in production — they exist so you can build, debug, and explain agent behavior while you're developing it.

### Why This Matters

Think of Swagger UI as the **restaurant menu** — every TruvaG3 tool and agent is a restaurant, its capabilities are the dishes, and instead of maintaining a hand-written menu that drifts out of sync with the kitchen, the menu **prints itself** from what the chef is actually cooking today. No external tooling, no build-time code generation, no spec files to maintain.

```
Without self-describing APIs:           With /openapi.json:

Register capability "get_weather"       Register capability "get_weather"
  → Forget to update openapi.yaml         → /openapi.json reflects it
  → Docs drift                            → Swagger UI picks it up
  → Callers guess field names             → Callers "Try it out" live
  → API gateway rejects payloads          → Gateway routes auto-configured
```

The `/openapi.json` endpoint is **opt-in** — disabled by default — so a production deployment never accidentally leaks its full API surface to anyone who can reach the pod. You enable it explicitly in dev and staging environments (via one env var or one framework option), and leave it off in prod unless you have a specific reason to turn it on behind a trust boundary.

Think of the Registry Viewer as the **security camera and flight recorder** for the same restaurant — it doesn't tell you what the menu *should* be, it tells you what's happening on the floor right now. Which waiters are working (service registrations), what the head chef is reasoning about (LLM calls), which orders got held up for manager approval (HITL checkpoints), exactly how a specific ticket moved through the kitchen (execution DAG), and what the team remembers about the dinner rush so far (shared memory).

Swagger UI alone is enough to **build** against TruvaG3. Registry Viewer becomes essential the moment you start **debugging** it — especially when the question shifts from "my code is wrong" to "my code is correct but the LLM made a decision I don't understand."

By exposing a standards-compliant OpenAPI spec at a well-known path, TruvaG3's Swagger UI side also integrates with any of the following **without custom code**:

- **Swagger UI** — interactive spec browser with "Try it out" forms
- **Redoc / Redocly** — read-only reference documentation
- **Backstage** — Spotify's developer portal and service catalog
- **Kong, Apigee, AWS API Gateway** — gateways that ingest OpenAPI to auto-configure routes
- **SwaggerHub, Stoplight, Postman** — spec registries for cross-team sharing
- **Internal dev portals** — anything that consumes OpenAPI URLs

You write zero code for any of this. You set one environment variable and point your existing tool at `/openapi.json`.

---

# Part 1 — Swagger UI: API Exploration

Sections 2 through 6 cover the first tool: the `/openapi.json` contract every TruvaG3 component publishes, how to enable it, the local kind cluster Swagger UI deployment, how enterprise teams integrate with existing documentation systems, and the security model.

---

## 2. The `/openapi.json` Contract

This is the one promise the framework makes. When enabled, every tool and agent exposes exactly one additional endpoint:

```
GET /openapi.json
```

The response is a valid OpenAPI 3.0.0 document as JSON, generated at request time from the component's registered capabilities. "At request time" matters — if you register a new capability after startup, the very next call to `/openapi.json` reflects it. There is no cache to bust, no file to regenerate, and no process to restart.

### What the Spec Contains

| Field | Source |
|-------|--------|
| `info.title` | Component name (e.g. `weather-tool-v2`) |
| `info.description` | `Auto-generated OpenAPI spec for {tool\|agent} {name}` |
| `info.version` | `1.0.0` |
| `paths.{endpoint}.post` | One `POST` operation per registered `Capability` (all capabilities use POST with a JSON body) |
| `paths.{endpoint}.post.operationId` | `Capability.Name` |
| `paths.{endpoint}.post.summary` | `Capability.Description` |
| `paths.{endpoint}.post.tags` | Capability type (`tool`, `reasoning`, or `orchestrator` — defaults to `tool` if unset) plus `internal` if `Capability.Internal == true` |
| `paths.{endpoint}.post.requestBody` | If `InputSummary != nil`: references `#/components/schemas/{Name}Input`. Otherwise: inline `{type: "object"}` with no properties |
| `paths.{endpoint}.post.responses.200` | Always present. If `OutputSummary != nil`: references `#/components/schemas/{Name}Output`. Otherwise: description-only with no `content` field (clients should treat the 200 body as opaque) |
| `components.schemas.{Name}Input` | Emitted only when `InputSummary != nil`. Built from `RequiredFields` (added to schema `required[]`) and `OptionalFields` (properties only) |
| `components.schemas.{Name}Output` | Emitted only when `OutputSummary != nil`. Same construction rules as the input schema |
| `paths` — `/api/capabilities` | Always included. `GET` returning an array of capability objects, tagged `framework` |
| `paths` — `/health` | Always included. `GET` returning a simple health JSON (`status`, `type`, `name`, `id`), tagged `framework` |

The endpoint path for a capability is taken from `Capability.Endpoint`. If unset, it defaults to `/api/capabilities/{Name}`.

**What is NOT in the spec:** two other endpoints the framework registers automatically are intentionally left out of the generated spec to keep it focused on the public API contract:
- `/openapi.json` itself (the spec endpoint does not self-document)
- `/api/capabilities/{name}/schema` — the auto-generated JSON Schema endpoint the framework exposes per capability when `InputSummary` is provided, used by the orchestration module's schema-cache validation layer (see [TOOL_SCHEMA_DISCOVERY_GUIDE.md](../building/TOOL_SCHEMA_DISCOVERY_GUIDE.md) Phase 3)

### How Field-Level Schemas Are Built

Each `FieldHint` maps directly to an OpenAPI property:

| `FieldHint` | OpenAPI property |
|-------------|------------------|
| `Name` | property key |
| `Type` | `type` (already JSON Schema types: `string`, `number`, `boolean`, `object`, `array`) |
| `Example` | `example` |
| `Description` | `description` |

Fields in `RequiredFields` are added to the schema's `required[]` array; fields in `OptionalFields` are properties only.

Quality of the spec is entirely a function of the quality of the `InputSummary` and `OutputSummary` you provide when registering capabilities. See [TOOL_DEVELOPMENT_GUIDE.md](../building/TOOL_DEVELOPMENT_GUIDE.md) Section 5 and [TOOL_SCHEMA_DISCOVERY_GUIDE.md](../building/TOOL_SCHEMA_DISCOVERY_GUIDE.md) for the authoring conventions.

### Capability Filtering

All registered capabilities are included in the spec, including ones marked `Internal: true`. The `Internal` flag means "hidden from the LLM orchestration planner", not "hidden from API documentation" — internal capabilities are still real REST endpoints developers need to call (chat streams, session management, admin endpoints, HITL resumes). They are tagged with `"internal"` in the OpenAPI tags list so UI tooling can group or filter them.

If you want an endpoint completely absent from the spec, do not register it through `RegisterCapability()` — register the handler directly on the mux.

---

## 3. Enabling the Endpoint

The endpoint is **disabled by default** in core. That's deliberate — a component that ships with its full API surface pre-exposed would be a security footgun in production. You turn it on explicitly, and the framework gives you three ways to do that in priority order:

### Option 1: Explicit framework option (highest priority)

```go
tool := core.NewTool("weather-tool-v2")
tool.RegisterCapability(...)

framework, err := core.NewFramework(tool,
    core.WithPort(8096),
    core.WithOpenAPI(true),  // enable /openapi.json
)
```

Use this when a single component should always expose its spec regardless of environment — for example, a reference tool whose sole purpose is to demonstrate the framework. Most components do not need this and instead rely on Option 2 at the deployment level.

### Option 2: Environment variable

```
TRUVAG3_ENABLE_OPENAPI=true
```

Set this at the deployment level (ConfigMap, Helm values, kustomize patch, etc.). Every tool or agent in that deployment will expose its spec. This is the recommended path for dev and staging environments.

### Option 3: Default

If neither of the above is set, the endpoint is not registered. A `GET /openapi.json` against the component returns 404.

### Precedence

- Explicit `WithOpenAPI(...)` always wins over env var and default
- `TRUVAG3_ENABLE_OPENAPI` wins over the default
- Default is `false`

### Verifying

After enabling, `curl` the component's `/openapi.json` endpoint directly. The exact URL depends on where you are running `curl`:

```bash
# From inside the cluster (another pod in the same namespace):
curl -s http://weather-tool-v2-service/openapi.json | jq .openapi
# → "3.0.0"

# From your laptop via kubectl port-forward:
kubectl port-forward -n truvag3-examples svc/weather-tool-v2-service 8096:80
curl -s http://localhost:8096/openapi.json | jq .openapi
# → "3.0.0"

# From your laptop via the example swagger-ui proxy (local kind cluster only):
curl -s http://swagger.localhost/svc/weather-tool-v2-service/openapi.json | jq .openapi
# → "3.0.0"
```

A `404` response means the endpoint is disabled (either the option wasn't set or `TRUVAG3_ENABLE_OPENAPI` is unset/false). A valid OpenAPI document means the gate is on and the spec generator is running.

---

## 4. Local Kind Cluster Setup

The repo ships a working Swagger UI deployment for the local kind cluster so you can see the whole thing in action without building anything yourself. Open [http://swagger.localhost](http://swagger.localhost), pick a service from the dropdown, and you're looking at its auto-generated spec — complete with "Try it out" buttons that actually call the pod. New tools show up in the dropdown on the next page refresh; tools scaled to zero disappear within ~30 seconds.

Important: this is **scaffolding for the repo's demo environment**, not part of the framework itself. If it doesn't match your environment, skip ahead to [Section 5](#5-enterprise-integration-patterns) for the patterns that do.

### What Gets Deployed

Three pieces live under [examples/k8-deployment/](https://github.com/truvaagents/truva-g3/tree/main/examples/k8-deployment):

| File | Purpose |
|------|---------|
| `swagger-ui.yaml` | Deployment + Service + ConfigMap for the `swaggerapi/swagger-ui` image, plus a custom nginx config that proxies all `/svc/{service-name}/...` requests (spec fetches AND "Try it out" capability calls) to internal ClusterIP services |
| `ingress-infra.yaml` | Adds an Ingress rule exposing `swagger.localhost` → `swagger-ui` service (alongside the other infra ingress rules) |
| `setup-infrastructure.sh` | Deploys Swagger UI alongside Redis, Prometheus, Grafana, etc. |

Separately, the [registry-viewer](https://github.com/truvaagents/truva-g3/tree/main/examples/registry-viewer-app) app exposes `GET /swagger-urls.json` — a list of every TruvaG3 service currently registered in Redis, formatted as Swagger UI's `urls` config array. The kind-cluster Swagger UI fetches this URL at page load to auto-populate its service dropdown.

### How Auto-Discovery Works

```
┌──────────┐   GET /         ┌────────────┐   GET /swagger-urls.json  ┌────────────────┐
│ Browser  ├────────────────>│ swagger-ui ├──────────────────────────>│ registry-viewer │
│          │                 │  (nginx)   │                           │                 │
│          │<────HTML + JS───┤            │<────[{name,url},...]──────┤                 │
│          │                 │            │                           └────────┬────────┘
│          │   GET /swagger- │            │                                    │
│          ├──urls.json─────>│            │                                    │ Redis
│          │                 │            │                                    │ SCAN
│          │  GET /svc/{svc} │            │  GET /openapi.json                 │
│          ├──/openapi.json─>│            ├──────────────────────>┌────────┐   │
│          │                 │            │  + X-Forwarded-Host:  │ tool X │<──┘
│          │                 │            │    swagger.localhost  │  pod   │
│          │                 │            │  + X-Forwarded-Prefix:│        │
│          │                 │            │    /svc/{svc}         │        │
│          │<────spec────────┤            │<──OpenAPI 3.0 spec────┤        │
│          │  servers:[{url: │            │  with servers URL     │        │
│          │  "http://swagger│            │  pointing back here   │        │
│          │  .localhost/svc/│            │                       │        │
│          │  {svc}"}]       │            │                       │        │
│          │                 │            │                       │        │
│          │  POST /svc/{svc}│            │  POST /api/...        │        │
│          ├──/api/capabili──>            ├──────────────────────>│        │
│          │  ties/X         │            │  (Try it out call)    │        │
│          │<────result──────┤            │<───────result─────────┤        │
└──────────┘                 └────────────┘                       └────────┘
```

1. Browser loads `http://swagger.localhost/`
2. `swagger-initializer.js` fetches `/swagger-urls.json` — proxied by nginx to the registry-viewer, which reads all registered services from Redis
3. Swagger UI renders a dropdown with every service
4. When the user picks a service, Swagger UI fetches `/svc/{service-name}/openapi.json`. The catch-all nginx location proxies it to that ClusterIP service's `/openapi.json` endpoint, setting `X-Forwarded-Host: swagger.localhost` and `X-Forwarded-Prefix: /svc/{service-name}` so the spec's `servers` URL points back through this same proxy.
5. Swagger UI renders the spec, offering "Try it out" forms for every capability. Each form generates a request to `http://swagger.localhost/svc/{service-name}/api/capabilities/...`, which the same nginx location proxies through to the capability endpoint on the cluster service.

This means individual agents never need to know their public ingress hostname (e.g. `hitl-agent.localhost`, `telemetry-agent.localhost`) for Swagger UI to work — the K8s Service name (already in the Redis registration) is the only routing key. New tools appear in the dropdown on the next page refresh. No config edits, no redeploys of Swagger UI. Tools that are scaled to zero or crash disappear within the Redis TTL (~30 seconds).

### Dev Default: `TRUVAG3_ENABLE_OPENAPI=true`

For the local kind cluster workflow to work out of the box, every tool and agent's ConfigMap needs `TRUVAG3_ENABLE_OPENAPI=true`. Rather than require developers to add this to ~40 `.env` files, the shared [setup-env-lib.sh](https://github.com/truvaagents/truva-g3/blob/main/examples/k8-deployment/setup-env-lib.sh) library injects it automatically into every ConfigMap it creates. The injection is two-step:

```bash
# Step 1 — while scanning the .env file, track whether the developer set
# TRUVAG3_ENABLE_OPENAPI explicitly (to either true or false):
if [[ "$key" == "TRUVAG3_ENABLE_OPENAPI" ]]; then
    _openapi_set_from_env=true
fi

# Step 2 — after the scan, inject the dev default only if the developer
# did NOT set it explicitly AND they have not globally disabled the default:
if [[ "${_openapi_set_from_env:-false}" != "true" ]] && [[ "${TRUVAG3_DEV_DISABLE_OPENAPI:-false}" != "true" ]]; then
    kubectl_args="$kubectl_args --from-literal=TRUVAG3_ENABLE_OPENAPI=true"
fi
```

This applies only when deploying through the example `setup.sh` scripts that source this library. Production deployments managed by SRE pipelines do not source `setup-env-lib.sh` and therefore do not get the default — they must opt in explicitly via whatever config management they use (Helm values, kustomize, plain ConfigMap YAML, etc.).

### Overriding the Dev Default

A developer can override in two ways:

1. **Per-tool** — set `TRUVAG3_ENABLE_OPENAPI` to a non-empty value (`true` or `false`) in the tool's `.env` file. The value wins over the library default because the library tracks the key while scanning `.env` and skips the injection step when it finds it. Note: `TRUVAG3_ENABLE_OPENAPI=` with an empty value does **not** register as "explicitly set" because the library skips empty values during its `.env` scan; to disable for a single tool, use `TRUVAG3_ENABLE_OPENAPI=false`.
2. **Globally for a run** — `export TRUVAG3_DEV_DISABLE_OPENAPI=true` before running `setup.sh rebuild`. The library skips injecting the default for every tool in that run. The ConfigMap will then have no `TRUVAG3_ENABLE_OPENAPI` at all, so the core module's own default (`false`) takes effect and the endpoint is disabled.

### Deploying Swagger UI to the Local Cluster

```bash
cd examples/k8-deployment
kubectl apply -f swagger-ui.yaml
kubectl apply -f ingress-infra.yaml
```

Or, more commonly, it is deployed as part of the infrastructure bootstrap:

```bash
./setup-infrastructure.sh
```

Access it at [http://swagger.localhost](http://swagger.localhost) once the pod is running.

---

## 5. Enterprise Integration Patterns

Here's the honest truth about adopting TruvaG3 in an enterprise environment: most teams already have an API documentation story. They already run a central Swagger UI, or Backstage, or an API gateway that ingests OpenAPI specs. They don't need another one.

Good news — you don't need any of the scaffolding above. The framework's contract is just the `/openapi.json` endpoint on each component. Everything else (the kind-cluster Swagger UI, the registry-viewer feed, the nginx proxy) is example code you can ignore. You point your existing documentation system at each service's spec URL, and it just works.

This section walks through the five most common integration patterns. Pick the one that matches your environment, or mix and match.

### Pattern 1: Centralized Swagger UI / Redocly (most common)

Most orgs already run a central Swagger UI or Redocly instance backed by a list of API URLs. Add each TruvaG3 service to that list.

For Swagger UI, the `urls` config array accepts a list of `{name, url}` pairs:

```json
[
  {"name": "weather-tool-v2", "url": "https://apis.dev.corp/weather-tool-v2/openapi.json"},
  {"name": "travel-chat-agent", "url": "https://apis.dev.corp/travel-chat-agent/openapi.json"}
]
```

The URL points to the TruvaG3 service's `/openapi.json` endpoint, exposed through whatever ingress or service mesh the team uses. No registry-viewer needed.

**Operational model**: the dev-portal team maintains the `urls.json`. When a new TruvaG3 service is promoted to dev or staging, it gets added via a PR.

### Pattern 2: Backstage (service catalog with auto-discovery)

[Backstage](https://backstage.io) is Spotify's open-source developer portal. Its software catalog has a first-class `API` kind that renders OpenAPI specs inline using a bundled Swagger UI plugin.

Register each TruvaG3 service as a Backstage `API` entity in the repo's `catalog-info.yaml`:

```yaml
apiVersion: backstage.io/v1alpha1
kind: API
metadata:
  name: weather-tool-v2
  description: Open-Meteo weather data for locations by coordinate
spec:
  type: openapi
  lifecycle: production
  owner: team-platform
  definition:
    # The exact field name and syntax for loading a spec from a URL varies
    # by Backstage version. Consult the Backstage docs for the version
    # your team runs:
    # https://backstage.io/docs/features/software-catalog/descriptor-format#kind-api
    $text: http://weather-tool-v2-service.truvag3-prod.svc.cluster.local/openapi.json
```

Backstage caches the spec, surfaces an interactive API explorer in the catalog, and links the API to its owning team, repo, and docs. Supports search and deep-linking.

**Operational model**: each service repo contains a `catalog-info.yaml` that registers the component and its API. Backstage's GitHub/GitLab discovery plugin auto-picks up new files on push. Your team's Backstage admin will know which loader syntax applies to your installation.

### Pattern 3: API Gateway Ingestion (Kong, Apigee, AWS API Gateway)

These gateways ingest OpenAPI specs to auto-configure routes, authentication, rate limiting, and request validation.

**Kong**: use the [Gateway declarative config](https://docs.konghq.com/gateway/latest/production/deployment-topologies/db-less-and-declarative-config/) with the OpenAPI plugin. Point it at each TruvaG3 service's spec URL and Kong creates routes automatically.

**Apigee**: use the [generate-apis-from-openapi](https://cloud.google.com/apigee/docs/api-platform/fundamentals/creating-api-proxies) flow with the spec URL as the source.

**AWS API Gateway**: import the spec via the console, CLI (`aws apigateway import-rest-api --body fileb://openapi.json`), or Terraform.

**Operational model**: the platform team runs a periodic job that pulls each TruvaG3 service's `/openapi.json` and pushes it through the gateway's spec importer.

### Pattern 4: CI/CD Spec Publishing (SwaggerHub, Stoplight, Git)

Some teams don't want live spec endpoints — they publish frozen specs to a registry during CI. This is common when APIs cross trust boundaries (internal → partner) or when spec history needs to be auditable.

Add a step to the build pipeline:

```bash
# Assuming the tool is running in a test environment during CI
curl -sf http://weather-tool-v2-service.ci-test/openapi.json > openapi.json

# Publish to SwaggerHub
curl -X POST https://api.swaggerhub.com/apis/myorg/weather-tool-v2/1.0.0 \
  -H "Authorization: $SWAGGERHUB_API_KEY" \
  -H "Content-Type: application/json" \
  --data @openapi.json

# Or commit to a spec repo
cp openapi.json ../api-specs/weather-tool-v2.json
cd ../api-specs && git add . && git commit -m "Update weather-tool-v2 spec" && git push
```

**Operational model**: every merge to main in a tool's repo triggers a spec publish. Consumers (partner teams, SDK generators, documentation sites) pull from the registry.

### Pattern 5: Dynamic Multi-Service Aggregation (the pattern we ship)

If your team wants the kind-cluster experience — one Swagger UI dropdown showing every live service with auto-refresh on deploy — the registry-viewer's `/swagger-urls.json` feed is a reference implementation you can port to your environment.

The endpoint reads service registrations from Redis (TruvaG3's default discovery backend) and emits the `{name, url}` array that Swagger UI expects. ~50 lines of Go in [examples/registry-viewer-app/main.go](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/main.go) (`handleSwaggerURLs`).

To adapt it:

- **Different discovery backend** (Consul, Eureka, etcd, k8s API): replace the `getRedisServices()` call with a query against your backend. The rest of the handler is unchanged.
- **Different service hosting** (not in-cluster): update the URL template. Instead of `/svc/{svc}/openapi.json` proxied via nginx, point directly at `https://apis.dev.corp/{svc}/openapi.json`.
- **Filter by team or domain**: each `ServiceInfo` carries a `Metadata map[string]interface{}` that the framework populates at registration time. Add a query-parameter filter so a team-scoped Swagger UI only shows services matching a metadata key (e.g. `team=platform`).

The key insight is that the aggregation step is **trivial** once each component publishes a standard spec. A team can build this in a day and tailor it to their environment.

---

## 6. Security Considerations

Now the tricky part. The `/openapi.json` endpoint exposes the full API surface of the component — every capability name, every endpoint path, every input and output field, every description. For a dev environment that's a feature. For production, it's something you need to think about carefully. This section tells you exactly what the endpoint reveals, what it doesn't, when it's safe to enable in production, and when it isn't.

### Production Defaults

The framework's default is `EnableOpenAPI: false`. Production deployments that do not explicitly opt in do not expose the `/openapi.json` endpoint — `GET /openapi.json` returns 404.

> **Important caveat:** disabling the OpenAPI gate does **not** fully hide a component's schema information. The framework separately registers a `GET /api/capabilities/{name}/schema` endpoint for every capability that provides an `InputSummary`, and that endpoint is wired at capability-registration time — independent of the `EnableOpenAPI` setting. It returns the full JSON Schema draft-07 form of the capability's input. This is used by the orchestration module's schema-cache validation layer (see [TOOL_SCHEMA_DISCOVERY_GUIDE.md](../building/TOOL_SCHEMA_DISCOVERY_GUIDE.md) Phase 3).
>
> The endpoint returns more limited information than `/openapi.json` (one capability's input schema at a time, nothing about outputs, nothing about the capability list), but if your threat model requires hiding all schema data from reachable callers, you must protect these endpoints at the network layer in addition to disabling the OpenAPI gate.

### What the Endpoint Reveals

Anyone who can reach the pod and is allowed to `GET /openapi.json` can see:

- All capability names, descriptions, and operation IDs
- **All endpoint paths**, including those of capabilities marked `Internal: true`. This is the most architecturally revealing part of the spec — the path structure tells a reader how the component is organized internally (`/chat/session/{id}`, `/hitl/command`, `/admin/reindex`, etc.)
- All input field names, types, examples, and descriptions (from `InputSummary`)
- All output field names, types, examples, and descriptions (from `OutputSummary`)
- Tags that distinguish `tool`, `reasoning`, `orchestrator`, and `internal` capabilities
- The framework-provided endpoints `/api/capabilities` and `/health`

The endpoint does **not** reveal:

- Secrets, API keys, or credentials (these live in environment variables and K8s Secrets, which are not exported to the spec)
- Request or response bodies from past calls (the spec is a schema, not a log)
- Other components' configuration, registration state, or discovery data
- End-user data
- Source code or stack traces

Note: the generator intentionally does not self-document `/openapi.json` or the per-capability `/api/capabilities/{name}/schema` endpoints. The latter are still reachable — see the caveat under Production Defaults above.

### When to Enable in Production

There are legitimate reasons to enable the endpoint in production:
- Internal API portal discovery
- Dynamic API gateway configuration
- Partner API documentation behind a trust boundary

In all of these cases, the endpoint should be protected at the network layer:
- Ingress rules that only allow traffic from known systems (the API portal pod, the gateway controller)
- Mutual TLS at the service mesh level
- Network policies that restrict lateral access

Do not rely on the endpoint itself for access control — it has none. It is a publishing surface, not a secure endpoint.

### When NOT to Enable in Production

- Public-facing components with anonymous access
- Components whose capability names or input schemas leak internal architecture
- Multi-tenant systems where the capability list varies by tenant
- Any deployment where the output schemas contain hints about downstream data stores or integration partners

---

# Part 2 — Registry Viewer: Runtime Observability

Sections 7 and 8 cover the second tool: the Registry Viewer web app. If Swagger UI tells you what your services *can* do, the Registry Viewer tells you what they *are* doing — right now, for any request you pick, across every layer the orchestrator touched. Section 9 ties both tools together with a decision table you can scan when you're stuck and not sure which one to open.

---

## 7. Registry Viewer Overview

Open [http://registry.localhost](http://registry.localhost) against the local kind cluster and you land in a single-page web app with five tabs across the top: **Registry**, **LLM Debug**, **HITL Interrupted**, **Execution DAG**, and **Memory**. Each tab is its own view of the live system, backed by different data in Redis (and Qdrant for memory), refreshing on demand or on a timer. Everything is read-only except for one narrow exception: the HITL view can submit Approve/Reject commands back to the agent that owns a pending checkpoint.

The app is a reference implementation — not a framework component. It ships in [`examples/registry-viewer-app/`](https://github.com/truvaagents/truva-g3/tree/main/examples/registry-viewer-app) and you can fork it, strip it down, or rewrite it for your own environment. What it demonstrates is that every piece of runtime state TruvaG3 produces is persisted in Redis (and optionally Qdrant) under well-known keys, and a read-only dashboard can be built on top of it in a few thousand lines of Go + vanilla JS.

### What It Shows You

A quick tour of the five views before we go deep:

| View | What it answers | Data source |
|------|-----------------|-------------|
| 📋 **Registry** | Which services are registered right now? What capabilities does each one expose? Are they healthy? When was each last seen? | Redis `truvag3:services:*` keys — the same data Swagger UI's `/swagger-urls.json` feed reads |
| 🔍 **LLM Debug** | Every LLM call the orchestrator made on behalf of an agent (plan generation, synthesis, tool selection, semantic retry, memory hook calls, error analysis) plus any direct AI calls an agent made through an instrumented client — with full prompts and responses, searchable across all requests and agents. How many tokens? How long? Did it error? | Redis `truvag3:llm:debug:*` (DB 7). Two writers feed the same store with the same record format: the orchestration module's `RedisLLMDebugStore` (called directly by orchestrator-internal components like compactors, resolvers, error analyzers — gated by `TRUVAG3_LLM_DEBUG_ENABLED`) and `telemetry.RedisLLMCallRecorder` (called by agents that wrap their AI client in `InstrumentedAIClient` — used for direct AI calls that don't go through the orchestrator). Both writers can be active independently. |
| ✋ **HITL Interrupted** | Is there a human approval waiting? What did the agent pause for? Who needs to act? How much time until it expires? | Redis HITL checkpoint store (`orchestration.CheckpointStore`) |
| 🔀 **Execution DAG** | For this specific request, what happened? What was the plan? How did each step run? Which LLM calls fired and why? Were memory hooks involved? Did HITL interrupt it? Was the plan regenerated mid-flight? | Redis `truvag3:execution:debug:*` — populated by the orchestrator's debug recorder |
| 📝 **Memory** | What's in shared memory right now for this domain? What events happened in the last 24 hours? What investigations are active? What's the current digest the LLM sees? | Redis shared-memory stores + optional Qdrant for knowledge vectors |

All five views have the same layout shape: a **list panel** on the left (filterable, sortable, searchable) and a **detail panel** on the right that populates when you pick a row. Each view's detail panel has its own tabs, and the header shows per-view stats (record count, token count, pending count, etc.).

### How It Gets Its Data

The Registry Viewer is a thin reader over Redis and Qdrant. Nothing more. It does not scrape logs, does not talk to Prometheus, and does not invoke agents directly (except when you click Approve/Reject on a HITL checkpoint — that's a signed command POST back to the owning agent). Every piece of data it shows comes from one of these sources:

| Source | What it holds | Which views use it |
|--------|---------------|--------------------|
| Redis `truvag3:services:*` (DB 0) | Service registrations (tools and agents) with TTL refresh | Registry view; also feeds `/swagger-urls.json` for the Swagger UI dropdown |
| Redis `truvag3:llm:debug:*` (DB 7) | Full LLM interaction records (prompt, response, tokens, duration, type, category, success). The store has two writers (see `truvag3:llm:debug:*` row in §7 view summary above): the orchestration module's `RedisLLMDebugStore` and `telemetry.RedisLLMCallRecorder`. Both write the same record format. | LLM Debug view; DAG detail panel's LLM Calls tab fetches the same record by request ID |
| Redis HITL checkpoint store (`{base-prefix}[:{agent-name}]:checkpoint:*`; base defaults to `truvag3:hitl`) | Pending and expired checkpoints with plan, current step, resolved parameters, decision, status, agent name, agent address, request mode. Configure the base with `TRUVAG3_HITL_KEY_PREFIX`; the framework appends `TRUVAG3_AGENT_NAME` or `TRUVAG3_K8S_SERVICE_NAME` when present. | HITL Interrupted view; DAG detail panel's HITL tab |
| Redis `truvag3:execution:debug:*` (DB 8) | Full execution records (plan, per-step results, LLM interactions for pre/post hooks, HITL events, phase count, regeneration events). Written by `orchestration.RedisExecutionDebugStore` independently of the LLM debug store — the two have separate enable flags and separate Redis databases. | Execution DAG view |
| Redis shared memory keys (episodic events, activities, investigations, digest cache) | Source agents' episodic events; live activity signals from `ActivityCoordinator`; investigation locks from `InvestigationCoordinator`; cached compaction digests | Memory view |
| Qdrant collections (when `TRUVAG3_VECTOR_DB_URL` is set) | Semantic knowledge vectors (Phase 2 shared memory) | Memory view (knowledge detail) — currently behind feature flag |

Because everything flows through Redis and Qdrant, **the Registry Viewer has no authority over the system** — killing it or restarting it loses zero data. It is a lens, not a source of truth.

> **Privacy design note — user memory.** You'll notice the Memory view only shows **shared memory** (domain-scoped events and investigations). There is no tab, list, or browser for **user memory** (per-user facts). This is intentional: user memory holds potentially personal or sensitive data, and exposing it in a general-purpose developer dashboard would be the wrong default. If a developer legitimately needs to inspect what's stored for a given user, they query the vector DB (Qdrant) and the user-memory Redis keys directly with the credentials they already have. The dashboard stays generic; the privacy-bearing data stays behind an explicit access step. The Execution DAG view still surfaces **user-memory activity** during a specific execution — which recall and extraction calls ran, how long they took, whether reconciliation fired — but it never shows the *contents* of the facts stored, only the hook invocations.

### Prerequisites

For the views to show data, the underlying features have to be enabled in your cluster:

| View | What must be true for data to appear |
|------|--------------------------------------|
| Registry | Services must register with a shared Redis via `core.WithDiscovery(true, "redis")`. This is the default for every example tool/agent in the repo. |
| LLM Debug | Two enablement paths, often both at once: (a) **Orchestrator-internal calls** (`plan_generation`, `tiered_selection`, `synthesis_streaming`, memory hooks, error analysis, etc.) populate the store automatically when the orchestrator is constructed with `TRUVAG3_LLM_DEBUG_ENABLED=true` — the orchestration module's `RedisLLMDebugStore` is wired up by the factory. (b) **Direct agent AI calls** (an agent that calls `ai.GenerateResponse(...)` outside of an orchestration step) populate the store only when the agent wraps its AI client with `ai.NewInstrumentedClient(..., debugRecorder, ...)` using `telemetry.NewRedisLLMCallRecorder`. See [`examples/agent-with-telemetry/research_agent.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-telemetry/research_agent.go) for the wiring. Most chat agents in the kind cluster don't need (b) because their LLM calls go through the orchestrator and are captured by (a). |
| HITL Interrupted | At least one agent must run the orchestration module with HITL enabled (`HITLConfig.Enabled: true` and a configured checkpoint store). See [HUMAN_IN_THE_LOOP_USER_GUIDE.md](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md). |
| Execution DAG | The orchestrator must run with `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true`. This is **independent** of `TRUVAG3_LLM_DEBUG_ENABLED` — the execution debug store and the LLM debug store live in separate Redis databases (DB 8 and DB 7) and are wired by separate factory branches. In practice you almost always want both enabled, because the DAG view's LLM Calls tab pulls data from the LLM debug store. See the per-agent enablement table below — not every example chat agent in the kind cluster has both set, and the asymmetric setups are the most common gotchas. |
| Memory | Agents must be wired with shared memory via `memory.NewSharedBackends(...)` and `orchestration.BuildMemoryHooks(...)`. See [AGENT_MEMORY_USER_GUIDE.md](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md). `TRUVAG3_AGENT_DOMAIN` must be set so the view has a domain to pick from in its dropdown. |

On the local kind cluster, **enablement varies per agent** because the env vars are independent and each example was originally written for its own purpose:

| Agent | `TRUVAG3_LLM_DEBUG_ENABLED` | `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED` | Implication |
|-------|:--:|:--:|---|
| `travel-chat-agent` | ❌ | ✅ | Appears in Execution DAG view; its orchestrator-internal LLM calls are NOT recorded to LLM Debug, so the DAG's LLM Calls tab is empty for its records |
| `devops-chat-agent` | ✅ | ✅ | Fully populated across both views |
| `qa-agent` | ✅ | ✅ | Fully populated |
| `event-driven-agent` | ✅ | ✅ | Fully populated |
| `agent-with-telemetry` (research-agent-telemetry) | ✅ | ❌ | Its direct AI calls (instrumented client) appear in LLM Debug, but it does NOT appear in the Execution DAG view at all because the execution store isn't enabled |

So the most common gotchas are exactly the asymmetric setups above: a request that shows up in DAG but has an empty LLM Calls tab (executor enabled, LLM debug disabled), or a record that shows up in LLM Debug but has no corresponding row in DAG (LLM debug enabled, executor disabled). Always check both env vars on a new agent.

---

## 8. The Five Views

This section walks through each view in detail: what the list panel shows, what the filters do, what each detail tab reveals, and concretely when to open that view while debugging. The views have similar shapes, so once you understand one, the others follow the same pattern.

### 8.1 Registry View — Service Catalog

**Use this view when you want to ask:** "Is my new tool actually registered? Is it healthy? When was it last seen? What capabilities did it publish?"

This is the simplest view and the one you'll open first when checking whether a deployment succeeded. It lists every tool and agent currently registered in Redis under `truvag3:services:*`, with the Redis TTL driving health — a service that stops sending heartbeats drops off the list within 30 seconds.

**List panel.** A sortable table with columns:
- **Type** — `tool` or `agent` (with an icon for quick scanning)
- **Name** — service name as registered (e.g. `weather-tool-v2`)
- **Health** — color-coded badge: `healthy`, `degraded`, `unhealthy`, or `unknown`
- **Last Seen** — relative time since the last heartbeat ("3s ago", "12m ago")
- **Capabilities** — count; click the row to expand in the detail panel

**Filters.** A search box (matches name, description, and capability names) plus three filter buttons: **All / Agents / Tools**.

**Detail panel.** Pick a service and the right side shows its registration record in two tabs:
- **Formatted** — human-readable breakdown organized into sections: **Service Info** (ID, name, type, health, last seen, description, address, port), **Metadata** (any registered metadata key/value pairs), and **Capabilities** (a card per capability showing name, internal/external badge, description, endpoint, and the input parameter list — required fields shown as `name: type` tags, optional as `name?: type`). Each capability card has a "View Full JSON" expandable for the raw capability object.
- **JSON** — the raw service registration record returned by `/api/services`, syntax-highlighted.

> **Note on what this view does NOT show.** The registry-viewer's backend uses its own narrower `Capability` struct that omits the `OutputSummary` field — it only surfaces `InputSummary`. So neither the Formatted view nor the JSON view will show output schemas for capabilities, even when the underlying agent registered them. To see output schemas, open the same service in **Swagger UI** instead — Swagger UI fetches the agent's `/openapi.json` directly, which includes both input and output schemas. This is purely a registry-viewer-side gap, not a framework-level one.

**Header stats.** Total, Agents, Tools.

**Typical workflow.** You deploy a new tool, port-forward or open http://registry.localhost, switch to the Tools filter, search for your tool name, and confirm (a) it appears, (b) health is green, (c) the capability you just added is listed with the field hints you expected. If any of those are missing, the problem is in the tool's startup — it never reached `Initialize()` or `Start()` successfully, or `RegisterCapability()` wasn't called.

### 8.2 LLM Debug View — Raw LLM Call Stream

**Use this view when you want to ask:** "What LLM calls has this system made? How many tokens are we burning? Why did that call fail? Show me the exact prompt and the exact response."

This is the flat, searchable log of every LLM interaction recorded into the LLM debug store, written by two sources working in parallel: the orchestration module's internal components (planner, synthesizer, tool selector, memory hooks, error analyzer, semantic retry — all of which write directly to the store when `TRUVAG3_LLM_DEBUG_ENABLED=true`) and any agent that wraps its AI client with `InstrumentedAIClient` for direct LLM calls outside of orchestration. Unlike the DAG view (which assembles everything for a single execution), this view cuts across all executions, all agents, and all categories.

**List panel.** A sortable table with columns:
- **Request ID** — the orchestration request ID this LLM debug record belongs to (e.g. `orch-1775509658444116982`)
- **Conversation** — `original_request_id || request_id`. For a one-shot request these are the same value, so this column is **never blank**; it just mirrors the Request ID. For a HITL conversation that spans multiple resume requests, all related records share the same `original_request_id`, and the field is prefixed with a 🔗 icon and rendered as a clickable link that filters the table to the linked records.
- **Source** — `source_components`, an **array** of agent/component names that contributed LLM calls to this record. When the array is empty (orchestrator-internal calls only — plan_generation, synthesis, etc.), the column shows a single `orchestrator` badge. When agents wrap their AI client with `InstrumentedAIClient` and pass `WithComponentName(...)`, their names appear here as additional badges. So a record can show one orchestrator badge, one or more agent badges, or both.
- **Interactions** — how many distinct LLM calls rolled up under this request ID (plan + synthesis + retries + memory hooks + everything else)
- **Tokens** — total tokens for the record (sum of all interactions)
- **Status** — `Success` or `Has Errors` (set when any interaction in the record failed)
- **Time** — relative time since the record was created

**Filters.** Search box (matches request ID, source component name, or interaction type strings) plus four filter buttons:
- **All** — every record
- **Success** — only records where all interactions succeeded
- **Errors** — only records with at least one failed interaction
- **Grouped** — sorts the table by `original_request_id` then `created_at`, so all records belonging to the same HITL conversation cluster together. Useful when one user-visible conversation spans multiple checkpoint resumes — each resume becomes its own request_id but shares the original_request_id. Combined with the 🔗 click-to-filter on the Conversation column, this is the workflow for inspecting a multi-step HITL session end-to-end.

**Detail panel.** Pick a record and two tabs open:
- **Interactions** — an expandable list of every LLM call in the record, each showing: type (`plan_generation`, `tiered_selection`, `synthesis_streaming`, `user_memory_reconciliation`, etc. — see §8.4 for the full list), category (typically `llm` or `embedding`), duration, token counts (prompt + completion + total), model, attempt number, success flag, and the full prompt and response. Click an interaction to expand it; click again to collapse. There's an "expand all" button when you need to scan fast.
- **JSON** — the full record as stored in Redis, for when you want to pipe it into `jq` or share it in a bug report.

**Header stats.** Records, Total Tokens.

**Typical workflow.** Something weird happened — an orchestrator picked the wrong tool, or the synthesis is nonsense, or the LLM refused a task. You open LLM Debug, find the request by ID (or by searching the conversation text), and walk through the interactions. Within seconds you can tell whether the problem was bad tool-selection prompting, a broken plan-generation output, a stale memory digest, or a retry loop that burned tokens without progressing.

**Gotcha.** If a request is missing from this view entirely, check that the agent that handled it has `TRUVAG3_LLM_DEBUG_ENABLED=true` — the orchestration module's LLM debug store is gated on this env var. If the request appears but the **Source** column shows only an `orchestrator` badge (no agent name), the agent's direct AI client isn't wrapped with `InstrumentedAIClient`. That's fine for chat agents whose LLM calls all flow through the orchestrator (the orchestrator handles them), but it means a custom agent that calls `ai.GenerateResponse(...)` directly outside of an orchestration step won't have its direct calls attributed to it. See [`examples/agent-with-telemetry/research_agent.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-telemetry/research_agent.go) for the wiring pattern.

### 8.3 HITL Interrupted View — Pending Approvals

**Use this view when you want to ask:** "Is anything waiting on me right now? Which agent paused for approval, and what did it want to do?"

This is the operational queue for Human-in-the-Loop checkpoints. Every time an agent hits a sensitive capability (or a full-plan approval gate) and pauses, a checkpoint record lands in Redis with a TTL. This view shows every pending one, sorted by expiry (most-urgent-first by default), and lets a developer review and respond.

**List panel.** A sortable table with columns:
- **Priority** — derived from the interrupt reason and time remaining; expiring-soon checkpoints bubble to the top
- **Agent** — which agent created the checkpoint (sortable)
- **Type** — `plan` (full-plan approval before any step runs) or `step` (step-level sensitive capability) or `error` (semantic retry requesting guidance)
- **Request** — the original user request that triggered the workflow
- **Progress** — how many steps have already run vs. how many remain
- **Created** — when the checkpoint was created (sortable)
- **Expires** — time until the checkpoint auto-expires (sortable, default-sorted ascending so the most urgent are at the top)

**Filters.** Search box (matches checkpoint ID, request text, or decision message) plus **All / Plan / Step / Error**.

**Detail panel.** Pick a checkpoint and three tabs open:
- **Overview** — the essential info: which agent, why it paused, the blocked step (with resolved parameters — not templates, so a developer can see `amount: 15000` instead of `amount: "{{step-1.total}}"`), the interrupt decision with reason and sensitive-capability matches, and the action panel.
- **Plan** — the full `RoutingPlan` that was running, rendered as a step list so you can see the full intended workflow, not just the blocked step.
- **JSON** — the complete `ExecutionCheckpoint` object.

**Header stats.** Pending, Critical.

**Action panel — when buttons appear.** The Overview tab's action panel adapts based on the checkpoint's `request_mode` field:
- **Non-streaming checkpoints** (`request_mode == "non_streaming"`): inline **Approve & Resume** and **Reject** buttons that POST to `/api/hitl/command` (proxied from the registry-viewer to the owning agent). An optional reason text box is rendered above the buttons. Buttons are disabled if the checkpoint has no `agent_name` set, with a warning suggesting `TRUVAG3_AGENT_NAME` needs to be configured on the agent.
- **Streaming checkpoints** (`request_mode == "streaming"`): the buttons are **hidden** and replaced with a warning: *"Streaming request — approve/reject must be performed by the connected client."* This is intentional — for streaming HITL, the UI that launched the conversation is the only place that can complete the round-trip cleanly. The dashboard reverts to a read-only view of the checkpoint, useful for inspecting state but not for resolving it.

**Typical workflow.** An agent paused a destructive operation (say, a Kubernetes rollout restart) and you want to review it before approving. You open this view, see the pending checkpoint at the top of the list, switch to the Plan tab to confirm the full workflow makes sense, check the resolved parameters on the blocked step, and either approve (which resumes the workflow from where it paused) or reject. If you walk away and the checkpoint expires, the default action configured in `HITLConfig.DefaultAction` kicks in.

### 8.4 Execution DAG View — The Flagship Debugging View

**Use this view when you want to ask:** "For this specific request, walk me through exactly what happened — the plan, the steps, the LLM calls, the hooks, the memory, everything — in one place."

This is the view you open when logs aren't enough. It takes a single orchestration execution and assembles **every piece of data the orchestrator recorded about it** into one interactive timeline: the planned DAG, per-step results, all LLM calls grouped by phase, pre-execution hooks, post-execution hooks, HITL checkpoints that fired during the run, and any plan regenerations that happened mid-flight. This is the most information-dense view in the app and where most debugging time is spent.

**List panel.** A sortable table with columns:
- **Status** — success / failed / interrupted (with colored icon)
- **Request** — the original user query text (sortable by request text)
- **Steps** — how many steps the plan had
- **Duration** — total execution time (sortable)
- **Created** — when the request started (sortable, default-sorted descending so the newest is at the top)

**Filters.** Search box (matches request ID or query text) plus **All / Success / Failed / Interrupted**.

**Detail panel — seven tabs**, most of which only appear when the execution has relevant data. The visibility rules are: LLM-Calls appears if the record has any LLM interactions, Pre-Execution and Post-Execution appear if any pre/post hook ran, HITL appears if any checkpoint fired during this execution, and the other three are always visible.

#### Tab 1: DAG Visualization

The headline tab. Renders the execution as an interactive graph using Cytoscape.js with the Dagre layout algorithm. Each node is a step, each edge is a dependency (explicit `depends_on` or implicit sequential). Click a node to see that step's details inline; click an edge to see the template binding that ties the two steps together.

The header strip above the canvas shows a comprehensive metadata line:
- Original request text (click to expand/collapse if truncated)
- Agent name (which agent owned this execution)
- Request ID and trace ID (W3C trace ID for Jaeger cross-reference)
- Step count and total duration (executor time + LLM time, both measured and displayed)
- Success / failed / interrupted badge
- LLM call count (if any)
- HITL badge (if HITL fired but didn't interrupt)
- Phase count (for iterative multi-phase plans — shown when > 1)
- Forced-terminal badge (when the orchestrator had to force-terminate a phase)

Below the header, if the plan was regenerated mid-flight, a warning strip shows each regeneration event: which phase regenerated, what validation error caused it, and the old → new plan ID transition. This is the single biggest debugging shortcut for "why did the orchestrator change its mind halfway through?"

A **Steps Only / Full Flow toggle** at the top switches between two modes:
- **Steps Only** — just the tool-invocation steps, clean and compact
- **Full Flow** — steps + every LLM planning/synthesis call + every HITL checkpoint, interleaved in chronological order. This is the full timeline of the execution from the orchestrator's point of view

#### Tab 2: Pre-Execution

Only visible when the execution had `BeforePlanning` hooks that ran. Shows the hook activity that happened *before* the planner even saw the prompt:

- **User Memory Enrichment** — which recall calls ran (`user_memory_recall_identity`, `user_memory_recall_summary`, `user_memory_recall_query`, `user_memory_recall_universal`), how long each took, and whether the `user_memory_enrichment_injected` step succeeded (meaning the `<user_profile>` XML fragment made it into the plan-generation prompt). Note: this tab shows that the enrichment ran, not the contents of the profile itself — see the privacy note in §7.
- **Memory Compaction** — `activity_compaction_incremental` calls and their durations. Useful for diagnosing slow first-requests where the digest cache was cold.

This tab is the place to look when you suspect the LLM didn't see the context you expected.

#### Tab 3: Step Details

Flat per-step list with everything the orchestrator recorded about each step: step ID, tool/agent name, resolved capability, resolved parameters (not templates), the response body, success flag, error message if any, duration, attempt count (retries), start and end timestamps, and any metadata the step recorded (e.g., HITL checkpoint metadata, resolution-layer info from the 4-layer parameter binding system).

When a step retried, you can see exactly how many attempts happened and which resolution layer fixed the parameters on the retry — the orchestration module's 4-layer parameter-binding story is fully visible here.

#### Tab 4: LLM Calls

Only visible when the execution had LLM interactions recorded. Shows every LLM call the orchestrator made during this execution, in order, with full prompts and responses. This is functionally a filtered slice of LLM Debug constrained to one execution, rendered inline alongside the DAG context.

Each call shows: type (e.g. `plan_generation`, `continuation_plan_generation`, `continuation_plan_regeneration`, `tiered_selection`, `synthesis_streaming`, plus the `user_memory_*` family — `user_memory_recall_identity`, `user_memory_recall_summary`, `user_memory_recall_query`, `user_memory_recall_universal`, `user_memory_enrichment_injected`, `user_memory_extraction`, `user_memory_similarity_search`, `user_memory_reconciliation`, `user_memory_remember`, `user_memory_summary`, `user_memory_summary_remember`), category (`llm`, `embedding`), model, token counts (prompt + completion + total), duration, attempt number, temperature, max tokens, success flag, full prompt, and full response. Click an interaction to expand the prompt or response; they're long.

**When to open this tab vs. the LLM Debug view:** use this tab when you already have the request ID open and want to see the LLM calls in the context of the steps they produced. Use the LLM Debug view when you want to search across requests (e.g., "show me every synthesis call that errored yesterday").

#### Tab 5: Post-Execution

Only visible when the execution had `AfterSynthesis` hooks that ran. The mirror image of Pre-Execution:

- **Event Summarization** — LLM calls that generated the episodic event text written to shared memory
- **User Memory Write-back** — `user_memory_extraction`, `user_memory_embed_candidate`, `user_memory_similarity_search`, `user_memory_reconciliation` (per-candidate path) or `user_memory_reconciliation_batch` + per-item `user_memory_reconciliation_batch_item` rows (batched path), `user_memory_reconciliation_skip` (no neighbors), and `user_memory_remember`. The batched path emits ONE `user_memory_reconciliation_batch` row with category `llm` carrying the full token usage, plus N lightweight `_batch_item` rows with category `derived` so the registry viewer's LLM-call totals are not double-counted. Same privacy rule as Pre-Execution: you see the invocations, not the fact contents.

This tab answers "what did the system learn from this request?"

#### Tab 6: HITL

Only visible when a checkpoint fired during this execution. Shows the checkpoint lifecycle *within* the execution timeline:
- When the checkpoint was created
- What decision triggered it (policy match, sensitive-capability match, semantic retry request)
- When and how it was resolved (approve / reject / expire with default action)
- The resumed execution path, if any

Useful for tracing "the user approved X, then what happened?"

#### Tab 7: Raw JSON

The full execution record as stored in Redis under `truvag3:execution:debug:*`. When you need to copy-paste into a bug report or pipe into external tooling.

**Header stats.** Executions, Success Rate.

**Typical workflow.** The chat UI shows a weird answer, or an agent took 40 seconds when you expected 4. You grab the request ID from the chat UI or from the agent's logs, open DAG view, paste the ID into the search box, and click into the record. The DAG Visualization tab gives you the shape immediately; the Step Details tab shows which step was slow; the LLM Calls tab shows the prompts that drove the decisions; and the Pre-/Post-Execution tabs reveal whether the memory hooks did something unexpected.

### 8.5 Memory View — Shared Memory Inspector

**Use this view when you want to ask:** "What does the LLM know right now? What events happened in the last 24 hours? Are any investigations in progress? What does the compacted digest look like?"

This view is for inspecting the state of **shared memory** (domain-scoped). It answers "what does my domain's memory look like right now, and what's the LLM currently seeing when it generates plans?"

**List panel.** The left side has three controls instead of a table:
- **Domain dropdown** — populated from Redis (`/api/memory/domains`). Defaults to the last domain you picked (stored in localStorage). Agents from the same domain share memory, so you pick the domain to inspect. Typical values: `infrastructure`, `travel`, `devops`.
- **Time range buttons** — `1h`, `6h`, `24h` (default), `3d`, `7d`. Filters the events list below.
- **Investigations strip** — a horizontal scrolling bar showing currently-active investigations from the `InvestigationCoordinator`. Each entry is a pill rendered as `entity_id (holder)` — the entity that's being investigated (e.g. a pod name or deployment name) and the agent currently holding the investigation lock. Strip is hidden entirely when there are no active investigations. The underlying data comes from `/api/memory/investigations?domain={domain}` which returns `entity_id`, `holder`, and `domain` only — there's no started-at timestamp in the response.
- **Events list** — the actual event timeline for the chosen domain and time range, with relative timestamps, source agents, entity references, and outcomes.

**Filters.** Type in the search area to narrow the events list; click an event to pull it into the detail panel.

**Detail panel — three tabs:**
- **Event Detail** — everything about the selected event: source agent, request ID, timestamp, entities, actions, outcome, and any metadata the event carries.
- **Live Activity** — the real-time activity signals from other agents in the same domain. This is the data that goes into the `<agent_coordination>` XML block the LLM sees before planning.
- **Digest** — the compacted LLM-generated summary of domain activity, as it would appear in the `<agent_memory>` XML block at plan time. This is the single most useful tab when you're debugging "why did the LLM make that assumption?" — the digest is what the LLM *actually reads* on each request, and it's the output of an LLM call itself (compaction).

**Header stats.** Domain, Events, Investigations.

**What this view does not show.** As noted in §7, **user memory is not surfaced here by design**. There is no per-user fact list, no browser for the vector DB entries, and no "what does this system remember about alice@example.com" lookup. User-memory activity is visible in the Execution DAG view (Pre-Execution and Post-Execution tabs) at the hook-invocation level, but never at the fact-contents level. If you need to inspect the actual facts stored for a user, query Qdrant directly using the collection name and user ID key — credentials are the same ones the agent uses.

**Typical workflow.** Two agents keep making conflicting decisions about the same entity (say, the same Kubernetes deployment). You open Memory view, pick the `infrastructure` domain, widen the time range to 7 days, and scroll the event list looking for what each agent recorded about that entity. The Digest tab tells you what the LLM is currently reading about the domain; the Live Activity tab tells you whether one of the agents is mid-investigation right now.

---

## 9. Which Tool for What: A Decision Table

When you're stuck and not sure which tab to open, scan this table.

| I want to... | Open | In view | Tab / filter |
|--------------|------|---------|--------------|
| Try calling a tool's capability with a filled-in form | Swagger UI | dropdown → service | "Try it out" on the operation |
| Know whether my new tool registered at all | Registry Viewer | Registry | All filter, search for name |
| See the exact `InputSummary` AND `OutputSummary` a capability published | Swagger UI | dropdown → service | look at `requestBody` and `responses.200` |
| See only the `InputSummary` (input fields and types) plus the rest of the registration record | Registry Viewer | Registry | click service → Formatted or JSON tab. Note: `OutputSummary` is NOT exposed by registry-viewer's API — use Swagger UI for output schemas |
| Find every LLM call that errored today | Registry Viewer | LLM Debug | Errors filter |
| Read the exact prompt the planner sent for request X | Registry Viewer | LLM Debug | search by request ID → Interactions tab → `plan_generation` |
| See every LLM call for request X in the context of the steps they produced | Registry Viewer | Execution DAG | search by request ID → LLM Calls tab |
| Check total token usage across all agents | Registry Viewer | LLM Debug | header stats (Total Tokens) |
| Visualize the DAG the orchestrator planned for a request | Registry Viewer | Execution DAG | search by request ID → DAG Visualization tab |
| See which memory recall calls fired before planning on a specific request | Registry Viewer | Execution DAG | click request → Pre-Execution tab |
| Find out why the orchestrator regenerated a plan mid-flight | Registry Viewer | Execution DAG | click request → DAG Visualization → look for the regeneration warning strip |
| Walk through per-step parameters (resolved, not template) for a failed request | Registry Viewer | Execution DAG | click request → Step Details tab |
| See the 4-layer parameter resolution metadata for a retry | Registry Viewer | Execution DAG | click request → Step Details tab → step metadata |
| Find any pending HITL approvals right now | Registry Viewer | HITL Interrupted | All filter, default sort shows most-urgent first |
| Approve or reject a paused workflow (dev testing) | Registry Viewer | HITL Interrupted | click checkpoint → Overview tab → Approve/Reject buttons (non-streaming checkpoints only; streaming checkpoints must be resolved by the connected client) |
| See the full `RoutingPlan` for a paused checkpoint | Registry Viewer | HITL Interrupted | click checkpoint → Plan tab |
| Inspect what the LLM is currently reading from shared memory | Registry Viewer | Memory | pick domain → Digest tab |
| See recent events in a domain | Registry Viewer | Memory | pick domain + time range → events list |
| Find which agents are actively working on the same entity right now | Registry Viewer | Memory | investigations strip at the top, or Live Activity tab on an event |
| Inspect stored facts for a specific user | **Not in the dashboard** (by design) | — | Query Qdrant directly or use the agent's user-memory HTTP endpoints |
| Generate an OpenAPI spec to feed into Kong / Apigee / Backstage | Swagger UI | — | `GET /openapi.json` directly from each service |
| Confirm `/openapi.json` is enabled on a component | `curl` | — | `curl http://svc/openapi.json` — 404 means disabled, 200 means enabled |

---

## 10. See Also

**Framework contract (what ships in core):**
- **[`core/openapi.go`](https://github.com/truvaagents/truva-g3/blob/main/core/openapi.go)** — Spec generator and `/openapi.json` HTTP handler. Only registered when `EnableOpenAPI` is true.
- **[`core/config.go`](https://github.com/truvaagents/truva-g3/blob/main/core/config.go)** — `HTTPConfig.EnableOpenAPI` field, `WithOpenAPI(bool)` option, and `TRUVAG3_ENABLE_OPENAPI` env var loader. Default is `false`.
- **[`core/tool.go`](https://github.com/truvaagents/truva-g3/blob/main/core/tool.go)** / **[`core/agent.go`](https://github.com/truvaagents/truva-g3/blob/main/core/agent.go)** — `/openapi.json` endpoint gate in `setupStandardEndpoints()` / `Start()`. Also the `/api/capabilities/{name}/schema` endpoint registration in `RegisterCapability()` — note that this one is **NOT** gated by `EnableOpenAPI` (see [Section 6](#6-security-considerations)).
- **[`core/openapi_test.go`](https://github.com/truvaagents/truva-g3/blob/main/core/openapi_test.go)** — Unit tests for the on/off gate (`TestOpenAPIEndpoint_*`) and the spec generator (`TestGenerateOpenAPISpec_*`).

**Swagger UI scaffolding (local kind cluster only):**
- **[`examples/k8-deployment/swagger-ui.yaml`](https://github.com/truvaagents/truva-g3/blob/main/examples/k8-deployment/swagger-ui.yaml)** — Swagger UI Deployment + Service + ConfigMap + nginx proxy for the local kind cluster.
- **[`examples/registry-viewer-app/main.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/main.go)** — `handleSwaggerURLs` is the reference implementation for [Pattern 5](#pattern-5-dynamic-multi-service-aggregation-the-pattern-we-ship). Reads from Redis and emits a Swagger UI compatible `urls` feed.
- **[`examples/k8-deployment/setup-env-lib.sh`](https://github.com/truvaagents/truva-g3/blob/main/examples/k8-deployment/setup-env-lib.sh)** — `truvag3_create_configmap` injects `TRUVAG3_ENABLE_OPENAPI=true` for every tool deployed via the example setup scripts. Dev-workflow only.

**Registry Viewer implementation:**
- **[`examples/registry-viewer-app/`](https://github.com/truvaagents/truva-g3/tree/main/examples/registry-viewer-app)** — Full app source (Go backend + vanilla-JS frontend).
- **[`examples/registry-viewer-app/main.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/main.go)** — Backend: Redis scan loops, HTTP handlers for every view's data, HITL command proxy.
- **[`examples/registry-viewer-app/static/js/views/registry.js`](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/static/js/views/registry.js)** — Registry view (§8.1).
- **[`examples/registry-viewer-app/static/js/views/llm-debug.js`](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/static/js/views/llm-debug.js)** — LLM Debug view (§8.2).
- **[`examples/registry-viewer-app/static/js/views/hitl.js`](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/static/js/views/hitl.js)** — HITL Interrupted view (§8.3).
- **[`examples/registry-viewer-app/static/js/views/dag.js`](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/static/js/views/dag.js)** — Execution DAG view (§8.4) — the largest view, ~3,300 lines.
- **[`examples/registry-viewer-app/static/js/views/memory.js`](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/static/js/views/memory.js)** — Memory view (§8.5).
- **[`examples/registry-viewer-app/k8-deployment.yaml`](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/k8-deployment.yaml)** — Kubernetes deployment and service.
- **[`examples/k8-deployment/ingress-infra.yaml`](https://github.com/truvaagents/truva-g3/blob/main/examples/k8-deployment/ingress-infra.yaml)** — Ingress rule exposing `registry.localhost`.

**Related guides:**
- **[Tool Development Guide](../building/TOOL_DEVELOPMENT_GUIDE.md)** — How to write good `InputSummary` and `OutputSummary` when registering capabilities. Spec quality depends on this.
- **[Tool Schema Discovery Guide](../building/TOOL_SCHEMA_DISCOVERY_GUIDE.md)** — The 3-phase schema architecture (descriptions → field hints → full JSON Schema) that underpins all capability metadata. Phase 3 is what the per-capability `/schema` endpoint implements.
- **[Agent Memory User Guide](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md)** — How to wire shared and user memory into an agent. The Memory view in the Registry Viewer reads the storage this guide tells you how to set up.
- **[Human-in-the-Loop User Guide](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md)** — How HITL checkpoints work. The HITL Interrupted view in the Registry Viewer is an operational dashboard for the checkpoints this guide tells you how to create.
- **[Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md)** — How W3C trace IDs flow through TruvaG3. The DAG view surfaces trace IDs so you can pivot into Jaeger for even lower-level detail.
- **[Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md)** — Full list of `TRUVAG3_*` environment variables, including `TRUVAG3_ENABLE_OPENAPI`, `TRUVAG3_LLM_DEBUG_ENABLED`, and `TRUVAG3_AGENT_DOMAIN`.
- **[Architecture Overview](https://github.com/truvaagents/truva-g3/blob/main/docs/overview/ARCHITECTURE.md)** — Framework-wide architectural overview.
