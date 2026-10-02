package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

func TestTerminalCorrelationCopiesMetadata(t *testing.T) {
	if executionWithRequestID(nil, "request") != nil {
		t.Fatal("fabricated execution")
	}
	for _, metadata := range []map[string]interface{}{nil, {"application": "preserved", MetadataKeyExecutionRequestID: "original"}} {
		base := &ExecutionResult{Success: true, Metadata: metadata}
		result := executionWithRequestID(base, "resumed-segment")
		if result.Metadata[MetadataKeyExecutionRequestID] != "resumed-segment" || !result.Success {
			t.Fatalf("result: %+v", result)
		}
		result.Metadata["new"] = true
		if _, found := base.Metadata["new"]; found {
			t.Fatal("phase metadata aliased")
		}
		if metadata != nil && (metadata[MetadataKeyExecutionRequestID] != "original" || result.Metadata["application"] != "preserved") {
			t.Fatal("metadata ownership changed")
		}
	}
}

func TestTerminalCorrelationReturnAndEarlyPublisher(t *testing.T) {
	for _, reason := range []string{"success", "synthesis_failed", "execution_failed", "interrupted", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			store := &terminalRecordStore{NoOpExecutionStore: NewNoOpExecutionStore()}
			o := setupTestOrchestrator(t, &mockAIClient{})
			o.executionStore = store
			holder := newPipelineHookExecutionHolder()
			ctx := withPipelineHookExecutionHolder(t.Context(), holder)
			ctx = telemetry.WithBaggage(ctx, "original_request_id", "original")
			base := &ExecutionResult{Success: reason == "success", Metadata: map[string]interface{}{"application": "preserved"}}
			state := &executionRunState{Context: ctx, StartedAt: time.Now(), Correlation: requestCorrelation{RequestID: "segment"}, completionReason: reason}
			state.Phase.Result = &phaseLoopResult{CombinedResult: base}
			if reason == "execution_failed" || reason == "interrupted" {
				o.storeTerminalExecutionAsync(ctx, "request", "segment", nil, base, nil)
			}
			var runErr error
			if reason != "success" {
				runErr = errors.New(reason)
			}
			result := o.finishRequestExecution(state, nil, runErr)
			if result.Execution.Metadata[MetadataKeyExecutionRequestID] != "segment" {
				t.Fatal("return lacks owning segment")
			}
			if _, found := base.Metadata[MetadataKeyExecutionRequestID]; found {
				t.Fatal("phase snapshot changed")
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := o.Shutdown(shutdownCtx); err != nil {
				t.Fatal(err)
			}
			records, _ := store.snapshot()
			if len(records) != 1 {
				t.Fatalf("competing terminal records: %d", len(records))
			}
			if records[0].Result.Metadata[MetadataKeyExecutionRequestID] != "segment" || records[0].OriginalRequestID != "original" {
				t.Fatalf("stored identity: %+v", records[0])
			}
		})
	}
}
