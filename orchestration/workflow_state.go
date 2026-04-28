package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

// StateStore interface for workflow state persistence
type StateStore interface {
	SaveExecution(ctx context.Context, execution *WorkflowExecution) error
	UpdateExecution(ctx context.Context, execution *WorkflowExecution) error
	UpdateStepExecution(ctx context.Context, executionID string, step *StepExecution) error
	GetExecution(ctx context.Context, executionID string) (*WorkflowExecution, error)
	ListExecutions(ctx context.Context, workflowID string) ([]*WorkflowExecution, error)
}

// RedisStateStore implements StateStore using Redis
type RedisStateStore struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisStateStore creates a new Redis-based state store
func NewRedisStateStore(discovery core.Discovery) StateStore {
	// In a real implementation, this would get Redis connection from discovery
	// For now, using a default Redis connection
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	return &RedisStateStore{
		client: client,
		ttl:    24 * time.Hour, // Keep execution history for 24 hours
	}
}

// SaveExecution saves a new workflow execution
func (s *RedisStateStore) SaveExecution(ctx context.Context, execution *WorkflowExecution) error {
	data, err := json.Marshal(execution)
	if err != nil {
		return fmt.Errorf("marshaling execution: %w", err)
	}

	key := fmt.Sprintf("workflow:exec:%s", execution.ID)
	err = s.client.Set(ctx, key, data, s.ttl).Err()
	if err != nil {
		return fmt.Errorf("saving to Redis: %w", err)
	}

	// Add to workflow's execution list
	listKey := fmt.Sprintf("workflow:executions:%s", execution.WorkflowID)
	err = s.client.LPush(ctx, listKey, execution.ID).Err()
	if err != nil {
		return fmt.Errorf("adding to execution list: %w", err)
	}

	return nil
}

// UpdateExecution updates an existing workflow execution
func (s *RedisStateStore) UpdateExecution(ctx context.Context, execution *WorkflowExecution) error {
	data, err := json.Marshal(execution)
	if err != nil {
		return fmt.Errorf("marshaling execution: %w", err)
	}

	key := fmt.Sprintf("workflow:exec:%s", execution.ID)

	// Use a transaction to ensure atomic update
	return s.client.Watch(ctx, func(tx *redis.Tx) error {
		// Check if execution exists
		exists, err := tx.Exists(ctx, key).Result()
		if err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("execution %s not found", execution.ID)
		}

		// Update the execution
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, data, s.ttl)
			return nil
		})
		return err
	}, key)
}

// UpdateStepExecution updates a single step's execution state
func (s *RedisStateStore) UpdateStepExecution(ctx context.Context, executionID string, step *StepExecution) error {
	key := fmt.Sprintf("workflow:exec:%s", executionID)

	return s.client.Watch(ctx, func(tx *redis.Tx) error {
		// Get current execution
		data, err := tx.Get(ctx, key).Bytes()
		if err != nil {
			return fmt.Errorf("getting execution: %w", err)
		}

		var execution WorkflowExecution
		if err := json.Unmarshal(data, &execution); err != nil {
			return fmt.Errorf("unmarshaling execution: %w", err)
		}

		// Update the specific step
		execution.Steps[step.StepID] = step

		// Save back
		newData, err := json.Marshal(execution)
		if err != nil {
			return fmt.Errorf("marshaling updated execution: %w", err)
		}

		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, newData, s.ttl)
			return nil
		})
		return err
	}, key)
}

// GetExecution retrieves a workflow execution
func (s *RedisStateStore) GetExecution(ctx context.Context, executionID string) (*WorkflowExecution, error) {
	key := fmt.Sprintf("workflow:exec:%s", executionID)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, fmt.Errorf("execution %s not found", executionID)
		}
		return nil, fmt.Errorf("getting execution: %w", err)
	}

	var execution WorkflowExecution
	if err := json.Unmarshal(data, &execution); err != nil {
		return nil, fmt.Errorf("unmarshaling execution: %w", err)
	}

	return &execution, nil
}

// ListExecutions lists all executions for a workflow
func (s *RedisStateStore) ListExecutions(ctx context.Context, workflowID string) ([]*WorkflowExecution, error) {
	listKey := fmt.Sprintf("workflow:executions:%s", workflowID)

	// Get execution IDs
	execIDs, err := s.client.LRange(ctx, listKey, 0, 99).Result() // Last 100 executions
	if err != nil {
		return nil, fmt.Errorf("getting execution list: %w", err)
	}

	executions := make([]*WorkflowExecution, 0, len(execIDs))

	for _, execID := range execIDs {
		execution, err := s.GetExecution(ctx, execID)
		if err != nil {
			// Skip failed retrievals
			continue
		}
		executions = append(executions, execution)
	}

	return executions, nil
}

// InMemoryStateStore implements StateStore in memory (for testing)
type InMemoryStateStore struct {
	executions map[string]*WorkflowExecution
}

// NewInMemoryStateStore creates a new in-memory state store
func NewInMemoryStateStore() StateStore {
	return &InMemoryStateStore{
		executions: make(map[string]*WorkflowExecution),
	}
}

func (s *InMemoryStateStore) SaveExecution(ctx context.Context, execution *WorkflowExecution) error {
	s.executions[execution.ID] = execution
	return nil
}

func (s *InMemoryStateStore) UpdateExecution(ctx context.Context, execution *WorkflowExecution) error {
	s.executions[execution.ID] = execution
	return nil
}

func (s *InMemoryStateStore) UpdateStepExecution(ctx context.Context, executionID string, step *StepExecution) error {
	if execution, exists := s.executions[executionID]; exists {
		execution.Steps[step.StepID] = step
	}
	return nil
}

func (s *InMemoryStateStore) GetExecution(ctx context.Context, executionID string) (*WorkflowExecution, error) {
	if execution, exists := s.executions[executionID]; exists {
		return execution, nil
	}
	return nil, fmt.Errorf("execution not found")
}

func (s *InMemoryStateStore) ListExecutions(ctx context.Context, workflowID string) ([]*WorkflowExecution, error) {
	var executions []*WorkflowExecution
	for _, exec := range s.executions {
		if exec.WorkflowID == workflowID {
			executions = append(executions, exec)
		}
	}
	return executions, nil
}
