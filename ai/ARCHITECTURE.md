# TruvaG3 AI Module Architecture

**Version**: 1.8
**Module**: `github.com/truvaagents/truva-g3/ai`
**Purpose**: Production-grade AI provider abstraction with multi-provider support
**Audience**: Framework developers, application developers, operations teams

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Design Philosophy](#design-philosophy)
3. [Module Dependencies](#module-dependencies)
4. [Provider Registry System](#provider-registry-system)
5. [Provider Implementation Pattern](#provider-implementation-pattern)
6. [Chain Client (Failover)](#chain-client-failover)
7. [AI Agent and AI Tool](#ai-agent-and-ai-tool)
8. [Logging and Telemetry](#logging-and-telemetry)
9. [Configuration System](#configuration-system)
10. [Integration Patterns](#integration-patterns)
11. [Common Pitfalls](#common-pitfalls)
12. [Troubleshooting Guide](#troubleshooting-guide)

---

## Architecture Overview

### System Context

```
┌─────────────────────────────────────────────────────────────┐
│ Application Layer                                            │
│                                                             │
│  client, _ := ai.NewClient(ai.WithProvider("openai"))      │
│  response, _ := client.GenerateResponse(ctx, prompt, opts)  │
└─────────────────────────────────────────────────────────────┘
                         │
                         │ Uses factory pattern
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ AI Module (github.com/truvaagents/truva-g3/ai)                 │
│                                                             │
│  ┌─────────────┐    ┌──────────────┐    ┌──────────────┐  │
│  │  Registry   │───>│ ProviderFactory│───>│ AIClient    │  │
│  │  (Global)   │    │ (Per Provider)│    │ (Per Call)  │  │
│  └─────────────┘    └──────────────┘    └──────────────┘  │
│         │                                                   │
│         │ Import-time registration via init()              │
└─────────────────────────────────────────────────────────────┘
                         │
                         │ Implements core.AIClient
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ Provider Clients                                            │
│                                                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │ OpenAI   │  │Anthropic │  │  Gemini  │  │ Bedrock  │  │
│  │ Client   │  │  Client  │  │  Client  │  │  Client  │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
│         │            │            │            │           │
│         └────────────┴────────────┴────────────┘           │
│                              │                             │
│                    All embed BaseClient                    │
└─────────────────────────────────────────────────────────────┘
                         │
                         │ HTTPS API calls
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ External AI APIs                                            │
│                                                             │
│  OpenAI, Anthropic, Google Gemini, AWS Bedrock, etc.       │
└─────────────────────────────────────────────────────────────┘
```

### Key Components

| Component | Responsibility | Location |
|-----------|----------------|----------|
| `ProviderRegistry` | Global registry of provider factories | `registry.go` |
| `ProviderFactory` | Interface for provider creation | `registry.go` |
| `AIConfig` | Configuration options for clients | `provider.go` |
| `BaseClient` | Shared functionality (retry, logging) | `providers/base.go` |
| `ChainClient` | Multi-provider failover | `chain_client.go` |
| `requestpolicy.Engine` | Deterministic provider-request policy evaluation | `requestpolicy/` |
| `openaiwire.Codec` | Reusable OpenAI-compatible request/response translation | `providerkit/openaiwire/` |
| `AIAgent` | Agent with AI + discovery capabilities | `ai_agent.go` |
| `AITool` | Tool with AI capabilities (no discovery) | `ai_tool.go` |

---

## Design Philosophy

### 1. Import-Driven Provider Registration

**The Design Decision**: Providers self-register via `init()` functions when their package is imported.

```go
// Application chooses which providers to include at compile time
import (
    "github.com/truvaagents/truva-g3/ai"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"     // Registers OpenAI
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"  // Registers Anthropic
    // Don't import bedrock → not compiled in, smaller binary
)
```

**Benefits**:
- **Compile-time selection**: Only imported providers are included in binary
- **No runtime configuration**: Provider availability is determined at build time
- **Smaller binaries**: Unused providers don't bloat the executable
- **Clear dependencies**: `go.mod` shows exactly which providers are used

### 2. Factory Pattern for Client Creation

**The Design Decision**: Each provider implements `ProviderFactory` interface.

```go
type ProviderFactory interface {
    Create(config *AIConfig) core.AIClient
    DetectEnvironment() (priority int, available bool)
    Name() string
    Description() string
}
```

**Why Factory Pattern?**

| Pattern | Pros | Cons | TruvaG3 Choice |
|---------|------|------|---------------|
| Direct instantiation | Simple | Tight coupling | ❌ |
| Factory method | Flexible, testable | Slightly more code | ✅ Chosen |
| Dependency injection | Maximum flexibility | Complex setup | ❌ |

**Benefits**:
1. **Environment detection**: Auto-detect available providers
2. **Priority-based selection**: Choose best provider automatically
3. **Configuration injection**: Pass config at creation time
4. **Testability**: Easy to mock provider factories

### 3. Shared Base Client for Cross-Cutting Concerns

**The Design Decision**: All provider clients embed `BaseClient`.

```go
// providers/openai/client.go
type Client struct {
    *providers.BaseClient  // Embedded - provides retry, logging, defaults
    apiKey  string
    baseURL string
}
```

**What BaseClient Provides**:
- HTTP client with configurable timeout
- Exponential backoff retry logic
- Request/response logging
- Default value management
- Error handling utilities

### 4. Tool/Agent Separation with AI

**The Design Decision**: Maintain the framework's Tool/Agent distinction.

```
┌─────────────────────────────────────────────────────────────┐
│ AITool (Passive)                                            │
│ - Uses AI for capabilities                                  │
│ - NO discovery (cannot find other components)               │
│ - Example: Translation tool, Summarization tool             │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ AIAgent (Active)                                            │
│ - Uses AI for orchestration                                 │
│ - HAS discovery (can find and coordinate components)        │
│ - Example: Research agent, Planning agent                   │
└─────────────────────────────────────────────────────────────┘
```

This enforced distinction prevents architectural violations where tools accidentally become orchestrators.

### 5. Valuable, Not Mandatory

**The Design Decision**: The `ai` module should be the **best default path**, not a required abstraction wall.

TruvaG3 should support major providers out of the box, but developers must always retain two freedoms:

1. **Use the AI module for common portability and framework integration**
2. **Bypass the AI module and use a provider's native SDK directly when they need full control**

This means the AI module is intentionally designed as a convenience layer with strong defaults, not as the only allowed way to talk to an LLM provider.

**What this implies architecturally**:

- The framework should support major providers directly (`openai`, `anthropic`, `gemini`, `bedrock`, and key OpenAI-compatible aliases).
- The framework must not force developers to abandon `ai/` just because a provider shipped a new request field that TruvaG3 has not modeled yet.
- A developer who prefers the provider's native SDK should be able to plug that choice into TruvaG3 cleanly by supplying a compatible client or adapter.
- Framework-owned abstractions should focus on the stable common core, not on exhaustively normalizing every vendor-specific feature.

**Non-goal**:

- The AI module is **not** trying to become a perfect universal compatibility layer for every provider feature across time.

Instead, its job is to provide:

- a **portable core** for the common 80%
- **escape hatches** for provider-specific innovation
- a **clean exit path** for teams that want native SDK control

### 6. Portable Core, Provider Escape Hatches

**The Design Decision**: Separate the stable cross-provider surface from provider-specific extensions.

The AI module should expose a small portable core that TruvaG3 itself can rely on:

- model selection
- temperature
- max tokens
- system prompt
- reasoning intent
- response format / structured output intent

At the same time, it should preserve flexibility through escape hatches:

1. **Client-level provider extras and headers** for long-lived defaults
2. **Per-request provider extras and headers** for agent-specific or phase-specific parameters

This is the mechanism that prevents the framework from becoming obsolete every time a provider adds a new field.

**Principle**:

> A missing first-class TruvaG3 field or header must never force a developer to stop using the AI module.

If the provider supports an extra request field or a required request header and the developer wants to use it from agent code, the AI module should allow that via provider-specific extras/headers until or unless the capability becomes common enough to deserve first-class promotion.

### 7. Minimal Compatibility Responsibility

**The Design Decision**: The AI module should own only the minimum compatibility logic required to keep TruvaG3 safe and useful.

That means:

- It **should** protect users from obviously invalid provider/model combinations for the features TruvaG3 relies on heavily.
- It **should** translate a small number of semantic concepts that matter to the framework, such as reasoning intent or structured-output intent.
- It **should not** try to completely normalize every field or every vendor-specific feature.

The correct posture is:

- **thin compatibility guardrails**
- **best-effort portability**
- **clear logging when a requested feature is degraded or ignored**

not:

- full universal feature parity
- exhaustive modeling of the entire provider ecosystem

This keeps the module useful without over-committing the framework to an endless compatibility matrix.

---

## Module Dependencies

### Dependency Decision

```
Valid Dependencies:
┌─────────────────────────────────────────────────────────────┐
│  ai  →  core  +  telemetry                                  │
└─────────────────────────────────────────────────────────────┘
```

**Rationale**: The AI module is expanded to include telemetry for production visibility:

| Dependency | Purpose | Justification |
|------------|---------|---------------|
| `core` | Interfaces (AIClient, Logger) | Required foundation |
| `telemetry` | Metrics emission | Production observability |

**Why `ai` needs `telemetry`**:
1. **External API calls**: AI providers make external HTTP calls that need latency/error tracking
2. **Usage visibility**: Normalized token counts support capacity and efficiency analysis
3. **Failover tracking**: Chain client failovers need metrics
4. **Consistency**: Matches `resilience` and `orchestration` modules

### Import Structure

```go
// ai/client.go
import (
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"  // Allowed
)

// ai/providers/openai/client.go
import (
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/ai/providers"
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"  // Allowed
)
```

---

## Provider Registry System

### Global Registry Architecture

```go
// registry.go
var registry = &ProviderRegistry{
    providers: make(map[string]ProviderFactory),
}

// Thread-safe registration
func Register(factory ProviderFactory) error {
    registry.mu.Lock()
    defer registry.mu.Unlock()

    if _, exists := registry.providers[factory.Name()]; exists {
        return fmt.Errorf("provider '%s' already registered", factory.Name())
    }
    registry.providers[factory.Name()] = factory
    return nil
}
```

### Provider Registration Flow

```
┌──────────────────────────────────────────────────────────────┐
│ Application Startup                                           │
│                                                              │
│ 1. Go runtime processes imports                              │
│ 2. Each provider's init() runs                               │
│ 3. init() calls ai.Register(&Factory{})                      │
│ 4. Factory stored in global registry                         │
└──────────────────────────────────────────────────────────────┘
          │
          ↓
┌──────────────────────────────────────────────────────────────┐
│ Provider Registration (e.g., openai/factory.go)              │
│                                                              │
│ func init() {                                                │
│     if err := ai.Register(&Factory{}); err != nil {          │
│         panic(err)  // Fail fast on registration error       │
│     }                                                        │
│ }                                                            │
└──────────────────────────────────────────────────────────────┘
          │
          ↓
┌──────────────────────────────────────────────────────────────┐
│ Client Creation                                              │
│                                                              │
│ client, _ := ai.NewClient(                                   │
│     ai.WithProvider("openai"),  // Lookup in registry        │
│ )                                                            │
│                                                              │
│ // Registry finds "openai" factory, calls factory.Create()   │
└──────────────────────────────────────────────────────────────┘
```

### Auto-Detection Logic

The detection system supports both simple providers and multi-sub-provider factories (e.g., OpenAI managing Groq, DeepSeek, etc.) via the optional `SubProviderEnumerator` interface:

```go
// Optional interface for factories managing multiple sub-providers
type SubProviderEnumerator interface {
    DetectAvailableAliases() []AliasAvailability
}

type AliasAvailability struct {
    Alias        string // Full alias: "openai.groq", "anthropic"
    ProviderName string // Base factory name: "openai", "anthropic"
    Priority     int    // Detection priority (higher = tried first)
}

// DetectAvailableProviders returns all available providers sorted by priority
func DetectAvailableProviders(logger core.Logger) []AliasAvailability {
    // For each registered factory:
    //   - If it implements SubProviderEnumerator → collect per-alias entries
    //   - Otherwise → use DetectEnvironment() as single entry
    // Returns sorted by priority descending, only available entries
}
```

The `detectBestProvider()` function delegates to `DetectAvailableProviders()` and returns the highest-priority result. This ensures single-client auto-detection and chain auto-detection share the same logic.

**Provider Priorities** (default):
| Provider | Alias | Priority | Detection Method |
|----------|-------|----------|------------------|
| OpenAI | `openai` | 1000 | `OPENAI_API_KEY` exists |
| Anthropic | `anthropic` | 900 | `ANTHROPIC_API_KEY` exists |
| Gemini | `gemini` | 800 | `GOOGLE_API_KEY` or `GEMINI_API_KEY` exists |
| Groq | `openai.groq` | 700 | `GROQ_API_KEY` exists |
| DeepSeek | `openai.deepseek` | 600 | `DEEPSEEK_API_KEY` exists |
| xAI | `openai.xai` | 500 | `XAI_API_KEY` exists |
| Mistral | `openai.mistral` | 450 | `MISTRAL_API_KEY` exists |
| Qwen | `openai.qwen` | 400 | `QWEN_API_KEY` exists |
| Together | `openai.together` | 300 | `TOGETHER_API_KEY` exists |
| Bedrock | `bedrock` | 200 | AWS credentials available |
| Ollama | `openai.ollama` | 100 | `OLLAMA_BASE_URL` set and local server reachable |
| Mock | `mock` | 1 | Never auto-detected |

---

## Provider Implementation Pattern

### Factory Implementation

Each provider implements this pattern:

```go
// providers/openai/factory.go
package openai

func init() {
    if err := ai.Register(&Factory{}); err != nil {
        panic(fmt.Sprintf("failed to register openai provider: %v", err))
    }
}

type Factory struct{}

func (f *Factory) Name() string        { return "openai" }
func (f *Factory) Description() string { return "OpenAI GPT models" }

func (f *Factory) DetectEnvironment() (priority int, available bool) {
    if os.Getenv("OPENAI_API_KEY") != "" {
        return 100, true  // High priority when key exists
    }
    return 0, false
}

func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
    // Extract configuration
    apiKey := firstNonEmpty(config.APIKey, os.Getenv("OPENAI_API_KEY"))
    baseURL := firstNonEmpty(config.BaseURL, os.Getenv("OPENAI_BASE_URL"), DefaultBaseURL)

    // CRITICAL: Get logger from config, wrap with component
    logger := config.Logger
    if logger != nil {
        if cal, ok := logger.(core.ComponentAwareLogger); ok {
            logger = cal.WithComponent("framework/ai")
        }
    }

    return NewClient(apiKey, baseURL, logger)
}
```

### Client Implementation

```go
// providers/openai/client.go
package openai

type Client struct {
    *providers.BaseClient  // Embedded for retry, logging
    apiKey  string
    baseURL string
}

func NewClient(apiKey, baseURL string, logger core.Logger) *Client {
    base := providers.NewBaseClient(180*time.Second, logger)  // 3 min default for reasoning models
    base.DefaultModel = "gpt-3.5-turbo"
    base.DefaultMaxTokens = 1000

    return &Client{
        BaseClient: base,
        apiKey:     apiKey,
        baseURL:    baseURL,
    }
}

func (c *Client) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
    // Apply defaults
    options = c.ApplyDefaults(options)
    semanticModel := ResolveModel("openai", options.Model)

    // Log only context-correlated, sanitized request metadata.
    c.LogRequestMetadata(ctx, providers.RequestObservation{
        Provider:      "openai",
        ProviderAlias: "openai",
        SemanticModel: semanticModel,
        PromptLength:  len(prompt),
    })
    startTime := time.Now()

    // Build and execute request...

    // Keep provider-reported wire model data out of metric dimensions.
    c.LogResponseMetadata(ctx, providers.ResponseObservation{
        Provider:      "openai",
        ProviderAlias: "openai",
        SemanticModel: semanticModel,
        Usage:         result.Usage,
        Duration:      time.Since(startTime),
    })

    return result, nil
}
```

### OpenAI-Compatible Providers (Provider Aliases)

The `ai` module supports OpenAI-compatible providers through aliases:

```go
// Usage
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai.deepseek"),  // Uses DeepSeek API
    ai.WithModel("smart"),                     // Resolves to "deepseek-reasoner"
)
```

**Supported Aliases**:
| Alias | Base URL | API Key Env |
|-------|----------|-------------|
| `openai.deepseek` | `api.deepseek.com` | `DEEPSEEK_API_KEY` |
| `openai.groq` | `api.groq.com/openai/v1` | `GROQ_API_KEY` |
| `openai.xai` | `api.x.ai/v1` | `XAI_API_KEY` |
| `openai.together` | `api.together.xyz/v1` | `TOGETHER_API_KEY` |
| `openai.qwen` | `dashscope-intl.aliyuncs.com/...` | `QWEN_API_KEY` |
| `openai.ollama` | `localhost:11434/v1` | (none required) |

---

## Chain Client (Failover)

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ ChainClient                                                  │
│                                                             │
│  GenerateResponse(ctx, prompt, opts)                        │
│         │                                                   │
│         ├──→ Provider 1 (OpenAI) ────→ Success? Return     │
│         │         │                                        │
│         │         ↓ Failure (5xx)                          │
│         │                                                   │
│         ├──→ Provider 2 (Anthropic) ──→ Success? Return    │
│         │         │                                        │
│         │         ↓ Failure (5xx)                          │
│         │                                                   │
│         └──→ Provider 3 (Gemini) ────→ Success? Return     │
│                   │                                        │
│                   ↓ All failed                             │
│                                                             │
│              Return aggregated error                        │
└─────────────────────────────────────────────────────────────┘
```

### Usage

```go
// Explicit chain: providers are tried in the order specified
chain, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "openai.deepseek", "anthropic"),
    ai.WithChainLogger(logger),
)

// Uses OpenAI first, falls back to DeepSeek, then Anthropic
response, err := chain.GenerateResponse(ctx, prompt, opts)
```

### Explicit Heterogeneous Entries

`NewChain` is the request-aware construction path for independently configured
provider instances and application-local clients:

```go
chain, err := ai.NewChain(
    ai.ProviderEntry(
        "anthropic-primary",
        "anthropic",
        ai.WithModel("premium"),
        ai.WithCredentialSource(primaryCredentials),
        ai.WithEndpointResolver(primaryRoute),
        ai.WithRequestRules(primaryRules...),
    ),
    ai.ProviderEntry(
        "anthropic-backup",
        "anthropic",
        ai.WithModel("fast"),
        ai.WithCredentialSource(backupCredentials),
        ai.WithEndpointResolver(backupRoute),
    ),
    ai.ClientEntry("local-native-adapter", nativeClient),
)

result, err := chain.Generate(ctx, request)
```

Provider entries are framework-managed and are constructed through
`NewRequestClient`; therefore the selected factory must support request-aware
construction. OpenAI, Azure OpenAI, Anthropic, and Gemini support it directly. Bedrock
supports it when the application is compiled with the `bedrock` build tag.
Providers without a request-aware factory can still be supplied through
`ClientEntry` as legacy or application-local clients.
`ClientEntry` accepts any `core.AIClient`, including application-local
request-aware or native adapters.
Injected clients remain caller-owned: the chain invokes them but does not call
optional logger, telemetry, or lifecycle setters on them.

Entry names must be unique, stable, non-secret operator labels. They appear in
sanitized chain report adjustments, logs, and spans, but never as metric
dimensions. Every attempt receives a recursive `core.CloneAIRequest` snapshot,
so failed providers cannot mutate the request, legacy options, or provider
patches observed by a later entry. A provider report returned with either
success or failure is preserved and receives a chain adjustment containing the
entry name and attempt number.

`GenerateResponse` and `StreamResponse` remain compatible legacy adapters. For
chains built through `NewChain` or `NewChainClient`, they compile the legacy
prompt/options into a provider-neutral request and use the same request-aware
failover loop. A legacy-only entry is called only when it can represent the
request without dropping advanced semantics; otherwise the entry produces an
unsupported-capability failure and the chain may continue.

Request-aware streaming follows the same entry order. Failover is allowed only
before the application callback receives a chunk. Once any chunk is visible,
the chain returns that entry's partial result and error instead of switching
providers and corrupting stream semantics. Each attempt delegates request-aware
versus legacy capability adaptation to `core.StreamAI`, which is the single
owner of the same lossless representability rules used by `core.GenerateAI`.

### Auto-Detect Mode

When no `WithProviderChain` is specified, the chain client auto-detects available providers from the environment and orders them by priority:

```go
// Auto-detect: discovers providers from API keys in environment
chain, err := ai.NewChainClient(
    ai.WithChainLogger(logger),
)
// If ANTHROPIC_API_KEY and GROQ_API_KEY are set:
// → chain = ["anthropic" (900), "openai.groq" (700)]
```

**How auto-detect works**:
1. Calls `DetectAvailableProviders()` which checks all registered factories
2. Factories implementing `SubProviderEnumerator` (e.g., OpenAI) report each sub-provider individually
3. Results are sorted by priority (highest first) and used as the chain order
4. If no providers are detected, fails fast with: `"configuration error: no providers detected (check API keys)"`

**When to use auto-detect vs explicit chain**:
| Scenario | Recommendation |
|----------|----------------|
| Development / prototyping | Auto-detect — adapts to whatever keys are available |
| Staging | Auto-detect — mirrors production env vars |
| Production (deterministic order matters) | Explicit `WithProviderChain` — locks the failover order |
| Production (environment-driven) | Auto-detect — ops controls order via which keys are deployed |

### Failover Behavior

| Error Type | Behavior | Rationale |
|------------|----------|-----------|
| **Client errors (4xx)** | Fail fast, no retry | Request is invalid for all providers |
| **Transient proxy errors (4xx, `IsTransient`)** | Try next provider | Proxy/infra issue (e.g., Cloudflare), not a request problem |
| **Provider-specific terminal errors (4xx, `IsRetryable`)** | Try next provider | Billing exhausted, account suspended, or similar — terminal on this provider but may succeed on a different one. Detected by `BaseClient.HandleError` from a narrow set of structured response markers (`credit balance`, `insufficient_quota`, `payment required`, and case/underscore variants); see [providers/base.go](providers/base.go) `billingExhaustedPhrases` for the authoritative list. |
| **Server errors (5xx)** | Try next provider | Provider-specific issue |
| **Rate limits (429)** | Try next provider | Provider at capacity |
| **Network errors** | Try next provider | Transient connectivity |

The two override flags (`IsTransient` and `IsRetryable`) are independent — see
godoc on `core.ProviderError` for the contract. Both bypass the fail-fast 4xx
classification, but they signal different conditions: `IsTransient` = "this
never reached the API" (proxy 4xx), `IsRetryable` = "the API gave a definitive
answer that may differ on a different provider" (billing/quota).
`IsRetryable` takes precedence over generic HTTP-status classification when the
chain derives the bounded `failover_reason`, so an `insufficient_quota` response
reported as HTTP 429 remains `provider_retryable`; an ordinary 429 remains
`rate_limit`. This bounded reason is the canonical operator-action signal on
both non-terminal failover and terminal exhaustion events. Operations whose
names say “failover” are emitted only when another entry actually remains.

### Per-Provider Retry Budget

Inside each provider, `BaseClient.ExecuteWithRetry` may retry on transient errors (5xx, 429, network, CDN) with exponential backoff before returning the final error. When a chain client sits above the provider, a returned error triggers failover to the next provider. The retry count and its default depend on whether the provider was created standalone or inside a chain.

**Single client (`ai.NewClient`)** — default `MaxRetries = 3`. Single clients have no failover layer below them, so in-provider retries are the only mechanism for absorbing transient blips (5xx, 429, network errors, brief CDN hiccups). The historical default of 3 is preserved.

**Chain client (`ai.NewChainClient`)** — default per-provider `MaxRetries = 0`. The chain client's failover loop IS the retry mechanism inside a chain: when a provider fails on a retryable error, the chain walks to the next provider. Per-provider in-provider retries inside a chain just amplify wasted token spend on the dead provider before failover kicks in. Operators who want in-provider retries inside a chain (e.g. flaky network during a deploy where one provider should retry once before failover) must opt in via `ai.WithChainMaxRetries(n)` or `TRUVAG3_AI_RETRY_ATTEMPTS=n`.

**Configuration knobs:**

- **`ai.WithMaxRetries(n)`** — single-client option. Programmatic Go API; any non-negative integer is honored, including `0` (no retries).
- **`ai.WithChainMaxRetries(n)`** — chain option. Applies uniformly to every provider in the chain. Same semantics as `WithMaxRetries`; overrides the chain default of 0.
- **`TRUVAG3_AI_RETRY_ATTEMPTS`** — env var fallback for both client types. Per FRAMEWORK_DESIGN_PRINCIPLES §3.5 rule 3, env var values are guarded with `val > 0`. Zero, negative, and non-integer values are silently rejected and fall through to the appropriate default (3 for single, 0 for chain).

**Precedence (highest to lowest):**

1. Explicit `WithMaxRetries(n)` / `WithChainMaxRetries(n)` — programmatic call, any non-negative integer including 0
2. `TRUVAG3_AI_RETRY_ATTEMPTS` env var — only positive integers honored
3. Default — `3` for single clients, `0` for chain clients

**To disable retries entirely on a single client**, use `ai.WithMaxRetries(0)` programmatically — the env var path cannot do this because the framework rule rejects `≤ 0`.

See [docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md](../docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md#ai-configuration) for the env var documentation.

### Metrics for Failover

When telemetry is initialized, the chain client should emit:

```go
// On failover
telemetry.Counter("ai.chain.failover",
    "module", telemetry.ModuleAI,
    "status", "attempted",
    "reason", "server_error")

// On complete failure
telemetry.Counter("ai.chain.exhausted",
    "module", telemetry.ModuleAI,
    "status", "exhausted",
    "reason", "rate_limit")
```

Chain entry names and from/to transitions are recorded on correlated logs and
spans, not metric labels. Entry names are application-defined and would create
unbounded time series if used as metric dimensions.

---

## AI Agent and AI Tool

### AIAgent (Active Orchestrator)

```go
type AIAgent struct {
    *core.BaseAgent               // Has discovery capability
    AI              core.AIClient // AI client for processing
}

// Can discover and coordinate other components
func (a *AIAgent) DiscoverAndOrchestrate(ctx context.Context, query string) (string, error) {
    // 1. Use AI to understand intent
    // 2. Discover available components via a.Discover()
    // 3. Use AI to plan component usage
    // 4. Execute plan
    // 5. Synthesize response
}
```

### AITool (Passive Service)

```go
type AITool struct {
    *core.BaseTool    // NO discovery capability
    aiClient core.AIClient
}

// Can only process requests, cannot discover
func (t *AITool) ProcessWithAI(ctx context.Context, input string) (string, error) {
    return t.aiClient.GenerateResponse(ctx, input, opts)
}
```

### When to Use Each

| Component | Use Case | Example |
|-----------|----------|---------|
| **AIAgent** | Orchestration requiring discovery | Research agent coordinating multiple tools |
| **AITool** | Single-purpose AI capability | Translation tool, Summarization tool |
| **Raw AIClient** | Direct API access | Custom integration |

---

## Logging and Telemetry

### Logging Guidelines

**Where to Log** (all logging uses `core.Logger`):

| Location | Log Level | What to Log |
|----------|-----------|-------------|
| `client.go` | INFO | Client creation, provider selection |
| `registry.go` | INFO/DEBUG | Provider detection, auto-selection |
| `providers/*/factory.go` | INFO | Provider initialization |
| `providers/*/client.go` | INFO/DEBUG | Request/response, errors |
| `providers/base.go` | INFO/WARN | Retries, failures |
| `chain_client.go` | INFO/WARN | Failover events |
| `ai_agent.go` | INFO | Orchestration phases |

**Structured Log Fields**:

```go
// Standard fields for AI operations
logger.InfoWithContext(ctx, "AI request initiated", map[string]interface{}{
    "operation":     "ai_request",        // Operation type
    "provider":      "openai",            // Provider name
    "model":         "gpt-4",             // Semantic model, never a deployment
    "prompt_length": len(prompt),         // Input size
    "max_tokens":    1000,                // Token limit
})

logger.InfoWithContext(ctx, "AI response received", map[string]interface{}{
    "operation":         "ai_response",
    "provider":          "openai",
    "model":             "gpt-4", // Semantic model from the request report
    "prompt_tokens":     usage.PromptTokens,
    "completion_tokens": usage.CompletionTokens,
    "duration_ms":       duration.Milliseconds(),
    "status":            "success",
})
```

Provider request logs are request-scoped and therefore use the context-aware
logger methods. AI observation helpers also attach a nonempty `request_id` from
telemetry baggage with core request context as fallback, preserving correlation
when telemetry is disabled; the logger context supplies trace correlation when
available. Every log retains a stable `operation`; error logs also use a bounded
`error_type` and a sanitized `error` value. Provider clients never log
prompt text, system prompts, generated content, serialized request/response
bodies, credentials, credential scopes, complete endpoint URLs, query values,
or route-owned deployment/publisher-model identifiers. Prompt and response
lengths and token counts are safe metadata. The optional application-controlled
`LLMCallRecorder` is a separate debug-capture facility and is not permission for
provider/common logs to record content.

### Telemetry Guidelines

**Metrics to Emit** (using `telemetry` module):

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `ai.request.duration_ms` | Histogram | module, provider, status | Latency tracking |
| `ai.request.tokens` | Counter | module, provider, token_type | Usage tracking |
| `ai.request.errors` | Counter | provider, error_type | Error rates |
| `ai.chain.failover` | Counter | module, status/reason | Failover frequency; provider/entry identities remain in logs and spans |
| `ai.provider.available` | Gauge | provider | Health monitoring |

Metric labels are deliberately bounded. Semantic model identity belongs in
logs, reports, and spans, where it remains distinguishable from the wire
deployment. Neither semantic model strings nor provider-reported wire model,
deployment, route identity, credential scope, endpoint, request ID, or tenant
identity is added as a provider metric label. This also prevents an Azure
deployment or Vertex publisher-model ID returned in `AIResponse.Model` from
silently becoming a time-series dimension.

**Implementation Pattern**:

```go
// In providers/*/client.go
func (c *Client) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
    startTime := time.Now()

    // ... execute request ...

    duration := time.Since(startTime).Milliseconds()

    // Emit metrics (if telemetry is initialized)
    telemetry.RecordAIRequest(telemetry.ModuleAI, "openai", float64(duration), "success")
    telemetry.RecordAITokens(telemetry.ModuleAI, "openai", "prompt", int64(usage.PromptTokens))
    telemetry.RecordAITokens(telemetry.ModuleAI, "openai", "completion", int64(usage.CompletionTokens))

    return result, nil
}
```

### Component Filtering

All AI module logs should use the `framework/ai` component:

```go
// In factory.go
if config.Logger != nil {
    if cal, ok := config.Logger.(core.ComponentAwareLogger); ok {
        config.Logger = cal.WithComponent("framework/ai")
    }
}
```

This enables filtering:
```bash
kubectl logs ... | jq 'select(.component == "framework/ai")'
```

---

## Configuration System

### Configuration Hierarchy

Priority order (highest to lowest):

```
1. Explicit options     → ai.WithAPIKey("sk-...")
2. Provider-specific    → OPENAI_API_KEY, ANTHROPIC_API_KEY
3. TRUVAG3 prefixed      → TRUVAG3_AI_PROVIDER
4. Defaults             → "auto" detection
```

### Configuration Options

```go
client, err := ai.NewClient(
    // Provider selection
    ai.WithProvider("openai"),           // Explicit provider
    ai.WithProviderAlias("openai.groq"), // OpenAI-compatible service

    // Credentials
    ai.WithAPIKey("sk-..."),             // API key
    ai.WithBaseURL("https://..."),       // Custom endpoint

    // Model configuration
    ai.WithModel("gpt-4"),               // Model selection
    ai.WithTemperature(0.7),             // Generation temperature
    ai.WithMaxTokens(1000),              // Token limit

    // Connection settings
    ai.WithTimeout(180 * time.Second),   // Request timeout (default, supports reasoning models)
    ai.WithMaxRetries(3),                // Retry count

    // Observability
    ai.WithLogger(logger),               // Logger instance
)
```

### Request-Capable Construction

`NewRequestClient` is the additive construction path for presence-aware
`core.AIRequest` calls and provider request policies. Every existing
`AIOption` also satisfies `ClientOption`, so provider, model, retry, logging,
and telemetry configuration is shared with `NewClient`:

```go
requestClient, err := ai.NewRequestClient(
    ai.WithProvider("anthropic"),
    ai.WithModel("default"),
    ai.WithRequestRules(core.AIProviderPatch{
        Name:    "application-anthropic-sampling-policy",
        Version: "1",
        Selector: core.AIProviderSelector{
            Provider: "anthropic",
            Surface:  "messages",
            Model:    "claude-sonnet-5-*",
        },
        Remove: []string{"/temperature", "/top_p", "/top_k"},
    }),
    ai.WithCompatibilityMode(requestpolicy.CompatibilityStrict),
)
if err != nil {
    return err
}

request := core.NewAIRequest(prompt, "planning")
request.Generation.MaxTokens = core.SetAIParameter(4000)
result, err := requestClient.Generate(ctx, request)
```

Application rules must have stable `Name` and `Version` identities. Patch
`Set` values are JSON-native, and body paths use RFC 6901 JSON Pointer syntax.
`WithRequestMiddleware` accepts constrained middleware that may edit only the
provider draft exposed through `requestpolicy.RequestEditor`. Middleware must
be concurrency-safe; it produces a reusable policy fingerprint only when it
implements `requestpolicy.StableRequestMiddleware` and explicitly declares
stable semantics.

Built-in request-capable clients also implement
`core.AIRequestFingerprinter`. The capability prepares an isolated request and
returns the same secret-free policy/route identity carried by a successful
request report, without acquiring credentials or invoking provider transport.
An unstable middleware, invalid request, unresolved route, or unavailable
fingerprint returns `stable=false`; AI-output caches must bypass rather than
reuse an incomplete namespace. The common instrumentation wrapper delegates
this capability and supplies a stable adapter namespace for faithfully
representable legacy clients. A chain fingerprint covers every ordered
failover entry. Provider-local invocation viability checks that do not change
semantic policy or route identity run after fingerprint preparation. A
deterministic failure at that later boundary may therefore return a stable
request report and permit chain failover without disabling cache identity for
the healthy entry; a dynamic resolver failure remains fingerprint-unstable.

`NewRequestClient` never silently discards integration behavior. A legacy
factory may be used only when no integration options are supplied and its
client already implements `core.AIRequestClient`. OpenAI, Azure OpenAI,
Anthropic, and Gemini have built-in request adapters, and Bedrock provides one
behind its build tag. Providers without a request adapter return
`core.ErrAIRequestFeatureUnsupported`.

### Effective Request-Report Contract

The AI module owns population of provider-effective request evidence. Every
successfully prepared request-aware invocation must expose a sanitized
`core.AIRequestReport`, including when transport or provider execution later
fails. The report describes the logical provider request after built-in
compatibility rules, application rules, middleware, and per-request patches
have run. Orchestration and other consumers must not infer sent values by
interpreting provider-local adjustment paths.

For the portable fields currently consumed by framework debugging:

- `EffectiveTemperature=Set(value)` means that exact value was present in the
  final provider request, including an explicit zero;
- `EffectiveTemperature=Omit` means the final provider request contained no
  temperature field;
- `EffectiveMaxTokens` uses the same `Set`/`Omit` semantics for the provider's
  selected token-limit field; and
- `Inherit` means the adapter cannot report the final field reliably. It is not
  a synonym for absence and must not be used merely because policy changed the
  caller's requested value.

Provider drafts identify their provider-local logical paths through the shared
request-policy projection seam; `requestpolicy.Engine` reads those paths only
after final policy validation and populates the provider-neutral report. A
provider may use different wire names or nesting, but those details remain
inside its adapter. Reports must never contain prompts, system instructions,
raw bodies, credentials, endpoints, query values, or secret adjustment values.

Provider tests must cover exact sent values, explicit zero, removal by policy,
and any model behavior that routinely changes field presence—for example,
Anthropic adaptive-thinking sampling removal. OpenAI-compatible, Azure OpenAI,
Anthropic, Gemini, Bedrock, and every future request-aware provider are subject to this
contract. A provider-parity plan must name these report fields explicitly in
its implementation and verification scope.

Provider authors can add error-capable construction without breaking the
legacy `ProviderFactory` contract:

```go
type ValidatedProviderFactory interface {
    ProviderFactory
    CreateValidated(*AIConfig) (core.AIClient, error)
}

type RequestProviderFactory interface {
    ProviderFactory
    CreateRequestClient(
        *AIConfig,
        ProviderIntegrationConfig,
    ) (core.AIRequestClient, error)
}

type ProviderRequestTimeoutFactory interface {
    ProviderFactory
    DefaultRequestTimeout() time.Duration
}
```

Factories validate and wire configuration only. They do not invoke middleware
or take ownership of application-supplied lifecycle components. The timeout
interface is optional: providers that omit it retain 180 seconds. A positive
factory value applies only to an explicitly selected standalone provider when
the application did not supply a positive `WithTimeout`. Auto-detected clients
and framework-managed chain entries retain the failover-safe 180-second
framework default; caller-owned `ClientEntry` values remain untouched.

### Enterprise Credentials, Routing, and HTTP Transport

Request-capable providers may also accept application-owned credential,
endpoint, and HTTP transport integrations:

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("anthropic"),
    ai.WithCredentialSource(credentials),
    ai.WithEndpointResolver(routes),
    ai.WithHTTPClient(httpClient),
)
```

`CredentialSource` receives only sanitized request identity plus trusted route
metadata and returns one complete authentication header. It is called after
semantic request policy for every transport attempt, allowing token rotation
without rebuilding the client. An injected credential source takes precedence
over the static provider API key. Credential values and credential scopes are
excluded from request reports, fingerprints, spans, and framework logs.

For simple dynamic headers, `WithAuthHeader(name, callback)` adapts a
concurrency-safe callback into a credential source. Applications that need to
invalidate cached credentials after early revocation should implement
`CredentialRejectionObserver`; Anthropic, Azure OpenAI, Gemini, and OpenAI notify it on
HTTP 401 and 403 before returning the original provider error. Observer
failures are diagnostic and do not replace that error. The providers do not
perform an immediate authentication retry because generation acceptance cannot
generally be proven from an auth response; ordinary provider retry and chain
failover semantics remain intact.

`EndpointResolver` runs after portable request identity and concrete semantic
model resolution, but before provider-draft construction and semantic request
policy. This is the normative order for every provider that accepts the shared
resolver, including Anthropic, Azure OpenAI, Gemini, and OpenAI. It lets a trusted route
supply a deployment or publisher-model identifier needed to construct
protected wire structure without treating that identifier as the semantic
model.

```text
snapshot and validate portable request identity
    -> resolve provider alias and concrete semantic model
    -> validate known semantic capabilities
    -> resolve deterministic endpoint route and deployment
    -> select an explicit, versioned wire profile
    -> build one provider-local policy draft
    -> apply rules, middleware, and per-request patches
    -> validate and encode immutable request semantics
    -> acquire credentials for each transport attempt
    -> send and decode
```

Basic portable-intent, model, and capability validation happens before route
resolution. Application rules, middleware, per-request patches, and final draft
validation happen after it. Consequently, a resolver can be invoked before a
later policy failure, and a route failure takes precedence when both routing
and a downstream policy stage would fail. A resolver receives no policy-edited
body or headers and must not depend on policy having executed.

AI-output caches may evaluate the resolver during fingerprint preflight and
again when a cache miss proceeds to provider execution, so resolver
implementations must be concurrency-safe, side-effect-free, and return a stable
route identity for the same semantic request. For HTTP-backed providers,
`ResolvedEndpoint.URL` is the complete HTTP endpoint. An SDK-native provider may
instead require `URL` to be nil and consume `Deployment` as an opaque SDK
destination. `RouteIdentity` must be a stable, non-secret identifier suitable
for a sanitized report and fingerprint. Resolver-owned URLs and query maps are
cloned before use. Query values, deployment names, and credential scopes are
available to transport/credential integration but are not reported. A
deployment affects an SDK input, body, or path only when an explicitly selected,
typed provider surface consumes it; generic OpenAI and Anthropic routes do not
silently reinterpret `Deployment` as their body model.

An injected `*http.Client` remains caller-owned. The provider shallow-copies
it, preserves its transport, redirect, cookie-jar, and timeout policies, and
uses `http.DefaultTransport` when its transport is nil. The framework request
timeout is a context deadline and does not overwrite the injected client's
timeout. The configured transport sees the final serialized body, route,
eligible application headers, and credential header, so mTLS and signing
transports compose normally.

Anthropic, Azure OpenAI, Gemini, and OpenAI support these HTTP integrations.
Gemini uses the GenerateContent `v1beta` profile, validates complete
`generateContent`/`streamGenerateContent?alt=sse` routes, and attaches static
or dynamic credentials as an attempt-local `x-goog-api-key` header. It never
places credentials in a URL. Every Gemini body pins protected top-level
`store=false`; provider extras and request policy cannot enable Google-side
request storage, background execution, or previous-interaction state. Exact
model capability rows determine thinking levels, token limits, and fields that
current Gemini families forbid; unknown pass-through IDs receive conservative
validation without prefix-based capability inference.

Bedrock
accepts request rules, middleware, and an SDK-destination endpoint resolver. A
Bedrock resolver supplies only the opaque Converse `modelId` through
`Deployment` and a sanitized route identity; it must return no URL, query, or
credential scope. Bedrock deliberately rejects credential sources, injected
HTTP clients, and request headers. AWS credentials, SigV4, service endpoint,
region, and transport remain owned by `aws.Config` and the AWS SDK.

### Reusable Codecs and Native SDK Drafts

Provider policy operates on a logical `requestpolicy.Draft`; it does not require
every provider to use the same transport or wire format. The draft is prepared
once per logical call, policy is applied once, and sync and stream execution are
derived from the resulting semantics.

Provider drafts keep semantic identity separate from wire identity. The
semantic model remains the input to capability lookup, policy selectors,
reports, and compatibility rules. An explicitly selected, versioned wire
profile decides whether a route-owned deployment is emitted as the protected
body model, placed only in the endpoint path, or ignored by that surface. Wire
profiles are selected by provider configuration or an exact provider alias,
never inferred from a hostname or arbitrary URL shape. Credentials remain
outside both profiles and drafts.

`ai/providerkit/openaiwire` is the public extension package for OpenAI-compatible
Chat Completions adapters. Its codec owns:

- construction and validation of the policy-editable request draft;
- encoding the finalized request body;
- decoding synchronous responses and streaming events; and
- normalization of content, finish reasons, and token-usage details.

The codec does not own endpoint selection, credentials, retries, provider
identity, logging, or telemetry. The stock OpenAI provider composes those
concerns around the codec. Application-local and third-party enterprise adapters
may reuse the codec with their own routing and authentication without registering
as, or masquerading as, the stock OpenAI provider. To preserve the module
dependency direction, `providerkit/openaiwire` imports `core` and
`ai/requestpolicy` but never the root `ai` package.

The hosted-surface extension uses the same composition rule. Azure OpenAI
selects the explicit aliases `azureopenai.v1` and
`azureopenai.classic`. Google-hosted Claude selects `anthropic.vertex` and
reuses the Anthropic Messages semantics with a provider-local typed profile:
the publisher model is route-owned and omitted from the body,
`anthropic_version` is a protected body member, and the Google access token is
attached later through `CredentialSource`. These profiles are request-aware
only; Azure OpenAI additionally disables automatic environment selection and
requires an explicit endpoint resolver.

Bedrock demonstrates the non-HTTP form of the same contract. Its logical
`bedrock.Draft` is evaluated by the shared policy engine and then translated
directly into `bedrockruntime.ConverseInput` or
`bedrockruntime.ConverseStreamInput`. It does not serialize an artificial HTTP
JSON request merely to reuse request policy. The provider and its tests are
compiled only with the `bedrock` build tag; CI has a dedicated tagged build,
race-test, lint, and vulnerability-check path.

Bedrock also follows route-before-draft identity separation. The resolved
semantic model drives policy selectors, normalized results, logs, and request
reports. A resolver-owned deployment is the protected wire `ModelId` only and
may contain a foundation model, inference profile, application inference
profile, or provisioned-model identifier accepted by Converse. Raw wire IDs and
ARNs are excluded from reports, fingerprints, logs, and spans; the stable
sanitized route identity binds the policy fingerprint and may be recorded on the
provider-local span. Without a resolver, the direct route deliberately uses the
semantic model as the wire model and never silently adds a geographic or global
inference-profile prefix. Because the implicit Sonnet 5 default is documented
for direct in-region use only in `us-east-1`, request preparation outside that
region rejects an invocation that would use that implicit default after
per-request model selection. The route-classified viability check runs after
policy/report preparation, so it does not erase a deterministic fingerprint or
poison a failover chain's successful report. An explicit framework client
model, per-request model/profile, or endpoint resolver remains
application-owned and bypasses that guard. Direct package clients declare the
same intent with `Client.SetDefaultModel` during construction. Bedrock route
errors implement the exported, AI-local `AIRequestFailureReasoner` contract and
return `AIRequestFailureReasonRoute`. The chain accepts only exported bounded
reasons and never imports a provider package or inspects an error string.
Generate and streaming attempts, recoveries, aborts, and exhaustion therefore
report `error_type=route`, `failover_reason=route`, and bounded metric
`reason=route` consistently without exposing the raw route error. Caller
cancellation and deadlines retain precedence over a wrapped route marker.
Exhaustion classification comes from the final attempted error; the joined
error is reserved for the caller. A failed final entry emits only the terminal
exhaustion event, never a misleading “trying next” failover event.

One Bedrock-local, boundary-aware, case-insensitive family classifier selects
both sampling mutation and final validation. Sonnet 5 and Opus 4.7/4.8 remove
`temperature`, `top_p`, and `top_k`; Fable 5 removes inherited incompatible
temperature and `top_k` while preserving only `temperature=1` and `top_p` in
`[0.99,1)`. Converse common sampling belongs in `inference_config`.
Case-insensitive legacy `Extra` spellings are canonicalized but remain in
`additional_model_request_fields` while policy runs, so application rules,
middleware, or per-request patches can remove them or explicitly set the
canonical common field and remove the legacy copy. Case-insensitive duplicates
fail closed before policy; final validation rejects every unremediated Fable
temperature/top-p field in the additional container. JSON-decoder
`json.Number` values receive bounded validation in common inference fields.
The complete model-specific additional document is recursively validated
during final draft validation, before the policy report can be marked stable:
empty or malformed numbers, structs, `uintptr`, non-string map keys, cycles,
and non-finite floats fail locally with a path-qualified error. Valid
`json.Number` values are converted to Smithy document numbers immediately
before SDK translation; named signed and unsigned native numeric values remain
numeric. Cycle detection distinguishes legal overlapping slice views from true
recursive cycles.
Models exposed only through a different AWS surface, including the current
Mythos Messages surface, are not classified as Converse models. Resolver
implementations must preserve semantic equivalence: the semantic model drives
policy even when the protected wire deployment is an opaque profile or ARN.

Provider factories may implement the additive
`ProviderRequestTimeoutFactory` contract when their documented operation
headroom differs from the framework's 180-second default. The root constructor
applies that factory default only to an explicitly selected standalone
provider when the application does not provide a positive `WithTimeout`;
Bedrock declares 60 minutes. Auto-detected clients and framework-managed
failover entries retain 180 seconds so a long provider default cannot stall
provider selection. Direct `bedrock.NewClient` construction also retains the
60-minute default, while caller-owned chain clients remain untouched. This
keeps provider-specific operation policy in the provider module while explicit
positive application configuration retains precedence. Zero and negative
durations mean unset; they do not request an unbounded provider call.

The Bedrock-specific embedding helper defaults to Titan Text Embeddings V2
(`amazon.titan-embed-text-v2:0`, 1024 dimensions by default). The exported
`ModelTitanEmbedV1` constant exists only as an explicit migration pin for
1536-dimensional V1 stores. A per-call V1 pin discards inherited V2-only client
defaults, while V2 dimensions or normalization explicitly supplied on that
same call are rejected before SDK invocation (zero dimensions remains an
explicit omission). Boundary-aware recognition also covers recognizable V1
variants and foundation-model ARNs. The framework never mixes or migrates
vector-store dimensions implicitly. `WithoutEmbeddingNormalization` explicitly
omits an inherited V2 normalization setting for one call. Embedding spans use
only the bounded V1 or V2 semantic family and never a route-owned model ID or
ARN.

| Built-in provider | Policy surface | Request rules/middleware | HTTP integration seams |
|-------------------|----------------|--------------------------|------------------------|
| Anthropic and `anthropic.vertex` | Messages / Vertex publisher prediction | Yes | Yes |
| Azure OpenAI v1/classic | Profiled Chat Completions | Yes | Yes; resolver required |
| OpenAI and compatible aliases | Chat Completions via `openaiwire` | Yes | Yes |
| Bedrock (`bedrock` build tag) | Converse SDK draft | Yes | SDK destination resolver only; AWS SDK owns HTTP, credentials, signing, and region |
| Gemini | GenerateContent `v1beta` | Yes | Yes |

### Common Logical Instrumentation

Clients returned by `NewClient` and `NewRequestClient` are decorated by the
common `InstrumentedAIClient`. The decorator preserves the stable
`core.AIClient` surface, exposes the additive request capability, and creates
one logical parent span for each call:

```text
ai.generate or ai.stream
    └── provider-local preparation / execution span
        └── ai.http_attempt (one per transport attempt)
```

The logical span owns normalized duration, provider/model/surface identity,
token usage, and policy adjustment paths. It does not record
prompts, system prompts, generated content, serialized provider request or
response bodies, credentials, credential scopes, complete endpoint URLs, query
values, or route-owned deployments. Provider transports continue to own
network-attempt spans. A provider-local span may record the resolved semantic
model, bounded surface, sanitized stable route identity, status code, durations,
token counts, and a bounded error classification. Errors recorded on common or
provider-local spans use sanitized messages rather than raw provider response
material.

Orchestration emits exactly one `ai.request.prepared` event on its active phase
span for every effective invocation, including provider failures that return no
sanitized report. The event always contains bounded effective identity,
stability, adjustment count, and `ai.request.reported`; provider, alias,
surface, and operation are present only when a report exists, and the
fingerprint appears only when stable. It never contains prompt, body, endpoint,
or credential data. Providers return reports when preparation succeeded but do
not emit this orchestration-owned event themselves.

Because the registered-provider constructors return the common decorator,
code that needs a concrete provider type should construct that provider
directly. Ordinary framework code should depend on `core.AIClient` or
`core.AIRequestClient`.

### Environment Variable Reference

| Variable | Provider | Description |
|----------|----------|-------------|
| `OPENAI_API_KEY` | OpenAI | API key |
| `OPENAI_BASE_URL` | OpenAI | Custom endpoint |
| `ANTHROPIC_API_KEY` | Anthropic | API key |
| `GOOGLE_API_KEY` | Gemini | Preferred API key; wins when both Gemini variables are set |
| `GEMINI_API_KEY` | Gemini | Fallback API key |
| `AWS_REGION` | Bedrock | AWS region |
| `DEEPSEEK_API_KEY` | OpenAI.DeepSeek | API key |
| `GROQ_API_KEY` | OpenAI.Groq | API key |
| `XAI_API_KEY` | OpenAI.xAI | API key |
| `TOGETHER_API_KEY` | OpenAI.Together | API key |
| `QWEN_API_KEY` | OpenAI.Qwen | API key |

---

## Integration Patterns

### Pattern 1: Direct Client Usage

```go
import (
    "github.com/truvaagents/truva-g3/ai"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
    client, err := ai.NewClient(
        ai.WithProvider("openai"),
        ai.WithModel("gpt-4"),
    )
    if err != nil {
        log.Fatal(err)
    }

    response, err := client.GenerateResponse(ctx, "Hello!", nil)
}
```

### Pattern 2: With Framework Integration

```go
import (
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/telemetry"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
    // Initialize telemetry
    telemetry.Initialize(telemetry.UseProfile(telemetry.ProfileProduction))
    defer telemetry.Shutdown(context.Background())

    // Create agent with AI
    agent, err := ai.NewAIAgent("research-agent", os.Getenv("OPENAI_API_KEY"))
    if err != nil {
        log.Fatal(err)
    }

    // Create framework
    framework, err := core.NewFramework(agent,
        core.WithLogger(logger),
        core.WithDiscovery(true, "redis"),
    )

    // Run
    framework.Run(context.Background())
}
```

### Pattern 3: Multi-Provider with Failover

```go
import (
    "github.com/truvaagents/truva-g3/ai"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
)

func main() {
    // Option A: Explicit chain order
    chain, err := ai.NewChainClient(
        ai.WithProviderChain("openai", "anthropic"),
        ai.WithChainLogger(logger),
    )

    // Option B: Auto-detect from environment (discovers all available providers)
    chain, err := ai.NewChainClient(
        ai.WithChainLogger(logger),
    )

    if err != nil {
        log.Fatal(err)
    }

    // Automatically fails over if first provider is unavailable
    response, err := chain.GenerateResponse(ctx, prompt, nil)
}
```

---

## Common Pitfalls

### Pitfall 1: Forgetting Provider Import

**Problem**:
```go
import "github.com/truvaagents/truva-g3/ai"
// Missing: _ "github.com/truvaagents/truva-g3/ai/providers/openai"

client, err := ai.NewClient(ai.WithProvider("openai"))
// Error: provider 'openai' not registered
```

**Solution**:
```go
import (
    "github.com/truvaagents/truva-g3/ai"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"  // Add blank import
)
```

### Pitfall 2: Nil Logger in Factory

**Problem**:
```go
// In factory.go (BROKEN)
func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
    var logger core.Logger  // Nil!
    return NewClient(apiKey, baseURL, logger)
}
```

**Symptom**: Silent failures, no logging from provider.

**Solution**:
```go
func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
    logger := config.Logger
    if logger != nil {
        if cal, ok := logger.(core.ComponentAwareLogger); ok {
            logger = cal.WithComponent("framework/ai")
        }
    }
    return NewClient(apiKey, baseURL, logger)
}
```

### Pitfall 3: Missing Error Handling

**Problem**:
```go
client, _ := ai.NewClient()  // Ignoring error
response, _ := client.GenerateResponse(ctx, prompt, nil)  // Panic if client is nil
```

**Solution**:
```go
client, err := ai.NewClient()
if err != nil {
    log.Fatalf("Failed to create AI client: %v", err)
}

response, err := client.GenerateResponse(ctx, prompt, nil)
if err != nil {
    // Handle error appropriately
}
```

### Pitfall 4: Using Auto-Detection in Production

**For single clients** — auto-detection picks ONE provider, which could change if env vars change:
```go
// Potentially surprising in production - provider could change unexpectedly
client, _ := ai.NewClient()  // Uses auto-detection → picks single best provider
```

**Solution for single client**:
```go
// Explicit provider selection in production
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Explicit
    ai.WithModel("gpt-4"),      // Explicit
)
```

**For chain clients** — auto-detection is safe because it builds a **failover list of ALL available providers**, which is the intended production use case:
```go
// Safe for production - builds failover chain from all available providers
chain, _ := ai.NewChainClient(ai.WithChainLogger(logger))
// All detected providers become failover targets, ordered by priority
```

Use explicit `WithProviderChain(...)` when you need deterministic ordering that doesn't change with environment.

---

## Troubleshooting Guide

### Issue: "provider not registered"

**Diagnostic**:
```go
// Check registered providers
providers := ai.ListProviders()
fmt.Printf("Registered providers: %v\n", providers)
```

**Common Causes**:
1. Missing blank import for provider package
2. Build tags excluding provider (e.g., `//go:build bedrock`)

**Solution**: Add blank import:
```go
import _ "github.com/truvaagents/truva-g3/ai/providers/openai"
```

### Issue: "no provider detected in environment"

**Diagnostic**:
```go
info := ai.GetProviderInfo()
for _, p := range info {
    fmt.Printf("%s: available=%v, priority=%d\n", p.Name, p.Available, p.Priority)
}
```

**Common Causes**:
1. API key environment variables not set
2. Using auto-detection with no configured providers

**Solution**: Set required environment variables or use explicit configuration.

### Issue: Silent Failures (No Logs)

**Diagnostic**:
```go
// Check if logger is being passed
client, _ := ai.NewClient(
    ai.WithLogger(myLogger),  // Ensure logger is passed
)
```

**Common Causes**:
1. Logger not passed to `NewClient`
2. Factory not propagating logger to client
3. A custom factory does not propagate the configured logger

**Solution**: Ensure logger propagation through factory chain.

### Issue: Chain Client Exhausts All Providers

**Diagnostic**: Check logs for failover sequence.

**Common Causes**:
1. All providers have same underlying issue (invalid API key)
2. Request is malformed (true 4xx client errors don't trigger failover; transient proxy 4xx errors with `IsTransient() == true` do)

**Solution**:
- Verify API keys for all providers in chain
- Check if error is client error (4xx) vs server error (5xx)

---

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 1.8 | 2026-08-17 | Added Gemini's request-aware GenerateContent profile, static model-capability policy, HTTP integration seams, and corrected orchestration request-evidence semantics |
| 1.7 | 2026-07-23 | Exported the bounded route-failure marker, aligned terminal generate/stream observability and success attributes, removed last-entry failover logs, and moved Bedrock additional-document rejection before stable fingerprinting |
| 1.6 | 2026-07-23 | Split Bedrock semantic fingerprint preparation from invocation viability, made Fable legacy sampling policy-remediable and fail-closed, normalized Bedrock document numbers, added bounded route failover classification, direct-client model intent, and embedding override/semantic-family controls |
| 1.5 | 2026-07-23 | Aligned Bedrock family sampling classification, implicit-region validation, failover-safe timeout scoping, and Titan V1 migration support |
| 1.4 | 2026-07-22 | Added SDK-native Bedrock destination resolution, provider-specific default timeouts, semantic/wire model separation, and bounded route observability |
| 1.3 | 2026-07-22 | Approved route-before-draft hosted-provider lifecycle, semantic/wire identity separation, and bounded provider observability contracts |
| 1.2 | 2026-07-20 | Added request fingerprint delegation, chain composition, and orchestration cache-safety guidance |
| 1.1 | 2026-07-20 | Added request-aware provider status, reusable OpenAI wire codecs, and native Bedrock policy drafts |
| 1.0 | 2025-12-14 | Initial architecture documentation |

---

## Related Documentation

- [Framework Design Principles](../FRAMEWORK_DESIGN_PRINCIPLES.md) - Overall framework architecture
- [Core Module Architecture](../core/ARCHITECTURE.md) - Core module rules
- [Telemetry Architecture](../telemetry/ARCHITECTURE.md) - Telemetry patterns

---

**Remember**: The AI module abstracts away provider complexity while maintaining the framework's architectural principles. When in doubt, favor explicit configuration over auto-detection, and always propagate loggers through the factory chain for production visibility.
