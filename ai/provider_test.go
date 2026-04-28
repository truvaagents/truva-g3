package ai

import (
	"context"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestProviderOptions(t *testing.T) {
	tests := []struct {
		name   string
		option AIOption
		verify func(*testing.T, *AIConfig)
	}{
		{
			name:   "WithProvider",
			option: WithProvider("custom"),
			verify: func(t *testing.T, c *AIConfig) {
				if c.Provider != "custom" {
					t.Errorf("expected provider 'custom', got %q", c.Provider)
				}
			},
		},
		{
			name:   "WithAPIKey",
			option: WithAPIKey("test-api-key"),
			verify: func(t *testing.T, c *AIConfig) {
				if c.APIKey != "test-api-key" {
					t.Errorf("expected API key 'test-api-key', got %q", c.APIKey)
				}
			},
		},
		{
			name:   "WithBaseURL",
			option: WithBaseURL("https://custom.api.com/v1"),
			verify: func(t *testing.T, c *AIConfig) {
				if c.BaseURL != "https://custom.api.com/v1" {
					t.Errorf("expected base URL 'https://custom.api.com/v1', got %q", c.BaseURL)
				}
			},
		},
		{
			name:   "WithTimeout",
			option: WithTimeout(60 * time.Second),
			verify: func(t *testing.T, c *AIConfig) {
				if c.Timeout != 60*time.Second {
					t.Errorf("expected timeout 60s, got %v", c.Timeout)
				}
			},
		},
		{
			name:   "WithMaxRetries",
			option: WithMaxRetries(5),
			verify: func(t *testing.T, c *AIConfig) {
				if c.MaxRetries != 5 {
					t.Errorf("expected max retries 5, got %d", c.MaxRetries)
				}
			},
		},
		{
			name:   "WithModel",
			option: WithModel("gpt-4-turbo"),
			verify: func(t *testing.T, c *AIConfig) {
				if c.Model != "gpt-4-turbo" {
					t.Errorf("expected model 'gpt-4-turbo', got %q", c.Model)
				}
			},
		},
		{
			name:   "WithTemperature",
			option: WithTemperature(0.8),
			verify: func(t *testing.T, c *AIConfig) {
				if c.Temperature != 0.8 {
					t.Errorf("expected temperature 0.8, got %f", c.Temperature)
				}
			},
		},
		{
			name:   "WithMaxTokens",
			option: WithMaxTokens(2000),
			verify: func(t *testing.T, c *AIConfig) {
				if c.MaxTokens != 2000 {
					t.Errorf("expected max tokens 2000, got %d", c.MaxTokens)
				}
			},
		},
		{
			name:   "WithRegion for AWS",
			option: WithRegion("us-west-2"),
			verify: func(t *testing.T, c *AIConfig) {
				if c.Extra == nil {
					t.Fatal("expected Extra map to be initialized")
				}
				if region, ok := c.Extra["region"].(string); !ok || region != "us-west-2" {
					t.Errorf("expected region 'us-west-2', got %v", c.Extra["region"])
				}
			},
		},
		{
			name:   "WithAWSCredentials full",
			option: WithAWSCredentials("access-key", "secret-key", "session-token"),
			verify: func(t *testing.T, c *AIConfig) {
				if c.Extra == nil {
					t.Fatal("expected Extra map to be initialized")
				}
				if ak, ok := c.Extra["aws_access_key_id"].(string); !ok || ak != "access-key" {
					t.Errorf("expected access key 'access-key', got %v", c.Extra["aws_access_key_id"])
				}
				if sk, ok := c.Extra["aws_secret_access_key"].(string); !ok || sk != "secret-key" {
					t.Errorf("expected secret key 'secret-key', got %v", c.Extra["aws_secret_access_key"])
				}
				if st, ok := c.Extra["aws_session_token"].(string); !ok || st != "session-token" {
					t.Errorf("expected session token 'session-token', got %v", c.Extra["aws_session_token"])
				}
			},
		},
		{
			name:   "WithAWSCredentials without session token",
			option: WithAWSCredentials("access-key", "secret-key", ""),
			verify: func(t *testing.T, c *AIConfig) {
				if c.Extra == nil {
					t.Fatal("expected Extra map to be initialized")
				}
				if _, exists := c.Extra["aws_session_token"]; exists {
					t.Error("expected no session token in Extra map")
				}
			},
		},
		{
			name: "WithHeaders new map",
			option: WithHeaders(map[string]string{
				"X-Custom-Header": "custom-value",
				"Authorization":   "Bearer token",
			}),
			verify: func(t *testing.T, c *AIConfig) {
				if c.Headers == nil {
					t.Fatal("expected Headers map to be initialized")
				}
				if c.Headers["X-Custom-Header"] != "custom-value" {
					t.Errorf("expected header X-Custom-Header='custom-value', got %q", c.Headers["X-Custom-Header"])
				}
				if c.Headers["Authorization"] != "Bearer token" {
					t.Errorf("expected header Authorization='Bearer token', got %q", c.Headers["Authorization"])
				}
			},
		},
		{
			name:   "WithExtra custom field",
			option: WithExtra("custom_field", "custom_value"),
			verify: func(t *testing.T, c *AIConfig) {
				if c.Extra == nil {
					t.Fatal("expected Extra map to be initialized")
				}
				if val, ok := c.Extra["custom_field"].(string); !ok || val != "custom_value" {
					t.Errorf("expected custom_field='custom_value', got %v", c.Extra["custom_field"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AIConfig{}
			tt.option(config)
			tt.verify(t, config)
		})
	}
}

func TestMultipleOptions(t *testing.T) {
	config := &AIConfig{}

	// Apply multiple options
	options := []AIOption{
		WithProvider("openai"),
		WithAPIKey("test-key"),
		WithModel("gpt-4"),
		WithTemperature(0.7),
		WithMaxTokens(1500),
		WithHeaders(map[string]string{"X-Header-1": "value1"}),
		WithHeaders(map[string]string{"X-Header-2": "value2"}), // Second call should merge
		WithExtra("field1", "value1"),
		WithExtra("field2", "value2"),
	}

	for _, opt := range options {
		opt(config)
	}

	// Verify all options were applied
	if config.Provider != "openai" {
		t.Errorf("expected provider 'openai', got %q", config.Provider)
	}
	if config.APIKey != "test-key" {
		t.Errorf("expected API key 'test-key', got %q", config.APIKey)
	}
	if config.Model != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got %q", config.Model)
	}
	if config.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %f", config.Temperature)
	}
	if config.MaxTokens != 1500 {
		t.Errorf("expected max tokens 1500, got %d", config.MaxTokens)
	}

	// Verify headers were merged
	if len(config.Headers) != 2 {
		t.Errorf("expected 2 headers, got %d", len(config.Headers))
	}
	if config.Headers["X-Header-1"] != "value1" {
		t.Errorf("expected X-Header-1='value1', got %q", config.Headers["X-Header-1"])
	}
	if config.Headers["X-Header-2"] != "value2" {
		t.Errorf("expected X-Header-2='value2', got %q", config.Headers["X-Header-2"])
	}

	// Verify extra fields
	if len(config.Extra) != 2 {
		t.Errorf("expected 2 extra fields, got %d", len(config.Extra))
	}
	if config.Extra["field1"] != "value1" {
		t.Errorf("expected field1='value1', got %v", config.Extra["field1"])
	}
	if config.Extra["field2"] != "value2" {
		t.Errorf("expected field2='value2', got %v", config.Extra["field2"])
	}
}

func TestProviderConstants(t *testing.T) {
	// Test that provider constants have expected values
	tests := []struct {
		provider Provider
		expected string
	}{
		{ProviderOpenAI, "openai"},
		{ProviderAnthropic, "anthropic"},
		{ProviderGemini, "gemini"},
		{ProviderOllama, "ollama"},
		{ProviderAuto, "auto"},
		{ProviderCustom, "custom"},
	}

	for _, tt := range tests {
		if string(tt.provider) != tt.expected {
			t.Errorf("Provider constant %v = %q, want %q", tt.provider, string(tt.provider), tt.expected)
		}
	}
}

// mockTelemetry for testing
type mockTelemetry struct{}

func (m *mockTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	return ctx, &mockSpan{}
}

func (m *mockTelemetry) RecordMetric(name string, value float64, labels map[string]string) {}

type mockSpan struct{}

func (m *mockSpan) End()                                       {}
func (m *mockSpan) SetAttribute(key string, value interface{}) {}
func (m *mockSpan) RecordError(err error)                      {}

func TestWithTelemetry(t *testing.T) {
	tests := []struct {
		name      string
		telemetry core.Telemetry
		expectNil bool
	}{
		{
			name:      "with telemetry",
			telemetry: &mockTelemetry{},
			expectNil: false,
		},
		{
			name:      "with nil telemetry",
			telemetry: nil,
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AIConfig{}
			opt := WithTelemetry(tt.telemetry)
			opt(config)

			if tt.expectNil {
				if config.Telemetry != nil {
					t.Error("expected nil telemetry")
				}
			} else {
				if config.Telemetry == nil {
					t.Error("expected non-nil telemetry")
				}
				if config.Telemetry != tt.telemetry {
					t.Error("telemetry not set correctly")
				}
			}
		})
	}
}
