package orchestration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResumeCoordinatorSuccessorFailurePreservesRecovery(t *testing.T) {
	for _, scenario := range []string{"execution failure", "continued finalization failure", "recovered successor finalization failure"} {
		t.Run(scenario, func(t *testing.T) {
			failure := errors.New("injected failure")
			recovery := scenario == "recovered successor finalization failure"
			var child *ExecutionCheckpoint
			var attemptID string
			var executionErr error
			executed := false
			store := &resumeStoreStub{}
			store.claim = func(_ context.Context, request ResumeClaimRequest) (*CheckpointResumeClaim, error) {
				claim := stubResumeClaim(request)
				attemptID = request.AttemptID
				child = &ExecutionCheckpoint{CheckpointID: request.SuccessorCheckpointID, ParentCheckpointID: request.CheckpointID, ParentResumeAttemptID: request.AttemptID, Status: CheckpointStatusPending}
				return claim, nil
			}
			store.load = func(_ context.Context, id string) (*ExecutionCheckpoint, error) {
				if id != child.CheckpointID {
					t.Fatalf("loaded unrelated successor %q", id)
				}
				if !executed && !recovery {
					return nil, &ErrCheckpointNotFound{CheckpointID: id}
				}
				return child, nil
			}
			store.finalize = func(_ context.Context, final ResumeFinalization) error {
				if final.CheckpointID != "parent" || final.AttemptID != attemptID || final.SuccessorCheckpointID != child.CheckpointID || final.Outcome != ResumeFinalizationContinued {
					t.Fatalf("wrong finalization: %+v", final)
				}
				return failure
			}
			actual := &ExecutionResult{Success: true}
			r := newResumeTestCoordinator(t, store, func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error) {
				executed = true
				store.record("execute")
				executionErr = NewInterruptError(child)
				if scenario == "execution failure" {
					executionErr = failure
				}
				return actual, executionErr
			})
			result, err := r.ResumeExecution(t.Context(), "parent")
			var lifecycle *ErrCheckpointResumeLifecycle
			stage := "finalize"
			wantCalls := []string{"claim", "load", "execute", "load", "renew", "finalize:continued"}
			if scenario == "execution failure" {
				stage = "execute"
				wantCalls = []string{"claim", "load", "execute", "load"}
			}
			if recovery {
				actual = nil
				wantCalls = []string{"claim", "load", "renew", "finalize:continued"}
			}
			if result != actual || !errors.Is(err, failure) || !errors.As(err, &lifecycle) || lifecycle.Stage != stage {
				t.Fatalf("result=%+v error=%v; want original result and %s failure", result, err, stage)
			}
			if executionErr != nil && !errors.Is(err, executionErr) {
				t.Fatalf("lost execution cause: %v", err)
			}
			if status, code, _ := HITLErrorResponse(err); status != http.StatusInternalServerError || code != "resume_failed" {
				t.Fatalf("failure became a successful interruption: %d %s", status, code)
			}
			if got := store.operations(); !reflect.DeepEqual(got, wantCalls) {
				t.Fatalf("unexpected replay, release, or finalization: %v, want %v", got, wantCalls)
			}
			if child.Status != CheckpointStatusPending {
				t.Fatal("failed attempt changed the saved successor")
			}
		})
	}
}

func TestResumeWithExecutorIsolatesConcurrentOverrides(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defaultResult := &ExecutionResult{Success: true}
	var defaultCalls atomic.Int32
	r := newResumeTestCoordinator(t, &resumeStoreStub{}, func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error) {
		defaultCalls.Add(1)
		return defaultResult, nil
	})
	checkDefault := func(id string) {
		t.Helper()
		if result, err := r.ResumeExecution(ctx, id); err != nil || result != defaultResult {
			t.Fatalf("default executor replaced: result=%+v error=%v", result, err)
		}
	}
	checkDefault("before-overrides")
	entered := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var workers sync.WaitGroup
	defer func() { unblock(); workers.Wait() }()
	type completion struct {
		id     string
		result *ExecutionResult
		err    error
	}
	completed := make(chan completion, 2)
	expected := map[string]*ExecutionResult{
		"first-request":  {Success: true},
		"second-request": {Success: true},
	}
	for id, want := range expected {
		workers.Go(func() {
			executor := ResumeExecutorFunc(func(callCtx context.Context, checkpoint *ExecutionCheckpoint) (*ExecutionResult, error) {
				entered <- id
				if resumeID, ok := IsResumeMode(callCtx); !ok || resumeID != id || checkpoint.CheckpointID != id {
					return nil, fmt.Errorf("executor %s received another request's context", id)
				}
				select {
				case <-release:
					return want, nil
				case <-callCtx.Done():
					return nil, callCtx.Err()
				}
			})
			result, err := r.ResumeWithExecutor(ctx, id, executor)
			completed <- completion{id, result, err}
		})
	}
	seen := make(map[string]bool)
	for range expected {
		select {
		case id := <-entered:
			if seen[id] {
				t.Fatalf("executor invoked twice: %s", id)
			}
			seen[id] = true
		case <-ctx.Done():
			t.Fatal("request-specific executors did not overlap")
		}
	}
	checkDefault("during-overrides")
	unblock()
	workers.Wait()
	for range expected {
		got := <-completed
		if got.err != nil || got.result != expected[got.id] {
			t.Fatalf("executor result crossed requests: %+v", got)
		}
	}
	checkDefault("after-overrides")
	if got := defaultCalls.Load(); got != 3 {
		t.Fatalf("default executor calls=%d, want 3", got)
	}
}
