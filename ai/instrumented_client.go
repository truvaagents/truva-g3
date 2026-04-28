package ai

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// InstrumentedAIClient wraps any core.AIClient and records every LLM call
// to a telemetry.LLMCallRecorder for debugging and observability.
//
// This follows the same decorator pattern as ChainClient (failover).
// The wrapper is transparent — callers see a standard core.AIClient.
//
// Goroutine safety: All async recordings are tracked via WaitGroup.
// Call Shutdown() before process exit to drain in-flight recordings.
type InstrumentedAIClient struct {
	wrapped       core.AIClient
	recorder      telemetry.LLMCallRecorder
	logger        core.Logger    // For Warn-level recording failure logs
	componentName string         // default source component name
	defaultType   string         // default call type (e.g., "agent_llm_call")
	debugWg       sync.WaitGroup // tracks in-flight async recordings
}

// InstrumentedOption configures an InstrumentedAIClient.
type InstrumentedOption func(*InstrumentedAIClient)

// WithComponentName sets the source component name for recordings.
func WithComponentName(name string) InstrumentedOption {
	return func(c *InstrumentedAIClient) { c.componentName = name }
}

// WithDefaultCallType overrides the default call type (default: "agent_llm_call").
func WithDefaultCallType(callType string) InstrumentedOption {
	return func(c *InstrumentedAIClient) { c.defaultType = callType }
}

// WithInstrumentedLogger sets the logger for recording failure warnings.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md: "Resilient runtime behavior" — log, don't fail.
// Named WithInstrumentedLogger to avoid collision with ai.WithLogger (AIOption).
func WithInstrumentedLogger(logger core.Logger) InstrumentedOption {
	return func(c *InstrumentedAIClient) { c.logger = logger }
}

func NewInstrumentedClient(client core.AIClient, recorder telemetry.LLMCallRecorder, opts ...InstrumentedOption) *InstrumentedAIClient {
	c := &InstrumentedAIClient{
		wrapped:     client,
		recorder:    recorder,
		defaultType: "agent_llm_call",
	}
	for _, opt := range opts {
		opt(c)
	}
	// Nil-safety: default to NoOp if recorder is nil (Issue 9)
	if c.recorder == nil {
		c.recorder = &telemetry.NoOpLLMCallRecorder{}
	}
	if c.logger == nil {
		c.logger = &core.NoOpLogger{}
	}
	// Issue 14 fix: Apply component-aware logger filtering per ai/ARCHITECTURE.md
	// Section 8 requires all AI module logs use "framework/ai" component.
	if cal, ok := c.logger.(core.ComponentAwareLogger); ok {
		c.logger = cal.WithComponent("framework/ai")
	}
	return c
}

// GenerateResponse calls the wrapped client and records the LLM call for debugging.
func (c *InstrumentedAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	start := time.Now()

	// Call the wrapped client — this is the critical path, never block it
	resp, err := c.wrapped.GenerateResponse(ctx, prompt, options)

	// Extract request_id — try OTel baggage first (if available), then explicit context key (from headers)
	requestID := c.resolveRequestID(ctx)
	if requestID == "" {
		return resp, err // Not called from orchestration — skip recording
	}

	// Skip recording when the caller has deferred it via
	// telemetry.WithLLMCallRecordingDeferred. The caller takes
	// responsibility for emitting the record themselves (orchestration's
	// direct recordDebugInteraction path, which carries richer metadata
	// like semantic type, attempt, hook phase). Calls without the marker
	// (reflection job, knowledge extraction hook, custom agent endpoints)
	// continue to be recorded here as agent_llm_call.
	//
	// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
	if telemetry.IsLLMCallRecordingDeferred(ctx) {
		return resp, err
	}

	// Build record
	record := telemetry.LLMCallRecord{
		CallType:        c.defaultType,
		SourceComponent: c.componentName,
		StepID:          c.resolveStepID(ctx),
		PhaseNumber:     c.resolvePhaseNumber(ctx),
		Timestamp:       start,
		DurationMs:      time.Since(start).Milliseconds(),
		Prompt:          prompt,
		Success:         err == nil,
	}
	// Nil-safe options access — options may be nil (matches ChainClient pattern)
	if options != nil {
		record.Temperature = float64(options.Temperature)
		record.MaxTokens = options.MaxTokens
		record.SystemPrompt = options.SystemPrompt
	}
	if resp != nil {
		record.Model = resp.Model
		record.Provider = resp.Provider
		record.Response = resp.Content
		record.PromptTokens = resp.Usage.PromptTokens
		record.CompletionTokens = resp.Usage.CompletionTokens
		record.TotalTokens = resp.Usage.TotalTokens
	}
	if err != nil {
		record.Error = err.Error()
		// Extract model/provider from structured error when response is nil
		// (works through ChainClient clone boundary via %w wrapping)
		if resp == nil {
			var pe core.ProviderError
			if errors.As(err, &pe) {
				record.Model = pe.Model()
				record.Provider = pe.Provider()
			}
		}
	}

	// Record asynchronously — never block the LLM call path
	c.recordAsync(ctx, requestID, record)

	return resp, err
}

// SetLogger updates the logger used for recording failure warnings.
// Called by core.applyConfigToComponent after framework initialization
// replaces the NoOpLogger with the real component logger.
func (c *InstrumentedAIClient) SetLogger(logger core.Logger) {
	c.logger = logger
	// Propagate to wrapped client too
	if loggable, ok := c.wrapped.(interface{ SetLogger(core.Logger) }); ok {
		loggable.SetLogger(logger)
	}
}

// Shutdown waits for all in-flight recordings to complete or ctx to expire.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md: "Goroutines: clean up on shutdown".
// Call from agent shutdown path (e.g., graceful HTTP server shutdown).
func (c *InstrumentedAIClient) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		c.debugWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("instrumented client shutdown timed out: in-flight recordings may be lost")
	}
}

// resolveRequestID tries OTel baggage first, then explicit context key.
func (c *InstrumentedAIClient) resolveRequestID(ctx context.Context) string {
	// Primary: OTel baggage (automatic propagation via otelhttp)
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		if reqID := baggage["request_id"]; reqID != "" {
			return reqID
		}
	}
	// Fallback: explicit context key (from ExtractRequestContext)
	return core.GetRequestID(ctx)
}

// resolvePhaseNumber reads phase number from explicit context key, then OTel baggage.
func (c *InstrumentedAIClient) resolvePhaseNumber(ctx context.Context) int {
	// Primary: explicit context key (from ExtractRequestContext)
	if n := core.GetPhaseNumber(ctx); n > 0 {
		return n
	}
	// Fallback: OTel baggage
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		if phaseStr := baggage["phase_number"]; phaseStr != "" {
			if n, err := strconv.Atoi(phaseStr); err == nil {
				return n
			}
		}
	}
	return 0
}

// resolveStepID tries explicit context key, then OTel baggage.
func (c *InstrumentedAIClient) resolveStepID(ctx context.Context) string {
	// Primary: explicit context key (from ExtractRequestContext or core.WithStepID)
	if stepID := core.GetStepID(ctx); stepID != "" {
		return stepID
	}
	// Fallback: OTel baggage (if step_id was added as baggage)
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		if stepID := baggage["step_id"]; stepID != "" {
			return stepID
		}
	}
	return ""
}

// Verify interface compliance
var _ core.AIClient = (*InstrumentedAIClient)(nil)

// StreamResponse delegates to the wrapped client if it supports streaming.
// The final response (returned after streaming completes) is recorded.
func (c *InstrumentedAIClient) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	streamer, ok := c.wrapped.(core.StreamingAIClient)
	if !ok {
		return nil, fmt.Errorf("wrapped client does not support streaming")
	}

	start := time.Now()
	resp, err := streamer.StreamResponse(ctx, prompt, options, callback)

	// Record the final aggregated response (same logic as GenerateResponse).
	// Skip recording when the caller has deferred via
	// telemetry.WithLLMCallRecordingDeferred — mirrors the deferral
	// check in GenerateResponse so both code paths share the same
	// suppression contract. See
	// orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
	requestID := c.resolveRequestID(ctx)
	if requestID != "" && !telemetry.IsLLMCallRecordingDeferred(ctx) {
		record := telemetry.LLMCallRecord{
			CallType:        c.defaultType,
			SourceComponent: c.componentName,
			StepID:          c.resolveStepID(ctx),
			PhaseNumber:     c.resolvePhaseNumber(ctx),
			Timestamp:       start,
			DurationMs:      time.Since(start).Milliseconds(),
			Prompt:          prompt,
			Success:         err == nil,
		}
		// Nil-safe options access — options may be nil (matches ChainClient pattern)
		if options != nil {
			record.Temperature = float64(options.Temperature)
			record.MaxTokens = options.MaxTokens
			record.SystemPrompt = options.SystemPrompt
		}
		if resp != nil {
			record.Model = resp.Model
			record.Provider = resp.Provider
			record.Response = resp.Content
			record.PromptTokens = resp.Usage.PromptTokens
			record.CompletionTokens = resp.Usage.CompletionTokens
			record.TotalTokens = resp.Usage.TotalTokens
		}
		if err != nil {
			record.Error = err.Error()
			// Extract model/provider from structured error when response is nil
			// (same pattern as GenerateResponse — ORCH-008 Fix 4)
			if resp == nil {
				var pe core.ProviderError
				if errors.As(err, &pe) {
					record.Model = pe.Model()
					record.Provider = pe.Provider()
				}
			}
		}
		c.recordAsync(ctx, requestID, record)
	}

	return resp, err
}

// SupportsStreaming returns true if the wrapped client supports streaming.
func (c *InstrumentedAIClient) SupportsStreaming() bool {
	if streamer, ok := c.wrapped.(core.StreamingAIClient); ok {
		return streamer.SupportsStreaming()
	}
	return false
}

// recordAsync fires an async recording goroutine tracked by the WaitGroup.
// Extracted to avoid duplication between GenerateResponse and StreamResponse.
func (c *InstrumentedAIClient) recordAsync(ctx context.Context, requestID string, record telemetry.LLMCallRecord) {
	c.debugWg.Add(1)
	go func() {
		defer c.debugWg.Done()
		// Keep request-scoped values like baggage while detaching from caller cancellation.
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		if recErr := c.recorder.RecordLLMCall(recordCtx, requestID, record); recErr != nil {
			c.logger.Warn("Failed to record LLM debug interaction", map[string]interface{}{
				"request_id":       requestID,
				"source_component": c.componentName,
				"call_type":        c.defaultType,
				"error":            recErr.Error(),
			})
		}
	}()
}
