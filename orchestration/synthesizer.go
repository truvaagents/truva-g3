package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// AISynthesizer uses AI to synthesize agent responses
type AISynthesizer struct {
	aiClient core.AIClient
	strategy SynthesisStrategy
	logger   core.Logger

	// Synthesis LLM parameters (propagated from OrchestratorConfig)
	aiOptionsOverride    *AIOptionsOverride
	synthesisTemperature float64
	synthesisMaxTokens   int
	model                string

	// Result processing for large data management (Layer 2+)
	resultProcessor  ResultProcessor
	resultTrimConfig *ResultTrimConfig

	// LLM Debug Store for full payload visibility
	debugStore LLMDebugStore
	debugWg    sync.WaitGroup
	debugSeqID atomic.Uint64

	// precedenceEntityExtractor is propagated from the orchestrator; used
	// by recordDebugInteraction to populate LLMInteraction.PrecedenceAudit
	// entity fields. Nil = cheap structural audit only.
	precedenceEntityExtractor PrecedenceEntityExtractor
}

// SetPrecedenceEntityExtractor wires a domain-specific entity extractor
// used when recording synthesis interactions. Mirrors the orchestrator
// setter; AIOrchestrator.SetPrecedenceEntityExtractor propagates here
// automatically so callers only need to wire it at one layer.
func (s *AISynthesizer) SetPrecedenceEntityExtractor(extractor PrecedenceEntityExtractor) {
	s.precedenceEntityExtractor = extractor
}

// synthesisSystemPrompt is the shared system prompt for synthesis LLM calls.
// Used by both streaming (orchestrator.go) and non-streaming (synthesizer.go) paths.
// Structure follows docs/building/EFFECTIVE_PROMPTS_GUIDE.md §8.3 template.
const synthesisSystemPrompt = `<identity>
You are an AI synthesis engine. You combine responses from multiple specialized
agents into a single coherent answer for the user.
</identity>

<instructions>
1. Synthesize the agent responses into a comprehensive answer addressing the user's original request
2. Combine information from multiple agents where their data overlaps or complements
3. Highlight important findings, recommendations, or actionable insights
4. Be concise but thorough — prefer clarity over verbosity
5. If some agents failed, work with the available information and note any gaps only when relevant to the answer
6. Preserve specific data points (numbers, names, dates) exactly as provided by agents
</instructions>`

// clarificationModeAddendum is appended to synthesisSystemPrompt when the
// planner has emitted needs_user_input. The synthesizer produces a
// conversational response that summarizes partial progress and asks the
// user the clarification question naturally. (ORCH-018)
//
// All instructions are positive directives per docs/building/EFFECTIVE_PROMPTS_GUIDE.md §2.4.
const clarificationModeAddendum = `

<clarification_mode>
The execution paused because the planner needs information from the user to continue.
Produce a conversational response that:
1. Summarizes any partial progress from <agent_responses> in one or two sentences.
2. Asks the user the question from <clarification_needed> naturally, woven into the same reply.
3. Reads like a normal conversational message — phrase the question as a regular sentence within the prose.
4. When <clarification_needed> lists missing_fields, mention each one as part of the question naturally.
</clarification_mode>`

// synthesisSystemPromptFor returns the appropriate system prompt for the
// given execution result. Returns the default for normal completions and
// the clarification-augmented variant when ClarificationNeeded is set.
// (ORCH-018)
func synthesisSystemPromptFor(results *ExecutionResult) string {
	if results != nil && results.ClarificationNeeded != nil {
		return synthesisSystemPrompt + clarificationModeAddendum
	}
	return synthesisSystemPrompt
}

// NewAISynthesizer creates a new AI-powered synthesizer
func NewAISynthesizer(aiClient core.AIClient) *AISynthesizer {
	return &AISynthesizer{
		aiClient:             aiClient,
		strategy:             StrategyLLM,
		synthesisTemperature: 0.5,
		synthesisMaxTokens:   5000,
	}
}

// Synthesize combines agent responses into a final response using AI
func (s *AISynthesizer) Synthesize(ctx context.Context, request string, results *ExecutionResult) (string, error) {
	switch s.strategy {
	case StrategyLLM:
		return s.synthesizeWithLLM(ctx, request, results)
	case StrategyTemplate:
		return s.synthesizeWithTemplate(request, results)
	case StrategySimple:
		return s.synthesizeSimple(results)
	default:
		return s.synthesizeWithLLM(ctx, request, results)
	}
}

// synthesizeWithLLM uses the LLM to create a coherent response
func (s *AISynthesizer) synthesizeWithLLM(ctx context.Context, request string, results *ExecutionResult) (string, error) {
	// Build prompt with all agent responses
	prompt := s.buildSynthesisPrompt(ctx, request, results)
	systemPrompt := synthesisSystemPromptFor(results) // ORCH-018: clarification-aware

	// Extract request_id once for all telemetry in this method (LOGGING_IMPLEMENTATION_GUIDE.md Pattern 3)
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = s.generateFallbackRequestID()
	}

	// ORCH-018: annotate synthesis span when clarification mode is active.
	// Follows docs/observability/DISTRIBUTED_TRACING_GUIDE.md §11 and
	//         docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md §11 patterns.
	if results != nil && results.ClarificationNeeded != nil {
		// Span attributes — filterable in trace search
		telemetry.SetSpanAttributes(ctx,
			attribute.Bool("synthesis.clarification_mode", true),
			attribute.Int("synthesis.partial_progress_len", len(results.ClarificationNeeded.PartialProgress)),
			attribute.Int("synthesis.missing_field_count", len(results.ClarificationNeeded.MissingFields)),
		)

		// Span event — Pattern 6: request_id as FIRST attribute
		telemetry.AddSpanEvent(ctx, "orchestrator.synthesis.clarification_mode",
			attribute.String("request_id", requestID),
			attribute.String("question", truncateString(results.ClarificationNeeded.Question, 200)),
			attribute.Int("missing_field_count", len(results.ClarificationNeeded.MissingFields)),
			attribute.Bool("has_partial_progress", results.ClarificationNeeded.PartialProgress != ""),
		)

		// Counter — Pattern 5: module label
		telemetry.Counter("orchestrator.synthesis.clarification_mode",
			"module", telemetry.ModuleOrchestration,
		)

		// Structured log — Patterns 1+2+3 (logger nil check, operation field, request_id)
		if s.logger != nil {
			s.logger.InfoWithContext(ctx, "Synthesizer entered clarification mode", map[string]interface{}{
				"operation":            "synthesis_clarification_mode",
				"request_id":           requestID,
				"missing_field_count":  len(results.ClarificationNeeded.MissingFields),
				"has_partial_progress": results.ClarificationNeeded.PartialProgress != "",
				"question_length":      len(results.ClarificationNeeded.Question),
			})
		}
	}

	// Record trimming stats as span event
	if s.resultTrimConfig != nil && s.resultTrimConfig.Enabled {
		var totalOriginalBytes int
		for _, step := range results.Steps {
			if step.Success {
				totalOriginalBytes += len(step.Response)
			}
		}
		telemetry.AddSpanEvent(ctx, "result_trim.synthesis",
			attribute.String("request_id", requestID),
			attribute.Int("original_total_bytes", totalOriginalBytes),
			attribute.Int("prompt_length", len(prompt)),
			attribute.Int("step_count", len(results.Steps)),
		)
	}

	// Telemetry: Record LLM prompt for synthesis
	baseOpts := &core.AIOptions{
		Temperature:  0.5,
		MaxTokens:    5000,
		SystemPrompt: systemPrompt,
	}
	synthesisOpts := mergeAIOptions(baseOpts, s.aiOptionsOverride)

	synthesisEventAttrs := []attribute.KeyValue{
		attribute.String("request_id", requestID),
		attribute.String("original_request", truncateString(request, 500)),
		attribute.String("prompt", truncateString(prompt, 2000)),
		attribute.Int("prompt_length", len(prompt)),
		attribute.Int("step_count", len(results.Steps)),
		attribute.Float64("temperature", float64(synthesisOpts.Temperature)),
		attribute.Int("max_tokens", synthesisOpts.MaxTokens),
	}
	if synthesisOpts.Model != "" {
		synthesisEventAttrs = append(synthesisEventAttrs, attribute.String("model", synthesisOpts.Model))
	}
	telemetry.AddSpanEvent(ctx, "llm.synthesis.request", synthesisEventAttrs...)

	// Call LLM for synthesis
	ctx = telemetry.WithBaggage(ctx, "ai.purpose", "synthesis")
	llmStartTime := time.Now()
	invocation := aiInvocation{
		Purpose:        "synthesis",
		Prompt:         prompt,
		Options:        synthesisOpts,
		DeferRecording: s.debugStore != nil,
	}
	invocationResult, err := invokeAI(ctx, s.aiClient, invocation)
	var aiResponse *core.AIResponse
	if invocationResult != nil {
		aiResponse = invocationResult.Response
	}
	effective := effectiveAIRequestForDebug(invocationResult, invocation)
	llmDuration := time.Since(llmStartTime)
	if err == nil {
		core.RecordTokenUsage(ctx, "synthesis", aiResponse.Usage)
	}

	if err != nil {
		telemetry.AddSpanEvent(ctx, "llm.synthesis.error",
			attribute.String("error", err.Error()),
			attribute.Int64("duration_ms", llmDuration.Milliseconds()),
		)

		// LLM Debug: Record failed synthesis attempt from the prepared request.
		model, provider := effectiveAIIdentity(invocationResult, aiResponse, err)
		s.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:         "synthesis",
			Timestamp:    llmStartTime,
			DurationMs:   llmDuration.Milliseconds(),
			Prompt:       effective.Prompt,
			SystemPrompt: effective.SystemPrompt,
			Temperature:  effectiveAITemperature(effective, synthesisOpts.Temperature),
			MaxTokens:    effectiveAIMaxTokens(effective, synthesisOpts.MaxTokens),
			Model:        model,
			Provider:     provider,
			Success:      false,
			Error:        err.Error(),
			Attempt:      1,
		})

		return "", fmt.Errorf("synthesis failed: %w", err)
	}

	// Telemetry: Record LLM response for synthesis
	telemetry.AddSpanEvent(ctx, "llm.synthesis.response",
		attribute.String("response", truncateString(aiResponse.Content, 2000)),
		attribute.Int("response_length", len(aiResponse.Content)),
		attribute.Int("prompt_tokens", aiResponse.Usage.PromptTokens),
		attribute.Int("completion_tokens", aiResponse.Usage.CompletionTokens),
		attribute.Int("total_tokens", aiResponse.Usage.TotalTokens),
		attribute.Int64("duration_ms", llmDuration.Milliseconds()),
	)

	// Mirror the same fields onto the parent orchestrator.synthesis span as
	// attributes (instead of just events) so they're queryable in Jaeger
	// without expanding the span. Streaming path enriches its own span inline.
	telemetry.SetSpanAttributes(ctx,
		attribute.String("synthesis.model", aiResponse.Model),
		attribute.String("synthesis.provider", aiResponse.Provider),
		attribute.Int("synthesis.prompt_length", len(prompt)),
		attribute.Int("synthesis.response_length", len(aiResponse.Content)),
		attribute.Int("synthesis.prompt_tokens", aiResponse.Usage.PromptTokens),
		attribute.Int("synthesis.completion_tokens", aiResponse.Usage.CompletionTokens),
		attribute.Int("synthesis.total_tokens", aiResponse.Usage.TotalTokens),
		attribute.Int64("synthesis.llm_duration_ms", llmDuration.Milliseconds()),
	)

	// LLM Debug: Record successful synthesis
	model, provider := effectiveAIIdentity(invocationResult, aiResponse, nil)
	s.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "synthesis",
		Timestamp:        llmStartTime,
		DurationMs:       llmDuration.Milliseconds(),
		Prompt:           effective.Prompt,
		SystemPrompt:     effective.SystemPrompt,
		Temperature:      effectiveAITemperature(effective, synthesisOpts.Temperature),
		MaxTokens:        effectiveAIMaxTokens(effective, synthesisOpts.MaxTokens),
		Model:            model,
		Provider:         provider,
		Response:         aiResponse.Content,
		PromptTokens:     aiResponse.Usage.PromptTokens,
		CompletionTokens: aiResponse.Usage.CompletionTokens,
		TotalTokens:      aiResponse.Usage.TotalTokens,
		Success:          true,
		Attempt:          1,
	})

	return aiResponse.Content, nil
}

// buildSynthesisPrompt creates the prompt for response synthesis.
// ctx is threaded for OTEL span event correlation in ResultProcessor.
// When multiple successful results exist and MaxTotalPromptBytes is configured,
// uses ProcessMultipleForBudget for proportional budget allocation across results.
func (s *AISynthesizer) buildSynthesisPrompt(ctx context.Context, request string, results *ExecutionResult) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "<user_request>\n%s\n</user_request>\n\n", request)

	// Include agent coordination, memory, and conversation history from pipeline enrichments.
	// Gives the synthesizer awareness of active agents, prior cross-agent activity, and
	// session context so it can produce more informed, deduplicated summaries.
	enrichments := core.GetPipelineEnrichments(ctx)
	if len(enrichments) > 0 {
		if coordCtx, ok := enrichments[core.EnrichmentActivityCoordination]; ok {
			if coordStr, isStr := coordCtx.(string); isStr && coordStr != "" {
				builder.WriteString("<agent_coordination>\n")
				builder.WriteString(coordStr)
				builder.WriteString("\n</agent_coordination>\n\n")
			}
		}
		// User profile from UserMemoryEnrichmentHook (per-user private facts)
		if userProfile, ok := enrichments[core.EnrichmentUserProfile]; ok {
			if profileStr, isStr := userProfile.(string); isStr && profileStr != "" {
				builder.WriteString(profileStr)
				builder.WriteString("\n\n")
			}
		}
		if ragCtx, ok := enrichments[core.EnrichmentRAGContext]; ok {
			if ragStr, isStr := ragCtx.(string); isStr && ragStr != "" {
				builder.WriteString("<agent_memory>\n")
				builder.WriteString(ragStr)
				builder.WriteString("\n</agent_memory>\n\n")
			}
		}
		if convHistory, ok := enrichments[core.EnrichmentConversationHistory]; ok {
			if convStr, isStr := convHistory.(string); isStr && convStr != "" {
				builder.WriteString("<conversation_history>\n")
				builder.WriteString(convStr)
				builder.WriteString("\n</conversation_history>\n\n")
			}
		}
	}

	// Precedence rule: emitted right after the enrichments so the synthesizer
	// reads it immediately before the agent_responses block. Without this, a
	// stale <user_profile> "Context" entry can override what the agents
	// actually returned (the original Switzerland-vs-Italy regression
	// reaches this prompt as well as the planner).
	writeContextPrecedence(ctx, &builder, enrichments, PromptKindSynthesis)

	builder.WriteString("<agent_responses>\n\n")

	// Extract request_id for logging (LOGGING_IMPLEMENTATION_GUIDE.md Pattern 3)
	promptRequestID := ""
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		promptRequestID = bag["request_id"]
	}

	// Collect successful steps for budget-aware processing
	var successfulSteps []StepResult
	for _, step := range results.Steps {
		if step.Success {
			successfulSteps = append(successfulSteps, step)
		}
	}

	// Pre-process with budget allocation when multiple results and total budget configured
	var budgetProcessed map[string]string // stepID → processed response
	var budgetMeta map[string]*ResultTrimMetadata
	useBudgetAlloc := len(successfulSteps) > 1 &&
		s.resultTrimConfig != nil && s.resultTrimConfig.Enabled &&
		s.resultProcessor != nil && s.resultTrimConfig.MaxTotalPromptBytes > 0

	if useBudgetAlloc {
		processed, bm := ProcessMultipleForBudget(ctx, s.resultProcessor, successfulSteps,
			s.resultTrimConfig.MaxTotalPromptBytes, s.resultTrimConfig.MaxResultBytes, request)
		budgetProcessed = make(map[string]string, len(successfulSteps))
		for i, step := range successfulSteps {
			budgetProcessed[step.StepID] = processed[i]
		}
		budgetMeta = bm
	}

	for i, step := range results.Steps {
		if step.Success {
			response := step.Response
			originalSize := len(response)
			var trimMeta *ResultTrimMetadata

			if bp, ok := budgetProcessed[step.StepID]; ok {
				// Budget-allocated response (Phase 3)
				response = bp
				trimMeta = budgetMeta[step.StepID]
			} else if s.resultTrimConfig != nil && s.resultTrimConfig.Enabled && s.resultProcessor != nil {
				// Per-result trimming (single result or no total budget)
				trimCtx, meta := WithTrimMetadataCapture(ctx)
				response = s.resultProcessor.ProcessForPrompt(trimCtx, response, s.resultTrimConfig.MaxTotalPromptBytes, ResultProcessorContext{
					StepID:        step.StepID,
					AgentName:     step.AgentName,
					Instruction:   step.Instruction,
					OriginalQuery: request,
				})
				trimMeta = meta
			} else if s.resultTrimConfig != nil && s.resultTrimConfig.Enabled && len(response) > s.resultTrimConfig.MaxTotalPromptBytes {
				// Byte truncation fallback (no result processor ran → no model analyzed the dropped
				// tail). appendDisclosure cuts + discloses within the budget in one step. (Phase 16)
				response = appendDisclosure(response, truncationDisclosure(), s.resultTrimConfig.MaxTotalPromptBytes)
				trimMeta = &ResultTrimMetadata{
					OriginalBytes: originalSize, TrimmedBytes: len(response), Method: "truncate",
					PartialCoverage: true, ContentLost: true,
				}
			}

			// Write trim metadata back through the slice index — step is a range copy.
			if trimMeta != nil {
				if results.Steps[i].Metadata == nil {
					results.Steps[i].Metadata = make(map[string]interface{})
				}
				results.Steps[i].Metadata["result_trim"] = cloneResultTrimMetadata(trimMeta)
			}

			trimmedSize := len(response)

			// Emit span event for result trim decisions (visible in Jaeger) — see
			// lossyTrimEvent for why the gate is not byte inequality alone.
			if lossyTrimEvent(trimMeta, originalSize, trimmedSize) {
				attrs := []attribute.KeyValue{
					attribute.String("request_id", promptRequestID),
					attribute.String("step_id", step.StepID),
					attribute.String("agent_name", step.AgentName),
					attribute.String("method", trimMeta.Method),
					// Unconditional: explicit false = verified lossless, distinct from a
					// legacy span with no signal — the same tri-state the JSON field carries.
					attribute.Bool("content_lost", trimMeta.ContentLost),
					attribute.Int("original_bytes", trimMeta.OriginalBytes),
					attribute.Int("trimmed_bytes", trimMeta.TrimmedBytes),
					attribute.Int("fields_kept", trimMeta.FieldsKept),
					attribute.Int("fields_dropped", trimMeta.FieldsDropped),
				}
				if trimMeta.BackfilledCount > 0 {
					attrs = append(attrs, attribute.Int("backfilled_count", trimMeta.BackfilledCount))
				}
				if trimMeta.ThresholdSkipped > 0 {
					attrs = append(attrs, attribute.Int("threshold_skipped", trimMeta.ThresholdSkipped))
				}
				if trimMeta.BudgetAllocated > 0 {
					attrs = append(attrs, attribute.Int("budget_allocated", trimMeta.BudgetAllocated))
				}
				if len(trimMeta.Keywords) > 0 {
					attrs = append(attrs, attribute.String("keywords", strings.Join(trimMeta.Keywords, ",")))
				}
				if len(trimMeta.MatchedPaths) > 0 {
					attrs = append(attrs, attribute.String("matched_paths", strings.Join(trimMeta.MatchedPaths, ",")))
				}
				// Phase 16 coverage fields — surfaced so Jaeger can distinguish a 28%-seen
				// distill from a 100%-seen one without digging into step metadata.
				if trimMeta.SourceCoverageRatio > 0 {
					attrs = append(attrs, attribute.Float64("source_coverage_ratio", trimMeta.SourceCoverageRatio))
				}
				if trimMeta.LLMInputBytes > 0 {
					attrs = append(attrs, attribute.Int("llm_input_bytes", trimMeta.LLMInputBytes))
				}
				if trimMeta.SegmentsTotal > 0 {
					attrs = append(attrs,
						attribute.Int("segments_analyzed", trimMeta.SegmentsAnalyzed),
						attribute.Int("segments_total", trimMeta.SegmentsTotal))
				}
				if trimMeta.PartialCoverage {
					attrs = append(attrs, attribute.Bool("partial_coverage", true))
				}
				if trimMeta.CombineTruncated {
					attrs = append(attrs, attribute.Bool("combine_truncated", true))
				}
				telemetry.AddSpanEvent(ctx, "result_trim.completed", attrs...)
			}

			if s.logger != nil && lossyTrimEvent(trimMeta, originalSize, trimmedSize) {
				s.logger.DebugWithContext(ctx, "Result trimmed for synthesis prompt", map[string]interface{}{
					"operation":        "result_trim",
					"request_id":       promptRequestID,
					"step_id":          step.StepID,
					"agent_name":       step.AgentName,
					"original_bytes":   originalSize,
					"trimmed_bytes":    trimmedSize,
					"budget_allocated": useBudgetAlloc,
				})
			}

			// Format response: pretty-print JSON, or use plain text as-is.
			// UseNumber so large IDs survive this last re-parse before the synthesis prompt.
			if parsed, perr := unmarshalPreservingNumbers([]byte(response)); perr == nil {
				parsed = deserializeStringValues(parsed) // Fix double-escaping
				if formatted, err := json.MarshalIndent(parsed, "", "  "); err == nil {
					response = string(formatted)
				}
			}

			fmt.Fprintf(&builder, "<agent name=%q task=%q status=\"success\">\n%s\n</agent>\n\n", step.AgentName, step.Instruction, response)
		} else {
			fmt.Fprintf(&builder, "<agent name=%q task=%q status=\"failed\">\n%s\n</agent>\n\n", step.AgentName, step.Instruction, step.Error)
		}
	}

	builder.WriteString("</agent_responses>\n\n")

	// ORCH-018: clarification-aware section. Only present when the planner
	// emitted needs_user_input and the phase loop short-circuited. The
	// synthesisSystemPromptFor() helper has already added <clarification_mode>
	// to the system prompt, so this section just supplies the structured data.
	if results.ClarificationNeeded != nil {
		cr := results.ClarificationNeeded
		builder.WriteString("<clarification_needed>\n")
		fmt.Fprintf(&builder, "Question to ask the user: %s\n", cr.Question)
		if len(cr.MissingFields) > 0 {
			fmt.Fprintf(&builder, "Missing fields: %s\n", strings.Join(cr.MissingFields, ", "))
		}
		if cr.PartialProgress != "" {
			fmt.Fprintf(&builder, "Partial progress to mention: %s\n", cr.PartialProgress)
		}
		builder.WriteString("</clarification_needed>\n\n")
	}

	builder.WriteString("Synthesize the above into a helpful answer.")

	prompt := builder.String()

	// Emit synthesis prompt size metric
	if registry := core.GetGlobalMetricsRegistry(); registry != nil {
		registry.Histogram("orchestration.synthesis_prompt.size_bytes", float64(len(prompt)))
	}

	return prompt
}

// synthesizeWithTemplate uses predefined templates for synthesis
func (s *AISynthesizer) synthesizeWithTemplate(request string, results *ExecutionResult) (string, error) {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Response to: %s\n\n", request)

	// Group results by success/failure
	var successful, failed []StepResult
	for _, step := range results.Steps {
		if step.Success {
			successful = append(successful, step)
		} else {
			failed = append(failed, step)
		}
	}

	// Present successful results
	if len(successful) > 0 {
		builder.WriteString("Results:\n")
		for _, step := range successful {
			fmt.Fprintf(&builder, "\n%s:\n", step.AgentName)

			// Try to parse and present JSON nicely (UseNumber preserves large IDs)
			if data, err := unmarshalPreservingNumbers([]byte(step.Response)); err == nil {
				formatted, _ := json.MarshalIndent(data, "  ", "  ")
				builder.WriteString(string(formatted))
			} else {
				fmt.Fprintf(&builder, "  %s", step.Response)
			}
			builder.WriteString("\n")
		}
	}

	// Note any failures
	if len(failed) > 0 {
		builder.WriteString("\nNote: Some agents encountered errors:\n")
		for _, step := range failed {
			fmt.Fprintf(&builder, "- %s: %s\n", step.AgentName, step.Error)
		}
	}

	// Summary
	fmt.Fprintf(&builder, "\nCompleted %d of %d tasks successfully.\n",
		len(successful), len(results.Steps))

	return builder.String(), nil
}

// synthesizeSimple concatenates responses
func (s *AISynthesizer) synthesizeSimple(results *ExecutionResult) (string, error) {
	var responses []string

	for _, step := range results.Steps {
		if step.Success {
			responses = append(responses, fmt.Sprintf("%s: %s", step.AgentName, step.Response))
		}
	}

	if len(responses) == 0 {
		return "No successful responses to synthesize", nil
	}

	return strings.Join(responses, "\n\n"), nil
}

// SetStrategy sets the synthesis strategy
func (s *AISynthesizer) SetStrategy(strategy SynthesisStrategy) {
	s.strategy = strategy
}

// SetLogger sets the logger for the synthesizer.
func (s *AISynthesizer) SetLogger(logger core.Logger) {
	if logger == nil {
		s.logger = nil
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			s.logger = cal.WithComponent("framework/orchestration")
		} else {
			s.logger = logger
		}
	}
}

// SetLLMDebugStore sets the LLM debug store for full payload visibility.
func (s *AISynthesizer) SetLLMDebugStore(store LLMDebugStore) {
	s.debugStore = store
}

// SetResultProcessor sets the result processor for trimming step results.
func (s *AISynthesizer) SetResultProcessor(processor ResultProcessor) {
	s.resultProcessor = processor
}

// SetResultTrimConfig sets the result trim configuration.
func (s *AISynthesizer) SetResultTrimConfig(config *ResultTrimConfig) {
	s.resultTrimConfig = config
}

// SetAIOptionsOverride sets the per-phase AI options override for synthesis calls.
func (s *AISynthesizer) SetAIOptionsOverride(opts *AIOptionsOverride) {
	s.aiOptionsOverride = opts
	merged := mergeAIOptions(&core.AIOptions{
		Temperature: 0.5,
		MaxTokens:   5000,
	}, opts)
	s.synthesisTemperature = roundLegacyFloat(float64(merged.Temperature))
	s.synthesisMaxTokens = merged.MaxTokens
	s.model = merged.Model
}

// Deprecated compatibility setters kept while tests/examples migrate.
func (s *AISynthesizer) SetSynthesisTemperature(temp float64) {
	if temp < 0 || temp > 2.0 {
		return
	}
	s.synthesisTemperature = roundLegacyFloat(temp)
	if s.aiOptionsOverride == nil {
		s.aiOptionsOverride = &AIOptionsOverride{}
	}
	s.aiOptionsOverride.Temperature = Float32Ptr(float32(temp))
}

func (s *AISynthesizer) SetSynthesisMaxTokens(maxTokens int) {
	if maxTokens <= 0 {
		return
	}
	s.synthesisMaxTokens = maxTokens
	if s.aiOptionsOverride == nil {
		s.aiOptionsOverride = &AIOptionsOverride{}
	}
	s.aiOptionsOverride.MaxTokens = IntPtr(maxTokens)
}

func (s *AISynthesizer) SetSynthesisModel(model string) {
	s.model = model
	if s.aiOptionsOverride == nil {
		s.aiOptionsOverride = &AIOptionsOverride{}
	}
	s.aiOptionsOverride.Model = StringPtr(model)
}

// recordDebugInteraction stores an LLM interaction for debugging.
// Runs asynchronously to avoid blocking synthesis. Errors are logged, not propagated.
// Uses WaitGroup to track in-flight recordings for graceful shutdown.
// This follows FRAMEWORK_DESIGN_PRINCIPLES.md: Resilient Runtime Behavior.
func (s *AISynthesizer) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
	if s.debugStore == nil {
		return
	}

	// Derive context-precedence audit synchronously; mirrors the
	// orchestrator's recordDebugInteraction. Self-gated on tag presence
	// so non-synthesis interactions recorded here stay clean.
	if interaction.PrecedenceAudit == nil {
		interaction.PrecedenceAudit = DerivePrecedenceAudit(ctx, interaction, s.precedenceEntityExtractor)
	}

	// Track this goroutine for graceful shutdown
	s.debugWg.Add(1)

	// Run async to avoid blocking synthesis
	go func() {
		defer s.debugWg.Done()

		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()

		if err := s.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			// Log but don't fail - debug is observability, not critical path
			if s.logger != nil {
				s.logger.Warn("Failed to record LLM debug interaction", map[string]interface{}{
					"request_id": requestID,
					"type":       interaction.Type,
					"error":      err.Error(),
				})
			}
		}
	}()
}

// generateFallbackRequestID generates a request ID when TraceID is not available.
func (s *AISynthesizer) generateFallbackRequestID() string {
	seq := s.debugSeqID.Add(1)
	return fmt.Sprintf("synth-%d-%d", time.Now().UnixNano(), seq)
}

// Shutdown waits for pending debug recordings to complete.
func (s *AISynthesizer) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.debugWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SimpleSynthesizer provides basic synthesis without AI
type SimpleSynthesizer struct {
	strategy SynthesisStrategy
}

// NewSynthesizer creates a new synthesizer (backward compatibility)
func NewSynthesizer() *SimpleSynthesizer {
	return &SimpleSynthesizer{
		strategy: StrategySimple,
	}
}

// Synthesize combines agent responses (simple version)
func (s *SimpleSynthesizer) Synthesize(ctx context.Context, request string, results *ExecutionResult) (string, error) {
	switch s.strategy {
	case StrategyTemplate:
		return s.synthesizeWithTemplate(request, results)
	case StrategySimple:
		return s.synthesizeSimple(results)
	default:
		return s.synthesizeSimple(results)
	}
}

// synthesizeWithTemplate uses predefined templates
func (s *SimpleSynthesizer) synthesizeWithTemplate(request string, results *ExecutionResult) (string, error) {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Response to: %s\n\n", request)

	for _, step := range results.Steps {
		if step.Success {
			fmt.Fprintf(&builder, "%s completed successfully:\n%s\n\n",
				step.AgentName, step.Response)
		} else {
			fmt.Fprintf(&builder, "%s failed: %s\n\n",
				step.AgentName, step.Error)
		}
	}

	return builder.String(), nil
}

// synthesizeSimple concatenates responses
func (s *SimpleSynthesizer) synthesizeSimple(results *ExecutionResult) (string, error) {
	var responses []string

	for _, step := range results.Steps {
		if step.Success {
			responses = append(responses, step.Response)
		}
	}

	return strings.Join(responses, "\n"), nil
}

// SetStrategy sets the synthesis strategy
func (s *SimpleSynthesizer) SetStrategy(strategy SynthesisStrategy) {
	s.strategy = strategy
}
