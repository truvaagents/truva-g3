package orchestration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestTransientFailuresCannotInvokeParameterCorrection(t *testing.T) {
	for _, status := range []int{0, 408, 429, 500, 501, 502, 503, 504, 505, 599} {
		for _, mode := range []string{"analyzer", "legacy", "disabled", "exhausted", "hitl", "orchestrator"} {
			t.Run(fmt.Sprintf("%d/%s", status, mode), func(t *testing.T) {
				kind := core.CapabilityTool
				if mode == "orchestrator" {
					kind = core.CapabilityOrchestrator
				}
				catalog := &AgentCatalog{agents: map[string]*AgentInfo{"target": {
					Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080},
					Capabilities: []EnhancedCapability{{Name: "call", Endpoint: "/call", Type: kind}},
				}}}
				e := NewSmartExecutor(catalog)
				e.SetStepRetryBackoff(core.BackoffConfig{InitialDelay: time.Millisecond, MaxDelay: time.Millisecond, BackoffFactor: 1})
				rt := NewMockRoundTripper()
				if status == 0 {
					rt.SetError("http://localhost:8080/call", errors.New("connection reset"))
				} else {
					rt.SetResponse("http://localhost:8080/call", status, `{"success":false,"error":{"code":"TEMPORARY","retryable":true}}`)
				}
				e.httpClient = &http.Client{Transport: rt}
				model := newMockAIClientWithResponse(map[string]interface{}{"should_retry": true, "suggested_changes": map[string]interface{}{"id": "replacement"}})
				if mode != "legacy" {
					e.SetErrorAnalyzer(NewErrorAnalyzer(model, nil))
				}
				if mode == "disabled" {
					e.errorAnalyzer.enabled = false
				}
				if mode == "exhausted" {
					e.maxValidationRetries = 0
				}
				corrections := 0
				e.SetCorrectionCallback(func(context.Context, RoutingStep, map[string]interface{}, string, *EnhancedCapability) (map[string]interface{}, error) {
					corrections++
					return map[string]interface{}{"id": "replacement"}, nil
				})
				step := RoutingStep{StepID: "step-1", AgentName: "target", Metadata: map[string]interface{}{"capability": "call", "parameters": map[string]interface{}{"id": "original"}}}
				ctx := context.Background()
				if mode == "hitl" {
					ctx = context.WithValue(ctx, preResolvedParamsKey, map[string]interface{}{"id": "original"})
					ctx = context.WithValue(ctx, preResolvedStepIDKey, step.StepID)
				}
				result := e.executeStep(ctx, step)
				want := 3
				if mode == "orchestrator" {
					want = 1
				}
				if result.Success || result.DispatchCount != want || len(model.calls) != 0 || corrections != 0 {
					t.Fatalf("unexpected retry policy: result=%+v AI=%d corrections=%d", result, len(model.calls), corrections)
				}
				for _, dispatch := range result.Dispatches {
					if dispatch.Parameters["id"] != "original" {
						t.Fatalf("transient retry rewrote input: %+v", dispatch)
					}
				}
			})
		}
	}
}
