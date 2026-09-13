package backendconformance

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

// CheckpointResumeFixture supplies independent adapters over one isolated
// dataset. Advance moves backend time and record expiry together.
type CheckpointResumeFixture struct {
	Persistence  orchestration.CheckpointPersistence
	Resumers     []orchestration.CheckpointResumePersistence
	Advance      func(time.Duration)
	RemainingTTL func(string) time.Duration
}

type CheckpointResumeFactory func(*testing.T) CheckpointResumeFixture

// RunCheckpointResumeConformance tests ownership independently of transport or
// provider implementation. The fixture must not share process-local claim locks.
func RunCheckpointResumeConformance(t *testing.T, factory CheckpointResumeFactory) {
	t.Helper()
	setup := func(t *testing.T) (CheckpointResumeFixture, orchestration.ResumeClaimRequest) {
		t.Helper()
		fixture := factory(t)
		if len(fixture.Resumers) < 2 || fixture.Advance == nil || fixture.RemainingTTL == nil {
			t.Fatal("resume conformance requires two independent adapters, time advancement, and retention inspection")
		}
		checkpoint := conformanceCheckpoint("resume-parent", time.Now().Add(time.Hour))
		checkpoint.Status = orchestration.CheckpointStatusApproved
		if err := fixture.Persistence.SaveCheckpoint(t.Context(), checkpoint); err != nil {
			t.Fatal(err)
		}
		return fixture, orchestration.ResumeClaimRequest{CheckpointID: checkpoint.CheckpointID,
			AttemptID: "attempt-a", Owner: "owner-a", Lease: 30 * time.Second, SuccessorCheckpointID: "resume-child"}
	}
	load := func(t *testing.T, fixture CheckpointResumeFixture, id string) *orchestration.ExecutionCheckpoint {
		t.Helper()
		checkpoint, err := fixture.Persistence.LoadCheckpoint(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return checkpoint
	}

	t.Run("two simultaneous claims have one owner", func(t *testing.T) {
		fixture, request := setup(t)
		start := make(chan struct{})
		results := make(chan error, 2)
		var group sync.WaitGroup
		for i, adapter := range fixture.Resumers[:2] {
			group.Add(1)
			go func() {
				defer group.Done()
				candidate := request
				if i == 1 {
					candidate.AttemptID = "attempt-b"
					candidate.Owner = "owner-b"
				}
				<-start
				_, err := adapter.ClaimCheckpointForResume(t.Context(), candidate)
				results <- err
			}()
		}
		close(start)
		group.Wait()
		close(results)
		successes, conflicts := 0, 0
		for err := range results {
			var conflict *orchestration.ErrCheckpointResumeInProgress
			switch {
			case err == nil:
				successes++
			case errors.As(err, &conflict):
				conflicts++
			default:
				t.Fatalf("unexpected claim failure: %v", err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("claims: successes=%d conflicts=%d", successes, conflicts)
		}
	})

	t.Run("claim snapshot and finalization preserve retention", func(t *testing.T) {
		fixture, request := setup(t)
		fixture.Advance(time.Minute)
		ttl := fixture.RemainingTTL(request.CheckpointID)
		claim, err := fixture.Resumers[0].ClaimCheckpointForResume(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if claim.Checkpoint.Status != orchestration.CheckpointStatusApproved || claim.PreviousStatus != orchestration.CheckpointStatusApproved {
			t.Fatalf("execution snapshot = %#v", claim)
		}
		claim.Checkpoint.OriginalRequest = "caller mutation"
		stored := load(t, fixture, request.CheckpointID)
		if stored.Status != orchestration.CheckpointStatusResuming || stored.OriginalRequest == "caller mutation" {
			t.Fatalf("durable checkpoint = %#v", stored)
		}
		if err := fixture.Resumers[0].RenewCheckpointResumeClaim(t.Context(), request.CheckpointID, request.AttemptID, time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Resumers[0].FinalizeCheckpointResume(t.Context(), orchestration.ResumeFinalization{CheckpointID: request.CheckpointID, AttemptID: request.AttemptID, Outcome: orchestration.ResumeFinalizationCompleted}); err != nil {
			t.Fatal(err)
		}
		stored = load(t, fixture, request.CheckpointID)
		if stored.Status != orchestration.CheckpointStatusCompleted || stored.ResumeState.Outcome != orchestration.ResumeFinalizationCompleted {
			t.Fatalf("completion = %#v", stored)
		}
		remaining := fixture.RemainingTTL(request.CheckpointID)
		if remaining > ttl || remaining <= 0 {
			t.Fatalf("retention changed from %v to %v", ttl, remaining)
		}
		_, err = fixture.Resumers[1].ClaimCheckpointForResume(t.Context(), request)
		var terminal *orchestration.ErrCheckpointNotResumable
		if !errors.As(err, &terminal) {
			t.Fatalf("terminal claim = %v", err)
		}
	})

	t.Run("expired ownership cannot renew release or finalize", func(t *testing.T) {
		fixture, request := setup(t)
		first, err := fixture.Resumers[0].ClaimCheckpointForResume(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		fixture.Advance(request.Lease + time.Second)
		operations := []func() error{
			func() error {
				return fixture.Resumers[0].RenewCheckpointResumeClaim(t.Context(), request.CheckpointID, request.AttemptID, request.Lease)
			},
			func() error {
				return fixture.Resumers[0].ReleaseCheckpointResumeClaim(t.Context(), request.CheckpointID, request.AttemptID)
			},
			func() error {
				return fixture.Resumers[0].FinalizeCheckpointResume(t.Context(), orchestration.ResumeFinalization{CheckpointID: request.CheckpointID, AttemptID: request.AttemptID, Outcome: orchestration.ResumeFinalizationCompleted})
			},
		}
		for _, operation := range operations {
			var lost *orchestration.ErrCheckpointResumeClaimLost
			if err := operation(); !errors.As(err, &lost) {
				t.Fatalf("expired owner mutation = %v", err)
			}
		}
		recovery := request
		recovery.AttemptID, recovery.Owner, recovery.SuccessorCheckpointID = "attempt-b", "owner-b", "unused-child"
		second, err := fixture.Resumers[1].ClaimCheckpointForResume(t.Context(), recovery)
		if err != nil {
			t.Fatal(err)
		}
		if second.Checkpoint.ResumeState.ReservedSuccessorCheckpointID != first.Checkpoint.ResumeState.ReservedSuccessorCheckpointID {
			t.Fatal("recovery replaced successor reservation")
		}
		for _, operation := range operations {
			var lost *orchestration.ErrCheckpointResumeClaimLost
			if err := operation(); !errors.As(err, &lost) {
				t.Fatalf("stale owner mutation = %v", err)
			}
		}
		if err := fixture.Resumers[1].ReleaseCheckpointResumeClaim(t.Context(), recovery.CheckpointID, recovery.AttemptID); err != nil {
			t.Fatal(err)
		}
		stored := load(t, fixture, recovery.CheckpointID)
		if stored.Status != orchestration.CheckpointStatusApproved || stored.ResumeState != nil {
			t.Fatalf("released checkpoint = %#v", stored)
		}
	})

	t.Run("generic save status update and deletion cannot defeat ownership", func(t *testing.T) {
		fixture, request := setup(t)
		stale := load(t, fixture, request.CheckpointID)
		if _, err := fixture.Resumers[0].ClaimCheckpointForResume(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		stale.Status = orchestration.CheckpointStatusPending
		if err := fixture.Persistence.SaveCheckpoint(t.Context(), stale); err == nil {
			t.Fatal("stale snapshot overwrote ownership")
		}
		if err := fixture.Persistence.UpdateCheckpointStatus(t.Context(), request.CheckpointID, orchestration.CheckpointStatusPending, orchestration.CheckpointStatusApproved); err == nil {
			t.Fatal("stale decision overwrote ownership")
		}
		for _, advance := range []time.Duration{0, request.Lease + time.Second} {
			fixture.Advance(advance)
			var conflict *orchestration.ErrCheckpointDeletionConflict
			if err := fixture.Persistence.DeleteCheckpoint(t.Context(), request.CheckpointID); !errors.As(err, &conflict) {
				t.Fatalf("owned deletion = %v", err)
			}
		}
		stored := load(t, fixture, request.CheckpointID)
		if stored.Status != orchestration.CheckpointStatusResuming || stored.ResumeState.AttemptID != request.AttemptID {
			t.Fatalf("ownership was changed: %#v", stored)
		}
	})

	t.Run("durable successor survives recovery without replacing provenance", func(t *testing.T) {
		fixture, request := setup(t)
		if _, err := fixture.Resumers[0].ClaimCheckpointForResume(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		child := conformanceCheckpoint(request.SuccessorCheckpointID, time.Now().Add(time.Hour))
		child.ParentCheckpointID, child.ParentResumeAttemptID = request.CheckpointID, request.AttemptID
		child.Status = orchestration.CheckpointStatusPreparing
		if err := fixture.Persistence.SaveCheckpoint(t.Context(), child); err != nil {
			t.Fatal(err)
		}
		finalize := orchestration.ResumeFinalization{CheckpointID: request.CheckpointID, AttemptID: request.AttemptID, Outcome: orchestration.ResumeFinalizationContinued, SuccessorCheckpointID: child.CheckpointID}
		var invalid *orchestration.ErrCheckpointSuccessorConflict
		if err := fixture.Resumers[0].FinalizeCheckpointResume(t.Context(), finalize); !errors.As(err, &invalid) {
			t.Fatalf("partial successor finalization = %v", err)
		}
		if err := fixture.Resumers[0].ReleaseCheckpointResumeClaim(t.Context(), request.CheckpointID, request.AttemptID); !errors.As(err, &invalid) {
			t.Fatalf("release discarded partial successor: %v", err)
		}
		child.Status = orchestration.CheckpointStatusPending
		if err := fixture.Persistence.SaveCheckpoint(t.Context(), child); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Persistence.SaveCheckpoint(t.Context(), child); err == nil {
			t.Fatal("authoritative successor allowed a full-record rewrite")
		}
		fixture.Advance(request.Lease + time.Second)
		recovery := request
		recovery.AttemptID, recovery.Owner = "attempt-b", "owner-b"
		if _, err := fixture.Resumers[1].ClaimCheckpointForResume(t.Context(), recovery); err != nil {
			t.Fatal(err)
		}
		finalize.AttemptID = recovery.AttemptID
		if err := fixture.Resumers[1].FinalizeCheckpointResume(t.Context(), finalize); err != nil {
			t.Fatal(err)
		}
		parent := load(t, fixture, request.CheckpointID)
		if parent.Status != orchestration.CheckpointStatusContinued || parent.ResumeState.SuccessorCheckpointID != child.CheckpointID {
			t.Fatalf("continued parent = %#v", parent)
		}
		if stored := load(t, fixture, child.CheckpointID); stored.ParentResumeAttemptID != request.AttemptID {
			t.Fatalf("child provenance changed: %#v", stored)
		}
	})

	t.Run("expired records are never recreated by ownership mutations", func(t *testing.T) {
		fixture, request := setup(t)
		if _, err := fixture.Resumers[0].ClaimCheckpointForResume(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		fixture.Advance(fixture.RemainingTTL(request.CheckpointID) + time.Second)
		var lost *orchestration.ErrCheckpointResumeClaimLost
		if err := fixture.Resumers[0].RenewCheckpointResumeClaim(t.Context(), request.CheckpointID, request.AttemptID, request.Lease); !errors.As(err, &lost) {
			t.Fatalf("missing renewal = %v", err)
		}
		if err := fixture.Persistence.DeleteCheckpoint(t.Context(), request.CheckpointID); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.Resumers[1].ClaimCheckpointForResume(t.Context(), request)
		if !orchestration.IsCheckpointNotFound(err) {
			t.Fatalf("expired checkpoint claim = %v", err)
		}
	})
}
