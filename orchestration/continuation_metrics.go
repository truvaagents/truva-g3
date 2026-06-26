package orchestration

import "github.com/truvaagents/truva-g3/telemetry"

// emitContinuationBudgetMetrics records aggregate metrics for the Phase-14 continuation
// <completed_steps> budgeting outcome, complementing the per-request "orchestrator.continuation_budget"
// span event with module-owned metrics (telemetry/ARCHITECTURE.md §4 — each module owns its metrics and
// emits them via the primitive API with the ModuleOrchestration label):
//
//   - section_chars / steps_shown — Histograms for the rendered section size and the number of
//     completed steps kept, so dashboards can see the budget-usage and fill distributions.
//   - steps_evicted / c_escalations — Counters (one increment per evicted step / per C escalation,
//     mirroring conversation_history's per-dropped-turn counter), so eviction pressure and how often
//     the continuation distiller fires are alertable in aggregate, not just per-request in traces.
//
// All emissions are NoOp-safe when telemetry is uninitialized (the package-level helpers load the
// global registry atomically and discard when nil), per the FDP Built-in Telemetry Requirements.
func emitContinuationBudgetMetrics(stepsTotal, stepsShown, cEscalations, sectionChars int) {
	telemetry.Histogram("orchestration.continuation.section_chars", float64(sectionChars),
		"module", telemetry.ModuleOrchestration,
	)
	telemetry.Histogram("orchestration.continuation.steps_shown", float64(stepsShown),
		"module", telemetry.ModuleOrchestration,
	)

	for i := 0; i < stepsTotal-stepsShown; i++ {
		telemetry.Counter("orchestration.continuation.steps_evicted",
			"module", telemetry.ModuleOrchestration,
		)
	}

	for i := 0; i < cEscalations; i++ {
		telemetry.Counter("orchestration.continuation.c_escalations",
			"module", telemetry.ModuleOrchestration,
		)
	}
}
