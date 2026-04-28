package resilience

import (
	"context"
	"fmt"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// TelemetryMetrics implements MetricsCollector using the new telemetry API
type TelemetryMetrics struct{}

// NewTelemetryMetrics creates a metrics collector that uses the new telemetry API
func NewTelemetryMetrics() *TelemetryMetrics {
	return &TelemetryMetrics{}
}

// RecordSuccess records a successful circuit breaker execution
func (t *TelemetryMetrics) RecordSuccess(name string) {
	telemetry.Counter("circuit_breaker.calls", "name", name, "state", "success")
}

// RecordFailure records a failed circuit breaker execution
func (t *TelemetryMetrics) RecordFailure(name string, errorType string) {
	telemetry.Counter("circuit_breaker.calls", "name", name, "state", "failure")
	telemetry.Counter("circuit_breaker.failures", "name", name, "error_type", errorType)
}

// RecordStateChange records a circuit breaker state transition
func (t *TelemetryMetrics) RecordStateChange(name string, from, to string) {
	telemetry.Counter("circuit_breaker.state_changes",
		"name", name,
		"from_state", from,
		"to_state", to)

	// Also update the current state gauge
	stateValue := 0.0
	switch to {
	case "half-open":
		stateValue = 0.5
	case "open":
		stateValue = 1.0
	}
	telemetry.Gauge("circuit_breaker.current_state", stateValue, "name", name)
}

// RecordRejection records a request rejected by an open circuit
func (t *TelemetryMetrics) RecordRejection(name string) {
	telemetry.Counter("circuit_breaker.rejected", "name", name)
}

// ExecuteWithTelemetry wraps circuit breaker execution with telemetry
func ExecuteWithTelemetry(cb *CircuitBreaker, ctx context.Context, fn func() error) error {
	start := time.Now()

	// Emit the current state before execution
	state := cb.GetState()
	telemetry.Emit("circuit_breaker.calls", 1,
		"name", cb.config.Name,
		"state", string(state))

	// Execute the function
	err := cb.Execute(ctx, fn)

	// Record duration
	duration := float64(time.Since(start).Milliseconds())
	status := "success"
	if err != nil {
		status = "failure"
	}

	telemetry.Histogram("circuit_breaker.duration_ms", duration,
		"name", cb.config.Name,
		"status", status)

	return err
}

// Example of how to create a circuit breaker with telemetry integration
func NewCircuitBreakerWithTelemetry(name string) (*CircuitBreaker, error) {
	config := DefaultConfig()
	config.Name = name
	config.Metrics = NewTelemetryMetrics() // Use telemetry-based metrics

	return NewCircuitBreaker(config)
}

// RetryWithTelemetry performs retry with telemetry tracking
func RetryWithTelemetry(ctx context.Context, operation string, config *RetryConfig, fn func() error) error {
	if config == nil {
		config = DefaultRetryConfig()
	}
	maxAttempts := config.MaxAttempts
	start := time.Now()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Track attempt
		telemetry.Counter("retry.attempts",
			"operation", operation,
			"attempt_number", fmt.Sprintf("%d", attempt))

		// Execute the function
		err := fn()

		if err == nil {
			// Success
			telemetry.Counter("retry.success",
				"operation", operation,
				"final_attempt", fmt.Sprintf("%d", attempt))

			// Record total duration
			duration := float64(time.Since(start).Milliseconds())
			telemetry.Histogram("retry.duration_ms", duration,
				"operation", operation,
				"status", "success")

			return nil
		}

		// If this was the last attempt, record failure
		if attempt == maxAttempts {
			telemetry.Counter("retry.failures",
				"operation", operation,
				"error_type", fmt.Sprintf("%T", err))

			// Record total duration
			duration := float64(time.Since(start).Milliseconds())
			telemetry.Histogram("retry.duration_ms", duration,
				"operation", operation,
				"status", "failure")

			return err
		}

		// Calculate and apply backoff
		if attempt < maxAttempts {
			delay := config.InitialDelay * time.Duration(float64(attempt-1)*config.BackoffFactor)
			if delay > config.MaxDelay {
				delay = config.MaxDelay
			}

			// Record backoff duration
			telemetry.Histogram("retry.backoff_ms", float64(delay.Milliseconds()),
				"operation", operation,
				"strategy", "exponential")

			// Check context before sleeping
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}

	return fmt.Errorf("retry exhausted after %d attempts", maxAttempts)
}
