package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// LLMDistiller implements ResultProcessor using a two-stage pipeline:
// Stage 1: StructuralTrimmer pre-filters to PreFilterBudget
// Stage 2: LLM distills to TargetSize
// Falls back to StructuralTrimmer on LLM failure.
type LLMDistiller struct {
	aiClient          core.AIClient
	config            ResultDistillConfig
	preFilter         ResultProcessor
	logger            core.Logger
	aiOptionsOverride *AIOptionsOverride

	// LLM Debug Store for recording distillation interactions
	debugStore LLMDebugStore
	debugWg    sync.WaitGroup
}

// normalizeResultDistillConfig applies construction-time invariants to a distill config and
// returns the normalized copy (the input is a value, so this never mutates the caller's config):
//
//   - resolve an unset PreFilterBudget to its default, so the threshold check below compares
//     against the value the map-reduce chunker will actually use,
//   - resolve an unset ModelContextTokens to its default, so the reduce-fits-context gate is
//     satisfiable on threshold-routed map-reduces (a zero context would permanently truncate
//     the reduce step), and
//   - disable a MapReduceThresholdBytes that sits below the effective PreFilterBudget (a
//     threshold under the chunk size routes results whose single chunk carries no fan-out value).
//
// Salt coherence is structural, not contractual: distillKeySalt calls this function itself (with a
// nil logger) before hashing, so cache keys are always computed from the same normalized values the
// distiller runs — no caller has to remember to normalize before salting.
func normalizeResultDistillConfig(cfg ResultDistillConfig, logger core.Logger) ResultDistillConfig {
	if cfg.PreFilterBudget <= 0 {
		cfg.PreFilterBudget = defaultPreFilterBudget
	}
	// A zero context makes the reduce gate (data+overhead <= ModelContextTokens) unsatisfiable —
	// the reduce LLM would never fire and every over-target combine would be head-truncated —
	// and P17 threshold routing makes map-reduce reachable without DefaultConfig. The compaction
	// model always has a real context size; backfill it.
	if cfg.ModelContextTokens <= 0 {
		cfg.ModelContextTokens = defaultModelContextTokens
	}
	// A zero threshold sends EVERY result — however tiny — through a paid LLM distill call on
	// the hot path (and the caching wrapper caches each one). A minimal Layer-2/Layer-3 config
	// means "defaults please", not "distill everything"; backfill like the two fields above.
	if cfg.DistillThreshold <= 0 {
		cfg.DistillThreshold = defaultDistillThreshold
	}
	// CompactionDeadline is deliberately NOT backfilled: 0 = disabled is a documented
	// programmatic opt-out (see the config field doc + LIMITS_CHEATSHEET). The advisory
	// unbounded-fan-out warn lives in the factory and the Layer-2 helper — NOT here — so it
	// fires once per assembled stack instead of once per distiller construction (a factory
	// builds two: synthesis + continuation).
	if cfg.MapReduceThresholdBytes > 0 && cfg.MapReduceThresholdBytes < cfg.PreFilterBudget {
		if logger != nil {
			logger.Warn("MapReduceThresholdBytes below PreFilterBudget; ignoring (map-reduce routing stays disabled)",
				map[string]interface{}{
					"operation":        "result_distill.config_normalization", // REQUIRED field (DISTRIBUTED_TRACING_GUIDE Pattern 2)
					"threshold":        cfg.MapReduceThresholdBytes,
					"prefilter_budget": cfg.PreFilterBudget,
				})
		}
		cfg.MapReduceThresholdBytes = 0
	}
	return cfg
}

// NewLLMDistiller creates a two-stage distiller with structural pre-filtering.
// preFilter MUST honor the ResultProcessor contract (see the interface doc): report any
// content loss via captureTrimMetadata's ContentLost and emit only registered single-line
// annotations. The partial-source disclosure and coverage accounting key on that signal — a
// pre-filter that trims silently disables every Phase 16 disclosure for its results.
//
// The config is normalized here (normalizeResultDistillConfig), so EVERY construction path — the
// factory, the Layer-2 BuildDistillationEnabledResultProcessor helper, and direct Layer-3
// construction — gets the single-chunk-footgun guard. A factory-only check would miss the other two.
func NewLLMDistiller(aiClient core.AIClient, config ResultDistillConfig, preFilter ResultProcessor, logger core.Logger) *LLMDistiller {
	config = normalizeResultDistillConfig(config, logger)
	return &LLMDistiller{aiClient: aiClient, config: config, preFilter: preFilter, logger: logger}
}

// EffectiveSize reports the post-distill footprint of a raw result for budget allocation (Phase 9).
// Below the threshold the result passes through structurally at ~raw size; at/above it the result is
// distilled to ~TargetSize regardless of how large it started. Implements EffectiveSizer.
func (d *LLMDistiller) EffectiveSize(rawSize int) int {
	if rawSize < d.config.DistillThreshold {
		return rawSize
	}
	if d.config.TargetSize > 0 && d.config.TargetSize < rawSize {
		return d.config.TargetSize
	}
	return rawSize
}

// SetAIOptionsOverride sets the per-phase AI options override for result distillation calls.
func (d *LLMDistiller) SetAIOptionsOverride(opts *AIOptionsOverride) {
	d.aiOptionsOverride = opts
}

// SetLLMDebugStore enables recording of distillation LLM calls in the registry viewer.
func (d *LLMDistiller) SetLLMDebugStore(store LLMDebugStore) {
	d.debugStore = store
}

// deferLLMRecordingIfWeWillRecord marks ctx so InstrumentedAIClient skips
// its own agent_llm_call emission when LLMDistiller will emit a typed
// result_distillation record itself. Gated on debugStore presence to preserve
// the graceful-fallback invariant in orchestration/ARCHITECTURE.md.
// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
func (d *LLMDistiller) deferLLMRecordingIfWeWillRecord(ctx context.Context) context.Context {
	if d.debugStore == nil {
		return ctx
	}
	return telemetry.WithLLMCallRecordingDeferred(ctx)
}

// recordDebugInteraction persists a distillation LLM interaction asynchronously.
// Baggage is extracted before spawning the goroutine and re-injected inside it,
// matching the pattern used by ErrorAnalyzer and AISynthesizer.
func (d *LLMDistiller) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
	if d.debugStore == nil || requestID == "" {
		return
	}

	d.debugWg.Add(1)
	go func() {
		defer d.debugWg.Done()

		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()

		if err := d.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			if d.logger != nil {
				d.logger.Warn("Failed to record distillation debug interaction", map[string]interface{}{
					"operation":  "result_distill",
					"request_id": requestID,
					"step_id":    interaction.StepID,
					"error":      err.Error(),
					"error_type": "debug_recording",
				})
			}
		}
	}()
}

// Shutdown waits for in-flight debug recordings to complete.
func (d *LLMDistiller) Shutdown() {
	d.debugWg.Wait()
}

// ProcessForPrompt applies two-stage distillation for results exceeding the threshold.
// Below threshold, delegates to the structural pre-filter only.
func (d *LLMDistiller) ProcessForPrompt(
	ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext,
) string {
	if len(result) < d.config.DistillThreshold {
		return d.preFilter.ProcessForPrompt(ctx, result, maxBytes, stepCtx)
	}

	// Extract request_id from baggage for Jaeger correlation (DISTRIBUTED_TRACING_GUIDE.md Pattern 6)
	requestID := ""
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		requestID = bag["request_id"]
	}

	distillStart := time.Now()

	// Output target, shared by the single-call and map-reduce paths.
	//
	// Shrink to the per-result budget only when that budget is a MEANINGFUL smaller value.
	// A 0/near-0 budget means the BudgetAllocator saturated and gave this (large) result
	// nothing — but distillation still compresses it to ~TargetSize regardless, so a 0
	// target must NOT produce a "max 0 characters" prompt (which makes the model answer
	// "impossible / no matching entries"). Ignore a non-positive budget and floor the
	// target so distillation always has room to emit a useful compaction.
	// (Live evidence: orch-1781973307736373861 — 8 oversized steps got budget 0 → empty.)
	targetSize := d.config.TargetSize
	if maxBytes > 0 && maxBytes < targetSize {
		targetSize = maxBytes
	}
	if targetSize < minDistillTargetSize {
		targetSize = minDistillTargetSize
	}

	// Outlier path: a result too large for a single model context is chunked and map-reduced over
	// the FULL result, bypassing the structural pre-filter below (which would lossily trim the bulk
	// before the LLM saw it). Two independent triggers (P17):
	//   - over-context: doesn't fit one model context (token estimate) — always map-reduce;
	//   - over-threshold: larger than MapReduceThresholdBytes (bytes) even though it FITS the
	//     context — routes the mid-band through map-reduce so the whole result reaches an LLM
	//     instead of only the pre-filtered head. 0 = disabled (context-only routing). A result
	//     whose compact form fits one chunk (raw-vs-compact shrink) still gets an LLM: the
	//     single-chunk case runs one extract call, never the bare structural floor.
	overContext := d.config.ModelContextTokens > 0 && estimateTokens(result) > d.config.ModelContextTokens
	overThreshold := d.config.MapReduceThresholdBytes > 0 && len(result) > d.config.MapReduceThresholdBytes
	if overContext || overThreshold {
		reason := "threshold"
		if overContext {
			reason = "context"
		}
		telemetry.AddSpanEvent(ctx, "result_distill.mapreduce_route",
			attribute.String("request_id", requestID),
			attribute.String("step_id", stepCtx.StepID),
			attribute.Int("original_bytes", len(result)),
			attribute.Int("estimated_tokens", estimateTokens(result)),
			attribute.String("reason", reason),
		)
		return d.mapReduceCompact(ctx, result, targetSize, stepCtx)
	}

	// Stage 1: Pre-filter. Nested capture so the pre-filter's stage-1 metadata (exact unit counts)
	// survives — the final distill metadata below overwrites the ctx slot (last-write-wins). (Phase 16)
	preCtx, preMeta := WithTrimMetadataCapture(ctx)
	preFiltered := d.preFilter.ProcessForPrompt(preCtx, result, d.config.PreFilterBudget, stepCtx)

	// Whether stage-1 lost content is decided by the trimmer's explicit ContentLost signal,
	// never inferred from bytes: pure re-serialization (pretty→compact) shrinks bytes without
	// losing content, escape/annotation inflation grows bytes while content WAS dropped, and a
	// passthrough source that happens to end in an annotation-shaped line would strip to a
	// false sub-1 ratio. The stripped-body byte ratio serves only as the magnitude estimate
	// once loss is known; 0.99 floors the inflation case so real loss never reads as full.
	coverage := 1.0
	if preMeta.ContentLost {
		coverage = lossyByteCoverage(len(stripResultAnnotation(preFiltered)), len(result))
	}

	telemetry.AddSpanEvent(ctx, "result_distill.stage1_complete",
		attribute.String("request_id", requestID),
		attribute.String("step_id", stepCtx.StepID),
		attribute.Int("original_bytes", len(result)),
		attribute.Int("prefiltered_bytes", len(preFiltered)),
	)

	// Stage 2: LLM distill
	prompt := d.buildDistillationPrompt(preFiltered, targetSize, stepCtx, coverage)

	// Scale MaxTokens from target size (~3-4 chars/token + headroom)
	maxTokens := targetSize/3 + 100
	if maxTokens < 1000 {
		maxTokens = 1000
	}
	baseOptions := &core.AIOptions{Temperature: 0.1, MaxTokens: maxTokens, Model: d.config.Model, SystemPrompt: distillationSystemPrompt}
	options := mergeAIOptions(baseOptions, d.aiOptionsOverride)

	callCtx := d.deferLLMRecordingIfWeWillRecord(ctx)
	// Bound the single compaction call by the global deadline. A timeout surfaces as an
	// err, so the existing fail-open below (structural pre-filter) applies — the synthesis
	// hot path is never blocked past the deadline. (The map-reduce path returns partial
	// LLM output instead; see result_mapreduce.go.)
	if d.config.CompactionDeadline > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(callCtx, d.config.CompactionDeadline)
		defer cancel()
	}
	response, err := d.aiClient.GenerateResponse(callCtx, prompt, options)
	duration := time.Since(distillStart)
	// Record usage BEFORE the empty-content guard below: an empty 200 response still billed
	// its prompt tokens, and hiding it from cost telemetry would make a content-filter burst
	// read as near-zero spend (the map-reduce path records usage the same way).
	if err == nil && response != nil {
		core.RecordTokenUsage(ctx, "distillation", response.Usage)
	}
	if err == nil && (response == nil || strings.TrimSpace(response.Content) == "") {
		// A 200-with-empty/whitespace-only content (content filter, stop-token edge) is the
		// same transient class as an error: without this guard the empty string ships as a
		// "successful" distillation — stamped lossless and CACHED for the TTL — while every
		// map-reduce call already converts the identical condition to a failure.
		err = fmt.Errorf("empty distillation response")
	}

	if err != nil {
		// Error handling order per DISTRIBUTED_TRACING_GUIDE.md Pattern 4:
		// 1. RecordSpanError (Jaeger visibility) → 2. SpanEvent → 3. Metrics → 4. Log
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "result_distill.llm_failed",
			attribute.String("request_id", requestID),
			attribute.String("step_id", stepCtx.StepID),
			attribute.String("error", err.Error()),
			attribute.Int64("duration_ms", duration.Milliseconds()),
		)
		if registry := core.GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("orchestration.result_distill.failed", "agent_name", stepCtx.AgentName)
			registry.Histogram("orchestration.result_distill.duration_ms", float64(duration.Milliseconds()), "agent_name", stepCtx.AgentName)
		}
		if d.logger != nil {
			d.logger.WarnWithContext(ctx, "LLM distillation failed, falling back to structural trim", map[string]interface{}{
				"operation":   "result_distill",
				"request_id":  requestID,
				"step_id":     stepCtx.StepID,
				"agent_name":  stepCtx.AgentName,
				"error":       err.Error(),
				"error_type":  "compaction",
				"duration_ms": duration.Milliseconds(),
			})
		}
		// Record failed distillation attempt in LLM Debug tab.
		// options.Model is populated by the AI client's ApplyDefaults before the network call.
		d.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:            "result_distillation",
			Timestamp:       distillStart,
			DurationMs:      duration.Milliseconds(),
			Prompt:          prompt,
			SystemPrompt:    distillationSystemPrompt,
			Response:        fmt.Sprintf("[DISTILLATION FAILED: %s — fell back to StructuralTrimmer]", err.Error()),
			Model:           options.Model,
			Temperature:     float64(options.Temperature),
			MaxTokens:       options.MaxTokens,
			Success:         false,
			Error:           err.Error(),
			StepID:          stepCtx.StepID,
			CallDescription: fmt.Sprintf("Distill %s result FAILED: %s", stepCtx.AgentName, err.Error()),
		})
		// This output is a structural fallback, not a successful distillation. Flag it so
		// the cache does not store a degraded result from a transient LLM/provider failure.
		// Budget = max(maxBytes, targetSize): the targetSize FLOOR protects tiny/zero budgets
		// from the near-empty output the floor exists to prevent, while a GENEROUS per-result
		// budget is kept in full — flooring alone would cap a 16 KB allocation at ~TargetSize
		// and quadruple content loss on exactly the runs that also lost their LLM pass
		// (review-caught regression of the first floor fix).
		fallbackBudget := maxBytes
		if fallbackBudget < targetSize {
			fallbackBudget = targetSize
		}
		markResultNonCacheable(ctx)
		return d.preFilter.ProcessForPrompt(ctx, result, fallbackBudget, stepCtx)
	}

	telemetry.AddSpanEvent(ctx, "result_distill.stage2_complete",
		attribute.String("request_id", requestID),
		attribute.String("step_id", stepCtx.StepID),
		attribute.Int("distilled_bytes", len(response.Content)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
		attribute.Int("prompt_tokens", response.Usage.PromptTokens),
		attribute.Int("completion_tokens", response.Usage.CompletionTokens),
		attribute.Int("total_tokens", response.Usage.TotalTokens),
	)

	if d.logger != nil {
		d.logger.DebugWithContext(ctx, "LLM distillation completed", map[string]interface{}{
			"operation":       "result_distill",
			"request_id":      requestID,
			"step_id":         stepCtx.StepID,
			"agent_name":      stepCtx.AgentName,
			"original_bytes":  len(result),
			"distilled_bytes": len(response.Content),
			"duration_ms":     duration.Milliseconds(),
		})
	}

	if registry := core.GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("orchestration.result_distill.triggered", "agent_name", stepCtx.AgentName)
		registry.Histogram("orchestration.result_distill.duration_ms", float64(duration.Milliseconds()), "agent_name", stepCtx.AgentName)
	}

	// Record successful distillation in LLM Debug tab.
	d.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "result_distillation",
		Timestamp:        distillStart,
		DurationMs:       duration.Milliseconds(),
		Prompt:           prompt,
		SystemPrompt:     distillationSystemPrompt,
		Response:         response.Content,
		Model:            response.Model,
		Provider:         response.Provider,
		Temperature:      float64(options.Temperature),
		MaxTokens:        options.MaxTokens,
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
		TotalTokens:      response.Usage.TotalTokens,
		Success:          true,
		StepID:           stepCtx.StepID,
		CallDescription:  fmt.Sprintf("Distill %s result: %d→%d bytes (stage1: %d bytes)", stepCtx.AgentName, len(result), len(response.Content), len(preFiltered)),
	})

	// Phase 16 — deterministically append the partial-source disclosure to the OUTPUT (what reaches
	// synthesis) when stage-1 dropped content. Framework-guaranteed; never relies on the model obeying
	// the secondary prompt signal above. Plain append (bounded overshoot of the note's ~111 B), NOT
	// appendDisclosure: reserving the note inside targetSize would silently cut the tail of the
	// model's own analyzed output whenever it lands within the note's length of targetSize — an
	// undisclosed deterministic cut, the exact class this phase eliminates.
	out := response.Content
	if coverage < 1 {
		out += partialSourceDisclosure(coverage)
	}

	captureTrimMetadata(ctx, ResultTrimMetadata{
		OriginalBytes:       len(result),
		TrimmedBytes:        len(out),
		Method:              "distill",
		FieldsKept:          preMeta.FieldsKept, // stage-1 exact counts survive the distill record
		FieldsDropped:       preMeta.FieldsDropped,
		KeptRatio:           preMeta.KeptRatio,
		SourceCoverageRatio: coverage, // approximate — see the ResultTrimMetadata field docs
		LLMInputBytes:       len(preFiltered),
		SegmentsAnalyzed:    1,
		SegmentsTotal:       1,
		PartialCoverage:     coverage < 1,
		ContentLost:         preMeta.ContentLost, // the authoritative bit must survive into the cached envelope
	})
	return out
}

// distillPromptVersion identifies the distillation prompt template. Bump it whenever
// buildDistillationPrompt OR distillationSystemPrompt changes so the cache key (via
// distillKeySalt) invalidates outputs produced by the old template instead of serving
// them for the TTL.
// "3" = Phase 13 task-primary (task leads <context> + dual-anchored on the final line).
// "4" = Phase 13 §2.9 split (identity + rules moved to the system message).
// "5" = Phase 16 partial-source coverage note added to <context> (invalidates cached outputs).
// "6" = Phase 16 ContentLost-gated disclosure + plain-appended partial-source note (output
// composition changed, so cached "5"-era outputs must not be replayed).
// "7" = Phase 16 wrapper-drop disclosure on map-reduce outputs + tri-state ContentLost
// envelope semantics (explicit false = verified lossless); stale envelopes would otherwise
// replay an affirmative lossless claim for a lossy result, so they must be unreachable.
// "8" = Phase 17 chunker/trimmer hardening: the map-reduce object path now REPLICATES the
// wrapper into every chunk (chunkJSONObjectPreservingWrapper — P17.6) and the whole trim
// pipeline decodes JSON with UseNumber (P17.5), so what the LLM sees per chunk — and the
// preserved large IDs — changed; stale "7"-era outputs must not be replayed. (This is now a
// general PIPELINE version, not just the prompt template — bump it for any change to what the
// chunker/trimmer feeds the model, not only buildDistillationPrompt/distillationSystemPrompt.)
const distillPromptVersion = "8"

// distillationSystemPrompt is the STATIC policy for distillation — identity + rules. It is
// dispatched as the system message (mirroring synthesizer.go's synthesisSystemPrompt) so the
// rules are structurally elevated above, and not confused with, the arbitrary <data> being
// compacted (EFFECTIVE_PROMPTS_GUIDE §2.9 / §9.1 — which lists these tags as "System msg").
// The per-call byte budget is dynamic, so it lives in the user message (buildDistillationPrompt)
// and is anchored on the final line.
const distillationSystemPrompt = `<identity>
You compact one upstream result so a downstream task can use it. A downstream model
reads ONLY your output and never the original, so anything you omit becomes invisible
to it. For evidence data you SELECT and COPY the relevant content verbatim — you are a
selector, not a paraphraser.
</identity>

<instructions>
1. Output at most the character budget stated on the final line of the request.
2. First decide what kind of data this is:
   - EVIDENCE (records, log lines, rows, search hits, transactions, items, events):
     copy the matching units VERBATIM — preserve exact text, identifiers, primary keys,
     timestamps, and numeric values byte-for-byte. Keep each unit whole.
   - NARRATIVE (prose, descriptions, explanations): write a concise factual summary and
     still preserve identifiers, keys, and exact numeric values.
3. Select the units relevant to the downstream task, using the user goal as the broader
   intent — keep the units that actually answer them.
4. If no unit is relevant, output exactly: No matching entries found (use this only when
   the input genuinely contains nothing relevant).
5. If relevant units must be dropped to fit the limit, keep the most relevant and end
   with: [truncated: N additional matching units omitted to fit budget — treat anything
   not shown as UNKNOWN, not absent].
6. State that something is absent only after inspecting the entire input and confirming
   it — omission to fit the budget is not evidence of absence.
</instructions>`

// minDistillTargetSize is the floor for a distillation output target (bytes). It guards
// against a per-result budget of 0/near-0 (which the BudgetAllocator can hand an oversized
// result when the total budget is saturated) producing a degenerate "max 0 characters"
// prompt. Distillation compresses large results to ~TargetSize regardless, so it always
// needs at least this much room to emit something useful.
const minDistillTargetSize = 256

// defaultPreFilterBudget is the structural pre-filter cap (single-call path) and the
// map-reduce chunk size (bytes) used when ResultDistillConfig.PreFilterBudget is unset.
// 128 KB ≈ 37K tokens at ~3.5 B/tok: it fits the smallest fast-tier compaction context
// (DeepSeek-chat's 64K endpoint, leaving ~27K for the prompt template + output) while
// covering 128K+ context models with ample headroom, and it cuts map-reduce chunk/call
// count ~4× vs the prior 32 KB. Raise via TRUVAG3_RESULT_DISTILL_PREFILTER on deployments
// pinned to large-context fast models (Gemini Flash-Lite / gpt-4.1-mini at 1M).
const defaultPreFilterBudget = 131072

// defaultDistillThreshold is the minimum result size (bytes) that triggers an LLM distillation
// call, used when ResultDistillConfig.DistillThreshold is unset. Backfilled at normalization: a
// zero threshold makes EVERY result — however tiny — take a paid LLM call on the hot path.
const defaultDistillThreshold = 16384

// defaultModelContextTokens is the usable compaction-model context (tokens) used when
// ResultDistillConfig.ModelContextTokens is unset. Backfilled at normalization: a zero value
// would make the reduce-fits-context gate (data + overhead <= 0) unsatisfiable, permanently
// truncating the reduce step for Layer-2/Layer-3 configs that set only the byte threshold.
const defaultModelContextTokens = 150000

// noMatchSentinel is the exact string the distillation prompt (distillationSystemPrompt, instruction
// #4) tells the model to emit when nothing in the input is relevant. The map-reduce path matches on
// it to consolidate empty chunks (result_mapreduce.go) — keep the two in sync.
const noMatchSentinel = "No matching entries found"

func (d *LLMDistiller) buildDistillationPrompt(result string, maxBytes int, stepCtx ResultProcessorContext, coverage float64) string {
	capabilityAttr := ""
	if stepCtx.Capability != "" {
		capabilityAttr = fmt.Sprintf(" capability=%q", stepCtx.Capability)
	}
	// Phase 13 — task-primary USER message. The downstream task is the PRIMARY relevance signal: it is
	// the precise lens for THIS result, so it LEADS <context> and is echoed verbatim on the final line
	// (dual-anchor / U-shaped attention). The user's overall goal follows as the broader intent
	// (retained — dropping it regresses Phase 11: a thin instruction carries no selection criteria and
	// the distiller is a one-way lossy gate). Each line is empty-guarded so an empty Instruction never
	// emits a dangling "Downstream task:" line and the final anchor degrades to a generic phrase.
	// Identity + rules are the system message (distillationSystemPrompt, §2.9); only the per-call
	// context/data + the budget-bearing final line are built here.
	downstreamTaskLine := ""
	taskAnchor := "the downstream task"
	if stepCtx.Instruction != "" {
		downstreamTaskLine = fmt.Sprintf("Downstream task: %s\n", stepCtx.Instruction)
		taskAnchor = fmt.Sprintf("%q", stepCtx.Instruction)
	}
	userGoalLine := ""
	if stepCtx.OriginalQuery != "" {
		userGoalLine = fmt.Sprintf("User goal: %s\n", stepCtx.OriginalQuery)
	}
	// Phase 16 — secondary (non-guaranteeing) signal that stage-1 showed the model only part of the
	// source. Lives in <context> at the TOP high-attention edge (EFFECTIVE_PROMPTS_GUIDE §2.1) and uses
	// positive phrasing (§2.4). The GUARANTEE is the framework-appended output disclosure
	// (partialSourceDisclosure); this line only nudges the model to self-qualify.
	coverageLine := ""
	if coverage < 1 {
		coverageLine = fmt.Sprintf("Note: you received ~%d%% of the source by bytes; report any absence as absence WITHIN THE PROVIDED SAMPLE.\n", coveragePct(coverage))
	}
	return fmt.Sprintf(`<context source=%q%s>
%s%s%s</context>

<data>
%s
</data>

Return the compacted result for %s (at most %d characters):`,
		stepCtx.AgentName, capabilityAttr, downstreamTaskLine, userGoalLine, coverageLine, result, taskAnchor, maxBytes)
}
