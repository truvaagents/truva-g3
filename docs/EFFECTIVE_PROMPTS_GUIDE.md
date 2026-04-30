# Effective Prompts Guide

Research-backed principles for writing prompts that LLMs actually follow.

If you've ever had an LLM ignore your instructions, repeat the same mistake across parallel steps, or confidently present hallucinated data — this guide explains why, and how to fix it. These are not opinions. Every principle traces back to published research, official provider documentation, or lessons learned debugging real production failures in a multi-agent orchestration system.

This guide is useful for anyone writing prompts for LLM-based systems. While the examples draw from TruvaG3's multi-agent architecture, the principles apply universally — whether you're building agents, chatbots, code generators, or data extraction pipelines.

> **TruvaG3 Configuration**: For `PromptConfig`, `TypeRules`, `SystemInstructions`, and domain-specific prompt setup, see the [Domain-Specific Agent Configuration Guide](guides/LLM_PLANNING_PROMPT_GUIDE.md).
>
> **Architecture**: For TruvaG3's prompt builder interfaces and multi-phase planning design, see [orchestration/ARCHITECTURE.md](../orchestration/ARCHITECTURE.md).

---

## Table of Contents

- [1. Why Prompts Break](#1-why-prompts-break)
- [2. 11 Research-Backed Principles](#2-11-research-backed-principles)
  - [2.1 Position Critical Information at the Edges](#21-position-critical-information-at-the-edges)
  - [2.2 Stay Under the Bloat Threshold (~3,000 Tokens)](#22-stay-under-the-bloat-threshold-3000-tokens)
  - [2.3 Use Concrete Examples Instead of Rule Lists](#23-use-concrete-examples-instead-of-rule-lists)
  - [2.4 Reframe Negative Instructions as Positive Directives](#24-reframe-negative-instructions-as-positive-directives)
  - [2.5 Start Minimal, Add Complexity Based on Failures](#25-start-minimal-add-complexity-based-on-failures)
  - [2.6 Use 2-5 Few-Shot Examples](#26-use-2-5-few-shot-examples)
  - [2.7 Maintain a Single Consistent Persona](#27-maintain-a-single-consistent-persona)
  - [2.8 Follow the Industry-Converged Section Ordering](#28-follow-the-industry-converged-section-ordering)
  - [2.9 Use the System Message for Identity and Rules](#29-use-the-system-message-for-identity-and-rules)
  - [2.10 Use XML Tags for Section Boundaries](#210-use-xml-tags-for-section-boundaries)
  - [2.11 Standard Models Need Precision; Reasoning Models Prefer Brevity](#211-standard-models-need-precision-reasoning-models-prefer-brevity)
- [3. Common Anti-Patterns](#3-common-anti-patterns)
- [4. 6 Design Principles for Prompt Restructuring](#4-6-design-principles-for-prompt-restructuring)
  - [4.1 Schema + Example > Rules](#41-schema--example---rules)
  - [4.2 Industry-Converged Ordering](#42-industry-converged-ordering)
  - [4.3 Eliminate Negative Instructions](#43-eliminate-negative-instructions)
  - [4.4 Deduplicate Ruthlessly](#44-deduplicate-ruthlessly)
  - [4.5 Slim Down Continuation Prompts](#45-slim-down-continuation-prompts)
  - [4.6 Single Persona, Single CRITICAL](#46-single-persona-single-critical)
  - [4.7 Evidence: Token Reduction Results](#47-evidence-token-reduction-results)
- [5. Cross-Provider Compatibility](#5-cross-provider-compatibility)
  - [5.1 Provider Feature Matrix](#51-provider-feature-matrix)
  - [5.2 Universal Practices](#52-universal-practices)
  - [5.3 Provider-Specific Caveats](#53-provider-specific-caveats)
  - [5.4 Provider Terminology Reference](#54-provider-terminology-reference)
  - [5.5 Why This Structure Works Across All Providers](#55-why-this-structure-works-across-all-providers)
- [6. Dynamic Tool Selection in Multi-Agent Systems](#6-dynamic-tool-selection-in-multi-agent-systems)
  - [6.1 The Problem with Static Tool Selection](#61-the-problem-with-static-tool-selection)
  - [6.2 Framework Survey](#62-framework-survey)
  - [6.3 Academic Evidence](#63-academic-evidence)
  - [6.4 The Controller Equation](#64-the-controller-equation)
  - [6.5 Practical Implications for Prompt Design](#65-practical-implications-for-prompt-design)
- [7. TruvaG3 Application](#7-truvag3-application)
  - [7.1 Principle-to-Implementation Mapping](#71-principle-to-implementation-mapping)
  - [7.2 The Restructured Prompt Architecture](#72-the-restructured-prompt-architecture)
- [8. Quick Reference](#8-quick-reference)
  - [8.1 The 11 Principles at a Glance](#81-the-11-principles-at-a-glance)
  - [8.2 The 6 Design Principles at a Glance](#82-the-6-design-principles-at-a-glance)
  - [8.3 Prompt Structure Template](#83-prompt-structure-template)
  - [8.4 Before/After Summary](#84-beforeafter-summary)
  - [8.5 Prompt Review Checklist](#85-prompt-review-checklist)
  - [8.6 Full Prompt Example: Before and After](#86-full-prompt-example-before-and-after)
- [9. Reserved XML Tags — Orchestration Framework](#9-reserved-xml-tags--orchestration-framework)
  - [9.1 Tags by LLM Phase](#91-tags-by-llm-phase)
  - [9.2 Safe Tag Names for Agent Prompts](#92-safe-tag-names-for-agent-prompts)
- [10. Research Sources](#10-research-sources)

---

## 1. Why Prompts Break

Prompts don't fail randomly. They fail in predictable, studied ways. Three research findings explain most prompt failures:

### The U-Shaped Attention Curve

LLMs don't read your prompt like a human reads a document. They attend most to the **beginning and end**, with performance dropping **30%+** for content in the middle. This was demonstrated by Liu et al. (2024) in "Lost in the Middle" (TACL) and has been replicated across model families.

```
Attention
 High ║ ██                              ██
      ║   ██                          ██
      ║     ██                      ██
      ║       ██   Dead Zone      ██
 Low  ║         ██████████████████
      ╚══════════════════════════════════
        Start       Middle          End
                Position in Prompt
```

If your most important instruction sits at position 5 of 10 sections, the model is statistically likely to degrade on it. This is not a model bug — it is a property of transformer attention.

### The 3,000-Token Cliff

You might think "I have 128K context, so token count doesn't matter." It does. Research by Goldberg et al. shows that reasoning performance degrades significantly at around **3,000 tokens** — well below modern context limits. Even small amounts of irrelevant information cause inconsistent predictions.

Every token in your prompt must earn its place. If you can say it in 2,000 tokens instead of 4,000, do it.

### Examples Beat Rules

Anthropic's 2026 guidance on context engineering puts it bluntly: "Teams will often stuff a laundry list of edge cases into a prompt... We do not recommend this." Instead: "Curate a set of diverse, canonical examples that effectively portray expected behavior." For an LLM, **examples are the "pictures" worth a thousand words**.

One realistic example that demonstrates the desired output format, edge cases, and reasoning will outperform a page of rules trying to describe the same thing.

The rest of this guide shows you how to work with these constraints, not against them.

---

## 2. 11 Research-Backed Principles

### 2.1 Position Critical Information at the Edges

> **Source**: [Lost in the Middle: How Language Models Use Long Contexts](https://arxiv.org/abs/2307.03172) — Liu et al., 2024 (TACL)

Place your most important instructions at the **beginning** and **end** of the prompt. Never bury critical operational rules in the middle.

**Before** — Critical cross-step reference rules at position 5 of 10 sections:
```
1. Persona             ← High attention
2. Agent catalog
3. User request
4. JSON template
5. Cross-step rules    ← CRITICAL RULES buried in dead zone (30%+ degradation)
6. Type rules
7. Custom instructions
8. General rules
9. Iterative planning
10. Format rules       ← High attention
```

**After** — Critical rules moved to position 2 (high attention zone):
```
1. Identity            ← High attention
2. Instructions        ← CRITICAL RULES at top
3. Example             ← Demonstrates rules in action
4. Agent catalog
5. User request
6. Format constraint   ← High attention (final line)
```

This also extends to multi-turn conversations. Research on "LLMs Get Lost in Multi-Turn Conversation" (arXiv:2505.06120) shows the same U-shaped degradation across conversation turns — instructions from earlier turns lose salience.

### 2.2 Stay Under the Bloat Threshold (~3,000 Tokens)

> **Source**: [The Impact of Prompt Bloat on LLM Output Quality](https://mlops.community/the-impact-of-prompt-bloat-on-llm-output-quality/) — Goldberg et al.

Keep your instructional content under ~3,000 tokens. Dynamic content (context data, user requests, capability catalogs) sits on top of this, but the rules and instructions themselves should be compact.

**What to cut:**
- Redundant instructions (the same concept stated 2-3 times)
- Anti-pattern examples showing "wrong" output (see [Principle 2.4](#24-reframe-negative-instructions-as-positive-directives))
- Generic templates that could be replaced by one concrete example
- Rules that the model already follows by default

**Evidence**: Trimming a continuation prompt from 4,234 tokens to ~2,200 tokens (a 47% reduction) eliminated unnecessary phase splits and improved plan quality — with zero loss of instruction compliance.

### 2.3 Use Concrete Examples Instead of Rule Lists

> **Source**: [Effective Context Engineering for AI Agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents) — Anthropic, 2026

One realistic example teaches more than pages of rules, especially for structured output tasks.

**Before** — 450 tokens of rules describing template syntax:
```
CROSS-STEP DATA REFERENCES:
| Syntax | Description |
|--------|-------------|
| {{step-N.response.data.field}} | Reference data from step N |
...
CRITICAL: Always use DOUBLE curly braces {{...}}, NOT single braces {...}
Template references MUST be quoted strings.
depends_on MUST include the referenced step ID.
...
```

**After** — One 250-token example that demonstrates all rules in action:
```json
{
  "steps": [
    {
      "step_id": "step-1",
      "agent_name": "geocoding-tool",
      "depends_on": [],
      "metadata": {
        "capability": "geocode_location",
        "parameters": {"location": "Tokyo"}
      }
    },
    {
      "step_id": "step-2",
      "agent_name": "weather-tool-v2",
      "depends_on": ["step-1"],
      "metadata": {
        "capability": "get_current_weather",
        "parameters": {
          "lat": "{{step-1.response.data.lat}}",
          "lon": "{{step-1.response.data.lon}}"
        }
      }
    }
  ]
}
```

This single example teaches template syntax (`{{double-braces}}`), `depends_on` declarations, quoting rules (templates as strings), parallelism (independent steps have no dependency), and parameter types — all without a single explicit rule.

### 2.4 Reframe Negative Instructions as Positive Directives

> **Source**: [The Pink Elephant Problem: Negative Instructions in LLMs](https://eval.16x.engineer/blog/the-pink-elephant-negative-instructions-llms-effectiveness-analysis)

"Do NOT" phrasing is significantly less effective than positive reframing. Anthropic's official guidance: "Tell Claude what to do instead of what not to do." Negative instructions paradoxically draw the model's attention to the prohibited pattern, making it *more likely* to produce that output.

This is the "Pink Elephant Problem" — tell someone "do NOT think of a pink elephant" and they immediately think of one. LLMs exhibit the same behavior.

| Negative (Avoid) | Positive (Use Instead) |
|---|---|
| "Do NOT wrap JSON in code fences" | "Start with `{` and end with `}`" |
| "Do NOT use markdown formatting: no `**`, no `*`" | "Use plain text in all string values" |
| "WRONG: `{step-1.response.data.id}`" | *(Remove — the correct example already shows double braces)* |
| "NOT strings for literal values (e.g., `\"35.6897\"`)" | "Number parameters use JSON numbers: `35.6897`" |
| "Do NOT put completed step IDs in depends_on" | "`depends_on` references only this phase's step IDs" |

Anti-pattern examples (code blocks labeled "WRONG") are particularly counterproductive — they show the model exactly what it should avoid, which paradoxically increases the probability of producing that output. If you have a correct example, the incorrect form is unnecessary.

### 2.5 Start Minimal, Add Complexity Based on Failures

> **Source**: [Effective Context Engineering for AI Agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents) — Anthropic, 2026

"Start by testing a minimal prompt with the best model available, then add clear instructions and examples to improve performance based on failure modes found during initial testing."

The workflow:

1. Write the shortest prompt that could possibly work
2. Test it with the most capable model you support
3. When it fails, add **one** instruction or example to fix that specific failure
4. Repeat until quality is acceptable
5. Test with less capable models and add instructions only if they fail differently

This prevents prompt bloat. Every instruction in your final prompt exists because a specific failure required it — not because it "seems like a good idea."

### 2.6 Use 2-5 Few-Shot Examples

> **Source**: [The Few-Shot Dilemma: Over-prompting LLMs](https://arxiv.org/html/2509.13196v1)

Research shows that 2-5 well-selected examples outperform verbose instruction lists, but excessive examples cause over-prompting side-effects. The optimal number depends on the task:

| Task Complexity | Recommended Examples | Rationale |
|---|---|---|
| Simple structured output (JSON plan) | 1 concrete example | Format is self-evident from one good example |
| Edge case handling | 2-3 examples | Show the normal case + 1-2 edge cases |
| Complex multi-modal tasks | 3-5 examples | Cover distinct categories of input |

More is not always better. Beyond 5 examples, you risk the model over-indexing on superficial patterns in the examples rather than generalizing the underlying rule.

### 2.7 Maintain a Single Consistent Persona

> **Source**: Persona consistency research; Anthropic and OpenAI best practices

Conflicting role definitions degrade instruction compliance. If your system message says "You are an intelligent orchestrator" but your prompt body says "You are a friendly travel assistant," the model must reconcile competing behavioral frames — and structured output compliance suffers.

**Before** — Two contradictory identities:
```
System message: "You are an intelligent orchestrator that creates execution plans
                 for multi-agent systems."
Prompt body:    "You are a friendly travel chat assistant."
```

**After** — One merged identity:
```
System message: "You are a travel planning assistant that creates JSON execution
                 plans for a multi-agent system."
```

Pick one identity that captures both the domain role and the operational behavior. Place it in the system message (see [Principle 2.9](#29-use-the-system-message-for-identity-and-rules)).

### 2.8 Follow the Industry-Converged Section Ordering

> **Sources**: [OpenAI Prompt Engineering Guide](https://platform.openai.com/docs/guides/prompt-engineering), [Anthropic Claude 4.x Best Practices](https://docs.anthropic.com/en/docs/build-with-claude/prompt-engineering/claude-4-best-practices), [Google Gemini Prompting Strategies](https://ai.google.dev/gemini-api/docs/prompting-strategies)

All major providers converge on the same recommended ordering:

**Identity → Instructions → Examples → Context**

This is not one vendor's opinion. It is industry consensus:

| Provider | Recommended Ordering | Source |
|---|---|---|
| OpenAI | Identity → Instructions → Examples → Context | Prompt Engineering Guide |
| Anthropic | Role → Constraints → Output format (system prompt) | Claude 4.x Best Practices |
| Google Gemini | "Place behavioral constraints, role definitions, and output format at the very beginning" | Prompting Strategies |
| DeepSeek V3 | Standard prompt structure, role at top | General compatibility |

**Why this ordering works:**
- **Identity** at position 1 gets peak attention (U-curve start)
- **Instructions** at position 2 get high attention before degradation begins
- **Examples** at position 3 demonstrate the instructions concretely
- **Context** (dynamic data like tool catalogs, user requests) at the end — it changes per request and should not displace static instructions

This ordering also enables **prompt caching** for providers that support it (OpenAI automatic prefix caching, Anthropic explicit `cache_control`). The Identity + Instructions + Examples block is identical across requests — if it's at the prefix, it can be cached. Dynamic context at the end changes per request and doesn't break the cache.

### 2.9 Use the System Message for Identity and Rules

> **Sources**: [OpenAI Model Spec — Chain of Command](https://model-spec.openai.com/2025-02-12.html#chain_of_command), All major provider documentation

The system-level message carries **higher priority** than user messages across all major providers. Persona and behavioral rules belong in this high-priority message — not embedded in the user prompt.

If you put your identity and rules in the user message, they compete with the actual user content for attention. In the system message, they are structurally elevated.

Different providers use different names for the same concept:

| Provider | High-Priority Message | Low-Priority Message |
|---|---|---|
| OpenAI | `developer` (formerly `system`) | `user` |
| Anthropic | `system` prompt | `human` turn |
| Google Gemini | `system_instruction` | User content |
| OpenAI-compatible (Groq, DeepSeek, xAI, etc.) | `system` role | `user` role |

The underlying principle is universal: identity and rules go in the high-priority message. Dynamic content goes in the user message.

### 2.10 Use XML Tags for Section Boundaries

> **Sources**: [Groundbreaking Research: Prompt Styles and LLMs for Structured Data](https://opendatanewswire.com/research-impact/2025/12/02/groundbreaking-research-compares-prompt-styles-and-llms-for-structured-data-generation-unveiling-key-trade-offs-for-real-world-ai-applications/), [Markdown vs XML in LLM Prompts](https://www.robertodiasduarte.com.br/en/markdown-vs-xml-em-prompts-para-llms-uma-analise-comparativa/)

XML tags for section delineation are the most universally effective structural delimiter across LLM providers:

- **OpenAI**: Explicitly recommends XML tags in prompts
- **Anthropic**: XML is their signature prompting technique
- **Google Gemini**: Endorses `<role>`, `<constraints>`, `<instructions>` tags
- **Llama**: Research shows XML "consistently outperformed other formats for complex prompts"
- **DeepSeek V3**: Documentation confirms "Markdown headers and XML tags significantly improve adherence"

**Before** — Plain-text all-caps labels:
```
CROSS-STEP DATA REFERENCES:
...rules here...

CRITICAL - Parameter Type Rules:
...rules here...

ITERATIVE PLANNING:
...rules here...
```

**After** — XML-tagged sections:
```xml
<instructions>
1. Use depends_on + templates in the same phase for data passing
2. Only set terminal: false when the plan structure depends on result content
3. Budget: up to 3 phases, 20 total steps
4. All parameter types must match capability schema
</instructions>

<example>
{...concrete JSON example...}
</example>

<available_agents>
...dynamic capability catalog...
</available_agents>
```

XML tags help the model parse distinct sections, reduce cross-section "bleed" (where instructions from one section contaminate another), and work reliably across all supported providers.

### 2.11 Standard Models Need Precision; Reasoning Models Prefer Brevity

> **Sources**: OpenAI o3/o4-mini documentation, DeepSeek R1 guidance, Google Gemini thinking mode

Standard (non-reasoning) chat models — GPT-4.1, Claude Sonnet/Haiku, Gemini Flash, Llama, DeepSeek V3 — benefit from precise, detailed instructions with concrete examples.

Reasoning models — OpenAI o3, DeepSeek R1, Gemini with thinking — prefer high-level goals with minimal constraints. These models allocate internal "thinking" tokens that count against the budget. Verbose prompts directly reduce the space available for reasoning. DeepSeek's guidance states explicitly: "too much information reduces accuracy" for R1.

If your system supports both model types (as TruvaG3 does via configurable `smart`/`default` aliases), design prompts that sit at the intersection: **precise enough for standard models, concise enough for reasoning models**. The 31-47% token reduction achieved by applying these principles benefits both families.

---

## 3. Common Anti-Patterns

These are real problems found while debugging production prompt failures. Each links to the principle that fixes it.

| # | Anti-Pattern | Quantified Impact | Fix |
|---|---|---|---|
| 1 | **Critical info in the attention dead zone** — Cross-step reference rules at position 5 of 10 | 30%+ attention degradation (Liu et al.) | [Principle 2.1](#21-position-critical-information-at-the-edges) |
| 2 | **Instruction redundancy** — Same concept stated 2-3 times across sections | Dilutes salience of unique instructions | [Principle 4.4](#44-deduplicate-ruthlessly) |
| 3 | **Excessive negative instructions** — 23 "NOT"/"WRONG"/"NEVER" instances | Each draws attention to the prohibited pattern | [Principle 2.4](#24-reframe-negative-instructions-as-positive-directives) |
| 4 | **No concrete few-shot example** — Generic template with placeholders instead of realistic data | 450 tokens of rules failed to teach what one example could | [Principle 2.3](#23-use-concrete-examples-instead-of-rule-lists) |
| 5 | **Persona conflict** — System says "orchestrator", body says "travel assistant" | Competing behavioral frames degrade structured output | [Principle 2.7](#27-maintain-a-single-consistent-persona) |
| 6 | **Bloated continuation prompts** — Repeating all instructions in Phase 2+ even though the model already produced valid output | 4,234 tokens, 47% over the bloat threshold | [Principle 4.5](#45-slim-down-continuation-prompts) |
| 7 | **Overloaded "CRITICAL" labels** — 3 sections all marked "CRITICAL" | When everything is critical, nothing is critical | [Principle 4.6](#46-single-persona-single-critical) |
| 8 | **Wrong section ordering** — Capability catalog (context) at position 2, before instructions | Prime real estate wasted on dynamic content | [Principle 2.8](#28-follow-the-industry-converged-section-ordering) |
| 9 | **No structural delimiters** — Plain-text all-caps labels with no XML or markdown boundaries | Model struggles to parse distinct sections | [Principle 2.10](#210-use-xml-tags-for-section-boundaries) |
| 10 | **Persona in wrong message role** — Custom identity injected into user message instead of system message | Lower priority than the generic system prompt | [Principle 2.9](#29-use-the-system-message-for-identity-and-rules) |

---

## 4. 6 Design Principles for Prompt Restructuring

Where Section 2 presents the research and Section 3 identifies problems, this section presents the actionable methodology. These are engineering rules you can apply when restructuring any prompt.

### 4.1 Schema + Example > Rules

Replace rule lists with one concrete example that demonstrates the rules in action.

A single example that shows correct template syntax, dependency chains, parallelism, type correctness, and quoting rules teaches all of these simultaneously — without the model needing to mentally compose individual rules into a coherent behavior.

**Token math**: 450 tokens of cross-step reference tables + 200 tokens of generic JSON template = 650 tokens. One concrete example with annotations: 300 tokens. **Net savings: 350 tokens** with better instruction compliance.

Add a brief annotation after the example to call out key patterns:
```
Key rules visible in this example:
- step-2 uses depends_on to reference step-1, and {{double-brace}} templates for data
- step-1 and step-3 run in parallel (no dependency between them)
- Template references are always quoted strings
- Number parameters use JSON numbers, not strings
```

### 4.2 Industry-Converged Ordering

Structure every prompt as:

```
SYSTEM MESSAGE (static across requests):
  Identity → Instructions → Example

USER MESSAGE (per-request):
  Context → Request → Output format constraint (final line)
```

This exploits the U-shaped attention curve (critical rules at top, output format at bottom), follows all provider recommendations, and enables prompt caching (the static prefix is identical across requests).

### 4.3 Eliminate Negative Instructions

Systematically rewrite every negative instruction as a positive directive:

| Negative (Remove) | Positive (Replace With) |
|---|---|
| "Do NOT wrap JSON in code fences" | "Start with `{` and end with `}`" |
| "Do NOT use markdown formatting" | "Use plain text in all string values" |
| "WRONG: `{step-1.response.data.id}`" | *(Delete — the correct example already shows `{{double-braces}}`)* |
| "NOT strings for literal values" | "Number parameters use JSON numbers: `35.6897`" |
| "Do NOT put completed step IDs in depends_on" | "`depends_on` references only this phase's step IDs" |
| "NEVER reference steps from prior phases" | "All `depends_on` targets must be within the current phase" |

If an anti-pattern example exists alongside a correct example, delete the anti-pattern. The correct example is sufficient.

### 4.4 Deduplicate Ruthlessly

Audit your prompt for concepts stated more than once. Each redundancy adds tokens without improving compliance — research shows it dilutes the salience of unique instructions.

| Concept | Found In (Before) | Target (After) |
|---|---|---|
| "Use `{{step-N.response.field}}` syntax" | JSON example, reference instructions, general rules | Example only (self-evident from the concrete case) |
| "Declare dependencies in `depends_on`" | Reference instructions, general rules | Example only |
| "Use DOUBLE curly braces" | Reference header table, reference bottom "CRITICAL" line | Example only (model sees `{{...}}` in the example) |
| "Template references must be quoted strings" | Reference instructions, continuation prompt | Example only |
| "Output only valid JSON" | Format rules header, format rules bullet 1 | Final line only: "Start with `{` and end with `}`" |

**One mention per concept, maximum.** If a concrete example already demonstrates the rule, a separate textual statement is redundant.

### 4.5 Slim Down Continuation Prompts

For multi-turn or multi-phase systems, the follow-up prompt should **not** repeat everything from the first prompt. If the model already produced valid output, it has demonstrated understanding of the rules.

**What to drop in continuation prompts:**
- Full iterative planning instructions (the model already produced `terminal: false`)
- Full agent catalog (only include agents relevant to this phase)
- Generic JSON structure example (the model has already produced valid JSON plans)
- Repeated format rules (the model already formatted correctly)

**What to add:**
- Phase budget: how many phases remain, how many steps are used/remaining
- Completed step summaries: what earlier phases discovered
- An optimization reminder: prefer intra-phase dependency chains over new phases
- A FINAL phase warning when applicable (force `terminal: true`)

```xml
<phase_budget>
Phase: 2 of 3 maximum.
Steps used: 4 of 20. Remaining: 16.
Phases remaining after this: 1.
</phase_budget>

<optimization_reminder>
If remaining steps can reference completed step data via {{step-N.response.data.field}}
templates, include them in THIS phase with depends_on chains rather than requesting
another continuation. Only use terminal: false if you need to discover new entities.
</optimization_reminder>
```

### 4.6 Single Persona, Single CRITICAL

- **One identity** in the system message. Merge domain persona ("travel assistant") with operational role ("creates JSON execution plans") into a single sentence.
- **Reserve "CRITICAL"** for exactly one rule — the one that fails most often in testing. When 3 sections are all marked "CRITICAL", the model cannot differentiate true priority from regular instructions.

### 4.7 Evidence: Token Reduction Results

Applying all 6 principles to TruvaG3's planning prompts produced measurable improvements:

| Metric | Before | After | Reduction |
|---|---|---|---|
| Initial planning prompt | ~2,747 tokens | ~1,900 tokens | **-31%** |
| Continuation prompt (Phase 2+) | ~4,234 tokens | ~2,200 tokens | **-47%** |
| Negative instruction count | 23 | 0 | **-100%** |
| "CRITICAL" labels | 3 | 1 | **-67%** |
| Redundant concept mentions | 5 | 0 | **-100%** |

The continuation prompt savings are especially significant — it dropped below the 3,000-token bloat threshold, eliminating the reasoning degradation observed in Phase 2+ plans.

---

## 5. Cross-Provider Compatibility

If your system supports multiple LLM providers, prompt design must be validated across all of them. The principles in this guide were tested against 9 providers. This section documents what is universal and what requires caveats.

### 5.1 Provider Feature Matrix

| Provider | Alias | Model Family | System Prompt | JSON Mode | Prompt Caching |
|---|---|---|---|---|---|
| OpenAI | `openai` | GPT-4.1, o3, GPT-5 | Yes (`developer` msg) | Yes (API-level) | Auto, 1024+ tokens, 5-10 min TTL |
| Anthropic | `anthropic` | Claude Sonnet/Opus/Haiku | Yes (`system` prompt) | Yes (structured outputs) | Explicit `cache_control`, 4 breakpoints, 5 min TTL |
| Google Gemini | `gemini` | Gemini 2.5/3 | Yes (`system_instruction`) | Yes (`responseJsonSchema`) | 32,768 token min, 1 hr TTL |
| DeepSeek | `openai.deepseek` | DeepSeek-V3, R1 | Yes (V3 + R1-0528+) | Yes (JSON mode) | No |
| Groq | `openai.groq` | Llama 3.x | Yes (OpenAI-compat) | Yes | No |
| xAI | `openai.xai` | Grok | Yes (OpenAI-compat) | Varies | No |
| Qwen | `openai.qwen` | Qwen | Yes (OpenAI-compat) | Varies | No |
| Together | `openai.together` | Various | Yes (OpenAI-compat) | Yes | No |
| Ollama | `openai.ollama` | Llama, Mistral, etc. | Yes (OpenAI-compat) | Varies by model | No |

### 5.2 Universal Practices

These recommendations are confirmed safe across all providers:

| Practice | OpenAI | Anthropic | Gemini | DeepSeek V3 | Llama/Groq | Evidence |
|---|---|---|---|---|---|---|
| **XML tags for section boundaries** | Explicit recommendation | Signature technique | Endorses `<role>`, `<constraints>` | "XML tags improve adherence" | "Consistently outperformed other formats" | Industry convergence |
| **Few-shot examples > verbose rules** | "Include concrete examples" | "Examples are pictures worth a thousand words" | "Always include few-shot examples" | Standard few-shot supported | Effective across Llama family | Universal |
| **Positive instructions > negative** | Implied | "Tell Claude what to do instead of what not to do" | "Negative instructions cause over-indexing" | General best practice | General best practice | Pink Elephant research |
| **System prompt for identity/role** | `developer` message priority | System prompt respected | "Place role definitions in System Instruction" | V3 + R1-0528+: supported | OpenAI-compatible system role | Universal |
| **One concrete example > many rules** | Especially for structured output | Explicit recommendation | "Remove instructions if examples are clear enough" | Supported | Supported | Research-backed |
| **Concise prompts for reasoning models** | o1/o3 prefer high-level goals | Claude adaptive thinking calibrates | Gemini thinking: clear tasks | R1: "too much info reduces accuracy" | N/A (no reasoning variants) | Reasoning model research |

### 5.3 Provider-Specific Caveats

#### 1. Gemini's Dual-Anchor Requirement

Gemini 3 may silently drop constraints that appear only in the middle of the prompt. Google's guidance: "place your core request and most critical restrictions as the final line of your instruction."

**Action**: Critical constraints should be anchored at **both ends** of the prompt — in the system instruction (top) and reinforced as the final line before the request (bottom). The converged ordering in [Principle 2.8](#28-follow-the-industry-converged-section-ordering) naturally satisfies this.

#### 2. Small/Local Model Robustness (Ollama)

Local models via Ollama (Llama 3.2, Mistral, etc.) may have weaker instruction following than frontier models. Concrete examples are especially important for these models because:
- Examples are more robust to weak instruction following than abstract rules
- XML tags help even low-capability models parse structure
- Fewer rules means less chance of rule-dropping

#### 3. Reasoning Model Token Sensitivity

When your system routes to reasoning models (o3, DeepSeek R1, Gemini with thinking), verbose prompts consume the internal reasoning token budget. The 31-47% token reduction from applying these principles is particularly valuable for reasoning model users.

#### 4. Structured Output API vs. Prompt-Based JSON

Several providers support API-level JSON enforcement (`response_format: json_schema`). Prompt-based JSON instruction ("Start with `{` and end with `}`") is the most portable approach — it works with all 9 providers including Ollama. The two approaches are complementary: use API-level enforcement where available, and keep the prompt-based instruction as a universal fallback.

#### 5. Prompt Caching Differences

Static-prefix prompt structure benefits caching, but the mechanism varies wildly:

| Provider | Mechanism | Minimum Tokens | Notes |
|---|---|---|---|
| OpenAI | Automatic prefix matching | 1,024 | Works well with static-prefix strategy |
| Anthropic | Explicit `cache_control` breakpoints | ~1,024 (varies) | Requires code-level integration |
| Gemini | Context caching API | 32,768 | Too high for most instructional prompts |
| Others | None | N/A | No caching benefit |

**Recommendation**: Frame static-prefix ordering as benefiting the **U-shaped attention curve** (universal) with prompt caching as a **bonus for providers that support it**. The principle stays the same regardless of caching support.

### 5.4 Provider Terminology Reference

See the provider message terminology table in [Principle 2.9](#29-use-the-system-message-for-identity-and-rules). Despite the naming differences (`developer`, `system`, `system_instruction`), the underlying principle is identical across all providers: identity and rules go in the high-priority message.

### 5.5 Why This Structure Works Across All Providers

| Design Choice | OpenAI | Anthropic | Gemini | Llama/Groq | DeepSeek |
|---|---|---|---|---|---|
| Identity in system message | High priority per model spec | System prompt respected | `system_instruction` anchors reasoning | OpenAI-compat `system` role | V3 + R1-0528+: supported natively |
| XML tags | Recommended | Signature technique | Recommended | "Consistently outperformed other formats" | "Improves adherence" |
| Concrete example before context | Best practice | Best practice | "Prompts without examples less effective" | Effective | Effective |
| Dynamic context at end | "Near end of prompt" (caching) | Flexible | "Place instructions at end after context" (large ctx) | Flexible | Flexible |
| Format constraint as final line | Good practice | Good practice | **Required** (may drop if too early) | Good practice | Good practice |
| Concise instruction set | Supports reasoning models (o3) | Supports adaptive thinking | Supports Gemini thinking | Reduces rule-dropping for smaller models | R1: "too much info reduces accuracy" |

---

## 6. Dynamic Tool Selection in Multi-Agent Systems

If your system uses multiple tools or agents across phases, how you select tools matters. Static selection (pick tools once, use the same set for every phase) leaves significant accuracy on the table.

### 6.1 The Problem with Static Tool Selection

In a multi-phase orchestration, Phase 1 might discover information that makes new tools relevant for Phase 2. Examples:

| Scenario | Phase 1 Tools | Phase 1 Discovery | Phase 2 Needs (Not in Phase 1 Set) |
|---|---|---|---|
| "Plan my trip to Tokyo" | flight, hotel, weather | User's nationality requires visa | visa-check tool |
| "Research restaurants in Rome" | web-search, news | Top restaurant requires reservation | booking tool |
| "Analyze startup opportunities in Berlin" | web-search, news, country-info | Key companies are publicly traded | stock-market tool |

If your tool selection prompt receives no phase context, Phase 2 will select the same tools as Phase 1 — missing the tools that the discovered information demands.

### 6.2 Framework Survey

Every major framework supports dynamic tool re-evaluation:

| Framework | Tool Selection Model | Re-evaluated Per Phase? | Results Influence Selection? |
|---|---|---|---|
| **LangGraph** (Aug 2025) | Dynamic (RAG-based per step) | Yes — explicit re-selection node | Yes — `reselect_tools` for self-correction |
| **AutoGen/AG2** | Dynamic (context-based) | Yes — via `ReplyResult` routing | Yes — tools determine next-agent flow |
| **CrewAI** | Static-dynamic (per task) | Task-level only | Design-time only |
| **OpenAI API** | Application-controlled | Supported (app changes tools between calls) | Architecturally supported |
| **MCP** (June 2025) | Protocol-level dynamic | Yes — `notifications/tools/list_changed` | Yes — context-dependent discovery |

LangGraph's announcement: "Agents don't always need the same tools at every step." MCP was designed from the ground up for dynamic tool availability.

### 6.3 Academic Evidence

Every paper examined supports dynamic tool re-evaluation, with measured accuracy gains:

| Paper | Year | Key Finding | Accuracy Improvement |
|---|---|---|---|
| **AutoTool** (arXiv:2512.13278) | Dec 2025 | Dynamic selection throughout reasoning trajectories | +4.5% to +7.7% over static |
| **OctoTools** (Stanford, arXiv:2502.11271) | Feb 2025 | Planner-driven dynamic selection with tool cards | +9.3% over GPT-4o baseline |
| **PEARL** (arXiv:2601.20439) | Jan 2026 | RL-trained planner for multi-hop tool use | SOTA 56.5% on ToolHop |
| **Chameleon** (NeurIPS 2023) | 2023 | Dynamic tool routing based on task context | +11.37% on ScienceQA |
| **AvaTaR** (NeurIPS 2024) | 2024 | Contrastive reasoning for tool optimization | +13-14% relative on Hit@1 |

**Unanimous consensus**: 4.5% to 17% accuracy improvement with dynamic selection over static.

### 6.4 The Controller Equation

The survey paper by Xu et al. (2025, "LLM-Based Agents for Tool Learning," Data Science and Engineering) formalizes the pattern:

```
(Plan_{r+1}, T_{r+1}) <- Controller(Plan_r, H_t, f_r, R_r, Q)
```

Where:
- `T_{r+1}` — the tool pool selected for the next iteration
- `H_t` — history of tool invocations (what Phase 1 did)
- `f_r` — feedback from the perceiver (execution outcomes)
- `R_r` — validator's reflection (error analysis)
- `Q` — the original query

The key insight: **the tool pool `T` changes between iterations** based on execution history and feedback. It is not fixed at planning time.

### 6.5 Practical Implications for Prompt Design

To enable context-aware tool selection, your Phase 2+ tool selection prompt should include:

1. **Prior tools used** — which agents/tools were invoked in earlier phases
2. **Continuation note** — the LLM's stated reason for continuing (what it still needs to do)
3. **Compact result summary** — brief description of what earlier phases discovered
4. **Phase number** — so the selection model knows it is not starting from scratch

This context allows the tool selection LLM to pick different (or additional) tools based on what earlier phases discovered — exactly the pattern that research shows yields 4.5-17% accuracy improvements.

---

## 7. TruvaG3 Application

This section maps the universal principles to TruvaG3's specific implementation. For detailed configuration, see the [Domain-Specific Agent Configuration Guide](guides/LLM_PLANNING_PROMPT_GUIDE.md).

### 7.1 Principle-to-Implementation Mapping

| Principle | TruvaG3 Implementation | Key File |
|---|---|---|
| System message priority (2.9) | `SystemPromptBuilder` interface | `orchestration/prompt_builder.go` |
| Converged ordering (2.8) | `buildSystemPrompt()` + `BuildPlanningPrompt()` | `orchestration/default_prompt_builder.go` |
| XML tags (2.10) | `<identity>`, `<instructions>`, `<example>`, `<available_agents>`, `<user_request>` | `orchestration/default_prompt_builder.go` |
| Concrete example (2.3) | `buildConcreteExample()` — realistic few-shot plan | `orchestration/default_prompt_builder.go` |
| Positive instructions (2.4) | All rules rewritten as positive directives | `orchestration/default_prompt_builder.go` |
| Slim continuations (4.5) | `buildContinuationPrompt()` with `<phase_budget>` and `<optimization_reminder>` | `orchestration/orchestrator.go` |
| Single persona (2.7) | Merged identity via `SystemInstructions` config | `orchestration/interfaces.go` |
| Dynamic tool selection (6) | Phase context metadata in `GetCapabilities()` | `orchestration/interfaces.go`, `orchestration/tiered_capability_provider.go` |

### 7.2 The Restructured Prompt Architecture

TruvaG3's planning prompts follow this structure after applying the principles:

```
SYSTEM MESSAGE (via SystemPromptBuilder — static, cacheable):

  <identity>
  You are a travel planning assistant that creates JSON execution plans
  for a multi-agent system.
  </identity>

  <instructions>
  1. If a step's parameters are expressible as {{step-N.response.data.field}}
     templates, include it in the SAME phase with depends_on.
  2. Only set terminal: false when the plan STRUCTURE depends on the
     semantic CONTENT of a result.
  3. Budget: up to 3 phases, 20 total steps. Minimize phases.
  4. All parameter types must match capability schema.
  </instructions>

  <example>
  [Concrete multi-step plan demonstrating depends_on, templates,
   parallelism, types, and the phase split rule]
  </example>

USER MESSAGE (per-request — dynamic content):

  <available_agents>
  [Capability catalog from tiered selection]
  </available_agents>

  <user_request>
  [The actual query]
  </user_request>

  Return a JSON execution plan. Start with { and end with }.
```

**Continuation prompts** (Phase 2+) drop the repeated instructions and example, adding instead:
- `<completed_steps>` — summaries of what earlier phases produced
- `<phase_budget>` — dynamic phase/step counts with FINAL phase warning
- `<optimization_reminder>` — prefer intra-phase depends_on over new phases

> **For configuration details**: See the [Domain-Specific Agent Configuration Guide](guides/LLM_PLANNING_PROMPT_GUIDE.md) for `PromptConfig`, `SystemInstructions`, `CustomInstructions`, `TypeRules`, `TemplatePromptBuilder`, and domain-specific examples (healthcare, finance, travel, e-commerce).

---

## 8. Quick Reference

### 8.1 The 11 Principles at a Glance

| # | Principle | One-Line Rule |
|---|---|---|
| 1 | Position at edges | Critical rules at start and end, never in the middle |
| 2 | Stay under 3K tokens | Every token must earn its place |
| 3 | Examples > rules | One real example beats a page of instructions |
| 4 | Positive instructions | "Start with `{`" not "Do NOT wrap in code fences" |
| 5 | Start minimal | Add complexity only to fix observed failures |
| 6 | 2-5 few-shot | More examples is not always better |
| 7 | Single persona | One identity, no conflicts |
| 8 | Converged ordering | Identity -> Instructions -> Examples -> Context |
| 9 | System message | Persona and rules in the high-priority message |
| 10 | XML tags | `<section>` delimiters for every major block |
| 11 | Model-aware | Precise for standard models, concise for reasoning |

### 8.2 The 6 Design Principles at a Glance

| # | Principle | Action |
|---|---|---|
| 1 | Schema + Example > Rules | Replace rule lists with one annotated concrete example |
| 2 | Industry-converged ordering | Identity -> Instructions -> Examples -> Context |
| 3 | Eliminate negatives | Rewrite every "Do NOT" as a positive directive |
| 4 | Deduplicate | One mention per concept, maximum |
| 5 | Slim continuations | Drop repeated instructions in follow-up prompts |
| 6 | Single CRITICAL | Reserve the label for exactly one rule |

### 8.3 Prompt Structure Template

```
SYSTEM MESSAGE:
  <identity>Who you are and what you do</identity>
  <instructions>Numbered positive rules (concise)</instructions>
  <example>One realistic example demonstrating the rules</example>

USER MESSAGE:
  <context>Dynamic data (tool catalogs, prior results, etc.)</context>
  <request>The actual user query</request>
  Output format constraint as the final line.
```

### 8.4 Before/After Summary

```
Initial prompt:        2,747 → 1,900 tokens  (-31%)
Continuation prompt:   4,234 → 2,200 tokens  (-47%)
Negative instructions:    23 → 0
CRITICAL labels:           3 → 1
Redundant concepts:        5 → 0
```

### 8.5 Prompt Review Checklist

Use this checklist to audit any prompt. Each check maps to a principle — follow the link for the full rationale and examples.

| # | Check | Pass Criteria | Fix |
|---|---|---|---|
| 1 | **Attention positioning** | Critical rules in the first or last 20% of the prompt — nothing important buried in the middle | [2.1](#21-position-critical-information-at-the-edges) |
| 2 | **Token budget** | Instructional content under ~3,000 tokens (excluding dynamic context) | [2.2](#22-stay-under-the-bloat-threshold-3000-tokens) |
| 3 | **Concrete example** | At least 1 realistic example that demonstrates the desired output format | [2.3](#23-use-concrete-examples-instead-of-rule-lists) |
| 4 | **No negative instructions** | Zero "Do NOT", "NEVER", "WRONG" phrasing — all rewritten as positive directives | [2.4](#24-reframe-negative-instructions-as-positive-directives) |
| 5 | **No redundancy** | Each concept stated exactly once — no rule repeated across sections | [4.4](#44-deduplicate-ruthlessly) |
| 6 | **Single persona** | Exactly 1 identity, defined in the system message — no conflicting roles | [2.7](#27-maintain-a-single-consistent-persona) |
| 7 | **Section ordering** | Identity → Instructions → Examples → Context (static before dynamic) | [2.8](#28-follow-the-industry-converged-section-ordering) |
| 8 | **XML boundaries** | Major sections delimited with XML tags (`<instructions>`, `<example>`, etc.) | [2.10](#210-use-xml-tags-for-section-boundaries) |
| 9 | **Single CRITICAL** | "CRITICAL" label used for at most 1 rule | [4.6](#46-single-persona-single-critical) |
| 10 | **Continuation slim** | Follow-up prompts drop repeated instructions; add only phase-specific context | [4.5](#45-slim-down-continuation-prompts) |

### 8.6 Full Prompt Example: Before and After

This shows a real planning prompt restructured using the principles. Use this as a reference when building or reviewing prompts.

**BEFORE** (~2,747 tokens, 10 sections, multiple violations):

```
System: "You are an intelligent orchestrator that creates execution plans
         for multi-agent systems."

User prompt:
  You are a friendly travel chat assistant.          ← PERSONA CONFLICT (2nd identity)

  Available Agents:                                  ← CONTEXT at position 2 (wrong)
  - geocoding-tool: geocode_location(location)
  - weather-tool-v2: get_current_weather(lat, lon)
  - currency-tool: convert_currency(from, to, amount)

  User request: "Weather in Tokyo and convert        ← CONTEXT before instructions
  100 USD to JPY"

  JSON Structure:                                    ← GENERIC template (no real example)
  {"plan_id": "unique-id", "steps": [{"step_id": "step-N", ...}]}

  CROSS-STEP DATA REFERENCES:                        ← DEAD ZONE (position 5 of 10)
  | Syntax | Description |
  | {{step-N.response.data.field}} | Reference... |
  CRITICAL: Always use DOUBLE curly braces
  Template references MUST be quoted strings
  Do NOT use single braces {step-1}                  ← NEGATIVE instruction
  WRONG: {step-1.response.data.id}                   ← ANTI-PATTERN example

  CRITICAL - Parameter Type Rules:                   ← 2nd "CRITICAL" label
  Do NOT use strings for numbers                     ← NEGATIVE instruction
  WRONG: "35.6897"                                   ← ANTI-PATTERN example

  Custom Instructions:
  Prefer parallel steps when possible

  General Rules:
  1. Use agent_name from the catalog
  2. Match parameter types
  3. Order steps by dependencies
  4. Use {{step-N.response.data.field}} syntax        ← REDUNDANT (3rd mention)
  5. Declare dependencies in depends_on               ← REDUNDANT (2nd mention)
  6. ...

  ITERATIVE PLANNING:                                ← BURIED in lower-middle
  Set terminal: false if more phases needed

  CRITICAL FORMAT RULES:                             ← 3rd "CRITICAL" label
  Do NOT wrap JSON in markdown code fences           ← NEGATIVE instruction
  Do NOT use formatting characters                   ← NEGATIVE instruction
  Output only valid JSON                             ← REDUNDANT (2nd mention)
```

**AFTER** (~1,900 tokens, clean structure, all principles applied):

```xml
SYSTEM MESSAGE:

  <identity>
  You are a travel planning assistant that creates JSON execution plans
  for a multi-agent system.
  </identity>

  <instructions>
  1. If a step's parameters are expressible as {{step-N.response.data.field}}
     templates, include it in the SAME phase with depends_on.
  2. Only set terminal: false when the plan STRUCTURE depends on the
     semantic CONTENT of a result.
  3. Budget: up to 3 phases, 20 total steps. Minimize phases.
  4. All parameter types must match capability schema.
  </instructions>

  <example>
  {
    "plan_id": "travel-japan-001",
    "original_request": "Weather in Tokyo and convert 100 USD to JPY",
    "mode": "autonomous",
    "terminal": true,
    "steps": [
      {
        "step_id": "step-1",
        "agent_name": "geocoding-tool",
        "depends_on": [],
        "metadata": {
          "capability": "geocode_location",
          "parameters": {"location": "Tokyo"}
        }
      },
      {
        "step_id": "step-2",
        "agent_name": "weather-tool-v2",
        "depends_on": ["step-1"],
        "metadata": {
          "capability": "get_current_weather",
          "parameters": {
            "lat": "{{step-1.response.data.lat}}",
            "lon": "{{step-1.response.data.lon}}"
          }
        }
      },
      {
        "step_id": "step-3",
        "agent_name": "currency-tool",
        "depends_on": [],
        "metadata": {
          "capability": "convert_currency",
          "parameters": {"from": "USD", "to": "JPY", "amount": 100}
        }
      }
    ]
  }

  Key patterns:
  - step-2 depends on step-1, using {{double-brace}} templates for data
  - step-3 runs in parallel with step-1 (no dependency)
  - Templates are always quoted strings; numbers are JSON numbers
  </example>

USER MESSAGE:

  <available_agents>
  - geocoding-tool: geocode_location(location: string)
  - weather-tool-v2: get_current_weather(lat: number, lon: number)
  - currency-tool: convert_currency(from: string, to: string, amount: number)
  </available_agents>

  <user_request>
  Weather in Tokyo and convert 100 USD to JPY
  </user_request>

  Return a JSON execution plan. Start with { and end with }.
```

**What changed:**
- 1 merged persona in system message (not 2 conflicting ones)
- 4 concise positive instructions (not 23 negative + redundant rules)
- 1 concrete example with real agents (not a generic template + 450 tokens of rule tables)
- XML-tagged sections (not plain-text all-caps labels)
- Identity → Instructions → Example → Context ordering (not context-first)
- Format constraint as the final line (satisfies Gemini's dual-anchor)
- 1 "CRITICAL" would go in instructions if needed (not 3 competing labels)

---

## 9. Reserved XML Tags — Orchestration Framework

When writing `SystemInstructions` or `CustomInstructions` for an agent's `PromptConfig`, **avoid reusing tag names** that the orchestration module already injects into LLM prompts. Duplicate tag names across the system and user messages can confuse the model about which section to follow.

Use distinct tag names for your domain-specific content (e.g., `<workflow>` instead of `<instructions>`, `<workflow_example>` instead of `<example>`).

### 9.1 Tags by LLM Phase

#### Plan Generation (`default_prompt_builder.go`)

Your `SystemInstructions` become the **system message**, which the framework suffixes with a `<runtime_context>` block. The **user message** is built with the remaining tags below:

| Tag | Location | Purpose |
|-----|----------|---------|
| `<runtime_context>` | System msg | Current UTC date with a directive to resolve relative-date references ("today", "tomorrow", "next week") against that value. Appended to the persona by `appendRuntimeContext` in `default_prompt_builder.go`; the same block is emitted by every fallback builder so planners always see a fresh anchor date and do not invent date-arithmetic macros like `{{today_plus_1}}`. |
| `<instructions>` | User msg | Core planning rules (step ordering, depends_on, templates) |
| `<iterative_planning>` | User msg | Phase budget and terminal/non-terminal guidance |
| `<example>` | User msg | Concrete JSON plan example (weather + currency) |
| `<type_rules>` | User msg | JSON type mapping (string, number, array, etc.) + `AdditionalTypeRules` |
| `<domain_rules>` | User msg | Domain-specific instructions (healthcare/finance/legal) |
| `<custom_instructions>` | User msg | Your `CustomInstructions` array, numbered |
| `<agent_identity>` | User msg | The name of the agent whose orchestrator is generating this plan. Tells the LLM which agent it belongs to so it can self-reference (e.g., setting `target_agent` in `schedule_task`). Only present when `OrchestratorConfig.Name` is set. |
| `<available_agents>` | User msg | Discovered tools/capabilities with schemas |
| `<agent_coordination>` | User msg | Real-time activity signals from other agents in the same domain. Injected by `ActivityAnnouncementHook` via `EnrichmentActivityCoordination`. Shows what other agents are currently working on (agent name, query, status, timing). Only present when activity coordination is enabled and other agents are active. |
| `<agent_memory>` | User msg | Cross-agent shared memory context injected by pipeline hooks (episodic events, active investigations, prior knowledge). Only present when memory hooks are registered. |
| `<conversation_history>` | User msg | Session conversation history for chat agents. Injected through the shared conversation-history preparation path via `EnrichmentConversationHistory` from raw metadata or the optional `ConversationHistoryHook`. Only present for session-based agents. Separates history from the current request for clean prompt structure per §2.8 and §2.10. |
| `<context_precedence>` | User msg | One-line directive emitted immediately before `<user_request>` whenever `<user_profile>` and/or `<conversation_history>` are present. Tells the planner that the live turn (current `<user_request>`, then the most recent `<conversation_history>`) wins over any stored profile "Context" when they conflict on a subject. Lands in the high-attention tail per §2.1. Emission is observable via the `orchestrator.context_precedence.evaluated` counter (labels: `prompt_kind`, `emitted`) and the `orchestrator.context_precedence.emitted` span event. Per-interaction compliance metadata is captured on `LLMInteraction.precedence_audit` (see §9.3). |
| `<user_request>` | User msg | The actual user query (current request only — no conversation history mixed in) |
| `<user_profile>` | User msg | Per-user private facts (identity, preferences, constraints, session summaries) injected by `UserMemoryEnrichmentHook` via `EnrichmentUserProfile`. Only present for agents with user memory enabled. Separate from `<agent_memory>` — different privacy boundary. Durable facts render as `(source, <high/medium/low> confidence)`; transient and summary facts render as `(source, recorded <age>)` so the planner can judge staleness. When `<user_profile>` conflicts with the live turn, `<context_precedence>` directs the planner to trust the live turn. |

#### User Memory Extraction (`user_memory_extraction.go`)

Three standalone LLM calls, each with its own prompt. All tags use the `user_memory_` prefix to avoid conflicts if ever inlined into planning/synthesis prompts.

**Fact Extraction:**

| Tag | Location | Purpose |
|-----|----------|---------|
| `<user_memory_role>` | User msg | Persona: "You are a user fact extractor" |
| `<user_memory_extraction_rules>` | User msg | Persistence classification (persistent vs one-time vs contextual) |
| `<user_memory_extraction_example>` | User msg | Concrete input/output example for the extraction task |
| `<user_memory_extraction_example_2>` | User msg | Second concrete example showing durable preference vs active trip context classification |
| `<user_memory_extraction_example_3>` | User msg | Third concrete example: pronoun-only user turn where the destination appears only in the assistant response, expected output is `[]`. Anchors the attribution rule that prevents assistant-echoed entities from becoming user facts (closes the stored-context feedback loop). |
| `<user_memory_user_message>` | User msg | The user's request being analyzed |
| `<user_memory_assistant_message>` | User msg | The agent's response being analyzed |

**Fact Reconciliation:**

| Tag | Location | Purpose |
|-----|----------|---------|
| `<user_memory_role>` | User msg | Persona: "You are a user memory reconciliation system" |
| `<user_memory_reconciliation_rules>` | User msg | ADD/UPDATE/CONTRADICT/DUPLICATE classification rules |
| `<user_memory_existing_facts>` | User msg | Existing facts being compared against a new candidate (per-candidate prompt) |
| `<user_memory_new_fact>` | User msg | The candidate fact being reconciled (per-candidate prompt) |
| `<user_memory_candidates>` | User msg | All candidates + their own neighbor lists for the batched reconciliation prompt (one LLM call classifies every candidate) |

**Session Summary:**

| Tag | Location | Purpose |
|-----|----------|---------|
| `<user_memory_role>` | User msg | Persona: "You are a session summarizer" |
| `<user_memory_summary_rules>` | User msg | Summary formatting rules (one sentence, third person, attribution rule) |
| `<user_memory_summary_example>` | User msg | Concrete input/output example for a clean user-led turn |
| `<user_memory_summary_example_2>` | User msg | Second concrete example: pronoun-only user turn where the agent suggests an entity. Anchors the attribution rule that prevents assistant-echoed entities from being summarized as user decisions (closes the stored-context feedback loop on the summary path). |
| `<user_memory_user_message>` | User msg | The user's request being summarized |
| `<user_memory_assistant_message>` | User msg | The agent's response being summarized |

> **Note:** All user memory tags are used in standalone LLM calls (not in planning or synthesis prompts), so they cannot conflict with reserved tags. The `user_memory_` prefix ensures future-safety.

#### Tiered Tool Selection (`tiered_capability_provider.go`)

| Tag | Location | Purpose |
|-----|----------|---------|
| `<identity>` | System msg | "You are a tool selector for a multi-agent system" |
| `<selection_guide>` | User msg | A/B/C reasoning structure for tool selection |
| `<available_tools>` | User msg | Formatted tool list (agent/capability: description) |
| `<user_request>` | User msg | The user's request |
| `<output_format>` | User msg | JSON array format specification with example |
| `<phase_context>` | User msg | Prior tools used (continuation phases only) |
| `<custom_instructions>` | User msg | Domain-specific workflow rules from CustomInstructions, numbered (ORCH-014) |

#### Synthesis (`synthesizer.go`)

| Tag | Location | Purpose |
|-----|----------|---------|
| `<identity>` | System msg | "You are an AI synthesis engine" |
| `<instructions>` | System msg | 6 synthesis rules |
| `<clarification_mode>` | System msg | Conversational guidance appended when `ExecutionResult.ClarificationNeeded` is set — directs the synthesizer to summarize partial progress and weave the planner's question into a natural reply. |
| `<user_request>` | User msg | Original user request |
| `<agent_responses>` | User msg | Container for all agent outputs |
| `<agent>` | User msg | Individual agent result (attrs: `name`, `task`, `status`) |
| `<clarification_needed>` | User msg | Structured clarification data (question, missing_fields, partial_progress) when the planner emitted `needs_user_input`. Only present when `ExecutionResult.ClarificationNeeded` is set. |

#### Micro-Resolution (`micro_resolver.go`)

| Tag | Location | Purpose |
|-----|----------|---------|
| `<identity>` | System msg | "You are a parameter extraction assistant" |
| `<instructions>` | System/User msg | Extraction or mapping rules |
| `<source_data>` | User msg | Raw data to extract values from |
| `<target_parameters>` | User msg | Parameters needed (attr: `capability`) |
| `<data_structure>` | User msg | Structural summary for schema mapping |
| `<example>` | User msg | Schema mapping example |

#### Semantic Retry (`contextual_re_resolver.go`)

| Tag | Location | Purpose |
|-----|----------|---------|
| `<identity>` | System msg | "You are a parameter re-resolution assistant" |
| `<instructions>` | System msg | Re-resolution rules |
| `<user_request>` | User msg | Original user intent |
| `<source_data>` | User msg | Available data from dependencies |
| `<failed_attempt>` | User msg | What was tried (attrs: `capability`, `status`) |
| `<previous_failed_attempts>` | User msg | History of prior failures |
| `<capability_schema>` | User msg | Required parameters and types |

#### Error Analysis (`error_analyzer.go`)

| Tag | Location | Purpose |
|-----|----------|---------|
| `<identity>` | System msg | "You are an error analysis assistant" |
| `<instructions>` | System msg | Analysis rules |
| `<capability>` | User msg | Failed tool (attr: `name`) |
| `<original_request>` | User msg | Parameters that were sent |
| `<error_response>` | User msg | Error from the tool (attr: `status`) |
| `<user_query>` | User msg | Original user intent |

#### Continuation Planning (`orchestrator.go`)

| Tag | Location | Purpose |
|-----|----------|---------|
| `<user_request>` | User msg | Original user request |
| `<completed_steps>` | User msg | Results from prior phase steps |
| `<executed_ids>` | User msg | Step IDs already completed + next ID range |
| `<phase_budget>` | User msg | Phase and step budget info |
| `<optimization_reminder>` | User msg | Guidance on minimizing phase splits |
| `<previous_note>` | User msg | Continuation note from prior phase |
| `<upstream_failures>` | User msg | Nested inside `<previous_note>` when the prior phase had steps skipped because their upstream template-referenced dependencies failed. Carries the per-skip enumeration of failed upstream deps (with single-lined / length-bounded errors), the optional one-line failure-pattern summary, and the planner directive (propose alternative approach OR return `terminal:true, steps:[]`). Emitted by `buildRemediationContinuationNote`. |
| `<available_agents>` | User msg | Capability catalog |
| `<custom_instructions>` | User msg | Your `CustomInstructions` |
| `<planning_instructions>` | User msg | Next phase planning guidance |
| `<parse_error>` | User msg | JSON parsing error feedback (attr: `detail`) |
| `<json_rules>` | User msg | JSON correction rules |

#### Event Summarization (`event_summarizer.go`)

| Tag | Location | Purpose |
|-----|----------|---------|
| `<identity>` | System msg | "You are an execution step summarizer for a multi-agent orchestration system" |
| `<instructions>` | System msg | 6 factual summarization rules |
| `<example>` | System msg | Concrete input→output example with diverse tool types (§2.3, §2.6) |
| `<steps>` | User msg | Container for all execution steps to summarize |
| `<step>` | User msg | Individual step (attr: `id`). Contains `<agent>`, `<capability>`, `<instruction>`, `<parameters>`, `<response>`, `<outcome>` |

#### Result Distillation (`result_distiller.go`)

| Tag | Location | Purpose |
|-----|----------|---------|
| `<identity>` | System msg | "You are a data distillation assistant" |
| `<instructions>` | System msg | Distillation rules with byte limit |
| `<context>` | User msg | Downstream task context (attrs: `source`, `capability`) |
| `<data>` | User msg | Data to distill |

### 9.2 Safe Tag Names for Agent Prompts

Since the framework reserves `<instructions>`, `<example>`, `<identity>`, `<available_agents>`, `<agent_memory>`, `<conversation_history>`, `<user_profile>`, and `<context_precedence>` in its prompt templates, use **distinct names** in your `SystemInstructions`:

| Instead of | Use |
|-----------|-----|
| `<instructions>` | `<workflow>`, `<rules>`, `<guidelines>` |
| `<example>` | `<workflow_example>`, `<plan_example>`, `<domain_example>` |
| `<identity>` | OK in SystemInstructions — it becomes the system message, separate from the user message where the framework uses `<identity>` |
| `<context>` | `<domain_context>`, `<background>` |
| `<user_profile>` | `<agent_user_context>`, `<assistant_user_state>` |
| `<context_precedence>` | `<workflow_precedence>`, `<conflict_resolution>` |

**Example** — QA agent SystemInstructions with safe tag names:
```
<identity>
You are an autonomous QA testing agent...
</identity>

<workflow>
Your plan always has exactly 4 steps...
</workflow>

<workflow_example>
For the request "Test https://example.com", the plan is:
step-1: explore_page on playwright-tool...
</workflow_example>
```

### 9.3 Context Precedence Observability

Every planning- or synthesis-style prompt that can carry conflicting enrichments emits two signals so compliance can be dashboarded and debugged without scanning raw prompt text.

**Telemetry (emitted by `writeContextPrecedence`):**

| Signal | Shape | When |
|---|---|---|
| `orchestrator.context_precedence.evaluated` (counter) | Labels: `prompt_kind`, `emitted` ("true"/"false") | Every call. Gives denominator (how often the code path ran) AND emission rate. |
| `orchestrator.context_precedence.emitted` (span event) | Attributes: `prompt_kind`, `has_profile`, `has_history` | Only when the directive is actually emitted. Attached to the current span so Jaeger stays clean. |

`prompt_kind` values: `planning`, `planning_fallback`, `continuation`, `synthesis`, `synthesis_orchestrator` (constants: `PromptKindPlanning`, etc. in `orchestration/default_prompt_builder.go`).

**Per-interaction audit (on `LLMInteraction.precedence_audit`):**

Populated by the central `recordDebugInteraction` path via `DerivePrecedenceAudit`. Self-gated: nil for interactions whose prompt has no `<user_profile>` or `<conversation_history>`, so hook LLM calls / micro-resolution / tiered-selection records stay clean.

Always-available fields (no extractor required):
- `directive_emitted`, `profile_present`, `history_present`, `prompt_kind`

Opt-in fields (populated only when `AIOrchestrator.SetPrecedenceEntityExtractor` is wired):
- `profile_context_entities`, `conversation_entities`, `request_entities`, `plan_target_entities` — per-section entity lists.
- `compliance` — heuristic label: `compliant` | `anchored_on_profile` | `inconclusive`.
- `auditor_version` — version string from the extractor implementation; lets the registry viewer hide/demote old records when the extractor evolves.

Entity extraction is opt-in because named-entity recognition is domain-specific; the framework stays domain-agnostic per `FRAMEWORK_DESIGN_PRINCIPLES.md §Framework is domain-agnostic`. Agents that need compliance detection implement `PrecedenceEntityExtractor` and wire it at startup.

**Contract:**
```go
type PrecedenceEntityExtractor interface {
    ExtractEntities(ctx context.Context, section PrecedenceSection, text string) []string
    Version() string
}
```

The audit record JSON is stable — adding a registry-viewer card that surfaces it is purely additive JS. See the struct definition in [orchestration/precedence_audit.go](../orchestration/precedence_audit.go).

---

## 10. Research Sources

### Core Prompt Engineering Research

- [Lost in the Middle: How Language Models Use Long Contexts](https://arxiv.org/abs/2307.03172) — Liu et al., 2024 (TACL). U-shaped attention curve, 30%+ degradation in middle positions.
- [The Impact of Prompt Bloat on LLM Output Quality](https://mlops.community/the-impact-of-prompt-bloat-on-llm-output-quality/) — Goldberg et al. Reasoning degrades at ~3,000 tokens.
- [Effective Context Engineering for AI Agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents) — Anthropic, 2026. "Examples are the pictures worth a thousand words." Minimal-first approach.
- [The Pink Elephant Problem: Negative Instructions in LLMs](https://eval.16x.engineer/blog/the-pink-elephant-negative-instructions-llms-effectiveness-analysis) — Negative instructions paradoxically increase prohibited output probability.
- [The Few-Shot Dilemma: Over-prompting LLMs](https://arxiv.org/html/2509.13196v1) — 2-5 well-selected examples outperform verbose rules; excessive examples cause degradation.
- [LLMs Get Lost in Multi-Turn Conversation](https://arxiv.org/html/2505.06120v1) — Extends "lost in the middle" to conversational settings.

### Provider-Specific Guides

- [Prompt Engineering — OpenAI Official Guide](https://platform.openai.com/docs/guides/prompt-engineering) — Identity → Instructions → Examples → Context ordering. Developer messages prioritized. XML tags recommended.
- [OpenAI Model Spec — Chain of Command](https://model-spec.openai.com/2025-02-12.html#chain_of_command) — `developer` messages take priority over `user` messages.
- [Anthropic Claude 4.x Best Practices](https://docs.anthropic.com/en/docs/build-with-claude/prompt-engineering/claude-4-best-practices) — XML tags for structure, "tell Claude what to do instead of what not to do."
- [Google Gemini Prompting Strategies](https://ai.google.dev/gemini-api/docs/prompting-strategies) — "Place behavioral constraints at the beginning," "always include few-shot examples."
- [Gemini 3 Prompting Best Practices](https://www.philschmid.de/gemini-3-prompt-practices) — Critical restrictions at final line, negative constraints may be dropped if too early.
- [DeepSeek Prompting Techniques](https://www.datastudios.org/post/deepseek-prompting-techniques-strategies-limits-best-practices-etc) — V3 supports standard techniques; R1 prefers concise prompts.

### Cross-Provider Comparison Studies

- [Groundbreaking Research: Prompt Styles and LLMs for Structured Data](https://opendatanewswire.com/research-impact/2025/12/02/groundbreaking-research-compares-prompt-styles-and-llms-for-structured-data-generation-unveiling-key-trade-offs-for-real-world-ai-applications/) — Claude leads accuracy (85%) with hierarchical formats; GPT-4o leads token efficiency.
- [Markdown vs XML in LLM Prompts](https://www.robertodiasduarte.com.br/en/markdown-vs-xml-em-prompts-para-llms-uma-analise-comparativa/) — XML outperforms for complex prompts across model families.
- [Llama Cookbook: XML Tags vs Markdown](https://github.com/meta-llama/llama-cookbook/issues/450) — Llama 3.x responds well to XML structuring.
- [Cross-Provider Prompt Format Research (2025)](https://medium.com/@isaiahdupree33/optimal-prompt-formats-for-llms-xml-vs-markdown-performance-insights-cef650b856db) — XML converges as preferred format across providers.
- [Prompt Caching Comparison: OpenAI, Anthropic, Gemini](https://medium.com/@m_sea_bass/comparing-prompt-caching-openai-anthropic-and-gemini-0eac16541898) — Implementation differs per provider; Anthropic 90% discount, Gemini 32K minimum, OpenAI automatic.

### Dynamic Tool Selection — Academic Papers

- [AutoTool (arXiv:2512.13278)](https://arxiv.org/abs/2512.13278) — Dec 2025. Dynamic tool selection throughout reasoning trajectories. +4.5% to +7.7% over static.
- [OctoTools (Stanford, arXiv:2502.11271)](https://arxiv.org/abs/2502.11271) — Feb 2025. Planner-driven dynamic selection with standardized tool cards. +9.3% over GPT-4o baseline.
- [PEARL (arXiv:2601.20439)](https://arxiv.org/abs/2601.20439) — Jan 2026. RL-trained planner for multi-hop tool use. SOTA 56.5% on ToolHop.
- **Chameleon** — Lu et al., NeurIPS 2023. Dynamic tool routing based on task context. +11.37% on ScienceQA.
- **AvaTaR** — NeurIPS 2024. Contrastive reasoning for tool optimization. +13-14% relative on Hit@1.
- **"LLM-Based Agents for Tool Learning: A Survey"** — Xu et al., 2025, Data Science and Engineering, Vol. 10, pp. 533-563. Formalizes the controller equation for dynamic tool pool management.

### Dynamic Tool Selection — Framework Documentation

- **LangGraph** (Aug 2025) — "Dynamic Tool Calling in LangGraph Agents": per-step semantic search, `reselect_tools` fallback tool.
- **AutoGen/AG2** — Dynamic context-based tool selection via `ReplyResult` routing.
- **MCP (Model Context Protocol)** (June 2025) — Protocol-level dynamic tool availability via `tools/list` and `notifications/tools/list_changed`.
- **CrewAI** — Task-level static-dynamic tool selection.

### Related TruvaG3 Documentation

- [Domain-Specific Agent Configuration Guide](guides/LLM_PLANNING_PROMPT_GUIDE.md) — `PromptConfig`, `SystemInstructions`, `TypeRules`, domain examples
- [AI Providers Setup Guide](AI_PROVIDERS_SETUP_GUIDE.md) — Provider configuration, API keys, model aliases
- [Orchestration Architecture](../orchestration/ARCHITECTURE.md) — Prompt builder interfaces, multi-phase planning design

---

*This guide was distilled from research conducted during production debugging of TruvaG3's orchestration module. Every principle was validated against real execution traces and applied to fix measurable prompt quality issues.*
