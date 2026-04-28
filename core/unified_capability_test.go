package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestUnifiedCapabilityPattern verifies that Tools and Agents follow the same capability pattern
func TestUnifiedCapabilityPattern(t *testing.T) {
	ctx := context.Background()

	t.Run("Tool capability registration", func(t *testing.T) {
		tool := NewTool("test-tool")

		// Test auto-endpoint generation
		tool.RegisterCapability(Capability{
			Name:        "process",
			Description: "Processes data",
		})

		// Verify endpoint was auto-generated
		if len(tool.Capabilities) != 1 {
			t.Fatalf("Expected 1 capability, got %d", len(tool.Capabilities))
		}

		cap := tool.Capabilities[0]
		if cap.Endpoint != "/api/capabilities/process" {
			t.Errorf("Expected auto-generated endpoint /api/capabilities/process, got %s", cap.Endpoint)
		}

		// Test with custom handler
		handlerCalled := false
		tool.RegisterCapability(Capability{
			Name:        "custom",
			Description: "Custom handler",
			Handler: func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			},
		})

		// Verify second capability
		if len(tool.Capabilities) != 2 {
			t.Fatalf("Expected 2 capabilities, got %d", len(tool.Capabilities))
		}

		// Test the custom handler
		req := httptest.NewRequest("GET", "/api/capabilities/custom", nil)
		w := httptest.NewRecorder()
		tool.Capabilities[1].Handler(w, req)

		if !handlerCalled {
			t.Error("Custom handler was not called")
		}
	})

	t.Run("Agent capability registration", func(t *testing.T) {
		agent := NewBaseAgent("test-agent")

		// Test auto-endpoint generation (same as Tool)
		agent.RegisterCapability(Capability{
			Name:        "analyze",
			Description: "Analyzes data",
		})

		// Verify endpoint was auto-generated
		if len(agent.Capabilities) != 1 {
			t.Fatalf("Expected 1 capability, got %d", len(agent.Capabilities))
		}

		cap := agent.Capabilities[0]
		if cap.Endpoint != "/api/capabilities/analyze" {
			t.Errorf("Expected auto-generated endpoint /api/capabilities/analyze, got %s", cap.Endpoint)
		}

		// Test with custom handler (same as Tool)
		handlerCalled := false
		agent.RegisterCapability(Capability{
			Name:        "custom",
			Description: "Custom handler",
			Handler: func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			},
		})

		// Verify second capability
		if len(agent.Capabilities) != 2 {
			t.Fatalf("Expected 2 capabilities, got %d", len(agent.Capabilities))
		}

		// Test the custom handler
		req := httptest.NewRequest("GET", "/api/capabilities/custom", nil)
		w := httptest.NewRecorder()
		agent.Capabilities[1].Handler(w, req)

		if !handlerCalled {
			t.Error("Custom handler was not called")
		}
	})

	t.Run("Tool standard endpoints", func(t *testing.T) {
		tool := NewTool("endpoint-test-tool")
		tool.RegisterCapability(Capability{
			Name:        "test",
			Description: "Test capability",
		})

		// Setup standard endpoints
		tool.setupStandardEndpoints()

		// Test /api/capabilities endpoint
		req := httptest.NewRequest("GET", "/api/capabilities", nil)
		w := httptest.NewRecorder()
		tool.mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		// Verify response contains capabilities
		var capabilities []Capability
		if err := json.NewDecoder(w.Body).Decode(&capabilities); err != nil {
			t.Fatalf("Failed to decode capabilities: %v", err)
		}

		if len(capabilities) != 1 {
			t.Errorf("Expected 1 capability in response, got %d", len(capabilities))
		}
	})

	t.Run("Framework accepts both Tools and Agents", func(t *testing.T) {
		// Test with Tool
		tool := NewTool("framework-tool")
		toolFramework, err := NewFramework(tool, WithPort(8080))
		if err != nil {
			t.Fatalf("Failed to create framework with tool: %v", err)
		}

		// Verify framework was created
		if toolFramework.component != tool {
			t.Error("Framework component is not the tool")
		}

		// Test with Agent
		agent := NewBaseAgent("framework-agent")
		agentFramework, err := NewFramework(agent, WithPort(8081))
		if err != nil {
			t.Fatalf("Failed to create framework with agent: %v", err)
		}

		// Verify framework was created
		if agentFramework.component != agent {
			t.Error("Framework component is not the agent")
		}
	})

	t.Run("Tool and Agent Start methods have same signature", func(t *testing.T) {
		tool := NewTool("start-test-tool")
		agent := NewBaseAgent("start-test-agent")

		// Both should start with context and port
		go func() {
			_ = tool.Start(ctx, 8090)
		}()

		go func() {
			_ = agent.Start(ctx, 8091)
		}()

		// Give them time to start
		time.Sleep(100 * time.Millisecond)

		// Shutdown both
		shutdownCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()

		_ = tool.Shutdown(shutdownCtx)
		// Note: Agent doesn't have Shutdown method in current implementation
		// but both can be stopped via context cancellation
	})

	t.Run("Generic handler for capabilities without custom handler", func(t *testing.T) {
		tool := NewTool("generic-handler-tool")

		// Register capability without handler
		tool.RegisterCapability(Capability{
			Name:        "generic",
			Description: "Generic capability",
			InputTypes:  []string{"json"},
			OutputTypes: []string{"json"},
		})

		// The capability should have a handler (generic one)
		if len(tool.Capabilities) != 1 {
			t.Fatalf("Expected 1 capability, got %d", len(tool.Capabilities))
		}

		// Test the generic handler by calling it directly
		req := httptest.NewRequest("POST", "/api/capabilities/generic", nil)
		w := httptest.NewRecorder()

		// Call the handleCapabilityRequest directly
		handler := tool.handleCapabilityRequest(tool.Capabilities[0])
		handler(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		// Verify response contains capability info
		var response map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if response["capability"] != "generic" {
			t.Errorf("Expected capability name in response")
		}

		if response["description"] != "Generic capability" {
			t.Errorf("Expected capability description in response")
		}
	})
}

// TestHTTPComponentInterface verifies that both Tool and Agent implement HTTPComponent
func TestHTTPComponentInterface(t *testing.T) {
	// This test ensures compile-time verification that both types implement HTTPComponent
	var _ HTTPComponent = (*BaseTool)(nil)
	var _ HTTPComponent = (*BaseAgent)(nil)

	// Also verify they implement their specific interfaces
	var _ Tool = (*BaseTool)(nil)
	var _ Agent = (*BaseAgent)(nil)
}
