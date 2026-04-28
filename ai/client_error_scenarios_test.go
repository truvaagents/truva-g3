package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// TestMustNewClientPanic tests that MustNewClient panics on errors
func TestMustNewClientPanic(t *testing.T) {
	// Save and restore original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	// Set up registry with no providers to force error
	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}

	defer func() {
		if r := recover(); r != nil {
			// Expected panic - check that error message is reasonable
			errMsg := r.(string)
			if !strings.Contains(errMsg, "failed to create AI client") {
				t.Errorf("Expected panic message to contain 'failed to create AI client', got: %s", errMsg)
			}
		} else {
			t.Error("Expected MustNewClient to panic when no providers available")
		}
	}()

	// This should panic
	MustNewClient()
}

// TestNewClientNoProviders tests client creation when no providers are registered
func TestNewClientNoProviders(t *testing.T) {
	// Save and restore original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	// Set up empty registry
	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}

	_, err := NewClient()
	if err == nil {
		t.Error("Expected error when no providers are registered")
	}

	expectedErrMsg := "no AI provider available"
	if !strings.Contains(err.Error(), expectedErrMsg) {
		t.Errorf("Expected error to contain '%s', got: %s", expectedErrMsg, err.Error())
	}
}

// TestNewClientProviderFactoryError tests when provider factory creation fails
func TestNewClientProviderFactoryError(t *testing.T) {
	// Save and restore original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	// Set up registry with a factory that errors on creation
	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}

	factoryErr := errors.New("factory creation failed")
	registry.providers["error-provider"] = &mockFactory{
		name:      "error-provider",
		available: true,
		createErr: factoryErr,
	}

	// NewClient should succeed (it creates an errorClient)
	client, err := NewClient(WithProvider("error-provider"))
	if err != nil {
		t.Errorf("Unexpected error creating client: %v", err)
	}
	if client == nil {
		t.Fatal("Expected client to be created")
	}

	// But using the client should fail with the factory error
	ctx := context.Background()
	_, err = client.GenerateResponse(ctx, "test", nil)
	if err == nil {
		t.Error("Expected error when using client created from error factory")
	}
	if !strings.Contains(err.Error(), "factory creation failed") {
		t.Errorf("Expected error to contain factory error message, got: %s", err.Error())
	}
}

// TestNewClientInvalidProviderName tests client creation with invalid provider names
func TestNewClientInvalidProviderName(t *testing.T) {
	// Save and restore original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	// Set up empty registry
	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}

	testCases := []struct {
		name         string
		providerName string
	}{
		{"unknown provider", "unknown-provider"},
		{"empty provider name", ""},
		{"special characters", "provider@#$%"},
		{"very long name", strings.Repeat("a", 1000)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(WithProvider(tc.providerName))
			if err == nil {
				t.Errorf("Expected error for provider '%s'", tc.providerName)
			}

			expectedErrMsg := "not registered"
			if !strings.Contains(err.Error(), expectedErrMsg) {
				t.Errorf("Expected error to contain '%s', got: %s", expectedErrMsg, err.Error())
			}
		})
	}
}

// TestNewClientAutoDetectionEdgeCases tests auto-detection with various edge cases
func TestNewClientAutoDetectionEdgeCases(t *testing.T) {
	testCases := []struct {
		name        string
		setup       func()
		expectError bool
		errorMsg    string
	}{
		{
			name: "all providers unavailable",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				registry.providers["unavailable1"] = &mockFactory{
					name:      "unavailable1",
					priority:  100,
					available: false,
				}
				registry.providers["unavailable2"] = &mockFactory{
					name:      "unavailable2",
					priority:  50,
					available: false,
				}
			},
			expectError: true,
			errorMsg:    "no AI provider available",
		},
		{
			name: "providers with same priority",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				registry.providers["provider1"] = &mockFactory{
					name:      "provider1",
					priority:  100,
					available: true,
					client:    &mockAIClient{},
				}
				registry.providers["provider2"] = &mockFactory{
					name:      "provider2",
					priority:  100, // Same priority
					available: true,
					client:    &mockAIClient{},
				}
			},
			expectError: false, // Should succeed with one of them
		},
		{
			name: "negative priority providers",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				registry.providers["negative"] = &mockFactory{
					name:      "negative",
					priority:  -10,
					available: true,
					client:    &mockAIClient{},
				}
				registry.providers["positive"] = &mockFactory{
					name:      "positive",
					priority:  10,
					available: true,
					client:    &mockAIClient{},
				}
			},
			expectError: false, // Should pick the positive priority one
		},
		{
			name: "only negative priority available",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				registry.providers["negative"] = &mockFactory{
					name:      "negative",
					priority:  -10,
					available: true,
					client:    &mockAIClient{},
				}
			},
			expectError: false, // Should still work with negative priority
		},
	}

	// Save and restore original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()

			client, err := NewClient() // Auto-detection

			if tc.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				} else if !strings.Contains(err.Error(), tc.errorMsg) {
					t.Errorf("Expected error to contain '%s', got: %s", tc.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if client == nil {
					t.Error("Expected client to be created")
				}
			}
		})
	}
}

// TestClientOptionsAppliedInOrder tests that options are applied in the correct order
func TestClientOptionsAppliedInOrder(t *testing.T) {
	// Save and restore original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	// Set up registry with a provider that captures config
	var capturedConfig *AIConfig
	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}
	registry.providers["test"] = &mockFactory{
		name:      "test",
		priority:  100,
		available: true,
		client: &mockAIClient{
			generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
				return &core.AIResponse{Content: "test"}, nil
			},
		},
	}

	// Override the Create method to capture config
	registry.providers["test"] = &configCapturingFactory{
		mockFactory: registry.providers["test"].(*mockFactory),
		configPtr:   &capturedConfig,
	}

	// Apply options in specific order - later options should override earlier ones
	client, err := NewClient(
		WithProvider("test"),
		WithTemperature(0.5), // First temperature
		WithMaxTokens(100),   // First max tokens
		WithTemperature(0.8), // Should override first temperature
		WithAPIKey("key1"),   // First API key
		WithAPIKey("key2"),   // Should override first API key
	)

	if err != nil {
		t.Fatalf("Unexpected error creating client: %v", err)
	}

	if client == nil {
		t.Fatal("Expected client to be created")
	}

	if capturedConfig == nil {
		t.Fatal("Expected config to be captured")
	}

	// Check that last values win
	if capturedConfig.Temperature != 0.8 {
		t.Errorf("Expected temperature 0.8 (last value), got %f", capturedConfig.Temperature)
	}
	if capturedConfig.APIKey != "key2" {
		t.Errorf("Expected API key 'key2' (last value), got %s", capturedConfig.APIKey)
	}
	if capturedConfig.MaxTokens != 100 {
		t.Errorf("Expected max tokens 100, got %d", capturedConfig.MaxTokens)
	}
}

// configCapturingFactory captures the config passed to Create
type configCapturingFactory struct {
	*mockFactory
	configPtr **AIConfig
}

func (f *configCapturingFactory) Create(config *AIConfig) core.AIClient {
	*f.configPtr = config
	return f.mockFactory.Create(config)
}

// TestNewClientWithAllOptions tests client creation with all possible options
func TestNewClientWithAllOptions(t *testing.T) {
	// Save and restore original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	var capturedConfig *AIConfig
	registry = &ProviderRegistry{
		providers: make(map[string]ProviderFactory),
	}
	registry.providers["comprehensive"] = &configCapturingFactory{
		mockFactory: &mockFactory{
			name:      "comprehensive",
			priority:  100,
			available: true,
			client:    &mockAIClient{},
		},
		configPtr: &capturedConfig,
	}

	// Create client with every possible option
	client, err := NewClient(
		WithProvider("comprehensive"),
		WithAPIKey("test-api-key"),
		WithBaseURL("https://api.test.com"),
		WithModel("test-model"),
		WithTemperature(0.9),
		WithMaxTokens(2000),
		WithTimeout(60000000000), // 60 seconds in nanoseconds
		WithMaxRetries(5),
		WithHeaders(map[string]string{
			"User-Agent": "test-agent",
			"Custom":     "header-value",
		}),
		WithRegion("us-west-2"),
		WithAWSCredentials("aws-key", "aws-secret", "aws-token"),
		WithExtra("custom_param", "custom_value"),
	)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if client == nil {
		t.Fatal("Expected client to be created")
	}

	if capturedConfig == nil {
		t.Fatal("Expected config to be captured")
	}

	// Verify all configuration was applied
	tests := []struct {
		name     string
		expected interface{}
		actual   interface{}
	}{
		{"Provider", "comprehensive", capturedConfig.Provider},
		{"APIKey", "test-api-key", capturedConfig.APIKey},
		{"BaseURL", "https://api.test.com", capturedConfig.BaseURL},
		{"Model", "test-model", capturedConfig.Model},
		{"Temperature", float32(0.9), capturedConfig.Temperature},
		{"MaxTokens", 2000, capturedConfig.MaxTokens},
		{"MaxRetries", 5, capturedConfig.MaxRetries},
	}

	for _, test := range tests {
		if test.actual != test.expected {
			t.Errorf("%s: expected %v, got %v", test.name, test.expected, test.actual)
		}
	}

	// Check headers
	if capturedConfig.Headers == nil {
		t.Fatal("Expected headers to be set")
	}
	if capturedConfig.Headers["User-Agent"] != "test-agent" {
		t.Errorf("Expected User-Agent header to be 'test-agent', got %s", capturedConfig.Headers["User-Agent"])
	}

	// Check extra/AWS parameters
	if capturedConfig.Extra == nil {
		t.Fatal("Expected extra parameters to be set")
	}
	if capturedConfig.Extra["region"] != "us-west-2" {
		t.Errorf("Expected region to be 'us-west-2', got %v", capturedConfig.Extra["region"])
	}
	if capturedConfig.Extra["aws_access_key_id"] != "aws-key" {
		t.Errorf("Expected AWS access key to be 'aws-key', got %v", capturedConfig.Extra["aws_access_key_id"])
	}
	if capturedConfig.Extra["custom_param"] != "custom_value" {
		t.Errorf("Expected custom_param to be 'custom_value', got %v", capturedConfig.Extra["custom_param"])
	}
}
