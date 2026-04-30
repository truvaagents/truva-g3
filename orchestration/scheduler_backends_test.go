package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRedisSchedulerBackends_PopulatesBothPrimitives(t *testing.T) {
	_, client := setupRedis(t)
	backends, err := NewRedisSchedulerBackends(client)
	require.NoError(t, err)

	require.NotNil(t, backends)
	require.NotNil(t, backends.ScheduleStore)
	require.NotNil(t, backends.TaskDispatcher)

	// Verify the concrete types are Redis-backed.
	_, isRedisStore := backends.ScheduleStore.(*RedisScheduleStore)
	_, isRedisDispatcher := backends.TaskDispatcher.(*RedisTaskDispatcher)
	assert.True(t, isRedisStore, "ScheduleStore should be *RedisScheduleStore")
	assert.True(t, isRedisDispatcher, "TaskDispatcher should be *RedisTaskDispatcher")
}

func TestNewRedisSchedulerBackends_NilClient_ReturnsError(t *testing.T) {
	backends, err := NewRedisSchedulerBackends(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, errNilRedisClient)
	assert.Nil(t, backends)
}

func TestNewInMemorySchedulerBackends_PopulatesBothPrimitives(t *testing.T) {
	backends := NewInMemorySchedulerBackends()

	require.NotNil(t, backends)
	require.NotNil(t, backends.ScheduleStore)
	require.NotNil(t, backends.TaskDispatcher)

	// Verify the concrete types are in-memory.
	_, isMemStore := backends.ScheduleStore.(*InMemoryScheduleStore)
	_, isMemDispatcher := backends.TaskDispatcher.(*InMemoryTaskDispatcher)
	assert.True(t, isMemStore, "ScheduleStore should be *InMemoryScheduleStore")
	assert.True(t, isMemDispatcher, "TaskDispatcher should be *InMemoryTaskDispatcher")
}
