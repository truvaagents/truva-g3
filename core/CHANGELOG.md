# Changelog

## Unreleased — shared memory contracts and configuration

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

## Unreleased — AI completion provenance (2026-10-04)

- Add response finish/source/separate reasoning facts and a separate stream
  reasoning channel; keep legacy interfaces and canonical dispatcher behavior.

## Unreleased — memory summary failure facts (2026-10-02)

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
