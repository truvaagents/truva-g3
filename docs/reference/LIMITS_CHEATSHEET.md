# TruvaG3 Limits & Configuration Cheatsheet

Quick reference for all configurable limits, thresholds, and budgets. Every value below can be overridden via environment variable or programmatic config.

**Precedence:** Functional option > Environment variable > `DefaultConfig()` default

> For full details on environment variables see [ENVIRONMENT_VARIABLES_GUIDE.md](ENVIRONMENT_VARIABLES_GUIDE.md).
> For programmatic configuration and `With*` options see [API_REFERENCE.md](API_REFERENCE.md).

---

## LLM Token Limits

| What | Default | Env Var | Programmatic Override |
|------|---------|---------|----------------|
| Plan generation output | 15000 | `TRUVAG3_PLAN_MAX_TOKENS` | `WithPlanAIOptions(&AIOptionsOverride{MaxTokens: IntPtr(n)})` |
| Synthesis output | 5000 | `TRUVAG3_SYNTHESIS_MAX_TOKENS` | `WithSynthesisAIOptions(&AIOptionsOverride{MaxTokens: IntPtr(n)})` |
| Synthesis temperature | 0.5 | `TRUVAG3_SYNTHESIS_TEMPERATURE` | `WithSynthesisAIOptions(&AIOptionsOverride{Temperature: Float32Ptr(t)})` |
| Plan model override | — | `TRUVAG3_PLAN_MODEL` | `WithPlanAIOptions(&AIOptionsOverride{Model: StringPtr(m)})` |
| Synthesis model override | — | `TRUVAG3_SYNTHESIS_MODEL` | `WithSynthesisAIOptions(&AIOptionsOverride{Model: StringPtr(m)})` |
| Micro-resolution & semantic-retry model override | — | `TRUVAG3_MICRO_RESOLUTION_MODEL` | `WithMicroResolutionAIOptions(&AIOptionsOverride{Model: StringPtr(m)})` |
| Micro-resolution & semantic-retry output | 2000 | `TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS` | `WithMicroResolutionAIOptions(&AIOptionsOverride{MaxTokens: IntPtr(n)})` |
| Tiered selection output | 2000 | `TRUVAG3_TIERED_SELECTION_MAX_TOKENS` | `WithTieredSelectionMaxTokens(n)` |
| Core AI max tokens | 2000 | `TRUVAG3_AI_MAX_TOKENS` | — |

> The micro-resolution token/model knobs are shared: `TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS` and `TRUVAG3_MICRO_RESOLUTION_MODEL` govern **both** micro-resolution **and** Layer 4 semantic retry (contextual re-resolution). There is no separate `TRUVAG3_SEMANTIC_RETRY_MAX_TOKENS` — raise the micro-resolution token limit if a semantic-retry response is being truncated (e.g. when the corrected parameters contain a large text payload). `TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS` (see Retry Budgets) controls retry *count*, not output tokens.

## Conversation History Preparation

| What | Default | Env Var | Programmatic Override |
|------|---------|---------|----------------|
| Prepared conversation-history budget | 48000 | `TRUVAG3_CONVERSATION_TOKEN_BUDGET` | `OrchestratorConfig.ConversationTokenBudget` or `ConversationHistoryProcessorConfig.TokenBudget` |
| Recent turns preserved verbatim | 4 | `TRUVAG3_CONVERSATION_RECENT_TURNS_PRESERVED` | `OrchestratorConfig.ConversationRecentTurnsPreserved` or `ConversationHistoryProcessorConfig.RecentTurnsPreserved` |
| Summary cache size (Tier 2 only) | 256 | `TRUVAG3_CONVERSATION_SUMMARY_CACHE_SIZE` | `OrchestratorConfig.ConversationSummaryCacheSize` and `NewSummaryCache(n)` |

> Tier 1 conversation-history protection is default-on for metadata and hook ingress paths.
> Tier 2 recursive compaction is opt-in and only active when the application injects a preparer configured with both a `SummaryCache` and a `ConversationCompactor`.

## Iterative Planning (Multi-Phase DAG)

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Enabled | true | `TRUVAG3_ITERATIVE_PLANNING_ENABLED` | — |
| Max phases per request | 5 | `TRUVAG3_ITERATIVE_MAX_PHASES` | — |
| Max total steps (all phases) | 200 | `TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS` | — |
| Phase timeout | 180s | `TRUVAG3_ITERATIVE_PHASE_TIMEOUT` | — |
| Max validation rounds | 4 | `TRUVAG3_ITERATIVE_MAX_VALIDATION_ROUNDS` | Regen attempts after first validation failure; phase fails if exceeded |

## Execution Limits

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Max parallel steps | 25 | `TRUVAG3_EXECUTION_MAX_CONCURRENCY` | `WithMaxConcurrency(n)` |
| Step timeout | 120s | `TRUVAG3_EXECUTION_STEP_TIMEOUT` | `WithStepTimeout(d)` |
| Total execution timeout | 600s | `TRUVAG3_ORCHESTRATION_TIMEOUT` | `WithTotalTimeout(d)` |

## Scheduled Executor

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Worker count | 5 | `TRUVAG3_EXECUTOR_WORKER_COUNT` | — |
| Max dispatch retries | 3 | `TRUVAG3_EXECUTOR_MAX_RETRIES` | — |
| Retry base delay | 5s | `TRUVAG3_EXECUTOR_RETRY_BASE_DELAY` | — |
| Retry max delay | 60s | `TRUVAG3_EXECUTOR_RETRY_MAX_DELAY` | — |
| Dispatch timeout | 15m | `TRUVAG3_EXECUTOR_DISPATCH_TIMEOUT` | — |

> The dispatch timeout is the HTTP client timeout for the synchronous POST to the target agent. It must be ≥ the target agent's `TRUVAG3_ORCHESTRATION_TIMEOUT`, otherwise the executor cancels the request before multi-phase orchestration completes.

## Continuation Prompt Limits

> **Phase 14:** completed-step results in continuation planning prompts are rendered as **structure-complete JSON digests** (skeletons) under a per-section aggregate budget. Non-JSON blobs get a floor preview; a few may escalate to a fast-model summary. All knobs are top-level `OrchestratorConfig` fields (no `With*` option), env-tunable.

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Non-JSON floor preview (per-step) | 10000 (~2500 tokens) | `TRUVAG3_CONTINUATION_RESULT_MAX_CHARS` | — |
| Aggregate digest budget (whole `<completed_steps>`) | 32768 (32 KB) | `TRUVAG3_CONTINUATION_RESULT_MAX_TOTAL_CHARS` | — |
| Max non-JSON → distiller escalations / phase | 8 (`0` disables) | `TRUVAG3_CONTINUATION_MAX_ESCALATIONS` | — |
| Digest array head-sample size | 3 | `TRUVAG3_CONTINUATION_DIGEST_ARRAY_SAMPLE` | — |
| Digest inline-string cap (elision threshold) | 200 | `TRUVAG3_CONTINUATION_DIGEST_SCALAR_MAX` | — |
| Digest per-object key cap | 50 | `TRUVAG3_CONTINUATION_DIGEST_MAX_KEYS` | — |

> `…_RESULT_MAX_CHARS` now sizes only the **non-JSON** floor preview (logs/markdown/CSV) — JSON results are digested, and orchestrator JSON delegation responses are additionally child-summary-extracted, so child sub-steps stay visible regardless. `…_MAX_TOTAL_CHARS` bounds the whole section: digests fill newest-first; older steps evict with a "showing N of M" note (~268 B/digest measured → ~122 steps fit 32 KB). `…_MAX_ESCALATIONS` calls are newest-first, sequential fast-model calls and fire ~never on all-JSON workloads. Raise `…_DIGEST_SCALAR_MAX` so the planner sees longer salient values inline at plan time.

> **Migration (upgrading from pre-Phase-14):** `TRUVAG3_CONTINUATION_RESULT_MAX_CHARS` previously truncated **every** completed-step result (raw, per-step). It now governs **only the non-JSON floor preview** — valid-JSON steps are digested and no longer honor it. The env var name and its default (`10000`) are unchanged, so **no action is required**; JSON steps simply render as structure-complete digests instead of raw 10 KB truncations. If a deployment relied on the old raw-truncation behavior for JSON results, there is no equivalent toggle — the digest path supersedes it.

## Remediation Failure-Pattern Analyzer

Controls when and how a shared-error summary is embedded into the remediation continuation prompt that fires after template-induced skips.

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Min causal failures to emit pattern | 2 | `TRUVAG3_FAILURE_PATTERN_MIN_FAILURES` | — |
| Error-signature length (classification) | 120 | `TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN` | — |
| Error-signature length (display in prompt) | 80 | `TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN` | — |

> The summary is emitted only when at least `MIN_FAILURES` distinct failed upstream steps share the same error signature. Classification length is intentionally wider than display length so two genuinely-different errors that share a common prefix are less likely to collide into the same bucket. The display length applies after classification — the rendered error is truncated with a trailing `…`.

## Result Trimming (Large Data)

> **Note:** All env vars below expect values in **bytes** (integers), not KB/MB strings.
> For example, 16 KB = `16384`, 32 KB = `32768`, 128 KB = `131072`.

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Enabled | true | `TRUVAG3_RESULT_TRIM_ENABLED` | `WithResultTrimming(bool, n)` |
| Per-result max | 16384 (16 KB) | `TRUVAG3_RESULT_TRIM_MAX_BYTES` | `WithResultTrimming(bool, n)` |
| Total synthesis results | 32768 (32 KB) | `TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES` | — |
| Micro-resolution source | 65536 (64 KB) | `TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES` | — |
| Agent input per-param | 0 (disabled) | `TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES` | — |
| Schema mapping threshold | 16384 (16 KB) | `TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD` | — |
| Preserve keys | — | — | `WithResultPreserveKeys(keys)` |

> Agent input per-param defaults to `0` (no cap) — tool→tool data flows raw so downstream steps receive the full upstream output (fidelity-first). Set it `> 0` to cap each parameter value, or supply `deps.AgentInputProcessor` for custom input shaping.

## Result Distillation (Default-On LLM Summarization)

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Enabled | true | `TRUVAG3_RESULT_DISTILL_ENABLED` | `WithResultDistill(bool, n)` |
| Min size to trigger | 16384 (16 KB) | `TRUVAG3_RESULT_DISTILL_THRESHOLD` | `WithResultDistill(bool, n)` |
| Pre-filter budget | 131072 (128 KB) | `TRUVAG3_RESULT_DISTILL_PREFILTER` | — |
| Target output size | 4096 (4 KB) | `TRUVAG3_RESULT_DISTILL_TARGET` | — |
| Model override | fast | `TRUVAG3_RESULT_DISTILL_MODEL` | `WithResultDistillModel(m)` |
| Cache TTL | 5m | `TRUVAG3_RESULT_DISTILL_CACHE_TTL` | — |
| Compaction deadline | 45s | `TRUVAG3_RESULT_DISTILL_DEADLINE` | — |
| Map-reduce trigger (model context) | 150000 tokens | `TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS` | — |
| Map-reduce concurrency | 8 | `TRUVAG3_RESULT_DISTILL_MAP_CONCURRENCY` | — |

> Default-on (opt-out): distillation is the primary compaction path for oversized results when the orchestrator has an `AIClient` **and** Result Trimming is enabled (both true by default). Without an `AIClient` it falls back to the structural floor. Opt out with `TRUVAG3_RESULT_DISTILL_ENABLED=false`.

> **Migration (upgrading from pre-default-on):** result distillation was previously **off by default** (`Enabled: false`, threshold `32768`, pre-filter `32768`). It is now **on by default** (threshold `16384`, pre-filter `131072`), so a deployment with an `AIClient` configured will begin making fast-model distillation calls for oversized results — added cost/latency on the **cheap** (`fast`) tier; the structural-trim floor was the prior behavior. No code change is required; set `TRUVAG3_RESULT_DISTILL_ENABLED=false` to restore the structural-only path.

> Units are mixed in this section: byte budgets (`THRESHOLD`, `PREFILTER`, `TARGET`) are integers in bytes; `CACHE_TTL` / `DEADLINE` are Go durations (e.g. `5m`, `45s`) and accept only positive values via env — disable the deadline with the programmatic `CompactionDeadline: 0`; `CONTEXT_TOKENS` is in tokens (results estimated above it — ~525 KB at the default `150000`, using the framework's ≈3.5 bytes/token counter — are chunked → map-reduced); `MAP_CONCURRENCY` is a count.

## Tiered Capability Resolution

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Enabled | true | `TRUVAG3_TIERED_RESOLUTION_ENABLED` | `WithTieredResolution(bool)` |
| Min tools to trigger | 20 | `TRUVAG3_TIERED_MIN_TOOLS` | — |
| Selection max tokens | 2000 | `TRUVAG3_TIERED_SELECTION_MAX_TOKENS` | `WithTieredSelectionMaxTokens(n)` |

## Retry Budgets

| Layer | Default | Env Var | `With*` Option |
|-------|---------|---------|----------------|
| Plan parse retry | 2 | `TRUVAG3_PLAN_RETRY_MAX` | `WithPlanParseRetry(bool, n)` |
| Hallucination retry | 1 | `TRUVAG3_HALLUCINATION_MAX_RETRIES` | `WithHallucinationRetry(bool, n)` |
| Semantic retry (Layer 4) | 2 | `TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS` | — |
| Step execution retry | 2 | — | — |
| AI provider retry (single client) | 3 | `TRUVAG3_AI_RETRY_ATTEMPTS` (rejects ≤0) | `ai.WithMaxRetries(n)` |
| AI provider retry (inside chain) | 0 (failover is the retry layer) | `TRUVAG3_AI_RETRY_ATTEMPTS` (rejects ≤0) | `ai.WithChainMaxRetries(n)` |

## Circuit Breaker

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Enabled | false | `TRUVAG3_CB_ENABLED` | — |
| Failure threshold | 5 | `TRUVAG3_CB_THRESHOLD` | — |
| Recovery timeout | 30s | `TRUVAG3_CB_TIMEOUT` | — |
| Half-open requests | 3 | `TRUVAG3_CB_HALF_OPEN` | — |

## Resilience

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Retry max attempts | 3 | `TRUVAG3_RETRY_MAX_ATTEMPTS` | — |
| Retry initial interval | 1s | `TRUVAG3_RETRY_INITIAL_INTERVAL` | — |
| Retry max interval | 30s | `TRUVAG3_RETRY_MAX_INTERVAL` | — |
| Retry multiplier | 2.0 | `TRUVAG3_RETRY_MULTIPLIER` | — |
| Default timeout | 30s | `TRUVAG3_TIMEOUT_DEFAULT` | — |
| Max timeout | 5m | `TRUVAG3_TIMEOUT_MAX` | — |

## HTTP Server

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Read timeout | 300s | `TRUVAG3_HTTP_READ_TIMEOUT` | — |
| Write timeout | 300s | `TRUVAG3_HTTP_WRITE_TIMEOUT` | — |
| Idle timeout | 120s | `TRUVAG3_HTTP_IDLE_TIMEOUT` | — |
| Max header bytes | 1 MB | `TRUVAG3_HTTP_MAX_HEADER_BYTES` | — |
| Shutdown timeout | 10s | `TRUVAG3_HTTP_SHUTDOWN_TIMEOUT` | — |

## Framework Runnable Lifecycle

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Runnable drain timeout | 10s | `TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT` | — |

Maximum time `Framework.Run` waits for registered `core.Runnable` instances to honour `ctx.Done()` and exit after ctx is cancelled. After this, the framework logs a warning, returns, and the OS reaps any remaining goroutines on process exit. Go duration format; only positive values are honored.

## Memory (in-process `*core.MemoryStore`)

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Sweeper interval | 10m | `TRUVAG3_MEMORY_CLEANUP_INTERVAL` | — |

Sweep interval for the in-process eviction sweeper used by `Framework.AutoRegisterMemorySweeper()` (agents) and `core.NewMemoryStoreSweeper(...)` (tools). Bounds memory at `(active entries) + (entries that expired since the last sweep tick)`. Go duration format; only positive values are honored. The framework only ships one `Memory` implementation today (`*core.MemoryStore`); `TRUVAG3_MEMORY_PROVIDER` and `TRUVAG3_MEMORY_REDIS_URL` are no longer parsed.

## Discovery

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Cache TTL | 5m | `TRUVAG3_DISCOVERY_CACHE_TTL` | — |
| Heartbeat interval | 10s | `TRUVAG3_DISCOVERY_HEARTBEAT` | — |
| Registration TTL | 30s | `TRUVAG3_DISCOVERY_TTL` | — |

## Shared Memory

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Domain stream max length | 100000 | `TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN` | — |
| Investigation claim TTL | 30m | `TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL` | — |
| Enrichment max tokens | 2000 | `TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS` | `WithEnrichmentMaxTokens(n)` |
| Recent events limit | 20 | `TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT` | `WithEnrichmentRecentEventsLimit(n)` |
| Summarizer model override | — | `TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL` | `WithSummarizerModel(m)` |
| Compaction digest max tokens | 500 | `TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS` | `WithCompactionMaxTokens(n)` |
| Compaction raw event limit | 200 | `TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT` | `WithCompactionRawLimit(n)` |
| Compaction recent detail | 15 | `TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL` | `WithCompactionRecentDetail(n)` |
| Digest cache TTL | 5m | `TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL` | `WithDigestCacheTTL(d)` |
| Digest incremental threshold | 20 | `TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD` | `WithIncrementalThreshold(n)` |

## User Memory

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Max facts in prompt | 15 | `TRUVAG3_USER_MEMORY_MAX_FACTS_IN_PROMPT` | — |
| Max identity facts | 5 | `TRUVAG3_USER_MEMORY_MAX_IDENTITY_FACTS` | — |
| Max durable facts in prompt | 8 | `TRUVAG3_USER_MEMORY_MAX_DURABLE_FACTS_IN_PROMPT` | — |
| Max transient facts in prompt | 4 | `TRUVAG3_USER_MEMORY_MAX_TRANSIENT_FACTS_IN_PROMPT` | — |
| Max summary facts in prompt | 3 | `TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS_IN_PROMPT` | — |
| Max stable facts per category | 2 | `TRUVAG3_USER_MEMORY_MAX_STABLE_FACTS_PER_CATEGORY` | — |
| Max universal facts | 5 | `TRUVAG3_USER_MEMORY_MAX_UNIVERSAL_FACTS` | — |
| Min confidence threshold | 0.3 | `TRUVAG3_USER_MEMORY_MIN_CONFIDENCE` | — |
| Reconciliation similarity | 0.75 | `TRUVAG3_USER_MEMORY_RECONCILIATION_THRESHOLD` | — |
| Extraction model | agent default | `TRUVAG3_USER_MEMORY_EXTRACTION_MODEL` | — |
| Reconciliation model | extraction model | `TRUVAG3_USER_MEMORY_RECONCILIATION_MODEL` | — |
| Summary response truncation | 500 chars | `TRUVAG3_USER_MEMORY_SUMMARY_MAX_RESPONSE_LEN` | — |
| Batched reconciliation tokens per candidate | 400 (floor 500) | `TRUVAG3_USER_MEMORY_BATCH_TOKENS_PER_CANDIDATE` | — |
| Recall over-fetch multiplier | 3 | `TRUVAG3_USER_MEMORY_RECALL_OVERFETCH_MULTIPLIER` | — |
| Transient max age | 168h | `TRUVAG3_USER_MEMORY_TRANSIENT_MAX_AGE_HOURS` | — |
| Max split clauses | 3 | `TRUVAG3_USER_MEMORY_MAX_SPLIT_CLAUSES` | — |
| Vector DB collection | `truvag3_user_memory` | `TRUVAG3_USER_MEMORY_COLLECTION` | `WithUserMemoryVectorOption(WithCollectionName(s))` |
| Extraction logic | LLM-based | — | `WithUserFactExtractor(e)` |
| Reconciliation logic | LLM-based (batched when reconciler implements `BatchUserFactReconciler`) | — | `WithUserFactReconciler(r)` |
| Persistence policy | framework default | — | `WithUserFactPersistencePolicy(p)` |
| Retrieval weights | 0.20/0.50/0.30 | — | `WithUserMemoryRetrievalWeights(w)` |
| Extraction mode | async at Layer 1 preset | — | `WithSynchronousExtraction()` |

## Reflection Job (Tier 2 → Tier 3 Bridge)

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Pass interval | 24h | `TRUVAG3_REFLECTION_INTERVAL` | — |
| Event age threshold | 168h (7d) | `TRUVAG3_REFLECTION_AGE_THRESHOLD` | — |
| Min events per entity | 5 | `TRUVAG3_REFLECTION_MIN_EVENTS` | `WithReflectorMinEvents(n)` |
| LLM model override | agent default | `TRUVAG3_REFLECTION_MODEL` | `WithReflectorModel(m)` |
| Distributed lock | from `deps.Lock` | — | `WithReflectionLock(l)` |
| Telemetry provider | nil | — | `WithReflectionTelemetry(t)` |

## Activity Coordination

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Enabled | true | `TRUVAG3_ACTIVITY_COORDINATION_ENABLED` | — |
| Signal TTL | 5m | `TRUVAG3_ACTIVITY_SIGNAL_TTL` | `WithAnnouncementSignalTTL(d)` |
| Max signals in prompt | 10 | `TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT` | — |
| Query max length | 200 | `TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN` | `WithAnnouncementQueryMaxLen(n)` |

## Debug Stores

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| LLM debug enabled | false | `TRUVAG3_LLM_DEBUG_ENABLED` | `WithLLMDebug(bool)` |
| LLM debug TTL | 24h | `TRUVAG3_LLM_DEBUG_TTL` | `WithLLMDebugTTL(d)` |
| LLM debug error TTL | 7d | `TRUVAG3_LLM_DEBUG_ERROR_TTL` | `WithLLMDebugErrorTTL(d)` |
| Execution store enabled | false | `TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED` | — |
| Execution store TTL | 24h | `TRUVAG3_EXECUTION_DEBUG_TTL` | `WithExecutionStoreTTL(d)` |
| Execution store error TTL | 7d | `TRUVAG3_EXECUTION_DEBUG_ERROR_TTL` | `WithExecutionStoreErrorTTL(d)` |

## Capability Provider (Service Mode)

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Top-K results | 20 | `TRUVAG3_CAPABILITY_TOP_K` | — |
| Similarity threshold | 0.7 | `TRUVAG3_CAPABILITY_THRESHOLD` | — |

## Human-in-the-Loop (HITL)

| What | Default | Env Var | `With*` Option |
|------|---------|---------|----------------|
| Enabled | false | `TRUVAG3_HITL_ENABLED` | — |
| Require plan approval | false | `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | — |
| Default timeout action | reject | `TRUVAG3_HITL_DEFAULT_ACTION` | — |

---

## Common Tuning Scenarios

**Complex multi-tool requests (truncation issues):**
```bash
export TRUVAG3_PLAN_MAX_TOKENS=20000
export TRUVAG3_TIERED_SELECTION_MAX_TOKENS=3000
export TRUVAG3_SYNTHESIS_MAX_TOKENS=10000
```

**Micro-resolution / semantic-retry truncation (unresolved templates in long text fields, or a semantic-retry correction whose parameters include a large text payload):**
```bash
export TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS=4000
```

**Large API responses (data overflow):**
```bash
export TRUVAG3_RESULT_TRIM_MAX_BYTES=32768
export TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES=65536
export TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES=131072
```

**Activity compaction too slow (high event volume):**
```bash
# Reduce events sent to compactor
export TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT=100
# Cache digests longer to reduce LLM calls
export TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL=10m
# Lower threshold for incremental updates
export TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD=10
```

**Disable shared memory LLM calls (cost control):**
```bash
# Use raw events instead of compacted digest (no LLM call)
export TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT=15
# Agent code: omit WithActivityCompactor() from enrichment hook
```
