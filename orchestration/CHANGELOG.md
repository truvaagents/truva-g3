# Changelog

## Unreleased — canonical Redis adapters

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
No canonical key-schema change or data migration is required. See the
[root changelog](../CHANGELOG.md) for the development-only cleanup authorization.
