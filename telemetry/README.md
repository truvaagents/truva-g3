# Truva-G3 Telemetry Module

Welcome to the observability powerhouse of Truva-G3! Think of this guide as your friendly companion who'll walk you through every aspect of telemetry, from the simplest metric to sophisticated production monitoring. Grab a coffee and let's dive in! ☕

## Table of Contents

1. [What Is Telemetry and Why Should You Care?](#1-what-is-telemetry-and-why-should-you-care)
2. [The Simplest Thing That Works](#2-the-simplest-thing-that-works)
3. [The Three Types of Metrics](#3-the-three-types-of-metrics-and-when-to-use-each)
4. [Adding Context with Labels](#4-adding-context-with-labels)
5. [Progressive Disclosure: From Simple to Advanced](#5-progressive-disclosure-from-simple-to-advanced)
6. [Service Type Labeling](#6-service-type-labeling)
7. [Production-Ready Configuration](#7-production-ready-configuration)
8. [Deploying with Docker](#8-deploying-with-docker)
9. [Deploying on Kubernetes](#9-deploying-on-kubernetes)
10. [Adding Telemetry to Tools and Agents](#10-adding-telemetry-to-tools-and-agents)
11. [The Architecture Under the Hood](#11-the-architecture-under-the-hood)
12. [Production Safety Features](#12-production-safety-features)
13. [Testing Your Telemetry](#13-testing-your-telemetry)
14. [Debugging Telemetry Issues](#14-debugging-telemetry-issues)
15. [Advanced Patterns](#15-advanced-patterns)
16. [Best Practices Summary](#16-best-practices-summary)
17. [Quick Reference](#17-quick-reference)
18. [Unified Metrics API](#18-unified-metrics-api)
19. [Distributed Tracing](#19-distributed-tracing)
    - [Comprehensive Guide](../docs/DISTRIBUTED_TRACING_GUIDE.md)
20. [AI Module Distributed Tracing](#20-ai-module-distributed-tracing)
21. [Push-Based Telemetry Limitations](#21-push-based-telemetry-limitations)
22. [Summary](#22-summary)

## 1. What Is Telemetry and Why Should You Care?

Let me explain this with a story that everyone can relate to.

### The Dashboard Analogy

Imagine you're driving a car. Your dashboard tells you:
- **Speed** - How fast you're going
- **Fuel** - How much gas you have left
- **Temperature** - If your engine is overheating
- **Warning Lights** - If something needs attention

Without this dashboard, you'd be driving blind. You wouldn't know if you're about to run out of gas or if your engine is about to overheat.

**That's exactly what telemetry does for your software!** It gives you a dashboard to see what's happening inside your running application:
- **Metrics** tell you the numbers (requests per second, error rates)
- **Traces** show you the journey (how a request flows through your system)
- **Logs** capture the details (what happened and when)
- **Health checks** warn you of problems (circuit breakers, failures)

### Why Every Application Needs Telemetry

Think about these scenarios:
1. **Your app is slow** - But which part? Database? Network? Your code?
2. **Users report errors** - But you can't reproduce them locally
3. **Memory keeps growing** - But you don't know what's leaking
4. **Your service crashed at 3 AM** - But you were asleep

Without telemetry, you're debugging in the dark. With telemetry, you have X-ray vision into your application.

## 2. The Simplest Thing That Works

Let's start with the absolute basics. Here's how to add telemetry to your application in 30 seconds:

```go
package main

import (
    "context"
    "time"

    "github.com/truvaagents/truva-g3/telemetry"
)

func main() {
    // Step 1: Initialize telemetry (one line!)
    telemetry.Initialize(telemetry.Config{
        ServiceName: "my-app",
        Enabled:     true,
    })

    // Step 2: Always clean up when your app exits
    defer telemetry.Shutdown(context.Background())

    // Step 3: Emit metrics anywhere in your code
    telemetry.Counter("app.started")

    // That's it! You're now tracking metrics
    processRequest()
}

func processRequest() {
    // Track how long something takes
    start := time.Now()
    defer telemetry.Duration("request.duration_ms", start)

    // Count events
    telemetry.Counter("request.received")

    // Do your actual work here
    time.Sleep(100 * time.Millisecond)

    // Track success
    telemetry.Counter("request.success")
}
```

**That's literally all you need to start!** No complex setup, no configuration files, no external dependencies to install. The telemetry module handles everything internally.

## 3. The Three Types of Metrics (And When to Use Each)

Just like there are different tools in a toolbox, there are different types of metrics for different jobs:

### 1. Counters - Things That Only Go Up
Think of counters like the odometer in your car - they only increase, never decrease.

```go
// Perfect for counting events
telemetry.Counter("user.login")
telemetry.Counter("api.request", "endpoint", "/users")
telemetry.Counter("error.occurred", "type", "database")
```

**Use counters when you want to know "how many times did this happen?"**

### 2. Histograms - Distributions of Values
Think of histograms like a speed chart - they show you the range and frequency of values.

```go
// Perfect for measuring durations, sizes, or amounts
telemetry.Histogram("response.time_ms", 125.5)
telemetry.Histogram("payload.size_bytes", 2048)
telemetry.Histogram("batch.size", 50)
```

**Use histograms when you want to know "what's the typical value, and what's the range?"**

The beauty of histograms is they automatically calculate:
- Average (mean)
- Median (50th percentile)
- 95th and 99th percentiles
- Min and max values

### 3. Gauges - Values That Go Up and Down
Think of gauges like the fuel gauge in your car - they can increase or decrease.

```go
// Perfect for current state metrics
telemetry.Gauge("memory.used_mb", 512)
telemetry.Gauge("queue.size", 1500)
telemetry.Gauge("active.connections", 42)
```

**Use gauges when you want to know "what's the current value right now?"**

## 4. Adding Context with Labels

Labels are like tags on your metrics - they add context and allow you to filter and group your data.

### The Restaurant Menu Analogy
Imagine you run a restaurant and track "orders". That's good, but wouldn't it be better to know:
- **What** was ordered (pizza, pasta, salad)
- **When** it was ordered (lunch, dinner)
- **How** it was ordered (dine-in, takeout, delivery)

That's exactly what labels do for your metrics:

```go
// Without labels - not very useful
telemetry.Counter("orders")  // Total orders... but what kind?

// With labels - now we're talking!
telemetry.Counter("orders",
    "item", "pizza",
    "time", "dinner",
    "type", "delivery")

// You can now answer questions like:
// - How many pizzas were ordered at dinner?
// - What's our most popular delivery item?
// - Is lunch or dinner busier?
```

### Label Best Practices

```go
// ✅ GOOD: Low cardinality labels (limited set of values)
telemetry.Counter("api.request",
    "method", "GET",        // Only ~5 values (GET, POST, PUT, DELETE, PATCH)
    "status", "200",        // Only ~10 values (200, 201, 400, 404, 500, etc.)
    "endpoint", "/users")   // Only ~20-50 endpoints in your API

// ❌ BAD: High cardinality labels (unlimited unique values)
telemetry.Counter("api.request",
    "user_id", "12345",     // Could be millions of unique values!
    "timestamp", "1234567", // Every request has a unique timestamp!
    "request_id", "abc123") // Every request has a unique ID!
```

**Why does cardinality matter?** Each unique combination of labels creates a new metric series. Too many series = memory explosion!

## 5. Progressive Disclosure: From Simple to Advanced

The telemetry module follows the principle of progressive disclosure - start simple, add complexity only when needed.

### Level 1: Dead Simple (90% of Your Needs)
```go
// Just emit metrics - that's it!
telemetry.Counter("events.processed")
telemetry.Histogram("processing.time_ms", 45.2)
telemetry.Gauge("queue.depth", 100)
```

### Level 2: With Context (For Distributed Systems)
```go
// Add tracing context for distributed systems
ctx := telemetry.WithBaggage(ctx,
    "request_id", "req-123",
    "user_id", "user-456")

// Metrics now include trace context
telemetry.EmitWithContext(ctx, "payment.processed", 99.99)
```

### Level 3: Full Control (When You Need It)
```go
// Declare metrics upfront for validation
telemetry.DeclareMetrics("payment", telemetry.ModuleConfig{
    Metrics: []telemetry.MetricDefinition{
        {
            Name:    "payment.amount",
            Type:    "histogram",
            Help:    "Payment amounts in USD",
            Labels:  []string{"currency", "method"},
            Unit:    "dollars",
            Buckets: []float64{1, 10, 100, 1000, 10000},
        },
    },
})

// Use advanced emission options
telemetry.EmitWithOptions(ctx, "payment.amount", 99.99,
    telemetry.WithUnit(telemetry.UnitDollars),
    telemetry.WithSampleRate(0.1),  // Sample 10% for high-volume metrics
    telemetry.WithTimestamp(eventTime),
)
```

## 6. Service Type Labeling

The telemetry module automatically labels metrics with a `service_type` attribute that distinguishes between "tool" and "agent" services. This enables Grafana dashboard segregation and filtering by component type.

### Automatic Service Type Inference

When you create a component using `core.NewTool()` or `core.NewBaseAgent()`, the framework automatically tracks the component type. If you initialize telemetry **after** creating the component, the service type is auto-inferred:

```go
func main() {
    // Create component FIRST - this sets the component type
    tool := core.NewTool("weather-service")  // Sets type to "tool"

    // Initialize telemetry AFTER - auto-infers service_type="tool"
    config := telemetry.UseProfile(telemetry.ProfileProduction)
    config.ServiceName = "weather-service"
    telemetry.Initialize(config)

    // All metrics now include service_type="tool" label
}
```

### Manual Service Type Configuration

You can also explicitly set the service type if needed:

```go
config := telemetry.Config{
    ServiceName: "my-service",
    ServiceType: "tool",  // Explicit: "tool" or "agent"
}
telemetry.Initialize(config)
```

### Initialization Order Matters

For automatic inference to work correctly, follow this pattern:

```go
// ✅ CORRECT: Component created before telemetry
tool := NewMyTool()           // 1. Sets component type
initTelemetry("my-service")   // 2. Reads component type

// ❌ WRONG: Telemetry initialized first
initTelemetry("my-service")   // 1. No component type yet (empty)
tool := NewMyTool()           // 2. Too late, telemetry already initialized
```

### Grafana Dashboard Filtering

With service type labeling, you can filter metrics by component type in Prometheus/Grafana:

```promql
# Request rate for tools only
sum(rate(truvag3_requests_total{service_type="tool"}[5m])) by (service_name)

# Request rate for agents only
sum(rate(truvag3_requests_total{service_type="agent"}[5m])) by (service_name)

# Compare tool vs agent latencies
histogram_quantile(0.95,
  sum(rate(truvag3_request_duration_ms_bucket[5m])) by (le, service_type)
)
```

## 7. Production-Ready Configuration

When you're ready to deploy to production, you need more sophisticated configuration. Let me show you how to set up telemetry that adapts to different environments.

### The Smart Configuration Pattern

Think of this like your phone's battery saver mode:
- **Development**: Full brightness, all features on (capture everything for debugging)
- **Staging**: Balanced mode (good visibility, moderate resource usage)
- **Production**: Power saving mode (minimal overhead, only essential metrics)

Here's how to implement environment-aware configuration:

```go
package main

import (
    "context"
    "log"
    "os"
    "time"

    "github.com/truvaagents/truva-g3/telemetry"
)

// initTelemetry sets up telemetry based on your environment
func initTelemetry(serviceName string) {
    // Detect environment from APP_ENV variable
    env := os.Getenv("APP_ENV")
    if env == "" {
        env = "development" // Safe default
    }

    // Select the appropriate profile
    var profile telemetry.Profile
    switch env {
    case "production", "prod":
        profile = telemetry.ProfileProduction
    case "staging", "stage", "qa":
        profile = telemetry.ProfileStaging
    default:
        profile = telemetry.ProfileDevelopment
    }

    // Use the profile to get base configuration
    config := telemetry.UseProfile(profile)

    // Override with your service name
    config.ServiceName = serviceName

    // Allow environment variables to override specific settings
    if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
        config.Endpoint = endpoint
    }

    // Initialize telemetry
    if err := telemetry.Initialize(config); err != nil {
        // IMPORTANT: Don't let telemetry failures crash your app!
        log.Printf("WARNING: Telemetry initialization failed: %v", err)
        log.Printf("Application will continue without telemetry")
        return
    }

    log.Printf("✅ Telemetry initialized successfully")
    log.Printf("   Environment: %s", env)
    log.Printf("   Profile: %s", profile)
    log.Printf("   Service: %s", serviceName)
    log.Printf("   Endpoint: %s", config.Endpoint)
}

func main() {
    // Initialize telemetry with environment detection
    initTelemetry("my-service")

    // Always clean up
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := telemetry.Shutdown(ctx); err != nil {
            log.Printf("Warning: Telemetry shutdown error: %v", err)
        }
    }()

    // Your application code here
    runApplication()
}

func runApplication() {
    // Your main application logic here
    // This is where your actual service runs
}
```

### Understanding Telemetry Profiles

The module comes with three pre-configured profiles that represent common deployment scenarios:

#### Development Profile (Maximum Visibility)
```go
// ProfileDevelopment - Capture everything for debugging
// - 100% sampling (see every request)
// - No circuit breaker (don't hide problems)
// - High cardinality limits (track everything)
// - Local endpoint (localhost:4318)
config := telemetry.UseProfile(telemetry.ProfileDevelopment)
```

#### Staging Profile (Balanced Approach)
```go
// ProfileStaging - Good visibility with reasonable overhead
// - 10% sampling (see enough to understand patterns)
// - Circuit breaker enabled (protect the telemetry backend)
// - Moderate cardinality limits
// - Staging collector endpoint
config := telemetry.UseProfile(telemetry.ProfileStaging)
```

#### Production Profile (Optimized for Scale)
```go
// ProfileProduction - Minimal overhead, maximum reliability
// - 0.1% sampling (tiny overhead for high-volume services)
// - Aggressive circuit breaker (fail fast if backend is down)
// - Strict cardinality limits (prevent memory explosion)
// - Production collector endpoint
config := telemetry.UseProfile(telemetry.ProfileProduction)
```

### The Three-Tier Configuration System

Configuration follows a clear priority order (like CSS cascading):

```go
// Priority 1: Explicit configuration (highest priority)
config := telemetry.Config{
    ServiceName: "my-service",
    Endpoint:    "my-collector:4318",  // This wins
}

// Priority 2: Environment variables (medium priority)
// export OTEL_EXPORTER_OTLP_ENDPOINT=env-collector:4318
// If no explicit endpoint, this is used

// Priority 3: Profile defaults (lowest priority)
// If nothing else is set, profile defaults are used
```

Here's a complete example:

```go
func configureTelemetry() telemetry.Config {
    // Start with a profile
    config := telemetry.UseProfile(telemetry.ProfileProduction)

    // Override with explicit values
    config.ServiceName = "payment-service"

    // Environment variables can override
    if endpoint := os.Getenv("TELEMETRY_ENDPOINT"); endpoint != "" {
        config.Endpoint = endpoint
    }

    // Feature flags can control behavior
    if os.Getenv("TELEMETRY_DEBUG") == "true" {
        config.Verbose = true  // Enable verbose logging for debugging
    }

    return config
}
```

## 8. Deploying with Docker

Here's how to configure telemetry for containerized applications:

```dockerfile
# Dockerfile
FROM golang:1.21 AS builder
WORKDIR /app
COPY . .
RUN go build -o myapp .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/

# Copy the binary
COPY --from=builder /app/myapp .

# Set default environment
ENV APP_ENV=production
ENV OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4318

CMD ["./myapp"]
```

```yaml
# docker-compose.yml
version: '3.8'

services:
  myapp:
    build: .
    environment:
      - APP_ENV=staging
      - OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4318
      - SERVICE_NAME=my-service
    depends_on:
      - otel-collector

  otel-collector:
    image: otel/opentelemetry-collector-contrib:latest
    ports:
      - "4318:4318"  # HTTP
      - "4317:4317"  # gRPC
    volumes:
      - ./otel-config.yaml:/etc/otel/config.yaml
    command: ["--config", "/etc/otel/config.yaml"]
```

## 9. Deploying on Kubernetes

For Kubernetes deployments, use ConfigMaps and environment variables:

```yaml
# configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  APP_ENV: "production"
  OTEL_EXPORTER_OTLP_ENDPOINT: "otel-collector.monitoring:4318"

---
# deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-service
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: app
        image: my-service:latest
        envFrom:
        - configMapRef:
            name: app-config
        env:
        - name: SERVICE_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_IP
          valueFrom:
            fieldRef:
              fieldPath: status.podIP
```

## 10. Adding Telemetry to Tools and Agents

Now let's see how telemetry integrates with Truva-G3's core components - Tools and Agents.

### Adding Telemetry to a Tool

Remember: Tools are passive components that do one thing well. Here's how to add comprehensive telemetry:

```go
package main

import (
    "context"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

// WeatherTool fetches weather data (passive component)
type WeatherTool struct {
    *core.BaseTool
}

func NewWeatherTool() *WeatherTool {
    tool := &WeatherTool{
        BaseTool: core.NewTool("weather"),
    }

    // Declare the metrics this tool will emit
    telemetry.DeclareMetrics("weather", telemetry.ModuleConfig{
        Metrics: []telemetry.MetricDefinition{
            {
                Name:   "weather.api.calls",
                Type:   "counter",
                Help:   "Number of weather API calls",
                Labels: []string{"city", "status"},
            },
            {
                Name:    "weather.api.latency_ms",
                Type:    "histogram",
                Help:    "Weather API response time",
                Labels:  []string{"city"},
                Unit:    "milliseconds",
                Buckets: []float64{50, 100, 250, 500, 1000, 2000},
            },
            {
                Name:   "weather.cache.hits",
                Type:   "counter",
                Help:   "Weather data cache hits",
            },
            {
                Name:   "weather.temperature",
                Type:   "gauge",
                Help:   "Current temperature reading",
                Labels: []string{"city", "unit"},
            },
        },
    })

    return tool
}

// Weather represents weather data (example type for demonstration)
type Weather struct {
    Temperature float64
    City        string
    Conditions  string
}

// GetWeather demonstrates comprehensive telemetry in a tool
func (w *WeatherTool) GetWeather(ctx context.Context, city string) (*Weather, error) {
    // Track the overall operation
    start := time.Now()
    defer func() {
        telemetry.Histogram("weather.api.latency_ms",
            float64(time.Since(start).Milliseconds()),
            "city", city)
    }()

    // Check cache first
    if cached := w.checkCache(city); cached != nil {
        telemetry.Counter("weather.cache.hits")
        return cached, nil
    }

    // Track API call
    telemetry.Counter("weather.api.calls",
        "city", city,
        "status", "started")

    // Make the actual API call
    weather, err := w.callWeatherAPI(city)

    if err != nil {
        // Track failures
        telemetry.Counter("weather.api.calls",
            "city", city,
            "status", "error")
        telemetry.RecordError("weather.api.error", err.Error())
        return nil, err
    }

    // Track success
    telemetry.Counter("weather.api.calls",
        "city", city,
        "status", "success")

    // Track the actual temperature (business metric)
    telemetry.Gauge("weather.temperature",
        weather.Temperature,
        "city", city,
        "unit", "fahrenheit")

    // Cache the result
    w.updateCache(city, weather)

    return weather, nil
}

// Helper methods (example implementations)
func (w *WeatherTool) checkCache(city string) *Weather {
    // Your cache implementation here
    return nil
}

func (w *WeatherTool) callWeatherAPI(city string) (*Weather, error) {
    // Your API call implementation here
    return &Weather{Temperature: 72, City: city, Conditions: "Sunny"}, nil
}

func (w *WeatherTool) updateCache(city string, weather *Weather) {
    // Your cache update implementation here
}
```

### Adding Telemetry to an Agent

Agents are active orchestrators that coordinate multiple components. They need different telemetry patterns:

```go
package main

import (
    "context"
    "sync"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

// TravelAgent orchestrates travel planning (active component)
type TravelAgent struct {
    *core.BaseAgent
}

// TripPlan represents a travel plan (example type for demonstration)
type TripPlan struct {
    Destination string
    Weather     *Weather
    Flights     []Flight
    Hotels      []Hotel
}

// Flight and Hotel are example types for demonstration
type Flight struct {
    Number string
    Price  float64
}

type Hotel struct {
    Name  string
    Price float64
}

func NewTravelAgent() *TravelAgent {
    agent := &TravelAgent{
        BaseAgent: core.NewBaseAgent("travel-agent"),
    }

    // Declare metrics for orchestration patterns
    telemetry.DeclareMetrics("travel-agent", telemetry.ModuleConfig{
        Metrics: []telemetry.MetricDefinition{
            {
                Name:   "agent.orchestrations",
                Type:   "counter",
                Help:   "Number of orchestrations performed",
                Labels: []string{"workflow", "status"},
            },
            {
                Name:   "agent.tools.discovered",
                Type:   "gauge",
                Help:   "Number of tools discovered",
                Labels: []string{"type"},
            },
            {
                Name:   "agent.workflow.steps",
                Type:   "counter",
                Help:   "Workflow steps executed",
                Labels: []string{"workflow", "step", "status"},
            },
            {
                Name:    "agent.workflow.duration_ms",
                Type:    "histogram",
                Help:    "Total workflow execution time",
                Labels:  []string{"workflow"},
                Buckets: []float64{100, 500, 1000, 5000, 10000, 30000},
            },
        },
    })

    return agent
}

// PlanTrip demonstrates agent orchestration with telemetry
func (a *TravelAgent) PlanTrip(ctx context.Context, destination string) (*TripPlan, error) {
    // Track the entire orchestration
    start := time.Now()
    defer func() {
        telemetry.Histogram("agent.workflow.duration_ms",
            float64(time.Since(start).Milliseconds()),
            "workflow", "plan_trip")
    }()

    // Add context for distributed tracing
    ctx = telemetry.WithBaggage(ctx,
        "workflow", "plan_trip",
        "destination", destination,
        "agent_id", a.GetID())

    telemetry.Counter("agent.orchestrations",
        "workflow", "plan_trip",
        "status", "started")

    // Step 1: Discover available tools
    telemetry.Counter("agent.workflow.steps",
        "workflow", "plan_trip",
        "step", "discovery",
        "status", "started")

    tools, err := a.Discover(ctx, core.DiscoveryFilter{
        Type: core.ComponentTypeTool,
    })

    if err != nil {
        telemetry.Counter("agent.workflow.steps",
            "workflow", "plan_trip",
            "step", "discovery",
            "status", "error")
        return nil, err
    }

    telemetry.Gauge("agent.tools.discovered",
        float64(len(tools)),
        "type", "all")

    // Step 2: Orchestrate tools in parallel
    telemetry.Counter("agent.workflow.steps",
        "workflow", "plan_trip",
        "step", "orchestration",
        "status", "started")

    var wg sync.WaitGroup
    results := &TripPlan{}

    // Track concurrent operations
    telemetry.Gauge("agent.concurrent_operations", 3)
    defer telemetry.Gauge("agent.concurrent_operations", -3)

    // Get weather (tool invocation)
    wg.Add(1)
    go func() {
        defer wg.Done()
        weatherTool := a.findTool(tools, "weather")
        if weatherTool != nil {
            telemetry.Counter("agent.tool.invocation",
                "tool", "weather",
                "status", "started")
            // Invoke tool...
        }
    }()

    // Get flights (tool invocation)
    wg.Add(1)
    go func() {
        defer wg.Done()
        flightTool := a.findTool(tools, "flights")
        if flightTool != nil {
            telemetry.Counter("agent.tool.invocation",
                "tool", "flights",
                "status", "started")
            // Invoke tool...
        }
    }()

    // Get hotels (tool invocation)
    wg.Add(1)
    go func() {
        defer wg.Done()
        hotelTool := a.findTool(tools, "hotels")
        if hotelTool != nil {
            telemetry.Counter("agent.tool.invocation",
                "tool", "hotels",
                "status", "started")
            // Invoke tool...
        }
    }()

    wg.Wait()

    telemetry.Counter("agent.orchestrations",
        "workflow", "plan_trip",
        "status", "success")

    return results, nil
}

// Helper method to find a tool by name (example implementation)
func (a *TravelAgent) findTool(tools []*core.ServiceInfo, name string) *core.ServiceInfo {
    for _, tool := range tools {
        if tool.Name == name {
            return tool
        }
    }
    return nil
}
```

## 11. The Architecture Under the Hood

Let me explain how telemetry works internally, using an analogy everyone understands.

### The Post Office Analogy

Think of the telemetry system like a post office:

```
Your Code (Sender) → Envelope (Metric) → Post Office (Registry) → Delivery Truck (Exporter) → Destination (Collector)
```

Here's what happens when you emit a metric:

```go
telemetry.Counter("request.count")  // You drop a letter in the mailbox
```

1. **The Registry (Post Office)**: Receives your metric and checks if it's valid
2. **The Circuit Breaker (Safety System)**: Ensures the post office isn't overwhelmed
3. **The Cardinality Limiter (Size Checker)**: Makes sure you're not sending a package that's too big
4. **The Exporter (Delivery Truck)**: Batches metrics and sends them to the collector
5. **The Collector (Destination)**: Receives and stores your metrics

### The Three-Layer Architecture

```
┌─────────────────────────────────────────┐
│         Simple API Layer                 │  ← What you use (Counter, Histogram, Gauge)
│    Emit(), Counter(), Histogram()        │
├─────────────────────────────────────────┤
│        Smart Registry Layer              │  ← Manages everything
│   Thread-safe, Cardinality limits        │     (You never see this)
├─────────────────────────────────────────┤
│     OpenTelemetry Provider Layer         │  ← Does the actual work
│    HTTP export to collectors             │     (Handles the complexity)
└─────────────────────────────────────────┘
```

## 12. Production Safety Features

The telemetry module includes several safety features to protect your application in production:

### 1. The Circuit Breaker (Automatic Failure Protection)

Just like a circuit breaker in your house prevents electrical overload, the telemetry circuit breaker prevents a failing metrics backend from affecting your application:

```go
// The circuit breaker has three states:
// CLOSED: Normal operation, metrics flow through
// OPEN: Backend is down, metrics are dropped (fail fast)
// HALF-OPEN: Testing if backend recovered

// You don't need to configure this manually, but here's how it works:
config := telemetry.Config{
    CircuitBreaker: telemetry.CircuitConfig{
        Enabled:      true,
        MaxFailures:  5,              // Open after 5 failures
        RecoveryTime: 30 * time.Second, // Try again after 30 seconds
    },
}

// In your code, you just emit metrics normally
telemetry.Counter("my.metric")  // If circuit is open, this returns immediately
```

### 2. Cardinality Limits (Memory Protection)

High cardinality can cause memory explosions. The module protects you automatically:

```go
// BAD: User ID as a label creates millions of metric series
for _, userID := range users {
    telemetry.Counter("user.action", "user_id", userID)  // DON'T DO THIS!
}

// The cardinality limiter will automatically:
// 1. Detect high cardinality
// 2. Start dropping new label combinations
// 3. Log warnings so you know what's happening

// GOOD: Use bounded labels instead
telemetry.Counter("user.action",
    "user_type", "premium",  // Only a few types
    "action", "login")       // Only a few actions
```

### 3. Graceful Degradation

The module is designed to never crash your application:

```go
func main() {
    // Even if telemetry fails to initialize, your app keeps running
    if err := telemetry.Initialize(config); err != nil {
        log.Printf("Telemetry failed to initialize: %v", err)
        // Your app continues without telemetry
    }

    // Even if the metrics backend dies, your app keeps running
    telemetry.Counter("my.metric")  // Never panics, never blocks

    // Even if shutdown fails, your app exits cleanly
    defer func() {
        if err := telemetry.Shutdown(context.Background()); err != nil {
            log.Printf("Telemetry shutdown failed: %v", err)
            // Your app still exits normally
        }
    }()
}
```

## 13. Testing Your Telemetry

Here's how to test that your components emit metrics correctly:

```go
package main

import (
    "context"
    "testing"

    "github.com/truvaagents/truva-g3/telemetry"
)

// MyComponent is an example component for testing
type MyComponent struct {
    name string
}

func NewMyComponent() *MyComponent {
    return &MyComponent{name: "test-component"}
}

func (c *MyComponent) DoSomething(ctx context.Context) error {
    // Emit some metrics
    telemetry.Counter("component.operation", "name", c.name)
    return nil
}

func TestMyComponentTelemetry(t *testing.T) {
    // In tests, use development profile for predictable behavior
    config := telemetry.UseProfile(telemetry.ProfileDevelopment)
    config.ServiceName = "test"

    // Initialize telemetry for the test
    if err := telemetry.Initialize(config); err != nil {
        t.Fatalf("Failed to initialize telemetry: %v", err)
    }
    defer telemetry.Shutdown(context.Background())

    // Get health before operation
    healthBefore := telemetry.GetHealth()

    // Run your component
    component := NewMyComponent()
    err := component.DoSomething(context.Background())

    if err != nil {
        t.Fatalf("Component failed: %v", err)
    }

    // Get health after operation
    healthAfter := telemetry.GetHealth()

    // Verify metrics were emitted
    if healthAfter.MetricsEmitted <= healthBefore.MetricsEmitted {
        t.Error("Expected metrics to be emitted")
    }

    // For more detailed testing, you can:
    // 1. Use a test exporter to capture exact metrics
    // 2. Mock the telemetry system
    // 3. Use the metrics registry to query specific metrics
}

// Test with different profiles
func TestTelemetryProfiles(t *testing.T) {
    profiles := []telemetry.Profile{
        telemetry.ProfileDevelopment,
        telemetry.ProfileStaging,
        telemetry.ProfileProduction,
    }

    for _, profile := range profiles {
        t.Run(string(profile), func(t *testing.T) {
            config := telemetry.UseProfile(profile)

            // Verify profile-specific settings
            switch profile {
            case telemetry.ProfileDevelopment:
                if !config.Verbose {
                    t.Error("Development should have verbose logging enabled")
                }
            case telemetry.ProfileProduction:
                if config.Verbose {
                    t.Error("Production should not have verbose logging")
                }
            }
        })
    }
}
```

## 14. Debugging Telemetry Issues

When telemetry isn't working as expected, here's how to debug:

```go
import (
    "fmt"
    "github.com/truvaagents/truva-g3/telemetry"
)

func debugTelemetry() {
    // 1. Check if telemetry is initialized
    health := telemetry.GetHealth()
    fmt.Printf("Telemetry Health Check:\n")
    fmt.Printf("  Initialized: %v\n", health.Initialized)
    fmt.Printf("  Metrics Emitted: %d\n", health.MetricsEmitted)
    fmt.Printf("  Circuit State: %s\n", health.CircuitState)
    fmt.Printf("  Last Error: %s\n", health.LastError)

    // 2. Enable debug logging
    config := telemetry.Config{
        ServiceName: "debug-test",
        Enabled:     true,
        // In development, you might want to see everything
    }

    // 3. Test with a simple metric
    telemetry.Counter("debug.test")

    // 4. Check health again
    healthAfter := telemetry.GetHealth()
    if healthAfter.MetricsEmitted == health.MetricsEmitted {
        fmt.Println("WARNING: Metric was not emitted!")
        fmt.Printf("Possible reasons:\n")
        fmt.Printf("- Telemetry not initialized\n")
        fmt.Printf("- Circuit breaker is open\n")
        fmt.Printf("- Sampling rate is 0\n")
    }
}
```

## 15. Advanced Patterns

### Pattern 1: Request Tracing
```go
import (
    "net/http"
    "github.com/truvaagents/truva-g3/telemetry"
)

func handleRequest(w http.ResponseWriter, r *http.Request) {
    // Create request context with correlation ID
    ctx := telemetry.WithBaggage(r.Context(),
        "request_id", r.Header.Get("X-Request-ID"),
        "user_id", getUserID(r),
        "endpoint", r.URL.Path)

    // All metrics in this request will include this context
    defer telemetry.TimeOperation("request.duration",
        "endpoint", r.URL.Path,
        "method", r.Method)()

    // Your handler logic...
    processHTTPRequest(w, r)
}

// Helper function (example)
func getUserID(r *http.Request) string {
    // Your user ID extraction logic here
    return "user-123"
}

func processHTTPRequest(w http.ResponseWriter, r *http.Request) {
    // Your request processing logic here
    w.WriteHeader(http.StatusOK)
}
```

### Pattern 2: Batch Operations
```go
// Item represents a work item (example type)
type Item struct {
    ID   string
    Data interface{}
}

func processBatch(items []Item) {
    // Track batch metrics
    telemetry.Histogram("batch.size", float64(len(items)))

    start := time.Now()
    successful := 0
    failed := 0

    for _, item := range items {
        if err := processItem(item); err != nil {
            failed++
            telemetry.Counter("item.processing.failed",
                "error", err.Error())
        } else {
            successful++
            telemetry.Counter("item.processing.success")
        }
    }

    // Summary metrics
    telemetry.Histogram("batch.duration_ms",
        float64(time.Since(start).Milliseconds()))
    telemetry.Gauge("batch.success_rate",
        float64(successful)/float64(len(items))*100)
}

// Helper function (example)
func processItem(item Item) error {
    // Your item processing logic here
    return nil
}
```

### Pattern 3: Resource Monitoring
```go
import (
    "runtime"
    "time"
    "github.com/truvaagents/truva-g3/telemetry"
)

func monitorResources() {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        var m runtime.MemStats
        runtime.ReadMemStats(&m)

        // Memory metrics
        telemetry.Gauge("memory.alloc_mb", float64(m.Alloc/1024/1024))
        telemetry.Gauge("memory.total_alloc_mb", float64(m.TotalAlloc/1024/1024))
        telemetry.Gauge("memory.sys_mb", float64(m.Sys/1024/1024))
        telemetry.Gauge("memory.num_gc", float64(m.NumGC))

        // Goroutine metrics
        telemetry.Gauge("goroutines.count", float64(runtime.NumGoroutine()))
    }
}
```

## 16. Best Practices Summary

### DO ✅
- **Initialize early**: Set up telemetry at the start of main()
- **Use profiles**: Leverage pre-configured profiles for different environments
- **Add context**: Use labels to make metrics meaningful
- **Handle failures gracefully**: Don't let telemetry crash your app
- **Test your metrics**: Verify components emit expected metrics
- **Monitor cardinality**: Use bounded label values

### DON'T ❌
- **Don't use high-cardinality labels**: No user IDs, timestamps, or UUIDs
- **Don't block on telemetry**: Always use timeouts for shutdown
- **Don't emit sensitive data**: No passwords, tokens, or PII in metrics
- **Don't over-instrument**: Start simple, add more as needed
- **Don't ignore errors**: Log telemetry failures for debugging

## 17. Quick Reference

### Initialization
```go
// Simplest
telemetry.Initialize(telemetry.Config{ServiceName: "my-app"})

// With environment detection
config := telemetry.UseProfile(profile)
telemetry.Initialize(config)

// Always shutdown
defer telemetry.Shutdown(context.Background())
```

### Basic Metrics
```go
telemetry.Counter("metric.name", "label", "value")
telemetry.Histogram("metric.name", 123.45, "label", "value")
telemetry.Gauge("metric.name", 67.89, "label", "value")
telemetry.Duration("metric.name", startTime, "label", "value")
```

### With Context
```go
ctx = telemetry.WithBaggage(ctx, "key", "value")
telemetry.EmitWithContext(ctx, "metric.name", 123.45)
```

### Health Check
```go
health := telemetry.GetHealth()
if !health.Initialized {
    // Telemetry not working
}
```

## 18. Unified Metrics API

The Unified Metrics API provides pre-defined helper functions for recording cross-module metrics with consistent naming conventions. This enables unified observability across all Truva-G3 modules (agent, orchestration, core) with standardized Prometheus metrics.

### Why Unified Metrics?

When building distributed systems with Truva-G3, different modules emit their own metrics. The unified metrics API ensures:
- **Consistent naming**: All modules use the same metric names and label conventions
- **Cross-module dashboards**: Create Grafana dashboards that aggregate data from all modules
- **Simplified instrumentation**: Pre-defined functions for common patterns (requests, errors, tool calls, AI operations)
- **Pre-registered metrics**: All metrics are registered at init() time, avoiding runtime registration issues

### Module Constants

Use these constants to identify the source module in metrics:

```go
import "github.com/truvaagents/truva-g3/telemetry"

const (
    telemetry.ModuleAgent         = "agent"
    telemetry.ModuleOrchestration = "orchestration"
    telemetry.ModuleCore          = "core"
)
```

### Available Functions

#### Request Metrics

```go
// RecordRequest records a request with duration and status
// Emits: truvag3_request_duration_ms (histogram), truvag3_requests_total (counter)
telemetry.RecordRequest(module, operation string, durationMs float64, status string)

// RecordRequestError records a request error
// Emits: truvag3_request_errors_total (counter)
telemetry.RecordRequestError(module, operation, errorType string)
```

#### Tool/Capability Call Metrics

```go
// RecordToolCall records a tool invocation with duration
// Emits: truvag3_tool_call_duration_ms (histogram), truvag3_tool_calls_total (counter)
telemetry.RecordToolCall(module, toolName string, durationMs float64, status string)

// RecordToolCallError records a tool call error
// Emits: truvag3_tool_call_errors_total (counter)
telemetry.RecordToolCallError(module, toolName, errorType string)

// RecordToolCallRetry records a tool call retry attempt
// Emits: truvag3_tool_call_retries_total (counter)
telemetry.RecordToolCallRetry(module, toolName string)
```

#### AI Provider Metrics

```go
// RecordAIRequest records an AI provider request
// Emits: truvag3_ai_request_duration_ms (histogram), truvag3_ai_requests_total (counter)
telemetry.RecordAIRequest(module, provider string, durationMs float64, status string)

// RecordAITokens records token usage from AI providers
// Emits: truvag3_ai_tokens_total (counter)
// tokenType: "prompt", "completion", or "total"
telemetry.RecordAITokens(module, provider, tokenType string, count int64)
```

### Usage Examples

#### Agent Module Example

```go
package main

import (
    "time"
    "github.com/truvaagents/truva-g3/telemetry"
)

func handleResearchRequest(topic string) error {
    start := time.Now()

    // Record the request
    defer func() {
        duration := float64(time.Since(start).Milliseconds())
        telemetry.RecordRequest(telemetry.ModuleAgent, "research", duration, "success")
    }()

    // Call a tool
    toolStart := time.Now()
    result, err := callWeatherTool(topic)
    toolDuration := float64(time.Since(toolStart).Milliseconds())

    if err != nil {
        telemetry.RecordToolCallError(telemetry.ModuleAgent, "weather-tool", "api_error")
        telemetry.RecordRequestError(telemetry.ModuleAgent, "research", "tool_failure")
        return err
    }

    telemetry.RecordToolCall(telemetry.ModuleAgent, "weather-tool", toolDuration, "success")

    // Call AI for analysis
    aiStart := time.Now()
    analysis, tokens, err := callAI(result)
    aiDuration := float64(time.Since(aiStart).Milliseconds())

    if err != nil {
        telemetry.RecordAIRequest(telemetry.ModuleAgent, "openai", aiDuration, "error")
        return err
    }

    telemetry.RecordAIRequest(telemetry.ModuleAgent, "openai", aiDuration, "success")
    telemetry.RecordAITokens(telemetry.ModuleAgent, "openai", "prompt", int64(tokens.Prompt))
    telemetry.RecordAITokens(telemetry.ModuleAgent, "openai", "completion", int64(tokens.Completion))

    return nil
}
```

#### Orchestration Module Example

```go
package main

import (
    "time"
    "github.com/truvaagents/truva-g3/telemetry"
)

func executeWorkflow(workflowID string) error {
    start := time.Now()

    defer func() {
        duration := float64(time.Since(start).Milliseconds())
        telemetry.RecordRequest(telemetry.ModuleOrchestration, "workflow_execution", duration, "success")
    }()

    // Execute each step
    for _, step := range workflow.Steps {
        stepStart := time.Now()

        result, err := executeStep(step)
        stepDuration := float64(time.Since(stepStart).Milliseconds())

        if err != nil {
            telemetry.RecordToolCallError(telemetry.ModuleOrchestration, step.CapabilityName, "execution_error")

            // Try retry if configured
            if step.RetryEnabled {
                telemetry.RecordToolCallRetry(telemetry.ModuleOrchestration, step.CapabilityName)
                // ... retry logic
            }
            continue
        }

        telemetry.RecordToolCall(telemetry.ModuleOrchestration, step.CapabilityName, stepDuration, "success")
    }

    return nil
}
```

### Prometheus Query Examples

The unified metrics enable powerful cross-module queries in Prometheus:

```promql
# Total requests across all modules
sum(rate(truvag3_requests_total[5m])) by (module)

# Error rate by module
sum(rate(truvag3_request_errors_total[5m])) by (module)
  / sum(rate(truvag3_requests_total[5m])) by (module)

# Average tool call latency by tool
histogram_quantile(0.95,
  sum(rate(truvag3_tool_call_duration_ms_bucket[5m])) by (le, tool_name)
)

# AI token usage across all providers
sum(increase(truvag3_ai_tokens_total[1h])) by (provider, token_type)

# Cross-module request flow visualization
sum(rate(truvag3_requests_total[5m])) by (module, operation)
```

### Grafana Dashboard

With unified metrics, you can create a single dashboard showing:
- Request rates and latencies across agent, orchestration, and core modules
- Tool call success/failure rates
- AI provider usage and token consumption
- Error rates with breakdown by error type

See the [examples/k8-deployment/grafana.yaml](../examples/k8-deployment/grafana.yaml) for a pre-configured dashboard.

## 19. Distributed Tracing

Distributed tracing allows you to follow a request as it flows through multiple services. The telemetry module provides HTTP instrumentation that automatically propagates trace context using W3C TraceContext headers.

### The Journey of a Request

Imagine a user request that touches three services:

```
User → API Gateway → Weather Service → Database
```

Without distributed tracing, when something goes wrong, you have three separate log files with no way to connect them. With distributed tracing, you can see the entire journey as a single trace:

```
Trace ID: abc123
├── API Gateway (100ms)
│   ├── Weather Service (80ms)
│   │   └── Database Query (20ms)
│   └── Response formatting (5ms)
└── Total: 105ms
```

### Server-Side: Tracing Middleware

Wrap your HTTP handlers with `TracingMiddleware` to automatically:
- Extract trace context from incoming requests
- Create spans for each request
- Record HTTP metrics (status codes, latency)
- Propagate context to your handler code

```go
package main

import (
    "net/http"
    "github.com/truvaagents/truva-g3/telemetry"
)

func main() {
    // Initialize telemetry FIRST
    telemetry.Initialize(telemetry.Config{
        ServiceName: "weather-service",
        Endpoint:    "http://otel-collector:4318",
    })
    defer telemetry.Shutdown(context.Background())

    // Create your handlers
    mux := http.NewServeMux()
    mux.HandleFunc("/api/weather", handleWeather)
    mux.HandleFunc("/health", handleHealth)

    // Wrap with tracing middleware
    tracedHandler := telemetry.TracingMiddleware("weather-service")(mux)

    http.ListenAndServe(":8080", tracedHandler)
}
```

### Excluding Paths from Tracing

Health checks and metrics endpoints shouldn't create traces (they're noisy!):

```go
config := &telemetry.TracingMiddlewareConfig{
    ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live"},
}

tracedHandler := telemetry.TracingMiddlewareWithConfig("my-service", config)(mux)
```

### Custom Request Filtering

For more complex filtering logic (e.g., based on query parameters, headers, or request body),
use `RequestFilter`. This is evaluated after `ExcludedPaths` and gives you full access to the request:

```go
config := &telemetry.TracingMiddlewareConfig{
    ExcludedPaths: []string{"/health", "/metrics"},
    // Exclude polling requests from tracing to reduce noise
    // Return false to exclude, true to include
    RequestFilter: func(r *http.Request) bool {
        // Skip tracing for status polling requests
        return r.URL.Query().Get("poll") != "true"
    },
}

tracedHandler := telemetry.TracingMiddlewareWithConfig("my-service", config)(mux)
```

**Use cases for RequestFilter:**
- Exclude polling/heartbeat requests with specific query parameters
- Filter based on request headers (e.g., internal vs external traffic)
- Skip tracing for specific user agents (e.g., health check bots)
- Implement sampling logic based on request attributes

**Design principle:** The telemetry module provides the generic filtering mechanism.
Applications decide what to filter based on their specific needs. This keeps modules
decoupled - telemetry doesn't need to know about HITL, polling, or any specific use case.

### Custom Span Names

By default, spans are named `HTTP GET /api/weather`. Customize this:

```go
config := &telemetry.TracingMiddlewareConfig{
    SpanNameFormatter: func(operation string, r *http.Request) string {
        // Create semantic span names
        return r.Method + " " + getRoutePattern(r)  // "GET /api/users/:id"
    },
}
```

### Client-Side: Traced HTTP Client

When calling other services, use `NewTracedHTTPClient` to automatically propagate trace context:

```go
// Create once, reuse for all requests
client := telemetry.NewTracedHTTPClient(nil)

func callWeatherService(ctx context.Context, city string) (*Weather, error) {
    // Context carries trace information
    req, _ := http.NewRequestWithContext(ctx, "GET",
        "http://weather-service/api/weather?city="+city, nil)

    // Trace headers (traceparent, tracestate) are automatically injected
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    // Parse response...
}
```

### With Custom Transport Settings

For production, configure connection pooling:

```go
transport := &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 10,
    IdleConnTimeout:     90 * time.Second,
}

client := telemetry.NewTracedHTTPClientWithTransport(transport)
```

### Complete Example: Multi-Service Tracing

Here's how tracing flows across services:

```go
// === API Gateway (service 1) ===
func handleUserRequest(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()  // Contains incoming trace context

    // Call downstream services - context propagates automatically
    weather, _ := weatherClient.GetWeather(ctx, "NYC")
    news, _ := newsClient.GetNews(ctx, "NYC")

    // Combine and respond
    json.NewEncoder(w).Encode(map[string]interface{}{
        "weather": weather,
        "news":    news,
    })
}

// === Weather Service (service 2) ===
func handleWeather(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()  // Trace context from gateway!

    // This span is a child of the gateway's span
    telemetry.Counter("weather.requests", "city", r.URL.Query().Get("city"))

    // Fetch data and respond...
}

// === In Jaeger/Grafana Tempo ===
// You'll see a single trace spanning both services:
//
// api-gateway: handleUserRequest (150ms)
// ├── weather-service: handleWeather (50ms)
// └── news-service: handleNews (80ms)
```

### Environment Configuration

Configure tracing via environment variables:

```bash
# Required for trace collection
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318

# Service identification in traces
OTEL_SERVICE_NAME=weather-service

# Sampling (production should use lower rates)
OTEL_TRACES_SAMPLER=traceidratio
OTEL_TRACES_SAMPLER_ARG=0.1  # Sample 10% of traces
```

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: weather-service
spec:
  template:
    spec:
      containers:
      - name: app
        image: weather-service:latest
        env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector:4318"
        - name: OTEL_SERVICE_NAME
          value: "weather-service"
```

### Tracing Best Practices

**DO:**
- Initialize telemetry before creating traced middleware/clients
- Exclude health/metrics endpoints from tracing
- Use semantic span names that match your routing patterns
- Reuse `TracedHTTPClient` instances (connection pooling)
- Always pass `context.Context` through your call chain

**DON'T:**
- Create new traced clients for each request
- Trace every single internal operation (too noisy)
- Forget to call `telemetry.Shutdown()` (traces may be lost)
- Use tracing without an OTEL Collector (nowhere for traces to go!)

### 📖 Comprehensive Distributed Tracing Guide

For a complete deep-dive into distributed tracing with Truva-G3, including:
- **Trace-Log Correlation** - Connecting traces to logs for easier debugging
- **Complete Multi-Service Examples** - Based on actual working examples in `examples/agent-with-telemetry/`
- **Infrastructure Setup** - OTEL Collector, Jaeger, and Grafana configuration
- **Troubleshooting Guide** - Common problems and solutions
- **Best Practices** - Production-ready patterns

See the **[Distributed Tracing and Log Correlation Guide](../docs/DISTRIBUTED_TRACING_GUIDE.md)**.

## 20. AI Module Distributed Tracing

The Truva-G3 AI module supports distributed tracing, allowing you to see AI operations (`ai.generate_response`, `ai.http_attempt`) as part of your request traces in Jaeger.

### Critical: Initialization Order

**The telemetry module MUST be initialized BEFORE creating your AI client.** Otherwise, `telemetry.GetTelemetryProvider()` returns `nil` and AI spans won't be captured.

```go
func main() {
    // ✅ CORRECT ORDER

    // 1. Set component type (for service_type label)
    core.SetCurrentComponentType(core.ComponentTypeAgent)

    // 2. Initialize telemetry BEFORE creating AI client
    initTelemetry("my-agent")
    defer telemetry.Shutdown(context.Background())

    // 3. Create AI client AFTER telemetry - now it gets the provider
    aiClient, err := ai.NewClient(
        ai.WithTelemetry(telemetry.GetTelemetryProvider()),
    )

    // 4. Create your agent with the AI client
    agent := NewMyAgent(aiClient)
}
```

```go
// ❌ WRONG ORDER - AI telemetry won't work!
func main() {
    agent := NewMyAgent()  // Creates AI client internally
    initTelemetry("my-agent")  // Too late! AI client already created without telemetry
}
```

### Framework-Driven Logger Propagation

**Important:** The Truva-G3 Framework automatically propagates the logger to the AI client when you register components. This happens in `core.NewFramework()` during component registration via the `applyConfigToComponent()` function.

**How It Works:**

1. When you create an agent with `core.NewBaseAgent()`, the AI client initially has a `NoOpLogger` (silent logger)
2. When you call `core.NewFramework()` and register your agent, the Framework:
   - Checks if the AI client implements `SetLogger(Logger)`
   - Automatically calls `SetLogger()` with the production logger
   - The AI client wraps the logger with component prefix `"framework/ai"`

**Result:** AI module logs appear with `"component": "framework/ai"` and include trace IDs for correlation.

```go
// ai/providers/base.go - SetLogger method
func (b *BaseClient) SetLogger(logger core.Logger) {
    if logger == nil {
        b.Logger = &core.NoOpLogger{}
    } else if cal, ok := logger.(core.ComponentAwareLogger); ok {
        b.Logger = cal.WithComponent("framework/ai")  // Creates "framework/ai" prefix
    } else {
        b.Logger = logger
    }
}
```

**Developer Benefit:** You don't need to manually pass loggers to the AI client. The Framework handles this automatically during component registration. Just ensure telemetry is initialized before agent creation (as shown above).

**Example AI Module Log Output:**
```json
{
  "component": "framework/ai",
  "level": "DEBUG",
  "message": "AI HTTP request completed",
  "operation": "ai_http_success",
  "trace.span_id": "e75ad960517fa8fe",
  "trace.trace_id": "5b54aa1e7925acb809e77479b5797f5d"
}
```

### AI Spans Captured

When properly configured, the AI module emits these spans:

| Span Name | Description | Key Attributes |
|-----------|-------------|----------------|
| `ai.generate_response` | Overall AI request | `ai.provider`, `ai.model`, `ai.prompt_tokens`, `ai.completion_tokens`, `ai.total_tokens` |
| `ai.http_attempt` | Each HTTP attempt (including retries) | `ai.attempt`, `ai.max_retries`, `ai.is_retry`, `http.status_code`, `ai.attempt_duration_ms` |

### Viewing AI Traces in Jaeger

1. Open Jaeger UI: `http://localhost:16686`
2. Select your agent service (e.g., `travel-research-orchestration`)
3. Find a trace and expand it
4. Look for `ai.generate_response` and `ai.http_attempt` spans nested under your request spans
5. Click on a span to see detailed attributes (token counts, model, provider, etc.)

### Complete Example

See `examples/agent-with-orchestration/` for a working example with AI telemetry.

## 21. Push-Based Telemetry Limitations

### The Silent Failure Problem

Truva-G3's telemetry uses **push-based OpenTelemetry** where agents send metrics to an OTEL Collector. While this architecture is standard and scalable, it has a critical blind spot: **when an agent is broken, it cannot report that it's broken**.

#### The Data Flow

```
┌─────────────────────────────────────────────────────────────────┐
│  WHEN AGENT IS WORKING:                                         │
│                                                                 │
│  Agent → OTEL Collector → Prometheus                            │
│  (pushes metrics)   (exposes)    (scrapes)                      │
│                                                                 │
│  ✅ Metrics flow: registration, health checks, heartbeats       │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  WHEN AGENT IS BROKEN (not registering, crashed, etc.):        │
│                                                                 │
│  Agent → ❌ (no push) → OTEL Collector → Prometheus             │
│  (broken/misconfigured)                                         │
│                                                                 │
│  ❌ No metrics emitted = No data in Prometheus                  │
│  ❌ No "stale" or "removed" events recorded                     │
│  ❌ The failure is SILENT - no alerting possible                │
└─────────────────────────────────────────────────────────────────┘
```

#### Real-World Scenario

During troubleshooting of `research-agent-telemetry`, we discovered:

| Observation | Impact |
|-------------|--------|
| Agent was not registering in Redis | Not discoverable by other agents |
| `truvag3_discovery_deregistrations_total` = 0 | No record of the failure |
| Historical registration data = `null` | No time series before restart |
| Prometheus had no gap data | Cannot alert on "missing" services |

**The root cause (stale pod configuration) was invisible to the monitoring system because the broken agent couldn't emit "I'm broken" metrics.**

### What Prometheus Cannot Tell You

With the current push-based architecture, Prometheus **cannot** answer these questions:

1. **"Which agents are NOT registered?"** - Only registered agents emit metrics
2. **"When did an agent's Redis entry become stale?"** - Stale entries don't emit anything
3. **"How long was the registration gap?"** - No data = no timeline
4. **"Why did the agent fail to register?"** - Errors aren't captured externally

### Recommendations

#### 1. External Redis Monitor (Recommended)

Create a dedicated monitoring service that periodically checks Redis for service health:

```go
// redis_monitor.go - External service that runs independently
package main

import (
    "context"
    "time"
    "github.com/truvaagents/truva-g3/telemetry"
)

func monitorRedisRegistry(redisClient *redis.Client) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        ctx := context.Background()

        // Get all registered services
        keys, _ := redisClient.Keys(ctx, "truvag3:services:*").Result()

        // Emit gauge for total registered services
        telemetry.Gauge("redis.services.registered_total", float64(len(keys)))

        // Check each service's TTL and last_seen
        for _, key := range keys {
            serviceName := strings.TrimPrefix(key, "truvag3:services:")

            // Get TTL
            ttl, _ := redisClient.TTL(ctx, key).Result()
            telemetry.Gauge("redis.service.ttl_seconds",
                ttl.Seconds(),
                "service", serviceName)

            // Get service data and check last_seen
            data, _ := redisClient.Get(ctx, key).Result()
            var info ServiceInfo
            json.Unmarshal([]byte(data), &info)

            staleness := time.Since(info.LastSeen).Seconds()
            telemetry.Gauge("redis.service.staleness_seconds",
                staleness,
                "service", serviceName,
                "type", string(info.Type))

            // Alert on stale entries (> 60 seconds without update)
            if staleness > 60 {
                telemetry.Counter("redis.service.stale_detected",
                    "service", serviceName)
            }
        }

        // Check for expected services that are MISSING
        expectedServices := []string{
            "research-assistant",
            "research-agent-telemetry",
            "weather-service",
            // ... add your expected services
        }

        for _, expected := range expectedServices {
            key := "truvag3:services:" + expected
            exists, _ := redisClient.Exists(ctx, key).Result()

            if exists == 0 {
                telemetry.Counter("redis.service.missing",
                    "service", expected)
                telemetry.Gauge("redis.service.registered",
                    0, // Not registered
                    "service", expected)
            } else {
                telemetry.Gauge("redis.service.registered",
                    1, // Registered
                    "service", expected)
            }
        }
    }
}
```

**Prometheus Alerts with External Monitor:**

```yaml
# prometheus-alerts.yaml
groups:
  - name: truvag3-service-health
    rules:
      # Alert when expected service is not registered
      - alert: ServiceNotRegistered
        expr: redis_service_registered == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Service {{ $labels.service }} is not registered in Redis"

      # Alert when service entry is stale
      - alert: ServiceStale
        expr: redis_service_staleness_seconds > 60
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "Service {{ $labels.service }} hasn't updated in {{ $value }}s"

      # Alert when total registered services drops
      - alert: ServiceCountDrop
        expr: delta(redis_services_registered_total[5m]) < -1
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "Number of registered services decreased"
```

#### 2. Kubernetes-Based Health Monitoring

Leverage Kubernetes probes with Prometheus metrics:

```yaml
# k8-deployment.yaml
spec:
  containers:
  - name: agent
    livenessProbe:
      httpGet:
        path: /health
        port: 8092
      initialDelaySeconds: 10
      periodSeconds: 30
    readinessProbe:
      httpGet:
        path: /health
        port: 8092
      initialDelaySeconds: 5
      periodSeconds: 10
```

Then use `kube-state-metrics` to monitor pod health:

```promql
# Pods not ready
kube_pod_status_ready{namespace="truvag3-examples", condition="false"} == 1

# Container restarts (indicates crashes)
increase(kube_pod_container_status_restarts_total{namespace="truvag3-examples"}[1h]) > 3
```

#### 3. Hybrid Pull+Push Architecture (Advanced)

Add a `/metrics` endpoint alongside push-based OTEL for redundancy:

```go
// Add to your agent's main.go
import "github.com/prometheus/client_golang/prometheus/promhttp"

func main() {
    // Existing OTEL push-based telemetry
    telemetry.Initialize(config)

    // ALSO expose pull-based Prometheus endpoint
    http.Handle("/metrics", promhttp.Handler())

    // Now Prometheus can scrape directly as a backup
}
```

**Prometheus config to scrape both:**

```yaml
# prometheus.yaml
scrape_configs:
  # Primary: OTEL Collector (push via OTEL, pull from collector)
  - job_name: 'otel-collector'
    static_configs:
      - targets: ['otel-collector:8889']

  # Backup: Direct agent scraping (pull-based)
  - job_name: 'truvag3-agents-direct'
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_truvag3_framework_type]
        action: keep
        regex: agent
```

### Summary: Observability Gap Mitigation

| Approach | Complexity | Coverage | Recommendation |
|----------|------------|----------|----------------|
| External Redis Monitor | Medium | Complete | **Recommended for production** |
| Kubernetes Probes | Low | Partial | Good baseline |
| Hybrid Pull+Push | High | Complete | For critical systems |

**Key Takeaway:** Push-based telemetry is excellent for operational metrics, but you need external monitoring to detect **absence of data** - the most critical failure mode in distributed systems.

---

## 22. Summary

The telemetry module is your application's dashboard, giving you visibility into what's happening in production. It's designed to be:

- **Simple to start with** - One line to initialize, one line to emit metrics
- **Safe in production** - Circuit breakers, cardinality limits, graceful degradation
- **Flexible when needed** - Profiles, contexts, advanced options

Remember: Good telemetry is like good insurance - you hope you never need it, but when you do, you're incredibly glad it's there.

Start simple, add complexity as needed, and always prioritize your application's stability over perfect metrics. Happy monitoring! 📊