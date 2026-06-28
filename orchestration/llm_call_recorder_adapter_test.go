package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// =============================================================================
// Unit tests for LLMCallRecorderAdapter
// Pure unit tests — uses mock LLMDebugStore (no Redis)
// =============================================================================

// mockLLMDebugStore implements LLMDebugStore for testing.
// RecordInteraction is called concurrently by the distiller's async recording goroutines
// (one per LLM call — and map-reduce fans those out across chunks), so the appends are
// mutex-guarded. Tests that read the slices do so after distiller.Shutdown() (which waits
// on the recording WaitGroup), establishing a happens-before edge for race-free reads.
type mockLLMDebugStore struct {
	mu           sync.Mutex
	interactions []LLMInteraction
	requestIDs   []string
	err          error // injected error for failure testing
}

func (m *mockLLMDebugStore) RecordInteraction(_ context.Context, requestID string, interaction LLMInteraction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.interactions = append(m.interactions, interaction)
	m.requestIDs = append(m.requestIDs, requestID)
	return nil
}

func (m *mockLLMDebugStore) GetRecord(_ context.Context, _ string) (*LLMDebugRecord, error) {
	return nil, nil
}

func (m *mockLLMDebugStore) SetMetadata(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockLLMDebugStore) ExtendTTL(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (m *mockLLMDebugStore) ListRecent(_ context.Context, _ int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}

// --- NewLLMCallRecorderAdapter() ---

func TestNewLLMCallRecorderAdapter(t *testing.T) {
	t.Run("returns telemetry.LLMCallRecorder interface", func(t *testing.T) {
		store := &mockLLMDebugStore{}
		recorder := NewLLMCallRecorderAdapter(store)
		if recorder == nil {
			t.Fatal("expected non-nil recorder")
		}
		// Verify it satisfies the interface (compile-time is checked by var _ line,
		// but runtime nil check is the actual unit test value)
		_ = telemetry.LLMCallRecorder(recorder)
	})
}

// --- RecordLLMCall() ---

func TestLLMCallRecorderAdapter_RecordLLMCall(t *testing.T) {
	t.Run("skips recording when requestID is empty", func(t *testing.T) {
		store := &mockLLMDebugStore{}
		adapter := NewLLMCallRecorderAdapter(store)

		err := adapter.RecordLLMCall(context.Background(), "", telemetry.LLMCallRecord{
			CallType: "test",
			Prompt:   "hello",
		})
		if err != nil {
			t.Errorf("expected nil error for empty requestID, got: %v", err)
		}
		if len(store.interactions) != 0 {
			t.Error("expected no interactions recorded for empty requestID")
		}
	})

	t.Run("records interaction with correct field mapping", func(t *testing.T) {
		store := &mockLLMDebugStore{}
		adapter := NewLLMCallRecorderAdapter(store)

		now := time.Now()
		record := telemetry.LLMCallRecord{
			CallType:         "agent_llm_call",
			SourceComponent:  "research-assistant",
			Description:      "Tool selection",
			StepID:           "step-1",
			PhaseNumber:      2,
			Timestamp:        now,
			DurationMs:       150,
			Prompt:           "test prompt",
			SystemPrompt:     "system prompt",
			Temperature:      0.7,
			MaxTokens:        1000,
			Model:            "gpt-4o",
			Provider:         "openai",
			Response:         "test response",
			PromptTokens:     50,
			CompletionTokens: 100,
			TotalTokens:      150,
			Success:          true,
		}

		err := adapter.RecordLLMCall(context.Background(), "req-123", record)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(store.interactions) != 1 {
			t.Fatalf("expected 1 interaction, got %d", len(store.interactions))
		}
		if store.requestIDs[0] != "req-123" {
			t.Errorf("expected requestID 'req-123', got %q", store.requestIDs[0])
		}

		i := store.interactions[0]
		if i.Type != "agent_llm_call" {
			t.Errorf("Type: expected 'agent_llm_call', got %q", i.Type)
		}
		if i.SourceComponent != "research-assistant" {
			t.Errorf("SourceComponent: expected 'research-assistant', got %q", i.SourceComponent)
		}
		if i.CallDescription != "Tool selection" {
			t.Errorf("CallDescription: expected 'Tool selection', got %q", i.CallDescription)
		}
		if i.StepID != "step-1" {
			t.Errorf("StepID: expected 'step-1', got %q", i.StepID)
		}
		if i.PhaseNumber != 2 {
			t.Errorf("PhaseNumber: expected 2, got %d", i.PhaseNumber)
		}
		if i.Prompt != "test prompt" {
			t.Errorf("Prompt: expected 'test prompt', got %q", i.Prompt)
		}
		if i.Model != "gpt-4o" {
			t.Errorf("Model: expected 'gpt-4o', got %q", i.Model)
		}
		if i.Provider != "openai" {
			t.Errorf("Provider: expected 'openai', got %q", i.Provider)
		}
		if !i.Success {
			t.Error("Success: expected true")
		}
		if i.Attempt != 1 {
			t.Errorf("Attempt: expected 1 (hardcoded for agent calls), got %d", i.Attempt)
		}
		if i.PromptTokens != 50 {
			t.Errorf("PromptTokens: expected 50, got %d", i.PromptTokens)
		}
		if i.CompletionTokens != 100 {
			t.Errorf("CompletionTokens: expected 100, got %d", i.CompletionTokens)
		}
		if i.TotalTokens != 150 {
			t.Errorf("TotalTokens: expected 150, got %d", i.TotalTokens)
		}
	})

	t.Run("propagates store error", func(t *testing.T) {
		store := &mockLLMDebugStore{err: errors.New("redis connection refused")}
		adapter := NewLLMCallRecorderAdapter(store)

		err := adapter.RecordLLMCall(context.Background(), "req-456", telemetry.LLMCallRecord{
			CallType: "test",
		})
		if err == nil {
			t.Fatal("expected error from store")
		}
		if !errors.Is(err, store.err) {
			t.Errorf("expected store error, got: %v", err)
		}
	})
}
