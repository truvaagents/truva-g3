package ai

import (
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestConfigurationApplication(t *testing.T) {
	// Register a test provider to verify configuration application
	testFactory := &testProviderFactory{}
	_ = Register(testFactory)

	// Test that configuration is properly applied to providers
	tests := []struct {
		name   string
		opts   []AIOption
		verify func(*AIConfig) bool
	}{
		{
			name: "apply model configuration",
			opts: []AIOption{
				WithProvider("test-provider"),
				WithModel("gpt-4-turbo"),
				WithAPIKey("test-key"),
			},
			verify: func(c *AIConfig) bool {
				return c.Model == "gpt-4-turbo"
			},
		},
		{
			name: "apply temperature configuration",
			opts: []AIOption{
				WithProvider("test-provider"),
				WithTemperature(0.2),
				WithAPIKey("test-key"),
			},
			verify: func(c *AIConfig) bool {
				return c.Temperature == 0.2
			},
		},
		{
			name: "apply max tokens configuration",
			opts: []AIOption{
				WithProvider("test-provider"),
				WithMaxTokens(2000),
				WithAPIKey("test-key"),
			},
			verify: func(c *AIConfig) bool {
				return c.MaxTokens == 2000
			},
		},
		{
			name: "apply timeout configuration",
			opts: []AIOption{
				WithProvider("test-provider"),
				WithTimeout(60 * time.Second),
				WithAPIKey("test-key"),
			},
			verify: func(c *AIConfig) bool {
				return c.Timeout == 60*time.Second
			},
		},
		{
			name: "apply retry configuration",
			opts: []AIOption{
				WithProvider("test-provider"),
				WithMaxRetries(5),
				WithAPIKey("test-key"),
			},
			verify: func(c *AIConfig) bool {
				return c.MaxRetries == 5
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create config
			config := &AIConfig{
				Provider:    string(ProviderAuto),
				MaxRetries:  3,
				Timeout:     180 * time.Second,
				Temperature: 0.7,
				MaxTokens:   1000,
			}

			// Apply options
			for _, opt := range tt.opts {
				opt(config)
			}

			// Verify configuration
			if !tt.verify(config) {
				t.Errorf("Configuration not properly applied")
			}

			// Test that client can be created with configuration
			client, err := NewClient(tt.opts...)
			if err != nil {
				t.Errorf("Failed to create client: %v", err)
			}
			if client == nil {
				t.Error("Client is nil")
			}
		})
	}
}

// testProviderFactory is a mock implementation for testing
type testProviderFactory struct {
	lastConfig *AIConfig
}

func (t *testProviderFactory) Create(config *AIConfig) core.AIClient {
	t.lastConfig = config
	return &mockAIClient{}
}

func (t *testProviderFactory) DetectEnvironment() (int, bool) {
	return 50, true
}

func (t *testProviderFactory) Name() string {
	return "test-provider"
}

func (t *testProviderFactory) Description() string {
	return "Test Provider"
}
