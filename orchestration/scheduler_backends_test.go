package orchestration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
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

func TestNewRedisSchedulerBackends_UsesDeploymentKeyspace(t *testing.T) {
	_, client := setupRedis(t)
	keyspace, err := core.NewRedisKeyspace("production")
	require.NoError(t, err)

	backends, err := NewRedisSchedulerBackends(client, WithRedisSchedulerKeyspace(keyspace))
	require.NoError(t, err)

	store := backends.ScheduleStore.(*RedisScheduleStore)
	dispatcher := backends.TaskDispatcher.(*RedisTaskDispatcher)
	consumer := backends.TaskConsumer.(*RedisTaskConsumer)
	for _, key := range []string{store.dueKey(), dispatcher.queuePrefix, dispatcher.idPrefix, consumer.queuePrefix, consumer.dlqPrefix} {
		assert.True(t, strings.HasPrefix(key, "truvag3:v1:production:"), key)
	}
}

func TestNewRedisStreamsSchedulerBackends_UsesDeploymentKeyspace(t *testing.T) {
	_, client := setupRedis(t)
	keyspace, err := core.NewRedisKeyspace("production")
	require.NoError(t, err)

	backends, runnable, err := NewRedisStreamsSchedulerBackends(client, WithRedisSchedulerKeyspace(keyspace))
	require.NoError(t, err)

	dispatcher := backends.TaskDispatcher.(*RedisStreamsTaskDispatcher)
	consumer := backends.TaskConsumer.(*RedisStreamsTaskConsumer)
	reaper := runnable.(*RedisStreamsReaper)
	for _, key := range []string{dispatcher.streamPrefix, dispatcher.idPrefix, consumer.streamPrefix, consumer.dlqPrefix, reaper.streamKey} {
		assert.True(t, strings.HasPrefix(key, "truvag3:v1:production:"), key)
	}
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
