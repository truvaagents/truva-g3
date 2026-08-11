// Package conformance provides contract test suites for core interfaces.
//
// This package is the SINGLE SOURCE OF TRUTH for "what does it mean to
// correctly implement core.TaskConsumer." If your backend passes all
// sub-tests, the framework considers it compliant.
//
// Location rationale: lives in core/conformance/ because the interface
// under test (core.TaskConsumer + core.TaskHandle) lives in core/.
// The sub-package is separately importable so core/ proper doesn't
// inherit any test-only dependencies.
//
// Dependencies: only stdlib + github.com/truvaagents/truva-g3/core.
// No testify. No mocks. No orchestration types. Adheres to
// core/ARCHITECTURE.md §2 Zero Framework Dependencies.
package conformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// QueueName is the fixed queue used by all conformance tests. Test factories
// should use this constant when constructing consumers that need a queue name
// at construction time (e.g., RedisTaskConsumer).
const QueueName = "conformance-test-queue"

const testTimeout = 5 * time.Second

// TaskConsumerFactory produces a TaskConsumer paired with its matching
// TaskDispatcher (they must point at the same logical queue), plus a
// cleanup function. Called once per sub-test for isolation.
type TaskConsumerFactory func(t *testing.T) (
	consumer core.TaskConsumer,
	dispatcher core.TaskDispatcher,
	cleanup func(),
)

// TaskDeliveryProfile makes transport delivery semantics explicit. The base
// TaskConsumer contract deliberately supports both profiles; providers opt in
// to the additional assertions that match their advertised implementation.
type TaskDeliveryProfile string

const (
	TaskDeliveryAtMostOnce  TaskDeliveryProfile = "at_most_once"
	TaskDeliveryAtLeastOnce TaskDeliveryProfile = "at_least_once"
)

// TaskDeliveryFixture supplies neutral observation seams for behavior that is
// not visible through core.TaskConsumer alone. Provider test packages retain
// ownership of transport inspection and abandoned-claim recovery.
type TaskDeliveryFixture struct {
	Consumer   core.TaskConsumer
	Dispatcher core.TaskDispatcher
	Cleanup    func()

	DeadLetterContains func(
		ctx context.Context,
		queueName string,
		taskID string,
		reason string,
	) (bool, error)
	RecoverAbandoned func(
		ctx context.Context,
		queueName string,
		abandoned core.TaskHandle,
	) (core.TaskHandle, error)
}

type TaskDeliveryFactory func(t *testing.T) TaskDeliveryFixture

// RunTaskConsumerConformance runs the full contract test suite.
func RunTaskConsumerConformance(t *testing.T, factory TaskConsumerFactory) {
	t.Helper()
	t.Run("DispatchConsumeRoundtrip", func(t *testing.T) { testDispatchConsumeRoundtrip(t, factory) })
	t.Run("AckRemovesFromQueue", func(t *testing.T) { testAckRemovesFromQueue(t, factory) })
	t.Run("NackWithTerminalReason", func(t *testing.T) { testNackWithTerminalReason(t, factory) })
	t.Run("DeterministicIDIdempotency", func(t *testing.T) { testDeterministicIDIdempotency(t, factory) })
	t.Run("CtxCancellationReturnsGracefully", func(t *testing.T) { testCtxCancellation(t, factory) })
	t.Run("ConcurrentConsumersShareLoad", func(t *testing.T) { testConcurrentConsumers(t, factory) })
	t.Run("TaskFieldsRoundtripLossless", func(t *testing.T) { testFieldsRoundtrip(t, factory) })
	t.Run("HandleAckIsIdempotent", func(t *testing.T) { testAckIdempotent(t, factory) })
	t.Run("HandleTaskAccessorStable", func(t *testing.T) { testTaskAccessorStable(t, factory) })
	t.Run("NackThenAckReturnsError", func(t *testing.T) { testNackThenAck(t, factory) })
}

// RunTaskDeliveryProfileConformance runs the universal TaskConsumer contract
// and the assertions for one declared delivery profile. A profile fixture is
// recreated for every sub-test so state and cleanup remain deterministic.
func RunTaskDeliveryProfileConformance(
	t *testing.T,
	profile TaskDeliveryProfile,
	factory TaskDeliveryFactory,
) {
	t.Helper()
	if profile != TaskDeliveryAtMostOnce && profile != TaskDeliveryAtLeastOnce {
		t.Fatalf("unknown task delivery profile %q", profile)
	}
	RunTaskConsumerConformance(t, func(t *testing.T) (core.TaskConsumer, core.TaskDispatcher, func()) {
		fixture := factory(t)
		return fixture.Consumer, fixture.Dispatcher, fixture.Cleanup
	})

	t.Run("TerminalNackPersistsDeadLetter", func(t *testing.T) {
		fixture := factory(t)
		if fixture.Cleanup != nil {
			t.Cleanup(fixture.Cleanup)
		}
		if fixture.DeadLetterContains == nil {
			t.Fatal("delivery profile requires DeadLetterContains")
		}
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		const reason = "terminal_conformance_failure"
		task := makeTestTask("profile-dead-letter")
		if err := fixture.Dispatcher.Dispatch(ctx, QueueName, task); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		handle, err := fixture.Consumer.Consume(ctx, QueueName)
		if err != nil || handle == nil {
			t.Fatalf("Consume: handle=%v err=%v", handle, err)
		}
		if err := handle.Nack(ctx, reason); err != nil {
			t.Fatalf("Nack: %v", err)
		}
		found, err := fixture.DeadLetterContains(ctx, QueueName, task.ID, reason)
		if err != nil {
			t.Fatalf("inspect dead letter: %v", err)
		}
		if !found {
			t.Fatal("terminal Nack was not persisted in the dead-letter destination")
		}
	})

	if profile == TaskDeliveryAtLeastOnce {
		t.Run("AbandonedClaimRecoversAcrossConsumer", func(t *testing.T) {
			fixture := factory(t)
			if fixture.Cleanup != nil {
				t.Cleanup(fixture.Cleanup)
			}
			if fixture.RecoverAbandoned == nil {
				t.Fatal("at-least-once profile requires RecoverAbandoned")
			}
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			task := makeTestTask("profile-abandoned")
			if err := fixture.Dispatcher.Dispatch(ctx, QueueName, task); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			abandoned, err := fixture.Consumer.Consume(ctx, QueueName)
			if err != nil || abandoned == nil {
				t.Fatalf("initial Consume: handle=%v err=%v", abandoned, err)
			}
			recovered, err := fixture.RecoverAbandoned(ctx, QueueName, abandoned)
			if err != nil || recovered == nil {
				t.Fatalf("recover abandoned claim: handle=%v err=%v", recovered, err)
			}
			if recovered.Task() == nil || recovered.Task().ID != task.ID {
				t.Fatalf("recovered task = %#v, want ID %q", recovered.Task(), task.ID)
			}
			if err := recovered.Ack(ctx); err != nil {
				t.Fatalf("Ack recovered task: %v", err)
			}
		})
	}
}

func makeTestTask(id string) *core.Task {
	return &core.Task{
		ID:           id,
		Type:         core.ScheduledTaskType,
		Status:       core.TaskStatusQueued,
		TargetAgent:  "conformance-target-agent",
		ScheduleID:   "sch-conformance-" + id,
		TraceID:      "trace-" + id,
		ParentSpanID: "span-" + id,
		Input:        map[string]interface{}{"instruction": "conformance test " + id},
		CreatedAt:    time.Now(),
	}
}

// 1. DispatchConsumeRoundtrip
func testDispatchConsumeRoundtrip(t *testing.T, factory TaskConsumerFactory) {
	consumer, dispatcher, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	original := makeTestTask("roundtrip-1")
	if err := dispatcher.Dispatch(ctx, QueueName, original); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	handle, err := consumer.Consume(ctx, QueueName)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}
	if handle == nil {
		t.Fatal("Consume returned nil handle — expected the dispatched task")
	}
	defer func() {
		if err := handle.Ack(ctx); err != nil {
			t.Errorf("Ack failed during cleanup: %v", err)
		}
	}()

	got := handle.Task()
	if got.ID != original.ID {
		t.Errorf("ID: got %q, want %q", got.ID, original.ID)
	}
	if got.Type != original.Type {
		t.Errorf("Type: got %q, want %q", got.Type, original.Type)
	}
	if got.TargetAgent != original.TargetAgent {
		t.Errorf("TargetAgent: got %q, want %q", got.TargetAgent, original.TargetAgent)
	}
	if got.ScheduleID != original.ScheduleID {
		t.Errorf("ScheduleID: got %q, want %q", got.ScheduleID, original.ScheduleID)
	}
	if got.TraceID != original.TraceID {
		t.Errorf("TraceID: got %q, want %q", got.TraceID, original.TraceID)
	}
	if got.ParentSpanID != original.ParentSpanID {
		t.Errorf("ParentSpanID: got %q, want %q", got.ParentSpanID, original.ParentSpanID)
	}
}

// 2. AckRemovesFromQueue
func testAckRemovesFromQueue(t *testing.T, factory TaskConsumerFactory) {
	consumer, dispatcher, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	task := makeTestTask("ack-remove-1")
	if err := dispatcher.Dispatch(ctx, QueueName, task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	handle, err := consumer.Consume(ctx, QueueName)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if handle == nil {
		t.Fatal("nil handle")
	}
	if err := handle.Ack(ctx); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	shortCtx, shortCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer shortCancel()
	handle2, _ := consumer.Consume(shortCtx, QueueName)
	if handle2 != nil {
		t.Fatalf("Consume after Ack returned a handle (ID=%s) — task should be settled", handle2.Task().ID)
	}
}

// 3. NackWithTerminalReason
func testNackWithTerminalReason(t *testing.T, factory TaskConsumerFactory) {
	consumer, dispatcher, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	task := makeTestTask("nack-terminal-1")
	if err := dispatcher.Dispatch(ctx, QueueName, task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	handle, err := consumer.Consume(ctx, QueueName)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if handle == nil {
		t.Fatal("nil handle")
	}
	if err := handle.Nack(ctx, "max_retries_exhausted"); err != nil {
		t.Fatalf("Nack: %v", err)
	}

	shortCtx, shortCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer shortCancel()
	handle2, _ := consumer.Consume(shortCtx, QueueName)
	if handle2 != nil {
		t.Fatalf("Consume after Nack returned handle (ID=%s) — task should be dead-lettered", handle2.Task().ID)
	}
}

// 4. DeterministicIDIdempotency
func testDeterministicIDIdempotency(t *testing.T, factory TaskConsumerFactory) {
	_, dispatcher, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	task := makeTestTask("dedup-1")
	if err := dispatcher.Dispatch(ctx, QueueName, task); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}

	err := dispatcher.Dispatch(ctx, QueueName, task)
	if !errors.Is(err, core.ErrTaskAlreadyExists) {
		t.Fatalf("second Dispatch: got %v, want core.ErrTaskAlreadyExists", err)
	}
}

// 5. CtxCancellationReturnsGracefully
func testCtxCancellation(t *testing.T, factory TaskConsumerFactory) {
	consumer, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	var handle core.TaskHandle
	go func() {
		handle, _ = consumer.Consume(ctx, QueueName)
		close(done)
	}()

	select {
	case <-done:
		if handle != nil {
			t.Fatal("Consume on empty queue returned a handle")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Consume did not return within 2s after ctx cancellation — backend ignores context")
	}
}

// 6. ConcurrentConsumersShareLoad
func testConcurrentConsumers(t *testing.T, factory TaskConsumerFactory) {
	consumer, dispatcher, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	const taskCount = 20
	const workerCount = 4

	for i := 0; i < taskCount; i++ {
		task := makeTestTask(fmt.Sprintf("concurrent-%d", i))
		if err := dispatcher.Dispatch(ctx, QueueName, task); err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
	}

	var mu sync.Mutex
	seen := make(map[string]bool)
	var wg sync.WaitGroup

	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				handle, err := consumer.Consume(ctx, QueueName)
				if err != nil || handle == nil {
					return
				}
				task := handle.Task()
				if err := handle.Ack(ctx); err != nil {
					t.Errorf("Ack failed: %v", err)
					return
				}

				mu.Lock()
				if seen[task.ID] {
					t.Errorf("duplicate task consumed: %s", task.ID)
				}
				seen[task.ID] = true
				done := len(seen) >= taskCount
				mu.Unlock()

				if done {
					return
				}
			}
		}()
	}

	wg.Wait()
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != taskCount {
		t.Errorf("consumed %d tasks, want %d", len(seen), taskCount)
	}
}

// 7. TaskFieldsRoundtripLossless
func testFieldsRoundtrip(t *testing.T, factory TaskConsumerFactory) {
	consumer, dispatcher, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	original := makeTestTask("fields-1")
	original.Input["nested"] = map[string]interface{}{"key": "value"}

	if err := dispatcher.Dispatch(ctx, QueueName, original); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	handle, err := consumer.Consume(ctx, QueueName)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if handle == nil {
		t.Fatal("nil handle")
	}
	defer func() {
		if err := handle.Ack(ctx); err != nil {
			t.Errorf("Ack failed during cleanup: %v", err)
		}
	}()

	got := handle.Task()
	checks := []struct{ name, got, want string }{
		{"ID", got.ID, original.ID},
		{"Type", got.Type, original.Type},
		{"TargetAgent", got.TargetAgent, original.TargetAgent},
		{"ScheduleID", got.ScheduleID, original.ScheduleID},
		{"TraceID", got.TraceID, original.TraceID},
		{"ParentSpanID", got.ParentSpanID, original.ParentSpanID},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
	if !reflect.DeepEqual(got.Input, original.Input) {
		t.Errorf("Input mismatch:\n  got:  %v\n  want: %v", got.Input, original.Input)
	}
}

// 8. HandleAckIsIdempotent
func testAckIdempotent(t *testing.T, factory TaskConsumerFactory) {
	consumer, dispatcher, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	task := makeTestTask("ack-idempotent-1")
	if err := dispatcher.Dispatch(ctx, QueueName, task); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	handle, err := consumer.Consume(ctx, QueueName)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}
	if handle == nil {
		t.Fatal("nil handle")
	}

	if err := handle.Ack(ctx); err != nil {
		t.Fatalf("first Ack: %v", err)
	}
	// Second Ack — must not panic (may return nil or error)
	_ = handle.Ack(ctx)
}

// 9. HandleTaskAccessorStable
func testTaskAccessorStable(t *testing.T, factory TaskConsumerFactory) {
	consumer, dispatcher, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	task := makeTestTask("accessor-stable-1")
	if err := dispatcher.Dispatch(ctx, QueueName, task); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	handle, err := consumer.Consume(ctx, QueueName)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}
	if handle == nil {
		t.Fatal("nil handle")
	}
	defer func() {
		if err := handle.Ack(ctx); err != nil {
			t.Errorf("Ack failed during cleanup: %v", err)
		}
	}()

	ptr1 := handle.Task()
	ptr2 := handle.Task()
	if ptr1 != ptr2 {
		t.Error("Task() returned different pointers on consecutive calls")
	}
}

// 10. NackThenAckReturnsError
func testNackThenAck(t *testing.T, factory TaskConsumerFactory) {
	consumer, dispatcher, cleanup := factory(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	task := makeTestTask("nack-then-ack-1")
	if err := dispatcher.Dispatch(ctx, QueueName, task); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	handle, err := consumer.Consume(ctx, QueueName)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}
	if handle == nil {
		t.Fatal("nil handle")
	}

	if err := handle.Nack(ctx, "test_reason"); err != nil {
		t.Fatalf("Nack: %v", err)
	}
	err = handle.Ack(ctx)
	if err == nil {
		t.Error("Ack after Nack returned nil — expected an error indicating the handle is already settled")
	}
}
