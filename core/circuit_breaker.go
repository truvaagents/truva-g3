// Package core provides fundamental abstractions and interfaces for the TruvaG3 framework.
// This file defines the CircuitBreaker interface and related types for implementing
// fault tolerance patterns in distributed systems.
//
// Purpose:
// - Defines the CircuitBreaker interface for protecting against cascading failures
// - Provides configuration structures for circuit breaker implementations
// - Establishes a standard API for circuit breaker state management and metrics
// - Enables resilient service communication through automatic failure detection
//
// Scope:
// - CircuitBreaker interface: Core contract for all circuit breaker implementations
// - CircuitBreakerParams: Configuration and dependency injection for implementations
// - State management: closed, open, and half-open states
// - Metrics collection for monitoring circuit breaker behavior
// - Timeout support for operations that might hang
//
// Circuit Breaker Pattern:
// The circuit breaker acts as a proxy that monitors failures and temporarily
// blocks requests when a failure threshold is reached. States:
// 1. Closed: Normal operation, requests pass through
// 2. Open: Threshold exceeded, requests fail immediately
// 3. Half-Open: Testing if service recovered, limited requests allowed
//
// Architecture:
// This interface enables:
// 1. Multiple implementation strategies (in-memory, distributed)
// 2. Pluggable failure detection algorithms
// 3. Integration with telemetry and logging systems
// 4. Graceful degradation of service functionality
//
// Usage:
// Implementations wrap service calls with Execute() or ExecuteWithTimeout()
// to automatically handle failures, timeouts, and circuit state transitions.
// The circuit breaker protects both the caller and the downstream service
// from cascading failures and overload conditions.
package core

import (
	"context"
	"time"
)

// CircuitBreaker provides circuit breaker functionality for fault tolerance.
// Implementations should protect against cascading failures by temporarily
// blocking requests when a threshold of failures is reached.
type CircuitBreaker interface {
	// Execute runs the provided function with circuit breaker protection.
	// If the circuit is open, it returns ErrCircuitBreakerOpen immediately.
	// If the circuit is closed or half-open, it executes the function and
	// records the result to update the circuit state.
	Execute(ctx context.Context, fn func() error) error

	// ExecuteWithTimeout runs the function with both circuit breaker protection
	// and a timeout. This is useful for operations that might hang.
	ExecuteWithTimeout(ctx context.Context, timeout time.Duration, fn func() error) error

	// GetState returns the current circuit breaker state as a string.
	// Possible values: "closed", "open", "half-open"
	GetState() string

	// GetMetrics returns current metrics about the circuit breaker.
	// This typically includes success/failure counts, state transitions, etc.
	GetMetrics() map[string]interface{}

	// Reset manually resets the circuit breaker to closed state.
	// This clears all failure counts and metrics.
	Reset()

	// CanExecute returns true if the circuit breaker would allow execution.
	// This is useful for checking state without actually executing.
	CanExecute() bool
}

// CircuitBreakerParams provides parameters for circuit breaker implementations.
// This complements the existing CircuitBreakerConfig in config.go with
// implementation-specific fields like Logger and Telemetry.
type CircuitBreakerParams struct {
	// Name identifies the circuit breaker (for logging/metrics)
	Name string

	// Config embeds the basic configuration
	Config CircuitBreakerConfig

	// Optional: Logger for circuit breaker events
	Logger Logger

	// Optional: Telemetry for metrics
	Telemetry Telemetry
}

// DefaultCircuitBreakerParams returns sensible defaults for circuit breaker parameters
func DefaultCircuitBreakerParams(name string) CircuitBreakerParams {
	return CircuitBreakerParams{
		Name: name,
		Config: CircuitBreakerConfig{
			Enabled:          true,
			Threshold:        5,
			Timeout:          30 * time.Second,
			HalfOpenRequests: 3,
		},
	}
}
