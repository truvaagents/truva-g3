package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"
)

// =============================================================================
// Mock Policy Implementation for Controller Testing
// =============================================================================

// mockPolicy implements InterruptPolicy for testing
type mockPolicy struct {
	planDecision       *InterruptDecision
	planError          error
	beforeStepDecision *InterruptDecision
	beforeStepError    error
	afterStepDecision  *InterruptDecision
	afterStepError     error
	escalateDecision   *InterruptDecision
	escalateError      error

	// Call tracking
	planCalls       int
	beforeStepCalls int
	afterStepCalls  int
	escalateCalls   int
}

func (m *mockPolicy) ShouldApprovePlan(ctx context.Context, plan *RoutingPlan) (*InterruptDecision, error) {
	m.planCalls++
	if m.planError != nil {
		return nil, m.planError
	}
	if m.planDecision == nil {
		return &InterruptDecision{ShouldInterrupt: false}, nil
	}
	return m.planDecision, nil
}

func (m *mockPolicy) ShouldApproveBeforeStep(ctx context.Context, step RoutingStep, plan *RoutingPlan) (*InterruptDecision, error) {
	m.beforeStepCalls++
	if m.beforeStepError != nil {
		return nil, m.beforeStepError
	}
	if m.beforeStepDecision == nil {
		return &InterruptDecision{ShouldInterrupt: false}, nil
	}
	return m.beforeStepDecision, nil
}

func (m *mockPolicy) ShouldApproveAfterStep(ctx context.Context, step RoutingStep, result *StepResult) (*InterruptDecision, error) {
	m.afterStepCalls++
	if m.afterStepError != nil {
		return nil, m.afterStepError
	}
	if m.afterStepDecision == nil {
		return &InterruptDecision{ShouldInterrupt: false}, nil
	}
	return m.afterStepDecision, nil
}

func (m *mockPolicy) ShouldEscalateError(ctx context.Context, step RoutingStep, err error, attempts int) (*InterruptDecision, error) {
	m.escalateCalls++
	if m.escalateError != nil {
		return nil, m.escalateError
	}
	if m.escalateDecision == nil {
		return &InterruptDecision{ShouldInterrupt: false}, nil
	}
	return m.escalateDecision, nil
}

// mockInterruptHandler implements InterruptHandler for testing
type mockInterruptHandler struct {
	notifyError error
	waitCommand *Command
	waitError   error
	submitError error

	// Call tracking
	notifyCalls int
	waitCalls   int
	submitCalls int
}

func (m *mockInterruptHandler) NotifyInterrupt(ctx context.Context, checkpoint *ExecutionCheckpoint) error {
	m.notifyCalls++
	return m.notifyError
}

func (m *mockInterruptHandler) WaitForCommand(ctx context.Context, checkpointID string, timeout time.Duration) (*Command, error) {
	m.waitCalls++
	if m.waitError != nil {
		return nil, m.waitError
	}
	return m.waitCommand, nil
}

func (m *mockInterruptHandler) SubmitCommand(ctx context.Context, command *Command) error {
	m.submitCalls++
	return m.submitError
}

// =============================================================================
// NewInterruptController Tests
// =============================================================================

func TestNewInterruptController(t *testing.T) {
	policy := &mockPolicy{}
	store := newMockCheckpointStore()
	handler := &mockInterruptHandler{}

	controller := NewInterruptController(policy, store, handler)

	if controller == nil {
		t.Fatal("NewInterruptController should return non-nil controller")
	}
	if controller.policy != policy {
		t.Error("Policy not set correctly")
	}
	if controller.store != store {
		t.Error("Store not set correctly")
	}
	if controller.handler != handler {
		t.Error("Handler not set correctly")
	}
}

func TestNewInterruptController_WithOptions(t *testing.T) {
	policy := &mockPolicy{}
	store := newMockCheckpointStore()
	handler := &mockInterruptHandler{}

	controller := NewInterruptController(policy, store, handler,
		WithControllerLogger(nil), // NoOp logger
	)

	if controller == nil {
		t.Fatal("NewInterruptController should return non-nil controller")
	}
}

// =============================================================================
// SetPolicy/SetHandler/SetCheckpointStore Tests
// =============================================================================

func TestSetPolicy(t *testing.T) {
	controller := NewInterruptController(&mockPolicy{}, newMockCheckpointStore(), &mockInterruptHandler{})

	newPolicy := &mockPolicy{}
	controller.SetPolicy(newPolicy)

	if controller.policy != newPolicy {
		t.Error("SetPolicy should update the policy")
	}
}

func TestSetHandler(t *testing.T) {
	controller := NewInterruptController(&mockPolicy{}, newMockCheckpointStore(), &mockInterruptHandler{})

	newHandler := &mockInterruptHandler{}
	controller.SetHandler(newHandler)

	if controller.handler != newHandler {
		t.Error("SetHandler should update the handler")
	}
}

func TestSetCheckpointStore(t *testing.T) {
	controller := NewInterruptController(&mockPolicy{}, newMockCheckpointStore(), &mockInterruptHandler{})

	newStore := newMockCheckpointStore()
	controller.SetCheckpointStore(newStore)

	if controller.store != newStore {
		t.Error("SetCheckpointStore should update the store")
	}
}

// =============================================================================
// CheckPlanApproval Tests
// =============================================================================

func TestCheckPlanApproval_NoInterrupt(t *testing.T) {
	policy := &mockPolicy{
		planDecision: &InterruptDecision{ShouldInterrupt: false},
	}
	controller := NewInterruptController(policy, newMockCheckpointStore(), &mockInterruptHandler{})

	plan := &RoutingPlan{PlanID: "plan-1", Steps: []RoutingStep{{StepID: "step-1"}}}
	checkpoint, err := controller.CheckPlanApproval(context.Background(), plan)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint != nil {
		t.Error("Should return nil checkpoint when no interrupt")
	}
	if policy.planCalls != 1 {
		t.Errorf("Policy should be called once, got %d", policy.planCalls)
	}
}

func TestCheckPlanApproval_WithInterrupt(t *testing.T) {
	policy := &mockPolicy{
		planDecision: &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonSensitiveOperation,
			Message:         "Plan requires approval",
			Priority:        PriorityHigh,
		},
	}
	store := newMockCheckpointStore()
	handler := &mockInterruptHandler{}
	controller := NewInterruptController(policy, store, handler)

	plan := &RoutingPlan{PlanID: "plan-1", Steps: []RoutingStep{{StepID: "step-1"}}}
	checkpoint, err := controller.CheckPlanApproval(context.Background(), plan)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("Should return checkpoint when interrupt triggered")
	}
	if checkpoint.Status != CheckpointStatusPending {
		t.Errorf("Checkpoint status should be pending, got: %s", checkpoint.Status)
	}
	if checkpoint.InterruptPoint != InterruptPointPlanGenerated {
		t.Errorf("InterruptPoint should be plan_generated, got: %s", checkpoint.InterruptPoint)
	}
	if handler.notifyCalls != 1 {
		t.Errorf("NotifyInterrupt should be called once, got %d", handler.notifyCalls)
	}
}

func TestCheckPlanApproval_NilPolicy(t *testing.T) {
	controller := NewInterruptController(nil, newMockCheckpointStore(), &mockInterruptHandler{})

	plan := &RoutingPlan{PlanID: "plan-1"}
	checkpoint, err := controller.CheckPlanApproval(context.Background(), plan)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint != nil {
		t.Error("Should return nil checkpoint when policy is nil")
	}
}

func TestCheckPlanApproval_PolicyError(t *testing.T) {
	policy := &mockPolicy{
		planError: errors.New("policy evaluation failed"),
	}
	controller := NewInterruptController(policy, newMockCheckpointStore(), &mockInterruptHandler{})

	plan := &RoutingPlan{PlanID: "plan-1"}
	_, err := controller.CheckPlanApproval(context.Background(), plan)

	if err == nil {
		t.Fatal("Should return error when policy fails")
	}
	if !containsSubstring(err.Error(), "policy check failed") {
		t.Errorf("Error should mention policy check failed, got: %s", err.Error())
	}
}

func TestCheckPlanApproval_SaveError(t *testing.T) {
	policy := &mockPolicy{
		planDecision: &InterruptDecision{ShouldInterrupt: true, Reason: ReasonPlanApproval},
	}
	store := newMockCheckpointStore()
	store.saveErr = errors.New("redis connection failed")
	controller := NewInterruptController(policy, store, &mockInterruptHandler{})

	plan := &RoutingPlan{PlanID: "plan-1"}
	_, err := controller.CheckPlanApproval(context.Background(), plan)

	if err == nil {
		t.Fatal("Should return error when save fails")
	}
	if !containsSubstring(err.Error(), "failed to save checkpoint") {
		t.Errorf("Error should mention save failure, got: %s", err.Error())
	}
}

func TestCheckPlanApproval_NotifyError_ContinuesSuccessfully(t *testing.T) {
	policy := &mockPolicy{
		planDecision: &InterruptDecision{ShouldInterrupt: true, Reason: ReasonPlanApproval},
	}
	store := newMockCheckpointStore()
	handler := &mockInterruptHandler{notifyError: errors.New("webhook failed")}
	controller := NewInterruptController(policy, store, handler)

	plan := &RoutingPlan{PlanID: "plan-1"}
	checkpoint, err := controller.CheckPlanApproval(context.Background(), plan)

	// Notify failure should NOT cause the operation to fail
	if err != nil {
		t.Fatalf("Notify error should not cause failure: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("Checkpoint should be returned despite notify error")
	}
}

func TestCheckPlanApproval_ResumeMode_Skipped(t *testing.T) {
	policy := &mockPolicy{
		planDecision: &InterruptDecision{ShouldInterrupt: true},
	}
	controller := NewInterruptController(policy, newMockCheckpointStore(), &mockInterruptHandler{})

	// Set resume mode in context
	ctx := WithResumeMode(context.Background(), "cp-resume-123")
	plan := &RoutingPlan{PlanID: "plan-1"}

	checkpoint, err := controller.CheckPlanApproval(ctx, plan)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint != nil {
		t.Error("Should return nil when in resume mode")
	}
	if policy.planCalls != 0 {
		t.Errorf("Policy should NOT be called in resume mode, got %d calls", policy.planCalls)
	}
}

// TestCheckPlanApproval_RequestMode_Preserved verifies RequestMode from context is stored in checkpoint.
// This is CRITICAL for HITL expiry behavior - streaming mode uses implicit_deny, not apply_default.
func TestCheckPlanApproval_RequestMode_Preserved(t *testing.T) {
	testCases := []struct {
		name        string
		requestMode RequestMode
	}{
		{"streaming mode", RequestModeStreaming},
		{"non_streaming mode", RequestModeNonStreaming},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			policy := &mockPolicy{
				planDecision: &InterruptDecision{
					ShouldInterrupt: true,
					Reason:          ReasonSensitiveOperation,
					Message:         "Plan requires approval",
				},
			}
			store := newMockCheckpointStore()
			handler := &mockInterruptHandler{}
			controller := NewInterruptController(policy, store, handler)

			// Create context with RequestMode set
			ctx := WithRequestMode(context.Background(), tc.requestMode)

			plan := &RoutingPlan{PlanID: "plan-1", Steps: []RoutingStep{{StepID: "step-1"}}}
			checkpoint, err := controller.CheckPlanApproval(ctx, plan)

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if checkpoint == nil {
				t.Fatal("Should return checkpoint when interrupt triggered")
			}

			// Verify RequestMode is preserved in checkpoint
			if checkpoint.RequestMode != tc.requestMode {
				t.Errorf("RequestMode mismatch: got %q, want %q", checkpoint.RequestMode, tc.requestMode)
			}

			// Also verify it was saved to the store with correct RequestMode
			savedCp, _ := store.LoadCheckpoint(ctx, checkpoint.CheckpointID)
			if savedCp.RequestMode != tc.requestMode {
				t.Errorf("Saved checkpoint RequestMode mismatch: got %q, want %q", savedCp.RequestMode, tc.requestMode)
			}
		})
	}
}

// TestCheckPlanApproval_RequestMode_NotSet_EmptyInCheckpoint verifies behavior when RequestMode is not set.
// When not set, the checkpoint should have an empty RequestMode (which will trigger default behavior on expiry).
func TestCheckPlanApproval_RequestMode_NotSet_EmptyInCheckpoint(t *testing.T) {
	policy := &mockPolicy{
		planDecision: &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonSensitiveOperation,
			Message:         "Plan requires approval",
		},
	}
	store := newMockCheckpointStore()
	handler := &mockInterruptHandler{}
	controller := NewInterruptController(policy, store, handler)

	// Use context WITHOUT RequestMode set
	ctx := context.Background()

	plan := &RoutingPlan{PlanID: "plan-1", Steps: []RoutingStep{{StepID: "step-1"}}}
	checkpoint, err := controller.CheckPlanApproval(ctx, plan)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("Should return checkpoint when interrupt triggered")
	}

	// Verify RequestMode is empty when not set in context
	if checkpoint.RequestMode != "" {
		t.Errorf("RequestMode should be empty when not set in context, got %q", checkpoint.RequestMode)
	}
}

// =============================================================================
// CheckBeforeStep Tests
// =============================================================================

func TestCheckBeforeStep_NoInterrupt(t *testing.T) {
	policy := &mockPolicy{
		beforeStepDecision: &InterruptDecision{ShouldInterrupt: false},
	}
	controller := NewInterruptController(policy, newMockCheckpointStore(), &mockInterruptHandler{})

	step := RoutingStep{StepID: "step-1", AgentName: "agent-1"}
	plan := &RoutingPlan{PlanID: "plan-1"}

	checkpoint, err := controller.CheckBeforeStep(context.Background(), step, plan)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint != nil {
		t.Error("Should return nil checkpoint when no interrupt")
	}
}

func TestCheckBeforeStep_WithInterrupt(t *testing.T) {
	policy := &mockPolicy{
		beforeStepDecision: &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonSensitiveOperation,
		},
	}
	store := newMockCheckpointStore()
	handler := &mockInterruptHandler{}
	controller := NewInterruptController(policy, store, handler)

	step := RoutingStep{StepID: "step-1", AgentName: "payment-agent"}
	plan := &RoutingPlan{PlanID: "plan-1"}

	checkpoint, err := controller.CheckBeforeStep(context.Background(), step, plan)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("Should return checkpoint when interrupt triggered")
	}
	if checkpoint.InterruptPoint != InterruptPointBeforeStep {
		t.Errorf("InterruptPoint should be before_step, got: %s", checkpoint.InterruptPoint)
	}
	if checkpoint.CurrentStep == nil || checkpoint.CurrentStep.StepID != "step-1" {
		t.Error("CurrentStep should be set")
	}
}

func TestCheckBeforeStep_PolicyError(t *testing.T) {
	policy := &mockPolicy{
		beforeStepError: errors.New("policy failed"),
	}
	controller := NewInterruptController(policy, newMockCheckpointStore(), &mockInterruptHandler{})

	step := RoutingStep{StepID: "step-1"}
	plan := &RoutingPlan{PlanID: "plan-1"}

	_, err := controller.CheckBeforeStep(context.Background(), step, plan)

	if err == nil {
		t.Fatal("Should return error when policy fails")
	}
}

func TestCheckBeforeStep_NilPolicy(t *testing.T) {
	controller := NewInterruptController(nil, newMockCheckpointStore(), &mockInterruptHandler{})

	step := RoutingStep{StepID: "step-1"}
	plan := &RoutingPlan{PlanID: "plan-1"}

	checkpoint, err := controller.CheckBeforeStep(context.Background(), step, plan)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint != nil {
		t.Error("Should return nil when policy is nil")
	}
}

// =============================================================================
// CheckAfterStep Tests
// =============================================================================

func TestCheckAfterStep_NoInterrupt(t *testing.T) {
	policy := &mockPolicy{
		afterStepDecision: &InterruptDecision{ShouldInterrupt: false},
	}
	controller := NewInterruptController(policy, newMockCheckpointStore(), &mockInterruptHandler{})

	step := RoutingStep{StepID: "step-1"}
	result := &StepResult{StepID: "step-1", Success: true}

	checkpoint, err := controller.CheckAfterStep(context.Background(), step, result)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint != nil {
		t.Error("Should return nil checkpoint when no interrupt")
	}
}

func TestCheckAfterStep_WithInterrupt(t *testing.T) {
	policy := &mockPolicy{
		afterStepDecision: &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonOutputValidation,
		},
	}
	store := newMockCheckpointStore()
	controller := NewInterruptController(policy, store, &mockInterruptHandler{})

	step := RoutingStep{StepID: "step-1"}
	result := &StepResult{StepID: "step-1", Success: true}

	checkpoint, err := controller.CheckAfterStep(context.Background(), step, result)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("Should return checkpoint when interrupt triggered")
	}
	if checkpoint.InterruptPoint != InterruptPointAfterStep {
		t.Errorf("InterruptPoint should be after_step, got: %s", checkpoint.InterruptPoint)
	}
}

func TestCheckAfterStep_NilPolicy(t *testing.T) {
	controller := NewInterruptController(nil, newMockCheckpointStore(), &mockInterruptHandler{})

	step := RoutingStep{StepID: "step-1"}
	result := &StepResult{StepID: "step-1"}

	checkpoint, err := controller.CheckAfterStep(context.Background(), step, result)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint != nil {
		t.Error("Should return nil when policy is nil")
	}
}

// =============================================================================
// CheckOnError Tests
// =============================================================================

func TestCheckOnError_NoEscalation(t *testing.T) {
	policy := &mockPolicy{
		escalateDecision: &InterruptDecision{ShouldInterrupt: false},
	}
	controller := NewInterruptController(policy, newMockCheckpointStore(), &mockInterruptHandler{})

	step := RoutingStep{StepID: "step-1"}
	stepErr := errors.New("step failed")

	checkpoint, err := controller.CheckOnError(context.Background(), step, stepErr, 1)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint != nil {
		t.Error("Should return nil checkpoint when no escalation")
	}
}

func TestCheckOnError_WithEscalation(t *testing.T) {
	policy := &mockPolicy{
		escalateDecision: &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonEscalation,
		},
	}
	store := newMockCheckpointStore()
	controller := NewInterruptController(policy, store, &mockInterruptHandler{})

	step := RoutingStep{StepID: "step-1"}
	stepErr := errors.New("step failed after 3 attempts")

	checkpoint, err := controller.CheckOnError(context.Background(), step, stepErr, 3)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("Should return checkpoint when escalation triggered")
	}
	if checkpoint.InterruptPoint != InterruptPointOnError {
		t.Errorf("InterruptPoint should be on_error, got: %s", checkpoint.InterruptPoint)
	}
	// Verify error metadata is stored
	if checkpoint.Decision.Metadata == nil {
		t.Fatal("Decision metadata should be set")
	}
	if checkpoint.Decision.Metadata["original_error"] != "step failed after 3 attempts" {
		t.Error("Original error should be in metadata")
	}
	if checkpoint.Decision.Metadata["attempts"] != 3 {
		t.Error("Attempts should be in metadata")
	}
}

func TestCheckOnError_NilPolicy(t *testing.T) {
	controller := NewInterruptController(nil, newMockCheckpointStore(), &mockInterruptHandler{})

	step := RoutingStep{StepID: "step-1"}
	stepErr := errors.New("error")

	checkpoint, err := controller.CheckOnError(context.Background(), step, stepErr, 1)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if checkpoint != nil {
		t.Error("Should return nil when policy is nil")
	}
}

// =============================================================================
// ProcessCommand Tests
// =============================================================================

func TestProcessCommand_Approve(t *testing.T) {
	store := newMockCheckpointStore()
	// Pre-populate checkpoint
	store.checkpoints["cp-approve"] = &ExecutionCheckpoint{
		CheckpointID: "cp-approve",
		Status:       CheckpointStatusPending,
		CreatedAt:    time.Now().Add(-1 * time.Minute),
		Decision:     &InterruptDecision{Reason: ReasonPlanApproval},
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-approve",
		Type:         CommandApprove,
		UserID:       "user-1",
	}

	result, err := controller.ProcessCommand(context.Background(), command)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if !result.ShouldResume {
		t.Error("ShouldResume should be true for approve")
	}
	if result.Action != CommandApprove {
		t.Errorf("Action should be approve, got: %s", result.Action)
	}
}

func TestProcessCommand_Reject(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-reject"] = &ExecutionCheckpoint{
		CheckpointID: "cp-reject",
		Status:       CheckpointStatusPending,
		CreatedAt:    time.Now(),
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-reject",
		Type:         CommandReject,
		Feedback:     "Request denied",
	}

	result, err := controller.ProcessCommand(context.Background(), command)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result.ShouldResume {
		t.Error("ShouldResume should be false for reject")
	}
	if result.Feedback != "Request denied" {
		t.Error("Feedback should be set")
	}
}

func TestProcessCommand_Edit(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-edit"] = &ExecutionCheckpoint{
		CheckpointID: "cp-edit",
		Status:       CheckpointStatusPending,
		CreatedAt:    time.Now(),
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	editedPlan := &RoutingPlan{PlanID: "edited-plan"}
	command := &Command{
		CheckpointID: "cp-edit",
		Type:         CommandEdit,
		EditedPlan:   editedPlan,
	}

	result, err := controller.ProcessCommand(context.Background(), command)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.ShouldResume {
		t.Error("ShouldResume should be true for edit")
	}
	if result.ModifiedPlan != editedPlan {
		t.Error("ModifiedPlan should be set")
	}
}

func TestProcessCommand_Skip(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-skip"] = &ExecutionCheckpoint{
		CheckpointID: "cp-skip",
		Status:       CheckpointStatusPending,
		CreatedAt:    time.Now(),
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-skip",
		Type:         CommandSkip,
	}

	result, err := controller.ProcessCommand(context.Background(), command)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.ShouldResume {
		t.Error("ShouldResume should be true for skip")
	}
	if !result.SkipStep {
		t.Error("SkipStep should be true")
	}
}

func TestProcessCommand_Abort(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-abort"] = &ExecutionCheckpoint{
		CheckpointID: "cp-abort",
		Status:       CheckpointStatusPending,
		CreatedAt:    time.Now(),
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-abort",
		Type:         CommandAbort,
		Feedback:     "Abort requested",
	}

	result, err := controller.ProcessCommand(context.Background(), command)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result.ShouldResume {
		t.Error("ShouldResume should be false for abort")
	}
	if !result.Abort {
		t.Error("Abort should be true")
	}
}

func TestProcessCommand_Retry(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-retry"] = &ExecutionCheckpoint{
		CheckpointID: "cp-retry",
		Status:       CheckpointStatusPending,
		CreatedAt:    time.Now(),
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-retry",
		Type:         CommandRetry,
	}

	result, err := controller.ProcessCommand(context.Background(), command)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.ShouldResume {
		t.Error("ShouldResume should be true for retry")
	}
}

func TestProcessCommand_Respond(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-respond"] = &ExecutionCheckpoint{
		CheckpointID: "cp-respond",
		Status:       CheckpointStatusPending,
		CreatedAt:    time.Now(),
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-respond",
		Type:         CommandRespond,
		Response:     "Additional context provided",
	}

	result, err := controller.ProcessCommand(context.Background(), command)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.ShouldResume {
		t.Error("ShouldResume should be true for respond")
	}
}

func TestProcessCommand_InvalidType(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-invalid"] = &ExecutionCheckpoint{
		CheckpointID: "cp-invalid",
		Status:       CheckpointStatusPending,
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-invalid",
		Type:         CommandType("unknown"),
	}

	_, err := controller.ProcessCommand(context.Background(), command)

	if err == nil {
		t.Fatal("Should return error for invalid command type")
	}
	if !IsInvalidCommand(err) {
		t.Errorf("Error should be ErrInvalidCommand, got: %v", err)
	}
}

func TestProcessCommand_NotPending(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-not-pending"] = &ExecutionCheckpoint{
		CheckpointID: "cp-not-pending",
		Status:       CheckpointStatusApproved, // Already approved
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-not-pending",
		Type:         CommandApprove,
	}

	_, err := controller.ProcessCommand(context.Background(), command)

	if err == nil {
		t.Fatal("Should return error for non-pending checkpoint")
	}
	if !IsInvalidCommand(err) {
		t.Errorf("Error should be ErrInvalidCommand, got: %v", err)
	}
}

func TestProcessCommand_CheckpointNotFound(t *testing.T) {
	store := newMockCheckpointStore()
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-not-exists",
		Type:         CommandApprove,
	}

	_, err := controller.ProcessCommand(context.Background(), command)

	if err == nil {
		t.Fatal("Should return error for non-existent checkpoint")
	}
	if !IsCheckpointNotFound(err) {
		t.Errorf("Error should be ErrCheckpointNotFound, got: %v", err)
	}
}

func TestProcessCommand_NilStore(t *testing.T) {
	controller := NewInterruptController(&mockPolicy{}, nil, &mockInterruptHandler{})

	command := &Command{
		CheckpointID: "cp-1",
		Type:         CommandApprove,
	}

	_, err := controller.ProcessCommand(context.Background(), command)

	if err == nil {
		t.Fatal("Should return error when store is nil")
	}
}

// =============================================================================
// ResumeExecution Tests
// =============================================================================

func TestResumeExecution_Success(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-resume"] = &ExecutionCheckpoint{
		CheckpointID:   "cp-resume",
		RequestID:      "req-1",
		Status:         CheckpointStatusApproved,
		InterruptPoint: InterruptPointPlanGenerated,
		Plan:           &RoutingPlan{PlanID: "plan-1"},
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	result, err := controller.ResumeExecution(context.Background(), "cp-resume")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if !result.Success {
		t.Error("Success should be true")
	}
	if result.Metadata["resumed_from_checkpoint"] != "cp-resume" {
		t.Error("Metadata should contain checkpoint ID")
	}
}

func TestResumeExecution_EditedStatus(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-edited"] = &ExecutionCheckpoint{
		CheckpointID: "cp-edited",
		Status:       CheckpointStatusEdited,
		Plan:         &RoutingPlan{PlanID: "plan-1"},
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	result, err := controller.ResumeExecution(context.Background(), "cp-edited")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestResumeExecution_NotResumableStatus(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-rejected"] = &ExecutionCheckpoint{
		CheckpointID: "cp-rejected",
		Status:       CheckpointStatusRejected,
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	_, err := controller.ResumeExecution(context.Background(), "cp-rejected")

	if err == nil {
		t.Fatal("Should return error for non-resumable status")
	}
	if !containsSubstring(err.Error(), "not in a resumable state") {
		t.Errorf("Error should mention not resumable, got: %s", err.Error())
	}
}

func TestResumeExecution_NotFound(t *testing.T) {
	store := newMockCheckpointStore()
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	_, err := controller.ResumeExecution(context.Background(), "cp-not-exists")

	if err == nil {
		t.Fatal("Should return error for non-existent checkpoint")
	}
}

// =============================================================================
// UpdateCheckpointProgress Tests
// =============================================================================

func TestUpdateCheckpointProgress_Success(t *testing.T) {
	store := newMockCheckpointStore()
	store.checkpoints["cp-progress"] = &ExecutionCheckpoint{
		CheckpointID: "cp-progress",
		Status:       CheckpointStatusPending,
	}
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	completedSteps := []StepResult{
		{StepID: "step-1", Success: true},
		{StepID: "step-2", Success: true},
	}

	err := controller.UpdateCheckpointProgress(context.Background(), "cp-progress", completedSteps)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify checkpoint was updated
	cp := store.checkpoints["cp-progress"]
	if len(cp.CompletedSteps) != 2 {
		t.Errorf("CompletedSteps should have 2 items, got %d", len(cp.CompletedSteps))
	}
	if cp.StepResults["step-1"] == nil {
		t.Error("StepResults should contain step-1")
	}
	if cp.StepResults["step-2"] == nil {
		t.Error("StepResults should contain step-2")
	}
}

func TestUpdateCheckpointProgress_NotFound(t *testing.T) {
	store := newMockCheckpointStore()
	controller := NewInterruptController(&mockPolicy{}, store, &mockInterruptHandler{})

	err := controller.UpdateCheckpointProgress(context.Background(), "cp-not-exists", nil)

	if err == nil {
		t.Fatal("Should return error for non-existent checkpoint")
	}
}

// =============================================================================
// Interface Compliance Test
// =============================================================================

func TestDefaultInterruptController_ImplementsInterface(t *testing.T) {
	var _ InterruptController = (*DefaultInterruptController)(nil)
}
