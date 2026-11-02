package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type cancellationTestTransport func(*http.Request) (*http.Response, error)

func (transport cancellationTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func newCancellationTestExecutor(transport http.RoundTripper, count int) (*SmartExecutor, *RoutingPlan) {
	catalog := &AgentCatalog{agents: map[string]*AgentInfo{
		"test-tool": {
			Registration: &core.ServiceRegistration{ID: "test-tool", Name: "test-tool", Address: "localhost", Port: 8080},
			Capabilities: []EnhancedCapability{{Name: "observe", Endpoint: "/observe"}},
		},
	}}
	executor := NewSmartExecutor(catalog)
	executor.SetMaxAttempts(1)
	executor.httpClient = &http.Client{Transport: transport}
	plan := &RoutingPlan{PlanID: "observed-plan"}
	for index := range count {
		plan.Steps = append(plan.Steps, RoutingStep{
			StepID: fmt.Sprintf("step-%d", index+1), AgentName: "test-tool",
			Metadata: map[string]interface{}{
				"capability": "observe",
				"parameters": map[string]interface{}{"input": map[string]interface{}{"text": "a<b *pointer"}},
			},
		})
	}
	return executor, plan
}

func successfulCancellationResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"success":true,"data":{"observed":true,"text":"a<b *pointer"}}`)),
	}
}

func TestSmartExecutorCancellationPreservesObservedDispatch(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cause := errors.New("caller supplied stop cause <exact>")
	executor, plan := newCancellationTestExecutor(cancellationTestTransport(func(*http.Request) (*http.Response, error) {
		return successfulCancellationResponse(), nil
	}), 1)
	var callbackResult StepResult
	callbacks := 0
	executor.SetOnStepComplete(func(_ int, _ int, _ RoutingStep, result StepResult) {
		callbackResult = result
		callbacks++
		cancel(cause)
	})

	result, err := executor.Execute(ctx, plan)
	if !errors.Is(err, context.Canceled) || result == nil {
		t.Fatalf("Execute returned result=%+v, err=%v", result, err)
	}
	if result.Success || result.TotalDuration <= 0 || len(result.Steps) != 1 || callbacks != 1 {
		t.Fatalf("aggregate cancellation lost observed work: result=%+v callbacks=%d", result, callbacks)
	}
	step := result.Steps[0]
	if !step.Success || step.DispatchCount != 1 || len(step.Dispatches) != 1 || !step.Dispatches[0].Success ||
		step.Dispatches[0].Response != `{"success":true,"data":{"observed":true,"text":"a<b *pointer"}}` {
		t.Fatalf("successful dispatch was changed by cancellation: %+v", step)
	}
	if result.Metadata["termination_reason"] != "cancelled" || result.Metadata["execution_error"] != context.Canceled.Error() ||
		result.Metadata["context_cause"] != cause.Error() {
		t.Fatalf("observed cancellation diagnostics = %+v", result.Metadata)
	}
	callbackResult.Parameters["input"].(map[string]interface{})["text"] = "changed after return"
	callbackResult.Dispatches[0].Parameters["input"].(map[string]interface{})["text"] = "changed dispatch after return"
	callbackResult.Dispatches[0].Response = "changed response after return"
	if result.Steps[0].Parameters["input"].(map[string]interface{})["text"] != "a<b *pointer" ||
		result.Steps[0].Dispatches[0].Parameters["input"].(map[string]interface{})["text"] != "a<b *pointer" ||
		result.Steps[0].Dispatches[0].Response != step.Dispatches[0].Response {
		t.Fatal("returned cancellation snapshot aliases callback-owned mutable containers")
	}
	if ttl := executionRetentionTTLAt(&StoredExecution{Result: result}, time.Minute, time.Hour, time.Now()); ttl != time.Hour {
		t.Fatalf("direct-plan cancellation retention = %v, want error TTL", ttl)
	}
}

func TestSmartExecutorCancellationBeforeAndDuringDispatch(t *testing.T) {
	for _, mode := range []string{"before", "during", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantErr, wantReason := context.Canceled, "cancelled"
			if mode == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
				defer deadlineCancel()
				wantErr, wantReason = context.DeadlineExceeded, "deadline"
			}
			if mode == "before" {
				cancel()
			}
			executor, plan := newCancellationTestExecutor(cancellationTestTransport(func(request *http.Request) (*http.Response, error) {
				if mode == "during" {
					cancel()
				}
				<-request.Context().Done()
				return nil, request.Context().Err()
			}), 1)
			var callbacks atomic.Int32
			executor.SetOnStepComplete(func(int, int, RoutingStep, StepResult) { callbacks.Add(1) })
			result, err := executor.Execute(ctx, plan)
			if !errors.Is(err, wantErr) || result == nil || result.Success || result.TotalDuration <= 0 {
				t.Fatalf("Execute result=%+v err=%v, want %v", result, err, wantErr)
			}
			if len(result.Steps) != 1 || callbacks.Load() != 1 || result.Steps[0].Success || result.Metadata["termination_reason"] != wantReason {
				t.Fatalf("observed failed step/diagnostics were lost: %+v callbacks=%d", result, callbacks.Load())
			}
		})
	}
}

func TestSmartExecutorCancellationJoinsQueuedSiblings(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executor, plan := newCancellationTestExecutor(cancellationTestTransport(func(request *http.Request) (*http.Response, error) {
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		return successfulCancellationResponse(), nil
	}), 4)
	executor.SetMaxConcurrency(1)
	firstCallback := make(chan struct{})
	releaseCallback := make(chan struct{})
	defer func() {
		select {
		case <-releaseCallback:
		default:
			close(releaseCallback)
		}
	}()
	var callbacks atomic.Int32
	executor.SetOnStepComplete(func(_ int, _ int, _ RoutingStep, result StepResult) {
		if callbacks.Add(1) == 1 {
			if !result.Success {
				t.Error("first observed result should succeed before cancellation")
			}
			cancel()
			close(firstCallback)
			<-releaseCallback
		}
	})
	type outcome struct {
		result *ExecutionResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := executor.Execute(ctx, plan)
		done <- outcome{result, err}
	}()
	select {
	case <-firstCallback:
	case <-time.After(5 * time.Second):
		t.Fatal("first callback did not complete")
	}
	select {
	case <-done:
		t.Fatal("executor returned before the active callback/worker joined")
	default:
	}
	close(releaseCallback)
	select {
	case observed := <-done:
		if !errors.Is(observed.err, context.Canceled) || observed.result == nil || observed.result.Success ||
			len(observed.result.Steps) != len(plan.Steps) || callbacks.Load() != int32(len(plan.Steps)) {
			t.Fatalf("joined sibling evidence result=%+v err=%v callbacks=%d", observed.result, observed.err, callbacks.Load())
		}
		ids := make(map[string]bool)
		for _, step := range observed.result.Steps {
			if ids[step.StepID] {
				t.Fatalf("duplicate observed step %q", step.StepID)
			}
			ids[step.StepID] = true
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued workers failed to join after cancellation")
	}
}

func TestSmartExecutorCancellationAtSubphaseGate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	transport := NewMockRoundTripper()
	executor, plan := newCancellationTestExecutor(transport, 3)
	executor.catalog.agents["test-tool"].Capabilities = append(executor.catalog.agents["test-tool"].Capabilities,
		EnhancedCapability{Name: "delegate", Type: core.CapabilityOrchestrator, Endpoint: "/delegate"})
	// The direct executor's existing wait boundary also handles a blocked
	// subphase. Cancellation at that boundary must retain restored evidence.
	plan.Steps[0].Metadata["capability"] = "delegate"
	plan.Steps[0].DependsOn = []string{"step-1"}
	plan.Steps[1].DependsOn = []string{"step-1"}
	ctx = WithCompletedSteps(ctx, map[string]*StepResult{
		"step-3": {StepID: "step-3", Success: true, Response: `{"success":true}`, Metadata: map[string]interface{}{"phase_number": 1}},
	})
	result, err := executor.Execute(ctx, plan)
	if !errors.Is(err, context.Canceled) || result == nil || result.Success || len(result.Steps) != 1 ||
		result.Steps[0].StepID != "step-3" || !result.Steps[0].Success || result.TotalDuration <= 0 || transport.GetCallCount() != 0 {
		t.Fatalf("gated cancellation lost restored evidence: result=%+v err=%v", result, err)
	}
}

type cancellationHITLController struct {
	*mockInterruptController
	cancel context.CancelFunc
	ready  <-chan struct{}
	point  *ExecutionCheckpoint
}

func (controller *cancellationHITLController) CheckBeforeStep(_ context.Context, step RoutingStep, _ *RoutingPlan) (*ExecutionCheckpoint, error) {
	if step.StepID != "step-2" {
		return nil, nil
	}
	<-controller.ready
	controller.cancel()
	return controller.point, nil
}

func TestSmartExecutorCancellationPreservesHITLPrecedence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executor, plan := newCancellationTestExecutor(cancellationTestTransport(func(*http.Request) (*http.Response, error) {
		return successfulCancellationResponse(), nil
	}), 2)
	ready := make(chan struct{})
	callbacks := 0
	executor.SetOnStepComplete(func(int, int, RoutingStep, StepResult) {
		callbacks++
		close(ready)
	})
	checkpoint := &ExecutionCheckpoint{CheckpointID: "cancelled-approval"}
	executor.interruptController = &cancellationHITLController{
		mockInterruptController: newMockInterruptController(), cancel: cancel, ready: ready, point: checkpoint,
	}
	ctx = withCheckpointEnrichmentRequired(ctx)
	result, err := executor.Execute(ctx, plan)
	if result != nil || !IsInterrupted(err) || GetCheckpoint(err) != checkpoint || callbacks != 1 {
		t.Fatalf("HITL precedence changed: result=%+v err=%v callbacks=%d", result, err, callbacks)
	}
	if len(checkpoint.CompletedSteps) != 1 || checkpoint.CompletedSteps[0].StepID != "step-1" ||
		!checkpoint.CompletedSteps[0].Success || len(checkpoint.CompletedSteps[0].Dispatches) != 1 {
		t.Fatalf("successful sibling missing from checkpoint: %+v", checkpoint.CompletedSteps)
	}
}

func TestCanceledExecutionSnapshotOwnsFrameworkContainers(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	parameter := map[string]interface{}{"nested": []interface{}{map[string]interface{}{"value": json.Number("9007199254740993")}}}
	resolution := &ResolutionMetadata{
		Parameters:        []ParameterResolution{{Name: "input", Layer: "auto_wire", Value: map[string]interface{}{"value": json.Number("9007199254740993")}}},
		DependencyStepIDs: []string{"step-original"},
	}
	includes := []templateAutoInclude{{ReferencedStep: "step-original", Template: "{{step-original.value}}", Reason: "declared_prior_phase"}}
	opaque := &struct{ Value string }{Value: "application-owned"}
	source := &ExecutionResult{
		PlanID: "plan", Success: true, Metadata: map[string]interface{}{"labels": map[string]string{"label": "source"}},
		Steps: []StepResult{{
			StepID: "step", Success: true, Parameters: parameter,
			Metadata: map[string]interface{}{
				"typed": []string{"source"}, "resolution": resolution,
				"template_auto_includes": includes, "application_extension": opaque,
			},
			ToolError:  json.RawMessage(`{"code":"EXACT"}`),
			Dispatches: []StepDispatch{{Parameters: parameter, Response: "a<b *pointer", Success: true}},
		}},
	}
	snapshot := canceledExecutionSnapshot(ctx, source, time.Now().Add(-time.Second))
	source.Metadata["labels"].(map[string]string)["label"] = "changed"
	source.Steps[0].Metadata["typed"].([]string)[0] = "changed"
	source.Steps[0].Parameters["nested"].([]interface{})[0].(map[string]interface{})["value"] = "changed"
	source.Steps[0].ToolError[0] = '['
	source.Steps[0].Dispatches[0].Response = "changed"
	resolution.Parameters[0].Name = "changed"
	resolution.Parameters[0].Value.(map[string]interface{})["value"] = "changed"
	resolution.DependencyStepIDs[0] = "changed"
	includes[0].ReferencedStep = "changed"
	if snapshot.Metadata["labels"].(map[string]string)["label"] != "source" ||
		snapshot.Steps[0].Metadata["typed"].([]string)[0] != "source" ||
		snapshot.Steps[0].Parameters["nested"].([]interface{})[0].(map[string]interface{})["value"] != json.Number("9007199254740993") ||
		snapshot.Steps[0].Dispatches[0].Parameters["nested"].([]interface{})[0].(map[string]interface{})["value"] != json.Number("9007199254740993") ||
		string(snapshot.Steps[0].ToolError) != `{"code":"EXACT"}` || snapshot.Steps[0].Dispatches[0].Response != "a<b *pointer" {
		t.Fatal("cancellation snapshot changed payloads or retained mutable framework containers")
	}
	copiedResolution := snapshot.Steps[0].Metadata["resolution"].(*ResolutionMetadata)
	copiedIncludes := snapshot.Steps[0].Metadata["template_auto_includes"].([]templateAutoInclude)
	if copiedResolution == resolution || copiedResolution.Parameters[0].Name != "input" ||
		copiedResolution.Parameters[0].Value.(map[string]interface{})["value"] != json.Number("9007199254740993") ||
		copiedResolution.DependencyStepIDs[0] != "step-original" || copiedIncludes[0].ReferencedStep != "step-original" {
		t.Fatal("cancellation snapshot retained executor-owned typed metadata containers")
	}
	if snapshot.Steps[0].Metadata["application_extension"] != opaque {
		t.Fatal("cancellation snapshot changed opaque application ownership")
	}
}

func TestCancellationStepSnapshotPreservesEmptyTypedMetadata(t *testing.T) {
	for _, populated := range []bool{false, true} {
		resolution := &ResolutionMetadata{}
		var includes []templateAutoInclude
		if populated {
			resolution.Parameters = []ParameterResolution{}
			resolution.DependencyStepIDs = []string{}
			includes = []templateAutoInclude{}
		}
		step := StepResult{Metadata: map[string]interface{}{"resolution": resolution, "template_auto_includes": includes}}
		copied := cloneExecutionStepResult(step)
		copiedResolution := copied.Metadata["resolution"].(*ResolutionMetadata)
		copiedIncludes := copied.Metadata["template_auto_includes"].([]templateAutoInclude)
		if (copiedResolution.Parameters != nil) != populated || (copiedResolution.DependencyStepIDs != nil) != populated || (copiedIncludes != nil) != populated {
			t.Fatalf("typed metadata nil/empty shape changed: populated=%t resolution=%+v includes=%#v", populated, copiedResolution, copiedIncludes)
		}
	}
}

func TestCancellationAndCheckpointSnapshotsPreserveContainerShapes(t *testing.T) {
	payload := map[string]interface{}{
		"nil_interfaces":   []interface{}(nil),
		"empty_interfaces": []interface{}{},
		"interfaces":       []interface{}{map[string]interface{}{"value": "source"}},
		"nil_strings":      []string(nil),
		"empty_strings":    []string{},
		"strings":          []string{"source"},
		"nil_ints":         []int(nil),
		"empty_ints":       []int{},
		"ints":             []int{1},
		"nil_floats":       []float64(nil),
		"empty_floats":     []float64{},
		"floats":           []float64{1.5},
		"nil_bools":        []bool(nil),
		"empty_bools":      []bool{},
		"bools":            []bool{true},
	}
	resolution := &ResolutionMetadata{Parameters: []ParameterResolution{{Name: "payload", Value: payload}}}
	source := &ExecutionResult{
		Success:  true,
		Metadata: map[string]interface{}{"payload": payload},
		Steps: []StepResult{{
			StepID:     "step-1",
			Parameters: map[string]interface{}{"payload": payload},
			Metadata:   map[string]interface{}{"payload": payload, "resolution": resolution},
			Dispatches: []StepDispatch{{Parameters: map[string]interface{}{"payload": payload}}},
		}},
	}
	wantJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceled := canceledExecutionSnapshot(ctx, source, time.Now().Add(-time.Second))
	checkpoint := newExecutionRunSnapshot(1, map[string]*StepResult{"step-1": &source.Steps[0]}, nil, "")
	copiedResolution := canceled.Steps[0].Metadata["resolution"].(*ResolutionMetadata)
	containers := map[string]interface{}{
		"execution metadata":    canceled.Metadata["payload"],
		"step parameters":       canceled.Steps[0].Parameters["payload"],
		"step metadata":         canceled.Steps[0].Metadata["payload"],
		"dispatch parameters":   canceled.Steps[0].Dispatches[0].Parameters["payload"],
		"resolution value":      copiedResolution.Parameters[0].Value,
		"checkpoint parameters": checkpoint.AccumulatedResults["step-1"].Parameters["payload"],
		"checkpoint metadata":   checkpoint.AccumulatedResults["step-1"].Metadata["payload"],
	}
	for name, cloned := range containers {
		if !reflect.DeepEqual(cloned, payload) {
			t.Errorf("%s changed typed nil/empty container shapes: got %#v, want %#v", name, cloned, payload)
		}
	}

	payload["interfaces"].([]interface{})[0].(map[string]interface{})["value"] = "changed"
	payload["strings"].([]string)[0] = "changed"
	payload["ints"].([]int)[0] = 2
	payload["floats"].([]float64)[0] = 2.5
	payload["bools"].([]bool)[0] = false
	payload["added"] = true
	for name, cloned := range containers {
		gotJSON, marshalErr := json.Marshal(cloned)
		if marshalErr != nil {
			t.Fatalf("marshal %s: %v", name, marshalErr)
		}
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s changed JSON payload shape or retained source containers: got %s, want %s", name, gotJSON, wantJSON)
		}
	}
}
