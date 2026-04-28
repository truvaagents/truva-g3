# Truva-G3 Core Module

Welcome to the foundation of intelligent agent systems! This guide will walk you through everything step-by-step, like a friendly mentor sitting right next to you. ☕

## Table of Contents

1. [What Is This Module and Why Should You Care?](#1-what-is-this-module-and-why-should-you-care)
2. [What's Included (and What's Not)](#2-whats-included-and-whats-not)
3. [The Framework: Bringing It All Together](#3-the-framework-bringing-it-all-together)
4. [Quick Start: Your First Components](#4-quick-start-your-first-components)
5. [Registering Capabilities: Making Your Components Useful](#5-registering-capabilities-making-your-components-useful)
6. [Advanced Features: The Power Tools](#6-advanced-features-the-power-tools)
7. [Understanding Component Registration and Discovery](#7-understanding-component-registration-and-discovery)
8. [Architecture Patterns](#8-architecture-patterns)
9. [Advanced Features](#9-advanced-features)
10. [Best Practices](#10-best-practices)
11. [Common Patterns and Solutions](#11-common-patterns-and-solutions)
12. [Debugging and Monitoring](#12-debugging-and-monitoring)
13. [Performance Considerations](#13-performance-considerations)
14. [Summary](#14-summary)
15. [Next Steps](#15-next-steps)

## 1. What Is This Module and Why Should You Care?

Let me explain this in the simplest way possible.

### The Coffee Shop Analogy

Imagine you're running a coffee shop. You have different workers:
- **The Barista** makes coffee
- **The Cashier** takes orders  
- **The Baker** makes pastries
- **The Cleaner** keeps things tidy

Now, imagine if these workers could:
1. **Find each other automatically** (like having each other's phone numbers)
2. **Remember important things** (like regular customers' favorite drinks)
3. **Handle problems gracefully** (if the coffee machine breaks, they know what to do)
4. **Work together seamlessly** (cashier tells barista what to make)

**That's exactly what the Truva-G3 Core module does for your code!** It helps you build intelligent components that can work independently or together.

### The Two Types of Components: Tools and Agents

In Truva-G3, we have two fundamental building blocks:

#### 🔧 Tools (Passive Components)
Think of **Tools** like the appliances in your kitchen:
- A **toaster** toasts bread (doesn't make coffee)
- A **blender** blends ingredients (doesn't cook them)
- A **microwave** heats food (doesn't wash dishes)

Tools in Truva-G3:
- **Do ONE thing well** (like Unix commands: `ls`, `grep`, `sort`)
- **Register themselves** ("I'm a calculator, I can add and multiply")
- **Respond to requests** (process input, return output)
- **Are stateless** (don't maintain conversation context)
- **CANNOT discover or call other components** (they're passive)

```go
// Tools are created with NewTool()
calculator := core.NewTool("calculator")
```

#### Real-World Tool Examples

To understand what makes a good Tool, consider these examples from simple to complex:

**Level 1: Pure Computation (No External Dependencies)**

Like Unix commands that process input and return output:

| Unix Command | Truva-G3 Tool Equivalent | What It Does |
|--------------|------------------------|--------------|
| `ls` | `directory-tool` | Lists files in a directory |
| `grep` | `search-tool` | Searches text for patterns |
| `sort` | `sort-tool` | Sorts input data |
| `calc` | `calculator-tool` | Performs mathematical operations |

```go
// Calculator tool - pure computation, no external calls
func (t *CalculatorTool) handleAdd(w http.ResponseWriter, r *http.Request) {
    var input struct { A, B float64 }
    json.NewDecoder(r.Body).Decode(&input)

    result := input.A + input.B  // Pure computation

    json.NewEncoder(w).Encode(map[string]float64{"result": result})
}
```

**Level 2: External API Integration (Common Pattern)**

Most real-world tools call external APIs to fulfill their capability:

| Tool | External API | Capability |
|------|--------------|------------|
| `weather-tool` | OpenWeatherMap API | `current_weather` |
| `stock-tool` | Alpha Vantage API | `stock_quote` |
| `geocoding-tool` | Google Maps API | `geocode_location` |
| `news-tool` | NewsAPI | `search_news` |

```go
// Weather tool - calls external API to fulfill its capability
func (t *WeatherTool) handleWeather(w http.ResponseWriter, r *http.Request) {
    var input struct { Location string }
    json.NewDecoder(r.Body).Decode(&input)

    // Tool calls external API - this is normal and expected!
    resp, err := http.Get(fmt.Sprintf(
        "https://api.openweathermap.org/data/2.5/weather?q=%s&appid=%s",
        input.Location, t.apiKey,
    ))
    if err != nil {
        http.Error(w, "Weather API unavailable", 502)
        return
    }

    // Process and return
    var weather WeatherResponse
    json.NewDecoder(resp.Body).Decode(&weather)
    json.NewEncoder(w).Encode(weather)
}
```

**Level 3: AI-Enhanced Tools**

Tools can use AI internally while remaining "passive" within Truva-G3:

```go
// Translation tool - uses AI provider but doesn't orchestrate other tools
func (t *TranslatorTool) handleTranslate(w http.ResponseWriter, r *http.Request) {
    var input struct { Text, From, To string }
    json.NewDecoder(r.Body).Decode(&input)

    // Tool uses AI client internally
    response, err := t.aiClient.GenerateResponse(r.Context(),
        fmt.Sprintf("Translate from %s to %s: %s", input.From, input.To, input.Text),
        nil,
    )

    json.NewEncoder(w).Encode(map[string]string{"translated": response.Content})
}
```

#### What "Passive" Really Means

**Passive within Truva-G3** - Tools cannot:
- ❌ Discover other tools or agents
- ❌ Call other Truva-G3 components
- ❌ Orchestrate workflows

**Active outside Truva-G3** - Tools can (and often do):
- ✅ Call public APIs (OpenWeatherMap, Alpha Vantage, etc.)
- ✅ Call internal services in your cluster (company-data-api, auth-service, etc.)
- ✅ Use AI providers (OpenAI, Anthropic, etc.)
- ✅ Connect to databases
- ✅ Read/write files

The key rule: **A tool does ONE thing, even if that one thing requires resources outside the Truva-G3 ecosystem.**

#### 🤖 Agents (Active Orchestrators)
Think of **Agents** like the human workers who USE the tools:
- A **chef** uses multiple kitchen tools to create a meal
- A **barista** uses the coffee machine, grinder, and steamer
- A **manager** coordinates multiple workers

Agents in Truva-G3:
- **Register themselves** (discoverable by other agents)
- **Can discover both tools and other agents**
- **Orchestrate complex workflows**
- **Make intelligent decisions** (often using AI)
- **Coordinate multiple components**
- **Maintain context and state**
- **Optional memory**: `BaseAgent` has two optional fields — `ConversationMemory` (session-scoped history via `AddTurn`/`GetHistory`/`Clear`) and `SemanticMemory` (cross-session similarity search via `Store`/`Search`/`Delete`). See [Adding Context to Your Agent](../docs/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md)

```go
// Agents are created with NewBaseAgent()
orchestrator := core.NewBaseAgent("orchestrator")
```

### The Critical Architectural Rule

⚠️ **IMPORTANT** ⚠️  
**Tools are passive - they NEVER call or discover other components.**  
**Agents are active - they CAN discover and orchestrate everything.**

This separation ensures:
- Clean architecture
- Predictable behavior
- Easy testing
- Clear responsibility boundaries

### Anti-Pattern vs Correct Pattern

**❌ WRONG: Tool trying to discover:**
```go
// Tools should NEVER do this!
func (t *BadTool) Handler(w http.ResponseWriter, r *http.Request) {
    // Compile error! Tools don't have Discovery
    other, _ := t.Discovery.FindByCapability("something") // WON'T COMPILE!
}
```

**✅ RIGHT: Tool just does its job:**
```go
func (t *GoodTool) Handler(w http.ResponseWriter, r *http.Request) {
    // Process input, return output
    result := t.calculate(input)
    json.NewEncoder(w).Encode(result)
}
```

**✅ RIGHT: Agent orchestrating tools:**
```go
func (a *SmartAgent) Orchestrate(ctx context.Context) {
    // Agents CAN discover
    tools, _ := a.Discover(ctx, core.DiscoveryFilter{
        Type: core.ComponentTypeTool,
    })
    // ... coordinate the tools
}
```

### The Magic of Discovery (Redis Registry)

How do components find each other? Through a **registry** - think of it like a company directory.

**Redis** is our registry. It keeps track of:
- Which tools and agents are running
- Where they're located (their addresses)
- What they can do (their capabilities)
- Whether they're healthy (still working)

But here's the key difference:
- **Tools** can only REGISTER themselves ("I exist!")
- **Agents** can both REGISTER and DISCOVER ("Who's available?")

### Why This Matters for AI Applications

With the rise of AI and Large Language Models (LLMs), we need:
- **Discoverable tools** that AI can find and use
- **Intelligent agents** that can orchestrate tools
- **Clear boundaries** between passive tools and active orchestrators
- **Scalable architecture** where each component does one thing well

## 2. What's Included (and What's Not)

### ✅ Core Module Includes:
- **Component framework** - Both Tool and Agent base implementations
- **Discovery system** - Registry for tools, Discovery for agents
- **HTTP server** - Automatic server setup with health checks
- **Memory interface** - For state storage (in-memory by default)
- **Configuration** - Environment-based config with validation
- **Kubernetes support** - Automatic Service DNS resolution
- **Error handling** - Comprehensive error types and recovery
- **Streaming interfaces** - Types for real-time AI response streaming

### ❌ NOT Included (Bring Your Own):
- **AI/LLM integration** - Add via the `ai` module
- **Workflow orchestration** - Add via the `orchestration` module
- **Distributed tracing** - Add via the `telemetry` module
- **Circuit breakers** - Add via the `resilience` module
- **Actual business logic** - That's your job!

### 🧠 Core vs Orchestration: Error Handling

Core provides **error types and basic classification**. For **intelligent retry with AI**, use the orchestration module:

| Feature | Core Module | Orchestration Module |
|---------|-------------|---------------------|
| Error types (`ToolError`, `ToolResponse`) | ✅ | Uses core types |
| Error categories (`CategoryNotFound`, etc.) | ✅ | Uses core types |
| Simple retry check (`Retryable` field) | ✅ | - |
| **LLM error analysis** | ❌ | ✅ |
| **AI-powered parameter correction** | ❌ | ✅ |
| **Semantic retry (Layer 4)** | ❌ | ✅ |

> **Need smarter error recovery?** Use the `orchestration` module which provides AI-powered retry with automatic parameter correction. See [orchestration/README.md](../orchestration/README.md).

### 🤖 Adding AI Support (Optional)

Want AI-powered components? It's easy!

```go
// AI-enhanced Tool (passive, but uses AI internally)
translator := ai.NewAITool("translator", apiKey)

// AI-powered Agent (can orchestrate AND use AI)
assistant := ai.NewAIAgent("assistant", apiKey)
```

## 3. The Framework: Bringing It All Together

Before we build our first components, let's understand the Framework - it's the conductor that orchestrates everything!

### What is the Framework?

The Framework is like the stage manager in a theater - it:
- **Initializes** your component with all the right settings
- **Auto-injects dependencies** - Registry for Tools, Discovery for Agents (when enabled)
- **Connects** to Redis for discovery (if configured)
- **Sets up** HTTP server with health checks
- **Manages** the component lifecycle (start, run, shutdown)

### Using the Framework

```go
package main

import (
    "context"
    "log"
    "os"
    "strconv"
    "github.com/truvaagents/truva-g3/core"
)

func main() {
    // Create your component (Tool or Agent)
    tool := core.NewTool("my-tool")

    // Wrap it with Framework and configure
    // Set environment: export REDIS_URL="redis://localhost:6379"
    // Set environment: export PORT=8080
    portStr := os.Getenv("PORT")
    port := 8080 // default
    if portStr != "" {
        if p, err := strconv.Atoi(portStr); err == nil {
            port = p
        }
    }

    framework, err := core.NewFramework(tool,
        core.WithPort(port),
        core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
        core.WithDiscovery(true),
        core.WithCORS([]string{"https://app.example.com"}, true),
    )
    if err != nil {
        log.Fatal(err)
    }
    
    // Run (initializes and starts the component)
    ctx := context.Background()
    if err := framework.Run(ctx); err != nil {
        log.Fatal(err)
    }
}
```

### Framework Options

The Framework accepts configuration options that apply to your component:

```go
// Core options
core.WithName("my-service")          // Component name
core.WithPort(8080)                  // HTTP port
core.WithNamespace("production")     // Namespace for grouping

// Discovery options
// Set environment: export REDIS_URL="redis://localhost:6379"
core.WithRedisURL(os.Getenv("REDIS_URL"))    // e.g., "redis://localhost:6379"
core.WithDiscovery(true, "redis")            // Enable service discovery with provider
core.WithDiscoveryCacheEnabled(true)         // Enable discovery caching

// CORS options
core.WithCORS([]string{"https://app.example.com"}, true)

// Development options
core.WithDevelopmentMode(true)       // Enable development features
core.WithMockDiscovery(true)         // Use in-memory discovery (testing)
```

### Environment Variables and Constants

Truva-G3 defines standard environment variables for configuration. The framework provides constants in `core/constants.go` to reference these variables, eliminating magic strings and providing type safety.

#### Required Configuration

These environment variables **must** be set for the framework to function:

| Constant | Environment Variable | Description | Example |
|----------|---------------------|-------------|---------|
| `core.EnvRedisURL` | `REDIS_URL` | Redis connection for service discovery | `redis://localhost:6379` |

#### Optional Configuration (Framework Provides Defaults)

| Constant | Environment Variable | Default | Description |
|----------|---------------------|---------|-------------|
| `core.EnvPort` | `PORT` | `8080` | HTTP server port |
| `core.EnvDevMode` | `DEV_MODE` | `false` | Enable development mode |
| `core.EnvNamespace` | `NAMESPACE` | `"default"` | Kubernetes namespace (auto-detected in K8s) |
| `core.EnvServiceName` | `TRUVAG3_K8S_SERVICE_NAME` | Auto-detected | Service name in Kubernetes |

#### Orchestration Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `TRUVAG3_ORCHESTRATION_TIMEOUT` | `60s` | HTTP client timeout for orchestrator tool/agent calls. For long-running AI workflows, set higher values (e.g., `5m`, `10m`). Uses Go duration format. |

#### Feature Flags (Opt-In)

| Constant | Environment Variable | Default | Description |
|----------|---------------------|---------|-------------|
| `core.EnvValidatePayloads` | `TRUVAG3_VALIDATE_PAYLOADS` | Disabled | Enable Phase 3 schema validation for AI-generated payloads |

#### Redis and Schema Cache Constants

| Constant | Value | Description |
|----------|-------|-------------|
| `core.DefaultRedisPrefix` | `"truvag3:schema:"` | Default Redis key prefix for schema cache |
| `core.DefaultSchemaCacheTTL` | `24 * time.Hour` | Default TTL for cached schemas |
| `core.SchemaEndpointSuffix` | `"/schema"` | Auto-generated schema endpoint suffix |

#### Using Constants in Your Code

**✅ Recommended** - Use constants to avoid typos and enable IDE support:

```go
import (
    "os"
    "github.com/truvaagents/truva-g3/core"
)

// Good: Use constants
redisURL := os.Getenv(core.EnvRedisURL)
if os.Getenv(core.EnvValidatePayloads) == "true" {
    // Enable Phase 3 validation
}

// Initialize schema cache with framework constants
cache := core.NewSchemaCache(redisClient,
    core.WithPrefix(core.DefaultRedisPrefix),
    core.WithTTL(core.DefaultSchemaCacheTTL),
)
```

**❌ Avoid** - Magic strings are error-prone:

```go
// Bad: Magic strings (typos won't be caught at compile time)
redisURL := os.Getenv("REDIS_URL")
if os.Getenv("TRUVAG3_VALIDATE_PAYLOADS") == "true" {  // Typo risk!
    // ...
}
```

#### Configuration Priority

The framework follows this precedence order:

1. **Explicit options** passed to `core.NewFramework()` (highest priority)
2. **Environment variables** (e.g., `REDIS_URL`, `PORT`)
3. **Framework defaults** (e.g., port 8080, namespace "default")
4. **Auto-detection** (e.g., Kubernetes service name from `HOSTNAME`)

### Framework Dependency Injection

The Framework automatically handles dependency injection for your components:

#### For Tools (Registry Auto-Injection)
```go
// Tools get Registry automatically when discovery is enabled
tool := core.NewTool("calculator")

// Set environment: export REDIS_URL="redis://localhost:6379"
framework, _ := core.NewFramework(tool,
    core.WithDiscovery(true, "redis"),
    core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
)

// After framework.Run(), tool.Registry is automatically set!
// No manual setup required - the framework handles it
```

#### For Agents (Discovery Auto-Injection)
```go
// Agents get Discovery automatically when discovery is enabled
agent := core.NewBaseAgent("orchestrator")

// Set environment: export REDIS_URL="redis://localhost:6379"
framework, _ := core.NewFramework(agent,
    core.WithDiscovery(true, "redis"),
    core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
)

// After framework.Run(), agent.Discovery is automatically set!
// The agent can immediately start discovering other components
```

#### Backward Compatibility
```go
// Manual setup still works (for custom scenarios)
tool := core.NewTool("custom-tool")
// Set environment: export REDIS_URL="redis://localhost:6379"
registry, _ := core.NewRedisRegistry(os.Getenv("REDIS_URL"))  // e.g., "redis://localhost:6379"
tool.Registry = registry  // Manual assignment works

// Framework respects manual setup and won't override it
framework, _ := core.NewFramework(tool, ...)
```

### Framework Lifecycle

```go
// 1. Create component
component := core.NewTool("example")

// 2. Create framework with configuration
framework, _ := core.NewFramework(component, options...)

// 3. Run (initializes and starts - blocking)
framework.Run(ctx)  // Initializes, connects to Redis, registers, starts server

// The framework handles:
// - Graceful shutdown on SIGINT/SIGTERM
// - Deregistration from discovery
// - Connection cleanup
```

## 4. Quick Start: Your First Components

### Prerequisites
- Go 1.21 or later
- Basic Go knowledge (packages, functions, structs)
- Redis (optional, for discovery between components)

### Installation

```bash
go get github.com/truvaagents/truva-g3/core
```

### Example 1: Creating a Tool (Passive Component)

```go
package main

import (
    "context"
    "encoding/json"
    "net/http"
    "os"
    "strconv"

    "github.com/truvaagents/truva-g3/core"
)

func main() {
    // Create a tool (passive component)
    calculator := core.NewTool("calculator")
    
    // Register what it can do (see "Registering Capabilities" section for details)
    calculator.RegisterCapability(core.Capability{
        Name:        "add",
        Description: "Adds two numbers",
        Endpoint:    "/add",
        Handler: func(w http.ResponseWriter, r *http.Request) {
            // Parse input
            var input struct {
                A float64 `json:"a"`
                B float64 `json:"b"`
            }
            json.NewDecoder(r.Body).Decode(&input)
            
            // Calculate (tools just do their job)
            result := input.A + input.B
            
            // Return result
            json.NewEncoder(w).Encode(map[string]float64{
                "result": result,
            })
        },
    })
    
    // Initialize and start
    ctx := context.Background()
    calculator.Initialize(ctx)

    // Get port from environment or use default
    // Set environment: export PORT=8080
    portStr := os.Getenv("PORT")
    port := 8080 // default
    if portStr != "" {
        if p, err := strconv.Atoi(portStr); err == nil {
            port = p
        }
    }
    calculator.Start(ctx, port)

    // Tool is now running at http://localhost:8080 (or configured port)
    // It CANNOT discover or call other components
    select {} // Keep running
}
```

### Example 2: Creating an Agent (Active Orchestrator)

```go
package main

import (
    "context"
    "encoding/json"
    "net/http"
    "os"

    "github.com/truvaagents/truva-g3/core"
)

func main() {
    // Create an agent (active orchestrator)
    orchestrator := core.NewBaseAgent("orchestrator")

    // Configure with discovery capability
    // Set environment: export REDIS_URL="redis://localhost:6379"
    framework, _ := core.NewFramework(orchestrator,
        core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
        core.WithDiscovery(true),
    )
    
    ctx := context.Background()
    
    // Add a discovery capability to the agent
    orchestrator.RegisterCapability(core.Capability{
        Name:        "discover_components",
        Description: "Discovers and lists available components",
        Handler: func(w http.ResponseWriter, r *http.Request) {
            // Find all available tools
            tools, _ := orchestrator.Discover(ctx, core.DiscoveryFilter{
                Type: core.ComponentTypeTool,
            })
            
            // Find other agents
            agents, _ := orchestrator.Discover(ctx, core.DiscoveryFilter{
                Type: core.ComponentTypeAgent,
            })
            
            // Return discovery results
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(map[string]interface{}{
                "tools":  tools,
                "agents": agents,
            })
        },
    })
    
    // The framework handles initialization and starts the agent
    // Once running, you can call: GET /api/capabilities/discover_components
    framework.Run(ctx)
}
```

## 5. Registering Capabilities: Making Your Components Useful

Now that you know how to create Tools and Agents, let's learn the most important part: **how to make them actually DO something!** This is where capabilities come in.

### What Are Capabilities?

Think of capabilities like the menu at a restaurant:
- A **coffee shop** has capabilities: "make espresso", "make latte", "make cappuccino"
- A **calculator** has capabilities: "add", "subtract", "multiply", "divide"
- Each capability is something your component can do

When you register a capability, you're telling the world: "Hey, I can do this thing!"

### The Capability Structure

```go
type Capability struct {
    Name        string           `json:"name"`        // Unique identifier
    Description string           `json:"description"` // What it does
    Endpoint    string           `json:"endpoint"`    // Where to call it (optional)
    InputTypes  []string         `json:"input_types"` // Expected input formats
    OutputTypes []string         `json:"output_types"`// Output formats
    Handler     http.HandlerFunc `json:"-"`          // The actual function (optional)
    Internal    bool             `json:"internal"`    // Exclude from LLM catalog (default: false)
}
```

> **Note:** The `Internal` flag marks capabilities that should be excluded from LLM planning catalogs. Internal capabilities remain HTTP-callable but won't appear in the service catalog used for AI orchestration decisions. Use this for orchestration endpoints, admin endpoints, or deprecated capabilities.

### The Magic of RegisterCapability

Both Tools and Agents use `RegisterCapability()` to define what they can do:

```go
// For any component (Tool or Agent)
component.RegisterCapability(core.Capability{
    Name:        "capability_name",
    Description: "What this capability does",
    // That's it! Everything else is optional
})
```

### 🔧 Registering Capabilities for Tools

Tools register capabilities to define their functions. Here are two ways to do it:

**Great News for Tools**: Just like Agents, Tools also support auto-endpoint generation and generic handlers! If you don't specify an `Endpoint`, it will auto-generate as `/api/capabilities/{name}`. If you don't provide a `Handler`, a generic one is provided automatically. This means Tools and Agents use the exact same capability registration pattern!

#### Method 1: Auto-Generated Endpoint (Easiest!)

```go
package main

import (
    "encoding/json"
    "net/http"
    "github.com/truvaagents/truva-g3/core"
)

func main() {
    calculator := core.NewTool("calculator")
    
    // Simple capability - endpoint auto-generates as /api/capabilities/add
    calculator.RegisterCapability(core.Capability{
        Name:        "add",
        Description: "Adds two numbers",
        // No Endpoint specified - auto-generates!
        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler: func(w http.ResponseWriter, r *http.Request) {
            // Parse input
            var input struct {
                A float64 `json:"a"`
                B float64 `json:"b"`
            }
            json.NewDecoder(r.Body).Decode(&input)
            
            // Do the calculation
            result := input.A + input.B
            
            // Return result
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(map[string]float64{
                "result": result,
            })
        },
    })
    
    // Accessible at: http://localhost:8080/api/capabilities/add
}
```

#### Method 2: Custom Endpoint Path (When You Need Control)

```go
func main() {
    weatherTool := core.NewTool("weather-service")
    
    // Register with a custom endpoint path
    weatherTool.RegisterCapability(core.Capability{
        Name:        "current_weather",
        Description: "Gets current weather for a city",
        Endpoint:    "/weather/current",  // Custom endpoint (overrides auto-generation)
        Handler: func(w http.ResponseWriter, r *http.Request) {
            city := r.URL.Query().Get("city")
            // Fetch weather data...
            weather := getWeather(city)
            json.NewEncoder(w).Encode(weather)
        },
    })
    
    // Accessible at: http://localhost:8080/weather/current?city=London
}
```

#### Method 3: Ultra-Simple with Generic Handler (Perfect for Prototyping!)

```go
func main() {
    simpleTool := core.NewTool("simple-tool")
    
    // Just name and description - everything else is auto-generated!
    simpleTool.RegisterCapability(core.Capability{
        Name:        "ping",
        Description: "Simple ping capability for testing",
        // No Endpoint - auto-generates as /api/capabilities/ping
        // No Handler - uses generic handler that returns capability info
    })
    
    // The generic handler will return:
    // {"capability": "ping", "description": "Simple ping capability for testing"}
    // Accessible at: http://localhost:8080/api/capabilities/ping
}
```

### 🤖 Registering Capabilities for Agents

Agents register capabilities using the exact same pattern as Tools:
- **Auto-endpoint generation**: If you don't specify an `Endpoint`, it auto-generates as `/api/capabilities/{name}` (same as Tools)
- **Generic handler**: If you don't provide a `Handler`, a generic one is provided (same as Tools)
- **Orchestration**: Agents can discover and coordinate other components in their handlers (this is the key difference!)

#### Agent with Auto-Generated Endpoint

```go
package main

import (
    "context"
    "encoding/json"
    "net/http"
    "github.com/truvaagents/truva-g3/core"
)

func main() {
    // Create an agent
    orchestrator := core.NewBaseAgent("orchestrator")
    
    // Simple capability - endpoint auto-generates as /api/capabilities/status
    orchestrator.RegisterCapability(core.Capability{
        Name:        "status",
        Description: "Returns agent status",
        // No Endpoint specified - auto-generates!
        // No Handler specified - uses generic handler!
    })
    
    // Capability with custom handler but auto-generated endpoint
    orchestrator.RegisterCapability(core.Capability{
        Name:        "coordinate",
        Description: "Coordinates multiple tools",
        // No Endpoint - auto-generates as /api/capabilities/coordinate
        Handler: func(w http.ResponseWriter, r *http.Request) {
            // Custom orchestration logic
            w.Write([]byte("Coordinating tools..."))
        },
    })
}
```

#### Agent with Orchestration Capability

```go
func main() {
    // Create an agent  
    dataProcessor := core.NewBaseAgent("data-processor")
    
    // Register a capability that orchestrates multiple tools
    dataProcessor.RegisterCapability(core.Capability{
        Name:        "process_report",
        Description: "Fetches data, analyzes it, and generates a report",
        InputTypes:  []string{"json"},
        OutputTypes: []string{"pdf", "json"},
        Handler: func(w http.ResponseWriter, r *http.Request) {
            ctx := r.Context()
            
            // Agents can discover other components!
            dataTools, _ := dataProcessor.Discover(ctx, core.DiscoveryFilter{
                Type: core.ComponentTypeTool,
                Capabilities: []string{"fetch_data"},
            })
            
            analyticTools, _ := dataProcessor.Discover(ctx, core.DiscoveryFilter{
                Type: core.ComponentTypeTool,
                Capabilities: []string{"analyze"},
            })
            
            // Orchestrate the workflow
            // 1. Fetch data using data tool
            // 2. Analyze using analytics tool
            // 3. Generate report
            
            // Return the final report
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(report)
        },
    })
}
```

### 🌟 The Capability Discovery Endpoint

**IMPORTANT**: All registered capabilities are automatically exposed at a special endpoint!

```bash
# Every component automatically provides this endpoint:
GET http://localhost:8080/api/capabilities

# Returns:
[
  {
    "name": "add",
    "description": "Adds two numbers",
    "endpoint": "/api/capabilities/add",
    "input_types": ["json"],
    "output_types": ["json"]
  },
  {
    "name": "multiply",
    "description": "Multiplies two numbers",
    "endpoint": "/api/capabilities/multiply",
    "input_types": ["json"],
    "output_types": ["json"]
  }
]
```

This is how other agents discover what your component can do!

### 📝 Complete Example: Building a Translation Tool

Let's build a complete tool with multiple capabilities:

```go
package main

import (
    "context"
    "encoding/json"
    "net/http"
    "os"
    "strings"

    "github.com/truvaagents/truva-g3/core"
)

type TranslationTool struct {
    *core.BaseTool
    // Add any tool-specific fields
}

func NewTranslationTool() *TranslationTool {
    tool := &TranslationTool{
        BaseTool: core.NewTool("translator"),
    }
    
    // Register capability 1: Translate text
    tool.RegisterCapability(core.Capability{
        Name:        "translate",
        Description: "Translates text between languages",
        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler:     tool.handleTranslate,
    })
    
    // Register capability 2: Detect language
    tool.RegisterCapability(core.Capability{
        Name:        "detect_language",
        Description: "Detects the language of input text",
        InputTypes:  []string{"text", "json"},
        OutputTypes: []string{"json"},
        Handler:     tool.handleDetectLanguage,
    })
    
    // Register capability 3: List supported languages
    tool.RegisterCapability(core.Capability{
        Name:        "list_languages",
        Description: "Lists all supported languages",
        Endpoint:    "/languages",  // Custom endpoint
        Handler:     tool.handleListLanguages,
    })
    
    return tool
}

func (t *TranslationTool) handleTranslate(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Text     string `json:"text"`
        From     string `json:"from"`
        To       string `json:"to"`
    }
    json.NewDecoder(r.Body).Decode(&req)
    
    // Perform translation (simplified)
    translated := t.translate(req.Text, req.From, req.To)
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{
        "translated": translated,
        "from":       req.From,
        "to":         req.To,
    })
}

func (t *TranslationTool) handleDetectLanguage(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Text string `json:"text"`
    }
    json.NewDecoder(r.Body).Decode(&req)
    
    // Detect language (simplified example)
    language := "en"
    if strings.Contains(req.Text, "hola") {
        language = "es"
    } else if strings.Contains(req.Text, "bonjour") {
        language = "fr"
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "language":   language,
        "confidence": 0.95,
    })
}

func (t *TranslationTool) handleListLanguages(w http.ResponseWriter, r *http.Request) {
    languages := []map[string]string{
        {"code": "en", "name": "English"},
        {"code": "es", "name": "Spanish"},
        {"code": "fr", "name": "French"},
        {"code": "de", "name": "German"},
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(languages)
}

func (t *TranslationTool) translate(text, from, to string) string {
    // Your translation logic here
    return "translated: " + text
}

func main() {
    // Create and configure the tool
    translator := NewTranslationTool()

    // Initialize with framework
    // Set environment: export REDIS_URL="redis://localhost:6379"
    framework, _ := core.NewFramework(translator,
        core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
        core.WithDiscovery(true),
    )
    
    ctx := context.Background()
    framework.Initialize(ctx)
    
    // The tool is now running with three endpoints:
    // - POST /api/capabilities/translate
    // - POST /api/capabilities/detect_language  
    // - GET  /languages
    // - GET  /api/capabilities (lists all capabilities)
    
    framework.Run(ctx)
}
```

### 🎯 Best Practices for Capabilities

1. **Use Descriptive Names**: 
   - ✅ Good: `translate_text`, `calculate_tax`, `fetch_weather`
   - ❌ Bad: `process`, `handle`, `do_stuff`

2. **Provide Clear Descriptions**:
   - ✅ Good: "Translates text from source to target language"
   - ❌ Bad: "Translation function"

3. **Specify Input/Output Types**:
   ```go
   InputTypes:  []string{"json", "text"},  // Accept both
   OutputTypes: []string{"json"},          // Always return JSON
   ```

4. **Tools Should Have Focused Capabilities**:
   ```go
   // ✅ GOOD: Each capability does one thing
   calculator.RegisterCapability(core.Capability{Name: "add"})
   calculator.RegisterCapability(core.Capability{Name: "subtract"})
   
   // ❌ BAD: One capability doing everything
   calculator.RegisterCapability(core.Capability{Name: "calculate_anything"})
   ```

5. **Agents Can Have Orchestration Capabilities**:
   ```go
   // ✅ GOOD: Agent orchestrates a workflow
   agent.RegisterCapability(core.Capability{
       Name: "generate_monthly_report",
       Description: "Fetches data, analyzes trends, creates visualizations, generates PDF",
   })
   ```

### 🔍 Testing Your Capabilities

Once registered, test your capabilities:

```bash
# List all capabilities
curl http://localhost:8080/api/capabilities

# Call a specific capability (auto-generated endpoint)
curl -X POST http://localhost:8080/api/capabilities/translate \
  -H "Content-Type: application/json" \
  -d '{"text":"Hello","from":"en","to":"es"}'

# Call a custom endpoint
curl http://localhost:8080/languages
```

### 🎓 Key Takeaways

1. **Every component needs capabilities** to be useful
2. **RegisterCapability()** is how you define what your component can do
3. **Endpoints auto-generate** if you don't specify them
4. **Handlers are optional** but recommended for actual functionality
5. **All capabilities are discoverable** via `/api/capabilities`
6. **Tools register task capabilities**, Agents register orchestration capabilities

## 6. Advanced Features: The Power Tools

Now that you understand capabilities, let's explore the powerful features that make your components production-ready. Think of these as the professional-grade tools in your workshop!

### 📝 Memory Interface: Giving Your Components a Brain

Components often need to remember things - user preferences, cached data, conversation context. The Memory interface provides a simple key-value store with TTL (Time To Live) support.

#### What Can You Store?

```go
// Memory interface - your component's notepad
type Memory interface {
    Get(ctx context.Context, key string) (string, error)
    Set(ctx context.Context, key string, value string, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
}
```

#### Using Memory in Your Components

```go
package main

import (
    "context"
    "time"
    "github.com/truvaagents/truva-g3/core"
)

func main() {
    // Create a tool with memory
    calculator := core.NewTool("smart-calculator")
    
    // Use the default in-memory store
    // Note: The current NewInMemoryStore() implementation ignores TTL
    // For production with TTL support, configure Redis via framework options
    calculator.Memory = core.NewInMemoryStore()
    
    // Register a capability that uses memory
    calculator.RegisterCapability(core.Capability{
        Name:        "calculate_with_memory",
        Description: "Calculator that remembers previous results",
        Endpoint:    "/calculate",
        Handler: func(w http.ResponseWriter, r *http.Request) {
            ctx := r.Context()
            
            // Store the last result
            result := performCalculation()
            calculator.Memory.Set(ctx, "last_result", 
                strconv.FormatFloat(result, 'f', 2, 64), 
                5*time.Minute) // Remember for 5 minutes
            
            // Retrieve previous result
            lastResult, _ := calculator.Memory.Get(ctx, "last_result")
            
            json.NewEncoder(w).Encode(map[string]interface{}{
                "result":      result,
                "last_result": lastResult,
            })
        },
    })
}
```

#### Memory with Redis (for distributed systems)

```go
// When using Redis, memory is automatically shared across instances
// Set environment: export REDIS_URL="redis://localhost:6379"
framework, _ := core.NewFramework(tool,
    core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
    // Memory automatically uses Redis when available
)
```

### 🧠 Shared Memory Interfaces: Cross-Agent Coordination

Beyond per-component KV memory, the core module defines interfaces for **cross-agent shared memory** — episodic events, investigation coordination, activity compaction, and real-time signals. Implementations live in the `memory` module; LLM-powered components (compactor, summarizer) live in `orchestration`.

| Interface | Purpose |
|-----------|---------|
| `EpisodicMemory` | Record and query structured agent events (what happened, when, to which entity) |
| `InvestigationCoordinator` | Claim/release entity investigations to prevent duplicate work across agents |
| `ActivityCompactor` | Compress raw events into fixed-size LLM digests (full + incremental) |
| `DigestCache` | Cache compacted digests to avoid redundant LLM calls |
| `ActivityCoordinator` | Real-time agent signals — see what other agents are currently working on |
| `EventSummarizer` | Generate factual one-sentence step summaries with key identifiers |
| `SharedKnowledge` | Store and search reusable knowledge fragments via vector DB |

Each interface has a `NoOp` implementation (safe defaults when unconfigured) and a `Mock` implementation (for testing). See [API_REFERENCE.md](../docs/API_REFERENCE.md#shared-memory-interfaces) for full signatures.

The core module also defines interfaces for the **scheduled-execution subsystem**:

| Interface | Purpose |
|-----------|---------|
| `TaskConsumer` | Consume leased tasks from a transport (BRPOP, Streams, Postgres, etc.) |
| `TaskHandle` | Borrow-then-settle companion -- `Ack` (success) or `Nack` (terminal failure + DLQ) |
| `TaskDispatcher` | Enqueue a task for async consumption |
| `ScheduleStore` | Persist schedule definitions with time-indexed due queries |

The `core/conformance/` sub-package provides `RunTaskConsumerConformance(t, factory)` -- a contract test suite that validates any `TaskConsumer` implementation in ~5 lines. See [Scheduled Tasks Guide](../docs/SCHEDULED_TASKS_GUIDE.md).

### 🚦 CORS Middleware: Opening Doors Safely

When building web-accessible components, you need Cross-Origin Resource Sharing (CORS) support. Truva-G3 provides powerful CORS middleware with wildcard support.

#### Basic CORS Setup

```go
func main() {
    agent := core.NewBaseAgent("web-api")
    
    // Configure CORS
    agent.Config.HTTP.CORS = core.CORSConfig{
        Enabled:          true,
        AllowedOrigins:   []string{"https://myapp.com"},
        AllowCredentials: true,
    }
    
    // That's it! CORS headers are automatically added
}
```

#### Advanced CORS Patterns

```go
// Allow multiple origins with wildcards
agent.Config.HTTP.CORS = core.CORSConfig{
    Enabled: true,
    AllowedOrigins: []string{
        "https://app.example.com",
        "https://*.example.com",     // Wildcard subdomains
        "http://localhost:*",         // Any localhost port
    },
    AllowedMethods: []string{"GET", "POST", "PUT", "DELETE"},
    AllowedHeaders: []string{"Content-Type", "Authorization", "X-Custom-Header"},
    AllowCredentials: true,
    MaxAge: 86400, // Cache preflight for 24 hours
}
```

#### Using CORS Middleware Directly

```go
// You can also apply CORS to any HTTP handler
mux := http.NewServeMux()
corsConfig := &core.CORSConfig{
    Enabled: true,
    AllowedOrigins: []string{"*"}, // Allow all origins (use carefully!)
}

// Wrap your handler with CORS
handler := core.CORSMiddleware(corsConfig)(mux)
http.ListenAndServe(":8080", handler)
```

### 📊 Logging Interface: Know What's Happening

> **💡 Configuration Tip:** To configure logging levels and formats via environment variables, see [Logging Configuration in API Reference](../docs/API_REFERENCE.md#logging-configuration).

Every component gets a structured logger automatically. It's like having a flight recorder for your code!

#### The Logger Interface

```go
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
```

**Why Context-Aware Logging?** The `WithContext` methods enable distributed tracing by automatically including trace IDs, span IDs, and request correlation identifiers in your logs. This is essential for debugging distributed systems where a single request may traverse multiple services.

#### Using the Logger

```go
func main() {
    tool := core.NewTool("data-processor")
    
    // Logger is automatically available
    tool.Logger.Info("Starting data processor", map[string]interface{}{
        "version": "1.0.0",
        "mode":    "production",
    })
    
    // In your handlers
    tool.RegisterCapability(core.Capability{
        Name:     "process",
        Endpoint: "/process",
        Handler: func(w http.ResponseWriter, r *http.Request) {
            startTime := time.Now()
            
            // Log the request
            tool.Logger.Info("Processing request", map[string]interface{}{
                "method":     r.Method,
                "path":       r.URL.Path,
                "user_agent": r.UserAgent(),
            })
            
            // Process...
            if err := processData(); err != nil {
                tool.Logger.Error("Processing failed", map[string]interface{}{
                    "error":    err.Error(),
                    "duration": time.Since(startTime),
                })
                http.Error(w, "Processing failed", 500)
                return
            }
            
            tool.Logger.Info("Processing complete", map[string]interface{}{
                "duration": time.Since(startTime),
            })
        },
    })
}
```

#### Using Context-Aware Logging

When handling HTTP requests, use the `WithContext` methods to enable request correlation:

```go
tool.RegisterCapability(core.Capability{
    Name:     "process",
    Endpoint: "/process",
    Handler: func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()

        // Use WithContext methods - automatically includes trace/request IDs
        tool.Logger.InfoWithContext(ctx, "Processing request", map[string]interface{}{
            "method": r.Method,
            "path":   r.URL.Path,
        })

        if err := processData(ctx); err != nil {
            tool.Logger.ErrorWithContext(ctx, "Processing failed", map[string]interface{}{
                "error": err.Error(),
            })
            http.Error(w, "Processing failed", 500)
            return
        }

        tool.Logger.InfoWithContext(ctx, "Processing complete", nil)
        w.WriteHeader(http.StatusOK)
    },
})
```

**Benefits:**
- Logs from the same request are correlated via trace/request IDs
- Works seamlessly with OpenTelemetry distributed tracing
- Essential for debugging in production environments

### 🌊 Streaming Interface: Real-Time AI Responses

For chat agents and real-time AI applications, the core module provides streaming types that enable token-by-token delivery of AI responses.

#### Core Streaming Types

```go
// StreamChunk represents a single chunk of streaming output
type StreamChunk struct {
    Content      string                 // The text content of this chunk
    Done         bool                   // True if this is the final chunk
    FinishReason string                 // Why generation stopped (e.g., "stop", "length")
    Usage        *AIUsage               // Token usage (only on final chunk)
    Error        error                  // Error if streaming failed
    Metadata     map[string]interface{} // Provider-specific metadata
}

// StreamCallback is a function that receives streaming chunks
type StreamCallback func(chunk StreamChunk) error
```

#### Using Streaming in Handlers

```go
// Example: SSE streaming endpoint for chat
func (h *ChatHandler) HandleStream(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    flusher := w.(http.Flusher)

    // Set SSE headers
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    // Stream AI response
    err := h.aiClient.StreamResponse(ctx, prompt, nil,
        func(chunk core.StreamChunk) error {
            if chunk.Error != nil {
                return chunk.Error
            }

            // Send each token via SSE
            if chunk.Content != "" {
                fmt.Fprintf(w, "data: %s\n\n", chunk.Content)
                flusher.Flush()
            }

            // Handle completion
            if chunk.Done {
                fmt.Fprintf(w, "event: done\ndata: complete\n\n")
                flusher.Flush()
            }

            return nil
        },
    )

    if err != nil {
        fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
    }
}
```

#### StreamingAIClient Interface

AI clients that support streaming implement this interface:

```go
// StreamingAIClient extends AIClient with streaming support
type StreamingAIClient interface {
    AIClient

    // StreamResponse generates a streaming response
    StreamResponse(ctx context.Context, prompt string, options *AIOptions, callback StreamCallback) error
}
```

#### When to Use Streaming

| Scenario | Non-Streaming | Streaming |
|----------|---------------|-----------|
| API backend | ✅ Simpler | ❌ Overkill |
| Chat UI | ⚠️ Poor UX | ✅ Real-time feedback |
| Long-running queries | ⚠️ User waits | ✅ Immediate feedback |
| Token tracking | ✅ Simple | ✅ Final chunk includes usage |

For complete streaming examples, see the [Chat Agent Guide](../docs/CHAT_AGENT_GUIDE.md) and the [ai/README.md](../ai/README.md#-streaming-support).

### 🔍 Telemetry Interface: Measure Everything

Want to add distributed tracing and metrics? The Telemetry interface makes it easy!

#### Telemetry Interface

```go
type Telemetry interface {
    StartSpan(ctx context.Context, name string) (context.Context, Span)
    RecordMetric(name string, value float64, labels map[string]string)
}
```

#### Using Telemetry

```go
func main() {
    agent := core.NewBaseAgent("analytics")
    
    // Telemetry is optional - add it when needed
    // (Usually added by the telemetry module)
    
    agent.RegisterCapability(core.Capability{
        Name:     "analyze",
        Endpoint: "/analyze",
        Handler: func(w http.ResponseWriter, r *http.Request) {
            // Start a span for tracing
            ctx, span := agent.Telemetry.StartSpan(r.Context(), "analyze_data")
            defer span.End()
            
            // Add attributes to the span
            span.SetAttribute("data.size", len(data))
            span.SetAttribute("user.id", userID)
            
            // Record metrics
            agent.Telemetry.RecordMetric("analysis.count", 1, map[string]string{
                "type": "full",
            })
            
            // Process with context
            result, err := analyzeWithContext(ctx, data)
            if err != nil {
                span.RecordError(err)
                return
            }
            
            // Record performance metric
            agent.Telemetry.RecordMetric("analysis.duration", 
                time.Since(start).Seconds(),
                map[string]string{"status": "success"})
        },
    })
}
```

### ❌ Error Handling: Fail Gracefully

Truva-G3 provides a comprehensive error system with standard errors and helper functions.

#### Standard Errors

```go
// Use standard errors for consistency
if service == nil {
    return fmt.Errorf("failed to find service %s: %w", 
        serviceName, core.ErrServiceNotFound)
}

// Check error types
if errors.Is(err, core.ErrTimeout) {
    // Handle timeout specifically
    return retryWithBackoff()
}

// Check categories of errors
if core.IsRetryable(err) {
    // This error might succeed if we try again
    return retry()
}

if core.IsNotFound(err) {
    // Resource doesn't exist
    return createResource()
}
```

#### Complete Error Example

```go
func (t *DataTool) fetchData(ctx context.Context, id string) error {
    // Check initialization
    if t.client == nil {
        return core.ErrNotInitialized
    }
    
    // Set timeout
    ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    
    // Fetch with retry logic
    var lastErr error
    for i := 0; i < 3; i++ {
        data, err := t.client.Get(ctx, id)
        if err == nil {
            return nil // Success!
        }
        
        lastErr = err
        
        // Check if we should retry
        if !core.IsRetryable(err) {
            // No point retrying
            return fmt.Errorf("fetch failed: %w", err)
        }
        
        // Log retry attempt
        t.Logger.Warn("Retrying fetch", map[string]interface{}{
            "attempt": i + 1,
            "error":   err.Error(),
        })
        
        time.Sleep(time.Second * time.Duration(i+1)) // Exponential backoff
    }
    
    return fmt.Errorf("max retries exceeded: %w", core.ErrMaxRetriesExceeded)
}
```

#### Intelligent Error Handling for Tool-Agent Communication

When tools and agents communicate, they need a standardized way to report errors with enough context for intelligent retry decisions. Truva-G3 provides the `ToolError` and `ToolResponse` types for this purpose.

**Why This Matters:** A tool might fail because of bad input (don't retry with same data) or a rate limit (wait and retry). Without structured error information, agents can't make smart decisions.

```go
// ToolResponse wraps all tool responses with success/error information
type ToolResponse struct {
    Success bool        `json:"success"`
    Data    interface{} `json:"data,omitempty"`
    Error   *ToolError  `json:"error,omitempty"`
}

// ToolError provides structured error information
type ToolError struct {
    Code      string            `json:"code"`       // e.g., "INVALID_LOCATION"
    Message   string            `json:"message"`    // Human-readable description
    Category  ErrorCategory     `json:"category"`   // INPUT_ERROR, RATE_LIMIT, etc.
    Retryable bool              `json:"retryable"`  // Should agent retry?
    Details   map[string]string `json:"details,omitempty"` // Additional context
}
```

**Quick Usage in Tools:**

```go
// Success response
return core.ToolResponse{
    Success: true,
    Data:    weatherData,
}

// Error response with category
return core.ToolResponse{
    Success: false,
    Error: &core.ToolError{
        Code:      "INVALID_LOCATION",
        Message:   "City 'Flower Mount' not found. Did you mean 'Flower Mound'?",
        Category:  core.CategoryInputError,
        Retryable: true,
        Details:   map[string]string{"suggestion": "Flower Mound, TX"},
    },
}
```

**Error Categories:**

| Category | When to Use | HTTP Status |
|----------|-------------|-------------|
| `CategoryInputError` | Invalid user input, typos | 400 |
| `CategoryNotFound` | Resource doesn't exist | 404 |
| `CategoryRateLimit` | Too many requests | 429 |
| `CategoryAuthError` | Authentication failed | 401 |
| `CategoryServiceError` | Internal/external service failure | 500/502 |

**Classifying Upstream API Errors:**

Tools that call external APIs (Amadeus, OpenWeatherMap, etc.) should use `ClassifyUpstreamError` to map upstream HTTP status codes to the correct tool response status. This ensures the orchestrator routes errors correctly — e.g., an upstream 400 goes to the LLM error analyzer (not resilience retry).

```go
// In your tool's handler:
result, err := h.externalClient.Search(ctx, params)
if err != nil {
    info := core.ClassifyUpstreamError(err) // Extracts status from error message
    // info.HTTPStatus=400, info.Category=CategoryInputError, info.Retryable=false
    h.sendUpstreamError(rw, "Search failed: "+err.Error(), info)
    return
}
```

See [API Reference — Upstream Error Classification](../docs/API_REFERENCE.md#upstream-error-classification) for the full classification mapping.

📖 **For comprehensive implementation details, see the [Intelligent Error Handling Guide](../docs/INTELLIGENT_ERROR_HANDLING.md).**

### ⚙️ Configuration System: Three-Layer Magic

The configuration system uses a three-layer priority system:
1. **Default values** (lowest priority)
2. **Environment variables** (medium priority)  
3. **Functional options** (highest priority)

#### Configuration Priority Example

```go
// Layer 1: Defaults
cfg := core.DefaultConfig() // Port: 8080 (default)

// Layer 2: Environment variables override defaults
// If TRUVAG3_PORT=9090 is set, port becomes 9090

// Layer 3: Functional options override everything
cfg, _ = core.NewConfig(
    core.WithPort(3000), // This wins! Port is now 3000
)
```

#### Port Configuration Precedence (Component Startup)

When starting components, port configuration follows a specific precedence:

```go
tool := core.NewTool("example")
tool.Config = &core.Config{Port: 8080} // Config port

// Explicit parameter overrides config
err := tool.Start(ctx, 9090) // 9090 wins over config port 8080

// If no explicit parameter (port < 0), config is used
err := tool.Start(ctx, -1) // Uses config port 8080

// Framework also respects this precedence
framework, _ := core.NewFramework(tool, core.WithPort(7070))
// Framework port 7070 overrides tool.Config.Port
```

**Port Precedence Rules:**
1. **Explicit parameters** (`tool.Start(ctx, 9090)`) - Highest priority
2. **Config values** (`tool.Config.Port = 8080`) - Medium priority  
3. **Default values** (`8080` for most components) - Lowest priority

#### Complete Configuration Example

```go
package main

import (
    "log"
    "os"
    "strconv"

    "github.com/truvaagents/truva-g3/core"
)

func main() {
    // Configure with functional options
    // Set environment: export REDIS_URL="redis://localhost:6379"
    // Set environment: export PORT=8080
    portStr := os.Getenv("PORT")
    port := 8080 // default
    if portStr != "" {
        if p, err := strconv.Atoi(portStr); err == nil {
            port = p
        }
    }

    cfg, err := core.NewConfig(
        core.WithName("my-agent"),
        core.WithPort(port),

        // CORS configuration
        core.WithCORS([]string{"https://app.example.com"}, true),

        // Discovery configuration
        core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
        core.WithDiscoveryCacheEnabled(true),

        // Development mode
        core.WithDevelopmentMode(true),
    )

    if err != nil {
        log.Fatal(err)
    }

    // Create agent with config
    agent := core.NewBaseAgentWithConfig(cfg)
    _ = agent // Use the agent...
}
```

#### Environment Variables

All configuration can be set via environment variables:

```bash
# Core configuration
export TRUVAG3_AGENT_NAME="my-agent"
export TRUVAG3_PORT=8080
export TRUVAG3_NAMESPACE="production"

# HTTP configuration
export TRUVAG3_HTTP_READ_TIMEOUT="30s"
export TRUVAG3_HTTP_HEALTH_CHECK=true

# CORS configuration
export TRUVAG3_CORS_ENABLED=true
export TRUVAG3_CORS_ORIGINS="https://app.example.com,https://*.example.com"

# Redis configuration
export TRUVAG3_REDIS_URL="redis://localhost:6379"
export TRUVAG3_REDIS_PASSWORD="secret"

# Development mode
export TRUVAG3_DEV_MODE=true
```

### 💓 Health Checks: Is Everything OK?

Every component automatically gets a health check endpoint. It's like a heartbeat for your services!

#### Default Health Check

```go
// Every component automatically provides:
// GET /health (or configured path)

// Returns:
{
    "status": "healthy",
    "component": "calculator-tool",
    "timestamp": "2024-01-01T12:00:00Z"
}
```

#### Custom Health Check

```go
agent := core.NewBaseAgent("database-agent")

// Configure health check path
agent.Config.HTTP.HealthCheckPath = "/healthz"

// Add custom health logic
agent.RegisterCapability(core.Capability{
    Name:     "health",
    Endpoint: "/healthz",
    Handler: func(w http.ResponseWriter, r *http.Request) {
        // Check database connection
        if err := checkDatabase(); err != nil {
            w.WriteHeader(http.StatusServiceUnavailable)
            json.NewEncoder(w).Encode(map[string]interface{}{
                "status": "unhealthy",
                "error":  err.Error(),
            })
            return
        }
        
        // Check Redis connection
        if err := checkRedis(); err != nil {
            w.WriteHeader(http.StatusServiceUnavailable)
            json.NewEncoder(w).Encode(map[string]interface{}{
                "status": "degraded",
                "warning": "Redis unavailable",
            })
            return
        }
        
        w.WriteHeader(http.StatusOK)
        json.NewEncoder(w).Encode(map[string]interface{}{
            "status": "healthy",
            "checks": map[string]string{
                "database": "ok",
                "redis":    "ok",
            },
        })
    },
})
```

### 🔄 Circuit Breaker Interface: Fail Fast, Recover Faster

The Circuit Breaker pattern prevents cascading failures. When a service is struggling, the circuit breaker "opens" to give it time to recover.

#### Circuit Breaker States

```
CLOSED → (failures exceed threshold) → OPEN → (timeout) → HALF-OPEN → (success) → CLOSED
                                          ↑                    ↓
                                          ←── (failure) ←──────┘
```

#### Using Circuit Breaker

```go
// The CircuitBreaker interface (implementations in resilience module)
type CircuitBreaker interface {
    Execute(ctx context.Context, fn func() error) error
    ExecuteWithTimeout(ctx context.Context, timeout time.Duration, fn func() error) error
    GetState() string // "closed", "open", "half-open"
    Reset()
}

// Example usage (with resilience module)
func (a *Agent) callExternalService(ctx context.Context) error {
    return a.CircuitBreaker.Execute(ctx, func() error {
        // This function is protected by the circuit breaker
        resp, err := http.Get("https://api.example.com/data")
        if err != nil {
            return err // Circuit breaker counts this failure
        }
        if resp.StatusCode >= 500 {
            return fmt.Errorf("server error: %d", resp.StatusCode)
        }
        return nil // Success!
    })
}
```

### 🤖 AI-Powered Payload Generation: The 3-Phase Approach

When building AI agents that orchestrate tools, a critical challenge is: **How does an agent know what data to send to a tool?**

The answer: **Progressive enhancement through 3 phases**, each building on the previous one.

#### The Problem We're Solving

Imagine you have 100 different tools, each accepting different JSON payloads. Your AI agent discovers a weather tool and needs to call it. The agent needs to know:
- What fields are required? (`location`)
- What fields are optional? (`units`, `days`)
- What format do they expect? (string, number, etc.)

Without this information, the AI must guess—and guessing leads to errors.

#### The 3 Phases: How They Work Together

Think of these like building blocks that stack on top of each other:

```
┌─────────────────────────────────────────────────────────┐
│ Phase 3 (OPTIONAL): Schema Validation                  │
│ - Validates AI-generated payloads before sending       │
│ - Only if TRUVAG3_VALIDATE_PAYLOADS=true                │
│ - Cached in Redis, 0ms overhead after first fetch      │
├─────────────────────────────────────────────────────────┤
│ Phase 2 (RECOMMENDED): Field-Hint-Based Generation     │
│ - AI uses exact field names from structured hints      │
│ - ~95% accuracy for most tools                         │
│ - Falls back to Phase 1 if hints unavailable           │
├─────────────────────────────────────────────────────────┤
│ Phase 1 (ALWAYS PRESENT): Description-Based Generation │
│ - AI generates payloads from natural language          │
│ - ~85-90% accuracy baseline                            │
│ - Works for all tools, no extra configuration          │
└─────────────────────────────────────────────────────────┘
```

#### Quick Example: Weather Tool

**Phase 1 - Basic Description** (Always include this):
```go
tool.RegisterCapability(core.Capability{
    Name: "current_weather",
    Description: "Gets current weather conditions for a location. " +
                 "Required: location (city name). " +
                 "Optional: units (metric/imperial, default: metric).",
    Handler: handleWeather,
})
```

**Phase 2 - Add Field Hints** (Recommended for better accuracy):
```go
tool.RegisterCapability(core.Capability{
    Name: "current_weather",
    Description: "Gets current weather conditions for a location.",
    Handler: handleWeather,

    // Add structured field hints for AI
    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {Name: "location", Type: "string", Example: "London"},
        },
        OptionalFields: []core.FieldHint{
            {Name: "units", Type: "string", Example: "metric"},
        },
    },
})
```

**Phase 3 - Enable Validation** (Optional, for high-reliability scenarios):
```go
// In your agent, enable schema caching for validation
if redisURL := os.Getenv(core.EnvRedisURL); redisURL != "" {
    redisOpt, _ := redis.ParseURL(redisURL)
    redisClient := redis.NewClient(redisOpt)
    agent.SchemaCache = core.NewSchemaCache(redisClient)
}

// Enable validation via environment variable
// export TRUVAG3_VALIDATE_PAYLOADS=true
```

#### How Agents Use This

When your agent discovers a tool and needs to generate a payload:

```go
// Agent automatically chooses the best approach:
// 1. If InputSummary exists → Use Phase 2 (field hints)
// 2. Otherwise → Use Phase 1 (description)
// 3. If TRUVAG3_VALIDATE_PAYLOADS=true → Validate with Phase 3

payload, err := agent.generateToolPayload(ctx, userRequest, capability)
// Returns: {"location": "London", "units": "metric"}
```

#### When to Use Each Phase

| Your Needs | Phases to Use | Setup Time | Accuracy |
|-----------|---------------|------------|----------|
| **Quick prototype** | Phase 1 only | 30 seconds | ~85-90% |
| **Production tools** | Phase 1 + 2 | 2 minutes | ~95% |
| **Mission-critical** | Phase 1 + 2 + 3 | 5 minutes | ~99% |

#### Key Benefits

1. **Progressive Enhancement**: Start simple (Phase 1), add accuracy as needed (Phase 2), add validation for safety (Phase 3)
2. **Zero Breaking Changes**: Phase 1 works everywhere, Phase 2 and 3 are optional additions
3. **Shared Cache**: Phase 3 schemas cached in Redis, shared across all agent replicas
4. **AI Optimized**: Descriptions are "implicit prompts" that guide AI generation (based on 2024 LLM research)

#### Learn More

For a complete deep-dive including:
- Detailed architecture explanation
- Implementation walkthroughs
- Performance benchmarks
- Migration guides
- Best practices

See the comprehensive guide: [Tool Schema Discovery Guide](../docs/TOOL_SCHEMA_DISCOVERY_GUIDE.md)

#### Quick Setup Checklist

For a new tool:
- [ ] Add clear description to capability (Phase 1) - **Required**
- [ ] Add InputSummary with field hints (Phase 2) - **Recommended**
- [ ] Document your tool's behavior

For an AI agent:
- [ ] Generate payloads using AI + capability metadata
- [ ] Optional: Enable schema cache for validation (Phase 3)
- [ ] Optional: Set `TRUVAG3_VALIDATE_PAYLOADS=true` for validation

### 🎯 Best Practices for Production

1. **Always configure timeouts**:
   ```go
   // Set timeouts via config
   config.HTTP.ReadTimeout = 30 * time.Second
   config.HTTP.WriteTimeout = 30 * time.Second
   ```

2. **Use structured logging**:
   ```go
   logger.Info("Operation completed", map[string]interface{}{
       "duration": time.Since(start),
       "records":  count,
   })
   ```

3. **Handle errors properly**:
   ```go
   if core.IsRetryable(err) {
       return retryWithExponentialBackoff()
   }
   ```

4. **Set up health checks**:
   ```go
   // Components get /health automatically
   // Add custom checks for dependencies
   ```

5. **Configure CORS carefully**:
   ```go
   // Production: specific origins
   AllowedOrigins: []string{"https://app.example.com"}
   // Development: localhost
   AllowedOrigins: []string{"http://localhost:*"}
   ```

6. **Understand TTL and Heartbeat Behavior**:
   ```go
   // When using Redis discovery, components auto-register with a 30-second TTL
   // Framework automatically starts heartbeat every 15 seconds to keep registration alive
   
   // ✅ GOOD: Framework-managed components (automatic heartbeat)
   // Set environment: export REDIS_URL="redis://localhost:6379"
   framework, _ := core.NewFramework(tool,
       core.WithDiscovery(true, "redis"),
       core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
   )
   framework.Run(ctx) // Auto-starts heartbeat, keeps component alive
   
   // ⚠️  CAREFUL: Manual registration (no automatic heartbeat)
   tool.Registry.Register(ctx, serviceInfo) // Expires after 30 seconds without heartbeat
   
   // 🔍 For monitoring: Components without heartbeat disappear after 30 seconds
   // This helps automatically clean up crashed or stopped components
   ```

## 7. Understanding Component Registration and Discovery

### How Tools Register Themselves

Tools announce their existence but can't look for others:

```go
tool := core.NewTool("weather-tool")

// Framework automatically initializes Registry for tools when discovery is enabled
// Set environment: export REDIS_URL="redis://localhost:6379"
framework, _ := core.NewFramework(tool,
    core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
    core.WithDiscovery(true, "redis"), // Enables registration
)

// After framework.Run(), tool.Registry is automatically set!
// The tool registers itself and starts heartbeat automatically
// Other agents can find it, but it can't find others

// Manual setup still works (for advanced use cases)
// registry, _ := core.NewRedisRegistry(os.Getenv("REDIS_URL"))  // e.g., "redis://localhost:6379"
// tool.Registry = registry // Manual assignment (framework respects this)
```

### How Agents Discover Components

Agents can find both tools and other agents:

```go
agent := core.NewBaseAgent("coordinator")

// Framework automatically initializes Discovery for agents when discovery is enabled
// Set environment: export REDIS_URL="redis://localhost:6379"
framework, _ := core.NewFramework(agent,
    core.WithRedisURL(os.Getenv("REDIS_URL")),  // e.g., "redis://localhost:6379"
    core.WithDiscovery(true, "redis"), // Enables discovery
)

// After framework.Run(), agent.Discovery is automatically set!
// The agent can immediately start discovering other components

// Find tools for specific tasks
weatherTools, _ := agent.Discover(ctx, core.DiscoveryFilter{
    Type: core.ComponentTypeTool,
    Capabilities: []string{"weather_forecast"},
})

// Find other agents for delegation
aiAgents, _ := agent.Discover(ctx, core.DiscoveryFilter{
    Type: core.ComponentTypeAgent,
    Capabilities: []string{"natural_language_processing"},
})

// Manual setup still works (for advanced use cases)
// discovery, _ := core.NewRedisDiscovery(os.Getenv("REDIS_URL"))  // e.g., "redis://localhost:6379"
// agent.Discovery = discovery // Manual assignment (framework respects this)
```

## 8. Architecture Patterns

### Pattern 1: Tool Collection with Agent Coordinator

```
┌─────────────────────────────────────────┐
│           Orchestrator Agent            │
│         (Discovers & Coordinates)       │
└────────────────┬────────────────────────┘
                 │ Discovers
    ┌────────────┼────────────┬───────────┐
    ▼            ▼            ▼           ▼
┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐
│Calculator││ Weather  ││Database ││Translator│
│  Tool    ││  Tool    ││  Tool   ││  Tool    │
└─────────┘ └─────────┘ └─────────┘ └─────────┘
```

### Pattern 2: Hierarchical Agents

```
┌─────────────────────────────────────────┐
│          Master Agent (AI-Powered)      │
└────────────────┬────────────────────────┘
                 │ Discovers & Delegates
    ┌────────────┼────────────┐
    ▼            ▼            ▼
┌─────────┐ ┌─────────┐ ┌─────────┐
│  Data   ││Analytics ││ Report   │
│  Agent  ││  Agent   ││  Agent   │
└────┬────┘ └────┬────┘ └────┬────┘
     │           │           │
  [Tools]    [Tools]     [Tools]
```

## 9. Advanced Features

### Kubernetes Support

Both Tools and Agents work seamlessly in Kubernetes:

```go
// Configuration automatically detects Kubernetes
config := core.DefaultConfig()
config.Kubernetes.ServiceName = "calculator-tool"
config.Kubernetes.ServicePort = 80

tool := core.NewToolWithConfig(config)
// Registers as: calculator-tool.namespace.svc.cluster.local:80
```

### Component Filtering

Agents can filter discoveries by multiple criteria:

```go
// Find specific tools
mathTools, _ := agent.Discover(ctx, core.DiscoveryFilter{
    Type:         core.ComponentTypeTool,
    Capabilities: []string{"add", "multiply"},
    Name:         "calculator",
})

// Find agents in specific namespace
productionAgents, _ := agent.Discover(ctx, core.DiscoveryFilter{
    Type: core.ComponentTypeAgent,
    Metadata: map[string]interface{}{
        "environment": "production",
    },
})
```

## 10. Best Practices

### 1. Choose the Right Component Type

**Use a Tool when:**
- Building a single-purpose function
- Creating a stateless processor
- Implementing a pure calculation or transformation
- Building something that responds to requests

**Use an Agent when:**
- Orchestrating multiple components
- Making decisions based on discovery
- Implementing workflows
- Building something that initiates actions

### 2. Keep Tools Simple

```go
// ✅ GOOD: Tool does one thing
type CalculatorTool struct {
    *core.BaseTool
}

func (c *CalculatorTool) Add(a, b float64) float64 {
    return a + b
}

// ❌ BAD: Tool trying to do too much
type BadTool struct {
    *core.BaseTool
}

func (b *BadTool) ProcessOrder() {
    // Trying to calculate, save to DB, send email...
    // This should be an Agent orchestrating multiple tools!
}
```

### 3. Use Agents for Coordination

```go
// ✅ GOOD: Agent orchestrating tools
type OrderAgent struct {
    *core.BaseAgent
}

func (o *OrderAgent) ProcessOrder(ctx context.Context, order Order) error {
    // Find calculator tool
    calc, _ := o.Discover(ctx, core.DiscoveryFilter{
        Type: core.ComponentTypeTool,
        Name: "calculator",
    })
    // Call it for tax calculation
    
    // Find database tool
    db, _ := o.Discover(ctx, core.DiscoveryFilter{
        Type: core.ComponentTypeTool,
        Name: "database",
    })
    // Call it to save order
    
    // Find email tool
    email, _ := o.Discover(ctx, core.DiscoveryFilter{
        Type: core.ComponentTypeTool,
        Name: "email",
    })
    // Call it to send confirmation
    
    return nil
}
```

## 11. Common Patterns and Solutions

### Pattern: AI-Enhanced Tool
```go
// A tool that uses AI internally but doesn't orchestrate
translator := ai.NewAITool("translator", apiKey)
translator.RegisterCapability(core.Capability{
    Name: "translate",
    Description: "Translates text using AI",
    Handler: translateHandler,
})
// Can use AI but can't discover other components
```

### Pattern: Workflow Agent
```go
// An agent that orchestrates a complex workflow
workflow := core.NewBaseAgent("workflow-engine")
// Can discover and coordinate multiple tools and agents
```

### Pattern: Gateway Agent
```go
// An agent that acts as a gateway to multiple tools
gateway := core.NewBaseAgent("api-gateway")
// Discovers available tools and routes requests
```

## 12. Debugging and Monitoring

### Check Component Registration

```bash
# See what's registered in Redis
redis-cli KEYS "truvag3:*"

# Check specific component
redis-cli GET "truvag3:services:calculator-tool-abc123"
```

### Component Health Checks

All components automatically provide health endpoints:
- Tools: `http://localhost:8080/health`
- Agents: `http://localhost:8090/health`

## 13. Performance Considerations

### Tools are Lightweight
- Minimal overhead (~5MB binary)
- No discovery overhead
- Fast startup
- Perfect for serverless/FaaS

### Agents are Feature-Rich
- Full discovery capability (~10MB binary)
- Can coordinate complex workflows
- Suitable for long-running orchestrators
- Ideal for AI-powered coordination

### Production Reliability
- **Redis Failure Resilience**: Components automatically handle Redis outages and recover without manual intervention
- **Background Retry**: Services automatically retry Redis connection failures without blocking startup
- **Self-Healing Discovery**: Services re-register themselves when Redis comes back online
- **Fast Startup**: Initial retry reduced to ~7-10 seconds (down from 18s) for faster component startup
- **Exponential Backoff**: Background retry with intelligent backoff (30s → 60s → 120s → 300s cap)
- **Atomic Operations**: Registration uses Redis transactions to prevent partial state issues
- **Zero Downtime**: Services remain functional even when Redis is unavailable during startup

## 14. Summary

The Truva-G3 Core module provides two fundamental building blocks:

1. **Tools** - Passive components that do one thing well
2. **Agents** - Active orchestrators that discover and coordinate

This clear separation enables:
- Clean, maintainable architecture
- Predictable component behavior
- Easy testing and debugging
- Scalable AI-powered systems

Remember: **Tools work, Agents think!**

## 15. Next Steps

- Explore the [AI Module](../ai/README.md) for AI-enhanced components
- Learn about [Orchestration](../orchestration/README.md) for complex workflows
- Add [Telemetry](../telemetry/README.md) for observability
- Implement [Resilience](../resilience/README.md) for production readiness

Happy building! 🚀