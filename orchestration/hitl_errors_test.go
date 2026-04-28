package orchestration

import (
	"errors"
	"fmt"
	"testing"
)

// =============================================================================
// ErrInterrupted Tests
// =============================================================================

func TestErrInterrupted_Error_WithDecision(t *testing.T) {
	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-test-123",
		Decision: &InterruptDecision{
			Reason:  ReasonSensitiveOperation,
			Message: "Requires approval for payment",
		},
	}

	err := &ErrInterrupted{
		CheckpointID: "cp-test-123",
		Checkpoint:   checkpoint,
	}

	msg := err.Error()

	if msg == "" {
		t.Fatal("Error message should not be empty")
	}
	if !containsSubstring(msg, "workflow interrupted") {
		t.Errorf("Error should contain 'workflow interrupted', got: %s", msg)
	}
	if !containsSubstring(msg, "cp-test-123") {
		t.Errorf("Error should contain checkpoint ID, got: %s", msg)
	}
	if !containsSubstring(msg, string(ReasonSensitiveOperation)) {
		t.Errorf("Error should contain reason, got: %s", msg)
	}
	if !containsSubstring(msg, "Requires approval for payment") {
		t.Errorf("Error should contain message, got: %s", msg)
	}
}

func TestErrInterrupted_Error_WithoutDecision(t *testing.T) {
	err := &ErrInterrupted{
		CheckpointID: "cp-simple-456",
		Checkpoint:   &ExecutionCheckpoint{CheckpointID: "cp-simple-456"},
	}

	msg := err.Error()

	if msg == "" {
		t.Fatal("Error message should not be empty")
	}
	if !containsSubstring(msg, "workflow interrupted") {
		t.Errorf("Error should contain 'workflow interrupted', got: %s", msg)
	}
	if !containsSubstring(msg, "cp-simple-456") {
		t.Errorf("Error should contain checkpoint ID, got: %s", msg)
	}
}

func TestErrInterrupted_Error_NilCheckpoint(t *testing.T) {
	err := &ErrInterrupted{
		CheckpointID: "cp-nil-789",
		Checkpoint:   nil,
	}

	msg := err.Error()

	if msg == "" {
		t.Fatal("Error message should not be empty")
	}
	if !containsSubstring(msg, "workflow interrupted") {
		t.Errorf("Error should contain 'workflow interrupted', got: %s", msg)
	}
	if !containsSubstring(msg, "cp-nil-789") {
		t.Errorf("Error should contain checkpoint ID, got: %s", msg)
	}
}

func TestErrInterrupted_Unwrap(t *testing.T) {
	err := &ErrInterrupted{
		CheckpointID: "cp-unwrap",
	}

	unwrapped := err.Unwrap()

	if unwrapped != nil {
		t.Errorf("Unwrap should return nil, got: %v", unwrapped)
	}
}

// =============================================================================
// IsInterrupted Tests
// =============================================================================

func TestIsInterrupted_True(t *testing.T) {
	err := &ErrInterrupted{CheckpointID: "cp-123"}

	if !IsInterrupted(err) {
		t.Error("IsInterrupted should return true for ErrInterrupted")
	}
}

func TestIsInterrupted_WrappedTrue(t *testing.T) {
	innerErr := &ErrInterrupted{CheckpointID: "cp-inner"}
	wrappedErr := fmt.Errorf("outer error: %w", innerErr)

	if !IsInterrupted(wrappedErr) {
		t.Error("IsInterrupted should return true for wrapped ErrInterrupted")
	}
}

func TestIsInterrupted_False_OtherError(t *testing.T) {
	err := errors.New("some other error")

	if IsInterrupted(err) {
		t.Error("IsInterrupted should return false for non-interrupt errors")
	}
}

func TestIsInterrupted_False_Nil(t *testing.T) {
	if IsInterrupted(nil) {
		t.Error("IsInterrupted should return false for nil")
	}
}

// =============================================================================
// GetCheckpoint Tests
// =============================================================================

func TestGetCheckpoint_Success(t *testing.T) {
	checkpoint := &ExecutionCheckpoint{
		CheckpointID:   "cp-get-test",
		InterruptPoint: InterruptPointPlanGenerated,
	}
	err := &ErrInterrupted{
		CheckpointID: "cp-get-test",
		Checkpoint:   checkpoint,
	}

	result := GetCheckpoint(err)

	if result == nil {
		t.Fatal("GetCheckpoint should return checkpoint")
	}
	if result.CheckpointID != "cp-get-test" {
		t.Errorf("Checkpoint ID mismatch, got: %s", result.CheckpointID)
	}
	if result.InterruptPoint != InterruptPointPlanGenerated {
		t.Errorf("InterruptPoint mismatch, got: %s", result.InterruptPoint)
	}
}

func TestGetCheckpoint_FromWrappedError(t *testing.T) {
	checkpoint := &ExecutionCheckpoint{CheckpointID: "cp-wrapped"}
	innerErr := &ErrInterrupted{
		CheckpointID: "cp-wrapped",
		Checkpoint:   checkpoint,
	}
	wrappedErr := fmt.Errorf("wrapped: %w", innerErr)

	result := GetCheckpoint(wrappedErr)

	if result == nil {
		t.Fatal("GetCheckpoint should return checkpoint from wrapped error")
	}
	if result.CheckpointID != "cp-wrapped" {
		t.Errorf("Checkpoint ID mismatch, got: %s", result.CheckpointID)
	}
}

func TestGetCheckpoint_Nil_NonInterruptError(t *testing.T) {
	err := errors.New("not an interrupt")

	result := GetCheckpoint(err)

	if result != nil {
		t.Errorf("GetCheckpoint should return nil for non-interrupt errors, got: %v", result)
	}
}

func TestGetCheckpoint_Nil_NilError(t *testing.T) {
	result := GetCheckpoint(nil)

	if result != nil {
		t.Errorf("GetCheckpoint should return nil for nil error, got: %v", result)
	}
}

// =============================================================================
// GetCheckpointID Tests
// =============================================================================

func TestGetCheckpointID_Success(t *testing.T) {
	err := &ErrInterrupted{CheckpointID: "cp-id-test-abc"}

	result := GetCheckpointID(err)

	if result != "cp-id-test-abc" {
		t.Errorf("GetCheckpointID mismatch, got: %s", result)
	}
}

func TestGetCheckpointID_FromWrappedError(t *testing.T) {
	innerErr := &ErrInterrupted{CheckpointID: "cp-wrapped-id"}
	wrappedErr := fmt.Errorf("wrapped: %w", innerErr)

	result := GetCheckpointID(wrappedErr)

	if result != "cp-wrapped-id" {
		t.Errorf("GetCheckpointID mismatch, got: %s", result)
	}
}

func TestGetCheckpointID_Empty_NonInterruptError(t *testing.T) {
	err := errors.New("not an interrupt")

	result := GetCheckpointID(err)

	if result != "" {
		t.Errorf("GetCheckpointID should return empty for non-interrupt errors, got: %s", result)
	}
}

func TestGetCheckpointID_Empty_NilError(t *testing.T) {
	result := GetCheckpointID(nil)

	if result != "" {
		t.Errorf("GetCheckpointID should return empty for nil error, got: %s", result)
	}
}

// =============================================================================
// NewInterruptError Tests
// =============================================================================

func TestNewInterruptError(t *testing.T) {
	checkpoint := &ExecutionCheckpoint{
		CheckpointID:   "cp-new-error",
		InterruptPoint: InterruptPointBeforeStep,
		Decision: &InterruptDecision{
			Reason:  ReasonPlanApproval,
			Message: "Step requires approval",
		},
	}

	err := NewInterruptError(checkpoint)

	if err == nil {
		t.Fatal("NewInterruptError should return error")
	}
	if err.CheckpointID != "cp-new-error" {
		t.Errorf("CheckpointID mismatch, got: %s", err.CheckpointID)
	}
	if err.Checkpoint != checkpoint {
		t.Error("Checkpoint reference mismatch")
	}

	// Verify it's a proper error
	if !IsInterrupted(err) {
		t.Error("NewInterruptError result should pass IsInterrupted check")
	}
}

// =============================================================================
// ErrHITLDisabled Tests
// =============================================================================

func TestErrHITLDisabled_Error(t *testing.T) {
	err := &ErrHITLDisabled{}

	msg := err.Error()

	if msg == "" {
		t.Fatal("Error message should not be empty")
	}
	if !containsSubstring(msg, "HITL is not enabled") {
		t.Errorf("Error should mention HITL not enabled, got: %s", msg)
	}
	if !containsSubstring(msg, "HITLConfig.Enabled=true") {
		t.Errorf("Error should mention config option, got: %s", msg)
	}
}

func TestIsHITLDisabled_True(t *testing.T) {
	err := &ErrHITLDisabled{}

	if !IsHITLDisabled(err) {
		t.Error("IsHITLDisabled should return true for ErrHITLDisabled")
	}
}

func TestIsHITLDisabled_WrappedTrue(t *testing.T) {
	innerErr := &ErrHITLDisabled{}
	wrappedErr := fmt.Errorf("outer: %w", innerErr)

	if !IsHITLDisabled(wrappedErr) {
		t.Error("IsHITLDisabled should return true for wrapped ErrHITLDisabled")
	}
}

func TestIsHITLDisabled_False(t *testing.T) {
	err := errors.New("some other error")

	if IsHITLDisabled(err) {
		t.Error("IsHITLDisabled should return false for non-disabled errors")
	}
}

// =============================================================================
// Error Type Distinction Tests
// =============================================================================

func TestErrorTypes_AreDistinct(t *testing.T) {
	interrupted := &ErrInterrupted{CheckpointID: "cp-1"}
	notFound := &ErrCheckpointNotFound{CheckpointID: "cp-2"}
	expired := &ErrCheckpointExpired{CheckpointID: "cp-3"}
	invalid := &ErrInvalidCommand{CommandType: CommandApprove, Reason: "test"}
	disabled := &ErrHITLDisabled{}

	// Each error type should only match its own type checker
	testCases := []struct {
		name          string
		err           error
		isInterrupted bool
		isNotFound    bool
		isExpired     bool
		isInvalid     bool
		isDisabled    bool
	}{
		{"ErrInterrupted", interrupted, true, false, false, false, false},
		{"ErrCheckpointNotFound", notFound, false, true, false, false, false},
		{"ErrCheckpointExpired", expired, false, false, true, false, false},
		{"ErrInvalidCommand", invalid, false, false, false, true, false},
		{"ErrHITLDisabled", disabled, false, false, false, false, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if IsInterrupted(tc.err) != tc.isInterrupted {
				t.Errorf("IsInterrupted expected %v", tc.isInterrupted)
			}
			if IsCheckpointNotFound(tc.err) != tc.isNotFound {
				t.Errorf("IsCheckpointNotFound expected %v", tc.isNotFound)
			}
			if IsCheckpointExpired(tc.err) != tc.isExpired {
				t.Errorf("IsCheckpointExpired expected %v", tc.isExpired)
			}
			if IsInvalidCommand(tc.err) != tc.isInvalid {
				t.Errorf("IsInvalidCommand expected %v", tc.isInvalid)
			}
			if IsHITLDisabled(tc.err) != tc.isDisabled {
				t.Errorf("IsHITLDisabled expected %v", tc.isDisabled)
			}
		})
	}
}
