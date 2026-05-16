package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// =============================================================================
// MemoryLLMDebugStore Tests
// =============================================================================

func TestMemoryLLMDebugStore_RecordInteraction(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()
	requestID := "test-request-1"

	interaction := LLMInteraction{
		Type:             "plan_generation",
		Timestamp:        time.Now(),
		DurationMs:       100,
		Prompt:           "Test prompt",
		Response:         "Test response",
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
		Success:          true,
	}

	// Record first interaction
	err := store.RecordInteraction(ctx, requestID, interaction)
	if err != nil {
		t.Fatalf("RecordInteraction failed: %v", err)
	}

	// Verify record was created
	record, err := store.GetRecord(ctx, requestID)
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	if record.RequestID != requestID {
		t.Errorf("Expected RequestID %s, got %s", requestID, record.RequestID)
	}

	if len(record.Interactions) != 1 {
		t.Errorf("Expected 1 interaction, got %d", len(record.Interactions))
	}

	if record.Interactions[0].Prompt != "Test prompt" {
		t.Errorf("Expected prompt 'Test prompt', got %s", record.Interactions[0].Prompt)
	}

	// Record second interaction
	interaction2 := LLMInteraction{
		Type:       "synthesis",
		Timestamp:  time.Now(),
		DurationMs: 50,
		Prompt:     "Second prompt",
		Response:   "Second response",
		Success:    true,
	}

	err = store.RecordInteraction(ctx, requestID, interaction2)
	if err != nil {
		t.Fatalf("Second RecordInteraction failed: %v", err)
	}

	// Verify both interactions are present
	record, err = store.GetRecord(ctx, requestID)
	if err != nil {
		t.Fatalf("GetRecord after second interaction failed: %v", err)
	}

	if len(record.Interactions) != 2 {
		t.Errorf("Expected 2 interactions, got %d", len(record.Interactions))
	}
}

func TestLLMInteraction_HookPhase_RoundTrip(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()
	requestID := "test-hook-phase"

	// Mixed record with orchestration-level (empty), pre-hook, and post-hook entries.
	// Verifies HookPhase survives storage/recall and that the empty value
	// (orchestration-level) is preserved, not coerced.
	entries := []LLMInteraction{
		{Type: "plan_generation", Timestamp: time.Now(), Success: true}, // orchestration-level
		{Type: "user_memory_recall_identity", HookPhase: HookPhasePre, Timestamp: time.Now(), Success: true},
		{Type: "user_memory_remember", HookPhase: HookPhasePost, Timestamp: time.Now(), Success: true},
	}
	for _, e := range entries {
		if err := store.RecordInteraction(ctx, requestID, e); err != nil {
			t.Fatalf("RecordInteraction failed: %v", err)
		}
	}

	record, err := store.GetRecord(ctx, requestID)
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}
	if len(record.Interactions) != 3 {
		t.Fatalf("expected 3 interactions, got %d", len(record.Interactions))
	}

	wantPhases := []string{"", HookPhasePre, HookPhasePost}
	for i, want := range wantPhases {
		if got := record.Interactions[i].HookPhase; got != want {
			t.Errorf("interaction %d: expected HookPhase %q, got %q", i, want, got)
		}
	}
}

// TestLLMInteraction_HookPhase_JSONContract locks the wire format that
// downstream consumers (e.g., the registry viewer) depend on:
//   - the JSON key is snake_case "hook_phase"
//   - the literal values are lowercase "pre" / "post"
//   - empty HookPhase is omitted entirely (omitempty)
//
// Breaking any of these silently would regress the viewer's hook routing
// without any compile-time or unit-test signal — hence this explicit test.
func TestLLMInteraction_HookPhase_JSONContract(t *testing.T) {
	preEntry := LLMInteraction{Type: "user_memory_recall_identity", HookPhase: HookPhasePre}
	preBytes, err := json.Marshal(preEntry)
	if err != nil {
		t.Fatalf("marshal pre: %v", err)
	}
	if !strings.Contains(string(preBytes), `"hook_phase":"pre"`) {
		t.Errorf(`expected literal "hook_phase":"pre" in JSON, got %s`, string(preBytes))
	}

	postEntry := LLMInteraction{Type: "user_memory_remember", HookPhase: HookPhasePost}
	postBytes, err := json.Marshal(postEntry)
	if err != nil {
		t.Fatalf("marshal post: %v", err)
	}
	if !strings.Contains(string(postBytes), `"hook_phase":"post"`) {
		t.Errorf(`expected literal "hook_phase":"post" in JSON, got %s`, string(postBytes))
	}

	// Orchestration-level calls (empty phase) must omit the field entirely.
	// Otherwise the viewer's `!i.hook_phase` check would treat them as hooks.
	orchEntry := LLMInteraction{Type: "plan_generation"}
	orchBytes, err := json.Marshal(orchEntry)
	if err != nil {
		t.Fatalf("marshal orch: %v", err)
	}
	if strings.Contains(string(orchBytes), "hook_phase") {
		t.Errorf("empty HookPhase must be omitted from JSON, got %s", string(orchBytes))
	}
}

func TestMemoryLLMDebugStore_GetRecord_NotFound(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()

	_, err := store.GetRecord(ctx, "non-existent")
	if err == nil {
		t.Error("Expected error for non-existent record")
	}
}

func TestMemoryLLMDebugStore_SetMetadata(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()
	requestID := "test-request-2"

	// First create a record
	err := store.RecordInteraction(ctx, requestID, LLMInteraction{
		Type:    "plan_generation",
		Success: true,
	})
	if err != nil {
		t.Fatalf("RecordInteraction failed: %v", err)
	}

	// Set metadata
	err = store.SetMetadata(ctx, requestID, "investigation", "high_priority")
	if err != nil {
		t.Fatalf("SetMetadata failed: %v", err)
	}

	// Verify metadata
	record, err := store.GetRecord(ctx, requestID)
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	if record.Metadata["investigation"] != "high_priority" {
		t.Errorf("Expected metadata 'high_priority', got %s", record.Metadata["investigation"])
	}
}

func TestMemoryLLMDebugStore_SetMetadata_NotFound(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()

	err := store.SetMetadata(ctx, "non-existent", "key", "value")
	if err == nil {
		t.Error("Expected error for non-existent record")
	}
}

func TestMemoryLLMDebugStore_ExtendTTL(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()
	requestID := "test-request-3"

	// Create a record
	err := store.RecordInteraction(ctx, requestID, LLMInteraction{
		Type:    "plan_generation",
		Success: true,
	})
	if err != nil {
		t.Fatalf("RecordInteraction failed: %v", err)
	}

	// ExtendTTL should succeed for existing record
	err = store.ExtendTTL(ctx, requestID, 24*time.Hour)
	if err != nil {
		t.Errorf("ExtendTTL failed for existing record: %v", err)
	}

	// ExtendTTL should fail for non-existent record
	err = store.ExtendTTL(ctx, "non-existent", 24*time.Hour)
	if err == nil {
		t.Error("Expected error for non-existent record")
	}
}

func TestMemoryLLMDebugStore_ListRecent(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()

	// Create multiple records
	for i := 0; i < 5; i++ {
		requestID := "test-request-" + string(rune('a'+i))
		success := i%2 == 0 // Alternate success/failure

		err := store.RecordInteraction(ctx, requestID, LLMInteraction{
			Type:        "plan_generation",
			TotalTokens: (i + 1) * 100,
			Success:     success,
		})
		if err != nil {
			t.Fatalf("RecordInteraction failed: %v", err)
		}

		// Small delay to ensure different timestamps
		time.Sleep(1 * time.Millisecond)
	}

	// List all records
	summaries, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}

	if len(summaries) != 5 {
		t.Errorf("Expected 5 summaries, got %d", len(summaries))
	}

	// Verify ordered by creation time (newest first)
	for i := 1; i < len(summaries); i++ {
		if summaries[i].CreatedAt.After(summaries[i-1].CreatedAt) {
			t.Error("Records should be ordered by creation time (newest first)")
		}
	}

	// Test limit
	summaries, err = store.ListRecent(ctx, 2)
	if err != nil {
		t.Fatalf("ListRecent with limit failed: %v", err)
	}

	if len(summaries) != 2 {
		t.Errorf("Expected 2 summaries with limit, got %d", len(summaries))
	}
}

// TestMemoryLLMDebugStore_ListRecent_DedupeShadowsInSummary is the
// Finding-2 regression guard: when a record contains paired shadow rows
// (typed + agent_llm_call) written by InstrumentedAIClient, the list-
// level summary must count/sum against the deduped view so operators
// see correct totals even on historical traces written before Layer 2
// landed. Without the fix, InteractionCount and TotalTokens are
// inflated ~2× for wrapping-agent records.
// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
func TestMemoryLLMDebugStore_ListRecent_DedupeShadowsInSummary(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()

	// Record the paired writes the framework produces today for a single
	// physical LLM call on a wrapping agent: one typed plan_generation row
	// written by orchestration, one agent_llm_call shadow written by
	// InstrumentedAIClient. Both carry identical prompt/response/duration.
	if err := store.RecordInteraction(ctx, "req-paired", LLMInteraction{
		Type:        "plan_generation",
		Prompt:      "plan this",
		Response:    "plan A",
		DurationMs:  142,
		PhaseNumber: 1,
		TotalTokens: 500,
		Success:     true,
	}); err != nil {
		t.Fatalf("record typed row failed: %v", err)
	}
	if err := store.RecordInteraction(ctx, "req-paired", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "devops-chat-agent",
		Prompt:          "plan this",
		Response:        "plan A",
		DurationMs:      142,
		PhaseNumber:     1,
		TotalTokens:     500,
		Success:         true,
	}); err != nil {
		t.Fatalf("record shadow row failed: %v", err)
	}

	summaries, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}

	s := summaries[0]
	if s.InteractionCount != 1 {
		t.Errorf("InteractionCount must count deduped view (1 physical call); got %d (inflated by shadow)", s.InteractionCount)
	}
	if s.TotalTokens != 500 {
		t.Errorf("TotalTokens must sum deduped view (500); got %d (inflated by shadow)", s.TotalTokens)
	}
	// SourceComponents is derived from the raw slice so the wrapping-agent
	// attribution ("devops-chat-agent") still surfaces in the list view;
	// that is useful operator signal and does not depend on dedupe.
	if len(s.SourceComponents) != 1 || s.SourceComponents[0] != "devops-chat-agent" {
		t.Errorf("SourceComponents must include wrapping-agent attribution; got %v", s.SourceComponents)
	}
}

func TestMemoryLLMDebugStore_ListRecent_SourceComponents(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()

	// Record 1: orchestrator-only interactions (no SourceComponent)
	_ = store.RecordInteraction(ctx, "req-orch-only", LLMInteraction{
		Type:        "plan_generation",
		TotalTokens: 100,
		Success:     true,
	})
	_ = store.RecordInteraction(ctx, "req-orch-only", LLMInteraction{
		Type:        "synthesis",
		TotalTokens: 200,
		Success:     true,
	})
	time.Sleep(1 * time.Millisecond)

	// Record 2: single agent source
	_ = store.RecordInteraction(ctx, "req-single-agent", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "research-assistant",
		TotalTokens:     150,
		Success:         true,
	})
	time.Sleep(1 * time.Millisecond)

	// Record 3: mixed sources (orchestrator + two agents)
	_ = store.RecordInteraction(ctx, "req-mixed", LLMInteraction{
		Type:        "plan_generation",
		TotalTokens: 100,
		Success:     true,
	})
	_ = store.RecordInteraction(ctx, "req-mixed", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "travel-agent",
		TotalTokens:     200,
		Success:         true,
	})
	_ = store.RecordInteraction(ctx, "req-mixed", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "research-assistant",
		TotalTokens:     300,
		Success:         true,
	})
	// Duplicate source should be deduplicated
	_ = store.RecordInteraction(ctx, "req-mixed", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "travel-agent",
		TotalTokens:     150,
		Success:         true,
	})

	summaries, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(summaries) != 3 {
		t.Fatalf("Expected 3 summaries, got %d", len(summaries))
	}

	// Find each summary by request ID (order is newest first)
	summaryMap := make(map[string]LLMDebugRecordSummary)
	for _, s := range summaries {
		summaryMap[s.RequestID] = s
	}

	// Orchestrator-only: SourceComponents should be nil/empty
	orchSummary := summaryMap["req-orch-only"]
	if len(orchSummary.SourceComponents) != 0 {
		t.Errorf("Orchestrator-only record should have empty SourceComponents, got %v", orchSummary.SourceComponents)
	}

	// Single agent: SourceComponents should be ["research-assistant"]
	singleSummary := summaryMap["req-single-agent"]
	if len(singleSummary.SourceComponents) != 1 || singleSummary.SourceComponents[0] != "research-assistant" {
		t.Errorf("Single agent record should have [\"research-assistant\"], got %v", singleSummary.SourceComponents)
	}

	// Mixed: SourceComponents should be sorted and deduplicated
	mixedSummary := summaryMap["req-mixed"]
	if len(mixedSummary.SourceComponents) != 2 {
		t.Fatalf("Mixed record should have 2 unique sources, got %v", mixedSummary.SourceComponents)
	}
	if mixedSummary.SourceComponents[0] != "research-assistant" || mixedSummary.SourceComponents[1] != "travel-agent" {
		t.Errorf("Mixed record sources should be sorted [research-assistant, travel-agent], got %v", mixedSummary.SourceComponents)
	}
}

func TestMemoryLLMDebugStore_ClearAndCount(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()

	// Add some records
	for i := 0; i < 3; i++ {
		_ = store.RecordInteraction(ctx, "request-"+string(rune('a'+i)), LLMInteraction{
			Type:    "plan_generation",
			Success: true,
		})
	}

	if store.Count() != 3 {
		t.Errorf("Expected count 3, got %d", store.Count())
	}

	store.Clear()

	if store.Count() != 0 {
		t.Errorf("Expected count 0 after clear, got %d", store.Count())
	}
}

func TestMemoryLLMDebugStore_ConcurrentAccess(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()

	var wg sync.WaitGroup
	numGoroutines := 10
	numOperations := 100

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				requestID := "concurrent-request"
				_ = store.RecordInteraction(ctx, requestID, LLMInteraction{
					Type:    "plan_generation",
					Attempt: goroutineID*numOperations + j,
					Success: true,
				})
			}
		}(i)
	}

	wg.Wait()

	// Verify record exists
	record, err := store.GetRecord(ctx, "concurrent-request")
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	expectedInteractions := numGoroutines * numOperations
	if len(record.Interactions) != expectedInteractions {
		t.Errorf("Expected %d interactions, got %d", expectedInteractions, len(record.Interactions))
	}
}

// =============================================================================
// NoOpLLMDebugStore Tests
// =============================================================================

func TestNoOpLLMDebugStore_RecordInteraction(t *testing.T) {
	store := NewNoOpLLMDebugStore()
	ctx := context.Background()

	// Should always succeed silently
	err := store.RecordInteraction(ctx, "any-request", LLMInteraction{
		Type:    "plan_generation",
		Success: true,
	})

	if err != nil {
		t.Errorf("NoOp RecordInteraction should not return error, got: %v", err)
	}
}

func TestNoOpLLMDebugStore_GetRecord(t *testing.T) {
	store := NewNoOpLLMDebugStore()
	ctx := context.Background()

	// Should return error indicating not configured
	_, err := store.GetRecord(ctx, "any-request")
	if err == nil {
		t.Error("NoOp GetRecord should return error")
	}
}

func TestNoOpLLMDebugStore_SetMetadata(t *testing.T) {
	store := NewNoOpLLMDebugStore()
	ctx := context.Background()

	// Should always succeed silently
	err := store.SetMetadata(ctx, "any-request", "key", "value")
	if err != nil {
		t.Errorf("NoOp SetMetadata should not return error, got: %v", err)
	}
}

func TestNoOpLLMDebugStore_ExtendTTL(t *testing.T) {
	store := NewNoOpLLMDebugStore()
	ctx := context.Background()

	// Should always succeed silently
	err := store.ExtendTTL(ctx, "any-request", 24*time.Hour)
	if err != nil {
		t.Errorf("NoOp ExtendTTL should not return error, got: %v", err)
	}
}

func TestNoOpLLMDebugStore_ListRecent(t *testing.T) {
	store := NewNoOpLLMDebugStore()
	ctx := context.Background()

	// Should return empty slice
	summaries, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Errorf("NoOp ListRecent should not return error, got: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("NoOp ListRecent should return empty slice, got %d items", len(summaries))
	}
}

// =============================================================================
// recordDebugInteraction Tests
// =============================================================================

func TestOrchestrator_recordDebugInteraction_NilStore(t *testing.T) {
	// Create orchestrator without debug store
	aiClient := NewMockAIClient()
	discovery := &MockDiscovery{}
	orchestrator := NewAIOrchestrator(nil, discovery, aiClient)

	// Should return immediately without panic
	orchestrator.recordDebugInteraction(context.Background(), "test-request", LLMInteraction{
		Type:    "plan_generation",
		Success: true,
	})

	// No assertion needed - just verify it doesn't panic
}

func TestOrchestrator_recordDebugInteraction_WithStore(t *testing.T) {
	// Create orchestrator with memory store
	aiClient := NewMockAIClient()
	discovery := &MockDiscovery{}
	orchestrator := NewAIOrchestrator(nil, discovery, aiClient)

	store := NewMemoryLLMDebugStore()
	orchestrator.SetLLMDebugStore(store)

	requestID := "test-request-record"
	interaction := LLMInteraction{
		Type:       "plan_generation",
		Prompt:     "Test prompt for recording",
		Response:   "Test response",
		Success:    true,
		DurationMs: 150,
	}

	// Record interaction
	orchestrator.recordDebugInteraction(context.Background(), requestID, interaction)

	// Wait for async goroutine to complete
	time.Sleep(100 * time.Millisecond)

	// Verify interaction was recorded
	record, err := store.GetRecord(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	if len(record.Interactions) != 1 {
		t.Errorf("Expected 1 interaction, got %d", len(record.Interactions))
	}

	if record.Interactions[0].Prompt != "Test prompt for recording" {
		t.Errorf("Prompt mismatch: got %s", record.Interactions[0].Prompt)
	}
}

func TestSynthesizer_recordDebugInteraction_WithStore(t *testing.T) {
	aiClient := NewMockAIClient()
	synthesizer := NewAISynthesizer(aiClient)

	store := NewMemoryLLMDebugStore()
	synthesizer.SetLLMDebugStore(store)

	requestID := "test-synth-request"
	interaction := LLMInteraction{
		Type:     "synthesis",
		Prompt:   "Synthesize these results",
		Response: "Synthesized response",
		Success:  true,
	}

	// Record interaction
	synthesizer.recordDebugInteraction(context.Background(), requestID, interaction)

	// Wait for async goroutine
	time.Sleep(100 * time.Millisecond)

	// Verify
	record, err := store.GetRecord(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	if len(record.Interactions) != 1 {
		t.Errorf("Expected 1 interaction, got %d", len(record.Interactions))
	}

	if record.Interactions[0].Type != "synthesis" {
		t.Errorf("Expected type 'synthesis', got %s", record.Interactions[0].Type)
	}
}

func TestMicroResolver_recordDebugInteraction_WithStore(t *testing.T) {
	aiClient := NewMockAIClient()
	microResolver := NewMicroResolver(aiClient, nil)

	store := NewMemoryLLMDebugStore()
	microResolver.SetLLMDebugStore(store)

	requestID := "test-micro-request"
	interaction := LLMInteraction{
		Type:     "micro_resolution",
		Prompt:   "Resolve parameters",
		Response: `{"param": "value"}`,
		Success:  true,
	}

	// Record interaction
	microResolver.recordDebugInteraction(context.Background(), requestID, interaction)

	// Wait for async goroutine
	time.Sleep(100 * time.Millisecond)

	// Verify
	record, err := store.GetRecord(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	if record.Interactions[0].Type != "micro_resolution" {
		t.Errorf("Expected type 'micro_resolution', got %s", record.Interactions[0].Type)
	}
}

func TestContextualReResolver_recordDebugInteraction_WithStore(t *testing.T) {
	aiClient := NewMockAIClient()
	reResolver := NewContextualReResolver(aiClient, nil)

	store := NewMemoryLLMDebugStore()
	reResolver.SetLLMDebugStore(store)

	requestID := "test-reresolver-request"
	interaction := LLMInteraction{
		Type:     "semantic_retry",
		Prompt:   "Re-resolve parameters",
		Response: `{"should_retry": true}`,
		Success:  true,
		Attempt:  2,
	}

	// Record interaction
	reResolver.recordDebugInteraction(context.Background(), requestID, interaction)

	// Wait for async goroutine
	time.Sleep(100 * time.Millisecond)

	// Verify
	record, err := store.GetRecord(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	if record.Interactions[0].Type != "semantic_retry" {
		t.Errorf("Expected type 'semantic_retry', got %s", record.Interactions[0].Type)
	}

	if record.Interactions[0].Attempt != 2 {
		t.Errorf("Expected attempt 2, got %d", record.Interactions[0].Attempt)
	}
}

// =============================================================================
// StepID Propagation Tests (Phase 5b: DAG Visualization)
// =============================================================================

// TestMicroResolver_ResolveParameters_StepID_Propagation verifies that StepID
// is correctly propagated to LLMInteraction when MicroResolver.ResolveParameters is called.
// This is critical for DAG visualization to associate LLM calls with execution steps.
func TestMicroResolver_ResolveParameters_StepID_Propagation(t *testing.T) {
	// Create mock AI client that returns valid JSON for micro-resolution
	aiClient := &stepIDTestMockAI{
		response: `{"lat": 48.85}`,
	}
	resolver := NewMicroResolver(aiClient, nil)

	store := NewMemoryLLMDebugStore()
	resolver.SetLLMDebugStore(store)

	// Source data with different name than target (triggers micro-resolution)
	sourceData := map[string]interface{}{"latitude": 48.85}
	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	// Call with specific stepID
	expectedStepID := "step-5-get_weather"
	_, err := resolver.ResolveParameters(context.Background(), sourceData, targetCap, "extract lat", expectedStepID)
	if err != nil {
		t.Fatalf("ResolveParameters failed: %v", err)
	}

	// Wait for async recording using Shutdown (fast, no Sleep)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := resolver.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Get the recorded interaction
	summaries, err := store.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatal("No interactions recorded")
	}

	record, err := store.GetRecord(context.Background(), summaries[0].RequestID)
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	// Verify StepID was propagated
	if len(record.Interactions) == 0 {
		t.Fatal("No interactions in record")
	}

	if record.Interactions[0].StepID != expectedStepID {
		t.Errorf("Expected StepID '%s', got '%s'", expectedStepID, record.Interactions[0].StepID)
	}

	// Also verify type is correct
	if record.Interactions[0].Type != "micro_resolution" {
		t.Errorf("Expected type 'micro_resolution', got '%s'", record.Interactions[0].Type)
	}
}

// TestContextualReResolver_ReResolve_StepID_Propagation verifies that StepID
// from ExecutionContext is correctly propagated to LLMInteraction.
func TestContextualReResolver_ReResolve_StepID_Propagation(t *testing.T) {
	// Create mock AI client that returns valid re-resolution response
	aiClient := &stepIDTestMockAI{
		response: `{"should_retry": true, "analysis": "Fixed parameter", "corrected_parameters": {"symbol": "AAPL"}}`,
	}
	resolver := NewContextualReResolver(aiClient, nil)

	store := NewMemoryLLMDebugStore()
	resolver.SetLLMDebugStore(store)

	// Create execution context with specific StepID
	expectedStepID := "step-3-get_stock_quote"
	execCtx := &ExecutionContext{
		UserQuery:  "Get stock price for Apple",
		SourceData: map[string]interface{}{"company": "Apple Inc."},
		StepID:     expectedStepID,
		Capability: &EnhancedCapability{
			Name: "get_stock_quote",
			Parameters: []Parameter{
				{Name: "symbol", Type: "string", Required: true, Description: "Stock ticker symbol"},
			},
		},
		AttemptedParams: map[string]interface{}{"symbol": "Apple"},
		ErrorResponse:   "Invalid symbol: Apple",
		HTTPStatus:      400,
		RetryCount:      0,
	}

	// Call ReResolve
	_, err := resolver.ReResolve(context.Background(), execCtx)
	if err != nil {
		t.Fatalf("ReResolve failed: %v", err)
	}

	// Wait for async recording using Shutdown (fast, no Sleep)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := resolver.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Get the recorded interaction
	summaries, err := store.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatal("No interactions recorded")
	}

	record, err := store.GetRecord(context.Background(), summaries[0].RequestID)
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	// Verify StepID was propagated
	if len(record.Interactions) == 0 {
		t.Fatal("No interactions in record")
	}

	if record.Interactions[0].StepID != expectedStepID {
		t.Errorf("Expected StepID '%s', got '%s'", expectedStepID, record.Interactions[0].StepID)
	}

	// Also verify type is correct
	if record.Interactions[0].Type != "semantic_retry" {
		t.Errorf("Expected type 'semantic_retry', got '%s'", record.Interactions[0].Type)
	}
}

// TestMicroResolver_ResolveParameters_EmptyStepID verifies that empty StepID
// is correctly handled (for orchestrator-level calls).
func TestMicroResolver_ResolveParameters_EmptyStepID(t *testing.T) {
	aiClient := &stepIDTestMockAI{
		response: `{"lat": 48.85}`,
	}
	resolver := NewMicroResolver(aiClient, nil)

	store := NewMemoryLLMDebugStore()
	resolver.SetLLMDebugStore(store)

	sourceData := map[string]interface{}{"latitude": 48.85}
	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	// Call with empty stepID (orchestrator-level call)
	_, err := resolver.ResolveParameters(context.Background(), sourceData, targetCap, "extract lat", "")
	if err != nil {
		t.Fatalf("ResolveParameters failed: %v", err)
	}

	// Wait for async recording
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := resolver.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Get the recorded interaction
	summaries, _ := store.ListRecent(context.Background(), 1)
	if len(summaries) == 0 {
		t.Fatal("No interactions recorded")
	}

	record, _ := store.GetRecord(context.Background(), summaries[0].RequestID)

	// Verify StepID is empty (as expected for orchestrator-level calls)
	if record.Interactions[0].StepID != "" {
		t.Errorf("Expected empty StepID, got '%s'", record.Interactions[0].StepID)
	}
}

// TestMicroResolver_ResolveParameters_StepID_OnError verifies that StepID
// is correctly propagated even when the LLM call fails.
func TestMicroResolver_ResolveParameters_StepID_OnError(t *testing.T) {
	// Create mock AI client that returns an error
	aiClient := &stepIDTestMockAI{
		err: errors.New("LLM service unavailable"),
	}
	resolver := NewMicroResolver(aiClient, nil)

	store := NewMemoryLLMDebugStore()
	resolver.SetLLMDebugStore(store)

	sourceData := map[string]interface{}{"latitude": 48.85}
	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	// Call with specific stepID - should fail but still record
	expectedStepID := "step-5-get_weather"
	_, err := resolver.ResolveParameters(context.Background(), sourceData, targetCap, "extract lat", expectedStepID)

	// Expect error from LLM
	if err == nil {
		t.Fatal("Expected error from ResolveParameters")
	}

	// Wait for async recording
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := resolver.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Get the recorded interaction
	summaries, _ := store.ListRecent(context.Background(), 1)
	if len(summaries) == 0 {
		t.Fatal("No interactions recorded")
	}

	record, _ := store.GetRecord(context.Background(), summaries[0].RequestID)

	// Verify StepID was still propagated in error case
	if record.Interactions[0].StepID != expectedStepID {
		t.Errorf("Expected StepID '%s', got '%s'", expectedStepID, record.Interactions[0].StepID)
	}

	// Verify it's marked as failed
	if record.Interactions[0].Success {
		t.Error("Expected Success=false for error case")
	}

	// Verify error is recorded
	if record.Interactions[0].Error == "" {
		t.Error("Expected error message to be recorded")
	}
}

// TestContextualReResolver_ReResolve_StepID_OnError verifies that StepID
// is correctly propagated even when the LLM call fails.
func TestContextualReResolver_ReResolve_StepID_OnError(t *testing.T) {
	// Create mock AI client that returns an error
	aiClient := &stepIDTestMockAI{
		err: errors.New("LLM service unavailable"),
	}
	resolver := NewContextualReResolver(aiClient, nil)

	store := NewMemoryLLMDebugStore()
	resolver.SetLLMDebugStore(store)

	expectedStepID := "step-3-get_stock_quote"
	execCtx := &ExecutionContext{
		UserQuery:  "Get stock price for Apple",
		SourceData: map[string]interface{}{"company": "Apple Inc."},
		StepID:     expectedStepID,
		Capability: &EnhancedCapability{
			Name: "get_stock_quote",
			Parameters: []Parameter{
				{Name: "symbol", Type: "string", Required: true},
			},
		},
		AttemptedParams: map[string]interface{}{"symbol": "Apple"},
		ErrorResponse:   "Invalid symbol",
		HTTPStatus:      400,
	}

	// Call ReResolve - should fail but still record
	_, err := resolver.ReResolve(context.Background(), execCtx)

	// Expect error from LLM
	if err == nil {
		t.Fatal("Expected error from ReResolve")
	}

	// Wait for async recording
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := resolver.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Get the recorded interaction
	summaries, _ := store.ListRecent(context.Background(), 1)
	if len(summaries) == 0 {
		t.Fatal("No interactions recorded")
	}

	record, _ := store.GetRecord(context.Background(), summaries[0].RequestID)

	// Verify StepID was still propagated in error case
	if record.Interactions[0].StepID != expectedStepID {
		t.Errorf("Expected StepID '%s', got '%s'", expectedStepID, record.Interactions[0].StepID)
	}

	// Verify it's marked as failed
	if record.Interactions[0].Success {
		t.Error("Expected Success=false for error case")
	}
}

// stepIDTestMockAI is a minimal mock for StepID propagation tests
type stepIDTestMockAI struct {
	response string
	err      error // Optional error to simulate LLM failures
}

func (m *stepIDTestMockAI) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &core.AIResponse{
		Content:  m.response,
		Model:    "test-model",
		Provider: "test-provider",
		Usage: core.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}, nil
}

// =============================================================================
// SetLLMDebugStore Propagation Tests
// =============================================================================

func TestOrchestrator_SetLLMDebugStore_Propagation(t *testing.T) {
	aiClient := NewMockAIClient()
	discovery := &MockDiscovery{}

	// Create config with hybrid resolution enabled to ensure sub-components are created
	config := DefaultConfig()
	config.EnableHybridResolution = true
	config.SemanticRetry.Enabled = true

	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Create store and set it
	store := NewMemoryLLMDebugStore()
	orchestrator.SetLLMDebugStore(store)

	// Verify store was set on orchestrator
	if orchestrator.debugStore != store {
		t.Error("debugStore not set on orchestrator")
	}

	// Verify store was propagated to synthesizer
	if orchestrator.synthesizer != nil && orchestrator.synthesizer.debugStore != store {
		t.Error("debugStore not propagated to synthesizer")
	}
}

func TestOrchestrator_SetLLMDebugStore_NilGuard(t *testing.T) {
	aiClient := NewMockAIClient()
	discovery := &MockDiscovery{}
	orchestrator := NewAIOrchestrator(nil, discovery, aiClient)

	// First set a real store
	store := NewMemoryLLMDebugStore()
	orchestrator.SetLLMDebugStore(store)

	if orchestrator.debugStore == nil {
		t.Error("debugStore should be set")
	}

	// Try to set nil - should be ignored
	orchestrator.SetLLMDebugStore(nil)

	// Store should still be the original
	if orchestrator.debugStore != store {
		t.Error("debugStore should not be replaced with nil")
	}
}

// =============================================================================
// Shutdown Tests
// =============================================================================

func TestOrchestrator_Shutdown_WaitsForRecordings(t *testing.T) {
	aiClient := NewMockAIClient()
	discovery := &MockDiscovery{}
	orchestrator := NewAIOrchestrator(nil, discovery, aiClient)

	store := NewMemoryLLMDebugStore()
	orchestrator.SetLLMDebugStore(store)

	// Record multiple interactions
	for i := 0; i < 5; i++ {
		orchestrator.recordDebugInteraction(context.Background(), "shutdown-test", LLMInteraction{
			Type:    "plan_generation",
			Attempt: i,
			Success: true,
		})
	}

	// Shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := orchestrator.Shutdown(ctx)
	if err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}

	// Verify all interactions were recorded
	record, err := store.GetRecord(context.Background(), "shutdown-test")
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	if len(record.Interactions) != 5 {
		t.Errorf("Expected 5 interactions after shutdown, got %d", len(record.Interactions))
	}
}

func TestOrchestrator_Shutdown_Timeout(t *testing.T) {
	aiClient := NewMockAIClient()
	discovery := &MockDiscovery{}
	orchestrator := NewAIOrchestrator(nil, discovery, aiClient)

	// Create a slow store that simulates delay
	slowStore := &slowDebugStore{delay: 2 * time.Second}
	orchestrator.SetLLMDebugStore(slowStore)

	// Record an interaction
	orchestrator.recordDebugInteraction(context.Background(), "timeout-test", LLMInteraction{
		Type:    "plan_generation",
		Success: true,
	})

	// Shutdown with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := orchestrator.Shutdown(ctx)
	if err == nil {
		t.Error("Expected timeout error from Shutdown")
	}
}

func TestSynthesizer_Shutdown(t *testing.T) {
	aiClient := NewMockAIClient()
	synthesizer := NewAISynthesizer(aiClient)

	store := NewMemoryLLMDebugStore()
	synthesizer.SetLLMDebugStore(store)

	// Record an interaction
	synthesizer.recordDebugInteraction(context.Background(), "synth-shutdown", LLMInteraction{
		Type:    "synthesis",
		Success: true,
	})

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := synthesizer.Shutdown(ctx)
	if err != nil {
		t.Errorf("Synthesizer Shutdown failed: %v", err)
	}

	// Verify recording completed
	record, err := store.GetRecord(context.Background(), "synth-shutdown")
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}

	if len(record.Interactions) != 1 {
		t.Error("Expected interaction to be recorded before shutdown")
	}
}

func TestMicroResolver_Shutdown(t *testing.T) {
	aiClient := NewMockAIClient()
	microResolver := NewMicroResolver(aiClient, nil)

	store := NewMemoryLLMDebugStore()
	microResolver.SetLLMDebugStore(store)

	// Record an interaction
	microResolver.recordDebugInteraction(context.Background(), "micro-shutdown", LLMInteraction{
		Type:    "micro_resolution",
		Success: true,
	})

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := microResolver.Shutdown(ctx)
	if err != nil {
		t.Errorf("MicroResolver Shutdown failed: %v", err)
	}
}

func TestContextualReResolver_Shutdown(t *testing.T) {
	aiClient := NewMockAIClient()
	reResolver := NewContextualReResolver(aiClient, nil)

	store := NewMemoryLLMDebugStore()
	reResolver.SetLLMDebugStore(store)

	// Record an interaction
	reResolver.recordDebugInteraction(context.Background(), "reresolver-shutdown", LLMInteraction{
		Type:    "semantic_retry",
		Success: true,
	})

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := reResolver.Shutdown(ctx)
	if err != nil {
		t.Errorf("ContextualReResolver Shutdown failed: %v", err)
	}
}

// =============================================================================
// generateFallbackRequestID Tests
// =============================================================================

func TestOrchestrator_generateFallbackRequestID_Unique(t *testing.T) {
	aiClient := NewMockAIClient()
	discovery := &MockDiscovery{}
	orchestrator := NewAIOrchestrator(nil, discovery, aiClient)

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := orchestrator.generateFallbackRequestID()
		if ids[id] {
			t.Errorf("Duplicate ID generated: %s", id)
		}
		ids[id] = true
	}
}

func TestSynthesizer_generateFallbackRequestID_Unique(t *testing.T) {
	synthesizer := &AISynthesizer{}

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := synthesizer.generateFallbackRequestID()
		if ids[id] {
			t.Errorf("Duplicate ID generated: %s", id)
		}
		ids[id] = true
	}
}

// =============================================================================
// Factory Options Tests
// =============================================================================

func TestWithLLMDebug_Enabled(t *testing.T) {
	config := DefaultConfig()
	WithLLMDebug(true)(config)

	if !config.LLMDebug.Enabled {
		t.Error("LLMDebug should be enabled")
	}
}

func TestWithLLMDebug_Disabled(t *testing.T) {
	config := DefaultConfig()
	config.LLMDebug.Enabled = true // First enable it
	WithLLMDebug(false)(config)

	if config.LLMDebug.Enabled {
		t.Error("LLMDebug should be disabled")
	}
}

func TestWithLLMDebugStore(t *testing.T) {
	config := DefaultConfig()
	store := NewMemoryLLMDebugStore()
	WithLLMDebugStore(store)(config)

	if !config.LLMDebug.Enabled {
		t.Error("LLMDebug should be auto-enabled when store is set")
	}

	if config.LLMDebugStore != store {
		t.Error("LLMDebugStore should be set")
	}
}

func TestWithLLMDebugTTL(t *testing.T) {
	config := DefaultConfig()
	customTTL := 48 * time.Hour
	WithLLMDebugTTL(customTTL)(config)

	if config.LLMDebug.TTL != customTTL {
		t.Errorf("Expected TTL %v, got %v", customTTL, config.LLMDebug.TTL)
	}
}

func TestWithLLMDebugErrorTTL(t *testing.T) {
	config := DefaultConfig()
	customTTL := 14 * 24 * time.Hour
	WithLLMDebugErrorTTL(customTTL)(config)

	if config.LLMDebug.ErrorTTL != customTTL {
		t.Errorf("Expected ErrorTTL %v, got %v", customTTL, config.LLMDebug.ErrorTTL)
	}
}

// =============================================================================
// GetLLMDebugStore Test
// =============================================================================

func TestOrchestrator_GetLLMDebugStore(t *testing.T) {
	aiClient := NewMockAIClient()
	discovery := &MockDiscovery{}
	orchestrator := NewAIOrchestrator(nil, discovery, aiClient)

	// Initially nil
	if orchestrator.GetLLMDebugStore() != nil {
		t.Error("GetLLMDebugStore should return nil initially")
	}

	// After setting
	store := NewMemoryLLMDebugStore()
	orchestrator.SetLLMDebugStore(store)

	if orchestrator.GetLLMDebugStore() != store {
		t.Error("GetLLMDebugStore should return the configured store")
	}
}

// =============================================================================
// MemoryLLMDebugStore — OriginatingAgent semantics (interface parity with
// RedisLLMDebugStore). Mirrors the Redis-store tests so any implementation
// of LLMDebugStore can be swapped in and behave identically.
// =============================================================================

// TestMemoryLLMDebugStore_OriginatingAgent_FirstWriterWins asserts the
// interface invariant: the originator agent (from "agent_name" baggage)
// is captured on first write and not overwritten by later writes carrying
// a different agent_name (the downstream-worker case).
func TestMemoryLLMDebugStore_OriginatingAgent_FirstWriterWins(t *testing.T) {
	store := NewMemoryLLMDebugStore()

	ctxOrch := telemetry.WithBaggage(context.Background(), "agent_name", "travel-chat-agent")
	if err := store.RecordInteraction(ctxOrch, "req-orig", LLMInteraction{
		Type:    "plan_generation",
		Success: true,
	}); err != nil {
		t.Fatalf("first write failed: %v", err)
	}

	ctxDown := telemetry.WithBaggage(context.Background(), "agent_name", "research-agent-telemetry-service")
	if err := store.RecordInteraction(ctxDown, "req-orig", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "research-agent-telemetry-service",
		Success:         true,
	}); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	rec, err := store.GetRecord(context.Background(), "req-orig")
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}
	if rec.OriginatingAgent != "travel-chat-agent" {
		t.Errorf("OriginatingAgent must be first writer's value; got %q", rec.OriginatingAgent)
	}

	summaries, err := store.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].OriginatingAgent != "travel-chat-agent" {
		t.Errorf("summary OriginatingAgent must match record; got %q", summaries[0].OriginatingAgent)
	}
}

// TestMemoryLLMDebugStore_OriginatingAgent_BackfillOnLaterWrite covers the
// edge case where the first write carries no baggage (e.g. background task
// started outside an orchestrator context) but a later write does. The
// field should backfill rather than stay empty.
//
// Both implementations behave identically here: the Redis store gates its
// HSetNX call on non-empty baggage (see RedisLLMDebugStore.RecordInteraction),
// so an empty first write never locks the field — leaving room for a later
// write to populate it. The Memory store mirrors this with an explicit
// empty-string check on the current OriginatingAgent value. Net effect on
// both: first writer with a value wins, not first writer wins regardless
// of value. See TestRedisLLMDebugStore_OriginatingAgent_BackfillOnLaterWrite
// for the parity assertion against the Redis-backed store.
func TestMemoryLLMDebugStore_OriginatingAgent_BackfillOnLaterWrite(t *testing.T) {
	store := NewMemoryLLMDebugStore()

	if err := store.RecordInteraction(context.Background(), "req-late", LLMInteraction{
		Type:    "plan_generation",
		Success: true,
	}); err != nil {
		t.Fatalf("first (no-baggage) write failed: %v", err)
	}

	ctxNamed := telemetry.WithBaggage(context.Background(), "agent_name", "devops-chat-agent")
	if err := store.RecordInteraction(ctxNamed, "req-late", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "devops-chat-agent",
		Success:         true,
	}); err != nil {
		t.Fatalf("second (with-baggage) write failed: %v", err)
	}

	rec, err := store.GetRecord(context.Background(), "req-late")
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}
	if rec.OriginatingAgent != "devops-chat-agent" {
		t.Errorf("OriginatingAgent must backfill from later baggage when empty; got %q", rec.OriginatingAgent)
	}
}

// TestMemoryLLMDebugStore_OriginatingAgent_EmptyBaggage covers the pre-instrumented
// historical path: writes with no agent_name baggage at all must leave the field
// empty rather than stamping a placeholder.
func TestMemoryLLMDebugStore_OriginatingAgent_EmptyBaggage(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	ctx := context.Background()

	if err := store.RecordInteraction(ctx, "req-no-bag", LLMInteraction{
		Type:    "plan_generation",
		Success: true,
	}); err != nil {
		t.Fatalf("record failed: %v", err)
	}

	rec, err := store.GetRecord(ctx, "req-no-bag")
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}
	if rec.OriginatingAgent != "" {
		t.Errorf("OriginatingAgent must be empty when baggage carries no agent_name; got %q", rec.OriginatingAgent)
	}
}

// =============================================================================
// Test Helpers
// =============================================================================

// slowDebugStore is a test helper that simulates slow storage operations
type slowDebugStore struct {
	delay time.Duration
}

func (s *slowDebugStore) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	time.Sleep(s.delay)
	return nil
}

func (s *slowDebugStore) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	return nil, nil
}

func (s *slowDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	return nil
}

func (s *slowDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	return nil
}

func (s *slowDebugStore) ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}
