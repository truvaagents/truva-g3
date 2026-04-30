package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// Note: We directly inject discovery into BaseAgent instead of wrapping

// setupMockAgentWithDiscovery creates an AIAgent with mock discovery and AI client
func setupMockAgentWithDiscovery(tools []*core.ServiceInfo, agents []*core.ServiceInfo) *AIAgent {
	// Create mock discovery with predefined services
	mockDiscovery := core.NewMockDiscovery()
	ctx := context.Background()

	// Register tools
	for _, tool := range tools {
		mockDiscovery.Register(ctx, tool)
	}

	// Register agents
	for _, agent := range agents {
		mockDiscovery.Register(ctx, agent)
	}

	// Create mock AI client
	mockAI := &mockAIClient{
		generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: "AI generated response for: " + prompt,
				Model:   "mock-gpt-4",
			}, nil
		},
	}

	// Create BaseAgent and inject discovery
	baseAgent := core.NewBaseAgent("test-agent")
	baseAgent.Discovery = mockDiscovery

	// Create AIAgent
	aiAgent := &AIAgent{
		BaseAgent: baseAgent,
		AI:        mockAI,
		aiClient:  mockAI,
	}

	return aiAgent
}

// TestProcessWithAI tests the critical ProcessWithAI method
func TestProcessWithAI(t *testing.T) {
	// Sample tools that would be discovered
	tools := []*core.ServiceInfo{
		{
			ID:          "calculator-tool",
			Name:        "calculator",
			Type:        core.ComponentTypeTool,
			Description: "Mathematical calculations",
			Address:     "localhost",
			Port:        8080,
			Capabilities: []core.Capability{
				{
					Name:        "add",
					Description: "Add two numbers",
					Endpoint:    "/add",
					InputTypes:  []string{"number", "number"},
					OutputTypes: []string{"number"},
				},
				{
					Name:        "subtract",
					Description: "Subtract two numbers",
					Endpoint:    "/subtract",
					InputTypes:  []string{"number", "number"},
					OutputTypes: []string{"number"},
				},
			},
		},
		{
			ID:          "weather-tool",
			Name:        "weather-service",
			Type:        core.ComponentTypeTool,
			Description: "Weather information service",
			Address:     "localhost",
			Port:        8081,
			Capabilities: []core.Capability{
				{
					Name:        "get_weather",
					Description: "Get current weather for a location",
					Endpoint:    "/weather",
					InputTypes:  []string{"string"},
					OutputTypes: []string{"object"},
				},
			},
		},
	}

	// Sample agents that would be discovered
	agents := []*core.ServiceInfo{
		{
			ID:          "orchestrator-agent",
			Name:        "orchestrator",
			Type:        core.ComponentTypeAgent,
			Description: "Coordinates multiple tools and agents",
			Address:     "localhost",
			Port:        9090,
			Capabilities: []core.Capability{
				{
					Name:        "coordinate",
					Description: "Coordinate multiple services",
					Endpoint:    "/coordinate",
					InputTypes:  []string{"object"},
					OutputTypes: []string{"object"},
				},
			},
		},
	}

	tests := []struct {
		name             string
		request          string
		tools            []*core.ServiceInfo
		agents           []*core.ServiceInfo
		expectError      bool
		validateResponse func(*testing.T, *core.AIResponse)
	}{
		{
			name:        "successful processing with tools and agents",
			request:     "Calculate 5 + 3 and get weather for Seattle",
			tools:       tools,
			agents:      agents,
			expectError: false,
			validateResponse: func(t *testing.T, resp *core.AIResponse) {
				if resp == nil {
					t.Fatal("Expected non-nil response")
				}
				if resp.Content == "" {
					t.Error("Expected non-empty response content")
				}
				if resp.Model != "mock-gpt-4" {
					t.Errorf("Expected model 'mock-gpt-4', got %s", resp.Model)
				}
				// Should contain the original request in the prompt
				if !strings.Contains(resp.Content, "Calculate 5 + 3 and get weather for Seattle") {
					t.Error("Response should reference the original request")
				}
			},
		},
		{
			name:        "processing with only tools",
			request:     "Add 10 and 20",
			tools:       tools,
			agents:      []*core.ServiceInfo{}, // No agents
			expectError: false,
			validateResponse: func(t *testing.T, resp *core.AIResponse) {
				if resp == nil {
					t.Fatal("Expected non-nil response")
				}
				if resp.Content == "" {
					t.Error("Expected non-empty response content")
				}
			},
		},
		{
			name:        "processing with no available services",
			request:     "Help me with something",
			tools:       []*core.ServiceInfo{}, // No tools
			agents:      []*core.ServiceInfo{}, // No agents
			expectError: false,                 // Should still work, just with limited context
			validateResponse: func(t *testing.T, resp *core.AIResponse) {
				if resp == nil {
					t.Fatal("Expected non-nil response")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := setupMockAgentWithDiscovery(tt.tools, tt.agents)
			ctx := context.Background()

			response, err := agent.ProcessWithAI(ctx, tt.request)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if tt.validateResponse != nil {
					tt.validateResponse(t, response)
				}
			}
		})
	}
}

// TestDiscoverAndOrchestrate tests the high-level orchestration method
func TestDiscoverAndOrchestrate(t *testing.T) {
	tools := []*core.ServiceInfo{
		{
			ID:          "data-processor",
			Name:        "data-processing-tool",
			Type:        core.ComponentTypeTool,
			Description: "Processes and transforms data",
			Capabilities: []core.Capability{
				{
					Name:        "transform_data",
					Description: "Transform data format",
					InputTypes:  []string{"object"},
					OutputTypes: []string{"object"},
				},
			},
		},
	}

	tests := []struct {
		name           string
		userQuery      string
		tools          []*core.ServiceInfo
		agents         []*core.ServiceInfo
		expectError    bool
		validateResult func(*testing.T, string)
	}{
		{
			name:        "successful orchestration",
			userQuery:   "Process this data and generate a report",
			tools:       tools,
			agents:      []*core.ServiceInfo{},
			expectError: false,
			validateResult: func(t *testing.T, result string) {
				if result == "" {
					t.Error("Expected non-empty orchestration result")
				}
				// Should contain AI responses
				if !strings.Contains(result, "AI generated response") {
					t.Error("Result should contain AI generated content")
				}
			},
		},
		{
			name:        "orchestration with no available tools",
			userQuery:   "Help me with my task",
			tools:       []*core.ServiceInfo{},
			agents:      []*core.ServiceInfo{},
			expectError: false, // Should still attempt orchestration
			validateResult: func(t *testing.T, result string) {
				if result == "" {
					t.Error("Expected some result even with no tools")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := setupMockAgentWithDiscovery(tt.tools, tt.agents)
			ctx := context.Background()

			result, err := agent.DiscoverAndOrchestrate(ctx, tt.userQuery)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if tt.validateResult != nil {
					tt.validateResult(t, result)
				}
			}
		})
	}
}

// TestBuildContextPrompt tests the prompt generation logic
func TestBuildContextPrompt(t *testing.T) {

	tools := []*core.ServiceInfo{
		{
			ID:          "calculator",
			Name:        "calculator-tool",
			Type:        core.ComponentTypeTool,
			Description: "Mathematical calculations",
			Capabilities: []core.Capability{
				{
					Name:        "add",
					Description: "Add two numbers",
				},
				{
					Name:        "multiply",
					Description: "Multiply two numbers",
				},
			},
		},
	}

	agents := []*core.ServiceInfo{
		{
			ID:          "coordinator",
			Name:        "coordination-agent",
			Type:        core.ComponentTypeAgent,
			Description: "Coordinates multiple services",
			Capabilities: []core.Capability{
				{
					Name:        "orchestrate",
					Description: "Orchestrate multiple tools",
				},
			},
		},
	}

	tests := []struct {
		name           string
		tools          []*core.ServiceInfo
		agents         []*core.ServiceInfo
		request        string
		validatePrompt func(*testing.T, string)
	}{
		{
			name:    "prompt with tools and agents",
			tools:   tools,
			agents:  agents,
			request: "Calculate 5 * 3 and coordinate with other services",
			validatePrompt: func(t *testing.T, prompt string) {
				// Should contain tools section (correct format from buildContextPrompt)
				if !strings.Contains(prompt, "TOOLS (passive components):") {
					t.Error("Prompt should contain TOOLS section")
				}
				if !strings.Contains(prompt, "calculator-tool") {
					t.Error("Prompt should contain calculator tool name")
				}
				if !strings.Contains(prompt, "add") && !strings.Contains(prompt, "multiply") {
					t.Error("Prompt should contain tool capabilities")
				}

				// Should contain agents section (correct format from buildContextPrompt)
				if !strings.Contains(prompt, "AGENTS (active orchestrators):") {
					t.Error("Prompt should contain AGENTS section")
				}
				if !strings.Contains(prompt, "coordination-agent") {
					t.Error("Prompt should contain agent name")
				}
				if !strings.Contains(prompt, "orchestrate") {
					t.Error("Prompt should contain agent capabilities")
				}

				// Should contain the user request
				if !strings.Contains(prompt, "Calculate 5 * 3 and coordinate with other services") {
					t.Error("Prompt should contain user request")
				}
			},
		},
		{
			name:    "prompt with only tools",
			tools:   tools,
			agents:  []*core.ServiceInfo{},
			request: "Just do math calculations",
			validatePrompt: func(t *testing.T, prompt string) {
				if !strings.Contains(prompt, "TOOLS (passive components):") {
					t.Error("Prompt should contain TOOLS section")
				}
				if strings.Contains(prompt, "AGENTS (active orchestrators):") {
					t.Error("Prompt should not contain AGENTS section when no agents available")
				}
				if !strings.Contains(prompt, "Just do math calculations") {
					t.Error("Prompt should contain user request")
				}
			},
		},
		{
			name:    "prompt with no services",
			tools:   []*core.ServiceInfo{},
			agents:  []*core.ServiceInfo{},
			request: "Help me please",
			validatePrompt: func(t *testing.T, prompt string) {
				if strings.Contains(prompt, "TOOLS (passive components):") || strings.Contains(prompt, "AGENTS (active orchestrators):") {
					t.Error("Prompt should not contain TOOLS or AGENTS sections when none available")
				}
				if !strings.Contains(prompt, "Help me please") {
					t.Error("Prompt should contain user request")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use reflection or method exposure to test buildContextPrompt
			// Since it's a private method, we'll test it indirectly through ProcessWithAI
			agent := setupMockAgentWithDiscovery(tt.tools, tt.agents)

			// Capture the prompt by using a mock AI client that records the prompt
			var capturedPrompt string
			agent.AI = &mockAIClient{
				generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
					capturedPrompt = prompt
					return &core.AIResponse{
						Content: "test response",
						Model:   "test-model",
					}, nil
				},
			}

			ctx := context.Background()
			_, err := agent.ProcessWithAI(ctx, tt.request)
			if err != nil {
				t.Fatalf("ProcessWithAI failed: %v", err)
			}

			if capturedPrompt == "" {
				t.Fatal("No prompt was captured")
			}

			tt.validatePrompt(t, capturedPrompt)
		})
	}
}

// TestAIAgentWithNilClients tests error handling when AI client is not configured
func TestAIAgentWithNilClients(t *testing.T) {
	agent := &AIAgent{
		BaseAgent: core.NewBaseAgent("test-agent"),
		AI:        nil,
		aiClient:  nil,
	}

	// Set up discovery but no AI client
	mockDiscovery := core.NewMockDiscovery()
	agent.Discovery = mockDiscovery

	ctx := context.Background()

	// ProcessWithAI should return error when no AI client configured
	_, err := agent.ProcessWithAI(ctx, "test request")
	if err == nil {
		t.Error("Expected error when no AI client configured")
	}
	if !strings.Contains(err.Error(), "no AI client configured") {
		t.Errorf("Expected 'no AI client configured' error, got: %v", err)
	}

	// DiscoverAndOrchestrate should also return error
	_, err = agent.DiscoverAndOrchestrate(ctx, "test query")
	if err == nil {
		t.Error("Expected error when no AI client configured")
	}
}
