package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
)

// setupTestRedis creates a miniredis instance for testing
func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	return mr, client
}

func TestNewSchemaCache(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	if cache == nil {
		t.Fatal("NewSchemaCache returned nil")
	}

	// Verify it's the correct type
	_, ok := cache.(*RedisSchemaCache)
	if !ok {
		t.Fatal("NewSchemaCache did not return *RedisSchemaCache")
	}
}

func TestSchemaCache_GetSet_Basic(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	toolName := "weather-service"
	capabilityName := "current_weather"
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"location": map[string]interface{}{
				"type": "string",
			},
		},
	}

	// Test cache miss
	_, found := cache.Get(ctx, toolName, capabilityName)
	if found {
		t.Error("Expected cache miss, got hit")
	}

	// Test set
	err := cache.Set(ctx, toolName, capabilityName, schema)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Test cache hit
	retrieved, found := cache.Get(ctx, toolName, capabilityName)
	if !found {
		t.Error("Expected cache hit, got miss")
	}

	// Verify data integrity
	if retrieved["type"] != schema["type"] {
		t.Errorf("Retrieved schema type mismatch: got %v, want %v", retrieved["type"], schema["type"])
	}
}

func TestSchemaCache_MultipleSchemas(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	// Store multiple schemas
	schemas := []struct {
		tool       string
		capability string
		schema     map[string]interface{}
	}{
		{"weather-service", "current_weather", map[string]interface{}{"type": "weather"}},
		{"weather-service", "forecast", map[string]interface{}{"type": "forecast"}},
		{"stock-service", "get_price", map[string]interface{}{"type": "stock"}},
	}

	for _, s := range schemas {
		err := cache.Set(ctx, s.tool, s.capability, s.schema)
		if err != nil {
			t.Fatalf("Set failed for %s/%s: %v", s.tool, s.capability, err)
		}
	}

	// Retrieve and verify each schema
	for _, s := range schemas {
		retrieved, found := cache.Get(ctx, s.tool, s.capability)
		if !found {
			t.Errorf("Schema not found for %s/%s", s.tool, s.capability)
			continue
		}

		if retrieved["type"] != s.schema["type"] {
			t.Errorf("Schema mismatch for %s/%s: got %v, want %v",
				s.tool, s.capability, retrieved["type"], s.schema["type"])
		}
	}
}

func TestSchemaCache_Stats(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	schema := map[string]interface{}{"type": "test"}

	// Initial stats should be zero
	stats := cache.Stats()
	if stats["hits"].(int64) != 0 {
		t.Errorf("Initial hits should be 0, got %v", stats["hits"])
	}
	if stats["misses"].(int64) != 0 {
		t.Errorf("Initial misses should be 0, got %v", stats["misses"])
	}

	// Cause a miss
	cache.Get(ctx, "tool1", "cap1")
	stats = cache.Stats()
	if stats["misses"].(int64) != 1 {
		t.Errorf("After miss, misses should be 1, got %v", stats["misses"])
	}

	// Store and hit
	cache.Set(ctx, "tool1", "cap1", schema)
	cache.Get(ctx, "tool1", "cap1")
	stats = cache.Stats()
	if stats["hits"].(int64) != 1 {
		t.Errorf("After hit, hits should be 1, got %v", stats["hits"])
	}

	// Check hit rate calculation
	hitRate, ok := stats["hit_rate"].(float64)
	if !ok {
		t.Error("hit_rate should be a float64")
	}
	expectedRate := 1.0 / 2.0 // 1 hit out of 2 total lookups
	if hitRate != expectedRate {
		t.Errorf("Hit rate should be %.2f, got %.2f", expectedRate, hitRate)
	}
}

func TestSchemaCache_WithTTL(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	ttl := 100 * time.Millisecond
	cache := NewSchemaCache(client, WithTTL(ttl))
	ctx := context.Background()

	schema := map[string]interface{}{"type": "test"}

	// Set schema
	err := cache.Set(ctx, "tool1", "cap1", schema)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Should exist immediately
	_, found := cache.Get(ctx, "tool1", "cap1")
	if !found {
		t.Error("Schema should exist immediately after set")
	}

	// Fast-forward time in miniredis
	mr.FastForward(ttl + 10*time.Millisecond)

	// Should be expired
	_, found = cache.Get(ctx, "tool1", "cap1")
	if found {
		t.Error("Schema should be expired after TTL")
	}
}

func TestSchemaCache_WithPrefix(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	prefix := "custom:prefix:"
	cache := NewSchemaCache(client, WithPrefix(prefix))
	ctx := context.Background()

	schema := map[string]interface{}{"type": "test"}

	// Set schema
	err := cache.Set(ctx, "tool1", "cap1", schema)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Verify the key uses the custom prefix in Redis
	expectedKey := prefix + "tool1:cap1"
	exists := mr.Exists(expectedKey)
	if !exists {
		t.Errorf("Expected Redis key %s to exist", expectedKey)
	}

	// Verify retrieval still works
	_, found := cache.Get(ctx, "tool1", "cap1")
	if !found {
		t.Error("Schema should be retrievable with custom prefix")
	}
}

func TestSchemaCache_ConcurrentAccess(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	const numGoroutines = 50
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Run concurrent Set and Get operations
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			toolName := "tool"
			capName := "capability"
			schema := map[string]interface{}{
				"goroutine": id,
				"type":      "test",
			}

			for j := 0; j < numOperations; j++ {
				// Set
				err := cache.Set(ctx, toolName, capName, schema)
				if err != nil {
					t.Errorf("Concurrent Set failed: %v", err)
					return
				}

				// Get
				_, found := cache.Get(ctx, toolName, capName)
				if !found {
					// It's okay if not found due to race conditions,
					// we're mainly testing for panics/data races
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify stats are consistent (no race conditions on atomic counters)
	stats := cache.Stats()
	totalLookups := stats["total_lookups"].(int64)
	hits := stats["hits"].(int64)
	misses := stats["misses"].(int64)

	if hits+misses != totalLookups {
		t.Errorf("Stats inconsistent: hits(%d) + misses(%d) != total(%d)",
			hits, misses, totalLookups)
	}
}

func TestSchemaCache_RedisConnectionError(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	schema := map[string]interface{}{"type": "test"}

	// Set a schema first
	err := cache.Set(ctx, "tool1", "cap1", schema)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Close miniredis to simulate connection failure
	mr.Close()

	// Get should gracefully handle Redis errors (treat as cache miss)
	_, found := cache.Get(ctx, "tool1", "cap1")
	if found {
		t.Error("Expected cache miss after Redis connection failure")
	}

	// Set should return error when Redis is down
	err = cache.Set(ctx, "tool2", "cap2", schema)
	if err == nil {
		t.Error("Expected Set to fail when Redis is down")
	}
}

func TestSchemaCache_CorruptData(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	// Manually insert corrupt data into Redis
	corruptKey := "truvag3:schema:tool1:cap1"
	mr.Set(corruptKey, "not-valid-json")

	// Get should handle corrupt data gracefully (treat as cache miss)
	_, found := cache.Get(ctx, "tool1", "cap1")
	if found {
		t.Error("Expected cache miss for corrupt data")
	}

	// Stats should record this as a miss
	stats := cache.Stats()
	if stats["misses"].(int64) != 1 {
		t.Errorf("Corrupt data should count as miss, got %v misses", stats["misses"])
	}
}

func TestSchemaCache_ComplexSchema(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	// Test with a complex JSON Schema v7 document
	complexSchema := map[string]interface{}{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"type":        "object",
		"title":       "current_weather",
		"description": "Gets current weather conditions",
		"required":    []interface{}{"location"},
		"properties": map[string]interface{}{
			"location": map[string]interface{}{
				"type":        "string",
				"description": "City name",
				"examples":    []interface{}{"London", "New York"},
			},
			"units": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"metric", "imperial"},
			},
		},
		"additionalProperties": false,
	}

	// Store complex schema
	err := cache.Set(ctx, "weather", "current", complexSchema)
	if err != nil {
		t.Fatalf("Failed to set complex schema: %v", err)
	}

	// Retrieve and verify
	retrieved, found := cache.Get(ctx, "weather", "current")
	if !found {
		t.Fatal("Complex schema not found")
	}

	// Verify structure is preserved
	if retrieved["$schema"] != complexSchema["$schema"] {
		t.Error("Schema version not preserved")
	}

	// Verify nested properties
	props, ok := retrieved["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Properties not preserved as map")
	}

	location, ok := props["location"].(map[string]interface{})
	if !ok {
		t.Fatal("Nested location property not preserved")
	}

	if location["type"] != "string" {
		t.Error("Nested property type not preserved")
	}
}

func TestSchemaCache_ContextCancellation(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	schema := map[string]interface{}{"type": "test"}

	// Create a canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Set should respect context cancellation
	err := cache.Set(ctx, "tool1", "cap1", schema)
	if err == nil {
		t.Error("Expected Set to fail with canceled context")
	}

	// Get should respect context cancellation
	_, found := cache.Get(ctx, "tool1", "cap1")
	if found {
		t.Error("Expected Get to fail with canceled context")
	}
}

func TestSchemaCache_EmptySchema(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	// Test with empty schema
	emptySchema := map[string]interface{}{}

	err := cache.Set(ctx, "tool1", "cap1", emptySchema)
	if err != nil {
		t.Fatalf("Failed to set empty schema: %v", err)
	}

	retrieved, found := cache.Get(ctx, "tool1", "cap1")
	if !found {
		t.Fatal("Empty schema not found")
	}

	if len(retrieved) != 0 {
		t.Errorf("Expected empty schema, got %v", retrieved)
	}
}

func TestSchemaCache_KeyCollisionPrevention(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	// Test that keys with similar patterns don't collide
	// e.g., "weather.v2" + "current" vs "weather" + "v2.current"
	schema1 := map[string]interface{}{"version": 1}
	schema2 := map[string]interface{}{"version": 2}

	err := cache.Set(ctx, "weather.v2", "current", schema1)
	if err != nil {
		t.Fatalf("Set 1 failed: %v", err)
	}

	err = cache.Set(ctx, "weather", "v2.current", schema2)
	if err != nil {
		t.Fatalf("Set 2 failed: %v", err)
	}

	// Verify they're stored separately
	retrieved1, found := cache.Get(ctx, "weather.v2", "current")
	if !found || retrieved1["version"].(float64) != 1 {
		t.Error("Schema 1 not properly isolated")
	}

	retrieved2, found := cache.Get(ctx, "weather", "v2.current")
	if !found || retrieved2["version"].(float64) != 2 {
		t.Error("Schema 2 not properly isolated")
	}
}

func TestSchemaCache_LargeSchema(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	// Create a large schema with many properties
	largeSchema := map[string]interface{}{
		"type":       "object",
		"properties": make(map[string]interface{}),
	}

	props := largeSchema["properties"].(map[string]interface{})
	for i := 0; i < 100; i++ {
		props[string(rune('a'+i%26))+string(rune('0'+i/26))] = map[string]interface{}{
			"type":        "string",
			"description": "Field description",
		}
	}

	// Should handle large schemas
	err := cache.Set(ctx, "large-service", "complex-op", largeSchema)
	if err != nil {
		t.Fatalf("Failed to set large schema: %v", err)
	}

	retrieved, found := cache.Get(ctx, "large-service", "complex-op")
	if !found {
		t.Fatal("Large schema not found")
	}

	retrievedProps := retrieved["properties"].(map[string]interface{})
	if len(retrievedProps) != len(props) {
		t.Errorf("Large schema properties count mismatch: got %d, want %d",
			len(retrievedProps), len(props))
	}
}

// Benchmark tests
func BenchmarkSchemaCache_Set(b *testing.B) {
	mr, client := setupTestRedisBench(b)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"field1": map[string]interface{}{"type": "string"},
			"field2": map[string]interface{}{"type": "number"},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.Set(ctx, "tool", "capability", schema)
	}
}

func BenchmarkSchemaCache_Get_Hit(b *testing.B) {
	mr, client := setupTestRedisBench(b)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	schema := map[string]interface{}{"type": "test"}
	_ = cache.Set(ctx, "tool", "capability", schema)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.Get(ctx, "tool", "capability")
	}
}

func BenchmarkSchemaCache_Get_Miss(b *testing.B) {
	mr, client := setupTestRedisBench(b)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.Get(ctx, "nonexistent", "capability")
	}
}

func setupTestRedisBench(b *testing.B) (*miniredis.Miniredis, *redis.Client) {
	b.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		b.Fatalf("Failed to start miniredis: %v", err)
	}

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	return mr, client
}

// Test to verify JSON marshaling/unmarshaling preserves types
func TestSchemaCache_TypePreservation(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cache := NewSchemaCache(client)
	ctx := context.Background()

	// Test various JSON types
	schema := map[string]interface{}{
		"string_field": "test",
		"number_field": 42.5,
		"bool_field":   true,
		"null_field":   nil,
		"array_field":  []interface{}{"a", "b", "c"},
		"object_field": map[string]interface{}{
			"nested": "value",
		},
	}

	err := cache.Set(ctx, "tool1", "cap1", schema)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	retrieved, found := cache.Get(ctx, "tool1", "cap1")
	if !found {
		t.Fatal("Schema not found")
	}

	// Verify types are preserved through JSON round-trip
	if retrieved["string_field"] != "test" {
		t.Error("String type not preserved")
	}
	if retrieved["number_field"] != 42.5 {
		t.Error("Number type not preserved")
	}
	if retrieved["bool_field"] != true {
		t.Error("Bool type not preserved")
	}
	if retrieved["null_field"] != nil {
		t.Error("Null type not preserved")
	}

	// Arrays become []interface{} after JSON round-trip
	arr, ok := retrieved["array_field"].([]interface{})
	if !ok || len(arr) != 3 {
		t.Error("Array type not preserved")
	}

	// Nested objects become map[string]interface{}
	obj, ok := retrieved["object_field"].(map[string]interface{})
	if !ok || obj["nested"] != "value" {
		t.Error("Object type not preserved")
	}
}
