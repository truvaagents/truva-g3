package orchestration

import (
	"context"
	"fmt"
	"time"
)

// NoOpExecutionStore is a safe default implementation that does nothing.
// Used when execution storage is disabled or unavailable.
// This follows FRAMEWORK_DESIGN_PRINCIPLES.md: Safe Defaults principle.
type NoOpExecutionStore struct{}

// NewNoOpExecutionStore creates a new no-op execution store.
func NewNoOpExecutionStore() *NoOpExecutionStore {
	return &NoOpExecutionStore{}
}

// Store is a no-op that always succeeds silently.
func (s *NoOpExecutionStore) Store(ctx context.Context, execution *StoredExecution) error {
	return nil // Silent no-op
}

// Get returns an error indicating execution storage is not configured.
func (s *NoOpExecutionStore) Get(ctx context.Context, requestID string) (*StoredExecution, error) {
	return nil, fmt.Errorf("execution storage not configured: enable via TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true")
}

// GetByTraceID returns an error indicating execution storage is not configured.
func (s *NoOpExecutionStore) GetByTraceID(ctx context.Context, traceID string) (*StoredExecution, error) {
	return nil, fmt.Errorf("execution storage not configured: enable via TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true")
}

// SetMetadata is a no-op that always succeeds silently.
func (s *NoOpExecutionStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	return nil
}

// ExtendTTL is a no-op that always succeeds silently.
func (s *NoOpExecutionStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	return nil
}

// ListRecent returns an empty list.
func (s *NoOpExecutionStore) ListRecent(ctx context.Context, limit int) ([]ExecutionSummary, error) {
	return []ExecutionSummary{}, nil
}

// Ensure NoOpExecutionStore implements ExecutionStore
var _ ExecutionStore = (*NoOpExecutionStore)(nil)
