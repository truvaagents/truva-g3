# TruvaG3 Framework Design Principles & Architecture Guidelines

**Version**: 1.2

**Purpose**: Ensure consistency and maintainability across all framework development

**Audience**: Core contributors, module developers, LLM-based coding agents

---

## Core Philosophy

### Mission Statement
TruvaG3 enables **autonomous agent networks** in production environments through **compile-time architectural enforcement** and **intelligent defaults**. Unlike orchestrated frameworks, TruvaG3 agents discover and collaborate dynamically without centralized coordination.

### Design Principles

#### 1. **Production-First Architecture**
- **Single Binary Deployment**: All components must compile to standalone executables
- **Minimal Dependencies**: No runtime dependency hell - if it's not in `go.mod`, it doesn't exist
- **Fast Startup**: <1 second initialization for any component
- **Small Footprint**: Target 10-50MB memory per component, <20MB container images
- **Built-in Reliability**: Circuit breakers, retries, and health checks are not afterthoughts

#### 2. **Compile-Time Architectural Enforcement**
- **Interface-Based Separation**: Architecture violations must be caught at compile time
- **Tool/Agent Distinction**: 
  - Tools implement `Registry` interface only (passive, register-only)
  - Agents implement `Discovery` interface (active, can find others)
  - **This is enforced by Go's type system - not convention**
- **No Circular Dependencies**: Module dependency graph must be a proper DAG

#### 3. **Intelligent Configuration Over Convention**
- **Smart Defaults**: Framework should work with minimal configuration
- **Environment-Aware**: Automatically detect and use standard environment variables
- **Auto-Configuration**: When user intent is clear, auto-configure related settings
- **Explicit Override**: Always allow explicit configuration to override defaults
  through a supported customization seam. An override replaces a default; it
  does not disable downstream correctness, integrity, wire-format, cache, or
  lifecycle invariants.
- **Simple things should be simple, complex things should be possible** (Alan Kay): Every framework feature should offer layered access — a convenience path for common use (minimal code, env-var-driven), a customisation path for domain-specific needs (swap interfaces via options), and direct access to underlying constructors for full control. Developers drop to a lower layer for one concern without abandoning the higher layer for everything else. No cliffs between layers.
- **Configuration Split**: Numeric tuning (token budgets, TTLs, limits) uses environment variables — deployable without code changes. Behavioural plugs (custom interfaces, scoring functions) use `WithXXX()` option functions — domain-specific, requires code. If an option just sets a number, it should be an env var.

#### 4. **Interface-First Design**
- **Dependency Inversion**: All modules depend on `core` interfaces, not implementations
- **Testability**: All external dependencies must be mockable through interfaces
- **Modularity**: Each module implements well-defined interfaces from `core`
- **Extensibility**: New implementations can be swapped without changing dependent code
- **Composition over Bundling**: Cross-cutting concerns should be composable functions that return primitives the developer passes to the orchestrator — not monolithic constructors that bundle multiple concerns into a single call. Each module creates its own primitives; the application assembles them.

#### Safe Customization and Invariant Boundaries

Customization must identify what kind of seam it exposes rather than treating
every value as an unrestricted replacement:

1. **Defaults are replaceable.** A developer can override a default through
   typed configuration, an option, an injected interface, or a direct
   constructor at the documented layer.
2. **Additive content stays additive.** A bounded guidance, rule, or filter seam
   may refine framework behavior, but it cannot replace the framework-owned
   identity, protocol, validation, precedence, or output contract around it.
   A genuinely different behavior uses an explicit interface or direct
   constructor instead of smuggling a replacement through a string setting.
3. **Invariants remain enforced at their owning boundary.** Dropping to a lower
   composition layer gives control over dependencies and behavior; it does not
   make downstream validation, capability eligibility, integrity checks,
   concurrency rules, or lifecycle contracts optional. If an invariant must
   change, that is a reviewed contract change rather than configuration.
4. **Invalid explicit configuration fails construction unless the feature
   documents a semantically safe fallback.** The framework must never silently
   discard a validation error or install a nil or partially configured
   component. A documented fallback must emit its documented diagnostic and
   still validate the fallback component. A genuinely absent optional
   dependency may retain documented no-op behavior.

Environment variables are best suited to bounded scalar values, closed modes,
and references such as mounted-file paths. Large or multiline instructions and
templates should use typed Go configuration or a bounded, startup-loaded file
unless the feature explicitly documents and validates an inline representation.
The feature contract must state whether content is additive or replacement,
its precedence, its limits, and whether it is read once or watched.

Any extension or deployment setting that can change a cached semantic result
must be represented in the applicable cache identity. When the framework cannot
derive a stable identity for custom behavior, normal execution may continue but
cache reuse must be treated as ineligible rather than assuming equivalence.

#### Layered Composition Example

The following example demonstrates the principles above using shared memory as the cross-cutting concern. The same pattern applies to any framework feature.

```go
// Layer 1 — Convenience: most developers (minimal code, env-var-driven)
backends, _ := memory.NewSharedBackends(redisClient, logger)                         // module creates primitives
hooks, coord := orchestration.BuildMemoryHooks(backends.ToDeps(), aiClient, logger)  // builds hooks
deps.PipelineHooks = hooks                                                           // application composes
// Numeric tuning via env var: TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL=10m

// Layer 2 — Customisation: swap behaviour without dropping convenience
hooks, coord := orchestration.BuildMemoryHooks(backends.ToDeps(), aiClient, logger,
    orchestration.WithMemoryEntityExtractor(myExtractor),  // behavioural plug (interface)
)

// Layer 3 — Direct construction: full control over every option
enrichHook, _ := orchestration.NewMemoryEnrichmentHook(episodic, coordinator, name, domain, opts...)
deps.PipelineHooks = []core.PipelineHook{enrichHook}

// ❌ Bad: Monolithic constructor that bundles multiple concerns
agent := framework.NewSmartAgent(config)  // hides composition — what hooks? what order?
agent.Run()                                // no way to replace one part without escaping entirely
```

**Framework is domain-agnostic.** A corollary of "each module does its job" — the framework provides indexing primitives, storage interfaces, and hook contracts; **domain semantics come from the agent or its tools**. The framework has no opinion about what counts as a "pod", "order", "flight", or "patient". Hardcoding domain-specific patterns into framework defaults (e.g., a regex extractor that matches K8s pod names) is a layering violation: the framework default leaks into every agent regardless of whether it's actually a K8s agent. The right pattern is a framework-level no-op default that the agent or tool overrides with domain-aware logic. See [orchestration/ARCHITECTURE.md:367](orchestration/ARCHITECTURE.md#L367) for the same principle applied to parameter resolution.

#### Patterns That Endure

The principles in §3 and §4 above are extracted from software systems that have remained stable and relevant for 10+ years. When a future design decision is in doubt, vet it against these patterns:

- **Go's `database/sql`** (13 years, zero breaking changes): The framework defines interfaces (`driver.Driver`, `driver.Conn`); drivers register themselves and implement the interface. The framework composes; the plugin implements. Drivers never call back into framework lifecycle. After `sql.Open()` returns, the developer tunes behaviour at the call site (`db.SetMaxOpenConns(25)`) — not via factory configuration. **Lesson**: The framework composes, the plugin implements. Behaviour tuning is at the call site, not in factories. ([source](https://eli.thegreenplace.net/2019/design-patterns-in-gos-databasesql-package/))

- **Spring Boot Starters** (opinionated defaults + escape hatch): A starter provides a sensible default stack. Auto-configuration is **non-invasive** — at any point, the developer can define their own bean to replace specific parts without abandoning the starter. **Lesson**: Auto-configuration backs off when the developer defines their own. Presets are replaceable, not prisons. ([source](https://docs.spring.io/spring-boot/reference/using/auto-configuration.html))

- **Kubernetes Controller Pattern** (reconciliation — many simple things > one smart thing): Each controller manages ONE resource kind. Complex systems are a stack of simple controllers, not one monolithic controller. The scheduler doesn't know about the deployment controller. Controllers compose through shared state (etcd), not through code coupling. **Lesson**: Many simple things > one smart thing. Avoid monolithic abstractions that bundle multiple concerns. ([source](https://kubernetes.io/docs/concepts/architecture/controller/))

- **React Hooks** (composition beats inheritance): Higher-Order Components wrapped components in layers of invisible abstractions. Hooks made the same capabilities composable at the call site. The consumer decides what to compose, not the framework. **Lesson**: Let the consumer compose, don't compose for them. Provide primitives, not opinions about how to combine them. ([source](https://www.robinwieruch.de/react-hooks-higher-order-components/))

- **Go's Design Philosophy** ("Accept interfaces, return structs"): "The bigger the interface, the weaker the abstraction." Rob Pike: "Simplicity is complicated — it requires work to achieve." Go's 1.0 Compatibility Guarantee means code written in 2012 compiles and runs identically on Go 1.24 — stability comes from narrow, focused APIs. **Lesson**: Small interfaces that compose > large interfaces that prescribe. ([source](https://dave.cheney.net/2016/08/20/solid-go-design))

- **controller-runtime's `manager.Runnable`** (7+ years, the background-task interface used by most modern Go Kubernetes operators): A single-method interface — `Start(ctx context.Context) error` — for anything the manager should run in parallel with the main reconcile loop. Leader election, cache, webhook server, metrics server all implement `Runnable`. The manager calls `Start`, the runnable blocks until `ctx.Done()`, returns nil on clean shutdown. The manager cannot forcibly terminate a goroutine; honouring ctx is the runnable's contract, not the framework's. **Lesson**: A single blocking method with ctx-based cancellation is the right shape for background jobs. Don't invent `Stop()`, `Cancel()`, `Shutdown()` companion methods — they duplicate what ctx already expresses. TruvaG3's `core.Runnable` has the same shape and the same contract. ([source](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/manager#Runnable))

**Synthesis — the rules these patterns produce:**

1. **Each module does its job. The application composes.** Memory creates backends. Orchestration creates hooks. The application wires them together. No module reaches into another module's concerns.
2. **The factory stays dumb.** Convenience functions create primitives from explicit inputs. They don't read 15 env vars and bundle 5 components in opinionated order. The application sees every step.
3. **Plugins never reach into framework lifecycle.** A backend implementation doesn't call `Start()` on framework components. A driver doesn't open the database. Direction of control flows from framework → plugin, never the reverse.
4. **Convenience layers are presets — transparent and replaceable.** A convenience function reads env vars, applies sensible defaults, and assembles primitives. The developer sees what it does, can replace any part, and can drop to lower layers without abandoning the rest.
5. **No cliffs between layers.** Layer 1 calls Layer 2 calls Layer 3. A developer drops one concern to a lower layer (e.g., custom backend) without abandoning the higher layer for everything else. Adding `WithCustomBackend(...)` to a Layer 1 call should work.
6. **Behaviour tuning at the call site.** Numeric tuning belongs in env vars (per-deployment, ops-team-controlled). Behavioural plugs belong in `WithXXX()` options at the call site (per-domain, code-controlled). If an option just sets a number, it should be an env var.

---

## Module Architecture

### Core Module (Required Foundation)
**Responsibility**: Define all interfaces and provide base implementations

**Must Provide**:
- All framework interfaces (`Component`, `Registry`, `Discovery`, `AIClient`, `Telemetry`, etc.)
- Base implementations (`BaseTool`, `BaseAgent`)
- Configuration system with intelligent defaults
- Service discovery primitives
- Framework dependency injection

**Must NOT**:
- Import any other framework modules (dependency direction violation)
- Contain business logic beyond framework mechanics
- Make assumptions about specific implementations (Redis, OpenAI, etc.)

### Optional Modules

**Dependency Rule**: An optional framework module may import `core` and, when
it needs the shared observability implementation, `telemetry`. It must not
import a sibling optional framework module. These are maximum allowed direct
framework edges, not requirements that every module use both dependencies.

**Canonical framework-module DAG**:

```text
root facade  ──────────────────────> core
telemetry    ──────────────────────> core
ai           ──────────────────────> core + telemetry
memory       ──────────────────────> core + telemetry
resilience   ──────────────────────> core + telemetry
orchestration ─────────────────────> core + telemetry
core         ──────────────────────> no framework module
```

Packages below a module path are part of that same module and do not create a
new framework-module edge. Third-party libraries are governed by minimality,
ownership, and provider-neutrality rules separately; the allowance above does
not prohibit a module from using justified external libraries.

The module boundary applies equally to production source, internal packages,
module-local guide/example packages, and `_test.go` files in the framework
modules listed above. A test helper must not import a forbidden sibling module
to bypass the production DAG. Test-only reference implementations should stay
unexported in `_test.go` files and use existing or standard-library
dependencies unless an independently justified test dependency is required.
Standalone application and deployment examples are composition roots, not
framework library modules; they may import multiple framework modules to wire
the application, but no framework module may depend back on them.

A new feature does not automatically justify a new top-level Go module. Keep it
in the module that owns and consumes its lifecycle and expose narrow interfaces
there. Creating a module or adding a framework-module edge requires an explicit
architecture decision and a corresponding update to this canonical DAG; an
acyclic edge is still invalid if it is not listed here.

**Enforcement**:

- review both `go.mod` requirements and package-level imports;
- derive production and test import edges with `go list` for every workspace
  module;
- fail the owning module's architecture tests or CI guard when an unlisted
  framework edge appears;
- do not rely on successful compilation alone, because Go can compile a new
  acyclic edge that still violates this framework boundary.

**Critical Architectural Rule**: The `core` module **NEVER** imports optional modules. This ensures:
1. **Unidirectional dependency flow** - Core is the foundation
2. **True optional modules** - Telemetry remains genuinely optional
3. **Automated graph enforcement** - Architecture tests or CI reject unlisted
   edges even when the Go compiler could build them
4. **No circular dependencies** - The canonical graph is acyclic by design

---

## Implementation Guidelines

### Configuration System Rules

#### 1. **WithXXX() Option Functions**
```go
// ✅ Good: Smart auto-configuration
func WithDiscovery(enabled bool, provider string) Option {
    // Auto-configure related settings when intent is clear
    if enabled && provider == "redis" {
        // Auto-set Redis URL from environment variables
    }
}

// ❌ Bad: Dumb property setting
func WithDiscovery(enabled bool, provider string) Option {
    // Just set properties without intelligence
}
```

#### 2. **Environment Variable Precedence**
Standard precedence order (highest to lowest):
1. Explicitly set configuration options
2. `REDIS_URL`, `OPENAI_API_KEY`, etc. (standard names)
3. `TRUVAG3_*` prefixed variables
4. Sensible defaults (`localhost:6379`, etc.)

#### 3. **Environment Variable Naming - No Duplicates**
Before adding a new `TRUVAG3_*` environment variable, **always check for existing variables** that serve the same purpose:

```go
// ❌ Bad: Creating duplicate/conflicting env vars
TRUVAG3_AGENT_NAME        // Already exists for agent identity
TRUVAG3_ORCHESTRATOR_NAME // DON'T CREATE - same concept, causes confusion

// ✅ Good: Reuse existing env vars across modules
TRUVAG3_AGENT_NAME  // Single source of truth for agent identity
                   // Used by: HITL isolation, DAG visualization, logging
```

**Rules**:
- **Search before creating**: Grep for similar concepts in existing env var parsing
- **One concept = One variable**: If `TRUVAG3_AGENT_NAME` identifies the agent, don't create `TRUVAG3_ORCHESTRATOR_NAME`
- **Document usage**: When an env var is used by multiple modules, document all use cases in comments
- **Cross-module awareness**: Check how other modules (orchestration, HITL, telemetry) name similar concepts

#### 4. **Fail-Safe Defaults**
- Components must work with zero configuration in development
- Production deployment should require minimal explicit configuration
- Missing optional dependencies should not break core functionality

#### 5. **Externalize Hardcoded Limits — but only the operational ones**
Not every number is an operator knob. Distinguish two classes, and treat them differently:

**A. Operational limits** — a number an operator would plausibly tune per deployment because it bounds **capacity or externally-observable behavior**: token/byte budgets, TTLs, timeouts, concurrency caps, retry counts, size thresholds that gate a feature. These **must** be:
1. Defined as a field in the relevant config struct (e.g., `OrchestratorConfig`)
2. Set to a sensible default in `DefaultConfig()`
3. Overridable via a `TRUVAG3_*` environment variable, parsed with `strconv.Atoi` and guarded with `val > 0` — **unless** the limit has a documented zero-valued *disable* sentinel (a value where `0` is a meaningful "off", not "unset"), in which case parse with `val >= 0` so an explicit `0` is accepted and can pin the feature off (e.g. `TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD`, where `0` = disabled). The guard's job is to reject a *typo* from silently zeroing a budget; it must not reject a deliberate, documented `0`.
4. Documented in `docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md` and `docs/reference/LIMITS_CHEATSHEET.md`

Hardcoded operational limits that seem reasonable at development time can cause production failures when workloads differ from expectations (e.g., prompt truncation hiding critical data, timeout too short for cross-agent delegation). Externalizing them allows deployment-specific tuning without code changes.

**B. Internal algorithmic invariants** — a ratio or constant that governs an algorithm's **internal correctness/quality**, not deployment capacity, and that an operator has no basis to tune: safety ratios, quality thresholds, heuristic weights. These should be **named constants with a unit test pinning the behavior and a one-line rationale in `LIMITS_CHEATSHEET.md`** — **not** environment variables. Making every internal heuristic operator-configurable creates configuration noise and turns invariants into support surface. Examples: `maxWrapperShare` (0.5 — the wrapper-vs-chunk ratio that flips the map-reduce chunker to lossless byte-splitting) and `degenerateKeptRatio` (0.05 — the kept-fraction below which a structural trim is disclosed as "severely reduced"). Changing these changes *what the algorithm does*, not *how much a deployment can handle*; they belong in code, pinned by tests.

**Deciding which class:** ask "would an SRE tuning this deployment's capacity ever set this, and could they reason about a good value from their workload?" Yes → class A (env var). No, it's an internal quality/safety knob → class B (documented const). When in doubt, prefer a documented const with a test; promote to an env var only when a real deployment need appears.

### Component Lifecycle Rules

#### 1. **Consistent Behavior Across Components**
```go
// ✅ Both Tools and Agents must behave identically
func (t *BaseTool) Start(ctx context.Context, port int) error {
    return t.server.ListenAndServe() // Blocks until shutdown
}

func (a *BaseAgent) Start(ctx context.Context, port int) error {
    return a.server.ListenAndServe() // Blocks until shutdown  
}
```

#### 2. **Initialization Order**
1. **Configuration**: Apply all options, resolve environment variables
2. **Dependencies**: Auto-inject framework dependencies if needed
3. **Registration**: Register with discovery system if enabled
4. **Heartbeat**: Start keep-alive mechanisms for persistent services

#### 3. **Graceful Shutdown**
- All components must handle context cancellation
- Unregister from discovery systems before shutdown
- Close external connections cleanly

#### 4. **Background Jobs Implement `core.Runnable`**

Any component that needs to run in parallel with the HTTP server — periodic jobs, schedulers, queue consumers, expiry processors — implements `core.Runnable` and registers with the framework via `framework.RegisterRunnable(r)`. It does not start its own goroutine, does not expose its own `Stop()` method, and does not need wrapper code in the application's `main.go`.

```go
// ✅ Runnable — framework manages lifecycle
type MyBackgroundJob struct { /* ... */ }

func (j *MyBackgroundJob) Start(ctx context.Context) error {
    ticker := time.NewTicker(j.interval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            _ = j.runOnce(ctx)  // fail-open; log errors internally
        case <-ctx.Done():
            return nil           // clean shutdown
        }
    }
}

// In main.go:
framework.RegisterRunnable(&MyBackgroundJob{interval: 24 * time.Hour})
framework.Run(ctx)  // starts the HTTP server AND the runnable in parallel
```

Rules:

1. **`Start(ctx) error` is the entire contract.** Block until ctx is cancelled. Return nil on clean shutdown, error on startup or runtime failure. No `Stop()`, `Cancel()`, `Shutdown()`, or `stopCh` channels — ctx cancellation drives shutdown.
2. **Runnables must honour `ctx.Done()` within the drain timeout** (`TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT`, default `10s`). Buggy runnables that ignore ctx will leak goroutines until process exit — Go provides no mechanism for forcibly terminating them, so this is the runnable's responsibility, not the framework's.
3. **The framework starts runnables in parallel goroutines** and drains them after the HTTP server stops. `Framework.Run` blocks until both the HTTP server and all runnables have exited (or the drain timeout fires).
4. **New background work inside the framework should be a `Runnable`**, not a hand-rolled goroutine + channel + defer-stop block in the application's `main.go`. In-tree reference implementations are `memory.ReflectionJob` (memory-module Tier 2→3 reflection — LLM-driven bridging from episodic events to semantic knowledge) and `core.MemoryStoreSweeper` (core-module periodic eviction of expired `*core.MemoryStore` entries).

See [core/interfaces.go](core/interfaces.go) for the interface definition. Reference implementations:
- [memory/reflection_job.go](memory/reflection_job.go) — domain Runnable in a leaf module.
- [core/memory_store.go](core/memory_store.go) — `MemoryStoreSweeper`, a Runnable in the core module itself; registered via `framework.AutoRegisterMemorySweeper()` (agents) or `framework.RegisterRunnable(core.NewMemoryStoreSweeper(...))` (tools).

### Discovery System Rules

#### 1. **Automatic Registration**
```go
// ✅ Framework handles registration automatically
func (t *BaseTool) Initialize(ctx context.Context) error {
    if t.Registry != nil && config.Discovery.Enabled {
        // Auto-register and start heartbeat
    }
}

// ❌ User should not need manual registration
tool.Registry.Register(ctx, serviceInfo) // Should be automatic
```

#### 2. **TTL and Heartbeat Management**
- All registrations must have TTL (30 seconds default)
- Components must start heartbeat automatically after registration
- Heartbeat failures should trigger circuit breaker behavior

#### 3. **Capability-Based Discovery**
- Components are discovered by what they can do, not by name
- Capability definitions must be consistent across network
- Support both specific capability matches and pattern matching

### Error Handling Principles

#### 1. **Fail-Fast for Configuration Errors**
```go
// ✅ Configuration problems should fail immediately
func NewConfig(opts ...Option) (*Config, error) {
    if criticalConfigMissing {
        return nil, fmt.Errorf("configuration error: %w", err)
    }
}
```

#### 2. **Resilient Runtime Behavior**
```go
// ✅ Runtime problems should be handled gracefully
func (a *Agent) Process(ctx context.Context) error {
    if a.Telemetry != nil {
        a.Telemetry.Counter("requests.processed")
        // If telemetry fails, continue processing
    }
}
```

#### 3. **Circuit Breaker Integration**
- External API calls must be protected by circuit breakers
- Discovery calls must have circuit breaker protection
- Failed dependencies should not prevent startup (degrade gracefully)

---

## Testing Requirements

### Unit Test Coverage
- **Same-change ownership**: Every newly written production behavior that can
  be isolated must receive a unit test in the same change. This includes public
  adapters and defaulting wrappers, even when their implementation is small;
  their tests pin the contract they expose. Type/interface declarations and
  branches that cannot execute on the tested platform do not require artificial
  tests, but the reason must be clear in review. A coverage percentage is
  supporting evidence, not a substitute for behavior-specific assertions.
- **Interfaces**: Mock all external dependencies
- **Configuration**: Test all option combinations and precedence rules
- **Error Paths**: Test failure scenarios and error propagation
- **Edge Cases**: Empty configurations, missing dependencies, network failures

### Integration Test Patterns
```go
// ✅ Good: Test actual framework behavior
func TestFrameworkDependencyInjection(t *testing.T) {
    framework, err := core.NewFramework(agent,
        core.WithDiscovery(true, "redis"), // Should auto-configure
    )
    assert.NoError(t, err)
    // Verify auto-configuration worked
}

// ❌ Bad: Test implementation details
func TestConfigurationInternals(t *testing.T) {
    // Testing internal configuration fields
}
```

### Regression Prevention
- All fixed bugs must have regression tests
- Breaking changes must be caught by compilation failures
- Performance regressions must be caught by benchmarks

---

## Code Quality Standards

### Interface Design
```go
// ✅ Good: Minimal, focused interfaces
type Registry interface {
    Register(ctx context.Context, info *ServiceInfo) error
}

// ❌ Bad: Bloated interfaces
type Registry interface {
    Register(ctx context.Context, info *ServiceInfo) error
    GetMetrics() map[string]int // Mixing concerns
    Configure(config Config) error // Configuration is separate
}
```

### Error Messages
```go
// ✅ Good: Actionable error messages
return fmt.Errorf("failed to connect to Redis at %s: %w (check REDIS_URL environment variable)", url, err)

// ❌ Bad: Vague error messages  
return fmt.Errorf("connection failed: %w", err)
```

### Documentation
- All public interfaces must have clear godoc comments
- Complex configuration options must have usage examples
- Breaking changes must be documented in CHANGELOG.md

---

## Backwards Compatibility

### API Stability
- Public interfaces are stable once released
- Configuration options are stable once released
- Breaking changes require major version bump

### Deprecation Process
1. **Mark as deprecated** with clear migration path
2. **Keep working** for at least one minor version
3. **Remove** in next major version

### Migration Guidelines  
- Provide automated migration tools when possible
- Document migration steps clearly
- Support both old and new patterns during transition

---

## Performance Requirements

### Resource Usage
- **Memory**: Components must not leak memory over time
- **Goroutines**: Must clean up goroutines on shutdown
- **Network**: Must respect connection pooling and limits

### Scalability Targets
- **1000+ concurrent agents** on single machine (through goroutines)
- **Sub-second response times** for discovery operations
- **Minimal CPU overhead** from framework internals

---

## Security Considerations

### Secrets Management
- Never log secrets or API keys
- Support secret rotation without restart
- Use secure defaults (TLS, authentication)

### Network Security
- Default to secure communication protocols
- Support authentication for discovery systems
- Validate all external inputs

---

## Monitoring and Observability

### Telemetry Architecture Pattern

**Design Decision**: Telemetry uses **explicit initialization** at the application level, not framework-level auto-wiring.

**Why This Pattern?**

```go
// ❌ Framework CANNOT do this (violates architectural principles)
import "github.com/truvaagents/truva-g3/telemetry"  // Core cannot import modules

// ✅ Applications MUST do this (explicit initialization)
func main() {
    telemetry.Initialize(telemetry.UseProfile(telemetry.ProfileProduction))
    // Now all components can use telemetry
}
```

**Rationale**:
1. **Architectural Purity**: Core module cannot import telemetry module (dependency direction)
2. **True Optionality**: Telemetry remains genuinely optional at compile time
3. **Explicit Control**: Applications have full control over telemetry lifecycle
4. **No Magic**: Clear, predictable initialization order

**Integration Pattern**:

```go
// Step 1: Core defines interface (no implementation knowledge)
type Telemetry interface {
    RecordMetric(name string, value float64, labels map[string]string)
}

// Step 2: Components have Telemetry field (defaults to NoOp)
type BaseTool struct {
    Telemetry Telemetry  // Safe default: &NoOpTelemetry{}
}

// Step 3: Telemetry module provides global singleton
var globalRegistry atomic.Value  // Stores *OTelProvider

// Step 4: Application initializes telemetry
telemetry.Initialize(config)  // Sets up global registry

// Step 5: Application code emits metrics
telemetry.Counter("requests.total")  // Uses global registry
```

**Key Characteristics**:
- **Global Singleton**: `telemetry.Initialize()` sets up a global registry
- **Thread-Safe**: Atomic operations for concurrent metric emission
- **Zero-Cost if Unused**: NoOp implementation when not initialized
- **Standard Environment Variables**: Respects `OTEL_EXPORTER_OTLP_ENDPOINT`

### Built-in Telemetry Requirements

**For Framework Code**:
- Never assume telemetry is initialized
- Always check for nil before using Telemetry interface
- Use NoOp default in constructors
- Never fail operations due to telemetry failures

```go
// ✅ Good: Safe telemetry usage
func (t *BaseTool) processRequest() {
    if t.Telemetry != nil {
        t.Telemetry.RecordMetric("requests.total", 1.0, nil)
    }
    // Continue processing even if telemetry fails
}
```

**For Application Code**:
- Initialize telemetry in `main()` before creating components
- Use `defer telemetry.Shutdown()` for clean shutdown
- Configure via environment variables or explicit config
- Support both development and production profiles

### Health Checks
- All components must provide `/health` endpoints
- Health checks must be fast (<100ms) and reliable
- Support both liveness and readiness probes
- Health status should include telemetry initialization state (optional)

---

## Framework Evolution Guidelines

### Adding New Features
1. **Design interfaces first** in `core` module
2. **Implement in separate module** (avoid core bloat)
3. **Provide intelligent defaults** in configuration system
4. **Add comprehensive tests** including integration scenarios
5. **Update documentation** with examples

### Modifying Existing Features  
1. **Maintain backwards compatibility** in public APIs
2. **Add deprecation warnings** for old patterns
3. **Provide migration path** for breaking changes
4. **Update all related tests** and documentation

### Code Review Checklist
- [ ] Follows interface-first design
- [ ] Maintains Tool/Agent architectural separation
- [ ] Includes intelligent configuration defaults
- [ ] Has comprehensive test coverage
- [ ] Provides clear error messages
- [ ] Updates relevant documentation
- [ ] No backwards compatibility breaks without major version
- [ ] Production and test imports preserve the canonical framework-module DAG
- [ ] No new top-level module or framework-module edge lacks an explicit
      architecture decision and corresponding principles update
- [ ] Telemetry usage is nil-safe (checks before use)
- [ ] Application examples show proper telemetry initialization

---

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 1.2 | 2026-08-09 | Defined the canonical framework-module DAG and extended its enforcement to production, test, and conformance code |
| 1.1 | 2026-08-08 | Added safe-customization, invariant-boundary, and cache-identity principles |
| 1.0 | 2025-09-28 | Initial framework design principles |

---

**Remember**: These principles exist to maintain TruvaG3's core promise of **autonomous agent networks in production**. When in doubt, favor production reliability over development convenience, and architectural clarity over implementation simplicity.
