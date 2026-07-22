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
  - [Request-Aware and Custom Integrations](#request-aware-and-custom-integrations)
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
5. If all fail, returns one joined error containing every annotated entry
   failure (entry name and attempt number)

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

Bedrock participates only in binaries built with `-tags bedrock`. Ollama
auto-detection requires an explicitly configured loopback `OLLAMA_BASE_URL`
whose `/models` endpoint responds; use an explicit alias for a remote Ollama
endpoint.

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

| Alias | Type | Service | Credential configuration | Base URL / route configuration | Default URL |
|-------|------|---------|-----------------|------------------|-------------|
| `openai` | Native | OpenAI | `OPENAI_API_KEY` | `OPENAI_BASE_URL` | `https://api.openai.com/v1` |
| `anthropic` | Native | Anthropic Claude | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` | `https://api.anthropic.com/v1` |
| `gemini` | Native | Google Gemini | `GEMINI_API_KEY` or `GOOGLE_API_KEY` | `GEMINI_BASE_URL` | `https://generativelanguage.googleapis.com/v1beta` |
| `azureopenai.v1` | Hosted request-aware profile | Azure OpenAI v1 | `CredentialSource`, `WithAuthHeader`, or Azure `WithAPIKey` | Required `EndpointResolver` | Route-owned |
| `azureopenai.classic` | Hosted request-aware profile | Azure OpenAI classic | `CredentialSource`, `WithAuthHeader`, or Azure `WithAPIKey` | Required `EndpointResolver` | Route-owned |
| `anthropic.vertex` | Hosted request-aware profile | Claude on Vertex AI | Google `CredentialSource` or `WithAuthHeader` | Required `EndpointResolver` | Route-owned |
| `bedrock` | Native SDK | AWS Bedrock | AWS SDK default configuration or `WithAWSCredentials` | AWS region/configuration | Region-owned |
| `openai.groq` | OpenAI-compatible | Groq | `GROQ_API_KEY` | `GROQ_BASE_URL` | `https://api.groq.com/openai/v1` |
| `openai.deepseek` | OpenAI-compatible | DeepSeek | `DEEPSEEK_API_KEY` | `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` |
| `openai.xai` | OpenAI-compatible | xAI Grok | `XAI_API_KEY` | `XAI_BASE_URL` | `https://api.x.ai/v1` |
| `openai.mistral` | OpenAI-compatible | Mistral | `MISTRAL_API_KEY` | `MISTRAL_BASE_URL` | `https://api.mistral.ai/v1` |
| `openai.qwen` | OpenAI-compatible | Alibaba Qwen | `QWEN_API_KEY` | `QWEN_BASE_URL` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |
| `openai.together` | OpenAI-compatible | Together AI | `TOGETHER_API_KEY` | `TOGETHER_BASE_URL` | `https://api.together.xyz/v1` |
| `openai.ollama` | OpenAI-compatible | Ollama (local) | _(none)_ | `OLLAMA_BASE_URL` | `http://localhost:11434/v1` |

The Azure and Vertex aliases are request-aware-only, never auto-detected, and
do not accept a static base URL. Their resolver and credential recipes are in
[CUSTOM_AI_PROVIDER_GUIDE.md](CUSTOM_AI_PROVIDER_GUIDE.md#choose-a-hosted-cloud-recipe).

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

3. **Base URL overrides** (for a provider-documented endpoint or an internal proxy)
   ```bash
   GROQ_BASE_URL=https://ai-proxy.company.internal/groq/openai/v1
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
// This code works across providers that define this portable alias.
client, _ := ai.NewClient(
    ai.WithProviderAlias("openai"),  // or "anthropic", or "openai.groq"
    ai.WithModel("smart"),           // Resolves to the right model for each provider
)
```

### Standard Model Aliases

| Alias | Purpose | OpenAI | Anthropic | Gemini | Groq | DeepSeek |
|-------|---------|--------|-----------|--------|------|----------|
| `default` | General catalog choice | `gpt-4.1-mini` | `claude-sonnet-4-5-20250929` | `gemini-2.5-flash` | `openai/gpt-oss-120b` | `deepseek-chat` |
| `fast` | Latency-oriented catalog choice | `gpt-4.1-mini` | `claude-haiku-4-5-20251001` | `gemini-2.5-flash-lite` | `llama-3.1-8b-instant` | `deepseek-chat` |
| `smart` | Reasoning-oriented catalog choice | `o3` | `claude-sonnet-4-5-20250929` | `gemini-2.5-pro` | `openai/gpt-oss-120b` | `deepseek-reasoner` |
| `premium` | Highest-tier catalog choice | _(N/A)_ | `claude-opus-4-5-20251101` | `gemini-3-pro-preview` | _(N/A)_ | _(N/A)_ |
| `code` | Code-oriented catalog choice | `o3` | `claude-sonnet-4-5-20250929` | `gemini-2.5-pro` | `openai/gpt-oss-120b` | `deepseek-chat` |
| `vision` | Image-capable catalog choice | `gpt-4.1` | `claude-sonnet-4-5-20250929` | `gemini-2.5-flash` | _(N/A)_ | _(N/A)_ |

> **Note**: The `premium` alias is only available for Anthropic and Gemini.
> Other built-in catalogs use `smart` for their reasoning-oriented choice.

The table reports the aliases compiled into this branch, not a guarantee that
every model is enabled in every account or region. Provider lifecycle and
availability can change independently; verify the current provider docs and
override an alias with `TRUVAG3_<PROVIDER>_MODEL_<ALIAS>` when needed.

> **Provider lifecycle alert (verified July 22, 2026):** This branch still maps
> the DeepSeek aliases to `deepseek-chat` and `deepseek-reasoner`. DeepSeek will
> retire both model names after July 24, 2026 at 15:59 UTC and replaces them
> with `deepseek-v4-flash` and `deepseek-v4-pro`. Do not treat a model override
> as a complete migration: the framework capability catalog does not yet model
> V4's `thinking` object or its reasoning-effort contract. Update and unit-test
> the model catalog, capability rows, and request translation before using the
> V4 reasoning controls in production. See DeepSeek's
> [V4 announcement](https://api-docs.deepseek.com/news/news260424/) and
> [thinking-mode contract](https://api-docs.deepseek.com/guides/thinking_mode).
>
> The compiled Gemini `premium` mapping is also the legacy
> `gemini-3-pro-preview` name. Google shut that preview down on March 9, 2026
> and currently redirects the name to `gemini-3.1-pro-preview`; migrate the
> catalog or use an approved explicit replacement instead of relying on that
> redirect. See the official [Gemini release notes](https://ai.google.dev/gemini-api/docs/changelog).

Portable aliases are catalog-backed, not universal. The built-in OpenAI,
Anthropic, Gemini, and registered OpenAI-compatible aliases above resolve them;
Bedrock and an arbitrary custom provider do not unless that provider implements
equivalent resolution. In a failover chain, use an alias only when every entry
can resolve it.

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

# Do not override DeepSeek to V4 until the framework's V4 capability and
# thinking-mode translation has been updated and tested; see the alert above.
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
| Provider HTTP timeout (for example 408/504) | Retryable provider status | A different provider may remain healthy |

**Errors that STOP failover** (fails immediately):

| Error Type | `core.ProviderError` Classification | Why Failover Won't Help |
|------------|--------------------------------------|-------------------------|
| Bad request (400) | `StatusCode() == 400 && !IsTransient()` | Same input fails everywhere |
| Content policy | `StatusCode() == 400` with policy message | Same content fails everywhere |
| Malformed input | `StatusCode() == 400` with parse error | Structural issue in your code |
| Caller cancellation or context deadline | `errors.Is(err, context.Canceled)` or `context.DeadlineExceeded` | The caller's stop signal applies to the whole chain |

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
4. If ALL providers fail, ChainClient annotates every failure with its entry
   name and attempt, joins them with errors.Join(), and wraps the joined error
   with fmt.Errorf("...: %w", joined)
   ↓
5. Agent handler receives the error from orch.ProcessRequest()
   → errors.As(err, &pe) can find a matching ProviderError through the joined
     wrapping tree (do not assume that match is the final attempt)
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
ollama pull llama3.2

# Your .env file
OLLAMA_BASE_URL=http://localhost:11434/v1
TRUVAG3_OLLAMA_MODEL_DEFAULT=llama3.2
TRUVAG3_OLLAMA_MODEL_SMART=llama3.2
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
TRUVAG3_OLLAMA_MODEL_DEFAULT=llama3.2
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
- Use the exact model tag installed in your Ollama instance. If you use a
  different tag, set the alias override to that exact value.
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
GROQ_API_KEY=gsk-dev-key

# Use smaller/cheaper models in dev
TRUVAG3_GROQ_MODEL_DEFAULT=llama-3.1-8b-instant
TRUVAG3_GROQ_MODEL_SMART=llama-3.1-8b-instant
```

**Code**:
```go
// Use the provider selected for your development latency and cost target.
client, err := ai.NewClient(ai.WithProviderAlias("openai.groq"))
```

Groq exposes an OpenAI-compatible API and is often selected for low-latency
development. Quotas and prices are provider- and account-specific and change
independently of TruvaG3; confirm the current terms before choosing it for cost.

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

# Check context-aware framework/ai logs for:
# operation=ai_chain_provider_failed entry_name=openai error_type=credential
# operation=ai_chain_failover_success successful_entry=anthropic
```

### Scenario 4: Production with High Availability

**Goal**: Maximum uptime, automatic failover, production-approved models

**Setup**:
```bash
# .env.production
OPENAI_API_KEY=sk-prod-key
ANTHROPIC_API_KEY=sk-ant-prod-key
GROQ_API_KEY=gsk-prod-key

# Use the models approved for this production workload
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
# Alert on sustained increases in the exported ai.chain.failover counter.
# Exported Prometheus names depend on your OpenTelemetry collector/exporter
# normalization. The framework metric has bounded module/status/reason labels;
# identify the affected entry from correlated logs or spans, not a metric label.
```

### Scenario 5: Cost-Optimized Production

**Goal**: Minimize AI costs while maintaining quality

**Setup**:
```bash
# .env.production-cost-optimized
# Supply credentials for providers ordered below by your verified current cost
GROQ_API_KEY=gsk-key
OPENAI_API_KEY=sk-key
ANTHROPIC_API_KEY=sk-ant-key

# Use smaller models where acceptable
TRUVAG3_GROQ_MODEL_DEFAULT=llama-3.1-8b-instant
```

**Code**:
```go
// Example cost-oriented chain. Validate this order against current contracts,
// regions, quotas, and measured output quality before deployment.
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai.groq", "openai", "anthropic"),
)
```

**Cost monitoring**:
```go
// Log token usage per request for cost analysis
response, err := client.GenerateResponse(ctx, prompt, nil)
if err != nil {
    return fmt.Errorf("generate AI response: %w", err)
}
logger.Info("Request completed", map[string]interface{}{
    "model":  response.Model,           // Which model was used
    "tokens": response.Usage.TotalTokens,
})
// Chain entry tracking is available on ai.chain.provider_attempt spans and
// on the parent ai.chain.generate span's ai.chain.successful_entry attribute.
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
TRUVAG3_OLLAMA_MODEL_DEFAULT=your-approved-model:tag
TRUVAG3_OLLAMA_MODEL_SMART=your-approved-model:tag
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
// Track route use via sanitized framework/ai logs and traces.
// ai.chain.generate carries ai.chain.successful_entry; each
// ai.chain.provider_attempt span carries ai.chain.entry_name and status.
// Entry/provider names are intentionally not metric labels because they are
// operator-controlled and would create unbounded time series.
```

### Scenario 7: Multi-Region Deployment

**Goal**: Route to nearest provider, handle regional outages

**Setup**:
```bash
# .env.production-us
OPENAI_API_KEY=internal-us-token
OPENAI_BASE_URL=https://ai-gateway.us.company.internal/openai/v1
ANTHROPIC_API_KEY=sk-ant-backup

# .env.production-eu
OPENAI_API_KEY=internal-eu-token
OPENAI_BASE_URL=https://ai-gateway.eu.company.internal/openai/v1
ANTHROPIC_API_KEY=sk-ant-backup
```

**Code** (same for all regions):
```go
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic"),
)
```

**The key insight**: Same code, different environment variables per region.
Use only endpoints documented by the provider or owned by your organization;
do not infer a regional vendor hostname. For Azure OpenAI, Google Cloud OpenAI
compatibility, and Vertex-hosted Claude, use the validated resolver recipes in
[CUSTOM_AI_PROVIDER_GUIDE.md](CUSTOM_AI_PROVIDER_GUIDE.md#choose-a-hosted-cloud-recipe).

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

**Symptom**: Auto-detected `NewChainClient` returns "no providers detected
(check API keys)", or an explicit chain cannot construct any entry.

**Cause**: Auto-detection found no configured provider. In an explicit chain,
construction fails only when entries cannot be materialized (for example, an
unregistered alias or invalid endpoint configuration). Most direct HTTP
providers do not preflight a missing API key during construction; that error is
returned when the entry is first called and can then fail over.

**Diagnosis**:
```bash
# Check which env vars are set
echo "OPENAI_API_KEY: ${OPENAI_API_KEY:-(not set)}"
echo "ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY:-(not set)}"
echo "GROQ_API_KEY: ${GROQ_API_KEY:-(not set)}"
```

**Fix**: For auto-detection, set at least one provider credential (or configure
and start loopback Ollama):
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

**Fix**: Use portable aliases like `default`, `fast`, or `smart` only when every
entry in the failover chain defines that alias. Bedrock and arbitrary custom
providers require an explicit strategy because they do not inherit the
built-in OpenAI/Anthropic/Gemini catalogs.

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

**Successful provider response**:
```json
{
  "component": "framework/ai",
  "level": "INFO",
  "message": "AI response received",
  "operation": "ai_response",
  "provider": "openai",
  "provider_alias": "openai",
  "model": "o3",
  "prompt_tokens": 150,
  "completion_tokens": 200,
  "total_tokens": 350,
  "status": "success",
  "duration_ms": 1250
}
```

**Failover event**:
```json
{
  "component": "framework/ai",
  "level": "WARN",
  "message": "Provider failed in chain, trying next",
  "operation": "ai_chain_provider_failed",
  "entry_name": "openai",
  "attempt": 1,
  "remaining": 1,
  "failover_reason": "auth",
  "error": "AI provider request failed: provider_client",
  "error_type": "provider_client"
}
```

**All providers exhausted**:
```json
{
  "component": "framework/ai",
  "level": "ERROR",
  "message": "All chain providers exhausted",
  "operation": "ai_chain_exhausted",
  "entries_tried": 3,
  "failed_entries": ["openai", "anthropic", "openai.groq"],
  "error": "AI provider request failed: provider_rate_limit",
  "error_type": "provider_rate_limit"
}
```

These observation errors are deliberately sanitized; the original typed error
is still returned to the caller. `request_id` is included when the supplied
context carries it (framework handlers and orchestration normally seed it).

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

### Request-Aware and Custom Integrations

There are two separate decisions here:

1. **How much request intent must TruvaG3 preserve?** The ordinary API carries
   portable values such as model, temperature, and max tokens. The
   request-aware API can also say "send zero," "omit this field," or "apply
   this named policy."
2. **Who owns the provider protocol?** A built-in provider can still be used
   with enterprise credentials and routing. You need a custom provider only
   when TruvaG3 does not already own the correct provider identity, wire
   format, SDK integration, or validation rules.

Request-aware does **not** automatically mean custom:

```text
Ordinary built-in call
Prompt + AIOptions
        └── built-in provider

Advanced built-in call
AIRequest + policy/routing/credentials
        └── the same built-in provider

New provider integration
AIRequest + policy/routing/credentials
        └── your provider factory + adapter
```

Most applications should stay with the first path. Move to the second or third
only when the request or integration has behavior that the ordinary setup
cannot preserve:

| Requirement | Use |
|---|---|
| Built-in provider, portable generation options, or homogeneous failover | `NewClient` or `NewChainClient`; continue using this guide |
| Explicit zero or omission, request policy, rotating credentials, or per-request routing | `NewRequestClient` |
| Independently configured or caller-owned failover entries | `NewChain` |
| New provider identity, custom wire contract, or SDK-native adapter | A custom request-aware provider factory |

Here are three concrete examples:

- **You use OpenAI with a normal API key:** stay with `NewClient`.
- **Your company routes OpenAI through regional gateways and issues a new token
  for every attempt:** keep the built-in OpenAI provider and use
  `NewRequestClient` with routing and credential hooks.
- **Your company has an internal model platform with its own SDK and response
  format:** implement a custom request-aware provider.

`NewRequestClient` accepts existing options such as `WithProvider`,
`WithModel`, and `WithTimeout` because every `AIOption` also satisfies
`ClientOption`:

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

Request-aware built-in construction currently supports direct OpenAI and
Anthropic, Azure OpenAI (`azureopenai.v1` and `azureopenai.classic`),
Google-hosted Claude (`anthropic.vertex`), and Bedrock when built with
`-tags bedrock`. The Azure and Vertex profiles are request-aware-only and
require endpoint and credential sources; they do not auto-detect. Gemini
continues to use the legacy API. The legacy `NewClient`, `GenerateResponse`,
and `NewChainClient` APIs remain supported for existing and simple portable
calls.

The
[Custom AI Providers and Enterprise Integration Guide](CUSTOM_AI_PROVIDER_GUIDE.md)
starts with the second example and shows when it becomes necessary to move to
the third. It explains policy and middleware, enterprise credential and route
hooks, heterogeneous chains, custom factories, reusable OpenAI-compatible
codecs, retry-body requirements, and semantic cache fingerprints.

### Anthropic Sampling Compatibility

Some Anthropic model families reject explicit sampling controls. The Anthropic
adapter resolves the model alias or environment override first, then applies a
model-family policy shared by sync and streaming requests. For the restricted
families currently listed in
[`ai/providers/anthropic/request_policy.go`](../../ai/providers/anthropic/request_policy.go),
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

Clients constructed through TruvaG3's built-in provider factories default to a
**180-second (3 minute) timeout**. The longer default accommodates model
families whose responses can take substantially longer than ordinary chat
requests. Application-defined clients and injected chain entries retain their
own timeout behavior.

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

Chain clients default each entry to **0 in-provider retries** because moving to
the next entry is already the retry mechanism. `NewClient` defaults to 3
transport retries. Use `ai.WithChainMaxRetries(n)` only when you intentionally
want each entry to retry before the chain advances.

If code does not supply an explicit retry option, a positive
`TRUVAG3_AI_RETRY_ATTEMPTS` value overrides the relevant default for both
client types. Explicit `WithMaxRetries(n)` or `WithChainMaxRetries(n)` wins over
the environment. Zero, negative, and non-integer environment values are
ignored; disable single-client retries explicitly with `WithMaxRetries(0)`.

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

Concrete model names are normally appropriate for a single-provider
`ai.NewClient()`. In a chain they are safe only when every entry accepts that
same literal model ID:

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
ANTHROPIC_BASE_URL=https://...
GEMINI_BASE_URL=https://...
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
TRUVAG3_OLLAMA_MODEL_DEFAULT=llama3.2
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
             ├── YES → Compare current provider pricing, quotas, and measured quality
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
| `errors.Is(err, context.Canceled)` | **No** | Caller canceled the whole operation |
| `errors.Is(err, context.DeadlineExceeded)` | **No** | Caller deadline applies to the whole operation |
| `StatusCode() == 400 && !IsTransient()` | **No** | Same input fails everywhere |
| `StatusCode() >= 400 && < 500` (other 4xx, not 401/403/429) | **No** | Fix your request |

**Implementation** (`ai/chain_request.go` and `ai/chain_client.go`, simplified):
```go
func shouldFailOver(err error) bool {
    if err == nil || errors.Is(err, context.Canceled) ||
        errors.Is(err, context.DeadlineExceeded) {
        return false
    }
    return !isClientError(err)
}

func isClientError(err error) bool {
    var pe core.ProviderError
    if errors.As(err, &pe) {
        if pe.IsTransient() { return false }          // Proxy errors → failover
        status := pe.StatusCode()
        return status >= 400 && status < 500 &&
            status != 401 && status != 403 && status != 429  // Auth/rate-limit → failover
    }
    return false // Unstructured/network error → failover
}
```

---

## See Also

- **[ai/README.md](../../ai/README.md)** - AI module overview and quick start
- **[ai/ARCHITECTURE.md](../../ai/ARCHITECTURE.md)** - Technical architecture details
- **[CUSTOM_AI_PROVIDER_GUIDE.md](CUSTOM_AI_PROVIDER_GUIDE.md)** - Request-aware clients, policy, enterprise routing and credentials, custom factories, and codecs
- **[AI_PROVIDER_CHANGE_PLAYBOOK.md](AI_PROVIDER_CHANGE_PLAYBOOK.md)** - Day-0 responses when providers change: broken parameter contracts, new models and providers, auth/endpoint churn, cache safety
- **[LOGGING_IMPLEMENTATION_GUIDE.md](../observability/LOGGING_IMPLEMENTATION_GUIDE.md)** - Logging patterns including AI module logging
- **[DISTRIBUTED_TRACING_GUIDE.md](../observability/DISTRIBUTED_TRACING_GUIDE.md)** - Tracing AI requests in Jaeger
