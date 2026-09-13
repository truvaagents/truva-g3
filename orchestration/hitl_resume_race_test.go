package orchestration

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

func TestResumeShutdownDoesNotHideConcurrentRenewalFailure(t *testing.T) {
	for _, cause := range []error{errors.New("renewal backend unavailable"), &ErrCheckpointResumeClaimLost{CheckpointID: "parent"}} {
		t.Run(cause.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan struct{})
				store := &resumeStoreStub{renew: func(ctx context.Context, _, _ string, _ time.Duration) error {
					close(entered)
					<-ctx.Done() // stopRenewal races the actual backend failure.
					return cause
				}}
				r, err := NewResumeCoordinator(store, ResumeExecutorFunc(func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error) {
					<-entered
					return &ExecutionResult{Success: true}, nil
				}), ResumeCoordinatorRuntimeConfig{ClaimLease: 3 * time.Second, CleanupTimeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				result, err := r.ResumeExecution(t.Context(), "parent")
				if result == nil || !errors.Is(err, cause) {
					t.Fatalf("result=%v error=%v", result, err)
				}
				if got := store.operations(); !reflect.DeepEqual(got, []string{"claim", "load", "renew"}) {
					t.Fatalf("finalized or released after renewal failure: %v", got)
				}
			})
		})
	}
}

func TestResumeExecutorCannotReplaceClaimCoordinates(t *testing.T) {
	store := &resumeStoreStub{
		renew: func(_ context.Context, id, attempt string, _ time.Duration) error {
			if id != "parent" || attempt == "replaced" {
				t.Fatal("executor changed ownership")
			}
			return nil
		},
		finalize: func(_ context.Context, final ResumeFinalization) error {
			if final.CheckpointID != "parent" || final.AttemptID == "replaced" {
				t.Fatal("executor changed finalization")
			}
			return nil
		},
	}
	r := newResumeTestCoordinator(t, store, func(_ context.Context, checkpoint *ExecutionCheckpoint) (*ExecutionResult, error) {
		checkpoint.CheckpointID = "replaced"
		checkpoint.ResumeState.AttemptID = "replaced"
		checkpoint.ResumeState.ReservedSuccessorCheckpointID = "replaced"
		return &ExecutionResult{Success: true}, nil
	})
	if _, err := r.ResumeExecution(t.Context(), "parent"); err != nil {
		t.Fatal(err)
	}
}

func TestResumeCoordinatorRejectsMalformedClaimBeforeExecution(t *testing.T) {
	for _, mutate := range []func(*CheckpointResumeClaim){
		func(c *CheckpointResumeClaim) { c.Checkpoint = nil },
		func(c *CheckpointResumeClaim) { c.AttemptID = "wrong" },
		func(c *CheckpointResumeClaim) { c.Checkpoint.ResumeState.Owner = "wrong" },
		func(c *CheckpointResumeClaim) { c.Checkpoint.Status = CheckpointStatusPending },
		func(c *CheckpointResumeClaim) { c.Checkpoint.ResumeState.ReservedSuccessorAttemptID = "" },
	} {
		store := &resumeStoreStub{claim: func(_ context.Context, request ResumeClaimRequest) (*CheckpointResumeClaim, error) {
			claim := stubResumeClaim(request)
			mutate(claim)
			return claim, nil
		}}
		r := newResumeTestCoordinator(t, store, func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error) {
			t.Fatal("malformed claim executed")
			return nil, nil
		})
		var conflict *ErrCheckpointSuccessorConflict
		if _, err := r.ResumeExecution(t.Context(), "parent"); !errors.As(err, &conflict) {
			t.Fatalf("error=%v", err)
		}
		if got := store.operations(); !reflect.DeepEqual(got, []string{"claim"}) {
			t.Fatalf("operations=%v", got)
		}
	}
}

func TestCheckpointClaimCannotCommitWithAnAlreadyExpiredLease(t *testing.T) {
	server, client := setupCheckpointTestRedis(t)
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now().UTC().Truncate(time.Millisecond)
	server.SetTime(now)
	store := newCheckpointTestStore(t, client)
	if err := store.SaveCheckpoint(t.Context(), &ExecutionCheckpoint{CheckpointID: "parent", Status: CheckpointStatusApproved}); err != nil {
		t.Fatal(err)
	}
	client.AddHook(&checkpointBeforeCommitHook{before: func() { server.SetTime(now.Add(4 * time.Second)) }})
	_, err := store.ClaimCheckpointForResume(t.Context(), ResumeClaimRequest{CheckpointID: "parent", AttemptID: "attempt", Owner: "owner", Lease: 3 * time.Second, SuccessorCheckpointID: "child"})
	var lost *ErrCheckpointResumeClaimLost
	if !errors.As(err, &lost) {
		t.Fatalf("expired claim=%v", err)
	}
	cp, err := store.LoadCheckpoint(t.Context(), "parent")
	if err != nil || cp.Status != CheckpointStatusApproved || cp.ResumeState != nil {
		t.Fatalf("expired claim changed record: %v %v", cp, err)
	}
}
