package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type cancellationDeliveryAI struct {
	promptCapturingAIClient
	streamCalls int
}

func (*cancellationDeliveryAI) SupportsStreaming() bool { return true }

func (client *cancellationDeliveryAI) StreamResponse(context.Context, string, *core.AIOptions, core.StreamCallback) (*core.AIResponse, error) {
	client.streamCalls++
	return nil, errors.New("synthesis must not run after execution cancellation")
}

func TestCancellationHandoffManagedDeliveryPreservesObservedResults(t *testing.T) {
	for _, mode := range []string{"buffered", "native_stream", "cross_phase"} {
		t.Run(mode, func(t *testing.T) {
			plans := []string{foundationTerminalSingleStepPlan}
			cancelStep, wantSteps := "step-1", 1
			if mode == "cross_phase" {
				plans = []string{foundationNonTerminalSingleStepPlan("first", "step-1"), strings.ReplaceAll(foundationTerminalSingleStepPlan, "step-1", "step-2")}
				cancelStep, wantSteps = "step-2", 2
			}
			client := &cancellationDeliveryAI{promptCapturingAIClient: promptCapturingAIClient{responses: plans}}
			o := setupTestOrchestrator(t, client)
			if err := o.discovery.Register(t.Context(), &core.ServiceRegistration{ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080, Type: core.ComponentTypeTool}); err != nil {
				t.Fatal(err)
			}
			o.catalog.agents = map[string]*AgentInfo{
				"test-agent": {
					Registration: &core.ServiceRegistration{ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080},
					Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process"}},
				},
			}
			o.executor.httpClient = &http.Client{Transport: cancellationTestTransport(func(*http.Request) (*http.Response, error) {
				return successfulCancellationResponse(), nil
			})}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := errors.New("caller stopped after observed reply <exact>")
			var callbacks atomic.Int32
			o.executor.SetOnStepComplete(func(_ int, _ int, _ RoutingStep, result StepResult) {
				callbacks.Add(1)
				if result.StepID == cancelStep {
					cancel(cause)
				}
			})
			hook := &terminalHook{}
			o.pipelineHooks = []core.PipelineHook{hook}
			store := &terminalRecordStore{NoOpExecutionStore: NewNoOpExecutionStore()}
			o.executionStore = store

			var execution *ExecutionResult
			var runErr error
			if mode == "native_stream" {
				response, observed, err := o.ProcessRequestStreamingWithExecution(ctx, "request", nil, func(core.StreamChunk) error { return nil })
				if response != nil {
					t.Fatalf("cancelled execution synthesized a response: %+v", response)
				}
				execution, runErr = observed, err
			} else {
				response, observed, err := o.ProcessRequestWithExecution(ctx, "request", nil)
				if response != nil {
					t.Fatalf("cancelled execution synthesized a response: %+v", response)
				}
				execution, runErr = observed, err
			}
			if !errors.Is(runErr, context.Canceled) || execution == nil || execution.Success || len(execution.Steps) != wantSteps {
				t.Fatalf("terminal result lost partial evidence: result=%+v error=%v", execution, runErr)
			}
			if execution.Metadata["context_cause"] != cause.Error() || execution.Metadata["termination_reason"] != "cancelled" {
				t.Fatalf("terminal cause lost during phase merge: %+v", execution.Metadata)
			}
			if callbacks.Load() != int32(wantSteps) || hook.AfterExecutionDone() || hook.Finished() || client.streamCalls != 0 || client.callCount != len(plans) {
				t.Fatalf("unexpected retry, hook or synthesis: callbacks=%d after=%v synthesis=%v calls=%d streams=%d", callbacks.Load(), hook.AfterExecutionDone(), hook.Finished(), client.callCount, client.streamCalls)
			}
			shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := o.Shutdown(shutdownCtx); err != nil {
				t.Fatal(err)
			}
			records, _ := store.snapshot()
			if len(records) == 0 {
				t.Fatal("no partial execution was persisted")
			}
			last := records[len(records)-1]
			if last.Lifecycle == nil || last.Lifecycle.State != RequestLifecycleCancelled || last.Lifecycle.Revision != 2 || last.Result == nil || last.Result.Success {
				t.Fatalf("terminal lifecycle/result changed: %+v", last)
			}
			if last.Result.TotalDuration != execution.TotalDuration || len(last.Result.Steps) != wantSteps || last.Result.Metadata["context_cause"] != cause.Error() {
				t.Fatalf("returned and persisted evidence diverged: returned=%+v stored=%+v", execution, last.Result)
			}
			seen := make(map[string]bool)
			for _, step := range last.Result.Steps {
				if seen[step.StepID] || !step.Success || step.DispatchCount != 1 || len(step.Dispatches) != 1 || !step.Dispatches[0].Success {
					t.Fatalf("observed successful step was duplicated or changed: %+v", step)
				}
				seen[step.StepID] = true
			}
		})
	}
}

func TestCancellationHandoffDirectWorkflowUsesErrorRetention(t *testing.T) {
	o, aiClient := createTestOrchestrator(t)
	o.catalog.agents["test-1"].Capabilities[0].Endpoint = "/process"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	o.executor.httpClient = &http.Client{Transport: cancellationTestTransport(func(*http.Request) (*http.Response, error) {
		return successfulCancellationResponse(), nil
	})}
	o.executor.SetOnStepComplete(func(int, int, RoutingStep, StepResult) { cancel() })
	store := &terminalRecordStore{NoOpExecutionStore: NewNoOpExecutionStore()}
	o.executionStore = store
	plan := &RoutingPlan{PlanID: "direct-cancel", Steps: []RoutingStep{{StepID: "step-1", AgentName: "test-agent", Metadata: map[string]interface{}{"capability": "test_capability"}}}}
	response, err := o.ExecutePlanWithSynthesis(ctx, plan, "request")
	if response != nil || !errors.Is(err, context.Canceled) || len(aiClient.calls) != 0 {
		t.Fatalf("direct workflow error/synthesis changed: response=%+v error=%v calls=%d", response, err, len(aiClient.calls))
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := o.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	records, _ := store.snapshot()
	if len(records) != 1 || records[0].Result == nil {
		t.Fatalf("direct cancellation evidence = %+v", records)
	}
	record := records[0]
	if record.Lifecycle != nil || record.Result.Success || record.Result.TotalDuration <= 0 || len(record.Result.Steps) != 1 || !record.Result.Steps[0].Success {
		t.Fatalf("direct API misclassified observed work: record=%+v result=%+v steps=%+v", record, record.Result, record.Result.Steps)
	}
	if ttl := executionRetentionTTLAt(record, time.Minute, time.Hour, time.Now()); ttl != time.Hour {
		t.Fatalf("direct cancellation selected %v, want error retention", ttl)
	}
}

func TestCancellationHandoffDeduplicatesRestoredSteps(t *testing.T) {
	prior := StepResult{StepID: "step-1", Success: true, Response: "original", Metadata: map[string]interface{}{"phase_number": 1}}
	restored := StepResult{StepID: "step-1", Success: true, Response: "restored"}
	current := StepResult{StepID: "step-2", Success: true, Response: "new"}
	result := buildNonSuccessResult([]StepResult{restored, current, current}, nil, 2, false, []StepResult{prior}, "phase-two", false)
	if len(result.Steps) != 2 || result.Steps[0].Response != "original" || result.Steps[0].Metadata["phase_number"] != 1 || result.Steps[1].Metadata["phase_number"] != 2 {
		t.Fatalf("restored step duplication or attribution change: %+v", result.Steps)
	}
	if current.Metadata != nil || restored.Metadata != nil || prior.Metadata["phase_number"] != 1 {
		t.Fatal("failure merge mutated caller-owned phase metadata")
	}
}

func TestCancellationHandoffManagedHITLResumePreservesEvidenceOnce(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		mode := "buffered"
		if streaming {
			mode = "native_stream"
		}
		t.Run(mode, func(t *testing.T) {
			client := &cancellationDeliveryAI{}
			o := setupTestOrchestrator(t, client)
			if err := o.discovery.Register(t.Context(), &core.ServiceRegistration{ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080, Type: core.ComponentTypeTool}); err != nil {
				t.Fatal(err)
			}
			o.catalog.agents = map[string]*AgentInfo{
				"test-agent": {
					Registration: &core.ServiceRegistration{ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080},
					Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process", Parameters: []Parameter{{Name: "input", Type: "object", Required: true}}}},
				},
			}
			var dispatches atomic.Int32
			approvedText := "approved *exact* <value> 世界"
			o.executor.httpClient = &http.Client{Transport: cancellationTestTransport(func(request *http.Request) (*http.Response, error) {
				dispatches.Add(1)
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				var params map[string]interface{}
				if err := json.Unmarshal(body, &params); err != nil {
					t.Errorf("dispatch was not JSON: %v", err)
				} else if input, ok := params["input"].(map[string]interface{}); !ok || input["text"] != approvedText {
					t.Errorf("approved parameters changed before dispatch: %+v", params)
				}
				return successfulCancellationResponse(), nil
			})}
			plan := &RoutingPlan{
				PlanID: "resumed-phase-two", PhaseNumber: 2, Terminal: boolPtr(true),
				Steps: []RoutingStep{
					{StepID: "step-2", AgentName: "test-agent", Metadata: map[string]interface{}{"capability": "test_capability"}},
					{StepID: "step-3", AgentName: "test-agent", DependsOn: []string{"step-2"}, Metadata: map[string]interface{}{"capability": "test_capability", "parameters": map[string]interface{}{"input": "superseded by approved values"}}},
				},
			}
			prior := &StepResult{StepID: "step-1", AgentName: "test-agent", Success: true, Response: `{"success":true,"data":{"origin":"phase-one"}}`, Metadata: map[string]interface{}{"phase_number": 1}, DispatchCount: 1, Dispatches: []StepDispatch{{Number: 1, Success: true, Response: "phase-one raw reply"}}}
			restored := &StepResult{StepID: "step-2", AgentName: "test-agent", Success: true, Response: `{"success":true,"data":{"origin":"phase-two"}}`, Metadata: map[string]interface{}{"phase_number": 2}, DispatchCount: 1, Dispatches: []StepDispatch{{Number: 1, Success: true, Response: "phase-two raw reply"}}}
			checkpoint := &ExecutionCheckpoint{
				CheckpointID: "approved-before-step-three", RequestID: "suspended-request", OriginalRequestID: "original-request",
				Status: CheckpointStatusApproved, InterruptPoint: InterruptPointBeforeStep,
				Plan: plan, CurrentStep: &plan.Steps[1], PhaseNumber: 2,
				StepResults:        map[string]*StepResult{"step-1": prior, "step-2": restored},
				ResolvedParameters: map[string]interface{}{"input": map[string]interface{}{"text": approvedText}},
			}
			checkpoints := newMockCheckpointStore()
			if err := checkpoints.SaveCheckpoint(t.Context(), checkpoint); err != nil {
				t.Fatal(err)
			}
			// These policies would interrupt again if resume identity or the exact
			// approved-step bypass were lost anywhere along the managed path.
			policy := &mockPolicy{
				planDecision:       &InterruptDecision{ShouldInterrupt: true},
				beforeStepDecision: &InterruptDecision{ShouldInterrupt: true},
			}
			o.config.HITL.Enabled = true
			o.SetInterruptController(NewInterruptController(policy, checkpoints, nil))
			hook := &terminalHook{}
			o.pipelineHooks = []core.PipelineHook{hook}
			store := &terminalRecordStore{NoOpExecutionStore: NewNoOpExecutionStore()}
			o.executionStore = store

			callerCtx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			ctx, endResume, err := BuildResumeContext(callerCtx, checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer endResume()
			cause := errors.New("caller cancelled resumed execution after observed response")
			var callbacks atomic.Int32
			o.executor.SetOnStepComplete(func(_ int, _ int, _ RoutingStep, result StepResult) {
				callbacks.Add(1)
				if result.StepID != "step-3" || !result.Success {
					t.Errorf("cached work was repeated or new dispatch failed: %+v", result)
				}
				cancel(cause)
			})
			var execution *ExecutionResult
			if streaming {
				response, partial, runErr := o.ProcessRequestStreamingWithExecution(ctx, "resume the approved work", nil, func(core.StreamChunk) error { return nil })
				if response != nil || !errors.Is(runErr, context.Canceled) {
					t.Fatalf("resumed streaming cancellation response=%+v error=%v", response, runErr)
				}
				execution = partial
			} else {
				response, partial, runErr := o.ProcessRequestWithExecution(ctx, "resume the approved work", nil)
				if response != nil || !errors.Is(runErr, context.Canceled) {
					t.Fatalf("resumed cancellation response=%+v error=%v", response, runErr)
				}
				execution = partial
			}
			if execution == nil || execution.Success || len(execution.Steps) != 3 || execution.TotalDuration <= 0 || execution.PhaseCount != 2 {
				t.Fatalf("resumed terminal evidence=%+v", execution)
			}
			if dispatches.Load() != 1 || callbacks.Load() != 1 || policy.planCalls != 0 || policy.beforeStepCalls != 0 ||
				client.callCount != 0 || client.streamCalls != 0 || hook.AfterExecutionDone() || hook.Finished() {
				t.Fatalf("resume introduced duplicate dispatch/callback, approval, hook or synthesis: dispatches=%d callbacks=%d planChecks=%d stepChecks=%d calls=%d streams=%d", dispatches.Load(), callbacks.Load(), policy.planCalls, policy.beforeStepCalls, client.callCount, client.streamCalls)
			}
			byID := make(map[string]StepResult, len(execution.Steps))
			for _, step := range execution.Steps {
				if _, duplicate := byID[step.StepID]; duplicate || !step.Success || step.DispatchCount != 1 || len(step.Dispatches) != 1 || !step.Dispatches[0].Success {
					t.Fatalf("restored/new step duplicated or changed: %+v", step)
				}
				byID[step.StepID] = step
			}
			if byID["step-1"].Response != prior.Response || byID["step-2"].Response != restored.Response ||
				byID["step-1"].Metadata["phase_number"] != 1 || byID["step-2"].Metadata["phase_number"] != 2 || byID["step-3"].Metadata["phase_number"] != 2 {
				t.Fatalf("restored payload or phase attribution changed: %+v", byID)
			}
			stopCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := o.Shutdown(stopCtx); err != nil {
				t.Fatal(err)
			}
			records, _ := store.snapshot()
			terminalCount := 0
			for _, record := range records {
				if record.Lifecycle == nil || record.Lifecycle.Revision != 2 {
					continue
				}
				terminalCount++
				if record.Lifecycle.State != RequestLifecycleCancelled || record.Result == nil || record.Result.Success ||
					record.Result.TotalDuration != execution.TotalDuration || len(record.Result.Steps) != 3 || record.Result.Metadata["context_cause"] != cause.Error() ||
					record.Metadata["resume_checkpoint_id"] != checkpoint.CheckpointID || record.OriginalRequestID != checkpoint.OriginalRequestID {
					t.Fatalf("stored/returned resumed cancellation diverged: record=%+v result=%+v", record, record.Result)
				}
				if ttl := executionRetentionTTLAt(record, time.Minute, time.Hour, time.Now()); ttl != time.Hour {
					t.Fatalf("resumed cancellation retention=%v, want error TTL", ttl)
				}
			}
			if terminalCount != 1 {
				t.Fatalf("settled terminal publications=%d, want exactly one", terminalCount)
			}
		})
	}
}
