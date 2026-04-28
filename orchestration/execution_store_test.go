package orchestration

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// mockStorageProvider is a mock implementation of StorageProvider for testing.
type mockStorageProvider struct {
	mu                 sync.RWMutex
	data               map[string]string
	indexes            map[string]map[string]float64 // Sorted indexes (was zsets)
	setErr             error
	getErr             error
	addToIndexErr      error // Error for AddToIndex
	listByScoreDescErr error // Error for ListByScoreDesc
}

func newMockStorageProvider() *mockStorageProvider {
	return &mockStorageProvider{
		data:    make(map[string]string),
		indexes: make(map[string]map[string]float64),
	}
}

func (m *mockStorageProvider) Get(ctx context.Context, key string) (string, error) {
	if m.getErr != nil {
		return "", m.getErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data[key], nil
}

func (m *mockStorageProvider) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *mockStorageProvider) Del(ctx context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		delete(m.data, key)
	}
	return nil
}

func (m *mockStorageProvider) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.data[key]
	return exists, nil
}

func (m *mockStorageProvider) AddToIndex(ctx context.Context, key string, score float64, member string) error {
	if m.addToIndexErr != nil {
		return m.addToIndexErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.indexes[key] == nil {
		m.indexes[key] = make(map[string]float64)
	}
	m.indexes[key][member] = score
	return nil
}

func (m *mockStorageProvider) ListByScoreDesc(ctx context.Context, key string, min, max string, offset, count int64) ([]string, error) {
	if m.listByScoreDescErr != nil {
		return nil, m.listByScoreDescErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	index := m.indexes[key]
	if index == nil {
		return []string{}, nil
	}

	// Sort by score descending
	type item struct {
		member string
		score  float64
	}
	items := make([]item, 0, len(index))
	for member, score := range index {
		items = append(items, item{member, score})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].score > items[j].score
	})

	// Apply offset and limit
	start := int(offset)
	end := start + int(count)
	if start >= len(items) {
		return []string{}, nil
	}
	if end > len(items) {
		end = len(items)
	}

	result := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		result = append(result, items[i].member)
	}
	return result, nil
}

func (m *mockStorageProvider) RemoveFromIndex(ctx context.Context, key string, members ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexes[key]
	if index == nil {
		return nil
	}
	for _, member := range members {
		delete(index, member)
	}
	return nil
}

// Test helper to create a sample execution
func sampleExecution(requestID string, success bool) *StoredExecution {
	now := time.Now()
	return &StoredExecution{
		RequestID:         requestID,
		OriginalRequestID: requestID,
		TraceID:           "trace-" + requestID,
		OriginalRequest:   "What's the weather in Tokyo?",
		Plan: &RoutingPlan{
			PlanID:          requestID,
			OriginalRequest: "What's the weather in Tokyo?",
			Mode:            ModeAutonomous,
			Steps: []RoutingStep{
				{
					StepID:      "step-1",
					AgentName:   "weather-tool",
					Instruction: "Get weather for Tokyo",
					DependsOn:   []string{},
				},
			},
			CreatedAt: now,
		},
		Result: &ExecutionResult{
			PlanID:        requestID,
			Success:       success,
			TotalDuration: 1500 * time.Millisecond,
			Steps: []StepResult{
				{
					StepID:    "step-1",
					AgentName: "weather-tool",
					Success:   success,
					Response:  `{"temp": 72, "unit": "F"}`,
					Duration:  1200 * time.Millisecond,
					StartTime: now,
					EndTime:   now.Add(1200 * time.Millisecond),
					Attempts:  1,
				},
			},
		},
		CreatedAt: now,
	}
}

func TestExecutionStore_Store(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()
	execution := sampleExecution("req-001", true)

	// Test successful store
	err := store.Store(ctx, execution)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Verify data was stored
	key := DefaultExecutionKeyPrefix + ":" + execution.RequestID
	if provider.data[key] == "" {
		t.Error("Execution record not stored")
	}

	// Verify index was updated
	if len(provider.indexes[DefaultExecutionKeyPrefix+":index"]) != 1 {
		t.Errorf("Index not updated: got %d entries, want 1", len(provider.indexes[DefaultExecutionKeyPrefix+":index"]))
	}

	// Verify trace mapping was stored
	traceKey := DefaultExecutionKeyPrefix + ":trace:" + execution.TraceID
	if provider.data[traceKey] != execution.RequestID {
		t.Errorf("Trace mapping not stored: got %q, want %q", provider.data[traceKey], execution.RequestID)
	}
}

func TestExecutionStore_Store_NilExecution(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()
	err := store.Store(ctx, nil)
	if err == nil {
		t.Error("Expected error for nil execution, got nil")
	}
}

func TestExecutionStore_Store_EmptyRequestID(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()
	execution := sampleExecution("", true)
	execution.RequestID = ""

	err := store.Store(ctx, execution)
	if err == nil {
		t.Error("Expected error for empty request_id, got nil")
	}
}

func TestExecutionStore_Get(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()
	execution := sampleExecution("req-002", true)

	// Store first
	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Test successful get
	retrieved, err := store.Get(ctx, "req-002")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved.RequestID != execution.RequestID {
		t.Errorf("RequestID mismatch: got %q, want %q", retrieved.RequestID, execution.RequestID)
	}
	if retrieved.OriginalRequest != execution.OriginalRequest {
		t.Errorf("OriginalRequest mismatch: got %q, want %q", retrieved.OriginalRequest, execution.OriginalRequest)
	}
	if len(retrieved.Plan.Steps) != len(execution.Plan.Steps) {
		t.Errorf("Steps count mismatch: got %d, want %d", len(retrieved.Plan.Steps), len(execution.Plan.Steps))
	}
}

func TestExecutionStore_Get_NotFound(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()
	_, err := store.Get(ctx, "nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent record, got nil")
	}
}

func TestExecutionStore_GetByTraceID(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()
	execution := sampleExecution("req-003", true)

	// Store first
	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Test get by trace ID
	retrieved, err := store.GetByTraceID(ctx, execution.TraceID)
	if err != nil {
		t.Fatalf("GetByTraceID failed: %v", err)
	}

	if retrieved.RequestID != execution.RequestID {
		t.Errorf("RequestID mismatch: got %q, want %q", retrieved.RequestID, execution.RequestID)
	}
}

func TestExecutionStore_SetMetadata(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()
	execution := sampleExecution("req-004", true)

	// Store first
	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Set metadata
	if err := store.SetMetadata(ctx, "req-004", "investigation", "performance issue"); err != nil {
		t.Fatalf("SetMetadata failed: %v", err)
	}

	// Verify metadata was set
	retrieved, err := store.Get(ctx, "req-004")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved.Metadata["investigation"] != "performance issue" {
		t.Errorf("Metadata not set: got %q, want %q", retrieved.Metadata["investigation"], "performance issue")
	}
}

func TestExecutionStore_ExtendTTL(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()
	execution := sampleExecution("req-005", true)

	// Store first
	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Extend TTL (this mainly tests that it doesn't error)
	if err := store.ExtendTTL(ctx, "req-005", 48*time.Hour); err != nil {
		t.Fatalf("ExtendTTL failed: %v", err)
	}

	// Verify record still exists
	retrieved, err := store.Get(ctx, "req-005")
	if err != nil {
		t.Fatalf("Get after ExtendTTL failed: %v", err)
	}
	if retrieved.RequestID != "req-005" {
		t.Error("Record not found after ExtendTTL")
	}
}

func TestExecutionStore_ListRecent(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()

	// Store multiple executions with different timestamps
	for i := 0; i < 5; i++ {
		execution := sampleExecution(fmt.Sprintf("req-%03d", i), i%2 == 0)
		execution.CreatedAt = time.Now().Add(time.Duration(i) * time.Minute)
		if err := store.Store(ctx, execution); err != nil {
			t.Fatalf("Store failed: %v", err)
		}
	}

	// List recent
	summaries, err := store.ListRecent(ctx, 3)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}

	if len(summaries) != 3 {
		t.Errorf("ListRecent returned %d items, want 3", len(summaries))
	}

	// Verify order (newest first)
	if len(summaries) >= 2 {
		if summaries[0].CreatedAt.Before(summaries[1].CreatedAt) {
			t.Error("ListRecent not sorted by newest first")
		}
	}
}

func TestExecutionStore_ErrorTTL(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	config.TTL = 24 * time.Hour
	config.ErrorTTL = 168 * time.Hour
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()

	// Store a failed execution
	execution := sampleExecution("req-error", false)
	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Record should exist (we can't easily verify TTL without Redis-specific commands)
	retrieved, err := store.Get(ctx, "req-error")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved.Result.Success != false {
		t.Error("Expected failed execution")
	}
}

func TestNoOpExecutionStore(t *testing.T) {
	store := NewNoOpExecutionStore()
	ctx := context.Background()

	// Store should succeed silently
	execution := sampleExecution("req-noop", true)
	if err := store.Store(ctx, execution); err != nil {
		t.Errorf("NoOp Store should not error: %v", err)
	}

	// Get should return error
	_, err := store.Get(ctx, "req-noop")
	if err == nil {
		t.Error("NoOp Get should return error")
	}

	// GetByTraceID should return error
	_, err = store.GetByTraceID(ctx, "trace-noop")
	if err == nil {
		t.Error("NoOp GetByTraceID should return error")
	}

	// SetMetadata should succeed silently
	if err := store.SetMetadata(ctx, "req-noop", "key", "value"); err != nil {
		t.Errorf("NoOp SetMetadata should not error: %v", err)
	}

	// ExtendTTL should succeed silently
	if err := store.ExtendTTL(ctx, "req-noop", time.Hour); err != nil {
		t.Errorf("NoOp ExtendTTL should not error: %v", err)
	}

	// ListRecent should return empty list
	list, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Errorf("NoOp ListRecent should not error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("NoOp ListRecent should return empty list, got %d items", len(list))
	}
}

func TestDefaultExecutionStoreConfig(t *testing.T) {
	config := DefaultExecutionStoreConfig()

	if config.Enabled != false {
		t.Error("Default should be disabled")
	}
	if config.TTL != 24*time.Hour {
		t.Errorf("Default TTL should be 24h, got %v", config.TTL)
	}
	if config.ErrorTTL != 168*time.Hour {
		t.Errorf("Default ErrorTTL should be 168h, got %v", config.ErrorTTL)
	}
}

func TestExecutionStore_ConcurrentAccess(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)

	ctx := context.Background()
	var wg sync.WaitGroup

	// Concurrent stores
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			execution := sampleExecution(fmt.Sprintf("concurrent-%d", i), true)
			if err := store.Store(ctx, execution); err != nil {
				t.Errorf("Concurrent store %d failed: %v", i, err)
			}
		}(i)
	}

	wg.Wait()

	// Verify all were stored
	list, err := store.ListRecent(ctx, 20)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(list) != 10 {
		t.Errorf("Expected 10 records, got %d", len(list))
	}
}
