package orchestration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestRedisTaskDispatcher_ImplementsInterface(t *testing.T) {
	_, client := setupRedis(t)
	var _ core.TaskDispatcher = mustRedisDispatcher(t, client)
}

func TestRedisTaskDispatcher_New_NilClient_ReturnsError(t *testing.T) {
	d, err := NewRedisTaskDispatcher(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, errNilRedisClient)
	assert.Nil(t, d)
}

func TestRedisTaskDispatcher_Dispatch_WritesToCorrectKey(t *testing.T) {
	_, client := setupRedis(t)
	d := mustRedisDispatcher(t, client)

	task := &core.Task{
		ID:         "sch-1:12345",
		Type:       core.ScheduledTaskType,
		Status:     core.TaskStatusQueued,
		Input:      map[string]interface{}{"instruction": "do it"},
		ScheduleID: "sch-1",
	}
	require.NoError(t, d.Dispatch(context.Background(), "event-driven-agent", task))

	// Verify the task landed at the expected key (matches orchestration's
	// RedisTaskQueue convention).
	key := taskQueueKeyPrefix + "event-driven-agent"
	count, err := client.LLen(context.Background(), key).Result()
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)

	// Verify the content round-trips.
	raw, err := client.LIndex(context.Background(), key, 0).Result()
	require.NoError(t, err)

	var decoded core.Task
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	assert.Equal(t, "sch-1:12345", decoded.ID)
	assert.Equal(t, core.ScheduledTaskType, decoded.Type)
	assert.Equal(t, "sch-1", decoded.ScheduleID)
	assert.Equal(t, "do it", decoded.Input["instruction"])
}

func TestRedisTaskDispatcher_Dispatch_MultipleQueuesIndependent(t *testing.T) {
	_, client := setupRedis(t)
	d := mustRedisDispatcher(t, client)

	require.NoError(t, d.Dispatch(context.Background(), "agent-a", &core.Task{ID: "a-1"}))
	require.NoError(t, d.Dispatch(context.Background(), "agent-b", &core.Task{ID: "b-1"}))
	require.NoError(t, d.Dispatch(context.Background(), "agent-a", &core.Task{ID: "a-2"}))

	countA, _ := client.LLen(context.Background(), taskQueueKeyPrefix+"agent-a").Result()
	countB, _ := client.LLen(context.Background(), taskQueueKeyPrefix+"agent-b").Result()
	assert.EqualValues(t, 2, countA)
	assert.EqualValues(t, 1, countB)
}

func TestRedisTaskDispatcher_Dispatch_NilTask(t *testing.T) {
	_, client := setupRedis(t)
	d := mustRedisDispatcher(t, client)
	err := d.Dispatch(context.Background(), "agent-a", nil)
	assert.ErrorIs(t, err, errNilTask)
}

func TestRedisTaskDispatcher_Dispatch_EmptyTaskID(t *testing.T) {
	_, client := setupRedis(t)
	d := mustRedisDispatcher(t, client)
	err := d.Dispatch(context.Background(), "agent-a", &core.Task{Type: "x"})
	assert.ErrorIs(t, err, errEmptyTaskID)
}

func TestRedisTaskDispatcher_Dispatch_EmptyQueueName(t *testing.T) {
	_, client := setupRedis(t)
	d := mustRedisDispatcher(t, client)
	err := d.Dispatch(context.Background(), "", &core.Task{ID: "t-1"})
	assert.ErrorIs(t, err, errEmptyQueueName)
}

func TestRedisTaskDispatcher_Dispatch_ClientError(t *testing.T) {
	mr, client := setupRedis(t)
	d := mustRedisDispatcher(t, client)
	mr.Close() // Force command to fail.

	err := d.Dispatch(context.Background(), "agent-a", &core.Task{ID: "t-1"})
	require.Error(t, err)
	// Error may come from the idempotency SETNX or the LPUSH — both fail
	// because the Redis server is closed. Just verify it's an error.
	assert.Contains(t, err.Error(), "scheduler:")
}
