# Changelog

## v0.6.2

All seven Go modules are co-versioned and require Go 1.27.3 or newer. Update
first-party module requirements together to v0.6.2. Memory and resilience have
dependency/toolchain updates without module-specific source changes.

### Execution, delegation and observability

- **Core:** add validated `IncomingCall` context helpers and automatic caller
  request/step/root capture in framework HTTP servers. Incoming caller facts
  stay separate from the receiver's own execution identity.
- **Orchestration:** record immediate parent request/step links on managed
  executions and checkpoints, preserve checkpoint-owned lineage across resume,
  and isolate child request/step/phase/plan context. Retain observed step results
  when cancellation/deadline errors return, while keeping aggregate failure and
  existing finalization ownership.
- **Delegated results:** retain readable answers, questions and factual child
  outcomes from registered framework envelopes in bounded continuation views.
  Encode structured clarification fields consistently in synthesis prompts;
  adapters explicitly forward their optional structured response field.
- **LLM debug and tracing:** preserve per-interaction trace IDs and stable record
  trace references, including linked background work and baggage-capacity
  fallback. Keep bounded span previews on valid UTF-8 boundaries. Raw payloads,
  trace ancestry and retention contracts are preserved.
- **Tool selection:** correct invalid full tool IDs within the existing attempt
  budget and frozen catalog, recording each selector call. Expose the existing
  regeneration-feedback byte limit through the direct provider setter.

### AI behavior and defaults

- OpenRouter's built-in `default` alias now selects
  `deepseek/deepseek-v4.1-flash`, and `smart` selects `z-ai/glm-5.3`. `fast`
  remains `openai/gpt-6-luna`. Declare JSON-object support for the exact GLM
  model and allow stable cache identity for eligible concrete default/smart
  routes; explicit overrides and router/fallback eligibility remain authoritative.
- Reject ambiguous buffered Bedrock Converse content when SDK union decoding
  reuses a text block after an unrecognized or null-valued member. Preserve
  distinct equal-text blocks and failure evidence. Advance the adapter cache
  fingerprint to `bedrock-converse-v6`.
- Raise the base per-call tiered tool-selection output allowance from 2,000 to
  20,000 tokens. Existing overrides, provider reasoning headroom and reviewed
  output ceilings still apply. Skill-selection budgets are unchanged.

### Security

- Require Go 1.27.3 or newer for framework builds, incorporating the
  `crypto/x509` fix for GO-2026-6809. Align dependent example module minimums,
  container compiler images, and generated workspaces with the patched version.
  Rebuild deployed binaries to receive the standard-library fix.

- Refresh framework dependencies, including the AWS SDK, Qdrant client,
  OpenTelemetry protocol/gRPC dependencies and test libraries. See each module's
  dependency manifest for the resolved versions.

### Adoption

- Rebuild and upgrade both callers and delegated agents to receive the new
  parent-link recording. Custom HTTP hosts use the incoming-call middleware
  contract. A shared trace/root alone is not an immediate-parent pointer, and
  historical records are not backfilled.
- Clarification ends a turn. This release does not add durable waiting,
  automatic resumption or automatic child-to-root question escalation. Earlier
  failed steps remain failed; preserve structured outcomes in application
  JSON/SSE adapters.
- Review the higher selector allowance and changed OpenRouter aliases when
  budgeting deployments. Use `TRUVAG3_TIERED_SELECTION_MAX_TOKENS` to override the
  selector allowance and provider-specific model overrides to retain a chosen
  route. Invalid-selector correction can add calls within its existing budget.
- Reference-example and external application version pins are updated after
  module publication. Workspace validation alone does not establish that
  published v0.6.2 modules are independently consumable.

See the [core](core/CHANGELOG.md), [telemetry](telemetry/CHANGELOG.md),
[AI](ai/CHANGELOG.md), [orchestration](orchestration/CHANGELOG.md) and
[memory](memory/CHANGELOG.md) changelogs for details.

## v0.6.1

All seven Go modules are co-versioned and require Go 1.27.2 or newer. Update
first-party module requirements together to v0.6.1. Memory and resilience are
co-versioned for their core/telemetry dependencies, without module-specific
behavior changes.

- **Core:** join HTTP draining before returning from serving; keep its configured
  timeout independent of a shutdown caller's cancelled context. Add synchronized
  `BaseAgent.GetDiscovery()` access and preserve discovery retry recovery. Reject
  concurrent tool starts and cancel runnables when serving exits.
- **Orchestration:** add `LastSuccessfulStep`, `SuccessfulToolData` and
  `ExecutionRequestID` for generic execution inspection. Use final-dispatch raw
  tool data where available; domain receipt and outcome validation stays in the
  application. Legacy results without dispatches retain their fidelity limits.
- **Telemetry:** assign a default `service.instance.id` per OpenTelemetry provider
  so replica counters remain distinct; add validated explicit overrides. Preserve
  initialization failure and contextual lifecycle diagnostics.
- **AI:** preserve buffered OpenAI-wire decode/read/context causes; distinguish
  decode failures from interrupted reads in existing observations. Classify
  credible OpenRouter 408 responses for bounded recovery. Caller or provider-call
  deadlines still stop recovery; this release does not change that chain policy.

Adoption: use `signal.NotifyContext` and await `Framework.Run` before closing
application-owned dependencies. The default HTTP drain is 10 seconds, configured
by `TRUVAG3_HTTP_SHUTDOWN_TIMEOUT`; invalid present values fail loading. Allow
process termination time for draining plus application and telemetry cleanup.
Use `GetDiscovery()` for runtime reads while discovery retry is enabled.
Collectors must preserve instance identity; this update does not repair historic
metric collisions. Reference-example source migrations need v0.6.1 pins for
standalone builds; workspace validation alone does not establish release adoption.

## v0.6.0

All seven Go modules are co-versioned and require Go 1.27.2 or newer.
Update first-party module requirements together to v0.6.0. This pre-1.0
release includes public API and configuration changes; review the adoption
requirements below and the module changelogs before upgrading.

- **AI:** GPT-6 family handling on native OpenAI and the shared OpenAI-wire helpers
  used by Azure; exact reviewed OpenRouter
  capabilities and sampling adaptation; one retry budget for transport and
  decoded provider errors; final-answer/reasoning separation and completion
  provenance. See [AI changelog](ai/CHANGELOG.md).
- **Orchestration:** generated-plan intent/normalization and bounded correction
  feedback; payload-preserving parsing and binding; retry classification before
  correction; bounded failed-operation views; compact return-field catalogs;
  resource-selector v2; truthful request lifecycle and shared-memory recording,
  enrichment, digest reuse and request-resource cleanup. See
  [orchestration changelog](orchestration/CHANGELOG.md).
- **Core/memory:** fail-fast explicit numeric memory configuration, observed
  read health, acquisition-safe advisory investigation claims, newest-first
  filtered retrieval, and failure/completion facts in shared contracts. See
  [core](core/CHANGELOG.md) and [memory](memory/CHANGELOG.md) changelogs.
- **Telemetry:** optional completion facts in debug records and safe serialized
  singleton shutdown. See [telemetry changelog](telemetry/CHANGELOG.md).

Adoption points: handle the new `BuildMemoryHooks` error return; correct invalid
numeric memory settings rather than relying on fallback; provide explicit intent
for zero-work model plans; treat `retryable:false` as refusal of all same-step
retries. Compatible codec consumers now receive final text separately from
reasoning unless the explicit legacy fallback option is enabled. These changes
do not add durable external-task execution or domain-specific workflow policy.

Additional adoption checks:

- Preserve resolved configuration defaults when changing individual fields;
  unresolved zero-value memory/activity configuration can now fail validation.
  Shared-backend stream/claim environment settings are now read and validated.
- Review hook-generated dependency declarations, compact return-field visibility,
  source-qualified binding metadata and whole-config failed-view overrides.
- Reasoning-only stream callbacks may carry no answer text. Compatible-codec v2
  fingerprints cause cache misses; partial stream delivery is a failed lifecycle.
- Approval protects inputs, not exactly-once effects. Ordinary approved calls
  may retry unchanged; side-effecting tools need application idempotency.

Module changelogs distinguish initial planning, continuation and regeneration
budgets, and enumerate the observability/adaptation changes.

## v0.5.0

All seven Go modules are co-versioned and require Go 1.27.0 or newer.

### Highlights since v0.4.0

- **Redis and Valkey topology support:** standalone, Sentinel, and cluster
  connections share a validated configuration path. Framework-owned data uses
  DB 0 with versioned deployment keyspaces and same-slot atomic operations.
- **Portable orchestration backends:** compose and validate persistence,
  queue, lock, and skills providers through `OrchestrationBackends`. Redis
  presets remain available; the backend-portability example demonstrates
  PostgreSQL, NATS, Redis, and mixed provider composition.
- **Required planning hooks:** applications can make an `AfterPlanning` hook
  fail closed. Invalid required-hook registration fails at construction;
  rejected plans stop before HITL or tool execution. Ordinary hooks retain
  their documented fail-open behavior.
- **Framework-owned HITL resume:** the coordinator handles checkpoint claims,
  lease renewal, context restoration, recovery, and finalization. Examples
  demonstrate synchronous HTTP, SSE, and queued-task adapters.
- **Execution observability:** preserve related execution and LLM-debug
  retention, record hook invocations, decisions, and application-reported
  effects, and expose hook-only failures and HITL lineage in Registry Viewer.
  The viewer also distinguishes pre-hook synthesis from the stored
  post-hook application response.
- **Examples and developer guides:** update Redis topology setup, HITL
  webhooks and resume adapters, and practical pipeline-hook and HITL recipes.
  Refresh framework, example, CI, and documentation dependencies.

### Breaking changes and adoption notes

This is a pre-1.0 release with direct API and configuration changes. Update
all first-party module requirements together; do not mix v0.4.0 core or
telemetry with v0.5.0 orchestration.

- Replace numbered Redis databases and raw role-prefix settings with DB 0
  and `RedisKeyspace` / `TRUVAG3_REDIS_NAMESPACE`. `REDIS_URL` is the standalone
  shorthand; it cannot be combined with structured topology settings.
  There is no automatic migration from the v0.4.0 numbered-DB layout.
- Use the replacement Redis constructors and options listed in the
  [core](core/CHANGELOG.md), [orchestration](orchestration/CHANGELOG.md), and
  [telemetry](telemetry/CHANGELOG.md) changelogs. Injected Redis clients remain
  application-owned.
- `WorkflowStateStore` lookups and step updates require a workflow ID;
  the old execution-ID-only store is removed.
- `NewHITLHandler` now returns `(*HITLHandler, error)` and accepts
  `CheckpointPersistence`. Wire a `ResumeCoordinator` through
  `WithHITLResumer` to register the default resume route. The controller's
  unimplemented `ResumeExecution` method is removed.
- Default HITL commands are approve, reject, and abort. Edit, skip, retry,
  and respond are rejected rather than acknowledged without applying them.
  Custom checkpoint backends need `CheckpointResumePersistence` to support
  coordinated resume. A lease does not make external tool actions exactly-once;
  applications must still handle idempotency.
- Pipeline hook registration rejects nil and typed-nil entries. At most one
  required planning hook is allowed, and it must be the last registered hook
  participating in `AfterPlanning`. Ordinary partial hooks remain supported.

See the [backend portability guide](docs/orchestration/ORCHESTRATION_BACKEND_PORTABILITY_GUIDE.md),
[pipeline hooks guide](docs/orchestration/PIPELINE_HOOKS_GUIDE.md), and
[HITL user guide](docs/orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md)
for configuration and working application examples.

### Framework-owned HITL resume

Orchestration now owns approved-checkpoint claims, renewal, recovery, and
finalization. The human-approval, DevOps, and event-driven examples supply
thin processing adapters for their HTTP, SSE, delegation, and task transports.
See the [orchestration changelog](orchestration/CHANGELOG.md) for the direct API
migration, supported commands, and observability changes.

### Redis/Valkey development cleanup

The framework has no external users. The author approved direct removal of
obsolete Redis APIs and configuration, without a compatibility release or data
migration. This is a scoped development-stage exception in
[FRAMEWORK_DESIGN_PRINCIPLES.md](FRAMEWORK_DESIGN_PRINCIPLES.md), not a change to
the policy for future externally used releases.

- All framework-owned Redis/Valkey topologies use DB 0 and validated, versioned
  deployment keyspaces. The API cleanup did not change that new canonical
  schema further; it does not make the older v0.4.0 layout compatible.
- Remove numbered-DB routing, obsolete Redis-prefix overrides, duplicate URL aliases,
  deprecated execution-debug aliases, and the execution-ID-only workflow store.
- Keep `REDIS_URL` as the standalone shorthand. Structured topology settings
  remain mutually exclusive with it. Injected clients remain application-owned.
- Update in-repository examples, architecture documents, and API/configuration
  guides. Full unit tests remain the primary automated gate; the release also
  checks local module resolution and temporary dependency bootstraps in CI.

See the [core](core/CHANGELOG.md), [orchestration](orchestration/CHANGELOG.md), and
[telemetry](telemetry/CHANGELOG.md) entries for API replacements.
