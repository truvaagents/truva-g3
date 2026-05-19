# Intelligent Error Handling Guide

Welcome to intelligent error handling in TruvaG3! This guide will walk you through how tools report errors and how agents recover from them intelligently. Think of it as teaching your components to have a meaningful conversation when things go wrong.

## Table of Contents

- [The Problem We're Solving](#the-problem-were-solving)
- [The Solution: A Clear Separation of Concerns](#the-solution-a-clear-separation-of-concerns)
- [Part 1: The Protocol Types (in core)](#part-1-the-protocol-types-in-core)
- [Part 2: Tool Implementation](#part-2-tool-implementation)
- [Part 3: Agent Implementation](#part-3-agent-implementation)
- [Part 4: Real-World Example Flow](#part-4-real-world-example-flow)
- [Configuration Options](#configuration-options)
- [Best Practices](#best-practices)
- [Quick Reference](#quick-reference)
- [Orchestration Module: Multi-Layer Type Safety](#orchestration-module-multi-layer-type-safety)

## The Problem We're Solving

Imagine you're at a restaurant. You order a "cheeseburger with extra pickles" and the waiter comes back saying:

> "Here's a grilled cheese sandwich."

That's not helpful! You wanted a cheeseburger. The waiter should have said:

> "Sorry, we're out of beef patties today. Would you like a chicken burger instead, or we have great veggie options?"

**This is exactly what happens with traditional API error handling.** When a tool fails, it often:

1. Returns mock/fake data (hiding the real problem)
2. Returns cryptic error messages with no context
3. Gives up without explaining what could be done differently

**Intelligent error handling fixes this.** Tools tell agents *exactly* what went wrong with enough context for the agent (or its AI) to try again with corrected input.

## The Solution: A Clear Separation of Concerns

TruvaG3 splits error handling into two clear roles:

| Component | Responsibility | Does NOT |
|-----------|---------------|----------|
| **Tool** | Reports what happened (error code, category, context) | Suggest fixes, implement retry logic, return mock data |
| **Agent** | Analyzes errors, decides retry strategy, uses AI to correct payloads | Hide errors from users |

Think of it like a doctor and a patient:
- **Patient (Tool)**: "I have a headache, it's on the right side, started yesterday, pain level 7"
- **Doctor (Agent)**: "Based on your symptoms, let's try this treatment..."

The patient describes the problem clearly. The doctor decides how to fix it.

## Part 1: The Protocol Types (in core)

TruvaG3 provides three core types for error communication. These live in the `core` package because they're the shared vocabulary between tools and agents.

### ErrorCategory: Classifying Errors

Error categories help agents quickly decide how to handle errors:

```go
import "github.com/truvaagents/truva-g3/core"

// Available categories
core.CategoryInputError   // Bad input format - might be fixable
core.CategoryNotFound     // Resource doesn't exist - might exist with different parameters
core.CategoryRateLimit    // Too many requests - wait and retry
core.CategoryAuthError    // Authentication failed - NOT fixable by retry
core.CategoryServiceError // Backend service down - transient, retry later
```

**When to use each:**

| Category | Example | Typically Retryable? |
|----------|---------|---------------------|
| `CategoryInputError` | Missing required field | Yes, with fixed input |
| `CategoryNotFound` | City "NYC" not found | Yes, try "New York, US" |
| `CategoryRateLimit` | API quota exceeded | Yes, after waiting |
| `CategoryAuthError` | Invalid API key | No, needs config fix |
| `CategoryServiceError` | Weather API is down | Yes, same input later |

### ToolError: Structured Error Information

`ToolError` provides all the context an agent needs to understand and potentially fix an error:

```go
type ToolError struct {
    Code      string            `json:"code"`      // Machine-readable: "LOCATION_NOT_FOUND"
    Message   string            `json:"message"`   // Human-readable: "City 'NYC' not found"
    Category  ErrorCategory     `json:"category"`  // Classification for routing
    Retryable bool              `json:"retryable"` // Can agent retry with different input?
    Details   map[string]string `json:"details"`   // Context for AI analysis
}
```

**Creating a ToolError:**

```go
err := &core.ToolError{
    Code:      "LOCATION_NOT_FOUND",
    Message:   "Location 'Flower Mound, TX' not found in weather database",
    Category:  core.CategoryNotFound,
    Retryable: true,  // Agent CAN try with corrected input
    Details: map[string]string{
        "original_location": "Flower Mound, TX",
        "hint":              "Try 'City, Country' format (e.g., 'London, UK')",
    },
}
```

### ToolResponse: Standard Response Envelope

All tool responses should use this envelope for consistency:

```go
type ToolResponse struct {
    Success bool        `json:"success"`          // Did it work?
    Data    interface{} `json:"data,omitempty"`   // Result if successful
    Error   *ToolError  `json:"error,omitempty"`  // Error details if failed
}
```

**Success response:**

```go
response := core.ToolResponse{
    Success: true,
    Data: map[string]interface{}{
        "temperature": 22.5,
        "condition":   "sunny",
        "location":    "London",
    },
}
```

**Error response:**

```go
response := core.ToolResponse{
    Success: false,
    Error: &core.ToolError{
        Code:      "LOCATION_NOT_FOUND",
        Message:   "City 'XYZ123' not found",
        Category:  core.CategoryNotFound,
        Retryable: true,
    },
}
```

### HTTPStatusForCategory: Consistent HTTP Status Codes

Use this helper to return the right HTTP status code:

```go
// Returns appropriate HTTP status for each category
status := core.HTTPStatusForCategory(err.Category)

// Mapping:
// CategoryInputError   → 400 Bad Request
// CategoryNotFound     → 404 Not Found
// CategoryAuthError    → 401 Unauthorized
// CategoryRateLimit    → 429 Too Many Requests
// CategoryServiceError → 503 Service Unavailable
```

## Part 2: Tool Implementation

Tools have one job: **report errors clearly and honestly**. Never return mock data. Never implement retry logic. Just describe what happened.

### Step 1: Handle the Capability Request

When an agent calls your tool, you need to handle the request in a specific order. Here's what happens:

1. **Parse the request** - Read the JSON body sent by the agent
2. **Validate inputs** - Check that required fields are present and valid
3. **Call the real API** - Make the actual external API call
4. **Handle errors** - If something fails, classify it and return a structured error
5. **Return success** - If everything works, return the data

Notice how each error scenario returns a `ToolError` with appropriate category and whether it's retryable. The tool never guesses or returns fake data.

```go
func (w *WeatherTool) handleCurrentWeather(rw http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Step 1: Parse the request
    var req struct {
        Location string `json:"location"`
        Units    string `json:"units"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        // Bad JSON = not retryable (agent sent malformed data)
        w.sendError(rw, &core.ToolError{
            Code:      "INVALID_REQUEST",
            Message:   "Failed to parse request body",
            Category:  core.CategoryInputError,
            Retryable: false,
        })
        return
    }

    // Step 2: Validate inputs
    if req.Location == "" {
        // Missing field = retryable (agent can add the field)
        w.sendError(rw, &core.ToolError{
            Code:      "MISSING_LOCATION",
            Message:   "Location is required",
            Category:  core.CategoryInputError,
            Retryable: true,
            Details: map[string]string{
                "hint": "Provide a location like 'London, UK' or 'Tokyo, Japan'",
            },
        })
        return
    }

    // Step 3: Call the real API
    weather, err := w.fetchWeatherFromAPI(ctx, req.Location, req.Units)

    // Step 4: Handle errors
    if err != nil {
        toolErr := w.classifyAPIError(err, req.Location)
        w.sendError(rw, toolErr)
        return
    }

    // Step 5: Return success
    w.sendSuccess(rw, weather)
}
```

### Step 2: Classify API Errors

> **Recommended for upstream API calls:** Use `core.ClassifyUpstreamError(err)` instead of writing per-tool classification logic. It extracts HTTP status codes from error messages (formats like `"error 400"`, `"status 429"`, `"code: 502"`) and returns the correct `(HTTPStatus, Category, Retryable)` tuple.
>
> - Upstream 400/404/409/422 → Tool returns 400 → LLM error analyzer (parameter correction)
> - Upstream 401/403 → Tool returns 401/403 → Fail immediately
> - Upstream 429 → Tool returns 429 → Resilience retry with exponential backoff
> - Upstream 5xx / unknown → Tool returns 502 → Resilience retry with exponential backoff
>
> See: [API Reference — ClassifyUpstreamError](../reference/API_REFERENCE.md#classifyupstreamerror) and [Tool Development Guide — Upstream Error Handling](../building/TOOL_DEVELOPMENT_GUIDE.md#upstream-error-handling)

When an external API fails, your tool needs to translate that failure into a structured `ToolError`. This is where you add intelligence - examining the raw error and deciding:

- **What category?** Is it a user input problem, authentication issue, or server error?
- **Is it retryable?** Can the agent fix this by changing the input, or is it unfixable?
- **What hints can we provide?** What information helps the agent (or AI) correct the request?

Here's the decision process:

1. **Check for "not found" errors** → User might have misspelled something → Retryable with corrected input
2. **Check for authentication errors** → API key is wrong → NOT retryable (agent can't fix credentials)
3. **Check for rate limit errors** → Too many requests → Retryable after waiting
4. **Default to service error** → Something unexpected → Retryable (might be transient)

```go
func (w *WeatherTool) classifyAPIError(err error, location string) *core.ToolError {
    errStr := err.Error()

    // Check 1: Location not found (agent can retry with different spelling)
    if strings.Contains(errStr, "city not found") || strings.Contains(errStr, "404") {
        return &core.ToolError{
            Code:      "LOCATION_NOT_FOUND",
            Message:   fmt.Sprintf("Location '%s' not found in weather database", location),
            Category:  core.CategoryNotFound,
            Retryable: true, // Agent CAN fix this with corrected location
            Details: map[string]string{
                "original_location": location,
                "hint":              "Try 'City, Country' format (e.g., 'Paris, France')",
            },
        }
    }

    // Check 2: Authentication failed (agent CANNOT fix this)
    if strings.Contains(errStr, "401") || strings.Contains(errStr, "invalid API key") {
        return &core.ToolError{
            Code:      "API_KEY_INVALID",
            Message:   "Weather API authentication failed",
            Category:  core.CategoryAuthError,
            Retryable: false, // Agent can't fix API keys - needs human intervention
        }
    }

    // Check 3: Rate limited (agent should wait and retry)
    if strings.Contains(errStr, "429") || strings.Contains(errStr, "rate limit") {
        return &core.ToolError{
            Code:      "RATE_LIMIT_EXCEEDED",
            Message:   "Weather API rate limit exceeded",
            Category:  core.CategoryRateLimit,
            Retryable: true, // Agent CAN retry after waiting
            Details: map[string]string{
                "retry_after": "60s", // Tell agent how long to wait
            },
        }
    }

    // Check 4: Default - treat as temporary service error
    return &core.ToolError{
        Code:      "SERVICE_UNAVAILABLE",
        Message:   "Weather service temporarily unavailable",
        Category:  core.CategoryServiceError,
        Retryable: true, // Agent CAN retry with same input later
        Details: map[string]string{
            "original_error": errStr, // Include raw error for debugging
        },
    }
}
```

### Step 3: Send Responses with Correct HTTP Status

The final piece is sending the response back to the agent. Two key points:

1. **Use `HTTPStatusForCategory()`** - This ensures the HTTP status code matches the error category. The agent uses this status code to make quick routing decisions.
2. **Always wrap in `ToolResponse`** - Whether success or failure, use the standard envelope so agents can parse it consistently.

```go
func (w *WeatherTool) sendError(rw http.ResponseWriter, toolErr *core.ToolError) {
    // Set content type so agent knows to parse JSON
    rw.Header().Set("Content-Type", "application/json")

    // Use the helper to get the right HTTP status (400, 401, 404, 429, 503)
    rw.WriteHeader(core.HTTPStatusForCategory(toolErr.Category))

    // Wrap in standard envelope
    response := core.ToolResponse{
        Success: false,
        Error:   toolErr,
    }
    json.NewEncoder(rw).Encode(response)
}

func (w *WeatherTool) sendSuccess(rw http.ResponseWriter, data interface{}) {
    rw.Header().Set("Content-Type", "application/json")
    rw.WriteHeader(http.StatusOK)

    // Wrap in standard envelope
    response := core.ToolResponse{
        Success: true,
        Data:    data,
    }
    json.NewEncoder(rw).Encode(response)
}
```

### Common Tool Error Codes

Here are recommended error codes for common scenarios:

```go
// Location/Resource errors
const (
    ErrCodeLocationNotFound = "LOCATION_NOT_FOUND"
    ErrCodeSymbolNotFound   = "SYMBOL_NOT_FOUND"
    ErrCodeResourceNotFound = "RESOURCE_NOT_FOUND"
)

// Input validation errors
const (
    ErrCodeInvalidRequest   = "INVALID_REQUEST"
    ErrCodeMissingField     = "MISSING_REQUIRED_FIELD"
    ErrCodeInvalidFormat    = "INVALID_FORMAT"
)

// Authentication errors
const (
    ErrCodeAPIKeyMissing  = "API_KEY_MISSING"
    ErrCodeAPIKeyInvalid  = "API_KEY_INVALID"
    ErrCodeUnauthorized   = "UNAUTHORIZED"
)

// Service errors
const (
    ErrCodeRateLimitExceeded  = "RATE_LIMIT_EXCEEDED"
    ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
    ErrCodeTimeout            = "REQUEST_TIMEOUT"
)
```

## Part 3: Agent Implementation

Agents receive errors from tools and decide how to handle them. With AI, agents can even analyze errors and generate corrected payloads.

> **Production Recommendation**: The retry logic shown in this section is for **educational purposes**. For production agents, use the **orchestration module** which provides:
> - **Layer 4 Semantic Retry**: LLM-powered error analysis
> - **Automatic Parameter Correction**: AI fixes payloads based on error context
> - **Workflow-Level Coordination**: Retry logic integrated with multi-step workflows
>
> See: [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md#-when-to-use-the-orchestration-module)

### The Retry Decision Flow

When a tool returns an error, the agent needs to answer two questions:

1. **Should I retry?** (Based on HTTP status code)
2. **With what payload?** (Same payload or AI-corrected payload)

Here's the decision tree:

```
HTTP Status Code Decision:
┌─────────────────────────────────────────────────────────────────────┐
│ 2xx (Success)     → No retry needed                                 │
│ 401/403 (Auth)    → NOT retryable (agent can't fix credentials)     │
│ 429 (Rate Limit)  → Retry after backoff (check retry_after)         │
│ 5xx (Server)      → Retry with SAME payload (transient error)       │
│ 4xx (Client)      → Check ToolError.Retryable for AI correction     │
└─────────────────────────────────────────────────────────────────────┘

Retry Strategy:
┌─────────────────────────────────────────────────────────────────────┐
│ 5xx (Server Error) → Retry with SAME input (server might recover)   │
│ 4xx (Client Error) → Retry with CORRECTED input (AI fixes payload)  │
└─────────────────────────────────────────────────────────────────────┘
```

### Agent Retry Configuration

Before implementing retry logic, define these configuration types. They live in your agent code, not in core:

```go
// RetryConfig controls how the agent handles failed tool calls
type RetryConfig struct {
    MaxRetries      int           // How many times to retry (default: 3)
    UseAI           bool          // Should AI analyze errors and suggest fixes?
    BackoffDuration time.Duration // How long to wait between retries
}

// ErrorContext bundles everything AI needs to analyze an error
type ErrorContext struct {
    HTTPStatus      int                    // e.g., 404
    OriginalRequest map[string]interface{} // The payload that failed
    ToolError       *core.ToolError        // Structured error from tool
    ToolName        string                 // e.g., "weather-tool"
    Capability      string                 // e.g., "current_weather"
    AttemptNumber   int                    // Which retry attempt this is
}

// Default configuration - sensible starting point
func DefaultRetryConfig() RetryConfig {
    return RetryConfig{
        MaxRetries:      3,
        UseAI:           true,
        BackoffDuration: 1 * time.Second,
    }
}
```

### Implementing Intelligent Retry

This is the heart of intelligent error handling. Here's how the retry loop works:

**Step-by-step process:**

1. **Make the HTTP call** to the tool
2. **Check for network errors** - If the call didn't even reach the tool, retry with the same payload
3. **Check for success (2xx)** - If successful, we're done!
4. **Parse the error response** - Extract the `ToolError` from the response body
5. **Route based on HTTP status:**
   - **401/403 (Auth)** → Stop immediately. Agent can't fix credentials.
   - **429 (Rate limit)** → Wait the specified time, then retry with SAME payload
   - **5xx (Server error)** → Wait briefly, then retry with SAME payload (server might recover)
   - **4xx (Client error)** → Check if `Retryable` is true. If so, use AI to fix the payload.
6. **If AI correction works**, retry with the corrected payload
7. **If all retries fail**, return the error to the caller

```go
func (a *Agent) callToolWithRetry(
    ctx context.Context,
    tool *core.ServiceInfo,
    capability *core.Capability,
    initialPayload map[string]interface{},
    config RetryConfig,
) (*ToolResult, error) {

    currentPayload := initialPayload
    var lastError *core.ToolError
    var lastHTTPStatus int

    for attempt := 0; attempt <= config.MaxRetries; attempt++ {

        // ══════════════════════════════════════════════════════════════
        // STEP 1: Make the HTTP call to the tool
        // ══════════════════════════════════════════════════════════════
        resp, body, err := a.callToolHTTP(ctx, tool, capability, currentPayload)

        // ══════════════════════════════════════════════════════════════
        // STEP 2: Handle network errors (couldn't reach the tool at all)
        // ══════════════════════════════════════════════════════════════
        if err != nil {
            if attempt < config.MaxRetries {
                time.Sleep(config.BackoffDuration)
                continue // Retry with same payload - network might recover
            }
            return nil, fmt.Errorf("network error after %d attempts: %w", attempt+1, err)
        }

        lastHTTPStatus = resp.StatusCode

        // ══════════════════════════════════════════════════════════════
        // STEP 3: Check for success - we're done!
        // ══════════════════════════════════════════════════════════════
        if resp.StatusCode >= 200 && resp.StatusCode < 300 {
            return a.parseSuccessResponse(body), nil
        }

        // ══════════════════════════════════════════════════════════════
        // STEP 4: Parse the error response to get ToolError details
        // ══════════════════════════════════════════════════════════════
        toolResp := a.parseToolResponse(body)
        if toolResp.Error != nil {
            lastError = toolResp.Error
        }

        // ══════════════════════════════════════════════════════════════
        // STEP 5: Route based on HTTP status code
        // ══════════════════════════════════════════════════════════════

        // 5a: Auth errors (401/403) - STOP! Agent can't fix credentials
        if resp.StatusCode == 401 || resp.StatusCode == 403 {
            return nil, fmt.Errorf("auth error: %s", lastError.Message)
        }

        // 5b: Rate limited (429) - Wait and retry with SAME payload
        if resp.StatusCode == 429 {
            retryAfter := a.parseRetryAfter(lastError) // Check Details["retry_after"]
            time.Sleep(retryAfter)
            continue
        }

        // 5c: Server error (5xx) - Retry with SAME payload (transient error)
        if resp.StatusCode >= 500 {
            if attempt < config.MaxRetries {
                time.Sleep(config.BackoffDuration)
                continue
            }
        }

        // 5d: Client error (4xx) - Check if AI can fix the payload
        if resp.StatusCode >= 400 && resp.StatusCode < 500 {
            // First check: Is this error marked as retryable?
            if lastError == nil || !lastError.Retryable {
                return nil, fmt.Errorf("client error: %s", lastError.Message)
            }

            // ══════════════════════════════════════════════════════════
            // STEP 6: Use AI to analyze the error and correct the payload
            // ══════════════════════════════════════════════════════════
            if config.UseAI && a.aiClient != nil && attempt < config.MaxRetries {
                corrected, err := a.aiCorrectPayload(ctx, ErrorContext{
                    HTTPStatus:      resp.StatusCode,
                    OriginalRequest: currentPayload,
                    ToolError:       lastError,
                    ToolName:        tool.Name,
                    Capability:      capability.Name,
                    AttemptNumber:   attempt + 1,
                })

                if err == nil && corrected != nil {
                    currentPayload = corrected // Use the AI-fixed payload
                    continue                   // Try again with corrected payload
                }
            }
            break // AI couldn't fix it, stop retrying
        }
    }

    // ══════════════════════════════════════════════════════════════════
    // STEP 7: All retries exhausted - return the error
    // ══════════════════════════════════════════════════════════════════
    return nil, fmt.Errorf("failed after %d attempts: %s", config.MaxRetries+1, lastError.Message)
}
```

### AI Error Correction

This is where the "intelligent" part of intelligent error handling comes in. When a tool returns a retryable error, we ask AI to:

1. **Analyze the error** - Understand why the request failed
2. **Decide if it's fixable** - Some errors (like auth failures) can't be fixed by changing input
3. **Generate a corrected payload** - If fixable, create a new payload that might work

**How it works:**

- We build a prompt that gives AI all the context: the error details, original payload, and hints from the tool
- AI returns a structured JSON response with its analysis and corrected payload
- We use low temperature (0.1) for deterministic, consistent results
- If AI says it can't fix the error, we return `nil` to signal "give up"

```go
func (a *Agent) aiCorrectPayload(ctx context.Context, errCtx ErrorContext) (map[string]interface{}, error) {

    // ══════════════════════════════════════════════════════════════════
    // Step 1: Build the prompt with all context AI needs
    // ══════════════════════════════════════════════════════════════════
    prompt := fmt.Sprintf(`You are an API error analyzer. A tool call failed and you need to fix it.

## Error Information
HTTP Status: %d
Tool: %s
Capability: %s
Error Code: %s
Error Category: %s
Error Message: %s
Error Details: %v

## Original Request Payload
%s

## Your Task
1. Analyze why this request failed
2. Determine if it can be fixed by modifying the input
3. If fixable, generate a corrected JSON payload

## Response Format
Return ONLY valid JSON:
{
  "can_fix": true/false,
  "analysis": "Brief explanation",
  "corrected_payload": { ... }  // Only if can_fix is true
}

## Examples
- "Flower Mound, TX" failed? Try "Flower Mound, Texas, US"
- "MSFT Inc" failed? Try just "MSFT"
- API key invalid? can_fix: false (can't fix credentials)`,
        errCtx.HTTPStatus,
        errCtx.ToolName,
        errCtx.Capability,
        errCtx.ToolError.Code,
        errCtx.ToolError.Category,
        errCtx.ToolError.Message,
        errCtx.ToolError.Details,
        formatJSON(errCtx.OriginalRequest),
    )

    // ══════════════════════════════════════════════════════════════════
    // Step 2: Call AI with low temperature for consistent results
    // ══════════════════════════════════════════════════════════════════
    response, err := a.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
        Temperature: 0.1, // Low = more deterministic (we want consistent fixes)
        MaxTokens:   300, // Keep it short - we just need JSON output
    })
    if err != nil {
        return nil, err
    }

    // ══════════════════════════════════════════════════════════════════
    // Step 3: Parse AI's response into a structured result
    // ══════════════════════════════════════════════════════════════════
    var result struct {
        CanFix           bool                   `json:"can_fix"`
        Analysis         string                 `json:"analysis"`
        CorrectedPayload map[string]interface{} `json:"corrected_payload"`
    }

    if err := json.Unmarshal([]byte(response.Content), &result); err != nil {
        return nil, err // AI returned invalid JSON - can't fix
    }

    // ══════════════════════════════════════════════════════════════════
    // Step 4: Return corrected payload or nil if unfixable
    // ══════════════════════════════════════════════════════════════════
    if !result.CanFix {
        return nil, nil // Signal to caller: "AI says this can't be fixed"
    }

    return result.CorrectedPayload, nil
}
```

## Part 4: Real-World Example Flow

Now let's see how all the pieces fit together. We'll trace through a real scenario where a user asks for weather in a city that the API doesn't recognize at first, but succeeds after AI-powered correction.

**The scenario:** User asks for "weather in Flower Mound, TX" - a small city in Texas. The weather API doesn't recognize "TX" as a country code, but after AI analyzes the error and reformats the location, the second attempt succeeds.

### Step 1: Agent Generates Initial Payload

```
User request: "What's the weather in Flower Mound, TX?"

Agent (via AI) generates payload:
{
  "location": "Flower Mound, TX",
  "units": "metric"
}
```

### Step 2: Tool Returns Structured Error

```
Tool calls OpenWeatherMap API
API returns: 404 city not found

Tool returns HTTP 404:
{
  "success": false,
  "error": {
    "code": "LOCATION_NOT_FOUND",
    "message": "Location 'Flower Mound, TX' not found in weather database",
    "category": "NOT_FOUND",
    "retryable": true,
    "details": {
      "original_location": "Flower Mound, TX",
      "hint": "Try 'City, Country' format"
    }
  }
}
```

### Step 3: Agent Decision Flow

```
Agent receives HTTP 404 response

Decision tree:
├── HTTP 404 is a 4xx error
├── Check ToolError.Retryable → true
├── AI correction enabled → yes
└── Use AI to analyze and fix
```

### Step 4: AI Analyzes and Corrects

```
AI input: Error context with original payload and error details

AI output:
{
  "can_fix": true,
  "analysis": "TX is a US state abbreviation. OpenWeatherMap prefers full format.",
  "corrected_payload": {
    "location": "Flower Mound, Texas, US",
    "units": "metric"
  }
}
```

### Step 5: Retry Succeeds

```
Agent retries with corrected payload:
{
  "location": "Flower Mound, Texas, US",
  "units": "metric"
}

Tool returns HTTP 200:
{
  "success": true,
  "data": {
    "location": "Flower Mound",
    "temperature": 25.3,
    "condition": "clear sky",
    "source": "OpenWeatherMap API"
  }
}
```

### Step 6: Agent Returns Result

```
Agent logs: "Tool call succeeded after AI-assisted retry (attempt 2)"
User receives: Weather data for Flower Mound, Texas
```

## Configuration Options

Configure retry behavior via environment variables:

```bash
# Maximum retry attempts (default: 3)
export TRUVAG3_AGENT_MAX_RETRIES=3

# Enable AI-powered error correction (default: true)
export TRUVAG3_AGENT_USE_AI_CORRECTION=true

# Backoff duration between retries (default: 1000ms)
export TRUVAG3_AGENT_RETRY_BACKOFF_MS=1000
```

## Best Practices

### For Tool Developers

1. **Always use ToolResponse envelope**
   ```go
   // Always wrap responses
   response := core.ToolResponse{Success: true, Data: result}
   ```

2. **Never return mock data**
   ```go
   // BAD: Hiding the error
   if err != nil {
       return mockWeatherData() // Don't do this!
   }

   // GOOD: Report the error
   if err != nil {
       return classifyAndReturnError(err)
   }
   ```

3. **Include helpful details**
   ```go
   Details: map[string]string{
       "original_input": userInput,
       "hint":           "Try this format instead...",
       "api_error":      err.Error(), // For debugging
   }
   ```

4. **Set Retryable correctly**
   ```go
   // Retryable: true → Agent CAN fix with different input
   // Example: "City not found" - maybe different spelling works

   // Retryable: false → Agent CANNOT fix
   // Example: "Invalid API key" - needs configuration change
   ```

### For Agent Developers

1. **Check HTTP status first, then ToolError**
   ```go
   // HTTP status gives quick routing decision
   // ToolError.Retryable gives fine-grained control
   ```

2. **Don't retry auth errors**
   ```go
   if status == 401 || status == 403 {
       return err // Agent can't fix credentials
   }
   ```

3. **Use exponential backoff for rate limits**
   ```go
   if status == 429 {
       retryAfter := parseRetryAfter(toolErr) // From Details["retry_after"]
       time.Sleep(retryAfter)
   }
   ```

4. **Log retry attempts for debugging**
   ```go
   a.Logger.Info("Retry attempt", map[string]interface{}{
       "tool":    toolName,
       "attempt": attemptNum,
       "reason":  toolErr.Code,
   })
   ```

## Quick Reference

### Error Category to HTTP Status

| Category | HTTP Status | Retryable? |
|----------|-------------|------------|
| `CategoryInputError` | 400 | Maybe (with corrected input) |
| `CategoryNotFound` | 404 | Maybe (with corrected input) |
| `CategoryAuthError` | 401 | No |
| `CategoryRateLimit` | 429 | Yes (after waiting) |
| `CategoryServiceError` | 503 | Yes (transient) |

### Retry Decision Matrix

| HTTP Status | Same Payload? | AI Correction? |
|-------------|--------------|----------------|
| 2xx | N/A (success) | N/A |
| 401/403 | Don't retry | Don't retry |
| 429 | Yes (after backoff) | No |
| 5xx | Yes (after backoff) | No |
| 4xx + Retryable | No | Yes |
| 4xx + Not Retryable | Don't retry | Don't retry |

### Complete Tool Error Example

```go
&core.ToolError{
    Code:      "LOCATION_NOT_FOUND",
    Message:   "Location 'Flower Mound, TX' not found",
    Category:  core.CategoryNotFound,
    Retryable: true,
    Details: map[string]string{
        "original_location": "Flower Mound, TX",
        "hint":              "Try 'City, Country' format",
        "api_error":         "404 city not found",
    },
}
```

---

## Benefits of This Approach

1. **Clean Separation**: Tools report, agents decide
2. **AI-Powered Recovery**: Agents can fix errors tools can't anticipate
3. **Transparency**: No mock data, clear error reporting
4. **Flexibility**: Agents choose their own retry strategies
5. **Observability**: Clear logs showing retry attempts and corrections

## Trade-offs

| Consideration | Impact | Mitigation |
|--------------|--------|------------|
| AI token cost | ~300 tokens per retry analysis | Cap retries at 3, skip AI for non-retryable errors |
| Latency | ~500ms per AI analysis | Use low temperature for fast responses |
| Complexity | Agent retry logic is more complex | Well-documented patterns, copy-paste examples |

---

## Orchestration Module: Multi-Layer Type Safety

When using the orchestration module's AI-driven natural language mode, an additional layer of error handling addresses a common LLM limitation: type mismatches in generated parameters.

### The Problem

LLMs sometimes generate JSON parameters with incorrect types:
```json
// LLM generates (WRONG):
{"lat": "35.6897", "lon": "139.6917"}

// Tool expects (CORRECT):
{"lat": 35.6897, "lon": 139.6917}
```

This causes Go's `json.Unmarshal` to fail with errors like:
```
json: cannot unmarshal string into Go struct field .lat of type float64
```

### The Solution: Multi-Layer Defense

The orchestration module implements four layers of defense:

#### Layer 1: Prompt Guidance
The LLM planning prompt includes explicit type rules and examples. While helpful, LLMs don't always follow instructions perfectly.

#### Layer 2: Schema-Based Coercion
Before calling a tool, the executor automatically coerces parameters to match the capability schema:

```go
// Automatic coercion using capability schema
// Input:  {"lat": "35.6897", "lon": "139.6917", "count": "5"}
// Output: {"lat": 35.6897, "lon": 139.6917, "count": 5}
```

This layer is:
- **Deterministic**: Uses `strconv.ParseFloat`, `strconv.ParseInt`, `strconv.ParseBool`
- **Schema-driven**: Only coerces when the capability defines expected types
- **Safe**: If coercion fails, the original value is preserved

#### Layer 3: Validation Feedback Retry
For edge cases that slip through Layers 1 and 2, if a tool returns a validation error (e.g., "Amount must be greater than 0"), the orchestrator:

1. Detects the type-related or validation error
2. Sends the error context back to the LLM
3. Requests corrected parameters
4. Retries with the corrected payload

**Error patterns detected:**
```go
// Type errors
"cannot unmarshal string into"
"json: cannot unmarshal"
"type mismatch"

// Validation errors (business logic)
"must be greater than"
"must be positive"
"is required"
"cannot be empty"
```

#### Layer 4: Contextual Re-Resolution (Semantic Retry)
When Layer 3's analysis concludes that an error **cannot be fixed by adjusting parameters alone** — but the source data needed to compute the correct value is present in the execution trajectory — Layer 4 takes over. It re-runs parameter resolution with the full prior step results in context, letting the LLM compute corrected parameters from upstream data rather than guessing.

Implemented in [`orchestration/contextual_re_resolver.go`](https://github.com/truvaagents/truva-g3/blob/main/orchestration/contextual_re_resolver.go); driven by [`orchestration/error_analyzer.go`](https://github.com/truvaagents/truva-g3/blob/main/orchestration/error_analyzer.go) decisions. See [Error Handling Guide §6 "Contextual Re-Resolution (Layer 4)"](ERROR_HANDLING_GUIDE.md#6-contextual-re-resolution-layer-4) for the full flow, including when Layer 4 fires versus when Layer 3 alone is sufficient.

### Configuration

```go
config := orchestration.DefaultConfig()

// Layer 3 is enabled by default for maximum reliability
config.ExecutionOptions.ValidationFeedbackEnabled = true  // Default: true
config.ExecutionOptions.MaxValidationRetries = 2          // Default: 2

// For cost-sensitive deployments, disable Layer 3
// (Relies on Layers 1 & 2 only, ~95% success rate)
config.ExecutionOptions.ValidationFeedbackEnabled = false
```

### Observability

**Distributed Tracing (Jaeger/Tempo):**
- `type_coercion_applied` span event when Layer 2 coerces parameters
- `validation_feedback_started` span event when Layer 3 initiates correction
- `validation_feedback_success` / `validation_feedback_failed` span events

**Prometheus Metrics:**
```promql
# Type coercion frequency by capability
sum(rate(orchestration_type_coercion_applied_total[5m])) by (capability)

# Validation feedback success rate
sum(rate(orchestration_validation_feedback_success_total[5m])) /
sum(rate(orchestration_validation_feedback_attempts_total[5m]))
```

### How This Complements Tool-Level Error Handling

| Layer | Responsibility | Location |
|-------|---------------|----------|
| **Tool** | Report structured errors with `ToolError` | Tool handlers |
| **Agent** | Retry with AI-corrected payloads | Agent retry logic |
| **Orchestrator** | Type coercion + validation feedback + semantic retry | Orchestration executor |

The orchestration layer adds automatic type safety *before* requests reach tools, reducing the burden on both tools and agents. When combined with proper tool error reporting and agent retry logic, this creates a robust error handling system that achieves ~99% success rates in production.

For detailed implementation information, see the [orchestration module documentation](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md#multi-layer-type-safety).

---

## Orchestration Module: Layer 4 Semantic Retry

When the multi-layer type safety system exhausts its options—when Layer 3 (Validation Feedback) determines "this error cannot be fixed with different parameters"—there's one more layer of defense: **Semantic Retry**.

### The Problem Semantic Retry Solves

Consider this multi-step workflow:
```
User: "Sell 100 Tesla shares and convert the proceeds to EUR"

Step 1 (stock-tool): Returns {symbol: "TSLA", price: 468.285}
Step 2 (currency-tool): Called with {amount: 0} ← MicroResolver couldn't compute this!
```

The currency tool returns `400: "amount must be greater than 0"`. At this point:
- **Layer 1 (Prompt Guidance)**: Already failed
- **Layer 2 (Schema Coercion)**: Can't help—`0` is already a valid number
- **Layer 3 (Validation Feedback)**: Says "cannot fix—don't know what amount should be"

But wait—**the data to compute the correct amount exists!** The user said "100 shares" and step 1 returned `price: 468.285`. A human developer would instantly compute: `100 × 468.285 = 46828.5`.

### How Semantic Retry Works

Semantic Retry (Layer 4) uses the **full execution trajectory** to compute corrected parameters:

```
┌─────────────────────────────────────────────────────────────┐
│ Layer 4: Contextual Re-Resolution                            │
│                                                              │
│   Input: Full execution context                              │
│   • User's original query (the intent)                       │
│   • Source data from dependent steps (what to compute from)  │
│   • Failed parameters (what went wrong)                      │
│   • Error response (the clue)                                │
│                                                              │
│   Output: Corrected parameters                               │
│   • should_retry: true                                       │
│   • corrected_parameters: {amount: 46828.5}                  │
│   • analysis: "Computed 100 × 468.285 = 46828.5"            │
└─────────────────────────────────────────────────────────────┘
```

### When Semantic Retry Activates

| Condition | Semantic Retry? |
|-----------|-----------------|
| Tool returns 400/404/409/422 + Layer 3 says "cannot fix" | ✅ Yes |
| Tool returns 500 (server error) | ❌ No (handled by resilience module) |
| Tool returns 401/403 (auth error) | ❌ No (not retryable) |
| Layer 3 successfully corrects parameters | ❌ No (already fixed) |
| Independent step (no dependencies) with error | ✅ Yes (uses user query + error context) |
| Independent step + `EnableForIndependentSteps=false` | ❌ No (disabled by config) |

**Note on Independent Steps:** Steps without dependencies (first steps, parallel steps) now trigger Semantic Retry by default. The LLM receives the user's original query and error context to suggest corrections, even without source data from dependent steps.

### Transient Error Handling (503 Timeouts)

A special case arises with **503 errors** containing structured tool responses. Unlike pure infrastructure errors (500, 502, 504), tools sometimes return 503 with semantic error information (e.g., "location not found" wrapped in a timeout error).

**How the Orchestrator Handles This:**

1. **LLM analyzes the error** - The ErrorAnalyzer examines the response body, not just the HTTP status
2. **LLM may return `should_retry=true`** - With or without parameter changes:
   - With changes: "Change 'Tokio' to 'Tokyo'" → Retry with corrected params
   - Without changes: "Timeout, service may recover" → Retry with same params
3. **`IsTransientError` flag** - LLM sets this for infrastructure issues (timeouts, service unavailable)
4. **Tool's `retryable: false` is overridden** - When LLM identifies a transient error, the executor continues retrying even if the tool said "don't retry"

**Decision Matrix:**

| LLM Result | Action |
|------------|--------|
| `should_retry=true`, `suggested_changes` non-empty | Retry with new parameters |
| `should_retry=true`, `suggested_changes` empty | Retry with same parameters (transient) |
| `should_retry=false`, `is_transient_error=true` | Resilience retry (backoff) |
| `should_retry=false`, `is_transient_error=false` | Don't retry |

This ensures that transient infrastructure errors don't fail permanently when a simple retry might succeed.

### Configuration

```go
// Semantic retry is enabled by default
config := orchestration.DefaultConfig()
config.SemanticRetry.Enabled = true                   // Default: true
config.SemanticRetry.MaxAttempts = 2                  // Default: 2
config.SemanticRetry.EnableForIndependentSteps = true // Default: true

// Or via environment variables:
// TRUVAG3_SEMANTIC_RETRY_ENABLED=true
// TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS=2
// TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS=true
```

**Environment Variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_SEMANTIC_RETRY_ENABLED` | `true` | Enable Layer 4 semantic retry |
| `TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS` | `2` | Maximum semantic retry attempts per step |
| `TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS` | `true` | Enable semantic retry for steps without dependencies |

### Observability

Semantic retry generates detailed telemetry:

**Distributed Tracing (Jaeger/Tempo):**
- `contextual_re_resolution.start` - When semantic retry begins
- `contextual_re_resolution.complete` - Result with should_retry, analysis, parameter count
- `independent_step` attribute - Boolean indicating step had no dependencies

**Prometheus Metrics:**
```promql
# Semantic retry success rate
sum(rate(orchestration_semantic_retry_success_total[5m])) /
sum(rate(orchestration_semantic_retry_attempts_total[5m]))

# LLM latency for semantic retry
histogram_quantile(0.95, orchestration_semantic_retry_llm_latency_ms)

# Independent step semantic retries (new metric)
sum(rate(orchestration_semantic_retry_independent_step_total[5m])) by (capability)
```

**LLM Debug Payload Store:**

For deep debugging of error handling flows, enable the LLM Debug Store to capture complete prompts and responses at all stages. Unlike Jaeger spans which truncate large payloads, this stores the full content:

```bash
export TRUVAG3_LLM_DEBUG_ENABLED=true
```

This captures interactions at 6 recording sites including `semantic_retry`, allowing you to inspect the exact prompts sent to the LLM and the responses received during error recovery.

### The Complete Error Handling Stack

With Semantic Retry, the orchestration module provides a **four-layer defense**:

| Layer | Strategy | When Used | Cost |
|-------|----------|-----------|------|
| **Layer 1** | Prompt Guidance | Always | Free |
| **Layer 2** | Schema Coercion | Always | Free |
| **Layer 3** | Validation Feedback | When tool returns correctable error | 1 LLM call |
| **Layer 4** | Semantic Retry | When Layer 3 says "cannot fix" | 1 LLM call |

This creates a robust system that handles the full spectrum of parameter errors—from simple type mismatches to complex computations that require understanding user intent.

---

Happy error handling!
