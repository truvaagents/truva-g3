package postgresadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/truvaagents/truva-g3/core"
)

// ScheduleStore implements core.ScheduleStore with PostgreSQL-owned durable
// schedule definitions and indexed due-time selection.
type ScheduleStore struct {
	pool      *pgxpool.Pool
	namespace string
}

func NewScheduleStore(pool *pgxpool.Pool, namespace string) (*ScheduleStore, error) {
	namespace, err := validateStoreConfig(pool, namespace)
	if err != nil {
		return nil, err
	}
	return &ScheduleStore{pool: pool, namespace: namespace}, nil
}

func (store *ScheduleStore) Create(ctx context.Context, schedule *core.Schedule) error {
	if err := validateSchedule(schedule); err != nil {
		return err
	}
	payload, err := json.Marshal(schedule)
	if err != nil {
		return fmt.Errorf("postgres adapter: marshal schedule: %w", err)
	}
	result, err := store.pool.Exec(ctx, `
        INSERT INTO portability_schedules
            (namespace, schedule_id, run_at, enabled, payload)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (namespace, schedule_id) DO NOTHING
    `, store.namespace, schedule.ID, schedule.RunAt, schedule.Enabled, payload)
	if err != nil {
		return fmt.Errorf("postgres adapter: create schedule: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("postgres adapter: schedule %q: %w", schedule.ID, core.ErrScheduleAlreadyExists)
	}
	return nil
}

func (store *ScheduleStore) Get(ctx context.Context, id string) (*core.Schedule, error) {
	id, err := requireID("schedule", id)
	if err != nil {
		return nil, err
	}
	var payload []byte
	if err := store.pool.QueryRow(ctx, `
        SELECT payload
        FROM portability_schedules
        WHERE namespace = $1 AND schedule_id = $2
    `, store.namespace, id).Scan(&payload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres adapter: schedule %q: %w", id, core.ErrScheduleNotFound)
		}
		return nil, fmt.Errorf("postgres adapter: get schedule: %w", err)
	}
	return decodeSchedule(payload)
}

func (store *ScheduleStore) List(ctx context.Context) ([]*core.Schedule, error) {
	rows, err := store.pool.Query(ctx, `
        SELECT payload
        FROM portability_schedules
        WHERE namespace = $1
        ORDER BY created_at, schedule_id
    `, store.namespace)
	if err != nil {
		return nil, fmt.Errorf("postgres adapter: list schedules: %w", err)
	}
	defer rows.Close()
	return scanSchedules(rows)
}

func (store *ScheduleStore) Update(ctx context.Context, schedule *core.Schedule) error {
	if err := validateSchedule(schedule); err != nil {
		return err
	}
	payload, err := json.Marshal(schedule)
	if err != nil {
		return fmt.Errorf("postgres adapter: marshal schedule: %w", err)
	}
	result, err := store.pool.Exec(ctx, `
        UPDATE portability_schedules
        SET run_at = $3, enabled = $4, payload = $5, updated_at = now()
        WHERE namespace = $1 AND schedule_id = $2
    `, store.namespace, schedule.ID, schedule.RunAt, schedule.Enabled, payload)
	if err != nil {
		return fmt.Errorf("postgres adapter: update schedule: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("postgres adapter: schedule %q: %w", schedule.ID, core.ErrScheduleNotFound)
	}
	return nil
}

func (store *ScheduleStore) Delete(ctx context.Context, id string) error {
	id, err := requireID("schedule", id)
	if err != nil {
		return err
	}
	result, err := store.pool.Exec(ctx, `
        DELETE FROM portability_schedules
        WHERE namespace = $1 AND schedule_id = $2
    `, store.namespace, id)
	if err != nil {
		return fmt.Errorf("postgres adapter: delete schedule: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("postgres adapter: schedule %q: %w", id, core.ErrScheduleNotFound)
	}
	return nil
}

func (store *ScheduleStore) GetDue(ctx context.Context, now time.Time) ([]*core.Schedule, error) {
	rows, err := store.pool.Query(ctx, `
        SELECT payload
        FROM portability_schedules
        WHERE namespace = $1 AND enabled = TRUE AND run_at <= $2
        ORDER BY run_at, schedule_id
    `, store.namespace, now)
	if err != nil {
		return nil, fmt.Errorf("postgres adapter: get due schedules: %w", err)
	}
	defer rows.Close()
	return scanSchedules(rows)
}

func (store *ScheduleStore) DeleteNamespace(ctx context.Context) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM portability_schedules WHERE namespace = $1`, store.namespace)
	if err != nil {
		return fmt.Errorf("postgres adapter: delete schedule namespace: %w", err)
	}
	return nil
}

// TaskStore implements core.TaskStore. Scheduler task IDs are unique within a
// namespace, making TaskStore.Create the durable idempotency barrier before
// NATS dispatch.
type TaskStore struct {
	pool      *pgxpool.Pool
	namespace string
}

func NewTaskStore(pool *pgxpool.Pool, namespace string) (*TaskStore, error) {
	namespace, err := validateStoreConfig(pool, namespace)
	if err != nil {
		return nil, err
	}
	return &TaskStore{pool: pool, namespace: namespace}, nil
}

func (store *TaskStore) Create(ctx context.Context, task *core.Task) error {
	if err := validateTask(task); err != nil {
		return err
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("postgres adapter: marshal task: %w", err)
	}
	result, err := store.pool.Exec(ctx, `
        INSERT INTO portability_tasks (namespace, task_id, status, payload)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (namespace, task_id) DO NOTHING
    `, store.namespace, task.ID, task.Status, payload)
	if err != nil {
		return fmt.Errorf("postgres adapter: create task: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("postgres adapter: task %q: %w", task.ID, core.ErrTaskAlreadyExists)
	}
	return nil
}

func (store *TaskStore) Get(ctx context.Context, taskID string) (*core.Task, error) {
	taskID, err := requireID("task", taskID)
	if err != nil {
		return nil, err
	}
	var payload []byte
	if err := store.pool.QueryRow(ctx, `
        SELECT payload
        FROM portability_tasks
        WHERE namespace = $1 AND task_id = $2
    `, store.namespace, taskID).Scan(&payload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres adapter: task %q: %w", taskID, core.ErrTaskNotFound)
		}
		return nil, fmt.Errorf("postgres adapter: get task: %w", err)
	}
	return decodeTask(payload)
}

func (store *TaskStore) Update(ctx context.Context, task *core.Task) error {
	if err := validateTask(task); err != nil {
		return err
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("postgres adapter: marshal task: %w", err)
	}
	result, err := store.pool.Exec(ctx, `
        UPDATE portability_tasks
        SET status = $3, payload = $4, updated_at = now()
        WHERE namespace = $1 AND task_id = $2
    `, store.namespace, task.ID, task.Status, payload)
	if err != nil {
		return fmt.Errorf("postgres adapter: update task: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("postgres adapter: task %q: %w", task.ID, core.ErrTaskNotFound)
	}
	return nil
}

func (store *TaskStore) Delete(ctx context.Context, taskID string) error {
	taskID, err := requireID("task", taskID)
	if err != nil {
		return err
	}
	result, err := store.pool.Exec(ctx, `
        DELETE FROM portability_tasks
        WHERE namespace = $1 AND task_id = $2
    `, store.namespace, taskID)
	if err != nil {
		return fmt.Errorf("postgres adapter: delete task: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("postgres adapter: task %q: %w", taskID, core.ErrTaskNotFound)
	}
	return nil
}

func (store *TaskStore) Cancel(ctx context.Context, taskID string) error {
	taskID, err := requireID("task", taskID)
	if err != nil {
		return err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgres adapter: begin task cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var payload []byte
	if err := tx.QueryRow(ctx, `
        SELECT payload
        FROM portability_tasks
        WHERE namespace = $1 AND task_id = $2
        FOR UPDATE
    `, store.namespace, taskID).Scan(&payload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("postgres adapter: task %q: %w", taskID, core.ErrTaskNotFound)
		}
		return fmt.Errorf("postgres adapter: load task for cancellation: %w", err)
	}
	task, err := decodeTask(payload)
	if err != nil {
		return err
	}
	if task.Status.IsTerminal() {
		return fmt.Errorf("postgres adapter: task %q has status %q: %w", taskID, task.Status, core.ErrTaskNotCancellable)
	}
	now := time.Now().UTC()
	task.Status = core.TaskStatusCancelled
	task.CancelledAt = &now
	task.Error = &core.TaskError{Code: core.TaskErrorCodeCancelled, Message: "task cancelled"}
	payload, err = json.Marshal(task)
	if err != nil {
		return fmt.Errorf("postgres adapter: marshal cancelled task: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        UPDATE portability_tasks
        SET status = $3, payload = $4, updated_at = now()
        WHERE namespace = $1 AND task_id = $2
    `, store.namespace, taskID, task.Status, payload); err != nil {
		return fmt.Errorf("postgres adapter: persist task cancellation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres adapter: commit task cancellation: %w", err)
	}
	return nil
}

func (store *TaskStore) DeleteNamespace(ctx context.Context) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM portability_tasks WHERE namespace = $1`, store.namespace)
	if err != nil {
		return fmt.Errorf("postgres adapter: delete task namespace: %w", err)
	}
	return nil
}

func validateStoreConfig(pool *pgxpool.Pool, namespace string) (string, error) {
	if pool == nil {
		return "", fmt.Errorf("postgres adapter: pool is required")
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return "", fmt.Errorf("postgres adapter: namespace is required")
	}
	if len(namespace) > 128 {
		return "", fmt.Errorf("postgres adapter: namespace exceeds 128 characters")
	}
	return namespace, nil
}

func validateSchedule(schedule *core.Schedule) error {
	if schedule == nil {
		return fmt.Errorf("postgres adapter: schedule is required")
	}
	if strings.TrimSpace(schedule.ID) == "" {
		return fmt.Errorf("postgres adapter: schedule ID is required")
	}
	if strings.TrimSpace(schedule.TargetAgent) == "" {
		return fmt.Errorf("postgres adapter: schedule target agent is required")
	}
	if schedule.RunAt.IsZero() {
		return fmt.Errorf("postgres adapter: schedule run time is required")
	}
	return nil
}

func validateTask(task *core.Task) error {
	if task == nil {
		return fmt.Errorf("postgres adapter: task is required")
	}
	if strings.TrimSpace(task.ID) == "" {
		return fmt.Errorf("postgres adapter: task ID is required")
	}
	return nil
}

func requireID(kind, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("postgres adapter: %s ID is required", kind)
	}
	return id, nil
}

func decodeSchedule(payload []byte) (*core.Schedule, error) {
	var schedule core.Schedule
	if err := json.Unmarshal(payload, &schedule); err != nil {
		return nil, fmt.Errorf("postgres adapter: decode schedule: %w", err)
	}
	return &schedule, nil
}

func decodeTask(payload []byte) (*core.Task, error) {
	var task core.Task
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, fmt.Errorf("postgres adapter: decode task: %w", err)
	}
	return &task, nil
}

type scheduleRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanSchedules(rows scheduleRows) ([]*core.Schedule, error) {
	schedules := make([]*core.Schedule, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("postgres adapter: scan schedule: %w", err)
		}
		schedule, err := decodeSchedule(payload)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres adapter: iterate schedules: %w", err)
	}
	return schedules, nil
}

var (
	_ core.ScheduleStore = (*ScheduleStore)(nil)
	_ core.TaskStore     = (*TaskStore)(nil)
)
