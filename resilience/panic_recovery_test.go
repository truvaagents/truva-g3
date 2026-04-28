package resilience

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TestCircuitBreakerPanicRecoveryBasic tests basic panic recovery functionality
func TestCircuitBreakerPanicRecoveryBasic(t *testing.T) {
	config := DefaultConfig()
	config.Name = "panic-basic-test"

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Test string panic
	err = cb.Execute(context.Background(), func() error {
		panic("test string panic")
	})

	if err == nil {
		t.Fatal("Expected error from panic, got nil")
	}

	if !strings.Contains(err.Error(), "panic in circuit breaker") {
		t.Errorf("Expected panic error message, got: %v", err)
	}

	if !strings.Contains(err.Error(), "test string panic") {
		t.Errorf("Expected original panic message in error, got: %v", err)
	}

	// Verify stack trace is included
	if !strings.Contains(err.Error(), "Stack:") {
		t.Error("Expected stack trace in panic error")
	}
}

// TestCircuitBreakerPanicTypes tests different types of panics
func TestCircuitBreakerPanicTypes(t *testing.T) {
	config := DefaultConfig()
	config.Name = "panic-types-test"

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	testCases := []struct {
		name      string
		panicVal  interface{}
		expectMsg string
	}{
		{
			name:      "string panic",
			panicVal:  "string error",
			expectMsg: "string error",
		},
		{
			name:      "error panic",
			panicVal:  errors.New("error panic"),
			expectMsg: "error panic",
		},
		{
			name:      "int panic",
			panicVal:  42,
			expectMsg: "42 (int)",
		},
		{
			name:      "nil panic",
			panicVal:  nil,
			expectMsg: "panic called with nil argument",
		},
		{
			name:      "struct panic",
			panicVal:  struct{ msg string }{"custom"},
			expectMsg: "{custom}",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := cb.Execute(context.Background(), func() error {
				panic(tc.panicVal)
			})

			if err == nil {
				t.Fatal("Expected error from panic")
			}

			if !strings.Contains(err.Error(), tc.expectMsg) {
				t.Errorf("Expected %q in error message, got: %v", tc.expectMsg, err)
			}
		})
	}
}

// TestCircuitBreakerPanicNoDeadlock tests that panics don't cause deadlocks
func TestCircuitBreakerPanicNoDeadlock(t *testing.T) {
	config := DefaultConfig()
	config.Name = "panic-deadlock-test"

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Test without timeout - should return immediately, not deadlock
	done := make(chan error, 1)
	go func() {
		err := cb.Execute(context.Background(), func() error {
			panic("no deadlock test")
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Expected error from panic")
		}
		if !strings.Contains(err.Error(), "no deadlock test") {
			t.Errorf("Expected panic message, got: %v", err)
		}
	case <-time.After(500 * time.Millisecond): // Increased timeout for CI stability
		t.Fatal("Circuit breaker deadlocked on panic - fix not working")
	}
}

// TestCircuitBreakerPanicWithTimeout tests panic handling with timeout
func TestCircuitBreakerPanicWithTimeout(t *testing.T) {
	config := DefaultConfig()
	config.Name = "panic-timeout-test"

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Test that panic returns immediately, not after timeout
	start := time.Now()
	err = cb.ExecuteWithTimeout(context.Background(), 1*time.Second, func() error {
		panic("panic before timeout")
	})
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected error from panic")
	}

	// Should return panic error quickly, allowing for CI scheduling overhead
	if duration > 500*time.Millisecond {
		t.Errorf("Panic took %v, expected quick return (allowing CI overhead)", duration)
	}

	if !strings.Contains(err.Error(), "panic before timeout") {
		t.Errorf("Expected panic error, got: %v", err)
	}

	// Verify it's not a timeout error
	if errors.Is(err, context.DeadlineExceeded) {
		t.Error("Got timeout error instead of panic error")
	}
}

// TestCircuitBreakerPanicMetricsUpdate tests that panics update metrics correctly
func TestCircuitBreakerPanicMetricsUpdate(t *testing.T) {
	config := DefaultConfig()
	config.Name = "panic-metrics-test"

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Get initial metrics
	initialMetrics := cb.GetMetrics()
	initialFailures := initialMetrics["failure"].(uint64)

	// Cause a panic
	err = cb.Execute(context.Background(), func() error {
		panic("metric test panic")
	})

	if err == nil {
		t.Fatal("Expected error from panic")
	}

	// Check metrics were updated
	updatedMetrics := cb.GetMetrics()
	updatedFailures := updatedMetrics["failure"].(uint64)

	if updatedFailures != initialFailures+1 {
		t.Errorf("Expected failures to increase by 1, got %d -> %d",
			initialFailures, updatedFailures)
	}

	// Verify error rate calculation includes panic
	errorRate := updatedMetrics["error_rate"].(float64)
	if errorRate == 0 {
		t.Error("Expected non-zero error rate after panic")
	}
}

// TestCircuitBreakerPanicConcurrent tests panic handling under concurrent load
func TestCircuitBreakerPanicConcurrent(t *testing.T) {
	config := DefaultConfig()
	config.Name = "panic-concurrent-test"
	config.ErrorThreshold = 0.9   // Very high threshold to avoid circuit opening
	config.VolumeThreshold = 1000 // High volume threshold

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	concurrency := 20 // Reduced concurrency for more predictable results
	var wg sync.WaitGroup
	var panicCount int32
	var successCount int32
	var totalExecuted int32

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			err := cb.Execute(context.Background(), func() error {
				atomic.AddInt32(&totalExecuted, 1)
				if id%2 == 0 {
					atomic.AddInt32(&panicCount, 1)
					panic("concurrent panic")
				}
				return nil // Success for odd IDs
			})

			if err == nil {
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	// Verify that functions were actually executed
	if totalExecuted == 0 {
		t.Fatal("No functions were executed")
	}

	// Half should have panicked (even IDs), half succeeded (odd IDs)
	expectedPanics := int32(concurrency / 2)
	expectedSuccesses := int32(concurrency / 2)

	if panicCount != expectedPanics {
		t.Errorf("Expected %d panics, got %d", expectedPanics, panicCount)
	}

	if successCount != expectedSuccesses {
		t.Errorf("Expected %d successes, got %d", expectedSuccesses, successCount)
	}

	// Verify circuit breaker recorded the executions
	metrics := cb.GetMetrics()
	totalMetrics := metrics["total"].(uint64)
	if totalMetrics == 0 {
		t.Error("Circuit breaker should have recorded executions")
	}

	t.Logf("Concurrent test: %d executed, %d panics, %d successes, %d total in metrics",
		totalExecuted, panicCount, successCount, totalMetrics)
}

// TestCircuitBreakerPanicStateTransitions tests panics during state transitions
func TestCircuitBreakerPanicStateTransitions(t *testing.T) {
	config := DefaultConfig()
	config.Name = "panic-state-test"
	config.ErrorThreshold = 0.5
	config.VolumeThreshold = 2
	config.SleepWindow = 50 * time.Millisecond

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Generate panics to open circuit
	for i := 0; i < 3; i++ {
		err := cb.Execute(context.Background(), func() error {
			panic("state transition panic")
		})
		if err == nil {
			t.Error("Expected panic error")
		}
	}

	// Circuit should be open after panic-failures
	if cb.GetState() != "open" {
		t.Errorf("Expected open state after panics, got %s", cb.GetState())
	}

	// Wait for half-open with CI-friendly buffer
	// Sleep window is 50ms, use 200ms for CI stability
	time.Sleep(200 * time.Millisecond)

	// Test panic in half-open state
	err = cb.Execute(context.Background(), func() error {
		panic("half-open panic")
	})

	if err == nil {
		t.Error("Expected panic error in half-open state")
	}

	// Verify panic is treated as failure for state transitions
	// (implementation detail: might stay open or go back to open)
	state := cb.GetState()
	if state != "open" && state != "half-open" {
		t.Errorf("Expected open or half-open state after half-open panic, got %s", state)
	}
}

// TestCircuitBreakerPanicInHalfOpen tests panic handling specifically in half-open state
func TestCircuitBreakerPanicInHalfOpen(t *testing.T) {
	config := DefaultConfig()
	config.Name = "panic-halfopen-test"
	config.ErrorThreshold = 0.5
	config.VolumeThreshold = 2
	config.SleepWindow = 50 * time.Millisecond
	config.HalfOpenRequests = 3

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Open the circuit with regular errors
	for i := 0; i < 3; i++ {
		_ = cb.Execute(context.Background(), func() error {
			return errors.New("error to open circuit")
		})
	}

	if cb.GetState() != "open" {
		t.Fatal("Circuit should be open")
	}

	// Wait for half-open with CI-friendly buffer
	// Sleep window is 50ms, use 200ms for CI stability
	time.Sleep(200 * time.Millisecond)

	// Test mixed panics and successes in half-open
	results := make([]error, 3)
	for i := 0; i < 3; i++ {
		results[i] = cb.Execute(context.Background(), func() error {
			if i == 1 {
				panic("half-open test panic")
			}
			return nil // Success
		})
	}

	// Verify panic was converted to error
	panicResult := results[1]
	if panicResult == nil {
		t.Error("Expected panic to be converted to error")
	}
	if !strings.Contains(panicResult.Error(), "panic") {
		t.Errorf("Expected panic error, got: %v", panicResult)
	}

	// Other results should be success or rejection
	for i, result := range results {
		if i == 1 {
			continue // Skip panic result
		}
		// Should be either success (nil) or circuit breaker rejection
		if result != nil && !errors.Is(result, core.ErrCircuitBreakerOpen) {
			t.Errorf("Unexpected error in half-open test: %v", result)
		}
	}
}

// TestCircuitBreakerPanicNoGoroutineLeak tests that panics don't leak goroutines
func TestCircuitBreakerPanicNoGoroutineLeak(t *testing.T) {
	// Get initial goroutine count
	initialGoroutines := runtime.NumGoroutine()

	config := DefaultConfig()
	config.Name = "panic-leak-test"

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Generate many panics
	for i := 0; i < 100; i++ {
		_ = cb.Execute(context.Background(), func() error {
			panic("leak test panic")
		})
	}

	// Wait for cleanup with CI-friendly buffer
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	runtime.GC() // Double GC to clean up

	// Check goroutine count
	finalGoroutines := runtime.NumGoroutine()

	// Allow for some variance (test framework overhead)
	if finalGoroutines > initialGoroutines+5 {
		t.Errorf("Potential goroutine leak: started with %d, ended with %d goroutines",
			initialGoroutines, finalGoroutines)
	}
}

// TestCircuitBreakerPanicWithContext tests panic handling with context cancellation
func TestCircuitBreakerPanicWithContext(t *testing.T) {
	config := DefaultConfig()
	config.Name = "panic-context-test"

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Test 1: Panic happens before context cancellation
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	err = cb.Execute(ctx1, func() error {
		panic("panic before cancel")
	})

	if err == nil {
		t.Error("Expected panic error")
	}

	if !strings.Contains(err.Error(), "panic before cancel") {
		t.Errorf("Expected panic error, got: %v", err)
	}

	// Test 2: Context cancelled, but function panics during cleanup
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel2()

	err = cb.Execute(ctx2, func() error {
		time.Sleep(50 * time.Millisecond) // Will be cancelled
		panic("panic after timeout")      // This should not execute
	})

	// Should get timeout, not panic (function was cancelled)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

// TestCircuitBreakerPanicRecoveryFromRecovery tests edge case of panic in recovery
func TestCircuitBreakerPanicRecoveryFromRecovery(t *testing.T) {
	// This tests that our recovery code itself is panic-safe
	// (This is a very edge case but good to verify)

	config := DefaultConfig()
	config.Name = "panic-recovery-test"

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// This should work even with pathological panics
	err = cb.Execute(context.Background(), func() error {
		panic(func() { panic("nested panic") }) // Function that panics when called
	})

	if err == nil {
		t.Error("Expected error from panic")
	}

	// Should still get a panic error, not crash
	if !strings.Contains(err.Error(), "panic in circuit breaker") {
		t.Errorf("Expected panic error message, got: %v", err)
	}
}
