package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestExecutionLifecycleOutcomeIsNotBusinessDisposition(t *testing.T) {
	for _, test := range []struct {
		name   string
		result *requestRunResult
		err    error
		want   string
	}{
		{"negative disposition", &requestRunResult{Response: OrchestratorResponse{Response: `{"decision":"no_change"}`}, Execution: &ExecutionResult{Success: false}}, nil, RequestLifecycleCompleted},
		{"fatal", nil, errors.New("synthesis failure"), RequestLifecycleFailed},
		{"cancelled", nil, context.Canceled, RequestLifecycleCancelled},
		{"deadline", nil, context.DeadlineExceeded, RequestLifecycleFailed},
		{"human suspension", nil, &ErrInterrupted{}, RequestLifecycleSuspended},
		{"partial delivery", &requestRunResult{Delivery: responseDeliveryState{PartialContent: true}}, nil, RequestLifecycleFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			holder := &executionLifecycle{observation: ExecutionLifecycleObservation{State: RequestLifecycleRunning, Revision: 1}}
			state := &executionRunState{Context: context.WithValue(t.Context(), executionLifecycleKey{}, holder)}
			o := &AIOrchestrator{}
			o.settleRequestLifecycle(state, test.result, test.err)
			got := executionLifecycleSnapshot(state.Context)
			if got.State != test.want || got.Revision != 2 || got.ObservedAt.IsZero() {
				t.Fatalf("outcome: %+v", got)
			}
			o.settleRequestLifecycle(state, &requestRunResult{}, nil)
			if *got != *executionLifecycleSnapshot(state.Context) {
				t.Fatal("terminal lifecycle changed")
			}
		})
	}
	if executionLifecycleSnapshot(t.Context()) != nil {
		t.Fatal("legacy context inferred lifecycle")
	}
	(&AIOrchestrator{}).settleRequestLifecycle(&executionRunState{Context: t.Context()}, nil, nil)
}

func TestExecutionRecorderLateEffectsPreserveTerminalSnapshot(t *testing.T) {
	store := &terminalRecordStore{}
	var wg sync.WaitGroup
	r := newExecutionRecorder(t.Context(), store, nil, &core.NoOpLogger{}, &wg, time.Second)
	running := &StoredExecution{RequestID: "request", Plan: &RoutingPlan{PlanID: "phase-1"}, Lifecycle: &ExecutionLifecycleObservation{State: RequestLifecycleRunning, Revision: 1}, Result: &ExecutionResult{Success: true}}
	r.Record(executionRecordSnapshot{Record: running, HookRevision: 1})
	response := "honest terminal response"
	terminal := &StoredExecution{RequestID: "request", Plan: &RoutingPlan{PlanID: "phase-3"}, Lifecycle: &ExecutionLifecycleObservation{State: RequestLifecycleCompleted, Revision: 2}, FinalResponse: &response, Result: &ExecutionResult{Success: true}}
	r.Record(executionRecordSnapshot{Record: terminal, HookRevision: 2})
	running.PipelineHooks = []PipelineHookExecution{{HookName: "enrichment", Effects: []core.PipelineHookEffect{{EffectID: "cleanup", Status: core.PipelineHookEffectSucceeded}}}}
	r.RecordHookEffects(executionRecordSnapshot{Record: running, HookRevision: 3})
	r.Record(executionRecordSnapshot{Record: running, HookRevision: 1})            // stale phase
	r.RecordHookEffects(executionRecordSnapshot{Record: running, HookRevision: 2}) // stale effect
	if !r.RecordLifecycle(&ExecutionLifecycleObservation{State: RequestLifecycleCompleted, Revision: 2}, time.Hour) {
		t.Fatal("missing latest record")
	}
	wg.Wait()
	records, _ := store.snapshot()
	if len(records) != 3 {
		t.Fatalf("stale snapshots accepted: %d", len(records))
	}
	last := records[2]
	if last.Lifecycle.State != RequestLifecycleCompleted || last.Plan.PlanID != "phase-3" || last.FinalResponse == nil || *last.FinalResponse != response || last.RecordRevision != 3 || len(last.PipelineHooks) != 1 {
		t.Fatalf("terminal snapshot regressed: %+v", last)
	}
	for i, record := range records {
		if record.RecordRevision != uint64(i+1) {
			t.Fatal("record revisions not monotonic")
		}
	}
	summary := executionSummaryFromStored(last)
	if summary.Lifecycle.State != RequestLifecycleCompleted || summary.RecordRevision != last.RecordRevision {
		t.Fatal("summary dropped lifecycle")
	}
}

func TestExecutionRecorderLifecycleOnlyAndLegacy(t *testing.T) {
	store := &terminalRecordStore{}
	var wg sync.WaitGroup
	r := newExecutionRecorder(t.Context(), store, nil, &core.NoOpLogger{}, &wg, time.Second)
	observation := &ExecutionLifecycleObservation{State: RequestLifecycleFailed, Revision: 2}
	if r.RecordLifecycle(observation, time.Hour) {
		t.Fatal("invented record")
	}
	r.Record(executionRecordSnapshot{Record: &StoredExecution{RequestID: "early", Lifecycle: &ExecutionLifecycleObservation{State: RequestLifecycleRunning, Revision: 1}}, RetentionTTL: time.Minute})
	if !r.RecordLifecycle(observation, time.Hour) {
		t.Fatal("lost record")
	}
	wg.Wait()
	records, _ := store.snapshot()
	if len(records) != 2 || records[1].Lifecycle.State != RequestLifecycleFailed || records[1].Result != nil {
		t.Fatal("early failure invented step result or lost state")
	}
	if executionRetentionTTL(records[1], time.Minute, time.Hour) != time.Hour || r.latest.RetentionTTL != time.Hour {
		t.Fatal("early failure shortened retention")
	}
	if executionSummaryFromStored(&StoredExecution{}).Lifecycle != nil {
		t.Fatal("legacy record inferred completion")
	}
}
