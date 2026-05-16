package orchestration

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// MemoryLLMDebugStore is an in-memory implementation for testing and development.
// Not suitable for production use as data is lost on restart.
type MemoryLLMDebugStore struct {
	mu      sync.RWMutex
	records map[string]*LLMDebugRecord
}

// NewMemoryLLMDebugStore creates a new in-memory debug store.
func NewMemoryLLMDebugStore() *MemoryLLMDebugStore {
	return &MemoryLLMDebugStore{
		records: make(map[string]*LLMDebugRecord),
	}
}

// RecordInteraction appends an LLM interaction to the debug record.
func (s *MemoryLLMDebugStore) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.records[requestID]
	if !exists {
		// Capture original_request_id from baggage for HITL correlation.
		// For initial requests: original_request_id == requestID
		// For resume requests: original_request_id is the conversation's first requestID
		originalRequestID := requestID
		originatingAgent := ""
		if bag := telemetry.GetBaggage(ctx); bag != nil {
			if origID := bag["original_request_id"]; origID != "" {
				originalRequestID = origID
			}
			originatingAgent = bag["agent_name"]
		}

		record = &LLMDebugRecord{
			RequestID:         requestID,
			OriginalRequestID: originalRequestID,
			TraceID:           getTraceIDFromContext(ctx),
			CreatedAt:         time.Now(),
			Interactions:      []LLMInteraction{},
			OriginatingAgent:  originatingAgent,
			Metadata:          make(map[string]string),
		}
		s.records[requestID] = record
	} else if record.OriginatingAgent == "" {
		// "First writer with a value wins" — mirrors RedisLLMDebugStore, which gates
		// its HSetNX call on non-empty baggage so an empty first write never locks
		// the field. A later write that carries agent_name baggage backfills it
		// here; once non-empty, it sticks. See the two
		// TestXxxLLMDebugStore_OriginatingAgent_BackfillOnLaterWrite tests for the
		// parity assertion.
		if bag := telemetry.GetBaggage(ctx); bag != nil {
			if name := bag["agent_name"]; name != "" {
				record.OriginatingAgent = name
			}
		}
	}

	record.Interactions = append(record.Interactions, interaction)
	record.UpdatedAt = time.Now()
	return nil
}

// GetRecord retrieves the complete debug record for a request.
func (s *MemoryLLMDebugStore) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, exists := s.records[requestID]
	if !exists {
		return nil, fmt.Errorf("record not found: %s", requestID)
	}

	// Return a copy to prevent external modification
	recordCopy := *record
	recordCopy.Interactions = make([]LLMInteraction, len(record.Interactions))
	copy(recordCopy.Interactions, record.Interactions)
	if record.Metadata != nil {
		recordCopy.Metadata = make(map[string]string)
		for k, v := range record.Metadata {
			recordCopy.Metadata[k] = v
		}
	}

	return &recordCopy, nil
}

// SetMetadata adds metadata to an existing record.
func (s *MemoryLLMDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.records[requestID]
	if !exists {
		return fmt.Errorf("record not found: %s", requestID)
	}

	if record.Metadata == nil {
		record.Metadata = make(map[string]string)
	}
	record.Metadata[key] = value
	record.UpdatedAt = time.Now()
	return nil
}

// ExtendTTL is a no-op for in-memory store (no TTL management).
func (s *MemoryLLMDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, exists := s.records[requestID]; !exists {
		return fmt.Errorf("record not found: %s", requestID)
	}
	return nil
}

// ListRecent returns recent records ordered by creation time.
func (s *MemoryLLMDebugStore) ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect all records
	records := make([]*LLMDebugRecord, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}

	// Sort by creation time (newest first)
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})

	// Apply limit
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}

	// Convert to summaries. Totals and count are computed from the deduped
	// view so shadow agent_llm_call rows do not inflate the list-level
	// summary (same behaviour as RedisLLMDebugStore.ListRecent).
	// SourceComponents is derived from the ORIGINAL slice because typed
	// rows always carry an empty SourceComponent; the only populated values
	// come from agent_llm_call partners and orphans.
	// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
	summaries := make([]LLMDebugRecordSummary, len(records))
	for i, record := range records {
		deduped := DedupeLLMInteractions(record.Interactions)
		totalTokens := 0
		hasErrors := false
		for _, interaction := range deduped {
			totalTokens += interaction.TotalTokens
			if !interaction.Success {
				hasErrors = true
			}
		}
		sourceSet := make(map[string]struct{})
		for _, interaction := range record.Interactions {
			if interaction.SourceComponent != "" {
				sourceSet[interaction.SourceComponent] = struct{}{}
			}
		}
		var sourceComponents []string
		for src := range sourceSet {
			sourceComponents = append(sourceComponents, src)
		}
		sort.Strings(sourceComponents)

		summaries[i] = LLMDebugRecordSummary{
			RequestID:         record.RequestID,
			OriginalRequestID: record.OriginalRequestID,
			TraceID:           record.TraceID,
			CreatedAt:         record.CreatedAt,
			InteractionCount:  len(deduped),
			TotalTokens:       totalTokens,
			HasErrors:         hasErrors,
			SourceComponents:  sourceComponents,
			OriginatingAgent:  record.OriginatingAgent,
		}
	}

	return summaries, nil
}

// Clear removes all records (useful for testing).
func (s *MemoryLLMDebugStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = make(map[string]*LLMDebugRecord)
}

// Count returns the number of records (useful for testing).
func (s *MemoryLLMDebugStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// getTraceIDFromContext extracts trace ID from context if available.
func getTraceIDFromContext(ctx context.Context) string {
	// Try to get trace ID from telemetry baggage
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		return tc.TraceID
	}
	return ""
}

// Ensure MemoryLLMDebugStore implements LLMDebugStore
var _ LLMDebugStore = (*MemoryLLMDebugStore)(nil)
