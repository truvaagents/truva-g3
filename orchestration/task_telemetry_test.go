package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

func init() {
	// Set up noop tracer for all telemetry tests
	otel.SetTracerProvider(noop.NewTracerProvider())
}

func TestEmitTaskSubmitted(t *testing.T) {
	ctx := context.Background()
	task := core.NewTask("test-task-1", "orchestration", map[string]interface{}{
		"query": "test",
	})

	// Should not panic
	EmitTaskSubmitted(ctx, task)
}

func TestEmitTaskStarted(t *testing.T) {
	ctx := context.Background()
	task := core.NewTask("test-task-2", "research", nil)

	tests := []struct {
		name     string
		workerID string
	}{
		{"with worker ID", "worker-1"},
		{"empty worker ID", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic
			EmitTaskStarted(ctx, task, tt.workerID)
		})
	}
}

func TestEmitTaskProgress(t *testing.T) {
	ctx := context.Background()
	task := core.NewTask("test-task-3", "test", nil)

	tests := []struct {
		name     string
		progress *core.TaskProgress
	}{
		{
			name:     "nil progress",
			progress: nil,
		},
		{
			name: "valid progress",
			progress: &core.TaskProgress{
				CurrentStep: 1,
				TotalSteps:  3,
				StepName:    "Processing",
				Percentage:  33.3,
				Message:     "In progress",
			},
		},
		{
			name: "progress without step name",
			progress: &core.TaskProgress{
				CurrentStep: 2,
				TotalSteps:  5,
				Percentage:  40.0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic
			EmitTaskProgress(ctx, task, tt.progress)
		})
	}
}

func TestEmitTaskCompleted(t *testing.T) {
	ctx := context.Background()
	task := core.NewTask("test-task-4", "test", nil)
	task.Status = core.TaskStatusCompleted

	// Should not panic
	EmitTaskCompleted(ctx, task, 5*time.Second)
	EmitTaskCompleted(ctx, task, 0)
	EmitTaskCompleted(ctx, task, 100*time.Millisecond)
}

func TestEmitTaskFailed(t *testing.T) {
	ctx := context.Background()
	task := core.NewTask("test-task-5", "test", nil)
	task.Status = core.TaskStatusFailed

	tests := []struct {
		name      string
		taskError *core.TaskError
		err       error
	}{
		{
			name:      "nil error",
			taskError: nil,
			err:       nil,
		},
		{
			name: "with task error",
			taskError: &core.TaskError{
				Code:    core.TaskErrorCodeHandlerError,
				Message: "handler failed",
				Details: "stack trace",
			},
			err: errors.New("handler failed"),
		},
		{
			name:      "with only error",
			taskError: nil,
			err:       errors.New("some error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task.Error = tt.taskError
			// Should not panic
			EmitTaskFailed(ctx, task, 2*time.Second, tt.err)
		})
	}
}

func TestEmitTaskCancelled(t *testing.T) {
	ctx := context.Background()
	task := core.NewTask("test-task-6", "test", nil)
	task.Status = core.TaskStatusCancelled

	// Should not panic with various durations
	EmitTaskCancelled(ctx, task, 5*time.Second)
	EmitTaskCancelled(ctx, task, 0) // Zero duration
	EmitTaskCancelled(ctx, task, 100*time.Millisecond)
}

func TestEmitTaskTimeout(t *testing.T) {
	ctx := context.Background()
	task := core.NewTask("test-task-7", "test", nil)
	task.Status = core.TaskStatusFailed

	// Should not panic
	EmitTaskTimeout(ctx, task, 30*time.Second)
	EmitTaskTimeout(ctx, task, 5*time.Minute)
}

func TestEmitQueueDepth(t *testing.T) {
	// Should not panic
	EmitQueueDepth("default", 10)
	EmitQueueDepth("high-priority", 0)
	EmitQueueDepth("low-priority", 100)
}

func TestEmitQueueWaitTime(t *testing.T) {
	ctx := context.Background()
	task := core.NewTask("test-task-8", "test", nil)

	// Should not panic
	EmitQueueWaitTime(ctx, task, 5*time.Second)
	EmitQueueWaitTime(ctx, task, 0)
	EmitQueueWaitTime(ctx, task, 100*time.Millisecond)
}

func TestEmitWorkerStarted(t *testing.T) {
	// Should not panic
	EmitWorkerStarted("worker-1", 1)
	EmitWorkerStarted("worker-2", 2)
	EmitWorkerStarted("worker-5", 5)
}

func TestEmitWorkerStopped(t *testing.T) {
	// Should not panic
	EmitWorkerStopped("worker-1", 4)
	EmitWorkerStopped("worker-2", 3)
	EmitWorkerStopped("worker-5", 0)
}

func TestEmitWorkerPanic(t *testing.T) {
	ctx := context.Background()

	// Should not panic (ironic, I know)
	EmitWorkerPanic(ctx, "worker-1", "test panic")
	EmitWorkerPanic(ctx, "worker-2", errors.New("panic error"))
	EmitWorkerPanic(ctx, "worker-3", nil)
}
