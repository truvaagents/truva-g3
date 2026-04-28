// Package core provides async task interfaces and types for long-running operations.
//
// This file defines the interfaces and types for the async task system that enables
// long-running operations (minutes to hours) in Truva-G3. It solves the HTTP timeout
// problem by providing HTTP 202 + polling pattern with background worker execution.
//
// # Architecture Overview
//
// The async task system consists of:
//   - TaskQueue: Handles task submission and retrieval (Redis-backed by default)
//   - TaskStore: Persists task state and results (Redis-backed by default)
//   - TaskWorker: Processes tasks from the queue in the background
//   - ProgressReporter: Allows handlers to report progress updates
//
// # Distributed Tracing
//
// The Task struct includes TraceID and ParentSpanID fields to preserve distributed
// trace context across async boundaries. Workers restore this context using
// telemetry.StartLinkedSpan() to maintain full trace visibility in Jaeger.
//
// # Usage
//
// Submitting a task:
//
//	task := &core.Task{
//	    ID:           generateTaskID(),
//	    Type:         "orchestration",
//	    Status:       core.TaskStatusQueued,
//	    Input:        map[string]interface{}{"query": "weather in Tokyo"},
//	    TraceID:      tc.TraceID,      // From telemetry.GetTraceContext(ctx)
//	    ParentSpanID: tc.SpanID,
//	}
//	err := taskQueue.Enqueue(ctx, task)
//
// Processing a task (in worker):
//
//	func handleOrchestration(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
//	    reporter.Report(&core.TaskProgress{CurrentStep: 1, TotalSteps: 3, StepName: "Planning"})
//	    // ... do work ...
//	    return nil
//	}
package core

import (
	"context"
	"errors"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// Errors
// ═══════════════════════════════════════════════════════════════════════════

// ErrTaskNotFound is returned when a task cannot be found
var ErrTaskNotFound = errors.New("task not found")

// ErrTaskNotCancellable is returned when a task cannot be cancelled
// (already completed, failed, or cancelled)
var ErrTaskNotCancellable = errors.New("task not cancellable")

// ErrTaskQueueEmpty is returned when Dequeue times out with no task available
var ErrTaskQueueEmpty = errors.New("task queue empty")

// ErrInvalidTaskStatus is returned when a task status transition is invalid
var ErrInvalidTaskStatus = errors.New("invalid task status transition")

// ErrTaskAlreadyExists is returned when TaskStore.Create is called with
// an ID that already exists. Used by the Scheduler for idempotent task
// creation when promoting due schedules — duplicate dispatch is detected
// via errors.Is(err, ErrTaskAlreadyExists).
var ErrTaskAlreadyExists = errors.New("task already exists")

// ErrScheduleNotFound is returned when a schedule cannot be found.
var ErrScheduleNotFound = errors.New("schedule not found")

// ErrScheduleAlreadyExists is returned when a schedule with the same ID already exists.
var ErrScheduleAlreadyExists = errors.New("schedule already exists")

// ═══════════════════════════════════════════════════════════════════════════
// Types
// ═══════════════════════════════════════════════════════════════════════════

// TaskStatus represents the state of a long-running task
type TaskStatus string

const (
	// TaskStatusQueued indicates the task is waiting in the queue
	TaskStatusQueued TaskStatus = "queued"

	// TaskStatusRunning indicates the task is currently being processed
	TaskStatusRunning TaskStatus = "running"

	// TaskStatusCompleted indicates the task finished successfully
	TaskStatusCompleted TaskStatus = "completed"

	// TaskStatusFailed indicates the task failed with an error
	TaskStatusFailed TaskStatus = "failed"

	// TaskStatusCancelled indicates the task was cancelled by request
	TaskStatusCancelled TaskStatus = "cancelled"
)

// IsTerminal returns true if the status is a terminal state (completed, failed, or cancelled)
func (s TaskStatus) IsTerminal() bool {
	return s == TaskStatusCompleted || s == TaskStatusFailed || s == TaskStatusCancelled
}

// Task represents a long-running async task
type Task struct {
	// ID is the unique identifier for this task
	ID string `json:"id"`

	// Type identifies the kind of task (e.g., "orchestration", "research")
	// Used to route tasks to the appropriate handler
	Type string `json:"type"`

	// Status is the current state of the task
	Status TaskStatus `json:"status"`

	// Input contains the task parameters
	Input map[string]interface{} `json:"input"`

	// Result contains the task output when completed
	Result interface{} `json:"result,omitempty"`

	// Error contains error information if the task failed
	Error *TaskError `json:"error,omitempty"`

	// Progress contains the current progress of the task
	Progress *TaskProgress `json:"progress,omitempty"`

	// Options configures task execution behavior
	Options TaskOptions `json:"options"`

	// CreatedAt is when the task was submitted
	CreatedAt time.Time `json:"created_at"`

	// StartedAt is when the worker began processing (nil if queued)
	StartedAt *time.Time `json:"started_at,omitempty"`

	// CompletedAt is when the task finished (nil if not complete)
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// CancelledAt is when the task was cancelled (nil if not cancelled)
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`

	// ═══════════════════════════════════════════════════════════════════════
	// Trace Context for Distributed Tracing
	// ═══════════════════════════════════════════════════════════════════════
	//
	// These fields preserve the trace chain across async boundaries.
	// When a task is submitted, extract trace context using:
	//   tc := telemetry.GetTraceContext(ctx)
	//   task.TraceID = tc.TraceID
	//   task.ParentSpanID = tc.SpanID
	//
	// When a worker processes the task, restore context using:
	//   ctx, endSpan := telemetry.StartLinkedSpan(ctx, "task.process",
	//       task.TraceID, task.ParentSpanID, attrs)
	//   defer endSpan()

	// TraceID is the W3C trace ID (32 hex chars) from the original request
	TraceID string `json:"trace_id,omitempty"`

	// ParentSpanID is the span ID (16 hex chars) of the submitting request
	ParentSpanID string `json:"parent_span_id,omitempty"`

	// ═══════════════════════════════════════════════════════════════════════
	// Schedule Context
	// ═══════════════════════════════════════════════════════════════════════

	// ScheduleID links this task to the Schedule that created it.
	// Empty for ad-hoc immediate tasks. Set by the Scheduler when promoting
	// a due schedule into the task queue.
	ScheduleID string `json:"schedule_id,omitempty"`

	// TargetAgent is the agent name that should receive this task when it
	// fires. Used by the scheduled-executor to resolve which agent's
	// /api/v1/scheduled endpoint to POST to. Empty for non-scheduled tasks.
	// Set by the Scheduler from Schedule.TargetAgent during task materialization.
	TargetAgent string `json:"target_agent,omitempty"`
}

// TaskProgress tracks execution progress
type TaskProgress struct {
	// CurrentStep is the current step number (1-indexed)
	CurrentStep int `json:"current_step"`

	// TotalSteps is the total number of steps
	TotalSteps int `json:"total_steps"`

	// StepName is a human-readable name for the current step
	StepName string `json:"step_name"`

	// Percentage is the overall completion percentage (0-100)
	Percentage float64 `json:"percentage"`

	// Message is an optional status message
	Message string `json:"message,omitempty"`
}

// TaskOptions configures task execution
type TaskOptions struct {
	// Timeout is the maximum duration for task execution
	// If zero, DefaultAsyncTaskConfig().DefaultTimeout is used
	Timeout time.Duration `json:"timeout"`
}

// TaskError contains error information
type TaskError struct {
	// Code is a machine-readable error code
	Code string `json:"code"`

	// Message is a human-readable error message
	Message string `json:"message"`

	// Details contains additional error details
	Details string `json:"details,omitempty"`
}

// Error implements the error interface
func (e *TaskError) Error() string {
	if e.Details != "" {
		return e.Code + ": " + e.Message + " (" + e.Details + ")"
	}
	return e.Code + ": " + e.Message
}

// Common error codes for TaskError
const (
	// TaskErrorCodeTimeout indicates the task exceeded its timeout
	TaskErrorCodeTimeout = "TASK_TIMEOUT"

	// TaskErrorCodeCancelled indicates the task was cancelled
	TaskErrorCodeCancelled = "TASK_CANCELLED"

	// TaskErrorCodeHandlerError indicates the handler returned an error
	TaskErrorCodeHandlerError = "HANDLER_ERROR"

	// TaskErrorCodePanic indicates the handler panicked
	TaskErrorCodePanic = "HANDLER_PANIC"

	// TaskErrorCodeInvalidInput indicates invalid task input
	TaskErrorCodeInvalidInput = "INVALID_INPUT"
)

// ═══════════════════════════════════════════════════════════════════════════
// Interfaces (v1 MVP)
// ═══════════════════════════════════════════════════════════════════════════

// TaskQueue handles async task submission and retrieval.
// The default implementation uses Redis lists (LPUSH/BRPOP).
type TaskQueue interface {
	// Enqueue adds a task to the queue.
	// The task's Status should be TaskStatusQueued.
	Enqueue(ctx context.Context, task *Task) error

	// Dequeue retrieves the next task from the queue.
	// Blocks until a task is available or timeout expires.
	// Returns nil, nil if timeout expires with no task.
	// Returns ErrTaskQueueEmpty if queue is empty after timeout.
	Dequeue(ctx context.Context, timeout time.Duration) (*Task, error)

	// Acknowledge marks a task as successfully processed.
	// Called after the worker completes task processing.
	Acknowledge(ctx context.Context, taskID string) error

	// Reject returns a task to the queue for retry.
	// Called when processing fails but should be retried.
	Reject(ctx context.Context, taskID string, reason string) error
}

// TaskStore persists task state and results.
// The default implementation uses Redis hashes.
type TaskStore interface {
	// Create persists a new task.
	// Implementations SHOULD wrap ErrTaskAlreadyExists when the duplicate
	// case is detected (e.g., via fmt.Errorf("%w: %s", ErrTaskAlreadyExists, id))
	// so callers can use errors.Is(err, ErrTaskAlreadyExists) for idempotent
	// paths — the Scheduler relies on this for dedup on leader failover.
	Create(ctx context.Context, task *Task) error

	// Get retrieves a task by ID.
	// Returns ErrTaskNotFound if task doesn't exist.
	Get(ctx context.Context, taskID string) (*Task, error)

	// Update persists task changes (status, progress, result).
	// Returns ErrTaskNotFound if task doesn't exist.
	Update(ctx context.Context, task *Task) error

	// Delete removes a task.
	// Used for cleanup of old tasks.
	Delete(ctx context.Context, taskID string) error

	// Cancel marks a task as cancelled.
	// Returns ErrTaskNotFound if task doesn't exist.
	// Returns ErrTaskNotCancellable if task is already in a terminal state.
	Cancel(ctx context.Context, taskID string) error
}

// TaskWorker processes tasks from the queue.
type TaskWorker interface {
	// Start begins processing tasks.
	// Blocks until ctx is cancelled or Stop is called.
	Start(ctx context.Context) error

	// Stop gracefully stops the worker.
	// Waits for in-progress tasks to complete up to shutdown timeout.
	Stop(ctx context.Context) error

	// RegisterHandler registers a handler for a task type.
	// Must be called before Start.
	RegisterHandler(taskType string, handler TaskHandler) error
}

// TaskHandler processes a specific task type.
// The handler receives:
//   - ctx: Context with trace information (from StartLinkedSpan)
//   - task: The task to process
//   - reporter: For reporting progress updates
//
// The handler should:
//  1. Process the task using task.Input
//  2. Report progress periodically via reporter
//  3. Return nil on success (result should be set via task store)
//  4. Return error on failure
type TaskHandler func(ctx context.Context, task *Task, reporter ProgressReporter) error

// ProgressReporter allows handlers to report progress.
type ProgressReporter interface {
	// Report updates task progress.
	// Progress is persisted to the TaskStore.
	Report(progress *TaskProgress) error
}

// ═══════════════════════════════════════════════════════════════════════════
// Configuration
// ═══════════════════════════════════════════════════════════════════════════

// AsyncTaskConfig configures the async task system
type AsyncTaskConfig struct {
	// QueuePrefix is the Redis key prefix for queue keys
	QueuePrefix string `json:"queue_prefix"`

	// WorkerCount is the number of concurrent workers
	WorkerCount int `json:"worker_count"`

	// DequeueTimeout is how long to wait for a task in Dequeue
	DequeueTimeout time.Duration `json:"dequeue_timeout"`

	// ShutdownTimeout is how long to wait for workers to finish on shutdown
	ShutdownTimeout time.Duration `json:"shutdown_timeout"`

	// DefaultTimeout is the default task execution timeout
	DefaultTimeout time.Duration `json:"default_timeout"`

	// ResultTTL is how long to keep completed task results
	ResultTTL time.Duration `json:"result_ttl"`
}

// DefaultAsyncTaskConfig returns sensible defaults for the async task system.
// These values are suitable for most production deployments.
func DefaultAsyncTaskConfig() AsyncTaskConfig {
	return AsyncTaskConfig{
		QueuePrefix:     "truvag3:tasks",
		WorkerCount:     5,
		DequeueTimeout:  30 * time.Second,
		ShutdownTimeout: 30 * time.Second,
		DefaultTimeout:  30 * time.Minute,
		ResultTTL:       24 * time.Hour,
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Scheduling Types and Interfaces
// ═══════════════════════════════════════════════════════════════════════════
//
// Scheduling is an optional framework feature that enables delayed and
// recurring task execution. The interfaces are defined in core so any
// storage/dispatch backend (Redis, Postgres, in-memory) can satisfy them.
// Reference implementations live in the orchestration/ package.
//
// The Scheduler component (orchestration module) reads due schedules from
// a ScheduleStore, creates tasks with deterministic IDs (for idempotency),
// and routes them to the target agent's queue via TaskDispatcher.
//
// Leader election for the Scheduler is provided by the existing
// core.DistributedLock interface — scheduling does NOT define a new lock
// interface.
//
// Multi-flow agents discriminate between scheduled task variants via a
// field inside Schedule.Input rather than via task type. The Scheduler
// always stamps materialized tasks with the fixed constant
// core.ScheduledTaskType ("truvag3.scheduled"), and the receiving agent
// registers exactly one handler under that name.

// MissedRunPolicy determines behavior when a recurring schedule's RunAt
// is in the past (e.g., the Scheduler was down for a period).
type MissedRunPolicy string

const (
	// MissedRunSkip fires once at the next tick, skipping all missed intervals.
	// Use for monitoring, health checks — missing a few is acceptable.
	MissedRunSkip MissedRunPolicy = "skip"

	// MissedRunCatchUp fires once for each missed cron interval from the
	// stored RunAt up to the current time. Use for reports, data pipelines —
	// every interval matters.
	MissedRunCatchUp MissedRunPolicy = "catchup"
)

// ScheduledTaskType is the fixed task.Type value the Scheduler stamps on
// every materialized task. The scheduled-executor validates this value
// before dispatching to the target agent's /api/v1/scheduled endpoint
// (mounted via orchestration.RegisterScheduledEndpoint).
//
// There is no per-schedule task type — the LLM and agent never need to
// agree on a routing name. Agents that need multiple distinct flows
// discriminate inside their handler via a field in Schedule.Input
// (e.g., Input["flow"]).
const ScheduledTaskType = "truvag3.scheduled"

// Schedule defines a one-shot or recurring scheduled task.
//
// One-shot: CronExpr is empty. RunAt is the absolute fire time. The
// schedule is deleted after firing.
//
// Recurring: CronExpr is set (standard 5-field cron syntax). RunAt is
// recomputed from CronExpr after each fire. The schedule persists until
// cancelled or disabled.
type Schedule struct {
	// ID is the unique identifier for this schedule.
	ID string `json:"id"`

	// Input is the payload passed to the task handler at execution time.
	// Set once at creation, delivered unchanged on every fire.
	// Multi-flow agents put a discriminator field here (e.g., Input["flow"]).
	Input map[string]interface{} `json:"input,omitempty"`

	// TargetAgent is the name of the agent that should execute this schedule
	// when it fires. The scheduled-executor will POST to this agent's
	// /api/v1/scheduled endpoint with the schedule's input as the payload.
	// The producer-side TaskDispatcher routes all scheduled tasks to a fixed
	// queue name (ScheduledExecutorQueue); TargetAgent is carried on the
	// materialized core.Task instead so the executor knows which agent to call.
	TargetAgent string `json:"target_agent"`

	// CronExpr is a standard 5-field cron expression for recurring schedules.
	// Empty string means one-shot (fire once at RunAt, then delete).
	CronExpr string `json:"cron_expr,omitempty"`

	// RunAt is the next (or only) fire time.
	// For recurring schedules: recomputed from CronExpr after each fire.
	// For one-shot schedules: the absolute fire time.
	RunAt time.Time `json:"run_at"`

	// LastRunAt is when this schedule last fired. Nil if never fired.
	LastRunAt *time.Time `json:"last_run_at,omitempty"`

	// Enabled controls whether GetDue() includes this schedule.
	// Disabled schedules are dormant — they don't fire but aren't deleted.
	Enabled bool `json:"enabled"`

	// MissedRunPolicy determines catchup behavior when RunAt is in the past.
	MissedRunPolicy MissedRunPolicy `json:"missed_run_policy"`

	// CreatedBy records which agent or system created this schedule (audit trail).
	CreatedBy string `json:"created_by"`

	// CreatedAt is when the schedule was created.
	CreatedAt time.Time `json:"created_at"`
}

// ScheduleStore persists schedule definitions.
// Implementations must be safe for concurrent access.
//
// Reference implementations live in the orchestration/ package:
//   - orchestration.RedisScheduleStore (production)
//   - orchestration.InMemoryScheduleStore (dev/test)
//
// Applications can provide their own implementation by satisfying this
// interface — for example, a Postgres-backed store in the developer's
// own package.
type ScheduleStore interface {
	// Create persists a new schedule.
	// Returns ErrScheduleAlreadyExists if a schedule with the same ID exists.
	Create(ctx context.Context, schedule *Schedule) error

	// Get retrieves a schedule by ID.
	// Returns ErrScheduleNotFound if the schedule doesn't exist.
	Get(ctx context.Context, id string) (*Schedule, error)

	// List returns all schedules.
	List(ctx context.Context) ([]*Schedule, error)

	// Update persists changes to an existing schedule.
	// Returns ErrScheduleNotFound if the schedule doesn't exist.
	Update(ctx context.Context, schedule *Schedule) error

	// Delete removes a schedule.
	// Returns ErrScheduleNotFound if the schedule doesn't exist.
	Delete(ctx context.Context, id string) error

	// GetDue returns all enabled schedules where RunAt <= now.
	// Used by the Scheduler tick loop to find schedules ready to fire.
	GetDue(ctx context.Context, now time.Time) ([]*Schedule, error)
}

// TaskDispatcher routes a task to a specific named queue.
// Used by the Scheduler to deliver materialized tasks to a target agent's
// task queue.
//
// This is intentionally a new interface, separate from TaskQueue, so that:
//   - The existing TaskQueue and RedisTaskQueue stay untouched
//   - The interface is single-method (Go: small interfaces compose better)
//   - The Scheduler depends only on what it needs (write-side)
//
// Reference implementations live in the orchestration/ package:
//   - orchestration.RedisTaskDispatcher (production — LPUSH)
//   - orchestration.InMemoryTaskDispatcher (dev — in-process channel map)
//
// The dispatcher must write to wherever the consumer reads from.
// In Redis deployments, that means LPUSH to "truvag3:tasks:queue:{name}",
// matching the existing TaskQueue convention.
type TaskDispatcher interface {
	// Dispatch delivers the task to the named queue.
	// For scheduled tasks, queueName is the fixed "scheduled-executor"
	// queue; the target agent name rides on Task.TargetAgent instead.
	Dispatch(ctx context.Context, queueName string, task *Task) error
}

// TaskHandle is a leased reference to a task returned by TaskConsumer.Consume.
// The worker MUST call exactly one of Ack or Nack before discarding the
// handle, even on panics. Failure to settle a handle is a programming error
// and may result in tasks being held in-flight indefinitely (for at-least-
// once backends) or having no effect (for at-most-once backends).
//
// For at-most-once backends (Redis BRPOP, Postgres DELETE...RETURNING), the
// task is already removed from the queue when Consume returns. Ack and Nack
// are no-ops on the transport — they still serve as compile-time documentation
// of "this dispatch attempt is over" and as hooks for metric emission.
//
// For at-least-once backends (Redis Streams XREADGROUP, Postgres SELECT FOR
// UPDATE with visibility timeout, NATS JetStream, SQS), the task stays
// claimed until explicitly settled:
//   - Ack permanently removes the task from the pending set. Call on
//     successful dispatch (HTTP 2xx from the target agent).
//   - Nack(reason) marks the task as terminally failed. The backend decides
//     where to persist it — typically a dead-letter list/table/stream keyed
//     on the reason. Call on terminal failures (max retries exhausted, 4xx
//     response, target_not_agent, etc.). The dispatch retry loop lives
//     inside the worker, NOT inside Nack — Nack is only called when retry
//     is no longer appropriate.
//
// Implementations are free to reclaim leases on dropped (never-settled)
// handles via a visibility-timeout pattern, but framework worker code always
// calls Ack or Nack explicitly and never relies on timeout-based reclaim.
//
// Handles must be safe to settle exactly once. Calling Ack or Nack a second
// time on the same handle should return a wrapped error without causing
// transport writes; the framework worker never calls them twice, but defensive
// implementations guard against it.
type TaskHandle interface {
	// Task returns the task payload. The returned pointer is valid for the
	// lifetime of the handle (until Ack or Nack is called). Callers must
	// not mutate the returned Task.
	Task() *Task

	// Ack marks the dispatch as successful and releases the handle.
	// Called by the worker after a successful HTTP 2xx response from the
	// target agent. For at-most-once backends this is a no-op on the
	// transport; for at-least-once backends it permanently removes the
	// task from the pending set.
	Ack(ctx context.Context) error

	// Nack marks the dispatch as terminally failed and releases the handle.
	// Called by the worker when retry is no longer appropriate: max retries
	// exhausted, semantic 4xx response, unknown target agent, target is a
	// tool not an agent, invalid task type, or DLQ persistence failure.
	// The reason is a short lowercase enum value (see the error_type
	// column in the executor's failure handling matrix) that the
	// implementation can use to choose a dead-letter destination.
	//
	// The dispatch retry loop lives inside the worker, not inside Nack.
	// Nack itself does not retry.
	Nack(ctx context.Context, reason string) error
}

// TaskConsumer receives leased tasks from a transport-specific source. It is
// the consumer-side counterpart of TaskDispatcher: dispatcher writes, consumer
// reads. Used by the scheduled-executor to drain tasks from whichever
// transport scheduler-tool dispatches them to.
//
// Consume blocks until a task is available, ctx is cancelled, or a transport
// error occurs. Returning (nil, nil) is permitted on graceful shutdown —
// callers should treat it as "no task; check ctx and continue."
//
// The returned TaskHandle MUST have exactly one of Ack or Nack called before
// the worker discards it.
//
// Implementations are responsible for any transport-specific blocking
// semantics (BRPOP timeout, NATS pull wait, SQS long-poll, Postgres poll
// interval) but must respect ctx cancellation.
//
// Reference implementations live in the orchestration/ package alongside
// the other Redis-backed stores (RedisTaskQueue, RedisTaskStore, etc.):
//   - orchestration.RedisTaskConsumer         (default — BRPOP-based, at-most-once)
//   - orchestration.RedisStreamsTaskConsumer  (alternative — XREADGROUP, at-least-once)
//   - orchestration.InMemoryTaskConsumer      (dev/test — channel-based)
//
// Applications can provide their own implementation by satisfying this
// interface — for example, a Postgres-backed consumer using SELECT FOR
// UPDATE SKIP LOCKED with a visibility-timeout claim column, or a NATS
// JetStream pull subscription with AckWait.
//
// Testing: the framework ships a contract test suite at
// github.com/truvaagents/truva-g3/core/conformance that verifies any
// TaskConsumer implementation against the full contract. See the godoc
// for conformance.RunTaskConsumerConformance for the exact test list and
// usage shape — your entire test file for a new backend can be ~5 lines.
type TaskConsumer interface {
	Consume(ctx context.Context, queueName string) (TaskHandle, error)
}

// ═══════════════════════════════════════════════════════════════════════════
// Helper Functions
// ═══════════════════════════════════════════════════════════════════════════

// NewTask creates a new task with the given type and input.
// Sets CreatedAt to now and Status to TaskStatusQueued.
func NewTask(id, taskType string, input map[string]interface{}) *Task {
	return &Task{
		ID:        id,
		Type:      taskType,
		Status:    TaskStatusQueued,
		Input:     input,
		CreatedAt: time.Now(),
	}
}

// NewTaskWithTimeout creates a new task with a custom timeout.
func NewTaskWithTimeout(id, taskType string, input map[string]interface{}, timeout time.Duration) *Task {
	task := NewTask(id, taskType, input)
	task.Options.Timeout = timeout
	return task
}

// SetTraceContext sets the trace context fields on a task.
// Use with telemetry.GetTraceContext(ctx) to preserve trace chain.
func (t *Task) SetTraceContext(traceID, spanID string) {
	t.TraceID = traceID
	t.ParentSpanID = spanID
}
