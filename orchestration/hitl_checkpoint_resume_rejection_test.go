package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type resumeRejectionValue struct {
	kind    string
	value   string
	members []string
	ttl     time.Duration
}

func snapshotResumeRejectionState(t *testing.T, server *miniredis.Miniredis) map[string]resumeRejectionValue {
	t.Helper()
	snapshot := make(map[string]resumeRejectionValue)
	for _, key := range server.Keys() {
		value := resumeRejectionValue{kind: server.Type(key), ttl: server.TTL(key)}
		var err error
		switch value.kind {
		case "string":
			value.value, err = server.Get(key)
		case "set":
			value.members, err = server.SMembers(key)
			slices.Sort(value.members)
		default:
			t.Fatalf("unexpected checkpoint key type %q", value.kind)
		}
		if err != nil {
			t.Fatal(err)
		}
		snapshot[key] = value
	}
	return snapshot
}

func newResumeRejectionFixture(t *testing.T, owned bool) (*miniredis.Miniredis, *RedisCheckpointStore) {
	t.Helper()
	server, client := setupCheckpointTestRedis(t)
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = client.Close() })
	server.SetTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	store := newCheckpointTestStore(t, client)
	if err := store.SaveCheckpoint(t.Context(), &ExecutionCheckpoint{CheckpointID: "parent", RequestID: "request", Status: CheckpointStatusApproved}); err != nil {
		t.Fatal(err)
	}
	if owned {
		if err := runResumeRejectionOperation(t, store, "claim"); err != nil {
			t.Fatal(err)
		}
	}
	return server, store
}

func runResumeRejectionOperation(t *testing.T, store *RedisCheckpointStore, operation string) error {
	t.Helper()
	switch operation {
	case "claim":
		claim, err := store.ClaimCheckpointForResume(t.Context(), ResumeClaimRequest{CheckpointID: "parent", AttemptID: "attempt", Owner: "owner", Lease: time.Minute, SuccessorCheckpointID: "child"})
		if (err == nil) != (claim != nil) {
			t.Fatal("claim result does not match its success or failure")
		}
		return err
	case "renew":
		return store.RenewCheckpointResumeClaim(t.Context(), "parent", "attempt", time.Minute)
	case "release":
		return store.ReleaseCheckpointResumeClaim(t.Context(), "parent", "attempt")
	case "finalize":
		return store.FinalizeCheckpointResume(t.Context(), ResumeFinalization{CheckpointID: "parent", AttemptID: "attempt", Outcome: ResumeFinalizationCompleted})
	case "save":
		return store.SaveCheckpoint(t.Context(), &ExecutionCheckpoint{CheckpointID: "parent", RequestID: "request", Status: CheckpointStatusPending})
	default:
		t.Fatalf("unknown test operation %q", operation)
		return nil
	}
}

func TestCheckpointResumeRejectsCorruptRecordsWithoutMutation(t *testing.T) {
	for _, body := range []string{`{`, `{"checkpoint_id":"different-record","status":"approved"}`} {
		for _, operation := range []string{"claim", "renew", "release", "finalize", "save"} {
			t.Run(operation+"/"+body, func(t *testing.T) {
				server, store := newResumeRejectionFixture(t, operation != "claim")
				if err := server.Set(store.keys.checkpoint("parent"), body); err != nil {
					t.Fatal(err)
				}
				before := snapshotResumeRejectionState(t, server)
				err := runResumeRejectionOperation(t, store, operation)
				if body == "{" {
					var syntax *json.SyntaxError
					if !errors.As(err, &syntax) {
						t.Fatalf("lost decoding error: %v", err)
					}
				} else {
					var conflict *ErrCheckpointSuccessorConflict
					if !errors.As(err, &conflict) || conflict.CheckpointID != "parent" {
						t.Fatalf("mismatched record identity accepted: %v", err)
					}
				}
				if after := snapshotResumeRejectionState(t, server); !reflect.DeepEqual(after, before) {
					t.Fatal("rejection changed records, indexes, or retention")
				}
			})
		}
	}
}

type checkpointCommandFailureHook struct {
	command string
	after   int
	calls   int
	cause   error
}

func (*checkpointCommandFailureHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}
func (*checkpointCommandFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (hook *checkpointCommandFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() == hook.command {
			hook.calls++
			if hook.calls == hook.after {
				command.SetErr(hook.cause)
				return hook.cause
			}
		}
		return next(ctx, command)
	}
}

func TestCheckpointResumeBackendFailuresPreserveCauseAndState(t *testing.T) {
	for _, test := range []struct {
		name, operation, command string
		after                    int
	}{
		{"claim read", "claim", "get", 1},
		{"claim clock", "claim", "time", 1},
		{"renew read", "renew", "get", 1},
		{"ownership clock", "renew", "time", 1},
		{"renewal clock", "renew", "time", 2},
		{"release successor watch", "release", "watch", 2},
		{"release successor read", "release", "get", 2},
		{"finalize successor watch", "finalize", "watch", 2},
		{"finalize successor read", "finalize", "get", 2},
		{"save read", "save", "get", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, store := newResumeRejectionFixture(t, test.operation != "claim")
			failure := errors.New("injected backend failure")
			hook := &checkpointCommandFailureHook{command: test.command, after: test.after, cause: failure}
			store.client.AddHook(hook)
			before := snapshotResumeRejectionState(t, server)
			if err := runResumeRejectionOperation(t, store, test.operation); !errors.Is(err, failure) {
				t.Fatalf("backend cause replaced: %v", err)
			}
			if hook.calls != test.after {
				t.Fatalf("backend failure retried or injection not reached: calls=%d, want %d", hook.calls, test.after)
			}
			if after := snapshotResumeRejectionState(t, server); !reflect.DeepEqual(after, before) {
				t.Fatal("backend failure changed records, indexes, or retention")
			}
		})
	}
}

func TestCheckpointSuccessorSaveRejectsInvalidLineage(t *testing.T) {
	for _, scenario := range []string{"missing parent", "wrong attempt", "unreserved ID", "invalid status", "orphan attempt", "supplied ownership"} {
		t.Run(scenario, func(t *testing.T) {
			server, store := newResumeRejectionFixture(t, true)
			child := &ExecutionCheckpoint{CheckpointID: "child", RequestID: "child-request", ParentCheckpointID: "parent", ParentResumeAttemptID: "attempt", Status: CheckpointStatusPending}
			switch scenario {
			case "missing parent":
				child.ParentCheckpointID = "missing"
			case "wrong attempt":
				child.ParentResumeAttemptID = "different-attempt"
			case "unreserved ID":
				child.CheckpointID = "unreserved"
			case "invalid status":
				child.Status = CheckpointStatusApproved
			case "orphan attempt":
				child.ParentCheckpointID = ""
			case "supplied ownership":
				child.ResumeState = &CheckpointResumeState{AttemptID: "attempt"}
			}
			before := snapshotResumeRejectionState(t, server)
			err := store.SaveCheckpoint(t.Context(), child)
			switch scenario {
			case "missing parent", "wrong attempt":
				var lost *ErrCheckpointResumeClaimLost
				if !errors.As(err, &lost) || lost.CheckpointID != child.ParentCheckpointID {
					t.Fatalf("unowned parent accepted: %v", err)
				}
			case "supplied ownership":
				var conflict *ErrCheckpointStatusConflict
				if !errors.As(err, &conflict) || conflict.CheckpointID != child.CheckpointID {
					t.Fatalf("unclaimed ownership accepted: %v", err)
				}
			default:
				var conflict *ErrCheckpointSuccessorConflict
				if !errors.As(err, &conflict) || conflict.CheckpointID != child.CheckpointID {
					t.Fatalf("invalid lineage accepted: %v", err)
				}
			}
			if after := snapshotResumeRejectionState(t, server); !reflect.DeepEqual(after, before) {
				t.Fatal("invalid successor changed records, indexes, or retention")
			}
		})
	}
}

func TestCheckpointFinalizationRejectsInvalidSuccessor(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong ID", "wrong parent", "wrong attempt", "preparing", "completed with successor", "release with successor"} {
		t.Run(scenario, func(t *testing.T) {
			server, store := newResumeRejectionFixture(t, true)
			child := &ExecutionCheckpoint{CheckpointID: "child", ParentCheckpointID: "parent", ParentResumeAttemptID: "attempt", Status: CheckpointStatusPending}
			final := ResumeFinalization{CheckpointID: "parent", AttemptID: "attempt", Outcome: ResumeFinalizationContinued, SuccessorCheckpointID: "child"}
			switch scenario {
			case "wrong ID":
				final.SuccessorCheckpointID = "different-child"
			case "wrong parent":
				child.ParentCheckpointID = "different-parent"
			case "wrong attempt":
				child.ParentResumeAttemptID = "different-attempt"
			case "preparing":
				child.Status = CheckpointStatusPreparing
			case "completed with successor":
				final.Outcome, final.SuccessorCheckpointID = ResumeFinalizationCompleted, ""
			}
			if scenario != "missing" {
				// Seed the stored successor directly, including invalid lineage cases.
				data, err := json.Marshal(child)
				if err != nil {
					t.Fatal(err)
				}
				if err := server.Set(store.keys.checkpoint("child"), string(data)); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotResumeRejectionState(t, server)
			var err error
			if scenario == "release with successor" {
				err = store.ReleaseCheckpointResumeClaim(t.Context(), "parent", "attempt")
			} else {
				err = store.FinalizeCheckpointResume(t.Context(), final)
			}
			var conflict *ErrCheckpointSuccessorConflict
			if !errors.As(err, &conflict) || conflict.CheckpointID != "parent" {
				t.Fatalf("invalid successor accepted: %v", err)
			}
			if after := snapshotResumeRejectionState(t, server); !reflect.DeepEqual(after, before) {
				t.Fatal("rejected finalization changed records, indexes, or retention")
			}
		})
	}
}
