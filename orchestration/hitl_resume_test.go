package orchestration

import (
	"context"
	"errors"
	"testing"
)

// =============================================================================
// Unit tests for RC8 (Issue 5) and RC9 (Issue 6) fixes
//
// RC8: Skip plan validation and step ID conflict check for HITL resume plans.
// RC9: Persist enriched checkpoint (with accumulated multi-phase step results)
//      back to the checkpoint store (DB 6) via the CheckpointEnricher interface.
// =============================================================================

// =============================================================================
// RC9: CheckpointEnricher Interface Tests
// =============================================================================

// --- SaveEnrichedCheckpoint on DefaultInterruptController ---

func TestSaveEnrichedCheckpoint_DelegatesToStore(t *testing.T) {
	store := newMockCheckpointStore()
	controller := NewInterruptController(nil, store, nil)

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-001",
		StepResults: map[string]*StepResult{
			"step-1": {StepID: "step-1", Response: "result-1", Success: true},
			"step-2": {StepID: "step-2", Response: "result-2", Success: true},
		},
	}

	err := controller.SaveEnrichedCheckpoint(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Verify the checkpoint was saved to the store
	saved, exists := store.checkpoints["cp-001"]
	if !exists {
		t.Fatal("expected checkpoint to be saved in store")
	}
	if len(saved.StepResults) != 2 {
		t.Errorf("expected 2 step results in saved checkpoint, got %d", len(saved.StepResults))
	}
}

func TestSaveEnrichedCheckpoint_NilStore_ReturnsNil(t *testing.T) {
	controller := &DefaultInterruptController{store: nil}

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-001",
		StepResults:  map[string]*StepResult{"step-1": {StepID: "step-1"}},
	}

	err := controller.SaveEnrichedCheckpoint(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("expected nil error for nil store, got: %v", err)
	}
}

func TestSaveEnrichedCheckpoint_StoreError_Propagated(t *testing.T) {
	store := newMockCheckpointStore()
	store.saveErr = errors.New("redis connection refused")
	controller := NewInterruptController(nil, store, nil)

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-001",
		StepResults:  map[string]*StepResult{},
	}

	err := controller.SaveEnrichedCheckpoint(context.Background(), checkpoint)
	if err == nil {
		t.Fatal("expected error to be propagated from store")
	}
	if err.Error() != "redis connection refused" {
		t.Errorf("expected store error, got: %v", err)
	}
}

func TestSaveEnrichedCheckpoint_OverwritesExistingCheckpoint(t *testing.T) {
	store := newMockCheckpointStore()
	controller := NewInterruptController(nil, store, nil)

	// Save initial checkpoint with 1 step result (simulates UpdateCheckpointProgress)
	initial := &ExecutionCheckpoint{
		CheckpointID: "cp-001",
		StepResults: map[string]*StepResult{
			"step-5": {StepID: "step-5", Response: "result-5", Success: true},
		},
	}
	if err := store.SaveCheckpoint(context.Background(), initial); err != nil {
		t.Fatalf("precondition: save initial checkpoint failed: %v", err)
	}

	// Now save enriched version with all phase results
	enriched := &ExecutionCheckpoint{
		CheckpointID: "cp-001",
		StepResults: map[string]*StepResult{
			"step-1": {StepID: "step-1", Response: "result-1", Success: true},
			"step-2": {StepID: "step-2", Response: "result-2", Success: true},
			"step-3": {StepID: "step-3", Response: "result-3", Success: true},
			"step-4": {StepID: "step-4", Response: "result-4", Success: true},
			"step-5": {StepID: "step-5", Response: "result-5", Success: true},
		},
	}

	err := controller.SaveEnrichedCheckpoint(context.Background(), enriched)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Verify the store now has the enriched version
	saved := store.checkpoints["cp-001"]
	if len(saved.StepResults) != 5 {
		t.Errorf("expected 5 step results after enrichment save, got %d", len(saved.StepResults))
	}
	// Verify prior-phase steps are present
	for _, stepID := range []string{"step-1", "step-2", "step-3", "step-4", "step-5"} {
		if _, ok := saved.StepResults[stepID]; !ok {
			t.Errorf("expected step %s to be in saved checkpoint", stepID)
		}
	}
}

// --- CheckpointEnricher type assertion ---

func TestCheckpointEnricher_TypeAssertion_DefaultController(t *testing.T) {
	store := newMockCheckpointStore()
	var controller InterruptController = NewInterruptController(nil, store, nil)

	enricher, ok := controller.(CheckpointEnricher)
	if !ok {
		t.Fatal("DefaultInterruptController should implement CheckpointEnricher")
	}

	// Verify it's usable
	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-test",
		StepResults:  map[string]*StepResult{"step-1": {StepID: "step-1"}},
	}
	if err := enricher.SaveEnrichedCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatalf("SaveEnrichedCheckpoint via type assertion failed: %v", err)
	}
	if _, exists := store.checkpoints["cp-test"]; !exists {
		t.Error("expected checkpoint to be saved via type-asserted enricher")
	}
}

func TestCheckpointEnricher_TypeAssertion_NonImplementor_GracefulNoOp(t *testing.T) {
	// mockInterruptController does NOT implement CheckpointEnricher.
	// The orchestrator's type assertion should gracefully skip the save.
	var controller InterruptController = newMockInterruptController()

	_, ok := controller.(CheckpointEnricher)
	if ok {
		t.Error("mockInterruptController should NOT implement CheckpointEnricher — type assertion should return false")
	}
}

// =============================================================================
// RC8: Plan Source Guard Tests (unit-level)
// =============================================================================

// These tests verify the planSource-based logic at the unit level.
// The orchestrator-level integration (executePhaseLoop with mocked planner/executor)
// is tested via the existing iterative_planning_test.go patterns.

func TestValidateNoStepIDConflicts_ResumeScenario_WouldConflict(t *testing.T) {
	// This test documents the scenario that RC8 guards against:
	// A resume plan intentionally contains already-executed step IDs.
	// Without the planSource guard, validateNoStepIDConflicts would reject it.
	plan := &RoutingPlan{
		PlanID: "plan-highmem-incident-phase2",
		Steps: []RoutingStep{
			{StepID: "step-5", AgentName: "jira-tool"},
			{StepID: "step-6", AgentName: "slack-tool"},
		},
	}
	executedStepIDs := []string{"step-1", "step-2", "step-3", "step-4", "step-5"}

	// The conflict check WOULD fire (step-5 overlaps)
	err := validateNoStepIDConflicts(plan, executedStepIDs)
	if err == nil {
		t.Fatal("expected conflict for overlapping step-5 — this is the scenario RC8 guards against")
	}

	// In the orchestrator, planSource == "hitl_resume" skips this check entirely.
	// The executor's WithCompletedSteps handles the overlap by skipping step-5.
}

func TestValidateNoStepIDConflicts_ContinuationPlan_StillDetectsConflict(t *testing.T) {
	// LLM-generated continuation plans should still be checked for conflicts.
	plan := &RoutingPlan{
		PlanID: "plan-continuation-002",
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "weather-tool"}, // conflict!
			{StepID: "step-7", AgentName: "news-tool"},
		},
	}
	executedStepIDs := []string{"step-1", "step-2", "step-3", "step-4", "step-5", "step-6"}

	err := validateNoStepIDConflicts(plan, executedStepIDs)
	if err == nil {
		t.Fatal("expected conflict detection for LLM-generated continuation plan with duplicate step-1")
	}
}

func TestValidateNoStepIDConflicts_ResumePhase1_NoConflict(t *testing.T) {
	// Phase 1 resume plans have no prior executed steps, so no conflict is possible.
	// In the orchestrator, phaseCount == 1 means the check is skipped anyway.
	plan := &RoutingPlan{
		PlanID: "plan-phase1",
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "jira-tool"},
			{StepID: "step-2", AgentName: "slack-tool"},
		},
	}

	err := validateNoStepIDConflicts(plan, []string{})
	if err != nil {
		t.Errorf("expected no conflict for phase 1 resume (empty executedStepIDs), got: %v", err)
	}
}

// =============================================================================
// RC8 + RC9: Context Helpers for Resume (verify contract)
// =============================================================================

func TestResumeContextHelpers_PlanOverrideAndCompletedSteps(t *testing.T) {
	// Verify that WithPlanOverride and WithCompletedSteps round-trip correctly.
	// These are used by BuildResumeContext (RC9 depends on completedSteps being
	// loaded from the enriched checkpoint in DB 6).
	ctx := context.Background()

	plan := &RoutingPlan{
		PlanID:      "plan-resume",
		PhaseNumber: 2,
		Steps: []RoutingStep{
			{StepID: "step-5", AgentName: "jira-tool"},
			{StepID: "step-6", AgentName: "slack-tool"},
		},
	}

	completedSteps := map[string]*StepResult{
		"step-1": {StepID: "step-1", Response: "r1", Success: true},
		"step-2": {StepID: "step-2", Response: "r2", Success: true},
		"step-3": {StepID: "step-3", Response: `{"data":{"ticket_id":"ABC-123"}}`, Success: true},
		"step-4": {StepID: "step-4", Response: "r4", Success: true},
		"step-5": {StepID: "step-5", Response: "r5", Success: true},
	}

	ctx = WithPlanOverride(ctx, plan)
	ctx = WithCompletedSteps(ctx, completedSteps)

	// Verify plan override
	retrieved := GetPlanOverride(ctx)
	if retrieved == nil {
		t.Fatal("expected plan override in context")
	}
	if retrieved.PlanID != "plan-resume" {
		t.Errorf("expected plan ID 'plan-resume', got %q", retrieved.PlanID)
	}

	// Verify completed steps — all 5 steps including cross-phase ones
	steps := GetCompletedSteps(ctx)
	if len(steps) != 5 {
		t.Errorf("expected 5 completed steps, got %d", len(steps))
	}

	// Verify cross-phase step (step-3 from Phase 1) is accessible for template resolution
	step3, ok := steps["step-3"]
	if !ok {
		t.Fatal("expected step-3 (cross-phase) to be in completed steps — required for template resolution")
	}
	if step3.Response != `{"data":{"ticket_id":"ABC-123"}}` {
		t.Errorf("expected step-3 response with ticket_id, got %q", step3.Response)
	}
}

// =============================================================================
// RC9: Checkpoint Enrichment Simulation
// =============================================================================

func TestCheckpointEnrichment_PlanLevel(t *testing.T) {
	// Simulates the plan-level enrichment flow (Site 1):
	// 1. createCheckpoint saves StepResults={} to DB 6
	// 2. Orchestrator enriches with allStepResults
	// 3. SaveEnrichedCheckpoint overwrites DB 6 with enriched version
	store := newMockCheckpointStore()
	controller := NewInterruptController(nil, store, nil)

	// Step 1: createCheckpoint saves initial (empty) checkpoint
	initial := &ExecutionCheckpoint{
		CheckpointID: "cp-plan-001",
		Plan: &RoutingPlan{
			PlanID: "plan-phase2",
			Steps: []RoutingStep{
				{StepID: "step-5", AgentName: "jira-tool"},
				{StepID: "step-6", AgentName: "slack-tool"},
			},
		},
		StepResults: make(map[string]*StepResult), // empty at creation time
	}
	if err := store.SaveCheckpoint(context.Background(), initial); err != nil {
		t.Fatalf("precondition: save initial checkpoint: %v", err)
	}

	// Verify DB 6 has empty step results
	loaded, _ := store.LoadCheckpoint(context.Background(), "cp-plan-001")
	if len(loaded.StepResults) != 0 {
		t.Fatalf("precondition: expected empty step results, got %d", len(loaded.StepResults))
	}

	// Step 2: Orchestrator enriches the local object (simulates lines 1710-1715)
	allStepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Response: "r1", Success: true},
		"step-2": {StepID: "step-2", Response: "r2", Success: true},
		"step-3": {StepID: "step-3", Response: `{"data":{"ticket_id":"ABC-123"}}`, Success: true},
		"step-4": {StepID: "step-4", Response: "r4", Success: true},
	}
	for stepID, result := range allStepResults {
		initial.StepResults[stepID] = result
	}

	// Step 3: RC9 saves enriched checkpoint back to DB 6
	if err := controller.SaveEnrichedCheckpoint(context.Background(), initial); err != nil {
		t.Fatalf("SaveEnrichedCheckpoint failed: %v", err)
	}

	// Verify DB 6 now has all 4 prior-phase step results
	reloaded, _ := store.LoadCheckpoint(context.Background(), "cp-plan-001")
	if len(reloaded.StepResults) != 4 {
		t.Errorf("expected 4 step results after enrichment, got %d", len(reloaded.StepResults))
	}
	for _, stepID := range []string{"step-1", "step-2", "step-3", "step-4"} {
		if _, exists := reloaded.StepResults[stepID]; !exists {
			t.Errorf("expected step %s in enriched checkpoint", stepID)
		}
	}
}

func TestCheckpointEnrichment_StepLevel(t *testing.T) {
	// Simulates the step-level enrichment flow (Site 2):
	// 1. UpdateCheckpointProgress saves current-batch steps {step-5} to DB 6
	// 2. Orchestrator enriches with allStepResults from prior phases
	// 3. SaveEnrichedCheckpoint overwrites DB 6 with enriched version
	store := newMockCheckpointStore()
	controller := NewInterruptController(nil, store, nil)

	// Step 1: UpdateCheckpointProgress saves only current-batch step
	afterProgress := &ExecutionCheckpoint{
		CheckpointID: "cp-step-001",
		Plan: &RoutingPlan{
			PlanID: "plan-phase2",
			Steps: []RoutingStep{
				{StepID: "step-5", AgentName: "jira-tool"},
				{StepID: "step-6", AgentName: "slack-tool"},
			},
		},
		StepResults: map[string]*StepResult{
			"step-5": {StepID: "step-5", Response: "jira-result", Success: true},
		},
	}
	if err := store.SaveCheckpoint(context.Background(), afterProgress); err != nil {
		t.Fatalf("precondition: save progress checkpoint: %v", err)
	}

	// Verify DB 6 only has step-5
	loaded, _ := store.LoadCheckpoint(context.Background(), "cp-step-001")
	if len(loaded.StepResults) != 1 {
		t.Fatalf("precondition: expected 1 step result, got %d", len(loaded.StepResults))
	}

	// Step 2: Orchestrator enriches with allStepResults (simulates lines 1777-1790)
	allStepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Response: "r1", Success: true},
		"step-2": {StepID: "step-2", Response: "r2", Success: true},
		"step-3": {StepID: "step-3", Response: `{"data":{"ticket_id":"ABC-123"}}`, Success: true},
		"step-4": {StepID: "step-4", Response: "r4", Success: true},
	}
	for stepID, result := range allStepResults {
		if _, exists := afterProgress.StepResults[stepID]; !exists {
			afterProgress.StepResults[stepID] = result
		}
	}
	afterProgress.AccumulatedResults = make(map[string]*StepResult)
	for k, v := range allStepResults {
		afterProgress.AccumulatedResults[k] = v
	}
	for k, v := range afterProgress.StepResults {
		afterProgress.AccumulatedResults[k] = v
	}
	afterProgress.ExecutedStepIDs = []string{"step-1", "step-2", "step-3", "step-4", "step-5"}

	// Step 3: RC9 saves enriched checkpoint back to DB 6
	if err := controller.SaveEnrichedCheckpoint(context.Background(), afterProgress); err != nil {
		t.Fatalf("SaveEnrichedCheckpoint failed: %v", err)
	}

	// Verify DB 6 now has all 5 step results (prior-phase + current-batch)
	reloaded, _ := store.LoadCheckpoint(context.Background(), "cp-step-001")
	if len(reloaded.StepResults) != 5 {
		t.Errorf("expected 5 step results after enrichment, got %d", len(reloaded.StepResults))
	}
	// Verify cross-phase step-3 is present (critical for template resolution)
	if step3, ok := reloaded.StepResults["step-3"]; !ok {
		t.Error("expected step-3 (cross-phase) in enriched checkpoint")
	} else if step3.Response != `{"data":{"ticket_id":"ABC-123"}}` {
		t.Errorf("expected step-3 response with ticket_id, got %q", step3.Response)
	}
	// Verify accumulated results
	if len(reloaded.AccumulatedResults) != 5 {
		t.Errorf("expected 5 accumulated results, got %d", len(reloaded.AccumulatedResults))
	}
	// Verify executed step IDs
	if len(reloaded.ExecutedStepIDs) != 5 {
		t.Errorf("expected 5 executed step IDs, got %d", len(reloaded.ExecutedStepIDs))
	}
}

func TestCheckpointEnrichment_SaveFailure_NonFatal(t *testing.T) {
	// RC9 design: save failure should be non-fatal (warn-and-continue).
	// This test verifies the controller method propagates the error so the
	// orchestrator can log a warning and continue.
	store := newMockCheckpointStore()
	store.saveErr = errors.New("redis timeout")
	controller := NewInterruptController(nil, store, nil)

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-fail",
		StepResults: map[string]*StepResult{
			"step-1": {StepID: "step-1", Success: true},
		},
	}

	err := controller.SaveEnrichedCheckpoint(context.Background(), checkpoint)

	// Error should be returned (orchestrator will log warning and continue)
	if err == nil {
		t.Fatal("expected error from failing store")
	}
	if err.Error() != "redis timeout" {
		t.Errorf("expected 'redis timeout' error, got: %v", err)
	}
}

func TestCheckpointEnrichment_EmptyCheckpointID_TypeAssertionGuard(t *testing.T) {
	// Site 2 in orchestrator.go guards with `checkpoint.CheckpointID != ""`.
	// This test documents that the controller method itself doesn't need this
	// guard — it's the orchestrator's responsibility. The controller just saves.
	store := newMockCheckpointStore()
	controller := NewInterruptController(nil, store, nil)

	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "", // Empty — non-HITL execution
		StepResults:  map[string]*StepResult{"step-1": {StepID: "step-1"}},
	}

	// The method itself doesn't reject empty IDs — the guard is in the orchestrator
	err := controller.SaveEnrichedCheckpoint(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("expected no error (guard is in orchestrator, not controller), got: %v", err)
	}
}
