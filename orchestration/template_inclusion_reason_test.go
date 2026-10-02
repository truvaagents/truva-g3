package orchestration

import (
	"encoding/json"
	"reflect"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestTemplateInclusionReason(t *testing.T) {
	for _, declaration := range []string{"implicit", "explicit", "undeclared"} {
		for _, state := range []string{"success", "failed", "skipped", "missing", "invalid_json"} {
			t.Run(declaration+"/"+state, func(t *testing.T) {
				logger := &MockLogger{}
				executor := &SmartExecutor{logger: logger}
				step := RoutingStep{StepID: "step-2", Metadata: map[string]interface{}{
					"parameters": map[string]interface{}{"value": "{{step-1.response.value}}", "again": "{{step-1.response.value}}"},
				}}
				switch declaration {
				case "implicit":
					step.ImplicitDeps = []string{"step-1"}
				case "explicit":
					step.DependsOn = []string{"step-1"}
				}
				before, _ := json.Marshal(step)
				source := &StepResult{Success: state != "failed", Skipped: state == "skipped", Response: `{"value":"exact-界"}`}
				if state == "invalid_json" {
					source.Response = "not JSON"
				}
				results := map[string]*StepResult{"step-1": source}
				if state == "missing" {
					delete(results, "step-1")
				}
				recorder := setupExecutorTestTracer(t)
				ctx, span := otel.Tracer("inclusion-test").Start(t.Context(), "context")
				ctx, inclusions := executor.buildStepContext(ctx, step, results)
				span.End()
				deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})
				_, available := deps["step-1"]
				if available != (state == "success") {
					t.Fatalf("unavailable evidence admitted: %v", deps)
				}
				if available && !reflect.DeepEqual(deps["step-1"]["response"], map[string]interface{}{"value": "exact-界"}) {
					t.Fatal("binding contents changed")
				}
				wantInclusion := available && declaration != "explicit"
				if (len(inclusions) == 1) != wantInclusion {
					t.Fatalf("wrong inclusion count: %+v", inclusions)
				}
				if wantInclusion {
					reason := "undeclared_reference"
					if declaration == "implicit" {
						reason = "declared_prior_phase"
					}
					if inclusions[0].Reason != reason {
						t.Fatalf("wrong reason: %+v", inclusions)
					}
					data, _ := json.Marshal(inclusions)
					var persisted []map[string]string
					_ = json.Unmarshal(data, &persisted)
					if persisted[0]["reason"] != reason || persisted[0]["referenced_step"] != "step-1" {
						t.Fatal("metadata did not preserve reason and legacy fields")
					}
					events := recorder.Ended()[0].Events()
					if len(events) != 1 || events[0].Name != "orchestration.template.dependency_inclusion" || events[0].Attributes[0].Value.AsString() != reason {
						t.Fatalf("missing trace reason: %+v", events)
					}
					if (len(logger.warnCalls) > 0) != (declaration == "undeclared") {
						t.Fatalf("misleading warning: %v", logger.warnCalls)
					}
				}
				after, _ := json.Marshal(step)
				if string(before) != string(after) {
					t.Fatal("diagnostic changed admitted dependencies/parameters")
				}
			})
		}
	}
}
