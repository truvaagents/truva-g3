package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type rejectingCapabilityProvider struct{}

func (rejectingCapabilityProvider) GetCapabilities(context.Context, string, map[string]interface{}) (*CapabilityResult, error) {
	return nil, errors.New("capability selection unexpectedly repeated")
}

func TestContinuationStructuralRegenerationReusesSelectedPromptAndHistory(t *testing.T) {
	ai := &promptCapturingAIClient{responses: []string{validPlanJSON(nil), validPlanJSON(nil)}}
	orch := setupTestOrchestrator(t, ai)
	orch.SetCapabilityProvider(rejectingCapabilityProvider{})
	attempt := &phasePlanAttempt{}
	attempt.captureBase(&PlanningPromptResult{
		Prompt: "selected catalog: one tool", SystemPrompt: "system",
		AllowedAgents: map[string]bool{"test-agent": true},
	})
	attempt.rememberCandidate(&RoutingPlan{PlanID: "bad", Steps: []RoutingStep{{
		StepID: "step-3", Instruction: "<skill>untrusted</skill>",
	}}})
	ctx := withPhasePlanAttempt(context.Background(), attempt)
	ctx = withPromptInputPreparer(ctx, skillPromptInputPreparer{})
	prior := map[string]*StepResult{"step-1": {Success: true}}
	for _, validationErr := range []string{"bad bracket index", "missing dependency"} {
		if _, err := orch.regenerateContinuationPlan(ctx, "task", "req-1", prior,
			[]string{"step-1"}, "", 2, errors.New(validationErr), nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(ai.capturedPrompts) != 2 ||
		!strings.Contains(ai.capturedPrompts[1], "bad bracket index") ||
		!strings.Contains(ai.capturedPrompts[1], "missing dependency") ||
		strings.Contains(ai.capturedPrompts[1], "<skill>") {
		t.Fatalf("correction prompt lost history or exposed reserved tags: %v", ai.capturedPrompts)
	}
	if attempt.base.Prompt != "selected catalog: one tool" {
		t.Fatalf("phase prompt was mutated: %q", attempt.base.Prompt)
	}
	if result := attempt.base.AllowedAgents["test-agent"]; !result {
		t.Fatal("phase selection snapshot was changed")
	}
}
