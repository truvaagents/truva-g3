# AI Providers Setup Guide

Welcome to the TruvaG3 AI providers guide! This document explains how to configure AI providers for your agents and tools, from simple single-provider setups to production-ready multi-provider failover systems. Think of this as your complete reference for doing AI integration the right way.

## Table of Contents

- [Why This Guide Exists](#why-this-guide-exists)
- [The Two Types of AI Clients](#the-two-types-of-ai-clients)
  - [Single Client: The Simple Path](#single-client-the-simple-path)
  - [Chain Client: Production-Grade Resilience](#chain-client-production-grade-resilience)
  - [When to Use Which](#when-to-use-which)
- [Provider Aliases: The Clean Way to Configure](#provider-aliases-the-clean-way-to-configure)
  - [What Problem Do They Solve?](#what-problem-do-they-solve)
  - [Complete Provider Alias Reference](#complete-provider-alias-reference)
  - [How Auto-Configuration Works](#how-auto-configuration-works)
- [Model Aliases: Portable Model Names](#model-aliases-portable-model-names)
  - [The Model Name Problem](#the-model-name-problem)
  - [Standard Model Aliases](#standard-model-aliases)
  - [Environment Variable Overrides](#environment-variable-overrides)
  - [How Model Resolution Works](#how-model-resolution-works)
- [Understanding Failover Behavior](#understanding-failover-behavior)
  - [How Chain Client Decides to Failover](#how-chain-client-decides-to-failover)
  - [Why Authentication Errors Allow Failover](#why-authentication-errors-allow-failover)
  - [The Options Isolation Problem (And How We Solved It)](#the-options-isolation-problem-and-how-we-solved-it)
- [Operational Scenarios](#operational-scenarios)
  - [Scenario 1: Local Development with Ollama](#scenario-1-local-development-with-ollama)
  - [Scenario 2: Development with Cloud Providers](#scenario-2-development-with-cloud-providers)
  - [Scenario 3: Staging Environment](#scenario-3-staging-environment)
  - [Scenario 4: Production with High Availability](#scenario-4-production-with-high-availability)
  - [Scenario 5: Cost-Optimized Production](#scenario-5-cost-optimized-production)
  - [Scenario 6: Privacy-First Deployment](#scenario-6-privacy-first-deployment)
  - [Scenario 7: Multi-Region Deployment](#scenario-7-multi-region-deployment)
- [Kubernetes Deployment](#kubernetes-deployment)
  - [Managing API Keys with Secrets](#managing-api-keys-with-secrets)
  - [Managing Model Aliases with ConfigMaps](#managing-model-aliases-with-configmaps)
  - [Same Image, Different Behavior](#same-image-different-behavior)
  - [Rolling Updates Without Downtime](#rolling-updates-without-downtime)
- [Troubleshooting Common Issues](#troubleshooting-common-issues)
  - [Issue 1: "No Providers Available" Error](#issue-1-no-providers-available-error)
  - [Issue 2: Wrong Model Being Used](#issue-2-wrong-model-being-used)
  - [Issue 3: Failover Not Working](#issue-3-failover-not-working)
  - [Issue 4: Model Not Found After Failover](#issue-4-model-not-found-after-failover)
  - [Issue 5: Unexpected Provider Being Used](#issue-5-unexpected-provider-being-used)
  - [Issue 6: Ollama Is Configured but Cloud Providers Still Win](#issue-6-ollama-is-configured-but-cloud-providers-still-win)
  - [Issue 7: Concrete Model Name Breaks Chain Failover](#issue-7-concrete-model-name-breaks-chain-failover)
  - [Issue 8: Streaming Text Arrives but the Request Does Not Finish Immediately](#issue-8-streaming-text-arrives-but-the-request-does-not-finish-immediately)
- [Debugging and Observability](#debugging-and-observability)
  - [Enabling Debug Logging](#enabling-debug-logging)
  - [Understanding AI Module Logs](#understanding-ai-module-logs)
  - [Tracing AI Requests in Jaeger](#tracing-ai-requests-in-jaeger)
- [Advanced Configuration](#advanced-configuration)
  - [Portable Fields vs Provider-Specific Escape Hatches](#portable-fields-vs-provider-specific-escape-hatches)
  - [Request-Aware Clients and Policy](#request-aware-clients-and-policy)
  - [Anthropic Sampling Compatibility](#anthropic-sampling-compatibility)
  - [Request Timeouts](#request-timeouts)
  - [Reasoning Model Support](#reasoning-model-support)
  - [Orchestration Model Overrides](#orchestration-model-overrides)
- [Quick Reference](#quick-reference)
  - [Environment Variable Cheat Sheet](#environment-variable-cheat-sheet)
  - [Decision Tree: Which Client Type?](#decision-tree-which-client-type)
  - [Error Classification Reference](#error-classification-reference)

---

## Why This Guide Exists

In a production system, AI integration is rarely as simple as "call OpenAI and hope for the best." You need to handle:

- **Provider outages**: What happens when OpenAI is down?
- **Budget exhaustion**: What happens when your API quota or spending limit is reached?
- **Cost management**: How do you use cheaper models in development?
- **API key rotation**: How do you change keys without redeploying?
- **Regional routing**: How do you route traffic to regional endpoints (e.g., EU data residency)?

Without a clear strategy, you end up with:
- Hardcoded API keys (security nightmare)
- No failover (single point of failure)
- Different code paths for different environments (maintenance nightmare)

This guide ensures every TruvaG3 deployment handles AI providers in a consistent, production-ready way.

---

## The Two Types of AI Clients

TruvaG3 provides two ways to connect to AI providers. Understanding when to use each is the first decision you'll make.

### Single Client: The Simple Path

A Single Client connects directly to one provider. It's the simplest approach and works great when you don't need failover.

```go
import (
    "github.com/truvaagents/truva-g3/ai"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

// The simplest possible setup - auto-detects from environment
client, err := ai.NewClient()

// Or explicitly choose a provider
client, err := ai.NewClient(
    ai.WithProviderAlias("openai.groq"),
    ai.WithModel("smart"),
)
```

**Behind the scenes**, when you call `ai.NewClient()` without arguments:
1. The module checks registered providers in priority order
2. Each provider's `DetectEnvironment()` method checks for API keys
3. The first available provider wins
4. You get a configured client without writing any configuration

**When Single Client makes sense:**
- Development and testing
- Simple applications where downtime is acceptable
- When you're locked into one provider (e.g., enterprise agreement)
- Background jobs where latency isn't critical

### Chain Client: Production-Grade Resilience

A Chain Client tries multiple providers in order until one succeeds. It's the production-ready approach for systems that can't afford downtime.

```go
import (
    "github.com/truvaagents/truva-g3/ai"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
)

// Option 1: Explicit chain — you control the order
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic", "openai.groq"),
)

// Option 2: Auto-detect — discovers providers from environment variables
// Builds chain in priority order from whatever API keys are available
client, err := ai.NewChainClient(
    ai.WithChainLogger(logger),
)
// With OPENAI_API_KEY + ANTHROPIC_API_KEY + GROQ_API_KEY set:
// → chain = ["openai" (1000), "anthropic" (900), "openai.groq" (700)]

// Use it exactly like a single client
response, err := client.GenerateResponse(ctx, "Analyze this data...", nil)
```

**Behind the scenes**, when you make a request:
1. Chain Client tries Provider 1 (OpenAI)
2. If it fails with a retryable error, it tries Provider 2 (Anthropic)
3. If that fails too, it tries Provider 3 (Groq)
4. Returns the first successful response
5. If all fail, returns an error with details from the last attempt

**Auto-detect provider priorities** (when no explicit chain is specified):

| Provider | Alias | Priority |
|----------|-------|----------|
| OpenAI | `openai` | 1000 |
| Anthropic | `anthropic` | 900 |
| Gemini | `gemini` | 800 |
| Groq | `openai.groq` | 700 |
| DeepSeek | `openai.deepseek` | 600 |
| xAI | `openai.xai` | 500 |
| Mistral | `openai.mistral` | 450 |
| Qwen | `openai.qwen` | 400 |
| Together | `openai.together` | 300 |
| Bedrock | `bedrock` | 200 |
| Ollama | `openai.ollama` | 100 |

**The key insight**: Each provider in the chain resolves model aliases independently. When you pass `Model: "smart"`, OpenAI resolves it to `o3`, Anthropic resolves it to `claude-sonnet-4-5`, and Groq resolves it to `openai/gpt-oss-120b`. You don't need different code for different providers.

### When to Use Which

| Situation | Recommended | Why |
|-----------|-------------|-----|
| Local development | Single Client | Simpler, faster iteration |
| Staging/testing | Chain Client (auto-detect) | Mirrors production env vars, tests failover |
| Production API | Chain Client | High availability is essential |
| Production (deterministic order) | Chain Client (explicit) | Locks failover order regardless of env changes |
| Production (ops-driven) | Chain Client (auto-detect) | Ops controls chain via which API keys are deployed |
| Background processing | Either | Depends on retry strategy |
| Cost-sensitive batch jobs | Chain Client (explicit) | Try cheap providers first in specific order |
| Compliance-restricted | Single Client | May not be allowed to send data to multiple providers |

**The golden rule**: If you'd lose money or users when AI is down, use Chain Client.

---

## Provider Aliases: The Clean Way to Configure

### What Problem Do They Solve?

Before provider aliases, configuring an OpenAI-compatible service looked like this:

```go
// The old, messy way
client, _ := ai.NewClient(
    ai.WithProvider("openai"),
    ai.WithBaseURL("https://api.groq.com/openai/v1"),  // Have to remember this
    ai.WithAPIKey(os.Getenv("GROQ_API_KEY")),          // Different env var
    ai.WithModel("openai/gpt-oss-120b"),               // Provider-specific model
)
```

Every new provider meant remembering URLs, env vars, and model names. And if Groq changed their URL? You'd have to update every project.

**With provider aliases**, it's one line:

```go
// The clean way
client, _ := ai.NewClient(ai.WithProviderAlias("openai.groq"))
```

The framework knows that `openai.groq` means:
- Use `GROQ_API_KEY` for authentication
- Connect to `https://api.groq.com/openai/v1`
- Use Groq's model naming conventions

### Complete Provider Alias Reference

| Alias | Type | Service | API Key Env Var | Base URL Env Var | Default URL |
|-------|------|---------|-----------------|------------------|-------------|
| `openai` | Native | OpenAI | `OPENAI_API_KEY` | `OPENAI_BASE_URL` | `https://api.openai.com/v1` |
| `anthropic` | Native | Anthropic Claude | `ANTHROPIC_API_KEY` | _(N/A - native API)_ | _(native implementation)_ |
| `gemini` | Native | Google Gemini | `GEMINI_API_KEY` or `GOOGLE_API_KEY` | _(N/A - native API)_ | _(native implementation)_ |
| `bedrock` | Native | AWS Bedrock | _(configured via `Extra`)_ | _(N/A - native API)_ | _(native implementation)_ |
| `openai.groq` | OpenAI-compatible | Groq | `GROQ_API_KEY` | `GROQ_BASE_URL` | `https://api.groq.com/openai/v1` |
| `openai.deepseek` | OpenAI-compatible | DeepSeek | `DEEPSEEK_API_KEY` | `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` |
| `openai.xai` | OpenAI-compatible | xAI Grok | `XAI_API_KEY` | `XAI_BASE_URL` | `https://api.x.ai/v1` |
| `openai.mistral` | OpenAI-compatible | Mistral | `MISTRAL_API_KEY` | `MISTRAL_BASE_URL` | `https://api.mistral.ai/v1` |
| `openai.qwen` | OpenAI-compatible | Alibaba Qwen | `QWEN_API_KEY` | `QWEN_BASE_URL` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |
| `openai.together` | OpenAI-compatible | Together AI | `TOGETHER_API_KEY` | `TOGETHER_BASE_URL` | `https://api.together.xyz/v1` |
| `openai.ollama` | OpenAI-compatible | Ollama (local) | _(none)_ | `OLLAMA_BASE_URL` | `http://localhost:11434/v1` |

### How Auto-Configuration Works

When you use a provider alias, the framework resolves configuration in this order:

1. **Explicit code configuration** (highest priority)
   ```go
   ai.WithAPIKey("sk-explicit-key")  // This wins
   ```

2. **Provider-specific environment variables**
   ```bash
   GROQ_API_KEY=gsk-from-env  # Used if no explicit key
   ```

3. **Base URL overrides** (for proxies, regional endpoints)
   ```bash
   GROQ_BASE_URL=https://eu.api.groq.com/openai/v1  # Optional override
   ```

4. **Default values** (built into the framework)
   - Default URL for the provider
   - Default model aliases

**Practical example**: Your code uses `openai.groq`. In production, you set:
```bash
GROQ_API_KEY=gsk-prod-key
GROQ_BASE_URL=https://ai-proxy.company.internal/groq  # Route through internal proxy
```

No code changes needed. The proxy gets all Groq traffic for logging/monitoring.

---

## Model Aliases: Portable Model Names

### The Model Name Problem

Every AI provider has different model names:
- OpenAI: `gpt-4.1-mini`, `o3`, `gpt-4.1`
- Anthropic: `claude-sonnet-4-5-20250929`, `claude-haiku-4-5-20251001`
- Groq: `openai/gpt-oss-120b`, `llama-3.3-70b-versatile`, `llama-3.1-8b-instant`

If you hardcode model names, switching providers means changing code everywhere. And when providers release new models? More code changes.

**Model aliases solve this** by providing portable names:

```go
// This code works with ANY provider
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai"),  // or "anthropic", or "openai.groq"
    ai.WithModel("smart"),           // Resolves to the right model for each provider
)
```

### Standard Model Aliases

| Alias | Purpose | OpenAI | Anthropic | Gemini | Groq | DeepSeek |
|-------|---------|--------|-----------|--------|------|----------|
| `default` | General use, balanced cost/quality | `gpt-4.1-mini` | `claude-sonnet-4-5` | `gemini-2.5-flash` | `openai/gpt-oss-120b` | `deepseek-chat` |
| `fast` | Speed and cost optimized | `gpt-4.1-mini` | `claude-haiku-4-5` | `gemini-2.5-flash-lite` | `llama-3.1-8b-instant` | `deepseek-chat` |
| `smart` | Best reasoning quality | `o3` | `claude-sonnet-4-5` | `gemini-2.5-pro` | `openai/gpt-oss-120b` | `deepseek-reasoner` |
| `premium` | Maximum intelligence | _(N/A)_ | `claude-opus-4-5` | `gemini-3-pro-preview` | _(N/A)_ | _(N/A)_ |
| `code` | Code generation | `o3` | `claude-sonnet-4-5` | `gemini-2.5-pro` | `openai/gpt-oss-120b` | `deepseek-chat` |
| `vision` | Image understanding | `gpt-4.1` | `claude-sonnet-4-5` | `gemini-2.5-flash` | _(N/A)_ | _(N/A)_ |

> **Note**: The `premium` alias is only available for Anthropic and Gemini. For other providers, use `smart` for best reasoning quality.

### Environment Variable Overrides

Here's where it gets powerful. You can override any alias at runtime without changing code:

```bash
# Pattern: TRUVAG3_{PROVIDER}_MODEL_{ALIAS}=actual-model-name

# Override OpenAI's "smart" alias
export TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1

# Override Anthropic's "fast" alias
export TRUVAG3_ANTHROPIC_MODEL_FAST=claude-haiku-4-5-20251001

# For OpenAI-compatible providers, strip the "openai." prefix
export TRUVAG3_GROQ_MODEL_DEFAULT=llama-3.1-8b-instant
export TRUVAG3_DEEPSEEK_MODEL_SMART=deepseek-reasoner
```

**Why this matters for ops**:
- **Cost control**: Set `TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1-mini` in dev to save money
- **A/B testing**: Route traffic to different models without code changes
- **Rollback**: If a new model has issues, switch back via env var

### How Model Resolution Works

When you call `client.GenerateResponse(ctx, prompt, &core.AIOptions{Model: "smart"})`, here's the resolution order:

1. **Environment variable** (highest priority)
   ```bash
   TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1  # If set, use this
   ```

2. **Hardcoded alias mapping**
   ```go
   modelAliases["openai"]["smart"] = "o3"  // Built-in default
   ```

3. **Pass-through** (lowest priority)
   ```go
   // If "smart" isn't recognized, use it literally
   // This lets you use explicit model names when needed
   ```

**Example flow**:
```
Request: Model="smart", Provider="openai"
  ↓
Check: TRUVAG3_OPENAI_MODEL_SMART env var?
  → Not set
  ↓
Check: modelAliases["openai"]["smart"]?
  → Returns "o3"
  ↓
Result: Use model "o3"
```

---

## Understanding Failover Behavior

### How Chain Client Decides to Failover

Not all errors should trigger failover. If your request is malformed, trying another provider won't help—you'll just get the same error three times.

Chain Client classifies errors using the `core.ProviderError` interface, which carries structured metadata (HTTP status code, provider name, model, and transient flag) from the AI provider layer. This replaces fragile string matching with type-safe error classification via `errors.As()`.

**Errors that ALLOW failover** (tries next provider):

| Error Type | `core.ProviderError` Classification | Why Failover Makes Sense |
|------------|--------------------------------------|--------------------------|
| Authentication (401) | `StatusCode() == 401` | Different providers have different keys |
| Authorization (403) | `StatusCode() == 403` | Permission scopes differ per provider |
| Server errors (5xx) | `StatusCode() >= 500` | Provider-specific outage |
| Rate limits (429) | `StatusCode() == 429` | Limits are per-provider |
| Network errors | Non-`ProviderError` (no HTTP status) | Might be routing/DNS issue |
| Transient proxy errors | `IsTransient() == true` | Proxy/CDN issue (e.g., Cloudflare HTML 400), not a request problem |

**Errors that STOP failover** (fails immediately):

| Error Type | `core.ProviderError` Classification | Why Failover Won't Help |
|------------|--------------------------------------|-------------------------|
| Bad request (400) | `StatusCode() == 400 && !IsTransient()` | Same input fails everywhere |
| Content policy | `StatusCode() == 400` with policy message | Same content fails everywhere |
| Malformed input | `StatusCode() == 400` with parse error | Structural issue in your code |

> **Transient proxy errors**: When a request passes through a CDN or reverse proxy (e.g., Cloudflare) and the proxy itself returns an error (typically an HTML page with a 400 status), the framework marks this as `IsTransient() == true`. These are infrastructure issues, not request problems, so failover to the next provider is appropriate. The `core.ProviderError` interface distinguishes these from genuine API 400 errors.

### Why Authentication Errors Allow Failover

This is a design decision that confuses some people. Traditionally, a 401 error means "your credentials are wrong, stop trying." But in a multi-provider chain, each provider has its own API key.

Consider this scenario:
```
Chain: ["openai", "anthropic", "openai.groq"]

Environment:
  OPENAI_API_KEY=sk-expired-key     # Oops, forgot to rotate
  ANTHROPIC_API_KEY=sk-ant-valid    # Works fine
  GROQ_API_KEY=gsk-valid            # Works fine
```

With traditional error handling:
```
Request → OpenAI → 401 "invalid key" → ERROR (stop)
User gets an error even though two providers would work
```

With Chain Client:
```
Request → OpenAI → 401 "invalid key" → Try next
        → Anthropic → Success!
User gets their response, ops gets alerted about OpenAI key
```

**The tradeoff**: You might make extra API calls before finding a working provider. But in production, uptime usually matters more than a few extra milliseconds.

### The Options Isolation Problem (And How We Solved It)

This is a subtle bug that caused real production issues before we fixed it. Here's what happened:

**The problem**: When Provider 1 fails, Provider 2 receives Provider 1's resolved model name.

```
Step 1: Request with Model="smart"
        Chain Client tries OpenAI
        OpenAI resolves "smart" → "o3"
        OpenAI fails with 401

Step 2: Chain Client tries Anthropic
        Options still has Model="o3" (from OpenAI!)
        Anthropic doesn't know "o3"
        Anthropic uses default model instead of resolving "smart"
```

**The fix**: Chain Client now clones options for each provider and resets the model to the original value:

```go
// Inside Chain Client (simplified)
originalModel := options.Model  // Save "smart"

for _, provider := range providers {
    providerOpts := cloneOptions(options)
    providerOpts.Model = originalModel  // Reset to "smart"

    response, err := provider.GenerateResponse(ctx, prompt, providerOpts)
    // Now each provider resolves "smart" independently
}
```

**What this means for you**: Model aliases work correctly during failover. You don't need to do anything special.

### How Errors Propagate Through the Stack

Understanding the error propagation path is critical for ensuring ChainClient failover works correctly. Here's the full lifecycle of an AI provider error:

```
1. Provider HTTP call fails (e.g., OpenAI returns 429)
   ↓
2. ai/providers/base.go HandleError() wraps it as *providerError
   (implements core.ProviderError: StatusCode=429, Provider="openai", Model="o3")
   ↓
3. ChainClient.isClientError() inspects via errors.As(err, &pe)
   → 429 is excluded from client errors → FAILOVER to next provider
   ↓
4. If ALL providers fail, the last error (still a core.ProviderError)
   propagates up through the orchestrator via fmt.Errorf("...: %w", err)
   ↓
5. Agent handler receives the error from orch.ProcessRequest()
   → errors.As(err, &pe) finds the ProviderError through the wrapping chain
   → Handler surfaces pe.StatusCode() as the HTTP response status
```

**The key rule**: Every layer in the stack uses `%w` verb when wrapping errors:

```go
// CORRECT — preserves core.ProviderError for upstream callers
return fmt.Errorf("orchestration failed: %w", err)

// WRONG — destroys the ProviderError type, ChainClient can't classify it
return fmt.Errorf("orchestration failed: %s", err)     // String conversion, type lost
return errors.New(err.Error())                          // New error, type lost
return fmt.Errorf("orchestration failed: %v", err)      // Value format, type lost
```

If any layer breaks the `%w` chain:
- **ChainClient** can't classify the error → defaults to failover (safe but wasteful for true client errors)
- **Agent handler** can't extract the status code → falls back to 500 (misleading to callers)
- **LLM debug store** can't extract provider/model → error records have empty metadata

**What this means for agent developers**: When you wrap errors from orchestrator or AI client calls, always use `%w`. The framework's own code already follows this rule — you just need to maintain it in your handler code.

> **See also:** [AGENT_DEVELOPMENT_GUIDE.md](AGENT_DEVELOPMENT_GUIDE.md) for the complete handler pattern showing `core.ProviderError` extraction.

---

## Operational Scenarios

This section covers real-world deployment scenarios. Find the one that matches your situation.

### Scenario 1: Local Development with Ollama

**Goal**: Develop without cloud API costs, test offline

**Setup on your laptop**:
```bash
# Install and start Ollama
ollama serve

# Pull a model
ollama pull gemma4:26b

# Your .env file
OLLAMA_BASE_URL=http://localhost:11434/v1
TRUVAG3_OLLAMA_MODEL_DEFAULT=gemma4:26b
TRUVAG3_OLLAMA_MODEL_SMART=gemma4:26b
```

**Code**:
```go
// Single client is fine for local dev
client, err := ai.NewClient(ai.WithProviderAlias("openai.ollama"))
```

**Setup from Kind / OrbStack on your laptop**:
```bash
# Pods need to reach the Ollama server running on your host
OLLAMA_BASE_URL=http://host.docker.internal:11434/v1
TRUVAG3_OLLAMA_MODEL_DEFAULT=gemma4:26b
```

**If you want Ollama to win over cloud providers**:
```go
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai.ollama", "openai"),
)
```

If you rely on auto-detect and also set `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
or other higher-priority providers, Ollama will usually be used only as a fallback.

**Useful notes**:
- `openai.ollama` does not require an API key.
- Use the exact model tag you run with Ollama. If you use `ollama run gemma4:26b`, set `TRUVAG3_OLLAMA_MODEL_DEFAULT=gemma4:26b`.
- `openai.ollama` can now accept reasoning controls through TruvaG3, but support is still model-dependent.

**Pro tip**: Create a `make dev` target that starts Ollama:
```makefile
dev:
    ollama serve &
    OLLAMA_BASE_URL=http://localhost:11434/v1 go run .
```

### Scenario 2: Development with Cloud Providers

**Goal**: Use cloud AI for development, but minimize costs

**Setup**:
```bash
# .env.development
GROQ_API_KEY=gsk-dev-key  # Groq has generous free tier

# Use smaller/cheaper models in dev
TRUVAG3_GROQ_MODEL_DEFAULT=llama-3.1-8b-instant
TRUVAG3_GROQ_MODEL_SMART=llama-3.1-8b-instant
```

**Code**:
```go
// Use Groq's free tier for development
client, err := ai.NewClient(ai.WithProviderAlias("openai.groq"))
```

**Why Groq for dev?**
- Free tier with 14,000 tokens/minute
- Ultra-fast inference (great for iteration)
- OpenAI-compatible API (easy to switch later)

### Scenario 3: Staging Environment

**Goal**: Mirror production setup, test failover, validate before deployment

**Setup**:
```bash
# .env.staging
# Use same providers as production
OPENAI_API_KEY=sk-staging-key
ANTHROPIC_API_KEY=sk-ant-staging-key
GROQ_API_KEY=gsk-staging-key

# But use mid-tier models to save costs
TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1-mini
TRUVAG3_ANTHROPIC_MODEL_SMART=claude-haiku-4-5-20251001
```

**Code**:
```go
// Same chain as production
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic", "openai.groq"),
)
```

**Testing failover in staging**:
```bash
# Temporarily break OpenAI to test failover
export OPENAI_API_KEY=sk-invalid-key

# Make a request - should succeed via Anthropic
curl -X POST http://staging:8080/api/analyze

# Check logs for:
# {"message": "Provider failed, trying next", "provider": "openai", "error": "401"}
# {"message": "Request succeeded", "provider": "anthropic"}
```

### Scenario 4: Production with High Availability

**Goal**: Maximum uptime, automatic failover, best models

**Setup**:
```bash
# .env.production
OPENAI_API_KEY=sk-prod-key
ANTHROPIC_API_KEY=sk-ant-prod-key
GROQ_API_KEY=gsk-prod-key

# Use best models in production
TRUVAG3_OPENAI_MODEL_SMART=o3
TRUVAG3_ANTHROPIC_MODEL_SMART=claude-sonnet-4-5-20250929
TRUVAG3_GROQ_MODEL_SMART=openai/gpt-oss-120b
```

**Code**:
```go
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic", "openai.groq"),
)
```

**Monitoring production failover**:
```bash
# Alert on consistent failover (indicates provider issues)
# Prometheus query:
rate(ai_chain_failover_total{from_provider="openai"}[5m]) > 0.1
```

### Scenario 5: Cost-Optimized Production

**Goal**: Minimize AI costs while maintaining quality

**Setup**:
```bash
# .env.production-cost-optimized
# Order providers by cost (cheapest first)
GROQ_API_KEY=gsk-key      # Free tier / very cheap
DEEPSEEK_API_KEY=sk-key   # Very affordable
OPENAI_API_KEY=sk-key     # Premium fallback

# Use smaller models where acceptable
TRUVAG3_GROQ_MODEL_DEFAULT=llama-3.1-8b-instant
TRUVAG3_DEEPSEEK_MODEL_DEFAULT=deepseek-chat
```

**Code**:
```go
// Cost-optimized chain: try cheapest first
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai.groq", "openai.deepseek", "openai"),
)
```

**Cost monitoring**:
```go
// Log token usage per request for cost analysis
response, err := client.GenerateResponse(ctx, prompt, nil)
logger.Info("Request completed", map[string]interface{}{
    "model":  response.Model,           // Which model was used
    "tokens": response.Usage.TotalTokens,
})
// Note: Provider tracking is available via telemetry spans (ai.chain.provider attribute)
```

TruvaG3 records normalized token usage, not an estimated USD amount. For
authoritative spend reporting, join provider/model/token telemetry with the
provider's billing export and your negotiated pricing.

### Scenario 6: Privacy-First Deployment

**Goal**: Keep sensitive data local, use cloud only as fallback

**Setup**:
```bash
# .env.production-privacy
# Local model is primary
OLLAMA_BASE_URL=http://gpu-server.internal:11434/v1

# Cloud fallback for when local is overloaded
OPENAI_API_KEY=sk-key

# Use capable local model
TRUVAG3_OLLAMA_MODEL_DEFAULT=llama3.2:70b
TRUVAG3_OLLAMA_MODEL_SMART=llama3.2:70b
```

**Code**:
```go
// Privacy-first: local → cloud
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai.ollama", "openai"),
)
```

**Privacy monitoring** (for compliance):
```go
// Track provider usage via telemetry for audit purposes
// The ai.chain.provider span attribute shows which provider handled each request
// You can query this in Jaeger/Prometheus:
//   rate(ai_chain_attempt{provider="openai",status="success"}[5m]) > 0
// This tells you when requests are being routed to cloud providers
```

### Scenario 7: Multi-Region Deployment

**Goal**: Route to nearest provider, handle regional outages

**Setup**:
```bash
# .env.production-us
OPENAI_API_KEY=sk-us-key
OPENAI_BASE_URL=https://api.openai.com/v1  # US endpoint

# .env.production-eu
OPENAI_API_KEY=sk-eu-key
OPENAI_BASE_URL=https://eu.api.openai.com/v1  # EU endpoint (if available)
DEEPSEEK_BASE_URL=https://eu.api.deepseek.com  # EU regional
```

**Code** (same for all regions):
```go
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "openai.deepseek", "openai.groq"),
)
```

**The key insight**: Same code, different environment variables per region. Kubernetes handles the routing.

---

## Kubernetes Deployment

### Managing API Keys with Secrets

Never put API keys in ConfigMaps or environment variables directly. Use Kubernetes Secrets:

```yaml
# secrets.yaml
apiVersion: v1
kind: Secret
metadata:
  name: ai-api-keys
  namespace: production
type: Opaque
stringData:
  OPENAI_API_KEY: "sk-prod-..."
  ANTHROPIC_API_KEY: "sk-ant-prod-..."
  GROQ_API_KEY: "gsk-prod-..."
```

**Apply it**:
```bash
kubectl apply -f secrets.yaml
```

**Reference in deployment**:
```yaml
spec:
  containers:
  - name: app
    envFrom:
    - secretRef:
        name: ai-api-keys
```

### Managing Model Aliases with ConfigMaps

Model aliases aren't secrets—they can go in ConfigMaps:

```yaml
# configmap-dev.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: ai-model-config
  namespace: development
data:
  TRUVAG3_OPENAI_MODEL_SMART: "gpt-4.1-mini"
  TRUVAG3_ANTHROPIC_MODEL_SMART: "claude-haiku-4-5-20251001"
  TRUVAG3_GROQ_MODEL_DEFAULT: "llama-3.1-8b-instant"

---
# configmap-prod.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: ai-model-config
  namespace: production
data:
  TRUVAG3_OPENAI_MODEL_SMART: "o3"
  TRUVAG3_ANTHROPIC_MODEL_SMART: "claude-sonnet-4-5-20250929"
  TRUVAG3_GROQ_MODEL_DEFAULT: "openai/gpt-oss-120b"
```

### Same Image, Different Behavior

The power of this setup: **one container image works in all environments**.

```yaml
# deployment.yaml (same for dev, staging, prod)
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-ai-service
spec:
  template:
    spec:
      containers:
      - name: app
        image: my-registry/my-ai-service:v1.2.3  # Same image everywhere
        envFrom:
        - secretRef:
            name: ai-api-keys      # Different per namespace
        - configMapRef:
            name: ai-model-config  # Different per namespace
```

**Promotion workflow**:
```bash
# Dev → Staging (same image, different config)
kubectl apply -f configmap-staging.yaml -n staging

# Staging → Prod (same image, different config)
kubectl apply -f configmap-prod.yaml -n production
```

### Rolling Updates Without Downtime

When you need to change models or API keys:

```bash
# Update the ConfigMap
kubectl edit configmap ai-model-config -n production
# Change TRUVAG3_OPENAI_MODEL_SMART from "o3" to "gpt-4.1"

# Trigger rolling restart
kubectl rollout restart deployment/my-ai-service -n production

# Watch the rollout
kubectl rollout status deployment/my-ai-service -n production
```

Pods restart one at a time, picking up the new environment variables. Zero downtime.

---

## Troubleshooting Common Issues

### Issue 1: "No Providers Available" Error

**Symptom**: `NewChainClient` returns "no providers could be initialized" or "no providers detected (check API keys)"

**Cause**: None of the providers in your chain (or environment, if using auto-detect) have valid API keys configured.

**Diagnosis**:
```bash
# Check which env vars are set
echo "OPENAI_API_KEY: ${OPENAI_API_KEY:-(not set)}"
echo "ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY:-(not set)}"
echo "GROQ_API_KEY: ${GROQ_API_KEY:-(not set)}"
```

**Fix**: Set at least one API key:
```bash
export OPENAI_API_KEY=sk-your-key
```

**In Kubernetes**:
```bash
# Check if secret is mounted
kubectl exec -it pod/my-ai-service-xxx -- env | grep API_KEY
```

### Issue 2: Wrong Model Being Used

**Symptom**: Logs show `gpt-4.1-mini` but you expected `o3`

**Cause**: Environment variable override is taking precedence

**Diagnosis**:
```bash
# Check for overrides
env | grep TRUVAG3_

# Look for:
TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1-mini  # This overrides the default!
```

**Fix**: Clear the override or set it to the model you want:
```bash
unset TRUVAG3_OPENAI_MODEL_SMART
# or
export TRUVAG3_OPENAI_MODEL_SMART=o3
```

### Issue 3: Failover Not Working

**Symptom**: Request fails immediately instead of trying next provider

**Cause**: Error is classified as "client error" (non-retryable)

**Diagnosis**: Check logs for error classification:
```json
{"message": "Provider failed", "error": "bad request: invalid prompt", "is_client_error": true}
```

**Understanding**: `is_client_error: true` means the error is in your request, not the provider. Failover won't help because the same request will fail everywhere.

**Fix**: Fix the underlying request issue (malformed JSON, invalid parameters, etc.)

### Issue 4: Model Not Found After Failover

**Symptom**: First provider fails, second provider says "model not found"

**Cause**: You might be running an old version with the options mutation bug

**Diagnosis**: Check the logs:
```json
{"provider": "anthropic", "model": "o3"}  // Anthropic shouldn't see "o3"!
```

**Fix**: Update to the latest TruvaG3 version. The options cloning fix was added in December 2025.

### Issue 5: Unexpected Provider Being Used

**Symptom**: You expected OpenAI but Groq handled the request

**Cause**: Auto-detection picked a different provider based on available env vars

**Diagnosis**:
```bash
# If OPENAI_API_KEY isn't set but GROQ_API_KEY is...
# Auto-detection will use Groq

echo "OPENAI_API_KEY: ${OPENAI_API_KEY:-(not set)}"
echo "GROQ_API_KEY: ${GROQ_API_KEY:-(not set)}"
```

**Fix**: Either set the expected API key, or use explicit provider alias:
```go
// Force OpenAI specifically
client, err := ai.NewClient(ai.WithProviderAlias("openai"))
```

### Issue 6: Ollama Is Configured but Cloud Providers Still Win

**Symptom**: `OLLAMA_BASE_URL` is set, but requests still use OpenAI or Anthropic.

**Cause**: Auto-detect uses provider priority. Ollama is lower priority than cloud providers.

**Fix**: Use an explicit chain if you want local models first:

```go
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai.ollama", "openai", "anthropic"),
)
```

### Issue 7: Concrete Model Name Breaks Chain Failover

**Symptom**: The first provider fails and the second fails with `404 model not found`.

**Cause**: A provider-specific concrete model name was used with `ChainClient`.

**Fix**: Use portable aliases like `default`, `fast`, or `smart` for failover chains.

### Issue 8: Streaming Text Arrives but the Request Does Not Finish Immediately

**Symptom**: The user sees streamed text, but the final `done` metadata takes longer.

**Cause**: Post-synthesis hooks such as user-memory extraction still run before the streaming request is considered complete.

**What to know**:
- The streamed text is already on screen
- The remaining delay is usually post-processing work, not the main synthesis call
- Token usage reported at the end may include these late hook calls

---

## Debugging and Observability

### Enabling Debug Logging

For detailed provider resolution logs, set the debug environment variable:

```bash
# Enable debug logging for all TruvaG3 components
export TRUVAG3_DEBUG=true

# Or set log level directly
export TRUVAG3_LOG_LEVEL=debug
```

Alternatively, use a custom logger with your chain client:

```go
import "github.com/truvaagents/truva-g3/core"

// Create a production logger with debug config
logger := core.NewProductionLogger(
    core.LoggingConfig{Level: "debug", Format: "json"},
    core.DevelopmentConfig{DebugLogging: true},
    "my-service",
)

// Pass to chain client
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic"),
    ai.WithChainLogger(logger),
)
```

### Understanding AI Module Logs

AI module logs use the component prefix `framework/ai`. Here's what to look for:

**Successful request**:
```json
{
  "component": "framework/ai",
  "level": "INFO",
  "message": "AI request completed",
  "operation": "ai_request_success",
  "provider": "openai",
  "model": "o3",
  "prompt_tokens": 150,
  "completion_tokens": 200,
  "duration_ms": 1250
}
```

**Failover event**:
```json
{
  "component": "framework/ai",
  "level": "WARN",
  "message": "Provider failed, trying next",
  "provider": "openai",
  "error": "401 unauthorized",
  "next_provider": "anthropic"
}
```

**All providers exhausted**:
```json
{
  "component": "framework/ai",
  "level": "ERROR",
  "message": "All providers failed",
  "providers_tried": ["openai", "anthropic", "openai.groq"],
  "last_error": "rate limit exceeded"
}
```

### Tracing AI Requests in Jaeger

If you have telemetry enabled, AI requests create spans:

1. Open Jaeger: `http://localhost:16686`
2. Select your service
3. Find traces with `ai.generate` or `ai.stream` spans
4. Expand to see:
   - `ai.provider`: Which provider handled the request
   - `ai.model`: Resolved model name
   - `ai.prompt_tokens`, `ai.completion_tokens`: Token usage
   - `ai.request.prepared`: Sanitized request-policy preparation event
   - `ai.generate_response` / `ai.stream_response`: Provider execution
   - `ai.http_attempt`: Each HTTP retry attempt

---

## Advanced Configuration

This section covers advanced configuration options for fine-tuning AI client behavior, particularly for reasoning models and long-running requests.

### Portable Fields vs Provider-Specific Escape Hatches

Most requests should use the portable `core.AIOptions` fields:

- `Model`
- `Temperature`
- `MaxTokens`
- `SystemPrompt`
- `ReasoningEffort`
- `ResponseFormat`

When a provider exposes a feature TruvaG3 does not model directly yet, use:

- `Extra map[string]interface{}`
- `Headers map[string]string`

Example:

```go
resp, err := client.GenerateResponse(ctx, prompt, &core.AIOptions{
    Model:          "smart",
    ResponseFormat: "json",
    Extra: map[string]interface{}{
        "top_p": 0.9,
    },
    Headers: map[string]string{
        "anthropic-beta": "context-1m-2025-08-07",
    },
})
```

Precedence rules:

- Framework-managed request fields win over everything else
- Per-request `Extra` / `Headers` override client defaults
- Protected headers such as auth and content type are not user-overridable
- The same resolved custom headers are applied to sync and streaming requests

Use escape hatches sparingly. If a field is portable, prefer the typed option.

### Request-Aware Clients and Policy

Use `ai.NewRequestClient` when a call must distinguish an inherited value from
an explicitly supplied zero or an intentionally omitted field, or when the
client needs request rules, middleware, dynamic credentials, or endpoint
routing. Existing `AIOption` values such as `WithProvider`, `WithModel`, and
`WithTimeout` can be passed directly because they also satisfy `ClientOption`.

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithModel("smart"),
)

request := core.NewAIRequest("Extract the entities", "entity_extraction")
request.Generation.Temperature = core.SetAIParameter(float32(0))
request.Generation.TopP = core.OmitAIParameter[float32]()
request.Generation.ResponseFormat = core.SetAIParameter("json")

result, err := core.GenerateAI(ctx, client, request)
```

The zero value of `AIParameter` means inherit, `SetAIParameter` means send the
value even when it is zero, and `OmitAIParameter` means the provider field must
be absent. If a provider or a legacy fallback cannot preserve that intent, the
call returns `core.ErrAIRequestFeatureUnsupported`; it never silently drops the
field.

Request-aware built-in construction currently supports Anthropic and OpenAI,
plus Bedrock when built with `-tags bedrock`. Gemini continues to use the legacy
API. The legacy `NewClient`, `GenerateResponse`, and `NewChainClient` APIs remain
supported for existing and simple portable calls.

Application policy is attached at construction with `WithRequestRules`,
`WithRequestMiddleware`, and `WithCompatibilityMode`. Per-call patches live on
`AIRequest.Patches`. Rules are validated and defensively copied before use;
protected model, stream, content-type, and credential fields cannot be changed
through policy.

For custom factories, policy precedence, enterprise credential and route
hooks, heterogeneous chains, reusable OpenAI-compatible codecs, retry-body
requirements, and cache fingerprints, see the
[Custom AI Providers and Enterprise Integration Guide](CUSTOM_AI_PROVIDER_GUIDE.md).

### Anthropic Sampling Compatibility

Some Anthropic model families reject explicit sampling controls. The Anthropic
adapter resolves the model alias or environment override first, then applies a
model-family policy shared by sync and streaming requests. For the restricted
families currently listed in
[`ai/providers/anthropic/request_policy.go`](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/anthropic/request_policy.go),
it removes `temperature`, `top_p`, and `top_k` and records only fields that were
actually present as adjustments. Matching uses an exact-or-hyphen boundary, so
a similarly prefixed model name is not classified accidentally.

Known compatible model families retain sampling fields. An unknown model also
preserves the legacy behavior instead of guessing that sampling is forbidden.
This protects older and private models, but a newly released restricted family
must be added to the built-in list. Until the framework is updated, a
request-aware client can apply an explicit removal rule:

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("anthropic"),
    ai.WithRequestRules(core.AIProviderPatch{
        Name:    "new-claude-sampling-compatibility",
        Version: "1",
        Selector: core.AIProviderSelector{
            Provider: "anthropic",
            Surface:  "messages",
            Model:    "claude-next-*",
        },
        Remove: []string{"/temperature", "/top_p", "/top_k"},
    }),
)
```

Use this only after confirming the provider's model contract. Prefer upgrading
to a framework release with the family in its built-in policy so every caller
gets the same sync/stream behavior.

### Request Timeouts

By default, all AI providers use a **180-second (3 minute) timeout**. This accommodates reasoning models (GPT-5, o1, o3, o4) which take longer due to internal chain-of-thought processing.

#### Single Client Timeout

```go
import "time"

// Default timeout (180s) - sufficient for most use cases including reasoning models
client, err := ai.NewClient()

// Custom timeout for very complex tasks
client, err := ai.NewClient(
    ai.WithTimeout(300 * time.Second),  // 5 minutes
)

// Shorter timeout for latency-sensitive applications
client, err := ai.NewClient(
    ai.WithTimeout(60 * time.Second),   // 1 minute
)
```

#### Chain Client Timeout

```go
// Chain client with custom timeout applied to all providers
chainClient, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic"),
    ai.WithChainTimeout(240 * time.Second),  // 4 minutes
)
```

#### When to Adjust Timeout

| Scenario | Recommended Timeout |
|----------|---------------------|
| Simple queries with standard models (GPT-4, Claude) | 60s |
| Complex prompts with standard models | 120s |
| Reasoning models (GPT-5, o1, o3, o4) | 180s (default) |
| Orchestration/plan generation | 240-300s |
| Very complex multi-step reasoning | 300s+ |

### Reasoning Model Support

Reasoning controls are supported through `ReasoningEffort`, but support is provider- and model-dependent.

#### Reasoning Effort

```go
resp, err := client.GenerateResponse(ctx, prompt, &core.AIOptions{
    Model:           "gpt-5.4",
    ReasoningEffort: "none",   // or low, medium, high, xhigh
    MaxTokens:       2000,
})
```

Chain-wide default:

```go
chainClient, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic"),
    ai.WithChainReasoningEffort("none"),
)
```

Notes:

- Native `openai` supports `ReasoningEffort` directly.
- `openai.ollama` now allows reasoning controls to pass through as well.
- For unsupported OpenAI-compatible aliases, advanced reasoning fields may be stripped conservatively.
- Anthropic does not expose this portable control. A request-aware `ReasoningEffort` set returns `core.ErrAIRequestFeatureUnsupported` instead of being silently dropped.
- There is currently no env-var shortcut for `ReasoningEffort`; use code or orchestration overrides.

OpenAI reasoning models (GPT-5, o1, o3, o4) require special handling because they:
1. **Reject `max_tokens`** - Must use `max_completion_tokens` instead
2. **Reject `temperature`** - Only default value (1) is supported
3. **Count internal reasoning tokens** - Chain-of-thought tokens are counted but NOT returned

The framework handles #1 and #2 automatically. For #3, a **token multiplier** ensures sufficient tokens for both reasoning and visible output.

#### The Token Multiplier Problem

```
Without multiplier (2000 tokens requested):
┌────────────────────────────────────┐
│  Internal Reasoning (invisible)    │  ← Uses ~1800 tokens
├────────────────────────────────────┤
│  Visible Output (truncated!)       │  ← Only ~200 tokens remain
└────────────────────────────────────┘
Result: Empty or truncated responses

With 5x multiplier (10000 tokens allocated):
┌────────────────────────────────────┐
│  Internal Reasoning (invisible)    │  ← Uses ~4000 tokens
├────────────────────────────────────┤
│  Visible Output (complete!)        │  ← ~6000 tokens for response
└────────────────────────────────────┘
Result: Full, complete responses
```

#### Configuring Token Multiplier

```go
// Default 5x multiplier (recommended for most use cases)
client, err := ai.NewClient()

// Lower multiplier for cost optimization (simpler prompts)
client, err := ai.NewClient(
    ai.WithReasoningTokenMultiplier(3),  // 3x multiplier
)

// Higher multiplier for very complex reasoning tasks
client, err := ai.NewClient(
    ai.WithReasoningTokenMultiplier(8),  // 8x multiplier
)

// Chain client with custom multiplier
chainClient, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic"),
    ai.WithChainReasoningTokenMultiplier(4),  // 4x multiplier
)
```

#### When to Adjust Token Multiplier

| Scenario | Recommended Multiplier |
|----------|------------------------|
| Simple Q&A with reasoning models | 3x |
| Standard prompts | 5x (default) |
| Complex analysis or planning | 6-8x |
| Multi-step orchestration plans | 8x+ |

> **Note**: The token multiplier only affects OpenAI reasoning models (GPT-5, o1, o3, o4). Standard models (GPT-4, Claude, etc.) are unaffected.

### Orchestration Model Overrides

The orchestration module makes several types of internal LLM calls. Each
can be routed to a different model for cost/latency optimization:

| Call Type | Config Field | Factory Function | Env Var | Default |
|-----------|-------------|------------------|---------|---------|
| Plan generation | `PlanAIOptions.Model` | `WithPlanAIOptions()` | `TRUVAG3_PLAN_MODEL` | AIClient default |
| Synthesis (streaming + non-streaming) | `SynthesisAIOptions.Model` | `WithSynthesisAIOptions()` | `TRUVAG3_SYNTHESIS_MODEL` | AIClient default |
| Micro-resolution | `MicroResolutionAIOptions.Model` | `WithMicroResolutionAIOptions()` | `TRUVAG3_MICRO_RESOLUTION_MODEL` | AIClient default |
| Tiered selection | `TieredSelectionAIOptions.Model` | `WithTieredSelectionAIOptions()` | _(code only today)_ | AIClient default |
| Error analysis | `ErrorAnalysisAIOptions.Model` | `WithErrorAnalysisAIOptions()` | _(code only today)_ | AIClient default |
| Result distillation | `ResultDistillAIOptions.Model` | `WithResultDistillAIOptions()` | `TRUVAG3_RESULT_DISTILL_MODEL` | `fast` alias |

**Important: Use portable aliases with ChainClient.**

When your orchestrator uses a `ChainClient` (multi-provider failover),
model override values **must** be portable aliases (`"fast"`, `"default"`,
`"smart"`), not concrete model names.

**Why:** The ChainClient tries providers in order. If the model string is a
concrete name like `"gpt-4o-mini"`, a non-matching provider (e.g., Anthropic)
returns 404. The ChainClient classifies 404 as a non-retryable client error
and **stops immediately** — it does not try the next provider. This silently
breaks failover.

Portable aliases work because each provider resolves them independently:
- `"fast"` → Anthropic resolves to Haiku, OpenAI resolves to gpt-4.1-mini
- Failover works correctly since each provider gets a model it recognizes

**Example** — route each orchestration phase to an appropriate model tier:

```env
# Set orchestration model overrides to portable aliases
TRUVAG3_PLAN_MODEL=smart
TRUVAG3_SYNTHESIS_MODEL=default
TRUVAG3_MICRO_RESOLUTION_MODEL=fast
TRUVAG3_RESULT_DISTILL_MODEL=fast

# Optionally control which concrete model each alias maps to per-provider
TRUVAG3_ANTHROPIC_MODEL_SMART=claude-opus-4-6
TRUVAG3_OPENAI_MODEL_SMART=o3
TRUVAG3_ANTHROPIC_MODEL_FAST=claude-haiku-4-5
TRUVAG3_OPENAI_MODEL_FAST=gpt-4.1-mini
```

Or programmatically:

```go
orch, _ := orchestration.CreateOrchestratorWithOptions(deps,
    orchestration.WithPlanAIOptions(&orchestration.AIOptionsOverride{
        Model: orchestration.StringPtr("smart"),
    }),
    orchestration.WithSynthesisAIOptions(&orchestration.AIOptionsOverride{
        Model:           orchestration.StringPtr("default"),
        ReasoningEffort: orchestration.StringPtr("none"),
        Temperature:     orchestration.Float32Ptr(0.7),
    }),
    orchestration.WithMicroResolutionAIOptions(&orchestration.AIOptionsOverride{
        Model: orchestration.StringPtr("fast"),
    }),
    orchestration.WithTieredSelectionAIOptions(&orchestration.AIOptionsOverride{
        Model: orchestration.StringPtr("fast"),
    }),
)
```

Concrete model names are safe **only** with a single-provider `ai.NewClient()`:

```go
// Safe — single provider, no failover chain
client, _ := ai.NewClient(ai.WithProviderAlias("openai"))
orch, _ := orchestration.CreateOrchestratorWithOptions(deps,
    orchestration.WithPlanAIOptions(&orchestration.AIOptionsOverride{Model: orchestration.StringPtr("o3")}),
    orchestration.WithSynthesisAIOptions(&orchestration.AIOptionsOverride{Model: orchestration.StringPtr("gpt-4.1")}),
    orchestration.WithMicroResolutionAIOptions(&orchestration.AIOptionsOverride{Model: orchestration.StringPtr("gpt-4.1-mini")}),  // OK, no chain
)
```

---

## Quick Reference

### Environment Variable Cheat Sheet

**API Keys**:
```bash
OPENAI_API_KEY=sk-...
ANTHROPIC_API_KEY=sk-ant-...
GEMINI_API_KEY=...          # or GOOGLE_API_KEY (either activates Gemini)
DEEPSEEK_API_KEY=sk-...
GROQ_API_KEY=gsk-...
XAI_API_KEY=xai-...
MISTRAL_API_KEY=...
QWEN_API_KEY=...
TOGETHER_API_KEY=...
```

**Base URL Overrides**:
```bash
OPENAI_BASE_URL=https://...
DEEPSEEK_BASE_URL=https://...
GROQ_BASE_URL=https://...
XAI_BASE_URL=https://...
MISTRAL_BASE_URL=https://...
QWEN_BASE_URL=https://...
TOGETHER_BASE_URL=https://...
OLLAMA_BASE_URL=http://...
```

**Model Alias Overrides**:
```bash
# Pattern: TRUVAG3_{PROVIDER}_MODEL_{ALIAS}
TRUVAG3_OPENAI_MODEL_SMART=gpt-4.1
TRUVAG3_ANTHROPIC_MODEL_FAST=claude-haiku-4-5-20251001
TRUVAG3_GROQ_MODEL_DEFAULT=openai/gpt-oss-120b
TRUVAG3_OLLAMA_MODEL_DEFAULT=gemma4:26b
# For openai.deepseek, strip prefix → TRUVAG3_DEEPSEEK_MODEL_*
```

### Decision Tree: Which Client Type?

```
Do you need 99.9%+ uptime for AI features?
├── YES → Use Chain Client
│         └── Do you need deterministic provider order?
│             ├── YES → WithProviderChain("openai", "anthropic", ...)
│             └── NO  → Auto-detect (no WithProviderChain)
│                       └── Chain built from available API keys, sorted by priority
│
└── NO → Use Single Client
         └── Is cost a concern?
             ├── YES → Use openai.groq (free tier)
             └── NO → Use your preferred provider
```

### Error Classification Reference

Error classification uses the `core.ProviderError` interface for structured, type-safe decisions. The Chain Client calls `errors.As(err, &pe)` to extract the provider error and inspects its fields:

| `core.ProviderError` Field | Failover? | Rationale |
|-----------------------------|-----------|-----------|
| `StatusCode() == 401` | Yes | Different keys per provider |
| `StatusCode() == 403` | Yes | Permission scopes differ per provider |
| `StatusCode() >= 500` | Yes | Provider outage |
| `StatusCode() == 429` | Yes | Per-provider rate limits |
| `IsTransient() == true` (any status) | Yes | Proxy/CDN issue, not a request problem |
| Non-`ProviderError` (network error) | Yes | Connection/DNS issue |
| `StatusCode() == 400 && !IsTransient()` | **No** | Same input fails everywhere |
| `StatusCode() >= 400 && < 500` (other 4xx, not 401/403/429) | **No** | Fix your request |

**Implementation** (`ai/chain_client.go`):
```go
func isClientError(err error) bool {
    var pe core.ProviderError
    if errors.As(err, &pe) {
        if pe.IsTransient() { return false }          // Proxy errors → failover
        status := pe.StatusCode()
        return status >= 400 && status < 500 &&
            status != 401 && status != 403 && status != 429  // Auth/rate-limit → failover
    }
    return false  // Non-ProviderError (network) → failover
}
```

---

## See Also

- **[ai/README.md](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md)** - AI module overview and quick start
- **[ai/ARCHITECTURE.md](https://github.com/truvaagents/truva-g3/blob/main/ai/ARCHITECTURE.md)** - Technical architecture details
- **[CUSTOM_AI_PROVIDER_GUIDE.md](CUSTOM_AI_PROVIDER_GUIDE.md)** - Request-aware clients, policy, enterprise routing and credentials, custom factories, and codecs
- **[LOGGING_IMPLEMENTATION_GUIDE.md](../observability/LOGGING_IMPLEMENTATION_GUIDE.md)** - Logging patterns including AI module logging
- **[DISTRIBUTED_TRACING_GUIDE.md](../observability/DISTRIBUTED_TRACING_GUIDE.md)** - Tracing AI requests in Jaeger
