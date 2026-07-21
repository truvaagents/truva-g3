# API Reference

A comprehensive guide to TruvaG3's APIs with practical examples and best practices.

## Quick Navigation

**Getting Started:**
- [NewFramework](#newframework) - Bootstrap your component with the framework
- [NewBaseAgent](#newbaseagent) - Create agents that discover and orchestrate
- [NewTool](#newtool) - Build tools that provide specific capabilities
- [NewAIAgent](#newaiagent) - Create AI-powered orchestration agents

**Key Features:**
- [RegisterCapability](#registercapability) - Define capabilities with AI-powered payload generation (3-phase approach)
- [Schema Cache](#schema-cache-phase-3-validation) - Redis-backed schema caching for validation
- [Schema Discovery](#registercapability) - Progressive enhancement: Phase 1 (descriptions) → Phase 2 (field hints) → Phase 3 (validation)
- [Request-Aware AI API](#request-aware-ai-api) - Presence-aware requests, policy, enterprise hooks, and heterogeneous failover

**By Module:**
- [Core](#core-module) - Foundation types and component lifecycle
- [AI](#ai-module) - AI provider integration and intelligent agents
- [Resilience](#resilience-module) - Circuit breakers and retry mechanisms
- [Telemetry](#telemetry-module) - Metrics, tracing, and observability
- [Orchestration](#orchestration-module) - Multi-agent coordination

---

## Core Module

The foundation of TruvaG3 - components, discovery, and lifecycle management.

### Component Interface

Every TruvaG3 component (tools and agents) implements this interface, providing a consistent API for initialization, identification, and capability discovery.

```go
type Component interface {
    Initialize(ctx context.Context) error
    GetID() string
    GetName() string
    GetCapabilities() []Capability
    GetType() ComponentType
}
```

**Why this matters:** This unified interface allows the framework to manage any component type consistently, whether it's a simple tool or complex orchestration agent.

**Example:**
```go
// Any component can be queried uniformly
func inspectComponent(comp core.Component) {
    fmt.Printf("Component: %s (ID: %s)\n", comp.GetName(), comp.GetID())
    fmt.Printf("Type: %s\n", comp.GetType()) // "agent" or "tool"

    caps := comp.GetCapabilities()
    fmt.Printf("Capabilities (%d):\n", len(caps))
    for _, cap := range caps {
        fmt.Printf("  - %s: %s\n", cap.Name, cap.Description)
    }
}
```

### NewBaseAgent

Creates an agent - an active component that can discover and orchestrate other services. Agents are the "brains" of your system, capable of finding tools and other agents to accomplish complex tasks.

```go
func NewBaseAgent(name string) *BaseAgent
func NewBaseAgentWithConfig(config *Config) *BaseAgent
```

**When to use agents vs tools:**
- **Use an Agent when:** You need to discover services, coordinate multiple components, or build orchestrators
- **Use a Tool when:** You're providing a specific capability that others will use

**Example - Simple Agent:**
```go
// Quick start with minimal config
agent := core.NewBaseAgent("data-processor")

// Agent can discover other services (tools can't!)
services, _ := agent.Discover(ctx, core.DiscoveryFilter{
    Type: core.ComponentTypeTool,
    Capabilities: []string{"database"},
})

agent.Initialize(ctx)
agent.Start(ctx, 8080)
```

**Example - Production Agent:**
```go
// Full configuration for production
config := core.NewConfig(
    core.WithName("orchestrator"),
    core.WithPort(8080),
    core.WithRedisURL("redis://redis:6379"),      // Enable discovery
    core.WithLogLevel("info"),
    core.WithEnableMetrics(true),
)

agent := core.NewBaseAgentWithConfig(config)

// Register what this agent can do
agent.RegisterCapability(core.Capability{
    Name:        "orchestrate",
    Description: "Coordinate multiple services to complete tasks",
    Handler: func(w http.ResponseWriter, r *http.Request) {
        // Handler implementation
    },
})

// Initialize connects to Redis, sets up telemetry, etc.
if err := agent.Initialize(ctx); err != nil {
    log.Fatal("Failed to initialize:", err)
}

// Start begins serving HTTP requests
if err := agent.Start(ctx, 8080); err != nil {
    log.Fatal("Failed to start:", err)
}
```

### NewTool

Creates a tool - a passive component that provides specific capabilities. Tools are discovered and used by agents but cannot discover other components themselves.

```go
func NewTool(name string) *BaseTool
func NewToolWithConfig(config *Config) *BaseTool
```

**Tools are perfect for:**
- Microservices that perform specific tasks
- API wrappers (weather, database, external services)
- Stateless functions exposed as services
- Any capability that doesn't need orchestration

**Example - Weather Tool:**
```go
// Create a specialized tool
weatherTool := core.NewTool("weather-service")

// Define what it can do
weatherTool.RegisterCapability(core.Capability{
    Name:        "get_current_weather",
    Description: "Get current weather for any city",
    Endpoint:    "/api/weather/current",
    Handler: func(w http.ResponseWriter, r *http.Request) {
        var req struct {
            City string `json:"city"`
        }
        json.NewDecoder(r.Body).Decode(&req)

        weather := fetchWeatherData(req.City)

        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(weather)
    },
})

// Tools still need initialization and startup
weatherTool.Initialize(ctx)
weatherTool.Start(ctx, 8081) // Different port from agents
```

**Key differences between Agents and Tools:**

| Feature | Agent | Tool |
|---------|-------|------|
| Can discover services | ✅ Yes | ❌ No |
| Can be discovered | ✅ Yes | ✅ Yes |
| Has HTTP server | ✅ Yes | ✅ Yes |
| Typical use case | Orchestrator, Router | Microservice, API |
| Complexity | Higher | Lower |

### Discover

**Agent-only capability** for finding components in your system. This is how agents build a dynamic picture of available services.

```go
func (a *BaseAgent) Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)
```

**DiscoveryFilter fields:**
- `Type` - Filter by ComponentType (agent/tool)
- `Capabilities` - Required capability names
- `Name` - Exact service name match
- `Metadata` - Custom key-value filters

**Example - Smart Service Discovery:**
```go
// Find all database tools
dbTools, err := agent.Discover(ctx, core.DiscoveryFilter{
    Type: core.ComponentTypeTool,
    Capabilities: []string{"database"},
})

// Find services in specific region (using metadata)
regionalServices, err := agent.Discover(ctx, core.DiscoveryFilter{
    Metadata: map[string]interface{}{
        "region": "us-west",
        "environment": "production",
    },
})

// Find any service that can translate
translators, err := agent.Discover(ctx, core.DiscoveryFilter{
    Capabilities: []string{"translate"},
})

// Load balance across discovered services
service := translators[rand.Intn(len(translators))]
response, err := http.Post(service.Address, "application/json", payload)
```

**Pro tip:** Discovery results are cached for 5 minutes by default to reduce Redis load. Use `WithDiscoveryCacheEnabled(false)` for real-time discovery in development.

### RegisterCapability

Add capabilities to any component. This is how you define what your component can do.

```go
func (c *BaseAgent) RegisterCapability(cap Capability)
func (c *BaseTool) RegisterCapability(cap Capability)
```

**Capability structure:**
```go
// CapabilityType indicates the nature of a capability for executor behavior decisions.
type CapabilityType string

const (
    CapabilityTool         CapabilityType = "tool"         // Calls external APIs, stateless, safe to retry
    CapabilityReasoning    CapabilityType = "reasoning"    // LLM analysis only, no side effects
    CapabilityOrchestrator CapabilityType = "orchestrator"  // Nested DAG with own planning, execution, HITL
)

type Capability struct {
    Name           string           // Unique identifier
    Description    string           // Human-readable description for AI (Phase 1)
    Endpoint       string           // HTTP endpoint path
    Type           CapabilityType   // "tool", "reasoning", or "orchestrator" (default: "tool")
    InputTypes     []string         // Accepted content types
    OutputTypes    []string         // Response content types
    Handler        http.HandlerFunc // HTTP handler function

    // Phase 2: Schema discovery fields for AI-powered payload generation
    InputSummary   *SchemaSummary   // Compact field hints for AI (Phase 2)
    OutputSummary  *SchemaSummary   // Output schema hints (optional)
    SchemaEndpoint string           // Full JSON Schema endpoint (Phase 3)

    // Internal capabilities are excluded from LLM planning catalogs
    // but remain HTTP-callable. Use for orchestration endpoints,
    // admin endpoints, or deprecated capabilities.
    Internal       bool             // Exclude from LLM catalog (default: false)
}

// SchemaSummary provides compact field hints for AI payload generation (Phase 2)
type SchemaSummary struct {
    RequiredFields []FieldHint  // Required input fields
    OptionalFields []FieldHint  // Optional input fields
}

// FieldHint describes a single field for AI understanding
type FieldHint struct {
    Name        string  // Field name (exact, used in JSON)
    Type        string  // Field type (string, number, boolean, object, array)
    Example     string  // Example value for AI
    Description string  // Human-readable description
}
```

**Capability Type** controls executor behavior during step execution:

| Type | Timeout | Retry | Use When |
|------|---------|-------|----------|
| `""` or `"tool"` | `TRUVAG3_EXECUTION_STEP_TIMEOUT` (default 120s) | Yes | Standard tool/API calls |
| `"reasoning"` | `TRUVAG3_EXECUTION_STEP_TIMEOUT` (default 120s) | Yes | LLM analysis only, no side effects |
| `"orchestrator"` | `TRUVAG3_HITL_DEFAULT_TIMEOUT` + `TRUVAG3_EXECUTION_STEP_TIMEOUT` | No | Nested delegation to another orchestrator agent |

The `"orchestrator"` type exists because cross-agent delegation steps may block while the child agent waits for HITL approval. The extended timeout (`TRUVAG3_HITL_DEFAULT_TIMEOUT + TRUVAG3_EXECUTION_STEP_TIMEOUT`) is applied as a per-request context deadline for that step only — other steps in the same plan use the standard timeout. The iterative phase timeout is also auto-extended to accommodate the longer step.

Most capabilities should leave `Type` empty — the default `"tool"` behavior is correct. Set `Type: core.CapabilityOrchestrator` on capabilities that delegate to another orchestrator agent (e.g., `devops-chat-agent`'s `/query` endpoint exposed to `travel-chat-agent`).

**Example - Phase 1 (Basic - Description Only):**
```go
// Minimal capability - AI generates payloads from description alone (~85-90% accuracy)
tool.RegisterCapability(core.Capability{
    Name:        "current_weather",
    Description: "Gets current weather for a location. Required: location (city name). Optional: units (metric/imperial).",
    Handler:     handleWeather,
})
```

**Example - Phase 2 (Recommended - With Field Hints):**
```go
// Enhanced capability with field hints for better AI accuracy (~95%)
tool.RegisterCapability(core.Capability{
    Name:        "current_weather",
    Description: "Gets current weather conditions for a location",
    InputTypes:  []string{"json"},
    OutputTypes: []string{"json"},
    Handler:     handleWeather,

    // Phase 2: Add structured field hints for AI payload generation
    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {
                Name:        "location",
                Type:        "string",
                Example:     "London",
                Description: "City name or coordinates (lat,lon)",
            },
        },
        OptionalFields: []core.FieldHint{
            {
                Name:        "units",
                Type:        "string",
                Example:     "metric",
                Description: "Temperature unit: metric or imperial",
            },
        },
    },
})
```

**Example - Phase 3 (Mission-Critical - With Validation):**
```go
// Full capability with schema validation endpoint for maximum reliability (~99%)
tool.RegisterCapability(core.Capability{
    Name:        "process_payment",
    Description: "Process a payment transaction",
    Handler:     handlePayment,

    // Phase 2: Field hints for AI generation
    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {Name: "amount", Type: "number", Example: "99.99", Description: "Payment amount"},
            {Name: "currency", Type: "string", Example: "USD", Description: "Currency code"},
            {Name: "card_number", Type: "string", Example: "4111111111111111", Description: "Credit card number"},
        },
    },

    // Phase 3: Schema endpoint for validation (auto-generated at /api/capabilities/process_payment/schema)
    // Framework automatically serves JSON Schema at this endpoint
})
```

### Schema Cache (Phase 3 Validation)

Redis-backed caching for JSON Schemas used in Phase 3 validation. Schemas are fetched once and cached forever, providing zero-overhead validation after initial fetch.

#### NewSchemaCache

Create a Redis-backed schema cache for agents that perform Phase 3 validation.

```go
func NewSchemaCache(redisClient *redis.Client, opts ...SchemaCacheOption) SchemaCache
```

**SchemaCache interface:**
```go
type SchemaCache interface {
    // Get retrieves a cached schema by tool and capability name
    Get(ctx context.Context, toolName, capabilityName string) (map[string]interface{}, bool)

    // Set stores a schema in the cache
    Set(ctx context.Context, toolName, capabilityName string, schema map[string]interface{}) error

    // Stats returns cache statistics for monitoring
    Stats() map[string]interface{}
}
```

**Example - Basic Schema Cache:**
```go
// In your agent initialization
redisOpt, _ := redis.ParseURL(os.Getenv("REDIS_URL"))
redisClient := redis.NewClient(redisOpt)

// Create schema cache with defaults (24-hour TTL)
agent.SchemaCache = core.NewSchemaCache(redisClient)
```

**Example - Custom Configuration:**
```go
// Production configuration with custom TTL and prefix
agent.SchemaCache = core.NewSchemaCache(redisClient,
    core.WithTTL(1 * time.Hour),          // Shorter TTL for frequently changing schemas
    core.WithPrefix("myapp:schemas:"),    // Custom prefix for multi-tenant deployments
)

// Enable validation via environment variable
// export TRUVAG3_VALIDATE_PAYLOADS=true

// Agent will now:
// 1. Generate payloads using AI + field hints (Phase 1/2)
// 2. Fetch schema from tool's /schema endpoint (once)
// 3. Cache schema in Redis (shared across all agent replicas)
// 4. Validate all future payloads against cached schema
```

**Cache configuration options:**

```go
// WithTTL sets cache expiration time
WithTTL(ttl time.Duration) SchemaCacheOption

// WithPrefix sets Redis key prefix (for namespacing)
WithPrefix(prefix string) SchemaCacheOption
```

**Monitoring cache performance:**
```go
// Get cache statistics
stats := agent.SchemaCache.Stats()
// Returns: {"hits": 150, "misses": 3, "total_lookups": 153, "hit_rate": 0.98}

// Log statistics periodically
ticker := time.NewTicker(1 * time.Minute)
go func() {
    for range ticker.C {
        stats := agent.SchemaCache.Stats()
        logger.Info("Schema cache stats", stats)
    }
}()
```

**When to use Schema Cache:**

| Scenario | Use Schema Cache? | Reason |
|----------|------------------|--------|
| **Development** | No | Schemas change frequently, overhead not worth it |
| **Production agents** | Yes | Shared cache across replicas, validates critical payloads |
| **Mission-critical APIs** | Yes | ~99% accuracy with validation |
| **Simple tools** | No | Phase 1+2 sufficient for most cases |

**Best practices:**
- Enable only when `TRUVAG3_VALIDATE_PAYLOADS=true`
- Use shared Redis instance for all agent replicas
- Monitor cache hit rate (should be >95% after warmup)
- Set reasonable TTL (24 hours default, schemas rarely change)
- Use custom prefix for multi-tenant deployments

### Component Lifecycle Management

Every component follows a three-phase lifecycle: Initialize → Start → Stop/Shutdown. Understanding this lifecycle is crucial for building robust services.

```go
// Initialize is the only lifecycle method on the Component interface itself
// (see "Component Interface" above) — every component implements it.
//
// Start is a concrete method on BaseAgent and BaseTool, not on the interface:
func (b *BaseAgent) Start(ctx context.Context, port int) error
func (t *BaseTool)  Start(ctx context.Context, port int) error

// Graceful shutdown is asymmetric — agents use Stop, tools use Shutdown:
func (b *BaseAgent) Stop(ctx context.Context) error
func (t *BaseTool)  Shutdown(ctx context.Context) error
```

**Lifecycle best practices:**

1. **Initialize Phase** - One-time setup
   - Connect to databases
   - Load configuration
   - Set up telemetry
   - Register with discovery

2. **Start Phase** - Begin operations
   - Start HTTP server
   - Begin health checks
   - Accept incoming requests

3. **Stop Phase** - Clean shutdown
   - Stop accepting new requests
   - Wait for in-flight requests
   - Unregister from discovery
   - Close connections

**Example - Production Lifecycle:**
```go
func main() {
    ctx := context.Background()

    // Create and configure
    agent := core.NewBaseAgent("my-service")
    agent.RegisterCapability(/* ... */)

    // Initialize (connects to Redis, etc.)
    if err := agent.Initialize(ctx); err != nil {
        log.Fatal("Initialization failed:", err)
    }

    // Start in goroutine
    errChan := make(chan error, 1)
    go func() {
        if err := agent.Start(ctx, 8080); err != nil {
            errChan <- err
        }
    }()

    // Wait for shutdown signal
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    select {
    case <-sigChan:
        log.Info("Received shutdown signal")
    case err := <-errChan:
        log.Error("Server error:", err)
    }

    // Graceful shutdown with timeout
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    if err := agent.Stop(shutdownCtx); err != nil {
        log.Error("Graceful shutdown failed:", err)
        os.Exit(1)
    }

    log.Info("Shutdown complete")
}
```

### NewFramework

The main entry point for running TruvaG3 components. The framework handles all the complex setup - discovery, telemetry, configuration - so you can focus on your business logic.

```go
func NewFramework(component HTTPComponent, opts ...Option) (*Framework, error)
```

**Why use the framework:**
- Automatic configuration from environment variables
- Built-in health checks and metrics
- Graceful shutdown handling
- Service discovery registration
- Standardized logging

**Example - Simple:**
```go
// Minimal setup - just works!
agent := core.NewBaseAgent("my-agent")
framework, _ := core.NewFramework(agent)
framework.Run(ctx)
```

**Example - Production:**
```go
// Full production setup
agent := createMyAgent()

framework, err := core.NewFramework(agent,
    // Discovery
    core.WithRedisURL("redis://redis:6379"),

    // Observability
    core.WithTelemetry(true, "http://otel:4317"),
    core.WithLogLevel("info"),

    // Networking
    core.WithPort(8080),
    core.WithCORSDefaults(),

    // Features
    core.WithEnableMetrics(true),
    core.WithEnableTracing(true),
)

if err != nil {
    log.Fatal("Framework creation failed:", err)
}

// Run blocks until shutdown
if err := framework.Run(ctx); err != nil {
    log.Fatal("Framework run failed:", err)
}
```

### Configuration

TruvaG3's configuration system is designed for flexibility - use code, environment variables, or config files.

#### NewConfig

Create configuration programmatically with validation and defaults.

```go
func NewConfig(options ...Option) (*Config, error)
func DefaultConfig() *Config
```

**Configuration priority (highest to lowest):**
1. Explicit options in code
2. Environment variables
3. Config file (if specified)
4. Default values

**Example - Different Configuration Styles:**
```go
// 1. Code-based (explicit, testable)
config := core.NewConfig(
    core.WithName("weather-service"),
    core.WithPort(8080),
    core.WithRedisDiscovery("redis://localhost:6379"),
)

// 2. Environment-based (12-factor app)
// Set: TRUVAG3_AGENT_NAME=weather-service
// Set: TRUVAG3_PORT=8080
// Set: REDIS_URL=redis://localhost:6379
// Set: TRUVAG3_ORCHESTRATION_TIMEOUT=5m  // For long-running AI workflows
config := core.DefaultConfig() // Reads from env

// 3. File-based (complex configs)
config := core.NewConfig(
    core.WithConfigFile("/etc/truvag3/config.yaml"),
)
```

#### Key Configuration Options

**Service Discovery:**
```go
// Enable Redis discovery
WithRedisURL(url string)           // Redis connection
WithRedisDiscovery(url string)     // Shorthand for Redis setup
WithDiscoveryCacheEnabled(bool)    // Cache discovery results
WithMockDiscovery(bool)            // Use mock for testing

// Registration TTL and heartbeat tuning
WithDiscoveryTTL(ttl)              // Registration TTL (default: 30s, min 5s)
WithHeartbeatInterval(d)           // Heartbeat interval (default: 0 = TTL/2, min 2s)

// Background retry for Redis connection failures: opt in via the
// TRUVAG3_DISCOVERY_RETRY=true / TRUVAG3_DISCOVERY_RETRY_INTERVAL env vars
// (no Go option function exposed today).
```

**AI Integration:**
```go
WithAI(enabled bool, provider, apiKey string)  // Enable AI with provider + API key
WithOpenAIAPIKey(key string)        // Set OpenAI key
WithAIModel(model string)           // Choose model (gpt-4, claude-3, etc.)
WithMockAI(bool)                    // Use mock for testing
```

**Observability:**
```go
WithTelemetry(enabled bool, endpoint string)  // Enable telemetry
WithEnableMetrics(bool)                       // Metrics only
WithEnableTracing(bool)                       // Tracing only
WithLogLevel(level string)                    // error, warn, info, debug
WithLogFormat(format string)                  // json or text
```

**Networking:**
```go
WithPort(port int)                  // HTTP port
WithAddress(addr string)            // Bind address
WithCORS(config CORSConfig)         // CORS settings
WithCORSDefaults()                  // Permissive CORS for dev
```

**Advanced:**
```go
WithCircuitBreaker(threshold int, timeout time.Duration) // Resilience settings
WithRetry(maxAttempts int, initialInterval time.Duration) // Retry configuration
WithKubernetes(serviceDiscovery, leaderElection bool)    // K8s integration
WithDevelopmentMode(enabled bool)                        // Debug logging, mock services
```

### Logging

TruvaG3 provides structured logging with automatic context propagation. The framework automatically injects loggers into all components.

**Log Levels (hierarchical):**
- `error` - Critical errors only
- `warn` - Warnings and errors
- `info` - Normal operations (default)
- `debug` - Detailed debugging information

**Using the Logger:**
```go
// Every component gets a logger automatically
func (t *MyTool) ProcessRequest(ctx context.Context, req Request) error {
    // Structured logging with context
    t.Logger.Info("Processing request", map[string]interface{}{
        "request_id": req.ID,
        "user_id":    req.UserID,
        "action":     req.Action,
    })

    result, err := t.performAction(req)
    if err != nil {
        t.Logger.Error("Action failed", map[string]interface{}{
            "error":      err.Error(),
            "request_id": req.ID,
            "duration":   time.Since(start).Milliseconds(),
        })
        return err
    }

    t.Logger.Debug("Action completed", map[string]interface{}{
        "request_id": req.ID,
        "result":     result,
    })

    return nil
}
```

**Configuration via Environment:**
```bash
# Production
export TRUVAG3_LOG_LEVEL=info
export TRUVAG3_LOG_FORMAT=json

# Development
export TRUVAG3_LOG_LEVEL=debug
export TRUVAG3_LOG_FORMAT=text

# Or use dev mode (sets debug + text automatically)
export TRUVAG3_DEV_MODE=true
```

### Interfaces

#### Discovery Interface

Service discovery interface for agents. Combines registration with powerful query capabilities.

```go
type Discovery interface {
    Registry  // Embed Registry (Register, UpdateHealth, Unregister)

    // Query methods
    Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)
    FindService(ctx context.Context, serviceName string) ([]*ServiceInfo, error)
    FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error)
}
```

**Method usage guide:**

| Method | Use When | Example |
|--------|----------|---------|
| `Discover()` | Complex multi-criteria search | Find healthy services in region with capability |
| `FindService()` | Know exact service name | Find all "user-service" instances |
| `FindByCapability()` | Need any service with capability | Find any translator |

**Example - Building a Load Balancer:**
```go
type LoadBalancer struct {
    discovery core.Discovery
    current   uint32
}

func (lb *LoadBalancer) GetService(ctx context.Context, capability string) (*core.ServiceInfo, error) {
    // Find services advertising this capability. The Redis-backed discovery
    // implementation only returns services that are passing their heartbeat,
    // so there is no separate "healthy only" filter on DiscoveryFilter.
    services, err := lb.discovery.Discover(ctx, core.DiscoveryFilter{
        Capabilities: []string{capability},
    })

    if len(services) == 0 {
        return nil, fmt.Errorf("no services available for %s", capability)
    }

    // Round-robin selection
    index := atomic.AddUint32(&lb.current, 1) % uint32(len(services))
    return services[index], nil
}
```

#### Registry Interface

Service registration interface for tools. Handles registration, health updates, and cleanup.

```go
type Registry interface {
    Register(ctx context.Context, info *ServiceInfo) error
    UpdateHealth(ctx context.Context, id string, status HealthStatus) error
    Unregister(ctx context.Context, id string) error
}
```

**Registration lifecycle:**
```go
// 1. Register on startup
info := &core.ServiceInfo{
    ID:       "weather-1",
    Name:     "weather-service",
    Type:     "tool",
    Address:  "http://localhost:8081",
    Health:   core.HealthHealthy,
}
registry.Register(ctx, info)

// 2. Periodic health updates
ticker := time.NewTicker(30 * time.Second)
go func() {
    for range ticker.C {
        health := checkHealth()
        registry.UpdateHealth(ctx, info.ID, health)
    }
}()

// 3. Unregister on shutdown
defer registry.Unregister(ctx, info.ID)
```

#### Background Redis Retry

TruvaG3 provides an intelligent background retry mechanism for handling Redis connection failures during service startup. This is particularly useful in Kubernetes environments where Redis may not be immediately available.

**Key features:**
- **Opt-in by default** - Backward compatible, must be explicitly enabled
- **Exponential backoff** - Retry intervals double on each failure (30s → 60s → 120s → 240s → 300s cap)
- **Automatic re-registration** - When Redis becomes available, service is automatically registered
- **Thread-safe** - Registry references are updated atomically

**Configuration:**
```go
// Enable via environment variables
// export TRUVAG3_DISCOVERY_RETRY=true
// export TRUVAG3_DISCOVERY_RETRY_INTERVAL=30s
// export TRUVAG3_DISCOVERY_TTL=60s
// export TRUVAG3_DISCOVERY_HEARTBEAT=20s

// Or via code configuration (background retry stays env-var-driven —
// set TRUVAG3_DISCOVERY_RETRY=true alongside this code).
config := core.NewConfig(
    core.WithRedisURL("redis://redis:6379"),
    core.WithDiscoveryTTL(60 * time.Second),
    core.WithHeartbeatInterval(20 * time.Second),
)
```

**How it works:**
1. Service attempts initial Redis connection during startup
2. If connection fails and retry is enabled, service starts normally (without discovery)
3. Background goroutine attempts reconnection at configured intervals
4. On successful reconnection, service is registered and heartbeat begins
5. Parent component's registry reference is updated via callback

**Example - Kubernetes Deployment:**
```yaml
env:
  - name: REDIS_URL
    value: "redis://redis:6379"
  - name: TRUVAG3_DISCOVERY_RETRY
    value: "true"
  - name: TRUVAG3_DISCOVERY_RETRY_INTERVAL
    value: "30s"
  - name: TRUVAG3_DISCOVERY_TTL
    value: "60s"
  - name: TRUVAG3_DISCOVERY_HEARTBEAT
    value: "20s"
```

**When to use:**
- Kubernetes environments where Redis may start after your service
- Systems with intermittent Redis connectivity
- Services that should remain functional even without discovery

**When NOT to use:**
- Development environments (use mock discovery instead)
- When Redis availability is a hard requirement

#### Memory Interface

Abstract storage for state, sessions, and caching.

```go
type Memory interface {
    Get(ctx context.Context, key string) (string, error)
    Set(ctx context.Context, key string, value string, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
}
```

**Example - Session Management:**
```go
type SessionManager struct {
    store core.Memory
}

func (sm *SessionManager) CreateSession(userID string) (string, error) {
    sessionID := generateSessionID()
    data := map[string]string{
        "user_id": userID,
        "created": time.Now().Format(time.RFC3339),
    }

    jsonData, _ := json.Marshal(data)

    // Session expires in 24 hours
    err := sm.store.Set(ctx, "session:"+sessionID, string(jsonData), 24*time.Hour)
    return sessionID, err
}

func (sm *SessionManager) ValidateSession(sessionID string) (string, error) {
    data, err := sm.store.Get(ctx, "session:"+sessionID)
    if err != nil {
        return "", errors.New("invalid or expired session")
    }

    var session map[string]string
    json.Unmarshal([]byte(data), &session)
    return session["user_id"], nil
}
```

#### Shared Memory Interfaces

Cross-agent shared memory for domain-scoped episodic events, investigation coordination, activity compaction, and real-time agent signals. Interfaces are in `core/interfaces.go`; implementations in the `memory` module.

```go
// EpisodicMemory stores and queries structured agent events.
type EpisodicMemory interface {
    RecordEvent(ctx context.Context, event AgentEvent) error
    QueryEvents(ctx context.Context, callerDomain string, filter EventFilter) ([]AgentEvent, error)
    QueryEntityHistory(ctx context.Context, callerDomain, entityType, entityID string, since time.Time) ([]AgentEvent, error)
    QueryRecentEvents(ctx context.Context, callerDomain string, since time.Time, limit int) ([]AgentEvent, error)
    DeleteEvents(ctx context.Context, eventIDs []string) error
}

// InvestigationCoordinator prevents duplicate investigations across agents.
type InvestigationCoordinator interface {
    ClaimInvestigation(ctx context.Context, agentName, entityID string, ttl time.Duration) (claimed bool, holder string, err error)
    ReleaseInvestigation(ctx context.Context, agentName, entityID string) error
    GetActiveInvestigations(ctx context.Context) (map[string]string, error)
}

// ActivityCompactor compresses raw events into a fixed-size LLM digest.
type ActivityCompactor interface {
    CompactEvents(ctx context.Context, events []AgentEvent, maxTokens int) (string, error)
    UpdateDigest(ctx context.Context, previousDigest string, newEvents []AgentEvent, maxTokens int) (string, error)
}

// DigestCache stores/retrieves compacted domain activity digests.
type DigestCache interface {
    GetDigest(ctx context.Context, domain string) ([]byte, error)
    SetDigest(ctx context.Context, domain string, data []byte, ttl time.Duration) error
}

// ActivityCoordinator manages real-time agent coordination signals.
type ActivityCoordinator interface {
    AnnounceActivity(ctx context.Context, signal ActivitySignal) error
    UpdateStatus(ctx context.Context, requestID, status string) error
    GetDomainActivities(ctx context.Context, domain string) ([]ActivitySignal, error)
    CompleteActivity(ctx context.Context, requestID string) error
}

// StepSummary is the per-step output produced by an EventSummarizer.
// Summary is a one-sentence factual description of what the step did.
// Entities is the LLM's identification of domain entities the step operated on.
// Implementations that do not extract entities should return Entities as nil.
type StepSummary struct {
    Summary  string      `json:"summary"`
    Entities []EntityRef `json:"entities,omitempty"`
}

// EventSummarizer generates actionable summaries for execution steps.
// Returns a map of stepID -> StepSummary. Implementations must be fail-open.
type EventSummarizer interface {
    SummarizeSteps(ctx context.Context, steps []StepSummaryInput) (map[string]StepSummary, error)
}

// SharedKnowledge stores and searches reusable knowledge fragments.
type SharedKnowledge interface {
    StoreKnowledge(ctx context.Context, fragment KnowledgeFragment) error
    SearchKnowledge(ctx context.Context, callerDomain, namespace, query string, topK int, weights RetrievalWeights) ([]ScoredKnowledge, error)
}

// MemoryReflector extracts patterns from events and generates knowledge.
type MemoryReflector interface {
    Reflect(ctx context.Context, entityType, entityID string, since time.Time) ([]KnowledgeFragment, error)
}
```

**Implementations:**

| Interface | Redis/Vector (shared) | In-Memory (testing) | NoOp (disabled) |
|-----------|----------------------|-------------------|-----------------|
| `EpisodicMemory` | `memory.StreamEpisodicMemory` | `memory.InMemoryEpisodicMemory` | — |
| `InvestigationCoordinator` | `memory.AtomicLockCoordinator` | — | — |
| `SharedKnowledge` | `memory.VectorSharedKnowledge` (Qdrant) | — | — |
| `MemoryReflector` | `memory.LLMMemoryReflector` | — | — |
| `ActivityCompactor` | — | — | `core.NoOpActivityCompactor` |
| `DigestCache` | `memory.RedisDigestCache` | `memory.InMemoryDigestCache` | `core.NoOpDigestCache` |
| `ActivityCoordinator` | `memory.RedisActivityCoordinator` | `memory.InMemoryActivityCoordinator` | `core.NoOpActivityCoordinator` |
| `EventSummarizer` | — | — | `core.NoOpEventSummarizer` |

`ActivityCompactor` and `EventSummarizer` are LLM-powered — implemented in the orchestration module as `LLMActivityCompactor` and `LLMEventSummarizer`. `MemoryReflector` is also LLM-powered via `LLMMemoryReflector` in the memory module.

#### Convenience Constructors

**`memory.NewSharedBackends`** — creates all memory backends from a Redis client:

```go
backends, err := memory.NewSharedBackends(redisClient, logger,
    memory.WithAgentName("my-agent"),
    memory.WithDomain("infrastructure"),
    memory.WithEmbeddingClient(embedder),  // optional — enables Phase 2
)
defer backends.Close()
```

Options: `WithDomain(string)`, `WithAgentName(string)`, `WithKnowledgeDisabled()`, `WithEmbeddingClient(core.EmbeddingClient)`.

**`orchestration.BuildMemoryHooks`** — creates pipeline hooks from backends:

```go
hooks, activityCoord := orchestration.BuildMemoryHooks(
    backends.ToDeps(), aiClient, logger,
    orchestration.WithMemoryEntityExtractor(e),    // optional: override BOTH hooks
    orchestration.WithMemoryImportanceFunc(fn),    // optional behavioural override
)
```

Returns `([]core.PipelineHook, core.ActivityCoordinator)`. Pass both to `OrchestratorDependencies`.

**Default extractor selection:** when `aiClient != nil`, the record hook defaults to `LLMEntityExtractor` (zero-cost LLM extraction piggybacking on the existing `EventSummarizer` call). The enrichment hook always defaults to `NoOpEntityExtractor` (pre-planning, the summarizer hasn't run yet, so LLM extraction would silently return nothing). When `aiClient == nil`, both default to `NoOpEntityExtractor`.

Behavioural options:
- `WithMemoryEntityExtractor(EntityExtractor)` — overrides BOTH hooks (shorthand)
- `WithBuilderRecordEntityExtractor(EntityExtractor)` — overrides record hook only
- `WithBuilderEnrichmentEntityExtractor(EntityExtractor)` — overrides enrichment hook only
- `WithMemoryImportanceFunc(func)`, `WithMemoryRetrievalWeights(RetrievalWeights)`, `WithMemoryLookback(time.Duration)`, `WithMemoryActivityFilter(ActivityFilter)`

Numeric tuning uses `TRUVAG3_SHARED_MEMORY_*` env vars.

**Available `EntityExtractor` implementations** (in `orchestration` package):
- `NoOpEntityExtractor` — honors explicit `metadata["entity_type"]`/`metadata["entity_id"]` or multi-entity `metadata["entities"]`. Performs no extraction of its own — the framework refuses to guess. Domain-agnostic.
- `LLMEntityExtractor` — reads entities the `EventSummarizer` LLM produced as a side effect of summarization (via the `metadata["llm_entities"]` key plumbed by `MemoryRecordHook`). Zero additional LLM round-trips. Falls through to explicit metadata if no LLM entities are present.

See [Agent Memory User Guide](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md) for full usage.

#### User Memory Interfaces

Per-user private memory for personal assistant agents. Stores learned facts (preferences, identity, constraints) across sessions and injects them into the planning prompt via a `<user_profile>` enrichment tag. Separate from shared memory — user facts are never visible to other users or agents outside the user's session. Interfaces are in `core/interfaces.go`; implementations in the `memory` module.

```go
// FactSource describes how a user fact was acquired.
type FactSource string

const (
    SourceExplicit   FactSource = "explicit"   // User directly stated it
    SourceCorrection FactSource = "correction" // User corrected the agent
    SourceInferred   FactSource = "inferred"   // Extracted from conversation patterns
    SourceDerived    FactSource = "derived"     // Synthesized from multiple facts/sessions
)

// UserFact represents a learned piece of information about a specific user.
type UserFact struct {
    FactID     string            `json:"fact_id"`
    UserID     string            `json:"user_id"`
    Namespace  string            `json:"namespace"`  // Agent type scope: "travel", "devops", "universal"
    Category   string            `json:"category"`   // "preference", "identity", "constraint", "context", "summary", "relationship"
    Content    string            `json:"content"`    // Natural language: "Prefers window seats on flights over 4 hours"
    Source     FactSource        `json:"source"`
    Confidence float64           `json:"confidence"` // 0.0-1.0
    CreatedAt  time.Time         `json:"created_at"`
    UpdatedAt  time.Time         `json:"updated_at"`
    Metadata   map[string]string `json:"metadata,omitempty"`
}

// UserMemory stores and retrieves per-user private facts.
// Implementations must enforce strict user isolation at the storage level.
type UserMemory interface {
    Remember(ctx context.Context, userID string, fact UserFact) error
    Recall(ctx context.Context, userID string, namespace string, queryContext string, limit int) ([]UserFact, error)
    RecallByCategory(ctx context.Context, userID string, namespace string, category string, limit int) ([]UserFact, error)
    Forget(ctx context.Context, userID string) error  // GDPR Article 17
}

// UserMemoryAdmin extends UserMemory with administrative operations.
type UserMemoryAdmin interface {
    UserMemory
    ListFacts(ctx context.Context, userID string, namespace string, offset int, limit int) ([]UserFact, int, error)
    ForgetNamespace(ctx context.Context, userID string, namespace string) error
    ForgetFact(ctx context.Context, userID string, factID string) error
}

// UserMemoryDeps holds dependencies for BuildUserMemoryHooks.
type UserMemoryDeps struct {
    UserMemory UserMemory      // Required
    Embedder   EmbeddingClient // Required for vector-backed backends; nil for InMemoryUserMemory
    Namespace  string          // Agent type scope (e.g., "travel", "devops")
}
```

**Implementations:**

| Interface | Vector DB (production) | In-Memory (testing) | NoOp (disabled) |
|-----------|----------------------|-------------------|-----------------|
| `UserMemory` | `memory.VectorUserMemory` (Qdrant) | `memory.InMemoryUserMemory` | `core.NoOpUserMemory` |
| `UserMemoryAdmin` | `memory.VectorUserMemory` | `memory.InMemoryUserMemory` | `core.NoOpUserMemory` |

The `VectorUserMemory` backend is the reference implementation. Custom backends (pgvector, Redis Stack) implement the `UserMemory` interface — same swap pattern as `EpisodicMemory`.

**Convenience constructors:**

**`memory.NewUserMemoryBackend`** — creates a user memory backend with auto-detection:

```go
backend, err := memory.NewUserMemoryBackend(logger,
    memory.WithUserMemoryNamespace("travel"),
    memory.WithUserMemoryEmbeddingClient(embedder),
)
defer backend.Close()
```

If `TRUVAG3_VECTOR_DB_URL` is set and an embedding client is provided, creates `VectorUserMemory`. Otherwise falls back to `InMemoryUserMemory`. Options: `WithUserMemoryNamespace(string)`, `WithUserMemoryEmbeddingClient(core.EmbeddingClient)`, `WithUserMemoryVectorOption(Option)` (wraps vector config options like `WithCollectionName`).

**`orchestration.BuildUserMemoryHooks`** — creates pipeline hooks from a deps struct:

```go
hooks, closer := orchestration.BuildUserMemoryHooks(
    backend.ToDeps(), aiClient, logger,
    orchestration.WithUserFactExtractor(e),     // optional behavioural override
    orchestration.WithUserFactReconciler(r),     // optional behavioural override
)
defer closer.Close()
```

Returns `([]core.PipelineHook, io.Closer)` — append the hooks to `OrchestratorDependencies.PipelineHooks` alongside shared memory hooks, and call `closer.Close()` during shutdown to drain any in-flight async extraction work. Separate from `BuildMemoryHooks` because user memory always requires an embedding client and not all agents need user memory.

Behavioural options: `WithUserFactExtractor(UserFactExtractor)`, `WithUserFactReconciler(UserFactReconciler)`, `WithUserFactPersistencePolicy(UserFactPersistencePolicy)`, `WithUserMemoryRetrievalWeights(RetrievalWeights)`, `WithSynchronousExtraction()` (opt out of the async Layer 1 default). Numeric tuning uses `TRUVAG3_USER_MEMORY_*` env vars — see [Environment Variables Guide](ENVIRONMENT_VARIABLES_GUIDE.md).

**`orchestration.BatchUserFactReconciler`** — optional extension interface. Reconcilers that implement it can classify all candidates from a single turn in one LLM call instead of one-per-candidate, collapsing N sequential reconciliation calls into 1:

```go
type BatchUserFactReconciler interface {
    UserFactReconciler
    ReconcileBatch(ctx context.Context, userID, namespace string,
        candidates []core.UserFact, neighbors [][]core.UserFact) ([]ReconcileResult, error)
}
```

The extraction hook detects this interface via type assertion and uses the batched path when available, falling back to per-candidate `Reconcile` on error, parse failure, or length mismatch. The default `LLMUserFactReconciler` implements it. Custom reconcilers continue to work unchanged via the per-candidate path.

**Well-known enrichment key:**

```go
const EnrichmentUserProfile = "user_profile"
```

Injected by `UserMemoryEnrichmentHook` into `PipelineContext.Enrichments`. Consumed by `DefaultPromptBuilder` and the orchestrator's synthesis prompt builder to include `<user_profile>` in the LLM prompt.

### Scheduling Interfaces

#### TaskConsumer Interface

Consumer-side counterpart of `TaskDispatcher`. Used by the scheduled-executor to drain tasks from the transport.

```go
// core/async_task.go
type TaskConsumer interface {
    Consume(ctx context.Context, queueName string) (TaskHandle, error)
}
```

Returns a `TaskHandle` that the worker must settle via exactly one `Ack` or `Nack` call. Returning `(nil, nil)` is permitted on graceful shutdown.

**Implementations:** `orchestration.RedisTaskConsumer` (BRPOP, default), `orchestration.RedisStreamsTaskConsumer` (Streams, at-least-once), `orchestration.InMemoryTaskConsumer` (dev/test)

#### TaskHandle Interface

Leased reference returned by `TaskConsumer.Consume`. Follows the borrow-then-settle pattern (same as `database/sql.Rows`, `net.Conn`, AMQP basic.deliver/basic.ack).

```go
// core/async_task.go
type TaskHandle interface {
    Task() *Task                           // Accessor
    Ack(ctx context.Context) error         // Success settlement
    Nack(ctx context.Context, reason string) error  // Terminal failure + DLQ persistence
}
```

Dead-letter persistence is folded into `Nack` -- there is no separate `DeadLetterSink` interface.

#### TaskDispatcher Interface

```go
// core/async_task.go
type TaskDispatcher interface {
    Dispatch(ctx context.Context, queueName string, task *Task) error
}
```

Returns `core.ErrTaskAlreadyExists` for duplicate task IDs (idempotency contract).

#### ScheduleStore Interface

```go
// core/async_task.go
type ScheduleStore interface {
    Create(ctx context.Context, schedule *Schedule) error
    Get(ctx context.Context, id string) (*Schedule, error)
    List(ctx context.Context) ([]*Schedule, error)
    Update(ctx context.Context, schedule *Schedule) error
    Delete(ctx context.Context, id string) error
    GetDue(ctx context.Context, now time.Time) ([]*Schedule, error)
}
```

#### RegisterScheduledEndpoint

Mounts `/api/v1/scheduled` on an agent to receive scheduled tasks from the executor.

```go
// orchestration/scheduled_endpoint.go
type OrchestratorFunc func() Orchestrator

func RegisterScheduledEndpoint(agent *core.BaseAgent, orchestratorFn OrchestratorFunc, opts ...ScheduledEndpointOption) error
```

Must be called **before** `framework.Run()` — `HandleFunc` rejects registrations after the HTTP server starts. The `OrchestratorFunc` is called on each request, supporting async orchestrator initialization (returns 503 until ready).

**Options:**

| Option | Purpose |
|--------|---------|
| `WithScheduledQueryBuilder(fn)` | Override how the user-query string is extracted from the request |
| `WithScheduledMetadataBuilder(fn)` | Override what metadata is passed to the orchestrator |
| `WithScheduledFilter(fn)` | Predicate that skips requests (returns 200 with `status: filtered`) |
| `WithScheduledEndpointLogger(logger)` | Override the logger (default reads `agent.Logger` dynamically) |

**Example — Agent wiring (before framework.Run):**

```go
if err := orchestration.RegisterScheduledEndpoint(agent.BaseAgent, func() orchestration.Orchestrator {
    if o := agent.GetOrchestrator(); o != nil {
        return o
    }
    return nil
}); err != nil {
    agent.Logger.Warn("Failed to register scheduled endpoint", map[string]interface{}{"error": err.Error()})
}
```

#### ExecutorDeps (Scheduled-Executor Configuration)

Code-level configuration for the scheduled-executor worker. Fields set in code take priority over environment variables; zero-valued fields fall back to env vars, then defaults.

```go
// examples/scheduled-executor/worker.go
type ExecutorDeps struct {
    Consumer        core.TaskConsumer  // Required
    HTTPClient      *http.Client       // Required (use telemetry.NewTracedHTTPClient)
    Catalog         AgentResolver      // Required
    Logger          core.Logger        // Optional (defaults to NoOpLogger)
    QueueName       string             // Default: "scheduled-executor"
    WorkerCount     int                // Default: 5    | Env: TRUVAG3_EXECUTOR_WORKER_COUNT
    MaxRetries      int                // Default: 3    | Env: TRUVAG3_EXECUTOR_MAX_RETRIES
    RetryBaseDelay  time.Duration      // Default: 5s   | Env: TRUVAG3_EXECUTOR_RETRY_BASE_DELAY
    RetryMaxDelay   time.Duration      // Default: 60s  | Env: TRUVAG3_EXECUTOR_RETRY_MAX_DELAY
    DispatchTimeout time.Duration      // Default: 15m  | Env: TRUVAG3_EXECUTOR_DISPATCH_TIMEOUT
}
```

**Configuration priority:** code-level field > environment variable > hardcoded default.

**Example — Code-level override:**

```go
worker, _ := NewWorker(ExecutorDeps{
    Consumer:        backends.TaskConsumer,
    HTTPClient:      tracedClient,
    Catalog:         catalog,
    WorkerCount:     10,               // overrides TRUVAG3_EXECUTOR_WORKER_COUNT
    MaxRetries:      5,                // overrides TRUVAG3_EXECUTOR_MAX_RETRIES
    DispatchTimeout: 15 * time.Minute, // overrides TRUVAG3_EXECUTOR_DISPATCH_TIMEOUT (must be >= agent's TRUVAG3_ORCHESTRATION_TIMEOUT)
})
```

See [Environment Variables Guide](ENVIRONMENT_VARIABLES_GUIDE.md#scheduled-execution-configuration) for the full env var reference.

#### SchedulerBackends

Bundle of producer + consumer backends returned by factory functions.

```go
// orchestration/scheduler_backends.go
type SchedulerBackends struct {
    ScheduleStore  core.ScheduleStore   // Producer-side
    TaskDispatcher core.TaskDispatcher  // Producer-side
    TaskConsumer   core.TaskConsumer    // Consumer-side
}
```

**Factory functions:**

| Factory | Delivery | Returns |
|---------|----------|---------|
| `NewRedisSchedulerBackends(client)` | At-most-once (BRPOP) | `(*SchedulerBackends, error)` |
| `NewRedisStreamsSchedulerBackends(client)` | At-least-once (Streams) | `(*SchedulerBackends, core.Runnable, error)` |
| `NewInMemorySchedulerBackends()` | In-memory (dev/test) | `*SchedulerBackends` |

`NewRedisStreamsSchedulerBackends` returns a reaper `Runnable` that **must** be registered alongside the worker — without it, crashed replicas leak pending entries.

#### core/conformance Sub-Package

Contract test suite for `TaskConsumer` implementations.

```go
// core/conformance/task_consumer_conformance.go
func RunTaskConsumerConformance(t *testing.T, factory TaskConsumerFactory)
```

Runs 10 sub-tests (roundtrip, settlement, idempotency, cancellation, concurrency, field preservation, double-settlement). Any backend that passes all 10 is contract-compliant. See [Scheduled Tasks Guide](../orchestration/SCHEDULED_TASKS_GUIDE.md) for details.

### Upstream Error Classification

Utilities for tools that call upstream/external APIs. Extracts HTTP status codes from error messages and maps them to the correct `(HTTPStatus, ErrorCategory, Retryable)` tuple, ensuring the orchestrator routes errors correctly (e.g., upstream 400 → LLM error analyzer, not resilience retry).

#### UpstreamErrorInfo

```go
type UpstreamErrorInfo struct {
    HTTPStatus int           // Tool HTTP response status (maps to orchestrator routing)
    Category   ErrorCategory // For ToolError.Category field
    Retryable  bool          // For ToolError.Retryable field
    Code       string        // For ToolError.Code field (e.g., "API_ERROR", "RATE_LIMIT")
}
```

#### ClassifyUpstreamError

Extracts an HTTP status code from an error message using the regex `(?:status|error|code)[:\s]+(\d{3})` and classifies it for orchestrator routing.

```go
func ClassifyUpstreamError(err error) UpstreamErrorInfo
```

**Classification mapping:**

| Upstream Status | Tool HTTP | Category | Retryable | Code |
|---|---|---|---|---|
| 400, 404, 409, 422 | 400 | `CategoryInputError` | false | `API_ERROR` |
| 401 | 401 | `CategoryAuthError` | false | `AUTH_ERROR` |
| 403 | 403 | `CategoryAuthError` | false | `AUTH_ERROR` |
| 429 | 429 | `CategoryRateLimit` | true | `RATE_LIMIT` |
| 500+ | 502 | `CategoryServiceError` | true | `SERVICE_ERROR` |
| No match / nil | 502 | `CategoryServiceError` | true | `SERVICE_ERROR` |

**Example — Tool handler with upstream API:**
```go
func (h *HotelHandler) handleSearch(rw http.ResponseWriter, r *http.Request) {
    result, err := h.amadeusClient.SearchHotels(ctx, params)
    if err != nil {
        info := core.ClassifyUpstreamError(err)
        h.sendUpstreamError(rw, "Hotel search failed: "+err.Error(), info)
        return
    }
    h.sendSuccess(rw, result)
}
```

### BackoffConfig

Pure calculation utility for exponential backoff with deterministic jitter. Used by the orchestration module (step retry) and resilience module (retry executor). Unlike `RetryConfig` (a serializable config struct), `BackoffConfig` is a runtime calculation type.

```go
type BackoffConfig struct {
    InitialDelay  time.Duration // Base delay for the first attempt (e.g., 500ms)
    MaxDelay      time.Duration // Upper bound — delay never exceeds this (e.g., 10s)
    BackoffFactor float64       // Multiplier per attempt (e.g., 2.0 for doubling)
    JitterEnabled bool          // Add deterministic ±10% jitter (math.Sin-based)
}

func DefaultBackoffConfig() BackoffConfig  // 500ms initial, 10s max, 2x factor, jitter enabled
func (c BackoffConfig) Delay(attempt int) time.Duration  // Calculate delay for attempt (1-indexed)
```

**Backoff progression (defaults):**

| Attempt | Base Delay | With Jitter (±10%) |
|---|---|---|
| 1 | 500ms | 450–550ms |
| 2 | 1s | 900ms–1.1s |
| 3 | 2s | 1.8–2.2s |
| 4 | 4s | 3.6–4.4s |
| 5+ | capped at 10s | capped at 10s |

### Request Context Propagation

Functions for propagating orchestration context (request IDs, step IDs) through the call chain. Used by agents to correlate LLM debug recordings back to the originating orchestration request.

#### Context Helpers

```go
// Store and retrieve request ID in context
func WithRequestID(ctx context.Context, id string) context.Context
func GetRequestID(ctx context.Context) string

// Store and retrieve step ID in context
func WithStepID(ctx context.Context, id string) context.Context
func GetStepID(ctx context.Context) string
```

#### ExtractRequestContext

Extract orchestration context from HTTP request headers. Call this in agent/tool HTTP handlers to enable LLM debug recording correlation.

```go
func ExtractRequestContext(ctx context.Context, r *http.Request) context.Context
```

**Headers extracted:**

| Header | Context Key | Purpose |
|--------|-------------|---------|
| `X-TruvaG3-Request-ID` | `truvag3_request_id` | Correlates agent LLM calls to orchestration request |
| `X-TruvaG3-Step-ID` | `truvag3_step_id` | Identifies which plan step triggered the call |

**Example — Agent HTTP Handler:**
```go
func handleCapabilityRequest(w http.ResponseWriter, r *http.Request) {
    // Extract orchestration context from headers set by executor
    ctx := core.ExtractRequestContext(r.Context(), r)

    // Now any LLM call made with this ctx will be recorded
    // under the orchestration request's debug trace
    response, err := aiClient.GenerateResponse(ctx, prompt, options)
}
```

### Memory Implementations

#### NewInMemoryStore

Fast, local memory storage with automatic expiration. Perfect for development and single-instance deployments.

```go
func NewInMemoryStore() *InMemoryStore
```

**Features:**
- **Automatic TTL expiration** - Items expire based on TTL
- **Background cleanup** - Removes expired items every 10 minutes
- **Thread-safe** - Concurrent access with RWMutex
- **Capacity limited** - Max 1000 items (prevents memory leaks)

**When to use:**
- Development and testing
- Single-instance deployments
- Temporary caching
- Session storage for small apps

**Example - Rate Limiting:**
```go
func RateLimitMiddleware(store core.Memory, limit int) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            clientIP := r.RemoteAddr
            key := "rate:" + clientIP

            // Check current count
            val, err := store.Get(ctx, key)
            if err != nil {
                // First request - allow and start counting
                store.Set(ctx, key, "1", 1*time.Minute)
                next.ServeHTTP(w, r)
                return
            }

            count, _ := strconv.Atoi(val)
            if count >= limit {
                http.Error(w, "Rate limit exceeded", 429)
                return
            }

            // Increment counter
            store.Set(ctx, key, strconv.Itoa(count+1), 1*time.Minute)
            next.ServeHTTP(w, r)
        })
    }
}
```

---

## AI Module

Connect to AI providers and build intelligent agents that leverage LLMs for natural language understanding and generation.

### Request-Aware AI API

The request-aware API extends the legacy `AIClient` contract without breaking
existing clients. Its provider-neutral types and dispatch helpers live in
`core`; provider construction and integration options live in `ai`.

#### Construction and dispatch

```go
func NewRequestClient(options ...ClientOption) (core.AIRequestClient, error)

func GenerateAI(
    ctx context.Context,
    client core.AIClient,
    request *core.AIRequest,
) (*core.AIResult, error)

func StreamAI(
    ctx context.Context,
    client core.AIClient,
    request *core.AIRequest,
    callback core.StreamCallback,
) (*core.AIResult, error)
```

Existing `AIOption` values satisfy `ClientOption`, so legacy configuration can
be mixed with the advanced options. `GenerateAI` and `StreamAI` prefer the
request-aware capability and fall back to legacy clients only when the request
is losslessly representable. An unsupported feature returns
`*core.AIRequestFeatureError` and matches
`core.ErrAIRequestFeatureUnsupported`.

Built-in request-aware factories currently include Anthropic and OpenAI, plus
Bedrock with the `bedrock` build tag. Gemini remains legacy-only.

#### AIRequest and presence-aware generation

```go
type AIRequest struct {
    Prompt     string
    Purpose    string
    Generation AIGenerationOptions
    Patches    []AIProviderPatch
}

type AIGenerationOptions struct {
    Model           string
    Temperature     AIParameter[float32]
    TopP            AIParameter[float32]
    TopK            AIParameter[int]
    MaxTokens       AIParameter[int]
    SystemPrompt    AIParameter[string]
    ReasoningEffort AIParameter[string]
    ResponseFormat  AIParameter[string]
}

func NewAIRequest(prompt, purpose string) *AIRequest
func NewAIRequestFromLegacy(prompt, purpose string, options *AIOptions) *AIRequest
func CloneAIRequest(request *AIRequest) (*AIRequest, error)

func InheritAIParameter[T any]() AIParameter[T]
func SetAIParameter[T any](value T) AIParameter[T]
func OmitAIParameter[T any]() AIParameter[T]
```

The zero value of `AIParameter` is `Inherit`. `Set` explicitly supplies a value,
including its zero value. `Omit` requires the provider field to be absent.
`Model` is structural: an empty string inherits and the model cannot be
explicitly omitted.

`Purpose` must be a stable, non-secret operation label because it may appear in
policy selectors, sanitized reports, and traces.

#### AIResult and request reports

```go
type AIResult struct {
    Response      *AIResponse
    RequestReport *AIRequestReport
    UsageDetails  *AIUsageDetails
}

type AIRequestReport struct {
    Provider       string
    ProviderAlias  string
    Surface        string
    Operation      string
    Purpose        string
    RequestedModel string
    ResolvedModel  string
    Adjustments    []AIRequestAdjustment
    Fingerprint    string
    Stable         bool
}

type AIUsageDetails struct {
    CachedInputTokens int64
    ReasoningTokens   int64
    AudioInputTokens  int64
    AudioOutputTokens int64
    Counters          map[string]int64
}
```

Reports contain only sanitized preparation facts. They must not contain prompt
text, credentials, raw bodies, complete secret-bearing endpoints, or secret
field values. `AIRequestFingerprinter` optionally exposes the same stable,
secret-free semantic identity before execution; AI-output caches must bypass
reads and writes when it reports `stable=false`.

#### Provider patches

```go
type AIProviderPatch struct {
    Name          string
    Version       string
    Selector      AIProviderSelector
    Set           map[string]interface{}
    Remove        []string
    SetHeaders    map[string]string
    RemoveHeaders []string
}

type AIProviderSelector struct {
    Provider      string
    ProviderAlias string
    Surface       string
    Model         string
    Operation     string
    Purpose       string
    AllProviders  bool
}
```

Body paths use RFC 6901 JSON Pointer syntax. `Set` values must be JSON-native:
string-keyed maps, slices, arrays, finite scalars, or `nil`. Pointers, structs,
non-finite floats, non-string map keys, and cyclic values are rejected.

Request-aware construction accepts:

```go
func WithRequestRules(rules ...core.AIProviderPatch) ClientOption
func WithRequestMiddleware(m ...requestpolicy.RequestMiddleware) ClientOption
func WithCompatibilityMode(mode requestpolicy.CompatibilityMode) ClientOption
```

The order is provider built-ins, application rules, middleware, per-request
patches, then compatibility and provider-draft validation. Compatible mode
reports built-in adjustments. Strict mode rejects a built-in adjustment to
explicit intent unless application policy acknowledges the affected path.

Middleware must be safe for concurrent use and must not retain its call-local
editor. It is fingerprint-unstable unless it implements
`requestpolicy.StableRequestMiddleware` and explicitly declares deterministic,
versioned behavior.

#### Enterprise integration options

```go
func WithCredentialSource(source CredentialSource) ClientOption
func WithEndpointResolver(resolver EndpointResolver) ClientOption
func WithAuthHeader(name string, value AuthHeaderFunc) ClientOption
func WithHTTPClient(client *http.Client) ClientOption
```

`CredentialSource` is called once per transport attempt after policy and route
resolution. Implementations must be concurrency-safe; credential values are
never included in reports, fingerprints, or logs. A source may implement
`CredentialRejectionObserver` to observe HTTP 401/403 responses without
replacing the original provider error.

`EndpointResolver` returns a complete URL and a stable, non-secret
`RouteIdentity`. It may run during cache fingerprint preflight and again on a
cache miss, so it must be concurrency-safe, stable, and side-effect-free.
`WithHTTPClient` is supported by the request-aware HTTP providers, which
shallow-copy and do not mutate the supplied client. SDK-native Bedrock rejects
HTTP-only integration options.

#### Heterogeneous chains

```go
func NewChain(entries ...ChainEntry) (*ChainClient, error)
func ProviderEntry(name, providerAlias string, options ...ClientOption) ChainEntry
func ClientEntry(name string, client core.AIClient) ChainEntry
```

Entry names must be unique, stable, and non-secret. `ProviderEntry` constructs
an independently configured request-aware provider. `ClientEntry` invokes a
caller-owned client without mutating it through optional setters. Streaming
failover is allowed only before the first chunk is delivered.

`NewChainClient` remains the legacy homogeneous-chain constructor. Use
`NewChain` when entries need different policy, routing, credentials, or client
implementations.

#### Provider factory extension contracts

```go
type ProviderFactory interface {
    Create(*AIConfig) core.AIClient
    DetectEnvironment() (priority int, available bool)
    Name() string
    Description() string
}

type ValidatedProviderFactory interface {
    ProviderFactory
    CreateValidated(*AIConfig) (core.AIClient, error)
}

type RequestProviderFactory interface {
    ProviderFactory
    CreateRequestClient(*AIConfig, ProviderIntegrationConfig) (core.AIRequestClient, error)
}
```

`NewClient` prefers `CreateValidated`. `NewRequestClient` uses
`CreateRequestClient` and passes an isolated integration snapshot. The reusable
`ai/providerkit/openaiwire` package supplies the OpenAI chat-completions draft,
encode, decode, and stream-decode contract without owning credentials, routing,
retries, provider identity, or telemetry.

See the [Custom AI Providers and Enterprise Integration Guide](../building/CUSTOM_AI_PROVIDER_GUIDE.md)
for implementation patterns and security requirements.

### NewClient

Create an AI client with automatic provider detection. The client intelligently selects the best available AI provider based on environment variables.

```go
func NewClient(opts ...AIOption) (core.AIClient, error)
func MustNewClient(opts ...AIOption) core.AIClient  // Panics on error
```

**Provider auto-detection order (by priority):**
1. OpenAI (`OPENAI_API_KEY`) - Priority 1000
2. Anthropic (`ANTHROPIC_API_KEY`) - Priority 900
3. Google Gemini (`GEMINI_API_KEY` or `GOOGLE_API_KEY`) - Priority 800
4. Groq (`GROQ_API_KEY`) - Priority 700
5. DeepSeek (`DEEPSEEK_API_KEY`) - Priority 600
6. xAI Grok (`XAI_API_KEY`) - Priority 500
7. Mistral (`MISTRAL_API_KEY`) - Priority 450
8. Qwen (`QWEN_API_KEY`) - Priority 400
9. Together AI (`TOGETHER_API_KEY`) - Priority 300
10. AWS Bedrock (AWS credentials) - Priority 200
11. Ollama (`OLLAMA_BASE_URL` must be set) - Priority 100

**Example - Auto-detect Provider:**
```go
// Automatically uses first available provider
client, err := ai.NewClient()
if err != nil {
    log.Fatal("No AI provider available. Set OPENAI_API_KEY or ANTHROPIC_API_KEY")
}

response, _ := client.GenerateResponse(ctx, "Explain quantum computing", nil)
fmt.Println(response.Content)
```

**Example - Specific Provider:**
```go
// Use specific provider with custom settings
client, err := ai.NewClient(
    ai.WithProvider("anthropic"),
    ai.WithModel("claude-3-opus-20240229"),
    ai.WithTemperature(0.7),
    ai.WithMaxTokens(2000),
)

// Use for complex reasoning tasks
response, _ := client.GenerateResponse(ctx,
    "Analyze this code for potential issues and suggest improvements",
    &core.AIOptions{
        Temperature: 0.3,  // Lower temperature for technical analysis
    },
)
```

### WithProviderAlias

Configure OpenAI-compatible providers with automatic endpoint and model resolution. Provider aliases offer a clean way to use alternative AI providers that implement the OpenAI API specification.

```go
func WithProviderAlias(alias string) AIOption
```

**Supported provider aliases:**
- `"openai"` - Standard OpenAI (default)
- `"openai.deepseek"` - DeepSeek with reasoning models
- `"openai.groq"` - Groq for ultra-fast inference
- `"openai.together"` - Together AI for open models
- `"openai.xai"` - xAI Grok models
- `"openai.qwen"` - Alibaba Qwen models

**Features:**
- **Automatic endpoint configuration** - No need to specify base URLs
- **Model aliases** - Use portable names like "smart", "fast", "code"
- **Environment variable support** - Override endpoints via environment
- **Three-tier configuration** - Explicit → Environment → Defaults

**Example - Using Alternative Providers:**
```go
// Use DeepSeek's reasoning model
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai.deepseek"),
    ai.WithModel("smart"),  // Resolves to "deepseek-reasoner"
)

// Use Groq for fast inference
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai.groq"),
    ai.WithModel("fast"),   // Resolves to "llama-3.1-8b-instant"
)

// Use Together AI with explicit model
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai.together"),
    ai.WithModel("meta-llama/Llama-3-70b-chat-hf"), // Explicit model name
)
```

**Configuration priority:**
```go
// 1. Explicit configuration (highest priority)
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai.groq"),
    ai.WithAPIKey("explicit-key"),        // Overrides environment
    ai.WithBaseURL("https://custom.url"), // Overrides defaults
)

// 2. Environment variables (medium priority)
// Set: GROQ_API_KEY=your-key
// Set: GROQ_BASE_URL=https://custom.groq.com  // Optional override
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai.groq"),
)

// 3. Hardcoded defaults (lowest priority)
// Uses built-in endpoints like https://api.groq.com/openai/v1
```

### Model Aliases

Use portable model names across different providers. Model aliases allow you to write provider-agnostic code.

**Standard aliases:**
- `"smart"` - Most capable model for complex tasks
- `"fast"` - Quick responses for simple queries
- `"code"` - Optimized for code generation
- `"vision"` - Multimodal/vision capabilities (if available)
- `"default"` - Provider's recommended default

**Model alias resolution examples** (snapshot — see [ai/providers/openai/models.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/models.go) for the canonical mapping):

```go
// "smart" resolves differently per provider
ai.WithProviderAlias("openai")          // → o3
ai.WithProviderAlias("openai.deepseek") // → deepseek-reasoner
ai.WithProviderAlias("openai.groq")     // → openai/gpt-oss-120b

// "fast" for quick responses
ai.WithProviderAlias("openai")          // → gpt-4.1-mini
ai.WithProviderAlias("openai.deepseek") // → deepseek-chat
ai.WithProviderAlias("openai.groq")     // → llama-3.1-8b-instant

// Direct model names still work (pass-through)
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai.deepseek"),
    ai.WithModel("deepseek-chat"), // Exact model, not an alias
)
```

### NewChainClient

Create a chain client that automatically fails over between multiple AI providers. Perfect for production systems requiring high availability and resilience.

```go
func NewChainClient(opts ...ChainOption) (*ChainClient, error)
```

**Features:**
- **Automatic failover** - Seamlessly switches to backup providers
- **Auto-detection** - Discovers available providers from API keys when no explicit chain is specified
- **Graceful degradation** - Works with single provider if needed
- **Smart error handling** - Only fails over on infrastructure errors
- **Configurable chain** - Define your provider priority order

**Example - Auto-Detect (recommended for development):**
```go
// Auto-detects available providers from environment API keys,
// ordered by priority: OpenAI(1000) > Anthropic(900) > Gemini(800) > Groq(700) > ...
chain, err := ai.NewChainClient(
    ai.WithChainLogger(logger),
    ai.WithChainTimeout(120*time.Second),
)

// Automatically tries providers in priority order until one succeeds
response, err := chain.GenerateResponse(ctx, "Explain quantum computing", nil)
```

**Example - Explicit Chain (recommended for production):**
```go
// Define exact failover order — providers are tried left to right
chain, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic", "openai.groq"),
    ai.WithChainTimeout(120*time.Second),
)

// Tries: OpenAI → Anthropic → Groq
response, err := chain.GenerateResponse(ctx, "Explain quantum computing", nil)
```

**Example - Using Provider Aliases:**
```go
// Chain with OpenAI-compatible providers using dot-notation aliases
chain, _ := ai.NewChainClient(
    ai.WithProviderChain("openai.deepseek", "openai.groq", "openai"),
)
```

### WithProviderChain

Configure the provider chain for automatic failover. Providers are tried in the order specified.

```go
func WithProviderChain(aliases ...string) ChainOption
```

- Each alias is a provider name (`"openai"`, `"anthropic"`) or a dot-notation sub-provider alias (`"openai.groq"`, `"openai.deepseek"`)
- When omitted, `NewChainClient()` auto-detects available providers from environment API keys and orders them by priority

**Example - Production Failover Strategy:**
```go
chain, _ := ai.NewChainClient(
    ai.WithProviderChain(
        "openai",           // Primary: Best quality
        "openai.deepseek",  // Backup 1: Good reasoning, lower cost
        "anthropic",        // Backup 2: Alternative provider
        "openai.groq",      // Emergency: Fast inference
    ),
    ai.WithChainTimeout(120*time.Second),
)
```

### Client Configuration Options

Configure AI client behavior with these options. All options work with both `NewClient` and `NewChainClient`.

#### WithTimeout

Set the HTTP timeout for AI API requests. Default is 180 seconds (3 minutes), which accommodates reasoning models that require longer processing time.

```go
func WithTimeout(timeout time.Duration) AIOption
func WithChainTimeout(timeout time.Duration) ChainOption  // For chain clients
```

**Example:**
```go
import "time"

// Single client with extended timeout for complex tasks
client, _ := ai.NewClient(
    ai.WithTimeout(300 * time.Second),  // 5 minutes
)

// Chain client with custom timeout
chainClient, _ := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic"),
    ai.WithChainTimeout(240 * time.Second),  // 4 minutes
)
```

#### WithReasoningTokenMultiplier

Configure the token multiplier for OpenAI reasoning models (GPT-5, o1, o3, o4). These models count internal chain-of-thought tokens against `max_completion_tokens` but don't return them, which can cause empty responses without adequate token allocation.

```go
func WithReasoningTokenMultiplier(multiplier int) AIOption
func WithChainReasoningTokenMultiplier(multiplier int) ChainOption  // For chain clients
```

**Default:** 5x multiplier (e.g., 2000 requested tokens → 10000 allocated)

**Example:**
```go
// Lower multiplier for cost optimization (simpler prompts)
client, _ := ai.NewClient(
    ai.WithReasoningTokenMultiplier(3),  // 3x multiplier
)

// Higher multiplier for complex reasoning tasks
client, _ := ai.NewClient(
    ai.WithReasoningTokenMultiplier(8),  // 8x multiplier
)

// Chain client with custom multiplier
chainClient, _ := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic"),
    ai.WithChainReasoningTokenMultiplier(6),
)
```

> **Note:** The multiplier only affects OpenAI reasoning models. Standard models (GPT-4, Claude, etc.) are unaffected.

#### Other Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `WithProvider(name)` | `string` | auto-detect | AI provider name |
| `WithProviderAlias(alias)` | `string` | - | Provider alias (e.g., "openai.groq") |
| `WithModel(model)` | `string` | provider default | Model name or alias |
| `WithAPIKey(key)` | `string` | from env | Override API key |
| `WithBaseURL(url)` | `string` | provider default | Custom API endpoint |
| `WithTemperature(t)` | `float32` | 0.7 | Sampling temperature (0.0-2.0) |
| `WithMaxTokens(n)` | `int` | 1000 | Default max tokens (per `ai/providers/base.go`) |
| `WithTimeout(d)` | `time.Duration` | 180s | HTTP request timeout |
| `WithReasoningTokenMultiplier(n)` | `int` | 5 | Token multiplier for reasoning models |
| `WithLogger(l)` | `core.Logger` | nil | Logger for AI operations |
| `WithTelemetry(t)` | `core.Telemetry` | nil | Telemetry for distributed tracing |

### GenerateResponse

Generate AI responses with optional parameters for fine-tuning behavior.

```go
// On the core.AIClient interface:
GenerateResponse(ctx context.Context, prompt string, options *AIOptions) (*AIResponse, error)

// Same shape on the concrete *ChainClient (failover wrapper):
func (c *ChainClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error)
```

### StreamResponse

Generate AI responses with real-time streaming. Tokens are delivered as they're generated by the AI provider, enabling lower time-to-first-token and real-time UX.

```go
// Streaming lives on the core.StreamingAIClient interface (which embeds AIClient).
// All built-in providers implement it; type-assert with `client.(core.StreamingAIClient)`
// when you need to call StreamResponse on a value typed as core.AIClient.
StreamResponse(ctx context.Context, prompt string, options *AIOptions, callback StreamCallback) (*AIResponse, error)

// Same shape on the concrete *ChainClient:
func (c *ChainClient) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error)
```

**StreamCallback type:**
```go
// StreamCallback is called for each chunk of the streaming response
type StreamCallback func(chunk StreamChunk) error

// StreamChunk represents a single chunk of streaming output
type StreamChunk struct {
    Content      string                 // The text content of this chunk
    Delta        bool                   // True for incremental delta chunks; false on the final chunk
    Index        int                    // Zero-based chunk index within the stream
    FinishReason string                 // Why generation stopped (set on the final chunk: "stop", "length", ...)
    Model        string                 // Model identifier
    Usage        *core.TokenUsage       // Token usage (set on the final chunk)
    Metadata     map[string]interface{} // Provider-specific metadata
}
```

Streaming errors are surfaced via the `error` returned from `StreamResponse`,
not through a field on `StreamChunk`. The end of the stream is signalled by a
final chunk with `Delta: false` and a populated `FinishReason` (and `Usage`).

**Example - Basic Streaming:**
```go
client, _ := ai.NewClient()

// NewClient returns core.AIClient; type-assert to StreamingAIClient for streaming.
streaming, ok := client.(core.StreamingAIClient)
if !ok {
    log.Fatal("provider does not support streaming")
}

resp, err := streaming.StreamResponse(ctx, "Explain quantum computing", nil,
    func(chunk core.StreamChunk) error {
        // Print each token as it arrives
        fmt.Print(chunk.Content)

        // Final chunk has Delta=false and a populated FinishReason
        if !chunk.Delta && chunk.FinishReason != "" {
            fmt.Println("\n--- Complete ---")
            if chunk.Usage != nil {
                fmt.Printf("Tokens: %d\n", chunk.Usage.TotalTokens)
            }
        }

        return nil
    },
)
if err != nil {
    log.Printf("stream failed: %v", err)
}
_ = resp // resp.Content holds the accumulated text; resp.Usage holds totals.
```

**Example - Streaming with Chain Client (Failover):**
```go
chain, _ := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic", "openai.groq"),
)

// ChainClient exposes StreamResponse directly — no type assertion needed.
_, err := chain.StreamResponse(ctx, prompt, nil, func(chunk core.StreamChunk) error {
    sendToUI(chunk.Content)
    return nil
})
// If OpenAI fails, automatically tries Anthropic, then Groq
```

**Example - Canceling a Stream:**
```go
ctx, cancel := context.WithCancel(context.Background())

go func() {
    time.Sleep(5 * time.Second)
    cancel() // Stop streaming after 5 seconds
}()

_, err := streaming.StreamResponse(ctx, prompt, nil, callback)
if errors.Is(err, context.Canceled) {
    fmt.Println("Stream was canceled")
}
```

**Provider streaming support:**
| Provider | Streaming | Notes |
|----------|-----------|-------|
| OpenAI | ✅ Full | Native streaming |
| Anthropic | ✅ Full | Native streaming |
| Gemini | ✅ Full | Native streaming |
| Bedrock | ✅ Full | Native streaming |
| Groq | ✅ Full | OpenAI-compatible |
| DeepSeek | ✅ Full | OpenAI-compatible |
| xAI | ✅ Full | OpenAI-compatible |
| Mistral | ✅ Full | OpenAI-compatible |
| Qwen | ✅ Full | OpenAI-compatible |
| Together | ✅ Full | OpenAI-compatible |
| Ollama | ✅ Full | OpenAI-compatible |

For a complete production example with SSE streaming, session management, and conversation history, see the [Chat Agent Implementation Guide](../memory-and-chat/CHAT_AGENT_GUIDE.md).

**AIOptions parameters:**
- `Temperature` (0.0-1.0) - Creativity level (0=deterministic, 1=creative)
- `MaxTokens` - Maximum response length
- `TopP` - Nucleus sampling (alternative to temperature)
- `Model` - Override default model

**Example - Different Use Cases:**
```go
// Technical analysis (low temperature for accuracy)
codeReview, _ := client.GenerateResponse(ctx,
    "Review this Go code for bugs",
    &core.AIOptions{
        Temperature: 0.2,
        MaxTokens:   1500,
    },
)

// Creative writing (high temperature for variety)
story, _ := client.GenerateResponse(ctx,
    "Write a short story about AI",
    &core.AIOptions{
        Temperature: 0.9,
        MaxTokens:   2000,
    },
)

// Structured data extraction (deterministic)
data, _ := client.GenerateResponse(ctx,
    "Extract JSON data from this text",
    &core.AIOptions{
        Temperature: 0,  // Deterministic
        MaxTokens:   500,
    },
)
```

### NewAIAgent

Create an intelligent agent with both AI capabilities and service discovery powers. Perfect for building orchestrators and assistants.

```go
func NewAIAgent(name string, apiKey string) (*AIAgent, error)
```

**AIAgent capabilities:**
- Full agent powers (discovery, orchestration)
- Built-in AI for natural language processing
- Conversation memory management
- Tool use and function calling
- Autonomous decision making

**Example - Intelligent Orchestrator:**
```go
// Create an AI-powered orchestrator
agent, err := ai.NewAIAgent("ai-orchestrator", os.Getenv("OPENAI_API_KEY"))
if err != nil {
    log.Fatal(err)
}

// It can process natural language
response, _ := agent.GenerateResponse(ctx,
    "Find the weather service and get weather for NYC",
    nil,
)

// It remembers conversations
agent.ProcessWithMemory(ctx, "My name is John")
agent.ProcessWithMemory(ctx, "What's my name?") // "Your name is John"

// It can discover and use other services
tools, _ := agent.Discover(ctx, core.DiscoveryFilter{
    Capabilities: []string{"weather", "news"},
})

// Orchestrate multiple tools based on user request
result := agent.ProcessRequest(ctx,
    "Get weather and news for NYC and summarize both",
    tools,
)
```

### NewAITool

Create an AI-powered tool that exposes AI capabilities as a service. Unlike AIAgent, tools are passive and cannot discover other services.

```go
func NewAITool(name string, apiKey string, opts ...AIToolOption) (*AITool, error)

// Options
func WithAIToolLogger(logger core.Logger) AIToolOption

// Capabilities are registered after construction. Each call mounts an HTTP
// endpoint at /ai/<name> that runs `prompt` against the tool's AI client and
// returns the response body.
func (t *AITool) RegisterAICapability(name, description, prompt string)
```

**When to use AITool vs AIAgent:**

| Feature | AITool | AIAgent |
|---------|--------|---------|
| **Purpose** | Provide AI service | Orchestrate services |
| **Discovery** | Can be discovered | Can discover & be discovered |
| **Memory** | Stateless | Stateful conversations |
| **Use case** | Translation, summarization | Orchestrators, assistants |

**Example - AI Microservices:**
```go
// Create a specialized AI tool, then attach one or more AI-backed capabilities
translator, _ := ai.NewAITool("translator-service", apiKey)
translator.RegisterAICapability(
    "translate",
    "Translate text between languages",
    "You are a translation assistant. Translate the user's input faithfully.",
)

summarizer, _ := ai.NewAITool("summarizer-service", apiKey)
summarizer.RegisterAICapability(
    "summarize",
    "Summarize long-form text",
    "Summarize the user's input in 3 sentences or fewer.",
)

// Each runs as an independent microservice
go translator.Start(ctx, 8081)
go summarizer.Start(ctx, 8082)

// Agents can discover and use these tools
// POST http://localhost:8081/ai/translate
// (request body is forwarded to the LLM as input)
```

### AI Provider Support

TruvaG3 supports the same set of providers documented in the auto-detection
order above, all behind the unified `core.AIClient` API. Pick a model with the
portable `"smart"`/`"fast"`/`"code"`/`"vision"` aliases or a provider-specific
identifier; concrete model lists evolve faster than this doc, so use
`ai/providers/openai/models.go` for the current alias resolutions.

| Provider | Alias | Best For |
|----------|-------|----------|
| **OpenAI** | `openai` (native) | General purpose, code generation |
| **Anthropic** | `anthropic` (native) | Complex reasoning, analysis |
| **Google Gemini** | `gemini` (native) | Multimodal, long context |
| **AWS Bedrock** | `bedrock` (native) | Enterprise/regulated workloads |
| **Groq** | `openai.groq` | Fastest inference (Llama family) |
| **DeepSeek** | `openai.deepseek` | Reasoning + chat at low cost |
| **xAI Grok** | `openai.xai` | Grok 3/4 family |
| **Mistral** | `openai.mistral` | European-hosted Mistral models |
| **Qwen** | `openai.qwen` | Alibaba Qwen family |
| **Together AI** | `openai.together` | Open-weights models with Turbo |
| **Ollama** | `openai.ollama` | Local/self-hosted models |

**Example - Multi-Provider Strategy:**
```go
// Use different providers for different tasks
type AIService struct {
    reasoning  core.AIClient  // Claude for complex reasoning
    creative   core.AIClient  // GPT-4 for creative tasks
    fast       core.AIClient  // Groq for quick responses
}

func NewAIService() *AIService {
    return &AIService{
        reasoning: ai.MustNewClient(
            ai.WithProvider("anthropic"),
            ai.WithModel("claude-3-opus-20240229"),
        ),
        creative: ai.MustNewClient(
            ai.WithProvider("openai"),
            ai.WithModel("gpt-4"),
        ),
        fast: ai.MustNewClient(
            ai.WithProviderAlias("openai.groq"),
            ai.WithModel("fast"), // resolves to llama-3.1-8b-instant; see ai/providers/openai/models.go
        ),
    }
}

func (s *AIService) AnalyzeCode(ctx context.Context, code string) (string, error) {
    // Use Claude for code analysis
    resp, err := s.reasoning.GenerateResponse(ctx,
        fmt.Sprintf("Analyze this code:\n%s", code),
        &core.AIOptions{Temperature: 0.3},
    )
    if err != nil {
        return "", err
    }
    return resp.Content, nil
}

func (s *AIService) GenerateDocumentation(ctx context.Context, code string) (string, error) {
    // Use GPT-4 for documentation
    resp, err := s.creative.GenerateResponse(ctx,
        fmt.Sprintf("Generate comprehensive docs for:\n%s", code),
        &core.AIOptions{Temperature: 0.7},
    )
    if err != nil {
        return "", err
    }
    return resp.Content, nil
}
```

### InstrumentedAIClient

Decorator that wraps any `core.AIClient`, emits the common logical
`ai.generate` / `ai.stream` spans, and optionally records every LLM call to a
`telemetry.LLMCallRecorder` for debugging. `NewClient` and `NewRequestClient`
always install this wrapper so logical telemetry is provider-independent.
Consequently, code must depend on capability interfaces rather than asserting a
constructor result to a concrete provider-client type.

```go
func NewInstrumentedClient(
    client core.AIClient,
    recorder telemetry.LLMCallRecorder,
    opts ...InstrumentedOption,
) *InstrumentedAIClient
```

**Options:**

| Option | Description |
|--------|-------------|
| `WithComponentName(name)` | Source component name for recordings (e.g., `"research-assistant"`) |
| `WithDefaultCallType(type)` | Override default call type (default: `"agent_llm_call"`) |
| `WithInstrumentedLogger(logger)` | Logger for recording failure warnings |
| `WithInstrumentedTelemetry(provider)` | Telemetry provider for logical `ai.generate` / `ai.stream` spans |

**Key behaviors:**
- **Async recording** — LLM calls are never blocked by debug recording
- **Nil-safe** — Defaults to `NoOpLLMCallRecorder` if recorder is nil
- **Request ID resolution** — Tries OTel baggage first, then `core.GetRequestID(ctx)`
- **Graceful shutdown** — `Shutdown(ctx)` drains in-flight recordings via `sync.WaitGroup`
- **Factory wrapper collapse** — wrapping a `NewClient` or `NewRequestClient` result for debug recording replaces its internal no-op-recorder layer, avoiding duplicate logical spans

**Example — Agent Setup:**
```go
// Create base AI client
baseClient, _ := ai.NewClient(ai.WithProvider("anthropic"))

// Create LLM call recorder
recorder, _ := telemetry.NewRedisLLMCallRecorder()

// Wrap with instrumentation
aiClient := ai.NewInstrumentedClient(baseClient, recorder,
    ai.WithComponentName("research-assistant"),
)
defer aiClient.Shutdown(context.Background())

// Use as normal — all LLM calls are recorded automatically
response, err := aiClient.GenerateResponse(ctx, prompt, options)
```

---

## Resilience Module

Build fault-tolerant systems with circuit breakers and intelligent retry mechanisms.

### Circuit Breakers

Protect your application from cascading failures by automatically stopping calls to failing services.

#### NewCircuitBreaker

Create a production-ready circuit breaker with comprehensive configuration.

```go
func NewCircuitBreaker(config *CircuitBreakerConfig) (*CircuitBreaker, error)
func NewCircuitBreakerLegacy(failureThreshold int, recoveryTimeout time.Duration) *CircuitBreaker
```

**How it works:**
- **Closed State**: Normal operation, requests pass through
- **Open State**: Service is down, requests fail immediately
- **Half-Open State**: Testing recovery with limited requests

**Configuration parameters:**
- `ErrorThreshold` - Percentage of failures to open (0.0-1.0)
- `VolumeThreshold` - Minimum requests before evaluation
- `SleepWindow` - How long to wait before testing recovery
- `HalfOpenRequests` - Test requests in half-open state
- `SuccessThreshold` - Success rate to close again

**Example - Production Configuration:**
```go
// Sophisticated circuit breaker for production
breaker, err := resilience.NewCircuitBreaker(&resilience.CircuitBreakerConfig{
    Name:             "payment-api",
    ErrorThreshold:   0.5,              // Open at 50% error rate
    VolumeThreshold:  10,                // Need 10+ requests to evaluate
    SleepWindow:      30 * time.Second, // Wait 30s before recovery test
    HalfOpenRequests: 5,                 // Test with 5 requests
    SuccessThreshold: 0.6,               // Need 60% success to recover

    // Smart error classification
    ErrorClassifier: func(err error) bool {
        // Only infrastructure errors trip the breaker
        if errors.Is(err, context.Canceled) {
            return false  // User cancelled, don't count
        }
        if httpErr, ok := err.(*HTTPError); ok {
            return httpErr.StatusCode >= 500  // Only server errors
        }
        return true  // Network/timeout errors count
    },
})
```

#### Execute Methods

Execute functions with circuit breaker protection.

```go
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func() error) error
func (cb *CircuitBreaker) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, fn func() error) error
```

**What Execute does:**
1. Checks circuit state (fails fast if open)
2. Executes your function
3. Records success/failure
4. Updates circuit state
5. Handles panics gracefully

**Example - API Call Protection:**
```go
func CallPaymentAPI(order Order) error {
    return paymentBreaker.Execute(ctx, func() error {
        // This is protected by the circuit breaker
        resp, err := http.Post(paymentURL, "application/json", order)
        if err != nil {
            return err
        }

        if resp.StatusCode >= 500 {
            return fmt.Errorf("server error: %d", resp.StatusCode)
        }

        return nil
    })
}

// Handle circuit breaker states
err := CallPaymentAPI(order)
if errors.Is(err, core.ErrCircuitBreakerOpen) {
    // Service is down, use fallback
    log.Warn("Payment service unavailable, using queue")
    return queuePaymentForLater(order)
}
```

#### Monitoring and Control

Monitor circuit breaker state and metrics for operational visibility.

```go
// Get current state
state := breaker.GetState()  // "closed", "open", or "half-open"

// Get detailed metrics
metrics := breaker.GetMetrics()
// Returns: state, success, failure, error_rate, total_executions, rejected

// Manual control for maintenance
breaker.ForceOpen()    // Block all requests
breaker.ForceClosed()  // Allow all requests
breaker.ClearForce()   // Resume automatic operation

// State change notifications
breaker.AddStateChangeListener(func(name string, from, to CircuitState) {
    if to == resilience.StateOpen {
        alert.Send("Circuit breaker %s opened!", name)
    }
})
```

### Retry Mechanisms

Automatically retry failed operations with configurable backoff strategies.

#### Retry Function

Simple retry with exponential backoff.

```go
func Retry(ctx context.Context, config *RetryConfig, fn func() error) error
```

**RetryConfig parameters:**
- `MaxAttempts` - Maximum retry attempts
- `InitialDelay` - First retry delay
- `BackoffFactor` - Delay multiplier (2.0 = double each time)
- `MaxDelay` - Maximum delay between retries
- `JitterEnabled` - Add randomness to prevent thundering herd

**Example:**
```go
config := &resilience.RetryConfig{
    MaxAttempts:   5,
    InitialDelay:  100 * time.Millisecond,
    BackoffFactor: 2.0,      // 100ms, 200ms, 400ms, 800ms, 1600ms
    MaxDelay:      5 * time.Second,
    JitterEnabled: true,      // Prevent synchronized retries
}

err := resilience.Retry(ctx, config, func() error {
    return callFlakyService()
})
```

#### RetryExecutor

Production-ready retry with logging and telemetry.

```go
func NewRetryExecutor(config *RetryConfig) *RetryExecutor
func (r *RetryExecutor) Execute(ctx context.Context, operation string, fn func() error) error
```

**Why use RetryExecutor:**
- Named operations in logs
- Detailed retry logging
- Telemetry integration
- Success/failure metrics

**Example - Database Operations:**
```go
executor := resilience.NewRetryExecutor(&resilience.RetryConfig{
    MaxAttempts:   3,
    InitialDelay:  50 * time.Millisecond,
    BackoffFactor: 2.0,
    JitterEnabled: true,
})
executor.SetLogger(logger)

// Named operation appears in logs
err := executor.Execute(ctx, "fetch-user-profile", func() error {
    return db.QueryRow("SELECT * FROM users WHERE id = ?", userID).Scan(&user)
})

// Logs show:
// INFO: Starting retry operation [fetch-user-profile]
// DEBUG: Attempt 1 failed, retrying...
// INFO: Operation succeeded on attempt 2
```

#### RetryWithCircuitBreaker

Combine retry and circuit breaker for maximum resilience.

```go
func RetryWithCircuitBreaker(ctx context.Context, config *RetryConfig, cb *CircuitBreaker, fn func() error) error
```

**Best practice pattern:**
```go
type ResilientClient struct {
    breaker *resilience.CircuitBreaker
    retry   *resilience.RetryExecutor
}

func (c *ResilientClient) Call(ctx context.Context, request Request) (Response, error) {
    var response Response

    // Retry handles transient failures
    // Circuit breaker prevents cascading failures
    err := c.breaker.Execute(ctx, func() error {
        return c.retry.Execute(ctx, "api-call", func() error {
            return c.makeHTTPCall(request, &response)
        })
    })

    return response, err
}
```

---

## Telemetry Module

Comprehensive observability with metrics, distributed tracing, and context propagation.

### Basic Metrics

Simple functions for common metrics without boilerplate.

```go
func Counter(name string, labels ...string)
func Histogram(name string, value float64, labels ...string)
func Gauge(name string, value float64, labels ...string)
func Duration(name string, startTime time.Time, labels ...string)
```

**Example - Request Metrics:**
```go
// Count requests
telemetry.Counter("api.requests",
    "method", r.Method,
    "endpoint", r.URL.Path,
    "status", strconv.Itoa(status),
)

// Track latency
start := time.Now()
processRequest()
telemetry.Duration("api.latency", start,
    "endpoint", r.URL.Path,
)

// Monitor concurrent requests
telemetry.Gauge("api.concurrent_requests", float64(active))

// Track response sizes
telemetry.Histogram("api.response_bytes", float64(len(response)),
    "endpoint", r.URL.Path,
)
```

### Context-Aware Telemetry

Advanced telemetry that automatically correlates metrics with distributed traces.

```go
func EmitWithContext(ctx context.Context, name string, value float64, labels ...string)

// Baggage is `type Baggage map[string]string`.
func GetBaggage(ctx context.Context) Baggage

// WithBaggage attaches W3C baggage labels to the context. Pass flat
// key-value pairs (variadic), not a map.
func WithBaggage(ctx context.Context, labels ...string) context.Context
```

Span creation is **not** a top-level function. Spans are started through a
`core.Telemetry` instance — typically the one stored on your component
(`agent.Telemetry.StartSpan(ctx, name)`). The framework wires this for you when
you enable telemetry; bypassing it is rarely needed.

**Why use context-aware telemetry:**
- Automatic trace correlation
- Request metadata propagation
- Cross-service tracking
- Debugging complex flows

**Example - Distributed Request Tracking:**
```go
// API Gateway
func (gw *Gateway) Handle(ctx context.Context, req Request) error {
    // Start a span using the component's Telemetry instance, then attach
    // baggage that downstream services and metrics will see.
    ctx, span := gw.Telemetry.StartSpan(ctx, "gateway.handle")
    defer span.End()

    ctx = telemetry.WithBaggage(ctx,
        "request_id", req.ID,
        "user_id", req.UserID,
    )

    // Metrics include trace context
    telemetry.EmitWithContext(ctx, "gateway.requests", 1,
        "endpoint", req.Path,
    )

    // Call downstream service (context propagates)
    result, err := gw.authService.Authenticate(ctx, req.UserID)

    return err
}

// Auth Service (automatically gets context)
func (auth *AuthService) Authenticate(ctx context.Context, userID string) error {
    // Access propagated metadata
    baggage := telemetry.GetBaggage(ctx)
    requestID := baggage["request_id"]  // From gateway!

    // Metrics correlated to same trace
    telemetry.EmitWithContext(ctx, "auth.attempts", 1,
        "user_id", userID,
        "request_id", requestID,
    )

    return nil
}
```

### Type-Specific Helpers

Semantic helper functions for common metric types.

```go
func RecordError(name string, errorType string, labels ...string)
func RecordSuccess(name string, labels ...string)
func RecordLatency(name string, milliseconds float64, labels ...string)
func RecordBytes(name string, bytes int64, labels ...string)
```

**Example:**
```go
start := time.Now()
err := processOrder(order)

if err != nil {
    telemetry.RecordError("order.processing", err.Error(),
        "order_id", order.ID,
    )
} else {
    telemetry.RecordSuccess("order.processing",
        "order_id", order.ID,
    )
}

telemetry.RecordLatency("order.processing",
    float64(time.Since(start).Milliseconds()),
    "order_id", order.ID,
)
```

### Unified Metrics API

Cross-module metrics that enable consistent observability across agents and orchestration. These helpers emit standardized metrics with a `module` label, enabling unified Grafana dashboards regardless of which TruvaG3 module you use.

```go
// Module constants
const (
    ModuleAgent         = "agent"
    ModuleOrchestration = "orchestration"
    ModuleCore          = "core"
)

// Request metrics
func RecordRequest(module, operation string, durationMs float64, status string)
func RecordRequestError(module, operation, errorType string)

// Tool/capability call metrics
func RecordToolCall(module, toolName string, durationMs float64, status string)
func RecordToolCallError(module, toolName, errorType string)
func RecordToolCallRetry(module, toolName string)

// AI provider metrics
func RecordAIRequest(module, provider string, durationMs float64, status string)
func RecordAITokens(module, provider, tokenType string, count int64)
```

**Why use unified metrics:**
- Single Grafana dashboard works for both agent and orchestration examples
- Consistent metric names across all TruvaG3 modules
- Easy to compare performance between different module implementations
- Prometheus queries work regardless of which module emits the data

**Example - Agent Request Handler:**
```go
func handleResearchRequest(w http.ResponseWriter, r *http.Request) {
    startTime := time.Now()
    var requestStatus = "success"

    defer func() {
        durationMs := float64(time.Since(startTime).Milliseconds())
        // Unified metric - works in cross-module dashboards
        telemetry.RecordRequest(telemetry.ModuleAgent, "research", durationMs, requestStatus)
    }()

    // Parse request
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        requestStatus = "error"
        telemetry.RecordRequestError(telemetry.ModuleAgent, "research", "validation")
        http.Error(w, "Invalid request", http.StatusBadRequest)
        return
    }

    // Process...
}
```

**Example - Orchestration with AI:**
```go
func executeWithAI(ctx context.Context, prompt string) (string, error) {
    startTime := time.Now()

    response, err := aiClient.GenerateResponse(ctx, prompt, nil)

    // Record AI metrics
    status := "success"
    if err != nil {
        status = "error"
    }
    telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "openai",
        float64(time.Since(startTime).Milliseconds()), status)

    if err != nil {
        return "", err
    }
    return response.Content, nil
}
```

**Prometheus Queries for Unified Dashboards:**
```promql
# Request rate across all modules
sum(rate(request_total[5m])) by (module, operation)

# P95 latency by module
histogram_quantile(0.95, sum(rate(request_duration_ms_bucket[5m])) by (le, module))

# Error rate comparison: agent vs orchestration
sum(rate(request_errors[5m])) by (module, error_type)

# AI request latency by provider
histogram_quantile(0.95, sum(rate(ai_request_duration_ms_bucket[5m])) by (le, provider))
```

### LLM Call Recording

Interface and implementation for recording LLM calls to Redis for debugging. Used by `ai.InstrumentedAIClient` to capture every LLM call an agent makes.

#### LLMCallRecorder Interface

```go
type LLMCallRecorder interface {
    // RecordLLMCall appends an LLM call record to the debug trace for a request.
    // If requestID is empty, implementations silently skip recording.
    RecordLLMCall(ctx context.Context, requestID string, record LLMCallRecord) error
}
```

#### LLMCallRecord

```go
type LLMCallRecord struct {
    CallType        string    // e.g., "agent_llm_call", "plan_generation"
    SourceComponent string    // e.g., "research-assistant"
    Description     string    // Human-readable: "Tool selection for research"
    StepID          string    // Orchestration step ID
    Timestamp       time.Time
    DurationMs      int64
    Prompt          string
    SystemPrompt    string
    Temperature     float64
    MaxTokens       int
    Model           string    // e.g., "gpt-4o", "claude-3-sonnet"
    Provider        string    // e.g., "openai", "anthropic"
    Response        string
    PromptTokens    int
    CompletionTokens int
    TotalTokens     int
    Success         bool
    Error           string
}
```

#### NoOpLLMCallRecorder

Safe default that discards all recordings. Used when debug recording is not enabled.

```go
recorder := &telemetry.NoOpLLMCallRecorder{}
```

#### NewRedisLLMCallRecorder

Write-only Redis-backed recorder that writes to Redis DB 7 in the same format as the orchestration module's `RedisLLMDebugStore`. Agents use this instead of importing the orchestration module directly.

```go
func NewRedisLLMCallRecorder(opts ...RecorderOption) (*RedisLLMCallRecorder, error)
```

**Options:**

| Option | Description |
|--------|-------------|
| `WithRecorderRedisURL(url)` | Redis connection URL |
| `WithRecorderRedisDB(db)` | Redis database number (default: 7) |
| `WithRecorderLogger(logger)` | Logger for recorder operations |
| `WithRecorderTTL(ttl)` | TTL for successful records (default: 24h) |
| `WithRecorderErrorTTL(ttl)` | TTL for error records (default: 7 days) |

**Built-in resilience:** Layer 1 retry with exponential backoff (3 attempts, 100ms→2s) and failure cooldown (5 failures within 30s triggers cooldown).

**Example:**
```go
recorder, err := telemetry.NewRedisLLMCallRecorder(
    telemetry.WithRecorderRedisURL("redis://redis:6379"),
    telemetry.WithRecorderLogger(logger),
)
if err != nil {
    log.Printf("LLM recording disabled: %v", err)
    recorder = nil // InstrumentedClient falls back to NoOp
}
defer recorder.Close()
```

### Distributed Tracing

HTTP instrumentation for automatic trace context propagation across service boundaries.

#### TracingMiddleware

Wrap HTTP handlers to automatically extract and propagate W3C TraceContext headers.

```go
func TracingMiddleware(serviceName string) func(http.Handler) http.Handler
func TracingMiddlewareWithConfig(serviceName string, config *TracingMiddlewareConfig) func(http.Handler) http.Handler
```

**What it does:**
- Extracts `traceparent` and `tracestate` headers from incoming requests
- Creates a span for each HTTP request
- Records HTTP metrics (status codes, latency)
- Propagates trace context to handler code via `context.Context`

**TracingMiddlewareConfig options:**
- `ExcludedPaths` - Paths to skip tracing (e.g., `/health`, `/metrics`)
- `SpanNameFormatter` - Custom function to generate span names

**Example - Basic Usage:**
```go
// Initialize telemetry FIRST
telemetry.Initialize(telemetry.Config{
    ServiceName: "my-service",
    Endpoint:    "http://otel-collector:4318",
})
defer telemetry.Shutdown(context.Background())

// Create handlers
mux := http.NewServeMux()
mux.HandleFunc("/api/users", handleUsers)

// Wrap with tracing middleware
tracedHandler := telemetry.TracingMiddleware("my-service")(mux)
http.ListenAndServe(":8080", tracedHandler)
```

**Example - With Configuration:**
```go
config := &telemetry.TracingMiddlewareConfig{
    // Don't trace health checks
    ExcludedPaths: []string{"/health", "/metrics", "/ready"},

    // Custom span names
    SpanNameFormatter: func(op string, r *http.Request) string {
        return r.Method + " " + r.URL.Path
    },
}

tracedHandler := telemetry.TracingMiddlewareWithConfig("my-service", config)(mux)
```

#### NewTracedHTTPClient

Create an HTTP client that automatically propagates trace context to downstream services.

```go
func NewTracedHTTPClient(baseTransport http.RoundTripper) *http.Client
func NewTracedHTTPClientWithTransport(transport *http.Transport) *http.Client
```

**What it does:**
- Injects `traceparent` and `tracestate` headers into outgoing requests
- Creates child spans for each HTTP call
- Enables distributed tracing across service boundaries

**Example - Basic Usage:**
```go
// Create once, reuse for all requests (connection pooling)
client := telemetry.NewTracedHTTPClient(nil)

func callDownstreamService(ctx context.Context) error {
    // Context carries trace information
    req, _ := http.NewRequestWithContext(ctx, "GET", "http://other-service/api/data", nil)

    // Trace headers automatically injected
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    // Process response...
    return nil
}
```

**Example - With Custom Transport:**
```go
// Production configuration with connection pooling
transport := &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 10,
    IdleConnTimeout:     90 * time.Second,
}

client := telemetry.NewTracedHTTPClientWithTransport(transport)
```

**Best practices:**
- Create `TracedHTTPClient` once and reuse (connection pooling)
- Always pass `context.Context` from incoming request to outgoing calls
- Initialize telemetry before creating traced clients

#### End-to-End Tracing Example

Complete example showing trace propagation across services:

```go
// === Service A (API Gateway) ===
func main() {
    telemetry.Initialize(telemetry.Config{ServiceName: "api-gateway"})
    defer telemetry.Shutdown(context.Background())

    // Create traced client for calling Service B
    serviceB := telemetry.NewTracedHTTPClient(nil)

    mux := http.NewServeMux()
    mux.HandleFunc("/api/request", func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()  // Contains trace from middleware

        // Call Service B - trace context propagates automatically
        req, _ := http.NewRequestWithContext(ctx, "GET", "http://service-b/process", nil)
        resp, _ := serviceB.Do(req)
        defer resp.Body.Close()

        // Respond...
    })

    // Wrap with tracing middleware
    traced := telemetry.TracingMiddleware("api-gateway")(mux)
    http.ListenAndServe(":8080", traced)
}

// === Service B ===
func main() {
    telemetry.Initialize(telemetry.Config{ServiceName: "service-b"})
    defer telemetry.Shutdown(context.Background())

    mux := http.NewServeMux()
    mux.HandleFunc("/process", func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()  // Trace context from Service A!

        // Metrics are correlated with the trace
        telemetry.EmitWithContext(ctx, "service-b.processed", 1)

        w.WriteHeader(http.StatusOK)
    })

    traced := telemetry.TracingMiddleware("service-b")(mux)
    http.ListenAndServe(":8081", traced)
}

// Result in Jaeger/Tempo:
// Trace abc123:
// ├── api-gateway: /api/request (100ms)
// │   └── service-b: /process (50ms)
```

---

## Orchestration Module

Intelligently coordinate multiple agents and tools to accomplish complex tasks.

### CreateSimpleOrchestrator

Zero-configuration orchestrator for getting started quickly.

```go
func CreateSimpleOrchestrator(discovery core.Discovery, aiClient core.AIClient) *AIOrchestrator
```

**Example:**
```go
// Just works - no configuration needed!
orchestrator := orchestration.CreateSimpleOrchestrator(discovery, aiClient)

// Process complex natural language requests
response, err := orchestrator.ProcessRequest(ctx,
    "Get weather for NYC and find related news articles, then summarize everything",
    nil,  // Auto-discovers needed services
)

fmt.Println(response.Response)
// Output: "Today in NYC, it's 72°F with partly cloudy skies. Recent news includes..."
```

### CreateOrchestratorWithOptions

Create an orchestrator with flexible configuration options.

```go
func CreateOrchestratorWithOptions(deps OrchestratorDependencies, opts ...OrchestratorOption) (*AIOrchestrator, error)
```

**Available options:**
- `WithCapabilityProvider(type, url)` - Configure capability discovery
- `WithTieredResolution(enabled)` - Enable tiered capability resolution (token optimization)
- `WithTieredSelectionRetry(enabled, maxRetries)` - Retry tiered selection on empty LLM responses and parse failures (default: true, 2)
- `WithTelemetry(enabled)` - Enable metrics and tracing
- `WithFallback(enabled)` - Graceful degradation
- `WithPlanParseRetry(enabled, maxRetries)` - Retry plan generation on JSON parse failures
- `WithHallucinationRetry(enabled, maxRetries)` - Retry on hallucinated agent names
- `WithPlanAIOptions(opts)` - Per-phase overrides for planning LLM calls
- `WithSynthesisAIOptions(opts)` - Per-phase overrides for synthesis LLM calls
- `WithMicroResolutionAIOptions(opts)` - Per-phase overrides for micro-resolution, semantic retry, and plan refinement calls
- `WithTieredSelectionAIOptions(opts)` - Per-phase overrides for tiered capability selection
- `WithErrorAnalysisAIOptions(opts)` - Per-phase overrides for error analysis calls
- `WithResultDistillAIOptions(opts)` - Per-phase overrides for result distillation calls
- `WithResultTrimming(enabled, maxResultBytes)` - Enable/configure structural result trimming (Layer 1)
- `WithResultPreserveKeys(keys)` - JSON keys the structural trimmer always keeps
- `WithResultDistill(enabled, distillThreshold)` - Enable/configure LLM result distillation (Layer 2)
- `WithResultDistillModel(model)` - Model or portable alias for distillation calls (use `fast`/`default`/`smart`, not a concrete name, for ChainClient failover)
- `WithMaxConcurrency(n)` - Max parallel step executions in DAG (default: 25)
- `WithStepTimeout(d)` - Per-step execution timeout (default: 120s)
- `WithTotalTimeout(d)` - Total HTTP client timeout for tool/agent calls (default: 600s)

**Iterative planning** is configured via `DefaultConfig()` struct fields or environment variables (no `With*` option function). See [IterativePlanConfig](#iterativeplanconfig) for details.

To build the Layer-2 distillation result processor **outside** `CreateOrchestrator` (e.g. in a custom runner), use `BuildDistillationEnabledResultProcessor(cfg ResultDistillConfig, ai core.AIClient, cache core.DigestCache, logger core.Logger) ResultProcessor` — a `StructuralTrimmer` wrapped by the `LLMDistiller` wrapped by a fail-open cache (nil `ai` → bare structural floor; nil `cache` → no caching).

**`OrchestratorDependencies` fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `Discovery` | `core.Discovery` | Yes | Service discovery provider |
| `AIClient` | `core.AIClient` | Yes | LLM provider client |
| `CircuitBreaker` | `core.CircuitBreaker` | No | Sophisticated resilience patterns |
| `Logger` | `core.Logger` | No | Structured logging |
| `Telemetry` | `core.Telemetry` | No | Metrics and tracing |
| `PromptBuilder` | `PromptBuilder` | No | Custom prompt building (default: `DefaultPromptBuilder`) |
| `EnableErrorAnalyzer` | `bool` | No | LLM-based error analysis (Layer 3) |
| `ResultProcessor` | `ResultProcessor` | No | Custom synthesis-prompt result compactor (default: `StructuralTrimmer`, or the LLM distiller when distillation is enabled) |
| `SourceResultProcessor` | `ResultProcessor` | No | Deterministic trimmer for micro-/semantic-retry **source** data. A custom override is always honored; the built-in `StructuralTrimmer` default is installed only when result trimming is enabled |
| `AgentInputProcessor` | `AgentInputProcessor` | No | Transform/redact/trim tool→tool input parameters before dispatch (default: identity; built-in byte guard when `MaxAgentInputBytes > 0`) |
| `ContinuationDistiller` | `ResultProcessor` | No | Summarizer for non-JSON continuation escalations (default: built-in LLM distiller when distillation is enabled and an `AIClient` is present) |
| `DistillCache` | `core.DigestCache` | No | Content-addressed cache for distillation results (nil = no caching) |
| `PipelineHooks` | `[]core.PipelineHook` | No | Per-stage middleware for context engineering. See [Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) |
| `ConversationHistoryPreparer` | `ConversationHistoryPreparer` | No | Shared conversation-history preparer for the metadata and hook ingress paths. If nil, the factory auto-builds the default Tier 1 processor from config. |
| `ActivityCoordinator` | `core.ActivityCoordinator` | No | Real-time agent coordination signals — typically the second return value of `orchestration.BuildMemoryHooks` |

**Example - Production Orchestrator:**
```go
// Set up dependencies
deps := orchestration.OrchestratorDependencies{
    Discovery: redisDiscovery,
    AIClient:  aiClient,
    Logger:    logger,
}

// Create with options
orchestrator, err := orchestration.CreateOrchestratorWithOptions(deps,
    orchestration.WithCapabilityProvider("service", "http://capability-service:8080"),
    orchestration.WithTelemetry(true),
    orchestration.WithFallback(true),
    orchestration.WithPlanAIOptions(&orchestration.AIOptionsOverride{
        MaxTokens: orchestration.IntPtr(15000),
    }),
)

// Process requests with automatic service discovery and coordination
response, err := orchestrator.ProcessRequest(ctx,
    "Analyze sales data and generate report",
    nil,
)
```

**Example - With Pipeline Hooks (Context Engineering):**
```go
deps := orchestration.OrchestratorDependencies{
    Discovery: redisDiscovery,
    AIClient:  aiClient,
    Logger:    logger,
    PipelineHooks: []core.PipelineHook{
        cacheHook,    // BeforePlanningHook — short-circuits on cache hit
        ragHook,      // BeforePlanningHook — injects RAG context
        analytics,    // AfterExecutionHook — logs to Kafka
        guardrails,   // AfterSynthesisHook — filters response
    },
}
```

For chat agents, the preferred conversation-history path is request metadata, not a hook. Pass raw turns in `metadata[orchestration.MetadataConversationTurns]` plus the stable session key in `metadata[orchestration.MetadataConversationSessionKey]`, and let the shared `ConversationHistoryPreparer` build the `<conversation_history>` enrichment before planning. `ConversationHistoryHook` remains available as an adapter for memory-backed integrations that cannot supply raw turns directly. For Tier 2 recursive compaction, the ergonomic Layer 2 path is `BuildCompactionEnabledConversationHistoryPreparer(config, aiClient, ...options)`, which installs the default cache and LLM compactor while still allowing `WithConversationSummaryCache(...)` and `WithConversationCompactor(...)` overrides.

For detailed pipeline hook implementation patterns, see [Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md). For the conversation-history metadata path plus Tier 2 / Layer 3 reference implementations, see [Conversation History Guide](../memory-and-chat/CONVERSATION_HISTORY_GUIDE.md).

### Orchestrator Interface Methods

The `Orchestrator` interface provides the following methods:

```go
type Orchestrator interface {
    // ProcessRequest handles a natural language request by orchestrating multiple agents
    ProcessRequest(ctx context.Context, request string, metadata map[string]interface{}) (*OrchestratorResponse, error)

    // ExecutePlan executes a pre-defined routing plan (raw results, no synthesis)
    ExecutePlan(ctx context.Context, plan *RoutingPlan) (*ExecutionResult, error)

    // ExecutePlanWithSynthesis executes a pre-defined routing plan with synthesis.
    // Unlike ExecutePlan(), this method:
    // 1. Uses the orchestrator's synthesizer (which auto-records to LLM Debug Store)
    // 2. Returns a complete OrchestratorResponse (not raw ExecutionResult)
    // 3. Stores execution to ExecutionStore for DAG visualization
    // 4. Sets up context baggage for request_id propagation
    ExecutePlanWithSynthesis(ctx context.Context, plan *RoutingPlan, originalRequest string) (*OrchestratorResponse, error)

    // GetExecutionHistory returns recent execution history
    GetExecutionHistory() []ExecutionRecord

    // GetMetrics returns orchestrator metrics
    GetMetrics() OrchestratorMetrics
}
```

**When to use each method:**

| Method | Use Case |
|--------|----------|
| `ProcessRequest` | Natural language requests with AI-driven planning |
| `ExecutePlan` | Pre-defined workflows when you need raw results for custom synthesis |
| `ExecutePlanWithSynthesis` | Pre-defined workflows with full observability (DAG visualization, LLM debug store) |

**Example - ExecutePlanWithSynthesis:**
```go
// Build a routing plan
plan := &orchestration.RoutingPlan{
    PlanID: "travel-research-123",
    Steps: []orchestration.RoutingStep{
        {
            StepID:      "step-1",
            AgentName:   "weather-tool",
            Instruction: "Get current weather",
            Metadata: map[string]interface{}{
                "capability": "get_weather",
                "parameters": map[string]interface{}{"location": "Tokyo"},
            },
        },
        {
            StepID:      "step-2",
            AgentName:   "currency-tool",
            Instruction: "Convert USD to JPY",
            Metadata: map[string]interface{}{
                "capability": "convert_currency",
                "parameters": map[string]interface{}{"from": "USD", "to": "JPY", "amount": 1000},
            },
        },
    },
}

// Execute with synthesis and DAG storage
response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Plan my trip to Tokyo")
if err != nil {
    return err
}

// Response includes synthesized result and step details
fmt.Println(response.Response)      // AI-synthesized summary
fmt.Println(response.Steps)         // Individual step results
fmt.Println(response.RequestID)     // For DAG visualization lookup
```

### ExecutionOptions Configuration

Configure execution behavior for the orchestrator, including retry logic and type safety features.

```go
type ExecutionOptions struct {
    MaxConcurrency           int           // Default: 25 | Env: TRUVAG3_EXECUTION_MAX_CONCURRENCY
    StepTimeout              time.Duration // Default: 120s | Env: TRUVAG3_EXECUTION_STEP_TIMEOUT
    TotalTimeout             time.Duration // Default: 600s | Env: TRUVAG3_ORCHESTRATION_TIMEOUT
    RetryAttempts            int           // Total attempts per step (default: 3 = 1 initial + 2 retries; overridable via TRUVAG3_STEP_RETRY_MAX_ATTEMPTS)
    RetryDelay               time.Duration // Delay between retries (default: 2s)
    CircuitBreaker           bool          // Enable circuit breaker (default: true)
    FailureThreshold         int           // Circuit breaker threshold (default: 5)
    RecoveryTimeout          time.Duration // Circuit breaker recovery (default: 30s)

    // Type Safety (Layer 3 - Validation Feedback)
    ValidationFeedbackEnabled bool         // Enable LLM-based parameter correction (default: true)
    MaxValidationRetries      int          // Max correction attempts (default: 2)
}
```

**Type Safety Configuration:**

```go
// Default configuration - maximum reliability (~99% success rate)
config := orchestration.DefaultConfig()
// ValidationFeedbackEnabled: true (default)
// MaxValidationRetries: 2 (default)

// Cost-sensitive configuration (~95% success rate)
config.ExecutionOptions.ValidationFeedbackEnabled = false

// Maximum reliability configuration
config.ExecutionOptions.ValidationFeedbackEnabled = true
config.ExecutionOptions.MaxValidationRetries = 3  // More retries for edge cases
```

See [Intelligent Error Handling](../orchestration/INTELLIGENT_ERROR_HANDLING.md#orchestration-module-multi-layer-type-safety) for details on how type safety layers work together.

#### Step Retry Backoff

The executor uses exponential backoff (via `core.BackoffConfig`) for step retries instead of linear delay. Configurable via setter or environment variables.

```go
// SetStepRetryBackoff configures the backoff strategy for step retries.
// Default is core.DefaultBackoffConfig() (500ms initial, 10s max, 2x factor, jitter enabled).
// Also overridable via TRUVAG3_STEP_RETRY_INITIAL_DELAY and TRUVAG3_STEP_RETRY_MAX_DELAY env vars.
func (e *SmartExecutor) SetStepRetryBackoff(config core.BackoffConfig)
```

**Environment variables:**

| Variable | Default | Description |
|---|---|---|
| `TRUVAG3_STEP_RETRY_INITIAL_DELAY` | `500ms` | Initial backoff delay. Go duration format (e.g., `500ms`, `2s`). |
| `TRUVAG3_STEP_RETRY_MAX_DELAY` | `10s` | Maximum backoff delay cap. Go duration format. |

**Precedence:** `SetStepRetryBackoff()` (explicit) > `TRUVAG3_STEP_RETRY_*` env vars > `core.DefaultBackoffConfig()` defaults.

### TieredCapabilityConfig

Configures tiered capability resolution for LLM token optimization. When enabled with 20+ tools, uses a two-phase approach: lightweight summaries for tool selection, then full schemas only for selected tools.

```go
type TieredCapabilityConfig struct {
    // MinToolsForTiering is the minimum tool count to trigger tiered resolution.
    // Below this threshold, all tools are sent directly (simpler, one LLM call).
    // Default: 20 | Env: TRUVAG3_TIERED_MIN_TOOLS
    // Research: "Less is More" (Nov 2024) shows LLM accuracy degradation at ~20 tools
    MinToolsForTiering int `json:"min_tools_for_tiering,omitempty"`

    // SelectionMaxTokens is the maximum output tokens for tiered selection LLM calls.
    // Higher values allow complex multi-tool selections but cost more tokens.
    // Default: 2000 | Env: TRUVAG3_TIERED_SELECTION_MAX_TOKENS
    SelectionMaxTokens int `json:"selection_max_tokens,omitempty"`

    // RetryEnabled enables retry on empty LLM responses and parse failures.
    // Default: true | Env: TRUVAG3_TIERED_SELECTION_RETRY_ENABLED
    RetryEnabled bool `json:"retry_enabled,omitempty"`

    // MaxRetries is the number of additional attempts after the initial failure.
    // 2 means up to 3 total attempts (1 initial + 2 retries).
    // Default: 2 | Env: TRUVAG3_TIERED_SELECTION_RETRY_MAX
    MaxRetries int `json:"max_retries,omitempty"`
}
```

**Example - Enable Tiered Resolution:**
```go
config := orchestration.DefaultConfig()
config.EnableTieredResolution = true
config.TieredResolution = orchestration.TieredCapabilityConfig{
    MinToolsForTiering: 25,    // Override threshold (default: 20)
    SelectionMaxTokens: 3000,  // Override selection output tokens (default: 2000)
    RetryEnabled:       true,  // Retry on empty responses / parse failures (default: true)
    MaxRetries:         2,     // Up to 2 retries after initial failure (default: 2)
}
```

### PromptConfig

Configures prompt building behavior including persona customization and type rules.

```go
type PromptConfig struct {
    // SystemInstructions defines the orchestrator's core behavioral context.
    // Similar to LangChain's system_prompt, AutoGen's system_message, OpenAI's instructions.
    // When set, the developer's persona becomes the primary identity.
    SystemInstructions string `json:"system_instructions,omitempty"`

    // AdditionalTypeRules extend default type handling rules.
    AdditionalTypeRules []TypeRule `json:"additional_type_rules,omitempty"`

    // TemplateFile is the path to a Go text/template file for prompt customization.
    TemplateFile string `json:"template_file,omitempty"`

    // Template is an inline Go text/template string.
    Template string `json:"template,omitempty"`

    // CustomInstructions are additional instructions appended to the prompt.
    CustomInstructions []string `json:"custom_instructions,omitempty"`

    // Domain provides context for domain-specific prompt adjustments.
    // Examples: "healthcare", "finance", "legal", "retail"
    Domain string `json:"domain,omitempty"`

    // IncludeAntiPatterns controls whether to show "what NOT to do" examples.
    IncludeAntiPatterns *bool `json:"include_anti_patterns,omitempty"`

    // IterativePlanConfig provides budget information (MaxPhases, MaxTotalSteps)
    // to prompt builders so they can embed budget-aware iterative planning
    // instructions. Populated automatically by NewAIOrchestrator when
    // iterative planning is enabled.
    IterativePlanConfig *IterativePlanConfig `json:"iterative_plan_config,omitempty"`
}
```

**Example - Custom Agent Persona:**
```go
config := orchestration.DefaultConfig()
config.PromptConfig = orchestration.PromptConfig{
    SystemInstructions: `You are a travel planning assistant.
Always check weather before recommending outdoor activities.
Prefer real-time data sources over cached information.`,
    Domain: "travel",
}
```

### IterativePlanConfig

Configures multi-phase DAG planning behavior. When enabled, the LLM planner can signal that a plan is partial (`terminal: false`), causing the orchestrator to execute the known phase, feed results back to the planner, and generate continuation plans until the planner produces a terminal plan.

This enables "discovery → action" queries where later steps depend on the semantic content of earlier results — e.g., "find famous tourist destinations in Germany and get their weather."

```go
type IterativePlanConfig struct {
    // Enabled controls whether iterative planning is active.
    // When false, the terminal field is ignored and all plans are treated as terminal.
    // Default: true | Env: TRUVAG3_ITERATIVE_PLANNING_ENABLED
    Enabled bool `json:"enabled"`

    // MaxPhases is the maximum number of planning phases allowed per request.
    // If reached without a terminal plan, the orchestrator forces termination
    // and synthesizes with available results.
    // Default: 5 | Env: TRUVAG3_ITERATIVE_MAX_PHASES
    MaxPhases int `json:"max_phases"`

    // MaxTotalSteps is the maximum total steps across all phases.
    // Prevents runaway plan generation.
    // Default: 200 | Env: TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS
    MaxTotalSteps int `json:"max_total_steps"`

    // PhaseTimeout is the maximum duration for a single phase (plan generation + execution).
    // Prevents a single continuation phase from hanging indefinitely.
    // Default: 180s | Env: TRUVAG3_ITERATIVE_PHASE_TIMEOUT
    PhaseTimeout time.Duration `json:"phase_timeout"`
}
```

**Example - Default (enabled, suitable for most queries):**
```go
config := orchestration.DefaultConfig()
// IterativePlanning is already configured with sensible defaults:
// Enabled: true, MaxPhases: 5, MaxTotalSteps: 200, PhaseTimeout: 180s
```

**Example - Complex research queries:**
```go
config := orchestration.DefaultConfig()
config.IterativePlanning = orchestration.IterativePlanConfig{
    Enabled:       true,
    MaxPhases:     5,              // Allow more discovery rounds
    MaxTotalSteps: 30,             // Allow more total steps for fan-out queries
    PhaseTimeout:  60 * time.Second, // Longer timeout for slow external APIs
}
```

**Example - Cost-sensitive (disable multi-phase):**
```go
config := orchestration.DefaultConfig()
config.IterativePlanning.Enabled = false // All plans treated as single-shot
```

**Environment variable overrides** (take precedence over programmatic config):
```bash
export TRUVAG3_ITERATIVE_PLANNING_ENABLED=true
export TRUVAG3_ITERATIVE_MAX_PHASES=5
export TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS=200
export TRUVAG3_ITERATIVE_PHASE_TIMEOUT=180s
```

**Configuration precedence:** Environment variable → Programmatic config → `DefaultConfig()` defaults.

### ContinuationResultMaxChars

Floor-preview size (in chars) for a **non-JSON** completed-step result in continuation planning prompts (Phase 14). JSON step results are rendered as a structure-complete digest (skeleton) instead — this knob governs only the fallback preview for non-JSON blobs (logs, markdown, CSV) that the planner reads when the continuation distiller does not summarize the step.

```go
type OrchestratorConfig struct {
    // ...
    ContinuationResultMaxChars int `json:"continuation_result_max_chars,omitempty"`
    // Default: 10000 | Env: TRUVAG3_CONTINUATION_RESULT_MAX_CHARS
}
```

**Default:** `10000`. **Related:** When an orchestrator step's response contains a `steps[]` array, the continuation builder extracts a structured child-sub-step summary (from the full response, before any trimming) and appends a "Do NOT duplicate" directive — so child-step visibility holds regardless of this knob.

### Continuation Digest Budgeting (Phase 14)

The continuation prompt's `<completed_steps>` section renders each completed step as a decision digest under an aggregate budget. These top-level `OrchestratorConfig` fields tune it (all env-overridable; no `With*` options):

```go
type OrchestratorConfig struct {
    // ...
    ContinuationResultMaxTotalChars int `json:"continuation_result_max_total_chars,omitempty"` // Default: 32768 | Env: TRUVAG3_CONTINUATION_RESULT_MAX_TOTAL_CHARS
    ContinuationMaxEscalations      int `json:"continuation_max_escalations,omitempty"`        // Default: 8     | Env: TRUVAG3_CONTINUATION_MAX_ESCALATIONS
    ContinuationDigestArraySample   int `json:"continuation_digest_array_sample,omitempty"`    // Default: 3     | Env: TRUVAG3_CONTINUATION_DIGEST_ARRAY_SAMPLE
    ContinuationDigestScalarMax     int `json:"continuation_digest_scalar_max,omitempty"`      // Default: 200   | Env: TRUVAG3_CONTINUATION_DIGEST_SCALAR_MAX
    ContinuationDigestMaxKeys       int `json:"continuation_digest_max_keys,omitempty"`        // Default: 50    | Env: TRUVAG3_CONTINUATION_DIGEST_MAX_KEYS
}
```

- **`ContinuationResultMaxTotalChars`** — a **soft target** (chars) for the whole completed-steps section; drives newest-first eviction (older steps evict with a "showing N of M" note, ~268 B/digest measured → ~122 steps fit 32 KB). **Not a hard cap:** failed steps are always kept, the newest successful step is always kept even if it alone exceeds the budget, and the N-of-M note plus any orchestrator child-summaries are appended after the budget decision — so the rendered section can modestly exceed it.
- **`ContinuationMaxEscalations`** — max non-JSON steps escalated to the continuation distiller (a fast-model summary) per phase, **newest-first**. Sequential fast-model calls on the phase-gating path; `0` disables. Fires ~never on all-JSON workloads.
- **`ContinuationDigestArraySample`** — per-array head-sample size (arrays render as the first N elements + a length sentinel).
- **`ContinuationDigestScalarMax`** — max length of a string value kept inline before elision; raise it to surface longer salient values (status/error strings) at plan time.
- **`ContinuationDigestMaxKeys`** — per-object key cap; schema objects are kept whole, map-shaped objects (many dynamic-ID keys) are sampled to N sorted keys + a sentinel.

### RemediationFailurePattern

Tunes the shared-error pattern summary that is embedded into the remediation continuation prompt when multiple upstream steps fail with the same error. When one or more steps are skipped because their template-referenced dependencies failed, the orchestrator forces a remediation continuation so the planner can adapt. If the causal failures share a dominant error signature, a one-line summary — e.g. `"Upstream failure pattern: 2 of 2 prior steps failed with the same error; retries exhausted (flight-tool/search_airports): upstream API error 500"` — is prepended to that prompt so the planner has concrete evidence that the upstream is persistently unavailable.

```go
type OrchestratorConfig struct {
    // ...
    // Minimum distinct failed upstream steps required before a pattern
    // summary is emitted. 1 would make every skip emit a summary (noisy).
    // Default: 2 | Env: TRUVAG3_FAILURE_PATTERN_MIN_FAILURES
    RemediationFailurePatternMinFailures int `json:"remediation_failure_pattern_min_failures,omitempty"`

    // Max chars of error text used to bucket failures into a shared
    // signature for classification. Wider than the display cap to reduce
    // false-positive collisions between distinct errors that share a
    // common prefix.
    // Default: 120 | Env: TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN
    RemediationFailurePatternSignatureLen int `json:"remediation_failure_pattern_signature_len,omitempty"`

    // Max chars of the shared error rendered into the remediation prompt
    // (trailing `…` appended on truncation). Kept short so the
    // continuation prompt stays slim per EFFECTIVE_PROMPTS_GUIDE §4.5.
    // Default: 80 | Env: TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN
    RemediationFailurePatternDisplayLen int `json:"remediation_failure_pattern_display_len,omitempty"`
}
```

**Observability:** Emission is observable via the `orchestrator.remediation.triggered` span event (attribute `has_failure_pattern`) and the `remediation_failure_pattern` DEBUG log (fields `emitted`, `reject_reason`). See [DISTRIBUTED_TRACING_GUIDE](../observability/DISTRIBUTED_TRACING_GUIDE.md) and [LOGGING_IMPLEMENTATION_GUIDE](../observability/LOGGING_IMPLEMENTATION_GUIDE.md) §11.

### ResultTrimConfig

Configures automatic structural trimming of large step results before they are embedded in LLM prompts. When tool/agent responses are large (e.g., full web pages, large API payloads), they can overflow the LLM's token budget during synthesis. The `StructuralTrimmer` intelligently reduces result sizes — preserving JSON structure, key names, and representative samples while dropping repetitive array elements and deeply nested data.

```go
type ResultTrimConfig struct {
    // Enabled controls whether result trimming is active.
    // Default: true | Env: TRUVAG3_RESULT_TRIM_ENABLED
    Enabled bool `json:"enabled"`

    // MaxResultBytes is the maximum bytes per individual step result.
    // Results exceeding this are structurally trimmed.
    // Note: Single-step requests use MaxTotalPromptBytes (32KB) instead,
    // since there is no multi-result allocation needed.
    // Default: 16384 (16 KB, ~4K tokens) | Env: TRUVAG3_RESULT_TRIM_MAX_BYTES
    MaxResultBytes int `json:"max_result_bytes"`

    // MaxTotalPromptBytes is the maximum total bytes across all step results
    // in a synthesis prompt. When combined results exceed this limit,
    // BudgetAllocator distributes budget proportionally by result size,
    // then redistributes savings from results clamped by MaxResultBytes
    // to other eligible results. Allocation is distillation-aware: a result the
    // processor will distill is sized at its post-distill footprint (~TargetSize,
    // via EffectiveSizer), not its raw bytes, so a large distillable result
    // doesn't crowd out the others.
    // Default: 32768 (32 KB, ~8K tokens) | Env: TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES
    MaxTotalPromptBytes int `json:"max_total_prompt_bytes"`

    // MaxMicroResolutionBytes controls how much source data is included when
    // the LLM resolves missing parameters from prior step results (micro-resolution)
    // or retries failed steps (semantic retry).
    // Default: 65536 (64 KB, ~26K tokens) | Env: TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES
    MaxMicroResolutionBytes int `json:"max_micro_resolution_bytes"`

    // MaxAgentInputBytes is the maximum bytes per parameter value for
    // agent/tool HTTP calls. Trims large data values before they are sent
    // as input parameters to downstream agents or tools.
    // Default: 0 (no cap — fidelity-first: tool→tool data flows raw so
    // downstream steps receive the full upstream output). Set > 0 (or supply
    // deps.AgentInputProcessor) to enable the byte-budget guard.
    // Env: TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES
    MaxAgentInputBytes int `json:"max_agent_input_bytes"`

    // SchemaGuidedMappingThreshold is the result size (bytes) above which
    // schema-guided mapping is used instead of direct value extraction for
    // micro-resolution. Uses JSON schema analysis to map fields without
    // passing raw data to the LLM. Set to 0 to disable schema-guided mapping.
    // Default: 16384 (16 KB) | Env: TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD
    SchemaGuidedMappingThreshold int `json:"schema_guided_mapping_threshold"`

    // PreserveKeys lists JSON keys that should never be trimmed.
    PreserveKeys []string `json:"preserve_keys,omitempty"`
}
```

**Example - Default (enabled, suitable for most queries):**
```go
config := orchestration.DefaultConfig()
// ResultTrim is already configured with sensible defaults:
// Enabled: true, MaxResultBytes: 16384, MaxTotalPromptBytes: 32768,
// MaxMicroResolutionBytes: 65536, MaxAgentInputBytes: 0 (no cap, fidelity-first),
// SchemaGuidedMappingThreshold: 16384
```

**Example - Larger budgets for data-heavy workflows:**
```go
config := orchestration.DefaultConfig()
config.ResultTrim = orchestration.ResultTrimConfig{
    Enabled:                      true,
    MaxResultBytes:               32768, // 32 KB per result
    MaxTotalPromptBytes:          65536, // 64 KB total
    MaxMicroResolutionBytes:      131072, // 128 KB for resolution prompts
    MaxAgentInputBytes:           131072, // 128 KB per agent parameter
    SchemaGuidedMappingThreshold: 16384,  // Schema mapping above 16 KB
}
```

**Example - Disable schema-guided mapping:**
```go
config := orchestration.DefaultConfig()
config.ResultTrim.SchemaGuidedMappingThreshold = 0 // Always use direct value extraction
```

**Environment variable overrides:**
```bash
export TRUVAG3_RESULT_TRIM_ENABLED=true
export TRUVAG3_RESULT_TRIM_MAX_BYTES=16384
export TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES=32768
export TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES=65536
export TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES=0   # default 0 = no cap (fidelity-first); set > 0 to enable the guard
export TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD=16384
```

### ResultTrimMetadata

Captures metadata about each result trimming operation. Populated by the `ResultProcessor` during `ProcessForPrompt()` and exposed via `StepResult.Metadata["result_trim"]`. Also emitted as attributes on the `result_trim.completed` span event.

```go
type ResultTrimMetadata struct {
    OriginalBytes    int      `json:"original_bytes"`              // Pre-trim size in bytes
    TrimmedBytes     int      `json:"trimmed_bytes"`               // Post-trim size in bytes
    Method           string   `json:"method"`                      // "structural", "structural_array", "structural_text", "truncate", "distill", "distill_mapreduce"
    FieldsKept       int      `json:"fields_kept,omitempty"`       // Number of fields/items retained
    FieldsDropped    int      `json:"fields_dropped,omitempty"`    // Number of fields/items dropped
    BackfilledCount  int      `json:"backfilled_count,omitempty"`  // Fields recovered via multi-field backfill
    ThresholdSkipped int      `json:"threshold_skipped,omitempty"` // Candidates skipped below minimum relevance (0.3)
    Keywords         []string `json:"keywords,omitempty"`          // Extracted query keywords from step instruction
    MatchedPaths     []string `json:"matched_paths,omitempty"`     // Field paths selected by keyword match
    BudgetAllocated  int      `json:"budget_allocated,omitempty"`  // Per-result budget from BudgetAllocator (multi-result only)
    Degenerate       bool     `json:"degenerate,omitempty"`        // Structural trim kept a non-representative fraction (< 5%); triggers the "severely reduced … treat as UNKNOWN" disclosure
    KeptRatio        float64  `json:"kept_ratio,omitempty"`        // trimmed_bytes / original_bytes
}
```

**Accessing trim metadata programmatically:**
```go
// After orchestration completes, inspect per-step trim metadata:
for _, step := range response.Steps {
    if meta, ok := step.Metadata["result_trim"]; ok {
        trimMeta := meta.(*orchestration.ResultTrimMetadata)
        fmt.Printf("Step %s: %d → %d bytes, %d fields kept, %d dropped\n",
            step.StepID, trimMeta.OriginalBytes, trimMeta.TrimmedBytes,
            trimMeta.FieldsKept, trimMeta.FieldsDropped)
    }
}
```

### ResultDistillConfig

Configures **default-on** LLM-based result distillation — the primary compaction path for over-budget results. Uses a two-stage pipeline: structural pre-filtering (Stage 1) followed by LLM-based summarization (Stage 2); results whose estimated token count exceeds `ModelContextTokens` are chunked and map-reduced. Active when the orchestrator has an `AIClient` **and** Result Trimming is enabled (both true by default); without an `AIClient` it falls back to the `StructuralTrimmer` floor. Opt out with `TRUVAG3_RESULT_DISTILL_ENABLED=false`. Every distillation is bounded by `CompactionDeadline` (fail-open). Distillation results are **cached only when a `DigestCache` is supplied** via `deps.DistillCache` — there is no cache by default — and `CacheTTL` applies only then.

```go
type ResultDistillConfig struct {
    // Enabled controls whether LLM distillation is active.
    // Default: true | Env: TRUVAG3_RESULT_DISTILL_ENABLED
    Enabled bool `json:"enabled"`

    // DistillThreshold is the minimum result size (bytes) to trigger distillation.
    // Results below this threshold use structural trimming only.
    // Default: 16384 (16 KB) | Env: TRUVAG3_RESULT_DISTILL_THRESHOLD
    DistillThreshold int `json:"distill_threshold"`

    // PreFilterBudget is the StructuralTrimmer byte budget applied before
    // LLM distillation (Stage 1 pre-filter). Reduces LLM input size.
    // Default: 131072 (128 KB — fits the 64K fast-tier context floor) | Env: TRUVAG3_RESULT_DISTILL_PREFILTER
    PreFilterBudget int `json:"prefilter_budget"`

    // TargetSize is the target output size for LLM distillation (Stage 2).
    // The LLM summarizes the pre-filtered result to approximately this size.
    // Default: 4096 (4 KB) | Env: TRUVAG3_RESULT_DISTILL_TARGET
    TargetSize int `json:"target_size"`

    // Model overrides the default AI model for distillation calls. Defaults to
    // the portable "fast" alias (ChainClient-safe; resolves to Haiku /
    // gpt-4.1-mini / gemini-flash-lite per provider). Empty string = use the
    // AIClient's default model.
    // Default: "fast" | Env: TRUVAG3_RESULT_DISTILL_MODEL
    Model string `json:"model,omitempty"`

    // CacheTTL is how long a distillation result stays cached (keyed by result
    // content + instruction + query + budget) — but only when a DigestCache is
    // supplied via deps.DistillCache; there is no cache by default (a nil cache is
    // a fail-open no-op). CacheTTL=0 does NOT reliably disable caching: a
    // Redis-backed cache treats 0 as "no expiration" (cached indefinitely). To
    // disable caching, don't supply a cache.
    // Go duration; env accepts positive values only.
    // Default: 5m | Env: TRUVAG3_RESULT_DISTILL_CACHE_TTL
    CacheTTL time.Duration `json:"cache_ttl,omitempty"`

    // CompactionDeadline bounds the wall-clock time a single compaction may
    // spend in the synthesis hot path. On timeout it fails open (single-call →
    // structural floor; map-reduce → completed chunks + a "partial" disclosure).
    // The programmatic zero value disables the deadline; the env var accepts
    // positive durations only. Keep it under the HTTP gateway timeout.
    // Default: 45s | Env: TRUVAG3_RESULT_DISTILL_DEADLINE
    CompactionDeadline time.Duration `json:"compaction_deadline,omitempty"`

    // ModelContextTokens is the usable context (tokens) of the compaction model.
    // Results estimated above this are chunked and map-reduced instead of sent
    // in one call (~525 KB at the default, using the ≈3.5 bytes/token counter).
    // Default: 150000 | Env: TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS
    ModelContextTokens int `json:"model_context_tokens,omitempty"`

    // MapConcurrency caps how many chunks are compacted concurrently in the
    // map-reduce path. <= 0 falls back to the default.
    // Default: 8 | Env: TRUVAG3_RESULT_DISTILL_MAP_CONCURRENCY
    MapConcurrency int `json:"map_concurrency,omitempty"`
}
```

**Example - Tune distillation (default-on; adjust individual fields):**
```go
config := orchestration.DefaultConfig()
// Distillation is already enabled with sensible defaults — mutate individual
// fields rather than reassigning the whole struct, which would zero the fields
// you omit: CompactionDeadline=0 disables the hot-path deadline, and
// ModelContextTokens=0 disables map-reduce routing. (CacheTTL=0 does NOT cleanly
// disable caching — see the CacheTTL note above; MapConcurrency=0 self-heals to
// the default.)
config.ResultDistill.DistillThreshold = 65536 // only distill results > 64 KB
config.ResultDistill.TargetSize = 2048        // tighter ~2 KB summaries
config.ResultDistill.Model = "fast"           // portable alias (ChainClient-safe)
// To opt out entirely:
// config.ResultDistill.Enabled = false
```

**Environment variable overrides (values shown are the defaults):**
```bash
export TRUVAG3_RESULT_DISTILL_ENABLED=true            # default-on; =false to opt out
export TRUVAG3_RESULT_DISTILL_THRESHOLD=16384         # min bytes to trigger (default 16 KB)
export TRUVAG3_RESULT_DISTILL_PREFILTER=131072        # Stage-1 pre-filter budget (default 128 KB)
export TRUVAG3_RESULT_DISTILL_TARGET=4096             # Stage-2 LLM output target (default 4 KB)
export TRUVAG3_RESULT_DISTILL_MODEL=fast              # portable alias (ChainClient-safe)
export TRUVAG3_RESULT_DISTILL_CACHE_TTL=5m            # distillation cache TTL
export TRUVAG3_RESULT_DISTILL_DEADLINE=45s            # hot-path compaction bound (positive durations only)
export TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS=150000   # above this, chunk → map-reduce
export TRUVAG3_RESULT_DISTILL_MAP_CONCURRENCY=8       # concurrent chunk distillations
```

**How the pipeline works:**
1. **Stage 1 — Structural Pre-Filter**: The `StructuralTrimmer` reduces the large result to `PreFilterBudget` bytes, preserving JSON structure and key data points.
2. **Stage 2 — LLM Distillation**: An LLM call summarizes the pre-filtered result to approximately `TargetSize` bytes, preserving the most relevant information for the user's query.
3. **Very large results — map-reduce**: when the result is estimated above `ModelContextTokens`, it is chunked and the chunks are compacted concurrently (`MapConcurrency` at a time), then reduced — instead of being sent in a single call.
4. **Bounded & (optionally) cached**: each compaction is bounded by `CompactionDeadline` (fail-open). Results are cached for `CacheTTL` **only when a `DigestCache` is supplied** via `deps.DistillCache` — there is no cache by default.
5. **Fallback**: if the LLM call fails (or the deadline trips), the Stage 1 pre-filtered structural result is used directly.

### ProcessRequestStreaming

Process requests with real-time streaming responses. Tokens are delivered as they're generated, enabling SSE/WebSocket chat interfaces and real-time UX.

```go
func (o *AIOrchestrator) ProcessRequestStreaming(
    ctx context.Context,
    query string,
    tools []*ServiceInfo,
    callback core.StreamCallback,
) (*StreamingOrchestratorResponse, error)
```

**StreamingOrchestratorResponse fields:**
```go
type StreamingOrchestratorResponse struct {
    OrchestratorResponse              // Embedded base response (see below)

    // Streaming-specific fields
    ChunksDelivered int               // Number of chunks delivered via callback
    StreamCompleted bool              // Whether streaming completed successfully
    PartialContent  bool              // True if response is partial due to error
    StepResults     []StepResult      // Detailed results from each execution step
    FinishReason    string            // Why streaming stopped ("stop", "length", "error")
}

type OrchestratorResponse struct {
    RequestID       string                    // Unique request identifier
    OriginalRequest string                    // The original user query
    Response        string                    // Complete response (accumulated from chunks)
    RoutingMode     RouterMode                // Routing mode used
    ExecutionTime   time.Duration             // Total execution time
    AgentsInvolved  []string                  // Tools/agents used during execution
    Metadata        map[string]interface{}    // Additional metadata
    Errors          []string                  // Any errors encountered
    Confidence      float64                   // Orchestration confidence score
    Steps           []StepResult              // Individual step results

    // Aggregated token usage across all LLM calls in this request
    Usage           *core.TokenUsage          // Total prompt + completion tokens
    UsageByPhase    map[string]core.TokenUsage // Breakdown by orchestration phase
}
```

Phase names in `UsageByPhase`: `planning`, `correction`, `synthesis`, `micro_resolution`, `schema_mapping`, `distillation`, `semantic_retry`, `error_analysis`, `tiered_selection`.

**Example - SSE Chat Endpoint:**
```go
func (h *ChatHandler) HandleStream(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    flusher := w.(http.Flusher)

    // Set SSE headers
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")

    var req ChatRequest
    json.NewDecoder(r.Body).Decode(&req)

    // Stream orchestration results
    result, err := h.orchestrator.ProcessRequestStreaming(ctx, req.Message, nil,
        func(chunk core.StreamChunk) error {
            if chunk.Content != "" {
                fmt.Fprintf(w, "data: %s\n\n", chunk.Content)
                flusher.Flush()
            }
            return nil
        },
    )

    if err != nil {
        fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
        return
    }

    // Send completion event
    fmt.Fprintf(w, "event: done\ndata: {\"request_id\": \"%s\"}\n\n", result.RequestID)
}
```

**Example - With Step Callbacks:**
```go
// Add per-request step callback for real-time tool progress
ctx = orchestration.WithStepCallback(ctx,
    func(stepIndex, totalSteps int, step orchestration.RoutingStep, result orchestration.StepResult) {
        // Send SSE event for each tool completion
        callback.SendStep(step.AgentName, result.Success, result.Duration.Milliseconds())
    },
)

result, err := orchestrator.ProcessRequestStreaming(ctx, query, nil,
    func(chunk core.StreamChunk) error {
        callback.SendChunk(chunk.Content)
        return nil
    },
)
```

For a complete implementation with conversation history, session management, and SSE events, see the [Chat Agent Implementation Guide](../memory-and-chat/CHAT_AGENT_GUIDE.md).

### Orchestration Strategies

Different strategies for different scales and use cases:

| Strategy | Use Case | Scale |
|----------|----------|-------|
| **Autonomous** | AI decides which services to use | Small-medium |
| **Directed** | Explicit service specification | Any scale |
| **Hybrid** | Mix of AI and rules | Medium-large |

**Example - Scaling Orchestration:**
```go
// Small scale (< 20 tools) - Let AI figure it out
config := &orchestration.OrchestratorConfig{
    RoutingMode:       orchestration.ModeAutonomous,
    SynthesisStrategy: orchestration.StrategyLLM,
}

// Medium scale (20-100 tools) - Use tiered resolution (default)
// Reduces token usage by 50-75% via two-phase tool selection
config := &orchestration.OrchestratorConfig{
    RoutingMode:            orchestration.ModeAutonomous,
    EnableTieredResolution: true,  // Default: true
    TieredResolution: orchestration.TieredCapabilityConfig{
        MinToolsForTiering: 20,    // Trigger threshold
        SelectionMaxTokens: 2000,  // Selection output tokens
    },
    PromptConfig: orchestration.PromptConfig{
        SystemInstructions: "You are a helpful assistant specializing in travel planning.",
    },
}

// Large scale (100+ services) - Use capability service
config := &orchestration.OrchestratorConfig{
    RoutingMode:            orchestration.ModeHybrid,
    CapabilityProviderType: "service",
    CapabilityServiceURL:   "http://capability-registry:8080",
}

// Capability service indexes all available capabilities
// and provides fast, structured search without hitting discovery
```

### OAuth Bearer Token Propagation

The orchestrator propagates OAuth Bearer tokens on all outbound HTTP calls to tool/agent endpoints. Two mechanisms are supported:

**OrchestratorConfig field** (Scenario 2: machine-to-machine):
```go
config := &orchestration.OrchestratorConfig{
    OAuthToken: "eyJhbGciOiJSUzI1NiIs...",  // or via TRUVAG3_OAUTH_TOKEN env var
}
```

**Context helpers** (Scenario 1: user token pass-through):
```go
// Attach per-request Bearer token
ctx = orchestration.WithOAuthToken(ctx, token)

// Retrieve token from context
token := orchestration.GetOAuthToken(ctx)
```

**Resolution order**: context token > config token > none. When set, outbound requests include `Authorization: Bearer <token>`.

### Custom Header Propagation

The orchestrator supports injecting custom headers into all outbound HTTP calls to tool/agent endpoints. This is useful for correlation IDs, tenant routing, and audit logging across non-OTel services.

**OrchestratorConfig field** (instance-level defaults):
```go
config := &orchestration.OrchestratorConfig{
    PropagatedHeaders: map[string]string{
        "X-Tenant-ID":      tenantID,
        "X-Correlation-ID": correlationID,
    },
}
```

**Context helpers** (per-request overrides):
```go
// Attach multiple headers to context
ctx = orchestration.WithPropagatedHeaders(ctx, map[string]string{
    "X-Tenant-ID":      r.Header.Get("X-Tenant-ID"),
    "X-Correlation-ID": r.Header.Get("X-Correlation-ID"),
})

// Add a single header to context
ctx = orchestration.AddPropagatedHeader(ctx, "X-Request-Source", "chat-ui")

// Retrieve headers from context
headers := orchestration.GetPropagatedHeaders(ctx)
```

**Runtime setter** (update without restart):
```go
orch.SetPropagatedHeaders(map[string]string{"X-Tenant-ID": newTenantID})
```

**Resolution order**: context headers > config headers > none. Context headers override config headers on key conflict; config-only keys are preserved.

**Reserved header protection**: `Authorization`, `Content-Type`, and `X-TruvaG3-*` headers cannot be overridden by propagated headers. OAuth is managed by dedicated logic, Content-Type is set by the framework, and framework headers (`X-TruvaG3-Request-ID`, `X-TruvaG3-Step-ID`) are set last.

No env var is provided for `PropagatedHeaders` (maps don't map to a single env var). Set programmatically via config or `SetPropagatedHeaders()`.

### LLM Debug Store

Captures complete LLM request/response payloads for production debugging. Unlike Jaeger spans which truncate large payloads, the debug store preserves full prompts and responses.

#### LLMDebugStore Interface

```go
type LLMDebugStore interface {
    // RecordInteraction appends an LLM interaction to the debug record
    RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error

    // GetRecord retrieves the complete debug record for a request
    GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error)

    // SetMetadata adds metadata to an existing record (for investigation notes)
    SetMetadata(ctx context.Context, requestID string, key, value string) error

    // ExtendTTL extends retention for important records
    ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error

    // ListRecent returns recent records for UI listing (newest first)
    ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error)
}
```

#### LLMInteraction Type

```go
type LLMInteraction struct {
    // Identity
    Type            string    // Recording site (plan_generation, synthesis, agent_llm_call, etc.)
    SourceComponent string    // Component that made this call (e.g., "research-assistant"); empty for orchestrator calls
    CallDescription string    // Human-readable label (e.g., "Tool selection for research")
    StepID          string    // Orchestration step ID (for step-specific calls)
    Timestamp       time.Time // When the interaction started
    DurationMs      int64     // Call duration in milliseconds

    // Request fields
    Prompt       string  // Complete prompt sent to LLM
    SystemPrompt string  // System prompt if used
    Temperature  float64 // Temperature setting
    MaxTokens    int     // Max tokens setting
    Model        string  // Model identifier (e.g., "gpt-4o")
    Provider     string  // Provider (openai, anthropic, gemini, bedrock)

    // Response fields
    Response         string // Complete response from LLM
    PromptTokens     int    // Prompt token count
    CompletionTokens int    // Completion token count
    TotalTokens      int    // Total token count

    // Status fields
    Success bool   // Whether the call succeeded
    Error   string // Error message if failed
    Attempt int    // Attempt number (for retries)
}
```

#### Factory Options

```go
// Orchestrator options (for CreateOrchestratorWithOptions)
orchestration.WithLLMDebug(true)                    // Enable debug capture
orchestration.WithLLMDebugStore(customStore)        // Inject custom store
orchestration.WithLLMDebugTTL(48 * time.Hour)       // Custom TTL for success
orchestration.WithLLMDebugErrorTTL(14 * 24 * time.Hour) // Custom TTL for errors

// RedisLLMDebugStore options (for NewRedisLLMDebugStore)
orchestration.WithDebugRedisURL("redis://localhost:6379")
orchestration.WithDebugRedisDB(7)
orchestration.WithDebugLogger(logger)
orchestration.WithDebugCircuitBreaker(cb)
orchestration.WithDebugTTL(24 * time.Hour)
orchestration.WithDebugErrorTTL(168 * time.Hour)
```

**Example - Enable Debug Capture:**
```go
deps := orchestration.OrchestratorDependencies{
    Discovery: discovery,
    AIClient:  aiClient,
}

// Enable debug capture
orchestrator, err := orchestration.CreateOrchestratorWithOptions(deps,
    orchestration.WithLLMDebug(true),
)

// Query debug records (from your application's API)
record, _ := orchestrator.GetLLMDebugStore().GetRecord(ctx, requestID)
for _, interaction := range record.Interactions {
    fmt.Printf("Site: %s, Model: %s, Provider: %s\n",
        interaction.Type, interaction.Model, interaction.Provider)
    fmt.Printf("Prompt: %s\n", interaction.Prompt[:100])  // First 100 chars
}
```

#### LLMCallRecorderAdapter

Bridges `orchestration.LLMDebugStore` to `telemetry.LLMCallRecorder`. Use this when the orchestrator already has a debug store and you want agents to write to the same store without creating a separate Redis connection.

```go
func NewLLMCallRecorderAdapter(store LLMDebugStore) telemetry.LLMCallRecorder
```

**Example:**
```go
debugStore, _ := orchestration.NewRedisLLMDebugStore()
recorder := orchestration.NewLLMCallRecorderAdapter(debugStore)
aiClient := ai.NewInstrumentedClient(baseClient, recorder)
```

#### LLMDebugRecordSummary

Lightweight summary for listing records. Includes source attribution.

```go
type LLMDebugRecordSummary struct {
    RequestID         string    `json:"request_id"`
    OriginalRequestID string    `json:"original_request_id,omitempty"`
    TraceID           string    `json:"trace_id"`
    CreatedAt         time.Time `json:"created_at"`
    InteractionCount  int       `json:"interaction_count"`
    TotalTokens       int       `json:"total_tokens"`
    HasErrors         bool      `json:"has_errors"`
    // SourceComponents lists unique agent names that made LLM calls in this record.
    // Empty for orchestrator-only records.
    SourceComponents []string   `json:"source_components,omitempty"`
}
```

**Environment Variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_LLM_DEBUG_ENABLED` | `false` | Enable debug capture |
| `TRUVAG3_LLM_DEBUG_TTL` | `24h` | TTL for successful records |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | `168h` | TTL for error records (7 days) |
| `TRUVAG3_LLM_DEBUG_REDIS_DB` | `7` | Redis database index |

### Human-in-the-Loop (HITL)

Add human oversight to AI orchestration. HITL pauses execution at critical points (checkpoints) and waits for human approval before proceeding.

For getting started and implementation patterns, see the [Human-in-the-Loop User Guide](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md).

#### Core Interfaces

HITL uses small, focused interfaces following Go idioms:

```go
// InterruptPolicy composes all approval behaviors
type InterruptPolicy interface {
    PlanApprover      // ShouldApprovePlan(ctx, plan) (*InterruptDecision, error)
    StepApprover      // ShouldApproveBeforeStep/AfterStep(ctx, step, ...) (*InterruptDecision, error)
    ErrorEscalator    // ShouldEscalateError(ctx, step, err, attempts) (*InterruptDecision, error)
}

// CheckpointStore persists workflow state for interrupt/resume
type CheckpointStore interface {
    SaveCheckpoint(ctx context.Context, checkpoint *ExecutionCheckpoint) error
    LoadCheckpoint(ctx context.Context, checkpointID string) (*ExecutionCheckpoint, error)
    UpdateCheckpointStatus(ctx context.Context, checkpointID string, status CheckpointStatus) error
    ListPendingCheckpoints(ctx context.Context, filter CheckpointFilter) ([]*ExecutionCheckpoint, error)
    DeleteCheckpoint(ctx context.Context, checkpointID string) error
    StartExpiryProcessor(ctx context.Context, config ExpiryProcessorConfig) error
    StopExpiryProcessor(ctx context.Context) error
    SetExpiryCallback(callback ExpiryCallback) error
}

// InterruptController coordinates all HITL functionality
type InterruptController interface {
    SetPolicy(policy InterruptPolicy)
    SetHandler(handler InterruptHandler)
    SetCheckpointStore(store CheckpointStore)
    CheckPlanApproval(ctx context.Context, plan *RoutingPlan) (*ExecutionCheckpoint, error)
    CheckBeforeStep(ctx context.Context, step RoutingStep, plan *RoutingPlan) (*ExecutionCheckpoint, error)
    CheckAfterStep(ctx context.Context, step RoutingStep, result *StepResult) (*ExecutionCheckpoint, error)
    CheckOnError(ctx context.Context, step RoutingStep, err error, attempts int) (*ExecutionCheckpoint, error)
    ProcessCommand(ctx context.Context, command *Command) (*ResumeResult, error)
    ResumeExecution(ctx context.Context, checkpointID string) (*ExecutionResult, error)
    UpdateCheckpointProgress(ctx context.Context, checkpointID string, completedSteps []StepResult) error
}

// CommandStore provides distributed command delivery
type CommandStore interface {
    PublishCommand(ctx context.Context, command *Command) error
    SubscribeCommand(ctx context.Context, checkpointID string) (<-chan *Command, func(), error)
    Close() error
}

// InterruptHandler manages notification and response collection
type InterruptHandler interface {
    NotifyInterrupt(ctx context.Context, checkpoint *ExecutionCheckpoint) error
    WaitForCommand(ctx context.Context, checkpointID string, timeout time.Duration) (*Command, error)
    SubmitCommand(ctx context.Context, command *Command) error
}
```

#### Constructors

**InterruptController:**
```go
func NewInterruptController(
    policy InterruptPolicy,
    store CheckpointStore,
    handler InterruptHandler,
    opts ...InterruptControllerOption,
) *DefaultInterruptController

// Options
func WithControllerLogger(logger core.Logger) InterruptControllerOption
func WithControllerTelemetry(telemetry core.Telemetry) InterruptControllerOption
```

**Policy:**
```go
func NewRuleBasedPolicy(config HITLConfig, opts ...PolicyOption) *RuleBasedPolicy
func NewNoOpPolicy() *NoOpPolicy  // For testing or HITL-disabled scenarios

// Options
func WithPolicyLogger(logger core.Logger) PolicyOption
```

**CheckpointStore:**
```go
func NewRedisCheckpointStore(opts ...interface{}) (*RedisCheckpointStore, error)

// Options
func WithCheckpointRedisURL(url string) RedisCheckpointStoreOption
func WithCheckpointRedisDB(db int) RedisCheckpointStoreOption
func WithCheckpointKeyPrefix(prefix string) RedisCheckpointStoreOption
func WithCheckpointTTL(ttl time.Duration) RedisCheckpointStoreOption
func WithInstanceID(id string) RedisCheckpointStoreOption  // For distributed claim coordination
func WithCheckpointStoreLogger(logger core.Logger) RedisCheckpointStoreOption
func WithCheckpointStoreTelemetry(telemetry core.Telemetry) RedisCheckpointStoreOption
```

**CommandStore:**
```go
func NewRedisCommandStore(opts ...RedisCommandStoreOption) (*RedisCommandStore, error)

// Options
func WithCommandStoreRedisURL(url string) RedisCommandStoreOption
func WithCommandStoreRedisDB(db int) RedisCommandStoreOption
func WithCommandStoreKeyPrefix(prefix string) RedisCommandStoreOption
func WithCommandStoreLogger(logger core.Logger) RedisCommandStoreOption
func WithCommandStoreTelemetry(t core.Telemetry) RedisCommandStoreOption
```

**InterruptHandler:**
```go
func NewWebhookInterruptHandler(webhookURL string, commandStore CommandStore, opts ...WebhookHandlerOption) *WebhookInterruptHandler
func NewNoOpInterruptHandler() *NoOpInterruptHandler  // For testing

// Options
func WithHandlerCircuitBreaker(cb core.CircuitBreaker) WebhookHandlerOption
func WithHandlerLogger(logger core.Logger) WebhookHandlerOption
func WithHandlerTelemetry(telemetry core.Telemetry) WebhookHandlerOption
```

**HITLHandler (HTTP API):**
```go
func NewHITLHandler(controller InterruptController, store CheckpointStore, opts ...HITLHandlerOption) *HITLHandler

// Options
func WithHITLHandlerLogger(logger core.Logger) HITLHandlerOption
func WithHITLHandlerTelemetry(t core.Telemetry) HITLHandlerOption

// Register all routes
func (h *HITLHandler) RegisterRoutes(mux *http.ServeMux)
```

**Orchestrator Integration:**
```go
// Wire HITL into orchestrator after creation
func (o *AIOrchestrator) SetInterruptController(controller InterruptController)
func (o *AIOrchestrator) GetInterruptController() InterruptController
```

#### HTTP API Endpoints

The framework provides these endpoints via `HITLHandler.RegisterRoutes`:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `POST /hitl/command` | POST | Submit approval command (approve, reject, edit, skip, abort, retry) |
| `POST /hitl/resume/{checkpoint_id}` | POST | Resume workflow execution after approval |
| `GET /hitl/checkpoints` | GET | List pending checkpoints |
| `GET /hitl/checkpoints/{id}` | GET | Get checkpoint details |

**Submit Command:**
```bash
curl -X POST http://agent:8080/hitl/command \
  -H "Content-Type: application/json" \
  -d '{"checkpoint_id": "cp-abc123", "type": "approve"}'
```

**Response:**
```json
{
  "checkpoint_id": "cp-abc123",
  "action": "approve",
  "should_resume": true
}
```

#### Interrupt Points

| Interrupt Point | Trigger | Use Case |
|-----------------|---------|----------|
| `plan_generated` | After AI creates execution plan | Review full plan before any execution |
| `before_step` | Before executing each tool/agent | Approve individual high-risk operations |
| `on_error` | When a step fails and is recoverable | Human decides whether to retry, skip, or abort |
| `after_step` | After step completes (reserved) | Output validation before proceeding |
| `context_gathering` | During context collection (reserved) | Approve data access requests |

#### Command Types by Interrupt Point

| Command | Description | `plan_generated` | `before_step` | `after_step` | `on_error` |
|---------|-------------|------------------|---------------|--------------|------------|
| `approve` | Proceed with execution | Yes | Yes | Yes | No |
| `reject` | Cancel and stop execution | Yes | Yes | Yes | No |
| `edit` | Modify parameters and proceed | Yes | Yes | Yes | No |
| `skip` | Skip this step, continue with next | No | Yes | Yes | Yes |
| `abort` | Abort entire execution | Yes | Yes | Yes | Yes |
| `retry` | Retry the failed step | No | Yes | No | Yes |
| `respond` | Provide requested information | No | No | No | No |

#### Key Types

```go
// ExecutionCheckpoint contains all state needed to resume execution
type ExecutionCheckpoint struct {
    CheckpointID       string                 `json:"checkpoint_id"`
    RequestID          string                 `json:"request_id"`
    OriginalRequestID  string                 `json:"original_request_id,omitempty"` // For trace correlation
    InterruptPoint     InterruptPoint         `json:"interrupt_point"`
    Decision           *InterruptDecision     `json:"decision"`
    Plan               *RoutingPlan           `json:"plan"`
    CompletedSteps     []StepResult           `json:"completed_steps"`
    CurrentStep        *RoutingStep           `json:"current_step,omitempty"`
    CurrentStepResult  *StepResult            `json:"current_step_result,omitempty"`
    StepResults        map[string]*StepResult `json:"step_results"`
    ResolvedParameters map[string]interface{} `json:"resolved_parameters,omitempty"`
    OriginalRequest    string                 `json:"original_request"`
    UserContext        map[string]interface{} `json:"user_context,omitempty"`
    RequestMode        RequestMode            `json:"request_mode,omitempty"`
    CreatedAt          time.Time              `json:"created_at"`
    ExpiresAt          time.Time              `json:"expires_at"`
    Status             CheckpointStatus       `json:"status"`
}

// InterruptPoint enum
const (
    InterruptPointPlanGenerated    InterruptPoint = "plan_generated"
    InterruptPointBeforeStep       InterruptPoint = "before_step"
    InterruptPointAfterStep        InterruptPoint = "after_step"
    InterruptPointOnError          InterruptPoint = "on_error"
    InterruptPointContextGathering InterruptPoint = "context_gathering"
)

// CheckpointStatus enum - includes human-initiated and expiry statuses
const (
    // Human-initiated
    CheckpointStatusPending   CheckpointStatus = "pending"
    CheckpointStatusApproved  CheckpointStatus = "approved"
    CheckpointStatusRejected  CheckpointStatus = "rejected"
    CheckpointStatusEdited    CheckpointStatus = "edited"
    CheckpointStatusCompleted CheckpointStatus = "completed"
    CheckpointStatusAborted   CheckpointStatus = "aborted"

    // Streaming expiry (implicit deny)
    CheckpointStatusExpired CheckpointStatus = "expired"

    // Non-streaming expiry (policy-driven)
    CheckpointStatusExpiredApproved CheckpointStatus = "expired_approved"
    CheckpointStatusExpiredRejected CheckpointStatus = "expired_rejected"
    CheckpointStatusExpiredAborted  CheckpointStatus = "expired_aborted"
)

// CommandType enum
const (
    CommandApprove CommandType = "approve"
    CommandEdit    CommandType = "edit"
    CommandReject  CommandType = "reject"
    CommandSkip    CommandType = "skip"
    CommandAbort   CommandType = "abort"
    CommandRetry   CommandType = "retry"
    CommandRespond CommandType = "respond"
)

// RequestMode determines expiry behavior
const (
    RequestModeStreaming    RequestMode = "streaming"     // Implicit deny on expiry
    RequestModeNonStreaming RequestMode = "non_streaming" // Apply DefaultAction on expiry
)

// Command represents a human decision
type Command struct {
    CommandID    string                 `json:"command_id"`
    CheckpointID string                 `json:"checkpoint_id"`
    Type         CommandType            `json:"type"`
    EditedPlan   *RoutingPlan           `json:"edited_plan,omitempty"`
    EditedStep   *RoutingStep           `json:"edited_step,omitempty"`
    EditedParams map[string]interface{} `json:"edited_params,omitempty"`
    Feedback     string                 `json:"feedback,omitempty"`
    Response     string                 `json:"response,omitempty"`
    UserID       string                 `json:"user_id,omitempty"`
    Timestamp    time.Time              `json:"timestamp"`
}

// ResumeResult contains the outcome of processing a command
type ResumeResult struct {
    CheckpointID string       `json:"checkpoint_id"`
    Action       CommandType  `json:"action"`
    ShouldResume bool         `json:"should_resume"`
    ModifiedPlan *RoutingPlan `json:"modified_plan,omitempty"`
    SkipStep     bool         `json:"skip_step,omitempty"`
    Abort        bool         `json:"abort,omitempty"`
    Feedback     string       `json:"feedback,omitempty"`
}
```

#### Context Helpers

**Request Context:**
```go
func WithRequestID(ctx context.Context, requestID string) context.Context
func GetRequestID(ctx context.Context) string

func WithRequestMode(ctx context.Context, mode RequestMode) context.Context
func GetRequestMode(ctx context.Context) RequestMode

func WithMetadata(ctx context.Context, metadata map[string]interface{}) context.Context
func GetMetadata(ctx context.Context) map[string]interface{}
```

**Resume Context:**
```go
// Recommended: one-call context setup. The returned func() releases any
// resources held by the resume context — defer it immediately.
func BuildResumeContext(ctx context.Context, checkpoint *ExecutionCheckpoint) (context.Context, func(), error)

// Manual context building (for more control)
func WithResumeMode(ctx context.Context, checkpointID string) context.Context
func WithPlanOverride(ctx context.Context, plan *RoutingPlan) context.Context
func WithCompletedSteps(ctx context.Context, results map[string]*StepResult) context.Context
func WithPreResolvedParams(ctx context.Context, params map[string]interface{}, stepID string) context.Context
```

> **Iterative planning note:** During multi-phase execution, `WithCompletedSteps` populates prior-phase results into context. The executor uses these for **cross-phase template resolution** (e.g., `{{step-1.response.data}}` in Phase 2 referencing Phase 1 results) while the loop termination logic only counts steps belonging to the current phase's plan.

#### Error Handling

```go
// Check if execution was interrupted (not a failure)
if orchestration.IsInterrupted(err) {
    checkpoint := orchestration.GetCheckpoint(err)
    checkpointID := orchestration.GetCheckpointID(err)
    // Handle checkpoint - workflow is paused, not failed
}

// Error type checking
if orchestration.IsCheckpointNotFound(err) { /* Checkpoint doesn't exist */ }
if orchestration.IsCheckpointExpired(err) { /* Timeout reached */ }
if orchestration.IsInvalidCommand(err) { /* Invalid command for state */ }
if orchestration.IsHITLDisabled(err) { /* HITL not enabled */ }
```

#### Status Helpers

```go
// Check if checkpoint can be resumed
if orchestration.IsResumableStatus(checkpoint.Status) {
    // Status is: approved, edited, or expired_approved
}

// Check if checkpoint is in terminal state
if orchestration.IsTerminalStatus(checkpoint.Status) {
    // Status is: completed, rejected, aborted, expired, expired_rejected, expired_aborted
}

// Check if checkpoint is awaiting response
if orchestration.IsPendingStatus(checkpoint.Status) {
    // Status is: pending
}
```

#### Configuration

```go
type HITLConfig struct {
    Enabled                   bool          `json:"enabled"`
    RequirePlanApproval       bool          `json:"require_plan_approval"`
    SensitiveCapabilities     []string      `json:"sensitive_capabilities"`      // Plan + Step approval
    SensitiveAgents           []string      `json:"sensitive_agents"`            // Plan + Step approval
    StepSensitiveCapabilities []string      `json:"step_sensitive_capabilities"` // Step-only approval
    StepSensitiveAgents       []string      `json:"step_sensitive_agents"`       // Step-only approval
    EscalateAfterRetries      int           `json:"escalate_after_retries"`
    DefaultTimeout            time.Duration `json:"default_timeout"`
    DefaultAction             CommandType   `json:"default_action"`
    CheckpointTTL             time.Duration `json:"checkpoint_ttl"`
    KeyPrefix                 string        `json:"key_prefix"`
    ExpiryProcessor           ExpiryProcessorConfig `json:"expiry_processor"`
}

// Create with smart defaults
func DefaultHITLConfig() HITLConfig
func NewHITLConfig(opts ...HITLOption) HITLConfig

// Options
func WithExpiryProcessor(config ExpiryProcessorConfig) HITLOption
```

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `TRUVAG3_HITL_ENABLED` | `false` | Enable HITL system |
| `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | `false` | Pause after every plan generation |
| `TRUVAG3_HITL_SENSITIVE_CAPABILITIES` | `` | Capabilities requiring plan + step approval |
| `TRUVAG3_HITL_SENSITIVE_AGENTS` | `` | Agents requiring plan + step approval |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | `` | Capabilities requiring step-only approval |
| `TRUVAG3_HITL_STEP_SENSITIVE_AGENTS` | `` | Agents requiring step-only approval |
| `TRUVAG3_HITL_ESCALATE_AFTER_RETRIES` | `3` | Escalate to human after N retry failures |
| `TRUVAG3_HITL_DEFAULT_TIMEOUT` | `5m` | Checkpoint expiration time |
| `TRUVAG3_HITL_DEFAULT_ACTION` | `reject` | Action on timeout (approve, reject, abort) |
| `TRUVAG3_HITL_KEY_PREFIX` | `truvag3:hitl` | Redis key prefix |

#### Expiry Processor Configuration

The expiry processor handles checkpoint timeouts in the background:

```go
type ExpiryProcessorConfig struct {
    Enabled           bool              // Run processor (default: true)
    ScanInterval      time.Duration     // Scan frequency (default: 10s)
    BatchSize         int               // Max per scan (default: 100)
    DeliverySemantics DeliverySemantics // Callback timing
}

const (
    DeliveryAtMostOnce  DeliverySemantics = "at_most_once"  // Default, safe
    DeliveryAtLeastOnce DeliverySemantics = "at_least_once" // Callback must be idempotent
)

type ExpiryCallback func(ctx context.Context, checkpoint *ExecutionCheckpoint, appliedAction CommandType)
```

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `TRUVAG3_HITL_EXPIRY_ENABLED` | `true` | Enable expiry processor |
| `TRUVAG3_HITL_EXPIRY_INTERVAL` | `10s` | Scan interval |
| `TRUVAG3_HITL_EXPIRY_BATCH_SIZE` | `100` | Max checkpoints per scan |
| `TRUVAG3_HITL_EXPIRY_DELIVERY` | `at_least_once` | Delivery semantics |
| `TRUVAG3_HITL_STREAMING_EXPIRY` | `implicit_deny` | Streaming request expiry behavior |
| `TRUVAG3_HITL_NON_STREAMING_EXPIRY` | `apply_default` | Non-streaming request expiry behavior |

#### Metrics

HITL emits these Prometheus metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `orchestration.hitl.checkpoint_created_total` | Counter | interrupt_point, reason | Checkpoints created |
| `orchestration.hitl.checkpoint_status_total` | Counter | from_status, to_status | Status transitions |
| `orchestration.hitl.command_processed_total` | Counter | command_type, success | Commands processed |
| `orchestration.hitl.checkpoint_expired_total` | Counter | action, request_mode, interrupt_point | Expired checkpoints |
| `orchestration.hitl.approval_latency_seconds` | Histogram | command_type | Human response time |
| `orchestration.hitl.expiry_scan_duration_seconds` | Histogram | - | Expiry scan duration |

---

## Common Patterns

### Production Service Template

Complete template for production-ready services:

```go
package main

import (
    "context"
    "errors"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/truvaagents/truva-g3/core"
)

func main() {
    // signal.NotifyContext returns a context that is cancelled on SIGINT/SIGTERM.
    // Framework.Run() blocks until this context is cancelled, then drains
    // registered runnables within TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT.
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    // Create component
    agent := core.NewBaseAgent("production-service")

    // Register capabilities
    agent.RegisterCapability(core.Capability{
        Name:        "process",
        Description: "Process data",
        Handler:     handleProcess,
    })

    // Create framework with production settings
    framework, err := core.NewFramework(agent,
        // Discovery
        core.WithRedisURL(os.Getenv("REDIS_URL")),

        // Observability
        core.WithTelemetry(true, os.Getenv("OTEL_ENDPOINT")),
        core.WithLogLevel("info"),

        // Networking
        core.WithPort(8080),
        core.WithCORSDefaults(),

        // Resilience
        core.WithCircuitBreaker(5, 30*time.Second),
    )
    if err != nil {
        log.Fatalf("Failed to create framework: %v", err)
    }

    // Run blocks until ctx is cancelled, then performs the graceful drain.
    if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
        log.Fatalf("Framework error: %v", err)
    }
}
```

### Multi-Provider AI Strategy

Use different AI providers for different tasks:

```go
type SmartAI struct {
    fast      core.AIClient  // Groq for speed
    accurate  core.AIClient  // Claude for accuracy
    creative  core.AIClient  // GPT-4 for creativity
}

func NewSmartAI() *SmartAI {
    return &SmartAI{
        fast: ai.MustNewClient(
            ai.WithProvider("groq"),
            ai.WithModel("llama3-8b"),
        ),
        accurate: ai.MustNewClient(
            ai.WithProvider("anthropic"),
            ai.WithModel("claude-3-opus-20240229"),
        ),
        creative: ai.MustNewClient(
            ai.WithProvider("openai"),
            ai.WithModel("gpt-4"),
        ),
    }
}

func (s *SmartAI) QuickAnswer(question string) (string, error) {
    // Use fast model for simple queries
    resp, err := s.fast.GenerateResponse(ctx, question, &core.AIOptions{
        MaxTokens:   200,
        Temperature: 0.3,
    })
    return resp.Content, err
}

func (s *SmartAI) AnalyzeCode(code string) (string, error) {
    // Use accurate model for code analysis
    resp, err := s.accurate.GenerateResponse(ctx,
        fmt.Sprintf("Analyze this code for bugs and improvements:\n%s", code),
        &core.AIOptions{
            Temperature: 0.1,  // Very low for technical accuracy
            MaxTokens:   2000,
        },
    )
    return resp.Content, err
}
```

### Service Mesh Pattern

Build a resilient service mesh with discovery and circuit breakers:

```go
type ServiceMesh struct {
    discovery core.Discovery
    breakers  map[string]*resilience.CircuitBreaker
    mu        sync.RWMutex
}

func (sm *ServiceMesh) Call(ctx context.Context, capability string, request interface{}) (interface{}, error) {
    // Discover service
    services, err := sm.discovery.FindByCapability(ctx, capability)
    if err != nil || len(services) == 0 {
        return nil, fmt.Errorf("no service found for %s", capability)
    }

    service := services[0]  // TODO: Add load balancing

    // Get or create circuit breaker for this service
    breaker := sm.getBreaker(service.ID)

    var response interface{}
    err = breaker.Execute(ctx, func() error {
        // Call the service
        return sm.callService(service, request, &response)
    })

    return response, err
}

func (sm *ServiceMesh) getBreaker(serviceID string) *resilience.CircuitBreaker {
    sm.mu.RLock()
    if breaker, exists := sm.breakers[serviceID]; exists {
        sm.mu.RUnlock()
        return breaker
    }
    sm.mu.RUnlock()

    // Create new breaker
    sm.mu.Lock()
    defer sm.mu.Unlock()

    breaker := resilience.NewCircuitBreakerLegacy(5, 30*time.Second)
    sm.breakers[serviceID] = breaker
    return breaker
}
```

---

## Best Practices

### 1. Always Use the Framework

The framework handles complex setup, so you don't have to:

```go
// ❌ Don't do this
agent := core.NewBaseAgent("my-agent")
agent.Initialize(ctx)
agent.Start(ctx, 8080)

// ✅ Do this instead
framework, _ := core.NewFramework(agent, options...)
framework.Run(ctx)
```

### 2. Configure via Environment

Follow 12-factor app principles:

```go
// ✅ Good - configuration from environment
framework, _ := core.NewFramework(agent,
    core.WithRedisURL(os.Getenv("REDIS_URL")),
    core.WithPort(getPortFromEnv()),
)

// ❌ Bad - hardcoded values
framework, _ := core.NewFramework(agent,
    core.WithRedisURL("redis://localhost:6379"),
    core.WithPort(8080),
)
```

### 3. Use Structured Logging

Always log with context:

```go
// ✅ Good - structured with context
logger.Info("Processing request", map[string]interface{}{
    "request_id": req.ID,
    "user_id":    req.UserID,
    "action":     req.Action,
})

// ❌ Bad - unstructured string
log.Printf("Processing request %s for user %s", req.ID, req.UserID)
```

### 4. Handle Circuit Breaker States

Always check for circuit breaker errors:

```go
err := breaker.Execute(ctx, func() error {
    return callService()
})

if errors.Is(err, core.ErrCircuitBreakerOpen) {
    // Service is down, use fallback
    return useFallback()
}

if err != nil {
    // Other error, handle accordingly
    return err
}
```

### 5. Use Context for Cancellation

Always respect context cancellation:

```go
func LongRunningOperation(ctx context.Context) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()  // Respect cancellation
        default:
            // Do work
        }
    }
}
```

---

## Error Handling

TruvaG3 uses typed errors for better error handling:

```go
// Defined in core/errors.go — match against them with errors.Is(err, core.Err...)
var (
    ErrCircuitBreakerOpen   = errors.New("circuit breaker open")
    ErrMaxRetriesExceeded   = errors.New("maximum retries exceeded")
    ErrServiceNotFound      = errors.New("service not found")
    ErrInvalidConfiguration = errors.New("invalid configuration")
)
```

**Example - Comprehensive Error Handling:**
```go
func HandleRequest(ctx context.Context, req Request) error {
    err := processWithResilience(ctx, req)

    switch {
    case errors.Is(err, core.ErrCircuitBreakerOpen):
        // Service down, use fallback
        return handleWithFallback(ctx, req)

    case errors.Is(err, core.ErrMaxRetriesExceeded):
        // Temporary failure, queue for later
        return queueForRetry(req)

    case errors.Is(err, context.DeadlineExceeded):
        // Timeout, return error to client
        return fmt.Errorf("request timeout: %w", err)

    case err != nil:
        // Unexpected error
        logger.Error("Unexpected error", map[string]interface{}{
            "error": err.Error(),
            "request_id": req.ID,
        })
        return fmt.Errorf("internal error: %w", err)
    }

    return nil
}
```

---

## Environment Variables

TruvaG3 supports configuration through environment variables:

### Core Configuration
- `TRUVAG3_AGENT_NAME` - Component name
- `TRUVAG3_PORT` - HTTP port (default: 8080)
- `REDIS_URL` - Redis connection URL
- `TRUVAG3_LOG_LEVEL` - Logging level (error/warn/info/debug)
- `TRUVAG3_LOG_FORMAT` - Log format (json/text)
- `TRUVAG3_DEV_MODE` - Development mode (true/false)

### Discovery Retry Configuration
- `TRUVAG3_DISCOVERY_RETRY` - Enable background Redis retry on connection failure (true/false, default: false)
- `TRUVAG3_DISCOVERY_RETRY_INTERVAL` - Initial retry interval (e.g., "30s", "1m", default: 30s)

### Schema Discovery & Validation
- `TRUVAG3_VALIDATE_PAYLOADS` - Enable Phase 3 schema validation (true/false, default: false)

### AI Configuration
- `OPENAI_API_KEY` - OpenAI API key
- `ANTHROPIC_API_KEY` - Anthropic API key
- `GEMINI_API_KEY` - Google Gemini API key
- `GROQ_API_KEY` - Groq API key
- `TOGETHER_API_KEY` - Together AI API key
- `DEEPSEEK_API_KEY` - DeepSeek API key
- `XAI_API_KEY` - xAI Grok API key
- `QWEN_API_KEY` - Qwen API key

### OpenAI-Compatible Provider URLs (Optional)
- `GROQ_BASE_URL` - Override Groq endpoint (default: https://api.groq.com/openai/v1)
- `TOGETHER_BASE_URL` - Override Together endpoint (default: https://api.together.xyz/v1)
- `DEEPSEEK_BASE_URL` - Override DeepSeek endpoint (default: https://api.deepseek.com/v1)
- `XAI_BASE_URL` - Override xAI endpoint (default: https://api.x.ai/v1)
- `QWEN_BASE_URL` - Override Qwen endpoint (default: https://dashscope.aliyuncs.com/compatible-mode/v1)

### Kubernetes Configuration
- `TRUVAG3_K8S_SERVICE_NAME` - Kubernetes service name
- `TRUVAG3_K8S_SERVICE_PORT` - Kubernetes service port
- `TRUVAG3_K8S_POD_IP` - Pod IP address
- `HOSTNAME` - Pod hostname

### Telemetry Configuration
- `OTEL_EXPORTER_OTLP_ENDPOINT` - OpenTelemetry endpoint
- `OTEL_SERVICE_NAME` - Service name for telemetry

### Shared Memory Configuration

**Episodic Memory & Coordination:**
- `TRUVAG3_AGENT_DOMAIN` - Groups agents for memory scoping (default: `"default"`)
- `TRUVAG3_SHARED_MEMORY_PROVIDER` - Storage provider: `redis` or `noop` (default: `"noop"`)
- `TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN` - Max events in domain stream (default: `100000`)
- `TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL` - Investigation claim auto-expiry (default: `30m`)
- `TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS` - Max tokens of memory context in planning prompt (default: `2000`)
- `TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT` - Recent domain events for baseline awareness (default: `20`)

**Activity Compaction (LLM-powered digest):**
- `TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL` - Model for summarization/compaction LLM calls. Supports aliases (`fast`, `smart`) (default: agent's model)
- `TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS` - Max tokens for compacted digest output (default: `500`)
- `TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT` - Max events fetched before compaction (default: `200`)
- `TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL` - Raw events appended after digest for detail (default: `15`)

**Digest Caching (reduces redundant LLM calls):**
- `TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL` - Cached digest expiry (default: `5m`)
- `TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD` - Max new events for incremental update; above this triggers full recompaction (default: `20`)

**Activity Coordination (real-time agent signals):**
- `TRUVAG3_ACTIVITY_COORDINATION_ENABLED` - Enable/disable coordination layer (default: `true`)
- `TRUVAG3_ACTIVITY_SIGNAL_TTL` - Signal auto-expiry (default: `5m`)
- `TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT` - Max signals in `<agent_coordination>` prompt section (default: `10`)
- `TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN` - Max query chars in signals (default: `200`)

---

## Troubleshooting

### Common Issues

**"No AI provider available"**
- Set one of: `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `GROQ_API_KEY`

**"Failed to connect to Redis"**
- Check `REDIS_URL` is correct
- Ensure Redis is running
- Use `WithMockDiscovery(true)` for testing without Redis

**"Circuit breaker is open"**
- Check if downstream service is healthy
- Review circuit breaker thresholds
- Check logs for failure patterns

**"Port already in use"**
- Another service is using the port
- Change port with `WithPort(different_port)`

### Debug Mode

Enable detailed logging for troubleshooting:

```bash
export TRUVAG3_LOG_LEVEL=debug
export TRUVAG3_LOG_FORMAT=text
# or
export TRUVAG3_DEV_MODE=true
```

---

## Support

- GitHub Issues: [github.com/truvaagents/truva-g3/issues](https://github.com/truvaagents/truva-g3/issues)
- Documentation: [github.com/truvaagents/truva-g3/docs](https://github.com/truvaagents/truva-g3/docs)
- Examples: [github.com/truvaagents/truva-g3/examples](https://github.com/truvaagents/truva-g3/examples)
