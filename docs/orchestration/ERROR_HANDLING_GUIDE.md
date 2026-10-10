# Error Handling Guide

How errors flow through TruvaG3 — from upstream API failure to intelligent recovery.

## Table of Contents

1. [The Error Journey at a Glance](#1-the-error-journey-at-a-glance)
2. [Error Classification: How Tools Report Errors](#2-error-classification-how-tools-report-errors)
3. [The Decision Matrix: Status Code Routing](#3-the-decision-matrix-status-code-routing)
4. [Executor Error Routing (Multi-Layer)](#4-executor-error-routing-multi-layer)
5. [LLM Error Analyzer (Layer 3)](#5-llm-error-analyzer-layer-3)
6. [Contextual Re-Resolution (Layer 4)](#6-contextual-re-resolution-layer-4)
7. [Step Retry and Backoff](#7-step-retry-and-backoff)
8. [Common Scenarios](#8-common-scenarios)
9. [Configuration Reference](#9-configuration-reference)
10. [Best Practices](#10-best-practices)
11. [Troubleshooting](#11-troubleshooting)
12. [Quick Reference](#12-quick-reference)

---

## 1. The Error Journey at a Glance

Think of TruvaG3's error handling like an air traffic control system. When a pilot (tool) encounters trouble mid-flight — bad weather, engine warning, communication failure — they transmit a distress code over radio. The control tower (the executor in the `orchestration` module) reads the code and routes to the appropriate recovery procedure:

- **Squawk 7600** (communication failure) — land immediately, no second chances. In TruvaG3, this is a 401/403 auth error: fail immediately.
- **Squawk 7700** (general emergency) — enter a holding pattern. For an eligible 429/5xx error without explicit retry refusal, the executor may retry the same payload after backoff.
- **Approach correction** — the pilot is on the wrong heading. An eligible 400 input error can reach parameter correction when the response does not explicitly refuse retries and the executor's correction gates pass.

Tools classify failures and can refuse retries. The executor chooses an eligible recovery path within that permission, its budgets and binding protections. `ClassifyUpstreamError` returns `Retryable:false` for recognized input 4xx errors; it does not automatically opt a tool into correction.

An omitted `retryable` retains bounded status-based compatibility. Standard Go
`core.ToolError` always serializes the boolean, so leaving it unset sends false.
Custom response envelopes, written in Go or another language, can omit it.

### Go Modules Involved

Four Go modules participate in error handling. Each has a distinct role:

| Go Module | Key File(s) | Role in Error Handling |
|:---|:---|:---|
| `core` | `tool_error.go`, `backoff.go` | Shared types (`ToolError`, `ToolResponse`, `ErrorCategory`) and utilities (`ClassifyUpstreamError`, `BackoffConfig`). Imported by tools, agents, and the `orchestration` module. |
| `orchestration` | `executor.go`, `error_analyzer.go` | The executor's step retry loop: routes errors by HTTP status, runs the LLM Error Analyzer, applies corrections, and retries with exponential backoff (calculated via `core.BackoffConfig`). Does **not** import the `resilience` module. |
| `resilience` | `retry.go`, `circuit_breaker.go` | Standalone `RetryExecutor` with circuit breaking (CLOSED/OPEN/HALF-OPEN), exponential backoff, and timeout management. Used by **agents** that call tools directly (e.g., `agent-with-resilience`). NOT used by the `orchestration` module or by tools. See [Section 7 — The `resilience` Module](#the-resilience-module-separate-from-step-retry). |
| Tool code (e.g., `examples/hotel-tool`) | `handlers.go` | Each tool defines its own `sendError()` and `sendUpstreamError()` helper methods locally. These are **not** imported from a module — they're a convention that tools follow. Tools import `core` for `ClassifyUpstreamError` and `ToolResponse`. |

> **"Step retry" in this guide** always means the executor's built-in retry loop in `orchestration/executor.go`, not the `resilience` module. The `orchestration` and `resilience` modules are separate Go modules with separate `go.mod` files.

### Full Error Flow

```
 Upstream API Error (e.g., Amadeus returns 400)
       │
       ▼
 TOOL CODE: core.ClassifyUpstreamError(err)         ← core module
       │  Extracts HTTP status from error message
       │  Maps to (HTTPStatus, Category, Retryable, Code)
       ▼
 Tool sends ToolResponse with ToolError + HTTP status
       │
       ▼
 ORCHESTRATION MODULE: executor.go                   ← orchestration module
 extractHTTPStatusFromError()
       │
       ├── Structured retryable:false ──► STOP (before correction or resend)
       │
       ├── 401 / 403 / 405 ──► FAIL IMMEDIATELY
       │                        (auth / permission issue)
       │
       ├── 408 / 429 / 5xx ──► STEP RETRY
       │                                    (same payload + exponential backoff)
       │
       └── Eligible mutable 4xx ──► LLM ERROR ANALYZER (Layer 3)
                                        │          ← error_analyzer.go
                                        │
                                        ├── Fixable ──► Retry with corrected params
                                        │
                                        ├── No valid correction ──► No unchanged 4xx resend
                                        │
                                        └── Not fixable ──► LAYER 4: Re-Resolution
                                                             │
                                                             ├── Corrected ──► Retry
                                                             └── Failed ──► FAIL
```

### Error Categories

| Category | Meaning | Example |
|:---|:---|:---|
| `INPUT_ERROR` | Malformed or invalid request payload | Missing required field, invalid JSON |
| `NOT_FOUND` | Resource doesn't exist (may exist with corrected params) | Unknown city code, invalid stock symbol |
| `RATE_LIMIT` | API quota exceeded | Too many requests per second |
| `AUTH_ERROR` | Authentication or authorization failure | Expired API key, insufficient permissions |
| `SERVICE_ERROR` | Backend service failed (usually transient) | Server error, timeout, gateway failure |

> These categories are defined in `core/tool_error.go` as `ErrorCategory` constants.

---

## 2. Error Classification: How Tools Report Errors

Each tool defines two error helper methods locally in its `handlers.go` — one for local validation, one for upstream API failures. These are **not** imported from a module; they are a convention that all tools follow (see any tool's `handlers.go` for the implementation):

| Helper | Where Defined | When to Use | Classification |
|:---|:---|:---|:---|
| `sendError()` | Each tool's `handlers.go` | Request decode failures, missing required fields | Manual: you set the HTTP status and error code |
| `sendUpstreamError()` | Each tool's `handlers.go` | Upstream API returned an error | Automatic: `core.ClassifyUpstreamError` sets HTTP status, category, retryable flag |

### The Classification Pattern

When an upstream API call fails, tools call `core.ClassifyUpstreamError(err)` (from the `core` module, `core/tool_error.go`) which extracts the HTTP status code from the error message using a regex:

```
(?:status|error|code)[:\s]+(\d{3})
```

This matches common API client formats:
- `"Amadeus API error 400: INVALID CITY CODE"` → extracts `400`
- `"HTTP status 429: rate limit exceeded"` → extracts `429`
- `"status_code: 503"` → extracts `503`

### Usage in Tool Handlers

```go
// Upstream API failed
response, err := h.client.SearchHotels(ctx, req)
if err != nil {
    // ClassifyUpstreamError extracts the status code from err.Error()
    // and returns the correct HTTP status, category, and retryable flag
    info := core.ClassifyUpstreamError(err)
    safeError := core.RedactSensitiveText(err.Error())
    h.sendUpstreamError(rw, "Hotel search failed: "+safeError, info)
    return
}
```

The tool's `sendUpstreamError` method sets the HTTP status code, `ToolError.Category`, and `ToolError.Retryable` from the classification result — ensuring the `orchestration` module's executor routes the error correctly.

> For the full handler pattern with telemetry and logging, see [Tool Development Guide — Step 4: Implement Handlers](../building/TOOL_DEVELOPMENT_GUIDE.md#6-step-4-implement-handlers).
>
> For type signatures, see [API Reference — ClassifyUpstreamError](../reference/API_REFERENCE.md#classifyupstreamerror).

---

## 3. The Decision Matrix: Status Code Routing

This is the authoritative mapping. The first two columns show what the tool does (via `core.ClassifyUpstreamError` in `core/tool_error.go`). The last column shows what the executor does in `orchestration/executor.go`:

| Upstream Status | Tool HTTP | Category | Retryable | Executor Action (`orchestration/executor.go`) | Example |
|:---:|:---:|:---|:---:|:---|:---|
| 400 | 400 | `INPUT_ERROR` | false | Stop before analyzer | Invalid date format |
| 401 | 401 | `AUTH_ERROR` | false | Fail immediately | Expired API key |
| 403 | 403 | `AUTH_ERROR` | false | Fail immediately | Insufficient permissions |
| 404 | 400 | `INPUT_ERROR` | false | Stop before analyzer | City not found |
| 409 | 400 | `INPUT_ERROR` | false | Stop before analyzer | Booking conflict |
| 422 | 400 | `INPUT_ERROR` | false | Stop before analyzer | Unprocessable entity |
| 429 | 429 | `RATE_LIMIT` | true | Step retry (same payload + backoff) | Rate limited |
| 500+ | 502 | `SERVICE_ERROR` | true | Step retry (same payload + backoff) | Server error |
| No match | 502 | `SERVICE_ERROR` | true | Step retry (same payload + backoff) | Timeout, unknown error |

> **Retry permission precedes status routing:** an explicit `retryable:false`
> stops before analysis and resend on every status, including HTTP 500/503.
> Transport errors and HTTP 408/429/5xx bypass parameter correction and use the
> executor's existing bounded same-payload backoff. Correctable input failures
> use non-transient 4xx statuses; a tool returning 503 for a typo must correct
> its status contract rather than rely on the analyzer to rewrite outage inputs.
> Omitted policy is distinct from false. Non-transient 4xx requests are not resent
> unchanged when correction is unavailable or exhausted.

### Non-Retryable Tool Errors

An explicit `ToolResponse.Error.Retryable=false` stops same-step retries before
LLM correction. An omitted, null or wrongly typed declaration is not an explicit
false. A non-transient 4xx is not resent unchanged when correction is unavailable
or exhausted. Authentication/method errors remain terminal; authorized later
planning is distinct from a same-step retry. See `dispatch_policy.go` and the
dispatch guards in `executor.go` for the current policy.

---

## 4. Executor Error Routing (Multi-Layer)

The `executeStep()` function in `orchestration/executor.go` classifies failures before entering any parameter-correction path. Transport failures and transient HTTP statuses use its existing same-payload backoff. Only eligible non-transient 4xx failures can reach `ErrorAnalyzer` or legacy correction, subject to configuration, correction budgets, and binding protection.

```
orchestration/executor.go — executeStep() retry loop
  for attempt = 1..maxAttempts:
    │
    HTTP call to tool/agent
    │
    ├── Success → return result
    │
    ├── Explicit retryable:false → stop before analyzer/resend
    │
    ├── HTTP 401/403/405 → stop
    │
    ├── Transport failure (status 0), HTTP 408/429/500–599
    │     └── Same admitted payload → existing backoff/retry budget
    │         (no ErrorAnalyzer, contextual re-resolution, or legacy correction)
    │
    ├── Other 4xx → correction eligibility
    │     ├── HITL-approved values → stop; preserve approved inputs
    │     ├── Eligible analyzer/contextual or legacy correction
    │     │     └── Changed, valid, binding-preserving parameters
    │     │           → apply, consume correction budget, attempt--, continue
    │     └── No accepted correction → stop; no unchanged 4xx resend
    │
    └── Remaining retry eligibility/backoff → existing bounded loop
```

> **Naming clarification:** `ErrorAnalyzer.shouldDelegateToResilience()` also protects direct analyzer callers; it is not a call to the `resilience` Go module. The executor classifies transient failures before invoking the analyzer and uses `core.BackoffConfig` for its own backoff. Orchestrator capabilities retain their single-dispatch limit; this diagram does not grant them additional attempts.

### The `attempt--` Trick

When the LLM Error Analyzer or Layer 4 provides corrected parameters, the attempt counter is decremented (`attempt--`). This means **LLM corrections don't count against `maxAttempts`** — they get a fresh retry. However, they DO count against `maxValidationRetries` (default: 2) to prevent infinite correction loops.

> For the full LLM error analysis and semantic retry deep dive, see [Intelligent Error Handling](INTELLIGENT_ERROR_HANDLING.md).

---

## 5. LLM Error Analyzer (Layer 3)

The LLM Error Analyzer examines the error response, original request parameters, capability description, and user query to determine if the error is fixable by changing parameters.

### What It Receives

```go
errCtx := &ErrorAnalysisContext{
    HTTPStatus:            400,
    ErrorResponse:         `{"error": "INVALID CITY CODE: PARIS"}`,
    OriginalRequest:       map[string]interface{}{"city_code": "PARIS", "check_in": "2026-04-01"},
    UserQuery:             "Find hotels in Paris for April 1-3",
    CapabilityName:        "search_hotels",
    CapabilityDescription: "Search hotel offers by IATA city code...",
    StepID:                "step-17",
}
```

### What It Returns

```go
// LLM determines: "PARIS" is not a valid IATA code — the correct code is "PAR"
result := &ErrorAnalysisResult{
    ShouldRetry:      true,
    Reason:           "PARIS is not a valid IATA city code. The correct code for Paris is PAR.",
    SuggestedChanges: map[string]interface{}{"city_code": "PAR"},
    IsTransientError: false,
}
```

### When It's Skipped

- A recognized error envelope declares `retryable:false` (including a Go bool's zero value)
- Inputs were approved through HITL, or the capability is a delegated orchestrator
- HTTP status is 401/403/405, or the failure is not an eligible non-transient 4xx
- `ErrorAnalyzer` is nil (not configured in factory)
- `IsEnabled()` returns false (disabled at runtime)
- `validationRetries >= maxValidationRetries` (correction budget exhausted)
- HTTP status is 408/429/5xx (the executor classifies it as transient before correction, then uses unchanged-input retry when permission and budgets allow)

> For the full LLM prompt, parsing logic, and configuration options, see [Intelligent Error Handling — Part 3: Agent Implementation](INTELLIGENT_ERROR_HANDLING.md#part-3-agent-implementation).

---

## 6. Contextual Re-Resolution (Layer 4)

Layer 4 activates when Layer 3 says "cannot fix." The key difference: Layer 3 only sees the error and original parameters, while Layer 4 has access to **source data from dependency steps**.

### When It Activates

1. Layer 3 returned `ShouldRetry: false`
2. `ContextualReResolver` is configured
3. `validationRetries < maxSemanticRetries` (default: 2)
4. For independent steps (no dependencies): controlled by `semanticRetryForIndependentSteps` (default: true)

### Example

A currency conversion step fails with "invalid currency code 'France'":
- **Layer 3** can't fix it — it doesn't know France's currency code
- **Layer 4** has the geocoding step's result: `{country: "France", ...}` and derives `"EUR"` from context

### Transient Error Handling

The HTTP classification is authoritative: transport failures and 408/429/5xx
use unchanged retry without an LLM call. A model's `IsTransientError` claim
does not authorize an unchanged resend of a non-transient 4xx. Explicit false
and orchestrator single-dispatch protections still take precedence.

> For configuration and observability details, see [Intelligent Error Handling — Layer 4 Semantic Retry](INTELLIGENT_ERROR_HANDLING.md#orchestration-module-layer-4-semantic-retry).

---

## 7. Step Retry and Backoff

When a transport failure or HTTP 408/429/5xx reaches the step retry path, the
executor waits using its existing exponential backoff with deterministic jitter.

### How the Executor Calculates Backoff

The executor stores a `core.BackoffConfig` (from the `core` module, defined in `core/backoff.go`) and calls its `Delay(attempt)` method to calculate the wait duration before each retry. This is **not** the `resilience` module — the executor implements its own retry loop and only uses `core.BackoffConfig` for the delay calculation:

```go
BackoffConfig{
    InitialDelay:  500 * time.Millisecond, // Base delay for first attempt
    MaxDelay:      10 * time.Second,       // Upper bound
    BackoffFactor: 2.0,                    // Doubling per attempt
    JitterEnabled: true,                   // Deterministic ±10% (math.Sin-based)
}
```

### Backoff Progression

| Attempt | Base Delay | With Jitter | Jitter Factor (`0.1 × sin(n)`) |
|:---:|:---:|:---:|:---|
| 1 | 500ms | ~542ms | `0.1 × sin(1) ≈ +0.084` → +42ms |
| 2 | 1s | ~1.09s | `0.1 × sin(2) ≈ +0.091` → +91ms |
| 3 | 2s | ~2.03s | `0.1 × sin(3) ≈ +0.014` → +28ms |
| 4 | 4s | ~3.70s | `0.1 × sin(4) ≈ -0.076` → -302ms |
| 5 | 8s | ~7.23s | `0.1 × sin(5) ≈ -0.096` → -767ms |
| 6+ | capped at 10s | ~9.7s | Base exceeds MaxDelay, capped before jitter |

The jitter is deterministic (not random) — the same attempt number always produces the same delay. This makes debugging reproducible while still spreading out concurrent retries.

### Context-Aware Waiting

The backoff sleep is context-aware: if the request context is cancelled during the wait, the timer stops immediately and the step exits without burning the remaining delay:

```go
timer := time.NewTimer(retryDelay)
select {
case <-ctx.Done():
    timer.Stop()
    return result // Exit immediately
case <-timer.C:
    // Continue to next attempt
}
```

> For `BackoffConfig` API details, see [API Reference — BackoffConfig](../reference/API_REFERENCE.md#backoffconfig).

### The `resilience` Module (Separate from Step Retry)

The `resilience` Go module (`resilience/retry.go`) is a **standalone retry utility** with features that go beyond the executor's step retry loop:

| Feature | Executor Step Retry (`orchestration/executor.go`) | `resilience` Module (`resilience/retry.go`) |
|:---|:---|:---|
| Retry loop | Built into `executeStep()` | Standalone `RetryExecutor.Execute()` |
| Backoff calculation | `core.BackoffConfig.Delay()` | Its own exponential backoff (same algorithm) |
| Circuit breaking | No | Yes — `CircuitBreaker` (CLOSED/OPEN/HALF-OPEN) |
| Timeout management | Via context | `CircuitBreaker.ExecuteWithTimeout()` |
| Telemetry | Span events + counters | Span events + counters |
| Used by | `orchestration` module (step-level retry) | **Agents** that call tools directly |

**Who uses the `resilience` module?** Agents that make their own HTTP calls to tools — not through the `orchestration` module's executor. For example, `examples/agent-with-resilience/research_agent.go` imports `resilience` and wraps each tool call with `resilience.RetryWithCircuitBreaker()`. This gives the agent per-tool circuit breakers and local retry before the error ever reaches the executor.

**Who does NOT use it?** The `orchestration` module. It has its own step retry loop in `executor.go` that uses `core.BackoffConfig` for delay calculation. Tools also don't use it by default — they classify errors and return them, letting the executor handle retry. However, tools CAN adopt the `resilience` module for local upstream API retry.

---

## 8. Common Scenarios

### Scenario A: Explicitly Permitted Parameter Correction (Upstream 400)

A hypothetical travel tool recognizes an invalid city code as safely correctable. This example requires an explicit tool decision; the generic classifier alone refuses correction for this error.

```
1. orchestration/executor.go resolves parameters: {city_code: "Paris", ...}
2. Executor calls hotel-tool via HTTP
3. hotel-tool calls Amadeus API
4. Amadeus returns: "error 400: INVALID CITY CODE: PARIS"
5. ClassifyUpstreamError returns HTTP 400 and Retryable:false
6. The tool recognizes this specific city-code validation error and deliberately
   returns HTTP 400, success:false, error.retryable:true, and an actionable hint
7. Executor checks permission, mutable inputs and correction budget → ErrorAnalyzer
8. ErrorAnalyzer calls LLM: "PARIS is not a valid IATA code. Correct code is PAR."
   → SuggestedChanges: {city_code: "PAR"}
9. executor.go: attempt--, applies {city_code: "PAR"}, retries
10. hotel-tool calls Amadeus with city_code=PAR → success
```

### Scenario B: Rate Limiting (Upstream 429)

Multiple parallel steps hit the same API simultaneously.

```
1. stock-market-tool calls external API
2. API returns: "error 429: rate limit exceeded"
3. stock-market-tool: core.ClassifyUpstreamError → {HTTPStatus: 429, Category: RATE_LIMIT}
4. stock-market-tool returns HTTP 429 to executor
5. executor.go → transientHTTPStatus(429) → true (unless retryable:false already stopped the attempt)
6. executor.go continues step retry loop: waits ~542ms (core.BackoffConfig), retries same payload
7. API returns success on attempt 2
```

### Scenario C: Auth Failure (Upstream 401)

An API key has expired.

```
1. flight-tool calls Amadeus API
2. Amadeus returns: "error 401: invalid API key"
3. flight-tool: core.ClassifyUpstreamError → {HTTPStatus: 401, Category: AUTH_ERROR}
4. flight-tool returns HTTP 401 to executor
5. executor.go → terminalHTTPStatus(401) → stop the step before error analysis
6. Step retains the component's HTTP 401 failure
7. No retry — requires configuration fix
```

### Scenario D: Misclassified Input Error via 503

A tool returns 503 even though its message describes invalid input. The executor
respects the transport classification; it does not infer a new parameter from
that transient response.

```
1. A hypothetical tool returns HTTP 503 with body: "location not found: Flower Mound"
2. explicit retryable:false → stop; otherwise transientHTTPStatus(503) → true
3. executor retries the same admitted location under its retry/deadline budget
4. persistent failure remains a failed step; no same-step LLM parameter correction
```

The tool should return a suitable non-transient 4xx for a correctable input
error, with explicit retry permission and an actionable hint. A later planner
may choose recovery, but that is separate from executor-local correction and is
not guaranteed when planning budgets are exhausted.

---

## 9. Configuration Reference

### Environment Variables

| Variable | Default | Description |
|:---|:---|:---|
| `TRUVAG3_STEP_RETRY_INITIAL_DELAY` | `500ms` | Initial backoff delay for step retries |
| `TRUVAG3_STEP_RETRY_MAX_DELAY` | `10s` | Maximum backoff delay cap |
| `TRUVAG3_STEP_RETRY_MAX_ATTEMPTS` | `3` | Max step attempts (initial + retries). Values `<1` are rejected and the default is kept. |
| `TRUVAG3_ORCHESTRATION_TIMEOUT` | `20m` | Base HTTP-call timeout; delegation may enlarge it, while phase/parent deadlines may end calls sooner. See [execution limits](../reference/LIMITS_CHEATSHEET.md#execution-limits). |

### Programmatic Configuration

These methods are on the `SmartExecutor` struct in `orchestration/executor.go`:

```go
// Step retry backoff (overrides env vars)
// Uses core.BackoffConfig from the core module for delay calculation
executor.SetStepRetryBackoff(core.BackoffConfig{
    InitialDelay:  1 * time.Second,
    MaxDelay:      30 * time.Second,
    BackoffFactor: 2.0,
    JitterEnabled: true,
})

// Retry attempts (executor's step retry loop)
executor.SetMaxAttempts(3)                   // Max step retries per step

// LLM Error Analyzer (Layer 3) — uses ErrorAnalyzer from error_analyzer.go
executor.SetErrorAnalyzer(analyzer)          // Enable LLM error analysis
executor.SetValidationFeedback(true, 3)      // Enable corrections, max 3 retries

// Contextual Re-Resolution (Layer 4)
executor.SetContextualReResolver(resolver)   // Enable semantic retry
executor.SetMaxSemanticRetries(2)            // Max Layer 4 attempts
```

### Precedence

`SetStepRetryBackoff()` (explicit) > `TRUVAG3_STEP_RETRY_*` env vars > `core.DefaultBackoffConfig()` defaults.

> For the complete environment variables list, see [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md).

---

## 10. Best Practices

### For Tool Developers

1. **Define and use your tool's upstream-error policy consistently.** The
   reference examples use `sendUpstreamError` plus
   `core.ClassifyUpstreamError`, then explicitly choose
   `core.RedactSensitiveText(err.Error())` for their application response and
   observation surfaces. That transformation belongs to the tool; the
   framework does not install it automatically or guarantee that it recognizes
   domain secrets.

2. **Use your tool's `sendError` method only for tool-local validation** — decode failures, missing required fields, invalid formats detected before calling the upstream API.

3. **Include the upstream HTTP status in your API client's error messages.** `core.ClassifyUpstreamError` (in `core/tool_error.go`) uses a regex to extract the status code, so it must be present in the error string:
   ```go
   // Good: status code is extractable by core.ClassifyUpstreamError
   return fmt.Errorf("Amadeus API error %d: %s", resp.StatusCode, body)

   // Bad: no status code to extract — defaults to 502 (step retry)
   return fmt.Errorf("API call failed: %s", body)
   ```

4. **Call `rw.WriteHeader(status)` BEFORE `json.Encode`.** Go defaults to HTTP 200 if `WriteHeader` is not called explicitly — the executor in `orchestration/executor.go` will treat the error as a success.

5. **Never return mock data on error.** This hides the real problem from the executor and prevents intelligent recovery.

### For Orchestration Developers

1. **Enable the LLM Error Analyzer for production workflows.** Without it, a configured legacy correction callback identifies candidates through type-error patterns or structured `retryable:true` errors. Both paths remain subject to HTTP status, input mutability and correction-budget gates; neither overrides explicit `retryable:false`.

2. **Monitor error handling metrics:**
   - `orchestration.error_analysis.retry` — LLM corrections applied
   - `orchestration.semantic_retry.applied` — Layer 4 corrections applied
   - `orchestration.non_retryable_errors` — errors that stopped retries
   - Physical `dispatch_count` / `dispatches` and step-attempt evidence — distinguish unchanged resends from correction attempts. The legacy `orchestration.transient_error.resilience_retry` counter is unreachable behind current status gates and is not a usable operational signal.

3. **Don't set `maxAttempts` too high.** Each retry incurs latency and potentially LLM cost (for Layer 3/4 analysis).

---

## 11. Troubleshooting

### Errors always retry but never succeed

**Cause:** Tool is returning HTTP 502 for all errors (wrapping upstream 400 as 502). The executor in `orchestration/executor.go` sees 502 and keeps retrying with the same payload.
**Fix:** Ensure the tool uses its `sendUpstreamError` method + `core.ClassifyUpstreamError` instead of hardcoding a status.

### LLM Error Analyzer never triggers

**Cause:** `ErrorAnalyzer` is nil (not set on the executor in `orchestration/executor.go`) or `validationRetries >= maxValidationRetries`.
**Fix:** Check factory setup in `orchestration/factory.go` — `EnableErrorAnalyzer` must be true and `AIClient` must be non-nil.

It is also intentionally skipped for transport/408/429/5xx failures, explicit
`retryable:false`, immutable HITL inputs and delegated orchestrator capabilities.
Those boundaries take precedence over analyzer enablement; increasing the
correction budget does not bypass them.

### Tool errors treated as success

**Cause:** `WriteHeader()` not called before `json.Encode` in the tool's handler. Go defaults to HTTP 200, so the executor in `orchestration/executor.go` treats the response as successful.
**Fix:** Always call `rw.WriteHeader(status)` before encoding the response body in the tool's `sendError` and `sendUpstreamError` methods.

### Retries happen for auth errors

**Cause:** Tool returns HTTP 502 instead of 401/403 for auth failures.
**Fix:** Use `core.ClassifyUpstreamError` (from `core/tool_error.go`) — it maps upstream 401 → tool 401 and 403 → tool 403. The executor rejects these statuses before parameter correction.

### 503 errors not retried

**Check:** A structured `retryable:false` stops all same-step retries, including
503. A Go `ToolError` with an unset bool emits that explicit false. Otherwise,
503 is classified as transient before any parameter-correction call and retried
with unchanged admitted inputs, subject to retry and deadline budgets. It is
not sent to an LLM to determine whether parameters should change.

---

## 12. Quick Reference

### Error Routing Summary

The executor applies `orchestration/dispatch_policy.go` before entering error
analysis. Explicit `retryable:false` takes precedence over every row below.
Delegated orchestrator capabilities retain a single physical dispatch.

| Tool HTTP Status | Route | What Happens |
|:---:|:---|:---|
| Non-transient 4xx, except 401/403/405 | Eligible parameter correction | Requires remaining budget and mutable inputs. Uses enabled analysis, or the configured legacy correction callback when the analyzer is nil and validation feedback is enabled; never resent unchanged. |
| 401, 403, 405 | Fail immediately | Executor's `terminalHTTPStatus()` guard — no retry or error-analysis call |
| 408, 429, all 5xx | Step retry | Same admitted payload + bounded backoff; no model-based parameter correction |

HITL-approved inputs and exact template bindings are protected from correction.
Missing retry policy is not permission to resend a non-transient 4xx unchanged.
Transport failures follow the existing retry budget without parameter rewriting.

### Component Retry Policy on the Wire

The executor reads `error.retryable` only when the response envelope has
`success:false` and an object-valued `error`. The value must be JSON `true` or
`false`; omitted, null or incorrectly typed values are unspecified. This applies
equally to Go and non-Go tools:

```json
{
  "success": false,
  "error": {"code": "INVALID_INPUT", "message": "Provide a valid input", "retryable": false}
}
```

A top-level `retryable`, an error object without `success:false`, or a string
error does not declare policy. Explicit false stops all same-step retries;
explicit true remains subject to terminal HTTP status, delegation, binding and
deadline guards. These flags do not change the HTTP status or make a later
planner-selected operation a same-step retry.

Before every actual dispatch, the executor checks unresolved reserved references,
required-key presence, declared enums and known container-shape mismatches.
The guard preserves scalar coercion, explicit null and custom schema behavior;
it is not a full JSON Schema validator. A rejected guard produces a failed step
without making that HTTP call.

### Minimal Error Handling Pattern

```go
// For upstream API errors:
info := core.ClassifyUpstreamError(err)
safeError := core.RedactSensitiveText(err.Error())
h.sendUpstreamError(rw, "API failed: "+safeError, info)

// For local validation errors:
h.sendError(rw, "city_code is required", http.StatusBadRequest, "MISSING_FIELDS")
```

### Key Files

| Go Module | File | Role in This Error Flow |
|:---|:---|:---|
| `core` | `core/tool_error.go` | `ClassifyUpstreamError`, `UpstreamErrorInfo`, `ErrorCategory`, `ToolError`, `ToolResponse` — shared types used by tools and the `orchestration` module |
| `core` | `core/backoff.go` | `BackoffConfig`, `Delay()` — backoff calculation used by the executor's step retry loop |
| `orchestration` | `orchestration/executor.go` | `executeStep()` step retry loop, `StepRetryBackoff`, env var parsing |
| `orchestration` | `orchestration/dispatch_policy.go` | `terminalHTTPStatus()`, `transientHTTPStatus()`, `componentRetryPolicy()` and `validateDispatchParameters()` |
| `orchestration` | `orchestration/error_analyzer.go` | `ErrorAnalyzer`, `AnalyzeError()`, `routeByHTTPStatus()` for direct analyzer callers, `shouldDelegateToResilience()` |
| `resilience` | `resilience/retry.go` | `RetryExecutor`, `Execute()`, `CircuitBreaker` — standalone retry + circuit breaking. Used by agents (e.g., `agent-with-resilience`), not by the `orchestration` module's executor. See [Section 7](#the-resilience-module-separate-from-step-retry). |

### See Also

- [Intelligent Error Handling](INTELLIGENT_ERROR_HANDLING.md) — Deep dive into LLM error analysis (Layers 3 and 4)
- [Tool Development Guide — Error Handling](../building/TOOL_DEVELOPMENT_GUIDE.md#10-best-practices) — Tool implementation patterns
- [API Reference — ClassifyUpstreamError](../reference/API_REFERENCE.md#classifyupstreamerror) — Type signatures and function docs
- [API Reference — BackoffConfig](../reference/API_REFERENCE.md#backoffconfig) — Backoff calculation details
- [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md) — All configurable env vars
- [Adding Context to Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) — Pipeline-hook error handling: ordinary optional enhancements (RAG, caching, memory) degrade gracefully; an explicitly registered `RequiredAfterPlanningHook` instead fails closed before HITL and execution
