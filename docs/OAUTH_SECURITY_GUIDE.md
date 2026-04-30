# OAuth & Custom Header Propagation Guide

Hey there! This guide will teach you how to secure your TruvaG3 agents with OAuth Bearer Token authentication and custom header propagation. If your tools and services live behind an OAuth gateway, or you need to pass tenant IDs, correlation IDs, or other custom metadata through the orchestration pipeline, this guide has you covered.

> **Reference Example**
>
> While there isn't a dedicated OAuth example yet, all the patterns in this guide can be applied to any TruvaG3 agent. We'll use the travel-chat-agent as our reference:
> - **Agent**: [`examples/travel-chat-agent/`](../examples/travel-chat-agent/)
> - **Frontend**: [`examples/chat-ui/index.html`](../examples/chat-ui/index.html)
>
> The code snippets below show exactly where to add OAuth and header propagation to that agent.

---

## Table of Contents

- [What Problem Does This Solve?](#what-problem-does-this-solve)
- [Quick Start](#quick-start)
- [Core Concepts](#core-concepts)
- [Scenario 1: User Token Pass-Through](#scenario-1-user-token-pass-through)
- [Scenario 2: Machine-to-Machine (M2M) Authentication](#scenario-2-machine-to-machine-m2m-authentication)
- [Custom Header Propagation](#custom-header-propagation)
- [Configuration Reference](#configuration-reference)
- [Security Architecture](#security-architecture)
- [Adding OAuth to the Travel Chat Agent](#adding-oauth-to-the-travel-chat-agent)
- [Runtime Token Refresh](#runtime-token-refresh)
- [Workflow Engine Support](#workflow-engine-support)
- [Troubleshooting](#troubleshooting)
- [Production Deployment](#production-deployment)

---

## What Problem Does This Solve?

When your TruvaG3 orchestrator coordinates multiple tools and services, every outbound HTTP call goes through the executor. Without authentication, those calls look like this:

```
POST /process HTTP/1.1
Host: weather-tool:8080
Content-Type: application/json
X-TruvaG3-Request-ID: orch-1768510279883440759

{"query": "weather in Tokyo"}
```

But in production, your services probably sit behind an API gateway or OAuth provider. They expect an `Authorization: Bearer` header. Without it, every tool call gets a `401 Unauthorized`.

Similarly, in multi-tenant systems, you might need to pass `X-Tenant-ID` or `X-Correlation-ID` through the entire orchestration pipeline so that each downstream tool knows which tenant the request belongs to.

TruvaG3's OAuth and Header Propagation features solve both problems:

```
POST /process HTTP/1.1
Host: weather-tool:8080
Content-Type: application/json
Authorization: Bearer eyJhbGciOiJSUzI1NiIs...
X-Tenant-ID: tenant-42
X-Correlation-ID: req-abc-123
X-TruvaG3-Request-ID: orch-1768510279883440759

{"query": "weather in Tokyo"}
```

### When Should You Use This?

**OAuth Bearer Token** - use when:
- Your tools/services require JWT or opaque OAuth tokens
- You're integrating with an identity provider (Keycloak, Auth0, Okta, etc.)
- Services sit behind an API gateway that validates Bearer tokens
- You need service-to-service authentication using `client_credentials` grant

**Custom Header Propagation** - use when:
- Multi-tenant routing via `X-Tenant-ID`
- Distributed tracing across non-OpenTelemetry services via `X-Correlation-ID`
- Audit logging with `X-Request-Source` or `X-User-ID`
- Any custom metadata that downstream services need to see

You don't need both - use whichever applies to your setup. They work independently and together.

---

## Quick Start

### Prerequisites

Before adding OAuth or header propagation, you should have:
- A working TruvaG3 agent with an orchestrator
  - For **streaming agents**: See the [Chat Agent Guide](CHAT_AGENT_GUIDE.md)
  - For **non-streaming agents**: See [`examples/agent-with-orchestration/`](../examples/agent-with-orchestration/)
- At least one tool registered in your service mesh
- An AI provider API key

### The Fastest Path: Environment Variable

If you just need a static Bearer token on all outbound calls, set one environment variable and you're done:

```bash
# In your .env file or deployment config
TRUVAG3_OAUTH_TOKEN=your-bearer-token-here
```

That's it. Every outbound HTTP call from the orchestrator will now include:
```
Authorization: Bearer your-bearer-token-here
```

No code changes required. `DefaultConfig()` reads this automatically.

### The Programmatic Path: Config Object

For more control, set the token in your orchestrator config:

```go
config := orchestration.DefaultConfig()
config.OAuthToken = "your-bearer-token-here"
config.PropagatedHeaders = map[string]string{
    "X-Tenant-ID":      "tenant-42",
    "X-Correlation-ID": "req-abc-123",
}
```

### The Per-Request Path: Context Injection

For the most flexibility (different token per user, per-request headers), use context:

```go
func handleRequest(w http.ResponseWriter, r *http.Request) {
    // Extract the user's Bearer token from the incoming request
    token := extractBearerToken(r)
    ctx := orchestration.WithOAuthToken(r.Context(), token)

    // Add custom headers from the incoming request
    ctx = orchestration.WithPropagatedHeaders(ctx, map[string]string{
        "X-Tenant-ID":      r.Header.Get("X-Tenant-ID"),
        "X-Correlation-ID": r.Header.Get("X-Correlation-ID"),
    })

    result, err := orchestrator.ProcessRequest(ctx, request, nil)
    // ...
}
```

---

## Core Concepts

### Two-Layer Resolution

Both OAuth tokens and custom headers support **two layers** of configuration. This is the most important concept to understand:

```
Layer 1: Config/Instance Level (default for all requests)
    ↓
Layer 2: Context/Per-Request Level (overrides Layer 1)
```

```
                    ┌──────────────────────────┐
                    │   Incoming HTTP Request   │
                    │  Authorization: Bearer X  │
                    │  X-Tenant-ID: tenant-42   │
                    └───────────┬──────────────┘
                                │
                    ┌───────────▼──────────────┐
                    │     Your HTTP Handler     │
                    │                           │
                    │  ctx = WithOAuthToken(    │  ← Per-request (Layer 2)
                    │    r.Context(), token)    │
                    │  ctx = WithPropagated     │
                    │    Headers(ctx, headers)  │
                    └───────────┬──────────────┘
                                │
                    ┌───────────▼──────────────┐
                    │      Orchestrator         │
                    │                           │
                    │  config.OAuthToken        │  ← Config default (Layer 1)
                    │  config.PropagatedHeaders │
                    └───────────┬──────────────┘
                                │
                    ┌───────────▼──────────────┐
                    │       Executor            │
                    │                           │
                    │  Merges Layer 1 + Layer 2 │
                    │  Context wins on conflict │
                    └───────────┬──────────────┘
                                │
              ┌─────────────────┼─────────────────┐
              ▼                 ▼                  ▼
        ┌──────────┐     ┌──────────┐      ┌──────────┐
        │ Tool A   │     │ Tool B   │      │ Tool C   │
        │          │     │          │      │          │
        │ Auth: X  │     │ Auth: X  │      │ Auth: X  │
        │ Tenant:42│     │ Tenant:42│      │ Tenant:42│
        └──────────┘     └──────────┘      └──────────┘
```

**Why two layers?**

- **Layer 1 (Config)** is great for M2M tokens, static tenant IDs, or headers that don't change per-request. Set it once, forget it.
- **Layer 2 (Context)** is for per-user tokens, per-request correlation IDs, or any header that varies by request. It overrides Layer 1 on key conflict.

### Header Injection Order

The executor applies headers in this exact order. Later headers override earlier ones on key conflict:

| Order | Header | Source | Can Override? |
|-------|--------|--------|---------------|
| 1 | `Content-Type: application/json` | Framework | No (reserved) |
| 2 | `Authorization: Bearer ...` | OAuth (context or config) | No (reserved) |
| 3 | Config propagated headers | `config.PropagatedHeaders` | Yes (by step 4) |
| 4 | Context propagated headers | `WithPropagatedHeaders(ctx)` | Yes (wins) |
| 5 | `X-TruvaG3-Request-ID` | Framework tracing | No (reserved) |
| 6 | `X-TruvaG3-Step-ID` | Framework tracing | No (reserved) |

Framework headers (steps 1, 2, 5, 6) are **protected** - propagated headers cannot override them. This prevents accidental breakage of authentication or distributed tracing.

### Thread Safety

All token and header storage uses `atomic.Value` for lock-free reads. You can safely:
- Call `SetOAuthToken()` from a refresh goroutine while requests are in flight
- Call `SetPropagatedHeaders()` at runtime without stopping the orchestrator
- Use `WithOAuthToken(ctx)` and `WithPropagatedHeaders(ctx)` concurrently across goroutines

---

## Scenario 1: User Token Pass-Through

**Use this when**: Your agent sits behind an API gateway that authenticates users, and you want to pass the user's token through to downstream tools.

```
User → API Gateway → Your Agent → Orchestrator → Tools
         ↓                              ↓
    Validates JWT                  Forwards same JWT
```

### Step 1: Extract the Token in Your Handler

In your SSE handler or HTTP handler, extract the Bearer token from the incoming request and attach it to the context:

```go
// In your SSE handler (like travel-chat-agent's sse_handler.go)
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Extract Bearer token from the incoming request
    token := extractBearerToken(r)
    if token != "" {
        ctx = orchestration.WithOAuthToken(ctx, token)
    }

    // ... rest of your handler uses ctx ...
    // Every outbound tool call will now include Authorization: Bearer <token>
}

// Helper to extract Bearer token from Authorization header
func extractBearerToken(r *http.Request) string {
    auth := r.Header.Get("Authorization")
    if strings.HasPrefix(auth, "Bearer ") {
        return strings.TrimPrefix(auth, "Bearer ")
    }
    return ""
}
```

### Step 2: No Other Changes Needed

That's it. The orchestrator's executor automatically checks context for a token on every outbound call. You don't need to modify the orchestrator config or any tool code.

### How It Works Internally

When the executor makes an outbound HTTP call (in `executor.go`), it checks for a context token first:

```go
// executor.go — callComponentWithBody (simplified)
if token := GetOAuthToken(ctx); token != "" {
    req.Header.Set("Authorization", "Bearer "+token)
} else if configToken := e.getOAuthToken(); configToken != "" {
    req.Header.Set("Authorization", "Bearer "+configToken)
}
```

Context token always wins over config token. If neither is set, no `Authorization` header is sent (backward compatible).

---

## Scenario 2: Machine-to-Machine (M2M) Authentication

**Use this when**: Your agent authenticates as a service (not a user) using OAuth `client_credentials` grant.

```
Your Agent                     OAuth Provider
     │                              │
     │── client_credentials grant ──│
     │                              │
     │←──── access_token ──────────│
     │                              │
     │── Bearer access_token ──→ Tools
```

### Option A: Environment Variable (Simplest)

Set the token via environment variable. `DefaultConfig()` reads it automatically:

```bash
# .env or k8-deployment.yaml
TRUVAG3_OAUTH_TOKEN=eyJhbGciOiJSUzI1NiIs...
```

```yaml
# k8-deployment.yaml
env:
  - name: TRUVAG3_OAUTH_TOKEN
    valueFrom:
      secretKeyRef:
        name: oauth-credentials
        key: access-token
```

### Option B: Config Object (More Control)

Set the token programmatically when creating your orchestrator:

```go
// In your agent's InitializeOrchestrator function
func (t *TravelChatAgent) InitializeOrchestrator(discovery core.Discovery) error {
    config := orchestration.DefaultConfig()
    config.RoutingMode = orchestration.ModeAutonomous
    config.SynthesisStrategy = orchestration.StrategyLLM

    // Set M2M OAuth token
    config.OAuthToken = fetchM2MToken() // Your token fetch logic

    deps := orchestration.OrchestratorDependencies{
        Discovery: discovery,
        AIClient:  t.AI,
        Logger:    t.Logger,
    }

    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return err
    }

    t.orchestrator = orch
    return nil
}
```

### Option C: Runtime Setter (For Token Refresh)

If your M2M token expires and you need to refresh it without restarting:

```go
// Start a background goroutine to refresh the token
go func() {
    for {
        token, expiresIn := fetchM2MToken()
        orchestrator.SetOAuthToken(token)

        // Refresh before expiry (e.g., at 80% of TTL)
        refreshIn := time.Duration(float64(expiresIn) * 0.8)
        time.Sleep(refreshIn)
    }
}()
```

This is thread-safe. In-flight requests continue using the old token until they complete; new requests pick up the refreshed token immediately.

---

## Custom Header Propagation

Custom header propagation lets you attach arbitrary headers to all outbound HTTP calls. This is independent of OAuth - you can use it with or without Bearer tokens.

### Config-Level Headers (Instance Defaults)

Set headers that apply to every request from this orchestrator instance:

```go
config := orchestration.DefaultConfig()
config.PropagatedHeaders = map[string]string{
    "X-Tenant-ID":      "tenant-42",
    "X-Request-Source":  "travel-agent",
    "X-Environment":    "production",
}
```

### Per-Request Headers (Context Override)

Attach headers to a specific request via context:

```go
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Forward incoming headers to downstream tools
    ctx = orchestration.WithPropagatedHeaders(ctx, map[string]string{
        "X-Tenant-ID":      r.Header.Get("X-Tenant-ID"),
        "X-Correlation-ID": r.Header.Get("X-Correlation-ID"),
        "X-User-ID":        r.Header.Get("X-User-ID"),
    })

    // Process with headers attached
    result, err := orchestrator.ProcessRequest(ctx, request, nil)
    // ...
}
```

### Adding a Single Header

Use `AddPropagatedHeader` to add one header without replacing existing ones:

```go
// Start with multiple headers from the incoming request
ctx = orchestration.WithPropagatedHeaders(ctx, map[string]string{
    "X-Tenant-ID":      r.Header.Get("X-Tenant-ID"),
    "X-Correlation-ID": r.Header.Get("X-Correlation-ID"),
})

// Later, add one more header (merges, doesn't replace)
ctx = orchestration.AddPropagatedHeader(ctx, "X-Feature-Flag", "new-pricing-v2")
```

### Runtime Header Update

Update config-level headers at runtime (thread-safe):

```go
// Dynamically update propagated headers without restart
orchestrator.SetPropagatedHeaders(map[string]string{
    "X-Tenant-ID":     "tenant-42",
    "X-Environment":   "canary",     // Changed!
    "X-Canary-Weight": "10",         // New!
})
```

---

## Configuration Reference

### Environment Variables

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `TRUVAG3_OAUTH_TOKEN` | string | `""` (empty) | Static Bearer token for M2M auth. When set, all outbound calls include `Authorization: Bearer <token>`. |

> **Note**: Custom headers don't have env vars because Go maps don't map cleanly to a single environment variable. Set them programmatically via `config.PropagatedHeaders`.

### OrchestratorConfig Fields

```go
type OrchestratorConfig struct {
    // ... other fields ...

    // OAuth Bearer Token for service-to-service authentication.
    // Per-request tokens set via WithOAuthToken(ctx) take priority.
    // When neither is set, no Authorization header is sent (backward compatible).
    //
    // Env: TRUVAG3_OAUTH_TOKEN (default: empty)
    OAuthToken string `json:"-"`

    // PropagatedHeaders defines custom headers to inject into all outbound HTTP calls.
    // Per-request headers set via WithPropagatedHeaders(ctx) override on key conflict.
    //
    // No env var. Set programmatically:
    //   config.PropagatedHeaders = map[string]string{"X-Tenant-ID": tenantID}
    PropagatedHeaders map[string]string `json:"-"`
}
```

Both fields use `json:"-"` to prevent accidental leakage in debug serialization or logging.

### Context Functions

| Function | Description |
|----------|-------------|
| `WithOAuthToken(ctx, token)` | Attach a per-request Bearer token to context |
| `GetOAuthToken(ctx)` | Retrieve the Bearer token from context |
| `WithPropagatedHeaders(ctx, headers)` | Attach per-request custom headers to context |
| `AddPropagatedHeader(ctx, key, value)` | Add a single header (merges with existing) |
| `GetPropagatedHeaders(ctx)` | Retrieve custom headers from context |

### Runtime Setters on AIOrchestrator

| Method | Description |
|--------|-------------|
| `SetOAuthToken(token)` | Update M2M token at runtime (thread-safe) |
| `SetPropagatedHeaders(headers)` | Update config-level headers at runtime (thread-safe) |

### Runtime Setters on WorkflowEngine

| Method | Description |
|--------|-------------|
| `SetOAuthToken(token)` | Update M2M token for workflow executor (thread-safe) |
| `SetPropagatedHeaders(headers)` | Update config-level headers for workflow executor (thread-safe) |

---

## Security Architecture

### Reserved Header Protection

TruvaG3 prevents propagated headers from overriding framework-critical headers. The following headers are **reserved** and will be silently skipped if included in propagated headers:

| Reserved Header | Why It's Protected |
|----------------|-------------------|
| `Authorization` | OAuth Bearer token (set by executor's OAuth logic) |
| `Content-Type` | Always `application/json` (set by executor) |
| `X-Truvag3-Request-Id` | Distributed tracing correlation (set by executor) |
| `X-Truvag3-Step-Id` | Step-level correlation for telemetry (set by executor) |
| `X-Workflow-Id` | Workflow correlation (set by workflow executor) |
| `X-Step-Id` | Workflow step correlation (set by workflow executor) |

Matching is **case-insensitive** via `http.CanonicalHeaderKey`. So `authorization`, `AUTHORIZATION`, and `Authorization` are all treated as reserved.

```go
// This will NOT override the OAuth token — "Authorization" is reserved
ctx = orchestration.WithPropagatedHeaders(ctx, map[string]string{
    "Authorization": "Bearer evil-token",  // Silently skipped
    "X-Tenant-ID":   "tenant-42",          // Applied normally
})
```

### Why Reserved Headers Matter

Without this protection, a malicious or buggy caller could:
1. **Override OAuth** by injecting `Authorization: Bearer evil-token` via propagated headers
2. **Break tracing** by overwriting `X-TruvaG3-Request-ID` with garbage
3. **Corrupt content negotiation** by changing `Content-Type` to `text/plain`

The reserved header guard prevents all three. OAuth is managed by dedicated logic that sets the `Authorization` header from a trusted source (context token or config token), tracing headers are set last (after propagated headers), and Content-Type is set first (before any propagation).

### Token Serialization Safety

Both `OAuthToken` and `PropagatedHeaders` use `json:"-"` struct tags:

```go
OAuthToken        string            `json:"-"` // Never serialized
PropagatedHeaders map[string]string `json:"-"` // Never serialized
```

This means:
- Tokens don't appear in `json.Marshal(config)` output
- Tokens don't leak into debug logs that serialize the config
- Tokens don't appear in the registry viewer or execution store

### Thread-Safe Token Storage

Tokens and headers are stored using `atomic.Value`, which provides lock-free reads:

```go
// SmartExecutor struct (executor.go)
type SmartExecutor struct {
    oauthToken        atomic.Value // stores string
    propagatedHeaders atomic.Value // stores map[string]string
    // ...
}
```

Setter methods make **defensive copies** to prevent external mutation:

```go
func (e *SmartExecutor) SetPropagatedHeaders(headers map[string]string) {
    cpy := make(map[string]string, len(headers))
    for k, v := range headers {
        cpy[k] = v
    }
    e.propagatedHeaders.Store(cpy) // Atomic store of the copy
}
```

This means you can safely mutate the map you passed in after calling `SetPropagatedHeaders` - it won't affect the stored copy.

---

## Adding OAuth to the Travel Chat Agent

Let's walk through adding OAuth and custom headers to the travel-chat-agent step by step.

### Step 1: Add M2M Token via Config

In [`chat_agent.go`](../examples/travel-chat-agent/chat_agent.go), update `InitializeOrchestrator`:

```go
func (t *TravelChatAgent) InitializeOrchestrator(discovery core.Discovery) error {
    t.mu.Lock()
    defer t.mu.Unlock()

    config := orchestration.DefaultConfig()
    config.RoutingMode = orchestration.ModeAutonomous
    config.SynthesisStrategy = orchestration.StrategyLLM
    config.MetricsEnabled = true
    config.EnableTelemetry = true

    // --- NEW: OAuth and custom headers ---
    // M2M token from env (or set programmatically)
    // If TRUVAG3_OAUTH_TOKEN is set, DefaultConfig() already reads it.
    // For explicit control:
    if token := os.Getenv("SERVICE_OAUTH_TOKEN"); token != "" {
        config.OAuthToken = token
    }

    // Instance-level custom headers
    config.PropagatedHeaders = map[string]string{
        "X-Request-Source": "travel-chat-agent",
        "X-Environment":   os.Getenv("APP_ENV"),
    }
    // --- END NEW ---

    config.PlanAIOptions = &orchestration.AIOptionsOverride{
        MaxTokens: orchestration.IntPtr(15000),
    }
    config.SynthesisAIOptions = &orchestration.AIOptionsOverride{
        MaxTokens: orchestration.IntPtr(5000),
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery:           discovery,
        AIClient:            t.AI,
        Logger:              t.Logger,
        Telemetry:           telemetry.GetTelemetryProvider(),
        EnableErrorAnalyzer: true,
    }

    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return fmt.Errorf("failed to create orchestrator: %w", err)
    }

    if err := orch.Start(context.Background()); err != nil {
        return fmt.Errorf("failed to start orchestrator: %w", err)
    }

    t.orchestrator = orch
    return nil
}
```

### Step 2: Forward User Token and Headers Per-Request

In [`sse_handler.go`](../examples/travel-chat-agent/sse_handler.go), update the `ServeHTTP` method to extract and forward authentication headers:

```go
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // --- NEW: Forward user's Bearer token to downstream tools ---
    if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
        token := strings.TrimPrefix(auth, "Bearer ")
        ctx = orchestration.WithOAuthToken(ctx, token)
    }

    // --- NEW: Forward custom headers from the incoming request ---
    propagatedHeaders := map[string]string{}
    if tenantID := r.Header.Get("X-Tenant-ID"); tenantID != "" {
        propagatedHeaders["X-Tenant-ID"] = tenantID
    }
    if corrID := r.Header.Get("X-Correlation-ID"); corrID != "" {
        propagatedHeaders["X-Correlation-ID"] = corrID
    }
    if len(propagatedHeaders) > 0 {
        ctx = orchestration.WithPropagatedHeaders(ctx, propagatedHeaders)
    }
    // --- END NEW ---

    // ... rest of handler (uses ctx throughout) ...
}
```

### Step 3: Update CORS to Allow Authorization Header

The travel-chat-agent's current CORS config allows `Content-Type, Accept, X-Requested-With, X-User-ID`. To accept Bearer tokens from browser clients, add `Authorization` to the allowed headers in [`sse_handler.go`](../examples/travel-chat-agent/sse_handler.go):

```go
// Update CORS to include Authorization header for OAuth
w.Header().Set("Access-Control-Allow-Headers",
    "Content-Type, Accept, X-Requested-With, X-User-ID, Authorization")
```

Without this, browser clients sending `Authorization: Bearer ...` will fail the CORS preflight check.

### Step 4: Update Environment Config

Add the OAuth token to your deployment config:

```bash
# .env
REDIS_URL=redis://localhost:6379
PORT=8099
OPENAI_API_KEY=your-key

# OAuth (new)
TRUVAG3_OAUTH_TOKEN=your-m2m-bearer-token

# Or use a custom env var if you prefer
SERVICE_OAUTH_TOKEN=your-m2m-bearer-token
```

```yaml
# k8-deployment.yaml
env:
  - name: TRUVAG3_OAUTH_TOKEN
    valueFrom:
      secretKeyRef:
        name: travel-agent-oauth
        key: access-token
```

---

## Runtime Token Refresh

For M2M tokens that expire, you'll want a background refresh loop. Here's a production-grade pattern:

```go
// tokenRefresher manages OAuth token lifecycle for the orchestrator.
func startTokenRefresher(orch *orchestration.AIOrchestrator, logger core.Logger) {
    go func() {
        for {
            // Fetch a new token from your OAuth provider
            token, expiresIn, err := fetchClientCredentialsToken()
            if err != nil {
                logger.Error("Token refresh failed", map[string]interface{}{
                    "error": err.Error(),
                })
                // Retry after a short backoff
                time.Sleep(30 * time.Second)
                continue
            }

            // Update the orchestrator's token (thread-safe, lock-free)
            orch.SetOAuthToken(token)
            logger.Info("OAuth token refreshed", map[string]interface{}{
                "expires_in": expiresIn.String(),
            })

            // Refresh at 80% of TTL to avoid edge-of-expiry failures
            refreshIn := time.Duration(float64(expiresIn) * 0.8)
            time.Sleep(refreshIn)
        }
    }()
}

// fetchClientCredentialsToken calls your OAuth provider's token endpoint.
func fetchClientCredentialsToken() (string, time.Duration, error) {
    // Example: POST to your OAuth provider
    // resp, err := http.PostForm("https://auth.example.com/oauth/token", url.Values{
    //     "grant_type":    {"client_credentials"},
    //     "client_id":     {os.Getenv("OAUTH_CLIENT_ID")},
    //     "client_secret": {os.Getenv("OAUTH_CLIENT_SECRET")},
    //     "scope":         {"tools:read tools:execute"},
    // })
    //
    // Parse response for access_token and expires_in
    // return token, time.Duration(expiresIn) * time.Second, nil

    return "placeholder", 1 * time.Hour, nil
}
```

Usage in your agent's initialization:

```go
// After creating the orchestrator
orch, err := orchestration.CreateOrchestrator(config, deps)
if err != nil {
    return err
}

// Start background token refresh
startTokenRefresher(orch, t.Logger)
```

---

## Workflow Engine Support

If you're using the `WorkflowEngine` for YAML-defined workflows, OAuth and header propagation work identically. The workflow engine delegates to the same underlying HTTP client.

### Config-Level (Set on WorkflowEngine)

```go
engine := orchestration.NewWorkflowEngine(discovery, stateStore, logger)
engine.SetOAuthToken("your-m2m-token")
engine.SetPropagatedHeaders(map[string]string{
    "X-Tenant-ID": "tenant-42",
})
```

### Per-Request (Context)

```go
ctx := orchestration.WithOAuthToken(r.Context(), userToken)
ctx = orchestration.WithPropagatedHeaders(ctx, map[string]string{
    "X-Correlation-ID": correlationID,
})

result, err := engine.ExecuteWorkflow(ctx, workflow, inputs)
```

Both `CallService` and `HealthCheck` in the workflow executor honor the same two-layer resolution.

---

## Troubleshooting

### "My tools still return 401 Unauthorized"

1. **Check that the token is being set.** Add a debug log in your handler:
   ```go
   token := orchestration.GetOAuthToken(ctx)
   logger.Debug("OAuth token present", map[string]interface{}{
       "has_token": token != "",
       "length":    len(token),
   })
   ```

2. **Check that `TRUVAG3_OAUTH_TOKEN` is loaded.** If using the env var, ensure your deployment actually sets it:
   ```bash
   kubectl exec -it <pod> -- env | grep TRUVAG3_OAUTH_TOKEN
   ```

3. **Check token format.** TruvaG3 prepends `Bearer ` automatically. Don't include "Bearer " in your token value:
   ```bash
   # Correct
   TRUVAG3_OAUTH_TOKEN=eyJhbGciOiJSUzI1NiIs...

   # Wrong (will produce "Bearer Bearer eyJ...")
   TRUVAG3_OAUTH_TOKEN="Bearer eyJhbGciOiJSUzI1NiIs..."
   ```

### "My custom headers aren't reaching downstream tools"

1. **Check for reserved headers.** If you're trying to set `Authorization`, `Content-Type`, or any `X-TruvaG3-*` header via propagation, it will be silently skipped. Use `WithOAuthToken()` for auth instead.

2. **Check that context is passed through.** Make sure the context with headers is the same context used in `ProcessRequest` or `ProcessRequestStreaming`:
   ```go
   // Wrong - ctx without headers
   ctx := context.Background()
   result, err := orch.ProcessRequest(ctx, request, nil)

   // Right - ctx with headers
   ctx := orchestration.WithPropagatedHeaders(r.Context(), headers)
   result, err := orch.ProcessRequest(ctx, request, nil)
   ```

3. **Verify on the receiving side.** Add a log in your tool to confirm which headers arrive:
   ```go
   func handleProcess(w http.ResponseWriter, r *http.Request) {
       log.Printf("Headers received: %v", r.Header)
       // ...
   }
   ```

### "Config-level headers are being overridden unexpectedly"

This is by design. Context-level headers override config-level headers on key conflict. If you set `X-Tenant-ID: tenant-A` in config and `X-Tenant-ID: tenant-B` in context, `tenant-B` wins.

To debug, check whether a context header is being set unintentionally:
```go
ctxHeaders := orchestration.GetPropagatedHeaders(ctx)
logger.Debug("Context headers", map[string]interface{}{
    "headers": ctxHeaders,
})
```

### "I set Authorization in PropagatedHeaders but it's ignored"

Correct behavior. `Authorization` is a reserved header - it's managed by the OAuth system. Use `WithOAuthToken(ctx, token)` or `config.OAuthToken` instead. This prevents accidental token conflicts between the OAuth system and propagated headers.

---

## Production Deployment

### Kubernetes Secrets for Tokens

Never hardcode tokens in your deployment YAML. Use Kubernetes secrets:

```yaml
# Create the secret
# kubectl create secret generic oauth-credentials \
#   --from-literal=access-token=eyJhbGci...

# Reference in deployment
env:
  - name: TRUVAG3_OAUTH_TOKEN
    valueFrom:
      secretKeyRef:
        name: oauth-credentials
        key: access-token
```

### Token Rotation Strategy

For M2M tokens in production:

1. **Short-lived tokens + refresh loop** (recommended): Use `client_credentials` grant with 1-hour tokens and the refresh pattern shown above.
2. **Long-lived tokens + secret rotation**: Use longer-lived tokens stored in Kubernetes secrets, rotated via your secret management tool (Vault, AWS Secrets Manager, etc.). Update the secret and restart pods.
3. **Sidecar pattern**: Run an OAuth sidecar that manages tokens and exposes them via a local file or API. Your agent reads the refreshed token periodically.

### Multi-Tenant Header Propagation

For multi-tenant deployments, extract the tenant from your API gateway and propagate it:

```go
func tenantMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        tenantID := r.Header.Get("X-Tenant-ID")
        if tenantID == "" {
            http.Error(w, "X-Tenant-ID required", http.StatusBadRequest)
            return
        }

        ctx := orchestration.WithPropagatedHeaders(r.Context(), map[string]string{
            "X-Tenant-ID": tenantID,
        })

        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### Monitoring

Watch for these signals that your auth setup is working:

- **No 401 errors in tool logs**: If you see `component returned status 401`, the token isn't reaching the tool or is expired.
- **Token refresh logging**: If using the runtime refresh pattern, your `startTokenRefresher` goroutine logs success/failure. Consider adding custom metrics (e.g., `telemetry.Counter("oauth.token_refresh", "status", "success")`) for production monitoring.
- **Header presence in tool logs**: Add request header logging on the receiving tool side to verify propagated headers are arriving as expected.

---

## Summary

| Feature | Env Var | Config Field | Context Function | Runtime Setter |
|---------|---------|-------------|-----------------|----------------|
| OAuth Bearer Token | `TRUVAG3_OAUTH_TOKEN` | `config.OAuthToken` | `WithOAuthToken(ctx, token)` | `orch.SetOAuthToken(token)` |
| Custom Headers | N/A | `config.PropagatedHeaders` | `WithPropagatedHeaders(ctx, map)` | `orch.SetPropagatedHeaders(map)` |
| Single Header | N/A | N/A | `AddPropagatedHeader(ctx, k, v)` | N/A |

All features are **opt-in** and **backward compatible**. If you don't set any token or headers, the orchestrator behaves exactly as before - no `Authorization` header is sent, no custom headers are injected.

For questions or issues, check the source code in [`orchestration/orchestrator.go`](../orchestration/orchestrator.go) and [`orchestration/executor.go`](../orchestration/executor.go), or open an issue on the repository.
