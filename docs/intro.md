---
sidebar_position: 1
---

# Introduction

TruvaG3 is a Go framework that takes the microservices pattern and applies
it to multi-agent systems. Specialized agents and tools register their
capabilities with a shared registry, find each other by **logical name**
at runtime, and coordinate without a central conductor. There is no
hardcoded wiring between caller and callee — a tool advertises
`geocode_location`; whichever process currently serves that capability
gets the call.

It's the open-source reference implementation of the architecture
described in [*A Microagents Reference Architecture: Dynamic Capability
Discovery and Decentralized
Coordination*](https://github.com/truvaagents/truva-g3/blob/main/www/blogs/microagents-architecture.html)
— and adds the operational features a multi-agent system tends to need
on top: a vendor-agnostic AI client, DAG-based execution with iterative
re-plan, reusable versioned agent skills with progressive disclosure, circuit
breakers and semantic retry, OpenTelemetry
instrumentation, two-tier memory, and human-in-the-loop approvals. Every
backend (service discovery, LLM provider, telemetry, memory store, skill
registry) and
most framework behaviors sit behind interfaces that can be swapped.

## Two coordination layers

The architecture splits coordination into two distinct layers — and the
framework treats them differently:

- **Inside each agent — orchestration.** One AIOrchestrator drives
  plan → execute → synthesize, returning to plan when fresh data unlocks
  the next phase. Plans are DAGs of capability invocations executed with
  parallelism where dependencies allow.
- **Between participants — decentralized coordination.** Each agent
  reads the shared registry, resolves capabilities to endpoints, and
  calls peers directly over HTTP/REST. No process in the middle routes,
  sequences, or coordinates.

## What you get out of the box

- **Provider-neutral AI.** Keep agent and orchestration logic on Core
  interfaces while using registered profiles for OpenAI, Anthropic, Gemini,
  Azure OpenAI, Claude on Vertex AI, build-tagged AWS Bedrock, and several
  OpenAI-compatible services. Other compatible endpoints, including vLLM and
  llama.cpp, can reuse the OpenAI adapter after application-level contract
  testing. Hosted surfaces may require different construction-time routes and
  credentials without changing the provider-neutral call sites.
- **Distributed-systems patterns.** Capability-based service discovery
  (Redis/Valkey by default, pluggable behind `core.Discovery`),
  circuit breakers, semantic retry, panic recovery,
  OpenTelemetry-native traces and metrics with W3C trace-context
  propagation across agents and tools.
- **Reusable agent skills.** Developers explicitly bind versioned procedural
  guidance, while orchestration resolves exact revisions per request and loads
  instructions and resources only at the lifecycle boundary that needs them.
  The runtime contract is provider-neutral; Redis is the included adapter.
- **Small runtime footprint.** Go binaries with ~15–44MB containers,
  ~6–45MB runtime memory, ~100ms startup. Replicas scale cheaply.
- **Direct use of Kubernetes primitives.** Deployments, Services,
  Ingress, NetworkPolicy, Secrets — no proprietary control plane, no
  custom CRDs, no sidecar requirements. The same operational model your
  platform team already runs.

## Where this fits best

TruvaG3 is most useful when the goal is not a single agent but a
**network** of agents and tools that can be developed and operated
independently, on infrastructure you already run. Self-hosted operation,
namespace-oriented isolation, direct in-cluster service communication,
and growth from a handful of participants to a large internal fleet —
all without an external SaaS control plane. It's well-suited to
regulated, internal, or air-gapped deployments where a hosted agent
control plane isn't an option.

## What you need

- **Go 1.26+** (the framework's `go.mod` declares 1.26.4)
- **Docker** (or [Podman](https://podman.io/) as a drop-in)
- **Kind + kubectl** — for the local Kubernetes cluster
- **One AI provider API key** — [Groq](https://groq.com/) has a free tier and is the fastest to start; OpenAI, Anthropic, Gemini, and others are supported

Step-by-step install commands for macOS, Linux, and Windows are in
[**Getting started → Prerequisites**](./getting-started.md#1-prerequisites).

## Quick start

Two complete examples deploy end-to-end via a single `./setup.sh full-deploy`.
Each provisions a Kind cluster, the shared infrastructure (Redis, OpenTelemetry
Collector, Loki, Prometheus, Jaeger, Grafana, ingress-nginx), the agent, and
its chat UI. The Travel and DevOps agents also validate and conditionally
publish their developer-bound example skills through the Registry Viewer.
First run takes about 5–15 minutes; subsequent examples on the same cluster
finish in 1–2 minutes.

Pick whichever matches your interest:

**[DevOps Chat Agent — minimum setup](./getting-started.md#quick-start-devops-chat-agent)**

The agent answers questions about the very cluster it runs in — pod
health, log searches in Loki, PromQL queries, trace lookups in Jaeger. It
needs **only one AI provider API key** and no third-party credentials.
Best fit if you want the absolute minimum setup or are evaluating against
an SRE / observability use case.

**[Travel Chat Agent — consumer-style](./getting-started.md#quick-start-travel-chat-agent)**

A multi-tool travel-planning agent. The six no-key tools (weather,
geocoding, currency, country info, time, travel advisories) deploy with
zero extra credentials; optional keys unlock flights, hotels, places, and
news.

Both run side-by-side on the same cluster.

## After your first deploy

- **[Framework Features Guide](/docs/overview/FRAMEWORK_FEATURES_GUIDE)** — the conceptual overview of what's in the framework and how the pieces fit.
- **[Go Package Reference](/docs/reference/GO_PACKAGE_REFERENCE)** — exported types, interfaces, and signatures on pkg.go.dev.
- **[Agent Development Guide](/docs/building/AGENT_DEVELOPMENT_GUIDE)** and **[Tool Development Guide](/docs/building/TOOL_DEVELOPMENT_GUIDE)** — patterns for building your own.
- **[Agent Skills Guide](/docs/orchestration/AGENT_SKILLS_GUIDE)** — author, publish, bind, operate, and troubleshoot reusable skills.

## Build your own

TruvaG3 ships three fill-in-the-blanks scaffolds:

| To build | Scaffold | Reference example |
|---|---|---|
| A tool wrapping an external HTTP API or local command | `examples/my-tool/` | `examples/stock-market-tool/` or `examples/devops-tool/` |
| A streaming chat agent (SSE, multi-tool orchestration) | `examples/my-streaming-agent/` | `examples/travel-chat-agent/` |
| An event-driven async agent (webhooks, queues, schedules) | `examples/my-async-agent/` | `examples/agent-with-async/` |

Each scaffold ships with a `PROMPT.md` — 12 self-contained steps you paste
into a coding agent one at a time, reviewing what it produces between
steps. Full walk-through:
[**Getting started → Build Your Own Components**](./getting-started.md#4-build-your-own-components-with-a-coding-agent).
