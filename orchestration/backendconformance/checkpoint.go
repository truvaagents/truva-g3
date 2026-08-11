// Package backendconformance contains provider-neutral contract suites for
// orchestration backend implementations. Runtime code must not import it.
package backendconformance

import (
	"context"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

type CheckpointFixture struct {
	Persistence orchestration.CheckpointPersistence
	Sources     []orchestration.ExpiredCheckpointSource
	Advance     func(time.Duration)
}

type CheckpointFactory func(*testing.T) CheckpointFixture

func RunCheckpointConformance(t *testing.T, factory CheckpointFactory) {
	t.Helper()
	t.Run("persistence round trip and status transition", func(t *testing.T) {
		fixture := factory(t)
		checkpoint := conformanceCheckpoint("round-trip", time.Now().Add(time.Hour))
		if err := fixture.Persistence.SaveCheckpoint(t.Context(), checkpoint); err != nil {
			t.Fatal(err)
		}
		loaded, err := fixture.Persistence.LoadCheckpoint(t.Context(), checkpoint.CheckpointID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.CheckpointID != checkpoint.CheckpointID || loaded.Status != orchestration.CheckpointStatusPending {
			t.Fatalf("loaded checkpoint = %#v", loaded)
		}
		if err := fixture.Persistence.UpdateCheckpointStatus(t.Context(), checkpoint.CheckpointID, orchestration.CheckpointStatusApproved); err != nil {
			t.Fatal(err)
		}
		loaded, err = fixture.Persistence.LoadCheckpoint(t.Context(), checkpoint.CheckpointID)
		if err != nil || loaded.Status != orchestration.CheckpointStatusApproved {
			t.Fatalf("updated checkpoint = %#v, %v", loaded, err)
		}
		if err := fixture.Persistence.DeleteCheckpoint(t.Context(), checkpoint.CheckpointID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.Persistence.LoadCheckpoint(t.Context(), checkpoint.CheckpointID); err == nil {
			t.Fatal("deleted checkpoint remained readable")
		}
	})

	t.Run("claims are eligible bounded and cross instance", func(t *testing.T) {
		fixture := factory(t)
		if len(fixture.Sources) < 2 {
			t.Fatal("two independently constructed sources are required")
		}
		past := time.Now().Add(-time.Minute)
		for _, checkpoint := range []*orchestration.ExecutionCheckpoint{
			conformanceCheckpoint("expired-1", past),
			conformanceCheckpoint("expired-2", past),
			conformanceCheckpoint("future", time.Now().Add(time.Hour)),
			func() *orchestration.ExecutionCheckpoint {
				checkpoint := conformanceCheckpoint("already-approved", past)
				checkpoint.Status = orchestration.CheckpointStatusApproved
				return checkpoint
			}(),
		} {
			if err := fixture.Persistence.SaveCheckpoint(t.Context(), checkpoint); err != nil {
				t.Fatal(err)
			}
		}
		request := orchestration.ExpiredCheckpointClaimRequest{Before: time.Now(), Limit: 1, Owner: "owner-a", Lease: time.Minute}
		claimed, err := fixture.Sources[0].ClaimExpiredCheckpoints(t.Context(), request)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("first claim = %#v, %v", claimed, err)
		}
		other := request
		other.Limit = 10
		other.Owner = "owner-b"
		claimedByOther, err := fixture.Sources[1].ClaimExpiredCheckpoints(t.Context(), other)
		if err != nil || len(claimedByOther) != 1 {
			t.Fatalf("cross-instance claim = %#v, %v", claimedByOther, err)
		}
		if claimedByOther[0].CheckpointID == claimed[0].CheckpointID || claimedByOther[0].CheckpointID == "future" {
			t.Fatalf("ineligible or already claimed checkpoint returned: %#v", claimedByOther)
		}
		if err := fixture.Sources[1].ReleaseExpiredCheckpointClaim(t.Context(), claimed[0].CheckpointID, "wrong-owner"); err != nil {
			t.Fatal(err)
		}
		probe := request
		probe.Owner = "owner-c"
		probe.Limit = 10
		stillClaimed, err := fixture.Sources[1].ClaimExpiredCheckpoints(t.Context(), probe)
		if err != nil {
			t.Fatal(err)
		}
		for _, checkpoint := range stillClaimed {
			if checkpoint.CheckpointID == claimed[0].CheckpointID {
				t.Fatal("wrong-owner release removed another owner's claim")
			}
		}
		if err := fixture.Sources[0].ReleaseExpiredCheckpointClaim(t.Context(), claimed[0].CheckpointID, "owner-a"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expired lease becomes claimable", func(t *testing.T) {
		fixture := factory(t)
		if len(fixture.Sources) < 2 || fixture.Advance == nil {
			t.Fatal("two sources and deterministic time advancement are required")
		}
		checkpoint := conformanceCheckpoint("lease-expiry", time.Now().Add(-time.Minute))
		if err := fixture.Persistence.SaveCheckpoint(t.Context(), checkpoint); err != nil {
			t.Fatal(err)
		}
		request := orchestration.ExpiredCheckpointClaimRequest{Before: time.Now(), Limit: 1, Owner: "owner-a", Lease: time.Second}
		claimed, err := fixture.Sources[0].ClaimExpiredCheckpoints(t.Context(), request)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("initial claim = %#v, %v", claimed, err)
		}
		fixture.Advance(2 * time.Second)
		request.Owner = "owner-b"
		claimed, err = fixture.Sources[1].ClaimExpiredCheckpoints(t.Context(), request)
		if err != nil || len(claimed) != 1 || claimed[0].CheckpointID != checkpoint.CheckpointID {
			t.Fatalf("claim after lease expiry = %#v, %v", claimed, err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		fixture := factory(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := fixture.Sources[0].ClaimExpiredCheckpoints(ctx, orchestration.ExpiredCheckpointClaimRequest{
			Before: time.Now(), Limit: 1, Owner: "owner", Lease: time.Second,
		})
		if err == nil {
			t.Fatal("cancelled claim returned nil error")
		}
	})
}

func conformanceCheckpoint(id string, expiresAt time.Time) *orchestration.ExecutionCheckpoint {
	return &orchestration.ExecutionCheckpoint{
		CheckpointID: id,
		RequestID:    "request-" + id,
		Status:       orchestration.CheckpointStatusPending,
		CreatedAt:    time.Now().Add(-time.Hour),
		ExpiresAt:    expiresAt,
		RequestMode:  orchestration.RequestModeNonStreaming,
	}
}
