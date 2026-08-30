package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TaskStoreFixture provides two adapters sharing one logical namespace and a
// third adapter using an isolated namespace.
type TaskStoreFixture struct {
	First    core.TaskStore
	Second   core.TaskStore
	Isolated core.TaskStore
	Cleanup  func()
}

type TaskStoreFactory func(t *testing.T) TaskStoreFixture

// RunTaskStoreConformance verifies persistence, cross-instance visibility,
// namespace isolation, sentinel errors, cancellation, and state transitions.
func RunTaskStoreConformance(t *testing.T, factory TaskStoreFactory) {
	t.Helper()
	t.Run("LifecycleCrossInstanceAndIsolation", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		task := makeTestTask("task-store-lifecycle")
		if err := fixture.First.Create(t.Context(), task); err != nil {
			t.Fatalf("Create: %v", err)
		}
		loaded, err := fixture.Second.Get(t.Context(), task.ID)
		if err != nil || loaded == nil || loaded.ID != task.ID || loaded.TraceID != task.TraceID {
			t.Fatalf("cross-instance Get = %#v, %v", loaded, err)
		}
		if _, err := fixture.Isolated.Get(t.Context(), task.ID); !errors.Is(err, core.ErrTaskNotFound) {
			t.Fatalf("isolated Get error = %v, want ErrTaskNotFound", err)
		}
		loaded.Status = core.TaskStatusRunning
		loaded.Progress = &core.TaskProgress{CurrentStep: 1, TotalSteps: 2, Percentage: 50}
		if err := fixture.Second.Update(t.Context(), loaded); err != nil {
			t.Fatalf("Update: %v", err)
		}
		updated, err := fixture.First.Get(t.Context(), task.ID)
		if err != nil || updated.Status != core.TaskStatusRunning || updated.Progress == nil || updated.Progress.Percentage != 50 {
			t.Fatalf("updated task = %#v, %v", updated, err)
		}
		if err := fixture.First.Delete(t.Context(), task.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := fixture.Second.Get(t.Context(), task.ID); !errors.Is(err, core.ErrTaskNotFound) {
			t.Fatalf("Get after Delete error = %v, want ErrTaskNotFound", err)
		}
	})

	t.Run("DuplicateMissingAndCancelSemantics", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		task := makeTestTask("task-store-errors")
		if err := fixture.First.Create(t.Context(), task); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := fixture.Second.Create(t.Context(), task); !errors.Is(err, core.ErrTaskAlreadyExists) {
			t.Fatalf("duplicate Create error = %v, want ErrTaskAlreadyExists", err)
		}
		missing := makeTestTask("task-store-missing")
		if err := fixture.First.Update(t.Context(), missing); !errors.Is(err, core.ErrTaskNotFound) {
			t.Fatalf("missing Update error = %v, want ErrTaskNotFound", err)
		}
		if err := fixture.Second.Cancel(t.Context(), task.ID); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		cancelled, err := fixture.First.Get(t.Context(), task.ID)
		if err != nil || cancelled.Status != core.TaskStatusCancelled || cancelled.CancelledAt == nil ||
			cancelled.Error == nil || cancelled.Error.Code != core.TaskErrorCodeCancelled {
			t.Fatalf("cancelled task = %#v, %v", cancelled, err)
		}
		if err := fixture.First.Cancel(t.Context(), task.ID); !errors.Is(err, core.ErrTaskNotCancellable) {
			t.Fatalf("second Cancel error = %v, want ErrTaskNotCancellable", err)
		}
		if err := fixture.First.Cancel(t.Context(), "missing"); !errors.Is(err, core.ErrTaskNotFound) {
			t.Fatalf("missing Cancel error = %v, want ErrTaskNotFound", err)
		}
	})

	t.Run("Cancellation", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := fixture.First.Create(ctx, makeTestTask("task-store-cancelled")); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Create error = %v, want context.Canceled", err)
		}
	})
}

// ScheduleStoreFixture provides two adapters sharing one logical namespace and
// a third adapter using an isolated namespace.
type ScheduleStoreFixture struct {
	First    core.ScheduleStore
	Second   core.ScheduleStore
	Isolated core.ScheduleStore
	Cleanup  func()
}

type ScheduleStoreFactory func(t *testing.T) ScheduleStoreFixture

// RunScheduleStoreConformance verifies schedule lifecycle, due selection,
// cross-instance visibility, namespace isolation, and cancellation.
func RunScheduleStoreConformance(t *testing.T, factory ScheduleStoreFactory) {
	t.Helper()
	t.Run("LifecycleCrossInstanceAndIsolation", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		schedule := makeConformanceSchedule("schedule-lifecycle", time.Unix(1_900_000_000, 0))
		if err := fixture.First.Create(t.Context(), schedule); err != nil {
			t.Fatalf("Create: %v", err)
		}
		loaded, err := fixture.Second.Get(t.Context(), schedule.ID)
		if err != nil || loaded == nil || loaded.TargetAgent != schedule.TargetAgent {
			t.Fatalf("cross-instance Get = %#v, %v", loaded, err)
		}
		if _, err := fixture.Isolated.Get(t.Context(), schedule.ID); !errors.Is(err, core.ErrScheduleNotFound) {
			t.Fatalf("isolated Get error = %v, want ErrScheduleNotFound", err)
		}
		if err := fixture.Second.Create(t.Context(), schedule); !errors.Is(err, core.ErrScheduleAlreadyExists) {
			t.Fatalf("duplicate Create error = %v, want ErrScheduleAlreadyExists", err)
		}
		loaded.TargetAgent = "updated-agent"
		loaded.Enabled = false
		if err := fixture.Second.Update(t.Context(), loaded); err != nil {
			t.Fatalf("Update: %v", err)
		}
		updated, err := fixture.First.Get(t.Context(), schedule.ID)
		if err != nil || updated.TargetAgent != "updated-agent" || updated.Enabled {
			t.Fatalf("updated schedule = %#v, %v", updated, err)
		}
		listed, err := fixture.First.List(t.Context())
		if err != nil || len(listed) != 1 || listed[0].ID != schedule.ID {
			t.Fatalf("List = %#v, %v", listed, err)
		}
		isolated, err := fixture.Isolated.List(t.Context())
		if err != nil || len(isolated) != 0 {
			t.Fatalf("isolated List = %#v, %v", isolated, err)
		}
		if err := fixture.First.Delete(t.Context(), schedule.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := fixture.Second.Get(t.Context(), schedule.ID); !errors.Is(err, core.ErrScheduleNotFound) {
			t.Fatalf("Get after Delete error = %v, want ErrScheduleNotFound", err)
		}
		if err := fixture.First.Delete(t.Context(), schedule.ID); !errors.Is(err, core.ErrScheduleNotFound) {
			t.Fatalf("second Delete error = %v, want ErrScheduleNotFound", err)
		}
		if err := fixture.First.Update(t.Context(), makeConformanceSchedule("missing", time.Now())); !errors.Is(err, core.ErrScheduleNotFound) {
			t.Fatalf("missing Update error = %v, want ErrScheduleNotFound", err)
		}
	})

	t.Run("DueSelectionTracksEnabledAndRunAt", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		base := time.Unix(1_900_000_000, 0)
		past := makeConformanceSchedule("due-past", base.Add(-time.Hour))
		exact := makeConformanceSchedule("due-exact", base)
		future := makeConformanceSchedule("due-future", base.Add(time.Hour))
		disabled := makeConformanceSchedule("due-disabled", base.Add(-time.Hour))
		disabled.Enabled = false
		for _, schedule := range []*core.Schedule{past, exact, future, disabled} {
			if err := fixture.First.Create(t.Context(), schedule); err != nil {
				t.Fatalf("Create %s: %v", schedule.ID, err)
			}
		}
		assertScheduleIDs(t, fixture.Second, base, map[string]bool{
			past.ID: true, exact.ID: true,
		})
		future.RunAt = base.Add(-time.Minute)
		if err := fixture.Second.Update(t.Context(), future); err != nil {
			t.Fatalf("move future schedule: %v", err)
		}
		past.Enabled = false
		if err := fixture.Second.Update(t.Context(), past); err != nil {
			t.Fatalf("disable past schedule: %v", err)
		}
		assertScheduleIDs(t, fixture.First, base, map[string]bool{
			exact.ID: true, future.ID: true,
		})
	})

	t.Run("Cancellation", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := fixture.First.Create(ctx, makeConformanceSchedule("schedule-cancelled", time.Now())); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Create error = %v, want context.Canceled", err)
		}
	})
}

// TaskQueueFixture provides two adapters sharing one logical queue and a third
// adapter using an isolated queue.
type TaskQueueFixture struct {
	First    core.TaskQueue
	Second   core.TaskQueue
	Isolated core.TaskQueue
	Cleanup  func()
}

type TaskQueueFactory func(t *testing.T) TaskQueueFixture

// RunTaskQueueConformance verifies the legacy queue contract, including FIFO,
// cross-instance settlement, rejection/requeue behavior, and cancellation.
func RunTaskQueueConformance(t *testing.T, factory TaskQueueFactory) {
	t.Helper()
	t.Run("FIFORoundTripCrossInstance", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		first := makeTestTask("task-queue-first")
		second := makeTestTask("task-queue-second")
		if err := fixture.First.Enqueue(t.Context(), first); err != nil {
			t.Fatalf("Enqueue first: %v", err)
		}
		if err := fixture.First.Enqueue(t.Context(), second); err != nil {
			t.Fatalf("Enqueue second: %v", err)
		}
		for _, want := range []*core.Task{first, second} {
			got, err := fixture.Second.Dequeue(t.Context(), time.Second)
			if err != nil || got == nil || got.ID != want.ID || got.TraceID != want.TraceID || got.TargetAgent != want.TargetAgent {
				t.Fatalf("Dequeue = %#v, %v; want ID %q", got, err, want.ID)
			}
			if err := fixture.First.Acknowledge(t.Context(), got.ID); err != nil {
				t.Fatalf("cross-instance Acknowledge: %v", err)
			}
		}
	})

	t.Run("RejectRequeuesInflightTask", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		task := makeTestTask("task-queue-reject")
		if err := fixture.First.Enqueue(t.Context(), task); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		claimed, err := fixture.Second.Dequeue(t.Context(), time.Second)
		if err != nil || claimed == nil {
			t.Fatalf("Dequeue: %#v, %v", claimed, err)
		}
		if err := fixture.First.Reject(t.Context(), task.ID, "retryable"); err != nil {
			t.Fatalf("cross-instance Reject: %v", err)
		}
		retried, err := fixture.Second.Dequeue(t.Context(), time.Second)
		if err != nil || retried == nil || retried.ID != task.ID {
			t.Fatalf("retry Dequeue = %#v, %v", retried, err)
		}
		if err := fixture.Second.Acknowledge(t.Context(), retried.ID); err != nil {
			t.Fatalf("Acknowledge retry: %v", err)
		}
	})

	t.Run("NamespaceIsolation", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		isolated := makeTestTask("task-queue-isolated")
		if err := fixture.Isolated.Enqueue(t.Context(), isolated); err != nil {
			t.Fatalf("isolated Enqueue: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		got, err := fixture.First.Dequeue(ctx, time.Second)
		if got != nil || (err != nil && !errors.Is(err, context.DeadlineExceeded)) {
			t.Fatalf("shared Dequeue observed isolated task: %#v, %v", got, err)
		}
		got, err = fixture.Isolated.Dequeue(t.Context(), time.Second)
		if err != nil || got == nil || got.ID != isolated.ID {
			t.Fatalf("isolated Dequeue = %#v, %v", got, err)
		}
		if err := fixture.Isolated.Acknowledge(t.Context(), got.ID); err != nil {
			t.Fatalf("isolated Acknowledge: %v", err)
		}
	})

	t.Run("Cancellation", func(t *testing.T) {
		fixture := factory(t)
		registerCleanup(t, fixture.Cleanup)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.First.Dequeue(ctx, time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Dequeue error = %v, want context.Canceled", err)
		}
		if err := fixture.First.Enqueue(ctx, makeTestTask("task-queue-cancelled")); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Enqueue error = %v, want context.Canceled", err)
		}
	})
}

func makeConformanceSchedule(id string, runAt time.Time) *core.Schedule {
	return &core.Schedule{
		ID: id, TargetAgent: "conformance-agent", RunAt: runAt, Enabled: true,
		MissedRunPolicy: core.MissedRunSkip, CreatedBy: "conformance", CreatedAt: runAt.Add(-time.Hour),
		Input: map[string]interface{}{"instruction": "conformance"},
	}
}

func assertScheduleIDs(t *testing.T, store core.ScheduleStore, now time.Time, want map[string]bool) {
	t.Helper()
	due, err := store.GetDue(t.Context(), now)
	if err != nil {
		t.Fatalf("GetDue: %v", err)
	}
	got := make(map[string]bool, len(due))
	for _, schedule := range due {
		if schedule != nil {
			got[schedule.ID] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("due schedule IDs = %#v, want %#v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("due schedule IDs = %#v, missing %q", got, id)
		}
	}
}

func registerCleanup(t *testing.T, cleanup func()) {
	t.Helper()
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
}
