# TruvaG3 Framework Environment Variables Guide

This document provides a comprehensive reference for all environment variables supported by the TruvaG3 framework. Variables are organized by module/functionality with their default values, descriptions, and verification status.

## Important Notes

**Variable Status Legend:**
- **Implemented** - Variable is actively read via `os.Getenv()` and works
- **Struct Tag Only** - Defined in struct tags but NOT currently loaded (requires code changes to work)
- **Example Only** - Used in example applications, not core framework

## Table of Contents

1. [Kubernetes Deployment Requirements](#kubernetes-deployment-requirements) *(Start here for K8s)*
2. [Core Configuration](#core-configuration)
3. [HTTP Server Configuration](#http-server-configuration)
4. [CORS Configuration](#cors-configuration)
5. [Discovery Configuration](#discovery-configuration)
6. [AI Configuration](#ai-configuration)
7. [AI Provider-Specific Variables](#ai-provider-specific-variables)
8. [Telemetry Configuration](#telemetry-configuration)
9. [Memory Configuration](#memory-configuration)
10. [Shared Memory Configuration](#shared-memory-configuration)
11. [Activity Coordination Configuration](#activity-coordination-configuration)
12. [Logging Configuration](#logging-configuration)
13. [Development Configuration](#development-configuration)
14. [Kubernetes Configuration](#kubernetes-configuration)
15. [Orchestration Configuration](#orchestration-configuration)
16. [LLM Debug Configuration](#llm-debug-configuration)
17. [Execution Debug Store Configuration](#execution-debug-store-configuration)
18. [Human-in-the-Loop (HITL) Configuration](#human-in-the-loop-hitl-configuration)
19. [Async Task Configuration](#async-task-configuration)
20. [Prompt Configuration](#prompt-configuration)
21. [Quick Reference Table](#quick-reference-table)

---

## Kubernetes Deployment Requirements

This section provides a quick reference for deploying TruvaG3 agents and tools in Kubernetes. Variables are categorized by requirement level to help you configure deployments correctly.

> **Working Examples**: See [agent-example/k8-deployment.yaml](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-example/k8-deployment.yaml) for a complete agent deployment and [tool-example/k8-deployment.yaml](https://github.com/truvaagents/truva-g3/blob/main/examples/tool-example/k8-deployment.yaml) for a complete tool deployment.

### Required Variables (All Deployments)

These variables are **mandatory** for proper operation in Kubernetes:

| Variable | Why Required | How to Set |
|----------|--------------|------------|
| `TRUVAG3_AGENT_NAME` | Validation fails without it ([core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) — `if c.Name == ""` check in `Validate`) | Static value |
| `REDIS_URL` | Required when discovery enabled (auto-enabled in K8s) | ConfigMap or static value |
| `TRUVAG3_K8S_SERVICE_NAME` | **Critical** for service-fronted discovery URL registration | Must match your K8s Service name |
| `TRUVAG3_K8S_SERVICE_PORT` | Service port for discovery URL (default: 80) | Must match your K8s Service port |

### Required Variables (via fieldRef)

These are populated automatically by Kubernetes using `fieldRef`:

| Variable | fieldRef Source | Purpose |
|----------|-----------------|---------|
| `TRUVAG3_K8S_NAMESPACE` | `metadata.namespace` | Pod namespace for service URL construction |
| `TRUVAG3_K8S_POD_IP` | `status.podIP` | Pod IP address for health checks |
| `TRUVAG3_K8S_NODE_NAME` | `spec.nodeName` | Node placement info for debugging |

### Required for AI Agents Only

Tools don't need AI configuration, but agents using AI features require:

| Variable | Purpose |
|----------|---------|
| `OPENAI_API_KEY` | Or any other AI provider key (see [AI Provider-Specific Variables](#ai-provider-specific-variables)) |

### Strongly Recommended

| Variable | Default | Why Recommended |
|----------|---------|-----------------|
| `TRUVAG3_DISCOVERY_RETRY` | `false` | Set to `true` to handle Redis startup race conditions |
| `TRUVAG3_DISCOVERY_RETRY_INTERVAL` | `30s` | Background retry interval when Redis unavailable |
| `TRUVAG3_DEV_MODE` | `false` | Explicitly disable for production |

### Auto-Detected (No Need to Set)

| Variable | Auto-Detection |
|----------|----------------|
| `KUBERNETES_SERVICE_HOST` | Set by K8s automatically, triggers K8s mode |
| `HOSTNAME` | Set by K8s to pod name |
| `TRUVAG3_ADDRESS` | Auto-set to `0.0.0.0` in K8s mode |
| `TRUVAG3_LOG_FORMAT` | Auto-set to `json` in K8s mode |

### Minimal Tool Deployment

```yaml
env:
  # REQUIRED - Core Identity
  - name: TRUVAG3_AGENT_NAME
    value: "weather-tool"

  # REQUIRED - Service Discovery
  - name: REDIS_URL
    value: "redis://redis.truvag3-examples:6379"

  # REQUIRED - Service-Fronted Discovery (must match Service definition)
  - name: TRUVAG3_K8S_SERVICE_NAME
    value: "weather-tool-service"
  - name: TRUVAG3_K8S_SERVICE_PORT
    value: "80"

  # REQUIRED - K8s Metadata (via fieldRef)
  - name: TRUVAG3_K8S_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
  - name: TRUVAG3_K8S_POD_IP
    valueFrom:
      fieldRef:
        fieldPath: status.podIP
  - name: TRUVAG3_K8S_NODE_NAME
    valueFrom:
      fieldRef:
        fieldPath: spec.nodeName

  # RECOMMENDED - Resilience
  - name: TRUVAG3_DISCOVERY_RETRY
    value: "true"
  - name: TRUVAG3_DISCOVERY_RETRY_INTERVAL
    value: "30s"
```

### Minimal Agent Deployment (with AI)

```yaml
env:
  # Same as Tool (above), plus:

  # REQUIRED - AI Provider Key
  - name: OPENAI_API_KEY
    valueFrom:
      secretKeyRef:
        name: ai-provider-keys
        key: OPENAI_API_KEY

  # OPTIONAL - Telemetry
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://otel-collector.truvag3-examples:4318"
```

### Common Mistakes to Avoid

1. **Missing `TRUVAG3_K8S_SERVICE_NAME`**: Without this, discovery registers the wrong URL and other services can't find your tool/agent.

2. **Mismatched Service Name/Port**: The `TRUVAG3_K8S_SERVICE_NAME` must exactly match your Kubernetes Service's `metadata.name`, and `TRUVAG3_K8S_SERVICE_PORT` must match the Service's `port` (not `targetPort`).

3. **Setting redundant variables**: You don't need both `REDIS_URL` and `TRUVAG3_REDIS_URL` - the framework checks both (with `TRUVAG3_REDIS_URL` taking precedence).

4. **Setting `TRUVAG3_ADDRESS`**: This is auto-detected as `0.0.0.0` in K8s - no need to set it.

---

## Core Configuration

These variables configure the fundamental settings of a TruvaG3 agent or tool.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_AGENT_NAME` | `truvag3-agent` | **Implemented** | Name of the agent | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AGENT_ID` | (auto-generated) | **Implemented** | Unique identifier for the agent instance | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_PORT` | `8080` | **Implemented** | HTTP server port | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_ADDRESS` | `localhost` (local) / `0.0.0.0` (K8s) | **Implemented** | Bind address for the HTTP server | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_NAMESPACE` | `default` | **Implemented** | Logical namespace for multi-tenancy | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

### Example

```bash
export TRUVAG3_AGENT_NAME="weather-tool"
export TRUVAG3_PORT=8085
export TRUVAG3_NAMESPACE="production"
```

---

## HTTP Server Configuration

Configure HTTP server timeouts and health check settings.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_HTTP_READ_TIMEOUT` | `30s` | **Implemented** | Maximum duration for reading the entire request | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_HTTP_WRITE_TIMEOUT` | `30s` | **Implemented** | Maximum duration for writing the response | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_HTTP_READ_HEADER_TIMEOUT` | `10s` | Struct Tag Only | Maximum duration for reading request headers | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_HTTP_IDLE_TIMEOUT` | `120s` | Struct Tag Only | Maximum duration to wait for the next request | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_HTTP_MAX_HEADER_BYTES` | `1048576` (1MB) | Struct Tag Only | Maximum size of request headers | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_HTTP_SHUTDOWN_TIMEOUT` | `10s` | Struct Tag Only | Graceful shutdown timeout | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_HTTP_HEALTH_CHECK` | `true` | Struct Tag Only | Enable health check endpoint | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_HTTP_HEALTH_PATH` | `/health` | Struct Tag Only | Path for health check endpoint | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

### Example

```bash
# For long-running AI workflows (these are implemented)
export TRUVAG3_HTTP_READ_TIMEOUT="5m"
export TRUVAG3_HTTP_WRITE_TIMEOUT="5m"
```

---

## CORS Configuration

Configure Cross-Origin Resource Sharing settings.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_CORS_ENABLED` | `false` | **Implemented** | Enable CORS support | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_CORS_ORIGINS` | (none) | **Implemented** | Comma-separated list of allowed origins | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_CORS_METHODS` | `GET,POST,PUT,DELETE,OPTIONS` | **Implemented** | Allowed HTTP methods | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_CORS_HEADERS` | `Content-Type,Authorization` | **Implemented** | Allowed request headers | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_CORS_CREDENTIALS` | `false` | **Implemented** | Allow credentials (cookies, auth headers) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_CORS_EXPOSED_HEADERS` | (none) | Struct Tag Only | Headers exposed to the browser | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_CORS_MAX_AGE` | `86400` (24h) | Struct Tag Only | Preflight cache duration in seconds | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

### Example

```bash
export TRUVAG3_CORS_ENABLED=true
export TRUVAG3_CORS_ORIGINS="https://app.example.com,https://*.example.com"
export TRUVAG3_CORS_CREDENTIALS=true
```

---

## Discovery Configuration

Configure service discovery for agent/tool registration and lookup.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_DISCOVERY_ENABLED` | `false` (local) / `true` (K8s) | **Implemented** | Enable service discovery | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_DISCOVERY_PROVIDER` | `redis` | **Implemented** | Discovery backend provider | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_REDIS_URL` | `redis://localhost:6379` | **Implemented** | Redis connection URL (takes precedence) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `REDIS_URL` | (fallback) | **Implemented** | Standard Redis URL (fallback if TRUVAG3_REDIS_URL not set) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_DISCOVERY_CACHE` | `true` | **Implemented** | Enable local caching of discovery results | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_DISCOVERY_RETRY` | `false` | **Implemented** | Enable background retry on initial connection failure | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_DISCOVERY_RETRY_INTERVAL` | `30s` | **Implemented** | Starting retry interval (increases exponentially) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_DISCOVERY_TTL` | `30s` | **Implemented** | Registration TTL for service keys (min 5s) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_DISCOVERY_HEARTBEAT` | `0` (= TTL/2) | **Implemented** | Heartbeat interval for registration refresh (min 2s) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_DISCOVERY_CACHE_TTL` | `5m` | Struct Tag Only | Cache time-to-live | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

### Variable Precedence

For Redis URL, the precedence order is:
1. Explicit configuration via `WithRedisURL()`
2. `TRUVAG3_REDIS_URL` environment variable
3. `REDIS_URL` environment variable
4. Default based on environment detection

### Example

```bash
export REDIS_URL="redis://redis:6379"
export TRUVAG3_DISCOVERY_CACHE=true
export TRUVAG3_DISCOVERY_RETRY=true
export TRUVAG3_DISCOVERY_RETRY_INTERVAL="30s"
export TRUVAG3_DISCOVERY_TTL="60s"
export TRUVAG3_DISCOVERY_HEARTBEAT="20s"
```

**Code equivalent** (functional options override env vars):

```go
core.NewConfig(
    core.WithRedisDiscovery("redis://redis:6379"),
    core.WithDiscoveryCacheEnabled(true),
    core.WithDiscoveryTTL(60 * time.Second),
    core.WithHeartbeatInterval(20 * time.Second),
)
```

---

## AI Configuration

Configure AI client settings for LLM integration.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_AI_ENABLED` | `false` | **Implemented** | Enable AI features | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AI_API_KEY` | (none) | **Implemented** | API key for the provider (auto-enables AI) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `OPENAI_API_KEY` | (fallback) | **Implemented** | Fallback API key (auto-enables AI) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AI_MODEL` | `gpt-4` | **Implemented** | Model name to use | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AI_BASE_URL` | Provider-specific | **Implemented** | Custom base URL for API calls | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AI_PROVIDER` | `openai` | Struct Tag Only | AI provider | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AI_TEMPERATURE` | `0.7` | Struct Tag Only | Sampling temperature (0.0-2.0) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AI_MAX_TOKENS` | `2000` | Struct Tag Only | Maximum tokens in response | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AI_TIMEOUT` | `180s` | Struct Tag Only | Request timeout (3 min default for reasoning model support) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AI_RETRY_ATTEMPTS` | `3` (single client) / `0` (chain client) | **Implemented** | Per-provider HTTP retry budget for AI API calls. Honored by both `ai.NewClient` and `ai.NewChainClient`, with different defaults: single clients absorb transient blips with 3 retries (no failover layer below), chain clients default to 0 because the chain's failover loop is the retry mechanism. Precedence: explicit `WithMaxRetries(n)` / `WithChainMaxRetries(n)` > `TRUVAG3_AI_RETRY_ATTEMPTS` > default. Per FRAMEWORK_DESIGN_PRINCIPLES §3.5 rule 3, env var values are guarded with `val > 0` — zero, negative, and non-integer values are silently rejected and fall through to the default. To explicitly disable retries on a single client, use `ai.WithMaxRetries(0)` programmatically. | [ai/client.go](https://github.com/truvaagents/truva-g3/blob/main/ai/client.go), [ai/chain_client.go](https://github.com/truvaagents/truva-g3/blob/main/ai/chain_client.go), [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AI_RETRY_DELAY` | `1s` | Struct Tag Only | Delay between retries | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

### Example

```bash
export TRUVAG3_AI_ENABLED=true
export TRUVAG3_AI_MODEL="gpt-4-turbo"
export TRUVAG3_AI_BASE_URL="https://api.openai.com/v1"
```

---

## AI Provider-Specific Variables

The framework supports multiple AI providers with automatic detection based on available API keys.

### OpenAI (Priority: 1000)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `OPENAI_API_KEY` | (none) | **Implemented** | OpenAI API key | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | **Implemented** | OpenAI API base URL | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |

### Anthropic Claude (Priority: 900)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `ANTHROPIC_API_KEY` | (none) | **Implemented** | Anthropic API key | [ai/providers/anthropic/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/anthropic/factory.go) |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | **Implemented** | Anthropic API base URL | [ai/providers/anthropic/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/anthropic/factory.go) |

### Google Gemini (Priority: 800)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `GEMINI_API_KEY` | (none) | **Implemented** | Google Gemini API key | [ai/providers/gemini/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/gemini/factory.go) |
| `GOOGLE_API_KEY` | (fallback) | **Implemented** | Alternative Google API key (either activates Gemini) | [ai/providers/gemini/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/gemini/factory.go) |
| `GEMINI_BASE_URL` | `https://generativelanguage.googleapis.com` | **Implemented** | Gemini API base URL | [ai/providers/gemini/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/gemini/factory.go) |

### Groq (Priority: 700)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `GROQ_API_KEY` | (none) | **Implemented** | Groq API key | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |
| `GROQ_BASE_URL` | `https://api.groq.com/openai/v1` | **Implemented** | Groq API base URL | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |

### DeepSeek (Priority: 600)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `DEEPSEEK_API_KEY` | (none) | **Implemented** | DeepSeek API key | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |
| `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | **Implemented** | DeepSeek API base URL | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |

### xAI Grok (Priority: 500)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `XAI_API_KEY` | (none) | **Implemented** | xAI API key | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |
| `XAI_BASE_URL` | `https://api.x.ai/v1` | **Implemented** | xAI API base URL | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |

### Mistral AI (Priority: 450)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `MISTRAL_API_KEY` | (none) | **Implemented** | Mistral AI API key | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |
| `MISTRAL_BASE_URL` | `https://api.mistral.ai/v1` | **Implemented** | Mistral AI API base URL | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |

### Alibaba Qwen (Priority: 400)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `QWEN_API_KEY` | (none) | **Implemented** | Qwen API key | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |
| `QWEN_BASE_URL` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | **Implemented** | Qwen API base URL | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |

### Together AI (Priority: 300)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TOGETHER_API_KEY` | (none) | **Implemented** | Together AI API key | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |
| `TOGETHER_BASE_URL` | `https://api.together.xyz/v1` | **Implemented** | Together AI API base URL | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |

### Ollama (Priority: 100 - Local)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `OLLAMA_BASE_URL` | `http://localhost:11434/v1` | **Implemented** | Ollama local server URL (must be explicitly set to activate detection) | [ai/providers/openai/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/factory.go) |

> **Note**: Ollama is only auto-detected when `OLLAMA_BASE_URL` is explicitly set. The framework does not probe `localhost:11434` by default to avoid a 2-second timeout penalty in environments where Ollama is not running.

### AWS Bedrock (Priority: 200)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `AWS_ACCESS_KEY_ID` | (none) | **Implemented** | AWS access key | [ai/providers/bedrock/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/bedrock/factory.go) |
| `AWS_SECRET_ACCESS_KEY` | (none) | **Implemented** | AWS secret key | [ai/providers/bedrock/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/bedrock/factory.go) |
| `AWS_SESSION_TOKEN` | (none) | **Implemented** | AWS session token (temporary credentials) | [ai/providers/bedrock/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/bedrock/factory.go) |
| `AWS_REGION` | `us-east-1` | **Implemented** | AWS region | [ai/providers/bedrock/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/bedrock/factory.go) |
| `AWS_DEFAULT_REGION` | (fallback) | **Implemented** | Alternative region variable | [ai/providers/bedrock/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/bedrock/factory.go) |
| `AWS_PROFILE` | (none) | **Implemented** | AWS CLI profile name | [ai/providers/bedrock/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/bedrock/factory.go) |
| `AWS_EXECUTION_ENV` | (auto) | **Implemented** | Set by AWS Lambda | [ai/providers/bedrock/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/bedrock/factory.go) |
| `AWS_LAMBDA_FUNCTION_NAME` | (auto) | **Implemented** | Set in Lambda environment | [ai/providers/bedrock/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/bedrock/factory.go) |
| `AWS_CONTAINER_CREDENTIALS_RELATIVE_URI` | (auto) | **Implemented** | Set in ECS environment | [ai/providers/bedrock/factory.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/bedrock/factory.go) |

### Auto-Detection Priority

When no explicit provider is specified, the framework auto-detects available providers:

1. **OpenAI** (1000) - `OPENAI_API_KEY`
2. **Anthropic** (900) - `ANTHROPIC_API_KEY`
3. **Gemini** (800) - `GEMINI_API_KEY` or `GOOGLE_API_KEY`
4. **Groq** (700) - `GROQ_API_KEY`
5. **DeepSeek** (600) - `DEEPSEEK_API_KEY`
6. **xAI** (500) - `XAI_API_KEY`
7. **Mistral** (450) - `MISTRAL_API_KEY`
8. **Qwen** (400) - `QWEN_API_KEY`
9. **Together AI** (300) - `TOGETHER_API_KEY`
10. **Bedrock** (200) - AWS credentials
11. **Ollama** (100) - Requires `OLLAMA_BASE_URL` to be explicitly set

### Overriding Auto-Detection Priority

The priority values above are **hardcoded defaults** used only during auto-detection (when calling `ai.NewClient()` without specifying a provider). Developers can override this behavior in several ways:

**1. Explicit Provider Selection** - Bypasses auto-detection entirely:
```go
// Use Anthropic regardless of OpenAI having higher priority
client, _ := ai.NewClient(ai.WithProvider("anthropic"))
```

**2. Provider Aliases** - Select specific OpenAI-compatible services:
```go
// Use DeepSeek even if OPENAI_API_KEY is also set
client, _ := ai.NewClient(ai.WithProviderAlias("openai.deepseek"))
```

**3. Chain Client** - Define your own failover order:
```go
// Your order: Groq → DeepSeek → OpenAI (ignores default priorities)
client, _ := ai.NewChainClient(
    ai.WithProviderChain("openai.groq", "openai.deepseek", "openai"),
)
```

**4. Custom Provider with Higher Priority** - Become the default:
```go
func (p *CustomProvider) DetectEnvironment() (priority int, available bool) {
    if os.Getenv("CUSTOM_LLM_KEY") != "" {
        return 200, true  // Higher than OpenAI's 100
    }
    return 0, false
}
```

> **Note**: There is no environment variable to change the priority order. To use a specific provider, use explicit selection or provider aliases in your code. See [AI Module README](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md) for detailed examples.

---

## Telemetry Configuration

Configure OpenTelemetry-based observability (metrics and tracing).

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_TELEMETRY_ENABLED` | `false` | **Implemented** | Enable telemetry collection | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_TELEMETRY_ENDPOINT` | (none) | **Implemented** | OTLP receiver endpoint (auto-enables telemetry) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (fallback) | **Implemented** | Standard OTEL endpoint variable (auto-enables telemetry) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_TELEMETRY_SERVICE_NAME` | Agent name | **Implemented** | Service name for traces/metrics | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `OTEL_SERVICE_NAME` | (fallback) | **Implemented** | Standard OTEL service name | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_TELEMETRY_PROVIDER` | `otel` | Struct Tag Only | Telemetry provider | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_TELEMETRY_METRICS` | `true` | Struct Tag Only | Enable metrics collection | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_TELEMETRY_TRACING` | `true` | Struct Tag Only | Enable distributed tracing | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_TELEMETRY_SAMPLING_RATE` | `1.0` | Struct Tag Only | Trace sampling rate (0.0-1.0) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_TELEMETRY_INSECURE` | `true` | Struct Tag Only | Use insecure connection (no TLS) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

### Telemetry Logger Variables

These are used by the telemetry module's internal logger:

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_LOG_LEVEL` | `INFO` | **Implemented** | Log level for telemetry logger | [telemetry/logger.go](https://github.com/truvaagents/truva-g3/blob/main/telemetry/logger.go) |
| `TRUVAG3_DEBUG` | `false` | **Implemented** | Enable debug logging | [telemetry/logger.go](https://github.com/truvaagents/truva-g3/blob/main/telemetry/logger.go) |
| `TELEMETRY_DEBUG` | `false` | **Implemented** | Enable telemetry-specific debug | [telemetry/logger.go](https://github.com/truvaagents/truva-g3/blob/main/telemetry/logger.go) |
| `TRUVAG3_LOG_FORMAT` | `text` (local) / `json` (K8s) | **Implemented** | Log format override | [telemetry/logger.go](https://github.com/truvaagents/truva-g3/blob/main/telemetry/logger.go) |
| `KUBERNETES_SERVICE_HOST` | (auto) | **Implemented** | Auto-detect K8s for JSON logging | [telemetry/logger.go](https://github.com/truvaagents/truva-g3/blob/main/telemetry/logger.go) |

### Example

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT="http://otel-collector:4318"
export OTEL_SERVICE_NAME="weather-tool"
```

---

## Memory Configuration

Configure state storage for agents.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_MEMORY_MAX_SIZE` | `1000` | Reserved (struct tag only) | Reserved for future use; not yet consumed at runtime. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) — `MemoryConfig.MaxSize` |
| `TRUVAG3_MEMORY_DEFAULT_TTL` | `1h` | Reserved (struct tag only) | Reserved for future use; not yet consumed at runtime. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) — `MemoryConfig.DefaultTTL` |
| `TRUVAG3_MEMORY_CLEANUP_INTERVAL` | `10m` | **Implemented** | Sweep interval for the in-process `*core.MemoryStore` eviction sweeper, used by `Framework.AutoRegisterMemorySweeper()` and tools that register `core.NewMemoryStoreSweeper(...)` directly. Format: `time.Duration` (e.g. `5m`, `30s`). | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) `LoadFromEnv` |

**Removed env vars** (no longer parsed as of the InMemoryStore cleanup): `TRUVAG3_MEMORY_PROVIDER`, `TRUVAG3_MEMORY_REDIS_URL`. The framework only ships one `Memory` implementation today (`*core.MemoryStore`, in-process); the prior "redis" provider option was unimplemented. To use Redis-backed memory, inject a custom `core.Memory` implementation by setting `agent.Memory = redisStore` before calling `core.NewFramework`.

---

## Shared Memory Configuration

Configure cross-agent shared memory — episodic events, knowledge extraction, activity compaction, and digest caching. Shared memory enables agents in the same domain to see each other's actions and coordinate.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_SHARED_MEMORY_PROVIDER` | `noop` | **Implemented** | Storage provider (`redis`, `noop`). Agents must use `redis` for cross-agent memory. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_AGENT_DOMAIN` | `default` | **Implemented** | Groups agents for memory scoping. Agents in the same domain see each other's events. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN` | `100000` | **Implemented** | Max events in the Redis domain stream (approximate, uses `~MAXLEN`). | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL` | `30m` | **Implemented** | Auto-expiry for investigation claims. Must be >= HITL timeout + execution buffer for agents using cross-agent delegation. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS` | `2000` | **Implemented** | Max tokens of memory context injected into the planning prompt. Caps enrichment to prevent prompt bloat. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT` | `20` | **Implemented** | Recent domain events for baseline situational awareness (without compactor). Higher values show more cross-agent activity but consume more prompt tokens. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL` | `""` (agent default) | **Implemented** | Model for LLM event summarization and activity compaction calls. Supports aliases (`fast`, `smart`) or concrete model names. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS` | `500` | **Implemented** | Max token budget for the compacted domain activity digest. Controls the output size of the `ActivityCompactor` LLM call. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT` | `200` | **Implemented** | Max raw events fetched before compaction. Higher values give the compactor more context but increase LLM input size. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL` | `15` | **Implemented** | Raw events appended after the compacted digest for immediate detail access. Set to `0` to disable. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL` | `5m` | **Implemented** | TTL for cached domain activity digests. When expired, next request triggers full compaction. Go duration format. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD` | `20` | **Implemented** | Max new events for incremental digest update. Above this threshold, full recompaction is triggered instead. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_SHARED_MEMORY_COMPACTION_ENABLED` | `false` | **Implemented** | Enable background compaction (event digesting into knowledge). Must be explicitly enabled. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

---

## User Memory Configuration

Configure per-user private memory for personal assistant agents. User memory stores learned facts (preferences, identity, constraints) across sessions and injects them into the planning prompt via a `<user_profile>` enrichment tag — enabling personalized agent responses without replaying raw conversation history. Numeric tuning uses environment variables; behavioural plugs (custom extractors, reconcilers) use `WithXXX()` options on `BuildUserMemoryHooks`.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_USER_MEMORY_COLLECTION` | `truvag3_user_memory` | **Implemented** | Vector DB collection name for user facts. Separate from shared knowledge collection. Used by the reference implementation (`VectorUserMemory`). | [memory/vector_user_memory.go](https://github.com/truvaagents/truva-g3/blob/main/memory/vector_user_memory.go) |
| `TRUVAG3_USER_MEMORY_MAX_FACTS_IN_PROMPT` | `15` | **Implemented** | Overall cap on facts injected into the `<user_profile>` enrichment tag after lifetime-aware selection. Higher values give the LLM more personalization context but consume more prompt tokens. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_MAX_IDENTITY_FACTS` | `5` | **Implemented** | Max identity facts (name, location, language) always included regardless of query relevance. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_MAX_DURABLE_FACTS_IN_PROMPT` | `8` | **Implemented** | Soft ceiling for durable facts (identity, preferences, constraints, relationships) selected before transient and summary facts compete for prompt space. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_MAX_TRANSIENT_FACTS_IN_PROMPT` | `4` | **Implemented** | Soft ceiling for transient/context facts in prompt selection after durable facts are considered. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS_IN_PROMPT` | `3` | **Implemented** | Soft ceiling for summary facts included for cross-session continuity after durable and transient selection. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_MAX_CONTEXT_FACTS` | — | **Legacy Compatibility** | Legacy fallback for transient prompt-budget configuration. Used only when `TRUVAG3_USER_MEMORY_MAX_TRANSIENT_FACTS_IN_PROMPT` is unset. Prefer the lifetime-aware variable instead. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS` | — | **Legacy Compatibility** | Legacy fallback for summary prompt-budget configuration. Used only when `TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS_IN_PROMPT` is unset. Prefer the lifetime-aware variable instead. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_MAX_STABLE_FACTS_PER_CATEGORY` | `2` | **Implemented** | Max stable namespace facts (constraints, preferences, relationships) fetched per category via category-based retrieval — bypasses semantic similarity so durable user attributes (e.g., home airport, dietary restrictions) are always injected regardless of query relevance. Applied only in non-universal namespaces. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_MAX_UNIVERSAL_FACTS` | `5` | **Implemented** | Max universal-namespace facts (cross-agent preferences, constraints) retrieved per request. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_MIN_CONFIDENCE` | `0.3` | **Implemented** | Minimum confidence threshold for fact inclusion. Facts below this are excluded from enrichment. | [orchestration/user_memory_hooks.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hooks.go) |
| `TRUVAG3_USER_MEMORY_RECONCILIATION_THRESHOLD` | `0.75` | **Implemented** | Cosine similarity threshold for triggering LLM reconciliation. Below this, candidate facts are treated as new (ADD). | [orchestration/user_memory_hook_builder.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hook_builder.go) |
| `TRUVAG3_USER_MEMORY_EXTRACTION_MODEL` | `""` (agent default) | **Implemented** | Model for fact extraction and session summary LLM calls. Use a fast/cheap model — these are high-volume, low-complexity calls. | [orchestration/user_memory_hook_builder.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hook_builder.go) |
| `TRUVAG3_USER_MEMORY_RECONCILIATION_MODEL` | extraction model | **Implemented** | Model for reconciliation LLM calls. Falls back to extraction model if unset. | [orchestration/user_memory_hook_builder.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_hook_builder.go) |
| `TRUVAG3_USER_MEMORY_SUMMARY_MAX_RESPONSE_LEN` | `500` | **Implemented** | Max characters of agent response included in the session summary prompt. Truncated at UTF-8 rune boundary. | [orchestration/user_memory_extraction.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_extraction.go) |
| `TRUVAG3_USER_MEMORY_BATCH_TOKENS_PER_CANDIDATE` | `400` | **Implemented** | Output token budget per candidate in the batched reconciliation LLM call (default reconciler collapses N per-candidate calls into one). Floors at 500 tokens for tiny batches. Increase if you see truncated `merged_content` on UPDATE-heavy turns; decrease for tighter cost control. | [orchestration/user_memory_extraction.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_extraction.go) |
| `TRUVAG3_USER_MEMORY_RECALL_OVERFETCH_MULTIPLIER` | `3` | **Implemented** | Over-fetch multiplier used by vector-backed recall before lifetime filtering so transient cleanup and bucket selection do not under-fill results. | [memory/vector_user_memory.go](https://github.com/truvaagents/truva-g3/blob/main/memory/vector_user_memory.go) |
| `TRUVAG3_USER_MEMORY_TRANSIENT_MAX_AGE_HOURS` | `168` | **Implemented** | Max age in hours for transient/context facts during recall-time filtering. Unset, invalid, or non-positive values fall back to 168 hours (7 days). | [memory/user_memory_backend.go](https://github.com/truvaagents/truva-g3/blob/main/memory/user_memory_backend.go) |
| `TRUVAG3_USER_MEMORY_MAX_SPLIT_CLAUSES` | `3` | **Implemented** | Max number of clauses emitted when Phase 4 splitting expands a mixed extracted fact before storage/reconciliation. | [orchestration/user_memory_extraction.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/user_memory_extraction.go) |

---

## Framework Runnable Lifecycle

Background components implementing `core.Runnable` are registered with the framework via `framework.RegisterRunnable(r)`. They start in goroutines when `Run(ctx)` is called and shut down when ctx is cancelled (typically by SIGTERM in Kubernetes). The only in-tree consumer today is [`memory.ReflectionJob`](https://github.com/truvaagents/truva-g3/blob/main/memory/reflection_job.go); the interface is general-purpose and any long-running background work an agent needs can implement it.

Because Go provides no mechanism for forcibly terminating goroutines, the framework gives each runnable a bounded grace period to honour `ctx.Done()` and exit cleanly. After the grace period, the framework logs a warning, returns from `Run`, and lets the OS reap any remaining goroutines on process exit. **Buggy runnables that ignore ctx will leak until process exit** — the framework cannot recover from this.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT` | `10s` | **Implemented** | Maximum time `Framework.Run` waits for registered `core.Runnable` instances to exit after ctx is cancelled. Go duration format (`5s`, `30s`, `2m`). Only positive values are honored — invalid or zero values fall back to the default. Shorter values trade clean shutdown for faster pod termination; longer values give in-flight reflection passes or other long-running jobs more time to finish their current iteration. | [core/agent.go](https://github.com/truvaagents/truva-g3/blob/main/core/agent.go) |

If you have a runnable that may legitimately need more than 10 seconds to wind down (e.g., a reflection pass mid-LLM-call), raise this. Conversely, if Kubernetes is killing your pods because shutdown takes too long, lower it — but be aware that runnables may be terminated mid-operation and any in-flight LLM calls or Redis writes will be abandoned.

The corresponding lifecycle log events are documented in [LOGGING_IMPLEMENTATION_GUIDE.md](../observability/LOGGING_IMPLEMENTATION_GUIDE.md) under the `framework_runnable_*` operations and the `runnable_drain_timeout` error_type.

---

## Reflection Job Configuration

Configure the background reflection job that bridges Tier 2 (episodic events in Redis) to Tier 3 (semantic knowledge fragments in Qdrant). The job runs on a periodic schedule, discovers entities with accumulated old events, asks an LLM to extract reusable patterns, embeds them, and stores them in the vector knowledge collection.

Configuration follows the Configuration Split principle from [FRAMEWORK_DESIGN_PRINCIPLES.md](https://github.com/truvaagents/truva-g3/blob/main/FRAMEWORK_DESIGN_PRINCIPLES.md): numeric tuning is via environment variables, behavioural plugs are via Go options. The available `With*` options are:

- **On `NewReflectionJob` / `BuildReflectionJob`** (`ReflectionJobOption`): `WithReflectionLock(core.DistributedLock)`, `WithReflectionTelemetry(core.Telemetry)`. See [memory/reflection_job.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflection_job.go).
- **On `NewLLMMemoryReflector`** (`ReflectorOption`): `WithReflectorLogger(core.Logger)`, `WithReflectorTelemetry(core.Telemetry)`, `WithReflectorMinEvents(int)`, `WithReflectorModel(string)`. See [memory/reflector.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflector.go).

The reflection job is wired automatically by `BuildReflectionJob` when Phase 2 backends (`SharedKnowledge` + `EmbeddingClient`) are available. It is registered with the framework via `framework.RegisterRunnable(job)` and managed by the framework lifecycle (started on `Run`, stopped on context cancel).

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_REFLECTION_INTERVAL` | `24h` | **Implemented** | How often the reflection pass runs. Go duration format (`5m`, `6h`, `24h`). Shorter intervals increase LLM cost; longer intervals delay knowledge accumulation. | [memory/reflection_job.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflection_job.go) |
| `TRUVAG3_REFLECTION_AGE_THRESHOLD` | `168h` (7 days) | **Implemented** | Only events older than this are eligible for reflection. Should be longer than the activity compactor's window — events younger than this are still represented in the per-request activity digest, so reflecting them prematurely spends LLM tokens on under-developed patterns. Go duration format. | [memory/reflection_job.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflection_job.go) |
| `TRUVAG3_REFLECTION_MIN_EVENTS` | `5` | **Implemented** | Minimum events per entity required to trigger reflection. Below this, the entity is skipped. Propagated to both the job's discovery pass and the LLM reflector — they share the same threshold. Lower values produce more fragments from less data (riskier); higher values produce fewer, more confident fragments. | [memory/reflection_job.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflection_job.go), [memory/reflection_job_builder.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflection_job_builder.go) |
| `TRUVAG3_REFLECTION_MODEL` | `""` (AIClient default) | **Implemented** | Model used for the reflection LLM call. Accepts cross-provider aliases (`fast`, `smart`, `default`) or concrete model names — the chain client resolves aliases per-provider at call time. Empty string means "use the AIClient's default selection." Reflection is durable — fragments influence every future request — so this is a real cost-vs-quality trade-off. See [Choosing a model for reflection](#choosing-a-model-for-reflection) below. | [memory/reflector.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflector.go), [memory/reflection_job_builder.go](https://github.com/truvaagents/truva-g3/blob/main/memory/reflection_job_builder.go) |

### Choosing a model for reflection

Reflection runs periodically (default: once per 24 hours, configurable via `TRUVAG3_REFLECTION_INTERVAL`) and writes durable knowledge fragments into Qdrant. Those fragments are recalled by semantic search and injected into `<agent_memory>` for **every future request**. The cost-vs-quality trade-off is therefore *unlike* per-request orchestration calls — a sloppy fragment isn't a one-off mistake, it lingers and surfaces as confident "prior knowledge" for as long as it remains in Qdrant.

Recommended configurations:

| Use case | `TRUVAG3_REFLECTION_MODEL` | `TRUVAG3_REFLECTION_MIN_EVENTS` | Reasoning |
|---|---|---|---|
| **Default (production)** | unset (uses agent default) | `5` | Best fragment quality. Reflection runs infrequently so cost is bounded. |
| **High volume / cost-sensitive** | `fast` | `5` | Cheaper and faster per pass. `min_events=5` ensures the cheap model only sees entities with strong signal. |
| **Aggressive learning** | unset | `2` | Captures patterns from sparse-event entities. Use only with the strongest model — low signal × weak model = noise. |
| **Bulk indexing of historical data** | `fast` | `10` | When backfilling reflection over a large existing event log, the cheap model handles the volume and the high `min_events` floor filters out low-signal entities. |

Aliases (`fast`, `smart`, `default`, `code`, `vision`) are resolved per-provider by the chain client at call time. The authoritative alias-to-model mappings live in the provider source files, not in this guide — bumping them here would silently desync from code on every model release. See:

- **Anthropic:** [ai/providers/anthropic/models.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/anthropic/models.go)
- **OpenAI and OpenAI-compatible (Groq, DeepSeek, xAI, Together, Mistral, Qwen, Ollama):** [ai/providers/openai/models.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/openai/models.go) — sub-providers use the `openai.<sub>` alias namespace
- **Google Gemini:** [ai/providers/gemini/models.go](https://github.com/truvaagents/truva-g3/blob/main/ai/providers/gemini/models.go)

You can override any alias on any provider without recompiling by setting `TRUVAG3_{PROVIDER}_MODEL_{ALIAS}` — for example `TRUVAG3_ANTHROPIC_MODEL_FAST=claude-haiku-4-5-20251001` or `TRUVAG3_GROQ_MODEL_FAST=llama-3.1-8b-instant`.

To set the model programmatically (Layer 3, bypassing the env var):

```go
reflector, err := memory.NewLLMMemoryReflector(
    aiClient, episodic, "infrastructure", logger,
    memory.WithReflectorModel("fast"),
)
if err != nil {
    return err
}
```

---

## Activity Coordination Configuration

Configure real-time agent coordination signals. Activity signals are transient Redis KV entries with TTL that allow agents to see what other agents are currently working on — enabling coordination without waiting for episodic events (which are only written after execution completes).

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_ACTIVITY_COORDINATION_ENABLED` | `true` | **Implemented** | Enable/disable the activity coordination layer. When false, no signals are emitted or read. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_ACTIVITY_SIGNAL_TTL` | `5m` | **Implemented** | Time-to-live for activity signals. Should be longer than the longest expected request duration. Go duration format. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT` | `10` | **Implemented** | Max activity signals shown in the `<agent_coordination>` prompt section. Most recent first after filtering. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN` | `200` | **Implemented** | Max characters of the request query included in activity signals. Longer queries are truncated. | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

---

## Logging Configuration

Configure logging output format and level.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_LOG_LEVEL` | `info` | **Implemented** | Minimum log level (debug, info, warn, error) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_LOG_FORMAT` | `json` (K8s) / `text` (local) | **Implemented** | Output format | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_LOG_OUTPUT` | `stdout` | Struct Tag Only | Output destination | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_LOG_TIME_FORMAT` | RFC3339Nano | Struct Tag Only | Timestamp format | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

---

## Development Configuration

Configure development-mode settings.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_DEV_MODE` | `false` | **Implemented** | Enable development mode (sets debug logging, text format) | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_MOCK_AI` | `false` | **Implemented** | Use mock AI responses | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_MOCK_DISCOVERY` | `false` | **Implemented** | Use in-memory mock discovery | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_DEBUG` | `false` | **Implemented** | Enable debug logging | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_PRETTY_LOGS` | `false` | Struct Tag Only | Enable human-readable logs | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

### Effects of `TRUVAG3_DEV_MODE=true`

When enabled, automatically sets:
- `TRUVAG3_LOG_LEVEL` to `debug`
- `TRUVAG3_LOG_FORMAT` to `text`
- Pretty logs enabled

---

## Kubernetes Configuration

Kubernetes-specific settings. Most are auto-detected when running in K8s.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `KUBERNETES_SERVICE_HOST` | (auto) | **Implemented** | Auto-set by K8s, triggers K8s mode | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `HOSTNAME` | (auto) | **Implemented** | Pod name, auto-set by K8s | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_K8S_NAMESPACE` | (auto-detected) | **Implemented** | Pod namespace | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_K8S_SERVICE_NAME` | Agent name | **Implemented** | Kubernetes service name | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_K8S_SERVICE_PORT` | `80` | **Implemented** | Kubernetes service port | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_K8S_POD_IP` | (auto) | **Implemented** | Pod IP address | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_K8S_NODE_NAME` | (auto) | **Implemented** | Node name where pod is running | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_K8S_SA_PATH` | `/var/run/secrets/kubernetes.io/serviceaccount` | Struct Tag Only | Service account mount path | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_K8S_SERVICE_DISCOVERY` | `true` | Struct Tag Only | Enable K8s service discovery | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |
| `TRUVAG3_K8S_LEADER_ELECTION` | `false` | Struct Tag Only | Enable leader election | [core/config.go](https://github.com/truvaagents/truva-g3/blob/main/core/config.go) |

### Kubernetes Deployment Example

```yaml
env:
  - name: TRUVAG3_AGENT_NAME
    value: "weather-tool"
  - name: TRUVAG3_K8S_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
  - name: TRUVAG3_K8S_POD_IP
    valueFrom:
      fieldRef:
        fieldPath: status.podIP
  - name: TRUVAG3_K8S_SERVICE_NAME
    value: "weather-tool"
  - name: TRUVAG3_K8S_SERVICE_PORT
    value: "8080"
```

---

## Orchestration Configuration

Configure the AI orchestrator for multi-agent coordination.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_ORCHESTRATION_TIMEOUT` | `120s` | **Implemented** | HTTP client timeout for tool/agent calls | [orchestration/executor.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/executor.go) |
| `TRUVAG3_EXECUTION_MAX_CONCURRENCY` | `25` | **Implemented** | Max parallel step executions in DAG. Controls goroutine concurrency during plan execution. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_EXECUTION_STEP_TIMEOUT` | `120s` | **Implemented** | Per-step execution timeout. Each individual step must complete within this duration. Go duration format. For `CapabilityOrchestrator` steps, the effective timeout is `TRUVAG3_HITL_DEFAULT_TIMEOUT` + this value. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_CONVERSATION_TOKEN_BUDGET` | `48000` | **Implemented** | Maximum estimated tokens allowed for prepared `<conversation_history>` before Tier 1 truncation/elision. Default-on safety net for metadata and hook ingress paths. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_CONVERSATION_RECENT_TURNS_PRESERVED` | `4` | **Implemented** | Number of newest turns always kept verbatim when turn-aware conversation preparation needs to shed context. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_CONVERSATION_SUMMARY_CACHE_SIZE` | `256` | **Implemented** | LRU capacity for the optional Tier 2 recursive conversation-summary cache. Only used when the application injects a compaction-enabled conversation-history preparer. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_STEP_RETRY_INITIAL_DELAY` | `500ms` | **Implemented** | Initial backoff delay for step retries. Go duration format (e.g., `500ms`, `2s`). Exponential backoff doubles this per attempt. Invalid values are silently ignored. | [orchestration/executor.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/executor.go) |
| `TRUVAG3_STEP_RETRY_MAX_DELAY` | `10s` | **Implemented** | Maximum backoff delay for step retries. Go duration format. Delay is capped at this value regardless of attempt count. Invalid values are silently ignored. | [orchestration/executor.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/executor.go) |
| `TRUVAG3_OAUTH_TOKEN` | (empty) | **Implemented** | OAuth Bearer token for outbound HTTP calls to tool/agent endpoints. Per-request tokens via `WithOAuthToken()` take priority. When set, executor requests include `Authorization: Bearer <token>`. | [orchestration/executor.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/executor.go) |
| `TRUVAG3_TIERED_RESOLUTION_ENABLED` | `true` | **Implemented** | Enable tiered capability resolution for LLM token optimization. Uses 2-phase approach to reduce tokens by 50-75%. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_TIERED_MIN_TOOLS` | `20` | **Implemented** | Minimum tool count to trigger tiered resolution. Below this threshold, all tools are sent directly. Research-backed default. | [orchestration/tiered_capability_provider.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/tiered_capability_provider.go) |
| `TRUVAG3_TIERED_SELECTION_MAX_TOKENS` | `2000` | **Implemented** | Max output tokens for tiered selection LLM calls. Higher values allow complex multi-tool selections but cost more tokens. | [orchestration/tiered_capability_provider.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/tiered_capability_provider.go) |
| `TRUVAG3_TIERED_SELECTION_RETRY_ENABLED` | `true` | **Implemented** | Enable retry on empty LLM responses and parse failures during tiered tool selection. Set to `false` to disable. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_TIERED_SELECTION_RETRY_MAX` | `2` | **Implemented** | Max retry attempts for tiered selection (0 = disabled). 2 means up to 3 total attempts (1 initial + 2 retries). | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_CAPABILITY_SERVICE_URL` | (none) | **Implemented** | External capability service URL | [orchestration/capability_provider.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/capability_provider.go) |
| `CAPABILITY_SERVICE_URL` | (fallback) | **Implemented** | Alternative capability service URL | [orchestration/capability_provider.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/capability_provider.go) |
| `TRUVAG3_CAPABILITY_TOP_K` | `20` | **Implemented** | Number of capabilities to return | [orchestration/capability_provider.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/capability_provider.go) |
| `TRUVAG3_CAPABILITY_THRESHOLD` | `0.7` | **Implemented** | Minimum similarity threshold | [orchestration/capability_provider.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/capability_provider.go) |
| `TRUVAG3_PLAN_RETRY_ENABLED` | `true` | **Implemented** | Retry plan generation on JSON parse failures | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_PLAN_RETRY_MAX` | `2` | **Implemented** | Maximum retry attempts for plan parsing (0 = disabled) | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_PLAN_MAX_TOKENS` | `15000` | **Implemented** | Max output tokens for plan generation LLM calls (including hallucination and validation retries) | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_SYNTHESIS_MAX_TOKENS` | `5000` | **Implemented** | Max output tokens for response synthesis LLM calls | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_SYNTHESIS_TEMPERATURE` | `0.5` | **Implemented** | LLM temperature for response synthesis (0.0–2.0). Lower = more deterministic | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_PLAN_MODEL` | `""` | **Implemented** | Portable alias or model name for plan generation LLM calls. Use `"smart"`, `"default"`, or `"fast"` with ChainClient | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_SYNTHESIS_MODEL` | `""` | **Implemented** | Portable alias or model name for response synthesis LLM calls (streaming + non-streaming) | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_MICRO_RESOLUTION_MODEL` | `""` | **Implemented** | Portable alias or model name for micro-resolution and semantic retry LLM calls | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS` | `2000` | **Implemented** | Maximum output tokens for micro-resolution and semantic retry LLM calls | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HALLUCINATION_VALIDATION_ENABLED` | `true` | **Implemented** | Validate that LLM-generated plans only reference agents from the allowed list | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HALLUCINATION_RETRY_ENABLED` | `true` | **Implemented** | Retry plan generation with enhanced context when hallucination is detected | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HALLUCINATION_MAX_RETRIES` | `1` | **Implemented** | Maximum retry attempts for hallucination correction (0 = disabled) | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_ITERATIVE_PLANNING_ENABLED` | `true` | **Implemented** | Enable multi-phase iterative planning. LLM planner can generate partial plans that execute in phases. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_ITERATIVE_MAX_PHASES` | `5` | **Implemented** | Maximum planning phases per request. Forces termination with synthesis if reached. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS` | `200` | **Implemented** | Maximum total steps across all phases. Prevents runaway plan generation. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_ITERATIVE_PHASE_TIMEOUT` | `180s` | **Implemented** | Maximum duration for a single phase (plan generation + execution). Go duration format. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_CONTINUATION_RESULT_MAX_CHARS` | `10000` | **Implemented** | Maximum chars per completed step result in continuation planning prompts (~2500 tokens). Orchestrator delegation responses can be 20-30KB; the default ensures child agent sub-steps are visible. Values below 4000 risk hiding delegation context. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_FAILURE_PATTERN_MIN_FAILURES` | `2` | **Implemented** | Minimum distinct failed upstream steps required for the remediation continuation prompt to embed a shared-error pattern summary. `1` would make every skip emit a summary (noisy); raise to make the analyzer more conservative. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN` | `120` | **Implemented** | Max chars of error text used to bucket failures into a shared signature for classification. Wider than the display cap to reduce false-positive collisions between distinct errors that share a common prefix. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN` | `80` | **Implemented** | Max chars of the shared error rendered into the remediation prompt (trailing `…` on truncation). Kept short so the continuation prompt stays slim per EFFECTIVE_PROMPTS_GUIDE §4.5. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_ENABLED` | `true` | **Implemented** | Enable structural trimming of large step results before prompt construction. Prevents token budget overflow. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_MAX_BYTES` | `16384` | **Implemented** | Maximum bytes per individual step result (~4K tokens). Results exceeding this are structurally trimmed. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES` | `32768` | **Implemented** | Maximum total bytes across all step results in a synthesis prompt (~8K tokens). | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES` | `65536` | **Implemented** | Maximum bytes for source data in micro-resolution and semantic retry prompts (~26K tokens). | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES` | `65536` | **Implemented** | Maximum bytes per parameter value for agent/tool HTTP calls (~26K tokens). | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD` | `16384` | **Implemented** | Result size threshold above which schema-guided mapping replaces direct value extraction. Set to 0 to disable. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_DISTILL_ENABLED` | `false` | **Implemented** | Enable opt-in LLM-based result distillation (two-stage: structural pre-filter → LLM distill). | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_DISTILL_THRESHOLD` | `32768` | **Implemented** | Minimum result size (bytes) to trigger LLM distillation. Below this, structural trimming only. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_DISTILL_PREFILTER` | `32768` | **Implemented** | StructuralTrimmer budget applied before LLM distillation (Stage 1 pre-filter). | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_DISTILL_TARGET` | `4096` | **Implemented** | Target output size for LLM distillation (Stage 2). | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_DISTILL_MODEL` | `""` | **Implemented** | Portable alias or model name for LLM distillation calls | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |

### Tiered Capability Resolution

Tiered resolution is a research-backed optimization that reduces LLM token usage by 50-75% for deployments with 20+ tools. It works by first sending lightweight tool summaries to select relevant tools, then fetching full schemas only for selected tools.

```bash
# Tiered resolution is enabled by default
export TRUVAG3_TIERED_RESOLUTION_ENABLED=true

# Adjust threshold for when tiering kicks in (default: 20)
export TRUVAG3_TIERED_MIN_TOOLS=25

# Increase selection output tokens for complex multi-tool registries (default: 2000)
export TRUVAG3_TIERED_SELECTION_MAX_TOKENS=3000

# Disable for small deployments (< 20 tools)
export TRUVAG3_TIERED_RESOLUTION_ENABLED=false

# Retry on empty responses / parse failures (default: enabled, 2 retries)
export TRUVAG3_TIERED_SELECTION_RETRY_ENABLED=true
export TRUVAG3_TIERED_SELECTION_RETRY_MAX=2
```

**When to use:**
- **< 20 tools**: Disable tiered resolution (overhead not worth it)
- **20-100 tools**: Use tiered resolution (default, 50-75% token savings)
- **100s+ tools**: Consider ServiceCapabilityProvider for semantic search

### Conversation History Preparation

Conversation history now has a framework-owned preparation layer that runs before planning. Tier 1 token protection is default-on; Tier 2 recursive compaction is opt-in.

```bash
# Raise/lower the default-on conversation-history budget (default: 48000)
export TRUVAG3_CONVERSATION_TOKEN_BUDGET=64000

# Keep more or fewer recent turns verbatim when history comes in as raw turns
export TRUVAG3_CONVERSATION_RECENT_TURNS_PRESERVED=6

# Only relevant when you inject a compaction-enabled preparer
export TRUVAG3_CONVERSATION_SUMMARY_CACHE_SIZE=512
```

**How these settings apply:**
- `TRUVAG3_CONVERSATION_TOKEN_BUDGET` protects both legacy formatted history and raw-turn metadata.
- `TRUVAG3_CONVERSATION_RECENT_TURNS_PRESERVED` matters only on the turn-aware path.
- `TRUVAG3_CONVERSATION_SUMMARY_CACHE_SIZE` matters only when the application enables Tier 2 recursive compaction by injecting a preparer with a summary cache and compactor.

### Plan Parse Retry

When LLMs generate execution plans, JSON parsing may fail due to:
- Arithmetic expressions in values (e.g., `"amount": 100 * price`)
- Malformed JSON syntax (trailing commas, missing quotes)
- Invalid JSON structures

The retry mechanism provides error feedback to the LLM, allowing it to correct its output:

```bash
# Disable retry (fail fast on parse errors)
export TRUVAG3_PLAN_RETRY_ENABLED=false

# Increase retry attempts (default: 2)
export TRUVAG3_PLAN_RETRY_MAX=3

# Disable by setting max retries to 0
export TRUVAG3_PLAN_RETRY_MAX=0
```

### LLM Synthesis Parameters

Controls the maximum output tokens and temperature for LLM calls during plan generation and response synthesis. The plan generation default is 15000 to prevent JSON truncation on complex plans. The synthesis default is 5000 tokens at temperature 0.5 (deterministic). Streaming chat applications typically use a higher temperature (0.7) for more natural responses.

```bash
# Increase plan generation token limit (default: 15000)
export TRUVAG3_PLAN_MAX_TOKENS=20000

# Increase synthesis token limit (default: 5000)
export TRUVAG3_SYNTHESIS_MAX_TOKENS=8000

# Adjust synthesis temperature (default: 0.5, range: 0.0-2.0)
# Higher values produce more creative responses (good for streaming chat)
export TRUVAG3_SYNTHESIS_TEMPERATURE=0.7

# Reduce plan generation for cost savings
export TRUVAG3_PLAN_MAX_TOKENS=5000

# Model overrides — use portable aliases for ChainClient compatibility
export TRUVAG3_PLAN_MODEL=smart
export TRUVAG3_SYNTHESIS_MODEL=default
export TRUVAG3_MICRO_RESOLUTION_MODEL=fast
```

### Hallucination Detection

LLMs can hallucinate non-existent agents when generating execution plans. For example, when asked about "time in CST", the LLM might invent a `time-tool-v1` agent even though only `weather-tool-v2` was provided in the prompt. Hallucination detection validates that all agents referenced in the plan were actually included in the capability information shown to the LLM.

When validation fails, the retry mechanism provides explicit error feedback with the list of allowed agents, giving the LLM a chance to self-correct:

```bash
# Disable hallucination validation entirely (not recommended)
export TRUVAG3_HALLUCINATION_VALIDATION_ENABLED=false

# Disable retry (fail immediately when hallucination detected)
export TRUVAG3_HALLUCINATION_RETRY_ENABLED=false

# Increase retry attempts (default: 1, usually sufficient for self-correction)
export TRUVAG3_HALLUCINATION_MAX_RETRIES=2
```

**How it works:**

1. **Plan Generated**: LLM produces an execution plan with agent references
2. **Validation**: Each agent name is checked against the allowed list (agents shown in the prompt)
3. **Hallucination Detected**: If an agent name isn't in the allowed list, validation fails
4. **Enhanced Retry**: The LLM receives error feedback with the exact hallucinated name and the complete allowed agent list
5. **Self-Correction**: LLM regenerates the plan using only valid agents

**When to disable:**
- **Testing/Development**: When using mock agents not in the catalog
- **Custom Validation**: When implementing your own agent validation logic
- **Debugging**: To see raw LLM output without intervention

### Semantic Retry Configuration (Layer 4)

Semantic Retry is an advanced error recovery feature that uses LLM analysis to compute corrected parameters when standard error analysis cannot fix the issue. When a tool call fails (e.g., `amount: 0` instead of `amount: 46828.5`), the contextual re-resolver uses full execution context—including the user's original query and source data from dependent steps—to compute the correct value.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_SEMANTIC_RETRY_ENABLED` | `true` | **Implemented** | Enable Layer 4 semantic retry with LLM-based parameter re-computation | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS` | `2` | **Implemented** | Maximum semantic retry attempts per step (0 = disabled) | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS` | `true` | **Implemented** | Enable semantic retry for steps without dependencies (first steps, parallel steps) | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |

**How Semantic Retry Works:**

1. **Error Detected**: Tool returns 4xx error (400, 404, 409, 422)
2. **Layer 3 Analysis**: ErrorAnalyzer determines it cannot fix the issue
3. **Layer 4 Activation**: ContextualReResolver receives full execution context:
   - User's original query (intent)
   - Source data from dependent steps (what to compute from)
   - Failed parameters and error message
4. **LLM Computation**: The LLM analyzes the context and computes corrected parameters
5. **Retry**: The step is retried with computed parameters

**Example scenario:**
```
User: "Sell 100 Tesla shares and convert proceeds to EUR"
Step 1 (stock-tool): Returns {price: 468.285}
Step 2 (currency-tool): Fails with "amount must be > 0" (amount: 0)

→ Layer 4 computes: amount = 100 × 468.285 = 46828.5
→ Retries currency conversion with corrected amount
```

**Disabling Semantic Retry:**

```bash
# Disable semantic retry entirely
export TRUVAG3_SEMANTIC_RETRY_ENABLED=false

# Or limit retry attempts
export TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS=1

# Disable only for independent steps (revert to old behavior)
export TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS=false
```

### Iterative Planning (Multi-Phase DAG)

Iterative planning allows the LLM planner to generate partial plans (`terminal: false`) and continue planning after intermediate results are available. This enables "discovery → action" queries like "find famous tourist destinations in Germany and get their weather" where later steps depend on the semantic content of earlier results.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_ITERATIVE_PLANNING_ENABLED` | `true` | **Implemented** | Enable multi-phase iterative planning. When enabled, the LLM planner can signal partial plans that execute in phases. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_ITERATIVE_MAX_PHASES` | `5` | **Implemented** | Maximum number of planning phases per request. If reached without a terminal plan, the orchestrator forces termination and synthesizes with available results. Most queries need at most 2. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS` | `200` | **Implemented** | Maximum total steps across all phases. Prevents runaway plan generation. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_ITERATIVE_PHASE_TIMEOUT` | `180s` | **Implemented** | Maximum duration for a single phase (plan generation + execution). Uses Go duration format. Prevents a single continuation phase from hanging indefinitely. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |

**How Iterative Planning Works:**

1. **Phase 1**: LLM generates a partial plan (`terminal: false`) with steps it can confidently plan
2. **Execution**: Orchestrator executes Phase 1 steps (parallel where possible)
3. **Phase 2**: LLM receives Phase 1 results and plans next steps based on actual data
4. **Repeat**: Until the LLM sets `terminal: true` or budget limits are reached

**Example scenario:**
```
User: "Tell me about Canada - weather at famous destinations and latest news"
Phase 1: country-info + web-search("famous destinations") + news → terminal: false
Phase 2: geocode(Vancouver, Banff, ...) + weather(coordinates) → terminal: true
```

**Configuration:**

```bash
# Enable iterative planning (default: true)
export TRUVAG3_ITERATIVE_PLANNING_ENABLED=true

# Allow up to 5 phases for complex queries
export TRUVAG3_ITERATIVE_MAX_PHASES=5

# Allow up to 30 total steps across all phases
export TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS=30

# Increase phase timeout for slow external APIs
export TRUVAG3_ITERATIVE_PHASE_TIMEOUT=60s

# Disable iterative planning (all plans treated as single-shot)
export TRUVAG3_ITERATIVE_PLANNING_ENABLED=false
```

**When to adjust:**
- **Increase `MAX_PHASES`**: Complex research queries requiring multiple discovery rounds
- **Increase `MAX_TOTAL_STEPS`**: Queries that fan out to many services (e.g., weather for 10+ cities)
- **Decrease `MAX_PHASES`**: Cost-sensitive deployments (each phase costs an LLM planning call)
- **Disable entirely**: When all queries are closed-form with known parameters upfront

### Result Trimming (Large Result Data Management)

When tool/agent responses are large (e.g., full web pages, large API payloads), they can overflow the LLM's token budget during synthesis. Result trimming uses a `StructuralTrimmer` to intelligently reduce result sizes before they are embedded in prompts — preserving JSON structure, key names, and representative samples while dropping repetitive array elements and deeply nested data.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_CONTINUATION_RESULT_MAX_CHARS` | `10000` | **Implemented** | Max chars per step result in continuation prompts (~2500 tokens). Increase for cross-agent delegation. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_ENABLED` | `true` | **Implemented** | Enable structural trimming of large step results before prompt construction. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_MAX_BYTES` | `16384` | **Implemented** | Maximum bytes per individual step result (~4K tokens). Results exceeding this are structurally trimmed. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES` | `32768` | **Implemented** | Maximum total bytes across all step results in a synthesis prompt (~8K tokens). | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES` | `65536` | **Implemented** | Maximum bytes for source data in micro-resolution and semantic retry prompts (~26K tokens). Controls how much of the original step result is included when the LLM resolves missing parameters or retries failed steps. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES` | `65536` | **Implemented** | Maximum bytes per parameter value for agent/tool HTTP calls (~26K tokens). Trims large data values before they are sent as input parameters to downstream agents or tools. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD` | `16384` | **Implemented** | Result size threshold (bytes) above which schema-guided mapping is used instead of direct value extraction for micro-resolution. Set to `0` to disable schema-guided mapping entirely. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |

**How it works:**

1. **Per-result trimming**: Each step result is individually limited to `MaxResultBytes`. The `StructuralTrimmer` preserves JSON structure while dropping array elements beyond the first few and truncating deeply nested objects.
2. **Total budget enforcement**: After individual trimming, if the combined results still exceed `MaxTotalPromptBytes`, results are proportionally reduced.
3. **Micro-resolution budgets**: When the LLM needs to resolve missing parameters from prior step results, `MaxMicroResolutionBytes` controls how much source data is included in the prompt. For very large results, schema-guided mapping (triggered above `SchemaGuidedMappingThreshold`) uses JSON schema analysis instead of passing raw data.
4. **Agent input trimming**: Before sending data to downstream agents/tools via HTTP, `MaxAgentInputBytes` trims individual parameter values to prevent oversized request payloads.
5. **Fallback**: Non-JSON results are truncated with a `... [trimmed]` suffix.

**Configuration:**

```bash
# Disable result trimming (not recommended for production — may cause token overflow)
export TRUVAG3_RESULT_TRIM_ENABLED=false

# Increase per-result limit for larger individual results
export TRUVAG3_RESULT_TRIM_MAX_BYTES=32768   # 32 KB per result

# Increase total budget for queries with many steps
export TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES=65536  # 64 KB total

# Increase micro-resolution budget for complex data dependencies
export TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES=131072  # 128 KB for resolution prompts

# Increase agent input budget for data-heavy tool calls
export TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES=131072  # 128 KB per parameter value

# Disable schema-guided mapping (always use direct value extraction)
export TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD=0
```

**Programmatic configuration:**

```go
config := orchestration.DefaultConfig()
config.ResultTrim = orchestration.ResultTrimConfig{
    Enabled:                      true,
    MaxResultBytes:               16384, // 16 KB per result
    MaxTotalPromptBytes:          65536, // 64 KB total
    MaxMicroResolutionBytes:      65536, // 64 KB for micro-resolution source data
    MaxAgentInputBytes:           65536, // 64 KB per agent/tool parameter value
    SchemaGuidedMappingThreshold: 16384, // 16 KB — use schema mapping above this
}
```

**When to adjust:**
- **Increase limits**: When LLM synthesis responses lack detail because results were trimmed too aggressively
- **Decrease limits**: When hitting token limits or getting slow synthesis responses
- **Disable**: Only for debugging — production deployments should always have trimming enabled

### Result Distillation (Opt-In LLM Summarization)

For results that are extremely large or contain domain-specific content that structural trimming cannot adequately compress, opt-in LLM distillation provides a two-stage pipeline: structural pre-filtering followed by LLM-based summarization.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_RESULT_DISTILL_ENABLED` | `false` | **Implemented** | Enable LLM-based result distillation. Opt-in because it adds an LLM call per large result. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_DISTILL_THRESHOLD` | `32768` | **Implemented** | Minimum result size (bytes) to trigger distillation. Below this threshold, structural trimming is used instead. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_DISTILL_PREFILTER` | `32768` | **Implemented** | StructuralTrimmer budget for Stage 1 pre-filtering. Reduces input to LLM before distillation. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_DISTILL_TARGET` | `4096` | **Implemented** | Target output size for the LLM distillation (Stage 2). The LLM summarizes the pre-filtered result to approximately this size. | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_RESULT_DISTILL_MODEL` | `""` | **Implemented** | Portable alias or model name for distillation LLM calls. Use `"fast"` for cost savings | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |

**How the two-stage pipeline works:**

1. **Stage 1 — Structural Pre-Filter**: The `StructuralTrimmer` reduces the large result to `PreFilterBudget` bytes, preserving JSON structure and key data points.
2. **Stage 2 — LLM Distillation**: An LLM call summarizes the pre-filtered result to approximately `TargetSize` bytes, preserving the most relevant information for the user's query.
3. **Fallback**: If the LLM call fails, the Stage 1 pre-filtered result is used directly.

**Configuration:**

```bash
# Enable distillation for extremely large results
export TRUVAG3_RESULT_DISTILL_ENABLED=true

# Only distill results larger than 64 KB
export TRUVAG3_RESULT_DISTILL_THRESHOLD=65536

# Allow larger pre-filter budget for better LLM context
export TRUVAG3_RESULT_DISTILL_PREFILTER=65536

# Target smaller output from LLM
export TRUVAG3_RESULT_DISTILL_TARGET=2048
export TRUVAG3_RESULT_DISTILL_MODEL=fast
```

**Programmatic configuration:**

```go
config := orchestration.DefaultConfig()
config.ResultDistill = orchestration.ResultDistillConfig{
    Enabled:          true,
    DistillThreshold: 65536, // Only distill results > 64 KB
    PreFilterBudget:  65536, // 64 KB pre-filter budget
    TargetSize:       2048,  // 2 KB target output
}
```

**When to enable:**
- **Large API responses**: When tools return full web pages, large JSON payloads, or document content
- **Quality over cost**: When you need better synthesis quality and are willing to pay the additional LLM call cost
- **Domain-specific content**: When structural trimming loses important semantic content that only an LLM can preserve

**When NOT to enable:**
- **Cost-sensitive deployments**: Each large result adds an LLM distillation call
- **Low-latency requirements**: The extra LLM call adds latency
- **Small results**: If your tools already return concise results, structural trimming is sufficient

### Example

```bash
# For long-running AI workflows
export TRUVAG3_ORCHESTRATION_TIMEOUT=5m

# For high-concurrency execution
export TRUVAG3_EXECUTION_MAX_CONCURRENCY=10

# For steps that need more time (e.g., large API responses)
export TRUVAG3_EXECUTION_STEP_TIMEOUT=180s

# For step retry backoff tuning (exponential backoff between retries)
export TRUVAG3_STEP_RETRY_INITIAL_DELAY=500ms
export TRUVAG3_STEP_RETRY_MAX_DELAY=10s

# For capability service integration
export TRUVAG3_CAPABILITY_SERVICE_URL="http://capability-service:8080"
export TRUVAG3_CAPABILITY_TOP_K=30
export TRUVAG3_CAPABILITY_THRESHOLD=0.75

# For plan parse retry configuration
export TRUVAG3_PLAN_RETRY_ENABLED=true
export TRUVAG3_PLAN_RETRY_MAX=2

# For hallucination detection (validates LLM doesn't invent agents)
export TRUVAG3_HALLUCINATION_VALIDATION_ENABLED=true
export TRUVAG3_HALLUCINATION_RETRY_ENABLED=true
export TRUVAG3_HALLUCINATION_MAX_RETRIES=1

# For semantic retry configuration (Layer 4)
export TRUVAG3_SEMANTIC_RETRY_ENABLED=true
export TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS=2
export TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS=true

# For iterative planning (multi-phase DAG)
export TRUVAG3_ITERATIVE_PLANNING_ENABLED=true
export TRUVAG3_ITERATIVE_MAX_PHASES=5
export TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS=200
export TRUVAG3_ITERATIVE_PHASE_TIMEOUT=180s

# Continuation prompt result visibility (cross-agent delegation)
export TRUVAG3_CONTINUATION_RESULT_MAX_CHARS=10000   # 10K chars (~2500 tokens) per step result

# For result trimming (enabled by default)
export TRUVAG3_RESULT_TRIM_ENABLED=true
export TRUVAG3_RESULT_TRIM_MAX_BYTES=16384
export TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES=32768
export TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES=65536
export TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES=65536
export TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD=16384

# For result distillation (opt-in, disabled by default)
export TRUVAG3_RESULT_DISTILL_ENABLED=false
export TRUVAG3_RESULT_DISTILL_THRESHOLD=32768
export TRUVAG3_RESULT_DISTILL_PREFILTER=32768
export TRUVAG3_RESULT_DISTILL_TARGET=4096
```

---

## LLM Debug Configuration

Configure LLM debug payload storage for debugging orchestration issues. This feature captures full LLM request/response payloads to help diagnose planning failures, parse errors, and unexpected AI behavior.

> **Important**: This feature is **disabled by default** to minimize storage overhead. Enable only when debugging is needed.

### LLM Debug Variables

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_LLM_DEBUG_ENABLED` | `false` | **Implemented** | Enable LLM debug payload storage | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_LLM_DEBUG_TTL` | `24h` | **Implemented** | Retention period for successful debug records | [orchestration/redis_llm_debug_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/redis_llm_debug_store.go) |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | `168h` (7 days) | **Implemented** | Retention period for error debug records (longer for troubleshooting) | [orchestration/redis_llm_debug_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/redis_llm_debug_store.go) |
| `TRUVAG3_LLM_DEBUG_REDIS_DB` | `7` | **Implemented** | Redis database number for debug storage (uses `core.RedisDBLLMDebug`) | [orchestration/redis_llm_debug_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/redis_llm_debug_store.go) |

### How It Works

When enabled, the orchestrator automatically captures:
- **Request payloads**: Full prompts sent to the LLM
- **Response payloads**: Complete LLM responses (parsed and raw)
- **Timing metadata**: Duration, timestamps, retry attempts
- **Error context**: Parse failures, validation errors with original content

### Example: Enable Debug Storage

```bash
# Enable LLM debug storage
export TRUVAG3_LLM_DEBUG_ENABLED=true

# Increase retention for debugging (optional)
export TRUVAG3_LLM_DEBUG_TTL=48h
export TRUVAG3_LLM_DEBUG_ERROR_TTL=168h
```

### Example: Kubernetes Deployment

```yaml
env:
  - name: TRUVAG3_LLM_DEBUG_ENABLED
    value: "true"
  - name: TRUVAG3_LLM_DEBUG_TTL
    value: "48h"
  - name: TRUVAG3_LLM_DEBUG_ERROR_TTL
    value: "168h"
```

### Viewing Debug Records

Use the Registry Viewer App to view captured debug records:
1. Navigate to the "LLM Debug" tab in the sidebar
2. Browse records by agent, timestamp, or status (success/error)
3. Inspect full request/response payloads for troubleshooting

---

## Execution Debug Store Configuration

Configure execution debug storage for DAG visualization and debugging. This feature stores complete plan execution records (plan + result) to enable visualization of LLM-based plan execution as a directed acyclic graph (DAG).

> **Important**: This feature is **disabled by default** to minimize storage overhead. Enable only when DAG visualization or execution debugging is needed.

### Execution Debug Store Variables

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED` | `false` | **Implemented** | Enable/disable execution debug storage | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_EXECUTION_DEBUG_TTL` | `24h` | **Implemented** | Retention period for successful execution records | [orchestration/redis_execution_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/redis_execution_store.go) |
| `TRUVAG3_EXECUTION_DEBUG_ERROR_TTL` | `168h` (7 days) | **Implemented** | Retention period for failed execution records (longer for troubleshooting) | [orchestration/redis_execution_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/redis_execution_store.go) |
| `TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX` | `truvag3:execution:debug` | **Implemented** | Key prefix for all storage keys. Allows multi-tenant deployments or custom namespacing. | [orchestration/redis_execution_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/redis_execution_store.go) |
| `TRUVAG3_EXECUTION_DEBUG_REDIS_DB` | `8` | **Implemented** | Redis database number (uses `core.RedisDBExecutionDebug`) | [orchestration/redis_execution_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/redis_execution_store.go) |

### Features (Same as LLM Debug Store)

The execution debug store has full feature parity with the LLM Debug Store:
- **Gzip compression**: Large payloads (>100KB) are automatically compressed
- **Layer 1 resilience**: Built-in retry with exponential backoff (3 retries, 100ms-2s backoff)
- **Layer 2 resilience**: Optional circuit breaker injection via `WithExecutionDebugCircuitBreaker()`
- **Layer 3 resilience**: NoOp fallback when Redis is unavailable

### How It Works

When enabled, the orchestrator automatically stores:
- **Plan data**: LLM-generated routing plans with step dependencies
- **Execution results**: Step-by-step results with timing, status, and responses
- **Trace correlation**: Links to distributed tracing (Jaeger) via trace IDs
- **Metadata**: Investigation notes and custom annotations

### Storage Key Patterns

The execution debug store uses configurable key patterns based on `TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX`:

| Key Pattern | Purpose |
|-------------|---------|
| `{prefix}{request_id}` | Main execution record (plan + result) |
| `{prefix}index` | Sorted index for listing recent executions |
| `{prefix}trace:{trace_id}` | Trace ID → Request ID mapping |

With the default prefix `truvag3:execution:debug:`:
- `truvag3:execution:debug:req-001` - Execution record
- `truvag3:execution:debug:index` - Recent executions index
- `truvag3:execution:debug:trace:abc123` - Trace mapping

### Example: Enable Execution Debug Storage

```bash
# Enable execution debug storage
export TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true

# Use default TTLs (24h for success, 7 days for errors)
# Or customize retention:
export TRUVAG3_EXECUTION_DEBUG_TTL=48h
export TRUVAG3_EXECUTION_DEBUG_ERROR_TTL=336h  # 14 days
```

### Example: Multi-Tenant Deployment

```bash
# Enable execution debug storage with custom prefix for isolation
export TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true
export TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX=myapp:prod:execution:debug:

# Keys will be: myapp:prod:execution:debug:req-001, etc.
```

### Example: Kubernetes Deployment

```yaml
env:
  # Enable execution debug storage
  - name: TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED
    value: "true"

  # Custom retention (optional)
  - name: TRUVAG3_EXECUTION_DEBUG_TTL
    value: "48h"
  - name: TRUVAG3_EXECUTION_DEBUG_ERROR_TTL
    value: "168h"

  # Custom key prefix for namespace isolation (optional)
  - name: TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX
    value: "myteam:execution:debug:"
```

### Viewing Execution Records

Use the Registry Viewer App to visualize execution DAGs:
1. Navigate to the "Executions" tab in the sidebar
2. Browse recent executions with success/failure status
3. Click on an execution to view the DAG visualization
4. Inspect step details, timing, and results
5. Link to Jaeger traces for distributed tracing correlation

### Storage Provider

The execution debug store uses the `StorageProvider` interface for backend abstraction:
- **Redis**: Use `NewRedisExecutionDebugStore()` (auto-configures from environment)
- **Other backends**: Implement the `StorageProvider` interface for PostgreSQL, DynamoDB, etc.

---

## Human-in-the-Loop (HITL) Configuration

Configure Human-in-the-Loop (HITL) checkpoints for human oversight of AI-generated plans and sensitive operations. HITL enables pausing execution for human approval before proceeding with critical steps.

> **Important**: HITL is **disabled by default**. Enable it via `TRUVAG3_HITL_ENABLED=true` when human oversight is required.

### Core HITL Variables

> **Source:** all HITL variables below are loaded by `DefaultConfig()` in
> [`orchestration/interfaces.go`](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) — look for
> the `HITLConfig` struct and the "HITL configuration from environment" block.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_HITL_ENABLED` | `false` | **Implemented** | Enable/disable HITL globally | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | `false` | **Implemented** | Require human approval for all LLM-generated plans | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HITL_SENSITIVE_CAPABILITIES` | (empty) | **Implemented** | Comma-separated list of capabilities requiring **both plan AND step approval** | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HITL_SENSITIVE_AGENTS` | (empty) | **Implemented** | Comma-separated list of agents requiring **both plan AND step approval** | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES` | (empty) | **Implemented** | Comma-separated list of capabilities requiring **step-only approval** (no plan approval) | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HITL_STEP_SENSITIVE_AGENTS` | (empty) | **Implemented** | Comma-separated list of agents requiring **step-only approval** (no plan approval) | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HITL_DEFAULT_TIMEOUT` | `5m` | **Implemented** | Timeout for human response before auto-action | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HITL_ESCALATE_AFTER_RETRIES` | `3` | **Implemented** | Number of retries before escalating to human | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |

### HITL Storage Variables

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_HITL_REDIS_DB` | `6` | **Implemented** | Redis database number for HITL checkpoint/command data | [orchestration/hitl_checkpoint_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_checkpoint_store.go) |
| `TRUVAG3_HITL_KEY_PREFIX` | `truvag3:hitl` | **Implemented** | Redis key prefix for HITL data | [orchestration/hitl_checkpoint_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_checkpoint_store.go) |

### HITL Handler Variables

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_HITL_WEBHOOK_URL` | (none) | **Implemented** | Webhook URL for checkpoint notifications | Application-specific |

### HITL Expiry Behavior Variables

These variables control what happens when a checkpoint times out (expires) without a human response. See [HITL User Guide: Expiry Behavior](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md#expiry-behavior) for detailed usage.

| Variable | Default | Values | Status | Description | Source |
|----------|---------|--------|--------|-------------|--------|
| `TRUVAG3_HITL_DEFAULT_ACTION` | `reject` | `approve`, `reject`, `abort` | **Implemented** | Action to take when `apply_default` expiry behavior is used. Fail-safe default is `reject` (HITL enabled = require explicit approval). | [orchestration/interfaces.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/interfaces.go) |
| `TRUVAG3_HITL_STREAMING_EXPIRY` | `implicit_deny` | `implicit_deny`, `apply_default` | **Implemented** | Expiry behavior for streaming (SSE) requests. `implicit_deny` = status→expired, no action. `apply_default` = auto-resume with `DefaultAction`. | [orchestration/hitl_checkpoint_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_checkpoint_store.go) |
| `TRUVAG3_HITL_NON_STREAMING_EXPIRY` | `apply_default` | `implicit_deny`, `apply_default` | **Implemented** | Expiry behavior for non-streaming (HTTP 202) requests. `apply_default` = auto-apply `DefaultAction` on timeout. | [orchestration/hitl_checkpoint_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_checkpoint_store.go) |
| `TRUVAG3_HITL_DEFAULT_REQUEST_MODE` | `non_streaming` | `streaming`, `non_streaming` | **Implemented** | Default request mode when not explicitly set via `WithRequestMode()`. A warning is logged when this fallback is used. | [orchestration/hitl_checkpoint_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_checkpoint_store.go) |

**Expiry Behavior Matrix** (see [HITL User Guide: Expiry Behavior Matrix](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md#expiry-behavior)):

| Request Mode | Expiry Behavior | What Happens |
|--------------|-----------------|--------------|
| Streaming + `implicit_deny` | Status → `expired` | User saw dialog but didn't respond. No action taken. |
| Streaming + `apply_default` | Status → `expired_approved/rejected` | Auto-resume enabled (see [Auto-Resume](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md#auto-resume-timeout-auto-approval)) |
| Non-Streaming + `apply_default` | Status → `expired_approved/rejected` | DefaultAction applied automatically |
| Non-Streaming + `implicit_deny` | Status → `expired` | Require manual intervention |

### HITL Expiry Processor Variables

The expiry processor is a background goroutine that scans for expired checkpoints and processes them according to the configured action. See [HITL User Guide: Auto-Resume](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md#auto-resume-timeout-auto-approval) for the auto-approval flow.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_HITL_EXPIRY_ENABLED` | `true` | **Implemented** | Enable/disable the background expiry processor. Set to `false` to disable automatic checkpoint expiration. | [orchestration/hitl_checkpoint_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_checkpoint_store.go) |
| `TRUVAG3_HITL_EXPIRY_INTERVAL` | `10s` | **Implemented** | How often the processor scans Redis for expired checkpoints. Lower values = faster detection but more Redis load. | [orchestration/hitl_checkpoint_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_checkpoint_store.go) |
| `TRUVAG3_HITL_EXPIRY_BATCH_SIZE` | `100` | **Implemented** | Maximum checkpoints processed per scan cycle. Prevents overwhelming the system when many expire simultaneously. | [orchestration/hitl_checkpoint_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_checkpoint_store.go) |
| `TRUVAG3_HITL_EXPIRY_DELIVERY` | `at_most_once` | **Implemented** | Callback delivery guarantee: `at_most_once` (safe default, no retries) or `at_least_once` (may retry, callback must be idempotent). | [orchestration/hitl_checkpoint_store.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/hitl_checkpoint_store.go) |

### HITL Approval Modes

HITL supports two approval modes based on risk level:

| Mode | Variables | Behavior |
|------|-----------|----------|
| **Full HITL** (Plan + Step) | `TRUVAG3_HITL_SENSITIVE_CAPABILITIES`, `TRUVAG3_HITL_SENSITIVE_AGENTS` | Pauses at plan generation for approval, then pauses again at each matching step |
| **Step-Only** | `TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES`, `TRUVAG3_HITL_STEP_SENSITIVE_AGENTS` | Skips plan approval, only pauses when the specific step is about to execute |

Use **Full HITL** for high-risk operations (e.g., `transfer_funds`, `delete_account`) where you want to review the entire plan before any execution.

Use **Step-Only** for medium-risk operations (e.g., `get_balance`, `view_orders`) where you trust the plan but want confirmation before each action.

### Example: Full HITL Configuration

```bash
# Enable HITL with plan approval
export TRUVAG3_HITL_ENABLED=true
export TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true

# Configure high-risk operations (plan + step approval)
export TRUVAG3_HITL_SENSITIVE_CAPABILITIES=transfer_funds,delete_account,send_email
export TRUVAG3_HITL_SENSITIVE_AGENTS=payment-service,admin-tool

# Timeout and escalation
export TRUVAG3_HITL_DEFAULT_TIMEOUT=5m
export TRUVAG3_HITL_ESCALATE_AFTER_RETRIES=3

# Redis storage (separate from service discovery DB 0)
export TRUVAG3_HITL_REDIS_DB=6
export TRUVAG3_HITL_KEY_PREFIX=truvag3:hitl

# Webhook for notifications
export TRUVAG3_HITL_WEBHOOK_URL=https://my-service/internal/hitl-webhook
```

### Example: Step-Only HITL Configuration

```bash
# Enable HITL (plan approval NOT required globally)
export TRUVAG3_HITL_ENABLED=true
export TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=false

# Full HITL (plan + step approval) for high-risk operations
export TRUVAG3_HITL_SENSITIVE_CAPABILITIES=transfer_funds,delete_account
export TRUVAG3_HITL_SENSITIVE_AGENTS=payment-service

# Step-only approval (no plan pause) for medium-risk operations
export TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=get_balance,view_orders,send_notification
export TRUVAG3_HITL_STEP_SENSITIVE_AGENTS=read-service,notification-tool

# Timeout and storage
export TRUVAG3_HITL_DEFAULT_TIMEOUT=5m
export TRUVAG3_HITL_REDIS_DB=6
```

### Example: Kubernetes Deployment

```yaml
env:
  # Enable HITL
  - name: TRUVAG3_HITL_ENABLED
    value: "true"
  - name: TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL
    value: "true"

  # Sensitive operations
  - name: TRUVAG3_HITL_SENSITIVE_CAPABILITIES
    value: "transfer_funds,delete_account"
  - name: TRUVAG3_HITL_SENSITIVE_AGENTS
    value: "payment-service"

  # Step-only operations
  - name: TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES
    value: "get_balance,view_orders"

  # Timeout
  - name: TRUVAG3_HITL_DEFAULT_TIMEOUT
    value: "10m"

  # Webhook
  - name: TRUVAG3_HITL_WEBHOOK_URL
    value: "http://notification-service/hitl-webhook"
```

### Viewing HITL Checkpoints

Use the Registry Viewer App or the HITL API endpoints to monitor pending checkpoints:
- `GET /hitl/checkpoints` - List pending checkpoints
- `GET /hitl/checkpoints/{id}` - Get checkpoint details
- `POST /hitl/command` - Submit approval/rejection decision (with `{"checkpoint_id": "...", "type": "approve|reject"}`)
- `POST /hitl/resume-sync/{id}` - Resume execution after approval (JSON response)
- `POST /hitl/resume/{id}` - Resume execution after approval (SSE stream)

See [HUMAN_IN_THE_LOOP_USER_GUIDE.md](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md) for detailed usage and examples.

---

## Async Task Configuration

Configure asynchronous task processing for long-running operations. The async task system enables the HTTP 202 + Polling pattern for operations that may take minutes to complete.

### Related Framework Types

The async task system uses these core framework types (no additional env vars required):

- `core.Task` - Task data structure with status, progress, result
- `core.TaskQueue` - Redis-backed task queue interface
- `core.TaskStore` - Redis-backed task state storage
- `core.TaskWorkerPool` - Worker pool implementation
- `orchestration.TaskAPIHandler` - HTTP API handler for task endpoints

See [Async Orchestration Guide](../orchestration/ASYNC_ORCHESTRATION_GUIDE.md) for detailed usage.

---

## Scheduled Execution Configuration

Configure the scheduled-execution subsystem: delayed and recurring task execution across agents. The system has two components (scheduler-tool produces schedules, scheduled-executor consumes and dispatches them). Code-level configuration via `ExecutorDeps` fields takes priority over environment variables.

### Scheduler-Tool (Producer)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_SCHEDULER_TICK_INTERVAL` | `5s` | **Implemented** | How often the Scheduler polls for due schedules (Go duration) | [orchestration/scheduler.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/scheduler.go) |
| `TRUVAG3_SCHEDULER_LOCK_TTL` | `30s` | **Implemented** | Distributed lock TTL for leader election; must be > tick interval (Go duration) | [orchestration/scheduler.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/scheduler.go) |

### Scheduled-Executor (Consumer)

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_EXECUTOR_WORKER_COUNT` | `5` | **Implemented** | Number of concurrent dispatch goroutines | [examples/scheduled-executor/worker.go](https://github.com/truvaagents/truva-g3/blob/main/examples/scheduled-executor/worker.go) |
| `TRUVAG3_EXECUTOR_MAX_RETRIES` | `3` | **Implemented** | Max retry attempts per task before dead-lettering | [examples/scheduled-executor/worker.go](https://github.com/truvaagents/truva-g3/blob/main/examples/scheduled-executor/worker.go) |
| `TRUVAG3_EXECUTOR_RETRY_BASE_DELAY` | `5s` | **Implemented** | Base delay for exponential backoff (Go duration) | [examples/scheduled-executor/worker.go](https://github.com/truvaagents/truva-g3/blob/main/examples/scheduled-executor/worker.go) |
| `TRUVAG3_EXECUTOR_RETRY_MAX_DELAY` | `60s` | **Implemented** | Maximum backoff cap (Go duration) | [examples/scheduled-executor/worker.go](https://github.com/truvaagents/truva-g3/blob/main/examples/scheduled-executor/worker.go) |
| `TRUVAG3_EXECUTOR_DISPATCH_TIMEOUT` | `15m` | **Implemented** | Per-request HTTP timeout for POST to target agent (Go duration). Must be ≥ the target agent's `TRUVAG3_ORCHESTRATION_TIMEOUT` | [examples/scheduled-executor/worker.go](https://github.com/truvaagents/truva-g3/blob/main/examples/scheduled-executor/worker.go) |

Both components require `REDIS_URL` for the task queue and service registry. The scheduled-executor also requires `TRUVAG3_K8S_SERVICE_NAME` and standard framework variables for service registration.

### Example: Tuning for High-Volume Scheduling

```bash
# Scheduler-tool: faster polling for time-sensitive schedules
export TRUVAG3_SCHEDULER_TICK_INTERVAL=2s
export TRUVAG3_SCHEDULER_LOCK_TTL=10s

# Scheduled-executor: more workers, more retries, tighter backoff
export TRUVAG3_EXECUTOR_WORKER_COUNT=10
export TRUVAG3_EXECUTOR_MAX_RETRIES=5
export TRUVAG3_EXECUTOR_RETRY_BASE_DELAY=2s
export TRUVAG3_EXECUTOR_RETRY_MAX_DELAY=30s
# Dispatch timeout defaults to 15m — only lower this if your agents
# complete orchestration faster (must be >= TRUVAG3_ORCHESTRATION_TIMEOUT).
```

### Code-Level Override

Environment variables are the deployment-time tuning layer. For stable compile-time defaults, set fields directly on `ExecutorDeps`:

```go
worker, _ := NewWorker(ExecutorDeps{
    WorkerCount:     10,              // overrides TRUVAG3_EXECUTOR_WORKER_COUNT
    MaxRetries:      5,               // overrides TRUVAG3_EXECUTOR_MAX_RETRIES
    DispatchTimeout: 30 * time.Second, // overrides TRUVAG3_EXECUTOR_DISPATCH_TIMEOUT
    // other fields...
})
```

See [Scheduled Tasks Guide](../orchestration/SCHEDULED_TASKS_GUIDE.md) for the full architecture and operational guide.

---

## Prompt Configuration

Configure LLM prompt customization for orchestration.

| Variable | Default | Status | Description | Source |
|----------|---------|--------|-------------|--------|
| `TRUVAG3_PROMPT_TEMPLATE_FILE` | (none) | **Implemented** | Path to custom prompt template file | [orchestration/prompt_config_env.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/prompt_config_env.go) |
| `TRUVAG3_PROMPT_DOMAIN` | (none) | **Implemented** | Domain context (healthcare, finance, legal, retail) | [orchestration/prompt_config_env.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/prompt_config_env.go) |
| `TRUVAG3_PROMPT_TYPE_RULES` | (none) | **Implemented** | JSON array of additional type rules | [orchestration/prompt_config_env.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/prompt_config_env.go) |
| `TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS` | (none) | **Implemented** | JSON array of custom instructions | [orchestration/prompt_config_env.go](https://github.com/truvaagents/truva-g3/blob/main/orchestration/prompt_config_env.go) |

### Example

```bash
export TRUVAG3_PROMPT_DOMAIN="healthcare"
export TRUVAG3_PROMPT_TYPE_RULES='[{"type_names":["uuid"],"json_type":"JSON strings","example":"\"abc-123\""}]'
export TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS='["Prefer local tools", "Minimize API calls"]'
```

---

## Quick Reference Table

### Essential Variables for Production (All Implemented)

```bash
# Core
export TRUVAG3_AGENT_NAME="my-agent"
export TRUVAG3_PORT=8080
export TRUVAG3_NAMESPACE="production"

# Discovery
export REDIS_URL="redis://redis:6379"

# AI (one of these)
export OPENAI_API_KEY="sk-..."
# or: GROQ_API_KEY, ANTHROPIC_API_KEY, etc.

# Telemetry
export OTEL_EXPORTER_OTLP_ENDPOINT="http://otel-collector:4318"
export OTEL_SERVICE_NAME="my-agent"

# Orchestration (for long-running AI workflows)
export TRUVAG3_ORCHESTRATION_TIMEOUT=5m
export TRUVAG3_HTTP_WRITE_TIMEOUT=5m
export TRUVAG3_HTTP_READ_TIMEOUT=5m
```

### Essential Variables for Development (All Implemented)

```bash
export TRUVAG3_DEV_MODE=true
export TRUVAG3_MOCK_DISCOVERY=true
export TRUVAG3_DEBUG=true
```

### Iterative Planning Variables (for Multi-Phase DAG)

```bash
# Enable multi-phase planning (default: true)
export TRUVAG3_ITERATIVE_PLANNING_ENABLED=true
export TRUVAG3_ITERATIVE_MAX_PHASES=5          # Max planning phases per request
export TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS=200   # Max total steps across all phases
export TRUVAG3_ITERATIVE_PHASE_TIMEOUT=180s    # Max duration per phase
```

### Step Retry Backoff Variables (for Retry Tuning)

```bash
# Exponential backoff for step retries (defaults shown)
export TRUVAG3_STEP_RETRY_INITIAL_DELAY=500ms   # Base delay, doubles per attempt
export TRUVAG3_STEP_RETRY_MAX_DELAY=10s          # Delay cap regardless of attempt count
```

### Result Data Management Variables (for Large Responses)

```bash
# Result trimming (enabled by default — prevents token overflow)
export TRUVAG3_RESULT_TRIM_ENABLED=true
export TRUVAG3_RESULT_TRIM_MAX_BYTES=16384                # 16 KB per result (~4K tokens)
export TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES=32768          # 32 KB total (~8K tokens)
export TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES=65536          # 64 KB for micro-resolution prompts
export TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES=65536    # 64 KB per agent/tool parameter
export TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD=16384 # 16 KB — schema mapping above this

# Result distillation (opt-in — adds LLM call per large result)
export TRUVAG3_RESULT_DISTILL_ENABLED=false
export TRUVAG3_RESULT_DISTILL_THRESHOLD=32768     # Min bytes to trigger
export TRUVAG3_RESULT_DISTILL_PREFILTER=32768     # Pre-filter budget
export TRUVAG3_RESULT_DISTILL_TARGET=4096         # LLM output target
export TRUVAG3_RESULT_DISTILL_MODEL=fast         # Model override (portable alias)
```

### Shared Memory Variables (for Cross-Agent Coordination)

```bash
# Domain scoping — agents in the same domain share memory
export TRUVAG3_AGENT_DOMAIN="infrastructure"

# Activity compaction — LLM-powered event digests
export TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL=fast     # Model for summarization/compaction
export TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS=500  # Digest output budget
export TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT=200  # Max events sent to compactor
export TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL=15  # Raw events after digest

# Digest caching — reduces redundant LLM calls
export TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL=5m       # Cache expiry
export TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD=20  # Above this → full recompact

# Activity coordination — real-time agent signals
export TRUVAG3_ACTIVITY_SIGNAL_TTL=5m                   # Signal expiry
export TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT=10         # Max signals in prompt
```

### LLM Debug Variables (for Troubleshooting)

```bash
# Enable debug payload storage (disabled by default)
export TRUVAG3_LLM_DEBUG_ENABLED=true
export TRUVAG3_LLM_DEBUG_TTL=24h           # Success record retention
export TRUVAG3_LLM_DEBUG_ERROR_TTL=168h    # Error record retention (7 days)
export TRUVAG3_LLM_DEBUG_REDIS_DB=7        # Redis database number
```

### Kubernetes ConfigMap Example

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: truvag3-config
data:
  TRUVAG3_AGENT_NAME: "research-agent"
  TRUVAG3_NAMESPACE: "production"
  TRUVAG3_LOG_LEVEL: "info"
  TRUVAG3_LOG_FORMAT: "json"
  TRUVAG3_DISCOVERY_CACHE: "true"
  OTEL_EXPORTER_OTLP_ENDPOINT: "http://otel-collector.observability:4318"
```

### Kubernetes Secret Example

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: truvag3-secrets
type: Opaque
stringData:
  OPENAI_API_KEY: "sk-..."
  REDIS_URL: "redis://:password@redis:6379"
```

---

## Configuration Priority

Configuration is resolved in three layers, applied sequentially (last write wins):

| Step | Source | Example | Priority |
|------|--------|---------|----------|
| 1 | `DefaultConfig()` | `AllowedHeaders: ["Content-Type", "Authorization"]` | Lowest |
| 2 | `LoadFromEnv()` | `TRUVAG3_CORS_HEADERS=Content-Type,Authorization,X-User-ID` | Medium |
| 3 | Functional options | `WithCORS([]string{"*"}, true)` | Highest |

Each step **merges into** the configuration struct. A later step only overwrites fields it explicitly sets — unset fields retain their value from earlier steps. For example, `WithCORS` sets `Enabled`, `AllowedOrigins`, and `AllowCredentials` but does not touch `AllowedHeaders`, so an env var override for headers survives.

This follows the standard Go / 12-factor convention (same as Viper, Cobra, Kong):
- **Defaults** provide safe baselines
- **Environment variables** allow deployment-time tuning without code changes (ideal for K8s ConfigMaps)
- **Functional options** express explicit developer intent and take precedence

> **See also:** `NewConfig()` in `core/config.go` documents this sequence in its godoc.

For environment variables with multiple names (e.g., `REDIS_URL` vs `TRUVAG3_REDIS_URL`):
- `TRUVAG3_REDIS_URL` takes precedence over `REDIS_URL`
- This allows framework-specific overrides while maintaining compatibility

---

## Variables Marked as "Struct Tag Only"

These variables are defined in Go struct tags with `env:"..."` annotations but are **not currently loaded** in the `LoadFromEnv()` function. They may work if you're using a reflection-based configuration loader, but the default `LoadFromEnv()` does not process them.

To use these variables, you would need to either:
1. Use programmatic configuration via functional options
2. Implement reflection-based struct tag parsing
3. Submit a PR to add explicit loading for these variables

---

## See Also

- [Core Module README](https://github.com/truvaagents/truva-g3/blob/main/core/README.md)
- [Orchestration README](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md)
- [AI Module README](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md)
- [Telemetry README](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md)
- [Kubernetes Deployment Guide](../operations/KUBERNETES.md)
- [Auto-Discovery Guide](../operations/AUTO_DISCOVERY_GUIDE.md)
