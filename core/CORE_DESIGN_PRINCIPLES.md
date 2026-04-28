# Truva-G3 Core Module Design Principles

**Version**: 1.0  
**Module**: `github.com/truvaagents/truva-g3/core`  
**Purpose**: Foundation module design principles and architectural guidelines  
**Audience**: Core maintainers, module implementers, LLM coding agents

---

## Core Module Mission

The **core module** is the foundation of the Truva-G3 framework. It defines all framework interfaces, provides base implementations, and ensures architectural consistency across all other modules. **Every other framework module depends on core - core depends on no other framework modules.**

### Primary Responsibilities

1. **Interface Definitions**: Define all framework contracts (`Component`, `Registry`, `Discovery`, `AIClient`, etc.)
2. **Base Implementations**: Provide extensible `BaseTool` and `BaseAgent` implementations
3. **Architectural Enforcement**: Use Go's type system to enforce Tool/Agent separation at compile time
4. **Configuration Intelligence**: Smart configuration with environment awareness and auto-injection
5. **Service Discovery Abstraction**: Platform-agnostic discovery with Redis and mock implementations
6. **Deployment Abstractions**: Kubernetes-aware address resolution and health checks

---

## Core Design Principles

### 1. **Interface-First Architecture**

**Rule**: Every external dependency and framework concept must be defined as an interface in core.

```go
// ✅ Good: Core defines interfaces, implementations are pluggable
type AIClient interface {
    GenerateResponse(ctx context.Context, prompt string, options *AIOptions) (*AIResponse, error)
}

// ✅ Modules implement the interface
type OpenAIClient struct { ... }
func (c *OpenAIClient) GenerateResponse(...) (*AIResponse, error) { ... }

// ❌ Bad: Core depending on concrete implementations
import "github.com/truvaagents/truva-g3/ai" // NEVER in core
```

**Benefits**:
- Testability through mocking
- Module interchangeability
- Dependency inversion principle
- Prevents circular dependencies

### 2. **Zero Framework Dependencies**

**Rule**: Core module must never import any other Truva-G3 framework modules.

**Current External Dependencies** (minimal and justified):
```go
// ✅ Justified external dependencies
require (
    github.com/go-redis/redis/v8 v8.11.5  // Redis implementation of discovery
    github.com/google/uuid v1.6.0         // ID generation
    github.com/stretchr/testify v1.11.1   // Testing only
)
```

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
        
        // Auto-configure related settings when intent is clear
        if enabled && provider == "redis" {
            // Try standard environment variables first
            redisURL := os.Getenv("REDIS_URL")
            if redisURL != "" {
                c.Discovery.RedisURL = redisURL
            } else if truvag3RedisURL := os.Getenv("TRUVAG3_REDIS_URL"); truvag3RedisURL != "" {
                c.Discovery.RedisURL = truvag3RedisURL
            } else if c.Discovery.RedisURL == "" {
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
3. TruvaG3-specific variables (`TRUVAG3_REDIS_URL`, etc.)
4. Sensible defaults (lowest)

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
// ✅ LoadFromEnv implementation: TRUVAG3_* variables take precedence
func (c *Config) LoadFromEnv() error {
    // TRUVAG3_* prefixed variables have priority in LoadFromEnv
    if v := os.Getenv("TRUVAG3_REDIS_URL"); v != "" {
        c.Discovery.RedisURL = v
    } else if v := os.Getenv("REDIS_URL"); v != "" {
        c.Discovery.RedisURL = v
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

// ✅ WithDiscovery() has different precedence: REDIS_URL takes precedence
func WithDiscovery(enabled bool, provider string) Option {
    // When auto-configuring Redis URL, standard REDIS_URL beats TRUVAG3_REDIS_URL
    if enabled && provider == "redis" {
        if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
            c.Discovery.RedisURL = redisURL
        } else if truvag3RedisURL := os.Getenv("TRUVAG3_REDIS_URL"); truvag3RedisURL != "" {
            c.Discovery.RedisURL = truvag3RedisURL
        }
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
// ✅ Actual implementation: configurable TTL in RedisRegistry
// From redis_registry.go:138
ttl: 30 * time.Second  // default; configurable via DiscoveryConfig.TTL or WithDiscoveryTTL()

// Registration expires after TTL unless refreshed by heartbeat
func (r *RedisRegistry) Register(ctx context.Context, info *ServiceInfo) error {
    key := fmt.Sprintf("truvag3:services:%s", info.ID)
    data, _ := json.Marshal(info)
    return r.client.SetEX(ctx, key, data, r.ttl).Err()
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
        {
            name: "REDIS_URL beats TRUVAG3_REDIS_URL",
            envVars: map[string]string{
                "REDIS_URL": "redis://standard:6379",
                "TRUVAG3_REDIS_URL": "redis://truvag3:6379",
            },
            options: []Option{WithDiscovery(true, "redis")},
            expectedResult: "redis://standard:6379",
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

### Integration Testing Patterns

#### 1. **Discovery System Testing**
```go
func TestRedisDiscoveryIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping Redis integration test in short mode")
    }
    
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
// ✅ Good: Redact secrets in logs
func (c *Config) String() string {
    // Never log API keys or sensitive information
    redacted := *c
    if redacted.AI.APIKey != "" {
        redacted.AI.APIKey = "[REDACTED]"
    }
    return fmt.Sprintf("%+v", redacted)
}
```

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

#### 3. **Connection Pooling**
```go
// ✅ Good: Reuse Redis connections
func NewRedisRegistry(redisURL string) (*RedisRegistry, error) {
    opts, err := redis.ParseURL(redisURL)
    if err != nil {
        return nil, fmt.Errorf("invalid Redis URL: %w", err)
    }
    
    // Configure connection pool
    opts.PoolSize = 10
    opts.MinIdleConns = 5
    
    client := redis.NewClient(opts)
    return &RedisRegistry{client: client, ttl: ttl}, nil  // ttl from NewRedisRegistryWithOptions, clamped (min 5s, default 30s)
}
```

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
- [ ] **Testing**: Comprehensive unit and integration tests

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

**Remember**: Every change to core affects all other modules. Prioritize backward compatibility, interface stability, and architectural consistency above implementation convenience.