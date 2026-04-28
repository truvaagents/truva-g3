package orchestration

import (
	"context"
	"fmt"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// =============================================================================
// RuleBasedPolicy - Reference Implementation
// =============================================================================
//
// RuleBasedPolicy implements InterruptPolicy with declarative rules.
// This is the framework's reference implementation - applications can use it
// directly or implement their own policy (ML-based, external service, etc.).
//
// Usage:
//
//	policy := NewRuleBasedPolicy(HITLConfig{
//	    RequirePlanApproval:   true,
//	    SensitiveCapabilities: []string{"transfer_funds", "delete_account"},
//	    SensitiveAgents:       []string{"payment-service"},
//	    EscalateAfterRetries:  3,
//	})
//
// =============================================================================

// RuleBasedPolicy implements InterruptPolicy with declarative rules.
type RuleBasedPolicy struct {
	config HITLConfig
	logger core.Logger
}

// NewRuleBasedPolicy creates a new rule-based policy with the given configuration.
// Returns concrete type per Go idiom "return structs, accept interfaces".
func NewRuleBasedPolicy(config HITLConfig, opts ...PolicyOption) *RuleBasedPolicy {
	p := &RuleBasedPolicy{
		config: config,
		logger: &core.NoOpLogger{}, // Safe default per framework
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// -----------------------------------------------------------------------------
// PlanApprover Implementation
// -----------------------------------------------------------------------------

// ShouldApprovePlan checks if the generated plan requires human approval.
// Logic:
//  1. Check for sensitive capabilities/agents FIRST
//  2. If found → require approval (regardless of RequirePlanApproval setting)
//  3. If NOT found AND RequirePlanApproval=true → require approval for all plans
//  4. If NOT found AND RequirePlanApproval=false → no approval needed
func (p *RuleBasedPolicy) ShouldApprovePlan(ctx context.Context, plan *RoutingPlan) (*InterruptDecision, error) {
	// Step 1: Check if any step involves sensitive capabilities or agents
	hasSensitiveOps := false
	sensitiveDetails := []string{}

	for _, step := range plan.Steps {
		// Check for sensitive agent
		if p.isSensitiveAgent(step.AgentName) {
			hasSensitiveOps = true
			sensitiveDetails = append(sensitiveDetails, fmt.Sprintf("agent:%s", step.AgentName))
		}

		// Check for sensitive capability in metadata
		if capability, ok := step.Metadata["capability"].(string); ok {
			if p.isSensitiveCapability(capability) {
				hasSensitiveOps = true
				sensitiveDetails = append(sensitiveDetails, fmt.Sprintf("capability:%s", capability))
			}
		}
	}

	// Step 2: If sensitive operations found, require approval (regardless of RequirePlanApproval)
	if hasSensitiveOps {
		// Record policy decision in trace
		telemetry.AddSpanEvent(ctx, "hitl.policy.plan_approval_required",
			attribute.String("plan_id", plan.PlanID),
			attribute.Int("step_count", len(plan.Steps)),
			attribute.String("trigger", "sensitive_operation"),
			attribute.Int("sensitive_count", len(sensitiveDetails)),
		)

		return &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonSensitiveOperation,
			Message:         fmt.Sprintf("Plan contains sensitive operations requiring approval: %v", sensitiveDetails),
			Priority:        PriorityHigh,
			Timeout:         p.config.DefaultTimeout,
			DefaultAction:   p.getDefaultAction(), // Uses config (default: reject, override via TRUVAG3_HITL_DEFAULT_ACTION)
			Metadata: map[string]interface{}{
				"plan_id":           plan.PlanID,
				"step_count":        len(plan.Steps),
				"has_sensitive_ops": true,
				"sensitive_details": sensitiveDetails,
			},
		}, nil
	}

	// Step 3: If no sensitive ops but RequirePlanApproval is enabled, require approval for all plans
	if p.config.RequirePlanApproval {
		// Record policy decision in trace
		telemetry.AddSpanEvent(ctx, "hitl.policy.plan_approval_required",
			attribute.String("plan_id", plan.PlanID),
			attribute.Int("step_count", len(plan.Steps)),
			attribute.String("trigger", "require_plan_approval_config"),
		)

		return &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonPlanApproval,
			Message:         fmt.Sprintf("Plan approval required for request: %s", truncateString(plan.OriginalRequest, 100)),
			Priority:        PriorityNormal,
			Timeout:         p.config.DefaultTimeout,
			DefaultAction:   p.getDefaultAction(), // Uses config (default: reject, override via TRUVAG3_HITL_DEFAULT_ACTION)
			Metadata: map[string]interface{}{
				"plan_id":           plan.PlanID,
				"step_count":        len(plan.Steps),
				"has_sensitive_ops": false,
				"sensitive_details": sensitiveDetails,
			},
		}, nil
	}

	// Step 4: No sensitive ops and RequirePlanApproval=false → no approval needed
	return &InterruptDecision{ShouldInterrupt: false}, nil
}

// -----------------------------------------------------------------------------
// StepApprover Implementation
// -----------------------------------------------------------------------------

// ShouldApproveBeforeStep checks if a step requires approval before execution.
// This checks both regular sensitive lists (also checked at plan level) and
// step-sensitive lists (only checked here, for Scenario 2 step-level-only approval).
func (p *RuleBasedPolicy) ShouldApproveBeforeStep(ctx context.Context, step RoutingStep, plan *RoutingPlan) (*InterruptDecision, error) {
	// Check if agent requires approval (regular sensitive or step-sensitive)
	if p.isSensitiveAgent(step.AgentName) || p.isStepSensitiveAgent(step.AgentName) {
		trigger := "sensitive_agent"
		if p.isStepSensitiveAgent(step.AgentName) {
			trigger = "step_sensitive_agent"
		}

		// Record policy decision in trace
		telemetry.AddSpanEvent(ctx, "hitl.policy.step_approval_required",
			attribute.String("step_id", step.StepID),
			attribute.String("agent_name", step.AgentName),
			attribute.String("trigger", trigger),
			attribute.String("trigger_type", "agent"),
		)

		return &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonSensitiveOperation,
			Message:         fmt.Sprintf("Step approval required for agent: %s", step.AgentName),
			Priority:        PriorityHigh,
			Timeout:         p.config.DefaultTimeout,
			DefaultAction:   p.getDefaultAction(), // Uses config (default: reject, override via TRUVAG3_HITL_DEFAULT_ACTION)
			Metadata: map[string]interface{}{
				"step_id":    step.StepID,
				"agent_name": step.AgentName,
				"trigger":    trigger,
			},
		}, nil
	}

	// Check if capability requires approval (regular sensitive or step-sensitive)
	if capability, ok := step.Metadata["capability"].(string); ok {
		if p.isSensitiveCapability(capability) || p.isStepSensitiveCapability(capability) {
			trigger := "sensitive_capability"
			if p.isStepSensitiveCapability(capability) {
				trigger = "step_sensitive_capability"
			}

			// Record policy decision in trace
			telemetry.AddSpanEvent(ctx, "hitl.policy.step_approval_required",
				attribute.String("step_id", step.StepID),
				attribute.String("agent_name", step.AgentName),
				attribute.String("capability", capability),
				attribute.String("trigger", trigger),
				attribute.String("trigger_type", "capability"),
			)

			return &InterruptDecision{
				ShouldInterrupt: true,
				Reason:          ReasonSensitiveOperation,
				Message:         fmt.Sprintf("Step approval required for operation: %s.%s", step.AgentName, capability),
				Priority:        PriorityHigh,
				Timeout:         p.config.DefaultTimeout,
				DefaultAction:   p.getDefaultAction(), // Uses config (default: reject, override via TRUVAG3_HITL_DEFAULT_ACTION)
				Metadata: map[string]interface{}{
					"step_id":    step.StepID,
					"agent_name": step.AgentName,
					"capability": capability,
					"trigger":    trigger,
				},
			}, nil
		}
	}

	return &InterruptDecision{ShouldInterrupt: false}, nil
}

// ShouldApproveAfterStep checks if step output requires validation.
func (p *RuleBasedPolicy) ShouldApproveAfterStep(ctx context.Context, step RoutingStep, result *StepResult) (*InterruptDecision, error) {
	// Check if this capability requires output validation
	if capability, ok := step.Metadata["capability"].(string); ok {
		if p.requiresOutputValidation(capability) {
			return &InterruptDecision{
				ShouldInterrupt: true,
				Reason:          ReasonOutputValidation,
				Message:         fmt.Sprintf("Output validation required for: %s.%s", step.AgentName, capability),
				Priority:        PriorityNormal,
				Timeout:         p.config.DefaultTimeout,
				DefaultAction:   p.getDefaultAction(), // Uses config (default: reject, override via TRUVAG3_HITL_DEFAULT_ACTION)
				Metadata: map[string]interface{}{
					"step_id":         step.StepID,
					"agent_name":      step.AgentName,
					"capability":      capability,
					"response_length": len(result.Response),
				},
			}, nil
		}
	}

	return &InterruptDecision{ShouldInterrupt: false}, nil
}

// -----------------------------------------------------------------------------
// ErrorEscalator Implementation
// -----------------------------------------------------------------------------

// ShouldEscalateError checks if an error should be escalated to human.
func (p *RuleBasedPolicy) ShouldEscalateError(ctx context.Context, step RoutingStep, err error, attempts int) (*InterruptDecision, error) {
	// Escalate after configured number of retries
	if p.config.EscalateAfterRetries > 0 && attempts >= p.config.EscalateAfterRetries {
		return &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonEscalation,
			Message:         fmt.Sprintf("Escalation after %d failed attempts: %s", attempts, err.Error()),
			Priority:        PriorityHigh,
			Timeout:         p.config.DefaultTimeout,
			DefaultAction:   CommandAbort, // Default to abort on escalation
			Metadata: map[string]interface{}{
				"step_id":    step.StepID,
				"agent_name": step.AgentName,
				"attempts":   attempts,
				"error":      err.Error(),
			},
		}, nil
	}

	return &InterruptDecision{ShouldInterrupt: false}, nil
}

// -----------------------------------------------------------------------------
// Helper Methods
// -----------------------------------------------------------------------------

// isSensitiveAgent checks if an agent is in the sensitive agents list
func (p *RuleBasedPolicy) isSensitiveAgent(agentName string) bool {
	for _, sensitive := range p.config.SensitiveAgents {
		if sensitive == agentName {
			return true
		}
	}
	return false
}

// isSensitiveCapability checks if a capability is in the sensitive capabilities list
func (p *RuleBasedPolicy) isSensitiveCapability(capability string) bool {
	for _, sensitive := range p.config.SensitiveCapabilities {
		if sensitive == capability {
			return true
		}
	}
	return false
}

// isStepSensitiveAgent checks if an agent is in the step-sensitive agents list.
// Step-sensitive agents only trigger step-level HITL (Scenario 2), not plan-level.
func (p *RuleBasedPolicy) isStepSensitiveAgent(agentName string) bool {
	for _, sensitive := range p.config.StepSensitiveAgents {
		if sensitive == agentName {
			return true
		}
	}
	return false
}

// isStepSensitiveCapability checks if a capability is in the step-sensitive capabilities list.
// Step-sensitive capabilities only trigger step-level HITL (Scenario 2), not plan-level.
func (p *RuleBasedPolicy) isStepSensitiveCapability(capability string) bool {
	for _, sensitive := range p.config.StepSensitiveCapabilities {
		if sensitive == capability {
			return true
		}
	}
	return false
}

// requiresOutputValidation checks if a capability requires output validation.
// By default, no capabilities require output validation - this can be extended.
func (p *RuleBasedPolicy) requiresOutputValidation(capability string) bool {
	// This could be configured via HITLConfig.ValidateOutputCapabilities
	// For now, we return false (no automatic output validation)
	return false
}

// getDefaultAction returns the configured default action, falling back to CommandReject.
// This ensures fail-safe behavior even if HITLConfig.DefaultAction is not explicitly set.
func (p *RuleBasedPolicy) getDefaultAction() CommandType {
	if p.config.DefaultAction != "" {
		return p.config.DefaultAction
	}
	return CommandReject // Fail-safe: HITL enabled = require explicit approval
}

// Note: truncateString is already defined in orchestrator.go

// =============================================================================
// NoOpPolicy - For testing and disabled HITL
// =============================================================================

// NoOpPolicy is a policy that never triggers interrupts.
// Useful for testing or when HITL is disabled.
type NoOpPolicy struct{}

// NewNoOpPolicy creates a no-op policy
func NewNoOpPolicy() *NoOpPolicy {
	return &NoOpPolicy{}
}

// ShouldApprovePlan never requires plan approval
func (p *NoOpPolicy) ShouldApprovePlan(ctx context.Context, plan *RoutingPlan) (*InterruptDecision, error) {
	return &InterruptDecision{ShouldInterrupt: false}, nil
}

// ShouldApproveBeforeStep never requires pre-step approval
func (p *NoOpPolicy) ShouldApproveBeforeStep(ctx context.Context, step RoutingStep, plan *RoutingPlan) (*InterruptDecision, error) {
	return &InterruptDecision{ShouldInterrupt: false}, nil
}

// ShouldApproveAfterStep never requires post-step validation
func (p *NoOpPolicy) ShouldApproveAfterStep(ctx context.Context, step RoutingStep, result *StepResult) (*InterruptDecision, error) {
	return &InterruptDecision{ShouldInterrupt: false}, nil
}

// ShouldEscalateError never escalates errors
func (p *NoOpPolicy) ShouldEscalateError(ctx context.Context, step RoutingStep, err error, attempts int) (*InterruptDecision, error) {
	return &InterruptDecision{ShouldInterrupt: false}, nil
}

// Compile-time interface compliance checks
var (
	_ InterruptPolicy = (*RuleBasedPolicy)(nil)
	_ InterruptPolicy = (*NoOpPolicy)(nil)
)
