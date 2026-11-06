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
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/codes"
)

func TestInvalidCapabilityWarningPreservesRequestCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		orchestrator  string
		core          string
		baggage       string
		wantRequestID string
	}{
		{name: "orchestrator identity", orchestrator: "managed-request", wantRequestID: "managed-request"},
		{name: "core identity", core: "direct-request", wantRequestID: "direct-request"},
		{name: "baggage identity", baggage: "baggage-request", wantRequestID: "baggage-request"},
		{name: "managed identity precedes inherited context", orchestrator: "managed-request", core: "older-core-request", baggage: "older-baggage-request", wantRequestID: "managed-request"},
		{name: "core identity precedes inherited baggage", core: "direct-request", baggage: "older-baggage-request", wantRequestID: "direct-request"},
		{name: "no request identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.orchestrator != "" {
				ctx = WithRequestID(ctx, tc.orchestrator)
			}
			if tc.core != "" {
				ctx = core.WithRequestID(ctx, tc.core)
			}
			if tc.baggage != "" {
				ctx = telemetry.WithBaggage(ctx, "request_id", tc.baggage)
			}
			logger := &TestLogger{}
			executor := NewSmartExecutor(nil)
			executor.SetLogger(logger)
			_, err := executor.ExecuteStep(ctx, RoutingStep{StepID: "step-1", AgentName: "target"})
			if err == nil || !strings.Contains(err.Error(), "metadata.capability") {
				t.Fatalf("expected capability validation error, got %v", err)
			}
			warnings := logger.GetLogsByLevel("WARN")
			if len(warnings) != 1 || warnings[0].Fields["operation"] != "step_validation" {
				t.Fatalf("expected one validation warning, got %+v", warnings)
			}
			requestID, present := warnings[0].Fields["request_id"]
			if tc.wantRequestID == "" && present || tc.wantRequestID != "" && requestID != tc.wantRequestID {
				t.Fatalf("validation warning lost request correlation: %+v, want %q", warnings, tc.wantRequestID)
			}
			if GetRequestID(ctx) != tc.orchestrator || core.GetRequestID(ctx) != tc.core || telemetry.GetBaggage(ctx)["request_id"] != tc.baggage {
				t.Fatal("validation changed the caller's request context")
			}
		})
	}
}

func TestInvalidCapabilityRejectedBeforeStepSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]interface{}
	}{
		{"nil metadata", nil},
		{"missing capability", map[string]interface{}{"Well": "placeholder"}},
		{"null capability", map[string]interface{}{"capability": nil}},
		{"non-string capability", map[string]interface{}{"capability": 42}},
		{"empty capability", map[string]interface{}{"capability": ""}},
		{"blank capability", map[string]interface{}{"capability": " \t\n\u2003"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := setupExecutorTestTracer(t)
			catalog := &AgentCatalog{agents: map[string]*AgentInfo{"target": {
				Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080},
			}}}
			e := NewSmartExecutor(catalog)
			logger := &TestLogger{}
			if tc.metadata != nil {
				e.SetLogger(logger)
			}
			rt := NewMockRoundTripper()
			e.httpClient = &http.Client{Transport: rt}
			ai := newMockAIClientWithResponse(map[string]interface{}{"should_retry": false})
			e.errorAnalyzer = NewErrorAnalyzer(ai, nil)
			e.contextualReResolver = NewContextualReResolver(ai, nil)
			approval := &recordingInterruptController{}
			e.interruptController = approval
			step := RoutingStep{StepID: "step-15", AgentName: "target", Metadata: tc.metadata}
			ctx := WithRequestID(t.Context(), "capability-rejection-request")
			result := e.executeStep(ctx, step)
			if result.Success || !strings.Contains(result.Error, "metadata.capability") || !strings.Contains(result.Error, step.StepID) {
				t.Fatalf("expected actionable capability rejection: %+v", result)
			}
			if result.Attempts != 0 || result.DispatchCount != 0 || len(result.Dispatches) != 0 || rt.GetCallCount() != 0 || len(ai.calls) != 0 || approval.observedStepID != "" {
				t.Fatalf("invalid capability caused side effects: result=%+v HTTP=%d AI=%d approval=%q", result, rt.GetCallCount(), len(ai.calls), approval.observedStepID)
			}
			if result.StartTime.IsZero() || result.EndTime.Before(result.StartTime) || result.Duration < 0 {
				t.Fatalf("rejected step timing missing: %+v", result)
			}
			if tc.metadata != nil {
				warnings := logger.GetLogsByLevel("WARN")
				if len(warnings) != 1 || warnings[0].Fields["operation"] != "step_validation" || warnings[0].Fields["request_id"] != "capability-rejection-request" || warnings[0].Fields["error"] != result.Error {
					t.Fatalf("rejection log lost operation, correlation or error: %+v", warnings)
				}
			}
			spans := recorder.Ended()
			if len(spans) != 1 || spans[0].Name() != "orchestrator.step.step-15" || spans[0].Status().Code != codes.Error {
				t.Fatalf("rejected step must close its span with error: %v", spans)
			}
			if result, err := e.ExecuteStep(t.Context(), step); err == nil || result != nil || !strings.Contains(err.Error(), "metadata.capability") {
				t.Fatalf("public ExecuteStep failure contract changed: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestNamedCapabilityWithoutSchemaPreservesDispatchFailure(t *testing.T) {
	catalog := &AgentCatalog{agents: map[string]*AgentInfo{"target": {
		Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080},
	}}}
	e := NewSmartExecutor(catalog)
	rt := NewMockRoundTripper()
	const body = "404 page not found"
	rt.SetResponse("http://localhost:8080/api/inspect", http.StatusNotFound, body)
	e.httpClient = &http.Client{Transport: rt}
	ai := newMockAIClientWithResponse(map[string]interface{}{"should_retry": false, "reason": "Endpoint is unavailable"})
	e.errorAnalyzer = NewErrorAnalyzer(ai, nil)
	e.contextualReResolver = NewContextualReResolver(ai, nil)
	e.semanticRetryForIndependentSteps = true
	result := e.executeStep(t.Context(), RoutingStep{
		StepID: "step-15", AgentName: "target", Metadata: map[string]interface{}{"capability": "inspect"},
	})
	if result.Success || !strings.Contains(result.Error, body) || strings.Contains(result.Error, "panic") || strings.Contains(result.Error, "schema is required") {
		t.Fatalf("original failure replaced: %+v", result)
	}
	if result.DispatchCount != 1 || len(result.Dispatches) != 1 || rt.GetCallCount() != 1 || result.Dispatches[0].Response != body || result.Dispatches[0].Success {
		t.Fatalf("original dispatch evidence lost: %+v", result)
	}
	if len(ai.calls) != 1 || result.EndTime.Before(result.StartTime) {
		t.Fatalf("unexpected retry or incomplete step: AI calls=%d result=%+v", len(ai.calls), result)
	}
}

func TestCapabilityNamePreservesNamedEndpointFallback(t *testing.T) {
	for _, withSchema := range []bool{false, true} {
		t.Run(fmt.Sprintf("schema=%v", withSchema), func(t *testing.T) {
			agent := &AgentInfo{Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080}}
			if withSchema {
				agent.Capabilities = []EnhancedCapability{{Name: "inspect", Endpoint: "/api/inspect"}}
			}
			e := NewSmartExecutor(&AgentCatalog{agents: map[string]*AgentInfo{"target": agent}})
			rt := NewMockRoundTripper()
			rt.SetResponse("http://localhost:8080/api/inspect", http.StatusOK, `{"success":true}`)
			e.httpClient = &http.Client{Transport: rt}
			result := e.executeStep(t.Context(), RoutingStep{StepID: "step-1", AgentName: "target", Metadata: map[string]interface{}{"capability": "inspect"}})
			if !result.Success || result.Capability != "inspect" || result.DispatchCount != 1 || rt.GetCallCount() != 1 {
				t.Fatalf("named zero-argument call rejected: %+v", result)
			}
		})
	}
	step := RoutingStep{StepID: "step-1", Metadata: map[string]interface{}{"capability": " inspect "}}
	if got, err := stepCapabilityName(step); err != nil || got != " inspect " {
		t.Fatalf("capability identity was rewritten: name=%q err=%v", got, err)
	}
}

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

func TestDeclaredDispatchConstraintsBeforeHTTP(t *testing.T) {
	for _, tc := range []struct {
		name      string
		parameter Parameter
		params    map[string]interface{}
		wantError string
	}{
		{"missing-required", Parameter{Name: "choice", Type: "string", Required: true}, map[string]interface{}{}, `required parameter "choice" is unresolved`},
		{"outside-enum", Parameter{Name: "choice", Type: "string", Enum: []string{"one", "two"}}, map[string]interface{}{"choice": "three"}, `parameter "choice" is outside its declared enum`},
		{"allowed-enum", Parameter{Name: "choice", Type: "string", Enum: []string{"one", "two"}}, map[string]interface{}{"choice": "two"}, ""},
		{"optional-absent", Parameter{Name: "choice", Type: "string", Enum: []string{"one"}}, map[string]interface{}{}, ""},
		{"explicit-null", Parameter{Name: "choice", Type: "string", Required: true, Enum: []string{"one"}}, map[string]interface{}{"choice": nil}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &AgentCatalog{agents: map[string]*AgentInfo{"target": {
				Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080},
				Capabilities: []EnhancedCapability{{Name: "call", Endpoint: "/call", Parameters: []Parameter{tc.parameter}}},
			}}}
			e := NewSmartExecutor(catalog)
			rt := NewMockRoundTripper()
			rt.SetResponse("http://localhost:8080/call", 200, `{"success":true}`)
			e.httpClient = &http.Client{Transport: rt}
			result := e.executeStep(t.Context(), RoutingStep{StepID: "step-1", AgentName: "target", Metadata: map[string]interface{}{"capability": "call", "parameters": tc.params}})
			if tc.wantError != "" {
				if result.Success || !strings.Contains(result.Error, tc.wantError) || rt.GetCallCount() != 0 || result.DispatchCount != 0 || len(result.Dispatches) != 0 {
					t.Fatalf("invalid input dispatched or lost rejection: %+v", result)
				}
			} else if !result.Success || rt.GetCallCount() != 1 || result.DispatchCount != 1 {
				t.Fatalf("valid input rejected: %+v", result)
			}
		})
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
