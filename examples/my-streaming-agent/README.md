# `<AGENT NAME>` — a TruvaG3 Streaming Agent

> **Template scaffold.** This README is a placeholder. After your coding
> agent populates the agent from [`PROMPT.md`](PROMPT.md), replace this
> file with a real description. Use these as writing references:
>
> - Canonical streaming chat: [`examples/travel-chat-agent/README.md`](../travel-chat-agent/README.md)
> - Streaming + public capability (agent-as-tool): [`examples/devops-chat-agent/README.md`](../devops-chat-agent/README.md)

---

## What this agent does

`<one-paragraph description — what task does this chat agent help users
accomplish, and which tools does it orchestrate?>`

## Tools this agent orchestrates

| Tool | Purpose |
|------|---------|
| `<tool_1>` | `<what it provides>` |
| `<tool_2>` | `<what it provides>` |

## How to run

```bash
# 1. Configure an AI provider key (REQUIRED — agent will fail without one)
cp .env.example .env
# Edit .env, uncomment + fill ONE of OPENAI_API_KEY / ANTHROPIC_API_KEY / GROQ_API_KEY / GEMINI_API_KEY

# 2. Cold-start full deployment (cluster + infra + agent + chat-ui)
./setup.sh full-deploy
# Or, if cluster + infra are already up:
# ./setup.sh deploy

# 3. Open the chat dashboard and click your agent's card
open http://chat.localhost
```

## Endpoints

| Endpoint | Purpose |
|----------|---------|
| `POST /chat/session` | Create a new chat session |
| `POST /chat/stream` | SSE streaming chat |
| `GET /health` | Liveness probe |
| `GET /api/capabilities` | Capability discovery |

## Required reading for contributors

- [`docs/AGENT_DEVELOPMENT_GUIDE.md`](../../docs/AGENT_DEVELOPMENT_GUIDE.md) (esp. §5 Streaming Agent, §8 SSE Streaming, §9 Session Management)
- [`docs/TOOL_SCHEMA_DISCOVERY_GUIDE.md`](../../docs/TOOL_SCHEMA_DISCOVERY_GUIDE.md) (only if exposing a public capability for other agents to call)
- [`docs/DISTRIBUTED_TRACING_GUIDE.md`](../../docs/DISTRIBUTED_TRACING_GUIDE.md)
- [`docs/LOGGING_IMPLEMENTATION_GUIDE.md`](../../docs/LOGGING_IMPLEMENTATION_GUIDE.md)
