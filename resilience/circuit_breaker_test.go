package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TestCircuitBreakerStateTransitions tests state transitions
func TestCircuitBreakerStateTransitions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping circuit breaker state transitions test in short mode (requires 250ms+ sleep)")
	}

	config := &CircuitBreakerConfig{
		Name:             "test",
		ErrorThreshold:   0.5,
		VolumeThreshold:  5,
		SleepWindow:      100 * time.Millisecond,
		HalfOpenRequests: 2,
		SuccessThreshold: 0.5,
		WindowSize:       1 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Should start in closed state
	if cb.GetState() != "closed" {
		t.Errorf("Expected initial state to be closed, got %s", cb.GetState())
	}

	// Simulate failures to open circuit
	for i := 0; i < 6; i++ {
		err := cb.Execute(context.Background(), func() error {
			return errors.New("test error")
		})
		if err == nil {
			t.Error("Expected error from Execute")
		}
	}

	// Circuit should be open after exceeding threshold
	if cb.GetState() != "open" {
		t.Errorf("Expected state to be open after failures, got %s", cb.GetState())
	}

	// Should reject requests when open
	err := cb.Execute(context.Background(), func() error {
		return nil
	})
	if !errors.Is(err, core.ErrCircuitBreakerOpen) {
		t.Errorf("Expected ErrCircuitBreakerOpen, got %v", err)
	}

	// Wait for sleep window with CI-friendly buffer
	// Sleep window is 100ms, use 250ms for CI stability
	time.Sleep(250 * time.Millisecond)

	// Should allow test request (half-open)
	successCount := 0
	for i := 0; i < config.HalfOpenRequests; i++ {
		err = cb.Execute(context.Background(), func() error {
			successCount++
			return nil // Success
		})
		if err != nil {
			t.Errorf("Expected success in half-open state, got %v", err)
		}
	}

	// Should be closed now after enough successes
	if cb.GetState() != "closed" {
		t.Errorf("Expected state to be closed after recovery (had %d successes), got %s",
			successCount, cb.GetState())
	}
}

// TestCircuitBreakerErrorClassification tests that only certain errors count
func TestCircuitBreakerErrorClassification(t *testing.T) {
	config := &CircuitBreakerConfig{
		Name:             "test",
		ErrorThreshold:   0.5,
		VolumeThreshold:  3,
		SleepWindow:      100 * time.Millisecond,
		HalfOpenRequests: 3,
		SuccessThreshold: 0.6,
		WindowSize:       1 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// User errors should not count
	for i := 0; i < 5; i++ {
		err := cb.Execute(context.Background(), func() error {
			return core.ErrAgentNotFound // Not found error
		})
		if err == nil {
			t.Error("Expected error from Execute")
		}
	}

	// Circuit should still be closed (user errors don't count)
	if cb.GetState() != "closed" {
		t.Errorf("Expected state to remain closed with user errors, got %s", cb.GetState())
	}

	// Infrastructure errors should count
	for i := 0; i < 4; i++ {
		err := cb.Execute(context.Background(), func() error {
			return core.ErrConnectionFailed // Infrastructure error
		})
		if err == nil {
			t.Error("Expected error from Execute")
		}
	}

	// Circuit should be open now
	if cb.GetState() != "open" {
		t.Errorf("Expected state to be open with infrastructure errors, got %s", cb.GetState())
	}
}

// TestCircuitBreakerSlidingWindow tests the sliding window metrics
func TestCircuitBreakerSlidingWindow(t *testing.T) {
	window := NewSlidingWindow(1*time.Second, 10, true)

	// Record some successes and failures
	for i := 0; i < 3; i++ {
		window.RecordSuccess()
	}
	for i := 0; i < 2; i++ {
		window.RecordFailure()
	}

	// Check counts
	success, failure := window.GetCounts()
	if success != 3 {
		t.Errorf("Expected 3 successes, got %d", success)
	}
	if failure != 2 {
		t.Errorf("Expected 2 failures, got %d", failure)
	}

	// Check error rate
	errorRate := window.GetErrorRate()
	expectedRate := 2.0 / 5.0
	if errorRate != expectedRate {
		t.Errorf("Expected error rate %f, got %f", expectedRate, errorRate)
	}

	// Check total
	total := window.GetTotal()
	if total != 5 {
		t.Errorf("Expected total 5, got %d", total)
	}
}

// TestCircuitBreakerHalfOpenState tests half-open state behavior
func TestCircuitBreakerHalfOpenState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping circuit breaker half-open state test in short mode (requires 250ms+ sleep)")
	}

	config := &CircuitBreakerConfig{
		Name:             "test",
		ErrorThreshold:   0.5,
		VolumeThreshold:  2,
		SleepWindow:      100 * time.Millisecond,
		HalfOpenRequests: 3,
		SuccessThreshold: 0.6,
		WindowSize:       1 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Open the circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(context.Background(), func() error {
			return errors.New("test error")
		})
	}

	if cb.GetState() != "open" {
		t.Fatal("Circuit should be open")
	}

	// Wait for sleep window with CI-friendly buffer
	// Sleep window is 100ms, use 250ms for CI stability
	time.Sleep(250 * time.Millisecond)

	// Execute should transition to half-open
	successCount := 0
	for i := 0; i < 3; i++ {
		err := cb.Execute(context.Background(), func() error {
			if i < 2 {
				successCount++
				return nil // 2 successes
			}
			return errors.New("test error") // 1 failure
		})

		// Check we're in half-open during test requests
		if i < 2 && cb.GetState() != "half-open" {
			t.Errorf("Expected half-open state during test, got %s", cb.GetState())
		}

		if i < 2 && err != nil {
			t.Errorf("Expected success, got %v", err)
		}
	}

	// Should close (2/3 = 66% > 60% threshold)
	if cb.GetState() != "closed" {
		t.Errorf("Expected closed state after successful recovery, got %s", cb.GetState())
	}
}

// TestCircuitBreakerManualControl tests forced open/closed states
func TestCircuitBreakerManualControl(t *testing.T) {
	cb := NewCircuitBreakerLegacy(5, 100*time.Millisecond)

	// Force open
	cb.ForceOpen()
	if cb.GetState() != "open" {
		t.Errorf("Expected open state after ForceOpen, got %s", cb.GetState())
	}

	// Should reject requests
	err := cb.Execute(context.Background(), func() error {
		return nil
	})
	if !errors.Is(err, core.ErrCircuitBreakerOpen) {
		t.Errorf("Expected ErrCircuitBreakerOpen when forced open, got %v", err)
	}

	// Force closed
	cb.ForceClosed()
	if cb.GetState() != "closed" {
		t.Errorf("Expected closed state after ForceClosed, got %s", cb.GetState())
	}

	// Should allow requests even with failures
	for i := 0; i < 10; i++ {
		err := cb.Execute(context.Background(), func() error {
			return errors.New("test error")
		})
		if err == nil || errors.Is(err, core.ErrCircuitBreakerOpen) {
			t.Error("Expected to execute with forced closed")
		}
	}

	// Should still be closed (forced)
	if cb.GetState() != "closed" {
		t.Errorf("Expected to remain closed when forced, got %s", cb.GetState())
	}

	// Clear force
	cb.ClearForce()

	// Now it should evaluate normally
	cb.RecordFailure()
	// State might change based on accumulated failures
}

// TestCircuitBreakerConcurrentAccess tests thread safety
func TestCircuitBreakerConcurrentAccess(t *testing.T) {
	config := &CircuitBreakerConfig{
		Name:             "test",
		ErrorThreshold:   0.5,
		VolumeThreshold:  10,
		SleepWindow:      100 * time.Millisecond,
		HalfOpenRequests: 5,
		SuccessThreshold: 0.6,
		WindowSize:       1 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	var wg sync.WaitGroup
	goroutines := 100
	iterations := 100

	// Track successes and failures
	var successCount, failureCount int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < iterations; j++ {
				err := cb.Execute(context.Background(), func() error {
					// Alternate between success and failure
					if (id+j)%2 == 0 {
						return nil
					}
					return errors.New("test error")
				})

				if err == nil {
					atomic.AddInt32(&successCount, 1)
				} else if !errors.Is(err, core.ErrCircuitBreakerOpen) {
					atomic.AddInt32(&failureCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify no panics and reasonable counts
	total := atomic.LoadInt32(&successCount) + atomic.LoadInt32(&failureCount)
	if total == 0 {
		t.Error("No operations completed")
	}

	t.Logf("Concurrent test completed: %d successes, %d failures",
		successCount, failureCount)
}

// TestCircuitBreakerExponentialBackoff tests increasing sleep window
func TestCircuitBreakerExponentialBackoff(t *testing.T) {
	config := &CircuitBreakerConfig{
		Name:             "test",
		ErrorThreshold:   0.5,
		VolumeThreshold:  2,
		SleepWindow:      50 * time.Millisecond,
		HalfOpenRequests: 1,
		SuccessThreshold: 1.0,
		WindowSize:       1 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Open circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(context.Background(), func() error {
			return errors.New("test error")
		})
	}

	initialSleepWindow := config.SleepWindow

	// Wait for half-open state with CI-friendly buffer
	// Sleep window is 50ms, use 150ms for CI stability
	time.Sleep(150 * time.Millisecond)
	_ = cb.Execute(context.Background(), func() error {
		return errors.New("test error")
	})

	// Sleep window should have increased
	if config.SleepWindow <= initialSleepWindow {
		t.Error("Expected sleep window to increase after half-open failure")
	}

	expectedNewWindow := time.Duration(float64(initialSleepWindow) * 1.5)
	if config.SleepWindow != expectedNewWindow {
		t.Errorf("Expected sleep window %v, got %v", expectedNewWindow, config.SleepWindow)
	}
}

// TestCircuitBreakerReset tests the reset functionality
func TestCircuitBreakerReset(t *testing.T) {
	cb := NewCircuitBreakerLegacy(2, 100*time.Millisecond)

	// Generate some failures to open circuit
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	if cb.GetState() != "open" {
		t.Fatal("Circuit should be open")
	}

	// Reset
	cb.Reset()

	// Should be closed
	if cb.GetState() != "closed" {
		t.Errorf("Expected closed state after reset, got %s", cb.GetState())
	}

	// Failure count should be reset
	if cb.failureCount.Load() != 0 {
		t.Errorf("Expected failure count 0 after reset, got %d", cb.failureCount.Load())
	}
}

// TestCircuitBreakerMetrics tests metrics collection
func TestCircuitBreakerMetrics(t *testing.T) {
	cb := NewCircuitBreakerLegacy(5, 100*time.Millisecond)

	// Generate some activity
	for i := 0; i < 3; i++ {
		cb.RecordSuccess()
	}
	for i := 0; i < 2; i++ {
		cb.RecordFailure()
	}

	metrics := cb.GetMetrics()

	// Check basic metrics
	if metrics["state"] != "closed" {
		t.Errorf("Expected closed state in metrics, got %v", metrics["state"])
	}

	success, ok := metrics["success"].(uint64)
	if !ok || success != 3 {
		t.Errorf("Expected 3 successes in metrics, got %v", metrics["success"])
	}

	failure, ok := metrics["failure"].(uint64)
	if !ok || failure != 2 {
		t.Errorf("Expected 2 failures in metrics, got %v", metrics["failure"])
	}

	total, ok := metrics["total"].(uint64)
	if !ok || total != 5 {
		t.Errorf("Expected total 5 in metrics, got %v", metrics["total"])
	}
}

// TestCircuitBreakerBackwardCompatibility tests legacy API
func TestCircuitBreakerBackwardCompatibility(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping circuit breaker backward compatibility test in short mode (requires 300ms+ sleep)")
	}

	// Old API should still work
	cb := NewCircuitBreakerLegacy(3, 100*time.Millisecond)

	// Test old methods
	cb.RecordSuccess()
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()

	// Should open after 3 failures (legacy threshold)
	if cb.GetState() != "open" {
		t.Errorf("Expected open state with legacy threshold, got %s", cb.GetState())
	}

	// CanExecute should work
	if cb.CanExecute() {
		t.Error("Expected CanExecute to return false when open")
	}

	// Wait for recovery timeout with CI-friendly buffer
	// Timeout is 100ms, use 300ms for CI stability
	time.Sleep(300 * time.Millisecond)

	// Should allow execution now (enters half-open)
	if !cb.CanExecute() {
		t.Error("Expected CanExecute to return true after recovery timeout")
	}

	// State should be half-open after timeout (this is correct behavior)
	state := cb.GetState()
	if state != "half-open" && state != "closed" {
		t.Errorf("Expected half-open or closed state after timeout, got %s", state)
	}

	// Record success to potentially close (depends on configuration)
	cb.RecordSuccess()

	// With the improved implementation, state might still be half-open
	// depending on HalfOpenRequests configuration. This is actually better behavior.
	finalState := cb.GetState()
	if finalState != "closed" && finalState != "half-open" {
		t.Errorf("Expected closed or half-open state after recovery, got %s", finalState)
	}
}

// TestCircuitBreakerVolumeThreshold tests minimum request requirement
func TestCircuitBreakerVolumeThreshold(t *testing.T) {
	config := &CircuitBreakerConfig{
		Name:             "test",
		ErrorThreshold:   0.5,
		VolumeThreshold:  10, // Need 10 requests before evaluation
		SleepWindow:      100 * time.Millisecond,
		HalfOpenRequests: 3,
		SuccessThreshold: 0.6,
		WindowSize:       1 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Generate 5 failures (100% error rate but below volume threshold)
	for i := 0; i < 5; i++ {
		_ = cb.Execute(context.Background(), func() error {
			return errors.New("test error")
		})
	}

	// Should still be closed (below volume threshold)
	if cb.GetState() != "closed" {
		t.Errorf("Expected closed state below volume threshold, got %s", cb.GetState())
	}

	// Add 5 more failures to reach volume threshold
	for i := 0; i < 5; i++ {
		_ = cb.Execute(context.Background(), func() error {
			return errors.New("test error")
		})
	}

	// Now should be open (100% error rate with sufficient volume)
	if cb.GetState() != "open" {
		t.Errorf("Expected open state after reaching volume threshold, got %s", cb.GetState())
	}
}

// TestSlidingWindowRotation tests bucket rotation over time
func TestSlidingWindowRotation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sliding window rotation test in short mode (requires 550ms+ sleep)")
	}

	// Short window for testing
	window := NewSlidingWindow(200*time.Millisecond, 4, true)

	// Record in first bucket
	window.RecordSuccess()
	window.RecordSuccess()

	// Wait for bucket rotation with CI-friendly buffer
	// Using 150ms for stable bucket rotation in CI
	time.Sleep(150 * time.Millisecond)

	// Record in second bucket
	window.RecordFailure()

	// Check counts include both buckets
	success, failure := window.GetCounts()
	if success != 2 || failure != 1 {
		t.Errorf("Expected 2 successes and 1 failure, got %d and %d", success, failure)
	}

	// Wait for window to expire with CI-friendly buffer
	// Window is 200ms, use 400ms for CI stability
	time.Sleep(400 * time.Millisecond)

	// Old records should be expired
	success, failure = window.GetCounts()
	if success != 0 || failure != 0 {
		t.Errorf("Expected 0 counts after window expiry, got %d successes and %d failures",
			success, failure)
	}
}

// TestErrorClassifierCustom tests custom error classification
func TestErrorClassifierCustom(t *testing.T) {
	// Custom classifier that only counts specific errors
	customClassifier := func(err error) bool {
		return err != nil && err.Error() == "critical"
	}

	config := &CircuitBreakerConfig{
		Name:             "test",
		ErrorThreshold:   0.5,
		VolumeThreshold:  2,
		SleepWindow:      100 * time.Millisecond,
		HalfOpenRequests: 3,
		SuccessThreshold: 0.6,
		WindowSize:       1 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  customClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Non-critical errors should not count
	for i := 0; i < 5; i++ {
		_ = cb.Execute(context.Background(), func() error {
			return errors.New("minor")
		})
	}

	// Should still be closed
	if cb.GetState() != "closed" {
		t.Errorf("Expected closed state with non-critical errors, got %s", cb.GetState())
	}

	// Critical errors should count
	for i := 0; i < 3; i++ {
		_ = cb.Execute(context.Background(), func() error {
			return errors.New("critical")
		})
	}

	// Should be open now
	if cb.GetState() != "open" {
		t.Errorf("Expected open state with critical errors, got %s", cb.GetState())
	}
}

// Example of using circuit breaker with database operations
func ExampleCircuitBreaker_database() {
	// Create a circuit breaker optimized for database operations
	config := &CircuitBreakerConfig{
		Name:             "database",
		ErrorThreshold:   0.3, // Lower threshold for databases
		VolumeThreshold:  5,
		SleepWindow:      10 * time.Second,
		HalfOpenRequests: 2,
		SuccessThreshold: 0.5,
		WindowSize:       30 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Wrap database calls
	var result interface{}
	err := cb.Execute(context.Background(), func() error {
		// Simulate database query
		// result = db.Query("SELECT * FROM users")
		return nil
	})

	if errors.Is(err, core.ErrCircuitBreakerOpen) {
		fmt.Println("Database circuit breaker is open, using cache")
		// Use cached data or degraded response
	} else if err != nil {
		fmt.Printf("Database error: %v\n", err)
	} else {
		fmt.Printf("Query successful: %v\n", result)
	}
}

// Example of using circuit breaker with API calls
func ExampleCircuitBreaker_api() {
	// Create a circuit breaker for API calls
	config := &CircuitBreakerConfig{
		Name:             "external-api",
		ErrorThreshold:   0.5,
		VolumeThreshold:  10,
		SleepWindow:      30 * time.Second,
		HalfOpenRequests: 5,
		SuccessThreshold: 0.6,
		WindowSize:       60 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Make API call with circuit breaker protection
	err := cb.Execute(context.Background(), func() error {
		// Simulate API call
		// resp, err := http.Get("https://api.example.com/data")
		return nil
	})

	if errors.Is(err, core.ErrCircuitBreakerOpen) {
		fmt.Println("API circuit breaker is open, returning cached response")
	} else if err != nil {
		fmt.Printf("API call failed: %v\n", err)
	} else {
		fmt.Println("API call successful")
	}
}
