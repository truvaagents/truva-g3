package orchestration

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeGeneratedPlanDerivesPriorReferenceWithoutChangingOriginal(t *testing.T) {
	original := &RoutingPlan{Steps: []RoutingStep{{
		StepID: "step-2", Metadata: map[string]interface{}{"parameters": map[string]interface{}{
			"lookup": []interface{}{"{{step-1.response.data.items[01].tags[1]}}", "{{now}}"},
		}},
	}}}
	prior := map[string]*StepResult{"step-1": {Success: false}}
	got, changes, err := normalizeGeneratedPlan(original, prior)
	if err != nil {
		t.Fatal(err)
	}
	if changes != 2 || !reflect.DeepEqual(got.Steps[0].ImplicitDeps, []string{"step-1"}) {
		t.Fatalf("normalization changes=%d deps=%v", changes, got.Steps[0].ImplicitDeps)
	}
	values := got.Steps[0].Metadata["parameters"].(map[string]interface{})["lookup"].([]interface{})
	if values[0] != "{{step-1.response.data.items.01.tags.1}}" || values[1] != "{{now}}" {
		t.Fatalf("normalized values = %v", values)
	}
	before := original.Steps[0].Metadata["parameters"].(map[string]interface{})["lookup"].([]interface{})
	if before[0] != "{{step-1.response.data.items[01].tags[1]}}" || len(original.Steps[0].ImplicitDeps) != 0 {
		t.Fatalf("original candidate mutated: %#v", original)
	}
	again, changes, err := normalizeGeneratedPlan(got, prior)
	if err != nil || changes != 0 || !reflect.DeepEqual(again, got) {
		t.Fatalf("normalization not idempotent: changes=%d err=%v", changes, err)
	}
}

func TestNormalizeGeneratedPlanKeepsContradictoryFailureGuardForValidation(t *testing.T) {
	plan := &RoutingPlan{Steps: []RoutingStep{{
		StepID: "step-3", ImplicitDeps: []string{"step-1"},
		Metadata: map[string]interface{}{"parameters": map[string]interface{}{
			"value": "{{step-2.response.data.value}}",
		}},
	}}}
	_, _, err := normalizeGeneratedPlan(plan, map[string]*StepResult{
		"step-1": {Success: false}, "step-2": {Success: true},
	})
	if err == nil || !strings.Contains(err.Error(), "step-1") {
		t.Fatalf("surplus failed-dependency guard was erased: %v", err)
	}
	if !reflect.DeepEqual(plan.Steps[0].ImplicitDeps, []string{"step-1"}) {
		t.Fatalf("original guard changed: %v", plan.Steps[0].ImplicitDeps)
	}
}

func TestNormalizeGeneratedPlanRejectsUnknownAndLeavesOtherBracketForms(t *testing.T) {
	plan := &RoutingPlan{Steps: []RoutingStep{{
		StepID: "step-2", Metadata: map[string]interface{}{"parameters": map[string]interface{}{
			"value": "{{step-99.response.data.items[0]}}",
		}},
	}}}
	if _, _, err := normalizeGeneratedPlan(plan, nil); err == nil {
		t.Fatal("unknown prior step accepted")
	}
	for _, path := range []string{"items[-1]", "items[key]", "items[1+1]"} {
		value, changes := rewriteNumericBracketSegments("{{step-1.response.data." + path + "}}")
		if changes != 0 || value != "{{step-1.response.data."+path+"}}" {
			t.Fatalf("unsupported bracket %s was rewritten to %v", path, value)
		}
	}
}

func TestNormalizeGeneratedPlanPreservesCanonicalOrderingAndNumericMapKeys(t *testing.T) {
	plan := &RoutingPlan{Steps: []RoutingStep{
		{StepID: "step-3", Metadata: map[string]interface{}{"parameters": map[string]interface{}{"literal": "{{now}}"}}},
		{
			StepID: "step-4", DependsOn: []string{"step-3"}, ImplicitDeps: []string{"step-1", "step-2"},
			Metadata: map[string]interface{}{"parameters": map[string]interface{}{
				"nested": []interface{}{map[string]interface{}{
					"key":        "prefix {{step-1.response.data.values.01}} and {{step-2.response.data.id}}",
					"same_phase": "{{step-3.response.data.id}}",
				}},
			}},
		},
	}}
	prior := map[string]*StepResult{"step-1": {Success: true}, "step-2": {Success: false}}
	got, changes, err := normalizeGeneratedPlan(plan, prior)
	if err != nil || changes != 0 || !reflect.DeepEqual(got, plan) {
		t.Fatalf("valid plan changed: changes=%d err=%v got=%#v", changes, err, got)
	}
	if got == plan || &got.Steps[0] == &plan.Steps[0] {
		t.Fatal("normalized plan aliases its input")
	}
}
