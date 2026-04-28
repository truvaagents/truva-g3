package resilience

import (
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// ResilienceDependencies holds optional dependencies (follows framework pattern)
type ResilienceDependencies struct {
	Logger    core.Logger
	Telemetry core.Telemetry
}

// Helper function to detect global telemetry availability
func globalTelemetryAvailable() bool {
	// Check if telemetry module has been initialized globally
	// This follows the same pattern as core module's global registry
	return telemetry.GetRegistry() != nil
}

// CreateCircuitBreaker creates a circuit breaker with proper dependency injection
func CreateCircuitBreaker(name string, deps ResilienceDependencies) (*CircuitBreaker, error) {
	config := DefaultConfig()
	config.Name = name

	// Ensure logger is available
	if deps.Logger != nil {
		config.Logger = deps.Logger
	} else {
		// Create default production logger with resilience component
		logger := core.NewProductionLogger(
			core.LoggingConfig{
				Level:  "info",
				Format: "json",
				Output: "stdout",
			},
			core.DevelopmentConfig{},
			"circuit-breaker",
		)
		// Set component to framework/resilience for log filtering
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			config.Logger = cal.WithComponent("framework/resilience")
		} else {
			config.Logger = logger
		}
	}

	// Auto-detect and enable telemetry if available
	if deps.Telemetry != nil {
		config.Metrics = NewTelemetryMetrics()
		config.Logger.Info("Telemetry integration enabled for circuit breaker", map[string]interface{}{
			"operation": "telemetry_integration",
			"name":      name,
			"component": "circuit_breaker",
		})
	} else {
		// Check if telemetry module is available globally
		if globalTelemetryAvailable() {
			config.Metrics = NewTelemetryMetrics()
			config.Logger.Info("Global telemetry detected and enabled", map[string]interface{}{
				"operation": "telemetry_auto_detection",
				"name":      name,
				"component": "circuit_breaker",
			})
		}
	}

	config.Logger.Info("Creating circuit breaker", map[string]interface{}{
		"operation":        "circuit_breaker_creation",
		"name":             name,
		"error_threshold":  config.ErrorThreshold,
		"volume_threshold": config.VolumeThreshold,
	})

	return NewCircuitBreaker(config)
}

// CreateRetryExecutor creates a retry executor with proper dependency injection
func CreateRetryExecutor(deps ResilienceDependencies) *RetryExecutor {
	executor := NewRetryExecutor(nil)

	// Inject logger
	if deps.Logger != nil {
		executor.SetLogger(deps.Logger)
	} else {
		// Create default production logger with resilience component
		logger := core.NewProductionLogger(
			core.LoggingConfig{
				Level:  "info",
				Format: "json",
				Output: "stdout",
			},
			core.DevelopmentConfig{},
			"retry-executor",
		)
		// Set component to framework/resilience for log filtering
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			executor.SetLogger(cal.WithComponent("framework/resilience"))
		} else {
			executor.SetLogger(logger)
		}
	}

	// Enable telemetry if available
	if deps.Telemetry != nil || globalTelemetryAvailable() {
		executor.telemetryEnabled = true
		executor.logger.Info("Telemetry integration enabled for retry executor", map[string]interface{}{
			"operation": "telemetry_integration",
			"component": "retry_executor",
		})
	}

	return executor
}

// WithLogger creates dependency injection option
func WithLogger(logger core.Logger) func(*ResilienceDependencies) {
	return func(d *ResilienceDependencies) {
		d.Logger = logger
	}
}

// WithTelemetry creates dependency injection option
func WithTelemetry(telemetry core.Telemetry) func(*ResilienceDependencies) {
	return func(d *ResilienceDependencies) {
		d.Telemetry = telemetry
	}
}
