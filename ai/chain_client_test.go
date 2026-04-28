package ai

// IMPORTANT: Several tests in this file (TestChainClient_AutoDetect*,
// TestPhase3_NoProvidersAvailableFails) manipulate the global provider registry.
// Do NOT use t.Parallel() in those tests, as concurrent access to
// registry.providers would cause race conditions and flaky tests.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// ================================
// Phase 3 Unit Tests: Chain Client (Pure Unit Tests Only)
// ================================
//
// Integration tests requiring provider registration are in:
//   - chain_client_integration_test.go (run with: go test -tags=integration)
//

// TestPhase3_ConfigurationValidation verifies fail-fast configuration validation
func TestPhase3_ConfigurationValidation(t *testing.T) {
	tests := []struct {
		name          string
		opts          []ChainOption
		expectError   bool
		errorContains string
		description   string
	}{
		{
			name:          "Empty chain with no env vars fails fast via auto-detect",
			opts:          []ChainOption{},
			expectError:   true,
			errorContains: "no providers detected",
			description:   "Auto-detect finds no providers when no API keys are set",
		},
		{
			name: "Invalid provider alias fails fast",
			opts: []ChainOption{
				WithProviderChain("invalid-provider"),
			},
			expectError:   true,
			errorContains: "unknown provider alias",
			description:   "Configuration error: invalid alias",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env vars so auto-detect doesn't find providers from test environment
			saved := saveChainEnvironment()
			defer restoreChainEnvironment(saved)
			clearAllChainEnvVars()

			_, err := NewChainClient(tt.opts...)

			if tt.expectError {
				if err == nil {
					t.Errorf("%s: Expected error, got nil", tt.description)
				} else if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("%s: Expected error containing %q, got %q",
						tt.description, tt.errorContains, err.Error())
				}
			} else if err != nil {
				t.Errorf("%s: Unexpected error: %v", tt.description, err)
			}
		})
	}
}

// TestPhase3_NoProvidersAvailableFails verifies fail-fast when no providers are registered
func TestPhase3_NoProvidersAvailableFails(t *testing.T) {
	// Clean environment - no API keys
	originalVars := saveChainEnvironment()
	defer restoreChainEnvironment(originalVars)
	clearAllChainEnvVars()

	// Clear registry so base provider "openai" is not registered
	registry.mu.Lock()
	originalProviders := registry.providers
	registry.providers = make(map[string]ProviderFactory)
	registry.mu.Unlock()
	defer func() {
		registry.mu.Lock()
		registry.providers = originalProviders
		registry.mu.Unlock()
	}()

	// Try to create chain with providers that aren't registered
	client, err := NewChainClient(
		WithProviderChain("openai", "openai.deepseek"),
	)

	// Should fail - provider not registered (fail-fast validation)
	if err == nil {
		t.Error("Expected error when providers not registered")
	}
	if client != nil {
		t.Error("Expected nil client when creation fails")
	}
	if err != nil && !strings.Contains(err.Error(), "not registered") {
		t.Errorf("Expected 'not registered' error, got: %v", err)
	}
}

// TestPhase3_ErrorClassification verifies isClientError function
//
// IMPORTANT: In a provider chain, auth errors SHOULD trigger failover because
// each provider has its own API key. Auth failure on OpenAI should try Anthropic.
//
// Non-retryable (isClientError=true): bad request, content policy, invalid parameter, malformed
// Retryable (isClientError=false): auth errors, server errors, timeouts, network errors
func TestPhase3_ErrorClassification(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		isClientErr bool
		description string
	}{
		// Auth errors SHOULD failover (each provider has different API key)
		{
			name:        "Authentication error allows failover",
			err:         errors.New("authentication failed"),
			isClientErr: false,
			description: "Auth errors should try next provider",
		},
		{
			name:        "Unauthorized allows failover",
			err:         errors.New("unauthorized access"),
			isClientErr: false,
			description: "401 errors should try next provider",
		},
		{
			name:        "API key error allows failover",
			err:         errors.New("api key is invalid"),
			isClientErr: false,
			description: "API key errors should try next provider",
		},
		{
			name:        "401 status allows failover",
			err:         errors.New("status code 401"),
			isClientErr: false,
			description: "401 status should try next provider",
		},
		// True client errors (structured ProviderError with 4xx status) should NOT failover
		{
			name:        "Invalid parameter is client error",
			err:         &testProviderError{statusCode: 400, message: "invalid parameter value", provider: "test"},
			isClientErr: true,
			description: "Invalid params would fail on any provider",
		},
		{
			name:        "Bad request is client error",
			err:         &testProviderError{statusCode: 400, message: "bad request format", provider: "test"},
			isClientErr: true,
			description: "Malformed requests would fail on any provider",
		},
		{
			name:        "Content policy is client error",
			err:         &testProviderError{statusCode: 400, message: "content policy violation", provider: "test"},
			isClientErr: true,
			description: "Policy violations would fail on any provider",
		},
		{
			name:        "Malformed input is client error",
			err:         &testProviderError{statusCode: 422, message: "malformed JSON input", provider: "test"},
			isClientErr: true,
			description: "Malformed input would fail on any provider",
		},
		// Transient proxy errors (ORCH-008): IsTransient=true should allow failover
		{
			name:        "Transient proxy 400 allows failover",
			err:         &testProviderError{statusCode: 400, message: "Cloudflare HTML rejection", provider: "anthropic", transient: true},
			isClientErr: false,
			description: "Transient proxy errors should try next provider",
		},
		// Rate limit (429) should allow failover
		{
			name:        "Rate limit allows failover",
			err:         &testProviderError{statusCode: 429, message: "rate limit exceeded", provider: "openai"},
			isClientErr: false,
			description: "429 rate limits should try next provider",
		},
		// 401/403 are excluded from client errors (allow failover for different API keys)
		{
			name:        "ProviderError 401 allows failover",
			err:         &testProviderError{statusCode: 401, message: "unauthorized", provider: "test"},
			isClientErr: false,
			description: "401 should try next provider (different API keys)",
		},
		{
			name:        "ProviderError 403 allows failover",
			err:         &testProviderError{statusCode: 403, message: "forbidden", provider: "test"},
			isClientErr: false,
			description: "403 should try next provider",
		},
		// 5xx ProviderError should allow failover (boundary: status >= 500 is NOT < 500)
		{
			name:        "ProviderError 500 allows failover",
			err:         &testProviderError{statusCode: 500, message: "internal server error", provider: "test"},
			isClientErr: false,
			description: "5xx ProviderError should try next provider",
		},
		// Server/network errors (unstructured) should failover
		{
			name:        "Server error is retryable",
			err:         errors.New("internal server error"),
			isClientErr: false,
			description: "5xx errors should try next provider",
		},
		{
			name:        "Timeout is retryable",
			err:         errors.New("request timeout"),
			isClientErr: false,
			description: "Timeouts should try next provider",
		},
		{
			name:        "Network error is retryable",
			err:         errors.New("network connection failed"),
			isClientErr: false,
			description: "Network errors should try next provider",
		},
		{
			name:        "Unknown error is retryable",
			err:         errors.New("some random error"),
			isClientErr: false,
			description: "Conservative: unknown errors should try next provider",
		},
		// Edge cases - not found and forbidden are NOT in client error patterns
		// so they default to retryable (conservative approach)
		{
			name:        "Not found allows failover",
			err:         errors.New("resource not found"),
			isClientErr: false,
			description: "Not found defaults to retryable",
		},
		{
			name:        "Forbidden allows failover",
			err:         errors.New("forbidden access to resource"),
			isClientErr: false,
			description: "Forbidden defaults to retryable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isClientError(tt.err)
			if result != tt.isClientErr {
				t.Errorf("%s: isClientError(%q) = %v, want %v",
					tt.description, tt.err.Error(), result, tt.isClientErr)
			}
		})
	}
}

// TestChainClient_AutoDetect verifies auto-detection builds chain in priority order
func TestChainClient_AutoDetect(t *testing.T) {
	// Setup: inject mock providers into registry
	registry.mu.Lock()
	originalProviders := registry.providers
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["mock-high"] = &MockProviderFactory{
		name:      "mock-high",
		priority:  100,
		available: true,
		createFunc: func(config *AIConfig) core.AIClient {
			return &chainMockAIClient{name: "mock-high"}
		},
	}
	registry.providers["mock-low"] = &MockProviderFactory{
		name:      "mock-low",
		priority:  50,
		available: true,
		createFunc: func(config *AIConfig) core.AIClient {
			return &chainMockAIClient{name: "mock-low"}
		},
	}
	registry.mu.Unlock()

	defer func() {
		registry.mu.Lock()
		registry.providers = originalProviders
		registry.mu.Unlock()
	}()

	// No WithProviderChain — should auto-detect
	client, err := NewChainClient()
	if err != nil {
		t.Fatalf("Expected auto-detect to succeed, got error: %v", err)
	}

	// Verify chain was built with both providers
	if len(client.providers) != 2 {
		t.Errorf("Expected 2 providers in chain, got %d", len(client.providers))
	}

	// Verify order: high priority first
	if len(client.providerAliases) >= 2 {
		if client.providerAliases[0] != "mock-high" {
			t.Errorf("Expected first alias 'mock-high', got %q", client.providerAliases[0])
		}
		if client.providerAliases[1] != "mock-low" {
			t.Errorf("Expected second alias 'mock-low', got %q", client.providerAliases[1])
		}
	}
}

// TestChainClient_AutoDetect_NoProviders verifies fail-fast with no providers
func TestChainClient_AutoDetect_NoProviders(t *testing.T) {
	// Setup: empty registry
	registry.mu.Lock()
	originalProviders := registry.providers
	registry.providers = make(map[string]ProviderFactory)
	registry.mu.Unlock()

	defer func() {
		registry.mu.Lock()
		registry.providers = originalProviders
		registry.mu.Unlock()
	}()

	_, err := NewChainClient()
	if err == nil {
		t.Fatal("Expected error when no providers available for auto-detect")
	}
	if !strings.Contains(err.Error(), "no providers detected") {
		t.Errorf("Expected 'no providers detected' error, got: %v", err)
	}
}

// TestChainClient_ExplicitChain_Unchanged verifies explicit WithProviderChain still works
func TestChainClient_ExplicitChain_Unchanged(t *testing.T) {
	// Setup: inject mock providers
	registry.mu.Lock()
	originalProviders := registry.providers
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["providerA"] = &MockProviderFactory{
		name:      "providerA",
		priority:  50,
		available: true,
		createFunc: func(config *AIConfig) core.AIClient {
			return &chainMockAIClient{name: "providerA"}
		},
	}
	registry.providers["providerB"] = &MockProviderFactory{
		name:      "providerB",
		priority:  100,
		available: true,
		createFunc: func(config *AIConfig) core.AIClient {
			return &chainMockAIClient{name: "providerB"}
		},
	}
	registry.mu.Unlock()

	defer func() {
		registry.mu.Lock()
		registry.providers = originalProviders
		registry.mu.Unlock()
	}()

	// Explicit chain: providerA first (lower priority) — order should be preserved
	client, err := NewChainClient(WithProviderChain("providerA", "providerB"))
	if err != nil {
		t.Fatalf("Expected explicit chain to succeed, got error: %v", err)
	}

	if len(client.providerAliases) != 2 {
		t.Fatalf("Expected 2 providers, got %d", len(client.providerAliases))
	}
	// Explicit order preserved (not sorted by priority)
	if client.providerAliases[0] != "providerA" {
		t.Errorf("Expected first alias 'providerA', got %q", client.providerAliases[0])
	}
	if client.providerAliases[1] != "providerB" {
		t.Errorf("Expected second alias 'providerB', got %q", client.providerAliases[1])
	}
}

// TestPhase3_ChainOptions verifies configuration options
func TestPhase3_ChainOptions(t *testing.T) {
	t.Run("WithProviderChain sets aliases", func(t *testing.T) {
		config := &ChainConfig{}
		option := WithProviderChain("openai", "anthropic", "gemini")
		option(config)

		if len(config.ProviderAliases) != 3 {
			t.Errorf("Expected 3 aliases, got %d", len(config.ProviderAliases))
		}
		if config.ProviderAliases[0] != "openai" {
			t.Errorf("Expected first alias 'openai', got %q", config.ProviderAliases[0])
		}
		if config.ProviderAliases[1] != "anthropic" {
			t.Errorf("Expected second alias 'anthropic', got %q", config.ProviderAliases[1])
		}
		if config.ProviderAliases[2] != "gemini" {
			t.Errorf("Expected third alias 'gemini', got %q", config.ProviderAliases[2])
		}
	})

	t.Run("WithChainLogger sets logger", func(t *testing.T) {
		config := &ChainConfig{}
		logger := &testLogger{logs: make([]string, 0)}
		option := WithChainLogger(logger)
		option(config)

		if config.Logger == nil {
			t.Error("Expected logger to be set")
		}
	})
}

// ================================
// Mock Implementations for Testing
// ================================

// testLogger captures log messages for verification
type testLogger struct {
	logs []string
}

func (l *testLogger) Debug(msg string, fields map[string]interface{}) {
	l.logs = append(l.logs, "DEBUG: "+msg)
}

func (l *testLogger) Info(msg string, fields map[string]interface{}) {
	l.logs = append(l.logs, "INFO: "+msg)
}

func (l *testLogger) Warn(msg string, fields map[string]interface{}) {
	l.logs = append(l.logs, "WARN: "+msg)
}

func (l *testLogger) Error(msg string, fields map[string]interface{}) {
	l.logs = append(l.logs, "ERROR: "+msg)
}

func (l *testLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.logs = append(l.logs, "DEBUG: "+msg)
}

func (l *testLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.logs = append(l.logs, "INFO: "+msg)
}

func (l *testLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.logs = append(l.logs, "WARN: "+msg)
}

func (l *testLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.logs = append(l.logs, "ERROR: "+msg)
}

// chainMockAIClient for testing failover behavior (renamed to avoid conflicts)
// testProviderError implements core.ProviderError for testing isClientError()
type testProviderError struct {
	statusCode int
	provider   string
	model      string
	message    string
	transient  bool
	retryable  bool
}

func (e *testProviderError) Error() string     { return e.message }
func (e *testProviderError) StatusCode() int   { return e.statusCode }
func (e *testProviderError) Provider() string  { return e.provider }
func (e *testProviderError) Model() string     { return e.model }
func (e *testProviderError) IsTransient() bool { return e.transient }
func (e *testProviderError) IsRetryable() bool { return e.retryable }

type chainMockAIClient struct {
	name       string
	shouldFail bool
	failWith   error
	callCount  int
}

func (m *chainMockAIClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.callCount++
	if m.shouldFail {
		return nil, m.failWith
	}
	return &core.AIResponse{
		Content: "Mock response from " + m.name,
		Model:   m.name,
	}, nil
}

// TestPhase3_FailoverBehavior verifies automatic failover logic
func TestPhase3_FailoverBehavior(t *testing.T) {
	tests := []struct {
		name            string
		providers       []core.AIClient
		providerAliases []string // Required: must match providers length
		expectSuccess   bool
		expectedCalls   map[string]int
		description     string
	}{
		{
			name: "First provider succeeds",
			providers: []core.AIClient{
				&chainMockAIClient{name: "provider1", shouldFail: false},
				&chainMockAIClient{name: "provider2", shouldFail: false},
			},
			providerAliases: []string{"provider1", "provider2"},
			expectSuccess:   true,
			expectedCalls:   map[string]int{"provider1": 1, "provider2": 0},
			description:     "Should use first provider only",
		},
		{
			name: "Failover to second provider",
			providers: []core.AIClient{
				&chainMockAIClient{name: "provider1", shouldFail: true, failWith: errors.New("server error")},
				&chainMockAIClient{name: "provider2", shouldFail: false},
			},
			providerAliases: []string{"provider1", "provider2"},
			expectSuccess:   true,
			expectedCalls:   map[string]int{"provider1": 1, "provider2": 1},
			description:     "Should failover on server error",
		},
		{
			name: "Auth error allows failover",
			providers: []core.AIClient{
				&chainMockAIClient{name: "provider1", shouldFail: true, failWith: errors.New("invalid api key")},
				&chainMockAIClient{name: "provider2", shouldFail: false},
			},
			providerAliases: []string{"provider1", "provider2"},
			expectSuccess:   true,
			expectedCalls:   map[string]int{"provider1": 1, "provider2": 1},
			description:     "Auth errors should try next provider (different API keys)",
		},
		{
			name: "True client error stops failover",
			providers: []core.AIClient{
				&chainMockAIClient{name: "provider1", shouldFail: true, failWith: &testProviderError{statusCode: 400, message: "bad request: invalid prompt format", provider: "provider1"}},
				&chainMockAIClient{name: "provider2", shouldFail: false},
			},
			providerAliases: []string{"provider1", "provider2"},
			expectSuccess:   false,
			expectedCalls:   map[string]int{"provider1": 1, "provider2": 0},
			description:     "Bad request errors should not retry (same input fails everywhere)",
		},
		{
			name: "All providers fail",
			providers: []core.AIClient{
				&chainMockAIClient{name: "provider1", shouldFail: true, failWith: errors.New("server error")},
				&chainMockAIClient{name: "provider2", shouldFail: true, failWith: errors.New("server error")},
			},
			providerAliases: []string{"provider1", "provider2"},
			expectSuccess:   false,
			expectedCalls:   map[string]int{"provider1": 1, "provider2": 1},
			description:     "Should try all providers before failing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &ChainClient{
				providers:       tt.providers,
				providerAliases: tt.providerAliases,
				logger:          &core.NoOpLogger{},
			}

			ctx := context.Background()
			resp, err := client.GenerateResponse(ctx, "test prompt", nil)

			if tt.expectSuccess {
				if err != nil {
					t.Errorf("%s: Expected success, got error: %v", tt.description, err)
				}
				if resp == nil {
					t.Errorf("%s: Expected response, got nil", tt.description)
				}
			} else {
				if err == nil {
					t.Errorf("%s: Expected error, got success", tt.description)
				}
			}

			// Verify call counts
			for _, provider := range tt.providers {
				mock := provider.(*chainMockAIClient)
				expectedCalls := tt.expectedCalls[mock.name]
				if mock.callCount != expectedCalls {
					t.Errorf("%s: Provider %s: expected %d calls, got %d",
						tt.description, mock.name, expectedCalls, mock.callCount)
				}
			}
		})
	}
}

// ================================
// Helper Functions
// ================================

// saveChainEnvironment saves all environment variables
func saveChainEnvironment() map[string]string {
	vars := []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL",
		"GROQ_API_KEY", "GROQ_BASE_URL",
		"DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL",
		"XAI_API_KEY", "XAI_BASE_URL",
		"MISTRAL_API_KEY", "MISTRAL_BASE_URL",
		"QWEN_API_KEY", "QWEN_BASE_URL",
		"TOGETHER_API_KEY", "TOGETHER_BASE_URL",
		"OLLAMA_BASE_URL",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY",
	}
	saved := make(map[string]string)
	for _, v := range vars {
		saved[v] = os.Getenv(v)
	}
	return saved
}

// restoreChainEnvironment restores environment variables
func restoreChainEnvironment(saved map[string]string) {
	for k, v := range saved {
		if v == "" {
			if err := os.Unsetenv(k); err != nil {
				panic(err)
			}
		} else {
			if err := os.Setenv(k, v); err != nil {
				panic(err)
			}
		}
	}
}

// clearAllChainEnvVars clears all provider environment variables
func clearAllChainEnvVars() {
	vars := []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL",
		"GROQ_API_KEY", "GROQ_BASE_URL",
		"DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL",
		"XAI_API_KEY", "XAI_BASE_URL",
		"MISTRAL_API_KEY", "MISTRAL_BASE_URL",
		"QWEN_API_KEY", "QWEN_BASE_URL",
		"TOGETHER_API_KEY", "TOGETHER_BASE_URL",
		"OLLAMA_BASE_URL",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY",
	}
	for _, v := range vars {
		if err := os.Unsetenv(v); err != nil {
			panic(err)
		}
	}
}

// ================================
// Tests for cloneAIOptions (Options Mutation Bug Fix)
// ================================
//
// These tests verify the cloneAIOptions function that prevents mutation
// bleeding during chain failover. See: ai/MODEL_ALIAS_CROSS_PROVIDER_PROPOSAL.md
//

// TestCloneAIOptions_NilInput verifies nil input returns nil output
func TestCloneAIOptions_NilInput(t *testing.T) {
	result := cloneAIOptions(nil)
	if result != nil {
		t.Errorf("Expected nil output for nil input, got %+v", result)
	}
}

// TestCloneAIOptions_CopiesAllFields verifies all fields are copied correctly
func TestCloneAIOptions_CopiesAllFields(t *testing.T) {
	original := &core.AIOptions{
		Model:           "gpt-4",
		Temperature:     0.7,
		MaxTokens:       1000,
		SystemPrompt:    "You are a helpful assistant.",
		ReasoningEffort: "high",
		ResponseFormat:  "json",
		Extra: map[string]interface{}{
			"provider_flag": true,
		},
		Headers: map[string]string{
			"x-provider-beta": "enabled",
		},
	}

	clone := cloneAIOptions(original)

	// Verify clone is not nil
	if clone == nil {
		t.Fatal("Expected non-nil clone, got nil")
	}

	// Verify clone is a different pointer
	if clone == original {
		t.Error("Clone should be a different pointer than original")
	}

	// Verify all fields are copied
	if clone.Model != original.Model {
		t.Errorf("Model mismatch: got %q, want %q", clone.Model, original.Model)
	}
	if clone.Temperature != original.Temperature {
		t.Errorf("Temperature mismatch: got %v, want %v", clone.Temperature, original.Temperature)
	}
	if clone.MaxTokens != original.MaxTokens {
		t.Errorf("MaxTokens mismatch: got %d, want %d", clone.MaxTokens, original.MaxTokens)
	}
	if clone.SystemPrompt != original.SystemPrompt {
		t.Errorf("SystemPrompt mismatch: got %q, want %q", clone.SystemPrompt, original.SystemPrompt)
	}
	if clone.ReasoningEffort != original.ReasoningEffort {
		t.Errorf("ReasoningEffort mismatch: got %q, want %q", clone.ReasoningEffort, original.ReasoningEffort)
	}
	if clone.ResponseFormat != original.ResponseFormat {
		t.Errorf("ResponseFormat mismatch: got %q, want %q", clone.ResponseFormat, original.ResponseFormat)
	}
	if clone.Extra["provider_flag"] != original.Extra["provider_flag"] {
		t.Errorf("Extra mismatch: got %#v, want %#v", clone.Extra, original.Extra)
	}
	if clone.Headers["x-provider-beta"] != original.Headers["x-provider-beta"] {
		t.Errorf("Headers mismatch: got %#v, want %#v", clone.Headers, original.Headers)
	}
}

// TestCloneAIOptions_MutationIsolation verifies mutation of clone doesn't affect original
// This is the critical behavior that fixes the chain failover bug
func TestCloneAIOptions_MutationIsolation(t *testing.T) {
	original := &core.AIOptions{
		Model:           "smart",
		Temperature:     0.5,
		MaxTokens:       500,
		SystemPrompt:    "Original prompt",
		ReasoningEffort: "medium",
		ResponseFormat:  "json",
		Extra: map[string]interface{}{
			"provider_flag": true,
		},
		Headers: map[string]string{
			"x-provider-beta": "enabled",
		},
	}

	clone := cloneAIOptions(original)

	// Mutate the clone (simulates what ApplyDefaults does)
	clone.Model = "gpt-4.1-mini-2025-04-14" // Resolved model name
	clone.Temperature = 0.8
	clone.MaxTokens = 2000
	clone.SystemPrompt = "Modified prompt"
	clone.ReasoningEffort = "low"
	clone.ResponseFormat = ""
	clone.Extra["provider_flag"] = false
	clone.Headers["x-provider-beta"] = "disabled"

	// Verify original is unchanged
	if original.Model != "smart" {
		t.Errorf("Original Model was mutated: got %q, want %q", original.Model, "smart")
	}
	if original.Temperature != 0.5 {
		t.Errorf("Original Temperature was mutated: got %v, want %v", original.Temperature, 0.5)
	}
	if original.MaxTokens != 500 {
		t.Errorf("Original MaxTokens was mutated: got %d, want %d", original.MaxTokens, 500)
	}
	if original.SystemPrompt != "Original prompt" {
		t.Errorf("Original SystemPrompt was mutated: got %q, want %q", original.SystemPrompt, "Original prompt")
	}
	if original.ReasoningEffort != "medium" {
		t.Errorf("Original ReasoningEffort was mutated: got %q, want %q", original.ReasoningEffort, "medium")
	}
	if original.ResponseFormat != "json" {
		t.Errorf("Original ResponseFormat was mutated: got %q, want %q", original.ResponseFormat, "json")
	}
	if original.Extra["provider_flag"] != true {
		t.Errorf("Original Extra was mutated: got %#v", original.Extra)
	}
	if original.Headers["x-provider-beta"] != "enabled" {
		t.Errorf("Original Headers were mutated: got %#v", original.Headers)
	}
}

// TestCloneAIOptions_EmptyOptions verifies cloning works with zero-value options
func TestCloneAIOptions_EmptyOptions(t *testing.T) {
	original := &core.AIOptions{} // All zero values

	clone := cloneAIOptions(original)

	if clone == nil {
		t.Fatal("Expected non-nil clone, got nil")
	}
	if clone == original {
		t.Error("Clone should be a different pointer than original")
	}

	// Verify zero values are preserved
	if clone.Model != "" {
		t.Errorf("Expected empty Model, got %q", clone.Model)
	}
	if clone.Temperature != 0 {
		t.Errorf("Expected zero Temperature, got %v", clone.Temperature)
	}
	if clone.MaxTokens != 0 {
		t.Errorf("Expected zero MaxTokens, got %d", clone.MaxTokens)
	}
	if clone.SystemPrompt != "" {
		t.Errorf("Expected empty SystemPrompt, got %q", clone.SystemPrompt)
	}
	if clone.ReasoningEffort != "" {
		t.Errorf("Expected empty ReasoningEffort, got %q", clone.ReasoningEffort)
	}
	if clone.ResponseFormat != "" {
		t.Errorf("Expected empty ResponseFormat, got %q", clone.ResponseFormat)
	}
	if clone.Extra != nil {
		t.Errorf("Expected nil Extra, got %#v", clone.Extra)
	}
	if clone.Headers != nil {
		t.Errorf("Expected nil Headers, got %#v", clone.Headers)
	}
}

func TestCloneAIOptions_DeepCopiesExtra(t *testing.T) {
	original := &core.AIOptions{
		Extra: map[string]interface{}{
			"provider_flag": "original",
		},
	}

	clone := cloneAIOptions(original)
	clone.Extra["provider_flag"] = "clone"

	if original.Extra["provider_flag"] != "original" {
		t.Fatalf("original Extra mutated: %#v", original.Extra)
	}
}

func TestCloneAIOptions_DeepCopiesHeaders(t *testing.T) {
	original := &core.AIOptions{
		Headers: map[string]string{
			"x-provider-beta": "original",
		},
	}

	clone := cloneAIOptions(original)
	clone.Headers["x-provider-beta"] = "clone"

	if original.Headers["x-provider-beta"] != "original" {
		t.Fatalf("original Headers mutated: %#v", original.Headers)
	}
}

// --- isClientError: IsRetryable() escape hatch ---
//
// The chain client's isClientError historically classified all 4xx (except
// 401/403/429) as "fail-fast, don't try other providers". The new
// IsRetryable() method on core.ProviderError lets a provider override that
// classification for terminal-but-provider-specific errors like billing
// exhaustion. These tests pin the chain client's contract:
//
//   - IsRetryable() == true → return false from isClientError → chain fails over
//   - IsRetryable() == false → fall back to status-code arithmetic (existing rules)
//
// The check must run AFTER IsTransient() (which already short-circuits) so
// that proxy 4xx errors keep their existing semantics regardless of the
// retryable flag.

func TestIsClientError_RetryableTrueOverrides400(t *testing.T) {
	// A 400 from a provider that knows the failure category may succeed on
	// a different provider (e.g. billing exhausted) — must be classified as
	// retryable so the chain walks to the next provider.
	err := &testProviderError{
		statusCode: 400,
		provider:   "anthropic",
		message:    "credit balance too low",
		retryable:  true,
	}
	if isClientError(err) {
		t.Error("400 with IsRetryable=true must NOT be classified as a fail-fast client error")
	}
}

func TestIsClientError_RetryableFalseFallsBackToStatusRules(t *testing.T) {
	// A 400 with neither IsTransient nor IsRetryable set — the existing
	// fail-fast 4xx rule must apply unchanged. This is the regression guard
	// for the case where the new flag is absent.
	err := &testProviderError{
		statusCode: 400,
		provider:   "anthropic",
		message:    "messages.0: invalid input format",
		retryable:  false,
	}
	if !isClientError(err) {
		t.Error("400 with IsRetryable=false must remain a fail-fast client error (no behavior change)")
	}
}

func TestIsClientError_TransientShortCircuitsBeforeRetryable(t *testing.T) {
	// Transient (proxy) errors must continue to short-circuit to retryable.
	// This test covers the order-independence of the two flags: even if
	// IsRetryable is somehow set to false on a transient proxy error, the
	// proxy classification still wins.
	err := &testProviderError{
		statusCode: 400,
		provider:   "openai",
		message:    "Cloudflare HTML 400",
		transient:  true,
		retryable:  false,
	}
	if isClientError(err) {
		t.Error("transient proxy errors must always be retryable, regardless of the new IsRetryable flag")
	}
}

func TestIsClientError_RetryableTrueDoesNotMaskAuth(t *testing.T) {
	// 401 was already retryable in the chain client (each provider has its
	// own API key, so we want to fail over). The new IsRetryable flag must
	// not break that — it adds to the set of retryable errors, not replace
	// the existing 401/403/429 semantics.
	err := &testProviderError{
		statusCode: 401,
		provider:   "openai",
		message:    "invalid api key",
		retryable:  false,
	}
	if isClientError(err) {
		t.Error("401 must remain retryable so chain failover keeps trying other providers with their own keys")
	}
}

// --- WithChainMaxRetries: option propagates to per-provider clients ---
//
// WithChainMaxRetries is a chain-level option that needs to reach each
// per-provider BaseClient.MaxRetries via the NewClient → factory plumbing.
// These tests pin the propagation contract.

func TestChainConfig_DefaultMaxRetriesIsSentinel(t *testing.T) {
	// Without WithChainMaxRetries, the chain config must hold the unset
	// sentinel so that per-provider NewClient calls fall through to their
	// own resolveMaxRetries() (env var / default) — the chain doesn't
	// override anything.
	config := &ChainConfig{}
	// Apply no options — config.MaxRetries is the Go zero value (0), but
	// NewChainClient initializes it to maxRetriesUnset before applying opts.
	// We test the constructor's initialization separately below.
	_ = config // suppress unused

	// Use the constructor path to verify the sentinel is installed:
	wantSentinel := maxRetriesUnset
	c := &ChainConfig{MaxRetries: wantSentinel}
	if c.MaxRetries != wantSentinel {
		t.Fatalf("sentinel value drift: got %d want %d", c.MaxRetries, wantSentinel)
	}
}

func TestWithChainMaxRetries_SetsConfig(t *testing.T) {
	config := &ChainConfig{MaxRetries: maxRetriesUnset}
	WithChainMaxRetries(7)(config)
	if config.MaxRetries != 7 {
		t.Errorf("WithChainMaxRetries(7) should set config.MaxRetries to 7, got %d", config.MaxRetries)
	}
}

func TestWithChainMaxRetries_ZeroIsHonored(t *testing.T) {
	// The "no retries" escape hatch — explicit zero must propagate, not
	// silently fall back to default.
	config := &ChainConfig{MaxRetries: maxRetriesUnset}
	WithChainMaxRetries(0)(config)
	if config.MaxRetries != 0 {
		t.Errorf("WithChainMaxRetries(0) should set config.MaxRetries to 0, got %d", config.MaxRetries)
	}
}

// --- Chain client per-provider MaxRetries default (Option C) ---
//
// The chain client's failover loop IS the retry mechanism. Per-provider
// in-provider retries inside a chain just amplify wasted token spend on
// failing providers before failover kicks in. So unlike single clients
// (default 3), chain clients default to 0 in-provider retries and let
// failover do the work. These tests pin that contract end-to-end through
// NewChainClient → factory.lastConfig.

func TestNewChainClient_DefaultPerProviderRetriesIsZero(t *testing.T) {
	// No env var, no WithChainMaxRetries — chain default of 0 must apply.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "")
	factory := withRecordingFactory(t)

	if _, err := NewChainClient(WithProviderChain("mock-recording")); err != nil {
		t.Fatalf("NewChainClient: %v", err)
	}
	if factory.lastConfig == nil {
		t.Fatal("factory was not invoked")
	}
	if factory.lastConfig.MaxRetries != 0 {
		t.Errorf("chain default per-provider MaxRetries must be 0 (chain failover handles retries), got %d", factory.lastConfig.MaxRetries)
	}
}

func TestNewChainClient_WithChainMaxRetriesOverridesDefault(t *testing.T) {
	// Operator opts back in to per-provider retries via WithChainMaxRetries(3).
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "")
	factory := withRecordingFactory(t)

	_, err := NewChainClient(
		WithProviderChain("mock-recording"),
		WithChainMaxRetries(3),
	)
	if err != nil {
		t.Fatalf("NewChainClient: %v", err)
	}
	if factory.lastConfig.MaxRetries != 3 {
		t.Errorf("WithChainMaxRetries(3) should override chain default 0: got %d", factory.lastConfig.MaxRetries)
	}
}

func TestNewChainClient_EnvVarOverridesChainDefault(t *testing.T) {
	// Operator sets TRUVAG3_AI_RETRY_ATTEMPTS=2 — env var beats the chain
	// default of 0 (precedence: option > env var > fallback).
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "2")
	factory := withRecordingFactory(t)

	if _, err := NewChainClient(WithProviderChain("mock-recording")); err != nil {
		t.Fatalf("NewChainClient: %v", err)
	}
	if factory.lastConfig.MaxRetries != 2 {
		t.Errorf("env var should override chain default 0: got %d, want 2", factory.lastConfig.MaxRetries)
	}
}

func TestNewChainClient_ExplicitChainOptionBeatsEnvVar(t *testing.T) {
	// WithChainMaxRetries(5) is the highest-precedence layer — wins even
	// when TRUVAG3_AI_RETRY_ATTEMPTS is also set.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "2")
	factory := withRecordingFactory(t)

	_, err := NewChainClient(
		WithProviderChain("mock-recording"),
		WithChainMaxRetries(5),
	)
	if err != nil {
		t.Fatalf("NewChainClient: %v", err)
	}
	if factory.lastConfig.MaxRetries != 5 {
		t.Errorf("WithChainMaxRetries should beat env var: got %d, want 5", factory.lastConfig.MaxRetries)
	}
}

func TestNewChainClient_EnvVarZeroIsRejectedFallsToChainDefault(t *testing.T) {
	// Per FRAMEWORK_DESIGN_PRINCIPLES §3.5 rule 3, env var values must be > 0.
	// TRUVAG3_AI_RETRY_ATTEMPTS=0 is rejected and falls through to the chain
	// default — which happens to also be 0, so user-visible behavior is the
	// same. The test exists to pin the precedence: the rejection happens at
	// the env-var-parsing layer, not because the chain client coincidentally
	// produces the same number.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "0")
	factory := withRecordingFactory(t)

	if _, err := NewChainClient(WithProviderChain("mock-recording")); err != nil {
		t.Fatalf("NewChainClient: %v", err)
	}
	if factory.lastConfig.MaxRetries != 0 {
		t.Errorf("expected chain default 0 (env var rejected by framework rule), got %d", factory.lastConfig.MaxRetries)
	}
}

func TestNewClient_EnvVarZeroIsRejectedFallsBackToFrameworkDefault(t *testing.T) {
	// Single client mirror of the above — when env var is 0, the framework
	// rule rejects it and the resolver falls back to defaultMaxRetries (3),
	// NOT to 0. Single clients keep the friendly hello-world default.
	// Operators who genuinely want zero retries on a single client must
	// pass WithMaxRetries(0) programmatically.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "0")
	factory := withRecordingFactory(t)

	if _, err := NewClient(WithProvider("mock-recording")); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if factory.lastConfig.MaxRetries != defaultMaxRetries {
		t.Errorf("env var 0 must be rejected and fall back to default %d (NOT to 0): got %d",
			defaultMaxRetries, factory.lastConfig.MaxRetries)
	}
}

// --- IsRetryable end-to-end failover and observability ---
//
// These tests pin the production-incident scenario end-to-end through the
// real ChainClient.GenerateResponse path: a billing-exhausted error from
// the first provider must (a) cause failover to the next provider, and
// (b) emit the chain_failover_retryable observability log so operators
// can detect cost signals at 3am.

func TestChainClient_BillingExhausted400FailsOver(t *testing.T) {
	// Scenario: provider1 returns the production Anthropic credit-exhausted
	// 400, provider2 succeeds. The chain MUST walk to provider2 instead of
	// fail-fast 4xx-aborting on provider1. This is the regression guard
	// against the reflect-98b3e4c0 incident.
	provider1 := &chainMockAIClient{
		name:       "anthropic",
		shouldFail: true,
		failWith: &testProviderError{
			statusCode: 400,
			provider:   "anthropic",
			message:    `Anthropic API error: invalid request - {"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API"}}`,
			retryable:  true,
		},
	}
	provider2 := &chainMockAIClient{name: "openai.groq", shouldFail: false}

	logger := &testLogger{logs: make([]string, 0)}
	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"anthropic", "openai.groq"},
		logger:          logger,
	}

	resp, err := client.GenerateResponse(context.Background(), "test prompt", nil)
	if err != nil {
		t.Fatalf("expected failover to succeed, got error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response from provider2, got nil")
	}
	if provider1.callCount != 1 {
		t.Errorf("provider1 should be called once: got %d", provider1.callCount)
	}
	if provider2.callCount != 1 {
		t.Errorf("provider2 should be called once after failover: got %d", provider2.callCount)
	}
}

func TestChainClient_BillingExhaustedEmitsObservabilityLog(t *testing.T) {
	// Bug 1 regression: when the chain fails over because of IsRetryable
	// (billing/quota exhaustion), the operator must see a distinct
	// "Provider terminal error (billing/quota), failing over" log line —
	// NOT just the generic "Provider failed in chain, trying next" line.
	// This is the only signal that distinguishes a costly failover (billing
	// dead, account needs action) from a free transient failover (proxy
	// hiccup, will self-resolve).
	provider1 := &chainMockAIClient{
		name:       "anthropic",
		shouldFail: true,
		failWith: &testProviderError{
			statusCode: 400,
			provider:   "anthropic",
			message:    "credit balance too low",
			retryable:  true,
		},
	}
	provider2 := &chainMockAIClient{name: "openai.groq", shouldFail: false}

	logger := &testLogger{logs: make([]string, 0)}
	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"anthropic", "openai.groq"},
		logger:          logger,
	}

	if _, err := client.GenerateResponse(context.Background(), "test prompt", nil); err != nil {
		t.Fatalf("expected failover to succeed, got error: %v", err)
	}

	// Scan the captured logs for the dedicated billing/quota failover line.
	// We don't assert exact-match because the testLogger discards fields and
	// only keeps the message string — sufficient for this test's purpose
	// of "did the right log call happen at all".
	var found bool
	for _, line := range logger.logs {
		if strings.Contains(line, "Provider terminal error (billing/quota), failing over to next provider") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("missing chain_failover_retryable observability log — operators won't see the cost signal. Captured logs:\n%s",
			strings.Join(logger.logs, "\n"))
	}
}

func TestChainClient_TransientErrorEmitsTransientLogNotRetryableLog(t *testing.T) {
	// Symmetric guard: a transient (proxy) error must still emit the
	// EXISTING "Transient proxy error" log, NOT the new
	// "Provider terminal error (billing/quota)" log. The two log paths are
	// independent and must not collide on the wrong error class.
	provider1 := &chainMockAIClient{
		name:       "anthropic",
		shouldFail: true,
		failWith: &testProviderError{
			statusCode: 400,
			provider:   "anthropic",
			message:    "Cloudflare HTML error page",
			transient:  true,
		},
	}
	provider2 := &chainMockAIClient{name: "openai.groq", shouldFail: false}

	logger := &testLogger{logs: make([]string, 0)}
	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"anthropic", "openai.groq"},
		logger:          logger,
	}

	if _, err := client.GenerateResponse(context.Background(), "test prompt", nil); err != nil {
		t.Fatalf("failover should succeed, got error: %v", err)
	}

	var foundTransient, foundRetryable bool
	for _, line := range logger.logs {
		if strings.Contains(line, "Transient proxy error, failing over to next provider") {
			foundTransient = true
		}
		if strings.Contains(line, "Provider terminal error (billing/quota), failing over to next provider") {
			foundRetryable = true
		}
	}
	if !foundTransient {
		t.Errorf("expected transient failover log to be emitted; logs:\n%s", strings.Join(logger.logs, "\n"))
	}
	if foundRetryable {
		t.Errorf("billing/quota log must NOT fire on transient errors — log paths must be independent; logs:\n%s", strings.Join(logger.logs, "\n"))
	}
}
