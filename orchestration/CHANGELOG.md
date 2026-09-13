# Changelog

## Unreleased — v0.5.0 preparation

The release is not yet published. See the [root changelog](../CHANGELOG.md)
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
