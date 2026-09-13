# Changelog

## Unreleased — v0.5.0 preparation

The release is not yet published. See the [root changelog](../CHANGELOG.md)
for the cross-module release summary.

### Recording and retention

- Move Redis/Valkey LLM records into canonical DB-0 deployment keyspaces with
  request-local, same-slot atomic updates for cluster support.
- Preserve per-request retention floors across late interaction writes, so
  delayed records do not shorten retention requested by orchestration.
- Keep recorded model payloads unchanged at the LLM persistence boundary;
  applications own any redaction required before recording.

### DB-0 LLM recorder API cleanup

- Remove `WithRecorderRedisDB`. The owning recorder uses the canonical core
  factory and requires DB 0 in every topology.
- Reject non-empty `TRUVAG3_LLM_DEBUG_REDIS_DB` and
  `TRUVAG3_LLM_DEBUG_KEY_PREFIX` before connecting. Use
  `WithRecorderKeyspace` or the deployment namespace for key isolation.
- Remove deprecation diagnostics. Preserve application-owned client lifetime,
  bounded startup errors, request-local append/metadata/minimum-TTL atomicity,
  and the separate advisory recent-request index.

The subsequent API cleanup does not change the new canonical Redis DB-0 key
schema further. The full release does replace the v0.4.0 numbered-DB layout;
no automatic migration is provided. See the
[author-approved development cleanup](../CHANGELOG.md).
