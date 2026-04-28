package orchestration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// testTaskQueue implements core.TaskQueue for worker testing
type testTaskQueue struct {
	tasks       chan *core.Task
	mu          sync.Mutex
	ackCalled   map[string]bool
	rejectCalls []struct {
		taskID string
		reason string
	}
}

func newTestTaskQueue(bufferSize int) *testTaskQueue {
	return &testTaskQueue{
		tasks:     make(chan *core.Task, bufferSize),
		ackCalled: make(map[string]bool),
	}
}

func (q *testTaskQueue) Enqueue(ctx context.Context, task *core.Task) error {
	select {
	case q.tasks <- task:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *testTaskQueue) Dequeue(ctx context.Context, timeout time.Duration) (*core.Task, error) {
	select {
	case task := <-q.tasks:
		return task, nil
	case <-time.After(timeout):
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *testTaskQueue) Acknowledge(ctx context.Context, taskID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ackCalled[taskID] = true
	return nil
}

func (q *testTaskQueue) Reject(ctx context.Context, taskID string, reason string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rejectCalls = append(q.rejectCalls, struct {
		taskID string
		reason string
	}{taskID, reason})
	return nil
}

// testTaskStore implements core.TaskStore for worker testing
type testTaskStore struct {
	mu     sync.RWMutex
	tasks  map[string]*core.Task
	errors map[string]error
}

func newTestTaskStore() *testTaskStore {
	return &testTaskStore{
		tasks:  make(map[string]*core.Task),
		errors: make(map[string]error),
	}
}

func (s *testTaskStore) Create(ctx context.Context, task *core.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = task
	return nil
}

func (s *testTaskStore) Get(ctx context.Context, taskID string) (*core.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, exists := s.tasks[taskID]
	if !exists {
		return nil, core.ErrTaskNotFound
	}
	return task, nil
}

func (s *testTaskStore) Update(ctx context.Context, task *core.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, hasErr := s.errors[task.ID]; hasErr {
		return err
	}
	s.tasks[task.ID] = task
	return nil
}

func (s *testTaskStore) Delete(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, taskID)
	return nil
}

func (s *testTaskStore) Cancel(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, exists := s.tasks[taskID]
	if !exists {
		return core.ErrTaskNotFound
	}
	if task.Status.IsTerminal() {
		return core.ErrTaskNotCancellable
	}
	now := time.Now()
	task.Status = core.TaskStatusCancelled
	task.CancelledAt = &now
	return nil
}

func TestTaskWorkerPool_RegisterHandler(t *testing.T) {
	queue := newTestTaskQueue(10)
	store := newTestTaskStore()
	pool := NewTaskWorkerPool(queue, store, nil)

	// Register valid handler
	err := pool.RegisterHandler("test", func(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
		return nil
	})
	if err != nil {
		t.Errorf("RegisterHandler() error = %v", err)
	}

	// Register with empty type
	err = pool.RegisterHandler("", func(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
		return nil
	})
	if err == nil {
		t.Error("RegisterHandler() should error with empty type")
	}

	// Register with nil handler
	err = pool.RegisterHandler("test2", nil)
	if err == nil {
		t.Error("RegisterHandler() should error with nil handler")
	}
}

func TestTaskWorkerPool_StartStop(t *testing.T) {
	queue := newTestTaskQueue(10)
	store := newTestTaskStore()
	config := &TaskWorkerConfig{
		WorkerCount:    2,
		DequeueTimeout: 100 * time.Millisecond,
	}
	pool := NewTaskWorkerPool(queue, store, config)

	// Start in background
	ctx, cancel := context.WithCancel(context.Background())
	startErr := make(chan error, 1)
	go func() {
		startErr <- pool.Start(ctx)
	}()

	// Give workers time to start
	time.Sleep(50 * time.Millisecond)

	// Stop the pool
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	if err := pool.Stop(stopCtx); err != nil {
		t.Errorf("Stop() error = %v", err)
	}

	// Wait for Start to return
	select {
	case <-startErr:
		// Expected
	case <-time.After(2 * time.Second):
		t.Error("Start() did not return after Stop()")
	}
}

func TestTaskWorkerPool_ProcessTask(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping task worker pool process test in short mode (requires 300ms+ sleep)")
	}

	queue := newTestTaskQueue(10)
	store := newTestTaskStore()
	config := &TaskWorkerConfig{
		WorkerCount:        1,
		DequeueTimeout:     100 * time.Millisecond,
		DefaultTaskTimeout: 5 * time.Second,
	}
	pool := NewTaskWorkerPool(queue, store, config)

	// Track handler calls
	var handlerCalled atomic.Bool

	// Register handler
	err := pool.RegisterHandler("test", func(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
		handlerCalled.Store(true)
		// Report progress
		_ = reporter.Report(&core.TaskProgress{
			CurrentStep: 1,
			TotalSteps:  1,
			Percentage:  100,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("RegisterHandler() error = %v", err)
	}

	// Create and store task
	task := core.NewTask("task-1", "test", nil)
	_ = store.Create(context.Background(), task)

	// Enqueue task
	_ = queue.Enqueue(context.Background(), task)

	// Start pool
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = pool.Start(ctx) }()

	// Wait for task to be processed
	time.Sleep(300 * time.Millisecond)

	// Stop pool
	cancel()
	_ = pool.Stop(context.Background())

	// Verify handler was called
	if !handlerCalled.Load() {
		t.Error("Handler was not called")
	}

	// Verify task was acknowledged
	queue.mu.Lock()
	acked := queue.ackCalled["task-1"]
	queue.mu.Unlock()
	if !acked {
		t.Error("Task was not acknowledged")
	}

	// Verify task status was updated
	updatedTask, _ := store.Get(context.Background(), "task-1")
	if updatedTask.Status != core.TaskStatusCompleted {
		t.Errorf("Task status = %v, want completed", updatedTask.Status)
	}
}

func TestTaskWorkerPool_HandlerError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping task worker pool handler error test in short mode (requires 300ms+ sleep)")
	}

	queue := newTestTaskQueue(10)
	store := newTestTaskStore()
	config := &TaskWorkerConfig{
		WorkerCount:        1,
		DequeueTimeout:     100 * time.Millisecond,
		DefaultTaskTimeout: 5 * time.Second,
	}
	pool := NewTaskWorkerPool(queue, store, config)

	// Register handler that returns error
	err := pool.RegisterHandler("test", func(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
		return errors.New("handler error")
	})
	if err != nil {
		t.Fatalf("RegisterHandler() error = %v", err)
	}

	// Create and store task
	task := core.NewTask("task-error", "test", nil)
	_ = store.Create(context.Background(), task)
	_ = queue.Enqueue(context.Background(), task)

	// Start pool
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = pool.Start(ctx) }()

	// Wait for task to be processed
	time.Sleep(300 * time.Millisecond)

	// Stop pool
	cancel()
	_ = pool.Stop(context.Background())

	// Verify task status was updated to failed
	updatedTask, _ := store.Get(context.Background(), "task-error")
	if updatedTask.Status != core.TaskStatusFailed {
		t.Errorf("Task status = %v, want failed", updatedTask.Status)
	}
	if updatedTask.Error == nil {
		t.Error("Task error should not be nil")
	}
}

func TestTaskWorkerPool_HandlerPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping task worker pool handler panic test in short mode (requires 300ms+ sleep)")
	}

	queue := newTestTaskQueue(10)
	store := newTestTaskStore()
	config := &TaskWorkerConfig{
		WorkerCount:        1,
		DequeueTimeout:     100 * time.Millisecond,
		DefaultTaskTimeout: 5 * time.Second,
	}
	pool := NewTaskWorkerPool(queue, store, config)

	// Register handler that panics
	err := pool.RegisterHandler("test", func(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
		panic("handler panic")
	})
	if err != nil {
		t.Fatalf("RegisterHandler() error = %v", err)
	}

	// Create and store task
	task := core.NewTask("task-panic", "test", nil)
	_ = store.Create(context.Background(), task)
	_ = queue.Enqueue(context.Background(), task)

	// Start pool
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = pool.Start(ctx) }()

	// Wait for task to be processed
	time.Sleep(300 * time.Millisecond)

	// Stop pool
	cancel()
	_ = pool.Stop(context.Background())

	// Verify task status was updated to failed
	updatedTask, _ := store.Get(context.Background(), "task-panic")
	if updatedTask.Status != core.TaskStatusFailed {
		t.Errorf("Task status = %v, want failed", updatedTask.Status)
	}
}

func TestTaskWorkerPool_NoHandler(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping task worker pool no handler test in short mode (requires 300ms+ sleep)")
	}

	queue := newTestTaskQueue(10)
	store := newTestTaskStore()
	config := &TaskWorkerConfig{
		WorkerCount:        1,
		DequeueTimeout:     100 * time.Millisecond,
		DefaultTaskTimeout: 5 * time.Second,
	}
	pool := NewTaskWorkerPool(queue, store, config)

	// Don't register any handler

	// Create and store task
	task := core.NewTask("task-no-handler", "unknown-type", nil)
	_ = store.Create(context.Background(), task)
	_ = queue.Enqueue(context.Background(), task)

	// Start pool
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = pool.Start(ctx) }()

	// Wait for task to be processed
	time.Sleep(300 * time.Millisecond)

	// Stop pool
	cancel()
	_ = pool.Stop(context.Background())

	// Verify task status was updated to failed
	updatedTask, _ := store.Get(context.Background(), "task-no-handler")
	if updatedTask.Status != core.TaskStatusFailed {
		t.Errorf("Task status = %v, want failed", updatedTask.Status)
	}
}

func TestDefaultTaskWorkerConfig(t *testing.T) {
	config := DefaultTaskWorkerConfig()

	if config.WorkerCount != 5 {
		t.Errorf("WorkerCount = %v, want 5", config.WorkerCount)
	}
	if config.DequeueTimeout != 30*time.Second {
		t.Errorf("DequeueTimeout = %v, want 30s", config.DequeueTimeout)
	}
	if config.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 30s", config.ShutdownTimeout)
	}
	if config.DefaultTaskTimeout != 30*time.Minute {
		t.Errorf("DefaultTaskTimeout = %v, want 30m", config.DefaultTaskTimeout)
	}
}

func TestTaskWorkerPool_ConfigDefaults(t *testing.T) {
	queue := newTestTaskQueue(10)
	store := newTestTaskStore()

	// Create with nil config
	pool := NewTaskWorkerPool(queue, store, nil)

	if pool.config.WorkerCount != 5 {
		t.Errorf("WorkerCount = %v, want 5", pool.config.WorkerCount)
	}
}

func TestTaskWorkerPool_DoubleStart(t *testing.T) {
	queue := newTestTaskQueue(10)
	store := newTestTaskStore()
	config := &TaskWorkerConfig{
		WorkerCount:    1,
		DequeueTimeout: 100 * time.Millisecond,
	}
	pool := NewTaskWorkerPool(queue, store, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start first time
	go func() { _ = pool.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Try to start again - should error
	err := pool.Start(ctx)
	if err == nil {
		t.Error("Double Start() should return error")
	}

	cancel()
	_ = pool.Stop(context.Background())
}

func TestProgressReporter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping progress reporter test in short mode (requires 300ms+ sleep)")
	}

	queue := newTestTaskQueue(10)
	store := newTestTaskStore()
	config := &TaskWorkerConfig{
		WorkerCount:        1,
		DequeueTimeout:     100 * time.Millisecond,
		DefaultTaskTimeout: 5 * time.Second,
	}
	pool := NewTaskWorkerPool(queue, store, config)

	var progressReported atomic.Bool

	// Register handler that reports progress
	err := pool.RegisterHandler("test", func(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
		err := reporter.Report(&core.TaskProgress{
			CurrentStep: 1,
			TotalSteps:  3,
			StepName:    "Step 1",
			Percentage:  33.3,
			Message:     "Processing...",
		})
		if err == nil {
			progressReported.Store(true)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RegisterHandler() error = %v", err)
	}

	// Create and store task
	task := core.NewTask("task-progress", "test", nil)
	_ = store.Create(context.Background(), task)
	_ = queue.Enqueue(context.Background(), task)

	// Start pool
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = pool.Start(ctx) }()

	// Wait for task to be processed
	time.Sleep(300 * time.Millisecond)

	// Stop pool
	cancel()
	_ = pool.Stop(context.Background())

	// Verify progress was reported
	if !progressReported.Load() {
		t.Error("Progress was not reported")
	}

	// Verify progress was stored
	updatedTask, _ := store.Get(context.Background(), "task-progress")
	if updatedTask.Progress == nil {
		t.Error("Task progress should not be nil")
	} else if updatedTask.Progress.CurrentStep != 1 {
		t.Errorf("Progress.CurrentStep = %v, want 1", updatedTask.Progress.CurrentStep)
	}
}

func TestTaskWorkerPool_SetLogger(t *testing.T) {
	queue := newTestTaskQueue(10)
	store := newTestTaskStore()
	pool := NewTaskWorkerPool(queue, store, nil)

	// Initially logger should be nil
	if pool.logger != nil {
		t.Error("Logger should be nil initially")
	}

	// Create a mock logger
	logger := &mockLogger{}

	// Set the logger
	pool.SetLogger(logger)

	// Verify logger is set
	if pool.logger == nil {
		t.Error("Logger should not be nil after SetLogger")
	}

	// Set nil logger - implementation ignores nil, so logger stays set
	pool.SetLogger(nil)
	if pool.logger == nil {
		t.Error("Logger should remain set after SetLogger(nil) - nil is ignored")
	}
}

func TestProgressReporter_NilProgress(t *testing.T) {
	store := newTestTaskStore()
	task := core.NewTask("task-nil-progress", "test", nil)
	_ = store.Create(context.Background(), task)

	reporter := &progressReporter{
		task:   task,
		store:  store,
		ctx:    context.Background(),
		logger: nil,
	}

	// Report nil progress should return nil without error
	err := reporter.Report(nil)
	if err != nil {
		t.Errorf("Report(nil) should return nil, got %v", err)
	}
}

func TestProgressReporter_StoreError(t *testing.T) {
	store := newTestTaskStore()
	task := core.NewTask("task-store-error", "test", nil)
	// Don't create the task in store, so Update will fail
	store.errors["task-store-error"] = errors.New("store error")
	store.tasks["task-store-error"] = task

	reporter := &progressReporter{
		task:   task,
		store:  store,
		ctx:    context.Background(),
		logger: nil,
	}

	// Report progress when store fails
	err := reporter.Report(&core.TaskProgress{
		CurrentStep: 1,
		TotalSteps:  2,
	})
	if err == nil {
		t.Error("Report() should return error when store fails")
	}
}
