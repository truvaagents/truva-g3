# TruvaG3 AI Module

Multi-provider LLM integration with automatic detection, reusable
OpenAI-compatible adapters, and an extensible provider architecture.

## Table of Contents

### Getting Started
1. [What Does This Module Do?](#1-what-does-this-module-do)
2. [Quick Start](#2-quick-start)
3. [Three Ways to Use AI](#3-three-ways-to-use-ai)
4. [Provider Configuration](#4-provider-configuration)
5. [Common Use Cases](#5-common-use-cases)
6. [Best Practices](#6-best-practices)

### Advanced Topics
7. [Provider Aliases](#7-provider-aliases---the-clean-way)
8. [Chain Client (Failover)](#8-automatic-failover-with-chain-client)
9. [Model Aliases](#9-model-aliases---portable-model-names)
10. [Supported Providers](#10-supported-providers)
11. [How It Works](#11-how-it-works)
12. [Core Concepts](#12-core-concepts-explained)
13. [Architecture](#13-how-it-fits-in-truvag3)
14. [Advanced Features](#14-advanced-features)
15. [Streaming Support](#15-streaming-support)
16. [Migration Guide](#16-migration-guide)
17. [Distributed Tracing](#17-distributed-tracing-for-ai-operations)
18. [Related Documentation](#18-related-documentation)
19. [Summary](#19-summary)

## 1. What Does This Module Do?

Think of this module as a **translator for supported AI services**. Just like a
power adapter lets you use the same laptop with different outlets, this module
lets agents keep one client contract across built-in providers and compatible
private LLM endpoints.

It's the bridge between your agents and the world of AI, handling all the complexity so you can focus on building great features.

### Real-World Analogy: A Multi-Device Remote

Think of a remote designed for several device families: one control surface,
with a device-specific adapter behind each supported integration.

- **Without this module**: Write different code for each AI provider (OpenAI code, Anthropic code, etc.)
- **With this module**: Keep one portable client contract while each provider
  adapter owns its wire format and capabilities

```go
// Monday: Using OpenAI
client, _ := ai.NewClient(ai.WithProvider("openai"))

// Tuesday: Switch to Anthropic
client, _ := ai.NewClient(ai.WithProvider("anthropic"))

// Wednesday: Use your company's internal LLM
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Uses OpenAI-compatible interface
    ai.WithBaseURL("https://llm.company.internal/v1"),
)

// Your call site keeps the same interface; provider-specific capabilities can differ.
response, _ := client.GenerateResponse(ctx, "Hello AI!", nil)
```

### Key Concepts at a Glance

| Concept | What It Does | Example |
|---------|--------------|---------|
| **Single Client** | Direct connection to one provider | `ai.NewClient(ai.WithProviderAlias("openai"))` |
| **Chain Client** | Auto-failover across multiple providers | `ai.NewChainClient(ai.WithProviderChain("openai", "anthropic"))` |
| **Request-Aware Client** | Presence-aware parameters, policy, reports, and enterprise hooks | `ai.NewRequestClient(ai.WithProvider("openai"))` |
| **Heterogeneous Chain** | Independently configured or injected failover entries | `ai.NewChain(ai.ProviderEntry(...), ai.ClientEntry(...))` |
| **Provider Aliases** | Clean identifiers with auto-configuration | `openai.groq` auto-configures Groq endpoint and API key |
| **Model Aliases** | Portable model names | `smart` → `gpt-5.6-sol` (OpenAI), `claude-opus-5` (Anthropic) |
| **Env Overrides** | Runtime model configuration | `TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1` overrides the "smart" alias |

**Failover behavior**: Authentication errors (401) **allow failover** because each provider has its own API key. True client errors (400, malformed input) **do not failover** because the same input would fail everywhere.

> 📖 **For detailed configuration, operational scenarios, and Kubernetes deployment guides, see [AI Providers Setup Guide](../docs/building/AI_PROVIDERS_SETUP_GUIDE.md).**

### 📍 How to Read This Document

| If you want to... | Start here |
|-------------------|------------|
| Make your first API call | [Quick Start](#2-quick-start) |
| Understand the 3 usage patterns | [Three Ways to Use AI](#3-three-ways-to-use-ai) |
| Configure providers & models | [Provider Configuration](#4-provider-configuration) |
| See practical examples | [Common Use Cases](#5-common-use-cases) |
| Learn production best practices | [Best Practices](#6-best-practices) |
| Use multi-provider failover | [Chain Client](#8-automatic-failover-with-chain-client) |

## 2. Quick Start

### Installation

```go
import (
    "github.com/truvaagents/truva-g3/ai"

    // Import the providers you plan to use
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"    // OpenAI and compatible services
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic" // Anthropic Claude (optional)
    _ "github.com/truvaagents/truva-g3/ai/providers/gemini"    // Google Gemini (optional)
)
```

### The Simplest Thing That Works

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/truvaagents/truva-g3/ai"
    // Import providers you want to use (they self-register)
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"    // For OpenAI, Groq, DeepSeek, Ollama, etc.
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic" // For Claude
    _ "github.com/truvaagents/truva-g3/ai/providers/gemini"    // For Gemini
)

func main() {
    // Select the highest-priority provider configured in the environment.
    client, err := ai.NewClient()
    if err != nil {
        log.Fatal(err)
    }

    response, err := client.GenerateResponse(
        context.Background(),
        "What is the meaning of life?",
        nil,
    )
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println(response.Content)
}
```

**Behind the scenes, here's what happens:**

1. **Provider Registration**: When you import providers with `_`, their `init()` functions automatically register them with the AI module's registry
2. **Environment Scanning**: `ai.NewClient()` calls each registered provider's `DetectEnvironment()` method to check if it's available (looking for API keys, local services, etc.)
3. **Priority Selection**: Available providers return a priority score - the module picks the highest priority provider that's configured
4. **Automatic Configuration**: The selected provider configures itself with found credentials and endpoints
5. **Ready to Use**: You get a working client without specifying any configuration

For example, if you have `OPENAI_API_KEY` set, it uses OpenAI. If you have `GROQ_API_KEY` instead, it automatically configures the OpenAI provider to use Groq's endpoint. No code changes needed!

## 3. Three Ways to Use AI

### Method 1: Zero Configuration (Auto-Pilot)

Perfect for getting started - the module figures everything out:

```bash
# Configure a provider in the process environment.
export OPENAI_API_KEY=sk-...
```

```go
client, err := ai.NewClient()
if err != nil {
    return err
}
response, err := client.GenerateResponse(ctx, "Hello!", nil)
if err != nil {
    return err
}
fmt.Println(response.Content)
```

**Behind the scenes:**
1. Checks environment variables
2. Finds available API keys
3. Auto-configures the appropriate provider
4. Ready to use!

### Method 2: Explicit Provider (You Choose)

When you want a specific provider:

```go
// Use native Anthropic implementation
client, _ := ai.NewClient(
    ai.WithProvider("anthropic"),
    ai.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
    ai.WithModel("smart"),
)

// Use native Gemini implementation
client, _ := ai.NewClient(
    ai.WithProvider("gemini"),
    ai.WithAPIKey(os.Getenv("GOOGLE_API_KEY")),
    ai.WithModel("smart"),
)
```

#### AWS Bedrock Provider

AWS Bedrock provides SDK-native Converse/ConverseStream access to supported
foundation models. It requires both the `bedrock` build tag and an import of
the build-tagged provider package:

```bash
go build -tags bedrock ./...
```

```go
import (
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/ai/providers/bedrock"
)

// The named import registers the provider and exposes current constants.
client, err := ai.NewClient(
    ai.WithProvider("bedrock"),
    ai.WithRegion("us-east-1"),
    ai.WithModel(bedrock.ModelClaudeSonnet5),
    // Omit this option to use the AWS SDK default credential chain.
    // ai.WithAWSCredentials(accessKey, secretKey, sessionToken),
)
```

The generation default is the bare `anthropic.claude-sonnet-5` model. An
implicit default is accepted only in `us-east-1`. The check runs after
per-request model selection, so a deployment that always supplies a request
model can still construct the client in another region. Configure an explicit
client or request model/profile ID, or an SDK-destination resolver, before an
invocation would otherwise use the implicit default.
TruvaG3 deliberately does not add `us.`, `global.`, or another
inference-profile prefix: those choices can change routing, data residency,
IAM/SCP behavior, availability, and cost. To map a semantic model to an
inference-profile ID or ARN, use the SDK-destination resolver recipe in the
[custom provider guide](../docs/building/CUSTOM_AI_PROVIDER_GUIDE.md#aws-bedrock-sdk-native-routing).
If you construct the provider package directly, call
`bedrockClient.SetDefaultModel(modelID)` before concurrent use to declare an
explicit default; assigning the embedded `DefaultModel` field alone does not
express that routing intent.

Current Claude Sonnet 5 and Opus 4.7/4.8 requests omit modified `temperature`,
`top_p`, and `top_k`; Fable 5 preserves only its documented compatibility
ranges and rejects `top_k`. Final validation covers both Converse
`inferenceConfig` and case-insensitive copies in
`additionalModelRequestFields`, including fields introduced by legacy `Extra`
or application request policy. Unique legacy Fable `temperature`/`top_p`
spellings remain policy-editable in the additional container: a rule,
middleware, or per-request patch may remove them, or set the canonical
`inferenceConfig` value and remove the legacy copy. Case-insensitive duplicates
and any unremediated wrong-container fields fail locally after policy.
JSON-decoder `json.Number` values remain numeric in both common inference and
nested model-specific additional fields; named signed and unsigned Go numeric
values are also accepted. The complete additional document is validated before
its policy fingerprint is marked stable. Empty or malformed decoder numbers,
structs, `uintptr`, non-string map keys, cycles, and non-finite floats fail
locally with a path-qualified error rather than reaching the AWS SDK.
The provider passes `WithMaxRetries(n)` to the AWS SDK as `n+1` total attempts,
with a minimum of one. Reasoning-content stream deltas are intentionally not
included in TruvaG3's normalized text response.

An explicitly selected standalone Bedrock client defaults to a 60-minute
request timeout, as does a client created directly with
`bedrock.NewClient`. Auto-detected clients and framework-managed failover-chain
entries retain the failover-safe 180-second framework default. Explicit
positive `ai.WithTimeout` or `ai.WithChainTimeout` values win. Zero and
negative values mean unset; they do not disable request deadlines.

`GetEmbeddings` is a Bedrock-specific, single-text helper. It uses
`amazon.titan-embed-text-v2:0` and supports validated per-call model,
dimensions, and normalization options:

```go
awsConfig, err := bedrock.CreateAWSConfig(ctx, "us-east-1")
if err != nil {
    return err
}
bedrockClient := bedrock.NewClient(awsConfig, "us-east-1", logger)
vector, err := bedrockClient.GetEmbeddings(
    ctx,
    text,
    bedrock.WithEmbeddingDimensions(512),
    bedrock.WithEmbeddingNormalization(true),
)
```

Titan V2 produces 1024 dimensions by default, while Titan V1 produces 1536.
When upgrading an application with an existing V1 vector store, pin V1 and do
not supply V2-only dimensions or normalization options on that call:

```go
vector, err := bedrockClient.GetEmbeddings(
    ctx,
    text,
    bedrock.WithEmbeddingModel(bedrock.ModelTitanEmbedV1),
)
```

The per-call V1 pin automatically omits V2-only dimensions and normalization
inherited from client defaults. Explicit V2 controls on the same V1 call are
rejected. `bedrock.WithoutEmbeddingNormalization()` explicitly omits an
inherited V2 `normalize` field for one call.

Do not mix vectors from the two models in one index; migrate or rebuild the
store before changing dimensions.

`WithAWSCredentials()` installs an explicit static credential provider. Without
it, TruvaG3 calls the AWS SDK default configuration loader; the SDK—not this
module—owns credentials, SigV4, region, service endpoint, and HTTP transport.
The request-aware Bedrock provider therefore rejects credential sources,
injected HTTP clients, and headers; its resolver can select only the opaque SDK
`modelId` and a sanitized route identity.

Verify model IDs, supported APIs, and regional/profile availability against
the [AWS Claude Sonnet 5 model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-5.html),
the [AWS Claude Opus 4.7 model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-7.html),
the [AWS Claude Opus 4.8 model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-8.html),
the [Converse API reference](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html),
and the [Titan V2 request contract](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-titan-embed-text.html).

### Method 3: Multi-Provider Strategy (Advanced)

For multi-provider failover strategies, see
[Chain Client](#8-automatic-failover-with-chain-client) in Advanced Topics
below.

## 4. Provider Configuration

### Environment Variables - Set and Forget

The module automatically detects and configures based on environment:

```bash
# Native providers (each has its own implementation)
export OPENAI_API_KEY=sk-...          # OpenAI
export ANTHROPIC_API_KEY=sk-ant-...   # Anthropic Claude
export OPENROUTER_API_KEY=...         # OpenRouter
export GOOGLE_API_KEY=...             # Google Gemini (preferred when both Gemini variables are set)

# OpenAI-compatible services with provider aliases (recommended)
# Each gets its own namespace - no conflicts!
export DEEPSEEK_API_KEY=sk-...        # DeepSeek reasoning models
export GROQ_API_KEY=gsk-...           # Groq OpenAI-compatible API
export XAI_API_KEY=xai-...            # xAI Grok models
export MISTRAL_API_KEY=...            # Mistral models
export QWEN_API_KEY=...               # Qwen (Alibaba) models
export TOGETHER_API_KEY=...           # Together AI models
export OLLAMA_BASE_URL=http://localhost:11434/v1  # Local Ollama (must be set to activate)

# AWS Bedrock (requires -tags bedrock during build)
export AWS_REGION=us-east-1
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
```

### Configuration Options

All configuration options available when creating a client:

```go
client, _ := ai.NewClient(
    // Provider selection: choose a base provider OR a registered alias.
    ai.WithProviderAlias("openai.groq"),

    // Authentication
    ai.WithAPIKey("your-key"),
    ai.WithBaseURL("https://..."),       // Custom endpoint

    // Model configuration
    ai.WithModel("gpt-4"),               // Model to use
    ai.WithTemperature(0.7),             // Creativity (0.0-1.0)
    ai.WithMaxTokens(2000),              // Max response tokens

    // Connection settings
    ai.WithTimeout(60 * time.Second),    // Request timeout
    ai.WithMaxRetries(3),                // Retries on failure
)
```

#### Default Values

These are `NewClient` defaults:

| Setting | Default |
|---------|---------|
| Provider | "auto" (auto-detects) |
| Timeout | 180 seconds; explicitly selected standalone Bedrock declares 60 minutes |
| MaxRetries | 3 |
| Temperature | 0.7 |
| MaxTokens | 1000 |

`NewChainClient` and framework-managed `ProviderEntry` values use a
failover-safe 180-second entry timeout and set each entry's in-provider retry
budget to `0`; walking the chain is its retry layer.
Override deliberately with `WithChainMaxRetries` when a provider should absorb
transient failures before failover.

When no explicit retry option is supplied, a positive
`TRUVAG3_AI_RETRY_ATTEMPTS` value overrides the applicable default for both
client types. Explicit `WithMaxRetries(n)` or `WithChainMaxRetries(n)` takes
precedence. Zero, negative, and non-integer environment values are ignored; use
`WithMaxRetries(0)` explicitly to disable retries on a single client.

## 5. Common Use Cases

### Simple Q&A Bot

```go
func handleQuestion(question string) string {
    client, err := ai.NewClient()
    if err != nil {
        return "Sorry, AI is not configured."
    }

    response, err := client.GenerateResponse(
        context.Background(),
        question,
        &core.AIOptions{
            MaxTokens: 500,
            Temperature: 0.7,
        },
    )

    if err != nil {
        return "Sorry, I couldn't process that question."
    }

    return response.Content
}
```

### Document Analysis

```go
func analyzeDocument(document string) (string, error) {
    client, err := ai.NewClient(
        ai.WithProvider("anthropic"),
        ai.WithModel("smart"),
    )
    if err != nil {
        return "", err
    }

    prompt := fmt.Sprintf(`
        Analyze this document and provide:
        1. Summary (2-3 sentences)
        2. Key points
        3. Action items

        Document: %s
    `, document)

    response, err := client.GenerateResponse(
        context.Background(),
        prompt,
        &core.AIOptions{
            Temperature: 0.3,
            MaxTokens: 1000,
        },
    )
    if err != nil {
        return "", err
    }

    return response.Content, nil
}
```

## 6. Best Practices

### The Golden Rules

1. **🔑 Never hardcode API keys**
```go
// ❌ Bad
client, _ := ai.NewClient(ai.WithAPIKey("sk-proj-123..."))

// ✅ Good
client, _ := ai.NewClient(ai.WithAPIKey(os.Getenv("OPENAI_API_KEY")))
```

2. **🔄 Always handle errors**
```go
response, err := client.GenerateResponse(ctx, prompt, options)
if err != nil {
    // Classify the typed provider error instead of copying an arbitrary
    // provider body into an application log.
    var providerErr core.ProviderError
    if errors.As(err, &providerErr) {
        log.Printf("AI request failed: provider=%s status=%d",
            providerErr.Provider(), providerErr.StatusCode())
    }
    return fallbackResponse, nil
}
```

Some current provider adapters transform selected diagnostic messages before
recording them. This is a legacy framework exception pending audit, not a
general promise that returned errors, application logs, or debug records have
been sanitized. Applications remain responsible for their own logging and data
protection policy.

3. **⏱️ Set appropriate timeouts**
```go
client, _ := ai.NewClient(
    ai.WithTimeout(30 * time.Second),
    ai.WithMaxRetries(3),
)
```

4. **📊 Monitor token usage**
```go
response, _ := client.GenerateResponse(ctx, prompt, options)
log.Printf("Request used %d tokens", response.Usage.TotalTokens)
```

5. **🎯 Use appropriate temperature**
```go
// For factual queries: lower temperature
factualResponse, _ := client.GenerateResponse(ctx, prompt, &core.AIOptions{
    Temperature: 0.2,
})

// For creative tasks: higher temperature
creativeResponse, _ := client.GenerateResponse(ctx, prompt, &core.AIOptions{
    Temperature: 0.8,
})
```

---

# 🔧 Advanced Topics

> Everything below is for production systems with multi-provider needs, custom configurations, and advanced patterns. Start here after you're comfortable with basic usage.

## 7. Provider Aliases - The Clean Way

### Real-World Analogy: Email Addresses vs Phone Extensions

Think about how email addresses work: `john@company.com` clearly identifies both the person (john) and the organization (company.com). Similarly, provider aliases like `openai.deepseek` clearly identify both the API compatibility (openai) and the specific service (deepseek).

Without aliases, it's like everyone at your company sharing the same email address - chaos!

### The Problem Provider Aliases Solve

**Before (Manual Configuration - Messy):**
```go
// Setting up Mistral the old way
client, _ := ai.NewClient(
    ai.WithProvider("openai"),
    ai.WithBaseURL("https://api.mistral.ai/v1"), // Have to remember this URL
    ai.WithAPIKey(os.Getenv("MISTRAL_API_KEY")),
    ai.WithModel("mistral-large-latest"),         // Have to know model names
)

// Setting up Groq the old way
client, _ := ai.NewClient(
    ai.WithProvider("openai"),
    ai.WithBaseURL("https://api.groq.com/openai/v1"),  // Different URL to remember
    ai.WithAPIKey(os.Getenv("GROQ_API_KEY")),
    ai.WithModel("openai/gpt-oss-120b"),  // Different model names
)
```

**After (Provider Aliases - Clean):**
```go
// Mistral with alias - clean and clear!
client, _ := ai.NewClient(ai.WithProviderAlias("openai.mistral"))

// Groq with alias - just as simple!
client, _ := ai.NewClient(ai.WithProviderAlias("openai.groq"))

// xAI with alias
client, _ := ai.NewClient(ai.WithProviderAlias("openai.xai"))

// Together AI with alias
client, _ := ai.NewClient(ai.WithProviderAlias("openai.together"))
```

### What Happens Behind the Scenes?

When you use `WithProviderAlias("openai.mistral")`, the framework automatically:

1. **Picks the right API key**: Looks for the `MISTRAL_API_KEY` environment variable
2. **Sets the correct endpoint**: Uses `https://api.mistral.ai/v1`
3. **Configures defaults**: Sets up sensible timeouts and retry policies
4. **Enables model aliases**: Lets you use a catalog-backed portable name such as `smart`

It's like speed dial for your phone - instead of remembering full phone numbers, just press one button!

### Registered OpenAI-Compatible Aliases

This table covers the aliases implemented by the reusable OpenAI-compatible
provider. Native and hosted profiles such as `anthropic`, `gemini`,
`azureopenai.v1`, `azureopenai.classic`, `anthropic.vertex`, and `bedrock` have
different construction contracts and are listed in
[Built-in Profiles and Registered Aliases](#built-in-profiles-and-registered-aliases).

| Alias | What It Is | Environment Variables | Auto-Configured URL |
|-------|-----------|----------------------|-------------------|
| `"openai"` | Vanilla OpenAI | `OPENAI_API_KEY` | `https://api.openai.com/v1` |
| `"openai.openrouter"` | OpenRouter | `OPENROUTER_API_KEY`, `OPENROUTER_BASE_URL` | `https://openrouter.ai/api/v1` |
| `"openai.deepseek"` | DeepSeek | `DEEPSEEK_API_KEY`, `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` |
| `"openai.groq"` | Groq | `GROQ_API_KEY`, `GROQ_BASE_URL` | `https://api.groq.com/openai/v1` |
| `"openai.xai"` | xAI Grok | `XAI_API_KEY`, `XAI_BASE_URL` | `https://api.x.ai/v1` |
| `"openai.mistral"` | Mistral | `MISTRAL_API_KEY`, `MISTRAL_BASE_URL` | `https://api.mistral.ai/v1` |
| `"openai.qwen"` | Qwen (Alibaba) | `QWEN_API_KEY`, `QWEN_BASE_URL` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |
| `"openai.together"` | Together AI | `TOGETHER_API_KEY`, `TOGETHER_BASE_URL` | `https://api.together.ai/v1` |
| `"openai.ollama"` | Local Ollama | `OLLAMA_BASE_URL` | `http://localhost:11434/v1` |

> **Ollama note**: `openai.ollama` does not require an API key. Use the exact
> model tag installed in your Ollama instance; the built-in `default` alias is
> currently `llama3.2`.

### Flexibility: Override URLs Without Code Changes

Need to use a different endpoint (regional, proxy, or testing)? Just set an environment variable!

```bash
# Production: Use the default Groq URL
export GROQ_API_KEY=gsk-production-key

# Testing: Route through an organization-owned gateway (no code changes)
export GROQ_BASE_URL=https://ai-test.company.internal/groq
export GROQ_API_KEY=gsk-test-key

# Corporate: Route through internal proxy
export MISTRAL_BASE_URL=https://ai-proxy.company.internal/mistral/v1
export MISTRAL_API_KEY=internal-key
```

Your code stays exactly the same:
```go
// This works with any GROQ_BASE_URL you set.
client, _ := ai.NewClient(ai.WithProviderAlias("openai.groq"))
```

### Using Multiple Providers Simultaneously

**The Old Problem:** Manually configured OpenAI-compatible services can end up
sharing the wrong `OPENAI_API_KEY` or base URL.

For OpenRouter, the minimal environment is `OPENROUTER_API_KEY=...`; the
default URL is `https://openrouter.ai/api/v1`. Its built-in `default` and
`smart` aliases use `openrouter/auto`, `fast` uses
`openai/gpt-5.6-luna`, and `code` uses `openrouter/pareto-code`. Override the
aliases independently when deterministic or task-specific model selection
matters:

```bash
TRUVAG3_OPENROUTER_MODEL_DEFAULT=openai/gpt-5.6-sol
TRUVAG3_OPENROUTER_MODEL_SMART=moonshotai/kimi-k3
TRUVAG3_OPENROUTER_MODEL_FAST=google/gemini-2.5-flash
TRUVAG3_OPENROUTER_MODEL_CODE=moonshotai/kimi-k3
```

`ai.WithModel("moonshotai/kimi-k3")` sets a concrete constructor default;
`&core.AIOptions{Model: "google/gemini-2.5-flash"}` overrides it for one
legacy-style call. `ai.WithModel("fast")` instead retains the `_FAST`
environment override and falls back to the built-in fast mapping. See
[OpenRouter configuration](../docs/building/AI_PROVIDERS_SETUP_GUIDE.md#openrouter-configuration)
for complete constructor, per-call, request-aware, and precedence examples.

The adapter protects `provider.data_collection=deny` and `provider.zdr=true`
on every request. The `default`, `smart`, and `code` aliases are routers and
bypass AI-output caches; `fast` is a concrete model and can be cache-eligible
when no native fallback list is present. One unstable router entry makes its
whole failover chain cache-ineligible. No built-in `free` alias is advertised because the required
privacy-constrained live probe failed. Exact `:free` IDs remain experimental
pass-through values.

**The New Solution:** Each alias has its own namespace!

```go
// All three can coexist happily!
openaiClient, _ := ai.NewClient(ai.WithProviderAlias("openai"))
mistralClient, _ := ai.NewClient(ai.WithProviderAlias("openai.mistral"))
groqClient, _ := ai.NewClient(ai.WithProviderAlias("openai.groq"))

// Use different providers for different tasks
summary, _ := openaiClient.GenerateResponse(ctx, "Summarize this...", nil)
reasoning, _ := mistralClient.GenerateResponse(ctx, "Analyze this complex problem...", nil)
fastResponse, _ := groqClient.GenerateResponse(ctx, "Quick answer please...", nil)
```

**Environment Setup:**
```bash
# All three configured simultaneously - no conflicts!
export OPENAI_API_KEY=sk-openai-production
export MISTRAL_API_KEY=sk-mistral-key
export GROQ_API_KEY=gsk-groq-key
```

## 8. Automatic Failover with Chain Client

### Real-World Analogy: Your Phone's Emergency Contacts

When you dial 911, if one emergency service doesn't answer, the system automatically tries the next one. That's exactly what Chain Client does with AI providers!

### The Problem: Manual Failover is Tedious

**Before (Manual Failover - Repetitive Code):**
```go
// You had to write all this error handling yourself!
response, err := primaryClient.GenerateResponse(ctx, prompt, nil)
if err != nil {
    log.Warn("Primary failed, trying fallback...")
    response, err = fallbackClient.GenerateResponse(ctx, prompt, nil)
    if err != nil {
        log.Warn("Fallback failed, trying emergency...")
        response, err = emergencyClient.GenerateResponse(ctx, prompt, nil)
        if err != nil {
            return nil, fmt.Errorf("all providers failed: %w", err)
        }
    }
}
```

**After (Automatic Failover - One Line):**
```go
// Chain Client handles all the failover automatically!
client, _ := ai.NewChainClient(
    ai.WithProviderChain("openai", "openai.groq", "anthropic"),
)

// Just make the call - failover happens automatically
response, err := client.GenerateResponse(ctx, prompt, nil)
// Tries: OpenAI → Groq → Anthropic (stops at first success)
```

### How It Works

Think of Chain Client as having multiple backup generators:

1. **Primary Provider (OpenAI)**: Try this first
2. **First Backup (Groq)**: If primary fails, try this
3. **Emergency Backup (Anthropic)**: If everything else fails, try this

The chain stops at the **first successful response** - no wasted API calls!

### Provider Priority Order

When using `ai.NewClient()` without specifying a provider (auto-detection mode), providers are selected based on their priority scores:

| Priority | Provider | Alias | Detection Method |
|----------|----------|-------|------------------|
| 1000 | OpenAI | `openai` | `OPENAI_API_KEY` |
| 900 | Anthropic | `anthropic` | `ANTHROPIC_API_KEY` |
| 850 | OpenRouter | `openai.openrouter` | `OPENROUTER_API_KEY` |
| 800 | Gemini | `gemini` | `GOOGLE_API_KEY` or `GEMINI_API_KEY` |
| 700 | Groq | `openai.groq` | `GROQ_API_KEY` |
| 600 | DeepSeek | `openai.deepseek` | `DEEPSEEK_API_KEY` |
| 500 | xAI Grok | `openai.xai` | `XAI_API_KEY` |
| 450 | Mistral | `openai.mistral` | `MISTRAL_API_KEY` |
| 400 | Qwen | `openai.qwen` | `QWEN_API_KEY` |
| 300 | Together AI | `openai.together` | `TOGETHER_API_KEY` |
| 200+ | AWS Bedrock | `bedrock` | AWS credentials (+50 on AWS infra) |
| 100 | Ollama | `openai.ollama` | `OLLAMA_BASE_URL` (must be explicitly set) |

> **Note**: When using Chain Client with explicit `WithProviderChain()`, providers are tried in the **order you specify**, not by priority. Priority applies to auto-detection with `ai.NewClient()` and to `ai.NewChainClient()` when no explicit chain is specified.

### Complete Example: Building a Resilient AI System

```go
package main

import (
    "context"
    "log"
    "github.com/truvaagents/truva-g3/ai"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
)

func main() {
    // Create a resilient AI client with 3 fallback levels
    client, err := ai.NewChainClient(
        ai.WithProviderChain(
            "openai",              // Primary: application-selected default
            "openai.groq",         // Backup: independent provider
            "anthropic",           // Emergency: Different provider entirely
        ),
        // ai.WithChainLogger(logger),  // Optional: Add custom logger
    )
    if err != nil {
        log.Fatal("Failed to create chain client:", err)
    }

    // Use it just like any other client!
    response, err := client.GenerateResponse(
        context.Background(),
        "Explain quantum computing in simple terms",
        nil,
    )

    if err != nil {
        log.Fatal("All providers failed:", err)
    }

    log.Println(response.Content)
}
```

**What Happens When You Run This:**

| Scenario | What Chain Client Does |
|----------|----------------------|
| ✅ OpenAI works | Uses OpenAI and returns without trying later entries |
| ⚠️ OpenAI down | Tries Groq automatically, returns if it works |
| 🚨 OpenAI + Groq down | Tries Anthropic as last resort |
| ❌ All providers down | Returns one joined error with every annotated entry failure |

### Environment Setup for Chain Client

```bash
# Set up API keys for each provider in the chain
export OPENAI_API_KEY=sk-openai-production-key
export GROQ_API_KEY=gsk-groq-backup-key
export ANTHROPIC_API_KEY=sk-ant-emergency-key

# Optional: Route through an endpoint documented by the provider or owned by you
export GROQ_BASE_URL=https://ai-gateway.company.internal/groq
```

### Smart Failover: Error Classification

Chain Client is smart about which errors trigger failover:

- **Allows failover**: Unstructured network errors, provider HTTP timeouts
  such as 408/504, server errors (5xx), rate limits, and authentication or
  authorization errors (401/403), plus provider-specific terminal errors marked
  `IsRetryable` such as exhausted credit or a hard quota
- **Stops failover**: Bad requests and other 4xx errors that are neither
  transient nor provider-retryable, caller cancellation, and caller context
  deadlines

Billing/quota failures use the bounded
`failover_reason=provider_retryable` operator signal on generate and stream
failover or exhaustion events. This takes precedence over generic status
classification when, for example, a provider reports `insufficient_quota` as
HTTP 429; an ordinary 429 remains `rate_limit`.

**Why authentication errors trigger failover**: Each provider has its own API key. If OpenAI's key is invalid, Groq might still work with a valid key. This enables resilient multi-provider setups.

```go
// Authentication errors trigger failover to the next provider
response, err := client.GenerateResponse(ctx, prompt, nil)
// If OpenAI returns 401 → tries Groq → tries Anthropic → returns first success
```

### Explicit Chains and Missing API Keys

Explicit aliases are materialized in the order supplied. Most direct HTTP
providers do not preflight API keys during construction, so a missing key is
normally discovered when that entry is called; the resulting credential error
can fail over to the next entry:

```bash
# Only OpenAI and Anthropic configured (Mistral missing)
export OPENAI_API_KEY=sk-xxx
export ANTHROPIC_API_KEY=sk-ant-yyy
# MISTRAL_API_KEY not set
```

```go
// Construction succeeds; Mistral reports its missing credential when called.
client, _ := ai.NewChainClient(
    ai.WithProviderChain("openai", "openai.mistral", "anthropic"),
)
// If OpenAI fails: Mistral credential error → Anthropic is tried next.
```

Explicit entries never substitute a different detected alias. If you specify
`"openai"` without `OPENAI_API_KEY`, that entry does not silently become Groq;
it fails with OpenAI's missing-key error and the chain follows its configured
order. Auto-detected chains, by contrast, include only providers detected from
the environment.

### Use Cases for Chain Client

| Use Case | Primary | Backup | Emergency | Why? |
|----------|---------|--------|-----------|------|
| **Production API** | Approved primary | Independent backup | Emergency provider | Order by measured quality, latency, and reliability |
| **Cost Optimization** | Lowest measured cost | Next approved option | Quality fallback | Base ordering on current contracts and measured quality |
| **Privacy-First** | Ollama (local) | Company LLM (private) | OpenAI (public) | Keep data local when possible |
| **Global App** | Regional enterprise gateway | Secondary gateway | Different provider | Use documented or organization-owned regional routes |

### Inspecting the Chain: GetProviderInfo()

For observability and debugging, you can inspect the chain configuration:

```go
client, _ := ai.NewChainClient(
    ai.WithProviderChain("openai", "openai.groq", "anthropic"),
)

// Get information about the configured chain
info := client.GetProviderInfo()

fmt.Printf("Provider count: %d\n", info.ProviderCount)
fmt.Printf("Primary provider: %s\n", info.PrimaryProvider)
fmt.Printf("Failover providers: %v\n", info.FailoverProviders)
fmt.Printf("Failover enabled: %v\n", info.FailoverEnabled)
```

**Output:**
```
Provider count: 3
Primary provider: openai
Failover providers: [openai.groq anthropic]
Failover enabled: true
```

The `ChainProviderInfo` struct contains:

| Field | Type | Description |
|-------|------|-------------|
| `AvailableProviders` | `[]string` | All providers in the chain (in order) |
| `ProviderCount` | `int` | Number of providers configured |
| `FailoverEnabled` | `bool` | `true` if more than one provider |
| `PrimaryProvider` | `string` | First provider in the chain |
| `FailoverProviders` | `[]string` | All backup providers (primary excluded) |

This is useful for:
- **Logging** chain configuration at startup
- **Metrics dashboards** showing which providers are configured
- **Health endpoints** exposing available AI providers
- **Debugging** failover behavior in production

## 9. Model Aliases - Portable Model Names

### Real-World Analogy: T-Shirt Sizes

When you buy a t-shirt, you don't say "I want a garment measuring 22 inches across the chest" - you say "Size Medium." Similarly, instead of remembering each provider-specific model ID, use a catalog-backed name such as `smart`.

### The Problem: Every Provider Has Different Model Names

**Without Model Aliases:**
```go
// Using different providers means remembering different model names
openai, _ := ai.NewClient(
    ai.WithProviderAlias("openai"),
    ai.WithModel("gpt-5.6-sol"),  // OpenAI model selected by the built-in smart alias
)

mistral, _ := ai.NewClient(
    ai.WithProviderAlias("openai.mistral"),
    ai.WithModel("mistral-large-latest"), // Mistral's model ID
)

groq, _ := ai.NewClient(
    ai.WithProviderAlias("openai.groq"),
    ai.WithModel("openai/gpt-oss-120b"),  // Groq's name for smart model
)
```

**With Model Aliases:**
```go
// The same model alias works across these catalog-backed providers.
openai, _ := ai.NewClient(
    ai.WithProviderAlias("openai"),
    ai.WithModel("smart"),  // Automatically uses gpt-5.6-sol
)

mistral, _ := ai.NewClient(
    ai.WithProviderAlias("openai.mistral"),
    ai.WithModel("smart"),  // Resolves to mistral-large-latest
)

groq, _ := ai.NewClient(
    ai.WithProviderAlias("openai.groq"),
    ai.WithModel("smart"),  // Automatically uses openai/gpt-oss-120b
)
```

### Standard Model Aliases

| Alias | Purpose | OpenAI | Anthropic | OpenRouter | Gemini | DeepSeek | Groq | Together | xAI | Qwen |
|-------|---------|--------|-----------|------------|--------|----------|------|----------|-----|------|
| **`default`** | General use, balanced | `gpt-5.6-terra` | `claude-sonnet-5` | `openrouter/auto` | `gemini-2.5-flash` | `deepseek-chat` | `openai/gpt-oss-120b` | `deepseek-ai/DeepSeek-V4-Flash-0731` | `grok-3-beta` | `qwen-plus` |
| **`fast`** | Latency-oriented catalog choice | `gpt-5.6-luna` | `claude-haiku-4-5` | `openai/gpt-5.6-luna` | `gemini-3.5-flash-lite` | `deepseek-chat` | `llama-3.1-8b-instant` | `google/gemma-4-31B-it` | `grok-2` | `qwen-turbo` |
| **`smart`** | Reasoning-oriented catalog choice | `gpt-5.6-sol` | `claude-opus-5` | `openrouter/auto` | `gemini-3.1-pro-preview` | `deepseek-reasoner` | `openai/gpt-oss-120b` | `moonshotai/Kimi-K3` | `grok-3-beta` | `qwen-max` |
| **`premium`** | Highest-tier catalog choice | `gpt-5.6-sol` | `claude-fable-5` | _(N/A)_ | `gemini-3.1-pro-preview` | _(N/A)_ | _(N/A)_ | _(N/A)_ | _(N/A)_ | _(N/A)_ |
| **`code`** | Code generation & analysis | `gpt-5.6-sol` | `claude-opus-5` | `openrouter/pareto-code` | `gemini-3.1-pro-preview` | `deepseek-chat` | `openai/gpt-oss-120b` | `moonshotai/Kimi-K3` | `grok-3-mini-beta` | `qwen3-coder-plus` |
| **`vision`** | Image understanding | `gpt-4.1` | `claude-opus-5` | _(N/A)_ | `gemini-2.5-flash` | _(N/A)_ | _(N/A)_ | _(N/A)_ | `grok-2-vision-latest` | _(N/A)_ |

> **Note**: The `premium` alias is available for OpenAI (`gpt-5.6-sol`),
> Anthropic (`claude-fable-5`), and Gemini (`gemini-3.1-pro-preview`). Other
> built-in catalogs use `smart` for their reasoning-oriented choice.

This table documents the catalog compiled into this branch; it is not a
provider availability guarantee. Providers own model lifecycle and regional
availability. Check the current provider documentation and use the
`TRUVAG3_<PROVIDER>_MODEL_<ALIAS>` overrides when your approved model differs.

> **Provider lifecycle alert (verified July 22, 2026):** This branch still maps
> the DeepSeek aliases to `deepseek-chat` and `deepseek-reasoner`. DeepSeek will
> retire both model names after July 24, 2026 at 15:59 UTC and replaces them
> with `deepseek-v4-flash` and `deepseek-v4-pro`. An environment override alone
> is not a complete reasoning migration: the framework capability catalog does
> not yet model V4's `thinking` object or reasoning-effort contract. Update and
> unit-test the catalog, capability rows, and request translation first. See
> DeepSeek's [V4 announcement](https://api-docs.deepseek.com/news/news260424/)
> and [thinking-mode contract](https://api-docs.deepseek.com/guides/thinking_mode).
>
> The Gemini `premium` catalog entry names `gemini-3.1-pro-preview` directly.
> Google shut down its predecessor, `gemini-3-pro-preview`, on March 9, 2026;
> the catalog does not rely on Google's redirect from that retired ID. See the
> official [Gemini release notes](https://ai.google.dev/gemini-api/docs/changelog).
> Live GenerateContent validation on August 17, 2026 found that Google rejects
> `gemini-2.5-flash-lite` and `gemini-2.5-pro` for new users and names
> `gemini-3.5-flash-lite` and `gemini-3.1-pro-preview` as their replacements.
> The portable `fast`, `smart`, and `code` aliases therefore use those current
> targets, while callers with grandfathered access may still select an exact
> Gemini 2.5 ID.
> The provider's dated GenerateContent capability snapshot also covers
> `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.5-flash-lite`,
> `gemini-3.1-pro-preview`, `gemini-3.1-flash-lite`, `gemini-3-flash-preview`,
> `gemini-3.5-flash`, `gemini-3.5-flash-lite`, `gemini-3.6-flash`, and
> `gemini-3.7-flash`. Coverage is independent of the portable alias map. A
> current model is removed only when Google's same-surface schedule gives an
> authoritative shutdown less than 45 calendar days away; the snapshot and
> tests must be refreshed together.

### Environment Variable Overrides

You can override any model alias at runtime using environment variables, without changing code:

```bash
# Pattern: TRUVAG3_{PROVIDER}_MODEL_{ALIAS}=actual-model-name

# Override OpenAI aliases
export TRUVAG3_OPENAI_MODEL_DEFAULT=gpt-5.6-terra
export TRUVAG3_OPENAI_MODEL_SMART=gpt-5.6-sol

# Override Anthropic aliases
export TRUVAG3_ANTHROPIC_MODEL_SMART=claude-opus-5
export TRUVAG3_ANTHROPIC_MODEL_FAST=claude-haiku-4-5

# Override Gemini aliases
export TRUVAG3_GEMINI_MODEL_FAST=gemini-3.5-flash

# For OpenAI-compatible providers, strip the "openai." prefix
export TRUVAG3_GROQ_MODEL_DEFAULT=openai/gpt-oss-120b
export TRUVAG3_XAI_MODEL_SMART=grok-3-beta
export TRUVAG3_QWEN_MODEL_CODE=qwen3-coder-plus
export TRUVAG3_OLLAMA_MODEL_DEFAULT=llama3.2

# Do not override DeepSeek to V4 until its capability and thinking-mode
# translation has been updated and tested; see the lifecycle alert above.
```

**Resolution Priority**:
1. **Environment variable** (highest) - `TRUVAG3_OPENAI_MODEL_SMART`
2. **Hardcoded alias** - Built-in mapping in `modelAliases`
3. **Pass-through** (lowest) - Use model name as-is

> **💡 Gotcha**: The `_DEFAULT` env var is special - it overrides ALL AI calls that don't specify an explicit model, not just calls with `Model: "default"`. Use `TRUVAG3_OPENAI_MODEL_DEFAULT=gpt-5.6-luna` when an application deliberately prefers the lower-cost tier for all unspecified OpenAI calls.

This enables:
- **Per-environment configuration**: Use application-approved model choices in each environment
- **Runtime model switching**: Change models without redeploying
- **Kubernetes ConfigMap integration**: Manage models via ConfigMaps/Secrets

### Write Once, Switch Providers Anytime

```go
// Works with providers that define the "smart" alias.
func createAIClient(provider string) (core.AIClient, error) {
    return ai.NewClient(
        ai.WithProviderAlias(provider),
        ai.WithModel("smart"),
    )
}

// Switch providers just by changing the argument!
client, _ := createAIClient("openai")          // Uses o3
client, _ := createAIClient("openai.groq")     // Uses openai/gpt-oss-120b

// Your business logic never changes!
response, _ := client.GenerateResponse(ctx, "Analyze this data...", nil)
```

Model aliases are catalog-backed, not universal. Built-in OpenAI-compatible
aliases, Anthropic, and Gemini define them; Bedrock and arbitrary custom
providers do not automatically inherit those catalogs. A chain alias is safe
only when every entry resolves it.

### When to Use Model Aliases vs Explicit Names

**Use Aliases When:**
- ✅ You want portable code that works across providers
- ✅ You're building a framework or library
- ✅ You want to switch providers easily (dev → prod, testing different providers)
- ✅ You don't care about specific model versions

**Use Explicit Names When:**
- 🎯 You need a specific model for compliance/certification reasons
- 🎯 You're fine-tuning and need exact model control
- 🎯 You need features only available in specific models
- 🎯 You're comparing model performance scientifically

```go
// Alias for flexibility
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai"),
    ai.WithModel("smart"),  // Will use whatever OpenAI considers "smart"
)

// Explicit for control
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai"),
    ai.WithModel("gpt-4.1-2025-04-14"),  // Exactly this version
)
```

### Combining All Three Features

Here's how provider aliases, chain client, and model aliases work together beautifully:

```go
// Create a resilient multi-provider system with portable model names!
client, _ := ai.NewChainClient(
    ai.WithProviderChain(
        "openai",           // Primary: Use OpenAI's "smart" model (GPT-5.6 Sol)
        "openai.groq",      // Backup: Use Groq's "smart" model (openai/gpt-oss-120b)
        "anthropic",        // Emergency: Use Anthropic's "smart" model
    ),
)

// Use the same model alias, but it adapts to whatever provider succeeds!
response, _ := client.GenerateResponse(
    context.Background(),
    "Complex reasoning task...",
    &core.AIOptions{
        Model: "smart",  // Portable across all providers in the chain!
    },
)
```

## 10. Supported Providers

### Reusable OpenAI-Compatible Provider

The OpenAI adapter is reusable across registered aliases and custom endpoints
that accept TruvaG3's OpenAI chat-completions contract. Registered aliases add
provider-specific credentials, default URLs, model catalogs, and compatibility
profiles; an arbitrary endpoint must be contract-tested by the application.

#### Quick Examples

```go
// Using OpenAI (Default)
client, _ := ai.NewClient(
    ai.WithAPIKey("your-openai-key"),
)

// Using Groq through the generic OpenAI-compatible adapter
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Same provider!
    ai.WithBaseURL("https://api.groq.com/openai/v1"),
    ai.WithAPIKey("your-groq-key"),
    ai.WithModel("openai/gpt-oss-120b"),
)

// Using Local Ollama
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Same provider!
    ai.WithBaseURL("http://localhost:11434/v1"),
    ai.WithModel("llama3.2"),
)

// Your company's OpenAI-compatible deployment
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Same provider!
    ai.WithBaseURL("https://llm.company.internal/v1"),
    ai.WithAPIKey("internal-key"),
)
```

### Built-in Profiles and Registered Aliases

| Profile or alias | Surface | Configuration | Auto-detection | Build |
|---|---|---|---|---|
| `openai` | OpenAI chat completions | `OPENAI_API_KEY`, optional `OPENAI_BASE_URL` | Yes | Default |
| `anthropic` | Anthropic Messages | `ANTHROPIC_API_KEY`, optional `ANTHROPIC_BASE_URL` | Yes | Default |
| `gemini` | Gemini GenerateContent `v1beta` | `GOOGLE_API_KEY` then `GEMINI_API_KEY`, optional `GEMINI_BASE_URL` | Yes | Default |
| `azureopenai.v1` / `azureopenai.classic` | Azure OpenAI chat profiles | Request-aware endpoint resolver and credential source | No | Default |
| `anthropic.vertex` | Claude on Vertex AI | Request-aware endpoint resolver and Google credential source | No | Default |
| `bedrock` | AWS Bedrock Converse | AWS SDK configuration; optional static credentials and SDK-destination resolver | Yes | `bedrock` tag |
| `openai.groq` | OpenAI-compatible | `GROQ_API_KEY`, optional `GROQ_BASE_URL` | Yes | Default |
| `openai.deepseek` | OpenAI-compatible | `DEEPSEEK_API_KEY`, optional `DEEPSEEK_BASE_URL` | Yes | Default |
| `openai.xai` | OpenAI-compatible | `XAI_API_KEY`, optional `XAI_BASE_URL` | Yes | Default |
| `openai.mistral` | OpenAI-compatible | `MISTRAL_API_KEY`, optional `MISTRAL_BASE_URL` | Yes | Default |
| `openai.qwen` | OpenAI-compatible | `QWEN_API_KEY`, optional `QWEN_BASE_URL` | Yes | Default |
| `openai.together` | OpenAI-compatible | `TOGETHER_API_KEY`, optional `TOGETHER_BASE_URL` | Yes | Default |
| `openai.openrouter` | OpenAI-compatible with protected privacy/routing policy | `OPENROUTER_API_KEY`, optional `OPENROUTER_BASE_URL` | Yes | Default |
| `openai.ollama` | OpenAI-compatible | `OLLAMA_BASE_URL`; no key | Loopback URL must be set and `/models` must respond | Default |

Perplexity, vLLM, llama.cpp, and other endpoints are not registered aliases in
this module. Configure a generic OpenAI client with explicit
`WithBaseURL`, `WithAPIKey`, and model, then verify the endpoint accepts every
request and streaming field your application uses. Azure OpenAI and
Vertex-hosted Claude are first-class request-aware profiles, not generic
`OPENAI_BASE_URL` recipes; use the
[hosted-cloud recipes](../docs/building/CUSTOM_AI_PROVIDER_GUIDE.md#choose-a-hosted-cloud-recipe).

### Auto-Detection Priority

When you use `ai.NewClient()` without specifying a provider, the module checks for available services in this order:

1. **OpenAI** (priority: 1000) - Checks for `OPENAI_API_KEY`
2. **Anthropic** (priority: 900) - Checks for `ANTHROPIC_API_KEY` (native implementation)
3. **OpenRouter** (priority: 850) - Checks for `OPENROUTER_API_KEY`, configures endpoint and protected privacy policy automatically
4. **Gemini** (priority: 800) - Checks for `GOOGLE_API_KEY` or `GEMINI_API_KEY`; the Google variable wins when both are set
5. **Groq** (priority: 700) - Checks for `GROQ_API_KEY`, configures endpoint automatically
6. **DeepSeek** (priority: 600) - Checks for `DEEPSEEK_API_KEY`, configures endpoint automatically
7. **xAI Grok** (priority: 500) - Checks for `XAI_API_KEY`, configures endpoint automatically
8. **Mistral** (priority: 450) - Checks for `MISTRAL_API_KEY`, configures endpoint automatically
9. **Qwen** (priority: 400) - Checks for `QWEN_API_KEY`, configures endpoint automatically
10. **Together AI** (priority: 300) - Checks for `TOGETHER_API_KEY`, configures endpoint automatically
11. **AWS Bedrock** (priority: 200+) - Checks for AWS credentials, IAM roles, or profiles
   - Gets +50 priority when running on AWS infrastructure (EC2/ECS/Lambda)
12. **Ollama** (priority: 100) - Requires `OLLAMA_BASE_URL` to be explicitly set (does not auto-probe localhost)

### Environment Variable Configuration

#### Method 1: Standard OpenAI
```bash
export OPENAI_API_KEY=your-key
```

#### Method 2: Custom OpenAI-Compatible Endpoint
```bash
export OPENAI_BASE_URL=https://api.groq.com/openai/v1
export OPENAI_API_KEY=your-groq-key
```

#### Method 3: Service-Specific (Auto-Configured)
The provider automatically detects and configures these services:

```bash
# Groq OpenAI-compatible API
export GROQ_API_KEY=your-key
# Automatically uses https://api.groq.com/openai/v1

# DeepSeek OpenAI-compatible API
export DEEPSEEK_API_KEY=your-key
# Automatically uses https://api.deepseek.com

# xAI Grok
export XAI_API_KEY=your-key
# Automatically uses https://api.x.ai/v1

# Qwen (Alibaba) OpenAI-compatible API
export QWEN_API_KEY=your-key
# Automatically uses https://dashscope-intl.aliyuncs.com/compatible-mode/v1

# Anthropic Claude - Native implementation
export ANTHROPIC_API_KEY=your-key

# Google Gemini - Native implementation
export GOOGLE_API_KEY=your-auth-key
```

### Key Benefits of the Reusable Provider

1. **Zero Code Duplication**: One implementation for all OpenAI-compatible services
2. **Reusable**: New endpoints can reuse the adapter when they pass the same contract tests
3. **Flexibility**: Use cloud providers, local models, or private deployments
4. **Simple Migration**: Switch registered aliases through configuration
5. **Auto-Detection**: Automatically finds and configures available services

## 11. How It Works

### Auto-Detection

When you call `ai.NewClient()` without specifying a provider, the module checks
registered services and selects the detected option with the highest configured
priority. Similarly, `ai.NewChainClient()` without `WithProviderChain()`
auto-detects available providers and builds a failover chain ordered by
priority. See [Auto-Detection Priority](#auto-detection-priority) for details.

## 12. Core Concepts Explained

### The Provider Registry - Plugin Architecture

The registry is like a plugin system that keeps track of all available providers. Providers register themselves automatically when imported:

```go
// Import the providers you need - each registers itself via init()
import (
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"    // OpenAI and registered compatible aliases
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic" // Native Anthropic Claude
    _ "github.com/truvaagents/truva-g3/ai/providers/gemini"    // Native Google Gemini
)

// Once imported, you can list all registered providers
providers := ai.ListProviders()
// Returns: ["anthropic", "gemini", "openai"]

// Get detailed info about available providers
info := ai.GetProviderInfo()
// Returns provider names, descriptions, availability, and priority
```

These registry functions report factory names, not every alias managed by a
factory. For example, `openai.groq` and `openai.deepseek` are aliases owned by
the registered `openai` factory, so `ListProviders` still reports `openai` once.


```bash
# Native providers (each has its own implementation)
export OPENAI_API_KEY=sk-...          # OpenAI
export ANTHROPIC_API_KEY=sk-ant-...   # Anthropic Claude
export OPENROUTER_API_KEY=...         # OpenRouter
export GOOGLE_API_KEY=...             # Google Gemini; takes precedence over GEMINI_API_KEY

# OpenAI-compatible services with provider aliases (recommended)
# Each gets its own namespace - no conflicts!
export DEEPSEEK_API_KEY=sk-...        # DeepSeek reasoning models
export DEEPSEEK_BASE_URL=https://...  # Optional: Override endpoint

export GROQ_API_KEY=gsk-...           # Groq OpenAI-compatible API
export GROQ_BASE_URL=https://...      # Optional: Override endpoint

export XAI_API_KEY=xai-...            # xAI Grok models
export XAI_BASE_URL=https://...       # Optional: Override endpoint

export MISTRAL_API_KEY=...            # Mistral models
export MISTRAL_BASE_URL=https://...   # Optional: Override endpoint

export QWEN_API_KEY=...               # Qwen (Alibaba) models
export QWEN_BASE_URL=https://...      # Optional: Override endpoint

export TOGETHER_API_KEY=...           # Together AI models
export TOGETHER_BASE_URL=https://...  # Optional: Override endpoint

export OLLAMA_BASE_URL=http://localhost:11434/v1  # Local Ollama (must be set to activate detection)

# Custom OpenAI-compatible endpoint (old method - still works)
export OPENAI_BASE_URL=https://llm.company.internal/v1
export OPENAI_API_KEY=internal-key

# AWS Bedrock (requires -tags bedrock during build)
export AWS_REGION=us-east-1              # or AWS_DEFAULT_REGION
export AWS_ACCESS_KEY_ID=...             # or use IAM role/profile
export AWS_SECRET_ACCESS_KEY=...         # or use IAM role/profile
export AWS_PROFILE=...                   # Alternative: use named profile
```

**🎯 Pro Tip:** The `*_BASE_URL` environment variables let you override endpoints without code changes! Perfect for:
- **Documented provider endpoints**: use the exact URL from the provider contract
- **Corporate proxies**: `GROQ_BASE_URL=https://ai-proxy.company.internal/groq`
- **Testing gateways**: `OPENAI_BASE_URL=https://ai-test.company.internal/openai/v1`
- **Remote Ollama**: `OLLAMA_BASE_URL=http://gpu-server.local:11434/v1`

### Configuration Options - Fine Control

All configuration options available when creating a client:

```go
client, _ := ai.NewClient(
    // Provider selection (choose ONE of these methods):
    ai.WithProvider("openai"),           // Method 1: Base provider ("openai", "anthropic", "gemini", "auto")
    // OR
    ai.WithProviderAlias("openai.groq"), // Method 2: Provider alias (replaces WithProvider + WithBaseURL)

    // Authentication
    ai.WithAPIKey("your-key"),          // API key (optional with aliases - can use env vars)
    ai.WithBaseURL("https://..."),      // Custom endpoint (rarely needed with aliases)

    // Model configuration
    ai.WithModel("gpt-4"),               // Model to use (provider-specific OR use alias like "smart")
    ai.WithTemperature(0.7),            // Creativity level (0.0 = focused, 1.0 = creative)
    ai.WithMaxTokens(2000),             // Maximum tokens in response

    // Connection settings
    ai.WithTimeout(180 * time.Second),  // Explicit request-timeout override
    ai.WithMaxRetries(3),               // Number of retries on failure (default: 3)

    // Custom headers (for special requirements)
    ai.WithHeaders(map[string]string{
        "X-Custom-Header": "value",
        "anthropic-beta": "context-1m-2025-08-07",
    }),

    // Reasoning controls for supported providers/models
    ai.WithReasoningEffort("none"),

    // AWS Bedrock specific (requires -tags bedrock)
    ai.WithRegion("us-west-2"),
    ai.WithAWSCredentials(accessKey, secretKey, sessionToken),

    // Advanced configuration
    ai.WithExtra("custom_param", value), // Provider-specific extra request body fields
)
```

#### Default Configuration Values

- **Provider**: "auto" (auto-detects from environment)
- **Timeout**: 180 seconds; explicitly selected standalone Bedrock clients
  declare 60 minutes
- **MaxRetries**: 3 for `NewClient`; 0 per entry for `NewChainClient`
- **Temperature**: 0.7
- **MaxTokens**: 1000

## 13. How It Fits in TruvaG3

### The Architecture

```
┌─────────────────────────────────────────┐
│            Your Application              │
│                                          │
│  "I need AI to analyze this data"       │
└────────────────┬────────────────────────┘
                 │
    ┌────────────▼────────────┐
    │     TruvaG3 Core         │
    │                         │
    │  Tools & Agents with AI │
    └────────────┬────────────┘
                 │
    ┌────────────▼────────────┐
    │      AI Module          │ ← You are here!
    │                         │
    │  • Provider Registry    │
    │  • Portable Interface   │
    │  • Auto-detection       │
    └────────────┬────────────┘
                 │
         ┌───────┼───────┐
         │       │       │
    ┌────▼──┐ ┌──▼──┐ ┌──▼──┐
    │OpenAI │ │Anthro│ │Custom│
    │Provider│ │ pic  │ │ LLM  │
    └────────┘ └─────┘ └──────┘
```

### Module Dependencies

The AI module follows TruvaG3's architectural principles:

```
ai → core + telemetry
```

| Dependency | Purpose |
|------------|---------|
| `core` | Interfaces (`AIClient`, `Logger`), base types |
| `telemetry` | Metrics and distributed tracing for production visibility |

**Why telemetry?** The AI module makes external API calls that need observability:
- Request latency and token usage tracking
- Provider failover metrics (chain client)
- Distributed trace spans for debugging

See [ai/ARCHITECTURE.md](./ARCHITECTURE.md) for detailed architectural documentation.

### AI-Enhanced Components: Tools vs Agents

The AI module provides two types of AI-enhanced components:

#### AI Tools (Passive, Single-Purpose)

```go
// Create an AI-powered tool (passive component)
translator, err := ai.NewAITool(
    "translator",
    os.Getenv("OPENAI_API_KEY"),
)
if err != nil {
    return err
}

// Tools do ONE thing well - they don't orchestrate
translator.RegisterAICapability(
    "translate",
    "Translates text between languages",
    "You are a professional translator. Translate the following text.",
)

// The tool responds to requests but doesn't discover others
```

#### AI Agents (Active Orchestrators)

```go
// Create an AI-powered agent (active orchestrator)
orchestrator, err := ai.NewAIAgent(
    "orchestrator",
    os.Getenv("OPENAI_API_KEY"),
)
if err != nil {
    return err
}

// Agents can discover and coordinate components
tools, _ := orchestrator.Discover(ctx, core.DiscoveryFilter{
    Type: core.ComponentTypeTool,
})

// Use AI to plan and execute workflows
response, _ := orchestrator.ProcessWithAI(ctx,
    "Analyze sales data and create a report")
```

### The Power of AI Orchestration

```go
// AI Agents orchestrate multiple tools intelligently
agent, err := ai.NewAIAgent("assistant", os.Getenv("OPENAI_API_KEY"))
if err != nil {
    return err
}

// The agent discovers available tools and coordinates them
response, err := agent.DiscoverAndOrchestrate(ctx,
    "Get the latest sales data and create a summary")
```

## 14. Advanced Features

### Provider Registry Functions

The module provides several useful functions to work with providers:

```go
// List all registered providers
providers := ai.ListProviders()
// Returns: ["anthropic", "gemini", "openai"]

// Get detailed provider information
info := ai.GetProviderInfo()
for _, provider := range info {
    fmt.Printf("Provider: %s\n", provider.Name)
    fmt.Printf("  Description: %s\n", provider.Description)
    fmt.Printf("  Available: %v\n", provider.Available)
    fmt.Printf("  Priority: %d\n", provider.Priority)
}

// Check if a specific provider exists
factory, exists := ai.GetProvider("openai")
if exists {
    // Provider is available
}

// Create client with fallback on error
client, err := ai.NewClient()
if err != nil {
    // Use MustNewClient if you want to panic on error
    client = ai.MustNewClient(ai.WithProvider("openai"))
}
```

### Creating Custom Providers

The legacy `ProviderFactory` remains supported and is sufficient for clients
that implement only `core.AIClient`:

```go
// mycompany/providers/custom_llm/provider.go
package custom_llm

import (
    "os"

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
)

type CustomProvider struct{}

func (p *CustomProvider) Name() string {
    return "custom-llm"
}

func (p *CustomProvider) Description() string {
    return "Custom company LLM"
}

func (p *CustomProvider) Create(config *ai.AIConfig) core.AIClient {
    return &CustomClient{
        endpoint: config.BaseURL,
        apiKey:   config.APIKey,
        // Your implementation
    }
}

func (p *CustomProvider) DetectEnvironment() (priority int, available bool) {
    if os.Getenv("CUSTOM_LLM_KEY") != "" {
        return 200, true  // Application-chosen auto-detection priority
    }
    return 0, false
}

// Auto-register when imported
func init() {
    ai.MustRegister(&CustomProvider{})
}
```

Using your custom provider:

```go
// main.go
import _ "mycompany/providers/custom_llm"  // Auto-registers!

client, _ := ai.NewClient(ai.WithProvider("custom-llm"))
```

If a custom provider uses `providers.BaseClient.ExecuteWithRetry`, every request
with a body must be replayable, even when the retry count is zero. Constructing
the request with `http.NewRequestWithContext` and a `bytes.Reader`,
`bytes.Buffer`, or `strings.Reader` body sets `GetBody` automatically. For other
body sources, set `GetBody` explicitly so it returns a fresh `io.ReadCloser`.
`ExecuteWithRetry` rejects a non-replayable body before making a network call.

New providers should also implement `ValidatedProviderFactory` so construction
errors can be returned, and `RequestProviderFactory` when they support
presence-aware requests, policy, reports, or enterprise integrations.
Request-aware construction is currently built into direct OpenAI, Anthropic,
and Gemini, Azure OpenAI (`azureopenai.v1` and `azureopenai.classic`),
Google-hosted Claude (`anthropic.vertex`), and Bedrock with the `bedrock` build
tag. Azure and Vertex are request-aware-only and require route and credential
sources.

For the complete contracts, request-policy precedence, dynamic credentials and
routing, heterogeneous chains, OpenAI-compatible codec reuse, and SDK-native
draft pattern, see the
[Custom AI Providers and Enterprise Integration Guide](../docs/building/CUSTOM_AI_PROVIDER_GUIDE.md).

### Adding New OpenAI-Compatible Services

A new endpoint can reuse the OpenAI adapter when it accepts the framework's
chat-completions request, response, error, and streaming contracts:

```go
// Example: Using a new AI service that just launched
// No code changes needed in the module!
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Reuse the OpenAI-compatible adapter
    ai.WithBaseURL("https://new-ai-service.com/v1"),
    ai.WithAPIKey("your-api-key"),
)

// Example: Using a self-hosted model
client, _ := ai.NewClient(
    ai.WithProvider("openai"),
    ai.WithBaseURL("https://your-gpu-server.com:8080/v1"),
    ai.WithAPIKey("optional-key"),
)
```

Contract-test every field your application depends on. “OpenAI-compatible” is
not a guarantee that a service honors every parameter or streaming extension.

### Binary Size Management

Only the AWS SDK-backed Bedrock provider is gated by a build tag. The other
provider packages are available without build tags but remain opt-in: an
application must import each package it wants to register.

```bash
# OpenAI, Anthropic (including the Vertex profile), Gemini, and Azure OpenAI
# are available to ordinary builds when their packages are imported.
go build

# Make the imported AWS SDK-backed Bedrock provider available.
go build -tags bedrock
```

There are no `azure` or `vertex` build tags. Those profiles use the default
HTTP implementation and application-supplied endpoint and credential sources.
Actual binary size depends on the importing application and toolchain, so the
module does not promise fixed size figures.

### Common Provider Features

The built-in providers expose the following common configuration, with
provider-specific execution details:

#### Automatic Retry with Exponential Backoff

```go
// Configure retry behavior
client, _ := ai.NewClient(
    ai.WithMaxRetries(5),        // NewClient default: 3
    ai.WithTimeout(60 * time.Second),
)

// The module automatically retries on:
// - Network errors
// - 5xx server errors
// - Rate limiting (429)
// - Transport timeouts while the request context is still active
```

#### Request/Response Logging

Built-in providers support structured, context-aware logging:

```go
// Logs include:
// - Request details (provider, model, prompt length)
// - Response metrics (tokens used, duration)
// - Retry attempts
// - Errors with context
```

#### Default Configuration

Each provider applies sensible defaults that can be overridden:

```go
// These defaults are applied if not specified:
// - Temperature: 0.7
// - MaxTokens: 1000
// - Timeout: 180 seconds (explicit standalone Bedrock: 60 minutes)
// - MaxRetries: 3 for NewClient (NewChainClient defaults each entry to 0)
// - RetryDelay: 1 second (with exponential backoff)
```

### Provider Capabilities

Each provider implementation can offer different capabilities:

```go
// Check if a provider supports streaming
if streamer, ok := client.(core.StreamingAIClient); ok {
    _, err := streamer.StreamResponse(ctx, prompt, options, func(chunk core.StreamChunk) error {
        fmt.Print(chunk.Content)  // Real-time streaming
        return nil
    })
}

// Embeddings use a separately configured OpenAI-compatible client.
embedder, err := ai.NewEmbeddingClient(
    ai.WithEmbeddingBaseURL("http://localhost:11434/v1"),
    ai.WithEmbeddingModel("nomic-embed-text"),
)
if err == nil {
    response, err := embedder.GenerateEmbeddings(ctx, []string{text}, nil)
    _ = response
    _ = err
}
// See docs/building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md §8 for
// core.EmbeddingClient integrations with vector stores.
```

### Request-Aware Clients and Policy

Use `NewRequestClient` when a plain `AIOptions` value cannot preserve intent,
or when the provider needs request rules, middleware, dynamic credentials, or
endpoint routing:

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithModel("smart"),
)

request := core.NewAIRequest("Summarize this incident", "incident_summary")
request.Generation.Temperature = core.SetAIParameter(float32(0))
request.Generation.TopP = core.OmitAIParameter[float32]()

result, err := core.GenerateAI(ctx, client, request)
```

Gemini uses the same public request interface. Its adapter selects the
GenerateContent `v1beta` profile, sends credentials in `x-goog-api-key`, pins
protected `store=false`, and applies exact-model rules for thinking levels,
token limits, Gemini 3.x `candidateCount`, and model families that no longer
accept sampling fields. `GOOGLE_API_KEY` wins when both supported Gemini
environment variables are present. Use a current Google AI Studio auth key:
Google rejects unrestricted standard keys and has announced that all Standard
keys stop working in September 2026. See Google's
[API-key guide](https://ai.google.dev/gemini-api/docs/api-key).

The zero value of `AIParameter` means inherit, `SetAIParameter` means explicitly
send even a zero value, and `OmitAIParameter` means require absence. Core's
`GenerateAI` and `StreamAI` helpers use the request-aware capability when
available and use a legacy client only when the request can be represented
without loss. Unsupported intent returns
`core.ErrAIRequestFeatureUnsupported`.

Application policy is configured with `WithRequestRules`,
`WithRequestMiddleware`, and `WithCompatibilityMode`; per-request patches live
on `AIRequest.Patches`. `AIResult.RequestReport` contains sanitized preparation
facts, provider-effective temperature/max-token presence, and a secret-free
semantic fingerprint. For those effective fields, `Set(value)` means the value
was present in the final provider body, `Omit` means it was absent, and
`Inherit` means the adapter could not report it reliably. The report never
contains prompt text, credentials, or raw request bodies.

Use `NewChain` with `ProviderEntry` and `ClientEntry` when failover entries need
independent policy, credentials, routes, or client implementations. The legacy
`NewChainClient` remains supported for a homogeneous option set.

See the [Custom AI Providers and Enterprise Integration Guide](../docs/building/CUSTOM_AI_PROVIDER_GUIDE.md)
and [API Reference](../docs/reference/API_REFERENCE.md#request-aware-ai-api) for
the complete contracts.

## 15. Streaming Support

The built-in generation providers support streaming. Custom providers may omit
the streaming capability; callers must check the interface before use.

### Core Streaming Types

```go
// From core/interfaces.go

// StreamChunk represents a single chunk of streaming output
type StreamChunk struct {
    Content      string                 // The text content of this chunk
    Delta        bool                   // True for incremental chunks; false for the final chunk
    Index        int                    // Zero-based chunk index
    FinishReason string                 // Why generation stopped (e.g., "stop", "length")
    Model        string                 // Resolved provider model
    Usage        *TokenUsage            // Token usage (normally on the final chunk)
    Metadata     map[string]interface{} // Provider-specific metadata
}

// StreamCallback is a function that receives streaming chunks
type StreamCallback func(chunk StreamChunk) error
```

### StreamingAIClient Interface

Built-in generation providers implement the `StreamingAIClient` interface;
custom providers advertise it by implementing the interface:

```go
// StreamingAIClient extends AIClient with streaming support
type StreamingAIClient interface {
    AIClient

    // StreamResponse generates a streaming response
    StreamResponse(ctx context.Context, prompt string, options *AIOptions, callback StreamCallback) (*AIResponse, error)
    SupportsStreaming() bool
}
```

### Basic Streaming Example

```go
import (
    "context"
    "fmt"
    "log"

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
    client, err := ai.NewClient()
    if err != nil {
        log.Fatal(err)
    }

    streaming, ok := client.(core.StreamingAIClient)
    if !ok || !streaming.SupportsStreaming() {
        log.Fatal("provider does not support streaming")
    }

    // Stream response token-by-token
    response, err := streaming.StreamResponse(
        context.Background(),
        "Explain quantum computing",
        nil, // Use default options
        func(chunk core.StreamChunk) error {
            // Print each token as it arrives
            fmt.Print(chunk.Content)

            if !chunk.Delta && chunk.FinishReason != "" {
                fmt.Println("\n--- Stream complete ---")
                if chunk.Usage != nil {
                    fmt.Printf("Tokens used: %d\n", chunk.Usage.TotalTokens)
                }
            }

            return nil
        },
    )

    if err != nil {
        fmt.Printf("Streaming failed: %v\n", err)
    }
    _ = response // Contains accumulated content and final usage.
}
```

### Streaming with Chain Client (Failover)

The Chain Client supports streaming with automatic failover:

```go
// Create chain client with streaming support
client, _ := ai.NewChainClient(
    ai.WithProviderChain("openai", "openai.groq", "anthropic"),
)

// Stream with automatic failover
_, err := client.StreamResponse(ctx, prompt, options, func(chunk core.StreamChunk) error {
    fmt.Print(chunk.Content)
    return nil
})
// Failover is allowed only before the callback observes the first chunk.
// After visible output, the active entry's result/error is returned unchanged.
```

A callback error is a caller-controlled stop, not a provider outage, and does
not trigger chain failover.

### Streaming with Custom Options

```go
_, err := client.StreamResponse(
    ctx,
    "Write a short story",
    &core.AIOptions{
        Model:       "smart",           // Use model alias
        Temperature: 0.8,               // More creative
        MaxTokens:   2000,              // Longer response
    },
    func(chunk core.StreamChunk) error {
        // Handle each chunk
        if chunk.Content != "" {
            sendToUI(chunk.Content)
        }

        // Check for finish reason
        if chunk.FinishReason == "length" {
            log.Warn("Response truncated due to max_tokens")
        }

        return nil
    },
)
```

### Canceling a Stream

Use context cancellation to stop streaming mid-response:

```go
ctx, cancel := context.WithCancel(context.Background())

go func() {
    time.Sleep(5 * time.Second)
    cancel() // Stop streaming after 5 seconds
}()

_, err := client.StreamResponse(ctx, prompt, nil, callback)
if errors.Is(err, context.Canceled) {
    fmt.Println("Stream was canceled")
}
```

### Provider Streaming Support

| Provider | Streaming | Notes |
|----------|-----------|-------|
| OpenAI | ✅ Full | Native streaming support |
| Anthropic | ✅ Full | Native streaming support |
| Gemini | ✅ Full | Native streaming support |
| Bedrock | ✅ Full | Native streaming support |
| Azure OpenAI | ✅ Full | Request-aware v1/classic profiles |
| Claude on Vertex AI | ✅ Full | Request-aware `anthropic.vertex` profile |
| Groq | ✅ Full | OpenAI-compatible streaming |
| DeepSeek | ⚠️ Transport only | OpenAI-compatible streaming; built-in model catalog and V4 thinking translation require migration |
| xAI | ✅ Full | OpenAI-compatible streaming |
| Mistral | ✅ Full | OpenAI-compatible streaming |
| Qwen | ✅ Full | OpenAI-compatible streaming |
| Together AI | ✅ Full | OpenAI-compatible streaming |
| Ollama | ✅ Full | OpenAI-compatible streaming |
| Mock | ✅ Full | Simulates realistic streaming |

For a generic OpenAI-compatible endpoint, streaming support depends on its
implementation of the protocol and the selected model; contract-test it.

### Streaming Best Practices

1. **Handle returned errors**: Provider and transport failures are returned by `StreamResponse`; return a callback error to stop early
2. **Check `Delta` and `FinishReason`**: The final chunk has `Delta: false` and normally carries the finish reason and usage
3. **Use context for cancellation**: Pass a cancellable context for user-initiated stops
4. **Buffer UI updates**: Consider buffering chunks before updating UI for smoother experience
5. **Track finish reason**: Check `FinishReason` to detect truncation or stop reasons

```go
// Best practice: Complete streaming handler
func streamWithBestPractices(ctx context.Context, client core.StreamingAIClient, prompt string) error {
    var totalContent strings.Builder

    _, err := client.StreamResponse(ctx, prompt, nil, func(chunk core.StreamChunk) error {
        // 1. Accumulate content
        totalContent.WriteString(chunk.Content)

        // 2. Update UI (could buffer for smoother experience)
        updateUI(chunk.Content)

        // 3. Handle completion
        if !chunk.Delta && chunk.FinishReason != "" {
            // 4. Track finish reason
            if chunk.FinishReason == "length" {
                log.Warn("Response was truncated")
            }

            // 5. Log usage
            if chunk.Usage != nil {
                log.Info("Streaming completed", map[string]interface{}{
                    "total_tokens": chunk.Usage.TotalTokens,
                    "content_length": totalContent.Len(),
                })
            }
        }

        return nil
    })

    return err
}
```

**For a complete working example** of streaming in a production chat agent with SSE, session management, and conversation history, see the [Chat Agent Implementation Guide](../docs/memory-and-chat/CHAT_AGENT_GUIDE.md). For the dedicated conversation-history integration and compaction guide, see the [Conversation History Guide](../docs/memory-and-chat/CONVERSATION_HISTORY_GUIDE.md).

## 16. Migration Guide

### Switching Between Providers

Switching providers is as simple as changing configuration:

```go
// From OpenAI to Anthropic
// Before:
client, _ := ai.NewClient(ai.WithProvider("openai"))

// After:
client, _ := ai.NewClient(ai.WithProvider("anthropic"))
// Your code doesn't change!
```

### Moving to OpenAI-Compatible Services

```go
// From OpenAI to Groq's OpenAI-compatible API
// Before:
client, _ := ai.NewClient(
    ai.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
)

// After:
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Same provider!
    ai.WithBaseURL("https://api.groq.com/openai/v1"),
    ai.WithAPIKey(os.Getenv("GROQ_API_KEY")),
)
```

### Using Environment Variables for Easy Switching

```bash
# Development: Select Groq through its registered alias
export GROQ_API_KEY=your-groq-key

# Staging: Use OpenAI
export OPENAI_API_KEY=your-openai-key

# Production: Use your custom deployment
export OPENAI_BASE_URL=https://llm.company.com/v1
export OPENAI_API_KEY=internal-key
```

Your code stays the same:
```go
client, _ := ai.NewClient()  // Auto-detects from environment
```

## 17. Distributed Tracing for AI Operations

The AI module supports distributed tracing via OpenTelemetry. Logical
provider-neutral operations use `ai.generate` and `ai.stream`; provider
execution and HTTP attempts appear beneath them in the same trace.

### Enabling AI Telemetry

Pass a telemetry provider when creating the AI client:

```go
import (
    "context"
    "log"
    "time"

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/telemetry"
)

// Initialize telemetry FIRST (critical!) using the framework profile.
telemetryConfig := telemetry.UseProfile(telemetry.ProfileProduction)
telemetryConfig.ServiceName = "my-agent"
telemetryConfig.Endpoint = "otel-collector:4318"
if err := telemetry.Initialize(telemetryConfig); err != nil {
    log.Fatalf("telemetry initialization failed: %v", err)
}
defer func() {
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := telemetry.Shutdown(shutdownCtx); err != nil {
        log.Printf("telemetry shutdown: %v", err)
    }
}()

// Create AI client WITH telemetry provider
aiClient, err := ai.NewClient(
    ai.WithTelemetry(telemetry.GetTelemetryProvider()),
)
```

### ⚠️ Critical: Initialization Order

**Telemetry MUST be initialized BEFORE creating the AI client.** If you create the AI client first, `telemetry.GetTelemetryProvider()` returns `nil` and no AI spans will be captured.

```go
// Telemetry first, then AI client.
func main() {
    config := telemetry.UseProfile(telemetry.ProfileProduction)
    config.ServiceName = "my-service"
    config.Endpoint = "otel-collector:4318"
    if err := telemetry.Initialize(config); err != nil {
        log.Fatalf("telemetry initialization failed: %v", err)
    }
    defer func() {
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        _ = telemetry.Shutdown(shutdownCtx)
    }()

    aiClient, _ := ai.NewClient(
        ai.WithTelemetry(telemetry.GetTelemetryProvider()),
    )
    // AI spans will appear in Jaeger!
}
```

Creating the client before `telemetry.Initialize` captures a nil telemetry
provider; initializing later does not retrofit that client unless telemetry is
explicitly propagated through the framework/client setter path.

### Spans Captured

| Span Name | Description | Key Attributes |
|-----------|-------------|----------------|
| `ai.generate` / `ai.stream` | Logical normalized call | `ai.provider`, `ai.model`, `ai.surface`, `ai.purpose`, token usage, policy adjustments |
| `ai.generate_response` / `ai.stream_response` | Provider-local preparation and execution | Provider/model and provider execution attributes |
| `ai.request.prepared` (event) | Sanitized request report | Purpose, requested/resolved model, adjustment count, fingerprint stability |
| `ai.http_attempt` | Each HTTP attempt | `ai.attempt`, `ai.max_retries`, `ai.is_retry`, `ai.attempt_status`, `ai.attempt_duration_ms`, `http.status_code` |

### Viewing in Jaeger

1. Open Jaeger: `http://localhost:16686`
2. Select your service
3. Find a trace with AI operations
4. Expand `ai.generate` or `ai.stream` to see provider execution and `ai.http_attempt` spans
5. Click spans to see token counts, model info, and timing

### Complete Example

See `examples/agent-with-orchestration/` for a production-ready example with full AI telemetry integration.

## 18. Related Documentation

| Document | Description |
|----------|-------------|
| **[AI Providers Setup Guide](../docs/building/AI_PROVIDERS_SETUP_GUIDE.md)** | Comprehensive guide for configuring providers, operational scenarios, Kubernetes deployment, and troubleshooting |
| **[Custom AI Providers and Enterprise Integration](../docs/building/CUSTOM_AI_PROVIDER_GUIDE.md)** | Request-aware contracts, policy, dynamic credentials and routing, heterogeneous chains, custom factories, and codecs |
| **[ARCHITECTURE.md](./ARCHITECTURE.md)** | Technical architecture and design decisions |

## 19. Summary

### What This Module Gives You

1. **Reusable OpenAI Adapter** - One implementation backs registered compatible aliases and contract-tested custom endpoints
2. **Provider-Specific Profiles** - Native Anthropic, Gemini, Azure OpenAI, Vertex-hosted Claude, and optional AWS Bedrock behavior
3. **Auto-Detection** - Selects the highest-priority registered provider detected from the environment
4. **Provider-Neutral Call Sites** - Keep agent and orchestration logic unchanged while provider-specific construction supplies the required credentials, routes, and transport
5. **Provider Registry** - Plugin architecture for easy extension with custom providers
6. **AI Components** - Build intelligent agents that can discover and orchestrate other components
7. **Smart Configuration** - Sensible defaults with fine-grained control when needed
8. **Binary Optimization** - The SDK-heavy Bedrock provider is opt-in with the `bedrock` build tag
9. **Extensibility** - Compatible endpoints can reuse the OpenAI adapter; distinct protocols use registered factories
10. **Production Ready** - Built-in retries, timeouts, and error handling
11. **Request-Aware Extensions** - Presence-aware intent, policy reports, enterprise routing and credentials, and heterogeneous failover

### The Power of Abstraction

```go
// Your code stays the same
response, _ := client.GenerateResponse(ctx, prompt, options)

// Whether you're using:
// - OpenAI's GPT-4
// - Anthropic's Claude
// - Google's Gemini
// - Your company's private LLM
// - A local Ollama model
// - Any future AI service
```

---

You now have the main pieces of the AI module: a portable client contract,
provider-specific adapters, explicit compatibility boundaries, and extension
points for integrations that need their own identity or wire format.

Remember: Start simple with auto-detection, then customize as your needs grow. The module scales with you from prototype to production. Happy building! 🚀
