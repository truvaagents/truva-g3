# TruvaG3 Memory Module Architecture

**Version**: 1.0
**Module**: `github.com/truvaagents/truva-g3/memory`
**Purpose**: Pluggable storage backend implementations for cross-agent shared memory
**Audience**: Core contributors, module developers, system architects, LLM-based coding agents

---

## Table of Contents

1. [Overview](#overview)
   - [1.1 The Problem](#11-the-problem)
   - [1.2 The Four Memory Types](#12-the-four-memory-types)
   - [1.3 Memory in the Orchestration Pipeline](#13-memory-in-the-orchestration-pipeline)
   - [1.4 Memory Across an Agent's Lifetime](#14-memory-across-an-agents-lifetime)
2. [Architectural Position](#architectural-position)
3. [Design Philosophy](#design-philosophy)
   - [1. Micro-Kernel: Core Defines Contracts, Memory Provides Implementations](#1-micro-kernel-core-defines-contracts-memory-provides-implementations)
   - [2. One Backend Per Concern, Not One Backend For All](#2-one-backend-per-concern-not-one-backend-for-all)
   - [3. Backend Selection at Import Time](#3-backend-selection-at-import-time)
   - [4. Fail-Open, Never Block the Pipeline](#4-fail-open-never-block-the-pipeline)
   - [5. Domain-Scoped by Default, Physically Isolated When Required](#5-domain-scoped-by-default-physically-isolated-when-required)
   - [6. Agents Stay Lightweight](#6-agents-stay-lightweight)
4. [Module Dependencies](#module-dependencies)
5. [Core Interfaces This Module Implements](#core-interfaces-this-module-implements)
6. [Component Architecture](#component-architecture)
7. [Storage Backend Pattern](#storage-backend-pattern)
   - [Backend Implementation Contract](#backend-implementation-contract)
   - [Default Backend: Qdrant (SharedKnowledge)](#default-backend-qdrant-sharedknowledge)
   - [Adding New Backends](#adding-new-backends)
8. [Memory Sharing Topology](#memory-sharing-topology)
   - [Sharing Hierarchy](#sharing-hierarchy)
   - [Sharing Rules](#sharing-rules)
   - [Standard Domains — Logical Isolation](#standard-domains--logical-isolation)
   - [Regulated Domains — Physical Isolation](#regulated-domains--physical-isolation)
   - [Nested Agent Delegation](#nested-agent-delegation)
9. [Integration Pattern](#integration-pattern)
10. [Configuration System](#configuration-system)
11. [Error Handling](#error-handling)
12. [Testing Strategy](#testing-strategy)
13. [Performance Considerations](#performance-considerations)
14. [Security & Compliance](#security--compliance)
15. [What This Module Does NOT Do](#what-this-module-does-not-do)

---

## Overview

### 1.1 The Problem

TruvaG3 agents are independent K8s services. Each `ProcessRequest` call starts with a blank slate. When the `event-driven-agent` restarts a pod due to an OOMKilled alert, and 5 minutes later the `devops-chat-agent` receives a query about the same service being slow — it has no idea the restart happened. It investigates from scratch, potentially making conflicting decisions.

This creates three concrete failures:
- **Duplicated work**: Two agents investigate the same root cause independently, wasting LLM tokens and tool invocations.
- **Missing context**: Agent B makes a decision without knowing Agent A's recent action on the same entity, leading to conflicting remediations.
- **No institutional learning**: Each agent starts from zero. If the system resolved the same class of issue 50 times, the 51st still runs the full investigation.

### 1.2 The Four Memory Types

The memory system is decomposed into four independent interfaces, each solving a distinct problem:

| Memory Type | Interface (in `core`) | What It Stores | Problem It Solves |
|---|---|---|---|
| **Episodic Memory** | `core.EpisodicMemory` | Structured events: "Agent X did Y to entity Z at time T with outcome W" | **Cross-agent visibility.** Agent B knows what Agent A did 5 minutes ago. |
| **Investigation Coordinator** | `core.InvestigationCoordinator` | Who is currently investigating which entity (claim/release with TTL) | **Deduplication.** Two agents don't waste LLM tokens on the same entity simultaneously. **Known limitation:** the lock key is `entityID` only (not `type:id`), so entities of different types that happen to share an ID can collide (e.g., `pod/foo` and `service/foo` both lock on `foo`). Low-impact in practice for most domains. |
| **Shared Knowledge** | `core.SharedKnowledge` | Embedded knowledge fragments: learned patterns, resolution strategies | **Institutional learning.** The 51st OOMKill resolves in 2 steps instead of 5. |
| **Reflection** | `core.MemoryReflector` | Higher-level synthesized insights from many episodic events | **Knowledge compaction.** Prevents episodic memory from growing unboundedly. |

Each interface can be implemented, deployed, and scaled independently. Phase 1 only requires Episodic + Coordinator. SharedKnowledge adds in Phase 2. Reflection adds in Phase 3. Missing types degrade gracefully — the pipeline continues without them.

### 1.2b User Memory (Per-User Private Facts)

Separate from the four shared memory types above, **User Memory** stores per-user private facts for personal assistant agents. It is NOT shared across agents or users — it's scoped to a single `user_id`.

| Memory Type | Interface (in `core`) | What It Stores | Problem It Solves |
|---|---|---|---|
| **User Memory** | `core.UserMemory` | Personal facts: preferences, identity, constraints, session summaries | **Cross-session personalization.** "I'm vegetarian" said once, remembered forever. |

**Key differences from shared memory:**

| Concern | Shared Memory | User Memory |
|---------|--------------|-------------|
| Scoping | Agent domain ("infrastructure") | User ID ("neelabh") |
| Visibility | All agents in same domain | One user only |
| Content | "Agent X restarted pod Y" | "User prefers DFW airport" |
| Lifetime | 24h lookback + knowledge persists | Months/years, evolves with user |
| Deletion | No per-user delete | GDPR: `Forget("user-42")` deletes everything |
| Backend | Redis streams (episodic) + Qdrant (knowledge) | Qdrant (`truvag3_user_memory` collection) or InMemory |

**Implementations in this module:**

| Interface | Vector DB (production) | In-Memory (testing) |
|-----------|----------------------|-------------------|
| `core.UserMemory` | `VectorUserMemory` (Qdrant) | `InMemoryUserMemory` |
| `core.UserMemoryAdmin` | `VectorUserMemory` | `InMemoryUserMemory` |

The `VectorUserMemory` backend generates embeddings internally (via injected `core.EmbeddingClient`) — unlike `SharedKnowledge` which receives pre-embedded fragments. This is because `UserFact` in `core` intentionally has no `Embedding` field (core must not assume all backends use client-side vector embeddings — per core/ARCHITECTURE.md §Zero Assumptions).

**Factory:** `NewUserMemoryBackend(logger, opts...)` auto-detects the backend from `TRUVAG3_VECTOR_DB_URL`. Returns `UserMemoryBackend` with `ToDeps()` for `BuildUserMemoryHooks()`.

**Pipeline hooks:** `UserMemoryEnrichmentHook` (BeforePlanning) and `UserMemoryExtractionHook` (AfterSynthesis) live in the `orchestration` module — same separation as shared memory hooks.

### 1.3 Memory in the Orchestration Pipeline

Memory integrates into the existing `ProcessRequest` pipeline via **five hook invocations** (three core memory hooks plus two activity coordination hooks):

```
User Request
    │
    ▼
┌─────────────────────────────────────────────────────────┐
│  BeforePlanningHook  ← MEMORY READ                      │
│                                                         │
│  1. Query recent domain events (always — embedding-     │
│     based retrieval, no entity extraction needed)       │
│  2. Entity-keyed event lookup (OPT-IN — see note)       │
│  3. Investigation claim check (OPT-IN — see note)       │
│  4. Search shared knowledge for relevant learnings      │
│  5. Inject all context into PipelineContext.Enrichments  │
│     → LLM sees this when generating its execution plan  │
└─────────────────────────────────────────────────────────┘
    │
    ▼
  Phase Loop (LLM planning → DAG execution, possibly multi-phase)
    │
    ▼
┌─────────────────────────────────────────────────────────┐
│  AfterExecutionHook  ← MEMORY WRITE                     │
│                                                         │
│  1. Record structured AgentEvent(s) from execution      │
│  2. Release investigation claim                         │
│  → Now visible to ALL agents on their next request      │
└─────────────────────────────────────────────────────────┘
    │
    ▼
  Synthesis (LLM combines results)
    │
    ▼
┌─────────────────────────────────────────────────────────┐
│  AfterSynthesisHook  ← KNOWLEDGE WRITE (async)          │
│                                                         │
│  1. Extract knowledge fragment from completed execution │
│  2. Embed and store in SharedKnowledge backend           │
│  → Future agents find this via semantic search           │
└─────────────────────────────────────────────────────────┘
```

**Critical details:**
- `BeforePlanningHook` fires **once** before the phase loop. Continuation phases (Phase 2+) don't get additional memory reads — they inherit the enrichment from Phase 1.
- `AfterExecutionHook` fires **once** after all phases complete. It receives the combined result of all phases.
- Memory operations add ~5-15% to the planning prompt size (~200-600 tokens), capped by a configurable `MaxEnrichmentTokens` (default 2000).
- When a step targets another **orchestrator agent** (nested delegation), the child agent's hooks run independently. The `X-TruvaG3-Investigation-Owner` header prevents claim collisions, and `AgentEvent.ParentEvent` links parent/child event trees.
- **Pre-planning entity-keyed lookup is opt-in** (steps 2-3 in the diagram above). The framework default for the enrichment hook's entity extractor is `NoOpEntityExtractor`, which extracts no entities pre-planning. Without entities, the entity-keyed history lookup returns nothing and the investigation claim check is a no-op. This is intentional: pre-planning, the `EventSummarizer` LLM hasn't run yet, so the post-execution `LLMEntityExtractor` would always fall through; making the no-op explicit removes ambiguity. The embedding-based recent-events retrieval (step 1) and shared knowledge search (step 4) still run unconditionally — they don't need entity extraction. Collision avoidance pre-planning is provided by `ActivityCoordinator` (an LLM-mediated soft-coordination mechanism that has zero dependency on entity extraction). Agents that need entity-keyed precision pre-planning can wire a domain-specific extractor via `WithBuilderEnrichmentEntityExtractor`.

### 1.4 Memory Across an Agent's Lifetime

```
Agent Starts (Day 1)
│
├── Request 1: "Investigate OOMKill on payment-service"
│   Episodic READ: nothing (empty) │ Knowledge READ: nothing (empty)
│   → Full 5-step investigation plan
│   Episodic WRITE: records event │ Knowledge WRITE: extracts learning
│
├── Request 2 (different agent, 10 min later): "payment-service is slow"
│   Episodic READ: "devops-chat-agent increased memory limit 10 min ago"
│   Knowledge READ: "OOMKill → reconciliation job"
│   → Focused 2-step plan (skips redundant investigation)
│
├── ... 50 more OOMKill incidents ...
│
├── Reflection Job (nightly cron):
│   Reads 50 episodic events, synthesizes: "OOMKill incidents cluster
│   around 2-4 AM, correlating with batch jobs. 90% resolve with memory
│   limit increase." Stores as high-importance knowledge fragment.
│
└── Request 51: "OOMKill on notification-service"
    Knowledge READ: pattern + resolution strategy
    → 1-step plan: apply known fix. 60% fewer tokens, 80% faster.
```

---

The memory module provides **production-ready implementations** of the shared memory interfaces defined in `core`. It is the bridge between abstract memory contracts (`core.EpisodicMemory`, `core.SharedKnowledge`, etc.) and concrete storage backends.

**This module follows TruvaG3's micro-kernel architecture**: the `core` module is the lightweight kernel that defines all interfaces and contracts. The `memory` module is a pluggable extension that provides storage-backend-specific implementations. Applications choose which backends to use at compile time by importing the relevant subpackage.

### What This Module Provides

Concrete implementations of `core` memory interfaces that require **external client libraries** not already in `core/go.mod`. Each backend lives in this module as a pluggable provider, following the same pattern as `ai/providers/` for AI backends.

**Current default:** Qdrant-backed `SharedKnowledge` (vector similarity search).
**Future:** pgvector, NATS JetStream episodic, valkey-search (when mature), and any community-contributed backends.

### What This Module Does NOT Provide

- **Interfaces or types** — all defined in `core/interfaces.go`
- **Pipeline hooks** — live in `orchestration/` (hooks are orchestration concerns)
- **NoOp/Mock implementations** — live in `core/` (used as zero-config defaults and for testing)

### Why This Module Exists

The `core` module's dependency footprint must stay minimal (currently: go-redis, uuid, testify). Backend-specific implementations that require heavy client libraries (gRPC for vector DBs, SQL drivers for pgvector, messaging clients for NATS) cannot live in `core` without violating the production-first principle (C8). This module absorbs those dependencies so that `core` stays lightweight and agents that don't use these backends never pull in unnecessary dependencies.

This is the same pattern as the `ai` module: `core` defines `AIClient`, `ai/` provides OpenAI/Anthropic/Gemini implementations with their respective SDKs. Similarly, `core` defines `SharedKnowledge`, `memory/` provides vector DB implementations with their respective clients.

---

## Architectural Position

```
┌──────────────────────────────────────────────────────────────┐
│                     Applications (main.go)                    │
│   Wire together: core interfaces + module impls + hooks      │
└──────────────────┬───────────────────────────────────────────┘
                   │ imports
        ┌──────────┼──────────────────────┐
        │          │                      │
┌───────▼────┐ ┌───▼──────────┐  ┌────────▼───────┐
│  memory    │ │ orchestration│  │      ai        │
│            │ │              │  │                │
│ Implements:│ │   Uses:      │  │ Implements:    │
│ - Episodic │ │ - EpisodicMem│  │ - AIClient     │
│   Memory   │ │ - SharedKnow │  │ - EmbeddingCli │
│ - SharedKnow│ │ - Coordinator│  │                │
│ - Coordinat│ │ - DigestCache│  │ Depends on:    │
│ - DigestCach│ │ - ActivityCoo│  │                │
│ - ActivityCo│ │  (via core   │  │                │
│ - Reflector│ │   interfaces)│  │                │
│ Depends on:│ │              │  │ Depends on:    │
│ - core     │ │    interfaces│  │ - core         │
│ - telemetry│ │    only)     │  │ - telemetry    │
└──────┬─────┘ └──────┬───────┘  └────────┬──────┘
       │              │                    │
       └──────────────┼────────────────────┘
                      │
              ┌───────▼───────┐
              │     core      │
              │               │
              │  Defines:     │
              │  - EpisodicMem│  ← interfaces only
              │  - SharedKnow │  ← interfaces only
              │  - Coordinator│  ← interfaces only
              │  - Reflector  │  ← interfaces only
              │  - AIClient   │  ← interfaces only
              │  - Embedding  │  ← interfaces only
              │               │
              │  Provides:    │
              │  - NoOps      │  ← safe defaults
              │  - Mocks      │  ← for testing
              └───────────────┘
```

**Key relationships:**
- `memory` → `core` (required, for interfaces and types)
- `memory` → `telemetry` (allowed, for observability — same rule as all optional modules)
- `ai` → `core` + `telemetry` (implements `core.AIClient`, `core.EmbeddingClient`)
- `orchestration` → `core` + `telemetry` (uses core interfaces for all memory/AI operations)
- `memory` does NOT import `orchestration`, `ai`, or `resilience`
- `orchestration` does NOT import `memory` — it uses `core.SharedKnowledge` interface
- Only `main.go` imports `memory`, `orchestration`, and `ai` to wire them together

---

## Design Philosophy

### 1. Micro-Kernel: Core Defines Contracts, Memory Provides Implementations

TruvaG3 follows a **micro-kernel architecture** where the `core` module is the minimal kernel:

```
┌─────────────────────────────────────────────────────────┐
│                    MICRO-KERNEL (core)                    │
│                                                          │
│  Interfaces:  EpisodicMemory, SharedKnowledge,           │
│               InvestigationCoordinator, MemoryReflector   │
│  Types:       AgentEvent, KnowledgeFragment, EventFilter │
│  Defaults:    NoOp implementations, in-memory fallbacks  │
│  NoOp impls: Safe defaults when backends unconfigured    │
│                                                          │
│  Total deps: go-redis, uuid, testify (unchanged)         │
└─────────────────────────┬───────────────────────────────┘
                          │
           ┌──────────────┼──────────────┐
           │              │              │
    ┌──────▼──────┐ ┌─────▼─────┐ ┌──────▼──────┐
    │   memory    │ │    ai     │ │ orchestration│
    │   (Qdrant)  │ │ (OpenAI)  │ │  (hooks)     │
    │             │ │ (Anthropic)│ │              │
    │  +grpc      │ │ +http     │ │  +core only  │
    │  +protobuf  │ │ +provider │ │              │
    └─────────────┘ └───────────┘ └──────────────┘
         PLUGINS (optional, import only what you need)
```

**The Rule**: If an implementation requires a dependency not already in `core/go.mod`, it goes in `memory/` (or another plugin module), not in `core/`. This keeps the kernel small and agents lightweight.

```go
// ❌ WRONG: Adding Qdrant client to core
// core/shared_memory_qdrant.go
import "github.com/qdrant/go-client"  // Adds gRPC + protobuf to EVERY TruvaG3 user

// ✅ CORRECT: Qdrant implementation in memory module
// memory/qdrant_shared_knowledge.go
import "github.com/qdrant/go-client"  // Only pulled when app imports memory/
```

### 2. One Backend Per Concern, Not One Backend For All

Each memory concern uses the **best-fit backend**, not a single backend forced to serve all needs:

| Concern | Default Backend | Why This Backend | Module |
|---------|----------------|------------------|--------|
| Episodic memory (events) | Valkey Streams | Already deployed for discovery. Mature Streams API. | `memory` |
| Investigation coordination | Valkey SETNX | Already deployed. Atomic claim/release. | `memory` |
| Semantic knowledge (vectors) | Qdrant | Purpose-built vector DB. Mature. Apache 2.0. | `memory` |
| Embeddings | Ollama (OpenAI-compatible) | Already supported via `ProviderOllama`. | `ai` |

This is not a framework opinion — it's the default. Users can swap any backend via the `core` interfaces.

### 3. Backend Selection at Import Time

Following the `ai` module's import-driven provider pattern, applications choose backends at compile time:

```go
// main.go — Application decides which backends to use

import (
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/memory"          // Imports Qdrant backend
    "github.com/truvaagents/truva-g3/orchestration"
)

func main() {
    // Valkey implementations from core (no extra import needed)
    episodic := memory.NewStreamEpisodicMemory(
        memory.WithEpisodicRedisClient(redisClient),
        memory.WithEpisodicDomain(domain),
    )
    coordinator := memory.NewAtomicLockCoordinator(
        memory.WithCoordinatorRedisClient(redisClient),
        memory.WithCoordinatorDomain(domain),
    )

    // Qdrant implementation from memory module
    knowledge := memory.NewVectorSharedKnowledge(qdrantAddr, knowledgeConfig)

    // Wire into orchestration hooks (hooks use core interfaces, not memory module)
    hooks := []core.PipelineHook{
        orchestration.NewMemoryEnrichmentHook(episodic, coordinator, knowledge, ...),
        orchestration.NewMemoryRecordHook(episodic, ...),
    }
}
```

**If an application doesn't need Qdrant** (e.g., Phase 1 only — episodic + coordination), it never imports `memory/` and never pulls in gRPC/protobuf:

```go
// Phase 1 only — no memory module import, no Qdrant dependency
import (
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration"
)

func main() {
    episodic := memory.NewStreamEpisodicMemory(
        memory.WithEpisodicRedisClient(redisClient),
        memory.WithEpisodicDomain(domain),
    )
    coordinator := memory.NewAtomicLockCoordinator(
        memory.WithCoordinatorRedisClient(redisClient),
        memory.WithCoordinatorDomain(domain),
    )
    // SharedKnowledge stays nil → hooks gracefully skip knowledge queries
}
```

### 4. Fail-Open, Never Block the Pipeline

Memory is an enhancement, not a prerequisite. The orchestration pipeline **must never fail** due to memory unavailability:

```go
// ✅ CORRECT: Memory failure is logged and skipped
func (q *VectorSharedKnowledge) SearchKnowledge(ctx context.Context, ...) ([]core.ScoredKnowledge, error) {
    results, err := q.client.Search(ctx, ...)
    if err != nil {
        q.logger.WarnWithContext(ctx, "Qdrant search failed, returning empty results", ...)
        return nil, nil  // Return empty, not error — let the pipeline continue
    }
    return results, nil
}

// ❌ WRONG: Memory failure aborts the request
func (q *VectorSharedKnowledge) SearchKnowledge(ctx context.Context, ...) ([]core.ScoredKnowledge, error) {
    results, err := q.client.Search(ctx, ...)
    if err != nil {
        return nil, fmt.Errorf("qdrant unavailable: %w", err)  // This would break the pipeline
    }
}
```

**Rationale**: Per FRAMEWORK_DESIGN_PRINCIPLES.md — "Missing optional dependencies should not break core functionality." An LLM can still generate a reasonable plan without memory context. It just won't benefit from cross-agent visibility or institutional learning.

**Error propagation rule**: Memory implementations return `(nil, nil)` on infrastructure failure, never `(nil, error)`. Errors are logged with context (trace ID, agent name, operation) for debugging. The calling hook treats `nil` results as "no memory available" and continues.

### 5. Domain-Scoped by Default, Physically Isolated When Required

Memory scoping operates at two levels:

**Logical isolation (default):** All agents share the same Qdrant collection. Domain scoping is enforced via payload filtering on every query. An agent in domain "commerce" cannot see "infrastructure" domain fragments because every `SearchKnowledge` call includes a domain filter condition.

```go
// Every search includes domain scoping — not optional, not bypassable
filter := &qdrant.Filter{
    Must: []*qdrant.Condition{
        qdrant.NewMatch("scope", "global"),  // OR matching callerDomain
    },
}
```

**Physical isolation (compliance):** Regulated domains (HIPAA, PCI-DSS) get a separate Qdrant instance. Same code, same interface — only the connection address differs. The `memory` module does not enforce this — the application configures which Qdrant address to use per domain. K8s NetworkPolicies enforce the boundary.

### 6. Agents Stay Lightweight

No memory implementation in this module runs **in-process** within the agent. All backends are external services. Agents maintain their 8-20MB runtime footprint. The memory module adds dependencies to the compiled binary (~5-10MB for gRPC), but not to the agent's runtime memory.

```
Agent memory budget:
  Base agent:     8-20MB (unchanged)
  + gRPC client:  ~2MB heap (connection pool, buffers)
  = Total:        10-22MB

NOT acceptable:
  + In-process HNSW index: 4-388MB depending on fragments
  = Total:        12-408MB  ← violates lightweight agent principle
```

---

## Module Dependencies

### Dependency Decision

```go
// memory/go.mod
module github.com/truvaagents/truva-g3/memory

require (
    github.com/truvaagents/truva-g3/core v0.1.0       // Required: interfaces and types
    github.com/truvaagents/truva-g3/telemetry v0.1.0   // Allowed: observability
    github.com/qdrant/go-client v1.17.0             // Qdrant gRPC client (Apache 2.0)
)
```

### What This Module May Import

```go
// ✅ ALLOWED
import "github.com/truvaagents/truva-g3/core"       // Interfaces, types, config
import "github.com/truvaagents/truva-g3/telemetry"   // Metrics, tracing, logging

// ❌ FORBIDDEN
import "github.com/truvaagents/truva-g3/ai"           // No AI dependency
import "github.com/truvaagents/truva-g3/orchestration" // No orchestration dependency
import "github.com/truvaagents/truva-g3/resilience"    // No resilience dependency
```

### Why Not Import AI?

The `SharedKnowledge.StoreKnowledge` method receives **pre-embedded** `KnowledgeFragment`s (with `Embedding []float32` already populated). The embedding step is owned by the caller (the orchestration hook), not the storage backend. This keeps the `memory` module independent of any specific embedding provider.

```go
// The caller embeds BEFORE calling StoreKnowledge
embedding, _ := embeddingClient.GenerateEmbeddings(ctx, []string{fragment.Content}, nil)
fragment.Embedding = embedding.Embeddings[0]

// memory module stores the pre-embedded fragment — no AI dependency needed
knowledge.StoreKnowledge(ctx, fragment)
```

---

## Core Interfaces This Module Implements

All interfaces are defined in `core/interfaces.go`. This module provides concrete backend implementations for interfaces that require **external client libraries not in `core/go.mod`**.

### Implementation Responsibility Split

The decision of where an implementation lives follows one rule: **does it need a dependency that core doesn't already have?**

| Core Interface | Lives In | Implementation | Why There |
|---|---|---|---|
| `core.EpisodicMemory` | `memory/` | `StreamEpisodicMemory` (Redis), `InMemoryEpisodicMemory` | Redis Streams for event storage, in-memory for testing |
| `core.InvestigationCoordinator` | `memory/` | `AtomicLockCoordinator` (Redis) | SETNX-based atomic locking |
| `core.SharedKnowledge` | `memory/` | `VectorSharedKnowledge` (Qdrant) | Requires vector DB gRPC client |
| `core.MemoryReflector` | `memory/` | `LLMMemoryReflector` | Uses `core.AIClient` + `core.EpisodicMemory` |
| `core.ActivityCoordinator` | `memory/` | `RedisActivityCoordinator`, `InMemoryActivityCoordinator` | Transient KV with TTL for real-time signals |
| `core.DigestCache` | `memory/` | `RedisDigestCache`, `InMemoryDigestCache` | Cached LLM digests to avoid redundant compaction |
| `core.UserMemory` | `memory/` | `VectorUserMemory` (Qdrant), `InMemoryUserMemory` | Per-user private facts with vector search |

**Note on placement:** All memory implementations live in `memory/` to keep `core` lightweight. The `core` module provides only NoOp defaults (used when no backend is configured) and Mock implementations (for testing).

### Future backends in this module

Any `core` interface implementation that needs an external client library belongs here:

| Core Interface | Potential Backend | Client Library Required |
|---|---|---|
| `core.SharedKnowledge` | pgvector | `pgvector/pgvector-go` + `jackc/pgx` |
| `core.SharedKnowledge` | valkey-search (when mature) | May need `go-redis` version upgrade for FT.* commands |
| `core.EpisodicMemory` | NATS JetStream | `nats-io/nats.go` |
| `core.SharedKnowledge` | Weaviate, Milvus, etc. | Respective Go clients |

---

## Component Architecture

```
memory/
├── ARCHITECTURE.md                     # This document
├── README.md                           # Quick-start guide
├── go.mod / go.sum
├── doc.go                              # Package documentation
├── stream_episodic.go                  # StreamEpisodicMemory + AtomicLockCoordinator (Redis)
├── inmemory_episodic.go                # InMemoryEpisodicMemory (testing)
├── vector_shared_knowledge.go          # VectorSharedKnowledge (Qdrant)
├── vector_config.go                    # Qdrant config + Option functions
├── reflector.go                        # LLMMemoryReflector
├── redis_activity_coordinator.go       # RedisActivityCoordinator
├── inmemory_activity_coordinator.go    # InMemoryActivityCoordinator (testing)
├── redis_digest_cache.go               # RedisDigestCache
└── inmemory_digest_cache.go            # InMemoryDigestCache (testing)
```

### Package Documentation (`doc.go`)

```go
// Package memory provides storage backend implementations for TruvaG3's
// cross-agent shared memory interfaces defined in core.
//
// This package exists to isolate heavy client dependencies (gRPC, protobuf)
// from the core module. Applications that don't use shared knowledge
// (Phase 2) never need to import this package.
//
// Default backend: Qdrant (Apache 2.0, gRPC).
//
// Usage:
//
//	knowledge, err := memory.NewVectorSharedKnowledge(
//	    memory.WithAddress("qdrant:6334"),
//	)
//	defer knowledge.Close()
//
// See ARCHITECTURE.md for design decisions and integration patterns.
package memory
```

### Implementation Structure (using default Qdrant backend as example)

Every backend implementation follows the same structural pattern:

```go
// <Backend>SharedKnowledge implements core.SharedKnowledge using <backend>.
type <Backend>SharedKnowledge struct {
    conn           <connection_type>       // Underlying connection (for Close())
    client         <client_type>           // Backend-specific client
    collectionName string                  // Storage container name
    vectorSize     int                     // Embedding dimension
    logger         core.Logger             // Optional, NoOp default
    telemetry      core.Telemetry          // Optional, NoOp default
}
```

**Design constraints (apply to ALL backends):**
- **Stateless**: No in-memory caches, no local state. All state lives in the backend.
- **Thread-safe**: Backend clients must be safe for concurrent use. No application-level mutexes.
- **Context-aware**: All operations accept `context.Context` for cancellation and tracing.
- **Fail-open**: Runtime errors are logged and return `(nil, nil)`, never abort the caller.
- **Graceful shutdown**: Implements `Close() error` to clean up connections. Applications should call `defer knowledge.Close()` after construction.

```go
// Graceful shutdown — per FRAMEWORK_DESIGN_PRINCIPLES §Component Lifecycle
func (x *XxxSharedKnowledge) Close() error {
    if x.conn != nil {
        return x.conn.Close()
    }
    return nil
}
```

---

## Storage Backend Pattern

### Backend Implementation Contract

Every `SharedKnowledge` backend implementation in this module MUST:

1. **Implement `core.SharedKnowledge` interface** — the two methods (`StoreKnowledge`, `SearchKnowledge`)
2. **Enforce scope filtering at query time** — `callerDomain` parameter controls visibility:
   - `ScopeGlobal` fragments: visible to all callers
   - `ScopeSharedDomain` fragments: visible only to callers in the same domain
   - `ScopePrivate` fragments: **never stored in SharedKnowledge** — `StoreKnowledge` must reject them
3. **Receive pre-embedded fragments** — the `KnowledgeFragment.Embedding` field is populated by the caller (orchestration hook using `core.EmbeddingClient`). Backends store and search embeddings, they don't generate them. This keeps the module independent of any AI provider.
4. **Follow the fail-open pattern** — runtime errors return `(nil, nil)`, never `(nil, error)`. Log and continue.
5. **Follow the fail-fast pattern for startup** — constructor returns an error if the backend is unreachable. Don't silently degrade at startup.
6. **Implement `Close() error`** — for graceful shutdown of connections.
7. **Use `WithXXX() Option` functions** — for configuration, following the framework's standard pattern.
8. **Accept `core.Logger` and `core.Telemetry`** — via option functions, defaulting to NoOp.
9. **Be stateless** — no in-memory caches, no local state. All state lives in the backend.
10. **Be thread-safe** — multiple goroutines will call methods concurrently.

```go
// The pattern every backend implementation follows:

// Constructor with option functions
func NewXxxSharedKnowledge(opts ...Option) (*XxxSharedKnowledge, error) {
    // 1. Apply defaults
    // 2. Override with env vars
    // 3. Apply explicit options (highest priority)
    // 4. Connect to backend (fail-fast)
    // 5. Return implementation
}

// StoreKnowledge — validate scope, store fragment
func (x *XxxSharedKnowledge) StoreKnowledge(ctx context.Context, fragment core.KnowledgeFragment) error {
    if fragment.Scope == core.ScopePrivate {
        return fmt.Errorf("private fragments cannot be stored in shared knowledge")
    }
    // Store in backend...
}

// SearchKnowledge — build scope filter, search, return scored results
func (x *XxxSharedKnowledge) SearchKnowledge(ctx context.Context, callerDomain string,
    namespace string, query string, topK int, weights core.RetrievalWeights) ([]core.ScoredKnowledge, error) {
    // Build scope filter: global OR (shared_domain AND matching domain)
    // Search backend with embedding similarity + scope filter
    // Apply weighted scoring (recency, relevance, importance)
    // Return results, or (nil, nil) on error
}

// Close — release backend connection
func (x *XxxSharedKnowledge) Close() error { ... }
```

### Default Backend: Qdrant (SharedKnowledge)

The current default implementation uses Qdrant as the vector search backend. This is a **batteries-included default**, not an architectural requirement. It can be swapped for any other backend (pgvector, Weaviate, Milvus) by providing an alternative implementation of `core.SharedKnowledge`.

**Why Qdrant was chosen as default:** Purpose-built vector DB, Apache 2.0 license, 4 years mature, 20K+ GitHub stars, official Go client (gRPC), lightweight Rust binary (~128Mi pod). See the implementation plan's Decision Log for the full evaluation.

**Qdrant-specific storage schema:**

```
Collection: truvag3_knowledge (configurable)
  Vector:  embedding (dimension configurable, default 768)
  Payload: fragment_id, namespace, agent_domain, scope, content,
           importance, created_at, last_accessed, access_count,
           source_events, metadata
```

**Qdrant-specific scope enforcement:** Uses Qdrant payload filter conditions that run **inside** the HNSW traversal (not post-filter), so performance is unaffected by the number of domains.

**Qdrant-specific multi-tenancy:** Follows Qdrant's tiered multitenancy (v1.16+):
- Default: single collection with payload-based filtering
- Growth: named shards per high-volume domain
- Compliance: separate Qdrant instance per regulated domain

### Adding New Backends

New backends follow the same contract. Example for a pgvector implementation:

```go
// Step 1: Implement the core interface + backend contract
type PgvectorSharedKnowledge struct {
    pool *pgxpool.Pool
    // ...
}

func (p *PgvectorSharedKnowledge) StoreKnowledge(ctx context.Context, fragment core.KnowledgeFragment) error {
    if fragment.Scope == core.ScopePrivate {
        return fmt.Errorf("private fragments cannot be stored in shared knowledge")
    }
    // INSERT INTO knowledge_fragments (embedding, content, scope, agent_domain, ...) VALUES ($1, $2, ...)
}

func (p *PgvectorSharedKnowledge) SearchKnowledge(ctx context.Context, callerDomain string, ...) ([]core.ScoredKnowledge, error) {
    // SELECT * FROM knowledge_fragments
    // WHERE embedding <=> $1
    //   AND (scope = 'global' OR (scope = 'shared_domain' AND agent_domain = $2))
    // ORDER BY distance LIMIT $3
}

func (p *PgvectorSharedKnowledge) Close() error { return p.pool.Close() }

// Step 2: Constructor with option functions
func NewPgvectorSharedKnowledge(opts ...PgvectorOption) (*PgvectorSharedKnowledge, error)

// Step 3: Application wires it (1-line swap from Qdrant)
knowledge, _ := memory.NewPgvectorSharedKnowledge(memory.WithPgConnString("postgres://..."))
agent.SharedKnowledge = knowledge  // Same interface, different backend
```

Backends that target the same `core` interface are **interchangeable with zero application code changes** beyond the constructor line in `main.go`.

---

## Memory Sharing Topology

TruvaG3 is a platform for building enterprise agentic systems. An enterprise deployment has multiple agent types, each with multiple replicas, serving different business domains. This section defines how memory is shared, isolated, and scoped across these boundaries.

### Sharing Hierarchy

```
Platform (TruvaG3 installation)
  └── Domain (infrastructure, commerce, healthcare)
       ├── Isolation Level: standard | regulated
       └── Agent Type (devops-chat-agent, order-exception-agent)
            └── Agent Instance (K8s replica)
```

Every `AgentEvent` and `KnowledgeFragment` carries `AgentDomain`, `AgentName`, and `Scope` (private/shared_domain/global). These three fields, combined with the `callerDomain` parameter on query methods, determine visibility.

### Sharing Rules

| Scenario | What's Shared | What's Isolated | Example |
|---|---|---|---|
| **Same agent, multiple replicas** | Everything. All replicas write with the same `AgentName`. | Nothing — replicas are interchangeable. | 3 replicas of `event-driven-agent` all write to `AgentName: "event-driven-agent"`. One replica claims entity X; others see the claim and skip. |
| **Different agents, same domain** | `ScopeSharedDomain` + `ScopeGlobal` events and knowledge. | `ScopePrivate` events (internal reasoning traces). | `devops-chat-agent` sees that `event-driven-agent` restarted pod X. Cannot see event-driven-agent's internal notes. |
| **Different agents, different domains** | Only `ScopeGlobal` events (entity state changes, cross-domain correlations). | Domain-specific events and knowledge. | `order-exception-agent` (commerce) sees "service-X is down" (global) but NOT infrastructure investigation details. |
| **Regulated domains** | Nothing. Physically isolated infrastructure. | Everything. | `clinical-agent` (healthcare/HIPAA) has its own backend instances. No data crosses the boundary. |

### Standard Domains — Logical Isolation

For standard (non-regulated) domains, all agents connect to **shared centralized stores** with **logical isolation at query time**:

```
┌──────────────────────────────────────────────────┐
│            Shared Backend (standard domains)       │
│                                                    │
│  Scope enforcement: query-time filtering           │
│  by callerDomain in every Read operation           │
│                                                    │
│  Optional hardening: Backend-level ACLs            │
│  (e.g., Valkey ACLs restricting key patterns       │
│   per domain's credentials)                        │
└──────────────────────────────────────────────────┘
```

### Regulated Domains — Physical Isolation

For domains with regulatory requirements (HIPAA, PCI-DSS, SOC 2), **logical isolation is insufficient**. These domains get physically separate infrastructure:

```
┌─────────────────────────────────────────────────┐
│  K8s Namespace: truvag3-healthcare                │
│                                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────────┐   │
│  │ Backend A │  │ Backend B │  │ Agents       │   │
│  │(dedicated)│  │(dedicated)│  │ (replicas)   │   │
│  └──────────┘  └──────────┘  └──────────────┘   │
│                                                  │
│  NetworkPolicy: deny all ingress/egress except   │
│  within this namespace. No traffic to shared.    │
└─────────────────────────────────────────────────┘
```

Same code, same interfaces — only the connection URLs differ. No code changes for compliance isolation. The `memory` module connects to whatever backend address is configured; isolation is enforced by infrastructure (K8s NetworkPolicy + separate instances), not by application code.

### Instance Identity

When multiple replicas of an agent write events, they all write `AgentName: "agent-type-name"`. This is intentional — for episodic memory and coordination, the agent type is what matters, not which replica. For debugging, replica identity is captured via `TraceID` on `AgentEvent`, which links to the distributed trace (including the specific pod).

### Nested Agent Delegation

When Agent A delegates to Agent B (an orchestrator agent), Agent B runs its own `ProcessRequest` with its own hooks. Two mechanisms prevent conflicts:
- **Investigation Coordinator**: `X-TruvaG3-Investigation-Owner` header propagated via the executor. Agent B's hook skips claiming when it sees it's working on behalf of Agent A.
- **Episodic Deduplication**: Agent B tags its events with `ParentEvent = Agent A's request_id`. Agent A records a lightweight "delegated" event instead of duplicating Agent B's detailed breakdown. The result is a tree of events, not a flat list.

---

## Integration Pattern

The memory module is **never imported by orchestration**. The wiring happens in the application:

```go
// main.go — the ONLY place where memory/ and orchestration/ meet

import (
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/memory"
    "github.com/truvaagents/truva-g3/orchestration"
    "github.com/truvaagents/truva-g3/ai"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
    // --- Storage backends ---
    episodic := memory.NewStreamEpisodicMemory(
        memory.WithEpisodicRedisClient(redisClient),
        memory.WithEpisodicDomain(domain),
    )
    coordinator := memory.NewAtomicLockCoordinator(
        memory.WithCoordinatorRedisClient(redisClient),
        memory.WithCoordinatorDomain(domain),
    )
    knowledge := memory.NewVectorSharedKnowledge("qdrant:6334", knowledgeConfig)

    // --- AI ---
    aiClient, _ := ai.NewClient(ai.WithProvider("openai"))
    embedder := ai.NewEmbeddingClient(ai.WithEmbeddingProvider("ollama"))

    // --- Orchestration hooks (use core interfaces, unaware of backends) ---
    enrichHook := orchestration.NewMemoryEnrichmentHook(
        episodic, coordinator, knowledge, embedder, "infrastructure",
        core.RetrievalWeights{Recency: 0.5, Relevance: 0.3, Importance: 0.2},
    )
    recordHook := orchestration.NewMemoryRecordHook(episodic, "my-agent", "infrastructure")

    // --- Wire into orchestrator ---
    deps := orchestration.OrchestratorDependencies{
        Discovery:     discovery,
        AIClient:      aiClient,
        PipelineHooks: []core.PipelineHook{enrichHook, recordHook},
    }
    orchestrator, _ := orchestration.CreateOrchestrator(config, deps)
}
```

---

## Configuration System

### WithXXX() Option Functions

Following the framework's configuration pattern (FRAMEWORK_DESIGN_PRINCIPLES §Implementation Guidelines), `VectorSharedKnowledge` uses option functions with intelligent defaults:

```go
// ✅ Good: Option functions with smart defaults and env var detection
knowledge, err := memory.NewVectorSharedKnowledge(
    memory.WithAddress("qdrant:6334"),           // Explicit takes priority
    memory.WithCollectionName("truvag3_knowledge"),
    memory.WithVectorSize(768),
)

// ✅ Good: Zero-config — auto-detects from environment variables
knowledge, err := memory.NewVectorSharedKnowledge()    // Uses TRUVAG3_VECTOR_DB_URL, etc.
```

### VectorDBConfig (Internal)

```go
type VectorDBConfig struct {
    // Connection
    Address        string        // "qdrant:6334" (gRPC)
    APIKey         string        // Optional API key
    TLS            bool          // Enable TLS for gRPC connection

    // Collection
    CollectionName string        // Default: "truvag3_knowledge"
    VectorSize     int           // Default: 768 (nomic-embed-text)
    Distance       string        // Default: "Cosine"

    // Timeouts
    ConnectTimeout time.Duration // Default: 5s
    SearchTimeout  time.Duration // Default: 3s

    // Auto-create collection if not exists
    AutoCreateCollection bool    // Default: true

    // Observability (injected, not configured)
    Logger    core.Logger        // Optional, NoOp default
    Telemetry core.Telemetry     // Optional, NoOp default
}
```

### Option Functions

```go
type Option func(*VectorDBConfig) error

func WithAddress(addr string) Option {
    return func(c *VectorDBConfig) error {
        c.Address = addr
        return nil
    }
}

func WithCollectionName(name string) Option {
    return func(c *VectorDBConfig) error {
        c.CollectionName = name
        return nil
    }
}

func WithVectorSize(size int) Option {
    return func(c *VectorDBConfig) error {
        if size <= 0 {
            return fmt.Errorf("invalid vector size %d: must be positive", size)
        }
        c.VectorSize = size
        return nil
    }
}

func WithLogger(logger core.Logger) Option {
    return func(c *VectorDBConfig) error {
        c.Logger = logger
        return nil
    }
}

func WithTelemetry(telemetry core.Telemetry) Option {
    return func(c *VectorDBConfig) error {
        c.Telemetry = telemetry
        return nil
    }
}
```

### Environment Variable Precedence

Following FRAMEWORK_DESIGN_PRINCIPLES §Configuration System Rules:

1. **Explicit option functions** (highest priority)
2. **Standard environment variables** (`QDRANT_URL` if one existed — N/A for Qdrant)
3. **`TRUVAG3_*` prefixed variables**
4. **Sensible defaults** (lowest priority)

```go
func NewVectorSharedKnowledge(opts ...Option) (*VectorSharedKnowledge, error) {
    // Start with defaults
    config := &VectorDBConfig{
        Address:              "localhost:6334",
        CollectionName:       "truvag3_knowledge",
        VectorSize:           768,
        Distance:             "Cosine",
        ConnectTimeout:       5 * time.Second,
        SearchTimeout:        3 * time.Second,
        AutoCreateCollection: true,
    }

    // Override with TRUVAG3_* env vars (lower priority than explicit options)
    if addr := os.Getenv("TRUVAG3_VECTOR_DB_URL"); addr != "" && config.Address == "localhost:6334" {
        config.Address = addr
    }
    if key := os.Getenv("TRUVAG3_QDRANT_API_KEY"); key != "" {
        config.APIKey = key
    }
    // ... other env vars

    // Apply explicit options (highest priority)
    for _, opt := range opts {
        if err := opt(config); err != nil {
            return nil, fmt.Errorf("invalid configuration: %w", err)
        }
    }

    // Set NoOp defaults for optional dependencies
    if config.Logger == nil {
        config.Logger = &core.NoOpLogger{}
    }
    if config.Telemetry == nil {
        config.Telemetry = &core.NoOpTelemetry{}
    }

    // Connect to Qdrant (fail-fast on configuration errors)
    conn, err := grpc.Dial(config.Address, ...)
    if err != nil {
        return nil, fmt.Errorf("failed to connect to Qdrant at %s: %w (check TRUVAG3_VECTOR_DB_URL)", config.Address, err)
    }

    return &VectorSharedKnowledge{...}, nil
}
```

### Environment Variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `TRUVAG3_VECTOR_DB_URL` | Qdrant gRPC address | `"localhost:6334"` |
| `TRUVAG3_QDRANT_API_KEY` | Qdrant API key (optional) | `""` |
| `TRUVAG3_QDRANT_COLLECTION` | Collection name | `"truvag3_knowledge"` |
| `TRUVAG3_QDRANT_VECTOR_SIZE` | Embedding dimension | `768` |

---

## Error Handling

### Fail-Open Principle

All methods follow the same pattern:

```go
func (q *VectorSharedKnowledge) SearchKnowledge(ctx context.Context, ...) ([]core.ScoredKnowledge, error) {
    // 1. Build query with scope filters
    // 2. Call Qdrant
    results, err := q.client.Search(ctx, request)
    if err != nil {
        // 3a. Log with full context for debugging
        q.logger.WarnWithContext(ctx, "Qdrant search failed", map[string]interface{}{
            "operation": "search_knowledge",
            "error":     err.Error(),
        })
        // 3b. Emit metric for monitoring
        if q.telemetry != nil {
            q.telemetry.RecordMetric("memory.knowledge.search.errors", 1, nil)
        }
        // 3c. Return empty — never propagate error to pipeline
        return nil, nil
    }
    // 4. Map Qdrant results to core.ScoredKnowledge
    return mapResults(results), nil
}
```

### Startup Errors

Connection failures at startup are **not** fail-open. If Qdrant is configured but unreachable, the constructor returns an error. This follows FRAMEWORK_DESIGN_PRINCIPLES.md: "Fail-fast for configuration errors, resilient for runtime errors."

```go
knowledge, err := memory.NewVectorSharedKnowledge(
    memory.WithAddress("qdrant:6334"),
)
if err != nil {
    // Configuration/connection error — fail fast
    log.Fatalf("Failed to initialize shared knowledge: %v", err)
}
defer knowledge.Close()
```

---

## Testing Strategy

### Unit Tests (required for every backend)

Every backend implementation must have unit tests covering:

```go
func TestSearchKnowledge_ScopeFiltering(t *testing.T) {
    // Verify that callerDomain="infrastructure" only sees
    // scope=global OR (scope=shared_domain AND agent_domain=infrastructure)
}

func TestSearchKnowledge_BackendUnavailable_ReturnsEmpty(t *testing.T) {
    // Verify fail-open: backend error → (nil, nil), not (nil, error)
}

func TestStoreKnowledge_RejectsPrivateScope(t *testing.T) {
    // Verify ScopePrivate fragments are rejected
}

func TestStoreKnowledge_MapsAllFields(t *testing.T) {
    // Verify all KnowledgeFragment fields are persisted in the backend
}

func TestClose_ReleasesConnection(t *testing.T) {
    // Verify graceful shutdown
}
```

Use mock backend clients (not real servers) for unit tests. Each backend provides its own mock.

### Integration Tests

```go
func TestSharedKnowledge_Integration(t *testing.T) {
    // Uses testcontainers or a shared backend instance
    // 1. Store a fragment in domain "infrastructure"
    // 2. Search from domain "infrastructure" → found
    // 3. Search from domain "commerce" → not found (unless scope=global)
    // 4. Store a global fragment → found from both domains
    // 5. Store a ScopePrivate fragment → rejected
}
```

### What NOT to Test Here

- Pipeline hook behavior → tested in `orchestration/memory_hooks_test.go`
- Stream episodic memory → tested in `core/shared_memory_stream_test.go`
- Embedding generation → tested in `ai/embedding_test.go`
- Interface compliance → verified by Go compiler (type system enforcement)

---

## Performance Considerations

### Latency Budget

Memory operations are on the hot path of `ProcessRequest`:

| Operation | Target Latency | Where |
|---|---|---|
| `SearchKnowledge` | < 10ms | `BeforePlanningHook` (blocking — adds to request latency) |
| `StoreKnowledge` | < 5ms | `AfterSynthesisHook` (async, non-blocking) |

Backend implementations must meet these targets. If a backend cannot meet the search latency target (e.g., high-latency network to a remote vector DB), the `MaxEnrichmentTokens` cap and the fail-open pattern prevent degraded user experience — the LLM plans without memory context rather than waiting.

### Connection Management

Backend implementations should:
- Reuse connections across requests (connection pooling or multiplexing)
- Not create per-request connections (latency and resource waste)
- Handle connection recovery transparently (auto-reconnect on transient failures)

### Memory Usage

Backend implementations in this module must NOT run vector indexes in-process. All data lives in the external backend. Agent memory footprint should increase by no more than ~2-5MB for client connection state.

```
Agent memory budget:
  Base agent:     8-20MB (unchanged)
  + Backend client: ~2-5MB heap (connection pool, buffers)
  = Total:        10-25MB (acceptable)

NOT acceptable:
  + In-process vector index: 4-388MB
  = Total:        12-408MB (violates lightweight agent principle)
```

---

## Security & Compliance

### Data Scoping

Domain scoping is enforced at the **query level**, not the application level. Every `SearchKnowledge` call includes a mandatory `callerDomain` parameter that backend implementations use to construct filters. There is no API path that bypasses domain filtering — this is a requirement of the backend implementation contract (§7).

### Regulated Domains

For compliance-critical domains, the application points the backend implementation at a **dedicated instance** within an isolated K8s namespace. The module doesn't need to know about compliance — it connects to whatever backend address is configured. Isolation is enforced by infrastructure (K8s NetworkPolicy + separate instances), not by application code. See the implementation plan's §0.6.4 for the compliance isolation topology.

### Secrets Management

Backend implementations must:
- Read credentials from environment variables or option functions, never hardcode them
- Never log secrets (API keys, connection strings with passwords)
- Support TLS for production connections
- Never store application secrets in the backend's data payloads

---

## What This Module Does NOT Do

| Responsibility | Where It Lives | Why Not Here |
|---|---|---|
| Define memory interfaces | `core/interfaces.go` | Core defines all contracts (C1) |
| Implement backends using existing core deps | `core/` (Valkey, InMemory) | go-redis already in `core/go.mod`, no new dep needed |
| Provide NoOp implementations | `core/noop.go` | Core provides all NoOps (C5) |
| Provide Mock implementations | `core/mock_memory.go` | Core provides all test mocks |
| Implement pipeline hooks | `orchestration/` | Hooks are orchestration concerns — they decide *when* to use memory |
| Generate embeddings | `ai/` | AI module owns all LLM/embedding interactions |
| Enforce K8s-level isolation | K8s manifests (NetworkPolicy) | Infrastructure concern, not code |

**In short:** Core defines *what* memory is (interfaces). Orchestration decides *when* to use it (hooks). This module provides *how* to store and retrieve it (backend implementations).
