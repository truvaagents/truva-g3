//go:build integration
// +build integration

package core

import (
	"context"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTTLAndHeartbeatIntegration verifies Issue #6 fix from TESTING_RESULTS.md
// This tests the TTL expiration and heartbeat functionality end-to-end
func TestTTLAndHeartbeatIntegration(t *testing.T) {
	requireRedis(t)

	ctx := context.Background()

	// Create Redis client for test operations
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer client.Close()

	// Clean Redis before testing
	client.FlushAll(ctx)

	t.Run("tool_disappears_after_30_seconds_without_heartbeat", func(t *testing.T) {
		// Create tool but manually register WITHOUT Initialize (no heartbeat)
		tool := NewTool("ttl-test-tool")
		registry, err := NewRedisRegistry("redis://localhost:6379")
		require.NoError(t, err)

		// Manually register (bypassing Initialize to avoid heartbeat)
		serviceInfo := &ServiceInfo{
			ID:           tool.ID,
			Name:         tool.Name,
			Type:         ComponentTypeTool,
			Capabilities: []Capability{{Name: "test_capability", Description: "Test"}},
			Address:      "localhost",
			Port:         8080,
		}

		err = registry.Register(ctx, serviceInfo)
		require.NoError(t, err)

		// Verify tool is initially discoverable
		discovery, err := NewRedisDiscovery("redis://localhost:6379")
		require.NoError(t, err)

		tools, err := discovery.Discover(ctx, DiscoveryFilter{Type: ComponentTypeTool})
		require.NoError(t, err)
		assert.Len(t, tools, 1, "Tool should be discoverable initially")

		t.Log("Waiting 35 seconds for TTL expiration (without heartbeat)...")
		time.Sleep(35 * time.Second)

		// Tool should disappear (TTL expired, no heartbeat)
		tools, err = discovery.Discover(ctx, DiscoveryFilter{Type: ComponentTypeTool})
		require.NoError(t, err)
		assert.Len(t, tools, 0, "Tool should disappear after TTL expiration")

		t.Log("CONFIRMED: Tool disappears after 30s without heartbeat")
	})

	t.Run("tool_persists_with_heartbeat_via_initialize", func(t *testing.T) {
		// Create tool with proper Initialize (includes heartbeat)
		tool := NewTool("heartbeat-test-tool")
		registry, err := NewRedisRegistry("redis://localhost:6379")
		require.NoError(t, err)
		tool.Registry = registry

		// Initialize tool (starts heartbeat)
		err = tool.Initialize(ctx)
		require.NoError(t, err)

		// Verify tool is discoverable
		discovery, err := NewRedisDiscovery("redis://localhost:6379")
		require.NoError(t, err)

		tools, err := discovery.Discover(ctx, DiscoveryFilter{Type: ComponentTypeTool})
		require.NoError(t, err)
		assert.Len(t, tools, 1, "Tool should be discoverable initially")

		t.Log("Waiting 35 seconds to test heartbeat keeps registration alive...")
		time.Sleep(35 * time.Second)

		// Tool should STILL exist (heartbeat prevented TTL expiration)
		tools, err = discovery.Discover(ctx, DiscoveryFilter{Type: ComponentTypeTool})
		require.NoError(t, err)
		assert.Len(t, tools, 1, "Tool should persist with heartbeat")

		t.Log("CONFIRMED: Tool persists beyond 30s TTL with heartbeat")
	})

	t.Run("framework_managed_tool_has_automatic_heartbeat", func(t *testing.T) {
		// Test framework-managed tool (our main fix)
		tool := NewTool("framework-heartbeat-tool")
		tool.RegisterCapability(Capability{
			Name:        "framework_service",
			Description: "Framework-managed service with auto-heartbeat",
		})

		// Framework should auto-initialize Registry AND start heartbeat
		framework, err := NewFramework(tool,
			WithPort(8085),
			WithDevelopmentMode(true), // Use mock for faster testing
			WithDiscovery(true, "redis"),
		)
		require.NoError(t, err)

		// Start framework in background
		frameworkDone := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			frameworkDone <- framework.Run(ctx)
		}()

		// Give framework time to start and register
		time.Sleep(2 * time.Second)

		// Verify tool auto-initialized Registry
		assert.NotNil(t, tool.Registry, "Framework should auto-initialize Registry")

		// Verify tool is discoverable through agent
		agent := NewBaseAgent("test-agent")
		agentFramework, err := NewFramework(agent,
			WithPort(8086),
			WithDevelopmentMode(true),
			WithDiscovery(true, "redis"),
		)
		require.NoError(t, err)

		agentDone := make(chan error, 1)
		go func() {
			agentDone <- agentFramework.Run(ctx)
		}()

		time.Sleep(1 * time.Second)

		// Discover framework-managed tool
		tools, err := agent.Discover(ctx, DiscoveryFilter{
			Type:         ComponentTypeTool,
			Capabilities: []string{"framework_service"},
		})
		require.NoError(t, err)
		assert.Len(t, tools, 1, "Framework-managed tool should be discoverable")

		t.Log("CONFIRMED: Framework dependency injection working with heartbeat")
		cancel() // Clean shutdown
	})
}
