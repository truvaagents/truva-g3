# TruvaG3 AI Module

Multi-provider LLM integration with automatic detection, universal compatibility, and extensible architecture.

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

Think of this module as your **universal translator for AI services**. Just like how a power adapter lets you plug your laptop into outlets worldwide, this module lets your agents talk to any AI service - OpenAI, Anthropic, Google, or even your company's private LLM.

It's the bridge between your agents and the world of AI, handling all the complexity so you can focus on building great features.

### Real-World Analogy: The Universal Remote

Remember universal TV remotes? One remote controls any TV brand. That's exactly what this module does for AI:

- **Without this module**: Write different code for each AI provider (OpenAI code, Anthropic code, etc.)
- **With this module**: Write once, use ANY provider with a single configuration change

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

// Your code doesn't change! Same interface, different providers
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
| **Model Aliases** | Portable model names | `smart` → `o3` (OpenAI), `claude-sonnet-4-5` (Anthropic) |
| **Env Overrides** | Runtime model configuration | `TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1` overrides the "smart" alias |

**Failover behavior**: Authentication errors (401) **allow failover** because each provider has its own API key. True client errors (400, malformed input) **do not failover** because the same input would fail everywhere.

> 📖 **For detailed configuration, operational scenarios, and Kubernetes deployment guides, see [AI Providers Setup Guide](../docs/building/AI_PROVIDERS_SETUP_GUIDE.md).**

### 📍 How to Read This Document

| If you want to... | Start here |
|-------------------|------------|
| Make your first API call | [Quick Start](#-quick-start) |
| Understand the 3 usage patterns | [Three Ways to Use AI](#-three-ways-to-use-ai) |
| Configure providers & models | [Provider Configuration](#-provider-configuration) |
| See practical examples | [Common Use Cases](#-common-use-cases) |
| Learn production best practices | [Best Practices](#-best-practices) |
| Use multi-provider failover | [Advanced Topics](#-advanced-topics) (Chain Client, Provider Aliases) |

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
import (
    "github.com/truvaagents/truva-g3/ai"
    // Import providers you want to use (they self-register)
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"    // For OpenAI, Groq, DeepSeek, Ollama, etc.
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic" // For Claude
    _ "github.com/truvaagents/truva-g3/ai/providers/gemini"    // For Gemini
)

// Zero configuration - just works!
client, _ := ai.NewClient()

// Ask a question
response, _ := client.GenerateResponse(
    context.Background(),
    "What is the meaning of life?",
    nil,
)

fmt.Println(response.Content)
// Output: "The meaning of life is a philosophical question..."
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

```go
// Just set an environment variable
export OPENAI_API_KEY=sk-...

// In your code - that's it!
client, _ := ai.NewClient()
response, _ := client.GenerateResponse(ctx, "Hello!", nil)
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
    ai.WithAPIKey("sk-ant-..."),
    ai.WithModel("claude-3-sonnet-20240229"),
)

// Use native Gemini implementation
client, _ := ai.NewClient(
    ai.WithProvider("gemini"),
    ai.WithAPIKey("..."),
    ai.WithModel("gemini-1.5-pro"),
)
```

#### AWS Bedrock Provider

AWS Bedrock provides unified access to multiple foundation models including Claude, Llama, Titan, and more. It requires the `bedrock` build tag:

```bash
# Build with Bedrock support
go build -tags bedrock
```

**Configuration Methods:**

```go
// Method 1: Use AWS environment variables or IAM role
client, _ := ai.NewClient(
    ai.WithProvider("bedrock"),
    ai.WithRegion("us-east-1"),
)

// Method 2: Explicit credentials
client, _ := ai.NewClient(
    ai.WithProvider("bedrock"),
    ai.WithRegion("us-west-2"),
    ai.WithAWSCredentials(accessKey, secretKey, sessionToken),
)

// Method 3: Specify a model
client, _ := ai.NewClient(
    ai.WithProvider("bedrock"),
    ai.WithModel("anthropic.claude-3-sonnet-20240229-v1:0"),
)
```

**Supported Models in Bedrock:**
- Anthropic Claude (Opus, Sonnet, Haiku, Instant)
- Meta Llama 2 & 3 (8B, 13B, 70B variants)
- Amazon Titan (Text and Embeddings)
- Mistral and Mixtral models
- Cohere Command models

**Authentication Priority:**
1. Explicit credentials via `WithAWSCredentials()`
2. Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`)
3. AWS Profile (`AWS_PROFILE`)
4. IAM role (when running on EC2/ECS/Lambda)
5. Credentials file (`~/.aws/credentials`)

### Method 3: Multi-Provider Strategy (Advanced)

For multi-provider failover strategies, see [Chain Client](#-automatic-failover-with-chain-client) in Advanced Topics below.

## 4. Provider Configuration

### Environment Variables - Set and Forget

The module automatically detects and configures based on environment:

```bash
# Native providers (each has its own implementation)
export OPENAI_API_KEY=sk-...          # OpenAI
export ANTHROPIC_API_KEY=sk-ant-...   # Anthropic Claude
export GEMINI_API_KEY=...             # Google Gemini

# OpenAI-compatible services with provider aliases (recommended)
# Each gets its own namespace - no conflicts!
export DEEPSEEK_API_KEY=sk-...        # DeepSeek reasoning models
export GROQ_API_KEY=gsk-...           # Groq ultra-fast inference
export XAI_API_KEY=xai-...            # xAI Grok models
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
    // Provider selection
    ai.WithProvider("openai"),           // Base provider
    ai.WithProviderAlias("openai.groq"), // Or use provider alias

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

| Setting | Default |
|---------|---------|
| Provider | "auto" (auto-detects) |
| Timeout | 30 seconds |
| MaxRetries | 3 |
| Temperature | 0.7 |
| MaxTokens | 1000 |

## 5. Common Use Cases

### Simple Q&A Bot

```go
func handleQuestion(question string) string {
    client, _ := ai.NewClient()

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
    client, _ := ai.NewClient(
        ai.WithProvider("anthropic"),
        ai.WithModel("claude-3-sonnet-20240229"),
    )

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

    return response.Content, err
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
    log.Printf("AI request failed: %v", err)
    return fallbackResponse, nil
}
```

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
// Setting up DeepSeek the old way
client, _ := ai.NewClient(
    ai.WithProvider("openai"),
    ai.WithBaseURL("https://api.deepseek.com"),  // Have to remember this URL
    ai.WithAPIKey(os.Getenv("DEEPSEEK_API_KEY")),
    ai.WithModel("deepseek-reasoner"),  // Have to know model names
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
// DeepSeek with alias - clean and clear!
client, _ := ai.NewClient(ai.WithProviderAlias("openai.deepseek"))

// Groq with alias - just as simple!
client, _ := ai.NewClient(ai.WithProviderAlias("openai.groq"))

// xAI with alias
client, _ := ai.NewClient(ai.WithProviderAlias("openai.xai"))

// Together AI with alias
client, _ := ai.NewClient(ai.WithProviderAlias("openai.together"))
```

### What Happens Behind the Scenes?

When you use `WithProviderAlias("openai.deepseek")`, the framework automatically:

1. **Picks the right API key**: Looks for `DEEPSEEK_API_KEY` environment variable
2. **Sets the correct endpoint**: Uses `https://api.deepseek.com` (no need to remember!)
3. **Configures defaults**: Sets up sensible timeouts and retry policies
4. **Enables model aliases**: So you can use "smart" instead of "deepseek-reasoner"

It's like speed dial for your phone - instead of remembering full phone numbers, just press one button!

### Supported Provider Aliases

| Alias | What It Is | Environment Variables | Auto-Configured URL |
|-------|-----------|----------------------|-------------------|
| `"openai"` | Vanilla OpenAI | `OPENAI_API_KEY` | `https://api.openai.com/v1` |
| `"openai.deepseek"` | DeepSeek (reasoning) | `DEEPSEEK_API_KEY`, `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` |
| `"openai.groq"` | Groq (ultra-fast) | `GROQ_API_KEY`, `GROQ_BASE_URL` | `https://api.groq.com/openai/v1` |
| `"openai.xai"` | xAI Grok | `XAI_API_KEY`, `XAI_BASE_URL` | `https://api.x.ai/v1` |
| `"openai.mistral"` | Mistral | `MISTRAL_API_KEY`, `MISTRAL_BASE_URL` | `https://api.mistral.ai/v1` |
| `"openai.qwen"` | Qwen (Alibaba) | `QWEN_API_KEY`, `QWEN_BASE_URL` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |
| `"openai.together"` | Together AI | `TOGETHER_API_KEY`, `TOGETHER_BASE_URL` | `https://api.together.xyz/v1` |
| `"openai.ollama"` | Local Ollama | `OLLAMA_BASE_URL` | `http://localhost:11434/v1` |

> **Ollama note**: `openai.ollama` does not require an API key. Use the exact model tag you run locally, for example `TRUVAG3_OLLAMA_MODEL_DEFAULT=gemma4:26b` if you use `ollama run gemma4:26b`.

### Flexibility: Override URLs Without Code Changes

Need to use a different endpoint (regional, proxy, or testing)? Just set an environment variable!

```bash
# Production: Use default DeepSeek URL
export DEEPSEEK_API_KEY=sk-production-key

# Testing: Override to use EU endpoint (no code changes!)
export DEEPSEEK_BASE_URL=https://eu.api.deepseek.com
export DEEPSEEK_API_KEY=sk-test-key

# Corporate: Route through internal proxy
export GROQ_BASE_URL=https://ai-proxy.company.internal/groq
export GROQ_API_KEY=internal-key
```

Your code stays exactly the same:
```go
// This works with any DEEPSEEK_BASE_URL you set!
client, _ := ai.NewClient(ai.WithProviderAlias("openai.deepseek"))
```

### Using Multiple Providers Simultaneously

**The Old Problem:** You couldn't use OpenAI and DeepSeek at the same time because they both fought over `OPENAI_API_KEY`.

**The New Solution:** Each alias has its own namespace!

```go
// All three can coexist happily!
openaiClient, _ := ai.NewClient(ai.WithProviderAlias("openai"))
deepseekClient, _ := ai.NewClient(ai.WithProviderAlias("openai.deepseek"))
groqClient, _ := ai.NewClient(ai.WithProviderAlias("openai.groq"))

// Use different providers for different tasks
summary, _ := openaiClient.GenerateResponse(ctx, "Summarize this...", nil)
reasoning, _ := deepseekClient.GenerateResponse(ctx, "Analyze this complex problem...", nil)
fastResponse, _ := groqClient.GenerateResponse(ctx, "Quick answer please...", nil)
```

**Environment Setup:**
```bash
# All three configured simultaneously - no conflicts!
export OPENAI_API_KEY=sk-openai-production
export DEEPSEEK_API_KEY=sk-deepseek-key
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
    ai.WithProviderChain("openai", "openai.deepseek", "openai.groq"),
)

// Just make the call - failover happens automatically
response, err := client.GenerateResponse(ctx, prompt, nil)
// Tries: OpenAI → DeepSeek → Groq (stops at first success)
```

### How It Works

Think of Chain Client as having multiple backup generators:

1. **Primary Provider (OpenAI)**: Try this first
2. **First Backup (DeepSeek)**: If primary fails, try this
3. **Emergency Backup (Groq)**: If everything else fails, try this

The chain stops at the **first successful response** - no wasted API calls!

### Provider Priority Order

When using `ai.NewClient()` without specifying a provider (auto-detection mode), providers are selected based on their priority scores:

| Priority | Provider | Alias | Detection Method |
|----------|----------|-------|------------------|
| 1000 | OpenAI | `openai` | `OPENAI_API_KEY` |
| 900 | Anthropic | `anthropic` | `ANTHROPIC_API_KEY` |
| 800 | Gemini | `gemini` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` |
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
            "openai",              // Primary: Best quality
            "openai.deepseek",     // Backup: Good reasoning
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
| ✅ OpenAI works | Uses OpenAI, returns immediately (fastest) |
| ⚠️ OpenAI down | Tries DeepSeek automatically, returns if it works |
| 🚨 OpenAI + DeepSeek down | Tries Anthropic as last resort |
| ❌ All providers down | Returns error with details from last attempt |

### Environment Setup for Chain Client

```bash
# Set up API keys for each provider in the chain
export OPENAI_API_KEY=sk-openai-production-key
export DEEPSEEK_API_KEY=sk-deepseek-backup-key
export ANTHROPIC_API_KEY=sk-ant-emergency-key

# Optional: Override endpoints if needed
export DEEPSEEK_BASE_URL=https://eu.api.deepseek.com
```

### Smart Failover: Error Classification

Chain Client is smart about which errors trigger failover:

- **Retryable (tries next provider)**: Network errors, timeouts, server errors (5xx), rate limits, **authentication errors (401)**
- **Non-retryable (fails immediately)**: Bad request (400), content policy violations, malformed requests

**Why authentication errors trigger failover**: Each provider has its own API key. If OpenAI's key is invalid, DeepSeek might still work with a valid key. This enables resilient multi-provider setups.

```go
// Authentication errors trigger failover to the next provider
response, err := client.GenerateResponse(ctx, prompt, nil)
// If OpenAI returns 401 → tries DeepSeek → tries Groq → returns first success
```

### Partial Chain: Some Providers Missing API Keys

Chain Client is forgiving - if some providers aren't configured, it skips them gracefully:

```bash
# Only OpenAI and Groq configured (DeepSeek missing)
export OPENAI_API_KEY=sk-xxx
export GROQ_API_KEY=gsk-yyy
# DEEPSEEK_API_KEY not set
```

```go
// This still works! DeepSeek is skipped with a warning
client, _ := ai.NewChainClient(
    ai.WithProviderChain("openai", "openai.deepseek", "openai.groq"),
)
// Logs: "Provider not available (will skip in chain): openai.deepseek"
// Effective chain: OpenAI → Groq
```

> **💡 Gotcha**: Explicit providers in a chain don't auto-detect alternatives. If you specify `"openai"` but `OPENAI_API_KEY` is not set, it skips cleanly with `api_key_missing` - it won't secretly use Groq even if `GROQ_API_KEY` is available. This prevents credential-model mismatches.

### Use Cases for Chain Client

| Use Case | Primary | Backup | Emergency | Why? |
|----------|---------|--------|-----------|------|
| **Production API** | OpenAI (quality) | DeepSeek (reasoning) | Groq (speed) | Best quality first, fast fallback |
| **Cost Optimization** | Groq (free tier) | DeepSeek (cheap) | OpenAI (expensive) | Use cheap first, OpenAI only if needed |
| **Privacy-First** | Ollama (local) | Company LLM (private) | OpenAI (public) | Keep data local when possible |
| **Global App** | Regional OpenAI | US OpenAI | Anthropic | Use nearest region, fallback to others |

### Inspecting the Chain: GetProviderInfo()

For observability and debugging, you can inspect the chain configuration:

```go
client, _ := ai.NewChainClient(
    ai.WithProviderChain("openai", "openai.deepseek", "anthropic"),
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
Failover providers: [openai.deepseek anthropic]
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

When you buy a t-shirt, you don't say "I want a garment measuring 22 inches across the chest" - you say "Size Medium." Similarly, instead of remembering "openai/gpt-oss-120b" or "deepseek-reasoner," just say "smart"!

### The Problem: Every Provider Has Different Model Names

**Without Model Aliases:**
```go
// Using different providers means remembering different model names
openai, _ := ai.NewClient(
    ai.WithProviderAlias("openai"),
    ai.WithModel("o3"),  // OpenAI's name for smart model
)

deepseek, _ := ai.NewClient(
    ai.WithProviderAlias("openai.deepseek"),
    ai.WithModel("deepseek-reasoner"),  // DeepSeek's name for smart model
)

groq, _ := ai.NewClient(
    ai.WithProviderAlias("openai.groq"),
    ai.WithModel("openai/gpt-oss-120b"),  // Groq's name for smart model
)
```

**With Model Aliases:**
```go
// Same model alias works across all providers!
openai, _ := ai.NewClient(
    ai.WithProviderAlias("openai"),
    ai.WithModel("smart"),  // Automatically uses o3
)

deepseek, _ := ai.NewClient(
    ai.WithProviderAlias("openai.deepseek"),
    ai.WithModel("smart"),  // Automatically uses deepseek-reasoner
)

groq, _ := ai.NewClient(
    ai.WithProviderAlias("openai.groq"),
    ai.WithModel("smart"),  // Automatically uses openai/gpt-oss-120b
)
```

### Standard Model Aliases

| Alias | Purpose | OpenAI | Anthropic | Gemini | DeepSeek | Groq | xAI | Qwen |
|-------|---------|--------|-----------|--------|----------|------|-----|------|
| **`default`** | General use, balanced | `gpt-4.1-mini` | `claude-sonnet-4-5` | `gemini-2.5-flash` | `deepseek-chat` | `openai/gpt-oss-120b` | `grok-3-beta` | `qwen-plus` |
| **`fast`** | Quick responses, lower cost | `gpt-4.1-mini` | `claude-haiku-4-5` | `gemini-2.5-flash-lite` | `deepseek-chat` | `llama-3.1-8b-instant` | `grok-2` | `qwen-turbo` |
| **`smart`** | Best reasoning, higher quality | `o3` | `claude-sonnet-4-5` | `gemini-2.5-pro` | `deepseek-reasoner` | `openai/gpt-oss-120b` | `grok-3-beta` | `qwen-max` |
| **`premium`** | Maximum intelligence | _(N/A)_ | `claude-opus-4-5` | `gemini-3-pro-preview` | _(N/A)_ | _(N/A)_ | _(N/A)_ | _(N/A)_ |
| **`code`** | Code generation & analysis | `o3` | `claude-sonnet-4-5` | `gemini-2.5-pro` | `deepseek-chat` | `openai/gpt-oss-120b` | `grok-3-mini-beta` | `qwen3-coder-plus` |
| **`vision`** | Image understanding | `gpt-4.1` | `claude-sonnet-4-5` | `gemini-2.5-flash` | _(N/A)_ | _(N/A)_ | `grok-2-vision-latest` | _(N/A)_ |

> **Note**: The `premium` alias is only available for Anthropic (claude-opus-4-5) and Gemini (gemini-3-pro-preview). For OpenAI and other providers, use `smart` for best reasoning quality. Model names shown are abbreviated; actual IDs include version dates (e.g., `claude-sonnet-4-5-20250929`).

### Environment Variable Overrides

You can override any model alias at runtime using environment variables, without changing code:

```bash
# Pattern: TRUVAG3_{PROVIDER}_MODEL_{ALIAS}=actual-model-name

# Override OpenAI aliases
export TRUVAG3_OPENAI_MODEL_DEFAULT=gpt-4.1-mini
export TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1

# Override Anthropic aliases
export TRUVAG3_ANTHROPIC_MODEL_SMART=claude-opus-4-5-20251101
export TRUVAG3_ANTHROPIC_MODEL_FAST=claude-haiku-4-5-20251001

# Override Gemini aliases
export TRUVAG3_GEMINI_MODEL_FAST=gemini-2.0-flash

# For OpenAI-compatible providers, strip the "openai." prefix
export TRUVAG3_DEEPSEEK_MODEL_SMART=deepseek-reasoner
export TRUVAG3_GROQ_MODEL_DEFAULT=openai/gpt-oss-120b
export TRUVAG3_XAI_MODEL_SMART=grok-3-beta
export TRUVAG3_QWEN_MODEL_CODE=qwen3-coder-plus
export TRUVAG3_OLLAMA_MODEL_DEFAULT=gemma4:26b
```

**Resolution Priority**:
1. **Environment variable** (highest) - `TRUVAG3_OPENAI_MODEL_SMART`
2. **Hardcoded alias** - Built-in mapping in `modelAliases`
3. **Pass-through** (lowest) - Use model name as-is

> **💡 Gotcha**: The `_DEFAULT` env var is special - it overrides ALL AI calls that don't specify an explicit model, not just calls with `Model: "default"`. Use `TRUVAG3_OPENAI_MODEL_DEFAULT=gpt-4.1-mini` to control costs across your entire application.

This enables:
- **Per-environment configuration**: Use cheaper models in dev, premium models in prod
- **Runtime model switching**: Change models without redeploying
- **Kubernetes ConfigMap integration**: Manage models via ConfigMaps/Secrets

### Write Once, Switch Providers Anytime

```go
// Configuration function that works with ANY provider
func createAIClient(provider string) (core.AIClient, error) {
    return ai.NewClient(
        ai.WithProviderAlias(provider),
        ai.WithModel("smart"),  // Portable! Works with all providers
    )
}

// Switch providers just by changing the argument!
client, _ := createAIClient("openai")          // Uses o3
client, _ := createAIClient("openai.deepseek") // Uses deepseek-reasoner
client, _ := createAIClient("openai.groq")     // Uses openai/gpt-oss-120b

// Your business logic never changes!
response, _ := client.GenerateResponse(ctx, "Analyze this data...", nil)
```

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
        "openai",           // Primary: Use OpenAI's "smart" model (o3)
        "openai.deepseek",  // Backup: Use DeepSeek's "smart" model (deepseek-reasoner)
        "openai.groq",      // Emergency: Use Groq's "smart" model (openai/gpt-oss-120b)
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

### Universal OpenAI-Compatible Provider

The TruvaG3 AI module features a **universal OpenAI-compatible provider** that works with 20+ services using a single implementation. This means one provider implementation handles OpenAI, Groq, DeepSeek, local models, and any OpenAI-compatible API!

#### Quick Examples

```go
// Using OpenAI (Default)
client, _ := ai.NewClient(
    ai.WithAPIKey("your-openai-key"),
)

// Using Groq (300 tokens/sec, free tier available)
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Same provider!
    ai.WithBaseURL("https://api.groq.com/openai/v1"),
    ai.WithAPIKey("your-groq-key"),
    ai.WithModel("openai/gpt-oss-120b"),
)

// Using DeepSeek (advanced reasoning)
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Same provider!
    ai.WithBaseURL("https://api.deepseek.com"),
    ai.WithAPIKey("your-deepseek-key"),
    ai.WithModel("deepseek-reasoner"),
)

// Using Local Ollama
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Same provider!
    ai.WithBaseURL("http://localhost:11434/v1"),
    ai.WithModel("llama3:70b"),
)

// Your company's OpenAI-compatible deployment
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Same provider!
    ai.WithBaseURL("https://llm.company.internal/v1"),
    ai.WithAPIKey("internal-key"),
)
```

### Complete Provider List

| Provider | Type | Base URL | Auto-Detection | Build Tag |
|----------|------|----------|----------------|-----------|
| **OpenAI** | Native | `https://api.openai.com/v1` | ✅ `OPENAI_API_KEY` | Default |
| **Anthropic Claude** | Native | N/A | ✅ `ANTHROPIC_API_KEY` | Default |
| **Google Gemini** | Native | N/A | ✅ `GEMINI_API_KEY` | Default |
| **AWS Bedrock** | Native | Region-based | ✅ AWS credentials, IAM roles, profiles | `bedrock` |
| **Groq** | OpenAI-compatible | `https://api.groq.com/openai/v1` | ✅ `GROQ_API_KEY` | Default |
| **DeepSeek** | OpenAI-compatible | `https://api.deepseek.com` | ✅ `DEEPSEEK_API_KEY` | Default |
| **xAI Grok** | OpenAI-compatible | `https://api.x.ai/v1` | ✅ `XAI_API_KEY` | Default |
| **Qwen (Alibaba)** | OpenAI-compatible | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | ✅ `QWEN_API_KEY` | Default |
| **Together AI** | OpenAI-compatible | Custom endpoint | Use `OPENAI_BASE_URL` | Default |
| **Perplexity** | OpenAI-compatible | Custom endpoint | Use `OPENAI_BASE_URL` | Default |
| **OpenRouter** | OpenAI-compatible | Custom endpoint | Use `OPENAI_BASE_URL` | Default |
| **Azure OpenAI** | OpenAI-compatible | `https://{resource}.openai.azure.com` | Use `OPENAI_BASE_URL` | Default |
| **Ollama** | OpenAI-compatible | `http://localhost:11434/v1` | ✅ Auto-detected if running | Default |
| **vLLM** | OpenAI-compatible | `http://localhost:8000/v1` | Use `OPENAI_BASE_URL` | Default |
| **llama.cpp** | OpenAI-compatible | `http://localhost:8080/v1` | Use `OPENAI_BASE_URL` | Default |
| **Any OpenAI-compatible API** | OpenAI-compatible | Your endpoint | Use `OPENAI_BASE_URL` | Default |

### Auto-Detection Priority

When you use `ai.NewClient()` without specifying a provider, the module checks for available services in this order:

1. **OpenAI** (priority: 1000) - Checks for `OPENAI_API_KEY`
2. **Anthropic** (priority: 900) - Checks for `ANTHROPIC_API_KEY` (native implementation)
3. **Gemini** (priority: 800) - Checks for `GEMINI_API_KEY` or `GOOGLE_API_KEY`
4. **Groq** (priority: 700) - Checks for `GROQ_API_KEY`, configures endpoint automatically
5. **DeepSeek** (priority: 600) - Checks for `DEEPSEEK_API_KEY`, configures endpoint automatically
6. **xAI Grok** (priority: 500) - Checks for `XAI_API_KEY`, configures endpoint automatically
7. **Mistral** (priority: 450) - Checks for `MISTRAL_API_KEY`, configures endpoint automatically
8. **Qwen** (priority: 400) - Checks for `QWEN_API_KEY`, configures endpoint automatically
9. **Together AI** (priority: 300) - Checks for `TOGETHER_API_KEY`, configures endpoint automatically
10. **AWS Bedrock** (priority: 200+) - Checks for AWS credentials, IAM roles, or profiles
   - Gets +50 priority when running on AWS infrastructure (EC2/ECS/Lambda)
11. **Ollama** (priority: 100) - Requires `OLLAMA_BASE_URL` to be explicitly set (does not auto-probe localhost)

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
# Groq - Ultra-fast inference
export GROQ_API_KEY=your-key
# Automatically uses https://api.groq.com/openai/v1

# DeepSeek - Advanced reasoning models
export DEEPSEEK_API_KEY=your-key
# Automatically uses https://api.deepseek.com

# xAI Grok - Elon's AI
export XAI_API_KEY=your-key
# Automatically uses https://api.x.ai/v1

# Qwen (Alibaba) - Multilingual excellence
export QWEN_API_KEY=your-key
# Automatically uses https://dashscope-intl.aliyuncs.com/compatible-mode/v1

# Anthropic Claude - Native implementation
export ANTHROPIC_API_KEY=your-key

# Google Gemini - Native implementation
export GEMINI_API_KEY=your-key
```

### Key Benefits of Universal Provider

1. **Zero Code Duplication**: One implementation for all OpenAI-compatible services
2. **Future-Proof**: New OpenAI-compatible services work immediately without code changes
3. **Flexibility**: Use cloud providers, local models, or private deployments
4. **Simple Migration**: Switch providers by changing base URL only
5. **Auto-Detection**: Automatically finds and configures available services

## 11. How It Works

### Auto-Detection

When you call `ai.NewClient()` without specifying a provider, the module automatically checks for available services in priority order and configures the best option. Similarly, `ai.NewChainClient()` without `WithProviderChain()` auto-detects all available providers and builds a failover chain ordered by priority. See the [Auto-Detection Priority](#auto-detection-priority) section for details.

## 12. Core Concepts Explained

### The Provider Registry - Plugin Architecture

The registry is like a plugin system that keeps track of all available providers. Providers register themselves automatically when imported:

```go
// Import the providers you need - each registers itself via init()
import (
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"    // Universal provider for 20+ services
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


```bash
# Native providers (each has its own implementation)
export OPENAI_API_KEY=sk-...          # OpenAI
export ANTHROPIC_API_KEY=sk-ant-...   # Anthropic Claude
export GEMINI_API_KEY=...             # Google Gemini

# OpenAI-compatible services with provider aliases (recommended)
# Each gets its own namespace - no conflicts!
export DEEPSEEK_API_KEY=sk-...        # DeepSeek reasoning models
export DEEPSEEK_BASE_URL=https://...  # Optional: Override endpoint

export GROQ_API_KEY=gsk-...           # Groq ultra-fast inference
export GROQ_BASE_URL=https://...      # Optional: Override endpoint

export XAI_API_KEY=xai-...            # xAI Grok models
export XAI_BASE_URL=https://...       # Optional: Override endpoint

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
- **Regional endpoints**: `DEEPSEEK_BASE_URL=https://eu.api.deepseek.com`
- **Corporate proxies**: `GROQ_BASE_URL=https://ai-proxy.company.internal/groq`
- **Testing environments**: `OPENAI_BASE_URL=https://test.openai.com`
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
    ai.WithTimeout(180 * time.Second),  // Request timeout (default: 180s)
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
- **Timeout**: 180 seconds
- **MaxRetries**: 3
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
    │  • Universal Interface  │
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
translator := ai.NewAITool("translator", "your-api-key")

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
orchestrator := ai.NewAIAgent("orchestrator", "your-api-key")

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
agent := ai.NewAIAgent("assistant", "your-api-key")

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
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
)

type CustomProvider struct{}

func (p *CustomProvider) Name() string {
    return "custom-llm"
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
        return 200, true  // High priority
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
Request-aware construction is currently built into Anthropic and OpenAI, plus
Bedrock with the `bedrock` build tag; Gemini remains legacy-only.

For the complete contracts, request-policy precedence, dynamic credentials and
routing, heterogeneous chains, OpenAI-compatible codec reuse, and SDK-native
draft pattern, see the
[Custom AI Providers and Enterprise Integration Guide](../docs/building/CUSTOM_AI_PROVIDER_GUIDE.md).

### Adding New OpenAI-Compatible Services

Any new OpenAI-compatible service works immediately without code changes:

```go
// Example: Using a new AI service that just launched
// No code changes needed in the module!
client, _ := ai.NewClient(
    ai.WithProvider("openai"),  // Use the universal provider
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

This future-proofs your code - as new services emerge, they'll work automatically if they follow the OpenAI API standard.

### Binary Size Management

The framework uses build tags to keep binaries lightweight:

```bash
# Default build: ~5.5MB (includes OpenAI, Anthropic, Gemini)
go build

# With AWS Bedrock: ~8.2MB (adds AWS SDK)
go build -tags bedrock

# With multiple cloud providers: ~12MB
go build -tags "bedrock,azure,vertex"
```

**The Rule:** Cloud SDK providers (AWS, Azure, GCP) require explicit build tags to avoid bloating binaries. All other providers are included by default if they add less than 1MB.

### Common Provider Features

All providers share these built-in features from the base client:

#### Automatic Retry with Exponential Backoff

```go
// Configure retry behavior
client, _ := ai.NewClient(
    ai.WithMaxRetries(5),        // Default: 3
    ai.WithTimeout(60 * time.Second),
)

// The module automatically retries on:
// - Network errors
// - 5xx server errors 
// - Rate limiting (429)
// - Timeout errors
```

#### Request/Response Logging

All providers support structured logging for debugging:

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
// - Timeout: 180 seconds
// - MaxRetries: 3
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

// Check if a provider supports embeddings
if embedder, ok := client.(ai.EmbeddingClient); ok {
    embeddings, _ := embedder.GenerateEmbeddings(ctx, text)
}
// For the core.EmbeddingClient interface with OpenAI, Qdrant,
// and Redis examples, see docs/building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md §8.
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

The zero value of `AIParameter` means inherit, `SetAIParameter` means explicitly
send even a zero value, and `OmitAIParameter` means require absence. Core's
`GenerateAI` and `StreamAI` helpers use the request-aware capability when
available and use a legacy client only when the request can be represented
without loss. Unsupported intent returns
`core.ErrAIRequestFeatureUnsupported`.

Application policy is configured with `WithRequestRules`,
`WithRequestMiddleware`, and `WithCompatibilityMode`; per-request patches live
on `AIRequest.Patches`. `AIResult.RequestReport` contains sanitized preparation
facts and a secret-free semantic fingerprint. It never contains prompt text,
credentials, or raw request bodies.

Use `NewChain` with `ProviderEntry` and `ClientEntry` when failover entries need
independent policy, credentials, routes, or client implementations. The legacy
`NewChainClient` remains supported for a homogeneous option set.

See the [Custom AI Providers and Enterprise Integration Guide](../docs/building/CUSTOM_AI_PROVIDER_GUIDE.md)
and [API Reference](../docs/reference/API_REFERENCE.md#request-aware-ai-api) for
the complete contracts.

## 15. Streaming Support

The AI module provides comprehensive streaming support across all providers. Streaming delivers AI responses token-by-token as they're generated, enabling real-time UX and lower time-to-first-token.

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

All providers implement the `StreamingAIClient` interface:

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

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
    client, _ := ai.NewClient()

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
    ai.WithProviderChain("openai", "openai.deepseek", "anthropic"),
)

// Stream with automatic failover
_, err := client.StreamResponse(ctx, prompt, options, func(chunk core.StreamChunk) error {
    fmt.Print(chunk.Content)
    return nil
})
// If OpenAI fails, automatically tries DeepSeek, then Anthropic
```

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
| Groq | ✅ Full | OpenAI-compatible streaming |
| DeepSeek | ✅ Full | OpenAI-compatible streaming |
| xAI | ✅ Full | OpenAI-compatible streaming |
| Qwen | ✅ Full | OpenAI-compatible streaming |
| Ollama | ✅ Full | OpenAI-compatible streaming |
| Mock | ✅ Full | Simulates realistic streaming |

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
// From OpenAI to Groq (faster, cheaper)
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
# Development: Use fast, cheap Groq
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
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/telemetry"
)

// Initialize telemetry FIRST (critical!)
telemetry.Initialize(telemetry.Config{
    ServiceName: "my-agent",
    Endpoint:    "http://otel-collector:4318",
})
defer telemetry.Shutdown(context.Background())

// Create AI client WITH telemetry provider
aiClient, err := ai.NewClient(
    ai.WithTelemetry(telemetry.GetTelemetryProvider()),
)
```

### ⚠️ Critical: Initialization Order

**Telemetry MUST be initialized BEFORE creating the AI client.** If you create the AI client first, `telemetry.GetTelemetryProvider()` returns `nil` and no AI spans will be captured.

```go
// ✅ CORRECT: Telemetry first, then AI client
func main() {
    initTelemetry("my-service")
    defer telemetry.Shutdown(context.Background())

    aiClient, _ := ai.NewClient(
        ai.WithTelemetry(telemetry.GetTelemetryProvider()),
    )
    // AI spans will appear in Jaeger!
}

// ❌ WRONG: AI client created before telemetry
func main() {
    aiClient, _ := ai.NewClient(
        ai.WithTelemetry(telemetry.GetTelemetryProvider()), // Returns nil!
    )
    initTelemetry("my-service")  // Too late
    // No AI spans in traces
}
```

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

1. **Universal OpenAI Provider** - One implementation works with 20+ services (OpenAI, Groq, DeepSeek, xAI, Qwen, Ollama, and any OpenAI-compatible API)
2. **Native Providers** - Optimized implementations for Anthropic Claude, Google Gemini, and AWS Bedrock
3. **Auto-Detection** - Automatically finds and configures the best available provider from your environment
4. **Zero Code Changes** - Switch between providers by changing configuration, not code
5. **Provider Registry** - Plugin architecture for easy extension with custom providers
6. **AI Components** - Build intelligent agents that can discover and orchestrate other components
7. **Smart Configuration** - Sensible defaults with fine-grained control when needed
8. **Binary Optimization** - Cloud providers use build tags to keep binaries small
9. **Future-Proof** - New OpenAI-compatible services work instantly without any code changes
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

**🎊 Congratulations!** You now understand the AI module - your universal interface to the world of AI. The module handles all the complexity of different providers, letting you focus on building amazing AI-powered features.

Remember: Start simple with auto-detection, then customize as your needs grow. The module scales with you from prototype to production. Happy building! 🚀
