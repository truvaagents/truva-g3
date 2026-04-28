package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestThreadSafeGlobalRegistry(t *testing.T) {
	// Reset for test
	initOnce = sync.Once{}
	globalRegistry.Store((*Registry)(nil))

	// Test concurrent initialization
	var wg sync.WaitGroup
	errors := make([]error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errors[idx] = Initialize(UseProfile(ProfileDevelopment))
		}(i)
	}

	wg.Wait()

	// All initializations should return nil (Initialize is idempotent)
	for i, err := range errors {
		if err != nil {
			t.Errorf("Initialization %d failed: %v", i, err)
		}
	}

	// Registry should be initialized
	if GetRegistry() == nil {
		t.Error("Registry not initialized")
	}
}

func TestConcurrentEmission(t *testing.T) {
	// Reset and initialize
	initOnce = sync.Once{}
	globalRegistry.Store((*Registry)(nil))

	err := Initialize(UseProfile(ProfileDevelopment))
	if err != nil {
		t.Fatalf("Failed to initialize telemetry: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			Emit("test.metric", float64(n), "goroutine", string(rune(n)))
		}(i)
	}
	wg.Wait()

	health := GetHealth()
	if health.Errors > 0 {
		t.Errorf("Expected no errors, got %d", health.Errors)
	}
}

func TestCardinalityLimiter(t *testing.T) {
	limiter := NewCardinalityLimiter(map[string]int{
		"user_id": 3,
	})
	defer limiter.Stop()

	// Test cardinality limiting
	results := []string{
		limiter.CheckAndLimit("test.metric", "user_id", "user1"),
		limiter.CheckAndLimit("test.metric", "user_id", "user2"),
		limiter.CheckAndLimit("test.metric", "user_id", "user3"),
		limiter.CheckAndLimit("test.metric", "user_id", "user4"), // Should be limited
		limiter.CheckAndLimit("test.metric", "user_id", "user1"), // Existing, should pass
	}

	expected := []string{"user1", "user2", "user3", "other", "user1"}
	for i, result := range results {
		if result != expected[i] {
			t.Errorf("Test %d: expected %s, got %s", i, expected[i], result)
		}
	}
}

func TestTelemetryCircuitBreaker(t *testing.T) {
	config := CircuitConfig{
		Enabled:      true,
		MaxFailures:  3,
		RecoveryTime: 100 * time.Millisecond,
	}

	cb := NewTelemetryCircuitBreaker(config)

	// Record failures
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	// Circuit should be open
	if cb.Allow() {
		t.Error("Circuit breaker should be open")
	}

	// Wait for recovery time
	time.Sleep(150 * time.Millisecond)

	// Should transition to half-open
	if !cb.Allow() {
		t.Error("Circuit breaker should allow test request")
	}
}

func TestProgressiveAPI(t *testing.T) {
	// Reset and initialize
	initOnce = sync.Once{}
	globalRegistry.Store((*Registry)(nil))

	err := Initialize(UseProfile(ProfileDevelopment))
	if err != nil {
		t.Fatalf("Failed to initialize telemetry: %v", err)
	}

	// Test Level 1 API
	Counter("test.counter", "label", "value")
	Histogram("test.histogram", 100.5, "label", "value")
	Gauge("test.gauge", 42.0, "label", "value")

	start := time.Now()
	time.Sleep(10 * time.Millisecond)
	Duration("test.duration", start, "label", "value")

	// Test Level 2 API
	RecordError("test.errors", "timeout", "service", "api")
	RecordSuccess("test.success", "service", "api")
	RecordLatency("test.latency", 150.0, "service", "api")
	RecordBytes("test.bytes", 1024, "direction", "inbound")

	// Test Level 3 API
	ctx := context.Background()
	EmitWithOptions(ctx, "test.advanced", 99.0,
		WithLabel("key", "value"),
		WithUnit(UnitMilliseconds),
		WithSampleRate(0.5))

	// Test batch emission
	BatchEmit([]struct {
		Name   string
		Value  float64
		Labels []string
	}{
		{"batch.metric1", 1.0, []string{"test", "batch"}},
		{"batch.metric2", 2.0, []string{"test", "batch"}},
	})

	// Check health
	health := GetHealth()
	if !health.Initialized {
		t.Error("Telemetry not initialized")
	}
}

func TestHealthEndpoint(t *testing.T) {
	// Reset for test
	initOnce = sync.Once{}
	globalRegistry.Store((*Registry)(nil))
	defer func() {
		// Reset again after test
		initOnce = sync.Once{}
		globalRegistry.Store((*Registry)(nil))
	}()

	// Test uninitialized state
	health := GetHealth()
	if health.Initialized {
		t.Error("Should not be initialized")
	}

	// Initialize
	err := Initialize(UseProfile(ProfileDevelopment))
	if err != nil {
		t.Fatalf("Failed to initialize telemetry: %v", err)
	}

	// Emit some metrics
	for i := 0; i < 10; i++ {
		Emit("test.metric", float64(i))
	}

	// Check health
	health = GetHealth()
	if !health.Initialized {
		t.Error("Should be initialized")
	}
	if health.MetricsEmitted != 10 {
		t.Errorf("Expected 10 metrics emitted, got %d", health.MetricsEmitted)
	}
}

func BenchmarkEmit(b *testing.B) {
	// Reset and initialize
	initOnce = sync.Once{}
	globalRegistry.Store((*Registry)(nil))

	_ = Initialize(UseProfile(ProfileDevelopment))

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			Emit("bench.metric", 1.0, "test", "value")
		}
	})
}

func BenchmarkEmitWithCardinality(b *testing.B) {
	// Reset and initialize with cardinality limits
	initOnce = sync.Once{}
	globalRegistry.Store((*Registry)(nil))

	config := UseProfile(ProfileProduction)
	config.CardinalityLimits = map[string]int{
		"user_id": 100,
	}
	_ = Initialize(config)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			Emit("bench.metric", 1.0, "user_id", string(rune(i%200))) // Will trigger cardinality limiting
		}
	})
}
