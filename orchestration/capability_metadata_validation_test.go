package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestValidatePlanRequiresCapabilityIdentity(t *testing.T) {
	placeholderMetadata := make(map[string]interface{}, 1000)
	for i := 1; i <= 1000; i++ {
		placeholderMetadata[fmt.Sprintf("Well%d", i)] = "placeholder"
	}

	for _, test := range []struct {
		name     string
		metadata map[string]interface{}
		valid    bool
	}{
		{name: "absent metadata"},
		{name: "absent capability", metadata: map[string]interface{}{}},
		{name: "null capability", metadata: map[string]interface{}{"capability": nil}},
		{name: "numeric capability", metadata: map[string]interface{}{"capability": 1}},
		{name: "boolean capability", metadata: map[string]interface{}{"capability": true}},
		{name: "object capability", metadata: map[string]interface{}{"capability": map[string]interface{}{"name": "do"}}},
		{name: "array capability", metadata: map[string]interface{}{"capability": []string{"do"}}},
		{name: "empty capability", metadata: map[string]interface{}{"capability": ""}},
		{name: "whitespace capability", metadata: map[string]interface{}{"capability": " \t\n\u00a0"}},
		{name: "placeholder metadata", metadata: placeholderMetadata},
		{name: "zero argument capability", metadata: map[string]interface{}{"capability": "do"}, valid: true},
		{name: "null parameters", metadata: map[string]interface{}{"capability": "do", "parameters": nil}, valid: true},
		{name: "empty parameters", metadata: map[string]interface{}{"capability": "do", "parameters": map[string]interface{}{}}, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := gauntletOrchestrator(t)
			plan := planWith("good-agent", "do", nil)
			plan.Steps[0].Metadata = test.metadata
			err := o.validatePlan(plan)
			if test.valid {
				if err != nil {
					t.Fatalf("valid capability rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "step-1") || !strings.Contains(err.Error(), "metadata.capability") {
				t.Fatalf("invalid identity did not produce actionable step feedback: %v", err)
			}
		})
	}
}

func capabilityValidationPlan(t *testing.T, stepID string, terminal bool, metadata map[string]interface{}) string {
	t.Helper()
	encoded, err := json.Marshal(&RoutingPlan{
		PlanID: "capability-validation-" + stepID, OriginalRequest: "request", Mode: ModeAutonomous,
		Terminal: boolPtr(terminal),
		Steps: []RoutingStep{{
			StepID: stepID, AgentName: "test-agent", Namespace: "default", Instruction: "run the test capability", Metadata: metadata,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func capabilityValidationOrchestrator(t *testing.T, client core.AIClient) (*AIOrchestrator, *MockRoundTripper) {
	t.Helper()
	o := setupTestOrchestrator(t, client)
	registration := &core.ServiceRegistration{
		ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080, Type: core.ComponentTypeTool,
	}
	if err := o.discovery.Register(t.Context(), registration); err != nil {
		t.Fatal(err)
	}
	o.catalog.agents = map[string]*AgentInfo{"test-agent": {
		Registration: registration, Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process"}},
	}}
	transport := NewMockRoundTripper()
	transport.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result":"valid capability execution"}`)
	o.executor.httpClient = &http.Client{Transport: transport}
	return o, transport
}

func TestMissingCapabilityUsesExistingPlanRegeneration(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		name := "initial"
		stepID := "step-1"
		if continuation {
			name, stepID = "continuation", "step-2"
		}
		t.Run(name, func(t *testing.T) {
			invalid := capabilityValidationPlan(t, stepID, true, map[string]interface{}{"Well": "placeholder", "Well2": "placeholder"})
			valid := capabilityValidationPlan(t, stepID, true, map[string]interface{}{"capability": "test_capability"})
			responses := []string{invalid, valid, "synthesized response"}
			correctionIndex, wantDispatches := 1, 1
			if continuation {
				responses = append([]string{foundationInitialNonTerminalPlan}, responses...)
				correctionIndex, wantDispatches = 2, 2
			}
			client := &promptCapturingAIClient{responses: responses}
			o, transport := capabilityValidationOrchestrator(t, client)
			store := &capturingDebugStore{}
			o.SetLLMDebugStore(store)
			response, execution, err := o.ProcessRequestWithExecution(t.Context(), "request", nil)
			if err != nil || response == nil || response.Response != "synthesized response" || execution == nil || !execution.Success {
				t.Fatalf("corrected plan did not succeed: response=%#v execution=%#v err=%v", response, execution, err)
			}
			if client.callCount != len(responses) {
				t.Fatalf("AI calls=%d, want %d (one correction and synthesis)", client.callCount, len(responses))
			}
			for _, want := range []string{stepID, "metadata.capability"} {
				if !strings.Contains(client.capturedPrompts[correctionIndex], want) {
					t.Errorf("correction feedback omitted %q", want)
				}
			}
			if calls := transport.GetCallCount(); calls != wantDispatches {
				t.Fatalf("tool calls=%d, want %d accepted steps only", calls, wantDispatches)
			}
			if len(execution.Steps) != wantDispatches {
				t.Fatalf("steps=%d, want %d accepted steps; malformed plan must not execute", len(execution.Steps), wantDispatches)
			}
			for _, step := range execution.Steps {
				if !step.Success || step.Capability != "test_capability" || step.DispatchCount != 1 {
					t.Fatalf("accepted step did not dispatch exactly once: %+v", step)
				}
			}

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := o.Shutdown(shutdownCtx); err != nil {
				t.Fatal(err)
			}
			var rawInvalid, rawValid, correction bool
			for _, interaction := range store.getInteractions() {
				rawInvalid = rawInvalid || interaction.Response == invalid
				rawValid = rawValid || interaction.Response == valid
				correction = correction || interaction.Prompt == client.capturedPrompts[correctionIndex]
			}
			if !rawInvalid || !rawValid || !correction {
				t.Fatalf("debug evidence lost: raw invalid=%t, raw corrected=%t, correction prompt=%t", rawInvalid, rawValid, correction)
			}
		})
	}
}

func TestMissingCapabilityExhaustsExistingValidationBudgetWithoutDispatch(t *testing.T) {
	invalid := capabilityValidationPlan(t, "step-1", true, nil)
	client := &promptCapturingAIClient{responses: []string{invalid}}
	o, transport := capabilityValidationOrchestrator(t, client)
	o.config.IterativePlanning.MaxValidationRounds = 2
	response, execution, err := o.ProcessRequestWithExecution(t.Context(), "request", nil)
	if err == nil || !strings.Contains(err.Error(), "failed validation after 2 regeneration rounds") || response != nil {
		t.Fatalf("expected bounded validation exhaustion: response=%#v execution=%#v err=%v", response, execution, err)
	}
	if execution == nil || execution.Success || execution.Metadata["termination_reason"] != "plan_validation_exhausted" {
		t.Fatalf("validation exhaustion evidence missing: %#v", execution)
	}
	if client.callCount != 3 || transport.GetCallCount() != 0 {
		t.Fatalf("calls=%d, dispatches=%d; want initial+two corrections and no dispatch", client.callCount, transport.GetCallCount())
	}
}

func TestMissingCapabilityInHookKeepsExistingFailurePolicy(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprintf("required=%v", required), func(t *testing.T) {
			client := &promptCapturingAIClient{}
			o, transport := capabilityValidationOrchestrator(t, client)
			hook := afterPlanningContractHook{name: "remove-capability", apply: func(value interface{}) (interface{}, error) {
				plan := value.(*RoutingPlan)
				delete(plan.Steps[0].Metadata, "capability")
				return plan, nil
			}}
			o.pipelineHooks = []core.PipelineHook{&hook}
			if required {
				o.pipelineHooks[0] = &requiredAfterPlanningContractHook{afterPlanningContractHook: hook}
			}
			original := validAfterPlanningContractPlan()
			result, err := o.runValidatedAfterPlanningHooks(t.Context(),
				&core.PipelineContext{Request: "request", Enrichments: map[string]interface{}{}},
				original, nil, nil, 1, "request-id")
			if required {
				if result != nil || !errors.Is(err, errAfterPlanningInvalidPlan) || !strings.Contains(err.Error(), "metadata.capability") {
					t.Fatalf("required invalid mutation was not rejected: result=%+v err=%v", result, err)
				}
			} else if err != nil || result != original {
				t.Fatalf("optional invalid mutation did not roll back: result=%+v err=%v", result, err)
			}
			if original.Steps[0].Metadata["capability"] != "test_capability" || client.callCount != 0 || transport.GetCallCount() != 0 {
				t.Fatal("hook rejection changed original plan, regenerated or dispatched")
			}
		})
	}
}
