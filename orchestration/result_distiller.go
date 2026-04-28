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
					"request_id": requestID,
					"step_id":    interaction.StepID,
					"error":      err.Error(),
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

	// Stage 1: Pre-filter
	preFiltered := d.preFilter.ProcessForPrompt(ctx, result, d.config.PreFilterBudget, stepCtx)

	telemetry.AddSpanEvent(ctx, "result_distill.stage1_complete",
		attribute.String("request_id", requestID),
		attribute.String("step_id", stepCtx.StepID),
		attribute.Int("original_bytes", len(result)),
		attribute.Int("prefiltered_bytes", len(preFiltered)),
	)

	// Stage 2: LLM distill
	targetSize := d.config.TargetSize
	if maxBytes < targetSize {
		targetSize = maxBytes
	}
	prompt := d.buildDistillationPrompt(preFiltered, targetSize, stepCtx)

	// Scale MaxTokens from target size (~3-4 chars/token + headroom)
	maxTokens := targetSize/3 + 100
	if maxTokens < 1000 {
		maxTokens = 1000
	}
	baseOptions := &core.AIOptions{Temperature: 0.1, MaxTokens: maxTokens, Model: d.config.Model}
	options := mergeAIOptions(baseOptions, d.aiOptionsOverride)

	callCtx := d.deferLLMRecordingIfWeWillRecord(ctx)
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
			Response:        fmt.Sprintf("[DISTILLATION FAILED: %s — fell back to StructuralTrimmer]", err.Error()),
			Model:           options.Model,
			Temperature:     float64(options.Temperature),
			MaxTokens:       options.MaxTokens,
			Success:         false,
			Error:           err.Error(),
			StepID:          stepCtx.StepID,
			CallDescription: fmt.Sprintf("Distill %s result FAILED: %s", stepCtx.AgentName, err.Error()),
		})
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

func (d *LLMDistiller) buildDistillationPrompt(result string, maxBytes int, stepCtx ResultProcessorContext) string {
	capabilityLine := ""
	if stepCtx.Capability != "" {
		capabilityLine = fmt.Sprintf(" (capability: %s)", stepCtx.Capability)
	}
	return fmt.Sprintf(`<identity>
You are a data distillation assistant. Summarize data for use in a downstream task.
</identity>

<instructions>
1. Output at most %d characters
2. Preserve all identifier fields, primary keys, and exact numeric values
3. Prioritize values most relevant to the downstream task instruction
4. For JSON data, keep the same top-level structure with only essential fields
5. For plain text, output a concise factual summary
</instructions>

<context source="%s%s">
Downstream task: %s
</context>

<data>
%s
</data>

Return the distilled result:`,
		maxBytes,
		stepCtx.AgentName, capabilityLine, stepCtx.Instruction, result)
}
