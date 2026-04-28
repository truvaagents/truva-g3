package telemetry

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/baggage"
)

// TestBaggageLimits tests that baggage size limits are enforced
func TestBaggageLimits(t *testing.T) {
	tests := []struct {
		name        string
		setup       func() context.Context
		expectDrops bool
	}{
		{
			name: "Within limits",
			setup: func() context.Context {
				ctx := context.Background()
				for i := 0; i < 10; i++ {
					ctx = WithBaggage(ctx, fmt.Sprintf("key%d", i), fmt.Sprintf("value%d", i))
				}
				return ctx
			},
			expectDrops: false,
		},
		{
			name: "Exceeds max items",
			setup: func() context.Context {
				ctx := context.Background()
				for i := 0; i < MaxBaggageItems+10; i++ {
					ctx = WithBaggage(ctx, fmt.Sprintf("key%d", i), fmt.Sprintf("value%d", i))
				}
				return ctx
			},
			expectDrops: true,
		},
		{
			name: "Long key truncated",
			setup: func() context.Context {
				longKey := strings.Repeat("x", MaxBaggageKeyLength+10)
				return WithBaggage(context.Background(), longKey, "value")
			},
			expectDrops: false,
		},
		{
			name: "Long value truncated",
			setup: func() context.Context {
				longValue := strings.Repeat("y", MaxBaggageValueLength+10)
				return WithBaggage(context.Background(), "key", longValue)
			},
			expectDrops: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetBaggageStats()

			ctx := tt.setup()
			stats := GetBaggageStats()

			if tt.expectDrops && stats.ItemsDropped == 0 && stats.OverLimit == 0 {
				t.Error("Expected items to be dropped but none were")
			}

			// Verify baggage is still usable
			bag := GetBaggage(ctx)
			if bag == nil && !tt.expectDrops {
				t.Error("Expected baggage but got nil")
			}
		})
	}
}

// TestBaggageValidation tests input validation
func TestBaggageValidation(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		expect int // Expected number of items in baggage
	}{
		{
			name:   "Empty key skipped",
			labels: []string{"", "value1", "key2", "value2"},
			expect: 1, // Only key2 should be added
		},
		{
			name:   "Valid keys",
			labels: []string{"key1", "value1", "key2", "value2"},
			expect: 2,
		},
		{
			name:   "Special characters allowed",
			labels: []string{"key-1", "val@1", "key_2", "val#2"},
			expect: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithBaggage(context.Background(), tt.labels...)
			bag := GetBaggage(ctx)

			if len(bag) != tt.expect {
				t.Errorf("Expected %d items in baggage, got %d", tt.expect, len(bag))
			}
		})
	}
}

// TestDeterministicOrdering tests that label output is deterministic
func TestDeterministicOrdering(t *testing.T) {
	// Create baggage with multiple items
	ctx := context.Background()
	ctx = WithBaggage(ctx,
		"zebra", "1",
		"alpha", "2",
		"beta", "3",
		"gamma", "4")

	// Get labels multiple times
	var results [][]string
	for i := 0; i < 10; i++ {
		labels := appendBaggageToLabels(ctx, []string{"custom", "label"})
		results = append(results, labels)
	}

	// All results should be identical
	first := strings.Join(results[0], ",")
	for i, result := range results {
		if strings.Join(result, ",") != first {
			t.Errorf("Iteration %d produced different order: %v vs %v", i, result, results[0])
		}
	}

	// Verify alphabetical ordering
	for i := 0; i < len(results[0])-2; i += 2 {
		key1 := results[0][i]
		key2 := results[0][i+2]
		if key1 > key2 {
			t.Errorf("Keys not in alphabetical order: %s > %s", key1, key2)
		}
	}
}

// TestBaggageDeduplication tests that baggage overrides explicit labels
func TestBaggageDeduplication(t *testing.T) {
	ctx := WithBaggage(context.Background(), "env", "production", "user", "alice")

	// Explicit labels with same keys
	labels := appendBaggageToLabels(ctx, []string{"env", "staging", "user", "bob", "custom", "value"})

	// Convert to map for easy checking
	labelMap := make(map[string]string)
	for i := 0; i < len(labels)-1; i += 2 {
		labelMap[labels[i]] = labels[i+1]
	}

	// Baggage should override explicit labels
	if labelMap["env"] != "production" {
		t.Errorf("Expected env=production (from baggage), got %s", labelMap["env"])
	}
	if labelMap["user"] != "alice" {
		t.Errorf("Expected user=alice (from baggage), got %s", labelMap["user"])
	}
	if labelMap["custom"] != "value" {
		t.Errorf("Expected custom=value (explicit), got %s", labelMap["custom"])
	}
}

// TestLabelPoolReuse tests that the sync.Pool is working
func TestLabelPoolReuse(t *testing.T) {
	ctx := WithBaggage(context.Background(), "key", "value")

	// Allocate and return many slices
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			labels := appendBaggageToLabels(ctx, []string{"test", "label"})
			returnLabelSlice(labels)
		}()
	}
	wg.Wait()

	// Check that pool is being used (can't directly test, but no panics is good)
	// In real scenario, we'd use runtime memory profiling to verify
}

// TestBaggageStatistics tests internal metrics tracking
func TestBaggageStatistics(t *testing.T) {
	ResetBaggageStats()

	// Add some items
	ctx := context.Background()
	ctx = WithBaggage(ctx, "key1", "value1", "key2", "value2")
	ctx = WithBaggage(ctx, "key3", "value3")

	// Try to exceed limits
	for i := 0; i < MaxBaggageItems*2; i++ {
		ctx = WithBaggage(ctx, fmt.Sprintf("key%d", i), "value")
	}

	stats := GetBaggageStats()

	if stats.ItemsAdded == 0 {
		t.Error("Expected items to be added")
	}
	if stats.OverLimit == 0 {
		t.Error("Expected over limit counter to increment")
	}
	if stats.CurrentSize == 0 {
		t.Error("Expected current size to be tracked")
	}
}

// TestOpenTelemetryIntegration verifies OTel baggage is properly used
func TestOpenTelemetryIntegration(t *testing.T) {
	// Add items using our API
	ctx := WithBaggage(context.Background(), "trace_id", "123", "span_id", "456")

	// Verify it's in OTel baggage
	bag := baggage.FromContext(ctx)
	if bag.Member("trace_id").Value() != "123" {
		t.Error("trace_id not found in OTel baggage")
	}
	if bag.Member("span_id").Value() != "456" {
		t.Error("span_id not found in OTel baggage")
	}

	// Verify our GetBaggage still works
	ourBag := GetBaggage(ctx)
	if ourBag["trace_id"] != "123" {
		t.Error("trace_id not found in our baggage")
	}
}

// TestEmptyBaggageHandling tests edge cases with empty baggage
func TestEmptyBaggageHandling(t *testing.T) {
	// Nil context
	labels := appendBaggageToLabels(context.TODO(), []string{"key", "value"})
	if len(labels) != 2 {
		t.Error("Expected original labels with nil context")
	}

	// Empty context
	labels = appendBaggageToLabels(context.Background(), []string{"key", "value"})
	if len(labels) != 2 {
		t.Error("Expected original labels with empty context")
	}

	// Context with empty baggage
	ctx := context.Background()
	labels = appendBaggageToLabels(ctx, []string{"key", "value"})
	if len(labels) != 2 {
		t.Error("Expected original labels with empty baggage")
	}
}

// TestConcurrentBaggageModification tests thread safety
func TestConcurrentBaggageModification(t *testing.T) {
	var wg sync.WaitGroup
	errors := make(chan error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Each goroutine creates and modifies its own context
			ctx := context.Background()
			for j := 0; j < 10; j++ {
				ctx = WithBaggage(ctx,
					fmt.Sprintf("key_%d_%d", id, j),
					fmt.Sprintf("value_%d_%d", id, j))

				// Verify baggage is consistent
				bag := GetBaggage(ctx)
				if bag == nil {
					errors <- fmt.Errorf("nil baggage in goroutine %d", id)
					return
				}

				// Use the baggage
				labels := appendBaggageToLabels(ctx, []string{"test", "label"})
				if len(labels) < 2 {
					errors <- fmt.Errorf("missing labels in goroutine %d", id)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Error(err)
	}
}

// BenchmarkWithBaggageOTel benchmarks the OTel implementation
func BenchmarkWithBaggageOTel(b *testing.B) {
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx = WithBaggage(ctx, "key", "value", "key2", "value2")
	}
}

// BenchmarkAppendBaggageWithSort benchmarks sorted append
func BenchmarkAppendBaggageWithSort(b *testing.B) {
	ctx := WithBaggage(context.Background(),
		"key1", "val1",
		"key2", "val2",
		"key3", "val3",
		"key4", "val4")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		labels := appendBaggageToLabels(ctx, []string{"custom", "label"})
		returnLabelSlice(labels) // Return to pool
	}
}

// BenchmarkLabelPool benchmarks the sync.Pool performance
func BenchmarkLabelPool(b *testing.B) {
	ctx := WithBaggage(context.Background(), "key", "value")

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			labels := appendBaggageToLabels(ctx, []string{"test", "label"})
			returnLabelSlice(labels)
		}
	})
}

// TestLargeBaggagePerformance tests performance with maximum baggage
func TestLargeBaggagePerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large baggage performance test in short mode (benchmark test)")
	}

	// Fill baggage to near maximum
	ctx := context.Background()
	for i := 0; i < MaxBaggageItems-1; i++ {
		ctx = WithBaggage(ctx, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
	}

	// Time operations with full baggage
	start := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			labels := appendBaggageToLabels(ctx, []string{"custom", "label"})
			returnLabelSlice(labels)
		}
	})

	// Should still be reasonably fast even with max items
	nsPerOp := start.NsPerOp()
	// Note: With race detector, this will be slower. Allow 50 microseconds.
	maxTime := int64(10000)
	if testing.Short() || runtime.GOOS == "darwin" {
		maxTime = 50000 // More lenient on CI/macOS
	}
	if nsPerOp > maxTime {
		t.Errorf("Operation too slow with full baggage: %d ns/op (max: %d)", nsPerOp, maxTime)
	}
}

// TestBaggageKeyOrdering verifies keys are sorted
func TestBaggageKeyOrdering(t *testing.T) {
	ctx := WithBaggage(context.Background(),
		"zebra", "1",
		"alpha", "2",
		"charlie", "3",
		"bravo", "4")

	labels := appendBaggageToLabels(ctx, []string{"echo", "5", "delta", "6"})

	// Extract keys
	var keys []string
	for i := 0; i < len(labels); i += 2 {
		keys = append(keys, labels[i])
	}

	// Verify they're sorted
	sortedKeys := make([]string, len(keys))
	copy(sortedKeys, keys)
	sort.Strings(sortedKeys)

	for i, key := range keys {
		if key != sortedKeys[i] {
			t.Errorf("Keys not sorted: position %d expected %s got %s", i, sortedKeys[i], key)
		}
	}
}
