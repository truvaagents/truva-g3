package orchestration

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/codes"
)

type resumeStoreStub struct {
	mu       sync.Mutex
	calls    []string
	claim    func(context.Context, ResumeClaimRequest) (*CheckpointResumeClaim, error)
	load     func(context.Context, string) (*ExecutionCheckpoint, error)
	renew    func(context.Context, string, string, time.Duration) error
	release  func(context.Context, string, string) error
	finalize func(context.Context, ResumeFinalization) error
}

func (s *resumeStoreStub) record(operation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, operation)
}
func (s *resumeStoreStub) operations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}
func (s *resumeStoreStub) ClaimCheckpointForResume(ctx context.Context, request ResumeClaimRequest) (*CheckpointResumeClaim, error) {
	s.record("claim")
	if s.claim != nil {
		return s.claim(ctx, request)
	}
	return stubResumeClaim(request), nil
}
func stubResumeClaim(request ResumeClaimRequest) *CheckpointResumeClaim {
	state := &CheckpointResumeState{AttemptID: request.AttemptID, PreviousStatus: CheckpointStatusApproved,
		Owner: request.Owner, LeaseExpiresAt: time.Now().Add(request.Lease), ReservedSuccessorCheckpointID: request.SuccessorCheckpointID, ReservedSuccessorAttemptID: request.AttemptID}
	return &CheckpointResumeClaim{Checkpoint: &ExecutionCheckpoint{CheckpointID: request.CheckpointID,
		RequestID: "previous-request", OriginalRequestID: "root-request", Status: CheckpointStatusApproved,
		ResumeState: state, UserContext: map[string]interface{}{MetadataConversationID: "resume-conversation"}},
		AttemptID: request.AttemptID, PreviousStatus: state.PreviousStatus, LeaseExpiresAt: state.LeaseExpiresAt}
}
func (s *resumeStoreStub) LoadCheckpoint(ctx context.Context, id string) (*ExecutionCheckpoint, error) {
	s.record("load")
	if s.load != nil {
		return s.load(ctx, id)
	}
	return nil, &ErrCheckpointNotFound{CheckpointID: id}
}
func (s *resumeStoreStub) RenewCheckpointResumeClaim(ctx context.Context, id, attempt string, lease time.Duration) error {
	s.record("renew")
	if s.renew != nil {
		return s.renew(ctx, id, attempt, lease)
	}
	return nil
}
func (s *resumeStoreStub) ReleaseCheckpointResumeClaim(ctx context.Context, id, attempt string) error {
	s.record("release")
	if s.release != nil {
		return s.release(ctx, id, attempt)
	}
	return nil
}
func (s *resumeStoreStub) FinalizeCheckpointResume(ctx context.Context, final ResumeFinalization) error {
	s.record("finalize:" + string(final.Outcome))
	if s.finalize != nil {
		return s.finalize(ctx, final)
	}
	return nil
}

func newResumeTestCoordinator(t *testing.T, store CheckpointResumePersistence, execute ResumeExecutorFunc, opts ...ResumeCoordinatorOption) *ResumeCoordinator {
	t.Helper()
	r, err := NewResumeCoordinator(store, execute, DefaultResumeCoordinatorRuntimeConfig(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResumeCoordinatorTerminalResults(t *testing.T) {
	failure := errors.New("application failed")
	for _, test := range []struct {
		name      string
		result    *ExecutionResult
		err       error
		wantCalls []string
	}{
		{"success", &ExecutionResult{Success: true}, nil, []string{"claim", "load", "execute", "load", "renew", "finalize:completed"}},
		{"nil result", nil, nil, []string{"claim", "load", "execute", "load", "release"}},
		{"unsuccessful result", &ExecutionResult{Success: false}, nil, []string{"claim", "load", "execute", "load", "release"}},
		{"partial result and error", &ExecutionResult{Success: true}, failure, []string{"claim", "load", "execute", "load", "release"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &resumeStoreStub{}
			r := newResumeTestCoordinator(t, store, func(ctx context.Context, checkpoint *ExecutionCheckpoint) (*ExecutionResult, error) {
				store.record("execute")
				if id, ok := IsResumeMode(ctx); !ok || id != checkpoint.CheckpointID {
					t.Fatal("executor did not receive restored context")
				}
				if core.GetConversationID(ctx) != "resume-conversation" || telemetry.GetBaggage(ctx)["original_request_id"] != "root-request" {
					t.Fatal("canonical correlation missing")
				}
				return test.result, test.err
			})
			result, err := r.ResumeExecution(t.Context(), "parent")
			if result != test.result {
				t.Fatal("coordinator replaced the application result")
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("lost error: %v", err)
			}
			if (err == nil) != (test.name == "success") {
				t.Fatalf("outcome error = %v", err)
			}
			if got := store.operations(); !reflect.DeepEqual(got, test.wantCalls) {
				t.Fatalf("operations = %v", got)
			}
		})
	}
}

func TestResumeCoordinatorClaimRejectionDoesNotExecute(t *testing.T) {
	for _, cause := range []error{&ErrCheckpointResumeInProgress{CheckpointID: "parent"}, &ErrCheckpointNotFound{CheckpointID: "parent"}, &ErrCheckpointNotResumable{CheckpointID: "parent", Status: CheckpointStatusPending}, errors.New("backend unavailable"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			store := &resumeStoreStub{claim: func(context.Context, ResumeClaimRequest) (*CheckpointResumeClaim, error) { return nil, cause }}
			r := newResumeTestCoordinator(t, store, func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error) {
				t.Fatal("rejected call executed")
				return nil, nil
			})
			result, err := r.ResumeExecution(t.Context(), "parent")
			if result != nil || !errors.Is(err, cause) || !reflect.DeepEqual(store.operations(), []string{"claim"}) {
				t.Fatalf("rejection = %v/%v calls=%v", result, err, store.operations())
			}
		})
	}
}

func TestResumeCoordinatorCleanupFailureKeepsBothCauses(t *testing.T) {
	executionErr, releaseErr := errors.New("execution cause"), errors.New("release cause")
	store := &resumeStoreStub{release: func(context.Context, string, string) error { return releaseErr }}
	r := newResumeTestCoordinator(t, store, func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error) { return nil, executionErr })
	_, err := r.ResumeExecution(t.Context(), "parent")
	var lifecycle *ErrCheckpointResumeLifecycle
	if !errors.Is(err, executionErr) || !errors.Is(err, releaseErr) || !errors.As(err, &lifecycle) || lifecycle.Stage != "release" {
		t.Fatalf("lost cause/stage: %v", err)
	}
}

func TestResumeCoordinatorRestorationFailureReleasesOwnedClaim(t *testing.T) {
	store := &resumeStoreStub{claim: func(_ context.Context, request ResumeClaimRequest) (*CheckpointResumeClaim, error) {
		claim := stubResumeClaim(request)
		claim.Checkpoint.SkillCacheContext = &SkillCacheContext{}
		return claim, nil
	}}
	r := newResumeTestCoordinator(t, store, func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error) {
		t.Fatal("invalid skill state executed")
		return nil, nil
	})
	_, err := r.ResumeExecution(t.Context(), "parent")
	if !errors.Is(err, ErrSkillIntegrity) || !reflect.DeepEqual(store.operations(), []string{"claim", "release"}) {
		t.Fatalf("restoration = %v calls=%v", err, store.operations())
	}
}

func TestResumeCoordinatorFinalizationAndDetachedCleanup(t *testing.T) {
	for _, failureAt := range []string{"none", "renew", "finalize"} {
		t.Run(failureAt, func(t *testing.T) {
			ctx, disconnect := context.WithCancel(t.Context())
			defer disconnect()
			failure := errors.New("persistence unavailable")
			checkCleanup := func(ctx context.Context) {
				if ctx.Err() != nil {
					t.Fatal("post-execution cleanup inherited cancellation")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("cleanup is not bounded")
				}
				if telemetry.GetBaggage(ctx)["request_id"] != "new-execution" {
					t.Fatal("cleanup lost the actual execution ID")
				}
			}
			store := &resumeStoreStub{
				renew: func(ctx context.Context, _, _ string, _ time.Duration) error {
					checkCleanup(ctx)
					if failureAt == "renew" {
						return failure
					}
					return nil
				},
				finalize: func(ctx context.Context, final ResumeFinalization) error {
					checkCleanup(ctx)
					if final.Outcome != ResumeFinalizationCompleted {
						t.Fatal("wrong outcome")
					}
					if failureAt == "finalize" {
						return failure
					}
					return nil
				},
			}
			r := newResumeTestCoordinator(t, store, func(ctx context.Context, _ *ExecutionCheckpoint) (*ExecutionResult, error) {
				reportResumeRequestID(ctx, "new-execution")
				disconnect()
				return &ExecutionResult{Success: true}, nil
			})
			_, err := r.ResumeExecution(ctx, "parent")
			if failureAt == "none" && err != nil {
				t.Fatal(err)
			}
			if failureAt != "none" && !errors.Is(err, failure) {
				t.Fatalf("persistence failure hidden: %v", err)
			}
			for _, call := range store.operations() {
				if call == "release" {
					t.Fatal("ambiguous successful work was released for immediate replay")
				}
			}
		})
	}
}

func TestResumeCoordinatorSuccessorRecovery(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		for _, state := range []string{"valid", "preparing", "wrong parent", "wrong attempt", "wrong reported ID", "missing reported child"} {
			t.Run(state+map[bool]string{true: "/recovery", false: "/execution"}[recovery], func(t *testing.T) {
				var claim *CheckpointResumeClaim
				var child *ExecutionCheckpoint
				executed := 0
				store := &resumeStoreStub{claim: func(_ context.Context, request ResumeClaimRequest) (*CheckpointResumeClaim, error) {
					claim = stubResumeClaim(request)
					if recovery {
						claim.Checkpoint.ResumeState.ReservedSuccessorAttemptID = "older-attempt"
					}
					child = &ExecutionCheckpoint{CheckpointID: claim.Checkpoint.ResumeState.ReservedSuccessorCheckpointID, ParentCheckpointID: request.CheckpointID, ParentResumeAttemptID: claim.Checkpoint.ResumeState.ReservedSuccessorAttemptID, Status: CheckpointStatusPending}
					switch state {
					case "preparing":
						child.Status = CheckpointStatusPreparing
					case "wrong parent":
						child.ParentCheckpointID = "unrelated"
					case "wrong attempt":
						child.ParentResumeAttemptID = "unrelated"
					}
					return claim, nil
				}}
				store.load = func(_ context.Context, id string) (*ExecutionCheckpoint, error) {
					if (!recovery && executed == 0) || state == "missing reported child" {
						return nil, &ErrCheckpointNotFound{CheckpointID: id}
					}
					return child, nil
				}
				store.finalize = func(_ context.Context, final ResumeFinalization) error {
					if final.Outcome != ResumeFinalizationContinued || final.SuccessorCheckpointID != child.CheckpointID {
						t.Fatal("wrong successor finalized")
					}
					return nil
				}
				r := newResumeTestCoordinator(t, store, func(_ context.Context, _ *ExecutionCheckpoint) (*ExecutionResult, error) {
					executed++
					if state == "wrong reported ID" {
						return nil, &ErrInterrupted{CheckpointID: "unrelated"}
					}
					return nil, NewInterruptError(child)
				})
				_, err := r.ResumeExecution(t.Context(), "parent")
				wantSuccess := state == "valid" || (recovery && state == "wrong reported ID")
				var lifecycle *ErrCheckpointResumeLifecycle
				if wantSuccess {
					if !IsInterrupted(err) || errors.As(err, &lifecycle) || GetCheckpoint(err) != child {
						t.Fatalf("continuation = %v", err)
					}
				} else if err == nil || !errors.As(err, &lifecycle) {
					t.Fatalf("invalid successor accepted: %v", err)
				}
				if recovery && state != "missing reported child" && executed != 0 {
					t.Fatal("recovery replayed work despite durable successor")
				}
				for _, call := range store.operations() {
					if call == "release" {
						t.Fatal("successor evidence was released")
					}
				}
			})
		}
	}
}

func TestResumeCoordinatorPeriodicClaimLossCancelsAndJoins(t *testing.T) {
	lost := &ErrCheckpointResumeClaimLost{CheckpointID: "parent"}
	store := &resumeStoreStub{renew: func(context.Context, string, string, time.Duration) error { return lost }}
	executed := make(chan struct{})
	r, err := NewResumeCoordinator(store, ResumeExecutorFunc(func(ctx context.Context, _ *ExecutionCheckpoint) (*ExecutionResult, error) {
		close(executed)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(4 * time.Second):
			return nil, errors.New("renewal did not cancel execution")
		}
	}), ResumeCoordinatorRuntimeConfig{ClaimLease: 3 * time.Second, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.ResumeExecution(t.Context(), "parent")
	if !errors.Is(err, lost) || !errors.Is(err, context.Canceled) {
		t.Fatalf("lost ownership error = %v", err)
	}
	select {
	case <-executed:
	default:
		t.Fatal("executor not invoked")
	}
	if got := store.operations(); !reflect.DeepEqual(got, []string{"claim", "load", "renew"}) {
		t.Fatalf("mutated after ownership loss: %v", got)
	}
}

func TestResumeCoordinatorPanicPreservesIdentityAndClosesSpan(t *testing.T) {
	recorder := setupHITLResumeTestTracer(t)
	store := &resumeStoreStub{}
	panicValue := &struct{ value string }{"original panic"}
	r := newResumeTestCoordinator(t, store, func(ctx context.Context, _ *ExecutionCheckpoint) (*ExecutionResult, error) {
		reportResumeRequestID(ctx, "panic-execution")
		panic(panicValue)
	})
	func() {
		defer func() {
			if got := recover(); got != panicValue {
				t.Fatalf("panic changed: %v", got)
			}
		}()
		_, _ = r.ResumeExecution(t.Context(), "parent")
		t.Fatal("panic was swallowed")
	}()
	if got := store.operations(); !reflect.DeepEqual(got, []string{"claim", "load"}) {
		t.Fatalf("panic changed persistence: %v", got)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "hitl.resume" || spans[0].Status().Code != codes.Error {
		t.Fatalf("panic span not closed/failed: %v", spans)
	}
	terminal := 0
	for _, event := range spans[0].Events() {
		if event.Name == "hitl.resume.failed" {
			terminal++
			if len(event.Attributes) == 0 || string(event.Attributes[0].Key) != "request_id" || event.Attributes[0].Value.AsString() != "panic-execution" {
				t.Fatalf("terminal request identity lost: %v", event.Attributes)
			}
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events = %d", terminal)
	}
}
