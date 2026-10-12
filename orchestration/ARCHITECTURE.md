# TruvaG3 Orchestration Module Architecture

**Version**: 1.47
**Purpose**: Comprehensive architectural documentation for the orchestration module
**Audience**: Core contributors, module developers, system architects, LLM-based coding agents

---

## Table of Contents

1. [Overview](#overview)
2. [Design Philosophy](#design-philosophy)
3. [Core Architecture](#core-architecture)
4. [Dependency Injection Pattern](#dependency-injection-pattern)
5. [Component Architecture](#component-architecture)
6. [Execution Models](#execution-models)
7. [Integration Patterns](#integration-patterns)
8. [Capability Provider Architecture](#capability-provider-architecture)
9. [Resilience & Fault Tolerance](#resilience--fault-tolerance)
   - [LLM Debug Payload Store](#llm-debug-payload-store)
10. [Performance & Scalability](#performance--scalability)
11. [Production Deployment](#production-deployment)
12. [Common Patterns & Examples](#common-patterns--examples)
13. [Troubleshooting Guide](#troubleshooting-guide)
14. [Future Considerations](#future-considerations)
15. [Foundation Lifecycle and Configuration Contracts](#foundation-lifecycle-and-configuration-contracts)
16. [Redis/Valkey Backend Contract](#redisvalkey-backend-contract)
17. [Agent Skills V1](#agent-skills-v1)
18. [Summary](#summary)
19. [Version History](#version-history)

---

## Overview

The orchestration module provides multi-agent coordination with AI-driven orchestration and declarative workflows. It acts as the conductor of the TruvaG3 framework, coordinating multiple agents and tools to accomplish complex tasks.

### Key Capabilities

1. **AI-Driven Orchestration**: Natural language request processing with intelligent routing
2. **Workflow Engine**: Declarative, DAG-based workflow execution
3. **Hybrid Mode**: Combines AI flexibility with workflow predictability
4. **Dynamic Discovery**: Runtime discovery and routing of components
5. **Parallel Execution**: Automatic parallelization based on dependencies
6. **Resilient Design**: Built-in retry, circuit breaker, and fallback mechanisms

### Architectural Position

```
┌──────────────────────────────────────────┐
│            Applications                   │
│  (Wire together modules and components)   │
└──────────────┬───────────────────────────┘
               │
    ┌──────────▼───────────┐
    │    Orchestration      │
    │                       │
    │  • AI Orchestrator    │
    │  • Workflow Engine    │
    │  • Smart Executor     │
    └──────────┬───────────┘
               │
    ┌──────────▼───────────┐
    │      Core Module      │
    │                       │
    │  • Interfaces         │
    │  • Base Types         │
    │  • Discovery           │
    └───────────────────────┘
```

---

## Design Philosophy

### 1. Interface-Based Dependency Injection

**The Principle**: The orchestration module uses interface-based dependencies for most optional modules. Per [FRAMEWORK_DESIGN_PRINCIPLES.md](../FRAMEWORK_DESIGN_PRINCIPLES.md), the valid dependencies are:

- `orchestration` → `core` (required)
- `orchestration` → `telemetry` (allowed for observability)

```go
// ❌ NEVER DO THIS - Would create circular dependency
import "github.com/truvaagents/truva-g3/ai"
import "github.com/truvaagents/truva-g3/resilience"

// ✅ ALLOWED - Per FRAMEWORK_DESIGN_PRINCIPLES.md
import "github.com/truvaagents/truva-g3/core"
import "github.com/truvaagents/truva-g3/telemetry"  // For observability

type AIOrchestrator struct {
    aiClient    core.AIClient    // Interface - injected by application
    discovery   core.Discovery   // Interface - injected by application
}
```

**Rationale**:
1. **Prevents circular dependencies**: `ai` module cannot be imported (would create cycle)
2. **Enables testing**: Can use mocks without importing real modules
3. **Maintains modularity**: Can swap implementations without code changes
4. **Follows SOLID principles**: Dependency Inversion Principle
5. **Telemetry exception**: `telemetry` is allowed because it provides observability infrastructure that all modules need, and it doesn't create circular dependencies

### 2. Explicit Configuration Over Magic

**The Principle**: Configuration is explicit and predictable, with intelligent defaults.

```go
// Explicit dependency injection
deps := OrchestratorDependencies{
    Discovery: discovery,  // Required
    AIClient:  aiClient,   // Required
    Logger:    logger,     // Optional - will create default if nil
    Telemetry: telemetry,  // Optional - will work without it
}

orchestrator, err := CreateOrchestrator(config, deps)
```

### 3. Progressive Enhancement

**The Principle**: Start simple, add complexity as needed.

```go
// Level 1: Zero configuration
orchestrator := CreateSimpleOrchestrator(discovery, aiClient)

// Level 2: With configuration
config := DefaultConfig()
config.CacheEnabled = true
orchestrator := NewAIOrchestrator(config, discovery, aiClient)

// Level 3: Full production setup
deps := OrchestratorDependencies{
    Discovery:      discovery,
    AIClient:       aiClient,
    CircuitBreaker: cb,
    Logger:         logger,
    Telemetry:      telemetry,
}
orchestrator, _ := CreateOrchestrator(config, deps)
```

### 4. Fail-Safe Defaults

**The Principle**: The system should degrade gracefully when optional components are unavailable.

```go
// If telemetry is nil, use NoOp implementation
if o.telemetry == nil {
    o.telemetry = &core.NoOpTelemetry{}
}

// If capability service fails, fall back to default provider
if err != nil && o.config.EnableFallback {
    return o.defaultProvider.GetCapabilities(ctx)
}
```

---

## Core Architecture

### Dependency Flow

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│     core     │     │     core     │     │     core     │
│              │     │              │     │              │
│   Defines:   │     │   Defines:   │     │   Defines:   │
│ - AIClient   │     │ - Discovery  │     │ - Telemetry  │
│ - Logger     │     │ - Registry   │     │ - Span       │
│              │     │ - Episodic   │     │              │
│              │     │   Memory     │     │              │
│              │     │ - Activity   │     │              │
│              │     │   Coordinator│     │              │
└──────▲───────┘     └──────▲───────┘     └──────▲───────┘
       │                    │                    │
       ├────────────────────┼────────────────────┤
       │                    │                    │
┌──────┴───────┐     ┌──────┴───────┐     ┌──────┴───────┐
│      ai      │     │orchestration │     │  telemetry   │
│              │     │              │     │              │
│ Implements:  │     │    Uses:     │     │ Implements:  │
│ - AIClient   │     │ - AIClient   │     │ - Telemetry  │
│ - Embedding  │     │ - Discovery  │     └──────────────┘
│   Client     │     │ - Telemetry  │
└──────────────┘     │ - Logger     │     ┌──────────────┐
                     │ - Pipeline   │     │    memory     │
                     │   Hooks      │     │              │
                     └──────────────┘     │ Implements:  │
                                          │ - Episodic   │
                                          │   Memory     │
                                          │ - Activity   │
                                          │   Coordinator│
                                          │ - DigestCache│
                                          │ - SharedKnow.│
                                          └──────▲───────┘
                                                 │
                                          core + telemetry

Note: Each module imports core + at most telemetry (per FRAMEWORK_DESIGN_PRINCIPLES.md).
Applications wire memory implementations into orchestration hooks at startup.
```

### Module Dependencies

```go
// orchestration/go.mod
module github.com/truvaagents/truva-g3/orchestration

require (
    github.com/truvaagents/truva-g3/core v0.5.0
    github.com/truvaagents/truva-g3/telemetry v0.5.0  // Allowed for observability
    // NO direct imports of ai or resilience modules
)
```

---

## Dependency Injection Pattern

### Why Not Import AI Module Directly?

The orchestration module needs AI capabilities for intelligent routing, but it does NOT import the `ai` module. This is a **critical architectural decision**.

#### The Problem It Solves

```go
// If orchestration imported ai directly:
// orchestration → ai
// ai → orchestration (for orchestrating AI workflows)
// CIRCULAR DEPENDENCY! ❌
```

#### The Solution: Interface-Based DI

```go
// orchestration/orchestrator.go
type AIOrchestrator struct {
    aiClient core.AIClient  // Interface from core
}

func NewAIOrchestrator(config *Config, discovery core.Discovery, aiClient core.AIClient) *AIOrchestrator {
    return &AIOrchestrator{
        aiClient: aiClient,  // Injected, not created
    }
}
```

#### Application Wiring

```go
// main.go - Application layer wires everything together
import (
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/orchestration"
)

func main() {
    // App creates AI client
    aiClient, _ := ai.NewClient(
        ai.WithProvider("openai"),
        ai.WithAPIKey(apiKey),
    )

    // App injects it into orchestrator
    orchestrator := orchestration.CreateSimpleOrchestrator(discovery, aiClient)

    // Orchestration has AI capabilities without importing ai module!
}
```

### Benefits of This Pattern

1. **Testability**
```go
func TestOrchestrator(t *testing.T) {
    // Use mock instead of real AI
    aiClient := &MockAIClient{
        GenerateResponseFunc: func(ctx context.Context, prompt string, opts *AIOptions) (*AIResponse, error) {
            return &AIResponse{Content: "mock response"}, nil
        },
    }

    orchestrator := NewAIOrchestrator(config, discovery, aiClient)
    // Test without real AI calls or importing ai module
}
```

2. **Provider Flexibility**
```go
// Today: OpenAI
aiClient := ai.NewClient(ai.WithProvider("openai"))

// Tomorrow: Custom implementation
aiClient := company.InternalLLMClient()

// Orchestration code unchanged!
orchestrator := orchestration.NewAIOrchestrator(config, discovery, aiClient)
```

3. **Clean Architecture**
- Clear separation of concerns
- No circular dependencies possible
- Each module has single responsibility
- Dependencies flow in one direction: toward core

---

## Component Architecture

### Core Components

```
┌──────────────────────────────────────────────────────────┐
│                      AIOrchestrator                       │
│                                                           │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐   │
│  │   Component  │  │    Smart     │  │    Result    │   │
│  │    Catalog   │  │   Executor   │  │   Processor  │   │
│  └──────────────┘  └──────────────┘  └──────────────┘   │
│                                                           │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐   │
│  │      AI      │  │   Routing    │  │  Capability  │   │
│  │  Synthesizer │  │     Cache    │  │   Provider   │   │
│  └──────────────┘  └──────────────┘  └──────────────┘   │
│                                                           │
│  ┌──────────────┐                                        │
│  │    Metrics   │                                        │
│  │    Tracker   │                                        │
│  └──────────────┘                                        │
└──────────────────────────────────────────────────────────┘
```

#### Component Catalog
- **Purpose**: Maintains registry of available agents and tools
- **Updates**: Refreshes every 10 seconds via discovery
- **Data**: Component names, capabilities, health status, endpoints

#### Smart Executor
- **Purpose**: Executes tool/agent calls with parallelization
- **Features**: Retry logic, timeout handling, error aggregation
- **Optimization**: Automatically detects independent steps for parallel execution

#### AI Synthesizer
- **Purpose**: Combines multiple responses into coherent answer
- **Strategies**: LLM-based, template-based, or simple concatenation
- **Context**: Maintains conversation context for coherent synthesis
- **Input**: Receives pre-processed results from the `ResultProcessor` pipeline

#### Result Processor
- **Purpose**: Pre-processes step results before synthesis to fit within LLM token budgets
- **Interface**: Pluggable via `ResultProcessor` interface; default uses query-conditioned field selection (`StructuralTrimmer`)
- **Budget**: Multi-result scenarios use proportional allocation with redistribution (`BudgetAllocator`)
- **Honesty contract (Phase 16)**: A processor that loses content (drops/truncates/samples) reports it by calling the exported `CaptureResultTrimMetadata(ctx, ResultTrimMetadata{... ContentLost: true})`; the framework prepares the per-step capture slot, and downstream disclosure gating keys exclusively on `ContentLost`. Custom processors are first-class here — the reporting hook is public so an extension can honor the same honesty invariant the built-ins do.

#### Routing Cache
- **Purpose**: Caches routing decisions to reduce LLM calls
- **Types**: Time-based (TTL) or LRU (Least Recently Used)
- **Benefit**: Reduces latency and cost for repeated requests

#### Capability Provider
- **Purpose**: Provides component capabilities to LLM for routing
- **Types**: Default (all capabilities) or Service (filtered/semantic search)
- **Scaling**: Critical for 100s-1000s of agents

### Layered Parameter Resolution

When the Smart Executor runs a multi-step DAG, each step may depend on data produced by previous steps. Parameter resolution is the process of binding output data from completed steps to input parameters of the next step.

**Design Principle**: The framework is **domain-agnostic**. It contains no hardcoded mappings (e.g., "latitude" → "lat", "France" → "EUR"). All semantic understanding is delegated to the LLM. Auto-wiring handles only trivial cases where names already match.

#### Resolution Flow

Layer 0 runs at plan time, before any step dispatches. Layers 1–4 run at step execution time. A dispatch-time guard sits immediately before the HTTP call and fails the step if reserved framework-form `{{step-…}}` tokens survived the runtime layers. Unrelated tool-owned syntax such as `{{now}}` is excluded. Preserving literal strings during JSON parsing does not disable intentional framework template binding at execution time.

```
[Plan validation]   →  Layer 0: Template / Macro / Depends-On Validation  ─  (regenerate on failure)
                                         │
Dependency Results → Layer 1 (Auto-Wire) → Layer 2 (Micro-Resolution) → Resolved Parameters
                                                                              │
                                                           [Pre-dispatch ────┤
                                                            guard]           ▼
                                                                     HTTP Call to Tool
                                                                              │
                                                              (on 4xx error) ─┘
                                                                              │
                                                    Layer 3 (Error Analysis) ─┘
                                                                              │
                                                    Layer 4 (Semantic Retry) ─┘
```

#### Layer 0: Plan-Time Template Validation (No LLM Cost)

Runs once per plan, before the DAG executes, and rejects invalid template references.
Model-plan rejection feeds back into bounded regeneration. Hook-modified plans
instead follow hook validation/failure policy; their rejection does not trigger
model regeneration.

| Check | Purpose |
|---|---|
| Template shape | Framework-form `{{step-…}}` macros must match `{{stepId.fieldPath}}`; unrelated tool-owned templates are left alone |
| Cross-phase awareness | A template reference is valid if the step exists in the current plan **or** in a completed prior phase |
| Template declaration consistency | Every step ID referenced by a template must appear in `depends_on` (same-phase) or `implicit_deps` (prior-phase) |
| Exact prior-phase declarations | `implicit_deps` contains only prior-phase step IDs actually referenced by parameter templates, not historical planning context; surplus declarations reject the plan (model plans may regenerate) |

Freshly generated plans first normalize numeric bracket aliases (such as `items[0]` to `items.0`) and derive their exact prior-phase `implicit_deps` from parameter templates. This is a syntax/declaration correction, not a path-existence waiver: all normal validators still run, surplus declarations still fail, and hook-mutated, caller-supplied, and approved HITL plans are not normalized. The canonical array form remains `{{step-1.response.data.items.0.id}}`; keyed and expression brackets remain invalid.

**Implementation**: `normalizeGeneratedPlan` in `plan_normalization.go`; `validateTemplatePaths`, `validateNoUnknownMacros`, and `validateDependencyConsistency` in `orchestrator.go`. The pre-dispatch guard shown in the diagram above remains the runtime safety net.

At execution, template sources loaded outside `depends_on` retain their existing
`template_auto_includes` metadata, with an additive `reason`: `declared_prior_phase`
for `implicit_deps` sources (Debug), or `undeclared_reference` (existing Warn).
The legacy `orchestration.template.auto_dependency` metric remains a count of
inclusions, not planner errors. A new `orchestration.template.dependency_inclusion`
counter splits the closed reason set without step-ID labels; the same-named span
event carries reason and step/source IDs. Scheduling, admitted dependencies and
successful/non-skipped source requirements are unchanged.

#### Layer 1: Auto-Wiring (No LLM Cost)

Fast, deterministic matching with zero LLM calls. Handles the common case where upstream and downstream tools use compatible naming.

| Strategy | Example | Match Type |
|---|---|---|
| Exact name | `"lat"` → `"lat"` | `exact` |
| Case-insensitive | `"LAT"` → `"lat"` | `case_insensitive` |
| Nested extraction | `{"code":"EUR","name":"Euro"}` → `"EUR"` | `nested_extraction` |
| Type coercion | `"48.85"` (string) → `48.85` (number) | (applied on top) |

**Implementation**: `AutoWirer` in `auto_wire.go`

The executor copies admitted parameters and resolves explicit templates first,
including prior-phase sources. Exact bindings and literal inputs cannot be
overwritten by hybrid mapping. Missing parameters use source-ID-keyed outputs;
conflicting auto-wire matches remain unmapped. Missing-path semantic fallback
uses only that template's named source and records inferred provenance; null
inference remains unresolved rather than becoming an empty string.

Exact whole-value references preserve native objects/arrays. A known
container-to-scalar schema mismatch fails before coercion and dispatch; the
planner can select a scalar field explicitly. Embedded interpolation remains
text. Literal JSON-looking strings stay strings when the destination is a
string. This does not retire semantic `nested_extraction`, numeric/bool coercion,
or unknown-schema behavior. Approved pre-resolved HITL inputs bypass repeat
coercion; the existing snapshot/application-transform ordering is unchanged.
The final dispatch validator rechecks container shape after transforms and
retry corrections. A rejected local binding records zero dispatches.

Failed-operation prompt evidence is a bounded derived view of immutable
`StepDispatch` records, not a rewritten tool result. Each failed attempt carries
its own parameters and structured response code; planned inputs and successful
attempts are excluded. Existing terminal error text remains separate. Built-in
continuation and both synthesis paths use the same projector; custom synthesizers
are unchanged. `FailedOperationViewConfig` provides numeric ceilings, an explicit
disable switch and a typed restrictive `InputPolicy`; policy failure omits that
attempt's inputs. An unknown custom policy disables semantic cache eligibility.
Delegation uses the explicit `ProjectFailedOperationEvidence` application seam,
not automatic cross-agent disclosure. See the Result Compaction Guide for budgets.

#### Layer 2: LLM Micro-Resolution (On-Demand)

When Layer 1 leaves **required** parameters unmapped (names don't match), the `HybridResolver` triggers a lightweight LLM call to infer the correct mapping from available source data.

- Only fires when required params are unmapped (optional params don't trigger it)
- LLM receives: source data keys/values, target parameter schema, step instruction context
- Handles ordinal references ("first company", "second stock") via instruction context
- Auto-wired values take priority — LLM cannot overwrite what was already matched

**Implementation**: `MicroResolver` in `micro_resolver.go`, orchestrated by `HybridResolver` in `hybrid_resolver.go`

#### Layer 3: Error Analysis (Post-Failure)

For an eligible non-transient 4xx error without explicit retry refusal, the
`ErrorAnalyzer` can use an LLM to determine whether parameters can be corrected,
subject to input protections and correction budgets.

- Analyzes error message semantics (e.g., "invalid currency code")
- Determines if the error is fixable (bad params) vs. permanent (resource not found)
- Can suggest corrected parameter values
- Transport failures and HTTP 408/429/5xx bypass all parameter-correction paths;
  existing bounded retry/backoff resends the admitted payload unchanged.

The executor enforces tool-declared correction permission before any analyzer,
contextual retry, or legacy callback. Explicit `retryable:false` forbids both
semantic correction and unchanged executor resends, including for 408, 429 and
5xx responses; omission retains bounded status-based compatibility. Framework Go
tools serialize the zero Boolean as false, so producers must deliberately mark
correctable errors true. Non-transient 4xx responses are never resent unchanged,
and exact-bound/HITL-approved inputs cannot be rewritten by correction. Ordinary
bounded transient retries remain available when not explicitly forbidden; orchestrator delegations remain
single-dispatch. `StepResult.dispatch_count` and `dispatches` expose physical
requests independently of the legacy regular-retry `attempts` counter.

Failed responses retain their structured `tool_error` without promoting it to
successful output. Continuation uses a bounded, explicitly labelled projection;
full errors, dispatch history, and binding provenance remain in execution records.
Template source admission uses `StepResult.Success` and `Skipped`, not a domain
payload's `success` field. Failed, skipped and nil results are not binding sources;
missing-parameter inference considers required fields only.

**Implementation**: `ErrorAnalyzer` in `error_analyzer.go`

#### Layer 4: Contextual Re-Resolution (Semantic Retry)

When Layer 3 cannot fix the error (lacks source data context), the `ContextualReResolver` attempts recovery using the **full execution trajectory**: user query, source data from dependencies, attempted parameters, error response, and capability schema.

- Computes derived values (e.g., `100 shares × $468.28 = $46,828`)
- Re-interprets source data given the error context
- Configurable via `TRUVAG3_SEMANTIC_RETRY_ENABLED` and `TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS`
- Optionally runs for independent steps (no dependencies) via `TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS`

**Implementation**: `ContextualReResolver` in `contextual_re_resolver.go`

#### Layer 5: Human-in-the-Loop (HITL)

When a step reaches a HITL checkpoint, resolved parameters are presented for
approval. The default controller supports approve/reject/abort, not
edit/skip/retry/respond. The stored approved parameters bypass other resolution
layers during resume.

**Implementation**: `InterruptController` in `interrupt_controller.go`

#### Design Rationale

| Decision | Rationale |
|---|---|
| No semantic aliases | Framework must handle novel domains without code changes |
| Auto-wire first | Avoids unnecessary LLM calls for 80%+ of cases |
| LLM for semantics | Only the LLM can infer "France" → "EUR" without hardcoding |
| Auto-wire priority | Prevents LLM hallucination from overwriting exact matches |
| Per-layer timing | Enables cost analysis (auto-wire: µs, LLM: ms) |

---

## Execution Models

Before each tool/agent dispatch, `validateDispatchParameters` checks declared
required inputs, unresolved reserved references, enum membership and supported
container/scalar shape mismatches. This is generic schema validation, not domain
validation or permission to invent missing values. Corrections are validated
again before resend. Explicit retry refusal, approved bindings and capability
type still constrain the retry path.

### Conversation correlation ingress

`resolveConversationContext` is the single orchestration-ingress authority for
canonical multi-turn correlation. It applies this presence-aware precedence:

1. `metadata[MetadataConversationID]`
2. the effective core candidate (`X-TruvaG3-Conversation-ID` before
   programmatic `core.WithConversationID`)
3. W3C baggage member `conversation_id`

The resolver first clones metadata and removes inherited conversation identity
from core context and baggage. A present higher-precedence candidate therefore
blocks all lower-precedence candidates even when it is invalid. Valid identity
is promoted atomically to sanitized request metadata, core context, and exact
W3C baggage. The baggage member is marked metric-ineligible. Rejection is
fail-open for business execution: it records only a bounded diagnostic reason,
does not mark a span failed, does not expose the raw value, and leaves no
conversation identity in the resulting request context.

`MetadataConversationID` is canonical correlation metadata and is independent
of `MetadataConversationTurns`. Producers must set it whenever an application
has a conversation identity, including the first turn and delegation paths
with no stored history. `session_id` remains application metadata; an example
may map a session UUID to the canonical conversation ID, but the framework
does not assign session semantics.

Both structured-turn and formatted-history compaction caches use the resolved
conversation ID. Changing that identifier during an in-flight conversation
causes a cache miss and re-compaction; it never joins content from two
conversations.

### 1. AI-Driven Orchestration

```
Request → [PrepareKnownEnrichments] → [BeforePlanningHooks] → Understanding → Discovery → Planning → [AfterPlanningHooks] → Execution → [AfterExecutionHooks] → Result Processing → Synthesis → [AfterSynthesisHooks] → Response
```

> 📖 **Plan Structure Details**: For the complete JSON plan structure, DAG visualization, and Jaeger tracing guide, see [LLM-Generated Execution Plan Structure](README.md#llm-execution-plan).

#### Pipeline Hooks

Pipeline hooks (`core.PipelineHook`) provide per-stage middleware for context
engineering. Hooks are registered via
`OrchestratorDependencies.PipelineHooks` in the factory and run sequentially at
each stage. Ordinary hooks retain their documented fail-open fallback. An
`AfterPlanningHook` that also implements `RequiredAfterPlanningHook` is a
fail-closed execution invariant: a callback error, wrong return type, clone
failure, or invalid mutation aborts before phase persistence, HITL, or tool
execution. Required intent is independent of the stage interface, so factory
construction rejects a required marker that no longer implements
`AfterPlanningHook`. At most one required after-planning hook may be registered,
and it must be the final hook participating in that stage; applications compose
multiple mandatory checks behind that one terminal boundary.

On returned required-hook failure, the phase loop returns accumulated diagnostic
state alongside the typed error. With execution recording enabled, the existing
request lifecycle records a non-nil unsuccessful result, preserving admitted
phases and completed tool results while excluding the rejected candidate. The
existing retention selector therefore applies error retention, including to
later effect-driven rewrites.
This does not synthesize a response, alter optional-hook retention, or introduce
another recorder or storage protocol.

Before hooks run, orchestration also performs a shared enrichment-preparation pass for known metadata inputs. This is how raw conversation-turn metadata is normalized into the `conversation_history` enrichment on the primary chat-agent path without requiring a dedicated hook. The conversation-history preparer always performs a token-aware **preparation** step (`conversation_history_prepare`, recorded as a logical pre-execution interaction when LLM debug is enabled). When Tier 2 recursive compaction is configured, that same stage may additionally emit `conversation_history_compaction` as the optional LLM-backed extension.

Available hook stages:
- **BeforePlanningHook** — Runs before the phase loop. Can inject enrichments (RAG context, conversation history, agent memory, activity coordination) into `PipelineContext.Enrichments`, or return a `PipelineShortCircuit` to skip the entire pipeline (e.g., semantic cache hit).
- **AfterPlanningHook** — Runs once for each final validated planner-produced
  phase plan, before HITL persistence and execution. Mutations are applied to a
  copy and fully revalidated. An ordinary hook's invalid mutation is rejected
  while the last valid plan is retained; a required hook's rejection stops the
  phase. HITL resume does not rerun this boundary.
- **AfterExecutionHook** — Runs after tool execution completes.
- **AfterSynthesisHook** — Runs after synthesis. Can mutate the final response.

Enrichments prepared from metadata and enrichments set by hooks propagate via `core.WithPipelineEnrichments(ctx)` and are available to the prompt builder via `PromptInput.Metadata`.

**Built-in hooks** (from the orchestration module):

| Order | Hook | Stage | Enrichment Key |
|-------|------|-------|---------------|
| 1 | `ActivityAnnouncementHook` | BeforePlanning | `activity_coordination` |
| 2 | `ConversationHistoryHook` (optional adapter) | BeforePlanning | `conversation_history` |
| 3 | `MemoryEnrichmentHook` | BeforePlanning | `rag_context` |
| 4 | `MemoryRecordHook` | AfterExecution | — (writes events) |
| 5 | `KnowledgeExtractionHook` | AfterSynthesis | — (extracts knowledge) |
| 6 | `ActivityCleanupHook` | AfterSynthesis | — (removes signal) |

#### Request Processing Pipeline

```go
func (o *AIOrchestrator) ProcessRequest(ctx context.Context, request string) (*Response, error) {
    // 0. Prepare known enrichments from metadata (shared framework ingress path)
    pctx := &core.PipelineContext{Request: request, Metadata: metadata, Enrichments: map[string]interface{}{}}
    prepareKnownEnrichments(ctx, metadata, pctx.Enrichments, o.conversationHistoryPreparer)

    // 1. Pipeline hooks: before planning (context engineering)
    if shortCircuit := o.runBeforePlanningHooks(ctx, pctx); shortCircuit != nil {
        return shortCircuitResponse, nil
    }
    ctx = core.WithPipelineEnrichments(ctx, pctx.Enrichments)

    // Prepared enrichments are reused across the full request, including any
    // continuation phases in iterative planning.

    // 2. Understanding: Extract intent
    intent := o.extractIntent(request)

    // 3. Discovery: Find available components
    components := o.catalog.GetComponents()

    // 4. Planning: Generate execution plan (PromptInput.Metadata receives enrichments)
    plan, err := o.generateExecutionPlan(ctx, request)

    // 5. Execution: Run plan with parallelization
    results, err := o.executor.Execute(ctx, plan)

    // 6. Pipeline hooks: after execution
    o.runAfterExecutionHooks(ctx, pctx, results)

    // 7. Result Processing: Trim results to fit LLM token budget
    processed := o.processResults(ctx, results, request)

    // 8. Synthesis: Combine results
    response := o.synthesizer.Synthesize(processed)

    // 9. Pipeline hooks: after synthesis
    response = o.runAfterSynthesisHooks(ctx, pctx, response)

    return response, nil
}
```

#### LLM Prompt Structure

The planning prompt uses XML-tagged sections for clear boundaries and cross-provider compatibility.
Persona/system instructions are delivered via a **separate system message** (not embedded in the user prompt),
following the `SystemPromptBuilder` interface pattern.

```go
// System message (via SystemPromptBuilder or config.SystemInstructions):
// "You are an intelligent orchestrator that creates execution plans for multi-agent systems."

// User prompt structure (XML-tagged sections):
// <instructions>        — Numbered rules (1-6) + iterative planning guidance
// <example>             — Concrete few-shot JSON plan example
// <type_rules>          — Parameter type → JSON type mapping
// <domain_rules>        — Domain-specific constraints (healthcare, finance, legal)
// <custom_instructions> — User-provided additional rules
// <available_agents>    — Capability catalog from discovery
// <agent_coordination>  — Real-time activity signals from other agents (from ActivityAnnouncementHook)
// <agent_memory>        — Cross-agent shared memory context (from MemoryEnrichmentHook)
// <conversation_history> — Session history prepared from metadata or the optional ConversationHistoryHook
// <user_request>        — The natural language request
// Final line: "Return a JSON execution plan. Start with { and end with }."
```

The prompt builder is pluggable via the `PromptBuilder` interface:
- **`DefaultPromptBuilder`**: XML-tagged sections with concrete examples (default)
- **`TemplatePromptBuilder`**: Go `text/template` for full structural customization

> 📖 **For template variables, customization guide, and cross-provider tips, see [LLM_PLANNING_PROMPT_GUIDE.md](../docs/orchestration/LLM_PLANNING_PROMPT_GUIDE.md).**

### 1b. Multi-Phase Iterative Planning (AI-Driven Extension)

When a single planning pass cannot produce a complete DAG (e.g., the LLM needs intermediate results before planning the next steps), the orchestrator supports **multi-phase iterative planning**. This extends the AI-driven model with a plan-execute-continue loop.

```
Request → Plan (Phase 1) → HITL → Execute → [terminal?]
                                                  │ no
                                        Continuation Plan (Phase 2) → HITL → Execute → [terminal?]
                                                                                            │ no
                                                                                  ... (up to MaxPhases)
                                                                                            │ yes/forced
                                                                                       Synthesis → Response
```

#### How It Works

The LLM planner can set `terminal: false` on a `RoutingPlan` to signal that the plan is **partial** — it covers the steps that can be planned now, but more steps will be needed after seeing the results.

Terminal-planning guidance is based on task completeness, not whether entity
names or parameter shapes are already known. Use a non-terminal phase when
interpreting its results determines whether further investigation or action is
needed, including for already-known entities. Keep predictable, template-linked
actions together; a terminal plan is appropriate when those actions are expected
to satisfy the objective and only answer composition will remain. When no
available action can resolve an uncertainty, report the limitation rather than
continuing without useful work. Initial and continuation planning share these
instructions. This is planner guidance, not a post-execution completion check
or a guarantee of model reasoning correctness; runtime termination and safety
limits are unchanged.

```go
// RoutingPlan with iterative planning fields
type RoutingPlan struct {
    Steps            []RoutingStep `json:"steps"`
    Terminal         *bool         `json:"terminal,omitempty"`          // nil=terminal (backward compat)
    ContinuationNote string       `json:"continuation_note,omitempty"` // Why continuation is needed
    PhaseNumber      int          `json:"phase_number,omitempty"`      // Set by orchestrator (1-indexed)
}

// IsTerminal defaults to true for backward compatibility
func (p *RoutingPlan) IsTerminal() bool {
    if p.Terminal == nil { return true }
    return *p.Terminal
}
```

The orchestrator's `executePhaseLoop()` method drives the cycle:

1. **Generate plan** (or use the initial plan for Phase 1)
2. **Validate** step ID uniqueness across all phases
3. **HITL checkpoint** — if enabled, the plan goes through human approval
4. **Execute** the phase's DAG
5. **Check termination** — if `terminal: true` or safety limits reached, break. **Exception:** if the just-finished phase had one or more steps skipped because their template-referenced dependencies failed (`blocking_reason=template_induced`), the terminal decision is overridden once per orchestration (`remediationAttempted` guard). The next phase is forced to run even if the LLM marked the current plan terminal, so the planner can adapt instead of producing a dead-end result. See `decideRemediation` in `executor.go`.
6. **Clear resume mode** — Phase 2+ plans must go through independent HITL evaluation
7. **Generate continuation plan** — feed completed results back to the LLM. When remediation was triggered in step 5, the continuation prompt carries a slim note describing the skipped steps and offering two options: (a) propose a materially different approach, (b) return `{"terminal": true, "steps": []}` so the synthesizer tells the user the upstream is unavailable. When the causal-failure set shows a strictly-majority shared error, the prompt is additionally prefixed with a one-line upstream-failure-pattern summary (N of M prior steps failed with the same error, retries exhausted, optional agent/capability attribution) so the planner has concrete evidence to pick between options (a) and (b). See `summarizeUpstreamFailurePattern` in `executor.go`.
8. **Repeat** from step 2

#### Safety Limits

```go
type IterativePlanConfig struct {
    Enabled                      bool          // Default: true
    MaxPhases                    int           // Default: 20
    MaxTotalSteps                int           // Default: 200
    PhaseTimeout                 time.Duration // Default: 25m per phase
    MaxValidationRounds          int           // Default: 4 — regenerations after first validation failure
    RegenerationFeedbackMaxBytes int           // Default: 16 KiB of added correction context
}
```

When safety limits are reached without a terminal plan, the orchestrator forces termination (`ForcedTerminal: true`) and synthesizes with all results collected so far.

Plan parsing preserves application-owned strings byte-for-byte after JSON
decoding, including source code, Markdown and embedded fences. Valid JSON is
passed directly to typed plan decoding; a type mismatch does not trigger syntax
repair. Supported outer prose/Markdown envelopes are recovered with string-aware
object matching. Unquoted-template and trailing-comma recovery modify only
structure outside strings; they do not remove Markdown or rewrite payloads.
Dependency, schema, hook and dispatch validation still apply to recovered plans.
The parse-retry prompt constrains the outer JSON envelope while allowing code and
Markdown within escaped string values. See `plan_json.go` and `parsePlan`.

Model responses additionally pass the model-only intent decoder in
`plan_envelope.go`. Nonempty steps preserve omitted-terminal compatibility;
zero-work model responses require explicit `terminal:true` or a clarification
object with a nonblank `question`. A present clarification object with a blank
or missing question is rejected even alongside steps or terminal intent because
the phase loop gives clarification priority. Question contents are preserved;
whitespace is inspected only to recognize intent. Null controls alone, identity-only JSON,
unknown-only envelopes and null roots do not establish intent. Known-field type
errors remain typed errors; additive unknown fields on recognized plans remain
compatible. Structural recovery passes through the same check before generated
fields and terminal-synthesis normalization. Typed/direct plans, refinement and
HITL resume retain their existing validation/terminal semantics.

Envelope correction uses the existing regeneration feedback byte budget,
JSON-encoded sorted top-level keys and an omitted-key count. Field values are
excluded from this diagnostic; opted-in raw debug recording remains unchanged.
Existing initial/continuation parse-error events add the bounded `reason` field:
`unrecognized_plan_envelope`, `missing_explicit_plan_intent`,
`invalid_clarification_request` or `plan_decode_failed`.
No new attempts, selection pass or executor behavior is introduced.

The default initial prompt builder, fallback/continuation format rules and
agent-name retry guidance show
`{"terminal": true, "steps": []}` for requests that available capabilities cannot
fulfill, including when iterative planning is disabled. Both active and legacy
iterative instruction text limit omitted-terminal compatibility to nonempty
model plans. This aligns guidance with the existing decoder; it does not change
typed/direct-plan terminal defaults, hallucination retry budgets or dispatch.

In initial planning, if a replacement produced during unknown-agent recovery fails plan decoding,
the next recovery attempt uses that latest parse/intent error and reuses the
selected catalog and its system prompt. It spends the existing
`HallucinationMaxRetries` budget; it does not start a separate parse-retry budget
or reselect tools for a syntax correction. A valid replacement clears the parse
failure and still passes agent and subsequent phase validation. Exhaustion
preserves the latest typed parse error, rather than reporting the earlier
unavailable agent. Provider/cancellation errors retain their immediate return
path. The phase loop's existing fallback regeneration still applies and receives
the actual generation failure; the inner limit is not a whole-request call cap.
Continuation unknown-agent recovery instead makes one regeneration attempt and
returns the original unknown-agent error if that attempt fails. It has no
initial-style replacement-parse loop. The outer phase loop may still attempt
fallback regeneration, subject to its context and preparation gates.
Parse failures use `llm.plan_generation.parse_error` with
`retry_context=hallucination_recovery`. Correction and exhaustion emit
`llm.plan_generation.retry_parse_correction` and
`llm.plan_generation.retry_parse_exhausted`; the latter has a corresponding
`plan_generation.retry_parse_exhausted` counter with a bounded `reason` label.
These are deterministic diagnostics, not additional model interactions.

Each `hallucination_detection` debug record carries the effective prompt, system
prompt and raw response of the attempt whose plan was rejected. Its allowed-agent
diagnostics reflect that attempt's validated catalog, including after selection
refresh or an intervening parse correction. This request-local evidence does not
alter correction prompts or retry decisions. Detection records remain diagnostics
without duplicated provider identity, token usage or call duration.

Validation failure is different: the phase fails without executing the rejected
candidate. `MaxValidationRounds` bounds regeneration attempts after the first
validation failure. Malformed JSON returned by regeneration consumes an attempt
from this same budget and its parse error is fed back to the planner. A recovered
candidate restarts normalization and the full validation gauntlet before hooks,
HITL, or execution. Provider, boundary-preparation, and cancellation errors are
not retried by this loop. Initial-generation retry policies and HITL checkpoint
resume behavior are unchanged. Parse failures emit
`orchestrator.plan_validation.regeneration_parse_error` span events. A regenerated
continuation that still conflicts with executed step IDs returns early; the
round limit is a ceiling, not a guaranteed number of model calls. Exhaustion
uses the existing validation-exhaustion event and counter. Structural corrections
reuse the phase's selected capability prompt and add bounded, encoded feedback
containing the rejected candidate and distinct errors. A new phase selects again;
a genuine discovery miss may also refresh selection. Normalization and selection
reuse emit span events without inventing LLM calls. On exhaustion, the request
still fails, but its returned and stored execution retains admitted steps plus
`termination_reason=plan_validation_exhausted` and `execution_request_id` metadata.

The phase loop also detects a dependency-only fixed point: if consecutive
non-empty phases contain only unsuccessful dependency-skipped steps rooted in
the same failed step IDs, a non-terminal continuation is stopped. The first
blocked phase allows the planner to adapt; actual attempted work, including
polling and unsuccessful retries, resets this check. This is a structural
no-progress invariant, not a comparison of domain responses or a prescribed
recovery workflow. Cyclic or incomplete dependency evidence is ineligible.
The result retains every admitted plan and step, sets `ForcedTerminal`, and
records `termination_reason=repeated_failed_dependencies` in result metadata.
Existing `ExecutionResult.Success` semantics are unchanged.

Dependency skips carry `Skipped=true`, `Success=false`, capability, instruction,
zero attempts, and the exact skip reason. Their original inputs are retained
under `Metadata.planned_parameters`; `Parameters` remains unset because no
request was dispatched. Legacy/resumed plans still receive fail-closed
dependency checks even if their declarations predate the stricter validator.

`MemoryRecordHook` excludes dependency skips from LLM event summarization and
episodic recording, just as it excludes refinement skips: neither dispatched
an action. Actual failed attempts remain eligible for recording. Causal links,
planned inputs, and skip reasons remain in execution evidence; existing
episodic records are not deleted. Registry Viewer classifies unsuccessful
dependency skips as skipped but preserves successful refinement-satisfied
steps as completed, including when displaying older stored executions.

#### Phase Number Propagation

The phase number flows across module boundaries for debug attribution:

```
Orchestrator (sets phase_number in OTel baggage)
    → Executor (reads baggage, sets X-TruvaG3-Phase-Number header)
        → Agent HTTP handler (core.ExtractRequestContext reads header)
            → ai.InstrumentedAIClient (resolvePhaseNumber from context/baggage)
                → telemetry.LLMCallRecord.PhaseNumber
```

This enables the registry-viewer to show which phase each LLM call belongs to.

#### Configuration

```bash
TRUVAG3_ITERATIVE_PLANNING_ENABLED=true   # Default: true
TRUVAG3_ITERATIVE_MAX_PHASES=20           # Default: 20
TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS=200     # Default: 200
TRUVAG3_ITERATIVE_PHASE_TIMEOUT=25m       # Default: 25m
TRUVAG3_ITERATIVE_MAX_VALIDATION_ROUNDS=4 # Default: 4
```

#### Tiered Approach to Iterative Planning

The iterative planning system takes a **tiered approach** across phases:

1. **Phase 1 (Discovery):** Standard tiered tool selection → LLM generates a partial plan (`terminal: false`) for the steps it can plan now
2. **Phase 2+ (Action):** Context-aware tiered tool re-selection (see [Tiered Capability Provider](#3-tiered-capability-provider-20-100-tools---default)) → continuation plan generation with prior results fed back → execution
3. **Termination:** When the LLM sets `terminal: true` or safety limits are reached, synthesis aggregates results from all phases

This tiered approach allows the LLM to progressively refine its tool selection as new information becomes available — a "discover then act" pattern where Phase 1 might use search/discovery tools and Phase 2 uses action/data tools based on what was found.

#### Backward Compatibility

- Plans without the `terminal` field (`nil`) are treated as terminal — existing single-phase flows are unaffected
- The `PhaseNumber` field uses `omitempty` — absent in single-phase JSON
- `IterativePlanConfig.Enabled = false` disables the feature entirely, treating all plans as terminal

### 2. Workflow Engine

```
Workflow → Parse → Build DAG → Schedule → Execute → Collect → Response
```

#### DAG Execution

```go
type WorkflowEngine struct {
    discovery   core.Discovery
    scheduler   *DAGScheduler
    executor    *StepExecutor
    state       *StateManager
}

func (e *WorkflowEngine) ExecuteWorkflow(ctx context.Context, workflow *WorkflowDef, inputs map[string]interface{}) (*ExecutionResult, error) {
    // 1. Parse workflow and build DAG
    dag := e.buildDAG(workflow)

    // 2. Initialize execution state
    state := e.state.Initialize(workflow.Name, inputs)

    // 3. Schedule and execute steps
    for !dag.IsComplete() {
        // Get ready steps (no pending dependencies)
        readySteps := dag.GetReadySteps()

        // Execute in parallel
        results := e.executeParallel(ctx, readySteps, state)

        // Update state and DAG
        state.UpdateSteps(results)
        dag.MarkComplete(readySteps)
    }

    return state.GetResult(), nil
}
```

#### Variable Substitution

```go
func (e *WorkflowEngine) substituteVariables(template string, state *ExecutionState) string {
    // ${inputs.fieldName} - Input parameters
    // ${steps.stepName.output} - Step outputs
    // ${steps.stepName.output.field} - Specific fields

    return variableRegex.ReplaceAllStringFunc(template, func(match string) string {
        path := extractPath(match)
        value := state.GetValue(path)
        return fmt.Sprintf("%v", value)
    })
}
```

### 3. Hybrid Mode

Combines AI flexibility with workflow predictability:

```yaml
# Workflow with AI-driven steps
name: hybrid-analysis
steps:
  - name: understand-request
    type: ai-routing  # AI decides which components
    prompt: "Analyze the user's request and determine data sources needed"

  - name: gather-data
    type: workflow    # Fixed workflow steps
    parallel:
      - tool: market-data
      - tool: news-feed

  - name: analyze
    type: ai-routing  # AI chooses analysis approach
    inputs: ${steps.gather-data.output}
```

---

## Integration Patterns

### Pattern 1: Tool Integration

Tools are passive components that respond to requests:

```go
// Tool implementation
type WeatherTool struct {
    *core.BaseTool
    apiClient *WeatherAPIClient
}

func (t *WeatherTool) GetCapabilities() []core.Capability {
    return []core.Capability{
        {
            Name:        "get_weather",
            Description: "Get current weather for a location",
            Parameters: map[string]interface{}{
                "location": "string",
            },
        },
    }
}

// Orchestration uses the tool
plan := &RoutingPlan{
    Steps: []PlanStep{
        {
            Name:      "weather",
            Component: "weather-tool",
            Action:    "get_weather",
            Inputs:    map[string]interface{}{"location": "NYC"},
        },
    },
}
```

### Pattern 2: Agent Integration

Agents are active components that can orchestrate others:

```go
// Agent can discover and coordinate
type ResearchAgent struct {
    *core.BaseAgent
    discovery core.Discovery
}

func (a *ResearchAgent) ProcessRequest(ctx context.Context, request string) (*Response, error) {
    // Agent can discover other components
    tools, _ := a.discovery.FindByCapability(ctx, "data_gathering")

    // Agent orchestrates multiple tools
    for _, tool := range tools {
        // Call tools in sequence or parallel
    }

    return response, nil
}

// Orchestration delegates to agent
plan := &RoutingPlan{
    Steps: []PlanStep{
        {
            Name:      "research",
            Component: "research-agent",
            Action:    "comprehensive_analysis",
            Inputs:    map[string]interface{}{"topic": "Tesla"},
        },
    },
}
```

### Pattern 3: Service Integration

External services via HTTP/gRPC:

```go
// Capability service integration
type ServiceCapabilityProvider struct {
    endpoint string
    client   *http.Client
}

func (p *ServiceCapabilityProvider) GetCapabilities(ctx context.Context, request string) ([]Capability, error) {
    // Query external service for relevant capabilities
    resp, err := p.client.Post(p.endpoint+"/search", "application/json",
        bytes.NewBuffer([]byte(`{"query": "`+request+`"}`)))

    // Return filtered capabilities
    return capabilities, nil
}
```

---

## Capability Provider Architecture

### Built-in catalog rendering contract

Full and tier-selected planner catalogs render declared return fields as compact
`name:type` hints even when `Returns.Type` is empty. Return-field descriptions and
examples stay in metadata but are not rendered by these builders. Input parameter
descriptions are rendered; input examples are not. Custom builders can choose a
different view. Template validation uses declared output field names, so planner
visibility and the underlying schema are distinct contracts.

### Scaling Challenge

At scale (100s-1000s of agents), sending all capabilities to LLM causes:
- Token limit overflow
- Increased costs
- Slower responses

### Solution: Capability Provider Pattern

```
┌─────────────────────────────────────────────────┐
│                  Orchestrator                    │
│                                                  │
│  ┌────────────┐        ┌────────────────────┐   │
│  │   Request  │───────▶│ Capability Provider│   │
│  └────────────┘        └────────────────────┘   │
│                               │                  │
│                               ▼                  │
│                    ┌─────────────────────┐      │
│                    │  Filtered/Relevant  │      │
│                    │    Capabilities     │      │
│                    └─────────────────────┘      │
│                               │                  │
│                               ▼                  │
│                    ┌─────────────────────┐      │
│                    │      LLM Router     │      │
│                    └─────────────────────┘      │
└─────────────────────────────────────────────────┘
```

### Provider Types

#### 1. Default Provider (< 200 agents)
```go
type DefaultCapabilityProvider struct {
    catalog *AgentCatalog
}

func (p *DefaultCapabilityProvider) GetCapabilities(ctx context.Context, request string) ([]Capability, error) {
    // Return ALL capabilities
    return p.catalog.GetAllCapabilities(), nil
}
```

#### 2. Service Provider (100s-1000s agents)
```go
type ServiceCapabilityProvider struct {
    endpoint       string
    topK           int
    threshold      float64
    circuitBreaker core.CircuitBreaker
}

func (p *ServiceCapabilityProvider) GetCapabilities(ctx context.Context, request string) ([]Capability, error) {
    // Use circuit breaker for resilience
    result, err := p.circuitBreaker.Execute(func() (interface{}, error) {
        // Semantic search for relevant capabilities
        return p.queryService(ctx, request, p.topK, p.threshold)
    })

    if err != nil && p.fallback != nil {
        // Fall back to default provider
        return p.fallback.GetCapabilities(ctx, request)
    }

    return result.([]Capability), nil
}
```

#### 3. Tiered Capability Provider (20-100 tools) - Default

The `TieredCapabilityProvider` implements a research-backed 2-phase capability resolution strategy that reduces LLM token usage by 50-75% for medium-scale deployments.

```
┌─────────────────────────────────────────────────────────────────┐
│                      User Request                                │
└───────────────────────────┬─────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  Tier 1: Tool Selection (Lightweight)                           │
│  • Send only: tool names + 1-sentence summaries                 │
│  • ~50-100 tokens per tool (vs 200-500 for full schema)         │
│  • Output: JSON array of needed tool names                      │
└───────────────────────────┬─────────────────────────────────────┘
                            │ ["weather-tool", "currency-tool"]
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  Tier 2: Schema Retrieval (Targeted)                            │
│  • Fetch full capability schemas ONLY for selected tools        │
│  • No LLM call - just catalog lookup                            │
└───────────────────────────┬─────────────────────────────────────┘
                            │ Full schemas for selected tools
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  Tier 3: Plan Generation (Existing Logic)                       │
│  • Generate execution plan with full parameter details          │
│  • Reduced context = better accuracy                            │
└─────────────────────────────────────────────────────────────────┘
```

**Key Design Decisions:**
- **Enabled by default**: Research shows accuracy degrades beyond ~20 tools ("Less is More", Nov 2024)
- **Graceful fallback**: On Tier 1 failure, falls back to sending all tools (safe degradation)
- **Hallucination filtering**: Validates LLM-selected tools exist in catalog before proceeding
- **No external dependencies**: Works without RAG service or vector database
- **CustomInstructions-aware**: Injects `PromptConfig.CustomInstructions` into selection prompts so domain-required tools are not filtered out

```go
type TieredCapabilityProvider struct {
    catalog            *AgentCatalog
    aiClient           core.AIClient
    MinToolsForTiering int      // Default: 20
    customInstructions []string // Domain rules from PromptConfig

    // Optional dependencies (injected)
    logger     core.Logger
    telemetry  core.Telemetry
    debugStore LLMDebugStore
}

func (t *TieredCapabilityProvider) GetCapabilities(ctx context.Context, request string, metadata map[string]interface{}) (string, error) {
    summaries := t.catalog.GetCapabilitySummaries()

    // Below threshold - use direct approach
    if len(summaries) < t.MinToolsForTiering {
        return t.catalog.FormatForLLM(), nil
    }

    // Tier 1: Select relevant tools (phase-aware for iterative planning)
    selectedTools, err := t.selectRelevantTools(ctx, request, summaries, metadata)
    if err != nil {
        // Graceful fallback to all tools
        return t.catalog.FormatForLLM(), nil
    }

    // Tier 2: Get full schemas for selected tools only
    return t.catalog.FormatToolsForLLM(selectedTools), nil
}
```

#### Context-Aware Phase 2+ Re-Selection

During multi-phase iterative planning, the tool landscape may change between phases — Phase 1 might discover destinations, while Phase 2 needs weather and currency tools. The tiered provider detects phase context via `PhaseContextKey` constants and builds a **continuation selection prompt** that includes:

- **Prior tools used** — avoids redundant re-selection of already-used tools
- **Continuation note** — the LLM planner's stated reason for continuation
- **Compact result summary** — abbreviated Phase 1 results for informed tool discovery

```go
// Phase context keys (compile-time safe contract between producer and consumer)
const (
    PhaseContextKeyPhaseNumber      PhaseContextKey = "phase_number"
    PhaseContextKeyContinuationNote PhaseContextKey = "continuation_note"
    PhaseContextKeyPriorToolsUsed   PhaseContextKey = "prior_tools_used"
    PhaseContextKeyCompletedSummary PhaseContextKey = "completed_summary"
)
```

For Phase 1, the standard selection prompt is used. For Phase 2+, `buildContinuationSelectionPrompt()` constructs a context-enriched prompt that enables the LLM to select **different** tools than Phase 1 while avoiding hallucinated tool names (validated against the catalog).

**Observability:** Phase-aware selection is instrumented with:
- `orchestration.tiered_selection_count` counter with `context_aware` attribute
- `phase_context` and `phase_comparison` spans for tracing tool selection drift across phases

**Configuration:**
```go
config := orchestration.DefaultConfig()
config.EnableTieredResolution = true  // Default
config.TieredResolution = orchestration.TieredCapabilityConfig{
    MinToolsForTiering: 20,  // Threshold based on research
}
```

**Environment Variables:**
```bash
TRUVAG3_TIERED_RESOLUTION_ENABLED=true   # Default
TRUVAG3_TIERED_MIN_TOOLS=20              # Research-backed default
```

### Auto-Configuration

```go
func (c *OrchestratorConfig) AutoConfigure() {
    // Check environment for capability service
    if endpoint := os.Getenv("TRUVAG3_CAPABILITY_SERVICE_URL"); endpoint != "" {
        c.CapabilityProviderType = "service"
        c.CapabilityService.Endpoint = endpoint

        // Smart defaults
        if c.CapabilityService.TopK == 0 {
            c.CapabilityService.TopK = 20
        }
        if c.CapabilityService.Threshold == 0 {
            c.CapabilityService.Threshold = 0.7
        }

        // Enable fallback for production resilience
        c.EnableFallback = true
    }
}
```

---

## Resilience & Fault Tolerance

### Design Philosophy & Goals

The resilience architecture for the orchestration module's capability provider system is designed to handle external service failures gracefully while maintaining framework architectural principles.

#### Design Goals

1. **Framework Compliance**: Respect module dependency rules (orchestration → core + telemetry only)
2. **Extensibility**: Allow sophisticated resilience patterns without hard dependencies
3. **Progressive Enhancement**: Work with zero configuration, enhance when needed
4. **Production Ready**: Support best-practice resilience requirements
5. **Pattern Consistency**: Follow established patterns from other modules (UI)

### Three-Layer Resilience Architecture

The design provides three layers of resilience, each building on the previous:

#### Layer 1: Simple Built-in Resilience (Always Active)
- 3 retries with exponential backoff
- Simple failure tracking (5 failures → 30s cooldown)
- Timeout protection (30s default)
- No external dependencies
- Works out of the box with zero configuration

#### Layer 2: Circuit Breaker (Optional, Injected)
- Full circuit breaker pattern (closed/open/half-open states)
- Sliding window metrics
- Configurable thresholds and recovery
- Provided by application, not framework
- Injected via dependency injection

#### Layer 3: Fallback Provider (Configurable)
- Falls back to DefaultCapabilityProvider on failure
- Ensures system continues working
- Enabled by default with service provider
- Graceful degradation under failures

### Dependency Injection Pattern for Resilience

Following the UI module's proven pattern, we use dependency injection for optional resilience features:

```go
// ServiceCapabilityConfig accepts optional dependencies
type ServiceCapabilityConfig struct {
    // Required configuration
    Endpoint  string
    TopK      int
    Threshold float64
    Timeout   time.Duration

    // Optional dependencies (injected)
    CircuitBreaker   core.CircuitBreaker  `json:"-"`
    Logger           core.Logger          `json:"-"`
    Telemetry        core.Telemetry       `json:"-"`
    FallbackProvider CapabilityProvider   `json:"-"`
}
```

### Application Usage Patterns

#### Pattern 1: Simple Usage (Development)
```go
// Zero configuration - uses built-in Layer 1 resilience
deps := orchestration.OrchestratorDependencies{
    Discovery: discovery,
    AIClient:  aiClient,
}
orchestrator, _ := orchestration.CreateOrchestrator(nil, deps)
```

#### Pattern 2: Production Usage (With Circuit Breaker)
```go
// Application creates and injects circuit breaker
cb, _ := resilience.NewCircuitBreaker(&resilience.CircuitBreakerConfig{
    Name:             "capability-service",
    ErrorThreshold:   0.5,
    VolumeThreshold:  10,
    SleepWindow:      30 * time.Second,
})

deps := orchestration.OrchestratorDependencies{
    Discovery:      discovery,
    AIClient:       aiClient,
    CircuitBreaker: cb,  // Inject sophisticated circuit breaker
    Logger:         logger,  // Optional: Structured logging
}

orchestrator, _ := orchestration.CreateOrchestrator(config, deps)
```

#### Pattern 3: Tier 2 Conversation Compaction (Opt-In)

```go
processor, _ := orchestration.BuildCompactionEnabledConversationHistoryPreparer(
    config,
    aiClient,
    orchestration.WithConversationSummaryCache(myCache),   // optional Layer 2 override
    orchestration.WithConversationCompactor(myCompactor),  // optional Layer 2 override
)

deps := orchestration.OrchestratorDependencies{
    Discovery:                   discovery,
    AIClient:                    aiClient,
    ConversationHistoryPreparer: processor,
}
orchestrator, _ := orchestration.CreateOrchestrator(config, deps)
```

Tier 1 conversation-history protection is factory-default behavior. Tier 2 recursive compaction is enabled only when the application explicitly injects a preparer configured with both a `SummaryCache` and a `ConversationCompactor`. `BuildCompactionEnabledConversationHistoryPreparer(...)` is the ergonomic Layer 2 path: it creates the default cache and LLM compactor for you, then applies any caller-supplied overrides last so you can swap one concern without dropping to full direct construction.

### Provider-neutral AI invocation and cache safety

All production orchestration LLM calls pass through the internal
`invokeAI`/`streamAI` boundary. The helper constructs `core.AIRequest`, assigns
a stable provider-neutral purpose, delegates capability selection to
`core.GenerateAI` or `core.StreamAI`, preserves typed LLM-debug recording
deferral, and attaches the sanitized request report to the active trace. No
orchestration call site imports provider packages or branches on provider or
model names.

The result-distillation, conversation-summary, and activity-digest caches hold
AI-derived output. Before lookup they ask the optional
`core.AIRequestFingerprinter` capability for a secret-free policy and route
identity. A stable fingerprint joins the cache namespace; an unstable or
unavailable fingerprint bypasses only that cache. Legacy-only clients using
legacy-representable options remain cacheable under either their existing
direct-client namespace or the common wrapper's stable adapter namespace. On
a miss, the cache records output only when the executed request report matches
the preflight fingerprint, preventing dynamic route or middleware drift from
poisoning an entry. Purely structural caches are unchanged.

#### Pattern 4: Service Mesh (Kubernetes)
```yaml
# Let Istio handle circuit breaking at network level
apiVersion: networking.istio.io/v1beta1
kind: DestinationRule
metadata:
  name: capability-service
spec:
  host: capability-service
  trafficPolicy:
    outlierDetection:
      consecutiveErrors: 5
      interval: 30s
      baseEjectionTime: 30s
```

### Memory Wiring Boundary

Managed `ProcessRequest*` segments carry a private execution resource scope.
When an investigation coordinator implements `core.InvestigationLeaseCoordinator`,
memory enrichment acquires fresh execution-owned claims, and the outer request
finalizer releases those exact handles on every exit, including panic, failure,
cancellation and HITL suspension. Recording events does not release unrelated
step entities in this mode. Each resumed request runs enrichment with restored
checkpoint metadata (explicit adapter metadata wins), acquiring fresh handles.
Handles are never serialized into checkpoints, prompts or diagnostic payloads.

Cleanup is joined and uses a cancellation-independent, correlated context bounded
by `TRUVAG3_EXECUTION_CLEANUP_TIMEOUT` (5s default) or `WithExecutionCleanupTimeout`.
Invalid explicit values fail construction in both config modes. Providers must
honor context; Go cannot forcibly stop a non-cooperative implementation. Cleanup
failure is optional and cannot replace the original result/panic. Pending hook
effects become observed release outcomes before terminal recorder publication.
The scope does not renew investigation TTLs or promise crash-durable cleanup.
Legacy custom coordinators and directly invoked hooks retain their existing
name-owned behavior; direct `ExecutePlan*` callers do not gain managed ownership.
An ordinary trace records the ownership mode so this limitation stays visible.

The error-returning factories reject invalid cleanup synchronously. Legacy
errorless constructors preserve fatal resolution/validation errors and reject
`Start` and execution without initializing executable components.

`BuildMemoryHooks` also resolves `TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS`
for both raw and AI-compacted enrichment. The default is 2000, interpreted as
8000 encoded bytes, not exact model tokens. Generic numeric parsing and range
checks fail construction even without AI or episodic dependencies; selected
implementations enforce their additional arithmetic invariants. The builder returns
hooks, coordinator and error; applications check it synchronously before serving.
Absent episodic memory with valid configuration is a successful no-op. All
selected hooks must construct successfully before a bundle is returned. Direct
numeric options use the same validation; no new module edge or builder knob is
added. Runtime backend failures retain their existing fail-open behavior.

`core.Config` shared-memory/activity fields are validated configuration data;
they are not a runtime handoff into `BuildMemoryHooks` or `NewSharedBackends`.
Those bundles independently read their documented environment settings. Typed
numeric/custom summarizer/compactor composition currently uses direct hook
constructors and their options, with environment tuning supplied by the adopter.
An explicit resolved-config handoff and builder behavior-injection seams remain
separate work; this release does not claim typed/JSON values override the bundles.

The built-in activity compactor checks its five-times-target generation allowance
for integer overflow. A hook selecting that implementation validates its final
target at construction, independent of option order. Direct non-empty full and
incremental calls also reject invalid targets before invoking AI; incremental
rejection preserves the previous digest alongside the error. Custom compactors
are not constrained by the built-in multiplier. Empty-event no-op behavior and
valid digest targets, prompts and generation allowances remain unchanged.

The derived historical view reserves scope/headings/counts and admits complete
JSON records in the existing domain → entity → investigation → knowledge order.
Request-local event detail uses `event-detail/v2-record-facts`: an `event` tuple containing
the exact event ID, origin request ID, timestamp, agent, action and outcome,
alongside named summary/entity fields. The complete tuple legend precedes all
records, follows the historical-scope notice, and is charged to the same budget.
Records remain self-contained; there is no shared provenance lookup table.
Sparse histories retain named fields when estimated metadata savings do not
repay the legend; duplicate IDs do not inflate that estimate. This choice does
not reinterpret summary content or change admission priority. The version and
chosen `event_format` (`named` or `tuple`) are included in the hook view report
and `view_bounded` trace event.
Only admitted `(AgentDomain, EventID)` pairs are deduplicated; the same custom
event ID in another domain remains a distinct record. Empty IDs are not
deduplicated. Display summaries may be shortened at
UTF-8 boundaries and explicitly labelled; immutable provenance either fits or
the record is omitted. Stored events, query order, entity/claim semantics, other
hooks' RAG context and compactor generation budgets are unchanged. Detail views
omit the repeated multi-entity list with an explicit count; compactor input still
retains that list. Cached digests never contain these tuples. The
`historical-origin/v5-record-facts` cache joins the existing AI fingerprint with a
bounded returned-window identity: complete event-content hashes, domain,
lookback, raw-event limit and output-token target. Unchanged windows reuse a
digest without a model call even at the configured limit. A strict extension
updates from only new events when their count is at most the incremental threshold
(20 by default), including equal-timestamp arrivals; larger additions or changed,
expired or displaced records require a bounded full rebaseline. Summary and
window metadata are written together. Concurrent stale writes may cause a
later cache miss, but are not treated as coverage of a different current window.
No cursor, atomic read snapshot or full-history guarantee is asserted.
Legacy/custom episodic readers without the observed-read extension and
independently implemented external compactors remain functional but bypass
digest cache reuse. The behavior
fingerprint method is package-private, not a supported custom-compactor extension.
The built-in compactor needs a stable effective AI fingerprint.
Backend read failure leaves the cache untouched;
compaction failure exposes current raw events instead of an uncovered stale
digest. Old view versions are cache misses, not automatically deleted records.
Mixed-version writers sharing a domain cache can recompact until rollout converges.
View completeness describes presentation of fetched records, not backend health
or complete historical coverage.

Existing hook effects carry byte budgets and per-section admission/omission
counts; bounded trace events contain counts, not event payloads. Legacy
`context_chars` remains byte-valued for compatibility alongside `context_bytes`.
Optional resource-selector attempt diagnostics remain distinct from the one
terminal omission owned by admission, after its parent-cancellation check.

Event-summary output budgeting remains owned by the orchestration memory-hook
builder, independently of planning and synthesis. The optional
`TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_MAX_TOKENS` startup setting overrides the
default 10000-token fixed per-call allowance. Invalid explicit values fail construction.
Direct `WithSummarizerTokensPerStep(n)` opts into the calculated batch allowance;
an explicit fixed allowance wins regardless of option order.
Direct callers use
`WithSummarizerMaxTokens(n)` and receive an error for non-positive values.
Requests continue through the existing AI, debug, and tracing paths.

Recording supplies optional error text and copied structured error bytes to
summarizers, including when the step response is empty. The built-in prompt
places failure facts first as JSON-encoded untrusted strings, sharing the
existing per-step response allowance (4000 encoded payload bytes by default).
`TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_MAX_RESPONSE_BYTES` configures that
positive allowance through the builder; direct callers retain
`WithSummarizerMaxResponseChars`, whose historical name counts bytes. Omitted
or shortened views are labelled; original execution fields stay unchanged.
Failed heuristic summaries use the recorded code, otherwise error text,
otherwise an unknown-cause label. Stored event metadata distinguishes
`summary_source=model|heuristic|custom` and a fixed fallback reason.
The heuristic cause (including a structured code) is limited to 256 UTF-8-safe
bytes by default, configured through
`TRUVAG3_SHARED_MEMORY_RECORD_FAILURE_CAUSE_MAX_BYTES` or direct
`WithRecordFailureCauseMaxBytes`. Positive values are required; invalid explicit
builder values fail even without dependencies. Operation/entity framing and the
additional ` [shortened]` marker are outside this cause-only budget. Shortening
sets `summary_cause_shortened=true`; original execution errors and model/custom
summaries are unchanged. Steps excluded by the built-in summary batch cap record
`batch_limit`, not the admitted batch's model failure; `batch_excluded` is a
separate coverage count, not parser truncation. Record effects expose independent
`backend_assigns_event_id` and `backend_assigns_timestamp` booleans, replacing
the ambiguous combined diagnostic field. Historical effects are not rewritten.

`WithBuilderRecordRequestEntityExtractor` (or direct
`WithRecordRequestEntityExtractor`) opts into extracting recording entities
from `PipelineContext.Request` and cloned JSON-like request metadata. Valid
request entities precede distinct step/model entities. Opaque metadata values
remain application-owned and read-only. The default step extractor, enrichment
extractor and claims acquired at request start are unchanged. Request-only
recording entities never enter legacy name-owned release; managed cleanup
still releases only its acquired private handles.

New events use a non-future, internally consistent step end time, then a valid
start time, then recording time with unknown occurrence. Metadata retains
`recorded_at`, `event_time_basis` and any fallback reason. Event windows and
content-hash cache identity therefore see occurrence time, while provider
retention stays ingestion-based. Detail views project summary/time provenance;
legacy records report unknown provenance, not fabricated facts. Historical
records, query algorithms, planning/executor behavior and AI call budgets are
unchanged. Recording span counts include entity-less events; hook effects
remain producer/provider-call observations, not durable-write acknowledgements.

Direct summary construction rejects overflow in the calculated batch allowance
unless a valid fixed allowance replaces it. Memory retrieval weights accept
finite relative weights (including signed/zero values) without enforcing a sum;
non-finite weights and temperatures fail construction. The announcement
constructor retains zero as its default sentinel and rejects negative limits.

Built-in event summaries are validated per submitted step ID. Complete valid
entries preceding an unfinished JSON suffix survive as **partial** coverage;
missing/invalid/duplicate IDs fall back once in the recorder. A malformed syntax
error other than an unfinished suffix rejects the batch. Complete outer JSON
fences and unambiguous complete prose-wrapped arrays remain compatible, including
prose only after an array. JSON decoding determines the exact array boundary;
strings inside it are unchanged. Partial prose envelopes, multiple arrays, other
JSON envelopes/values and structural JSON markers outside the array are rejected.
Raw/outer-fenced truncated-array recovery remains unchanged. Invalid
entities are discarded independently of valid summary text; model-derived,
explicit-input and custom-summarizer entity paths remain supported. No new
grounding policy or additional model retry is introduced.

Both event-summary prompts share the complete `step_id`/`summary`/`entities`
output contract, including a legitimate empty entity array. Additive
`batch.entity_fields` coverage classifies retained summaries as missing, empty,
valid nonempty, invalid or mixed-validity entity fields, and counts accepted and
rejected entities. Aggregation occurs after duplicate-step invalidation; rejected
or discarded summaries do not inflate retained field coverage. Missing/null
fields still leave usable summary text available. Legacy batch outcome and
rejected-entity semantics remain unchanged, so a complete summary batch may
still have missing/invalid entity fields. An explicit empty array is structurally
valid, not proof that no real entity was missed. These are shape observations,
not grounding or durable-write acknowledgements. Custom summarizers have no
built-in batch observation, rather than a fabricated zero count. The new coverage
fields in recording effects and summary/recording span events contain counts only;
raw IDs and responses retain their existing execution-debug boundaries.

Outcome metadata is call-local and private to the built-in summarizer. The
public `core.EventSummarizer` interface is unchanged. Existing recording-hook
effects include summary source, eligible/summarized/fallback counts and optional
built-in batch coverage. These describe summary coverage, not successful durable
backend writes. New events use the current trace context's trace ID or an empty
value when unavailable, separately from their request identity; stored historical
events are not rewritten. Empty/whitespace compactor output is unusable: full
compaction falls back to raw details, while an incremental failure preserves the
previous cache entry, while the prompt receives current raw details. Custom
empty digests receive the same guard.

`orchestration.event_summarization.success` retains its provider-completion
meaning. The additive `orchestration.event_summarization.outcomes` counter has
`outcome=complete|partial|fallback|llm_unavailable|parent_cancelled|summary_timeout`,
emitted once per non-empty built-in batch. `batch_excluded` is a count in the
outcome event/report, not an outcome label.
Both memory dashboard copies distinguish that semantic series from legacy
completion. Corresponding summary/compaction/recording span events contain
bounded counts and outcomes; raw content stays in the existing debug path.

`OrchestratorDependencies` does **not** include a generic `Memory` field — and it should not. Memory in this codebase is split across several distinct interfaces, each wired at a different layer:

| Interface | Where it lives | Wired by |
|---|---|---|
| `core.Memory` (basic key-value cache) | `BaseAgent.Memory` field; tools use a per-tool `cache *core.MemoryStore` field | Framework default (`*core.MemoryStore`) at construction; replaceable by the caller before `NewFramework`. Sweeper Runnable registered via `framework.AutoRegisterMemorySweeper()` (agents) or `framework.RegisterRunnable(core.NewMemoryStoreSweeper(...))` (tools). |
| `core.ConversationMemory` / `core.SemanticMemory` | Optional fields on `BaseAgent` | Application wires at startup (e.g. `agent.ConversationMemory = redisBackedImpl`). |
| `core.EpisodicMemory`, `core.ActivityCoordinator`, `core.DigestCache` | Higher-level cross-agent memory | Wired via `memory.NewSharedBackends(redisClient, ...)` and pipeline hooks; see Pattern 3 above for the conversation-compactor variant of this flow. |

**Why no `Memory` field on `OrchestratorDependencies`:** the orchestrator does not consume `core.Memory` directly (`grep -n Memory orchestration/factory.go orchestration/interfaces.go` returns zero matches). Tool-level response caching is a tool-local concern; agent-level state is on `BaseAgent.Memory`. Adding a `Memory` field to `OrchestratorDependencies` would suggest the orchestrator owns it, which is the opposite of the actual dependency direction. If a future feature needs key-value state inside orchestration, the `RoutingCache` ([§Routing Cache](#core-components)) is the right mechanism — purpose-built for orchestration, with its own LRU/TTL semantics.

### Module Dependencies for Resilience

#### What Orchestration Module Provides
- CapabilityProvider interface
- ServiceCapabilityProvider with simple resilience (Layer 1)
- Injection points for optional dependencies
- Fallback mechanisms (Layer 3)

#### What Application Provides
- Circuit breaker implementation (using resilience module)
- Logger implementation
- Telemetry implementation
- Configuration and tuning parameters

#### Dependency Flow for Resilience
```
Application Code
    ├── imports orchestration (for orchestrator)
    ├── imports resilience (for circuit breaker)
    └── injects circuit breaker into orchestrator

Orchestration Module
    ├── imports core (for interfaces)
    ├── imports telemetry (for observability)
    └── accepts core.CircuitBreaker (no resilience import!)

Core Module
    └── defines CircuitBreaker interface

Resilience Module
    ├── imports core
    └── implements CircuitBreaker interface
```

### Benefits of This Design

#### For Framework Maintainers
- **Clean Architecture**: No dependency violations
- **Consistent Patterns**: Same as UI module
- **Testable**: All dependencies mockable
- **Extensible**: Clear injection points

#### For Application Developers
- **Progressive Enhancement**: Start simple, add resilience as needed
- **Flexibility**: Choose any circuit breaker implementation
- **Production Ready**: Inject sophisticated patterns
- **Service Mesh Compatible**: Works with Istio/Linkerd

#### For Operations
- **Observable**: Metrics through telemetry interface
- **Configurable**: All parameters tunable
- **Graceful Degradation**: Multiple fallback layers
- **Fast Recovery**: Automatic recovery testing

### Migration Path

#### From Current Implementation
1. Current simple resilience remains as Layer 1
2. Add injection points for optional dependencies
3. No breaking changes to existing code

#### For Applications
```go
// Stage 1: Use as-is (current implementation works)
orchestrator := orchestration.CreateSimpleOrchestrator(discovery, aiClient)

// Stage 2: Add logging and telemetry
deps := orchestration.OrchestratorDependencies{
    Discovery: discovery,
    AIClient:  aiClient,
    Logger:    logger,
    Telemetry: telemetry,
}
orchestrator, _ := orchestration.CreateOrchestrator(nil, deps)

// Stage 3: Add circuit breaker for production
deps.CircuitBreaker = resilience.NewCircuitBreaker(config)
orchestrator, _ = orchestration.CreateOrchestrator(config, deps)
```

### Design Rationale

#### Why Not Import Resilience Directly?
- **Framework Rule**: Orchestration can only import core + telemetry
- **Separation of Concerns**: Framework provides capability, apps choose implementation
- **Flexibility**: Apps might use different circuit breaker libraries
- **Testability**: Can test orchestration without resilience module

#### Why Follow UI Module Pattern?
- **Proven Pattern**: Already working successfully in production
- **Consistency**: Same pattern across framework
- **Developer Familiarity**: Learn once, apply everywhere
- **Maintenance**: Single pattern to maintain and document

#### Why Three Layers of Resilience?
- **Defense in Depth**: Multiple failure handling strategies
- **Progressive Enhancement**: Each layer adds protection
- **Flexibility**: Choose appropriate level for use case
- **No Vendor Lock-in**: Can use framework, custom, or service mesh resilience

### Implementation Examples

#### Service Provider with Three-Layer Resilience
```go
func (s *ServiceCapabilityProvider) GetCapabilities(ctx context.Context, request string, metadata map[string]interface{}) (string, error) {
    // Layer 2: Use injected circuit breaker if provided
    if s.circuitBreaker != nil {
        var result string
        err := s.circuitBreaker.Execute(ctx, func() error {
            var err error
            result, err = s.queryExternalService(ctx, request, metadata)
            return err
        })

        if err != nil {
            // Layer 3: Try fallback
            if s.fallback != nil {
                return s.fallback.GetCapabilities(ctx, request, metadata)
            }
            return "", err
        }
        return result, nil
    }

    // Layer 1: Use simple built-in resilience
    return s.getCapabilitiesWithSimpleResilience(ctx, request, metadata)
}
```

#### Circuit Breaker Integration
```go
type ResilientOrchestrator struct {
    *AIOrchestrator
    circuitBreakers map[string]core.CircuitBreaker
}

func (o *ResilientOrchestrator) callComponent(ctx context.Context, component string, request interface{}) (interface{}, error) {
    cb, exists := o.circuitBreakers[component]
    if !exists {
        cb = o.createCircuitBreaker(component)
        o.circuitBreakers[component] = cb
    }

    return cb.Execute(func() (interface{}, error) {
        return o.executeComponentCall(ctx, component, request)
    })
}
```

#### Retry Mechanisms
```go
type RetryConfig struct {
    MaxAttempts int
    InitialDelay time.Duration
    BackoffFactor float64
    MaxDelay time.Duration
}

func (e *SmartExecutor) executeWithRetry(ctx context.Context, step PlanStep, config RetryConfig) (interface{}, error) {
    var lastErr error
    delay := config.InitialDelay

    for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
        result, err := e.execute(ctx, step)
        if err == nil {
            return result, nil
        }

        lastErr = err
        if attempt < config.MaxAttempts {
            time.Sleep(delay)
            delay = time.Duration(float64(delay) * config.BackoffFactor)
            if delay > config.MaxDelay {
                delay = config.MaxDelay
            }
        }
    }

    return nil, fmt.Errorf("failed after %d attempts: %w", config.MaxAttempts, lastErr)
}
```

#### Timeout Management
```go
func (e *SmartExecutor) executeWithTimeout(ctx context.Context, step PlanStep, timeout time.Duration) (interface{}, error) {
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    resultChan := make(chan interface{}, 1)
    errorChan := make(chan error, 1)

    go func() {
        result, err := e.execute(ctx, step)
        if err != nil {
            errorChan <- err
        } else {
            resultChan <- result
        }
    }()

    select {
    case result := <-resultChan:
        return result, nil
    case err := <-errorChan:
        return nil, err
    case <-ctx.Done():
        return nil, fmt.Errorf("step %s timed out after %v", step.Name, timeout)
    }
}
```

#### Graceful Degradation
```go
func (o *AIOrchestrator) ProcessRequestWithDegradation(ctx context.Context, request string) (*Response, error) {
    // Try AI routing
    response, err := o.ProcessRequest(ctx, request)
    if err == nil {
        return response, nil
    }

    // Fall back to cached response
    if cached := o.cache.Get(request); cached != nil {
        return cached.(*Response), nil
    }

    // Fall back to basic routing
    if basicResponse := o.basicRouter.Route(request); basicResponse != nil {
        return basicResponse, nil
    }

    // Return helpful error
    return &Response{
        Status: "degraded",
        Content: "Service temporarily limited. Please try again or use specific commands.",
    }, nil
}
```

#### Layer 4: Semantic Retry (Contextual Re-Resolution)

Beyond the three-layer resilience architecture for capability services, the execution layer has its own advanced error recovery: **Semantic Retry**.

**When Layer 3 Error Analysis says "cannot fix"**, Semantic Retry provides one more chance by using the full execution trajectory:

```go
// ExecutionContext captures everything needed for semantic retry
type ExecutionContext struct {
    UserQuery       string                 // Original user intent
    SourceData      map[string]interface{} // Data from dependent steps
    StepID          string                 // Current step being executed
    Capability      *EnhancedCapability    // Target capability schema
    AttemptedParams map[string]interface{} // What we tried
    ErrorResponse   string                 // What went wrong
    HTTPStatus      int                    // Error status code
}

// ContextualReResolver computes corrected parameters
type ContextualReResolver struct {
    aiClient core.AIClient
    logger   core.Logger
}

func (r *ContextualReResolver) ReResolve(ctx context.Context, execCtx *ExecutionContext) (*ReResolutionResult, error) {
    // LLM analyzes full context and computes corrected parameters
    // Returns: {ShouldRetry: true, CorrectedParameters: {...}, Analysis: "..."}
}
```

**Hypothetical opt-in scenario:** The shipped `currency-tool` sends
`retryable:false` for amount validation and stops. The recovery below assumes
explicit correction permission for that condition and all executor gates passing.
```
User: "Sell 100 Tesla shares and convert proceeds to EUR"
Step 1: Returns {price: 468.285}
Step 2: Fails with "amount must be > 0" (amount: 0)

Layer 4 computes: 100 × 468.285 = 46828.5
Retries with corrected parameters → SUCCESS
```

**Configuration:**
```go
config := DefaultConfig()
config.SemanticRetry.Enabled = true    // Default: true
config.SemanticRetry.MaxAttempts = 2   // Default: 2
```

**Environment Variables:**
```bash
TRUVAG3_SEMANTIC_RETRY_ENABLED=true
TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS=2
```

**Note on reuse:** `ExecutionContext` is scoped to the post-error path today, but four of its six fields (`UserQuery`, `SourceData`, `StepID`, `Capability`) are meaningful before any dispatch too. Any future pre-dispatch semantic fallback should reuse this shape with `ErrorResponse=""` and `HTTPStatus=0` rather than introduce a parallel context type.

### LLM Debug Payload Store

The orchestration module includes a debug store that captures complete LLM request/response payloads for production debugging. This addresses the limitation of Jaeger spans which truncate large payloads.

**Architecture:**
```
┌─────────────────────────────────────────────────────────────────┐
│                    LLM Debug Store Architecture                  │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Recording Sites (11 total):                                     │
│  ├── Orchestrator: plan_generation, correction,                  │
│  │   synthesis_streaming, hallucination_detection,               │
│  │   continuation_plan_generation,                               │
│  │   continuation_plan_regeneration                              │
│  ├── ConversationHistoryPreparer: conversation_history_prepare   │
│  │   (logic) and conversation_history_compaction (optional LLM)  │
│  ├── Synthesizer: synthesis                                      │
│  ├── MicroResolver: micro_resolution                             │
│  ├── ContextualReResolver: semantic_retry                        │
│  ├── TieredCapabilityProvider: tiered_selection                  │
│  └── InstrumentedAIClient: agent_llm_call (via ai module +      │
│      telemetry.RedisLLMCallRecorder)                             │
│                                                                  │
│  Interaction Fields (attribution):                               │
│  ├── SourceComponent: agent/component name (e.g. "research-     │
│  │   assistant") — set by WithComponentName() on                 │
│  │   InstrumentedAIClient. Empty for orchestrator calls.         │
│  ├── CallDescription: human-readable label for the interaction   │
│  └── Category: rendering hint ("llm", "logic", "vector_db",     │
│      "storage", "embedding") for non-LLM hook activity          │
│                                                                  │
│  Record-Level Attribution:                                       │
│  └── OriginatingAgent: the agent whose orchestrator (or         │
│      background job) initiated this request — surfaced in the    │
│      registry-viewer Source column. Sourced from the "agent_    │
│      name" OTel baggage key the orchestrator stamps from         │
│      o.config.Name (TRUVAG3_AGENT_NAME). Persisted via HSetNX    │
│      on the meta hash so first writer with a non-empty value     │
│      wins — keeps the originator stable across multi-writer      │
│      requests (orchestrator + downstream agent writing to the    │
│      same record). Background-job writers must stamp the         │
│      baggage themselves before recording (see                    │
│      memory.ReflectionJob.RunOnce).                              │
│                                                                  │
│  Summary Listing:                                                │
│  ├── LLMDebugRecordSummary.SourceComponents: sorted, unique     │
│  │   agent names extracted from interactions. Used by registry   │
│  │   viewer to show which agents triggered each record.          │
│  └── LLMDebugRecordSummary.OriginatingAgent: mirror of the       │
│      record field above — table Source column prefers this       │
│      when non-empty, falls back to SourceComponents otherwise.   │
│                                                                  │
│  Storage Layer:                                                  │
│  ├── Redis preset role (DB 0, versioned deployment keyspace)   │
│  ├── MemoryLLMDebugStore (testing)                               │
│  └── NoOpLLMDebugStore (disabled/fallback)                       │
│                                                                  │
│  Alternative Writer (no orchestration import needed):            │
│  └── telemetry.RedisLLMCallRecorder — write-only Redis recorder  │
│      for agents. Same DB-0 request-local format and retention     │
│      rules; recent-index projection is best-effort.               │
│                                                                  │
│  Three-Layer Resilience:                                         │
│  ├── Layer 1: Built-in retry (3 attempts, 50ms backoff)         │
│  ├── Layer 2: Optional circuit breaker (via DI)                  │
│  └── Layer 3: Automatic fallback to NoOp                         │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**Key Design Decisions:**
- **Disabled by default**: Must explicitly enable via `TRUVAG3_LLM_DEBUG_ENABLED=true`
- **Provider-neutral runtime contract**: Runtime behavior consumes
  `LLMDebugStore`; the included Redis preset is one DB-0 adapter and is not a
  neutral storage requirement
- **TTL-based cleanup**: 72h (3 days) for both success and errors;
  final execution outcome and lineage promotion may retain the current and
  root records longer
- **Async recording**: Uses goroutines with WaitGroup for non-blocking capture
- **Completion provenance ownership**: Callers that defer generic AI recording
  preserve the exact response's reasoning, finish reason and content source in
  their authoritative typed interaction, including available partial-error
  responses. Missing provider facts stay unknown. Validation-only audit records
  do not manufacture another completion. These payload fields remain in opt-in
  debug storage, not logs, span attributes or metric labels.
- **Request preparation evidence**: Authoritative planning, correction, tiered
  selection and synthesis interactions use the shared effective-request
  projection. Requested/resolved model, compatibility adjustments, policy
  fingerprint and stability come from that same call's provider report, alongside
  effective generation settings. An absent report does not imply a stable policy.
- **Graceful fallback**: Never fails orchestration if debug store fails
- **Payload fidelity**: Built-in stores preserve application and model
  payloads without framework-inferred redaction. Sanitization, access control,
  encryption, and retention policy for sensitive workloads belong to the
  adopter, as required by the root framework design principles. The legacy
  executor and skills error transformations documented below occur before
  storage and remain pending a separate audit; they are not part of the store
  contract or precedent for new paths
- **Request-local atomic minimum retention**: Both Redis writers preserve a
  longer or persistent lifetime in the same atomic operation that appends an
  interaction. Metadata, interactions, and the retention floor share one hash
  slot. The deployment-wide recent index is an advisory post-commit projection;
  its bounded retry never repeats the authoritative append
- **Late-writer retention floor**: Final execution recording stores a small,
  non-displayable retention-floor key. Orchestrator and standalone-agent
  writers consult that key atomically, so a write that starts later or in a
  different pod still receives the required audit lifetime

The store name is historical: it now carries both true LLM calls and selected non-LLM hook interactions when they materially improve production debugging. Examples include `conversation_history_prepare` (`Category: "logic"`), `user_memory_similarity_search` (`Category: "vector_db"`), and persistence-policy decisions (`Category: "logic"`). The registry viewer uses `HookPhase` plus `Category` to route these uniformly into LLM Debug, Pre-Execution, and Post-Execution views.

#### Conversation-aware execution storage

Conversation identity is stored in the existing `StoredExecution.Metadata` and
`ExecutionSummary.Metadata` maps. `ExecutionConversationID` and
`ExecutionSummaryConversationID` are the canonical accessors; the conversation
contract does not require a dedicated conversation field on either record. The
separate terminal-response fields described below are not conversation
identity.

`ExecutionStore` keeps its minimal required method set.
`ConversationExecutionLister` is a separate optional capability discovered by
type assertion. `NoOpExecutionStore` intentionally does not advertise it.
`StorageProvider` remains the backend-neutral base storage contract.
`KeyTTLManager` separately defines atomic minimum-retention operations on
ordinary values. Its two operations
extend an existing key without creating it, and rewrite a value while
preserving the larger of its prior remaining lifetime and the requested
minimum. Neither operation may shorten a longer lifetime or make a persistent
key expiring.

`ExecutionStorageProvider` composes `StorageProvider` and `KeyTTLManager` and
is the compile-time requirement for the provider-backed execution store. This
makes lineage promotion and TTL-preserving rewrites correctness invariants,
not optional backend behavior. `IndexTTLManager` remains optional because a
conversation index is only an accelerator; providers without index-TTL support
rely on bounded lazy stale-member cleanup.

Both the provider-backed store and direct Redis store:

- validate `conversation_id` before any conversation-index command;
- store records even when optional index or index-TTL maintenance fails;
- use canonical single-colon keys and a SHA-256 digest rather than exposing the
  raw ID in the key;
- treat record metadata as authoritative and the index as an accelerator;
- verify loaded record membership and prune stale or mismatched index members
  without deleting live execution records;
- return conversation results chronologically.

`ExecutionStoreConfig.ConversationQueryLimit` defaults to 1000 and bounds
returned records. `ConversationIndexScanLimit` defaults to 5000 and bounds
work when stale entries are encountered. Explicit positive config values win;
otherwise environment values are normalized by the factory and invalid or
non-positive values fall back to defaults.

#### Pipeline-hook execution evidence

`StoredExecution.PipelineHooks` is the authoritative, provider-neutral record
of the pipeline hooks observed for that request. Each entry preserves the hook
name, lifecycle phase, terminal invocation outcome, phase-local sequence, optional
iterative-plan phase, start time, duration, and exact failure text. The
request-local holder is concurrency-safe because execution snapshots are
written asynchronously, while hook invocation remains sequential.

The invocation outcome reports whether the framework call returned normally,
returned an error, panicked, or was skipped. It does not claim that every side effect
initiated by the hook completed successfully: a hook may deliberately fail
open, and `KnowledgeExtractionHook` schedules work that completes later.

Each invocation may therefore carry versioned `Effects`. Effects have their own
`pending`, `succeeded`, `partial`, `failed`, or `skipped` status, exact failure
text, timing, and producer-owned JSON data. The hook runner automatically
captures exact per-key `PipelineContext.Enrichments` changes at
`BeforePlanning`. Built-in shared-memory hooks additionally report submitted
activity signals, discovery/selection metadata, episodic event and claim-release
calls, extracted knowledge fragments and provider-call observations, and
activity cleanup calls. Hook-specific context metadata does not duplicate the
exact value already carried by its enrichment effect.
Custom hooks use the typed `core.PipelineHookEffectReporter` context seam; no
orchestration type or storage provider is exposed to the hook.

On normal return, the same entry also carries an orchestration-owned `Decision`:
the boundary's `failure_policy`, actual pipeline `action`, and bounded `reason`.
Invocation failure is not request failure: an optional hook error records
`fail_open` / `continue`, while required after-planning rejection records
`fail_closed` / `terminate`. Required success still has a fail-closed policy
but records `continue`. Accepted before-planning responses record
`short_circuit`; rejected cache provenance records `continue` without serving
the cached response. A missing decision means not recorded, never inferred
success. A panic crossing an invocation boundary records `failed` with action
`propagate_panic` and reason `panic`, closes the hook span, and re-panics the
original value. The ordinary error policy remains recorded but never suppresses
a panic. No later hook or protected phase work runs after that boundary.

The phase loop transfers available failed diagnostic state through a private
callback while unwinding, so a later-phase panic preserves admitted plans and
completed steps. Its active phase span and timeout are closed. The shared
request defer records the failure through the existing terminal publisher and
marks/ends the request span before propagating the same panic. Native streaming
also closes the enclosing synthesis span if an after-synthesis hook panics;
already-delivered tokens cannot be retracted. Pending producer effects remain
pending until the producer reports their outcome; a callback panic does not
invent a side-effect result. Subsequent effect rewrites retain the failed state.
This is best-effort in-process diagnostic capture, not durability through process
termination or recovery of panics in separate application-owned goroutines.

Native streaming records the successful synthesis model interaction before
running AfterSynthesis hooks. Its exact prompt/response/usage and model-call
duration therefore survive a later hook panic without labeling that LLM call
failed. The request still fails and no final application response is stored.
The typed request state retains the existing stream callback count so error
completion logs do not claim zero chunks after streaming has begun.

Invalid explicit before-planning decisions store a failed, zero-step result.
The hook-terminal fallback also supplies a failed result when no result exists
and the request ended with a non-interruption error. Later planning/validation
failures carry completed work and admitted plans to that same terminal writer;
they do not admit the rejected candidate. An existing richer terminal publisher
is never replaced. HITL suspension remains an interruption, not an invented
failure. The ordinary execution-store outcome rule selects failure retention,
including subsequent effect-driven rewrites; no new retention mechanism is used.

The decision is copied under the existing holder mutex before its revision is
published, and defensively copied in snapshots. Detached effect updates retain
it. The runner supplies that same value to the existing span as bounded
`pipeline.hook.failure_policy`, `pipeline.hook.action`, and
`pipeline.hook.reason` attributes before ending the span, even when execution
recording is disabled. No application payload is added to these attributes.

This evidence is captured by orchestration and stored through the existing
`ExecutionStore.Store` method. It does not add a backend method, Redis command,
key, index, or topology rule. A custom `ExecutionStore`—including a future
DynamoDB implementation—receives the same optional field. Observability UIs
must read this record rather than query a tracing backend and infer ownership
from distributed spans. Trace IDs remain optional correlation pointers only.

The existing `pipeline.hook.<phase>.<hook-name>` spans remain the timing and
causality surface. Their lifetime covers the complete framework-observed
boundary, including after-planning clone, return-type, and validation checks,
so a rejected result is recorded as a failed span. Effect payloads are not
copied into span attributes, events, or logs. Detached knowledge extraction
uses a fresh shutdown-owned context with copied request baggage and a linked
`pipeline.hook.async.knowledge-extraction` root span; it does not retain the
request's cancellation or make an ended request span its parent. The exact
model response, fragments, and provider observations remain in execution-debug
effects. Built-in fail-open provider failures mark the active span with a
bounded error classification and use bounded ordinary log fields; raw provider
text is not duplicated outside the opt-in effect record. Application-owned
custom hook callback errors remain exact.

An asynchronous hook must record a new effect as `pending` before its
invocation returns. Detached work may then transition that same effect ID to a
terminal state; a new post-invocation ID and a terminal-to-pending regression
are rejected. Every request-ending execution snapshot--success, failure,
partial native-stream delivery, or HITL suspension--installs a request-local
publisher on the same orchestrator-owned execution recorder. The request
lifecycle also closes uncommon errors that occur before the planner or executor
can produce a richer terminal result, provided at least one hook ran. A terminal
effect update rewrites the complete record through `ExecutionStore.Store`.
Holder revisions are admitted monotonically before per-request recorder
serialization, so concurrently finishing detached effects cannot submit an
older snapshot after a newer one. Pending work is counted in the orchestrator's
recording lifecycle, so shutdown waits for the effect update and the resulting
store write. This relies only on the existing last-write-wins `Store` contract
and remains backend-neutral.

The execution debug store is an explicit fidelity-preserving opt-in, so hook
errors, effect errors, and effect data are stored exactly as supplied. The
framework does not silently redact, truncate, or summarize them. Applications
that need transformation must provide it at their hook, logger, store, or other
documented customization boundary. Effect status is producer-reported rather
than an independent durability attestation. This distinction is material for
the memory module's documented fail-open providers, which may log an internal
runtime failure and return a neutral result to keep the request alive; built-in
effect schemas label submissions and provider-call observations accordingly.
Workflow-mode `ExecutePlanWithSynthesis` does not run pipeline hooks and
therefore does not populate this field.

Enrichment snapshots are serialized only when an execution-debug invocation
exists. Structured hook effects check for an active reporter before marshaling
application values. Disabling capture therefore avoids invoking application
`MarshalJSON` methods for troubleshooting evidence; enabled capture retains
the exact-fidelity contract above.

#### Terminal application-response evidence

`StoredExecution.FinalResponse` is a pointer so an absent terminal response is
different from an application that intentionally returned an empty string.
`StoredExecution.FinalResponseSource` identifies the capture boundary. The
normal buffered and native-streaming `ProcessRequest` paths run
`AfterSynthesis` hooks and then persist the resulting application response with
source `FinalResponseSourceAfterSynthesisHooks` (`after_synthesis_hooks`).
An accepted `BeforePlanning` short-circuit stores its exact terminal response
with source `FinalResponseSourceBeforePlanningShortCircuit`
(`before_planning_short_circuit`) and a successful zero-step result. That makes
the hook invocation visible and prevents list consumers from misclassifying a
successful short-circuit as a failed record merely because no model plan ran.

This record is deliberately separate from the synthesis interaction in the LLM
debug store. The synthesis interaction is the model output before application
governance; it remains useful evidence but is not proof of the final business
response. Registry Viewer therefore shows synthesis under LLM Calls and the
governed terminal value under Post-Execution.

On native streaming, synthesis tokens have already been delivered when
`AfterSynthesis` runs. `FinalResponse` is therefore the post-hook response
object, not a claim that the hook-mutated text was the byte stream observed by
the client.

At request finalization, partial-content delivery is a failed lifecycle even if
some text reached the client. `request_lifecycle.go` also treats a returned error
or response errors as failure; partial output is not authoritative successful
completion and must not be displayed as such.

Native partial-stream delivery retains content and available usage without a
retry. Its authoritative synthesis debug interaction records `Success=false`
and the exact returned error; the response's `Errors` also retains that cause.
The delivery `FinishReason` is `cancelled` or `deadline_exceeded` only when the
returned error wraps the corresponding context sentinel, otherwise `interrupted`.
The provider's own finish reason remains unchanged in the debug record. The
synthesis span records a bounded partial-stream exception and
`synthesis.interruption_reason`, not raw provider error text.

Workflow-mode `ExecutePlanWithSynthesis` executes a caller-supplied plan and
synthesizes its results, but it does not run pipeline hooks and does not
currently persist `FinalResponse`. Older records also predate these fields.
Consumers must treat a missing value as "no post-hook terminal evidence was
stored," not as proof that the application returned the raw synthesis.

#### Lineage-aware debug retention

Execution and LLM-debug evidence are retained as one audit lineage rather than
as unrelated records. The shared execution-retention selector applies these
rules:

- a successful execution receives the configured normal TTL;
- a failed execution receives the configured error TTL; and
- an interrupted execution receives at least the maximum of normal TTL, error
  TTL, and the checkpoint's remaining approval lifetime.

The normal and error TTLs both default to 72 hours (3 days). Changing a default
does not rewrite existing Redis keys or revive expired records; longer existing
or explicitly extended retention remains protected by the minimum-TTL contract.

Each execution `Store` also attempts to persist a small retention-link record
containing only its trace ID, conversation ID, and related-root ID. Normal TTL
extension reads this projection instead of decompressing and unmarshalling the
full execution payload. A projection-write failure after the authoritative
record succeeds is a sanitized warning, not a false `Store` failure; indexing
continues, and later extension falls back to the full record when the projection
is absent. Direct Redis atomically extends the request-local execution record
and retention link, which share one hash slot. Trace, conversation, and recent
indexes are repairable deployment-wide projections updated outside that
request-local operation; projection failure is observable but does not turn a
successful authoritative write into failure. `ExtendTTL` follows related-root links recursively with a
cycle guard, so an investigation extension on a retained descendant promotes
the complete available lineage. Promotion never creates a missing execution
and never fails the business request. A missing requested execution returns the
typed `ErrExecutionRecordNotFound`; a missing later ancestor ends successful
available-chain traversal. Expected absence is surfaced only after an injected
breaker operation succeeds, so it cannot count against storage health; other
failures remain inside resilience handling and are sanitized at logging
boundaries. `Store`, `Update`, and `SetMetadata` use atomic rewrite operations
to avoid undoing a prior promotion. Direct Redis `Update` replaces its
authoritative record and retention projection together through one MSET-backed
Lua operation. Non-positive programmatic `TTL` and `ErrorTTL` values, including
values supplied by direct Redis functional options, are normalized to the
framework defaults before either store is used.

Final execution recording coordinates LLM evidence through the optional
`LLMDebugRetentionPreserver` capability. The Redis implementation creates or
extends a small retention-floor marker without creating an empty debug record,
and applies the same floor to any existing metadata, interaction, or legacy
record keys. Both Redis writers read that marker in their request-local append
script, which
covers writes that begin after execution recording and writes from another
process. Execution storage and each current/root LLM-retention target use
separate bounded timeouts.
Within one request, the recorder skips duplicate floor writes and repeats only
when the selected retention increases. A failed execution-store write is
logged but does not prevent the independent LLM-retention attempt. Custom
stores without this capability fall back to `LLMDebugStore.ExtendTTL`; typed
`ErrLLMDebugRecordNotFound` remains an expected no-op. Recorder goroutines are
owned and drained by the existing orchestrator lifecycle; they are request
work, not independent background `core.Runnable` jobs.

HITL checkpoints use a framework-owned shallow clone of top-level application
metadata. Framework additions, including `conversation_id` and
`original_trace_id`, are read from `checkpoint.UserContext`; the caller's
original top-level map is not mutated. Nested application values retain normal
Go reference semantics. Resume context restores the validated canonical
conversation ID before the `hitl.resume` linked span starts. Missing or invalid
identity is scrubbed rather than inherited.

**Configuration:**
```bash
# Enable debug capture (default: false)
export TRUVAG3_LLM_DEBUG_ENABLED=true

# TTL settings
export TRUVAG3_LLM_DEBUG_TTL=72h       # Success records (3 days)
export TRUVAG3_LLM_DEBUG_ERROR_TTL=72h # Error records (3 days)

# Optional deployment namespace shared by every Redis-backed role.
export TRUVAG3_REDIS_NAMESPACE=production
```

All Redis topologies use DB 0. Removed role-database environment settings are
rejected for selected roles; settings belonging only to unselected roles are
outside that process's effective configuration. Empty values count as absent.

**Canonical Programmatic Usage:**
```go
ownedBackends, err := redisprovider.NewDefaultBackends(
    logger,
    redisprovider.WithDefaultBackendRoles(redisprovider.ClientRoleLLMDebug),
)
if err != nil {
    return err
}
defer ownedBackends.Close()

config := orchestration.NewDefaultOrchestratorConfig()
config.LLMDebug.Enabled = true
if err := orchestration.WireOrchestratorBackends(
    config,
    ownedBackends.Backends(),
); err != nil {
    return err
}

deps := orchestration.OrchestratorDependencies{
    Discovery: discovery,
    AIClient:  aiClient,
    Logger:    logger,
}
orchestrator, err := orchestration.CreateResolvedOrchestrator(config, deps)
```

For a custom implementation, assign `config.LLMDebugStore` directly before
`CreateResolvedOrchestrator`; no Redis package or setting is involved.
`CreateOrchestratorWithOptions(..., WithLLMDebug(true))` remains a supported
compatibility path and retains its historical Redis-from-environment bootstrap,
but new composition code should inject the store explicitly as above.

### Implementation Checklist

When implementing resilience patterns:

- [ ] Update ServiceCapabilityConfig with optional dependencies
- [ ] Refactor ServiceCapabilityProvider to use injected circuit breaker
- [ ] Create OrchestratorDependencies struct
- [ ] Update factory functions to accept dependencies
- [ ] Add WithCircuitBreaker option function
- [ ] Write unit tests with mock circuit breaker
- [ ] Optionally add a manually invoked, non-CI integration test with a real
      circuit breaker when it adds useful evidence
- [ ] Update documentation with usage examples
- [ ] Create migration guide for existing users
- [ ] Add telemetry metrics for resilience monitoring
- [ ] Configure health checks to report degraded state
- [ ] Document service mesh integration patterns

---

## Performance & Scalability

### Optimization Strategies

#### 1. Parallel Execution
```go
func (e *SmartExecutor) ExecuteParallel(ctx context.Context, steps []PlanStep) map[string]interface{} {
    results := make(map[string]interface{})
    resultChan := make(chan struct{name string; result interface{}}, len(steps))

    var wg sync.WaitGroup
    for _, step := range steps {
        wg.Add(1)
        go func(s PlanStep) {
            defer wg.Done()
            result, _ := e.execute(ctx, s)
            resultChan <- struct{name string; result interface{}}{s.Name, result}
        }(step)
    }

    go func() {
        wg.Wait()
        close(resultChan)
    }()

    for r := range resultChan {
        results[r.name] = r.result
    }

    return results
}
```

#### 2. Intelligent Caching
```go
type RoutingCache struct {
    cache *lru.Cache
    ttl   time.Duration
}

func (c *RoutingCache) GetOrCompute(key string, compute func() (*RoutingPlan, error)) (*RoutingPlan, error) {
    // Check cache
    if cached, ok := c.cache.Get(key); ok {
        entry := cached.(*cacheEntry)
        if time.Since(entry.timestamp) < c.ttl {
            return entry.plan, nil
        }
        c.cache.Remove(key)
    }

    // Compute and cache
    plan, err := compute()
    if err == nil {
        c.cache.Add(key, &cacheEntry{
            plan:      plan,
            timestamp: time.Now(),
        })
    }

    return plan, err
}
```

#### 3. Connection Pooling
```go
type ComponentConnPool struct {
    pools map[string]*ConnectionPool
    mu    sync.RWMutex
}

func (p *ComponentConnPool) GetConnection(component string) (*Connection, error) {
    p.mu.RLock()
    pool, exists := p.pools[component]
    p.mu.RUnlock()

    if !exists {
        p.mu.Lock()
        pool = NewConnectionPool(component, 10) // Max 10 connections
        p.pools[component] = pool
        p.mu.Unlock()
    }

    return pool.Get()
}
```

### Metrics & Monitoring

```go
type OrchestratorMetrics struct {
    TotalRequests        int64
    SuccessfulRequests   int64
    FailedRequests       int64
    AverageLatency       time.Duration
    ComponentCalls       map[string]int64
    CacheHitRate         float64
    ParallelExecutions   int64
}

func (o *AIOrchestrator) recordMetrics(start time.Time, success bool, components []string) {
    duration := time.Since(start)

    o.metricsMutex.Lock()
    defer o.metricsMutex.Unlock()

    o.metrics.TotalRequests++
    if success {
        o.metrics.SuccessfulRequests++
    } else {
        o.metrics.FailedRequests++
    }

    // Update average latency
    o.metrics.AverageLatency = time.Duration(
        (int64(o.metrics.AverageLatency)*(o.metrics.TotalRequests-1) + int64(duration)) / o.metrics.TotalRequests,
    )

    // Track component usage
    for _, comp := range components {
        o.metrics.ComponentCalls[comp]++
    }

    // Emit metrics if telemetry available
    if o.telemetry != nil {
        o.telemetry.RecordMetric("orchestrator.request.duration", duration.Seconds(), map[string]string{
            "success": fmt.Sprintf("%v", success),
        })
    }
}
```

---

## Production Deployment

### Kubernetes Configuration

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: orchestrator
  namespace: truvag3-system
spec:
  replicas: 3
  selector:
    matchLabels:
      app: orchestrator
  template:
    metadata:
      labels:
        app: orchestrator
    spec:
      containers:
      - name: orchestrator
        image: truvag3/orchestrator:latest
        env:
        - name: REDIS_URL
          value: "redis://redis:6379"
        - name: OPENAI_API_KEY
          valueFrom:
            secretKeyRef:
              name: ai-secrets
              key: openai-key
        - name: TRUVAG3_CAPABILITY_SERVICE_URL
          value: "http://capability-service:8080"
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector:4318"
        resources:
          requests:
            memory: "256Mi"
            cpu: "100m"
          limits:
            memory: "1Gi"
            cpu: "1000m"
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /ready
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
```

### Environment Variables

```bash
# Required
export REDIS_URL="redis://redis:6379"              # Discovery service
export OPENAI_API_KEY="sk-..."                     # AI provider

# Optional - Capability Service (for scale)
export TRUVAG3_CAPABILITY_SERVICE_URL="http://capability-service:8080"
export TRUVAG3_CAPABILITY_TOP_K="50"
export TRUVAG3_CAPABILITY_THRESHOLD="0.75"

# Optional - Telemetry
export OTEL_EXPORTER_OTLP_ENDPOINT="http://otel-collector:4318"
export OTEL_SERVICE_NAME="orchestrator"

# Optional - Configuration
export TRUVAG3_EXECUTION_MAX_CONCURRENCY="10"
export TRUVAG3_EXECUTION_STEP_TIMEOUT="30s"
# StepTimeout contributes to delegated orchestrator allowance together with
# HITL.DefaultTimeout; it is not a standalone per-tool execution deadline.
# The shared HTTP timeout starts at TotalTimeout and can be raised to that
# delegation allowance. The enclosing request/phase context still bounds calls.
```

### Health Checks

```go
func (o *AIOrchestrator) HealthCheck() HealthStatus {
    status := HealthStatus{
        Status: "healthy",
        Checks: make(map[string]bool),
    }

    // Check discovery connection
    if err := o.discovery.Ping(); err != nil {
        status.Checks["discovery"] = false
        status.Status = "degraded"
    } else {
        status.Checks["discovery"] = true
    }

    // Check AI client
    if o.aiClient != nil {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if _, err := o.aiClient.GenerateResponse(ctx, "test", nil); err != nil {
            status.Checks["ai"] = false
            status.Status = "degraded"
        } else {
            status.Checks["ai"] = true
        }
    }

    // Check capability service if configured
    if o.capabilityProvider != nil {
        if _, err := o.capabilityProvider.GetCapabilities(context.Background(), "test"); err != nil {
            status.Checks["capabilities"] = false
            // Don't degrade if fallback is enabled
            if !o.config.EnableFallback {
                status.Status = "degraded"
            }
        } else {
            status.Checks["capabilities"] = true
        }
    }

    return status
}
```

---

## Common Patterns & Examples

### Pattern 1: Simple Q&A System

```go
func createQAOrchestrator() *orchestration.AIOrchestrator {
    // Minimal setup for Q&A
    discovery := core.NewLocalDiscovery()
    aiClient, _ := ai.NewClient()

    return orchestration.CreateSimpleOrchestrator(discovery, aiClient)
}

func handleQuestion(orchestrator *orchestration.AIOrchestrator, question string) string {
    response, err := orchestrator.ProcessRequest(
        context.Background(),
        question,
        nil,
    )

    if err != nil {
        return "Sorry, I couldn't process that question."
    }

    return response.Response
}
```

### Pattern 2: Multi-Tool Analysis

```go
func analyzeCompany(orchestrator *orchestration.AIOrchestrator, company string) (*Analysis, error) {
    request := fmt.Sprintf("Provide comprehensive analysis of %s including financials, news, sentiment, and technical indicators", company)

    response, err := orchestrator.ProcessRequest(
        context.Background(),
        request,
        map[string]interface{}{
            "priority": "high",
            "cache":    true,
        },
    )

    if err != nil {
        return nil, err
    }

    // Parse structured response
    var analysis Analysis
    if err := json.Unmarshal([]byte(response.Response), &analysis); err != nil {
        return nil, err
    }

    return &analysis, nil
}
```

### Pattern 3: Workflow-Based ETL

```yaml
name: etl-pipeline
inputs:
  source:
    type: string
    required: true
  destination:
    type: string
    required: true

steps:
  - name: extract
    tool: data-extractor
    action: extract
    inputs:
      source: ${inputs.source}
    retry:
      max_attempts: 3

  - name: validate
    tool: data-validator
    action: validate
    inputs:
      data: ${steps.extract.output}
    depends_on: [extract]

  - name: transform
    agent: transformation-agent
    action: transform
    inputs:
      data: ${steps.validate.output}
      rules: ${inputs.transform_rules}
    depends_on: [validate]

  - name: load
    tool: data-loader
    action: load
    inputs:
      data: ${steps.transform.output}
      destination: ${inputs.destination}
    depends_on: [transform]

outputs:
  records_processed: ${steps.load.output.count}
  status: ${steps.load.output.status}
```

### Pattern 4: Hybrid Intelligence

```go
func hybridOrchestration(
    orchestrator *orchestration.AIOrchestrator,
    discovery core.Discovery,
    backends *orchestration.OrchestrationBackends,
    logger core.Logger,
) error {
    // Use AI for exploration
    explorationResponse, _ := orchestrator.ProcessRequest(
        context.Background(),
        "What data sources should I use for Tesla analysis?",
        nil,
    )

    // Extract patterns from AI response
    patterns := extractPatterns(explorationResponse)

    // Create workflow from patterns
    workflow := createWorkflowFromPatterns(patterns)

    // Execute through a provider-neutral capability supplied by application bootstrap.
    requirements, err := orchestration.RequirementsForFeatures(
        nil,
        orchestration.BackendFeatureWorkflow,
    )
    if err != nil {
        return err
    }
    if err := backends.ValidateFor(requirements); err != nil {
        return err
    }
    engine := orchestration.NewWorkflowEngine(discovery, backends.Workflow(), logger)
    result, err := engine.ExecuteWorkflow(
        context.Background(),
        workflow,
        map[string]interface{}{"company": "TSLA"},
    )
    if err != nil {
        return err
    }
    _ = result // Application-specific result handling.
    return nil
}
```

`OrchestrationBackends` is supplied by the application composition root. The
Layer-1 `redisprovider.NewDefaultBackends` path resolves environment
configuration, constructs and validates selected roles, and returns an
ownership handle that the application closes after framework shutdown. Layer 2
keeps client configuration, owned clients, provider options, and
`redisprovider.NewOrchestrationBackends` separately callable. An application
may also assemble Layer-3 domain adapters directly. Runtime workflow
code consumes only the narrow `WorkflowStateStore` returned by `Workflow()` and never
imports or infers a storage provider. Direct Redis composition uses
`NewRedisStateStoreWithClient(client, keyspace, ttl)` with an application-owned
client. The obsolete `NewRedisStateStore` constructor is removed.
The proof-only `examples/orchestration-backend-portability` module implements
PostgreSQL workflow state and NATS command/task adapters using only these public
contracts, then validates a mixed Redis/PostgreSQL/NATS composition in an
isolated Kind cluster. Those internal example adapters are architectural
evidence, not advertised framework providers.
The preset is exercised by the in-tree skill-enabled applications. Processes
that need only skills use `WithDefaultBackendRoles(ClientRoleSkills)` so the
convenience path neither constructs nor validates unrelated backend groups, and
then pass only the narrow interfaces onward. `WireOrchestratorBackends` and
`SkillAdministrationDependencies` provide fail-fast composition-boundary
unpacking without passing the aggregate into runtime behavior.

---

## Troubleshooting Guide

### Common Issues and Solutions

#### Issue 1: AI Client Not Responding

**Symptoms**: Orchestrator fails with "AI client not configured" or timeouts

**Diagnosis**:
```go
// Check AI client availability
if orchestrator.aiClient == nil {
    log.Error("AI client is nil")
}

// Test AI client directly
response, err := aiClient.GenerateResponse(ctx, "test", nil)
if err != nil {
    log.Errorf("AI client error: %v", err)
}
```

**Solutions**:
1. Verify AI API key is set correctly
2. Check network connectivity to AI service
3. Ensure AI client is properly injected into orchestrator
4. Verify AI service rate limits aren't exceeded

#### Issue 2: Discovery Service Failures

**Symptoms**: Cannot find tools/agents, "no components available"

**Diagnosis**:
```go
// Check discovery connection
components, err := discovery.Discover(ctx, core.DiscoveryFilter{})
if err != nil {
    log.Errorf("Discovery error: %v", err)
}
log.Infof("Found %d components", len(components))
```

**Solutions**:
1. Verify Redis is running and accessible
2. Check that components are registering correctly
3. Ensure discovery refresh is happening (every 10s by default)
4. Verify network policies allow discovery traffic

#### Issue 3: Workflow Execution Hangs

**Symptoms**: Workflow starts but never completes

**Diagnosis**:
```go
// Enable debug logging
config.LogLevel = "debug"

// Add step monitoring
workflow.OnStepComplete = func(step string, duration time.Duration) {
    log.Infof("Step %s completed in %v", step, duration)
}
```

**Solutions**:
1. Check for circular dependencies in workflow
2. Verify all required inputs are provided
3. Ensure component timeouts are configured
4. Check for deadlocks in parallel execution

#### Issue 4: High Memory Usage

**Symptoms**: Orchestrator memory grows unbounded

**Diagnosis**:
```go
// Monitor metrics
metrics := orchestrator.GetMetrics()
log.Infof("Cache size: %d entries", metrics.CacheSize)
log.Infof("History size: %d records", len(orchestrator.GetExecutionHistory()))
```

**Solutions**:
1. Configure cache with appropriate TTL and size limits
2. Limit execution history buffer size
3. Ensure proper cleanup of completed executions
4. Check for goroutine leaks in parallel execution

#### Issue 5: Capability Service Overload

**Symptoms**: Slow responses when using service-based capability provider

**Diagnosis**:
```bash
# Check capability service health
curl http://capability-service:8080/health

# Monitor response times
time curl -X POST http://capability-service:8080/search \
  -H "Content-Type: application/json" \
  -d '{"query": "test", "top_k": 20}'
```

**Solutions**:
1. Increase capability service resources
2. Tune TopK parameter (reduce from 50 to 20)
3. Increase threshold to filter more aggressively
4. Enable fallback to default provider
5. Add caching layer for capability queries

---

## Future Considerations

### Potential Enhancements

1. **Streaming Response Support**
```go
type StreamingOrchestrator interface {
    ProcessRequestStream(ctx context.Context, request string) (<-chan ResponseChunk, error)
}
```

2. **Distributed Workflow Execution**
```go
type DistributedEngine struct {
    coordinator *ConsistentHash
    workers     map[string]*WorkerNode
}
```

3. **Visual Workflow Designer**
- Web-based UI for creating workflows
- Drag-and-drop component composition
- Real-time execution visualization

4. **Advanced Routing Strategies**
- Cost-based routing (minimize API costs)
- Load-based routing (distribute work evenly)
- Capability scoring (best fit selection)

5. **Event-Driven Orchestration**
```go
type EventOrchestrator struct {
    *AIOrchestrator
    eventBus *EventBus
}

func (o *EventOrchestrator) OnEvent(event Event) {
    // Trigger orchestration based on events
}
```

6. **Workflow Versioning**
```yaml
name: analysis-workflow
version: 2.0
compatible_with: ">=1.5"
migration:
  from: 1.0
  steps:
    - rename: old_step -> new_step
    - add: validation_step
```

### Areas for Research

1. **Predictive Caching**: Use ML to predict and pre-cache likely requests
2. **Adaptive Parallelization**: Dynamically adjust concurrency based on system load
3. **Semantic Workflow Discovery**: Find similar workflows based on intent
4. **Federated Orchestration**: Coordinate across multiple orchestrator instances
5. **Zero-Shot Planning**: Generate workflows for unseen request patterns

---

## Foundation Lifecycle and Configuration Contracts

Canonical construction resolves a complete `OrchestratorConfig` before request
execution. `NewDefaultOrchestratorConfig` is deterministic and environment-free;
`ResolveOrchestratorConfig` applies the explicitly selected environment mode and
then code options; `CreateResolvedOrchestrator` performs no environment reads.
Compatibility constructors retain documented environment bootstrap behavior and
emit bounded diagnostics for fallbacks.

Fatal resolution failures are not compatible fallbacks. In particular, invalid
cleanup or resource-selector attempt timeouts cannot discard otherwise valid
HITL, execution-store or phase settings. `DefaultConfigWithDiagnostics` returns
`Err` with a nil `Config`; `DefaultConfig` preserves that failure on a
non-executable configuration. Error-returning factories reject it before
bootstrap. Legacy errorless constructors return an inert instance that rejects
`Start` and every execution entry point; applications should prefer the
error-returning factories for synchronous startup validation.

Legacy partial-config constructors normalize absent cleanup to 5s and validate
explicit cleanup, failed-operation projection, skill and prompt invariants.
They retain unrelated legacy zero-value semantics; full resolved-limit validation
belongs to the canonical constructor. Construction snapshots caller configuration
rather than normalizing it in place.

`ResolveOrchestratorConfigIdentity` returns a sanitized summary plus explicit
cache eligibility. Prompt bodies, credentials, endpoints, raw environment values,
and provider extensions never leave through this surface. A deterministic
projection returns `CacheEligible=true` and a fingerprint. If any extension is
not deterministically representable, normal execution remains valid but
`CacheEligible=false` and `Fingerprint` is empty. Cache consumers must test the
eligibility bit rather than interpreting a fallback string as identity.

All public request entry points share one package-private lifecycle runner. The
runner owns request correlation, hooks, phase boundaries, synthesis, terminal
recording, and delivery-mode differences. Evolving per-request data is carried by
typed run state or a typed extension seam rather than ad hoc context values.
Execution-debug writes are defensively cloned and serialized per request by an
orchestrator-owned recorder. Each write is bounded by
`TRUVAG3_EXECUTION_STORE_WRITE_TIMEOUT` (five seconds by default); cancellation
and recorder draining are owned by the orchestrator lifecycle.

At a framework-owned HITL suspension, the included controller first stores a
checkpoint with the transient `preparing` status. Such a checkpoint is neither
pending nor resumable, and no interrupt notification is sent. The checkpoint
coordinator attaches the typed run snapshot, performs the authoritative save as
`pending`, and only then notifies the handler. Direct controller callers that do
not request orchestration enrichment retain the original pending-save-and-notify
behavior. Provider-neutral expiry processing implements `core.Runnable`, blocks
until cancellation even when disabled, and obtains atomic claims through
`ExpiredCheckpointSource`; storage adapters do not own background goroutines.
The claim lease is operational runtime configuration, while the claim-owner
length cap is a fixed coordination-protocol invariant.

### Framework-owned resume lifecycle

#### Request observation and transient activity ownership

Managed request segments attach `ExecutionLifecycleObservation` to execution
snapshots and summaries. `running` (revision 1) becomes `completed`, `failed`,
`cancelled`, or `suspended` (revision 2) only at the outer delivery finalizer.
Completion describes the request pipeline, not application disposition or tool
success. A deadline is failed; caller cancellation is cancelled; HITL is
suspended. Legacy/direct-plan records without this field have unknown lifecycle.
No fake initial plan is stored: before the first ordinary snapshot, live activity
and hooks are the observation surfaces. Resume uses a new linked request ID.

The request recorder assigns `RecordRevision` to each accepted snapshot. Late
hook-effect updates merge with the latest full record; they cannot restore an
earlier phase, response or lifecycle. Lifecycle-only settlement preserves the
latest phase evidence and error-retention floor. These are additive fields on
the existing provider-neutral `ExecutionStore`, not a task/workflow engine.

An activity announcement inside a managed request installs one request-owned
renewal loop. As with per-call resume-claim renewal below, this is joined
request work, not an application/background-service `Runnable`. Reannouncement
and status updates serialize the latest signal using its original request ID
and start time. Renewal runs every effective signal TTL / 3 (minimum one
nanosecond for representability); each submission is bounded by the smaller of
that interval and the configured execution-cleanup timeout. Operational inputs
remain `WithAnnouncementSignalTTL` / `TRUVAG3_ACTIVITY_SIGNAL_TTL` and
`WithExecutionCleanupTimeout` / `TRUVAG3_EXECUTION_CLEANUP_TIMEOUT`.

Every request exit cancels and joins renewal before removing the signal.
Removal and investigation cleanup share one cleanup deadline; custom providers
must honor context cancellation. Suspended requests advertise no active work;
process loss falls back to TTL. Direct hook callers retain their existing
announcement/cleanup ownership. This does not renew investigation claims.

Renewal/cleanup effects are registered pending during announcement and settled
by the owner. Counters describe submissions and returned errors, never inferred
backend health: the legacy activity provider's fail-open contract is unchanged.
Periodic span events attach to the live request span, not an ended hook span;
ordinary logs/events contain bounded classifications, not signal query text.

`HITLResumer`, `ResumeExecutor`, and `CheckpointResumePersistence` belong to
orchestration. `ResumeCoordinator` borrows persistence and an application
adapter; it imports no Redis types and creates no clients.
`BackendFeatureHITLResume` requires persistence plus atomic resume support.
The Redis provider exposes both through the same checkpoint adapter, existing
DB-0 keyspace, and agent hash slot.

The controller saves pending decisions using expected-status CAS. Publication
and `RecordCheckpointStatus` follow successful persistence, not a lost race.
Resume is no longer a controller method. The coordinator claims approved or
expiry-approved work, then calls `BuildResumeContext` exactly once. Durable
state is `resuming`; the adapter receives an isolated snapshot retaining the
preceding approval status and restored plan, parameters, results, and skills.

`ProcessRequestWithExecution` and `ProcessRequestStreamingWithExecution`
expose the actual terminal result through the existing shared runner and
terminal recorder. Partial results, synthesis/hook/delivery errors, and panics
are preserved. Successful tool steps alone cannot become resume success.

Renewal is per-call and joined on every exit. No resources survive between
calls, so the coordinator needs no `Close()` or `Start()`. HTTP/SSE follows
caller cancellation; queued work uses its worker context. Only cleanup after
execution settles uses a bounded detached context. Hosts drain calls before
closing borrowed dependencies. Defaults are a 30-second lease and 5-second
cleanup timeout; environment loading is explicit, outside construction.

Backend time and fencing govern ownership. The Redis adapter uses WATCH and
one same-slot transactional script, checking the lease again at commit. Only
optimistic contention is retried. Record/index retention is preserved; missing
records are not recreated, and generic saves/deletion cannot defeat ownership.
Renewal does not extend the human approval window or storage TTL.

Each attempt reserves a stable successor ID. Child persistence validates parent
ownership and records producing-attempt provenance. Continued finalization
verifies that child and marks its parent terminal `continued`. Recovery of an
expired claim returns a saved child without replaying the parent. Completion
requires successful terminal execution and persistence. Ordinary failure with
no child releases the previous approval; claim loss, ambiguous persistence
failure, and panic require inspection/recovery, not automatic replay. External
tool idempotency remains the application's responsibility.

The HTTP handler's optional resumer controls route registration; invalid
explicit dependencies fail construction. HTTP/SSE/task terminal success follows
durable finalization. `CheckpointResponse` exposes original payload fields and
parent/successor navigation without framework ownership fields. It does not
scan or alter application data. Attempt/owner IDs remain internal log/trace
fields, never metric labels or added baggage. Existing linked-trace,
conversation, skill restoration, and expiry-delivery contracts are retained.

The optional `redisprovider` package owns Redis-specific client/preset
composition plus the skills and distributed-lock adapters; root orchestration
continues to own the Redis adapters that remain colocated with their backend
contracts.
`NewDefaultBackends` is its Layer-1 path: it delegates to the lower layers,
constructs only explicitly selected roles when requested, validates every
capability promised by those roles, and returns `OwnedBackends` for deterministic
client cleanup. `WithDefaultBackendClientConfig`,
`WithDefaultBackendProviderOptions`, and `WithDefaultBackendOverrides` retain
code-over-environment precedence and per-capability replacement without forcing
the caller to abandon the convenience layer.
`NewOptions` and `NewOrchestrationBackends` are deterministic and environment-
free. `LoadOptionsFromEnvironment` applies the preset's execution-debug and LLM-debug retention,
checkpoint retention, workflow TTL, task-queue service scope and retry,
task-index reconciliation, and schedule-capacity limits; `ConfigureOptions`
applies later code overrides, including `WithTaskQueueScope`. `WithLogger`
propagates one application-owned logger to every
logging-capable adapter assembled by the preset; each adapter retains its
standard `framework/orchestration` component attribution and NoOp behavior when
no logger is supplied. Root orchestration contracts and lifecycle code do not
import the preset package or Redis clients.

Backend validation is closed over actual runtime dependencies. A scheduler
producer requires schedule and task stores, a dispatcher, and a distributed
lock. Canonical checkpoint expiry requires persistence, an atomic expired-item
source, and a runnable processor. The processor occupies a typed composition
slot; generic runnables append to the lifecycle set. Replacing either checkpoint
dependency invalidates the previous processor, and the Redis preset reconstructs
its default against final caller overrides.

Direct Redis constructors that receive an application-owned client never close
that client. For the execution-debug, LLM-debug, checkpoint, and command stores,
URL-owning constructors resolve the standard DB-0 shorthand or structured
topology through Core and close only their own clients; `WithClient`
constructors leave connection ownership to application bootstrap.

Prefix behavior is explicit per adapter. Canonical provider composition passes
one `core.RedisKeyspace` and a validated agent scope to command and checkpoint
stores, so both use the same deployment-scoped HITL tag. Direct Redis adapters
use typed keyspace options as well; raw HITL, schedule, skills, execution, and LLM-debug
prefix paths are removed. The generic non-Redis execution storage adapter may
still use its logical `ExecutionStoreConfig.KeyPrefix`; Redis construction
requires its default or an explicit `WithExecutionDebugKeyspace` override.
Task and workflow adapter constructors take their keyspace through explicit
configuration and never infer another logical database.

**Legacy exception — pending dedicated audit:** Some error paths that predate
the framework payload-fidelity policy still sanitize downstream failure bodies
or error messages before they reach retry prompts, logs, trace events,
execution-debug records, skills debug records, or returned observations. The
executor currently uses `core.RedactSensitiveText` for a failed component body
and `core.RedactSensitiveError` for its Go error; the latter retains the original
cause for `errors.Is` and `errors.As` while exposing a transformed message.
Skills authoring transforms an AI error returned to its caller, and skills AI
observability transforms error text recorded for debugging. The Redis topology
work retired the automatic transformations previously used by the two
URL-owning execution- and LLM-debug constructors; their bounded startup errors
now preserve the original cause. Existing transformations remain unchanged in
skills-backend construction, skill-store returned/logged failures, and
distributed-lock operational logging. Those retained paths are still legacy
exceptions pending the broader audit.

This paragraph records current implementation behavior; it is not a normative
sanitization guarantee and must not be copied into new or modified paths.
Adopters remain responsible for payload classification and protection. Removal
or redesign is deferred to a dedicated audit that must check retry behavior, Go
error identity, stored evidence, returned observations, and log/trace output
across every framework call site.

---

## Redis/Valkey Backend Contract

Orchestration runtime behavior consumes provider-neutral backend interfaces.
Redis adapter implementations currently live in two places: workflow, HITL,
schedule/task, execution-debug, and LLM-debug adapters remain in the root
orchestration package, while the skills and distributed-lock adapters plus the
default Redis client/preset composition live in the optional
`orchestration/redisprovider` package.
Portability comes from interface consumption and replaceable application
composition, not from physical separation of every Redis implementation. The
provider package may depend on root orchestration; root orchestration must not
import the provider package. `TestBackendPackageDependencyDirection` enforces
that one-way dependency.

Canonical application composition resolves one `core.RedisConnectionConfig`
and one `core.RedisKeyspace`, creates a `redis.UniversalClient`, and injects it
into the selected backend roles. The default bundle shares one DB-0 client;
`WithRoleClient` remains the explicit seam for separate physical endpoints.
Standalone, Sentinel, and cluster modes therefore use the same adapter code.
Pool size is per connected node. An injected client is application-owned and
is never closed by an adapter. `OwnedBackends` closes only clients created by
the provider and does so idempotently.

Canonical keys begin `truvag3:v1:<deployment>:<subsystem>`. Braces delimit only
the Redis cluster hash tag; the deployment remains outside the tag as part of
the stable ACL/diagnostic prefix. Atomic scopes are deliberate:

```text
workflow:        {...:workflow:<workflow-id>}:execution:<execution-id>
HITL:            {...:hitl:<agent-scope>}:checkpoint|request|claim|command:...
schedules:       {...:schedules}:data:<id>, index:all, index:due
skills:          {...:skills}:...
execution debug: {...:execution-debug:<request-id>}:record|retention
LLM debug:       {...:llm-debug:<request-id>}:meta|interactions|retention-floor
```

Workflow state is scoped by both workflow and execution identity. Schedule and
skills operations deliberately concentrate their bounded control-plane data in
one deployment slot. HITL concentrates one agent scope per slot so checkpoint,
pending, request, claim, and command transactions remain atomic. Task queue to
processing-list handoff is intentionally multi-command because the lists may
occupy different slots; its documented retry windows allow duplicates but not
silent loss.

Request-correctness paths never rely on cluster-node-local keyspace `SCAN`,
`KEYS`, or arbitrary cross-slot `MGET`. Task status and all-services indexes use
`SSCAN`; record hydration uses ordinary pipelines so go-redis routes individual
commands. Schedules maintain bounded explicit all/due indexes and reject writes
over `TRUVAG3_SCHEDULER_MAX_SCHEDULES`. Task indexes are explicit projections
repaired by a `core.Runnable` reconciler configured with
`TRUVAG3_TASK_INDEX_RECONCILE_INTERVAL`. That application-owned maintenance
path is the one deliberate keyspace-scan exception: it rotates fairly across
the explicit task indexes and every Redis Cluster primary, keeps persistent
cursors, and buffers oversized `SCAN`/`SSCAN` pages so each pass observes its
strict configured task-ID bound. Request paths never call it.

Execution and LLM-debug records are authoritative request-local writes. Recent,
trace, and conversation indexes are advisory deployment-wide projections and
are updated separately to avoid `CROSSSLOT`. Projection failure is classified
with fixed, bounded diagnostics; it does not repeat or falsify a successful
authoritative write. Registry Viewer uses exported key builders and pipelined
single-key reads, and may prune only confirmed-missing projection members.

Redis store constructors scope component-aware loggers to
`framework/orchestration`, including direct injected-client construction.
Request-path retry, decode, schedule, task, and projection records use the
caller context and explicit request correlation; raw Redis and stored-payload
errors are not copied into ordinary logs. Detached bounded maintenance uses a
non-context logger and does not manufacture or retain request trace context.

The development-stage cleanup removes role-database options, the duplicate
Redis URL alias, obsolete Redis key-prefix options, execution-debug option aliases,
and the execution-ID-only workflow adapter. `StateStore` aliases the canonical
`WorkflowStateStore`: lookup and step mutation require both workflow and
execution IDs. This follows the author-approved exception in the framework
principles; there is no compatibility waiting period or data-migration layer.
Direct URL-owning constructors remain a supported convenience path and use
the same DB-0 resolver/factory. Removed settings fail with bounded configuration
errors for selected roles. Dependency, ownership, retention, logging, and
caller-owned span-error contracts remain unchanged.

The skills adapter has one key path, including direct construction through
`WithSkillStoreKeyspace`: `truvag3:v1:<deployment>:skills:{<deployment>:skills}:…`.
The former `WithSkillStoreKeyPrefix` / `:{store}` branch is removed. Skill
package namespaces are identities within that deployment, not replacements for
the deployment namespace. Task adapters retain their explicit queue-key and
prefix composition seams; callers derive those values from `RedisKeyspace`, as
the provider preset does. These are supported routing primitives, not a
numbered-DB compatibility mode.

### Real-topology verification contract

The existing CI workflow and its complete default unit-test selection remain
the mandatory gate. CI does not use `-short`, so no default unit-test path is
omitted from that gate. Isolated contracts use narrow mocks, fakes, miniredis,
and in-memory telemetry exporters; they cover successful behavior, error paths,
ownership, and concurrency without introducing real-service dependencies.

Real-topology verification is optional, manually invoked supplemental evidence,
never a CI job or a substitute for unit coverage. The existing
integration-tagged matrix runs the same provider-neutral workflow, HITL,
command, execution, LLM-debug, scheduling, lock, and skill conformance surfaces
against three-primary/three-replica
clusters for Redis OSS 8.8.0, Valkey 8.1.9, and Valkey 9.1.1 in DB 0. It also
asks the server for key slots, proves a cross-slot command is rejected, moves a
slot under load, performs a coordinated primary failover, and requires the
client to refresh topology without exposing a domain error.

A separate manually invoked Sentinel test writes through the complete provider
composition, confirms replication, terminates the primary, and requires reads
to recover through the promoted replica. The cluster fixture uses Linux host networking
because Redis and Valkey Cluster advertise node endpoints and do not support
ordinary remapped/NAT ports for this topology. These tests require both
`-tags integration` and their explicit environment opt-ins; they are absent
from default `go test ./...` selection and skip explicitly on unsupported
hosts. Unit tests do not claim to prove real server redirects or failover.

An additional manual opt-in runs the accepted single-slot registry capacity
profile: 2,000 services with five unique capabilities, 133.3 heartbeats/s, 100
filtered discoveries/s, and one unfiltered discovery/s for two minutes. The
gate requires zero domain errors and the documented p99 bounds. Capacity
threshold changes are architecture decisions and must record a new workload
and measurement basis.

---

## Agent Skills V1

Agent skills are reusable, versioned instruction packages owned by
orchestration. They add planning guidance, response guidance, capability hints,
and independently loadable text resources without granting a tool, permission,
or new execution authority. `core` remains skill-agnostic.

An application explicitly binds eligible skills through `SkillConfig.Bindings`
and injects a provider-neutral `SkillRegistry`. `TRUVAG3_SKILL_BINDINGS_JSON`
is a deployment-time complete replacement for the code binding list; there is
no runtime binding CRUD or replica-local merge. The included Redis adapter is
composed in `orchestration/redisprovider`, while runtime lifecycle code imports
no Redis types.

For every request, orchestration performs one authoritative batched candidate
resolution before `BeforePlanning` hooks. Mutable aliases such as `published`
are resolved to exact version-and-hash identities and pinned for the execution.
Host code may add bounded request-local expected-capability hints with
`WithSkillExpectedCapabilities`; they are normalized and pinned with the
candidate snapshot, influence selection only, and never grant a capability,
tool, or permission. V1 does not infer them from user text or generated plans.
The generic pipeline cache gate receives one opaque `skills` variation
fingerprint. For a binding that targets `published`, a publish therefore
affects the next request without Pub/Sub or replica invalidation; a numeric
version binding remains fixed, and an in-flight or resumed execution retains
its pinned identities. Only verified immutable manifest and resource bodies
may be held in the optional bounded process-local cache.

Every manifest and resource is verified against its pinned hash before use. A
cached mismatch is evicted and reread once by exact version; a persistent
mismatch is unavailable and is never projected or cached. V1 has no
`allow_unverified` mode. Required-policy failures stop the applicable boundary;
optional content is omitted with bounded diagnostic evidence.

Cached-answer short-circuits are eligible only when the skill behavior identity
is stable: either `SkillConfig.RuntimePolicyID` identifies the custom runtime,
or no custom activation policy, resolver, or token counter is installed and
both selector models are explicitly pinned. Ineligible configurations execute
normally and may still use the immutable-content cache; they only bypass
response-cache reuse.

Only short-circuits explicitly classified as cached responses are subject to
that comparison; authoritative policy/guardrail short-circuits pass unchanged.
The reserved `skills` dimension is compared symmetrically, so a value present
on only the current request or only the cached entry is a mismatch. This also
prevents a cached skill-influenced answer from being served after skills are
disabled.

Runtime prompt-admission estimates use the injected `core.TokenCounter` when
configured through `WithSkillTokenCounter`, otherwise the framework heuristic.
Invalid custom output falls back to that heuristic with a bounded diagnostic.
Because a custom counter can change projection, it requires the code-owned
`SkillConfig.RuntimePolicyID` only when response-cache eligibility is desired.

Skill disclosure follows named lifecycle boundaries:

1. `always` bindings and trusted host-only `explicit` requests activate
   deterministically; remaining `auto` candidates may use the bounded selector.
   Activation is monotonic: a skill activated initially or during continuation
   remains active for the rest of the execution. `required` controls failure
   handling and never forces activation.
2. Initial and continuation planning receive planning instructions and only the
   resources selected for that boundary. Authored `applies_to` scopes filter
   eligibility; `required_when_selected` never forces selection but makes a
   selected resource's load, integrity, or admission failure fatal.
3. Regeneration reuses the accepted phase projection exactly; it does not
   reselect or reread content.
4. Synthesis receives response instructions and synthesis-scoped resources.
5. Checkpoints retain only body-free pinned state and selection evidence.

Resume revalidates checkpointed exact tuples in one batch and never repins them
to a newer publication or adds a newly configured binding. A legacy checkpoint
with no skill fields, or an explicitly empty compatibility snapshot, remains
skill-free without a registry read, cache dimension, or skill prompt
projection. That decision is carried by private provenance installed by
`BuildResumeContext`; setting the public `WithResumeMode` flag alone cannot
suppress developer-configured skills.

The current runtime behavior/cache projection is recomputed for decisions made
after resume. A selector-model or policy deployment change while the request is
suspended is diagnostic and does not reject the resume; it cannot rewrite the
checkpointed exact skill tuples.

Framework-generated `<active_skills>` data is placed in the user message under
an immutable system-level `<skill_precedence>` contract. Dynamic request,
history, memory, tool, and retrieved values are encoded before rendering.
Reserved skill tags cannot be supplied by prompt overrides, while developer
guidance remains available through bounded additive fields and mounted-file
environment variables. With skills disabled or unbound, prompt bytes and the
ordinary lifecycle remain unchanged.

The included activation, resource-selection, and authoring-advisor calls leave
provider-native response format unset. Their provider-neutral structured-output
contract is a fixed JSON prompt, strict decoding and identity validation, and
one bounded correction retry for activation and authoring. Resource selector v2
accepts a complete outer JSON/unlabelled fence, retaining strict body validation,
and permits a child-attempt timeout retry within two total attempts. Its duration
limit is `SkillConfig.Limits.ResourceSelectionAttemptTimeout`, configured by
`TRUVAG3_SKILL_RESOURCE_SELECTION_ATTEMPT_TIMEOUT`; zero inherits the parent.
Optional omission is observed distinctly from selection; parent cancellation is
terminal. Zero is omitted from budget serialization; nonzero limits and the v2
policy version participate in the existing cache identity. The corresponding AI-option overrides accept
only model and reasoning-effort intent; an application that requires a
provider-native response protocol supplies a custom resolver or advisor.

The provider-neutral `SkillAdminHandler` supplies schema, deterministic
validation, optional non-mutating AI analysis, publication, catalog/history
reads, and guarded version deletion. Publication uses `If-None-Match: *` for
creation or the current `If-Match` ETag for update. Deletion requires the
current ETag and an audit reason; the published and immediately preceding
versions are protected. Mutations commit before audit delivery; a subsequent
audit failure is reported truthfully as `audit_recorded: false` with a bounded
warning and does not roll back the committed mutation. V1 authoring payloads
are JSON packages; `SKILL.md` import/export interoperability is deferred.
Authentication, authorization, and network exposure remain responsibilities of
the hosting application and platform.

V1 has one operational state, `published`. A content-changing accepted `PUT`
atomically creates and publishes the next immutable revision; an identical
canonical package is a no-op. There are no draft, rollback, archive, or
automatic garbage-collection workflows. Recovery is roll-forward, with older
unprotected revisions retained until an explicit guarded deletion.

Ordinary traces, metrics, logs, and execution debug records contain bounded
identity and decision evidence but never instruction or resource bodies. Jaeger
shows `orchestrator.skills.*`, `skills.registry.*`, `skills.store.*`, and
`skills.admin.*` spans. Execution records expose candidate, activation,
resource-selection, content-load, projection, and diagnostic summaries through
the Registry Viewer execution **Skills** tab. Content-load records include
body-free source/cache disposition, integrity hashes, attempt/retry outcome,
byte and canonical-token estimates, duration, and bounded diagnostics.

---

## Summary

The orchestration module is the brain of the TruvaG3 framework, coordinating tools and agents to accomplish complex tasks. Its architecture emphasizes:

1. **Clean Separation**: Interface-based dependencies prevent coupling
2. **Progressive Enhancement**: Start simple, add complexity as needed
3. **Production Readiness**: Built-in resilience, monitoring, and scaling
4. **Flexibility**: Support for both AI-driven and workflow-based orchestration
5. **Performance**: Automatic parallelization, caching, and optimization

The module follows the framework's design principles religiously, ensuring that it remains modular, testable, and maintainable while providing powerful orchestration capabilities.

Remember: **The orchestration module imports only `core` and the explicitly
allowed `telemetry` module**. AI, memory, resilience, storage, and other sibling
behavior reaches orchestration through narrow interfaces and application
composition. This is not a limitation but a strength that enables true
modularity and flexibility.

---

## Version History

Version 1.47 (2026-10-11): preserve fatal configuration errors through legacy
helpers and constructors; enforce cleanup/projection invariants before component
wiring, normalize absent direct cleanup, and preserve caller-owned configuration.
Clarify the still-unwired core typed/JSON shared-memory configuration boundary.
Guard built-in activity-compaction multiplication at construction and invocation,
preserving custom compactor rules; distinguish cleanup-timeout diagnostic reasons.

Version 1.46 (2026-10-08): centralize generous defaults: planning 20000,
synthesis 30000, synthesis skill input 6000 tokens; 20 phases at 25m,
15m delegation execution allowance, 20m outbound HTTP, 64 KiB per-result and
192 KiB aggregate result views. Event summaries default to 10000 output tokens;
explicit per-step calculation and fixed-budget overrides remain available.
Parent deadlines, validation, retries, telemetry and domain-independent execution
contracts are unchanged. Larger default values change configuration fingerprints.

Version 1.45 (2026-10-04): classify transient failures before analyzer,
contextual and legacy correction; keep configured budgets but narrow correction
eligibility. Explicit false stops retries, non-transient 4xx cannot be resent
unchanged, and approved inputs permit only eligible unchanged resends.
Delegation stays single-dispatch. Debug interactions preserve exact finish/source/separate reasoning
facts supplied by AI adapters, without changing planning or result acceptance.

Version 1.44 (2026-10-02): additive summary failure inputs and bounded views;
record-only request entity extraction; occurrence/recording provenance and
versioned historical detail/digest views. Executor and query algorithms unchanged.

Version 1.43 (2026-10-02): pair unavailable-agent rejection records with the actual
rejected attempt's effective prompt/system, raw response and validated catalog;
preserve planning feedback, retry budgets and deterministic diagnostic semantics.

Version 1.42 (2026-10-02): consolidate the active iterative terminal example and
carry latest parse failure through initial unknown-agent recovery using its existing
budget and selected context. Preserve the phase fallback, validation and
executor; add reasoned correction/exhaustion telemetry and regression coverage.

Version 1.41 (2026-10-02): align default and agent-name retry no-work guidance with
explicit model terminal intent; qualify active/legacy omission instructions.
Preserve decoder, typed-plan, executor and retry behavior.

Version 1.40 (2026-10-02): reject missing/blank questions in fresh model
clarification objects, including mixed-intent responses; preserve question bytes,
typed/direct-plan compatibility and existing correction budgets.

Version 1.39 (2026-10-01): fresh model-plan decoding requires recognized routing
intent, preserves typed/direct/resume contracts and payload fidelity, and reports
bounded actionable envelope diagnostics within existing correction budgets.

Version 1.38 (2026-09-29): independent request lifecycle observations, monotonic
record/effect merging, and request-owned activity renewal with joined all-exit
cleanup. Legacy/direct-plan lifecycle remains unknown.

Version 1.37 (2026-09-29): request-segment-owned investigation leases, bounded
all-exit cleanup, private ownership and fresh resume acquisition. Preserve legacy
direct-hook/custom-coordinator behavior and original request outcomes.

Version 1.36 (2026-09-29): fail-fast numeric memory-hook construction with an
explicit error return and synchronous application handling; preserve absent
defaults, direct-option sentinels, ownership and runtime memory fail-open.

Version 1.35 (2026-09-29): classify declared prior-phase template inclusions
without planner-error warnings; preserve legacy totals and add reason telemetry.
Restore symmetric complete summary prose envelopes without changing payloads or
truncated-prefix recovery.

| Version | Date | Changes |
|---------|------|---------|
| 1.34 | 2026-09-29 | Compact self-contained request-local memory detail with budgeted tuple legend and view identity; stored events, compactor/cache semantics and section priority unchanged |
| 1.33 | 2026-09-29 | Shared complete event-summary output contract and additive retained-summary entity-field coverage; preserved legacy outcomes, custom extraction, raw evidence and bounded telemetry |
| 1.32 | 2026-09-28 | Call-local partial event-summary recovery and semantic outcomes; empty digest fallback; recorder coverage and actual trace identity; custom/extracted entity seams preserved |
| 1.31 | 2026-09-28 | Complete-record historical view budgeting and working enrichment-limit builder wiring; view-v3 cache identity; one terminal resource omission with separate attempt diagnostics |
| 1.30 | 2026-09-28 | Compact output-field visibility; origin-labelled historical memory and versioned digest views; resource-selector v2 with configurable attempt deadlines, one shared retry budget, strict outer-fence handling and explicit omission diagnostics |
| 1.29 | 2026-09-27 | Documented payload-preserving plan parsing and string-aware syntax recovery; corrected the dispatch guard description to reserved framework templates |
| 1.28 | 2026-09-11 | Added provider-neutral fenced HITL resume coordination, real terminal-result access, expected-status decisions, public checkpoint projection, and explicit transport/cancellation ownership |
| 1.27 | 2026-09-11 | Added panic-safe per-hook completion, partial-state transfer to the existing request recorder, and phase/native-synthesis span cleanup while preserving the original panic and asynchronous effect semantics |
| 1.26 | 2026-09-10 | Preserved failed terminal results for invalid early decisions and partial work for later planning errors, using existing retention/publisher semantics without changing HITL suspension |
| 1.25 | 2026-09-10 | Added normal-return hook decisions to existing execution records and spans, distinguishing invocation status from pipeline consequence while preserving ordered effect publication and backend contracts |
| 1.24 | 2026-09-10 | Added construction-validated, terminal `RequiredAfterPlanningHook` composition and fail-closed propagation before persistence, HITL, and execution; preserved prior-phase audit state and failed-record retention through the existing terminal recorder |
| 1.23 | 2026-09-08 | Removed numbered-DB, obsolete raw-prefix (including skills), duplicate execution-debug options, and execution-ID-only workflow compatibility paths under the development-stage exception; preserved typed composition, explicit task routing, scoped workflow identity, and observability/ownership contracts |
| 1.22 | 2026-09-08 | Preserved the unchanged CI workflow and full unit-test selection as the primary gate; real-topology and capacity fixtures are integration-tagged, optional manual verification, superseding the mandatory integration-gate policy in 1.14 |
| 1.21 | 2026-09-08 | Gated enrichment/effect serialization on active debug capture and made HITL checkpoint/command adapters preserve caller-owned span errors while emitting bounded, correlated diagnostics |
| 1.20 | 2026-09-04 | Aligned direct Redis stores with component-scoped, request-correlated, bounded logging and clarified that detached projection maintenance neither inherits nor manufactures request trace context |
| 1.19 | 2026-09-04 | Aligned hook evidence with observability contracts: complete-boundary hook spans, linked and correlated asynchronous knowledge extraction, component-scoped logging, and strict separation between bounded trace/log signals and exact execution-debug effects |
| 1.18 | 2026-09-04 | Added versioned provider-neutral hook effects, exact enrichment capture, asynchronous terminal-record rewrites, and explicit producer-observation semantics for fail-open backends |
| 1.17 | 2026-09-03 | Made request-local pipeline-hook outcomes part of provider-neutral execution evidence so debugging consumers no longer query tracing infrastructure to reconstruct hook lifecycles |
| 1.16 | 2026-09-02 | Made Layer-2 Redis composition environment-free by resolving task-queue service scope in Layer 1 and documented explicit code-over-environment queue scoping |
| 1.15 | 2026-09-01 | Restricted projection cleanup to confirmed-missing records, rejected cluster schedule raw prefixes, and documented the fair, strictly bounded, all-primary task-index maintenance scan needed to recover record-only writes |
| 1.14 | 2026-09-01 | Made the Linux real-topology Redis OSS/Valkey cluster matrix, Sentinel failover, live reshard/failover, and 2,000-service registry capacity profile mandatory implementation gates |
| 1.13 | 2026-08-31 | Added the DB-0 standalone/Sentinel/cluster backend contract, versioned slot-safe schemas, explicit indexes and reconciliation, advisory debug projections required to avoid `CROSSSLOT`, shared-client ownership, and the scoped retirement/inventory of Redis diagnostic transformations |
| 1.12 | 2026-08-30 | Recorded the external PostgreSQL/NATS mixed-composition proof and its non-provider status |
| 1.11 | 2026-08-30 | Added the layered Redis backend composition contract, explicit ownership handle, role-selective Layer-1 construction, and canonical LLM-debug preset wiring |
| 1.10 | 2026-08-29 | Documented governed terminal-response evidence separately from raw LLM synthesis, including workflow-mode and historical-record absence cases |
| 1.9 | 2026-08-28 | Clarified available-lineage retention extension and direct Redis option normalization, documented coupled record/projection updates, and explicitly inventoried the returned skills-authoring AI error among the legacy payload-fidelity exceptions pending audit |
| 1.8 | 2026-08-28 | Reconciled exact-payload ownership with explicitly labeled legacy error transformations pending a separate audit; prohibited treating those paths as an adopter-facing sanitization guarantee |
| 1.7 | 2026-08-28 | Made missing lineage evidence breaker-neutral and retention-link-only Store failures best-effort while preserving full-record fallback and sanitized diagnostics |
| 1.6 | 2026-08-28 | Replaced request-local LLM write ordering with a cross-process retention floor; documented recursive retention links, typed execution absence, independent timeouts, and bounded floor writes |
| 1.5 | 2026-08-28 | Documented atomic minimum retention, execution/HITL lineage promotion, final-outcome LLM retention, provider capability, and adopter-owned debug-data protection |
| 1.4 | 2026-08-12 | Added the shipped Agent Skills V1 ownership, request pinning, progressive-disclosure, prompt, management, backend, and observability contracts |
| 1.3 | 2026-08-09 | Documented canonical configuration/cache eligibility, shared request recording, authoritative HITL suspension, and provider-neutral expiry lifecycle contracts |
| 1.2 | 2026-08-07 | Corrected the summary to state the canonical `orchestration -> core + telemetry` module boundary |
| 1.1 | 2026-07-27 | Established the pre-release conversation-resolution, checkpoint, and optional execution-store capability contracts |
| 1.0 | 2025-09-28 | Initial architecture documentation |

### Terminal result correlation and bounded event summarization

Terminal execution results carry `MetadataKeyExecutionRequestID` (`execution_request_id`)
for their owning request segment, whether successful, failed or interrupted. Terminal
storage snapshots and returned results receive copy-on-write metadata; an existing
phase snapshot and its publisher are preserved. Resume segments use their own ID,
not `original_request_id`. No execution is fabricated for an absent result; older
records may lack the key. Applications may bind this ID with `WithRequestID` when
projecting failure evidence after the request returns.

The builtin event summarizer bounds only its AI invocation to 2m by default.
Its 10000-token base generation allowance is per batch, not per entry. A separate
1024-byte stored-prose limit (`WithSummarizerSummaryMaxBytes`, builder environment
`TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_SUMMARY_MAX_BYTES`, minimum 32) marks
rune-safe shortened summaries and exposes counts in coverage/log/span evidence.
Exact entity IDs and pre-parse raw debug completions are untouched. Custom
summarizers retain ownership of their output policy.
`WithSummarizerTimeout` configures direct construction; `BuildMemoryHooks` resolves
`TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_TIMEOUT` before optional-dependency checks.
Absent values use the constructor default; explicit empty, malformed, non-positive
or overflowing durations fail construction. Shorter enclosing deadlines win.
A child timeout reports `summary_timeout` and the recorder falls back once per
uncovered step using the live parent context. Parent cancellation reports
`parent_cancelled` and prevents fallback writes. Successful late-returning responses
are still parsed. Custom summarizers and the default 20-step batch remain unchanged.
