# Logging Implementation Guide

Welcome to the TruvaG3 logging guide! This document explains how to implement consistent, production-ready logging across your tools and agents. Think of this as your complete reference for doing logging the right way.

## Table of Contents

1. [Why This Guide Exists](#1-why-this-guide-exists)
2. [The Logger Interface](#2-the-logger-interface)
3. [Log Levels Explained](#3-log-levels-explained)
4. [Environment Configuration](#4-environment-configuration)
5. [Where to Use Each Logger Method](#5-where-to-use-each-logger-method)
6. [Agent Logging: Complete Example](#6-agent-logging-complete-example)
7. [Tool Logging: Complete Example](#7-tool-logging-complete-example)
8. [Handler Logging with Trace Correlation](#8-handler-logging-with-trace-correlation)
9. [HITL (Human-in-the-Loop) Request Tracing](#9-hitl-human-in-the-loop-request-tracing)
10. [Structured Logging: Field Naming Standards](#10-structured-logging-field-naming-standards)
11. [Required Patterns for Framework-Level Logging](#11-required-patterns-for-framework-level-logging)
12. [The Mixed Logging Problem](#12-the-mixed-logging-problem)
13. [Telemetry Integration](#13-telemetry-integration)
14. [Component-Aware Logging for Framework Modules](#14-component-aware-logging-for-framework-modules)
15. [Common Mistakes and How to Avoid Them](#15-common-mistakes-and-how-to-avoid-them)
16. [Quick Reference](#16-quick-reference)
17. [Manual Trace ID Extraction](#17-manual-trace-id-extraction)

---

## 1. Why This Guide Exists

In a distributed system with multiple agents and tools, logs are your primary debugging tool. Without consistent logging:

- You can't correlate requests across services
- You can't filter logs effectively in production
- You waste hours debugging issues that should take minutes

This guide ensures every TruvaG3 component logs in a consistent, useful way.

---

## 2. The Logger Interface

TruvaG3 uses a custom `Logger` interface defined in [`core/interfaces.go`](../../core/interfaces.go) (search for `type Logger interface`). This design:

- **Avoids vendor lock-in** (not tied to zap, logrus, zerolog, etc.)
- **Is minimal and composable** (easy to test and mock)
- **Supports trace correlation** (via context-aware methods)

### The Interface Definition

```go
// From core/interfaces.go
type Logger interface {
    // Basic logging methods (no trace correlation)
    Info(msg string, fields map[string]interface{})
    Error(msg string, fields map[string]interface{})
    Warn(msg string, fields map[string]interface{})
    Debug(msg string, fields map[string]interface{})

    // Context-aware methods (with trace correlation)
    InfoWithContext(ctx context.Context, msg string, fields map[string]interface{})
    ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{})
    WarnWithContext(ctx context.Context, msg string, fields map[string]interface{})
    DebugWithContext(ctx context.Context, msg string, fields map[string]interface{})
}
```

### Why Two Sets of Methods?

| Method Type | When to Use | Example Location |
|-------------|-------------|------------------|
| Basic (no context) | Startup, shutdown, background tasks | `main()`, init functions |
| WithContext | HTTP handlers, any request processing | Handler functions |

**The golden rule**: If you have access to `context.Context` from an HTTP request, use the `WithContext` methods. They enable trace-log correlation, which is essential for debugging in production.

### Default Logger Behavior

When you create a component with `core.NewBaseAgent()` or `core.NewTool()`, the Logger is initially set to `NoOpLogger` (a silent logger defined in [`core/interfaces.go`](../../core/interfaces.go) — search for `type NoOpLogger`). The framework replaces this with a `ProductionLogger` when you call `core.NewFramework()`.

---

## 3. Log Levels Explained

TruvaG3 uses four standard log levels, from most to least verbose:

| Level | When to Use | Example |
|-------|-------------|---------|
| **DEBUG** | Detailed flow information for troubleshooting | "Executing step 3 of workflow" |
| **INFO** | Significant events, lifecycle changes | "Request completed successfully" |
| **WARN** | Unexpected but recoverable situations | "Retrying request (attempt 2/3)" |
| **ERROR** | Failures that need attention | "Failed to connect to database" |

### Level Hierarchy

```
DEBUG (0) → INFO (1) → WARN (2) → ERROR (3)
```

> **Source**: [`core/config.go`](../../core/config.go) (`LogLevel` constants)

When you set `TRUVAG3_LOG_LEVEL=INFO`, you see INFO, WARN, and ERROR logs. DEBUG logs are hidden.

### Production Recommendations

| Environment | Recommended Level |
|-------------|-------------------|
| Development | DEBUG |
| Staging | INFO |
| Production | INFO (or WARN for high-volume services) |

---

## 4. Environment Configuration

TruvaG3 logging is configured through environment variables:

### Core Environment Variables

| Variable | Values | Default | Description |
|----------|--------|---------|-------------|
| `TRUVAG3_LOG_LEVEL` | debug, info, warn, error | info | Minimum level to log |
| `TRUVAG3_LOG_FORMAT` | json, text | json | Output format |
| `TRUVAG3_DEBUG` | true, false | false | Enable debug mode |

> **Source**: [`core/config.go`](../../core/config.go) (`LoggingConfig struct`)

### Format Behavior

The framework's `ProductionLogger` uses the format from configuration (defaults to JSON).

The telemetry module's `TelemetryLogger` has additional auto-detection logic:

```go
// From telemetry/logger.go
if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
    format = "json" // Use JSON in K8s for log aggregation
}
```

**Recommendation**: For consistency, explicitly set `TRUVAG3_LOG_FORMAT`:
- **Production/Kubernetes**: `json` (for log aggregation tools like Loki, ELK)
- **Local development**: `text` (human-readable)

### Output Format Examples

**Text format (local development):**
```
2024-01-15T10:30:45Z [INFO] [weather-service] Processing weather request lat=35.67 lon=139.65
```

**JSON format (production/K8s):**
```json
{
  "timestamp": "2024-01-15T10:30:45Z",
  "level": "INFO",
  "service": "weather-service",
  "component": "framework",
  "message": "Processing weather request",
  "lat": 35.67,
  "lon": 139.65
}
```

---

## 5. Where to Use Each Logger Method

This is the most important section. Understanding when to use each method prevents the inconsistencies that make debugging difficult.

### Decision Tree

```
Are you in a function that received context.Context from an HTTP request?
├── YES → Use WithContext methods
│         t.Logger.InfoWithContext(ctx, "message", fields)
│
└── NO → Use basic methods
         t.Logger.Info("message", fields)
```

### Specific Locations

| Location | Method to Use | Why |
|----------|---------------|-----|
| `main()` startup | `Info()` / `Error()` | No request context exists yet |
| `initTelemetry()` | `Info()` / `Error()` | Background initialization |
| HTTP handler | `InfoWithContext()` | Request context available for tracing |
| Background goroutine | `Info()` / `Error()` | No request context |
| Graceful shutdown | `Info()` | No request context |

---

## 6. Agent Logging: Complete Example

Here's a complete, correctly-implemented agent with proper logging at every level.

### main.go (Startup Logging)

```go
package main

import (
    "context"
    "errors"
    "log"  // ONLY for fatal startup errors before framework is ready
    "os"
    "os/signal"
    "strconv"
    "syscall"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

func main() {
    // 1. Validate configuration first (fail fast)
    // Use standard log here because framework isn't created yet
    if err := validateConfig(); err != nil {
        log.Fatalf("Configuration error: %v", err)
    }

    // 2. Initialize telemetry BEFORE creating agent
    initTelemetry("my-research-agent")
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := telemetry.Shutdown(ctx); err != nil {
            // Use standard log because we're shutting down
            log.Printf("Warning: Telemetry shutdown error: %v", err)
        }
    }()

    // 3. Create agent
    agent, err := NewResearchAgent()
    if err != nil {
        log.Fatalf("Failed to create agent: %v", err)
    }

    // 4. Get port configuration
    port := 8080
    if portStr := os.Getenv("PORT"); portStr != "" {
        if p, err := strconv.Atoi(portStr); err == nil {
            port = p
        }
    }

    // 5. Create framework
    framework, err := core.NewFramework(agent,
        core.WithName("my-research-agent"),
        core.WithPort(port),
        core.WithNamespace(os.Getenv("NAMESPACE")),
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),
        core.WithCORS([]string{"*"}, true),
        core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
        core.WithMiddleware(telemetry.TracingMiddleware("my-research-agent")),
    )
    if err != nil {
        log.Fatalf("Failed to create framework: %v", err)
    }

    // 6. Log startup information using the agent's Logger
    // At this point, framework has configured the agent's Logger
    agent.Logger.Info("Agent starting", map[string]interface{}{
        "id":           agent.GetID(),
        "name":         agent.GetName(),
        "port":         port,
        "capabilities": len(agent.Capabilities),
    })

    // 7. Set up graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        agent.Logger.Info("Shutting down gracefully", nil)
        cancel()
    }()

    // 8. Run the framework
    if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
        agent.Logger.Error("Framework error", map[string]interface{}{
            "error": err.Error(),
        })
        os.Exit(1)
    }

    agent.Logger.Info("Shutdown completed", nil)
}
```

### research_agent.go (Agent Definition)

```go
package main

import (
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
)

type ResearchAgent struct {
    *core.BaseAgent
}

func NewResearchAgent() (*ResearchAgent, error) {
    agent := core.NewBaseAgent("my-research-agent")

    // Auto-configure AI client
    aiClient, err := ai.NewClient()
    if err != nil {
        // Log warning but continue - AI is optional
        agent.Logger.Warn("AI client creation failed, some features limited", map[string]interface{}{
            "error": err.Error(),
        })
    } else {
        agent.AI = aiClient
        agent.Logger.Info("AI client configured", map[string]interface{}{
            "provider": "auto-detected",
        })
    }

    researchAgent := &ResearchAgent{
        BaseAgent: agent,
    }

    // Register capabilities
    researchAgent.registerCapabilities()

    agent.Logger.Info("Research agent created", map[string]interface{}{
        "capabilities": len(agent.Capabilities),
    })

    return researchAgent, nil
}

func (r *ResearchAgent) registerCapabilities() {
    r.RegisterCapability(core.Capability{
        Name:        "research_topic",
        Description: "Research a topic using available tools",
        Endpoint:    "/api/capabilities/research_topic",
        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler:     r.handleResearchTopic,
    })

    r.Logger.Debug("Registered capability", map[string]interface{}{
        "name":     "research_topic",
        "endpoint": "/api/capabilities/research_topic",
    })
}
```

### handlers.go (Request Handlers - WITH Context)

```go
package main

import (
    "context"
    "encoding/json"
    "net/http"
    "time"
)

type ResearchRequest struct {
    Topic string `json:"topic"`
}

type ResearchResponse struct {
    Topic     string      `json:"topic"`
    Results   interface{} `json:"results"`
    Duration  string      `json:"duration"`
    RequestID string      `json:"request_id,omitempty"`
}

// handleResearchTopic processes research requests
// IMPORTANT: Always use WithContext methods in handlers!
func (r *ResearchAgent) handleResearchTopic(w http.ResponseWriter, req *http.Request) {
    startTime := time.Now()
    ctx := req.Context()  // Get context from request

    // Log request start WITH CONTEXT (enables trace correlation)
    r.Logger.InfoWithContext(ctx, "Processing research request", map[string]interface{}{
        "method": req.Method,
        "path":   req.URL.Path,
    })

    // Parse request
    var request ResearchRequest
    if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
        r.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
            "error": err.Error(),
        })
        http.Error(w, "Invalid request format", http.StatusBadRequest)
        return
    }

    // Validate request
    if request.Topic == "" {
        r.Logger.WarnWithContext(ctx, "Empty topic in request", nil)
        http.Error(w, "Topic is required", http.StatusBadRequest)
        return
    }

    r.Logger.DebugWithContext(ctx, "Request validated", map[string]interface{}{
        "topic": request.Topic,
    })

    // Perform research (your business logic here)
    results, err := r.performResearch(ctx, request.Topic)
    if err != nil {
        r.Logger.ErrorWithContext(ctx, "Research failed", map[string]interface{}{
            "topic": request.Topic,
            "error": err.Error(),
        })
        http.Error(w, "Research failed", http.StatusInternalServerError)
        return
    }

    // Build response
    response := ResearchResponse{
        Topic:    request.Topic,
        Results:  results,
        Duration: time.Since(startTime).String(),
    }

    // Log successful completion WITH CONTEXT
    r.Logger.InfoWithContext(ctx, "Research completed", map[string]interface{}{
        "topic":       request.Topic,
        "duration_ms": time.Since(startTime).Milliseconds(),
        "status":      "success",
    })

    // Send response
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}

func (r *ResearchAgent) performResearch(ctx context.Context, topic string) (interface{}, error) {
    // Log internal operations with context for tracing
    r.Logger.DebugWithContext(ctx, "Starting tool discovery", map[string]interface{}{
        "topic": topic,
    })

    // ... your research logic here ...

    return nil, nil
}
```

---

## 7. Tool Logging: Complete Example

Tools follow the same patterns as agents. Here's a weather tool example:

### main.go

```go
package main

import (
    "context"
    "errors"
    "log"
    "os"
    "os/signal"
    "strconv"
    "syscall"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

func main() {
    if err := validateConfig(); err != nil {
        log.Fatalf("Configuration error: %v", err)
    }

    initTelemetry("weather-tool")
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        telemetry.Shutdown(ctx)
    }()

    tool := NewWeatherTool()

    port := 8080
    if portStr := os.Getenv("PORT"); portStr != "" {
        if p, err := strconv.Atoi(portStr); err == nil {
            port = p
        }
    }

    framework, err := core.NewFramework(tool,
        core.WithName("weather-tool"),
        core.WithPort(port),
        core.WithNamespace(os.Getenv("NAMESPACE")),
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),
        core.WithCORS([]string{"*"}, true),
        core.WithMiddleware(telemetry.TracingMiddleware("weather-tool")),
    )
    if err != nil {
        log.Fatalf("Failed to create framework: %v", err)
    }

    // Use tool's Logger after framework is created
    tool.Logger.Info("Weather tool starting", map[string]interface{}{
        "port":         port,
        "capabilities": len(tool.Capabilities),
    })

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        tool.Logger.Info("Shutting down", nil)
        cancel()
    }()

    if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
        tool.Logger.Error("Framework error", map[string]interface{}{
            "error": err.Error(),
        })
    }
}
```

### weather_tool.go

```go
package main

import (
    "github.com/truvaagents/truva-g3/core"
)

type WeatherTool struct {
    *core.BaseTool
}

func NewWeatherTool() *WeatherTool {
    tool := core.NewTool("weather-tool")

    weatherTool := &WeatherTool{
        BaseTool: tool,
    }

    weatherTool.registerCapabilities()

    return weatherTool
}

func (w *WeatherTool) registerCapabilities() {
    w.RegisterCapability(core.Capability{
        Name:        "get_weather",
        Description: "Get current weather for coordinates",
        Endpoint:    "/api/capabilities/get_weather",
        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler:     w.handleGetWeather,
        InputSummary: &core.SchemaSummary{
            RequiredFields: []core.FieldHint{
                {Name: "lat", Type: "number", Example: "35.6762", Description: "Latitude"},
                {Name: "lon", Type: "number", Example: "139.6503", Description: "Longitude"},
            },
        },
    })
}
```

### handlers.go

```go
package main

import (
    "encoding/json"
    "net/http"
    "time"
)

type WeatherRequest struct {
    Lat float64 `json:"lat"`
    Lon float64 `json:"lon"`
}

type WeatherResponse struct {
    Temperature float64 `json:"temperature"`
    Condition   string  `json:"condition"`
    Location    string  `json:"location"`
}

// handleGetWeather processes weather requests
func (w *WeatherTool) handleGetWeather(rw http.ResponseWriter, req *http.Request) {
    startTime := time.Now()
    ctx := req.Context()

    // Always use WithContext in handlers
    w.Logger.InfoWithContext(ctx, "Processing weather request", map[string]interface{}{
        "method": req.Method,
        "path":   req.URL.Path,
    })

    var request WeatherRequest
    if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
        w.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
            "error": err.Error(),
        })
        http.Error(rw, "Invalid request", http.StatusBadRequest)
        return
    }

    w.Logger.DebugWithContext(ctx, "Fetching weather data", map[string]interface{}{
        "lat": request.Lat,
        "lon": request.Lon,
    })

    // Fetch weather data (your implementation)
    response := WeatherResponse{
        Temperature: 22.5,
        Condition:   "Sunny",
        Location:    "Tokyo, Japan",
    }

    w.Logger.InfoWithContext(ctx, "Weather request completed", map[string]interface{}{
        "lat":         request.Lat,
        "lon":         request.Lon,
        "duration_ms": time.Since(startTime).Milliseconds(),
    })

    rw.Header().Set("Content-Type", "application/json")
    json.NewEncoder(rw).Encode(response)
}
```

---

## 8. Handler Logging with Trace Correlation

The `WithContext` methods enable trace-log correlation. Here's how it works:

### How Trace Correlation Works

1. **TracingMiddleware** extracts/creates trace context from incoming requests
2. **Context** carries the trace ID and span ID through your code
3. **WithContext methods** extract and include these IDs in log output

> **Source**: Trace context extraction is handled by [`telemetry/trace_context.go`](../../telemetry/trace_context.go) and [`telemetry/framework_integration.go`](../../telemetry/framework_integration.go)

### What Your Logs Look Like

**Without trace correlation (bad):**
```
10:00:00 [INFO] [weather-service] Processing weather request
10:00:00 [INFO] [weather-service] Processing weather request  <- Which is which?
10:00:01 [ERROR] [weather-service] Request failed             <- Which request?
```

**With trace correlation (good):**
```
10:00:00 [INFO] [weather-service] [req=abc123] Processing weather request
10:00:00 [INFO] [weather-service] [req=def456] Processing weather request
10:00:01 [ERROR] [weather-service] [req=abc123] Request failed  <- Clear!
```

### JSON Output with Trace Context

When using JSON format (production), trace context appears as **top-level fields** per the [OpenTelemetry specification](https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/compatibility/logging_trace_context.md):

```json
{
  "timestamp": "2024-01-15T10:00:00Z",
  "level": "INFO",
  "service": "weather-tool",
  "component": "tool/weather",
  "message": "Processing weather request",
  "trace_id": "abc123def456789012345678901234",
  "span_id": "1234567890abcdef",
  "lat": 35.67,
  "lon": 139.65
}
```

> **Design Principle**: TruvaG3 uses standard OpenTelemetry field names (`trace_id`, `span_id`) at the root level for vendor-agnostic compatibility with any OTel-compliant observability backend (SigNoz, Grafana Loki, Datadog, Elastic, etc.).

---

## 9. HITL (Human-in-the-Loop) Request Tracing

HITL workflows present a unique logging challenge: a single user conversation may span **multiple HTTP requests** with **different `request_id` values**. This section explains how to correlate logs across the entire HITL conversation.

### The Challenge: Multiple Requests, One Conversation

```
┌─────────────────────────────────────────────────────────────────────────┐
│ HITL CONVERSATION FLOW                                                   │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Request 1 (Parent)         Request 2 (Child/Resume)                    │
│  ─────────────────          ──────────────────────                      │
│  request_id: "req-abc123"   request_id: "req-def456"  ← DIFFERENT!     │
│  trace_id: "trace-111"      trace_id: "trace-222"     ← DIFFERENT!     │
│                                                                          │
│  [User sends query]         [After human approval]                       │
│       ↓                           ↓                                      │
│  [Plan generated]           [Resume from checkpoint]                     │
│       ↓                           ↓                                      │
│  [HITL interrupt]           [Plan executes]                              │
│       ↓                           ↓                                      │
│  [Checkpoint created]       [Result returned]                            │
│       ↓                                                                  │
│  [ErrInterrupted returned]                                               │
│                                                                          │
│  HOW DO WE CORRELATE THESE TWO REQUESTS?                                │
│  Answer: original_request_id = "req-abc123" (same for both!)            │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

### The Solution: `original_request_id`

TruvaG3 uses `original_request_id` to link all requests in a HITL conversation:

| Field | Initial Request | Resume Request | Purpose |
|-------|-----------------|----------------|---------|
| `request_id` | `req-abc123` | `req-def456` | Unique per HTTP request |
| `original_request_id` | `req-abc123` | `req-abc123` | Same across conversation |
| `trace_id` | `trace-111` | `trace-222` | Unique per distributed trace |
| `checkpoint_id` | `cp-xyz789` | `cp-xyz789` | Links interrupt to resume |

**Key insight**: `original_request_id` is the **correlation key** for the entire HITL conversation.

### How `original_request_id` Flows Through the System

#### 1. Initial Request (Parent)

```go
// orchestrator.go - ProcessRequest
requestID := generateRequestID()  // e.g., "req-abc123"

// For initial requests, original_request_id == request_id
ctx = telemetry.WithBaggage(ctx, "request_id", requestID)
// original_request_id defaults to request_id when not set in baggage
```

When a HITL checkpoint is created, `hitl_controller.go` captures:

```go
// hitl_controller.go - createCheckpoint
originalRequestID := requestID  // Default to current request_id
if bag := telemetry.GetBaggage(ctx); bag != nil {
    if origID := bag["original_request_id"]; origID != "" {
        originalRequestID = origID  // Use preserved original for resume
    }
}

checkpoint := &ExecutionCheckpoint{
    RequestID:         requestID,          // "req-abc123"
    OriginalRequestID: originalRequestID,  // "req-abc123" (same for initial)
    // ...
}
```

#### 2. Resume Request (Child)

When the agent resumes from a checkpoint, it **must** set `original_request_id` in baggage. The reference implementation in `agent-with-human-approval` uses this priority logic:

```go
// handlers.go - handleResumeSSE (from agent-with-human-approval)
checkpoint, _ := checkpointStore.LoadCheckpoint(ctx, checkpointID)

// Determine original_request_id for trace correlation across HITL conversation
// Priority: 1) Header from UI, 2) Checkpoint's OriginalRequestID, 3) Checkpoint's RequestID (fallback)
// NOTE: For step checkpoints, RequestID is the resume request's ID, not the original.
// OriginalRequestID preserves the first request's ID across the entire HITL conversation.
originalRequestID := originalRequestIDFromHeader  // From X-Original-Request-ID header
if originalRequestID == "" {
    if checkpoint.OriginalRequestID != "" {
        originalRequestID = checkpoint.OriginalRequestID
    } else {
        originalRequestID = checkpoint.RequestID  // Fallback for legacy checkpoints
    }
}

// Log the trace correlation setup
t.Logger.InfoWithContext(ctx, "Trace correlation IDs determined", map[string]interface{}{
    "operation":                       "hitl_resume_trace_setup",
    "checkpoint_id":                   checkpointID,
    "checkpoint_request_id":           checkpoint.RequestID,
    "checkpoint_original_request_id":  checkpoint.OriginalRequestID,
    "original_request_id_used":        originalRequestID,
})

// Set in baggage BEFORE calling orchestrator
ctx = telemetry.WithBaggage(ctx, "original_request_id", originalRequestID)

// Now call orchestrator - it will generate a NEW request_id
// but original_request_id will be preserved via baggage
result, err := orchestrator.ProcessRequestStreaming(ctx, checkpoint.OriginalRequest, ...)
```

#### 3. Execution Storage

The `storeExecutionAsync` helper extracts `original_request_id` from baggage:

```go
// orchestrator.go - storeExecutionAsync
originalRequestID := requestID  // Default
if bag != nil {
    if origID, ok := bag["original_request_id"]; ok && origID != "" {
        originalRequestID = origID  // Links to parent request
    }
}

stored := &StoredExecution{
    RequestID:         requestID,          // "req-def456" (new)
    OriginalRequestID: originalRequestID,  // "req-abc123" (preserved)
    // ...
}
```

### Logging Fields for HITL

When logging in HITL-related code, include these fields:

| Field | Required | Description |
|-------|----------|-------------|
| `request_id` | **YES** | Current request's unique ID |
| `original_request_id` | For HITL | Links to the conversation's first request |
| `checkpoint_id` | For HITL | Links interrupt to resume |
| `interrupted` | For HITL | Boolean indicating if this request was interrupted |

**Example: HITL checkpoint creation log** (from orchestrator.go)

```go
if o.logger != nil {
    o.logger.InfoWithContext(ctx, "Plan execution interrupted for human approval", map[string]interface{}{
        "operation":     "hitl_plan_approval",
        "request_id":    requestID,
        "plan_id":       plan.PlanID,
        "checkpoint_id": checkpoint.CheckpointID,
    })
}
```

> **Note**: The `original_request_id` is captured in the `ExecutionCheckpoint` struct and persisted to the checkpoint store. The checkpoint's `OriginalRequestID` field is then used during resume to correlate the conversation.

**Example: Resume execution log**

```go
if a.Logger != nil {
    a.Logger.InfoWithContext(ctx, "Resuming execution from checkpoint", map[string]interface{}{
        "operation":           "hitl_resume",
        "request_id":          newRequestID,        // New request_id
        "original_request_id": originalRequestID,  // Preserved from parent
        "checkpoint_id":       checkpoint.CheckpointID,
        "checkpoint_status":   checkpoint.Status,
    })
}
```

### Filtering Logs by `original_request_id`

To see the **entire HITL conversation** across all requests:

#### JSON Format (with jq)

```bash
# Find all logs for a HITL conversation
kubectl logs -n truvag3-examples -l app=agent-with-human-approval | \
  jq 'select(.original_request_id == "req-abc123" or .request_id == "req-abc123")'

# Show the conversation timeline
kubectl logs -n truvag3-examples -l app=agent-with-human-approval | \
  jq 'select(.original_request_id == "req-abc123")' | \
  jq -s 'sort_by(.timestamp) | .[] | {timestamp, operation, request_id, message}'
```

#### Grafana Loki (LogQL)

```logql
# All logs in a HITL conversation
{namespace="truvag3-examples", app="agent-with-human-approval"}
  | json
  | original_request_id="req-abc123"

# HITL interrupts only
{namespace="truvag3-examples"}
  | json
  | interrupted="true"

# Correlation: find resume logs for a checkpoint
{namespace="truvag3-examples"}
  | json
  | checkpoint_id="cp-xyz789"
  | operation="hitl_resume"
```

### Example: Complete HITL Conversation Logs

Here's what a full HITL conversation looks like in logs:

```json
// === INITIAL REQUEST (Parent) ===
{
  "timestamp": "2025-01-15T10:00:00Z",
  "level": "INFO",
  "component": "framework/orchestration",
  "message": "Starting request processing",
  "operation": "process_request",
  "request_id": "req-abc123",
  "request_length": 45
}

{
  "timestamp": "2025-01-15T10:00:01Z",
  "level": "INFO",
  "component": "framework/orchestration",
  "message": "Plan generated successfully",
  "operation": "plan_generation",
  "request_id": "req-abc123",
  "plan_id": "plan-001",
  "step_count": 3
}

{
  "timestamp": "2025-01-15T10:00:01Z",
  "level": "INFO",
  "component": "framework/orchestration",
  "message": "Plan execution interrupted for human approval",
  "operation": "hitl_plan_approval",
  "request_id": "req-abc123",
  "plan_id": "plan-001",
  "checkpoint_id": "cp-xyz789"
}

// === HUMAN APPROVES (separate trace) ===
{
  "timestamp": "2025-01-15T10:05:00Z",
  "level": "INFO",
  "component": "agent/agent-with-human-approval",
  "message": "Checkpoint approved by human",
  "operation": "hitl_approve",
  "checkpoint_id": "cp-xyz789",
  "command": "approve"
}

// === RESUME REQUEST (Child) ===
{
  "timestamp": "2025-01-15T10:05:01Z",
  "level": "INFO",
  "component": "agent/agent-with-human-approval",
  "message": "Resuming execution from checkpoint",
  "operation": "hitl_resume",
  "request_id": "req-def456",
  "original_request_id": "req-abc123",
  "checkpoint_id": "cp-xyz789"
}

{
  "timestamp": "2025-01-15T10:05:02Z",
  "level": "INFO",
  "component": "framework/orchestration",
  "message": "Using plan override from checkpoint",
  "operation": "hitl_resume_plan_override",
  "request_id": "req-def456",
  "plan_id": "plan-001",
  "step_count": 3
}

{
  "timestamp": "2025-01-15T10:05:05Z",
  "level": "INFO",
  "component": "framework/orchestration",
  "message": "Plan execution completed",
  "operation": "plan_execution",
  "request_id": "req-def456",
  "plan_id": "plan-001",
  "success": true,
  "duration_ms": 3000
}
```

**Observation**: Notice how `original_request_id: "req-abc123"` appears in both the initial interrupt log and the resume logs, allowing you to trace the entire conversation.

### DAG Visualization and `original_request_id`

The DAG visualization in the Registry Viewer uses `original_request_id` to group related executions:

```go
// ExecutionSummary in redis_execution_store.go
type ExecutionSummary struct {
    RequestID         string    `json:"request_id"`
    OriginalRequestID string    `json:"original_request_id,omitempty"`
    Interrupted       bool      `json:"interrupted,omitempty"`
    // ...
}
```

In the UI, executions with the same `original_request_id` are grouped as parent-child:

```
Execution DAG
├─ req-abc123 (Parent, Interrupted: true)
│  └─ Checkpoint: cp-xyz789 (Plan Approval)
│
└─ req-def456 (Child, OriginalRequestID: req-abc123)
   └─ Steps: weather → currency → response
```

### Background Goroutine Logging for HITL

When storing HITL executions asynchronously, use **non-context logging methods** since the goroutine runs with `context.Background()`:

```go
// orchestrator.go - storeExecutionAsync
// This runs in a goroutine with context.Background()
// so we use o.logger.Warn() NOT o.logger.WarnWithContext()

if storeErr := store.Store(storeCtx, stored); storeErr != nil {
    if o.logger != nil {
        logFields := map[string]interface{}{
            "operation":   "execution_store",
            "request_id":  requestID,
            "interrupted": checkpoint != nil,
            "error":       storeErr.Error(),
        }
        if traceID != "" {
            logFields["trace_id"] = traceID
        }
        if checkpoint != nil && checkpoint.CheckpointID != "" {
            logFields["checkpoint_id"] = checkpoint.CheckpointID
        }
        o.logger.Warn("Failed to store execution for DAG visualization", logFields)
    }
}
```

**Why non-context method?** The parent HTTP request context may be canceled after the handler returns, but we still want the async storage to complete. Using `context.Background()` ensures the operation isn't canceled prematurely.

### HITL Logging Checklist

When implementing HITL-related logging:

- [ ] Include `request_id` in all logs (current request's unique ID)
- [ ] Include `original_request_id` when in a HITL flow (links to conversation start)
- [ ] Include `checkpoint_id` when creating or resuming from checkpoints
- [ ] Include `interrupted: true` when storing interrupted executions
- [ ] Set `original_request_id` in baggage **before** calling orchestrator during resume
- [ ] Use non-context logging methods in background goroutines
- [ ] Log both the interrupt (parent) and resume (child) with matching `original_request_id`

### Reference Implementation: agent-with-human-approval

The [`examples/agent-with-human-approval`](../../examples/agent-with-human-approval) directory contains the reference implementation for HITL logging patterns. Key files to study:

| File | Purpose |
|------|---------|
| [`handlers.go`](../../examples/agent-with-human-approval/handlers.go) | `handleResumeSSE` - Shows complete trace correlation setup with priority-based `original_request_id` resolution |
| [`handlers_auto_resume.go`](../../examples/agent-with-human-approval/handlers_auto_resume.go) | `handleAutoResumeSSE` - Expiry-triggered auto-resume with same trace correlation patterns |
| [`hitl_setup.go`](../../examples/agent-with-human-approval/hitl_setup.go) | HITL controller and checkpoint store setup with expiry callbacks |

**Key patterns demonstrated:**

1. **Trace correlation setup** with `StartLinkedSpan` for Jaeger trace linking
2. **Priority-based `original_request_id`** resolution (header → checkpoint → fallback)
3. **Detailed logging** of trace correlation decisions for debugging
4. **Baggage propagation** before orchestrator calls

To run the example and observe the logging:

```bash
cd examples/agent-with-human-approval
./setup.sh  # Deploys to Kubernetes

# Port forward and test
kubectl port-forward -n truvag3-examples svc/agent-with-human-approval 8352:8352

# Send a request that triggers HITL
curl -X POST http://localhost:8352/api/sse/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "transfer $15000 to savings", "session_id": "test-session"}'

# View logs with HITL correlation
kubectl logs -n truvag3-examples -l app=agent-with-human-approval --since=5m | \
  jq 'select(.operation | startswith("hitl"))'
```

---

## 10. Structured Logging: Field Naming Standards

Consistent field names make log searching and filtering much easier.

### Service Identity Contract (Loki `service_name`)

When your app emits a log line, two fields carry identity:

- **`service`** — the string your application writes in the JSON body. Set at framework
  construction time from `cfg.Name`, which is configured via `core.WithName("…")` in
  your `main.go` (or the `TRUVAG3_AGENT_NAME` env var if you prefer env-based config).
  See [core/config.go](../../core/config.go). Shown in every log line for
  human readability.
- **`service_name`** — an indexed Loki stream label derived from the pod's
  `metadata.labels.app` value by the cluster-wide OTel log collector (via the
  `k8sattributes` processor). This is what you filter on in LogQL:
  `{service_name="your-tool"}`.

**The `service_name` label does not come from your log body.** It comes from the pod
manifest. If the pod `app:` label does not match the string you emit in the `service`
field, a LogQL filter on `service_name` and a grep on the log body's `service` field
will return different sets of records for the same pod.

The convention: **pod `app:` label == `core.WithName(...)` (log body `service` field)
== `OTEL_SERVICE_NAME` (SDK traces/metrics `service.name`)**, all set to the tool/agent
name. `cfg.Telemetry.ServiceName` defaults to `cfg.Name` when unset, so if you don't
set `OTEL_SERVICE_NAME` at all, traces/metrics automatically pick up the framework's
name.

**State of the examples in this repository** (audited post-fix): 9 examples set
`OTEL_SERVICE_NAME` explicitly and aligned to pod `app:`, 23 leave it unset (correctly
fall back to `cfg.Name`), and **12 still drift** — `.env.example` files set
`OTEL_SERVICE_NAME` to a different string (e.g., `stock-market-tool/.env.example` has
`OTEL_SERVICE_NAME=stock-service` while pod `app: stock-tool`). Drift causes split-brain:
logs land under the pod-label identity (thanks to the k8sattributes pipeline), but
traces and metrics land under the `OTEL_SERVICE_NAME` value. Cleanup is tracked
separately from the logs-pipeline fix — new examples you create should follow the
aligned convention from the start. See
[TOOL_DEVELOPMENT_GUIDE.md §8 — Observability Identity](../building/TOOL_DEVELOPMENT_GUIDE.md#8-step-6-add-deployment-files)
for the deployment-side rule.

**Resource attributes Loki indexes as stream labels** (filterable with `{key="value"}`):

| Label | Source | Example |
|-------|--------|---------|
| `service_name` | pod label `app` | `hotel-tool` |
| `k8s_pod_name` | pod metadata | `hotel-tool-7f6fc48496-vhfhh` |
| `k8s_namespace_name` | pod metadata | `truvag3-examples` |
| `k8s_deployment_name` | owner-reference walk (pod → replicaset → deployment) | `hotel-tool` |
| `deployment_environment` | collector `resource` processor | `development` |

**Additional resource attributes attached to each record** (available as structured
metadata — filterable with `| key="value"` pipe syntax, not `{key="value"}`):

| Attribute | Source | Example |
|-----------|--------|---------|
| `k8s_node_name` | pod metadata | `truvag3-demo-kind-control-plane` |

### Standard Field Names

Use these field names across all your services:

| Field Name | Type | Description | Example |
|------------|------|-------------|---------|
| `operation` | string | The operation being performed | "research_topic", "get_weather" |
| `status` | string | Result status | "success", "error", "retry" |
| `error` | string | Error message | "connection refused" |
| `error_type` | string | Error classification for filtering and alerting | "timeout", "validation", "network", "marshal", "stream_write", "index_read", "episodic_read", "episodic_recent_read", "episodic_write", "embedding", "llm_unavailable", "parse_failure", "knowledge_store", "claim", "claim_release", "release", "session_read", "cache_read", "cache_write", "cache_unmarshal", "activity_announce", "activity_discover", "activity_complete", "summarizer_error", "debug_recording", "lock_acquire", "entity_discovery", "count_tokens", "compaction", "watermark_mismatch", "preparation", "runnable_exit", "runnable_drain_timeout" |
| `duration_ms` | number | Operation duration in milliseconds | 125 |
| `method` | string | HTTP method | "GET", "POST" |
| `path` | string | Request path | "/api/capabilities/get_weather" |
| `topic` | string | Research topic | "Tokyo weather" |
| `tool_name` | string | Tool being called | "weather-tool" |
| `capability` | string | Capability being invoked | "get_weather" |

### Good vs Bad Field Names

```go
// BAD - inconsistent naming
logger.Info("Request", map[string]interface{}{
    "time_taken": duration,     // Should be duration_ms
    "err": err.Error(),         // Should be error
    "api": "weather",           // Vague
})

// GOOD - consistent naming
logger.Info("Request completed", map[string]interface{}{
    "duration_ms": duration.Milliseconds(),
    "error":       err.Error(),
    "tool_name":   "weather-tool",
    "capability":  "get_weather",
})
```

---

## 11. Required Patterns for Framework-Level Logging

This section documents **required patterns** that MUST be followed when implementing logging in TruvaG3 framework modules. These patterns are used throughout [orchestrator.go](../../orchestration/orchestrator.go) and [executor.go](../../orchestration/executor.go).

### Pattern 1: Logger Nil Check (REQUIRED)

**Always check for nil before calling any logger method.** This is non-negotiable for framework code.

```go
// From orchestration/orchestrator.go
if o.logger != nil {
    o.logger.InfoWithContext(ctx, "Starting request processing", map[string]interface{}{
        "operation":      "process_request",
        "request_id":     requestID,
        "request_length": len(request),
    })
}

// From orchestration/executor.go
if e.logger != nil {
    e.logger.ErrorWithContext(ctx, "Agent not found in catalog", map[string]interface{}{
        "operation":  "agent_discovery",
        "step_id":    step.StepID,
        "agent_name": step.AgentName,
    })
}
```

**Why this is required:**
- Components may be instantiated without a logger
- Prevents nil pointer panics in production
- Framework design allows optional logging
- Enables graceful degradation

### Pattern 2: Operation Field (REQUIRED)

**Every log entry MUST include an `operation` field.** This is critical for log filtering and analysis.

```go
// From orchestration/orchestrator.go
if o.logger != nil {
    o.logger.ErrorWithContext(ctx, "Plan generation failed", map[string]interface{}{
        "operation":   "plan_generation",  // REQUIRED - describes what operation failed
        "request_id":  requestID,
        "error":       err.Error(),
        "duration_ms": time.Since(startTime).Milliseconds(),
    })
}

// From orchestration/orchestrator.go
if o.logger != nil {
    o.logger.InfoWithContext(ctx, "Plan generated successfully", map[string]interface{}{
        "operation":          "plan_generation",  // Same operation, different message
        "request_id":         requestID,
        "plan_id":            plan.PlanID,
        "step_count":         len(plan.Steps),
        "generation_time_ms": time.Since(startTime).Milliseconds(),
    })
}
```

**Standard operation values:**

| Module | Operation | Description |
|--------|-----------|-------------|
| orchestration | `process_request` | Main request handling |
| orchestration | `process_request_complete` | Successful turn completion (carries `termination_reason` field: `completed` / `forced_terminal` / `clarification`) |
| orchestration | `streaming_complete` | Streaming turn completion (parity with `process_request_complete`) |
| orchestration | `plan_generation` | LLM plan creation |
| orchestration | `plan_validation` | Plan structural validation |
| orchestration | `plan_execution` | Executing plan steps |
| orchestration | `agent_discovery` | Finding agents |
| orchestration | `llm_call` | LLM API calls |
| orchestration | `conversation_history` | Shared conversation-history preparation and compaction lifecycle. Used by the metadata ingress path and the optional `ConversationHistoryHook` adapter. Success logs include `path`, token estimates, and `duration_ms`; degraded paths use bounded `error_type` values such as `count_tokens`, `compaction`, `watermark_mismatch`, `preparation`, or `session_read` |
| orchestration | `clarification_short_circuit` | Phase loop terminated early because the planner emitted `needs_user_input` |
| orchestration | `synthesis_clarification_mode` | Synthesizer entered clarification mode and used the augmented system prompt |
| orchestration | `empty_fallback_to_prior` | Tiered selector returned `[]` in a continuation phase; recovered using `prior_tool_ids` from phase context instead of the all-agents fallback |
| orchestration | `semantic_empty_phase1` | Phase 1 (or continuation without prior tools) saw a semantic-empty selector response; retries short-circuited and the caller falls back to all-agents after a single LLM call |
| orchestration | `remediation_trigger` | Phase loop forced a remediation continuation because one or more steps were skipped on a template-induced dependency failure; the next phase runs even if the current plan marked itself terminal. WARN level. Fields: `phase_number`, `plan_id`, `skipped_count`, `skipped_step_ids`, `error_type=template_induced_skip` |
| orchestration | `remediation_failure_pattern` | Diagnostic for the shared-error pattern analyzer that runs alongside `remediation_trigger`. Lets operators answer "did the pattern line make it into the remediation prompt, and if not, why not?" without re-running the computation. DEBUG level. Fields: `phase_number`, `emitted` (bool). When emitted: `total_failed`, `dominant_count`. When rejected: `reject_reason` (bounded enum: `insufficient_failures`, `no_majority_error`) |
| ai | `ai_request` | AI provider calls |
| ai | `chain_failover` | Chain client failed over to the next provider after a transient (proxy/CDN) 4xx error classified via `core.ProviderError.IsTransient()`. Indicates an infrastructure hiccup, not a request problem; usually self-resolves on the next provider |
| ai | `chain_failover_retryable` | Chain client failed over to the next provider after a billing/quota terminal error classified via `core.ProviderError.IsRetryable()` (e.g. Anthropic credit balance exhausted, OpenAI insufficient_quota). **Cost signal — the failed provider's account needs operator action (top up credits, raise quota).** Distinct from `chain_failover` because the operator response is different: transient errors self-resolve, billing errors require intervention |
| resilience | `circuit_breaker` | Circuit breaker state |
| resilience | `retry_attempt` | Retry operations |
| core | `framework_register_runnable` | A `core.Runnable` was registered with the framework via `Framework.RegisterRunnable`. Emitted once per registration, before `Run` is called |
| core | `framework_runnable_start` | `Framework.Run` is about to launch all registered runnables in parallel goroutines. Emitted once per `Run`, skipped when no runnables are registered |
| core | `framework_runnable_exit` | A registered runnable's `Start` method returned. Emitted at INFO level on clean exit (returned `nil` or `context.Canceled`), or at ERROR level with `error_type=runnable_exit` on any other error |
| core | `framework_runnable_drain` | Runnable drain lifecycle event. Emitted in three contexts: (1) INFO when draining begins after the HTTP server stops, (2) INFO when all runnables exit cleanly within the drain budget, (3) WARN with `error_type=runnable_drain_timeout` when the drain budget (`TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT`, default `10s`) is exceeded |
| memory | `memory_enrichment` | `MemoryEnrichmentHook.BeforePlanning` lifecycle — covers recent-events query failures (`error_type=episodic_recent_read`), entity-keyed history query failures (`error_type=episodic_read`), digest cache decisions (DEBUG-level path tracking), and successful enrichment with `entities_found` and `context_chars` summary at INFO level |
| memory | `memory_record` | `MemoryRecordHook.AfterExecution` lifecycle — covers per-step event recording. INFO on successful batch (`events_recorded` count), WARN on summarizer failure (`error_type=summarizer_error`), WARN on episodic write failure (`error_type=episodic_write`) for both entity-indexed and entity-less events, WARN on investigation release failure (`error_type=release`) |
| memory | `reflection_job` | Outer reflection-job lifecycle — just two log lines: "Reflection job started" (once at registration) and "Reflection job stopping (context cancelled)" (once at shutdown). Does not cover any individual pass |
| memory | `reflection_pass` | One reflection pass. Covers pass start/completion, distributed-lock acquire/skip outcomes, entity-discovery errors, per-fragment embed and knowledge-store errors. Per-entity reflector events (including the per-entity LLM call outcome) are logged under `operation=reflect` instead |
| core | `memory_sweeper` | Outer `*core.MemoryStoreSweeper` lifecycle — INFO at start, INFO at clean shutdown on ctx cancellation, WARN with `error_type=runnable_exit` if `Start` returned without ctx being cancelled (defensive, fires alongside the `memory.sweeper.unexpected_exits` counter). Does not cover any individual pass |
| core | `memory_sweep_pass` | One eviction-sweep pass over a `*core.MemoryStore`. INFO when at least one entry was deleted (`deleted_count > 0`) with `sweep_id`, `deleted_count`, `duration_ms`, and `status=success`; DEBUG with `sweep_id`, `deleted_count=0`, and `duration_ms` (no `status` field) when nothing expired. `sweep_id` is also propagated as `request_id` so log/trace correlation matches user-request flows |
| memory | `reflect` | Inner `LLMMemoryReflector.Reflect` call — per-entity logging. Emits: "knowledge fragments generated" on success, "insufficient events, skipping" when below the `MIN_EVENTS` threshold, and WARN-level errors for episodic read failures (`error_type=episodic_read`), LLM call failures (`llm_unavailable`), and response parse failures (`parse_failure`) |
| memory | `compact` | Inner `LLMMemoryReflector.Compact` call — Tier 2 maintenance that groups old events into digest events inside the episodic stream. Distinct from `reflection_pass`, which is the Tier 2 → Tier 3 bridge into Qdrant. Not invoked by `ReflectionJob.RunOnce` in the current wiring; call it explicitly if you want periodic compaction |
| scheduled-executor | `executor_start` | Worker goroutine pool started. Emitted once per `Worker.Start`, at INFO level, with `worker_count`, `queue_name`, and `max_retries` fields |
| scheduled-executor | `executor_stop` | Worker draining on ctx cancellation. Emitted once per `Worker.Start` exit, at INFO level |
| scheduled-executor | `executor_consume` | Consumer returned a transient error (Redis transport glitch, etc.). Emitted at WARN level with `error_type=consume_error` |
| scheduled-executor | `executor_dispatch` | One dispatch of a scheduled task to a target agent's `/api/v1/scheduled` endpoint. Error sub-types: `invalid_task_type`, `missing_target_agent`, `unknown_target_agent`, `target_not_agent`, `marshal_error`, `agent_error`, `max_retries_exhausted`, `dlq_write_failure`, `ack_failure` |
| scheduled-executor | `scheduled_task_handle` | Agent-side receipt of a scheduled task via `/api/v1/scheduled`. Emitted by `orchestration.RegisterScheduledEndpoint`. Includes `request_id`, `schedule_id`, `task_id`, `status`, and `duration_ms` |

### Pattern 3: Request ID Propagation (REQUIRED)

**Include `request_id` in all logs within a request context.** This enables request tracing.

```go
// From orchestration/orchestrator.go

// Step 1: Generate request_id at the entry point
requestID := generateRequestID()

// Step 2: Add to context baggage for downstream components
ctx = telemetry.WithBaggage(ctx, "request_id", requestID)

// Step 3: Include in all logs
if o.logger != nil {
    o.logger.InfoWithContext(ctx, "Starting request processing", map[string]interface{}{
        "operation":      "process_request",
        "request_id":     requestID,  // ALWAYS include
        "request_length": len(request),
    })
}
```

**Retrieving request_id in downstream components:**

```go
// In any component that receives the context
func (c *Component) doWork(ctx context.Context) error {
    // Retrieve request_id from context baggage
    requestID := telemetry.GetBaggage(ctx, "request_id")

    if c.logger != nil {
        c.logger.InfoWithContext(ctx, "Doing work", map[string]interface{}{
            "operation":  "do_work",
            "request_id": requestID,
        })
    }
    // ...
}
```

### Complete Logging Pattern

Here's the complete pattern that combines all requirements:

```go
// Complete pattern from framework code
func (o *Orchestrator) ProcessRequest(ctx context.Context, request string) (*Response, error) {
    startTime := time.Now()

    // Generate request_id
    requestID := generateRequestID()
    ctx = telemetry.WithBaggage(ctx, "request_id", requestID)

    // Log start with nil check + operation + request_id
    if o.logger != nil {
        o.logger.InfoWithContext(ctx, "Starting request processing", map[string]interface{}{
            "operation":      "process_request",
            "request_id":     requestID,
            "request_length": len(request),
        })
    }

    result, err := o.doWork(ctx, request)
    if err != nil {
        // Error logging with all required fields
        if o.logger != nil {
            o.logger.ErrorWithContext(ctx, "Request processing failed", map[string]interface{}{
                "operation":   "process_request",
                "request_id":  requestID,
                "error":       err.Error(),
                "duration_ms": time.Since(startTime).Milliseconds(),
            })
        }
        return nil, err
    }

    // Success logging with all required fields
    if o.logger != nil {
        o.logger.InfoWithContext(ctx, "Request processing completed", map[string]interface{}{
            "operation":   "process_request",
            "request_id":  requestID,
            "status":      "success",
            "duration_ms": time.Since(startTime).Milliseconds(),
        })
    }

    return result, nil
}
```

### Logging Checklist for New Code

Before submitting code, verify:

- [ ] All logger calls wrapped in `if logger != nil { ... }`
- [ ] Every log has an `operation` field
- [ ] Request-scoped logs include `request_id`
- [ ] Error logs include `error` field with `err.Error()`
- [ ] Error logs include `error_type` for classification (used as Prometheus metric label)
- [ ] Duration-sensitive operations include `duration_ms`
- [ ] Using `WithContext` methods for request handlers
- [ ] Using standard field names (see table above)

---

## 12. The Mixed Logging Problem

A common mistake is mixing Go's standard `log` package with the framework's Logger.

### The Problem

```go
func main() {
    // Creates standard log output - not integrated with framework
    log.Println("Starting agent...")

    agent := NewAgent()

    // Creates framework log output - different format, no correlation
    agent.Logger.Info("Agent created", nil)
}
```

This creates inconsistent output:

```
2024/01/15 10:00:00 Starting agent...                          <- Standard log format
2024-01-15T10:00:01Z [INFO] [my-agent] Agent created          <- Framework format
```

### The Solution

Use `log.Fatalf` only for unrecoverable startup errors. Once the framework is created, use the component's Logger exclusively:

```go
func main() {
    // BEFORE framework - standard log is acceptable for fatal errors
    if err := validateConfig(); err != nil {
        log.Fatalf("Configuration error: %v", err)
    }

    agent := NewAgent()
    framework, err := core.NewFramework(agent, ...)
    if err != nil {
        log.Fatalf("Framework creation failed: %v", err)
    }

    // AFTER framework - use component Logger exclusively
    agent.Logger.Info("Starting agent", map[string]interface{}{
        "port": port,
    })
}
```

---

## 13. Telemetry Integration

Logging integrates with TruvaG3's telemetry system for metrics and tracing.

### Three-Layer Observability

TruvaG3's `ProductionLogger` (defined in [`core/config.go`](../../core/config.go) — search for `type ProductionLogger`) implements three layers:

1. **Layer 1 - Console Output**: Always works, immediate visibility
2. **Layer 2 - Metrics Emission**: When telemetry is initialized
3. **Layer 3 - Trace Context**: When using `WithContext` methods

### Enabling Telemetry

Initialize telemetry before creating your component:

```go
func main() {
    // Initialize telemetry FIRST
    initTelemetry("my-service")
    defer telemetry.Shutdown(context.Background())

    // Create component - Logger will auto-integrate with telemetry
    agent := NewAgent()
    framework, _ := core.NewFramework(agent, ...)
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
        log.Printf("Warning: Telemetry initialization failed: %v", err)
        // Continue without telemetry - graceful degradation
    }
}
```

### Metric Emission from Logs

When telemetry is enabled, logs automatically emit metrics. Only specific low-cardinality fields are used as metric labels to prevent metric explosion:

**Allowed label fields** (defined in [`core/config.go`](../../core/config.go)):
- `operation`
- `status`
- `error_type`
- `service_type`
- `provider`

```go
// This log statement...
agent.Logger.Error("Request failed", map[string]interface{}{
    "operation": "get_weather",
    "error_type": "timeout",
})

// ...also emits this metric (automatically):
// truvag3.framework.operations{level="ERROR", service="my-agent", operation="get_weather", error_type="timeout"}
```

---

## 14. Component-Aware Logging for Framework Modules

TruvaG3 uses a component-based logging architecture that separates framework-level logs from agent/tool-level logs. This section explains how this segregation works and how to use it effectively.

### Understanding Log Segregation

Every log message in TruvaG3 includes a `component` field that identifies the source of the log. Components are organized into categories:

| Category | Format | Examples |
|----------|--------|----------|
| Framework modules | `framework/<module>` | `framework/core`, `framework/orchestration`, `framework/resilience`, `framework/ai` |
| Agents | `agent/<name>` | `agent/travel-research-agent`, `agent/research-agent-telemetry` |
| Tools | `tool/<name>` | `tool/weather-service`, `tool/currency-service` |

This separation makes it easy to filter and analyze logs by origin.

### Real-World Example Logs

Here are actual logs from a deployed `research-agent-telemetry` agent in a Kubernetes cluster, showing how components are segregated:

**Framework Core Logs** (service discovery operations):
```json
{
  "component": "framework/core",
  "level": "INFO",
  "message": "Starting service discovery",
  "service": "research-agent-telemetry",
  "timestamp": "2025-12-12T20:24:41Z"
}

{
  "component": "framework/core",
  "level": "INFO",
  "message": "Service discovery completed",
  "service": "research-agent-telemetry",
  "services_checked": 11,
  "services_found": 11,
  "timestamp": "2025-12-12T20:24:41Z"
}
```

**Agent Handler Logs** (your application code):
```json
{
  "component": "agent/research-agent-telemetry",
  "level": "INFO",
  "message": "AI-powered tool+capability selection (1 call, 50% cost savings)",
  "capability": "current_weather",
  "tool": "weather-service",
  "topic": "weather in Tokyo",
  "timestamp": "2025-12-12T20:25:06Z"
}

{
  "component": "agent/research-agent-telemetry",
  "level": "INFO",
  "message": "Tool call completed",
  "capability": "current_weather",
  "tool": "weather-service",
  "success": true,
  "timestamp": "2025-12-12T20:25:07Z"
}

{
  "component": "agent/research-agent-telemetry",
  "level": "INFO",
  "message": "Research topic completed",
  "processing_time": "3.04614971s",
  "tools_used": 1,
  "topic": "weather in Tokyo",
  "timestamp": "2025-12-12T20:25:07Z"
}
```

Notice how:
- Framework infrastructure logs show `"component": "framework/core"`
- Application-level logs show `"component": "agent/research-agent-telemetry"`
- Both share the same `service` field for correlation

### How Logging Works in Agents and Tools

When you create an agent or tool and pass a logger to framework modules, each module automatically identifies itself in log output. Here's how it flows:

```go
// Your agent passes its logger to the orchestrator
orchestrator := orchestration.NewAIOrchestrator(aiClient, catalogConfig, logger)

// Inside the orchestrator, the logger is wrapped with the framework component
// Logs will show "component": "framework/orchestration" instead of your agent's name
```

**Example: What your logs look like**

When your `travel-research-agent` calls the orchestration module:

```json
// Agent-level log (your code)
{
  "message": "Starting travel research request",
  "component": "agent/travel-research-agent",
  "topic": "Paris trip"
}

// Framework-level log (orchestration module)
{
  "message": "Auto-wiring parameters for step",
  "component": "framework/orchestration",
  "step": "get_weather",
  "params_resolved": 2
}

// Framework-level log (resilience module)
{
  "message": "Circuit breaker state change",
  "component": "framework/resilience",
  "state": "closed"
}
```

### AI Module Logger Propagation

The AI module (`ai/` package) requires special attention for logging because it operates independently from agents but needs the same production logger for trace correlation.

**How the Framework Propagates the Logger:**

When you register an agent with the Framework (`core.NewFramework()`), the Framework automatically:

1. Detects if the agent's `BaseAgent.AI` field contains an AI client
2. Checks if the AI client implements `SetLogger(Logger)` via interface detection
3. Propagates the production logger to the AI client
4. The AI client wraps the logger with `"framework/ai"` component prefix

**Implementation Details:**

```go
// core/agent.go - applyConfigToComponent() function
// Propagate logger to AI client if it exists
if base.AI != nil {
    if loggable, ok := base.AI.(interface{ SetLogger(Logger) }); ok {
        loggable.SetLogger(base.Logger)
    }
}
```

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

**Why This Matters:**

Without this propagation, AI module logs would be silent (using `NoOpLogger`). This caused issues where:
- AI requests worked correctly but produced no logs
- Trace IDs weren't being captured for AI operations
- Debugging AI-related issues was difficult

**Result: AI Module Logs with Trace Correlation:**

```json
{
  "timestamp": "2024-01-15T10:00:00Z",
  "level": "DEBUG",
  "service": "my-agent",
  "component": "framework/ai",
  "message": "AI HTTP request completed",
  "operation": "ai_http_success",
  "trace_id": "5b54aa1e7925acb809e77479b5797f5d",
  "span_id": "e75ad960517fa8fe"
}
```

**Critical: Initialization Order**

For AI logging to work correctly, telemetry must be initialized BEFORE creating your agent:

```go
func main() {
    // 1. Set component type
    core.SetCurrentComponentType(core.ComponentTypeAgent)

    // 2. Initialize telemetry BEFORE agent creation
    initTelemetry("my-agent")

    // 3. Create agent AFTER telemetry (Framework propagates logger automatically)
    agent, err := NewMyAgent()

    // 4. Create and start Framework
    framework, _ := core.NewFramework(agent)
    framework.Start()
}
```

### Using Logging in Your Agents

When building agents, use the `Logger` from `BaseAgent` for all your application logs:

```go
type MyAgent struct {
    *core.BaseAgent
}

func (a *MyAgent) handleRequest(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Your agent logs use your agent's component name automatically
    a.Logger.InfoWithContext(ctx, "Processing request", map[string]interface{}{
        "path": r.URL.Path,
    })

    // When you use framework modules (orchestration, resilience, etc.),
    // their logs will show "framework/<module>" as the component
    result, err := a.orchestrator.ExecuteWorkflow(ctx, workflow)

    // Your completion log uses your agent's component name
    a.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
        "status": "success",
    })
}
```

### Using Logging in Your Tools

Tools work the same way as agents:

```go
type WeatherTool struct {
    *core.BaseTool
}

func (t *WeatherTool) handleGetWeather(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Tool logs show "tool/weather-tool" as the component
    t.Logger.InfoWithContext(ctx, "Fetching weather data", map[string]interface{}{
        "lat": lat,
        "lon": lon,
    })
}
```

### Filtering Logs by Component

In production, you can filter logs to focus on specific components. These commands work with the deployed examples in Kubernetes.

#### Quick Component Count

Get a summary of which components are logging:

```bash
# Count logs by component in the last 60 seconds
kubectl logs -n truvag3-examples -l app=research-agent-telemetry --since=60s 2>&1 | \
  grep -oP '"component":"[^"]*"' | sort | uniq -c

# Example output:
#   5 "component":"agent/research-agent-telemetry"
#  15 "component":"framework/core"
```

#### JSON Format Filtering (with jq)

**Show only framework logs:**
```bash
kubectl logs -n truvag3-examples -l app=my-agent | jq 'select(.component | startswith("framework/"))'
```

**Show only your agent/tool logs (hide framework noise):**
```bash
kubectl logs -n truvag3-examples -l app=research-agent-telemetry | \
  jq 'select(.component | startswith("agent/") or startswith("tool/"))'
```

**Show specific framework module logs:**
```bash
# Core module (discovery, registry)
kubectl logs -n truvag3-examples -l app=my-agent | jq 'select(.component == "framework/core")'

# Orchestration module
kubectl logs -n truvag3-examples -l app=my-agent | jq 'select(.component == "framework/orchestration")'

# Resilience module (retries, circuit breakers)
kubectl logs -n truvag3-examples -l app=my-agent | jq 'select(.component == "framework/resilience")'

# AI module
kubectl logs -n truvag3-examples -l app=my-agent | jq 'select(.component == "framework/ai")'
```

**Filter by component AND log level:**
```bash
# Only errors from orchestration
kubectl logs -n truvag3-examples -l app=my-agent | \
  jq 'select(.component == "framework/orchestration" and .level == "ERROR")'

# Warnings from any framework module
kubectl logs -n truvag3-examples -l app=my-agent | \
  jq 'select(.component | startswith("framework/") and .level == "WARN")'
```

**Extract specific fields for analysis:**
```bash
# Show timestamp, component, and message only
kubectl logs -n truvag3-examples -l app=research-agent-telemetry | \
  jq '{timestamp, component, message}'
```

#### Using grep for Text-Format Logs

When JSON parsing isn't available, use grep:

```bash
# Framework logs
kubectl logs -n truvag3-examples -l app=my-agent | grep '"component":"framework/'

# Agent logs
kubectl logs -n truvag3-examples -l app=my-agent | grep '"component":"agent/'

# Tool logs
kubectl logs -n truvag3-examples -l app=my-agent | grep '"component":"tool/'

# Specific component
kubectl logs -n truvag3-examples -l app=my-agent | grep '"component":"framework/orchestration"'
```

#### Grafana Loki (LogQL)

If using Grafana Loki for log aggregation:

```logql
# One service's logs
{service_name="hotel-tool"}

# All agents' handler logs
{k8s_namespace_name="truvag3-examples"} | json | component =~ "agent/.*"

# Framework orchestration errors from a specific agent
{service_name="travel-chat-agent"} | json | component="framework/orchestration" | level="ERROR"

# All tool logs with slow responses (>1 second)
{k8s_namespace_name="truvag3-examples"} | json | component =~ "tool/.*" | duration_ms > 1000

# Trace a request across all components using trace_id
{k8s_namespace_name="truvag3-examples"} | json | trace_id="abc123def456"

# All logs for a given orchestration request_id (finds the tool side too)
{k8s_namespace_name="truvag3-examples"} |= "orch-1776904450804389754"
```

**Why `{service_name="…"}` and `{k8s_namespace_name="…"}`?** These are the indexed
stream labels Loki actually exposes for this pipeline. `service_name` comes from the
pod's `app:` label (set per-record by `k8sattributes`), and `k8s_namespace_name` comes
from pod metadata. Filtering by either is cheap. The old `{namespace="…"}` idiom does
not work — that label is not indexed.

### Identifying Log Origins

When debugging, the `component` field tells you exactly where the log came from:

| Component | Origin | Example Log Messages |
|-----------|--------|----------------------|
| `agent/<name>` | Your agent's code (handlers, business logic) | "Processing research request", "Research topic completed" |
| `tool/<name>` | Your tool's code (capability handlers) | "Fetching weather data", "Tool call completed" |
| `framework/core` | Core infrastructure (discovery, registry, config) | "Service discovery completed", "Starting service discovery" |
| `framework/orchestration` | Orchestration (auto-wiring, execution, planning) | "Building execution plan", "Workflow execution complete" |
| `framework/resilience` | Resilience patterns (retries, circuit breakers) | "Retry attempt 2/3", "Circuit breaker opened" |
| `framework/ai` | AI module (LLM calls, prompts) | "AI request completed", "Token usage logged" |

### Sample Log Output Analysis

Here's a complete request flow from a deployed `research-agent-telemetry` showing how components are segregated:

```
20:24:41 [INFO] [framework/core] Starting service discovery
20:24:41 [INFO] [framework/core] Service discovery completed (11 services found)
20:25:06 [INFO] [agent/research-agent-telemetry] AI-powered tool+capability selection (1 call, 50% cost savings)
20:25:06 [INFO] [agent/research-agent-telemetry] Calling AI-selected tool+capability
20:25:07 [INFO] [agent/research-agent-telemetry] AI-generated payload successfully
20:25:07 [INFO] [agent/research-agent-telemetry] Calling tool with intelligent retry enabled
20:25:07 [INFO] [agent/research-agent-telemetry] Tool call completed (success)
20:25:07 [INFO] [agent/research-agent-telemetry] Research topic completed (3.04s)
```

From this log:
- Lines with `[framework/core]` are infrastructure operations (discovery, registry)
- Lines with `[agent/research-agent-telemetry]` are your application's business logic
- The request took 3.04 seconds total, with AI selection and tool execution

### Testing Component Logging

To verify component-aware logging is working in your deployment:

```bash
# 1. Port forward to your agent
kubectl port-forward -n truvag3-examples svc/research-agent-telemetry 8092:8092 &

# 2. Make a test request
curl -s -X POST http://localhost:8092/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{"topic":"weather in Tokyo","use_ai":false}'

# 3. Check logs for component field
kubectl logs -n truvag3-examples -l app=research-agent-telemetry --since=60s | \
  grep -oP '"component":"[^"]*"' | sort | uniq -c

# Expected output should show both framework and agent components:
#   5 "component":"agent/research-agent-telemetry"
#  15 "component":"framework/core"
```

### The ComponentAwareLogger Interface

The component segregation is powered by the `ComponentAwareLogger` interface defined in [`core/interfaces.go`](../../core/interfaces.go):

```go
// ComponentAwareLogger extends Logger with component context support
type ComponentAwareLogger interface {
    Logger
    // WithComponent returns a new logger with the specified component
    WithComponent(component string) Logger
}
```

The framework's `ProductionLogger` implements this interface, so component segregation works automatically when you use TruvaG3's standard logging setup.

### For Framework Module Developers

**Key Pattern**: Every framework module's `SetLogger` method should wrap the logger with `WithComponent("framework/<module>")`:

```go
func (x *MyComponent) SetLogger(logger core.Logger) {
    if logger == nil {
        x.logger = &core.NoOpLogger{}
    } else {
        if cal, ok := logger.(core.ComponentAwareLogger); ok {
            x.logger = cal.WithComponent("framework/mymodule")
        } else {
            x.logger = logger
        }
    }
}
```

---

## 15. Common Mistakes and How to Avoid Them

### Mistake 1: Using Basic Methods in Handlers

```go
// BAD - loses trace correlation
func (r *Agent) handleRequest(w http.ResponseWriter, req *http.Request) {
    r.Logger.Info("Processing request", nil)  // No context!
}

// GOOD - enables trace correlation
func (r *Agent) handleRequest(w http.ResponseWriter, req *http.Request) {
    ctx := req.Context()
    r.Logger.InfoWithContext(ctx, "Processing request", nil)
}
```

### Mistake 2: Logging Sensitive Data

```go
// BAD - exposes secrets
logger.Info("API call", map[string]interface{}{
    "api_key": apiKey,  // NEVER log secrets!
    "password": pwd,
})

// GOOD - safe logging
logger.Info("API call", map[string]interface{}{
    "provider": "openai",
    "has_key": apiKey != "",  // Boolean is safe
})
```

### Mistake 3: High-Cardinality Fields

```go
// BAD - creates too many unique metric labels
logger.Info("Request", map[string]interface{}{
    "user_id": "user-12345",     // High cardinality
    "request_id": uuid.New(),    // Unique every time
    "timestamp": time.Now(),     // Always different
})

// GOOD - low cardinality fields as labels
logger.Info("Request", map[string]interface{}{
    "operation": "get_weather",  // Fixed set of values
    "status": "success",         // Fixed set of values
    "duration_ms": 125,          // Not a label, just a value
})
```

### Mistake 4: Not Logging Errors Properly

```go
// BAD - loses error context
if err != nil {
    logger.Error("Failed", nil)
    return err
}

// GOOD - includes error details
if err != nil {
    logger.ErrorWithContext(ctx, "Operation failed", map[string]interface{}{
        "operation": "fetch_data",
        "error": err.Error(),
        "error_type": fmt.Sprintf("%T", err),
    })
    return err
}
```

### Mistake 5: Forgetting to Log Success

```go
// BAD - only logs failures
func (r *Agent) handleRequest(w http.ResponseWriter, req *http.Request) {
    ctx := req.Context()

    result, err := doWork()
    if err != nil {
        r.Logger.ErrorWithContext(ctx, "Failed", map[string]interface{}{"error": err.Error()})
        return
    }

    // Where's the success log?
    json.NewEncoder(w).Encode(result)
}

// GOOD - logs both success and failure
func (r *Agent) handleRequest(w http.ResponseWriter, req *http.Request) {
    ctx := req.Context()
    startTime := time.Now()

    r.Logger.InfoWithContext(ctx, "Processing request", nil)

    result, err := doWork()
    if err != nil {
        r.Logger.ErrorWithContext(ctx, "Request failed", map[string]interface{}{
            "error": err.Error(),
            "duration_ms": time.Since(startTime).Milliseconds(),
        })
        return
    }

    r.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
        "duration_ms": time.Since(startTime).Milliseconds(),
        "status": "success",
    })

    json.NewEncoder(w).Encode(result)
}
```

---

## 16. Quick Reference

### Environment Variables

| Variable | Values | Default |
|----------|--------|---------|
| `TRUVAG3_LOG_LEVEL` | debug, info, warn, error | info |
| `TRUVAG3_LOG_FORMAT` | json, text | json |
| `TRUVAG3_DEBUG` | true, false | false |

### Method Selection

| Situation | Method |
|-----------|--------|
| HTTP handler | `InfoWithContext(ctx, ...)` |
| Startup/shutdown | `Info(...)` |
| Background task | `Info(...)` |
| Any error | `ErrorWithContext(ctx, ...)` or `Error(...)` |

### Standard Fields

| Field | Required | Use For |
|-------|----------|---------|
| `operation` | **YES** | What action is being performed (MUST be in every log) |
| `request_id` | **YES** | Request identifier (for request-scoped logs) |
| `error` | On errors | Error message (use `err.Error()`) |
| `duration_ms` | Recommended | How long it took |
| `status` | Recommended | success, error, retry |
| `method` | For HTTP | HTTP method |
| `path` | For HTTP | Request path |

### Logging Checklist

**Required (Framework Code):**
- [ ] Logger nil checks: `if logger != nil { ... }`
- [ ] `operation` field in every log entry
- [ ] `request_id` in all request-scoped logs

**Required (Application Code):**
- [ ] Using `WithContext` methods in all HTTP handlers
- [ ] `error` field for error logs (use `err.Error()`)
- [ ] Logging both success and failure paths

**Recommended:**
- [ ] Including `duration_ms` for operations
- [ ] Using consistent field names
- [ ] Not logging sensitive data
- [ ] Using appropriate log levels
- [ ] Initializing telemetry before creating components

---

## 17. Manual Trace ID Extraction

For advanced use cases where you need direct access to trace IDs (e.g., including them in API responses or external logging systems), use `telemetry.GetTraceContext()`:

```go
import "github.com/truvaagents/truva-g3/telemetry"

func (a *MyAgent) handleRequest(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Extract trace context for manual use
    tc := telemetry.GetTraceContext(ctx)

    // Include in response headers for client correlation
    if tc.TraceID != "" {
        w.Header().Set("X-Trace-ID", tc.TraceID)
    }

    // Or include in structured logs manually
    a.Logger.InfoWithContext(ctx, "Processing", map[string]interface{}{
        "trace_id": tc.TraceID,  // Usually automatic via WithContext
        "span_id":  tc.SpanID,
    })
}
```

> **Note**: The `WithContext` methods automatically include trace correlation. Manual extraction is only needed for special cases like response headers or external system integration.

For complete distributed tracing setup including infrastructure (Jaeger, OTEL Collector), client-side propagation, and trace visualization, see **[DISTRIBUTED_TRACING_GUIDE.md](DISTRIBUTED_TRACING_GUIDE.md)**.

---

## Summary

1. **Use the framework's Logger**, not Go's standard `log` package (except for fatal startup errors)
2. **Always use `WithContext` methods** in HTTP handlers for trace correlation
3. **Be consistent with field names** across all services
4. **Log both success and failure** with duration metrics
5. **Initialize telemetry first** to enable all three observability layers

Following these guidelines ensures your logs are useful in production, easy to search, and properly correlated across your distributed system.

---

## See Also

- **[DISTRIBUTED_TRACING_GUIDE.md](DISTRIBUTED_TRACING_GUIDE.md)** - Complete guide for distributed tracing setup, including TracingMiddleware, TracedHTTPClient, Jaeger/OTEL infrastructure, and trace visualization
- **[orchestration/hitl_interfaces.go](../../orchestration/hitl_interfaces.go)** - HITL interfaces including `ExecutionCheckpoint` with `OriginalRequestID` field
- **[telemetry/trace_context.go](../../telemetry/trace_context.go)** - Source for `GetTraceContext()`, `AddSpanEvent()`, `RecordSpanError()`
- **[core/config.go](../../core/config.go)** - ProductionLogger implementation and `WithComponent()` method
- **[core/interfaces.go](../../core/interfaces.go)** - Logger interface and `ComponentAwareLogger` interface definitions
