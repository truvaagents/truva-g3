# Distributed Tracing and Log Correlation Guide

Welcome to the complete guide on distributed tracing in TruvaG3! Think of this as your friendly mentor sitting next to you, explaining how to follow a request as it travels through your entire system. Grab a coffee, and let's dive in!

## Table of Contents

1. [What Is Distributed Tracing and Why Should You Care?](#1-what-is-distributed-tracing-and-why-should-you-care)
2. [The Problem Without Tracing](#2-the-problem-without-tracing)
3. [The Solution: Context Propagation](#3-the-solution-context-propagation)
4. [Understanding Trace IDs, Span IDs, and Parent Spans](#4-understanding-trace-ids-span-ids-and-parent-spans)
5. [Trace-Log Correlation: The Magic Glue](#5-trace-log-correlation-the-magic-glue)
6. [Implementation: Server-Side (TracingMiddleware)](#6-implementation-server-side-tracingmiddleware)
7. [Implementation: Client-Side (TracedHTTPClient)](#7-implementation-client-side-tracedhttpclient)
8. [Complete Example: Multi-Service Tracing](#8-complete-example-multi-service-tracing)
9. [Infrastructure Setup (Kubernetes)](#9-infrastructure-setup-kubernetes)
10. [Viewing Your Traces](#10-viewing-your-traces)
    - [Using request_id for Troubleshooting](#using-request_id-for-troubleshooting)
11. [Required Patterns for Framework-Level Tracing](#11-required-patterns-for-framework-level-tracing)
12. [Best Practices](#12-best-practices)
13. [Troubleshooting](#13-troubleshooting)
14. [Quick Reference](#14-quick-reference)
15. [LLM Telemetry in Orchestration (Automatic)](#15-llm-telemetry-in-orchestration-automatic)
16. [HITL Cross-Trace Correlation](#16-hitl-cross-trace-correlation)
17. [AI Module Distributed Tracing](#17-ai-module-distributed-tracing)

---

## 1. What Is Distributed Tracing and Why Should You Care?

Let me explain this with a story everyone can relate to.

### The Package Delivery Analogy

Imagine you order a gift online that needs to be assembled from parts made by different factories:

1. **Factory A** makes the electronics
2. **Factory B** makes the casing
3. **Factory C** does the assembly
4. **Shipping Center** packs and ships it

Now imagine your package never arrives. You call customer service, and they say:
- "Factory A says they sent their part on time"
- "Factory B has no record of your order"
- "Factory C says they never got anything"
- "Shipping says they have 10,000 packages and can't find yours"

**Nightmare, right?**

Now imagine if every package had a **tracking number** that followed it through every step:
- Factory A: "Package #12345 - electronics completed at 10:00 AM"
- Factory B: "Package #12345 - casing completed at 11:00 AM"
- Factory C: "Package #12345 - waiting for casing (still at Factory B!)"

**That tracking number is exactly what a Trace ID does for your requests!**

### Why This Matters for Your Applications

In a microservices architecture (like TruvaG3's tools and agents), a single user request might touch:
- 1 Agent (orchestrator)
- 5 Tools (weather, currency, geocoding, etc.)
- 2 Databases
- 1 External API

Without distributed tracing, when something goes wrong, you have:
- **6+ separate log files** with no way to connect them
- **No visibility** into which service caused the delay
- **Debugging nightmares** at 3 AM during an outage

With distributed tracing, you get:
- **One trace ID** connecting all logs across all services
- **Visual timeline** showing exactly where time was spent
- **Instant root cause analysis** - "Oh, the currency service took 5 seconds!"

---

## 2. The Problem Without Tracing

Let me show you what debugging looks like without distributed tracing.

### Scenario: User Request Is Slow

A user complains: "Getting weather and stock data takes forever!"

**Without tracing, your logs look like this:**

```
# research-agent logs
2024-01-01 10:00:00 INFO  Processing research request
2024-01-01 10:00:05 INFO  Research completed

# weather-tool logs
2024-01-01 10:00:01 INFO  Weather request received
2024-01-01 10:00:01 INFO  Weather response sent

# stock-tool logs
2024-01-01 10:00:02 INFO  Stock request received
2024-01-01 10:00:04 INFO  Stock response sent
```

**Questions you can't answer:**
- Which request in the agent logs corresponds to which tool calls?
- Was the 5-second delay in the agent or the tools?
- If there were 100 concurrent requests, which logs go together?

### The Correlation Challenge

The fundamental problem is: **logs from different services have no common identifier**.

```
Service A: "Started processing"      ← Which request?
Service B: "Database query slow"     ← Same request? Different request?
Service C: "Returned response"       ← No idea!
```

---

## 3. The Solution: Context Propagation

The solution is elegantly simple: **pass a unique identifier with every request**.

### How It Works

```
┌─────────────────────────────────────────────────────────────────────┐
│                         USER REQUEST                                 │
│                    (no trace ID yet)                                │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────┐
│                      RESEARCH AGENT                                  │
│                                                                      │
│  TracingMiddleware extracts OR generates:                           │
│  trace_id: abc123                                                   │
│  span_id: span-001                                                  │
│                                                                      │
│  Every log now includes: {"trace.trace_id": "abc123", ...}          │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
          ┌─────────────────┼─────────────────┐
          │                 │                 │
          ▼                 ▼                 ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│  WEATHER TOOL   │ │  STOCK TOOL     │ │  CURRENCY TOOL  │
│                 │ │                 │ │                 │
│ HTTP Headers:   │ │ HTTP Headers:   │ │ HTTP Headers:   │
│ traceparent:    │ │ traceparent:    │ │ traceparent:    │
│ 00-abc123-...   │ │ 00-abc123-...   │ │ 00-abc123-...   │
│                 │ │                 │ │                 │
│ Logs include:   │ │ Logs include:   │ │ Logs include:   │
│ trace_id:abc123 │ │ trace_id:abc123 │ │ trace_id:abc123 │
└─────────────────┘ └─────────────────┘ └─────────────────┘
```

### The W3C TraceContext Standard

TruvaG3 uses the **W3C TraceContext** standard, which is supported by all major tracing systems (Jaeger, Zipkin, Datadog, etc.).

The magic happens through HTTP headers:

```http
# Outgoing request from agent to tool
POST /api/capabilities/get_weather HTTP/1.1
Host: weather-tool:8080
traceparent: 00-abc123def456789-span001-01
tracestate: truvag3=research-agent
```

**The `traceparent` header contains:**
- `00` - Version (always 00)
- `abc123def456789` - **Trace ID** (same for ALL services in this request)
- `span001` - **Span ID** (unique to this specific operation)
- `01` - Flags (01 = sampled)

---

## 4. Understanding Trace IDs, Span IDs, and Parent Spans

Before we dive into implementation, let's understand the core concepts.

### The Family Tree Analogy

Think of a trace like a family tree:
- **Trace ID** = The family name (shared by everyone in the family)
- **Span ID** = Each person's unique ID
- **Parent Span ID** = Who is your parent

```
Trace ID: "Smith Family" (abc123)
│
├── Grandparent (span: A, parent: none)
│   ├── Parent (span: B, parent: A)
│   │   ├── Child 1 (span: C, parent: B)
│   │   └── Child 2 (span: D, parent: B)
│   └── Uncle (span: E, parent: A)
│       └── Cousin (span: F, parent: E)
```

### In Practice: A Research Request

```
Trace ID: fee30b72efcbefd21fddf9cd56d2c8c9
│
├── research-agent: HTTP POST /api/research (span: 1134)
│   ├── research-agent: call_weather_tool (span: 2245, parent: 1134)
│   │   └── weather-tool: HTTP POST /api/get_weather (span: 3356, parent: 2245)
│   │       └── weather-tool: fetch_api_data (span: 4467, parent: 3356)
│   │
│   ├── research-agent: call_stock_tool (span: 5578, parent: 1134)
│   │   └── stock-tool: HTTP POST /api/stock_quote (span: 6689, parent: 5578)
│   │
│   └── research-agent: aggregate_results (span: 7790, parent: 1134)
```

### What This Gives You

In Jaeger or Grafana, you see a beautiful timeline:

```
research-agent: HTTP POST /api/research ─────────────────────────────▶ 350ms
├─ call_weather_tool ─────────────────▶ 150ms
│  └─ weather-tool: HTTP POST ────────▶ 145ms
│     └─ fetch_api_data ─────▶ 100ms
│
├─ call_stock_tool ──────────────────▶ 180ms
│  └─ stock-tool: HTTP POST ──────────▶ 175ms
│
└─ aggregate_results ▶ 10ms
```

**Now you can instantly see:** The stock tool is the bottleneck (180ms vs 150ms for weather).

---

## 5. Trace-Log Correlation: The Magic Glue

Distributed tracing shows you the *timeline*. But logs show you the *details*. **Trace-log correlation connects them.**

### The Problem: Searching Through Logs

Even with Jaeger showing you a slow span, you need to find the actual logs:

```bash
# Without correlation - good luck finding the right log!
grep "error" /var/log/stock-tool.log | head -100
# Returns 100 lines... which one is YOUR request?
```

### The Solution: Trace IDs in Every Log

With trace-log correlation, every log entry includes the trace ID:

```json
{
  "timestamp": "2024-01-01T10:00:02Z",
  "level": "info",
  "message": "Processing stock quote request",
  "trace.trace_id": "fee30b72efcbefd21fddf9cd56d2c8c9",
  "trace.span_id": "6689abcd1234",
  "symbol": "AAPL"
}
```

**Now you can search:**

```bash
# Find ALL logs for this specific request across ALL services
grep "fee30b72efcbefd21fddf9cd56d2c8c9" /var/log/*.log
```

### How TruvaG3 Implements This

When using the `TracingMiddleware`, you can extract trace information from the context and include it in your logs:

```go
// In your handler, extract trace context from the request
func (t *MyTool) handleRequest(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Extract trace context for logging
    tc := telemetry.GetTraceContext(ctx)

    // Include in your logs
    log.Printf("Processing request trace_id=%s span_id=%s symbol=%s",
        tc.TraceID, tc.SpanID, req.Symbol)
}
```

For structured JSON logging, you can create a helper:

```go
// Helper to add trace context to log fields
func logWithTrace(ctx context.Context, msg string, fields map[string]interface{}) {
    tc := telemetry.GetTraceContext(ctx)
    fields["trace.trace_id"] = tc.TraceID
    fields["trace.span_id"] = tc.SpanID
    // Use your preferred JSON logger (zerolog, zap, etc.)
    jsonLog(msg, fields)
}
```

**The trace context is automatically propagated** via HTTP headers - you just need to include it in your logs for correlation!

---

## 6. Implementation: Server-Side (TracingMiddleware)

Now let's get practical. Here's how to add distributed tracing to your TruvaG3 tools and agents.

### What TracingMiddleware Does

The `TracingMiddleware` wraps your HTTP handlers and automatically:
1. **Extracts** trace context from incoming `traceparent` headers
2. **Creates** a new span for this request
3. **Records** HTTP metrics (status codes, latency)
4. **Propagates** context to your handler code via `r.Context()`

### Basic Usage (Recommended)

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

func main() {
    // 1. Initialize telemetry FIRST
    initTelemetry("weather-service")
    defer telemetry.Shutdown(context.Background())

    // 2. Create your tool
    tool := NewWeatherTool()

    // 3. Create framework WITH tracing middleware
    framework, err := core.NewFramework(tool,
        core.WithName("weather-service"),
        core.WithPort(8080),
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),

        // THIS IS THE KEY LINE - adds tracing middleware
        core.WithMiddleware(telemetry.TracingMiddleware("weather-service")),
    )
    if err != nil {
        log.Fatalf("Failed to create framework: %v", err)
    }

    // 4. Run
    ctx := context.Background()
    framework.Run(ctx)
}

func initTelemetry(serviceName string) {
    config := telemetry.UseProfile(telemetry.ProfileDevelopment)
    config.ServiceName = serviceName

    // Point to your OTEL Collector
    if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
        config.Endpoint = endpoint
    }

    if err := telemetry.Initialize(config); err != nil {
        log.Printf("Warning: Telemetry init failed: %v", err)
    }
}
```

### What Happens Under the Hood

When a request arrives:

```
Incoming Request
    │
    ▼
┌─────────────────────────────────────────────────────────────────┐
│ TracingMiddleware                                                │
│                                                                  │
│ 1. Check for traceparent header                                 │
│    - If present: Extract trace_id and parent_span_id            │
│    - If absent: Generate new trace_id                           │
│                                                                  │
│ 2. Create a new span for this request                           │
│    - Name: "HTTP POST /api/capabilities/get_weather"            │
│    - Parent: The extracted parent_span_id (if any)              │
│                                                                  │
│ 3. Add span to context                                          │
│    ctx = context.WithValue(ctx, spanKey, span)                  │
│                                                                  │
│ 4. Call your handler with enriched context                      │
│    next.ServeHTTP(w, r.WithContext(ctx))                        │
│                                                                  │
│ 5. When handler returns, end the span and record metrics        │
└─────────────────────────────────────────────────────────────────┘
    │
    ▼
Your Handler (receives ctx with trace info)
```

### Excluding Health Checks (Best Practice)

Health check endpoints are called frequently by Kubernetes. You don't want to trace them:

```go
// Use TracingMiddlewareWithConfig for more control
config := &telemetry.TracingMiddlewareConfig{
    ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live"},
}

framework, _ := core.NewFramework(tool,
    core.WithMiddleware(
        telemetry.TracingMiddlewareWithConfig("weather-service", config),
    ),
)
```

### Custom Span Names

By default, span names are `HTTP GET /api/capabilities/get_weather`. You can customize:

```go
config := &telemetry.TracingMiddlewareConfig{
    SpanNameFormatter: func(operation string, r *http.Request) string {
        // Create more semantic names
        return fmt.Sprintf("%s %s", r.Method, getRoutePattern(r))
    },
}
```

---

## 7. Implementation: Client-Side (TracedHTTPClient)

Server-side tracing is only half the story. When your **agent calls a tool**, you need to **propagate the trace context** in the outgoing request.

### What TracedHTTPClient Does

The `NewTracedHTTPClient()` creates an HTTP client that automatically:
1. **Injects** `traceparent` header into all outgoing requests
2. **Creates** client-side spans for each HTTP call
3. **Records** request/response metrics
4. **Propagates** the trace context to downstream services

### Basic Usage

This example is from `examples/agent-with-telemetry/research_agent.go`:

```go
package main

import (
    "context"
    "net/http"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

type ResearchAgent struct {
    *core.BaseAgent
    httpClient *http.Client  // Traced HTTP client
}

func NewResearchAgent() (*ResearchAgent, error) {
    agent := core.NewBaseAgent("research-assistant-telemetry")

    // Create traced HTTP client with custom transport for production use
    // This is the ACTUAL pattern from agent-with-telemetry example
    tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
        MaxIdleConns:        100,              // Connection pool size
        MaxIdleConnsPerHost: 10,               // Per-host connection limit
        IdleConnTimeout:     90 * time.Second, // Keep-alive timeout
        DisableKeepAlives:   false,            // Enable connection reuse
        ForceAttemptHTTP2:   true,             // Use HTTP/2 when available
    })
    tracedClient.Timeout = 30 * time.Second

    return &ResearchAgent{
        BaseAgent:  agent,
        httpClient: tracedClient,
    }, nil
}

func (a *ResearchAgent) callWeatherTool(ctx context.Context, city string) (*Weather, error) {
    // Create request WITH CONTEXT - this is crucial!
    req, err := http.NewRequestWithContext(ctx, "POST",
        "http://weather-tool:8080/api/capabilities/get_weather",
        strings.NewReader(`{"location": "`+city+`"}`))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")

    // The traced client automatically adds traceparent header!
    resp, err := a.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    // Parse response...
    var weather Weather
    json.NewDecoder(resp.Body).Decode(&weather)
    return &weather, nil
}
```

### What Happens Under the Hood

```
Agent's httpClient.Do(req)
    │
    ▼
┌─────────────────────────────────────────────────────────────────┐
│ otelhttp.Transport (inside TracedHTTPClient)                    │
│                                                                  │
│ 1. Extract trace context from ctx                               │
│    trace_id: abc123, span_id: span-001                          │
│                                                                  │
│ 2. Create child span for this HTTP call                         │
│    Name: "HTTP POST weather-tool:8080"                          │
│    Parent: span-001                                              │
│    New span_id: span-002                                         │
│                                                                  │
│ 3. Inject traceparent header into request                       │
│    traceparent: 00-abc123-span002-01                            │
│                                                                  │
│ 4. Make the actual HTTP request                                 │
│                                                                  │
│ 5. When response returns, end span with status                  │
└─────────────────────────────────────────────────────────────────┘
    │
    ▼
Weather Tool receives request with traceparent header
```

### Important: Always Pass Context!

The trace propagation only works if you pass the context:

```go
// CORRECT - trace context propagates
req, _ := http.NewRequestWithContext(ctx, "POST", url, body)
resp, _ := client.Do(req)

// WRONG - trace context is lost!
req, _ := http.NewRequest("POST", url, body)  // No context!
resp, _ := client.Do(req)  // traceparent header won't be added
```

### Simple vs Production Client

For quick development, you can use the simpler form:

```go
// Simple form - uses default transport settings
client := telemetry.NewTracedHTTPClient(nil)
```

For production, use `NewTracedHTTPClientWithTransport` with custom settings (as shown in the agent-with-telemetry example above) to control connection pooling and timeouts.

---

## 8. Complete Example: Multi-Service Tracing

The best way to understand distributed tracing is to look at the **actual working examples** in the TruvaG3 repository.

> **Working Examples:**
> - Agent: `examples/agent-with-telemetry/` - Full agent with tracing
> - Tool: `examples/tool-example/` - Weather tool with tracing

### The Architecture

```
User Request
    │
    ▼
┌─────────────────────────────────────────────────────────────────┐
│ Research Agent (Port 8092)                                       │
│ See: examples/agent-with-telemetry/                             │
│                                                                  │
│ - TracingMiddleware (extracts/creates trace)                    │
│ - TracedHTTPClient (propagates trace to tools)                  │
└───────────────────────────────┬─────────────────────────────────┘
                                │
        ┌───────────────────────┼───────────────────────┐
        │                       │                       │
        ▼                       ▼                       ▼
┌───────────────┐       ┌───────────────┐       ┌───────────────┐
│ Weather Tool  │       │ Stock Tool    │       │ Currency Tool │
│ (Port 8080)   │       │ (Port 8082)   │       │ (Port 8094)   │
│               │       │               │       │               │
│ TracingMW     │       │ TracingMW     │       │ TracingMW     │
│               │       │               │       │               │
│ tool-example/ │       │ stock-market- │       │ currency-tool/│
│               │       │ tool/         │       │               │
└───────────────┘       └───────────────┘       └───────────────┘
```

### Agent Code (from examples/agent-with-telemetry/research_agent.go)

This is a **simplified version** of the actual code. See the full implementation for additional features like metric declarations, AI integration, and more.

```go
package main

import (
    "bytes"
    "context"
    "encoding/json"
    "log"
    "net/http"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

type ResearchAgent struct {
    *core.BaseAgent
    httpClient *http.Client
}

func NewResearchAgent() (*ResearchAgent, error) {
    agent := core.NewBaseAgent("research-assistant-telemetry")

    // Create traced HTTP client with production settings
    // This is the ACTUAL pattern from the example
    tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
        DisableKeepAlives:   false,
        ForceAttemptHTTP2:   true,
    })
    tracedClient.Timeout = 30 * time.Second

    researchAgent := &ResearchAgent{
        BaseAgent:  agent,
        httpClient: tracedClient,
    }

    // Register capabilities
    researchAgent.registerCapabilities()
    return researchAgent, nil
}

func (r *ResearchAgent) registerCapabilities() {
    r.RegisterCapability(core.Capability{
        Name:        "research_topic",
        Description: "Researches a topic using multiple tools",
        Handler:     r.handleResearchTopic,
    })
}

func (r *ResearchAgent) handleResearchTopic(w http.ResponseWriter, req *http.Request) {
    ctx := req.Context()  // Contains trace context from TracingMiddleware

    var request struct {
        Topic string `json:"topic"`
    }
    json.NewDecoder(req.Body).Decode(&request)

    log.Printf("Starting research for topic: %s", request.Topic)

    // Call tools - trace context propagates via TracedHTTPClient
    weather, _ := r.callTool(ctx, "http://weather-tool:8080/api/capabilities/get_weather",
        map[string]string{"location": "London"})

    stock, _ := r.callTool(ctx, "http://stock-tool:8082/api/capabilities/stock_quote",
        map[string]string{"symbol": "AAPL"})

    log.Printf("Research completed, called 2 tools")

    // Return combined results
    json.NewEncoder(w).Encode(map[string]interface{}{
        "weather": weather,
        "stock":   stock,
    })
}

func (r *ResearchAgent) callTool(ctx context.Context, url string, params interface{}) (interface{}, error) {
    body, _ := json.Marshal(params)

    // CRITICAL: Use NewRequestWithContext to propagate trace!
    req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")

    // TracedHTTPClient adds traceparent header automatically
    resp, err := r.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var result interface{}
    json.NewDecoder(resp.Body).Decode(&result)
    return result, nil
}
```

### Agent Main (from examples/agent-with-telemetry/main.go)

```go
package main

import (
    "context"
    "log"
    "os"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

func main() {
    // Initialize telemetry BEFORE creating agent
    initTelemetry("research-assistant-telemetry")
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        telemetry.Shutdown(ctx)
    }()

    // Create agent
    agent, err := NewResearchAgent()
    if err != nil {
        log.Fatalf("Failed to create agent: %v", err)
    }

    // Create framework with tracing middleware
    framework, _ := core.NewFramework(agent,
        core.WithName("research-assistant-telemetry"),
        core.WithPort(8092),
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),

        // Add tracing middleware for incoming requests
        core.WithMiddleware(telemetry.TracingMiddleware("research-assistant-telemetry")),
    )

    ctx := context.Background()
    log.Println("Research Agent starting on port 8092...")
    framework.Run(ctx)
}

func initTelemetry(serviceName string) {
    env := os.Getenv("APP_ENV")
    if env == "" {
        env = "development"
    }

    var profile telemetry.Profile
    switch env {
    case "production", "prod":
        profile = telemetry.ProfileProduction
    case "staging", "stage":
        profile = telemetry.ProfileStaging
    default:
        profile = telemetry.ProfileDevelopment
    }

    config := telemetry.UseProfile(profile)
    config.ServiceName = serviceName

    if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
        config.Endpoint = endpoint
    }

    if err := telemetry.Initialize(config); err != nil {
        log.Printf("Warning: Telemetry init failed: %v", err)
    }
}
```

### Tool Code (from examples/tool-example/main.go)

```go
package main

import (
    "context"
    "log"
    "os"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

func main() {
    // Initialize telemetry
    initTelemetry("weather-service")
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        telemetry.Shutdown(ctx)
    }()

    // Create tool
    tool := NewWeatherTool()

    // Create framework with tracing middleware
    framework, _ := core.NewFramework(tool,
        core.WithName("weather-service"),
        core.WithPort(8080),
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),

        // Add tracing middleware - extracts trace from incoming requests
        core.WithMiddleware(telemetry.TracingMiddleware("weather-service")),
    )

    ctx := context.Background()
    log.Println("Weather Tool starting on port 8080...")
    framework.Run(ctx)
}

// initTelemetry follows the same pattern as the agent
func initTelemetry(serviceName string) {
    // ... same as agent example above
}
```

### The Result: Connected Traces

When you make a request to the agent:

```bash
curl -X POST http://localhost:8092/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic": "weather and stocks"}'
```

**In Jaeger, you'll see ONE trace spanning all services:**

```
research-agent: HTTP POST /api/capabilities/research_topic ─────────────────▶ 450ms
├── research-agent: callTool(weather) ─────────────────────▶ 200ms
│   └── weather-service: HTTP POST /api/capabilities/get_weather ──▶ 195ms
│
└── research-agent: callTool(stock) ────────────────────────▶ 220ms
    └── stock-service: HTTP POST /api/capabilities/stock_quote ────▶ 215ms
```

**In your logs, every entry has the same trace ID:**

```json
// research-agent log
{"level":"info","message":"Starting research","trace.trace_id":"abc123","trace.span_id":"1111"}

// weather-service log
{"level":"info","message":"Fetching weather","trace.trace_id":"abc123","trace.span_id":"2222"}

// stock-service log
{"level":"info","message":"Getting stock quote","trace.trace_id":"abc123","trace.span_id":"3333"}
```

---

## 9. Infrastructure Setup (Kubernetes)

For distributed tracing to work, you need a place to **collect** and **visualize** traces. Here's the recommended setup.

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Your Services                                 │
│                                                                      │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐              │
│  │   Agent      │  │ Weather Tool │  │ Stock Tool   │              │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘              │
│         │                 │                 │                        │
│         │ OTLP/HTTP       │ OTLP/HTTP       │ OTLP/HTTP             │
│         │ :4318           │ :4318           │ :4318                 │
│         └────────────────┬┴─────────────────┘                        │
│                          │                                           │
│                          ▼                                           │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │              OTEL Collector (otel-collector:4318)              │  │
│  │                                                                │  │
│  │  Receives traces → Batches them → Exports to backends         │  │
│  └─────────────────────────────┬─────────────────────────────────┘  │
│                                │                                     │
│              ┌─────────────────┴─────────────────┐                  │
│              │                                   │                  │
│              ▼                                   ▼                  │
│  ┌───────────────────┐               ┌───────────────────┐         │
│  │      Jaeger       │               │    Prometheus     │         │
│  │  (Trace Storage)  │               │  (Metric Storage) │         │
│  │   Port 16686 UI   │               │   Port 9090 UI    │         │
│  └───────────────────┘               └───────────────────┘         │
│              │                                   │                  │
│              └─────────────────┬─────────────────┘                  │
│                                │                                     │
│                                ▼                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │                    Grafana (:3000)                             │  │
│  │                                                                │  │
│  │   - Trace visualization (via Jaeger datasource)               │  │
│  │   - Metrics dashboards (via Prometheus datasource)            │  │
│  │   - Correlated views (trace + metrics together)               │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

### Environment Variables for Services

Every service that sends traces needs to know where the collector is:

```yaml
# In your Kubernetes deployment
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://otel-collector:4318"
  - name: APP_ENV
    value: "production"  # or "development" for 100% sampling
```

### OTEL Collector Configuration

The collector receives traces from your services and forwards them to Jaeger:

```yaml
# otel-collector.yaml (ConfigMap)
receivers:
  otlp:
    protocols:
      http:
        endpoint: "0.0.0.0:4318"
      grpc:
        endpoint: "0.0.0.0:4317"

processors:
  batch:
    timeout: 1s
    send_batch_size: 1024

exporters:
  otlp/jaeger:
    endpoint: "jaeger-collector:4317"
    tls:
      insecure: true

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp/jaeger]
```

### Quick Start: Deploy Infrastructure

If you're using the TruvaG3 examples, the infrastructure is already defined:

```bash
# Apply the infrastructure
kubectl apply -f examples/k8-deployment/otel-collector.yaml
kubectl apply -f examples/k8-deployment/jaeger.yaml
kubectl apply -f examples/k8-deployment/prometheus.yaml
kubectl apply -f examples/k8-deployment/grafana.yaml

# Verify everything is running
kubectl get pods -n truvag3-examples
```

---

## 10. Viewing Your Traces

Once your infrastructure is set up, here's how to view and analyze traces.

### Accessing Jaeger UI

```bash
# Port-forward to access Jaeger locally
kubectl port-forward -n truvag3-examples svc/jaeger-query 16686:80

# Open in browser
open http://localhost:16686
```

### Finding a Trace

1. **Select your service** from the "Service" dropdown
2. **Click "Find Traces"**
3. **Click on a trace** to see the full timeline

### What to Look For

**Healthy Trace:**
```
research-agent: POST /research ─────────────────────────▶ 150ms
├── weather-tool: POST /weather ─────────▶ 50ms
└── stock-tool: POST /stock ─────────▶ 45ms
```

**Problem: Sequential calls that should be parallel:**
```
research-agent: POST /research ─────────────────────────────────────▶ 300ms
├── weather-tool: POST /weather ─────────▶ 50ms
│                                        (100ms gap - why?)
└── stock-tool: POST /stock                         ─────────▶ 45ms
```

**Problem: One slow service:**
```
research-agent: POST /research ─────────────────────────────────────▶ 5200ms
├── weather-tool: POST /weather ─▶ 50ms
└── stock-tool: POST /stock ──────────────────────────────────────▶ 5100ms
    └── database query ────────────────────────────────────────────▶ 5050ms
```

### Searching by Trace ID

If you have a trace ID from your logs:

```bash
# In Jaeger, paste the trace ID directly in the search box
# Or construct the URL:
http://localhost:16686/trace/fee30b72efcbefd21fddf9cd56d2c8c9
```

### Correlating with Logs

1. Find the problematic span in Jaeger
2. Note the `trace_id`
3. Search your logs:
   ```bash
   # Kubernetes logs
   kubectl logs -n truvag3-examples deployment/stock-tool | grep "fee30b72efcbefd21fddf9cd56d2c8c9"

   # Or in Grafana Loki
   {app="stock-tool"} |= "fee30b72efcbefd21fddf9cd56d2c8c9"
   ```

### Using request_id for Troubleshooting

When you make an orchestration request, the API response includes a `request_id`:

```json
{
  "request_id": "1765636433370038463-370038546",
  "response": "Here's the weather in Tokyo...",
  "tools_used": ["weather-tool", "currency-tool"],
  "confidence": 1.0
}
```

**The `request_id` is your primary troubleshooting key** - it connects API responses to distributed traces and logs.

#### How request_id Relates to Traces

The orchestrator sets `request_id` as a span attribute on the trace:

```go
span.SetAttribute("request_id", requestID)
```

This means you can search for traces using the `request_id` from your API response.

#### Finding Traces by request_id in Jaeger UI

1. Open Jaeger: `http://localhost:16686`
2. Select service: `travel-research-orchestration` (or your agent's service name)
3. In the **Tags** field, enter: `request_id=1765636433370038463-370038546`
4. Click **Find Traces**
5. Click on the trace to see the full waterfall view

#### Finding Traces by request_id via CLI

```bash
# Search traces by request_id tag
curl -s "http://localhost:16686/api/traces?service=travel-research-orchestration&tags=%7B%22request_id%22%3A%221765636433370038463-370038546%22%7D" | jq '.data[0].traceID'

# Get the full trace once you have the trace_id
curl -s "http://localhost:16686/api/traces/cd41f5a1a12afa1158f3e666a340d543" | jq '.data[0]'
```

#### Direct URL to Trace

Once you know the trace_id, you can construct a direct URL:

```
http://localhost:16686/trace/<trace_id>
```

#### Searching Logs by request_id

The `request_id` also appears in all structured logs throughout the request lifecycle:

```bash
# Search pod logs
kubectl logs -n truvag3-examples deploy/travel-research-agent | grep "1765636433370038463-370038546"

# Search across all pods
kubectl logs -n truvag3-examples -l app.kubernetes.io/part-of=truvag3 --all-containers | grep "1765636433370038463-370038546"
```

#### What You'll See in the Trace

A typical orchestration trace shows the complete request flow:

```
HTTP POST /orchestrate/natural (15.87s)
└── orchestrator.process_request (15.87s)
    ├── orchestrator.build_prompt (1.4ms)
    ├── prompt-builder.build (0.1ms)
    ├── HTTP POST → geocode_location (594ms)
    ├── HTTP POST → get_current_weather (610ms)
    ├── HTTP POST → convert_currency (894ms)
    ├── HTTP POST → stock_quote (223ms)
    ├── HTTP POST → get_country_info (200ms)
    └── HTTP POST → search_news (200ms)
```

Each span shows:
- **Duration** (colored bars in the UI)
- **Tags** (click span to see `request_id`, `capability`, etc.)
- **Events** (including `error_analyzer.*` events if LLM error analysis occurred)

#### Troubleshooting Checklist

| What You Have | How to Find the Trace |
|---------------|----------------------|
| `request_id` from API response | Search Jaeger by tag: `request_id=<value>` |
| `trace_id` from logs | Direct URL: `http://localhost:16686/trace/<trace_id>` |
| Time range of issue | Filter by service + time in Jaeger UI |
| Error message | Search by tag: `error=true` |

---

## 11. Required Patterns for Framework-Level Tracing

This section documents the **required patterns** used throughout the TruvaG3 framework for consistent distributed tracing. These patterns are implemented in [orchestrator.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/orchestrator.go) and [executor.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/executor.go).

### Pattern 1: Logger Nil Check

**Always check for nil before logging.** This ensures graceful degradation when logging is not configured.

```go
// From orchestration/orchestrator.go:573-580
if o.logger != nil {
    o.logger.InfoWithContext(ctx, "Starting request processing", map[string]interface{}{
        "operation":      "process_request",
        "request_id":     requestID,
        "request_length": len(request),
    })
}
```

**Why this matters:**
- Components may run without a logger configured
- Prevents nil pointer panics in production
- Allows flexible deployment configurations

### Pattern 2: Operation Field in Every Log

**Every log entry MUST include an `operation` field.** This enables filtering and analysis across distributed systems.

```go
// From orchestration/orchestrator.go:598-604
if o.logger != nil {
    o.logger.ErrorWithContext(ctx, "Plan generation failed", map[string]interface{}{
        "operation":   "plan_generation",  // REQUIRED
        "request_id":  requestID,
        "error":       err.Error(),
        "duration_ms": time.Since(startTime).Milliseconds(),
    })
}
```

**Standard operation values in orchestration:**
- `process_request` - Main request handling
- `plan_generation` - LLM plan creation
- `plan_execution` - Step execution
- `agent_discovery` - Finding agents in catalog
- `llm_call` - LLM API calls

### Pattern 3: Request ID Generation and Context Propagation

**Generate request_id early and propagate via context baggage.** This enables end-to-end request tracking.

```go
// From orchestration/orchestrator.go:567-571

// Step 1: Generate unique request_id
requestID := generateRequestID()

// Step 2: Add to context baggage for downstream access
ctx = telemetry.WithBaggage(ctx, "request_id", requestID)

// Step 3: Set as span attribute
if span != nil {
    span.SetAttribute("request_id", requestID)
}

// Step 4: Include in all logs
if o.logger != nil {
    o.logger.InfoWithContext(ctx, "Starting request", map[string]interface{}{
        "operation":  "process_request",
        "request_id": requestID,  // Always include
    })
}
```

**Downstream retrieval:**

```go
// Any component can retrieve request_id from context baggage
var requestID string
if baggage := telemetry.GetBaggage(ctx); baggage != nil {
    requestID = baggage["request_id"]
}
```

### Pattern 4: telemetry.RecordSpanError for All Errors

**Always record errors on the span for trace visibility.** This makes errors visible in Jaeger.

At an external-service or AI-provider boundary, record a sanitized derivative
when the original error may contain response bodies, endpoints, credential
diagnostics, or other sensitive material. Preserve the original error in the
caller-visible error chain. The AI module uses
`providers.RecordObservationError` for this purpose; it records the sanitized
exception and bounded `ai.error_type` while still marking the span as failed.

```go
// From orchestration/executor.go:1228-1235
agentInfo := e.findAgentByName(step.AgentName)
if agentInfo == nil {
    err := fmt.Errorf("agent %s not found in catalog", step.AgentName)

    // Record error on span FIRST (visible in Jaeger)
    telemetry.RecordSpanError(ctx, err)

    // Then log the error
    if e.logger != nil {
        e.logger.ErrorWithContext(ctx, "Agent not found in catalog", map[string]interface{}{
            "operation":  "agent_discovery",
            "step_id":    step.StepID,
            "agent_name": step.AgentName,
        })
    }
    return result
}
```

### Pattern 5: telemetry.Counter with Module Label

**Include the `module` label in all counter metrics.** This enables per-module metric analysis.

```go
// From orchestration/orchestrator.go:1123-1124
telemetry.Counter("plan_generation.total",
    "module", telemetry.ModuleOrchestration,  // REQUIRED
    "status", "error",
)

// From orchestration/executor.go (success case)
telemetry.Counter("orchestration.hybrid_resolution.success",
    "capability", capability,
    "module", telemetry.ModuleOrchestration,  // REQUIRED
)
```

**Standard module constants:**
- `telemetry.ModuleOrchestration` - Orchestration module
- `telemetry.ModuleAI` - AI module
- `telemetry.ModuleResilience` - Resilience module
- `telemetry.ModuleCore` - Core module

### Pattern 6: request_id as First Span Attribute

**When calling `telemetry.AddSpanEvent()`, always put `request_id` as the first attribute.** This ensures consistent attribute ordering in Jaeger.

```go
// From orchestration/orchestrator.go:1092-1099
telemetry.AddSpanEvent(ctx, "llm.plan_generation.request",
    attribute.String("request_id", requestID),  // FIRST attribute
    attribute.String("prompt", truncateString(prompt, 2000)),
    attribute.Int("prompt_length", len(prompt)),
    attribute.Float64("temperature", 0.3),
    attribute.Int("max_tokens", 2000),
    attribute.Int("attempt", attempt),
)
```

### Complete Error Handling Pattern

Here's the complete pattern combining all requirements:

```go
func (e *Executor) executeStep(ctx context.Context, step *Step) *StepResult {
    // Get request_id from context (propagated via baggage)
    var requestID string
    if baggage := telemetry.GetBaggage(ctx); baggage != nil {
        requestID = baggage["request_id"]
    }

    result, err := e.doWork(ctx, step)
    if err != nil {
        // 1. Record error on span (visible in Jaeger)
        telemetry.RecordSpanError(ctx, err)

        // 2. Add span event with request_id first
        telemetry.AddSpanEvent(ctx, "step.execution.error",
            attribute.String("request_id", requestID),
            attribute.String("step_id", step.StepID),
            attribute.String("error", err.Error()),
        )

        // 3. Emit counter metric with module label
        telemetry.Counter("orchestration.step.failed",
            "step_type", step.Type,
            "module", telemetry.ModuleOrchestration,
        )

        // 4. Log with nil check, operation field, and request_id
        if e.logger != nil {
            e.logger.ErrorWithContext(ctx, "Step execution failed", map[string]interface{}{
                "operation":  "step_execution",
                "request_id": requestID,
                "step_id":    step.StepID,
                "error":      err.Error(),
            })
        }

        return &StepResult{Success: false, Error: err.Error()}
    }

    return result
}
```

---

## 12. Best Practices

### DO

1. **Always pass context through your code:**
   ```go
   // GOOD
   result, err := processData(ctx, input)

   // BAD
   result, err := processData(input)  // Lost trace context!
   ```

2. **Include trace IDs in logs for correlation:**
   ```go
   // Extract trace context and include in logs
   tc := telemetry.GetTraceContext(ctx)
   log.Printf("Processing request trace_id=%s span_id=%s", tc.TraceID, tc.SpanID)
   ```

3. **Initialize telemetry early:**
   ```go
   func main() {
       initTelemetry("my-service")
       defer telemetry.Shutdown(context.Background())
       // ... rest of main
   }
   ```

4. **Exclude noisy endpoints:**
   ```go
   config := &telemetry.TracingMiddlewareConfig{
       ExcludedPaths: []string{"/health", "/metrics", "/ready"},
   }
   ```

5. **Reuse HTTP clients:**
   ```go
   // GOOD - create once
   client := telemetry.NewTracedHTTPClient(nil)

   // BAD - creates connection pool per request
   for _, url := range urls {
       client := telemetry.NewTracedHTTPClient(nil)  // Don't do this!
       client.Get(url)
   }
   ```

6. **Inject telemetry into components that create spans:**
   ```go
   // Enable orchestrators to create child spans linked to parent requests
   if provider := telemetry.GetTelemetryProvider(); provider != nil {
       orchestrator.SetTelemetry(provider)
   }
   ```

### DON'T

1. **Don't forget `context.Background()` for background tasks:**
   ```go
   // If you're starting a background goroutine, use a fresh context
   // Note: For custom spans, use OpenTelemetry's tracer directly:
   //   tracer := otel.Tracer("my-service")
   //   ctx, span := tracer.Start(context.Background(), "background-task")
   go func() {
       ctx := context.Background()
       // ... work with ctx
   }()
   ```

2. **Don't trace every internal operation:**
   ```go
   // For custom spans, use OpenTelemetry's tracer:
   tracer := otel.Tracer("my-service")

   // BAD - too noisy
   for i := 0; i < 1000; i++ {
       _, span := tracer.Start(ctx, "loop-iteration")
       doTinyThing()
       span.End()
   }

   // GOOD - trace meaningful operations
   ctx, span := tracer.Start(ctx, "process-batch")
   for i := 0; i < 1000; i++ {
       doTinyThing()
   }
   span.End()
   ```

3. **Don't forget to call `Shutdown()`:**
   ```go
   // BAD - traces may be lost
   func main() {
       telemetry.Initialize(config)
       runApp()
       // Exit without flushing!
   }

   // GOOD
   func main() {
       telemetry.Initialize(config)
       defer telemetry.Shutdown(context.Background())  // Flushes pending traces
       runApp()
   }
   ```

---

## 13. Troubleshooting

### Problem: Traces Not Appearing in Jaeger

**Symptoms:** Services are running, but no traces in Jaeger UI.

**Check 1: OTEL Collector connectivity**
```bash
# Check if collector is running
kubectl get pods -n truvag3-examples | grep otel-collector

# Check collector logs
kubectl logs -n truvag3-examples deployment/otel-collector
```

**Check 2: Service environment variables**
```bash
# Verify OTEL endpoint is set
kubectl exec -n truvag3-examples deployment/weather-tool -- env | grep OTEL
# Should show: OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
```

**Check 3: Telemetry initialization**
```bash
# Check service logs for telemetry initialization
kubectl logs -n truvag3-examples deployment/weather-tool | grep -i telemetry
# Should show: "Telemetry initialized for weather-service"
```

### Problem: Traces Are Disconnected

**Symptoms:** Traces appear, but agent and tools have separate traces (not connected).

**Cause:** Context not propagating between services.

**Fix 1: Use `NewRequestWithContext`**
```go
// WRONG
req, _ := http.NewRequest("POST", url, body)

// RIGHT
req, _ := http.NewRequestWithContext(ctx, "POST", url, body)
```

**Fix 2: Use `TracedHTTPClient`**
```go
// WRONG - regular http.Client doesn't inject headers
client := &http.Client{}

// RIGHT - traced client injects traceparent header
client := telemetry.NewTracedHTTPClient(nil)
```

### Problem: Logs Don't Have Trace IDs

**Symptoms:** Traces work, but logs don't show `trace.trace_id`.

**Cause:** Not extracting trace context from the request context.

**Fix:**
```go
// WRONG - no trace context in log
log.Printf("Processing request")

// RIGHT - extract and include trace ID
tc := telemetry.GetTraceContext(ctx)
log.Printf("Processing request trace_id=%s span_id=%s", tc.TraceID, tc.SpanID)
```

### Problem: Too Many Traces (Noisy)

**Symptoms:** Millions of traces, hard to find important ones.

**Fix 1: Reduce sampling rate for production**
```go
// In telemetry initialization
config := telemetry.UseProfile(telemetry.ProfileProduction)  // 0.1% sampling
```

**Fix 2: Exclude health endpoints**
```go
config := &telemetry.TracingMiddlewareConfig{
    ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live"},
}
```

### Problem: High Latency from Tracing

**Symptoms:** Service is slower after adding tracing.

**Check 1: Ensure collector is batching**
```yaml
# In otel-collector config
processors:
  batch:
    timeout: 1s
    send_batch_size: 1024  # Don't send one trace at a time!
```

**Check 2: Use async export (default)**
The telemetry module uses asynchronous export by default. If you've customized it, ensure you're not using synchronous export.

---

## 14. Quick Reference

### Adding Tracing to a New Service

```go
// 1. Initialize telemetry
telemetry.Initialize(telemetry.Config{
    ServiceName: "my-service",
    Endpoint:    os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
})
defer telemetry.Shutdown(context.Background())

// 2. Add TracingMiddleware to framework
framework, _ := core.NewFramework(component,
    core.WithMiddleware(telemetry.TracingMiddleware("my-service")),
)

// 3. Use TracedHTTPClient for outgoing calls
client := telemetry.NewTracedHTTPClient(nil)

// 4. Always pass context
req, _ := http.NewRequestWithContext(ctx, "POST", url, body)
resp, _ := client.Do(req)

// 5. Include trace ID in logs for correlation
tc := telemetry.GetTraceContext(ctx)
log.Printf("Message trace_id=%s span_id=%s", tc.TraceID, tc.SpanID)
```

### Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTEL Collector endpoint | `http://otel-collector:4318` |
| `APP_ENV` | Environment (affects sampling) | `production`, `development` |

### Key Types and Functions

| Type/Function | Purpose |
|---------------|---------|
| `telemetry.TracingMiddleware()` | Extracts trace from incoming requests |
| `telemetry.NewTracedHTTPClient()` | Injects trace into outgoing requests (simple form) |
| `telemetry.NewTracedHTTPClientWithTransport()` | Injects trace with custom transport (production) |
| `telemetry.GetTraceContext(ctx)` | Returns `TraceContext` struct with `.TraceID` and `.SpanID` |
| `telemetry.HasTraceContext(ctx)` | Returns true if context has valid trace info |
| `telemetry.AddSpanEvent(ctx, name, attrs...)` | Add named events to the current span |
| `telemetry.RecordSpanError(ctx, err)` | Record an error on the current span |
| `telemetry.SetSpanAttributes(ctx, attrs...)` | Add attributes to the current span |
| `telemetry.GetTelemetryProvider()` | Returns `core.Telemetry` for injecting into orchestrators |
| `telemetry.TracingMiddlewareConfig` | Configure path exclusions, span names |

### Telemetry Profiles

| Profile | Sampling | Use Case |
|---------|----------|----------|
| `ProfileDevelopment` | 100% | Local development, see everything |
| `ProfileStaging` | 10% | Testing environments |
| `ProfileProduction` | 0.1% | High-traffic production |

---

## 15. LLM Telemetry in Orchestration (Automatic)

When using the orchestration module, **LLM interactions are automatically traced** without any additional developer code. This gives you complete visibility into AI operations within Jaeger.

### What Gets Captured Automatically

The orchestration module emits span events for every LLM interaction:

| Event Name | Description | Key Attributes |
|------------|-------------|----------------|
| `llm.plan_generation.request` | AI prompt for routing plan creation | `prompt`, `prompt_length`, `temperature`, `max_tokens`, `model`¹ |
| `llm.plan_generation.response` | AI response for routing plan | `response`, `prompt_tokens`, `completion_tokens`, `total_tokens`, `duration_ms` |
| `llm.micro_resolution.request` | AI prompt for parameter binding | `capability`, `prompt`, `hint`, `model`¹ |
| `llm.micro_resolution.response` | AI response for parameter binding | `capability`, `response`, `duration_ms` |
| `llm.synthesis.request` | AI prompt for result synthesis | `original_request`, `prompt`, `step_count`, `temperature`, `max_tokens`, `model`¹ |
| `llm.synthesis.response` | AI response for synthesis | `response`, `prompt_tokens`, `completion_tokens`, `total_tokens`, `duration_ms` |
| `error_analyzer.llm_error_analysis_start` | Error analysis begins | `error_type`, `original_error`, `capability`, `tool_name` |
| `error_analyzer.llm_error_analysis_result` | Error analysis result | `reason`, `recoverable`, `suggested_changes`, `has_suggestions` |
| `error_analyzer.llm_error_analysis_retry` | Automatic retry with suggested fixes | `capability`, `original_params`, `suggested_changes`, `retry_count` |
| `contextual_re_resolution.start` | Layer 4 semantic retry begins | `step_id`, `capability`, `retry_count`, `http_status`, `source_data_keys`, `model`¹ |
| `contextual_re_resolution.complete` | Layer 4 semantic retry finished | `should_retry`, `analysis`, `corrected_params_count`, `duration_ms` |
| `contextual_re_resolution.error` | Layer 4 LLM call failed | `error`, `duration_ms` |
| `semantic_retry_applied` | Executor applies corrected parameters | `step_id`, `capability`, `analysis`, `corrected_params` |
| `result_trim.structural` | Structural trimming analysis begins | `request_id`, `step_id`, `agent_name`, `original_bytes`, `budget_bytes`, `keyword_count` |
| `result_trim.completed` | Per-step trimming outcome | `request_id`, `step_id`, `agent_name`, `method`, `original_bytes`, `trimmed_bytes`, `fields_kept`, `fields_dropped`, `backfilled_count`\*, `threshold_skipped`\*, `budget_allocated`\*, `keywords`\*, `matched_paths`\* |
| `result_trim.synthesis` | Total bytes at synthesis start | `request_id`, `original_total_bytes`, `prompt_length`, `step_count` |
| `result_trim.micro_resolution` | Source data trimmed for parameter binding | `request_id`, `step_id`, `capability`, `original_bytes`, `trimmed_bytes`, `configured_budget_bytes`, `effective_budget_bytes` |
| `result_trim.agent_input` | Parameter trimmed before agent HTTP call | `request_id`, `step_id`, `agent_name`, `parameter_name`, `original_bytes`, `trimmed_bytes`, `budget_bytes` |
| `result_trim.semantic_retry` | Source data trimmed for Layer 4 retry | `request_id`, `step_id`, `capability`, `original_bytes`, `trimmed_bytes`, `configured_budget_bytes`, `effective_budget_bytes` |
| `llm.event_summarization.request` | Batched LLM call for step summaries | `request_id`, `step_count`, `prompt_length`, `model`¹ |
| `llm.event_summarization.response` | Summarization result | `request_id`, `response_length`, `model`¹ |
| `llm.activity_compaction.request` | Full activity compaction LLM call | `request_id`, `event_count`, `prompt_length` |
| `llm.activity_compaction.response` | Compaction result | `request_id`, `response_length` |
| `llm.activity_compaction.incremental_request` | Incremental digest update LLM call | `request_id`, `new_event_count`, `prompt_length` |
| `llm.activity_compaction.incremental_response` | Incremental update result | `request_id`, `response_length` |
| `activity.compaction.cache_decision` | Digest cache path taken | `request_id`, `cache_hit`, `path`², `duration_ms` |
| `memory.enrichment.injected` | Memory context injected into planning prompt | `request_id`, `entities_found`, `context_chars` |
| `memory.record.events_written` | Events recorded after execution | `request_id`, `agent_name`, `events_recorded` |
| `memory.entity_extraction.completed` | Entity extraction outcome — emitted at both `MemoryEnrichmentHook.BeforePlanning` and `MemoryRecordHook.AfterExecution` (once per step at the record hook). Use this to verify which extractor is in use, whether the LLM produced entities, and whether entity-less events are being recorded. | `request_id`, `step_id`³, `hook` (`enrichment`/`record`), `extractor_type` (`noop`/`llm`/`custom`/`none`), `source` (`llm_entities`/`explicit_metadata`/`none`), `entities_found` |
| `memory.reflection.entity_processed` | Reflection job successfully extracted ≥1 fragment for an entity. Added to the `memory.reflection_pass` parent span (see "Reflection Job Spans" below). | `pass_id`, `entity_type`, `entity_id`, `fragments` |
| `memory.reflection.llm_response` | Reflector LLM call completed for one entity. Added to the `memory.reflection` child span. | `request_id`, `response_length` |
| `user_memory.recall.identity` | Recall identity facts for user | `request_id`, `user_id`, `namespace` |
| `user_memory.enrichment.injected` | User profile injected into planning prompt | `request_id`, `user_id`, `facts_count`, `profile_chars` |
| `user_memory.extraction.llm_request` | LLM fact extraction from conversation | `request_id`, `user_id` |
| `user_memory.extraction.complete` | Extraction + reconciliation finished | `request_id`, `user_id`, `candidates`, `stored` |
| `user_memory.summary.llm_request` | Session summary LLM generation | `request_id`, `user_id` |
| `user_memory.summary.stored` | Session summary fact stored | `request_id`, `user_id` |
| `activity.coordination.complete` | Activity signals discovered and formatted | `request_id`, `signals_discovered`, `signals_shown` |
| `activity.cleanup.complete` | Activity signal cleaned up after synthesis | `request_id` |
| `orchestrator.remediation.triggered` | Phase loop forced a remediation continuation after one or more steps were skipped because their template-referenced dependencies failed; the next phase runs even if the current plan marked itself terminal. Rare — at most once per orchestration. `has_failure_pattern` records whether a shared-error pattern summary was embedded into the remediation prompt. | `request_id`, `phase_number`, `plan_id`, `skipped_count`, `skipped_step_ids`, `has_failure_pattern` |
| `orchestrator.clarification_short_circuit` | Planner emitted `needs_user_input` and the phase loop terminated early instead of starting another phase. Conditional — only present on clarification turns. | `request_id`, `phase_number`, `question`, `missing_field_count`, `prior_completed_steps` |
| `orchestrator.synthesis.clarification_mode` | Synthesizer entered clarification mode and used the augmented system prompt to weave the planner's question into a conversational reply. Conditional — only on clarification turns. | `request_id`, `question`, `missing_field_count`, `has_partial_progress` |
| `orchestrator.terminal_synthesis.normalized` | Acceptance-time normalizer stripped a terminal synthesis pseudo-step and collapsed the plan toward a zero-step terminal plan (the framework synthesizes the final answer). NOT an error — no `RecordSpanError`. Paired counter: `orchestration.plan.terminal_synthesis_normalized`. | `request_id`, `plan_id`, `dropped_agent`, `dropped_capability`, `remaining_steps` |
| `orchestrator.plan_validation.exhausted` | Plan still failed validation after `MaxValidationRounds` regenerations; the phase fails explicitly rather than dispatching a known-bad plan. Paired with `RecordSpanError` and counter `orchestration.plan.validation_exhausted` (`error_type=plan_validation_exhausted`). | `request_id`, `phase_number`, `rounds` |
| `llm.tiered_selection.empty_recovered` | Tiered selector returned `[]`; defensive recovery used `prior_tool_ids` from phase context instead of the all-agents fallback, preserving tiered selection's token-saving purpose. Rare — only fires when the selector returns empty in a continuation phase. | `request_id`, `prior_count`, `attempt` |
| `llm.tiered_selection.semantic_empty_phase1` | Phase 1 (or continuation without prior tools) saw a semantic-empty `[]` selector response; retries are short-circuited and the sentinel error is returned so `selectTools` falls back to all-agents after exactly one LLM call. Common for memory-recall and conversational queries. | `request_id`, `attempt` |

\* Conditionally present — only included when > 0 or non-empty.
¹ `model` attribute is only present when a model override is configured (via `With*Model()` or `TRUVAG3_*_MODEL` env var).
² `path` values: `full` (cache miss), `cached` (hit, 0 new events), `incremental` (hit, few new events), `full_recompact` (hit, burst of new events).
³ `step_id` is only present when emitted from the record hook (one event per step). The enrichment hook emits one event per request without `step_id` because enrichment runs once per request.

### Background-Job Spans

Most orchestration instrumentation in the table above is **span events** attached to a parent span that already exists for the user request. Background jobs are different: they run detached from any user request (no inbound HTTP call), so there is no pre-existing parent span to attach events to. Instead each background `core.Runnable` creates its own dedicated root spans so each pass appears as a self-contained trace tree in Jaeger.

In-tree background jobs that follow this pattern: `memory.ReflectionJob` (LLM-driven Tier 2→3 reflection — bridging episodic events to semantic knowledge; distinct from Tier 2 `compact` maintenance) and `core.MemoryStoreSweeper` (periodic eviction of expired `*core.MemoryStore` entries).

| Span Name | Created By | Typical Lifetime | Attributes |
|-----------|-----------|------------------|------------|
| `memory.reflection_pass` | `ReflectionJob.RunOnce` ([memory/reflection_job.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflection_job.go)) — root span covering one entire pass | Tens of seconds to a few minutes per pass, depending on how many entities qualify and how fast the LLM responds | `pass_id`, `domain`, `age_threshold` |
| `memory.reflection` | `LLMMemoryReflector.Reflect` ([memory/reflector.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflector.go)) — child span per entity processed in the pass | A few seconds per entity (one LLM call) | `request_id`, `entity_type`, `entity_id` |
| `memory.sweep_pass` | `MemoryStoreSweeper.runSweepPass` ([core/memory_store.go](https://github.com/truvaagents/truva-g3/blob/main/core/memory_store.go)) — root span covering one eviction-sweep tick. Created only when `WithMemoryStoreSweeperTelemetry` is set; otherwise the sweeper emits metrics + logs without spans | Sub-millisecond to a few milliseconds per pass for typical per-agent caches (~10³ entries) | `sweep_id`, `interval`, `deleted_count`, `duration_ms` |

**Span tree** for a pass that processes 3 entities:

```
memory.reflection_pass                           pass_id=reflect-19e210c3
│
├── memory.reflection  entity=pod/product-catalog-api
│   └── ai.chain.generate_response               (reflector LLM call)
│
├── memory.reflection  entity=pod/orch-1774585774
│   └── ai.chain.generate_response
│
└── memory.reflection  entity=pod/agent-with-human-approval
    └── ai.chain.generate_response
```

**Span events** on the tree above (visible in Jaeger's "Logs" tab when you click on the respective span — they are *not* separate tree nodes):

- On each `memory.reflection` span: one `memory.reflection.llm_response` event recording the LLM response length
- On the `memory.reflection_pass` span: one `memory.reflection.entity_processed` event per entity that produced ≥1 fragment (so 0 to N events per pass, where N is the entity count)

**Trace correlation with user requests**: `pass_id` is also injected into OTel baggage as `request_id`, so both the orchestration `request_id` filter in Jaeger and the `request_id` field in agent logs find reflection-pass traces alongside user-request traces. Reflection passes sort into the same timeline as user activity with IDs of the form `reflect-XXXXXXXX`. See [AGENT_MEMORY_USER_GUIDE.md — Long-Term Knowledge Retention](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md#long-term-knowledge-retention-the-reflection-job) for the broader context.

### What Developers See in Jaeger

When you click on an orchestration span in Jaeger and expand the **Logs** tab, you'll see detailed LLM interactions:

```
▼ llm.plan_generation.request
  prompt: "You are an AI orchestrator. Given available tools and a user request..."
  prompt_length: 2456
  temperature: 0.3
  max_tokens: 2000

▼ llm.plan_generation.response
  response: "Based on the user request, I recommend the following execution plan..."
  prompt_tokens: 1234
  completion_tokens: 456
  total_tokens: 1690
  duration_ms: 2341
```

For error recovery scenarios, you'll see the full diagnostic chain:

```
▼ error_analyzer.llm_error_analysis_start
  error_type: "invalid_parameter"
  original_error: "The country parameter '대한민국' is not a valid ISO country code"
  capability: "get_country_info"
  tool_name: "country-info-tool"

▼ error_analyzer.llm_error_analysis_result
  reason: "The country parameter '대한민국' is provided in Korean..."
  recoverable: true
  suggested_changes: {"country":"South Korea"}
  has_suggestions: true

▼ error_analyzer.llm_error_analysis_retry
  capability: "get_country_info"
  original_params: {"country":"대한민국"}
  suggested_changes: {"country":"South Korea"}
  retry_count: 1
```

For **semantic retry scenarios** (Layer 4), where computation is needed to fix parameters:

```
▼ error_analyzer.analysis_complete
  should_retry: false
  reason: "Cannot be fixed by modifying request parameters..."
  suggested_changes_count: 0

▼ contextual_re_resolution.start
  step_id: "step-5-convert_currency"
  capability: "convert_currency"
  retry_count: 0
  http_status: 400
  source_data_keys: 5

▼ contextual_re_resolution.complete
  should_retry: true
  analysis: "The amount should be 100 × 468.285 = 46828.5 USD"
  corrected_params_count: 3
  duration_ms: 1247

▼ semantic_retry_applied
  step_id: "step-5-convert_currency"
  capability: "convert_currency"
  corrected_params: {"from":"USD","to":"KRW","amount":46828.5}
```

For **result trimming scenarios**, where large step results are reduced before synthesis:

```
▼ result_trim.structural
  request_id: "abc-123"
  step_id: "step-1"
  agent_name: "devops-tool"
  original_bytes: 296000
  budget_bytes: 32768
  keyword_count: 4

▼ result_trim.completed
  request_id: "abc-123"
  step_id: "step-1"
  agent_name: "devops-tool"
  method: "structural"
  original_bytes: 296000
  trimmed_bytes: 31842
  fields_kept: 12
  fields_dropped: 47
  backfilled_count: 3
  keywords: "pods,status,namespace,running"
  matched_paths: "data.items[0].status,data.items[0].metadata.namespace"
```

For **shared memory scenarios**, you'll see pipeline hooks and LLM compaction as dedicated child spans:

```
▼ pipeline.hook.before_planning.memory-enrichment (2836ms)
  ▼ activity.compaction.cache_decision
    cache_hit: true
    path: "incremental"
    duration_ms: 2705

  ▼ memory.enrichment.injected
    entities_found: 0
    context_chars: 6424

  └─ orchestrator.activity_compaction_incremental (2704ms)  ← child span
       new_event_count: 4
       ▼ llm.activity_compaction.incremental_request
         prompt_length: 3193
       ▼ llm.activity_compaction.incremental_response
         response_length: 2006

▼ pipeline.hook.after_execution.memory-record (1545ms)
  ▼ memory.record.events_written
    events_recorded: 1

▼ pipeline.hook.before_planning.activity-announcement (2ms)
  ▼ activity.coordination.complete
    signals_discovered: 2
    signals_shown: 1
```

For **conversation-history preparation**, the orchestration request trace now includes a dedicated prepare span, and optionally a nested compaction span on Tier 2 paths:

```
▼ orchestrator.process_request (4821ms)
  ▼ conversation_history.prepare (37ms)
    request_id: "orch-123"
    original_request_id: "orch-123"
    path: "metadata_turns"  // metadata_text | metadata_turns | hook

    ▼ conversation_history.compact (24ms)  // only when Tier 2 runs
      request_id: "orch-123"
      original_request_id: "orch-123"
      prior_summary_chars: 812
      new_turns: 6
      result_chars: 391

  ▼ pipeline.hook.before_planning.memory-enrichment (2836ms)
  ▼ ai.chain.generate_response (planner call)
```

### Zero Developer Configuration Required

This telemetry is **built into the orchestration framework** at:
- [orchestration/orchestrator.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/orchestrator.go) - Plan generation, phase loop short-circuit, termination_reason classification
- [orchestration/micro_resolver.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/micro_resolver.go) - Parameter resolution
- [orchestration/synthesizer.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/synthesizer.go) - Result synthesis, clarification mode
- [orchestration/tiered_capability_provider.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/tiered_capability_provider.go) - Tiered tool selection, semantic-empty short-circuit
- [orchestration/error_analyzer.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/error_analyzer.go) - Error analysis and recovery
- [orchestration/contextual_re_resolver.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/contextual_re_resolver.go) - Semantic retry (Layer 4)
- [orchestration/structural_trimmer.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/structural_trimmer.go) - Result trimming analysis
- [orchestration/executor.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/executor.go) - Agent input trimming
- [orchestration/memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/memory_hooks.go) - Memory enrichment and recording
- [orchestration/activity_compactor.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/activity_compactor.go) - Activity compaction (full + incremental)
- [orchestration/event_summarizer.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/event_summarizer.go) - Event summarization
- [orchestration/activity_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/activity_hooks.go) - Activity coordination signals

**Developers don't need to add any code** to get LLM visibility. Simply:
1. Use the orchestration module as documented
2. Enable telemetry (as shown in this guide)
3. View traces in Jaeger

### Use Cases for LLM Telemetry

| Scenario | What to Look For |
|----------|------------------|
| Slow orchestration requests | Check `duration_ms` on LLM response events |
| High AI costs | Sum `total_tokens` across requests |
| Poor routing decisions | Read the `prompt` and `response` to understand AI reasoning |
| Failed parameter binding | Check `llm.micro_resolution.*` events |
| Error recovery debugging | Follow `error_analyzer.*` events for the full recovery chain |
| Semantic retry debugging | Follow `contextual_re_resolution.*` and `semantic_retry_applied` events |
| Prompt engineering | Export prompts from traces to analyze and improve |
| Result data loss debugging | Follow `result_trim.*` events — check `result_trim.completed` for `fields_dropped > 0` or large `original_bytes` vs small `trimmed_bytes`. Use `result_trim.structural` to inspect keyword extraction. |

### Example: Debugging LLM Error Recovery

To analyze how the orchestrator recovered from an error:

1. Find the trace in Jaeger using `request_id` or time filter
2. Locate the span with `error_analyzer.*` events
3. Examine:
   - `error_analyzer.llm_error_analysis_start` - What error occurred
   - `error_analyzer.llm_error_analysis_result` - What the LLM suggested
   - `error_analyzer.llm_error_analysis_retry` - What parameters were retried
4. If the retry succeeded, you'll see a subsequent successful tool call span

This automatic visibility into AI decision-making makes debugging orchestration issues straightforward without instrumenting your own code.

---

## 16. HITL Cross-Trace Correlation

Human-in-the-Loop (HITL) flows present a unique distributed tracing challenge: the original request trace ends when execution pauses for human approval, and a completely new trace begins when the user resumes. Without special handling, these traces appear disconnected in Jaeger, making it difficult to understand the full request lifecycle.

### The Problem: Disconnected Traces

```
Request A: User sends query → Plan generated → HITL pauses (trace ends)
                          [minutes/hours pass - human reviews]
Request B: User approves → Resume → Execution continues (new trace)

In Jaeger, these appear as TWO UNRELATED traces:
- Trace A: "chat.process_request" (stops at checkpoint)
- Trace B: "hitl.resume" (starts fresh, no connection to Trace A)
```

### The Solution: Linked Spans with Baggage

TruvaG3 uses **trace links** (not parent-child relationships) to connect resume traces to their original requests. This is the correct semantic model because:

1. Resume operations are causally related but not direct children of the paused operation
2. The original trace may have already ended
3. Resume can happen long after the original request

#### How It Works

**Step 1: Store Trace Context in Checkpoint**

When HITL creates a checkpoint, it automatically stores the current trace context:

```go
// orchestration/hitl_controller.go (lines 797-800)
tc := telemetry.GetTraceContext(ctx)
if tc.TraceID != "" {
    userContext["original_trace_id"] = tc.TraceID
    userContext["original_span_id"] = tc.SpanID
}
```

**Step 2: Create Linked Span on Resume**

When execution resumes, create a linked span that references the original trace:

```go
// examples/agent-with-human-approval/handlers.go (lines 330-358)
// Extract trace context from checkpoint
originalTraceID := ""
originalSpanID := ""
originalRequestID := checkpoint.RequestID

if checkpoint.UserContext != nil {
    if tid, ok := checkpoint.UserContext["original_trace_id"].(string); ok {
        originalTraceID = tid
    }
    if sid, ok := checkpoint.UserContext["original_span_id"].(string); ok {
        originalSpanID = sid
    }
}

// Create linked span (NOT a child span)
ctx, endLinkedSpan := telemetry.StartLinkedSpan(
    ctx,
    "hitl.resume",
    originalTraceID,
    originalSpanID,
    map[string]string{
        "checkpoint_id":       checkpointID,
        "request_id":          checkpoint.RequestID,
        "original_request_id": originalRequestID,
        "link.type":           "hitl_resume",
    },
)
defer endLinkedSpan()
```

**Step 3: Propagate Original Request ID**

Use W3C Baggage to propagate the original request ID through all downstream spans:

```go
// Propagate original_request_id through all downstream operations
ctx = telemetry.WithBaggage(ctx, "original_request_id", originalRequestID)
```

This ensures that every span created during the resumed execution has access to `original_request_id`, making correlation searches possible.

### Frontend Integration for Trace Correlation

The frontend should send the original request ID on resume calls:

```javascript
// examples/chat-ui/hitl.html
// Store original request_id from first checkpoint
if (data.request_id && !originalRequestId) {
    originalRequestId = data.request_id;
}

// Send on all resume requests
const headers = {
    'Content-Type': 'application/json',
    'Accept': 'text/event-stream'
};
if (originalRequestId) {
    headers['X-Truvag3-Original-Request-ID'] = originalRequestId;
}

fetch(`${backendUrl}/hitl/resume/${checkpointId}`, {
    method: 'POST',
    headers: headers
});
```

### Understanding Trace Links vs Parent-Child

In Jaeger, you'll see two types of trace relationships:

| Relationship | Visual | When to Use |
|--------------|--------|-------------|
| **Parent-Child** | Nested spans under parent | Synchronous operations within the same request |
| **Trace Link** | "References" section in span details | Async resumption, cross-request correlation |

```
Jaeger View:
┌─────────────────────────────────────────────────────────────────┐
│ Trace: 7a8b9c0d1e2f (hitl.resume - 2.5s)                       │
│                                                                 │
│ References: (click to view linked trace)                        │
│   └─ FOLLOWS_FROM: 1a2b3c4d5e6f (chat.process_request)         │
│                                                                 │
│ Tags:                                                           │
│   checkpoint_id: cp-abc123                                      │
│   original_request_id: req-xyz789                               │
│   link.type: hitl_resume                                        │
└─────────────────────────────────────────────────────────────────┘
```

### Finding HITL Traces in Jaeger

**Method 1: Search by Original Request ID**
```
Service: agent-with-human-approval
Tags: original_request_id=req-xyz789
```

**Method 2: Search by Checkpoint ID**
```
Service: agent-with-human-approval
Tags: checkpoint_id=cp-abc123
```

**Method 3: Follow Links from Original Trace**
1. Find the original trace that created the checkpoint
2. Look for spans with `hitl_checkpoint_created` events
3. Note the `checkpoint_id`
4. Search for traces with that `checkpoint_id` tag

### Complete HITL Tracing Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                     HITL Cross-Trace Correlation                 │
└─────────────────────────────────────────────────────────────────┘

TRACE A: Original Request (trace_id: aaa111)
├── chat.process_request (span_id: bbb222)
│   ├── orchestrator.process_request
│   │   ├── orchestrator.generate_plan
│   │   └── [HITL checkpoint created]
│   │       Event: hitl_checkpoint_created
│   │       Tags: checkpoint_id=cp-abc123
│   └── [Trace ends - waiting for approval]
│
│       [Human reviews and approves - minutes/hours later]
│
TRACE B: Resume Request (trace_id: ccc333)
├── hitl.resume (span_id: ddd444)
│   │   References: FOLLOWS_FROM trace_id=aaa111, span_id=bbb222
│   │   Tags: original_request_id=req-xyz789
│   │         checkpoint_id=cp-abc123
│   │         link.type=hitl_resume
│   │   Baggage: original_request_id=req-xyz789
│   │
│   ├── orchestrator.process_request  ← Baggage propagated
│   │   ├── HTTP POST → weather-tool  ← Baggage propagated
│   │   └── orchestrator.synthesize
│   └── [Response returned]
└─────────────────────────────────────────────────────────────────┘

In Jaeger:
- Find Trace A by request_id or time
- See "hitl_checkpoint_created" event with checkpoint_id
- Search for Trace B using checkpoint_id or original_request_id
- Click "References" in Trace B to jump to Trace A
```

### Key Functions for HITL Tracing

| Function | Purpose | Location |
|----------|---------|----------|
| `telemetry.StartLinkedSpan` | Create span with trace link to another trace | `telemetry/async_span.go:84` |
| `telemetry.WithBaggage` | Propagate key-value through all downstream spans | `telemetry/context.go:76` |
| `telemetry.GetTraceContext` | Extract current trace/span IDs for storage | `telemetry/trace_context.go:78` |

### Best Practices for HITL Tracing

1. **Always store trace context in checkpoints** - The controller does this automatically via `userContext`

2. **Use linked spans, not child spans** - Resume operations are semantically "follows from", not "child of"

3. **Propagate original_request_id via baggage** - This enables searching by the user-visible request ID

4. **Include checkpoint_id in all HITL spans** - Essential for correlating approval commands with execution

5. **Log cross-trace correlation in span events** - Add events when creating links for debugging:
   ```go
   import "go.opentelemetry.io/otel/attribute"

   telemetry.AddSpanEvent(ctx, "hitl.trace_link_created",
       attribute.String("original_trace_id", originalTraceID),
       attribute.String("checkpoint_id", checkpointID),
   )
   ```

---

## 17. AI Module Distributed Tracing

In addition to the orchestration module's LLM telemetry (which captures prompt/response events), the **AI module itself** emits distributed tracing spans for each AI request. These spans give you visibility into the actual AI API calls, including token usage, retry behavior, and HTTP-level details.

### Critical: Initialization Order

**The most common issue with AI tracing is initialization order.** The telemetry module MUST be initialized BEFORE creating the AI client.

```go
func main() {
    // ✅ CORRECT ORDER

    // 1. Set component type for service_type labeling
    core.SetCurrentComponentType(core.ComponentTypeAgent)

    // 2. Initialize telemetry BEFORE creating agent/AI client
    initTelemetry("my-agent")
    defer telemetry.Shutdown(context.Background())

    // 3. Create agent/AI client AFTER telemetry is initialized
    // The AI client will now receive the telemetry provider
    agent, err := NewMyAgent()  // Internally uses ai.WithTelemetry(telemetry.GetTelemetryProvider())
}
```

If you create the AI client before telemetry is initialized, `telemetry.GetTelemetryProvider()` returns `nil` and no AI spans will be captured.

### AI Spans Captured

When properly configured, the AI module emits these spans:

| Span Name | Description | Key Attributes |
|-----------|-------------|----------------|
| `ai.generate` | Logical normalized generation | `ai.provider`, `ai.model`, `ai.surface`, `ai.purpose`, token usage, policy adjustments |
| `ai.stream` | Logical normalized streaming call | Same normalized identity, usage, and policy attributes as `ai.generate` |
| `ai.generate_response` / `ai.stream_response` | Provider-local preparation and execution | Semantic provider/model, optional sanitized route identity, and provider-specific execution attributes |
| `ai.get_embeddings` | Bedrock Titan embedding operation | `ai.provider`, stable Titan semantic family, `ai.text_length`, embedding dimensions, bounded error classification |
| `ai.invoke_model` | Direct Bedrock model invocation; a child of `ai.get_embeddings` for Titan embeddings | `ai.provider`, `ai.surface`, request/response lengths, bounded error classification; raw SDK model/profile IDs are omitted |
| `ai.request.prepared` (event) | Sanitized orchestration request report | Provider/surface, purpose, requested/resolved model, adjustment count, stability, and stable policy fingerprint |
| `ai.http_attempt` | Each HTTP attempt (including retries) | `ai.attempt`, `ai.max_retries`, `ai.is_retry`, `ai.attempt_status`, `ai.attempt_duration_ms`, `http.status_code` |

Every operation span that returns an error is marked failed, including both an
outer embedding span and its invocation child when the invocation fails. This
is expected nested observability rather than duplicate logical
instrumentation. Provider-boundary exception messages are sanitized; the
original error remains available to the caller through `errors.Is` and
`errors.As`.

Provider-local spans distinguish the semantic model from a route-owned wire
deployment. A stable application-sanitized route identity may be attached as
`ai.request.route_identity`; raw deployment names, publisher-model IDs,
inference-profile IDs/ARNs, endpoint URLs, query values, and credential scopes
must not be recorded. Bedrock's direct `InvokeModel` surface therefore omits
`ai.model`, while the Titan helper records only its stable semantic family.

The AI layer reports provider token usage but does not derive or emit an
`ai.cost_usd` value. Provider prices, discounts, cached-token rules, and billing
semantics change independently of the framework, so an inferred currency value
would not be authoritative. Join token usage with the provider's billing export
when accurate cost reporting is required.

### Enabling AI Telemetry in Your Agent

When creating an AI client, pass the telemetry provider:

```go
import (
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/telemetry"
)

func NewMyAgent() (*MyAgent, error) {
    // Get telemetry provider (must be initialized first!)
    aiClient, err := ai.NewClient(
        ai.WithTelemetry(telemetry.GetTelemetryProvider()),
    )
    if err != nil {
        return nil, err
    }

    // Use the AI client in your agent
    return &MyAgent{aiClient: aiClient}, nil
}
```

`ai.NewClient` and `ai.NewRequestClient` always return the common instrumented
wrapper, even when telemetry is nil. Depend on `core.AIClient` and optional
capability interfaces rather than asserting the result to a concrete provider
client type. If an application wraps that factory-managed client with
`ai.NewInstrumentedClient` to add debug recording, the constructor collapses
the internal wrapper so one call does not create duplicate common spans.

Failover chains may still show nested logical AI spans because the chain and
each attempted entry are independently observable. That is intentional: the
outer operation represents the logical call, while child operations show which
entries were attempted.

### What You'll See in Jaeger

When you expand a trace containing AI operations, you'll see:

```
travel-research-orchestration: HTTP POST /orchestrate/natural (15.87s)
└── orchestrator.process_request (15.87s)
    ├── orchestrator.build_prompt (1.4ms)
    ├── ai.generate (2.34s)                             ← logical normalized call
    │   └── ai.generate_response (2.34s)                ← provider execution
    │       └── ai.http_attempt (2.33s)                 ← HTTP attempt/retry
    ├── HTTP POST → geocoding-tool (594ms)
    ├── HTTP POST → weather-tool-v2 (610ms)
    └── ai.generate (1.89s)
        └── ai.generate_response (1.89s)
            └── ai.http_attempt (1.88s)
```

### Troubleshooting: AI Spans Not Appearing

If you don't see `ai.generate`, provider execution, or `ai.http_attempt` spans:

1. **Check initialization order**: Telemetry MUST be initialized before creating the AI client
2. **Verify telemetry is enabled**: Check your logs for "Telemetry initialized successfully"
3. **Confirm AI client has telemetry**: Ensure you pass `ai.WithTelemetry(telemetry.GetTelemetryProvider())`
4. **Check sampling rate**: In production profile (0.1% sampling), most traces won't be captured

### Framework-Driven Logger Propagation

**Important:** The TruvaG3 Framework automatically propagates the logger to the AI client when you register components. You don't need to manually call `ai.WithLogger()` - the Framework handles this during component registration in `core.NewFramework()`.

**How It Works:**

The Framework's `applyConfigToComponent()` function automatically:
1. Detects if the agent has an AI client (via the `AI` field on `BaseAgent`)
2. Checks if the AI client implements `SetLogger(Logger)`
3. Propagates the production logger to the AI client
4. The AI client wraps the logger with the `"framework/ai"` component prefix

**Root Cause of Silent AI Logs:**

If AI logs were silent (no output despite AI requests working), the cause is typically:
- AI client was created **before** telemetry was initialized
- The Framework hadn't yet propagated the production logger to the AI client
- AI client was still using the default `NoOpLogger`

**The Fix:**
Ensure telemetry is initialized BEFORE creating your agent/AI client (as shown in the initialization order above). The Framework will then automatically propagate the production logger.

**Example AI Module Log (after fix):**
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

### Working Examples

See these examples for production-ready AI telemetry patterns:

- `examples/agent-with-orchestration/` - Full orchestration with AI telemetry
- `examples/agent-with-telemetry/` - Agent with comprehensive telemetry

---

## Summary

Distributed tracing transforms debugging from guesswork into science. Here's what you've learned:

1. **The Problem:** Without tracing, logs from different services have no common identifier
2. **The Solution:** Trace IDs propagate through HTTP headers (W3C TraceContext)
3. **Server-Side:** `TracingMiddleware` extracts/creates traces for incoming requests
4. **Client-Side:** `TracedHTTPClient` propagates traces to downstream services
5. **Log Correlation:** Extract trace IDs from context to include in your logs
6. **Infrastructure:** OTEL Collector + Jaeger + Grafana for collection and visualization

**Remember:** Tracing is like having GPS for your requests. You always know where they are, where they've been, and why they're stuck in traffic!

---

## Related Documentation

- [Telemetry Module README](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md) - Metrics and configuration
- [Core Module README](https://github.com/truvaagents/truva-g3/blob/main/core/README.md) - Framework fundamentals
- [API Reference - Tracing Section](../reference/API_REFERENCE.md#distributed-tracing) - API details
- [Examples](https://github.com/truvaagents/truva-g3/tree/main/examples) - Working code samples

Happy tracing!
