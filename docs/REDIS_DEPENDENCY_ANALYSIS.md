# Redis Dependency Analysis

This document provides a comprehensive analysis of Redis usage in the Truva-G3 framework, evaluating whether Redis is a required or optional dependency, and assessing the feasibility of swapping Redis with alternative backends like Memcached.

## Table of Contents

- [Executive Summary](#executive-summary)
- [Redis Licensing and Impact on Truva-G3 Users](#redis-licensing-and-impact-on-truvag3-users)
- [Cloud Provider Availability](#cloud-provider-availability)
- [Redis Usage in Core Module](#redis-usage-in-core-module)
- [Redis Usage in Orchestration Module](#redis-usage-in-orchestration-module)
- [Interface Abstractions](#interface-abstractions)
- [Configuration Options](#configuration-options)
- [Memcached Swap Feasibility](#memcached-swap-feasibility)
- [Feature-by-Feature Impact Analysis](#feature-by-feature-impact-analysis)
- [Alternative Backend Options](#alternative-backend-options)
- [Open-Source Alternatives to Redis](#open-source-alternatives-to-redis)
  - [Drop-in Redis Replacements](#category-1-drop-in-redis-replacements) (Valkey, DragonflyDB, KeyDB, Garnet)
  - [Message Brokers](#category-2-message-brokers-for-pubsub-replacement) (NATS, RabbitMQ, Kafka)
  - [PostgreSQL-Based Solutions](#category-3-postgresql-based-solutions)
  - [Distributed Coordination](#category-4-distributed-coordination-for-service-discovery) (etcd, Consul)
  - [Recommended Combinations](#recommended-combinations)
- [Recommendations](#recommendations)

---

## Executive Summary

### Is Redis Required?

**No, Redis is optional.** The Truva-G3 framework is designed with interface abstractions that allow Redis to be disabled or swapped with alternative implementations.

### Can Redis Be Swapped with Memcached?

**Not directly.** Memcached lacks critical data structures (Pub/Sub, Lists, Sets, Sorted Sets) that Truva-G3 depends on for core functionality. A direct swap would require:

- Complete architectural redesign for HITL (Human-in-the-Loop)
- External message broker integration for Pub/Sub
- Significant performance degradation for task queuing
- Loss of atomic transaction guarantees

---

## Redis Licensing and Impact on Truva-G3 Users

### Redis License History

| Date | Event |
|------|-------|
| **Pre-2024** | Redis was BSD-licensed (fully permissive open source) |
| **March 2024** | Redis changed to dual license: RSALv2 + SSPLv1 (not OSI-approved) |
| **March 2024** | Community forked Redis 7.2.4 → **Valkey** under Linux Foundation |
| **May 2025** | Redis reversed course, released **Redis 8 under AGPLv3** |

### Current Status (2026)

**Redis 8+ is open source again** under AGPLv3 (GNU Affero General Public License v3).

| Aspect | BSD (original) | AGPLv3 (current) |
|--------|----------------|------------------|
| Commercial use | ✅ Unrestricted | ✅ Allowed |
| Modification | ✅ Unrestricted | ✅ Must share source |
| Network use | No requirements | ⚠️ Must disclose source if modified and used over network |
| Proprietary forks | ✅ Allowed | ❌ Not allowed |

### Impact on Truva-G3 Users

**Truva-G3 users are NOT affected by Redis's AGPLv3 license.**

| Scenario | Affected? | Reason |
|----------|-----------|--------|
| Using Redis as a database/cache | ❌ No | You're a *user*, not modifying Redis |
| Building agents/tools with Truva-G3 | ❌ No | Truva-G3 connects to Redis via network protocol |
| Hosting your own Redis instance | ❌ No | Running unmodified Redis is permitted |
| Using managed Redis services | ❌ No | Cloud provider handles licensing |
| Modifying Redis source code | ✅ Yes | Must release modifications under AGPLv3 |
| Offering modified Redis-as-a-Service | ✅ Yes | Network clause applies |

**Bottom Line:** Organizations using Truva-G3 to create and host agents/tools have **zero licensing concerns** with Redis. The AGPLv3 "network clause" only affects those who modify Redis itself and offer it as a service—not application developers using Redis as a dependency.

### License-Conscious Alternative

For organizations preferring fully permissive licensing:

| Option | License | Impact |
|--------|---------|--------|
| **Valkey** | BSD-3-Clause | No copyleft, no restrictions |
| **Redis 8+** | AGPLv3 | Copyleft only if you modify Redis |

Both are drop-in compatible with Truva-G3—no code changes required.

---

## Cloud Provider Availability

Redis and Valkey are **universally available** across all major cloud providers as managed services.

### Managed Service Matrix

| Cloud Provider | Service Name | Engine | Status | Regions |
|----------------|--------------|--------|--------|---------|
| **AWS** | Amazon ElastiCache for Redis | Redis 7.x | ✅ GA | All AWS regions |
| **AWS** | Amazon ElastiCache for Valkey | Valkey 7.2-8.1 | ✅ GA | All AWS regions |
| **AWS** | Amazon MemoryDB | Redis/Valkey | ✅ GA | All AWS regions |
| **Azure** | Azure Managed Redis | Redis | ✅ GA | Expanding globally |
| **Azure** | Azure Cache for Redis | Redis | ⚠️ Retiring Sept 2028 | All Azure regions |
| **GCP** | Memorystore for Redis | Redis 7.2 | ✅ GA (frozen at 7.2) | 30+ regions |
| **GCP** | Memorystore for Valkey | Valkey | ✅ GA | Expanding |
| **Alibaba Cloud** | ApsaraDB for Redis | Redis | ✅ GA | All Alibaba regions |
| **Oracle Cloud** | OCI Cache | Redis/Valkey | ✅ GA | OCI regions |
| **DigitalOcean** | Managed Caching | Valkey | ✅ GA | All DO regions |
| **Aiven** | Aiven for Redis/Valkey | Both | ✅ GA | Multi-cloud (AWS, GCP, Azure, DO) |
| **Heroku** | Heroku Data for Redis | Redis/Valkey | ✅ GA | Heroku regions |

### Pricing Highlights

| Provider | Valkey Pricing | Notes |
|----------|---------------|-------|
| **AWS ElastiCache** | 20-33% lower than Redis | Serverless option available |
| **Azure Managed Redis** | Tiered pricing | Memory/Balanced/Compute optimized |
| **GCP Memorystore** | Standard pricing | Moving development to Valkey |
| **Aiven** | Per-hour billing | Deploy on any major cloud |

### On-Premises Deployment

For self-hosted deployments, all options are readily available:

| Option | Installation | Docker | Kubernetes | Complexity |
|--------|-------------|--------|------------|------------|
| **Redis** | Package managers, source | `redis:latest` | Helm charts, operators | Low |
| **Valkey** | Package managers, source | `valkey/valkey:latest` | Helm charts available | Low |
| **DragonflyDB** | Binary, source | `docker.dragonflydb.io/dragonflydb/dragonfly` | Helm charts | Low |
| **Garnet** | .NET runtime required | `ghcr.io/microsoft/garnet` | Custom deployment | Medium |

### Deployment Recommendations

| Deployment | Recommended Option | Reason |
|------------|-------------------|--------|
| **AWS** | ElastiCache for Valkey | Best pricing, native integration |
| **Azure** | Azure Managed Redis | New service, full support |
| **GCP** | Memorystore for Valkey | Future-proof, actively developed |
| **Multi-cloud** | Aiven for Valkey | Single provider, any cloud |
| **On-premises** | Valkey or Redis | Both easy to deploy, well-documented |
| **Kubernetes** | Valkey with Helm | BSD license, excellent K8s support |

### High Availability Options

| Provider | HA Feature | SLA |
|----------|-----------|-----|
| **AWS ElastiCache** | Multi-AZ, Global Datastore | 99.99% |
| **Azure Managed Redis** | Geo-replication, Multi-region | Up to 99.999% |
| **GCP Memorystore** | Standard tier with replicas | 99.9% |
| **Self-hosted** | Redis Sentinel, Cluster mode | Depends on setup |

---

## Redis Usage in Core Module

### Files Using Redis

| File | Purpose |
|------|---------|
| `core/redis_client.go` | Unified Redis wrapper with database isolation |
| `core/redis_registry.go` | Service registration (implements `Registry` interface) |
| `core/redis_discovery.go` | Service discovery (implements `Discovery` interface) |

### Redis Commands Used

#### String Operations
- `GET`, `SET`, `DEL` - Basic key-value operations
- `INCR`, `INCRBY` - Atomic counters
- `TTL`, `EXPIRE` - TTL management
- `SETNX` - Conditional set (distributed locking)

#### Set Operations
- `SADD`, `SREM` - Add/remove from sets
- `SMEMBERS` - Get all set members
- Used for: Service indexing by type, name, and capability

#### Sorted Set Operations
- `ZADD` - Add with score
- `ZREMRANGEBYSCORE` - Remove by score range
- `ZCARD`, `ZCOUNT` - Cardinality operations

#### Transaction Operations
- `TxPipeline()` - Atomic multi-command execution
- `Exec()` - Execute pipeline

### Database Isolation Scheme

Truva-G3 uses Redis database numbers for logical separation:

```go
const (
    RedisDBServiceDiscovery = 0    // Service registry
    RedisDBRateLimiting     = 1    // Rate limiting
    RedisDBSessions         = 2    // Session storage
    RedisDBCache            = 3    // General caching
    RedisDBCircuitBreaker   = 4    // Circuit breaker state
    RedisDBMetrics          = 5    // Metrics buffering
    RedisDBTelemetry        = 6    // Telemetry data
    RedisDBLLMDebug         = 7    // LLM debug payloads
    // Reserved: 8-15 for framework extensions
)
```

---

## Redis Usage in Orchestration Module

### Files Using Redis

| File | Purpose | Critical Redis Features |
|------|---------|------------------------|
| `orchestration/redis_task_queue.go` | Task queue operations | Lists (LPUSH, BRPOP) |
| `orchestration/redis_task_store.go` | Task persistence | String ops, SCAN |
| `orchestration/redis_llm_debug_store.go` | LLM interaction logging | Sorted Sets |
| `orchestration/hitl_command_store.go` | HITL command broadcasting | **Pub/Sub** |
| `orchestration/hitl_checkpoint_store.go` | HITL checkpoint management | Sets, SetNX |

### Feature Breakdown

#### Async Task Processing

```go
// redis_task_queue.go
LPUSH  // Enqueue tasks
BRPOP  // Blocking dequeue with timeout (CRITICAL)
LLEN   // Queue length
```

#### HITL (Human-in-the-Loop)

```go
// hitl_command_store.go - Pub/Sub for real-time commands
Publish(ctx, channel, data)           // Broadcast approval/rejection
Subscribe(ctx, channel)               // Listen for commands

// hitl_checkpoint_store.go - Set operations for indexing
SAdd(ctx, indexKey, checkpointID)     // Track pending checkpoints
SMembers(ctx, indexKey)               // List pending checkpoints
SRem(ctx, indexKey, checkpointID)     // Remove from pending
SetNX(ctx, claimKey, instanceID, ttl) // Distributed locking
```

#### LLM Debug Store

```go
// redis_llm_debug_store.go - Sorted sets for timestamp indexing
ZAdd(ctx, indexKey, &redis.Z{Score: timestamp, Member: requestID})
ZRevRange(ctx, indexKey, 0, limit-1)  // Get recent records
```

---

## Interface Abstractions

Truva-G3 provides interface abstractions that enable alternative implementations.

### Core Module Interfaces

#### Memory Interface

```go
// core/interfaces.go
type Memory interface {
    Get(ctx context.Context, key string) (string, error)
    Set(ctx context.Context, key string, value string, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
}
```

**Implementations:**
- `InMemoryStore` - In-memory (default)
- Redis backend - Production

**Memcached Compatible:** Yes

#### Registry Interface

```go
// core/interfaces.go
type Registry interface {
    Register(ctx context.Context, info *ServiceInfo) error
    UpdateHealth(ctx context.Context, id string, status HealthStatus) error
    Unregister(ctx context.Context, id string) error
}
```

**Implementations:**
- `RedisRegistry` - Production
- `MockDiscovery` - Testing

**Memcached Compatible:** Partial (loses atomic set operations)

#### Discovery Interface

```go
// core/interfaces.go
type Discovery interface {
    Registry
    Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)
    FindService(ctx context.Context, serviceName string) ([]*ServiceInfo, error)
    FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error)
}
```

**Implementations:**
- `RedisDiscovery` - Production
- `MockDiscovery` - Testing

**Memcached Compatible:** No (depends on Sets for indexing)

### Orchestration Module Interfaces

#### TaskQueue Interface

```go
// orchestration/interfaces.go
type TaskQueue interface {
    Enqueue(ctx context.Context, task *Task) error
    Dequeue(ctx context.Context, timeout time.Duration) (*Task, error)
    Acknowledge(ctx context.Context, taskID string) error
    Reject(ctx context.Context, taskID string, reason string) error
    QueueLength(ctx context.Context) (int64, error)
    Close() error
}
```

**Memcached Compatible:** No (requires Lists and blocking operations)

#### TaskStore Interface

```go
// orchestration/interfaces.go
type TaskStore interface {
    Create(ctx context.Context, task *Task) error
    Get(ctx context.Context, taskID string) (*Task, error)
    Update(ctx context.Context, task *Task) error
    Delete(ctx context.Context, taskID string) error
    Cancel(ctx context.Context, taskID string) error
    ListByStatus(ctx context.Context, status TaskStatus) ([]*Task, error)
}
```

**Memcached Compatible:** Partial (basic CRUD works, `ListByStatus` requires pattern matching)

#### LLMDebugStore Interface

```go
// orchestration/llm_debug_store.go
type LLMDebugStore interface {
    RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error
    GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error)
    SetMetadata(ctx context.Context, requestID string, key, value string) error
    ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error
    ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error)
}
```

**Implementations:**
- `RedisLLMDebugStore` - Production
- `MemoryLLMDebugStore` - In-memory
- `NoOpLLMDebugStore` - Disabled (safe default)

**Memcached Compatible:** No (`ListRecent` requires Sorted Sets)

#### CommandStore Interface (HITL)

```go
// orchestration/hitl_command_store.go
type CommandStore interface {
    PublishCommand(ctx context.Context, command *Command) error
    SubscribeCommand(ctx context.Context, checkpointID string) (<-chan *Command, func(), error)
    Close() error
}
```

**Memcached Compatible:** **IMPOSSIBLE** (requires Pub/Sub)

#### CheckpointStore Interface (HITL)

```go
// orchestration/hitl_checkpoint_store.go
type CheckpointStore interface {
    Create(ctx context.Context, checkpoint *Checkpoint) error
    Get(ctx context.Context, checkpointID string) (*Checkpoint, error)
    Update(ctx context.Context, checkpoint *Checkpoint) error
    Delete(ctx context.Context, checkpointID string) error
    ListPending(ctx context.Context) ([]*Checkpoint, error)
    ListByRequest(ctx context.Context, requestID string) ([]*Checkpoint, error)
}
```

**Memcached Compatible:** No (requires Sets for indexing)

#### ScheduleStore Interface (Scheduling)

```go
// core/async_task.go
type ScheduleStore interface {
    Create(ctx context.Context, schedule *Schedule) error
    Get(ctx context.Context, id string) (*Schedule, error)
    List(ctx context.Context) ([]*Schedule, error)
    Update(ctx context.Context, schedule *Schedule) error
    Delete(ctx context.Context, id string) error
    GetDue(ctx context.Context, now time.Time) ([]*Schedule, error)
}
```

**Implementations:** `orchestration.RedisScheduleStore` (production), `orchestration.InMemoryScheduleStore` (dev/test)

**Memcached Compatible:** No (requires Sorted Sets for the time-indexed due set)

#### TaskDispatcher Interface (Scheduling)

```go
// core/async_task.go
type TaskDispatcher interface {
    Dispatch(ctx context.Context, queueName string, task *Task) error
}
```

**Implementations:** `orchestration.RedisTaskDispatcher` (LPUSH, production), `orchestration.RedisStreamsTaskDispatcher` (XADD, alternative), `orchestration.InMemoryTaskDispatcher` (dev/test)

**Memcached Compatible:** No (requires Lists or Streams)

#### TaskConsumer Interface (Scheduling)

```go
// core/async_task.go
type TaskConsumer interface {
    Consume(ctx context.Context, queueName string) (TaskHandle, error)
}

type TaskHandle interface {
    Task() *Task
    Ack(ctx context.Context) error
    Nack(ctx context.Context, reason string) error
}
```

**Implementations:**
- `orchestration.RedisTaskConsumer` -- BRPOP-based, at-most-once (default)
- `orchestration.RedisStreamsTaskConsumer` -- XREADGROUP + XACK, at-least-once (alternative, requires companion `RedisStreamsReaper` Runnable)
- `orchestration.InMemoryTaskConsumer` -- channel-based (dev/test)

Dead-letter persistence is folded into `TaskHandle.Nack` -- there is no separate `DeadLetterSink` interface.

**Memcached Compatible:** No (requires Lists or Streams)

**Contract testing:** The `core/conformance/` sub-package provides `RunTaskConsumerConformance(t, factory)` -- an executable contract test suite for any `TaskConsumer` implementation. See [Scheduled Tasks Guide](SCHEDULED_TASKS_GUIDE.md).

---

## Configuration Options

### Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `TRUVAG3_MEMORY_PROVIDER` | `inmemory` | Memory backend selection |
| `TRUVAG3_DISCOVERY_ENABLED` | `false` | Enable service discovery |
| `TRUVAG3_DISCOVERY_PROVIDER` | `redis` | Discovery backend |
| `TRUVAG3_MOCK_DISCOVERY` | `false` | Use mock discovery |
| `TRUVAG3_LLM_DEBUG_ENABLED` | `false` | Enable LLM debug storage |
| `TRUVAG3_LLM_DEBUG_TTL` | `24h` | Debug record TTL |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | `7d` | Error record TTL |
| `TRUVAG3_LLM_DEBUG_REDIS_DB` | `7` | Redis database for debug |

### Functional Options

```go
// Core module
WithMemoryProvider("inmemory")     // Use in-memory storage
WithMemoryProvider("redis")        // Use Redis storage
WithDiscovery(true, "redis")       // Enable Redis discovery
WithMockDiscovery(true)            // Use mock discovery

// Orchestration module
WithLLMDebug(true)                 // Enable LLM debug
WithLLMDebugStore(customStore)     // Inject custom backend
WithLLMDebugTTL(duration)          // Custom TTL
```

### Running Without Redis

```go
config := core.DefaultConfig()
config.Memory.Provider = "inmemory"       // Skip Redis for memory
config.Discovery.Enabled = false          // Skip Redis for discovery
config.Development.MockDiscovery = true   // Use mock instead
```

---

## Memcached Swap Feasibility

### Redis vs Memcached Feature Comparison

| Feature | Redis | Memcached | Truva-G3 Uses | Impact |
|---------|-------|-----------|-------------|--------|
| String Get/Set with TTL | ✓ | ✓ | Heavy | Compatible |
| Sets (SADD, SMEMBERS) | ✓ | ✗ | Heavy | **INCOMPATIBLE** |
| Sorted Sets (ZADD) | ✓ | ✗ | Moderate | **INCOMPATIBLE** |
| Pub/Sub | ✓ | ✗ | Heavy | **INCOMPATIBLE** |
| Lists (LPUSH, BRPOP) | ✓ | ✗ | Moderate | **INCOMPATIBLE** |
| Hashes | ✓ | ✗ | Light | **INCOMPATIBLE** |
| Transactions | ✓ | ✗ | Moderate | **INCOMPATIBLE** |
| Database Isolation | ✓ | ✗ | Yes | **INCOMPATIBLE** |
| Pattern Matching (KEYS/SCAN) | ✓ | ✗ | Moderate | **INCOMPATIBLE** |
| Atomic Operations | ✓ | ✗ | Yes | **INCOMPATIBLE** |

### Critical Showstoppers

1. **Pub/Sub for HITL** - No equivalent in Memcached
2. **Blocking Queues (BRPOP)** - Cannot implement efficient task dequeue
3. **Sets for Service Indexing** - Core to discovery functionality
4. **Sorted Sets for Timestamps** - LLM debug indexing dependent
5. **Transactions/Atomicity** - Multi-step operations lose consistency

---

## Feature-by-Feature Impact Analysis

### HITL (Human-in-the-Loop)

| Component | Redis Feature | Memcached Alternative | Effort |
|-----------|---------------|----------------------|--------|
| Command broadcasting | `Publish/Subscribe` | **NONE** - requires external broker | IMPOSSIBLE |
| Pending checkpoint index | `SAdd/SMembers/SRem` | JSON array workaround | HIGH |
| Distributed locking | `SetNX` | CAS operation (different semantics) | MEDIUM |

**Verdict:** Cannot implement HITL with Memcached alone. Requires external message broker (RabbitMQ, Kafka, etc.) for Pub/Sub functionality.

### Async Task Processing

| Component | Redis Feature | Memcached Alternative | Effort |
|-----------|---------------|----------------------|--------|
| Task enqueue | `LPUSH` | Index-based keys | HIGH |
| Task dequeue | `BRPOP` (blocking) | **Polling loop** | VERY HIGH |
| Queue length | `LLEN` | Manual counter | MEDIUM |
| List by status | `SCAN` pattern | Maintain separate indexes | HIGH |

**Verdict:** Would require polling instead of blocking, introducing latency and CPU overhead.

### LLM Debug Store

| Component | Redis Feature | Memcached Alternative | Effort |
|-----------|---------------|----------------------|--------|
| Record storage | `SET/GET` | Compatible | LOW |
| Timestamp index | `ZAdd` | Manual sorting | HIGH |
| List recent | `ZRevRange` | Fetch all + sort in memory | HIGH |

**Verdict:** Basic storage works, but `ListRecent()` becomes inefficient.

### Service Discovery

| Component | Redis Feature | Memcached Alternative | Effort |
|-----------|---------------|----------------------|--------|
| Service indexing | Sets | JSON arrays | HIGH |
| Multi-filter queries | Set intersection | Fetch all + filter | HIGH |
| Atomic registration | Transactions | Read-modify-write (race conditions) | HIGH |

**Verdict:** Would work but with significant performance degradation and race condition risks.

---

## Alternative Backend Options

### Option 1: Keep Redis (Recommended)

Redis is the right tool for this use case. It provides all required data structures with excellent performance.

### Option 2: Hybrid Approach

Use Memcached for simple caching, keep Redis for:
- HITL (requires Pub/Sub)
- Task queues (requires blocking operations)
- Service discovery (requires sets)

### Option 3: PostgreSQL + Message Broker

For environments where Redis is not an option:

| Component | Technology |
|-----------|------------|
| Key-value storage | PostgreSQL JSONB |
| Pub/Sub | RabbitMQ or Kafka |
| Task queues | PostgreSQL with `SKIP LOCKED` or dedicated queue (SQS, etc.) |
| Sets/Indexing | PostgreSQL arrays or junction tables |

**Effort:** 3-6 months of development + extensive testing

### Option 4: Custom Memcached Implementation

Would require implementing these additional interfaces:

```go
// New interfaces needed for Memcached
type MemcachedRegistry struct { ... }
type MemcachedDiscovery struct { ... }
type MemcachedTaskQueue struct { ... }
type MemcachedTaskStore struct { ... }
type MemcachedLLMDebugStore struct { ... }
type MemcachedCheckpointStore struct { ... }
// MemcachedCommandStore - IMPOSSIBLE without external Pub/Sub
```

---

## Open-Source Alternatives to Redis

This section evaluates open-source tools that can replace Redis for the specific functions Truva-G3 depends on.

### Category 1: Drop-in Redis Replacements

These are Redis-compatible alternatives that support the RESP protocol and can work with existing Redis clients.

#### Valkey (Recommended)

| Aspect | Details |
|--------|---------|
| **License** | BSD-3-Clause (fully open source) |
| **Backing** | Linux Foundation (AWS, Google, Alibaba, Tencent) |
| **Origin** | Fork of Redis 7.2.4 after Redis license change |
| **Status** | Very active development |

**Supported Features:**
- ✅ All Redis data structures (Strings, Lists, Sets, Sorted Sets, Hashes)
- ✅ Pub/Sub
- ✅ Transactions and Pipelining
- ✅ Clustering (up to 1,000 nodes)
- ✅ Multi-threaded I/O (Valkey 8.0+)

**Truva-G3 Compatibility:** **FULL** - Drop-in replacement, no code changes required.

**Performance:** Similar to Redis; Valkey 8.0 enhanced I/O multithreading to match enterprise Redis throughput.

**Considerations:**
- Valkey 8.1 does not yet support time series and vector sets (Redis 8.0 features)
- Experimental RDMA support may have stability trade-offs

**Resources:**
- [Valkey Official Site](https://valkey.io/)
- [Valkey vs Redis Comparison](https://www.dragonflydb.io/guides/valkey-vs-redis)

---

#### DragonflyDB

| Aspect | Details |
|--------|---------|
| **License** | BSL 1.1 (source-available, not OSI-approved) |
| **Architecture** | Built from scratch in C++, shared-nothing |
| **Performance** | Claims 25x higher throughput than Redis |
| **Memory** | Up to 60% better memory utilization |

**Supported Features:**
- ✅ Redis API compatible (RESP protocol)
- ✅ All data structures Truva-G3 uses
- ✅ Pub/Sub
- ✅ Multi-threaded (shared-nothing architecture)
- ⚠️ RDB snapshotting only (no AOF)

**Truva-G3 Compatibility:** **FULL** - Drop-in replacement.

**Considerations:**
- BSL license restricts cloud providers from offering as managed service
- No AOF persistence (RDB snapshots only)
- Fewer operational tools compared to Redis ecosystem

**Resources:**
- [DragonflyDB GitHub](https://github.com/dragonflydb/dragonfly)
- [DragonflyDB Guides](https://www.dragonflydb.io/guides/best-redis-alternatives-top-oss-and-managed-solutions)

---

#### KeyDB

| Aspect | Details |
|--------|---------|
| **License** | BSD-3-Clause |
| **Origin** | Multithreaded fork of Redis (2019) |
| **Performance** | 2-3x better than Redis on same hardware |

**Supported Features:**
- ✅ Full Redis compatibility
- ✅ Active Replica and Multi-Master modes
- ✅ Flash storage support

**Truva-G3 Compatibility:** **FULL** - Drop-in replacement.

**⚠️ WARNING:** As of September 2025, KeyDB has not been actively updated for 1.5 years. The main maintainer left in January 2025. **Not recommended for new projects.**

**Resources:**
- [KeyDB GitHub](https://github.com/Snapchat/KeyDB)

---

#### Microsoft Garnet

| Aspect | Details |
|--------|---------|
| **License** | MIT (fully open source) |
| **Architecture** | Built from scratch in C#/.NET |
| **Performance** | 1-2 orders of magnitude higher throughput than Redis |
| **Latency** | <300μs at 99.9th percentile |

**Supported Features:**
- ✅ RESP protocol compatible
- ✅ Native multithreading with lock-free structures
- ✅ Cluster sharding, replication, key migration
- ⚠️ Partial Redis API coverage

**Truva-G3 Compatibility:** **PARTIAL** - May require testing; not all Redis commands supported.

**Considerations:**
- Only partially supports Redis API (migration challenges)
- Best for new high-performance deployments
- Used in production at Microsoft (Azure Resource Manager)

**Resources:**
- [Garnet GitHub](https://github.com/microsoft/garnet)
- [Microsoft Research Blog](https://www.microsoft.com/en-us/research/blog/introducing-garnet-an-open-source-next-generation-faster-cache-store-for-accelerating-applications-and-services/)

---

### Category 2: Message Brokers (for Pub/Sub Replacement)

If using a non-Redis backend for storage, you'll need a separate message broker for HITL Pub/Sub functionality.

#### NATS

| Aspect | Details |
|--------|---------|
| **License** | Apache 2.0 |
| **Language** | Go |
| **Use Case** | Cloud-native, microservices, IoT |

**Features:**
- ✅ Pub/Sub messaging
- ✅ Request-Reply pattern
- ✅ Queue groups (load balancing)
- ✅ JetStream for persistence (200k msgs/sec with persistence)
- ✅ Lightweight, low latency

**Truva-G3 Integration:**
```go
// Would require new NATSCommandStore implementation
type NATSCommandStore struct {
    conn *nats.Conn
}

func (s *NATSCommandStore) PublishCommand(ctx context.Context, cmd *Command) error {
    return s.conn.Publish(channelName, data)
}

func (s *NATSCommandStore) SubscribeCommand(ctx context.Context, checkpointID string) (<-chan *Command, func(), error) {
    sub, _ := s.conn.Subscribe(channelName, handler)
    // ...
}
```

**Resources:**
- [NATS Official Site](https://nats.io/)
- [NATS vs RabbitMQ Comparison](https://gcore.com/learning/nats-rabbitmq-nsq-kafka-comparison)

---

#### RabbitMQ

| Aspect | Details |
|--------|---------|
| **License** | MPL 2.0 |
| **Protocol** | AMQP, MQTT, STOMP |
| **Use Case** | Enterprise messaging, complex routing |

**Features:**
- ✅ Pub/Sub via topic exchanges
- ✅ Message persistence and acknowledgment
- ✅ Complex routing patterns
- ✅ Management UI
- ✅ Clustering and high availability

**Truva-G3 Integration:** Would require implementing `CommandStore` interface with AMQP client.

**Resources:**
- [RabbitMQ Official Site](https://www.rabbitmq.com/)
- [RabbitMQ vs Redis](https://aws.amazon.com/compare/the-difference-between-rabbitmq-and-redis/)

---

#### Apache Kafka

| Aspect | Details |
|--------|---------|
| **License** | Apache 2.0 |
| **Use Case** | High-throughput event streaming |
| **Throughput** | Millions of messages/second |

**Features:**
- ✅ Pub/Sub with consumer groups
- ✅ Message persistence (log-based)
- ✅ Replay capability
- ✅ Exactly-once semantics

**Considerations:**
- Higher operational complexity than NATS/RabbitMQ
- Better suited for high-volume event streaming than real-time command delivery
- May be overkill for HITL use case

---

### Category 3: PostgreSQL-Based Solutions

PostgreSQL can replace Redis for several use cases, reducing infrastructure complexity.

#### PostgreSQL Feature Mapping

| Truva-G3 Need | PostgreSQL Solution | Performance |
|-------------|---------------------|-------------|
| Key-Value Storage | JSONB columns or UNLOGGED tables | 50-158% slower per op |
| Pub/Sub | `LISTEN/NOTIFY` | Adequate for moderate load |
| Task Queues | `SELECT ... FOR UPDATE SKIP LOCKED` | Good for background jobs |
| Sets | Arrays or junction tables | Depends on query complexity |
| Sorted Sets | Indexed columns with `ORDER BY` | Good with proper indexes |
| Distributed Locking | Advisory locks | Strong consistency |

**LISTEN/NOTIFY for Pub/Sub:**
```sql
-- Publisher
NOTIFY hitl_commands, '{"checkpoint_id": "abc", "action": "approve"}';

-- Subscriber (in Go with pgx)
conn.Exec(ctx, "LISTEN hitl_commands")
for {
    notification, _ := conn.WaitForNotification(ctx)
    // Process notification.Payload
}
```

**Task Queue with SKIP LOCKED:**
```sql
-- Dequeue a task atomically
UPDATE tasks
SET status = 'processing', worker_id = $1
WHERE id = (
    SELECT id FROM tasks
    WHERE status = 'pending'
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING *;
```

**Benefits:**
- Single database for everything
- ACID transactions
- Strong consistency
- Familiar SQL tooling

**Limitations:**
- Higher latency than Redis (0.1-1ms difference)
- `LISTEN/NOTIFY` doesn't queue messages (subscribers must be connected)
- No blocking dequeue (requires polling or `LISTEN`)

**Resources:**
- [PostgreSQL as Redis Replacement](https://dev.to/polliog/i-replaced-redis-with-postgresql-and-its-faster-4942)
- [Postgres Pub/Sub](https://spin.atomicobject.com/redis-postgresql/)

---

### Category 4: Distributed Coordination (for Service Discovery)

#### etcd

| Aspect | Details |
|--------|---------|
| **License** | Apache 2.0 |
| **Consistency** | Strong (Raft consensus) |
| **Use Case** | Configuration, service discovery, distributed locking |

**Features:**
- ✅ Distributed locking with leases
- ✅ Watch API for change notifications
- ✅ Strong consistency guarantees
- ✅ Used by Kubernetes for cluster state

**Truva-G3 Integration:**
```go
// Service registration with etcd
client.Put(ctx, "/services/my-tool/instance-1", serviceInfoJSON)
client.Grant(ctx, 30) // 30-second lease for TTL

// Service discovery with prefix
resp, _ := client.Get(ctx, "/services/my-tool/", clientv3.WithPrefix())
```

**Considerations:**
- Optimized for consistency over throughput
- Higher latency than Redis for high-frequency operations
- Better for configuration/discovery than caching

**Resources:**
- [etcd Official Site](https://etcd.io/)
- [Redis vs etcd Comparison](https://www.svix.com/resources/faq/redis-vs-etcd/)

---

#### HashiCorp Consul

| Aspect | Details |
|--------|---------|
| **License** | BSL 1.1 (source-available) |
| **Features** | Service discovery, health checking, KV store |

**Features:**
- ✅ Built-in service discovery and health checks
- ✅ DNS and HTTP interfaces
- ✅ Distributed locking
- ✅ Multi-datacenter support

**Considerations:**
- More feature-rich than etcd for service mesh
- BSL license may be concern for some organizations
- Higher operational complexity

---

### Recommended Combinations

#### Option A: Valkey (Simplest Migration)

```
┌─────────────────────────────────────────┐
│                 Valkey                  │
│  (Drop-in Redis replacement)            │
│                                         │
│  • All Truva-G3 features work unchanged   │
│  • BSD-3 license                        │
│  • Linux Foundation backing             │
└─────────────────────────────────────────┘
```

**Effort:** Minimal - just change connection string.

---

#### Option B: PostgreSQL + NATS (Redis-Free)

```
┌─────────────────────┐    ┌─────────────────────┐
│     PostgreSQL      │    │        NATS         │
│                     │    │                     │
│  • Task storage     │    │  • HITL Pub/Sub     │
│  • LLM debug store  │    │  • Real-time cmds   │
│  • Service registry │    │                     │
│  • Checkpoint store │    │                     │
└─────────────────────┘    └─────────────────────┘
```

**Effort:** High - requires implementing new store backends.

---

#### Option C: DragonflyDB (Performance-Focused)

```
┌─────────────────────────────────────────┐
│              DragonflyDB                │
│                                         │
│  • 25x throughput improvement           │
│  • 60% better memory efficiency         │
│  • Drop-in compatible                   │
│  • BSL license (check compatibility)    │
└─────────────────────────────────────────┘
```

**Effort:** Minimal - just change connection string.

---

### Comparison Matrix

| Solution | License | Truva-G3 Compat | Pub/Sub | Sets | Sorted Sets | Effort |
|----------|---------|---------------|---------|------|-------------|--------|
| **Valkey** | BSD-3 | ✅ Full | ✅ | ✅ | ✅ | Minimal |
| **DragonflyDB** | BSL | ✅ Full | ✅ | ✅ | ✅ | Minimal |
| **Garnet** | MIT | ⚠️ Partial | ✅ | ✅ | ✅ | Low-Medium |
| **KeyDB** | BSD-3 | ✅ Full | ✅ | ✅ | ✅ | Minimal (but unmaintained) |
| **PostgreSQL** | PostgreSQL | ⚠️ Partial | ⚠️ LISTEN/NOTIFY | ✅ Arrays | ✅ Indexed | High |
| **PostgreSQL + NATS** | Apache 2.0 | ✅ Full | ✅ | ✅ | ✅ | High |
| **etcd** | Apache 2.0 | ⚠️ Partial | ⚠️ Watch | ❌ | ❌ | Very High |

---

## Recommendations

### For Production Deployments

1. **Use Valkey** - Best drop-in replacement with full open-source license and strong backing
2. **Use Redis** - If already deployed and working well; AGPLv3 does not affect application users
3. **All Redis-dependent features are optional** - Disable what you don't need
4. **Use managed services** (AWS ElastiCache, Azure Managed Redis, GCP Memorystore) for reliability

### For Development/Testing

1. **Use mock implementations** - `WithMockDiscovery(true)`
2. **Use in-memory stores** - `WithMemoryProvider("inmemory")`
3. **Disable optional features** - Set `TRUVAG3_DISCOVERY_ENABLED=false`

### If Redis Is Not an Option

1. **Use Valkey** - Fully open-source, drop-in compatible, backed by Linux Foundation
2. **Evaluate DragonflyDB** - If BSL license is acceptable and you need maximum performance
3. **PostgreSQL + NATS** - For a completely different stack with familiar technologies
4. **Avoid KeyDB** - Unmaintained as of 2025; not recommended for new projects

---

## Appendix: Implementation Checklist for Alternative Backends

If implementing a new backend, ensure these interfaces are satisfied:

### Core Module

- [ ] `Memory` interface - Simple key-value (Memcached compatible)
- [ ] `Registry` interface - Service registration
- [ ] `Discovery` interface - Service discovery with filtering

### Orchestration Module

- [ ] `TaskQueue` interface - Task queue operations
- [ ] `TaskStore` interface - Task persistence
- [ ] `LLMDebugStore` interface - Debug record storage
- [ ] `CommandStore` interface - Pub/Sub for HITL commands
- [ ] `CheckpointStore` interface - HITL checkpoint management
- [ ] `ScheduleStore` interface - Schedule persistence + due-index
- [ ] `TaskDispatcher` interface - Task queue writes
- [ ] `TaskConsumer` interface + `TaskHandle` - Task queue reads with settlement

### Required Capabilities by Interface

| Interface | Key-Value | Sets | Sorted Sets | Pub/Sub | Lists | Streams | Transactions |
|-----------|-----------|------|-------------|---------|-------|---------|--------------|
| Memory | ✓ | | | | | | |
| Registry | ✓ | ✓ | | | | | ✓ |
| Discovery | ✓ | ✓ | | | | | |
| TaskQueue | ✓ | | | | ✓ | | |
| TaskStore | ✓ | | | | | | |
| LLMDebugStore | ✓ | | ✓ | | | | |
| CommandStore | | | | ✓ | | | |
| CheckpointStore | ✓ | ✓ | | | | | |
| ScheduleStore | ✓ | | ✓ | | | | |
| TaskDispatcher (BRPOP) | ✓ | | | | ✓ | | |
| TaskDispatcher (Streams) | ✓ | | | | | ✓ | |
| TaskConsumer (BRPOP) | ✓ | | | | ✓ | | |
| TaskConsumer (Streams) | ✓ | | | | ✓ | ✓ | |

---

## Sources and References

### Redis Alternatives
- [Best Redis Alternatives - DragonflyDB Guide](https://www.dragonflydb.io/guides/best-redis-alternatives-top-oss-and-managed-solutions)
- [Valkey vs Redis Comparison](https://www.dragonflydb.io/guides/valkey-vs-redis)
- [Top Redis Alternatives 2025 - BullMQ](https://bullmq.io/articles/redis/top-redis-alternatives-2025/)
- [Microsoft Garnet GitHub](https://github.com/microsoft/garnet)
- [Introducing Garnet - Microsoft Research](https://www.microsoft.com/en-us/research/blog/introducing-garnet-an-open-source-next-generation-faster-cache-store-for-accelerating-applications-and-services/)

### Message Brokers
- [NATS vs RabbitMQ vs Kafka Comparison](https://gcore.com/learning/nats-rabbitmq-nsq-kafka-comparison)
- [Pulsar vs RabbitMQ vs NATS](https://streamnative.io/pulsar/pulsar-vs-rabbitmq-vs-nats)
- [RabbitMQ vs Redis - AWS](https://aws.amazon.com/compare/the-difference-between-rabbitmq-and-redis/)

### PostgreSQL Solutions
- [I Replaced Redis with PostgreSQL](https://dev.to/polliog/i-replaced-redis-with-postgresql-and-its-faster-4942)
- [PostgreSQL Does Queuing, Locking, & Pub/Sub](https://spin.atomicobject.com/redis-postgresql/)
- [Postgres is a Great Pub/Sub & Job Server](https://webapp.io/blog/postgres-is-the-answer/)

### Distributed Coordination
- [Redis vs etcd Comparison](https://www.svix.com/resources/faq/redis-vs-etcd/)
- [etcd vs Other Key-Value Stores](https://etcd.io/docs/v3.5/learning/why/)

### Cloud Provider Services
- [AWS ElastiCache for Valkey](https://aws.amazon.com/elasticache/what-is-valkey/)
- [AWS ElastiCache Valkey Pricing](https://aws.amazon.com/blogs/database/reduce-your-amazon-elasticache-costs-by-up-to-60-with-valkey-and-cudos/)
- [Year One of Valkey - AWS Blog](https://aws.amazon.com/blogs/database/year-one-of-valkey-open-source-innovations-and-elasticache-version-8-1-for-valkey/)
- [Azure Managed Redis Pricing](https://azure.microsoft.com/en-us/pricing/details/managed-redis/)
- [Azure Cache for Redis Overview](https://azure.microsoft.com/en-us/products/cache)
- [GCP Memorystore for Redis](https://cloud.google.com/memorystore/docs/redis/)
- [GCP Memorystore Regions](https://docs.cloud.google.com/memorystore/docs/redis/regions)
- [Aiven for Valkey](https://aiven.io/valkey)
- [Oracle Supports Valkey](https://blogs.oracle.com/cloud-infrastructure/post/oracle-supports-valkey)
- [Valkey Participants](https://valkey.io/participants/)

### Valkey Project
- [Valkey Official Site](https://valkey.io/)
- [2024: The Year of Valkey](https://valkey.io/blog/2024-year-of-valkey/)
- [Valkey: An Investment in Open Source](https://valkey.io/blog/valkey-investment-in-open-source/)

---

*Document generated: January 2026*
*Last updated: January 24, 2026*
