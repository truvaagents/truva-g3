package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"
)

// =============================================================================
// RuleBasedPolicy Unit Tests
// =============================================================================
//
// These tests cover the pure policy logic with no external dependencies.
// The policy evaluates rules based on configuration and input data.
//
// =============================================================================

// -----------------------------------------------------------------------------
// NewRuleBasedPolicy Tests
// -----------------------------------------------------------------------------

func TestNewRuleBasedPolicy_DefaultLogger(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{})

	if policy.logger == nil {
		t.Error("NewRuleBasedPolicy should have a default logger")
	}
}

func TestNewRuleBasedPolicy_WithConfig(t *testing.T) {
	config := HITLConfig{
		RequirePlanApproval:   true,
		SensitiveAgents:       []string{"payment-agent"},
		SensitiveCapabilities: []string{"transfer_funds"},
		EscalateAfterRetries:  3,
		DefaultTimeout:        5 * time.Minute,
	}

	policy := NewRuleBasedPolicy(config)

	if !policy.config.RequirePlanApproval {
		t.Error("Config.RequirePlanApproval should be true")
	}
	if len(policy.config.SensitiveAgents) != 1 {
		t.Errorf("Config.SensitiveAgents length = %d, want 1", len(policy.config.SensitiveAgents))
	}
}

func TestNewRuleBasedPolicy_WithLoggerOption(t *testing.T) {
	logger := &testPolicyLogger{}
	policy := NewRuleBasedPolicy(HITLConfig{}, WithPolicyLogger(logger))

	if policy.logger != logger {
		t.Error("WithPolicyLogger should set the logger")
	}
}

// -----------------------------------------------------------------------------
// ShouldApprovePlan Tests
// -----------------------------------------------------------------------------

func TestShouldApprovePlan_NoApprovalNeeded(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		RequirePlanApproval: false,
	})

	plan := &RoutingPlan{
		PlanID: "plan-1",
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "regular-agent"},
		},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if decision.ShouldInterrupt {
		t.Error("Should not require approval for regular plan with RequirePlanApproval=false")
	}
}

func TestShouldApprovePlan_RequirePlanApprovalEnabled(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		RequirePlanApproval: true,
		DefaultTimeout:      5 * time.Minute,
	})

	plan := &RoutingPlan{
		PlanID:          "plan-1",
		OriginalRequest: "Process this request",
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "regular-agent"},
		},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Should require approval when RequirePlanApproval=true")
	}

	if decision.Reason != ReasonPlanApproval {
		t.Errorf("Reason = %q, want %q", decision.Reason, ReasonPlanApproval)
	}

	if decision.Priority != PriorityNormal {
		t.Errorf("Priority = %q, want %q", decision.Priority, PriorityNormal)
	}
}

func TestShouldApprovePlan_SensitiveAgent(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		RequirePlanApproval: false, // Even without this, sensitive should trigger
		SensitiveAgents:     []string{"payment-agent", "admin-agent"},
	})

	plan := &RoutingPlan{
		PlanID: "plan-1",
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "regular-agent"},
			{StepID: "step-2", AgentName: "payment-agent"}, // Sensitive!
		},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Should require approval for sensitive agent")
	}

	if decision.Reason != ReasonSensitiveOperation {
		t.Errorf("Reason = %q, want %q", decision.Reason, ReasonSensitiveOperation)
	}

	if decision.Priority != PriorityHigh {
		t.Errorf("Priority = %q, want %q", decision.Priority, PriorityHigh)
	}

	// Check metadata
	if decision.Metadata == nil {
		t.Fatal("Metadata should not be nil")
	}
	if !decision.Metadata["has_sensitive_ops"].(bool) {
		t.Error("Metadata[has_sensitive_ops] should be true")
	}
}

func TestShouldApprovePlan_SensitiveCapability(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		RequirePlanApproval:   false,
		SensitiveCapabilities: []string{"transfer_funds", "delete_account"},
	})

	plan := &RoutingPlan{
		PlanID: "plan-1",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "regular-agent",
				Metadata: map[string]interface{}{
					"capability": "transfer_funds", // Sensitive!
				},
			},
		},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Should require approval for sensitive capability")
	}

	if decision.Reason != ReasonSensitiveOperation {
		t.Errorf("Reason = %q, want %q", decision.Reason, ReasonSensitiveOperation)
	}
}

func TestShouldApprovePlan_MultipleSensitiveOps(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveAgents:       []string{"payment-agent"},
		SensitiveCapabilities: []string{"delete_account"},
	})

	plan := &RoutingPlan{
		PlanID: "plan-1",
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "payment-agent"}, // Sensitive agent
			{
				StepID:    "step-2",
				AgentName: "admin-agent",
				Metadata: map[string]interface{}{
					"capability": "delete_account", // Sensitive capability
				},
			},
		},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Should require approval")
	}

	// Should have both sensitive details
	details := decision.Metadata["sensitive_details"].([]string)
	if len(details) != 2 {
		t.Errorf("Should have 2 sensitive details, got %d", len(details))
	}
}

func TestShouldApprovePlan_EmptyPlan(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		RequirePlanApproval: false,
	})

	plan := &RoutingPlan{
		PlanID: "plan-1",
		Steps:  []RoutingStep{}, // Empty
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if decision.ShouldInterrupt {
		t.Error("Empty plan should not require approval")
	}
}

func TestShouldApprovePlan_DefaultActionAndTimeout(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		RequirePlanApproval: true,
		DefaultTimeout:      10 * time.Minute,
		DefaultAction:       CommandApprove, // Override default reject via config
	})

	plan := &RoutingPlan{
		PlanID: "plan-1",
		Steps:  []RoutingStep{{StepID: "step-1", AgentName: "agent"}},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if decision.Timeout != 10*time.Minute {
		t.Errorf("Timeout = %v, want 10m", decision.Timeout)
	}

	// Policy respects config.DefaultAction - when set to CommandApprove, decision uses it
	// (Default is CommandReject, but can be overridden via TRUVAG3_HITL_DEFAULT_ACTION or config)
	if decision.DefaultAction != CommandApprove {
		t.Errorf("DefaultAction = %q, want %q (config override)", decision.DefaultAction, CommandApprove)
	}
}

// -----------------------------------------------------------------------------
// ShouldApproveBeforeStep Tests
// -----------------------------------------------------------------------------

func TestShouldApproveBeforeStep_NoApprovalNeeded(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "regular-agent",
	}
	plan := &RoutingPlan{PlanID: "plan-1"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if decision.ShouldInterrupt {
		t.Error("Regular step should not require approval")
	}
}

func TestShouldApproveBeforeStep_SensitiveAgent(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveAgents: []string{"payment-agent"},
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "payment-agent",
	}
	plan := &RoutingPlan{PlanID: "plan-1"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Sensitive agent should require step approval")
	}

	if decision.Reason != ReasonSensitiveOperation {
		t.Errorf("Reason = %q, want %q", decision.Reason, ReasonSensitiveOperation)
	}

	if decision.Metadata["trigger"] != "sensitive_agent" {
		t.Errorf("Metadata[trigger] = %q, want %q", decision.Metadata["trigger"], "sensitive_agent")
	}
}

func TestShouldApproveBeforeStep_StepSensitiveAgent(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		StepSensitiveAgents: []string{"step-only-agent"},
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "step-only-agent",
	}
	plan := &RoutingPlan{PlanID: "plan-1"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Step-sensitive agent should require step approval")
	}

	if decision.Metadata["trigger"] != "step_sensitive_agent" {
		t.Errorf("Metadata[trigger] = %q, want %q", decision.Metadata["trigger"], "step_sensitive_agent")
	}
}

func TestShouldApproveBeforeStep_SensitiveCapability(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveCapabilities: []string{"transfer_funds"},
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
		Metadata: map[string]interface{}{
			"capability": "transfer_funds",
		},
	}
	plan := &RoutingPlan{PlanID: "plan-1"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Sensitive capability should require step approval")
	}

	if decision.Metadata["trigger"] != "sensitive_capability" {
		t.Errorf("Metadata[trigger] = %q, want %q", decision.Metadata["trigger"], "sensitive_capability")
	}
}

func TestShouldApproveBeforeStep_StepSensitiveCapability(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		StepSensitiveCapabilities: []string{"step_only_action"},
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
		Metadata: map[string]interface{}{
			"capability": "step_only_action",
		},
	}
	plan := &RoutingPlan{PlanID: "plan-1"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Step-sensitive capability should require step approval")
	}

	if decision.Metadata["trigger"] != "step_sensitive_capability" {
		t.Errorf("Metadata[trigger] = %q, want %q", decision.Metadata["trigger"], "step_sensitive_capability")
	}
}

func TestShouldApproveBeforeStep_NoCapabilityMetadata(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveCapabilities: []string{"transfer_funds"},
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
		Metadata:  nil, // No metadata
	}
	plan := &RoutingPlan{PlanID: "plan-1"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if decision.ShouldInterrupt {
		t.Error("Step without capability metadata should not trigger sensitive capability check")
	}
}

// -----------------------------------------------------------------------------
// ShouldApproveAfterStep Tests
// -----------------------------------------------------------------------------

func TestShouldApproveAfterStep_NoValidationNeeded(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
		Metadata: map[string]interface{}{
			"capability": "some_action",
		},
	}
	result := &StepResult{
		Response: "some response",
	}

	decision, err := policy.ShouldApproveAfterStep(context.Background(), step, result)
	if err != nil {
		t.Fatalf("ShouldApproveAfterStep() error = %v", err)
	}

	// Currently, requiresOutputValidation always returns false
	if decision.ShouldInterrupt {
		t.Error("Default policy should not require output validation")
	}
}

func TestShouldApproveAfterStep_NoCapabilityMetadata(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
		Metadata:  nil,
	}
	result := &StepResult{
		Response: "some response",
	}

	decision, err := policy.ShouldApproveAfterStep(context.Background(), step, result)
	if err != nil {
		t.Fatalf("ShouldApproveAfterStep() error = %v", err)
	}

	if decision.ShouldInterrupt {
		t.Error("Step without capability should not require output validation")
	}
}

// -----------------------------------------------------------------------------
// ShouldEscalateError Tests
// -----------------------------------------------------------------------------

func TestShouldEscalateError_EscalateAfterRetries(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		EscalateAfterRetries: 3,
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
	}
	err := errors.New("connection timeout")

	// At retry 3 (attempts >= threshold)
	decision, decErr := policy.ShouldEscalateError(context.Background(), step, err, 3)
	if decErr != nil {
		t.Fatalf("ShouldEscalateError() error = %v", decErr)
	}

	if !decision.ShouldInterrupt {
		t.Error("Should escalate after reaching retry threshold")
	}

	if decision.Reason != ReasonEscalation {
		t.Errorf("Reason = %q, want %q", decision.Reason, ReasonEscalation)
	}

	if decision.DefaultAction != CommandAbort {
		t.Errorf("DefaultAction = %q, want %q", decision.DefaultAction, CommandAbort)
	}

	if decision.Metadata["attempts"] != 3 {
		t.Errorf("Metadata[attempts] = %v, want 3", decision.Metadata["attempts"])
	}
}

func TestShouldEscalateError_DoNotEscalateBeforeThreshold(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		EscalateAfterRetries: 3,
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
	}
	err := errors.New("connection timeout")

	// At retry 2 (attempts < threshold)
	decision, decErr := policy.ShouldEscalateError(context.Background(), step, err, 2)
	if decErr != nil {
		t.Fatalf("ShouldEscalateError() error = %v", decErr)
	}

	if decision.ShouldInterrupt {
		t.Error("Should not escalate before reaching retry threshold")
	}
}

func TestShouldEscalateError_ThresholdZero(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		EscalateAfterRetries: 0, // Disabled
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
	}
	err := errors.New("connection timeout")

	// Even with many attempts
	decision, decErr := policy.ShouldEscalateError(context.Background(), step, err, 10)
	if decErr != nil {
		t.Fatalf("ShouldEscalateError() error = %v", decErr)
	}

	if decision.ShouldInterrupt {
		t.Error("Should not escalate when threshold is 0 (disabled)")
	}
}

func TestShouldEscalateError_ExactlyAtThreshold(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		EscalateAfterRetries: 5,
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
	}
	err := errors.New("error")

	// Exactly at threshold
	decision, decErr := policy.ShouldEscalateError(context.Background(), step, err, 5)
	if decErr != nil {
		t.Fatalf("ShouldEscalateError() error = %v", decErr)
	}

	if !decision.ShouldInterrupt {
		t.Error("Should escalate at exactly the retry threshold")
	}
}

func TestShouldEscalateError_AboveThreshold(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		EscalateAfterRetries: 3,
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
	}
	err := errors.New("error")

	// Above threshold
	decision, decErr := policy.ShouldEscalateError(context.Background(), step, err, 10)
	if decErr != nil {
		t.Fatalf("ShouldEscalateError() error = %v", decErr)
	}

	if !decision.ShouldInterrupt {
		t.Error("Should escalate when above the retry threshold")
	}
}

// -----------------------------------------------------------------------------
// NoOpPolicy Tests
// -----------------------------------------------------------------------------

func TestNewNoOpPolicy(t *testing.T) {
	policy := NewNoOpPolicy()
	if policy == nil {
		t.Error("NewNoOpPolicy should not return nil")
	}
}

func TestNoOpPolicy_ShouldApprovePlan(t *testing.T) {
	policy := NewNoOpPolicy()
	plan := &RoutingPlan{
		PlanID: "plan-1",
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "payment-agent"}, // Would be sensitive
		},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if decision.ShouldInterrupt {
		t.Error("NoOpPolicy should never require plan approval")
	}
}

func TestNoOpPolicy_ShouldApproveBeforeStep(t *testing.T) {
	policy := NewNoOpPolicy()
	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "payment-agent",
	}
	plan := &RoutingPlan{PlanID: "plan-1"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if decision.ShouldInterrupt {
		t.Error("NoOpPolicy should never require step approval")
	}
}

func TestNoOpPolicy_ShouldApproveAfterStep(t *testing.T) {
	policy := NewNoOpPolicy()
	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
	}
	result := &StepResult{Response: "response"}

	decision, err := policy.ShouldApproveAfterStep(context.Background(), step, result)
	if err != nil {
		t.Fatalf("ShouldApproveAfterStep() error = %v", err)
	}

	if decision.ShouldInterrupt {
		t.Error("NoOpPolicy should never require output validation")
	}
}

func TestNoOpPolicy_ShouldEscalateError(t *testing.T) {
	policy := NewNoOpPolicy()
	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "agent",
	}
	err := errors.New("error")

	decision, decErr := policy.ShouldEscalateError(context.Background(), step, err, 100)
	if decErr != nil {
		t.Fatalf("ShouldEscalateError() error = %v", decErr)
	}

	if decision.ShouldInterrupt {
		t.Error("NoOpPolicy should never escalate errors")
	}
}

// -----------------------------------------------------------------------------
// Helper Method Tests (via public API)
// -----------------------------------------------------------------------------

func TestPolicy_IsSensitiveAgent_Multiple(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveAgents: []string{"agent-a", "agent-b", "agent-c"},
	})

	testCases := []struct {
		agentName string
		sensitive bool
	}{
		{"agent-a", true},
		{"agent-b", true},
		{"agent-c", true},
		{"agent-d", false},
		{"", false},
	}

	for _, tc := range testCases {
		plan := &RoutingPlan{
			PlanID: "plan-1",
			Steps:  []RoutingStep{{StepID: "step-1", AgentName: tc.agentName}},
		}

		decision, _ := policy.ShouldApprovePlan(context.Background(), plan)

		if tc.sensitive && !decision.ShouldInterrupt {
			t.Errorf("Agent %q should be sensitive", tc.agentName)
		}
		if !tc.sensitive && decision.ShouldInterrupt {
			t.Errorf("Agent %q should not be sensitive", tc.agentName)
		}
	}
}

func TestPolicy_IsSensitiveCapability_Multiple(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveCapabilities: []string{"cap-a", "cap-b"},
	})

	testCases := []struct {
		capability string
		sensitive  bool
	}{
		{"cap-a", true},
		{"cap-b", true},
		{"cap-c", false},
	}

	for _, tc := range testCases {
		plan := &RoutingPlan{
			PlanID: "plan-1",
			Steps: []RoutingStep{{
				StepID:    "step-1",
				AgentName: "agent",
				Metadata:  map[string]interface{}{"capability": tc.capability},
			}},
		}

		decision, _ := policy.ShouldApprovePlan(context.Background(), plan)

		if tc.sensitive && !decision.ShouldInterrupt {
			t.Errorf("Capability %q should be sensitive", tc.capability)
		}
		if !tc.sensitive && decision.ShouldInterrupt {
			t.Errorf("Capability %q should not be sensitive", tc.capability)
		}
	}
}

// -----------------------------------------------------------------------------
// Edge Cases
// -----------------------------------------------------------------------------

func TestShouldApprovePlan_NonStringCapability(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveCapabilities: []string{"transfer_funds"},
	})

	plan := &RoutingPlan{
		PlanID: "plan-1",
		Steps: []RoutingStep{{
			StepID:    "step-1",
			AgentName: "agent",
			Metadata: map[string]interface{}{
				"capability": 123, // Not a string
			},
		}},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if decision.ShouldInterrupt {
		t.Error("Non-string capability should not trigger sensitive check")
	}
}

func TestShouldApproveBeforeStep_AgentPriorityOverCapability(t *testing.T) {
	// When agent is sensitive, the trigger should be "sensitive_agent"
	// even if capability is also sensitive
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveAgents:       []string{"payment-agent"},
		SensitiveCapabilities: []string{"transfer_funds"},
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "payment-agent", // Sensitive agent
		Metadata: map[string]interface{}{
			"capability": "transfer_funds", // Also sensitive capability
		},
	}
	plan := &RoutingPlan{PlanID: "plan-1"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	// Agent check comes first, so trigger should be agent
	if decision.Metadata["trigger"] != "sensitive_agent" {
		t.Errorf("Metadata[trigger] = %q, want %q (agent check has priority)",
			decision.Metadata["trigger"], "sensitive_agent")
	}
}

// -----------------------------------------------------------------------------
// DefaultAction by Checkpoint Type Tests (Phase 10)
// -----------------------------------------------------------------------------
// These tests verify that RuleBasedPolicy returns the correct DefaultAction
// for each checkpoint type, ensuring fail-safe behavior for sensitive operations.

func TestRuleBasedPolicy_DefaultAction_PlanCheckpoint_SensitiveOps(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveCapabilities: []string{"transfer_funds"},
		DefaultTimeout:        5 * time.Minute,
	})

	plan := &RoutingPlan{
		PlanID: "plan-123",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "payment-agent",
				Metadata:  map[string]interface{}{"capability": "transfer_funds"},
			},
		},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Expected ShouldInterrupt=true for sensitive plan")
	}

	// Plan checkpoints should reject on timeout (HITL = require explicit approval)
	if decision.DefaultAction != CommandReject {
		t.Errorf("DefaultAction = %q, want %q (HITL enabled = reject on timeout)",
			decision.DefaultAction, CommandReject)
	}
}

func TestRuleBasedPolicy_DefaultAction_PlanCheckpoint_RequirePlanApproval(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		RequirePlanApproval: true,
		DefaultTimeout:      5 * time.Minute,
	})

	plan := &RoutingPlan{
		PlanID:          "plan-456",
		OriginalRequest: "What is the weather?",
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "weather-agent"},
		},
	}

	decision, err := policy.ShouldApprovePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ShouldApprovePlan() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Expected ShouldInterrupt=true with RequirePlanApproval=true")
	}

	// Plan checkpoints should reject on timeout (HITL = require explicit approval)
	if decision.DefaultAction != CommandReject {
		t.Errorf("DefaultAction = %q, want %q (HITL enabled = reject on timeout)",
			decision.DefaultAction, CommandReject)
	}
}

func TestRuleBasedPolicy_DefaultAction_StepCheckpoint_SensitiveAgent(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveAgents: []string{"payment-agent"},
		DefaultTimeout:  5 * time.Minute,
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "payment-agent",
	}
	plan := &RoutingPlan{PlanID: "plan-789"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Expected ShouldInterrupt=true for sensitive agent")
	}

	// Step checkpoints should fail-safe (reject) on timeout
	if decision.DefaultAction != CommandReject {
		t.Errorf("DefaultAction = %q, want %q (step checkpoints fail-safe reject)",
			decision.DefaultAction, CommandReject)
	}
}

func TestRuleBasedPolicy_DefaultAction_StepCheckpoint_SensitiveCapability(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		SensitiveCapabilities: []string{"delete_account"},
		DefaultTimeout:        5 * time.Minute,
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "user-service",
		Metadata:  map[string]interface{}{"capability": "delete_account"},
	}
	plan := &RoutingPlan{PlanID: "plan-abc"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Expected ShouldInterrupt=true for sensitive capability")
	}

	// Step checkpoints should fail-safe (reject) on timeout
	if decision.DefaultAction != CommandReject {
		t.Errorf("DefaultAction = %q, want %q (step checkpoints fail-safe reject)",
			decision.DefaultAction, CommandReject)
	}
}

func TestRuleBasedPolicy_DefaultAction_StepCheckpoint_StepSensitiveCapability(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		StepSensitiveCapabilities: []string{"send_email"},
		DefaultTimeout:            5 * time.Minute,
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "email-service",
		Metadata:  map[string]interface{}{"capability": "send_email"},
	}
	plan := &RoutingPlan{PlanID: "plan-def"}

	decision, err := policy.ShouldApproveBeforeStep(context.Background(), step, plan)
	if err != nil {
		t.Fatalf("ShouldApproveBeforeStep() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Expected ShouldInterrupt=true for step-sensitive capability")
	}

	// Step checkpoints should fail-safe (reject) on timeout
	if decision.DefaultAction != CommandReject {
		t.Errorf("DefaultAction = %q, want %q (step checkpoints fail-safe reject)",
			decision.DefaultAction, CommandReject)
	}
}

func TestRuleBasedPolicy_DefaultAction_ErrorEscalation(t *testing.T) {
	policy := NewRuleBasedPolicy(HITLConfig{
		EscalateAfterRetries: 3,
		DefaultTimeout:       5 * time.Minute,
	})

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "failing-agent",
	}
	testErr := errors.New("connection timeout")

	decision, err := policy.ShouldEscalateError(context.Background(), step, testErr, 3)
	if err != nil {
		t.Fatalf("ShouldEscalateError() error = %v", err)
	}

	if !decision.ShouldInterrupt {
		t.Error("Expected ShouldInterrupt=true after retry threshold")
	}

	// Error escalation should abort on timeout
	if decision.DefaultAction != CommandAbort {
		t.Errorf("DefaultAction = %q, want %q (error escalation aborts)",
			decision.DefaultAction, CommandAbort)
	}
}

// TestRuleBasedPolicy_DefaultAction_Summary verifies all checkpoint types have correct defaults
func TestRuleBasedPolicy_DefaultAction_Summary(t *testing.T) {
	testCases := []struct {
		name           string
		setupPolicy    func() *RuleBasedPolicy
		testFunc       func(*RuleBasedPolicy) (*InterruptDecision, error)
		wantInterrupt  bool
		wantAction     CommandType
		checkpointType string
	}{
		{
			name: "Plan checkpoint (sensitive ops)",
			setupPolicy: func() *RuleBasedPolicy {
				return NewRuleBasedPolicy(HITLConfig{SensitiveCapabilities: []string{"cap1"}})
			},
			testFunc: func(p *RuleBasedPolicy) (*InterruptDecision, error) {
				plan := &RoutingPlan{PlanID: "p1", Steps: []RoutingStep{{StepID: "s1", Metadata: map[string]interface{}{"capability": "cap1"}}}}
				return p.ShouldApprovePlan(context.Background(), plan)
			},
			wantInterrupt:  true,
			wantAction:     CommandReject, // HITL = require explicit approval, reject on timeout
			checkpointType: "plan_generated",
		},
		{
			name: "Plan checkpoint (RequirePlanApproval)",
			setupPolicy: func() *RuleBasedPolicy {
				return NewRuleBasedPolicy(HITLConfig{RequirePlanApproval: true})
			},
			testFunc: func(p *RuleBasedPolicy) (*InterruptDecision, error) {
				plan := &RoutingPlan{PlanID: "p2", Steps: []RoutingStep{{StepID: "s1"}}}
				return p.ShouldApprovePlan(context.Background(), plan)
			},
			wantInterrupt:  true,
			wantAction:     CommandReject, // HITL = require explicit approval, reject on timeout
			checkpointType: "plan_generated",
		},
		{
			name: "Step checkpoint (sensitive agent)",
			setupPolicy: func() *RuleBasedPolicy {
				return NewRuleBasedPolicy(HITLConfig{SensitiveAgents: []string{"agent1"}})
			},
			testFunc: func(p *RuleBasedPolicy) (*InterruptDecision, error) {
				step := RoutingStep{StepID: "s1", AgentName: "agent1"}
				return p.ShouldApproveBeforeStep(context.Background(), step, &RoutingPlan{})
			},
			wantInterrupt:  true,
			wantAction:     CommandReject,
			checkpointType: "before_step",
		},
		{
			name: "Step checkpoint (sensitive capability)",
			setupPolicy: func() *RuleBasedPolicy {
				return NewRuleBasedPolicy(HITLConfig{SensitiveCapabilities: []string{"cap1"}})
			},
			testFunc: func(p *RuleBasedPolicy) (*InterruptDecision, error) {
				step := RoutingStep{StepID: "s1", AgentName: "agent", Metadata: map[string]interface{}{"capability": "cap1"}}
				return p.ShouldApproveBeforeStep(context.Background(), step, &RoutingPlan{})
			},
			wantInterrupt:  true,
			wantAction:     CommandReject,
			checkpointType: "before_step",
		},
		{
			name: "Error escalation",
			setupPolicy: func() *RuleBasedPolicy {
				return NewRuleBasedPolicy(HITLConfig{EscalateAfterRetries: 1})
			},
			testFunc: func(p *RuleBasedPolicy) (*InterruptDecision, error) {
				step := RoutingStep{StepID: "s1", AgentName: "agent"}
				return p.ShouldEscalateError(context.Background(), step, errors.New("err"), 1)
			},
			wantInterrupt:  true,
			wantAction:     CommandAbort,
			checkpointType: "on_error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			policy := tc.setupPolicy()
			decision, err := tc.testFunc(policy)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if decision.ShouldInterrupt != tc.wantInterrupt {
				t.Errorf("ShouldInterrupt = %v, want %v", decision.ShouldInterrupt, tc.wantInterrupt)
			}

			if decision.DefaultAction != tc.wantAction {
				t.Errorf("[%s] DefaultAction = %q, want %q",
					tc.checkpointType, decision.DefaultAction, tc.wantAction)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Test Helper
// -----------------------------------------------------------------------------

type testPolicyLogger struct{}

func (l *testPolicyLogger) Debug(msg string, fields map[string]interface{}) {}
func (l *testPolicyLogger) Info(msg string, fields map[string]interface{})  {}
func (l *testPolicyLogger) Warn(msg string, fields map[string]interface{})  {}
func (l *testPolicyLogger) Error(msg string, fields map[string]interface{}) {}
func (l *testPolicyLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (l *testPolicyLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (l *testPolicyLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (l *testPolicyLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
