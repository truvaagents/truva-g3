package orchestration

import "testing"

// TestEmitContinuationBudgetMetrics_NoOpSafe asserts the Phase-14 continuation metrics emit
// without panicking across all branches. Telemetry is uninitialized in unit tests, so every
// emission must be a safe NoOp (FDP Built-in Telemetry Requirements: never fail operations due
// to telemetry). This exercises the eviction and escalation counter loops, the empty case, and
// the no-eviction/no-escalation case.
func TestEmitContinuationBudgetMetrics_NoOpSafe(t *testing.T) {
	cases := []struct {
		name                     string
		total, shown, esc, chars int
	}{
		{"eviction and escalation", 10, 6, 2, 13396},
		{"empty", 0, 0, 0, 0},
		{"no eviction no escalation", 5, 5, 0, 1000},
		{"eviction only", 3, 1, 0, 500},
		{"escalation only", 4, 4, 3, 2000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			emitContinuationBudgetMetrics(c.total, c.shown, c.esc, c.chars)
		})
	}
}
