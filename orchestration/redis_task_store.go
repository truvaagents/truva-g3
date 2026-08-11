// Package orchestration provides Redis-backed task store implementation.
//
// This file implements the core.TaskStore interface using Redis hashes.
// Each task is stored as a hash with the key pattern: {prefix}:task:{task_id}
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

// RedisTaskStore implements core.TaskStore using Redis hashes.
// Each task is stored as a JSON string in a key with pattern: {prefix}:task:{task_id}
type RedisTaskStore struct {
	client redis.Cmdable
	config RedisTaskStoreConfig
	logger core.Logger
}

// RedisTaskStoreConfig configures the Redis task store.
type RedisTaskStoreConfig struct {
	// KeyPrefix is the prefix for all task keys
	// Default: "truvag3:tasks"
	KeyPrefix string `json:"key_prefix"`

	// TTL is how long to keep task data after completion
	// Default: 24 hours
	TTL time.Duration `json:"ttl"`

	// Logger is an optional logger for store operations
	Logger core.Logger `json:"-"`

	// RetryAttempts is the number of retries for failed Redis operations
	// Default: 3
	RetryAttempts int `json:"retry_attempts"`

	// RetryDelay is the delay between retry attempts
	// Default: 100ms
	RetryDelay time.Duration `json:"retry_delay"`
}

// DefaultRedisTaskStoreConfig returns default configuration.
func DefaultRedisTaskStoreConfig() RedisTaskStoreConfig {
	return RedisTaskStoreConfig{
		KeyPrefix:     "truvag3:tasks",
		TTL:           24 * time.Hour,
		RetryAttempts: 3,
		RetryDelay:    100 * time.Millisecond,
	}
}

// NewRedisTaskStore creates a new Redis-backed task store.
// The client should already be connected to Redis.
func NewRedisTaskStore(client *redis.Client, config *RedisTaskStoreConfig) *RedisTaskStore {
	return NewRedisTaskStoreWithClient(client, config)
}

// NewRedisTaskStoreWithClient creates a task store using an
// application-owned Redis-compatible client.
func NewRedisTaskStoreWithClient(client redis.Cmdable, config *RedisTaskStoreConfig) *RedisTaskStore {
	if config == nil {
		defaultConfig := DefaultRedisTaskStoreConfig()
		config = &defaultConfig
	}

	// Apply defaults for unset values
	if config.KeyPrefix == "" {
		config.KeyPrefix = "truvag3:tasks"
	}
	if config.TTL <= 0 {
		config.TTL = 24 * time.Hour
	}
	if config.RetryAttempts <= 0 {
		config.RetryAttempts = 3
	}
	if config.RetryDelay <= 0 {
		config.RetryDelay = 100 * time.Millisecond
	}

	s := &RedisTaskStore{
		client: client,
		config: *config,
		logger: config.Logger,
	}

	// Apply component-aware logging if available
	if s.logger != nil {
		if cal, ok := s.logger.(core.ComponentAwareLogger); ok {
			s.logger = cal.WithComponent("framework/orchestration")
		}
	}

	return s
}

// SetLogger sets the logger for store operations.
func (s *RedisTaskStore) SetLogger(logger core.Logger) {
	if logger != nil {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			s.logger = cal.WithComponent("framework/orchestration")
		} else {
			s.logger = logger
		}
	}
}

// taskKey returns the Redis key for a task.
func (s *RedisTaskStore) taskKey(taskID string) string {
	return fmt.Sprintf("%s:task:%s", s.config.KeyPrefix, taskID)
}

// Create persists a new task.
// Returns error if task with same ID already exists.
func (s *RedisTaskStore) Create(ctx context.Context, task *core.Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}
	if task.ID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	key := s.taskKey(task.ID)

	// Serialize task to JSON
	data, err := json.Marshal(task)
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to serialize task", map[string]interface{}{
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
		return fmt.Errorf("failed to serialize task: %w", err)
	}

	// Use SETNX to ensure task doesn't already exist
	set, err := s.client.SetNX(ctx, key, data, s.config.TTL).Result()
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to create task", map[string]interface{}{
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
		return fmt.Errorf("failed to create task: %w", err)
	}

	if !set {
		return fmt.Errorf("%w: %s", core.ErrTaskAlreadyExists, task.ID)
	}

	if s.logger != nil {
		s.logger.InfoWithContext(ctx, "Task created", map[string]interface{}{
			"task_id":   task.ID,
			"task_type": task.Type,
			"status":    task.Status,
		})
	}

	return nil
}

// Get retrieves a task by ID.
// Returns core.ErrTaskNotFound if task doesn't exist.
func (s *RedisTaskStore) Get(ctx context.Context, taskID string) (*core.Task, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task ID cannot be empty")
	}

	key := s.taskKey(taskID)

	data, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, core.ErrTaskNotFound
		}
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to get task", map[string]interface{}{
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

	var task core.Task
	if err := json.Unmarshal([]byte(data), &task); err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to deserialize task", map[string]interface{}{
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
		return nil, fmt.Errorf("failed to deserialize task: %w", err)
	}

	return &task, nil
}

// Update persists task changes (status, progress, result).
// Returns core.ErrTaskNotFound if task doesn't exist.
func (s *RedisTaskStore) Update(ctx context.Context, task *core.Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}
	if task.ID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	key := s.taskKey(task.ID)

	// Check if task exists
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to check task existence", map[string]interface{}{
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
		return fmt.Errorf("failed to check task existence: %w", err)
	}
	if exists == 0 {
		return core.ErrTaskNotFound
	}

	// Serialize task to JSON
	data, err := json.Marshal(task)
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to serialize task", map[string]interface{}{
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
		return fmt.Errorf("failed to serialize task: %w", err)
	}

	// Update with TTL refresh
	if err := s.client.Set(ctx, key, data, s.config.TTL).Err(); err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to update task", map[string]interface{}{
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
		return fmt.Errorf("failed to update task: %w", err)
	}

	if s.logger != nil {
		s.logger.DebugWithContext(ctx, "Task updated", map[string]interface{}{
			"task_id": task.ID,
			"status":  task.Status,
		})
	}

	return nil
}

// Delete removes a task.
// Used for cleanup of old tasks.
func (s *RedisTaskStore) Delete(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	key := s.taskKey(taskID)

	deleted, err := s.client.Del(ctx, key).Result()
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to delete task", map[string]interface{}{
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
		return fmt.Errorf("failed to delete task: %w", err)
	}

	if deleted == 0 {
		if s.logger != nil {
			s.logger.WarnWithContext(ctx, "Task not found for deletion", map[string]interface{}{
				"task_id": taskID,
			})
		}
	} else {
		if s.logger != nil {
			s.logger.InfoWithContext(ctx, "Task deleted", map[string]interface{}{
				"task_id": taskID,
			})
		}
	}

	return nil
}

// Cancel marks a task as cancelled.
// Returns core.ErrTaskNotFound if task doesn't exist.
// Returns core.ErrTaskNotCancellable if task is already in a terminal state.
func (s *RedisTaskStore) Cancel(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	// Get current task
	task, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}

	// Check if task can be cancelled
	if task.Status.IsTerminal() {
		if s.logger != nil {
			s.logger.WarnWithContext(ctx, "Cannot cancel task in terminal state", map[string]interface{}{
				"task_id": taskID,
				"status":  task.Status,
			})
		}
		return core.ErrTaskNotCancellable
	}

	// Update task status
	now := time.Now()
	task.Status = core.TaskStatusCancelled
	task.CancelledAt = &now
	task.Error = &core.TaskError{
		Code:    core.TaskErrorCodeCancelled,
		Message: "Task was cancelled by request",
	}

	if err := s.Update(ctx, task); err != nil {
		return err
	}

	if s.logger != nil {
		s.logger.InfoWithContext(ctx, "Task cancelled", map[string]interface{}{
			"task_id": taskID,
		})
	}

	return nil
}

// ListByStatus returns all tasks with the given status.
// Useful for monitoring and admin operations.
// Note: This scans all keys with the prefix, so use sparingly in production.
func (s *RedisTaskStore) ListByStatus(ctx context.Context, status core.TaskStatus) ([]*core.Task, error) {
	pattern := fmt.Sprintf("%s:task:*", s.config.KeyPrefix)

	var tasks []*core.Task
	var cursor uint64

	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to scan tasks: %w", err)
		}

		for _, key := range keys {
			data, err := s.client.Get(ctx, key).Result()
			if err != nil {
				continue // Skip tasks that disappeared
			}

			var task core.Task
			if err := json.Unmarshal([]byte(data), &task); err != nil {
				continue // Skip invalid tasks
			}

			if task.Status == status {
				tasks = append(tasks, &task)
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return tasks, nil
}

// Close performs any cleanup needed.
// Note: Does not close the Redis client as it may be shared.
func (s *RedisTaskStore) Close() error {
	// No cleanup needed - Redis client is managed externally
	return nil
}
