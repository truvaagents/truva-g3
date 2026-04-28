package core

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestNewMemoryStore tests the advanced MemoryStore constructor
func TestNewMemoryStore(t *testing.T) {
	store := NewMemoryStore()

	if store == nil {
		t.Fatal("NewMemoryStore() returned nil")
	}

	// Verify internal store is initialized (we can't access it directly, but can test behavior)
	ctx := context.Background()

	// Should be able to set and get immediately
	err := store.Set(ctx, "test", "value", 0)
	if err != nil {
		t.Errorf("Set() on new store failed: %v", err)
	}

	value, err := store.Get(ctx, "test")
	if err != nil {
		t.Errorf("Get() on new store failed: %v", err)
	}
	if value != "value" {
		t.Errorf("Get() = %q, want %q", value, "value")
	}
}

// TestMemoryStore_Get tests the Get method
func TestMemoryStore_Get(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Test getting non-existent key
	value, err := store.Get(ctx, "non-existent")
	if err != nil {
		t.Errorf("Get() returned unexpected error: %v", err)
	}
	if value != "" {
		t.Errorf("Get() for non-existent key = %q, want empty string", value)
	}

	// Set a value and get it
	err = store.Set(ctx, "key1", "value1", 0)
	if err != nil {
		t.Fatalf("Set() failed: %v", err)
	}

	value, err = store.Get(ctx, "key1")
	if err != nil {
		t.Errorf("Get() returned unexpected error: %v", err)
	}
	if value != "value1" {
		t.Errorf("Get() = %q, want %q", value, "value1")
	}

	// Test context cancellation (should not affect operation)
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	value, err = store.Get(cancelCtx, "key1")
	if err != nil {
		t.Errorf("Get() with cancelled context failed: %v", err)
	}
	if value != "value1" {
		t.Errorf("Get() with cancelled context = %q, want %q", value, "value1")
	}
}

// TestMemoryStore_GetExpiredKey tests TTL expiration on Get
func TestMemoryStore_GetExpiredKey(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Set value with short TTL
	err := store.Set(ctx, "expiring-key", "expiring-value", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Set() with TTL failed: %v", err)
	}

	// Should exist immediately
	value, err := store.Get(ctx, "expiring-key")
	if err != nil {
		t.Errorf("Get() immediately after Set() failed: %v", err)
	}
	if value != "expiring-value" {
		t.Errorf("Get() immediately = %q, want %q", value, "expiring-value")
	}

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should return empty string for expired key
	value, err = store.Get(ctx, "expiring-key")
	if err != nil {
		t.Errorf("Get() after expiration failed: %v", err)
	}
	if value != "" {
		t.Errorf("Get() after expiration = %q, want empty string", value)
	}
}

// TestMemoryStore_Set tests the Set method comprehensively
func TestMemoryStore_Set(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	tests := []struct {
		name  string
		key   string
		value string
		ttl   time.Duration
	}{
		{
			name:  "set simple value",
			key:   "key1",
			value: "value1",
			ttl:   0,
		},
		{
			name:  "set with TTL",
			key:   "key2",
			value: "value2",
			ttl:   time.Hour,
		},
		{
			name:  "overwrite existing",
			key:   "key1",
			value: "new_value",
			ttl:   0,
		},
		{
			name:  "empty key",
			key:   "",
			value: "value",
			ttl:   0,
		},
		{
			name:  "empty value",
			key:   "empty_val",
			value: "",
			ttl:   0,
		},
		{
			name:  "negative TTL treated as no expiration",
			key:   "negative-ttl",
			value: "never-expires",
			ttl:   -time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.Set(ctx, tt.key, tt.value, tt.ttl)
			if err != nil {
				t.Errorf("Set() error = %v", err)
			}

			// Verify value was set
			gotValue, err := store.Get(ctx, tt.key)
			if err != nil {
				t.Errorf("Get() after Set() error = %v", err)
			}
			if gotValue != tt.value {
				t.Errorf("After Set(), Get() = %q, want %q", gotValue, tt.value)
			}
		})
	}
}

// TestMemoryStore_SetWithTTL tests TTL functionality
func TestMemoryStore_SetWithTTL(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Set value with TTL
	err := store.Set(ctx, "ttl-test", "will-expire", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Set() with TTL failed: %v", err)
	}

	// Value should exist immediately
	value, err := store.Get(ctx, "ttl-test")
	if err != nil {
		t.Errorf("Get() immediately after Set() failed: %v", err)
	}
	if value != "will-expire" {
		t.Errorf("Get() immediately = %q, want %q", value, "will-expire")
	}

	// Value should still exist before expiration
	time.Sleep(50 * time.Millisecond)
	value, err = store.Get(ctx, "ttl-test")
	if err != nil {
		t.Errorf("Get() before expiration failed: %v", err)
	}
	if value != "will-expire" {
		t.Errorf("Get() before expiration = %q, want %q", value, "will-expire")
	}

	// Value should expire
	time.Sleep(100 * time.Millisecond)
	value, err = store.Get(ctx, "ttl-test")
	if err != nil {
		t.Errorf("Get() after expiration failed: %v", err)
	}
	if value != "" {
		t.Errorf("Get() after expiration = %q, want empty string", value)
	}
}

// TestMemoryStore_Delete tests the Delete method
func TestMemoryStore_Delete(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Set some values
	_ = store.Set(ctx, "key1", "value1", 0)
	_ = store.Set(ctx, "key2", "value2", 0)

	// Delete existing key
	err := store.Delete(ctx, "key1")
	if err != nil {
		t.Errorf("Delete() error = %v", err)
	}

	// Verify key was deleted
	value, err := store.Get(ctx, "key1")
	if err != nil {
		t.Errorf("Get() after Delete() error = %v", err)
	}
	if value != "" {
		t.Errorf("After Delete(), Get() = %q, want empty string", value)
	}

	// Verify other key still exists
	value, err = store.Get(ctx, "key2")
	if err != nil {
		t.Errorf("Get() error = %v", err)
	}
	if value != "value2" {
		t.Errorf("Get() = %q, want %q", value, "value2")
	}

	// Delete non-existent key (should not error)
	err = store.Delete(ctx, "non-existent")
	if err != nil {
		t.Errorf("Delete() non-existent key error = %v", err)
	}

	// Delete with cancelled context
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	err = store.Delete(cancelCtx, "key2")
	if err != nil {
		t.Errorf("Delete() with cancelled context error = %v", err)
	}
}

// TestMemoryStore_Exists tests the Exists method
func TestMemoryStore_Exists(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Check non-existent key
	exists, err := store.Exists(ctx, "key1")
	if err != nil {
		t.Errorf("Exists() error = %v", err)
	}
	if exists {
		t.Error("Exists() = true for non-existent key, want false")
	}

	// Set a value
	_ = store.Set(ctx, "key1", "value1", 0)

	// Check existing key
	exists, err = store.Exists(ctx, "key1")
	if err != nil {
		t.Errorf("Exists() error = %v", err)
	}
	if !exists {
		t.Error("Exists() = false for existing key, want true")
	}

	// Set empty value
	_ = store.Set(ctx, "empty", "", 0)

	// Check key with empty value
	exists, err = store.Exists(ctx, "empty")
	if err != nil {
		t.Errorf("Exists() error = %v", err)
	}
	if !exists {
		t.Error("Exists() = false for key with empty value, want true")
	}

	// Delete and check
	_ = store.Delete(ctx, "key1")
	exists, err = store.Exists(ctx, "key1")
	if err != nil {
		t.Errorf("Exists() error = %v", err)
	}
	if exists {
		t.Error("Exists() = true for deleted key, want false")
	}
}

// TestMemoryStore_ExistsExpired tests Exists with expired keys
func TestMemoryStore_ExistsExpired(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Set value with short TTL
	err := store.Set(ctx, "expiring", "value", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Set() with TTL failed: %v", err)
	}

	// Should exist immediately
	exists, err := store.Exists(ctx, "expiring")
	if err != nil {
		t.Errorf("Exists() immediately after Set() failed: %v", err)
	}
	if !exists {
		t.Error("Exists() immediately after Set() = false, want true")
	}

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should not exist after expiration
	exists, err = store.Exists(ctx, "expiring")
	if err != nil {
		t.Errorf("Exists() after expiration failed: %v", err)
	}
	if exists {
		t.Error("Exists() after expiration = true, want false")
	}
}

// TestMemoryStore_Store tests the backward compatibility Store method
func TestMemoryStore_Store(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	tests := []struct {
		name     string
		key      string
		value    interface{}
		expected string
	}{
		{
			name:     "string value",
			key:      "string-key",
			value:    "string-value",
			expected: "string-value",
		},
		{
			name:     "empty string",
			key:      "empty-key",
			value:    "",
			expected: "",
		},
		{
			name:     "non-string value (int)",
			key:      "int-key",
			value:    42,
			expected: "", // Non-string values become empty string
		},
		{
			name:     "non-string value (bool)",
			key:      "bool-key",
			value:    true,
			expected: "", // Non-string values become empty string
		},
		{
			name:     "non-string value (slice)",
			key:      "slice-key",
			value:    []string{"a", "b"},
			expected: "", // Non-string values become empty string
		},
		{
			name:     "nil value",
			key:      "nil-key",
			value:    nil,
			expected: "", // Nil becomes empty string
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.Store(ctx, tt.key, tt.value)
			if err != nil {
				t.Errorf("Store() error = %v", err)
			}

			// Verify value was stored correctly
			gotValue, err := store.Get(ctx, tt.key)
			if err != nil {
				t.Errorf("Get() after Store() error = %v", err)
			}
			if gotValue != tt.expected {
				t.Errorf("After Store(), Get() = %q, want %q", gotValue, tt.expected)
			}
		})
	}
}

// TestMemoryStore_Retrieve tests the backward compatibility Retrieve method
func TestMemoryStore_Retrieve(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Set some values using Set
	_ = store.Set(ctx, "key1", "value1", 0)
	_ = store.Set(ctx, "key2", "", 0)

	// Retrieve existing key
	value, err := store.Retrieve(ctx, "key1")
	if err != nil {
		t.Errorf("Retrieve() error = %v", err)
	}
	if strValue, ok := value.(string); !ok || strValue != "value1" {
		t.Errorf("Retrieve() = %v, want %q", value, "value1")
	}

	// Retrieve key with empty value
	value, err = store.Retrieve(ctx, "key2")
	if err != nil {
		t.Errorf("Retrieve() error = %v", err)
	}
	if strValue, ok := value.(string); !ok || strValue != "" {
		t.Errorf("Retrieve() = %v, want empty string", value)
	}

	// Retrieve non-existent key
	value, err = store.Retrieve(ctx, "non-existent")
	if err != nil {
		t.Errorf("Retrieve() for non-existent key error = %v", err)
	}
	if strValue, ok := value.(string); !ok || strValue != "" {
		t.Errorf("Retrieve() for non-existent key = %v, want empty string", value)
	}
}

// TestMemoryStore_ThreadSafety tests concurrent access
func TestMemoryStore_ThreadSafety(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	var wg sync.WaitGroup
	numGoroutines := 100
	numOpsPerGoroutine := 100

	// Test concurrent operations
	wg.Add(numGoroutines * 4) // 4 types of operations

	// Concurrent Sets
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				key := fmt.Sprintf("set-%d-%d", id, j)
				value := fmt.Sprintf("value-%d-%d", id, j)
				_ = store.Set(ctx, key, value, 0)
			}
		}(i)
	}

	// Concurrent Gets
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				key := fmt.Sprintf("get-%d-%d", id, j%10) // Read some keys multiple times
				_, _ = store.Get(ctx, key)
			}
		}(i)
	}

	// Concurrent Exists
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				key := fmt.Sprintf("exists-%d-%d", id, j%10)
				_, _ = store.Exists(ctx, key)
			}
		}(i)
	}

	// Concurrent Deletes
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				key := fmt.Sprintf("delete-%d-%d", id, j)
				_ = store.Delete(ctx, key)
			}
		}(i)
	}

	wg.Wait()

	// Verify no panics occurred and basic functionality still works
	err := store.Set(ctx, "final-test", "works", 0)
	if err != nil {
		t.Errorf("Set() after concurrent operations failed: %v", err)
	}

	value, err := store.Get(ctx, "final-test")
	if err != nil {
		t.Errorf("Get() after concurrent operations failed: %v", err)
	}
	if value != "works" {
		t.Errorf("Get() after concurrent operations = %q, want %q", value, "works")
	}
}

// TestMemoryStore_TTLEdgeCases tests edge cases with TTL
func TestMemoryStore_TTLEdgeCases(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Zero TTL means no expiration
	err := store.Set(ctx, "no-ttl", "never-expires", 0)
	if err != nil {
		t.Fatalf("Set() with zero TTL failed: %v", err)
	}

	// Should still exist after a reasonable time
	time.Sleep(50 * time.Millisecond)
	value, err := store.Get(ctx, "no-ttl")
	if err != nil {
		t.Errorf("Get() after time with zero TTL failed: %v", err)
	}
	if value != "never-expires" {
		t.Errorf("Get() with zero TTL = %q, want %q", value, "never-expires")
	}

	// Negative TTL treated as no expiration
	err = store.Set(ctx, "negative-ttl", "also-never-expires", -time.Hour)
	if err != nil {
		t.Fatalf("Set() with negative TTL failed: %v", err)
	}

	value, err = store.Get(ctx, "negative-ttl")
	if err != nil {
		t.Errorf("Get() with negative TTL failed: %v", err)
	}
	if value != "also-never-expires" {
		t.Errorf("Get() with negative TTL = %q, want %q", value, "also-never-expires")
	}

	// Very short TTL
	err = store.Set(ctx, "micro-ttl", "very-short", 1*time.Nanosecond)
	if err != nil {
		t.Fatalf("Set() with nanosecond TTL failed: %v", err)
	}

	// Should likely be expired immediately (but may pass due to timing)
	time.Sleep(1 * time.Millisecond)
	value, err = store.Get(ctx, "micro-ttl")
	if err != nil {
		t.Errorf("Get() after micro TTL failed: %v", err)
	}
	// Don't assert specific value as timing is unpredictable
	t.Logf("Value after nanosecond TTL: %q", value)
}

// TestMemoryStore_StoreRetrieveIntegration tests Store/Retrieve together
func TestMemoryStore_StoreRetrieveIntegration(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Test Store then Retrieve
	err := store.Store(ctx, "integration-test", "test-value")
	if err != nil {
		t.Fatalf("Store() failed: %v", err)
	}

	value, err := store.Retrieve(ctx, "integration-test")
	if err != nil {
		t.Errorf("Retrieve() failed: %v", err)
	}
	if strValue, ok := value.(string); !ok || strValue != "test-value" {
		t.Errorf("Retrieve() = %v, want %q", value, "test-value")
	}

	// Test Set then Retrieve
	err = store.Set(ctx, "mixed-test", "set-value", 0)
	if err != nil {
		t.Fatalf("Set() failed: %v", err)
	}

	value, err = store.Retrieve(ctx, "mixed-test")
	if err != nil {
		t.Errorf("Retrieve() after Set() failed: %v", err)
	}
	if strValue, ok := value.(string); !ok || strValue != "set-value" {
		t.Errorf("Retrieve() after Set() = %v, want %q", value, "set-value")
	}

	// Test Store then Get
	err = store.Store(ctx, "reverse-test", "store-value")
	if err != nil {
		t.Fatalf("Store() failed: %v", err)
	}

	getValue, err := store.Get(ctx, "reverse-test")
	if err != nil {
		t.Errorf("Get() after Store() failed: %v", err)
	}
	if getValue != "store-value" {
		t.Errorf("Get() after Store() = %q, want %q", getValue, "store-value")
	}
}
