package resilience

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// BenchmarkCircuitBreakerExecute measures overhead of circuit breaker execution
func BenchmarkCircuitBreakerExecute(b *testing.B) {
	config := &CircuitBreakerConfig{
		Name:             "bench",
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
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = cb.Execute(ctx, func() error {
			return nil // Always succeed
		})
	}
}

// BenchmarkCircuitBreakerExecuteWithErrors measures overhead with mixed results
func BenchmarkCircuitBreakerExecuteWithErrors(b *testing.B) {
	config := &CircuitBreakerConfig{
		Name:             "bench",
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
	ctx := context.Background()
	testErr := errors.New("test error")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = cb.Execute(ctx, func() error {
			// 30% error rate
			if i%10 < 3 {
				return testErr
			}
			return nil
		})
	}
}

// BenchmarkCircuitBreakerConcurrentExecute measures concurrent performance
func BenchmarkCircuitBreakerConcurrentExecute(b *testing.B) {
	config := &CircuitBreakerConfig{
		Name:             "bench",
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
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = cb.Execute(ctx, func() error {
				return nil
			})
		}
	})
}

// BenchmarkSlidingWindowRecord measures sliding window performance
func BenchmarkSlidingWindowRecord(b *testing.B) {
	window := NewSlidingWindow(60*time.Second, 10, true)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			window.RecordSuccess()
		} else {
			window.RecordFailure()
		}
	}
}

// BenchmarkSlidingWindowGetMetrics measures metrics calculation performance
func BenchmarkSlidingWindowGetMetrics(b *testing.B) {
	window := NewSlidingWindow(60*time.Second, 10, true)

	// Populate with some data
	for i := 0; i < 1000; i++ {
		if i%2 == 0 {
			window.RecordSuccess()
		} else {
			window.RecordFailure()
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = window.GetErrorRate()
		_ = window.GetTotal()
	}
}

// BenchmarkCircuitBreakerCanExecute measures decision making performance
func BenchmarkCircuitBreakerCanExecute(b *testing.B) {
	config := &CircuitBreakerConfig{
		Name:             "benchmark",
		FailureThreshold: 5,
		RecoveryTimeout:  100 * time.Millisecond,
		SleepWindow:      100 * time.Millisecond,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}
	cb := NewCircuitBreakerWithConfig(config)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = cb.CanExecute()
	}
}

// BenchmarkCircuitBreakerStateTransition measures state change overhead
func BenchmarkCircuitBreakerStateTransition(b *testing.B) {
	config := &CircuitBreakerConfig{
		Name:             "bench",
		ErrorThreshold:   0.5,
		VolumeThreshold:  2,                    // Low threshold for frequent transitions
		SleepWindow:      1 * time.Millisecond, // Short window
		HalfOpenRequests: 1,
		SuccessThreshold: 0.5,
		WindowSize:       1 * time.Second,
		BucketCount:      10,
		ErrorClassifier:  DefaultErrorClassifier,
		Logger:           &core.NoOpLogger{},
		Metrics:          &noopMetrics{},
	}

	cb := NewCircuitBreakerWithConfig(config)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Force state transitions
		cb.RecordFailure()
		cb.RecordFailure()
		cb.RecordFailure()               // Opens
		time.Sleep(2 * time.Millisecond) // Wait for sleep window
		cb.RecordSuccess()               // Half-open to closed
	}
}

// BenchmarkErrorClassifier measures error classification performance
func BenchmarkErrorClassifier(b *testing.B) {
	classifier := DefaultErrorClassifier
	testErrors := []error{
		errors.New("generic error"),
		context.Canceled,
		nil,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = classifier(testErrors[i%len(testErrors)])
	}
}

// BenchmarkCircuitBreakerGetMetrics measures metrics collection performance
func BenchmarkCircuitBreakerGetMetrics(b *testing.B) {
	cb := NewCircuitBreakerLegacy(5, 100*time.Millisecond)

	// Generate some activity
	for i := 0; i < 100; i++ {
		if i%2 == 0 {
			cb.RecordSuccess()
		} else {
			cb.RecordFailure()
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = cb.GetMetrics()
	}
}

// BenchmarkCircuitBreakerMemoryUsage measures memory allocation patterns
func BenchmarkCircuitBreakerMemoryUsage(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		config := &CircuitBreakerConfig{
			Name:             "bench",
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

		_ = NewCircuitBreakerWithConfig(config)
	}
}

// BenchmarkCircuitBreakerHighContention simulates high contention scenario
func BenchmarkCircuitBreakerHighContention(b *testing.B) {
	config := &CircuitBreakerConfig{
		Name:             "bench",
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
	ctx := context.Background()

	// Create contention with many goroutines
	goroutines := 100

	b.ResetTimer()
	b.ReportAllocs()

	var wg sync.WaitGroup
	for i := 0; i < b.N; i++ {
		wg.Add(goroutines)
		for j := 0; j < goroutines; j++ {
			go func(id int) {
				defer wg.Done()
				_ = cb.Execute(ctx, func() error {
					if id%10 == 0 {
						return errors.New("error")
					}
					return nil
				})
			}(j)
		}
		wg.Wait()
	}
}

// Comparison benchmarks for baseline

// BenchmarkDirectFunctionCall provides a baseline without circuit breaker
func BenchmarkDirectFunctionCall(b *testing.B) {
	fn := func() error {
		return nil
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = fn()
	}
}

// BenchmarkMutexLockUnlock provides a baseline for mutex operations
func BenchmarkMutexLockUnlock(b *testing.B) {
	var mu sync.RWMutex

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		mu.Lock()
		// Empty critical section - measuring pure mutex overhead
		//nolint:staticcheck // Intentionally empty for benchmarking mutex overhead
		mu.Unlock()
	}
}

// BenchmarkTimeNow provides a baseline for time operations
func BenchmarkTimeNow(b *testing.B) {
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = time.Now()
	}
}
