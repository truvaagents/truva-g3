# TruvaG3 Core Module Architecture

**Version**: 1.13
**Module**: `github.com/truvaagents/truva-g3/core`  
**Purpose**: Foundation module architecture, contracts, and design principles  
**Audience**: Core maintainers, module implementers, LLM coding agents

---

## Core Module Mission

The **core module** is the foundation of the TruvaG3 framework. It defines all framework interfaces, provides base implementations, and ensures architectural consistency across all other modules. **Every other framework module depends on core - core depends on no other framework modules.**

### Primary Responsibilities

1. **Interface Definitions**: Define all framework contracts (`Component`, `Registry`, `Discovery`, `AIClient`, etc.)
2. **Base Implementations**: Provide extensible `BaseTool` and `BaseAgent` implementations
3. **Architectural Enforcement**: Use Go's type system to enforce Tool/Agent separation at compile time
4. **Configuration Intelligence**: Smart configuration with environment awareness and auto-injection
5. **Service Discovery Abstraction**: Platform-agnostic discovery with Redis and mock implementations
6. **Deployment Abstractions**: Kubernetes-aware address resolution and health checks
7. **Opaque Correlation Carriers**: Validate and transport request-scoped identifiers without owning application session semantics

---

## Core Design Principles

### 1. **Interface-First Architecture**

**Rule**: Every external dependency and framework concept must be defined as an interface in core.

```go
// ✅ Good: Core defines interfaces, implementations are pluggable
type AIClient interface {
    GenerateResponse(ctx context.Context, prompt string, options *AIOptions) (*AIResponse, error)
}

// Additive capability: callers that need presence-aware request intent,
// sanitized preparation reports or detailed usage can use
// AIRequestClient without changing the legacy AIClient contract.
type AIRequestClient interface {
    AIClient
    Generate(context.Context, *AIRequest) (*AIResult, error)
}

// StreamingAIRequestClient and the canonical StreamAI dispatcher provide the
// same lossless capability selection for streaming calls.
type StreamingAIRequestClient interface {
    AIRequestClient
    Stream(context.Context, *AIRequest, StreamCallback) (*AIResult, error)
}

// AIRequestFingerprinter is optional. AI-output caches use only stable,
// secret-free policy and route fingerprints; stable=false means bypass.
type AIRequestFingerprinter interface {
    RequestFingerprint(context.Context, *AIRequest) (string, bool)
}

// AIRequestReport is sanitized provider evidence. Presence-aware effective
// temperature/max-token fields describe the post-policy provider body:
// Inherit means unreported, Omit means absent, and Set means sent.
// Prompts, credentials, and raw request bodies are forbidden.

// LegacyRepresentable centralizes the lossless-fallback decision so wrappers
// and cache adapters do not duplicate parameter representability rules.
func (r *AIRequest) LegacyRepresentable() bool

// GenerateAI and StreamAI are the canonical dispatchers. They prefer the
// request-aware capabilities and use legacy clients only when the request can
// be represented without loss.
func generate(ctx context.Context, client AIClient, request *AIRequest) (*AIResult, error) {
    return GenerateAI(ctx, client, request)
}

// ✅ Modules implement the interface
type OpenAIClient struct { ... }
func (c *OpenAIClient) GenerateResponse(...) (*AIResponse, error) { ... }

// ❌ Bad: Core depending on concrete implementations
import "github.com/truvaagents/truva-g3/ai" // NEVER in core
```

#### Portable AI request semantics

`AIOptions.ResponseFormat` is deliberately narrow: `""` means unspecified and
`"json"` is the only non-empty portable value. Provider adapters translate that
semantic value into the provider's native wire spelling. Native objects such as
OpenAI-compatible `json_object` or `json_schema` requests belong in
`AIOptions.Extra`; their native spellings are not aliases for the portable
field.

**Benefits**:
- Testability through mocking
- Module interchangeability
- Dependency inversion principle
- Prevents circular dependencies
- Additive AI capabilities without forcing provider or orchestration imports

### 2. **Zero Framework Dependencies**

**Rule**: Core module must never import any other TruvaG3 framework modules.

**Current External Dependencies** (minimal and justified):
```go
// ✅ Justified external dependencies
require (
    github.com/redis/go-redis/v9 v9.22.0  // Redis implementation of discovery
    github.com/google/uuid v1.6.0          // ID generation
    github.com/stretchr/testify v1.11.1    // Testing only
)
```

Framework-owned Redis clients pass their options through
`ApplyRedisUniversalDefaults`; `ApplyRedisClientDefaults` remains a standalone
options helper. This keeps go-redis/v9 on RESP2 and preserves the
established timeout, retry, and idle-connection defaults across standalone,
Sentinel, and cluster clients.
The dialer implementation, TCP keepalive, and buffer sizing use go-redis/v9
defaults unless the application overrides them. An application-owned client may
choose RESP3 explicitly, but the application must validate its complete command
and backend surface before injecting that client. The helper returns a shallow
copy, clones mutable address/TLS inputs, fills only zero-value option fields,
and leaves explicit non-zero settings and negative timeout sentinels
application-owned.

#### Redis and Valkey topology contract

`RedisConnectionConfig` is Core's topology-neutral connection profile. Its mode
is explicit—address count never guesses topology—and
`NewRedisUniversalClient` constructs exactly one of a standalone, Sentinel, or
cluster client. Standalone requires one address, Sentinel requires one or more
Sentinel addresses plus a master name, and cluster requires one or more seed
addresses. Every mode requires DB 0 and rejects a non-zero DB at configuration time.

`REDIS_URL` is the standard DB-0 standalone shorthand. Structured
`TRUVAG3_REDIS_MODE` and `TRUVAG3_REDIS_*` connection fields express all three
topologies. Those sources are mutually exclusive; combining them is an invalid
configuration rather than a precedence case. The removed
`TRUVAG3_REDIS_URL` alias fails with a bounded configuration error when non-empty.
`ResolveRedisConnectionConfig` returns the validated `RedisConnectionConfig`
directly; there is no deprecation-diagnostic wrapper. Numbered-DB constants,
reserved-DB naming helpers, and the old `RedisClientOptions` constructor are
removed. Namespaced command clients use `NewRedisClientWithConnection` or
`NewRedisClientWithClient`. Pool size is per node in cluster mode, so the
deployment-wide upper bound is approximately `PoolSize * shard nodes` (plus Sentinel/control-plane
connections where applicable).

Constructors that create a client own and close it. Injected construction accepts
an application-owned `redis.UniversalClient` (or the narrower `redis.Cmdable`
where only commands are needed) and never closes it. APIs such as
`NewSchemaCache(redis.Cmdable, ...)` use that interface directly instead of
retaining a duplicate concrete-client wrapper. `CheckRedisStartup` bounds the
constructor's wait independently of go-redis `ContextTimeoutEnabled`, without
changing options or closing a borrowed pool. If its PING ignores cancellation,
the outstanding command finishes under the client's socket deadline or the
owner's close; applications remain responsible for closing failed-startup
clients they own, especially when I/O deadlines are disabled. This preserves dependency
injection, lets applications supply managed Redis or Valkey clients, and avoids
moving orchestration concepts into Core. The
`RedisKeyspace` validates deployment names and defines the canonical
`truvag3:v1:<deployment>:<subsystem>` DB-0 layout. Both plain and tagged keys
keep the deployment before the subsystem; tagged keys add the deployment,
subsystem, and atomic scope inside braces later in the key. This gives ACLs,
metrics, and operator tooling one stable deployment prefix.

The Redis registry keeps service records and its all/name/type/capability
indexes in the deployment-wide `{<deployment>:registry}` slot. Registration,
heartbeat, and unregister transactions are therefore single-slot. Discovery
uses explicit sets and `SSCAN`, never keyspace `SCAN`; expired index members are
pruned best-effort through a same-slot Lua existence recheck plus index removal.
A re-registration between hydration and cleanup must retain its fresh index
membership. This deliberately places all registry traffic for one
deployment on one primary. At 1,000 agents and a 30-second heartbeat, the
steady-state refresh load is roughly 33 heartbeats/second before multiplying by
the bounded number of index refreshes per service—a few hundred commands per
second for typical capability counts. That control-plane concentration is an
accepted tradeoff at the documented scale and must be revisited before much
larger deployments.

**Forbidden**:
```go
// ❌ Never allowed in core
import "github.com/truvaagents/truva-g3/ai"
import "github.com/truvaagents/truva-g3/telemetry"
import "github.com/truvaagents/truva-g3/resilience"
```

### 3. **Compile-Time Architectural Enforcement**

**Rule**: Use Go's type system to prevent architectural violations at compile time.

```go
// ✅ Enforced separation - Tools cannot discover
type Tool interface {
    Component
    Start(ctx context.Context, port int) error
    RegisterCapability(cap Capability)
    // NO discovery methods - physically impossible to call
}

// ✅ Agents have full discovery capabilities
type Agent interface {
    Component
    Start(ctx context.Context, port int) error
    RegisterCapability(cap Capability)
    Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)
}
```

**Result**: If a tool tries to call discovery methods, it won't compile.

### 4. **Base Implementation Extensibility**

**Rule**: `BaseTool` and `BaseAgent` must be extensible through embedding, not modification.

```go
// ✅ Good: User extends through embedding
type MyTool struct {
    *core.BaseTool
    customField string
}

func NewMyTool() *MyTool {
    return &MyTool{
        BaseTool: core.NewTool("my-tool"),
        customField: "value",
    }
}

// ❌ Bad: Modifying BaseTool directly
// Don't add business-specific fields to BaseTool
```

### 5. **Configuration Intelligence Over Convention**

**Rule**: Configuration system should require minimal user input while being completely customizable.

```go
// ✅ Good: Smart auto-configuration
func WithDiscovery(enabled bool, provider string) Option {
    return func(c *Config) error {
        c.Discovery.Enabled = enabled
        c.Discovery.Provider = provider

        // Preserve a connection already resolved from explicit configuration
        // or the environment; otherwise install the local standalone default.
        if enabled && provider == "redis" {
            if c.Discovery.RedisConnection == nil {
                connection := DefaultRedisConnectionConfig()
                c.Discovery.RedisConnection = &connection
                c.Discovery.RedisURL = "redis://localhost:6379"
            }
        }
        return nil
    }
}
```

**Configuration Priority (implemented)**:
1. Explicit function options (highest)
2. Standard environment variables (`REDIS_URL`, `OPENAI_API_KEY`)
3. TruvaG3-specific variables (`TRUVAG3_REDIS_MODE`, etc.)
4. Sensible defaults (lowest)

Precedence applies only to compatible representations. Contradictory Redis
topology forms—for example `REDIS_URL` plus structured mode/address fields—fail
validation instead of silently overriding one another.

---

## Conversation Correlation Context Contract

Core owns the transport-safe representation of a multi-turn conversation
identifier; it does not decide what an application session means. An
application may deliberately use the same opaque value for `session_id` and
`conversation_id`, but that mapping belongs to the application. Core never
loads sessions, groups executions, or derives conversation identity from
history, timestamps, users, or agents.

The public contract in `request_context.go` is:

- `ValidateConversationID` accepts an opaque, non-empty identifier of at most
  `MaxConversationIDLength` (512 bytes). Its allowed visible-ASCII ranges are a
  protocol invariant chosen so the value can be propagated exactly as W3C
  baggage. It returns a bounded `ConversationIDValidationReason` and never
  includes the rejected value.
- `WithConversationID` records a programmatic candidate.
  `ExtractRequestContext` records a separate
  `X-TruvaG3-Conversation-ID` header candidate. Keeping the two slots separate
  makes effective precedence independent of call order.
- A present explicit-header candidate has precedence over a programmatic
  candidate, including when the header is invalid. Therefore an invalid
  higher-precedence value cannot reveal or inherit a lower-precedence value.
- `GetConversationIDCandidate` preserves source, presence, and bounded
  rejection reason for the orchestration ingress resolver.
  `GetConversationID` exposes only the effective validated value.
- Invalid candidates are represented without retaining their raw value.
  `WithoutConversationID` shadows both candidate slots and the effective value
  while preserving every unrelated context value. This is the supported
  defense against identity leakage when a context is reused.

This is intentionally a small value/helper API rather than a new interface or
session abstraction. It keeps core dependency-free and leaves resolution,
storage, telemetry, and UI behavior to the modules that own those concerns.

---

## Architectural Patterns

### Interface Hierarchy

```
Component (base interface)
├── Tool (passive - register only)
│   └── BaseTool (concrete implementation)
└── Agent (active - register + discover)
    └── BaseAgent (concrete implementation)

HTTPComponent (unified framework interface)
├── Tool (embeds Component + adds HTTP methods)
└── Agent (embeds Component + adds HTTP + Discovery methods)

Registry (registration capability)
└── Discovery (embeds Registry + adds discovery methods)
```

**Key Interface Definitions** (from actual code):
```go
// Component - base interface for all framework components
type Component interface {
    Initialize(ctx context.Context) error
    GetID() string
    GetName() string
    GetCapabilities() []Capability
    GetType() ComponentType
}

// HTTPComponent - unified interface for HTTP-capable components
type HTTPComponent interface {
    Component
    Start(ctx context.Context, port int) error
    RegisterCapability(cap Capability)
}

// Tool - passive components (no discovery)
type Tool interface {
    Component
    Start(ctx context.Context, port int) error
    RegisterCapability(cap Capability)
    // Tools cannot discover other components
}

// Agent - active components (with discovery)
type Agent interface {
    Component
    Start(ctx context.Context, port int) error
    RegisterCapability(cap Capability)
    Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)
}
```

### Dependency Injection Pattern

**Core Framework Responsibility**: Auto-inject dependencies based on configuration intent.

```go
// ✅ Framework should handle this automatically
framework, err := core.NewFramework(tool,
    core.WithDiscovery(true, "redis"),
)
// Tool.Registry should be automatically configured and injected

// ❌ User should not need manual setup
registry, _ := core.NewRedisRegistry("redis://localhost:6379")
tool.Registry = registry // Should be automatic
```

### Discovery Abstraction Pattern

```go
// Abstract interfaces (defined in core)
type Registry interface {
    Register(ctx context.Context, info *ServiceInfo) error
    UpdateHealth(ctx context.Context, id string, status HealthStatus) error
    Unregister(ctx context.Context, id string) error
}

type Discovery interface {
    Registry // Embeds registration capability
    Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)
    FindService(ctx context.Context, serviceName string) ([]*ServiceInfo, error)
    FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error)
}

// Concrete implementations (in core for platform independence)
type RedisRegistry struct { ... }     // Production implementation
type MockDiscovery struct { ... }     // Testing implementation
```

---

## Implementation Guidelines

### Interface Design Rules

#### 1. **Minimal Interface Principle**
```go
// ✅ Good: Focused, single-responsibility interface with context support
type Logger interface {
    // Basic logging methods
    Info(msg string, fields map[string]interface{})
    Error(msg string, fields map[string]interface{})
    Warn(msg string, fields map[string]interface{})
    Debug(msg string, fields map[string]interface{})

    // Context-aware methods for distributed tracing and request correlation
    InfoWithContext(ctx context.Context, msg string, fields map[string]interface{})
    ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{})
    WarnWithContext(ctx context.Context, msg string, fields map[string]interface{})
    DebugWithContext(ctx context.Context, msg string, fields map[string]interface{})
}

// ❌ Bad: Bloated interface mixing concerns
type Logger interface {
    Info(msg string, fields map[string]interface{})
    Error(msg string, fields map[string]interface{})
    GetMetrics() map[string]int  // Wrong concern
    Configure(cfg LogConfig)     // Configuration is separate
}
```

#### 2. **Context-First Parameter Pattern**
```go
// ✅ Good: Context is always first parameter
func (d *Discovery) Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)

// ❌ Bad: Context in wrong position
func (d *Discovery) Discover(filter DiscoveryFilter, ctx context.Context) ([]*ServiceInfo, error)
```

#### 3. **Error Handling Conventions**
```go
// ✅ Good: Return error as last parameter
func Register(ctx context.Context, info *ServiceInfo) error

// ✅ Good: Use wrapped errors for context
return fmt.Errorf("failed to register service %s: %w", info.Name, err)
```

### Base Implementation Rules

#### 1. **Dependency Injection Points**
```go
// ✅ Base implementations must have injectable dependencies
type BaseTool struct {
    // Core fields
    ID           string
    Name         string
    Type         ComponentType
    Capabilities []Capability

    // Injectable dependencies (all optional)
    Registry  Registry   // Can register only
    Logger    Logger
    Telemetry Telemetry
    AI        AIClient

    // Configuration
    Config *Config

    // Note: BaseTool intentionally does not provide a Memory field. Response
    // caching is a tool-local optimization with tool-specific TTL/eviction
    // semantics; tools that want it should add their own cache field
    // (e.g. `cache *core.MemoryStore`) opted in explicitly. See
    // FRAMEWORK_DESIGN_PRINCIPLES.md §"Composition over Bundling".
}
```

#### 2. **Initialization Patterns**
```go
// ✅ Good: Base implementations handle dependency auto-injection
func (t *BaseTool) Initialize(ctx context.Context) error {
    // Auto-inject dependencies if configured
    if t.Registry != nil && t.Config.Discovery.Enabled {
        // Auto-register with discovery system
        // Auto-start heartbeat
    }
    return nil
}
```

#### 3. **Capability Management**
```go
// ✅ Good: Thread-safe capability management
type BaseTool struct {
    capabilities []Capability
    capMutex     sync.RWMutex
}

func (t *BaseTool) RegisterCapability(cap Capability) {
    t.capMutex.Lock()
    defer t.capMutex.Unlock()
    
    // Auto-generate endpoint if not provided
    if cap.Endpoint == "" {
        cap.Endpoint = fmt.Sprintf("/api/capabilities/%s", cap.Name)
    }
    
    t.capabilities = append(t.capabilities, cap)
    // Register HTTP handler
}
```

### Configuration System Rules

#### 1. **Option Function Pattern**
```go
// ✅ Good: Option functions with intelligence
type Option func(*Config) error

func WithPort(port int) Option {
    return func(c *Config) error {
        if port <= 0 || port > 65535 {
            return fmt.Errorf("invalid port %d: must be between 1-65535", port)
        }
        c.Port = port
        return nil
    }
}
```

#### 2. **Environment Variable Loading**
```go
// ✅ LoadFromEnv delegates Redis source selection to one shared resolver.
func (c *Config) LoadFromEnv() error {
    if redisConnectionSourcePresent(os.LookupEnv) {
        resolution, err := ResolveRedisConnectionConfig(nil, os.LookupEnv)
        if err != nil {
            return err
        }
        c.Discovery.RedisConnection = &resolution
    }
    // Memory is in-process only; no env-var URL.
    if v := os.Getenv("TRUVAG3_MEMORY_CLEANUP_INTERVAL"); v != "" {
        if d, err := time.ParseDuration(v); err == nil && d > 0 {
            c.Memory.CleanupInterval = d
        }
    }
    // ... continue for other variables
    return nil
}

// ✅ WithDiscovery preserves the profile already resolved by LoadFromEnv.
func WithDiscovery(enabled bool, provider string) Option {
    if enabled && provider == "redis" && c.Discovery.RedisConnection == nil {
        connection := DefaultRedisConnectionConfig()
        c.Discovery.RedisConnection = &connection
    }
}
```

#### 3. **Configuration Validation**
```go
// ✅ Good: Validate configuration after all options applied
func (c *Config) Validate() error {
    if c.Name == "" {
        return fmt.Errorf("component name is required")
    }
    if c.Discovery.Enabled && c.Discovery.Provider == "" {
        return fmt.Errorf("discovery provider is required when discovery is enabled")
    }
    return nil
}
```

### Service Discovery Rules

#### 1. **ServiceInfo Structure**
```go
// ✅ Complete service information for discovery (from component.go)
type ServiceInfo struct {
    ID           string                 `json:"id"`           // Unique identifier
    Name         string                 `json:"name"`         // Component name
    Type         ComponentType          `json:"type"`         // "tool" or "agent"
    Description  string                 `json:"description"`  // Human-readable description
    Address      string                 `json:"address"`      // Network address
    Port         int                    `json:"port"`         // Network port
    Capabilities []Capability           `json:"capabilities"` // What it can do
    Metadata     map[string]interface{} `json:"metadata"`     // Environment info
    Health       HealthStatus           `json:"health"`       // Current health status
    LastSeen     time.Time              `json:"last_seen"`    // TTL tracking
}

// ✅ Capability structure (from agent.go)
type Capability struct {
    Name        string           `json:"name"`        // Unique identifier
    Description string           `json:"description"` // What it does
    Endpoint    string           `json:"endpoint"`    // Where to call it (auto-generated if empty)
    InputTypes  []string         `json:"input_types"` // Expected input formats
    OutputTypes []string         `json:"output_types"`// Output formats
    Handler     http.HandlerFunc `json:"-"`          // Optional custom handler (excluded from JSON)
}
```

#### 2. **Discovery Filter Design**
```go
// ✅ Good: Flexible filtering with reasonable defaults
type DiscoveryFilter struct {
    Type         ComponentType `json:"type,omitempty"`         // Filter by tool/agent
    Name         string        `json:"name,omitempty"`         // Filter by name
    Capabilities []string      `json:"capabilities,omitempty"` // Filter by capabilities
    Metadata     map[string]interface{} `json:"metadata,omitempty"` // Filter by metadata
    HealthStatus HealthStatus  `json:"health_status,omitempty"` // Filter by health
}
```

#### 3. **TTL and Heartbeat Management**
```go
// ✅ Key shape: configurable TTL in RedisRegistry
ttl: 30 * time.Second  // default; configurable via DiscoveryConfig.TTL or WithDiscoveryTTL()

// Registration expires after TTL unless refreshed by heartbeat. The actual
// writer atomically maintains the service record and registry indexes.
func (r *RedisRegistry) Register(ctx context.Context, info *ServiceInfo) error {
    key := r.keys.service(info.ID)
    data, _ := json.Marshal(info)
    return r.client.Set(ctx, key, data, r.ttl).Err()
}

// StartHeartbeat accepts an explicit heartbeat interval (0 = ttl/2).
// Minimum 2s. Must be < TTL. See NewRedisRegistryWithOptions for TTL clamping.
func (r *RedisRegistry) StartHeartbeat(ctx context.Context, id string, heartbeatInterval time.Duration) {
    // Default: refresh registration every ttl/2 (~15s with 30s TTL)
    // Configurable via DiscoveryConfig.HeartbeatInterval or WithHeartbeatInterval()
}
```

---

## Testing Patterns

### Unit Testing Rules

#### 1. **Interface Mocking**
```go
// ✅ Good: Mock external dependencies through interfaces
type MockRegistry struct {
    registerCalls []ServiceInfo
    registerError error
}

func (m *MockRegistry) Register(ctx context.Context, info *ServiceInfo) error {
    m.registerCalls = append(m.registerCalls, *info)
    return m.registerError
}

func TestBaseTool_Initialize(t *testing.T) {
    mockRegistry := &MockRegistry{}
    tool := &BaseTool{
        Registry: mockRegistry,
        Config: &Config{Discovery: DiscoveryConfig{Enabled: true}},
    }
    
    err := tool.Initialize(context.Background())
    assert.NoError(t, err)
    assert.Len(t, mockRegistry.registerCalls, 1)
}
```

#### 2. **Configuration Testing**
```go
// ✅ Good: Test all configuration scenarios
func TestConfigurationPrecedence(t *testing.T) {
    tests := []struct {
        name           string
        envVars        map[string]string
        options        []Option
        expectedResult string
    }{
        {
            name: "explicit option beats environment",
            envVars: map[string]string{"REDIS_URL": "redis://env:6379"},
            options: []Option{WithRedisURL("redis://explicit:6379")},
            expectedResult: "redis://explicit:6379",
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Set environment variables
            for k, v := range tt.envVars {
                os.Setenv(k, v)
                defer os.Unsetenv(k)
            }
            
            config, err := NewConfig(tt.options...)
            assert.NoError(t, err)
            assert.Equal(t, tt.expectedResult, config.Discovery.RedisURL)
        })
    }
}

func TestConfigurationRejectsContradictoryRedisSources(t *testing.T) {
    t.Setenv("REDIS_URL", "redis://standalone:6379")
    t.Setenv("TRUVAG3_REDIS_MODE", "cluster")
    t.Setenv("TRUVAG3_REDIS_ADDRS", "cluster-a:6379,cluster-b:6379")

    _, err := NewConfig(WithDiscovery(true, "redis"))
    assert.ErrorIs(t, err, ErrInvalidConfiguration)
}
```

#### 3. **Architectural Constraint Testing**
```go
// ✅ Good: Test that architectural constraints are enforced
func TestToolCannotDiscover(t *testing.T) {
    tool := NewTool("test-tool")
    
    // This should not compile if Tool interface had Discover method
    // tool.Discover(ctx, filter) // Compilation error
    
    // Verify tool can only register
    assert.Implements(t, (*Registry)(nil), tool.Registry)
    
    // Verify tool cannot be cast to Discovery
    _, ok := tool.(Discovery)
    assert.False(t, ok, "Tools should not implement Discovery interface")
}
```

### Reusable Contract Conformance

Provider-neutral interfaces owned by Core may ship reusable executable suites
under the separately importable `core/conformance` package. This placement
keeps the contract beside its owner without making `core` depend on an optional
framework module or forcing provider tests to duplicate behavioral rules.

The conformance package is test support, not runtime framework behavior:

- it may import only the standard library and the root `core` package;
- it must not import orchestration or another optional framework module;
- runtime packages must not import it;
- exported `Run*Conformance` functions accept factories for implementations
  and test observable contract behavior rather than implementation details;
- provider and in-memory implementations run every suite corresponding to the
  capabilities they advertise.

Current suites cover AI-options cloning, `TaskConsumer` delivery profiles,
`TaskStore`, `ScheduleStore`, and the legacy `TaskQueue`. Orchestration-owned
persistence and coordination contracts remain in
`orchestration/backendconformance`; moving those suites into Core would move
ownership in the wrong direction.

### Optional Integration Testing Pattern

Integration tests are optional, manually invoked supplements and must remain
outside CI. Put them behind the `integration` build tag instead of relying on
`testing.Short`; CI runs the complete default unit suite without `-short`.

#### 1. **Discovery System Testing**

```go
//go:build integration

package core

func TestRedisDiscoveryIntegration(t *testing.T) {
    registry, err := NewRedisRegistry("redis://localhost:6379")
    assert.NoError(t, err)
    
    // Test registration and discovery
    info := &ServiceInfo{
        ID: "test-service",
        Name: "test",
        Type: ComponentTypeTool,
        Capabilities: []Capability{{Name: "test_capability"}},
    }
    
    err = registry.Register(context.Background(), info)
    assert.NoError(t, err)
    
    // Test discovery
    results, err := registry.Discover(context.Background(), DiscoveryFilter{
        Capabilities: []string{"test_capability"},
    })
    assert.NoError(t, err)
    assert.Len(t, results, 1)
}
```

---

## Error Handling Standards

### Comprehensive Error System (Implemented)

#### 1. **Framework Error Types** (from errors.go)
```go
// ✅ Complete sentinel error system already implemented
var (
    // Agent-related errors
    ErrAgentNotFound      = errors.New("agent not found")
    ErrAgentNotReady      = errors.New("agent not ready")
    ErrAgentAlreadyExists = errors.New("agent already exists")

    // Capability-related errors
    ErrCapabilityNotFound   = errors.New("capability not found")
    ErrCapabilityNotEnabled = errors.New("capability not enabled")

    // Discovery-related errors
    ErrServiceNotFound      = errors.New("service not found")
    ErrDiscoveryUnavailable = errors.New("discovery service unavailable")

    // Configuration errors
    ErrInvalidConfiguration = errors.New("invalid configuration")
    ErrMissingConfiguration = errors.New("missing required configuration")
    ErrPortOutOfRange       = errors.New("port out of range")

    // State errors
    ErrAlreadyStarted    = errors.New("already started")
    ErrNotInitialized    = errors.New("not initialized")
    ErrAlreadyRegistered = errors.New("already registered")

    // Operation errors
    ErrTimeout            = errors.New("operation timeout")
    ErrContextCanceled    = errors.New("context canceled")
    ErrMaxRetriesExceeded = errors.New("maximum retries exceeded")

    // HTTP/Network errors
    ErrConnectionFailed = errors.New("connection failed")
    ErrRequestFailed    = errors.New("request failed")

    // Resilience errors
    ErrCircuitBreakerOpen = errors.New("circuit breaker open")

    // AI operation errors
    ErrAIOperationFailed = errors.New("AI operation failed")
)

// ✅ Error classification helpers implemented
func IsRetryable(err error) bool { ... }
func IsNotFound(err error) bool { ... }
func IsConfigurationError(err error) bool { ... }
func IsStateError(err error) bool { ... }

// ✅ Structured error type for rich context
type FrameworkError struct {
    Op      string // Operation that failed
    Kind    string // Kind of error (e.g., "validation", "configuration")
    Message string // Human-readable message
    Err     error  // Underlying error
}
```

#### 2. **Graceful Degradation**
```go
// ✅ Good: Optional dependencies don't break core functionality
func (t *BaseTool) Initialize(ctx context.Context) error {
    // Core functionality always works
    t.setupHTTPHandlers()
    
    // Optional features degrade gracefully
    if t.Registry != nil && t.Config.Discovery.Enabled {
        if err := t.Registry.Register(ctx, t.buildServiceInfo()); err != nil {
            // Log but don't fail initialization
            t.Logger.Error("Failed to register with discovery service", map[string]interface{}{
                "error": err.Error(),
                "component": t.Name,
            })
            // Tool still works without discovery
        }
    }
    
    return nil
}
```

---

## Security Considerations

### Secrets Management

#### 1. **No Secret Logging**
```go
// Keep the original error for control flow; sanitize only the observation.
logger.Error("Provider request failed", map[string]interface{}{
    "operation":  "provider_request",
    "error_type": "upstream",
    "error":      core.RedactSensitiveText(err.Error()),
})
return err
```

`RedactSensitiveText` is a dependency-free, explicit, lossy transformation. An
application or subsystem may choose it for a diagnostic field after deciding
that reduced fidelity is appropriate. It recognizes common authorization
headers, credential assignments, secret-bearing URL user information, and
credential query parameters while retaining useful diagnostic structure. It is
not a general secret detector, and the framework does not automatically apply
it as a persistence or observability policy.

When an adopter explicitly chooses to return a transformed error,
`RedactSensitiveError` provides the same observable-message transformation
while implementing `Unwrap` so `errors.Is` and `errors.As` still reach the
original cause. Use `RedactSensitiveText` for observation-only fields and
`RedactSensitiveError` when error-chain control flow must survive that chosen
transformation.

Some legacy orchestration, skills, provider, and backend diagnostic paths still
call these helpers automatically. They are documented exceptions pending a
dedicated audit, not a security guarantee or a precedent for new framework
paths. Built-in debug stores preserve the values they receive; applications own
any required payload classification and sanitization.

#### 2. **Input Validation**
```go
// ✅ Good: Validate all external inputs
func (r *RedisRegistry) Register(ctx context.Context, info *ServiceInfo) error {
    if info == nil {
        return fmt.Errorf("service info cannot be nil")
    }
    if info.ID == "" {
        return fmt.Errorf("service ID cannot be empty")
    }
    if info.Name == "" {
        return fmt.Errorf("service name cannot be empty")
    }
    // Continue validation...
}
```

---

## Performance Considerations

### Resource Management

#### 1. **Memory Management**
```go
// ✅ Good: Prevent memory leaks in capability management
func (t *BaseTool) GetCapabilities() []Capability {
    t.capMutex.RLock()
    defer t.capMutex.RUnlock()
    
    // Return copy to prevent external modification
    caps := make([]Capability, len(t.capabilities))
    copy(caps, t.capabilities)
    return caps
}
```

#### 2. **Goroutine Management**
```go
// ✅ Good: Proper goroutine lifecycle management
func (r *RedisRegistry) StartHeartbeat(ctx context.Context, id string, heartbeatInterval time.Duration) {
    go func() {
        // heartbeatInterval is clamped: min 2s, must be < TTL, default ttl/2
        ticker := time.NewTicker(heartbeatInterval)
        defer ticker.Stop()

        for {
            select {
            case <-ctx.Done():
                return // Clean exit on context cancellation
            case <-ticker.C:
                r.refreshRegistration(ctx, id)
            }
        }
    }()
}
```

> **Note on new background work**: The `StartHeartbeat` example above predates
> the `core.Runnable` interface and is grandfathered as legacy. Per
> [FRAMEWORK_DESIGN_PRINCIPLES.md §"Background Jobs Implement `core.Runnable`"](../FRAMEWORK_DESIGN_PRINCIPLES.md),
> **new background jobs in core (or anywhere) should implement `core.Runnable`**
> and register with `Framework.RegisterRunnable` — no `StartXxx` methods,
> no `Stop()` companion methods, no internal `stopCh` channels. In-tree
> reference implementations are `memory.ReflectionJob` and
> `core.MemoryStoreSweeper`. The existing `StartHeartbeat` will be migrated
> in a separate cleanup PR if/when prioritized.

#### 3. **Connection Pooling and Ownership**
```go
// ✅ Owning path: construct, verify, and close with the registry.
func NewRedisRegistryWithConnection(
    profile RedisConnectionConfig,
    keyspace RedisKeyspace,
    ttl time.Duration,
) (*RedisRegistry, error) {
    client, err := NewRedisUniversalClient(profile)
    if err != nil {
        return nil, fmt.Errorf("initialize Redis registry: %w: %w", ErrConnectionFailed, err)
    }
    registry, err := newRedisRegistry(client, true, keyspace, ttl)
    if err != nil {
        _ = client.Close()
        return nil, err
    }
    return registry, nil
}

// ✅ Injected path: the application retains client lifecycle ownership.
func NewRedisRegistryWithClient(
    client redis.UniversalClient,
    keyspace RedisKeyspace,
    ttl time.Duration,
) (*RedisRegistry, error) {
    return newRedisRegistry(client, false, keyspace, ttl)
}
```

---

## Pipeline Short-Circuit and Cache-Variation Contracts

Core keeps the legacy `PipelineContext`, `PipelineShortCircuit`, and
`BeforePlanningHook` field/method shapes unchanged. Provenance-aware behavior is
additive:

- `BeforePlanningDecisionHook` classifies a response as `authoritative` or
  `cache` without teaching core any feature semantics;
- authoritative policy, denial, rate-limit, and canned responses remain valid
  independently of cache variation;
- a cache decision returns the variation map persisted with the cache entry,
  not the gate's current map echoed at lookup time; and
- `PipelineGate.CacheVary` exposes a defensive copy of request-local named
  dimensions. The orchestration consumer performs exact symmetric comparison:
  a missing dimension on either side is a mismatch.

Dimension names and values are opaque strings to core. The feature that owns a
dimension owns its projection and semantics; core provides only provenance and
transport. This keeps the contract reusable for memory, conversation
compaction, model-routing, policy, and later features without importing any of
their packages or vocabulary.

### Pipeline-hook effect reporting

Core also owns the optional, typed `PipelineHookEffectReporter` extension seam.
Orchestration injects an invocation-bound reporter into the hook context only
when execution-debug persistence is configured. A hook calls
`ReportPipelineHookEffect` with a stable effect ID, schema version, effect
status, timing, and optional `json.RawMessage` data. A hook announces a new
`pending` effect before its invocation returns; detached work may then report
the same ID to transition it to a terminal status. This makes asynchronous
lifecycle accounting race-free without adding storage concepts to core.

The contract deliberately separates a hook invocation outcome from its effects:
a callback can return successfully while a fail-open write fails or background
work remains pending. Effect data is exact-fidelity debug evidence. Core does
not redact, truncate, summarize, interpret, or persist it; orchestration and the
configured execution store own capture and retention. Applications that emit
custom effect data own its schema and sensitivity. Effect status is explicitly
producer-reported; it is not an independent verification of backend durability.

---

## Implementation Checklist

### Adding New Interfaces

- [ ] **Minimal Interface**: Single responsibility, focused methods
- [ ] **Context Parameter**: `context.Context` as first parameter where applicable  
- [ ] **Error Handling**: Return error as last parameter
- [ ] **Documentation**: Clear godoc with usage examples
- [ ] **Mock Implementation**: For testing purposes
- [ ] **Validation**: Input parameter validation

### Extending Base Implementations

- [ ] **Dependency Injection**: Support for optional dependencies
- [ ] **Configuration**: Integration with config system
- [ ] **Error Handling**: Graceful degradation for optional features
- [ ] **Thread Safety**: Proper synchronization for concurrent access
- [ ] **Resource Cleanup**: Proper cleanup in shutdown scenarios
- [ ] **Testing**: Mandatory comprehensive unit tests; optional integration
      tests remain manual and outside CI

### Configuration Changes

- [ ] **Backward Compatibility**: Don't break existing configurations
- [ ] **Environment Variables**: Support standard and TruvaG3-prefixed variants
- [ ] **Validation**: Validate configuration after all options applied
- [ ] **Documentation**: Update configuration examples
- [ ] **Testing**: Test all precedence scenarios

---

## Current Implementation Status

### ✅ **Completed & Verified**
- Interface definitions for all framework contracts
- Tool/Agent architectural separation enforced at compile-time
- Base implementations with dependency injection support
- Configuration system with intelligent auto-configuration
- Redis-based discovery implementation with TTL and heartbeat
- Mock implementations for testing
- Kubernetes-aware address resolution
- CORS support for web integration
- Framework dependency injection (fixed September 2025)
- Opaque, bounded, call-order-independent conversation correlation context

### ⚠️ **Needs Review/Refinement**
- Performance optimization for high-throughput scenarios
- Security audit of configuration and secret handling
- Memory usage optimization for large-scale deployments
- Enhanced capability matching (semantic, pattern-based)

### 📋 **Future Enhancements**
- Additional discovery backend implementations (etcd, Consul)
- Enhanced capability matching (semantic, pattern-based)
- Configuration hot-reload capabilities
- Advanced health check patterns

---

**Core Module Philosophy**: *"Provide everything other modules need, depend on nothing they provide. Enable architectural correctness through type system constraints, not documentation."*

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 1.13 | 2026-09-08 | Removed numbered-DB constructors/constants and deprecation-only resolution plumbing under the author-approved development exception; all owned topologies validate DB 0 and borrowed-client ownership is unchanged |
| 1.12 | 2026-09-08 | Made discovery cleanup atomic with its missing-record recheck, preserved explicit namespace precedence over invalid environment values, and shared independently bounded startup checks without taking ownership of borrowed clients |
| 1.11 | 2026-09-04 | Scoped topology-aware Redis client and configuration diagnostics to `framework/core` and required bounded, request-correlated health/error observations without endpoint or credential text |
| 1.10 | 2026-09-04 | Added the typed request-scoped pipeline-hook effect reporting seam, producer-reported status semantics, exact debug-data ownership, and race-free pending-effect lifecycle contract |
| 1.9 | 2026-09-02 | Made command-interface injection canonical for schema caching and kept Redis pool/retry/timeout defaults solely in `NewRedisUniversalClient` |
| 1.8 | 2026-08-31 | Made the versioned deployment-first DB-0 keyspace normative and documented the registry's single-slot `SSCAN` indexes plus the accepted 1,000-agent heartbeat hot-slot estimate |
| 1.7 | 2026-08-31 | Added explicit standalone, Sentinel, and cluster connection profiles; centralized universal-client defaults and source resolution; documented DB-0 cluster validation, per-node pooling, deprecated numbered-DB compatibility, keyspace preparation, and injected-client ownership |
| 1.6 | 2026-08-30 | Documented the module-owned `core/conformance` test-support package and its dependency, ownership, and runtime-import constraints |
| 1.5 | 2026-08-29 | Clarified that redaction helpers are explicit adopter/subsystem transformations and documented the remaining automatic framework uses as legacy exceptions |
| 1.4 | 2026-08-20 | Defined `AIOptions.ResponseFormat="json"` as the sole non-empty portable structured-output value and reserved native formats for `Extra` |
| 1.3 | 2026-08-17 | Migrated the included Redis implementation to go-redis/v9 with explicit RESP2 compatibility defaults and application-owned RESP3 opt-in |
| 1.2 | 2026-08-09 | Added generic provenance-aware pipeline short-circuit and named cache-variation contracts without changing legacy hook payloads |
| 1.1 | 2026-07-27 | Established the pre-release opaque conversation-correlation context and validation contract |
| 1.0 | 2025-09-28 | Initial architecture documentation |

**Remember**: Every change to core affects all other modules. Prioritize backward compatibility, interface stability, and architectural consistency above implementation convenience.
