package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

type Worker struct {
	workflow orchestration.StateStore
	consumer core.TaskConsumer
	queue    string
	logger   core.Logger
}

var _ core.Runnable = (*Worker)(nil)

func NewWorker(backends WorkerBackends, logger core.Logger) (*Worker, error) {
	if backends.Workflow == nil || backends.Consumer == nil {
		return nil, fmt.Errorf("live portability: worker workflow and consumer backends are required")
	}
	if logger == nil {
		return nil, fmt.Errorf("live portability: logger is required")
	}
	return &Worker{
		workflow: backends.Workflow,
		consumer: backends.Consumer,
		queue:    backends.Queue,
		logger:   logger,
	}, nil
}

func (worker *Worker) Start(ctx context.Context) error {
	worker.logger.Info("portable worker started", map[string]interface{}{"queue": worker.queue})
	for ctx.Err() == nil {
		handle, err := worker.consumer.Consume(ctx, worker.queue)
		if err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return nil
			}
			worker.logger.Error("consume failed", map[string]interface{}{"error": err.Error()})
			if !waitForRetry(ctx) {
				break
			}
			continue
		}
		if handle == nil {
			continue
		}
		if err := worker.process(ctx, handle); err != nil {
			taskID := ""
			if task := handle.Task(); task != nil {
				taskID = task.ID
			}
			worker.logger.Error("task processing failed", map[string]interface{}{
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
	}
	return nil
}

func (worker *Worker) process(ctx context.Context, handle core.TaskHandle) error {
	task := handle.Task()
	if task == nil || strings.TrimSpace(task.ID) == "" {
		return handle.Nack(ctx, "invalid_task")
	}
	execution, err := worker.workflow.GetExecution(ctx, task.ID)
	if err != nil {
		// Leave the claim unsettled so transient PostgreSQL failures redeliver.
		return fmt.Errorf("load PostgreSQL execution: %w", err)
	}
	if execution.Status == orchestration.ExecutionCompleted {
		return handle.Ack(ctx)
	}
	if task.Type != "portable-weather" {
		return worker.fail(ctx, handle, execution, "invalid_task_type", fmt.Errorf("unexpected task type %q", task.Type))
	}
	location, ok := task.Input["location"].(string)
	if !ok || strings.TrimSpace(location) == "" {
		return worker.fail(ctx, handle, execution, "invalid_input", fmt.Errorf("location is required"))
	}

	execution.Status = orchestration.ExecutionRunning
	if err := worker.workflow.UpdateExecution(ctx, execution); err != nil {
		return fmt.Errorf("mark PostgreSQL execution running: %w", err)
	}

	finished := time.Now().UTC()
	execution.Status = orchestration.ExecutionCompleted
	execution.EndTime = &finished
	execution.Outputs = map[string]interface{}{
		"location": location,
		"summary":  fmt.Sprintf("portable task %s completed for %s", task.ID, location),
	}
	if err := worker.workflow.UpdateExecution(ctx, execution); err != nil {
		// Persist before Ack. A redelivery is safe because completed executions
		// are acknowledged without invoking the tools a second time.
		return fmt.Errorf("complete PostgreSQL execution: %w", err)
	}
	if err := handle.Ack(ctx); err != nil {
		return fmt.Errorf("acknowledge NATS task: %w", err)
	}
	worker.logger.Info("portable task completed", map[string]interface{}{
		"task_id":  task.ID,
		"location": location,
	})
	return nil
}

func (worker *Worker) fail(
	ctx context.Context,
	handle core.TaskHandle,
	execution *orchestration.WorkflowExecution,
	reason string,
	cause error,
) error {
	finished := time.Now().UTC()
	execution.Status = orchestration.ExecutionFailed
	execution.EndTime = &finished
	execution.Outputs = map[string]interface{}{
		"error":  cause.Error(),
		"reason": reason,
	}
	if err := worker.workflow.UpdateExecution(ctx, execution); err != nil {
		// Preserve at-least-once recovery if PostgreSQL is temporarily down.
		return fmt.Errorf("persist PostgreSQL failure: %w", err)
	}
	if err := handle.Nack(ctx, reason); err != nil {
		return fmt.Errorf("dead-letter NATS task after %v: %w", cause, err)
	}
	return cause
}

func waitForRetry(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
