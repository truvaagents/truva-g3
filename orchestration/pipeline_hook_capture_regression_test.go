package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestInvalidBeforePlanningDecisionStoresFailedTerminalResult(t *testing.T) {
	for _, mode := range []string{"buffered", "native", "simulated"} {
		for _, missing := range []bool{true, false} {
			for _, recording := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/missing_%t/recording_%t", mode, missing, recording), func(t *testing.T) {
					o, client, transport, store, debug := newRequiredHookRecordingFixture(t, nil)
					client.native = mode == "native"
					if !recording {
						o.executionStore, o.debugStore = nil, nil
					}
					decision := &core.PipelineShortCircuitDecision{Kind: "unsupported", ShortCircuit: &core.PipelineShortCircuit{Response: "must not escape"}}
					wantErr, reason := ErrUnknownPipelineShortCircuitKind, "unknown_kind"
					if missing {
						decision = &core.PipelineShortCircuitDecision{Kind: core.PipelineShortCircuitAuthoritative}
						wantErr, reason = ErrInvalidPipelineShortCircuitDecision, "missing_payload"
					}
					post := &terminalHook{}
					o.pipelineHooks = []core.PipelineHook{&decisionHook{name: "contract", decision: func(core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
						return decision, nil
					}}, post}
					var err error
					if mode == "buffered" {
						_, err = o.ProcessRequest(t.Context(), "request", nil)
					} else {
						_, err = o.ProcessRequestStreaming(t.Context(), "request", nil, func(core.StreamChunk) error {
							t.Error("invalid decision delivered a response chunk")
							return nil
						})
					}
					shutdownRequiredHookFixture(t, o)
					if !errors.Is(err, wantErr) || client.callCount != 0 || client.streamCalls != 0 || transport.GetCallCount() != 0 || post.Finished() || post.AfterExecutionDone() {
						t.Fatalf("contract failure crossed protected boundary: err=%v client=%+v tools=%d", err, client, transport.GetCallCount())
					}
					records, _ := store.snapshot()
					if !recording {
						if len(records) != 0 {
							t.Fatal("disabled store received records")
						}
						return
					}
					if len(records) != 1 {
						t.Fatalf("terminal records = %d, want 1", len(records))
					}
					last := records[0]
					if last.Result == nil || last.Result.Success || last.Result.TotalDuration <= 0 || len(last.Result.Steps) != 0 || last.Plan != nil || last.Interrupted || last.FinalResponse != nil {
						t.Fatalf("untruthful failed terminal state: %+v", last)
					}
					if len(last.PipelineHooks) != 1 {
						t.Fatalf("hook records = %+v", last.PipelineHooks)
					}
					assertPipelineHookDecision(t, last.PipelineHooks[0].Decision, PipelineHookDecision{PipelineHookFailClosed, PipelineHookTerminate, reason})
					if debug.ttl(last.RequestID) != o.config.ExecutionStore.ErrorTTL {
						t.Fatal("LLM floor selected normal retention")
					}
					assertRequiredHookStoreRetention(t, last, o.config.ExecutionStore, o.config.ExecutionStore.ErrorTTL)
				})
			}
		}
	}
}

func TestLaterPlanningFailurePreservesSuccessfulHookAndPriorWork(t *testing.T) {
	for _, mode := range []string{"buffered", "native", "simulated"} {
		for _, failureKind := range []string{"generation", "validation", "regeneration"} {
			t.Run(mode+"/"+failureKind, func(t *testing.T) {
				invalid := strings.ReplaceAll(foundationNonTerminalSingleStepPlan("bad-phase", "step-2"), "test_capability", "unregistered_capability")
				if failureKind == "regeneration" {
					// This template reaches the phase-loop validation gauntlet,
					// rather than failing inside the continuation planner.
					invalid = strings.Replace(foundationNonTerminalSingleStepPlan("bad-phase", "step-2"),
						`"parameters":{}`, `"parameters":{"input":"{{step-1}}"}`, 1)
				}
				o, client, transport, store, debug := newRequiredHookRecordingFixture(t, []string{foundationNonTerminalSingleStepPlan("phase-1", "step-1"), invalid})
				client.native = mode == "native"
				regenerationErr := errors.New("regeneration provider unavailable")
				if failureKind == "regeneration" {
					client.errors = []error{nil, nil, regenerationErr}
				}
				if failureKind == "generation" {
					client.errors = make([]error, 32)
					for i := 1; i < len(client.errors); i++ {
						client.errors[i] = errors.New("planner unavailable")
					}
				}
				post := &terminalHook{}
				o.pipelineHooks = []core.PipelineHook{&afterPlanningContractHook{name: "observer"}, post}
				var err error
				if mode == "buffered" {
					_, err = o.ProcessRequest(t.Context(), "request", nil)
				} else {
					_, err = o.ProcessRequestStreaming(t.Context(), "request", nil, func(chunk core.StreamChunk) error {
						if chunk.Metadata["type"] != "phase_complete" {
							t.Error("failed planner emitted final content")
						}
						return nil
					})
				}
				shutdownRequiredHookFixture(t, o)
				if err == nil || !strings.Contains(err.Error(), "phase 2") || transport.GetCallCount() != 1 || post.Finished() || post.AfterExecutionDone() || client.streamCalls != 0 {
					t.Fatalf("unexpected failure boundary: err=%v tools=%d", err, transport.GetCallCount())
				}
				if failureKind == "validation" && !strings.Contains(err.Error(), "plan failed validation after") {
					t.Fatalf("test did not exercise validation exhaustion: %v", err)
				}
				if failureKind == "regeneration" && (!errors.Is(err, regenerationErr) ||
					!strings.Contains(err.Error(), "failed to generate valid plan") || client.callCount != 3) {
					t.Fatalf("test did not preserve the regeneration failure: err=%v calls=%d", err, client.callCount)
				}
				records, _ := store.snapshot()
				if len(records) < 2 {
					t.Fatalf("missing terminal snapshot: %d", len(records))
				}
				last := records[len(records)-1]
				if last.Result == nil || last.Result.Success || last.Result.TotalDuration <= 0 || last.Plan == nil || last.Plan.PlanID != "phase-1" || last.PhaseCount != 1 || len(last.PhasePlans) != 1 || last.FinalResponse != nil {
					t.Fatalf("terminal failure lost admitted state: %+v", last)
				}
				if !reflect.DeepEqual(records[0].Result.Steps, last.Result.Steps) || len(last.PipelineHooks) != 1 || last.PipelineHooks[0].Status != PipelineHookSucceeded {
					t.Fatalf("earlier successful work changed: %+v", last)
				}
				assertPipelineHookDecision(t, last.PipelineHooks[0].Decision, PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "accepted"})
				if debug.ttl(last.RequestID) != o.config.ExecutionStore.ErrorTTL {
					t.Fatal("failure selected normal retention")
				}
				assertRequiredHookStoreRetention(t, last, o.config.ExecutionStore, o.config.ExecutionStore.ErrorTTL)
			})
		}
	}
}

func TestResumeBoundaryFailureReturnsPriorWorkWithoutAdmittingPlan(t *testing.T) {
	o, client, transport, _, _ := newRequiredHookRecordingFixture(t, nil)
	o.config.IterativePlanning.PhaseTimeout = time.Minute
	spans := &pipelineHookSpanTelemetry{}
	o.telemetry = spans
	hook := &requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "must-not-run"}}
	o.pipelineHooks = []core.PipelineHook{hook}
	o.config.HITL.Enabled = true
	controller := &requiredBoundaryInterruptController{}
	o.SetInterruptController(controller)
	plan := validAfterPlanningContractPlan()
	plan.PhaseNumber = 5
	prior := StepResult{StepID: "step-4", Success: true, Response: "previously completed work"}
	ctx := WithPlanOverride(t.Context(), plan)
	ctx = WithCompletedSteps(ctx, map[string]*StepResult{prior.StepID: &prior})
	failure := errors.New("resume boundary unavailable")
	var boundaryCtx context.Context
	var boundaries []orchestrationBoundary
	ctx = withBoundaryPreparer(ctx, func(ctx context.Context, boundary orchestrationBoundary) (context.Context, error) {
		boundaryCtx = ctx
		boundaries = append(boundaries, boundary)
		return ctx, failure
	})

	result, err := o.executePhaseLoop(ctx, "request", "resume-request", time.Now(),
		&core.NoOpSpan{}, &core.PipelineContext{}, nil, nil)
	var boundaryErr *boundaryPreparationError
	if !errors.Is(err, failure) || !errors.As(err, &boundaryErr) || boundaryErr.boundary != boundaryResume ||
		!reflect.DeepEqual(boundaries, []orchestrationBoundary{boundaryResume}) {
		t.Fatalf("wrong failure boundary: err=%v boundaries=%v", err, boundaries)
	}
	if result == nil || result.CombinedResult == nil || result.CombinedResult.Success || result.CombinedResult.TotalDuration <= 0 {
		t.Fatalf("missing failed partial result: %+v", result)
	}
	if !reflect.DeepEqual(result.CombinedResult.Steps, []StepResult{prior}) ||
		result.LastPlan != nil || len(result.PhasePlans) != 0 || result.CombinedResult.PlanID != "" {
		t.Fatalf("lost prior work or admitted the unprepared resume plan: %+v", result)
	}
	if hook.calls != 0 || controller.planApprovalCalls != 0 || controller.beforeStepCalls != 0 ||
		client.callCount != 0 || client.streamCalls != 0 || transport.GetCallCount() != 0 {
		t.Fatal("resume preparation failure reached a hook, HITL, AI, or tool")
	}
	if boundaryCtx == nil || !errors.Is(boundaryCtx.Err(), context.Canceled) {
		t.Fatal("failed phase did not cancel its context")
	}
	if len(spans.spans) != 1 || spans.spans[0].endCount != 1 || len(spans.spans[0].errors) != 1 ||
		!errors.Is(spans.spans[0].errors[0], failure) {
		t.Fatalf("failed phase span was not recorded and closed once: %+v", spans.spans)
	}
}

func TestLaterPlannerFailurePreservesResumePhaseNumber(t *testing.T) {
	o, client, transport, store, _ := newRequiredHookRecordingFixture(t, nil)
	o.config.IterativePlanning.MaxPhases = 8
	client.errors = make([]error, 32)
	for i := range client.errors {
		client.errors[i] = errors.New("planner unavailable")
	}
	var override RoutingPlan
	if err := json.Unmarshal([]byte(foundationNonTerminalSingleStepPlan("resumed", "step-5")), &override); err != nil {
		t.Fatal(err)
	}
	override.PhaseNumber = 5
	ctx := WithPlanOverride(t.Context(), &override)
	ctx = WithCompletedSteps(ctx, map[string]*StepResult{"step-4": {StepID: "step-4", Success: true, Metadata: map[string]interface{}{"phase_number": 4}}})
	o.pipelineHooks = []core.PipelineHook{&allStagesHook{name: "observer"}}
	_, err := o.ProcessRequest(ctx, "request", nil)
	shutdownRequiredHookFixture(t, o)
	if err == nil || !strings.Contains(err.Error(), "phase 6") || transport.GetCallCount() != 1 {
		t.Fatalf("unexpected resume boundary: %v", err)
	}
	records, _ := store.snapshot()
	last := records[len(records)-1]
	if last.PhaseCount != 5 || last.Result == nil || last.Result.Success || len(last.Result.Steps) != 2 || last.Plan.PlanID != "resumed" || len(last.PhasePlans) != 1 {
		t.Fatalf("resume numbering or prior work lost: %+v", last)
	}
}

func TestLaterBoundaryFailurePreservesPriorWorkInExistingPublisher(t *testing.T) {
	o, client, transport, store, _ := newRequiredHookRecordingFixture(t, []string{foundationNonTerminalSingleStepPlan("phase-1", "step-1")})
	client.native = true
	o.pipelineHooks = []core.PipelineHook{&afterPlanningContractHook{name: "observer"}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := o.ProcessRequestStreaming(ctx, "request", nil, func(chunk core.StreamChunk) error {
		if chunk.Metadata["type"] == "phase_complete" {
			cancel() // The first phase completed; the next boundary cannot prepare.
		}
		return nil
	})
	shutdownRequiredHookFixture(t, o)
	if !errors.Is(err, context.Canceled) || transport.GetCallCount() != 1 || client.callCount != 1 {
		t.Fatalf("boundary failure retried or lost prior execution: %v", err)
	}
	records, _ := store.snapshot()
	if len(records) != 2 {
		t.Fatalf("expected intermediate plus existing terminal publisher, got %d", len(records))
	}
	last := records[1]
	if last.Result == nil || last.Result.Success || len(last.Result.Steps) != 1 || !last.Result.Steps[0].Success || last.Plan == nil || last.Plan.PlanID != "phase-1" || last.PhaseCount != 1 || len(last.PipelineHooks) != 1 {
		t.Fatalf("boundary publisher erased completed work: %+v", last)
	}
	assertRequiredHookStoreRetention(t, last, o.config.ExecutionStore, o.config.ExecutionStore.ErrorTTL)
}

type hookCaptureCheckpointController struct {
	*mockInterruptController
	failCheck bool
}

func (c *hookCaptureCheckpointController) CheckPlanApproval(_ context.Context, plan *RoutingPlan) (*ExecutionCheckpoint, error) {
	if plan.PhaseNumber != 2 {
		return nil, nil
	}
	if c.failCheck {
		return nil, errors.New("checkpoint check unavailable")
	}
	return &ExecutionCheckpoint{CheckpointID: "save-failed", Status: CheckpointStatusPreparing}, nil
}

func (*hookCaptureCheckpointController) SaveEnrichedCheckpoint(context.Context, *ExecutionCheckpoint) error {
	return errors.New("checkpoint store unavailable")
}

func TestCheckpointFailureAfterHookPreservesPriorWork(t *testing.T) {
	for _, failCheck := range []bool{true, false} {
		t.Run(fmt.Sprint(failCheck), func(t *testing.T) {
			o, _, transport, store, _ := newRequiredHookRecordingFixture(t, []string{
				foundationNonTerminalSingleStepPlan("phase-1", "step-1"), foundationNonTerminalSingleStepPlan("phase-2", "step-2"),
			})
			o.pipelineHooks = []core.PipelineHook{&afterPlanningContractHook{name: "observer"}}
			o.config.HITL.Enabled = true
			o.SetInterruptController(&hookCaptureCheckpointController{mockInterruptController: newMockInterruptController(), failCheck: failCheck})
			_, err := o.ProcessRequest(t.Context(), "request", nil)
			shutdownRequiredHookFixture(t, o)
			if err == nil || IsInterrupted(err) || transport.GetCallCount() != 1 {
				t.Fatalf("wrong failure boundary: %v", err)
			}
			if !failCheck && !errors.Is(err, ErrCheckpointAuthoritativeSave) {
				t.Fatalf("unexpected save error: %v", err)
			}
			records, _ := store.snapshot()
			last := records[len(records)-1]
			if last.Interrupted || last.Checkpoint != nil || last.Result == nil || last.Result.Success || last.PhaseCount != 2 || len(last.Result.Steps) != 1 || !last.Result.Steps[0].Success || len(last.PipelineHooks) != 2 {
				t.Fatalf("failed checkpoint lost work or became a suspension: %+v", last)
			}
			assertRequiredHookStoreRetention(t, last, o.config.ExecutionStore, o.config.ExecutionStore.ErrorTTL)
		})
	}
}

type hookCaptureStepCheckpointController struct {
	*mockInterruptController
	checkpoint *ExecutionCheckpoint
	saveCalls  int
}

func (c *hookCaptureStepCheckpointController) CheckBeforeStep(_ context.Context, step RoutingStep, plan *RoutingPlan) (*ExecutionCheckpoint, error) {
	if step.StepID != "step-3" {
		return nil, nil
	}
	c.checkpoint = &ExecutionCheckpoint{
		CheckpointID: "step-save-failed", Status: CheckpointStatusPreparing,
		InterruptPoint: InterruptPointBeforeStep, CurrentStep: &step, Plan: plan,
	}
	return c.checkpoint, nil
}

func (c *hookCaptureStepCheckpointController) SaveEnrichedCheckpoint(context.Context, *ExecutionCheckpoint) error {
	c.saveCalls++
	return errors.New("checkpoint store unavailable")
}

func TestStepCheckpointSaveFailurePreservesPriorAndCurrentPhaseWork(t *testing.T) {
	var plan RoutingPlan
	if err := json.Unmarshal([]byte(foundationNonTerminalSingleStepPlan("phase-2", "step-2")), &plan); err != nil {
		t.Fatal(err)
	}
	interruptedStep := plan.Steps[0]
	interruptedStep.StepID = "step-3"
	interruptedStep.DependsOn = []string{"step-2"}
	plan.Steps = append(plan.Steps, interruptedStep)
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	o, client, transport, store, debug := newRequiredHookRecordingFixture(t, []string{
		foundationNonTerminalSingleStepPlan("phase-1", "step-1"), string(encoded),
	})
	post := &terminalHook{}
	o.pipelineHooks = []core.PipelineHook{&afterPlanningContractHook{name: "observer"}, post}
	o.config.HITL.Enabled = true
	controller := &hookCaptureStepCheckpointController{mockInterruptController: newMockInterruptController()}
	o.SetInterruptController(controller)

	response, err := o.ProcessRequest(t.Context(), "request", nil)
	shutdownRequiredHookFixture(t, o)
	if response != nil || !errors.Is(err, ErrCheckpointAuthoritativeSave) || IsInterrupted(err) ||
		controller.saveCalls != 1 || transport.GetCallCount() != 2 || client.callCount != 2 ||
		post.AfterExecutionDone() || post.Finished() {
		t.Fatalf("wrong step-save failure boundary: response=%v err=%v saves=%d tools=%d AI=%d", response, err,
			controller.saveCalls, transport.GetCallCount(), client.callCount)
	}
	records, _ := store.snapshot()
	if len(records) != 2 {
		t.Fatalf("expected intermediate and terminal records, got %d", len(records))
	}
	if records[0].Result == nil || len(records[0].Result.Steps) != 1 {
		t.Fatalf("missing first phase's completed step: %+v", records[0])
	}
	last := records[1]
	if last.Result == nil || last.Result.Success || last.Result.TotalDuration <= 0 ||
		last.Interrupted || last.Checkpoint != nil || last.FinalResponse != nil ||
		last.Plan == nil || last.Plan.PlanID != "phase-2" || last.PhaseCount != 2 || len(last.PhasePlans) != 2 {
		t.Fatalf("failed save became success/suspension or lost admitted phases: %+v", last)
	}
	if len(last.Result.Steps) != 2 || !reflect.DeepEqual(last.Result.Steps[0], records[0].Result.Steps[0]) ||
		last.Result.Steps[1].StepID != "step-2" || !last.Result.Steps[1].Success || last.Result.Steps[1].Response == "" {
		t.Fatalf("prior/current completed steps lost or interrupted step admitted: %+v", last.Result.Steps)
	}
	if len(last.PipelineHooks) != 2 {
		t.Fatalf("lost accepted hook evidence: %+v", last.PipelineHooks)
	}
	for _, hook := range last.PipelineHooks {
		assertPipelineHookDecision(t, hook.Decision, PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "accepted"})
	}
	if controller.checkpoint == nil || controller.checkpoint.Status != CheckpointStatusPreparing ||
		debug.ttl(last.RequestID) != o.config.ExecutionStore.ErrorTTL {
		t.Fatal("failed save changed checkpoint status or selected success retention")
	}
}

type countingHookJSON struct{ calls *int }

func (v countingHookJSON) MarshalJSON() ([]byte, error) {
	*v.calls++
	return []byte(`"application-value"`), nil
}

func TestDisabledHookCaptureDoesNotMarshalEnrichments(t *testing.T) {
	calls := 0
	o := &AIOrchestrator{pipelineHooks: []core.PipelineHook{&allStagesHook{name: "capture-test"}}}
	pctx := &core.PipelineContext{Enrichments: map[string]interface{}{"application": countingHookJSON{&calls}}}
	_, err := o.runBeforePlanningHooks(t.Context(), pctx, newPipelineGate(nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("disabled hook capture invoked application MarshalJSON %d times", calls)
	}
}

type captureEffectReporter struct{ effects []core.PipelineHookEffect }

func (r *captureEffectReporter) ReportPipelineHookEffect(effect core.PipelineHookEffect) error {
	r.effects = append(r.effects, effect)
	return nil
}

func TestStructuredHookEffectRequiresReporterBeforeSerialization(t *testing.T) {
	calls := 0
	value := countingHookJSON{&calls}
	reportStructuredPipelineHookEffect(t.Context(), "effect", "effect", core.PipelineHookEffectSucceeded, "", value, nil, time.Now())
	if calls != 0 {
		t.Fatalf("absent reporter invoked MarshalJSON %d times", calls)
	}
	reporter := &captureEffectReporter{}
	ctx := core.WithPipelineHookEffectReporter(t.Context(), reporter)
	reportStructuredPipelineHookEffect(ctx, "effect", "effect", core.PipelineHookEffectSucceeded, "", value, nil, time.Now())
	if calls != 1 || len(reporter.effects) != 1 || string(reporter.effects[0].Data) != `"application-value"` {
		t.Fatalf("enabled capture lost exact evidence: calls=%d effects=%+v", calls, reporter.effects)
	}
}
