package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func assertPipelineHookDecision(t *testing.T, got *PipelineHookDecision, want PipelineHookDecision) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("decision = %+v, want %+v", got, want)
	}
}

func assertPipelineHookDecisionSpan(t *testing.T, span *pipelineHookSpan, want PipelineHookDecision) {
	t.Helper()
	if !span.ended || span.attributesAfterEnd {
		t.Fatal("hook span was not closed after setting decision attributes")
	}
	expected := map[string]interface{}{
		"pipeline.hook.failure_policy": string(want.FailurePolicy),
		"pipeline.hook.action":         string(want.Action),
		"pipeline.hook.reason":         want.Reason,
	}
	if !reflect.DeepEqual(span.attributes, expected) {
		t.Fatalf("hook span attributes = %+v, want only bounded decision fields %+v", span.attributes, expected)
	}
}

func TestBeforePlanningDecisionsMatchRuntimeAndTrace(t *testing.T) {
	failure := errors.New("exact callback failure")
	payload := &core.PipelineShortCircuit{Response: "exact early response"}
	tests := []struct {
		name             string
		decision         *core.PipelineShortCircuitDecision
		callbackError    error
		legacy, disabled bool
		want             PipelineHookDecision
		wantError        error
	}{
		{name: "callback error", callbackError: failure, want: PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "hook_error"}},
		{name: "no decision", want: PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "no_decision"}},
		{name: "legacy callback error", legacy: true, callbackError: failure, want: PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "hook_error"}},
		{name: "legacy no decision", legacy: true, want: PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "no_decision"}},
		{name: "missing payload", decision: &core.PipelineShortCircuitDecision{Kind: core.PipelineShortCircuitCache}, want: PipelineHookDecision{PipelineHookFailClosed, PipelineHookTerminate, "missing_payload"}, wantError: ErrInvalidPipelineShortCircuitDecision},
		{name: "unknown kind", decision: &core.PipelineShortCircuitDecision{ShortCircuit: payload, Kind: "not-supported"}, want: PipelineHookDecision{PipelineHookFailClosed, PipelineHookTerminate, "unknown_kind"}, wantError: ErrUnknownPipelineShortCircuitKind},
		{name: "cache disabled", disabled: true, decision: &core.PipelineShortCircuitDecision{ShortCircuit: payload, Kind: core.PipelineShortCircuitCache}, want: PipelineHookDecision{PipelineHookFailClosed, PipelineHookContinue, "cache_read_disabled"}},
		{name: "cache mismatch", decision: &core.PipelineShortCircuitDecision{ShortCircuit: payload, Kind: core.PipelineShortCircuitCache, CachedAgainst: map[string]string{"version": "old"}}, want: PipelineHookDecision{PipelineHookFailClosed, PipelineHookContinue, "cache_dimension_mismatch"}},
		{name: "cache match", decision: &core.PipelineShortCircuitDecision{ShortCircuit: payload, Kind: core.PipelineShortCircuitCache, CachedAgainst: map[string]string{"version": "current"}}, want: PipelineHookDecision{PipelineHookFailClosed, PipelineHookShortCircuit, "cache_match"}},
		{name: "authoritative", disabled: true, decision: &core.PipelineShortCircuitDecision{ShortCircuit: payload, Kind: core.PipelineShortCircuitAuthoritative}, want: PipelineHookDecision{PipelineHookFailClosed, PipelineHookShortCircuit, "authoritative"}},
		{name: "legacy authoritative", legacy: true, disabled: true, decision: &core.PipelineShortCircuitDecision{ShortCircuit: payload}, want: PipelineHookDecision{PipelineHookFailClosed, PipelineHookShortCircuit, "legacy_authoritative"}},
	}
	for _, test := range tests {
		for _, recording := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/recording=%t", test.name, recording), func(t *testing.T) {
				var hook core.PipelineHook = &decisionHook{name: "decision", decision: func(core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
					return test.decision, test.callbackError
				}}
				if test.legacy {
					legacy := &allStagesHook{name: "legacy", beforePlanErr: test.callbackError}
					if test.decision != nil {
						legacy.shortCircuit = test.decision.ShortCircuit
					}
					hook = legacy
				}
				following := &allStagesHook{name: "following"}
				spans := &pipelineHookSpanTelemetry{}
				o := &AIOrchestrator{pipelineHooks: []core.PipelineHook{hook, following}, telemetry: spans}
				holder := newPipelineHookExecutionHolder()
				ctx := t.Context()
				if recording {
					ctx = withPipelineHookExecutionHolder(ctx, holder)
				}
				result, err := o.runBeforePlanningHooks(ctx, &core.PipelineContext{}, newPipelineGate(map[string]string{"version": "current"}, test.disabled), "version")
				if !errors.Is(err, test.wantError) {
					t.Fatalf("error = %v, want %v", err, test.wantError)
				}
				if (result != nil) != (test.want.Action == PipelineHookShortCircuit) || (result != nil && result.shortCircuit.Response != payload.Response) {
					t.Fatalf("short circuit = %+v", result)
				}
				wantFollowing := 0
				if test.want.Action == PipelineHookContinue {
					wantFollowing = 1
				}
				if following.beforePlanCalls != wantFollowing || len(spans.spans) != 1+wantFollowing {
					t.Fatalf("following callback calls = %d, spans = %d", following.beforePlanCalls, len(spans.spans))
				}
				assertPipelineHookDecisionSpan(t, spans.spans[0], test.want)
				wantStatus := PipelineHookSucceeded
				if test.callbackError != nil || test.wantError != nil {
					wantStatus = PipelineHookFailed
				}
				if recording {
					records := holder.Snapshot()
					if len(records) != 1+wantFollowing || records[0].Status != wantStatus {
						t.Fatalf("hook records = %+v", records)
					}
					assertPipelineHookDecision(t, records[0].Decision, test.want)
					if wantStatus == PipelineHookFailed && (len(spans.spans[0].errors) != 1 || spans.spans[0].errors[0].Error() != records[0].Error) {
						t.Fatal("exact failure text diverged between record and trace")
					}
				} else if holder.HasExecutions() {
					t.Fatal("disabled recording captured executions")
				}
			})
		}
	}
}

func TestAfterPlanningDecisionsWithAndWithoutRecording(t *testing.T) {
	for _, required := range []bool{false, true} {
		for _, recording := range []bool{false, true} {
			for _, reason := range []string{"clone_failed", "hook_error", "invalid_type", "invalid_plan", "accepted"} {
				t.Run(fmt.Sprintf("%s/required=%t/recording=%t", reason, required, recording), func(t *testing.T) {
					plan := validAfterPlanningContractPlan()
					failure := errors.New("exact callback failure")
					if reason == "clone_failed" {
						plan.Steps[0].Metadata["unsupported"] = make(chan struct{})
					}
					hook := afterPlanningContractHook{name: "governance", apply: func(candidate interface{}) (interface{}, error) {
						switch reason {
						case "hook_error":
							return nil, failure
						case "invalid_type":
							return "not a plan", nil
						case "invalid_plan":
							candidate.(*RoutingPlan).Steps[0].AgentName = "unregistered-agent"
						}
						return candidate, nil
					}}
					o := newAfterPlanningContractOrchestrator(t)
					o.pipelineHooks = []core.PipelineHook{&hook}
					if required {
						o.pipelineHooks = []core.PipelineHook{&requiredAfterPlanningContractHook{afterPlanningContractHook: hook}}
					}
					spans := &pipelineHookSpanTelemetry{}
					o.telemetry = spans
					holder := newPipelineHookExecutionHolder()
					ctx := t.Context()
					if recording {
						ctx = withPipelineHookExecutionHolder(ctx, holder)
					}
					got, err := o.runValidatedAfterPlanningHooks(ctx, &core.PipelineContext{}, plan, nil, nil, 2, "request")
					want := PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, reason}
					if required {
						want.FailurePolicy = PipelineHookFailClosed
						if reason != "accepted" {
							want.Action = PipelineHookTerminate
						}
					}
					stops := want.Action == PipelineHookTerminate
					if (got == nil) != stops || IsRequiredAfterPlanningError(err) != stops || (!stops && err != nil) {
						t.Fatalf("returned plan/error = %+v/%v, decision = %+v", got, err, want)
					}
					if stops && reason == "hook_error" && !errors.Is(err, failure) {
						t.Fatal("required callback error identity lost")
					}
					if !required && reason != "accepted" && got != plan {
						t.Fatal("optional rejection changed the accepted plan")
					}
					if len(spans.spans) != 1 {
						t.Fatalf("spans = %d", len(spans.spans))
					}
					assertPipelineHookDecisionSpan(t, spans.spans[0], want)
					if recording {
						records := holder.Snapshot()
						if len(records) != 1 || records[0].PlanPhase != 2 {
							t.Fatalf("records = %+v", records)
						}
						assertPipelineHookDecision(t, records[0].Decision, want)
						if reason != "accepted" {
							assertAfterPlanningRejectionRecordForPhase(t, records[0], reason, 2)
						}
					}
				})
			}
		}
	}
}

func assertAfterPlanningRejectionRecordForPhase(t *testing.T, record PipelineHookExecution, reason string, phase int) {
	t.Helper()
	wantStatus := PipelineHookFailed
	if reason == "clone_failed" {
		wantStatus = PipelineHookSkipped
	}
	if record.Status != wantStatus || record.Error == "" || record.PlanPhase != phase {
		t.Fatalf("rejection record = %+v", record)
	}
}

func TestPostHookDecisionsPreserveFailOpenAndResponse(t *testing.T) {
	for _, phase := range []string{PipelineHookPhaseAfterExecution, PipelineHookPhaseAfterSynthesis} {
		for _, fails := range []bool{false, true} {
			for _, recording := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/fails=%t/recording=%t", phase, fails, recording), func(t *testing.T) {
					hook := &allStagesHook{name: "post", responseMutation: "changed response"}
					want := PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "completed"}
					if fails {
						hook.afterExecErr = errors.New("exact failure")
						hook.afterSynthErr = hook.afterExecErr
						want.Reason = "hook_error"
					}
					following := &allStagesHook{name: "following"}
					spans := &pipelineHookSpanTelemetry{}
					o := &AIOrchestrator{pipelineHooks: []core.PipelineHook{hook, following}, telemetry: spans}
					holder := newPipelineHookExecutionHolder()
					ctx := t.Context()
					if recording {
						ctx = withPipelineHookExecutionHolder(ctx, holder)
					}
					if phase == PipelineHookPhaseAfterExecution {
						o.runAfterExecutionHooks(ctx, &core.PipelineContext{}, "exact results")
						if following.afterExecCalls != 1 {
							t.Fatal("following hook was skipped")
						}
					} else {
						got := o.runAfterSynthesisHooks(ctx, &core.PipelineContext{}, "original response")
						wantResponse := "changed response"
						if fails {
							wantResponse = "original response"
						}
						if got != wantResponse || following.afterSynthCalls != 1 {
							t.Fatalf("response = %q, following calls = %d", got, following.afterSynthCalls)
						}
					}
					if len(spans.spans) != 2 {
						t.Fatalf("spans = %d", len(spans.spans))
					}
					assertPipelineHookDecisionSpan(t, spans.spans[0], want)
					if recording {
						records := holder.Snapshot()
						if len(records) != 2 {
							t.Fatalf("records = %+v", records)
						}
						assertPipelineHookDecision(t, records[0].Decision, want)
						if fails && (records[0].Status != PipelineHookFailed || records[0].Error != hook.afterExecErr.Error()) {
							t.Fatal("lost callback failure")
						}
					}
				})
			}
		}
	}
}

func TestPipelineHookDecisionDefensiveSnapshotsAndConcurrentEffects(t *testing.T) {
	holder := newPipelineHookExecutionHolder()
	ctx := withPipelineHookExecutionHolder(t.Context(), holder)
	invocation := beginPipelineHookInvocation(ctx, "governance", PipelineHookPhaseAfterPlanning, 1, 2, time.Now())
	hookCtx := invocation.Context(ctx)
	pending := core.PipelineHookEffect{EffectID: "write", Name: "Write", Status: core.PipelineHookEffectPending}
	if err := core.ReportPipelineHookEffect(hookCtx, pending); err != nil {
		t.Fatal(err)
	}
	want := PipelineHookDecision{PipelineHookFailClosed, PipelineHookTerminate, "hook_error"}
	var published []PipelineHookExecution
	var publishedMu sync.Mutex
	_, revision := holder.SnapshotWithRevision()
	holder.SetTerminalPublisher(revision, newOrderedPipelineHookExecutionPublisher(revision, func(records []PipelineHookExecution) {
		publishedMu.Lock()
		defer publishedMu.Unlock()
		published = records
	}))
	var wg sync.WaitGroup
	wg.Go(func() { invocation.Complete(PipelineHookFailed, errors.New("exact failure"), want) })
	wg.Go(func() {
		pending.Status = core.PipelineHookEffectSucceeded
		pending.Data = json.RawMessage(`{"value":"exact evidence"}`)
		if err := core.ReportPipelineHookEffect(hookCtx, pending); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	assertPipelineHookDecision(t, published[0].Decision, want)
	if string(published[0].Effects[0].Data) != `{"value":"exact evidence"}` || published[0].Error != "exact failure" {
		t.Fatalf("published snapshot lost decision/effects/error: %+v", published)
	}
	published[0].Decision.Action = PipelineHookContinue
	snapshot := holder.Snapshot()
	assertPipelineHookDecision(t, snapshot[0].Decision, want)
	snapshot[0].Decision.Reason = "caller mutation"
	assertPipelineHookDecision(t, holder.Snapshot()[0].Decision, want)
	other := newPipelineHookExecutionHolder()
	original := holder.Snapshot()[0]
	other.Append(original)
	original.Decision.Action = PipelineHookContinue
	assertPipelineHookDecision(t, other.Snapshot()[0].Decision, want)
	// The completion argument is also caller-owned; it must be copied.
	want.Action = PipelineHookContinue
	if holder.Snapshot()[0].Decision.Action != PipelineHookTerminate {
		t.Fatal("holder retained caller-owned decision")
	}
}

func TestPipelineHookDecisionMissingMeansNotRecorded(t *testing.T) {
	var record PipelineHookExecution
	if err := json.Unmarshal([]byte(`{"hook_name":"older","status":"succeeded"}`), &record); err != nil {
		t.Fatal(err)
	}
	if record.Decision != nil {
		t.Fatal("inferred a decision from invocation status")
	}
	encoded, err := json.Marshal(clonePipelineHookExecution(record))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "decision") {
		t.Fatalf("unrecorded decision must be omitted: %s", encoded)
	}
}

func TestPipelineHookDecisionNilAndUnavailableInstrumentation(t *testing.T) {
	want := PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "completed"}
	completePipelineHookInvocation(nil, nil, PipelineHookSucceeded, nil, want)
	for _, invocation := range []*pipelineHookInvocation{{}, {holder: newPipelineHookExecutionHolder(), index: -1}} {
		invocation.Complete(PipelineHookSucceeded, nil, want)
	}
	if _, ok := pipelineHookExecutionHolderFromContext(context.Background()); ok {
		t.Fatal("unexpected holder")
	}
}
