package telemetry

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// TelemetryCircuitBreaker protects telemetry backend from overload
type TelemetryCircuitBreaker struct {
	config CircuitConfig

	state           atomic.Value // string: "closed", "open", "half-open"
	failures        atomic.Int64
	successes       atomic.Int64
	lastFailureTime atomic.Value // time.Time

	mu sync.Mutex
}

// CircuitConfig configures the telemetry circuit breaker
type CircuitConfig struct {
	Enabled      bool
	MaxFailures  int
	RecoveryTime time.Duration
	HalfOpenMax  int // Max requests in half-open state
}

// NewTelemetryCircuitBreaker creates a new circuit breaker
func NewTelemetryCircuitBreaker(config CircuitConfig) *TelemetryCircuitBreaker {
	if !config.Enabled {
		return nil
	}

	// Set defaults
	if config.MaxFailures == 0 {
		config.MaxFailures = 10
	}
	if config.RecoveryTime == 0 {
		config.RecoveryTime = 30 * time.Second
	}
	if config.HalfOpenMax == 0 {
		config.HalfOpenMax = 5
	}

	cb := &TelemetryCircuitBreaker{
		config: config,
	}
	cb.state.Store("closed")
	cb.lastFailureTime.Store(time.Time{})

	return cb
}

// Allow checks if a request should be allowed
func (cb *TelemetryCircuitBreaker) Allow() bool {
	if cb == nil {
		return true // No circuit breaker configured
	}

	state := cb.State()

	switch state {
	case "open":
		// Check if we should transition to half-open
		lastFailureVal := cb.lastFailureTime.Load()
		if lastFailure, ok := lastFailureVal.(time.Time); ok && !lastFailure.IsZero() {
			if time.Since(lastFailure) > cb.config.RecoveryTime {
				cb.mu.Lock()
				// Double-check after acquiring lock
				if cb.state.Load().(string) == "open" {
					cb.state.Store("half-open")
					cb.successes.Store(0)

					// Log state transition to half-open
					GetLogger().Info("Circuit breaker entering HALF-OPEN state", map[string]interface{}{
						"previous_state":     "open",
						"recovery_wait":      cb.config.RecoveryTime.String(),
						"time_since_failure": time.Since(lastFailure).String(),
						"max_test_requests":  cb.config.HalfOpenMax,
						"action":             "Testing backend connectivity with limited requests",
						"impact":             fmt.Sprintf("Up to %d test requests will be allowed", cb.config.HalfOpenMax),
					})
				}
				cb.mu.Unlock()
				return true
			}
		}
		return false

	case "half-open":
		// Allow limited requests in half-open state
		currentRequests := cb.successes.Load()
		allowed := currentRequests < int64(cb.config.HalfOpenMax)

		// Log when we're rejecting requests in half-open
		if !allowed {
			GetLogger().Debug("Circuit breaker rejecting request in half-open state", map[string]interface{}{
				"current_tests": currentRequests,
				"max_tests":     cb.config.HalfOpenMax,
				"state":         "half-open",
			})
		}

		return allowed

	default: // closed
		return true
	}
}

// RecordSuccess records a successful operation.
// In half-open state, enough successes will close the circuit.
// In closed state, this resets the failure counter.
// This helps the circuit breaker recover after the backend is healthy again.
func (cb *TelemetryCircuitBreaker) RecordSuccess() {
	if cb == nil {
		return
	}

	cb.successes.Add(1)
	state := cb.State()

	switch state {
	case "half-open":
		successes := cb.successes.Load()

		// Log recovery progress
		GetLogger().Debug("Circuit breaker recovery test", map[string]interface{}{
			"successes": successes,
			"required":  cb.config.HalfOpenMax,
			"progress":  fmt.Sprintf("%d/%d", successes, cb.config.HalfOpenMax),
			"state":     "half-open",
		})

		// Check if we should close the circuit
		if successes >= int64(cb.config.HalfOpenMax) {
			cb.mu.Lock()
			if cb.state.Load().(string) == "half-open" {
				cb.state.Store("closed")
				cb.failures.Store(0)

				// Log successful recovery
				// Calculate recovery duration safely
				var recoveryDuration string
				if lastFailure, ok := cb.lastFailureTime.Load().(time.Time); ok && !lastFailure.IsZero() {
					recoveryDuration = time.Since(lastFailure).String()
				} else {
					recoveryDuration = "unknown"
				}

				GetLogger().Info("Circuit breaker CLOSED - Telemetry recovered", map[string]interface{}{
					"recovery_tests":    successes,
					"state":             "closed",
					"impact":            "Metrics emission resumed",
					"recovery_duration": recoveryDuration,
				})
			}
			cb.mu.Unlock()
		}
	case "closed":
		// Reset failure count on success in closed state
		cb.failures.Store(0)
	}
}

// RecordFailure records a failed operation
func (cb *TelemetryCircuitBreaker) RecordFailure() {
	if cb == nil {
		return
	}

	failures := cb.failures.Add(1)
	cb.lastFailureTime.Store(time.Now())

	// Log circuit breaker state changes and warnings at specific thresholds
	if failures >= int64(cb.config.MaxFailures) {
		cb.mu.Lock()
		if cb.state.Load().(string) != "open" {
			previousState := cb.state.Load().(string)
			cb.state.Store("open")
			cb.successes.Store(0)

			// Log circuit breaker opening - CRITICAL for operators
			GetLogger().Warn("Circuit breaker OPENED - Metrics will be dropped", map[string]interface{}{
				"previous_state": previousState,
				"failure_count":  failures,
				"max_failures":   cb.config.MaxFailures,
				"recovery_time":  cb.config.RecoveryTime.String(),
				"impact":         "All metrics will be dropped until recovery",
				"action":         "Check OTEL collector health at configured endpoint",
			})
		}
		cb.mu.Unlock()
	} else if failures == 1 {
		// Log first failure as info for early awareness
		GetLogger().Info("Circuit breaker recorded first failure", map[string]interface{}{
			"failure_count": 1,
			"max_failures":  cb.config.MaxFailures,
			"state":         cb.State(),
		})
	} else if cb.config.MaxFailures > 2 {
		// Calculate meaningful thresholds for warnings (only for MaxFailures > 2)
		halfwayPoint := (cb.config.MaxFailures + 1) / 2 // Better rounding for odd numbers

		if failures == int64(halfwayPoint) {
			// Warn when reaching halfway point (calculated correctly for odd/even)
			percentage := (failures * 100) / int64(cb.config.MaxFailures)
			GetLogger().Warn("Circuit breaker failures increasing", map[string]interface{}{
				"failure_count": failures,
				"max_failures":  cb.config.MaxFailures,
				"percentage":    percentage,
				"status":        "approaching_limit",
				"action":        "Investigate telemetry backend connectivity",
			})
		} else if failures == int64(cb.config.MaxFailures)-1 {
			// Critical warning one failure before opening
			GetLogger().Warn("Circuit breaker one failure from opening", map[string]interface{}{
				"failure_count": failures,
				"max_failures":  cb.config.MaxFailures,
				"status":        "critical",
				"impact":        "Next failure will open circuit breaker",
			})
		}
	}
}

// State returns the current circuit breaker state
func (cb *TelemetryCircuitBreaker) State() string {
	if cb == nil {
		return "disabled"
	}
	return cb.state.Load().(string)
}

// Reset resets the circuit breaker
func (cb *TelemetryCircuitBreaker) Reset() {
	if cb == nil {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	previousState := cb.state.Load().(string)
	previousFailures := cb.failures.Load()

	cb.state.Store("closed")
	cb.failures.Store(0)
	cb.successes.Store(0)
	cb.lastFailureTime.Store(time.Time{})

	// Log reset only if there was a state change or failures
	if previousState != "closed" || previousFailures > 0 {
		GetLogger().Info("Circuit breaker manually reset", map[string]interface{}{
			"previous_state":    previousState,
			"previous_failures": previousFailures,
			"state":             "closed",
			"action":            "Manual reset performed",
		})
	}
}
