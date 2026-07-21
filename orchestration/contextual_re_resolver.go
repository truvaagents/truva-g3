// Package orchestration provides intelligent parameter binding for multi-step workflows.
//
// This file implements Layer 4: Contextual Re-Resolution for semantic retry.
// When ErrorAnalyzer says "cannot fix" but source data exists, this component
// can compute derived values using the full execution context.
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

// ExecutionContext captures all information needed for semantic retry.
// This is the "full trajectory" that enables intelligent re-resolution.
type ExecutionContext struct {
	// Original user intent (critical for understanding what to compute)
	UserQuery string

	// All source data from dependent steps (what MicroResolver had)
	SourceData map[string]interface{}

	// Step being executed
	StepID     string
	Capability *EnhancedCapability

	// What we tried (failed parameters)
	AttemptedParams map[string]interface{}

	// What went wrong
	ErrorResponse string
	HTTPStatus    int

	// Retry state (memory across attempts)
	RetryCount     int
	PreviousErrors []string
}

// ReResolutionResult is returned by ContextualReResolver
type ReResolutionResult struct {
	// Should we retry with corrected parameters?
	ShouldRetry bool `json:"should_retry"`

	// Corrected parameters to use for retry
	CorrectedParameters map[string]interface{} `json:"corrected_parameters"`

	// Explanation of what was fixed (for logging/debugging)
	Analysis string `json:"analysis"`
}

// ContextualReResolver combines error analysis with parameter re-resolution.
// Unlike ErrorAnalyzer (which only analyzes), this component can PRESCRIBE fixes
// because it has access to the full execution context including source data.
type ContextualReResolver struct {
	aiClient core.AIClient
	logger   core.Logger

	// Source data trimming (prevents prompt overflow on large dependency results)
	resultProcessor ResultProcessor
	maxSourceBytes  int

	// Per-phase AI options override for semantic retry calls.
	aiOptionsOverride *AIOptionsOverride
	model             string

	// LLM Debug Store for full payload visibility
	debugStore LLMDebugStore
	debugWg    sync.WaitGroup
	debugSeqID atomic.Uint64
}

// NewContextualReResolver creates a new contextual re-resolver
func NewContextualReResolver(aiClient core.AIClient, logger core.Logger) *ContextualReResolver {
	r := &ContextualReResolver{
		aiClient: aiClient,
		logger:   logger,
	}
	return r
}

// SetAIOptionsOverride sets the per-phase AI options override for semantic retry calls.
func (r *ContextualReResolver) SetAIOptionsOverride(opts *AIOptionsOverride) {
	r.aiOptionsOverride = opts
	merged := mergeAIOptions(&core.AIOptions{
		Temperature: 0.0,
		MaxTokens:   1000,
	}, opts)
	r.model = merged.Model
}

// Deprecated compatibility setter kept while tests/examples migrate.
func (r *ContextualReResolver) SetModel(model string) {
	r.model = model
	if r.aiOptionsOverride == nil {
		r.aiOptionsOverride = &AIOptionsOverride{}
	}
	r.aiOptionsOverride.Model = StringPtr(model)
}

// ReResolve attempts to resolve parameters after an execution failure.
// It uses the full execution context to compute corrected parameters.
func (r *ContextualReResolver) ReResolve(
	ctx context.Context,
	execCtx *ExecutionContext,
) (*ReResolutionResult, error) {
	if execCtx == nil {
		return nil, fmt.Errorf("execution context is required")
	}

	if r.aiClient == nil {
		return &ReResolutionResult{
			ShouldRetry: false,
			Analysis:    "AI client not configured for semantic retry",
		}, nil
	}

	// Check context before expensive LLM operation
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Build comprehensive prompt with ALL context (source data trimmed if needed)
	prompt := r.buildReResolutionPrompt(ctx, execCtx)

	// Telemetry: Track re-resolution attempt
	reResEventAttrs := []attribute.KeyValue{
		attribute.String("step_id", execCtx.StepID),
		attribute.String("capability", execCtx.Capability.Name),
		attribute.Int("retry_count", execCtx.RetryCount),
		attribute.Int("http_status", execCtx.HTTPStatus),
		attribute.Int("source_data_keys", len(execCtx.SourceData)),
	}
	baseOpts := &core.AIOptions{
		Temperature: 0.0,
		MaxTokens:   1000,
	}
	reResolveOpts := mergeAIOptions(baseOpts, r.aiOptionsOverride)

	if reResolveOpts.Model != "" {
		reResEventAttrs = append(reResEventAttrs, attribute.String("model", reResolveOpts.Model))
	}
	telemetry.AddSpanEvent(ctx, "contextual_re_resolution.start", reResEventAttrs...)

	r.logInfo("Starting contextual re-resolution", map[string]interface{}{
		"step_id":          execCtx.StepID,
		"capability":       execCtx.Capability.Name,
		"http_status":      execCtx.HTTPStatus,
		"source_data_keys": getMapKeys(execCtx.SourceData),
	})

	// Get request ID from context baggage for debug correlation
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = r.generateFallbackRequestID()
	}

	startTime := time.Now()

	// LLM generates corrected parameters with reasoning
	response, _, err := invokeAI(ctx, r.aiClient, aiInvocation{
		Purpose:        "semantic-retry",
		Prompt:         prompt,
		Options:        reResolveOpts,
		DeferRecording: r.debugStore != nil,
	})
	if err == nil {
		core.RecordTokenUsage(ctx, "semantic_retry", response.Usage)
	}

	duration := time.Since(startTime)

	// Record LLM latency
	telemetry.Histogram("orchestration.semantic_retry.llm_latency_ms",
		float64(duration.Milliseconds()),
		"capability", execCtx.Capability.Name,
		"module", telemetry.ModuleOrchestration,
	)

	if err != nil {
		telemetry.AddSpanEvent(ctx, "contextual_re_resolution.error",
			attribute.String("error", err.Error()),
			attribute.Int64("duration_ms", duration.Milliseconds()),
		)
		telemetry.Counter("orchestration.semantic_retry.llm_errors",
			"capability", execCtx.Capability.Name,
			"module", telemetry.ModuleOrchestration,
		)

		// LLM Debug: Record failed semantic retry attempt
		r.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:        "semantic_retry",
			Timestamp:   startTime,
			DurationMs:  duration.Milliseconds(),
			Prompt:      prompt,
			Temperature: float64(reResolveOpts.Temperature),
			MaxTokens:   reResolveOpts.MaxTokens,
			Model:       reResolveOpts.Model,
			Success:     false,
			Error:       err.Error(),
			Attempt:     execCtx.RetryCount + 1,
			StepID:      execCtx.StepID,
		})

		r.logWarn("Re-resolution LLM call failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})
		return nil, fmt.Errorf("re-resolution LLM call failed: %w", err)
	}

	// LLM Debug: Record successful semantic retry
	r.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "semantic_retry",
		Timestamp:        startTime,
		DurationMs:       duration.Milliseconds(),
		Prompt:           prompt,
		Temperature:      float64(reResolveOpts.Temperature),
		MaxTokens:        reResolveOpts.MaxTokens,
		Model:            response.Model,
		Provider:         response.Provider,
		Response:         response.Content,
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
		TotalTokens:      response.Usage.TotalTokens,
		Success:          true,
		Attempt:          execCtx.RetryCount + 1,
		StepID:           execCtx.StepID,
	})

	// Parse structured response
	result, parseErr := r.parseReResolutionResponse(response.Content)
	if parseErr != nil {
		telemetry.AddSpanEvent(ctx, "contextual_re_resolution.parse_error",
			attribute.String("error", parseErr.Error()),
			attribute.String("response", truncateString(response.Content, 200)),
		)
		r.logWarn("Failed to parse re-resolution response", map[string]interface{}{
			"error":    parseErr.Error(),
			"response": truncateString(response.Content, 200),
		})
		return nil, fmt.Errorf("failed to parse re-resolution response: %w", parseErr)
	}

	// Telemetry: Track result
	telemetry.AddSpanEvent(ctx, "contextual_re_resolution.complete",
		attribute.Bool("should_retry", result.ShouldRetry),
		attribute.String("analysis", truncateString(result.Analysis, 200)),
		attribute.Int("corrected_params_count", len(result.CorrectedParameters)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	if result.ShouldRetry {
		telemetry.Counter("orchestration.semantic_retry.success",
			"capability", execCtx.Capability.Name,
			"module", telemetry.ModuleOrchestration,
		)
	} else {
		telemetry.Counter("orchestration.semantic_retry.cannot_fix",
			"capability", execCtx.Capability.Name,
			"module", telemetry.ModuleOrchestration,
		)
	}

	r.logInfo("Contextual re-resolution completed", map[string]interface{}{
		"step_id":          execCtx.StepID,
		"capability":       execCtx.Capability.Name,
		"should_retry":     result.ShouldRetry,
		"analysis":         result.Analysis,
		"corrected_params": result.CorrectedParameters,
		"duration_ms":      duration.Milliseconds(),
	})

	return result, nil
}

// buildReResolutionPrompt creates the domain-agnostic prompt for re-resolution.
// The framework provides ALL available context and lets the LLM figure out
// what computation (if any) is needed.
// Source data is trimmed via ResultProcessor when it exceeds the configured budget.
func (r *ContextualReResolver) buildReResolutionPrompt(ctx context.Context, execCtx *ExecutionContext) string {
	// Compact JSON for accurate budget accounting (indented JSON inflates size by ~30%,
	// causing false-positive trimming for responses in the 100-128KB range)
	sourceJSON, _ := json.Marshal(execCtx.SourceData)

	// Reserve space for the prompt template that wraps the source data.
	// The re-resolution template is larger than micro-resolution (~800+ bytes for
	// error context, capability schema, user query, previous errors), but
	// promptTemplateReserve (2 KB) covers it generously.
	effectiveBudget := r.maxSourceBytes - promptTemplateReserve
	if effectiveBudget < 1024 {
		effectiveBudget = 1024 // minimum reasonable budget
	}

	// Trim source data if it exceeds effective budget (prevents 429/400 on large dependency results)
	if r.resultProcessor != nil && effectiveBudget > 0 && len(sourceJSON) > effectiveBudget {
		// Build instruction from error + capability for keyword extraction
		instruction := fmt.Sprintf("Fix error for %s: %s", execCtx.Capability.Name, execCtx.ErrorResponse)
		trimmed := r.resultProcessor.ProcessForPrompt(ctx, string(sourceJSON), effectiveBudget, ResultProcessorContext{
			StepID:      execCtx.StepID,
			AgentName:   "contextual_re_resolver",
			Instruction: instruction,
		})

		requestID := ""
		if bag := telemetry.GetBaggage(ctx); bag != nil {
			requestID = bag["request_id"]
		}
		telemetry.AddSpanEvent(ctx, "result_trim.semantic_retry",
			attribute.String("request_id", requestID),
			attribute.String("step_id", execCtx.StepID),
			attribute.String("capability", execCtx.Capability.Name),
			attribute.Int("original_bytes", len(sourceJSON)),
			attribute.Int("trimmed_bytes", len(trimmed)),
			attribute.Int("configured_budget_bytes", r.maxSourceBytes),
			attribute.Int("effective_budget_bytes", effectiveBudget),
		)

		sourceJSON = []byte(trimmed)
	}

	failedJSON, _ := json.MarshalIndent(execCtx.AttemptedParams, "", "  ")

	// Build parameter schema description
	var paramDescs []string
	for _, p := range execCtx.Capability.Parameters {
		required := ""
		if p.Required {
			required = " (required)"
		}
		paramDescs = append(paramDescs, fmt.Sprintf("- %s (%s%s): %s",
			p.Name, p.Type, required, p.Description))
	}

	// Include previous errors if this is a retry of a retry
	previousContext := ""
	if len(execCtx.PreviousErrors) > 0 {
		previousContext = fmt.Sprintf("\n<previous_failed_attempts>\n%s\n</previous_failed_attempts>\n",
			strings.Join(execCtx.PreviousErrors, "\n---\n"))
	}

	return fmt.Sprintf(`<identity>
You are a parameter re-resolution assistant. Given a failed tool call, source data, and the user's intent, compute corrected parameters.
</identity>

<instructions>
1. Analyze the error message to understand what went wrong
2. Use the user request to understand the intended computation
3. Use the source data to find or derive values that fix the error
4. If the fix requires a calculation, combination, or transformation, perform it and provide the result
5. Return corrected_parameters as a complete parameters object matching the capability schema
</instructions>

<user_request>
%s
</user_request>

<source_data>
%s
</source_data>

<failed_attempt capability="%s" status="%d">
Parameters sent: %s
Error: %s
</failed_attempt>
%s
<capability_schema>
%s
</capability_schema>

Return a JSON result. Output raw JSON — no markdown, no code blocks. Start with { and end with }.
{"should_retry": true/false, "analysis": "...", "corrected_parameters": {...}}`,
		execCtx.UserQuery,
		string(sourceJSON),
		execCtx.Capability.Name,
		execCtx.HTTPStatus,
		string(failedJSON),
		execCtx.ErrorResponse,
		previousContext,
		strings.Join(paramDescs, "\n"),
	)
}

// parseReResolutionResponse parses the LLM's JSON response.
// Uses the same pattern as ErrorAnalyzer.parseAnalysisResponse.
func (r *ContextualReResolver) parseReResolutionResponse(content string) (*ReResolutionResult, error) {
	// Clean up the response (handle markdown, extra text, etc.)
	content = strings.TrimSpace(content)

	// Remove markdown code blocks if present
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	// Find JSON object (same logic as error_analyzer.go:328-337)
	jsonStart := strings.Index(content, "{")
	if jsonStart == -1 {
		return nil, fmt.Errorf("no JSON object found in response")
	}

	jsonEnd := findJSONEndSimple(content, jsonStart) // Reuse from error_analyzer.go
	if jsonEnd == -1 {
		return nil, fmt.Errorf("invalid JSON structure in response")
	}

	jsonStr := content[jsonStart:jsonEnd]

	var result ReResolutionResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Initialize empty map if nil
	if result.CorrectedParameters == nil {
		result.CorrectedParameters = make(map[string]interface{})
	}

	return &result, nil
}

// SetLogger sets the logger for the contextual re-resolver.
// The component is always set to "framework/orchestration" for proper attribution.
func (r *ContextualReResolver) SetLogger(logger core.Logger) {
	if logger == nil {
		r.logger = nil
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			r.logger = cal.WithComponent("framework/orchestration")
		} else {
			r.logger = logger
		}
	}
}

// SetResultProcessor configures source data trimming for re-resolution prompts.
// When set, source data exceeding maxBytes is trimmed before embedding in the prompt,
// preventing overflow errors (429/400) on large dependency results.
func (r *ContextualReResolver) SetResultProcessor(processor ResultProcessor, maxBytes int) {
	r.resultProcessor = processor
	r.maxSourceBytes = maxBytes
}

// Logging helpers
func (r *ContextualReResolver) logInfo(msg string, fields map[string]interface{}) {
	if r.logger != nil {
		r.logger.Info(msg, fields)
	}
}

func (r *ContextualReResolver) logWarn(msg string, fields map[string]interface{}) {
	if r.logger != nil {
		r.logger.Warn(msg, fields)
	}
}

// SetLLMDebugStore sets the LLM debug store for full payload visibility.
func (r *ContextualReResolver) SetLLMDebugStore(store LLMDebugStore) {
	r.debugStore = store
}

// recordDebugInteraction stores an LLM interaction for debugging.
// Uses WaitGroup to track in-flight recordings for graceful shutdown.
func (r *ContextualReResolver) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
	if r.debugStore == nil {
		return
	}

	r.debugWg.Add(1)
	go func() {
		defer r.debugWg.Done()

		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()

		if err := r.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			r.logWarn("Failed to record LLM debug interaction", map[string]interface{}{
				"request_id": requestID,
				"type":       interaction.Type,
				"error":      err.Error(),
			})
		}
	}()
}

// generateFallbackRequestID generates a request ID when TraceID is not available.
func (r *ContextualReResolver) generateFallbackRequestID() string {
	seq := r.debugSeqID.Add(1)
	return fmt.Sprintf("reresolver-%d-%d", time.Now().UnixNano(), seq)
}

// Shutdown waits for pending debug recordings to complete.
func (r *ContextualReResolver) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		r.debugWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
