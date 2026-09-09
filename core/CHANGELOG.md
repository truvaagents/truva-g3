# Changelog

## Unreleased — DB-0 API cleanup

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
unchanged; see the [root changelog](../CHANGELOG.md).
