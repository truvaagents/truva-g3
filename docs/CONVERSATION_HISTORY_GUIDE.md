# Conversation History Guide

Hey there! This guide shows you how to give your TruvaG3 chat agents **multi-turn conversation memory** without letting long sessions blow up prompt size. If you've ever had a chat agent understand the first few turns perfectly and then start losing context as the session grows, this is the feature you're looking for.

> **Working Examples**
>
> Everything in this guide maps to working agents in this repo:
> - **Travel chat agent**: [`examples/travel-chat-agent/`](../examples/travel-chat-agent/)
> - **DevOps chat agent**: [`examples/devops-chat-agent/`](../examples/devops-chat-agent/)
> - **Agent with human approval**: [`examples/agent-with-human-approval/`](../examples/agent-with-human-approval/)
>
> The simplest Tier 2 reference wiring lives in:
> - [`examples/travel-chat-agent/chat_agent.go`](../examples/travel-chat-agent/chat_agent.go)

---

## Table of Contents

- [What is Conversation History Protection?](#what-is-conversation-history-protection)
- [When Do You Need This?](#when-do-you-need-this)
- [The Problem It Solves](#the-problem-it-solves)
- [How It Works](#how-it-works)
- [What Most Teams Should Do](#what-most-teams-should-do)
- [The Three Layers](#the-three-layers)
- [Quick Start](#quick-start)
- [Where This Code Goes in Your Agent](#where-this-code-goes-in-your-agent)
- [Step 1: Pass Raw Turns in Metadata](#step-1-pass-raw-turns-in-metadata)
- [Step 2: Use the Default Tier 1 Path](#step-2-use-the-default-tier-1-path)
- [Step 3: Enable Tier 2 Recursive Compaction](#step-3-enable-tier-2-recursive-compaction)
- [Step 4: Tune Behavior with Environment Variables](#step-4-tune-behavior-with-environment-variables)
- [Level 3: Full Manual Control](#level-3-full-manual-control)
  - [A Minimal Custom Compactor](#a-minimal-custom-compactor)
  - [Layer 3 Behavior Contract](#layer-3-behavior-contract)
- [When to Use ConversationHistoryHook](#when-to-use-conversationhistoryhook)
- [What the LLM Sees](#what-the-llm-sees)
- [Observability: How to See Compaction Working](#observability-how-to-see-compaction-working)
- [Troubleshooting](#troubleshooting)
- [See Also](#see-also)

---

## What is Conversation History Protection?

Imagine this conversation:

```text
User: I'm rewriting an executive note about enterprise AI agents.
Assistant: You should add governance, observability, rollout constraints, and ROI framing.
User: Great. Can you give me a better version under 1100 words?
Assistant: [provides improved version]
User: Will this work for CIOs and CTOs?
Assistant: Yes, because...
User: Can you update it with internet research?
```

That last request only makes sense if the agent still remembers:

- what the original task was
- what the assistant already suggested
- the 1100-word constraint
- the CIO/CTO audience
- the fact that the user now wants a research-backed revision, not a brand new draft

That is what conversation-history protection is for. It gives the orchestrator enough prior dialogue to understand the current turn, while also keeping long sessions from overrunning prompt budgets.

In TruvaG3, this matters even more than in a simple "single prompt in, single prompt out" chat app, because the prepared conversation history is reused across multiple orchestration prompts:

- planning
- continuation prompts in later phases
- synthesis

So conversation history is not just a UI nicety. It is part of the framework's request context.

Conversation-history protection does two jobs:

1. It preserves multi-turn chat context so the model still understands references like "there," "that version," or "the issue from earlier."
2. It prevents long chat sessions from overwhelming orchestration prompts.

TruvaG3 handles this in a shared framework path that prepares the `<conversation_history>` enrichment before planning. The prepared history is then reused across planning, continuation, and synthesis.

This feature works on **conversation turns**, not just user messages. If older turns are compacted, the summary folds in both:
- user turns
- assistant turns

That matters because later requests often depend on what the assistant already suggested, not just what the user asked.

You can think of it like this:

```
Without protection:                With protection:

Short chat feels smart             Short chat stays smart
Long chat gets expensive           Long chat stays within budget
Eventually prompt size explodes    Older context is compacted safely
Recent working context gets lost   Recent working context stays verbatim
```

---

## When Do You Need This?

You need this guide if you are building a chat-style agent where users expect the assistant to remember earlier turns.

Common examples:

- a travel assistant that needs to remember destination, dates, and budget across follow-up questions
- a DevOps assistant that needs to remember what issue was already discussed
- a writing assistant that needs to preserve the user's goal, constraints, and prior revisions
- a human-approval agent that needs to resume a conversation without losing why a step needs approval

You probably **do not** need this guide if your agent is:

- single-turn only
- event-driven with no session history
- already shaping a one-off payload instead of carrying a multi-turn conversation

The short version:

- if users say things like "there," "that version," "the earlier issue," or "do the same thing but for X," you need conversation history
- if your sessions can get long, you probably also want compaction

---

## The Problem It Solves

Without conversation-history preparation, chat agents usually fall into one of three failure modes:

1. **Too little context**
   The agent only sees the current user message, so follow-up requests become ambiguous.

2. **Too much context**
   The agent keeps appending raw history forever, which increases token cost and eventually pushes planning prompts over budget.

3. **The wrong context stays literal**
   If you respond to prompt growth by simply chopping off old history, you often discard durable facts that still matter while keeping less important recent wording.

Here is what that looks like in practice:

| Failure Mode | What the User Experiences | Why It Happens |
|---|---|---|
| **Missing context** | "What do you mean by 'there'?" style mistakes | Prior turns never reach orchestration |
| **Context overflow** | Latency and token cost grow with every turn, then prompts fail or degrade | Raw history is appended without budgeting |
| **Blunt truncation** | The agent forgets goals, constraints, or prior decisions | History is cut by count or size instead of preserving durable state |

TruvaG3's conversation-history path addresses all three:

| Mode | What Happens |
|---|---|
| **Tier 1** | Keeps conversation history under budget using token-aware elision/truncation safeguards |
| **Tier 2** | Recursively compacts older turns into a summary while preserving the newest turns verbatim |

Tier 1 is the safe default. Tier 2 is the higher-fidelity opt-in path for longer-lived chat sessions.

The key design choice is that Tier 2 does **not** summarize "user questions only." It summarizes the older part of the conversation as a sequence of turns, so the durable state includes both:

- what the user asked for
- what the assistant already answered, recommended, or decided

That is what makes later turns coherent.

---

## How It Works

Here is the simplest mental model:

1. your agent stores chat turns
2. your agent passes those turns into orchestration as metadata
3. the framework turns them into a clean `<conversation_history>` block
4. if the history is too large, the framework shrinks it safely
5. the prepared history is reused across orchestration prompts for that request

That means your agent does **not** have to keep manually stitching old messages into the query text.

Here is the flow:

```
Chat session turns
    │
    ▼
metadata[orchestration.MetadataConversationTurns]
metadata[orchestration.MetadataConversationSessionKey]
    │
    ▼
ConversationHistoryPreparer
    │
    ├── Tier 1: token budgeting / trimming
    └── Tier 2: recursive summary of older turns (optional)
    │
    ▼
<conversation_history> enrichment
    │
    ▼
Planning → Continuation → Synthesis
```

Two important details:

- The preferred path is **raw turns in request metadata**.
- `ConversationHistoryHook` still exists, but it is now an **adapter** for memory-backed integrations that cannot supply raw turns directly.

---

## What Most Teams Should Do

If you are skimming, this is the recommended path for most teams:

### Start here

1. store conversation turns in your session store
2. pass them in metadata using:
   - `orchestration.MetadataConversationTurns`
   - `orchestration.MetadataConversationSessionKey`
3. create the orchestrator with `CreateOrchestrator(...)`

That gives you **Tier 1** automatically.

### Add this when sessions get longer

If your chat sessions are regularly long enough that prompt size becomes a concern, add:

```go
preparer, err := orchestration.BuildCompactionEnabledConversationHistoryPreparer(
    config,
    agent.AI,
)
```

and inject it through `OrchestratorDependencies.ConversationHistoryPreparer`.

That gives you **Tier 2** without making you assemble the lower-level pieces by hand.

### Only go deeper if you really need to

Most teams do **not** need:

- a custom token counter
- a custom compactor
- direct hook-based wiring

Those are there for advanced cases, and this guide covers them later.

---

## The Three Layers

The API follows TruvaG3's three-layer framework style:

| Layer | Who It's For | What You Write |
|---|---|---|
| **Layer 1** | Most chat agents | Pass raw turns in metadata and call `CreateOrchestrator(...)` |
| **Layer 2** | Agents that want Tier 2 compaction without manual assembly | Call `BuildCompactionEnabledConversationHistoryPreparer(...)` and inject it |
| **Layer 3** | Advanced integrations that want full control | Construct `ConversationHistoryProcessor` directly and swap individual pieces |

This layering keeps the default path low-friction while still leaving room for advanced overrides.

If you are new to TruvaG3, read the layers like this:

- **Layer 1**: "I just want safe conversation history"
- **Layer 2**: "I want better long-session behavior, but I still want the framework to do most of the setup"
- **Layer 3**: "I know exactly which pieces I want to replace"

---

## Quick Start

If you want the shortest path:

1. Store your chat history as `[]core.ConversationTurn`
2. Pass it in request metadata under:
   - `orchestration.MetadataConversationTurns`
   - `orchestration.MetadataConversationSessionKey`
3. Create the orchestrator normally with `CreateOrchestrator(...)`

That gives you **Tier 1 automatically**.

If you also want recursive compaction:

4. Build a Tier 2 preparer with `BuildCompactionEnabledConversationHistoryPreparer(...)`
5. Inject it via `OrchestratorDependencies.ConversationHistoryPreparer`

---

## Where This Code Goes in Your Agent

One thing that can be hard on a first read is knowing **where** each snippet belongs. The pattern is simpler than it looks:

### Agent initialization

This is where you:

- create the orchestrator config
- optionally build the Tier 2 preparer
- inject it into `OrchestratorDependencies`
- call `CreateOrchestrator(...)`

Look at these examples:

- [`examples/travel-chat-agent/chat_agent.go`](../examples/travel-chat-agent/chat_agent.go) — simplest Tier 2 wiring
- [`examples/devops-chat-agent/chat_agent.go`](../examples/devops-chat-agent/chat_agent.go) — Tier 2 plus memory and HITL wiring
- [`examples/agent-with-human-approval/chat_agent.go`](../examples/agent-with-human-approval/chat_agent.go) — Tier 2 plus human-approval flow

### Per-request handling

This is where you:

- load session history
- convert it into `[]core.ConversationTurn`
- add metadata keys
- call `ProcessRequest(...)` or `ProcessRequestStreaming(...)`

Look at these example methods:

- `addConversationHistoryMetadata(...)`
- `ProcessWithStreaming(...)`
- the synchronous `ProcessRequest(...)` path when present

In the example agents, all of that lives in the same `chat_agent.go` files linked above.

### Simple mental split

If you're not sure where a snippet belongs, use this rule:

- **Anything that builds a preparer or touches `OrchestratorDependencies`** belongs in agent initialization
- **Anything that reads session history or adds metadata** belongs in the request path

---

## Step 1: Pass Raw Turns in Metadata

This is the preferred integration for chat agents. In plain terms: your agent takes the session history it already has in memory or storage, converts it into `[]core.ConversationTurn`, and attaches that to the request.

```go
func (a *YourChatAgent) addConversationHistoryMetadata(
    metadata map[string]interface{},
    sessionID string,
    history []Message,
) map[string]interface{} {
    if metadata == nil {
        metadata = make(map[string]interface{})
    }
    if len(history) == 0 {
        return metadata
    }

    turns := make([]core.ConversationTurn, 0, len(history))
    for _, msg := range history {
        turns = append(turns, core.ConversationTurn{
            Role:    msg.Role,
            Content: msg.Content,
        })
    }

    metadata[orchestration.MetadataConversationTurns] = turns
    metadata[orchestration.MetadataConversationSessionKey] = sessionID
    return metadata
}
```

This is what all three reference chat agents do:

- [`examples/travel-chat-agent/chat_agent.go`](../examples/travel-chat-agent/chat_agent.go)
- [`examples/devops-chat-agent/chat_agent.go`](../examples/devops-chat-agent/chat_agent.go)
- [`examples/agent-with-human-approval/chat_agent.go`](../examples/agent-with-human-approval/chat_agent.go)

Those examples demonstrate the **right framework entry point**. One nuance: their example session stores still use a bounded sliding window, which is fine for many practical chat deployments but is not the strongest possible Tier 2 contract for very long-lived sessions.

**Important contract**: `MetadataConversationTurns` should ideally contain the **full append-only conversation turn list for that session**, not a rolling window. Tier 2 watermarking is most correct when turn order is stable across the full logical history.

If you only have a pre-formatted history string and cannot provide raw turns yet, the framework also supports the legacy text path through `metadata[core.EnrichmentConversationHistory]`. That path still gets Tier 1 protection, but Tier 2 recursive compaction works best from raw turns.

If you want a concrete reference, start with:

- [`examples/travel-chat-agent/chat_agent.go`](../examples/travel-chat-agent/chat_agent.go)

It has the cleanest small example of:

- converting `[]Message` to `[]core.ConversationTurn`
- storing `MetadataConversationTurns`
- storing `MetadataConversationSessionKey`
- keeping the legacy formatted-history fallback during rollout

---

## Step 2: Use the Default Tier 1 Path

Once your agent passes raw turns in metadata, Tier 1 is automatic if you create the orchestrator through the factory.

```go
func (a *YourChatAgent) initializeOrchestrator(discovery core.Discovery) error {
    // -----------------------------------------------------------------
    // 1. Regular orchestrator setup
    // -----------------------------------------------------------------
    config := orchestration.DefaultConfig()

    // -----------------------------------------------------------------
    // 2. Tier 1: do NOT build a custom conversation-history preparer
    // -----------------------------------------------------------------
    // This is the important part for Tier 1. Leave
    // ConversationHistoryPreparer unset and let the factory create the
    // default one for you.
    deps := orchestration.OrchestratorDependencies{
        Discovery:           discovery,
        AIClient:            a.AI,
        Logger:              a.Logger,
        Telemetry:           telemetry.GetTelemetryProvider(),
        EnableErrorAnalyzer: true,
    }

    // -----------------------------------------------------------------
    // 3. Create the orchestrator
    // -----------------------------------------------------------------
    // Because deps.ConversationHistoryPreparer is not set, this call
    // auto-installs the default Tier 1 conversation-history path.
    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return fmt.Errorf("failed to create orchestrator: %w", err)
    }

    // -----------------------------------------------------------------
    // 4. Normal startup continues as usual
    // -----------------------------------------------------------------
    if err := orch.Start(context.Background()); err != nil {
        return fmt.Errorf("failed to start orchestrator: %w", err)
    }

    // Store the orchestrator on the agent, same as any other setup.
    a.orchestrator = orch
    return nil
}
```

What the factory does for you behind the scenes:

- installs the default shared `ConversationHistoryPreparer`
- applies token budgeting from config/env
- prepares `<conversation_history>` once before planning
- reuses the prepared value across later orchestration phases

This is enough for many chat agents, especially if sessions are not extremely long.

If you want to see this in a real agent constructor, use:

- [`examples/travel-chat-agent/chat_agent.go`](../examples/travel-chat-agent/chat_agent.go)
- [`examples/devops-chat-agent/chat_agent.go`](../examples/devops-chat-agent/chat_agent.go)

The relative position to remember is:

- metadata wiring happens in your request path
- Tier 1 setup happens in your orchestrator initialization
- for Tier 1, the important thing is what you **do not** add: no custom preparer is required

---

## Step 3: Enable Tier 2 Recursive Compaction

If you want better preservation on long-running sessions, use the Layer 2 helper. This is the "good default with compaction" path.

```go
func (a *YourChatAgent) initializeOrchestrator(discovery core.Discovery) error {
    // -----------------------------------------------------------------
    // 1. Regular orchestrator setup
    // -----------------------------------------------------------------
    config := orchestration.DefaultConfig()

    // -----------------------------------------------------------------
    // 2. Tier 2 addition: build a compaction-enabled preparer
    // -----------------------------------------------------------------
    // This is the main difference from Tier 1.
    preparer, err := orchestration.BuildCompactionEnabledConversationHistoryPreparer(
        config,
        a.AI,
    )
    if err != nil {
        return fmt.Errorf("failed to build conversation history preparer: %w", err)
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery:           discovery,
        AIClient:            a.AI,
        Logger:              a.Logger,
        Telemetry:           telemetry.GetTelemetryProvider(),
        EnableErrorAnalyzer: true,

        // -----------------------------------------------------------------
        // 3. Tier 2 addition: inject the preparer here
        // -----------------------------------------------------------------
        // This line tells the factory to use your Tier 2-enabled
        // conversation-history path instead of auto-building the default
        // Tier 1-only path.
        ConversationHistoryPreparer: preparer,
    }

    // -----------------------------------------------------------------
    // 4. Create the orchestrator normally
    // -----------------------------------------------------------------
    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return fmt.Errorf("failed to create orchestrator: %w", err)
    }

    // -----------------------------------------------------------------
    // 5. The rest of startup is unchanged
    // -----------------------------------------------------------------
    if err := orch.Start(context.Background()); err != nil {
        return fmt.Errorf("failed to start orchestrator: %w", err)
    }

    a.orchestrator = orch
    return nil
}
```

If your agent may start before `a.AI` is ready, use this slightly more defensive pattern:

```go
func (a *YourChatAgent) initializeOrchestrator(discovery core.Discovery) error {
    // -----------------------------------------------------------------
    // 1. Regular orchestrator setup
    // -----------------------------------------------------------------
    config := orchestration.DefaultConfig()

    // Leave this nil by default. If it stays nil, the factory uses Tier 1.
    var conversationHistoryPreparer orchestration.ConversationHistoryPreparer

    // -----------------------------------------------------------------
    // 2. Enable Tier 2 only when the AI client is ready
    // -----------------------------------------------------------------
    if a.AI != nil {
        preparer, err := orchestration.BuildCompactionEnabledConversationHistoryPreparer(
            config,
            a.AI,
        )
        if err != nil {
            return fmt.Errorf("failed to build compaction-enabled conversation history preparer: %w", err)
        }
        conversationHistoryPreparer = preparer
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery:                   discovery,
        AIClient:                    a.AI,
        Logger:                      a.Logger,
        Telemetry:                   telemetry.GetTelemetryProvider(),
        EnableErrorAnalyzer:         true,

        // If non-nil: Tier 2
        // If nil: the factory falls back to the default Tier 1 path
        ConversationHistoryPreparer: conversationHistoryPreparer,
    }

    // Create the orchestrator the same way either way.
    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return fmt.Errorf("failed to create orchestrator: %w", err)
    }

    // Normal startup continues unchanged.
    if err := orch.Start(context.Background()); err != nil {
        return fmt.Errorf("failed to start orchestrator: %w", err)
    }

    a.orchestrator = orch
    return nil
}
```

What this helper does:

- creates a default `SummaryCache`
- creates a default `LLMConversationCompactor`
- builds a shared `ConversationHistoryProcessor`
- applies your overrides last if you provide any

This is the recommended **Layer 2 reference implementation** because it keeps Tier 2 easy to adopt without hiding the underlying pieces.

`BuildCompactionEnabledConversationHistoryPreparer(...)` requires a non-nil `AIClient`, because Tier 2 uses an LLM compactor. If your agent sometimes starts without an AI client, the safe pattern is to fall back to the default Tier 1 path until AI is available.

For a real production-shaped example, see:

- [`examples/travel-chat-agent/chat_agent.go`](../examples/travel-chat-agent/chat_agent.go) for the simplest version
- [`examples/devops-chat-agent/chat_agent.go`](../examples/devops-chat-agent/chat_agent.go) for the same pattern in a larger agent
- [`examples/agent-with-human-approval/chat_agent.go`](../examples/agent-with-human-approval/chat_agent.go) for the same pattern in a HITL agent

The relative position to remember is:

- build the preparer before you assemble `OrchestratorDependencies`
- inject it into `deps.ConversationHistoryPreparer`
- then call `CreateOrchestrator(...)` normally
- your request-path metadata code does not change just because Tier 2 is enabled

### Layer 2 with selective overrides

If you want the convenience path but still need to override one concern, pass options:

```go
preparer, err := orchestration.BuildCompactionEnabledConversationHistoryPreparer(
    config,
    agent.AI,
    orchestration.WithConversationSummaryCache(myCache),
    orchestration.WithConversationCompactor(myCompactor),
)
if err != nil {
    return fmt.Errorf("failed to build conversation history preparer: %w", err)
}
```

This is usually the sweet spot:

- you keep the ergonomic helper
- you can still replace the cache or compactor
- you do not need to drop to full manual construction yet

**One important note**: there is no env-only switch that turns Tier 2 on by itself. Tier 2 is a code-level opt-in because it changes behavior and adds an LLM compaction call.

---

## Step 4: Tune Behavior with Environment Variables

The main knobs are:

```bash
# Tier 1 + Tier 2 budget
TRUVAG3_CONVERSATION_TOKEN_BUDGET=48000

# How many most-recent turns stay verbatim
TRUVAG3_CONVERSATION_RECENT_TURNS_PRESERVED=4

# Cache capacity for recursive summaries
TRUVAG3_CONVERSATION_SUMMARY_CACHE_SIZE=256
```

How to think about them:

- **Token budget**: upper bound for prepared conversation history
- **Recent turns preserved**: latest turns kept exactly as-is before older turns are compacted
- **Summary cache size**: number of session summaries remembered in-process

For most deployments:

- lower the budget if you want more aggressive history control
- increase preserved turns if recency matters more than compression
- keep cache size proportional to the number of concurrently active sessions on a pod

---

## Level 3: Full Manual Control

If you want to control every moving piece yourself, construct the processor directly.

```go
config := orchestration.DefaultConfig()

// Create the shared cache used by the processor.
cache, err := orchestration.NewSummaryCache(config.ConversationSummaryCacheSize)
if err != nil {
    return fmt.Errorf("failed to create summary cache: %w", err)
}

// Default framework compactor. Replace this with your own implementation
// if you want custom prompt behavior or other Layer 3 changes.
compactor, err := orchestration.NewLLMConversationCompactor(agent.AI, nil)
if err != nil {
    return fmt.Errorf("failed to create conversation compactor: %w", err)
}

// Build the processor directly instead of using the Layer 2 helper.
preparer, err := orchestration.NewConversationHistoryProcessor(
    orchestration.ConversationHistoryProcessorConfig{
        TokenBudget:          config.ConversationTokenBudget,
        RecentTurnsPreserved: config.ConversationRecentTurnsPreserved,
    },
    // Swap pieces here as needed.
    orchestration.WithConversationSummaryCache(cache),
    orchestration.WithConversationCompactor(compactor),
    orchestration.WithConversationTokenCounter(myTokenCounter),
)
if err != nil {
    return fmt.Errorf("failed to create conversation history processor: %w", err)
}

deps := orchestration.OrchestratorDependencies{
    Discovery:                   discovery,
    AIClient:                    agent.AI,
    Logger:                      agent.Logger,
    Telemetry:                   telemetry.GetTelemetryProvider(),
    // Inject the manually constructed processor here.
    ConversationHistoryPreparer: preparer,
}
```

This is the **Layer 3 reference implementation**.

It is the right choice when you need things like:

- a provider-specific token counter
- a custom compactor implementation
- a non-default summary cache lifecycle
- explicit constructor-level control for testing or integration boundaries

One important Layer 3 detail: the default `LLMConversationCompactor` uses a framework-owned prompt. There is no built-in option today to override just that prompt text. If you want different compaction instructions, summary style, or prompt structure, the Layer 3 way to do it is to provide your own `core.ConversationCompactor` implementation and inject it with `WithConversationCompactor(...)`.

The pieces you can swap are intentionally small:

| Piece | Interface | Why You Might Replace It |
|---|---|---|
| Token counter | `core.TokenCounter` | Better budget estimates for a specific provider |
| Compactor | `core.ConversationCompactor` | Custom summary behavior, model choice, or policy |
| Memory backend | `core.FullConversationMemory` | Full append-only turn retrieval for hook-backed integrations |

### A Minimal Custom Compactor

The most common Layer 3 customization is a custom compactor. Here is the interface you are implementing:

```go
type ConversationCompactor interface {
    Compact(ctx context.Context, priorSummary string, newTurns []core.ConversationTurn) (string, error)
}
```

Here is a minimal custom implementation shape:

```go
type MyCompactor struct {
    aiClient core.AIClient
    logger   core.Logger
}

func NewMyCompactor(aiClient core.AIClient) *MyCompactor {
    return &MyCompactor{
        aiClient: aiClient,
        logger:   &core.NoOpLogger{},
    }
}

// Optional, but recommended if you want the same logger propagation
// pattern as the framework-provided compactor.
func (c *MyCompactor) SetLogger(logger core.Logger) {
    if logger == nil {
        c.logger = &core.NoOpLogger{}
        return
    }
    c.logger = logger
}

func (c *MyCompactor) Compact(
    ctx context.Context,
    priorSummary string,
    newTurns []core.ConversationTurn,
) (string, error) {
    // Preserve existing behavior when there is nothing new to fold in.
    if len(newTurns) == 0 {
        return priorSummary, nil
    }

    // Build your custom compaction prompt here.
    var prompt strings.Builder
    prompt.WriteString("Summarize the durable state of this conversation.\n")
    prompt.WriteString("Preserve goals, constraints, decisions, and unresolved questions.\n")
    prompt.WriteString("Do not include stale workflow narration.\n\n")
    if priorSummary != "" {
        prompt.WriteString("Existing summary:\n")
        prompt.WriteString(priorSummary)
        prompt.WriteString("\n\n")
    }
    prompt.WriteString("New turns:\n")
    for _, turn := range newTurns {
        prompt.WriteString(turn.Role)
        prompt.WriteString(": ")
        prompt.WriteString(turn.Content)
        prompt.WriteString("\n")
    }

    // Make the LLM call using your own prompt.
    resp, err := c.aiClient.GenerateResponse(ctx, prompt.String(), nil)
    if err != nil {
        // Fail open so the processor can degrade gracefully.
        if c.logger != nil {
            c.logger.WarnWithContext(ctx, "Custom conversation compaction failed", map[string]interface{}{
                "operation":  "conversation_history",
                "error":      err.Error(),
                "error_type": "compaction",
            })
        }
        return "", nil
    }

    // Return the updated recursive summary string.
    return strings.TrimSpace(resp.Content), nil
}
```

Then inject it like this:

```go
config := orchestration.DefaultConfig()

// Layer 3 still needs a cache.
cache, err := orchestration.NewSummaryCache(config.ConversationSummaryCacheSize)
if err != nil {
    return fmt.Errorf("failed to create summary cache: %w", err)
}

// This is your custom compactor with custom prompt behavior.
myCompactor := NewMyCompactor(a.AI)

// Build the processor manually and inject your compactor.
preparer, err := orchestration.NewConversationHistoryProcessor(
    orchestration.ConversationHistoryProcessorConfig{
        TokenBudget:          config.ConversationTokenBudget,
        RecentTurnsPreserved: config.ConversationRecentTurnsPreserved,
    },
    orchestration.WithConversationSummaryCache(cache),
    orchestration.WithConversationCompactor(myCompactor),
)
if err != nil {
    return fmt.Errorf("failed to create conversation history processor: %w", err)
}
```

That is the Layer 3 path for prompt-level customization. Instead of asking the framework to expose a prompt override knob, you replace the compactor with one that owns its own prompt.

### Layer 3 Behavior Contract

If you implement your own compactor, these are the rules to follow:

1. `Compact(...)` receives:
   - `priorSummary`: the existing recursive summary, if one already exists
   - `newTurns`: the next older turns that should be folded into that summary

2. Your job is to return the **updated summary string**, not the full final `<conversation_history>` block.

3. Your implementation should be safe for the request path:
   - keep latency reasonable
   - avoid extra retries inside the compactor
   - degrade gracefully on failure

4. Failing open is usually the right behavior:
   - return `"" , nil` if you want the processor to fall back rather than fail the whole request

5. If you want better framework integration, you can optionally implement:
   - `SetLogger(core.Logger)`
   - `SetTelemetry(core.Telemetry)`
   - `SetLLMDebugStore(LLMDebugStore)`

The default processor will detect those methods via type assertions and forward the shared logger, telemetry provider, and debug store when available.

---

## When to Use ConversationHistoryHook

Most chat agents should **not** need a hook for conversation history anymore.

Use `ConversationHistoryHook` only when:

- your integration already has a `ConversationMemory` backend
- you can read conversation history from memory
- but you cannot attach raw turns to request metadata directly

Example:

```go
processor, err := orchestration.BuildConversationHistoryProcessor(config)
if err != nil {
    return fmt.Errorf("failed to build processor: %w", err)
}

hook, err := orchestration.NewConversationHistoryHook(
    convMemory,
    sessionID,
    orchestration.WithConversationHistoryPreparer(processor),
)
if err != nil {
    return fmt.Errorf("failed to create conversation history hook: %w", err)
}
```

If your memory backend implements `core.FullConversationMemory`, the hook can read the full append-only history and keep Tier 2 watermarking safe. If it only implements `GetHistory(..., maxTurns)`, the hook falls back to the text path and behaves more like Tier 1 protection.

If that sentence feels abstract, the practical takeaway is simple:

- metadata path first
- hook path only when the metadata path is not available

---

## What the LLM Sees

When Tier 2 runs, the prepared history generally looks like this:

```text
Conversation Summary:
The user previously shared a draft about enterprise AI agents. The assistant critiqued it and pointed out missing topics around governance, observability, and deployment constraints. The user then asked for a revised version within an 1100-word limit.

Recent Conversation:
Assistant: [improved version...]
User: The audience here are CIO/CTOs. Will this write-up be right for them?
Assistant: Yes, it is suitable because...
User: Can you research on the topics from internet and give me an updated write-up based on the findings?
```

The split is intentional:

- **older turns** become a compact durable summary
- **recent turns** stay verbatim so the freshest working context is preserved

That prepared `<conversation_history>` block is then reused across:

- planning
- continuation prompts
- synthesis

---

## Observability: How to See Compaction Working

If Tier 2 is active and a request actually needs compaction, you should see:

### In Jaeger

- `conversation_history.prepare`
- `conversation_history.compact`

If you only see `conversation_history.prepare`, the request stayed on Tier 1 or Tier 2 was not injected.

### In the Registry Viewer

**LLM Debug**
- `History Compaction`

**Execution DAG**
- a pre-execution conversation-history compaction step

### In logs and metrics

The processor records preparation outcomes such as:

- raw turns
- compacted turns
- verbatim turns kept
- estimated tokens before and after
- path (`metadata_turns`, `metadata_text`, or `hook`)

This makes it much easier to answer practical rollout questions like:

- "Did compaction run?"
- "Did it touch the right turns?"
- "Did we stay under budget?"

---

## Troubleshooting

### I only see Tier 1 behavior

Check these first:

- Are you injecting `ConversationHistoryPreparer` explicitly with `BuildCompactionEnabledConversationHistoryPreparer(...)`?
- Is `agent.AI` available when the preparer is built?
- Are you passing `MetadataConversationTurns` and `MetadataConversationSessionKey`?

If you are only passing `core.EnrichmentConversationHistory` as a formatted string, that is expected to stay on the Tier 1-style text path.

### I see `conversation_history.prepare` but not `conversation_history.compact`

This usually means one of three things:

- the request stayed under budget
- the conversation did not exceed `ConversationRecentTurnsPreserved`
- Tier 2 was not enabled for that agent

### The hook path is not compacting like the metadata path

If you are using `ConversationHistoryHook`, make sure the memory backend implements `core.FullConversationMemory` if you want safe full-history recursive compaction. A windowed `GetHistory(..., maxTurns)` path cannot safely drive the same watermarking behavior.

### Can I use this for arbitrary event payloads?

No. This feature is specifically for **conversation history**. Large inbound event payloads are a different problem and should use a separate agent-owned shaping step or a dedicated framework primitive in the future.

---

## See Also

- [CHAT_AGENT_GUIDE.md](./CHAT_AGENT_GUIDE.md) - End-to-end chat agent architecture and SSE flow
- [ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md](./ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) - Pipeline hooks and context engineering patterns
- [AGENT_DEVELOPMENT_GUIDE.md](./AGENT_DEVELOPMENT_GUIDE.md) - Production agent structure and chat-agent patterns
- [API_REFERENCE.md](./API_REFERENCE.md) - Constructor and interface details
- [orchestration/ARCHITECTURE.md](../orchestration/ARCHITECTURE.md) - Framework-side orchestration architecture
