// Package orchestration provides telemetry helpers for async tasks.
//
// This file provides centralized functions for emitting task-related metrics
// and span events, ensuring consistent observability across the async task system.
package orchestration

import (
	"context"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// ═══════════════════════════════════════════════════════════════════════════
// Task Lifecycle Span Events
// ═══════════════════════════════════════════════════════════════════════════

// EmitTaskSubmitted emits span event and metric when a task is submitted.
func EmitTaskSubmitted(ctx context.Context, task *core.Task) {
	// Emit metric
	telemetry.Counter("truvag3.tasks.submitted",
		"task_type", task.Type,
	)

	// Emit span event
	telemetry.AddSpanEvent(ctx, "task.submitted",
		attribute.String("task_id", task.ID),
		attribute.String("task_type", task.Type),
		attribute.String("trace_id", task.TraceID),
	)
}

// EmitTaskStarted emits span event and metric when a worker starts processing a task.
func EmitTaskStarted(ctx context.Context, task *core.Task, workerID string) {
	// Emit metric
	telemetry.Counter("truvag3.tasks.started",
		"task_type", task.Type,
	)

	// Emit span event
	attrs := []attribute.KeyValue{
		attribute.String("task_id", task.ID),
		attribute.String("task_type", task.Type),
	}
	if workerID != "" {
		attrs = append(attrs, attribute.String("worker_id", workerID))
	}
	telemetry.AddSpanEvent(ctx, "task.started", attrs...)
}

// EmitTaskProgress emits span event when task progress is updated.
func EmitTaskProgress(ctx context.Context, task *core.Task, progress *core.TaskProgress) {
	if progress == nil {
		return
	}

	telemetry.AddSpanEvent(ctx, "task.progress",
		attribute.String("task_id", task.ID),
		attribute.Int("step", progress.CurrentStep),
		attribute.Int("total_steps", progress.TotalSteps),
		attribute.Float64("percentage", progress.Percentage),
		attribute.String("step_name", progress.StepName),
		attribute.String("message", progress.Message),
	)
}

// EmitTaskCompleted emits span event and metrics when a task completes successfully.
func EmitTaskCompleted(ctx context.Context, task *core.Task, duration time.Duration) {
	// Emit completion counter
	telemetry.Counter("truvag3.tasks.completed",
		"task_type", task.Type,
		"status", "completed",
	)

	// Emit duration histogram
	telemetry.Histogram("truvag3.tasks.duration_ms", float64(duration.Milliseconds()),
		"task_type", task.Type,
		"status", "completed",
	)

	// Emit span event
	telemetry.AddSpanEvent(ctx, "task.completed",
		attribute.String("task_id", task.ID),
		attribute.String("task_type", task.Type),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)
}

// EmitTaskFailed emits span event and metrics when a task fails.
func EmitTaskFailed(ctx context.Context, task *core.Task, duration time.Duration, err error) {
	errorCode := "unknown"
	if task.Error != nil {
		errorCode = task.Error.Code
	}

	// Emit completion counter with failed status
	telemetry.Counter("truvag3.tasks.completed",
		"task_type", task.Type,
		"status", "failed",
		"error_code", errorCode,
	)

	// Emit duration histogram
	telemetry.Histogram("truvag3.tasks.duration_ms", float64(duration.Milliseconds()),
		"task_type", task.Type,
		"status", "failed",
	)

	// Emit span event
	attrs := []attribute.KeyValue{
		attribute.String("task_id", task.ID),
		attribute.String("task_type", task.Type),
		attribute.Int64("duration_ms", duration.Milliseconds()),
		attribute.String("error_code", errorCode),
	}
	if err != nil {
		attrs = append(attrs, attribute.String("error", err.Error()))
	}
	telemetry.AddSpanEvent(ctx, "task.failed", attrs...)

	// Record error on span
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
	}
}

// EmitTaskCancelled emits span event and metrics when a task is cancelled.
func EmitTaskCancelled(ctx context.Context, task *core.Task, duration time.Duration) {
	// Emit completion counter with cancelled status
	telemetry.Counter("truvag3.tasks.completed",
		"task_type", task.Type,
		"status", "cancelled",
	)

	// Emit duration histogram
	if duration > 0 {
		telemetry.Histogram("truvag3.tasks.duration_ms", float64(duration.Milliseconds()),
			"task_type", task.Type,
			"status", "cancelled",
		)
	}

	// Emit span event
	telemetry.AddSpanEvent(ctx, "task.cancelled",
		attribute.String("task_id", task.ID),
		attribute.String("task_type", task.Type),
	)
}

// EmitTaskTimeout emits span event and metrics when a task times out.
func EmitTaskTimeout(ctx context.Context, task *core.Task, timeout time.Duration) {
	// Emit completion counter with timeout status
	telemetry.Counter("truvag3.tasks.completed",
		"task_type", task.Type,
		"status", "timeout",
	)

	// Emit span event
	telemetry.AddSpanEvent(ctx, "task.timeout",
		attribute.String("task_id", task.ID),
		attribute.String("task_type", task.Type),
		attribute.Int64("timeout_ms", timeout.Milliseconds()),
	)
}

// ═══════════════════════════════════════════════════════════════════════════
// Queue Metrics
// ═══════════════════════════════════════════════════════════════════════════

// EmitQueueDepth emits the current queue depth as a gauge metric.
func EmitQueueDepth(queueName string, depth int64) {
	telemetry.Gauge("truvag3.tasks.queue_depth", float64(depth),
		"queue", queueName,
	)
}

// EmitQueueWaitTime emits the time a task waited in the queue before processing.
func EmitQueueWaitTime(ctx context.Context, task *core.Task, waitTime time.Duration) {
	telemetry.Histogram("truvag3.tasks.queue_wait_ms", float64(waitTime.Milliseconds()),
		"task_type", task.Type,
	)

	telemetry.AddSpanEvent(ctx, "task.queue_wait",
		attribute.String("task_id", task.ID),
		attribute.Int64("wait_ms", waitTime.Milliseconds()),
	)
}

// ═══════════════════════════════════════════════════════════════════════════
// Worker Metrics
// ═══════════════════════════════════════════════════════════════════════════

// EmitWorkerStarted emits event when a worker starts.
func EmitWorkerStarted(workerID string, workerCount int) {
	telemetry.Counter("truvag3.tasks.worker.started",
		"worker_id", workerID,
	)
	telemetry.Gauge("truvag3.tasks.workers.active", float64(workerCount))
}

// EmitWorkerStopped emits event when a worker stops.
func EmitWorkerStopped(workerID string, workerCount int) {
	telemetry.Counter("truvag3.tasks.worker.stopped",
		"worker_id", workerID,
	)
	telemetry.Gauge("truvag3.tasks.workers.active", float64(workerCount))
}

// EmitWorkerPanic emits event when a worker encounters a panic.
func EmitWorkerPanic(ctx context.Context, workerID string, panicValue interface{}) {
	telemetry.Counter("truvag3.tasks.worker.panic",
		"worker_id", workerID,
	)

	telemetry.AddSpanEvent(ctx, "worker.panic",
		attribute.String("worker_id", workerID),
	)
}
