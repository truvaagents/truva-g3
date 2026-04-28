package mock

import (
	"context"
	"errors"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestFactory(t *testing.T) {
	factory := &Factory{}

	// Test Name
	if factory.Name() != "mock" {
		t.Errorf("expected name 'mock', got %q", factory.Name())
	}

	// Test Description
	if factory.Description() == "" {
		t.Error("expected non-empty description")
	}

	// Test Priority
	if factory.Priority() != 1 {
		t.Errorf("expected priority 1, got %d", factory.Priority())
	}

	// Test DetectEnvironment
	priority, available := factory.DetectEnvironment()
	if priority != 0 || available != false {
		t.Errorf("expected (0, false), got (%d, %v)", priority, available)
	}

	// Test Create
	config := &ai.AIConfig{
		Model: "test-model",
	}
	client := factory.Create(config)
	if client == nil {
		t.Error("expected non-nil client")
	}
}

func TestClient_GenerateResponse(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*Client)
		prompt      string
		options     *core.AIOptions
		wantContent string
		wantModel   string
		wantErr     bool
	}{
		{
			name:        "default response",
			prompt:      "test prompt",
			wantContent: "Mock response",
			wantModel:   "mock-model",
		},
		{
			name: "multiple responses",
			setup: func(c *Client) {
				c.SetResponses("First", "Second", "Third")
			},
			prompt:      "test",
			wantContent: "First",
			wantModel:   "mock-model",
		},
		{
			name: "with error",
			setup: func(c *Client) {
				c.SetError(errors.New("test error"))
			},
			prompt:  "test",
			wantErr: true,
		},
		{
			name:   "with options",
			prompt: "test",
			options: &core.AIOptions{
				Model:       "custom-model",
				MaxTokens:   100,
				Temperature: 0.7,
			},
			wantContent: "Mock response",
			wantModel:   "custom-model",
		},
		{
			name: "model from config",
			setup: func(c *Client) {
				c.Config = &ai.AIConfig{
					Model: "config-model",
				}
			},
			prompt:      "test",
			wantContent: "Mock response",
			wantModel:   "config-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(nil)

			if tt.setup != nil {
				tt.setup(client)
			}

			resp, err := client.GenerateResponse(context.Background(), tt.prompt, tt.options)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if resp.Content != tt.wantContent {
				t.Errorf("expected content %q, got %q", tt.wantContent, resp.Content)
			}

			if resp.Model != tt.wantModel {
				t.Errorf("expected model %q, got %q", tt.wantModel, resp.Model)
			}

			// Check that prompt and options were recorded
			if client.LastPrompt != tt.prompt {
				t.Errorf("expected LastPrompt %q, got %q", tt.prompt, client.LastPrompt)
			}

			if tt.options != nil && client.LastOptions != tt.options {
				t.Error("LastOptions not recorded correctly")
			}

			if client.CallCount != 1 {
				t.Errorf("expected CallCount 1, got %d", client.CallCount)
			}
		})
	}
}

func TestClient_MultipleResponses(t *testing.T) {
	client := NewClient(nil)
	client.SetResponses("One", "Two", "Three")

	ctx := context.Background()

	// First call
	resp1, err := client.GenerateResponse(ctx, "test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp1.Content != "One" {
		t.Errorf("expected 'One', got %q", resp1.Content)
	}

	// Second call
	resp2, err := client.GenerateResponse(ctx, "test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp2.Content != "Two" {
		t.Errorf("expected 'Two', got %q", resp2.Content)
	}

	// Third call
	resp3, err := client.GenerateResponse(ctx, "test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp3.Content != "Three" {
		t.Errorf("expected 'Three', got %q", resp3.Content)
	}

	// Fourth call should error
	_, err = client.GenerateResponse(ctx, "test", nil)
	if err == nil {
		t.Error("expected error when no more responses, got nil")
	}

	if client.CallCount != 4 {
		t.Errorf("expected CallCount 4, got %d", client.CallCount)
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	client := NewClient(nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := client.GenerateResponse(ctx, "test", nil)
	if err == nil {
		t.Error("expected context cancellation error, got nil")
	}

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestClient_Reset(t *testing.T) {
	client := NewClient(nil)
	client.SetResponses("One", "Two")
	client.SetError(errors.New("test"))

	// Make a call
	client.GenerateResponse(context.Background(), "test prompt", &core.AIOptions{Model: "test"})

	// Verify state before reset
	// Note: ResponseIndex should be 0 because error was returned before consuming response
	if client.ResponseIndex != 0 {
		t.Errorf("expected ResponseIndex 0 (error returned, no response consumed), got %d", client.ResponseIndex)
	}
	if client.CallCount != 1 {
		t.Errorf("expected CallCount 1, got %d", client.CallCount)
	}
	if client.LastPrompt != "test prompt" {
		t.Errorf("expected LastPrompt 'test prompt', got %q", client.LastPrompt)
	}
	if client.Error == nil {
		t.Error("expected Error to be set")
	}

	// Reset
	client.Reset()

	// Verify state after reset
	if client.ResponseIndex != 0 {
		t.Errorf("expected ResponseIndex 0 after reset, got %d", client.ResponseIndex)
	}
	if client.CallCount != 0 {
		t.Errorf("expected CallCount 0 after reset, got %d", client.CallCount)
	}
	if client.LastPrompt != "" {
		t.Errorf("expected empty LastPrompt after reset, got %q", client.LastPrompt)
	}
	if client.LastOptions != nil {
		t.Error("expected nil LastOptions after reset")
	}
	if client.Error != nil {
		t.Error("expected nil Error after reset")
	}
}

func TestClient_TokenUsage(t *testing.T) {
	client := NewClient(nil)

	prompt := "This is a test prompt"
	response := "This is a mock response"
	client.SetResponses(response)

	resp, err := client.GenerateResponse(context.Background(), prompt, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check token usage estimation
	expectedPromptTokens := len(prompt) / 4
	expectedCompletionTokens := len(response) / 4
	expectedTotalTokens := (len(prompt) + len(response)) / 4

	if resp.Usage.PromptTokens != expectedPromptTokens {
		t.Errorf("expected PromptTokens %d, got %d", expectedPromptTokens, resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != expectedCompletionTokens {
		t.Errorf("expected CompletionTokens %d, got %d", expectedCompletionTokens, resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != expectedTotalTokens {
		t.Errorf("expected TotalTokens %d, got %d", expectedTotalTokens, resp.Usage.TotalTokens)
	}
}
