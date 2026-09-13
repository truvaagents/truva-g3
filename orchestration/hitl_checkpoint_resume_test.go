package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type checkpointBeforeCommitHook struct {
	once   sync.Once
	before func()
}

func (*checkpointBeforeCommitHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (*checkpointBeforeCommitHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (hook *checkpointBeforeCommitHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		hook.once.Do(hook.before)
		return next(ctx, commands)
	}
}

func TestCheckpointLeaseIsCheckedAtCommit(t *testing.T) {
	for _, operation := range []string{"renew", "release", "finalize", "successor save"} {
		t.Run(operation, func(t *testing.T) {
			server, client := setupCheckpointTestRedis(t)
			t.Cleanup(server.Close)
			t.Cleanup(func() { _ = client.Close() })
			now := time.Now().UTC().Truncate(time.Millisecond)
			server.SetTime(now)
			store := newCheckpointTestStore(t, client)
			checkpoint := &ExecutionCheckpoint{CheckpointID: "parent", RequestID: "request", Status: CheckpointStatusApproved}
			if err := store.SaveCheckpoint(t.Context(), checkpoint); err != nil {
				t.Fatal(err)
			}
			request := ResumeClaimRequest{CheckpointID: "parent", AttemptID: "attempt", Owner: "owner", Lease: 3 * time.Second, SuccessorCheckpointID: "child"}
			if _, err := store.ClaimCheckpointForResume(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			before, err := client.Get(t.Context(), store.keys.checkpoint("parent")).Result()
			if err != nil {
				t.Fatal(err)
			}
			client.AddHook(&checkpointBeforeCommitHook{before: func() { server.SetTime(now.Add(4 * time.Second)) }})
			switch operation {
			case "renew":
				err = store.RenewCheckpointResumeClaim(t.Context(), "parent", "attempt", time.Minute)
			case "release":
				err = store.ReleaseCheckpointResumeClaim(t.Context(), "parent", "attempt")
			case "finalize":
				err = store.FinalizeCheckpointResume(t.Context(), ResumeFinalization{CheckpointID: "parent", AttemptID: "attempt", Outcome: ResumeFinalizationCompleted})
			case "successor save":
				err = store.SaveCheckpoint(t.Context(), &ExecutionCheckpoint{CheckpointID: "child", ParentCheckpointID: "parent", ParentResumeAttemptID: "attempt", Status: CheckpointStatusPending})
			}
			var lost *ErrCheckpointResumeClaimLost
			if !errors.As(err, &lost) {
				t.Fatalf("lease-expired commit returned %v", err)
			}
			after, readErr := client.Get(t.Context(), store.keys.checkpoint("parent")).Result()
			if readErr != nil || after != before || server.Exists(store.keys.checkpoint("child")) {
				t.Fatalf("expired write changed state: error=%v", readErr)
			}
		})
	}
}

func TestCheckpointDeleteVersusClaimIsAtomic(t *testing.T) {
	for _, first := range []string{"claim", "delete"} {
		t.Run(first+" wins", func(t *testing.T) {
			server, client := setupCheckpointTestRedis(t)
			t.Cleanup(server.Close)
			t.Cleanup(func() { _ = client.Close() })
			otherClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = otherClient.Close() })
			store, other := newCheckpointTestStore(t, client), newCheckpointTestStore(t, otherClient)
			if err := store.SaveCheckpoint(t.Context(), &ExecutionCheckpoint{CheckpointID: "parent", Status: CheckpointStatusApproved}); err != nil {
				t.Fatal(err)
			}
			request := ResumeClaimRequest{CheckpointID: "parent", AttemptID: "attempt", Owner: "owner", Lease: time.Minute, SuccessorCheckpointID: "child"}
			var winningErr error
			client.AddHook(&checkpointBeforeCommitHook{before: func() {
				if first == "claim" {
					_, winningErr = other.ClaimCheckpointForResume(t.Context(), request)
				} else {
					winningErr = other.DeleteCheckpoint(t.Context(), "parent")
				}
			}})
			if first == "claim" {
				var conflict *ErrCheckpointDeletionConflict
				if err := store.DeleteCheckpoint(t.Context(), "parent"); !errors.As(err, &conflict) {
					t.Fatalf("delete lost race: %v", err)
				}
				stored, err := store.LoadCheckpoint(t.Context(), "parent")
				if err != nil || stored.Status != CheckpointStatusResuming {
					t.Fatalf("claim not retained: %#v %v", stored, err)
				}
			} else {
				if _, err := store.ClaimCheckpointForResume(t.Context(), request); !IsCheckpointNotFound(err) {
					t.Fatalf("claim lost race: %v", err)
				}
				if server.Exists(store.keys.checkpoint("parent")) {
					t.Fatal("claim recreated deleted checkpoint")
				}
			}
			if winningErr != nil {
				t.Fatalf("winning operation: %v", winningErr)
			}
		})
	}
}

func TestCheckpointConcurrentDecisionsHaveOneWinner(t *testing.T) {
	server, client := setupCheckpointTestRedis(t)
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = client.Close() })
	otherClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = otherClient.Close() })
	store, other := newCheckpointTestStore(t, client), newCheckpointTestStore(t, otherClient)
	if err := store.SaveCheckpoint(t.Context(), &ExecutionCheckpoint{CheckpointID: "decision", Status: CheckpointStatusPending}); err != nil {
		t.Fatal(err)
	}
	var winner error
	client.AddHook(&checkpointBeforeCommitHook{before: func() {
		winner = other.UpdateCheckpointStatus(t.Context(), "decision", CheckpointStatusPending, CheckpointStatusRejected)
	}})
	err := store.UpdateCheckpointStatus(t.Context(), "decision", CheckpointStatusPending, CheckpointStatusApproved)
	var conflict *ErrCheckpointStatusConflict
	if winner != nil || !errors.As(err, &conflict) {
		t.Fatalf("decision race: winner=%v loser=%v", winner, err)
	}
	stored, err := store.LoadCheckpoint(t.Context(), "decision")
	if err != nil || stored.Status != CheckpointStatusRejected {
		t.Fatalf("decision = %#v %v", stored, err)
	}
	pending, err := client.SIsMember(t.Context(), store.keys.pending(), "decision").Result()
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("decided checkpoint remained pending")
	}
}

func TestCheckpointStatusClassificationIsExhaustive(t *testing.T) {
	for _, test := range []struct {
		status                       CheckpointStatus
		pending, resumable, terminal bool
	}{
		{CheckpointStatusPreparing, false, false, false},
		{CheckpointStatusPending, true, false, false},
		{CheckpointStatusApproved, false, true, false},
		{CheckpointStatusExpiredApproved, false, true, false},
		{CheckpointStatusResuming, false, false, false},
		{CheckpointStatusContinued, false, false, true},
		{CheckpointStatusCompleted, false, false, true},
		{CheckpointStatusRejected, false, false, true},
		{CheckpointStatusAborted, false, false, true},
		{CheckpointStatusExpired, false, false, true},
		{CheckpointStatusExpiredRejected, false, false, true},
		{CheckpointStatusExpiredAborted, false, false, true},
		{CheckpointStatusEdited, false, false, false},
		{"", false, false, false},
		{"unknown", false, false, false},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			if IsPendingStatus(test.status) != test.pending || IsResumableStatus(test.status) != test.resumable || IsTerminalStatus(test.status) != test.terminal {
				t.Fatalf("wrong classification for %q", test.status)
			}
		})
	}
}
