# TruvaG3 Framework Features Guide

## Overview

TruvaG3 is a Kubernetes-native agent framework for building AI systems as ordinary services: tools expose focused capabilities, agents discover and orchestrate those capabilities, and the platform keeps discovery, execution, memory, observability, and resilience inside your own infrastructure.

This guide is a feature map for the framework. It is intentionally organized around what the framework provides rather than around implementation files. Use it when evaluating TruvaG3, planning a new agent system, or deciding which deeper guide to read next.

## Table of Contents

- [Overview](#overview)
- [Table of Contents](#table-of-contents)
- [How To Read This Guide](#how-to-read-this-guide)
- [Feature Map](#feature-map)
  - [1. Component Runtime](#1-component-runtime)
    - [Tools](#tools)
    - [Agents](#agents)
    - [Framework Lifecycle](#framework-lifecycle)
    - [Capability Registration](#capability-registration)
    - [Generated HTTP APIs](#generated-http-apis)
    - [CORS And Middleware](#cors-and-middleware)
  - [2. Runtime Discovery](#2-runtime-discovery)
    - [Service Registry](#service-registry)
    - [Capability Discovery](#capability-discovery)
    - [Heartbeat And TTL Leases](#heartbeat-and-ttl-leases)
    - [Kubernetes Service-Fronted Discovery](#kubernetes-service-fronted-discovery)
  - [3. Tool Contracts And API Surfacing](#3-tool-contracts-and-api-surfacing)
    - [Capability Metadata](#capability-metadata)
    - [Input And Output Summaries](#input-and-output-summaries)
    - [Three-Phase Payload Generation](#three-phase-payload-generation)
    - [Schema Validation](#schema-validation)
    - [OpenAPI Generation](#openapi-generation)
  - [4. AI Provider Layer](#4-ai-provider-layer)
    - [Zero-Configuration Provider Detection](#zero-configuration-provider-detection)
    - [Single Client](#single-client)
    - [Chain Client Failover](#chain-client-failover)
    - [Provider Aliases](#provider-aliases)
    - [Model Aliases](#model-aliases)
    - [Provider Registry And Custom Providers](#provider-registry-and-custom-providers)
    - [Embeddings And Vector Integration](#embeddings-and-vector-integration)
    - [Reasoning Controls And Request Options](#reasoning-controls-and-request-options)
    - [Streaming And AI Telemetry](#streaming-and-ai-telemetry)
  - [5. Orchestration](#5-orchestration)
    - [Dynamic Natural-Language Orchestration](#dynamic-natural-language-orchestration)
    - [DAG Execution](#dag-execution)
    - [Context Window And Token Budget Management](#context-window-and-token-budget-management)
    - [Iterative Multi-Phase Planning](#iterative-multi-phase-planning)
    - [Workflow Modes](#workflow-modes)
    - [Streaming Orchestration](#streaming-orchestration)
    - [Clarification Requests](#clarification-requests)
    - [Execution Controls And Token Usage](#execution-controls-and-token-usage)
    - [Prompt Customization](#prompt-customization)
    - [Large Catalog Support](#large-catalog-support)
  - [6. Reliability And Error Recovery](#6-reliability-and-error-recovery)
    - [Structured Tool Errors](#structured-tool-errors)
    - [LLM Error Analysis](#llm-error-analysis)
    - [Semantic Retry](#semantic-retry)
    - [Result Trimming And Distillation](#result-trimming-and-distillation)
    - [Schema-Guided Mapping For Large Results](#schema-guided-mapping-for-large-results)
    - [Circuit Breakers](#circuit-breakers)
    - [Retry And Backoff](#retry-and-backoff)
    - [Panic Recovery](#panic-recovery)
  - [7. Async Execution, HITL, And Scheduling](#7-async-execution-hitl-and-scheduling)
    - [Async Tasks](#async-tasks)
    - [Progress Reporting](#progress-reporting)
    - [Human-in-the-Loop Approval](#human-in-the-loop-approval)
    - [Scheduled Execution](#scheduled-execution)
    - [Pluggable Task Backends](#pluggable-task-backends)
  - [8. Memory And Context](#8-memory-and-context)
    - [Component Key-Value Memory](#component-key-value-memory)
    - [Pipeline Hooks](#pipeline-hooks)
    - [Shared Agent Memory](#shared-agent-memory)
    - [Per-User Memory](#per-user-memory)
    - [Conversation History Protection](#conversation-history-protection)
    - [RAG, Caching, And Guardrails](#rag-caching-and-guardrails)
  - [9. Chat Agent Features](#9-chat-agent-features)
    - [SSE Streaming](#sse-streaming)
    - [Session Storage](#session-storage)
    - [Multi-Turn Context](#multi-turn-context)
    - [HITL Resume For Chat](#hitl-resume-for-chat)
  - [10. Observability And Developer Tools](#10-observability-and-developer-tools)
    - [OpenTelemetry Metrics](#opentelemetry-metrics)
    - [Distributed Tracing](#distributed-tracing)
    - [Structured Logging](#structured-logging)
    - [LLM And Execution Debug Stores](#llm-and-execution-debug-stores)
    - [Registry Viewer](#registry-viewer)
    - [Swagger UI](#swagger-ui)
  - [11. Security And Request Propagation](#11-security-and-request-propagation)
    - [Security Model](#security-model)
    - [OAuth Bearer Propagation](#oauth-bearer-propagation)
    - [Runtime Token Refresh](#runtime-token-refresh)
    - [Custom Header Propagation](#custom-header-propagation)
    - [Protected Framework Headers](#protected-framework-headers)
    - [OpenAPI And Developer Tool Exposure](#openapi-and-developer-tool-exposure)
    - [Privacy, Guardrails, And Audit Hooks](#privacy-guardrails-and-audit-hooks)
    - [Kubernetes Secrets And Config](#kubernetes-secrets-and-config)
    - [Platform Security Controls](#platform-security-controls)
  - [12. Deployment And Operations](#12-deployment-and-operations)
    - [Kubernetes-Native Deployment](#kubernetes-native-deployment)
    - [Environment Configuration](#environment-configuration)
    - [Self-Hosted Operation](#self-hosted-operation)
    - [Runtime Backends](#runtime-backends)
  - [13. Extension Points](#13-extension-points)
    - [Pluggable Interfaces](#pluggable-interfaces)
    - [Custom Hooks](#custom-hooks)
    - [Custom Prompt Builders](#custom-prompt-builders)
    - [Testing And Conformance Helpers](#testing-and-conformance-helpers)
    - [MCP Integration Paths](#mcp-integration-paths)
- [Feature-To-Module Index](#feature-to-module-index)
- [Documentation Index](#documentation-index)
- [Known Documentation Gaps](#known-documentation-gaps)

## How To Read This Guide

Start with [Feature Map](#feature-map) for the current framework surface. Each feature group links to the docs that go deeper.

For implementation details, prefer the module READMEs and architecture docs:

- [core/README.md](https://github.com/truvaagents/truva-g3/blob/main/core/README.md)
- [ai/README.md](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md)
- [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md)
- [memory/README.md](https://github.com/truvaagents/truva-g3/blob/main/memory/README.md)
- [resilience/README.md](https://github.com/truvaagents/truva-g3/blob/main/resilience/README.md)
- [telemetry/README.md](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md)

## Feature Map

### 1. Component Runtime

TruvaG3 models an agent system as independently deployed components. The runtime distinction between tools and agents is central to the framework.

#### Tools

Tools are passive components. They register capabilities, expose HTTP endpoints, and execute focused work when called. They can call external APIs, databases, internal services, or AI providers as part of their own implementation, but they do not discover or orchestrate other TruvaG3 components.

Typical tool features:

- independent process and Kubernetes deployment
- one or more registered capabilities
- HTTP capability endpoints
- health endpoint support
- structured success/error responses
- optional OpenAPI exposure

See [Tool Development Guide](../building/TOOL_DEVELOPMENT_GUIDE.md) and [core/README.md](https://github.com/truvaagents/truva-g3/blob/main/core/README.md).

#### Agents

Agents are active components. They can register their own capabilities, discover tools and other agents, maintain request context, and coordinate complex work.

Typical agent features:

- component registration and discovery
- AI client integration
- orchestration support
- optional session, memory, and telemetry wiring
- ability to expose capabilities other agents can call

See [Agent Development Guide](../building/AGENT_DEVELOPMENT_GUIDE.md), [Chat Agent Guide](../memory-and-chat/CHAT_AGENT_GUIDE.md), and [core/README.md](https://github.com/truvaagents/truva-g3/blob/main/core/README.md).

#### Framework Lifecycle

The core framework handles common runtime plumbing:

- configuration loading from environment variables and functional options
- HTTP server startup
- component registration
- health endpoints
- CORS configuration
- middleware wiring
- graceful shutdown
- logger propagation to framework modules

See [API Reference](../reference/API_REFERENCE.md#core-module) and [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md).

#### Capability Registration

Capabilities describe what a component can do. A registered capability gives the framework enough information to expose an endpoint, advertise the component in discovery, help LLMs choose tools, and optionally generate schemas and OpenAPI.

Capability metadata can include:

- name
- description
- endpoint
- input and output type hints
- input summary
- output summary
- generated schema endpoint
- internal flag
- capability type

See [Tool Development Guide](../building/TOOL_DEVELOPMENT_GUIDE.md#5-step-3-register-capabilities).

#### Generated HTTP APIs

Registered capabilities become HTTP-callable APIs. If a capability does not provide an explicit endpoint, the framework can generate one at `/api/capabilities/{name}`. Components also expose capability catalog endpoints that are useful for readiness checks, manual testing, and developer tooling.

Common generated or framework-managed endpoints include:

- `/health`
- `/api/capabilities`
- `/api/capabilities/{name}`
- `/api/capabilities/{name}/schema` when schema metadata is available
- `/openapi.json` when OpenAPI generation is enabled

See [core/README.md](https://github.com/truvaagents/truva-g3/blob/main/core/README.md#registering-capabilities-making-your-components-useful) and [Tool Development Guide](../building/TOOL_DEVELOPMENT_GUIDE.md#9-testing-your-tool).

#### CORS And Middleware

The framework can wire HTTP middleware around component handlers. Built-in and documented patterns include CORS configuration, tracing middleware, and custom middleware for cross-cutting concerns.

This is useful when exposing agents or tools to browser clients, service meshes, gateways, or internal platform middleware.

See [core/README.md](https://github.com/truvaagents/truva-g3/blob/main/core/README.md), [Tool Development Guide](../building/TOOL_DEVELOPMENT_GUIDE.md#complete-main-implementation), and [Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md).

### 2. Runtime Discovery

Discovery lets agents find tools and other agents by what they can do rather than by hardcoded addresses.

#### Service Registry

Redis/Valkey is the default service registry backend. The registry stores component records and capability indexes. The discovery interface is pluggable, so applications can replace the backend behind the core interfaces.

See [Auto-Discovery Guide](../operations/AUTO_DISCOVERY_GUIDE.md).

#### Capability Discovery

Agents can discover components by:

- capability
- service name
- component type
- metadata filters
- combined discovery filters

This supports dynamic orchestration: newly deployed tools become available to agents without redeploying those agents.

See [Auto-Discovery Guide](../operations/AUTO_DISCOVERY_GUIDE.md#how-agents-discover-tools-and-other-agents).

#### Heartbeat And TTL Leases

Components register with TTL-backed leases and refresh those leases with heartbeats. If a component crashes or stops heartbeating, its registry entry expires automatically.

Discovery supports:

- short-lived component identity leases
- longer-lived capability indexes
- automatic cleanup of dead components
- multi-pod behavior where healthy replicas keep service-level capability indexes alive

See [Auto-Discovery Guide](../operations/AUTO_DISCOVERY_GUIDE.md#heartbeat-and-ttl-management).

#### Kubernetes Service-Fronted Discovery

In Kubernetes, components can register stable service URLs instead of pod-local URLs. This lets agents call tools through Kubernetes Services and use native load balancing.

This model is compatible with transparent service meshes such as Istio and Linkerd. TruvaG3 still advertises the Kubernetes Service DNS name, while the mesh sidecars and mesh control plane can handle mTLS, traffic routing, policy, retries, and mesh telemetry. Mesh-specific routing should be configured in the mesh itself, such as with Istio `VirtualService` and `DestinationRule` resources or Gateway API `HTTPRoute` resources.

TruvaG3 does not currently provide first-class registration for custom mesh gateway hosts, custom `VirtualService` hosts, full advertised URLs, or path-prefixed gateway routes. That is intentional for now: the framework keeps discovery centered on Kubernetes Services and lets the platform own mesh-specific routing.

Relevant configuration includes:

- `TRUVAG3_K8S_SERVICE_NAME`
- `TRUVAG3_K8S_SERVICE_PORT`
- `TRUVAG3_K8S_NAMESPACE`
- `TRUVAG3_K8S_POD_IP`

See [Auto-Discovery Guide §"Address Resolution"](../operations/AUTO_DISCOVERY_GUIDE.md#address-resolution-pod-ip-vs-service-dns) and [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#kubernetes-deployment-requirements).

### 3. Tool Contracts And API Surfacing

TruvaG3 treats tool contracts as runtime metadata, not a separate hand-maintained specification.

#### Capability Metadata

Capability descriptions help both humans and LLM planners understand when a capability should be used. Good descriptions are the baseline for payload generation and tool selection.

See [Tool Development Guide](../building/TOOL_DEVELOPMENT_GUIDE.md#writing-effective-descriptions).

#### Input And Output Summaries

`InputSummary` and `OutputSummary` provide structured field hints. They help LLMs generate correct JSON and help the framework generate schemas and OpenAPI.

See [Tool Development Guide](../building/TOOL_DEVELOPMENT_GUIDE.md#inputsummary-components).

#### Three-Phase Payload Generation

TruvaG3 uses progressive payload guidance:

1. Description-based generation is always available.
2. Field-hint-based generation is recommended.
3. Schema-based validation is optional.

This lets simple tools stay simple while production tools can add stronger validation.

See [Tool Schema Discovery Guide](../building/TOOL_SCHEMA_DISCOVERY_GUIDE.md).

#### Schema Validation

When enabled, the orchestrator can fetch capability JSON Schemas from tool endpoints, cache them, and validate LLM-generated payloads before sending tool calls.

See [API Reference](../reference/API_REFERENCE.md#schema-cache-phase-3-validation).

#### OpenAPI Generation

When enabled, each component can expose `/openapi.json`, generated from registered capabilities. This supports Swagger UI, developer portals, API gateways, and human inspection.

The endpoint is disabled by default and can be enabled by configuration.

See [Developer Tools Guide](../operations/DEV_TOOLS_GUIDE.md#2-the-openapijson-contract).

### 4. AI Provider Layer

The AI module gives agents a provider-neutral interface for LLM calls.

#### Zero-Configuration Provider Detection

`ai.NewClient()` can scan the environment, detect configured providers, and create a working client without hardcoding a provider in application code. This is useful for local development, promotion between environments, and running the same container image with different provider settings.

See [ai/README.md](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md#method-1-zero-configuration-auto-pilot) and [AI Providers Setup Guide](../building/AI_PROVIDERS_SETUP_GUIDE.md#how-auto-configuration-works).

#### Single Client

The single client connects to one provider. It can auto-detect configured providers from environment variables or be configured explicitly.

See [AI Providers Setup Guide](../building/AI_PROVIDERS_SETUP_GUIDE.md#single-client-the-simple-path).

#### Chain Client Failover

The chain client tries multiple providers in order and fails over when errors are retryable. This supports high availability and provider outage handling.

See [AI Providers Setup Guide](../building/AI_PROVIDERS_SETUP_GUIDE.md#chain-client-production-grade-resilience).

#### Provider Aliases

Provider aliases allow clean, environment-driven configuration. Current documented aliases include native providers and OpenAI-compatible endpoints:

- `openai`
- `anthropic`
- `gemini`
- `bedrock`
- `openai.groq`
- `openai.deepseek`
- `openai.xai`
- `openai.mistral`
- `openai.qwen`
- `openai.together`
- `openai.ollama`

The native Bedrock provider is build-tagged and requires importing the Bedrock provider package and building with the `bedrock` tag.

See [AI Providers Setup Guide](../building/AI_PROVIDERS_SETUP_GUIDE.md#provider-aliases-the-clean-way-to-configure).

#### Model Aliases

Model aliases provide portable names, including `default`, `fast`, `smart`, `premium`, `code`, and `vision`, that resolve differently per provider. This lets application code request a model role without hardcoding provider-specific model IDs.

See [AI Providers Setup Guide](../building/AI_PROVIDERS_SETUP_GUIDE.md#model-aliases-portable-model-names).

#### Provider Registry And Custom Providers

The AI module has a provider registry. Providers register themselves through package initialization, and teams can add custom providers or OpenAI-compatible services behind the same `AIClient` interface.

This matters when evaluating the framework for private endpoints, internal gateways, new provider integrations, or self-hosted models.

See [ai/README.md](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md#the-provider-registry---plugin-architecture), [ai/README.md](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md#creating-custom-providers), and [ai/README.md](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md#adding-new-openai-compatible-services).

#### Embeddings And Vector Integration

The AI layer includes embedding-capable interfaces for providers that support embeddings. The memory module can use an embedding client with Qdrant-backed shared knowledge and user memory.

This prevents agents from having to own vectorization code when adding semantic recall, shared knowledge search, or user memory.

See [ai/README.md](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md#provider-capabilities), [memory/README.md](https://github.com/truvaagents/truva-g3/blob/main/memory/README.md), and [API Reference](../reference/API_REFERENCE.md#shared-memory-interfaces).

#### Reasoning Controls And Request Options

AI requests support common portable options such as temperature, max tokens, and system prompts. The documented provider setup also supports reasoning-model controls, request timeouts, token multipliers for reasoning models, and provider-specific escape hatches.

Orchestration can override AI options per phase, including planning, synthesis, micro-resolution, error analysis, tiered selection, and result distillation.

See [AI Providers Setup Guide](../building/AI_PROVIDERS_SETUP_GUIDE.md#portable-fields-vs-provider-specific-escape-hatches), [AI Providers Setup Guide](../building/AI_PROVIDERS_SETUP_GUIDE.md#reasoning-model-support), and [API Reference](../reference/API_REFERENCE.md#createorchestratorwithoptions).

#### Streaming And AI Telemetry

AI clients support non-streaming and streaming responses. When telemetry is initialized before the AI client, AI operations can emit spans and metrics such as provider, model, request duration, and token counts.

See [ai/README.md](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md#15-streaming-support) and [telemetry/README.md](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md#20-ai-module-distributed-tracing).

### 5. Orchestration

The orchestration module coordinates tools and agents to satisfy user requests.

#### Dynamic Natural-Language Orchestration

Dynamic mode lets users describe a request in natural language. The orchestrator asks an LLM to select tools, produce a plan, execute it, and synthesize the results.

Use this for chat interfaces, exploratory workflows, and systems where the needed tools vary by request.

See [Orchestration Modes Guide](../orchestration/ORCHESTRATION_MODES_GUIDE.md#3-dynamic-mode-let-the-ai-decide).

#### DAG Execution

Plans are modeled as directed acyclic graphs. Independent steps can run in parallel, while dependent steps wait for upstream results.

Features include:

- dependency-aware execution
- template references to prior step outputs
- result aggregation
- step-level callbacks
- retry-aware execution

See [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md#3-how-it-works).

#### Context Window And Token Budget Management

The orchestration layer includes several mechanisms to avoid context-window overflow as the number of tools, conversation turns, and tool outputs grow:

- tiered capability selection sends lightweight tool summaries first, then fetches full schemas only for selected tools
- tiered selection is default for medium catalogs and is configurable through `TRUVAG3_TIERED_RESOLUTION_ENABLED`, `TRUVAG3_TIERED_MIN_TOOLS`, and `TRUVAG3_TIERED_SELECTION_MAX_TOKENS`
- iterative planning can re-select tools in later phases using prior tools, continuation notes, and compact result summaries
- continuation planning has a per-step result character limit so large delegated-agent responses do not swamp later planning prompts
- orchestration responses expose token usage by phase, including `planning`, `tiered_selection`, `synthesis`, `distillation`, and retry phases

This is the planning-side context optimization layer. Large output handling is covered separately by [Result Trimming And Distillation](#result-trimming-and-distillation), and long chat sessions are covered by [Conversation History Protection](#conversation-history-protection).

See [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md#16-scaling-to-hundreds-of-agents---capability-provider-architecture), [API Reference](../reference/API_REFERENCE.md#tieredcapabilityconfig), and [Limits Cheatsheet](../reference/LIMITS_CHEATSHEET.md#tiered-capability-resolution).

#### Iterative Multi-Phase Planning

For complex requests, the planner can generate partial plans, execute a phase, feed results back into planning, and continue until the plan is terminal or configured limits are reached.

This supports research-style workflows where the first phase discovers context and later phases choose different tools based on what was found. Limits exist for maximum phases, total steps, phase timeout, and continuation prompt size.

See [API Reference](../reference/API_REFERENCE.md#iterativeplanconfig) and [Limits Cheatsheet](../reference/LIMITS_CHEATSHEET.md#iterative-planning-multi-phase-dag).

#### Workflow Modes

The framework supports multiple orchestration styles:

- dynamic mode for LLM-generated plans
- predefined workflow mode for deterministic pipelines
- custom mode for exact step control and debugging
- YAML workflow engine for declarative workflows

See [Orchestration Modes Guide](../orchestration/ORCHESTRATION_MODES_GUIDE.md).

#### Streaming Orchestration

Streaming orchestration emits progress and response events while work is running. This supports chat-style UX and long-running workflows where users need visibility before the final answer is complete.

See [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md#18-streaming-support).

#### Clarification Requests

When the planner needs user-provided information to proceed, the orchestrator can surface a structured clarification request. Simple chat UIs can forward the natural-language response, while richer UIs can render structured prompts from the `Clarification` field.

See [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md#clarification-requests) and [API Reference](../reference/API_REFERENCE.md#processrequeststreaming).

#### Execution Controls And Token Usage

Orchestration exposes controls for production behavior and cost management:

- max parallel step execution
- per-step and total timeouts
- plan parse retry
- hallucinated agent retry
- per-phase AI option overrides
- step completion callbacks
- aggregated token usage and token usage by orchestration phase

See [API Reference](../reference/API_REFERENCE.md#createorchestratorwithoptions), [API Reference](../reference/API_REFERENCE.md#processrequeststreaming), and [Limits Cheatsheet](../reference/LIMITS_CHEATSHEET.md).

#### Prompt Customization

Prompt customization supports:

- `PromptConfig`
- additional type rules
- system instructions
- domain-specific instructions
- templates
- custom prompt builders
- iterative planning instructions

See [LLM Planning Prompt Guide](../orchestration/LLM_PLANNING_PROMPT_GUIDE.md) and [Effective Prompts Guide](../building/EFFECTIVE_PROMPTS_GUIDE.md).

#### Large Catalog Support

For large systems, capability providers reduce prompt bloat and help the planner work with relevant subsets of a large tool/agent catalog.

See [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md#16-scaling-to-hundreds-of-agents---capability-provider-architecture).

### 6. Reliability And Error Recovery

TruvaG3 provides both generic resilience primitives and orchestration-specific recovery behavior.

#### Structured Tool Errors

Tools communicate failures through structured error categories and retry hints. The shared protocol includes `ToolError`, `ToolResponse`, and upstream error classification helpers.

See [Intelligent Error Handling Guide](../orchestration/INTELLIGENT_ERROR_HANDLING.md) and [Error Handling Guide](../orchestration/ERROR_HANDLING_GUIDE.md).

#### LLM Error Analysis

For input-like failures, the orchestrator can ask an LLM to inspect the error, original payload, capability metadata, and user request, then suggest corrected parameters.

See [Error Handling Guide](../orchestration/ERROR_HANDLING_GUIDE.md#5-llm-error-analyzer-layer-3).

#### Semantic Retry

Semantic retry performs contextual re-resolution when the original parameters were wrong or incomplete. This helps recover from failures such as ambiguous locations, invalid codes, or values that need to be derived from prior results.

See [Error Handling Guide](../orchestration/ERROR_HANDLING_GUIDE.md#6-contextual-re-resolution-layer-4).

#### Result Trimming And Distillation

Large tool and agent results can be processed before they are embedded in LLM prompts. This prevents token budget overflow during synthesis, continuation planning, micro-resolution, and downstream agent/tool calls.

Built-in controls include:

- structural trimming enabled by default
- per-result and total prompt byte budgets
- micro-resolution source-data budget
- downstream agent/tool input budget
- content-aware field scoring and relevance-aware field selection
- preservation of JSON structure, key names, and representative samples
- trim metadata on step results and `result_trim.completed` span events

For extremely large or domain-specific outputs, opt-in result distillation adds a two-stage pipeline:

1. structural pre-filtering reduces the source result
2. an LLM summarizes the pre-filtered content to a target size

The trimming contract is pluggable through the `ResultProcessor` interface, so teams can replace the default `StructuralTrimmer` with a domain-specific implementation.

See [API Reference](../reference/API_REFERENCE.md#resulttrimconfig), [API Reference](../reference/API_REFERENCE.md#resultdistillconfig), [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#result-trimming-large-result-data-management), and [Limits Cheatsheet](../reference/LIMITS_CHEATSHEET.md#result-trimming-large-data).

#### Schema-Guided Mapping For Large Results

When prior step results are too large for direct LLM parameter extraction, schema-guided mapping can use JSON schema analysis instead of sending raw result data into the micro-resolution prompt.

This is controlled by `TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD`; setting it to `0` disables schema-guided mapping.

See [API Reference](../reference/API_REFERENCE.md#resulttrimconfig) and [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#result-trimming-large-result-data-management).

#### Circuit Breakers

Circuit breakers prevent cascading failures by failing fast after repeated errors, then testing recovery through half-open probes.

See [resilience/README.md](https://github.com/truvaagents/truva-g3/blob/main/resilience/README.md#basic-circuit-breaker).

#### Retry And Backoff

Retry logic supports exponential backoff, jitter, context cancellation, and maximum delay caps.

See [resilience/README.md](https://github.com/truvaagents/truva-g3/blob/main/resilience/README.md#smart-retry-with-backoff) and [Error Handling Guide](../orchestration/ERROR_HANDLING_GUIDE.md#7-step-retry-and-backoff).

#### Panic Recovery

The resilience module includes panic recovery helpers so unexpected panics can be converted into errors, logged, and handled without tearing down the process.

See [resilience/README.md](https://github.com/truvaagents/truva-g3/blob/main/resilience/README.md#panic-recovery-system).

### 7. Async Execution, HITL, And Scheduling

Some agent workflows are too long-running or sensitive for a single synchronous request.

#### Async Tasks

Async tasks implement the HTTP 202 plus polling pattern:

1. API accepts the request and returns a task ID.
2. Worker processes the task in the background.
3. Client polls for status, progress, and final result.

See [Async Orchestration Guide](../orchestration/ASYNC_ORCHESTRATION_GUIDE.md).

#### Progress Reporting

Async task handlers can publish progress updates. Orchestration step callbacks can feed task progress so clients see meaningful state while execution runs.

See [Async Orchestration Guide](../orchestration/ASYNC_ORCHESTRATION_GUIDE.md#7-progress-reporting).

#### Human-in-the-Loop Approval

HITL checkpoints pause execution until a human approves, rejects, edits, or lets the checkpoint expire.

Supported interrupt points include:

- plan approval
- before-step approval
- error escalation

HITL supports Redis-backed checkpoint storage, webhook-style notification patterns, command handling, expiry processing, and resume flows.

See [Human-in-the-Loop User Guide](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md).

#### Scheduled Execution

Scheduled execution lets agents schedule future work:

- one-shot delay
- absolute time
- recurring cron

The scheduler tool creates schedules, the scheduled executor dispatches due work, and target agents receive scheduled instructions through a registered endpoint.

See [Scheduled Tasks Guide](../orchestration/SCHEDULED_TASKS_GUIDE.md).

#### Pluggable Task Backends

Async tasks and scheduled execution use related but distinct backend interfaces:

- async worker flow: `TaskQueue` and `TaskStore`
- scheduled producer and consumer flow: `ScheduleStore`, `TaskDispatcher`, `TaskConsumer`, and `TaskHandle`

Redis list-backed implementations cover the default async path and the default scheduled consumer. Redis Streams supports at-least-once scheduled task consumption, and in-memory implementations support development and tests. Conformance tests help validate custom `TaskConsumer` implementations.

See [Scheduled Tasks Guide](../orchestration/SCHEDULED_TASKS_GUIDE.md#extending-writing-your-own-backend).

### 8. Memory And Context

Memory and context features let agents avoid starting from zero on every request.

#### Component Key-Value Memory

The core module defines a simple component memory interface with `Get`, `Set`, `Delete`, and `Exists`. Components can use this for small state, cached values, and local context. In-memory storage supports development and tests, while Redis-backed memory is the production path for distributed instances.

This is separate from shared agent memory and user memory: it is the lightweight per-component storage primitive.

See [core/README.md](https://github.com/truvaagents/truva-g3/blob/main/core/README.md) and [API Reference](../reference/API_REFERENCE.md#memory-implementations).

#### Pipeline Hooks

Pipeline hooks run at defined stages:

- before planning
- after planning
- after execution
- after synthesis

Hooks can inject context, short-circuit the pipeline, mutate plans, observe results, or post-process responses. Hooks are fail-open by design.

See [Adding Context To Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md).

#### Shared Agent Memory

Shared memory gives agents in the same domain visibility into recent work and learned knowledge.

Features include:

- episodic event recording
- investigation coordination
- real-time activity coordination
- digest caching
- vector-backed shared knowledge
- LLM-powered memory reflection

See [memory/README.md](https://github.com/truvaagents/truva-g3/blob/main/memory/README.md) and [Agent Memory User Guide](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md).

#### Per-User Memory

Per-user memory supports personalization for assistant-like agents. It can extract user facts from conversations, reconcile them against existing facts, and recall relevant facts later through semantic search.

Features include:

- user-scoped facts
- ADD/UPDATE/CONTRADICT/DUPLICATE reconciliation
- semantic recall
- GDPR-style forget support

See [Agent Memory User Guide](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md#user-memory-per-user-personalization).

#### Conversation History Protection

Conversation history preparation gives chat agents multi-turn context while protecting prompt budgets.

Features include:

- raw turn metadata
- prompt-safe conversation history enrichment
- Tier 1 token budgeting
- Tier 2 recursive compaction for long sessions
- reuse across planning, continuation, and synthesis prompts

See [Conversation History Guide](../memory-and-chat/CONVERSATION_HISTORY_GUIDE.md).

#### RAG, Caching, And Guardrails

Pipeline hooks can implement:

- RAG context injection
- semantic caching
- response filtering
- PII redaction
- analytics logging
- custom context enrichment

See [Adding Context To Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md).

### 9. Chat Agent Features

The chat guides describe a reference pattern for production chat agents built on top of orchestration.

#### SSE Streaming

Server-Sent Events can stream status, step progress, token chunks, errors, and completion events to the frontend.

See [Chat Agent Guide](../memory-and-chat/CHAT_AGENT_GUIDE.md#sse-streaming-real-time-responses).

#### Session Storage

Example chat agents use Redis-backed session storage. Sessions store metadata and bounded message history with TTL.

See [Chat Session Management Guide](../memory-and-chat/CHAT_SESSION_MANAGEMENT_GUIDE.md).

#### Multi-Turn Context

Chat handlers convert stored messages into conversation turns and pass them into orchestration metadata. The orchestration layer prepares the prompt-safe conversation history.

See [Conversation History Guide](../memory-and-chat/CONVERSATION_HISTORY_GUIDE.md).

#### HITL Resume For Chat

The DevOps chat example carries session context through HITL interruptions so an approval resume can continue the same conversation when the session is still available.

See [Chat Session Management Guide](../memory-and-chat/CHAT_SESSION_MANAGEMENT_GUIDE.md).

### 10. Observability And Developer Tools

TruvaG3 is built around OpenTelemetry-compatible observability and developer-facing runtime inspection.

#### OpenTelemetry Metrics

Telemetry supports counters, histograms, and gauges, plus unified helper functions for common framework metrics.

Features include:

- request metrics
- tool call metrics
- AI request metrics
- token metrics
- error metrics
- service type labels
- cardinality protection
- graceful degradation if telemetry backends fail

See [telemetry/README.md](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md) and [API Reference](../reference/API_REFERENCE.md#telemetry-module).

#### Distributed Tracing

Tracing uses W3C TraceContext propagation. The telemetry module provides server middleware and traced HTTP clients.

Features include:

- incoming trace extraction
- span creation
- outgoing trace injection
- trace-log correlation
- request filtering
- custom span names
- AI spans
- async boundary linking
- HITL cross-trace correlation

See [Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md).

#### Structured Logging

The framework provides a logger interface with basic and context-aware methods. Context-aware logging carries trace IDs when request context is available.

See [Logging Implementation Guide](../observability/LOGGING_IMPLEMENTATION_GUIDE.md).

#### LLM And Execution Debug Stores

Debug stores can persist full LLM payloads and execution records with TTLs for troubleshooting orchestration behavior. The telemetry package also includes LLM call recording interfaces for storing request/response metadata outside the request path.

See [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md#llm-debug-configuration), [API Reference: LLM Debug Store](../reference/API_REFERENCE.md#llm-debug-store), and [API Reference: LLM Call Recording](../reference/API_REFERENCE.md#llm-call-recording).

#### Registry Viewer

The Registry Viewer is a developer-facing runtime dashboard. It can inspect:

- service registry
- LLM debug records
- HITL checkpoints
- execution DAGs
- shared memory

See [Developer Tools Guide](../operations/DEV_TOOLS_GUIDE.md#7-registry-viewer-overview).

#### Swagger UI

Swagger UI consumes generated OpenAPI specs from components that expose `/openapi.json`. It supports interactive API exploration and can integrate with developer portals and API gateways.

See [Developer Tools Guide](../operations/DEV_TOOLS_GUIDE.md#2-the-openapijson-contract).

### 11. Security And Request Propagation

Security features focus on propagating authentication and request metadata through orchestrated calls, keeping sensitive configuration out of code, and aligning agent systems with normal Kubernetes and service-platform controls.

#### Security Model

TruvaG3 components are ordinary HTTP services. The framework does not replace your API gateway, identity provider, service mesh, Kubernetes RBAC, network policies, mTLS, or secrets management. Instead, it provides the hooks and propagation points needed to fit into those systems.

This means the framework helps with:

- forwarding user or service tokens to downstream tools
- propagating tenant, audit, and correlation metadata
- preventing propagated headers from overriding framework-reserved headers
- keeping API keys and provider credentials in environment-backed secrets
- exposing runtime metadata only when explicitly enabled
- adding guardrails, redaction, and audit behavior through hooks and prompt builders

See [README.md](https://github.com/truvaagents/truva-g3/blob/main/README.md#what-makes-truvag3-unique-dynamic-agent-discovery-vendor-agnostic-microservice-native-ai), [OAuth Security Guide](../operations/OAUTH_SECURITY_GUIDE.md#security-architecture), and [Kubernetes Deployment Guide](../operations/KUBERNETES.md#️-security-best-practices).

#### OAuth Bearer Propagation

The orchestrator can attach Bearer tokens to outbound tool and agent calls through:

- static configuration
- runtime token setters
- per-request context injection

See [OAuth Security Guide](../operations/OAUTH_SECURITY_GUIDE.md).

#### Runtime Token Refresh

OAuth tokens and propagated headers can be updated at runtime. This supports service-to-service token refresh without rebuilding the orchestrator or stopping in-flight requests.

See [OAuth Security Guide](../operations/OAUTH_SECURITY_GUIDE.md#runtime-token-refresh).

#### Custom Header Propagation

Custom headers support multi-tenant routing, audit metadata, and correlation IDs. Context-level headers override config-level defaults, while protected framework headers cannot be overridden by propagated headers.

See [OAuth Security Guide](../operations/OAUTH_SECURITY_GUIDE.md#custom-header-propagation).

#### Protected Framework Headers

The executor reserves framework-critical headers such as `Content-Type`, `Authorization`, `X-TruvaG3-Request-ID`, and `X-TruvaG3-Step-ID`. Propagated headers cannot override these reserved values, which prevents accidental breakage of authentication, content handling, or trace/request correlation.

See [OAuth Security Guide](../operations/OAUTH_SECURITY_GUIDE.md#header-injection-order).

#### OpenAPI And Developer Tool Exposure

OpenAPI generation is disabled by default because it exposes component API surface. Schema endpoints are generated when schema metadata is available. Teams should treat `/openapi.json`, schema endpoints, Swagger UI, and the Registry Viewer as developer/operator tooling and protect them at the network, gateway, or mesh layer.

Developer tooling has additional privacy boundaries: the Registry Viewer intentionally does not expose per-user memory contents in its shared-memory views.

See [Developer Tools Guide](../operations/DEV_TOOLS_GUIDE.md#6-security-considerations) and [Developer Tools Guide](../operations/DEV_TOOLS_GUIDE.md#7-registry-viewer-overview).

#### Privacy, Guardrails, And Audit Hooks

Pipeline hooks and custom prompt builders provide extension points for application-specific controls:

- PII redaction before returning responses
- content policy checks
- tenant-aware prompt construction
- audit logging for prompts and LLM calls
- domain-specific compliance instructions
- filtering or enriching context before planning

For streaming responses, note that `AfterSynthesis` hooks run after tokens have already streamed; use pre-planning hooks, guarded prompt builders, or non-streaming paths when content must be blocked before user delivery.

See [Adding Context To Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md#7-scenario-response-guardrails-and-content-filtering), [LLM Planning Prompt Guide](../orchestration/LLM_PLANNING_PROMPT_GUIDE.md#detailed-example-soc-2-audit-logging-promptbuilder), and [Developer Tools Guide](../operations/DEV_TOOLS_GUIDE.md#7-registry-viewer-overview).

#### Kubernetes Secrets And Config

API keys, OAuth tokens, and provider keys should be managed through Kubernetes Secrets. Non-sensitive deployment settings can be managed through ConfigMaps.

See [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md) and [AI Providers Setup Guide](../building/AI_PROVIDERS_SETUP_GUIDE.md#kubernetes-deployment).

#### Platform Security Controls

The Kubernetes docs describe the platform controls expected around TruvaG3 deployments:

- Kubernetes Secrets for AI provider keys and service credentials
- ConfigMaps for non-sensitive runtime configuration
- non-root containers and pod security contexts
- NetworkPolicies for service-to-service restrictions
- ServiceAccounts and RBAC with minimal permissions
- ingress, gateway, service mesh, or mTLS controls at trust boundaries

See [Kubernetes Deployment Guide](../operations/KUBERNETES.md#️-security-best-practices) and [AI Providers Setup Guide](../building/AI_PROVIDERS_SETUP_GUIDE.md#managing-api-keys-with-secrets).

### 12. Deployment And Operations

TruvaG3 is designed to run inside existing platform infrastructure.

#### Kubernetes-Native Deployment

The framework aligns with ordinary Kubernetes primitives:

- Deployments
- Services
- Ingress
- ConfigMaps
- Secrets
- health probes
- namespace isolation
- rolling updates

See [Kubernetes Deployment Guide](../operations/KUBERNETES.md).

#### Environment Configuration

The framework can be configured through environment variables and functional options. The environment guide marks variables by implementation status and groups them by subsystem.

See [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md).

#### Self-Hosted Operation

TruvaG3 is intended for teams that want agents, tools, discovery, traces, and runtime data inside their own environment. This supports restricted, regulated, and air-gapped-friendly deployments.

See [README.md](https://github.com/truvaagents/truva-g3/blob/main/README.md#25-enterprise-deployment-model-run-agent-ecosystems-inside-your-existing-kubernetes-platform).

#### Runtime Backends

Common runtime backends include:

- Redis/Valkey for discovery, sessions, memory events, scheduling, async tasks, Redis Streams consumers, and debug stores
- Qdrant for vector-backed shared knowledge and user memory
- OpenTelemetry Collector for metrics and traces
- Prometheus, Jaeger, Grafana, and Loki in the example stack

See [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md) and [memory/README.md](https://github.com/truvaagents/truva-g3/blob/main/memory/README.md).

### 13. Extension Points

TruvaG3 exposes interfaces at the boundaries where teams are likely to need replacement or customization.

#### Pluggable Interfaces

Examples include:

- discovery and registry interfaces
- AI client interface
- telemetry interface
- memory interfaces
- task queue and task store interfaces for async work
- schedule store, scheduler dispatcher, task consumer, and task handle interfaces
- LLM debug, execution debug, and LLM call recording store interfaces
- embedding client interface

See [API Reference](../reference/API_REFERENCE.md#interfaces).

#### Custom Hooks

Pipeline hooks are the main extension point for context engineering, caching, memory, guardrails, auditing, and background side effects.

See [Adding Context To Your Agent](../building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md).

#### Custom Prompt Builders

Prompt builders allow teams to replace prompt construction logic while preserving the orchestration interfaces.

See [LLM Planning Prompt Guide](../orchestration/LLM_PLANNING_PROMPT_GUIDE.md#advanced-custom-promptbuilder).

#### Testing And Conformance Helpers

The framework includes test-friendly implementations and contracts so developers can add features without standing up the full runtime:

- in-memory discovery for tests
- in-memory task consumers and stores for dev/test flows
- `NoOp` implementations for optional memory, HITL, and hook dependencies
- mock memory and discovery implementations
- `core/conformance` tests for custom `TaskConsumer` backends

See [core/README.md](https://github.com/truvaagents/truva-g3/blob/main/core/README.md), [Scheduled Tasks Guide](../orchestration/SCHEDULED_TASKS_GUIDE.md#testing-the-conformance-helper), and [API Reference](../reference/API_REFERENCE.md#scheduling-interfaces).

#### MCP Integration Paths

TruvaG3 tools and MCP servers solve different problems. The documented direction is to add MCP at the edges:

- expose TruvaG3 tools to MCP clients through an MCP gateway
- consume external MCP servers as virtual TruvaG3 tools

See [TruvaG3 Tools vs MCP Servers](../reference/TRUVAG3_TOOLS_VS_MCP_SERVERS.md).

## Feature-To-Module Index

| Feature Area | Primary Module | Supporting Modules |
|---|---|---|
| Tools, agents, generated HTTP APIs, framework runtime | `core` | `telemetry` |
| Discovery and registration | `core` | Redis/Valkey backend |
| AI provider clients, provider registry, embeddings | `ai` | `telemetry`, `memory` |
| Dynamic planning, iterative planning, and workflow execution | `orchestration` | `core`, `ai`, `telemetry` |
| Async tasks and scheduling interfaces | `core`, `orchestration` | Redis/Valkey backend |
| HITL approvals | `orchestration` | Redis/Valkey backend |
| Component, shared, and user memory | `core`, `memory` | `orchestration`, `telemetry`, Redis/Valkey, Qdrant |
| Resilience primitives | `resilience` | `core`, `telemetry` |
| Metrics, tracing, logging support | `telemetry` | `core` |
| OpenAPI generation | `core` | capability metadata |
| Test helpers and backend conformance | `core` | `orchestration`, `memory` |

## Documentation Index

Use these docs for deeper feature-level details:

- [README.md](https://github.com/truvaagents/truva-g3/blob/main/README.md) - project overview and positioning
- [GETTING_STARTED.md](https://github.com/truvaagents/truva-g3/blob/main/GETTING_STARTED.md) - first-run path
- [core/README.md](https://github.com/truvaagents/truva-g3/blob/main/core/README.md) - tools, agents, framework runtime
- [ai/README.md](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md) - AI provider layer
- [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md) - orchestration, workflows, streaming, HITL, production features
- [memory/README.md](https://github.com/truvaagents/truva-g3/blob/main/memory/README.md) - shared memory backend implementations
- [resilience/README.md](https://github.com/truvaagents/truva-g3/blob/main/resilience/README.md) - circuit breakers, retry, panic recovery
- [telemetry/README.md](https://github.com/truvaagents/truva-g3/blob/main/telemetry/README.md) - metrics, tracing, telemetry configuration
- [API_REFERENCE.md](../reference/API_REFERENCE.md) - API surface
- [ARCHITECTURE.md](https://github.com/truvaagents/truva-g3/blob/main/docs/overview/ARCHITECTURE.md) - framework architecture
- [TOOL_DEVELOPMENT_GUIDE.md](../building/TOOL_DEVELOPMENT_GUIDE.md) - building tools
- [TOOL_SCHEMA_DISCOVERY_GUIDE.md](../building/TOOL_SCHEMA_DISCOVERY_GUIDE.md) - schema discovery and tool payload generation
- [AGENT_DEVELOPMENT_GUIDE.md](../building/AGENT_DEVELOPMENT_GUIDE.md) - building agents
- [AI_PROVIDERS_SETUP_GUIDE.md](../building/AI_PROVIDERS_SETUP_GUIDE.md) - provider aliases, model aliases, and client setup
- [ORCHESTRATION_MODES_GUIDE.md](../orchestration/ORCHESTRATION_MODES_GUIDE.md) - orchestration modes
- [EFFECTIVE_PROMPTS_GUIDE.md](../building/EFFECTIVE_PROMPTS_GUIDE.md) - capability descriptions and prompt quality
- [ERROR_HANDLING_GUIDE.md](../orchestration/ERROR_HANDLING_GUIDE.md) - structured error patterns
- [INTELLIGENT_ERROR_HANDLING.md](../orchestration/INTELLIGENT_ERROR_HANDLING.md) - LLM-assisted error analysis
- [ASYNC_ORCHESTRATION_GUIDE.md](../orchestration/ASYNC_ORCHESTRATION_GUIDE.md) - background task pattern
- [HUMAN_IN_THE_LOOP_USER_GUIDE.md](../orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md) - HITL approvals
- [SCHEDULED_TASKS_GUIDE.md](../orchestration/SCHEDULED_TASKS_GUIDE.md) - delayed and recurring execution
- [AGENT_MEMORY_USER_GUIDE.md](../memory-and-chat/AGENT_MEMORY_USER_GUIDE.md) - shared and user memory
- [CONVERSATION_HISTORY_GUIDE.md](../memory-and-chat/CONVERSATION_HISTORY_GUIDE.md) - prompt-safe chat history
- [CHAT_AGENT_GUIDE.md](../memory-and-chat/CHAT_AGENT_GUIDE.md) - chat agent pattern
- [CHAT_SESSION_MANAGEMENT_GUIDE.md](../memory-and-chat/CHAT_SESSION_MANAGEMENT_GUIDE.md) - Redis-backed session lifecycle
- [DISTRIBUTED_TRACING_GUIDE.md](../observability/DISTRIBUTED_TRACING_GUIDE.md) - trace and log correlation
- [LOGGING_IMPLEMENTATION_GUIDE.md](../observability/LOGGING_IMPLEMENTATION_GUIDE.md) - logging conventions
- [OAUTH_SECURITY_GUIDE.md](../operations/OAUTH_SECURITY_GUIDE.md) - OAuth and header propagation
- [DEV_TOOLS_GUIDE.md](../operations/DEV_TOOLS_GUIDE.md) - Swagger UI and Registry Viewer
- [ENVIRONMENT_VARIABLES_GUIDE.md](../reference/ENVIRONMENT_VARIABLES_GUIDE.md) - configuration reference
- [LIMITS_CHEATSHEET.md](../reference/LIMITS_CHEATSHEET.md) - runtime limits and tuning reference
- [TRUVAG3_TOOLS_VS_MCP_SERVERS.md](../reference/TRUVAG3_TOOLS_VS_MCP_SERVERS.md) - TruvaG3 tool model compared with MCP
- [AUTO_DISCOVERY_GUIDE.md](../operations/AUTO_DISCOVERY_GUIDE.md) - auto-discovery feature guide (registration, lookup, lease architecture, multi-replica, resilience)
- [KUBERNETES.md](../operations/KUBERNETES.md) - Kubernetes deployment
- [LLM_PLANNING_PROMPT_GUIDE.md](../orchestration/LLM_PLANNING_PROMPT_GUIDE.md) - prompt customization

## Known Documentation Gaps

Some docs still appear older than the current codebase and should be reviewed before relying on them as authoritative:

- [examples/README.md](https://github.com/truvaagents/truva-g3/blob/main/examples/README.md) references several example directories that are no longer present.
- [API_REFERENCE.md](../reference/API_REFERENCE.md) describes a `ui` package with chat and REST transports, but no matching package or implementation appears in the repository.
This guide is based on the current root/module READMEs and the current guides under `docs/`.
