// Package orchestration provides background task worker pool implementation.
//
// This file implements the core.TaskWorker interface with a concurrent worker pool
// that processes tasks from a queue. Workers restore trace context using
// telemetry.StartLinkedSpan() to maintain distributed trace continuity.
package orchestration

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// TaskWorkerPool implements core.TaskWorker with concurrent worker goroutines.
type TaskWorkerPool struct {
	queue    core.TaskQueue
	store    core.TaskStore
	handlers map[string]core.TaskHandler
	config   TaskWorkerConfig
	logger   core.Logger

	// Lifecycle management
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// State tracking
	running      atomic.Bool
	activeCount  atomic.Int32
	handlersLock sync.RWMutex

	// Worker identification
	workerIDCounter atomic.Int32
}

// TaskWorkerConfig configures the worker pool.
type TaskWorkerConfig struct {
	// WorkerCount is the number of concurrent workers
	// Default: 5
	WorkerCount int `json:"worker_count"`

	// DequeueTimeout is how long each worker waits for a task
	// Default: 30s
	DequeueTimeout time.Duration `json:"dequeue_timeout"`

	// ShutdownTimeout is how long to wait for workers to finish on shutdown
	// Default: 30s
	ShutdownTimeout time.Duration `json:"shutdown_timeout"`

	// DefaultTaskTimeout is the default timeout for task execution
	// Default: 30 minutes
	DefaultTaskTimeout time.Duration `json:"default_task_timeout"`

	// Logger is an optional logger for worker operations
	Logger core.Logger `json:"-"`
}

// DefaultTaskWorkerConfig returns default configuration.
// Environment variable overrides (highest to lowest priority):
//   - TRUVAG3_TASK_TIMEOUT: Override DefaultTaskTimeout (e.g., "30m", "2h")
func DefaultTaskWorkerConfig() TaskWorkerConfig {
	config := TaskWorkerConfig{
		WorkerCount:        5,
		DequeueTimeout:     30 * time.Second,
		ShutdownTimeout:    30 * time.Second,
		DefaultTaskTimeout: 30 * time.Minute,
	}

	// Environment variable overrides
	if taskTimeout := os.Getenv("TRUVAG3_TASK_TIMEOUT"); taskTimeout != "" {
		if d, err := time.ParseDuration(taskTimeout); err == nil && d > 0 {
			config.DefaultTaskTimeout = d
		}
	}

	return config
}

// NewTaskWorkerPool creates a new worker pool.
func NewTaskWorkerPool(queue core.TaskQueue, store core.TaskStore, config *TaskWorkerConfig) *TaskWorkerPool {
	if config == nil {
		defaultConfig := DefaultTaskWorkerConfig()
		config = &defaultConfig
	}

	// Apply defaults
	if config.WorkerCount <= 0 {
		config.WorkerCount = 5
	}
	if config.DequeueTimeout <= 0 {
		config.DequeueTimeout = 30 * time.Second
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 30 * time.Second
	}
	if config.DefaultTaskTimeout <= 0 {
		config.DefaultTaskTimeout = 30 * time.Minute
	}

	p := &TaskWorkerPool{
		queue:    queue,
		store:    store,
		handlers: make(map[string]core.TaskHandler),
		config:   *config,
		logger:   config.Logger,
	}

	// Apply component-aware logging
	if p.logger != nil {
		if cal, ok := p.logger.(core.ComponentAwareLogger); ok {
			p.logger = cal.WithComponent("framework/orchestration")
		}
	}

	return p
}

// SetLogger sets the logger for worker operations.
func (p *TaskWorkerPool) SetLogger(logger core.Logger) {
	if logger != nil {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			p.logger = cal.WithComponent("framework/orchestration")
		} else {
			p.logger = logger
		}
	}
}

// RegisterHandler registers a handler for a task type.
// Must be called before Start.
func (p *TaskWorkerPool) RegisterHandler(taskType string, handler core.TaskHandler) error {
	if taskType == "" {
		return fmt.Errorf("task type cannot be empty")
	}
	if handler == nil {
		return fmt.Errorf("handler cannot be nil")
	}

	if p.running.Load() {
		return fmt.Errorf("cannot register handler while worker pool is running")
	}

	p.handlersLock.Lock()
	defer p.handlersLock.Unlock()

	p.handlers[taskType] = handler

	if p.logger != nil {
		p.logger.Info("Handler registered", map[string]interface{}{
			"task_type": taskType,
		})
	}

	return nil
}

// Start begins processing tasks.
// Blocks until ctx is cancelled or Stop is called.
func (p *TaskWorkerPool) Start(ctx context.Context) error {
	if p.running.Swap(true) {
		return fmt.Errorf("worker pool already running")
	}

	// Create cancellable context
	workerCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	if p.logger != nil {
		p.logger.Info("Starting worker pool", map[string]interface{}{
			"worker_count": p.config.WorkerCount,
		})
	}

	// Start workers
	for i := 0; i < p.config.WorkerCount; i++ {
		workerID := fmt.Sprintf("worker-%d", p.workerIDCounter.Add(1))
		p.wg.Add(1)
		go p.runWorker(workerCtx, workerID)
	}

	// Wait for all workers to finish
	p.wg.Wait()

	p.running.Store(false)

	if p.logger != nil {
		p.logger.Info("Worker pool stopped", map[string]interface{}{
			"worker_count": p.config.WorkerCount,
		})
	}

	return nil
}

// Stop gracefully stops the worker pool.
// Waits for in-progress tasks to complete up to shutdown timeout.
func (p *TaskWorkerPool) Stop(ctx context.Context) error {
	if !p.running.Load() {
		return nil
	}

	if p.logger != nil {
		p.logger.Info("Stopping worker pool", map[string]interface{}{
			"active_workers": p.activeCount.Load(),
		})
	}

	// Cancel worker context
	if p.cancel != nil {
		p.cancel()
	}

	// Wait for workers with timeout
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(p.config.ShutdownTimeout):
		return fmt.Errorf("shutdown timeout: some workers may still be running")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runWorker is the main loop for each worker goroutine.
func (p *TaskWorkerPool) runWorker(ctx context.Context, workerID string) {
	defer p.wg.Done()

	p.activeCount.Add(1)
	EmitWorkerStarted(workerID, int(p.activeCount.Load()))

	if p.logger != nil {
		p.logger.Info("Worker started", map[string]interface{}{
			"worker_id": workerID,
		})
	}

	defer func() {
		count := p.activeCount.Add(-1)
		EmitWorkerStopped(workerID, int(count))

		if p.logger != nil {
			p.logger.Info("Worker stopped", map[string]interface{}{
				"worker_id": workerID,
			})
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Dequeue next task
		task, err := p.queue.Dequeue(ctx, p.config.DequeueTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return // Context cancelled
			}
			if p.logger != nil {
				p.logger.Error("Dequeue error", map[string]interface{}{
					"worker_id": workerID,
					"error":     err.Error(),
				})
			}
			continue
		}

		if task == nil {
			// Timeout, no task available
			continue
		}

		// Process task
		p.processTask(ctx, workerID, task)
	}
}

// processTask processes a single task with trace context restoration.
func (p *TaskWorkerPool) processTask(parentCtx context.Context, workerID string, task *core.Task) {
	// Restore trace context and create processing span
	ctx, endSpan := telemetry.StartLinkedSpan(
		context.Background(), // Fresh context for worker
		"task.process",
		task.TraceID,
		task.ParentSpanID,
		map[string]string{
			"task.id":   task.ID,
			"task.type": task.Type,
			"worker.id": workerID,
		},
	)
	defer endSpan()
	// Every dequeued task reaches a terminal outcome in this method. Settle the
	// legacy TaskQueue handle on success, handler failure, timeout, and missing
	// handler paths so Redis in-flight records cannot accumulate indefinitely.
	defer func() {
		if err := p.queue.Acknowledge(ctx, task.ID); err != nil && p.logger != nil {
			p.logger.WarnWithContext(ctx, "Failed to acknowledge task", map[string]interface{}{
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
	}()

	startTime := time.Now()

	// Calculate queue wait time
	if !task.CreatedAt.IsZero() {
		waitTime := startTime.Sub(task.CreatedAt)
		EmitQueueWaitTime(ctx, task, waitTime)
	}

	// Emit task started
	EmitTaskStarted(ctx, task, workerID)

	// Update task status to running
	now := time.Now()
	task.Status = core.TaskStatusRunning
	task.StartedAt = &now

	if err := p.store.Update(ctx, task); err != nil {
		if p.logger != nil {
			p.logger.ErrorWithContext(ctx, "Failed to update task status to running", map[string]interface{}{
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
	}

	// Get handler for task type
	p.handlersLock.RLock()
	handler, exists := p.handlers[task.Type]
	p.handlersLock.RUnlock()

	if !exists {
		p.failTask(ctx, task, startTime, fmt.Errorf("no handler for task type: %s", task.Type))
		return
	}

	// Determine timeout
	timeout := p.config.DefaultTaskTimeout
	if task.Options.Timeout > 0 {
		timeout = task.Options.Timeout
	}

	// Create timeout context
	taskCtx, taskCancel := context.WithTimeout(ctx, timeout)
	defer taskCancel()

	// Create progress reporter
	reporter := &progressReporter{
		task:   task,
		store:  p.store,
		ctx:    taskCtx,
		logger: p.logger,
	}

	// Execute handler with panic recovery
	err := p.executeHandler(taskCtx, handler, task, reporter)

	duration := time.Since(startTime)

	if err != nil {
		if taskCtx.Err() == context.DeadlineExceeded {
			p.timeoutTask(ctx, task, timeout)
		} else {
			p.failTask(ctx, task, startTime, err)
		}
		return
	}

	// Success - update task
	completedAt := time.Now()
	task.Status = core.TaskStatusCompleted
	task.CompletedAt = &completedAt

	if err := p.store.Update(ctx, task); err != nil {
		if p.logger != nil {
			p.logger.ErrorWithContext(ctx, "Failed to update completed task", map[string]interface{}{
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
	}

	EmitTaskCompleted(ctx, task, duration)

	if p.logger != nil {
		p.logger.InfoWithContext(ctx, "Task completed", map[string]interface{}{
			"task_id":     task.ID,
			"task_type":   task.Type,
			"duration_ms": duration.Milliseconds(),
		})
	}
}

// executeHandler runs the handler with panic recovery.
func (p *TaskWorkerPool) executeHandler(ctx context.Context, handler core.TaskHandler, task *core.Task, reporter core.ProgressReporter) (err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			err = fmt.Errorf("handler panic: %v", r)

			EmitWorkerPanic(ctx, task.ID, r)

			if p.logger != nil {
				p.logger.ErrorWithContext(ctx, "Handler panicked", map[string]interface{}{
					"task_id": task.ID,
					"panic":   r,
					"stack":   stack,
				})
			}
		}
	}()

	return handler(ctx, task, reporter)
}

// failTask marks a task as failed.
func (p *TaskWorkerPool) failTask(ctx context.Context, task *core.Task, startTime time.Time, err error) {
	duration := time.Since(startTime)
	now := time.Now()

	task.Status = core.TaskStatusFailed
	task.CompletedAt = &now
	task.Error = &core.TaskError{
		Code:    core.TaskErrorCodeHandlerError,
		Message: err.Error(),
	}

	if updateErr := p.store.Update(ctx, task); updateErr != nil {
		if p.logger != nil {
			p.logger.ErrorWithContext(ctx, "Failed to update failed task", map[string]interface{}{
				"task_id": task.ID,
				"error":   updateErr.Error(),
			})
		}
	}

	EmitTaskFailed(ctx, task, duration, err)

	if p.logger != nil {
		p.logger.ErrorWithContext(ctx, "Task failed", map[string]interface{}{
			"task_id":     task.ID,
			"task_type":   task.Type,
			"duration_ms": duration.Milliseconds(),
			"error":       err.Error(),
		})
	}
}

// timeoutTask marks a task as timed out.
func (p *TaskWorkerPool) timeoutTask(ctx context.Context, task *core.Task, timeout time.Duration) {
	now := time.Now()

	task.Status = core.TaskStatusFailed
	task.CompletedAt = &now
	task.Error = &core.TaskError{
		Code:    core.TaskErrorCodeTimeout,
		Message: fmt.Sprintf("task exceeded timeout of %v", timeout),
	}

	if updateErr := p.store.Update(ctx, task); updateErr != nil {
		if p.logger != nil {
			p.logger.ErrorWithContext(ctx, "Failed to update timed out task", map[string]interface{}{
				"task_id": task.ID,
				"error":   updateErr.Error(),
			})
		}
	}

	EmitTaskTimeout(ctx, task, timeout)

	if p.logger != nil {
		p.logger.ErrorWithContext(ctx, "Task timed out", map[string]interface{}{
			"task_id":    task.ID,
			"task_type":  task.Type,
			"timeout_ms": timeout.Milliseconds(),
		})
	}
}

// progressReporter implements core.ProgressReporter.
type progressReporter struct {
	task   *core.Task
	store  core.TaskStore
	ctx    context.Context
	logger core.Logger
}

// Report updates task progress.
func (r *progressReporter) Report(progress *core.TaskProgress) error {
	if progress == nil {
		return nil
	}

	r.task.Progress = progress

	if err := r.store.Update(r.ctx, r.task); err != nil {
		if r.logger != nil {
			r.logger.WarnWithContext(r.ctx, "Failed to update task progress", map[string]interface{}{
				"task_id": r.task.ID,
				"error":   err.Error(),
			})
		}
		return err
	}

	EmitTaskProgress(r.ctx, r.task, progress)

	return nil
}
