package orchestration

import (
	"context"
	"time"
)

// HITLResumer continues approved work and persists its lifecycle outcome.
type HITLResumer interface {
	ResumeExecution(context.Context, string) (*ExecutionResult, error)
}

// ResumeExecutor runs application processing with the restored resume context.
// It returns the real terminal result and error, including partial results.
type ResumeExecutor interface {
	ExecuteResume(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error)
}

// ResumeExecutorFunc adapts an application function to ResumeExecutor.
type ResumeExecutorFunc func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error)

func (f ResumeExecutorFunc) ExecuteResume(ctx context.Context, checkpoint *ExecutionCheckpoint) (*ExecutionResult, error) {
	return f(ctx, checkpoint)
}

// CheckpointResumeState is durable attempt ownership, not public API data.
// Its lease governs ownership only; it does not extend checkpoint retention.
type CheckpointResumeState struct {
	AttemptID                     string                    `json:"attempt_id"`
	Owner                         string                    `json:"owner,omitempty"`
	PreviousStatus                CheckpointStatus          `json:"previous_status"`
	LeaseExpiresAt                time.Time                 `json:"lease_expires_at"`
	Outcome                       ResumeFinalizationOutcome `json:"outcome,omitempty"`
	ReservedSuccessorCheckpointID string                    `json:"reserved_successor_checkpoint_id,omitempty"`
	ReservedSuccessorAttemptID    string                    `json:"reserved_successor_attempt_id,omitempty"`
	SuccessorCheckpointID         string                    `json:"successor_checkpoint_id,omitempty"`
}

// ResumeClaimRequest identifies one attempt and its reserved successor.
type ResumeClaimRequest struct {
	CheckpointID          string
	AttemptID             string
	Owner                 string
	Lease                 time.Duration
	SuccessorCheckpointID string
}

// CheckpointResumeClaim contains an isolated approved snapshot for execution.
// Durable state is resuming; Checkpoint.Status is the preceding approval status.
type CheckpointResumeClaim struct {
	Checkpoint     *ExecutionCheckpoint
	AttemptID      string
	PreviousStatus CheckpointStatus
	LeaseExpiresAt time.Time
}

// ResumeFinalizationOutcome distinguishes completion from another interruption.
type ResumeFinalizationOutcome string

const (
	ResumeFinalizationCompleted ResumeFinalizationOutcome = "completed"
	ResumeFinalizationContinued ResumeFinalizationOutcome = "continued"
)

// ResumeFinalization commits the outcome only while the attempt still owns it.
type ResumeFinalization struct {
	CheckpointID          string
	AttemptID             string
	Outcome               ResumeFinalizationOutcome
	SuccessorCheckpointID string
}

// CheckpointResumePersistence is the optional atomic resume capability.
// Lease checks use backend time. Every mutation preserves remaining retention;
// missing records and expired leases must never be silently recreated or renewed.
type CheckpointResumePersistence interface {
	LoadCheckpoint(context.Context, string) (*ExecutionCheckpoint, error)
	ClaimCheckpointForResume(context.Context, ResumeClaimRequest) (*CheckpointResumeClaim, error)
	RenewCheckpointResumeClaim(ctx context.Context, checkpointID, attemptID string, lease time.Duration) error
	ReleaseCheckpointResumeClaim(ctx context.Context, checkpointID, attemptID string) error
	FinalizeCheckpointResume(context.Context, ResumeFinalization) error
}

// This lineage is installed only from a claimed snapshot, not application
// metadata or WithResumeMode. Child writes are also checked by persistence.
type resumeLineage struct {
	CheckpointID          string
	AttemptID             string
	SuccessorCheckpointID string
}
type resumeLineageKey struct{}
