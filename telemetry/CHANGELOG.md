# Changelog

## v0.6.1

- Include bounded `operation` fields in initialization and module-registration
  logs, including initialization failures.

- Give each OpenTelemetry provider a default UUID `service.instance.id`, shared
  by traces and metrics, so concurrent replicas export independent counters.
  Support `Config.ServiceInstanceID`, `WithServiceInstanceID` and
  `TRUVAG3_TELEMETRY_SERVICE_INSTANCE_ID` overrides, validated at construction.
  Collectors must preserve resource identity; historical collisions are not
  repaired. Existing three-argument constructor calls remain supported.
- Preserve the first `Initialize` error on subsequent calls instead of reporting
  success after a failed initialization. Singleton reinitialization remains
  unsupported.

## v0.6.0

Requires Go 1.27.2 or newer. See the [root changelog](../CHANGELOG.md)
for coordinated module versions and adoption requirements.

### Three-day debug retention

- Align the standalone Redis LLM recorder with orchestration: successful and
  failed records both default to 72 hours (3 days), instead of 24 hours and
  7 days respectively. Preserve explicit overrides and existing longer or
  persistent lifetimes; no existing keys are retroactively rewritten.

### Singleton teardown

- Use a typed atomic registry pointer and serialize public shutdown, including
  core unregistration. Successful shutdown exposes a real nil registry/provider
  and no-op emission; preserve initialization ownership and exporter-error
  behavior. This does not introduce reinitialization support.

### AI completion records

- Extend format-compatible LLM debug records with optional separate reasoning,
  exact finish reason and content provenance, without interpreting task outcomes.

## v0.5.0

See the [root changelog](../CHANGELOG.md)
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
