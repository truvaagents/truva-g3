# Result Compaction Guide

This guide explains how TruvaG3 keeps large tool and agent results from overwhelming your LLM prompts — and how to tune that behavior. If you've ever watched an agent fetch a megabyte of logs or a giant JSON payload and wondered "how does that ever fit back into the model?", this guide is for you. The good news: it all runs **automatically** inside any orchestrating agent that has an `AIClient` — you don't need to configure anything to get it.

---

## Table of Contents

1. [What Is Result Compaction (and Why You Need It)](#1-what-is-result-compaction-and-why-you-need-it)
2. [The Big Picture: Three Layers, Two Prompt Paths](#2-the-big-picture-three-layers-two-prompt-paths)
3. [Quick Reference](#3-quick-reference)
4. [Layer 1 — Result Trimming (the structural floor)](#4-layer-1--result-trimming-the-structural-floor)
5. [Layer 2 — Result Distillation (LLM-first, the synthesis path)](#5-layer-2--result-distillation-llm-first-the-synthesis-path)
6. [Layer 3 — Continuation Digests (the planning path)](#6-layer-3--continuation-digests-the-planning-path)
7. [How a Result Flows Through](#7-how-a-result-flows-through)
8. [Configuration](#8-configuration)
9. [Customizing and Extending](#9-customizing-and-extending)
10. [Observability](#10-observability)
11. [Cost and Latency](#11-cost-and-latency)
12. [Tuning Scenarios](#12-tuning-scenarios)
13. [Troubleshooting](#13-troubleshooting)
14. [Reference](#14-reference)

---

## 1. What Is Result Compaction (and Why You Need It)

When an agent runs a multi-step plan, each step produces a result, and those results have to be fed back into the LLM in two places:

1. **Synthesis** — the final-answer prompt, which includes the step results the answer is built from.
2. **Continuation planning** — between phases, the planner re-reads what's been done so far to decide the next steps.

The problem: real tool results aren't small. A `kubectl get pods -o json` over a busy namespace is ~1 MB. A Loki query can return hundreds of log streams. A web scrape is tens of KB. Put all of that into a prompt verbatim and you run into two limits:

- **Hard limit** — you exceed the model's context window and the call fails.
- **Soft limit (the bigger one)** — reasoning accuracy *degrades* well before the context fills. See [EFFECTIVE_PROMPTS_GUIDE §2.2](../building/EFFECTIVE_PROMPTS_GUIDE.md#22-stay-under-the-bloat-threshold-3000-tokens): accuracy on a controlled task drops from ~0.92 to ~0.68 as inputs grow into the low thousands of tokens. A prompt full of raw payload is a prompt that reasons worse.

**Result compaction** is the framework's answer: shrink each result to fit a budget while preserving the signal the model actually needs — and do it automatically, so agent authors don't have to think about it.

### See It in Action

The clearest way to feel this is a data-heavy request. Point the [`devops-chat-agent`](https://github.com/truvaagents/truva-g3/tree/main/examples/devops-chat-agent) (with the [`devops-tool`](https://github.com/truvaagents/truva-g3/tree/main/examples/devops-tool) and [`devops-observability-tool`](https://github.com/truvaagents/truva-g3/tree/main/examples/devops-observability-tool) deployed) at a query like:

> *"I want you to do a deep analysis of the logs for the pods that have `tool` in their name and are in the truvag3-examples namespace. Analyze the logs from the last 30 minutes, and tell me how things are looking. Are there any operational or performance issues?"*

To answer this, the agent fans out across both tools — `devops-tool` lists/inspects the pods (`kubectl … -o json`, ~1 MB) and `devops-observability-tool` pulls each pod's Loki logs (hundreds of KB to several MB). Instead of stuffing all of that into the synthesis prompt, the orchestrator **distills** each oversized result down to a query-relevant summary on a fast model (chunking and map-reducing the biggest ones). You can watch it happen in the registry-viewer's execution diagram — the `distill` nodes are the compaction calls sitting between the tool steps and the final synthesis:

![Result compaction (distillation) shown as distill nodes in the execution diagram](https://assets.truvag3.dev/images/truvag3-result-compaction-example-new.png)

Open the same request in the registry-viewer's LLM-debug view to see the `result_distillation` interactions in detail (including `map-reduce chunk i/N` calls for the largest results). The rest of this guide explains what each of those steps does and how to tune it.

---

## 2. The Big Picture: Three Layers, Two Prompt Paths

There are **three compaction mechanisms**, and which one runs depends on *where* the result is headed.

| Layer | Runs on | LLM? | What it produces |
|---|---|---|---|
| **Result Trimming** (`StructuralTrimmer`) | synthesis, resolution-source & tool-input paths; the synthesis fail-open floor | No | a size-bounded subset (JSON → valid-JSON body + a `[trimmed …]` note; non-JSON → plain-text truncation) |
| **Result Distillation** (`LLMDistiller`) | the **synthesis** prompt | Yes (fast model) | a query-relevant summary |
| **Continuation Digests** | the **continuation planning** prompt | No (rarely yes) | structure-complete JSON skeletons |

The mental model:

```
                          ┌──────────────────────────────────────────────┐
  step result (maybe MB)  │                                              │
  ───────────────────────▶│   over a size budget?                        │
                          │      │                                       │
                          │      ├─ headed to SYNTHESIS  ── Distillation │
                          │      │     (LLM-first; structural floor if   │
                          │      │      no AIClient; map-reduce if huge) │
                          │      │                                       │
                          │      └─ headed to CONTINUATION ── Digest     │
                          │            (JSON skeleton; non-JSON → floor; │
                          │             rare fast-model escalation)      │
                          │                                              │
                          │   Trimming is the synthesis path's           │
                          │   deterministic floor (alone if no           │
                          │   AIClient). Continuation has its own.       │
                          └──────────────────────────────────────────────┘
```

Three design principles tie it together:

- **Fail-open, always.** Every layer degrades to a smaller, safe result rather than failing the request — no AIClient → structural floor; cache error → recompute; deadline hit → partial + disclosure.
- **Default-on.** You get sensible compaction out of the box; you opt *out* or tune, you don't opt in.
- **Prompt-only — the full result is never lost.** Compaction shrinks only what the *LLM sees in a given prompt*. The complete, untouched result stays in the orchestrator's step memory, so `{{step-N.fieldpath}}` templates always resolve against the **full** value at execution time. A digest or summary is a *view for reasoning*, never a replacement for the data — which is what makes lossy compaction safe.

---

## 3. Quick Reference

Defaults that matter most (full tables in the [Limits Cheatsheet](../reference/LIMITS_CHEATSHEET.md)):

| Setting | Default | Env Var |
|---|---|---|
| Result trimming enabled | `true` | `TRUVAG3_RESULT_TRIM_ENABLED` |
| Per-result trim budget | 16 KB | `TRUVAG3_RESULT_TRIM_MAX_BYTES` |
| Distillation enabled | `true` | `TRUVAG3_RESULT_DISTILL_ENABLED` |
| Distill trigger size | 16 KB | `TRUVAG3_RESULT_DISTILL_THRESHOLD` |
| Distill target output | 4 KB | `TRUVAG3_RESULT_DISTILL_TARGET` |
| Distill model | `fast` alias | `TRUVAG3_RESULT_DISTILL_MODEL` |
| Map-reduce trigger (tokens) | 150000 tokens | `TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS` |
| Map-reduce trigger (bytes) | `0` (disabled) | `TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD` |
| Continuation aggregate budget | 32 KB | `TRUVAG3_CONTINUATION_RESULT_MAX_TOTAL_CHARS` |
| Continuation non-JSON floor | 10000 chars | `TRUVAG3_CONTINUATION_RESULT_MAX_CHARS` |

> All byte budgets are integers in bytes (16 KB = `16384`). All values are env-tunable without a code change.

---

## 4. Layer 1 — Result Trimming (the structural floor)

The `StructuralTrimmer` is the deterministic, LLM-free baseline. It does **whole-unit structural selection** to fit a byte budget: it inventories the result's fields and array items, then keeps whole units in breadth-first order — shallowest first, and largest-first within a depth — descending into a unit only when it's too big to keep whole, so a unit that fits is kept intact rather than decomposed. Units that don't fit are dropped (recorded as `FieldsDropped`). A **top-level** array is filled from its head; a **nested** array is first capped to its first 500 items and then run through the same whole-unit sort (shallowest-then-largest-first), so its kept items aren't strictly a head sample. Crucially, the primary cut is **not** a query-relevance ranking — that's distillation's job; relevance scoring here only feeds a secondary backfill pass and key ordering, never the main selection. On the JSON path the kept payload is valid JSON, but the trimmer appends a short plain-text annotation — `[trimmed: N/M fields kept …]`, or `[severely reduced … treat as UNKNOWN]` when it keeps under 5% — so the **returned string is a JSON body followed by that note, not a pure-JSON document**. A **non-JSON** input (logs, plain text) skips structure entirely: it's truncated as text, with the same annotation, so there's no JSON body at all. (The always-valid-JSON guarantee belongs to continuation digests; see §6.) It is *structure-aware*, not *structure-complete*: it can drop whole fields. Because it makes no LLM call, it's fast, free, and always safe — which is why it also serves as the **fail-open floor** for the synthesis distiller.

It runs in several spots, each with its own budget:

| Budget | Default | Env Var | Purpose |
|---|---|---|---|
| Per-result cap | 16 KB | `TRUVAG3_RESULT_TRIM_MAX_BYTES` | per-result ceiling when **several** results share one synthesis prompt (multi-result allocation) |
| Total prompt | 32 KB | `TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES` | all results combined in one synthesis prompt — and the per-call budget when a **single** result is processed |
| Micro-resolution source | 64 KB | `TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES` | source data fed to parameter micro-resolution / semantic retry |
| Agent input per-param | **0 (no cap)** | `TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES` | tool→tool data flow |
| Schema-mapping threshold | 16 KB | `TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD` | switch micro-resolution to schema-guided mapping above this |

**Fidelity-first tool→tool flow.** Note that agent-input trimming defaults to `0` — *no cap*. When one tool's output feeds another tool's input, the downstream tool gets the **full** upstream result, not a trimmed one. The reasoning: a tool consuming data programmatically usually needs all of it, unlike an LLM consuming it for reasoning. Set it `> 0` (or supply a custom `AgentInputProcessor`) only if you specifically want to bound inter-tool payloads.

**Prioritizing specific fields.** `ResultTrim.PreserveKeys` lists object keys — matched **case-insensitively** — that the trimmer should favor keeping. Each listed key gets a large boost in the trimmer's relevance score, which (as above) drives the **secondary backfill pass and key ordering**, not the primary whole-unit cut: preserved fields are strongly favored when the trimmer fills the remaining budget, and are surfaced earlier in what it keeps. Set it via `WithResultPreserveKeys([]string{"id", "error"})` or the `config.ResultTrim.PreserveKeys` field (there's no env var); it's honored by every `StructuralTrimmer` the factory builds — the synthesis floor and the distiller's Stage-1 pre-filter, the resolution-source trim, the continuation distiller's pre-filter (§6.4), and agent-input trimming when that's enabled. It's a preference, **not a guarantee** — a tight enough budget can still drop a preferred field — so it raises the odds a critical field (an `id`, an `error`) survives rather than pinning it. Because it changes what the trimmer keeps (and so what the LLM sees), the preserve set is folded into the distill cache salt (§5.3).

Disable trimming with `TRUVAG3_RESULT_TRIM_ENABLED=false`. This turns off the **synthesis** path — structural trimming *and* the synthesis LLM distiller (which is gated on trimming) — but **not** the continuation-planning distiller, which is wired independently. To turn off *all* LLM compaction, use `TRUVAG3_RESULT_DISTILL_ENABLED=false` (the master switch: it gates both the synthesis distiller and the continuation distiller); `TRUVAG3_CONTINUATION_MAX_ESCALATIONS=0` disables only the continuation escalation.

---

## 5. Layer 2 — Result Distillation (LLM-first, the synthesis path)

Structural trimming preserves *shape*, but for the final answer you often want *meaning*: "of these 200 log lines, which ones matter for the user's question?" That's what distillation does — and it's the **primary compaction path for the synthesis prompt**.

### 5.1 How it works

Distillation is **On by default** (opt out with `TRUVAG3_RESULT_DISTILL_ENABLED=false`). When a step result exceeds `TRUVAG3_RESULT_DISTILL_THRESHOLD` (16 KB) and the orchestrator has an `AIClient`, it runs a two-stage pipeline:

1. **Stage 1 — structural pre-filter.** The `StructuralTrimmer` reduces the raw result to `PreFilterBudget` (128 KB) so the LLM isn't handed the entire payload.
2. **Stage 2 — LLM distill.** A **fast** model (Haiku 4.5 / GPT-5.6 Luna / Gemini Flash-Lite per provider, via the portable `fast` alias) summarizes the pre-filtered content to ~`TargetSize` (4 KB), *conditioned on the user's query and the step's task* so it keeps what's relevant.

> **Identifier fidelity.** The JSON round-trips on the **compaction and synthesis** path — the Stage-1 pre-filter, map-reduce chunking, and the final synthesis re-parse — decode numbers with full precision preserved (`UseNumber`), so large integer identifiers (snowflake IDs, 64-bit keys, anything above 2⁵³) survive **verbatim** rather than being silently rounded to a float or rewritten in scientific notation. The distill prompt also instructs the model to copy identifiers, keys, timestamps, and numeric values byte-for-byte; on evidence-style data (records, log lines, rows) it selects and copies matching units rather than paraphrasing them. (This covers what the *LLM* sees; the tool→tool template-resolution path — `{{step-N.field}}` binding — still decodes with standard JSON and can round a large number to a float, so don't rely on it to carry a 64-bit ID between tools.)

Without an `AIClient` (or with `TRUVAG3_RESULT_DISTILL_ENABLED=false`), distillation is skipped: the factory installs a plain `StructuralTrimmer` that runs at the **synthesis budgets** (16 KB per-result / 32 KB total) — *not* the 128 KB Stage-1 pre-filter, which exists only inside the distiller. You always get a safe, trimmed result, never a failure.

When several results share one synthesis prompt, the budget allocator is **distillation-aware**: a result that *will* be distilled is sized at its post-distill footprint (~`TargetSize`), not its raw bytes, so one huge result doesn't starve the others' budgets.

### 5.2 Very large results: map-reduce

A 128 KB pre-filter would throw away most of a 1 MB result before the LLM ever saw it — which is exactly where the important finding might be hiding. So when a result's estimated token count exceeds `TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS` (150000 tokens ≈ 525 KB), distillation switches to **map-reduce**:

1. Split the **full** result into chunks of ≤128 KB, preferring structural boundaries — top-level JSON array elements, then the dominant array inside a JSON object, then newline-delimited records — and falling back to a raw byte split (which may cut a record) only as a last resort. When the result is an **object wrapped around a dominant array** (e.g. `{"data":{"result":[…]},"status":…,"stats":…}`), the ordinary grouped chunks each replicate the wrapper and sibling fields, so that surrounding context reaches the model rather than being dropped with everything outside the array. An array element too big for one chunk is emitted as its own standalone chunk(s) — split recursively, never truncated — and the wrapper is still represented via the grouped chunks, or, when *every* element is oversized, via a separate wrapper chunk whose dominant array holds a sentinel marker (so an emptied array is never misread as "zero records"). If the wrapper is too large to replicate efficiently, the chunker falls back to newline splitting when the data has line boundaries, otherwise a raw byte split (surfaced as `chunk_strategy: lines` or `bytes`, §10).
2. Distill each chunk concurrently (up to `TRUVAG3_RESULT_DISTILL_MAP_CONCURRENCY`, default 8), on the fast tier.
3. Reduce the chunk extracts into one final summary.

This is what lets a multi-MB batch of logs surface a finding buried at byte ~500 K — a positional truncation would miss it entirely.

**Routing by bytes (opt-in).** There are **two** independent triggers into map-reduce: the token estimate above (`TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS`), and an optional **byte threshold**, `TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD`. The byte threshold is **`0` (disabled) by default** — out of the box, routing is context-only. Set it to a byte size and any result larger than that map-reduces *even when it comfortably fits the model context*, so the **whole** result is represented to an LLM instead of only its pre-filtered head. That's the knob for the 128 KB–525 KB band (see the caveat below). It's independent of `CONTEXT_TOKENS`; a value below `PreFilterBudget` (128 KB) is ignored with a factory warning, because a result that small is a single chunk — routing it to map-reduce adds no fan-out benefit over the ordinary single-call distill (that lone chunk still runs an LLM extract call when it fits the context).

Here's a real map-reduce distillation in the registry-viewer's execution diagram — a single oversized result fans out into concurrent `map-reduce chunk i/N` calls that reduce into one summary:

![Map-reduce distillation execution: an oversized result split into concurrent chunk distillations that reduce into a single summary](https://assets.truvag3.dev/images/truvag3-distill-map-reduce-execution.png)

> **Single-call caveat.** By default map-reduce engages only above the context threshold (~525 KB). A result in the **128 KB–525 KB** band takes the single-call path, where Stage 1 pre-filters to `PreFilterBudget` (128 KB) *before* the LLM — so the model sees the structural pre-filter, not the whole result. **That drop is disclosed:** the distilled output carries a `[partial source: the model received ~N% of the source by bytes …]` note and the trim metadata sets `content_lost` and `partial_coverage`, so a downstream reader is never told the sample is the whole. If you'd rather the *entire* result in that band reach the LLM, set `TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD` to a byte size (per "Routing by bytes" above) so it map-reduces regardless of the token estimate; alternatively lower `TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS` or raise `TRUVAG3_RESULT_DISTILL_PREFILTER`. (Results below 128 KB are pre-filtered without loss.)
>
> **Upper boundary.** Compaction targets results up to roughly the model's context window. **GB-class** outputs don't belong in a prompt at all — reduce them at the source (have the tool return less) or use an extract-then-reference workflow (artifact / code-exec backend). Don't try to compact a gigabyte in-prompt.

### 5.3 Caching and deadlines (both fail-open)

- **Cache (opt-in).** Caching is **off unless you supply a `core.DigestCache`** via `deps.DistillCache` — it's nil by default, so out of the box every distillation recomputes. When a cache *is* supplied, identical `(result + instruction + query + budget)` inputs (plus a config salt: prompt version, model, target size, pre-filter budget, model context tokens, the map-reduce byte threshold, any `PreserveKeys`, and any per-call `AIOptionsOverride` — so changing the routing knobs, the Stage-1 preserve set, or overriding the model / max tokens / temperature / system prompt all invalidate stale entries) reuse a prior distillation for `TRUVAG3_RESULT_DISTILL_CACHE_TTL` (5m), turning scheduled/repetitive runs into cache hits. For request-aware clients, the key also includes the stable, secret-free provider policy and route fingerprint, so rule, middleware, resolved-model, surface, or semantic-route changes cannot reuse an incompatible answer; an unstable fingerprint bypasses both cache lookup and storage, and after a miss the executed request report must match the preflight fingerprint before the result is written. On a hit, the processor restores the stored coverage metadata before returning the output (the cache holds a versioned envelope of the output plus its trim metadata), so an audited distillation reads the same whether freshly computed or cached; an invalid or unsupported envelope fails open by recomputing. Failed or partial outputs — a deadline cut **or** a map-reduce run where some chunk calls failed — are deliberately **not** cached, so a transient error can't be served for the whole TTL. A nil/erroring cache simply recomputes — never fails. (The continuation distiller has no cache wrapper — it fires rarely, so a cache would mostly miss.)
- **Deadline** — each compaction is bounded by `TRUVAG3_RESULT_DISTILL_DEADLINE` (45s). On timeout the single-call path falls back to the structural floor; the map-reduce path returns the chunks that finished plus an honest "partial" disclosure. (That same `[partial: …]` disclosure also covers chunks lost to a *failed* model call, not only the deadline — see §5.6.) Keep this under your HTTP gateway timeout.

### 5.4 Scope

The **synthesis result distiller** is scoped to the synthesis path. Parameter resolution (micro-resolution, semantic retry) uses the deterministic structural trimmer for its source data — those prompts drive their own resolution LLM call and must not be lossily summarized first. Continuation planning uses digests (next section); it has its own **separate** distiller, invoked only for the rare non-JSON escalation (§6.4) — never for the synthesis prompt.

### 5.5 Every distillation knob

| Knob | Default | Env Var | What it controls |
|---|---|---|---|
| Enabled | `true` | `TRUVAG3_RESULT_DISTILL_ENABLED` | master switch; `=false` falls back to the structural floor |
| Trigger threshold | 16 KB | `TRUVAG3_RESULT_DISTILL_THRESHOLD` | minimum result size to invoke the LLM (below it → structural trim to the allocated budget, not the LLM; raw only if it already fits) |
| Pre-filter budget | 128 KB | `TRUVAG3_RESULT_DISTILL_PREFILTER` | Stage-1 structural cap before the LLM (sized to fit the fast-tier context) |
| Target output | 4 KB | `TRUVAG3_RESULT_DISTILL_TARGET` | approximate size of the distilled summary |
| Model | `fast` | `TRUVAG3_RESULT_DISTILL_MODEL` | model/alias for distill calls (see the alias note below) |
| Cache TTL | 5m | `TRUVAG3_RESULT_DISTILL_CACHE_TTL` | how long an identical distillation is reused (**only when a `deps.DistillCache` is supplied** — no caching by default) |
| Compaction deadline | 45s | `TRUVAG3_RESULT_DISTILL_DEADLINE` | wall-clock bound per compaction (fail-open on timeout) |
| Map-reduce trigger (tokens) | 150000 tokens | `TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS` | estimated token count above which a result is chunked + map-reduced |
| Map-reduce trigger (bytes) | `0` (disabled) | `TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD` | byte size above which a result map-reduces *regardless* of the token estimate (whole result → LLM, not just the pre-filtered head); `0` keeps routing context-only; a value below `PREFILTER` (128 KB) is ignored with a warning |
| Map concurrency | 8 | `TRUVAG3_RESULT_DISTILL_MAP_CONCURRENCY` | parallel chunk distillations in the map-reduce path |

> **Use a portable model alias, not a concrete name.** With a `ChainClient` (multi-provider failover), set `TRUVAG3_RESULT_DISTILL_MODEL` to `fast` / `default` / `smart` — a concrete model name is provider-specific and breaks failover (the chain classifies a 404 as a non-retryable client error and stops instead of trying the next provider). The `fast` alias resolves to each provider's efficient tier (Haiku 4.5 / GPT-5.6 Luna / Gemini Flash-Lite). Per-call AI options (model, max tokens, temperature, system prompt) can be overridden programmatically with `WithResultDistillAIOptions(...)`.

### 5.6 Disclosure notes: how a compacted result flags what it dropped

Compaction is **honest about loss.** Whenever any layer *deterministically* drops content, it appends a single-line note to the result and sets `content_lost` in the trim metadata (see [`ResultTrimMetadata`](../reference/API_REFERENCE.md#resulttrimmetadata)). Each note starts on its own line with a `[` prefix (so it's machine-parseable and can be stripped) and tells the downstream model to treat omitted content as **UNKNOWN, not absent** — the invariant that keeps lossy compaction safe: the model is never allowed to read a trimmed result as complete. You may see any of these in a step result or the synthesis prompt:

| Note (prefix) | Emitted by | What it means |
|---|---|---|
| `[trimmed: …]` | Structural trim (Layer 1) | Whole fields, array items, or sentences were dropped to fit the budget — `[trimmed: N/M … kept …]` with exact unit counts. A plain byte truncation instead reads `[trimmed: original → kept bytes …]`. |
| `[severely reduced: kept N of ~M bytes (P%) …]` | Structural trim (Layer 1) | The trim kept **under 5%** of the source (the `degenerateKeptRatio` floor) — too little to be representative. Metadata sets `degenerate=true` with `kept_ratio`. |
| `[partial source: the model received ~N% of the source by bytes …]` | Distillation (Layer 2) | A single-call distill's Stage-1 **pre-filter** dropped content before the LLM (the 128 KB–525 KB band, §5.2), so the model saw only part of the source. Metadata sets `partial_coverage=true`. |
| `[partial: N of M segments analyzed …]` | Map-reduce (Layer 2) | Only `N` of `M` chunks completed — the `CompactionDeadline` was hit, or a chunk's model call failed and was skipped. The unanalyzed segments are UNKNOWN. |
| `[findings truncated: …]` | Map-reduce *reduce* (Layer 2) | The combined extracts (from the segments that completed) were truncated to fit the budget. This does **not** imply every segment was analyzed — a `[partial: …]` note can accompany it, so check `chunks_completed`/`chunks_total`. The `combine_truncated_reason` span attribute says which: `reduce_failed` (a transient reduce-call failure) or `over_context` (too large to reduce). |
| `[reduced without model analysis: …]` | Synthesis floor | No result processor was configured (e.g. a fallback seam), so the prompt byte-truncated the result and **no LLM analyzed** the dropped tail. |

> **One note is advisory, not framework-guaranteed.** The distill system prompt asks the model to end an over-budget distillation with `[truncated: N additional matching units omitted to fit budget …]`. Unlike the six framework notes above — which are appended deterministically and are recognized by the framework's annotation stripper — this model-emitted `[truncated: …]` form is best-effort (it depends on the model obeying the instruction) and is **deliberately not** registered with the stripper: real tools emit their own `[truncated: …]` trailers, and peeling them would silently delete a *tool's* own truncation signal. When Stage-1 actually drops content, the authoritative, framework-guaranteed signal is the `[partial source: …]` note, not the model's `[truncated: …]`.

---

## 6. Layer 3 — Continuation Digests (the planning path)

Between phases, the continuation planner re-reads completed steps to decide what's next. It doesn't need the *values* in those results — it needs their *structure*, so it can reference fields and avoid re-issuing work. So instead of distilling (an LLM call per step) or truncating (which mangles JSON), completed-step results are rendered as **structure-complete JSON digests**.

### 6.1 What a digest looks like

A digest is a skeleton of the result that's **always valid JSON**:

- **All object keys are kept** (up to `TRUVAG3_CONTINUATION_DIGEST_MAX_KEYS`, default 50; wider "map-shaped" objects are sampled to N sorted keys + a sentinel).
- **Arrays are head-sampled** to `TRUVAG3_CONTINUATION_DIGEST_ARRAY_SAMPLE` (default 3) elements + a length sentinel (`"…197 more of 200"`).
- **Long strings are elided** to a descriptor (`"…(16384 chars)"`) above `TRUVAG3_CONTINUATION_DIGEST_SCALAR_MAX` (default 200).

A 519 KB Loki result becomes a ~600-byte skeleton that still tells the planner: there's a `data.streams` array of 200 entries, each with `line` and `labels` fields. That's everything the planner needs.

### 6.2 Addressability — the key idea

Digests are **lossy on values but complete on structure**, and that's deliberate: the planner references fields with `{{step-N.fieldpath}}` templates, and **the full value resolves from memory at execution time** — not from the digest. So a plan can say `{{step-9.response.data.streams}}` and the executor binds the *entire* 200-entry array at run time, even though the planner only ever saw the skeleton.

### 6.3 The budget: greedy recency-fill

`TRUVAG3_CONTINUATION_RESULT_MAX_TOTAL_CHARS` (32 KB) is a **target** that drives eviction — not a hard cap. The fill rule:

- **Failed steps are always kept** — they carry a lot of signal but take little space (they're short `[FAILED: …]` markers). They count against the budget but are never evicted.
- **Successful steps fill newest-first** until the budget is spent; older ones are evicted with a `[showing N of M completed steps]` note (they remain referenceable by step-ID at execution).
- **The newest successful step is always kept**, even if it alone exceeds the budget, so the section is never empty.
- Steps render in **chronological order**, so the newest sits at the end of the prompt, where models pay the most attention.

Because failed steps and the newest successful step are force-kept — and per-step orchestrator NOTEs plus the `[showing N of M …]` marker (always emitted, even when N = M) are appended *after* the budget decision — the rendered section can modestly exceed the target on large or failure-heavy fan-outs.

### 6.4 Non-JSON results and the escalation path

Not every result is JSON (logs, markdown, CSV). A non-JSON result can't be digested, so it gets a **floor preview** sized by `TRUVAG3_CONTINUATION_RESULT_MAX_CHARS` (10000 chars). A capped number of the newest non-JSON steps (`TRUVAG3_CONTINUATION_MAX_ESCALATIONS`, default 8; `0` disables) may **escalate** to the continuation distiller for a fast-model summary that *replaces* the floor preview. This rarely triggers on all-JSON workloads, and is always fail-open (an empty summary leaves the floor in place).

### 6.5 Every continuation-digest knob

| Knob | Default | Env Var | What it controls |
|---|---|---|---|
| Aggregate budget | 32 KB | `TRUVAG3_CONTINUATION_RESULT_MAX_TOTAL_CHARS` | **target** chars for the `<completed_steps>` section — drives N-of-M eviction (a soft target, not a hard cap: failed steps and the newest successful step are always kept) |
| Non-JSON floor | 10000 chars | `TRUVAG3_CONTINUATION_RESULT_MAX_CHARS` | floor-preview size for a non-JSON step (JSON steps are digested, not floored) |
| Max escalations | 8 (`0` disables) | `TRUVAG3_CONTINUATION_MAX_ESCALATIONS` | newest non-JSON steps that may escalate to the distiller per phase |
| Array sample | 3 | `TRUVAG3_CONTINUATION_DIGEST_ARRAY_SAMPLE` | head-sample size for arrays in a digest |
| Scalar elision cap | 200 | `TRUVAG3_CONTINUATION_DIGEST_SCALAR_MAX` | max inline string length before it's elided to a `…(N chars)` descriptor |
| Object key cap | 50 | `TRUVAG3_CONTINUATION_DIGEST_MAX_KEYS` | max keys kept per object before sampling to sorted keys + a sentinel |

> These are all top-level `OrchestratorConfig` fields (env-tunable; no `With*` option) — they're numeric tuning, so they live as env vars.

---

## 7. How a Result Flows Through

Putting it together, here's the decision a result goes through:

```
result produced by a step
│
├─ used as INPUT to another tool ───────────────▶ pass through raw (fidelity-first; cap only if MaxAgentInputBytes > 0)
│
├─ used as SOURCE for param resolution ─────────▶ StructuralTrimmer to MaxMicroResolutionBytes (deterministic, never the LLM distiller)
│
├─ headed to the SYNTHESIS prompt ──────────────▶ ≤ threshold? structural trim to the budget (raw only if it already fits)
│                                                  > threshold + AIClient? distill (fast model)
│                                                      └─ > model context? chunk → map-reduce
│                                                  no AIClient? StructuralTrimmer floor
│
└─ headed to the CONTINUATION prompt ───────────▶ valid JSON? structure-complete digest
                                                   non-JSON? floor preview (+ rare fast-model escalation)
                                                   whole section targeted by aggregate budget — soft, not a hard cap (newest-first, N-of-M eviction; failed + newest always kept)
```

---

## 8. Configuration

### 8.1 Environment variables (numeric tuning)

All budgets, thresholds, and models are env-tunable — deploy-time changes, no rebuild. The three families:

- `TRUVAG3_RESULT_TRIM_*` — structural trimming (Layer 1)
- `TRUVAG3_RESULT_DISTILL_*` — distillation (Layer 2)
- `TRUVAG3_CONTINUATION_*` — continuation digests (Layer 3)

Full tables with every variable, default, and description: [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md) and [Limits Cheatsheet](../reference/LIMITS_CHEATSHEET.md).

**Precedence** (highest wins): a programmatic `With*` option or explicit config field → the `TRUVAG3_*` variable → the built-in default. (These compaction knobs have no "standard" env-var aliases — only the `TRUVAG3_*` names are parsed into `DefaultConfig`. Standard vars like `OPENAI_API_KEY` / `REDIS_URL` configure other subsystems, not these budgets.)

**Parsing semantics.** Byte budgets are plain integers *in bytes* (`16 KB` = `16384`); durations (`CACHE_TTL`, `DEADLINE`) are Go duration strings (`5m`, `45s`). For numeric budgets, an empty or non-positive value is ignored and the default stands — a typo can't silently zero a budget. Three numerics instead treat `0` as a deliberate, accepted value (not "ignore, use default"): `TRUVAG3_CONTINUATION_MAX_ESCALATIONS` (`0` disables continuation escalation), `TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD` (`0` disables schema-guided mapping), and `TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD` (`0` = disabled, which is also its default — parsed with `>= 0` so an explicit `0` pins it off against a future nonzero default). **Booleans are stricter:** `TRUVAG3_RESULT_TRIM_ENABLED` and `TRUVAG3_RESULT_DISTILL_ENABLED` count as enabled only for the literal `true` (case-insensitive) — any *other* non-empty value (`1`, `yes`, `on`) parses as `false`, so always set them to exactly `true` or `false`. (The compaction deadline and cache TTL can be set to `0` to disable only programmatically; an env value of `0` is ignored and the default stands.)

### 8.2 Programmatic options

```go
config := orchestration.DefaultConfig()

// Trimming
config.ResultTrim.MaxResultBytes = 32768          // or WithResultTrimming(true, 32768)
config.ResultTrim.PreserveKeys = []string{"id"}   // or WithResultPreserveKeys([]string{"id"})

// Distillation (default-on; mutate individual fields, don't reassign the whole struct —
// a wholesale literal would zero CacheTTL / CompactionDeadline / ModelContextTokens)
config.ResultDistill.DistillThreshold = 65536     // or WithResultDistill(true, 65536)
config.ResultDistill.Model = "fast"               // or WithResultDistillModel("fast")
// Per-distill-call AI options (model / max tokens / temperature / system prompt):
//   WithResultDistillAIOptions(&orchestration.AIOptionsOverride{Model: orchestration.StringPtr("fast")})
// config.ResultDistill.Enabled = false           // opt out entirely

// Continuation digests are top-level OrchestratorConfig fields (env-tunable; no With* option)
config.ContinuationDigestScalarMax = 400          // surface longer salient values at plan time
```

### 8.3 Dependency-injection seams (behavioral plugs)

Numeric tuning belongs in env vars; *behavioral* replacement belongs in `OrchestratorDependencies`. Each seam is honored if set, otherwise the factory builds the default:

| Seam | Replaces | Used for |
|---|---|---|
| `deps.ResultProcessor` | the synthesis processor | a custom (possibly LLM) compactor for the final-answer prompt |
| `deps.SourceResultProcessor` | the resolution-source trimmer | a custom deterministic processor for micro-resolution source data |
| `deps.AgentInputProcessor` | the tool→tool transform | redact / validate / enrich / trim parameters before dispatch |
| `deps.ContinuationDistiller` | the continuation distiller | a custom summarizer for non-JSON continuation escalations |
| `deps.DistillCache` | (none — nil disables) | a `core.DigestCache` (e.g. Redis-backed) so distillations are cached across pods |

---

## 9. Customizing and Extending

The compaction contract is the single-method `ResultProcessor` interface:

```go
type ResultProcessor interface {
    ProcessForPrompt(ctx context.Context, result string, maxBytes int, stepContext ResultProcessorContext) string
}
```

> **Honesty contract for custom processors.** If your processor drops, truncates, or samples content, report it by calling the exported `CaptureResultTrimMetadata(ctx, ResultTrimMetadata{... ContentLost: true})` from inside `ProcessForPrompt`. The framework prepares the per-step metadata slot before invoking you, so this surfaces on the step's `Metadata["result_trim"]` and the `result_trim.completed` span, and — crucially — feeds the same disclosure gating the built-in processors use (§5.6). Downstream honesty keys **only** on `ContentLost`; a lossy processor that skips this call makes real loss read as full coverage with no disclosure. The call is a safe no-op outside the framework's capture context.

Swap in your own implementation through the relevant `deps.*` seam. Common reasons:

- **Domain-specific compaction** — you know your payloads (e.g. keep the `error` and `stack` fields verbatim, drop everything else).
- **Redaction** — strip secrets/PII from tool→tool flows via a custom `AgentInputProcessor` (fail-closed if redaction fails).
- **Shared cache** — pass a Redis-backed `core.DigestCache` as `deps.DistillCache` so scheduled/repetitive runs share distillation results across replicas.

The framework stays domain-agnostic: the defaults preserve structure generically and never hardcode field semantics. Your overrides are where domain knowledge lives.

**Wiring the stack outside `CreateOrchestrator`.** If you build a processor by hand (e.g. in a custom runner), `BuildDistillationEnabledResultProcessor(cfg, aiClient, cache, logger)` constructs the same Layer-2 stack the factory does — a `StructuralTrimmer` (pre-filter + floor) wrapped by the `LLMDistiller`, wrapped by a fail-open cache. A `nil` AI client yields the bare structural floor; a `nil` cache disables caching. (For a fully custom backend, pass it via `deps.ResultProcessor` instead.)

---

## 10. Observability

Compaction is visible in all three telemetry channels:

- **Traces** — span events on the distillation path, all correlated by `request_id`/`step_id`:
  - `result_distill.mapreduce_route` — emitted when a result is routed to map-reduce, with `reason` (`context` = over the token estimate, or `threshold` = over `MapReduceThresholdBytes`), `original_bytes`, and `estimated_tokens`.
  - `result_distill.stage1_complete` — pre-filter done (`original_bytes` → pre-filtered size).
  - `result_distill.stage2_complete` — the LLM distill returned (`distilled_bytes`, token usage).
  - `result_distill.mapreduce_complete` — a map-reduce finished, with `chunks_total`, `chunks_completed`, `combined_bytes`, `llm_input_bytes`, `chunk_strategy` (the **worst** boundary used across the top-level split and any recursive sub-splits: `wrapper`/`array` = clean structural boundaries; `lines`/`bytes` = degraded fallbacks where a record may be split mid-JSON), and — only when the reduce truncated — `combine_truncated_reason` (`reduce_failed`/`over_context`).
  - `result_distill.cache_hit` — a supplied `DigestCache` served a prior distillation (`cached_bytes`); a miss recomputes silently.
  - `result_distill.llm_failed` — a distill call failed and fell open to the structural floor (`error`, `duration_ms`).

  On the planning path, `orchestrator.continuation_budget` (with `steps_total`, `steps_shown`, `c_escalations`). Search by `request_id` in Jaeger (see [Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md)).
- **Metrics** — `orchestration.result_distill.{triggered,duration_ms,failed,mapreduce,cache_hit,cache_miss}` (labeled by `agent_name`) on the distillation path, and `orchestration.continuation.{section_chars,steps_shown,steps_evicted,c_escalations}` on the planning path (Histograms/Counters, labeled `module=orchestration`) for dashboards and alerts on distill volume/failures, cache hit-rate, eviction pressure, and escalation rate.
- **Trim metadata** — a processed step carries a [`ResultTrimMetadata`](../reference/API_REFERENCE.md#resulttrimmetadata) record on `StepResult.Metadata["result_trim"]` with the authoritative `content_lost` flag plus the coverage fields (`source_coverage_ratio`, `llm_input_bytes`, `segments_analyzed`/`segments_total`, `partial_coverage`, `combine_truncated`, `chunk_strategy`). Whenever a trim changed the result (content lost **or** the serialized size changed) the loss/coverage bits are also mirrored onto a `result_trim.completed` span (`content_lost` — always — plus `source_coverage_ratio`, `llm_input_bytes`, `segments_*`, `partial_coverage`, `combine_truncated`; `chunk_strategy` stays on the record and the `mapreduce_complete` span) so Jaeger can tell a 28%-seen distill from a 100%-seen one. Detect loss programmatically from `content_lost` — never by comparing byte counts (§5.6).
- **LLM-debug** — **when LLM-debug is enabled** (`TRUVAG3_LLM_DEBUG_ENABLED=true` or `WithLLMDebug(true)` — it's off by default), every distillation LLM call is recorded as a `result_distillation` interaction (with `call_description` like `map-reduce chunk 4/7`), viewable in the registry-viewer's LLM-debug tab alongside planning/synthesis calls. Without a debug store the recording is a no-op — the distillation itself still runs.

---

## 11. Cost and Latency

Distillation makes LLM calls, so it has a cost — but the design keeps it cheap:

- **Cheap tier.** Distillation runs on the `fast` model — several times cheaper per token than the reasoning tier (≈3× on the default Sonnet→Haiku stack, more when the reasoning model is a frontier tier). A run that distills several MB of results does the bulk of the reading on the cheap tier, keeping the expensive model for actual reasoning.
- **Map-reduce is concurrent.** Chunks distill in parallel up to `MapConcurrency` (default 8). When all chunks fit in one wave, wall-clock is the slowest chunk + reduce; with more chunks than `MapConcurrency` they run in successive waves — all bounded by the shared `CompactionDeadline`, not summed.
- **Caching (when enabled).** If you supply a `deps.DistillCache`, repeated/scheduled runs hit the distillation cache instead of re-calling (there's no cache by default — see §5.3).
- **It runs only when needed.** Results under the threshold skip the LLM entirely (a cheap structural pass, or raw if they already fit the budget); continuation planning uses digests (no LLM) and rarely escalates to the distiller on JSON workloads.

If you're cost-sensitive: lower `TRUVAG3_RESULT_DISTILL_TARGET` (smaller summaries), raise `TRUVAG3_RESULT_DISTILL_THRESHOLD` (distill less often), point `TRUVAG3_RESULT_DISTILL_MODEL` at an even cheaper alias, or opt out with `TRUVAG3_RESULT_DISTILL_ENABLED=false` (you'll fall back to the structural floor).

---

## 12. Tuning Scenarios

**"Results are getting summarized too aggressively; I want more detail in the final answer."**
```bash
export TRUVAG3_RESULT_DISTILL_TARGET=8192      # larger summaries
export TRUVAG3_RESULT_DISTILL_THRESHOLD=65536  # distill only the really big ones
```

**"The planner isn't seeing a value it needs to reference at plan time."**
```bash
export TRUVAG3_CONTINUATION_DIGEST_SCALAR_MAX=1000   # keep longer strings inline in digests
```
(Remember: the planner references *fields*, and full values resolve at execution — you usually need this only when the planning *decision* depends on a value's content.)

**"Cost-sensitive / latency-sensitive deployment."**
```bash
export TRUVAG3_RESULT_DISTILL_ENABLED=false    # structural floor only, no LLM distill calls
```

**"Huge results are timing out the synchronous request."**
```bash
export TRUVAG3_RESULT_DISTILL_DEADLINE=30s     # fall back to floor/partial sooner (keep under the gateway timeout)
```

**"Continuation prompts evicting too many steps on big fan-outs."**
```bash
export TRUVAG3_CONTINUATION_RESULT_MAX_TOTAL_CHARS=65536   # larger aggregate budget
```

---

## 13. Troubleshooting

**Synthesis distillation isn't running.** The *synthesis* distiller requires (a) `TRUVAG3_RESULT_DISTILL_ENABLED=true` (default), (b) `TRUVAG3_RESULT_TRIM_ENABLED=true` (default — the synthesis distiller is gated on trimming), and (c) an `AIClient` on the orchestrator. Without an AIClient you get the structural floor by design. (The *continuation* distiller is wired independently of `RESULT_TRIM_ENABLED` — it needs only `RESULT_DISTILL_ENABLED=true`, an `AIClient`, and `CONTINUATION_MAX_ESCALATIONS > 0`.)

**I see a "partial"/"trimmed"/"reduced" note in a result.** Those are honest disclosure notes, not errors — §5.6 catalogs every one and what it means. Two of them signal an *incomplete* compaction (versus an ordinary budget trim), both on the map-reduce path:

- `[partial: N of M segments analyzed …]` — not every chunk completed: the `CompactionDeadline` was hit (it returned the chunks that finished), or a chunk's fast-model call failed and was skipped.
- `[findings truncated: …]` — the combined extracts (from the segments that completed) were truncated to fit the budget; it does **not** by itself mean every segment was analyzed (a `[partial: …]` note may accompany it).

Diagnose from the `result_distill.mapreduce_complete` span: compare `chunks_completed` vs `chunks_total`, and read `combine_truncated_reason` — `reduce_failed` (a transient reduce-call failure; the result isn't cached, so a later compaction recomputes — the reduce is not retried within this one) vs `over_context` (the extracts were too big to reduce). The LLM-debug view shows any failed `map-reduce chunk i/N` calls. **Remedy:** for a deadline cause, raise `TRUVAG3_RESULT_DISTILL_DEADLINE` (keep it under your gateway timeout); for `over_context`, lower `TRUVAG3_RESULT_DISTILL_TARGET` so the extracts reduce, or shrink the data at the source. A `[partial source: ~N% …]` note is the *expected* disclosure that a **single-call** distill pre-filtered the source (§5.2) — if you want the whole result in that band analyzed, route it through map-reduce with `TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD`.

**Continuation prompt shows `[showing N of M completed steps]` with N < M.** Steps were evicted for budget — expected on large fan-outs. The evicted steps are still referenceable by step-ID at execution; raise `TRUVAG3_CONTINUATION_RESULT_MAX_TOTAL_CHARS` if you want more shown.

**A value got elided to `"…(N chars)"` in a continuation digest.** That's the scalar-elision sentinel — by design, since the planner addresses fields, not values. If a planning decision truly needs the content, raise `TRUVAG3_CONTINUATION_DIGEST_SCALAR_MAX`.

**Distillation cost is higher than expected.** Check the LLM-debug view for `result_distillation` interactions: many `map-reduce chunk i/N` calls mean a few very large results were chunked (each chunk is one fast-model call). That's expected for multi-MB results; lower `TRUVAG3_RESULT_DISTILL_TARGET` or raise the threshold to reduce volume.

**A result carries a `[severely reduced … treat as UNKNOWN]` note.** A deterministic structural trim kept under 5% of the source (the `degenerateKeptRatio` floor), so the synthesizer is told explicitly not to treat anything *not* shown as absent. It's an honesty marker, not an error — it appears on the floor path (no `AIClient`, or a fallback after a failed/timed-out distill), and the trim metadata carries `degenerate=true` with the `kept_ratio`. If it shows up often, the source dwarfs the budget: raise the per-result/total budget, or let distillation handle it (LLM-selected output is exempt from the marker).

---

## 14. Reference

- [API Reference — `ResultTrimConfig`](../reference/API_REFERENCE.md#resulttrimconfig) and [`ResultDistillConfig`](../reference/API_REFERENCE.md#resultdistillconfig) — full struct fields and `With*` options
- [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md) — every `TRUVAG3_RESULT_*` and `TRUVAG3_CONTINUATION_*` variable
- [Limits Cheatsheet](../reference/LIMITS_CHEATSHEET.md#result-trimming-large-data) — defaults at a glance + migration notes
- [Effective Prompts Guide §2.2](../building/EFFECTIVE_PROMPTS_GUIDE.md#22-stay-under-the-bloat-threshold-3000-tokens) — the research behind keeping prompts lean
- [Framework Features Guide — Result Trimming And Distillation](../overview/FRAMEWORK_FEATURES_GUIDE.md#result-trimming-and-distillation)

> **Not to be confused with…** Result compaction handles large **tool/step results**. The adjacent — and separate — context-management layer is [Conversation History Protection](../memory-and-chat/CONVERSATION_HISTORY_GUIDE.md), which compacts long **chat histories** across turns. Different inputs, different knobs (`TRUVAG3_CONVERSATION_*`); they compose without overlapping.
