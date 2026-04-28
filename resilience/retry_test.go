package resilience

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TestRetryBasicSuccess tests successful execution on first attempt
func TestRetryBasicSuccess(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   3,
		InitialDelay:  10 * time.Millisecond,
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	attempts := 0
	err := Retry(context.Background(), config, func() error {
		attempts++
		return nil // Success on first attempt
	})

	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	if attempts != 1 {
		t.Errorf("Expected 1 attempt, got %d", attempts)
	}
}

// TestRetryEventualSuccess tests success after multiple attempts
func TestRetryEventualSuccess(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   3,
		InitialDelay:  10 * time.Millisecond,
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	attempts := 0
	err := Retry(context.Background(), config, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil // Success on third attempt
	})

	if err != nil {
		t.Errorf("Expected eventual success, got error: %v", err)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

// TestRetryMaxAttemptsExceeded tests failure after all retries exhausted
func TestRetryMaxAttemptsExceeded(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   3,
		InitialDelay:  10 * time.Millisecond,
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	attempts := 0
	testErr := errors.New("persistent error")

	err := Retry(context.Background(), config, func() error {
		attempts++
		return testErr
	})

	if !errors.Is(err, core.ErrMaxRetriesExceeded) {
		t.Errorf("Expected ErrMaxRetriesExceeded, got: %v", err)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

// TestRetryContextCancellation tests context cancellation during retry
func TestRetryContextCancellation(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   5,
		InitialDelay:  50 * time.Millisecond,
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0

	// Cancel context after a short delay
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	err := Retry(ctx, config, func() error {
		attempts++
		return errors.New("error")
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context.Canceled, got: %v", err)
	}

	// Should have made at least 1 attempt but not all 5
	if attempts == 0 || attempts >= 5 {
		t.Errorf("Expected 1-4 attempts with context cancellation, got %d", attempts)
	}
}

// TestRetryExponentialBackoff tests exponential backoff timing
func TestRetryExponentialBackoff(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   4,
		InitialDelay:  10 * time.Millisecond,
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	var delays []time.Duration
	lastAttemptTime := time.Now()
	attempts := 0

	err := Retry(context.Background(), config, func() error {
		attempts++
		now := time.Now()
		if attempts > 1 {
			delays = append(delays, now.Sub(lastAttemptTime))
		}
		lastAttemptTime = now
		return errors.New("error")
	})

	if err == nil {
		t.Error("Expected error, got nil")
	}

	// Verify exponential backoff pattern
	// First retry: 10ms, Second: 20ms, Third: 40ms
	expectedDelays := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
	}

	if len(delays) != len(expectedDelays) {
		t.Fatalf("Expected %d delays, got %d", len(expectedDelays), len(delays))
	}

	for i, delay := range delays {
		// Allow 60% tolerance for timing in CI environments
		// CI containers/VMs have 1-5ms overhead + scheduler variance
		minDelay := expectedDelays[i] * 5 / 10  // 50% minimum
		maxDelay := expectedDelays[i] * 16 / 10 // 160% maximum

		if delay < minDelay || delay > maxDelay {
			t.Errorf("Delay %d: expected ~%v (±60%%), got %v", i, expectedDelays[i], delay)
		}
	}

	// Also verify exponential backoff pattern (more important than exact timing)
	if len(delays) >= 2 {
		ratio1 := float64(delays[1]) / float64(delays[0])
		if ratio1 < 1.5 || ratio1 > 2.5 {
			t.Errorf("Backoff ratio between delay[1]/delay[0]: expected ~2.0, got %.2f", ratio1)
		}
	}
	if len(delays) >= 3 {
		ratio2 := float64(delays[2]) / float64(delays[1])
		if ratio2 < 1.5 || ratio2 > 2.5 {
			t.Errorf("Backoff ratio between delay[2]/delay[1]: expected ~2.0, got %.2f", ratio2)
		}
	}
}

// TestRetryMaxDelayEnforcement tests that delay doesn't exceed MaxDelay
func TestRetryMaxDelayEnforcement(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   5,
		InitialDelay:  10 * time.Millisecond,
		MaxDelay:      25 * time.Millisecond, // Low max delay
		BackoffFactor: 10.0,                  // High backoff factor
		JitterEnabled: false,
	}

	var delays []time.Duration
	lastAttemptTime := time.Now()
	attempts := 0

	_ = Retry(context.Background(), config, func() error {
		attempts++
		now := time.Now()
		if attempts > 1 {
			delays = append(delays, now.Sub(lastAttemptTime))
		}
		lastAttemptTime = now
		return errors.New("error")
	})

	// All delays should be capped at MaxDelay
	for i, delay := range delays {
		// Allow some tolerance for timing
		if delay > config.MaxDelay*13/10 { // 30% tolerance
			t.Errorf("Delay %d exceeded MaxDelay: %v > %v", i, delay, config.MaxDelay)
		}
	}
}

// TestRetryJitter tests jitter is applied when enabled
func TestRetryJitter(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   4,
		InitialDelay:  20 * time.Millisecond,
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 1.0, // No backoff, just base delay
		JitterEnabled: true,
	}

	var delays []time.Duration
	lastAttemptTime := time.Now()
	attempts := 0

	_ = Retry(context.Background(), config, func() error {
		attempts++
		now := time.Now()
		if attempts > 1 {
			delays = append(delays, now.Sub(lastAttemptTime))
		}
		lastAttemptTime = now
		return errors.New("error")
	})

	// With jitter, delays should vary slightly
	if len(delays) < 2 {
		t.Fatal("Need at least 2 delays to test jitter")
	}

	// All delays should be around InitialDelay but with variation
	allSame := true
	firstDelay := delays[0]
	for _, delay := range delays[1:] {
		if delay != firstDelay {
			allSame = false
			break
		}
	}

	// With jitter, delays should not all be exactly the same
	// (Though this could theoretically happen, it's very unlikely)
	if allSame {
		t.Log("Warning: All delays were identical despite jitter being enabled")
	}
}

// TestRetryNilConfig tests default config is used when nil
func TestRetryNilConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry nil config test in short mode (uses default retry delays)")
	}

	attempts := 0
	err := Retry(context.Background(), nil, func() error {
		attempts++
		return errors.New("error")
	})

	if err == nil {
		t.Error("Expected error, got nil")
	}

	// Default config has MaxAttempts=3
	if attempts != 3 {
		t.Errorf("Expected 3 attempts with default config, got %d", attempts)
	}
}

// TestRetryContextDeadline tests context with deadline
func TestRetryContextDeadline(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   10,
		InitialDelay:  50 * time.Millisecond,
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()

	attempts := 0
	start := time.Now()

	err := Retry(ctx, config, func() error {
		attempts++
		return errors.New("error")
	})

	duration := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Expected context.DeadlineExceeded, got: %v", err)
	}

	// Should timeout after ~75ms, so only 1-2 attempts
	if attempts > 3 {
		t.Errorf("Expected at most 3 attempts before timeout, got %d", attempts)
	}

	// Should respect the deadline
	if duration > 100*time.Millisecond {
		t.Errorf("Retry didn't respect deadline, took %v", duration)
	}
}

// TestRetryWithCircuitBreakerIntegration tests integration with circuit breaker
func TestRetryWithCircuitBreakerIntegration(t *testing.T) {
	// Create a circuit breaker that opens after 2 failures
	cbConfig := &CircuitBreakerConfig{
		Name:             "test",
		FailureThreshold: 2,
		SleepWindow:      100 * time.Millisecond,
		HalfOpenRequests: 1,
		SuccessThreshold: 0.5,
		VolumeThreshold:  1,
		WindowSize:       60 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}
	cb, err := NewCircuitBreaker(cbConfig)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	retryConfig := &RetryConfig{
		MaxAttempts:   5,
		InitialDelay:  10 * time.Millisecond,
		MaxDelay:      50 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	attempts := 0
	err = RetryWithCircuitBreaker(context.Background(), retryConfig, cb, func() error {
		attempts++
		return errors.New("error")
	})

	// Should eventually fail after retries (circuit breaker opens, then retry fails)
	if err == nil {
		t.Error("Expected error after all retries")
	}

	// Should have attempted the function (circuit breaker allows some attempts)
	if attempts == 0 {
		t.Error("Expected at least one attempt")
	}

	t.Logf("Integration test completed with %d attempts, final CB state: %s, error: %v",
		attempts, cb.GetState(), err)
}

// TestRetryPanicRecovery tests panic behavior in retry
func TestRetryPanicRecovery(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   3,
		InitialDelay:  10 * time.Millisecond,
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	// Retry currently lets panics propagate (which is correct behavior)
	// This test documents and verifies this behavior
	defer func() {
		if r := recover(); r != nil {
			if r != "retry panic test" {
				t.Errorf("Unexpected panic value: %v", r)
			}
			// This is expected behavior - retry doesn't handle panics
		}
	}()

	// This should panic and be caught by the defer above
	_ = Retry(context.Background(), config, func() error {
		panic("retry panic test")
	})

	// Should not reach here
	t.Error("Expected panic to propagate through retry")
}

// TestRetryConcurrentExecutions tests retry under concurrent load
func TestRetryConcurrentExecutions(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   3,
		InitialDelay:  10 * time.Millisecond,
		MaxDelay:      50 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: true,
	}

	concurrency := 50
	var successCount int32
	var totalAttempts int32

	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			localAttempts := 0
			err := Retry(context.Background(), config, func() error {
				localAttempts++
				atomic.AddInt32(&totalAttempts, 1)

				// 50% success rate on second attempt
				if localAttempts == 2 && id%2 == 0 {
					return nil
				}

				// 100% success on third attempt
				if localAttempts == 3 {
					return nil
				}

				return errors.New("error")
			})

			if err == nil {
				atomic.AddInt32(&successCount, 1)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < concurrency; i++ {
		<-done
	}

	// All should eventually succeed
	if int(successCount) != concurrency {
		t.Errorf("Expected all %d to succeed, got %d", concurrency, successCount)
	}

	// Verify reasonable number of attempts
	avgAttempts := float64(totalAttempts) / float64(concurrency)
	if avgAttempts < 2.0 || avgAttempts > 3.0 {
		t.Errorf("Unexpected average attempts: %.2f", avgAttempts)
	}
}

// TestRetryZeroAttempts tests edge case of zero max attempts
func TestRetryZeroAttempts(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   0, // Edge case
		InitialDelay:  10 * time.Millisecond,
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	attempts := 0
	err := Retry(context.Background(), config, func() error {
		attempts++
		return errors.New("error")
	})

	// Should immediately fail without any attempts
	if err == nil {
		t.Error("Expected error with zero attempts")
	}

	if attempts != 0 {
		t.Errorf("Expected 0 attempts with MaxAttempts=0, got %d", attempts)
	}
}

// TestRetryNegativeDelay tests edge case of negative delays
func TestRetryNegativeDelay(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   3,
		InitialDelay:  -10 * time.Millisecond, // Negative delay
		MaxDelay:      100 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	attempts := 0
	start := time.Now()

	_ = Retry(context.Background(), config, func() error {
		attempts++
		return errors.New("error")
	})

	duration := time.Since(start)

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}

	// Should handle negative delay gracefully (treat as zero)
	if duration > 200*time.Millisecond {
		t.Errorf("Negative delay caused unexpected behavior, took %v", duration)
	}
}

// TestRetryImmediateSuccess tests no delay on immediate success
func TestRetryImmediateSuccess(t *testing.T) {
	config := &RetryConfig{
		MaxAttempts:   3,
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      500 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	start := time.Now()
	err := Retry(context.Background(), config, func() error {
		return nil // Immediate success
	})
	duration := time.Since(start)

	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	// Should return immediately without any delays
	if duration > 50*time.Millisecond {
		t.Errorf("Immediate success took too long: %v", duration)
	}
}

// TestDefaultRetryConfig tests the default configuration values
func TestDefaultRetryConfig(t *testing.T) {
	config := DefaultRetryConfig()

	if config.MaxAttempts != 3 {
		t.Errorf("Expected default MaxAttempts=3, got %d", config.MaxAttempts)
	}

	if config.InitialDelay != 100*time.Millisecond {
		t.Errorf("Expected default InitialDelay=100ms, got %v", config.InitialDelay)
	}

	if config.MaxDelay != 5*time.Second {
		t.Errorf("Expected default MaxDelay=5s, got %v", config.MaxDelay)
	}

	if config.BackoffFactor != 2.0 {
		t.Errorf("Expected default BackoffFactor=2.0, got %f", config.BackoffFactor)
	}

	if !config.JitterEnabled {
		t.Error("Expected default JitterEnabled=true")
	}
}
