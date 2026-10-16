# Changelog

## v0.6.1

- Label agent initialized/shutdown lifecycle counters with `module=core`.
  This creates new labelled series after adoption; historical series are unchanged.

- Added `BaseAgent.GetDiscovery()` for synchronized runtime reads during Redis
  discovery recovery; base-agent discovery reads use the same lock as recovery.

- Built-in agent/tool HTTP servers drain on context cancellation and join that
  drain before returning. Explicit shutdown joins the same operation. Reuse the
  10s HTTP shutdown setting, now loaded from its environment variable; invalid
  present values fail loading. Zero typed timeout selects the default.
- Give the shared HTTP drain its configured budget independently of the
  initiating caller's cancellation/deadline; each caller's context limits only
  its own wait. Start continues to join the drain before returning.
- Reject a second concurrent `BaseTool.Start` call with `server already started`.
- Cancel registered runnables when HTTP serving exits, including startup
  failures. Preserve caller ownership of external clients and telemetry.

## v0.6.0

Requires Go 1.27.2 or newer. See the [root changelog](../CHANGELOG.md)
for coordinated module versions and adoption requirements.

### Shared memory contracts and configuration

- Default HTTP write timeout is now 35 minutes; default AI configuration timeout
  is 5 minutes. The latter's env tag remains reserved/unloaded. Task workers retain
  their existing 30-minute default; explicit application settings remain authoritative.

Adoption: start typed configuration from `DefaultConfig()` and check constructor
errors. `Config.Validate` rejects zero values for positive memory/activity fields;
an unresolved zero-value struct does not receive defaults through validation.
Only documented sentinels (such as zero recent-detail limits) remain valid.
Announcement limits must be positive in typed configuration and environment
settings; direct `NewActivityAnnouncementHook(..., 0)` instead selects its default
of 10. This is a stricter configuration contract, even when memory is disabled.

- Validate explicit numeric shared-memory/activity settings at construction,
  including environment presence, JSON null/type errors, overflow, positive
  duration constraints and finite retrieval weights. Preserve absent defaults
  and documented zero sentinels; do not turn runtime backend failure into a
  configuration failure.
- Add optional `ObservedEpisodicReader` for read errors without changing legacy
  fail-open episodic methods.
- Add optional `InvestigationLeaseCoordinator` with private acquisition identity
  and observed release outcomes. Preserve legacy coordinator interfaces and
  advisory ownership; introduce no domain-specific policy or background worker.

### AI completion provenance

- Add response finish/source/separate reasoning facts and a separate stream
  reasoning channel; keep legacy interfaces and canonical dispatcher behavior.

### Memory summary failure facts

- Add omitted `Error` and raw `ToolError` fields to `StepSummaryInput`, preserving
  the `EventSummarizer` interface and module dependency boundary.

## v0.5.0

See the [root changelog](../CHANGELOG.md)
for the cross-module release summary.

### Redis/Valkey topology and keyspaces

- Add shared standalone, Sentinel, and cluster connection configuration and
  client construction. Use DB 0 and validated `RedisKeyspace` values to isolate
  deployments and build same-slot keys. This replaces the v0.4.0 numbered-DB
  layout; no automatic data migration is provided.
- Preserve ownership boundaries: constructors that create clients own their
  lifetime; injected clients remain the application's responsibility.

### Pipeline contracts

- Add `RequiredAfterPlanningHook`, a required-intent marker independent of the
  stage method, so orchestration can reject signature drift at construction.
- Add application-reported pipeline effects and explicit short-circuit
  decision contracts. Payload capture is opt-in and does not perform
  framework-inferred redaction.

### DB-0 API cleanup

- `ResolveRedisConnectionConfig` returns `RedisConnectionConfig` directly;
  remove callers' `.Config` access. The diagnostics wrapper and startup
  deprecation-notice helper are removed.
- Remove `RedisDB*` role constants, `IsReservedDB`, `GetRedisDBName`,
  `RedisClientOptions`, and `NewRedisClient`. Use
  `NewRedisClientWithConnection` or `NewRedisClientWithClient` with
  `RedisClientConnectionOptions` and a validated `RedisKeyspace`.
- Remove `ParseStandaloneRedisURLForCompatibility` and
  `NewRedisUniversalClientForCompatibility`. The canonical parser/factory
  require DB 0 for standalone, Sentinel, and cluster modes. URL APIs require
  `redis://` or `rediss://`; bare host strings are not URL shorthand.
- Reject non-empty `TRUVAG3_REDIS_URL` when resolving environment configuration;
  use `REDIS_URL` or the mutually exclusive structured topology source.

This author-approved development cleanup needs no compatibility interval or
data migration. The canonical DB-0 schema and client-ownership contract are
unchanged by the subsequent API cleanup, not by the full v0.4.0-to-v0.5.0
transition; see the [root changelog](../CHANGELOG.md).
