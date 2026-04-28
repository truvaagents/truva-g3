package core

import (
	"errors"
	"fmt"
	"testing"
)

// Test IsRetryable function
func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "ErrDiscoveryUnavailable is retryable",
			err:      ErrDiscoveryUnavailable,
			expected: true,
		},
		{
			name:     "ErrTimeout is retryable",
			err:      ErrTimeout,
			expected: true,
		},
		{
			name:     "ErrConnectionFailed is retryable",
			err:      ErrConnectionFailed,
			expected: true,
		},
		{
			name:     "ErrServiceNotFound is retryable",
			err:      ErrServiceNotFound,
			expected: true,
		},
		{
			name:     "ErrCircuitBreakerOpen is retryable",
			err:      ErrCircuitBreakerOpen,
			expected: true,
		},
		{
			name:     "wrapped retryable error is retryable",
			err:      fmt.Errorf("operation failed: %w", ErrTimeout),
			expected: true,
		},
		{
			name:     "ErrAgentNotFound is not retryable",
			err:      ErrAgentNotFound,
			expected: false,
		},
		{
			name:     "ErrInvalidConfiguration is not retryable",
			err:      ErrInvalidConfiguration,
			expected: false,
		},
		{
			name:     "custom error is not retryable",
			err:      errors.New("custom error"),
			expected: false,
		},
		{
			name:     "nil error is not retryable",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsRetryable(tt.err)
			if result != tt.expected {
				t.Errorf("IsRetryable(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// Test IsNotFound function
func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "ErrAgentNotFound is not found",
			err:      ErrAgentNotFound,
			expected: true,
		},
		{
			name:     "ErrCapabilityNotFound is not found",
			err:      ErrCapabilityNotFound,
			expected: true,
		},
		{
			name:     "ErrServiceNotFound is not found",
			err:      ErrServiceNotFound,
			expected: true,
		},
		{
			name:     "wrapped not found error is detected",
			err:      fmt.Errorf("failed to locate: %w", ErrAgentNotFound),
			expected: true,
		},
		{
			name:     "ErrTimeout is not a not-found error",
			err:      ErrTimeout,
			expected: false,
		},
		{
			name:     "ErrInvalidConfiguration is not a not-found error",
			err:      ErrInvalidConfiguration,
			expected: false,
		},
		{
			name:     "custom error is not a not-found error",
			err:      errors.New("something else"),
			expected: false,
		},
		{
			name:     "nil error is not a not-found error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsNotFound(tt.err)
			if result != tt.expected {
				t.Errorf("IsNotFound(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// Test IsConfigurationError function
func TestIsConfigurationError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "ErrInvalidConfiguration is configuration error",
			err:      ErrInvalidConfiguration,
			expected: true,
		},
		{
			name:     "ErrMissingConfiguration is configuration error",
			err:      ErrMissingConfiguration,
			expected: true,
		},
		{
			name:     "wrapped configuration error is detected",
			err:      fmt.Errorf("config validation failed: %w", ErrInvalidConfiguration),
			expected: true,
		},
		{
			name:     "ErrPortOutOfRange is not checked as configuration error",
			err:      ErrPortOutOfRange,
			expected: false,
		},
		{
			name:     "ErrAgentNotFound is not configuration error",
			err:      ErrAgentNotFound,
			expected: false,
		},
		{
			name:     "custom error is not configuration error",
			err:      errors.New("random error"),
			expected: false,
		},
		{
			name:     "nil error is not configuration error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsConfigurationError(tt.err)
			if result != tt.expected {
				t.Errorf("IsConfigurationError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// Test IsStateError function
func TestIsStateError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "ErrAlreadyStarted is state error",
			err:      ErrAlreadyStarted,
			expected: true,
		},
		{
			name:     "ErrNotInitialized is state error",
			err:      ErrNotInitialized,
			expected: true,
		},
		{
			name:     "ErrAlreadyRegistered is state error",
			err:      ErrAlreadyRegistered,
			expected: true,
		},
		{
			name:     "ErrAgentNotReady is state error",
			err:      ErrAgentNotReady,
			expected: true,
		},
		{
			name:     "wrapped state error is detected",
			err:      fmt.Errorf("cannot proceed: %w", ErrNotInitialized),
			expected: true,
		},
		{
			name:     "ErrTimeout is not state error",
			err:      ErrTimeout,
			expected: false,
		},
		{
			name:     "ErrAgentNotFound is not state error",
			err:      ErrAgentNotFound,
			expected: false,
		},
		{
			name:     "custom error is not state error",
			err:      errors.New("some other error"),
			expected: false,
		},
		{
			name:     "nil error is not state error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsStateError(tt.err)
			if result != tt.expected {
				t.Errorf("IsStateError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// Test error wrapping and unwrapping
func TestErrorWrapping(t *testing.T) {
	// Test that wrapped errors are properly detected
	baseErr := ErrAgentNotFound
	wrappedOnce := fmt.Errorf("failed to find agent 'test': %w", baseErr)
	wrappedTwice := fmt.Errorf("operation failed: %w", wrappedOnce)

	// All should be detected as not-found errors
	if !IsNotFound(baseErr) {
		t.Error("Base error should be detected as not-found")
	}
	if !IsNotFound(wrappedOnce) {
		t.Error("Once-wrapped error should be detected as not-found")
	}
	if !IsNotFound(wrappedTwice) {
		t.Error("Twice-wrapped error should be detected as not-found")
	}

	// Test with errors.Is directly
	if !errors.Is(wrappedTwice, ErrAgentNotFound) {
		t.Error("errors.Is should work through multiple wrapping layers")
	}
}

// Test combinations of errors
func TestErrorCombinations(t *testing.T) {
	// ErrServiceNotFound is both retryable and not-found
	if !IsRetryable(ErrServiceNotFound) {
		t.Error("ErrServiceNotFound should be retryable")
	}
	if !IsNotFound(ErrServiceNotFound) {
		t.Error("ErrServiceNotFound should be not-found")
	}

	// These errors should be mutually exclusive
	if IsConfigurationError(ErrTimeout) {
		t.Error("ErrTimeout should not be a configuration error")
	}
	if IsStateError(ErrInvalidConfiguration) {
		t.Error("ErrInvalidConfiguration should not be a state error")
	}
}

// Benchmark error checking functions
func BenchmarkIsRetryable(b *testing.B) {
	err := fmt.Errorf("wrapped: %w", ErrTimeout)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsRetryable(err)
	}
}

func BenchmarkIsNotFound(b *testing.B) {
	err := fmt.Errorf("wrapped: %w", ErrAgentNotFound)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsNotFound(err)
	}
}

func BenchmarkIsConfigurationError(b *testing.B) {
	err := fmt.Errorf("wrapped: %w", ErrInvalidConfiguration)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsConfigurationError(err)
	}
}

func BenchmarkIsStateError(b *testing.B) {
	err := fmt.Errorf("wrapped: %w", ErrNotInitialized)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsStateError(err)
	}
}
