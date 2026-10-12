# TruvaG3 Memory Module

Pluggable storage backends for cross-agent shared memory. Agents in the same domain see each other's events, coordinate investigations, and share knowledge — enabling multi-agent systems that learn from collective experience.

## Installation

```bash
go get github.com/truvaagents/truva-g3/memory
```

**Dependencies**: `core` + `telemetry` (follows [FRAMEWORK_DESIGN_PRINCIPLES.md](../FRAMEWORK_DESIGN_PRINCIPLES.md) valid dependency rule).

The Redis-backed episodic reader requires **Redis 6.2+** (or a compatible server
supporting exclusive `XREVRANGE` bounds) for stream pagination. See the
[Redis command history](https://redis.io/docs/latest/commands/xrevrange/#history).
In-memory backends do not require Redis.

## What This Module Provides

Implementations of six `core` interfaces, each with Redis (shared) and/or in-memory (testing) backends:

| Interface | Redis Implementation | In-Memory Implementation | Purpose |
|-----------|---------------------|--------------------------|---------|
| `EpisodicMemory` | `StreamEpisodicMemory` | `InMemoryEpisodicMemory` | Record and query structured agent events |
| `InvestigationCoordinator` | `AtomicLockCoordinator` | `InMemoryInvestigationCoordinator` | Advisory claim/release of entity investigations |
| `SharedKnowledge` | `VectorSharedKnowledge` | — | Store and search knowledge via Qdrant vector DB |
| `ActivityCoordinator` | `RedisActivityCoordinator` | `InMemoryActivityCoordinator` | Real-time agent signals (what agents are currently doing) |
| `DigestCache` | `RedisDigestCache` | `InMemoryDigestCache` | Cache compacted activity digests (avoid redundant LLM calls) |
| `MemoryReflector` | `LLMMemoryReflector` | — | Extract patterns from events via LLM (uses `EpisodicMemory` + `SharedKnowledge`) |

The orchestration layer stores the stable, secret-free AI policy and route
fingerprint with each activity digest. A semantic policy change misses the old
entry; an unstable fingerprint bypasses cache reads and writes. The memory
backend treats that fingerprint as opaque cache-envelope data and does not need
provider-specific logic.

## Quick Start

The simplest way to add memory to an agent — `NewSharedBackends` creates all backends, `BuildMemoryHooks` creates all hooks:

```go
// Step 1: memory module creates backends from a Redis client
backends, err := memory.NewSharedBackends(redisClient, logger,
    memory.WithAgentName("my-agent"),
    memory.WithDomain("infrastructure"),
)
if err != nil {
    return fmt.Errorf("configure memory backends: %w", err)
}
defer backends.Close()

// Step 2: orchestration module creates hooks from backends
hooks, activityCoord, err := orchestration.BuildMemoryHooks(backends.ToDeps(), aiClient, logger)
if err != nil {
    return fmt.Errorf("configure memory hooks: %w", err)
}

// Step 3: pass to orchestrator
deps := orchestration.OrchestratorDependencies{
    PipelineHooks:       hooks,
    ActivityCoordinator: activityCoord,
}
```

See [Agent Memory User Guide](../docs/memory-and-chat/AGENT_MEMORY_USER_GUIDE.md) for full details including Phase 2 (knowledge search) and behavioural customisation. See [examples/devops-chat-agent/main.go](../examples/devops-chat-agent/main.go) for a production example.

## Key Design Decisions

- **Interface-first**: Shared contracts live in `core`; this module implements storage, not agent policy. Optional `ObservedEpisodicReader` and `InvestigationLeaseCoordinator` contracts expose read health and acquisition-safe ownership without widening legacy interfaces.
- **Configuration errors versus runtime failures**: Invalid explicit numeric memory settings fail construction; absent settings select documented defaults. Legacy reads remain fail-open. Observed reads and lease operations return backend errors so callers can distinguish missing data from an unhealthy backend. Orchestration may continue without optional memory, but cannot claim an unobserved operation succeeded.
- **Domain-scoped**: Agents in the same `TRUVAG3_AGENT_DOMAIN` share memory. Different domains are isolated.
- **Multi-entity events**: A single execution step can reference multiple entities via `AgentEvent.Entities []EntityRef`. The episodic store indexes the event under each entity for cross-entity queries.
- **Newest-first recall**: Recent/event queries order by event timestamp, not insertion time. Global events are visible but do not jump ahead of newer domain events. Filters precede the limit; inclusive time bounds retain full timestamp precision. See the architecture's [retrieval tradeoffs](ARCHITECTURE.md#episodic-newest-first-retrieval), including retained-source scan cost and legacy fail-open read limitations.
- **Vendor-agnostic naming**: `StreamEpisodicMemory` (not `ValkeyEpisodicMemory`), `VectorSharedKnowledge` (not `QdrantSharedKnowledge`).

## Configuration

All configuration via environment variables or `WithXXX()` option functions:

| Variable | Default | Purpose |
|----------|---------|---------|
| `TRUVAG3_AGENT_DOMAIN` | `default` | Memory domain scoping |
| `TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN` | `100000` | Max events in domain stream |
| `TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL` | `30m` | Investigation claim auto-expiry |
| `TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL` | `5m` | Cached digest expiry |
| `TRUVAG3_VECTOR_DB_URL` | `localhost:6334` | Qdrant gRPC endpoint |
| `TRUVAG3_EMBEDDING_BASE_URL` | — | Embedding API endpoint (Ollama/OpenAI) |
| `TRUVAG3_EMBEDDING_MODEL` | — | Embedding model name |

See [ENVIRONMENT_VARIABLES_GUIDE.md](../docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md#shared-memory-configuration) for the complete list.

## Architecture

For detailed architecture, storage topology, backend patterns, and sharing rules, see [ARCHITECTURE.md](ARCHITECTURE.md).

## Related Documentation

- [Changelog](CHANGELOG.md) — implemented storage and contract changes
- [API Reference — Shared Memory Interfaces](../docs/reference/API_REFERENCE.md#shared-memory-interfaces)
- [Adding Context to Your Agent](../docs/building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) — Pipeline hooks that consume these backends
- [Distributed Tracing Guide](../docs/observability/DISTRIBUTED_TRACING_GUIDE.md#15-llm-telemetry-in-orchestration-automatic) — Span events emitted by memory hooks
- [Limits Cheatsheet](../docs/reference/LIMITS_CHEATSHEET.md#shared-memory) — All configurable limits
