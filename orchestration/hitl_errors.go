package orchestration

import (
	"errors"
	"fmt"
)

// =============================================================================
// HITL Error Types
// =============================================================================
//
// This file defines error types for the HITL system. The key concept is that
// an "interrupt" is NOT a failure - it's a signal that the workflow is paused
// awaiting human input.
//
// Usage:
//
//	response, err := orchestrator.ProcessRequest(ctx, request, nil)
//	if IsInterrupted(err) {
//	    checkpoint := GetCheckpoint(err)
//	    // Handle interrupt - notify user, store checkpoint ID, etc.
//	    return checkpoint.CheckpointID
//	}
//	if err != nil {
//	    // Handle actual error
//	    return err
//	}
//
// =============================================================================

// ErrInterrupted is a special error that signals workflow interruption.
// It's not a failure - it indicates the workflow is paused awaiting human input.
type ErrInterrupted struct {
	CheckpointID string
	Checkpoint   *ExecutionCheckpoint
}

// Error implements the error interface
func (e *ErrInterrupted) Error() string {
	if e.Checkpoint != nil && e.Checkpoint.Decision != nil {
		return fmt.Sprintf("workflow interrupted: checkpoint_id=%s, reason=%s, message=%s",
			e.CheckpointID, e.Checkpoint.Decision.Reason, e.Checkpoint.Decision.Message)
	}
	return fmt.Sprintf("workflow interrupted: checkpoint_id=%s", e.CheckpointID)
}

// Unwrap returns nil as this is a terminal error type
func (e *ErrInterrupted) Unwrap() error {
	return nil
}

// IsInterrupted checks if an error is an interrupt (not a failure).
// Returns true if the workflow is paused awaiting human input.
//
// Usage:
//
//	if IsInterrupted(err) {
//	    checkpoint := GetCheckpoint(err)
//	    // Workflow is paused, not failed
//	}
func IsInterrupted(err error) bool {
	if err == nil {
		return false
	}
	var interrupted *ErrInterrupted
	return errors.As(err, &interrupted)
}

// GetCheckpoint extracts the checkpoint from an interrupt error.
// Returns nil if the error is not an interrupt.
//
// Usage:
//
//	if IsInterrupted(err) {
//	    checkpoint := GetCheckpoint(err)
//	    fmt.Printf("Paused at: %s\n", checkpoint.InterruptPoint)
//	}
func GetCheckpoint(err error) *ExecutionCheckpoint {
	if err == nil {
		return nil
	}
	var interrupted *ErrInterrupted
	if errors.As(err, &interrupted) {
		return interrupted.Checkpoint
	}
	return nil
}

// GetCheckpointID extracts just the checkpoint ID from an interrupt error.
// Returns empty string if the error is not an interrupt.
func GetCheckpointID(err error) string {
	if err == nil {
		return ""
	}
	var interrupted *ErrInterrupted
	if errors.As(err, &interrupted) {
		return interrupted.CheckpointID
	}
	return ""
}

// NewInterruptError creates a new interrupt error with the given checkpoint.
// This is typically called by the InterruptController when an interrupt is triggered.
func NewInterruptError(checkpoint *ExecutionCheckpoint) *ErrInterrupted {
	return &ErrInterrupted{
		CheckpointID: checkpoint.CheckpointID,
		Checkpoint:   checkpoint,
	}
}

// =============================================================================
// Additional HITL-specific errors
// =============================================================================

// ErrCheckpointNotFound indicates a checkpoint could not be found
type ErrCheckpointNotFound struct {
	CheckpointID string
}

func (e *ErrCheckpointNotFound) Error() string {
	return fmt.Sprintf("checkpoint not found: %s (may have expired or been deleted)", e.CheckpointID)
}

// IsCheckpointNotFound checks if an error is a checkpoint not found error
func IsCheckpointNotFound(err error) bool {
	var notFound *ErrCheckpointNotFound
	return errors.As(err, &notFound)
}

// ErrCheckpointExpired indicates a checkpoint has expired
type ErrCheckpointExpired struct {
	CheckpointID string
}

func (e *ErrCheckpointExpired) Error() string {
	return fmt.Sprintf("checkpoint expired: %s (timeout reached without human response)", e.CheckpointID)
}

// IsCheckpointExpired checks if an error is a checkpoint expired error
func IsCheckpointExpired(err error) bool {
	var expired *ErrCheckpointExpired
	return errors.As(err, &expired)
}

// ErrInvalidCommand indicates an invalid command was submitted
type ErrInvalidCommand struct {
	CommandType CommandType
	Reason      string
}

func (e *ErrInvalidCommand) Error() string {
	return fmt.Sprintf("invalid command %s: %s", e.CommandType, e.Reason)
}

// IsInvalidCommand checks if an error is an invalid command error
func IsInvalidCommand(err error) bool {
	var invalid *ErrInvalidCommand
	return errors.As(err, &invalid)
}

// ErrHITLDisabled indicates HITL features are not enabled
type ErrHITLDisabled struct{}

func (e *ErrHITLDisabled) Error() string {
	return "HITL is not enabled (set HITLConfig.Enabled=true or TRUVAG3_HITL_ENABLED=true)"
}

// IsHITLDisabled checks if an error indicates HITL is disabled
func IsHITLDisabled(err error) bool {
	var disabled *ErrHITLDisabled
	return errors.As(err, &disabled)
}
