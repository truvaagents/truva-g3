package orchestration

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestExactBindingShapeBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, schema, reference string
		want                    interface{}
		reject                  bool
	}{
		{"object-to-string", "string", "{{step-1.response.data.ref}}", map[string]interface{}{"id": "one"}, true},
		{"object-to-object", "object", "{{step-1.response.data.ref}}", map[string]interface{}{"id": "one"}, false},
		{"array-to-string", "string", "{{step-1.response.data.list}}", []interface{}{"one"}, true},
		{"array-to-array", "array", "{{step-1.response.data.list}}", []interface{}{"one"}, false},
		{"dotted-string", "string", "{{step-1.response.data.ref.id}}", "one", false},
		{"literal-json", "string", `{"id":"literal"}`, `{"id":"literal"}`, false},
		{"embedded", "string", "result: {{step-1.response.data.ref.id}}", "result: one", false},
		{"no-schema", "", "{{step-1.response.data.ref}}", map[string]interface{}{"id": "one"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &AgentCatalog{agents: map[string]*AgentInfo{"target": {
				Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080},
				Capabilities: []EnhancedCapability{{Name: "get", Endpoint: "/get", Parameters: []Parameter{{Name: "value", Type: tc.schema, Required: true}}}},
			}}}
			e := NewSmartExecutor(catalog)
			rt := NewMockRoundTripper()
			rt.SetResponse("http://localhost:8080/get", http.StatusOK, `{"success":true}`)
			e.httpClient = &http.Client{Transport: rt}
			step := RoutingStep{StepID: "step-2", AgentName: "target", ImplicitDeps: []string{"step-1"}, Metadata: map[string]interface{}{"capability": "get", "parameters": map[string]interface{}{"value": tc.reference}}}
			ctx, _ := e.buildStepContext(t.Context(), step, map[string]*StepResult{"step-1": {Success: true, Response: `{"data":{"ref":{"id":"one"},"list":["one"]}}`}})
			result := e.executeStep(ctx, step)
			require.Equal(t, !tc.reject, result.Success, result.Error)
			require.Equal(t, tc.want, result.Parameters["value"])
			if tc.reject {
				require.Empty(t, result.Dispatches)
				require.Zero(t, result.DispatchCount)
				require.Equal(t, "binding_shape_mismatch", result.Metadata["failure_kind"])
			} else {
				require.Len(t, result.Dispatches, 1)
				require.Equal(t, tc.want, result.Dispatches[0].Parameters["value"])
			}
		})
	}
}

func TestDispatchContainerShapeGuard(t *testing.T) {
	for _, expected := range []string{"string", "integer", "boolean", "array"} {
		capability := &EnhancedCapability{Parameters: []Parameter{{Name: "value", Type: expected}}}
		require.Error(t, validateDispatchParameters(map[string]interface{}{"value": map[string]interface{}{"id": "one"}}, capability))
	}
	params, _ := coerceParameterTypes(map[string]interface{}{"value": map[string]interface{}{"code": "EUR", "name": "Euro"}}, []Parameter{{Name: "value", Type: "string"}})
	require.Equal(t, "EUR", params["value"], "semantic nested extraction remains available")
}

func TestBindingSourcesDoNotCollide(t *testing.T) {
	h := NewHybridResolver(nil, nil)
	sources := map[string]*StepResult{
		"step-1": {Success: true, Response: `{"data":{"workspace_id":"workspace-one"},"success":true}`},
		"step-2": {Success: true, Response: `{"data":{"encoded":"first"},"success":true}`},
		"step-3": {Success: true, Response: `{"data":{"encoded":"second"},"success":true}`},
	}
	result, err := h.ResolveParameters(context.Background(), sources, &EnhancedCapability{Parameters: []Parameter{{Name: "workspace_id", Type: "string", Required: true}}}, "step-4", "")
	if err != nil || result.Parameters["workspace_id"] != "workspace-one" {
		t.Fatalf("unique source was lost: %+v, %v", result, err)
	}
	conflict := h.autoWirer.AutoWireFromMultipleSources(map[string]string{
		"step-1": `{"data":{"id":"one"}}`, "step-2": `{"data":{"id":"two"}}`,
	}, []Parameter{{Name: "id", Type: "string", Required: true}})
	if _, ok := conflict.Parameters["id"]; ok {
		t.Fatalf("ambiguous source silently selected: %+v", conflict)
	}
}

func TestExplicitBindingWinsOverOrderingDependency(t *testing.T) {
	ai := newMockAIClientWithResponse(map[string]interface{}{"id": nil})
	catalog := &AgentCatalog{agents: map[string]*AgentInfo{"target": {
		Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080},
		Capabilities: []EnhancedCapability{{Name: "get", Endpoint: "/get", Parameters: []Parameter{{Name: "id", Type: "string", Required: true}, {Name: "optional", Type: "string"}}}},
	}}}
	e := NewSmartExecutor(catalog)
	e.hybridResolver = NewHybridResolver(ai, nil)
	e.useHybridResolution = true
	rt := NewMockRoundTripper()
	rt.SetResponse("http://localhost:8080/get", http.StatusOK, `{"success":true}`)
	e.httpClient = &http.Client{Transport: rt}
	step := RoutingStep{StepID: "step-3", AgentName: "target", DependsOn: []string{"step-2"}, ImplicitDeps: []string{"step-1"}, Metadata: map[string]interface{}{"capability": "get", "parameters": map[string]interface{}{"id": "{{step-1.response.data.id}}"}}}
	ctx, _ := e.buildStepContext(context.Background(), step, map[string]*StepResult{
		"step-1": {Success: true, Response: `{"data":{"id":"correct-id"}}`},
		"step-2": {Success: true, Response: `{"data":{"optional":"ordering-only-value"}}`},
	})
	result := e.executeStep(ctx, step)
	if !result.Success || result.Parameters["id"] != "correct-id" || len(ai.calls) != 0 {
		t.Fatalf("binding failed: %+v; LLM calls=%d", result, len(ai.calls))
	}
	if step.Metadata["parameters"].(map[string]interface{})["id"] != "{{step-1.response.data.id}}" {
		t.Fatal("admitted plan was mutated")
	}
	if _, exists := result.Parameters["optional"]; exists {
		t.Fatal("ordering dependency populated absent optional parameter")
	}
}

func TestTemplateSourcesUseExecutionStatus(t *testing.T) {
	for _, mode := range []string{"failed", "skipped", "nil", "successful-payload-false"} {
		for _, declared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/declared=%v", mode, declared), func(t *testing.T) {
				ai := newMockAIClientWithResponse(map[string]interface{}{"currency": "EUR"})
				e := NewSmartExecutor(&AgentCatalog{})
				e.hybridResolver = NewHybridResolver(ai, nil)
				result := &StepResult{Success: mode == "successful-payload-false", Skipped: mode == "skipped", Response: `{"success":false,"data":{"country":"France"}}`}
				if mode == "nil" {
					result = nil
				}
				params := map[string]interface{}{"currency": "{{step-1.response.data.country.currency}}"}
				step := RoutingStep{StepID: "step-2", Metadata: map[string]interface{}{"parameters": params}}
				if declared {
					step.DependsOn = []string{"step-1"}
				}
				ctx, _ := e.buildStepContext(context.Background(), step, map[string]*StepResult{"step-1": result})
				sources := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})
				got := e.resolveUnresolvedTemplatesWithLLM(ctx, params, sources, step.StepID)
				if mode == "successful-payload-false" {
					if got["currency"] != "EUR" || len(ai.calls) != 1 {
						t.Fatal("application field overrode execution success")
					}
				} else if len(sources) != 0 || len(ai.calls) != 0 || got["currency"] != params["currency"] {
					t.Fatal("failed/skipped source reached inference")
				}
			})
		}
	}
}

func TestScopedFallbackRetainsInferenceAndRejectsNull(t *testing.T) {
	for _, test := range []struct {
		name   string
		output interface{}
		source bool
		want   string
		calls  int
	}{
		{"inference", "EUR", true, "EUR", 1}, {"null", nil, true, "{{step-1.response.data.country.currency}}", 1}, {"missing", "EUR", false, "{{step-1.response.data.country.currency}}", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			ai := newMockAIClientWithResponse(map[string]interface{}{"currency": test.output})
			e := NewSmartExecutor(&AgentCatalog{})
			e.hybridResolver = NewHybridResolver(ai, nil)
			sources := map[string]map[string]interface{}{"step-2": {"response": map[string]interface{}{"secret_unrelated": "must-not-be-seen"}}}
			if test.source {
				sources["step-1"] = map[string]interface{}{"response": map[string]interface{}{"data": map[string]interface{}{"country": "France"}}}
			}
			params := map[string]interface{}{"currency": "{{step-1.response.data.country.currency}}"}
			got := e.resolveUnresolvedTemplatesWithLLM(context.Background(), params, sources, "step-3")
			if got["currency"] != test.want || len(ai.calls) != test.calls {
				t.Fatalf("got %v, calls %d", got, len(ai.calls))
			}
			for _, prompt := range ai.calls {
				if strings.Contains(prompt, "must-not-be-seen") {
					t.Fatal("fallback mixed unrelated sources")
				}
			}
		})
	}
}

func TestBindingCopyPreservesNestedPlan(t *testing.T) {
	original := map[string]interface{}{"files": []interface{}{map[string]interface{}{"text": "original"}}, "number": int64(7)}
	copy := cloneParameterMap(original)
	copy["files"].([]interface{})[0].(map[string]interface{})["text"] = "changed"
	if original["files"].([]interface{})[0].(map[string]interface{})["text"] != "original" || !reflect.DeepEqual(copy["number"], int64(7)) {
		t.Fatal("copy aliased plan or changed its types")
	}
}

func TestNullIsNotAnAutoWiredString(t *testing.T) {
	r := AutoWireParameters(map[string]interface{}{"id": nil}, []Parameter{{Name: "id", Type: "string", Required: true}})
	if _, ok := r.Parameters["id"]; ok {
		t.Fatalf("null claimed as resolved: %+v", r)
	}
}
