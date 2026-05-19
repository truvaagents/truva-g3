# TruvaG3 Agent Development Guide

This guide provides a comprehensive, step-by-step tutorial for developing **agents** in the TruvaG3 framework. Agents are active components that use AI-powered orchestration to coordinate multiple tools and provide intelligent responses to natural language queries.

## Table of Contents

1. [Understanding Agents in TruvaG3](#1-understanding-agents-in-truvag3)
2. [Choosing Your Agent Type](#2-choosing-your-agent-type)
3. [Project Structure](#3-project-structure)
4. [Step 1: Create the Agent Struct](#4-step-1-create-the-agent-struct)
5. [Step 2: Configure the Orchestrator](#5-step-2-configure-the-orchestrator)
6. [Step 3: Register Capabilities](#6-step-3-register-capabilities)
7. [Step 4: Implement Handlers](#7-step-4-implement-handlers)
8. [Step 5: Add SSE Streaming (Streaming Agents Only)](#8-step-5-add-sse-streaming-streaming-agents-only)
9. [Step 6: Add Session Management (Chat Agents Only)](#9-step-6-add-session-management-chat-agents-only)
10. [Step 7: Create the Main Entry Point](#10-step-7-create-the-main-entry-point)
   - [Background Jobs: `core.Runnable` and `framework.RegisterRunnable`](#background-jobs-corerunnable-and-frameworkregisterrunnable)
11. [Step 8: Add Deployment Files](#11-step-8-add-deployment-files)
12. [Logging and Observability](#12-logging-and-observability)
13. [Distributed Tracing](#13-distributed-tracing)
14. [Testing Your Agent](#14-testing-your-agent)
15. [Adding Human-in-the-Loop (HITL) Approval](#15-adding-human-in-the-loop-hitl-approval)
   - 15.1 [When to Use HITL](#151-when-to-use-hitl)
   - 15.2 [Resume Context: The Critical Contract](#152-resume-context-the-critical-contract)
   - 15.3 [Common Pitfall: Manual Context Setup](#153-common-pitfall-manual-context-setup)
16. [Best Practices](#16-best-practices)
17. [Troubleshooting](#17-troubleshooting)
18. [Quick Reference](#18-quick-reference)

---

## 1. Understanding Agents in TruvaG3

### What is an Agent?

In TruvaG3, an **Agent** is an active component that:
- Uses AI-powered orchestration to coordinate multiple tools
- Processes natural language requests and generates execution plans
- Can discover other components via service discovery (Redis)
- Synthesizes results from multiple tool calls into coherent responses
- Optionally streams responses in real-time via SSE

### Agents vs Tools

| Aspect | Tool | Agent |
|--------|------|-------|
| Base type | `*core.BaseTool` | `*core.BaseAgent` |
| Discovery | Can register, **cannot** discover | Can both register **and** discover |
| Orchestration | Cannot orchestrate | Orchestrates other components via AI |
| AI client | Rarely needed | **Required** for orchestration |
| Purpose | Provide specific capabilities (API wrapper) | Coordinate tools, process natural language |
| Imports | `core`, `telemetry` | `core`, `telemetry`, `ai`, `orchestration` |
| Example | weather-tool, currency-tool | travel-chat-agent, devops-chat-agent |

### Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────┐
│                          Your Agent                                   │
│                                                                       │
│  ┌──────────────────────────────────────────────────────────────────┐ │
│  │                    AI Orchestrator                                │ │
│  │  ┌──────────┐  ┌──────────┐  ┌───────────┐  ┌──────────────┐   │ │
│  │  │ Planner  │  │ Executor │  │ Synthesizer│  │ Error Recovery│   │ │
│  │  │ (LLM)   │→│ (DAG)    │→│ (LLM)      │  │ (4-Layer)    │   │ │
│  │  └──────────┘  └──────────┘  └───────────┘  └──────────────┘   │ │
│  └──────────────────────────────────────────────────────────────────┘ │
│                                │                                      │
│  ┌──────────────┐   ┌──────────▼──────────┐   ┌──────────────────┐  │
│  │ Session Store│   │  Service Discovery  │   │  SSE Handler     │  │
│  │ (Redis DB 2) │   │  (Redis DB 0)       │   │  (Streaming)     │  │
│  └──────────────┘   └─────────────────────┘   └──────────────────┘  │
│                                │                                      │
└────────────────────────────────┼──────────────────────────────────────┘
                                 │
            ┌────────────────────┼────────────────────┐
            │                    │                    │
       ┌────▼───┐          ┌────▼───┐          ┌────▼───┐
       │  Tool  │          │  Tool  │          │  Tool  │
       │weather │          │currency│          │ devops │
       └────────┘          └────────┘          └────────┘
```

---

## 2. Choosing Your Agent Type

TruvaG3 supports two agent patterns. Choose based on your use case:

### Non-Streaming Agent (Request/Response)

**Use when:** Building API backends, batch processing, or server-to-server orchestration.

```
Client → POST /orchestrate/natural → Agent → Orchestrator → Tools → JSON Response
```

- Uses `orchestrator.ProcessRequest()` — returns complete response
- Standard HTTP JSON request/response
- Simpler to implement
- **Reference implementation:** `examples/agent-with-orchestration`

### Streaming Agent (SSE Chat)

**Use when:** Building chat UIs, real-time dashboards, or interactive experiences.

```
Client → POST /chat/stream → Agent → Orchestrator → Tools → SSE Events (streaming)
```

- Uses `orchestrator.ProcessRequestStreaming()` — delivers tokens as they're generated
- Server-Sent Events (SSE) for real-time delivery
- Per-step progress callbacks for tool completion events
- Session management with conversation history
- **Reference implementation:** `examples/travel-chat-agent`

### Feature Comparison

| Feature | Non-Streaming | Streaming |
|---------|--------------|-----------|
| Response delivery | Complete JSON | Token-by-token SSE |
| Time to first byte | After full processing | Immediate (planning status) |
| Step progress | In final response | Real-time SSE events |
| Conversation history | Optional | Typically required |
| Session management | Optional | Typically required |
| Implementation complexity | Lower | Higher |
| Files needed | 3-4 Go files | 4-5 Go files |

---

## 3. Project Structure

### Non-Streaming Agent

```
examples/your-agent/
├── main.go              # Entry point, telemetry, framework setup, orchestrator init
├── your_agent.go        # Agent struct, AI client, orchestrator config, capability registration
├── handlers.go          # HTTP handlers for orchestration and utility endpoints
├── go.mod               # Go module with ai, core, orchestration, telemetry dependencies
├── .env                 # Environment variables (API keys, Redis URL, port)
├── Dockerfile           # Standalone container build
├── Dockerfile.workspace # Development container (builds from truvag3 root)
├── k8-deployment.yaml   # Kubernetes deployment manifest
└── setup.sh             # Full lifecycle: build, deploy, test, clean
```

### Streaming Agent (Chat)

```
examples/your-chat-agent/
├── main.go              # Entry point, telemetry, framework setup, orchestrator init
├── chat_agent.go        # Agent struct, AI client, orchestrator config, streaming logic
├── sse_handler.go       # SSE handler, StreamCallback interface, SSE event formatting
├── session.go           # Redis-backed session store, conversation history management
├── handlers.go          # REST handlers for session CRUD, health, discovery
├── go.mod               # Go module with ai, core, orchestration, telemetry dependencies
├── .env                 # Environment variables (API keys, Redis URL, port)
├── Dockerfile           # Standalone container build
├── Dockerfile.workspace # Development container (builds from truvag3 root)
├── k8-deployment.yaml   # Kubernetes deployment manifest
└── setup.sh             # Full lifecycle: build, deploy, test, clean
```

### File Responsibilities

| File | Non-Streaming | Streaming |
|------|--------------|-----------|
| `main.go` | Config validation, telemetry init, framework setup, background orchestrator init, graceful shutdown | Same |
| `*_agent.go` | Agent struct, AI chain client, orchestrator config with PromptConfig, capability registration | Same + `ProcessWithStreaming()` method, conversation context formatting |
| `handlers.go` | `handleNaturalOrchestration()`, `handleHealth()`, `handleDiscover()`, helper functions | Session CRUD handlers, health, discover, helper functions |
| `sse_handler.go` | N/A | `StreamCallback` interface, `SSECallback` impl, `SSEHandler.ServeHTTP()` |
| `session.go` | N/A | `SessionStore` (Redis-backed), `Session`/`Message` types, conversation history |

---

## 4. Step 1: Create the Agent Struct

The agent struct embeds `*core.BaseAgent` and holds the orchestrator, HTTP client, and any domain-specific state.

### Non-Streaming Agent

```go
// your_agent.go
package main

import (
    "fmt"
    "net/http"
    "sync"
    "time"

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration"
    "github.com/truvaagents/truva-g3/telemetry"
)

type YourAgent struct {
    *core.BaseAgent
    orchestrator *orchestration.AIOrchestrator
    httpClient   *http.Client
    mu           sync.RWMutex
}

func NewYourAgent() (*YourAgent, error) {
    agent := core.NewBaseAgent("your-agent")

    // Create AI client with provider chain for automatic failover
    // Provider chain: OpenAI (primary) → Anthropic (backup)
    chainClient, err := ai.NewChainClient(
        ai.WithProviderChain("openai", "anthropic"),
        ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
        ai.WithChainLogger(agent.Logger),
    )
    if err != nil {
        // Fallback to single provider
        singleClient, err := ai.NewClient()
        if err != nil {
            agent.Logger.Warn("AI client creation failed", map[string]interface{}{
                "error": err.Error(),
            })
        } else {
            agent.AI = singleClient
        }
    } else {
        agent.AI = chainClient
    }

    // Declare custom metrics for observability
    telemetry.DeclareMetrics("your-agent", telemetry.ModuleConfig{
        Metrics: []telemetry.MetricDefinition{
            {
                Name:    "orchestration.request.duration_ms",
                Type:    "histogram",
                Help:    "Orchestration request duration in milliseconds",
                Labels:  []string{"status"},
                Unit:    "milliseconds",
                Buckets: []float64{100, 500, 1000, 5000, 10000, 30000},
            },
            {
                Name:   "orchestration.requests",
                Type:   "counter",
                Help:   "Number of orchestration requests",
                Labels: []string{"status"},
            },
        },
    })

    // Create traced HTTP client for distributed tracing propagation
    tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
        ForceAttemptHTTP2:   true,
    })
    tracedClient.Timeout = 300 * time.Second

    yourAgent := &YourAgent{
        BaseAgent:  agent,
        httpClient: tracedClient,
    }

    yourAgent.registerCapabilities()
    return yourAgent, nil
}
```

### Streaming Chat Agent

```go
// chat_agent.go
package main

import (
    "context"
    "fmt"
    "net/http"
    "os"
    "strings"
    "sync"
    "time"

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration"
    "github.com/truvaagents/truva-g3/telemetry"
    "go.opentelemetry.io/otel/attribute"
)

type YourChatAgent struct {
    *core.BaseAgent
    orchestrator *orchestration.AIOrchestrator
    sessionStore *SessionStore
    httpClient   *http.Client
    mu           sync.RWMutex
}

func NewYourChatAgent() (*YourChatAgent, error) {
    agent := core.NewBaseAgent("your-chat-agent")

    // AI client with auto-detect chain (discovers providers from available API keys)
    // For explicit ordering, add: ai.WithProviderChain("openai", "anthropic", "openai.groq")
    chainClient, err := ai.NewChainClient(
        ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
        ai.WithChainLogger(agent.Logger),
        ai.WithChainTimeout(240*time.Second), // Extended for reasoning models
    )
    if err != nil {
        singleClient, _ := ai.NewClient(ai.WithTimeout(240 * time.Second))
        agent.AI = singleClient
    } else {
        agent.AI = chainClient
    }

    // Declare metrics (same pattern)
    telemetry.DeclareMetrics("your-chat-agent", telemetry.ModuleConfig{
        Metrics: []telemetry.MetricDefinition{
            {
                Name:    "chat.request.duration_ms",
                Type:    "histogram",
                Help:    "Chat request duration in milliseconds",
                Labels:  []string{"session_id", "status"},
                Unit:    "milliseconds",
                Buckets: []float64{100, 500, 1000, 2000, 5000, 10000, 30000},
            },
            {
                Name:   "chat.requests",
                Type:   "counter",
                Help:   "Number of chat requests",
                Labels: []string{"status"},
            },
        },
    })

    // Traced HTTP client (same pattern)
    tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
        ForceAttemptHTTP2:   true,
    })
    tracedClient.Timeout = 300 * time.Second

    // Create Redis-backed session store (uses Redis DB 2)
    redisURL := os.Getenv("REDIS_URL")
    sessionStore, err := NewSessionStore(redisURL, 48*time.Hour, 50, agent.Logger)
    if err != nil {
        return nil, fmt.Errorf("failed to create session store: %w", err)
    }

    chatAgent := &YourChatAgent{
        BaseAgent:    agent,
        sessionStore: sessionStore,
        httpClient:   tracedClient,
    }

    chatAgent.registerCapabilities()
    return chatAgent, nil
}
```

### Key Points

1. **Embed `*core.BaseAgent`**: Provides `Logger`, `AI`, `Discovery`, `RegisterCapability()`, and `GetID()`
2. **AI provider chain**: Use `ai.NewChainClient()` for automatic failover between providers. Falls back to `ai.NewClient()` for single provider
3. **Traced HTTP client**: Always use `telemetry.NewTracedHTTPClientWithTransport()` for trace context propagation to downstream tools
4. **Mutex protection**: The orchestrator is initialized asynchronously, so protect access with `sync.RWMutex`
5. **Declare metrics**: Use `telemetry.DeclareMetrics()` to register custom metrics at startup
6. **Optional memory fields**: `BaseAgent` has two optional fields — `ConversationMemory` (session-scoped history) and `SemanticMemory` (cross-session similarity search). Set them when your agent needs conversation context or long-term knowledge retrieval. See [Adding Context to Your Agent](ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) for implementation patterns

### AI Client Configuration

The code above uses `ai.NewChainClient()` for multi-provider failover. Key concepts:

- **Provider aliases**: One-line identifiers (`"openai"`, `"anthropic"`, `"openai.groq"`, `"openai.ollama"`) that auto-resolve base URL + API key env var
- **Model aliases**: Portable names (`"default"`, `"fast"`, `"smart"`, `"premium"`) that resolve to the best model per provider. Override at runtime: `TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1`
- **Explicit vs auto-detect chain**: Use `ai.WithProviderChain("openai", "anthropic")` for deterministic order, or omit for auto-detection from available API keys
- **Reasoning models** (o1, o3, o4): Need extended timeouts (240s+) and a token multiplier (default 5x) for chain-of-thought overhead

> **Reference:** [AI_PROVIDERS_SETUP_GUIDE.md](AI_PROVIDERS_SETUP_GUIDE.md) for provider/model alias tables, failover behavior, K8s secrets/ConfigMaps, and operational scenarios.

---

## 5. Step 2: Configure the Orchestrator

The orchestrator is initialized **after** the framework starts (because it needs Discovery, which connects to Redis during `framework.Run()`). This is done in a background goroutine from `main.go`.

### InitializeOrchestrator Method

```go
// your_agent.go (continued)

func (a *YourAgent) InitializeOrchestrator(discovery core.Discovery) error {
    a.mu.Lock()
    defer a.mu.Unlock()

    if discovery == nil {
        return fmt.Errorf("discovery service not available")
    }

    // 1. Create orchestrator config
    config := orchestration.DefaultConfig()
    config.RoutingMode = orchestration.ModeAutonomous     // AI-driven, no workflow
    config.SynthesisStrategy = orchestration.StrategyLLM  // LLM synthesizes results
    config.MetricsEnabled = true
    config.EnableTelemetry = true

    // 2. Phase-specific LLM configuration
    config.PlanAIOptions = &orchestration.AIOptionsOverride{
        MaxTokens: orchestration.IntPtr(15000),
    }
    config.SynthesisAIOptions = &orchestration.AIOptionsOverride{
        MaxTokens:   orchestration.IntPtr(5000),
        Temperature: orchestration.Float32Ptr(0.5),  // 0.5 for non-streaming, 0.7 for more natural streaming
    }

    // 3. Execution timeouts
    config.ExecutionOptions.TotalTimeout = 5 * time.Minute
    config.ExecutionOptions.StepTimeout = 120 * time.Second

    // 4. Configure PromptConfig for your domain
    config.PromptConfig = orchestration.PromptConfig{
        // SystemInstructions: The agent's persona and behavioral context.
        // This becomes the primary identity for the LLM planner and synthesizer.
        SystemInstructions: `You are a helpful DevOps assistant.
You help users manage Kubernetes clusters, check system status, and troubleshoot issues.
Provide clear, actionable information and warn about potentially destructive operations.`,

        // Domain helps the LLM understand the context
        Domain: "devops",

        // Domain-specific type rules for correct JSON generation
        AdditionalTypeRules: []orchestration.TypeRule{
            {
                TypeNames:   []string{"namespace"},
                JsonType:    "JSON strings",
                Example:     `"default"`,
                Description: "Kubernetes namespace names",
            },
            {
                TypeNames:   []string{"replicas", "replica_count"},
                JsonType:    "JSON integers",
                Example:     `3`,
                Description: "Number of pod replicas (0-10)",
            },
        },

        // Custom instructions guide the LLM planner
        CustomInstructions: []string{
            "For pod queries, always specify the namespace parameter",
            "Before scaling, check current replica count first",
            "Prefer parallel execution when steps are independent",
        },
    }

    // 5. Create dependencies with dependency injection
    deps := orchestration.OrchestratorDependencies{
        Discovery:           discovery,
        AIClient:            a.AI,
        Logger:              a.Logger,
        Telemetry:           telemetry.GetTelemetryProvider(),
        EnableErrorAnalyzer: true, // Enable LLM-based error analysis (Layer 3)
    }

    // 6. Create and start orchestrator
    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return fmt.Errorf("failed to create orchestrator: %w", err)
    }

    ctx := context.Background()
    if err := orch.Start(ctx); err != nil {
        return fmt.Errorf("failed to start orchestrator: %w", err)
    }

    a.orchestrator = orch

    a.Logger.Info("Orchestrator initialized", map[string]interface{}{
        "routing_mode":       config.RoutingMode,
        "synthesis_strategy": config.SynthesisStrategy,
    })

    return nil
}
```

### PromptConfig Tips

`PromptConfig` is the most important configuration — it controls how the LLM plans and synthesizes:
- **SystemInstructions**: Define persona in first sentence, state what agent does, include constraints. Keep to 3-5 sentences
- **CustomInstructions**: One decision per instruction. Use "always", "before", "prefer" to be unambiguous
- **AdditionalTypeRules**: Teach LLM domain-specific JSON types (e.g., namespace names, replica counts)

> **See also:** [docs/orchestration/LLM_PLANNING_PROMPT_GUIDE.md](../orchestration/LLM_PLANNING_PROMPT_GUIDE.md) for full PromptConfig documentation.

---

## 6. Step 3: Register Capabilities

Agent capabilities define the HTTP endpoints your agent exposes. **Critical rule:** All agent capabilities must be marked `Internal: true` to prevent the orchestrator's LLM from including the agent's own endpoints in execution plans (which would cause recursive self-calls).

### Non-Streaming Agent Capabilities

```go
func (a *YourAgent) registerCapabilities() {
    // Primary endpoint: Natural language orchestration
    a.RegisterCapability(core.Capability{
        Name:        "orchestrate_natural",
        Description: "Process natural language requests using AI-powered orchestration. " +
                     "Required: request (natural language query). " +
                     "Optional: ai_synthesis (boolean, default: true), metadata (object).",
        Endpoint:    "/orchestrate/natural",
        InputTypes:  []string{"json", "text"},
        OutputTypes: []string{"json"},
        Handler:     a.handleNaturalOrchestration,
        Internal:    true, // CRITICAL: Prevents recursive self-calls
        InputSummary: &core.SchemaSummary{
            RequiredFields: []core.FieldHint{
                {
                    Name:        "request",
                    Type:        "string",
                    Example:     "What is the cluster status?",
                    Description: "Natural language request to process",
                },
            },
            OptionalFields: []core.FieldHint{
                {
                    Name:        "ai_synthesis",
                    Type:        "boolean",
                    Example:     "true",
                    Description: "Enable AI synthesis of results (default: true)",
                },
                {
                    Name:        "metadata",
                    Type:        "object",
                    Example:     `{"namespace": "production"}`,
                    Description: "Additional context and preferences",
                },
            },
        },
    })

    // Utility: Health check with orchestrator status
    a.RegisterCapability(core.Capability{
        Name:        "health",
        Description: "Health check with orchestrator status and metrics",
        Endpoint:    "/health",
        InputTypes:  []string{},
        OutputTypes: []string{"json"},
        Handler:     a.handleHealth,
        Internal:    true,
    })

    // Utility: Discover available tools
    a.RegisterCapability(core.Capability{
        Name:        "discover_tools",
        Description: "Discovers available tools and their capabilities",
        Endpoint:    "/discover",
        InputTypes:  []string{},
        OutputTypes: []string{"json"},
        Handler:     a.handleDiscover,
        Internal:    true,
    })
}
```

### Streaming Chat Agent Capabilities

```go
func (a *YourChatAgent) registerCapabilities() {
    // SSE streaming chat endpoint
    a.RegisterCapability(core.Capability{
        Name:        "chat_stream",
        Description: "SSE streaming chat endpoint",
        Endpoint:    "/chat/stream",
        Handler:     NewSSEHandler(a).ServeHTTP,
        Internal:    true,
    })

    // Session management + health + discover — all Internal: true
    a.RegisterCapability(core.Capability{Name: "create_session", Endpoint: "/chat/session", Handler: a.handleCreateSession, Internal: true})
    a.RegisterCapability(core.Capability{Name: "get_session", Endpoint: "/chat/session/{id}", Handler: a.handleGetSession, Internal: true})
    a.RegisterCapability(core.Capability{Name: "get_history", Endpoint: "/chat/session/{id}/history", Handler: a.handleGetHistory, Internal: true})
    a.RegisterCapability(core.Capability{Name: "list_sessions", Endpoint: "/chat/sessions", Handler: a.handleListSessions, Internal: true})
    a.RegisterCapability(core.Capability{Name: "update_title", Endpoint: "/chat/session/{id}/title", Handler: a.handleUpdateTitle, Internal: true})
    a.RegisterCapability(core.Capability{Name: "delete_session", Endpoint: "/chat/session/delete", Handler: a.handleDeleteSession, Internal: true})
    a.RegisterCapability(core.Capability{Name: "health", Endpoint: "/health", Handler: a.handleHealth, Internal: true})
    a.RegisterCapability(core.Capability{Name: "discover", Endpoint: "/discover", Handler: a.handleDiscover, Internal: true})
}
```

### Why `Internal: true` is Critical

```
Without Internal: true:
  User: "Check cluster status"
  LLM Plan: step-1: your-agent.orchestrate_natural("Check cluster status")  ← RECURSIVE!

With Internal: true:
  User: "Check cluster status"
  LLM Plan: step-1: devops-tool.get_cluster_status()  ← Correct!
```

The `Internal` flag excludes capabilities from the LLM's tool catalog while keeping them callable via HTTP. This prevents the orchestrator from planning steps that call itself.

### When to Use `Internal: false` (Agent-as-Tool)

For multi-agent hierarchies, expose domain-specific capabilities with `Internal: false` so other agents can orchestrate your agent. Orchestration endpoints (`/orchestrate/*`, `/chat/stream`) remain `Internal: true`.

```go
// Exposed to other agents' LLM planners
a.RegisterCapability(core.Capability{
    Name:        "research_topic",
    Description: "Researches a topic using multiple tools and returns a synthesized report. " +
                 "Required: topic (research subject). Optional: depth (brief/standard/deep).",
    Endpoint:    "/research",
    Type:        core.CapabilityOrchestrator, // Extended timeout for nested orchestration + HITL
    InputTypes:  []string{"json"},
    OutputTypes: []string{"json"},
    Handler:     a.handleResearch,
    Internal:    false, // Visible to other agents
    InputSummary: &core.SchemaSummary{ /* same FieldHint pattern as above */ },
})

// Orchestration endpoint stays internal
a.RegisterCapability(core.Capability{
    Name: "orchestrate_natural", Endpoint: "/orchestrate/natural",
    Handler: a.handleNaturalOrchestration, Internal: true,
})
```

Set `Type: core.CapabilityOrchestrator` on capabilities that perform nested orchestration (their own planning → execution → synthesis cycle). The executor uses this to apply an extended per-step timeout (`TRUVAG3_HITL_DEFAULT_TIMEOUT + TRUVAG3_EXECUTION_STEP_TIMEOUT`) so the parent agent does not kill the connection while the child agent waits for HITL approval. Plain tool capabilities should leave `Type` empty — the default `"tool"` behavior applies the standard step timeout.

Capabilities with `Internal: false` enter the orchestrator's tiered tool selection, where only lightweight summaries (not full descriptions) are sent to the LLM. Front-load your `Description` or set an explicit `Summary` field — see [TOOL_DEVELOPMENT_GUIDE.md — Tiered Selection and the Summary Field](TOOL_DEVELOPMENT_GUIDE.md#tiered-selection-and-the-summary-field).

### Writing Effective Capability Descriptions

The LLM uses descriptions to select tools and generate JSON payloads. TruvaG3 uses 3-phase progressive enhancement:

| Phase | Accuracy | What You Provide |
|-------|----------|------------------|
| Phase 1 | ~85-90% | `Description` field (always required) |
| Phase 2 | ~95% | `InputSummary` with `FieldHint` structs (recommended for production) |
| Phase 3 | ~99% | Schema endpoint (optional, auto-generated from Phase 2) |

**Description formula:** `[Action] [what it does]. Required: [fields]. Optional: [fields with defaults].`

**InputSummary** (shown in the non-streaming example above) gives the AI exact field names, types, and examples. FieldHint tips:
- **Name**: Exact JSON field name your handler expects
- **Type**: Standard JSON types: `"string"`, `"number"`, `"boolean"`, `"object"`, `"array"`
- **Example**: Realistic value showing format (e.g., `"2026-04-15"`, `"JFK"`)
- **Description**: Meaning + constraints (e.g., `"0-10"` for replica count)

> **Reference:** [TOOL_SCHEMA_DISCOVERY_GUIDE.md](TOOL_SCHEMA_DISCOVERY_GUIDE.md) for the complete 3-phase approach including Phase 3 JSON Schema validation.

---

## 7. Step 4: Implement Handlers

### Non-Streaming Handler (ProcessRequest)

```go
// handlers.go
package main

import (
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
    "go.opentelemetry.io/otel/attribute"
)

// OrchestrationResponse is the JSON response for non-streaming requests.
type OrchestrationResponse struct {
    RequestID     string                 `json:"request_id"`
    Request       string                 `json:"request"`
    Response      string                 `json:"response"`
    ToolsUsed     []string               `json:"tools_used"`
    ExecutionTime string                 `json:"execution_time"`
    Confidence    float64                `json:"confidence"`
    Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

func (a *YourAgent) handleNaturalOrchestration(w http.ResponseWriter, r *http.Request) {
    startTime := time.Now()
    ctx := r.Context()

    // 1. Add span event for Jaeger visibility
    telemetry.AddSpanEvent(ctx, "request_received",
        attribute.String("operation", "natural_orchestration"),
    )

    // 2. Log with trace context for correlation
    a.Logger.InfoWithContext(ctx, "Processing orchestration request", map[string]interface{}{
        "operation": "natural_orchestration",
        "method":    r.Method,
    })

    // 3. Parse and validate request
    var req struct {
        Request  string                 `json:"request"`
        Metadata map[string]interface{} `json:"metadata,omitempty"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        telemetry.RecordSpanError(ctx, err)
        a.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
            "operation": "natural_orchestration",
            "error":     err.Error(),
        })
        writeError(w, http.StatusBadRequest, "Invalid request format", err)
        return
    }

    if req.Request == "" {
        writeError(w, http.StatusBadRequest, "Request field is required", nil)
        return
    }

    // 4. Check orchestrator availability
    a.mu.RLock()
    orch := a.orchestrator
    a.mu.RUnlock()

    if orch == nil {
        writeError(w, http.StatusServiceUnavailable, "Orchestrator initializing", nil)
        return
    }

    // 5. Process through AI orchestrator
    telemetry.AddSpanEvent(ctx, "orchestration_started",
        attribute.String("request", req.Request),
    )

    result, err := orch.ProcessRequest(ctx, req.Request, req.Metadata)
    if err != nil {
        // Check if the error originated from an AI provider (e.g., 400 Bad Request, 429 Rate Limit).
        // core.ProviderError carries the original HTTP status, provider name, and model —
        // surface the real status instead of masking everything as 500.
        var pe core.ProviderError
        if errors.As(err, &pe) && pe.StatusCode() >= 400 && pe.StatusCode() < 500 {
            a.Logger.WarnWithContext(ctx, "LLM provider returned client error", map[string]interface{}{
                "operation":   "natural_orchestration",
                "error":       pe.Error(),
                "status_code": pe.StatusCode(),
                "provider":    pe.Provider(),
                "model":       pe.Model(),
                "is_transient": pe.IsTransient(),
                "duration_ms": time.Since(startTime).Milliseconds(),
            })
            telemetry.RecordSpanError(ctx, err)
            writeError(w, pe.StatusCode(), pe.Error(), err)
            return
        }

        a.Logger.ErrorWithContext(ctx, "Orchestration failed", map[string]interface{}{
            "operation":   "natural_orchestration",
            "error":       err.Error(),
            "duration_ms": time.Since(startTime).Milliseconds(),
        })
        telemetry.RecordSpanError(ctx, err)
        writeError(w, http.StatusInternalServerError, "Orchestration failed", err)
        return
    }

    // 6. Build and send response
    response := &OrchestrationResponse{
        RequestID:     result.RequestID,
        Request:       req.Request,
        Response:      result.Response,
        ToolsUsed:     result.AgentsInvolved,
        ExecutionTime: time.Since(startTime).String(),
        Confidence:    result.Confidence,
        Metadata:      result.Metadata,
    }

    // 7. Record metrics
    durationMs := float64(time.Since(startTime).Milliseconds())
    telemetry.RecordRequest(telemetry.ModuleOrchestration, "natural_request", durationMs, "success")

    // 8. Add completion span event
    telemetry.AddSpanEvent(ctx, "orchestration_completed",
        attribute.String("request_id", result.RequestID),
        attribute.Int("tools_used", len(result.AgentsInvolved)),
        attribute.Float64("confidence", result.Confidence),
    )

    // 9. Log completion
    a.Logger.InfoWithContext(ctx, "Orchestration completed", map[string]interface{}{
        "operation":   "natural_orchestration",
        "request_id":  result.RequestID,
        "tools_used":  len(result.AgentsInvolved),
        "duration_ms": time.Since(startTime).Milliseconds(),
        "status":      "success",
    })

    writeJSON(w, http.StatusOK, response)
}
```

### Error Propagation: Why This Pattern Matters

The `core.ProviderError` check above is not just cosmetic — it's the final link in a chain that starts inside the AI provider layer and flows through the ChainClient and orchestrator:

```
AI Provider (e.g., OpenAI returns 429 Rate Limit)
  ↓  providerError{StatusCode: 429, Provider: "openai", Model: "o3"}
ChainClient.isClientError() — inspects via errors.As()
  ↓  429 excluded from client errors → tries next provider (Anthropic)
  ↓  Anthropic also fails → error propagates up
Orchestrator — wraps with fmt.Errorf("...: %w", err)
  ↓  ProviderError preserved through %w chain
Your Handler — errors.As(err, &pe) finds the ProviderError
  ↓  Surfaces pe.StatusCode() = 429 as HTTP response
```

**Critical rule: always use `%w` when wrapping errors.** This preserves `core.ProviderError` through the wrapping chain so every layer can inspect it:

```go
// CORRECT — preserves core.ProviderError
return fmt.Errorf("streaming orchestration failed: %w", err)

// WRONG — destroys the type, errors.As() won't find ProviderError
return fmt.Errorf("failed: %s", err)   // string conversion
return errors.New(err.Error())          // new error, original type lost
```

If you break the `%w` chain:
- **ChainClient** can't classify the error → defaults to failover (safe but wastes API calls for true client errors like malformed prompts)
- **Your handler** can't extract the status → returns 500 for what was actually a 400 (misleading to callers, breaks client retry logic)
- **LLM debug store** can't extract provider/model → error records have empty metadata

> **Reference:** [AI_PROVIDERS_SETUP_GUIDE.md — How Errors Propagate Through the Stack](AI_PROVIDERS_SETUP_GUIDE.md#how-errors-propagate-through-the-stack) for the complete error flow diagram.

### Streaming Handler (ProcessWithStreaming)

For streaming agents, the orchestration logic lives in the agent struct (not in the SSE handler), because it needs access to the session store and orchestrator:

```go
// chat_agent.go (continued)

// addConversationHistoryMetadata passes raw turns to orchestration.
// The framework builds <conversation_history> before planning and reuses
// the prepared value across continuation and synthesis phases.
func (a *YourChatAgent) addConversationHistoryMetadata(
    metadata map[string]interface{},
    sessionID string,
    history []Message,
) map[string]interface{} {
    if metadata == nil {
        metadata = make(map[string]interface{})
    }
    if len(history) == 0 {
        return metadata
    }

    turns := make([]core.ConversationTurn, 0, len(history))
    for _, msg := range history {
        turns = append(turns, core.ConversationTurn{
            Role:    msg.Role,
            Content: msg.Content,
        })
    }

    metadata[orchestration.MetadataConversationTurns] = turns
    metadata[orchestration.MetadataConversationSessionKey] = sessionID
    return metadata
}

// ProcessWithStreaming processes a query and streams progress via SSE callback.
func (a *YourChatAgent) ProcessWithStreaming(
    ctx context.Context, sessionID, query string, callback StreamCallback,
) error {
    startTime := time.Now()

    a.mu.RLock()
    orch := a.orchestrator
    a.mu.RUnlock()

    if orch == nil {
        return fmt.Errorf("orchestrator not initialized")
    }

    // 1. Load conversation history for context continuity
    history := a.sessionStore.GetHistory(sessionID)
    metadata := a.addConversationHistoryMetadata(nil, sessionID, history)

    // 2. Log with trace context
    a.Logger.InfoWithContext(ctx, "Processing chat request", map[string]interface{}{
        "operation":     "process_chat",
        "session_id":    sessionID,
        "history_turns": len(history),
    })

    // 3. Send planning status to client
    callback.SendStatus("planning", "Analyzing your request...")

    // 4. Add span event
    telemetry.AddSpanEvent(ctx, "orchestration.started",
        attribute.String("session_id", sessionID),
        attribute.Int("history_turns", len(history)),
    )

    // 5. Attach per-step callback for real-time tool progress
    globalStepCounter := 0
    ctx = orchestration.WithStepCallback(ctx,
        func(stepIndex, totalSteps int, step orchestration.RoutingStep, stepResult orchestration.StepResult) {
            globalStepCounter++
            callback.SendStep(
                fmt.Sprintf("step_%d", globalStepCounter),
                step.AgentName,
                stepResult.Success,
                stepResult.Duration.Milliseconds(),
            )
        },
    )

    // 6. Stream the orchestration response
    result, err := orch.ProcessRequestStreaming(ctx, query, metadata,
        func(chunk core.StreamChunk) error {
            // Phase-complete chunks are progress indicators, not content
            if chunk.Metadata != nil && chunk.Metadata["type"] == "phase_complete" {
                phaseNum, _ := chunk.Metadata["phase"].(int)
                callback.SendStatus("phase_complete",
                    fmt.Sprintf("Phase %d complete. Planning next phase...", phaseNum))
                return nil
            }
            // Forward content chunks to SSE
            if chunk.Content != "" {
                callback.SendChunk(chunk.Content)
            }
            return nil
        },
    )
    if err != nil {
        a.Logger.ErrorWithContext(ctx, "Streaming orchestration failed", map[string]interface{}{
            "operation":   "process_chat",
            "error":       err.Error(),
            "duration_ms": time.Since(startTime).Milliseconds(),
        })
        telemetry.RecordSpanError(ctx, err)
        return fmt.Errorf("streaming orchestration failed: %w", err)
    }

    // 7. Send token usage stats
    if result.Usage != nil {
        callback.SendUsage(
            result.Usage.PromptTokens,
            result.Usage.CompletionTokens,
            result.Usage.TotalTokens,
        )
    }

    // 8. Send finish reason if available
    if result.FinishReason != "" {
        callback.SendFinish(result.FinishReason)
    }

    // 9. Add completion span event
    telemetry.AddSpanEvent(ctx, "orchestration.completed",
        attribute.String("request_id", result.RequestID),
        attribute.Int("agents_used", len(result.AgentsInvolved)),
        attribute.Float64("confidence", result.Confidence),
    )

    // 10. Send completion event
    callback.SendDone(result.RequestID, result.AgentsInvolved,
        time.Since(startTime).Milliseconds())

    // 11. Store assistant response in session history
    a.sessionStore.AddMessage(sessionID, Message{
        Role:      "assistant",
        Content:   result.Response,
        Timestamp: time.Now(),
        Metadata: map[string]interface{}{
            "request_id": result.RequestID,
            "tools_used": result.AgentsInvolved,
        },
    })

    // 12. Log completion
    a.Logger.InfoWithContext(ctx, "Chat request completed", map[string]interface{}{
        "operation":   "process_chat",
        "session_id":  sessionID,
        "request_id":  result.RequestID,
        "tools_used":  len(result.AgentsInvolved),
        "duration_ms": time.Since(startTime).Milliseconds(),
        "status":      "success",
    })

    return nil
}
```

Tier 1 conversation-history protection is automatic once your agent passes raw turns in metadata and creates the orchestrator through `CreateOrchestrator(...)`. If you want Tier 2 recursive compaction, use the Layer 2 helper and inject the resulting preparer:

```go
config := orchestration.DefaultConfig()
preparer, err := orchestration.BuildCompactionEnabledConversationHistoryPreparer(
    config,
    a.AI,
    orchestration.WithConversationSummaryCache(myCache),   // optional override
    orchestration.WithConversationCompactor(myCompactor),  // optional override
)
if err != nil {
    return fmt.Errorf("failed to build conversation history preparer: %w", err)
}

deps := orchestration.OrchestratorDependencies{
    Discovery:                   discovery,
    AIClient:                    a.AI,
    Logger:                      a.Logger,
    Telemetry:                   telemetry.GetTelemetryProvider(),
    ConversationHistoryPreparer: preparer,
}
```

That helper creates the default cache and LLM compactor for you, then applies any supplied overrides last, so you can customize one concern without dropping to full direct construction.

For a dedicated walkthrough of Tier 1 defaults, Tier 2 recursive compaction, and full Layer 3 construction, see [CONVERSATION_HISTORY_GUIDE.md](../memory-and-chat/CONVERSATION_HISTORY_GUIDE.md).

### Health Handler (Common to Both Types)

```go
func (a *YourAgent) handleHealth(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    health := map[string]interface{}{
        "status":    "healthy",
        "timestamp": time.Now().Unix(),
    }

    // Check Redis/Discovery
    if a.Discovery != nil {
        if _, err := a.Discovery.Discover(ctx, core.DiscoveryFilter{}); err != nil {
            health["status"] = "degraded"
            health["redis"] = "unavailable"
            a.Logger.WarnWithContext(ctx, "Health check: Redis unavailable", map[string]interface{}{
                "error": err.Error(),
            })
        } else {
            health["redis"] = "healthy"
        }
    }

    // Check orchestrator
    a.mu.RLock()
    orch := a.orchestrator
    a.mu.RUnlock()

    if orch != nil {
        metrics := orch.GetMetrics()
        health["orchestrator"] = map[string]interface{}{
            "status":              "active",
            "total_requests":      metrics.TotalRequests,
            "successful_requests": metrics.SuccessfulRequests,
        }
    } else {
        health["orchestrator"] = "initializing"
    }

    // Check AI
    if a.AI != nil {
        health["ai_provider"] = "connected"
    } else {
        health["ai_provider"] = "not configured"
    }

    statusCode := http.StatusOK
    if health["status"] == "degraded" || health["status"] == "unhealthy" {
        statusCode = http.StatusServiceUnavailable
    }

    setCORSHeaders(w)
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(statusCode)
    json.NewEncoder(w).Encode(health)
}

// Helper functions

func setCORSHeaders(w http.ResponseWriter) {
    w.Header().Set("Access-Control-Allow-Origin", "*")
    w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-User-ID")
    w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
}

func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
    setCORSHeaders(w)
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(statusCode)
    json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, statusCode int, message string, err error) {
    response := map[string]interface{}{
        "error":   message,
        "status":  statusCode,
        "success": false,
    }
    if err != nil {
        response["details"] = err.Error()
    }
    writeJSON(w, statusCode, response)
}
```

---

## 8. Step 5: Add SSE Streaming (Streaming Agents Only)

### StreamCallback Interface

Define a clean interface for SSE event types:

```go
// sse_handler.go
package main

import (
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
    "go.opentelemetry.io/otel/attribute"
)

// StreamCallback defines the interface for SSE event delivery.
type StreamCallback interface {
    SendStatus(step, message string)
    SendStep(stepID, tool string, success bool, durationMs int64)
    SendChunk(text string)
    SendDone(requestID string, toolsUsed []string, totalDurationMs int64)
    SendError(code, message string, retryable bool)
    SendUsage(promptTokens, completionTokens, totalTokens int)
    SendFinish(reason string)
}
```

### SSECallback Implementation

```go
// SSECallback implements StreamCallback for HTTP SSE responses.
type SSECallback struct {
    w       http.ResponseWriter
    flusher http.Flusher
}

func NewSSECallback(w http.ResponseWriter, flusher http.Flusher) *SSECallback {
    return &SSECallback{w: w, flusher: flusher}
}

func (c *SSECallback) SendStatus(step, message string) {
    c.sendEvent("status", map[string]interface{}{"step": step, "message": message})
}

func (c *SSECallback) SendStep(stepID, tool string, success bool, durationMs int64) {
    c.sendEvent("step", map[string]interface{}{
        "step_id": stepID, "tool": tool, "success": success, "duration_ms": durationMs,
    })
}

func (c *SSECallback) SendChunk(text string) {
    c.sendEvent("chunk", map[string]interface{}{"text": text})
}

func (c *SSECallback) SendDone(requestID string, toolsUsed []string, totalDurationMs int64) {
    c.sendEvent("done", map[string]interface{}{
        "request_id": requestID, "tools_used": toolsUsed, "total_duration_ms": totalDurationMs,
    })
}

func (c *SSECallback) SendError(code, message string, retryable bool) {
    c.sendEvent("error", map[string]interface{}{
        "code": code, "message": message, "retryable": retryable,
    })
}

func (c *SSECallback) SendUsage(promptTokens, completionTokens, totalTokens int) {
    c.sendEvent("usage", map[string]interface{}{
        "prompt_tokens": promptTokens, "completion_tokens": completionTokens, "total_tokens": totalTokens,
    })
}

func (c *SSECallback) SendFinish(reason string) {
    c.sendEvent("finish", map[string]interface{}{"reason": reason})
}

func (c *SSECallback) sendEvent(eventType string, data interface{}) {
    jsonData, err := json.Marshal(data)
    if err != nil {
        return
    }
    fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", eventType, jsonData)
    c.flusher.Flush()
}
```

### SSE Handler

```go
type SSEHandler struct {
    agent *YourChatAgent
}

func NewSSEHandler(agent *YourChatAgent) *SSEHandler {
    return &SSEHandler{agent: agent}
}

type ChatRequest struct {
    SessionID string                 `json:"session_id,omitempty"`
    Message   string                 `json:"message"`
    Options   map[string]interface{} `json:"options,omitempty"`
}

func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // 1. CORS headers
    w.Header().Set("Access-Control-Allow-Origin", "*")
    w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With, X-User-ID")
    w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
    w.Header().Set("Access-Control-Max-Age", "86400")
    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }

    // 2. Verify SSE support
    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "SSE not supported", http.StatusInternalServerError)
        return
    }

    // 3. Set SSE headers
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no") // Disable Nginx buffering

    // 4. Only accept POST
    if r.Method != http.MethodPost {
        callback := NewSSECallback(w, flusher)
        callback.SendError("method_not_allowed", "Only POST supported", false)
        return
    }

    // 5. Parse request
    var req ChatRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        callback := NewSSECallback(w, flusher)
        callback.SendError("invalid_request", "Invalid JSON", false)
        return
    }

    if req.Message == "" {
        callback := NewSSECallback(w, flusher)
        callback.SendError("validation_error", "Message is required", false)
        return
    }

    // 6. Create or retrieve session
    userID := r.Header.Get("X-User-ID")
    sessionID := req.SessionID
    if sessionID == "" {
        session := h.agent.sessionStore.Create(userID, nil)
        sessionID = session.ID

        callback := NewSSECallback(w, flusher)
        callback.sendEvent("session", map[string]interface{}{"id": sessionID})
    }

    // Validate session exists (create new if expired)
    if session := h.agent.sessionStore.Get(sessionID); session == nil {
        session := h.agent.sessionStore.Create(userID, nil)
        sessionID = session.ID

        callback := NewSSECallback(w, flusher)
        callback.sendEvent("session", map[string]interface{}{"id": sessionID})
    }

    // 7. Store user message
    h.agent.sessionStore.AddMessage(sessionID, Message{
        Role:      "user",
        Content:   req.Message,
        Timestamp: time.Now(),
    })

    // 8. Check orchestrator
    if h.agent.GetOrchestrator() == nil {
        callback := NewSSECallback(w, flusher)
        callback.SendError("service_unavailable", "Orchestrator initializing", true)
        return
    }

    // 9. Process with streaming
    callback := NewSSECallback(w, flusher)
    if err := h.agent.ProcessWithStreaming(ctx, sessionID, req.Message, callback); err != nil {
        // Extract core.ProviderError to surface the real error code and retryability.
        // Provider 4xx errors (bad request, rate limit) are NOT retryable by the client.
        // Server errors and transient proxy errors ARE retryable.
        var pe core.ProviderError
        if errors.As(err, &pe) {
            retryable := pe.StatusCode() >= 500 || pe.IsTransient()
            h.agent.Logger.WarnWithContext(ctx, "Stream processing: provider error", map[string]interface{}{
                "operation":   "chat_stream",
                "session_id":  sessionID,
                "error":       pe.Error(),
                "status_code": pe.StatusCode(),
                "provider":    pe.Provider(),
                "retryable":   retryable,
            })
            callback.SendError(fmt.Sprintf("provider_%d", pe.StatusCode()), pe.Error(), retryable)
            return
        }

        h.agent.Logger.ErrorWithContext(ctx, "Stream processing failed", map[string]interface{}{
            "operation":  "chat_stream",
            "session_id": sessionID,
            "error":      err.Error(),
        })
        callback.SendError("processing_failed", err.Error(), true)
    }
}
```

### SSE Event Protocol

The SSE protocol used by TruvaG3 streaming agents:

| Event | When | Data Fields |
|-------|------|-------------|
| `session` | New session created | `id` |
| `status` | Planning/phase transitions | `step`, `message` |
| `step` | Tool call completed | `step_id`, `tool`, `success`, `duration_ms` |
| `chunk` | AI token generated | `text` |
| `usage` | Token usage stats | `prompt_tokens`, `completion_tokens`, `total_tokens` |
| `finish` | AI finish reason | `reason` |
| `done` | Request complete | `request_id`, `tools_used`, `total_duration_ms` |
| `error` | Error occurred | `code`, `message`, `retryable` |

---

## 9. Step 6: Add Session Management (Chat Agents Only)

For streaming chat agents, use a Redis-backed session store to persist conversation history across requests. This is what enables multi-turn conversations where the LLM understands references to previous messages.

### Session and Message Types

```go
// session.go
package main

type Session struct {
    ID        string                 `json:"id"`
    UserID    string                 `json:"user_id"`
    Title     string                 `json:"title"`
    CreatedAt time.Time              `json:"created_at"`
    UpdatedAt time.Time              `json:"updated_at"`
    Messages  []Message              `json:"messages"`
    Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type Message struct {
    ID        string                 `json:"id"`
    Role      string                 `json:"role"` // "user" or "assistant"
    Content   string                 `json:"content"`
    Timestamp time.Time              `json:"timestamp"`
    Metadata  map[string]interface{} `json:"metadata,omitempty"`
}
```

### SessionStore

```go
// SessionStore provides Redis-backed session management.
// Uses Redis DB 2 (core.RedisDBSessions) to isolate from service registry (DB 0).
type SessionStore struct {
    client      *core.RedisClient
    ttl         time.Duration
    maxMessages int
    logger      core.Logger
}

func NewSessionStore(redisURL string, ttl time.Duration, maxMessages int, logger core.Logger) (*SessionStore, error) {
    client, err := core.NewRedisClient(core.RedisClientOptions{
        RedisURL:  redisURL,
        DB:        core.RedisDBSessions, // DB 2 - separate from registry (DB 0)
        Namespace: "truvag3:sessions",
        Logger:    logger,
    })
    if err != nil {
        return nil, fmt.Errorf("failed to create Redis client for sessions: %w", err)
    }
    return &SessionStore{client: client, ttl: ttl, maxMessages: maxMessages, logger: logger}, nil
}
```

### Key SessionStore Methods

| Method | Purpose |
|--------|---------|
| `Create(userID, metadata)` | Create new session, add to user index |
| `Get(sessionID)` | Retrieve session by ID |
| `AddMessage(sessionID, msg)` | Append message, auto-title from first user message, trim to sliding window |
| `GetHistory(sessionID)` | Get full conversation history for context |
| `List(userID, offset, limit)` | Paginated session list with lazy cleanup of expired entries |
| `UpdateTitle(sessionID, title)` | Update session title |
| `Delete(sessionID)` | Remove session and index entry |

**Key design points:** Redis DB 2 (separate from registry DB 0), sliding window of 50 messages, auto-titling from first user message (57 chars + "..."). See `examples/travel-chat-agent/session.go` for complete implementation.

---

## 10. Step 7: Create the Main Entry Point

The `main.go` follows a precise initialization order that's critical for telemetry and orchestrator setup.

### Complete main.go

The numbered comments below show the **critical initialization order** — telemetry must come before agent creation (for AI spans), and the orchestrator must init in background (after `framework.Run()` enables Discovery).

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "log"
    "os"
    "os/signal"
    "strconv"
    "syscall"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"

    // Import AI providers for auto-detection
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
    // 1. Validate configuration (fail fast)
    if err := validateConfig(); err != nil {
        log.Fatalf("Configuration error: %v", err)
    }

    // 2. Set component type for telemetry service_type labels
    core.SetCurrentComponentType(core.ComponentTypeAgent)

    // 3. Initialize telemetry BEFORE creating agent (critical for AI spans)
    initTelemetry("your-agent")
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        telemetry.Shutdown(ctx)
    }()

    // 4. Create agent AFTER telemetry
    agent, err := NewYourAgent()
    if err != nil {
        log.Fatalf("Failed to create agent: %v", err)
    }

    // 5. Create framework
    middlewareConfig := &telemetry.TracingMiddlewareConfig{
        ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live"},
    }

    framework, err := core.NewFramework(agent,
        core.WithName("your-agent"),
        core.WithPort(getPort()),
        core.WithNamespace(os.Getenv("NAMESPACE")),
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),
        core.WithCORS([]string{"*"}, true),
        core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("your-agent", middlewareConfig)),
    )
    if err != nil {
        log.Fatalf("Failed to create framework: %v", err)
    }

    // 6. Initialize orchestrator in background
    go func() {
        startTime := time.Now()
        lastWarning := time.Time{}

        for agent.BaseAgent.Discovery == nil {
            time.Sleep(100 * time.Millisecond)

            elapsed := time.Since(startTime)
            if elapsed > 30*time.Second && time.Since(lastWarning) > 60*time.Second {
                if lastWarning.IsZero() {
                    agent.Logger.Warn("Discovery not available after 30s", map[string]interface{}{
                        "hint": "check Redis connectivity (REDIS_URL)",
                    })
                }
                lastWarning = time.Now()
            }
        }

        if err := agent.InitializeOrchestrator(agent.BaseAgent.Discovery); err != nil {
            agent.Logger.Warn("Failed to initialize orchestrator", map[string]interface{}{
                "error": err.Error(),
            })
        } else {
            agent.Logger.Info("Orchestrator initialized successfully", nil)
        }
    }()

    // 7. Graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigChan
        log.Println("Shutting down gracefully...")
        cancel()
    }()

    // 8. Run framework (blocking)
    log.Printf("Starting agent on port %d", getPort())
    if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
        log.Fatalf("Framework error: %v", err)
    }
}

// validateConfig(), initTelemetry(), getPort() — standard helpers.
// See examples/travel-chat-agent/main.go for complete implementations.
```

### Background Jobs: `core.Runnable` and `framework.RegisterRunnable`

Some agents need to do work in the background, parallel to serving HTTP requests — periodic tasks, queue consumers, expiry processors, scheduled jobs. Don't hand-roll a goroutine in `main.go` and try to manage its lifecycle by hand. Use `core.Runnable` instead.

A `Runnable` is anything that satisfies a single-method interface ([core/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/core/interfaces.go)):

```go
type Runnable interface {
    Start(ctx context.Context) error
}
```

Implement `Start(ctx)`, register the instance with the framework, and the framework starts it in a goroutine alongside the HTTP server, drains it on shutdown, and emits structured lifecycle logs under the `framework_register_runnable`, `framework_runnable_start`, `framework_runnable_exit`, and `framework_runnable_drain` operations (see [LOGGING_IMPLEMENTATION_GUIDE.md](../observability/LOGGING_IMPLEMENTATION_GUIDE.md)).

**Minimal example — a periodic background job:**

```go
type MyJob struct {
    interval time.Duration
    logger   core.Logger
}

func (j *MyJob) Start(ctx context.Context) error {
    ticker := time.NewTicker(j.interval)
    defer ticker.Stop()
    j.logger.Info("MyJob started", map[string]interface{}{
        "operation": "my_job",
        "interval":  j.interval.String(),
    })
    for {
        select {
        case <-ticker.C:
            // Do work. Log errors internally; don't propagate them
            // unless they're truly fatal — fail-open keeps the job running.
            _ = j.runOnce(ctx)
        case <-ctx.Done():
            j.logger.Info("MyJob stopping (context cancelled)", map[string]interface{}{
                "operation": "my_job",
            })
            return nil
        }
    }
}
```

**Wire it up in `main.go` after creating the framework but before `framework.Run`:**

```go
job := &MyJob{interval: 1 * time.Hour, logger: agent.Logger}
framework.RegisterRunnable(job)

// framework.Run now starts the HTTP server AND the runnable in parallel,
// and drains the runnable on ctx cancel.
if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
    log.Fatalf("Framework error: %v", err)
}
```

That's the entire wiring. **Three lines instead of the goroutine + waitgroup + defer-stop dance you'd otherwise write.** The framework handles all of:

- Starting the runnable in a goroutine
- Logging when it starts and when it exits
- Distinguishing clean exit (`nil` or `context.Canceled`) from runtime error (everything else, logged at ERROR with `error_type=runnable_exit`)
- Draining all registered runnables after the HTTP server stops, with a configurable timeout
- Logging a warning if the drain timeout fires (`error_type=runnable_drain_timeout`)

**The contract you must honour:**

1. **`Start(ctx)` blocks until ctx is cancelled.** This is the entire interface — no `Stop()`, no `Cancel()`, no companion methods. ctx cancellation drives shutdown.
2. **You must respond to `ctx.Done()` within `TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT`** (default `10s`, raise via env var if your job needs a longer grace period). Buggy runnables that ignore ctx will leak goroutines until process exit — Go provides no mechanism for forcibly terminating them.
3. **Return `nil` on graceful shutdown, return an error on startup or runtime failure.** `context.Canceled` is treated as clean exit.
4. **Don't call `Start` yourself.** The framework calls it exactly once per registered runnable when `Run(ctx)` is invoked.
5. **Register before `Run`.** Calling `RegisterRunnable` after `Run` has started is undefined behaviour.

**Reference implementation:** [`memory.ReflectionJob`](https://github.com/truvaagents/truva-g3/blob/main/memory/reflection_job.go) is the in-tree reference `core.Runnable` — a periodic background job that distills episodic events into long-term knowledge. It's registered in the `devops-chat-agent`, `qa-agent`, and `event-driven-agent` examples (the `event-driven-agent` also shows the worker-mode pattern where the runnable is started in a goroutine tied directly to the worker context, since worker mode has no `core.Framework` instance). See [AGENT_MEMORY_USER_GUIDE.md — Long-Term Knowledge Retention](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md#long-term-knowledge-retention-the-reflection-job) for the conceptual overview and [examples/devops-chat-agent/main.go](https://github.com/truvaagents/truva-g3/blob/main/examples/devops-chat-agent/main.go) for the production wiring.

**Use cases that should be a `Runnable`:**

- Periodic jobs (every interval, do X)
- Queue consumers (block on a channel or external queue, process messages)
- Schedulers (look up the next due item, sleep until then, run it)
- Expiry processors (sweep expired entries from a store)
- Lease renewal loops
- Health-driven reconciliation loops

**Use cases that should NOT be a Runnable:**

- Per-request work — use a handler
- Async work spawned by a request — use a goroutine inside the handler with `context.Background()` if it must outlive the request
- One-shot startup tasks — call them synchronously before `framework.Run`
- One-shot shutdown tasks — defer them in `main.go`

### Receiving Scheduled Tasks

If your agent needs to receive tasks scheduled for the future (e.g., "check this service again in 1 hour"), wire the scheduled endpoint **before** `framework.Run()`:

```go
// Lazy orchestrator resolution — supports async init
if err := orchestration.RegisterScheduledEndpoint(agent.BaseAgent, func() orchestration.Orchestrator {
    if o := agent.GetOrchestrator(); o != nil {
        return o
    }
    return nil
}); err != nil {
    agent.Logger.Warn("Failed to register scheduled endpoint", map[string]interface{}{"error": err.Error()})
}
```

This mounts `/api/v1/scheduled` on your agent. When a scheduled task fires, the centralized `scheduled-executor` POSTs to this endpoint with the instruction, and your agent's orchestrator plans and executes it using whatever tools are available.

> **Note**: This is different from Runnables. A Runnable is a background job your agent owns end-to-end (like a periodic sweep). Receiving scheduled tasks is an HTTP endpoint where work arrives from the external `scheduled-executor` service. Use Runnables for agent-internal periodic work; use `RegisterScheduledEndpoint` for receiving externally-scheduled tasks.

For the full story on scheduling -- architecture, delivery semantics, observability, troubleshooting, and writing custom backends -- see the [Scheduled Tasks Guide](../orchestration/SCHEDULED_TASKS_GUIDE.md).

---

## 11. Step 8: Add Deployment Files

### go.mod

Agents depend on four framework modules: `ai`, `core`, `orchestration`, and `telemetry`.

```go
module github.com/truvaagents/truva-g3/examples/your-agent

go 1.26.2

require (
    github.com/google/uuid v1.6.0
    github.com/truvaagents/truva-g3/ai v0.9.1
    github.com/truvaagents/truva-g3/core v0.9.1
    github.com/truvaagents/truva-g3/orchestration v0.9.1
    github.com/truvaagents/truva-g3/telemetry v0.9.1
    go.opentelemetry.io/otel v1.38.0
)

// Use local workspace modules for development
replace (
    github.com/truvaagents/truva-g3/ai => ../../ai
    github.com/truvaagents/truva-g3/core => ../../core
    github.com/truvaagents/truva-g3/orchestration => ../../orchestration
    github.com/truvaagents/truva-g3/resilience => ../../resilience
    github.com/truvaagents/truva-g3/telemetry => ../../telemetry
)
```

### .env

```bash
# Required: Redis for service discovery and session storage
REDIS_URL=redis://localhost:6379

# Required: HTTP server port (see examples/README.md for port allocation)
PORT=8357

# AI Provider (auto-detected — set at least one)
OPENAI_API_KEY=sk-...
# ANTHROPIC_API_KEY=sk-ant-...

# Optional: Model alias overrides (pattern: TRUVAG3_{PROVIDER}_MODEL_{ALIAS})
# These override the portable model aliases (default, fast, smart, premium, code, vision)
# without requiring code changes. See AI_PROVIDERS_SETUP_GUIDE.md for full reference.
# TRUVAG3_OPENAI_MODEL_DEFAULT=gpt-4.1-2025-04-14
# TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1         # Use cheaper model in dev instead of o3
# TRUVAG3_ANTHROPIC_MODEL_DEFAULT=claude-sonnet-4-6

# Telemetry
APP_ENV=development
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318

# Debug
DEV_MODE=true
TRUVAG3_LOG_LEVEL=debug
```

### Streaming Chat Agent .env Checklist

Streaming chat agents that serve a frontend (e.g., `chat-ui`) require additional environment variables beyond the basics. Use this checklist when creating a new agent to avoid missing configuration:

| Variable | Required | Default | Purpose |
|----------|----------|---------|---------|
| `REDIS_URL` | Yes | — | Service discovery and session storage |
| `PORT` | Yes | — | HTTP server port ([port allocation](https://github.com/truvaagents/truva-g3/blob/main/examples/README.md)) |
| AI provider key(s) | Yes (at least one) | — | `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GROQ_API_KEY`, etc. |
| `TRUVAG3_CORS_HEADERS` | **Yes** (chat agents) | `Content-Type` | Must include `X-User-ID` for chat-ui: `Content-Type,Authorization,X-User-ID,X-Requested-With` |
| `TRUVAG3_SYNTHESIS_MAX_TOKENS` | Recommended | `5000` | Max output tokens for LLM synthesis. Chat agents typically need `10000` for detailed responses |
| `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED` | Recommended | `false` | Stores orchestration DAGs in Redis DB 8 for [Registry Viewer](https://github.com/truvaagents/truva-g3/tree/main/examples/registry-viewer-app) inspection |
| `TRUVAG3_LLM_DEBUG_ENABLED` | Recommended | `false` | Stores LLM request/response payloads for debugging |
| `DEV_MODE` | No | `false` | Enables verbose logging |
| `TRUVAG3_LOG_LEVEL` | No | `info` | Log verbosity: `debug`, `info`, `warn`, `error` |
| `TRUVAG3_DEBUG` | No | `false` | Enables debug mode (sets log level to `debug`) |
| `APP_ENV` | No | `development` | Telemetry profile: `development`, `staging`, `production` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | No | — | OpenTelemetry collector for tracing/metrics |
| `TRUVAG3_{PROVIDER}_MODEL_{ALIAS}` | No | Provider default | Model alias overrides (e.g., `TRUVAG3_OPENAI_MODEL_DEFAULT=gpt-4.1-2025-04-14`) |

**Common mistakes:**
- Missing `TRUVAG3_CORS_HEADERS` — the chat-ui frontend sends `X-User-ID` via a custom header; without this setting, CORS blocks the request silently
- Low `TRUVAG3_SYNTHESIS_MAX_TOKENS` — the default of 5000 tokens is fine for simple tools but chat agents with multi-step orchestration often need 10000+ for complete responses
- Missing `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED` — without this, the Registry Viewer's "Execution DAG" tab shows no data, making orchestration debugging harder

### Dockerfile.workspace, k8-deployment.yaml, setup.sh

Follow the same patterns as tools. See [TOOL_DEVELOPMENT_GUIDE.md - Step 6: Add Deployment Files](TOOL_DEVELOPMENT_GUIDE.md#8-step-6-add-deployment-files) for templates — including the **Observability Identity** subsection that covers the three-way alignment rule between pod `app:` label, `OTEL_SERVICE_NAME`, and the logger's service field. Agents are subject to the same rule.

Key differences for agents:

| Aspect | Tool | Agent |
|--------|------|-------|
| COPY in Dockerfile | `core/`, `telemetry/` | `ai/`, `core/`, `orchestration/`, `resilience/`, `telemetry/` |
| K8s `component` label | `tool` | `agent` |
| K8s resource limits | Lower (50m-100m CPU) | Higher (100m-500m CPU) — AI calls are compute-intensive |
| Secrets | Tool-specific API keys | AI provider API keys |

---

## 12. Logging and Observability

### The Logger Interface

Every agent gets a `Logger` from `core.BaseAgent`. It provides both basic and context-aware methods:

```go
a.Logger.InfoWithContext(ctx, "message", fields)  // In handlers (includes trace_id, span_id)
a.Logger.Info("message", fields)                   // Startup/shutdown only (no request context)
// Also: Warn, Error, Debug — each has a WithContext variant
```

### When to Use Each Level

| Level | When | Example |
|-------|------|---------|
| `Debug` | Detailed internal state, request payloads | `"Request payload"`, `"Streaming stats"` |
| `Info` | Normal operations, request lifecycle | `"Processing request"`, `"Request completed"` |
| `Warn` | Recoverable issues, degraded operation | `"Discovery not available"`, `"AI fallback activated"` |
| `Error` | Failures requiring attention | `"Orchestration failed"`, `"Redis unavailable"` |

### Standard Log Fields

Use consistent field names across all handlers:

| Field | Type | When | Description |
|-------|------|------|-------------|
| `operation` | string | Every log | The capability name (e.g., `"process_chat"`, `"natural_orchestration"`) |
| `session_id` | string | Chat agents | Session identifier |
| `request_id` | string | After orchestration | Orchestrator-generated request ID |
| `status` | string | Completion | `"success"` or `"failure"` |
| `duration_ms` | int64 | Completion | Operation duration in milliseconds |
| `error` | string | Error logs | Error message |
| `tools_used` | int/[]string | Completion | Number or list of tools invoked |

### Context-Aware Logging Pattern

**Always use `WithContext` in handlers** for automatic trace-log correlation:

```go
// GOOD — trace_id and span_id are automatically injected
a.Logger.InfoWithContext(ctx, "Processing request", map[string]interface{}{
    "operation":  "process_chat",
    "session_id": sessionID,
})

// BAD — loses trace correlation
a.Logger.Info("Processing request", map[string]interface{}{
    "operation":  "process_chat",
    "session_id": sessionID,
})
```

When logs include trace context, you can search in your log aggregator (Loki, CloudWatch) by `trace_id` and jump directly to the corresponding Jaeger trace.

### Environment Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `TRUVAG3_LOG_FORMAT` | `json` | Output format: `json` or `text` (auto-detects `json` in K8s) |
| `TRUVAG3_DEBUG` | `false` | Enable debug mode (sets level to `debug`) |

### Key Rules

1. **`WithContext` in handlers**, basic methods in startup/shutdown only
2. **Always include `operation` field** — your primary filter key
3. **Never log API keys** — use `"has_key": apiKey != ""` instead
4. **Log both success and failure** with `duration_ms`
5. **Init telemetry before agent** — otherwise AI calls produce silent logs
6. **`log.Fatalf` only before framework** — use `agent.Logger` after `core.NewFramework()`

Logs auto-include a `component` field (`agent/<name>`, `framework/orchestration`, `framework/ai`) for filtering. With telemetry enabled, logs auto-emit Prometheus metrics using `operation`, `status`, `error_type` as labels.

> **Reference:** [LOGGING_IMPLEMENTATION_GUIDE.md](../observability/LOGGING_IMPLEMENTATION_GUIDE.md) for component filtering, metric emission, HITL tracing, and production recipes.

---

## 13. Distributed Tracing

Tracing is handled by three layers — all already shown in the code above:

1. **Server-side** — `TracingMiddlewareWithConfig` in `main.go` extracts/creates trace context
2. **Client-side** — `TracedHTTPClientWithTransport` propagates trace context to downstream tools
3. **Manual enrichment** — `telemetry.AddSpanEvent()` and `telemetry.RecordSpanError()` add business context to spans

**Key APIs for handlers:**

```go
// Add span events at key lifecycle points
telemetry.AddSpanEvent(ctx, "orchestration.started", attribute.String("session_id", sessionID))

// Record errors on spans (appears as red in Jaeger)
telemetry.RecordSpanError(ctx, err)

// Record metrics for dashboards
telemetry.RecordRequest(telemetry.ModuleOrchestration, "natural_request", durationMs, "success")
```

**Critical init order** (already shown in `main.go`): `SetCurrentComponentType` → `initTelemetry` → `NewAgent`. If reversed, AI calls won't generate spans.

> **Reference:** [DISTRIBUTED_TRACING_GUIDE.md](../observability/DISTRIBUTED_TRACING_GUIDE.md) for Jaeger setup, cross-service correlation, span event patterns, and troubleshooting.

---

## 14. Testing Your Agent

### Prerequisites

```bash
# 1. Start Redis
docker run -d -p 6379:6379 redis:alpine

# 2. Start Jaeger (optional, for tracing)
docker run -d --name jaeger \
  -p 16686:16686 -p 4318:4318 \
  -e COLLECTOR_OTLP_ENABLED=true \
  jaegertracing/all-in-one:latest

# 3. Deploy tools your agent will orchestrate
# (e.g., weather-tool, currency-tool, devops-tool)
```

### Build and Run

```bash
# Using setup.sh (recommended)
./setup.sh run

# Or manually
cd examples/your-agent
set -a && source .env && set +a
GOWORK=off go build -o your-agent .
./your-agent
```

### Test Non-Streaming Agent

```bash
# Health check
curl http://localhost:8357/health | jq .

# Discover tools
curl http://localhost:8357/discover | jq .

# Natural language orchestration
curl -X POST http://localhost:8357/orchestrate/natural \
  -H "Content-Type: application/json" \
  -d '{"request": "What is the cluster status?"}'
```

### Test Streaming Agent

```bash
# Create session
curl -X POST http://localhost:8357/chat/session \
  -H "Content-Type: application/json" \
  -H "X-User-ID: test-user" | jq .

# SSE streaming chat
curl -N -X POST http://localhost:8357/chat/stream \
  -H "Content-Type: application/json" \
  -H "X-User-ID: test-user" \
  -d '{"message": "What pods are running in the default namespace?"}'

# Get conversation history
curl http://localhost:8357/chat/session/{session_id}/history | jq .
```

### Viewing Traces

Open Jaeger at `http://localhost:16686`, select your agent's service name, and search by operation (e.g., `POST /chat/stream`) or `request_id` from the API response.

---

## 15. Adding Human-in-the-Loop (HITL) Approval

If your agent performs high-stakes operations (deployments, ticket creation, sending messages, financial transactions), you should add HITL checkpoints that pause execution and wait for human approval before proceeding.

### 15.1 When to Use HITL

Add HITL approval when your agent's tools can:
- **Modify external systems** — Kubernetes rollouts, database writes, API calls with side effects
- **Send communications** — Slack messages, emails, ticket creation
- **Incur costs** — Cloud resource provisioning, paid API calls
- **Make irreversible changes** — Data deletion, production deployments

Mark these capabilities as sensitive in your `.env`:

```bash
TRUVAG3_HITL_ENABLED=true
TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=rollout_restart,scale_deployment,send_message,create_ticket

# Required when multiple HITL-enabled agents share the same Redis instance.
# Without it, agents share a single pending-checkpoint index and the registry
# viewer's HITL list will show duplicate rows. K8s manifests set this via the
# Deployment env; for local dev with more than one HITL agent, set it explicitly.
# See HUMAN_IN_THE_LOOP_USER_GUIDE.md#agent-isolation for the full mechanism.
TRUVAG3_AGENT_NAME=my-agent
```

The orchestrator will automatically pause before executing any step that uses a sensitive capability, creating a checkpoint that stores the full execution state (plan, completed steps, resolved parameters). At construction, the framework logs a startup `WARN HITL checkpoint store using shared key prefix` if no isolating identifier was supplied — treat that log line as a configuration error, not noise.

### 15.2 Resume Context: The Critical Contract

When a human approves a checkpoint, your resume handler must rebuild the orchestrator's context from the stored checkpoint. The framework provides `BuildResumeContext` for this — **always use it**:

```go
func (a *MyAgent) handleResume(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    checkpointID := extractCheckpointID(r)

    // 1. Load checkpoint from store
    checkpoint, err := a.checkpointStore.LoadCheckpoint(ctx, checkpointID)
    if err != nil {
        http.Error(w, "Checkpoint not found", http.StatusNotFound)
        return
    }

    // 2. Build resume context — single call handles the full contract
    resumeCtx, err := orchestration.BuildResumeContext(ctx, checkpoint)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    // 3. Re-enter the orchestrator with the original request
    result, err := a.orchestrator.ProcessRequest(resumeCtx, checkpoint.OriginalRequest, checkpoint.UserContext)
    // ...
}
```

`BuildResumeContext` ([hitl_helpers.go:141](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_helpers.go#L141)) sets up the full resume contract in a single call:

| What it sets | Why it matters |
|---|---|
| `WithResumeMode(ctx, checkpointID)` | Prevents re-triggering the same checkpoint |
| `WithPlanOverride(ctx, plan)` | Reuses the stored plan (step IDs must match for skip logic) |
| `WithCompletedSteps(ctx, results)` | Skips already-executed steps |
| `WithPreResolvedParams(ctx, params, stepID)` | Uses the human-approved parameter values |
| `WithRequestMode(ctx, mode)` | Preserves streaming/non-streaming expiry behavior |
| `WithMetadata(ctx, userContext)` | Preserves session metadata (session ID, user info, etc.) |

It also validates that the checkpoint has a resumable status (`approved`, `edited`, or `expired_approved`), returning an error for terminal or pending checkpoints.

### 15.3 Common Pitfall: Manual Context Setup

Do **not** manually call individual `With*` helpers in your resume handler:

```go
// BAD — fragile, will drift as the framework evolves
ctx = orchestration.WithResumeMode(ctx, checkpointID)
// Missing WithPlanOverride → orchestrator replans from scratch
// Missing WithCompletedSteps → orchestrator re-executes already-completed steps
// Missing WithPreResolvedParams → human-approved parameter values are ignored
// Missing WithRequestMode, WithMetadata → expiry behavior and session context lost
```

The orchestrator's `executePhaseLoop` checks `GetPlanOverride(ctx)` and `GetCompletedSteps(ctx)` to restore accumulated phase state. If `WithPlanOverride` is absent, the orchestrator triggers a fresh LLM planning call and generates a new plan with different step IDs. If `WithCompletedSteps` is absent, the executor sees no previously completed work and re-runs every step. Either omission causes a full or partial replay of work that already ran. Missing `WithPreResolvedParams` is subtler — the approved parameter values are silently dropped and the orchestrator substitutes AI-generated values instead.

This is a **context drift** problem: as the framework grows the resume contract (from 3 to 6 context values), any manual `With*` call list becomes stale without a compiler error or test failure to warn you. `BuildResumeContext` is the single source of truth and automatically applies the full contract as it evolves.

> **Deep dive:** For the complete HITL setup — checkpoint stores, expiry callbacks, SSE streaming integration, status lifecycle, and configuration reference — see [HUMAN_IN_THE_LOOP_USER_GUIDE.md](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md).
>
> **Reference implementations:**
> - [examples/agent-with-human-approval](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-human-approval) — Streaming SSE agent with approval checkpoints and auto-resume timeouts
> - [examples/event-driven-agent](https://github.com/truvaagents/truva-g3/tree/main/examples/event-driven-agent) — Event-driven async agent with webhook ingestion and HITL approval

---

## 16. Best Practices

1. **Orchestration endpoints `Internal: true`** — `/orchestrate/*` and `/chat/stream` must be internal to prevent recursive self-calls. Domain-specific endpoints may use `Internal: false` for multi-agent hierarchies
2. **Invest in PromptConfig** — `SystemInstructions` and `CustomInstructions` are what make your agent smart
3. **Deferred orchestrator init** — always via background goroutine after Discovery is available
4. **Check orchestrator availability** before processing — return 503 if still initializing
5. **Surface AI provider errors correctly** — Use `errors.As(err, &pe)` with `core.ProviderError` to extract the real HTTP status from AI provider errors. Return the original status code (e.g., 400, 429) instead of wrapping everything as 500. See the handler example in [Step 4](#7-step-4-implement-handlers)
6. **Extended AI timeouts** — 240s+ for reasoning models; cap session history at ~50 messages
7. **Never log API keys** — use env vars for all credentials, restrict CORS in production
8. **Redis DB isolation** — sessions use DB 2, registry uses DB 0

---

## 17. Troubleshooting

| Problem | Symptoms | Check |
|---------|----------|-------|
| **Orchestrator not initializing** | Health shows `"orchestrator": "initializing"` | Redis connectivity (`redis-cli ping`), `REDIS_URL` format, AI provider key |
| **Recursive self-calls** | Agent calling its own endpoints | All orchestration capabilities must be `Internal: true` |
| **AI 4xx errors returned as 500** | Client sees 500 for provider bad-request/rate-limit | Add `core.ProviderError` check with `errors.As()` before generic 500 — see [Step 4](#7-step-4-implement-handlers) |
| **No AI providers** | `"no providers detected"` on startup | API key env vars set, blank import present (`_ ".../ai/providers/openai"`) |
| **Wrong model** | Logs show unexpected model name | Check `env \| grep TRUVAG3_` for alias overrides |
| **No traces in Jaeger** | Requests succeed, no traces | `OTEL_EXPORTER_OTLP_ENDPOINT` set, init order correct, `TracingMiddleware` added |
| **Missing trace_id in logs** | Logs lack `trace_id`/`span_id` | Use `WithContext` methods, `ctx` from `r.Context()` |
| **SSE events not arriving** | Connection opens, no events | `http.Flusher` supported, `X-Accel-Buffering: no` for Nginx, `flusher.Flush()` called |

> **See also:** [AI_PROVIDERS_SETUP_GUIDE.md](AI_PROVIDERS_SETUP_GUIDE.md) for AI provider troubleshooting, [DISTRIBUTED_TRACING_GUIDE.md](../observability/DISTRIBUTED_TRACING_GUIDE.md) for trace debugging.

---

## 18. Quick Reference

### Minimal Non-Streaming Agent

```go
package main

import (
    "context"
    "encoding/json"
    "log"
    "net/http"
    "os"

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration"
    "github.com/truvaagents/truva-g3/telemetry"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

type MinimalAgent struct {
    *core.BaseAgent
    orchestrator *orchestration.AIOrchestrator
}

func main() {
    core.SetCurrentComponentType(core.ComponentTypeAgent)
    telemetry.Initialize(telemetry.UseProfile(telemetry.ProfileDevelopment))
    defer telemetry.Shutdown(context.Background())

    agent := &MinimalAgent{BaseAgent: core.NewBaseAgent("minimal-agent")}
    agent.AI, _ = ai.NewClient()

    agent.RegisterCapability(core.Capability{
        Name:     "orchestrate",
        Endpoint: "/orchestrate",
        Internal: true,
        Handler: func(w http.ResponseWriter, r *http.Request) {
            var req struct{ Query string `json:"query"` }
            json.NewDecoder(r.Body).Decode(&req)

            result, err := agent.orchestrator.ProcessRequest(r.Context(), req.Query, nil)
            if err != nil {
                http.Error(w, err.Error(), 500)
                return
            }

            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(result)
        },
    })

    framework, _ := core.NewFramework(agent,
        core.WithPort(8357),
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),
    )

    go func() {
        for agent.Discovery == nil {
            // wait
        }
        config := orchestration.DefaultConfig()
        deps := orchestration.OrchestratorDependencies{
            Discovery: agent.Discovery, AIClient: agent.AI,
        }
        agent.orchestrator, _ = orchestration.CreateOrchestrator(config, deps)
        agent.orchestrator.Start(context.Background())
    }()

    framework.Run(context.Background())
}
```

### Environment Variables Reference

| Variable | Required | Description |
|----------|----------|-------------|
| `REDIS_URL` | Yes | Redis connection URL |
| `PORT` | Yes | HTTP server port |
| `OPENAI_API_KEY` | One AI key required | OpenAI API key |
| `ANTHROPIC_API_KEY` | One AI key required | Anthropic API key |
| `TRUVAG3_CORS_HEADERS` | Streaming agents | Allowed CORS headers (include `X-User-ID` for chat-ui) |
| `TRUVAG3_SYNTHESIS_MAX_TOKENS` | No | Max tokens for synthesis (default: 5000, chat agents: 10000) |
| `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED` | No | Store orchestration DAGs in Redis DB 8 for Registry Viewer |
| `TRUVAG3_LLM_DEBUG_ENABLED` | No | Store LLM request/response payloads for debugging |
| `DEV_MODE` | No | Enable verbose logging |
| `TRUVAG3_LOG_LEVEL` | No | `debug`, `info`, `warn`, `error` |
| `TRUVAG3_DEBUG` | No | Enable debug mode (sets level to `debug`) |
| `APP_ENV` | No | `development`, `staging`, `production` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | No | OpenTelemetry collector endpoint |
| `TRUVAG3_PLAN_MAX_TOKENS` | No | Max tokens for planning (default: 15000) |
| `TRUVAG3_{PROVIDER}_MODEL_{ALIAS}` | No | Override model alias (e.g., `TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1`) |

For additional AI provider keys (`GROQ_API_KEY`, `DEEPSEEK_API_KEY`, `GEMINI_API_KEY`), base URL overrides, and Ollama setup, see [AI_PROVIDERS_SETUP_GUIDE.md](AI_PROVIDERS_SETUP_GUIDE.md). For the full streaming chat agent .env checklist, see [Step 8: Streaming Chat Agent .env Checklist](#streaming-chat-agent-env-checklist).

---

## See Also

### Core Documentation
- [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md) - Orchestration module reference
- [docs/orchestration/LLM_PLANNING_PROMPT_GUIDE.md](../orchestration/LLM_PLANNING_PROMPT_GUIDE.md) - PromptConfig deep dive
- [TOOL_DEVELOPMENT_GUIDE.md](TOOL_DEVELOPMENT_GUIDE.md) - Tool development (counterpart to this guide)
- [TOOL_SCHEMA_DISCOVERY_GUIDE.md](TOOL_SCHEMA_DISCOVERY_GUIDE.md) - 3-phase AI payload generation (descriptions, field hints, schema validation)
- [AI_PROVIDERS_SETUP_GUIDE.md](AI_PROVIDERS_SETUP_GUIDE.md) - Provider aliases, model aliases, failover behavior, K8s secrets/ConfigMaps, operational scenarios

### Advanced Features
- [HUMAN_IN_THE_LOOP_USER_GUIDE.md](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md) - Complete HITL guide: checkpoint stores, expiry callbacks, `BuildResumeContext`, status lifecycle, and configuration
- [ASYNC_ORCHESTRATION_GUIDE.md](../orchestration/ASYNC_ORCHESTRATION_GUIDE.md) - Async task system: HTTP 202 + polling, worker pools, task handlers, progress reporting, HITL integration

### Telemetry & Observability
- [DISTRIBUTED_TRACING_GUIDE.md](../observability/DISTRIBUTED_TRACING_GUIDE.md) - Complete distributed tracing guide
- [LOGGING_IMPLEMENTATION_GUIDE.md](../observability/LOGGING_IMPLEMENTATION_GUIDE.md) - Logging patterns and standards
- [telemetry/README.md](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md) - Telemetry module API reference

### Reference Implementations

| Agent | Type | Key Features |
|-------|------|--------------|
| [examples/travel-chat-agent](https://github.com/truvaagents/truva-g3/tree/main/examples/travel-chat-agent) | Streaming | SSE streaming, session management, conversation history, multi-phase planning |
| [examples/agent-with-orchestration](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-orchestration) | Non-streaming | Natural language + workflow orchestration, predefined DAGs, AI synthesis toggle |
| [examples/agent-with-human-approval](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-human-approval) | Streaming + HITL | Human approval checkpoints, auto-resume timeouts, approval audit trail |
| [examples/event-driven-agent](https://github.com/truvaagents/truva-g3/tree/main/examples/event-driven-agent) | Async + HITL | Webhook ingestion, alert dedup, async task queue, HITL approval for high-stakes ops |
