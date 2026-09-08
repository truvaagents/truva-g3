package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

type workflowScopedStateStoreTestDouble struct{}

var _ WorkflowStateStore = workflowScopedStateStoreTestDouble{}

func (workflowScopedStateStoreTestDouble) SaveExecution(context.Context, *WorkflowExecution) error {
	return nil
}
func (workflowScopedStateStoreTestDouble) UpdateExecution(context.Context, *WorkflowExecution) error {
	return nil
}
func (workflowScopedStateStoreTestDouble) UpdateStepExecution(context.Context, string, string, *StepExecution) error {
	return nil
}
func (workflowScopedStateStoreTestDouble) GetExecution(context.Context, string, string) (*WorkflowExecution, error) {
	return nil, nil
}
func (workflowScopedStateStoreTestDouble) ListExecutions(context.Context, string) ([]*WorkflowExecution, error) {
	return nil, nil
}

func TestWorkflowStateStoreContractIsAvailableAdditively(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	legacy, err := NewLegacyRedisStateStoreWithClientAndPrefix(
		client, time.Hour, "workflow",
	)
	if err != nil {
		t.Fatal(err)
	}
	execution := &WorkflowExecution{
		ID: "legacy-execution", WorkflowID: "legacy-workflow", Status: ExecutionPending,
		Steps: make(map[string]*StepExecution),
	}
	if err := legacy.SaveExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	step := &StepExecution{StepID: "legacy-step", Status: StepCompleted}
	if err := legacy.UpdateStepExecution(t.Context(), execution.ID, step); err != nil {
		t.Fatal(err)
	}
	loaded, err := legacy.GetExecution(t.Context(), execution.ID)
	if err != nil || loaded.Steps[step.StepID] == nil {
		t.Fatalf("legacy behavioral round trip = %#v, %v", loaded, err)
	}
	listed, err := legacy.ListExecutions(t.Context(), execution.WorkflowID)
	if err != nil || len(listed) != 1 || listed[0].ID != execution.ID {
		t.Fatalf("legacy behavioral list = %#v, %v", listed, err)
	}
}

func TestRedisStateStoreClientConstructors(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	keyspace, err := core.NewRedisKeyspace("default")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewRedisStateStoreWithClient(client, keyspace, 0)
	if err != nil {
		t.Fatalf("NewRedisStateStoreWithClient() error = %v", err)
	}
	if store.client != client || store.ttl != 24*time.Hour || store.keyspace.Deployment() != "default" {
		t.Fatalf("default client store = client %T, ttl %v, deployment %q", store.client, store.ttl, store.keyspace.Deployment())
	}

	tenant, err := core.NewRedisKeyspace("tenant")
	if err != nil {
		t.Fatal(err)
	}
	store, err = NewRedisStateStoreWithClient(client, tenant, time.Hour)
	if err != nil {
		t.Fatalf("NewRedisStateStoreWithClientAndPrefix() error = %v", err)
	}
	if store.ttl != time.Hour || store.keyspace.Deployment() != "tenant" {
		t.Fatalf("explicit client store = ttl %v, deployment %q", store.ttl, store.keyspace.Deployment())
	}

	if _, err := NewRedisStateStoreWithClient(nil, keyspace, time.Hour); err == nil {
		t.Fatal("nil workflow-state Redis client was accepted")
	}
	if _, err := NewLegacyRedisStateStoreWithClientAndPrefix(client, time.Hour, " : "); err == nil {
		t.Fatal("empty workflow-state key prefix was accepted")
	}
}

func TestRedisStateStoreUpdatesRefreshWorkflowIndexTTL(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("workflow-retention")
	if err != nil {
		t.Fatal(err)
	}
	const ttl = time.Hour
	store, err := NewRedisStateStoreWithClient(client, keyspace, ttl)
	if err != nil {
		t.Fatal(err)
	}
	execution := &WorkflowExecution{
		ID: "execution-1", WorkflowID: "workflow-1", Status: ExecutionPending,
		Steps: make(map[string]*StepExecution),
	}
	if err := store.SaveExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}

	server.FastForward(40 * time.Minute)
	execution.Status = ExecutionRunning
	if err := store.UpdateExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	if got := server.TTL(store.indexKey(execution.WorkflowID)); got != ttl {
		t.Fatalf("index TTL after execution update = %s, want %s", got, ttl)
	}

	server.FastForward(40 * time.Minute)
	step := &StepExecution{StepID: "step-1", Status: StepCompleted}
	if err := store.UpdateStepExecution(t.Context(), execution.WorkflowID, execution.ID, step); err != nil {
		t.Fatal(err)
	}
	if got := server.TTL(store.indexKey(execution.WorkflowID)); got != ttl {
		t.Fatalf("index TTL after step update = %s, want %s", got, ttl)
	}

	server.FastForward(40 * time.Minute)
	listed, err := store.ListExecutions(t.Context(), execution.WorkflowID)
	if err != nil || len(listed) != 1 || listed[0].ID != execution.ID {
		t.Fatalf("live updated execution disappeared from workflow index: %#v, %v", listed, err)
	}
}
