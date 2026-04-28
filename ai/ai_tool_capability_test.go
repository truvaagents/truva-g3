package ai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// TestRegisterAICapability tests the critical RegisterAICapability method
func TestRegisterAICapability(t *testing.T) {
	tests := []struct {
		name            string
		capabilityName  string
		description     string
		prompt          string
		requestBody     string
		mockAIResponse  *core.AIResponse
		mockAIError     error
		expectedStatus  int
		expectedContent string
		validateRequest func(*testing.T, string) // Validate the prompt sent to AI
	}{
		{
			name:           "successful AI capability processing",
			capabilityName: "translate",
			description:    "Translates text",
			prompt:         "Translate the following text to French:",
			requestBody:    "Hello world",
			mockAIResponse: &core.AIResponse{
				Content: "Bonjour le monde",
				Model:   "gpt-3.5-turbo",
			},
			mockAIError:     nil,
			expectedStatus:  200,
			expectedContent: "Bonjour le monde",
			validateRequest: func(t *testing.T, prompt string) {
				expectedPrompt := "Translate the following text to French:\n\nInput: Hello world"
				if prompt != expectedPrompt {
					t.Errorf("Expected prompt '%s', got '%s'", expectedPrompt, prompt)
				}
			},
		},
		{
			name:           "empty request body",
			capabilityName: "summarize",
			description:    "Summarizes text",
			prompt:         "Summarize this text:",
			requestBody:    "",
			mockAIResponse: &core.AIResponse{
				Content: "No content to summarize",
				Model:   "gpt-3.5-turbo",
			},
			mockAIError:     nil,
			expectedStatus:  200,
			expectedContent: "No content to summarize",
			validateRequest: func(t *testing.T, prompt string) {
				expectedPrompt := "Summarize this text:\n\nInput: "
				if prompt != expectedPrompt {
					t.Errorf("Expected prompt '%s', got '%s'", expectedPrompt, prompt)
				}
			},
		},
		{
			name:           "AI processing error",
			capabilityName: "analyze",
			description:    "Analyzes sentiment",
			prompt:         "Analyze sentiment:",
			requestBody:    "I hate this",
			mockAIResponse: nil,
			mockAIError:    fmt.Errorf("AI service temporarily unavailable"),
			expectedStatus: 500,
		},
		{
			name:           "complex multi-line input",
			capabilityName: "review",
			description:    "Reviews code",
			prompt:         "Review this code for bugs:",
			requestBody: `func add(a, b int) int {
    return a + b
}`,
			mockAIResponse: &core.AIResponse{
				Content: "The function looks correct. No bugs found.",
				Model:   "gpt-4",
			},
			mockAIError:     nil,
			expectedStatus:  200,
			expectedContent: "The function looks correct. No bugs found.",
			validateRequest: func(t *testing.T, prompt string) {
				if !strings.Contains(prompt, "Review this code for bugs:") {
					t.Error("Prompt should contain the base prompt")
				}
				if !strings.Contains(prompt, "func add(a, b int)") {
					t.Error("Prompt should contain the input code")
				}
				if !strings.Contains(prompt, "\n\nInput: ") {
					t.Error("Prompt should have correct format with Input: prefix")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock AI client that captures the prompt
			var capturedPrompt string
			var capturedOptions *core.AIOptions
			mockAI := &mockAIClient{
				generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
					capturedPrompt = prompt
					capturedOptions = options
					if tt.mockAIError != nil {
						return nil, tt.mockAIError
					}
					return tt.mockAIResponse, nil
				},
			}

			// Create AITool with mock AI client
			tool := &AITool{
				BaseTool: core.NewTool("test-tool"),
				aiClient: mockAI,
			}

			// Register the AI capability
			tool.RegisterAICapability(tt.capabilityName, tt.description, tt.prompt)

			// Verify capability was registered
			if len(tool.Capabilities) != 1 {
				t.Fatalf("Expected 1 capability, got %d", len(tool.Capabilities))
			}

			capability := tool.Capabilities[0]
			if capability.Name != tt.capabilityName {
				t.Errorf("Expected capability name '%s', got '%s'", tt.capabilityName, capability.Name)
			}
			if capability.Description != tt.description {
				t.Errorf("Expected description '%s', got '%s'", tt.description, capability.Description)
			}
			expectedEndpoint := "/ai/" + tt.capabilityName
			if capability.Endpoint != expectedEndpoint {
				t.Errorf("Expected endpoint '%s', got '%s'", expectedEndpoint, capability.Endpoint)
			}

			// Test the HTTP handler
			req := httptest.NewRequest("POST", capability.Endpoint, bytes.NewBufferString(tt.requestBody))
			req.Header.Set("Content-Type", "text/plain")
			recorder := httptest.NewRecorder()

			// Call the handler
			capability.Handler(recorder, req)

			// Check status code
			if recorder.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, recorder.Code)
			}

			// For successful requests, validate AI interaction and response
			if tt.expectedStatus == 200 {
				// Verify AI was called with correct parameters
				if capturedPrompt == "" {
					t.Error("Expected AI to be called with a prompt")
				}
				if tt.validateRequest != nil {
					tt.validateRequest(t, capturedPrompt)
				}

				// Verify AI options are set correctly (from ProcessWithAI)
				if capturedOptions == nil {
					t.Error("Expected AI options to be set")
				} else {
					if capturedOptions.Model != "gpt-3.5-turbo" {
						t.Errorf("Expected model 'gpt-3.5-turbo', got '%s'", capturedOptions.Model)
					}
					if capturedOptions.Temperature != 0.7 {
						t.Errorf("Expected temperature 0.7, got %f", capturedOptions.Temperature)
					}
					if capturedOptions.MaxTokens != 500 {
						t.Errorf("Expected max tokens 500, got %d", capturedOptions.MaxTokens)
					}
				}

				// Check response content
				responseBody := recorder.Body.String()
				if responseBody != tt.expectedContent {
					t.Errorf("Expected response '%s', got '%s'", tt.expectedContent, responseBody)
				}

				// Check content type header
				contentType := recorder.Header().Get("Content-Type")
				if contentType != "text/plain" {
					t.Errorf("Expected Content-Type 'text/plain', got '%s'", contentType)
				}
			}
		})
	}
}

// TestRegisterAICapabilityErrorScenarios tests error handling in RegisterAICapability
func TestRegisterAICapabilityErrorScenarios(t *testing.T) {
	tests := []struct {
		name             string
		requestBodyError bool // Simulate request body read error
		expectedStatus   int
	}{
		{
			name:             "request body read error",
			requestBodyError: true,
			expectedStatus:   400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock AI client (won't be called in error scenarios)
			mockAI := &mockAIClient{}

			// Create AITool
			tool := &AITool{
				BaseTool: core.NewTool("test-tool"),
				aiClient: mockAI,
			}

			// Register capability
			tool.RegisterAICapability("test", "test capability", "test prompt")

			// Create request that will cause body read error
			var req *http.Request
			if tt.requestBodyError {
				// Create a request with a body that will error on read
				req = httptest.NewRequest("POST", "/ai/test", &errorReader{})
			}

			recorder := httptest.NewRecorder()

			// Call handler
			tool.Capabilities[0].Handler(recorder, req)

			// Check status
			if recorder.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, recorder.Code)
			}
		})
	}
}

// errorReader simulates an io.Reader that returns an error
type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, fmt.Errorf("simulated read error")
}

// TestRegisterAICapabilityMultiple tests registering multiple AI capabilities
func TestRegisterAICapabilityMultiple(t *testing.T) {
	mockAI := &mockAIClient{
		generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
			// Return different responses based on prompt content
			if strings.Contains(prompt, "translate") {
				return &core.AIResponse{Content: "translated text"}, nil
			} else if strings.Contains(prompt, "summarize") {
				return &core.AIResponse{Content: "summary text"}, nil
			}
			return &core.AIResponse{Content: "default response"}, nil
		},
	}

	tool := &AITool{
		BaseTool: core.NewTool("multi-tool"),
		aiClient: mockAI,
	}

	// Register multiple capabilities
	tool.RegisterAICapability("translate", "Translation service", "Translate this:")
	tool.RegisterAICapability("summarize", "Summarization service", "Summarize this:")
	tool.RegisterAICapability("analyze", "Analysis service", "Analyze this:")

	// Verify all capabilities were registered
	if len(tool.Capabilities) != 3 {
		t.Fatalf("Expected 3 capabilities, got %d", len(tool.Capabilities))
	}

	// Test each capability endpoint
	capabilities := map[string]string{
		"translate": "/ai/translate",
		"summarize": "/ai/summarize",
		"analyze":   "/ai/analyze",
	}

	for name, expectedEndpoint := range capabilities {
		// Find the capability
		var cap *core.Capability
		for i := range tool.Capabilities {
			if tool.Capabilities[i].Name == name {
				cap = &tool.Capabilities[i]
				break
			}
		}

		if cap == nil {
			t.Errorf("Capability '%s' not found", name)
			continue
		}

		// Check endpoint
		if cap.Endpoint != expectedEndpoint {
			t.Errorf("Expected endpoint '%s' for '%s', got '%s'", expectedEndpoint, name, cap.Endpoint)
		}

		// Test the handler works
		req := httptest.NewRequest("POST", cap.Endpoint, strings.NewReader("test input"))
		recorder := httptest.NewRecorder()
		cap.Handler(recorder, req)

		if recorder.Code != 200 {
			t.Errorf("Handler for '%s' returned status %d, expected 200", name, recorder.Code)
		}
	}
}

// TestRegisterAICapabilityWithNilAIClient tests error handling when AI client is nil
func TestRegisterAICapabilityWithNilAIClient(t *testing.T) {
	tool := &AITool{
		BaseTool: core.NewTool("test-tool"),
		aiClient: nil, // Nil AI client
	}

	// Register capability
	tool.RegisterAICapability("test", "test capability", "test prompt")

	// Try to use the capability
	req := httptest.NewRequest("POST", "/ai/test", strings.NewReader("test input"))
	recorder := httptest.NewRecorder()

	// This should panic or return an error
	defer func() {
		if r := recover(); r == nil {
			// If no panic, check if we got an error status
			if recorder.Code != 500 {
				t.Error("Expected either panic or 500 status with nil AI client")
			}
		}
	}()

	tool.Capabilities[0].Handler(recorder, req)
}

// aiToolTestLogger for testing WithAIToolLogger
type aiToolTestLogger struct {
	infoCalls  []string
	errorCalls []string
}

func (l *aiToolTestLogger) Info(msg string, fields map[string]interface{}) {
	l.infoCalls = append(l.infoCalls, msg)
}
func (l *aiToolTestLogger) Error(msg string, fields map[string]interface{}) {
	l.errorCalls = append(l.errorCalls, msg)
}
func (l *aiToolTestLogger) Warn(msg string, fields map[string]interface{})  {}
func (l *aiToolTestLogger) Debug(msg string, fields map[string]interface{}) {}
func (l *aiToolTestLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (l *aiToolTestLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (l *aiToolTestLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (l *aiToolTestLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}

func TestWithAIToolLogger(t *testing.T) {
	tests := []struct {
		name      string
		logger    core.Logger
		expectNil bool
	}{
		{
			name:      "with logger",
			logger:    &aiToolTestLogger{},
			expectNil: false,
		},
		{
			name:      "with nil logger",
			logger:    nil,
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &aiToolConfig{}
			opt := WithAIToolLogger(tt.logger)
			opt(config)

			if tt.expectNil {
				if config.logger != nil {
					t.Error("expected nil logger in config")
				}
			} else {
				if config.logger == nil {
					t.Error("expected non-nil logger in config")
				}
				if config.logger != tt.logger {
					t.Error("logger not set correctly in config")
				}
			}
		})
	}
}
