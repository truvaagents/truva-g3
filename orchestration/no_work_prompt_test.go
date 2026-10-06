package orchestration

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNoWorkPlanningGuidanceMatchesModelIntent(t *testing.T) {
	const noWork = `{"terminal": true, "steps": []}`
	for _, tc := range []struct {
		name   string
		config *IterativePlanConfig
	}{
		{name: "nil iterative config"},
		{name: "iterative disabled", config: &IterativePlanConfig{Enabled: false}},
		{name: "iterative enabled", config: &IterativePlanConfig{Enabled: true, MaxPhases: 3, MaxTotalSteps: 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The default initial builder and the fallback/continuation rules
			// must agree even when no iterative instructions are appended.
			if !strings.Contains(buildDefaultFormatRules(tc.config), noWork) {
				t.Fatal("no-work rule does not specify the recognized model envelope")
			}
			builder, err := NewDefaultPromptBuilder(nil)
			if err != nil {
				t.Fatal(err)
			}
			builder.config.IterativePlanConfig = tc.config
			prompt, err := builder.BuildPlanningPrompt(context.Background(), PromptInput{Request: "Perform the unsupported action"})
			if err != nil || !strings.Contains(prompt, "If available agents cannot fulfill the request, return "+noWork) {
				t.Fatalf("default initial builder lacks explicit no-work guidance: %v", err)
			}
			plan, err := (&AIOrchestrator{}).parsePlan(noWork)
			if err != nil || plan == nil || !plan.IsTerminal() || len(plan.Steps) != 0 {
				t.Fatalf("documented no-work envelope was rejected: plan=%v err=%v", plan, err)
			}
		})
	}
	if !strings.Contains(IterativePlanningInstructions, "For nonempty plans, omitting terminal") ||
		!strings.Contains(IterativePlanningInstructions, `For zero-work plans, explicitly set "terminal": true and "steps": []`) {
		t.Fatal("legacy terminal omission guidance conflicts with model-wire intent")
	}
	active := BuildIterativePlanningInstructions(&IterativePlanConfig{Enabled: true})
	if !strings.Contains(active, "For nonempty plans, omitting terminal") ||
		!strings.Contains(active, `{ "plan_id": "...", "terminal": true, "steps": [] }`) {
		t.Fatal("active instructions lack the compatible terminal example")
	}
	if strings.Contains(active, "For zero-work plans, explicitly set") {
		t.Fatal("active instructions repeat the terminal example as an extra rule")
	}
}

func TestHallucinatedAgentCanRecoverToExplicitNoWork(t *testing.T) {
	const noWork = `{"terminal": true, "steps": []}`
	for _, iterative := range []bool{false, true} {
		name := "iterative disabled"
		if iterative {
			name = "iterative enabled"
		}
		t.Run(name, func(t *testing.T) {
			client := &promptCapturingAIClient{responses: []string{
				`{"steps":[{"step_id":"step-1","agent_name":"unregistered-agent","instruction":"Unsupported action","metadata":{"capability":"unavailable","parameters":{}}}],"terminal":false}`,
				noWork,
				"No available capability can perform the requested action.",
			}}
			o := setupTestOrchestrator(t, client)
			o.config.CacheEnabled = false
			o.config.PlanParseRetryEnabled = false
			o.config.HallucinationRetryEnabled = true
			o.config.HallucinationMaxRetries = 1
			o.config.PlanRefinementEnabled = false
			o.config.IterativePlanning.Enabled = iterative
			transport := NewMockRoundTripper()
			o.executor.httpClient = &http.Client{Transport: transport}

			response, execution, err := o.ProcessRequestWithExecution(context.Background(), "Perform the unsupported action", nil)
			if err != nil || response == nil || execution == nil {
				t.Fatalf("explicit no-work recovery failed: response=%v execution=%v err=%v", response, execution, err)
			}
			if client.callCount != 3 || transport.GetCallCount() != 0 || len(execution.Steps) != 0 {
				t.Fatalf("unexpected correction or dispatch: calls=%d dispatches=%d steps=%d", client.callCount, transport.GetCallCount(), len(execution.Steps))
			}
			if !strings.Contains(client.capturedPrompts[1], "unregistered-agent") || !strings.Contains(client.capturedPrompts[1], noWork) {
				t.Fatal("agent-name retry did not give an explicit supported no-work shape")
			}
			if !strings.Contains(client.capturedPrompts[0], "If available agents cannot fulfill the request, return "+noWork) {
				t.Fatal("initial planning did not receive explicit no-work guidance")
			}
			if response.Response != "No available capability can perform the requested action." {
				t.Fatal("no-work limitation did not reach the final response")
			}
		})
	}
}
