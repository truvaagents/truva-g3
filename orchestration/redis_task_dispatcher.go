// Package orchestration — RedisTaskDispatcher implements core.TaskDispatcher.
//
// Writes tasks to "truvag3:tasks:queue:{queueName}" using LPUSH. This matches
// the key convention used by the existing orchestration.RedisTaskQueue so
// the existing TaskWorkerPool's BRPOP picks up scheduled tasks without any
// worker-side changes.
//
// DRIFT RISK — DUPLICATED CONVENTION (not a shared constant):
// The "truvag3:tasks:queue:" prefix is also hardcoded in
// orchestration/redis_task_queue.go (as a string literal inside
// DefaultRedisTaskQueueConfig). Both modules must use the same prefix or
// dispatched tasks will land in a key that worker pools aren't reading.
//
// Why it's duplicated rather than shared via core: exporting a constant
// from core would require updating orchestration/redis_task_queue.go to
// reference it, which touches pre-existing tech-debt code outside the
// scope of this phase. A future tech-debt cleanup should consolidate the
// convention into a single exported constant.
//
// If you change the prefix below, grep for "truvag3:tasks:queue" across the
// repo to find the other hardcoded location and update it too.

package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time check: RedisTaskDispatcher satisfies core.TaskDispatcher.
var _ core.TaskDispatcher = (*RedisTaskDispatcher)(nil)

// taskQueueKeyPrefix is the per-agent task queue key namespace. This
// matches orchestration.RedisTaskQueue's default key format ("truvag3:tasks:queue:{name}")
// so scheduled tasks are picked up by existing worker pools without any
// special routing.
const taskQueueKeyPrefix = "truvag3:tasks:queue:"

// RedisTaskDispatcher is a Redis-backed implementation of core.TaskDispatcher.
// Uses LPUSH to write tasks to per-agent queue lists.
//
// Accepts redis.Cmdable (rather than the concrete *redis.Client) so tests
// can inject miniredis clients and production can use *redis.ClusterClient
// transparently — matching the pattern established by memory.RedisDistributedLock.
type RedisTaskDispatcher struct {
	client redis.Cmdable
}

// NewRedisTaskDispatcher creates a new Redis-backed task dispatcher.
//
// Returns errNilRedisClient if client is nil — consistent with the error-
// return pattern in memory.NewRedisDistributedLock. The scheduler-tool's
// main.go should propagate this via log.Fatal during startup.
func NewRedisTaskDispatcher(client redis.Cmdable) (*RedisTaskDispatcher, error) {
	if client == nil {
		return nil, errNilRedisClient
	}
	return &RedisTaskDispatcher{client: client}, nil
}

// Dispatch delivers the task to the named queue via LPUSH.
//
// The task is JSON-marshalled and pushed onto the left of the list at
// "truvag3:tasks:queue:{queueName}". A TaskWorkerPool BRPOPing on the same
// key picks it up and runs the registered handler.
func (d *RedisTaskDispatcher) Dispatch(ctx context.Context, queueName string, task *core.Task) error {
	if task == nil {
		return errNilTask
	}
	if task.ID == "" {
		return errEmptyTaskID
	}
	if queueName == "" {
		return errEmptyQueueName
	}

	// Idempotency: SETNX on a per-task-ID key. The Scheduler materializes
	// tasks with deterministic IDs for leader-failover dedup — a second
	// Dispatch of the same ID must return core.ErrTaskAlreadyExists.
	idKey := "truvag3:tasks:id:" + task.ID
	ok, err := d.client.SetNX(ctx, idKey, "1", 24*time.Hour).Result()
	if err != nil {
		return fmt.Errorf("scheduler: idempotency check: %w", err)
	}
	if !ok {
		return core.ErrTaskAlreadyExists
	}

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("scheduler: failed to marshal task: %w", err)
	}

	key := taskQueueKeyPrefix + queueName
	if err := d.client.LPush(ctx, key, data).Err(); err != nil {
		return fmt.Errorf("scheduler: failed to dispatch to %s: %w", key, err)
	}
	return nil
}
