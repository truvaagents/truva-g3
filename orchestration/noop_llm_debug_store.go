package orchestration

import (
	"context"
	"fmt"
	"time"
)

// NoOpLLMDebugStore is a safe default implementation that does nothing.
// Used when debug storage is disabled or unavailable.
// This follows FRAMEWORK_DESIGN_PRINCIPLES.md: Safe Defaults principle.
type NoOpLLMDebugStore struct{}

// NewNoOpLLMDebugStore creates a new no-op debug store.
func NewNoOpLLMDebugStore() *NoOpLLMDebugStore {
	return &NoOpLLMDebugStore{}
}

// RecordInteraction is a no-op that always succeeds silently.
func (s *NoOpLLMDebugStore) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	return nil // Silent no-op
}

// GetRecord returns an error indicating debug storage is not configured.
func (s *NoOpLLMDebugStore) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	return nil, fmt.Errorf("LLM debug storage not configured: enable via TRUVAG3_LLM_DEBUG_ENABLED=true")
}

// SetMetadata is a no-op that always succeeds silently.
func (s *NoOpLLMDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	return nil
}

// ExtendTTL is a no-op that always succeeds silently.
func (s *NoOpLLMDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	return nil
}

// ListRecent returns an empty list.
func (s *NoOpLLMDebugStore) ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error) {
	return []LLMDebugRecordSummary{}, nil
}

// Ensure NoOpLLMDebugStore implements LLMDebugStore
var _ LLMDebugStore = (*NoOpLLMDebugStore)(nil)
