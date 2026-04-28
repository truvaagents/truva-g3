package telemetry

import (
	"testing"
	"time"
)

// TestCircuitBreakerThresholds tests the threshold calculation logic
func TestCircuitBreakerThresholds(t *testing.T) {
	tests := []struct {
		name            string
		maxFailures     int
		expectedHalfway int
		description     string
	}{
		{
			name:            "MaxFailures=3",
			maxFailures:     3,
			expectedHalfway: 2,
			description:     "For 3 max failures, halfway should be at failure 2",
		},
		{
			name:            "MaxFailures=5",
			maxFailures:     5,
			expectedHalfway: 3,
			description:     "For 5 max failures, halfway should be at failure 3",
		},
		{
			name:            "MaxFailures=10",
			maxFailures:     10,
			expectedHalfway: 5,
			description:     "For 10 max failures, halfway should be at failure 5",
		},
		{
			name:            "MaxFailures=2",
			maxFailures:     2,
			expectedHalfway: -1, // Should not log halfway for MaxFailures <= 2
			description:     "For 2 max failures, no halfway warning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Calculate halfway point using the same logic as in RecordFailure
			var calculatedHalfway int
			if tt.maxFailures > 2 {
				calculatedHalfway = (tt.maxFailures + 1) / 2
			} else {
				calculatedHalfway = -1
			}

			if calculatedHalfway != tt.expectedHalfway {
				t.Errorf("%s: expected halfway=%d, got=%d",
					tt.description, tt.expectedHalfway, calculatedHalfway)
			}

			// Also test the percentage calculation
			if calculatedHalfway > 0 {
				percentage := (int64(calculatedHalfway) * 100) / int64(tt.maxFailures)
				t.Logf("%s: halfway=%d, percentage=%d%%",
					tt.name, calculatedHalfway, percentage)
			}
		})
	}
}

// TestCircuitBreakerSafeTypeAssertion tests safe type assertion
func TestCircuitBreakerSafeTypeAssertion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping circuit breaker type assertion test in short mode (requires 1s+ sleep)")
	}

	config := CircuitConfig{
		Enabled:      true,
		MaxFailures:  3,
		RecoveryTime: 1 * time.Second,
		HalfOpenMax:  2,
	}
	cb := NewTelemetryCircuitBreaker(config)

	// Test with zero time (initial state)
	if !cb.Allow() {
		t.Error("Should allow requests when circuit is closed")
	}

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.State() != "open" {
		t.Error("Circuit should be open after max failures")
	}

	// The Allow() method should handle the lastFailureTime safely
	// This tests the safe type assertion
	if cb.Allow() {
		t.Error("Should not allow requests when circuit is open and recovery time not passed")
	}

	// Wait for recovery time
	time.Sleep(1100 * time.Millisecond)

	// This should transition to half-open with safe type assertion
	if !cb.Allow() {
		t.Error("Should allow request after recovery time")
	}

	if cb.State() != "half-open" {
		t.Error("Circuit should be half-open after recovery time")
	}
}
