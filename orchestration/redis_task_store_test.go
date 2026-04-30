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
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

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
