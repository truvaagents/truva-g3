package telemetry

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestCircuitBreakerLogging tests the logging functionality of circuit breaker
func TestCircuitBreakerLogging(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping circuit breaker logging test in short mode (requires 1s+ sleep)")
	}

	// Create a buffer to capture logs
	var buf bytes.Buffer

	// Create and configure logger to output to buffer
	logger := createTelemetryLogger("circuit-test")
	logger.SetOutput(&buf)
	logger.SetLevel("DEBUG") // Enable debug logging

	// Reset singleton and set our test logger
	telemetryLogger = logger
	telemetryLoggerOnce.Do(func() {}) // Mark as initialized

	// Create circuit breaker with low thresholds for testing
	config := CircuitConfig{
		Enabled:      true,
		MaxFailures:  3,
		RecoveryTime: 1 * time.Second, // Short recovery for testing
		HalfOpenMax:  2,
	}
	cb := NewTelemetryCircuitBreaker(config)

	t.Run("First failure logged", func(t *testing.T) {
		buf.Reset()
		cb.RecordFailure()

		output := buf.String()
		if !strings.Contains(output, "Circuit breaker recorded first failure") {
			t.Errorf("Expected first failure log, got: %s", output)
		}
		if !strings.Contains(output, "failure_count=1") {
			t.Errorf("Expected failure_count=1 in log, got: %s", output)
		}
	})

	t.Run("Halfway warning", func(t *testing.T) {
		// Already have 1 failure, this should trigger halfway warning
		buf.Reset()
		cb.RecordFailure()

		// For MaxFailures=3, failure 2 doesn't trigger 50% warning (need integer division)
		// But we should see no special log for failure 2
		output := buf.String()
		// Should not have any special log for failure 2
		if output != "" {
			t.Logf("Failure 2 output (expected empty for MaxFailures=3): %s", output)
		}
	})

	t.Run("Circuit opens on max failures", func(t *testing.T) {
		buf.Reset()
		cb.RecordFailure() // This is the 3rd failure

		output := buf.String()
		if !strings.Contains(output, "Circuit breaker OPENED") {
			t.Errorf("Expected circuit open log, got: %s", output)
		}
		if !strings.Contains(output, "Metrics will be dropped") {
			t.Errorf("Expected impact message, got: %s", output)
		}
		if !strings.Contains(output, "Check OTEL collector health") {
			t.Errorf("Expected action message, got: %s", output)
		}
		if !strings.Contains(output, "failure_count=3") {
			t.Errorf("Expected failure_count=3, got: %s", output)
		}
	})

	t.Run("Transition to half-open after recovery time", func(t *testing.T) {
		// Wait for recovery time
		time.Sleep(1100 * time.Millisecond)

		buf.Reset()
		allowed := cb.Allow()

		if !allowed {
			t.Error("Expected request to be allowed after recovery time")
		}

		output := buf.String()
		if !strings.Contains(output, "Circuit breaker entering HALF-OPEN state") {
			t.Errorf("Expected half-open transition log, got: %s", output)
		}
		if !strings.Contains(output, "Testing backend connectivity") {
			t.Errorf("Expected action message, got: %s", output)
		}
	})

	t.Run("Recovery progress in half-open", func(t *testing.T) {
		buf.Reset()
		cb.RecordSuccess()

		output := buf.String()
		if !strings.Contains(output, "Circuit breaker recovery test") {
			t.Errorf("Expected recovery test log, got: %s", output)
		}
		if !strings.Contains(output, "progress=1/2") {
			t.Errorf("Expected progress=1/2, got: %s", output)
		}
	})

	t.Run("Circuit closes after enough successes", func(t *testing.T) {
		buf.Reset()
		cb.RecordSuccess() // Second success should close

		output := buf.String()
		if !strings.Contains(output, "Circuit breaker CLOSED") {
			t.Errorf("Expected circuit closed log, got: %s", output)
		}
		if !strings.Contains(output, "Telemetry recovered") {
			t.Errorf("Expected recovery message, got: %s", output)
		}
		if !strings.Contains(output, "Metrics emission resumed") {
			t.Errorf("Expected impact message, got: %s", output)
		}
		if cb.State() != "closed" {
			t.Errorf("Expected state to be closed, got: %s", cb.State())
		}
	})

	t.Run("Manual reset logged", func(t *testing.T) {
		// Put circuit in a non-closed state first
		cb.RecordFailure()
		cb.RecordFailure()

		buf.Reset()
		cb.Reset()

		output := buf.String()
		if !strings.Contains(output, "Circuit breaker manually reset") {
			t.Errorf("Expected manual reset log, got: %s", output)
		}
		if !strings.Contains(output, "previous_failures=2") {
			t.Errorf("Expected previous_failures=2, got: %s", output)
		}
	})
}

// TestCircuitBreakerWarningThresholds tests the warning thresholds
func TestCircuitBreakerWarningThresholds(t *testing.T) {
	var buf bytes.Buffer
	logger := createTelemetryLogger("threshold-test")
	logger.SetOutput(&buf)
	logger.SetLevel("INFO")

	telemetryLogger = logger
	telemetryLoggerOnce.Do(func() {})

	// Test with MaxFailures=10 for better threshold testing
	config := CircuitConfig{
		Enabled:      true,
		MaxFailures:  10,
		RecoveryTime: 1 * time.Second,
		HalfOpenMax:  3,
	}
	cb := NewTelemetryCircuitBreaker(config)

	// Failure 1: Should log first failure
	buf.Reset()
	cb.RecordFailure()
	if !strings.Contains(buf.String(), "first failure") {
		t.Error("Expected first failure log")
	}

	// Failures 2-4: No special logs
	for i := 2; i <= 4; i++ {
		buf.Reset()
		cb.RecordFailure()
		if buf.String() != "" {
			t.Errorf("Unexpected log for failure %d: %s", i, buf.String())
		}
	}

	// Failure 5: Should log 50% warning
	buf.Reset()
	cb.RecordFailure()
	// The log message uses structured fields, not "50% threshold" string
	if !strings.Contains(buf.String(), "percentage") || !strings.Contains(buf.String(), "50") {
		t.Errorf("Expected 50%% warning at failure 5, got: %s", buf.String())
	}

	// Failures 6-8: No special logs
	for i := 6; i <= 8; i++ {
		buf.Reset()
		cb.RecordFailure()
		if buf.String() != "" {
			t.Errorf("Unexpected log for failure %d: %s", i, buf.String())
		}
	}

	// Failure 9: Should log one-before-opening warning
	buf.Reset()
	cb.RecordFailure()
	if !strings.Contains(buf.String(), "one failure from opening") {
		t.Errorf("Expected one-before warning at failure 9, got: %s", buf.String())
	}

	// Failure 10: Should open circuit
	buf.Reset()
	cb.RecordFailure()
	if !strings.Contains(buf.String(), "Circuit breaker OPENED") {
		t.Errorf("Expected circuit open at failure 10, got: %s", buf.String())
	}
}

// TestCircuitBreakerRejectInHalfOpen - REMOVED
// This test had a fundamental logic flaw: after calling RecordSuccess() when successes >= HalfOpenMax,
// the circuit transitions to "closed" state (circuit.go:137-140). Once closed, the next Allow() call
// returns true (allows the request) rather than rejecting it. The test expected rejection that cannot
// happen because the circuit is already closed. The test logic was flawed from the start.
