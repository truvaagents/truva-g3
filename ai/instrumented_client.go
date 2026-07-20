package ai

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
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
	wrapped        core.AIClient
	recorder       telemetry.LLMCallRecorder
	logger         core.Logger    // For Warn-level recording failure logs
	componentName  string         // default source component name
	defaultType    string         // default call type (e.g., "agent_llm_call")
	debugWg        sync.WaitGroup // tracks in-flight async recordings
	telemetry      core.Telemetry
	factoryManaged bool
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

// WithInstrumentedTelemetry enables the logical ai.generate and ai.stream
// spans emitted by the common wrapper.
func WithInstrumentedTelemetry(provider core.Telemetry) InstrumentedOption {
	return func(c *InstrumentedAIClient) { c.telemetry = provider }
}

func withFactoryInstrumentation() InstrumentedOption {
	return func(c *InstrumentedAIClient) { c.factoryManaged = true }
}

func NewInstrumentedClient(client core.AIClient, recorder telemetry.LLMCallRecorder, opts ...InstrumentedOption) *InstrumentedAIClient {
	c := &InstrumentedAIClient{
		wrapped:     client,
		recorder:    recorder,
		defaultType: "agent_llm_call",
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	// NewClient and NewRequestClient already install a no-op-recorder wrapper
	// for logical spans. When an application adds debug
	// recording, collapse that internal layer so one logical call produces one
	// common span.
	if existing, ok := client.(*InstrumentedAIClient); ok && existing.factoryManaged {
		c.wrapped = existing.wrapped
		if c.telemetry == nil {
			c.telemetry = existing.telemetry
		}
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

// GenerateResponse adapts the legacy call through the request-aware path.
func (c *InstrumentedAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	result, err := c.Generate(ctx, core.NewAIRequestFromLegacy(prompt, "", options))
	if result != nil {
		return result.Response, err
	}
	return nil, err
}

// Generate executes one logical generation call and records common telemetry
// around request-capable or legacy clients.
func (c *InstrumentedAIClient) Generate(
	ctx context.Context,
	request *core.AIRequest,
) (result *core.AIResult, err error) {
	if request == nil {
		return nil, errors.New("AI request is nil")
	}
	started := time.Now()
	ctx, span := c.startLogicalSpan(ctx, "ai.generate", request.Purpose)
	defer func() {
		c.finishLogicalSpan(span, result, err, started)
	}()

	result, err = core.GenerateAI(ctx, c.wrapped, request)
	c.recordResult(ctx, started, request, result, err)
	return result, err
}

// SetLogger updates the logger used for recording failure warnings.
// Called by core.applyConfigToComponent after framework initialization
// replaces the NoOpLogger with the real component logger.
func (c *InstrumentedAIClient) SetLogger(logger core.Logger) {
	if logger == nil {
		c.logger = &core.NoOpLogger{}
	} else if componentLogger, ok := logger.(core.ComponentAwareLogger); ok {
		c.logger = componentLogger.WithComponent("framework/ai")
	} else {
		c.logger = logger
	}
	// Propagate to wrapped client too
	if loggable, ok := c.wrapped.(interface{ SetLogger(core.Logger) }); ok {
		loggable.SetLogger(logger)
	}
}

// SetTelemetry updates logical tracing and propagates the provider to the
// wrapped framework client when it supports runtime telemetry configuration.
func (c *InstrumentedAIClient) SetTelemetry(provider core.Telemetry) {
	c.telemetry = provider
	if configurable, ok := c.wrapped.(interface{ SetTelemetry(core.Telemetry) }); ok {
		configurable.SetTelemetry(provider)
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

// StreamResponse adapts the legacy streaming call through the request-aware
// path. A nil legacy callback remains a no-op for backward compatibility.
func (c *InstrumentedAIClient) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	if callback == nil {
		callback = func(core.StreamChunk) error { return nil }
	}
	result, err := c.Stream(ctx, core.NewAIRequestFromLegacy(prompt, "", options), callback)
	if result != nil {
		return result.Response, err
	}
	return nil, err
}

// Stream executes one logical streaming call through the canonical core
// capability adapter and records the final normalized result.
func (c *InstrumentedAIClient) Stream(
	ctx context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (result *core.AIResult, err error) {
	if request == nil {
		return nil, errors.New("AI request is nil")
	}
	started := time.Now()
	ctx, span := c.startLogicalSpan(ctx, "ai.stream", request.Purpose)
	defer func() {
		c.finishLogicalSpan(span, result, err, started)
	}()

	result, err = core.StreamAI(ctx, c.wrapped, request, callback)
	c.recordResult(ctx, started, request, result, err)
	return result, err
}

// SupportsStreaming returns true if the wrapped client supports streaming.
func (c *InstrumentedAIClient) SupportsStreaming() bool {
	if _, ok := c.wrapped.(core.StreamingAIRequestClient); ok {
		return true
	}
	if streamer, ok := c.wrapped.(core.StreamingAIClient); ok {
		return streamer.SupportsStreaming()
	}
	return false
}

func (c *InstrumentedAIClient) startLogicalSpan(
	ctx context.Context,
	name string,
	purpose string,
) (context.Context, core.Span) {
	if c.telemetry == nil {
		return ctx, &core.NoOpSpan{}
	}
	spanCtx, span := c.telemetry.StartSpan(ctx, name)
	if spanCtx == nil {
		spanCtx = ctx
	}
	if span == nil {
		span = &core.NoOpSpan{}
	}
	telemetry.SetCommonAttrsOn(spanCtx, span)
	span.SetAttribute("ai.operation", strings.TrimPrefix(name, "ai."))
	if purpose != "" {
		span.SetAttribute("ai.purpose", purpose)
	}
	return spanCtx, span
}

func (c *InstrumentedAIClient) finishLogicalSpan(
	span core.Span,
	result *core.AIResult,
	err error,
	started time.Time,
) {
	if span == nil {
		return
	}
	defer span.End()
	span.SetAttribute("ai.duration_ms", time.Since(started).Milliseconds())
	if err != nil {
		span.SetAttribute("ai.status", "error")
		// Provider errors may contain raw response material. Record a stable,
		// secret-safe logical error while lower provider spans retain transport
		// diagnostics under their own sanitization contract.
		span.RecordError(errors.New("AI request failed"))
	} else {
		span.SetAttribute("ai.status", "success")
	}
	if result == nil {
		return
	}
	report := result.RequestReport
	if report != nil {
		setSpanString(span, "ai.provider", report.Provider)
		setSpanString(span, "ai.provider_alias", report.ProviderAlias)
		setSpanString(span, "ai.surface", report.Surface)
		setSpanString(span, "ai.request.operation", report.Operation)
		setSpanString(span, "ai.purpose", report.Purpose)
		setSpanString(span, "ai.requested_model", report.RequestedModel)
		setSpanString(span, "ai.model", report.ResolvedModel)
		span.SetAttribute("ai.request.policy_stable", report.Stable)
		span.SetAttribute("ai.request.adjustment_count", len(report.Adjustments))
		if report.Stable {
			setSpanString(span, "ai.request.policy_fingerprint", report.Fingerprint)
		}
		setAdjustmentAttributes(span, report.Adjustments)
	}
	if response := result.Response; response != nil {
		if report == nil || report.Provider == "" {
			setSpanString(span, "ai.provider", response.Provider)
		}
		if report == nil || report.ResolvedModel == "" {
			setSpanString(span, "ai.model", response.Model)
		}
		span.SetAttribute("ai.prompt_tokens", response.Usage.PromptTokens)
		span.SetAttribute("ai.completion_tokens", response.Usage.CompletionTokens)
		span.SetAttribute("ai.total_tokens", response.Usage.TotalTokens)
	}
	if details := result.UsageDetails; details != nil {
		span.SetAttribute("ai.cached_input_tokens", details.CachedInputTokens)
		span.SetAttribute("ai.reasoning_tokens", details.ReasoningTokens)
		span.SetAttribute("ai.audio_input_tokens", details.AudioInputTokens)
		span.SetAttribute("ai.audio_output_tokens", details.AudioOutputTokens)
		span.SetAttribute("ai.usage_detail_count", len(details.Counters))
	}
}

func setSpanString(span core.Span, key, value string) {
	if value != "" {
		span.SetAttribute(key, value)
	}
}

func setAdjustmentAttributes(span core.Span, adjustments []core.AIRequestAdjustment) {
	if len(adjustments) == 0 {
		return
	}
	paths := make(map[string]struct{}, len(adjustments))
	rules := make(map[string]struct{}, len(adjustments))
	for _, adjustment := range adjustments {
		if adjustment.Path != "" {
			paths[adjustment.Path] = struct{}{}
		}
		identity := adjustment.Source
		if adjustment.Rule != "" {
			if identity != "" {
				identity += "/"
			}
			identity += adjustment.Rule
		}
		if identity != "" {
			rules[identity] = struct{}{}
		}
	}
	setSortedAttribute(span, "ai.request.adjusted_paths", paths)
	setSortedAttribute(span, "ai.request.adjustment_rules", rules)
}

func setSortedAttribute(span core.Span, key string, values map[string]struct{}) {
	if len(values) == 0 {
		return
	}
	sorted := make([]string, 0, len(values))
	for value := range values {
		sorted = append(sorted, value)
	}
	sort.Strings(sorted)
	span.SetAttribute(key, strings.Join(sorted, ","))
}

func (c *InstrumentedAIClient) recordResult(
	ctx context.Context,
	started time.Time,
	request *core.AIRequest,
	result *core.AIResult,
	err error,
) {
	requestID := c.resolveRequestID(ctx)
	if requestID == "" || telemetry.IsLLMCallRecordingDeferred(ctx) {
		return
	}
	record := telemetry.LLMCallRecord{
		CallType:        c.defaultType,
		SourceComponent: c.componentName,
		StepID:          c.resolveStepID(ctx),
		PhaseNumber:     c.resolvePhaseNumber(ctx),
		Timestamp:       started,
		DurationMs:      time.Since(started).Milliseconds(),
		Prompt:          request.Prompt,
		Success:         err == nil,
	}
	applyDebugRequestOptions(&record, request)
	if result != nil && result.Response != nil {
		response := result.Response
		record.Model = response.Model
		record.Provider = response.Provider
		record.Response = response.Content
		record.PromptTokens = response.Usage.PromptTokens
		record.CompletionTokens = response.Usage.CompletionTokens
		record.TotalTokens = response.Usage.TotalTokens
	}
	if err != nil {
		record.Error = err.Error()
		if result == nil || result.Response == nil {
			var providerError core.ProviderError
			if errors.As(err, &providerError) {
				record.Model = providerError.Model()
				record.Provider = providerError.Provider()
			}
		}
	}
	c.recordAsync(ctx, requestID, record)
}

func applyDebugRequestOptions(record *telemetry.LLMCallRecord, request *core.AIRequest) {
	if options := request.LegacyOptions(); options != nil {
		record.Temperature = float64(options.Temperature)
		record.MaxTokens = options.MaxTokens
		record.SystemPrompt = options.SystemPrompt
	}
	if parameter := request.Generation.Temperature; parameter.Mode != core.AIParameterInherit {
		if parameter.Mode == core.AIParameterSet {
			record.Temperature = float64(parameter.Value)
		} else {
			record.Temperature = 0
		}
	}
	if parameter := request.Generation.MaxTokens; parameter.Mode != core.AIParameterInherit {
		if parameter.Mode == core.AIParameterSet {
			record.MaxTokens = parameter.Value
		} else {
			record.MaxTokens = 0
		}
	}
	if parameter := request.Generation.SystemPrompt; parameter.Mode != core.AIParameterInherit {
		if parameter.Mode == core.AIParameterSet {
			record.SystemPrompt = parameter.Value
		} else {
			record.SystemPrompt = ""
		}
	}
}

// Verify interface compliance.
var (
	_ core.AIClient                 = (*InstrumentedAIClient)(nil)
	_ core.AIRequestClient          = (*InstrumentedAIClient)(nil)
	_ core.StreamingAIClient        = (*InstrumentedAIClient)(nil)
	_ core.StreamingAIRequestClient = (*InstrumentedAIClient)(nil)
)

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
