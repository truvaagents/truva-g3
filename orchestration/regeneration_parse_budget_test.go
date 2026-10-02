package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

const invalidContinuationDependency = `{"plan_id":"invalid-dependency","terminal":true,"steps":[{"step_id":"step-2","agent_name":"test-agent","instruction":"do","implicit_deps":["step-1"],"metadata":{"capability":"test_capability","parameters":{}}}]}`

type cancelOnRegenerationClient struct {
	*promptCapturingAIClient
	cancel context.CancelFunc
}

func (c *cancelOnRegenerationClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	response, err := c.promptCapturingAIClient.GenerateResponse(ctx, prompt, opts)
	if c.callCount == 3 {
		c.cancel()
	}
	return response, err
}

func TestValidationRegenerationParseBudget(t *testing.T) {
	malformed := `{"steps":[}`
	providerErr := &json.SyntaxError{Offset: 7} // Provider errors must not be classified by JSON error type or text.
	for _, tc := range []struct {
		name          string
		responses     []string
		maxRounds     int
		providerError bool
		cancel        bool
		wantError     string
		wantCalls     int
		wantHooks     int
	}{
		{"valid regeneration unchanged", []string{foundationInitialNonTerminalPlan, invalidContinuationDependency, foundationTerminalEmptyPlan, "synthesized response"}, 3, false, false, "", 4, 2},
		{"malformed then valid", []string{foundationInitialNonTerminalPlan, invalidContinuationDependency, malformed, foundationTerminalEmptyPlan, "synthesized response"}, 3, false, false, "", 5, 2},
		{"revalidate recovered plan", []string{foundationInitialNonTerminalPlan, invalidContinuationDependency, malformed, invalidContinuationDependency, foundationTerminalEmptyPlan, "synthesized response"}, 3, false, false, "", 6, 2},
		{"malformed exhausts same budget", []string{foundationInitialNonTerminalPlan, invalidContinuationDependency, malformed}, 3, false, false, "failed validation after 3 regeneration rounds", 5, 1},
		{"single regeneration limit", []string{foundationInitialNonTerminalPlan, invalidContinuationDependency, malformed}, 1, false, false, "failed validation after 1 regeneration rounds", 3, 1},
		{"provider failure stops", []string{foundationInitialNonTerminalPlan, invalidContinuationDependency, malformed}, 3, true, false, "continuation plan retry LLM call failed", 3, 1},
		{"cancellation stops", []string{foundationInitialNonTerminalPlan, invalidContinuationDependency, malformed}, 3, false, true, "failed to generate valid plan", 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &promptCapturingAIClient{responses: tc.responses}
			if tc.providerError {
				client.errors = []error{nil, nil, providerErr}
			}
			var aiClient core.AIClient = client
			if tc.cancel {
				aiClient = &cancelOnRegenerationClient{promptCapturingAIClient: client, cancel: cancel}
			}
			o := setupTestOrchestrator(t, aiClient)
			o.config.IterativePlanning.MaxValidationRounds = tc.maxRounds
			registration := &core.ServiceRegistration{ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080, Type: core.ComponentTypeTool}
			if err := o.discovery.Register(ctx, registration); err != nil {
				t.Fatal(err)
			}
			o.catalog.agents = map[string]*AgentInfo{"test-agent": {
				Registration: registration, Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process"}},
			}}
			transport := NewMockRoundTripper()
			transport.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result":"prior-phase-evidence"}`)
			o.executor.httpClient = &http.Client{Transport: transport}
			hook := &allStagesHook{name: "regeneration-budget"}
			o.pipelineHooks = []core.PipelineHook{hook}
			response, err := o.ProcessRequest(ctx, "request", nil)
			if tc.wantError == "" {
				if err != nil || response == nil || response.Response != "synthesized response" {
					t.Fatalf("response=%v, error=%v", response, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error=%v, want %q", err, tc.wantError)
			}
			if tc.providerError && !errors.Is(err, providerErr) {
				t.Fatalf("provider error identity lost: %v", err)
			}
			if client.callCount != tc.wantCalls || hook.afterPlanCalls != tc.wantHooks {
				t.Fatalf("AI calls=%d want=%d; accepted-plan hooks=%d want=%d", client.callCount, tc.wantCalls, hook.afterPlanCalls, tc.wantHooks)
			}
			if calls := transport.GetCallCount(); calls != 1 {
				t.Fatalf("tool dispatches=%d, want only the accepted prior-phase step", calls)
			}
			if tc.name == "malformed then valid" || tc.name == "revalidate recovered plan" {
				prompt := client.capturedPrompts[3]
				for _, want := range []string{"prior-phase-evidence", `"terminal": true`, "failed to parse plan JSON"} {
					if !strings.Contains(prompt, want) {
						t.Errorf("recovery prompt lost %q", want)
					}
				}
			}
		})
	}
}

func TestFirstPhaseValidationRegenerationParseBudget(t *testing.T) {
	invalid := strings.Replace(foundationInitialNonTerminalPlan, `"parameters":{}`, `"parameters":{"value":"{{step-1}}"}`, 1)
	for _, recoverPlan := range []bool{true, false} {
		name := "exhausted"
		responses := []string{invalid, `{"steps":[}`}
		wantCalls := 3 // initial generation plus two regeneration attempts
		if recoverPlan {
			name = "recovered"
			responses = append(responses, foundationTerminalEmptyPlan, "synthesized response")
			wantCalls++
		}
		t.Run(name, func(t *testing.T) {
			client := &promptCapturingAIClient{responses: responses}
			o := setupTestOrchestrator(t, client)
			o.config.IterativePlanning.MaxValidationRounds = 2
			if err := o.discovery.Register(context.Background(), &core.ServiceRegistration{
				ID: "test-agent", Name: "test-agent", Type: core.ComponentTypeTool,
			}); err != nil {
				t.Fatal(err)
			}
			transport := NewMockRoundTripper()
			o.executor.httpClient = &http.Client{Transport: transport}
			hook := &allStagesHook{name: "first-phase-regeneration-budget"}
			o.pipelineHooks = []core.PipelineHook{hook}
			response, err := o.ProcessRequest(context.Background(), "request", nil)
			if recoverPlan {
				if err != nil || response == nil || response.Response != "synthesized response" || hook.afterPlanCalls != 1 {
					t.Fatalf("response=%v, error=%v, accepted-plan hooks=%d", response, err, hook.afterPlanCalls)
				}
			} else if err == nil || !strings.Contains(err.Error(), "failed validation after 2 regeneration rounds") || hook.afterPlanCalls != 0 {
				t.Fatalf("error=%v, accepted-plan hooks=%d", err, hook.afterPlanCalls)
			}
			if client.callCount != wantCalls || transport.GetCallCount() != 0 {
				t.Fatalf("AI calls=%d want=%d; tool dispatches=%d want=0", client.callCount, wantCalls, transport.GetCallCount())
			}
		})
	}
}
