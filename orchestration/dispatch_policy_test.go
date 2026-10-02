package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestRetryDeclarationPresence(t *testing.T) {
	for _, tc := range []struct {
		body           string
		known, allowed bool
	}{
		{`{"success":false,"error":{"retryable":false}}`, true, false},
		{`{"success":false,"error":{"retryable":true}}`, true, true},
		{`{"success":false,"error":{}}`, false, false},
		{`{"success":false,"error":{"retryable":null}}`, false, false},
		{`{"success":false,"error":{"retryable":"false"}}`, false, false},
		{`invalid`, false, false},
	} {
		policy := componentRetryPolicy(tc.body)
		if (policy != nil) != tc.known || policy != nil && *policy != tc.allowed {
			t.Fatalf("wrong policy for %s: %v", tc.body, policy)
		}
	}
}

func TestExplicitRetryRefusalStopsEveryHTTPStatus(t *testing.T) {
	for _, status := range []int{400, 408, 429, 500} {
		for _, mode := range []string{"enabled", "disabled", "exhausted", "orchestrator"} {
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
				body := `{"success":false,"error":{"code":"UNCERTAIN_OUTCOME","retryable":false}}`
				rt.SetRetryResponses("http://localhost:8080/call", []struct {
					StatusCode int
					Body       string
				}{{status, body}, {200, `{"success":true}`}})
				e.httpClient = &http.Client{Transport: rt}
				ai := newMockAIClientWithResponse(map[string]interface{}{"should_retry": true, "suggested_changes": map[string]interface{}{"value": "changed"}})
				e.errorAnalyzer = NewErrorAnalyzer(ai, nil)
				if mode == "disabled" {
					e.errorAnalyzer.enabled = false
				}
				if mode == "exhausted" {
					e.maxValidationRetries = 0
				}
				result := e.executeStep(context.Background(), RoutingStep{StepID: "step-1", AgentName: "target", Metadata: map[string]interface{}{"capability": "call", "parameters": map[string]interface{}{"value": "original"}}})
				if result.Success || result.DispatchCount != 1 || rt.GetCallCount() != 1 || len(ai.calls) != 0 || !json.Valid(result.ToolError) {
					t.Fatalf("refusal lost: success=%v dispatches=%d HTTP=%d AI=%d error=%s", result.Success, result.DispatchCount, rt.GetCallCount(), len(ai.calls), result.ToolError)
				}
			})
		}
	}
}

func TestDispatchCorrectionPolicyAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body                              string
		orchestrator, exhausted, disabled, noOp bool
		want                                    int
	}{
		{"refusal", `{"success":false,"error":{"code":"INVALID","retryable":false}}`, false, false, false, false, 1},
		{"permitted", `{"success":false,"error":{"code":"INVALID","retryable":true}}`, false, false, false, false, 2},
		{"omitted-disabled", `{"success":false,"error":{"code":"INVALID"}}`, false, false, true, false, 1},
		{"omitted-exhausted", `{"success":false,"error":{"code":"INVALID"}}`, false, true, false, false, 1},
		{"orchestrator", `{"success":false,"error":{"code":"INVALID","retryable":true}}`, true, false, false, false, 1},
		{"no-op", `{"success":false,"error":{"code":"INVALID","retryable":true}}`, false, false, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := core.CapabilityTool
			if tc.orchestrator {
				kind = core.CapabilityOrchestrator
			}
			catalog := &AgentCatalog{agents: map[string]*AgentInfo{"target": {Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080}, Capabilities: []EnhancedCapability{{Name: "call", Endpoint: "/call", Type: kind}}}}}
			e := NewSmartExecutor(catalog)
			rt := NewMockRoundTripper()
			rt.SetRetryResponses("http://localhost:8080/call", []struct {
				StatusCode int
				Body       string
			}{{422, tc.body}, {200, `{"success":true}`}})
			e.httpClient = &http.Client{Transport: rt}
			changes := map[string]interface{}{"value": "corrected"}
			if tc.noOp {
				changes["value"] = "original"
			}
			ai := newMockAIClientWithResponse(map[string]interface{}{"should_retry": true, "suggested_changes": changes})
			e.errorAnalyzer = NewErrorAnalyzer(ai, nil)
			if tc.disabled {
				e.errorAnalyzer.enabled = false
			}
			if tc.exhausted {
				e.maxValidationRetries = 0
			}
			params := map[string]interface{}{"value": "original"}
			step := RoutingStep{StepID: "step-1", AgentName: "target", Metadata: map[string]interface{}{"capability": "call", "parameters": params}}
			result := e.executeStep(context.Background(), step)
			if result.DispatchCount != tc.want || len(result.Dispatches) != tc.want || rt.GetCallCount() != tc.want {
				t.Fatalf("dispatches=%d, records=%d, HTTP=%d; want %d", result.DispatchCount, len(result.Dispatches), rt.GetCallCount(), tc.want)
			}
			if params["value"] != "original" || result.Dispatches[0].Parameters["value"] != "original" {
				t.Fatal("retry mutated original evidence")
			}
			if tc.want == 2 && (!result.Success || result.Parameters["value"] != "corrected" || len(result.ToolError) > 0) {
				t.Fatalf("bad recovered result: %+v", result)
			}
			if tc.want == 1 && (result.Success || !json.Valid(result.ToolError)) {
				t.Fatalf("lost failure evidence: %+v", result)
			}
		})
	}
}

func TestStructuredFailureProjectionRetainsReferences(t *testing.T) {
	raw := json.RawMessage(`{"code":"REPAIR_EVIDENCE_INVALID","retryable":false,"details":{"candidate_id":"candidate-reference","verification_id":"verification-reference"}}`)
	projection := boundedFailureProjection(raw, 10000, continuationDigestOpts{})
	rendered := renderContinuationStepResult(&StepResult{Success: false, ToolError: raw}, projection)
	if !strings.Contains(rendered, "FAILED") || !strings.Contains(rendered, "candidate-reference") || !strings.Contains(rendered, "verification-reference") {
		t.Fatal(rendered)
	}
	if got := boundedFailureProjection(raw, 100, continuationDigestOpts{sampleN: 1, scalarMax: 10, maxKeys: 2}); !strings.Contains(got, "truncated") {
		t.Fatal("missing omission disclosure", got)
	}
}

func TestCorrectionsCannotReplaceBoundValues(t *testing.T) {
	before := map[string]interface{}{"id": "exact", "other": "old"}
	protected := map[string]interface{}{"id": "exact"}
	if validParameterCorrection(before, map[string]interface{}{"id": "invented", "other": "new"}, protected) {
		t.Fatal("binding replaced")
	}
	if validParameterCorrection(before, cloneParameterMap(before), protected) {
		t.Fatal("no-op accepted")
	}
	if !validParameterCorrection(before, map[string]interface{}{"id": "exact", "other": "new"}, protected) {
		t.Fatal("valid unbound correction rejected")
	}
}

func TestNoOpCorrectionDoesNotSuppressTransientRetries(t *testing.T) {
	for _, analyzer := range []bool{false, true} {
		catalog := &AgentCatalog{agents: map[string]*AgentInfo{"target": {Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080}, Capabilities: []EnhancedCapability{{Name: "call", Endpoint: "/call"}}}}}
		e := NewSmartExecutor(catalog)
		rt := NewMockRoundTripper()
		rt.SetRetryResponses("http://localhost:8080/call", []struct {
			StatusCode int
			Body       string
		}{{503, `{"success":false,"error":{"code":"BUSY","retryable":true}}`}, {200, `{"success":true}`}})
		e.httpClient = &http.Client{Transport: rt}
		if analyzer {
			e.errorAnalyzer = NewErrorAnalyzer(newMockAIClientWithResponse(map[string]interface{}{"should_retry": true, "suggested_changes": map[string]interface{}{}}), nil)
		} else {
			e.SetCorrectionCallback(func(_ context.Context, _ RoutingStep, params map[string]interface{}, _ string, _ *EnhancedCapability) (map[string]interface{}, error) {
				return params, nil
			})
			e.SetValidationFeedback(true, 2)
		}
		result := e.executeStep(context.Background(), RoutingStep{StepID: "step-1", AgentName: "target", Metadata: map[string]interface{}{"capability": "call", "parameters": map[string]interface{}{"value": "original"}}})
		if !result.Success || result.DispatchCount != 2 || result.Attempts != 2 {
			t.Fatalf("analyzer=%v result=%+v", analyzer, result)
		}
	}
}
