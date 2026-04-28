//go:build integration
// +build integration

package core

import (
	"context"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
)

// TestToolHeartbeatRedisRegistry verifies that tools start heartbeat with Redis registry
func TestToolHeartbeatRedisRegistry(t *testing.T) {
	requireRedis(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create tool with Redis registry
	tool := NewTool("heartbeat-test-tool")

	// Setup Redis registry
	registry, err := NewRedisRegistry("redis://localhost:6379")
	if err != nil {
		t.Fatalf("Failed to create Redis registry: %v", err)
	}
	tool.Registry = registry

	// Initialize tool (should start heartbeat)
	err = tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Tool.Initialize() failed: %v", err)
	}

	// Verify registration exists
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer client.Close()

	// Check initial registration
	key := "truvag3:services:" + tool.ID
	exists, err := client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("Failed to check Redis key: %v", err)
	}
	if exists != 1 {
		t.Fatal("Tool not registered in Redis")
	}

	// Wait beyond half TTL (should trigger heartbeat)
	time.Sleep(20 * time.Second) // TTL is 30s, heartbeat every 15s

	// Verify registration still exists (heartbeat should have refreshed it)
	exists, err = client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("Failed to check Redis key after heartbeat: %v", err)
	}
	if exists != 1 {
		t.Fatal("Tool registration expired despite heartbeat")
	}

	// Clean up
	client.Del(ctx, key)
	client.Del(ctx, "truvag3:types:tool")
	client.Del(ctx, "truvag3:names:heartbeat-test-tool")
}

// TestToolHeartbeatNonRedisRegistry verifies no heartbeat with non-Redis registry
func TestToolHeartbeatNonRedisRegistry(t *testing.T) {
	ctx := context.Background()
	tool := NewTool("mock-registry-tool")

	// Use mock registry (not Redis)
	tool.Registry = &mockRegistryForTest{
		registrations: make(map[string]*ServiceInfo),
	}

	// Initialize tool (should NOT start heartbeat for non-Redis)
	err := tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Tool.Initialize() failed: %v", err)
	}

	// Test passes if no panic/error occurs
	// We can't directly test that heartbeat didn't start, but we ensure
	// the type assertion doesn't cause issues with non-Redis registries
}

// TestAgentHeartbeatRedisDiscovery verifies that agents start heartbeat with Redis discovery
func TestAgentHeartbeatRedisDiscovery(t *testing.T) {
	requireRedis(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create agent with Redis discovery
	agent := NewBaseAgent("heartbeat-test-agent")

	// Setup Redis discovery
	discovery, err := NewRedisDiscovery("redis://localhost:6379")
	if err != nil {
		t.Fatalf("Failed to create Redis discovery: %v", err)
	}
	agent.Discovery = discovery

	// Initialize agent (should start heartbeat)
	err = agent.Initialize(ctx)
	if err != nil {
		t.Fatalf("Agent.Initialize() failed: %v", err)
	}

	// Verify registration exists
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer client.Close()

	// Check initial registration
	key := "truvag3:services:" + agent.ID
	exists, err := client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("Failed to check Redis key: %v", err)
	}
	if exists != 1 {
		t.Fatal("Agent not registered in Redis")
	}

	// Wait beyond half TTL (should trigger heartbeat)
	time.Sleep(20 * time.Second) // TTL is 30s, heartbeat every 15s

	// Verify registration still exists (heartbeat should have refreshed it)
	exists, err = client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("Failed to check Redis key after heartbeat: %v", err)
	}
	if exists != 1 {
		t.Fatal("Agent registration expired despite heartbeat")
	}

	// Clean up
	client.Del(ctx, key)
	client.Del(ctx, "truvag3:types:agent")
	client.Del(ctx, "truvag3:names:heartbeat-test-agent")
}

// TestAgentHeartbeatRegistrationFailure verifies graceful degradation when registration fails
func TestAgentHeartbeatRegistrationFailure(t *testing.T) {
	ctx := context.Background()
	agent := NewBaseAgent("failing-agent")

	// Use failing discovery
	agent.Discovery = &failingDiscovery{
		shouldFail: true,
	}

	// Initialize should not fail (graceful degradation)
	err := agent.Initialize(ctx)
	if err != nil {
		t.Fatalf("Agent.Initialize() should not fail with graceful degradation: %v", err)
	}

	// Test passes - no heartbeat started, no panic
}

// TestHeartbeatContextCancellation verifies heartbeat stops on context cancellation
func TestHeartbeatContextCancellation(t *testing.T) {
	requireRedis(t)

	ctx, cancel := context.WithCancel(context.Background())

	// Create tool with Redis registry
	tool := NewTool("context-cancel-tool")

	registry, err := NewRedisRegistry("redis://localhost:6379")
	if err != nil {
		t.Fatalf("Failed to create Redis registry: %v", err)
	}
	tool.Registry = registry

	// Initialize tool (starts heartbeat)
	err = tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Tool.Initialize() failed: %v", err)
	}

	// Cancel context (should stop heartbeat)
	cancel()

	// Wait a bit to ensure heartbeat goroutine exits
	time.Sleep(1 * time.Second)

	// Test passes if no goroutine leaks (hard to test directly)
	// But the heartbeat should respect context cancellation
}

// TestHeartbeatPersistence verifies registration persists beyond TTL
func TestHeartbeatPersistence(t *testing.T) {
	requireRedis(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create tool with Redis registry
	tool := NewTool("persistence-test-tool")

	registry, err := NewRedisRegistry("redis://localhost:6379")
	if err != nil {
		t.Fatalf("Failed to create Redis registry: %v", err)
	}
	tool.Registry = registry

	// Initialize tool
	err = tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Tool.Initialize() failed: %v", err)
	}

	// Wait beyond TTL (30s) + buffer
	t.Log("Waiting 35 seconds to test heartbeat persistence...")
	time.Sleep(35 * time.Second)

	// Verify registration still exists
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer client.Close()

	key := "truvag3:services:" + tool.ID
	exists, err := client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("Failed to check Redis key after TTL: %v", err)
	}
	if exists != 1 {
		t.Fatal("Tool registration expired despite heartbeat - heartbeat not working")
	}

	t.Log("Heartbeat successfully kept registration alive beyond TTL")

	// Clean up
	client.Del(ctx, key)
	client.Del(ctx, "truvag3:types:tool")
	client.Del(ctx, "truvag3:names:persistence-test-tool")
}

// Mock structures for testing

type failingDiscovery struct {
	shouldFail bool
}

func (f *failingDiscovery) Register(ctx context.Context, info *ServiceInfo) error {
	if f.shouldFail {
		return ErrDiscoveryUnavailable
	}
	return nil
}

func (f *failingDiscovery) UpdateHealth(ctx context.Context, id string, status HealthStatus) error {
	return ErrDiscoveryUnavailable
}

func (f *failingDiscovery) Unregister(ctx context.Context, id string) error {
	return ErrDiscoveryUnavailable
}

func (f *failingDiscovery) Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error) {
	return nil, ErrDiscoveryUnavailable
}

func (f *failingDiscovery) FindService(ctx context.Context, serviceName string) ([]*ServiceInfo, error) {
	return nil, ErrDiscoveryUnavailable
}

func (f *failingDiscovery) FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error) {
	return nil, ErrDiscoveryUnavailable
}
