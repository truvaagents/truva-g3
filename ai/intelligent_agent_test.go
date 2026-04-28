package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// mockAIClientForAgent implements core.AIClient for testing
type mockAIClientForAgent struct {
	responses []string
	index     int
	err       error
}

func (m *mockAIClientForAgent) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	if m.err != nil {
		return nil, m.err
	}

	if m.index >= len(m.responses) {
		return nil, errors.New("no more responses")
	}

	response := m.responses[m.index]
	m.index++

	return &core.AIResponse{
		Content: response,
		Model:   "test-model",
		Usage:   core.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}, nil
}

func TestNewIntelligentAgent(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantNil bool
	}{
		{
			name:    "valid agent creation",
			id:      "test-agent",
			wantNil: false,
		},
		{
			name:    "empty id",
			id:      "",
			wantNil: false, // Should still create agent with empty ID
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := NewIntelligentAgent(tt.id)

			if tt.wantNil && agent != nil {
				t.Error("expected nil agent, got non-nil")
			}

			if !tt.wantNil && agent == nil {
				t.Error("expected non-nil agent, got nil")
			}

			if agent != nil && agent.ID != tt.id {
				t.Errorf("expected agent ID %q, got %q", tt.id, agent.ID)
			}
		})
	}
}

func TestIntelligentAgent_GenerateResponse(t *testing.T) {
	tests := []struct {
		name      string
		agent     *IntelligentAgent
		prompt    string
		options   *core.AIOptions
		wantError bool
		wantText  string
	}{
		{
			name: "successful response",
			agent: &IntelligentAgent{
				BaseAgent: core.NewBaseAgent("test"),
				AI: &mockAIClientForAgent{
					responses: []string{"Generated response"},
				},
			},
			prompt:    "Test prompt",
			options:   &core.AIOptions{Model: "test-model"},
			wantError: false,
			wantText:  "Generated response",
		},
		{
			name: "nil AI client",
			agent: &IntelligentAgent{
				BaseAgent: core.NewBaseAgent("test"),
				AI:        nil,
			},
			prompt:    "Test prompt",
			wantError: true,
		},
		{
			name: "AI client error",
			agent: &IntelligentAgent{
				BaseAgent: core.NewBaseAgent("test"),
				AI: &mockAIClientForAgent{
					err: errors.New("AI error"),
				},
			},
			prompt:    "Test prompt",
			wantError: true,
		},
		{
			name: "with nil options",
			agent: &IntelligentAgent{
				BaseAgent: core.NewBaseAgent("test"),
				AI: &mockAIClientForAgent{
					responses: []string{"Response with nil options"},
				},
			},
			prompt:    "Test prompt",
			options:   nil,
			wantError: false,
			wantText:  "Response with nil options",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := tt.agent.GenerateResponse(context.Background(), tt.prompt, tt.options)

			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if response == nil {
				t.Error("expected response, got nil")
				return
			}

			if response.Content != tt.wantText {
				t.Errorf("expected content %q, got %q", tt.wantText, response.Content)
			}
		})
	}
}

func TestIntelligentAgent_SetAI(t *testing.T) {
	agent := NewIntelligentAgent("test")

	// Initially AI should be nil
	if agent.AI != nil {
		t.Error("expected AI to be nil initially")
	}

	// Set AI client
	mockClient := &mockAIClientForAgent{
		responses: []string{"test"},
	}
	agent.SetAI(mockClient)

	if agent.AI != mockClient {
		t.Error("AI client was not set correctly")
	}

	// Test that we can use the set AI client
	resp, err := agent.GenerateResponse(context.Background(), "test", nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if resp.Content != "test" {
		t.Errorf("expected 'test', got %q", resp.Content)
	}
}

func TestIntelligentAgent_ProcessWithMemory(t *testing.T) {
	tests := []struct {
		name         string
		agent        *IntelligentAgent
		input        string
		wantError    bool
		wantResponse string
		checkMemory  bool
	}{
		{
			name: "process with memory storage",
			agent: &IntelligentAgent{
				BaseAgent: func() *core.BaseAgent {
					ba := core.NewBaseAgent("test")
					ba.Memory = core.NewMemoryStore()
					return ba
				}(),
				AI: &mockAIClientForAgent{
					responses: []string{"Processed: input"},
				},
			},
			input:        "test input",
			wantError:    false,
			wantResponse: "Processed: input",
			checkMemory:  true,
		},
		{
			name: "process without memory",
			agent: &IntelligentAgent{
				BaseAgent: core.NewBaseAgent("test"), // No memory set
				AI: &mockAIClientForAgent{
					responses: []string{"Processed without memory"},
				},
			},
			input:        "test input",
			wantError:    false,
			wantResponse: "Processed without memory",
			checkMemory:  false,
		},
		{
			name: "process with AI error",
			agent: &IntelligentAgent{
				BaseAgent: func() *core.BaseAgent {
					ba := core.NewBaseAgent("test")
					ba.Memory = core.NewMemoryStore()
					return ba
				}(),
				AI: &mockAIClientForAgent{
					err: errors.New("AI failure"),
				},
			},
			input:     "test input",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.agent.ProcessWithMemory(context.Background(), tt.input)

			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result != tt.wantResponse {
				t.Errorf("expected response %q, got %q", tt.wantResponse, result)
			}

			// Check if memory was updated
			if tt.checkMemory && tt.agent.Memory != nil {
				ctx := context.Background()

				// Check if input was stored
				storedInput, _ := tt.agent.Memory.Get(ctx, "input:test input")
				if storedInput != "test input" {
					t.Errorf("expected input to be stored in memory")
				}

				// Check if response was stored
				storedResponse, _ := tt.agent.Memory.Get(ctx, "response:test input")
				if storedResponse != tt.wantResponse {
					t.Errorf("expected response to be stored in memory")
				}
			}
		})
	}
}

func TestIntelligentAgent_ThinkAndAct(t *testing.T) {
	tests := []struct {
		name      string
		agent     *IntelligentAgent
		situation string
		responses []string // Multiple responses for think then act
		wantError bool
		wantPlan  string
		wantAct   string
	}{
		{
			name: "successful think and act",
			agent: &IntelligentAgent{
				BaseAgent: core.NewBaseAgent("test"),
				AI: &mockAIClientForAgent{
					responses: []string{
						"Plan: I will solve this step by step",
						"Action: Execute the plan",
					},
				},
			},
			situation: "Complex problem",
			wantError: false,
			wantPlan:  "Plan: I will solve this step by step",
			wantAct:   "Action: Execute the plan",
		},
		{
			name: "error during thinking",
			agent: &IntelligentAgent{
				BaseAgent: core.NewBaseAgent("test"),
				AI: &mockAIClientForAgent{
					err: errors.New("thinking failed"),
				},
			},
			situation: "Complex problem",
			wantError: true,
		},
		{
			name: "error during acting",
			agent: &IntelligentAgent{
				BaseAgent: core.NewBaseAgent("test"),
				AI: &mockAIClientForAgent{
					responses: []string{
						"Plan: I will solve this",
						// Second call will fail due to no more responses
					},
				},
			},
			situation: "Complex problem",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, action, err := tt.agent.ThinkAndAct(context.Background(), tt.situation)

			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if plan != tt.wantPlan {
				t.Errorf("expected plan %q, got %q", tt.wantPlan, plan)
			}

			if action != tt.wantAct {
				t.Errorf("expected action %q, got %q", tt.wantAct, action)
			}
		})
	}
}

func TestIntelligentAgent_Integration(t *testing.T) {
	// Test the full integration of an intelligent agent
	agent := NewIntelligentAgent("integration-test")

	// Set up memory
	agent.Memory = core.NewMemoryStore()

	// Set up AI client with multiple responses
	agent.AI = &mockAIClientForAgent{
		responses: []string{
			"Hello, I am an intelligent agent",
			"I can remember our conversation",
			"Plan: Analyze the data",
			"Action: Process the results",
		},
	}

	ctx := context.Background()

	// Test basic response
	resp1, err := agent.GenerateResponse(ctx, "Introduce yourself", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp1.Content != "Hello, I am an intelligent agent" {
		t.Errorf("unexpected response: %q", resp1.Content)
	}

	// Test process with memory
	resp2, err := agent.ProcessWithMemory(ctx, "Remember this")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp2 != "I can remember our conversation" {
		t.Errorf("unexpected response: %q", resp2)
	}

	// Verify memory was updated
	stored, _ := agent.Memory.Get(ctx, "response:Remember this")
	if stored != "I can remember our conversation" {
		t.Error("memory was not updated correctly")
	}

	// Test think and act
	plan, action, err := agent.ThinkAndAct(ctx, "Process data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan != "Plan: Analyze the data" {
		t.Errorf("unexpected plan: %q", plan)
	}
	if action != "Action: Process the results" {
		t.Errorf("unexpected action: %q", action)
	}
}
