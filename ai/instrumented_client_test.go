package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// =============================================================================
// Unit tests for InstrumentedAIClient
// Pure unit tests — uses mock AIClient and mock LLMCallRecorder
// =============================================================================

// --- Test mocks ---

// mockAIClientForInstr implements core.AIClient for testing
type mockAIClientForInstr struct {
	generateResp *core.AIResponse
	generateErr  error
	callCount    int
}

func (m *mockAIClientForInstr) GenerateResponse(_ context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
	m.callCount++
	return m.generateResp, m.generateErr
}

// mockStreamingAIClientForInstr implements core.StreamingAIClient for testing
type mockStreamingAIClientForInstr struct {
	mockAIClientForInstr
	streamResp     *core.AIResponse
	streamErr      error
	streamCalls    int
	supportsStream bool
}

type mockRequestAIClientForInstr struct {
	mockAIClientForInstr
	generateResult  *core.AIResult
	requestErr      error
	requestCalls    int
	receivedRequest *core.AIRequest
}

type mockFingerprintAIClientForInstr struct {
	mockRequestAIClientForInstr
	fingerprint string
	stable      bool
	request     *core.AIRequest
}

func (m *mockFingerprintAIClientForInstr) RequestFingerprint(_ context.Context, request *core.AIRequest) (string, bool) {
	m.request = request
	return m.fingerprint, m.stable
}

func (m *mockRequestAIClientForInstr) Generate(_ context.Context, request *core.AIRequest) (*core.AIResult, error) {
	m.requestCalls++
	m.receivedRequest = request
	return m.generateResult, m.requestErr
}

func TestInstrumentedClientDelegatesRequestFingerprint(t *testing.T) {
	wrapped := &mockFingerprintAIClientForInstr{fingerprint: "policy-v1", stable: true}
	client := NewInstrumentedClient(wrapped, nil)
	request := core.NewAIRequest("prompt", "planning")

	fingerprint, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || fingerprint != "policy-v1" || wrapped.request != request {
		t.Fatalf("fingerprint delegation = %q, %t, request %p", fingerprint, stable, wrapped.request)
	}

	unsupported := NewInstrumentedClient(&mockRequestAIClientForInstr{}, nil)
	if fingerprint, stable := unsupported.RequestFingerprint(t.Context(), request); stable || fingerprint != "" {
		t.Fatalf("unsupported fingerprint = %q, %t", fingerprint, stable)
	}

	legacy := NewInstrumentedClient(&mockAIClientForInstr{}, nil)
	legacyRequest := core.NewAIRequestFromLegacy("prompt", "planning", &core.AIOptions{Model: "legacy-model"})
	legacyFingerprint, stable := legacy.RequestFingerprint(t.Context(), legacyRequest)
	if !stable || len(legacyFingerprint) != 64 {
		t.Fatalf("legacy fingerprint = %q, %t", legacyFingerprint, stable)
	}
	legacyRequest.Purpose = "synthesis"
	changed, stable := legacy.RequestFingerprint(t.Context(), legacyRequest)
	if !stable || changed == legacyFingerprint {
		t.Fatalf("legacy purpose did not change fingerprint: %q, %q", legacyFingerprint, changed)
	}
	legacyRequest.Generation.TopP = core.SetAIParameter(float32(0.9))
	if fingerprint, stable := legacy.RequestFingerprint(t.Context(), legacyRequest); stable || fingerprint != "" {
		t.Fatalf("unrepresentable legacy fingerprint = %q, %t", fingerprint, stable)
	}
}

type mockStreamingRequestAIClientForInstr struct {
	mockRequestAIClientForInstr
	streamResult *core.AIResult
	streamErr    error
	streamCalls  int
}

func (m *mockStreamingRequestAIClientForInstr) Stream(
	_ context.Context,
	_ *core.AIRequest,
	callback core.StreamCallback,
) (*core.AIResult, error) {
	m.streamCalls++
	if callback != nil {
		if err := callback(core.StreamChunk{Content: "chunk", Delta: true}); err != nil {
			return nil, err
		}
	}
	return m.streamResult, m.streamErr
}

type phase6InstrumentedTelemetry struct {
	names []string
	spans []*phase6InstrumentedSpan
	nil   bool
}

func (provider *phase6InstrumentedTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	provider.names = append(provider.names, name)
	if provider.nil {
		return nil, nil
	}
	span := &phase6InstrumentedSpan{attributes: make(map[string]interface{})}
	provider.spans = append(provider.spans, span)
	return ctx, span
}

func (*phase6InstrumentedTelemetry) RecordMetric(string, float64, map[string]string) {}

type phase6InstrumentedSpan struct {
	attributes map[string]interface{}
	errors     []error
	ended      int
}

func (span *phase6InstrumentedSpan) End() { span.ended++ }
func (span *phase6InstrumentedSpan) SetAttribute(key string, value interface{}) {
	span.attributes[key] = value
}
func (span *phase6InstrumentedSpan) RecordError(err error) {
	span.errors = append(span.errors, err)
}

func (m *mockStreamingAIClientForInstr) StreamResponse(_ context.Context, _ string, _ *core.AIOptions, _ core.StreamCallback) (*core.AIResponse, error) {
	m.streamCalls++
	return m.streamResp, m.streamErr
}

func (m *mockStreamingAIClientForInstr) SupportsStreaming() bool {
	return m.supportsStream
}

// mockRecorder implements telemetry.LLMCallRecorder for testing
type mockRecorder struct {
	mu      sync.Mutex
	records []telemetry.LLMCallRecord
	reqIDs  []string
	err     error
}

func (m *mockRecorder) RecordLLMCall(_ context.Context, requestID string, record telemetry.LLMCallRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.records = append(m.records, record)
	m.reqIDs = append(m.reqIDs, requestID)
	return nil
}

func (m *mockRecorder) getRecords() []telemetry.LLMCallRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]telemetry.LLMCallRecord, len(m.records))
	copy(cp, m.records)
	return cp
}

func (m *mockRecorder) getReqIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.reqIDs))
	copy(cp, m.reqIDs)
	return cp
}

// mockComponentAwareLogger implements both core.Logger and core.ComponentAwareLogger
type mockComponentAwareLogger struct {
	mockLoggerForInstr
	componentSet string
}

func (m *mockComponentAwareLogger) WithComponent(component string) core.Logger {
	return &mockComponentAwareLogger{componentSet: component}
}

// mockLoggableAIClient implements core.AIClient and has a SetLogger method
type mockLoggableAIClient struct {
	mockAIClientForInstr
	loggerSet core.Logger
}

func (m *mockLoggableAIClient) SetLogger(logger core.Logger) {
	m.loggerSet = logger
}

type mockTelemetryAIClientForInstr struct {
	mockAIClientForInstr
	telemetrySet core.Telemetry
}

func (m *mockTelemetryAIClientForInstr) SetTelemetry(provider core.Telemetry) {
	m.telemetrySet = provider
}

// mockLoggerForInstr captures warning messages for testing
type mockLoggerForInstr struct {
	mu       sync.Mutex
	warnings []string
}

func (m *mockLoggerForInstr) Debug(_ string, _ map[string]interface{}) {}
func (m *mockLoggerForInstr) Info(_ string, _ map[string]interface{})  {}
func (m *mockLoggerForInstr) Warn(msg string, _ map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.warnings = append(m.warnings, msg)
}
func (m *mockLoggerForInstr) Error(_ string, _ map[string]interface{}) {}
func (m *mockLoggerForInstr) DebugWithContext(_ context.Context, _ string, _ map[string]interface{}) {
}
func (m *mockLoggerForInstr) InfoWithContext(_ context.Context, _ string, _ map[string]interface{}) {
}
func (m *mockLoggerForInstr) WarnWithContext(_ context.Context, msg string, _ map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.warnings = append(m.warnings, msg)
}
func (m *mockLoggerForInstr) ErrorWithContext(_ context.Context, _ string, _ map[string]interface{}) {
}
func (m *mockLoggerForInstr) WithFields(_ map[string]interface{}) core.Logger { return m }
func (m *mockLoggerForInstr) WithError(_ error) core.Logger                   { return m }

// --- NewInstrumentedClient() ---

func TestNewInstrumentedClient(t *testing.T) {
	t.Run("creates client with defaults", func(t *testing.T) {
		mock := &mockAIClientForInstr{}
		client := NewInstrumentedClient(mock, nil)

		if client == nil {
			t.Fatal("expected non-nil client")
		}
		if client.wrapped != mock {
			t.Error("expected wrapped client to match")
		}
		if client.defaultType != "agent_llm_call" {
			t.Errorf("expected default type 'agent_llm_call', got %q", client.defaultType)
		}
		// nil recorder should default to NoOp
		if _, ok := client.recorder.(*telemetry.NoOpLLMCallRecorder); !ok {
			t.Error("expected nil recorder to default to NoOpLLMCallRecorder")
		}
		// nil logger should default to NoOp
		if _, ok := client.logger.(*core.NoOpLogger); !ok {
			t.Error("expected nil logger to default to NoOpLogger")
		}
	})

	t.Run("applies options", func(t *testing.T) {
		mock := &mockAIClientForInstr{}
		rec := &mockRecorder{}
		logger := &mockLoggerForInstr{}

		client := NewInstrumentedClient(mock, rec,
			WithComponentName("my-agent"),
			WithDefaultCallType("custom_type"),
			WithInstrumentedLogger(logger),
		)

		if client.componentName != "my-agent" {
			t.Errorf("expected component 'my-agent', got %q", client.componentName)
		}
		if client.defaultType != "custom_type" {
			t.Errorf("expected type 'custom_type', got %q", client.defaultType)
		}
	})
}

// --- GenerateResponse() ---

func TestInstrumentedClient_GenerateResponse(t *testing.T) {
	t.Run("delegates to wrapped client", func(t *testing.T) {
		mock := &mockAIClientForInstr{
			generateResp: &core.AIResponse{
				Content:  "hello world",
				Model:    "gpt-4o",
				Provider: "openai",
				Usage:    core.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
			},
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec)

		resp, err := client.GenerateResponse(context.Background(), "test prompt", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Content != "hello world" {
			t.Errorf("expected 'hello world', got %q", resp.Content)
		}
		if mock.callCount != 1 {
			t.Errorf("expected 1 call to wrapped client, got %d", mock.callCount)
		}
	})

	t.Run("skips recording when no request ID in context", func(t *testing.T) {
		mock := &mockAIClientForInstr{
			generateResp: &core.AIResponse{Content: "hi"},
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec)

		_, _ = client.GenerateResponse(context.Background(), "test", nil)

		// Give async goroutine time to run (if it were to run)
		time.Sleep(50 * time.Millisecond)

		if len(rec.getRecords()) != 0 {
			t.Error("expected no recordings without request ID")
		}
	})

	t.Run("records when request ID is in context key", func(t *testing.T) {
		mock := &mockAIClientForInstr{
			generateResp: &core.AIResponse{
				Content:  "response",
				Model:    "gpt-4o",
				Provider: "openai",
				Usage:    core.TokenUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
			},
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec, WithComponentName("test-agent"))

		ctx := core.WithRequestID(context.Background(), "req-abc")
		ctx = core.WithStepID(ctx, "step-1")
		ctx = core.WithPhaseNumber(ctx, 2)

		resp, err := client.GenerateResponse(ctx, "prompt", &core.AIOptions{
			Temperature:  0.5,
			MaxTokens:    500,
			SystemPrompt: "sys",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Content != "response" {
			t.Errorf("expected 'response', got %q", resp.Content)
		}

		// Wait for async recording
		client.Shutdown(context.Background())

		records := rec.getRecords()
		if len(records) != 1 {
			t.Fatalf("expected 1 recording, got %d", len(records))
		}
		r := records[0]
		if r.SourceComponent != "test-agent" {
			t.Errorf("SourceComponent: expected 'test-agent', got %q", r.SourceComponent)
		}
		if r.StepID != "step-1" {
			t.Errorf("StepID: expected 'step-1', got %q", r.StepID)
		}
		if r.PhaseNumber != 2 {
			t.Errorf("PhaseNumber: expected 2, got %d", r.PhaseNumber)
		}
		if r.Prompt != "prompt" {
			t.Errorf("Prompt: expected 'prompt', got %q", r.Prompt)
		}
		if !r.Success {
			t.Error("Success: expected true")
		}
		if r.Model != "gpt-4o" {
			t.Errorf("Model: expected 'gpt-4o', got %q", r.Model)
		}
		if r.Temperature != 0.5 {
			t.Errorf("Temperature: expected 0.5, got %f", r.Temperature)
		}
		if r.MaxTokens != 500 {
			t.Errorf("MaxTokens: expected 500, got %d", r.MaxTokens)
		}
		if r.SystemPrompt != "sys" {
			t.Errorf("SystemPrompt: expected 'sys', got %q", r.SystemPrompt)
		}

		reqIDs := rec.getReqIDs()
		if reqIDs[0] != "req-abc" {
			t.Errorf("requestID: expected 'req-abc', got %q", reqIDs[0])
		}
	})

	t.Run("records error on LLM failure", func(t *testing.T) {
		mock := &mockAIClientForInstr{
			generateErr: errors.New("rate limit exceeded"),
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec)

		ctx := core.WithRequestID(context.Background(), "req-err")
		_, err := client.GenerateResponse(ctx, "fail prompt", nil)
		if err == nil {
			t.Fatal("expected error")
		}

		client.Shutdown(context.Background())

		records := rec.getRecords()
		if len(records) != 1 {
			t.Fatalf("expected 1 recording, got %d", len(records))
		}
		if records[0].Success {
			t.Error("expected Success=false on error")
		}
		if records[0].Error != "rate limit exceeded" {
			t.Errorf("expected error message 'rate limit exceeded', got %q", records[0].Error)
		}
	})

	t.Run("logs warning when recorder fails", func(t *testing.T) {
		mock := &mockAIClientForInstr{
			generateResp: &core.AIResponse{Content: "ok"},
		}
		rec := &mockRecorder{err: errors.New("redis down")}
		logger := &mockLoggerForInstr{}
		client := NewInstrumentedClient(mock, rec, WithInstrumentedLogger(logger))

		ctx := core.WithRequestID(context.Background(), "req-log")
		_, _ = client.GenerateResponse(ctx, "test", nil)

		client.Shutdown(context.Background())

		logger.mu.Lock()
		defer logger.mu.Unlock()
		if len(logger.warnings) == 0 {
			t.Error("expected warning log when recorder fails")
		}
	})

	t.Run("nil options handled safely", func(t *testing.T) {
		mock := &mockAIClientForInstr{
			generateResp: &core.AIResponse{Content: "ok"},
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec)

		ctx := core.WithRequestID(context.Background(), "req-nil")
		resp, err := client.GenerateResponse(ctx, "test", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Content != "ok" {
			t.Errorf("expected 'ok', got %q", resp.Content)
		}

		client.Shutdown(context.Background())

		records := rec.getRecords()
		if len(records) != 1 {
			t.Fatalf("expected 1 recording, got %d", len(records))
		}
		// Options fields should be zero values
		if records[0].Temperature != 0 {
			t.Errorf("expected Temperature=0 for nil options, got %f", records[0].Temperature)
		}
	})
}

// --- StreamResponse() ---

func TestInstrumentedClient_StreamResponse(t *testing.T) {
	t.Run("returns error when wrapped client does not support streaming", func(t *testing.T) {
		mock := &mockAIClientForInstr{}
		client := NewInstrumentedClient(mock, nil)

		_, err := client.StreamResponse(context.Background(), "test", nil, nil)
		if err == nil {
			t.Fatal("expected error for non-streaming client")
		}
		if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
			t.Errorf("error = %v, want unsupported request feature", err)
		}
	})

	t.Run("delegates to streaming wrapped client and records", func(t *testing.T) {
		mock := &mockStreamingAIClientForInstr{
			mockAIClientForInstr: mockAIClientForInstr{},
			streamResp: &core.AIResponse{
				Content:  "streamed",
				Model:    "gpt-4o",
				Provider: "openai",
				Usage:    core.TokenUsage{PromptTokens: 3, CompletionTokens: 7, TotalTokens: 10},
			},
			supportsStream: true,
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec, WithComponentName("stream-agent"))

		ctx := core.WithRequestID(context.Background(), "req-stream")
		resp, err := client.StreamResponse(ctx, "stream prompt", &core.AIOptions{MaxTokens: 200}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Content != "streamed" {
			t.Errorf("expected 'streamed', got %q", resp.Content)
		}
		if mock.streamCalls != 1 {
			t.Errorf("expected 1 stream call, got %d", mock.streamCalls)
		}

		client.Shutdown(context.Background())

		records := rec.getRecords()
		if len(records) != 1 {
			t.Fatalf("expected 1 recording, got %d", len(records))
		}
		if records[0].Prompt != "stream prompt" {
			t.Errorf("Prompt: expected 'stream prompt', got %q", records[0].Prompt)
		}
		if records[0].Model != "gpt-4o" {
			t.Errorf("Model: expected 'gpt-4o', got %q", records[0].Model)
		}
	})

	t.Run("skips recording without request ID", func(t *testing.T) {
		mock := &mockStreamingAIClientForInstr{
			streamResp:     &core.AIResponse{Content: "hi"},
			supportsStream: true,
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec)

		_, _ = client.StreamResponse(context.Background(), "test", nil, nil)

		time.Sleep(50 * time.Millisecond)

		if len(rec.getRecords()) != 0 {
			t.Error("expected no recordings without request ID")
		}
	})
}

// --- SupportsStreaming() ---

func TestInstrumentedClient_SupportsStreaming(t *testing.T) {
	t.Run("false when wrapped client is not streaming", func(t *testing.T) {
		mock := &mockAIClientForInstr{}
		client := NewInstrumentedClient(mock, nil)
		if client.SupportsStreaming() {
			t.Error("expected false for non-streaming client")
		}
	})

	t.Run("delegates to wrapped streaming client", func(t *testing.T) {
		mock := &mockStreamingAIClientForInstr{supportsStream: true}
		client := NewInstrumentedClient(mock, nil)
		if !client.SupportsStreaming() {
			t.Error("expected true for streaming client")
		}
	})

	t.Run("returns false when wrapped streaming client says false", func(t *testing.T) {
		mock := &mockStreamingAIClientForInstr{supportsStream: false}
		client := NewInstrumentedClient(mock, nil)
		if client.SupportsStreaming() {
			t.Error("expected false when wrapped streaming client says false")
		}
	})

	t.Run("true for request-aware streaming client", func(t *testing.T) {
		client := NewInstrumentedClient(&mockStreamingRequestAIClientForInstr{}, nil)
		if !client.SupportsStreaming() {
			t.Error("expected true for request-aware streaming client")
		}
	})
}

// --- Shutdown() ---

func TestInstrumentedClient_Shutdown(t *testing.T) {
	t.Run("returns nil when no in-flight recordings", func(t *testing.T) {
		client := NewInstrumentedClient(&mockAIClientForInstr{}, nil)
		err := client.Shutdown(context.Background())
		if err != nil {
			t.Errorf("expected nil error, got: %v", err)
		}
	})

	t.Run("waits for in-flight recordings", func(t *testing.T) {
		mock := &mockAIClientForInstr{
			generateResp: &core.AIResponse{Content: "ok"},
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec)

		ctx := core.WithRequestID(context.Background(), "req-shutdown")
		_, _ = client.GenerateResponse(ctx, "test", nil)

		err := client.Shutdown(context.Background())
		if err != nil {
			t.Errorf("expected nil error, got: %v", err)
		}

		if len(rec.getRecords()) != 1 {
			t.Error("expected recording to complete before shutdown returns")
		}
	})

	t.Run("returns error on timeout", func(t *testing.T) {
		client := NewInstrumentedClient(&mockAIClientForInstr{}, nil)

		// Manually add a WaitGroup entry that will never complete
		client.debugWg.Add(1)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		err := client.Shutdown(ctx)
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if err.Error() != "instrumented client shutdown timed out: in-flight recordings may be lost" {
			t.Errorf("unexpected error message: %v", err)
		}

		// Clean up to avoid test panic
		client.debugWg.Done()
	})
}

// --- resolvePhaseNumber() ---

func TestInstrumentedClient_resolvePhaseNumber(t *testing.T) {
	client := NewInstrumentedClient(&mockAIClientForInstr{}, nil)

	t.Run("returns 0 for empty context", func(t *testing.T) {
		n := client.resolvePhaseNumber(context.Background())
		if n != 0 {
			t.Errorf("expected 0, got %d", n)
		}
	})

	t.Run("returns value from context key (primary)", func(t *testing.T) {
		ctx := core.WithPhaseNumber(context.Background(), 3)
		n := client.resolvePhaseNumber(ctx)
		if n != 3 {
			t.Errorf("expected 3, got %d", n)
		}
	})

	t.Run("falls back to OTel baggage", func(t *testing.T) {
		ctx := telemetry.WithBaggage(context.Background(), "phase_number", "2")
		n := client.resolvePhaseNumber(ctx)
		if n != 2 {
			t.Errorf("expected 2 from baggage, got %d", n)
		}
	})

	t.Run("context key takes priority over baggage", func(t *testing.T) {
		ctx := core.WithPhaseNumber(context.Background(), 5)
		ctx = telemetry.WithBaggage(ctx, "phase_number", "2")
		n := client.resolvePhaseNumber(ctx)
		if n != 5 {
			t.Errorf("expected 5 from context key (priority), got %d", n)
		}
	})
}

// --- resolveStepID() ---

func TestInstrumentedClient_resolveStepID(t *testing.T) {
	client := NewInstrumentedClient(&mockAIClientForInstr{}, nil)

	t.Run("returns empty for empty context", func(t *testing.T) {
		id := client.resolveStepID(context.Background())
		if id != "" {
			t.Errorf("expected empty, got %q", id)
		}
	})

	t.Run("returns value from context key", func(t *testing.T) {
		ctx := core.WithStepID(context.Background(), "step-42")
		id := client.resolveStepID(ctx)
		if id != "step-42" {
			t.Errorf("expected 'step-42', got %q", id)
		}
	})

	t.Run("falls back to OTel baggage", func(t *testing.T) {
		ctx := telemetry.WithBaggage(context.Background(), "step_id", "step-99")
		id := client.resolveStepID(ctx)
		if id != "step-99" {
			t.Errorf("expected 'step-99' from baggage, got %q", id)
		}
	})
}

// --- resolveRequestID() ---

func TestInstrumentedClient_resolveRequestID(t *testing.T) {
	client := NewInstrumentedClient(&mockAIClientForInstr{}, nil)

	t.Run("returns empty for empty context", func(t *testing.T) {
		id := client.resolveRequestID(context.Background())
		if id != "" {
			t.Errorf("expected empty, got %q", id)
		}
	})

	t.Run("returns value from OTel baggage (primary)", func(t *testing.T) {
		ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-baggage")
		id := client.resolveRequestID(ctx)
		if id != "req-baggage" {
			t.Errorf("expected 'req-baggage', got %q", id)
		}
	})

	t.Run("falls back to context key", func(t *testing.T) {
		ctx := core.WithRequestID(context.Background(), "req-ctx")
		id := client.resolveRequestID(ctx)
		if id != "req-ctx" {
			t.Errorf("expected 'req-ctx' from context key, got %q", id)
		}
	})

	t.Run("baggage takes priority over context key", func(t *testing.T) {
		ctx := core.WithRequestID(context.Background(), "req-ctx")
		ctx = telemetry.WithBaggage(ctx, "request_id", "req-baggage")
		id := client.resolveRequestID(ctx)
		if id != "req-baggage" {
			t.Errorf("expected 'req-baggage' (baggage priority), got %q", id)
		}
	})
}

// --- SetLogger() ---

func TestInstrumentedClient_SetLogger(t *testing.T) {
	t.Run("updates logger", func(t *testing.T) {
		client := NewInstrumentedClient(&mockAIClientForInstr{}, nil)

		newLogger := &mockLoggerForInstr{}
		client.SetLogger(newLogger)

		if client.logger != newLogger {
			t.Error("expected logger to be updated")
		}
	})

	t.Run("applies component to replacement logger", func(t *testing.T) {
		client := NewInstrumentedClient(&mockAIClientForInstr{}, nil)

		client.SetLogger(&mockComponentAwareLogger{})

		logger, ok := client.logger.(*mockComponentAwareLogger)
		if !ok || logger.componentSet != "framework/ai" {
			t.Fatalf("replacement logger = %#v", client.logger)
		}
	})

	t.Run("nil replacement uses no-op logger", func(t *testing.T) {
		client := NewInstrumentedClient(&mockAIClientForInstr{}, nil)

		client.SetLogger(nil)

		if _, ok := client.logger.(*core.NoOpLogger); !ok {
			t.Fatalf("nil replacement logger = %T", client.logger)
		}
	})
}

// --- NewInstrumentedClient with ComponentAwareLogger ---

func TestNewInstrumentedClient_ComponentAwareLogger(t *testing.T) {
	t.Run("applies WithComponent when logger is ComponentAwareLogger", func(t *testing.T) {
		mock := &mockAIClientForInstr{}
		rec := &mockRecorder{}
		logger := &mockComponentAwareLogger{}

		client := NewInstrumentedClient(mock, rec, WithInstrumentedLogger(logger))

		// The logger should have been replaced with a component-aware child
		if cal, ok := client.logger.(*mockComponentAwareLogger); ok {
			if cal.componentSet != "framework/ai" {
				t.Errorf("expected component 'framework/ai', got %q", cal.componentSet)
			}
		} else {
			t.Errorf("expected logger to be *mockComponentAwareLogger, got %T", client.logger)
		}
	})
}

// --- SetLogger propagation to wrapped client ---

func TestInstrumentedClient_SetLogger_Propagation(t *testing.T) {
	t.Run("propagates logger to wrapped client with SetLogger", func(t *testing.T) {
		mock := &mockLoggableAIClient{}
		client := NewInstrumentedClient(mock, nil)

		newLogger := &mockLoggerForInstr{}
		client.SetLogger(newLogger)

		if mock.loggerSet != newLogger {
			t.Error("expected logger to be propagated to wrapped client")
		}
	})
}

func TestInstrumentedClient_SetTelemetry_Propagation(t *testing.T) {
	wrapped := &mockTelemetryAIClientForInstr{}
	client := NewInstrumentedClient(wrapped, nil)
	provider := &phase6InstrumentedTelemetry{}

	client.SetTelemetry(provider)

	if client.telemetry != provider || wrapped.telemetrySet != provider {
		t.Fatalf("telemetry propagation = wrapper %T, wrapped %T", client.telemetry, wrapped.telemetrySet)
	}
}

// --- StreamResponse error recording ---

func TestInstrumentedClient_StreamResponse_Error(t *testing.T) {
	t.Run("records error when streaming fails", func(t *testing.T) {
		mock := &mockStreamingAIClientForInstr{
			streamErr:      errors.New("stream interrupted"),
			supportsStream: true,
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec)

		ctx := core.WithRequestID(context.Background(), "req-stream-err")
		_, err := client.StreamResponse(ctx, "fail stream", nil, nil)
		if err == nil {
			t.Fatal("expected error")
		}

		client.Shutdown(context.Background())

		records := rec.getRecords()
		if len(records) != 1 {
			t.Fatalf("expected 1 recording, got %d", len(records))
		}
		if records[0].Success {
			t.Error("expected Success=false on stream error")
		}
		if records[0].Error != "stream interrupted" {
			t.Errorf("expected error 'stream interrupted', got %q", records[0].Error)
		}
	})
}

// --- recordAsync baggage propagation ---

func TestInstrumentedClient_RecordAsync_Baggage(t *testing.T) {
	t.Run("records with OTel baggage request ID", func(t *testing.T) {
		mock := &mockAIClientForInstr{
			generateResp: &core.AIResponse{Content: "ok"},
		}
		rec := &mockRecorder{}
		client := NewInstrumentedClient(mock, rec)

		// Set request_id via baggage (primary path for resolveRequestID)
		ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-baggage-test")
		_, _ = client.GenerateResponse(ctx, "test", nil)

		client.Shutdown(context.Background())

		reqIDs := rec.getReqIDs()
		if len(reqIDs) != 1 {
			t.Fatalf("expected 1 recording, got %d", len(reqIDs))
		}
		if reqIDs[0] != "req-baggage-test" {
			t.Errorf("expected request ID 'req-baggage-test', got %q", reqIDs[0])
		}
	})
}

// --- Recording deferral (Layer 2 of BUG_LLM_INTERACTION_DOUBLE_RECORDING.md) ---

// TestInstrumentedClient_GenerateResponse_DeferralHonoured is the core
// Layer 2 invariant test: when the caller marks ctx with
// telemetry.WithLLMCallRecordingDeferred before calling GenerateResponse,
// the wrapper must NOT emit its own agent_llm_call record. The wrapped
// client's result and error MUST still be returned verbatim.
//
// This is the path orchestration call sites use to prevent the
// double-write for orchestration-initiated LLM calls.
func TestInstrumentedClient_GenerateResponse_DeferralHonoured(t *testing.T) {
	mock := &mockAIClientForInstr{
		generateResp: &core.AIResponse{
			Content:  "deferred-response",
			Model:    "gpt-4o",
			Provider: "openai",
			Usage:    core.TokenUsage{PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33},
		},
	}
	rec := &mockRecorder{}
	client := NewInstrumentedClient(mock, rec, WithComponentName("orch-agent"))

	// Request ID is present (would otherwise short-circuit) AND the
	// deferral marker is set. Wrapper should early-return without
	// emitting to the recorder.
	ctx := core.WithRequestID(context.Background(), "req-deferred")
	ctx = telemetry.WithLLMCallRecordingDeferred(ctx)

	resp, err := client.GenerateResponse(ctx, "prompt", &core.AIOptions{MaxTokens: 100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "deferred-response" {
		t.Errorf("wrapped client result must pass through unchanged; got %q", resp.Content)
	}
	if mock.callCount != 1 {
		t.Errorf("wrapped client must still be called exactly once; got %d", mock.callCount)
	}

	// Drain the wrapper's in-flight goroutines (if any had fired).
	_ = client.Shutdown(context.Background())

	if got := len(rec.getRecords()); got != 0 {
		t.Fatalf("deferral must suppress recording; got %d records", got)
	}
}

// TestInstrumentedClient_GenerateResponse_WithoutDeferralRecordsAsUsual
// is the complementary regression check: when the marker is NOT set,
// the wrapper continues to emit agent_llm_call exactly as it did
// pre-fix. Captures the reflection-job / knowledge-extraction-hook /
// custom-agent-endpoint code paths that rely on the wrapper being the
// single source of truth.
func TestInstrumentedClient_GenerateResponse_WithoutDeferralRecordsAsUsual(t *testing.T) {
	mock := &mockAIClientForInstr{
		generateResp: &core.AIResponse{
			Content:  "normal-response",
			Model:    "gpt-4o",
			Provider: "openai",
			Usage:    core.TokenUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
		},
	}
	rec := &mockRecorder{}
	client := NewInstrumentedClient(mock, rec, WithComponentName("standalone-agent"))

	// Request ID is present, marker is NOT set. Wrapper should record.
	ctx := core.WithRequestID(context.Background(), "req-no-defer")
	_, err := client.GenerateResponse(ctx, "prompt", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = client.Shutdown(context.Background())

	records := rec.getRecords()
	if len(records) != 1 {
		t.Fatalf("absent marker must still emit one agent_llm_call record; got %d", len(records))
	}
	if records[0].CallType != "agent_llm_call" {
		t.Errorf("CallType: expected agent_llm_call, got %q", records[0].CallType)
	}
	if records[0].SourceComponent != "standalone-agent" {
		t.Errorf("SourceComponent: expected standalone-agent, got %q", records[0].SourceComponent)
	}
}

// TestInstrumentedClient_StreamResponse_DeferralHonoured mirrors the
// GenerateResponse deferral test for the streaming path. Streamed
// chunks must still reach the callback (the wrapped streamer is
// unaffected), only the final-response recording is suppressed.
func TestInstrumentedClient_StreamResponse_DeferralHonoured(t *testing.T) {
	mock := &mockStreamingAIClientForInstr{
		streamResp: &core.AIResponse{
			Content:  "streamed-deferred",
			Model:    "gpt-4o",
			Provider: "openai",
			Usage:    core.TokenUsage{PromptTokens: 4, CompletionTokens: 8, TotalTokens: 12},
		},
		supportsStream: true,
	}
	rec := &mockRecorder{}
	client := NewInstrumentedClient(mock, rec, WithComponentName("orch-stream"))

	ctx := core.WithRequestID(context.Background(), "req-stream-deferred")
	ctx = telemetry.WithLLMCallRecordingDeferred(ctx)

	// Sentinel callback — asserting the streamer is still invoked even
	// when recording is suppressed. (The mock streamer here does not
	// actually emit chunks through the callback, but the contract we're
	// locking in is "wrapper does not change streaming behaviour" —
	// covered by the streamCalls counter below.)
	var nilCallback core.StreamCallback
	resp, err := client.StreamResponse(ctx, "stream-prompt", &core.AIOptions{MaxTokens: 200}, nilCallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "streamed-deferred" {
		t.Errorf("streamed result must pass through unchanged; got %q", resp.Content)
	}
	if mock.streamCalls != 1 {
		t.Errorf("streamer must still be invoked exactly once; got %d", mock.streamCalls)
	}

	_ = client.Shutdown(context.Background())

	if got := len(rec.getRecords()); got != 0 {
		t.Fatalf("deferral must suppress streaming recording; got %d records", got)
	}
}

func TestInstrumentedClient_Generate_RequestCapabilityAndLogicalSpan(t *testing.T) {
	providerResult := &core.AIResult{
		Response: &core.AIResponse{
			Content:  "normalized response",
			Model:    "response-model",
			Provider: "response-provider",
			Usage: core.TokenUsage{
				PromptTokens:     13,
				CompletionTokens: 21,
				TotalTokens:      34,
			},
		},
		RequestReport: &core.AIRequestReport{
			Provider:       "anthropic",
			ProviderAlias:  "anthropic.enterprise",
			Surface:        "messages",
			Operation:      "generate",
			Purpose:        "planning",
			RequestedModel: "premium",
			ResolvedModel:  "deployment-sonnet",
			Adjustments: []core.AIRequestAdjustment{
				{Source: "application", Rule: "tenant-policy", Path: "/metadata", Action: "set"},
				{Source: "provider", Rule: "sampling-policy", Path: "/temperature", Action: "remove"},
			},
			Fingerprint: "stable-fingerprint",
			Stable:      true,
		},
		UsageDetails: &core.AIUsageDetails{
			CachedInputTokens: 5,
			ReasoningTokens:   8,
			Counters:          map[string]int64{"cache_write": 3},
		},
	}
	wrapped := &mockRequestAIClientForInstr{generateResult: providerResult}
	tracing := &phase6InstrumentedTelemetry{}
	client := NewInstrumentedClient(
		wrapped,
		nil,
		WithInstrumentedTelemetry(tracing),
	)
	request := core.NewAIRequest("secret prompt body", "planning")
	request.Generation.SystemPrompt = core.SetAIParameter("secret system prompt")

	result, err := client.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if wrapped.requestCalls != 1 || wrapped.receivedRequest != request {
		t.Fatalf("request delegation = calls %d, request %p", wrapped.requestCalls, wrapped.receivedRequest)
	}
	if result != providerResult {
		t.Fatal("request-capable result identity was not preserved")
	}
	if len(tracing.names) != 1 || tracing.names[0] != "ai.generate" || len(tracing.spans) != 1 {
		t.Fatalf("logical spans = names %#v, spans %d", tracing.names, len(tracing.spans))
	}
	span := tracing.spans[0]
	if span.ended != 1 || len(span.errors) != 0 {
		t.Fatalf("span completion = ended %d, errors %#v", span.ended, span.errors)
	}
	wantAttributes := map[string]interface{}{
		"ai.provider":                   "anthropic",
		"ai.provider_alias":             "anthropic.enterprise",
		"ai.surface":                    "messages",
		"ai.model":                      "deployment-sonnet",
		"ai.purpose":                    "planning",
		"ai.prompt_tokens":              13,
		"ai.completion_tokens":          21,
		"ai.total_tokens":               34,
		"ai.cached_input_tokens":        int64(5),
		"ai.reasoning_tokens":           int64(8),
		"ai.request.policy_fingerprint": "stable-fingerprint",
		"ai.request.adjusted_paths":     "/metadata,/temperature",
		"ai.request.adjustment_rules":   "application/tenant-policy,provider/sampling-policy",
	}
	for key, want := range wantAttributes {
		if got := span.attributes[key]; got != want {
			t.Errorf("span attribute %s = %#v, want %#v", key, got, want)
		}
	}
	spanText := fmt.Sprintf("%#v %#v", span.attributes, span.errors)
	for _, secret := range []string{"secret prompt body", "secret system prompt"} {
		if strings.Contains(spanText, secret) {
			t.Fatalf("logical span leaked %q: %s", secret, spanText)
		}
	}
}

func TestInstrumentedClient_Generate_LegacyRepresentability(t *testing.T) {
	response := &core.AIResponse{
		Content:  "legacy",
		Model:    "unpriced-model",
		Provider: "custom",
		Usage:    core.TokenUsage{PromptTokens: 1, TotalTokens: 1},
	}
	wrapped := &mockAIClientForInstr{generateResp: response}
	client := NewInstrumentedClient(wrapped, nil)

	request := core.NewAIRequest("prompt", "legacy-compatible")
	request.Generation.Temperature = core.SetAIParameter(float32(0.25))
	wantFingerprint, stable := client.RequestFingerprint(t.Context(), request)
	if !stable {
		t.Fatal("legacy preflight fingerprint is unstable")
	}
	result, err := client.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if result == nil || result.Response != response || wrapped.callCount != 1 {
		t.Fatalf("legacy result = %#v, calls %d", result, wrapped.callCount)
	}
	if result.RequestReport == nil || !result.RequestReport.Stable || result.RequestReport.Fingerprint != wantFingerprint {
		t.Fatalf("legacy request report fingerprint = %#v", result.RequestReport)
	}

	advanced := core.NewAIRequest("prompt", "advanced")
	advanced.Generation.TopP = core.SetAIParameter(float32(0.9))
	if _, err := client.Generate(t.Context(), advanced); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("advanced Generate() error = %v, want unsupported", err)
	}
	if wrapped.callCount != 1 {
		t.Fatalf("unsupported request reached legacy path: calls %d", wrapped.callCount)
	}
}

func TestInstrumentedClient_LogicalErrorIsSecretSafe(t *testing.T) {
	wrapped := &mockRequestAIClientForInstr{requestErr: errors.New("upstream included credential-secret")}
	tracing := &phase6InstrumentedTelemetry{}
	client := NewInstrumentedClient(wrapped, nil, WithInstrumentedTelemetry(tracing))

	if _, err := client.Generate(t.Context(), core.NewAIRequest("prompt-secret", "testing")); err == nil {
		t.Fatal("Generate() error = nil")
	}
	span := tracing.spans[0]
	if span.ended != 1 || len(span.errors) != 1 || span.errors[0].Error() != "AI provider request failed: unknown" {
		t.Fatalf("logical span errors = %#v, ended %d", span.errors, span.ended)
	}
	if span.attributes["ai.error_type"] != "unknown" {
		t.Fatalf("logical span error type = %#v", span.attributes["ai.error_type"])
	}
	spanText := fmt.Sprintf("%#v %#v", span.attributes, span.errors)
	for _, secret := range []string{"credential-secret", "prompt-secret"} {
		if strings.Contains(spanText, secret) {
			t.Fatalf("logical span leaked %q: %s", secret, spanText)
		}
	}
}

func TestInstrumentedClient_Stream_RequestCapabilityAndNilTelemetrySpan(t *testing.T) {
	response := &core.AIResponse{
		Content:  "streamed",
		Model:    "gpt-4o-mini",
		Provider: "openai",
		Usage:    core.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
	wrapped := &mockStreamingRequestAIClientForInstr{
		streamResult: &core.AIResult{Response: response},
	}
	tracing := &phase6InstrumentedTelemetry{nil: true}
	client := NewInstrumentedClient(
		wrapped,
		nil,
		WithInstrumentedTelemetry(tracing),
	)
	chunks := 0
	result, err := client.Stream(t.Context(), core.NewAIRequest("prompt", "streaming"), func(core.StreamChunk) error {
		chunks++
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if wrapped.streamCalls != 1 || chunks != 1 || result == nil || result.Response != response {
		t.Fatalf("stream result = %#v, calls %d, chunks %d", result, wrapped.streamCalls, chunks)
	}
	if len(tracing.names) != 1 || tracing.names[0] != "ai.stream" {
		t.Fatalf("logical stream spans = %#v", tracing.names)
	}
}

func TestInstrumentedClient_Generate_NilRequestDoesNoWork(t *testing.T) {
	wrapped := &mockRequestAIClientForInstr{}
	tracing := &phase6InstrumentedTelemetry{}
	client := NewInstrumentedClient(
		wrapped,
		nil,
		WithInstrumentedTelemetry(tracing),
	)
	if _, err := client.Generate(t.Context(), nil); err == nil || err.Error() != "AI request is nil" {
		t.Fatalf("Generate(nil) error = %v", err)
	}
	if wrapped.requestCalls != 0 || len(tracing.names) != 0 {
		t.Fatalf("nil request work = provider %d, spans %d", wrapped.requestCalls, len(tracing.names))
	}
}

func TestInstrumentedClient_Generate_DebugRecordUsesPresenceAwareIntent(t *testing.T) {
	wrapped := &mockRequestAIClientForInstr{generateResult: &core.AIResult{
		Response: &core.AIResponse{Content: "ok"},
	}}
	recorder := &mockRecorder{}
	client := NewInstrumentedClient(wrapped, recorder)
	request := core.NewAIRequestFromLegacy("prompt", "debug", &core.AIOptions{
		Temperature:  0.8,
		MaxTokens:    500,
		SystemPrompt: "legacy secret to omit",
	})
	request.Generation.Temperature = core.SetAIParameter(float32(0.2))
	request.Generation.MaxTokens = core.OmitAIParameter[int]()
	request.Generation.SystemPrompt = core.OmitAIParameter[string]()

	ctx := core.WithRequestID(t.Context(), "phase6-debug")
	if _, err := client.Generate(ctx, request); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if err := client.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	records := recorder.getRecords()
	if len(records) != 1 {
		t.Fatalf("debug records = %d", len(records))
	}
	if records[0].Temperature != float64(float32(0.2)) || records[0].MaxTokens != 0 || records[0].SystemPrompt != "" {
		t.Fatalf("presence-aware debug record = %#v", records[0])
	}
}

// --- Interface compliance ---

func TestInstrumentedClient_ImplementsAIClient(t *testing.T) {
	var _ core.AIClient = (*InstrumentedAIClient)(nil)
	var _ core.AIRequestClient = (*InstrumentedAIClient)(nil)
	var _ core.StreamingAIRequestClient = (*InstrumentedAIClient)(nil)
}
