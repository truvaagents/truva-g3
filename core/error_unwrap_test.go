package core

import (
	"errors"
	"testing"
)

// TestFrameworkError_Unwrap tests the Unwrap method for error unwrapping
func TestFrameworkError_Unwrap(t *testing.T) {
	// Test with wrapped error
	t.Run("with wrapped error", func(t *testing.T) {
		originalErr := errors.New("original error")
		wrappedErr := &FrameworkError{
			Op:      "test_operation",
			Kind:    "validation",
			Message: "configuration error",
			Err:     originalErr,
		}

		unwrapped := wrappedErr.Unwrap()
		if unwrapped != originalErr {
			t.Errorf("Unwrap() = %v, want %v", unwrapped, originalErr)
		}
	})

	// Test with nil wrapped error
	t.Run("with nil wrapped error", func(t *testing.T) {
		wrappedErr := &FrameworkError{
			Op:      "test_operation",
			Kind:    "validation",
			Message: "configuration error",
			Err:     nil,
		}

		unwrapped := wrappedErr.Unwrap()
		if unwrapped != nil {
			t.Errorf("Unwrap() = %v, want nil", unwrapped)
		}
	})

	// Test unwrapping chain with errors.Is
	t.Run("unwrapping chain with errors.Is", func(t *testing.T) {
		originalErr := ErrAgentNotFound
		wrappedErr := &FrameworkError{
			Op:      "lookup_agent",
			Kind:    "not_found",
			Message: "agent lookup failed",
			Err:     originalErr,
		}

		// Should be able to use errors.Is to check for the original error
		if !errors.Is(wrappedErr, originalErr) {
			t.Error("errors.Is() should find original error in wrapped error")
		}
	})

	// Test unwrapping chain with errors.As
	t.Run("unwrapping chain with errors.As", func(t *testing.T) {
		originalErr := &FrameworkError{
			Op:      "find_agent",
			Kind:    "not_found",
			Message: "agent not found",
			Err:     nil,
		}

		wrappedErr := &FrameworkError{
			Op:      "validate_config",
			Kind:    "validation",
			Message: "configuration error",
			Err:     originalErr,
		}

		var targetErr *FrameworkError
		if !errors.As(wrappedErr, &targetErr) {
			t.Error("errors.As() should find ComponentError in wrapped error")
		}

		// Should find the outermost error (wrappedErr)
		if targetErr != wrappedErr {
			t.Error("errors.As() should return the outermost FrameworkError")
		}
	})

	// Test multiple levels of wrapping
	t.Run("multiple levels of wrapping", func(t *testing.T) {
		baseErr := errors.New("base error")

		level1Err := &FrameworkError{
			Op:      "connect_service",
			Kind:    "connection",
			Message: "service error",
			Err:     baseErr,
		}

		level2Err := &FrameworkError{
			Op:      "validate_config",
			Kind:    "validation",
			Message: "config error",
			Err:     level1Err,
		}

		// Direct unwrap should return level1Err
		unwrapped := level2Err.Unwrap()
		if unwrapped != level1Err {
			t.Errorf("Unwrap() = %v, want %v", unwrapped, level1Err)
		}

		// errors.Is should find the base error through the chain
		if !errors.Is(level2Err, baseErr) {
			t.Error("errors.Is() should find base error through multiple wrapping levels")
		}

		// errors.Is should find intermediate error
		if !errors.Is(level2Err, level1Err) {
			t.Error("errors.Is() should find intermediate error")
		}
	})

	// Test with standard library error
	t.Run("with standard library error", func(t *testing.T) {
		stdErr := errors.New("standard error")
		wrappedErr := &FrameworkError{
			Op:      "connect",
			Kind:    "connection",
			Message: "connection failed",
			Err:     stdErr,
		}

		unwrapped := wrappedErr.Unwrap()
		if unwrapped != stdErr {
			t.Errorf("Unwrap() = %v, want %v", unwrapped, stdErr)
		}

		// Should work with errors.Is
		if !errors.Is(wrappedErr, stdErr) {
			t.Error("errors.Is() should work with standard library errors")
		}
	})
}
