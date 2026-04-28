package ai

import (
	"testing"
	"time"
)

// TestConfigurationEdgeCases tests edge cases and validation for AI configuration options
func TestConfigurationEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		options  []AIOption
		validate func(*testing.T, *AIConfig)
	}{
		{
			name: "zero temperature",
			options: []AIOption{
				WithTemperature(0.0),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Temperature != 0.0 {
					t.Errorf("Expected temperature 0.0, got %f", config.Temperature)
				}
			},
		},
		{
			name: "maximum temperature",
			options: []AIOption{
				WithTemperature(1.0),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Temperature != 1.0 {
					t.Errorf("Expected temperature 1.0, got %f", config.Temperature)
				}
			},
		},
		{
			name: "negative max tokens",
			options: []AIOption{
				WithMaxTokens(-1),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.MaxTokens != -1 {
					t.Errorf("Expected max tokens -1, got %d", config.MaxTokens)
				}
			},
		},
		{
			name: "zero max tokens",
			options: []AIOption{
				WithMaxTokens(0),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.MaxTokens != 0 {
					t.Errorf("Expected max tokens 0, got %d", config.MaxTokens)
				}
			},
		},
		{
			name: "very large max tokens",
			options: []AIOption{
				WithMaxTokens(1000000),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.MaxTokens != 1000000 {
					t.Errorf("Expected max tokens 1000000, got %d", config.MaxTokens)
				}
			},
		},
		{
			name: "negative max retries",
			options: []AIOption{
				WithMaxRetries(-1),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.MaxRetries != -1 {
					t.Errorf("Expected max retries -1, got %d", config.MaxRetries)
				}
			},
		},
		{
			name: "zero timeout",
			options: []AIOption{
				WithTimeout(0),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Timeout != 0 {
					t.Errorf("Expected timeout 0, got %v", config.Timeout)
				}
			},
		},
		{
			name: "very long timeout",
			options: []AIOption{
				WithTimeout(24 * time.Hour),
			},
			validate: func(t *testing.T, config *AIConfig) {
				expected := 24 * time.Hour
				if config.Timeout != expected {
					t.Errorf("Expected timeout %v, got %v", expected, config.Timeout)
				}
			},
		},
		{
			name: "empty string values",
			options: []AIOption{
				WithProvider(""),
				WithAPIKey(""),
				WithBaseURL(""),
				WithModel(""),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Provider != "" || config.APIKey != "" ||
					config.BaseURL != "" || config.Model != "" {
					t.Error("Expected empty string values to be preserved")
				}
			},
		},
		{
			name: "unicode and special characters",
			options: []AIOption{
				WithAPIKey("key-with-unicode-🔑-chars"),
				WithModel("model/with/slashes"),
				WithBaseURL("https://api.example.com/v1/"),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.APIKey != "key-with-unicode-🔑-chars" {
					t.Errorf("Unicode API key not preserved: %s", config.APIKey)
				}
				if config.Model != "model/with/slashes" {
					t.Errorf("Model with slashes not preserved: %s", config.Model)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create default config
			config := &AIConfig{
				Provider:    string(ProviderAuto),
				MaxRetries:  3,
				Timeout:     180 * time.Second,
				Temperature: 0.7,
				MaxTokens:   1000,
			}

			// Apply options
			for _, opt := range tt.options {
				opt(config)
			}

			// Validate the configuration
			tt.validate(t, config)
		})
	}
}

// TestAWSConfigurationOptions tests AWS-specific configuration options
func TestAWSConfigurationOptions(t *testing.T) {
	tests := []struct {
		name     string
		options  []AIOption
		validate func(*testing.T, *AIConfig)
	}{
		{
			name: "AWS region configuration",
			options: []AIOption{
				WithRegion("us-west-2"),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Extra == nil {
					t.Fatal("Expected Extra map to be initialized")
				}
				region, ok := config.Extra["region"]
				if !ok {
					t.Fatal("Expected region to be set in Extra")
				}
				if region != "us-west-2" {
					t.Errorf("Expected region us-west-2, got %v", region)
				}
			},
		},
		{
			name: "AWS credentials without session token",
			options: []AIOption{
				WithAWSCredentials("access123", "secret456", ""),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Extra == nil {
					t.Fatal("Expected Extra map to be initialized")
				}

				accessKey, ok := config.Extra["aws_access_key_id"]
				if !ok || accessKey != "access123" {
					t.Errorf("Expected access key access123, got %v", accessKey)
				}

				secretKey, ok := config.Extra["aws_secret_access_key"]
				if !ok || secretKey != "secret456" {
					t.Errorf("Expected secret key secret456, got %v", secretKey)
				}

				// Session token should not be set when empty
				if _, exists := config.Extra["aws_session_token"]; exists {
					t.Error("Expected session token not to be set when empty")
				}
			},
		},
		{
			name: "AWS credentials with session token",
			options: []AIOption{
				WithAWSCredentials("access123", "secret456", "session789"),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Extra == nil {
					t.Fatal("Expected Extra map to be initialized")
				}

				sessionToken, ok := config.Extra["aws_session_token"]
				if !ok || sessionToken != "session789" {
					t.Errorf("Expected session token session789, got %v", sessionToken)
				}
			},
		},
		{
			name: "multiple AWS configurations",
			options: []AIOption{
				WithRegion("eu-central-1"),
				WithAWSCredentials("key1", "secret1", "token1"),
				WithRegion("us-east-1"), // Should override previous region
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Extra == nil {
					t.Fatal("Expected Extra map to be initialized")
				}

				// Last region should win
				region, ok := config.Extra["region"]
				if !ok || region != "us-east-1" {
					t.Errorf("Expected region us-east-1, got %v", region)
				}

				// Credentials should still be present
				accessKey, ok := config.Extra["aws_access_key_id"]
				if !ok || accessKey != "key1" {
					t.Errorf("Expected access key to remain key1, got %v", accessKey)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AIConfig{}

			// Apply options
			for _, opt := range tt.options {
				opt(config)
			}

			// Validate the configuration
			tt.validate(t, config)
		})
	}
}

// TestHeadersConfiguration tests headers configuration functionality
func TestHeadersConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		options  []AIOption
		validate func(*testing.T, *AIConfig)
	}{
		{
			name: "single header set",
			options: []AIOption{
				WithHeaders(map[string]string{
					"Authorization": "Bearer token123",
				}),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Headers == nil {
					t.Fatal("Expected Headers map to be initialized")
				}
				if auth := config.Headers["Authorization"]; auth != "Bearer token123" {
					t.Errorf("Expected Authorization header, got %v", auth)
				}
			},
		},
		{
			name: "multiple headers in one call",
			options: []AIOption{
				WithHeaders(map[string]string{
					"User-Agent":      "truvag3/1.0",
					"Content-Type":    "application/json",
					"X-Custom-Header": "custom-value",
				}),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Headers == nil {
					t.Fatal("Expected Headers map to be initialized")
				}

				expected := map[string]string{
					"User-Agent":      "truvag3/1.0",
					"Content-Type":    "application/json",
					"X-Custom-Header": "custom-value",
				}

				for key, expectedValue := range expected {
					if actualValue := config.Headers[key]; actualValue != expectedValue {
						t.Errorf("Expected header %s=%s, got %s", key, expectedValue, actualValue)
					}
				}
			},
		},
		{
			name: "multiple header calls should merge",
			options: []AIOption{
				WithHeaders(map[string]string{
					"Header1": "value1",
					"Header2": "value2",
				}),
				WithHeaders(map[string]string{
					"Header3": "value3",
					"Header1": "overwritten", // Should overwrite previous value
				}),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Headers == nil {
					t.Fatal("Expected Headers map to be initialized")
				}

				expected := map[string]string{
					"Header1": "overwritten", // Overwritten by second call
					"Header2": "value2",      // Preserved from first call
					"Header3": "value3",      // Added by second call
				}

				for key, expectedValue := range expected {
					if actualValue := config.Headers[key]; actualValue != expectedValue {
						t.Errorf("Expected header %s=%s, got %s", key, expectedValue, actualValue)
					}
				}
			},
		},
		{
			name: "empty headers map",
			options: []AIOption{
				WithHeaders(map[string]string{}),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Headers == nil {
					t.Fatal("Expected Headers map to be initialized even when empty")
				}
				if len(config.Headers) != 0 {
					t.Errorf("Expected empty headers map, got %v", config.Headers)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AIConfig{}

			// Apply options
			for _, opt := range tt.options {
				opt(config)
			}

			// Validate the configuration
			tt.validate(t, config)
		})
	}
}

// TestExtraConfigurationOptions tests the generic extra configuration mechanism
func TestExtraConfigurationOptions(t *testing.T) {
	tests := []struct {
		name     string
		options  []AIOption
		validate func(*testing.T, *AIConfig)
	}{
		{
			name: "single extra option",
			options: []AIOption{
				WithExtra("custom_option", "custom_value"),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Extra == nil {
					t.Fatal("Expected Extra map to be initialized")
				}
				if value := config.Extra["custom_option"]; value != "custom_value" {
					t.Errorf("Expected custom_option=custom_value, got %v", value)
				}
			},
		},
		{
			name: "multiple extra options with different types",
			options: []AIOption{
				WithExtra("string_opt", "string_value"),
				WithExtra("int_opt", 42),
				WithExtra("bool_opt", true),
				WithExtra("float_opt", 3.14),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Extra == nil {
					t.Fatal("Expected Extra map to be initialized")
				}

				expected := map[string]interface{}{
					"string_opt": "string_value",
					"int_opt":    42,
					"bool_opt":   true,
					"float_opt":  3.14,
				}

				for key, expectedValue := range expected {
					if actualValue := config.Extra[key]; actualValue != expectedValue {
						t.Errorf("Expected %s=%v, got %v", key, expectedValue, actualValue)
					}
				}
			},
		},
		{
			name: "extra option overwrite",
			options: []AIOption{
				WithExtra("option", "first_value"),
				WithExtra("option", "second_value"),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Extra == nil {
					t.Fatal("Expected Extra map to be initialized")
				}
				if value := config.Extra["option"]; value != "second_value" {
					t.Errorf("Expected option=second_value (overwritten), got %v", value)
				}
			},
		},
		{
			name: "complex data structures in extra",
			options: []AIOption{
				WithExtra("nested_map", map[string]string{
					"key1": "value1",
					"key2": "value2",
				}),
				WithExtra("array", []string{"item1", "item2", "item3"}),
			},
			validate: func(t *testing.T, config *AIConfig) {
				if config.Extra == nil {
					t.Fatal("Expected Extra map to be initialized")
				}

				// Validate nested map
				nestedMap, ok := config.Extra["nested_map"].(map[string]string)
				if !ok {
					t.Fatal("Expected nested_map to be map[string]string")
				}
				if nestedMap["key1"] != "value1" || nestedMap["key2"] != "value2" {
					t.Errorf("Nested map values incorrect: %v", nestedMap)
				}

				// Validate array
				array, ok := config.Extra["array"].([]string)
				if !ok {
					t.Fatal("Expected array to be []string")
				}
				if len(array) != 3 || array[0] != "item1" {
					t.Errorf("Array values incorrect: %v", array)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AIConfig{}

			// Apply options
			for _, opt := range tt.options {
				opt(config)
			}

			// Validate the configuration
			tt.validate(t, config)
		})
	}
}
