package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestCheckpointCoordinator_SaveAuthoritativeMakesPendingAndCopiesSnapshot(t *testing.T) {
	store := newMockCheckpointStore()
	controller := NewInterruptController(nil, store, nil)
	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "checkpoint-ready",
		Status:       CheckpointStatusPreparing,
		StepResults:  make(map[string]*StepResult),
	}
	source := &StepResult{
		StepID: "step-1", Success: true, Response: "original",
		Parameters: map[string]interface{}{"nested": map[string]interface{}{"value": "original"}},
		Metadata:   map[string]interface{}{"items": []interface{}{map[string]interface{}{"value": "original"}}},
	}
	snapshot := newExecutionRunSnapshot(2, map[string]*StepResult{"step-1": source}, []string{"step-1"}, "continue")

	coordinator := checkpointCoordinator{controller: controller}
	if err := coordinator.saveAuthoritative(context.Background(), checkpoint, snapshot); err != nil {
		t.Fatalf("saveAuthoritative() error = %v", err)
	}
	source.Response = "mutated"
	source.Parameters["nested"].(map[string]interface{})["value"] = "mutated"
	source.Metadata["items"].([]interface{})[0].(map[string]interface{})["value"] = "mutated"

	stored := store.checkpoints[checkpoint.CheckpointID]
	if stored == nil || stored.Status != CheckpointStatusPending {
		t.Fatalf("stored checkpoint status = %#v", stored)
	}
	if stored.PhaseNumber != 2 || stored.AccumulatedResults["step-1"].Response != "original" {
		t.Fatalf("stored snapshot = %#v", stored)
	}
	storedStep := stored.AccumulatedResults["step-1"]
	if storedStep.Parameters["nested"].(map[string]interface{})["value"] != "original" ||
		storedStep.Metadata["items"].([]interface{})[0].(map[string]interface{})["value"] != "original" {
		t.Fatalf("stored snapshot retained nested mutable aliases: %#v", storedStep)
	}
}

func TestCheckpointCoordinator_LegacyControllerRequiresEnrichmentOnlyForDurableState(t *testing.T) {
	coordinator := checkpointCoordinator{controller: newMockInterruptController()}
	legacy := &ExecutionCheckpoint{CheckpointID: "legacy"}
	if err := coordinator.saveAuthoritative(context.Background(), legacy, executionRunSnapshot{PhaseNumber: 1}); err != nil {
		t.Fatalf("simple legacy checkpoint error = %v", err)
	}

	required := &ExecutionCheckpoint{CheckpointID: "required"}
	err := coordinator.saveAuthoritative(context.Background(), required, executionRunSnapshot{PhaseNumber: 2})
	if !errors.Is(err, ErrCheckpointEnrichmentUnsupported) {
		t.Fatalf("durable legacy checkpoint error = %v", err)
	}
}

func TestBuildResumeContext_RejectsPreparingCheckpoint(t *testing.T) {
	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "checkpoint-incomplete",
		Status:       CheckpointStatusPreparing,
	}
	if _, _, err := BuildResumeContext(context.Background(), checkpoint); err == nil {
		t.Fatal("BuildResumeContext() accepted a preparing checkpoint")
	}
}

func TestCheckpointCoordinator_DefersNotificationUntilAuthoritativeSave(t *testing.T) {
	policy := &mockPolicy{planDecision: &InterruptDecision{
		ShouldInterrupt: true,
		Reason:          ReasonSensitiveOperation,
	}}
	store := newMockCheckpointStore()
	handler := &mockInterruptHandler{}
	controller := NewInterruptController(policy, store, handler)
	ctx := withCheckpointEnrichmentRequired(context.Background())

	checkpoint, err := controller.CheckPlanApproval(ctx, &RoutingPlan{PlanID: "plan-1"})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Status != CheckpointStatusPreparing || handler.notifyCalls != 0 {
		t.Fatalf("initial checkpoint status=%q notifications=%d", checkpoint.Status, handler.notifyCalls)
	}

	coordinator := checkpointCoordinator{controller: controller}
	if err := coordinator.saveAuthoritative(ctx, checkpoint, executionRunSnapshot{PhaseNumber: 1}); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Status != CheckpointStatusPending || handler.notifyCalls != 1 {
		t.Fatalf("authoritative checkpoint status=%q notifications=%d", checkpoint.Status, handler.notifyCalls)
	}
}

func TestCheckpointCoordinatorFailureLogUsesBoundedClassification(t *testing.T) {
	logger := &TestLogger{}
	orchestrator := &AIOrchestrator{
		interruptController: newMockInterruptController(),
		logger:              logger,
	}
	ctx := telemetry.WithBaggage(t.Context(), "request_id", "request-checkpoint")
	ctx = WithRequestID(ctx, "request-checkpoint")
	checkpoint := &ExecutionCheckpoint{CheckpointID: "checkpoint-failure"}

	err := orchestrator.saveAuthoritativeCheckpoint(
		ctx,
		checkpoint,
		executionRunSnapshot{PhaseNumber: 2},
		"plan_level",
	)
	if !errors.Is(err, ErrCheckpointEnrichmentUnsupported) {
		t.Fatalf("saveAuthoritativeCheckpoint() error = %v", err)
	}
	logs := logger.GetLogsByOperation("checkpoint_enrichment")
	if len(logs) != 1 || logs[0].Level != "WARN" ||
		logs[0].Fields["request_id"] != "request-checkpoint" ||
		logs[0].Fields["status"] != "error" || logs[0].Fields["reason"] != "unsupported" ||
		logs[0].Fields["error_type"] != "preparation" ||
		logs[0].Fields["error"] != ErrCheckpointEnrichmentUnsupported.Error() {
		t.Fatalf("checkpoint enrichment failure log = %#v", logs)
	}
}

func TestAuthoritativeCheckpointNotificationFailureIsCorrelatedAndSanitized(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	logger := &TestLogger{}
	handler := &mockInterruptHandler{notifyError: errors.New("webhook secret token leaked")}
	controller := NewInterruptController(nil, newMockCheckpointStore(), handler, WithControllerLogger(logger))
	ctx := withCheckpointEnrichmentRequired(t.Context())
	ctx = WithRequestID(ctx, "request-notification")
	ctx, span := provider.Tracer("checkpoint-coordinator-test").Start(ctx, "request")
	checkpoint := &ExecutionCheckpoint{
		CheckpointID:      "checkpoint-notification",
		RequestID:         "request-suspended",
		OriginalRequestID: "request-original",
		Decision:          &InterruptDecision{Reason: ReasonPlanApproval},
	}

	if err := controller.SaveEnrichedCheckpoint(ctx, checkpoint); err != nil {
		t.Fatalf("SaveEnrichedCheckpoint() error = %v", err)
	}
	span.End()
	logs := logger.GetLogsByOperation("hitl_notify_interrupt")
	if len(logs) != 1 || logs[0].Level != "WARN" ||
		logs[0].Fields["request_id"] != "request-notification" ||
		logs[0].Fields["original_request_id"] != "request-original" ||
		logs[0].Fields["status"] != "error" || logs[0].Fields["error_type"] != "notification" ||
		logs[0].Fields["error"] != "interrupt notification failed" {
		t.Fatalf("notification failure log = %#v", logs)
	}
	var eventFound bool
	for _, event := range recorder.Ended()[0].Events() {
		if event.Name != "hitl.notification.failed" {
			continue
		}
		eventFound = true
		if len(event.Attributes) == 0 || event.Attributes[0].Key != "request_id" ||
			event.Attributes[0].Value.AsString() != "request-notification" {
			t.Fatalf("notification event correlation = %#v", event.Attributes)
		}
		for _, attr := range event.Attributes {
			if attr.Key == "error" || strings.Contains(attr.Value.AsString(), "secret") {
				t.Fatalf("notification event exposed handler error: %#v", event.Attributes)
			}
		}
	}
	if !eventFound {
		t.Fatal("hitl.notification.failed event was not emitted")
	}
}

func TestCheckpointFailureErrorType(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{err: ErrCheckpointEnrichmentUnsupported, want: "preparation"},
		{err: ErrCheckpointAuthoritativeSave, want: "store_write"},
		{err: errors.New("unexpected"), want: "unknown"},
	} {
		if got := checkpointFailureErrorType(test.err); got != test.want {
			t.Fatalf("checkpointFailureErrorType(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}
