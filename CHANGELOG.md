# Changelog

## Unreleased — Redis/Valkey development cleanup

The framework has no external users. The author approved direct removal of
obsolete Redis APIs and configuration, without a compatibility release or data
migration. This is a scoped development-stage exception in
[FRAMEWORK_DESIGN_PRINCIPLES.md](FRAMEWORK_DESIGN_PRINCIPLES.md), not a change to
the policy for future externally used releases.

- All framework-owned Redis/Valkey topologies use DB 0 and validated, versioned
  deployment keyspaces. This cleanup leaves the canonical key schema unchanged.
- Remove numbered-DB routing, obsolete Redis-prefix overrides, duplicate URL aliases,
  deprecated execution-debug aliases, and the execution-ID-only workflow store.
- Keep `REDIS_URL` as the standalone shorthand. Structured topology settings
  remain mutually exclusive with it. Injected clients remain application-owned.
- Update in-repository examples, architecture documents, and API/configuration
  guides. CI remains unchanged; full unit tests are the primary automated gate.

See the [core](core/CHANGELOG.md), [orchestration](orchestration/CHANGELOG.md), and
[telemetry](telemetry/CHANGELOG.md) entries for API replacements. No release or
version tag is implied by these Unreleased entries.
