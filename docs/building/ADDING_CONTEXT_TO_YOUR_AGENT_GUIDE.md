# Adding Context to Your Agent

How to give your TruvaG3 agent conversation memory, RAG knowledge, semantic caching, and response filtering — without modifying framework internals.

## Table of Contents

1. [Why This Guide Exists](#1-why-this-guide-exists)
2. [What Are Pipeline Hooks?](#2-what-are-pipeline-hooks)
3. [Architecture Overview](#3-architecture-overview)
4. [Scenario: Adding RAG Context from a Vector Database](#4-scenario-adding-rag-context-from-a-vector-database)
5. [Scenario: Semantic Caching with Redis](#5-scenario-semantic-caching-with-redis)
6. [Scenario: Conversation History for Chat Agents](#6-scenario-conversation-history-for-chat-agents)
7. [Scenario: Response Guardrails and Content Filtering](#7-scenario-response-guardrails-and-content-filtering)
8. [Scenario: Generating Embeddings for Semantic Search](#8-scenario-generating-embeddings-for-semantic-search)
9. [Scenario: Logging Execution Results to an Analytics Pipeline](#9-scenario-logging-execution-results-to-an-analytics-pipeline)
10. [Wiring Hooks into the Orchestrator](#10-wiring-hooks-into-the-orchestrator)
11. [Mock and NoOp Implementations for Testing](#11-mock-and-noop-implementations-for-testing)
12. [Interface Reference](#12-interface-reference)
13. [Design Decisions and Trade-offs](#13-design-decisions-and-trade-offs)
14. [Further Reading](#14-further-reading)

---

## 1. Why This Guide Exists

Out of the box, a TruvaG3 agent is stateless. It receives a request, plans, executes tools, and synthesizes a response — but it has no memory of past conversations, no access to your domain knowledge base, and no way to cache or filter responses.

Without context engineering:
- Every request starts from zero — the agent can't recall what the user said two messages ago
- Your curated knowledge (internal docs, FAQs, product catalogs) never reaches the LLM prompt
- Identical queries hit the LLM every time, wasting tokens and adding latency
- You can't enforce content policies or compliance rules on generated responses

This guide shows you how to solve each of these problems using **pipeline hooks** and **memory interfaces** — TruvaG3's extension points for injecting context at every stage of the orchestration pipeline. Each section is a self-contained scenario with real-world code you can adapt to your stack.

---

## 2. What Are Pipeline Hooks?

Pipeline hooks are **per-stage middleware** that run at defined points in the orchestration pipeline. They let you inject data, short-circuit processing, mutate plans, and post-process responses — all without forking or modifying the framework.

Every hook implements the base `core.PipelineHook` interface (just a `Name()` method) and one or more stage-specific interfaces:

| Hook Stage | When It Runs | Can Mutate? | Can Short-Circuit? |
|---|---|---|---|
| `BeforePlanningHook` | Before the planning phase begins | Enrichments | Yes — skip entire pipeline |
| `AfterPlanningHook` | After the LLM generates an execution plan | Plan | No |
| `AfterExecutionHook` | After all tools finish executing | No (observe-only) | No |
| `AfterSynthesisHook` | After the LLM synthesizes the final response | Response text | No |

Hooks are **resilient by design**: if a hook returns an error, the orchestrator logs a warning and continues. A failing hook never aborts the pipeline.

---

## 3. Architecture Overview

```
Request
  │
  ▼
┌─────────────────────────────┐
│  BeforePlanningHook(s)      │  ← Inject RAG context, conversation history
│  Can short-circuit here     │  ← Return cached response (skip everything)
└─────────────┬───────────────┘
              │ PipelineContext.Enrichments → ctx via WithPipelineEnrichments()
              ▼
┌─────────────────────────────┐
│  Planning (LLM call)        │  ← PromptInput.Metadata receives enrichments
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│  AfterPlanningHook(s)       │  ← Mutate/validate the plan
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│  Execution (tool calls)     │
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│  AfterExecutionHook(s)      │  ← Log results, update metrics
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│  Synthesis (LLM call)       │
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│  AfterSynthesisHook(s)      │  ← Guardrails, PII redaction, memory storage
└─────────────┬───────────────┘
              ▼
           Response
```

**Key concept: `PipelineContext`** — A struct that flows through all hooks in a request. It carries:
- `Request` — the original user query
- `Metadata` — user-supplied metadata (session ID, user ID, etc.)
- `Enrichments` — a map that hooks populate with injected data (RAG context, history)

Enrichments set by `BeforePlanningHook` are stored in the Go context via `core.WithPipelineEnrichments()` and automatically available to the prompt builder through `PromptInput.Metadata`.

---

## 4. Scenario: Adding RAG Context from a Vector Database

**Problem**: Your agent needs to answer questions using internal company documentation stored in Qdrant (or Pinecone, Weaviate, Milvus, etc.). The LLM should see relevant document snippets alongside the user's question.

**Solution**: Implement a `BeforePlanningHook` that queries the vector database and injects results into enrichments.

```go
package hooks

import (
    "context"
    "fmt"
    "strings"

    "github.com/truvaagents/truva-g3/core"
    qdrant "github.com/qdrant/go-client/qdrant"
)

// RAGHook retrieves relevant documents from Qdrant before planning.
type RAGHook struct {
    qdrantClient qdrant.PointsClient
    embedder     core.EmbeddingClient // TruvaG3's embedding interface
    collection   string
    topK         int
}

func NewRAGHook(client qdrant.PointsClient, embedder core.EmbeddingClient, collection string) *RAGHook {
    return &RAGHook{
        qdrantClient: client,
        embedder:     embedder,
        collection:   collection,
        topK:         5,
    }
}

func (h *RAGHook) Name() string { return "rag_qdrant" }

func (h *RAGHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
    // 1. Generate embedding for the user's query
    embResp, err := h.embedder.GenerateEmbeddings(ctx, []string{pctx.Request}, &core.EmbeddingOptions{
        Model: "text-embedding-3-small",
    })
    if err != nil {
        return nil, fmt.Errorf("embedding generation failed: %w", err)
    }

    // 2. Search Qdrant for similar documents
    searchResult, err := h.qdrantClient.Search(ctx, &qdrant.SearchPoints{
        CollectionName: h.collection,
        Vector:         embResp.Embeddings[0],
        Limit:          uint64(h.topK),
        WithPayload:    qdrant.NewWithPayload(true),
    })
    if err != nil {
        return nil, fmt.Errorf("qdrant search failed: %w", err)
    }

    // 3. Format retrieved documents into enrichments
    var docs []string
    for _, point := range searchResult {
        if content, ok := point.Payload["content"]; ok {
            docs = append(docs, content.GetStringValue())
        }
    }
    pctx.Enrichments[core.EnrichmentRAGContext] = strings.Join(docs, "\n---\n")

    // Never short-circuit — we want planning to proceed with the enriched context
    return nil, nil
}
```

**How the enrichments reach the LLM**: The orchestrator calls `core.WithPipelineEnrichments(ctx, pctx.Enrichments)` after your hook returns. When the prompt builder constructs the planning prompt, it reads `PromptInput.Metadata` — which now contains your RAG documents under the `rag_context` key. A custom `PromptBuilder` can format this into the prompt however you like.

---

## 5. Scenario: Semantic Caching with Redis

**Problem**: Many users ask similar questions. You want to skip the entire LLM pipeline when a semantically similar query was already answered, saving cost and latency.

**Solution**: Implement a `BeforePlanningHook` that checks a Redis-backed semantic cache. If a match is found, return a `PipelineShortCircuit` to skip planning, execution, and synthesis entirely.

```go
package hooks

import (
    "context"
    "fmt"

    "github.com/truvaagents/truva-g3/core"
    "github.com/redis/go-redis/v9"
)

// SemanticCacheHook checks Redis for cached responses to similar queries.
// Uses embedding similarity to match queries, not exact string matching.
type SemanticCacheHook struct {
    redis         *redis.Client
    embedder      core.EmbeddingClient
    threshold     float64 // Similarity threshold (e.g. 0.95)
    cachePrefix   string
}

func NewSemanticCacheHook(redis *redis.Client, embedder core.EmbeddingClient) *SemanticCacheHook {
    return &SemanticCacheHook{
        redis:       redis,
        embedder:    embedder,
        threshold:   0.95,
        cachePrefix: "truvag3:cache:",
    }
}

func (h *SemanticCacheHook) Name() string { return "semantic_cache" }

func (h *SemanticCacheHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
    // 1. Generate embedding for the incoming query
    embResp, err := h.embedder.GenerateEmbeddings(ctx, []string{pctx.Request}, nil)
    if err != nil {
        return nil, err // Error is logged and skipped — pipeline continues normally
    }

    // 2. Search Redis for a similar cached query (using RediSearch FT.SEARCH with vector similarity)
    // This is a simplified example — production code would use Redis Vector Similarity Search (VSS)
    cached, err := h.redis.Get(ctx, h.cachePrefix+hashVector(embResp.Embeddings[0])).Result()
    if err == redis.Nil {
        return nil, nil // Cache miss — continue with normal pipeline
    }
    if err != nil {
        return nil, err
    }

    // 3. Cache hit — short-circuit the entire pipeline
    return &core.PipelineShortCircuit{
        Response: cached,
        Source:   "semantic_cache",
    }, nil
}
```

When a `PipelineShortCircuit` is returned:
- `ProcessRequest` immediately builds a response without calling the LLM
- `ProcessRequestStreaming` chunks the cached response and delivers it via the stream callback
- The orchestrator records a `pipeline.hook.short_circuit` telemetry metric with the hook name and source

---

## 6. Scenario: Conversation History for Chat Agents

**Problem**: Your chat agent needs multi-turn context. Each request should include prior turns so the LLM understands the dialogue flow.

**Solution**: Use `ConversationMemory` to store turns, pass raw turns in request metadata on the primary chat-agent path, and use `ConversationHistoryHook` only as an adapter when your integration can read from memory but cannot attach raw turns directly.

### 6.1 Storing Turns with ConversationMemory

```go
// In your chat handler — store each turn and pass conversation turns to
// orchestration as metadata on the next request.
func (h *ChatHandler) HandleMessage(ctx context.Context, sessionID string, userMessage string) (string, error) {
    // Store the user's message
    err := h.conversationMemory.AddTurn(ctx, sessionID, core.ConversationTurn{
        Role:      "user",
        Content:   userMessage,
        Timestamp: time.Now(),
    })
    if err != nil {
        h.logger.Warn("Failed to store user turn", map[string]interface{}{"error": err.Error()})
    }

    metadata := map[string]interface{}{}
    if fullMemory, ok := h.conversationMemory.(core.FullConversationMemory); ok {
        turns, histErr := fullMemory.GetFullHistory(ctx, sessionID)
        if histErr != nil {
            h.logger.Warn("Failed to load conversation history", map[string]interface{}{"error": histErr.Error()})
        } else {
            metadata = addConversationHistoryMetadata(metadata, sessionID, turns)
        }
    }

    // Process via orchestrator. The shared conversation-history preparer
    // converts raw turns into the <conversation_history> enrichment.
    response, err := h.orchestrator.ProcessRequest(ctx, userMessage, metadata)
    if err != nil {
        return "", err
    }

    // Store the assistant's response
    _ = h.conversationMemory.AddTurn(ctx, sessionID, core.ConversationTurn{
        Role:      "assistant",
        Content:   response.Response,
        Timestamp: time.Now(),
    })

    return response.Response, nil
}
```

### 6.2 Passing Conversation History to Orchestration

```go
package chatagent

import (
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration"
)

// Preferred chat-agent path: attach raw turns to request metadata.
// The shared conversation-history preparer converts them into the
// <conversation_history> enrichment before planning and reuses the
// prepared value across later orchestration phases.
func addConversationHistoryMetadata(
    metadata map[string]interface{},
    sessionID string,
    turns []core.ConversationTurn,
) map[string]interface{} {
    if metadata == nil {
        metadata = make(map[string]interface{})
    }
    if len(turns) == 0 {
        return metadata
    }
    metadata[orchestration.MetadataConversationTurns] = turns
    metadata[orchestration.MetadataConversationSessionKey] = sessionID
    return metadata
}
```

If your integration cannot pass raw turns in metadata and only has a memory backend, `ConversationHistoryHook` remains available as a thin adapter:

```go
processor, _ := orchestration.BuildConversationHistoryProcessor(config)
hook, _ := orchestration.NewConversationHistoryHook(
    convMemory,
    sessionID,
    orchestration.WithConversationHistoryPreparer(processor),
)
```

For the full metadata-first path, Tier 2 recursive compaction, and Layer 3 override patterns, see [Conversation History Guide](../memory-and-chat/CONVERSATION_HISTORY_GUIDE.md).

### 6.3 Implementing ConversationMemory with Redis

```go
package memory

import (
    "context"
    "encoding/json"
    "fmt"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/redis/go-redis/v9"
)

// RedisConversationMemory stores conversation turns in Redis Lists.
// Each session gets its own list key with automatic TTL expiration.
type RedisConversationMemory struct {
    client     *redis.Client
    keyPrefix  string
    sessionTTL time.Duration // e.g. 24 hours — auto-expire inactive sessions
}

func NewRedisConversationMemory(client *redis.Client) *RedisConversationMemory {
    return &RedisConversationMemory{
        client:     client,
        keyPrefix:  "truvag3:conv:",
        sessionTTL: 24 * time.Hour,
    }
}

func (m *RedisConversationMemory) AddTurn(ctx context.Context, sessionID string, turn core.ConversationTurn) error {
    data, err := json.Marshal(turn)
    if err != nil {
        return fmt.Errorf("marshal turn: %w", err)
    }
    key := m.keyPrefix + sessionID
    pipe := m.client.Pipeline()
    pipe.RPush(ctx, key, data)
    pipe.Expire(ctx, key, m.sessionTTL) // Refresh TTL on each new turn
    _, err = pipe.Exec(ctx)
    return err
}

func (m *RedisConversationMemory) GetHistory(ctx context.Context, sessionID string, maxTurns int) ([]core.ConversationTurn, error) {
    key := m.keyPrefix + sessionID

    // Get the last N turns (LRANGE with negative indices)
    results, err := m.client.LRange(ctx, key, int64(-maxTurns), -1).Result()
    if err != nil {
        return nil, err
    }

    turns := make([]core.ConversationTurn, 0, len(results))
    for _, raw := range results {
        var turn core.ConversationTurn
        if err := json.Unmarshal([]byte(raw), &turn); err != nil {
            continue // Skip malformed entries
        }
        turns = append(turns, turn)
    }
    return turns, nil
}

func (m *RedisConversationMemory) Clear(ctx context.Context, sessionID string) error {
    return m.client.Del(ctx, m.keyPrefix+sessionID).Err()
}
```

---

## 7. Scenario: Response Guardrails and Content Filtering

**Problem**: Before returning a response to the user, you need to check it for PII (personally identifiable information), toxic content, or policy violations. If the response fails the check, you want to replace it with a safe fallback.

**Solution**: Implement an `AfterSynthesisHook` that inspects and optionally mutates the synthesized response.

```go
package hooks

import (
    "context"
    "strings"

    "github.com/truvaagents/truva-g3/core"
)

// GuardrailsHook checks the synthesized response against content policies
// and redacts PII patterns before returning to the user.
type GuardrailsHook struct {
    piiPatterns []string // Regex patterns or keyword lists
    blocklist   []string // Blocked phrases
}

func NewGuardrailsHook() *GuardrailsHook {
    return &GuardrailsHook{
        blocklist: []string{"internal-only", "confidential"},
    }
}

func (h *GuardrailsHook) Name() string { return "guardrails" }

func (h *GuardrailsHook) AfterSynthesis(ctx context.Context, pctx *core.PipelineContext, response string) (string, error) {
    // Check for blocked content
    lower := strings.ToLower(response)
    for _, blocked := range h.blocklist {
        if strings.Contains(lower, blocked) {
            // Replace the entire response with a safe fallback
            return "I'm sorry, but I can't provide that information. " +
                "Please contact support for assistance.", nil
        }
    }

    // Redact email addresses (simplified example — use regex in production)
    response = redactEmails(response)

    return response, nil
}
```

The `AfterSynthesisHook` runs **after** the LLM has generated the final response. Multiple hooks run in registration order — each receives the output of the previous hook. This makes it easy to chain: first redact PII, then check content policy, then apply formatting.

> **Streaming note**: In `ProcessRequestStreaming`, tokens have already been streamed to the client by the time `AfterSynthesisHook` runs. The hook operates on the full accumulated response. Use it for post-processing tasks like memory storage, audit logging, or updating the `Response` field in the final `StreamingOrchestratorResponse`.

---

## 8. Scenario: Generating Embeddings for Semantic Search

**Problem**: Multiple hooks need vector embeddings — the RAG hook needs query embeddings, the semantic cache needs them, and long-term memory storage needs them. You want a single, provider-agnostic embedding interface.

**Solution**: Implement the `EmbeddingClient` interface. Hooks receive it as a dependency — they never know (or care) which provider generates the vectors.

### 8.1 OpenAI Embeddings Implementation

```go
package embeddings

import (
    "context"
    "fmt"

    "github.com/truvaagents/truva-g3/core"
    openai "github.com/sashabaranov/go-openai"
)

// OpenAIEmbeddingClient implements core.EmbeddingClient using OpenAI's API.
type OpenAIEmbeddingClient struct {
    client *openai.Client
}

func NewOpenAIEmbeddingClient(apiKey string) *OpenAIEmbeddingClient {
    return &OpenAIEmbeddingClient{
        client: openai.NewClient(apiKey),
    }
}

func (c *OpenAIEmbeddingClient) GenerateEmbeddings(
    ctx context.Context,
    texts []string,
    options *core.EmbeddingOptions,
) (*core.EmbeddingResponse, error) {
    model := openai.SmallEmbedding3 // text-embedding-3-small
    if options != nil && options.Model != "" {
        model = openai.EmbeddingModel(options.Model)
    }

    req := openai.EmbeddingRequest{
        Input: texts,
        Model: model,
    }
    if options != nil && options.Dimensions > 0 {
        req.Dimensions = options.Dimensions
    }

    resp, err := c.client.CreateEmbeddings(ctx, req)
    if err != nil {
        return nil, fmt.Errorf("openai embeddings: %w", err)
    }

    embeddings := make([][]float32, len(resp.Data))
    for i, item := range resp.Data {
        embeddings[i] = item.Embedding
    }

    return &core.EmbeddingResponse{
        Embeddings: embeddings,
        Model:      string(resp.Model),
        Provider:   "openai",
        Usage: core.TokenUsage{
            PromptTokens: resp.Usage.PromptTokens,
            TotalTokens:  resp.Usage.TotalTokens,
        },
    }, nil
}
```

### 8.2 Sharing the EmbeddingClient Across Hooks

```go
// In your application setup:
embedder := embeddings.NewOpenAIEmbeddingClient(os.Getenv("OPENAI_API_KEY"))

// Both hooks share the same embedder — no provider coupling
ragHook := hooks.NewRAGHook(qdrantClient, embedder, "company_docs")
cacheHook := hooks.NewSemanticCacheHook(redisClient, embedder)
```

> **Batch limits**: OpenAI's `text-embedding-3-small` supports up to 2048 texts per request. If your hook sends more, implement batching in your `EmbeddingClient`. The interface is batch-by-default (`[]string` input) — callers pass one or many texts per call.

---

## 9. Scenario: Logging Execution Results to an Analytics Pipeline

**Problem**: After tools execute, you want to log the results to Kafka (or Datadog, Splunk, BigQuery) for analytics — which tools were called, latency, success rates — without blocking the response.

**Solution**: Implement an `AfterExecutionHook`. It receives the combined results from all executed tools.

```go
package hooks

import (
    "context"
    "encoding/json"

    "github.com/truvaagents/truva-g3/core"
    "github.com/segmentio/kafka-go"
)

// AnalyticsHook publishes execution results to Kafka for downstream analytics.
type AnalyticsHook struct {
    writer *kafka.Writer
    topic  string
}

func NewAnalyticsHook(brokers []string, topic string) *AnalyticsHook {
    return &AnalyticsHook{
        writer: &kafka.Writer{
            Addr:  kafka.TCP(brokers...),
            Topic: topic,
        },
        topic: topic,
    }
}

func (h *AnalyticsHook) Name() string { return "analytics_kafka" }

func (h *AnalyticsHook) AfterExecution(ctx context.Context, pctx *core.PipelineContext, results interface{}) error {
    event := map[string]interface{}{
        "request":    pctx.Request,
        "results":    results,
        "session_id": pctx.Metadata["session_id"],
    }

    data, err := json.Marshal(event)
    if err != nil {
        return err // Logged and skipped — response delivery is not affected
    }

    // Fire-and-forget write to Kafka
    return h.writer.WriteMessages(ctx, kafka.Message{
        Value: data,
    })
}
```

The `AfterExecutionHook` is **observe-only** — it cannot mutate the results. This is intentional: execution results feed into the synthesis LLM call, and allowing mutation here could break the synthesis prompt. If you need to transform results, use a custom `ResultProcessor` instead (see [API Reference](../reference/API_REFERENCE.md)).

---

## 10. Wiring Hooks into the Orchestrator

Hooks are registered via `OrchestratorDependencies.PipelineHooks` when creating the orchestrator through the factory. They run in the order you register them.

```go
package main

import (
    "github.com/truvaagents/truva-g3/orchestration"
    "github.com/truvaagents/truva-g3/core"
)

func setupOrchestrator() (*orchestration.AIOrchestrator, error) {
    // Create your hook implementations
    embedder := embeddings.NewOpenAIEmbeddingClient(os.Getenv("OPENAI_API_KEY"))
    convMemory := memory.NewRedisConversationMemory(redisClient)

    ragHook := hooks.NewRAGHook(qdrantClient, embedder, "docs")
    cacheHook := hooks.NewSemanticCacheHook(redisClient, embedder)
    guardrails := hooks.NewGuardrailsHook()
    analytics := hooks.NewAnalyticsHook(kafkaBrokers, "orchestrator-events")

    config := orchestration.DefaultConfig()
    orch, err := orchestration.CreateOrchestrator(config, orchestration.OrchestratorDependencies{
        Discovery:                  discovery,
        AIClient:                   aiClient,
        Logger:                     logger,
        Telemetry:                  telemetry,
        PipelineHooks: []core.PipelineHook{
            cacheHook,    // BeforePlanningHook — short-circuits on cache hit
            ragHook,      // BeforePlanningHook — injects RAG context
            analytics,    // AfterExecutionHook — logs to Kafka
            guardrails,   // AfterSynthesisHook — filters response
        },
    })

    return orch, err
}
```

Most chat agents should pass raw turns via `orchestration.MetadataConversationTurns` and let the shared preparer handle history injection. Add `ConversationHistoryHook` only when you have a memory-backed integration that cannot supply raw turns in metadata.

If you want Tier 2 recursive compaction, use the convenience helper and override only the pieces you care about:

```go
config := orchestration.DefaultConfig()
preparer, err := orchestration.BuildCompactionEnabledConversationHistoryPreparer(
    config,
    aiClient,
    orchestration.WithConversationSummaryCache(myCache),   // optional
    orchestration.WithConversationCompactor(myCompactor),  // optional
)
if err != nil {
    return nil, err
}

orch, err := orchestration.CreateOrchestrator(config, orchestration.OrchestratorDependencies{
    Discovery:                   discovery,
    AIClient:                    aiClient,
    Logger:                      logger,
    Telemetry:                   telemetry,
    ConversationHistoryPreparer: preparer,
    PipelineHooks: []core.PipelineHook{cacheHook, ragHook, analytics, guardrails},
})
```

That gives you the richer Layer 2 path without forcing you into full direct construction.

**Hook ordering guidelines:**
- Hooks that may **short-circuit** should be registered first (cache hooks). Once a short-circuit is returned, no further hooks run.
- **Enrichment** hooks should run in dependency order. If you still use `ConversationHistoryHook` and your RAG hook benefits from conversation context, register the history hook before the RAG hook.
- **AfterSynthesis** hooks run in order — the output of one feeds into the next. Register PII redaction before content policy checks if you want the policy check to see the redacted version.

---

## 11. Mock and NoOp Implementations for Testing

The framework ships test helpers for every new interface so you can test hooks without real infrastructure.

### 11.1 NoOp Implementations (Zero-Config Defaults)

When no memory backend is configured, use NoOps — they satisfy the interface and do nothing:

```go
// These are safe to use as zero-value defaults in production.
// The orchestrator runs unmodified — no hooks, no memory overhead.
var _ core.ConversationMemory = (*core.NoOpConversationMemory)(nil)
var _ core.SemanticMemory     = (*core.NoOpSemanticMemory)(nil)
var _ core.EmbeddingClient    = (*core.NoOpEmbeddingClient)(nil)
```

### 11.2 Mock Implementations (Test Assertions)

Each mock has two features per method: a **function override** (`Fn`) to control behavior, and a **call counter** (`Ct`) to assert invocations.

```go
func TestConversationHistoryHook_InjectsHistory(t *testing.T) {
    // Arrange: mock returns 2 turns
    mock := &core.MockConversationMemory{
        GetHistFn: func(ctx context.Context, sid string, max int) ([]core.ConversationTurn, error) {
            return []core.ConversationTurn{
                {Role: "user", Content: "What's the weather in Paris?"},
                {Role: "assistant", Content: "It's 18°C and sunny in Paris."},
            }, nil
        },
    }
    processor, err := orchestration.NewConversationHistoryProcessor(
        orchestration.ConversationHistoryProcessorConfig{
            TokenBudget:          48000,
            RecentTurnsPreserved: 4,
        },
    )
    if err != nil {
        t.Fatalf("unexpected processor error: %v", err)
    }
    hook, err := orchestration.NewConversationHistoryHook(
        mock,
        "sess-123",
        orchestration.WithConversationHistoryPreparer(processor),
    )
    if err != nil {
        t.Fatalf("unexpected hook error: %v", err)
    }

    // Act
    pctx := &core.PipelineContext{
        Request:     "And what about London?",
        Enrichments: make(map[string]interface{}),
    }
    shortCircuit, err := hook.BeforePlanning(context.Background(), pctx)

    // Assert
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if shortCircuit != nil {
        t.Fatal("expected no short-circuit")
    }
    if mock.GetHistCt != 1 {
        t.Errorf("expected 1 GetHistory call, got %d", mock.GetHistCt)
    }
    history, ok := pctx.Enrichments[core.EnrichmentConversationHistory].(string)
    if !ok || history == "" {
        t.Error("expected conversation history in enrichments")
    }
}

func TestConversationHistoryHook_RequiresPreparer(t *testing.T) {
    _, err := orchestration.NewConversationHistoryHook(
        &core.MockConversationMemory{},
        "sess-123",
    )
    if err == nil {
        t.Fatal("expected constructor to fail without a conversation history preparer")
    }
}
```

### 11.3 Testing Error Resilience

To verify that your hook degrades gracefully:

```go
func TestRAGHook_EmbeddingFailure_DoesNotBreakPipeline(t *testing.T) {
    failingEmbedder := &core.MockEmbeddingClient{
        GenerateEmbedFn: func(ctx context.Context, texts []string, opts *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
            return nil, fmt.Errorf("OpenAI rate limit exceeded")
        },
    }
    hook := hooks.NewRAGHook(nil, failingEmbedder, "docs")

    pctx := &core.PipelineContext{
        Request:     "test query",
        Enrichments: make(map[string]interface{}),
    }
    // The hook returns an error — but the orchestrator logs it and continues.
    // The pipeline proceeds without RAG context rather than failing entirely.
    _, err := hook.BeforePlanning(context.Background(), pctx)

    if err == nil {
        t.Fatal("expected error from failing embedder")
    }
    // Verify no enrichments were set (graceful degradation)
    if len(pctx.Enrichments) != 0 {
        t.Error("expected empty enrichments on failure")
    }
}
```

---

## 12. Interface Reference

### Pipeline Hook Interfaces

```go
// Base interface — all hooks implement this
type PipelineHook interface {
    Name() string
}

// Before the planning phase — can inject enrichments or short-circuit
type BeforePlanningHook interface {
    PipelineHook
    BeforePlanning(ctx context.Context, pctx *PipelineContext) (*PipelineShortCircuit, error)
}

// After plan generation — can mutate the plan
type AfterPlanningHook interface {
    PipelineHook
    AfterPlanning(ctx context.Context, pctx *PipelineContext, plan interface{}) (interface{}, error)
}

// After tool execution — observe-only
type AfterExecutionHook interface {
    PipelineHook
    AfterExecution(ctx context.Context, pctx *PipelineContext, results interface{}) error
}

// After synthesis — can mutate the response
type AfterSynthesisHook interface {
    PipelineHook
    AfterSynthesis(ctx context.Context, pctx *PipelineContext, response string) (string, error)
}
```

### Memory Interfaces

```go
// Session-scoped conversation history
type ConversationMemory interface {
    AddTurn(ctx context.Context, sessionID string, turn ConversationTurn) error
    GetHistory(ctx context.Context, sessionID string, maxTurns int) ([]ConversationTurn, error)
    Clear(ctx context.Context, sessionID string) error
}

// Cross-session similarity-based memory
type SemanticMemory interface {
    Store(ctx context.Context, namespace string, content string, metadata map[string]interface{}) error
    Search(ctx context.Context, namespace string, query string, topK int) ([]MemoryResult, error)
    Delete(ctx context.Context, namespace string, filter map[string]interface{}) error
}
```

### Embedding Interface

```go
// Vector embedding generation — batch by default
type EmbeddingClient interface {
    GenerateEmbeddings(ctx context.Context, texts []string, options *EmbeddingOptions) (*EmbeddingResponse, error)
}
```

### Supporting Structs

| Struct | Fields | Purpose |
|---|---|---|
| `PipelineContext` | `Request`, `Metadata`, `Enrichments` | Shared state flowing through all hooks |
| `PipelineShortCircuit` | `Response`, `Source` | Skip the pipeline with a pre-computed response |
| `ConversationTurn` | `Role`, `Content`, `Timestamp`, `Metadata` | A single turn in a conversation |
| `MemoryResult` | `Content`, `Score`, `Metadata`, `Timestamp` | A semantic search result |
| `EmbeddingOptions` | `Model`, `Dimensions` | Provider-specific embedding configuration |
| `EmbeddingResponse` | `Embeddings`, `Model`, `Provider`, `Usage` | Embedding generation result with token usage |

### Well-Known Enrichment Keys

| Constant | Value | Used By |
|---|---|---|
| `core.EnrichmentConversationHistory` | `"conversation_history"` | Shared `ConversationHistoryPreparer` (optionally fed by `ConversationHistoryHook`) |
| `core.EnrichmentRAGContext` | `"rag_context"` | RAG hooks, MemoryEnrichmentHook |
| `core.EnrichmentActivityCoordination` | `"activity_coordination"` | ActivityAnnouncementHook |
| `core.EnrichmentUserProfile` | `"user_profile"` | UserMemoryEnrichmentHook |

### Built-In Memory Pipeline Hooks

The orchestration module provides ready-to-use hooks for cross-agent shared memory. Wire them via `memory.NewSharedBackends()` + `orchestration.BuildMemoryHooks()` (see [Agent Memory User Guide](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md)), or construct them directly for full control:

| Hook | Stage | Purpose |
|------|-------|---------|
| `MemoryEnrichmentHook` | BeforePlanning | Queries episodic events, active investigations, shared knowledge, and domain activity digest. Injects into `rag_context`. |
| `MemoryRecordHook` | AfterExecution | Records structured `AgentEvent` for each execution step. Uses LLM `EventSummarizer` for actionable summaries (falls back to heuristic). |
| `KnowledgeExtractionHook` | AfterSynthesis | Extracts reusable knowledge from the synthesis response via LLM and stores in vector DB. |
| `ActivityAnnouncementHook` | BeforePlanning | Announces the agent's current activity and injects other agents' signals into `activity_coordination`. |
| `ActivityCleanupHook` | AfterSynthesis | Marks the agent's activity as completed (removes the transient signal). |

These hooks are ordered: announcement (first) → enrichment → record → extraction → cleanup (last). See [examples/devops-chat-agent/main.go](https://github.com/truvaagents/truva-g3/blob/main/examples/devops-chat-agent/main.go) for wiring via `BuildMemoryHooks`, or construct hooks manually for full control over each option.

#### Entity Extraction Defaults

`MemoryEnrichmentHook` and `MemoryRecordHook` use an `EntityExtractor` to identify which domain entities each step operated on. The framework picks an extractor automatically based on whether you wired an `AIClient`:

```go
// Default behaviour — record hook gets LLMEntityExtractor (zero-cost LLM
// extraction piggybacking on the existing EventSummarizer call), enrichment
// hook gets NoOpEntityExtractor (pre-planning, the summarizer hasn't run yet).
hooks, _ := orchestration.BuildMemoryHooks(backends.ToDeps(), agent.AI, agent.Logger)

// Opt out of LLM extraction (use explicit metadata only)
hooks, _ := orchestration.BuildMemoryHooks(backends.ToDeps(), agent.AI, agent.Logger,
    orchestration.WithBuilderRecordEntityExtractor(orchestration.NoOpEntityExtractor{}))

// Use a domain-specific extractor on the enrichment path
hooks, _ := orchestration.BuildMemoryHooks(backends.ToDeps(), agent.AI, agent.Logger,
    orchestration.WithBuilderEnrichmentEntityExtractor(myDomainExtractor))

// Use the same extractor for both hooks (shorthand)
hooks, _ := orchestration.BuildMemoryHooks(backends.ToDeps(), agent.AI, agent.Logger,
    orchestration.WithMemoryEntityExtractor(myExtractor))
```

The framework treats `Entity{Type, ID}` as an opaque key — domain semantics are entirely the application's responsibility. See [Agent Memory User Guide](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md#how-entity-extraction-works) for the full design rationale.

### Built-In User Memory Pipeline Hooks

For personal assistant agents that need cross-session user personalization. Wire via `memory.NewUserMemoryBackend()` + `orchestration.BuildUserMemoryHooks()`, or construct directly. Separate from shared memory hooks — compose independently.

| Hook | Stage | Purpose |
|------|-------|---------|
| `UserMemoryEnrichmentHook` | BeforePlanning | Recalls per-user facts (identity, preferences, constraints, session summaries) and injects `<user_profile>` into the planning prompt. |
| `UserMemoryExtractionHook` | AfterSynthesis | Extracts persistent facts from the conversation via LLM, reconciles with existing facts (ADD/UPDATE/CONTRADICT/DUPLICATE), stores new facts, generates session summary. |

```go
// Wiring user memory alongside shared memory
backend, _ := memory.NewUserMemoryBackend(logger,
    memory.WithUserMemoryNamespace("travel"),
    memory.WithUserMemoryEmbeddingClient(embedder),
)
defer backend.Close()
userHooks, userHooksCloser := orchestration.BuildUserMemoryHooks(backend.ToDeps(), aiClient, logger)
defer userHooksCloser.Close()

deps := orchestration.OrchestratorDependencies{
    PipelineHooks: append(sharedMemoryHooks, userHooks...),
}
```

`BuildUserMemoryHooks(...)` uses asynchronous extraction by default so post-synthesis memory work does not delay chat completion. Pass `orchestration.WithSynchronousExtraction()` if you need extraction to finish before the request continues.

User memory requires a `user_id` in `PipelineContext.Metadata["user_id"]`. Agents without user context (e.g., event-driven agents) skip user memory hooks silently. See [API Reference](../reference/API_REFERENCE.md) for interface details.

---

## 13. Design Decisions and Trade-offs

**Why per-stage interfaces instead of a single callback?**
A single `OnEvent(stage, data)` callback requires type-switching inside every hook. Per-stage interfaces let developers implement only the stages they care about. The hook runner uses type assertions — a hook that only implements `BeforePlanningHook` is silently skipped for all other stages.

**Why errors don't abort the pipeline?**
Hooks are optional enhancements. A failing RAG retrieval shouldn't prevent the agent from answering — the LLM can still work without the extra context. This follows the framework's design principle: "Missing optional dependencies should not break core functionality."

**Why `interface{}` for plan and results types?**
The plan and execution result types are internal to the orchestration module. Exposing concrete types like `*RoutingPlan` would couple hook implementations to the orchestration module's internals. Using `interface{}` keeps hooks in the `core` module (which has no dependencies) and lets the orchestration module evolve its types freely.

**Why separate ConversationMemory and SemanticMemory?**
They serve different access patterns. `ConversationMemory` is session-scoped and ordered (FIFO — "give me the last 10 turns"). `SemanticMemory` is cross-session and similarity-based ("find content similar to this query"). Merging them into one interface would force every implementation to support both patterns.

**Why sessionID per-call instead of per-instance?**
Unlike LangChain (which binds memory to a session at construction), TruvaG3 passes `sessionID` on every call. This lets a single memory instance serve multiple concurrent sessions — important in K8s deployments where an agent pod handles requests from many users.

---

## 14. Further Reading

- [API Reference](../reference/API_REFERENCE.md) — Full orchestrator API, including `OrchestratorDependencies` and `PromptBuilder`
- [Conversation History Guide](../memory-and-chat/CONVERSATION_HISTORY_GUIDE.md) — Tier 1 defaults, Tier 2 compaction, and advanced overrides
- [Orchestration Modes Guide](../orchestration/ORCHESTRATION_MODES_GUIDE.md) — How planning, execution, and synthesis work
- [Tool Development Guide](TOOL_DEVELOPMENT_GUIDE.md) — Building tools that hooks can enrich
- [Agent Development Guide](AGENT_DEVELOPMENT_GUIDE.md) — Building agents with `BaseAgent` (which includes the `ConversationMemory` and `SemanticMemory` fields)
- [Error Handling Guide](../orchestration/ERROR_HANDLING_GUIDE.md) — How hook errors flow through the resilient error handling system
- [Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md) — Each hook gets its own telemetry span (e.g. `pipeline.hook.before_planning.rag_qdrant`)
