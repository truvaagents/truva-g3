package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// admitResumeTask follows normal task submission: persist before enqueue so
// the worker can update the record and callers can poll the returned task ID.
// Admission does not claim the checkpoint; only the worker may do that.
func admitResumeTask(ctx context.Context, queue core.TaskQueue, store core.TaskStore, task *core.Task) error {
	if err := store.Create(ctx, task); err != nil {
		return fmt.Errorf("create resume task: %w", err)
	}
	if err := queue.Enqueue(ctx, task); err != nil {
		// A disconnected submitter must not prevent bounded admission cleanup.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if cleanupErr := store.Delete(cleanupCtx, task.ID); cleanupErr != nil {
			return errors.Join(fmt.Errorf("enqueue resume task: %w", err), fmt.Errorf("remove unqueued resume task: %w", cleanupErr))
		}
		return fmt.Errorf("enqueue resume task: %w", err)
	}
	return nil
}
