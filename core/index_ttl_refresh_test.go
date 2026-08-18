//go:build integration
// +build integration

package core

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIndexSetTTLRefresh tests the critical bug fix where healthy services
// become undiscoverable after 60 seconds due to index set expiration.
//
// Problem: Service keys have 30s TTL (refreshed every 15s by heartbeat),
// but index sets have 60s TTL (never refreshed). After 60s, healthy services
// disappear from filtered discovery even though they're alive.
//
// Solution: refreshIndexSetTTLs() called during UpdateHealth refreshes
// all index set TTLs to prevent this issue.
func TestIndexSetTTLRefresh(t *testing.T) {
	requireRedis(t)

	ctx := context.Background()

	// Create Redis client for test operations
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer client.Close()

	// Clean up any existing test data
	pattern := "truvag3-ttl-test:*"
	keys, _ := client.Keys(ctx, pattern).Result()
	if len(keys) > 0 {
		client.Del(ctx, keys...)
	}

	// Create registry with realistic TTLs for testing
	registry, err := NewRedisRegistryWithNamespace("redis://localhost:6379", "truvag3-ttl-test")
	require.NoError(t, err, "Failed to create test registry")

	// Override TTL for faster testing (normally 30s, use 10s for test)
	// Service TTL = 10s, Index TTL = 20s (2x service TTL)
	registry.ttl = 10 * time.Second

	// Create discovery client
	discovery, err := NewRedisDiscoveryWithNamespace("redis://localhost:6379", "truvag3-ttl-test")
	require.NoError(t, err, "Failed to create discovery client")

	// Test service info
	testService := &ServiceInfo{
		ID:      "test-service-ttl-refresh",
		Name:    "ttl-test-service",
		Type:    "tool",
		Address: "localhost",
		Port:    9999,
		Capabilities: []Capability{
			{Name: "test-capability", Description: "Test capability for TTL testing"},
		},
		Health:   HealthHealthy,
		LastSeen: time.Now(),
	}

	t.Run("Service remains discoverable after index set expiration", func(t *testing.T) {
		// Register the test service
		err := registry.Register(ctx, testService)
		require.NoError(t, err, "Failed to register test service")

		// Verify initial discovery works
		t.Log("=== Initial Discovery Check ===")
		services, err := discovery.FindByCapability(ctx, "test-capability")
		require.NoError(t, err, "Initial capability discovery failed")
		assert.Len(t, services, 1, "Should find 1 service initially")
		assert.Equal(t, testService.ID, services[0].ID, "Should find correct service")

		services, err = discovery.FindService(ctx, "ttl-test-service")
		require.NoError(t, err, "Initial name discovery failed")
		assert.Len(t, services, 1, "Should find 1 service by name initially")

		// Check initial TTLs
		t.Log("=== Checking Initial TTLs ===")
		serviceKey := "truvag3-ttl-test:services:test-service-ttl-refresh"
		capabilityKey := "truvag3-ttl-test:capabilities:test-capability"
		nameKey := "truvag3-ttl-test:names:ttl-test-service"
		typeKey := "truvag3-ttl-test:types:tool"

		serviceTTL, _ := client.TTL(ctx, serviceKey).Result()
		capabilityTTL, _ := client.TTL(ctx, capabilityKey).Result()
		nameTTL, _ := client.TTL(ctx, nameKey).Result()
		typeTTL, _ := client.TTL(ctx, typeKey).Result()

		t.Logf("Initial TTLs - Service: %v, Capability: %v, Name: %v, Type: %v",
			serviceTTL, capabilityTTL, nameTTL, typeTTL)

		// Service TTL should be ~10s, index sets should be ~20s
		assert.InDelta(t, 10.0, serviceTTL.Seconds(), 3.0, "Service TTL should be ~10s")
		assert.InDelta(t, 20.0, capabilityTTL.Seconds(), 3.0, "Capability index TTL should be ~20s")

		// Wait 8 seconds (service still alive but getting close to expiration)
		t.Log("=== Waiting 8 seconds (service key nearing expiration) ===")
		time.Sleep(8 * time.Second)

		// Send heartbeat to refresh service key and (with our fix) index sets
		t.Log("=== Sending heartbeat (should refresh both service and index TTLs) ===")
		err = registry.UpdateHealth(ctx, testService.ID, HealthHealthy)
		require.NoError(t, err, "Heartbeat should succeed")

		// Check TTLs after heartbeat
		serviceTTL, _ = client.TTL(ctx, serviceKey).Result()
		capabilityTTL, _ = client.TTL(ctx, capabilityKey).Result()
		nameTTL, _ = client.TTL(ctx, nameKey).Result()
		typeTTL, _ = client.TTL(ctx, typeKey).Result()

		t.Logf("TTLs after heartbeat - Service: %v, Capability: %v, Name: %v, Type: %v",
			serviceTTL, capabilityTTL, nameTTL, typeTTL)

		// After heartbeat, both service and index TTLs should be refreshed
		assert.InDelta(t, 10.0, serviceTTL.Seconds(), 3.0, "Service TTL should be refreshed to ~10s")
		assert.InDelta(t, 20.0, capabilityTTL.Seconds(), 3.0, "Capability index TTL should be refreshed to ~20s")

		// Service should still be discoverable via filtered discovery
		t.Log("=== Testing discovery after heartbeat ===")
		services, err = discovery.FindByCapability(ctx, "test-capability")
		require.NoError(t, err, "Capability discovery after heartbeat failed")
		assert.Len(t, services, 1, "Should still find 1 service after heartbeat")
		assert.Equal(t, testService.ID, services[0].ID, "Should find correct service after heartbeat")

		services, err = discovery.FindService(ctx, "ttl-test-service")
		require.NoError(t, err, "Name discovery after heartbeat failed")
		assert.Len(t, services, 1, "Should still find 1 service by name after heartbeat")

		// Wait another 8 seconds and send another heartbeat
		t.Log("=== Waiting another 8 seconds for second heartbeat cycle ===")
		time.Sleep(8 * time.Second)

		err = registry.UpdateHealth(ctx, testService.ID, HealthHealthy)
		require.NoError(t, err, "Second heartbeat should succeed")

		// Service should still be discoverable
		t.Log("=== Testing discovery after second heartbeat ===")
		services, err = discovery.FindByCapability(ctx, "test-capability")
		require.NoError(t, err, "Capability discovery after second heartbeat failed")
		assert.Len(t, services, 1, "Should still find 1 service after second heartbeat")

		services, err = discovery.FindService(ctx, "ttl-test-service")
		require.NoError(t, err, "Name discovery after second heartbeat failed")
		assert.Len(t, services, 1, "Should still find 1 service by name after second heartbeat")

		t.Log("SUCCESS: Service remains discoverable through multiple heartbeat cycles")
	})

	t.Run("Service disappears when heartbeat stops", func(t *testing.T) {
		// Wait for all TTLs to expire (25 seconds should be enough)
		t.Log("=== Waiting for TTLs to expire without heartbeat ===")
		time.Sleep(25 * time.Second)

		// Now service should be gone from all discovery methods
		services, err := discovery.FindByCapability(ctx, "test-capability")
		require.NoError(t, err, "Discovery should not error even when no services found")
		assert.Len(t, services, 0, "Should find no services after TTL expiration")

		services, err = discovery.FindService(ctx, "ttl-test-service")
		require.NoError(t, err, "Discovery should not error even when no services found")
		assert.Len(t, services, 0, "Should find no services by name after TTL expiration")

		t.Log("SUCCESS: Service correctly disappears when heartbeat stops")
	})

	// Clean up test data
	keys, _ = client.Keys(ctx, pattern).Result()
	if len(keys) > 0 {
		client.Del(ctx, keys...)
	}
}

// TestIndexSetTTLRefreshWithFailures tests that the fix is robust against Redis errors
func TestIndexSetTTLRefreshWithFailures(t *testing.T) {
	requireRedis(t)

	ctx := context.Background()

	// Create Redis client for test operations
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer client.Close()

	// Clean up any existing test data
	pattern := "truvag3-ttl-failure-test:*"
	keys, _ := client.Keys(ctx, pattern).Result()
	if len(keys) > 0 {
		client.Del(ctx, keys...)
	}

	// Create registry
	registry, err := NewRedisRegistryWithNamespace("redis://localhost:6379", "truvag3-ttl-failure-test")
	require.NoError(t, err, "Failed to create test registry")

	// Test service
	testService := &ServiceInfo{
		ID:      "test-service-failure",
		Name:    "failure-test-service",
		Type:    "tool",
		Address: "localhost",
		Port:    9998,
		Capabilities: []Capability{
			{Name: "test-capability-failure", Description: "Test capability"},
		},
		Health:   HealthHealthy,
		LastSeen: time.Now(),
	}

	t.Run("UpdateHealth succeeds even if index TTL refresh fails", func(t *testing.T) {
		// Register service
		err := registry.Register(ctx, testService)
		require.NoError(t, err, "Failed to register test service")

		// Delete one of the index sets to simulate partial failure
		capabilityKey := "truvag3-ttl-failure-test:capabilities:test-capability-failure"
		client.Del(ctx, capabilityKey)

		// UpdateHealth should still succeed even though capability index doesn't exist
		err = registry.UpdateHealth(ctx, testService.ID, HealthHealthy)
		assert.NoError(t, err, "UpdateHealth should succeed even with missing index sets")

		// Service key should still be updated
		serviceKey := "truvag3-ttl-failure-test:services:test-service-failure"
		exists, _ := client.Exists(ctx, serviceKey).Result()
		assert.Equal(t, int64(1), exists, "Service key should still exist after UpdateHealth")

		t.Log("SUCCESS: UpdateHealth is robust against index refresh failures")
	})

	// Clean up
	keys, _ = client.Keys(ctx, pattern).Result()
	if len(keys) > 0 {
		client.Del(ctx, keys...)
	}
}
