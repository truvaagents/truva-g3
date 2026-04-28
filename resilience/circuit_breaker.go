package resilience

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// CircuitState represents the state of the circuit breaker
type CircuitState int

const (
	// StateClosed allows all requests through
	StateClosed CircuitState = iota
	// StateOpen blocks all requests
	StateOpen
	// StateHalfOpen allows limited requests for testing
	StateHalfOpen
)

// String returns the string representation of the state
func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// MetricsCollector interface for circuit breaker metrics
type MetricsCollector interface {
	RecordSuccess(name string)
	RecordFailure(name string, errorType string)
	RecordStateChange(name string, from, to string)
	RecordRejection(name string)
}

// noopMetrics is a no-op metrics implementation
type noopMetrics struct{}

func (n *noopMetrics) RecordSuccess(name string)                      {}
func (n *noopMetrics) RecordFailure(name string, errorType string)    {}
func (n *noopMetrics) RecordStateChange(name string, from, to string) {}
func (n *noopMetrics) RecordRejection(name string)                    {}

// ErrorClassifier determines which errors should count toward circuit breaker thresholds
type ErrorClassifier func(error) bool

// DefaultErrorClassifier only counts infrastructure errors, not user errors
func DefaultErrorClassifier(err error) bool {
	if err == nil {
		return false
	}

	// Configuration errors - DON'T count (user error)
	if core.IsConfigurationError(err) {
		return false
	}

	// Not found errors - DON'T count (user error)
	if core.IsNotFound(err) {
		return false
	}

	// State errors - DON'T count (programming error)
	if core.IsStateError(err) {
		return false
	}

	// Context cancellation - DON'T count (client gave up)
	if errors.Is(err, context.Canceled) || errors.Is(err, core.ErrContextCanceled) {
		return false
	}

	// All other errors count as failures (network, timeout, connection issues)
	return true
}

// CircuitBreakerConfig holds configuration for the circuit breaker
type CircuitBreakerConfig struct {
	// Name identifies the circuit breaker
	Name string

	// FailureThreshold is the number of failures before opening (deprecated, use ErrorThreshold)
	FailureThreshold int

	// RecoveryTimeout is how long to wait before attempting recovery (deprecated, use SleepWindow)
	RecoveryTimeout time.Duration

	// ErrorThreshold is the error rate (0.0 to 1.0) that triggers opening
	ErrorThreshold float64

	// VolumeThreshold is the minimum number of requests before evaluation
	VolumeThreshold int

	// SleepWindow is how long to wait before entering half-open state
	SleepWindow time.Duration

	// HalfOpenRequests is the number of test requests in half-open state
	HalfOpenRequests int

	// SuccessThreshold is the success rate needed to close from half-open
	SuccessThreshold float64

	// WindowSize is the sliding window duration for metrics
	WindowSize time.Duration

	// BucketCount is the number of buckets in the sliding window
	BucketCount int

	// ErrorClassifier determines which errors count as failures
	ErrorClassifier ErrorClassifier

	// Logger for circuit breaker events
	Logger core.Logger

	// Metrics collector for monitoring
	Metrics MetricsCollector
}

// DefaultConfig returns a production-ready default configuration
func DefaultConfig() *CircuitBreakerConfig {
	return &CircuitBreakerConfig{
		Name:             "default",
		ErrorThreshold:   0.5, // 50% error rate
		VolumeThreshold:  10,  // Need 10 requests minimum
		SleepWindow:      30 * time.Second,
		HalfOpenRequests: 5,
		SuccessThreshold: 0.6, // 60% success to recover
		WindowSize:       60 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}
}

// ExecutionToken tracks in-flight requests to prevent orphaned executions
type ExecutionToken struct {
	id         uint64
	startTime  time.Time
	isHalfOpen bool
}

// CircuitBreaker addresses all production issues identified in review
type CircuitBreaker struct {
	// Configuration
	config *CircuitBreakerConfig

	// State management (using atomic for frequently accessed state)
	state          atomic.Value // CircuitState
	stateChangedAt atomic.Value // time.Time
	generation     uint64

	// Metrics tracking
	window *SlidingWindow

	// Half-open state management with atomic operations
	halfOpenCount     atomic.Int32
	halfOpenTotal     atomic.Int32 // Total requests allowed in current half-open period
	halfOpenSuccesses atomic.Int32
	halfOpenFailures  atomic.Int32
	halfOpenTokens    sync.Map // map[uint64]ExecutionToken for tracking in-flight requests
	tokenCounter      atomic.Uint64

	// Manual control
	forceOpen   atomic.Bool
	forceClosed atomic.Bool

	// Legacy compatibility
	failureCount atomic.Int32

	// Error type cache to avoid allocations
	errorTypeCache sync.Map // map[error]string

	// State change listeners
	listeners []func(name string, from, to CircuitState)

	// Reduced lock contention - only for state transitions
	mu sync.Mutex

	// Metrics for monitoring
	executionsInFlight atomic.Int32
	totalExecutions    atomic.Uint64
	rejectedExecutions atomic.Uint64
}

// NewCircuitBreaker creates a production-ready circuit breaker
func NewCircuitBreaker(config *CircuitBreakerConfig) (*CircuitBreaker, error) {
	// Check nil before calling Validate() to prevent panic
	if config == nil {
		config = DefaultConfig()
	}

	// Log validation start for debugging
	if config.Logger != nil { // config cannot be nil here - we just checked above
		config.Logger.Debug("Validating circuit breaker configuration", map[string]interface{}{
			"operation":        "circuit_breaker_validation",
			"name":             config.Name,
			"error_threshold":  config.ErrorThreshold,
			"volume_threshold": config.VolumeThreshold,
			"sleep_window":     config.SleepWindow.String(),
		})
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		// Log validation failure
		if config.Logger != nil { // config is guaranteed non-nil here
			config.Logger.Error("Circuit breaker configuration validation failed", map[string]interface{}{
				"operation":  "circuit_breaker_validation_failed",
				"name":       config.Name,
				"error":      err.Error(),
				"error_type": fmt.Sprintf("%T", err),
			})
		}
		return nil, fmt.Errorf("invalid circuit breaker config: %w", err)
	}

	// Apply defaults for missing values
	if config.WindowSize == 0 {
		config.WindowSize = 60 * time.Second
	}
	if config.BucketCount == 0 {
		config.BucketCount = 10
	}
	if config.ErrorClassifier == nil {
		config.ErrorClassifier = DefaultErrorClassifier
	}
	if config.Logger == nil {
		config.Logger = &core.NoOpLogger{}
	}
	if config.Metrics == nil {
		config.Metrics = &noopMetrics{}
	}
	if config.SuccessThreshold == 0 {
		config.SuccessThreshold = 0.6
	}
	if config.HalfOpenRequests == 0 {
		config.HalfOpenRequests = 5
	}

	cb := &CircuitBreaker{
		config:     config,
		generation: 0,
		// Use new constructor to pass logger and name for time skew detection
		window:    NewSlidingWindowWithLogger(config.WindowSize, config.BucketCount, true, config.Logger, config.Name),
		listeners: make([]func(string, CircuitState, CircuitState), 0),
	}

	// Initialize atomic values
	cb.state.Store(StateClosed)
	cb.stateChangedAt.Store(time.Now())

	// Log successful creation
	if config.Logger != nil {
		config.Logger.Info("Circuit breaker created successfully", map[string]interface{}{
			"operation":          "circuit_breaker_created",
			"name":               config.Name,
			"error_threshold":    config.ErrorThreshold,
			"volume_threshold":   config.VolumeThreshold,
			"sleep_window_ms":    config.SleepWindow.Milliseconds(),
			"half_open_requests": config.HalfOpenRequests,
		})
	}

	return cb, nil
}

// SetLogger sets the logger provider (follows framework design principles)
// The component is always set to "framework/resilience" to ensure proper log attribution
// regardless of which agent or tool is using the resilience module.
func (cb *CircuitBreaker) SetLogger(logger core.Logger) {
	if logger == nil {
		cb.config.Logger = &core.NoOpLogger{}
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			cb.config.Logger = cal.WithComponent("framework/resilience")
		} else {
			cb.config.Logger = logger
		}
	}
}

// Execute runs the given function with circuit breaker protection and proper timeout handling
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	return cb.ExecuteWithTimeout(ctx, 0, fn)
}

// ExecuteWithTimeout runs the function with timeout protection
func (cb *CircuitBreaker) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, fn func() error) error {
	startTime := time.Now()

	// Log execution start for debugging
	if cb.config.Logger != nil {
		cb.config.Logger.Debug("Circuit breaker execution starting", map[string]interface{}{
			"operation":     "circuit_breaker_execute",
			"name":          cb.config.Name,
			"current_state": cb.GetState(),
			"timeout_ms":    timeout.Milliseconds(),
		})
	}

	// Check if we can execute
	token, allowed := cb.startExecution()
	if !allowed {
		// Log rejection for monitoring
		if cb.config.Logger != nil {
			cb.config.Logger.Info("Circuit breaker rejected execution", map[string]interface{}{
				"operation":     "circuit_breaker_reject",
				"name":          cb.config.Name,
				"current_state": cb.GetState(),
				"reason":        "circuit_open",
			})
		}

		cb.rejectedExecutions.Add(1)
		cb.config.Metrics.RecordRejection(cb.config.Name)
		return fmt.Errorf("circuit breaker '%s' is open: %w", cb.config.Name, core.ErrCircuitBreakerOpen)
	}

	// Log execution allowed for debugging
	if cb.config.Logger != nil {
		cb.config.Logger.Debug("Circuit breaker allowed execution", map[string]interface{}{
			"operation":     "circuit_breaker_allow",
			"name":          cb.config.Name,
			"current_state": cb.GetState(),
			"token_id":      token.id,
			"is_half_open":  token.isHalfOpen,
		})
	}

	// Track in-flight execution
	cb.executionsInFlight.Add(1)
	defer cb.executionsInFlight.Add(-1)
	cb.totalExecutions.Add(1)

	// Setup timeout if specified
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Execute the function in a goroutine
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Preserve stack trace for debugging
				stack := debug.Stack()

				// Create descriptive error based on panic type
				var panicErr error
				switch v := r.(type) {
				case error:
					panicErr = fmt.Errorf("panic in circuit breaker: %w\nStack:\n%s", v, stack)
				case string:
					panicErr = fmt.Errorf("panic in circuit breaker: %s\nStack:\n%s", v, stack)
				default:
					panicErr = fmt.Errorf("panic in circuit breaker: %v (%T)\nStack:\n%s", v, v, stack)
				}

				// Log immediately for debugging
				cb.config.Logger.Error("Circuit breaker caught panic", map[string]interface{}{
					"name":  cb.config.Name,
					"panic": fmt.Sprintf("%v", r),
					"type":  fmt.Sprintf("%T", r),
				})

				// Send error through channel - this ensures channel always receives a value
				done <- panicErr
			}
		}()

		// Execute the function
		done <- fn()
	}()

	// Wait for either completion or timeout
	select {
	case err := <-done:
		// Normal completion
		cb.completeExecution(token, err)

		// Log execution completion for debugging
		if cb.config.Logger != nil {
			cb.config.Logger.Debug("Circuit breaker execution completed", map[string]interface{}{
				"operation":   "circuit_breaker_complete",
				"name":        cb.config.Name,
				"success":     err == nil,
				"duration_ms": time.Since(startTime).Milliseconds(),
				"token_id":    token.id,
				"error": func() string {
					if err != nil {
						return err.Error()
					}
					return ""
				}(),
			})
		}

		return err
	case <-ctx.Done():
		// Context cancelled or timeout
		// The function is still running - this creates an orphaned request
		// It will be cleaned up when it eventually completes

		// Log context cancellation for debugging
		if cb.config.Logger != nil {
			cb.config.Logger.Debug("Circuit breaker execution cancelled", map[string]interface{}{
				"operation":   "circuit_breaker_cancelled",
				"name":        cb.config.Name,
				"reason":      ctx.Err().Error(),
				"duration_ms": time.Since(startTime).Milliseconds(),
				"token_id":    token.id,
			})
		}

		go func() {
			<-done // Wait for function to complete
			cb.completeExecution(token, ctx.Err())
		}()
		return ctx.Err()
	}
}

// startExecution attempts to start an execution and returns a token if allowed
func (cb *CircuitBreaker) startExecution() (ExecutionToken, bool) {
	// Check manual overrides first (atomic read)
	if cb.forceClosed.Load() {
		return ExecutionToken{}, true
	}
	if cb.forceOpen.Load() {
		return ExecutionToken{}, false
	}

	currentState := cb.state.Load().(CircuitState)

	// Log state evaluation for debugging
	if cb.config.Logger != nil {
		cb.config.Logger.Debug("Evaluating circuit breaker execution", map[string]interface{}{
			"operation":      "circuit_breaker_evaluate",
			"name":           cb.config.Name,
			"current_state":  currentState.String(),
			"force_open":     cb.forceOpen.Load(),
			"force_closed":   cb.forceClosed.Load(),
			"error_rate":     cb.window.GetErrorRate(),
			"total_requests": cb.window.GetTotal(),
		})
	}

	switch currentState {
	case StateClosed:
		// Always allow in closed state
		return ExecutionToken{
			id:         cb.tokenCounter.Add(1),
			startTime:  time.Now(),
			isHalfOpen: false,
		}, true

	case StateOpen:
		// Check if we should transition to half-open
		stateChangedAt := cb.stateChangedAt.Load().(time.Time)
		timeInOpen := time.Since(stateChangedAt)

		if timeInOpen > cb.config.SleepWindow {
			// Log transition attempt
			if cb.config.Logger != nil {
				cb.config.Logger.Info("Circuit breaker attempting half-open transition", map[string]interface{}{
					"operation":       "circuit_breaker_transition_attempt",
					"name":            cb.config.Name,
					"from_state":      "open",
					"to_state":        "half-open",
					"time_in_open_ms": timeInOpen.Milliseconds(),
					"sleep_window_ms": cb.config.SleepWindow.Milliseconds(),
				})
			}

			// Try to transition to half-open
			cb.mu.Lock()
			// Double-check state after acquiring lock
			if cb.state.Load().(CircuitState) == StateOpen {
				cb.transitionToUnlocked(StateHalfOpen)
			}
			cb.mu.Unlock()

			// Retry after transition
			return cb.startExecution()
		}
		return ExecutionToken{}, false

	case StateHalfOpen:
		current := cb.halfOpenTotal.Load()

		// Log half-open capacity reached
		if cb.config.Logger != nil {
			cb.config.Logger.Debug("Half-open state capacity check", map[string]interface{}{
				"operation":           "half_open_capacity_check",
				"name":                cb.config.Name,
				"current_requests":    current,
				"max_requests":        cb.config.HalfOpenRequests,
				"half_open_successes": cb.halfOpenSuccesses.Load(),
				"half_open_failures":  cb.halfOpenFailures.Load(),
			})
		}

		// Atomically check and increment total requests
		for {
			current = cb.halfOpenTotal.Load()
			if cb.config.HalfOpenRequests > 0 && int(current) >= cb.config.HalfOpenRequests {
				// Already at limit
				return ExecutionToken{}, false
			}
			// Try to increment
			if cb.halfOpenTotal.CompareAndSwap(current, current+1) {
				// Successfully reserved a slot
				break
			}
			// Someone else modified it, retry
		}

		// Also track concurrent requests
		cb.halfOpenCount.Add(1)

		token := ExecutionToken{
			id:         cb.tokenCounter.Add(1),
			startTime:  time.Now(),
			isHalfOpen: true,
		}

		// Track this token to prevent orphaned requests
		cb.halfOpenTokens.Store(token.id, token)

		return token, true

	default:
		return ExecutionToken{}, false
	}
}

// completeExecution records the result of an execution
func (cb *CircuitBreaker) completeExecution(token ExecutionToken, err error) {
	// Skip if manually controlled
	if cb.forceClosed.Load() || cb.forceOpen.Load() {
		return
	}

	// Clean up half-open token if applicable
	if token.isHalfOpen {
		cb.halfOpenTokens.Delete(token.id)
		cb.halfOpenCount.Add(-1) // Decrement counter when request completes
	}

	// Record in sliding window
	if err == nil {
		cb.window.RecordSuccess()
		cb.config.Metrics.RecordSuccess(cb.config.Name)

		if token.isHalfOpen {
			cb.halfOpenSuccesses.Add(1)
		}
	} else {
		// Log error classification decision for debugging
		shouldCount := cb.config.ErrorClassifier(err)

		if cb.config.Logger != nil {
			// Determine classification and reason
			classification := "infrastructure_error"
			reason := "will_count"

			// Check error types to understand classification rationale
			if core.IsConfigurationError(err) {
				classification = "configuration_error"
				reason = "user_error_wont_count"
			} else if core.IsNotFound(err) {
				classification = "not_found"
				reason = "user_error_wont_count"
			} else if core.IsStateError(err) {
				classification = "state_error"
				reason = "programming_error_wont_count"
			} else if errors.Is(err, context.Canceled) || errors.Is(err, core.ErrContextCanceled) {
				classification = "context_canceled"
				reason = "client_gave_up_wont_count"
			} else if errors.Is(err, context.DeadlineExceeded) {
				classification = "deadline_exceeded"
				reason = "timeout_will_count"
			}

			cb.config.Logger.Debug("Error classification decision", map[string]interface{}{
				"operation":         "error_classification",
				"name":              cb.config.Name,
				"error":             err.Error(),
				"error_type":        fmt.Sprintf("%T", err),
				"classification":    classification,
				"reason":            reason,
				"counts_as_failure": shouldCount,
			})
		}

		if shouldCount {
			cb.window.RecordFailure()

			// Cache error type to avoid allocation
			errorType := cb.getErrorType(err)
			cb.config.Metrics.RecordFailure(cb.config.Name, errorType)

			// Legacy failure counting
			cb.failureCount.Add(1)

			if token.isHalfOpen {
				cb.halfOpenFailures.Add(1)
			}
		}
	}

	// Evaluate state
	cb.evaluateState()
}

// getErrorType returns cached error type string to avoid allocations
func (cb *CircuitBreaker) getErrorType(err error) string {
	if cached, ok := cb.errorTypeCache.Load(err); ok {
		return cached.(string)
	}

	// Use type assertion for common errors to avoid fmt.Sprintf
	switch err.(type) {
	case *core.FrameworkError:
		return "*core.FrameworkError"
	default:
		// Check for well-known errors
		if errors.Is(err, context.DeadlineExceeded) {
			return "DeadlineExceeded"
		}
		if errors.Is(err, context.Canceled) {
			return "Canceled"
		}
		// Only allocate for unknown error types
		errorType := fmt.Sprintf("%T", err)
		cb.errorTypeCache.Store(err, errorType)
		return errorType
	}
}

// evaluateState checks if state transition is needed
func (cb *CircuitBreaker) evaluateState() {
	currentState := cb.state.Load().(CircuitState)
	errorRate := cb.window.GetErrorRate()
	total := cb.window.GetTotal()

	// Log state evaluation for debugging
	if cb.config.Logger != nil {
		cb.config.Logger.Debug("Evaluating circuit breaker state", map[string]interface{}{
			"operation":        "state_evaluation",
			"name":             cb.config.Name,
			"current_state":    currentState.String(),
			"error_rate":       errorRate,
			"total_requests":   total,
			"volume_threshold": cb.config.VolumeThreshold,
			"error_threshold":  cb.config.ErrorThreshold,
			"failure_count":    cb.failureCount.Load(),
		})
	}

	switch currentState {
	case StateClosed:
		// Check if we should open

		// Use legacy threshold if set
		if cb.config.FailureThreshold > 0 && int(cb.failureCount.Load()) >= cb.config.FailureThreshold {
			cb.mu.Lock()
			cb.transitionToUnlocked(StateOpen)
			cb.mu.Unlock()
			return
		}

		// Use error rate threshold
		// Safe comparison: VolumeThreshold is int, total is uint64
		// #nosec G115 - VolumeThreshold is checked to be > 0 before conversion
		if cb.config.VolumeThreshold > 0 && total >= uint64(cb.config.VolumeThreshold) && errorRate >= cb.config.ErrorThreshold {
			// Log opening decision
			if cb.config.Logger != nil {
				cb.config.Logger.Info("Circuit breaker opening due to error threshold", map[string]interface{}{
					"operation":        "circuit_breaker_opening",
					"name":             cb.config.Name,
					"trigger":          "error_threshold_exceeded",
					"error_rate":       errorRate,
					"error_threshold":  cb.config.ErrorThreshold,
					"total_requests":   total,
					"volume_threshold": cb.config.VolumeThreshold,
				})
			}

			cb.mu.Lock()
			cb.transitionToUnlocked(StateOpen)
			cb.mu.Unlock()
		}

	case StateHalfOpen:
		// Check if we have enough test requests to make a decision
		successes := cb.halfOpenSuccesses.Load()
		failures := cb.halfOpenFailures.Load()
		totalHalfOpen := successes + failures

		if cb.config.HalfOpenRequests > 0 && int(totalHalfOpen) >= cb.config.HalfOpenRequests {
			successRate := float64(successes) / float64(totalHalfOpen)

			// Log half-open transition decision
			if cb.config.Logger != nil {
				cb.config.Logger.Info("Circuit breaker half-open evaluation complete", map[string]interface{}{
					"operation":         "half_open_evaluation",
					"name":              cb.config.Name,
					"success_rate":      successRate,
					"success_threshold": cb.config.SuccessThreshold,
					"total_attempts":    totalHalfOpen,
					"successes":         successes,
					"failures":          failures,
				})
			}

			cb.mu.Lock()
			if successRate >= cb.config.SuccessThreshold {
				// Log recovery
				if cb.config.Logger != nil {
					cb.config.Logger.Info("Circuit breaker recovering to closed state", map[string]interface{}{
						"operation":    "circuit_breaker_recovery",
						"name":         cb.config.Name,
						"success_rate": successRate,
						"threshold":    cb.config.SuccessThreshold,
					})
				}
				// Enough successes, close the circuit
				cb.transitionToUnlocked(StateClosed)
				cb.failureCount.Store(0) // Reset legacy counter
			} else {
				// Log re-opening from half-open
				if cb.config.Logger != nil {
					cb.config.Logger.Info("Circuit breaker re-opening due to insufficient success rate", map[string]interface{}{
						"operation":           "circuit_breaker_reopen",
						"name":                cb.config.Name,
						"success_rate":        successRate,
						"threshold":           cb.config.SuccessThreshold,
						"new_sleep_window_ms": time.Duration(float64(cb.config.SleepWindow) * 1.5).Milliseconds(),
					})
				}
				// Too many failures, reopen
				cb.transitionToUnlocked(StateOpen)
				// Exponential backoff for next attempt
				cb.config.SleepWindow = time.Duration(float64(cb.config.SleepWindow) * 1.5)
				if cb.config.SleepWindow > 5*time.Minute {
					cb.config.SleepWindow = 5 * time.Minute // Cap at 5 minutes
				}
			}
			cb.mu.Unlock()
		}
	}
}

// transitionToUnlocked changes state (must be called with lock held)
func (cb *CircuitBreaker) transitionToUnlocked(newState CircuitState) {
	oldState := cb.state.Load().(CircuitState)
	if oldState == newState {
		return
	}

	cb.state.Store(newState)
	cb.stateChangedAt.Store(time.Now())
	cb.generation++

	// Reset half-open counters when entering half-open
	if newState == StateHalfOpen {
		cb.halfOpenCount.Store(0)
		cb.halfOpenTotal.Store(0) // Reset total count for new half-open period
		cb.halfOpenSuccesses.Store(0)
		cb.halfOpenFailures.Store(0)
		// Clear any orphaned tokens
		cb.halfOpenTokens.Range(func(key, value interface{}) bool {
			cb.halfOpenTokens.Delete(key)
			return true
		})
	}

	// Log state change
	cb.config.Logger.Info("Circuit breaker state changed", map[string]interface{}{
		"name":       cb.config.Name,
		"from":       oldState.String(),
		"to":         newState.String(),
		"error_rate": cb.window.GetErrorRate(),
	})

	// Record metrics
	cb.config.Metrics.RecordStateChange(cb.config.Name, oldState.String(), newState.String())

	// Notify listeners
	for _, listener := range cb.listeners {
		go listener(cb.config.Name, oldState, newState)
	}
}

// AddStateChangeListener adds a listener for state changes
func (cb *CircuitBreaker) AddStateChangeListener(listener func(name string, from, to CircuitState)) {
	cb.mu.Lock()
	cb.listeners = append(cb.listeners, listener)
	cb.mu.Unlock()
}

// GetState returns the current state
func (cb *CircuitBreaker) GetState() string {
	return cb.state.Load().(CircuitState).String()
}

// GetMetrics returns current metrics
func (cb *CircuitBreaker) GetMetrics() map[string]interface{} {
	success, failure := cb.window.GetCounts()
	total := success + failure

	metrics := map[string]interface{}{
		"name":                 cb.config.Name,
		"state":                cb.GetState(),
		"generation":           cb.generation,
		"success":              success,
		"failure":              failure,
		"total":                total,
		"error_rate":           cb.window.GetErrorRate(),
		"force_open":           cb.forceOpen.Load(),
		"force_closed":         cb.forceClosed.Load(),
		"executions_in_flight": cb.executionsInFlight.Load(),
		"total_executions":     cb.totalExecutions.Load(),
		"rejected_executions":  cb.rejectedExecutions.Load(),
	}

	currentState := cb.state.Load().(CircuitState)
	if currentState == StateHalfOpen {
		metrics["half_open_count"] = cb.halfOpenCount.Load()
		metrics["half_open_successes"] = cb.halfOpenSuccesses.Load()
		metrics["half_open_failures"] = cb.halfOpenFailures.Load()

		// Count orphaned tokens
		orphaned := 0
		now := time.Now()
		cb.halfOpenTokens.Range(func(key, value interface{}) bool {
			token := value.(ExecutionToken)
			if now.Sub(token.startTime) > 30*time.Second {
				orphaned++
			}
			return true
		})
		metrics["orphaned_requests"] = orphaned
	}

	return metrics
}

// Reset resets the circuit breaker
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// Capture comprehensive state before reset for debugging
	oldState := cb.state.Load().(CircuitState)
	success, failure := cb.window.GetCounts()
	errorRate := cb.window.GetErrorRate()

	// Capture half-open state metrics if relevant (valuable debugging info)
	var halfOpenInProgress, halfOpenSuccesses, halfOpenFailures int32
	if oldState == StateHalfOpen {
		halfOpenInProgress = cb.halfOpenCount.Load()
		halfOpenSuccesses = cb.halfOpenSuccesses.Load()
		halfOpenFailures = cb.halfOpenFailures.Load()
	}

	// Perform reset
	cb.state.Store(StateClosed)
	cb.stateChangedAt.Store(time.Now())
	cb.failureCount.Store(0)
	cb.halfOpenCount.Store(0)
	cb.halfOpenSuccesses.Store(0)
	cb.halfOpenFailures.Store(0)
	// Use new constructor to maintain logger and name for time skew detection
	cb.window = NewSlidingWindowWithLogger(cb.config.WindowSize, cb.config.BucketCount, true, cb.config.Logger, cb.config.Name)

	// Clear tokens and count orphaned
	orphanedCount := 0
	cb.halfOpenTokens.Range(func(key, value interface{}) bool {
		cb.halfOpenTokens.Delete(key)
		orphanedCount++
		return true
	})

	// Log reset with relevant context
	if cb.config.Logger != nil {
		fields := map[string]interface{}{
			"operation":           "circuit_breaker_reset",
			"name":                cb.config.Name,
			"previous_state":      oldState.String(),
			"new_state":           "closed",
			"cleared_success":     success,
			"cleared_failure":     failure,
			"previous_error_rate": errorRate,
			"action":              "manual_reset",
			"timestamp":           time.Now().UTC().Format(time.RFC3339),
		}

		// Only include orphaned tokens if there were any (reduce noise)
		if orphanedCount > 0 {
			fields["orphaned_tokens"] = orphanedCount
			// This is important - indicates potential in-flight requests that were abandoned
		}

		// If resetting from half-open, include valuable debugging metrics
		if oldState == StateHalfOpen {
			fields["half_open_in_progress"] = halfOpenInProgress
			if halfOpenSuccesses > 0 || halfOpenFailures > 0 {
				fields["half_open_successes"] = halfOpenSuccesses
				fields["half_open_failures"] = halfOpenFailures
				// This helps understand why half-open didn't transition naturally
			}
		}

		cb.config.Logger.Info("Circuit breaker reset", fields)
	}
}

// ForceOpen manually opens the circuit
func (cb *CircuitBreaker) ForceOpen() {
	previousState := cb.GetState()

	// Log manual intervention
	if cb.config.Logger != nil {
		cb.config.Logger.Info("Circuit breaker manually forced open", map[string]interface{}{
			"operation":      "circuit_breaker_force_open",
			"name":           cb.config.Name,
			"previous_state": previousState,
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
		})
	}

	cb.forceOpen.Store(true)
	cb.forceClosed.Store(false)

	cb.mu.Lock()
	if cb.state.Load().(CircuitState) != StateOpen {
		cb.transitionToUnlocked(StateOpen)
	}
	cb.mu.Unlock()
}

// ForceClosed manually closes the circuit
func (cb *CircuitBreaker) ForceClosed() {
	previousState := cb.GetState()

	// Log manual intervention
	if cb.config.Logger != nil {
		cb.config.Logger.Info("Circuit breaker manually forced closed", map[string]interface{}{
			"operation":      "circuit_breaker_force_closed",
			"name":           cb.config.Name,
			"previous_state": previousState,
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
		})
	}

	cb.forceClosed.Store(true)
	cb.forceOpen.Store(false)

	cb.mu.Lock()
	if cb.state.Load().(CircuitState) != StateClosed {
		cb.transitionToUnlocked(StateClosed)
	}
	cb.mu.Unlock()
}

// ClearForce removes manual override
func (cb *CircuitBreaker) ClearForce() {
	wasForceOpen := cb.forceOpen.Load()
	wasForceClosed := cb.forceClosed.Load()

	// Log manual override clear if it was active
	if cb.config.Logger != nil && (wasForceOpen || wasForceClosed) {
		cb.config.Logger.Info("Circuit breaker manual override cleared", map[string]interface{}{
			"operation":        "circuit_breaker_clear_force",
			"name":             cb.config.Name,
			"was_force_open":   wasForceOpen,
			"was_force_closed": wasForceClosed,
			"current_state":    cb.GetState(),
			"timestamp":        time.Now().UTC().Format(time.RFC3339),
		})
	}

	cb.forceOpen.Store(false)
	cb.forceClosed.Store(false)
}

// CleanupOrphanedRequests cleans up requests that have been in-flight too long
func (cb *CircuitBreaker) CleanupOrphanedRequests(maxAge time.Duration) int {
	// Log cleanup start for debugging
	if cb.config.Logger != nil {
		cb.config.Logger.Debug("Starting orphaned request cleanup", map[string]interface{}{
			"operation":       "orphaned_cleanup_start",
			"name":            cb.config.Name,
			"max_age_ms":      maxAge.Milliseconds(),
			"half_open_count": cb.halfOpenCount.Load(),
		})
	}

	cleaned := 0
	now := time.Now()
	const maxTokensToLog = 100  // Limit tokens logged to prevent memory issues
	var orphanedTokens []uint64 // Track orphaned token IDs for debugging

	cb.halfOpenTokens.Range(func(key, value interface{}) bool {
		token, ok := value.(ExecutionToken)
		if !ok {
			// Skip invalid entries safely
			return true
		}
		if now.Sub(token.startTime) > maxAge {
			// Only collect first 100 token IDs to avoid memory issues
			if len(orphanedTokens) < maxTokensToLog {
				orphanedTokens = append(orphanedTokens, token.id)
			}
			cb.halfOpenTokens.Delete(key)
			// Record as failure
			cb.completeExecution(token, errors.New("request orphaned"))
			cleaned++
		}
		return true
	})

	// Log cleanup summary
	if cb.config.Logger != nil {
		if cleaned > 0 {
			fields := map[string]interface{}{
				"operation":       "orphaned_cleanup_complete",
				"name":            cb.config.Name,
				"cleaned_count":   cleaned,
				"max_age_ms":      maxAge.Milliseconds(),
				"orphaned_tokens": orphanedTokens,
			}
			// Indicate if token list was truncated
			if cleaned > maxTokensToLog {
				fields["tokens_truncated"] = true
				fields["tokens_shown"] = maxTokensToLog
			}
			cb.config.Logger.Warn("Orphaned requests cleaned up", fields)
		} else {
			cb.config.Logger.Debug("No orphaned requests found", map[string]interface{}{
				"operation":     "orphaned_cleanup_complete",
				"name":          cb.config.Name,
				"cleaned_count": 0,
			})
		}
	}

	return cleaned
}

// Validate validates the circuit breaker configuration
func (c *CircuitBreakerConfig) Validate() error {
	if c == nil {
		return errors.New("configuration cannot be nil")
	}

	if c.Name == "" {
		return errors.New("circuit breaker name is required")
	}

	if c.ErrorThreshold < 0 || c.ErrorThreshold > 1 {
		return fmt.Errorf("error threshold must be between 0 and 1, got %f", c.ErrorThreshold)
	}

	if c.VolumeThreshold < 0 {
		return fmt.Errorf("volume threshold must be non-negative, got %d", c.VolumeThreshold)
	}

	if c.SuccessThreshold < 0 || c.SuccessThreshold > 1 {
		return fmt.Errorf("success threshold must be between 0 and 1, got %f", c.SuccessThreshold)
	}

	if c.HalfOpenRequests < 1 {
		return fmt.Errorf("half-open requests must be at least 1, got %d", c.HalfOpenRequests)
	}

	if c.SleepWindow < 0 {
		return fmt.Errorf("sleep window must be non-negative, got %v", c.SleepWindow)
	}

	if c.WindowSize < 0 {
		return fmt.Errorf("window size must be non-negative, got %v", c.WindowSize)
	}

	if c.BucketCount < 1 {
		return fmt.Errorf("bucket count must be at least 1, got %d", c.BucketCount)
	}

	return nil
}

// bucket represents a time bucket in the sliding window
type bucket struct {
	timestamp time.Time
	success   uint64
	failure   uint64
}

// SlidingWindow with time skew protection
type SlidingWindow struct {
	buckets      []bucket
	windowSize   time.Duration
	bucketSize   time.Duration
	currentIdx   int
	lastRotation time.Time
	mu           sync.RWMutex
	monotonic    bool // Use monotonic time to avoid skew

	// Include logger and name for time skew detection
	logger core.Logger // For logging time skew and reset events
	name   string      // Circuit breaker name for context
}

// NewSlidingWindow creates a sliding window with time skew protection
// Maintained for backward compatibility - calls NewSlidingWindowWithLogger with nil logger
func NewSlidingWindow(windowSize time.Duration, bucketCount int, monotonic bool) *SlidingWindow {
	return NewSlidingWindowWithLogger(windowSize, bucketCount, monotonic, nil, "")
}

// NewSlidingWindowWithLogger creates a sliding window with time skew protection and logging
// This is the new constructor that supports logging of time skew detection
func NewSlidingWindowWithLogger(windowSize time.Duration, bucketCount int, monotonic bool, logger core.Logger, name string) *SlidingWindow {
	if bucketCount <= 0 {
		bucketCount = 10
	}

	// Default to no-op logger if nil
	if logger == nil {
		logger = &core.NoOpLogger{}
	}

	bucketSize := windowSize / time.Duration(bucketCount)
	buckets := make([]bucket, bucketCount)
	now := time.Now()

	for i := range buckets {
		buckets[i].timestamp = now
	}

	return &SlidingWindow{
		buckets:      buckets,
		windowSize:   windowSize,
		bucketSize:   bucketSize,
		lastRotation: now,
		monotonic:    monotonic,
		logger:       logger,
		name:         name,
	}
}

// rotateBuckets with time skew protection
func (sw *SlidingWindow) rotateBuckets() {
	now := time.Now()

	var elapsed time.Duration
	if sw.monotonic {
		// Use monotonic time (won't jump backward)
		elapsed = now.Sub(sw.lastRotation)
	} else {
		elapsed = now.Sub(sw.buckets[sw.currentIdx].timestamp)
	}

	// Handle time skew - if time went backward, reset
	if elapsed < 0 {
		// Log time skew detection - indicates system clock issue
		if sw.logger != nil {
			// Get counts before reset (without locking since we already hold the lock)
			var success, failure uint64
			cutoff := now.Add(-sw.windowSize)
			for i := range sw.buckets {
				b := &sw.buckets[i]
				if b.timestamp.After(cutoff) {
					success += atomic.LoadUint64(&b.success)
					failure += atomic.LoadUint64(&b.failure)
				}
			}

			sw.logger.Warn("Time skew detected in sliding window - resetting metrics", map[string]interface{}{
				"operation":       "sliding_window_time_skew",
				"name":            sw.name,
				"elapsed_ns":      elapsed.Nanoseconds(),
				"action":          "window_reset",
				"lost_success":    success,
				"lost_failure":    failure,
				"lost_total":      success + failure,
				"window_size_sec": sw.windowSize.Seconds(),
				"monotonic_mode":  sw.monotonic,
			})
		}

		sw.reset()
		return
	}

	// Normal rotation
	if elapsed >= sw.bucketSize {
		bucketsToRotate := int(elapsed / sw.bucketSize)
		if bucketsToRotate > len(sw.buckets) {
			bucketsToRotate = len(sw.buckets)
		}

		for i := 0; i < bucketsToRotate; i++ {
			sw.currentIdx = (sw.currentIdx + 1) % len(sw.buckets)
			sw.buckets[sw.currentIdx] = bucket{
				timestamp: now,
				success:   0,
				failure:   0,
			}
		}

		sw.lastRotation = now
	}
}

// reset clears all buckets (used when time skew detected)
func (sw *SlidingWindow) reset() {
	now := time.Now()
	for i := range sw.buckets {
		sw.buckets[i] = bucket{
			timestamp: now,
			success:   0,
			failure:   0,
		}
	}
	sw.currentIdx = 0
	sw.lastRotation = now
}

// RecordSuccess records a successful operation
func (sw *SlidingWindow) RecordSuccess() {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	sw.rotateBuckets()
	atomic.AddUint64(&sw.buckets[sw.currentIdx].success, 1)
}

// RecordFailure records a failed operation
func (sw *SlidingWindow) RecordFailure() {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	sw.rotateBuckets()
	atomic.AddUint64(&sw.buckets[sw.currentIdx].failure, 1)
}

// GetCounts returns success and failure counts
func (sw *SlidingWindow) GetCounts() (success, failure uint64) {
	sw.mu.RLock()
	defer sw.mu.RUnlock()

	cutoff := time.Now().Add(-sw.windowSize)

	for i := range sw.buckets {
		b := &sw.buckets[i]
		if b.timestamp.After(cutoff) {
			success += atomic.LoadUint64(&b.success)
			failure += atomic.LoadUint64(&b.failure)
		}
	}

	return success, failure
}

// GetErrorRate returns the current error rate
func (sw *SlidingWindow) GetErrorRate() float64 {
	success, failure := sw.GetCounts()
	total := success + failure
	if total == 0 {
		return 0
	}
	return float64(failure) / float64(total)
}

// GetTotal returns the total number of requests
func (sw *SlidingWindow) GetTotal() uint64 {
	success, failure := sw.GetCounts()
	return success + failure
}

// CanExecute checks if the circuit breaker allows execution (backward compatibility)
func (cb *CircuitBreaker) CanExecute() bool {
	state := cb.state.Load().(CircuitState)
	if state == StateClosed {
		return true
	}
	if state == StateOpen {
		// Check if we should transition to half-open
		stateChangedAt := cb.stateChangedAt.Load().(time.Time)
		if time.Since(stateChangedAt) > cb.config.SleepWindow {
			// Transition to half-open
			cb.mu.Lock()
			if cb.state.Load().(CircuitState) == StateOpen {
				cb.transitionToUnlocked(StateHalfOpen)
			}
			cb.mu.Unlock()
			return true
		}
		return false
	}
	// Half-open - check if we have capacity
	return cb.config.HalfOpenRequests > 0 && int(cb.halfOpenTotal.Load()) < cb.config.HalfOpenRequests
}

// RecordSuccess records a successful operation (backward compatibility)
func (cb *CircuitBreaker) RecordSuccess() {
	cb.window.RecordSuccess()
	cb.evaluateState()
}

// RecordFailure records a failed operation (backward compatibility)
func (cb *CircuitBreaker) RecordFailure() {
	cb.window.RecordFailure()
	cb.failureCount.Add(1)
	cb.evaluateState()
}

// NewCircuitBreakerWithConfig creates a circuit breaker with config (backward compatibility)
func NewCircuitBreakerWithConfig(config *CircuitBreakerConfig) *CircuitBreaker {
	cb, _ := NewCircuitBreaker(config)
	return cb
}

// NewCircuitBreakerLegacy creates a circuit breaker with legacy parameters (backward compatibility)
func NewCircuitBreakerLegacy(failureThreshold int, recoveryTimeout time.Duration) *CircuitBreaker {
	config := &CircuitBreakerConfig{
		Name:             "legacy",
		FailureThreshold: failureThreshold,
		RecoveryTimeout:  recoveryTimeout,
		SleepWindow:      recoveryTimeout,
		ErrorThreshold:   0.5,
		VolumeThreshold:  1,   // Set to 1 for backward compatibility with old behavior
		HalfOpenRequests: 1,   // Only allow 1 test request in half-open for simpler behavior
		SuccessThreshold: 0.5, // Lower threshold for easier recovery
		WindowSize:       60 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}
	cb, _ := NewCircuitBreaker(config)
	return cb
}
