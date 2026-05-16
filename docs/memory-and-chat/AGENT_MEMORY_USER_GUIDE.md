# Agent Memory User Guide

Hey there! This guide shows you how to give your TruvaG3 agents **shared memory** — so they can see what other agents have done, avoid duplicating work, and learn from past experience. If you've ever had two agents investigate the same incident independently (wasting tokens and time), this is the fix.

> **Working Example**
>
> Everything in this guide comes from a fully working, production-tested implementation:
> - **Agent**: [`examples/devops-chat-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/devops-chat-agent)
> - **Memory wiring**: [`examples/devops-chat-agent/main.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/devops-chat-agent/main.go) (search for `NewSharedBackends`)
> - **Main entry point**: [`examples/devops-chat-agent/main.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/devops-chat-agent/main.go)
>
> We recommend having the example open alongside this guide.

---

## Table of Contents

- [What is Agent Memory?](#what-is-agent-memory)
- [The Problem It Solves](#the-problem-it-solves)
- [How It Works](#how-it-works)
- [Quick Start](#quick-start)
- [Step 1: Environment Setup](#step-1-environment-setup)
- [Step 2: Add Memory to Your Agent](#step-2-add-memory-to-your-agent)
- [Customising Memory Behaviour](#customising-memory-behaviour)
- [What the LLM Sees](#what-the-llm-sees)
- [Activity Compaction: Scaling to Thousands of Events](#activity-compaction-scaling-to-thousands-of-events)
- [Real-Time Coordination: What Are Other Agents Doing Right Now?](#real-time-coordination-what-are-other-agents-doing-right-now)
- [Knowledge Extraction: Learning From Experience](#knowledge-extraction-learning-from-experience)
- [Long-Term Knowledge Retention: The Reflection Job](#long-term-knowledge-retention-the-reflection-job)
- [Tuning for Your Use Case](#tuning-for-your-use-case)
- [Observability: Knowing What Memory Is Doing](#observability-knowing-what-memory-is-doing)
- [Troubleshooting](#troubleshooting)
- [User Memory: Per-User Personalization](#user-memory-per-user-personalization)
  - [How Shared Memory and User Memory Compare](#how-shared-memory-and-user-memory-compare)
  - [How User Memory Works](#how-user-memory-works)
  - [User Memory Environment Setup](#user-memory-environment-setup)
  - [Adding User Memory to Your Agent](#adding-user-memory-to-your-agent)
  - [What the LLM Sees (User Profile)](#what-the-llm-sees-user-profile)
  - [Fact Reconciliation: How Contradictions Are Handled](#fact-reconciliation-how-contradictions-are-handled)
  - [Customising User Memory Behaviour](#customising-user-memory-behaviour)
  - [GDPR: Right to Erasure](#gdpr-right-to-erasure)
  - [User Memory Observability](#user-memory-observability)
  - [User Memory Troubleshooting](#user-memory-troubleshooting)
- [See Also](#see-also)

---

## What is Agent Memory?

In TruvaG3, each `ProcessRequest` call starts with a blank slate. The AI has no idea what happened on the previous request — or what other agents did. Agent memory fixes this by giving agents a shared history.

Think of it like a **team shared notebook**:
- After each request, the agent writes down what it did (episodic events)
- Before the next request, agents read the notebook to see what happened recently
- If two agents reach for the same problem, the second one sees "already being handled" and backs off

```
Without memory:                     With memory:

Agent A: "Pod X is OOMKilled"       Agent A: "Pod X is OOMKilled"
  → Investigates (5 steps)            → Investigates (5 steps)
  → Creates JIRA ticket                → Creates JIRA DEVOPS-42
  → Sends Slack notification            → Writes event to memory

Agent B: "Pod X is slow"            Agent B: "Pod X is slow"
  → Investigates (5 steps) AGAIN       → Reads memory: "Agent A restarted
  → Creates DUPLICATE ticket              Pod X and created DEVOPS-42"
  → Sends DUPLICATE notification        → 2-step plan: check status,
                                          add comment to DEVOPS-42
```

---

## The Problem It Solves

Without memory, agents have three concrete failures:

1. **Duplicated work**: Two agents investigate the same root cause independently, wasting LLM tokens and tool invocations.

2. **Missing context**: Agent B makes a decision without knowing Agent A's recent action on the same entity, leading to conflicting remediations.

3. **No institutional learning**: Each agent starts from zero. If the system resolved the same class of issue 50 times, the 51st still runs the full investigation.

Agent memory solves all three with five components:

| Component | What It Does | When It Runs |
|-----------|-------------|--------------|
| **Episodic Memory** | Records what each agent did (events) | After execution |
| **Investigation Coordinator** | Prevents two agents from investigating the same entity | Before planning |
| **Activity Compaction** | Compresses 200 events into a ~500-token digest via LLM | Before planning |
| **Activity Coordination** | Shows what agents are working on *right now* (real-time signals) | Before planning |
| **Knowledge Extraction** | Learns reusable patterns from execution results | After synthesis |

You don't need all five. Start with episodic memory + coordination (Phase 1), and add the rest as needed.

---

## How It Works

Memory plugs into the orchestration pipeline via **pipeline hooks** — middleware that runs at specific stages of request processing. You register the hooks once, and they run automatically on every request.

```
User Request
    │
    ▼
┌──────────────────────────────────────────────────────────┐
│  1. ActivityAnnouncementHook (BeforePlanning)              │
│     Announces "I'm working on X" + shows other agents'    │
│     signals → injected into <agent_coordination>           │
├──────────────────────────────────────────────────────────┤
│  2. MemoryEnrichmentHook (BeforePlanning)                  │
│     Queries episodic events + compacts into digest +       │
│     searches knowledge → injected into <agent_memory>      │
└──────────────────────────────────────────────────────────┘
    │
    ▼  LLM sees memory context when generating its plan
    │
  Planning → Execution → Synthesis
    │                       │
    ▼                       ▼
┌────────────────────┐  ┌──────────────────────────────────┐
│ 3. MemoryRecordHook│  │ 4. KnowledgeExtractionHook       │
│    (AfterExecution)│  │    (AfterSynthesis)               │
│                    │  │                                    │
│ Records events for │  │ Extracts reusable knowledge from  │
│ each execution step│  │ the response → stores in vector DB│
├────────────────────┤  ├──────────────────────────────────┤
│ 5. ActivityCleanup │  │ (async, fail-open)                │
│    (AfterSynthesis)│  │                                    │
│ Removes "working"  │  │                                    │
│ signal             │  │                                    │
└────────────────────┘  └──────────────────────────────────┘
```

The key insight: **hooks are fail-open**. If Redis is down, if the LLM compaction fails, if vector search is unavailable — the pipeline continues without memory. It just won't have the extra context.

---

## Quick Start

### Prerequisites

- A working TruvaG3 agent with an orchestrator
- Redis running (memory uses Redis for event storage and coordination)
- An AI provider API key

Optional (for knowledge extraction):
- Qdrant vector DB (for semantic knowledge search)
- An embedding endpoint (Ollama or OpenAI)

---

## Step 1: Environment Setup

Add these to your `.env` file:

```bash
# Required: Redis for episodic memory + coordination
REDIS_URL=redis://redis:6379

# Domain scoping — agents in the same domain share memory
TRUVAG3_AGENT_DOMAIN=infrastructure

# Optional: Model for LLM summarization/compaction (default: agent's model)
# Use a fast, cheap model — these calls don't need reasoning power
TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL=fast

# Optional: Vector DB for knowledge extraction (Phase 2)
# Without these, Phase 1 (events + coordination) still works
TRUVAG3_VECTOR_DB_URL=qdrant.truvag3-examples:6334
TRUVAG3_EMBEDDING_BASE_URL=http://host.docker.internal:11434/v1
TRUVAG3_EMBEDDING_MODEL=nomic-embed-text
```

**What's a domain?** Agents in the same domain see each other's events. A DevOps agent and an event-driven alert agent should share the same domain (e.g., `infrastructure`) because they operate on the same entities (pods, services, deployments). A commerce order agent would use a different domain.

### Embedding provider options

The framework's embedding client speaks any OpenAI-compatible `/v1/embeddings` endpoint, so the same three env vars work across providers — switch by changing `TRUVAG3_EMBEDDING_BASE_URL`, `TRUVAG3_EMBEDDING_MODEL`, and (where required) `TRUVAG3_EMBEDDING_API_KEY`:

```bash
TRUVAG3_EMBEDDING_BASE_URL=<provider base URL ending in /v1>
TRUVAG3_EMBEDDING_MODEL=<model ID>
TRUVAG3_EMBEDDING_API_KEY=<api key, if required>
```

`TRUVAG3_EMBEDDING_API_KEY` is omitted for local Ollama (no auth) and required for hosted providers. Precedence is: explicit `With...` options > env vars > defaults (`http://localhost:11434/v1` + `nomic-embed-text`).

| Provider | Base URL | Models (ID — output dim) |
|---|---|---|
| Ollama (local) | `http://<host>:11434/v1` | Any pulled embedding model (e.g., `nomic-embed-text` — 768, `mxbai-embed-large` — 1024) |
| OpenAI | `https://api.openai.com/v1` | `text-embedding-3-small` — 1536; `text-embedding-3-large` — 3072; `text-embedding-ada-002` — 1536 (legacy). All accept a `dimensions` parameter to truncate. Max input: 8192 tokens. |
| Mistral | `https://api.mistral.ai/v1` | `mistral-embed` — 1024 (8k tokens); `codestral-embed` — up to 3072, configurable (8k tokens) |
| Voyage AI | `https://api.voyageai.com/v1` | Hosted: `voyage-4-large`, `voyage-4`, `voyage-4-lite`, `voyage-code-3` — 1024 default (configurable to 256 / 512 / 2048, 32k tokens); `voyage-finance-2` — 1024 (32k); `voyage-law-2` — 1024 (16k); `voyage-code-2` — 1536 (16k, legacy). Note: Voyage's recommended usage includes an `input_type` parameter ("query"/"document") that the framework's client does not send, so retrieval quality may be lower than Voyage's recommended setup. |
| Jina AI | `https://api.jina.ai/v1` | `jina-embeddings-v4` — 2048, truncatable to 128 (32k tokens); `jina-embeddings-v3` — 1024, truncatable to 32 (8k tokens). A v5 family (`jina-embeddings-v5-text`, `jina-embeddings-v5-omni`) has also been announced — check Jina's docs for current hosted-API availability and model IDs. |
| Together AI | `https://api.together.xyz/v1` | Multiple open-weight models served via `/v1/embeddings` (e.g., `BAAI/bge-large-en-v1.5` — 1024; `BAAI/bge-base-en-v1.5` — 768; `WhereIsAI/UAE-Large-V1` — 1024; `intfloat/multilingual-e5-large-instruct` — 1024; `togethercomputer/m2-bert-80M-{2k,8k,32k}-retrieval` — 768). See [Together's serverless models catalog](https://docs.together.ai/docs/serverless-models) for the current list. |
| Cohere (Compatibility API) | `https://api.cohere.ai/compatibility/v1` | `embed-v4.0` — 1536 default (Matryoshka: 256, 512, 1024, 1536). Note: Cohere's standard API at `api.cohere.com/v1` is **not** OpenAI-schema-compatible — only the `/compatibility/v1` path works with this client. |

Model IDs, output dimensions, and context lengths above are sourced from each provider's official documentation at the time of writing; verify against the provider's docs before deploying.

> **Switching providers and Qdrant collections.**
> A Qdrant collection is created with the embedding dimension of the model that first writes to it. Switching to a different dimension (e.g., 768 → 1536) requires recreating the affected collections — Qdrant vector dimensions cannot change in place. Either drop the relevant collections in Qdrant and let the framework rebuild them, or use a fresh `TRUVAG3_AGENT_DOMAIN`.

---

## Step 2: Add Memory to Your Agent

Add these lines to your `main.go`, after the Redis client is available and before orchestrator initialization:

```go
import (
    "github.com/go-redis/redis/v8"
    "github.com/truvaagents/truva-g3/memory"
    "github.com/truvaagents/truva-g3/orchestration"
)

// Create Redis client (you may already have one for discovery)
redisOpt, _ := redis.ParseURL(os.Getenv("REDIS_URL"))
redisClient := redis.NewClient(redisOpt)

// Step 1: memory module creates backends
backends, _ := memory.NewSharedBackends(redisClient, agent.Logger,
    memory.WithAgentName("my-agent"),
    memory.WithDomain("infrastructure"),
)
if backends != nil {
    defer backends.Close()
}

// Step 2: orchestration module creates hooks from backends
hooks, activityCoord := orchestration.BuildMemoryHooks(backends.ToDeps(), agent.AI, agent.Logger)
```

Then pass them to your orchestrator:

```go
deps := orchestration.OrchestratorDependencies{
    Discovery:           discovery,
    AIClient:            agent.AI,
    Logger:              agent.Logger,
    Telemetry:           telemetry.GetTelemetryProvider(),
    PipelineHooks:       hooks,           // Memory hooks
    ActivityCoordinator: activityCoord,    // Real-time signals
}
orchestrator, err := orchestration.CreateOrchestrator(config, deps)
```

That's it. No `memory_setup.go` file needed. From this point on, every request automatically reads memory before planning and writes events after execution.

**With Phase 2 (knowledge search)** — add an embedding client:

```go
embedder, _ := ai.NewEmbeddingClient(ai.WithEmbeddingLogger(agent.Logger))

backends, _ := memory.NewSharedBackends(redisClient, agent.Logger,
    memory.WithAgentName("my-agent"),
    memory.WithDomain("infrastructure"),
    memory.WithEmbeddingClient(embedder),  // Enables Phase 2
)
```

**What each call does:**
- `NewSharedBackends` reads `TRUVAG3_AGENT_DOMAIN` and creates all storage backends (episodic memory, coordination, activity signals, digest cache, and optionally Qdrant knowledge store)
- `BuildMemoryHooks` reads `TRUVAG3_SHARED_MEMORY_*` env vars for tuning and creates all 5 pipeline hooks in the correct order (announcement → enrichment → record → extraction → cleanup)
- Both calls are visible, replaceable, and testable independently

> **Layer 3 (manual control)**: For full control over every hook option, construct hooks individually via `NewMemoryEnrichmentHook`, `NewMemoryRecordHook`, etc. See [Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) for the manual hook construction pattern.

## Customising Memory Behaviour

For domain-specific importance scoring, custom entity extraction, or different lookback windows, pass options to `BuildMemoryHooks`:

```go
hooks, activityCoord := orchestration.BuildMemoryHooks(backends.ToDeps(), agent.AI, agent.Logger,
    orchestration.WithMemoryEntityExtractor(myExtractor),     // override BOTH hooks
    orchestration.WithMemoryImportanceFunc(myScorer),          // domain-specific scoring
    orchestration.WithMemoryRetrievalWeights(myWeights),       // Phase 2 tuning
    orchestration.WithMemoryLookback(48 * time.Hour),          // longer event history
    orchestration.WithMemoryActivityFilter(myFilter),          // custom signal filtering
)
```

These are **behavioural plugs** — interfaces and functions you swap per-domain. Numeric tuning (token budgets, TTLs, model names) uses [environment variables](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#shared-memory-configuration) instead.

### How Entity Extraction Works

Entity extraction identifies which domain entities a step operated on (a pod, an order, a flight, a patient, etc.) so events can be indexed for fast retrieval (e.g., "show me everything about `pod product-catalog-api`"). The framework ships two implementations and picks one automatically.

**Auto-default selection:**
- **Record hook** (post-execution event recording):
  - When you wire an `AIClient` into `BuildMemoryHooks` (the common case): defaults to `LLMEntityExtractor`. This piggybacks on the existing `EventSummarizer` LLM call that already runs once per step — the LLM identifies entities from the step's parameters and response as a side effect of generating the step summary. **Zero additional LLM calls.** Domain-appropriate entity types (`pod`, `service`, `cluster_component`, `ticket`, `channel`, `endpoint`, ...) are produced by the LLM, not hardcoded by the framework.
  - When no `AIClient` is wired: defaults to `NoOpEntityExtractor`. Honors explicit `metadata["entity_type"]`/`metadata["entity_id"]` or multi-entity `metadata["entities"]` if your tools populate them; performs no extraction otherwise.
- **Enrichment hook** (pre-planning context lookup): always defaults to `NoOpEntityExtractor`. Pre-planning, the summarizer hasn't run yet, so `LLMEntityExtractor` would silently return nothing — making `NoOpEntityExtractor` the explicit default removes that ambiguity. Collision avoidance pre-planning is handled by `ActivityCoordinator` (see [Real-Time Coordination](#real-time-coordination-what-are-other-agents-doing-right-now) below), which has zero dependency on entity extraction.

**Per-hook override** for fine-grained control:

```go
// Opt out of LLM extraction on the record path (use explicit metadata only)
hooks, _ := orchestration.BuildMemoryHooks(backends.ToDeps(), agent.AI, agent.Logger,
    orchestration.WithBuilderRecordEntityExtractor(orchestration.NoOpEntityExtractor{}))

// Use a domain-specific extractor on the enrichment path (pre-planning entity-keyed lookup)
hooks, _ := orchestration.BuildMemoryHooks(backends.ToDeps(), agent.AI, agent.Logger,
    orchestration.WithBuilderEnrichmentEntityExtractor(myDomainExtractor))
```

**Writing a custom extractor:** implement the `orchestration.EntityExtractor` interface. The framework treats `Entity{Type, ID}` as an opaque key — domain semantics are entirely up to you.

```go
type myDomainExtractor struct{}

func (myDomainExtractor) ExtractEntities(text string, metadata map[string]interface{}) []orchestration.Entity {
    // Your domain-specific logic here.
    // Return []orchestration.Entity with whatever Type/ID makes sense for your domain.
    return []orchestration.Entity{{Type: "patient", ID: "MRN-12345"}}
}
```

**Why no regex extractor by default?** The framework previously shipped a `RegexEntityExtractor` with hardcoded K8s patterns (`pod`, `service`, `order`, `alert`). It produced massive amounts of garbage by matching English compounds in LLM-generated step instructions (`pod:non-fatal`, `pod:human-readable`, etc.). The hardcoded patterns were also a layering violation — the framework is supposed to be domain-agnostic. The LLM-based approach is both cleaner (the LLM understands semantics, not strings) and domain-agnostic (entity types are domain-meaningful, not framework-imposed).

---

## What the LLM Sees

When the LLM generates its execution plan, it sees two additional XML sections in the prompt:

### `<agent_coordination>` — Real-Time Signals

```xml
<agent_coordination>
Other agents currently active in domain 'infrastructure':
- event-driven-agent: investigating "TruvaG3HighLatency on product-catalog-api" (status: executing, started 3s ago)
</agent_coordination>
```

This tells the LLM: "someone else is already working on this." The LLM can decide to coordinate rather than duplicate.

### `<agent_memory>` — Domain Activity + Entity History

```xml
<agent_memory>
Domain activity summary:
Domain activity (last 2 hours, 106 events):
- Deployment restart: agent-with-human-approval restarted via rollout (200ms), verified healthy
- JIRA: ticket DEVOPS-49 created for the restart, comment added with verification details
- Slack: notification sent to #notifications with JIRA reference
- Monitoring: 4 Prometheus queries run by event-driven-agent for product-catalog-api metrics

Most recent events (detail):
- [2026-03-26 23:31] devops-chat-agent: Queried logs for request orch-1774567851534733422 (outcome: success)
- [2026-03-26 23:30] devops-chat-agent: Created JIRA ticket DEVOPS-52 (outcome: success)
</agent_memory>
```

The digest gives the LLM situational awareness. The recent events give it detail on what just happened. Together, they enable the LLM to make informed decisions — like "DEVOPS-52 already exists for this entity, add a comment instead of creating a new ticket."

---

## Activity Compaction: Scaling to Thousands of Events

Without compaction, every request sends all recent events (up to 200) to the LLM. That's expensive. Activity compaction solves this:

```
Request 1 (first request, cold cache):
  200 events → LLM compaction (5-10s) → 500-token digest → cached in Redis

Request 2 (cache hit, 0 new events):
  Cached digest reused → 0ms, no LLM call

Request 3 (cache hit, 5 new events since last request):
  Previous digest + 5 new events → LLM incremental update (0.5-1s) → updated digest

Request 4 (burst of 50 new events):
  Full recompaction triggered → LLM (5-10s) → new digest cached
```

**Typical steady-state**: 80% cache hits (0ms) + 15% incremental (0.5s) + 5% full (5s) = ~0.3s average.

The cache is shared across all agents in the same domain via Redis — if `event-driven-agent` compacts the digest, `devops-chat-agent` benefits from the cache too.

Configuration:

```bash
# How long to cache digests (default: 5m)
TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL=5m

# New events above this threshold trigger full recompaction (default: 20)
TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD=20

# Max tokens for the digest (default: 500)
TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS=500
```

---

## Real-Time Coordination: What Are Other Agents Doing Right Now?

Episodic events are only visible after execution completes (2-4 second delay). For concurrent requests, that's too slow. Activity coordination fills the gap with **transient Redis signals**:

```
T=0s: Alert fires → event-driven-agent starts investigating Pod X
      Immediately: signal written to Redis ("event-driven-agent: investigating Pod X")

T=1s: Same alert arrives at devops-chat-agent
      Reads signals: sees event-driven-agent is already on it
      LLM decides: "skip investigation, monitor status instead"

T=30s: event-driven-agent finishes → signal cleaned up, event recorded
```

Signals expire automatically via TTL (default: 5 minutes). No cleanup needed if an agent crashes.

Configuration:

```bash
# Signal expiry — should be longer than your longest request (default: 5m)
TRUVAG3_ACTIVITY_SIGNAL_TTL=5m

# Max signals shown to the LLM (default: 10)
TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT=10
```

---

## Knowledge Extraction: Learning From Experience

This is Phase 2 — requires Qdrant vector DB and an embedding endpoint. When configured, the `KnowledgeExtractionHook` runs after synthesis and extracts reusable knowledge fragments:

```
Request: "Pod X is OOMKilled"
  → Agent investigates, finds memory limit too low, increases it
  → Synthesis: "Increased memory limit from 256Mi to 512Mi..."

  AfterSynthesis (async):
    LLM extracts: "OOMKilled on pods with <256Mi memory is resolved by
                   doubling the memory limit. Check resource quotas first."
    → Embedded via nomic-embed-text → stored in Qdrant

Next request (days later): "Pod Y is OOMKilled"
  BeforePlanning:
    Vector search finds the knowledge fragment
    → LLM generates a 2-step plan instead of 5-step investigation
```

Knowledge extraction is async and fail-open. If Qdrant is down, the agent works normally — it just won't have learned patterns from past experience.

---

## Long-Term Knowledge Retention: The Reflection Job

`KnowledgeExtractionHook` (above) only sees **the current request** — its lessons come from one synthesis cycle at a time. There's a second, complementary path that learns from **patterns across many past requests**: the background reflection job.

### Why both?

The episodic event log (Tier 2, Redis) has a 60-day TTL by default. The activity compactor only looks back 24 hours (`MemoryEnrichmentHook` lookback default). Without something else doing periodic distillation, every event older than that window becomes invisible to the LLM and eventually expires unread. **Reflection closes that gap.**

There are two independent timelines worth keeping straight:

```
PASS CADENCE — when does the reflection job actually run?

T=0       T=24h     T=48h     T=72h     T=96h    ...
└─ pass ──┴─ pass ──┴─ pass ──┴─ pass ──┴─ pass ──→
   ↑
   TRUVAG3_REFLECTION_INTERVAL (default: 24h)
   Each pass scans the eligible event window and processes
   any entities that have crossed the AGE_THRESHOLD since
   the previous pass.

PER-EVENT ELIGIBILITY — when does ONE event become reflection-eligible?

T=0         T=24h         T=7d                       T=60d
│           │              │                          │
recorded    in digest      eligible for reflection    expires
            via compactor  (≥ AGE_THRESHOLD old)      (TTL)
            (≤24h window)
            ──────────────►
                           ◄─ window where reflection ─►
                              passes can pick it up
```

A typical journey for one event:

```
T=0:     Tool runs → episodic event recorded (60-day TTL)
T=0-24h: Activity compactor sees event in the per-request digest
T>24h:   Event still in Redis but no longer in the compactor's window
T=7d+:   Event crosses AGE_THRESHOLD — eligible for reflection
         (the next reflection pass that runs after this point picks it up)
         The reflection LLM extracts 1-3 KnowledgeFragments from this
         event + others for the same entity, embeds them, and stores
         them in Qdrant (permanent).
T=60d:   Original event expires from Redis. Fragments survive in Qdrant.
T=6mo:   New incident → semantic search retrieves the reflected fragment
         → Agent plans informed by patterns from events that no longer exist
```

### How it gets wired

The reflection job is created by `BuildReflectionJob` (Layer 1 convenience) and registered with the framework as a `Runnable`. Add this to your agent's `main.go` after creating memory backends:

```go
// Reflection job: long-term knowledge retention via background distillation.
// Returns nil silently if Phase 2 backends (Knowledge + Embedder) are unavailable.
if reflectionJob, _ := memory.BuildReflectionJob(memBackends.ToDeps(), agent.AI, agent.Logger); reflectionJob != nil {
    framework.RegisterRunnable(reflectionJob)
}
```

That's it. The framework starts the job on `Run()`, manages its lifecycle, and stops it on shutdown. The job uses a distributed Redis lock so multi-replica deployments don't double-process the same events.

### Configuration

```bash
# How often the reflection pass runs (default: 24h)
TRUVAG3_REFLECTION_INTERVAL=24h

# Only events older than this qualify (default: 168h / 7 days)
# Should be longer than the compactor's 24h window so the two layers don't compete
TRUVAG3_REFLECTION_AGE_THRESHOLD=168h

# Min events per entity to trigger reflection (default: 5)
# Lower values capture sparser entities; higher values produce more confident fragments
TRUVAG3_REFLECTION_MIN_EVENTS=5

# Optional: model used for the reflection LLM call.
# Empty (default) uses your agent's main AI client model — typically a strong model.
# Set to "fast" to route reflection to the cheap/fast alias, "smart" for the
# strong-reasoning alias, or a concrete model name. The alias is resolved
# per-provider by the chain client at call time; see ENVIRONMENT_VARIABLES_GUIDE.md
# for per-provider override env vars.
TRUVAG3_REFLECTION_MODEL=
```

### Choosing a model: cost vs. fragment quality

Reflection writes **durable knowledge that influences every future request**. A bad fragment isn't a one-off mistake — it lingers in Qdrant and surfaces as confident "prior knowledge" for as long as it remains in the collection. So unlike other background LLM calls, the reflection model choice matters.

| Use case | `TRUVAG3_REFLECTION_MODEL` | `TRUVAG3_REFLECTION_MIN_EVENTS` |
|---|---|---|
| **Default (production)** | unset | `5` |
| **High volume / cost-sensitive** | `fast` | `5` |
| **Aggressive learning** | unset | `2` |
| **Bulk indexing of historical events** | `fast` | `10` |

Aliases (`fast`, `smart`, `default`) are resolved per-provider by the chain client at call time, so the same setting works across Anthropic, OpenAI, Groq, Gemini, and the rest. The authoritative alias-to-model mappings live in the provider source files — see [ENVIRONMENT_VARIABLES_GUIDE.md — Reflection Job Configuration](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#reflection-job-configuration) for the file pointers and per-provider override env vars.

### How to verify it's working

1. **Logs:** look for `"Reflection pass starting"` and `"Reflection pass completed"` from `framework/memory`. The completion log includes `entities_discovered`, `entities_processed`, `fragments_stored`, and `embedding_tokens` — these tell you the pass actually did work.
2. **Qdrant:** `truvag3_knowledge` collection grows over time with fragments tagged `agent_domain=<your domain>`.
3. **Registry Viewer LLM Debug screen:** each reflection pass appears as a single record with `request_id` like `reflect-XXXXXXXX` and an interaction per processed entity. You can inspect the exact prompts and the fragments the LLM produced. Requires `TRUVAG3_LLM_DEBUG_ENABLED=true` and the agent's `agent.AI` to be wrapped with `ai.NewInstrumentedClient(...)`.
4. **The real test:** issue a query semantically related to a fragment in a fresh session. The `<agent_memory>` block in the planning prompt should contain the reflected fragment under "Prior knowledge relevant to this request:" — and the agent's response should reference that knowledge even though no event in the current session produced it.

### Trade-offs and limits

- **Cost grows with pass frequency, but sub-linearly.** A 1-hour `INTERVAL` runs 24× more passes per day than the 24-hour default, but each of those passes discovers fewer *newly-eligible* entities (fewer have crossed `AGE_THRESHOLD` since the last run), so total LLM spend grows less than 24×. Pick the interval based on how fast your event log actually accumulates patterns worth learning, not on raw cadence, and validate the real cost by inspecting `entities_processed`, `embedding_tokens`, and per-entity LLM call counts in the "Reflection pass completed" log line.
- **`MIN_EVENTS` is a quality knob**: very low values (2-3) extract patterns from sparse data, which is hit-or-miss. Production-grade fragments usually need ≥5 events.
- **Multi-replica safe**: the job acquires a Redis distributed lock (`truvag3:lock:reflection:<domain>`) before each pass. Other replicas log "lock held by another replica, skipping" and return.
- **Fail-open**: if the LLM call fails or Qdrant is down, the pass logs and exits cleanly. The next interval will retry. No errors propagate to user requests.
- **Append-only**: reflection never deletes episodic events. They expire naturally via the 60-day TTL. The reflection-produced fragments outlive them.

---

## Tuning for Your Use Case

### Reduce compaction latency (high event volume)

```bash
TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT=100     # Fewer events sent to LLM
TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL=10m          # Cache longer
TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD=10  # Incremental updates more often
```

### Disable LLM calls entirely (cost control)

Don't pass `WithActivityCompactor()` to the enrichment hook. The hook will use raw events (last N) instead of the LLM digest. Set the limit:

```bash
TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT=15
```

### Use a specific model for summarization

```bash
# Use a fast, cheap model for background LLM calls
TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL=fast
```

This affects event summarization and activity compaction LLM calls. Knowledge extraction uses the agent's main AI client. Does not affect planning or synthesis.

### Full configuration reference

See [ENVIRONMENT_VARIABLES_GUIDE.md — Shared Memory](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#shared-memory-configuration) and [LIMITS_CHEATSHEET.md — Shared Memory](../reference/LIMITS_CHEATSHEET.md#shared-memory) for the complete list of tunable values.

---

## Observability: Knowing What Memory Is Doing

### Jaeger Traces

Memory hooks appear as dedicated spans in your Jaeger traces:

```
▼ pipeline.hook.before_planning.activity-announcement (2ms)
    signals_discovered: 2, signals_shown: 1

▼ pipeline.hook.before_planning.memory-enrichment (2836ms)
    ▼ activity.compaction.cache_decision
        cache_hit: true, path: "incremental", duration_ms: 2705
    ▼ memory.enrichment.injected
        entities_found: 0, context_chars: 6424
    └─ orchestrator.activity_compaction_incremental (2704ms)  ← child span

▼ pipeline.hook.after_execution.memory-record (1545ms)
    events_recorded: 1
```

The `cache_decision` span event tells you which path was taken: `full` (cache miss), `cached` (0ms hit), `incremental` (few new events), or `full_recompact` (burst).

### LLM Debug Store

If you have `TRUVAG3_LLM_DEBUG_ENABLED=true`, compaction and summarization LLM calls appear in the registry viewer alongside planning and synthesis calls:

```
[1] type=activity_compaction_incremental, tokens=2308, duration=6209ms
[2] type=tiered_selection, tokens=4750, duration=1445ms
[3] type=plan_generation, tokens=11291, duration=6653ms
[4] type=event_summarization, tokens=3089, duration=3185ms
[5] type=synthesis_streaming, tokens=13705, duration=18236ms
```

---

## Troubleshooting

### Memory Context Not Appearing in Plans

Check the enrichment hook is registered:
```go
// Hooks must be passed to OrchestratorDependencies
deps := orchestration.OrchestratorDependencies{
    PipelineHooks: memoryHooks,  // Not nil!
}
```

Verify in Jaeger: look for `pipeline.hook.before_planning.memory-enrichment` span. If missing, the hook isn't registered.

### Events Not Being Recorded

Check `pipeline.hook.after_execution.memory-record` span in Jaeger. If `events_recorded: 0`, entities weren't extracted from the step results. The entity extractor uses regex patterns and structured metadata — generic operations without entity references won't produce events.

### Compaction Taking Too Long

If `activity.compaction.cache_decision` shows `path: "full"` on every request, the digest cache is not working. Check:
- Is `WithDigestCache(digestCache)` passed to the enrichment hook?
- Is Redis accessible from the agent pod?
- Is `TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL` too short?

### Activity Signals Not Showing

If `<agent_coordination>` is empty when other agents are active:
- Both agents must be in the same `TRUVAG3_AGENT_DOMAIN`
- Activity coordination hooks must be registered (announcement + cleanup)
- Check `activity.coordination.complete` span: `signals_discovered` should be > 0

### Knowledge Search Returns Nothing

Phase 2 requires all three: Qdrant running, embedding endpoint configured, and at least one prior request that generated knowledge. Check:
- `TRUVAG3_VECTOR_DB_URL` is accessible from the pod
- `TRUVAG3_EMBEDDING_BASE_URL` and `TRUVAG3_EMBEDDING_MODEL` are set
- The `KnowledgeExtractionHook` is in the hooks list (it's only added when Phase 2 backends are available)

---

## User Memory: Per-User Personalization

Everything above covers **shared agent memory** — cross-agent visibility within a domain. TruvaG3 also provides **user memory** — per-user private facts for personal assistant agents. These are completely separate concerns with separate stores, hooks, and privacy boundaries.

Think of it like a **personal assistant's notebook** vs a **team shared notebook**:
- Shared memory: "Agent A restarted pod X and created DEVOPS-42" — everyone reads it
- User memory: "Jenny is vegan, lives in Coppell TX, prefers DFW airport" — only Jenny's sessions see it

```
Without user memory:                  With user memory:

Session 1: "I'm vegan,          Session 1: "I'm vegan,
  plan a trip to Tokyo"                plan a trip to Tokyo"
  → Plans trip (no dietary filter)      → Plans trip (no dietary filter)
                                        → Extracts: "User is vegan"
                                        → Stores in Qdrant

Session 2: "Plan a trip to Paris"     Session 2: "Plan a trip to Paris"
  → Has no idea about dietary needs     → Recalls: "User is vegan"
  → Suggests steak restaurants          → Plans with vegan dining
  → User has to repeat themselves       → No repetition needed
```

> **Working Example**
>
> The travel-chat-agent has a fully working user memory implementation:
> - **Agent**: [`examples/travel-chat-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/travel-chat-agent)
> - **Memory wiring**: [`examples/travel-chat-agent/chat_agent.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/travel-chat-agent/chat_agent.go) (search for `NewUserMemoryBackend`)
> - **User identity**: [`examples/chat-ui/index.html`](https://github.com/truvaagents/truva-g3/blob/main/examples/chat-ui/index.html) (dialog + `X-User-ID` header)

### How Shared Memory and User Memory Compare

| | Shared Memory | User Memory |
|---|---|---|
| **Scope** | All agents in a domain | One user only |
| **Content** | "Agent X restarted pod Y" | "User prefers DFW airport" |
| **Visibility** | Domain-wide | Private to user |
| **Lifetime** | 24h lookback + knowledge persists | Months/years |
| **GDPR delete** | Not per-user | `Forget("user-42")` deletes everything |
| **Hooks** | `BuildMemoryHooks()` | `BuildUserMemoryHooks()` |
| **Enrichment tag** | `<agent_memory>` | `<user_profile>` |
| **Backend** | Redis (events) + Qdrant (knowledge) | Qdrant or InMemory |
| **Required** | Redis | Nothing (InMemory fallback) |

### How User Memory Works

User memory plugs into the same pipeline hook system as shared memory, but at different stages with different data:

```
User Request (with X-User-ID header)
    │
    ▼
┌──────────────────────────────────────────────────────────┐
│  UserMemoryEnrichmentHook (BeforePlanning)                │
│  Recalls facts for this user:                             │
│  - Identity (always): name, location, language            │
│  - Summaries (always): last 3 session summaries           │
│  - Preferences (by relevance): semantic search            │
│  → Injected into <user_profile> tag                       │
└──────────────────────────────────────────────────────────┘
    │
    ▼  LLM sees user profile when generating its plan
    │
  Planning → Execution → Synthesis → Response streamed
                                        │
                                        ▼
┌──────────────────────────────────────────────────────────┐
│  UserMemoryExtractionHook (AfterSynthesis)                │
│  1. LLM extracts persistent facts from conversation       │
│  2. For each candidate fact:                               │
│     - Searches for similar existing facts (Qdrant)         │
│     - LLM classifies: ADD / UPDATE / CONTRADICT / DUPLICATE│
│     - Stores new or updated facts                          │
│  3. Generates one-sentence session summary                 │
└──────────────────────────────────────────────────────────┘
```

Important: extraction runs **after** the response is streamed to the user. It doesn't add latency to the response — the user sees the answer immediately, and facts are stored in the background.

### User Memory Environment Setup

Add these to your `.env` file (you may already have them for shared memory):

```bash
# Vector DB for user fact storage (Qdrant)
TRUVAG3_VECTOR_DB_URL=qdrant.truvag3-examples:6334

# Embedding endpoint (for semantic search when recalling facts)
TRUVAG3_EMBEDDING_BASE_URL=http://host.docker.internal:11434/v1
TRUVAG3_EMBEDDING_MODEL=nomic-embed-text
```

To use a hosted provider (OpenAI, Mistral, Voyage, Jina, Together, Cohere) instead of local Ollama, see **Embedding provider options** in Step 1 for the full list of supported base URLs and model IDs.

Without a vector DB, user memory falls back to `InMemoryUserMemory` — functional for development but facts are lost on restart.

Optional tuning (all have sensible defaults):

```bash
# Max facts injected into the planning prompt (default: 15)
TRUVAG3_USER_MEMORY_MAX_FACTS_IN_PROMPT=15

# Model for fact extraction + session summary LLM calls (default: agent's model)
# Use a fast/cheap model — these are high-volume, low-complexity calls
TRUVAG3_USER_MEMORY_EXTRACTION_MODEL=fast

# Cosine similarity threshold for triggering LLM reconciliation (default: 0.75)
# Below this, new facts are added without checking for contradictions
TRUVAG3_USER_MEMORY_RECONCILIATION_THRESHOLD=0.75
```

See [Environment Variables Guide — User Memory](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#user-memory-configuration) for all 10 env vars.

### Adding User Memory to Your Agent

Add these lines to your `main.go`, after AI client creation and before orchestrator initialization:

```go
import (
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/memory"
    "github.com/truvaagents/truva-g3/orchestration"
)

// Create embedding client (memory module can't import ai — passed via option)
embedder, _ := ai.NewEmbeddingClient(ai.WithEmbeddingLogger(agent.Logger))

// Step 1: memory module creates backend
userMemBackend, _ := memory.NewUserMemoryBackend(agent.Logger,
    memory.WithUserMemoryNamespace("travel"),       // scopes facts by agent type
    memory.WithUserMemoryEmbeddingClient(embedder), // enables vector search
)
if userMemBackend != nil {
    defer userMemBackend.Close()
}

// Step 2: orchestration module creates hooks
userHooks, userHooksCloser := orchestration.BuildUserMemoryHooks(
    userMemBackend.ToDeps(), agent.AI, agent.Logger,
)
defer userHooksCloser.Close()
```

`BuildUserMemoryHooks(...)` returns both the hooks and an `io.Closer`. The Layer 1 preset runs extraction asynchronously by default so chat responses are not delayed; use `orchestration.WithSynchronousExtraction()` when tests or downstream logic need extraction to complete before continuing.

Then pass them to your orchestrator alongside shared memory hooks:

```go
deps := orchestration.OrchestratorDependencies{
    // Shared memory hooks + user memory hooks compose together
    PipelineHooks: append(sharedMemoryHooks, userHooks...),
}
```

**What's a namespace?** It scopes which facts an agent reads and writes. A travel agent with namespace `"travel"` stores "prefers DFW airport" — a DevOps agent with namespace `"devops"` won't see it. Both always query the `"universal"` namespace for cross-agent facts (name, language, timezone).

**Where does user_id come from?** The hooks read `pctx.Metadata["user_id"]`. Your HTTP handler sets it from the request — typically from an `X-User-ID` header, a JWT claim, or a session store. If `user_id` is missing, both hooks skip silently (event-driven agents, cron agents — no user context, no user memory).

### What the LLM Sees (User Profile)

When the enrichment hook runs, the LLM receives a `<user_profile>` tag in its planning prompt:

```xml
<user_profile>
Identity:
- User lives in Coppell, TX (explicit, high confidence)

Constraint:
- User and family are vegan, no eggs or gelatin (explicit, high confidence)

Preference:
- User prefers DFW airport for flights (explicit, high confidence)

Relationship:
- Family of three: user, wife, and 12-year-old son (explicit, high confidence)

Summary:
- Planned a week-long vegan-friendly family trip to Zurich (derived, recorded 3 days ago)
- Asked about Switzerland weather, currency, and travel tips (derived, recorded 3 days ago)
</user_profile>

<context_precedence>
When <user_profile>, <conversation_history>, and <user_request> disagree about a subject — destination, dates, party, budget, or any named entity — trust the live turn: the current <user_request> first, then the most recent <conversation_history> turn. Treat <user_profile> "Context" entries as hints that may be stale; recency labels reflect when they were last recorded.
</context_precedence>

<user_request>
Plan a summer vacation in Europe
</user_request>
```

Durable facts (identity, preference, constraint, relationship) render with a confidence band. Transient facts (`category: "context"`) and `summary` facts render with a recency label instead — their current truth depends on time, not on how strongly the user stated them once. The `<context_precedence>` rule tells the planner to trust the live turn when a stored "Context" fact disagrees with the new request.

The LLM now plans around the user's dietary constraints, family size, and airport preference — without the user repeating any of it.

This is separate from `<agent_memory>` (shared operational memory). Both can be present in the same prompt — different sources, different privacy boundaries.

### Fact Reconciliation: How Contradictions Are Handled

When the extraction hook finds a new candidate fact, it doesn't blindly store it. It searches for similar existing facts and classifies the relationship:

| Operation | When | What Happens |
|-----------|------|-------------|
| **ADD** | No similar facts exist | New fact stored as-is |
| **DUPLICATE** | Same information already stored | Skip — update `last_used_at` timestamp |
| **UPDATE** | New fact refines existing | "Likes cricket" → "Likes cricket with friends on weekends" |
| **CONTRADICT** | New fact supersedes existing | "Vegan" → "No longer vegan" — old fact deleted, new stored |

This is LLM-driven — pure embedding similarity can't distinguish "prefers window seats" from "prefers aisle seats" (both are about seating preferences, high similarity, but contradictory). The LLM classifies the actual semantic relationship.

**Cost optimization:** If no similar facts exist (cosine similarity < 0.75), reconciliation skips the LLM call entirely and does a direct ADD. For new users, most facts are new — zero reconciliation LLM cost.

### Customising User Memory Behaviour

Like shared memory, user memory uses the **configuration split**: numbers via env vars, behaviour via options.

```go
// Custom fact extractor (e.g., domain-specific extraction rules)
userHooks, userHooksCloser := orchestration.BuildUserMemoryHooks(deps, aiClient, logger,
    orchestration.WithUserFactExtractor(myCustomExtractor),
    orchestration.WithUserFactReconciler(myCustomReconciler),
    orchestration.WithUserMemoryRetrievalWeights(core.RetrievalWeights{
        Recency:    0.10,  // less weight on recency
        Relevance:  0.60,  // more weight on semantic match
        Importance: 0.30,  // unchanged
    }),
    orchestration.WithSynchronousExtraction(), // optional: opt out of async preset
)
defer userHooksCloser.Close()
```

Or swap the entire backend — use PostgreSQL with pgvector instead of Qdrant:

```go
pgUserMem := mycompany.NewPgUserMemory(pgPool, logger)
deps := &core.UserMemoryDeps{
    UserMemory: pgUserMem,
    Embedder:   embedder,
    Namespace:  "travel",
}
userHooks, userHooksCloser := orchestration.BuildUserMemoryHooks(deps, aiClient, logger)
defer userHooksCloser.Close()
```

Same hooks, same extraction, same reconciliation — different storage backend. The `UserMemory` interface is the boundary.

### GDPR: Right to Erasure

User memory has first-class deletion support:

```go
// Delete all facts for a user (GDPR Article 17)
err := userMemory.Forget(ctx, "user-42")

// Delete facts in a specific namespace
err := userMemAdmin.ForgetNamespace(ctx, "user-42", "travel")

// Delete a single fact
err := userMemAdmin.ForgetFact(ctx, "user-42", "fact-id-123")
```

`Forget()` deletes all vectors and payloads for the user from the vector DB. After compaction, no residual data remains in indexes.

### User Memory Observability

**Jaeger traces** — both hooks emit span events under auto-created parent spans:

```
pipeline.hook.before_planning.user-memory-enrichment
  ├─ user_memory.recall.identity (user_id, namespace)
  └─ user_memory.enrichment.injected (facts_count, profile_chars)

pipeline.hook.after_synthesis.user-memory-extraction
  ├─ user_memory.extraction.llm_request (user_id)
  ├─ user_memory.extraction.complete (candidates, stored)
  ├─ user_memory.summary.llm_request (user_id)
  └─ user_memory.summary.stored (user_id)
```

**Telemetry counters:**

| Metric | What It Tracks |
|--------|---------------|
| `user_memory.reconciliation.operation` | Count per operation type (ADD/UPDATE/CONTRADICT/DUPLICATE) |
| `user_memory.reconciliation.batch{outcome}` | Batched reconciliation attempts, labelled `success` (one LLM call classified all candidates) or `fallback` (batched call failed, dropped to per-candidate path). A rising fallback rate signals a model regression on structured-output compliance. |
| `user_memory.summary.stored` | Session summaries generated |

### User Memory Troubleshooting

**Facts Not Being Recalled**

Check `pipeline.hook.before_planning.user-memory-enrichment` span in Jaeger. If `facts_count: 0`:
- Is `user_id` set in `pctx.Metadata`? (Check your HTTP handler — must set `"user_id"` in the metadata map passed to `ProcessRequest`)
- Is `TRUVAG3_VECTOR_DB_URL` accessible from the pod?
- Does the Qdrant collection `truvag3_user_memory` exist? (Auto-created on first `Remember()` call)
- Are facts stored for this user ID? (Check Qdrant directly via REST API)

**Extraction Not Running**

Check `pipeline.hook.after_synthesis.user-memory-extraction` span. If missing:
- Is `BuildUserMemoryHooks` registered in `PipelineHooks`?
- Is `user_id` present in metadata? (Hooks skip silently without it)

**Too Many Reconciliation LLM Calls**

If every fact triggers a reconciliation LLM call, the similarity threshold may be too low. Increase `TRUVAG3_USER_MEMORY_RECONCILIATION_THRESHOLD` (default 0.75). Higher values mean fewer false "similar" matches, fewer LLM calls, but slightly higher chance of storing near-duplicates.

**Facts Not Appearing in Response**

The `<user_profile>` tag must be consumed by the prompt builder. Check the debug store (`/api/llm-debug/{request_id}`) — look for `<user_profile>` in the `plan_generation`, `continuation_plan_generation`, and `synthesis_streaming` prompts. If missing from any, the prompt builder isn't reading `EnrichmentUserProfile`.

---

## See Also

**Shared Memory:**
- **[Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md)** — Building custom pipeline hooks
- **[Environment Variables Guide — Shared Memory](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#shared-memory-configuration)** — All configurable env vars
- **[Limits Cheatsheet — Shared Memory](../reference/LIMITS_CHEATSHEET.md#shared-memory)** — Quick reference for all limits
- **[Distributed Tracing Guide — LLM Telemetry](../observability/DISTRIBUTED_TRACING_GUIDE.md#15-llm-telemetry-in-orchestration-automatic)** — Span events emitted by memory hooks
- **[API Reference — Shared Memory Interfaces](../reference/API_REFERENCE.md#shared-memory-interfaces)** — Full interface signatures
- **[Memory Module Architecture](https://github.com/truvaagents/truva-g3/blob/main/memory/ARCHITECTURE.md)** — Deep dive into storage backends, sharing topology, and design decisions
- **[`examples/devops-chat-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/devops-chat-agent)** — Full working implementation with all phases
- **[`examples/event-driven-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/event-driven-agent)** — Event-driven agent with the same memory wiring

**User Memory:**
- **[API Reference — User Memory Interfaces](../reference/API_REFERENCE.md#user-memory-interfaces)** — Interface signatures and convenience constructors
- **[Environment Variables Guide — User Memory](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#user-memory-configuration)** — All configurable env vars
- **[Limits Cheatsheet — User Memory](../reference/LIMITS_CHEATSHEET.md#user-memory)** — Quick reference for all limits
- **[`examples/travel-chat-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/travel-chat-agent)** — Full working implementation with user memory
