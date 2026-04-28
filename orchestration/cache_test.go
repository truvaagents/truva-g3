package orchestration

import (
	"testing"
	"time"
)

func TestSimpleCache(t *testing.T) {
	cache := NewSimpleCacheWithOptions(10, 100*time.Millisecond)
	defer cache.Stop()

	// Test basic set and get
	plan := &RoutingPlan{
		PlanID:          "test-plan-1",
		OriginalRequest: "test request",
		Mode:            ModeAutonomous,
		CreatedAt:       time.Now(),
	}

	cache.Set("test-prompt", plan, 1*time.Second)

	// Should find the plan
	retrieved, found := cache.Get("test-prompt")
	if !found {
		t.Error("Expected to find cached plan")
	}
	if retrieved.PlanID != plan.PlanID {
		t.Errorf("Expected plan ID %s, got %s", plan.PlanID, retrieved.PlanID)
	}

	// Test cache miss
	_, found = cache.Get("non-existent")
	if found {
		t.Error("Expected cache miss for non-existent key")
	}

	// Test expiration
	cache.Set("expiring", plan, 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	_, found = cache.Get("expiring")
	if found {
		t.Error("Expected cached item to expire")
	}

	// Test clear
	cache.Set("to-clear", plan, 1*time.Second)
	cache.Clear()
	_, found = cache.Get("to-clear")
	if found {
		t.Error("Expected cache to be cleared")
	}

	// Test stats
	stats := cache.Stats()
	if stats.Size != 0 {
		t.Errorf("Expected cache size 0 after clear, got %d", stats.Size)
	}
}

func TestSimpleCache_MaxSize(t *testing.T) {
	cache := NewSimpleCacheWithOptions(2, 1*time.Minute)
	defer cache.Stop()

	plan1 := &RoutingPlan{PlanID: "plan-1"}
	plan2 := &RoutingPlan{PlanID: "plan-2"}
	plan3 := &RoutingPlan{PlanID: "plan-3"}

	cache.Set("prompt-1", plan1, 1*time.Second)
	cache.Set("prompt-2", plan2, 1*time.Second)

	// Adding third item should trigger eviction
	cache.Set("prompt-3", plan3, 1*time.Second)

	stats := cache.Stats()
	if stats.Size > 2 {
		t.Errorf("Expected cache size <= 2, got %d", stats.Size)
	}

	// Newest item should be present
	if _, found := cache.Get("prompt-3"); !found {
		t.Error("Expected newest item to be in cache")
	}
}

func TestLRUCache(t *testing.T) {
	cache := NewLRUCache(3)

	plan1 := &RoutingPlan{PlanID: "plan-1"}
	plan2 := &RoutingPlan{PlanID: "plan-2"}
	plan3 := &RoutingPlan{PlanID: "plan-3"}
	plan4 := &RoutingPlan{PlanID: "plan-4"}

	// Fill cache
	cache.Set("prompt-1", plan1, 1*time.Hour)
	cache.Set("prompt-2", plan2, 1*time.Hour)
	cache.Set("prompt-3", plan3, 1*time.Hour)

	// Access prompt-1 to make it most recently used
	_, found := cache.Get("prompt-1")
	if !found {
		t.Error("Expected to find prompt-1")
	}

	// Add new item, should evict prompt-2 (least recently used)
	cache.Set("prompt-4", plan4, 1*time.Hour)

	// Check that prompt-2 was evicted
	_, found = cache.Get("prompt-2")
	if found {
		t.Error("Expected prompt-2 to be evicted")
	}

	// Check that prompt-1 is still there (was accessed recently)
	retrieved, found := cache.Get("prompt-1")
	if !found {
		t.Error("Expected prompt-1 to still be in cache")
	}
	if retrieved.PlanID != "plan-1" {
		t.Errorf("Expected plan-1, got %s", retrieved.PlanID)
	}

	// Test clear
	cache.Clear()
	stats := cache.Stats()
	if stats.Size != 0 {
		t.Errorf("Expected cache size 0 after clear, got %d", stats.Size)
	}
}

func TestLRUCache_UpdateExisting(t *testing.T) {
	cache := NewLRUCache(2)

	plan1 := &RoutingPlan{PlanID: "plan-1"}
	plan2 := &RoutingPlan{PlanID: "plan-2"}
	updatedPlan1 := &RoutingPlan{PlanID: "plan-1-updated"}

	cache.Set("prompt-1", plan1, 1*time.Hour)
	cache.Set("prompt-2", plan2, 1*time.Hour)

	// Update existing entry
	cache.Set("prompt-1", updatedPlan1, 1*time.Hour)

	// Should not increase size
	stats := cache.Stats()
	if stats.Size != 2 {
		t.Errorf("Expected cache size 2, got %d", stats.Size)
	}

	// Should have updated value
	retrieved, found := cache.Get("prompt-1")
	if !found {
		t.Error("Expected to find prompt-1")
	}
	if retrieved.PlanID != "plan-1-updated" {
		t.Errorf("Expected updated plan, got %s", retrieved.PlanID)
	}
}

func TestLRUCache_Expiration(t *testing.T) {
	cache := NewLRUCache(5)

	plan := &RoutingPlan{PlanID: "expiring-plan"}

	// Set with short TTL
	cache.Set("expiring", plan, 50*time.Millisecond)

	// Should be found immediately
	_, found := cache.Get("expiring")
	if !found {
		t.Error("Expected to find plan before expiration")
	}

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should not be found after expiration
	_, found = cache.Get("expiring")
	if found {
		t.Error("Expected plan to expire")
	}

	// Stats should reflect the eviction
	stats := cache.Stats()
	if stats.Evictions != 1 {
		t.Errorf("Expected 1 eviction, got %d", stats.Evictions)
	}
}

func TestCacheStats_HitRate(t *testing.T) {
	cache := NewSimpleCache()
	defer cache.Stop()

	plan := &RoutingPlan{PlanID: "test-plan"}
	cache.Set("prompt", plan, 1*time.Hour)

	// Generate some hits and misses
	cache.Get("prompt")       // hit
	cache.Get("prompt")       // hit
	cache.Get("non-existent") // miss
	cache.Get("prompt")       // hit

	stats := cache.Stats()
	expectedHitRate := 3.0 / 4.0 // 3 hits, 1 miss
	if stats.HitRate != expectedHitRate {
		t.Errorf("Expected hit rate %f, got %f", expectedHitRate, stats.HitRate)
	}
	if stats.Hits != 3 {
		t.Errorf("Expected 3 hits, got %d", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("Expected 1 miss, got %d", stats.Misses)
	}
}

func BenchmarkSimpleCache_Get(b *testing.B) {
	cache := NewSimpleCache()
	defer cache.Stop()

	plan := &RoutingPlan{PlanID: "bench-plan"}
	cache.Set("test-prompt", plan, 1*time.Hour)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Get("test-prompt")
		}
	})
}

func BenchmarkLRUCache_Get(b *testing.B) {
	cache := NewLRUCache(1000)

	plan := &RoutingPlan{PlanID: "bench-plan"}
	cache.Set("test-prompt", plan, 1*time.Hour)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Get("test-prompt")
		}
	})
}

func BenchmarkSimpleCache_Set(b *testing.B) {
	cache := NewSimpleCacheWithOptions(10000, 1*time.Hour)
	defer cache.Stop()

	plan := &RoutingPlan{PlanID: "bench-plan"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Set("prompt-"+string(rune(i%1000)), plan, 1*time.Hour)
	}
}

func BenchmarkLRUCache_Set(b *testing.B) {
	cache := NewLRUCache(10000)

	plan := &RoutingPlan{PlanID: "bench-plan"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Set("prompt-"+string(rune(i%1000)), plan, 1*time.Hour)
	}
}
