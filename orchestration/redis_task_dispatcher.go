// Package orchestration — RedisTaskDispatcher implements core.TaskDispatcher.
//
// Writes tasks using LPUSH to the versioned deployment-scoped queue keys shared
// by RedisTaskQueue and RedisTaskConsumer. Composition derives the task prefix
// from core.RedisKeyspace.Plain("tasks"); producer and consumer must use the
// same deployment and logical queue name.

package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time check: RedisTaskDispatcher satisfies core.TaskDispatcher.
var _ core.TaskDispatcher = (*RedisTaskDispatcher)(nil)

// taskQueueKeyPrefix is the per-agent task queue key namespace. This
// matches RedisTaskQueue's default "truvag3:v1:default:tasks:queue:<name>" format
// so scheduled tasks are picked up by existing worker pools without any
// special routing.
var taskQueueKeyPrefix = defaultRedisKeyspace().Plain("tasks", "queue") + ":"

// RedisTaskDispatcher is a Redis-backed implementation of core.TaskDispatcher.
// Uses LPUSH to write tasks to per-agent queue lists.
//
// Accepts redis.Cmdable (rather than the concrete *redis.Client) so tests
// can inject miniredis clients and production can use *redis.ClusterClient
// transparently — matching the pattern established by memory.RedisDistributedLock.
type RedisTaskDispatcher struct {
	client      redis.Cmdable
	queuePrefix string
	idPrefix    string
}

// NewRedisTaskDispatcher creates a new Redis-backed task dispatcher.
//
// Returns errNilRedisClient if client is nil — consistent with the error-
// return pattern in memory.NewRedisDistributedLock. The scheduler-tool's
// main.go should propagate this via log.Fatal during startup.
func NewRedisTaskDispatcher(client redis.Cmdable) (*RedisTaskDispatcher, error) {
	return NewRedisTaskDispatcherWithPrefix(client, defaultRedisKeyspace().Plain("tasks"))
}

// NewRedisTaskDispatcherWithPrefix accepts an explicit task-routing prefix.
// Canonical composition supplies keyspace.Plain("tasks"). The caller owns client.
func NewRedisTaskDispatcherWithPrefix(client redis.Cmdable, prefix string) (*RedisTaskDispatcher, error) {
	if client == nil {
		return nil, errNilRedisClient
	}
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), ":")
	if prefix == "" {
		return nil, fmt.Errorf("orchestration: task dispatcher prefix is required")
	}
	return &RedisTaskDispatcher{client: client, queuePrefix: prefix + ":queue:", idPrefix: prefix + ":id:"}, nil
}

// Dispatch delivers the task to the named queue via LPUSH.
//
// The task is JSON-marshalled and pushed onto the left of the list at
// "<task-prefix>:queue:<queueName>". A TaskWorkerPool BRPOPing on the same
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
	idKey := d.idPrefix + task.ID
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

	key := d.queuePrefix + queueName
	if err := d.client.LPush(ctx, key, data).Err(); err != nil {
		return fmt.Errorf("scheduler: failed to dispatch to %s: %w", key, err)
	}
	return nil
}
