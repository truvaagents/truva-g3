package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type panicStageHook struct {
	stage                string
	value                interface{}
	planCall, atPlanCall int
	beforePanic          func(context.Context)
}

func (*panicStageHook) Name() string { return "panic-probe" }
func (h *panicStageHook) trip(ctx context.Context, stage string) {
	if h.stage != stage {
		return
	}
	if stage == PipelineHookPhaseAfterPlanning {
		h.planCall++
		if h.atPlanCall > 0 && h.planCall != h.atPlanCall {
			return
		}
	}
	if h.beforePanic != nil {
		h.beforePanic(ctx)
	}
	panic(h.value)
}
func (h *panicStageHook) BeforePlanning(ctx context.Context, _ *core.PipelineContext) (*core.PipelineShortCircuit, error) {
	h.trip(ctx, PipelineHookPhaseBeforePlanning)
	return nil, nil
}
func (h *panicStageHook) AfterPlanning(ctx context.Context, _ *core.PipelineContext, plan interface{}) (interface{}, error) {
	h.trip(ctx, PipelineHookPhaseAfterPlanning)
	return plan, nil
}
func (h *panicStageHook) AfterExecution(ctx context.Context, _ *core.PipelineContext, _ interface{}) error {
	h.trip(ctx, PipelineHookPhaseAfterExecution)
	return nil
}
func (h *panicStageHook) AfterSynthesis(ctx context.Context, _ *core.PipelineContext, response string) (string, error) {
	h.trip(ctx, PipelineHookPhaseAfterSynthesis)
	return response, nil
}

type panicDecisionHook struct{ *panicStageHook }

func (h *panicDecisionHook) BeforePlanningDecision(ctx context.Context, _ *core.PipelineContext, _ core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
	h.trip(ctx, PipelineHookPhaseBeforePlanning)
	return nil, nil
}

type requiredPanicHook struct{ *panicStageHook }

func (*requiredPanicHook) RequireAfterPlanningSuccess() {}

type panicJSONValue struct{ value interface{} }

func (v panicJSONValue) MarshalJSON() ([]byte, error) { panic(v.value) }

type panicValidationDiscovery struct {
	core.Discovery
	value interface{}
}

func (d panicValidationDiscovery) FindService(context.Context, string) ([]*core.ServiceRegistration, error) {
	panic(d.value)
}

func caughtPanic(fn func()) (value interface{}) {
	defer func() { value = recover() }()
	fn()
	return nil
}

func TestPipelineHookPanicCompletesInvocationAndSpan(t *testing.T) {
	for _, stage := range []string{"before_planning", "decision", "after_planning", "required", "clone", "validation", "after_execution", "after_synthesis"} {
		for _, recording := range []bool{false, true} {
			for _, tracing := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/recording_%t/tracing_%t", stage, recording, tracing), func(t *testing.T) {
					failure := errors.New("exact panic payload password: not-a-real-secret")
					h := &panicStageHook{stage: stage, value: failure}
					var hook core.PipelineHook = h
					policy := PipelineHookFailOpen
					plan := validAfterPlanningContractPlan()
					switch stage {
					case "decision":
						h.stage = PipelineHookPhaseBeforePlanning
						hook = &panicDecisionHook{h}
					case "required":
						h.stage = PipelineHookPhaseAfterPlanning
						hook = &requiredPanicHook{h}
						policy = PipelineHookFailClosed
					case "clone":
						h.stage = PipelineHookPhaseAfterPlanning
						plan.Steps[0].Metadata["probe"] = panicJSONValue{failure}
					}
					o := newAfterPlanningContractOrchestrator(t)
					if stage == "validation" {
						h.stage = "do-not-panic-in-callback"
						o.discovery = panicValidationDiscovery{Discovery: o.discovery, value: failure}
					}
					following := &allStagesHook{name: "must-not-run"}
					o.pipelineHooks = []core.PipelineHook{hook, following}
					spans := &pipelineHookSpanTelemetry{}
					if tracing {
						o.telemetry = spans
					} else {
						o.telemetry = nil
					}
					holder := newPipelineHookExecutionHolder()
					ctx := t.Context()
					if recording {
						ctx = withPipelineHookExecutionHolder(ctx, holder)
					}
					got := caughtPanic(func() {
						switch h.stage {
						case PipelineHookPhaseBeforePlanning:
							_, _ = o.runBeforePlanningHooks(ctx, &core.PipelineContext{}, newPipelineGate(nil, false))
						case PipelineHookPhaseAfterPlanning, "do-not-panic-in-callback":
							_, _ = o.runValidatedAfterPlanningHooks(ctx, &core.PipelineContext{}, plan, nil, nil, 2, "request")
						case PipelineHookPhaseAfterExecution:
							o.runAfterExecutionHooks(ctx, &core.PipelineContext{}, &ExecutionResult{})
						case PipelineHookPhaseAfterSynthesis:
							_ = o.runAfterSynthesisHooks(ctx, &core.PipelineContext{}, "draft")
						}
					})
					if got != failure {
						t.Fatalf("panic identity = %v, want original pointer", got)
					}
					if following.beforePlanCalls+following.afterPlanCalls+following.afterExecCalls+following.afterSynthCalls != 0 {
						t.Fatal("panic ran a following hook")
					}
					want := PipelineHookDecision{policy, PipelineHookPropagatePanic, "panic"}
					if tracing {
						if len(spans.spans) != 1 {
							t.Fatalf("span count = %d", len(spans.spans))
						}
						assertPipelineHookDecisionSpan(t, spans.spans[0], want)
						if spans.spans[0].endCount != 1 || len(spans.spans[0].errors) != 1 || !strings.Contains(spans.spans[0].errors[0].Error(), failure.Error()) {
							t.Fatalf("panic span = %+v", spans.spans[0])
						}
					}
					if recording {
						records := holder.Snapshot()
						if len(records) != 1 || records[0].Status != PipelineHookFailed || records[0].Duration <= 0 || !strings.Contains(records[0].Error, failure.Error()) {
							t.Fatalf("panic evidence = %+v", records)
						}
						assertPipelineHookDecision(t, records[0].Decision, want)
					}
				})
			}
		}
	}
}

func TestPipelineHookPanicPreservesRequestEvidence(t *testing.T) {
	for _, mode := range []string{"buffered", "native", "simulated"} {
		for _, stage := range []string{PipelineHookPhaseBeforePlanning, PipelineHookPhaseAfterPlanning, "later_planning", PipelineHookPhaseAfterExecution, PipelineHookPhaseAfterSynthesis} {
			for _, recording := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/recording_%t", mode, stage, recording), func(t *testing.T) {
					plans := []string{foundationTerminalSingleStepPlan, "synthesized response"}
					if stage == "later_planning" {
						plans = []string{foundationNonTerminalSingleStepPlan("phase-1", "step-1"), foundationNonTerminalSingleStepPlan("phase-2", "step-2")}
					}
					o, client, transport, store, debug := newRequiredHookRecordingFixture(t, plans)
					client.native = mode == "native"
					if stage == PipelineHookPhaseAfterSynthesis && mode == "native" {
						o.aiClient = &terminalStreamingClient{}
						o.synthesizer = NewAISynthesizer(o.aiClient)
					}
					if !recording {
						o.executionStore, o.debugStore = nil, nil
					}
					spans := &pipelineHookSpanTelemetry{}
					o.telemetry = spans
					failure := &struct{ message string }{"original panic"}
					h := &panicStageHook{stage: stage, value: failure}
					if stage == "later_planning" {
						h.stage, h.atPlanCall = PipelineHookPhaseAfterPlanning, 2
					}
					o.pipelineHooks = []core.PipelineHook{h}
					got := caughtPanic(func() {
						if mode == "buffered" {
							_, _ = o.ProcessRequest(t.Context(), "request", nil)
						} else {
							_, _ = o.ProcessRequestStreaming(t.Context(), "request", nil, func(core.StreamChunk) error { return nil })
						}
					})
					shutdownRequiredHookFixture(t, o)
					if got != failure {
						t.Fatalf("panic identity changed: %v", got)
					}
					wantSteps := 1
					if stage == PipelineHookPhaseBeforePlanning || stage == PipelineHookPhaseAfterPlanning {
						wantSteps = 0
					}
					if transport.GetCallCount() != wantSteps {
						t.Fatalf("tool count = %d, want %d", transport.GetCallCount(), wantSteps)
					}
					for _, span := range spans.spans {
						if !span.ended || span.endCount != 1 || span.attributesAfterEnd {
							t.Fatalf("span not completed once: %+v", span)
						}
						if strings.HasPrefix(span.name, "orchestrator.process_request") && len(span.errors) != 1 {
							t.Fatalf("request failure not traced: %+v", span)
						}
					}
					records, _ := store.snapshot()
					if !recording {
						if len(records) != 0 {
							t.Fatal("disabled store was used")
						}
						return
					}
					if len(records) == 0 {
						t.Fatal("no terminal failure record")
					}
					last := records[len(records)-1]
					if last.Result == nil || last.Result.Success || last.Result.TotalDuration <= 0 || len(last.Result.Steps) != wantSteps || last.FinalResponse != nil || last.Interrupted {
						t.Fatalf("incorrect panic result: %+v", last)
					}
					if stage == "later_planning" && (last.PhaseCount != 1 || len(last.PhasePlans) != 1 || last.Plan.PlanID != "phase-1" || !reflect.DeepEqual(records[0].Result.Steps, last.Result.Steps)) {
						t.Fatalf("prior phase erased: %+v", last)
					}
					lastHook := last.PipelineHooks[len(last.PipelineHooks)-1]
					assertPipelineHookDecision(t, lastHook.Decision, PipelineHookDecision{PipelineHookFailOpen, PipelineHookPropagatePanic, "panic"})
					if debug.ttl(last.RequestID) != o.config.ExecutionStore.ErrorTTL {
						t.Fatal("panic selected normal retention")
					}
					assertRequiredHookStoreRetention(t, last, o.config.ExecutionStore, o.config.ExecutionStore.ErrorTTL)
				})
			}
		}
	}
}

func TestPipelineHookPanicValuesAreNotReplaced(t *testing.T) {
	for _, value := range []interface{}{nil, "exact string", 17, []int{1, 2}, errors.New("exact error")} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			holder := newPipelineHookExecutionHolder()
			ctx := withPipelineHookExecutionHolder(t.Context(), holder)
			o := &AIOrchestrator{pipelineHooks: []core.PipelineHook{&panicStageHook{stage: PipelineHookPhaseBeforePlanning, value: value}}}
			got := caughtPanic(func() { _, _ = o.runBeforePlanningHooks(ctx, &core.PipelineContext{}, newPipelineGate(nil, false)) })
			if value == nil {
				if _, ok := got.(*runtime.PanicNilError); !ok {
					t.Fatalf("nil panic not propagated: %T", got)
				}
			} else if !reflect.DeepEqual(got, value) {
				t.Fatalf("panic replaced: got %v, want %v", got, value)
			}
			records := holder.Snapshot()
			if len(records) != 1 || records[0].Status != PipelineHookFailed || records[0].Error != fmt.Sprintf("pipeline hook panic: %v", got) {
				t.Fatalf("panic evidence = %+v", records)
			}
		})
	}
}

func TestPipelineHookPanicCancelsPhaseAndPreservesResumeNumber(t *testing.T) {
	o, _, transport, store, _ := newRequiredHookRecordingFixture(t, []string{foundationNonTerminalSingleStepPlan("next-phase", "step-6")})
	o.config.IterativePlanning.MaxPhases = 8
	o.config.IterativePlanning.PhaseTimeout = time.Hour
	var override RoutingPlan
	if err := json.Unmarshal([]byte(foundationNonTerminalSingleStepPlan("resumed", "step-5")), &override); err != nil {
		t.Fatal(err)
	}
	override.PhaseNumber = 5
	ctx := WithPlanOverride(t.Context(), &override)
	ctx = WithCompletedSteps(ctx, map[string]*StepResult{"step-4": {StepID: "step-4", Success: true, Response: "exact prior result"}})
	var hookCtx context.Context
	failure := errors.New("continuation panic")
	o.pipelineHooks = []core.PipelineHook{&requiredPanicHook{&panicStageHook{stage: PipelineHookPhaseAfterPlanning, value: failure, beforePanic: func(ctx context.Context) { hookCtx = ctx }}}}
	got := caughtPanic(func() { _, _ = o.ProcessRequest(ctx, "request", nil) })
	shutdownRequiredHookFixture(t, o)
	if got != failure || hookCtx == nil || !errors.Is(hookCtx.Err(), context.Canceled) || transport.GetCallCount() != 1 {
		t.Fatalf("panic/phase cleanup: got=%v ctx=%v tools=%d", got, hookCtx, transport.GetCallCount())
	}
	records, _ := store.snapshot()
	last := records[len(records)-1]
	if last.PhaseCount != 5 || last.Result.Success || len(last.Result.Steps) != 2 || last.Plan.PlanID != "resumed" || len(last.PhasePlans) != 1 {
		t.Fatalf("lost resumed prior work: %+v", last)
	}
	hook := last.PipelineHooks[len(last.PipelineHooks)-1]
	if hook.PlanPhase != 6 {
		t.Fatalf("panic phase = %d", hook.PlanPhase)
	}
	assertPipelineHookDecision(t, hook.Decision, PipelineHookDecision{PipelineHookFailClosed, PipelineHookPropagatePanic, "panic"})
}

func TestPipelineHookPanicLateEffectPreservesFailureAndDrainsShutdown(t *testing.T) {
	for _, stage := range []string{PipelineHookPhaseBeforePlanning, PipelineHookPhaseAfterPlanning, PipelineHookPhaseAfterSynthesis} {
		t.Run(stage, func(t *testing.T) {
			o, _, _, _, debug := newRequiredHookRecordingFixture(t, []string{foundationTerminalSingleStepPlan, "draft"})
			store := &pipelineHookCaptureExecutionStore{NoOpExecutionStore: NewNoOpExecutionStore(), records: make(chan *StoredExecution, 8)}
			o.executionStore = store
			var hookCtx context.Context
			effectCompleted := false
			defer func() {
				if hookCtx != nil && !effectCompleted {
					_ = core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{EffectID: "pending", Name: "Delayed audit", Status: core.PipelineHookEffectFailed})
				}
			}()
			failure := errors.New("panic after scheduling effect")
			o.pipelineHooks = []core.PipelineHook{&panicStageHook{stage: stage, value: failure, beforePanic: func(ctx context.Context) {
				hookCtx = ctx
				if err := core.ReportPipelineHookEffect(ctx, core.PipelineHookEffect{EffectID: "pending", Name: "Delayed audit", Status: core.PipelineHookEffectPending}); err != nil {
					t.Fatal(err)
				}
			}}}
			if got := caughtPanic(func() { _, _ = o.ProcessRequest(t.Context(), "request", nil) }); got != failure {
				t.Fatalf("panic = %v", got)
			}
			var initial *StoredExecution
			deadline := time.After(3 * time.Second)
			for initial == nil {
				select {
				case record := <-store.records:
					// Synthesis can have a legitimate earlier step snapshot. Wait
					// for the terminal panic, not simply the first stored record.
					if n := len(record.PipelineHooks); n > 0 && record.PipelineHooks[n-1].Decision != nil && record.PipelineHooks[n-1].Decision.Action == PipelineHookPropagatePanic {
						initial = record
					}
				case <-deadline:
					t.Fatal("terminal write missing")
				}
			}
			if initial.Result == nil || initial.Result.Success || initial.FinalResponse != nil {
				t.Fatalf("initial result = %+v", initial)
			}
			shutdown := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				shutdown <- o.Shutdown(ctx)
			}()
			select {
			case err := <-shutdown:
				t.Fatalf("shutdown did not wait for announced effect: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{EffectID: "pending", Name: "Delayed audit", Status: core.PipelineHookEffectFailed, Error: "exact backend failure", Data: json.RawMessage(`{"value":"exact"}`)}); err != nil {
				t.Fatal(err)
			}
			effectCompleted = true
			select {
			case err := <-shutdown:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("shutdown hung after effect completion")
			}
			var updated *StoredExecution
			select {
			case updated = <-store.records:
			default:
				t.Fatal("effect rewrite missing after shutdown")
			}
			if !reflect.DeepEqual(initial.Result, updated.Result) || updated.FinalResponse != nil || debug.ttl(updated.RequestID) != o.config.ExecutionStore.ErrorTTL {
				t.Fatal("effect overwrote failed result or retention")
			}
			h := updated.PipelineHooks[len(updated.PipelineHooks)-1]
			assertPipelineHookDecision(t, h.Decision, PipelineHookDecision{PipelineHookFailOpen, PipelineHookPropagatePanic, "panic"})
			if len(h.Effects) != 1 || h.Effects[0].Error != "exact backend failure" || h.Effects[0].Status != core.PipelineHookEffectFailed {
				t.Fatalf("effect = %+v", h.Effects)
			}
		})
	}
}

func TestPipelineHookBuiltinEffectsRemainIndependentOfInvocation(t *testing.T) {
	failure := errors.New("exact provider failure")
	cleanup, err := NewActivityCleanupHook(&core.MockActivityCoordinator{
		CompleteActivityFn: func(context.Context, string) error { return failure },
	}, &core.NoOpLogger{})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	extraction, err := NewKnowledgeExtractionHook(&core.MockSharedKnowledge{}, &core.MockEmbeddingClient{}, &mockAIClient{
		generateFunc: func(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
			<-release
			return nil, failure
		},
	}, "test-agent", "test-domain")
	if err != nil {
		t.Fatal(err)
	}
	defer extraction.Close()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	holder := newPipelineHookExecutionHolder()
	ctx := withPipelineHookExecutionHolder(t.Context(), holder)
	o := &AIOrchestrator{pipelineHooks: []core.PipelineHook{cleanup, extraction}}
	if got := o.runAfterSynthesisHooks(ctx, &core.PipelineContext{Request: "request"}, "exact final answer"); got != "exact final answer" {
		t.Fatal("built-in hook altered response")
	}
	records := holder.Snapshot()
	for _, h := range records {
		if h.Status != PipelineHookSucceeded {
			t.Fatalf("effect failure changed invocation: %+v", h)
		}
		assertPipelineHookDecision(t, h.Decision, PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "completed"})
	}
	if len(records) != 2 || records[0].Effects[0].Status != core.PipelineHookEffectFailed || records[0].Effects[0].Error != failure.Error() || records[1].Effects[0].Status != core.PipelineHookEffectPending {
		t.Fatalf("effect statuses = %+v", records)
	}
	close(release)
	released = true
	extraction.Close()
	updated := holder.Snapshot()[1]
	if updated.Status != PipelineHookSucceeded || updated.Effects[0].Status != core.PipelineHookEffectFailed || updated.Effects[0].Error != failure.Error() {
		t.Fatalf("asynchronous effect changed invocation or lost error: %+v", updated)
	}
	assertPipelineHookDecision(t, updated.Decision, PipelineHookDecision{PipelineHookFailOpen, PipelineHookContinue, "completed"})
}

type panicCompletionLogger struct {
	core.NoOpLogger
	completions []map[string]interface{}
}

func (l *panicCompletionLogger) ErrorWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	if fields["operation"] == "process_request_complete" || fields["operation"] == "streaming_complete" {
		l.completions = append(l.completions, fields)
	}
}

func TestPipelineHookPanicLogsOneBoundedRequestCompletion(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			o, _, _, _, _ := newRequiredHookRecordingFixture(t, nil)
			logger := &panicCompletionLogger{}
			o.logger = logger
			o.telemetry = &core.NoOpTelemetry{}
			payload := "application-owned panic text"
			o.pipelineHooks = []core.PipelineHook{&panicStageHook{stage: PipelineHookPhaseBeforePlanning, value: payload}}
			got := caughtPanic(func() {
				if streaming {
					_, _ = o.ProcessRequestStreaming(t.Context(), "request", nil, func(core.StreamChunk) error { return nil })
				} else {
					_, _ = o.ProcessRequest(t.Context(), "request", nil)
				}
			})
			shutdownRequiredHookFixture(t, o)
			if got != payload || len(logger.completions) != 1 {
				t.Fatalf("panic=%v completions=%+v", got, logger.completions)
			}
			fields := logger.completions[0]
			if fields["termination_reason"] != "panic" || fields["status"] != "error" || fields["success"] != false || fields["error_type"] != "request_failed" || fields["request_id"] == "" || fields["duration_ms"] == nil {
				t.Fatalf("missing completion contract: %+v", fields)
			}
			if strings.Contains(fmt.Sprint(fields), payload) {
				t.Fatal("request log duplicated raw panic payload")
			}
		})
	}
}

func TestNativeSynthesisPanicRetainsCompletedLLMCallAndChunkCount(t *testing.T) {
	for _, tracing := range []bool{false, true} {
		t.Run(fmt.Sprint(tracing), func(t *testing.T) {
			o, _, _, store, _ := newRequiredHookRecordingFixture(t, nil)
			o.aiClient = &terminalStreamingClient{}
			o.synthesizer = NewAISynthesizer(o.aiClient)
			debug := NewMemoryLLMDebugStore()
			o.debugStore = debug
			logger := &panicCompletionLogger{}
			o.logger = logger
			if !tracing {
				o.telemetry = nil
			}
			failure := errors.New("after synthesis panic")
			o.pipelineHooks = []core.PipelineHook{&panicStageHook{stage: PipelineHookPhaseAfterSynthesis, value: failure}}
			chunks := 0
			got := caughtPanic(func() {
				_, _ = o.ProcessRequestStreaming(t.Context(), "request", nil, func(core.StreamChunk) error { chunks++; return nil })
			})
			shutdownRequiredHookFixture(t, o)
			if got != failure || chunks == 0 {
				t.Fatalf("panic=%v delivered=%d", got, chunks)
			}
			records, _ := store.snapshot()
			last := records[len(records)-1]
			llm, err := debug.GetRecord(t.Context(), last.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			var synthesis []LLMInteraction
			for _, interaction := range llm.Interactions {
				if interaction.Type == "synthesis_streaming" {
					synthesis = append(synthesis, interaction)
				}
			}
			if len(synthesis) != 1 || !synthesis[0].Success || synthesis[0].Response != "streamed response" || synthesis[0].Model != "synthesizer" || synthesis[0].Provider != "test" {
				t.Fatalf("completed model call lost or reclassified: %+v", synthesis)
			}
			if last.Result.Success || last.FinalResponse != nil {
				t.Fatal("model success became request success")
			}
			if len(logger.completions) != 1 || logger.completions[0]["chunks_delivered"] != chunks {
				t.Fatalf("failure log lost delivered chunks: %+v", logger.completions)
			}
		})
	}
}
