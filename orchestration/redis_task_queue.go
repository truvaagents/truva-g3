// Package orchestration provides Redis-backed task queue implementation.
//
// This file implements the core.TaskQueue interface using Redis lists.
// Tasks are added with LPUSH and retrieved with BRPOP for reliable FIFO
// processing with blocking wait support.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

const defaultTaskQueueRecoveryTimeout = 5 * time.Second

// RedisTaskQueue implements core.TaskQueue using a Redis queue list plus a list of
// dequeued tasks awaiting settlement. Enqueue/Dequeue retain FIFO behavior;
// Acknowledge removes the in-flight record and Reject requeues it.
type RedisTaskQueue struct {
	client redis.Cmdable
	config RedisTaskQueueConfig
	logger core.Logger
}

// RedisTaskQueueConfig configures the Redis task queue.
type RedisTaskQueueConfig struct {
	// QueueKey is the Redis key for the task queue list
	// Default: "truvag3:tasks:queue"
	QueueKey string `json:"queue_key"`

	// ProcessingKey is the Redis list key for tasks awaiting acknowledgement.
	// Default: "truvag3:tasks:processing", service-scoped when EnvServiceName is set.
	ProcessingKey string `json:"processing_key"`

	// CircuitBreaker is an optional circuit breaker for Redis operations
	// If nil, operations are executed directly without circuit breaker protection
	CircuitBreaker core.CircuitBreaker `json:"-"`

	// Logger is an optional logger for queue operations
	Logger core.Logger `json:"-"`

	// RetryAttempts is the number of retries for failed Redis operations
	// Default: 3
	RetryAttempts int `json:"retry_attempts"`

	// RetryDelay is the delay between retry attempts
	// Default: 100ms
	RetryDelay time.Duration `json:"retry_delay"`
}

// DefaultRedisTaskQueueConfig returns default configuration.
// The queue and processing keys are auto-namespaced by
// TRUVAG3_K8S_SERVICE_NAME to prevent cross-agent task stealing or settlement
// collisions when multiple agents share a Redis instance.
// Precedence: explicit QueueKey > TRUVAG3_K8S_SERVICE_NAME > hardcoded default.
// Uses core.EnvServiceName constant — single source of truth per design principles §3.3.
func DefaultRedisTaskQueueConfig() RedisTaskQueueConfig {
	queueKey := "truvag3:tasks:queue"
	processingKey := "truvag3:tasks:processing"
	if svc := os.Getenv(core.EnvServiceName); svc != "" {
		queueKey = fmt.Sprintf("truvag3:tasks:queue:%s", svc)
		processingKey = fmt.Sprintf("truvag3:tasks:processing:%s", svc)
	}
	return RedisTaskQueueConfig{
		QueueKey:      queueKey,
		ProcessingKey: processingKey,
		RetryAttempts: 3,
		RetryDelay:    100 * time.Millisecond,
	}
}

// NewRedisTaskQueue creates a new Redis-backed task queue.
// The client should already be connected to Redis.
func NewRedisTaskQueue(client *redis.Client, config *RedisTaskQueueConfig) *RedisTaskQueue {
	return NewRedisTaskQueueWithClient(client, config)
}

// NewRedisTaskQueueWithClient creates a task queue using an
// application-owned Redis-compatible client.
func NewRedisTaskQueueWithClient(client redis.Cmdable, config *RedisTaskQueueConfig) *RedisTaskQueue {
	if config == nil {
		defaultConfig := DefaultRedisTaskQueueConfig()
		config = &defaultConfig
	}

	// Apply defaults for unset values — mirror DefaultRedisTaskQueueConfig env logic.
	explicitQueueKey := config.QueueKey != ""
	if config.QueueKey == "" {
		queueSource := "default"
		if svc := os.Getenv(core.EnvServiceName); svc != "" {
			config.QueueKey = fmt.Sprintf("truvag3:tasks:queue:%s", svc)
			queueSource = core.EnvServiceName
		} else {
			config.QueueKey = "truvag3:tasks:queue"
		}
		if config.Logger != nil {
			config.Logger.Debug("Task queue key resolved", map[string]interface{}{
				"operation": "task_queue_init",
				"queue_key": config.QueueKey,
				"source":    queueSource,
			})
		}
	}
	if config.ProcessingKey == "" {
		if explicitQueueKey {
			config.ProcessingKey = config.QueueKey + ":processing"
		} else if svc := os.Getenv(core.EnvServiceName); svc != "" {
			config.ProcessingKey = fmt.Sprintf("truvag3:tasks:processing:%s", svc)
		} else {
			config.ProcessingKey = "truvag3:tasks:processing"
		}
	}
	if config.RetryAttempts <= 0 {
		config.RetryAttempts = 3
	}
	if config.RetryDelay <= 0 {
		config.RetryDelay = 100 * time.Millisecond
	}

	q := &RedisTaskQueue{
		client: client,
		config: *config,
		logger: config.Logger,
	}

	// Apply component-aware logging if available
	if q.logger != nil {
		if cal, ok := q.logger.(core.ComponentAwareLogger); ok {
			q.logger = cal.WithComponent("framework/orchestration")
		}
	}

	return q
}

// SetLogger sets the logger for queue operations.
func (q *RedisTaskQueue) SetLogger(logger core.Logger) {
	if logger != nil {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			q.logger = cal.WithComponent("framework/orchestration")
		} else {
			q.logger = logger
		}
	}
}

// Enqueue adds a task to the queue.
// Uses LPUSH to add to the left side of the list.
func (q *RedisTaskQueue) Enqueue(ctx context.Context, task *core.Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}
	if task.ID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	// Serialize task to JSON
	data, err := json.Marshal(task)
	if err != nil {
		if q.logger != nil {
			q.logger.ErrorWithContext(ctx, "Failed to serialize task", map[string]interface{}{
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
		return fmt.Errorf("failed to serialize task: %w", err)
	}

	// Execute with retries
	var lastErr error
	for attempt := 0; attempt < q.config.RetryAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(q.config.RetryDelay)
		}

		err = q.enqueueWithCircuitBreaker(ctx, data)
		if err == nil {
			if q.logger != nil {
				q.logger.InfoWithContext(ctx, "Task enqueued", map[string]interface{}{
					"task_id":   task.ID,
					"task_type": task.Type,
					"queue_key": q.config.QueueKey,
				})
			}
			return nil
		}

		lastErr = err
		if q.logger != nil {
			q.logger.WarnWithContext(ctx, "Enqueue attempt failed", map[string]interface{}{
				"task_id": task.ID,
				"attempt": attempt + 1,
				"error":   err.Error(),
			})
		}
	}

	if q.logger != nil {
		q.logger.ErrorWithContext(ctx, "Failed to enqueue task after retries", map[string]interface{}{
			"task_id":  task.ID,
			"attempts": q.config.RetryAttempts,
			"error":    lastErr.Error(),
		})
	}

	return fmt.Errorf("failed to enqueue task after %d attempts: %w", q.config.RetryAttempts, lastErr)
}

// enqueueWithCircuitBreaker executes the enqueue operation with optional circuit breaker.
func (q *RedisTaskQueue) enqueueWithCircuitBreaker(ctx context.Context, data []byte) error {
	if q.config.CircuitBreaker != nil {
		return q.config.CircuitBreaker.Execute(ctx, func() error {
			return q.client.LPush(ctx, q.config.QueueKey, data).Err()
		})
	}
	return q.client.LPush(ctx, q.config.QueueKey, data).Err()
}

// Dequeue retrieves the next task from the queue.
// Blocks until a task is available or timeout expires.
// Returns nil, nil if timeout expires with no task.
func (q *RedisTaskQueue) Dequeue(ctx context.Context, timeout time.Duration) (*core.Task, error) {
	// Use BRPOP to block until a task is available
	result, err := q.client.BRPop(ctx, timeout, q.config.QueueKey).Result()
	if err != nil {
		if err == redis.Nil {
			// Timeout expired, no task available
			return nil, nil
		}
		if ctx.Err() != nil {
			// Context cancelled
			return nil, ctx.Err()
		}
		if q.logger != nil {
			q.logger.ErrorWithContext(ctx, "Failed to dequeue task", map[string]interface{}{
				"error":     err.Error(),
				"queue_key": q.config.QueueKey,
			})
		}
		return nil, fmt.Errorf("failed to dequeue task: %w", err)
	}

	// BRPOP returns [key, value], we want the value
	if len(result) < 2 {
		return nil, fmt.Errorf("unexpected BRPOP result format")
	}

	// Deserialize task
	var task core.Task
	if err := json.Unmarshal([]byte(result[1]), &task); err != nil {
		if q.logger != nil {
			q.logger.ErrorWithContext(ctx, "Failed to deserialize task", map[string]interface{}{
				"error": err.Error(),
				"data":  result[1],
			})
		}
		return nil, fmt.Errorf("failed to deserialize task: %w", err)
	}
	if task.ID == "" {
		return nil, fmt.Errorf("dequeued task ID cannot be empty")
	}
	// Retain the payload so Acknowledge and Reject work across processes. Keep
	// the established list representation of ProcessingKey, and intentionally
	// avoid a multi-key command so Redis Cluster keys need not share a slot.
	if err := q.client.LPush(ctx, q.config.ProcessingKey, result[1]).Err(); err != nil {
		// Best-effort recovery: the queue pop already succeeded, so put the task
		// back before reporting the tracking failure.
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTaskQueueRecoveryTimeout)
		defer cancel()
		_ = q.client.RPush(recoveryCtx, q.config.QueueKey, result[1]).Err()
		return nil, fmt.Errorf("failed to track dequeued task: %w", err)
	}

	if q.logger != nil {
		q.logger.InfoWithContext(ctx, "Task dequeued", map[string]interface{}{
			"task_id":   task.ID,
			"task_type": task.Type,
		})
	}

	return &task, nil
}

// Acknowledge marks a task as successfully processed by removing its in-flight
// payload. It is idempotent so cleanup paths may safely retry it.
func (q *RedisTaskQueue) Acknowledge(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	if q.logger != nil {
		q.logger.DebugWithContext(ctx, "Task acknowledged", map[string]interface{}{
			"task_id": taskID,
		})
	}

	payload, found, err := q.inFlightPayload(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to load acknowledged task: %w", err)
	}
	if !found {
		return nil
	}
	if err := q.client.LRem(ctx, q.config.ProcessingKey, 1, payload).Err(); err != nil {
		return fmt.Errorf("failed to acknowledge task: %w", err)
	}
	return nil
}

// Reject returns a task to the front of the queue for retry.
func (q *RedisTaskQueue) Reject(ctx context.Context, taskID string, reason string) error {
	if taskID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	if q.logger != nil {
		q.logger.WarnWithContext(ctx, "Task rejected", map[string]interface{}{
			"task_id": taskID,
			"reason":  reason,
		})
	}

	payload, found, err := q.inFlightPayload(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to load rejected task: %w", err)
	}
	if !found {
		return fmt.Errorf("task %s is not awaiting acknowledgement", taskID)
	}
	// Enqueue first so a transient cleanup failure cannot lose the task. RPush
	// places the retry at the side consumed by BRPOP, ahead of newer LPUSHes.
	if err := q.client.RPush(ctx, q.config.QueueKey, payload).Err(); err != nil {
		return fmt.Errorf("failed to requeue rejected task: %w", err)
	}
	if err := q.client.LRem(ctx, q.config.ProcessingKey, 1, payload).Err(); err != nil {
		return fmt.Errorf("requeued rejected task but failed to clear in-flight record: %w", err)
	}
	return nil
}

func (q *RedisTaskQueue) inFlightPayload(ctx context.Context, taskID string) (string, bool, error) {
	payloads, err := q.client.LRange(ctx, q.config.ProcessingKey, 0, -1).Result()
	if err != nil {
		return "", false, err
	}
	for _, payload := range payloads {
		var task struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(payload), &task); err == nil && task.ID == taskID {
			return payload, true, nil
		}
	}
	return "", false, nil
}

// QueueLength returns the current number of tasks in the queue.
// Useful for monitoring and metrics.
func (q *RedisTaskQueue) QueueLength(ctx context.Context) (int64, error) {
	length, err := q.client.LLen(ctx, q.config.QueueKey).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get queue length: %w", err)
	}
	return length, nil
}

// Close performs any cleanup needed.
// Note: Does not close the Redis client as it may be shared.
func (q *RedisTaskQueue) Close() error {
	// No cleanup needed - Redis client is managed externally
	return nil
}
