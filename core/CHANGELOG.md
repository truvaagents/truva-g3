# Changelog

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
