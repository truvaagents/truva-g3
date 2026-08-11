# Pipeline Hooks Guide

The extension points that let you inject context, short-circuit work, and post-process responses at every stage of the orchestration pipeline — without forking the framework.

> **Working Example**
>
> The hooks described here are exercised by several shipped agents:
> - **Memory-backed Q&A**: [`examples/qa-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/qa-agent) (search for `BuildMemoryHooks`)
> - **Per-user memory in chat**: [`examples/travel-chat-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/travel-chat-agent) (search for `BuildUserMemoryHooks`)
> - **DevOps chat + activity coordination**: [`examples/devops-chat-agent/`](https://github.com/truvaagents/truva-g3/tree/main/examples/devops-chat-agent)
>
> If you want a scenario-first cookbook (RAG, semantic caching, guardrails) rather than this mechanism reference, read [Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) alongside this guide.

## Table of Contents

1. [Why This Guide Exists](#1-why-this-guide-exists)
2. [What Are Pipeline Hooks?](#2-what-are-pipeline-hooks)
3. [The Pipeline Lifecycle](#3-the-pipeline-lifecycle)
4. [The Hook Stages](#4-the-hook-stages)
5. [PipelineContext and Enrichments](#5-pipelinecontext-and-enrichments)
6. [Short-Circuiting the Pipeline](#6-short-circuiting-the-pipeline)
7. [Writing Your Own Hook](#7-writing-your-own-hook)
8. [Registering Hooks](#8-registering-hooks)
9. [Execution Semantics and Guarantees](#9-execution-semantics-and-guarantees)
10. [Built-in Hooks Reference](#10-built-in-hooks-reference)
11. [Testing Hooks](#11-testing-hooks)
12. [Design Decisions and Trade-offs](#12-design-decisions-and-trade-offs)
13. [Troubleshooting](#13-troubleshooting)
14. [Further Reading](#14-further-reading)

---

## 1. Why This Guide Exists

Out of the box, a TruvaG3 orchestrator does four things in sequence: it **plans** (asks an LLM which tools to call), **executes** those tools, **synthesizes** a final answer, and returns it. That loop is intentionally closed — it has no opinion about your knowledge base, your conversation history, your cache, or your content policy.

Pipeline hooks are how you open that loop *without touching framework internals*. They let you:

- Inject retrieved context (RAG, memory, conversation history) into the prompt **before** the planner runs.
- Return a cached answer and skip the entire pipeline (**short-circuit**).
- Record execution outcomes to memory or analytics **after** tools finish.
- Filter, redact, or log the final response **after** synthesis.

Memory is the framework's most prominent consumer of hooks — but it has no privileged status. Everything memory does, your own code can do through the same interfaces. This guide documents the mechanism itself: the contracts, where each stage fires, how data flows into the prompt, and the sharp edges to avoid.

> **Who this is for**
>
> Developers wiring custom context into an agent, or anyone debugging why a hook didn't fire (or fired but didn't reach the LLM). If you just want to add RAG or caching from a recipe, start with [Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) and return here when you need the underlying detail.

---

## 2. What Are Pipeline Hooks?

A pipeline hook is **per-stage middleware**: a small object that the orchestrator calls at a defined point in the request lifecycle. Every hook implements the base `core.PipelineHook` interface — a single `Name()` method used for logging and telemetry span names — plus **one or more** stage-specific interfaces.

```go
// core/interfaces.go
type PipelineHook interface {
    Name() string
}
```

There are four stage interfaces. A single hook type can implement as many as it wants; the orchestrator discovers what a hook supports via type assertion at each stage, so partial implementations are normal and expected.

| Hook Stage | When It Runs | Can Mutate? | Can Short-Circuit? | Wired in live path? |
|---|---|---|---|---|
| `BeforePlanningHook` | Before the planning phase begins | Enrichments | **Yes** — skips the entire pipeline | Yes |
| `AfterPlanningHook` | After each phase reaches one final validated planner-produced plan | The plan | No | Yes |
| `AfterExecutionHook` | After all tools finish executing | No (observe-only) | No | Yes |
| `AfterSynthesisHook` | After the LLM synthesizes the final response | Response text | No | Yes |

Two properties define the whole system and are worth committing to memory:

- **Hook callback failures fail open.** If a hook returns an error, the orchestrator logs a warning and continues. Invalid provenance-aware short-circuit contracts (missing payload or unknown kind) are configuration/programming errors and fail the request instead of silently bypassing cache enforcement.
- **Hooks run sequentially, in registration order.** There is no concurrent access to the shared context object, so hooks can read and write it without locking.

---

## 3. The Pipeline Lifecycle

Here is where each stage fires relative to the orchestrator's core work. The single `PipelineContext` object (§5) threads through every stage of a request.

```
Request
  │
  ▼
┌─────────────────────────────────────────────┐
│  prepareKnownEnrichments()                    │  ← auto-promotes conversation_history /
│                                               │     rag_context from Metadata (no hook needed)
└───────────────────────┬───────────────────────┘
                        ▼
┌─────────────────────────────────────────────┐
│  BeforePlanningHook(s)                        │  ← inject RAG / memory / history into Enrichments
│  ▶ may return a short-circuit decision ───────┼──▶ skip everything, return accepted Response
└───────────────────────┬───────────────────────┘
                        │  Enrichments copied into ctx via WithPipelineEnrichments()
                        ▼
┌─────────────────────────────────────────────┐
│  Planning (LLM call)                          │  ← prompt builder reads enrichments back out
└───────────────────────┬───────────────────────┘
                        ▼
┌─────────────────────────────────────────────┐
│  AfterPlanningHook(s)                        │  ← copy-on-write mutation; full plan revalidation
└───────────────────────┬───────────────────────┘
                        ▼
┌─────────────────────────────────────────────┐
│  Execution (tool calls, possibly multi-phase) │
└───────────────────────┬───────────────────────┘
                        ▼
┌─────────────────────────────────────────────┐
│  AfterExecutionHook(s)                        │  ← record outcomes to memory / analytics
└───────────────────────┬───────────────────────┘
                        ▼
┌─────────────────────────────────────────────┐
│  Synthesis (LLM call)                         │
└───────────────────────┬───────────────────────┘
                        ▼
┌─────────────────────────────────────────────┐
│  AfterSynthesisHook(s)                        │  ← guardrails, redaction, memory write-back
└───────────────────────┬───────────────────────┘
                        ▼
                     Response
```

All public request modes enter the shared lifecycle in
`orchestration/request_lifecycle.go`. Phase planning and the
`AfterPlanningHook` boundary live in `orchestration/orchestrator.go`; the hook
runners and validation logic live in `orchestration/pipeline_hooks.go`.

> **Streaming caveat**
>
> On the streaming path, tokens are sent to the client *during* synthesis. By the time `AfterSynthesis` hooks run, the user has already seen the text. The returned (possibly mutated) string updates the final `OrchestratorResponse.Response` for recording purposes, but **it cannot un-stream what was already delivered**. If you need to *block* content before the user sees it, do it in a `BeforePlanning` hook, in a guarded prompt builder, or use the non-streaming path. See `orchestrator.go:3510-3513`.

---

## 4. The Hook Stages

All four contracts are defined in `core/interfaces.go:264-301`. Each embeds `PipelineHook`.

### 4.1 BeforePlanningHook

```go
// core/interfaces.go
type BeforePlanningHook interface {
    PipelineHook
    BeforePlanning(ctx context.Context, pctx *PipelineContext) (*PipelineShortCircuit, error)
}
```

Runs before the planner sees the prompt. This is the **only** stage that can both
**enrich** (write into `pctx.Enrichments`) and **short-circuit** (through the
legacy payload or the provenance-aware decision contract). It is where RAG
retrieval, memory recall, conversation-history injection, and semantic-cache
lookups belong. New cache hooks should use `BeforePlanningDecisionHook`; legacy
short-circuits are interpreted as authoritative responses.

Return `(nil, nil)` for the common "I added some context, carry on" case. Return a non-nil short-circuit only when you have a complete answer and want to bypass planning, execution, and synthesis entirely (§6).

### 4.2 AfterPlanningHook

```go
// core/interfaces.go
type AfterPlanningHook interface {
    PipelineHook
    AfterPlanning(ctx context.Context, pctx *PipelineContext, plan interface{}) (interface{}, error)
}
```

Runs exactly once for the final planner-produced plan in each phase, after the
normalization/validation fixpoint and before HITL, persistence, or execution.
HITL resume plans do not run this hook because they are approved checkpoint
state rather than newly produced plans.

Mutation is copy-on-write and chained in registration order. Each hook must
return a non-nil `*orchestration.RoutingPlan`. The framework normalizes and
runs the complete plan-validation gauntlet after each mutation. A hook error,
wrong return type, clone failure, or invalid mutation is rejected with bounded
diagnostics and the last valid plan continues; it never triggers LLM
regeneration and cannot corrupt that prior plan.

### 4.3 AfterExecutionHook

```go
// core/interfaces.go
type AfterExecutionHook interface {
    PipelineHook
    AfterExecution(ctx context.Context, pctx *PipelineContext, results interface{}) error
}
```

Runs after all tool execution (including multi-phase loops) completes, before synthesis. It receives the combined execution result and returns only an `error` — there is **no way to change `results`**. This is an **observe-only** stage: record the outcome to memory, emit metrics, log to an analytics pipeline. The built-in `MemoryRecordHook` lives here.

### 4.4 AfterSynthesisHook

```go
// core/interfaces.go
type AfterSynthesisHook interface {
    PipelineHook
    AfterSynthesis(ctx context.Context, pctx *PipelineContext, response string) (string, error)
}
```

Runs after the final response is synthesized. It receives the response string and returns a possibly-modified one; the output of one hook is fed as input to the next (**chaining**, §9). Use it for guardrails, PII redaction, response logging, and memory/knowledge write-back. Mind the **Streaming caveat** blockquote in [§3](#3-the-pipeline-lifecycle) — on the streaming path the mutation is post-hoc.

---

## 5. PipelineContext and Enrichments

### 5.1 The context object

Every stage receives the same `*PipelineContext` for the request:

```go
// core/interfaces.go:251-255
type PipelineContext struct {
    Request     string                 // the raw user request
    Metadata    map[string]interface{} // caller-supplied request metadata (user_id, session, turns…)
    Enrichments map[string]interface{} // the scratchpad hooks write into to reach the prompt
}
```

- **`Request`** — the original user query, read-only in practice.
- **`Metadata`** — whatever the caller attached to the request (e.g. `user_id`, conversation turns, session keys). Hooks *read* from this to decide what to do.
- **`Enrichments`** — a mutable map, initialized empty by the orchestrator. A `BeforePlanning` hook *writes* into it; the orchestrator then makes it available to the prompt builder.

### 5.2 How enrichments reach the LLM

This is the part that trips people up: writing to `pctx.Enrichments` is necessary but the magic that carries it into the prompt is a two-step handoff.

```
BeforePlanning hook        orchestrator                    prompt builder
─────────────────────      ─────────────────────────       ────────────────────────
pctx.Enrichments[key]  ──▶  ctx = core.WithPipeline    ──▶  enrichments :=
   = value                  Enrichments(ctx,                core.GetPipelineEnrichments(ctx)
                            pctx.Enrichments)                → emits <agent_memory>, etc.
```

1. Your hook writes `pctx.Enrichments[core.EnrichmentRAGContext] = "...retrieved docs..."`.
2. After `BeforePlanning` hooks finish, the orchestrator copies the map into the Go context: `ctx = core.WithPipelineEnrichments(ctx, pctx.Enrichments)` (`orchestrator.go:2960`). The helpers are in `core/context.go:10-19`.
3. The prompt builder reads it back via `core.GetPipelineEnrichments(ctx)` and emits known keys as tagged sections of the planning prompt (`orchestrator.go:5229-5258`).

> **Default builder vs. custom `PromptBuilder`**
>
> The two routes to the prompt are the same map seen from two surfaces. The **default** builder calls `core.GetPipelineEnrichments(ctx)` itself and renders the well-known keys below. If you supply a **custom `PromptBuilder`**, the orchestrator hands it the identical enrichment map as `PromptInput.Metadata` (`orchestrator.go:5199`) — so your builder reads `input.Metadata[core.EnrichmentRAGContext]` rather than calling the context helper. [Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) documents the `PromptInput.Metadata` path; this guide documents the default-builder path. They are the same data.

### 5.3 Well-known enrichment keys

The default prompt builder recognizes four keys. Using these keys means your injected context reaches the LLM with zero extra wiring:

| Constant | String value | Rendered as | Typical writer |
|---|---|---|---|
| `core.EnrichmentRAGContext` | `"rag_context"` | wrapped in `<agent_memory>…</agent_memory>` | RAG / `MemoryEnrichmentHook` |
| `core.EnrichmentConversationHistory` | `"conversation_history"` | wrapped in `<conversation_history>…</conversation_history>` | chat history |
| `core.EnrichmentActivityCoordination` | `"activity_coordination"` | wrapped in `<agent_coordination>…</agent_coordination>` | `ActivityAnnouncementHook` |
| `core.EnrichmentUserProfile` | `"user_profile"` | **inserted verbatim** — no tag wrapping | `UserMemoryEnrichmentHook` |

The first three are defined at `core/interfaces.go:258-262`; `EnrichmentUserProfile` at `core/interfaces.go:932`. Three are wrapped in their own XML section by the builder; `user_profile` is the exception — the builder writes it **as-is** (`default_prompt_builder.go:284-288`), so a custom user-profile hook must supply its own preformatted `<user_profile>…</user_profile>` block, exactly as `UserMemoryEnrichmentHook` does.

> **Custom keys: enrichments are the only channel**
>
> You can put *any* key into `Enrichments`, but only these four are rendered by the default builder — a custom key needs a custom `PromptBuilder` to read it (§5.2). The asymmetry worth remembering: auto-promotion (§5.4) only lifts `conversation_history` and `rag_context` out of request `Metadata`, so putting an arbitrary key in request **metadata** does **nothing** unless a hook copies it into `pctx.Enrichments`.

> **Note: enrichments survive multi-phase planning**
>
> Enrichments are set once, before the first planning call, and stored in `ctx`. When the orchestrator runs continuation phases (Phase 2+), the continuation-prompt builder re-reads `GetPipelineEnrichments(ctx)` and re-emits the same sections (`orchestrator.go:5500-5535`). You inject once; every phase sees it.

### 5.4 Auto-promotion: the zero-hook path

Before any hook runs, the orchestrator calls `prepareKnownEnrichments` (`orchestration/pipeline_hooks.go:40-70`). It copies `conversation_history` and `rag_context` straight out of `Metadata` into `Enrichments`, and runs conversation turns through the configured `ConversationHistoryPreparer`. The practical consequence:

> If you can attach your context as request **metadata**, you may not need a hook at all. Pass `metadata[core.EnrichmentRAGContext]` (or conversation turns via `metadata[orchestration.MetadataConversationTurns]`) and the framework promotes it for you. Hooks run *after* this step, so a `BeforePlanning` hook can still override or augment the prepared values. Reach for a hook when retrieval requires logic (a vector query, a cache lookup) rather than data you already hold.

---

## 6. Short-Circuiting the Pipeline

A provenance-aware `BeforePlanningDecisionHook` can end the request immediately:

```go
type BeforePlanningDecisionHook interface {
    PipelineHook
    BeforePlanningDecision(
        ctx context.Context,
        pctx *PipelineContext,
        gate PipelineGate,
    ) (*PipelineShortCircuitDecision, error)
}

type PipelineShortCircuitDecision struct {
    ShortCircuit  *PipelineShortCircuit
    Kind          PipelineShortCircuitKind // authoritative or cache
    CachedAgainst map[string]string         // variation map stored with a cache entry
}
```

Use `PipelineShortCircuitAuthoritative` for policy denials, rate limits, and
other answers that remain valid regardless of cache state. Use
`PipelineShortCircuitCache` only for a cache entry. For cache decisions,
`CachedAgainst` must be the variation map persisted when that entry was written,
not `gate.CacheVary()` read at lookup time. Echoing current values would make
the freshness check meaningless.

The framework accepts a cached response only when cache reads are enabled and
every orchestration-reserved dimension matches symmetrically: a value present
on only one side is also a mismatch. The foundation exposes the generic gate;
its variation map can be empty until an installed orchestration feature owns a
reserved dimension. Unknown kinds and missing payloads fail the request as hook
contract errors. Other rejected cache decisions continue through planning.

When a decision is accepted:

- The orchestrator **stops running further `BeforePlanning` hooks** — the first short-circuit wins.
- Planning, execution, and synthesis are **all skipped**.
- A complete `OrchestratorResponse` is built from the short-circuit, with `Confidence: 1.0` and any already-injected enrichments merged into the response metadata (`buildShortCircuitResponse`, `pipeline_hooks.go:13-35`).
- A bounded `orchestration.pipeline.short_circuit.decision` metric records the
  provenance kind, decision reason, and status. Hook names and `Source` values
  are intentionally excluded from metric labels; accepted decisions remain
  identifiable through the correlated `pipeline.short_circuit.decision` trace
  event.
- On the streaming path, the cached response is chunked and streamed to preserve the streaming contract (`orchestrator.go:3273-3294`).

The canonical cache use case is a **semantic cache**: hash or embed the request,
look for a hit, and return the cached answer without paying for an LLM round
trip. None of the shipped hooks short-circuit.

```go
func (h *SemanticCacheHook) BeforePlanningDecision(
    ctx context.Context,
    pctx *core.PipelineContext,
    gate core.PipelineGate,
) (*core.PipelineShortCircuitDecision, error) {
    entry, ok, err := h.cache.Lookup(ctx, pctx.Request)
    if err != nil {
        return nil, err // fail open — logged & skipped, pipeline continues normally
    }
    if !ok || gate.ResponseCacheReadDisabled() {
        return nil, nil // cache miss — carry on with planning
    }
    return &core.PipelineShortCircuitDecision{
        ShortCircuit: &core.PipelineShortCircuit{
            Response: entry.Response,
            Source:   "semantic_cache",
        },
        Kind:          core.PipelineShortCircuitCache,
        CachedAgainst: entry.VariationDimensions, // stored alongside Response
    }, nil
}
```

The legacy `BeforePlanningHook` remains source-compatible. A legacy
`PipelineShortCircuit` is treated as authoritative and emits a diagnostic when
reserved cache dimensions exist. Cache implementations should migrate to the
decision hook; policy/guardrail hooks may remain legacy or declare
`PipelineShortCircuitAuthoritative` explicitly.

---

## 7. Writing Your Own Hook

A hook is any struct with a `Name()` method plus at least one stage method. Nothing else is required — no registration interface, no base class to embed.

### 7.1 The smallest possible hook

This is the minimal shape — a `BeforePlanning` hook that injects a static enrichment. (It mirrors the `enrichmentHook` test helper in `pipeline_hooks_test.go:87-97`.)

```go
package hooks

import (
    "context"

    "github.com/truvaagents/truva-g3/core"
)

type StaticEnrichmentHook struct {
    key   string
    value interface{}
}

func (h *StaticEnrichmentHook) Name() string { return "static-" + h.key }

func (h *StaticEnrichmentHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
    pctx.Enrichments[h.key] = h.value
    return nil, nil
}
```

### 7.2 A realistic RAG hook

A `BeforePlanning` hook that queries a vector store and injects the results under the well-known RAG key, so the default prompt builder renders them as `<agent_memory>`:

```go
// rag_hook.go
package hooks

import (
    "context"
    "fmt"
    "strings"

    "github.com/truvaagents/truva-g3/core"
)

type RAGHook struct {
    retriever VectorRetriever // your interface: Search(ctx, query) ([]Doc, error)
    topK      int
    logger    core.Logger
}

func NewRAGHook(retriever VectorRetriever, topK int) *RAGHook {
    return &RAGHook{retriever: retriever, topK: topK}
}

func (h *RAGHook) Name() string { return "rag-retriever" }

func (h *RAGHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
    docs, err := h.retriever.Search(ctx, pctx.Request)
    if err != nil {
        return nil, fmt.Errorf("rag search: %w", err) // fail open
    }
    if len(docs) == 0 {
        return nil, nil
    }

    var b strings.Builder
    for i, d := range docs {
        if i >= h.topK {
            break
        }
        fmt.Fprintf(&b, "- %s\n", d.Text)
    }
    // Well-known key → rendered as <agent_memory> in the planning prompt.
    pctx.Enrichments[core.EnrichmentRAGContext] = b.String()
    return nil, nil
}

// SetLogger is optional — the factory injects the framework logger if present.
func (h *RAGHook) SetLogger(l core.Logger) { h.logger = l }
```

### 7.3 A guardrail hook

An `AfterSynthesis` hook that redacts the response. Remember the streaming caveat — on streaming paths this is post-hoc.

```go
func (h *RedactionHook) Name() string { return "pii-redaction" }

func (h *RedactionHook) AfterSynthesis(ctx context.Context, pctx *core.PipelineContext, response string) (string, error) {
    return h.redactor.Scrub(response), nil
}
```

> **Optional setter interfaces**
>
> If your hook implements any of `SetLogger(core.Logger)`, `SetTelemetry(core.Telemetry)`, or `SetLLMDebugStore(LLMDebugStore)`, the factory detects them by type assertion and injects the framework's instances after registration (`factory.go:364-384`). These are entirely optional — implement them only if your hook needs to log, trace, or record LLM calls.

> **Tip: assert the contract at compile time**
>
> Because stages are discovered by type assertion, a typo in a method signature fails silently — your hook compiles, registers, and is simply skipped at runtime. Add a compile-time assertion so the mismatch becomes a build error instead. Several built-in hooks do this (e.g. `ConversationHistoryHook`, `UserMemoryEnrichmentHook`):
>
> ```go
> var _ core.BeforePlanningHook = (*RAGHook)(nil)
> ```

---

## 8. Registering Hooks

> **Registration is factory-only.** There is no framework-level `WithHooks` option. Hooks are attached through `OrchestratorDependencies.PipelineHooks` when you create the orchestrator. (`framework.go` exposes config options but nothing about pipeline hooks.)

### 8.1 The direct path

```go
deps := orchestration.OrchestratorDependencies{
    Discovery:           discovery,
    AIClient:            agent.AI,
    Logger:              agent.Logger,
    Telemetry:           telemetry.GetTelemetryProvider(),
    PipelineHooks:       []core.PipelineHook{ // ← registration order = execution order
        NewSemanticCacheHook(cache), // BeforePlanning (may short-circuit)
        NewRAGHook(retriever, 5),     // BeforePlanning (enrich)
        &RedactionHook{redactor: r},  // AfterSynthesis
    },
}

orch, err := orchestration.CreateOrchestrator(config, deps)
```

The factory assigns the slice to the orchestrator and runs the optional-setter injection described in §7 (`factory.go:354-385`). This is exactly what [`examples/qa-agent/qa_agent.go:220-230`](https://github.com/truvaagents/truva-g3/blob/main/examples/qa-agent/qa_agent.go) does.

### 8.2 The builder path (memory)

You rarely assemble memory hooks by hand. The `BuildMemoryHooks` builder returns a ready-ordered `[]core.PipelineHook` (announcement → enrichment → record → knowledge-extraction → cleanup) plus an `ActivityCoordinator`, gated on which backends you configured:

```go
// examples/qa-agent/main.go
memHooks, activityCoord := orchestration.BuildMemoryHooks(
    memBackends.ToDeps(), agent.AI, agent.Logger,
)
// …then later…
deps.PipelineHooks = memHooks
deps.ActivityCoordinator = activityCoord
```

For per-user memory, `BuildUserMemoryHooks` plays the same role — see [`examples/travel-chat-agent/chat_agent.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/travel-chat-agent/chat_agent.go). Behavioral knobs (lookback window, retrieval weights, entity extractor) are passed as options to the builder; see [Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) and the [Agent Memory User Guide](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md).

> **Mixing custom and built-in hooks**
>
> `PipelineHooks` is a flat slice, and slice order is execution order. **Append** enrichment hooks to the builder's output — your RAG hook and `MemoryEnrichmentHook` both run at `BeforePlanning`, each accumulating into `Enrichments`. But **prepend** a short-circuiting cache hook if you want it to pre-empt the builder's (expensive) enrichment hooks: the first `BeforePlanning` hook to short-circuit wins (§9).

---

## 9. Execution Semantics and Guarantees

The runners in `orchestration/pipeline_hooks.go` all share one shape: iterate `pipelineHooks` in order, type-assert to the stage interface (skip hooks that don't implement it), open a telemetry span named `pipeline.hook.<stage>.<Name()>`, call the hook.

**Ordering.** Hooks fire in registration (slice) order within a stage. A hook that implements multiple stages is invoked once per stage, at the appropriate point in the lifecycle.

**Type filtering.** Implementing `BeforePlanningHook` does not obligate you to implement the others. At each stage the runner does `h, ok := hook.(core.AfterSynthesisHook)` and silently skips non-matching hooks. This is why partial hooks are idiomatic.

**Fail-open resilience.** Every runner handles a hook error identically: log `"Pipeline hook failed, skipping"` with the hook name and `continue`. One bad hook never aborts the request and never prevents later hooks from running. If your hook's work is *mandatory* (e.g. a hard compliance gate), a hook is the wrong place — enforce it at a layer that can reject the request.

**Mutation chaining.** `AfterSynthesis` and `AfterPlanning` chain: the accepted output of hook *N* is the input to hook *N+1*. Order matters. Invalid after-planning output leaves the last valid plan in place. `AfterExecution` is observe-only and `BeforePlanning` accumulates into the shared `Enrichments` map, so neither "chains" a return value.

**Short-circuit precedence.** The first `BeforePlanning` hook to return a non-nil short-circuit wins; subsequent `BeforePlanning` hooks are not called. Put your cache hook early if you want it to pre-empt expensive enrichment hooks.

**Telemetry.** Each hook invocation is wrapped in its own span (when a telemetry provider is configured), so you can see in a trace exactly which hooks ran and how long each took — e.g. `pipeline.hook.before_planning.rag-retriever`. Errors are recorded on the span. See the [Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md).

**Concurrency.** Hooks for a single request run sequentially with no concurrent access to `PipelineContext` (`core/interfaces.go:250`), so you don't need locks around reads/writes of `Enrichments`. Hooks across *different* requests run on different `PipelineContext` instances; any shared state *your* hook holds (a cache client, a retriever) must be safe for concurrent use.

---

## 10. Built-in Hooks Reference

These ship in the `orchestration` module and are the best worked examples to read. Most are assembled for you by `BuildMemoryHooks` / `BuildUserMemoryHooks` (§8.2) — the exception is `ConversationHistoryHook`, which you construct directly via `NewConversationHistoryHook` and add to the slice yourself (the builders do not wire conversation history). None of them short-circuit.

| Type | File | Stage | What it does |
|---|---|---|---|
| `MemoryEnrichmentHook` | `orchestration/memory_hooks.go:176` | `BeforePlanning` | Extracts entities from the request, queries shared episodic memory, and **appends** recent/related domain events into `Enrichments[EnrichmentRAGContext]`. Read/enrich. |
| `MemoryRecordHook` | `orchestration/memory_hooks.go:718` | `AfterExecution` | Records the completed execution (entities, outcome, importance) into episodic memory. Write/record. |
| `UserMemoryEnrichmentHook` | `orchestration/user_memory_hooks.go:27` | `BeforePlanning` | Reads `Metadata["user_id"]`; recalls per-user identity and query-relevant facts into `Enrichments[EnrichmentUserProfile]`. No-ops without a user ID. |
| `UserMemoryExtractionHook` | `orchestration/user_memory_extraction.go:59` | `AfterSynthesis` | Extracts durable user facts from request+response via LLM and stores them per-user. Returns the response unchanged. Async or sync. |
| `KnowledgeExtractionHook` | `orchestration/knowledge_extraction_hook.go:24` | `AfterSynthesis` | Asks an LLM for 0–3 reusable knowledge fragments, embeds and stores them. Runs in a goroutine; never mutates the response. |
| `ConversationHistoryHook` | `orchestration/conversation_history_hook.go:15` | `BeforePlanning` | Adapter that injects memory-backed conversation history into `Enrichments[EnrichmentConversationHistory]`. Constructed manually via `NewConversationHistoryHook` — not wired by the builders. (For raw turns, prefer the metadata path in §5.4.) |
| `ActivityAnnouncementHook` | `orchestration/activity_hooks.go:52` | `BeforePlanning` | Announces this agent's activity to the coordinator and injects concurrent activities into `Enrichments[EnrichmentActivityCoordination]`. |
| `ActivityCleanupHook` | `orchestration/activity_hooks.go:228` | `AfterSynthesis` | Clears this agent's announced activity signal after the response. Pass-through. |

Notice the symmetry: the `BeforePlanning` hooks **read/enrich**, the `AfterExecution`/`AfterSynthesis` hooks **write/record**. Several also declare the compile-time conformance assertion recommended in [§7](#7-writing-your-own-hook) — a habit worth adopting in your own hooks.

---

## 11. Testing Hooks

This section covers unit-testing a hook in isolation. For diagnosing a hook that misbehaves inside a *running* agent (didn't fire, fired but didn't reach the LLM), jump to [§13 Troubleshooting](#13-troubleshooting).

Hooks are plain structs, so they're trivial to unit-test in isolation: construct a `*core.PipelineContext`, call the stage method, assert on the returned value and the mutated `Enrichments`.

```go
func TestRAGHook_InjectsContext(t *testing.T) {
    h := NewRAGHook(stubRetriever{docs: []Doc{{Text: "Paris is the capital of France."}}}, 5)
    pctx := &core.PipelineContext{
        Request:     "What is the capital of France?",
        Metadata:    map[string]interface{}{},
        Enrichments: map[string]interface{}{},
    }

    sc, err := h.BeforePlanning(context.Background(), pctx)
    require.NoError(t, err)
    require.Nil(t, sc) // no short-circuit
    require.Contains(t, pctx.Enrichments[core.EnrichmentRAGContext], "Paris")
}
```

The framework's own contract tests are worth reading as a reference for expected behavior — `orchestration/pipeline_hooks_test.go` proves type filtering (only matching hooks fire), short-circuit precedence (first wins, rest skipped), and fail-open error handling (an erroring hook is logged and skipped). The file also contains compact exemplar hooks:

- `enrichmentHook` (`:87-97`) — the minimal one-stage hook shown in §7.
- `allStagesHook` (`:33-84`) — implements all four stages with call counters and injectable errors/mutations; a good template for an exhaustive test double.
- `beforeOnlyHook` (`:100-109`) — demonstrates the partial-implementation pattern.

There is no exported NoOp hook in non-test code; if you want one for tests, the `enrichmentHook` shape above is the smallest thing that compiles. To stub a hook's *dependencies* (an embedding client, a memory backend), the `core` module ships `NoOp*` and `Mock*` doubles — see [Adding Context to Your Agent §11](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) for the conventions.

---

## 12. Design Decisions and Trade-offs

**Why per-stage interfaces instead of one big callback?** Type assertion lets a hook declare exactly the stages it cares about. A guardrail hook implements only `AfterSynthesis`; the orchestrator skips it everywhere else with no boilerplate on your side. It also keeps each method signature honest about what data is available and what can change at that point.

**Why do hooks fail open?** Context injection is an enhancement. A vector store being briefly unreachable should degrade the answer's quality, not turn the request into a 500. If you need hard enforcement, the hook layer is the wrong layer — reject at the API boundary instead.

**Why can only `BeforePlanning` short-circuit?** Short-circuiting means "I already have the answer, skip the work." That only makes sense before any work has happened. After planning or execution, you've already paid the cost, so there's nothing to save.

**Why is `AfterExecution` observe-only?** Letting a hook rewrite raw tool results mid-pipeline invites subtle, hard-to-trace corruption of the data the synthesizer reasons over. Recording and metrics are safe; mutation is intentionally withheld. Shape the *response* in `AfterSynthesis` instead.

**Why metadata auto-promotion *and* hooks?** Two audiences. Simple agents that already hold the context (chat turns, a precomputed snippet) shouldn't have to write a hook — they pass metadata. Agents that must *compute* context (a vector query, a cache lookup) need code, and that's a hook. The two compose: auto-promotion runs first, hooks can override.

---

## 13. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Hook never runs | Not in `deps.PipelineHooks`, or doesn't implement the stage interface you expect | Confirm it's in the slice and that the method signature exactly matches the interface (pointer vs value receiver matters for the type assertion) |
| `AfterPlanning` hook never fires | Request short-circuited, no planner-produced plan was accepted, the request is a HITL resume, or the hook signature does not match | Confirm the request reached planning and look for `pipeline.hook.after_planning.<name>` plus `after_planning_hook` diagnostics |
| Enrichment set but LLM ignores it | Wrong key, or a custom `PromptBuilder` that doesn't read enrichments | Use a well-known key (§5) with the default builder, or have your custom builder call `core.GetPipelineEnrichments(ctx)` |
| Guardrail didn't block streamed text | `AfterSynthesis` runs after tokens stream | Move the check to `BeforePlanning`, a guarded prompt builder, or the non-streaming path |
| Hook error silently swallowed | Fail-open by design — errors are logged at WARN and skipped | Check logs for `"Pipeline hook failed, skipping"`; enforce mandatory logic outside the hook layer |
| Hook ran but its work was overwritten | Another hook later in the slice mutated the same enrichment key or chained response | Reorder the slice; remember `AfterSynthesis` chains and `BeforePlanning` shares one `Enrichments` map |
| Can't find the hook in a trace | Telemetry provider not configured, or hook never matched its stage | Verify telemetry is initialized; look for span `pipeline.hook.<stage>.<name>` |

To verify a hook fired, look in Jaeger for its span (`pipeline.hook.before_planning.<name>`) or grep agent logs for the hook name. The DAG/registry-viewer debug UI surfaces `BeforePlanning` activity under its **Pre-Execution** tab and `AfterSynthesis` activity under **Post-Execution** — see the [Dev Tools Guide](../operations/DEV_TOOLS_GUIDE.md).

That's the whole surface: four stages, one shared context, fail-open semantics. Wire a hook, confirm its span shows up in a trace, and build out from there. Happy hooking!

---

## 14. Further Reading

- [Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) — the scenario cookbook (RAG, semantic cache, conversation history, guardrails) built on these hooks
- [Agent Memory User Guide](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md) — how the built-in memory hooks read and write shared episodic memory
- [Conversation History Guide](../memory-and-chat/CONVERSATION_HISTORY_GUIDE.md) — the metadata path and `ConversationHistoryPreparer` referenced in §5
- [Orchestration Modes Guide](ORCHESTRATION_MODES_GUIDE.md) — how planning, execution, and synthesis fit together
- [Error Handling Guide](ERROR_HANDLING_GUIDE.md) — how hook errors flow through the resilient error-handling system
- [Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md) — each hook gets its own span (e.g. `pipeline.hook.before_planning.rag-retriever`)
- [Dev Tools Guide](../operations/DEV_TOOLS_GUIDE.md) — Pre-Execution / Post-Execution tabs in the execution DAG viewer
- [API Reference](../reference/API_REFERENCE.md) — `OrchestratorDependencies`, `CreateOrchestrator`, and the `PromptBuilder` contract
