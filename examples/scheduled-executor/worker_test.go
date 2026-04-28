package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

// ═══════════════════════════════════════════════════════════════════════════
// Fakes
// ═══════════════════════════════════════════════════════════════════════════

type fakeResolver struct {
	mu         sync.RWMutex
	agents     map[string]*core.ServiceInfo
	refreshN   int
	refreshErr error
}

func newFakeResolver(agents map[string]*core.ServiceInfo) *fakeResolver {
	return &fakeResolver{agents: agents}
}

func (f *fakeResolver) FindByName(name string) *core.ServiceInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.agents[name]
}

func (f *fakeResolver) Refresh(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshN++
	return f.refreshErr
}

func makeTask(id, targetAgent string) *core.Task {
	return &core.Task{
		ID:          id,
		Type:        core.ScheduledTaskType,
		Status:      core.TaskStatusQueued,
		ScheduleID:  "sch-" + id,
		TargetAgent: targetAgent,
		Input:       map[string]interface{}{"instruction": "test"},
		CreatedAt:   time.Now(),
	}
}

// dispatchOne is a helper: dispatches a task, consumes the handle, and
// calls w.dispatch on it. Returns the consumer so tests can inspect DLQ.
func dispatchOne(t *testing.T, w *Worker, task *core.Task) *orchestration.InMemoryTaskConsumer {
	t.Helper()
	d := orchestration.NewInMemoryTaskDispatcher()
	c := orchestration.NewInMemoryTaskConsumerFromDispatcher(d)
	ctx := context.Background()
	if err := d.Dispatch(ctx, defaultQueueName, task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	handle, err := c.Consume(ctx, defaultQueueName)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	w.dispatch(ctx, handle)
	return c
}

func resolverForServer(name string, ts *httptest.Server) *fakeResolver {
	addr := ts.Listener.Addr().(*net.TCPAddr)
	return newFakeResolver(map[string]*core.ServiceInfo{
		name: {
			Name:    name,
			Address: addr.IP.String(),
			Port:    addr.Port,
			Type:    core.ComponentTypeAgent,
		},
	})
}

func mustWorker(t *testing.T, client *http.Client, catalog AgentResolver) *Worker {
	t.Helper()
	// Consumer is not used — we call dispatch() directly.
	d := orchestration.NewInMemoryTaskDispatcher()
	c := orchestration.NewInMemoryTaskConsumerFromDispatcher(d)
	w, err := NewWorker(ExecutorDeps{
		Consumer:        c,
		HTTPClient:      client,
		Catalog:         catalog,
		WorkerCount:     1,
		MaxRetries:      2,
		RetryBaseDelay:  10 * time.Millisecond,
		RetryMaxDelay:   50 * time.Millisecond,
		DispatchTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w
}

// ═══════════════════════════════════════════════════════════════════════════
// NewWorker validation
// ═══════════════════════════════════════════════════════════════════════════

func TestNewWorker_NilConsumer(t *testing.T) {
	_, err := NewWorker(ExecutorDeps{HTTPClient: &http.Client{}, Catalog: newFakeResolver(nil)})
	if err == nil {
		t.Fatal("expected error for nil Consumer")
	}
}

func TestNewWorker_NilHTTPClient(t *testing.T) {
	d := orchestration.NewInMemoryTaskDispatcher()
	c := orchestration.NewInMemoryTaskConsumerFromDispatcher(d)
	_, err := NewWorker(ExecutorDeps{Consumer: c, Catalog: newFakeResolver(nil)})
	if err == nil {
		t.Fatal("expected error for nil HTTPClient")
	}
}

func TestNewWorker_NilCatalog(t *testing.T) {
	d := orchestration.NewInMemoryTaskDispatcher()
	c := orchestration.NewInMemoryTaskConsumerFromDispatcher(d)
	_, err := NewWorker(ExecutorDeps{Consumer: c, HTTPClient: &http.Client{}})
	if err == nil {
		t.Fatal("expected error for nil Catalog")
	}
}

func TestNewWorker_AppliesDefaults(t *testing.T) {
	d := orchestration.NewInMemoryTaskDispatcher()
	c := orchestration.NewInMemoryTaskConsumerFromDispatcher(d)
	w, err := NewWorker(ExecutorDeps{Consumer: c, HTTPClient: &http.Client{}, Catalog: newFakeResolver(nil)})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if w.deps.WorkerCount != defaultWorkerCount {
		t.Errorf("WorkerCount = %d, want %d", w.deps.WorkerCount, defaultWorkerCount)
	}
	if w.deps.QueueName != defaultQueueName {
		t.Errorf("QueueName = %q, want %q", w.deps.QueueName, defaultQueueName)
	}
	if w.deps.Logger == nil {
		t.Error("Logger should default to NoOpLogger")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dispatch — happy path
// ═══════════════════════════════════════════════════════════════════════════

func TestDispatch_Success_Ack(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "data": "ok"})
	}))
	defer ts.Close()

	resolver := resolverForServer("agent-a", ts)
	w := mustWorker(t, ts.Client(), resolver)
	c := dispatchOne(t, w, makeTask("t-1", "agent-a"))

	if len(c.DLQEntries()) != 0 {
		t.Errorf("expected 0 DLQ entries, got %d", len(c.DLQEntries()))
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dispatch — agent returns success: false (semantic failure → Ack, not Nack)
// ═══════════════════════════════════════════════════════════════════════════

func TestDispatch_AgentSuccessFalse_StillAck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   map[string]interface{}{"message": "rate limit"},
		})
	}))
	defer ts.Close()

	resolver := resolverForServer("agent-a", ts)
	w := mustWorker(t, ts.Client(), resolver)
	c := dispatchOne(t, w, makeTask("t-2", "agent-a"))

	// Semantic failure: Ack'd, NOT Nack'd — so DLQ should be empty.
	if len(c.DLQEntries()) != 0 {
		t.Errorf("expected 0 DLQ entries (Ack on semantic failure), got %d", len(c.DLQEntries()))
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dispatch — 5xx retries then exhausts → Nack
// ═══════════════════════════════════════════════════════════════════════════

func TestDispatch_5xx_MaxRetriesExhausted_Nack(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	resolver := resolverForServer("agent-a", ts)
	w := mustWorker(t, ts.Client(), resolver)
	c := dispatchOne(t, w, makeTask("t-3", "agent-a"))

	entries := c.DLQEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d", len(entries))
	}
	if entries[0].Reason != "max_retries_exhausted" {
		t.Errorf("DLQ reason = %q, want max_retries_exhausted", entries[0].Reason)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dispatch — 4xx (non-retryable) → immediate Nack
// ═══════════════════════════════════════════════════════════════════════════

func TestDispatch_4xx_NonRetryable_Nack(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	resolver := resolverForServer("agent-a", ts)
	w := mustWorker(t, ts.Client(), resolver)
	c := dispatchOne(t, w, makeTask("t-4", "agent-a"))

	entries := c.DLQEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d", len(entries))
	}
	if entries[0].Reason != fmt.Sprintf("non_retryable_status_%d", http.StatusBadRequest) {
		t.Errorf("DLQ reason = %q", entries[0].Reason)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dispatch — invalid task type → Nack
// ═══════════════════════════════════════════════════════════════════════════

func TestDispatch_InvalidTaskType_Nack(t *testing.T) {
	resolver := newFakeResolver(nil)
	w := mustWorker(t, &http.Client{}, resolver)

	task := makeTask("t-5", "agent-a")
	task.Type = "wrong-type"
	c := dispatchOne(t, w, task)

	entries := c.DLQEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d", len(entries))
	}
	if entries[0].Reason != "invalid_task_type" {
		t.Errorf("DLQ reason = %q, want invalid_task_type", entries[0].Reason)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dispatch — missing TargetAgent → Nack
// ═══════════════════════════════════════════════════════════════════════════

func TestDispatch_MissingTargetAgent_Nack(t *testing.T) {
	resolver := newFakeResolver(nil)
	w := mustWorker(t, &http.Client{}, resolver)

	task := makeTask("t-6", "")
	c := dispatchOne(t, w, task)

	entries := c.DLQEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d", len(entries))
	}
	if entries[0].Reason != "missing_target_agent" {
		t.Errorf("DLQ reason = %q, want missing_target_agent", entries[0].Reason)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dispatch — unknown agent → Nack
// ═══════════════════════════════════════════════════════════════════════════

func TestDispatch_UnknownAgent_Nack(t *testing.T) {
	resolver := newFakeResolver(map[string]*core.ServiceInfo{})
	w := mustWorker(t, &http.Client{}, resolver)
	c := dispatchOne(t, w, makeTask("t-7", "nonexistent-agent"))

	entries := c.DLQEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d", len(entries))
	}
	if entries[0].Reason != "unknown_target_agent" {
		t.Errorf("DLQ reason = %q, want unknown_target_agent", entries[0].Reason)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dispatch — target is a tool, not an agent → Nack
// ═══════════════════════════════════════════════════════════════════════════

func TestDispatch_TargetNotAgent_Nack(t *testing.T) {
	resolver := newFakeResolver(map[string]*core.ServiceInfo{
		"some-tool": {Name: "some-tool", Address: "127.0.0.1", Port: 8080, Type: core.ComponentTypeTool},
	})
	w := mustWorker(t, &http.Client{}, resolver)
	c := dispatchOne(t, w, makeTask("t-8", "some-tool"))

	entries := c.DLQEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d", len(entries))
	}
	if entries[0].Reason != "target_not_agent" {
		t.Errorf("DLQ reason = %q, want target_not_agent", entries[0].Reason)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dispatch — catalog miss, refresh resolves → success
// ═══════════════════════════════════════════════════════════════════════════

// lateResolver simulates a cache miss that resolves on Refresh.
type lateResolver struct {
	mu         sync.Mutex
	agents     map[string]*core.ServiceInfo
	lateAgents map[string]*core.ServiceInfo // added on first Refresh
	refreshed  bool
}

func (r *lateResolver) FindByName(name string) *core.ServiceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agents[name]
}

func (r *lateResolver) Refresh(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.refreshed {
		for k, v := range r.lateAgents {
			r.agents[k] = v
		}
		r.refreshed = true
	}
	return nil
}

func TestDispatch_CacheMiss_RefreshResolves_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	}))
	defer ts.Close()

	addr := ts.Listener.Addr().(*net.TCPAddr)
	resolver := &lateResolver{
		agents: map[string]*core.ServiceInfo{},
		lateAgents: map[string]*core.ServiceInfo{
			"late-agent": {
				Name: "late-agent", Address: addr.IP.String(), Port: addr.Port, Type: core.ComponentTypeAgent,
			},
		},
	}

	w := mustWorker(t, ts.Client(), resolver)
	c := dispatchOne(t, w, makeTask("t-9", "late-agent"))
	if len(c.DLQEntries()) != 0 {
		t.Errorf("expected 0 DLQ entries after refresh resolved, got %d", len(c.DLQEntries()))
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Settlement backstop — ensures unsettled handles are Nack'd
// ═══════════════════════════════════════════════════════════════════════════

func TestDispatch_SettlementBackstop(t *testing.T) {
	// A task with valid type but empty TargetAgent triggers the
	// missing_target_agent path which calls settle(false, ...).
	// This test just confirms the backstop deferred doesn't double-settle.
	resolver := newFakeResolver(nil)
	w := mustWorker(t, &http.Client{}, resolver)
	task := makeTask("t-backstop", "")
	c := dispatchOne(t, w, task)

	entries := c.DLQEntries()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 DLQ entry, got %d", len(entries))
	}
}
