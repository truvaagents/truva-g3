package ai

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// TestNewTranslationTool tests the pre-built translation tool
func TestNewTranslationTool(t *testing.T) {
	tests := []struct {
		name           string
		apiKey         string
		expectError    bool
		errorMsg       string
		validateTool   func(*testing.T, *AITool)
		testCapability bool
		inputText      string
		expectedOutput string
	}{
		{
			name:        "successful creation with valid API key",
			apiKey:      "test-api-key",
			expectError: false,
			validateTool: func(t *testing.T, tool *AITool) {
				// Verify tool name
				if tool.Name != "translation-tool" {
					t.Errorf("Expected tool name 'translation-tool', got '%s'", tool.Name)
				}

				// Verify capability was registered
				if len(tool.Capabilities) != 1 {
					t.Fatalf("Expected 1 capability, got %d", len(tool.Capabilities))
				}

				cap := tool.Capabilities[0]
				if cap.Name != "translate" {
					t.Errorf("Expected capability name 'translate', got '%s'", cap.Name)
				}
				if cap.Description != "Translates text between languages" {
					t.Errorf("Expected specific description, got '%s'", cap.Description)
				}
				if cap.Endpoint != "/ai/translate" {
					t.Errorf("Expected endpoint '/ai/translate', got '%s'", cap.Endpoint)
				}
			},
			testCapability: true,
			inputText:      "Hello world",
			expectedOutput: "Bonjour le monde",
		},
		{
			name:        "creation with empty API key",
			apiKey:      "",
			expectError: false, // NewAITool handles this
			validateTool: func(t *testing.T, tool *AITool) {
				if len(tool.Capabilities) != 1 {
					t.Errorf("Expected capability to be registered even with empty API key")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock the AI client creation to avoid actual API calls
			originalRegistry := registry
			defer func() { registry = originalRegistry }()

			// Set up mock registry
			registry = &ProviderRegistry{
				providers: make(map[string]ProviderFactory),
			}
			registry.providers["openai"] = &mockFactory{
				name:      "openai",
				available: true,
				client: &mockAIClient{
					generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
						// Verify the prompt contains the correct template
						expectedPromptStart := "You are a professional translator. Translate the following text, preserving meaning and context."
						if !strings.Contains(prompt, expectedPromptStart) {
							t.Errorf("Prompt should contain translation template, got: %s", prompt)
						}
						return &core.AIResponse{Content: tt.expectedOutput}, nil
					},
				},
			}

			tool, err := NewTranslationTool(tt.apiKey)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				if tt.errorMsg != "" && !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if tool == nil {
				t.Fatal("Expected tool to be created")
			}

			// Run tool validation
			if tt.validateTool != nil {
				tt.validateTool(t, tool)
			}

			// Test the capability if requested
			if tt.testCapability && len(tool.Capabilities) > 0 {
				req := httptest.NewRequest("POST", "/ai/translate", bytes.NewBufferString(tt.inputText))
				recorder := httptest.NewRecorder()

				tool.Capabilities[0].Handler(recorder, req)

				if recorder.Code != 200 {
					t.Errorf("Expected status 200, got %d", recorder.Code)
				}

				responseBody := recorder.Body.String()
				if responseBody != tt.expectedOutput {
					t.Errorf("Expected output '%s', got '%s'", tt.expectedOutput, responseBody)
				}
			}
		})
	}
}

// TestNewSummarizationTool tests the pre-built summarization tool
func TestNewSummarizationTool(t *testing.T) {
	// Set up mock registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}
	registry.providers["openai"] = &mockFactory{
		name:      "openai",
		available: true,
		client: &mockAIClient{
			generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
				// Verify the prompt contains the correct template
				expectedPromptStart := "You are an expert at summarization. Provide a concise summary of the following text, highlighting key points."
				if !strings.Contains(prompt, expectedPromptStart) {
					t.Errorf("Prompt should contain summarization template, got: %s", prompt)
				}
				return &core.AIResponse{Content: "Summary: Key points extracted"}, nil
			},
		},
	}

	tool, err := NewSummarizationTool("test-api-key")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify tool properties
	if tool.Name != "summarization-tool" {
		t.Errorf("Expected tool name 'summarization-tool', got '%s'", tool.Name)
	}

	// Verify capability
	if len(tool.Capabilities) != 1 {
		t.Fatalf("Expected 1 capability, got %d", len(tool.Capabilities))
	}

	cap := tool.Capabilities[0]
	if cap.Name != "summarize" {
		t.Errorf("Expected capability name 'summarize', got '%s'", cap.Name)
	}
	if cap.Description != "Summarizes long text into key points" {
		t.Errorf("Expected specific description, got '%s'", cap.Description)
	}
	if cap.Endpoint != "/ai/summarize" {
		t.Errorf("Expected endpoint '/ai/summarize', got '%s'", cap.Endpoint)
	}

	// Test the capability
	longText := "This is a very long text that needs to be summarized. It contains multiple sentences and important information that should be condensed."
	req := httptest.NewRequest("POST", "/ai/summarize", bytes.NewBufferString(longText))
	recorder := httptest.NewRecorder()

	cap.Handler(recorder, req)

	if recorder.Code != 200 {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	expectedOutput := "Summary: Key points extracted"
	responseBody := recorder.Body.String()
	if responseBody != expectedOutput {
		t.Errorf("Expected output '%s', got '%s'", expectedOutput, responseBody)
	}
}

// TestNewSentimentAnalysisTool tests the pre-built sentiment analysis tool
func TestNewSentimentAnalysisTool(t *testing.T) {
	// Set up mock registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}
	registry.providers["openai"] = &mockFactory{
		name:      "openai",
		available: true,
		client: &mockAIClient{
			generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
				// Verify the prompt contains the correct template
				expectedPromptPart := "You are a sentiment analysis expert. Analyze the sentiment of the following text and respond with: POSITIVE, NEGATIVE, or NEUTRAL"
				if !strings.Contains(prompt, expectedPromptPart) {
					t.Errorf("Prompt should contain sentiment analysis template, got: %s", prompt)
				}
				return &core.AIResponse{Content: "POSITIVE (85) - Expresses enthusiasm and satisfaction"}, nil
			},
		},
	}

	tool, err := NewSentimentAnalysisTool("test-api-key")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify tool properties
	if tool.Name != "sentiment-tool" {
		t.Errorf("Expected tool name 'sentiment-tool', got '%s'", tool.Name)
	}

	// Verify capability
	if len(tool.Capabilities) != 1 {
		t.Fatalf("Expected 1 capability, got %d", len(tool.Capabilities))
	}

	cap := tool.Capabilities[0]
	if cap.Name != "analyze_sentiment" {
		t.Errorf("Expected capability name 'analyze_sentiment', got '%s'", cap.Name)
	}
	if cap.Description != "Analyzes sentiment of text (positive, negative, neutral)" {
		t.Errorf("Expected specific description, got '%s'", cap.Description)
	}
	if cap.Endpoint != "/ai/analyze_sentiment" {
		t.Errorf("Expected endpoint '/ai/analyze_sentiment', got '%s'", cap.Endpoint)
	}

	// Test the capability
	text := "I love this product! It works perfectly and exceeds my expectations."
	req := httptest.NewRequest("POST", "/ai/analyze_sentiment", bytes.NewBufferString(text))
	recorder := httptest.NewRecorder()

	cap.Handler(recorder, req)

	if recorder.Code != 200 {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	expectedOutput := "POSITIVE (85) - Expresses enthusiasm and satisfaction"
	responseBody := recorder.Body.String()
	if responseBody != expectedOutput {
		t.Errorf("Expected output '%s', got '%s'", expectedOutput, responseBody)
	}
}

// TestNewCodeReviewTool tests the pre-built code review tool
func TestNewCodeReviewTool(t *testing.T) {
	// Set up mock registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}
	registry.providers["openai"] = &mockFactory{
		name:      "openai",
		available: true,
		client: &mockAIClient{
			generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
				// Verify the prompt contains the correct template
				expectedPromptParts := []string{
					"You are an expert code reviewer",
					"1. Potential bugs",
					"2. Performance issues",
					"3. Security vulnerabilities",
					"4. Code style and best practices",
					"5. Suggested improvements",
				}
				for _, part := range expectedPromptParts {
					if !strings.Contains(prompt, part) {
						t.Errorf("Prompt should contain '%s', got: %s", part, prompt)
					}
				}
				return &core.AIResponse{Content: "Code Review: No issues found. Good implementation."}, nil
			},
		},
	}

	tool, err := NewCodeReviewTool("test-api-key")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify tool properties
	if tool.Name != "code-review-tool" {
		t.Errorf("Expected tool name 'code-review-tool', got '%s'", tool.Name)
	}

	// Verify capability
	if len(tool.Capabilities) != 1 {
		t.Fatalf("Expected 1 capability, got %d", len(tool.Capabilities))
	}

	cap := tool.Capabilities[0]
	if cap.Name != "review_code" {
		t.Errorf("Expected capability name 'review_code', got '%s'", cap.Name)
	}
	if cap.Description != "Reviews code for quality, bugs, and improvements" {
		t.Errorf("Expected specific description, got '%s'", cap.Description)
	}
	if cap.Endpoint != "/ai/review_code" {
		t.Errorf("Expected endpoint '/ai/review_code', got '%s'", cap.Endpoint)
	}

	// Test the capability
	code := `function calculateSum(a, b) {
    return a + b;
}`
	req := httptest.NewRequest("POST", "/ai/review_code", bytes.NewBufferString(code))
	recorder := httptest.NewRecorder()

	cap.Handler(recorder, req)

	if recorder.Code != 200 {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	expectedOutput := "Code Review: No issues found. Good implementation."
	responseBody := recorder.Body.String()
	if responseBody != expectedOutput {
		t.Errorf("Expected output '%s', got '%s'", expectedOutput, responseBody)
	}
}

// TestPrebuiltToolsErrorHandling tests error scenarios for all pre-built tools
func TestPrebuiltToolsErrorHandling(t *testing.T) {
	// Set up mock registry that will cause AI client creation to fail
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	tests := []struct {
		name       string
		createTool func(string) (*AITool, error)
	}{
		{"NewTranslationTool", NewTranslationTool},
		{"NewSummarizationTool", NewSummarizationTool},
		{"NewSentimentAnalysisTool", NewSentimentAnalysisTool},
		{"NewCodeReviewTool", NewCodeReviewTool},
	}

	for _, tt := range tests {
		t.Run(tt.name+" with provider error", func(t *testing.T) {
			// Set up registry with no providers to force error
			registry = &ProviderRegistry{
				providers: make(map[string]ProviderFactory),
			}

			tool, err := tt.createTool("test-api-key")
			if err == nil {
				t.Error("Expected error when no providers available")
			}
			if tool != nil {
				t.Error("Expected nil tool when creation fails")
			}
		})
	}
}

// TestPrebuiltToolsWithFailingAI tests how pre-built tools handle AI failures
func TestPrebuiltToolsWithFailingAI(t *testing.T) {
	// Set up mock registry with failing AI client
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}
	registry.providers["openai"] = &mockFactory{
		name:      "openai",
		available: true,
		client: &mockAIClient{
			generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
				return nil, fmt.Errorf("AI service is down")
			},
		},
	}

	tools := map[string]*AITool{}
	var err error

	// Create all tools
	tools["translation"], err = NewTranslationTool("test-api-key")
	if err != nil {
		t.Fatalf("Failed to create translation tool: %v", err)
	}

	tools["summarization"], err = NewSummarizationTool("test-api-key")
	if err != nil {
		t.Fatalf("Failed to create summarization tool: %v", err)
	}

	tools["sentiment"], err = NewSentimentAnalysisTool("test-api-key")
	if err != nil {
		t.Fatalf("Failed to create sentiment tool: %v", err)
	}

	tools["code-review"], err = NewCodeReviewTool("test-api-key")
	if err != nil {
		t.Fatalf("Failed to create code review tool: %v", err)
	}

	// Test each tool's capability with failing AI
	for name, tool := range tools {
		t.Run(name+" with failing AI", func(t *testing.T) {
			if len(tool.Capabilities) == 0 {
				t.Fatal("Tool has no capabilities")
			}

			req := httptest.NewRequest("POST", tool.Capabilities[0].Endpoint, bytes.NewBufferString("test input"))
			recorder := httptest.NewRecorder()

			tool.Capabilities[0].Handler(recorder, req)

			// Should return 500 status when AI fails
			if recorder.Code != 500 {
				t.Errorf("Expected status 500 when AI fails, got %d", recorder.Code)
			}
		})
	}
}
