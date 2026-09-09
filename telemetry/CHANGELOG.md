# Changelog

## Unreleased — DB-0 LLM recorder

- Remove `WithRecorderRedisDB`. The owning recorder uses the canonical core
  factory and requires DB 0 in every topology.
- Reject non-empty `TRUVAG3_LLM_DEBUG_REDIS_DB` and
  `TRUVAG3_LLM_DEBUG_KEY_PREFIX` before connecting. Use
  `WithRecorderKeyspace` or the deployment namespace for key isolation.
- Remove deprecation diagnostics. Preserve application-owned client lifetime,
  bounded startup errors, request-local append/metadata/minimum-TTL atomicity,
  and the separate advisory recent-request index.

The canonical Redis DB-0 key schema is unchanged. No migration or compatibility
release is required under the [author-approved development cleanup](../CHANGELOG.md).
