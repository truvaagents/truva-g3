# Agent Skills Guide

Agent skills let you package reusable domain guidance once and deliberately
attach it to the agents that should use it. The framework then decides when the
guidance is relevant, loads only the content needed for the current execution
stage, and records enough evidence to explain what happened.

This guide starts with the general idea of an agent skill, then walks through
the complete TruvaG3 lifecycle: authoring, validation, publication, agent
binding, runtime selection, multi-phase execution, operations, observability,
and Registry Viewer workflows.

> **Working examples**
>
> The examples below are grounded in the shipped implementations:
>
> - [`examples/travel-chat-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/travel-chat-agent) — `always` and `auto` skills in a multi-tool travel agent
> - [`examples/devops-chat-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/devops-chat-agent) — a required safety skill, an automatic investigation skill, and human-in-the-loop (HITL) execution
> - [`examples/registry-viewer-app/`](https://github.com/truvaagents/truva-g3/tree/main/examples/registry-viewer-app) — skill management and execution inspection user interface (UI)
>
> Run examples through their `setup.sh` scripts. The Travel and DevOps setup
> scripts validate and conditionally publish their bound skill packages before
> restarting the agent.

---

## Table of Contents

- [1. What is an agent skill?](#1-what-is-an-agent-skill)
- [2. How TruvaG3 skills work](#2-how-truvag3-skills-work)
- [3. Quick start](#3-quick-start)
- [4. Author a skill package](#4-author-a-skill-package)
- [5. Validate, analyze, and publish](#5-validate-analyze-and-publish)
- [6. Bind skills to an agent](#6-bind-skills-to-an-agent)
- [7. Runtime lifecycle](#7-runtime-lifecycle)
- [8. Multi-phase execution, regeneration, and HITL](#8-multi-phase-execution-regeneration-and-hitl)
- [9. Configuration and customization](#9-configuration-and-customization)
- [10. Integrity, caching, versions, and consistency](#10-integrity-caching-versions-and-consistency)
- [11. Skills management lifecycle](#11-skills-management-lifecycle)
- [12. Registry Viewer](#12-registry-viewer)
- [13. Observability](#13-observability)
- [14. Kubernetes and multi-replica operations](#14-kubernetes-and-multi-replica-operations)
- [15. Testing skills](#15-testing-skills)
- [16. Troubleshooting](#16-troubleshooting)
- [17. FAQ](#17-faq)
- [18. Quick reference](#18-quick-reference)

If you are new to agent skills, read Sections 1–8 first. Application developers
will usually need Sections 4–10 and 15. Platform operators can focus on
Sections 10–14 and 16. The API and environment-variable quick reference is in
Section 18.

---

## 1. What is an agent skill?

An agent skill is a reusable package of instructions and reference material
that teaches an agent how to handle a class of tasks.

For example, a travel-weather skill might teach an agent to:

- resolve a destination to coordinates before calling a forecast tool;
- consider how far into the future the forecast extends and how uncertain it is;
- load a severe-weather checklist only when disruption risk matters; and
- distinguish observed conditions from forecast uncertainty in the answer.

The skill does not perform those actions itself. It guides the agent while the
agent's existing tools and orchestration perform the work.

### Skills, tools, prompts, and memory are different

| Concept | What it contributes | What it does not do |
|---|---|---|
| **Skill** | Reusable step-by-step and response guidance, tool hints, and selectively loaded reference material | It does not create a capability or grant authority. |
| **Tool** | An executable capability such as `get_weather` or `query_prometheus` | It does not decide the complete task strategy. |
| **Agent prompt** | The agent's stable identity, role, and general behavior | It is not the best home for specialized procedures needed only for certain requests. |
| **Memory/context** | User, conversation, or operational facts relevant to this request | It is data, not trusted procedural policy. |
| **Pipeline hook** | Application logic at a defined point in the execution lifecycle, such as adding context or applying a safety check | It is code, not a reusable instruction package. |

A simple way to remember the difference is:

> **Tools provide verbs. Skills provide reusable know-how. Context provides the facts.**

### Terms used in this guide

The guide uses the following terms throughout:

| Term | Meaning |
|---|---|
| **Activation** | The decision that a bound skill is relevant to the current request. |
| **Admission** | Acceptance of selected content after the framework checks identity, scope, integrity, and configured limits. |
| **Atomic operation** | An operation that either completes fully or makes no change. It does not leave a partial result. |
| **Binding** | A developer-controlled configuration that makes an agent eligible to use a named skill. A binding does not guarantee activation. |
| **Boundary** | A named point in execution, such as initial planning, continuation planning, or synthesis. |
| **Catalog** | The metadata-only list used to choose skills or resources without loading their contents. |
| **Manifest** | The main part of a skill package: metadata, instructions, tool hints, and the resource index. |
| **Body-free** | Metadata that excludes instruction text and resource contents. This lets the framework make an early selection without loading all content. |
| **Bounded** | Limited by a documented maximum, such as a byte, token, item, or time limit. |
| **Authoritative** | The source or result that the framework uses for its final decision. For example, the registry is authoritative for published skill content. |
| **Normalized** | Converted to the framework's required, consistent format. |
| **Deterministic** | Produces the same result from the same input without asking a model to decide. |
| **Immutable revision** | A numbered stored version whose contents do not change after publication. |
| **No-op** | A successful operation that makes no change because the requested state already exists. |
| **Provider-neutral** | Defined through framework interfaces instead of depending on one storage or model provider. |
| **Replica** | One running copy of an agent application, usually managed by Kubernetes. |
| **Registry** | The configured backend interface used to resolve and load published skill versions. |
| **Selector** | Framework or application logic that chooses relevant skills or resources from an allowed candidate list. |
| **Host application** | The application that constructs the framework components and exposes optional framework Hypertext Transfer Protocol (HTTP) handlers. |
| **Pin an exact version** | Select one immutable revision and retain it for the execution, even if a newer version is published. |
| **Projection** | The skill content that the framework adds to one model prompt at a specific execution stage. |
| **Short-circuit** | A response returned by a pipeline hook before the normal execution flow continues. |
| **Token** | A unit an artificial intelligence (AI) model uses to measure text. A token may be a word, part of a word, punctuation, or another text fragment. |
| **ETag** | An HTTP version identifier used to prevent one author from accidentally overwriting another author's update. |
| **Fingerprint** | A generated identifier used to detect whether configuration or content that affects behavior has changed. |
| **Idempotency key** | A unique request identifier that lets a client safely retry a management operation without creating the same change twice. |
| **Integrity hash** | A value calculated from content and used to verify that the content has not changed unexpectedly. |
| **Tombstone** | A deletion marker retained as history after a stored version is deleted. |
| **Roll-forward** | Recover by publishing a corrected newer version instead of changing an older immutable version. |

Public application programming interface (API) names and code identifiers
retain their exact spelling throughout the guide.

### Why progressive disclosure matters

Progressive disclosure means giving the AI model only the information it needs
at each decision point. Putting every instruction and reference document into
every prompt wastes context and makes selection less reliable, especially on
smaller models. TruvaG3 therefore exposes a skill in layers:

1. **Catalog metadata** — identity, display name, description, domains, and
   tags. This is enough to decide whether a skill may be relevant.
2. **Main manifest content** — planning instructions, response instructions,
   tool hints, and a metadata-only resource index. Resource contents are not
   included in this index. The manifest is loaded only after the
   skill activates.
3. **Resource bodies** — detailed reference text. Each resource is selected
   and loaded independently for a relevant lifecycle boundary.

This keeps ordinary requests small while still allowing rich, specialized
guidance when a task needs it.

### What a skill cannot do

A skill cannot:

- grant access to a tool or service;
- bypass framework, platform, or human-approval policy;
- add a capability that the agent did not discover;
- update its own binding or publication;
- make user text trusted; or
- execute code embedded in the package.

TruvaG3 Version 1 (V1) accepts non-executable `text/plain` and `text/markdown`
resources only. The authoring validator rejects credentials, executable or
binary payload patterns, and common instructions that try to bypass authority.

---

## 2. How TruvaG3 skills work

TruvaG3 treats skills as a built-in orchestration feature. They are not a
pre- or post-execution hook, and their implementation is not limited to one
model provider.

The application supplies two things:

1. a complete developer-owned list of eligible skill bindings; and
2. a provider-neutral `orchestration.SkillRegistry` implementation.

For each request, orchestration resolves the bindings together, pins exact
immutable revisions, and progressively loads content at named lifecycle
boundaries.

```text
SKILL MANAGEMENT                               AGENT REQUEST

Author package                                Developer bindings
      │                                              │
      ▼                                              ▼
Validate ──► Publish immutable revision       Resolve in one batch
      │              │                               │
      │              ├─ catalog metadata             ▼
      │              ├─ main manifest          1. Pin exact versions
      │              └─ resource bodies               │
      │                                              ▼
      └──────── shared SkillRegistry ─────────► 2. Activate relevant skills
                                                     │
                                                     ▼
                                               3. Select resources
                                                     │
                                                     ▼
                                               4. Load + verify content
                                                     │
                                                     ▼
                                               5. Project into this prompt
```

This diagram is an overview. For the inputs, decisions, failure behavior, and
runtime evidence produced at each stage, see
[Section 7: Runtime lifecycle](#7-runtime-lifecycle).

### The important design choices

- **Developers bind skills explicitly.** Runtime does not browse an entire
  domain and give the model arbitrary skills.
- **Bindings are eligibility, not activation.** An `auto` binding still needs
  to be selected for the current task.
- **Every request pins exact versions.** A publication in the middle of an
  execution cannot change that execution's instructions.
- **Content is loaded on demand.** The framework does not put the complete
  catalog into one large prompt, and it does not load resource contents before
  they are needed.
- **The runtime is provider-neutral.** Redis/Valkey is included as the default
  adapter, but orchestration depends on skill interfaces rather than Redis.
- **No-skills behavior stays intact.** When skills are disabled or unbound, no
  skill registry is required and ordinary prompt/lifecycle behavior is
  preserved.

### Identity, domains, and versions

A skill's identity is:

```text
namespace/name
```

For example:

```text
travel/weather-assessment
devops/observability-investigation
```

Domains and tags help describe and filter a skill, but they are not identity.
Two namespaces can therefore contain the same skill name with different
descriptions and content. A skill namespace is a logical package namespace; it
does not have to equal a Kubernetes namespace, Redis key namespace, or agent
domain.

Every accepted content change creates an immutable numeric revision. The one
operational alias is `published`, which points to the current revision.

---

## 3. Quick start

The quickest way to see the full lifecycle is to run one of the shipped agents.

### Option A: use the Travel example

For the first example in a new local environment:

```bash
cd examples/travel-chat-agent
cp .env.example .env
# Add one supported AI provider key to .env.
./setup.sh full-deploy
```

If the Kind cluster and shared infrastructure already exist:

```bash
cd examples/travel-chat-agent
./setup.sh deploy
```

Deploy the two tools needed by the weather request used below (each example is
deployed through its own script):

```bash
cd ../weather-tool-v2 && ./setup.sh deploy && cd ../travel-chat-agent
cd ../geocoding-tool && ./setup.sh deploy && cd ../travel-chat-agent
```

The script publishes these packages through the Registry Viewer host:

- `travel/action-verification` — `always`
- `travel/travel-search-preparation` — `auto`
- `travel/currency-conversion` — `auto`
- `travel/travel-readiness-assessment` — `auto`
- `travel/weather-assessment` — `auto`

The packages are reviewable files under
`skills/packages/<namespace>/<name>.json`. The directory path supplies the
stored skill identity, and the setup helper discovers every package there.
`full-deploy` recreates them when the Kind cluster has an empty runtime
registry. When the cluster and management API are already running, inspect or
reconcile only the skills:

```bash
./setup.sh skills-check  # Read-only comparison with Git
./setup.sh skills-sync   # Create/update packages and verify the result
```

These commands do not build an image, restart the agent, or provision
infrastructure. `skills-sync` skips equivalent content and rolls changed
behavior forward as a new immutable revision. If the store cannot return a
valid published package and ETag, the command stops; repair the configured
backend and run `skills-sync` again instead of bypassing integrity checks.

The example deployment commands use a different, best-effort wrapper. They
attempt the same synchronization before creating or restarting the workload,
but validation, routing, or management API failures are warnings and do not
stop agent deployment. Existing published revisions remain available. If a
required package has never been published, requests that need it can fail until
you run the strict `skills-sync` command successfully.

For a headless or remote setup host that intentionally cannot route to the
management API, skip the automatic attempt:

```bash
TRUVAG3_SKIP_SKILLS_SYNC=true ./setup.sh deploy
```

This setup-only switch does not weaken `./setup.sh skills-sync` or
`./setup.sh skills-check`; explicit maintenance commands always remain strict.
Set `TRUVAG3_SKILLS_API_URL` to the full `/api/v1/skills` base URL when the API
is reachable at another address.

Automatic setup briefly retries an HTTP `404` because a new ingress can serve
its default backend while the Skills route is converging. A strict command
treats `404` as an incorrect API base URL and fails immediately. The read-only
`skills-check` also uses a shorter retry budget than synchronization so an
unavailable API is reported promptly. Finder-created `.DS_Store` files are
ignored during package discovery; other unexpected files remain errors.

Open:

- Travel chat UI: `http://chat.localhost/`
- Travel agent API: `http://travel-chat-agent.localhost/`
- Registry Viewer: `http://registry.localhost/`
- Jaeger: `http://jaeger.localhost/`

Ask a request that should activate the weather skill, such as:

```text
Will storms disrupt my trip to Tokyo next week, and what should I pack?
```

Then open **Registry Viewer → Executions**, select the request, and open its
**Skills** tab. You should see the request move through Pin, Activate, Select
resources, Load, and Project.

### Option B: add one skill to your own agent

The minimum sequence is:

1. Write a complete JavaScript Object Notation (JSON) package.
2. Validate it with `POST .../validate`.
3. Publish it with a conditional `PUT`.
4. Compose a `SkillRegistry` at application startup.
5. Add a `SkillBinding` to the agent's orchestrator configuration.
6. Run a matching request and inspect the execution evidence.

The next sections walk through each step.

---

## 4. Author a skill package

TruvaG3 V1 uses one complete JSON package for validation and publication. The
namespace and name come from the HTTP path; the JSON body contains the authored
content. Namespace, name, domain, tag, and resource-name values use normalized
lowercase slugs: letters, digits, and internal hyphens, with no leading or
trailing hyphen (64 characters by default).

Keep source packages with the application or deployment repository that owns
them. The shipped examples use
`examples/<agent>/skills/packages/<namespace>/<name>.json`. Treat those files
as reviewable source and publish them through the management API during
deployment. The example setup helper derives namespace and name from the path,
so adding a package does not require another per-package shell command. After a
successful publication, the configured skill store is the runtime source of
truth; agent pods do not read the source file directly.

> **V1 format note:** Native `SKILL.md` import/export interoperability is not
> part of V1. Use the JSON package shown here. A future interoperability layer
> can translate formats without changing the runtime contracts.

### A complete example

Save this as `weather-assessment.json`:

```json
{
  "display_name": "Travel Weather Assessment",
  "description": "Use when a travel request asks about forecasts, severe weather, seasonal conditions, or weather-related risk for a destination or route.",
  "domains": ["travel"],
  "tags": ["weather", "forecast", "risk"],
  "planning_instructions": [
    "Resolve the destination to coordinates before requesting forecast data when the weather capability requires coordinates.",
    "Use forecast horizon and confidence when assessing whether weather may disrupt the trip."
  ],
  "response_instructions": [
    "Separate observed conditions from forecast uncertainty and call out material travel risks."
  ],
  "tool_hints": ["geocode", "get_weather", "get_forecast"],
  "resources": [
    {
      "name": "weather-risk-checklist",
      "description": "A compact checklist for weather-sensitive travel recommendations.",
      "load_when": "Load when severe weather, a long forecast horizon, or disruption risk affects a recommendation.",
      "applies_to": ["planning", "continuation", "synthesis"],
      "required_when_selected": false,
      "content_type": "text/markdown",
      "content": "Check forecast horizon, confidence, precipitation, wind, temperature extremes, and official travel advisories. Express long-range forecasts with explicit uncertainty."
    }
  ],
  "activation_examples": {
    "should_activate": [
      "Will storms disrupt my trip to Tokyo next week?",
      "What should I pack for the forecast in Oslo?"
    ],
    "should_not_activate": [
      "Convert 100 USD to JPY",
      "Find a hotel in Paris"
    ]
  },
  "change_reason": "Initial V1 package"
}
```

### Package fields

| Field | Required? | Purpose |
|---|---:|---|
| `display_name` | Yes | Human-readable title shown in authoring and operator views. |
| `description` | Yes | Concise catalog text that tells selectors what the skill does and when it applies. |
| `domains` | No | Lowercase taxonomy labels used for compatibility checks and catalog filtering. |
| `tags` | No | Lowercase searchable labels. |
| `planning_instructions` | Yes | Positive procedural guidance projected into planning and continuation prompts. |
| `response_instructions` | No | Guidance projected into synthesis prompts. |
| `tool_hints` | No | Hints shown to tiered capability selection; they do not add or authorize tools. |
| `resources` | No | Independently selectable text references. |
| `activation_examples` | No | Evaluation and authoring examples; they are not included in ordinary runtime prompts. |
| `change_reason` | Yes | Audit explanation for this publication attempt; maximum 1,024 UTF-8 bytes. |

### Write a description that selects well

The description is the main body-free signal used for `auto` activation. It
should say both **what** the skill covers and **when** it should activate.

Prefer concrete request vocabulary:

```text
Use when a request asks about forecasts, severe weather, seasonal conditions,
or weather-related travel risk.
```

Avoid descriptions that are broad or circular:

```text
Helps with travel tasks.
```

The deterministic validator warns when the description lacks recognizable
activation language. The case-insensitive V1 phrases are `use when`,
`activate when`, `when a`, `when the`, `for requests`, `for tasks`, and
`whenever`.

### Keep the main instructions focused

`planning_instructions` and `response_instructions` are loaded whenever the
skill is active at their applicable boundary. Put stable, broadly applicable
guidance there.

Use positive, direct instructions:

```text
Report success only when the execution result contains concrete completion evidence.
```

Move long conditional procedures into resources. This lets the runtime load
them only for the task and phase that need them.

### Design resources for reliable selection

A resource needs a unique lowercase slug, a clear description, a concrete
`load_when`, a content type, and content.

`applies_to` accepts:

- `planning`
- `continuation`
- `synthesis`

An empty or omitted `applies_to` list makes the resource eligible at every
supported boundary. Restrict it when the content only makes sense in one part
of the lifecycle.

`required_when_selected` has a narrow meaning:

- it does **not** force the resource to be selected;
- once selected, its load, integrity, limit, or admission failure causes that
  boundary to fail.

Use concrete `load_when` text:

```text
Load when the request includes a production deletion, rollout, or namespace-wide change.
```

Avoid:

```text
Load when relevant.
```

### Authoring sizes and recommendations

Hard defaults protect the management endpoint and runtime:

| Content | Hard default | Early quality warning |
|---|---:|---:|
| Description | 1,024 characters | 320 characters |
| Main manifest | 5,000 estimated tokens and 24 KiB | 2,500 tokens |
| One resource | 8,000 estimated tokens and 32 KiB | 4,000 tokens |
| Resources per package | 32 | 12 |
| Complete package | 1 MiB | 256 KiB |
| Combined normative guidance | Bounded by the limits above | 3,000 tokens |

The ingestion estimator is deterministic and uses the framework's V1
approximately-3.5-UTF-8-bytes-per-token heuristic. Runtime projection uses the
configured `core.TokenCounter` when one is supplied and safely falls back to
the framework heuristic if that counter fails.

A package that passes authoring limits can still be partially omitted at
runtime if a request's active-skill or phase-local token budgets are exceeded.
Authoring limits bound storage and package quality; runtime limits bound a
specific model call.

### Normalization and deterministic validation

Before publication, the framework:

- trims metadata and instruction entries;
- normalizes line endings;
- lowercases, deduplicates, sorts, and validates domains and tags;
- rejects duplicate JSON fields and unknown fields;
- rejects duplicate resource names and duplicate scopes;
- calculates byte and estimated-token metrics;
- checks required fields, slugs, UTF-8, and control characters;
- rejects credential, binary/executable, and authority-bypass patterns; and
- emits quality warnings without silently rewriting the author's intent.

Validation is deterministic. An optional AI advisor may suggest improvements,
but it cannot make an invalid package valid or publish a mutation.

---

## 5. Validate, analyze, and publish

The framework supplies `NewSkillAdminHandler`, a provider-neutral HTTP handler.
The Registry Viewer is the included host and exposes it at:

```text
/api/v1/skills
```

The host chooses the storage adapter, middleware, and network exposure. Agent
runtime code does not own this endpoint, and agents do not write raw backend
keys.

### 5.1 Inspect the active schema

```bash
curl -sS http://registry.localhost/api/v1/skills/schema | jq
```

The JSON Schema describes the HTTP request and response structure and the
limits that the schema can express. The
validate endpoint remains authoritative for normalization, semantic checks,
byte limits, and estimated-token limits.

### 5.2 Validate without changing stored state

```bash
curl -sS \
  -X POST \
  -H 'Content-Type: application/json' \
  --data-binary @weather-assessment.json \
  http://registry.localhost/api/v1/skills/travel/weather-assessment/validate \
  | jq
```

A successful response contains the normalized package and a validation result.
The normalized object below is abbreviated only to keep the example readable:

```json
{
  "normalized": {
    "display_name": "Travel Weather Assessment",
    "description": "Use when a travel request asks about forecasts or weather-related risk.",
    "planning_instructions": [
      "Resolve the destination to coordinates before requesting forecast data."
    ],
    "change_reason": "Initial V1 package"
  },
  "validation": {
    "valid": true,
    "errors": [],
    "warnings": [],
    "metrics": {
      "manifest_bytes": 326,
      "resource_bytes": 184,
      "package_bytes": 1120,
      "manifest_tokens": 94,
      "resource_tokens": 53,
      "resource_count": 1,
      "token_estimator": {
        "name": "truvag3-canonical",
        "version": "v1"
      }
    }
  }
}
```

The numeric values above are illustrative; use the returned values for your
package. Errors block publication. Warnings are authoring advice and do not
block it.

### 5.3 Optional artificial intelligence (AI) authoring analysis

When a host configures a `SkillAuthoringAdvisor`, it may expose:

```bash
curl -sS \
  -X POST \
  -H 'Content-Type: application/json' \
  --data-binary @weather-assessment.json \
  http://registry.localhost/api/v1/skills/travel/weather-assessment/analyze \
  | jq
```

Analysis provides advice and does not change stored data. Proposed JSON Patch
operations describe edits to the package. The author must review and apply
them, then submit the complete package through normal validation and
publication. If deterministic validation fails, the endpoint returns those
findings and does not call the advisor.

The included default advisor sends normalized metadata, main planning/response
guidance, tool hints, activation examples, deterministic validation results,
and a body-free resource index to its large language model (LLM). It does not
send resource bodies or
`change_reason`. A custom advisor is a host-supplied implementation and owns
how it handles the provided authoring input.

The bundled Registry Viewer does **not** configure an advisor in V1. Its
**Analyze** action therefore explains that analysis is unavailable while
deterministic validation remains available.

A custom management host can compose the included advisor explicitly:

```go
advisor, err := orchestration.NewDefaultSkillAuthoringAdvisor(
    orchestration.DefaultSkillAuthoringAdvisorDependencies{
        AIClient: aiClient,
        AIOptions: &orchestration.AIOptionsOverride{
            Model:           orchestration.StringPtr("your-reviewer-model-id"),
            ReasoningEffort: orchestration.StringPtr("low"),
        },
        MaxOutputTokens:    1024,
        AdditionalGuidance: "Prefer concrete domain vocabulary in activation descriptions.",
        LLMDebugStore:      llmDebugStore,
        Logger:             logger,
        Telemetry:          telemetryProvider,
    },
)
if err != nil {
    return fmt.Errorf("create skill authoring advisor: %w", err)
}

// Supply advisor in SkillAdminHandlerDependencies.Advisor.
```

Only model and reasoning-effort intent may be overridden. Additional guidance
is additive and has the same fixed 4,096-byte/512-token bound as runtime
selector guidance. A host may map deployment settings to these fields, but V1
does not define framework-global authoring-model or authoring-guidance
environment variables.

### 5.4 Publish the first revision

Creation requires `If-None-Match: *`:

```bash
curl -sS \
  -X PUT \
  -H 'Content-Type: application/json' \
  -H 'If-None-Match: *' \
  -H 'Idempotency-Key: travel-weather-initial' \
  --data-binary @weather-assessment.json \
  http://registry.localhost/api/v1/skills/travel/weather-assessment \
  | jq
```

The server validates again, creates immutable version `1`, moves `published`
to it atomically, returns an ETag, and delivers a body-free audit event. If the
audit sink fails after the commit, the response reports the committed mutation
with `audit_recorded: false` and a repair warning.

### 5.5 Publish an update safely

First read the current representation and ETag:

```bash
if ! curl -fsS \
  -D /tmp/truvag3-skill-headers \
  -o /tmp/truvag3-skill-current.json \
  http://registry.localhost/api/v1/skills/travel/weather-assessment; then
  echo "Could not read the current skill revision" >&2
  exit 1
fi

SKILL_ETAG=$(awk 'tolower($1) == "etag:" {print $2}' /tmp/truvag3-skill-headers | tr -d '\r' | tail -1)
if [ -z "$SKILL_ETAG" ]; then
  echo "The skill response did not include an ETag" >&2
  exit 1
fi
```

After editing the complete package and updating `change_reason`, publish with
the current ETag:

```bash
curl -sS \
  -X PUT \
  -H 'Content-Type: application/json' \
  -H "If-Match: ${SKILL_ETAG}" \
  -H 'Idempotency-Key: travel-weather-update-2026-08-14' \
  --data-binary @weather-assessment.json \
  http://registry.localhost/api/v1/skills/travel/weather-assessment \
  | jq
```

An out-of-date ETag returns `412 Precondition Failed`. Read the current package,
compare it with your edit, resolve any differences, and retry with the new
ETag.

If the normalized versioned content is unchanged, the operation is a
`same_content_noop`; `change_reason` alone does not create a new revision.
Replaying the same idempotency key is also safe.

### 5.6 Why publication does not require an agent restart

Bindings that use `version: "published"` resolve that alias at the start of
every new request. The next request therefore sees the new revision without a
publish/subscribe (Pub/Sub) event, cache flush, or replica restart.

An already-running request keeps its previously pinned exact revision.
Numeric-version bindings also stay fixed until their deployment configuration
changes.

---

## 6. Bind skills to an agent

Publishing a skill makes it available in the registry. It does not make every
agent eligible to use it. The agent developer must bind it deliberately.

The binding list can come from either of these configuration paths:

| Configuration path | Use it for | Behavior |
|---|---|---|
| Go configuration with `WithSkills` | Developer-owned defaults | Supplies the complete binding list in application code. |
| `TRUVAG3_SKILL_BINDINGS_JSON` | Deployment-owned replacement | Replaces the complete code list; it never appends or merges. |
| Kubernetes ConfigMap | Kubernetes delivery of the environment value | Uses `TRUVAG3_SKILL_BINDINGS_JSON`; see [Section 9.2](#92-kubernetes-binding-configuration). |

V1 has no runtime HTTP API for changing one agent replica's bindings.

### 6.1 Binding fields

```go
type SkillBinding struct {
    Namespace  string
    Name       string
    Version    string
    Activation SkillActivation
    Required   bool
}
```

| Field/value | Behavior |
|---|---|
| `namespace`, `name` | Exact logical identity. Domains never substitute for this pair. |
| `version: "published"` | Resolve the current publication at request start, then pin the exact version and hashes. This is the default when version is omitted. |
| `version: "N"` | Resolve positive immutable revision `N`. Later publications do not move it. |
| `activation: "always"` | Activate deterministically whenever candidate resolution admits the binding. |
| `activation: "auto"` | Let the bounded selector activate it initially or at a later continuation boundary. |
| `activation: "explicit"` | Activate only through a trusted context value supplied by host code. User text cannot request it. |
| `required: true` | Make applicable availability, integrity, or admission failures cause the request to fail. It does not force relevance or activation. |

The runtime limit for each binding's `namespace` and `name` is fixed at 64
characters. This is separate from the management host's configurable authoring
slug limit. Even if a host raises its authoring limit, keep namespaces and names
at 64 characters or fewer when agents must bind the resulting package.

Here are common binding combinations in Go:

```go
bindings := []orchestration.SkillBinding{
    {
        // Optional and selected only when relevant to the request.
        // Omitting Version defaults it to "published".
        Namespace:  "travel",
        Name:       "weather-assessment",
        Activation: orchestration.SkillActivationAuto,
    },
    {
        // Applied to every request. Failure to resolve or load required
        // content causes the applicable request boundary to fail.
        Namespace:  "devops",
        Name:       "kubernetes-safety",
        Version:    "published",
        Activation: orchestration.SkillActivationAlways,
        Required:   true,
    },
    {
        // Fixed at revision 3 and activated only by trusted host code.
        Namespace:  "finance",
        Name:       "regulated-reporting",
        Version:    "3",
        Activation: orchestration.SkillActivationExplicit,
    },
}
```

Every binding must set `Activation`; it has no implicit default. To activate
the explicit binding above for one trusted request:

```go
requestContext, err := orchestration.WithTrustedSkillActivations(
    request.Context(),
    orchestration.SkillRef{
        Namespace: "finance",
        Name:      "regulated-reporting",
    },
)
if err != nil {
    return fmt.Errorf("attach trusted skill activation: %w", err)
}

response, err := orch.ProcessRequest(requestContext, requestText, metadata)
```

Only trusted application logic should call `WithTrustedSkillActivations`.
Raw user text and ordinary request metadata cannot activate an `explicit`
binding. See [Section 9.6](#96-trusted-explicit-activation) for the complete
trust-boundary behavior.

`required` is often misunderstood. A required `auto` skill is not
automatically active. Candidate-resolution failure causes the request to fail
because the required skill is unavailable, but a valid candidate that the
selector does not choose remains inactive.

### 6.2 Configure bindings in code

`skillRegistry` is not created by `WithSkills`. It is the provider-neutral
runtime dependency that reads published skill metadata and content. The host
application must construct it and keep it available for as long as the
orchestrator is running.

The following example calls `newSkillRegistry`, whose complete Redis/Valkey
implementation is shown in [Section 6.4](#64-compose-the-included-redisvalkey-registry).
An application using another backend supplies its own implementation of
`orchestration.SkillRegistry` instead.

```go
// newSkillRegistry returns the runtime interface and the backend client owner.
// Keep the owner alive while the orchestrator is running and close it during
// application shutdown.
skillRegistry, skillRegistryCloser, err := newSkillRegistry(logger)
if err != nil {
    return fmt.Errorf("create skill registry: %w", err)
}
defer skillRegistryCloser.Close()

skillConfig := orchestration.SkillConfig{
    Enabled: true,
    Bindings: []orchestration.SkillBinding{
        {
            Namespace:  "travel",
            Name:       "action-verification",
            Version:    "published",
            Activation: orchestration.SkillActivationAlways,
        },
        {
            Namespace:  "travel",
            Name:       "weather-assessment",
            Version:    "published",
            Activation: orchestration.SkillActivationAuto,
        },
    },
}

resolved, err := orchestration.ResolveOrchestratorConfig(
    orchestration.ConfigResolution{
        // Read and validate supported deployment environment overrides.
        Environment: orchestration.EnvironmentStrict,
        Options: []orchestration.OrchestratorOption{
            orchestration.WithSkills(skillConfig),
            orchestration.WithSkillRegistry(skillRegistry),
        },
    },
)
if err != nil {
    return fmt.Errorf("resolve orchestrator configuration: %w", err)
}

orch, err := orchestration.CreateResolvedOrchestrator(
    resolved.Config,
    orchestration.OrchestratorDependencies{
        Discovery: discovery,
        AIClient:  aiClient,
        Logger:    logger,
        Telemetry: telemetryProvider,
    },
)
if err != nil {
    return fmt.Errorf("create orchestrator: %w", err)
}
```

This is the recommended construction path: resolve the selected
configuration layers once, validate them, and construct an environment-free
orchestrator. `WithSkills` supplies the eligible bindings;
`WithSkillRegistry` supplies the backend-independent reader that resolves and
loads those bindings.

### 6.3 Replace the complete list through environment configuration

Environment configuration replaces the binding list, but it does not construct
the skill registry. The host application still injects a `SkillRegistry` as
shown in Sections 6.2 and 6.4. This keeps deployment-owned binding choices
separate from the provider-neutral backend dependency.

For example, add the following entries to the agent's `.env` file to enable one
automatic skill binding. Keep the complete JSON array on one physical line:

```bash
TRUVAG3_SKILLS_ENABLED=true
TRUVAG3_SKILL_BINDINGS_JSON='[{"namespace":"travel","name":"weather-assessment","version":"published","activation":"auto","required":false}]'
```

Do not include the shell `export` keyword in `.env`. The single quotes protect
the JSON when the example `setup.sh` scripts load the file; they are not part
of the environment value received by the agent.

For an interactive shell, the equivalent configuration can be formatted across
multiple lines:

```bash
export TRUVAG3_SKILLS_ENABLED=true
export TRUVAG3_SKILL_BINDINGS_JSON='[
  {
    "namespace": "travel",
    "name": "weather-assessment",
    "version": "published",
    "activation": "auto",
    "required": false
  },
  {
    "namespace": "travel",
    "name": "action-verification",
    "version": "3",
    "activation": "always",
    "required": true
  }
]'
```

`TRUVAG3_SKILL_BINDINGS_JSON` is a **complete replacement**, not an additive
list. It never merges with code bindings. This makes every replica in a
Kubernetes Deployment receive one unambiguous effective list.

An explicit empty array disables all bindings for that deployment:

```bash
export TRUVAG3_SKILL_BINDINGS_JSON='[]'
```

There is no V1 HTTP endpoint that attaches a binding to one running agent
replica. Binding changes belong in code or deployment configuration and should
be rolled out through the normal Kubernetes deployment process.

For the shipped Travel agent, use the setup script after changing the
configuration:

```bash
cd examples/travel-chat-agent

# Only .env changed: update configuration and restart the pods.
./setup.sh rollout

# Go binding code changed: rebuild the image and redeploy it.
./setup.sh rebuild
```

Run `./setup.sh help` in another example before choosing a deployment command;
example command sets may differ.

### 6.4 Compose the included Redis/Valkey registry

Orchestration receives only the provider-neutral `SkillRegistry`. The
application composition root chooses the included adapter. This helper is the
`newSkillRegistry` function used by the Section 6.2 example:

```go
func newSkillRegistry(
    logger core.Logger,
) (orchestration.SkillRegistry, io.Closer, error) {
    clientConfig, err := redisprovider.LoadClientConfigFromEnvironment(
        redisprovider.DefaultClientConfig(),
        os.LookupEnv,
    )
    if err != nil {
        return nil, nil, fmt.Errorf(
            "resolve skill backend configuration: %w", err,
        )
    }

    ownedClients, err := redisprovider.NewOwnedClients(clientConfig)
    if err != nil {
        return nil, nil, fmt.Errorf("create skill backend clients: %w", err)
    }

    skillStore, err := redisprovider.NewSkillStore(
        ownedClients.ClientSet().Resolve(redisprovider.ClientRoleSkills),
        redisprovider.WithSkillStoreLogger(logger),
    )
    if err != nil {
        _ = ownedClients.Close()
        return nil, nil, fmt.Errorf("create skill registry: %w", err)
    }

    return skillStore, ownedClients, nil
}
```

The caller owns the returned `io.Closer` and closes it during application
shutdown. The included client role uses Redis database `9` by default and
honors `TRUVAG3_SKILLS_REDIS_DB`.
`LoadClientConfigFromEnvironment` follows the orchestration-backend convention:
`REDIS_URL` first, then `TRUVAG3_REDIS_URL`, then its configured/default URL.
Avoid defining both URL variables with different values.

The runtime is not coupled to that choice. A custom runtime adapter implements
this four-method framework contract:

```go
type SkillRegistry interface {
    ListMetadata(
        context.Context,
        orchestration.SkillMetadataFilter,
    ) ([]orchestration.SkillMetadata, error)

    ResolveCandidates(
        context.Context,
        []orchestration.SkillCandidateRequest,
    ) ([]orchestration.SkillCandidate, error)

    GetManifest(
        context.Context,
        orchestration.SkillVersionRef,
    ) (orchestration.SkillManifest, error)

    GetResource(
        context.Context,
        orchestration.SkillResourceRef,
    ) (orchestration.SkillResource, error)
}
```

The actual interface is `orchestration.SkillRegistry`; the local declaration
above is shown only to make the required methods visible. Add a compile-time
check beside a custom adapter:

```go
var _ orchestration.SkillRegistry = (*MySkillRegistry)(nil)
```

Then pass a `*MySkillRegistry` instance to `WithSkillRegistry` in the same way
as the included store. A complete management-capable backend also implements
the administration, revision-read, deletion, and audit interfaces described in
[Section 14](#14-kubernetes-and-multi-replica-operations). Its test suite can
use `backendconformance.RunSkillConformance` to verify the complete storage
contract.

### 6.5 Disabled and unbound behavior

Skills are disabled by default. When either of these is true:

- `Skills.Enabled` is false; or
- the effective binding list is empty;

the orchestrator does not require a `SkillRegistry`, does not perform skill
reads or selector calls, and does not add skill sections to prompts.

If `WithSkills` is omitted and no authoritative environment setting enables
skills, the framework keeps the default disabled state. To set that state
explicitly in code:

```go
skillConfig := orchestration.SkillConfig{
    Enabled: false,
}

resolved, err := orchestration.ResolveOrchestratorConfig(
    orchestration.ConfigResolution{
        Environment: orchestration.EnvironmentCompatible,
        Options: []orchestration.OrchestratorOption{
            orchestration.WithSkills(skillConfig),
        },
    },
)
if err != nil {
    return fmt.Errorf("resolve orchestrator configuration: %w", err)
}
```

To keep the feature enabled but give this agent no eligible skills, provide an
empty binding list:

```go
skillConfig := orchestration.SkillConfig{
    Enabled:  true,
    Bindings: []orchestration.SkillBinding{},
}
```

In both cases, `WithSkillRegistry` may be omitted while these remain the
effective values because the runtime has no active skill bindings. If
configuration resolution reads the environment, `TRUVAG3_SKILLS_ENABLED` and
`TRUVAG3_SKILL_BINDINGS_JSON` remain authoritative and can replace these code
values. If an environment override enables skills and supplies bindings, the
host must inject a registry. To disable skills through the agent's `.env` file,
use:

```bash
TRUVAG3_SKILLS_ENABLED=false
TRUVAG3_SKILL_BINDINGS_JSON='[]'
```

Use `EnvironmentDisabled` only when the application intends to ignore **all**
orchestration environment configuration, not only skill variables. See
[Section 9.1](#91-configuration-precedence) for the complete precedence rules.

A required binding while skills are disabled is invalid configuration and
fails startup validation.

---

## 7. Runtime lifecycle

```text
BOUND SKILLS + CURRENT REQUEST
              │
              ▼
┌──────────────────────────────────────────────────────────────┐
│ 1. PIN CANDIDATES                                            │
│    Resolve all bindings; retain exact versions and hashes.   │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│ 2. ACTIVATE RELEVANT SKILLS                                  │
│    Apply always, trusted explicit, and automatic decisions.  │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│ 3. SELECT RESOURCES                                          │
│    Choose relevant resources for the current boundary.       │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│ 4. LOAD AND VERIFY CONTENT                                   │
│    Load selected content; enforce hashes and limits.         │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│ 5. PROJECT INTO THE CURRENT PROMPT                           │
│    Add only accepted content for this execution boundary.    │
└──────────────────────────────────────────────────────────────┘
```

The runtime follows the same five stages shown in the Registry Viewer
execution UI. The following subsections explain each stage in order.

### Stage 1: Pin candidates

Before `BeforePlanning` hooks, orchestration sends the complete binding list to
`SkillRegistry.ResolveCandidates` in one bounded batch.

For each binding, the registry returns body-free status and, when available,
an exact tuple containing:

- namespace and name;
- numeric version; and
- manifest hash.

The request pins this snapshot. Catalog metadata and mutable `published`
aliases are not cached across requests.

This batched read is intentionally simple:

```text
Agent replica A ─┐
Agent replica B ─┼─ each new request ─► one authoritative binding batch
Agent replica C ─┘                           │
                                             └─ exact versions pinned locally
```

There is no fleet-wide refresh event or background subscription to coordinate.

The resulting skills fingerprint is exposed to the generic pipeline cache gate,
then `BeforePlanning` hooks run. If a hook returns an accepted authoritative or
matching cached short-circuit, the phase loop does not perform activation or
content loading. Otherwise, enrichments produced by those hooks are available
to the initial activation and resource decisions that follow.

### Stage 2: Activate relevant skills

Activation uses body-free catalog summaries. Main instruction bodies are not
loaded before selection.

- `always` activates deterministically.
- `explicit` activates only if trusted host code requested the exact bound
  identity.
- `auto` is offered to an optional deterministic policy and then the included
  bounded AI selector, or to a custom resolver.

The included selector sees the request, candidate descriptions/taxonomy, and
bounded lifecycle context. Its choice is revalidated against the candidate
set; a model cannot invent or bind a skill.

If the selector is unavailable, invalid, or over its catalog budget, automatic
activation is skipped with a diagnostic. Deterministic `always` and trusted
`explicit` choices still apply.

Activation only moves in one direction: once a skill activates, it remains
active for the rest of the execution.

### Stage 3: Select resources

After activation, the runtime builds a body-free resource catalog from active
manifests and filters it by the current authored `applies_to` scope.

Trusted host resource requests are admitted first when valid. Remaining
resources are selected by a custom resolver or the included bounded resource
selector using descriptions, `load_when`, and lifecycle context.

Resource selection occurs separately at each eligible boundary. A resource
selected during initial planning is not automatically injected into a later
continuation or synthesis prompt; it must still be relevant there. Prior
selections are available as bounded selector context.

### Stage 4: Load and verify content

The runtime loads:

- the main manifest only after a skill is selected; and
- a resource body only after that resource is selected.

Every load is for an exact pinned version and expected hash. Verified immutable
content may come from the process-local content cache; otherwise it comes from
the authoritative registry.

An optional-content failure is omitted with diagnostics. A required active
manifest or a selected `required_when_selected` resource fails the applicable
boundary.

### Stage 5: Project into the current prompt

Only admitted content for the current boundary enters the model prompt:

| Boundary | Skill content projected |
|---|---|
| Initial planning | Planning instructions and planning-scoped selected resources |
| Continuation | Planning instructions and continuation-scoped selected resources |
| Plan regeneration | The already accepted phase projection, reused exactly |
| Synthesis | Response instructions and synthesis-scoped selected resources |
| Tiered capability selection | Active skill tool hints only |

The framework adds a system-level `<skill_precedence>` contract and a
framework-generated `<active_skills>` user section for planning, continuation,
regeneration, and synthesis. Active skills have equal precedence. Their
normalized ordering does not change their meaning or importance.

Tool hints use a separate `<active_skill_tool_hints>` section for tiered
capability selection. They remain hints; the selected capability must still be
present in the agent's real catalog.

Dynamic user, conversation, memory, tool, web, and retrieved values are encoded
before they are rendered beside skills. Authored package text is escaped before
projection, so a tag-like string cannot create a framework section; validation
also warns about reserved-tag text. Developer prompt overrides and selector
guidance that try to supply a reserved skill section are rejected.

---

## 8. Multi-phase execution, regeneration, and HITL

Skills are aware of execution stages. The framework does not inject all skill
content at the beginning and carry the same large block through every stage.

### Initial planning

The runtime pins all bindings, activates `always`/trusted/selected `auto`
skills, loads their manifests, selects planning resources, and compiles the
initial projection.

### Continuation planning

When a phase completes and the orchestrator plans the next phase:

- already active skills remain active;
- unresolved `auto` candidates may activate using the new objective, expected
  capabilities, prior results, enrichments, executed steps, and prior resource
  selections;
- continuation-scoped resources are selected again for the new boundary; and
- the new phase gets only that phase's admitted projection.

This is how a skill that was irrelevant before a tool result can become useful
later without repinning to a different version.

### Plan regeneration after validation failure

A regenerated plan within the same phase reuses the accepted projection. It
does not run activation or resource selection again and does not reread a newer
publication. That prevents prompt drift while the planner corrects a rejected
plan. This is distinct from the executor's narrow parameter-correction call,
whose fixed JSON-only contract does not receive skill sections.

### Synthesis

The synthesis boundary uses response instructions, not planning instructions.
It performs a synthesis-scoped resource decision and admits the result within
the synthesis and total skill budgets.

### HITL suspension and resume

A skill-enabled HITL checkpoint stores body-free state:

- effective bindings;
- exact pinned versions and hashes;
- active-skill identities and selection reasons;
- resource-selection history;
- cache/runtime-policy fingerprints; and
- bounded diagnostics.

It does not store instruction or resource bodies.

On resume, orchestration revalidates the checkpointed exact tuples in one
batch. It does not:

- move a checkpoint from version `N` to newly published `N+1`;
- add a binding introduced while the execution was paused; or
- rerun completed phase selection.

A selector model or runtime-policy deployment change while the request is
suspended does not fail resume. The difference is diagnostic, and the current
behavior identity is used for new continuation/synthesis cache decisions.

A legacy checkpoint with no skill state remains skill-free. Host code should
always resume through the framework's `BuildResumeContext` path rather than
reconstructing resume context manually.

---

## 9. Configuration and customization

TruvaG3 exposes safe customization where deployments and use cases commonly
differ, while keeping integrity, lifecycle, prompt precedence, and wire
contracts fixed.

### 9.1 Configuration precedence

Skills use a deliberate precedence rule:

- `TRUVAG3_SKILLS_ENABLED` and `TRUVAG3_SKILL_BINDINGS_JSON`, when present,
  are authoritative deployment settings and override code options;
- other skill environment values fill only fields that code left unset; and
- framework defaults fill the remaining values.

Use `EnvironmentDisabled` for a completely code-owned configuration with no
environment reads. Use `EnvironmentStrict` for production startup that should
reject malformed deployment values. Skill values that participate in effective
resolution fail on malformed or ambiguous input rather than silently changing
eligibility.

Non-overridden domain, cache, runtime-limit, and registry-timeout variables are
processed whether or not skills have effective bindings. Selector model and
guidance-file variables are different: they are consulted only when skills are
effectively enabled with at least one binding, and they still fill only values
left unset by code. Therefore, an invalid selector-model value or an unreadable
guidance file does not fail startup while skills are disabled or unbound; it is
evaluated when the configuration makes skills active.

### 9.2 Kubernetes binding configuration

A block scalar keeps the complete JSON readable in a ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: travel-agent-config
data:
  TRUVAG3_SKILLS_ENABLED: "true"
  TRUVAG3_SKILL_BINDINGS_JSON: |-
    [
      {
        "namespace": "travel",
        "name": "action-verification",
        "version": "published",
        "activation": "always",
        "required": false
      },
      {
        "namespace": "travel",
        "name": "weather-assessment",
        "version": "published",
        "activation": "auto",
        "required": false
      }
    ]
```

Update the ConfigMap and roll the Deployment so every replica gets the same
binding policy.

### 9.3 Domain compatibility

`SkillConfig.DomainCompatibilityMode` compares a bound candidate's authored
domains with `PromptConfig.Domain`:

| Mode | Behavior on a mismatch |
|---|---|
| `off` | Skip the comparison. |
| `warn` | Keep the binding and record a diagnostic. This is the default. |
| `enforce` | Omit an optional mismatch; fail a required mismatch. |

An empty agent domain or empty skill-domain list is not a mismatch. The mode
never searches by domain or substitutes another skill.

```bash
export TRUVAG3_SKILL_DOMAIN_COMPATIBILITY_MODE=enforce
```

### 9.4 Pin selector models

Activation and resource selection are small structured tasks. You can select a
specific model and reasoning effort without replacing the framework prompt,
parser, retry, temperature, or output contract:

```go
activationOptions := &orchestration.AIOptionsOverride{
    Model:           orchestration.StringPtr("fast"),
    ReasoningEffort: orchestration.StringPtr("low"),
}
resourceOptions := &orchestration.AIOptionsOverride{
    Model:           orchestration.StringPtr("fast"),
    ReasoningEffort: orchestration.StringPtr("low"),
}

resolved, err := orchestration.ResolveOrchestratorConfig(
    orchestration.ConfigResolution{
        Environment: orchestration.EnvironmentStrict,
        Options: []orchestration.OrchestratorOption{
            orchestration.WithSkills(skillConfig),
            orchestration.WithSkillRegistry(skillRegistry),
            orchestration.WithSkillActivationAIOptions(activationOptions),
            orchestration.WithSkillResourceAIOptions(resourceOptions),
        },
    },
)
```

Equivalent deployment variables are:

```bash
TRUVAG3_SKILL_ACTIVATION_MODEL=fast
TRUVAG3_SKILL_RESOURCE_MODEL=fast

# Optional provider-specific definitions of the portable "fast" alias.
TRUVAG3_OPENAI_MODEL_FAST=gpt-5.6-luna
TRUVAG3_ANTHROPIC_MODEL_FAST=your-approved-anthropic-model-id
TRUVAG3_GROQ_MODEL_FAST=your-approved-groq-model-id
```

Orchestration treats the model value as an opaque identifier. Clients created
by the included `ai` module recognize its lower-case portable aliases, including
`default`, `fast`, and `smart`. A custom `core.AIClient` must provide equivalent
alias handling or accept the configured value as a literal model identifier.

Use a portable alias when the agent uses `ChainClient`. Every provider attempt
receives the same logical alias, but each provider resolves it to its own model:

```text
skill selector requests "fast"
    ├── OpenAI    → TRUVAG3_OPENAI_MODEL_FAST or the built-in OpenAI mapping
    ├── Anthropic → TRUVAG3_ANTHROPIC_MODEL_FAST or the built-in Anthropic mapping
    └── Groq      → TRUVAG3_GROQ_MODEL_FAST or the built-in Groq mapping
```

Do not use an OpenAI-specific model identifier for a selector that may fail over
to Anthropic or Groq. The chain preserves the requested model value between
attempts. A fallback provider may therefore receive that OpenAI identifier,
reject it as an invalid model, and prevent recovery. Aliases are case-sensitive;
use `fast`, not `FAST`.

Failover still depends on the chain's error policy. Provider outages,
authentication failures, rate limits, server failures, and recognized billing
or quota failures can move to the next provider. Cancellation, an exhausted
request deadline, and genuine malformed input do not. Skill selector calls are
non-streaming, so partial-stream failover restrictions do not apply to them.

The two selector-model variables are read only when skills are effectively
enabled with at least one binding, as described in
[Section 9.1](#91-configuration-precedence). In Jaeger, the `ai.generate` span
shows the alias in `ai.requested_model` and the provider-resolved model in
`ai.model`. Registry Viewer's LLM Calls view shows the effective model.

The built-in selectors use temperature `0.01`, a fixed portable JSON prompt,
strict decoding and identity validation, one bounded correction retry, and a
512-token output ceiling by default. Provider-native response-format values
remain unset so the contract works across providers. Request-aware AI clients
are recommended; the non-zero low temperature also remains representable by
the legacy client adapter.

### 9.5 Add bounded selector guidance

Selector guidance adjusts how activation or resource relevance is decided. It
does not replace the framework prompt or become a skill body.

```go
orchestration.WithSkillPromptGuidance(orchestration.SkillPromptGuidance{
    Activation: "Prefer the incident skill only when the request includes an active or recent operational failure.",
    Resource:   "Select compliance checklists only when the requested action changes production state.",
})
```

For deployment-owned text, mount UTF-8 files and point the environment to them:

```bash
export TRUVAG3_SKILL_ACTIVATION_GUIDANCE_FILE=/etc/truvag3/skills/activation-guidance.txt
export TRUVAG3_SKILL_RESOURCE_GUIDANCE_FILE=/etc/truvag3/skills/resource-guidance.txt
```

Each guidance value is capped at 4,096 bytes and 512 estimated tokens. Code
guidance takes precedence when already set. File paths are read only when
skills are effectively enabled with at least one binding.

#### Existing prompt overrides remain usable

Skills do not remove the framework's ordinary prompt customization. A
developer may still set `PromptConfig.SystemInstructions`, custom instructions,
or planning/synthesis `SystemPrompt` overrides. When a boundary has admitted
skill content, orchestration finalizes the effective system prompt by appending
the framework-owned `<skill_precedence>` contract after the developer base and
places the framework-generated skill section in the user message.

Developer text must not contain framework-reserved skill sections such as
`<skill_precedence>` or `<active_skills>`. Active skill configuration fails
validation when a configured prompt override contains one, and prompt assembly
rejects a reserved section produced dynamically by a custom builder. This keeps
the precedence contract single and unambiguous. Selector AI-option overrides
are narrower still: only model and reasoning-effort intent are accepted; the
selector system prompt and output contract are not replaceable.

### 9.6 Trusted explicit activation

Use `explicit` when trusted application logic, rather than the model or raw user
text, owns the decision:

```go
ctx, err := orchestration.WithTrustedSkillActivations(
    request.Context(),
    orchestration.SkillRef{
        Namespace: "finance",
        Name:      "regulated-reporting",
    },
)
if err != nil {
    return err
}

response, err := orch.ProcessRequest(ctx, requestText, metadata)
```

The requested identity must be bound with `Activation: explicit`. Ineligible or
unbound requests are ignored with diagnostics. The trusted context value can
contain no more than 32 unique normalized identities.

### 9.7 Trusted resource requests

Host code can request an exact named resource from a bound active skill:

```go
ctx, err := orchestration.WithTrustedSkillResourceRequests(
    request.Context(),
    orchestration.SkillResourceRequest{
        Skill: orchestration.SkillRef{
            Namespace: "devops",
            Name:      "kubernetes-safety",
        },
        Name: "production-change-checklist",
    },
)
```

The runtime still verifies that the skill is bound and active, the resource
exists in the pinned manifest, its scope applies, and its hash and budgets pass.
This trusted request does not bypass the normal admission checks.

### 9.8 Expected capability hints

Host code can add bounded selection context without expanding the real
capability catalog:

```go
ctx, err := orchestration.WithSkillExpectedCapabilities(
    request.Context(),
    "weather-forecasting",
    "route-planning",
)
```

The framework trims, deduplicates, sorts, and pins at most 32 values of at most
128 bytes each. V1 does not infer these values from user text or generated
plans.

### 9.9 Custom policies and resolvers

Advanced applications can customize narrow decisions:

```go
orchestration.WithSkillActivationPolicy(policy)  // deterministic refinement
orchestration.WithSkillResolver(resolver)         // replaces activation model task
orchestration.WithSkillResourceResolver(resolver) // replaces resource model task
orchestration.WithSkillTokenCounter(counter)      // advisory runtime token count
```

The framework still owns binding eligibility, exact-version pinning, decision
revalidation, content integrity, limits, prompt projection, and lifecycle
ordering.

If custom behavior needs to remain eligible for an application's cached-answer
short-circuit, set a stable code-owned behavior identity:

```go
skillConfig.RuntimePolicyID = "acme.skill-policy/2026-08-14"
```

Change this ID whenever the custom behavior changes in a way that could change
selection or projection. It is code-only, capped at 128 bytes, and is not an
environment switch.

See [Section 9.12](#912-advanced-configuration-recipes) for complete activation
policy, resource resolver, token counter, custom cache, and storage-provider
recipes.

### 9.10 Runtime limit variables

| Variable | Default | Controls |
|---|---:|---|
| `TRUVAG3_SKILL_MAX_BINDINGS` | `32` | Complete binding-list size |
| `TRUVAG3_SKILL_MAX_AUTO_CANDIDATES` | `16` | Candidates sent to activation selection |
| `TRUVAG3_SKILL_CATALOG_TOKEN_BUDGET` | `2000` | Body-free activation catalog |
| `TRUVAG3_SKILL_MAX_RESOURCE_CANDIDATES` | `32` | Resource entries considered at one boundary |
| `TRUVAG3_SKILL_RESOURCE_CATALOG_TOKEN_BUDGET` | `2000` | Body-free resource catalog |
| `TRUVAG3_SKILL_MAX_ACTIVE_SKILLS` | `6` | Admitted active skills per execution |
| `TRUVAG3_SKILL_TOTAL_TOKEN_BUDGET` | `8192` | Total skill projection |
| `TRUVAG3_SKILL_MAIN_TOKEN_BUDGET` | `6144` | Main instruction projection |
| `TRUVAG3_SKILL_RESOURCE_TOKEN_BUDGET` | `4096` | Resource-content projection |
| `TRUVAG3_SKILL_MAX_RESOURCES_PER_PHASE` | `2` | Admitted resources at one boundary |
| `TRUVAG3_SKILL_MAX_RESOURCES_PER_EXECUTION` | `8` | Distinct resources across the execution |
| `TRUVAG3_SKILL_RESOLUTION_MAX_TOKENS` | `512` | Selector output ceiling |
| `TRUVAG3_SKILL_SYNTHESIS_TOKEN_BUDGET` | `2048` | Skill projection at synthesis |
| `TRUVAG3_SKILL_EFFECTIVE_INPUT_TOKEN_BUDGET` | `0` | Optional model-context allowance; see below |
| `TRUVAG3_SKILL_REGISTRY_READ_TIMEOUT` | `5s` | Each runtime registry operation |

When `TRUVAG3_SKILL_EFFECTIVE_INPUT_TOKEN_BUDGET` is positive, the effective
total skill budget is:

```text
min(total_token_budget, floor(effective_input_token_budget / 10))
```

This provides an optional “skills use at most 10% of effective model input”
guard. `0` disables that additional cap.

All count and token limits except the effective-input allowance must be
positive. Cross-field validation also requires each token sub-budget not to
exceed the total budget and the per-phase resource count not to exceed the
per-execution count. `MaxAutoCandidates` and `MaxActiveSkills` must each be less
than or equal to `MaxBindings`. When lowering `MaxBindings`, also lower either
of those limits whose default would exceed the new value. Main and resource
sub-budgets may overlap; the total projection budget still caps their combined
admitted result.

The same controls are available in code. Fields left at zero receive framework
defaults during configuration resolution. Explicitly set related sub-budgets
as a valid group:

```go
skillConfig.DomainCompatibilityMode = orchestration.SkillDomainCompatibilityEnforce
skillConfig.Cache = orchestration.SkillContentCacheConfig{
    Mode:     orchestration.SkillContentCacheLocal,
    MaxBytes: 8 * 1024 * 1024,
}
skillConfig.Limits.MaxActiveSkills = 4
skillConfig.Limits.TotalTokenBudget = 6144
skillConfig.Limits.MainTokenBudget = 4096
skillConfig.Limits.ResourceTokenBudget = 3072
skillConfig.Limits.SynthesisTokenBudget = 1536
skillConfig.Limits.RegistryReadTimeout = 3 * time.Second
```

Run this through `ResolveOrchestratorConfig`; do not construct an orchestrator
from an unvalidated partially normalized copy.

### 9.11 Registry Viewer authoring and administration limits

The framework exposes authoring and administration limits as Go configuration
on `NewSkillAdminHandler`. The Registry Viewer example maps these host-local
environment variables to that configuration:

| Registry Viewer variable | Default | Controls |
|---|---:|---|
| `TRUVAG3_SKILL_AUTHORING_MAX_NAME_CHARS` | `64` | Namespace, name, domain, tag, and resource-name slug length |
| `TRUVAG3_SKILL_AUTHORING_MAX_DESCRIPTION_CHARS` | `1024` | Main catalog description length |
| `TRUVAG3_SKILL_AUTHORING_MAX_MANIFEST_TOKENS` | `5000` | Estimated main normative-content tokens |
| `TRUVAG3_SKILL_AUTHORING_MAX_MANIFEST_BYTES` | `24576` | Main normative-content bytes |
| `TRUVAG3_SKILL_AUTHORING_MAX_RESOURCE_TOKENS` | `8000` | Estimated tokens in one resource body |
| `TRUVAG3_SKILL_AUTHORING_MAX_RESOURCE_BYTES` | `32768` | Bytes in one resource body |
| `TRUVAG3_SKILL_AUTHORING_MAX_RESOURCES` | `32` | Resources in one package |
| `TRUVAG3_SKILL_AUTHORING_MAX_PACKAGE_BYTES` | `1048576` | Complete HTTP package body |
| `TRUVAG3_SKILL_ADMIN_MAX_DELETE_VERSIONS` | `100` | Inclusive width of one deletion range |
| `TRUVAG3_SKILL_AUTHORING_ADVICE_MAX_OUTPUT_TOKENS` | `1024` | Optional advisor output ceiling |

These are Registry Viewer process settings, not framework-global environment
variables. A different host passes `SkillAuthoringLimits` and
`SkillAdministrationLimits` directly. Every value must be positive.

The bundled Registry Viewer `setup.sh` forwards the skills Redis database role,
but it does not automatically copy authoring/admin overrides from the invoking
shell into the Kubernetes Deployment. Add persistent overrides to the
Deployment/ConfigMap and pod environment when you need them.

Custom deterministic `SkillValidationRule` implementations may add bounded
errors or warnings after mandatory framework validation. They receive a
defensive package copy and cannot rewrite normalized content or remove
framework findings.

### 9.12 Advanced configuration recipes

The following recipes show how to replace narrow framework defaults without
changing skill eligibility, integrity verification, prompt precedence, or
lifecycle ordering.

| Use case | Extension point |
|---|---|
| Integrate an existing non-Redis/Valkey storage adapter | `SkillRegistry` and, when needed, the management interfaces |
| Add deterministic activation rules | `SkillActivationPolicy` |
| Replace model-based resource selection | `SkillResourceResolver` |
| Use an application-owned immutable-content cache | `SkillContentCache` |
| Use a model-specific token estimator | `core.TokenCounter` |
| Host management outside Registry Viewer | `SkillAdminHandler` |

These examples use application-owned types where the framework deliberately
does not provide a provider-specific implementation. Run every resulting
configuration through `ResolveOrchestratorConfig` before constructing the
orchestrator. Each recipe is independent; install only the extensions the
application needs. The Go snippets omit ordinary import blocks so the relevant
framework composition remains visible.

#### 9.12.1 Integrate an existing custom storage adapter

This recipe covers **application composition after a storage adapter has been
implemented and tested**. It does not describe how to implement a database
adapter. TruvaG3 V1 includes a Redis/Valkey implementation; it does not include
a DynamoDB, PostgreSQL, or other custom skills provider.

The agent runtime depends only on `orchestration.SkillRegistry`. Pass any
existing implementation through the same provider-neutral option used by the
bundled Redis/Valkey implementation:

```go
func skillOptionsForRegistry(
    registry orchestration.SkillRegistry,
    skillConfig orchestration.SkillConfig,
) []orchestration.OrchestratorOption {
    return []orchestration.OrchestratorOption{
        orchestration.WithSkills(skillConfig),
        orchestration.WithSkillRegistry(registry),
    }
}
```

Use the returned options as part of the normal configuration-resolution path:

```go
func createOrchestratorWithSkills(
    deps orchestration.OrchestratorDependencies,
    registry orchestration.SkillRegistry,
    skillConfig orchestration.SkillConfig,
) (*orchestration.AIOrchestrator, error) {
    resolved, err := orchestration.ResolveOrchestratorConfig(
        orchestration.ConfigResolution{
            Environment: orchestration.EnvironmentCompatible,
            Options:     skillOptionsForRegistry(registry, skillConfig),
        },
    )
    if err != nil {
        return nil, fmt.Errorf("resolve orchestrator configuration: %w", err)
    }

    return orchestration.CreateResolvedOrchestrator(resolved.Config, deps)
}
```

In `travel-chat-agent`, this seam already exists:

- `main.go` creates the concrete registry and owns its client lifecycle.
- `InitializeOrchestrator` in `chat_agent.go` receives only
  `orchestration.SkillRegistry`.
- `InitializeOrchestrator` passes that interface to `WithSkillRegistry`.

To use an existing custom adapter, replace only the concrete registry creation
in the application composition root. The agent and its skill-selection logic do
not need to know which database is behind the interface. Create the provider
client once during process startup, reuse it across requests, and close it when
the process shuts down.

An adapter is not complete merely because it satisfies the Go method set. Its
implementation must honor the runtime contract:

| Operation | Required behavior |
|---|---|
| `ListMetadata` | Return bounded metadata matching the supplied filter without instruction or resource bodies. |
| `ResolveCandidates` | Return one classified result for every requested binding. Do not silently omit an input binding. |
| `GetManifest` | Load the requested exact numbered version. Do not replace it with whichever version is currently published. |
| `GetResource` | Load the exact resource identity and immutable content expected by the manifest. |

Manifest and resource reads must preserve the version and hash identities used
by the framework's pinning and integrity checks. Provider-specific table design,
keys, indexes, consistency settings, retries, migrations, credentials, and SDK
dependencies belong to the provider package or application. TruvaG3 deliberately
does not define environment variables for providers it does not ship.

Before switching an agent to a custom adapter, verify all of the following:

1. The adapter implements `orchestration.SkillRegistry`, and provider tests
   cover every runtime behavior in the table above. A complete
   management-capable provider must also pass
   `backendconformance.RunSkillConformance`.
2. Existing published packages have been migrated to the new store, including
   every numbered manifest and resource required by in-flight executions.
3. The skills management service uses management interfaces backed by the same
   logical data set as the agents. Registry Viewer does not automatically follow
   an agent's provider selection. Changing only the agent while Registry Viewer
   continues publishing to Redis/Valkey would split management and runtime data.
4. The Kubernetes Deployment supplies the provider's endpoint, credentials,
   table or namespace settings, and any required identity configuration.
5. Startup, readiness, shutdown, retry, timeout, and observability behavior have
   been tested for the provider client.
6. A publish-and-run check confirms that the configured management host can
   publish a version and an agent replica can pin, activate, and load that same
   version. This can use Registry Viewer only after its backend composition has
   also been configured for the custom provider.

A provider that supports authoring and lifecycle management also implements the
applicable `SkillRevisionReader`, `SkillAdministrationStore`,
`SkillRevisionDeletionStore`, and `SkillAuditSink` contracts described in
Recipe 9.12.6. Building a new provider requires a provider-specific design and
implementation guide; the composition snippet above is intentionally not a
substitute for that work.

#### 9.12.2 Add a deterministic activation policy

An activation policy can include or exclude known cases before the default
activation selector handles undecided `auto` candidates. This example activates
one incident skill when the request contains a known severity term or a trusted
hook supplied an incident identifier:

```go
type IncidentActivationPolicy struct{}

func (IncidentActivationPolicy) Evaluate(
    _ context.Context,
    input orchestration.SkillActivationPolicyInput,
) (orchestration.SkillActivationPolicyDecision, error) {
    requestText := strings.ToLower(input.Request)
    _, hasIncidentID := input.Enrichments["incident_id"]

    decision := orchestration.SkillActivationPolicyDecision{}
    for _, candidate := range input.Candidates {
        ref := candidate.Ref.Ref
        if ref.Namespace != "devops" || ref.Name != "incident-investigation" {
            continue // Leave unrelated candidates for the default selector.
        }

        if hasIncidentID || containsAny(
            requestText,
            "sev1", "sev2", "production incident", "service outage",
        ) {
            decision.Include = append(decision.Include, ref)
        }
    }
    return decision, nil
}

func containsAny(value string, terms ...string) bool {
    for _, term := range terms {
        if strings.Contains(value, term) {
            return true
        }
    }
    return false
}

var _ orchestration.SkillActivationPolicy = IncidentActivationPolicy{}

skillConfig.RuntimePolicyID = "acme/incident-activation-v1"
options := []orchestration.OrchestratorOption{
    orchestration.WithSkills(skillConfig),
    orchestration.WithSkillRegistry(skillRegistry),
    orchestration.WithSkillActivationPolicy(IncidentActivationPolicy{}),
}
```

Return a candidate in `Include` only when the rule is conclusive. Return it in
`Exclude` only when the policy can conclusively reject it. Leave other
candidates in neither list so the configured resolver can decide. The runtime
revalidates every identity against the pinned candidate set.

`RuntimePolicyID` is code-owned. Change it whenever custom activation behavior
changes in a way that could alter a response. This keeps application response
caches from treating two different policies as the same behavior.

#### 9.12.3 Replace resource selection with a deterministic resolver

Use a custom resource resolver when resource choice follows application rules
and does not require a model call. The resolver receives resources only from
active skills and only for the current eligible boundary:

```go
type ProductionChecklistResolver struct{}

func (ProductionChecklistResolver) Resolve(
    _ context.Context,
    input orchestration.SkillResourceResolutionInput,
) (orchestration.SkillResourceDecision, error) {
    if input.Boundary != orchestration.SkillBoundaryInitialPlanning &&
        input.Boundary != orchestration.SkillBoundaryContinuation {
        return orchestration.SkillResourceDecision{}, nil
    }

    decision := orchestration.SkillResourceDecision{}
    for _, entry := range input.Resources {
        if entry.Skill.Ref.Namespace != "devops" ||
            entry.Skill.Ref.Name != "kubernetes-safety" ||
            entry.Resource.Name != "production-change-checklist" {
            continue
        }
        decision.Select = append(
            decision.Select,
            orchestration.SkillResourceRequest{
                Skill: entry.Skill.Ref,
                Name:  entry.Resource.Name,
            },
        )
    }
    return decision, nil
}

var _ orchestration.SkillResourceResolver = ProductionChecklistResolver{}

skillConfig.RuntimePolicyID = "acme/production-resource-policy-v1"
options := []orchestration.OrchestratorOption{
    orchestration.WithSkills(skillConfig),
    orchestration.WithSkillRegistry(skillRegistry),
    orchestration.WithSkillResourceResolver(ProductionChecklistResolver{}),
}
```

Installing `WithSkillResourceResolver` replaces the included model-based
resource-selection task. The framework still checks that every returned
resource belongs to an active pinned manifest, applies to the boundary, passes
the configured limits, and matches its integrity hash. Update the shared
`RuntimePolicyID` whenever this resolver's behavior changes.

#### 9.12.4 Use an application-owned content cache

`SkillContentCache` stores only exact immutable manifests and resources. The
following adapter lets an application reuse a bounded byte-object cache while
keeping framework values encoded as JSON:

```go
type ByteObjectCache interface {
    Get(context.Context, string) ([]byte, bool, error)
    Put(context.Context, string, []byte) error
    Delete(context.Context, string) error
}

type EncodedSkillContentCache struct {
    backend ByteObjectCache
}

func manifestCacheKey(ref orchestration.SkillVersionRef) string {
    return fmt.Sprintf(
        "skill-manifest/%s/%d/%s",
        ref.Ref.String(), ref.Version, ref.ManifestHash,
    )
}

func resourceCacheKey(ref orchestration.SkillResourceRef) string {
    return fmt.Sprintf(
        "skill-resource/%s/%d/%s/%s",
        ref.Skill.Ref.String(), ref.Skill.Version, ref.Name, ref.ExpectedHash,
    )
}

func readCachedJSON[T any](
    ctx context.Context,
    backend ByteObjectCache,
    key string,
) (T, bool, error) {
    var value T
    data, found, err := backend.Get(ctx, key)
    if err != nil || !found {
        return value, found, err
    }
    if err := json.Unmarshal(data, &value); err != nil {
        _ = backend.Delete(ctx, key)
        return value, false, err
    }
    return value, true, nil
}

func writeCachedJSON[T any](
    ctx context.Context,
    backend ByteObjectCache,
    key string,
    value T,
) error {
    data, err := json.Marshal(value)
    if err != nil {
        return err
    }
    return backend.Put(ctx, key, data)
}

func (cache *EncodedSkillContentCache) GetManifest(
    ctx context.Context,
    ref orchestration.SkillVersionRef,
) (orchestration.SkillManifest, bool, error) {
    return readCachedJSON[orchestration.SkillManifest](
        ctx, cache.backend, manifestCacheKey(ref),
    )
}

func (cache *EncodedSkillContentCache) PutManifest(
    ctx context.Context,
    ref orchestration.SkillVersionRef,
    manifest orchestration.SkillManifest,
) error {
    return writeCachedJSON(ctx, cache.backend, manifestCacheKey(ref), manifest)
}

func (cache *EncodedSkillContentCache) RemoveManifest(
    ctx context.Context,
    ref orchestration.SkillVersionRef,
) error {
    return cache.backend.Delete(ctx, manifestCacheKey(ref))
}

func (cache *EncodedSkillContentCache) GetResource(
    ctx context.Context,
    ref orchestration.SkillResourceRef,
) (orchestration.SkillResource, bool, error) {
    return readCachedJSON[orchestration.SkillResource](
        ctx, cache.backend, resourceCacheKey(ref),
    )
}

func (cache *EncodedSkillContentCache) PutResource(
    ctx context.Context,
    ref orchestration.SkillResourceRef,
    resource orchestration.SkillResource,
) error {
    return writeCachedJSON(ctx, cache.backend, resourceCacheKey(ref), resource)
}

func (cache *EncodedSkillContentCache) RemoveResource(
    ctx context.Context,
    ref orchestration.SkillResourceRef,
) error {
    return cache.backend.Delete(ctx, resourceCacheKey(ref))
}

var _ orchestration.SkillContentCache = (*EncodedSkillContentCache)(nil)

customCache := &EncodedSkillContentCache{backend: applicationByteCache}
skillConfig.Cache.Mode = orchestration.SkillContentCacheLocal
options := []orchestration.OrchestratorOption{
    orchestration.WithSkills(skillConfig),
    orchestration.WithSkillRegistry(skillRegistry),
    orchestration.WithSkillContentCache(customCache),
}
```

`applicationByteCache` is an application-created, process-local, size-bounded
cache. Its `Delete` operation should treat a missing key as success, and every
operation should honor context cancellation. The custom cache owns its
capacity, eviction, concurrency, and shutdown behavior. Keep
`SkillConfig.Cache.Mode` set to `local`; injecting a cache while the mode is
`disabled` fails configuration validation.

The framework verifies every cached value against the expected hash. A cache
miss or cache error falls back to the exact registry read. A corrupted cached
value is removed and reread from the registry. Do not cache the mutable
`published` alias or use this interface as the authoritative skill registry.

#### 9.12.5 Use a model-specific token counter

The default counter is a portable text-size estimate. An application that has
a tokenizer matching its selected model can adapt it to `core.TokenCounter`:

```go
type ProviderTokenizer interface {
    CountTokens(context.Context, string) (int, error)
}

type ModelTokenCounter struct {
    tokenizer ProviderTokenizer
}

func (counter ModelTokenCounter) CountTokens(
    ctx context.Context,
    text string,
) (int, error) {
    count, err := counter.tokenizer.CountTokens(ctx, text)
    if err != nil {
        return 0, err
    }
    if count < 0 {
        return 0, fmt.Errorf("tokenizer returned a negative count")
    }
    return count, nil
}

var _ core.TokenCounter = ModelTokenCounter{}

skillConfig.RuntimePolicyID = "acme/model-tokenizer-v1"
options := []orchestration.OrchestratorOption{
    orchestration.WithSkills(skillConfig),
    orchestration.WithSkillRegistry(skillRegistry),
    orchestration.WithSkillTokenCounter(ModelTokenCounter{
        tokenizer: providerTokenizer,
    }),
}
```

`providerTokenizer` is created and owned by the application. Counter errors or
invalid output cause the runtime to use the framework heuristic and record a
diagnostic; they do not bypass token limits. Use one combined
`RuntimePolicyID` that represents all custom selection, resolution, and
token-counting behavior installed on the agent.

#### 9.12.6 Host skills management in a separate service

Registry Viewer is the included management host, but it is not required. A
separate administrative service can mount the same provider-neutral handler.
A store that supports the complete V1 management surface implements:

```go
type SkillManagementStore interface {
    orchestration.SkillRegistry
    orchestration.SkillRevisionReader
    orchestration.SkillAdministrationStore
    orchestration.SkillRevisionDeletionStore
    orchestration.SkillAuditSink
}

func mountSkillManagement(
    mux *http.ServeMux,
    store SkillManagementStore,
    logger core.Logger,
    telemetryProvider core.Telemetry,
) error {
    handler, err := orchestration.NewSkillAdminHandler(
        orchestration.SkillAdminHandlerDependencies{
            Registry:       store,
            RevisionReader: store,
            Administration: store,
            Deletions:      store,
            Audit:          store,
            Logger:         logger,
            Telemetry:      telemetryProvider,
        },
    )
    if err != nil {
        return fmt.Errorf("create skill management handler: %w", err)
    }

    mux.Handle("/api/v1/skills", handler)
    mux.Handle("/api/v1/skills/", handler)
    return nil
}
```

The host may also provide `ValidationRules`, an optional `Advisor`, custom
authoring and administration limits, and trusted `AuditAttribution`. The host
owns HTTP serving, middleware, endpoint exposure, authentication,
authorization, rate limits, and provider-client shutdown. Mounting this handler
does not add or change any agent binding; agents still receive their complete
binding list through code or deployment environment configuration.

---

## 10. Integrity, caching, versions, and consistency

There are two different kinds of cache behavior around skills. Keeping them
separate prevents a lot of confusion.

### 10.1 Immutable content cache

The skill runtime includes a process-local, byte-bounded least-recently-used
(LRU) cache for verified manifest and resource contents.

```bash
export TRUVAG3_SKILL_CACHE_MODE=local
export TRUVAG3_SKILL_CACHE_MAX_BYTES=16777216
```

The default mode is `local`, and the default capacity is 16 MiB. Set the mode to
`disabled` to perform verified direct registry reads:

```bash
export TRUVAG3_SKILL_CACHE_MODE=disabled
```

The cache never stores or makes authoritative:

- the mutable `published` alias;
- the catalog list;
- a request's candidate snapshot; or
- unverified content.

It needs no time to live (TTL) because keys include immutable versions and
hashes. LRU size and removal decisions affect local performance; they do not
determine when a published update becomes visible.

Applications with a different process-local cache requirement can inject the
provider-neutral `SkillContentCache` interface with
`WithSkillContentCache(cache)`. Cache mode must remain `local`; choosing
`disabled` while also injecting a cache is rejected as ambiguous. The
framework's exact-version reads and hash verification still wrap the custom
cache, so it cannot become registry authority or weaken integrity behavior.
Recipe 9.12.4 shows a complete adapter over an application-owned byte cache.

### 10.2 Hash verification and mismatch handling

Every manifest and resource has a server-generated SHA-256 integrity hash.
Hashes let the runtime prove that separately stored or cached content matches
the exact record pinned at request start.

On a cached mismatch, the runtime:

1. evicts the cache entry;
2. rereads the exact version once from the authoritative registry; and
3. verifies the reread.

If the reread still mismatches, the content is unavailable. V1 has no
`allow_unverified` mode.

- Optional content is omitted with a diagnostic.
- Required active content fails the applicable boundary.

### 10.3 Response-cache safety

Skills do **not** add an answer cache. They participate in the framework's
generic cache-variation contract when an application pipeline hook already
returns a short-circuit classified as a cached response.

At request start, skills compute a fingerprint: a generated identifier that
callers must compare but do not need to interpret. The fingerprint covers the
full pinned candidate set and stable runtime behavior. A cached response is
accepted only when the stored entry and the current request either both omit
the skills fingerprint or both contain the same fingerprint. A missing value
on only one side is also a mismatch. This prevents:

- serving an answer produced with old published skill content;
- serving a skill-influenced answer after skills are disabled; or
- accepting a no-skills cached answer for a skill-enabled request.

The fingerprint deliberately covers every pinned binding, not only the skills
that later activate. Publishing any bound `published` skill can therefore miss
an agent's cached answers even for requests where that skill would have stayed
inactive. This conservative choice preserves correctness without running the
selector before the cache lookup.

Authoritative policy, denial, rate-limit, and guardrail short-circuits are not
treated as cached answers and continue to pass through normally.

Response-cache reuse is eligible when either:

- `SkillConfig.RuntimePolicyID` identifies custom behavior; or
- no custom skill policy/resolver/token counter is installed and both selector
  models are explicitly pinned.

An ineligible configuration still executes skills normally and may still use
the immutable content cache. It only bypasses cached-answer short-circuits.

If your application has no cached-response pipeline hook, this eligibility bit
does not change execution behavior.

### 10.4 Publication and replica consistency

A `published` binding is resolved on every new request. Therefore:

- all replicas become current through their next authoritative batch read;
- no Pub/Sub channel or cache-invalidation broadcast is needed;
- a numeric binding remains pinned to its configured revision; and
- an in-flight request is unaffected by a new publication.

If different replicas appear to resolve different revisions, check that they
use the same provider endpoint, database/namespace, and effective binding list.
The runtime deliberately does not hide provider inconsistency by using an
out-of-date, last-known-good catalog.

### 10.5 Deletion and running executions

The published revision and its immediate predecessor are protected from
deletion. That is a minimum operational safety window, not an indefinite
execution lease.

Deleting an older revision that is still pinned by a long-running or suspended
execution can make a later resource expansion or HITL resume fail for required
content or omit optional content. Keep older revisions for at least the
deployment's maximum ordinary execution, HITL suspension, audit, and support
window before deleting them.

---

## 11. Skills management lifecycle

V1 intentionally keeps the lifecycle small:

```text
Author complete package
        │
        ▼
Deterministic validate ── optional advisory analyze
        │
        ▼
Conditional PUT
        │
        ├─ same content ─► no-op
        │
        └─ changed content ─► next immutable version becomes published
                                  │
                                  ▼
                           Observe new requests
                                  │
                       correction needed?
                                  │
                                  ▼
                         Publish a corrected version
                              (roll forward)
```

There are no draft, rollback, archive, or automatic garbage-collection states
in V1. Recovery is roll-forward: correct the package and publish the next
revision.

### Example deployment synchronization

The example setup scripts treat the Git package directory as desired state,
but they do not make workload availability depend on the management API:

```text
deploy / rebuild / rollout / full-deploy
        │
        ├─ automatic sync succeeds ─► deploy agent
        │
        └─ automatic sync fails ─────► warn, then deploy agent

skills-sync / skills-check
        │
        ├─ operation succeeds ───────► exit 0
        │
        └─ drift or failure ─────────► non-zero exit
```

This separation keeps first-time setup usable when Registry Viewer or ingress
is not ready, while preserving strict commands for repair and CI. Automatic
sync never deletes a package. `TRUVAG3_SKIP_SKILLS_SYNC=true` disables only the
automatic attempt; `TRUVAG3_SKILLS_API_URL` selects another management host.

### Keep the management writer and runtime readers aligned

The management host publishes revisions and agent runtimes read them through
the same provider-neutral contracts, but separate Kubernetes workloads own
their own backend configuration. They must address the same logical skills
datastore.

With the included Redis implementation, keep the Redis address and
`TRUVAG3_SKILLS_REDIS_DB` consistent between Registry Viewer and every
skill-enabled agent. Keep Registry Viewer's default `truvag3` key namespace
unless the agent's `SkillStore` prefix is changed in code at the same time. An
agent `rollout` updates only that agent's ConfigMap. It does not update Registry
Viewer's ConfigMap, so changing the skills database on the agent alone can make
publication write to one database while runtime reads another.

For the local examples, apply a database change in this order:

```bash
# Set the same TRUVAG3_SKILLS_REDIS_DB in the environment used by setup.
./setup.sh infra        # redeploy the management host configuration
./setup.sh skills-sync  # publish through the updated management host
./setup.sh rollout      # update and restart the agent reader
```

Use an example that provides the `infra` verb. For another storage provider,
the equivalent rule is the same: the management writer and runtime readers
must share the provider's logical namespace or table configuration.

### List the catalog

```bash
curl -sS 'http://registry.localhost/api/v1/skills?namespace=travel&domain=travel&limit=50' | jq
```

Supported independent filters are `namespace`, `domain`, and `tag`.

### Read the published package

```bash
curl -sS \
  -D /tmp/truvag3-skill-headers \
  http://registry.localhost/api/v1/skills/travel/weather-assessment \
  | jq
```

### List body-free version history

```bash
curl -sS \
  'http://registry.localhost/api/v1/skills/travel/weather-assessment/versions?limit=20' \
  | jq
```

Use `before=<version>` for cursor-style pagination toward older versions.

### Read one retained exact version

```bash
curl -sS \
  http://registry.localhost/api/v1/skills/travel/weather-assessment/versions/2 \
  | jq
```

### Delete one eligible older version

Read the current ETag first, then provide it with an audit reason:

```bash
curl -sS \
  -X DELETE \
  -H "If-Match: ${SKILL_ETAG}" \
  -H 'X-Audit-Reason: Retention review completed' \
  http://registry.localhost/api/v1/skills/travel/weather-assessment/versions/1 \
  | jq
```

### Delete an eligible inclusive range

```bash
curl -sS \
  -X DELETE \
  -H "If-Match: ${SKILL_ETAG}" \
  -H 'X-Audit-Reason: Remove revisions past the execution retention window' \
  'http://registry.localhost/api/v1/skills/travel/weather-assessment/versions?from=1&to=3' \
  | jq
```

The range operation is atomic. If it intersects the current published
revision or published−1, the server returns `409 Conflict` and deletes nothing.
The default maximum inclusive range width is 100 versions. Repeating a delete
for already deleted versions is an idempotent no-op; version numbers are never
renumbered or reused.

### Mutation audit behavior

Publication and deletion require an injected `SkillAuditSink`. The store
mutation commits before the audit sink is called. If audit delivery fails, the
handler truthfully returns:

```json
{
  "audit_recorded": false,
  "warnings": [
    {
      "code": "skill_audit_not_recorded",
      "message": "The mutation committed, but audit delivery must be repaired."
    }
  ]
}
```

The mutation is not rolled back. Operators should treat this as a committed
change with an audit-repair action.

A host may also provide `SkillAuditAttributionProvider` to derive a bounded
actor from trusted request context established by its middleware. Attribution
is optional; invalid attribution is omitted with a diagnostic. The framework
does not interpret credentials or turn an arbitrary request header into an
authenticated identity.

### Route and precondition reference

| Method and path | Purpose | Important requirements |
|---|---|---|
| `GET /api/v1/skills/schema` | Get authoring schema | Always available on a constructed handler |
| `POST /api/v1/skills/{namespace}/{name}/validate` | Normalize and validate | `Content-Type: application/json` |
| `POST /api/v1/skills/{namespace}/{name}/analyze` | Optional advisory analysis | Registered only when an advisor is configured |
| `GET /api/v1/skills` | List body-free metadata | Registry capability; optional strict filters |
| `GET /api/v1/skills/{namespace}/{name}` | Read published package and ETag | Revision-reader capability |
| `GET /api/v1/skills/{namespace}/{name}/versions` | List body-free history | Optional `before`, `limit` |
| `GET /api/v1/skills/{namespace}/{name}/versions/{version}` | Read retained version | Positive numeric version |
| `PUT /api/v1/skills/{namespace}/{name}` | Create or publish next revision | Exactly one of `If-None-Match: *` or current `If-Match`; audit sink |
| `DELETE /api/v1/skills/{namespace}/{name}/versions/{version}` | Delete one old revision | Current `If-Match`, `X-Audit-Reason`, deletion store, audit sink |
| `DELETE /api/v1/skills/{namespace}/{name}/versions?from=N&to=M` | Delete inclusive range | Same as single delete; bounded atomic range |

Routes whose optional capabilities are not supplied to `NewSkillAdminHandler`
are not registered. Query parsing rejects unknown or repeated parameters.

Common management responses:

| Status | Typical meaning |
|---:|---|
| `400` | Invalid identity, query, number, range, or request shape |
| `404` | Skill/version not found or optional route not configured |
| `409` | Protected revision or another state conflict |
| `412` | Out-of-date ETag or failed precondition |
| `413` | Package body exceeds the configured maximum |
| `415` | Content type is not `application/json` |
| `422` | A publication package decoded but deterministic validation failed; standalone validation reports findings in a `200` result |
| `428` | Required creation/update/delete precondition is missing |
| `503` | Backend temporarily unavailable |
| `504` | Operation canceled or timed out |

---

## 12. Registry Viewer

Registry Viewer provides two complementary skills experiences.

### 12.1 Top-level Skills view: manage packages

Open `http://registry.localhost/` and choose **Skills** in the main navigation.
The bundled app mounts this management API only in its Redis-backed mode
(`USE_MOCK=false`, the Kubernetes default).

The left pane shows:

- display name and exact `namespace/name` identity;
- concise description;
- domain values as pills;
- tag values as pills; and
- current published revision.

Search matches name, namespace, domain, and tag.

Select a skill to open the right pane:

- **Package** — readable package summary, complete JSON editor, validation,
  optional analysis, and conditional publication;
- **Versions** — immutable body-free history, tombstone status, protected
  version guidance, and guarded single/range deletion; and
- **JSON** — syntax-highlighted complete current published representation.

Use **New skill** for first publication. Use **Edit package** for an update.
The UI automatically chooses `If-None-Match: *` or the current `If-Match`
ETag and generates an idempotency key.

The UI's version-history cards remain body-free. Use the exact-version API when
you need to inspect an older retained package body.

### 12.2 Execution Skills tab: explain runtime behavior

Open **Executions**, choose an execution, and then choose its **Skills** tab.
The tab appears only when the stored execution contains skill lifecycle data.

The expandable flow explains the five stages:

1. **Candidate pinning** — bound identities, requested alias/version, exact
   resolved revision, status, activation mode, and required flag.
2. **Activation decisions** — boundary, phase, selector source, selected or
   skipped decision, admission result, skill description, and reason.
3. **Resource selections** — eligible resource, boundary, selector source,
   admission, `required_when_selected`, and reason.
4. **Content loads** — manifest/resource identity, registry or cache source,
   cache outcome, attempt/retry, expected and observed hashes, byte/token
   estimates, duration, and result.
5. **Prompt projections** — exact skills and resources injected at each
   boundary, prompt kind, main/resource/total token estimates, and outcome.

The summary also shows binding source, binding/budget/cache fingerprints,
runtime policy versions, and response-cache eligibility. A Diagnostics section
appears when the runtime records omitted, degraded, mismatched, or failed
conditions.

### 12.3 Where to inspect actual prompt content

Execution skill records, ordinary logs, and traces are deliberately body-free.
They tell you **which** content was selected and projected, not the instruction
or resource text itself.

When LLM Debug is enabled, use:

- an execution's **LLM Calls** tab; or
- the top-level **LLM Debug** view.

Those views show the actual effective system and user prompts, including
`<skill_precedence>`, `<active_skills>`, selected resources, or
`<active_skill_tool_hints>` when present.

Look for the LLM call types `skill_activation_selection` and
`skill_resource_selection`. A management host whose optional default advisor
receives an `LLMDebugStore` also records `skill_authoring_analysis`.

LLM Debug is an explicit body-bearing diagnostic surface with its own retention
and access considerations. Keep it disabled when complete prompts are not
needed.

### 12.4 API-based execution inspection

The Registry Viewer backend can return the same unified execution data:

```bash
curl -sS \
  http://registry.localhost/api/executions/REQUEST_ID/unified \
  | jq '.skills'
```

Use this for automated basic comparisons or incident evidence without reading
data from rendered UI elements.

---

## 13. Observability

Skills use the same injected logger and telemetry provider as orchestration.
Agents do not need example-specific span code.

### 13.1 Jaeger traces

A skill-enabled request may contain:

| Span/event | What it tells you |
|---|---|
| `orchestrator.skills.pin_candidates` | Request-start batch resolution and pinning |
| `skills.registry.resolve_candidates` | Authoritative candidate-read latency and outcome |
| `orchestrator.skills.activate` | Activation boundary and bounded outcome |
| `skills.registry.load_manifest` | Exact manifest read |
| `orchestrator.skills.resolve_resources` | Resource-selection boundary and outcome |
| `skills.registry.load_resource` | Exact resource read |
| `orchestrator.skills.projection` event | Skill/resource counts and estimated tokens projected |
| `skills.admin.*` | HTTP management operation |
| `skills.store.*` | Provider-neutral management-store call |

Per-request spans may contain `skill.namespace`, `skill.name`, `skill.version`,
and `skill.resource`, so you can identify what loaded. These identities can
have many possible values, so the framework does not use them as metric labels.

Use Jaeger for timing, sequence, and relationships between operations. Use the
Registry Viewer execution
Skills tab for ordered decisions. Use LLM Debug only when actual prompt bodies
are required.

### 13.2 Metrics

The runtime emits bounded metric families including:

```text
orchestration.skills.operation.total
orchestration.skills.operation.duration_ms
orchestration.skills.candidate.batch_size
orchestration.skills.selector.tokens
orchestration.skills.content_cache.total
orchestration.skills.prompt.tokens
orchestration.skills.integrity.total
orchestration.skills.authoring.diagnostic.total
orchestration.skills.admin.operation.total
orchestration.skills.admin.operation.duration_ms
```

Metric labels use bounded dimensions such as module, stage, boundary, outcome,
content kind, selector kind, and prompt kind. Skill names, versions, request
IDs, error text, and backend URLs do not become labels.

Useful operational questions include:

- Is request-start skill latency increasing?
- Are selector calls consuming more tokens than expected?
- Is one deployment missing the immutable-content cache?
- Are integrity retries or omissions occurring?
- Did publication conflicts increase during an editing rollout?
- Are prompt projections consistently near their configured budgets?

### 13.3 Logs

Skill failures use bounded operation and error classifications. Common runtime
operations include:

```text
skills_pin_candidates
skills_activate
skills_resolve_resources
skills_registry_resolve_candidates
skills_registry_load_manifest
skills_registry_load_resource
```

Logs may include request ID, boundary, phase, status, reason, duration,
diagnostic code, and exact non-secret identity. They do not include complete
packages, instructions, resource bodies, selector prompts/responses, ETags,
idempotency keys, credentials, raw environment values, or raw backend URLs.

### 13.4 Enable stored execution and LLM evidence

For the Registry Viewer execution Skills tab:

```bash
export TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true
```

For full model-call prompts and responses:

```bash
export TRUVAG3_LLM_DEBUG_ENABLED=true
```

Telemetry/tracing must also be initialized and passed to the orchestrator for
Jaeger evidence. The shipped Travel and DevOps examples already compose these
pieces.

Body-free skill lifecycle evidence follows the execution-debug store's existing
retention (`TRUVAG3_EXECUTION_DEBUG_TTL` and
`TRUVAG3_EXECUTION_DEBUG_ERROR_TTL`). Body-bearing prompts follow the LLM-debug
store's retention (`TRUVAG3_LLM_DEBUG_TTL` and
`TRUVAG3_LLM_DEBUG_ERROR_TTL`). Tune those existing stores rather than adding a
separate skill-evidence retention mechanism.

---

## 14. Kubernetes and multi-replica operations

Skills are designed for agents running as Kubernetes Deployments with multiple
replicas.

### Keep bindings deployment-owned

Put the complete binding configuration in code or a Deployment/ConfigMap and
roll the ReplicaSet normally. Do not make replica-local runtime binding
changes. V1 intentionally has no agent-binding mutation endpoint and no merge
rules between code, registry, and an operator API.

### Publish content centrally

Publish and manage packages through one host of `SkillAdminHandler`, such as
Registry Viewer. All agent replicas read through their configured
`SkillRegistry` interface.

The framework does not require the management host to be Registry Viewer. Your
application can mount the same handler in a separate administrative service:

```go
handler, err := orchestration.NewSkillAdminHandler(
    orchestration.SkillAdminHandlerDependencies{
        Registry:       store,
        RevisionReader: store,
        Administration: store,
        Deletions:      store,
        Audit:          store,
        Logger:         logger,
        Telemetry:      telemetryProvider,
    },
)
if err != nil {
    return err
}

mux.Handle("/api/v1/skills", handler)
mux.Handle("/api/v1/skills/", handler)
```

The host and platform own middleware, endpoint exposure, authentication, and
authorization. The framework handler owns functional validation, optimistic
concurrency, guarded deletion, and audit delivery behavior.

### Recommended rollout order

For a new binding:

1. Validate and publish the skill package.
2. Confirm it appears in Registry Viewer.
3. Deploy the agent binding.
4. Send should-activate and should-not-activate requests.
5. Inspect execution Skills evidence, Jaeger, and bounded logs.
6. Expand rollout after the behavior matches expectations.

Publishing first is especially important for required bindings. A required
binding whose candidate cannot be resolved fails the request.

### Recommended update order

For an existing `published` binding:

1. Validate the complete edited package.
2. Publish with the current ETag.
3. Send a new request and verify the newly pinned version.
4. Observe activation and resource behavior across representative models.
5. If correction is needed, publish the next fixed revision.
6. Retain old revisions through the execution/HITL support window before
   guarded deletion.

No agent restart or cache broadcast is required for the content update.

### Backup and recovery

Include the skill-store role in the backend's normal durability, backup, and
restore policy. Keeping reviewed JSON source packages in version control makes
roll-forward reconstruction easier, but it does not replace retained numeric
revisions, hashes, tombstones, and audit evidence used to explain or resume an
older execution.

Do not repair provider records by editing raw keys or tables. Restore a
consistent backend snapshot or publish a corrected next revision through the
management API. After recovery, verify catalog listing, the published package,
retained exact-version reads, and a new agent request before resuming ordinary
version deletion.

### Provider-neutral storage

The included Redis/Valkey adapter is the default implementation, not the
orchestration contract. A deployment may use DynamoDB or another store by
implementing the needed interfaces:

- `SkillRegistry` for agent runtime reads;
- `SkillRevisionReader` for control-plane reads;
- `SkillAdministrationStore` for atomic publication;
- `SkillRevisionDeletionStore` for guarded deletion; and
- `SkillAuditSink` for mutation evidence.

An agent-only process usually needs just `SkillRegistry`. A management host
composes the additional interfaces it wants to expose.

Recipes 9.12.1 and 9.12.6 show runtime-provider injection and a separate
management-service composition.

---

## 15. Testing skills

Good skill testing checks authoring quality, runtime selection, prompt
projection, and operations. It does not need a large end-to-end suite for every
package.

### 15.1 Authoring checks

For every package:

- call the deterministic validate endpoint;
- fail CI or setup when `validation.valid` is false;
- review warnings rather than ignoring them indefinitely;
- keep concrete `should_activate` and `should_not_activate` examples; and
- verify that resources have distinguishable `load_when` conditions.

### 15.2 Runtime behavior set

At minimum, test:

1. one request that must activate each `auto` skill;
2. one nearby request that must not activate it;
3. an `always` skill request;
4. an optional-resource selection and non-selection case;
5. a multi-phase request where later results change relevance;
6. a synthesis response that follows `response_instructions`; and
7. a no-skills request to confirm the ordinary path is unchanged.

Evaluate on the smallest model class you intend to support as well as your
main production model. Selection descriptions that work on a large reasoning
model can still be too vague for a small or open-weight model.

### 15.3 Operational smoke test

After deployment:

- confirm the expected package and revision in the top-level Skills view;
- record a new execution ID;
- confirm Stage 1 pinned the expected exact version;
- confirm activation and resource reasons are sensible;
- confirm content loads are verified and cache/source results are expected;
- confirm projection token counts remain below budgets;
- inspect the actual LLM prompts when debug storage is enabled;
- open the trace and verify skill spans are children of the correct phase; and
- search logs by request ID for warnings or diagnostics.

### 15.4 Publication consistency test

For a safe test package:

1. start a request and note its pinned version;
2. publish the next revision;
3. verify the in-flight request keeps the old version;
4. send new requests across replicas; and
5. verify each new request pins the new version.

No Pub/Sub or restart should be needed.

### 15.5 HITL test

For a skill-enabled approval flow:

1. submit a request that reaches an approval checkpoint;
2. record the exact skill versions in the interrupted execution;
3. optionally publish a new revision while it is suspended;
4. call the agent's approval API through the documented HITL flow;
5. verify the resumed execution keeps the checkpointed exact versions; and
6. verify new continuation/synthesis projections and trace links are present.

---

## 16. Troubleshooting

### Agent fails to start: skill registry is required

**Cause:** Skills are enabled with a non-empty binding list, but
`SkillRegistry` is `nil`, or an interface contains a `nil` implementation value
(sometimes called a typed `nil`).

**Fix:** Compose and inject a provider-backed `SkillRegistry`, or disable/empty
the effective bindings.

### A published skill does not activate

Check the execution Skills tab in this order:

1. **Pinning** — was the binding present and resolved?
2. **Domain mode** — was it omitted by `enforce`?
3. **Activation** — is it `auto`, `explicit`, or `always`?
4. **Selector diagnostic** — did catalog size, AI availability, parsing, or a
   custom policy skip auto activation?
5. **Description quality** — does the catalog description contain concrete
   trigger vocabulary matching the request?
6. **LLM Debug** — what body-free candidate set did the selector actually see?

An `explicit` binding cannot be activated by wording the user request. Trusted
host code must use `WithTrustedSkillActivations`.

### A required skill did not activate

`required` means “fail when applicable content is unavailable,” not “always
activate.” Use `Activation: always` if the skill must apply to every request.

### A resource did not load

Check:

- the parent skill activated;
- the resource scope matches the boundary;
- `load_when` is concrete enough for selection;
- it was not over candidate/catalog limits;
- the per-phase or per-execution resource cap was not reached;
- prompt token budgets admitted it; and
- a trusted resource request used the exact bound identity and name.

Remember that `required_when_selected` does not force selection.

### A publication returns `428`

Provide exactly one of:

- `If-None-Match: *` for first publication; or
- the current `If-Match` ETag for an update.

### A publication returns `412`

Another writer published first or the ETag is out of date. Read the latest
package and ETag, compare them with your changes, resolve any differences, and
retry. Do not reuse an old ETag without first reading the current package.

### A delete returns `409`

The target intersects the protected published/published−1 set or another
provider rule. The range deletes nothing. Publish a correction rather than
trying to delete the current version.

### Analyze returns `404`

The host has no `SkillAuthoringAdvisor`. The bundled Registry Viewer behaves
this way by default. Use deterministic validation, or configure an advisor in a
custom management host.

### New requests still pin an older version

Check:

- the binding uses `published`, not a numeric revision;
- the request really started after publication;
- every replica uses the same skill backend role, database, and key namespace;
- the publication response succeeded rather than returning a no-op/conflict;
- the execution is not a HITL resume, which intentionally keeps checkpointed
  versions; and
- you are looking at a new execution rather than a cached UI record.

### Content shows a hash mismatch

The runtime evicts a mismatched local entry and rereads the exact version once.
A persistent mismatch indicates provider data inconsistency or corruption.
Optional content is omitted; required content fails. Fix the backend data path
or publish a correct next revision—do not weaken integrity checks.

### The execution has no Skills tab

Possible causes:

- skills were disabled or unbound for that request;
- the execution record predates skill support;
- execution debug storage is disabled or unavailable; or
- the request ended before skill state was recorded.

Use logs and Jaeger to distinguish configuration/startup failure from missing
stored execution evidence.

### The Skills tab shows identities but not instruction bodies

That is intentional. Execution evidence is body-free. Enable LLM Debug and use
LLM Calls/LLM Debug to inspect the actual prompt content.

### Selector latency is high

- pin a faster model with `TRUVAG3_SKILL_ACTIVATION_MODEL` and
  `TRUVAG3_SKILL_RESOURCE_MODEL`;
- improve descriptions so candidates are easy to distinguish;
- reduce the bound `auto` set;
- move deterministic cases to `always`, trusted `explicit`, or a custom
  activation policy; and
- inspect selector-token metrics and Jaeger spans before changing budgets.

---

## 17. FAQ

### Are skills reusable across agents?

Yes. A published package is reusable. Each agent developer independently binds
the exact skills that agent is eligible to use.

### Who binds a skill to an agent?

The agent developer, through code or the complete environment binding list.
Operators can choose the deployment-owned environment replacement, but V1 has
no replica-local binding API.

### Who selects which `auto` skill expands?

Orchestration. It first applies an optional deterministic activation policy,
then the included bounded selector or a custom `SkillResolver`. Every result is
validated against the pinned candidate set.

### When does selection happen?

Initially before the first planning prompt and again for still-inactive `auto`
candidates at continuation boundaries. Resource selection occurs separately at
planning, continuation, and synthesis boundaries. Regeneration reuses the
existing projection.

### Does every skill go into one large prompt?

No. Selection starts with body-free metadata. Main instructions load only after
activation. Resource bodies load independently only after phase-specific
selection.

### Are skills loaded from the registry for every request?

The complete binding set is authoritatively resolved in one batch for every new
request. Exact immutable manifest and resource bodies may be served from the
verified local content cache after activation/selection.

### How does a new publication reach all replicas?

Each replica resolves `published` on its next request. No Pub/Sub, background
refresh, or restart is required.

### What happens when a version is published during an execution?

The running execution keeps its exact pinned revision. New requests using a
`published` binding see the new revision.

### Does a skill run as a pre- or post-execution hook?

No. Skills are part of orchestration's request-start, planning,
continuation/regeneration, synthesis, and resume lifecycle. Hooks may consume
the generic request/cache context but do not own skill selection.

### Do skills grant tools or permissions?

No. Tool hints and expected-capability hints never widen the actual discovered
catalog or bypass platform/HITL policy.

### Is `/api/v1/skills` permanent, and who owns it?

It is the V1 route family implemented by `orchestration.SkillAdminHandler`.
The application hosting the handler—Registry Viewer in the bundled setup—owns
where and how it is exposed.

### Where are skill packages stored?

Behind provider-neutral skill interfaces. The included deployment uses
`orchestration/redisprovider.SkillStore` and Redis/Valkey database role `9` by
default. A custom host can use a different backend.

### Why retain versions instead of only the latest?

Exact versions make in-flight execution, HITL resume, audit, diagnosis, and
safe concurrent publication deterministic. Older versions also show what an
execution actually used. V1 recovery still favors roll-forward, not rollback.

### Can I delete versions?

Yes, one version or an inclusive bounded range. The published revision and its
immediate predecessor are protected, and deletion requires the current ETag
and an audit reason.

### Does the framework use AI when adding a skill?

Deterministic validation and publication do not require AI. An optional
authoring advisor can suggest improvements but cannot mutate or publish.

### Can I use `SKILL.md` packages directly?

Not in V1. The runtime and management API use the JSON package. `SKILL.md`
interoperability is a later compatibility concern, not a runtime dependency.

---

## 18. Quick reference

### Minimal authoring commands

```bash
SKILLS_API=http://registry.localhost/api/v1/skills

# Validate
curl -sS -X POST \
  -H 'Content-Type: application/json' \
  --data-binary @weather-assessment.json \
  "${SKILLS_API}/travel/weather-assessment/validate" | jq

# First publish
curl -sS -X PUT \
  -H 'Content-Type: application/json' \
  -H 'If-None-Match: *' \
  -H 'Idempotency-Key: travel-weather-initial' \
  --data-binary @weather-assessment.json \
  "${SKILLS_API}/travel/weather-assessment" | jq
```

### Minimal agent configuration

```go
skillConfig := orchestration.SkillConfig{
    Enabled: true,
    Bindings: []orchestration.SkillBinding{{
        Namespace:  "travel",
        Name:       "weather-assessment",
        Version:    "published",
        Activation: orchestration.SkillActivationAuto,
    }},
}

resolved, err := orchestration.ResolveOrchestratorConfig(
    orchestration.ConfigResolution{
        Environment: orchestration.EnvironmentStrict,
        Options: []orchestration.OrchestratorOption{
            orchestration.WithSkills(skillConfig),
            orchestration.WithSkillRegistry(skillRegistry),
        },
    },
)
```

### Runtime environment variables

```bash
TRUVAG3_SKILLS_ENABLED=true
TRUVAG3_SKILL_BINDINGS_JSON='[{"namespace":"travel","name":"weather-assessment","version":"published","activation":"auto","required":false}]'
TRUVAG3_SKILL_DOMAIN_COMPATIBILITY_MODE=warn
TRUVAG3_SKILL_CACHE_MODE=local
TRUVAG3_SKILL_CACHE_MAX_BYTES=16777216
TRUVAG3_SKILL_REGISTRY_READ_TIMEOUT=5s
TRUVAG3_SKILL_ACTIVATION_MODEL=fast
TRUVAG3_SKILL_RESOURCE_MODEL=fast
TRUVAG3_OPENAI_MODEL_FAST=gpt-5.6-luna
```

For every runtime, authoring, and Registry Viewer host variable, see the
[Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#agent-skills-configuration).

### Related documentation

- [Agent Development Guide](../building/AGENT_DEVELOPMENT_GUIDE.md)
- [Effective Prompts Guide](../building/EFFECTIVE_PROMPTS_GUIDE.md)
- [LLM Planning Prompt Guide](LLM_PLANNING_PROMPT_GUIDE.md)
- [Human-in-the-Loop User Guide](HUMAN_IN_THE_LOOP_USER_GUIDE.md)
- [Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md)
- [Logging Implementation Guide](../observability/LOGGING_IMPLEMENTATION_GUIDE.md)
- [API Reference](../reference/API_REFERENCE.md#agent-skills)
- [Limits Cheatsheet](../reference/LIMITS_CHEATSHEET.md#agent-skills)
- [Orchestration Architecture](https://github.com/truvaagents/truva-g3/blob/main/orchestration/ARCHITECTURE.md#agent-skills-v1)
- [Registry Viewer README](https://github.com/truvaagents/truva-g3/blob/main/examples/registry-viewer-app/README.md#skills-management)
