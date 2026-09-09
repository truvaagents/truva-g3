// Package orchestration provides Redis-backed task store implementation.
//
// This file implements the core.TaskStore interface using Redis hashes.
// Each task is stored as a hash with the key pattern: {prefix}:task:{task_id}
package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// RedisTaskStore implements core.TaskStore using Redis hashes.
// Each task is stored as a JSON string in a key with pattern: {prefix}:task:{task_id}
type RedisTaskStore struct {
	client redis.Cmdable
	config RedisTaskStoreConfig
	logger core.Logger

	reconcileMu      sync.Mutex
	reconcileCursors map[string]uint64
	reconcilePending map[string][]string
	reconcileNext    int
}

// TaskIndexUpdateError reports that an authoritative task mutation succeeded
// but one or more repairable status projections did not.
type TaskIndexUpdateError struct {
	TaskID string
	Cause  error
}

func (err *TaskIndexUpdateError) Error() string { return "task saved but status index update failed" }
func (err *TaskIndexUpdateError) Unwrap() error { return err.Cause }

// RedisTaskStoreConfig configures the Redis task store.
type RedisTaskStoreConfig struct {
	// KeyPrefix is the prefix for all task keys
	// Default: "truvag3:v1:default:tasks". Canonical composition uses keyspace.Plain("tasks").
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
		KeyPrefix:     defaultRedisKeyspace().Plain("tasks"),
		TTL:           24 * time.Hour,
		RetryAttempts: 3,
		RetryDelay:    100 * time.Millisecond,
	}
}

// NewRedisTaskStore creates a task store using an application-owned
// standalone, Sentinel, or cluster command client.
func NewRedisTaskStore(client redis.Cmdable, config *RedisTaskStoreConfig) *RedisTaskStore {
	if config == nil {
		defaultConfig := DefaultRedisTaskStoreConfig()
		config = &defaultConfig
	}

	// Apply defaults for unset values
	if config.KeyPrefix == "" {
		config.KeyPrefix = defaultRedisKeyspace().Plain("tasks")
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

	logger := config.Logger
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	s := &RedisTaskStore{
		client:           client,
		config:           *config,
		logger:           logger,
		reconcileCursors: make(map[string]uint64),
		reconcilePending: make(map[string][]string),
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

func (s *RedisTaskStore) statusKey(status core.TaskStatus) string {
	return fmt.Sprintf("%s:index:status:%s", s.config.KeyPrefix, status)
}

func (s *RedisTaskStore) allKey() string { return s.config.KeyPrefix + ":index:all" }

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
				"operation":  "task_create",
				"request_id": core.GetRequestID(ctx),
				"task_id":    task.ID,
				"error":      "task serialization failed",
				"error_type": "marshal",
			})
		}
		return fmt.Errorf("failed to serialize task: %w", err)
	}

	// Use SETNX to ensure task doesn't already exist
	set, err := s.client.SetNX(ctx, key, data, s.config.TTL).Result()
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to create task", map[string]interface{}{
				"operation":  "task_create",
				"request_id": core.GetRequestID(ctx),
				"task_id":    task.ID,
				"error":      "redis task write failed",
				"error_type": "backend_write",
			})
		}
		return fmt.Errorf("failed to create task: %w", err)
	}

	if !set {
		return fmt.Errorf("%w: %s", core.ErrTaskAlreadyExists, task.ID)
	}
	if err := s.updateStatusIndex(ctx, task.ID, "", task.Status, true); err != nil {
		return err
	}

	if s.logger != nil {
		s.logger.InfoWithContext(ctx, "Task created", map[string]interface{}{
			"operation":  "task_create",
			"request_id": core.GetRequestID(ctx),
			"task_id":    task.ID,
			"task_type":  task.Type,
			"status":     task.Status,
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
				"operation":  "task_get",
				"request_id": core.GetRequestID(ctx),
				"task_id":    taskID,
				"error":      "redis task read failed",
				"error_type": "backend_read",
			})
		}
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

	var task core.Task
	if err := json.Unmarshal([]byte(data), &task); err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to deserialize task", map[string]interface{}{
				"operation":  "task_get",
				"request_id": core.GetRequestID(ctx),
				"task_id":    taskID,
				"error":      "stored task is malformed",
				"error_type": "unmarshal",
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
	previous, err := s.Get(ctx, task.ID)
	if err != nil {
		return err
	}

	// Serialize task to JSON
	data, err := json.Marshal(task)
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to serialize task", map[string]interface{}{
				"operation":  "task_update",
				"request_id": core.GetRequestID(ctx),
				"task_id":    task.ID,
				"error":      "task serialization failed",
				"error_type": "marshal",
			})
		}
		return fmt.Errorf("failed to serialize task: %w", err)
	}

	// Update with TTL refresh
	if err := s.client.Set(ctx, key, data, s.config.TTL).Err(); err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to update task", map[string]interface{}{
				"operation":  "task_update",
				"request_id": core.GetRequestID(ctx),
				"task_id":    task.ID,
				"error":      "redis task write failed",
				"error_type": "backend_write",
			})
		}
		return fmt.Errorf("failed to update task: %w", err)
	}
	if err := s.updateStatusIndex(ctx, task.ID, previous.Status, task.Status, true); err != nil {
		return err
	}

	if s.logger != nil {
		s.logger.DebugWithContext(ctx, "Task updated", map[string]interface{}{
			"operation":  "task_update",
			"request_id": core.GetRequestID(ctx),
			"task_id":    task.ID,
			"status":     task.Status,
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
	previous, getErr := s.Get(ctx, taskID)
	if getErr != nil && !errors.Is(getErr, core.ErrTaskNotFound) {
		return getErr
	}

	deleted, err := s.client.Del(ctx, key).Result()
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to delete task", map[string]interface{}{
				"operation":  "task_delete",
				"request_id": core.GetRequestID(ctx),
				"task_id":    taskID,
				"error":      "redis task deletion failed",
				"error_type": "backend_write",
			})
		}
		return fmt.Errorf("failed to delete task: %w", err)
	}

	if deleted == 0 {
		if s.logger != nil {
			s.logger.WarnWithContext(ctx, "Task not found for deletion", map[string]interface{}{
				"operation":  "task_delete",
				"request_id": core.GetRequestID(ctx),
				"task_id":    taskID,
				"status":     "not_found",
			})
		}
	} else {
		var previousStatus core.TaskStatus
		if previous != nil {
			previousStatus = previous.Status
		}
		if err := s.updateStatusIndex(ctx, taskID, previousStatus, "", false); err != nil {
			return err
		}
		if s.logger != nil {
			s.logger.InfoWithContext(ctx, "Task deleted", map[string]interface{}{
				"operation":  "task_delete",
				"request_id": core.GetRequestID(ctx),
				"task_id":    taskID,
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
				"operation":  "task_cancel",
				"request_id": core.GetRequestID(ctx),
				"task_id":    taskID,
				"status":     task.Status,
				"error":      "task is not cancellable",
				"error_type": "terminal_state",
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
			"operation":  "task_cancel",
			"request_id": core.GetRequestID(ctx),
			"task_id":    taskID,
		})
	}

	return nil
}

// ListByStatus returns all tasks with the given status.
// Useful for monitoring and admin operations.
// The result is still O(N) in result memory. Add pagination before using this
// method as an unbounded high-cardinality request path.
func (s *RedisTaskStore) ListByStatus(ctx context.Context, status core.TaskStatus) ([]*core.Task, error) {
	ids, err := s.scanSetMembers(ctx, s.statusKey(status), 256)
	if err != nil {
		return nil, fmt.Errorf("scan task status index: %w", err)
	}
	tasks := make([]*core.Task, 0, len(ids))
	staleIDs := make([]interface{}, 0)
	for _, id := range ids {
		task, err := s.Get(ctx, id)
		if errors.Is(err, core.ErrTaskNotFound) || err == nil && task.Status != status {
			staleIDs = append(staleIDs, id)
			continue
		}
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if len(staleIDs) > 0 {
		_ = s.client.SRem(ctx, s.statusKey(status), staleIDs...).Err()
	}
	return tasks, nil
}

func (s *RedisTaskStore) updateStatusIndex(
	ctx context.Context,
	id string,
	previous, next core.TaskStatus,
	includeAll bool,
) error {
	pipe := s.client.Pipeline()
	if includeAll {
		pipe.SAdd(ctx, s.allKey(), id)
	} else {
		pipe.SRem(ctx, s.allKey(), id)
	}
	if previous != "" && previous != next {
		pipe.SRem(ctx, s.statusKey(previous), id)
	}
	if next != "" {
		pipe.SAdd(ctx, s.statusKey(next), id)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return &TaskIndexUpdateError{TaskID: id, Cause: err}
	}
	return nil
}

func (s *RedisTaskStore) scanSetMembers(ctx context.Context, key string, countHint int64) ([]string, error) {
	seen := make(map[string]struct{})
	var cursor uint64
	for {
		batch, next, err := s.client.SScan(ctx, key, cursor, "", countHint).Result()
		if err != nil {
			return nil, err
		}
		for _, id := range batch {
			seen[id] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}

var taskIndexStatuses = []core.TaskStatus{
	core.TaskStatusQueued,
	core.TaskStatusRunning,
	core.TaskStatusCompleted,
	core.TaskStatusFailed,
	core.TaskStatusCancelled,
}

type taskReconcileSource struct {
	name string
	scan func(context.Context, uint64, int64) ([]string, uint64, error)
}

func (s *RedisTaskStore) taskReconcileSources(ctx context.Context) ([]taskReconcileSource, error) {
	sources := make([]taskReconcileSource, 0, len(taskIndexStatuses)+2)
	indexKeys := make([]string, 0, len(taskIndexStatuses)+1)
	indexKeys = append(indexKeys, s.allKey())
	for _, status := range taskIndexStatuses {
		indexKeys = append(indexKeys, s.statusKey(status))
	}
	for _, key := range indexKeys {
		indexKey := key
		sources = append(sources, taskReconcileSource{
			name: "index:" + indexKey,
			scan: func(ctx context.Context, cursor uint64, count int64) ([]string, uint64, error) {
				return s.client.SScan(ctx, indexKey, cursor, "", count).Result()
			},
		})
	}

	recordPattern := s.config.KeyPrefix + ":task:*"
	recordPrefix := s.config.KeyPrefix + ":task:"
	addRecordSource := func(name string, client redis.Cmdable) {
		sources = append(sources, taskReconcileSource{
			name: "records:" + name,
			scan: func(ctx context.Context, cursor uint64, count int64) ([]string, uint64, error) {
				keys, next, err := client.Scan(ctx, cursor, recordPattern, count).Result()
				if err != nil {
					return nil, 0, err
				}
				ids := make([]string, 0, len(keys))
				for _, key := range keys {
					if id := strings.TrimPrefix(key, recordPrefix); id != key && id != "" {
						ids = append(ids, id)
					}
				}
				return ids, next, nil
			},
		})
	}
	if cluster, ok := s.client.(*redis.ClusterClient); ok {
		var mastersMu sync.Mutex
		masters := make([]*redis.Client, 0)
		if err := cluster.ForEachMaster(ctx, func(_ context.Context, client *redis.Client) error {
			mastersMu.Lock()
			masters = append(masters, client)
			mastersMu.Unlock()
			return nil
		}); err != nil {
			return nil, fmt.Errorf("list Redis cluster primaries for task repair: %w", err)
		}
		sort.Slice(masters, func(i, j int) bool {
			return masters[i].Options().Addr < masters[j].Options().Addr
		})
		for _, master := range masters {
			addRecordSource(master.Options().Addr, master)
		}
	} else {
		addRecordSource("default", s.client)
	}
	return sources, nil
}

func (s *RedisTaskStore) nextTaskReconcileBatch(
	ctx context.Context,
	source taskReconcileSource,
	limit int,
) ([]string, error) {
	if pending := s.reconcilePending[source.name]; len(pending) > 0 {
		count := min(limit, len(pending))
		batch := append([]string(nil), pending[:count]...)
		if count == len(pending) {
			delete(s.reconcilePending, source.name)
		} else {
			s.reconcilePending[source.name] = pending[count:]
		}
		return batch, nil
	}
	batch, next, err := source.scan(ctx, s.reconcileCursors[source.name], int64(limit))
	if err != nil {
		return nil, err
	}
	s.reconcileCursors[source.name] = next
	if len(batch) > limit {
		s.reconcilePending[source.name] = append([]string(nil), batch[limit:]...)
		batch = batch[:limit]
	}
	return batch, nil
}

func (s *RedisTaskStore) repairTaskIndexEntry(ctx context.Context, id string) error {
	task, err := s.Get(ctx, id)
	if errors.Is(err, core.ErrTaskNotFound) {
		pipe := s.client.Pipeline()
		pipe.SRem(ctx, s.allKey(), id)
		for _, status := range taskIndexStatuses {
			pipe.SRem(ctx, s.statusKey(status), id)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("prune missing task indexes: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.updateStatusIndex(ctx, id, "", task.Status, true); err != nil {
		return err
	}
	for _, status := range taskIndexStatuses {
		if status != task.Status {
			if err := s.client.SRem(ctx, s.statusKey(status), id).Err(); err != nil {
				return fmt.Errorf("repair task status index: %w", err)
			}
		}
	}
	return nil
}

// ReconcileTaskIndexes performs one bounded, application-owned repair pass.
func (s *RedisTaskStore) ReconcileTaskIndexes(ctx context.Context, maxIDs int) error {
	if maxIDs <= 0 {
		return fmt.Errorf("task index reconcile maximum must be positive")
	}
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	sources, err := s.taskReconcileSources(ctx)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return nil
	}
	s.reconcileNext %= len(sources)
	processed := 0
	visited := 0
	for processed < maxIDs && visited < len(sources) {
		sourceIndex := s.reconcileNext
		source := sources[sourceIndex]
		s.reconcileNext = (sourceIndex + 1) % len(sources)
		visited++
		batch, err := s.nextTaskReconcileBatch(ctx, source, maxIDs-processed)
		if err != nil {
			return fmt.Errorf("scan task repair source %q: %w", source.name, err)
		}
		for _, id := range batch {
			processed++
			if err := s.repairTaskIndexEntry(ctx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// TaskIndexReconciler runs bounded status-index repair under application lifecycle control.
type TaskIndexReconciler struct {
	store    *RedisTaskStore
	interval time.Duration
	maxIDs   int
}

func NewTaskIndexReconciler(store *RedisTaskStore, interval time.Duration, maxIDs int) (*TaskIndexReconciler, error) {
	if store == nil || interval <= 0 || maxIDs <= 0 {
		return nil, fmt.Errorf("task index reconciler requires a store and positive limits")
	}
	return &TaskIndexReconciler{store: store, interval: interval, maxIDs: maxIDs}, nil
}

func (reconciler *TaskIndexReconciler) Start(ctx context.Context) error {
	ticker := time.NewTicker(reconciler.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			startedAt := time.Now()
			if err := reconciler.store.ReconcileTaskIndexes(ctx, reconciler.maxIDs); err != nil {
				if reconciler.store.logger != nil {
					reconciler.store.logger.Warn("Task index reconciliation failed", map[string]interface{}{
						"operation":   "task_index_reconcile",
						"error":       "redis task index reconciliation failed",
						"error_type":  "index_reconcile",
						"duration_ms": time.Since(startedAt).Milliseconds(),
					})
				}
			}
		case <-ctx.Done():
			return nil
		}
	}
}

var _ core.Runnable = (*TaskIndexReconciler)(nil)

// Close performs any cleanup needed.
// Note: Does not close the Redis client as it may be shared.
func (s *RedisTaskStore) Close() error {
	// No cleanup needed - Redis client is managed externally
	return nil
}
