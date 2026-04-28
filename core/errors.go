package core

import (
	"errors"
	"fmt"
)

// Standard sentinel errors for comparison using errors.Is()
// These are generic errors that can be wrapped with additional context.
// Use these errors throughout the codebase for consistent error handling.
// Example: return fmt.Errorf("failed to find agent %s: %w", agentID, ErrAgentNotFound)
var (
	// Agent-related errors
	ErrAgentNotFound      = errors.New("agent not found")
	ErrAgentNotReady      = errors.New("agent not ready")
	ErrAgentAlreadyExists = errors.New("agent already exists")

	// Capability-related errors
	ErrCapabilityNotFound   = errors.New("capability not found")
	ErrCapabilityNotEnabled = errors.New("capability not enabled")

	// Discovery-related errors
	ErrServiceNotFound      = errors.New("service not found")
	ErrDiscoveryUnavailable = errors.New("discovery service unavailable")

	// Configuration errors
	ErrInvalidConfiguration = errors.New("invalid configuration")
	ErrMissingConfiguration = errors.New("missing required configuration")
	ErrPortOutOfRange       = errors.New("port out of range")

	// State errors
	ErrAlreadyStarted    = errors.New("already started")
	ErrNotInitialized    = errors.New("not initialized")
	ErrAlreadyRegistered = errors.New("already registered")

	// Operation errors
	ErrTimeout            = errors.New("operation timeout")
	ErrContextCanceled    = errors.New("context canceled")
	ErrMaxRetriesExceeded = errors.New("maximum retries exceeded")

	// HTTP/Network errors
	ErrConnectionFailed = errors.New("connection failed")
	ErrRequestFailed    = errors.New("request failed")

	// Resilience errors
	ErrCircuitBreakerOpen = errors.New("circuit breaker open")

	// AI operation errors
	ErrAIOperationFailed = errors.New("AI operation failed")

	// Streaming errors
	ErrStreamPartiallyCompleted = errors.New("stream partially completed before interruption")
)

// IsRetryable checks if an error is retryable.
// Retryable errors are typically transient network or availability issues
// that may succeed if attempted again after a short delay.
// This function is used by the retry mechanism to determine whether
// to attempt an operation again or fail immediately.
func IsRetryable(err error) bool {
	return errors.Is(err, ErrDiscoveryUnavailable) ||
		errors.Is(err, ErrTimeout) ||
		errors.Is(err, ErrConnectionFailed) ||
		errors.Is(err, ErrServiceNotFound) ||
		errors.Is(err, ErrCircuitBreakerOpen)
}

// IsNotFound checks if an error represents a "not found" condition.
// Use this to determine if a resource (agent, capability, service)
// doesn't exist, allowing for appropriate handling like creating
// the resource or returning a specific HTTP status code.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrAgentNotFound) ||
		errors.Is(err, ErrCapabilityNotFound) ||
		errors.Is(err, ErrServiceNotFound)
}

// IsConfigurationError checks if an error is configuration-related.
// Configuration errors typically indicate problems with setup or
// initialization that require user intervention to fix.
func IsConfigurationError(err error) bool {
	return errors.Is(err, ErrInvalidConfiguration) ||
		errors.Is(err, ErrMissingConfiguration)
}

// IsStateError checks if an error is related to invalid state transitions.
// State errors occur when an operation is attempted in an inappropriate
// state (e.g., starting an already running service, using an uninitialized component).
func IsStateError(err error) bool {
	return errors.Is(err, ErrAlreadyStarted) ||
		errors.Is(err, ErrNotInitialized) ||
		errors.Is(err, ErrAlreadyRegistered) ||
		errors.Is(err, ErrAgentNotReady)
}

// FrameworkError represents a framework-level error with context.
// This structured error type provides detailed information about failures,
// including the operation that failed, the kind of error, and any underlying errors.
// Use this for errors that need to convey rich context to callers.
type FrameworkError struct {
	Op      string // Operation that failed
	Kind    string // Kind of error (e.g., "validation", "configuration")
	Message string // Human-readable message
	Err     error  // Underlying error
}

// Error implements the error interface.
// Returns a formatted error string that includes all context information.
func (e *FrameworkError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s %s: %s: %v", e.Op, e.Kind, e.Message, e.Err)
	}
	return fmt.Sprintf("%s %s: %s", e.Op, e.Kind, e.Message)
}

// Unwrap returns the underlying error.
// This enables error wrapping and unwrapping with errors.Is() and errors.As().
func (e *FrameworkError) Unwrap() error {
	return e.Err
}
