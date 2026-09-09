package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// WorkflowStateStore is the canonical workflow-scoped state contract. The
// workflow identity is part of every lookup and step mutation so providers can
// use one atomic storage scope per workflow without keyspace scans.
type WorkflowStateStore interface {
	SaveExecution(context.Context, *WorkflowExecution) error
	UpdateExecution(context.Context, *WorkflowExecution) error
	UpdateStepExecution(context.Context, string, string, *StepExecution) error
	GetExecution(context.Context, string, string) (*WorkflowExecution, error)
	ListExecutions(context.Context, string) ([]*WorkflowExecution, error)
}

// StateStore is the workflow-scoped persistence contract.
type StateStore = WorkflowStateStore

// RedisStateStore implements WorkflowStateStore using one Redis cluster slot
// per workflow.
type RedisStateStore struct {
	client   redis.UniversalClient
	ttl      time.Duration
	keyspace core.RedisKeyspace
}

// NewRedisStateStoreWithClient creates a canonical workflow state store around
// an application-owned Redis-compatible client.
func NewRedisStateStoreWithClient(
	client redis.UniversalClient,
	keyspace core.RedisKeyspace,
	ttl time.Duration,
) (*RedisStateStore, error) {
	if client == nil {
		return nil, fmt.Errorf("redis workflow state client is required")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &RedisStateStore{client: client, ttl: ttl, keyspace: keyspace}, nil
}

func (s *RedisStateStore) executionKey(workflowID, executionID string) string {
	return s.keyspace.Tagged("workflow", workflowID, "execution", executionID)
}

func (s *RedisStateStore) indexKey(workflowID string) string {
	return s.keyspace.Tagged("workflow", workflowID, "executions")
}

func validateWorkflowExecution(execution *WorkflowExecution) error {
	if execution == nil {
		return fmt.Errorf("workflow execution is required")
	}
	if strings.TrimSpace(execution.WorkflowID) == "" {
		return fmt.Errorf("workflow ID is required")
	}
	if strings.TrimSpace(execution.ID) == "" {
		return fmt.Errorf("execution ID is required")
	}
	return nil
}

func (s *RedisStateStore) SaveExecution(ctx context.Context, execution *WorkflowExecution) error {
	if err := validateWorkflowExecution(execution); err != nil {
		return err
	}
	data, err := json.Marshal(execution)
	if err != nil {
		return fmt.Errorf("marshaling execution: %w", err)
	}
	_, err = s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, s.executionKey(execution.WorkflowID, execution.ID), data, s.ttl)
		pipe.LPush(ctx, s.indexKey(execution.WorkflowID), execution.ID)
		pipe.Expire(ctx, s.indexKey(execution.WorkflowID), s.ttl)
		return nil
	})
	if err != nil {
		return fmt.Errorf("saving execution to Redis: %w", err)
	}
	return nil
}

func (s *RedisStateStore) UpdateExecution(ctx context.Context, execution *WorkflowExecution) error {
	if err := validateWorkflowExecution(execution); err != nil {
		return err
	}
	data, err := json.Marshal(execution)
	if err != nil {
		return fmt.Errorf("marshaling execution: %w", err)
	}
	key := s.executionKey(execution.WorkflowID, execution.ID)
	return s.client.Watch(ctx, func(tx *redis.Tx) error {
		exists, err := tx.Exists(ctx, key).Result()
		if err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("execution %s not found", execution.ID)
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, data, s.ttl)
			pipe.Expire(ctx, s.indexKey(execution.WorkflowID), s.ttl)
			return nil
		})
		return err
	}, key)
}

func (s *RedisStateStore) UpdateStepExecution(
	ctx context.Context,
	workflowID, executionID string,
	step *StepExecution,
) error {
	if step == nil || strings.TrimSpace(step.StepID) == "" {
		return fmt.Errorf("workflow step with an ID is required")
	}
	key := s.executionKey(workflowID, executionID)
	return s.client.Watch(ctx, func(tx *redis.Tx) error {
		data, err := tx.Get(ctx, key).Bytes()
		if err != nil {
			return fmt.Errorf("getting execution: %w", err)
		}
		var execution WorkflowExecution
		if err := json.Unmarshal(data, &execution); err != nil {
			return fmt.Errorf("unmarshaling execution: %w", err)
		}
		if execution.Steps == nil {
			execution.Steps = make(map[string]*StepExecution)
		}
		execution.Steps[step.StepID] = step
		newData, err := json.Marshal(&execution)
		if err != nil {
			return fmt.Errorf("marshaling updated execution: %w", err)
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, newData, s.ttl)
			pipe.Expire(ctx, s.indexKey(workflowID), s.ttl)
			return nil
		})
		return err
	}, key)
}

func (s *RedisStateStore) GetExecution(
	ctx context.Context,
	workflowID, executionID string,
) (*WorkflowExecution, error) {
	data, err := s.client.Get(ctx, s.executionKey(workflowID, executionID)).Bytes()
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

func (s *RedisStateStore) ListExecutions(ctx context.Context, workflowID string) ([]*WorkflowExecution, error) {
	executionIDs, err := s.client.LRange(ctx, s.indexKey(workflowID), 0, 99).Result()
	if err != nil {
		return nil, fmt.Errorf("getting execution list: %w", err)
	}
	executions := make([]*WorkflowExecution, 0, len(executionIDs))
	for _, executionID := range executionIDs {
		execution, err := s.GetExecution(ctx, workflowID, executionID)
		if err == nil {
			executions = append(executions, execution)
		}
	}
	return executions, nil
}

// InMemoryStateStore is the canonical in-memory WorkflowStateStore.
type InMemoryStateStore struct {
	mu         sync.RWMutex
	executions map[string]map[string]*WorkflowExecution
}

func NewInMemoryStateStore() WorkflowStateStore {
	return &InMemoryStateStore{executions: make(map[string]map[string]*WorkflowExecution)}
}

func (s *InMemoryStateStore) SaveExecution(_ context.Context, execution *WorkflowExecution) error {
	if err := validateWorkflowExecution(execution); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.executions[execution.WorkflowID] == nil {
		s.executions[execution.WorkflowID] = make(map[string]*WorkflowExecution)
	}
	s.executions[execution.WorkflowID][execution.ID] = execution
	return nil
}

func (s *InMemoryStateStore) UpdateExecution(ctx context.Context, execution *WorkflowExecution) error {
	return s.SaveExecution(ctx, execution)
}

func (s *InMemoryStateStore) UpdateStepExecution(_ context.Context, workflowID, executionID string, step *StepExecution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if execution := s.executions[workflowID][executionID]; execution != nil {
		if execution.Steps == nil {
			execution.Steps = make(map[string]*StepExecution)
		}
		execution.Steps[step.StepID] = step
	}
	return nil
}

func (s *InMemoryStateStore) GetExecution(_ context.Context, workflowID, executionID string) (*WorkflowExecution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if execution := s.executions[workflowID][executionID]; execution != nil {
		return execution, nil
	}
	return nil, fmt.Errorf("execution not found")
}

func (s *InMemoryStateStore) ListExecutions(_ context.Context, workflowID string) ([]*WorkflowExecution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*WorkflowExecution, 0, len(s.executions[workflowID]))
	for _, execution := range s.executions[workflowID] {
		result = append(result, execution)
	}
	return result, nil
}
