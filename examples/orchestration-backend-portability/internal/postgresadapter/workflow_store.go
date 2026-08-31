// Package postgresadapter contains the proof-only PostgreSQL adapter used by
// the orchestration backend portability example. It is intentionally internal:
// passing conformance proves the public extension seam without advertising a
// supported framework provider.
package postgresadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/truvaagents/truva-g3/orchestration"
)

const schema = `
CREATE TABLE IF NOT EXISTS portability_workflow_executions (
    namespace    TEXT        NOT NULL,
    execution_id TEXT        NOT NULL,
    workflow_id  TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, execution_id)
);
CREATE INDEX IF NOT EXISTS portability_workflow_by_workflow
    ON portability_workflow_executions (namespace, workflow_id, created_at DESC);

CREATE TABLE IF NOT EXISTS portability_schedules (
    namespace   TEXT        NOT NULL,
    schedule_id TEXT        NOT NULL,
    run_at      TIMESTAMPTZ NOT NULL,
    enabled     BOOLEAN     NOT NULL,
    payload     JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, schedule_id)
);
CREATE INDEX IF NOT EXISTS portability_schedules_due
    ON portability_schedules (namespace, enabled, run_at);

CREATE TABLE IF NOT EXISTS portability_tasks (
    namespace  TEXT        NOT NULL,
    task_id    TEXT        NOT NULL,
    status     TEXT        NOT NULL,
    payload    JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, task_id)
);
CREATE INDEX IF NOT EXISTS portability_tasks_status
    ON portability_tasks (namespace, status, created_at DESC);
`

// EnsureSchema creates the durable tables required by the proof-owned
// workflow, schedule, and task stores. Schema ownership remains explicit at
// the integration composition boundary.
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("postgres adapter: pool is required")
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		return fmt.Errorf("postgres adapter: create workflow schema: %w", err)
	}
	return nil
}

// WorkflowStore implements orchestration.StateStore with application-owned
// PostgreSQL connections. It never closes the supplied pool.
type WorkflowStore struct {
	pool      *pgxpool.Pool
	namespace string
}

func NewWorkflowStore(pool *pgxpool.Pool, namespace string) (*WorkflowStore, error) {
	namespace, err := validateStoreConfig(pool, namespace)
	if err != nil {
		return nil, err
	}
	return &WorkflowStore{pool: pool, namespace: namespace}, nil
}

func (s *WorkflowStore) SaveExecution(ctx context.Context, execution *orchestration.WorkflowExecution) error {
	if err := validateExecution(execution); err != nil {
		return err
	}
	payload, err := json.Marshal(execution)
	if err != nil {
		return fmt.Errorf("postgres adapter: marshal workflow execution: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
        INSERT INTO portability_workflow_executions
            (namespace, execution_id, workflow_id, payload)
        VALUES ($1, $2, $3, $4)
    `, s.namespace, execution.ID, execution.WorkflowID, payload)
	if err != nil {
		return fmt.Errorf("postgres adapter: save workflow execution: %w", err)
	}
	return nil
}

func (s *WorkflowStore) UpdateExecution(ctx context.Context, execution *orchestration.WorkflowExecution) error {
	if err := validateExecution(execution); err != nil {
		return err
	}
	payload, err := json.Marshal(execution)
	if err != nil {
		return fmt.Errorf("postgres adapter: marshal workflow execution: %w", err)
	}
	result, err := s.pool.Exec(ctx, `
        UPDATE portability_workflow_executions
        SET workflow_id = $3, payload = $4, updated_at = now()
        WHERE namespace = $1 AND execution_id = $2
    `, s.namespace, execution.ID, execution.WorkflowID, payload)
	if err != nil {
		return fmt.Errorf("postgres adapter: update workflow execution: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("postgres adapter: workflow execution %q not found", execution.ID)
	}
	return nil
}

func (s *WorkflowStore) UpdateStepExecution(
	ctx context.Context,
	executionID string,
	step *orchestration.StepExecution,
) error {
	executionID = strings.TrimSpace(executionID)
	if executionID == "" {
		return fmt.Errorf("postgres adapter: execution ID is required")
	}
	if step == nil || strings.TrimSpace(step.StepID) == "" {
		return fmt.Errorf("postgres adapter: step with an ID is required")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgres adapter: begin step update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var payload []byte
	if err := tx.QueryRow(ctx, `
        SELECT payload
        FROM portability_workflow_executions
        WHERE namespace = $1 AND execution_id = $2
        FOR UPDATE
    `, s.namespace, executionID).Scan(&payload); err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("postgres adapter: workflow execution %q not found", executionID)
		}
		return fmt.Errorf("postgres adapter: load workflow execution for step update: %w", err)
	}

	var execution orchestration.WorkflowExecution
	if err := json.Unmarshal(payload, &execution); err != nil {
		return fmt.Errorf("postgres adapter: decode workflow execution: %w", err)
	}
	if execution.Steps == nil {
		execution.Steps = make(map[string]*orchestration.StepExecution)
	}
	execution.Steps[step.StepID] = step
	payload, err = json.Marshal(&execution)
	if err != nil {
		return fmt.Errorf("postgres adapter: marshal updated workflow execution: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        UPDATE portability_workflow_executions
        SET payload = $3, updated_at = now()
        WHERE namespace = $1 AND execution_id = $2
    `, s.namespace, executionID, payload); err != nil {
		return fmt.Errorf("postgres adapter: persist step update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres adapter: commit step update: %w", err)
	}
	return nil
}

func (s *WorkflowStore) GetExecution(
	ctx context.Context,
	executionID string,
) (*orchestration.WorkflowExecution, error) {
	executionID = strings.TrimSpace(executionID)
	if executionID == "" {
		return nil, fmt.Errorf("postgres adapter: execution ID is required")
	}
	var payload []byte
	if err := s.pool.QueryRow(ctx, `
        SELECT payload
        FROM portability_workflow_executions
        WHERE namespace = $1 AND execution_id = $2
    `, s.namespace, executionID).Scan(&payload); err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("postgres adapter: workflow execution %q not found", executionID)
		}
		return nil, fmt.Errorf("postgres adapter: load workflow execution: %w", err)
	}
	return decodeExecution(payload)
}

func (s *WorkflowStore) ListExecutions(
	ctx context.Context,
	workflowID string,
) ([]*orchestration.WorkflowExecution, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, fmt.Errorf("postgres adapter: workflow ID is required")
	}
	rows, err := s.pool.Query(ctx, `
        SELECT payload
        FROM portability_workflow_executions
        WHERE namespace = $1 AND workflow_id = $2
        ORDER BY created_at DESC
        LIMIT 100
    `, s.namespace, workflowID)
	if err != nil {
		return nil, fmt.Errorf("postgres adapter: list workflow executions: %w", err)
	}
	defer rows.Close()

	executions := make([]*orchestration.WorkflowExecution, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("postgres adapter: scan workflow execution: %w", err)
		}
		execution, err := decodeExecution(payload)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres adapter: iterate workflow executions: %w", err)
	}
	return executions, nil
}

func (s *WorkflowStore) DeleteNamespace(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
        DELETE FROM portability_workflow_executions WHERE namespace = $1
    `, s.namespace)
	if err != nil {
		return fmt.Errorf("postgres adapter: delete namespace: %w", err)
	}
	return nil
}

func validateExecution(execution *orchestration.WorkflowExecution) error {
	if execution == nil {
		return fmt.Errorf("postgres adapter: workflow execution is required")
	}
	if strings.TrimSpace(execution.ID) == "" {
		return fmt.Errorf("postgres adapter: workflow execution ID is required")
	}
	if strings.TrimSpace(execution.WorkflowID) == "" {
		return fmt.Errorf("postgres adapter: workflow ID is required")
	}
	return nil
}

func decodeExecution(payload []byte) (*orchestration.WorkflowExecution, error) {
	var execution orchestration.WorkflowExecution
	if err := json.Unmarshal(payload, &execution); err != nil {
		return nil, fmt.Errorf("postgres adapter: decode workflow execution: %w", err)
	}
	return &execution, nil
}

var _ orchestration.StateStore = (*WorkflowStore)(nil)
