# TruvaG3 Orchestration Module Architecture

**Version**: 1.9
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
15. [Agent Skills V1](#agent-skills-v1)
16. [Summary](#summary)
17. [Version History](#version-history)

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
    github.com/truvaagents/truva-g3/core v0.4.0
    github.com/truvaagents/truva-g3/telemetry v0.4.0  // Allowed for observability
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

Layer 0 runs at plan time, before any step dispatches. Layers 1–4 run at step execution time. A dispatch-time guard sits immediately before the HTTP call and fails the step if any `{{…}}` token survived the runtime layers.

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

Runs once per plan, before the DAG executes, and rejects plans whose template references cannot succeed at runtime. A rejection feeds back into the planner for regeneration instead of letting a doomed step reach the tool.

| Check | Purpose |
|---|---|
| Template shape | Every `{{…}}` must match `{{stepId.fieldPath}}`; unknown framework macros (e.g. `{{today_plus_1}}`) are rejected |
| Cross-phase awareness | A template reference is valid if the step exists in the current plan **or** in a completed prior phase |
| `depends_on` ⊆ templates | Every step ID referenced by a template must appear in `depends_on` (same-phase) or `implicit_deps` (prior-phase) |

**Implementation**: `validateTemplatePaths`, `validateNoUnknownMacros`, `validateDependencyConsistency` in `executor.go`. The pre-dispatch guard shown in the diagram above is the runtime safety net for anything that slips past Layer 0.

#### Layer 1: Auto-Wiring (No LLM Cost)

Fast, deterministic matching with zero LLM calls. Handles the common case where upstream and downstream tools use compatible naming.

| Strategy | Example | Match Type |
|---|---|---|
| Exact name | `"lat"` → `"lat"` | `exact` |
| Case-insensitive | `"LAT"` → `"lat"` | `case_insensitive` |
| Nested extraction | `{"code":"EUR","name":"Euro"}` → `"EUR"` | `nested_extraction` |
| Type coercion | `"48.85"` (string) → `48.85` (number) | (applied on top) |

**Implementation**: `AutoWirer` in `auto_wire.go`

#### Layer 2: LLM Micro-Resolution (On-Demand)

When Layer 1 leaves **required** parameters unmapped (names don't match), the `HybridResolver` triggers a lightweight LLM call to infer the correct mapping from available source data.

- Only fires when required params are unmapped (optional params don't trigger it)
- LLM receives: source data keys/values, target parameter schema, step instruction context
- Handles ordinal references ("first company", "second stock") via instruction context
- Auto-wired values take priority — LLM cannot overwrite what was already matched

**Implementation**: `MicroResolver` in `micro_resolver.go`, orchestrated by `HybridResolver` in `hybrid_resolver.go`

#### Layer 3: Error Analysis (Post-Failure)

When the HTTP call to a tool returns a 4xx error, the `ErrorAnalyzer` uses an LLM to inspect the error response and determine if parameters can be corrected.

- Analyzes error message semantics (e.g., "invalid currency code")
- Determines if the error is fixable (bad params) vs. permanent (resource not found)
- Can suggest corrected parameter values
- Identifies transient errors (503, timeouts) for simple retry

**Implementation**: `ErrorAnalyzer` in `error_analyzer.go`

#### Layer 4: Contextual Re-Resolution (Semantic Retry)

When Layer 3 cannot fix the error (lacks source data context), the `ContextualReResolver` attempts recovery using the **full execution trajectory**: user query, source data from dependencies, attempted parameters, error response, and capability schema.

- Computes derived values (e.g., `100 shares × $468.28 = $46,828`)
- Re-interprets source data given the error context
- Configurable via `TRUVAG3_SEMANTIC_RETRY_ENABLED` and `TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS`
- Optionally runs for independent steps (no dependencies) via `TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS`

**Implementation**: `ContextualReResolver` in `contextual_re_resolver.go`

#### Layer 5: Human-in-the-Loop (HITL)

When a step reaches a HITL checkpoint, parameters are presented to a human for approval. The human may approve as-is or provide corrected values. On resume, these user-provided parameters bypass all other resolution layers.

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

Pipeline hooks (`core.PipelineHook`) provide per-stage middleware for context engineering. Hooks are registered via `OrchestratorDependencies.PipelineHooks` in the factory and run sequentially at each stage. Errors are logged and skipped — they never abort the pipeline.

Before hooks run, orchestration also performs a shared enrichment-preparation pass for known metadata inputs. This is how raw conversation-turn metadata is normalized into the `conversation_history` enrichment on the primary chat-agent path without requiring a dedicated hook. The conversation-history preparer always performs a token-aware **preparation** step (`conversation_history_prepare`, recorded as a logical pre-execution interaction when LLM debug is enabled). When Tier 2 recursive compaction is configured, that same stage may additionally emit `conversation_history_compaction` as the optional LLM-backed extension.

Available hook stages:
- **BeforePlanningHook** — Runs before the phase loop. Can inject enrichments (RAG context, conversation history, agent memory, activity coordination) into `PipelineContext.Enrichments`, or return a `PipelineShortCircuit` to skip the entire pipeline (e.g., semantic cache hit).
- **AfterPlanningHook** — Runs once for each final validated planner-produced
  phase plan, before HITL persistence and execution. Mutations are applied to a
  copy and fully revalidated; an invalid mutation is rejected while the last
  valid plan is retained. HITL resume does not rerun this boundary.
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
    Enabled             bool          // Default: true
    MaxPhases           int           // Default: 5
    MaxTotalSteps       int           // Default: 200
    PhaseTimeout        time.Duration // Default: 180s per phase
    MaxValidationRounds int           // Default: 4 — regenerations after first validation failure
}
```

When safety limits are reached without a terminal plan, the orchestrator forces termination (`ForcedTerminal: true`) and synthesizes with all results collected so far.

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
TRUVAG3_ITERATIVE_MAX_PHASES=5            # Default: 5
TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS=200     # Default: 200
TRUVAG3_ITERATIVE_PHASE_TIMEOUT=180s      # Default: 180s
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
- **CustomInstructions-aware**: Injects `PromptConfig.CustomInstructions` into selection prompts so domain-required tools are not filtered out (ORCH-014)

```go
type TieredCapabilityProvider struct {
    catalog            *AgentCatalog
    aiClient           core.AIClient
    MinToolsForTiering int      // Default: 20
    customInstructions []string // Domain rules from PromptConfig (ORCH-014)

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

**Example scenario:**
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
│  ├── RedisLLMDebugStore (production) - Redis DB 7               │
│  ├── MemoryLLMDebugStore (testing)                               │
│  └── NoOpLLMDebugStore (disabled/fallback)                       │
│                                                                  │
│  Alternative Writer (no orchestration import needed):            │
│  └── telemetry.RedisLLMCallRecorder — write-only Redis recorder  │
│      for standalone agents. Same Redis DB 7 format and atomic    │
│      minimum-retention rules.                                     │
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
- **Dedicated Redis DB**: Uses `core.RedisDBLLMDebug` (DB 7) to isolate debug data
- **TTL-based cleanup**: 24h for success and 168h (7 days) for errors;
  final execution outcome and lineage promotion may retain the current and
  root records longer
- **Async recording**: Uses goroutines with WaitGroup for non-blocking capture
- **Graceful fallback**: Never fails orchestration if debug store fails
- **Payload fidelity**: Built-in stores preserve application and model
  payloads without framework-inferred redaction. Sanitization, access control,
  encryption, and retention policy for sensitive workloads belong to the
  adopter, as required by the root framework design principles. The legacy
  executor and skills error transformations documented below occur before
  storage and remain pending a separate audit; they are not part of the store
  contract or precedent for new paths
- **Atomic minimum retention**: Both Redis writers preserve a longer or
  persistent lifetime in the same atomic operation that appends an interaction
- **Late-writer retention floor**: Final execution recording stores a small,
  non-displayable retention-floor key. Orchestrator and standalone-agent
  writers consult that key atomically, so a write that starts later or in a
  different pod still receives the required audit lifetime

The store name is historical: it now carries both true LLM calls and selected non-LLM hook interactions when they materially improve production debugging. Examples include `conversation_history_prepare` (`Category: "logic"`), `user_memory_similarity_search` (`Category: "vector_db"`), and persistence-policy decisions (`Category: "logic"`). The registry viewer uses `HookPhase` plus `Category` to route these uniformly into LLM Debug, Pre-Execution, and Post-Execution views.

#### Conversation-aware execution storage

Conversation identity is stored in the existing `StoredExecution.Metadata` and
`ExecutionSummary.Metadata` maps. `ExecutionConversationID` and
`ExecutionSummaryConversationID` are the canonical accessors; the exported
record field sets remain unchanged.

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

#### Lineage-aware debug retention

Execution and LLM-debug evidence are retained as one audit lineage rather than
as unrelated records. The shared execution-retention selector applies these
rules:

- a successful execution receives the configured normal TTL;
- a failed execution receives the configured error TTL; and
- an interrupted execution receives at least the maximum of normal TTL, error
  TTL, and the checkpoint's remaining approval lifetime.

Each execution `Store` also attempts to persist a small retention-link record
containing only its trace ID, conversation ID, and related-root ID. Normal TTL
extension reads this projection instead of decompressing and unmarshalling the
full execution payload. A projection-write failure after the authoritative
record succeeds is a sanitized warning, not a false `Store` failure; indexing
continues, and later extension falls back to the full record when the projection
is absent. Direct Redis extends the execution, link, trace mapping, and optional
conversation index in one Lua operation inside the store's retry/circuit-
breaker boundary. `ExtendTTL` follows related-root links recursively with a
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
record keys. Both DB 7 writers read that marker in their append script, which
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
export TRUVAG3_LLM_DEBUG_TTL=24h       # Success records
export TRUVAG3_LLM_DEBUG_ERROR_TTL=168h # Error records (7 days)

# Redis database index
export TRUVAG3_LLM_DEBUG_REDIS_DB=7
```

**Programmatic Usage:**
```go
deps := orchestration.OrchestratorDependencies{
    Discovery: discovery,
    AIClient:  aiClient,
}

// Enable debug capture
orchestrator, _ := orchestration.CreateOrchestratorWithOptions(deps,
    orchestration.WithLLMDebug(true),
)

// Or inject custom store
orchestrator, _ := orchestration.CreateOrchestratorWithOptions(deps,
    orchestration.WithLLMDebugStore(customStore),
)
```

### Implementation Checklist

When implementing resilience patterns:

- [ ] Update ServiceCapabilityConfig with optional dependencies
- [ ] Refactor ServiceCapabilityProvider to use injected circuit breaker
- [ ] Create OrchestratorDependencies struct
- [ ] Update factory functions to accept dependencies
- [ ] Add WithCircuitBreaker option function
- [ ] Write unit tests with mock circuit breaker
- [ ] Write integration tests with real circuit breaker
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
export TRUVAG3_ORCHESTRATOR_CACHE_ENABLED="true"
export TRUVAG3_ORCHESTRATOR_CACHE_TTL="5m"
export TRUVAG3_ORCHESTRATOR_MAX_CONCURRENCY="10"
export TRUVAG3_ORCHESTRATOR_STEP_TIMEOUT="30s"
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

`OrchestrationBackends` is supplied by the application composition root. An
application may assemble it from custom implementations or select the included
Redis preset through `redisprovider.NewOrchestrationBackends`. Runtime workflow
code consumes only the narrow `StateStore` returned by `Workflow()` and never
imports or infers a storage provider. `NewRedisStateStore` remains a supported
compatibility constructor, but it is not the preferred pattern for new code.

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

The optional `redisprovider` preset owns only Redis adapter composition.
`NewOptions` is deterministic; `LoadOptionsFromEnvironment` applies the preset's
workflow TTL and task-queue retry limits; `ConfigureOptions` applies later code
overrides. `WithLogger` propagates one application-owned logger to every
logging-capable adapter assembled by the preset; each adapter retains its
standard `framework/orchestration` component attribution and NoOp behavior when
no logger is supplied. Root orchestration contracts and lifecycle code do not
import the preset package or Redis clients.

Direct Redis constructors that receive an application-owned client never close
that client. For the execution-debug, LLM-debug, checkpoint, and command stores,
the URL-owning compatibility constructors create and close their own clients;
their corresponding `WithClient` constructors leave connection and database
routing to application bootstrap.

Prefix behavior is explicit per adapter. `NewRedisCommandStoreWithClient` does
not read command-store identity from the process environment and uses the
deterministic `truvag3:hitl` default; applications select another prefix with
`WithCommandStoreKeyPrefix` or provider composition. Checkpoint identity is
different by design: both checkpoint constructors resolve
`TRUVAG3_HITL_KEY_PREFIX` plus `TRUVAG3_AGENT_NAME` or
`TRUVAG3_K8S_SERVICE_NAME`, with `WithCheckpointKeyPrefix` as the final code
override. Task and workflow adapter constructors take their namespace through
their explicit prefix or configuration arguments.

**Legacy exception — pending dedicated audit:** Some error paths that predate
the framework payload-fidelity policy still sanitize downstream failure bodies
or error messages before they reach retry prompts, logs, trace events,
execution-debug records, skills debug records, or returned observations. The
executor currently uses `core.RedactSensitiveText` for a failed component body
and `core.RedactSensitiveError` for its Go error; the latter retains the original
cause for `errors.Is` and `errors.As` while exposing a transformed message.
Skills authoring transforms an AI error returned to its caller, and skills AI
observability transforms error text recorded for debugging. Selected
Redis/provider construction and operational diagnostics contain similar legacy
calls.

This paragraph records current implementation behavior; it is not a normative
sanitization guarantee and must not be copied into new or modified paths.
Adopters remain responsible for payload classification and protection. Removal
or redesign is deferred to a dedicated audit that must check retry behavior, Go
error identity, stored evidence, returned observations, and log/trace output
across every framework call site.

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
one bounded correction retry. The corresponding AI-option overrides accept
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

| Version | Date | Changes |
|---------|------|---------|
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
