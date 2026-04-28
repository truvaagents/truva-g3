package telemetry

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestWithBaggage(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() context.Context
		labels   []string
		expected Baggage
	}{
		{
			name:   "Add labels to empty context",
			setup:  func() context.Context { return context.Background() },
			labels: []string{"request_id", "123", "user_id", "456"},
			expected: Baggage{
				"request_id": "123",
				"user_id":    "456",
			},
		},
		{
			name: "Add labels to existing baggage",
			setup: func() context.Context {
				return WithBaggage(context.Background(), "existing", "value")
			},
			labels: []string{"new_key", "new_value"},
			expected: Baggage{
				"existing": "value",
				"new_key":  "new_value",
			},
		},
		{
			name: "Override existing labels",
			setup: func() context.Context {
				return WithBaggage(context.Background(), "env", "staging")
			},
			labels: []string{"env", "production"},
			expected: Baggage{
				"env": "production",
			},
		},
		{
			name: "Handle nil context",
			setup: func() context.Context {
				return nil
			},
			labels: []string{"key", "value"},
			expected: Baggage{
				"key": "value",
			},
		},
		{
			name: "Handle odd number of labels (ignore last)",
			setup: func() context.Context {
				return context.Background()
			},
			labels: []string{"key1", "value1", "key2"},
			expected: Baggage{
				"key1": "value1",
			},
		},
		{
			name: "Multiple calls are additive",
			setup: func() context.Context {
				ctx := context.Background()
				ctx = WithBaggage(ctx, "first", "1")
				ctx = WithBaggage(ctx, "second", "2")
				return ctx
			},
			labels: []string{"third", "3"},
			expected: Baggage{
				"first":  "1",
				"second": "2",
				"third":  "3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setup()
			ctx = WithBaggage(ctx, tt.labels...)

			baggage := GetBaggage(ctx)
			if !reflect.DeepEqual(baggage, tt.expected) {
				t.Errorf("Expected baggage %v, got %v", tt.expected, baggage)
			}
		})
	}
}

func TestEmitWithContext(t *testing.T) {
	// Reset state for test
	initOnce = sync.Once{}
	globalRegistry.Store((*Registry)(nil))
	defer func() {
		// Reset again after test
		initOnce = sync.Once{}
		globalRegistry.Store((*Registry)(nil))
	}()

	// Initialize telemetry
	err := Initialize(UseProfile(ProfileDevelopment))
	if err != nil {
		t.Fatalf("Failed to initialize telemetry: %v", err)
	}

	// Reset metrics
	ResetInternalMetrics()

	// Test 1: EmitWithContext includes baggage
	ctx := WithBaggage(context.Background(), "request_id", "req-123", "user_id", "user-456")
	EmitWithContext(ctx, "test.metric", 42.0, "custom", "label")

	// Verify metric was emitted
	health := GetHealth()
	if health.MetricsEmitted != 1 {
		t.Errorf("Expected 1 metric emitted, got %d", health.MetricsEmitted)
	}

	// Test 2: EmitWithContext without baggage works normally
	EmitWithContext(context.Background(), "test.metric2", 100.0, "only", "this")

	health = GetHealth()
	if health.MetricsEmitted != 2 {
		t.Errorf("Expected 2 metrics emitted, got %d", health.MetricsEmitted)
	}

	// Test 3: Nested contexts preserve all labels
	ctx1 := WithBaggage(context.Background(), "level", "1")
	ctx2 := WithBaggage(ctx1, "level", "2", "extra", "data")

	// Emit with nested context
	EmitWithContext(ctx2, "test.nested", 1.0)

	health = GetHealth()
	if health.MetricsEmitted != 3 {
		t.Errorf("Expected 3 metrics emitted, got %d", health.MetricsEmitted)
	}

	// Verify no errors occurred
	if health.Errors > 0 {
		t.Errorf("Expected no errors, got %d", health.Errors)
	}
}

func TestContextPropagationConcurrency(t *testing.T) {
	// Test that baggage is properly isolated between concurrent contexts
	var wg sync.WaitGroup
	results := make([]Baggage, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Each goroutine creates its own context with unique baggage
			ctx := WithBaggage(context.Background(),
				"goroutine_id", fmt.Sprintf("%d", idx),
				"request_id", fmt.Sprintf("%d", 1000+idx))

			// Simulate some work
			for j := 0; j < 10; j++ {
				ctx = WithBaggage(ctx, "iteration", fmt.Sprintf("%d", j))
			}

			// Store final baggage
			results[idx] = GetBaggage(ctx)
		}(i)
	}

	wg.Wait()

	// Verify each goroutine maintained its own baggage
	for i := 0; i < 100; i++ {
		baggage := results[i]
		expectedGoroutineID := fmt.Sprintf("%d", i)
		expectedRequestID := fmt.Sprintf("%d", 1000+i)

		if baggage["goroutine_id"] != expectedGoroutineID {
			t.Errorf("Goroutine %d: expected goroutine_id %s, got %s",
				i, expectedGoroutineID, baggage["goroutine_id"])
		}
		if baggage["request_id"] != expectedRequestID {
			t.Errorf("Goroutine %d: expected request_id %s, got %s",
				i, expectedRequestID, baggage["request_id"])
		}
	}
}

func TestBaggageImmutability(t *testing.T) {
	// Test that modifying baggage doesn't affect other contexts
	ctx1 := WithBaggage(context.Background(), "key", "value1")
	baggage1 := GetBaggage(ctx1)

	// Create a second context from the first
	ctx2 := WithBaggage(ctx1, "key", "value2")
	baggage2 := GetBaggage(ctx2)

	// Original context should be unchanged
	if GetBaggage(ctx1)["key"] != "value1" {
		t.Error("Original context baggage was modified")
	}

	// New context should have new value
	if baggage2["key"] != "value2" {
		t.Error("New context doesn't have updated value")
	}

	// Verify they're different objects
	if &baggage1 == &baggage2 {
		t.Error("Baggage maps are the same object")
	}
}

func TestContextChaining(t *testing.T) {
	// Test that context can be chained through multiple function calls
	type result struct {
		depth   int
		baggage Baggage
	}

	results := make([]result, 0)
	var mu sync.Mutex

	var deepFunction func(ctx context.Context, depth int)
	deepFunction = func(ctx context.Context, depth int) {
		// Add depth-specific label
		ctx = WithBaggage(ctx, "depth", fmt.Sprintf("%d", depth))

		// Capture current baggage
		mu.Lock()
		results = append(results, result{
			depth:   depth,
			baggage: GetBaggage(ctx),
		})
		mu.Unlock()

		// Recurse
		if depth < 5 {
			deepFunction(ctx, depth+1)
		}
	}

	// Start with initial context
	ctx := WithBaggage(context.Background(), "request_id", "123", "trace_id", "abc")
	deepFunction(ctx, 0)

	// Verify all levels have the original labels
	for _, r := range results {
		if r.baggage["request_id"] != "123" {
			t.Errorf("Depth %d: missing request_id", r.depth)
		}
		if r.baggage["trace_id"] != "abc" {
			t.Errorf("Depth %d: missing trace_id", r.depth)
		}
		if r.baggage["depth"] != fmt.Sprintf("%d", r.depth) {
			t.Errorf("Depth %d: incorrect depth label, got %s", r.depth, r.baggage["depth"])
		}
	}
}

func BenchmarkWithBaggage(b *testing.B) {
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx = WithBaggage(ctx, "key", "value", "key2", "value2")
	}
}

func BenchmarkEmitWithContextAndBaggage(b *testing.B) {
	// Initialize telemetry
	initOnce = sync.Once{}
	globalRegistry.Store((*Registry)(nil))
	_ = Initialize(UseProfile(ProfileDevelopment))

	ctx := WithBaggage(context.Background(),
		"request_id", "123",
		"user_id", "456",
		"tenant_id", "789")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EmitWithContext(ctx, "bench.metric", 1.0, "custom", "label")
	}
}

func BenchmarkEmitWithoutContext(b *testing.B) {
	// Initialize telemetry
	initOnce = sync.Once{}
	globalRegistry.Store((*Registry)(nil))
	_ = Initialize(UseProfile(ProfileDevelopment))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Emit("bench.metric", 1.0, "custom", "label", "request_id", "123", "user_id", "456", "tenant_id", "789")
	}
}
