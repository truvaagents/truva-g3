package orchestration

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func dependencySkip(id string, deps ...string) StepResult {
	return StepResult{StepID: id, Skipped: true, Metadata: map[string]interface{}{
		metaKeyBlockingReason: blockingReasonTemplate, metaKeyAllFailedDependencies: deps,
	}}
}

func TestBlockedPhaseRoots(t *testing.T) {
	rootA := StepResult{StepID: "a", Attempts: 1}
	rootB := StepResult{StepID: "b", Attempts: 1}
	first := dependencySkip("first", "b", "a")
	next := dependencySkip("next", "first", "a")
	results := map[string]*StepResult{"a": &rootA, "b": &rootB, "first": &first, "next": &next}
	for _, phase := range [][]StepResult{{first}, {next}, {next, first}} {
		if got := blockedPhaseRoots(phase, results); !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("roots = %v", got)
		}
	}
	// Stored JSON uses []interface{}, not []string.
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &results); err != nil {
		t.Fatal(err)
	}
	if got := blockedPhaseRoots([]StepResult{next}, results); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("round-trip roots = %v", got)
	}
	for _, work := range []StepResult{
		{StepID: "poll", Success: true, Response: `{"status":"running"}`},
		{StepID: "retry", Attempts: 1, Error: "still unavailable"},
		{StepID: "refined", Success: true, Skipped: true},
		{StepID: "unknown", Skipped: true},
	} {
		if got := blockedPhaseRoots([]StepResult{first, work}, results); got != nil {
			t.Errorf("work %s must prevent stopping: %v", work.StepID, got)
		}
	}
	if got := blockedPhaseRoots(nil, results); got != nil {
		t.Errorf("empty phase = %v", got)
	}
	for _, deps := range [][]string{{}, {"missing"}, {"bad"}, {"a", "bad"}} {
		bad := dependencySkip("bad", deps...)
		results["bad"] = &bad
		if got := blockedPhaseRoots([]StepResult{bad}, results); got != nil {
			t.Errorf("incomplete/cyclic evidence %v = %v", deps, got)
		}
	}
}

func TestDependencyDeclarationsMatchParameterReferences(t *testing.T) {
	o := newOrchestratorWithCatalog(t, nil)
	for _, tc := range []struct {
		name      string
		deps      []string
		params    map[string]interface{}
		wantError bool
	}{
		{"literal retry", []string{"step-1"}, map[string]interface{}{"id": "literal"}, true},
		{"extra historical step", []string{"step-1", "step-2"}, map[string]interface{}{"id": "{{step-1.response.data.id}}"}, true},
		{"nested reference", []string{"step-1"}, map[string]interface{}{"items": []interface{}{map[string]interface{}{"id": "{{step-1.response.data.ids.0}}"}}}, false},
		{"independent retry", nil, map[string]interface{}{"id": "literal"}, false},
		{"same phase implicit", []string{"step-3"}, map[string]interface{}{"id": "{{step-3.response.data.id}}"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &RoutingPlan{Steps: []RoutingStep{
				{StepID: "step-3"},
				{StepID: "step-4", DependsOn: []string{"step-3"}, ImplicitDeps: tc.deps, Metadata: map[string]interface{}{"parameters": tc.params}},
			}}
			err := o.validateDependencyConsistency(plan)
			if (err != nil) != tc.wantError {
				t.Fatalf("validation = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

func TestArrayTemplateCorrectionExplainsSupportedIndexSyntax(t *testing.T) {
	params := map[string]interface{}{"id": "{{step-1.response.data.items[0].id}}"}
	plan := &RoutingPlan{Steps: []RoutingStep{{StepID: "step-2", Metadata: map[string]interface{}{"parameters": params}}}}
	err := validateNoUnknownMacros(plan)
	if err == nil || !strings.Contains(err.Error(), "{{step-1.response.data.items.0.id}}") {
		t.Fatalf("missing supported array-index example: %v", err)
	}
	params["id"] = "{{step-1.response.data.items.0.id}}"
	if err := validateNoUnknownMacros(plan); err != nil {
		t.Fatalf("dot-numeric array template rejected: %v", err)
	}
}

func TestDependencySkipPreservesPlannedEvidence(t *testing.T) {
	e := newSkipTestExecutor()
	params := map[string]interface{}{"id": "literal", "nested": map[string]interface{}{"proof": "exact-value"}}
	step := RoutingStep{StepID: "step-2", AgentName: "worker", Namespace: "test", Instruction: "Investigate", ImplicitDeps: []string{"step-1"}, Metadata: map[string]interface{}{
		"capability": "inspect", "parameters": params,
	}}
	results := map[string]*StepResult{"step-1": {StepID: "step-1", Error: "failed"}}
	var callbackResult StepResult
	ctx := WithStepCallback(context.Background(), func(_ int, _ int, _ RoutingStep, result StepResult) { callbackResult = result })
	result := &ExecutionResult{}
	e.skipStepsWithFailedDeps(ctx, &RoutingPlan{Steps: []RoutingStep{step}}, map[string]bool{}, results, result, "req")
	got := results["step-2"]
	if got == nil || !got.Skipped || got.Success || got.Attempts != 0 || got.Capability != "inspect" || got.Instruction != "Investigate" {
		t.Fatalf("skip evidence = %+v", got)
	}
	if got.Parameters != nil || !reflect.DeepEqual(got.Metadata["planned_parameters"], params) || got.SkipReason != got.Error || got.EndTime != got.StartTime {
		t.Fatalf("planned vs dispatched evidence = %+v", got)
	}
	if strings.Contains(got.Error, "parameter references") || !strings.Contains(got.Error, "implicit_deps declares step-1") {
		t.Fatalf("invented reference: %s", got.Error)
	}
	if !reflect.DeepEqual(callbackResult, *got) {
		t.Fatalf("callback lost skip evidence: %+v", callbackResult)
	}
}

type blockedContinuationAI struct {
	mockAIClientRemediationFlow
	retry bool
}

func (m *blockedContinuationAI) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	before := m.planningCalls
	response, err := m.mockAIClientRemediationFlow.GenerateResponse(ctx, prompt, opts)
	if before == 2 && m.planningCalls == 3 {
		step := RoutingStep{StepID: "step-3", AgentName: "flight-tool", Instruction: "Retry lookup", Metadata: map[string]interface{}{
			"capability": "search_airports", "parameters": map[string]interface{}{"keyword": "NYC"},
		}}
		if !m.retry {
			step.ImplicitDeps = []string{"step-2"}
			step.Metadata["parameters"] = map[string]interface{}{"keyword": "{{step-2.response.data.id}}"}
		}
		terminal := false
		body, marshalErr := json.Marshal(RoutingPlan{PlanID: "phase-3", Terminal: &terminal, Steps: []RoutingStep{step}})
		if marshalErr != nil {
			return nil, marshalErr
		}
		response.Content = string(body)
	}
	return response, err
}

func TestPhaseLoopStopsRepeatedDependencyOnlyWork(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked", true: "actual_retry"}[retry], func(t *testing.T) {
			discovery := NewMockDiscovery()
			registration := &core.ServiceRegistration{ID: "flight-1", Name: "flight-tool", Address: "localhost", Port: 8080,
				Capabilities: []core.Capability{{Name: "search_airports"}, {Name: "search_flights"}},
			}
			if err := discovery.Register(context.Background(), registration); err != nil {
				t.Fatal(err)
			}
			ai := &blockedContinuationAI{retry: retry}
			o := NewAIOrchestrator(DefaultConfig(), discovery, ai)
			o.catalog.agents = map[string]*AgentInfo{"flight-1": {Registration: registration, Capabilities: []EnhancedCapability{
				{Name: "search_airports", Endpoint: "/api/airports"}, {Name: "search_flights", Endpoint: "/api/flights"},
			}}}
			o.executor = NewSmartExecutor(o.catalog)
			o.executor.SetMaxAttempts(1)
			transport := NewMockRoundTripper()
			transport.SetResponse("http://localhost:8080/api/airports", 500, `{"error":"unavailable"}`)
			o.executor.httpClient = &http.Client{Transport: transport}
			store := &pipelineHookCaptureExecutionStore{NoOpExecutionStore: NewNoOpExecutionStore(), records: make(chan *StoredExecution, 32)}
			o.SetExecutionStore(store)
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
			ctx, span := provider.Tracer("phase-progress-test").Start(t.Context(), "request")
			response, result, err := o.ProcessRequestWithExecution(ctx, "find flights", nil)
			span.End()
			o.executionWg.Wait()
			close(store.records)
			if err != nil {
				t.Fatal(err)
			}
			wantPhases := 3
			if retry {
				wantPhases = 4
			}
			if result == nil || result.Success || result.PhaseCount != wantPhases || ai.planningCalls != wantPhases || ai.synthCalls != 1 {
				t.Fatalf("result=%+v plans=%d synthesis=%d", result, ai.planningCalls, ai.synthCalls)
			}
			if result.Metadata[MetadataKeyForcedTerminal] != !retry {
				t.Fatalf("forced terminal = %v", result.Metadata)
			}
			if !retry && result.Metadata["termination_reason"] != "repeated_failed_dependencies" {
				t.Fatalf("missing stop reason: %v", result.Metadata)
			}
			if len(result.Steps) != 3 || result.Steps[0].Skipped || !result.Steps[1].Skipped || result.Steps[2].Skipped == retry {
				t.Fatalf("lost execution evidence: %+v", result.Steps)
			}
			var final *StoredExecution
			for record := range store.records {
				final = record
			}
			if final == nil || final.Result == nil || final.Result.Success || len(final.Result.Steps) != 3 || final.ForcedTerminal != !retry {
				t.Fatalf("stored terminal evidence = %+v", final)
			}
			found := false
			for _, ended := range recorder.Ended() {
				for _, event := range ended.Events() {
					if event.Name != "orchestrator.forced_terminal" {
						continue
					}
					found = true
					if len(event.Attributes) == 0 || event.Attributes[0].Key != "request_id" || event.Attributes[0].Value.AsString() != response.RequestID {
						t.Fatalf("missing first request correlation: %+v", event.Attributes)
					}
					if ended.Status().Code != codes.Error {
						t.Fatalf("stopped request span was not failed: %+v", ended.Status())
					}
				}
			}
			if found == retry {
				t.Fatalf("forced-terminal event=%v, retry=%v", found, retry)
			}
		})
	}
}
