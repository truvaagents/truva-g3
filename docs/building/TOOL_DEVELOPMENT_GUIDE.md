# TruvaG3 Tool Development Guide

This guide provides a comprehensive, step-by-step tutorial for developing tools in the TruvaG3 framework. Tools are passive components that expose capabilities to agents and can interact with external APIs to provide real-world functionality.

## Table of Contents

1. [Understanding Tools in TruvaG3](#1-understanding-tools-in-truvag3)
2. [Project Structure](#2-project-structure)
3. [Step 1: Create the Tool Struct](#3-step-1-create-the-tool-struct)
4. [Step 2: Implement External API Client](#4-step-2-implement-external-api-client)
5. [Step 3: Register Capabilities](#5-step-3-register-capabilities)
6. [Step 4: Implement Handlers](#6-step-4-implement-handlers)
7. [Step 5: Create the Main Entry Point](#7-step-5-create-the-main-entry-point)
8. [Step 6: Add Deployment Files](#8-step-6-add-deployment-files)
9. [Testing Your Tool](#9-testing-your-tool)
10. [Best Practices](#10-best-practices)
11. [Troubleshooting](#11-troubleshooting)
12. [Advanced Telemetry](#12-advanced-telemetry)

---

## 1. Understanding Tools in TruvaG3

### What is a Tool?

In TruvaG3, a **Tool** is a passive component that:
- Registers one or more **capabilities** that can be discovered and invoked by agents
- Cannot discover or invoke other components (this is what makes it "passive")
- Typically wraps external APIs or provides specialized functionality
- Handles HTTP requests for its registered capabilities

### Tools vs Agents

| Aspect | Tool | Agent |
|--------|------|-------|
| Discovery | Can register, cannot discover | Can both register and discover |
| Orchestration | Cannot orchestrate | Can orchestrate other components |
| Purpose | Provide specific capabilities | Coordinate and execute complex tasks |
| External APIs | Primary use case | May use tools that wrap APIs |

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                      TruvaG3 Framework                        │
│  ┌─────────────────────────────────────────────────────┐    │
│  │                    Your Tool                         │    │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  │    │
│  │  │ Capability 1│  │ Capability 2│  │ Capability N│  │    │
│  │  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘  │    │
│  │         │                │                │         │    │
│  │         └────────────────┼────────────────┘         │    │
│  │                          │                          │    │
│  │                   ┌──────▼──────┐                   │    │
│  │                   │ API Client  │                   │    │
│  │                   └──────┬──────┘                   │    │
│  └──────────────────────────┼──────────────────────────┘    │
│                             │                                │
└─────────────────────────────┼────────────────────────────────┘
                              │
                              ▼
                    ┌─────────────────┐
                    │  External API   │
                    │  (e.g., Finnhub,│
                    │   OpenWeather)  │
                    └─────────────────┘
```

---

## 2. Project Structure

A well-organized tool follows this directory structure:

```
examples/your-tool/
├── main.go              # Entry point, framework configuration
├── your_tool.go         # Tool struct, capability registration
├── handlers.go          # HTTP handlers for capabilities
├── api_client.go        # External API client implementation
├── go.mod               # Go module definition
├── go.sum               # Dependency checksums
├── .env.example         # Environment variable documentation and defaults
├── Dockerfile           # Container build instructions (standalone)
├── Dockerfile.workspace # Development container (builds from truvag3 root)
├── k8-deployment.yaml   # Kubernetes deployment manifest
├── setup.sh             # Full lifecycle: cluster, build, deploy, test, clean
└── README.md            # Tool documentation (optional)
```

### File Responsibilities

| File | Purpose |
|------|---------|
| `main.go` | Configuration validation, telemetry initialization, framework setup, graceful shutdown |
| `your_tool.go` | Tool struct definition, capability registration with InputSummary |
| `handlers.go` | Request/response handling, logging, telemetry events, error handling |
| `api_client.go` | External API communication, HTTP client configuration, response parsing |
| `.env.example` | Documents all environment variables with descriptions, defaults, and setup instructions. Copied to `.env` for local development. Used by `setup.sh` to create K8s ConfigMaps. |

---

## 3. Step 1: Create the Tool Struct

The tool struct is the foundation of your implementation. It embeds `*core.BaseTool` and holds any tool-specific dependencies.

### Basic Structure

```go
// your_tool.go
package main

import (
    "os"
    "github.com/truvaagents/truva-g3/core"
)

// YourTool wraps an external API and exposes capabilities
type YourTool struct {
    *core.BaseTool
    apiKey string
    client *YourAPIClient  // External API client
}

// NewYourTool creates and initializes the tool
func NewYourTool() *YourTool {
    apiKey := os.Getenv("YOUR_API_KEY")

    tool := &YourTool{
        BaseTool: core.NewTool("your-service"),
        apiKey:   apiKey,
        client:   NewYourAPIClient(apiKey),
    }

    // Register all capabilities
    tool.registerCapabilities()
    return tool
}
```

### Key Points

1. **Embed `*core.BaseTool`**: This provides access to:
   - `Logger` - Context-aware logging
   - `Memory` - Caching with TTL support
   - `RegisterCapability()` - Capability registration method

2. **Store API credentials**: Read from environment variables, never hardcode

3. **Initialize external client**: Create your API client in the constructor

4. **Register capabilities**: Call a dedicated method to keep code organized

---

## 4. Step 2: Implement External API Client

The API client handles all communication with external services. This separation keeps your handlers clean and makes testing easier.

> **Note:** You can either create a separate API client file (like `finnhub_client.go` in stock-market-tool) or embed HTTP calls directly in handlers (like weather-tool-v2). A separate client is recommended for complex APIs with multiple endpoints.

### Client Structure with Distributed Tracing

**RECOMMENDED:** Use `telemetry.NewTracedHTTPClientWithTransport()` to create HTTP clients that automatically propagate trace context. This creates a child span for every outgoing HTTP call, giving you end-to-end visibility in Jaeger — including calls to external APIs where you can see latency, status codes, and URLs even though the external service won't propagate the trace further.

> For comprehensive details on HTTP client tracing, including what happens under the hood and troubleshooting tips, see [DISTRIBUTED_TRACING_GUIDE.md - Section 7: Client-Side TracedHTTPClient](../observability/DISTRIBUTED_TRACING_GUIDE.md#7-implementation-client-side-tracedhttpclient).

```go
// api_client.go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"

    "github.com/truvaagents/truva-g3/telemetry"
)

// YourAPIClient handles communication with the external API
type YourAPIClient struct {
    apiKey     string
    baseURL    string
    httpClient *http.Client
}

// NewYourAPIClient creates a configured API client with distributed tracing
func NewYourAPIClient(apiKey string) *YourAPIClient {
    // Use telemetry.NewTracedHTTPClientWithTransport for automatic trace propagation
    // This creates a child span for every outgoing HTTP call, providing visibility
    // into external API latency, status codes, and URLs in Jaeger
    tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    })
    tracedClient.Timeout = 30 * time.Second

    return &YourAPIClient{
        apiKey:     apiKey,
        baseURL:    "https://api.example.com",
        httpClient: tracedClient,
    }
}
```

### Alternative: Simple Traced Client

For simpler use cases, use `telemetry.NewTracedHTTPClient(nil)`:

```go
// Simple form - uses default transport settings
httpClient := telemetry.NewTracedHTTPClient(nil)
```

### API Request Method

```go
// GetData fetches data from the external API
func (c *YourAPIClient) GetData(ctx context.Context, param string) (*APIResponse, error) {
    // Build the request URL
    url := fmt.Sprintf("%s/endpoint?param=%s&token=%s",
        c.baseURL, param, c.apiKey)

    // Create request with context for cancellation and tracing
    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return nil, fmt.Errorf("failed to create request: %w", err)
    }

    // Set required headers
    req.Header.Set("Accept", "application/json")
    req.Header.Set("User-Agent", "TruvaG3-YourTool/1.0")

    // Execute the request
    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("API request failed: %w", err)
    }
    defer resp.Body.Close()

    // Check response status
    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("API error (status %d): %s",
            resp.StatusCode, string(body))
    }

    // Parse response
    var result APIResponse
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, fmt.Errorf("failed to decode response: %w", err)
    }

    return &result, nil
}
```

### Response Structs

```go
// APIResponse represents the external API response
type APIResponse struct {
    Data      string  `json:"data"`
    Timestamp int64   `json:"timestamp"`
    // Add fields matching the external API response
}
```

### Best Practices for API Clients

1. **Always use context**: Pass `context.Context` to support cancellation and tracing
2. **Configure timeouts**: Set appropriate timeouts on the HTTP client
3. **Handle errors gracefully**: Wrap errors with context using `fmt.Errorf("...: %w", err)`
4. **Close response bodies**: Always `defer resp.Body.Close()` after checking for errors
5. **Check status codes**: Don't assume success - verify the HTTP status

---

## 5. Step 3: Register Capabilities

Capabilities define what your tool can do. Each capability has a name, description, handler, and schema information for AI payload generation.

### Understanding the 3-Phase AI Payload Generation

TruvaG3 uses a progressive enhancement approach for AI to generate correct payloads:

| Phase | Mechanism | Accuracy | When to Use |
|-------|-----------|----------|-------------|
| Phase 1 | Description only | ~85-90% | Simplest capabilities |
| Phase 2 | Field hints (InputSummary) | ~95% | **Recommended for most tools** |
| Phase 3 | Full JSON Schema validation | ~99% | Complex nested structures |

**For most tools, Phase 2 (InputSummary) provides the best balance of accuracy and simplicity.**

### Capability Registration with InputSummary

```go
// your_tool.go (continued)
func (t *YourTool) registerCapabilities() {
    // Capability with required fields only
    t.RegisterCapability(core.Capability{
        Name:        "get_data",
        Description: "Retrieves data for a given identifier. Required: id (unique identifier string).",
        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler:     t.handleGetData,

        // Phase 2: Field hints for AI payload generation
        InputSummary: &core.SchemaSummary{
            RequiredFields: []core.FieldHint{
                {
                    Name:        "id",
                    Type:        "string",
                    Example:     "abc123",
                    Description: "Unique identifier for the resource",
                },
            },
        },
    })

    // Capability with required and optional fields
    t.RegisterCapability(core.Capability{
        Name:        "search_data",
        Description: "Searches for data matching criteria. Required: query (search term). Optional: limit (max results, default 10), from_date and to_date (YYYY-MM-DD format).",
        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler:     t.handleSearchData,

        InputSummary: &core.SchemaSummary{
            RequiredFields: []core.FieldHint{
                {
                    Name:        "query",
                    Type:        "string",
                    Example:     "technology",
                    Description: "Search query term",
                },
            },
            OptionalFields: []core.FieldHint{
                {
                    Name:        "limit",
                    Type:        "integer",
                    Example:     "10",
                    Description: "Maximum number of results to return",
                },
                {
                    Name:        "from_date",
                    Type:        "string",
                    Example:     "2024-01-01",
                    Description: "Start date in YYYY-MM-DD format",
                },
                {
                    Name:        "to_date",
                    Type:        "string",
                    Example:     "2024-01-31",
                    Description: "End date in YYYY-MM-DD format",
                },
            },
        },
    })
}
```

### InputSummary Components

```go
// From core module - reference structure
type SchemaSummary struct {
    RequiredFields []FieldHint `json:"required_fields,omitempty"`
    OptionalFields []FieldHint `json:"optional_fields,omitempty"`
}

type FieldHint struct {
    Name        string `json:"name"`        // Field name in JSON
    Type        string `json:"type"`        // string, integer, number, boolean, array, object
    Example     string `json:"example"`     // Example value (always as string)
    Description string `json:"description"` // What this field does
}
```

### OutputSummary — Declaring What Your Tool Returns

While `InputSummary` tells the orchestrator what fields your tool **accepts**, `OutputSummary` tells it what fields your tool **returns**. This enables the orchestrator to:

1. **Validate template references** — When a DAG plan references `{{step-1.response.data.analysis}}`, the orchestrator checks that `analysis` is a declared output field of the producing step's capability. Hallucinated field names are caught before execution.
2. **Render return fields in LLM prompts** — The `FormatForLLM()` catalog output includes a "Return Fields" section per capability, giving the planner accurate knowledge of available outputs.

```go
// Capability with both InputSummary and OutputSummary
t.RegisterCapability(core.Capability{
    Name:        "query_metrics",
    Description: "Executes an instant PromQL query against Prometheus.",
    InputTypes:  []string{"json"},
    OutputTypes: []string{"json"},
    Handler:     t.handleQueryMetrics,

    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {Name: "query", Type: "string", Example: "up",
                Description: "PromQL expression"},
        },
    },

    // Declare what this capability returns
    OutputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {Name: "query", Type: "string", Description: "The PromQL query that was executed"},
            {Name: "result_type", Type: "string", Description: "Result type: vector, scalar, string, or matrix"},
            {Name: "samples", Type: "array", Description: "List of metric samples with labels, timestamp, and value"},
            {Name: "source", Type: "string", Description: "Data source identifier"},
        },
        OptionalFields: []core.FieldHint{
            {Name: "warnings", Type: "array", Description: "Prometheus query warnings"},
        },
    },
})
```

**Guidelines for OutputSummary:**

| Guideline | Details |
|-----------|---------|
| **Match your response struct** | Field names must exactly match the JSON tags in your response struct |
| **Required vs Optional** | Required = always present in every response. Optional = conditionally present (use `omitempty` fields) |
| **Use `Example` for numeric types** | Helps the LLM understand value ranges (e.g., `Example: "0"` for exit codes) |
| **Top-level fields only** | Declare the top-level JSON keys. Nested object structures are not yet validated |
| **No OutputSummary = passthrough** | If you omit OutputSummary, template references to your output fields are allowed without validation (backwards compatible) |

### Writing Effective Descriptions

The capability description serves two critical purposes:
1. **Tool Selection** - Helps the LLM choose the right capability for a user query
2. **Payload Generation** - Guides the LLM to generate correct JSON payloads

#### The Description Formula

A well-structured description follows this pattern:

```
[WHAT it does] + [WHEN to use it] + [WHAT it returns] + [Required fields] + [Optional fields]
```

#### Description Guidelines

| Element | Purpose | Example |
|---------|---------|---------|
| **WHAT it does** | Core functionality | "Searches the web for general information" |
| **WHEN to use** | Differentiates from other capabilities | "when no specialized tool exists" |
| **Use cases** | Helps LLM match user queries | "Use for: destination recommendations, product comparisons" |
| **WHAT it returns** | Sets expectations | "Returns: titles, snippets, URLs with relevance scores" |
| **Required fields** | Must-have inputs | "Required: query (search terms)" |
| **Optional fields** | With defaults | "Optional: max_results (1-10, default 5)" |

#### Good vs Bad Descriptions

**Good Description (for LLM selection):**
```go
Description: "Searches the web for general information when no specialized tool exists. " +
             "Use for: destination recommendations, product comparisons, how-to guides, current events. " +
             "Returns: titles, snippets, URLs with relevance scores. " +
             "Required: query (search terms). Optional: max_results (1-10, default 5)."
```

**Why it works:**
- **WHEN to use**: "when no specialized tool exists" helps LLM decide between this and specialized tools
- **Use cases**: Lists concrete query types the capability handles
- **Returns**: Sets expectations about output format
- **Fields**: Clear required/optional with defaults

**Bad Description:**
```go
Description: "Gets stock data"
```

**Why it fails:**
- Too vague about what data is returned
- Doesn't explain when to use this vs other capabilities
- No field information for payload generation

#### Special Case: Pipeline/Utility Capabilities

Some capabilities are designed to be used AFTER other tools in orchestration pipelines. Make this explicit:

```go
Description: "Extracts named entities and fields from unstructured text using AI. " +
             "Use AFTER web_search or other tools to parse their output into structured JSON. " +
             "Example: extract destination names from search results to pass to weather tool. " +
             "Required: content (text to parse), extract_fields (field names like 'destination_names')."
```

**Key elements for pipeline capabilities:**
- "Use AFTER [tool]" - Indicates sequencing
- Concrete example showing the pipeline flow
- What it transforms (unstructured text → structured JSON)

#### Capability Selection in Practice

When an LLM receives a user query like "Find beach destinations and their weather", it evaluates all available capabilities. Your descriptions should help it:

1. **Match query intent** → "destination recommendations" matches user query
2. **Understand sequencing** → web_search first, then extract, then weather tool
3. **Know what's returned** → Helps plan subsequent steps

```
User Query: "Find beach destinations and their weather"

LLM sees capabilities:
├── web_search: "...destination recommendations...Returns: titles, snippets, URLs..."
│   → MATCH: "beach destinations"
├── extract_structured_data: "...Use AFTER web_search...extract destination names..."
│   → MATCH: Need to extract destination names for weather lookup
├── weather.current_weather: "Gets current weather...Required: location..."
│   → MATCH: Need weather for each destination
└── stock.stock_quote: "Gets stock price..."
    → NO MATCH: Not relevant to query
```

#### Tiered Selection and the `Summary` Field

In deployments with 20+ tools, the orchestrator uses **tiered selection** — an LLM call that receives only lightweight summaries (not full descriptions or schemas) to decide which tools are relevant. This means the quality and conciseness of your summary directly affects whether your tool gets selected.

**How summaries are derived:** Each capability has an optional `Summary` field. If set, it's used as-is. If not, `GetSummary()` auto-extracts the **first 2 sentences** of `Description`. No character-limit truncation is applied — but only those first 2 sentences reach the selection LLM.

```go
// Option A: Let the framework auto-extract from Description.
// The first 2 sentences MUST convey what the capability does.
Description: "Converts between currencies using live exchange rates. " +
             "Supports 150+ currencies including crypto. " +
             "Required: from_currency, to_currency, amount."
// Summary sent to selection LLM: "Converts between currencies using live exchange rates. Supports 150+ currencies including crypto."

// Option B: Set an explicit Summary for precise control.
Summary: "Converts between currencies using live exchange rates for 150+ currencies including crypto.",
Description: "Converts between currencies using live exchange rates. " +
             "Supports 150+ currencies including crypto. " +
             "Required: from_currency (ISO 4217 code), to_currency (ISO 4217 code), amount (number)."
```

**Practical guidance:**
- **Front-load your descriptions** — put the most distinctive information in the first 2 sentences since that's what the selection LLM sees.
- **Set explicit `Summary`** when the auto-extracted sentences don't capture the key differentiator (e.g., when the first sentence is generic).
- Keep summaries concise but specific enough to distinguish your capability from similar ones in the catalog.
- **Verify your summaries after writing descriptions.** The `extractFirstSentences` function in `orchestration/catalog.go` splits on `.`, `!`, or `?` characters. After writing a description, mentally extract the first 2 sentences and ask: "Would an LLM choose this capability over similar ones based on these sentences alone?" If not, either reword the first 2 sentences or set an explicit `Summary`.

---

## 6. Step 4: Implement Handlers

Handlers process incoming requests, call the external API, and return responses. They must include proper logging and telemetry for observability.

### Request/Response Types

```go
// handlers.go
package main

// Request types - match your InputSummary definitions
type GetDataRequest struct {
    ID string `json:"id"`
}

type SearchDataRequest struct {
    Query    string `json:"query"`
    Limit    int    `json:"limit,omitempty"`
    FromDate string `json:"from_date,omitempty"`
    ToDate   string `json:"to_date,omitempty"`
}

// Response types - what your capability returns
// These field names must match your OutputSummary declarations
type GetDataResponse struct {
    ID        string `json:"id"`
    Data      string `json:"data"`
    Timestamp int64  `json:"timestamp"`
    Source    string `json:"source"`
}
```

> **Tip:** Your response struct's JSON tags are the source of truth for `OutputSummary` field names. When you add or rename a response field, update your `OutputSummary` to match.

### Handler Implementation Pattern

```go
import (
    "encoding/json"
    "fmt"
    "net/http"
    "strings"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
    "go.opentelemetry.io/otel/attribute"
)

// Error codes for your tool (define constants for consistency)
const (
    ErrCodeInvalidRequest     = "INVALID_REQUEST"
    ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
    ErrCodeInvalidInput       = "INVALID_INPUT"
)

func (t *YourTool) handleGetData(rw http.ResponseWriter, r *http.Request) {
    // 1. Get context and start timing for duration tracking
    ctx := r.Context()
    startTime := time.Now()

    // 2. Extract request ID from upstream baggage (set by orchestrator)
    //    Falls back to X-TruvaG3-Request-ID header (set by executor)
    var requestID string
    if baggage := telemetry.GetBaggage(ctx); baggage != nil {
        requestID = baggage["request_id"]
    }
    if requestID == "" {
        requestID = r.Header.Get("X-TruvaG3-Request-ID")
    }

    // 3. Log the incoming request with required fields
    t.Logger.InfoWithContext(ctx, "Processing get_data request", map[string]interface{}{
        "operation":  "get_data",
        "method":     r.Method,
        "path":       r.URL.Path,
        "request_id": requestID,
    })

    // 4. Add span event for request received
    telemetry.AddSpanEvent(ctx, "request_received",
        attribute.String("method", r.Method),
        attribute.String("path", r.URL.Path),
        attribute.String("operation", "get_data"),
    )

    // 5. Decode and validate request
    var req GetDataRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        telemetry.RecordSpanError(ctx, err) // Record on span for Jaeger visibility
        t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
            "operation":   "get_data",
            "error":       err.Error(),
            "error_type":  "decode_error",
            "request_id":  requestID,
            "status":      "failure",
            "duration_ms": time.Since(startTime).Milliseconds(),
        })
        t.sendError(rw, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
        return
    }

    // 6. Normalize and validate input
    req.ID = strings.TrimSpace(req.ID)
    if req.ID == "" {
        err := fmt.Errorf("id is required")
        telemetry.RecordSpanError(ctx, err) // Record on span for Jaeger visibility
        t.Logger.WarnWithContext(ctx, "Empty ID in request", map[string]interface{}{
            "operation":   "get_data",
            "error_type":  "validation_error",
            "request_id":  requestID,
            "status":      "failure",
            "duration_ms": time.Since(startTime).Milliseconds(),
        })
        t.sendError(rw, "ID is required", http.StatusBadRequest, ErrCodeInvalidInput)
        return
    }

    // 7. Log validated request
    t.Logger.InfoWithContext(ctx, "Received get_data request", map[string]interface{}{
        "operation":  "get_data",
        "id":         req.ID,
        "request_id": requestID,
    })

    // 8. Add span event before external API call
    telemetry.AddSpanEvent(ctx, "calling_external_api",
        attribute.String("id", req.ID),
        attribute.String("api", "get_data"),
    )

    // 9. Call external API with timing
    apiStartTime := time.Now()
    data, err := t.client.GetData(ctx, req.ID)
    apiDuration := time.Since(apiStartTime)

    // 10. Handle API errors or build response
    var result GetDataResponse

    if err != nil || data == nil {
        if err == nil {
            err = fmt.Errorf("external API returned no data")
        }
        // Classify the original error, but sanitize all observation/response text.
        info := core.ClassifyUpstreamError(err)
        safeError := core.RedactSensitiveText(err.Error())

        // Record sanitized error text on the span for Jaeger visibility.
        if err != nil {
            telemetry.RecordSpanError(ctx, fmt.Errorf("%s", safeError))
        }

        // Log the failure with context
        t.Logger.WarnWithContext(ctx, "External API call failed", map[string]interface{}{
            "operation":   "get_data",
            "error":       safeError,
            "error_type":  "api_error",
            "id":          req.ID,
            "request_id":  requestID,
            "api_latency": apiDuration.String(),
        })

        // ClassifyUpstreamError extracts the upstream HTTP status from the error
        // message and maps it to the correct tool response for orchestrator routing.
        //
        // Alternative: If your tool provides fallback/mock data, skip the
        // sendUpstreamError + return and assign: result = generateFallbackData(req.ID)
        t.sendUpstreamError(rw, "Data fetch failed: "+safeError, info)
        return
    } else {
        // Log successful API call
        t.Logger.InfoWithContext(ctx, "External API call successful", map[string]interface{}{
            "operation":   "get_data",
            "id":          req.ID,
            "request_id":  requestID,
            "api_latency": apiDuration.String(),
        })

        // Map API response to our response format
        result = GetDataResponse{
            ID:        req.ID,
            Data:      data.Data,
            Timestamp: data.Timestamp,
            Source:    "External API",
        }
    }

    // 11. Optional: Cache the result
    cacheKey := fmt.Sprintf("data:%s", req.ID)
    cacheData, _ := json.Marshal(result)
    t.Memory.Set(ctx, cacheKey, string(cacheData), 5*time.Minute)

    // 12. Send successful response wrapped in core.ToolResponse
    rw.Header().Set("Content-Type", "application/json")
    response := core.ToolResponse{
        Success: true,
        Data:    result,
    }
    json.NewEncoder(rw).Encode(response)

    // 13. Add success span event
    telemetry.AddSpanEvent(ctx, "data_retrieved",
        attribute.String("id", req.ID),
        attribute.String("source", result.Source),
    )

    // 14. Log completion with required fields
    t.Logger.InfoWithContext(ctx, "get_data request completed", map[string]interface{}{
        "operation":   "get_data",
        "id":          req.ID,
        "source":      result.Source,
        "request_id":  requestID,
        "status":      "success",
        "duration_ms": time.Since(startTime).Milliseconds(),
    })
}

// sendError sends a structured error response for LOCAL validation errors
// (decode failures, missing fields, invalid input).
// Use sendUpstreamError for external API failures instead.
func (t *YourTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
    rw.Header().Set("Content-Type", "application/json")
    rw.WriteHeader(status)  // CRITICAL: must be called before json.Encode

    response := core.ToolResponse{
        Success: false,
        Error: &core.ToolError{
            Code:      code,
            Message:   message,
            Retryable: status >= 500,
        },
    }
    json.NewEncoder(rw).Encode(response)
}

// sendUpstreamError sends a structured error response for UPSTREAM API failures.
// Uses core.ClassifyUpstreamError to map the upstream HTTP status to the correct
// tool response status, category, and retryable flag for orchestrator routing.
func (t *YourTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
    rw.Header().Set("Content-Type", "application/json")
    rw.WriteHeader(info.HTTPStatus)
    json.NewEncoder(rw).Encode(core.ToolResponse{
        Success: false,
        Error: &core.ToolError{
            Code:      info.Code,
            Message:   message,
            Category:  info.Category,
            Retryable: info.Retryable,
        },
    })
}
```

> **CRITICAL: Always call `rw.WriteHeader(status)` before encoding error responses.**
>
> If you omit `WriteHeader()`, Go defaults to HTTP 200. The orchestrator determines step
> success **solely from the HTTP status code** — it does not parse the response body for
> `"success": false`. This means a tool that returns HTTP 200 with
> `core.ToolResponse{Success: false, Error: ...}` will be treated as a **successful step**,
> breaking error routing (resilience retries, LLM error analysis) and causing the DAG
> visualization to show failed steps as green/completed.
>
> Always use a `sendError()` helper (shown above) or `core.HTTPStatusForCategory()` to set
> the correct HTTP status code. See [core/tool_error.go](https://github.com/truvaagents/truva-g3/blob/main/core/tool_error.go) for the
> category-to-status mapping (e.g., `CategoryServiceError` → 503).

### Handler Checklist

Every handler should:

- [ ] Extract context from request: `ctx := r.Context()`
- [ ] Track start time: `startTime := time.Now()`
- [ ] Extract request ID from baggage (primary) or `X-TruvaG3-Request-ID` header (fallback)
- [ ] Log the incoming request with `InfoWithContext` (include `operation`, `request_id`)
- [ ] Add span event for request received with `telemetry.AddSpanEvent()`
- [ ] Decode JSON body and handle decode errors:
  - Record on span: `telemetry.RecordSpanError(ctx, err)`
  - Log with `ErrorWithContext` (include `operation`, `error_type`, `request_id`)
  - Return error via `sendError()` helper
- [ ] Validate and normalize input (check required fields, bounds, etc.)
- [ ] Handle validation errors:
  - Record on span: `telemetry.RecordSpanError(ctx, err)`
  - Log with `WarnWithContext` (include `operation`, `error_type`, `request_id`)
  - Return error via `sendError()` helper
- [ ] Log validated request parameters
- [ ] Add span event before external API call
- [ ] Call external API with context and measure duration
- [ ] Handle upstream API errors with `sendUpstreamError` + `core.ClassifyUpstreamError(err)`:
  - Record on span: `telemetry.RecordSpanError(ctx, err)`
  - Log with `WarnWithContext` or `ErrorWithContext` (include `operation`, `error_type`)
  - Return classified error: `t.sendUpstreamError(rw, msg, core.ClassifyUpstreamError(err))`
  - **Do NOT** use `sendError` for upstream failures — it bypasses error classification
- [ ] Set `Content-Type: application/json` header
- [ ] Wrap response in `core.ToolResponse{Success: true, Data: result}`
- [ ] Add span event for successful completion
- [ ] Log completion with required fields: `operation`, `status`, `duration_ms`, `request_id`

> **Want More?** For advanced telemetry features including custom metrics (Counter, Histogram, Gauge), log-trace correlation via `GetTraceContext()`, span enrichment with `SetSpanAttributes()`, and unified metrics, see [Section 12: Advanced Telemetry](#12-advanced-telemetry).

### Logging Guidelines

Use the appropriate log level:

| Level | When to Use | Example |
|-------|-------------|---------|
| `Debug` | Detailed debugging info | "Cache key generated: data:abc123" |
| `Info` | Normal operations | "Processing request", "Request completed" |
| `Warn` | Recoverable issues | "API failed, using fallback" |
| `Error` | Errors requiring attention | "Failed to decode request" |

Always use the `WithContext` variants for trace correlation:

```go
// Good - includes trace context
t.Logger.InfoWithContext(ctx, "Processing request", map[string]interface{}{
    "operation": "get_data",
    "id":        req.ID,
})

// Bad - loses trace correlation
t.Logger.Info("Processing request", map[string]interface{}{
    "operation": "get_data",
    "id":        req.ID,
})
```

### Standard Log Fields

Use consistent field names across your tool. The `operation` field is **required** in every log entry:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `operation` | string | **Yes** | The capability/operation name (e.g., "get_data") |
| `request_id` | string | In handlers | Upstream request ID for correlation |
| `status` | string | Completion logs | "success", "failure", "partial" |
| `error` | string | Error logs | Error message if applicable |
| `error_type` | string | Error logs | Category: "validation_error", "api_error", "decode_error" |
| `duration_ms` | int64 | Completion logs | Total handler duration from request received to response sent |
| `api_latency` | string | HTTP API calls only | External HTTP API call duration (e.g., `"537.2ms"`). **Not used** for tools backed by Go interfaces (e.g., Redis clients, framework interfaces) — only for tools that make outbound HTTP requests to external services. Track separately from `duration_ms` with a dedicated `apiStartTime`. |

> **Reference:** For complete logging standards including startup logging and the mixed logging problem, see [LOGGING_IMPLEMENTATION_GUIDE.md](../observability/LOGGING_IMPLEMENTATION_GUIDE.md).

---

## 7. Step 5: Create the Main Entry Point

The main.go file handles configuration, initialization, and graceful shutdown.

### Complete Main Implementation

```go
// main.go
package main

import (
    "context"
    "errors"
    "fmt"
    "log"
    "os"
    "os/signal"
    "strconv"
    "strings"
    "syscall"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"
)

func main() {
    // 1. Validate configuration first
    if err := validateConfig(); err != nil {
        log.Fatalf("Configuration error: %v", err)
    }

    // 2. Create the tool FIRST so component type is set for telemetry
    // The tool constructor calls core.SetCurrentComponentType(ComponentTypeTool)
    // which enables automatic service_type inference in telemetry
    tool := NewYourTool()

    // 3. Initialize telemetry AFTER tool creation
    initTelemetry("your-service")
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := telemetry.Shutdown(ctx); err != nil {
            log.Printf("Warning: Telemetry shutdown error: %v", err)
        }
    }()

    // 4. Get port configuration from environment (as int)
    port := 8080 // default port
    if portStr := os.Getenv("PORT"); portStr != "" {
        if p, err := strconv.Atoi(portStr); err == nil {
            port = p
        }
    }

    // 5. Create the framework with options
    framework, err := core.NewFramework(tool,
        // Core configuration
        core.WithName("your-service"),
        core.WithPort(port),
        core.WithNamespace(os.Getenv("NAMESPACE")),

        // Discovery configuration (tools can register but not discover)
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),

        // CORS for web access
        core.WithCORS([]string{"*"}, true),

        // Development mode from environment
        core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),

        // Distributed tracing middleware for context propagation
        core.WithMiddleware(telemetry.TracingMiddleware("your-service")),
    )
    if err != nil {
        log.Fatalf("Failed to create framework: %v", err)
    }

    // 6. Display startup information
    log.Println("Your Tool Service Starting...")
    log.Println("Telemetry: Enabled")
    log.Printf("Server Port: %d\n", port)

    // 7. Set up graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        log.Println("Shutting down gracefully...")

        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer shutdownCancel()

        cancel()

        select {
        case <-shutdownCtx.Done():
            log.Println("Shutdown timeout exceeded")
            os.Exit(1)
        case <-time.After(1 * time.Second):
            // Give framework time to clean up
        }

        log.Println("Shutdown completed")
        os.Exit(0)
    }()

    // 8. Run the framework (blocking call)
    if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
        log.Fatalf("Framework error: %v", err)
    }
}

// validateConfig validates all required configuration at startup
func validateConfig() error {
    // REDIS_URL is required for discovery
    redisURL := os.Getenv("REDIS_URL")
    if redisURL == "" {
        return fmt.Errorf("REDIS_URL environment variable required")
    }

    // Validate Redis URL format
    if !strings.HasPrefix(redisURL, "redis://") && !strings.HasPrefix(redisURL, "rediss://") {
        return fmt.Errorf("invalid REDIS_URL format (must start with redis:// or rediss://)")
    }

    // Warn about optional but recommended variables (API keys, etc.)
    if os.Getenv("YOUR_API_KEY") == "" {
        log.Println("Warning: YOUR_API_KEY not set - tool will use mock data")
    }

    // Validate port if set
    if portStr := os.Getenv("PORT"); portStr != "" {
        if _, err := strconv.Atoi(portStr); err != nil {
            return fmt.Errorf("invalid PORT value: %v", err)
        }
    }

    return nil
}

// initTelemetry sets up telemetry based on environment
func initTelemetry(serviceName string) {
    // Determine environment profile
    env := os.Getenv("APP_ENV")
    if env == "" {
        env = "development"
    }

    var profile telemetry.Profile
    switch env {
    case "production", "prod":
        profile = telemetry.ProfileProduction
    case "staging", "stage", "qa":
        profile = telemetry.ProfileStaging
    default:
        profile = telemetry.ProfileDevelopment
    }

    // Create config from profile
    config := telemetry.UseProfile(profile)
    config.ServiceName = serviceName

    // Override endpoint from environment if set
    if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
        config.Endpoint = endpoint
    }

    // Initialize telemetry
    if err := telemetry.Initialize(config); err != nil {
        log.Printf("Warning: Telemetry initialization failed: %v", err)
        log.Printf("   Tool will continue without telemetry")
        return
    }

    // Enable framework integration so core components (redis_registry,
    // discovery) emit metrics. Must be called AFTER Initialize().
    telemetry.EnableFrameworkIntegration(nil)

    log.Printf("Telemetry initialized for %s", serviceName)
}
```

### Framework Options Reference

| Option | Purpose | Example |
|--------|---------|---------|
| `WithName(name)` | Service name for registration | `WithName("stock-service")` |
| `WithPort(port)` | HTTP server port (int) | `WithPort(8080)` |
| `WithNamespace(ns)` | Namespace for multi-tenant setups | `WithNamespace(os.Getenv("NAMESPACE"))` |
| `WithRedisURL(url)` | Redis connection for discovery | `WithRedisURL("redis://localhost:6379")` |
| `WithDiscovery(enable, type)` | Enable capability discovery | `WithDiscovery(true, "redis")` |
| `WithCORS(origins, credentials)` | CORS configuration | `WithCORS([]string{"*"}, true)` |
| `WithDevelopmentMode(bool)` | Enable development mode | `WithDevelopmentMode(true)` |
| `WithMiddleware(mw)` | Add HTTP middleware | `WithMiddleware(telemetry.TracingMiddleware("svc"))` |

---

## 8. Step 6: Add Deployment Files

### go.mod

TruvaG3 uses a multi-module workspace. Each tool depends on `core` and `telemetry` as separate modules, with `replace` directives pointing to the local workspace copies for development.

```go
module github.com/truvaagents/truva-g3/examples/your-tool

go 1.26.6

require (
    github.com/truvaagents/truva-g3/core v0.9.1
    github.com/truvaagents/truva-g3/telemetry v0.9.1
    go.opentelemetry.io/otel v1.38.0
)

// Use local workspace modules for development
replace (
    github.com/truvaagents/truva-g3/core => ../../core
    github.com/truvaagents/truva-g3/telemetry => ../../telemetry
)
```

> **Note:** The `require` versions (e.g., `v0.9.1`) don't matter when `replace` directives are active — Go uses the local paths. The versions are there for when the module is consumed from a registry without replace directives.

### Dockerfile (standalone)

For publishing to a container registry. Copies the tool directory only — no local module references.

```dockerfile
FROM golang:1.26.6-alpine AS builder

RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
COPY *.go ./

# Configure Go to fetch from GitHub and ignore workspace
ENV GOWORK=off
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GONOSUMDB=github.com/truvaagents/truva-g3/*

RUN go mod download && go mod verify

RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o your-tool .

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /app/your-tool .
RUN chown appuser:appgroup /app/your-tool
USER appuser

ENV PORT=8080
EXPOSE ${PORT}
ENTRYPOINT ["./your-tool"]
```

### Dockerfile.workspace (for development)

Builds from the truvag3 root with local `core/` and `telemetry/` modules. Used by `setup.sh docker-build`.

```bash
# Build from truvag3 root:
docker build -f examples/your-tool/Dockerfile.workspace -t your-tool:latest .
```

```dockerfile
# Workspace Dockerfile for your-tool
# Usage (from truvag3 root): docker build -f examples/your-tool/Dockerfile.workspace -t your-tool:latest .

FROM golang:1.26.6-alpine AS builder

RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

# Copy local modules that are referenced by replace directives
COPY core/ ./core/
COPY telemetry/ ./telemetry/

# Copy the tool
COPY examples/your-tool/ ./examples/your-tool/

WORKDIR /app/examples/your-tool

# Configure Go build
ENV GOWORK=off
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GONOSUMDB=github.com/truvaagents/truva-g3/*

# Download remaining dependencies and build
RUN go mod tidy && go mod download

# Build the binary with optimizations
ARG TARGETARCH=arm64
RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o your-tool .

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /app/examples/your-tool/your-tool .
RUN chown appuser:appgroup /app/your-tool && \
    chmod +x /app/your-tool
USER appuser

ENV PORT=8080
EXPOSE ${PORT}
ENTRYPOINT ["./your-tool"]
```

### k8-deployment.yaml

**Important:** Define Service first, then Deployment. This ensures the service endpoint is available when pods start.

Environment variables are split into three categories:
1. **`envFrom` (ConfigMap):** User-configurable vars from `.env` (APP_ENV, DEV_MODE, log settings). Created by `setup.sh` using the shared `setup-env-lib.sh` helpers. Marked `optional: true` so pods start even without the ConfigMap.
2. **`env` (inline):** Infrastructure vars that are K8s-internal (PORT, NAMESPACE, REDIS_URL, OTEL endpoint). These never change per-user.
3. **`env` (secretKeyRef):** API keys from K8s Secrets (created by `setup.sh`).

```yaml
# Note: Secrets and ConfigMaps are created by setup.sh from .env file
# Run ./setup.sh deploy to deploy with proper API keys
---
apiVersion: v1
kind: Service
metadata:
  name: your-tool-service
  namespace: truvag3-examples
  labels:
    app: your-tool
    component: tool
spec:
  selector:
    app: your-tool
  ports:
    - name: http
      port: 80
      targetPort: 8080
      protocol: TCP
  type: ClusterIP

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: your-tool
  namespace: truvag3-examples
  labels:
    app: your-tool
    component: tool
spec:
  replicas: 2
  selector:
    matchLabels:
      app: your-tool
  template:
    metadata:
      labels:
        app: your-tool
        component: tool
    spec:
      containers:
      - name: your-tool
        image: your-tool:latest
        imagePullPolicy: Never  # For local Kind cluster
        ports:
        - containerPort: 8080
          name: http
        # User-configurable env vars from .env via setup.sh
        envFrom:
        - configMapRef:
            name: your-tool-env-config
            optional: true  # Created by setup.sh from .env values
        # Infrastructure env vars (K8s-internal, not from .env)
        env:
        - name: PORT
          value: "8080"
        - name: NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: REDIS_URL
          value: "redis://redis.truvag3-examples:6379"
        # Redis Registration Resilience (handles Redis startup race conditions)
        - name: TRUVAG3_DISCOVERY_RETRY
          value: "true"
        - name: TRUVAG3_DISCOVERY_RETRY_INTERVAL
          value: "30s"
        # Secrets (API keys — replace with your tool's keys)
        - name: YOUR_API_KEY
          valueFrom:
            secretKeyRef:
              name: your-tool-secrets
              key: YOUR_API_KEY
        # K8s identity
        - name: TRUVAG3_K8S_SERVICE_PORT
          value: "80"
        - name: TRUVAG3_K8S_POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        # Enables startup observability-identity drift check (optional but recommended)
        - name: TRUVAG3_K8S_POD_APP_LABEL
          valueFrom:
            fieldRef:
              fieldPath: metadata.labels['app']
        # Telemetry endpoint (K8s-internal address)
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector.truvag3-examples:4318"
        resources:
          requests:
            cpu: "50m"
            memory: "50Mi"
          limits:
            cpu: "100m"
            memory: "128Mi"
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /api/capabilities
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
```

> **Why `/api/capabilities` for readiness?** A tool's HTTP server may be running (`/health` passes) before its capabilities are registered. Using `/api/capabilities` ensures K8s only routes traffic to pods that are fully ready to serve requests. Use `/health` for livenessProbe (is the process alive?) and `/api/capabilities` for readinessProbe (is the tool ready for traffic?).

#### Observability Identity: Pick One Name and Use It Everywhere

**Before you write any code for a new tool, decide on the one canonical name and use
it mechanically in every place the tool's identity appears.** Do not shorten it in
one file "for readability", do not change its casing, do not pluralize it in YAML
but not in Go. Consistency beats cleverness.

Places the name must appear identically:

- **Directory name**: `examples/your-tool/`
- **Go code**: `core.WithName("your-tool")` and the `initTelemetry("your-tool")` helper call
- **K8s manifest**, in every resource:
  - `Deployment.metadata.name: your-tool`
  - `Service.metadata.name: your-tool-service` (only this one takes the `-service` suffix)
  - Pod template `metadata.labels.app: your-tool`
  - Service `spec.selector.app: your-tool`
  - Container name: `your-tool`
  - ConfigMap / Secret names that reference the tool: `your-tool-env-config`, `your-tool-secrets`
- **Environment**: `OTEL_SERVICE_NAME=your-tool` (or leave unset — it defaults to
  `cfg.Name`, which is what `core.WithName` sets)

Picking one name upfront and applying it uniformly makes the three-string alignment
below a natural consequence of how you typed the YAML, rather than something you
have to check. A real past example: one tool in this repo was created with
`core.WithName("stock-market-tool")` in Go but `name: stock-tool` in the K8s manifest
— and that latent drift stayed invisible until the logs pipeline started deriving
identity from the pod label, at which point the disagreement surfaced as an
observability bug. Don't do that.

##### The three-string alignment rule (enforced by the pipeline)

Three places in your deployment declare your tool's identity. Keep them in sync — the
shared observability pipeline assumes they agree. (State of existing examples in the
repo: roughly two-thirds align or correctly leave `OTEL_SERVICE_NAME` unset; a handful
of older `.env.example` files still drift — see
[LOGGING_IMPLEMENTATION_GUIDE.md §10](../observability/LOGGING_IMPLEMENTATION_GUIDE.md#10-structured-logging-field-naming-standards)
for the audit. New tools should follow the convention below from the start.)

| Where | Declares | Consumed by |
|-------|----------|-------------|
| Pod template `metadata.labels.app` | Canonical K8s identity | `service_name` label in **Loki**, plus `k8s.deployment.name` and related K8s resource attributes |
| `core.WithName("…")` functional option in `main.go` (or `TRUVAG3_AGENT_NAME` env var, if used) | Framework's `cfg.Name` | The `service` field in the JSON log body; service registration name in Redis discovery |
| `TRUVAG3_TELEMETRY_SERVICE_NAME` / `OTEL_SERVICE_NAME` env var (falls back to `cfg.Name` if unset — see [core/config.go:831-836](https://github.com/truvaagents/truva-g3/blob/main/core/config.go#L831-L836)) | `cfg.Telemetry.ServiceName` | `service.name` resource on **Jaeger** traces and **Prometheus** metrics (SDK-exported) |

All three **must equal the same string** (typically the tool name). If they drift, you
get split-brain observability: Loki labels one name, Jaeger another, the log body says
a third. Debugging a failing request becomes a manual join across mismatched identities.

In the YAML above, that means `metadata.labels.app: your-tool` on the Deployment, the
Service selector, and the pod template — and, if you set `OTEL_SERVICE_NAME` as an env
var, it should also say `your-tool`. The log body `service` field is driven by
`core.WithName("your-tool")` in `main.go`; leave it consistent.

> **Important: pod `app:` label is the Loki source of truth.** The cluster-wide OTel
> log pipeline derives `service_name` in Loki from the pod's `app:` label (via the
> `k8sattributes` processor), **not** from whatever string your application prints
> in its log body. Omitting the label lands your logs under `unknown_service`. Misaligning
> the label against `OTEL_SERVICE_NAME` means a Loki filter by `service_name` and a
> Jaeger filter by `service.name` won't return the same set of records. See
> [examples/k8-deployment/OBSERVABILITY.md](https://github.com/truvaagents/truva-g3/blob/main/examples/k8-deployment/OBSERVABILITY.md)
> for the pipeline details.

##### Recommended: let the framework catch drift at startup

Add this env entry to your pod template so the framework can detect
identity drift when the container boots:

```yaml
env:
- name: TRUVAG3_K8S_POD_APP_LABEL
  valueFrom:
    fieldRef:
      fieldPath: metadata.labels['app']
```

At startup, the framework compares this label against `cfg.Name` (set by
`core.WithName`) and `cfg.Telemetry.ServiceName` (set by `OTEL_SERVICE_NAME`).
If any disagree, it logs a WARN describing the mismatch — for example, a pod
with `app: stock-tool` but `OTEL_SERVICE_NAME=stock-service` produces:

```
WARN Observability identity drift detected at startup
  pod_app_label=stock-tool
  framework_name=stock-market-tool
  telemetry_service_name=stock-service
  drift_details=[framework name "stock-market-tool" ... differs from pod app label "stock-tool" ...; telemetry service name "stock-service" ... differs from pod app label "stock-tool" ...]
```

The check never fails startup and becomes a no-op when the env var is unset,
so it's safe to omit from older manifests. New tools should include it as
cheap insurance against silent drift.

#### Environment Variable Categories

| Category | Variables | Source | Purpose |
|----------|-----------|--------|---------|
| **envFrom (ConfigMap)** | `APP_ENV`, `DEV_MODE`, `TRUVAG3_LOG_LEVEL`, `TRUVAG3_LOG_FORMAT` | `.env` → ConfigMap via `setup.sh` | User-configurable, varies per environment |
| **env (inline)** | `PORT`, `NAMESPACE`, `REDIS_URL`, `TRUVAG3_DISCOVERY_RETRY*`, `TRUVAG3_K8S_*`, `OTEL_EXPORTER_OTLP_ENDPOINT` | Hardcoded in YAML | K8s infrastructure, same for all users |
| **env (secretKeyRef)** | `YOUR_API_KEY` (tool-specific) | K8s Secret via `setup.sh` | Sensitive credentials |

### setup.sh

A comprehensive setup script should support the full development lifecycle. See [examples/stock-market-tool/setup.sh](https://github.com/truvaagents/truva-g3/blob/main/examples/stock-market-tool/setup.sh) for a complete reference implementation (~500 lines).

**Shared Library:** All setup scripts source [`examples/k8-deployment/setup-env-lib.sh`](https://github.com/truvaagents/truva-g3/blob/main/examples/k8-deployment/setup-env-lib.sh) — the single source of truth for deployment helpers. Source it from your tool's `setup.sh`:

```bash
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"
```

The library exposes these helpers (read [setup-env-lib.sh](https://github.com/truvaagents/truva-g3/blob/main/examples/k8-deployment/setup-env-lib.sh) for full argument docs):

**Cluster lifecycle:**

| Helper | Purpose |
|---|---|
| `truvag3_check_prerequisites` | Verify `go`, `docker`, `kind`, `kubectl` are installed (optional convenience; most setup scripts skip it) |
| `truvag3_create_cluster [cluster_name]` | Create a Kind cluster wired for NGINX Ingress (ports 80/443). **Idempotent** — reuses an existing cluster of the same name |
| `truvag3_setup_infra [namespace]` | Deploy shared infra: Ingress controller, Redis, Prometheus, Grafana, Jaeger, Loki, OTel Collector, Metrics Server, and Ingress routes (idempotent — checks before deploying) |
| `truvag3_delete_cluster [cluster_name]` | Tear down the Kind cluster (used by `clean-all`) |

**Build & image loading:**

| Helper | Purpose |
|---|---|
| `truvag3_load_env <env_file_path>` | Source a `.env` file (exports vars); auto-bootstraps from a sibling `.env.example` on a fresh checkout |
| `truvag3_build_docker <image> <dockerfile> <context> [--no-cache]` | Build a Docker image; the `--no-cache` argument or `DOCKER_NO_CACHE=true` env var forces a fresh build |
| `truvag3_load_to_kind <image> [cluster_name]` | Side-load a built image into the Kind cluster. Auto-detects cluster from `kubectl` context, falling back to `$CLUSTER_NAME` |

**Kubernetes resources (created from `.env`):**

| Helper | Purpose |
|---|---|
| `truvag3_create_configmap <name> <ns> <env_file> [extra_vars…]` | ConfigMap from `.env` (auto-includes `TRUVAG3_*` + curated config vars; pass extras for tool-specific non-secret config). **API keys are deliberately excluded** — they belong in a Secret, not a ConfigMap |
| `truvag3_create_tool_secret <name> <ns> <KEY1> [KEY2…]` | Secret containing only the specified env-var keys (use for tool API keys, e.g. `FINNHUB_API_KEY`). Does **not** include AI provider keys |
| `truvag3_create_secret <name> <ns> [extra_keys…]` | Secret containing the configured standard AI-provider credentials (OpenAI, Anthropic, OpenRouter, Groq, DeepSeek, xAI, Mistral, Qwen, Together, Gemini/Google, and AWS), plus any extras you append. Set setup-only `TRUVAG3_SETUP_AI_PROVIDER` to restrict the Secret to one supported provider during an isolated deployment. Use this only if your tool actually calls an LLM — most tools should use `truvag3_create_tool_secret` instead |

**Service access:**

| Helper | Purpose |
|---|---|
| `truvag3_forward <svc> <local_port> <svc_port> [ns]` | Foreground port-forward with auto-reconnect on disconnect (Ctrl+C to stop) |
| `truvag3_forward_all <svc:lp:sp> [svc:lp:sp…]` | Port-forwards every additional service in the background, then runs the first one in the foreground with auto-reconnect; Ctrl+C stops all of them |

**Required Commands:**

| Command | Purpose |
|---------|---------|
| `cluster` | Create Kind cluster with port mappings |
| `infra` | Deploy infrastructure (Redis + monitoring) |
| `full-deploy` | One-click: cluster + infra + deploy + port-forward |
| `build` | Build Go binary locally (`GOWORK=off go build`) |
| `run` | Build and run locally |
| `docker-build` | Build Docker image (workspace mode from truvag3 root) |
| `deploy` | Build, load to Kind, create secrets + ConfigMap, apply manifests |
| `rebuild` | Rebuild with `--no-cache` and redeploy |
| `test` | Run API tests against deployed tool |
| `forward` | Port forward the service only |
| `forward-all` | Port forward service + Grafana + Prometheus + Jaeger |
| `logs` | View tool logs |
| `status` | Check deployment status |
| `rollout` | Restart deployment (with optional `--build`) |
| `clean` | Remove tool deployment only |
| `clean-all` | Delete entire Kind cluster |

**Minimal Example:**

```bash
#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="your-tool"
PORT=${PORT:-8080}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

# Load .env file if it exists
load_env() {
    if [ -f .env ]; then
        set -a && source .env && set +a
    elif [ -f .env.example ]; then
        cp .env.example .env
        set -a && source .env && set +a
    fi
}

# Build Go binary locally
# GOWORK=off is required because the workspace has a go.work file
# but individual tools use replace directives for local modules
cmd_build() {
    GOWORK=off go mod tidy
    GOWORK=off go build -o $APP_NAME .
}

# Build Docker image from truvag3 root using Dockerfile.workspace
cmd_docker_build() {
    local truvag3_root="$(dirname "$(dirname "$SCRIPT_DIR")")"
    docker build -f "$SCRIPT_DIR/Dockerfile.workspace" \
        -t $APP_NAME:latest "$truvag3_root"
}

# Deploy to Kubernetes
cmd_deploy() {
    load_env

    # Build and load image
    cmd_docker_build
    kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Create ConfigMap from .env (user-configurable vars like APP_ENV, DEV_MODE, TRUVAG3_*)
    # The 3rd arg is the .env file path; extra args are tool-specific vars to include
    truvag3_create_configmap "${APP_NAME}-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"

    # Create secret for API keys (replace with your tool's keys)
    truvag3_create_tool_secret "${APP_NAME}-secrets" "$NAMESPACE" "YOUR_API_KEY"

    # Apply manifests and wait
    kubectl apply -f k8-deployment.yaml
    kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s

    echo "$APP_NAME deployed successfully!"
}

# Port forward with monitoring
cmd_forward_all() {
    kubectl port-forward -n $NAMESPACE svc/${APP_NAME}-service $PORT:80 &

    # Reuse existing monitoring forwards or start new ones
    nc -z localhost 3000 2>/dev/null || kubectl port-forward -n $NAMESPACE svc/grafana 3000:80 &
    nc -z localhost 9090 2>/dev/null || kubectl port-forward -n $NAMESPACE svc/prometheus 9090:9090 &
    nc -z localhost 16686 2>/dev/null || kubectl port-forward -n $NAMESPACE svc/jaeger-query 16686:80 &

    echo "Access points:"
    echo "  Tool:       http://localhost:$PORT"
    echo "  Grafana:    http://localhost:3000"
    echo "  Prometheus: http://localhost:9090"
    echo "  Jaeger:     http://localhost:16686"
}

# Main entry point
case "${1:-help}" in
    build)       cmd_build ;;
    run)         load_env && cmd_build && ./$APP_NAME ;;
    deploy)      cmd_deploy ;;
    forward-all) cmd_forward_all ;;
    logs)        kubectl logs -n $NAMESPACE -l app=$APP_NAME -f --tail=100 ;;
    status)      kubectl get pods,svc -n $NAMESPACE -l app=$APP_NAME ;;
    *)           echo "Usage: ./setup.sh {build|run|deploy|forward-all|logs|status}" ;;
esac
```

> **Note:** For a full-featured setup script with cluster creation, infrastructure setup, and comprehensive help, copy and adapt from [examples/stock-market-tool/setup.sh](https://github.com/truvaagents/truva-g3/blob/main/examples/stock-market-tool/setup.sh).

---

## 9. Testing Your Tool

### Local Testing

1. **Start Redis:**
   ```bash
   docker run -d -p 6379:6379 redis:alpine
   ```

2. **Set environment variables (or use `.env` file):**
   ```bash
   cp .env.example .env
   # Edit .env with your API keys, then:
   set -a && source .env && set +a
   ```

3. **Build and run the tool:**
   ```bash
   # GOWORK=off is required because the workspace has a go.work file
   # but individual tools use replace directives for local modules
   GOWORK=off go mod tidy
   GOWORK=off go build -o your-tool .
   ./your-tool

   # Or use setup.sh which handles GOWORK=off for you:
   ./setup.sh run
   ```

4. **Test capabilities:**
   ```bash
   # Check health
   curl http://localhost:8080/health

   # Get capability schema
   curl http://localhost:8080/api/capabilities/get_data/schema

   # Invoke capability
   curl -X POST http://localhost:8080/api/capabilities/get_data \
     -H "Content-Type: application/json" \
     -d '{"id": "test123"}'
   ```

### Verifying Discovery Registration

```bash
# Check registered capabilities in Redis
redis-cli HGETALL "truvag3:capabilities"

# Or use the registry viewer if deployed
curl http://localhost:8081/api/registry
```

### Testing with Tracing

1. **Start Jaeger (with OTLP HTTP receiver):**
   ```bash
   docker run -d --name jaeger \
     -p 16686:16686 \
     -p 4318:4318 \
     -e COLLECTOR_OTLP_ENABLED=true \
     jaegertracing/all-in-one:latest
   ```

2. **Set OTEL endpoint (HTTP, not gRPC):**
   ```bash
   export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
   ```

   > **Note:** TruvaG3 uses OTLP over HTTP (port 4318), not gRPC (port 4317). The endpoint must include the `http://` scheme prefix.

3. **View traces:**
   Open http://localhost:16686 in your browser

---

## 10. Best Practices

### Code Organization

1. **Keep files focused**: Each file should have a single responsibility
2. **Use consistent naming**: `your_tool.go`, `handlers.go`, `api_client.go`
3. **Group related capabilities**: Register related capabilities together

### Error Handling

1. **Two error helpers, two use cases**: Every tool should have both `sendError` and `sendUpstreamError`:
   - `sendError(rw, message, status, code)` — for **local** errors where you control the status code. Use for: decode failures, missing/invalid fields, Go interface errors (Redis client failures, framework interface errors).
   - `sendUpstreamError(rw, message, info)` — **only** for errors from outbound **HTTP API** calls. Uses `core.ClassifyUpstreamError(err)` which extracts the upstream HTTP status via regex `(?:status|error|code)[:\s]+(\d{3})` (see [core/tool_error.go](https://github.com/truvaagents/truva-g3/blob/main/core/tool_error.go)). If the regex doesn't match, defaults to 502/SERVICE_ERROR.

   **Decision rule:** Does the error come from an HTTP call where the upstream status code is embedded in the error message? → `sendUpstreamError`. Everything else → `sendError`. Tools backed by Go interfaces (e.g., `core.EpisodicMemory`, Redis clients) should use `sendError` with `http.StatusServiceUnavailable` for backend failures — these errors don't carry HTTP status codes that `ClassifyUpstreamError` can extract.
2. **Always set HTTP status codes for errors**: Call `rw.WriteHeader(status)` before encoding any `core.ToolResponse{Success: false, ...}`. Omitting this causes Go to default to HTTP 200, which makes the orchestrator treat the step as successful.
3. **Always provide fallbacks**: When external APIs fail, return mock/cached data when possible
4. **Log errors with context**: Include relevant identifiers in error logs
5. **Record errors on spans**: Use `telemetry.RecordSpanError(ctx, err)` for visibility

#### Upstream Error Handling

The orchestrator routes errors by HTTP status code — **400s** go to the LLM error analyzer for parameter correction, **429/5xx** trigger resilience retry, and **401/403** fail immediately. If your tool wraps an upstream 400 as 502, the orchestrator retries with the same broken parameters instead of fixing them.

```go
// ❌ WRONG: All upstream errors become 502
t.sendError(rw, "API call failed: "+err.Error(), http.StatusBadGateway, "API_ERROR")

// ✅ CORRECT: Classify the original error, then sanitize returned text.
info := core.ClassifyUpstreamError(err)
safeError := core.RedactSensitiveText(err.Error())
t.sendUpstreamError(rw, "API call failed: "+safeError, info)
```

`ClassifyUpstreamError` extracts the HTTP status from error messages (handles `"status 400"`, `"error 400"`, `"code: 429"`, etc.) and maps it to the correct tool response. See [API Reference](../reference/API_REFERENCE.md#classifyupstreamerror) for the full classification mapping.

> **Important: Error Message Format Contract**
>
> `ClassifyUpstreamError` uses the regex `(?:status|error|code)[:\s]+(\d{3})` to extract upstream HTTP status codes. Your API client error messages **must** include one of the keywords `status`, `error`, or `code` followed by a colon/space and the 3-digit HTTP status code. If the regex doesn't match, the error defaults to 502/SERVICE_ERROR and the orchestrator will pointlessly retry instead of routing to the LLM error analyzer.
>
> ```go
> // ✅ CORRECT — these all match the regex:
> fmt.Errorf("API error 429: rate limit exceeded")      // "error 429" matches
> fmt.Errorf("API returned status %d: %s", code, body)  // "status 400" matches
> fmt.Errorf("API error (status %d): %s", code, body)   // "status 400" matches
>
> // ❌ WRONG — these will NOT match:
> fmt.Errorf("rate limit exceeded (429)")    // "exceeded" is not a keyword
> fmt.Errorf("invalid API key")              // no status code at all
> fmt.Errorf("country not found")            // no status code at all
> ```
>
> For business logic errors (HTTP 200 but empty/invalid data), use the same pattern:
> `fmt.Errorf("API error 404: invalid symbol or no data available")` — this routes to the LLM error analyzer which can suggest corrected parameters.

### Performance

1. **Configure HTTP client timeouts**: Prevent hanging requests
2. **Use connection pooling**: Configure `MaxIdleConns` and `MaxIdleConnsPerHost`
3. **Cache responses**: Use `Memory.Set()` for frequently accessed data
4. **Use context cancellation**: Pass context to all API calls

### Security

1. **Never log sensitive data**: Don't log API keys or credentials
2. **Validate input**: Check for empty/invalid values before processing
3. **Use environment variables**: Never hardcode credentials
4. **Set appropriate timeouts**: Prevent resource exhaustion

### Observability

1. **Add span events at key points**:
   - Request received
   - Before external API call
   - After successful completion

2. **Use consistent log fields**: Follow the standard field naming conventions

3. **Include timing information**: Log API latencies for performance monitoring

### Framework-Aware Design

Before adding capabilities to your tool, understand what the orchestration layer already provides. This prevents duplicating functionality and keeps tools simple.

**Key Principle:** Tools should be **pure API wrappers**. Semantic understanding and data transformation belong in the orchestrator, not in tools.

#### What the Orchestrator Already Provides

The orchestrator's **Intelligent Parameter Binding** system handles data flow between steps automatically:

| Layer | Function | Example |
|-------|----------|---------|
| **Layer 1: Auto-Wiring** | Exact field name matching, type coercion | `{"city": "Paris"}` → `city` parameter |
| **Layer 2: Micro-Resolution** | LLM extracts parameters from source data | Search results → extracts destination names for weather lookup |
| **Layer 3: Error Analyzer** | Diagnoses if errors are fixable | "Invalid format" → suggests transformation |
| **Layer 4: Contextual Re-Resolution** | Computes derived values post-failure | Country name → derives currency code |

#### Common Anti-Pattern: Data Extraction Capabilities

**Don't do this** - adding a capability like `extract_structured_data` to parse data between steps:

```go
// ANTI-PATTERN: This duplicates Layer 2 functionality
t.RegisterCapability(core.Capability{
    Name:        "extract_structured_data",
    Description: "Extracts named entities from text using AI...",
    Handler:     t.handleExtract,  // Uses AI to parse output
})
```

**Why it's wrong:**
1. Layer 2 (Micro-Resolution) already does this automatically
2. Adds unnecessary LLM call and latency
3. Increases tool complexity
4. Tool now requires AI client initialization

**Do this instead** - let the orchestrator handle it:

```go
// CORRECT: Single capability, pure API wrapper
t.RegisterCapability(core.Capability{
    Name:        "web_search",
    Description: "Searches the web for information...",
    Handler:     t.handleSearch,  // Pure HTTP API call
})
// Data extraction between steps is handled by orchestrator's Layer 2
```

#### Before Adding AI to Your Tool, Ask:

1. **Is this data transformation between steps?** → Orchestrator Layer 2 handles it
2. **Is this error recovery/retry logic?** → Orchestrator Layers 3-4 handle it
3. **Is this capability selection?** → Orchestrator planner handles it
4. **Is this calling an external API?** → ✅ This belongs in a tool

#### Telemetry Implications

Tools **without** AI capabilities use tool-style telemetry initialization:

```go
// Tool-style: NewTool → initTelemetry
tool := NewYourTool()
initTelemetry("your-service")
```

Tools **with** AI capabilities (rare, for legitimate AI use cases) require agent-style initialization:

```go
// Agent-style: SetComponentType → initTelemetry → NewTool
core.SetCurrentComponentType(core.ComponentTypeTool)
initTelemetry("your-service")
tool := NewYourTool()  // AI client must be created AFTER telemetry
```

Most tools should use tool-style initialization. If you find yourself needing agent-style, reconsider whether the AI capability truly belongs in the tool.

### Tools Backed by Framework Interfaces

Not all tools wrap external HTTP APIs. Some tools expose **framework interfaces** as capabilities — for example, a tool that reads from `core.EpisodicMemory` or `core.SharedKnowledge` backed by Redis/Qdrant. These tools differ from HTTP-API tools in several ways:

| Aspect | HTTP API Tool | Interface-Backed Tool |
|--------|--------------|----------------------|
| **API client file** | `api_client.go` — traced HTTP client | `*_backends.go` — Go interface constructors |
| **HTTP client** | `telemetry.NewTracedHTTPClientWithTransport(...)` | Not needed |
| **Error helper** | `sendUpstreamError` + `ClassifyUpstreamError` | `sendError` with explicit status codes |
| **`api_latency` field** | Yes — track upstream HTTP call duration | No — only `duration_ms` for total handler duration |
| **Trace propagation** | `http.NewRequestWithContext(ctx, ...)` | Context passed to interface methods directly |

**Handler checklist differences for interface-backed tools:**
- Skip "Add span event before external API call" — replace with "Add span event before backend query"
- Skip `sendUpstreamError` — use `sendError(rw, msg, http.StatusServiceUnavailable, "BACKEND_ERROR")` for interface errors
- Skip `api_latency` in logs — interface calls don't have separate upstream timing
- Keep all other checklist items (ctx extraction, requestID, nil-checked logging, Counter/Histogram/RecordToolCall, span events)

**Example:** [examples/agentic-memory-tool](https://github.com/truvaagents/truva-g3/tree/main/examples/agentic-memory-tool) — reads from `core.EpisodicMemory`, `core.SharedKnowledge`, and `core.InvestigationCoordinator` interfaces. Uses `sendError` for all backend failures. No traced HTTP client.

### Graceful Degradation for Optional Backends

Some tools have capabilities that depend on optional infrastructure (e.g., vector DB, embedding endpoint, secondary cache). When the backend is unavailable, the capability should degrade gracefully rather than fail.

**Pattern:** Return `Success: true` with empty data, not `Success: false` with an error.

```go
// ✅ CORRECT: Graceful degradation — empty results, HTTP 200
if m.knowledge == nil {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(core.ToolResponse{
        Success: true,
        Data:    KnowledgeResponse{Fragments: []KnowledgeFragment{}, TotalCount: 0, Domain: m.domain},
    })
    telemetry.RecordToolCall("my-tool", "query_knowledge", durationMs, "success")
    return
}

// ❌ WRONG: Backend unavailability treated as error — orchestrator may retry pointlessly
if m.knowledge == nil {
    m.sendError(rw, "Knowledge backend unavailable", http.StatusServiceUnavailable, "BACKEND_UNAVAILABLE")
    return
}
```

**Why `Success: true`?** The orchestrator routes errors by HTTP status code. Returning 503 for an unavailable optional backend would trigger resilience retries — which will keep failing since the backend isn't going to appear. Returning 200 with empty data lets the orchestrator proceed with whatever data is available from other steps.

**Telemetry:** Record as `"success"` in `RecordToolCall` and `Counter` — the tool successfully handled the request by returning an empty result set. This keeps success rate metrics accurate (the tool isn't broken, the backend is just absent).

**When to use this pattern:**
- Backend is configured as optional at startup (e.g., Qdrant nil after `NewVectorSharedKnowledge` fails)
- The capability's value is additive, not essential (e.g., knowledge search enriches but isn't required)
- The empty result is a valid, interpretable response (e.g., "no knowledge fragments found")

**When NOT to use this pattern:**
- The backend is required for the capability to have any value (e.g., `query_events` without Redis — the tool can't function)
- The empty result would be misleading (e.g., "0 pods found" when the real answer is "I can't check")

### Reference Implementations

The framework ships several fully-tested tools that serve as copy-paste starting points:

| Tool | Type | Capabilities | Pattern |
|---|---|---|---|
| [`examples/stock-market-tool/`](https://github.com/truvaagents/truva-g3/tree/main/examples/stock-market-tool) | HTTP API tool | 4 (stock data) | Traced HTTP client, API key secret, upstream error classification |
| [`examples/scheduler-tool/`](https://github.com/truvaagents/truva-g3/tree/main/examples/scheduler-tool) | Interface-backed tool | 5 (schedule CRUD) | Framework interfaces (`ScheduleStore`, `TaskDispatcher`), leader-elected `Runnable`, distributed lock |
| [`examples/playwright-tool/`](https://github.com/truvaagents/truva-g3/tree/main/examples/playwright-tool) | Script execution tool | 3 (browser automation) | Script directory, S3 artifact storage, security context |

> **Note**: [`examples/scheduled-executor/`](https://github.com/truvaagents/truva-g3/tree/main/examples/scheduled-executor) is a `core.BaseAgent`, not a `BaseTool` -- it needs Discovery access for target-agent resolution. It's documented in the [Agent Development Guide](AGENT_DEVELOPMENT_GUIDE.md) and the [Scheduled Tasks Guide](../orchestration/SCHEDULED_TASKS_GUIDE.md).

---

## 11. Troubleshooting

### Tool Not Registering

**Symptoms:** Capability not appearing in registry

**Solutions:**
1. Check Redis connection: `redis-cli ping`
2. Verify REDIS_URL is correct
3. Check logs for registration errors
4. Ensure `WithDiscovery(true, "redis")` is set

### External API Failures

**Symptoms:** Always returning fallback/mock data

**Solutions:**
1. Verify API key is set and valid
2. Check network connectivity to external API
3. Look for rate limiting errors in logs
4. Verify API endpoint URLs are correct

### Missing Traces in Jaeger

**Symptoms:** No traces appearing for requests

**Solutions:**
1. Verify OTEL_EXPORTER_OTLP_ENDPOINT is set
2. Check that `TracingMiddleware` is added to framework
3. Verify Jaeger/OTEL collector is running
4. Check for telemetry initialization errors in logs

### Context Not Propagating

**Symptoms:** Log entries missing trace IDs

**Solutions:**
1. Ensure using `r.Context()` in handlers
2. Use `WithContext` logging methods
3. Pass context through to API client methods
4. Verify middleware order (tracing should be first)

---

## 12. Advanced Telemetry

This section covers advanced telemetry capabilities that enhance observability beyond the basic patterns shown in earlier sections. While the basic patterns (span events, error recording) are sufficient for most tools, these advanced features provide deeper insights for production debugging and performance analysis.

### Custom Metrics

The telemetry module provides three metric types for tracking tool performance and business metrics.

#### Counter Metrics

Use counters for event counts (requests, errors, cache hits/misses):

```go
import "github.com/truvaagents/truva-g3/telemetry"

func (t *YourTool) handleGetData(rw http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Count successful requests
    telemetry.Counter("tool.requests.total",
        "capability", "get_data",
        "status", "success",
    )

    // Count errors by type
    if err != nil {
        telemetry.Counter("tool.errors.total",
            "capability", "get_data",
            "error_type", categorizeError(err),
        )
    }

    // Track cache behavior
    if cacheHit {
        telemetry.Counter("tool.cache.hits", "capability", "get_data")
    } else {
        telemetry.Counter("tool.cache.misses", "capability", "get_data")
    }
}
```

#### Histogram Metrics

Use histograms for latency distributions and value distributions:

```go
func (t *YourTool) handleGetData(rw http.ResponseWriter, r *http.Request) {
    startTime := time.Now()
    ctx := r.Context()

    // ... process request ...

    // Record request latency distribution
    telemetry.Histogram("tool.request.duration_ms",
        float64(time.Since(startTime).Milliseconds()),
        "capability", "get_data",
    )

    // Record response size distribution
    telemetry.Histogram("tool.response.size_bytes",
        float64(len(responseData)),
        "capability", "get_data",
    )
}
```

#### Gauge Metrics

Use gauges for current values that can go up or down:

```go
func (t *YourTool) updatePoolMetrics() {
    // Track connection pool size
    telemetry.Gauge("tool.connections.active",
        float64(t.pool.ActiveConnections()),
        "pool", "http_client",
    )

    // Track queue depth
    telemetry.Gauge("tool.queue.depth",
        float64(t.requestQueue.Len()),
        "capability", "get_data",
    )
}
```

**Caveat:** If you don't use custom metrics, you'll miss:
- Granular performance tracking per capability
- Business-level metrics (conversion rates, data volumes)
- Alerting based on application-specific thresholds

> **Reference:** For complete metrics API details, see [telemetry/README.md - Section 3: The Three Types of Metrics](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md#3-the-three-types-of-metrics-and-when-to-use-each).

### Log-Trace Correlation with GetTraceContext

Extract trace context to include in external logging systems or API responses:

```go
import "github.com/truvaagents/truva-g3/telemetry"

func (t *YourTool) handleGetData(rw http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Extract trace context for manual correlation
    tc := telemetry.GetTraceContext(ctx)

    // Include in response headers for client-side debugging
    if tc.TraceID != "" {
        rw.Header().Set("X-Trace-ID", tc.TraceID)
        rw.Header().Set("X-Span-ID", tc.SpanID)
    }

    // Include in structured logs manually (usually automatic with WithContext)
    t.Logger.InfoWithContext(ctx, "Processing request", map[string]interface{}{
        "trace_id": tc.TraceID,
        "span_id":  tc.SpanID,
        "sampled":  tc.Sampled,
    })

    // Include in error responses for debugging
    if err != nil {
        response := core.ToolResponse{
            Success: false,
            Error: &core.ToolError{
                Code:    "PROCESSING_ERROR",
                Message: err.Error(),
                Details: map[string]string{
                    "trace_id": tc.TraceID, // Helps support trace the issue
                },
            },
        }
        json.NewEncoder(rw).Encode(response)
        return
    }
}
```

**Caveat:** If you don't use GetTraceContext:
- Clients cannot easily locate traces for their specific requests
- Support tickets lack trace correlation information
- External logging systems cannot link to Jaeger traces

> **Reference:** For complete trace correlation patterns, see [DISTRIBUTED_TRACING_GUIDE.md - Section 5: Trace-Log Correlation](../observability/DISTRIBUTED_TRACING_GUIDE.md#5-trace-log-correlation-the-magic-glue).

### Span Enrichment with SetSpanAttributes

Add business context to spans for better trace analysis:

```go
import (
    "github.com/truvaagents/truva-g3/telemetry"
    "go.opentelemetry.io/otel/attribute"
)

func (t *YourTool) handleGetData(rw http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Add business context to the span
    telemetry.SetSpanAttributes(ctx,
        attribute.String("truvag3.tool.name", "your-tool"),
        attribute.String("truvag3.capability", "get_data"),
        attribute.String("request.id", req.ID),
        attribute.Bool("cache.hit", cacheHit),
        attribute.Int("retry.count", retryCount),
    )

    // Add data source information
    telemetry.SetSpanAttributes(ctx,
        attribute.String("data.source", "external_api"),
        attribute.String("api.version", "v2"),
        attribute.Int("response.items", len(results)),
    )
}
```

**Caveat:** If you don't use SetSpanAttributes:
- Traces lack business context for filtering
- Cannot search traces by business identifiers
- Debugging requires correlating multiple data sources manually

> **Reference:** For attribute naming conventions and best practices, see [DISTRIBUTED_TRACING_GUIDE.md - Section 11: Required Patterns](../observability/DISTRIBUTED_TRACING_GUIDE.md#11-required-patterns-for-framework-level-tracing).

### Unified Metrics API

For standardized tool metrics that integrate with dashboards:

```go
import "github.com/truvaagents/truva-g3/telemetry"

func (t *YourTool) handleGetData(rw http.ResponseWriter, r *http.Request) {
    startTime := time.Now()
    ctx := r.Context()

    // ... process request ...

    // Record standardized tool call metric
    // This integrates with pre-built Grafana dashboards
    status := "success"
    if err != nil { status = "error" }
    telemetry.RecordToolCall("your-tool", "get_data",
        float64(time.Since(startTime).Milliseconds()), status)
}
```

The unified metrics ensure consistency across all tools and enable:
- Pre-built Grafana dashboards
- Cross-tool comparisons
- SLA monitoring

**Caveat:** If you don't use RecordToolCall:
- Your tool won't appear in unified dashboards
- Cross-tool performance comparisons are impossible
- SLA calculations exclude your tool

### Baggage Propagation

Pass custom key-value pairs through the entire request chain:

```go
import "github.com/truvaagents/truva-g3/telemetry"

func (t *YourTool) handleGetData(rw http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Read baggage from upstream (set by agent or another tool)
    baggage := telemetry.GetBaggage(ctx)
    requestID := baggage["request_id"]
    userTier := baggage["user_tier"]

    t.Logger.InfoWithContext(ctx, "Processing request", map[string]interface{}{
        "request_id": requestID,
        "user_tier":  userTier,
    })

    // Add your own baggage for downstream services
    ctx = telemetry.WithBaggage(ctx, "data_source", "external_api")
    ctx = telemetry.WithBaggage(ctx, "cache_status", "miss")

    // Pass enriched context to external calls
    req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
    // ... TracedHTTPClient will propagate baggage in headers
}
```

**Caveat:** If you don't use baggage propagation:
- Correlation across service boundaries is limited to trace IDs
- Business context (user tier, feature flags) must be passed explicitly
- Request routing decisions cannot be based on upstream context

> **Reference:** For baggage implementation details, see [telemetry/README.md - Section 5: Progressive Disclosure](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md#5-progressive-disclosure-from-simple-to-advanced).

### When to Use Advanced Telemetry

| Feature | Use When | Skip When |
|---------|----------|-----------|
| Custom Metrics | You need alerting on specific thresholds | Basic request counting is sufficient |
| GetTraceContext | Exposing trace IDs to clients/external systems | All tracing is internal |
| SetSpanAttributes | Rich business context aids debugging | Simple request/response flows |
| RecordToolCall | Tool should appear in unified dashboards | Tool is internal/temporary |
| Baggage Propagation | Cross-service context is needed | Tool has no downstream dependencies |

### Complete Handler with Full Telemetry

Here's a handler that demonstrates all advanced telemetry features:

```go
func (t *YourTool) handleGetDataAdvanced(rw http.ResponseWriter, r *http.Request) {
    startTime := time.Now()
    ctx := r.Context()

    // 1. Extract trace context for response headers
    tc := telemetry.GetTraceContext(ctx)
    if tc.TraceID != "" {
        rw.Header().Set("X-Trace-ID", tc.TraceID)
    }

    // 2. Read upstream baggage
    baggage := telemetry.GetBaggage(ctx)
    requestID := baggage["request_id"]

    // 3. Add span attributes for business context
    telemetry.SetSpanAttributes(ctx,
        attribute.String("truvag3.tool.name", "your-tool"),
        attribute.String("truvag3.capability", "get_data"),
    )

    // 4. Add span event for request start (include request_id for correlation)
    telemetry.AddSpanEvent(ctx, "request_received",
        attribute.String("request_id", requestID),
        attribute.String("method", r.Method),
    )

    // 5. Decode and validate request
    var req GetDataRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        telemetry.RecordSpanError(ctx, err)
        telemetry.Counter("tool.errors.total",
            "capability", "get_data",
            "error_type", "decode_error",
        )
        t.sendError(rw, "Invalid request", http.StatusBadRequest, "INVALID_REQUEST")
        return
    }

    // 6. Check cache
    cacheHit := false
    if cached := t.Memory.Get(ctx, "data:"+req.ID); cached != "" {
        cacheHit = true
        telemetry.Counter("tool.cache.hits", "capability", "get_data")
        telemetry.SetSpanAttributes(ctx, attribute.Bool("cache.hit", true))
    } else {
        telemetry.Counter("tool.cache.misses", "capability", "get_data")
    }

    // 7. Add baggage for downstream calls
    ctx = telemetry.WithBaggage(ctx, "cache_status", map[bool]string{true: "hit", false: "miss"}[cacheHit])

    // 8. Call external API
    telemetry.AddSpanEvent(ctx, "calling_external_api",
        attribute.String("request_id", requestID),
        attribute.String("id", req.ID),
    )

    data, err := t.client.GetData(ctx, req.ID)
    if err != nil {
        safeError := core.RedactSensitiveText(err.Error())
        telemetry.RecordSpanError(ctx, fmt.Errorf("%s", safeError))
        telemetry.Counter("tool.errors.total",
            "capability", "get_data",
            "error_type", "api_error",
        )
        // Use sendUpstreamError for external API failures
        t.sendUpstreamError(rw, "Data fetch failed: "+safeError,
            core.ClassifyUpstreamError(err))
        return
    }

    // 9. Record success metrics
    duration := time.Since(startTime)
    telemetry.Histogram("tool.request.duration_ms",
        float64(duration.Milliseconds()),
        "capability", "get_data",
    )
    telemetry.Counter("tool.requests.total",
        "capability", "get_data",
        "status", "success",
    )

    // 10. Use unified metrics for dashboard integration
    telemetry.RecordToolCall("your-tool", "get_data",
        float64(duration.Milliseconds()), "success")

    // 11. Add completion span event (include request_id for correlation)
    telemetry.AddSpanEvent(ctx, "request_completed",
        attribute.String("request_id", requestID),
        attribute.String("id", req.ID),
        attribute.Int64("duration_ms", duration.Milliseconds()),
    )

    // 12. Log with context
    t.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
        "id":          req.ID,
        "request_id":  requestID,
        "cache_hit":   cacheHit,
        "duration_ms": duration.Milliseconds(),
    })

    // 13. Send response
    rw.Header().Set("Content-Type", "application/json")
    json.NewEncoder(rw).Encode(core.ToolResponse{Success: true, Data: data})
}
```

---

## Quick Reference: File Templates

### Minimal Tool Implementation

```go
// main.go
package main

import (
    "context"
    "encoding/json"
    "net/http"
    "os"
    "strconv"

    "github.com/truvaagents/truva-g3/core"
)

type MinimalTool struct {
    *core.BaseTool
}

func main() {
    tool := &MinimalTool{BaseTool: core.NewTool("minimal")}

    tool.RegisterCapability(core.Capability{
        Name:        "hello",
        Description: "Returns a greeting. Required: name (string).",
        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler: func(w http.ResponseWriter, r *http.Request) {
            var req struct{ Name string `json:"name"` }
            json.NewDecoder(r.Body).Decode(&req)

            w.Header().Set("Content-Type", "application/json")
            response := core.ToolResponse{
                Success: true,
                Data: map[string]string{
                    "message": "Hello, " + req.Name,
                },
            }
            json.NewEncoder(w).Encode(response)
        },
        InputSummary: &core.SchemaSummary{
            RequiredFields: []core.FieldHint{
                {Name: "name", Type: "string", Example: "World", Description: "Name to greet"},
            },
        },
    })

    port := 8080
    if p, err := strconv.Atoi(os.Getenv("PORT")); err == nil {
        port = p
    }

    framework, _ := core.NewFramework(tool,
        core.WithPort(port),
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),
    )

    framework.Run(context.Background())
}
```

### Response Types

TruvaG3 provides standard response structures for consistency:

```go
// core.ToolResponse wraps successful and error responses
type ToolResponse struct {
    Success bool        `json:"success"`
    Data    interface{} `json:"data,omitempty"`
    Error   *ToolError  `json:"error,omitempty"`
}

// core.ToolError provides structured error information
type ToolError struct {
    Code      string        `json:"code"`      // Error code (e.g., "INVALID_REQUEST", "API_ERROR")
    Message   string        `json:"message"`   // Human-readable message
    Category  ErrorCategory `json:"category"`  // Classification for orchestrator routing
    Retryable bool          `json:"retryable"` // Whether client should retry
    Details   map[string]string `json:"details,omitempty"` // Additional context (e.g., "retry_after", "hint")
}
```

**Note:** Some tools (like stock-market-tool) return raw JSON responses without the `core.ToolResponse` wrapper. Both patterns work, but using `core.ToolResponse` provides more consistency for error handling.

**Error response helpers:** Every tool should define two error helpers — `sendError` for local validation errors and `sendUpstreamError` for upstream API failures. See [Step 4: Implement Handlers](#6-step-4-implement-handlers) for the implementation pattern and [Best Practices: Error Handling](#error-handling) for when to use which.

---

## See Also

### Core Documentation
- [TOOL_SCHEMA_DISCOVERY_GUIDE.md](TOOL_SCHEMA_DISCOVERY_GUIDE.md) - Deep dive into AI payload generation
- [core/README.md](https://github.com/truvaagents/truva-g3/blob/main/core/README.md) - Framework architecture reference

### Telemetry & Observability
- [DISTRIBUTED_TRACING_GUIDE.md](../observability/DISTRIBUTED_TRACING_GUIDE.md) - Complete tracing implementation
  - [Section 6: Server-Side TracingMiddleware](../observability/DISTRIBUTED_TRACING_GUIDE.md#6-implementation-server-side-tracingmiddleware)
  - [Section 7: Client-Side TracedHTTPClient](../observability/DISTRIBUTED_TRACING_GUIDE.md#7-implementation-client-side-tracedhttpclient)
  - [Section 11: Required Patterns for Framework-Level Tracing](../observability/DISTRIBUTED_TRACING_GUIDE.md#11-required-patterns-for-framework-level-tracing)
  - [Section 14: Quick Reference](../observability/DISTRIBUTED_TRACING_GUIDE.md#14-quick-reference)
- [LOGGING_IMPLEMENTATION_GUIDE.md](../observability/LOGGING_IMPLEMENTATION_GUIDE.md) - Logging patterns and standards
  - [Section 5: Where to Use Each Logger Method](../observability/LOGGING_IMPLEMENTATION_GUIDE.md#5-where-to-use-each-logger-method)
  - [Section 7: Tool Logging Complete Example](../observability/LOGGING_IMPLEMENTATION_GUIDE.md#7-tool-logging-complete-example)
  - [Section 8: Handler Logging with Trace Correlation](../observability/LOGGING_IMPLEMENTATION_GUIDE.md#8-handler-logging-with-trace-correlation)
  - [Section 10: Structured Logging Field Naming Standards](../observability/LOGGING_IMPLEMENTATION_GUIDE.md#10-structured-logging-field-naming-standards)
- [telemetry/README.md](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md) - Telemetry module API reference

### Reference Implementations

| Tool | Key Features | HTTP Client Tracing |
|------|--------------|---------------------|
| [examples/stock-market-tool](https://github.com/truvaagents/truva-g3/tree/main/examples/stock-market-tool) | Separate API client file (`finnhub_client.go`), mock data fallback, Finnhub integration | Plain `http.Client` (external API) |
| [examples/weather-tool-v2](https://github.com/truvaagents/truva-g3/tree/main/examples/weather-tool-v2) | Embedded HTTP calls in handlers, `core.ToolResponse` wrapper, coordinate validation | `otelhttp.NewTransport()` |
| [examples/agent-with-telemetry](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-telemetry) | **Recommended pattern**: `telemetry.NewTracedHTTPClientWithTransport()` | Traced client (for TruvaG3 service calls) |

> **Best Practice:** Use `telemetry.NewTracedHTTPClient*()` when your tool calls other TruvaG3 services. Use plain `http.Client` when calling external third-party APIs.
