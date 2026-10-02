package main

import (
	"testing"

	"github.com/truvaagents/truva-g3/orchestration"
)

func TestExecutionLifecycleProjection(t *testing.T) {
	for _, state := range []string{"running", "completed", "failed", "cancelled", "suspended", ""} {
		t.Run(state, func(t *testing.T) {
			execution := &StoredExecution{RequestID: "request", RecordRevision: 4, Result: &ExecutionResult{Success: true}}
			if state != "" {
				execution.Lifecycle = &orchestration.ExecutionLifecycleObservation{State: state, Revision: 2}
			}
			summary := summarizeExecution(execution)
			// Building the unified view may enrich from optional backends; the
			// core lifecycle must always be forwarded without deriving success.
			if summary.Lifecycle != execution.Lifecycle || summary.RecordRevision != 4 {
				t.Fatal("summary lost producer evidence")
			}
			for _, filter := range []string{"success", "failed"} {
				want := (filter == "success" && state == "completed") || (filter == "failed" && (state == "failed" || state == "cancelled"))
				if got := executionMatchesGroupedQuery(summary, "", &groupedExecutionQuery{Status: filter}); got != want {
					t.Fatalf("%s filter=%v, want %v", filter, got, want)
				}
			}
		})
	}
}
