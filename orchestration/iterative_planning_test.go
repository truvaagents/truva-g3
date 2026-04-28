package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Unit tests for multi-phase iterative planning code
// Pure unit tests — no external dependencies (Redis, HTTP, etc.)
// =============================================================================

// --- IsTerminal() ---

func TestIsTerminal(t *testing.T) {
	t.Run("nil terminal defaults to true (backward compat)", func(t *testing.T) {
		plan := &RoutingPlan{Terminal: nil}
		if !plan.IsTerminal() {
			t.Error("expected nil Terminal to default to true")
		}
	})

	t.Run("true terminal returns true", func(t *testing.T) {
		v := true
		plan := &RoutingPlan{Terminal: &v}
		if !plan.IsTerminal() {
			t.Error("expected true Terminal to return true")
		}
	})

	t.Run("false terminal returns false", func(t *testing.T) {
		v := false
		plan := &RoutingPlan{Terminal: &v}
		if plan.IsTerminal() {
			t.Error("expected false Terminal to return false")
		}
	})
}

// --- validateNoStepIDConflicts() ---

func TestValidateNoStepIDConflicts(t *testing.T) {
	t.Run("no conflicts with nil executed IDs", func(t *testing.T) {
		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{StepID: "step-1"},
				{StepID: "step-2"},
			},
		}
		if err := validateNoStepIDConflicts(plan, nil); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("no conflicts with disjoint IDs", func(t *testing.T) {
		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{StepID: "step-3"},
				{StepID: "step-4"},
			},
		}
		if err := validateNoStepIDConflicts(plan, []string{"step-1", "step-2"}); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("detects single conflict", func(t *testing.T) {
		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{StepID: "step-1"},
				{StepID: "step-3"},
			},
		}
		err := validateNoStepIDConflicts(plan, []string{"step-1", "step-2"})
		if err == nil {
			t.Fatal("expected error for duplicate step ID")
		}
		if !strings.Contains(err.Error(), "step-1") {
			t.Errorf("error should mention conflicting ID 'step-1', got: %v", err)
		}
		if !strings.Contains(err.Error(), "already executed") {
			t.Errorf("error should mention 'already executed', got: %v", err)
		}
	})

	t.Run("empty plan steps is valid", func(t *testing.T) {
		plan := &RoutingPlan{Steps: nil}
		if err := validateNoStepIDConflicts(plan, []string{"step-1"}); err != nil {
			t.Errorf("expected no error for empty plan, got: %v", err)
		}
	})

	t.Run("empty executed IDs is valid", func(t *testing.T) {
		plan := &RoutingPlan{
			Steps: []RoutingStep{{StepID: "step-1"}},
		}
		if err := validateNoStepIDConflicts(plan, []string{}); err != nil {
			t.Errorf("expected no error with empty executed IDs, got: %v", err)
		}
	})
}

// --- clearResumeMode() ---

func TestClearResumeMode(t *testing.T) {
	t.Run("clears resume mode from context", func(t *testing.T) {
		ctx := WithResumeMode(context.Background(), "checkpoint-123")

		// Verify resume mode is set
		id, ok := IsResumeMode(ctx)
		if !ok || id != "checkpoint-123" {
			t.Fatal("precondition: expected resume mode to be set")
		}

		// Clear it
		ctx = clearResumeMode(ctx)

		// Verify it's cleared
		_, ok = IsResumeMode(ctx)
		if ok {
			t.Error("expected resume mode to be cleared after clearResumeMode()")
		}
	})

	t.Run("no-op on context without resume mode", func(t *testing.T) {
		ctx := clearResumeMode(context.Background())
		_, ok := IsResumeMode(ctx)
		if ok {
			t.Error("expected no resume mode on fresh context")
		}
	})
}

// --- DefaultConfig() iterative planning env var coverage ---

func TestDefaultConfigIterativePlanning(t *testing.T) {
	t.Run("defaults are set correctly", func(t *testing.T) {
		config := DefaultConfig()
		if !config.IterativePlanning.Enabled {
			t.Error("expected iterative planning enabled by default")
		}
		if config.IterativePlanning.MaxPhases != 5 {
			t.Errorf("expected MaxPhases=5, got %d", config.IterativePlanning.MaxPhases)
		}
		if config.IterativePlanning.MaxTotalSteps != 200 {
			t.Errorf("expected MaxTotalSteps=200, got %d", config.IterativePlanning.MaxTotalSteps)
		}
		if config.IterativePlanning.PhaseTimeout != 180*time.Second {
			t.Errorf("expected PhaseTimeout=180s, got %v", config.IterativePlanning.PhaseTimeout)
		}
	})

	t.Run("TRUVAG3_ITERATIVE_PLANNING_ENABLED=false disables", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_PLANNING_ENABLED", "false")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_PLANNING_ENABLED")

		config := DefaultConfig()
		if config.IterativePlanning.Enabled {
			t.Error("expected iterative planning disabled when env=false")
		}
	})

	t.Run("TRUVAG3_ITERATIVE_PLANNING_ENABLED=TRUE case insensitive", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_PLANNING_ENABLED", "TRUE")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_PLANNING_ENABLED")

		config := DefaultConfig()
		if !config.IterativePlanning.Enabled {
			t.Error("expected iterative planning enabled when env=TRUE (case insensitive)")
		}
	})

	t.Run("TRUVAG3_ITERATIVE_MAX_PHASES overrides default", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_MAX_PHASES", "7")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_MAX_PHASES")

		config := DefaultConfig()
		if config.IterativePlanning.MaxPhases != 7 {
			t.Errorf("expected MaxPhases=7, got %d", config.IterativePlanning.MaxPhases)
		}
	})

	t.Run("TRUVAG3_ITERATIVE_MAX_PHASES ignores non-numeric", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_MAX_PHASES", "abc")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_MAX_PHASES")

		config := DefaultConfig()
		if config.IterativePlanning.MaxPhases != 5 {
			t.Errorf("expected MaxPhases=5 (default), got %d", config.IterativePlanning.MaxPhases)
		}
	})

	t.Run("TRUVAG3_ITERATIVE_MAX_PHASES ignores zero", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_MAX_PHASES", "0")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_MAX_PHASES")

		config := DefaultConfig()
		if config.IterativePlanning.MaxPhases != 5 {
			t.Errorf("expected MaxPhases=5 (default for 0), got %d", config.IterativePlanning.MaxPhases)
		}
	})

	t.Run("TRUVAG3_ITERATIVE_MAX_PHASES ignores negative", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_MAX_PHASES", "-1")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_MAX_PHASES")

		config := DefaultConfig()
		if config.IterativePlanning.MaxPhases != 5 {
			t.Errorf("expected MaxPhases=5 (default for negative), got %d", config.IterativePlanning.MaxPhases)
		}
	})

	t.Run("TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS overrides default", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS", "50")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS")

		config := DefaultConfig()
		if config.IterativePlanning.MaxTotalSteps != 50 {
			t.Errorf("expected MaxTotalSteps=50, got %d", config.IterativePlanning.MaxTotalSteps)
		}
	})

	t.Run("TRUVAG3_ITERATIVE_PHASE_TIMEOUT overrides default", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_PHASE_TIMEOUT", "1m")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_PHASE_TIMEOUT")

		config := DefaultConfig()
		if config.IterativePlanning.PhaseTimeout != 1*time.Minute {
			t.Errorf("expected PhaseTimeout=1m, got %v", config.IterativePlanning.PhaseTimeout)
		}
	})

	t.Run("TRUVAG3_ITERATIVE_PHASE_TIMEOUT ignores invalid", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_PHASE_TIMEOUT", "not-a-duration")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_PHASE_TIMEOUT")

		config := DefaultConfig()
		if config.IterativePlanning.PhaseTimeout != 180*time.Second {
			t.Errorf("expected PhaseTimeout=180s (default), got %v", config.IterativePlanning.PhaseTimeout)
		}
	})

	t.Run("TRUVAG3_ITERATIVE_PHASE_TIMEOUT ignores negative", func(t *testing.T) {
		os.Setenv("TRUVAG3_ITERATIVE_PHASE_TIMEOUT", "-5s")
		defer os.Unsetenv("TRUVAG3_ITERATIVE_PHASE_TIMEOUT")

		config := DefaultConfig()
		if config.IterativePlanning.PhaseTimeout != 180*time.Second {
			t.Errorf("expected PhaseTimeout=180s (default for negative), got %v", config.IterativePlanning.PhaseTimeout)
		}
	})
}

// =============================================================================
// Phase 11: Plan Regeneration Observability — storeExecutionAsync serialization
// =============================================================================

// captureExecStore is a minimal ExecutionStore mock that captures stored executions.
type captureExecStore struct {
	mu     sync.Mutex
	stored []*StoredExecution
}

func (m *captureExecStore) Store(_ context.Context, e *StoredExecution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stored = append(m.stored, e)
	return nil
}

func (m *captureExecStore) Get(_ context.Context, _ string) (*StoredExecution, error) {
	return nil, nil
}

func (m *captureExecStore) GetByTraceID(_ context.Context, _ string) (*StoredExecution, error) {
	return nil, nil
}

func (m *captureExecStore) SetMetadata(_ context.Context, _, _, _ string) error { return nil }

func (m *captureExecStore) ExtendTTL(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (m *captureExecStore) ListRecent(_ context.Context, _ int) ([]ExecutionSummary, error) {
	return nil, nil
}

// TestStoreExecutionAsync_RegenEventsSerializedToStoredMetadata verifies that when
// MetadataKeyPlanRegenerations is set in ExecutionResult.Metadata, storeExecutionAsync
// serializes it as JSON into StoredExecution.Metadata[MetadataKeyPlanRegenerations].
func TestStoreExecutionAsync_RegenEventsSerializedToStoredMetadata(t *testing.T) {
	store := &captureExecStore{}
	orch := &AIOrchestrator{
		config:         &OrchestratorConfig{Name: "test-agent"},
		executionStore: store,
	}

	events := []map[string]interface{}{
		{
			"phase_number":         2,
			"validation_error":     "step-3 conflicts with prior phase",
			"original_plan_id":     "plan-old-abc",
			"original_terminal":    false,
			"original_steps":       3,
			"regenerated_plan_id":  "plan-new-xyz",
			"regenerated_terminal": true,
			"regenerated_steps":    2,
		},
	}
	result := &ExecutionResult{
		PlanID:  "plan-new-xyz",
		Success: true,
		Metadata: map[string]interface{}{
			MetadataKeyPhasePlans:        []*RoutingPlan{},
			MetadataKeyPhaseCount:        2,
			MetadataKeyForcedTerminal:    false,
			MetadataKeyPlanRegenerations: events,
		},
	}

	orch.storeExecutionAsync(context.Background(), "test request", "req-phase11-1", nil, result, nil)
	orch.executionWg.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.stored) != 1 {
		t.Fatalf("expected 1 stored execution, got %d", len(store.stored))
	}
	stored := store.stored[0]

	if stored.Metadata == nil {
		t.Fatal("expected stored.Metadata to be non-nil when regenEvents are present")
	}
	regenJSON, ok := stored.Metadata[MetadataKeyPlanRegenerations]
	if !ok {
		t.Fatalf("expected stored.Metadata[%q] to be present", MetadataKeyPlanRegenerations)
	}

	var decoded []map[string]interface{}
	if err := json.Unmarshal([]byte(regenJSON), &decoded); err != nil {
		t.Fatalf("stored plan_regeneration_events is not valid JSON: %v\nraw: %s", err, regenJSON)
	}
	if len(decoded) != 1 {
		t.Errorf("expected 1 decoded event, got %d", len(decoded))
	}
	if decoded[0]["phase_number"] != float64(2) {
		t.Errorf("expected phase_number=2, got %v", decoded[0]["phase_number"])
	}
	if decoded[0]["validation_error"] != "step-3 conflicts with prior phase" {
		t.Errorf("unexpected validation_error: %v", decoded[0]["validation_error"])
	}
	if decoded[0]["regenerated_plan_id"] != "plan-new-xyz" {
		t.Errorf("unexpected regenerated_plan_id: %v", decoded[0]["regenerated_plan_id"])
	}
	if decoded[0]["regenerated_terminal"] != true {
		t.Errorf("expected regenerated_terminal=true, got %v", decoded[0]["regenerated_terminal"])
	}
}

// TestStoreExecutionAsync_NoRegenEventsWhenNone verifies that when no regeneration
// events occurred, StoredExecution.Metadata does NOT contain the regen events key.
func TestStoreExecutionAsync_NoRegenEventsWhenNone(t *testing.T) {
	store := &captureExecStore{}
	orch := &AIOrchestrator{
		config:         &OrchestratorConfig{Name: "test-agent"},
		executionStore: store,
	}

	result := &ExecutionResult{
		PlanID:  "plan-1",
		Success: true,
		Metadata: map[string]interface{}{
			MetadataKeyPhasePlans:     []*RoutingPlan{},
			MetadataKeyPhaseCount:     1,
			MetadataKeyForcedTerminal: false,
			// MetadataKeyPlanRegenerations intentionally absent
		},
	}

	orch.storeExecutionAsync(context.Background(), "request", "req-phase11-2", nil, result, nil)
	orch.executionWg.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.stored) != 1 {
		t.Fatalf("expected 1 stored execution, got %d", len(store.stored))
	}
	stored := store.stored[0]

	if _, ok := stored.Metadata[MetadataKeyPlanRegenerations]; ok {
		t.Error("expected stored.Metadata NOT to contain plan_regeneration_events when no regenerations occurred")
	}
}
