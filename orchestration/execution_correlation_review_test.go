package orchestration

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/baggage"
)

func TestExecutionCorrelationRejectsInvalidInheritedRoot(t *testing.T) {
	for _, test := range []struct {
		name, inherited, incoming, wantRoot string
	}{
		{name: "space rejected with local fallback", inherited: "invalid root", wantRoot: "child"},
		{name: "Unicode rejected with header fallback", inherited: "旧-root", incoming: "header-root", wantRoot: "header-root"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Raw OTel baggage can carry decoded Unicode/spaces from a valid
			// percent-encoded W3C header, although those are invalid caller IDs.
			member, err := baggage.NewMemberRaw("original_request_id", test.inherited)
			require.NoError(t, err)
			bag, err := baggage.New(member)
			require.NoError(t, err)
			ctx := telemetry.WithBaggage(baggage.ContextWithBaggage(t.Context(), bag), "request_id", "parent")
			require.Equal(t, test.inherited, telemetry.GetBaggage(ctx)["original_request_id"], "fixture must carry the actual invalid inherited value")
			ctx = incomingExecutionContext(t, ctx, core.IncomingCall{RequestID: "parent", StepID: "step-17", OriginalRequestID: test.incoming})
			logger := &TestLogger{}
			orchestrator := &AIOrchestrator{logger: logger}
			child, correlation := orchestrator.beginExecutionCorrelation(ctx, "child")
			require.Equal(t, test.wantRoot, correlation.OriginalRequestID)
			require.Equal(t, executionParent{RequestID: "parent", StepID: "step-17"}, correlation.Parent)
			require.Equal(t, test.wantRoot, telemetry.GetBaggage(child)["original_request_id"])
			require.Equal(t, "child", core.GetRequestID(child))
			require.Equal(t, "child", GetRequestID(child))
			_, incomingPresent := core.IncomingCallFromContext(child)
			require.False(t, incomingPresent)
			require.Equal(t, test.inherited, telemetry.GetBaggage(ctx)["original_request_id"], "the caller context is immutable")
			logs := logger.GetLogsByOperation("execution_correlation")
			require.Len(t, logs, 1)
			require.Equal(t, "child", logs[0].Fields["request_id"])
			require.Equal(t, "original_request_id", logs[0].Fields["field"])
			require.Equal(t, "invalid_identifier", logs[0].Fields["reason"])
			require.Equal(t, "omitted", logs[0].Fields["status"])
			require.Equal(t, "execution correlation could not be propagated", logs[0].Fields["error"], "diagnostics do not echo rejected correlation data")
		})
	}
}

func TestExecutionCorrelationOmitsSelfParent(t *testing.T) {
	ctx := telemetry.WithBaggage(t.Context(), "request_id", "caller", "original_request_id", "root", "step_id", "step-17")
	ctx = incomingExecutionContext(t, ctx, core.IncomingCall{RequestID: "child", StepID: "step-17", OriginalRequestID: "root"})
	logger := &TestLogger{}
	orchestrator := &AIOrchestrator{logger: logger}
	child, correlation := orchestrator.beginExecutionCorrelation(ctx, "child")
	require.Empty(t, correlation.Parent)
	require.Equal(t, "root", correlation.OriginalRequestID, "omitting an invalid edge must preserve the family root")
	require.Equal(t, "child", core.GetRequestID(child))
	require.Equal(t, "child", GetRequestID(child))
	require.Equal(t, "root", telemetry.GetBaggage(child)["original_request_id"])
	require.Empty(t, telemetry.GetBaggage(child)["step_id"])
	_, incomingPresent := core.IncomingCallFromContext(child)
	require.False(t, incomingPresent)
	require.Equal(t, "caller", telemetry.GetBaggage(ctx)["request_id"])
	require.Equal(t, "step-17", telemetry.GetBaggage(ctx)["step_id"])
	logs := logger.GetLogsByOperation("execution_correlation")
	require.Len(t, logs, 1)
	require.Equal(t, "child", logs[0].Fields["request_id"])
	require.Equal(t, "parent_request_id", logs[0].Fields["field"])
	require.Equal(t, "self_parent", logs[0].Fields["reason"])
	require.Equal(t, "omitted", logs[0].Fields["status"])
}

func TestCheckpointResponsePreservesExecutionParent(t *testing.T) {
	for _, parent := range []executionParent{{RequestID: "caller", StepID: "step-18"}, {RequestID: "caller"}, {}} {
		checkpoint := &ExecutionCheckpoint{
			CheckpointID: "checkpoint", RequestID: "child", OriginalRequestID: "root",
			ParentRequestID: parent.RequestID, ParentStepID: parent.StepID,
			ParentCheckpointID: "prior-checkpoint", Status: CheckpointStatusPending,
			UserContext: map[string]interface{}{"content": "exact **中文** <a>"},
		}
		response := CheckpointResponseFrom(checkpoint)
		require.Equal(t, parent.RequestID, response.ParentRequestID)
		require.Equal(t, parent.StepID, response.ParentStepID)
		require.Equal(t, "prior-checkpoint", response.ParentCheckpointID)
		require.Equal(t, checkpoint.UserContext, response.UserContext)
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &fields))
		if parent.RequestID == "" {
			require.NotContains(t, fields, "parent_request_id")
		} else {
			require.JSONEq(t, `"caller"`, string(fields["parent_request_id"]))
		}
		if parent.StepID == "" {
			require.NotContains(t, fields, "parent_step_id")
		} else {
			require.JSONEq(t, `"step-18"`, string(fields["parent_step_id"]))
		}
		response.ParentRequestID = "changed-response"
		require.Equal(t, parent.RequestID, checkpoint.ParentRequestID)
	}
	require.Nil(t, CheckpointResponseFrom(nil))
}

func TestManagedCheckpointConstructionUsesTypedCorrelationBeforeEnrichment(t *testing.T) {
	store := newMockCheckpointStore()
	policy := &mockPolicy{planDecision: &InterruptDecision{ShouldInterrupt: true, Reason: ReasonPlanApproval}}
	controller := NewInterruptController(policy, store, nil)
	correlation := requestCorrelation{
		RequestID: "child", OriginalRequestID: "root",
		Parent: executionParent{RequestID: "parent", StepID: "step-17"},
	}
	ctx := withRequestCorrelation(WithRequestID(t.Context(), correlation.RequestID), correlation)
	ctx = withCheckpointEnrichmentRequired(ctx)
	// Exact-set rejection may have removed root baggage; the authoritative
	// typed root must already be present in the controller's preparing write.
	ctx = telemetry.WithoutBaggageMember(ctx, "original_request_id")
	checkpoint, err := controller.CheckPlanApproval(ctx, &RoutingPlan{PlanID: "plan"})
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	require.Equal(t, CheckpointStatusPreparing, checkpoint.Status)
	stored := store.checkpoints[checkpoint.CheckpointID]
	require.NotNil(t, stored)
	require.Equal(t, "root", stored.OriginalRequestID)
	require.Equal(t, "parent", stored.ParentRequestID)
	require.Equal(t, "step-17", stored.ParentStepID)
	encoded, err := json.Marshal(stored)
	require.NoError(t, err)
	var roundTrip ExecutionCheckpoint
	require.NoError(t, json.Unmarshal(encoded, &roundTrip))
	require.Equal(t, stored.ParentRequestID, roundTrip.ParentRequestID)
	require.Equal(t, stored.ParentStepID, roundTrip.ParentStepID)
	// Correlation alone does not add a required enrichment capability to the
	// legacy custom-controller contract.
	legacy := checkpointCoordinator{controller: newMockInterruptController()}
	legacyCheckpoint := &ExecutionCheckpoint{RequestID: "child", Status: CheckpointStatusPending}
	require.NoError(t, legacy.saveAuthoritative(ctx, legacyCheckpoint, executionRunSnapshot{Correlation: correlation}))
}

func TestAuthoritativeCheckpointCorrelationRequiresBothOwners(t *testing.T) {
	for _, test := range []struct {
		name, contextID, checkpointID  string
		managed                        bool
		wantRoot, wantParent, wantStep string
	}{
		{name: "matching owner clears stale parent", contextID: "child", checkpointID: "child", managed: true, wantRoot: "root"},
		{name: "different current execution", contextID: "direct", checkpointID: "child", managed: true, wantRoot: "prior-root", wantParent: "prior-parent", wantStep: "prior-step"},
		{name: "different checkpoint execution", contextID: "child", checkpointID: "direct", managed: true, wantRoot: "prior-root", wantParent: "prior-parent", wantStep: "prior-step"},
		{name: "unmanaged cannot supply snapshot identity", contextID: "child", checkpointID: "child", wantRoot: "prior-root", wantParent: "prior-parent", wantStep: "prior-step"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMockCheckpointStore()
			controller := NewInterruptController(nil, store, nil)
			o := &AIOrchestrator{interruptController: controller}
			ctx := WithRequestID(t.Context(), test.contextID)
			if test.managed {
				ctx = withRequestCorrelation(ctx, requestCorrelation{RequestID: "child", OriginalRequestID: "root"})
			}
			checkpoint := &ExecutionCheckpoint{
				CheckpointID: "checkpoint", RequestID: test.checkpointID,
				OriginalRequestID: "prior-root", ParentRequestID: "prior-parent",
				ParentStepID: "prior-step",
				Status:       CheckpointStatusPreparing,
			}
			snapshot := executionRunSnapshot{Correlation: requestCorrelation{
				RequestID: test.checkpointID, OriginalRequestID: "forged-snapshot-root",
				Parent: executionParent{RequestID: "forged-snapshot-parent"},
			}}
			require.NoError(t, o.saveAuthoritativeCheckpoint(ctx, checkpoint, snapshot, "plan_level"))
			stored := store.checkpoints["checkpoint"]
			require.NotNil(t, stored)
			require.Equal(t, CheckpointStatusPending, stored.Status)
			require.Equal(t, test.wantRoot, stored.OriginalRequestID)
			require.Equal(t, test.wantParent, stored.ParentRequestID)
			require.Equal(t, test.wantStep, stored.ParentStepID)
		})
	}
}

func TestExecutionCorrelationSurvivesLateEffectsAndStaleSnapshots(t *testing.T) {
	store := &terminalRecordStore{}
	var wg sync.WaitGroup
	recorder := newExecutionRecorder(t.Context(), store, nil, &core.NoOpLogger{}, &wg, time.Second)
	running := &StoredExecution{
		RequestID: "child", OriginalRequestID: "root", ParentRequestID: "parent", ParentStepID: "step-17",
		Lifecycle: &ExecutionLifecycleObservation{State: RequestLifecycleRunning, Revision: 1},
	}
	recorder.Record(executionRecordSnapshot{Record: running, HookRevision: 1})
	terminal := *running
	terminal.Plan = &RoutingPlan{PlanID: "last-child-plan"}
	recorder.Record(executionRecordSnapshot{Record: &terminal, HookRevision: 2})
	// An older phase's effect snapshot must update only its effects, even when
	// its own optional correlation fields are missing.
	running.ParentRequestID, running.ParentStepID, running.OriginalRequestID = "", "", "wrong-old-root"
	running.PipelineHooks = []PipelineHookExecution{{HookName: "cleanup"}}
	recorder.RecordHookEffects(executionRecordSnapshot{Record: running, HookRevision: 3})
	require.True(t, recorder.RecordLifecycle(&ExecutionLifecycleObservation{State: RequestLifecycleFailed, Revision: 2}, time.Hour))
	recorder.Record(executionRecordSnapshot{Record: running, HookRevision: 1})
	recorder.RecordHookEffects(executionRecordSnapshot{Record: running, HookRevision: 2})
	wg.Wait()
	records, _ := store.snapshot()
	require.Len(t, records, 4)
	latest := records[len(records)-1]
	require.Equal(t, "root", latest.OriginalRequestID)
	require.Equal(t, "parent", latest.ParentRequestID)
	require.Equal(t, "step-17", latest.ParentStepID)
	require.Equal(t, RequestLifecycleFailed, latest.Lifecycle.State)
	require.Equal(t, "last-child-plan", latest.Plan.PlanID)
	require.Len(t, latest.PipelineHooks, 1)
	require.EqualValues(t, 4, latest.RecordRevision)
	summary := executionSummaryFromStored(latest)
	require.Equal(t, latest.ParentRequestID, summary.ParentRequestID)
	require.Equal(t, latest.ParentStepID, summary.ParentStepID)
}
