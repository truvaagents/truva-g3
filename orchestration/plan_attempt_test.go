package orchestration

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type rejectingCapabilityProvider struct{}

func (rejectingCapabilityProvider) GetCapabilities(context.Context, string, map[string]interface{}) (*CapabilityResult, error) {
	return nil, errors.New("capability selection unexpectedly repeated")
}

func TestContinuationStructuralRegenerationReusesSelectedPromptAndHistory(t *testing.T) {
	ai := &promptCapturingAIClient{responses: []string{validPlanJSON(nil), validPlanJSON(nil), validPlanJSON(nil)}}
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
	for _, validationErr := range []string{"bad bracket index", "missing dependency", "bad bracket index"} {
		if _, err := orch.regenerateContinuationPlan(ctx, "task", "req-1", prior,
			[]string{"step-1"}, "", 2, errors.New(validationErr), nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(ai.capturedPrompts) != 3 ||
		!strings.Contains(ai.capturedPrompts[1], "bad bracket index") ||
		!strings.Contains(ai.capturedPrompts[1], "missing dependency") ||
		strings.Contains(ai.capturedPrompts[1], "<skill>") {
		t.Fatalf("correction prompt lost history or exposed reserved tags: %v", ai.capturedPrompts)
	}
	if !strings.Contains(ai.capturedPrompts[2], `Latest validation error: "\"bad bracket index\""`) ||
		!strings.Contains(ai.capturedPrompts[2], `Earlier validation errors: "\"missing dependency\""`) {
		t.Fatalf("repeated error displaced distinct history: %s", ai.capturedPrompts[2])
	}
	if attempt.base.Prompt != "selected catalog: one tool" {
		t.Fatalf("phase prompt was mutated: %q", attempt.base.Prompt)
	}
	if result := attempt.base.AllowedAgents["test-agent"]; !result {
		t.Fatal("phase selection snapshot was changed")
	}
}

func TestPlanAttemptErrorHistoryRecencyAndBound(t *testing.T) {
	var absent *phasePlanAttempt
	absent.rememberError(errors.New("ignored"))
	attempt := &phasePlanAttempt{}
	attempt.rememberError(nil)
	for i := 0; i < 8; i++ {
		attempt.rememberError(fmt.Errorf("error-%d", i))
	}
	attempt.rememberError(errors.New("error-0"))
	attempt.rememberError(errors.New("error-0"))
	attempt.rememberError(errors.New("error-8"))
	want := []string{"error-2", "error-3", "error-4", "error-5", "error-6", "error-7", "error-0", "error-8"}
	if !reflect.DeepEqual(attempt.errors, want) {
		t.Fatalf("history = %v; want %v", attempt.errors, want)
	}
}
