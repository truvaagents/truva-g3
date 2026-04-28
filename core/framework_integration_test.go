//go:build integration
// +build integration

package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFrameworkToolRegistrationFix verifies the framework auto-injection fix
func TestFrameworkToolRegistrationFix(t *testing.T) {
	t.Run("tool_registers_automatically_via_framework", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Create tool with ONLY framework options (no manual Registry setup)
		tool := NewTool("framework-test-tool")
		tool.RegisterCapability(Capability{
			Name:        "test_service",
			Description: "Framework test service",
		})

		// Framework should auto-initialize Registry
		framework, err := NewFramework(tool,
			WithPort(8083),
			WithDevelopmentMode(true),    // Use mock for testing
			WithDiscovery(true, "redis"), // Enable discovery
		)
		require.NoError(t, err, "Framework creation should succeed")

		// Create agent to discover the tool
		agent := NewBaseAgent("framework-test-agent")
		agentFramework, err := NewFramework(agent,
			WithPort(8084),
			WithDevelopmentMode(true),    // Use mock for testing
			WithDiscovery(true, "redis"), // Enable discovery
		)
		require.NoError(t, err, "Agent framework creation should succeed")

		// Start tool in background
		toolDone := make(chan error, 1)
		go func() {
			toolDone <- framework.Run(ctx)
		}()

		// Start agent in background
		agentDone := make(chan error, 1)
		go func() {
			agentDone <- agentFramework.Run(ctx)
		}()

		// Give frameworks time to initialize
		time.Sleep(1 * time.Second)

		// Verify tool has auto-initialized Registry
		assert.NotNil(t, tool.Registry, "Tool.Registry should be auto-initialized by framework")

		// Verify agent can discover services
		allServices, err := agent.Discover(ctx, DiscoveryFilter{})
		require.NoError(t, err, "Agent discovery should work")
		t.Logf("Discovered %d services total", len(allServices))

		// Filter for tools specifically
		var tools []*ServiceInfo
		for _, service := range allServices {
			if service.Type == ComponentTypeTool {
				tools = append(tools, service)
				t.Logf("Found tool: Name='%s', ID='%s', Type='%s'", service.Name, service.ID, service.Type)
			}
		}

		assert.Greater(t, len(tools), 0, "Should discover at least 1 tool")

		// Find our specific tool (framework generates ID with our name + random suffix)
		var ourTool *ServiceInfo
		for _, tool := range tools {
			if tool.Name == "framework-test-tool" ||
				strings.Contains(tool.ID, "framework-test-tool") {
				ourTool = tool
				break
			}
		}

		require.NotNil(t, ourTool, "Our framework-managed tool should be discovered")

		// Verify tool details (framework may override name to default)
		assert.True(t, ourTool.Name == "framework-test-tool" || ourTool.Name == "truvag3-agent",
			"Tool name should be original or framework default")
		assert.Equal(t, ComponentTypeTool, ourTool.Type)
		assert.Len(t, ourTool.Capabilities, 1)
		assert.Equal(t, "test_service", ourTool.Capabilities[0].Name)

		t.Log("SUCCESS: Framework dependency injection is working!")
		t.Log("Tool auto-initialized Registry and registered successfully")
		t.Log("Agent discovered tool through framework-managed discovery")

		// Clean shutdown
		cancel()
	})
}
