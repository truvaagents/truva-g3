package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// TestNewAIAgentErrorScenarios tests error scenarios in AIAgent creation
func TestNewAIAgentErrorScenarios(t *testing.T) {
	// Save and restore original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	tests := []struct {
		name        string
		agentName   string
		apiKey      string
		setup       func()
		expectError bool
		errorMsg    string
	}{
		{
			name:        "empty agent name",
			agentName:   "",
			apiKey:      "valid-key",
			setup:       setupValidOpenAIProvider,
			expectError: false, // BaseAgent should handle empty name gracefully
		},
		{
			name:        "empty API key",
			agentName:   "test-agent",
			apiKey:      "",
			setup:       setupValidOpenAIProvider,
			expectError: false, // Should create agent but might fail later during usage
		},
		{
			name:      "no providers available",
			agentName: "test-agent",
			apiKey:    "valid-key",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
			},
			expectError: true,
			errorMsg:    "failed to create AI client",
		},
		{
			name:      "openai provider not registered",
			agentName: "test-agent",
			apiKey:    "valid-key",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				// Register other providers but not openai
				registry.providers["anthropic"] = &mockFactory{
					name:      "anthropic",
					available: true,
					client:    &mockAIClient{},
				}
			},
			expectError: true,
			errorMsg:    "provider 'openai' not registered",
		},
		{
			name:        "very long agent name",
			agentName:   strings.Repeat("a", 1000),
			apiKey:      "valid-key",
			setup:       setupValidOpenAIProvider,
			expectError: false, // Should handle long names
		},
		{
			name:        "unicode agent name",
			agentName:   "agent-🤖-with-unicode",
			apiKey:      "valid-key",
			setup:       setupValidOpenAIProvider,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			agent, err := NewAIAgent(tt.agentName, tt.apiKey)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if agent == nil {
					t.Error("Expected agent to be created")
				}

				// Validate agent structure
				if agent.BaseAgent == nil {
					t.Error("Expected BaseAgent to be set")
				}
				if agent.AI == nil {
					t.Error("Expected AI client to be set")
				}
				if agent.aiClient == nil {
					t.Error("Expected internal aiClient to be set")
				}
				// Both AI and aiClient should point to the same instance
				if agent.AI != agent.aiClient {
					t.Error("Expected AI and aiClient to be the same instance")
				}
			}
		})
	}
}

// TestNewAIToolErrorScenarios tests error scenarios in AITool creation
func TestNewAIToolErrorScenarios(t *testing.T) {
	// Save and restore original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	tests := []struct {
		name        string
		toolName    string
		apiKey      string
		setup       func()
		expectError bool
		errorMsg    string
	}{
		{
			name:        "empty tool name",
			toolName:    "",
			apiKey:      "valid-key",
			setup:       setupValidOpenAIProvider,
			expectError: false, // BaseTool should handle empty name gracefully
		},
		{
			name:        "empty API key",
			toolName:    "test-tool",
			apiKey:      "",
			setup:       setupValidOpenAIProvider,
			expectError: false, // Should create tool but might fail later during usage
		},
		{
			name:     "no providers available",
			toolName: "test-tool",
			apiKey:   "valid-key",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
			},
			expectError: true,
			errorMsg:    "failed to create AI client",
		},
		{
			name:        "very long tool name",
			toolName:    strings.Repeat("b", 1000),
			apiKey:      "valid-key",
			setup:       setupValidOpenAIProvider,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			tool, err := NewAITool(tt.toolName, tt.apiKey)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if tool == nil {
					t.Error("Expected tool to be created")
				}

				// Validate tool structure
				if tool.BaseTool == nil {
					t.Error("Expected BaseTool to be set")
				}
				if tool.aiClient == nil {
					t.Error("Expected AI client to be set")
				}
				// BaseTool should have AI field set
				if tool.AI == nil {
					t.Error("Expected BaseTool.AI to be set")
				}
			}
		})
	}
}

// TestNewIntelligentAgentBackwardCompatibility tests the backward compatibility function
func TestNewIntelligentAgentBackwardCompatibility(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		validate func(*testing.T, *IntelligentAgent)
	}{
		{
			name: "normal ID",
			id:   "test-agent-123",
			validate: func(t *testing.T, agent *IntelligentAgent) {
				if agent.BaseAgent == nil {
					t.Error("Expected BaseAgent to be set")
				}
				if agent.ID != "test-agent-123" {
					t.Errorf("Expected ID to be 'test-agent-123', got %s", agent.ID)
				}
				if agent.AI != nil {
					t.Error("Expected AI to be nil (should be set later)")
				}
				if agent.aiClient != nil {
					t.Error("Expected aiClient to be nil (should be set later)")
				}
			},
		},
		{
			name: "empty ID",
			id:   "",
			validate: func(t *testing.T, agent *IntelligentAgent) {
				if agent.BaseAgent == nil {
					t.Error("Expected BaseAgent to be set")
				}
				if agent.ID != "" {
					t.Errorf("Expected ID to be empty, got %s", agent.ID)
				}
			},
		},
		{
			name: "unicode ID",
			id:   "agent-🚀-unicode",
			validate: func(t *testing.T, agent *IntelligentAgent) {
				if agent.ID != "agent-🚀-unicode" {
					t.Errorf("Expected unicode ID to be preserved, got %s", agent.ID)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := NewIntelligentAgent(tt.id)

			if agent == nil {
				t.Fatal("Expected agent to be created")
			}

			tt.validate(t, agent)
		})
	}
}

// TestAIAgentGenerateResponse tests the GenerateResponse method
func TestAIAgentGenerateResponse(t *testing.T) {
	tests := []struct {
		name         string
		setupAgent   func() *AIAgent
		prompt       string
		options      *core.AIOptions
		expectError  bool
		errorMsg     string
		validateResp func(*testing.T, *core.AIResponse)
	}{
		{
			name: "successful response with AI field",
			setupAgent: func() *AIAgent {
				mockClient := &mockAIClient{
					generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
						return &core.AIResponse{
							Content: "AI response: " + prompt,
							Model:   "test-model",
						}, nil
					},
				}
				return &AIAgent{
					BaseAgent: core.NewBaseAgent("test"),
					AI:        mockClient,
					aiClient:  nil, // AI field should be used
				}
			},
			prompt:      "test prompt",
			options:     &core.AIOptions{Model: "test"},
			expectError: false,
			validateResp: func(t *testing.T, resp *core.AIResponse) {
				if resp.Content != "AI response: test prompt" {
					t.Errorf("Expected 'AI response: test prompt', got %s", resp.Content)
				}
			},
		},
		{
			name: "fallback to aiClient field when AI is nil",
			setupAgent: func() *AIAgent {
				mockClient := &mockAIClient{
					generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
						return &core.AIResponse{
							Content: "fallback response: " + prompt,
						}, nil
					},
				}
				return &AIAgent{
					BaseAgent: core.NewBaseAgent("test"),
					AI:        nil,        // AI field is nil
					aiClient:  mockClient, // Should fallback to this
				}
			},
			prompt:      "fallback test",
			options:     nil,
			expectError: false,
			validateResp: func(t *testing.T, resp *core.AIResponse) {
				if resp.Content != "fallback response: fallback test" {
					t.Errorf("Expected 'fallback response: fallback test', got %s", resp.Content)
				}
			},
		},
		{
			name: "error when both AI fields are nil",
			setupAgent: func() *AIAgent {
				return &AIAgent{
					BaseAgent: core.NewBaseAgent("test"),
					AI:        nil,
					aiClient:  nil,
				}
			},
			prompt:      "should fail",
			options:     nil,
			expectError: true,
			errorMsg:    "no AI client configured",
		},
		{
			name: "AI client returns error",
			setupAgent: func() *AIAgent {
				mockClient := &mockAIClient{
					generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
						return nil, errors.New("AI service unavailable")
					},
				}
				return &AIAgent{
					BaseAgent: core.NewBaseAgent("test"),
					AI:        mockClient,
					aiClient:  nil,
				}
			},
			prompt:      "error test",
			options:     nil,
			expectError: true,
			errorMsg:    "AI service unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.setupAgent()
			ctx := context.Background()

			resp, err := agent.GenerateResponse(ctx, tt.prompt, tt.options)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if resp == nil {
					t.Error("Expected response to be returned")
				}
				if tt.validateResp != nil {
					tt.validateResp(t, resp)
				}
			}
		})
	}
}

// TestAIToolProcessWithAI tests the ProcessWithAI method
func TestAIToolProcessWithAI(t *testing.T) {
	tests := []struct {
		name        string
		setupTool   func() *AITool
		input       string
		expectError bool
		errorMsg    string
		validateOut func(*testing.T, string)
	}{
		{
			name: "successful processing",
			setupTool: func() *AITool {
				mockClient := &mockAIClient{
					generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
						// Verify that options are set correctly
						if options.Model != "gpt-3.5-turbo" {
							return nil, errors.New("expected model to be gpt-3.5-turbo")
						}
						if options.Temperature != 0.7 {
							return nil, errors.New("expected temperature to be 0.7")
						}
						if options.MaxTokens != 500 {
							return nil, errors.New("expected max tokens to be 500")
						}
						return &core.AIResponse{
							Content: "Processed: " + prompt,
						}, nil
					},
				}
				return &AITool{
					BaseTool: core.NewTool("test-tool"),
					aiClient: mockClient,
				}
			},
			input:       "test input",
			expectError: false,
			validateOut: func(t *testing.T, output string) {
				if output != "Processed: test input" {
					t.Errorf("Expected 'Processed: test input', got %s", output)
				}
			},
		},
		{
			name: "AI client returns error",
			setupTool: func() *AITool {
				mockClient := &mockAIClient{
					generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
						return nil, errors.New("rate limit exceeded")
					},
				}
				return &AITool{
					BaseTool: core.NewTool("test-tool"),
					aiClient: mockClient,
				}
			},
			input:       "test input",
			expectError: true,
			errorMsg:    "AI processing failed",
		},
		{
			name: "nil AI client",
			setupTool: func() *AITool {
				return &AITool{
					BaseTool: core.NewTool("test-tool"),
					aiClient: nil,
				}
			},
			input:       "test input",
			expectError: true, // Should panic or error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := tt.setupTool()
			ctx := context.Background()

			// Handle potential panics from nil AI client
			defer func() {
				if r := recover(); r != nil && !tt.expectError {
					t.Errorf("Unexpected panic: %v", r)
				}
			}()

			output, err := tool.ProcessWithAI(ctx, tt.input)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				} else if tt.errorMsg != "" && !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if tt.validateOut != nil {
					tt.validateOut(t, output)
				}
			}
		})
	}
}

// setupValidOpenAIProvider sets up a valid openai provider for testing
func setupValidOpenAIProvider() {
	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}
	registry.providers["openai"] = &mockFactory{
		name:      "openai",
		available: true,
		client:    &mockAIClient{},
	}
}
