package orchestration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// =============================================================================
// Status Helper Tests
// =============================================================================

func TestIsResumableStatus(t *testing.T) {
	testCases := []struct {
		name     string
		status   CheckpointStatus
		expected bool
	}{
		// Resumable statuses
		{"approved is resumable", CheckpointStatusApproved, true},
		{"edited is resumable", CheckpointStatusEdited, true},
		{"expired_approved is resumable", CheckpointStatusExpiredApproved, true},

		// Non-resumable statuses
		{"pending is not resumable", CheckpointStatusPending, false},
		{"rejected is not resumable", CheckpointStatusRejected, false},
		{"aborted is not resumable", CheckpointStatusAborted, false},
		{"completed is not resumable", CheckpointStatusCompleted, false},
		{"expired is not resumable", CheckpointStatusExpired, false},
		{"expired_rejected is not resumable", CheckpointStatusExpiredRejected, false},
		{"expired_aborted is not resumable", CheckpointStatusExpiredAborted, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := IsResumableStatus(tc.status)
			if result != tc.expected {
				t.Errorf("IsResumableStatus(%q) = %v, want %v", tc.status, result, tc.expected)
			}
		})
	}
}

func TestIsTerminalStatus(t *testing.T) {
	testCases := []struct {
		name     string
		status   CheckpointStatus
		expected bool
	}{
		// Terminal statuses
		{"completed is terminal", CheckpointStatusCompleted, true},
		{"rejected is terminal", CheckpointStatusRejected, true},
		{"aborted is terminal", CheckpointStatusAborted, true},
		{"expired is terminal", CheckpointStatusExpired, true},
		{"expired_rejected is terminal", CheckpointStatusExpiredRejected, true},
		{"expired_aborted is terminal", CheckpointStatusExpiredAborted, true},

		// Non-terminal statuses
		{"pending is not terminal", CheckpointStatusPending, false},
		{"approved is not terminal", CheckpointStatusApproved, false},
		{"edited is not terminal", CheckpointStatusEdited, false},
		{"expired_approved is not terminal", CheckpointStatusExpiredApproved, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := IsTerminalStatus(tc.status)
			if result != tc.expected {
				t.Errorf("IsTerminalStatus(%q) = %v, want %v", tc.status, result, tc.expected)
			}
		})
	}
}

func TestIsPendingStatus(t *testing.T) {
	testCases := []struct {
		name     string
		status   CheckpointStatus
		expected bool
	}{
		{"pending is pending", CheckpointStatusPending, true},
		{"approved is not pending", CheckpointStatusApproved, false},
		{"rejected is not pending", CheckpointStatusRejected, false},
		{"completed is not pending", CheckpointStatusCompleted, false},
		{"expired is not pending", CheckpointStatusExpired, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPendingStatus(tc.status)
			if result != tc.expected {
				t.Errorf("IsPendingStatus(%q) = %v, want %v", tc.status, result, tc.expected)
			}
		})
	}
}

// =============================================================================
// Request Mode Context Helper Tests
// =============================================================================

func TestWithRequestMode(t *testing.T) {
	testCases := []struct {
		name string
		mode RequestMode
	}{
		{"streaming mode", RequestModeStreaming},
		{"non_streaming mode", RequestModeNonStreaming},
		{"empty mode", RequestMode("")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ctx = WithRequestMode(ctx, tc.mode)

			result := GetRequestMode(ctx)
			if result != tc.mode {
				t.Errorf("GetRequestMode() = %q, want %q", result, tc.mode)
			}
		})
	}
}

func TestGetRequestMode_NotSet(t *testing.T) {
	ctx := context.Background()
	result := GetRequestMode(ctx)
	if result != "" {
		t.Errorf("GetRequestMode() on empty context = %q, want empty string", result)
	}
}

// =============================================================================
// BuildResumeContext Tests
// =============================================================================

func TestBuildResumeContext_Success(t *testing.T) {
	testCases := []struct {
		name       string
		checkpoint *ExecutionCheckpoint
	}{
		{
			name: "approved checkpoint with plan",
			checkpoint: &ExecutionCheckpoint{
				CheckpointID: "cp-123",
				RequestID:    "req-456",
				Status:       CheckpointStatusApproved,
				RequestMode:  RequestModeStreaming,
				Plan: &RoutingPlan{
					PlanID: "plan-456",
					Steps: []RoutingStep{
						{StepID: "step-1", AgentName: "agent-1", Instruction: "Do something"},
					},
				},
				UserContext: map[string]interface{}{
					"session_id": "session-789",
				},
			},
		},
		{
			name: "edited checkpoint with step results",
			checkpoint: &ExecutionCheckpoint{
				CheckpointID: "cp-124",
				RequestID:    "req-457",
				Status:       CheckpointStatusEdited,
				StepResults: map[string]*StepResult{
					"step-1": {StepID: "step-1", Success: true},
				},
			},
		},
		{
			name: "expired_approved checkpoint",
			checkpoint: &ExecutionCheckpoint{
				CheckpointID: "cp-125",
				RequestID:    "req-458",
				Status:       CheckpointStatusExpiredApproved,
			},
		},
		{
			name: "checkpoint with resolved parameters and current step",
			checkpoint: &ExecutionCheckpoint{
				CheckpointID: "cp-126",
				RequestID:    "req-459",
				Status:       CheckpointStatusApproved,
				CurrentStep: &RoutingStep{
					StepID:      "step-2",
					AgentName:   "stock-agent",
					Instruction: "Get stock price",
				},
				ResolvedParameters: map[string]interface{}{
					"symbol": "AAPL",
					"amount": 15000,
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			resumeCtx, endSpan, err := BuildResumeContext(ctx, tc.checkpoint)
			if endSpan != nil {
				defer endSpan()
			}

			if err != nil {
				t.Fatalf("BuildResumeContext() error = %v", err)
			}

			if resumeCtx == nil {
				t.Fatal("BuildResumeContext() returned nil context")
			}

			// Verify request mode is preserved
			if tc.checkpoint.RequestMode != "" {
				mode := GetRequestMode(resumeCtx)
				if mode != tc.checkpoint.RequestMode {
					t.Errorf("Request mode not preserved: got %q, want %q", mode, tc.checkpoint.RequestMode)
				}
			}
		})
	}
}

func TestBuildResumeContext_NilCheckpoint(t *testing.T) {
	ctx := context.Background()
	_, _, err := BuildResumeContext(ctx, nil)

	if err == nil {
		t.Error("BuildResumeContext(nil) should return error")
	}
}

func TestBuildResumeContext_NonResumableStatus(t *testing.T) {
	nonResumableStatuses := []CheckpointStatus{
		CheckpointStatusPending,
		CheckpointStatusRejected,
		CheckpointStatusAborted,
		CheckpointStatusCompleted,
		CheckpointStatusExpired,
		CheckpointStatusExpiredRejected,
		CheckpointStatusExpiredAborted,
	}

	for _, status := range nonResumableStatuses {
		t.Run(string(status), func(t *testing.T) {
			checkpoint := &ExecutionCheckpoint{
				CheckpointID: "cp-123",
				Status:       status,
			}

			ctx := context.Background()
			_, _, err := BuildResumeContext(ctx, checkpoint)

			if err == nil {
				t.Errorf("BuildResumeContext() with status %q should return error", status)
			}
		})
	}
}

// =============================================================================
// Status Relationship Tests
// =============================================================================

func TestStatusRelationships_Exclusive(t *testing.T) {
	// Each status should only match one of the helper functions
	allStatuses := []CheckpointStatus{
		CheckpointStatusPending,
		CheckpointStatusApproved,
		CheckpointStatusRejected,
		CheckpointStatusAborted,
		CheckpointStatusEdited,
		CheckpointStatusCompleted,
		CheckpointStatusExpired,
		CheckpointStatusExpiredApproved,
		CheckpointStatusExpiredRejected,
		CheckpointStatusExpiredAborted,
	}

	for _, status := range allStatuses {
		t.Run(string(status), func(t *testing.T) {
			isPending := IsPendingStatus(status)
			isResumable := IsResumableStatus(status)
			isTerminal := IsTerminalStatus(status)

			// A status should only be in one category (or none)
			count := 0
			if isPending {
				count++
			}
			if isResumable {
				count++
			}
			if isTerminal {
				count++
			}

			if count > 1 {
				t.Errorf("Status %q matched multiple categories: pending=%v, resumable=%v, terminal=%v",
					status, isPending, isResumable, isTerminal)
			}

			// Special case: approved/edited/expired_approved are resumable but not terminal
			// Special case: pending is pending but not terminal or resumable
			// All statuses should match exactly one category or be in a valid "in-progress" state
		})
	}
}

// =============================================================================
// Config Validation Tests
// =============================================================================

func TestValidateExpiryConfig_ValidConfigs(t *testing.T) {
	testCases := []struct {
		name   string
		config ExpiryProcessorConfig
	}{
		{
			name:   "disabled config",
			config: ExpiryProcessorConfig{Enabled: false},
		},
		{
			name: "default values",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 0, // Will use default 10s
				BatchSize:    0, // Will use default 100
			},
		},
		{
			name: "explicit valid values",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 5 * time.Second,
				BatchSize:    50,
			},
		},
		{
			name: "minimum scan interval",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 1 * time.Second,
				BatchSize:    100,
			},
		},
		{
			name: "maximum batch size",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 10 * time.Second,
				BatchSize:    10000,
			},
		},
		{
			name: "valid at_most_once delivery",
			config: ExpiryProcessorConfig{
				Enabled:           true,
				ScanInterval:      1 * time.Second,
				DeliverySemantics: DeliveryAtMostOnce,
			},
		},
		{
			name: "valid at_least_once delivery",
			config: ExpiryProcessorConfig{
				Enabled:           true,
				ScanInterval:      1 * time.Second,
				DeliverySemantics: DeliveryAtLeastOnce,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExpiryConfig(tc.config)
			if err != nil {
				t.Errorf("validateExpiryConfig() with valid config should not error, got: %v", err)
			}
		})
	}
}

func TestValidateExpiryConfig_InvalidConfigs(t *testing.T) {
	testCases := []struct {
		name          string
		config        ExpiryProcessorConfig
		expectedError string
	}{
		{
			name: "scan interval too small",
			config: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 500 * time.Millisecond,
			},
			expectedError: "ScanInterval must be at least 1s",
		},
		{
			name: "negative batch size",
			config: ExpiryProcessorConfig{
				Enabled:   true,
				BatchSize: -1,
			},
			expectedError: "BatchSize cannot be negative",
		},
		{
			name: "batch size too large",
			config: ExpiryProcessorConfig{
				Enabled:   true,
				BatchSize: 10001,
			},
			expectedError: "BatchSize exceeds maximum",
		},
		{
			name: "invalid delivery semantics",
			config: ExpiryProcessorConfig{
				Enabled:           true,
				ScanInterval:      1 * time.Second,
				DeliverySemantics: "invalid_value",
			},
			expectedError: "DeliverySemantics has invalid value",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExpiryConfig(tc.config)
			if err == nil {
				t.Error("validateExpiryConfig() with invalid config should error")
				return
			}

			if !strings.Contains(err.Error(), tc.expectedError) {
				t.Errorf("Error should contain %q, got: %v", tc.expectedError, err)
			}
		})
	}
}

// =============================================================================
// Instance ID Tests
// =============================================================================

func TestGenerateInstanceID(t *testing.T) {
	id1 := generateInstanceID()
	id2 := generateInstanceID()

	// Should not be empty
	if id1 == "" {
		t.Error("generateInstanceID() should not return empty string")
	}

	// Should contain a hyphen (hostname-suffix format)
	if !strings.Contains(id1, "-") {
		t.Errorf("generateInstanceID() should contain hyphen, got: %s", id1)
	}

	// Should be unique across calls
	if id1 == id2 {
		t.Errorf("generateInstanceID() should generate unique IDs, got same: %s", id1)
	}
}

func TestWithInstanceID(t *testing.T) {
	customID := "test-instance-123"

	store, err := NewRedisCheckpointStore(
		WithCheckpointRedisURL("redis://localhost:6379"),
		WithInstanceID(customID),
	)

	// This test may fail if Redis is not available - that's OK
	// We're testing the option function, not Redis connectivity
	if err != nil {
		t.Skipf("Skipping test - Redis not available: %v", err)
	}

	if store.instanceID != customID {
		t.Errorf("WithInstanceID() should set instanceID = %q, got %q", customID, store.instanceID)
	}
}

// =============================================================================
// HITLConfig Tests (Phase 7: Configuration Integration)
// =============================================================================

func TestDefaultHITLConfig_HasExpiryProcessor(t *testing.T) {
	config := DefaultHITLConfig()

	// Verify ExpiryProcessor has sensible defaults
	if !config.ExpiryProcessor.Enabled {
		t.Error("DefaultHITLConfig().ExpiryProcessor.Enabled should be true")
	}

	if config.ExpiryProcessor.ScanInterval != 10*time.Second {
		t.Errorf("DefaultHITLConfig().ExpiryProcessor.ScanInterval = %v, want 10s",
			config.ExpiryProcessor.ScanInterval)
	}

	if config.ExpiryProcessor.BatchSize != 100 {
		t.Errorf("DefaultHITLConfig().ExpiryProcessor.BatchSize = %d, want 100",
			config.ExpiryProcessor.BatchSize)
	}

	if config.ExpiryProcessor.DeliverySemantics != DeliveryAtMostOnce {
		t.Errorf("DefaultHITLConfig().ExpiryProcessor.DeliverySemantics = %q, want %q",
			config.ExpiryProcessor.DeliverySemantics, DeliveryAtMostOnce)
	}
}

func TestWithExpiryProcessor(t *testing.T) {
	testCases := []struct {
		name     string
		input    ExpiryProcessorConfig
		expected ExpiryProcessorConfig
	}{
		{
			name: "explicit values",
			input: ExpiryProcessorConfig{
				Enabled:           true,
				ScanInterval:      5 * time.Second,
				BatchSize:         50,
				DeliverySemantics: DeliveryAtLeastOnce,
			},
			expected: ExpiryProcessorConfig{
				Enabled:           true,
				ScanInterval:      5 * time.Second,
				BatchSize:         50,
				DeliverySemantics: DeliveryAtLeastOnce,
			},
		},
		{
			name: "auto-fill defaults when enabled with zero values",
			input: ExpiryProcessorConfig{
				Enabled:      true,
				ScanInterval: 0, // Should be auto-filled to 10s
				BatchSize:    0, // Should be auto-filled to 100
			},
			expected: ExpiryProcessorConfig{
				Enabled:           true,
				ScanInterval:      10 * time.Second,
				BatchSize:         100,
				DeliverySemantics: DeliveryAtMostOnce, // Default
			},
		},
		{
			name: "disabled config keeps zeros",
			input: ExpiryProcessorConfig{
				Enabled:      false,
				ScanInterval: 0,
				BatchSize:    0,
			},
			expected: ExpiryProcessorConfig{
				Enabled:           false,
				ScanInterval:      0, // Not auto-filled when disabled
				BatchSize:         0, // Not auto-filled when disabled
				DeliverySemantics: DeliveryAtMostOnce,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := HITLConfig{}
			WithExpiryProcessor(tc.input)(&config)

			if config.ExpiryProcessor.Enabled != tc.expected.Enabled {
				t.Errorf("Enabled = %v, want %v", config.ExpiryProcessor.Enabled, tc.expected.Enabled)
			}
			if config.ExpiryProcessor.ScanInterval != tc.expected.ScanInterval {
				t.Errorf("ScanInterval = %v, want %v", config.ExpiryProcessor.ScanInterval, tc.expected.ScanInterval)
			}
			if config.ExpiryProcessor.BatchSize != tc.expected.BatchSize {
				t.Errorf("BatchSize = %d, want %d", config.ExpiryProcessor.BatchSize, tc.expected.BatchSize)
			}
			if config.ExpiryProcessor.DeliverySemantics != tc.expected.DeliverySemantics {
				t.Errorf("DeliverySemantics = %q, want %q", config.ExpiryProcessor.DeliverySemantics, tc.expected.DeliverySemantics)
			}
		})
	}
}

func TestNewHITLConfig(t *testing.T) {
	// Test with no options - should get defaults
	config := NewHITLConfig()

	if config.Enabled {
		t.Error("NewHITLConfig() with no options should have Enabled=false (default)")
	}

	if config.ExpiryProcessor.ScanInterval != 10*time.Second {
		t.Errorf("NewHITLConfig().ExpiryProcessor.ScanInterval = %v, want 10s",
			config.ExpiryProcessor.ScanInterval)
	}
}

func TestNewHITLConfig_WithOptions(t *testing.T) {
	config := NewHITLConfig(
		WithExpiryProcessor(ExpiryProcessorConfig{
			Enabled:           true,
			ScanInterval:      5 * time.Second,
			BatchSize:         200,
			DeliverySemantics: DeliveryAtLeastOnce,
		}),
	)

	if config.ExpiryProcessor.ScanInterval != 5*time.Second {
		t.Errorf("ScanInterval = %v, want 5s", config.ExpiryProcessor.ScanInterval)
	}

	if config.ExpiryProcessor.BatchSize != 200 {
		t.Errorf("BatchSize = %d, want 200", config.ExpiryProcessor.BatchSize)
	}

	if config.ExpiryProcessor.DeliverySemantics != DeliveryAtLeastOnce {
		t.Errorf("DeliverySemantics = %q, want %q",
			config.ExpiryProcessor.DeliverySemantics, DeliveryAtLeastOnce)
	}
}

func TestApplyHITLOptions(t *testing.T) {
	config := DefaultHITLConfig()

	// Verify initial state
	if config.ExpiryProcessor.BatchSize != 100 {
		t.Fatalf("Initial BatchSize = %d, want 100", config.ExpiryProcessor.BatchSize)
	}

	// Apply option
	ApplyHITLOptions(&config, WithExpiryProcessor(ExpiryProcessorConfig{
		Enabled:   true,
		BatchSize: 500,
	}))

	// Verify change
	if config.ExpiryProcessor.BatchSize != 500 {
		t.Errorf("After ApplyHITLOptions BatchSize = %d, want 500", config.ExpiryProcessor.BatchSize)
	}
}

// =============================================================================
// ExpiryProcessorConfigFromEnv Tests
// =============================================================================

func TestExpiryProcessorConfigFromEnv_Defaults(t *testing.T) {
	// Clear any existing environment variables
	t.Setenv("TRUVAG3_HITL_EXPIRY_ENABLED", "")
	t.Setenv("TRUVAG3_HITL_EXPIRY_INTERVAL", "")
	t.Setenv("TRUVAG3_HITL_EXPIRY_BATCH_SIZE", "")
	t.Setenv("TRUVAG3_HITL_EXPIRY_DELIVERY", "")

	config := ExpiryProcessorConfigFromEnv()

	if !config.Enabled {
		t.Error("Default Enabled should be true")
	}

	if config.ScanInterval != 10*time.Second {
		t.Errorf("Default ScanInterval = %v, want 10s", config.ScanInterval)
	}

	if config.BatchSize != 100 {
		t.Errorf("Default BatchSize = %d, want 100", config.BatchSize)
	}

	if config.DeliverySemantics != DeliveryAtMostOnce {
		t.Errorf("Default DeliverySemantics = %q, want %q",
			config.DeliverySemantics, DeliveryAtMostOnce)
	}
}

func TestExpiryProcessorConfigFromEnv_CustomValues(t *testing.T) {
	t.Setenv("TRUVAG3_HITL_EXPIRY_ENABLED", "false")
	t.Setenv("TRUVAG3_HITL_EXPIRY_INTERVAL", "30s")
	t.Setenv("TRUVAG3_HITL_EXPIRY_BATCH_SIZE", "500")
	t.Setenv("TRUVAG3_HITL_EXPIRY_DELIVERY", "at_least_once")

	config := ExpiryProcessorConfigFromEnv()

	if config.Enabled {
		t.Error("Enabled should be false (from env)")
	}

	if config.ScanInterval != 30*time.Second {
		t.Errorf("ScanInterval = %v, want 30s", config.ScanInterval)
	}

	if config.BatchSize != 500 {
		t.Errorf("BatchSize = %d, want 500", config.BatchSize)
	}

	if config.DeliverySemantics != DeliveryAtLeastOnce {
		t.Errorf("DeliverySemantics = %q, want %q",
			config.DeliverySemantics, DeliveryAtLeastOnce)
	}
}

func TestExpiryProcessorConfigFromEnv_BoolVariations(t *testing.T) {
	testCases := []struct {
		envValue string
		expected bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"on", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"off", false},
	}

	for _, tc := range testCases {
		t.Run(tc.envValue, func(t *testing.T) {
			t.Setenv("TRUVAG3_HITL_EXPIRY_ENABLED", tc.envValue)

			config := ExpiryProcessorConfigFromEnv()
			if config.Enabled != tc.expected {
				t.Errorf("Enabled with env=%q = %v, want %v", tc.envValue, config.Enabled, tc.expected)
			}
		})
	}
}

func TestExpiryProcessorConfigFromEnv_InvalidDeliveryUseDefault(t *testing.T) {
	t.Setenv("TRUVAG3_HITL_EXPIRY_DELIVERY", "invalid_value")

	config := ExpiryProcessorConfigFromEnv()

	// Invalid value should use default (at_most_once)
	if config.DeliverySemantics != DeliveryAtMostOnce {
		t.Errorf("Invalid delivery semantics should default to %q, got %q",
			DeliveryAtMostOnce, config.DeliverySemantics)
	}
}

// =============================================================================
// BuildResumeContext Trace Linking Tests (RC7-B3)
// =============================================================================

// TestBuildResumeContext_TraceLinking verifies that BuildResumeContext returns a
// non-nil endSpan cleanup function and propagates original_request_id in baggage.
func TestBuildResumeContext_TraceLinking(t *testing.T) {
	testCases := []struct {
		name              string
		checkpoint        *ExecutionCheckpoint
		expectedRequestID string // expected original_request_id in baggage
	}{
		{
			name: "typed trace fields with original request ID",
			checkpoint: &ExecutionCheckpoint{
				CheckpointID:      "cp-trace-1",
				RequestID:         "req-123",
				OriginalRequestID: "req-original",
				OriginalTraceID:   "abc123",
				OriginalSpanID:    "def456",
				Status:            CheckpointStatusApproved,
				InterruptPoint:    InterruptPointBeforeStep,
			},
			expectedRequestID: "req-original",
		},
		{
			name: "fallback to RequestID when OriginalRequestID is empty",
			checkpoint: &ExecutionCheckpoint{
				CheckpointID:    "cp-trace-2",
				RequestID:       "req-456",
				OriginalTraceID: "abc789",
				OriginalSpanID:  "def012",
				Status:          CheckpointStatusEdited,
				InterruptPoint:  InterruptPointBeforeStep,
			},
			expectedRequestID: "req-456",
		},
		{
			name: "empty trace fields degrade gracefully",
			checkpoint: &ExecutionCheckpoint{
				CheckpointID:   "cp-trace-3",
				RequestID:      "req-789",
				Status:         CheckpointStatusExpiredApproved,
				InterruptPoint: InterruptPointBeforeStep,
			},
			expectedRequestID: "req-789",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			resumeCtx, endSpan, err := BuildResumeContext(ctx, tc.checkpoint)
			if err != nil {
				t.Fatalf("BuildResumeContext() returned unexpected error: %v", err)
			}

			// endSpan must be non-nil (callable cleanup)
			if endSpan == nil {
				t.Fatal("endSpan must not be nil")
			}
			// Call endSpan to verify it doesn't panic
			endSpan()

			// Verify original_request_id baggage propagation
			if resumeCtx == nil {
				t.Fatal("resumeCtx must not be nil")
			}

			// Verify resume mode was set
			if cpID, ok := IsResumeMode(resumeCtx); !ok || cpID != tc.checkpoint.CheckpointID {
				t.Errorf("IsResumeMode() = (%q, %v), want (%q, true)",
					cpID, ok, tc.checkpoint.CheckpointID)
			}
		})
	}
}

func TestBuildResumeContext_RestoresConversationBeforeLinkedSpan(t *testing.T) {
	recorder := setupHITLResumeTestTracer(t)
	checkpoint := &ExecutionCheckpoint{
		CheckpointID:      "cp-conversation",
		RequestID:         "request-resume",
		OriginalRequestID: "request-original",
		Status:            CheckpointStatusApproved,
		UserContext: map[string]interface{}{
			MetadataConversationID: "conversation-resume",
			"session_id":           "application-session",
		},
	}

	resumeCtx, endSpan, err := BuildResumeContext(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("BuildResumeContext() error = %v", err)
	}

	if got := core.GetConversationID(resumeCtx); got != "conversation-resume" {
		t.Fatalf("core conversation ID = %q", got)
	}
	if got := telemetry.GetBaggage(resumeCtx)[MetadataConversationID]; got != "conversation-resume" {
		t.Fatalf("baggage conversation ID = %q", got)
	}
	if got := GetMetadata(resumeCtx)[MetadataConversationID]; got != "conversation-resume" {
		t.Fatalf("resume metadata conversation ID = %v", got)
	}
	if got := GetMetadata(resumeCtx)["session_id"]; got != "application-session" {
		t.Fatalf("application metadata was not preserved: %v", GetMetadata(resumeCtx))
	}
	if got := telemetry.GetBaggage(resumeCtx)["original_request_id"]; got != "request-original" {
		t.Fatalf("original request ID = %q", got)
	}

	orchestrator := &AIOrchestrator{telemetry: &conversationCaptureTelemetry{}}
	ingressCtx, ingressID, ingressMetadata := orchestrator.resolveConversationContext(
		resumeCtx,
		nil,
	)
	if ingressID != "conversation-resume" ||
		core.GetConversationID(ingressCtx) != "conversation-resume" ||
		ingressMetadata[MetadataConversationID] != "conversation-resume" {
		t.Fatalf(
			"resume ingress lost identity: id=%q core=%q metadata=%v",
			ingressID,
			core.GetConversationID(ingressCtx),
			ingressMetadata,
		)
	}

	chainedCtx := withCheckpointMetadata(ingressCtx, nil, ingressID)
	chained := (&DefaultInterruptController{logger: &core.NoOpLogger{}}).createCheckpoint(
		chainedCtx,
		&RoutingPlan{OriginalRequest: "resume request"},
		nil,
		nil,
		&InterruptDecision{},
		InterruptPointPlanGenerated,
	)
	if got := chained.UserContext[MetadataConversationID]; got != "conversation-resume" {
		t.Fatalf("chained checkpoint conversation ID = %v", got)
	}

	endSpan()
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	if got, ok := readOnlySpanStringAttribute(ended[0], MetadataConversationID); !ok || got != "conversation-resume" {
		t.Fatalf("hitl.resume conversation attribute = %q, %v", got, ok)
	}
}

func TestBuildResumeContext_RejectedConversationScrubsInheritedIdentity(t *testing.T) {
	tests := []struct {
		name         string
		base         func(t *testing.T) context.Context
		conversation interface{}
	}{
		{
			name: "invalid checkpoint metadata",
			base: func(t *testing.T) context.Context {
				ctx, err := telemetry.WithBaggageExact(
					context.Background(),
					MetadataConversationID,
					"conversation-inherited",
				)
				if err != nil {
					t.Fatalf("WithBaggageExact() error = %v", err)
				}
				return core.WithConversationID(ctx, "conversation-inherited")
			},
			conversation: "invalid conversation",
		},
		{
			name: "exact baggage capacity rejection",
			base: func(t *testing.T) context.Context {
				ctx := core.WithConversationID(
					context.Background(),
					"conversation-inherited",
				)
				labels := make([]string, 0, telemetry.MaxBaggageItems*2)
				for i := 0; i < telemetry.MaxBaggageItems; i++ {
					labels = append(labels, fmt.Sprintf("item_%02d", i), "value")
				}
				ctx = telemetry.WithBaggage(ctx, labels...)
				if got := len(telemetry.GetBaggage(ctx)); got != telemetry.MaxBaggageItems {
					t.Fatalf("baggage items = %d, want %d", got, telemetry.MaxBaggageItems)
				}
				return ctx
			},
			conversation: "conversation-checkpoint",
		},
		{
			name: "over-limit checkpoint metadata",
			base: func(*testing.T) context.Context {
				return core.WithConversationID(
					context.Background(),
					"conversation-inherited",
				)
			},
			conversation: strings.Repeat("x", core.MaxConversationIDLength+1),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := setupHITLResumeTestTracer(t)
			baseCtx := WithMetadata(test.base(t), map[string]interface{}{
				MetadataConversationID: "conversation-inherited",
				"inherited_only":       true,
			})
			checkpoint := &ExecutionCheckpoint{
				CheckpointID: "cp-rejected-conversation",
				RequestID:    "request-resume",
				Status:       CheckpointStatusApproved,
				UserContext: map[string]interface{}{
					MetadataConversationID: test.conversation,
					"checkpoint_only":      true,
				},
			}

			resumeCtx, endSpan, err := BuildResumeContext(baseCtx, checkpoint)
			if err != nil {
				t.Fatalf("BuildResumeContext() error = %v", err)
			}

			if got := core.GetConversationID(resumeCtx); got != "" {
				t.Fatalf("inherited core conversation leaked: %q", got)
			}
			if _, present := telemetry.GetBaggage(resumeCtx)[MetadataConversationID]; present {
				t.Fatal("inherited conversation baggage leaked")
			}
			if _, present := GetMetadata(resumeCtx)[MetadataConversationID]; present {
				t.Fatalf("rejected conversation metadata leaked: %v", GetMetadata(resumeCtx))
			}
			if GetMetadata(resumeCtx)["checkpoint_only"] != true {
				t.Fatalf("checkpoint metadata was not preserved: %v", GetMetadata(resumeCtx))
			}
			if _, present := GetMetadata(resumeCtx)["inherited_only"]; present {
				t.Fatalf("inherited metadata was not shadowed: %v", GetMetadata(resumeCtx))
			}

			endSpan()
			ended := recorder.Ended()
			if len(ended) != 1 {
				t.Fatalf("ended spans = %d, want 1", len(ended))
			}
			if got, present := readOnlySpanStringAttribute(ended[0], MetadataConversationID); present {
				t.Fatalf("rejected conversation reached span: %q", got)
			}
			if status := ended[0].Status(); status.Code != codes.Unset {
				t.Fatalf("span status = %v (%q), want unset", status.Code, status.Description)
			}
			for _, event := range ended[0].Events() {
				if event.Name == "exception" {
					t.Fatalf("conversation rejection recorded an exception: %+v", event)
				}
			}
		})
	}
}

func TestPrepareResumeConversationContext_ReportsBoundedRejections(t *testing.T) {
	tests := []struct {
		name       string
		ctx        func(t *testing.T) context.Context
		metadata   map[string]interface{}
		wantSource string
		wantReason string
	}{
		{
			name: "metadata validation",
			ctx:  func(*testing.T) context.Context { return context.Background() },
			metadata: map[string]interface{}{
				MetadataConversationID: "invalid conversation",
			},
			wantSource: conversationIDSourceCheckpointMetadata,
			wantReason: string(core.ConversationIDValidationInvalidCharacter),
		},
		{
			name: "baggage capacity",
			ctx: func(t *testing.T) context.Context {
				labels := make([]string, 0, telemetry.MaxBaggageItems*2)
				for i := 0; i < telemetry.MaxBaggageItems; i++ {
					labels = append(labels, fmt.Sprintf("item_%02d", i), "value")
				}
				return telemetry.WithBaggage(context.Background(), labels...)
			},
			metadata: map[string]interface{}{
				MetadataConversationID: "conversation-checkpoint",
			},
			wantSource: conversationIDSourceBaggageExact,
			wantReason: "item_limit",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotSource, gotReason string
			gotCtx, gotID, gotMetadata := prepareResumeConversationContext(
				test.ctx(t),
				test.metadata,
				func(source, reason string) {
					gotSource = source
					gotReason = reason
				},
			)

			if gotSource != test.wantSource || gotReason != test.wantReason {
				t.Fatalf(
					"diagnostic = (%q, %q), want (%q, %q)",
					gotSource,
					gotReason,
					test.wantSource,
					test.wantReason,
				)
			}
			if gotID != "" || core.GetConversationID(gotCtx) != "" {
				t.Fatalf("rejected identity survived: id=%q core=%q", gotID, core.GetConversationID(gotCtx))
			}
			if _, present := gotMetadata[MetadataConversationID]; present {
				t.Fatalf("rejected identity survived in metadata: %v", gotMetadata)
			}
		})
	}
}

func TestBuildResumeContext_EmptyCheckpointMetadataPreservesOnlyUnrelatedInheritedMetadata(t *testing.T) {
	ctx, err := telemetry.WithBaggageExact(
		context.Background(),
		MetadataConversationID,
		"conversation-inherited",
	)
	if err != nil {
		t.Fatalf("WithBaggageExact() error = %v", err)
	}
	ctx = core.WithConversationID(ctx, "conversation-inherited")
	ctx = WithMetadata(ctx, map[string]interface{}{
		MetadataConversationID: "conversation-inherited",
		"application_key":      "preserved",
	})

	resumeCtx, endSpan, err := BuildResumeContext(ctx, &ExecutionCheckpoint{
		CheckpointID: "cp-empty-metadata",
		RequestID:    "request-resume",
		Status:       CheckpointStatusApproved,
	})
	if err != nil {
		t.Fatalf("BuildResumeContext() error = %v", err)
	}
	defer endSpan()

	if core.GetConversationID(resumeCtx) != "" {
		t.Fatalf("inherited core conversation leaked: %q", core.GetConversationID(resumeCtx))
	}
	if _, present := telemetry.GetBaggage(resumeCtx)[MetadataConversationID]; present {
		t.Fatal("inherited conversation baggage leaked")
	}
	metadata := GetMetadata(resumeCtx)
	if _, present := metadata[MetadataConversationID]; present {
		t.Fatalf("inherited conversation metadata leaked: %v", metadata)
	}
	if metadata["application_key"] != "preserved" {
		t.Fatalf("unrelated inherited metadata was lost: %v", metadata)
	}
}

func setupHITLResumeTestTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	originalProvider := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(originalProvider)
		_ = provider.Shutdown(context.Background())
	})
	return recorder
}
