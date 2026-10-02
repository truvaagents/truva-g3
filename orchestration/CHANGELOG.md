# Changelog

## Unreleased — typed bindings and failed-operation evidence (2026-09-30)

- Preserve exact whole-value container bindings and JSON-looking string inputs;
  reject known container/scalar mismatches before dispatch without changing
  semantic nested extraction or application transform/approval ordering.
- Add configurable bounded same-attempt failure evidence to continuation and
  both built-in synthesis paths, an application restriction interface and an
  explicit delegation projector. Raw records and custom synthesizers remain
  unchanged. Report effective encoded limits, omissions and successful-step
  retention tradeoffs in request-correlated span events.

## Unreleased — bounded digest windows (2026-09-30)

- Reuse activity digests only for an observed, matching bounded event window
  and configuration/AI identity. Deduplicate exact content, admit late events,
  rebaseline after expiry/edits/eviction, and preserve the prior cache on failure.
  Keep legacy episodic APIs fail-open; unknown read health bypasses cache reuse.
  Add read-health and digest-window hook effects plus bounded trace diagnostics.

## Unreleased — post-memory verification corrections (2026-09-29)

- Emit request lifecycle independently of step success, with monotonic record
  revisions and latest-snapshot merges for delayed hook effects. Preserve legacy
  unknown state and request-ID-linked resume. Own activity renewal per request,
  serialize status changes, and join renewal before all-exit bounded cleanup;
  submission telemetry does not assert backend health.

- Manage token-safe investigation claims in a private request-segment scope with
  observed, bounded all-exit cleanup and fresh resume acquisition. Preserve the
  legacy custom-coordinator/direct-hook path. Add configurable 5s cleanup timeout
  with fail-fast explicit values; preserve original failure/panic and raw payloads.

- Return construction errors from `BuildMemoryHooks`; validate all hook-owned
  numeric environment values even without AI/dependencies. Keep absent defaults,
  zero-detail/zero-announcement sentinels and runtime fail-open. Update in-repo
  callers to validate synchronously before orchestrator startup. Reject non-finite
  weights/temperatures and overflowing calculated summary allowances.
- Classify declared prior-phase template inclusions as Debug rather than missing
  dependencies. Preserve legacy inclusion totals/metadata, with additive bounded
  reason telemetry; no scheduling or parameter-binding changes.
- Accept one complete summary array with plain prose before and/or after it.
  Preserve exact decoded payloads and truncated-prefix fallback behavior;
  reject additional JSON values/envelopes or ambiguous multiple arrays.

## Unreleased

- Compact request-local memory event provenance into self-contained tuples with
  a budgeted field legend and `event-detail/v1` view identity in hook effects and
  traces. Preserve exact admitted IDs, named entity fields, section priority and
  explicit omissions. Stored events and full compactor inputs remain unchanged.

- Align both built-in event-summary prompts on the complete entity output
  contract. Add retained-summary entity-field coverage to existing hook effects,
  trace events and completion logs without redefining batch/provider success,
  changing the public summarizer interface or adding model calls. Preserve exact
  IDs, valid summary text and custom/explicit-metadata extraction paths.

- Preserve complete, individually valid event summaries before a truncated JSON
  suffix, with call-local complete/partial/fallback coverage and one recorder-owned
  fallback pass. Keep custom summarizers and model-derived entity extraction.
  Reject empty compactor digests without replacing usable cached history.
- Add semantic summary outcome telemetry alongside legacy provider-completion
  metrics; carry coverage in existing hook effects and use the actual trace ID
  (not a request ID) for newly recorded memory events.

- Bound derived memory views with complete encoded records, UTF-8-safe labelled
  shortening and admission/omission counts. Preserve stored events and existing
  section priority; deduplicate admitted rather than fetched records. Wire the
  existing enrichment-budget environment setting into raw and compacted hook
  construction, validate overflow and advance the digest view to v3.
- Count optional resource omission once per operation while preserving timeout
  diagnostics for each attempt and terminal parent cancellation.
- Render compact declared return fields in both full and tier-selected planner
  catalogs, including capabilities without a top-level return type.
- Version historical memory digest views, retain event/request origins and
  times, and distinguish request-start history from current execution evidence.
- Resource selector v2 accepts complete outer JSON fences and supports a
  configurable per-attempt deadline with one timeout retry within two attempts.
  Activation remains unchanged; timeout omissions and parent cancellation are
  distinct, and zero retains the prior parent-inherited deadline.

## v0.5.0

See the [root changelog](../CHANGELOG.md)
for the cross-module release summary.

### Backend composition and Redis/Valkey topology

- Add `OrchestrationBackends` composition, per-role overrides, capability
  validation, and reusable provider conformance tests. Application code can
  consume narrow contracts without constructing Redis adapters directly.
- Add standalone, Sentinel, and cluster Redis/Valkey support through DB-0
  keyspaces, same-slot atomic updates, and explicit client ownership. Global
  debug indexes are advisory rather than part of a cross-slot
  transaction. The v0.4.0 numbered-DB layout is not migrated automatically.

### Required planning hooks and observability

- Enforce `core.RequiredAfterPlanningHook` before HITL or execution. Reject
  stage signature drift, multiple required planning hooks, a required hook
  followed by another planning hook, and nil or typed-nil registrations.
  Optional hooks retain their documented fail-open behavior.
- Record pipeline invocations, decisions, application-reported effects, and
  failure evidence, including runs that fail before any tool step executes.
  Preserve panic propagation after recording the failed invocation.
- Preserve related execution retention and LLM-debug retention floors. Missing
  historical parents are treated as unavailable evidence, not fabricated
  lineage. Stored final application responses remain distinct from raw LLM
  synthesis output.

### Framework-owned HITL resume

- Add `ResumeCoordinator`, `ResumeExecutor`, and the optional
  `CheckpointResumePersistence` backend capability. Claim, renewal, release,
  and finalization use backend-time fencing and preserve checkpoint retention.
- Add `resuming` and `continued` states, durable successor provenance, guarded
  deletion, and expected-status CAS for human decisions. Keep the existing
  Redis/Valkey DB-0 keyspace, borrowed clients, and expiry callback behavior.
- Add result-bearing sync and streaming processing methods so adapters return
  the actual terminal result, including synthesis, hook, and callback failures.
- Remove the controller's unimplemented resume method. `NewHITLHandler` now
  returns an error and accepts `WithHITLResumer`; without it, no resume route is
  registered. Current workspace callers migrate directly, without compatibility
  wrappers, under the development-stage no-external-users policy.
- Default commands support approve/reject/abort only. Public checkpoint DTOs
  omit internal attempt ownership while preserving application payloads.
- Add correlated lifecycle events/logs and bounded resume counters. Status
  transition metrics count only successfully persisted command transitions.

### Canonical Redis adapter API cleanup

- Remove role-specific DB routing and `redisprovider.WithRoleDatabase`.
  Owned presets share one DB-0 client; `WithDatabase` accepts only zero.
- Reject non-empty obsolete role DB settings and Redis raw-prefix settings
  before constructing selected backends. Settings belonging only to unselected
  roles remain outside effective configuration. Errors never echo values.
- Remove `WithDebugRedisDB`, `WithExecutionDebugRedisDB`,
  `WithCheckpointRedisDB`, and `WithCommandStoreRedisDB`.
- Replace `WithDebugKeyPrefix`, `WithExecutionDebugKeyPrefix`,
  `WithCheckpointKeyPrefix`, and `WithCommandStoreKeyPrefix` with their typed
  `With*Keyspace` counterparts. Both HITL stores require the same keyspace and
  agent scope. Replace `RedisScheduleStoreConfig.KeyPrefix` with `Keyspace`.
- Remove `redisprovider.WithSkillStoreKeyPrefix` and its legacy `:{store}`
  branch. Use `WithSkillStoreKeyspace`; presets continue to use
  `WithNamespace`. Canonical skill keys and published versions are unchanged.
- Keep `ExecutionStoreConfig.KeyPrefix` for generic StorageProvider keys.
  A custom prefix is rejected by the direct Redis adapter unless an explicit
  `WithExecutionDebugKeyspace` overrides it.
- Remove deprecated `NewRedisExecutionStore` and `WithExecutionRedisURL`,
  `WithExecutionRedisDB`, `WithExecutionLogger`, `WithExecutionKeyPrefix`,
  `WithExecutionTTL`, and `WithExecutionErrorTTL`; use
  `NewRedisExecutionDebugStore` and the `WithExecutionDebug*` options.
- Remove the execution-ID-only `LegacyRedisStateStore`, its constructors and
  conformance fixture. `StateStore` now aliases `WorkflowStateStore`;
  `UpdateStepExecution` and `GetExecution` require `workflowID`.
- Remove `LLMDebugConfig.RedisDB` and provider deprecation diagnostics.
  Direct owning constructors use the canonical core DB-0 factory; injected
  clients remain open when adapters close.

The execution and LLM-debug stores already avoid automatic URL-constructor
redaction and use bounded diagnostics. This cleanup preserves that contract,
request-local atomicity, and separate advisory debug indexes.
The subsequent API cleanup does not change the new canonical key schema
further. This is not compatibility with the v0.4.0 numbered-DB layout. See the
[root changelog](../CHANGELOG.md) for the development-only cleanup authorization.

### Pending-work completion — 2026-10-01

- Attribute returned and stored terminal execution results to the owning request
  segment with copy-on-write metadata, including early failure publishers.
- Bound builtin event-summary AI calls to a configurable 2m default; distinguish
  child timeout from parent cancellation and retain successful late responses.
- Preserve plan validation, regeneration and executor behavior unchanged.
