package orchestration

import (
	"context"
	"fmt"
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

// NewLLMDistiller creates a two-stage distiller with structural pre-filtering.
func NewLLMDistiller(aiClient core.AIClient, config ResultDistillConfig, preFilter ResultProcessor, logger core.Logger) *LLMDistiller {
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

	// Outlier path: a result too large for a single model context is chunked and
	// map-reduced over the FULL result. This bypasses the structural pre-filter below,
	// which would lossily trim the bulk before the LLM ever saw it.
	if d.config.ModelContextTokens > 0 && estimateTokens(result) > d.config.ModelContextTokens {
		telemetry.AddSpanEvent(ctx, "result_distill.mapreduce_route",
			attribute.String("request_id", requestID),
			attribute.String("step_id", stepCtx.StepID),
			attribute.Int("original_bytes", len(result)),
			attribute.Int("estimated_tokens", estimateTokens(result)),
		)
		return d.mapReduceCompact(ctx, result, targetSize, stepCtx)
	}

	// Stage 1: Pre-filter
	preFiltered := d.preFilter.ProcessForPrompt(ctx, result, d.config.PreFilterBudget, stepCtx)

	telemetry.AddSpanEvent(ctx, "result_distill.stage1_complete",
		attribute.String("request_id", requestID),
		attribute.String("step_id", stepCtx.StepID),
		attribute.Int("original_bytes", len(result)),
		attribute.Int("prefiltered_bytes", len(preFiltered)),
	)

	// Stage 2: LLM distill
	prompt := d.buildDistillationPrompt(preFiltered, targetSize, stepCtx)

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
	if err == nil {
		core.RecordTokenUsage(ctx, "distillation", response.Usage)
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
		markResultNonCacheable(ctx)
		return d.preFilter.ProcessForPrompt(ctx, result, maxBytes, stepCtx)
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

	captureTrimMetadata(ctx, ResultTrimMetadata{
		OriginalBytes: len(result),
		TrimmedBytes:  len(response.Content),
		Method:        "distill",
	})
	return response.Content
}

// distillPromptVersion identifies the distillation prompt template. Bump it whenever
// buildDistillationPrompt OR distillationSystemPrompt changes so the cache key (via
// distillKeySalt) invalidates outputs produced by the old template instead of serving
// them for the TTL.
// "3" = Phase 13 task-primary (task leads <context> + dual-anchored on the final line).
// "4" = Phase 13 §2.9 split (identity + rules moved to the system message).
const distillPromptVersion = "4"

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

// noMatchSentinel is the exact string the distillation prompt (distillationSystemPrompt, instruction
// #4) tells the model to emit when nothing in the input is relevant. The map-reduce path matches on
// it to consolidate empty chunks (result_mapreduce.go) — keep the two in sync.
const noMatchSentinel = "No matching entries found"

func (d *LLMDistiller) buildDistillationPrompt(result string, maxBytes int, stepCtx ResultProcessorContext) string {
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
	return fmt.Sprintf(`<context source=%q%s>
%s%s</context>

<data>
%s
</data>

Return the compacted result for %s (at most %d characters):`,
		stepCtx.AgentName, capabilityAttr, downstreamTaskLine, userGoalLine, result, taskAnchor, maxBytes)
}
