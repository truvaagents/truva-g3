package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// Generated once per process. It is internal correlation, never API data or baggage.
var resumeProcessOwner = "resume-" + uuid.NewString()

// ResumeCoordinator owns one fenced resume lifecycle per invocation. Dependencies
// are borrowed; renewal is per-call and joined. No resources survive a call, so
// this component has no Close or Start method. Hosts drain calls before closing
// their persistence adapter and executor dependencies.
type ResumeCoordinator struct {
	store    CheckpointResumePersistence
	executor ResumeExecutor
	config   ResumeCoordinatorRuntimeConfig
	logger   core.Logger
	owner    string
	counter  func(string, ...string)
}

type ResumeCoordinatorOption func(*ResumeCoordinator) error

func WithResumeLogger(logger core.Logger) ResumeCoordinatorOption {
	return func(r *ResumeCoordinator) error {
		if isNilBackendValue(logger) {
			return errors.New("orchestration: explicit resume logger cannot be nil")
		}
		if scoped, ok := logger.(core.ComponentAwareLogger); ok {
			logger = scoped.WithComponent("framework/orchestration")
		}
		if isNilBackendValue(logger) {
			return errors.New("orchestration: scoped resume logger cannot be nil")
		}
		r.logger = logger
		return nil
	}
}

// WithResumeOwner overrides the generated process identity, for explicit hosts.
// Owner is internal correlation only and must be unique to the process instance.
func WithResumeOwner(owner string) ResumeCoordinatorOption {
	return func(r *ResumeCoordinator) error {
		if err := validateResumeIdentity("resume owner", owner); err != nil {
			return err
		}
		if len(owner) > maxCheckpointClaimOwnerLen {
			return errors.New("orchestration: resume owner exceeds 128 bytes")
		}
		r.owner = owner
		return nil
	}
}

func NewResumeCoordinator(store CheckpointResumePersistence, executor ResumeExecutor, config ResumeCoordinatorRuntimeConfig, opts ...ResumeCoordinatorOption) (*ResumeCoordinator, error) {
	if isNilBackendValue(store) || isNilBackendValue(executor) {
		return nil, errors.New("orchestration: resume persistence and executor are required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	r := &ResumeCoordinator{store: store, executor: executor, config: config, logger: &core.NoOpLogger{}, owner: resumeProcessOwner, counter: telemetry.Counter}
	for _, option := range opts {
		if option == nil {
			return nil, errors.New("orchestration: resume option cannot be nil")
		}
		if err := option(r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *ResumeCoordinator) ResumeExecution(ctx context.Context, checkpointID string) (*ExecutionResult, error) {
	return r.resume(ctx, checkpointID, r.executor)
}

// ResumeWithExecutor uses a request-local adapter, for example an SSE callback.
// It does not modify the coordinator's shared default executor.
func (r *ResumeCoordinator) ResumeWithExecutor(ctx context.Context, checkpointID string, executor ResumeExecutor) (*ExecutionResult, error) {
	return r.resume(ctx, checkpointID, executor)
}

func (r *ResumeCoordinator) resume(ctx context.Context, checkpointID string, executor ResumeExecutor) (result *ExecutionResult, returnErr error) {
	if ctx == nil {
		err := &ErrInvalidResumeRequest{Field: "context"}
		observation := &resumeObservation{coordinator: r, ctx: context.Background(), observer: &resumeRequestIDObserver{}, checkpointID: checkpointID, attemptID: uuid.NewString(), startedAt: time.Now()}
		observation.reject(err, "invalid_input")
		return nil, err
	}
	observation := &resumeObservation{coordinator: r, ctx: ctx, observer: &resumeRequestIDObserver{}, checkpointID: checkpointID, attemptID: uuid.NewString(), startedAt: time.Now()}
	if isNilBackendValue(executor) {
		err := &ErrInvalidResumeRequest{Field: "executor"}
		observation.reject(err, "invalid_input")
		return nil, err
	}
	if err := validateResumeIdentity("checkpoint ID", checkpointID); err != nil {
		err := &ErrInvalidResumeRequest{Field: "checkpoint_id"}
		observation.reject(err, "invalid_input")
		return nil, err
	}
	claim, err := r.store.ClaimCheckpointForResume(ctx, ResumeClaimRequest{
		CheckpointID: checkpointID, AttemptID: observation.attemptID, Owner: r.owner,
		Lease: r.config.ClaimLease, SuccessorCheckpointID: "cp-" + uuid.NewString(),
	})
	if err != nil {
		observation.reject(err, "")
		return nil, err
	}
	r.counter(MetricResumeAttempt, "module", telemetry.ModuleOrchestration, "outcome", "claimed")
	// A custom provider must return the same ownership that it durably acquired.
	if claim == nil || claim.Checkpoint == nil || claim.Checkpoint.CheckpointID != checkpointID ||
		claim.AttemptID != observation.attemptID || claim.Checkpoint.ResumeState == nil ||
		claim.Checkpoint.ResumeState.AttemptID != claim.AttemptID ||
		claim.Checkpoint.ResumeState.Owner != r.owner ||
		claim.PreviousStatus != claim.Checkpoint.Status || !IsResumableStatus(claim.PreviousStatus) ||
		claim.Checkpoint.ResumeState.ReservedSuccessorCheckpointID == "" || claim.Checkpoint.ResumeState.ReservedSuccessorAttemptID == "" {
		err := &ErrCheckpointSuccessorConflict{CheckpointID: checkpointID}
		observation.emit("hitl.resume.claimed", "hitl_resume_claim", "success", "claimed", "", "", "info")
		observation.terminal("failed", "recovery", "successor")
		return nil, err
	}
	resumeCtx, endSpan, restoreErr := BuildResumeContext(ctx, claim.Checkpoint)
	originalID := claim.Checkpoint.OriginalRequestID
	if originalID == "" {
		originalID = claim.Checkpoint.RequestID
	}
	observation.ctx = telemetry.WithBaggage(ctx, "original_request_id", originalID)
	if restoreErr == nil {
		observation.ctx = resumeCtx
		defer endSpan()
	}
	observation.emit("hitl.resume.claimed", "hitl_resume_claim", "success", "claimed", "", "", "info")
	if restoreErr != nil {
		cleanupCtx, cancel := r.cleanupContext(ctx)
		defer cancel()
		releaseErr := r.store.ReleaseCheckpointResumeClaim(cleanupCtx, checkpointID, claim.AttemptID)
		return nil, observation.failure(restoreErr, "restore", "context_restore", releaseErr, "release", "claim_release")
	}
	executionCtx := context.WithValue(resumeCtx, resumeRequestIDObserverKey{}, observation.observer)
	executionCtx, stopRenewal := r.startRenewal(executionCtx, claim)
	defer func() {
		if value := recover(); value != nil {
			_ = stopRenewal()
			observation.terminal("panic", "execute", "panic")
			panic(value)
		}
	}()

	// Recover a saved successor before replaying any application work.
	child, recoveryErr := r.successor(executionCtx, claim, "")
	if recoveryErr == nil && child == nil {
		observation.emit("hitl.resume.started", "hitl_resume_start", "success", "claimed", "", "", "info")
		// Keep ownership coordinates private to this call even if the adapter
		// modifies fields on its execution snapshot.
		executionCheckpoint := *claim.Checkpoint
		executionOwnership := *claim.Checkpoint.ResumeState
		executionCheckpoint.ResumeState = &executionOwnership
		result, err = executor.ExecuteResume(executionCtx, &executionCheckpoint)
	} else {
		err = recoveryErr
	}
	renewalErr := stopRenewal()
	if renewalErr != nil {
		return result, observation.failure(err, "execute", "execution", renewalErr, "renew", "renewal")
	}
	if recoveryErr != nil {
		// A partial or unverifiable successor is durable recovery evidence, not
		// permission to release the parent and replay immediately.
		return result, observation.failure(nil, "execute", "execution", recoveryErr, "recovery", "successor")
	}
	cleanupCtx, cancel := r.cleanupContext(observation.context())
	defer cancel()
	if child == nil {
		var successorErr error
		child, successorErr = r.successor(cleanupCtx, claim, GetCheckpointID(err))
		if successorErr != nil {
			return result, observation.failure(err, "execute", "execution", successorErr, "recovery", "successor")
		}
	}
	if child != nil {
		observation.successorID = child.CheckpointID
		if err != nil && !IsInterrupted(err) {
			// Keep the saved child for the next recovery. Do not turn an actual
			// application failure into a successful interruption response.
			return result, observation.failure(err, "execute", "execution", nil, "", "")
		}
		if finalErr := r.finalize(cleanupCtx, claim, ResumeFinalizationContinued, child.CheckpointID); finalErr != nil {
			return result, observation.failure(err, "execute", "execution", finalErr, "finalize", "finalization")
		}
		observation.terminal("continued", "finalize", "")
		return result, NewInterruptError(child)
	}
	if err == nil && result != nil && result.Success {
		if finalErr := r.finalize(cleanupCtx, claim, ResumeFinalizationCompleted, ""); finalErr != nil {
			return result, observation.failure(nil, "execute", "execution", finalErr, "finalize", "finalization")
		}
		observation.terminal("completed", "finalize", "")
		return result, nil
	}
	if err == nil {
		err = &ErrCheckpointExecutionFailed{}
	}
	releaseErr := r.store.ReleaseCheckpointResumeClaim(cleanupCtx, checkpointID, claim.AttemptID)
	return result, observation.failure(err, "execute", "execution", releaseErr, "release", "claim_release")
}

func (r *ResumeCoordinator) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), r.config.CleanupTimeout)
}

func (r *ResumeCoordinator) successor(ctx context.Context, claim *CheckpointResumeClaim, reportedID string) (*ExecutionCheckpoint, error) {
	state := claim.Checkpoint.ResumeState
	id := state.ReservedSuccessorCheckpointID
	if reportedID != "" && reportedID != id {
		return nil, &ErrCheckpointSuccessorConflict{CheckpointID: claim.Checkpoint.CheckpointID}
	}
	child, err := r.store.LoadCheckpoint(ctx, id)
	if IsCheckpointNotFound(err) && reportedID == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load resume successor: %w", err)
	}
	if child == nil || child.CheckpointID != id || child.ParentCheckpointID != claim.Checkpoint.CheckpointID ||
		child.ParentResumeAttemptID != state.ReservedSuccessorAttemptID || child.Status == CheckpointStatusPreparing {
		return nil, &ErrCheckpointSuccessorConflict{CheckpointID: claim.Checkpoint.CheckpointID}
	}
	return child, nil
}

func (r *ResumeCoordinator) finalize(ctx context.Context, claim *CheckpointResumeClaim, outcome ResumeFinalizationOutcome, successorID string) error {
	if err := r.renew(ctx, claim, "finalization"); err != nil {
		return &ErrCheckpointResumeLifecycle{Stage: "renew", Cause: err}
	}
	return r.store.FinalizeCheckpointResume(ctx, ResumeFinalization{CheckpointID: claim.Checkpoint.CheckpointID, AttemptID: claim.AttemptID, Outcome: outcome, SuccessorCheckpointID: successorID})
}

// The returned stop function is idempotent and always joins the renewal goroutine.
// Only a real renewal failure cancels the executor with a new cause; stopping a
// timer or canceling the caller is not evidence that another owner exists.
func (r *ResumeCoordinator) startRenewal(ctx context.Context, claim *CheckpointResumeClaim) (context.Context, func() error) {
	executionCtx, cancelExecution := context.WithCancelCause(ctx)
	renewCtx, cancelRenewal := context.WithCancel(executionCtx)
	done := make(chan struct{})
	var renewalErr error
	go func() {
		defer close(done)
		ticker := time.NewTicker(r.config.ClaimLease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				if renewCtx.Err() != nil {
					return
				}
				if err := r.renew(renewCtx, claim, "periodic"); err != nil {
					var lost *ErrCheckpointResumeClaimLost
					if renewCtx.Err() == nil || errors.As(err, &lost) || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
						renewalErr = err
						cancelExecution(err)
					}
					return
				}
			}
		}
	}()
	return executionCtx, func() error {
		cancelRenewal()
		<-done
		cancelExecution(nil)
		return renewalErr
	}
}

func (r *ResumeCoordinator) renew(ctx context.Context, claim *CheckpointResumeClaim, phase string) error {
	err := r.store.RenewCheckpointResumeClaim(ctx, claim.Checkpoint.CheckpointID, claim.AttemptID, r.config.ClaimLease)
	outcome := "succeeded"
	if err != nil {
		outcome, _ = resumeFailureClassification(err, "renewal")
		if outcome == "deadline" {
			outcome = "cancelled"
		}
	}
	r.counter(MetricResumeRenewal, "module", telemetry.ModuleOrchestration, "outcome", outcome, "phase", phase)
	return err
}

func (o *resumeObservation) reject(err error, outcome string) {
	level, errorType := "info", ""
	if outcome == "" {
		var busy *ErrCheckpointResumeInProgress
		var status *ErrCheckpointNotResumable
		switch {
		case errors.As(err, &busy):
			outcome = "in_progress"
		case errors.As(err, &status):
			outcome = "not_resumable"
		case IsCheckpointNotFound(err):
			outcome = "not_found"
		default:
			outcome, errorType = resumeFailureClassification(err, "claim")
			level = "error"
			if outcome == "cancelled" || outcome == "deadline" {
				level = "warn"
			}
			if outcome == "claim_lost" {
				outcome = "failed"
			}
		}
	}
	o.coordinator.counter(MetricResumeAttempt, "module", telemetry.ModuleOrchestration, "outcome", outcome)
	status := "rejected"
	if level == "error" {
		status = "error"
	}
	o.emit("hitl.resume.rejected", "hitl_resume_claim", status, outcome, "", errorType, level)
}

func (o *resumeObservation) failure(original error, stage, boundary string, lifecycle error, lifecycleStage, lifecycleBoundary string) error {
	cause := original
	if lifecycle != nil {
		cause, stage, boundary = lifecycle, lifecycleStage, lifecycleBoundary
		var renewal *ErrCheckpointResumeLifecycle
		if errors.As(lifecycle, &renewal) && renewal.Stage == "renew" {
			stage, boundary = "renew", "renewal"
		}
	}
	outcome, errorType := resumeFailureClassification(cause, boundary)
	if lifecycle != nil && outcome != "claim_lost" {
		outcome, errorType = "failed", boundary
	}
	o.terminal(outcome, stage, errorType)
	if lifecycle != nil {
		return &ErrCheckpointResumeLifecycle{Stage: stage, Cause: lifecycle, ExecutionError: original}
	}
	return &ErrCheckpointResumeLifecycle{Stage: stage, Cause: original}
}

var _ HITLResumer = (*ResumeCoordinator)(nil)
