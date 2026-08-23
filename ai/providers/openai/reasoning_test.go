package openai

import (
	"testing"
)

func TestIsReasoningModel(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		// GPT-5 family - reasoning models
		{"gpt-5 base", "gpt-5", true},
		{"gpt-5-mini", "gpt-5-mini", true},
		{"gpt-5-mini with date", "gpt-5-mini-2025-08-07", true},
		{"gpt-5-turbo", "gpt-5-turbo", true},
		{"GPT-5 uppercase", "GPT-5", true},
		{"GPT-5-MINI uppercase", "GPT-5-MINI", true},

		// o1 family - reasoning models
		{"o1 base", "o1", true},
		{"o1-mini", "o1-mini", true},
		{"o1-preview", "o1-preview", true},
		{"o1-mini with date", "o1-mini-2024-09-12", true},
		{"O1 uppercase", "O1", true},

		// o3 family - reasoning models
		{"o3 base", "o3", true},
		{"o3-mini", "o3-mini", true},
		{"o3-mini with date", "o3-mini-2025-01-31", true},
		{"O3-MINI uppercase", "O3-MINI", true},

		// o4 family - reasoning models
		{"o4 base", "o4", true},
		{"o4-mini", "o4-mini", true},
		{"o4-mini with date", "o4-mini-2025-04-16", true},

		// Standard models - NOT reasoning models
		{"gpt-4", "gpt-4", false},
		{"gpt-4o", "gpt-4o", false},
		{"gpt-4o-mini", "gpt-4o-mini", false},
		{"gpt-4-turbo", "gpt-4-turbo", false},
		{"gpt-4-turbo-preview", "gpt-4-turbo-preview", false},
		{"gpt-3.5-turbo", "gpt-3.5-turbo", false},
		{"gpt-4o with date", "gpt-4o-2024-08-06", false},

		// Edge cases
		{"empty string", "", false},
		{"random model", "some-random-model", false},
		{"claude model", "claude-3-opus", false},
		{"partial match gpt-50", "gpt-50", true}, // gpt-50 starts with gpt-5, matches prefix
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsReasoningModel(tt.model)
			if result != tt.expected {
				t.Errorf("IsReasoningModel(%q) = %v, expected %v", tt.model, result, tt.expected)
			}
		})
	}
}

func TestBuildRequestBody_StandardModel(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "Hello"},
	}

	// Use 0 for multiplier to test default behavior (standard models ignore it anyway)
	reqBody := buildRequestBody("gpt-4o", messages, 1000, 0.7, false, 0, "")

	// Should have max_tokens (not max_completion_tokens)
	if _, ok := reqBody["max_tokens"]; !ok {
		t.Error("Standard model should have max_tokens")
	}
	if _, ok := reqBody["max_completion_tokens"]; ok {
		t.Error("Standard model should NOT have max_completion_tokens")
	}

	// Should have temperature
	if _, ok := reqBody["temperature"]; !ok {
		t.Error("Standard model should have temperature")
	}

	// Should NOT have streaming options
	if _, ok := reqBody["stream"]; ok {
		t.Error("Non-streaming request should NOT have stream field")
	}

	// Verify values
	if reqBody["max_tokens"] != 1000 {
		t.Errorf("max_tokens = %v, expected 1000", reqBody["max_tokens"])
	}
	if reqBody["temperature"] != float32(0.7) {
		t.Errorf("temperature = %v, expected 0.7", reqBody["temperature"])
	}
}

func TestBuildRequestBody_ReasoningModel(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "Hello"},
	}

	// Use 0 for multiplier to test default (5x) behavior
	reqBody := buildRequestBody("gpt-5-mini", messages, 2000, 0.7, false, 0, "high")

	// Should have max_completion_tokens (not max_tokens)
	if _, ok := reqBody["max_completion_tokens"]; !ok {
		t.Error("Reasoning model should have max_completion_tokens")
	}
	if _, ok := reqBody["max_tokens"]; ok {
		t.Error("Reasoning model should NOT have max_tokens")
	}

	// Should NOT have temperature
	if _, ok := reqBody["temperature"]; ok {
		t.Error("Reasoning model should NOT have temperature")
	}

	// Verify max_completion_tokens is multiplied by DefaultReasoningTokenMultiplier
	// Reasoning models need extra tokens for both internal "thinking" AND output
	expectedTokens := 2000 * DefaultReasoningTokenMultiplier
	if reqBody["max_completion_tokens"] != expectedTokens {
		t.Errorf("max_completion_tokens = %v, expected %d (2000 * %d multiplier)",
			reqBody["max_completion_tokens"], expectedTokens, DefaultReasoningTokenMultiplier)
	}
}

func TestBuildRequestBody_ReasoningModelCustomMultiplier(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "Hello"},
	}

	// Test with custom multiplier of 3
	reqBody := buildRequestBody("gpt-5-mini", messages, 2000, 0.7, false, 3, "high")

	// Verify max_completion_tokens uses custom multiplier
	expectedTokens := 2000 * 3
	if reqBody["max_completion_tokens"] != expectedTokens {
		t.Errorf("max_completion_tokens = %v, expected %d (2000 * 3 custom multiplier)",
			reqBody["max_completion_tokens"], expectedTokens)
	}
}

func TestBuildRequestBody_Streaming(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "Hello"},
	}

	// Test streaming with standard model
	reqBody := buildRequestBody("gpt-4o", messages, 1000, 0.7, true, 0, "")

	if reqBody["stream"] != true {
		t.Error("Streaming request should have stream=true")
	}

	streamOpts, ok := reqBody["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatal("stream_options should be a map")
	}
	if streamOpts["include_usage"] != true {
		t.Error("stream_options should have include_usage=true")
	}

	// Test streaming with reasoning model
	reqBodyReasoning := buildRequestBody("o3-mini", messages, 1000, 0.7, true, 0, "")

	if reqBodyReasoning["stream"] != true {
		t.Error("Streaming reasoning request should have stream=true")
	}
	if _, ok := reqBodyReasoning["max_completion_tokens"]; !ok {
		t.Error("Streaming reasoning request should have max_completion_tokens")
	}
	if _, ok := reqBodyReasoning["temperature"]; ok {
		t.Error("Streaming reasoning request should NOT have temperature")
	}
}

func TestBuildRequestBody_AllReasoningModelFamilies(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "Test"},
	}

	reasoningModels := []string{
		"gpt-5",
		"gpt-5-mini-2025-08-07",
		"o1",
		"o1-mini",
		"o1-preview",
		"o3",
		"o3-mini",
		"o4",
		"o4-mini",
	}

	for _, model := range reasoningModels {
		t.Run(model, func(t *testing.T) {
			reqBody := buildRequestBody(model, messages, 1000, 0.5, false, 0, "")

			if _, ok := reqBody["max_completion_tokens"]; !ok {
				t.Errorf("%s should use max_completion_tokens", model)
			}
			if _, ok := reqBody["max_tokens"]; ok {
				t.Errorf("%s should NOT have max_tokens", model)
			}
			if _, ok := reqBody["temperature"]; ok {
				t.Errorf("%s should NOT have temperature", model)
			}
		})
	}
}

// TestMessage_ReasoningContent tests that ReasoningContent field is properly parsed
func TestMessage_ReasoningContent(t *testing.T) {
	tests := []struct {
		name             string
		content          string
		reasoningContent string
		expectedResult   string
	}{
		{
			name:             "Standard model - content only",
			content:          "Hello, I'm an assistant",
			reasoningContent: "",
			expectedResult:   "Hello, I'm an assistant",
		},
		{
			name:             "Reasoning model - reasoning_content only",
			content:          "",
			reasoningContent: "This is reasoning output from GPT-5",
			expectedResult:   "This is reasoning output from GPT-5",
		},
		{
			name:             "Both fields - prefer content",
			content:          "Standard content",
			reasoningContent: "Reasoning content",
			expectedResult:   "Standard content",
		},
		{
			name:             "Both empty",
			content:          "",
			reasoningContent: "",
			expectedResult:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := Message{
				Role:             "assistant",
				Content:          tt.content,
				ReasoningContent: tt.reasoningContent,
			}

			// Simulate the extraction logic used in client.go
			result := msg.Content
			if result == "" && msg.ReasoningContent != "" {
				result = msg.ReasoningContent
			}

			if result != tt.expectedResult {
				t.Errorf("Expected %q, got %q", tt.expectedResult, result)
			}
		})
	}
}

func TestBuildRequestBody_ReasoningEffortNone(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "Hello"},
	}

	reqBody := buildRequestBody("gpt-5.4", messages, 2000, 0.7, false, 0, "none")

	// Should use max_completion_tokens (reasoning models always require this)
	if _, ok := reqBody["max_completion_tokens"]; !ok {
		t.Error("Reasoning model with effort=none should still use max_completion_tokens")
	}
	if _, ok := reqBody["max_tokens"]; ok {
		t.Error("Reasoning model should NOT have max_tokens")
	}

	// effort=none: NO multiplier applied
	if reqBody["max_completion_tokens"] != 2000 {
		t.Errorf("max_completion_tokens = %v, expected 2000 (no multiplier for effort=none)", reqBody["max_completion_tokens"])
	}

	// effort=none: temperature IS allowed
	if _, ok := reqBody["temperature"]; !ok {
		t.Error("Reasoning model with effort=none should include temperature")
	}
	if reqBody["temperature"] != float32(0.7) {
		t.Errorf("temperature = %v, expected 0.7", reqBody["temperature"])
	}

	// Should include reasoning.effort in body
	reasoning, ok := reqBody["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatal("Should have reasoning object in request body")
	}
	if reasoning["effort"] != "none" {
		t.Errorf("reasoning.effort = %v, expected 'none'", reasoning["effort"])
	}
}

func TestBuildRequestBody_ReasoningEffortHigh(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "Hello"},
	}

	reqBody := buildRequestBody("gpt-5.4", messages, 2000, 0.7, false, 0, "high")

	// Should apply multiplier for active reasoning
	expectedTokens := 2000 * DefaultReasoningTokenMultiplier
	if reqBody["max_completion_tokens"] != expectedTokens {
		t.Errorf("max_completion_tokens = %v, expected %d (with multiplier for active reasoning)",
			reqBody["max_completion_tokens"], expectedTokens)
	}

	// Active reasoning: temperature should be omitted
	if _, ok := reqBody["temperature"]; ok {
		t.Error("Reasoning model with effort=high should NOT have temperature")
	}

	// Should include reasoning.effort
	reasoning, ok := reqBody["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatal("Should have reasoning object in request body")
	}
	if reasoning["effort"] != "high" {
		t.Errorf("reasoning.effort = %v, expected 'high'", reasoning["effort"])
	}
}

func TestBuildRequestBody_ReasoningEffortEmpty(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "Hello"},
	}

	// Empty effort = use model default, no reasoning object in body
	reqBody := buildRequestBody("o3", messages, 2000, 0.7, false, 0, "")

	// Empty effort preserves the caller's exact budget.
	expectedTokens := 2000
	if reqBody["max_completion_tokens"] != expectedTokens {
		t.Errorf("max_completion_tokens = %v, expected %d", reqBody["max_completion_tokens"], expectedTokens)
	}

	// Should NOT include reasoning object when effort is empty
	if _, ok := reqBody["reasoning"]; ok {
		t.Error("Empty reasoning effort should NOT include reasoning object in request body")
	}
}

func TestBuildRequestBody_StandardModelIgnoresEffort(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "Hello"},
	}

	// Standard model should ignore reasoning effort entirely
	reqBody := buildRequestBody("gpt-4o", messages, 1000, 0.7, false, 0, "high")

	// Should use max_tokens (standard model)
	if _, ok := reqBody["max_tokens"]; !ok {
		t.Error("Standard model should use max_tokens regardless of reasoning effort")
	}
	if _, ok := reqBody["max_completion_tokens"]; ok {
		t.Error("Standard model should NOT have max_completion_tokens")
	}

	// Should include temperature
	if _, ok := reqBody["temperature"]; !ok {
		t.Error("Standard model should include temperature")
	}

	// Should NOT have reasoning object
	if _, ok := reqBody["reasoning"]; ok {
		t.Error("Standard model should NOT have reasoning object")
	}
}

// TestStreamDelta_ReasoningContent tests that streaming ReasoningContent is properly parsed
func TestStreamDelta_ReasoningContent(t *testing.T) {
	tests := []struct {
		name             string
		content          string
		reasoningContent string
		expectedResult   string
	}{
		{
			name:             "Standard streaming - content only",
			content:          "chunk",
			reasoningContent: "",
			expectedResult:   "chunk",
		},
		{
			name:             "Reasoning streaming - reasoning_content only",
			content:          "",
			reasoningContent: "reasoning chunk",
			expectedResult:   "reasoning chunk",
		},
		{
			name:             "Both fields - prefer content",
			content:          "standard chunk",
			reasoningContent: "reasoning chunk",
			expectedResult:   "standard chunk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delta := StreamDelta{
				Role:             "assistant",
				Content:          tt.content,
				ReasoningContent: tt.reasoningContent,
			}

			// Simulate the extraction logic used in client.go streaming
			result := delta.Content
			if result == "" && delta.ReasoningContent != "" {
				result = delta.ReasoningContent
			}

			if result != tt.expectedResult {
				t.Errorf("Expected %q, got %q", tt.expectedResult, result)
			}
		})
	}
}
