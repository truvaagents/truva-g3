// Package orchestration — In-memory core.TaskConsumer for tests and dev.
//
// Wraps InMemoryTaskDispatcher's Subscribe channel. Tests use this together
// with InMemoryTaskDispatcher for end-to-end dispatch/consume testing
// without any transport. Nack entries are accumulated in a thread-safe
// slice accessible via DLQEntries() for test assertions.

package orchestration

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

var _ core.TaskConsumer = (*InMemoryTaskConsumer)(nil)
var _ core.TaskHandle = (*inMemoryHandle)(nil)

// InMemoryTaskConsumer wraps an InMemoryTaskDispatcher's channel.
type InMemoryTaskConsumer struct {
	dispatcher *InMemoryTaskDispatcher
	mu         sync.Mutex
	dlq        []InMemoryDLQEntry
}

// InMemoryDLQEntry captures a Nack-with-terminal-reason in the in-memory backend.
type InMemoryDLQEntry struct {
	Task     *core.Task
	Reason   string
	FailedAt time.Time
}

// NewInMemoryTaskConsumerFromDispatcher creates a consumer paired with the
// given dispatcher. Both must point at the same logical queue.
func NewInMemoryTaskConsumerFromDispatcher(d *InMemoryTaskDispatcher) *InMemoryTaskConsumer {
	return &InMemoryTaskConsumer{dispatcher: d}
}

// Consume implements core.TaskConsumer.
func (c *InMemoryTaskConsumer) Consume(ctx context.Context, queueName string) (core.TaskHandle, error) {
	if queueName == "" {
		return nil, fmt.Errorf("orchestration: Consume queueName is required")
	}
	if c.dispatcher == nil {
		return nil, fmt.Errorf("orchestration: InMemoryTaskConsumer has no dispatcher")
	}
	ch := c.dispatcher.Subscribe(queueName)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case task := <-ch:
		return &inMemoryHandle{consumer: c, task: task}, nil
	}
}

// DLQEntries returns a snapshot copy of all Nacked entries. Used by tests.
func (c *InMemoryTaskConsumer) DLQEntries() []InMemoryDLQEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]InMemoryDLQEntry, len(c.dlq))
	copy(out, c.dlq)
	return out
}

type inMemoryHandle struct {
	consumer *InMemoryTaskConsumer
	task     *core.Task
	mu       sync.Mutex
	settled  bool
}

func (h *inMemoryHandle) Task() *core.Task { return h.task }

func (h *inMemoryHandle) Ack(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled {
		return fmt.Errorf("orchestration: handle already settled")
	}
	h.settled = true
	return nil
}

func (h *inMemoryHandle) Nack(ctx context.Context, reason string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled {
		return fmt.Errorf("orchestration: handle already settled")
	}
	h.settled = true

	if h.task == nil {
		return errNilTask
	}
	h.consumer.mu.Lock()
	defer h.consumer.mu.Unlock()
	h.consumer.dlq = append(h.consumer.dlq, InMemoryDLQEntry{
		Task:     h.task,
		Reason:   reason,
		FailedAt: time.Now(),
	})
	return nil
}
