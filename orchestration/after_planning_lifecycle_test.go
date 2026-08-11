package orchestration

import (
	"context"
	"net/http"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

const foundationInitialNonTerminalPlan = `{
  "plan_id":"phase-1",
  "original_request":"request",
  "mode":"autonomous",
  "steps":[{
    "step_id":"step-1",
    "agent_name":"test-agent",
    "namespace":"default",
    "instruction":"run the test capability",
    "depends_on":[],
    "metadata":{"capability":"test_capability","parameters":{}}
  }],
  "terminal":false,
  "continuation_note":"continue"
}`

const foundationTerminalEmptyPlan = `{
  "plan_id":"phase-2",
  "original_request":"request",
  "mode":"autonomous",
  "steps":[],
  "terminal":true
}`

func TestAfterPlanningHookRunsOnceForContinuationAndRegenerationBoundary(t *testing.T) {
	tests := []struct {
		name      string
		responses []string
		wantCalls int
	}{
		{
			name: "accepted continuation",
			responses: []string{
				foundationInitialNonTerminalPlan,
				foundationTerminalEmptyPlan,
				"synthesized response",
			},
			wantCalls: 3,
		},
		{
			name: "continuation regenerated after step ID conflict",
			responses: []string{
				foundationInitialNonTerminalPlan,
				`{"plan_id":"conflict","original_request":"request","mode":"autonomous","steps":[{"step_id":"step-1","agent_name":"test-agent","instruction":"duplicate","depends_on":[],"metadata":{"capability":"test_capability","parameters":{}}}],"terminal":true}`,
				foundationTerminalEmptyPlan,
				"synthesized response",
			},
			wantCalls: 4,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &promptCapturingAIClient{responses: test.responses}
			orchestrator := setupTestOrchestrator(t, client)
			if err := orchestrator.discovery.Register(context.Background(), &core.ServiceRegistration{
				ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080,
				Type: core.ComponentTypeTool,
			}); err != nil {
				t.Fatalf("register test agent: %v", err)
			}
			orchestrator.catalog.agents = map[string]*AgentInfo{
				"test-agent": {
					Registration: &core.ServiceRegistration{
						ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080,
						Type: core.ComponentTypeTool,
					},
					Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process"}},
				},
			}
			transport := NewMockRoundTripper()
			transport.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result":"ok"}`)
			orchestrator.executor.httpClient = &http.Client{Transport: transport}
			hook := &allStagesHook{name: "continuation-after-planning"}
			orchestrator.pipelineHooks = []core.PipelineHook{hook}

			response, err := orchestrator.ProcessRequest(context.Background(), "request", nil)
			if err != nil {
				t.Fatalf("ProcessRequest() error = %v", err)
			}
			if response == nil || response.Response != "synthesized response" {
				t.Fatalf("response = %#v", response)
			}
			if hook.afterPlanCalls != 2 {
				t.Fatalf("AfterPlanning calls = %d, want one accepted plan for each of two phases", hook.afterPlanCalls)
			}
			if client.callCount != test.wantCalls {
				t.Fatalf("AI calls = %d, want %d", client.callCount, test.wantCalls)
			}
		})
	}
}
