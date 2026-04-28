// Package orchestration — InMemoryTaskDispatcher for dev/test and isolated
// scheduler unit tests without needing a worker pool.
//
// Each named queue is backed by an in-process buffered channel. Tests can
// call Subscribe(queueName) to read dispatched tasks for assertions.
//
// Not suitable for production: tasks do not cross process boundaries, so
// a real worker pool on another pod would never see them.

package orchestration

import (
	"context"
	"sync"

	"github.com/truvaagents/truva-g3/core"
)

// Compile-time check: InMemoryTaskDispatcher satisfies core.TaskDispatcher.
var _ core.TaskDispatcher = (*InMemoryTaskDispatcher)(nil)

// defaultInMemoryQueueCapacity is the buffer size used for each named
// queue's channel. Tests can drain channels as tasks are dispatched, so
// a modest buffer avoids blocking in most scenarios.
const defaultInMemoryQueueCapacity = 100

// InMemoryTaskDispatcher is an in-process implementation of
// core.TaskDispatcher. It maintains a separate buffered channel per queue
// name. Dispatch is non-blocking when the channel has capacity; if the
// channel is full, Dispatch returns an error (callers decide whether to
// retry or drop).
type InMemoryTaskDispatcher struct {
	mu     sync.RWMutex
	queues map[string]chan *core.Task
	// seenIDs tracks dispatched task IDs for idempotency.
	seenIDs map[string]bool
}

// NewInMemoryTaskDispatcher creates a new empty in-memory dispatcher.
func NewInMemoryTaskDispatcher() *InMemoryTaskDispatcher {
	return &InMemoryTaskDispatcher{
		queues:  make(map[string]chan *core.Task),
		seenIDs: make(map[string]bool),
	}
}

// Dispatch delivers the task to the named queue. Creates the queue if it
// doesn't exist yet. Non-blocking — returns an error if the queue buffer
// is full. Returns core.ErrTaskAlreadyExists if the same task ID was
// already dispatched (idempotency contract).
func (d *InMemoryTaskDispatcher) Dispatch(_ context.Context, queueName string, task *core.Task) error {
	if task == nil {
		return errNilTask
	}
	if task.ID == "" {
		return errEmptyTaskID
	}
	if queueName == "" {
		return errEmptyQueueName
	}

	d.mu.Lock()
	if d.seenIDs[task.ID] {
		d.mu.Unlock()
		return core.ErrTaskAlreadyExists
	}
	d.seenIDs[task.ID] = true
	d.mu.Unlock()

	ch := d.ensureQueue(queueName)

	select {
	case ch <- task:
		return nil
	default:
		return errQueueFull
	}
}

// Subscribe returns the read-only channel for the given queue name,
// creating it if necessary. Tests use this to consume dispatched tasks
// for assertions.
//
// Returned as <-chan so callers cannot accidentally send on the channel.
func (d *InMemoryTaskDispatcher) Subscribe(queueName string) <-chan *core.Task {
	return d.ensureQueue(queueName)
}

// ensureQueue returns the channel for the given queue name, creating it
// with default capacity if it doesn't exist yet. Safe for concurrent calls.
func (d *InMemoryTaskDispatcher) ensureQueue(queueName string) chan *core.Task {
	d.mu.RLock()
	ch, ok := d.queues[queueName]
	d.mu.RUnlock()
	if ok {
		return ch
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	// Double-check under write lock.
	if ch, ok := d.queues[queueName]; ok {
		return ch
	}
	ch = make(chan *core.Task, defaultInMemoryQueueCapacity)
	d.queues[queueName] = ch
	return ch
}

// errQueueFull is declared in errors.go alongside the other package-private
// validation errors.
