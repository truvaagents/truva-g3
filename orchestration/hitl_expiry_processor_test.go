package orchestration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// =============================================================================
// Expiry Processor Unit Tests
// =============================================================================
//
// These tests cover the mode-aware expiry logic, configuration hierarchy,
// delivery semantics, and error handling for the HITL expiry processor.
//
// Per FRAMEWORK_DESIGN_PRINCIPLES.md: "Unit Test Coverage - Test all option
// combinations and precedence rules, error paths, edge cases."
// =============================================================================

// -----------------------------------------------------------------------------
// Test Helpers
// -----------------------------------------------------------------------------

// newTestStore creates a minimal RedisCheckpointStore for testing helper methods.
// This does NOT connect to Redis - it's for testing pure logic methods only.
func newTestStore() *RedisCheckpointStore {
	return &RedisCheckpointStore{
		logger: &core.NoOpLogger{},
	}
}

// newTestStoreWithLogger creates a test store with a capturing logger.
func newTestStoreWithLogger(logger *capturingLogger) *RedisCheckpointStore {
	return &RedisCheckpointStore{
		logger: logger,
	}
}

// capturingLogger captures log messages for test verification.
type capturingLogger struct {
	core.NoOpLogger
	warnMessages []logMessage
	mu           sync.Mutex
}

type logMessage struct {
	message string
	fields  map[string]interface{}
}

func (l *capturingLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnMessages = append(l.warnMessages, logMessage{message: msg, fields: fields})
}

func (l *capturingLogger) getWarnMessages() []logMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]logMessage{}, l.warnMessages...)
}

// -----------------------------------------------------------------------------
// getDefaultRequestMode Tests
// -----------------------------------------------------------------------------

func TestGetDefaultRequestMode_FromDecision(t *testing.T) {
	store := newTestStore()

	testCases := []struct {
		name     string
		decision *InterruptDecision
		expected RequestMode
	}{
		{
			name: "decision specifies streaming",
			decision: &InterruptDecision{
				DefaultRequestMode: RequestModeStreaming,
			},
			expected: RequestModeStreaming,
		},
		{
			name: "decision specifies non_streaming",
			decision: &InterruptDecision{
				DefaultRequestMode: RequestModeNonStreaming,
			},
			expected: RequestModeNonStreaming,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkpoint := &ExecutionCheckpoint{
				Decision: tc.decision,
			}
			result := store.getDefaultRequestMode(checkpoint)
			if result != tc.expected {
				t.Errorf("getDefaultRequestMode() = %q, want %q", result, tc.expected)
			}
		})
	}
}

func TestGetDefaultRequestMode_FromEnvVar(t *testing.T) {
	store := newTestStore()

	testCases := []struct {
		name     string
		envValue string
		expected RequestMode
	}{
		{
			name:     "env var set to streaming",
			envValue: "streaming",
			expected: RequestModeStreaming,
		},
		{
			name:     "env var set to non_streaming",
			envValue: "non_streaming",
			expected: RequestModeNonStreaming,
		},
		{
			name:     "invalid env var uses default",
			envValue: "invalid",
			expected: RequestModeNonStreaming, // Default
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRUVAG3_HITL_DEFAULT_REQUEST_MODE", tc.envValue)

			checkpoint := &ExecutionCheckpoint{
				Decision: nil, // No decision, should fall through to env var
			}
			result := store.getDefaultRequestMode(checkpoint)
			if result != tc.expected {
				t.Errorf("getDefaultRequestMode() with env=%q = %q, want %q", tc.envValue, result, tc.expected)
			}
		})
	}
}

func TestGetDefaultRequestMode_DefaultsToNonStreaming(t *testing.T) {
	store := newTestStore()

	// Clear env var
	t.Setenv("TRUVAG3_HITL_DEFAULT_REQUEST_MODE", "")

	checkpoint := &ExecutionCheckpoint{
		Decision: nil,
	}
	result := store.getDefaultRequestMode(checkpoint)
	if result != RequestModeNonStreaming {
		t.Errorf("getDefaultRequestMode() with no config = %q, want %q", result, RequestModeNonStreaming)
	}
}

func TestGetDefaultRequestMode_DecisionOverridesEnvVar(t *testing.T) {
	store := newTestStore()

	// Set env var to streaming
	t.Setenv("TRUVAG3_HITL_DEFAULT_REQUEST_MODE", "streaming")

	// Decision says non_streaming - should win
	checkpoint := &ExecutionCheckpoint{
		Decision: &InterruptDecision{
			DefaultRequestMode: RequestModeNonStreaming,
		},
	}
	result := store.getDefaultRequestMode(checkpoint)
	if result != RequestModeNonStreaming {
		t.Errorf("Decision should override env var: got %q, want %q", result, RequestModeNonStreaming)
	}
}

// -----------------------------------------------------------------------------
// getEffectiveRequestMode Tests
// -----------------------------------------------------------------------------

func TestGetEffectiveRequestMode_UsesCheckpointMode(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	checkpoint := &ExecutionCheckpoint{
		RequestMode: RequestModeStreaming,
	}
	result := store.getEffectiveRequestMode(ctx, checkpoint)
	if result != RequestModeStreaming {
		t.Errorf("getEffectiveRequestMode() = %q, want %q", result, RequestModeStreaming)
	}
}

func TestGetEffectiveRequestMode_LogsWarnWhenUsingDefault(t *testing.T) {
	logger := &capturingLogger{}
	store := newTestStoreWithLogger(logger)
	ctx := context.Background()

	// Clear env var to ensure default is used
	t.Setenv("TRUVAG3_HITL_DEFAULT_REQUEST_MODE", "")

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-123",
		RequestID:    "req-456",
		RequestMode:  "", // Not set - should use default and log warning
	}

	result := store.getEffectiveRequestMode(ctx, checkpoint)

	// Should return default
	if result != RequestModeNonStreaming {
		t.Errorf("getEffectiveRequestMode() = %q, want %q", result, RequestModeNonStreaming)
	}

	// Should have logged a warning
	warnMsgs := logger.getWarnMessages()
	if len(warnMsgs) != 1 {
		t.Fatalf("Expected 1 warning message, got %d", len(warnMsgs))
	}

	if warnMsgs[0].message != "RequestMode not set, using default behavior" {
		t.Errorf("Unexpected warning message: %q", warnMsgs[0].message)
	}

	// Verify fields include checkpoint_id and request_id
	if warnMsgs[0].fields["checkpoint_id"] != "cp-123" {
		t.Errorf("Warning missing checkpoint_id field")
	}
	if warnMsgs[0].fields["request_id"] != "req-456" {
		t.Errorf("Warning missing request_id field")
	}
}

func TestGetEffectiveRequestMode_NoWarnWhenModeSet(t *testing.T) {
	logger := &capturingLogger{}
	store := newTestStoreWithLogger(logger)
	ctx := context.Background()

	checkpoint := &ExecutionCheckpoint{
		RequestMode: RequestModeStreaming, // Explicitly set
	}

	store.getEffectiveRequestMode(ctx, checkpoint)

	// Should NOT have logged a warning
	warnMsgs := logger.getWarnMessages()
	if len(warnMsgs) != 0 {
		t.Errorf("Expected no warning messages when mode is set, got %d", len(warnMsgs))
	}
}

// -----------------------------------------------------------------------------
// getStreamingExpiryBehavior Tests
// -----------------------------------------------------------------------------

func TestGetStreamingExpiryBehavior_FromDecision(t *testing.T) {
	store := newTestStore()

	testCases := []struct {
		name     string
		decision *InterruptDecision
		expected StreamingExpiryBehavior
	}{
		{
			name: "decision specifies implicit_deny",
			decision: &InterruptDecision{
				StreamingExpiryBehavior: StreamingExpiryImplicitDeny,
			},
			expected: StreamingExpiryImplicitDeny,
		},
		{
			name: "decision specifies apply_default",
			decision: &InterruptDecision{
				StreamingExpiryBehavior: StreamingExpiryApplyDefault,
			},
			expected: StreamingExpiryApplyDefault,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkpoint := &ExecutionCheckpoint{
				Decision: tc.decision,
			}
			result := store.getStreamingExpiryBehavior(checkpoint)
			if result != tc.expected {
				t.Errorf("getStreamingExpiryBehavior() = %q, want %q", result, tc.expected)
			}
		})
	}
}

func TestGetStreamingExpiryBehavior_FromEnvVar(t *testing.T) {
	store := newTestStore()

	testCases := []struct {
		name     string
		envValue string
		expected StreamingExpiryBehavior
	}{
		{
			name:     "env var set to implicit_deny",
			envValue: "implicit_deny",
			expected: StreamingExpiryImplicitDeny,
		},
		{
			name:     "env var set to apply_default",
			envValue: "apply_default",
			expected: StreamingExpiryApplyDefault,
		},
		{
			name:     "invalid env var uses default",
			envValue: "invalid",
			expected: StreamingExpiryImplicitDeny, // Default for streaming
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRUVAG3_HITL_STREAMING_EXPIRY", tc.envValue)

			checkpoint := &ExecutionCheckpoint{
				Decision: nil,
			}
			result := store.getStreamingExpiryBehavior(checkpoint)
			if result != tc.expected {
				t.Errorf("getStreamingExpiryBehavior() with env=%q = %q, want %q", tc.envValue, result, tc.expected)
			}
		})
	}
}

func TestGetStreamingExpiryBehavior_DefaultsToImplicitDeny(t *testing.T) {
	store := newTestStore()

	t.Setenv("TRUVAG3_HITL_STREAMING_EXPIRY", "")

	checkpoint := &ExecutionCheckpoint{
		Decision: nil,
	}
	result := store.getStreamingExpiryBehavior(checkpoint)
	if result != StreamingExpiryImplicitDeny {
		t.Errorf("getStreamingExpiryBehavior() default = %q, want %q", result, StreamingExpiryImplicitDeny)
	}
}

func TestGetStreamingExpiryBehavior_DecisionOverridesEnvVar(t *testing.T) {
	store := newTestStore()

	t.Setenv("TRUVAG3_HITL_STREAMING_EXPIRY", "implicit_deny")

	checkpoint := &ExecutionCheckpoint{
		Decision: &InterruptDecision{
			StreamingExpiryBehavior: StreamingExpiryApplyDefault,
		},
	}
	result := store.getStreamingExpiryBehavior(checkpoint)
	if result != StreamingExpiryApplyDefault {
		t.Errorf("Decision should override env var: got %q, want %q", result, StreamingExpiryApplyDefault)
	}
}

// -----------------------------------------------------------------------------
// getNonStreamingExpiryBehavior Tests
// -----------------------------------------------------------------------------

func TestGetNonStreamingExpiryBehavior_FromDecision(t *testing.T) {
	store := newTestStore()

	testCases := []struct {
		name     string
		decision *InterruptDecision
		expected NonStreamingExpiryBehavior
	}{
		{
			name: "decision specifies apply_default",
			decision: &InterruptDecision{
				NonStreamingExpiryBehavior: NonStreamingExpiryApplyDefault,
			},
			expected: NonStreamingExpiryApplyDefault,
		},
		{
			name: "decision specifies implicit_deny",
			decision: &InterruptDecision{
				NonStreamingExpiryBehavior: NonStreamingExpiryImplicitDeny,
			},
			expected: NonStreamingExpiryImplicitDeny,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkpoint := &ExecutionCheckpoint{
				Decision: tc.decision,
			}
			result := store.getNonStreamingExpiryBehavior(checkpoint)
			if result != tc.expected {
				t.Errorf("getNonStreamingExpiryBehavior() = %q, want %q", result, tc.expected)
			}
		})
	}
}

func TestGetNonStreamingExpiryBehavior_FromEnvVar(t *testing.T) {
	store := newTestStore()

	testCases := []struct {
		name     string
		envValue string
		expected NonStreamingExpiryBehavior
	}{
		{
			name:     "env var set to apply_default",
			envValue: "apply_default",
			expected: NonStreamingExpiryApplyDefault,
		},
		{
			name:     "env var set to implicit_deny",
			envValue: "implicit_deny",
			expected: NonStreamingExpiryImplicitDeny,
		},
		{
			name:     "invalid env var uses default",
			envValue: "invalid",
			expected: NonStreamingExpiryApplyDefault, // Default for non-streaming
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY", tc.envValue)

			checkpoint := &ExecutionCheckpoint{
				Decision: nil,
			}
			result := store.getNonStreamingExpiryBehavior(checkpoint)
			if result != tc.expected {
				t.Errorf("getNonStreamingExpiryBehavior() with env=%q = %q, want %q", tc.envValue, result, tc.expected)
			}
		})
	}
}

func TestGetNonStreamingExpiryBehavior_DefaultsToApplyDefault(t *testing.T) {
	store := newTestStore()

	t.Setenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY", "")

	checkpoint := &ExecutionCheckpoint{
		Decision: nil,
	}
	result := store.getNonStreamingExpiryBehavior(checkpoint)
	if result != NonStreamingExpiryApplyDefault {
		t.Errorf("getNonStreamingExpiryBehavior() default = %q, want %q", result, NonStreamingExpiryApplyDefault)
	}
}

func TestGetNonStreamingExpiryBehavior_DecisionOverridesEnvVar(t *testing.T) {
	store := newTestStore()

	t.Setenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY", "apply_default")

	checkpoint := &ExecutionCheckpoint{
		Decision: &InterruptDecision{
			NonStreamingExpiryBehavior: NonStreamingExpiryImplicitDeny,
		},
	}
	result := store.getNonStreamingExpiryBehavior(checkpoint)
	if result != NonStreamingExpiryImplicitDeny {
		t.Errorf("Decision should override env var: got %q, want %q", result, NonStreamingExpiryImplicitDeny)
	}
}

// -----------------------------------------------------------------------------
// shouldApplyDefaultAction Tests
// -----------------------------------------------------------------------------

func TestShouldApplyDefaultAction_Streaming(t *testing.T) {
	store := newTestStore()

	// Clear env vars
	t.Setenv("TRUVAG3_HITL_STREAMING_EXPIRY", "")

	testCases := []struct {
		name        string
		behavior    StreamingExpiryBehavior
		shouldApply bool
	}{
		{
			name:        "implicit_deny returns false",
			behavior:    StreamingExpiryImplicitDeny,
			shouldApply: false,
		},
		{
			name:        "apply_default returns true",
			behavior:    StreamingExpiryApplyDefault,
			shouldApply: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkpoint := &ExecutionCheckpoint{
				Decision: &InterruptDecision{
					StreamingExpiryBehavior: tc.behavior,
				},
			}
			result := store.shouldApplyDefaultAction(checkpoint, RequestModeStreaming)
			if result != tc.shouldApply {
				t.Errorf("shouldApplyDefaultAction() = %v, want %v", result, tc.shouldApply)
			}
		})
	}
}

func TestShouldApplyDefaultAction_NonStreaming(t *testing.T) {
	store := newTestStore()

	// Clear env vars
	t.Setenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY", "")

	testCases := []struct {
		name        string
		behavior    NonStreamingExpiryBehavior
		shouldApply bool
	}{
		{
			name:        "apply_default returns true",
			behavior:    NonStreamingExpiryApplyDefault,
			shouldApply: true,
		},
		{
			name:        "implicit_deny returns false",
			behavior:    NonStreamingExpiryImplicitDeny,
			shouldApply: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkpoint := &ExecutionCheckpoint{
				Decision: &InterruptDecision{
					NonStreamingExpiryBehavior: tc.behavior,
				},
			}
			result := store.shouldApplyDefaultAction(checkpoint, RequestModeNonStreaming)
			if result != tc.shouldApply {
				t.Errorf("shouldApplyDefaultAction() = %v, want %v", result, tc.shouldApply)
			}
		})
	}
}

func TestShouldApplyDefaultAction_DefaultBehaviors(t *testing.T) {
	store := newTestStore()

	// Clear env vars to use defaults
	t.Setenv("TRUVAG3_HITL_STREAMING_EXPIRY", "")
	t.Setenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY", "")

	checkpoint := &ExecutionCheckpoint{
		Decision: nil, // No decision, use defaults
	}

	// Streaming default: implicit_deny (don't apply)
	result := store.shouldApplyDefaultAction(checkpoint, RequestModeStreaming)
	if result != false {
		t.Errorf("Streaming default should be implicit_deny (false), got %v", result)
	}

	// Non-streaming default: apply_default (do apply)
	result = store.shouldApplyDefaultAction(checkpoint, RequestModeNonStreaming)
	if result != true {
		t.Errorf("Non-streaming default should be apply_default (true), got %v", result)
	}
}

// -----------------------------------------------------------------------------
// determineExpiryAction Tests
// -----------------------------------------------------------------------------

func TestDetermineExpiryAction_FromDecision(t *testing.T) {
	store := newTestStore()

	testCases := []struct {
		name          string
		defaultAction CommandType
	}{
		{"approve from decision", CommandApprove},
		{"reject from decision", CommandReject},
		{"abort from decision", CommandAbort},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkpoint := &ExecutionCheckpoint{
				Decision: &InterruptDecision{
					DefaultAction: tc.defaultAction,
				},
			}
			result := store.determineExpiryAction(checkpoint)
			if result != tc.defaultAction {
				t.Errorf("determineExpiryAction() = %q, want %q", result, tc.defaultAction)
			}
		})
	}
}

func TestDetermineExpiryAction_FallbackByInterruptPoint(t *testing.T) {
	store := newTestStore()

	// Design Decision (2026-01-24): HITL enabled = require explicit approval.
	// All checkpoints default to reject on expiry (fail-safe), except errors which abort.
	testCases := []struct {
		name           string
		interruptPoint InterruptPoint
		expected       CommandType
	}{
		{
			name:           "plan_generated -> reject (HITL = explicit approval)",
			interruptPoint: InterruptPointPlanGenerated,
			expected:       CommandReject,
		},
		{
			name:           "before_step -> reject (fail-safe)",
			interruptPoint: InterruptPointBeforeStep,
			expected:       CommandReject,
		},
		{
			name:           "after_step -> reject (HITL = explicit approval)",
			interruptPoint: InterruptPointAfterStep,
			expected:       CommandReject,
		},
		{
			name:           "on_error -> abort",
			interruptPoint: InterruptPointOnError,
			expected:       CommandAbort,
		},
		{
			name:           "unknown -> reject (fail-safe)",
			interruptPoint: InterruptPoint("unknown"),
			expected:       CommandReject,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkpoint := &ExecutionCheckpoint{
				InterruptPoint: tc.interruptPoint,
				Decision:       nil, // No decision, use fallback
			}
			result := store.determineExpiryAction(checkpoint)
			if result != tc.expected {
				t.Errorf("determineExpiryAction() for %q = %q, want %q",
					tc.interruptPoint, result, tc.expected)
			}
		})
	}
}

func TestDetermineExpiryAction_DecisionOverridesFallback(t *testing.T) {
	store := newTestStore()

	// InterruptPointOnError would normally return CommandAbort
	// But decision says approve
	checkpoint := &ExecutionCheckpoint{
		InterruptPoint: InterruptPointOnError,
		Decision: &InterruptDecision{
			DefaultAction: CommandApprove,
		},
	}
	result := store.determineExpiryAction(checkpoint)
	if result != CommandApprove {
		t.Errorf("Decision should override fallback: got %q, want %q", result, CommandApprove)
	}
}

// -----------------------------------------------------------------------------
// actionToExpiredStatus Tests
// -----------------------------------------------------------------------------

func TestActionToExpiredStatus(t *testing.T) {
	store := newTestStore()

	testCases := []struct {
		action   CommandType
		expected CheckpointStatus
	}{
		{CommandApprove, CheckpointStatusExpiredApproved},
		{CommandReject, CheckpointStatusExpiredRejected},
		{CommandAbort, CheckpointStatusExpiredAborted},
		{CommandType("unknown"), CheckpointStatusExpiredRejected}, // Default: fail-safe
		{CommandType(""), CheckpointStatusExpiredRejected},        // Empty: fail-safe
	}

	for _, tc := range testCases {
		t.Run(string(tc.action), func(t *testing.T) {
			result := store.actionToExpiredStatus(tc.action)
			if result != tc.expected {
				t.Errorf("actionToExpiredStatus(%q) = %q, want %q", tc.action, result, tc.expected)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// invokeCallbackSafely Tests (Panic Recovery)
// -----------------------------------------------------------------------------

func TestInvokeCallbackSafely_SuccessfulCallback(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	callbackCalled := false
	callback := func(ctx context.Context, cp *ExecutionCheckpoint, action CommandType) {
		callbackCalled = true
	}

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "test-cp",
	}

	success := store.invokeCallbackSafely(ctx, checkpoint, CommandApprove, callback)

	if !success {
		t.Error("invokeCallbackSafely() should return true for successful callback")
	}
	if !callbackCalled {
		t.Error("Callback should have been called")
	}
}

func TestInvokeCallbackSafely_PanicRecovery(t *testing.T) {
	logger := &capturingLogger{}
	store := newTestStoreWithLogger(logger)
	ctx := context.Background()

	panicCallback := func(ctx context.Context, cp *ExecutionCheckpoint, action CommandType) {
		panic("intentional test panic")
	}

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "test-cp",
		RequestID:    "test-req",
	}

	// Should NOT panic - should recover
	success := store.invokeCallbackSafely(ctx, checkpoint, CommandApprove, panicCallback)

	if success {
		t.Error("invokeCallbackSafely() should return false when callback panics")
	}
}

func TestInvokeCallbackSafely_RecordsMetricOnPanic(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	panicCallback := func(ctx context.Context, cp *ExecutionCheckpoint, action CommandType) {
		panic("test panic for metric")
	}

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "test-cp",
	}

	// This should not panic
	store.invokeCallbackSafely(ctx, checkpoint, CommandApprove, panicCallback)

	// Metric recording is verified by hitl_metrics_test.go
	// Here we just ensure the function completes without propagating the panic
}

func TestInvokeCallbackSafely_NilCallback(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "test-cp",
	}

	// Calling with nil callback - this will panic but should be recovered
	// Note: In production code, we check for nil callback before calling
	// This test verifies the panic recovery works even for nil pointer deref
	defer func() {
		if r := recover(); r != nil {
			t.Error("invokeCallbackSafely should recover from all panics including nil callback")
		}
	}()

	success := store.invokeCallbackSafely(ctx, checkpoint, CommandApprove, nil)
	if success {
		t.Error("invokeCallbackSafely with nil callback should return false")
	}
}

// -----------------------------------------------------------------------------
// Delivery Semantics Tests
// -----------------------------------------------------------------------------

func TestDeliverySemantics_Constants(t *testing.T) {
	// Verify constants are defined correctly
	if DeliveryAtMostOnce != "at_most_once" {
		t.Errorf("DeliveryAtMostOnce = %q, want %q", DeliveryAtMostOnce, "at_most_once")
	}
	if DeliveryAtLeastOnce != "at_least_once" {
		t.Errorf("DeliveryAtLeastOnce = %q, want %q", DeliveryAtLeastOnce, "at_least_once")
	}
}

func TestExpiryProcessorConfig_DeliverySemantics(t *testing.T) {
	testCases := []struct {
		name     string
		config   ExpiryProcessorConfig
		expected DeliverySemantics
	}{
		{
			name: "explicit at_most_once",
			config: ExpiryProcessorConfig{
				Enabled:           true,
				DeliverySemantics: DeliveryAtMostOnce,
			},
			expected: DeliveryAtMostOnce,
		},
		{
			name: "explicit at_least_once",
			config: ExpiryProcessorConfig{
				Enabled:           true,
				DeliverySemantics: DeliveryAtLeastOnce,
			},
			expected: DeliveryAtLeastOnce,
		},
		{
			name: "empty defaults to at_most_once via WithExpiryProcessor",
			config: ExpiryProcessorConfig{
				Enabled:           true,
				DeliverySemantics: "", // Empty
			},
			expected: DeliveryAtMostOnce,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := HITLConfig{}
			WithExpiryProcessor(tc.config)(&config)

			if config.ExpiryProcessor.DeliverySemantics != tc.expected {
				t.Errorf("DeliverySemantics = %q, want %q",
					config.ExpiryProcessor.DeliverySemantics, tc.expected)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Graceful Shutdown Tests
// -----------------------------------------------------------------------------

func TestStopExpiryProcessor_NotStarted(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	// Stopping when not started should be a no-op
	err := store.StopExpiryProcessor(ctx)
	if err != nil {
		t.Errorf("StopExpiryProcessor() when not started should not error, got: %v", err)
	}
}

func TestStopExpiryProcessor_ContextTimeout(t *testing.T) {
	store := newTestStore()

	// Simulate started state with a cancel function that won't complete
	store.expiryCancel = func() {}
	store.expiryWg.Add(1) // Simulate a goroutine that won't finish

	// Use a very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := store.StopExpiryProcessor(ctx)
	if err == nil {
		t.Error("StopExpiryProcessor() should error on context timeout")
	}

	// Clean up
	store.expiryWg.Done()
}

// -----------------------------------------------------------------------------
// Configuration Hierarchy Tests (Decision > Env > Default)
// -----------------------------------------------------------------------------

func TestConfigHierarchy_StreamingExpiry(t *testing.T) {
	store := newTestStore()

	// Test case: Decision > Env > Default
	t.Run("decision overrides env and default", func(t *testing.T) {
		t.Setenv("TRUVAG3_HITL_STREAMING_EXPIRY", "implicit_deny")

		checkpoint := &ExecutionCheckpoint{
			Decision: &InterruptDecision{
				StreamingExpiryBehavior: StreamingExpiryApplyDefault,
			},
		}
		result := store.getStreamingExpiryBehavior(checkpoint)
		if result != StreamingExpiryApplyDefault {
			t.Errorf("Decision should win: got %q", result)
		}
	})

	t.Run("env overrides default", func(t *testing.T) {
		t.Setenv("TRUVAG3_HITL_STREAMING_EXPIRY", "apply_default")

		checkpoint := &ExecutionCheckpoint{
			Decision: nil,
		}
		result := store.getStreamingExpiryBehavior(checkpoint)
		if result != StreamingExpiryApplyDefault {
			t.Errorf("Env should override default: got %q", result)
		}
	})

	t.Run("default when nothing set", func(t *testing.T) {
		t.Setenv("TRUVAG3_HITL_STREAMING_EXPIRY", "")

		checkpoint := &ExecutionCheckpoint{
			Decision: nil,
		}
		result := store.getStreamingExpiryBehavior(checkpoint)
		if result != StreamingExpiryImplicitDeny {
			t.Errorf("Default should be implicit_deny: got %q", result)
		}
	})
}

func TestConfigHierarchy_NonStreamingExpiry(t *testing.T) {
	store := newTestStore()

	t.Run("decision overrides env and default", func(t *testing.T) {
		t.Setenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY", "apply_default")

		checkpoint := &ExecutionCheckpoint{
			Decision: &InterruptDecision{
				NonStreamingExpiryBehavior: NonStreamingExpiryImplicitDeny,
			},
		}
		result := store.getNonStreamingExpiryBehavior(checkpoint)
		if result != NonStreamingExpiryImplicitDeny {
			t.Errorf("Decision should win: got %q", result)
		}
	})

	t.Run("env overrides default", func(t *testing.T) {
		t.Setenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY", "implicit_deny")

		checkpoint := &ExecutionCheckpoint{
			Decision: nil,
		}
		result := store.getNonStreamingExpiryBehavior(checkpoint)
		if result != NonStreamingExpiryImplicitDeny {
			t.Errorf("Env should override default: got %q", result)
		}
	})

	t.Run("default when nothing set", func(t *testing.T) {
		t.Setenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY", "")

		checkpoint := &ExecutionCheckpoint{
			Decision: nil,
		}
		result := store.getNonStreamingExpiryBehavior(checkpoint)
		if result != NonStreamingExpiryApplyDefault {
			t.Errorf("Default should be apply_default: got %q", result)
		}
	})
}

func TestConfigHierarchy_DefaultRequestMode(t *testing.T) {
	store := newTestStore()

	t.Run("decision overrides env and default", func(t *testing.T) {
		t.Setenv("TRUVAG3_HITL_DEFAULT_REQUEST_MODE", "non_streaming")

		checkpoint := &ExecutionCheckpoint{
			Decision: &InterruptDecision{
				DefaultRequestMode: RequestModeStreaming,
			},
		}
		result := store.getDefaultRequestMode(checkpoint)
		if result != RequestModeStreaming {
			t.Errorf("Decision should win: got %q", result)
		}
	})

	t.Run("env overrides default", func(t *testing.T) {
		t.Setenv("TRUVAG3_HITL_DEFAULT_REQUEST_MODE", "streaming")

		checkpoint := &ExecutionCheckpoint{
			Decision: nil,
		}
		result := store.getDefaultRequestMode(checkpoint)
		if result != RequestModeStreaming {
			t.Errorf("Env should override default: got %q", result)
		}
	})

	t.Run("default when nothing set", func(t *testing.T) {
		t.Setenv("TRUVAG3_HITL_DEFAULT_REQUEST_MODE", "")

		checkpoint := &ExecutionCheckpoint{
			Decision: nil,
		}
		result := store.getDefaultRequestMode(checkpoint)
		if result != RequestModeNonStreaming {
			t.Errorf("Default should be non_streaming: got %q", result)
		}
	})
}

// -----------------------------------------------------------------------------
// Error Message Actionable Hints Tests
// -----------------------------------------------------------------------------

func TestValidateExpiryConfig_ErrorMessages(t *testing.T) {
	testCases := []struct {
		name         string
		config       ExpiryProcessorConfig
		expectedHint string
	}{
		{
			name: "scan interval error has hint",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 500 * time.Millisecond,
			},
			expectedHint: "use 0 for default",
		},
		{
			name: "negative batch size error has hint",
			config: ExpiryProcessorConfig{
				Enabled:   true,
				BatchSize: -1,
			},
			expectedHint: "use 0 for default",
		},
		{
			name: "excessive batch size error has hint",
			config: ExpiryProcessorConfig{
				Enabled:   true,
				BatchSize: 20000,
			},
			expectedHint: "memory issues",
		},
		{
			name: "invalid delivery semantics error has hint",
			config: ExpiryProcessorConfig{
				Enabled:           true,
				ScanInterval:      1 * time.Second,
				DeliverySemantics: "invalid",
			},
			expectedHint: "valid values",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExpiryConfig(tc.config)
			if err == nil {
				t.Fatal("Expected error but got nil")
			}

			errMsg := err.Error()
			if !containsSubstring(errMsg, tc.expectedHint) {
				t.Errorf("Error message should contain %q, got: %s", tc.expectedHint, errMsg)
			}
		})
	}
}

// containsSubstring checks if s contains substr
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && hasSubstring(s, substr)))
}

func hasSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Mode-Aware Expiry Logic Integration Tests
// -----------------------------------------------------------------------------

func TestModeAwareExpiry_StreamingWithImplicitDeny(t *testing.T) {
	store := newTestStore()

	// Clear env vars
	t.Setenv("TRUVAG3_HITL_STREAMING_EXPIRY", "")

	checkpoint := &ExecutionCheckpoint{
		RequestMode: RequestModeStreaming,
		Decision:    nil, // Use defaults
	}

	// Should NOT apply default action (implicit deny is default for streaming)
	shouldApply := store.shouldApplyDefaultAction(checkpoint, checkpoint.RequestMode)
	if shouldApply {
		t.Error("Streaming with implicit_deny (default) should NOT apply default action")
	}
}

func TestModeAwareExpiry_StreamingWithApplyDefault(t *testing.T) {
	store := newTestStore()

	checkpoint := &ExecutionCheckpoint{
		RequestMode: RequestModeStreaming,
		Decision: &InterruptDecision{
			StreamingExpiryBehavior: StreamingExpiryApplyDefault,
		},
	}

	shouldApply := store.shouldApplyDefaultAction(checkpoint, checkpoint.RequestMode)
	if !shouldApply {
		t.Error("Streaming with apply_default should apply default action")
	}
}

func TestModeAwareExpiry_NonStreamingWithApplyDefault(t *testing.T) {
	store := newTestStore()

	// Clear env vars
	t.Setenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY", "")

	checkpoint := &ExecutionCheckpoint{
		RequestMode: RequestModeNonStreaming,
		Decision:    nil, // Use defaults
	}

	// Should apply default action (apply_default is default for non-streaming)
	shouldApply := store.shouldApplyDefaultAction(checkpoint, checkpoint.RequestMode)
	if !shouldApply {
		t.Error("Non-streaming with apply_default (default) should apply default action")
	}
}

func TestModeAwareExpiry_NonStreamingWithImplicitDeny(t *testing.T) {
	store := newTestStore()

	checkpoint := &ExecutionCheckpoint{
		RequestMode: RequestModeNonStreaming,
		Decision: &InterruptDecision{
			NonStreamingExpiryBehavior: NonStreamingExpiryImplicitDeny,
		},
	}

	shouldApply := store.shouldApplyDefaultAction(checkpoint, checkpoint.RequestMode)
	if shouldApply {
		t.Error("Non-streaming with implicit_deny should NOT apply default action")
	}
}

// -----------------------------------------------------------------------------
// validateExpiryConfig Additional Edge Cases
// -----------------------------------------------------------------------------

func TestValidateExpiryConfig_EdgeCases(t *testing.T) {
	testCases := []struct {
		name      string
		config    ExpiryProcessorConfig
		wantError bool
	}{
		{
			name: "exactly 1 second is valid",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 1 * time.Second,
			},
			wantError: false,
		},
		{
			name: "999 milliseconds is invalid",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 999 * time.Millisecond,
			},
			wantError: true,
		},
		{
			name: "exactly 10000 batch size is valid",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 1 * time.Second,
				BatchSize:    10000,
			},
			wantError: false,
		},
		{
			name: "10001 batch size is invalid",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 1 * time.Second,
				BatchSize:    10001,
			},
			wantError: true,
		},
		{
			name: "zero batch size is valid (uses default)",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 1 * time.Second,
				BatchSize:    0,
			},
			wantError: false,
		},
		{
			name: "empty delivery semantics is valid (uses default)",
			config: ExpiryProcessorConfig{
				Enabled:           true,
				ScanInterval:      1 * time.Second,
				DeliverySemantics: "",
			},
			wantError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExpiryConfig(tc.config)
			if tc.wantError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tc.wantError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}
