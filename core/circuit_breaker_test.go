package core

import (
	"testing"
	"time"
)

// TestDefaultCircuitBreakerParams tests the DefaultCircuitBreakerParams function
func TestDefaultCircuitBreakerParams(t *testing.T) {
	testName := "test-circuit-breaker"
	params := DefaultCircuitBreakerParams(testName)

	// Verify name is set correctly
	if params.Name != testName {
		t.Errorf("Name = %q, want %q", params.Name, testName)
	}

	// Verify config is not nil and has reasonable defaults
	if params.Config.Threshold <= 0 {
		t.Errorf("Config.Threshold = %d, want > 0", params.Config.Threshold)
	}

	if params.Config.Timeout <= 0 {
		t.Errorf("Config.Timeout = %v, want > 0", params.Config.Timeout)
	}

	if params.Config.HalfOpenRequests <= 0 {
		t.Errorf("Config.HalfOpenRequests = %d, want > 0", params.Config.HalfOpenRequests)
	}

	// Verify circuit breaker is enabled by default
	if !params.Config.Enabled {
		t.Error("Config.Enabled = false, want true")
	}

	// Verify specific expected default values
	expectedThreshold := 5
	if params.Config.Threshold != expectedThreshold {
		t.Errorf("Config.Threshold = %d, want %d", params.Config.Threshold, expectedThreshold)
	}

	expectedTimeout := 30 * time.Second
	if params.Config.Timeout != expectedTimeout {
		t.Errorf("Config.Timeout = %v, want %v", params.Config.Timeout, expectedTimeout)
	}

	expectedHalfOpenRequests := 3
	if params.Config.HalfOpenRequests != expectedHalfOpenRequests {
		t.Errorf("Config.HalfOpenRequests = %d, want %d", params.Config.HalfOpenRequests, expectedHalfOpenRequests)
	}

	// Verify that successive calls with same name return same values (pure function)
	params2 := DefaultCircuitBreakerParams(testName)
	if params.Name != params2.Name {
		t.Error("DefaultCircuitBreakerParams() should return consistent Name")
	}
	if params.Config.Threshold != params2.Config.Threshold {
		t.Error("DefaultCircuitBreakerParams() should return consistent Threshold")
	}
	if params.Config.Timeout != params2.Config.Timeout {
		t.Error("DefaultCircuitBreakerParams() should return consistent Timeout")
	}
	if params.Config.HalfOpenRequests != params2.Config.HalfOpenRequests {
		t.Error("DefaultCircuitBreakerParams() should return consistent HalfOpenRequests")
	}

	// Test with different names
	otherName := "other-circuit-breaker"
	params3 := DefaultCircuitBreakerParams(otherName)
	if params3.Name != otherName {
		t.Errorf("Name with different input = %q, want %q", params3.Name, otherName)
	}
	// Config should be the same regardless of name
	if params3.Config.Threshold != expectedThreshold {
		t.Error("Config should be same regardless of name")
	}

	// Test empty name
	emptyParams := DefaultCircuitBreakerParams("")
	if emptyParams.Name != "" {
		t.Errorf("Name with empty input = %q, want empty string", emptyParams.Name)
	}

	// Verify the returned params are suitable for circuit breaker usage
	t.Logf("Default circuit breaker params for %q: Enabled=%v, Threshold=%d, Timeout=%v, HalfOpenRequests=%d",
		params.Name, params.Config.Enabled, params.Config.Threshold, params.Config.Timeout, params.Config.HalfOpenRequests)

	// Test that we can modify the returned struct without affecting future calls
	originalThreshold := params.Config.Threshold
	params.Config.Threshold = 999
	params4 := DefaultCircuitBreakerParams(testName)
	if params4.Config.Threshold != originalThreshold {
		t.Error("Modifying returned params should not affect future calls")
	}
}
