package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// mockLogger implements core.Logger for testing
type mockLogger struct {
	logs   []string
	fields []map[string]interface{}
}

func (m *mockLogger) Debug(msg string, fields map[string]interface{}) {
	m.logs = append(m.logs, "DEBUG: "+msg)
	m.fields = append(m.fields, fields)
}

func (m *mockLogger) Info(msg string, fields map[string]interface{}) {
	m.logs = append(m.logs, "INFO: "+msg)
	m.fields = append(m.fields, fields)
}

func (m *mockLogger) Warn(msg string, fields map[string]interface{}) {
	m.logs = append(m.logs, "WARN: "+msg)
	m.fields = append(m.fields, fields)
}

func (m *mockLogger) Error(msg string, fields map[string]interface{}) {
	m.logs = append(m.logs, "ERROR: "+msg)
	m.fields = append(m.fields, fields)
}

func (m *mockLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.logs = append(m.logs, "DEBUG: "+msg)
	m.fields = append(m.fields, fields)
}

func (m *mockLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.logs = append(m.logs, "INFO: "+msg)
	m.fields = append(m.fields, fields)
}

func (m *mockLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.logs = append(m.logs, "WARN: "+msg)
	m.fields = append(m.fields, fields)
}

func (m *mockLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.logs = append(m.logs, "ERROR: "+msg)
	m.fields = append(m.fields, fields)
}

func TestNewClient(t *testing.T) {
	logger := &mockLogger{}

	tests := []struct {
		name    string
		apiKey  string
		baseURL string
		want    struct {
			apiKey  string
			baseURL string
		}
	}{
		{
			name:    "with custom base URL",
			apiKey:  "test-key",
			baseURL: "https://custom.api.com/v1",
			want: struct {
				apiKey  string
				baseURL string
			}{
				apiKey:  "test-key",
				baseURL: "https://custom.api.com/v1",
			},
		},
		{
			name:    "with default base URL",
			apiKey:  "test-key",
			baseURL: "",
			want: struct {
				apiKey  string
				baseURL string
			}{
				apiKey:  "test-key",
				baseURL: "https://api.openai.com/v1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.apiKey, tt.baseURL, "", logger)

			if client.apiKey != tt.want.apiKey {
				t.Errorf("apiKey = %q, want %q", client.apiKey, tt.want.apiKey)
			}
			if client.baseURL != tt.want.baseURL {
				t.Errorf("baseURL = %q, want %q", client.baseURL, tt.want.baseURL)
			}
			// DefaultModel is now "default" alias which gets resolved at request-time
			// This enables runtime model override via TRUVAG3_OPENAI_MODEL_DEFAULT env var
			if client.DefaultModel != "default" {
				t.Errorf("DefaultModel = %q, want \"default\" (alias)", client.DefaultModel)
			}
		})
	}
}

func TestClient_getProviderName(t *testing.T) {
	tests := []struct {
		name          string
		providerAlias string
		want          string
	}{
		{
			name:          "empty alias returns openai",
			providerAlias: "",
			want:          "openai",
		},
		{
			name:          "openai alias",
			providerAlias: "openai",
			want:          "openai",
		},
		{
			name:          "groq alias",
			providerAlias: "openai.groq",
			want:          "openai.groq",
		},
		{
			name:          "deepseek alias",
			providerAlias: "openai.deepseek",
			want:          "openai.deepseek",
		},
		{
			name:          "custom alias",
			providerAlias: "openai.custom",
			want:          "openai.custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient("test-key", "", tt.providerAlias, nil)
			got := client.getProviderName()
			if got != tt.want {
				t.Errorf("getProviderName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClient_GenerateResponse(t *testing.T) {
	tests := []struct {
		name           string
		apiKey         string
		prompt         string
		options        *core.AIOptions
		serverResponse string
		serverStatus   int
		wantError      bool
		wantContent    string
		validateReq    func(*testing.T, map[string]interface{})
	}{
		{
			name:   "successful response",
			apiKey: "test-key",
			prompt: "Hello, AI!",
			options: &core.AIOptions{
				Model:       "gpt-3.5-turbo",
				Temperature: 0.7,
				MaxTokens:   100,
			},
			serverResponse: `{
				"id": "chatcmpl-123",
				"object": "chat.completion",
				"created": 1677652288,
				"model": "gpt-3.5-turbo",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Hello! How can I help you today?"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 8,
					"total_tokens": 18
				}
			}`,
			serverStatus: http.StatusOK,
			wantError:    false,
			wantContent:  "Hello! How can I help you today?",
			validateReq: func(t *testing.T, req map[string]interface{}) {
				if req["model"] != "gpt-3.5-turbo" {
					t.Errorf("request model = %v, want gpt-3.5-turbo", req["model"])
				}
				if req["temperature"] != 0.7 {
					t.Errorf("request temperature = %v, want 0.7", req["temperature"])
				}
				if req["max_tokens"] != float64(100) {
					t.Errorf("request max_tokens = %v, want 100", req["max_tokens"])
				}
			},
		},
		{
			name:   "with system prompt",
			apiKey: "test-key",
			prompt: "What is 2+2?",
			options: &core.AIOptions{
				Model:        "gpt-3.5-turbo",
				SystemPrompt: "You are a helpful math tutor.",
				Temperature:  0.5,
				MaxTokens:    50,
			},
			serverResponse: `{
				"choices": [{
					"message": {
						"content": "2+2 equals 4."
					}
				}]
			}`,
			serverStatus: http.StatusOK,
			wantError:    false,
			wantContent:  "2+2 equals 4.",
			validateReq: func(t *testing.T, req map[string]interface{}) {
				messages := req["messages"].([]interface{})
				if len(messages) != 2 {
					t.Fatalf("expected 2 messages, got %d", len(messages))
				}

				// Check system message
				systemMsg := messages[0].(map[string]interface{})
				if systemMsg["role"] != "system" {
					t.Errorf("first message role = %v, want system", systemMsg["role"])
				}
				if systemMsg["content"] != "You are a helpful math tutor." {
					t.Errorf("system content = %v, want 'You are a helpful math tutor.'", systemMsg["content"])
				}

				// Check user message
				userMsg := messages[1].(map[string]interface{})
				if userMsg["role"] != "user" {
					t.Errorf("second message role = %v, want user", userMsg["role"])
				}
				if userMsg["content"] != "What is 2+2?" {
					t.Errorf("user content = %v, want 'What is 2+2?'", userMsg["content"])
				}
			},
		},
		{
			name:      "missing API key",
			apiKey:    "",
			prompt:    "Hello",
			options:   &core.AIOptions{Model: "gpt-3.5-turbo"},
			wantError: true,
		},
		{
			name:    "API error response",
			apiKey:  "test-key",
			prompt:  "Hello",
			options: &core.AIOptions{Model: "gpt-3.5-turbo"},
			serverResponse: `{
				"error": {
					"message": "Invalid API key",
					"type": "invalid_request_error",
					"code": "invalid_api_key"
				}
			}`,
			serverStatus: http.StatusUnauthorized,
			wantError:    true,
		},
		{
			name:           "malformed response",
			apiKey:         "test-key",
			prompt:         "Hello",
			options:        &core.AIOptions{Model: "gpt-3.5-turbo"},
			serverResponse: `{invalid json}`,
			serverStatus:   http.StatusOK,
			wantError:      true,
		},
		{
			name:           "empty choices array",
			apiKey:         "test-key",
			prompt:         "Hello",
			options:        &core.AIOptions{Model: "gpt-3.5-turbo"},
			serverResponse: `{"choices": []}`,
			serverStatus:   http.StatusOK,
			wantError:      true,
		},
		{
			name:    "with usage information",
			apiKey:  "test-key",
			prompt:  "Hello",
			options: &core.AIOptions{Model: "gpt-3.5-turbo"},
			serverResponse: `{
				"model": "gpt-3.5-turbo",
				"choices": [{
					"message": {"content": "Hi there!"},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 5,
					"completion_tokens": 3,
					"total_tokens": 8
				}
			}`,
			serverStatus: http.StatusOK,
			wantError:    false,
			wantContent:  "Hi there!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			var capturedRequest map[string]interface{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify headers
				if auth := r.Header.Get("Authorization"); auth != "Bearer "+tt.apiKey && tt.apiKey != "" {
					t.Errorf("Authorization header = %q, want %q", auth, "Bearer "+tt.apiKey)
				}
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type header = %q, want application/json", ct)
				}

				// Capture request body
				if r.Body != nil {
					body, _ := io.ReadAll(r.Body)
					json.Unmarshal(body, &capturedRequest)
				}

				// Send response
				w.WriteHeader(tt.serverStatus)
				w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			// Create client
			logger := &mockLogger{}
			client := NewClient(tt.apiKey, server.URL, "", logger)

			// Make request
			ctx := context.Background()
			resp, err := client.GenerateResponse(ctx, tt.prompt, tt.options)

			// Check error
			if (err != nil) != tt.wantError {
				t.Errorf("GenerateResponse() error = %v, wantError %v", err, tt.wantError)
			}

			// If successful, check response
			if !tt.wantError && resp != nil {
				if resp.Content != tt.wantContent {
					t.Errorf("response content = %q, want %q", resp.Content, tt.wantContent)
				}
			}

			// Validate request if provided
			if tt.validateReq != nil && capturedRequest != nil {
				tt.validateReq(t, capturedRequest)
			}
		})
	}
}

func TestClient_GenerateResponse_OllamaDoesNotRequireAPIKey(t *testing.T) {
	var capturedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "hello from ollama"
				},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 1,
				"completion_tokens": 1,
				"total_tokens": 2
			}
		}`)
	}))
	defer server.Close()

	client := NewClient("", server.URL, "openai.ollama", &mockLogger{})

	resp, err := client.GenerateResponse(context.Background(), "Hello", &core.AIOptions{
		Model: "default",
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}
	if resp.Content != "hello from ollama" {
		t.Fatalf("Content = %q, want %q", resp.Content, "hello from ollama")
	}
	if capturedAuth != "" {
		t.Fatalf("Authorization header = %q, want empty for ollama", capturedAuth)
	}
}

func TestClient_GenerateResponseWithDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)

		// Verify defaults were applied
		if req["model"] != "gpt-3.5-turbo" {
			t.Errorf("model = %v, want gpt-3.5-turbo (default)", req["model"])
		}
		if req["temperature"] != 0.7 {
			t.Errorf("temperature = %v, want 0.7 (default)", req["temperature"])
		}
		if req["max_tokens"] != float64(1000) {
			t.Errorf("max_tokens = %v, want 1000 (default)", req["max_tokens"])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices": [{"message": {"content": "response"}}]}`))
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)

	// Set defaults
	client.DefaultModel = "gpt-3.5-turbo"
	client.DefaultTemperature = 0.7
	client.DefaultMaxTokens = 1000

	// Call with nil options to use defaults
	_, err := client.GenerateResponse(context.Background(), "test", nil)
	if err != nil {
		t.Errorf("GenerateResponse() with defaults failed: %v", err)
	}
}

func TestClient_GenerateResponseContextCancellation(t *testing.T) {
	// Server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices": [{"message": {"content": "too late"}}]}`))
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)

	// Create context with immediate cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := client.GenerateResponse(ctx, "test", &core.AIOptions{Model: "gpt-3.5-turbo"})
	if err == nil {
		t.Error("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context canceled error, got: %v", err)
	}
}

func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "shorter than max",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "equal to max",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "longer than max",
			input:  "hello world",
			maxLen: 5,
			want:   "hello...",
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 5,
			want:   "",
		},
		{
			name:   "max is zero",
			input:  "hello",
			maxLen: 0,
			want:   "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForLog(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForLog(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestClient_SupportsStreaming(t *testing.T) {
	client := NewClient("test-key", "", "", nil)
	if !client.SupportsStreaming() {
		t.Error("OpenAI client should support streaming")
	}
}

func TestClient_GenerateResponse_ReasoningContent(t *testing.T) {
	// Test that content is extracted from reasoning_content field when content is empty
	// This is used by reasoning models like GPT-5, o1, o3, o4
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"model": "gpt-5-mini",
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "",
					"reasoning_content": "This is the reasoning response from GPT-5"
				},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 10,
				"completion_tokens": 50,
				"total_tokens": 60
			}
		}`))
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)

	resp, err := client.GenerateResponse(context.Background(), "test", &core.AIOptions{
		Model:     "gpt-5-mini",
		MaxTokens: 1000,
	})

	if err != nil {
		t.Fatalf("GenerateResponse() error = %v", err)
	}

	if resp.Content != "This is the reasoning response from GPT-5" {
		t.Errorf("Content = %q, want reasoning_content value", resp.Content)
	}
}

func TestClient_GenerateResponse_ReasoningModelParams(t *testing.T) {
	// Test that reasoning model parameters are correctly applied
	var capturedRequest map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedRequest)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [{
				"message": {"content": "reasoning response"}
			}]
		}`))
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)

	_, err := client.GenerateResponse(context.Background(), "test", &core.AIOptions{
		Model:       "o3-mini",
		Temperature: 0.7, // Should be ignored for reasoning models
		MaxTokens:   1000,
	})

	if err != nil {
		t.Fatalf("GenerateResponse() error = %v", err)
	}

	// Verify max_completion_tokens is used (not max_tokens)
	if _, ok := capturedRequest["max_completion_tokens"]; !ok {
		t.Error("Reasoning model should use max_completion_tokens")
	}
	if _, ok := capturedRequest["max_tokens"]; ok {
		t.Error("Reasoning model should NOT have max_tokens")
	}
	// Verify temperature is NOT included for reasoning models
	if _, ok := capturedRequest["temperature"]; ok {
		t.Error("Reasoning model should NOT have temperature")
	}
	// Verify multiplier was applied (1000 * 5 = 5000)
	if capturedRequest["max_completion_tokens"] != float64(5000) {
		t.Errorf("max_completion_tokens = %v, want 5000 (1000 * 5 default multiplier)", capturedRequest["max_completion_tokens"])
	}
}

func TestClient_GenerateResponse_CustomReasoningMultiplier(t *testing.T) {
	var capturedRequest map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedRequest)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [{
				"message": {"content": "response"}
			}]
		}`))
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)
	client.ReasoningTokenMultiplier = 3 // Custom multiplier

	_, err := client.GenerateResponse(context.Background(), "test", &core.AIOptions{
		Model:     "gpt-5-mini",
		MaxTokens: 1000,
	})

	if err != nil {
		t.Fatalf("GenerateResponse() error = %v", err)
	}

	// Verify custom multiplier was applied (1000 * 3 = 3000)
	if capturedRequest["max_completion_tokens"] != float64(3000) {
		t.Errorf("max_completion_tokens = %v, want 3000 (1000 * 3 custom multiplier)", capturedRequest["max_completion_tokens"])
	}
}

func TestClient_ResponseParsing(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		wantError   bool
		wantContent string
		wantModel   string
		wantTokens  int
	}{
		{
			name: "complete response with all fields",
			response: `{
				"id": "chatcmpl-123",
				"model": "gpt-4",
				"choices": [{
					"message": {"content": "Complete response"},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 5,
					"total_tokens": 15
				}
			}`,
			wantContent: "Complete response",
			wantModel:   "gpt-4",
			wantTokens:  15,
		},
		{
			name: "minimal valid response",
			response: `{
				"choices": [{
					"message": {"content": "Minimal"}
				}]
			}`,
			wantContent: "Minimal",
			wantModel:   "",
			wantTokens:  0,
		},
		{
			name: "error response from API",
			response: `{
				"error": {
					"message": "Invalid request",
					"type": "invalid_request_error"
				}
			}`,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(tt.response, "error") {
					// Use 400 Bad Request (non-retryable) for unit tests
					// Retry behavior with 429 is tested in integration tests
					w.WriteHeader(http.StatusBadRequest)
				} else {
					w.WriteHeader(http.StatusOK)
				}
				w.Write([]byte(tt.response))
			}))
			defer server.Close()

			logger := &mockLogger{}
			client := NewClient("test-key", server.URL, "", logger)

			resp, err := client.GenerateResponse(
				context.Background(),
				"test",
				&core.AIOptions{Model: "gpt-3.5-turbo"},
			)

			if (err != nil) != tt.wantError {
				t.Errorf("error = %v, wantError %v", err, tt.wantError)
			}

			if !tt.wantError && resp != nil {
				if resp.Content != tt.wantContent {
					t.Errorf("content = %q, want %q", resp.Content, tt.wantContent)
				}
				if resp.Model != tt.wantModel {
					t.Errorf("model = %q, want %q", resp.Model, tt.wantModel)
				}
				if resp.Usage.TotalTokens != tt.wantTokens {
					t.Errorf("tokens = %d, want %d", resp.Usage.TotalTokens, tt.wantTokens)
				}
			}
		})
	}
}

// =============================================================================
// StreamResponse Tests
// =============================================================================

func TestClient_StreamResponse_Success(t *testing.T) {
	// Create SSE server that sends streaming response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify streaming headers
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Error("Expected Accept: text/event-stream header")
		}

		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		// Flush the headers
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		// Send streaming chunks
		chunks := []string{
			`data: {"id":"1","model":"gpt-4","choices":[{"delta":{"role":"assistant"}}]}`,
			`data: {"id":"1","model":"gpt-4","choices":[{"delta":{"content":"Hello"}}]}`,
			`data: {"id":"1","model":"gpt-4","choices":[{"delta":{"content":" world"}}]}`,
			`data: {"id":"1","model":"gpt-4","choices":[{"delta":{"content":"!"},"finish_reason":"stop"}]}`,
			`data: {"id":"1","model":"gpt-4","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
			`data: [DONE]`,
		}

		for _, chunk := range chunks {
			w.Write([]byte(chunk + "\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)

	var receivedChunks []string
	callback := func(chunk core.StreamChunk) error {
		if chunk.Content != "" {
			receivedChunks = append(receivedChunks, chunk.Content)
		}
		return nil
	}

	resp, err := client.StreamResponse(context.Background(), "test", &core.AIOptions{
		Model:     "gpt-4",
		MaxTokens: 100,
	}, callback)

	if err != nil {
		t.Fatalf("StreamResponse() error = %v", err)
	}

	// Verify full content
	expectedContent := "Hello world!"
	if resp.Content != expectedContent {
		t.Errorf("Content = %q, want %q", resp.Content, expectedContent)
	}

	// Verify chunks received
	if len(receivedChunks) != 3 {
		t.Errorf("Received %d chunks, want 3", len(receivedChunks))
	}

	// Verify model
	if resp.Model != "gpt-4" {
		t.Errorf("Model = %q, want gpt-4", resp.Model)
	}

	// Verify provider
	if resp.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", resp.Provider)
	}
}

func TestClient_StreamResponse_MissingAPIKey(t *testing.T) {
	logger := &mockLogger{}
	client := NewClient("", "", "", logger) // No API key

	callback := func(chunk core.StreamChunk) error {
		return nil
	}

	_, err := client.StreamResponse(context.Background(), "test", &core.AIOptions{
		Model: "gpt-4",
	}, callback)

	if err == nil {
		t.Error("Expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "API key not configured") {
		t.Errorf("Expected API key error, got: %v", err)
	}
}

func TestClient_StreamResponse_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": {"message": "Invalid API key"}}`))
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)

	callback := func(chunk core.StreamChunk) error {
		return nil
	}

	_, err := client.StreamResponse(context.Background(), "test", &core.AIOptions{
		Model: "gpt-4",
	}, callback)

	if err == nil {
		t.Error("Expected error for API error response")
	}
}

func TestClient_StreamResponse_ReasoningModel(t *testing.T) {
	// Test streaming with reasoning model (GPT-5, o1, o3, o4) which uses reasoning_content
	var capturedRequest map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedRequest)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Streaming response with reasoning_content (used by o1/o3/gpt-5)
		chunks := []string{
			`data: {"id":"1","model":"o3-mini","choices":[{"delta":{"role":"assistant"}}]}`,
			`data: {"id":"1","model":"o3-mini","choices":[{"delta":{"reasoning_content":"Thinking..."}}]}`,
			`data: {"id":"1","model":"o3-mini","choices":[{"delta":{"content":"The answer is 42"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}

		for _, chunk := range chunks {
			w.Write([]byte(chunk + "\n\n"))
		}
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)

	var chunks []string
	callback := func(chunk core.StreamChunk) error {
		if chunk.Content != "" {
			chunks = append(chunks, chunk.Content)
		}
		return nil
	}

	resp, err := client.StreamResponse(context.Background(), "test", &core.AIOptions{
		Model:     "o3-mini",
		MaxTokens: 1000,
	}, callback)

	if err != nil {
		t.Fatalf("StreamResponse() error = %v", err)
	}

	// Verify max_completion_tokens is used for reasoning model
	if _, ok := capturedRequest["max_completion_tokens"]; !ok {
		t.Error("Reasoning model should use max_completion_tokens")
	}

	// Verify temperature is NOT included
	if _, ok := capturedRequest["temperature"]; ok {
		t.Error("Reasoning model should NOT have temperature")
	}

	// Both reasoning_content and content should be captured
	if len(chunks) < 2 {
		t.Errorf("Expected at least 2 chunks (reasoning + content), got %d", len(chunks))
	}

	// Final response should contain all content
	if resp.Content == "" {
		t.Error("Expected non-empty response content")
	}
}

func TestClient_StreamResponse_WithSystemPrompt(t *testing.T) {
	var capturedRequest map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedRequest)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		w.Write([]byte(`data: {"id":"1","model":"gpt-4","choices":[{"delta":{"content":"Hi"}}]}` + "\n\n"))
		w.Write([]byte(`data: [DONE]` + "\n\n"))
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)

	callback := func(chunk core.StreamChunk) error { return nil }

	_, err := client.StreamResponse(context.Background(), "test prompt", &core.AIOptions{
		Model:        "gpt-4",
		SystemPrompt: "You are a helpful assistant.",
	}, callback)

	if err != nil {
		t.Fatalf("StreamResponse() error = %v", err)
	}

	// Verify system message was included
	messages := capturedRequest["messages"].([]interface{})
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages (system + user), got %d", len(messages))
	}

	systemMsg := messages[0].(map[string]interface{})
	if systemMsg["role"] != "system" {
		t.Errorf("First message role = %v, want system", systemMsg["role"])
	}
	if systemMsg["content"] != "You are a helpful assistant." {
		t.Errorf("System content = %v, want 'You are a helpful assistant.'", systemMsg["content"])
	}
}

func TestClient_StreamResponse_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send first chunk
		w.Write([]byte(`data: {"id":"1","model":"gpt-4","choices":[{"delta":{"content":"Hello"}}]}` + "\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		// Wait longer than test will wait
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()

	logger := &mockLogger{}
	client := NewClient("test-key", server.URL, "", logger)

	ctx, cancel := context.WithCancel(context.Background())

	var chunksReceived int
	callback := func(chunk core.StreamChunk) error {
		chunksReceived++
		// Cancel after receiving first chunk
		cancel()
		return nil
	}

	resp, err := client.StreamResponse(ctx, "test", &core.AIOptions{
		Model: "gpt-4",
	}, callback)

	// Should return partial result with ErrStreamPartiallyCompleted or context error
	if resp != nil && resp.Content == "" && err == nil {
		t.Error("Expected either partial content or error on cancellation")
	}
}
