// Package orchestration — unit tests for RedisTaskStore.
//
// The primary purpose of this test file is to lock in the Phase 1a-bis
// surgical fix: RedisTaskStore.Create must return a wrapped
// core.ErrTaskAlreadyExists sentinel on duplicate, so the Scheduler can
// use errors.Is() to detect the dedup case idempotently.
//
// Additional tests cover the basic CRUD paths via miniredis so the store's
// behaviour is unit-tested without requiring a real Redis instance.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

type taskIndexPipelineFailureHook struct {
	enabled bool
}

func (*taskIndexPipelineFailureHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (*taskIndexPipelineFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}

func (hook *taskIndexPipelineFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		if hook.enabled {
			for _, command := range commands {
				if command.Name() == "sadd" {
					return errors.New("injected task index failure")
				}
			}
		}
		return next(ctx, commands)
	}
}

type taskScanPage struct {
	members []string
	cursor  uint64
}

type scriptedTaskSScanHook struct {
	pages []taskScanPage
	next  int
}

func (*scriptedTaskSScanHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *scriptedTaskSScanHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() != "sscan" || hook.next >= len(hook.pages) {
			return next(ctx, command)
		}
		page := hook.pages[hook.next]
		hook.next++
		scan, ok := command.(*redis.ScanCmd)
		if !ok {
			return fmt.Errorf("SSCAN command has type %T", command)
		}
		scan.SetVal(page.members, page.cursor)
		return nil
	}
}

func (*scriptedTaskSScanHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// setupTaskStoreTestRedis creates a miniredis instance + Redis client for
// RedisTaskStore tests. Mirrors the pattern used in hitl_checkpoint_store_test.go.
func setupTaskStoreTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err, "Failed to start miniredis")

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})

	return mr, client
}

// ═══════════════════════════════════════════════════════════════════════════
// Phase 1a-bis — surgical fix verification
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisTaskStore_Create_DuplicateReturnsWrappedSentinel(t *testing.T) {
	// This test is the contractual verification for the Phase 1a-bis
	// surgical fix. The Scheduler's idempotent fireOnce path relies on
	// errors.Is(err, core.ErrTaskAlreadyExists) returning true — if this
	// test breaks, Scheduler idempotency silently breaks.

	_, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)
	ctx := context.Background()

	task := &core.Task{
		ID:        "test-task-duplicate",
		Type:      "test",
		Status:    core.TaskStatusQueued,
		Input:     map[string]interface{}{"k": "v"},
		CreatedAt: time.Now(),
	}

	// First Create should succeed.
	err := store.Create(ctx, task)
	require.NoError(t, err, "first create should succeed")

	// Second Create with the same ID should return a wrapped sentinel.
	err = store.Create(ctx, task)
	require.Error(t, err, "duplicate create should return an error")
	assert.True(t, errors.Is(err, core.ErrTaskAlreadyExists),
		"duplicate create error must satisfy errors.Is(err, core.ErrTaskAlreadyExists) — this is the Scheduler idempotency contract")

	// Error message format is preserved for backwards compatibility with
	// any existing log scrapers that match on the string "task already exists: <id>".
	assert.Contains(t, err.Error(), "task already exists")
	assert.Contains(t, err.Error(), "test-task-duplicate")
}

// ═══════════════════════════════════════════════════════════════════════════
// Create — other error paths
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisTaskStore_Create_NilTask(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)

	err := store.Create(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task cannot be nil")
	// Nil task is a caller bug, not a dedup situation — must NOT wrap the
	// sentinel or the Scheduler would misinterpret it as a duplicate.
	assert.False(t, errors.Is(err, core.ErrTaskAlreadyExists))
}

func TestRedisTaskStore_Create_EmptyID(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)

	err := store.Create(context.Background(), &core.Task{
		Type:   "test",
		Status: core.TaskStatusQueued,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task ID cannot be empty")
	assert.False(t, errors.Is(err, core.ErrTaskAlreadyExists))
}

// ═══════════════════════════════════════════════════════════════════════════
// Get / Update / Delete / Cancel — basic happy paths via miniredis
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisTaskStore_CreateGetRoundTrip(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)
	ctx := context.Background()

	task := &core.Task{
		ID:         "roundtrip-1",
		Type:       "roundtrip-test",
		Status:     core.TaskStatusQueued,
		Input:      map[string]interface{}{"hello": "world"},
		CreatedAt:  time.Now(),
		ScheduleID: "sch-parent",
	}
	require.NoError(t, store.Create(ctx, task))

	got, err := store.Get(ctx, "roundtrip-1")
	require.NoError(t, err)
	assert.Equal(t, "roundtrip-1", got.ID)
	assert.Equal(t, "roundtrip-test", got.Type)
	assert.Equal(t, "world", got.Input["hello"])
	assert.Equal(t, "sch-parent", got.ScheduleID,
		"ScheduleID (Phase 1b field) should round-trip through Redis")
}

func TestRedisTaskStore_Get_NotFound(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)

	_, err := store.Get(context.Background(), "does-not-exist")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTaskNotFound)
}

func TestRedisTaskStoreMaintainsAndRepairsStatusIndexes(t *testing.T) {
	mr, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)
	ctx := context.Background()
	task := &core.Task{ID: "indexed", Type: "test", Status: core.TaskStatusQueued, CreatedAt: time.Now()}
	require.NoError(t, store.Create(ctx, task))
	assert.True(t, client.SIsMember(ctx, store.allKey(), task.ID).Val())
	assert.True(t, client.SIsMember(ctx, store.statusKey(core.TaskStatusQueued), task.ID).Val())

	task.Status = core.TaskStatusRunning
	require.NoError(t, store.Update(ctx, task))
	assert.False(t, client.SIsMember(ctx, store.statusKey(core.TaskStatusQueued), task.ID).Val())
	assert.True(t, client.SIsMember(ctx, store.statusKey(core.TaskStatusRunning), task.ID).Val())

	require.NoError(t, client.SAdd(ctx, store.statusKey(core.TaskStatusRunning), "stale").Err())
	running, err := store.ListByStatus(ctx, core.TaskStatusRunning)
	require.NoError(t, err)
	require.Len(t, running, 1)
	assert.Equal(t, task.ID, running[0].ID)
	assert.False(t, client.SIsMember(ctx, store.statusKey(core.TaskStatusRunning), "stale").Val())

	require.NoError(t, client.SRem(ctx, store.statusKey(core.TaskStatusRunning), task.ID).Err())
	require.NoError(t, store.ReconcileTaskIndexes(ctx, 100))
	assert.True(t, client.SIsMember(ctx, store.statusKey(core.TaskStatusRunning), task.ID).Val())
	require.NoError(t, store.Delete(ctx, task.ID))
	assert.False(t, client.SIsMember(ctx, store.allKey(), task.ID).Val())
	assert.False(t, client.SIsMember(ctx, store.statusKey(core.TaskStatusRunning), task.ID).Val())
	assert.False(t, mr.Exists(store.taskKey(task.ID)))
}

func TestTaskIndexReconcilerFindsRecordsMissingFromEveryProjection(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	hook := &taskIndexPipelineFailureHook{enabled: true}
	client.AddHook(hook)
	store := NewRedisTaskStore(client, nil)
	task := &core.Task{ID: "record-only", Type: "test", Status: core.TaskStatusQueued, CreatedAt: time.Now()}

	err := store.Create(t.Context(), task)
	var indexErr *TaskIndexUpdateError
	if !errors.As(err, &indexErr) {
		t.Fatalf("Create error = %v, want TaskIndexUpdateError", err)
	}
	if client.Exists(t.Context(), store.taskKey(task.ID)).Val() != 1 {
		t.Fatal("authoritative task record was not retained after index failure")
	}
	hook.enabled = false
	if err := store.ReconcileTaskIndexes(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if !client.SIsMember(t.Context(), store.allKey(), task.ID).Val() ||
		!client.SIsMember(t.Context(), store.statusKey(task.Status), task.ID).Val() {
		t.Fatal("maintenance record scan did not reconstruct missing projections")
	}
}

func TestTaskIndexReconcilerStrictlyBoundsOversizedScanPages(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)
	scanCalls := 0
	source := taskReconcileSource{
		name: "oversized",
		scan: func(context.Context, uint64, int64) ([]string, uint64, error) {
			scanCalls++
			return []string{"one", "two", "three"}, 0, nil
		},
	}
	for _, wanted := range []string{"one", "two", "three"} {
		batch, err := store.nextTaskReconcileBatch(t.Context(), source, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) != 1 || batch[0] != wanted {
			t.Fatalf("bounded batch = %v, want [%s]", batch, wanted)
		}
	}
	if scanCalls != 1 {
		t.Fatalf("oversized page was rescanned %d times, want 1", scanCalls)
	}
}

func TestTaskIndexReconcilerRotatesAcrossSources(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)
	allTask := &core.Task{ID: "all-source", Type: "test", Status: core.TaskStatusRunning, CreatedAt: time.Now()}
	statusTask := &core.Task{ID: "status-source", Type: "test", Status: core.TaskStatusQueued, CreatedAt: time.Now()}
	require.NoError(t, store.Create(t.Context(), allTask))
	require.NoError(t, store.Create(t.Context(), statusTask))
	require.NoError(t, client.SRem(t.Context(), store.allKey(), statusTask.ID).Err())

	require.NoError(t, store.ReconcileTaskIndexes(t.Context(), 1))
	if client.SIsMember(t.Context(), store.allKey(), statusTask.ID).Val() {
		t.Fatal("first bounded pass unexpectedly reached the status source")
	}
	require.NoError(t, store.ReconcileTaskIndexes(t.Context(), 1))
	if !client.SIsMember(t.Context(), store.allKey(), statusTask.ID).Val() {
		t.Fatal("large all-task source starved the next status source")
	}
}

func TestTaskStatusScanHandlesCursorEdgeCasesAndPrunesStaleMembers(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)
	live := &core.Task{ID: "live", Type: "test", Status: core.TaskStatusQueued, CreatedAt: time.Now()}
	require.NoError(t, store.Create(t.Context(), live))
	require.NoError(t, client.SAdd(t.Context(), store.statusKey(core.TaskStatusQueued), "stale").Err())
	client.AddHook(&scriptedTaskSScanHook{pages: []taskScanPage{
		{members: []string{live.ID, "stale"}, cursor: 11},
		{members: nil, cursor: 22},
		{members: []string{live.ID}, cursor: 0},
	}})

	tasks, err := store.ListByStatus(t.Context(), core.TaskStatusQueued)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != live.ID {
		t.Fatalf("tasks = %v, want one deduplicated live task", tasks)
	}
	if client.SIsMember(t.Context(), store.statusKey(core.TaskStatusQueued), "stale").Val() {
		t.Fatal("stale task member survived completed cursor iteration")
	}
}

func TestTaskIndexReconcilerValidatesLimits(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	store := NewRedisTaskStore(client, nil)
	if _, err := NewTaskIndexReconciler(store, 0, 100); err == nil {
		t.Fatal("zero reconcile interval was accepted")
	}
	if _, err := NewTaskIndexReconciler(store, time.Minute, 0); err == nil {
		t.Fatal("zero reconcile bound was accepted")
	}
}

func TestTaskIndexReconcilerUsesBoundedBackgroundFailureLog(t *testing.T) {
	_, client := setupTaskStoreTestRedis(t)
	logger := &TestLogger{}
	config := DefaultRedisTaskStoreConfig()
	config.Logger = logger
	store := NewRedisTaskStore(client, &config)
	reconciler, err := NewTaskIndexReconciler(store, time.Millisecond, 10)
	require.NoError(t, err)
	require.NoError(t, client.Close())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- reconciler.Start(ctx) }()
	require.Eventually(t, func() bool {
		return len(logger.GetLogsByOperation("task_index_reconcile")) > 0
	}, time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)

	fields := logger.GetLogsByOperation("task_index_reconcile")[0].Fields
	assert.Equal(t, "redis task index reconciliation failed", fields["error"])
	assert.Equal(t, "index_reconcile", fields["error_type"])
	assert.IsType(t, int64(0), fields["duration_ms"])
	_, hasRequestID := fields["request_id"]
	assert.False(t, hasRequestID, "background maintenance must not invent request correlation")
}
