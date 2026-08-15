# TruvaG3 Telemetry Module Architecture

**Version**: 1.2
**Module**: `github.com/truvaagents/truva-g3/telemetry`
**Purpose**: Production-grade observability with OpenTelemetry integration
**Audience**: Framework developers, application developers, operations teams

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Design Philosophy](#design-philosophy)
   - [Why Explicit Initialization?](#1-why-explicit-initialization)
   - [Global Singleton vs Dependency Injection](#2-global-singleton-vs-dependency-injection)
   - [Progressive Disclosure API Design](#3-progressive-disclosure-api-design)
   - [Module Boundaries for Metrics](#4-module-boundaries-for-metrics)
   - [HTTP Middleware Extensibility](#5-http-middleware-extensibility-generic-mechanisms)
3. [LLM Call Recording](#llm-call-recording)
4. [Baggage Correlation Contract](#baggage-correlation-contract)
5. [Global Singleton Pattern](#global-singleton-pattern)
6. [OpenTelemetry Integration](#opentelemetry-integration)
7. [Integration Patterns](#integration-patterns)
8. [OTLP Pipeline Architecture](#otlp-pipeline-architecture)
9. [Production Deployment](#production-deployment)
10. [Common Pitfalls](#common-pitfalls)
11. [Troubleshooting Guide](#troubleshooting-guide)
12. [Performance Characteristics](#performance-characteristics)
13. [Agent Skills Observation Contract](#agent-skills-observation-contract)
14. [Version History](#version-history)

---

## Architecture Overview

### System Context

```
┌─────────────────────────────────────────────────────────────┐
│ Application Layer (main.go)                                 │
│                                                             │
│  telemetry.Initialize(config)  ← Explicit initialization  │
│  defer telemetry.Shutdown(ctx)                             │
└─────────────────────────────────────────────────────────────┘
                         │
                         │ Sets up global registry
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ Telemetry Module (github.com/truvaagents/truva-g3/telemetry)  │
│                                                             │
│  ┌─────────────┐    ┌──────────────┐    ┌──────────────┐ │
│  │  Registry   │───>│ OTelProvider │───>│ Exporters    │ │
│  │  (Singleton)│    │              │    │ (OTLP/HTTP)  │ │
│  └─────────────┘    └──────────────┘    └──────────────┘ │
│         │                                                   │
│         │ Global access via atomic.Value                   │
└─────────────────────────────────────────────────────────────┘
                         │
                         │ Used by application code
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ Application Code                                            │
│                                                             │
│  telemetry.Counter("requests.total")                       │
│  telemetry.Histogram("latency.ms", 125.5)                  │
│  telemetry.Gauge("connections.active", 42)                 │
└─────────────────────────────────────────────────────────────┘
                         │
                         │ OTLP/HTTP Protocol
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ OTEL Collector (Infrastructure)                            │
│                                                             │
│  Port 4318 (OTLP/HTTP)  →  Prometheus, Jaeger, etc.       │
└─────────────────────────────────────────────────────────────┘
```

### Key Components

| Component | Responsibility | Thread Safety |
|-----------|----------------|---------------|
| `Registry` | Global telemetry coordinator | Thread-safe via atomic operations |
| `OTelProvider` | OpenTelemetry SDK wrapper | Thread-safe by design |
| `MetricInstruments` | Pre-registered OTEL instruments | Thread-safe |
| `CardinalityLimiter` | Prevents metric explosion | Thread-safe via sync.Map |
| `TelemetryCircuitBreaker` | Protects backend from overload | Thread-safe |

---

## Design Philosophy

### 1. Why Explicit Initialization?

**The Design Decision**: Telemetry requires explicit `Initialize()` call at application level, not automatic framework-level initialization.

**Rationale**:

```go
// ❌ Framework CANNOT do this
// core/framework.go
import "github.com/truvaagents/truva-g3/telemetry"  // FORBIDDEN

func (f *Framework) Run(ctx context.Context) error {
    telemetry.Initialize(...)  // Violates architectural principles
}
```

**Architectural Constraint**: The `core` module **never imports** optional modules. This ensures:
- **Unidirectional dependencies** - Core is the foundation layer
- **True optionality** - Telemetry is genuinely optional at compile time
- **No circular dependencies** - Impossible by architectural design
- **Interface-based decoupling** - Core defines `Telemetry` interface only

**Modules Allowed to Import Telemetry**:

| Module | Can Import Telemetry | Reason |
|--------|---------------------|--------|
| `core` | ❌ No | Foundation layer, cannot import optional modules |
| `ai` | ✅ Yes | AI operations need metrics/tracing for production visibility |
| `resilience` | ✅ Yes | Circuit breaker state, retry metrics |
| `orchestration` | ✅ Yes | Workflow execution metrics, plan generation tracking |
| `ui` | ❌ No (planned) | Currently uses core interfaces only; telemetry support planned |

```go
// ✅ Applications MUST do this
// examples/tool-example/main.go
import "github.com/truvaagents/truva-g3/telemetry"  // Application choice

func main() {
    // Explicit initialization - clear and predictable
    telemetry.Initialize(telemetry.UseProfile(telemetry.ProfileProduction))
    defer telemetry.Shutdown(context.Background())

    // Now all framework components can use telemetry
    tool := NewWeatherTool()
    framework, _ := core.NewFramework(tool, ...)
    framework.Run(ctx)
}
```

### 2. Global Singleton vs Dependency Injection

**Question**: Why not pass telemetry provider to each component?

**Comparison**:

| Pattern | Pros | Cons | TruvaG3 Choice |
|---------|------|------|---------------|
| **Dependency Injection** | Explicit dependencies | Verbose, boilerplate-heavy | ❌ |
| **Global Singleton** | Simple API, zero boilerplate | Global state | ✅ Chosen |

**Why Global Singleton?**

```go
// ❌ Dependency Injection approach (verbose)
func (t *Tool) handleRequest(ctx context.Context, provider telemetry.Provider) {
    provider.Counter("requests")  // Must pass provider everywhere
}

// ✅ Global Singleton approach (simple)
func (t *Tool) handleRequest(ctx context.Context) {
    telemetry.Counter("requests")  // Just works
}
```

**Benefits**:
1. **Zero Boilerplate**: No need to pass provider through call chains
2. **Simple API**: `telemetry.Counter()` works anywhere
3. **Framework Alignment**: Matches `log.Printf()` pattern familiarity
4. **Performance**: Atomic reads are extremely fast
5. **Thread-Safe**: Built-in concurrency safety

**Tradeoffs Accepted**:
- Global state (controlled through initialization)
- Testing requires `Initialize()` in test setup
- Cannot have multiple telemetry configurations per process

### 3. Progressive Disclosure API Design

**Principle**: Make simple things simple, complex things possible.

**API Layers**:

```go
// Level 1: Simple API (90% of use cases)
telemetry.Counter("requests.total")
telemetry.Histogram("latency.ms", 125.5)
telemetry.Gauge("connections.active", 42)

// Level 2: Type-Specific Helpers (9% of use cases)
telemetry.RecordError("errors.total", "timeout")
telemetry.RecordLatency("api.latency_ms", 45.2)
telemetry.Duration("operation.duration_ms", startTime)

// Level 3: Full Control (1% of use cases)
telemetry.EmitWithOptions(ctx, "custom.metric", 99.9,
    telemetry.WithLabel("env", "prod"),
    telemetry.WithSampleRate(0.1),
    telemetry.WithUnit(telemetry.UnitMilliseconds))
```

**Design Goal**: Developers should reach for Level 1 API by default, only dropping to lower levels when needed.

### 4. Module Boundaries for Metrics

**Principle**: The telemetry module defines **contracts**, not implementations for other modules.

**What belongs in `unified_metrics.go`**:
- Cross-module helper functions that multiple modules need identically (e.g., `RecordAIRequest()`, `RecordToolCall()`)
- Metric name constants that create a shared vocabulary across modules
- Module label constants (`ModuleAgent`, `ModuleOrchestration`, `ModuleCore`)

**What does NOT belong in telemetry module files**:
- Module-specific metrics (e.g., orchestration's `plan_generation.retries`)
- Metrics that only one module will ever emit
- Implementation details specific to a single module's internal operations

**Correct Pattern**:

```go
// ✅ In orchestration/orchestrator.go - Module-specific metrics
// Use primitive APIs directly for orchestration-local metrics
telemetry.Counter("plan_generation.retries",
    "module", telemetry.ModuleOrchestration)

telemetry.Histogram("plan_generation.duration_ms", float64(duration.Milliseconds()),
    "module", telemetry.ModuleOrchestration,
    "status", "success")

// ✅ Use cross-module helpers for shared patterns
telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "plan_generation",
    float64(llmDuration.Milliseconds()), "success")
```

**Incorrect Pattern**:

```go
// ❌ Do NOT add module-specific metrics to unified_metrics.go
// unified_metrics.go
const (
    UnifiedPlanGenerationRetry = "plan_generation.retries"  // WRONG: orchestration-specific
)

func RecordPlanGenerationRetry(module string) {  // WRONG: only orchestration uses this
    Counter(UnifiedPlanGenerationRetry, "module", module)
}
```

**Rationale**:
1. **Clear Ownership**: Each module owns its internal metrics
2. **No Coupling**: Telemetry module doesn't need to know about orchestration internals
3. **Simpler Maintenance**: Changes to module-specific metrics don't require telemetry module changes
4. **Contract Stability**: `unified_metrics.go` only changes when cross-module contracts evolve

**When to Add to `unified_metrics.go`**:
- The metric pattern is used by **2+ modules** with identical semantics
- The metric is part of a framework-wide observability contract
- Dashboard queries need to aggregate across modules using the same metric name

### 5. HTTP Middleware Extensibility (Generic Mechanisms)

**Principle**: The telemetry module provides **generic extension mechanisms**, not use-case-specific logic. Applications decide how to use these mechanisms.

**The Design Decision**: When applications need custom tracing behavior (e.g., excluding certain requests), the telemetry module provides configurable hooks rather than hard-coded rules.

**Example**: The `TracingMiddlewareConfig.RequestFilter` allows applications to exclude specific HTTP requests from tracing:

```go
// ✅ Application decides what to filter (in application main.go)
middlewareConfig := &telemetry.TracingMiddlewareConfig{
    ExcludedPaths: []string{"/health", "/metrics"},

    // Application-specific filter logic
    RequestFilter: func(r *http.Request) bool {
        // Exclude polling requests from traces
        return r.URL.Query().Get("poll") != "true"
    },
}
```

**Why This Design?**

| Approach | Pros | Cons | TruvaG3 Choice |
|----------|------|------|---------------|
| **Hard-coded rules in telemetry** | Simple for specific use case | Couples telemetry to application logic | ❌ |
| **Generic filter mechanism** | Flexible, decoupled | Requires application configuration | ✅ Chosen |

**Architectural Constraint**: The telemetry module **never knows about specific use cases** in other modules. For example:
- Telemetry doesn't know about HITL checkpoint polling
- Telemetry doesn't know about orchestration workflow types
- Telemetry doesn't know about specific agent behaviors

**Correct Pattern**:

```go
// ✅ In telemetry/http.go - Generic mechanism
type TracingMiddlewareConfig struct {
    ExcludedPaths []string
    RequestFilter func(r *http.Request) bool  // Application decides filter logic
}

// ✅ In application main.go - Application-specific decision
config.RequestFilter = func(r *http.Request) bool {
    return r.URL.Query().Get("poll") != "true"  // Application knows about ?poll=true
}
```

**Incorrect Pattern**:

```go
// ❌ Do NOT add use-case-specific logic to telemetry
// telemetry/http.go
func shouldTrace(r *http.Request) bool {
    // WRONG: Telemetry shouldn't know about HITL polling
    if r.URL.Query().Get("poll") == "true" {
        return false
    }
    return true
}
```

**Benefits of Generic Mechanisms**:
1. **No Module Coupling**: Telemetry never imports or references other modules
2. **Maximum Flexibility**: Applications can implement any filter logic
3. **Future-Proof**: New use cases don't require telemetry module changes
4. **Clear Responsibility**: Applications own their configuration decisions

**Common Use Cases for RequestFilter**:
- Exclude polling/health-check requests that create trace noise
- Sample only certain request types in high-traffic scenarios
- Filter requests based on headers, query params, or path patterns
- Conditionally trace based on feature flags or environment

---

## LLM Call Recording

### Purpose

The telemetry module provides an `LLMCallRecorder` interface and a Redis-backed implementation (`RedisLLMCallRecorder`) that lets agents record every LLM call for production debugging. This enables the registry-viewer to display agent-side LLM calls alongside orchestrator-side calls in a unified debug view.

### Why This Lives in Telemetry (Not Orchestration)

Agents need to record LLM calls, but they **must not import the orchestration module** (agents are independent microservices, not orchestrators). The dependency graph requires:

```
ai → core + telemetry     ✅  (ai.InstrumentedAIClient uses telemetry.LLMCallRecorder)
agent → ai + telemetry    ✅  (agent creates RedisLLMCallRecorder)
agent → orchestration     ❌  (would create coupling between agents and orchestrator)
```

By placing the recorder interface and Redis implementation in telemetry, agents can record LLM calls without knowing about orchestration internals.

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ Agent Application (main.go)                                  │
│                                                              │
│  recorder := telemetry.NewRedisLLMCallRecorder()            │
│  aiClient := ai.NewInstrumentedClient(base, recorder,       │
│      ai.WithComponentName("research-assistant"))             │
└──────────────────────┬───────────────────────────────────────┘
                       │ GenerateResponse(ctx, prompt, opts)
                       ↓
┌─────────────────────────────────────────────────────────────┐
│ ai.InstrumentedAIClient (decorator)                          │
│                                                              │
│  1. Calls wrapped client.GenerateResponse()                  │
│  2. Resolves requestID from OTel baggage or context          │
│  3. Builds telemetry.LLMCallRecord                           │
│  4. Fires async goroutine → recorder.RecordLLMCall()         │
└──────────────────────┬───────────────────────────────────────┘
                       │ Pipeline: RPUSH interaction + HSETNX metadata + index
                       ↓
┌─────────────────────────────────────────────────────────────┐
│ Redis DB 7                                                   │
│                                                              │
│  truvag3:llm:debug:{requestID}:interactions  [JSON, JSON, …] │
│  truvag3:llm:debug:{requestID}:meta          {hash fields}    │
│  truvag3:llm:debug:index                     {sorted set}     │
└─────────────────────────────────────────────────────────────┘
```

`ai.NewClient` and `ai.NewRequestClient` already install a factory-managed
`InstrumentedAIClient` with a no-op recorder so every constructed provider has
the common logical-span boundary. When an application calls
`ai.NewInstrumentedClient` with a real debug recorder, the constructor
collapses that factory-managed layer and retains one logical wrapper rather
than emitting duplicate common spans. Caller-owned clients that were not
created by those factories are wrapped normally.

### Key Design Decisions

1. **Write-only**: `RedisLLMCallRecorder` writes interactions and their
   format-compatible metadata/index entries. Reading is done by
   `orchestration.RedisLLMDebugStore.GetRecord()` via the registry-viewer.

2. **Format-compatible**: The JSON structure written by `RedisLLMCallRecorder` matches `orchestration.LLMInteraction` exactly. Both writers produce records that the registry-viewer can read seamlessly.

3. **No read-modify-write**: A Redis pipeline appends the interaction and
   performs first-valid metadata backfill with `HSETNX`, then updates the
   listing index and TTLs. Concurrent orchestration and agent writers share the
   same format without replacing an established conversation ID.

4. **Layer 1 resilience**: Built-in retry with exponential backoff (3 attempts, 100ms→2s) and failure cooldown (5 failures in 30s triggers 30s pause). Recording failures never impact the LLM call path.

5. **Request context propagation**: The orchestrator's executor sends
   framework-managed request, step, plan, phase, original-request,
   conversation, agent, and investigation headers. Agents extract the request,
   step, plan, phase, original-request, conversation, and agent headers via
   `core.ExtractRequestContext()`; investigation-owner propagation is not part
   of that helper. An initialized `otelhttp` transport also carries standard
   W3C Trace Context and W3C Baggage; the explicit conversation header is a
   framework fallback, not a replacement for standard propagation.

6. **Phase number propagation**: For multi-phase iterative planning, the executor also sends `X-TruvaG3-Phase-Number` as an HTTP header. `core.ExtractRequestContext()` extracts it into context, and `InstrumentedAIClient.resolvePhaseNumber()` reads it (with OTel baggage fallback). The `LLMCallRecord.PhaseNumber` field (`omitempty`, 0 = single-phase/Phase 1) enables the registry-viewer to correlate agent LLM calls to specific planning phases.

### Interface

```go
// LLMCallRecorder records LLM calls for debugging.
// Implementations must be safe for concurrent use.
type LLMCallRecorder interface {
    RecordLLMCall(ctx context.Context, requestID string, record LLMCallRecord) error
}

// NoOpLLMCallRecorder is a safe default that discards all recordings.
type NoOpLLMCallRecorder struct{}
```

### Relationship to Other Modules

| Module | Role |
|--------|------|
| `telemetry` | Defines `LLMCallRecorder` interface; provides `RedisLLMCallRecorder` (write-only) and `NoOpLLMCallRecorder` |
| `ai` | `InstrumentedAIClient` wraps any `core.AIClient` and calls `RecordLLMCall()` after each LLM call |
| `orchestration` | `LLMCallRecorderAdapter` bridges `LLMDebugStore` → `LLMCallRecorder`; `RedisLLMDebugStore` reads what both writers produce |
| `core` | `ExtractRequestContext()` extracts framework-managed request, lineage, conversation, and agent headers into context |

---

## Baggage Correlation Contract

`WithBaggage` remains the convenience API for ordinary telemetry enrichment.
It accepts multiple key/value pairs, truncates an overlong key or value,
silently skips invalid members, and applies the 64-member limit to each
candidate as it is added. `WithBaggageExact` is the lossless API for identity:
it adds or replaces one member, never truncates, and returns the original
context plus a bounded `BaggageExactError` on rejection.

Both construction paths enforce the same limits:

| Limit | Value |
|---|---:|
| Members | 64 |
| Key length | 128 bytes |
| Value length | 512 bytes |
| Complete serialized W3C baggage value | 8192 bytes |

The total includes separators, encoding, and member properties; it is not a
loose sum of raw key/value lengths. Replacing a member is allowed at the item
limit when the complete serialized value remains within the total limit.

`WithMetricLabelEligibility(false)` stores a generic W3C member property.
That property propagates across standard inject/extract and tells
context-aware metric enrichment to omit the member. It does not remove the
member from traces, structured logs, or downstream context. The framework uses
this for `conversation_id` because conversation identity is high-cardinality.
Unmarked baggage retains existing metric-enrichment behavior.

`CopyBaggage(dst, src)` copies the complete OpenTelemetry baggage object,
including member properties, while preserving destination cancellation,
deadlines, and values. `WithoutBaggageMember` removes one member while
preserving all other properties. These helpers do not rebuild individual
members. Exact-helper rejection updates bounded baggage statistics; removal and
copy do not increment add/drop counters.

HTTP middleware validates an explicit `X-TruvaG3-Conversation-ID` header before
setting it on the server span. Baggage-sourced common span enrichment propagates
the member as provided; framework orchestration places `conversation_id` into
baggage only after canonical validation. Applications that add
`conversation_id` through generic baggage APIs are responsible for supplying a
valid value. JSON context-aware logging may include the framework-validated
value; text logging keeps its existing format. Metric enrichment honors the
metric-eligibility property, so correlation can remain available to traces and
logs without creating a metric series per conversation.

## Global Singleton Pattern

### Implementation

**Source**: `telemetry/registry.go`

```go
var (
    // Global registry singleton
    globalRegistry atomic.Value  // Stores *Registry

    // Ensures single initialization
    initOnce sync.Once
)

// Initialize sets up the global telemetry system (ONCE)
func Initialize(config Config) error {
    var initErr error

    initOnce.Do(func() {
        registry, err := createRegistry(config)
        if err != nil {
            initErr = err
            return
        }

        // Store globally (atomic write)
        globalRegistry.Store(registry)
    })

    return initErr
}

// Counter uses the global registry (lock-free read)
func Counter(name string, labels ...string) {
    registry := globalRegistry.Load()  // Atomic read
    if registry != nil {
        r := registry.(*Registry)
        r.emitCounter(name, labels...)
    }
    // NoOp if not initialized - safe fallback
}
```

### Thread Safety Guarantees

1. **Initialization**: `sync.Once` ensures single initialization
2. **Global Access**: `atomic.Value` provides lock-free reads
3. **Concurrent Emission**: All emission operations are thread-safe
4. **Shutdown**: Coordinated via context and internal synchronization

### Performance Characteristics

```go
// Benchmark results (Go 1.25, 16 cores)
BenchmarkCounter-16             50000000    25.3 ns/op    0 B/op    0 allocs/op
BenchmarkHistogram-16           30000000    38.7 ns/op    0 B/op    0 allocs/op
BenchmarkGauge-16               40000000    29.1 ns/op    0 B/op    0 allocs/op
```

**Hot Path Optimization**: The critical path (metric emission) uses atomic operations only - no mutexes.

### Initialization Lifecycle

```go
// Application startup sequence
func main() {
    // 1. Initialize telemetry FIRST (before components)
    if err := telemetry.Initialize(telemetry.UseProfile(telemetry.ProfileProduction)); err != nil {
        log.Fatalf("Telemetry init failed: %v", err)
    }

    // 2. Ensure cleanup on exit
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()

        if err := telemetry.Shutdown(ctx); err != nil {
            log.Printf("Telemetry shutdown error: %v", err)
        }
    }()

    // 3. Create components (they'll use global registry)
    tool := NewWeatherTool()

    // 4. Run framework
    framework, _ := core.NewFramework(tool, ...)
    framework.Run(context.Background())
}
```

---

## OpenTelemetry Integration

### Architecture

```go
// OTelProvider bridges TruvaG3 and OpenTelemetry
type OTelProvider struct {
    tracer         trace.Tracer             // Distributed tracing
    meter          metric.Meter             // Metrics collection
    traceProvider  *sdktrace.TracerProvider // Manages trace export
    metricProvider *sdkmetric.MeterProvider // Manages metric export
    metrics        *MetricInstruments       // Pre-registered instruments
}
```

### OTLP/HTTP Export Pipeline

```
┌─────────────────────────────────────────────────────┐
│ Application Code                                    │
│                                                     │
│ telemetry.Counter("requests")  ← Simple API call  │
└─────────────────────────────────────────────────────┘
              │
              ↓
┌─────────────────────────────────────────────────────┐
│ Global Registry (Singleton)                         │
│                                                     │
│ - Cardinality limiting                              │
│ - Circuit breaker protection                        │
│ - Rate limiting                                     │
└─────────────────────────────────────────────────────┘
              │
              ↓
┌─────────────────────────────────────────────────────┐
│ OTelProvider                                        │
│                                                     │
│ ┌─────────────┐    ┌─────────────┐                │
│ │ Metric      │    │ Trace       │                │
│ │ Instruments │    │ Provider    │                │
│ └─────────────┘    └─────────────┘                │
└─────────────────────────────────────────────────────┘
              │
              ↓
┌─────────────────────────────────────────────────────┐
│ OTEL SDK (Batching & Processing)                   │
│                                                     │
│ - Batch metrics every 30 seconds                    │
│ - Batch traces immediately                          │
│ - Compress payloads                                 │
└─────────────────────────────────────────────────────┘
              │
              │ OTLP/HTTP Protocol
              ↓
┌─────────────────────────────────────────────────────┐
│ OTLP Endpoint (http://otel-collector:4318)         │
│                                                     │
│ Content-Type: application/x-protobuf                │
│ Gzip compression enabled                            │
└─────────────────────────────────────────────────────┘
```

### Endpoint Configuration

**Standard Environment Variables** (OpenTelemetry specification):

```bash
# Primary configuration
OTEL_EXPORTER_OTLP_ENDPOINT="http://otel-collector:4318"

# Service identification
OTEL_SERVICE_NAME="weather-service"

# Optional: Sampling rate for traces
OTEL_TRACES_SAMPLER="always_on"

# Optional: Export protocol (defaults to http/protobuf)
OTEL_EXPORTER_OTLP_PROTOCOL="http/protobuf"
```

**TruvaG3 Configuration Priority**:
1. Explicit `Config` passed to `Initialize()`
2. `OTEL_EXPORTER_OTLP_ENDPOINT` environment variable
3. `TRUVAG3_TELEMETRY_ENDPOINT` (framework-specific)
4. Default: `http://localhost:4318`

### Metric Type Mapping

TruvaG3 automatically maps metric names to appropriate OpenTelemetry instrument types:

| TruvaG3 API | Naming Pattern | OTEL Instrument | Use Case |
|------------|----------------|-----------------|----------|
| `Counter()` | `*count*`, `*total*`, `*errors*` | Counter (monotonic) | Cumulative counts |
| `Histogram()` | `*duration*`, `*latency*`, `*time*` | Histogram | Latency distributions |
| `Gauge()` | `*gauge*`, `*current*`, `*active*` | Histogram (as proxy) | Current values |

**Implementation** (`telemetry/otel.go:132-158`):

```go
func (o *OTelProvider) RecordMetric(name string, value float64, labels map[string]string) {
    // Heuristic-based routing
    switch {
    case contains(name, "duration", "latency", "time"):
        o.metrics.RecordHistogram(ctx, name, value, attrs...)
    case contains(name, "count", "total", "errors"):
        o.metrics.RecordCounter(ctx, name, int64(value), attrs...)
    case contains(name, "gauge", "current", "size"):
        o.metrics.RecordHistogram(ctx, name, value, attrs...)
    default:
        o.metrics.RecordHistogram(ctx, name, value, attrs...)
    }
}
```

**Why Histograms for Gauges?**: OpenTelemetry Gauges require callback functions. Using Histograms for gauge-like metrics provides simpler API while maintaining semantic correctness for most use cases.

---

## Integration Patterns

### Pattern 1: Tool Integration (Passive Components)

**Scenario**: Weather service tool that responds to requests.

```go
// weather_tool.go
package main

import (
    "context"
    "time"
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

type WeatherTool struct {
    *core.BaseTool
}

func NewWeatherTool() *WeatherTool {
    return &WeatherTool{
        BaseTool: core.NewTool("weather-service"),
    }
}

// Tool handlers emit metrics directly
func (w *WeatherTool) handleWeatherRequest(rw http.ResponseWriter, r *http.Request) {
    start := time.Now()

    // Track request started
    telemetry.Counter("weather.requests.total",
        "endpoint", "current_weather",
        "method", r.Method)

    // Process request
    result, err := w.fetchWeather(r.Context())

    // Track completion
    status := "success"
    if err != nil {
        status = "error"
        telemetry.Counter("weather.errors.total",
            "type", "fetch_failed",
            "endpoint", "current_weather")
    }

    // Track latency
    duration := time.Since(start).Milliseconds()
    telemetry.Histogram("weather.request.duration_ms", float64(duration),
        "endpoint", "current_weather",
        "status", status)

    // Response handling...
}

// main.go
func main() {
    // CRITICAL: Initialize telemetry BEFORE creating components
    telemetry.Initialize(telemetry.UseProfile(telemetry.ProfileProduction))
    defer telemetry.Shutdown(context.Background())

    tool := NewWeatherTool()
    framework, _ := core.NewFramework(tool, ...)
    framework.Run(context.Background())
}
```

### Pattern 2: Agent Integration (Active Components)

**Scenario**: Orchestrator agent that discovers and coordinates tools.

```go
// orchestrator_agent.go
package main

import (
    "context"
    "time"
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

type OrchestratorAgent struct {
    *core.BaseAgent
}

func NewOrchestratorAgent() *OrchestratorAgent {
    return &OrchestratorAgent{
        BaseAgent: core.NewBaseAgent("orchestrator"),
    }
}

// Agents emit metrics during orchestration
func (o *OrchestratorAgent) ExecuteWorkflow(ctx context.Context, workflowID string) error {
    start := time.Now()

    // Add workflow context for distributed tracing
    ctx = telemetry.WithBaggage(ctx,
        "workflow_id", workflowID,
        "operation", "execute_workflow")

    // Track workflow start
    telemetry.Counter("orchestrator.workflows.started",
        "workflow_id", workflowID)

    // Discover available tools
    tools, err := o.Discover(ctx, core.DiscoveryFilter{
        Type: core.ComponentTypeTool,
    })

    if err != nil {
        telemetry.Counter("orchestrator.errors.total",
            "type", "discovery_failed")
        return err
    }

    // Track discovered tools
    telemetry.Gauge("orchestrator.tools.discovered", float64(len(tools)),
        "workflow_id", workflowID)

    // Orchestrate tools (parallel execution)
    var wg sync.WaitGroup
    for _, tool := range tools {
        wg.Add(1)
        go func(t *core.ServiceInfo) {
            defer wg.Done()

            toolStart := time.Now()
            err := o.invokeTool(ctx, t)

            status := "success"
            if err != nil {
                status = "error"
            }

            telemetry.Histogram("orchestrator.tool.invocation.duration_ms",
                float64(time.Since(toolStart).Milliseconds()),
                "tool_name", t.Name,
                "status", status)
        }(tool)
    }

    wg.Wait()

    // Track workflow completion
    duration := time.Since(start).Milliseconds()
    telemetry.Counter("orchestrator.workflows.completed",
        "workflow_id", workflowID,
        "status", "success")

    telemetry.Histogram("orchestrator.workflow.duration_ms", float64(duration),
        "workflow_id", workflowID)

    return nil
}

// main.go
func main() {
    // Initialize telemetry
    telemetry.Initialize(telemetry.UseProfile(telemetry.ProfileProduction))
    defer telemetry.Shutdown(context.Background())

    agent := NewOrchestratorAgent()
    framework, _ := core.NewFramework(agent, ...)
    framework.Run(context.Background())
}
```

### Pattern 3: Context Propagation (Distributed Tracing)

**Scenario**: Request flows across multiple services.

```go
// Service A (Entry point)
func (a *ServiceA) HandleRequest(ctx context.Context, req Request) {
    // Add baggage for distributed context
    ctx = telemetry.WithBaggage(ctx,
        "request_id", req.ID,
        "user_id", req.UserID,
        "tenant_id", req.TenantID)

    // Start span for this service
    ctx, span := telemetry.StartSpan(ctx, "service-a.handle-request")
    defer span.End()

    span.SetAttribute("request.size", len(req.Data))

    // Call Service B (context propagates automatically)
    result, err := a.callServiceB(ctx, req)

    if err != nil {
        span.RecordError(err)
    }
}

// Service B (Downstream)
func (b *ServiceB) ProcessRequest(ctx context.Context, data []byte) {
    // Extract baggage automatically
    requestID := telemetry.GetBaggage(ctx, "request_id")
    userID := telemetry.GetBaggage(ctx, "user_id")

    // Start span (parent span is in context)
    ctx, span := telemetry.StartSpan(ctx, "service-b.process-request")
    defer span.End()

    // Metrics include baggage automatically
    telemetry.Counter("service-b.requests",
        "user_id", userID,  // From context
        "request_id", requestID)

    // Processing...
}
```

**Result**: Complete trace across all services with correlated metrics.

---

## OTLP Pipeline Architecture

### Production Deployment Stack

```
┌─────────────────────────────────────────────────────────────┐
│ Kubernetes Cluster (truvag3-examples namespace)              │
│                                                             │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐  │
│  │ Tool Pods    │   │ Agent Pods   │   │ Other Pods   │  │
│  │              │   │              │   │              │  │
│  │ telemetry.   │   │ telemetry.   │   │ telemetry.   │  │
│  │ Counter()    │   │ Counter()    │   │ Counter()    │  │
│  └──────────────┘   └──────────────┘   └──────────────┘  │
│         │                    │                    │         │
│         │  OTLP/HTTP         │  OTLP/HTTP         │         │
│         └────────────────────┴────────────────────┘         │
│                              │                               │
│                              ↓                               │
│  ┌───────────────────────────────────────────────────────┐ │
│  │ OTEL Collector (Sidecar or Deployment)               │ │
│  │                                                       │ │
│  │ Receivers:                                            │ │
│  │   - otlp (http: 4318, grpc: 4317)                   │ │
│  │                                                       │ │
│  │ Processors:                                           │ │
│  │   - memory_limiter (256MB limit)                     │ │
│  │   - batch (512 metrics, 10s timeout)                 │ │
│  │                                                       │ │
│  │ Exporters:                                            │ │
│  │   - prometheus (port 8889)                           │ │
│  │   - otlp/jaeger (port 4317)                          │ │
│  └───────────────────────────────────────────────────────┘ │
│                      │                       │               │
└──────────────────────┼───────────────────────┼──────────────┘
                       │                       │
          ┌────────────┴────────┐   ┌──────────┴─────────┐
          │                     │   │                    │
          ↓                     │   ↓                    │
┌──────────────────┐            │ ┌──────────────────┐  │
│ Prometheus       │            │ │ Jaeger           │  │
│                  │            │ │                  │  │
│ - Scrapes :8889  │            │ │ - Receives       │  │
│   every 15s      │            │ │   traces via     │  │
│ - Stores metrics │            │ │   OTLP/gRPC      │  │
│ - Alerts         │            │ │ - Trace search   │  │
└──────────────────┘            │ └──────────────────┘  │
          │                     │                        │
          ↓                     │                        │
┌──────────────────┐            │                        │
│ Grafana          │            │                        │
│                  │            │                        │
│ - Visualizes     │            │                        │
│   metrics        │            │                        │
│ - Dashboards     │            │                        │
│ - Alerts         │            │                        │
└──────────────────┘            │                        │
                                │                        │
                    ┌───────────┴────────────────────────┘
                    │
                    ↓
            [ Operations Team ]
```

### OTEL Collector Configuration

**Key Configuration** (`examples/k8-deployment/otel-collector.yaml`):

```yaml
receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318  # TruvaG3 uses HTTP by default
      grpc:
        endpoint: 0.0.0.0:4317  # Available but not primary

processors:
  memory_limiter:
    limit_mib: 256
    check_interval: 1s

  batch:
    timeout: 10s
    send_batch_size: 512
    send_batch_max_size: 1024

exporters:
  prometheus:
    endpoint: "0.0.0.0:8889"
    namespace: "truvag3"
    const_labels:
      cluster: "truvag3-examples"

  otlp/jaeger:
    endpoint: jaeger-collector:4317  # gRPC for Jaeger
    tls:
      insecure: true

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [otlp/jaeger]

    metrics:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [prometheus]
```

### Common Deployment Patterns

#### Pattern A: Sidecar Collector (Recommended for Production)

```yaml
# Each application pod has OTEL collector sidecar
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: weather-tool
        image: weather-tool:latest
        env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://localhost:4318"  # Sidecar

      - name: otel-collector
        image: otel/opentelemetry-collector:latest
        ports:
        - containerPort: 4318
```

**Benefits**:
- Low latency (localhost)
- Pod-level isolation
- Scales with application

**Tradeoffs**:
- More resource usage
- Collector per pod

#### Pattern B: Centralized Collector (Current Setup)

```yaml
# Single OTEL collector deployment
apiVersion: apps/v1
kind: Deployment
metadata:
  name: otel-collector
spec:
  replicas: 1  # Or HPA-scaled
```

**Benefits**:
- Fewer resources
- Central configuration
- Easier management

**Tradeoffs**:
- Network hop required
- Single point of failure (mitigated by replicas)

---

## Production Deployment

### Pre-Deployment Checklist

#### ✅ Application Code

- [ ] Import telemetry module: `import "github.com/truvaagents/truva-g3/telemetry"`
- [ ] Call `telemetry.Initialize()` in `main()` before component creation
- [ ] Use `defer telemetry.Shutdown(ctx)` for graceful shutdown
- [ ] Add metrics to critical code paths (handlers, workflows)
- [ ] Use consistent metric naming conventions
- [ ] Test telemetry locally before deploying

#### ✅ Kubernetes Configuration

- [ ] Set `OTEL_EXPORTER_OTLP_ENDPOINT` environment variable
- [ ] Set `OTEL_SERVICE_NAME` for service identification (optional)
- [ ] Set pod template `metadata.labels.app: <service-name>` — required for Loki log
      pipeline to derive `service_name` (via `k8sattributes`). Must equal
      `OTEL_SERVICE_NAME` and the framework logger's service name, or traces/metrics
      and logs will report different identities for the same pod
- [ ] Deploy OTEL Collector with correct configuration
- [ ] Deploy Prometheus for metrics storage
- [ ] Deploy Jaeger for trace visualization
- [ ] Deploy Grafana for dashboards
- [ ] Configure Prometheus scraping intervals
- [ ] Set up persistent storage for Prometheus data

#### ✅ Monitoring Configuration

- [ ] Create Grafana dashboards for key metrics
- [ ] Set up Prometheus alerts for anomalies
- [ ] Configure alert routing (PagerDuty, Slack, etc.)
- [ ] Test alert firing with synthetic errors
- [ ] Document dashboard access URLs
- [ ] Set up log aggregation for telemetry errors

### Environment Variables Reference

```bash
# Required for telemetry to function
OTEL_EXPORTER_OTLP_ENDPOINT="http://otel-collector:4318"

# Recommended for production
OTEL_SERVICE_NAME="weather-service"
OTEL_SERVICE_NAMESPACE="production"
OTEL_SERVICE_VERSION="v1.2.3"

# Optional: Sampling configuration
OTEL_TRACES_SAMPLER="always_on"           # Development
OTEL_TRACES_SAMPLER="traceidratio"        # Production
OTEL_TRACES_SAMPLER_ARG="0.1"             # Sample 10%

# Optional: Resource attributes
OTEL_RESOURCE_ATTRIBUTES="deployment.environment=production,cluster.name=us-west-2"
```

### Dockerfile Best Practices

```dockerfile
FROM golang:1.26.4-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build with telemetry support
RUN CGO_ENABLED=0 GOOS=linux go build -o weather-tool .

# Runtime image
FROM alpine:latest
RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /app/weather-tool .

# Expose application port
EXPOSE 8080

# Health check endpoint
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://localhost:8080/health || exit 1

# Run with proper signal handling
ENTRYPOINT ["/app/weather-tool"]
```

### Kubernetes Deployment Example

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: weather-tool
  namespace: truvag3-examples
spec:
  replicas: 3
  selector:
    matchLabels:
      app: weather-tool
  template:
    metadata:
      labels:
        app: weather-tool
        version: v1.0.0
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: "/metrics"
    spec:
      containers:
      - name: weather-tool
        image: weather-tool:v1.0.0
        ports:
        - containerPort: 8080
          name: http
        env:
        # Telemetry configuration
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector:4318"
        - name: OTEL_SERVICE_NAME
          value: "weather-tool"
        - name: OTEL_SERVICE_NAMESPACE
          value: "production"

        # Application configuration
        - name: REDIS_URL
          value: "redis://redis:6379"

        resources:
          requests:
            memory: "64Mi"
            cpu: "100m"
          limits:
            memory: "128Mi"
            cpu: "200m"

        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 30

        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
```

---

## Common Pitfalls

### Pitfall 1: Forgetting to Initialize

**Problem**:
```go
func main() {
    // ❌ NO telemetry initialization
    tool := NewWeatherTool()
    framework, _ := core.NewFramework(tool, ...)
    framework.Run(context.Background())
}

// Later in code...
func (t *Tool) handleRequest() {
    telemetry.Counter("requests")  // Silent NoOp - no metrics emitted!
}
```

**Symptom**: No metrics appear in Prometheus, no errors logged.

**Solution**:
```go
func main() {
    // ✅ Initialize FIRST
    if err := telemetry.Initialize(telemetry.UseProfile(telemetry.ProfileProduction)); err != nil {
        log.Fatalf("Telemetry init failed: %v", err)
    }
    defer telemetry.Shutdown(context.Background())

    tool := NewWeatherTool()
    framework, _ := core.NewFramework(tool, ...)
    framework.Run(context.Background())
}
```

### Pitfall 2: Wrong Endpoint Configuration

**Problem**:
```yaml
env:
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: "otel-collector:4318"  # ❌ Missing http:// prefix
```

**Symptom**: Telemetry initialization fails with connection error.

**Solution**:
```yaml
env:
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: "http://otel-collector:4318"  # ✅ Full URL
```

### Pitfall 3: Forgetting Shutdown

**Problem**:
```go
func main() {
    telemetry.Initialize(...)
    // ❌ No shutdown - metrics buffered in memory may not be exported

    tool := NewWeatherTool()
    framework, _ := core.NewFramework(tool, ...)
    framework.Run(context.Background())
    // Program exits - buffered metrics lost
}
```

**Symptom**: Metrics occasionally missing, especially on short-lived processes.

**Solution**:
```go
func main() {
    telemetry.Initialize(...)

    // ✅ Always defer shutdown
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        telemetry.Shutdown(ctx)
    }()

    // Rest of application...
}
```

### Pitfall 4: High Cardinality Labels

**Problem**:
```go
// ❌ User ID as label = millions of unique time series
telemetry.Counter("requests.total",
    "user_id", userID)  // Cardinality explosion!
```

**Symptom**: Memory usage grows unbounded, Prometheus struggles.

**Solution**:
```go
// ✅ Use low-cardinality labels
telemetry.Counter("requests.total",
    "endpoint", "/api/users",    // ~100 endpoints
    "status", "success",          // 2-3 values
    "region", "us-west-2")        // ~10 regions

// ✅ Use baggage for high-cardinality data (tracing only)
ctx = telemetry.WithBaggage(ctx, "user_id", userID)
```

**Built-in Protection**: TruvaG3 telemetry has cardinality limiter (default: 1000 unique combinations).

### Pitfall 5: OTEL Collector Misconfiguration

**Problem**:
```yaml
exporters:
  otlp/jaeger:
    endpoint: http://jaeger-collector:4318  # ❌ HTTP endpoint
    # Missing: Protocol specification
```

**Symptom**:
```
error reading server preface: http2: failed reading the frame payload
```

**Solution**:
```yaml
exporters:
  # Option 1: Use HTTP exporter
  otlphttp/jaeger:
    endpoint: http://jaeger-collector:4318

  # Option 2: Use gRPC port
  otlp/jaeger:
    endpoint: jaeger-collector:4317  # gRPC
    tls:
      insecure: true
```

### Pitfall 6: Missing Error Handling

**Problem**:
```go
// ❌ Ignoring initialization errors
telemetry.Initialize(config)  // Error ignored

// Application continues with NoOp telemetry
```

**Solution**:
```go
// ✅ Handle initialization errors appropriately
if err := telemetry.Initialize(config); err != nil {
    // Option A: Fail fast (recommended for production)
    log.Fatalf("Telemetry required but init failed: %v", err)

    // Option B: Log and continue (acceptable for development)
    log.Printf("WARNING: Running without telemetry: %v", err)
}
```

---

## Troubleshooting Guide

### Issue: No Metrics Appearing in Prometheus

**Diagnostic Steps**:

1. **Check if telemetry is initialized**:
   ```bash
   # In application logs
   grep "Telemetry initialized" /var/log/app.log
   ```

2. **Verify OTEL Collector is receiving data**:
   ```bash
   kubectl port-forward -n truvag3-examples svc/otel-collector 4318:4318

   # Send test metric
   curl -X POST http://localhost:4318/v1/metrics \
     -H "Content-Type: application/json" \
     -d '{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"test"}}]},"instrumentationLibraryMetrics":[{"metrics":[{"name":"test_metric","gauge":{"dataPoints":[{"asDouble":42.0}]}}]}]}]}'

   # Should return: {"partialSuccess":{}}
   ```

3. **Check OTEL Collector logs**:
   ```bash
   kubectl logs -n truvag3-examples deployment/otel-collector --tail=50

   # Look for:
   # ✅ "Traces exported successfully"
   # ✅ "Metrics exported successfully"
   # ❌ Connection errors
   # ❌ Configuration errors
   ```

4. **Verify Prometheus scraping**:
   ```bash
   kubectl port-forward -n truvag3-examples svc/prometheus 9090:9090

   # Open http://localhost:9090/targets
   # Check otel-collector target status
   ```

**Common Causes**:
- Application not calling `telemetry.Initialize()`
- Wrong `OTEL_EXPORTER_OTLP_ENDPOINT` value
- OTEL Collector not running
- Prometheus not scraping OTEL Collector

### Issue: Jaeger Not Showing Traces

**Diagnostic Steps**:

1. **Check OTEL Collector → Jaeger connection**:
   ```bash
   kubectl logs -n truvag3-examples deployment/otel-collector | grep jaeger

   # Look for connection errors
   ```

2. **Verify Jaeger is receiving data**:
   ```bash
   kubectl port-forward -n truvag3-examples svc/jaeger-query 16686:80

   # Open http://localhost:16686
   # Check "Search" tab for traces
   ```

3. **Test trace submission**:
   ```go
   // In application code
   ctx, span := telemetry.StartSpan(ctx, "test-operation")
   defer span.End()

   span.SetAttribute("test", "value")
   time.Sleep(100 * time.Millisecond)
   ```

**Common Causes**:
- OTEL Collector exporter misconfigured (gRPC vs HTTP)
- Jaeger not running or not accessible
- Traces not being generated by application code

### Issue: High Memory Usage

**Diagnostic Steps**:

1. **Check metric cardinality**:
   ```bash
   # In Prometheus
   # Run query: sum(count by(__name__)({__name__=~".+"}))

   # If > 10,000 time series: High cardinality problem
   ```

2. **Check OTEL Collector memory**:
   ```bash
   kubectl top pods -n truvag3-examples | grep otel-collector

   # If memory > 256MB: Increase memory_limiter
   ```

3. **Enable cardinality protection**:
   ```go
   config := telemetry.Config{
       Profile: telemetry.ProfileProduction,
       CardinalityLimit: 1000,  // Limit unique label combinations
   }
   ```

**Common Causes**:
- User IDs or timestamps as labels
- Too many unique label combinations
- Memory limiter not configured in OTEL Collector

### Issue: Slow Performance

**Diagnostic Steps**:

1. **Check if circuit breaker is open**:
   ```go
   health := telemetry.GetHealth()
   fmt.Printf("Circuit state: %s\n", health.CircuitState)

   // If "open": Backend is down or slow
   ```

2. **Measure emission overhead**:
   ```go
   start := time.Now()
   for i := 0; i < 10000; i++ {
       telemetry.Counter("test.metric")
   }
   duration := time.Since(start)
   fmt.Printf("10k emissions: %v (%.2f µs/op)\n",
       duration, float64(duration.Microseconds())/10000)

   // Should be: <5 µs/op
   ```

**Common Causes**:
- Slow network to OTEL Collector
- OTEL Collector overloaded
- Too frequent metric emission

### Issue: Metrics Delayed

**Diagnostic Steps**:

1. **Check batch export interval**:
   ```go
   // OpenTelemetry exports every 30 seconds by default
   // This is expected behavior
   ```

2. **Force export for testing**:
   ```go
   // Shutdown forces export
   telemetry.Shutdown(context.Background())
   ```

3. **Reduce export interval** (not recommended for production):
   ```yaml
   # In OTEL Collector config
   processors:
     batch:
       timeout: 5s  # Faster export (higher overhead)
   ```

**Common Causes**:
- Normal batching behavior (30s interval)
- OTEL Collector batch processor timeout
- Prometheus scrape interval

---

## Performance Characteristics

### Benchmarks

```
Benchmark Results (Go 1.25.0, darwin/arm64, Apple M1 Pro)

BenchmarkCounter-10                    50000000    25.3 ns/op      0 B/op    0 allocs/op
BenchmarkHistogram-10                  30000000    38.7 ns/op      0 B/op    0 allocs/op
BenchmarkGauge-10                      40000000    29.1 ns/op      0 B/op    0 allocs/op
BenchmarkCounterWithLabels-10          20000000    56.8 ns/op     48 B/op    1 allocs/op
BenchmarkStartSpan-10                  10000000   112.4 ns/op     96 B/op    2 allocs/op
BenchmarkBaggagePropagation-10          5000000   234.1 ns/op    128 B/op    3 allocs/op
```

### Resource Usage (Per Component)

| Metric | Development | Production | Notes |
|--------|-------------|------------|-------|
| Memory Baseline | ~5 MB | ~8 MB | Before initialization |
| Memory After Init | ~15 MB | ~25 MB | With OpenTelemetry SDK |
| Memory Per 10k Metrics | +2 MB | +2 MB | Batched exports |
| CPU Per 1M Metrics/sec | ~5% | ~5% | On 4-core system |
| Network Bandwidth | ~1 KB/s | ~10 KB/s | Depends on metric volume |

### Scalability Limits

| Dimension | Limit | Mitigation |
|-----------|-------|------------|
| Unique Metric Names | 10,000 | Use consistent naming conventions |
| Label Combinations | 1,000 (default) | Cardinality limiter enforced |
| Metrics/Second | 100,000 | Batching and sampling |
| Concurrent Emitters | Unlimited | Lock-free atomic operations |
| Trace Spans/Second | 10,000 | Sampling (1%-10% recommended) |

---

## Agent Skills Observation Contract

Skills reuse the existing injected `core.Telemetry` boundary; the telemetry
module has no skill dependency and no provider-specific code. Orchestration
creates child spans named `orchestrator.skills.*`, `skills.registry.*`,
`skills.store.*`, and `skills.admin.*`. Skill metric labels use only closed,
bounded enumerations for module, stage, boundary, outcome, selector, token kind,
content kind, source, retry outcome, action, severity,
diagnostic code, operation, and prompt kind. Namespace, skill name, exact
version, and resource name may appear as trace attributes for per-request
diagnosis, but never as metric labels.

Instruction text, resource bodies, selector payloads/responses, ETags,
idempotency keys, and raw backend errors are excluded from ordinary skill span
attributes/events and metrics. Model-call payload recording remains governed by
the existing explicit LLM-debug contract. Missing telemetry installs the same
NoOp implementation used by every other orchestration feature, so enabling
skills does not add a telemetry initialization requirement.

---

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 1.2 | 2026-08-12 | Documented the provider-neutral Agent Skills span, metric-cardinality, and content-exclusion contract |
| 1.1 | 2026-07-27 | Established exact/property-preserving baggage, metric-eligibility, conversation propagation, and format-twin LLM-recording contracts |
| 1.0 | 2025-09-28 | Initial architecture documentation |

---

## Related Documentation

- [Telemetry Module README](./README.md) - User-facing documentation and quick start
- [Framework Design Principles](../FRAMEWORK_DESIGN_PRINCIPLES.md) - Overall framework architecture
- [Core Module Architecture](../core/ARCHITECTURE.md) - Core module architectural rules
- [OpenTelemetry Specification](https://opentelemetry.io/docs/specs/otel/) - OTLP protocol details

---

**Remember**: The telemetry module is designed to be **invisible when it works, obvious when it doesn't**. Follow the patterns in this document to ensure reliable production observability.
