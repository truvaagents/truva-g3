# Should TruvaG3 Tools Be Replaced with MCP Servers?

**Short answer: No.** They solve different problems. But they can work together.

This document walks through the reasoning.

---

## Wait, what's MCP?

MCP (Model Context Protocol) is an open protocol originally created by Anthropic and now governed by the Agentic AI Foundation (under the Linux Foundation). It standardizes how LLM hosts (like Claude Desktop, Cursor, etc.) talk to tool-providing servers. Think of it like USB — a universal plug so any host can use any tool.

It uses JSON-RPC 2.0 over either:
- **stdio** — the host launches the server as a subprocess
- **Streamable HTTP** — the host connects to a remote HTTP endpoint

A client calls `tools/list` to see what's available, then `tools/call` to invoke one. That's essentially it.

---

## And what are TruvaG3 tools?

A TruvaG3 tool is an **independent HTTP microservice** that wraps an external API (weather, currency, Jira, etc.) and makes it available to LLM-powered agents.

Each tool is its own deployable process — its own K8s pod, its own port, its own scaling. When it starts up, it registers itself into a shared **Redis-based service registry**, advertising what capabilities it offers (e.g., `get_weather_forecast`, `convert_currency`). It then heartbeats roughly every 15 seconds (half the 30-second TTL, plus jitter) to prove it's still alive. If it dies, it silently expires from the registry.

Agents discover tools by querying that registry ("find me anything that can do `get_weather_forecast`"), and the orchestrator invokes them via plain HTTP POST to the tool's capability endpoint. The response comes back in a structured envelope — success/failure, data, and a typed error with category and retry hints.

In short: each tool is a small, self-contained service that knows how to talk to one external API and advertise itself for discovery.

---

## So how is that different from MCP?

TruvaG3's tool system is not just an interface — it's an **operational framework**.

| | TruvaG3 Tools | MCP Servers |
|---|---|---|
| **What it is** | A microservice you deploy | A protocol a process speaks |
| **One tool = one...** | K8s deployment (own pod, own scaling) | Method inside a server process |
| **Discovery** | Live Redis registry with heartbeats, TTL, indexed queries | `tools/list` inside an already-connected session |
| **How clients find tools** | Query Redis by capability, name, or type | They don't — connections are configured manually |
| **Health monitoring** | Mandatory heartbeat (~15s); dead tools auto-expire via TTL | Optional `ping` (spec recommends periodic use); no formal health system |
| **Scaling** | Each tool scales independently in K8s | N tools share one process; scale the whole server or nothing |
| **Tracing** | OTel distributed tracing with W3C baggage propagation | Not provided |
| **Error handling** | Structured `ToolError` with category, retryable flag, upstream classification | `isError: true` with a text message |
| **Orchestration** | Built-in DAG executor, LLM-driven planning, parallel execution | Not provided — the host figures it out |
| **Schema** | 3-tier: description, field hints (~200 bytes), full JSON Schema endpoint | Standard JSON Schema on `inputSchema` |

The core difference in one line:

> **TruvaG3 answers "how do I build and operate a tool ecosystem?"**
> **MCP answers "how do I standardize the wire format between a host and a tool?"**

---

## What would we lose if we replaced TruvaG3 tools with MCP servers?

Quite a lot, actually:

1. **The live registry.** MCP has no runtime discovery. No heartbeats, no TTL-based expiry, no "find me all tools that can do X" queries. You'd have to rebuild all of that outside the protocol.

2. **Distributed tracing.** Every TruvaG3 tool call carries OTel trace context end-to-end. MCP has no concept of this.

3. **Smart error handling.** TruvaG3's executor looks at error categories and retryable flags to decide what to do next. MCP just says "it failed" with a string.

4. **Per-tool scaling.** TruvaG3 tools are independent deployments. MCP bundles tools into a single server process.

5. **Orchestration integration.** The `SmartExecutor` knows how to chain tool calls, cache results for DAG dependencies, and handle tool-vs-agent invocation differences. With MCP, you'd need to build an adapter layer.

6. **Type-level safety.** TruvaG3 enforces at compile time that tools cannot discover other services — only agents can. MCP has no such guardrail.

---

## What would we gain?

Two things, and they're real:

1. **Client interoperability.** Claude Desktop, Cursor, Windsurf, and any MCP-compatible host could connect to TruvaG3 tools directly.

2. **Ecosystem access.** Thousands of community MCP servers could become available to TruvaG3 agents as tool providers.

---

## So what should we actually do?

**Add MCP at the edges. Don't replace the core.**

Two concrete paths:

### Path 1: MCP Gateway (expose TruvaG3 tools to MCP clients)

A thin MCP server that translates:
- `tools/list` → queries the Redis registry, returns tool definitions
- `tools/call` → HTTP POST to the underlying TruvaG3 tool, translates the response back

This gives MCP clients access to TruvaG3's tool ecosystem without changing how any tool works internally.

### Path 2: MCP Consumer (let TruvaG3 agents use external MCP servers)

An adapter that lets TruvaG3 agents treat external MCP servers as discoverable tools:
- Connect to an MCP server, call `tools/list`, register the tools in Redis as virtual entries
- When the orchestrator invokes one, translate the HTTP call into `tools/call` on the MCP session

This expands what tools TruvaG3 agents can use without touching the tool framework.

---

## The bottom line

TruvaG3 tools and MCP servers are **complementary, not competing**. TruvaG3 handles the hard operational stuff — discovery, health, tracing, scaling, orchestration. MCP handles the standardization stuff — a universal interface so different hosts and tools can talk.

Replacing one with the other means losing what the other is good at. The right architecture keeps TruvaG3's operational backbone and uses MCP as a bridge to the broader ecosystem.
