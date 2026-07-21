// Package orchestration provides intelligent error analysis for multi-step workflows.
//
// # LLM-First Error Analysis Design
//
// This file implements the ErrorAnalyzer which uses LLM to determine if an error
// can be fixed with different parameters. This removes the burden from tool developers
// to set Retryable flags - the LLM decides based on error context.
//
// HTTP Status Code Routing:
//   - 400, 404, 409, 422 → LLM Error Analyzer (might be fixable with different input)
//   - 408, 429, 5xx      → Resilience module (same payload + exponential backoff)
//   - 401, 403, 405      → Fail immediately (auth/permission issues)
//
// See PARAMETER_BINDING_FIX.md for the complete design rationale.
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

// ErrorAnalyzer uses LLM to determine if an error is fixable with different parameters.
// This removes the burden from tool developers to set Retryable flags.
type ErrorAnalyzer struct {
	aiClient          core.AIClient
	logger            core.Logger
	aiOptionsOverride *AIOptionsOverride

	// Configuration
	enabled bool // Whether LLM analysis is enabled (default: true)

	// LLM Debug Store for full payload visibility
	debugStore LLMDebugStore
	debugWg    sync.WaitGroup
	debugSeqID atomic.Uint64
}

// ErrorAnalyzerOption configures an ErrorAnalyzer
type ErrorAnalyzerOption func(*ErrorAnalyzer)

// WithErrorAnalysisEnabled enables or disables LLM error analysis
func WithErrorAnalysisEnabled(enabled bool) ErrorAnalyzerOption {
	return func(e *ErrorAnalyzer) {
		e.enabled = enabled
	}
}

// ErrorAnalysisResult contains the LLM's decision about error retryability
type ErrorAnalysisResult struct {
	// ShouldRetry indicates if the request should be retried with different parameters
	ShouldRetry bool `json:"should_retry"`

	// Reason explains why the error is or isn't retryable
	Reason string `json:"reason"`

	// SuggestedChanges contains parameter modifications that might fix the error
	// Keys are parameter names, values are the suggested new values
	SuggestedChanges map[string]interface{} `json:"suggested_changes,omitempty"`

	// IsTransientError indicates this is a temporary infrastructure issue (timeout, service unavailable)
	// that should be retried with the SAME parameters using exponential backoff.
	// When true and ShouldRetry is false, the executor should continue with resilience retry.
	IsTransientError bool `json:"is_transient_error,omitempty"`
}

// ErrorContext provides context for error analysis
type ErrorAnalysisContext struct {
	// HTTPStatus is the HTTP status code from the tool response
	HTTPStatus int

	// ErrorResponse is the raw error response body from the tool
	ErrorResponse string

	// OriginalRequest contains the parameters that were sent to the tool
	OriginalRequest map[string]interface{}

	// UserQuery is the original user request (provides intent context)
	UserQuery string

	// CapabilityName is the name of the capability that failed
	CapabilityName string

	// CapabilityDescription describes what the capability does
	CapabilityDescription string

	// StepID identifies the DAG step that triggered the error analysis
	StepID string
}

// NewErrorAnalyzer creates a new error analyzer
func NewErrorAnalyzer(aiClient core.AIClient, logger core.Logger, opts ...ErrorAnalyzerOption) *ErrorAnalyzer {
	e := &ErrorAnalyzer{
		aiClient: aiClient,
		logger:   logger,
		enabled:  true, // Default: enabled
	}

	for _, opt := range opts {
		opt(e)
	}

	return e
}

// SetAIOptionsOverride sets the per-phase AI options override for error analysis calls.
func (e *ErrorAnalyzer) SetAIOptionsOverride(opts *AIOptionsOverride) {
	e.aiOptionsOverride = opts
}

// AnalyzeError determines if an error can be fixed with different parameters.
// Returns nil if the error should be handled by the resilience module (transient errors).
// Returns a result with ShouldRetry=false for permanent failures.
// Returns a result with ShouldRetry=true and SuggestedChanges for fixable errors.
func (e *ErrorAnalyzer) AnalyzeError(ctx context.Context, errCtx *ErrorAnalysisContext) (*ErrorAnalysisResult, error) {
	if errCtx == nil {
		return nil, fmt.Errorf("error context is required")
	}

	// Layer 1: HTTP status heuristics (no LLM call needed)
	result := e.routeByHTTPStatus(errCtx.HTTPStatus)
	if result != nil {
		e.logDebug("Error routed by HTTP status", map[string]interface{}{
			"http_status":  errCtx.HTTPStatus,
			"should_retry": result.ShouldRetry,
			"reason":       result.Reason,
		})
		return result, nil
	}

	// HTTP status indicates this should go to resilience module
	if e.shouldDelegateToResilience(errCtx.HTTPStatus) {
		e.logDebug("Error delegated to resilience module", map[string]interface{}{
			"http_status": errCtx.HTTPStatus,
		})
		return nil, nil // nil means "let resilience module handle it"
	}

	// Layer 2: LLM analyzes the error
	if !e.enabled || e.aiClient == nil {
		e.logDebug("LLM error analysis disabled or unavailable", map[string]interface{}{
			"enabled":  e.enabled,
			"aiClient": e.aiClient != nil,
		})
		return &ErrorAnalysisResult{
			ShouldRetry: false,
			Reason:      "LLM error analysis not available",
		}, nil
	}

	return e.analyzeWithLLM(ctx, errCtx)
}

// routeByHTTPStatus returns an immediate result for status codes that don't need LLM analysis.
// Returns nil if LLM analysis is needed or if resilience module should handle.
func (e *ErrorAnalyzer) routeByHTTPStatus(status int) *ErrorAnalysisResult {
	switch status {
	case 401:
		return &ErrorAnalysisResult{
			ShouldRetry: false,
			Reason:      "Authentication failed (401 Unauthorized) - requires valid credentials",
		}
	case 403:
		return &ErrorAnalysisResult{
			ShouldRetry: false,
			Reason:      "Access denied (403 Forbidden) - permission issue, not fixable with different parameters",
		}
	case 405:
		return &ErrorAnalysisResult{
			ShouldRetry: false,
			Reason:      "Method not allowed (405) - framework or tool configuration issue",
		}
	}
	return nil // Needs further analysis
}

// shouldDelegateToResilience returns true if the error should be handled by the resilience module
// (same payload, exponential backoff).
// Note: 503 errors with structured tool responses (retryable: true) are now analyzed by LLM
// to potentially suggest parameter corrections. True service outages will still be identified
// by LLM as non-fixable.
func (e *ErrorAnalyzer) shouldDelegateToResilience(status int) bool {
	switch status {
	case 408, 429, 500, 502, 504:
		return true
		// Note: 503 is intentionally NOT included here
		// Tool responses with 503 often contain semantic errors (e.g., "location not found")
		// that LLM can help fix by suggesting corrected parameters
	}
	return false
}

// analyzeWithLLM uses LLM to determine if the error is fixable with different parameters.
func (e *ErrorAnalyzer) analyzeWithLLM(ctx context.Context, errCtx *ErrorAnalysisContext) (*ErrorAnalysisResult, error) {
	// Check context before expensive LLM operation
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Extract request_id for debug store correlation
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = e.generateFallbackRequestID()
	}

	// Add span event to mark LLM analysis start (uses existing span from context)
	telemetry.AddSpanEvent(ctx, "error_analyzer.llm_call_start",
		attribute.String("capability", errCtx.CapabilityName),
		attribute.Int("http_status", errCtx.HTTPStatus),
	)

	prompt := e.buildAnalysisPrompt(errCtx)

	e.logInfo("Analyzing error with LLM", map[string]interface{}{
		"capability":  errCtx.CapabilityName,
		"http_status": errCtx.HTTPStatus,
	})

	// Track LLM call latency
	llmStart := time.Now()

	// Capture options as a named pointer: all clients resolve options.Model in-place
	// at the start of GenerateResponse (before the network call), so opts.Model is
	// populated with the actual resolved model name even when the call fails.
	opts := mergeAIOptions(&core.AIOptions{
		Temperature: 0.0, // Deterministic for analysis
		MaxTokens:   500,
	}, e.aiOptionsOverride)
	resp, _, err := invokeAI(ctx, e.aiClient, aiInvocation{
		Purpose:        "error-analysis",
		Prompt:         prompt,
		Options:        opts,
		DeferRecording: e.debugStore != nil,
	})
	if err == nil {
		core.RecordTokenUsage(ctx, "error_analysis", resp.Usage)
	}

	llmDuration := time.Since(llmStart)

	// Record LLM latency histogram
	telemetry.Histogram("orchestration.error_analyzer.llm_latency_ms",
		float64(llmDuration.Milliseconds()),
		"capability", errCtx.CapabilityName,
		"module", telemetry.ModuleOrchestration,
	)

	if err != nil {
		telemetry.AddSpanEvent(ctx, "error_analyzer.llm_call_failed",
			attribute.String("error", err.Error()),
			attribute.String("capability", errCtx.CapabilityName),
		)
		telemetry.Counter("orchestration.error_analyzer.llm_errors",
			"capability", errCtx.CapabilityName,
			"module", telemetry.ModuleOrchestration,
		)

		e.logWarn("LLM error analysis failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": llmDuration.Milliseconds(),
		})

		// LLM Debug: Record failed error analysis attempt.
		// opts.Model is populated by the client's ApplyDefaults even on failure.
		// Provider is not recoverable without an AIClient.GetProvider() interface method.
		e.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:            "error_analysis",
			Timestamp:       llmStart,
			DurationMs:      llmDuration.Milliseconds(),
			Prompt:          prompt,
			Temperature:     0.0,
			MaxTokens:       500,
			Model:           opts.Model,
			Success:         false,
			Error:           err.Error(),
			StepID:          errCtx.StepID,
			CallDescription: fmt.Sprintf("Error analysis for %s (HTTP %d)", errCtx.CapabilityName, errCtx.HTTPStatus),
		})

		return nil, fmt.Errorf("LLM error analysis failed: %w", err)
	}

	// Add success event with duration
	telemetry.AddSpanEvent(ctx, "error_analyzer.llm_call_completed",
		attribute.Int64("duration_ms", llmDuration.Milliseconds()),
		attribute.String("capability", errCtx.CapabilityName),
	)

	result, err := e.parseAnalysisResponse(resp.Content)
	if err != nil {
		telemetry.AddSpanEvent(ctx, "error_analyzer.parse_failed",
			attribute.String("error", err.Error()),
		)

		e.logWarn("Failed to parse LLM analysis response", map[string]interface{}{
			"error":    err.Error(),
			"response": truncateString(resp.Content, 200),
		})

		// LLM Debug: Record parse-failure — the LLM responded but output was malformed
		e.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:             "error_analysis",
			Timestamp:        llmStart,
			DurationMs:       llmDuration.Milliseconds(),
			Prompt:           prompt,
			Temperature:      0.0,
			MaxTokens:        500,
			Model:            resp.Model,
			Provider:         resp.Provider,
			Response:         resp.Content,
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.PromptTokens + resp.Usage.CompletionTokens,
			Success:          false,
			Error:            fmt.Sprintf("parse failure: %s", err.Error()),
			StepID:           errCtx.StepID,
			CallDescription:  fmt.Sprintf("Error analysis for %s (HTTP %d)", errCtx.CapabilityName, errCtx.HTTPStatus),
		})

		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// Record analysis outcome as span event
	telemetry.AddSpanEvent(ctx, "error_analyzer.analysis_complete",
		attribute.Bool("should_retry", result.ShouldRetry),
		attribute.String("reason", result.Reason),
		attribute.Int("suggested_changes_count", len(result.SuggestedChanges)),
		attribute.Int64("total_duration_ms", llmDuration.Milliseconds()),
	)

	e.logInfo("LLM error analysis completed", map[string]interface{}{
		"capability":   errCtx.CapabilityName,
		"should_retry": result.ShouldRetry,
		"reason":       result.Reason,
		"has_changes":  len(result.SuggestedChanges) > 0,
		"duration_ms":  llmDuration.Milliseconds(),
	})

	// LLM Debug: Record successful error analysis
	e.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "error_analysis",
		Timestamp:        llmStart,
		DurationMs:       llmDuration.Milliseconds(),
		Prompt:           prompt,
		Temperature:      0.0,
		MaxTokens:        500,
		Model:            resp.Model,
		Provider:         resp.Provider,
		Response:         resp.Content,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.PromptTokens + resp.Usage.CompletionTokens,
		Success:          true,
		StepID:           errCtx.StepID,
		CallDescription:  fmt.Sprintf("Error analysis for %s (HTTP %d)", errCtx.CapabilityName, errCtx.HTTPStatus),
	})

	return result, nil
}

// buildAnalysisPrompt creates the prompt for LLM error analysis.
func (e *ErrorAnalyzer) buildAnalysisPrompt(errCtx *ErrorAnalysisContext) string {
	requestJSON, _ := json.MarshalIndent(errCtx.OriginalRequest, "", "  ")

	return fmt.Sprintf(`<identity>
You are an error analysis assistant that determines whether a failed tool call can be fixed by modifying its request parameters.
</identity>

<instructions>
1. Determine if the error can be fixed by changing the request parameters
2. If fixable, suggest the specific parameter changes
3. Identify transient errors (timeouts, "request canceled", service unavailable, connection refused, rate limiting, 503) by setting is_transient_error=true — these are temporary issues where retrying with the same parameters might succeed
4. Suggest retry only when the error indicates a correctable input issue: a likely typo (e.g., "Tokio" instead of "Tokyo"), invalid format (e.g., wrong date format, negative amount), or a value that can be inferred from context
5. Classify as non-retryable when the error is about permissions, authentication, or a genuinely non-existent resource
</instructions>

<capability name="%s">
%s
</capability>

<original_request>
%s
</original_request>

<error_response status="%d">
%s
</error_response>

<user_query>
%s
</user_query>

Return a JSON analysis. Output raw JSON — no markdown, no code blocks. Start with { and end with }.
{"should_retry": true/false, "reason": "...", "suggested_changes": {...}, "is_transient_error": true/false}`,
		errCtx.CapabilityName,
		errCtx.CapabilityDescription,
		string(requestJSON),
		errCtx.HTTPStatus,
		errCtx.ErrorResponse,
		errCtx.UserQuery,
	)
}

// parseAnalysisResponse parses the LLM's JSON response into an ErrorAnalysisResult.
func (e *ErrorAnalyzer) parseAnalysisResponse(content string) (*ErrorAnalysisResult, error) {
	// Clean up the response (handle markdown, extra text, etc.)
	content = strings.TrimSpace(content)

	// Remove markdown code blocks if present
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	// Find JSON object
	jsonStart := strings.Index(content, "{")
	if jsonStart == -1 {
		return nil, fmt.Errorf("no JSON object found in response")
	}

	jsonEnd := findJSONEndSimple(content, jsonStart)
	if jsonEnd == -1 {
		return nil, fmt.Errorf("invalid JSON structure in response")
	}

	jsonStr := content[jsonStart:jsonEnd]

	var result ErrorAnalysisResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &result, nil
}

// findJSONEndSimple finds the end of a JSON object starting at the given position.
func findJSONEndSimple(s string, start int) int {
	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		c := s[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}

	return -1
}

// SetLogger sets the logger for the error analyzer.
// The component is always set to "framework/orchestration" to ensure proper log attribution.
func (e *ErrorAnalyzer) SetLogger(logger core.Logger) {
	if logger == nil {
		e.logger = nil
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			e.logger = cal.WithComponent("framework/orchestration")
		} else {
			e.logger = logger
		}
	}
}

// Enable enables or disables LLM error analysis.
func (e *ErrorAnalyzer) Enable(enabled bool) {
	e.enabled = enabled
}

// IsEnabled returns whether LLM error analysis is enabled.
func (e *ErrorAnalyzer) IsEnabled() bool {
	return e.enabled && e.aiClient != nil
}

// Logging helpers
func (e *ErrorAnalyzer) logDebug(msg string, fields map[string]interface{}) {
	if e.logger != nil {
		e.logger.Debug(msg, fields)
	}
}

func (e *ErrorAnalyzer) logInfo(msg string, fields map[string]interface{}) {
	if e.logger != nil {
		e.logger.Info(msg, fields)
	}
}

func (e *ErrorAnalyzer) logWarn(msg string, fields map[string]interface{}) {
	if e.logger != nil {
		e.logger.Warn(msg, fields)
	}
}

// SetLLMDebugStore sets the LLM debug store for full payload visibility.
func (e *ErrorAnalyzer) SetLLMDebugStore(store LLMDebugStore) {
	e.debugStore = store
}

// recordDebugInteraction stores an LLM interaction for debugging.
// Runs asynchronously to avoid blocking error analysis. Errors are logged, not propagated.
// Uses WaitGroup to track in-flight recordings for graceful shutdown.
func (e *ErrorAnalyzer) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
	if e.debugStore == nil {
		return
	}

	// Track this goroutine for graceful shutdown
	e.debugWg.Add(1)

	go func() {
		defer e.debugWg.Done()

		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()

		if err := e.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			if e.logger != nil {
				e.logger.Warn("Failed to record error analysis LLM debug interaction", map[string]interface{}{
					"request_id": requestID,
					"type":       interaction.Type,
					"error":      err.Error(),
				})
			}
		}
	}()
}

// generateFallbackRequestID generates a request ID when TraceID is not available.
func (e *ErrorAnalyzer) generateFallbackRequestID() string {
	seq := e.debugSeqID.Add(1)
	return fmt.Sprintf("erranalyzer-%d-%d", time.Now().UnixNano(), seq)
}

// Shutdown waits for pending debug recordings to complete.
func (e *ErrorAnalyzer) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		e.debugWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// truncateString is defined in orchestrator.go
