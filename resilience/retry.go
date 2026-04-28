package resilience

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// RetryConfig configures retry behavior
type RetryConfig struct {
	MaxAttempts   int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	BackoffFactor float64
	JitterEnabled bool
}

// DefaultRetryConfig provides sensible defaults
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxAttempts:   3,
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      5 * time.Second,
		BackoffFactor: 2.0,
		JitterEnabled: true,
	}
}

// Retry executes a function with retry logic (updated to use logging when possible)
func Retry(ctx context.Context, config *RetryConfig, fn func() error) error {
	// Create executor with default logger for backward compatibility
	executor := NewRetryExecutor(config)
	// Use operation name "unknown" for backward compatibility
	return executor.Execute(ctx, "unknown", fn)
}

// RetryWithCircuitBreaker combines retry logic with circuit breaker
func RetryWithCircuitBreaker(ctx context.Context, config *RetryConfig, cb *CircuitBreaker, fn func() error) error {
	return Retry(ctx, config, func() error {
		if !cb.CanExecute() {
			return core.ErrCircuitBreakerOpen
		}

		err := fn()
		if err != nil {
			cb.RecordFailure()
			return err
		}

		cb.RecordSuccess()
		return nil
	})
}

// RetryExecutor struct for dependency injection (follows framework pattern)
type RetryExecutor struct {
	config           *RetryConfig
	logger           core.Logger
	telemetryEnabled bool
}

// NewRetryExecutor creates a new retry executor following framework pattern
func NewRetryExecutor(config *RetryConfig) *RetryExecutor {
	if config == nil {
		config = DefaultRetryConfig()
	}
	return &RetryExecutor{
		config: config,
		logger: &core.NoOpLogger{}, // Default to no-op
	}
}

// SetLogger sets the logger for dependency injection (follows framework design principles)
// The component is always set to "framework/resilience" to ensure proper log attribution
// regardless of which agent or tool is using the resilience module.
func (r *RetryExecutor) SetLogger(logger core.Logger) {
	if logger == nil {
		r.logger = &core.NoOpLogger{}
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			r.logger = cal.WithComponent("framework/resilience")
		} else {
			r.logger = logger
		}
	}
}

// Execute runs a function with comprehensive retry logic and logging
func (r *RetryExecutor) Execute(ctx context.Context, operation string, fn func() error) error {
	startTime := time.Now()

	// Log retry operation start
	if r.logger != nil {
		r.logger.InfoWithContext(ctx, "Starting retry operation", map[string]interface{}{
			"operation":       "retry_start",
			"retry_operation": operation,
			"max_attempts":    r.config.MaxAttempts,
			"initial_delay":   r.config.InitialDelay.String(),
			"max_delay":       r.config.MaxDelay.String(),
			"backoff_factor":  r.config.BackoffFactor,
			"jitter_enabled":  r.config.JitterEnabled,
		})
	}

	var lastErr error
	delay := r.config.InitialDelay

	for attempt := 1; attempt <= r.config.MaxAttempts; attempt++ {
		attemptStart := time.Now()

		// Emit attempt metric
		if r.telemetryEnabled {
			telemetry.Counter("retry.attempts",
				"operation", operation,
				"attempt_number", fmt.Sprintf("%d", attempt))
		}

		// Log attempt start
		if r.logger != nil {
			r.logger.DebugWithContext(ctx, "Starting retry attempt", map[string]interface{}{
				"operation":       "retry_attempt_start",
				"retry_operation": operation,
				"attempt":         attempt,
				"max_attempts":    r.config.MaxAttempts,
				"current_delay":   delay.String(),
			})
		}

		// Check context
		select {
		case <-ctx.Done():
			// Log context cancellation
			if r.logger != nil {
				r.logger.InfoWithContext(ctx, "Retry operation cancelled by context", map[string]interface{}{
					"operation":       "retry_cancelled",
					"retry_operation": operation,
					"attempt":         attempt,
					"reason":          ctx.Err().Error(),
					"duration_ms":     time.Since(startTime).Milliseconds(),
				})
			}
			return ctx.Err()
		default:
		}

		// Try the function
		if err := fn(); err == nil {
			// Emit success metric
			if r.telemetryEnabled {
				telemetry.Counter("retry.success",
					"operation", operation,
					"final_attempt", fmt.Sprintf("%d", attempt))

				telemetry.Histogram("retry.duration_ms",
					float64(time.Since(startTime).Milliseconds()),
					"operation", operation,
					"status", "success")
			}

			// Log success
			if r.logger != nil {
				r.logger.InfoWithContext(ctx, "Retry operation succeeded", map[string]interface{}{
					"operation":           "retry_success",
					"retry_operation":     operation,
					"successful_attempt":  attempt,
					"total_attempts":      attempt,
					"total_duration_ms":   time.Since(startTime).Milliseconds(),
					"attempt_duration_ms": time.Since(attemptStart).Milliseconds(),
				})
			}
			return nil
		} else {
			lastErr = err

			// Log attempt failure
			logLevel := "Debug" // Use DEBUG for retry attempts
			if attempt == r.config.MaxAttempts {
				logLevel = "Warn" // Use WARN for final failure
			}

			logData := map[string]interface{}{
				"operation":           "retry_attempt_failed",
				"retry_operation":     operation,
				"attempt":             attempt,
				"max_attempts":        r.config.MaxAttempts,
				"error":               err.Error(),
				"error_type":          fmt.Sprintf("%T", err),
				"will_retry":          attempt < r.config.MaxAttempts,
				"attempt_duration_ms": time.Since(attemptStart).Milliseconds(),
			}

			if r.logger != nil {
				if logLevel == "Warn" {
					r.logger.WarnWithContext(ctx, "Retry attempt failed (final)", logData)
				} else {
					r.logger.DebugWithContext(ctx, "Retry attempt failed, will retry", logData)
				}
			}
		}

		// Don't sleep after the last attempt
		if attempt == r.config.MaxAttempts {
			break
		}

		// Calculate next delay with exponential backoff
		if attempt > 1 {
			delay = time.Duration(float64(delay) * r.config.BackoffFactor)
			if delay > r.config.MaxDelay {
				delay = r.config.MaxDelay
			}
		}

		// Add jitter if enabled
		originalDelay := delay
		if r.config.JitterEnabled {
			jitter := time.Duration(float64(delay) * 0.1 * math.Sin(float64(attempt)))
			delay += jitter
		}

		// Emit backoff metric
		if r.telemetryEnabled {
			telemetry.Histogram("retry.backoff_ms",
				float64(delay.Milliseconds()),
				"operation", operation,
				"strategy", "exponential")
		}

		// Log backoff
		if r.logger != nil {
			r.logger.DebugWithContext(ctx, "Applying retry backoff", map[string]interface{}{
				"operation":         "retry_backoff",
				"retry_operation":   operation,
				"attempt":           attempt,
				"next_attempt":      attempt + 1,
				"original_delay_ms": originalDelay.Milliseconds(),
				"jitter_applied":    r.config.JitterEnabled,
				"final_delay_ms":    delay.Milliseconds(),
				"backoff_factor":    r.config.BackoffFactor,
			})
		}

		// Sleep with context cancellation
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			// Log context cancellation during backoff
			if r.logger != nil {
				r.logger.InfoWithContext(ctx, "Retry operation cancelled during backoff", map[string]interface{}{
					"operation":       "retry_cancelled_backoff",
					"retry_operation": operation,
					"attempt":         attempt,
					"reason":          ctx.Err().Error(),
				})
			}
			return ctx.Err()
		case <-timer.C:
		}
	}

	// Emit final failure metric
	if r.telemetryEnabled {
		errorType := "no_error"
		if lastErr != nil {
			errorType = fmt.Sprintf("%T", lastErr)
		}

		telemetry.Counter("retry.failures",
			"operation", operation,
			"error_type", errorType)

		telemetry.Histogram("retry.duration_ms",
			float64(time.Since(startTime).Milliseconds()),
			"operation", operation,
			"status", "failure")
	}

	// Log final failure
	if r.logger != nil {
		logData := map[string]interface{}{
			"operation":         "retry_exhausted",
			"retry_operation":   operation,
			"total_attempts":    r.config.MaxAttempts,
			"total_duration_ms": time.Since(startTime).Milliseconds(),
		}

		// Handle case where lastErr might be nil (e.g., MaxAttempts = 0)
		if lastErr != nil {
			logData["final_error"] = lastErr.Error()
			logData["final_error_type"] = fmt.Sprintf("%T", lastErr)
		} else {
			logData["final_error"] = "no attempts made"
			logData["final_error_type"] = "no_error"
		}

		r.logger.ErrorWithContext(ctx, "Retry operation failed after all attempts", logData)
	}

	return fmt.Errorf("max retry attempts (%d) exceeded for %v: %w", r.config.MaxAttempts, lastErr, core.ErrMaxRetriesExceeded)
}

// RetryWithLogging provides backward compatibility function with logging
func RetryWithLogging(ctx context.Context, operation string, config *RetryConfig, logger core.Logger, fn func() error) error {
	executor := NewRetryExecutor(config)
	if logger != nil {
		executor.SetLogger(logger)
	}
	return executor.Execute(ctx, operation, fn)
}
