# Changelog

## Unreleased

- Restore the bounded `error` field on incomplete episodic-query WARN logs,
  retaining contextual request correlation and `error_type=episodic_read`.
  Raw backend errors and event payloads are not copied into the log.

- Document the existing Redis 6.2+ requirement for exclusive stream-pagination
  bounds. No new Redis command or backward-compatibility path is introduced.

Changes since the framework's remote baseline `cb58a9745fe2`:

Adoption: `NewSharedBackends` now reads and validates
`TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN` and
`TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL`, which that constructor previously
ignored. Check its error return and remove invalid deployment values; omission
retains defaults.

- Sort event queries by occurrence timestamp, with deterministic domain/event-ID
  tie breaks; apply visibility, filters and inclusive time bounds before the
  limit. Global events compete in the same newest-first order rather than taking
  reserved positions. Entity-history reads retain their chronological contract.
- Scan retained candidates in bounded transport batches, deduplicate source keys
  and hydrate records before filtering. A batch size is not a recall limit;
  reads do not claim an atomic snapshot under concurrent writes. See the
  [retrieval tradeoffs](ARCHITECTURE.md#episodic-newest-first-retrieval).
- Implement optional `core.ObservedEpisodicReader` to surface query/read failures.
  Legacy reads remain fail-open; orchestration uses observed health for safe
  digest reuse instead of interpreting an outage as an empty history.
- Implement token-safe advisory investigation acquisition/release in Redis and
  in-memory providers, sharing the logical entity lock with legacy claims.
  An older request cannot release a newer request's claim by agent name alone.
  Repeating the same live acquisition is idempotent and does not renew its TTL.
- Validate explicit backend-owned stream/claim numeric settings before I/O in
  `NewSharedBackends`; retain valid absent defaults and runtime fallback.
- Preserve structured provenance and step occurrence times supplied by the
  recording hook; storage remains domain-agnostic and performs no LLM inference.

Earlier co-versioned release changes are in the [root changelog](../CHANGELOG.md).
