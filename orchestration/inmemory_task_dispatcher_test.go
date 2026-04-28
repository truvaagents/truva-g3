package orchestration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryTaskDispatcher_ImplementsInterface(t *testing.T) {
	var _ core.TaskDispatcher = NewInMemoryTaskDispatcher()
}

func TestInMemoryTaskDispatcher_DispatchAndSubscribe(t *testing.T) {
	d := NewInMemoryTaskDispatcher()
	ch := d.Subscribe("agent-a")

	task := &core.Task{ID: "t-1", Type: core.ScheduledTaskType, Status: core.TaskStatusQueued}
	require.NoError(t, d.Dispatch(context.Background(), "agent-a", task))

	select {
	case got := <-ch:
		require.NotNil(t, got)
		assert.Equal(t, "t-1", got.ID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected task on channel, got nothing")
	}
}

func TestInMemoryTaskDispatcher_Dispatch_NilTask(t *testing.T) {
	d := NewInMemoryTaskDispatcher()
	err := d.Dispatch(context.Background(), "agent-a", nil)
	assert.ErrorIs(t, err, errNilTask)
}

func TestInMemoryTaskDispatcher_Dispatch_EmptyTaskID(t *testing.T) {
	d := NewInMemoryTaskDispatcher()
	err := d.Dispatch(context.Background(), "agent-a", &core.Task{Type: "x"})
	assert.ErrorIs(t, err, errEmptyTaskID)
}

func TestInMemoryTaskDispatcher_Dispatch_EmptyQueueName(t *testing.T) {
	d := NewInMemoryTaskDispatcher()
	err := d.Dispatch(context.Background(), "", &core.Task{ID: "t-1"})
	assert.ErrorIs(t, err, errEmptyQueueName)
}

func TestInMemoryTaskDispatcher_Dispatch_MultipleQueues(t *testing.T) {
	d := NewInMemoryTaskDispatcher()
	chA := d.Subscribe("agent-a")
	chB := d.Subscribe("agent-b")

	require.NoError(t, d.Dispatch(context.Background(), "agent-a", &core.Task{ID: "ta-1"}))
	require.NoError(t, d.Dispatch(context.Background(), "agent-b", &core.Task{ID: "tb-1"}))

	select {
	case got := <-chA:
		assert.Equal(t, "ta-1", got.ID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no task on agent-a")
	}

	select {
	case got := <-chB:
		assert.Equal(t, "tb-1", got.ID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no task on agent-b")
	}
}

func TestInMemoryTaskDispatcher_Subscribe_CreatesAndReturnsSameChannel(t *testing.T) {
	d := NewInMemoryTaskDispatcher()
	ch1 := d.Subscribe("agent-a")
	ch2 := d.Subscribe("agent-a")

	// Subscribing twice to the same queue should return the same underlying
	// channel so tests can re-read without losing messages.
	require.NoError(t, d.Dispatch(context.Background(), "agent-a", &core.Task{ID: "t-1"}))

	select {
	case got := <-ch1:
		assert.Equal(t, "t-1", got.ID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no task on ch1")
	}
	// ch2 should be the same channel — no second task to read.
	select {
	case <-ch2:
		t.Fatal("ch2 should be the same channel as ch1, but got a separate message")
	case <-time.After(10 * time.Millisecond):
		// Expected — nothing to read.
	}
}

func TestInMemoryTaskDispatcher_Dispatch_QueueFull_ReturnsError(t *testing.T) {
	d := NewInMemoryTaskDispatcher()
	// Fill the queue to capacity.
	for i := 0; i < defaultInMemoryQueueCapacity; i++ {
		require.NoError(t, d.Dispatch(context.Background(), "agent-a", &core.Task{ID: fmt.Sprintf("t-%d", i)}))
	}
	// Next dispatch should fail with queue-full.
	err := d.Dispatch(context.Background(), "agent-a", &core.Task{ID: "overflow"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue is full")
}

func TestInMemoryTaskDispatcher_ConcurrentAccess(t *testing.T) {
	d := NewInMemoryTaskDispatcher()
	const n = 20

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = d.Dispatch(context.Background(), "agent-concurrent", &core.Task{ID: "concurrent"})
		}(i)
	}
	wg.Wait()

	ch := d.Subscribe("agent-concurrent")
	received := 0
	for {
		select {
		case <-ch:
			received++
		default:
			assert.GreaterOrEqual(t, received, 1, "at least one task should have been delivered")
			return
		}
	}
}
