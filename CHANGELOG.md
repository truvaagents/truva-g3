# Changelog

## Unreleased

These unreleased changes describe affected framework contracts and adoption
requirements. See the module changelogs for details.

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
