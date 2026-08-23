//go:build integration
// +build integration

package ai

import (
	"os"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// ================================
// Phase 3 Integration Tests: Require Provider Registration
// ================================
//
// Run with: go test -tags=integration ./ai/...
//
// These tests require:
// - Provider packages to be imported (see init() in ai package)
// - Valid API keys in environment or explicit configuration
//

// TestPhase3_ChainClientCreation verifies basic chain client creation with real providers
func TestPhase3_ChainClientCreation(t *testing.T) {
	// Save environment
	originalVars := saveChainEnvironment()
	defer restoreChainEnvironment(originalVars)

	// Set up multiple providers
	os.Setenv("OPENAI_API_KEY", "sk-openai-test")
	os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-test")

	tests := []struct {
		name          string
		opts          []ChainOption
		expectError   bool
		errorContains string
		expectedCount int
		description   string
	}{
		{
			name: "Valid chain with multiple providers",
			opts: []ChainOption{
				WithProviderChain("openai", "openai.deepseek"),
				WithChainLogger(&core.NoOpLogger{}),
			},
			expectError:   false,
			expectedCount: 2,
			description:   "Should create chain with 2 providers",
		},
		{
			name: "Single provider chain",
			opts: []ChainOption{
				WithProviderChain("openai"),
			},
			expectError:   false,
			expectedCount: 1,
			description:   "Single provider chain works",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewChainClient(tt.opts...)

			if tt.expectError {
				if err == nil {
					t.Errorf("%s: Expected error, got nil", tt.description)
				} else if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("%s: Expected error containing %q, got %q",
						tt.description, tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("%s: Unexpected error: %v", tt.description, err)
				}
				if client == nil {
					t.Errorf("%s: Expected non-nil client", tt.description)
				}
				if client != nil && len(client.providers) != tt.expectedCount {
					t.Errorf("%s: Expected %d providers, got %d",
						tt.description, tt.expectedCount, len(client.providers))
				}
			}
		})
	}
}

// TestPhase3_PartialChainSupport verifies graceful handling of missing providers
func TestPhase3_PartialChainSupport(t *testing.T) {
	// Save environment
	originalVars := saveChainEnvironment()
	defer restoreChainEnvironment(originalVars)

	// Only set one provider's API key
	clearAllChainEnvVars()
	os.Setenv("OPENAI_API_KEY", "sk-openai-test")

	// Request 3 providers but only 1 has API key
	client, err := NewChainClient(
		WithProviderChain("openai", "openai.deepseek", "anthropic"),
		WithChainLogger(&core.NoOpLogger{}),
	)

	// Should succeed with partial chain (warnings logged)
	if err != nil {
		t.Errorf("Partial chain should be allowed, got error: %v", err)
	}
	if client == nil {
		t.Error("Expected non-nil client")
	}

	// Should have at least 1 provider
	if client != nil && len(client.providers) < 1 {
		t.Errorf("Expected at least 1 provider, got %d", len(client.providers))
	}
}

// TestPhase3_ProviderAliasValidation verifies all valid aliases are accepted by real providers
func TestPhase3_ProviderAliasValidation(t *testing.T) {
	validAliases := []string{
		"openai",
		"openai.openrouter",
		"anthropic",
		"gemini",
		"openai.deepseek",
		"openai.groq",
		"openai.xai",
		"openai.together",
		"openai.qwen",
		"openai.ollama",
	}

	// Set minimal environment for OpenAI
	originalVars := saveChainEnvironment()
	defer restoreChainEnvironment(originalVars)
	os.Setenv("OPENAI_API_KEY", "sk-test")

	for _, alias := range validAliases {
		t.Run(alias, func(t *testing.T) {
			// Try creating chain with each alias
			// Some will fail due to missing API keys, but shouldn't get validation errors
			client, err := NewChainClient(
				WithProviderChain(alias),
			)

			// Should either succeed or fail with "no providers" (not validation error)
			if err != nil && strings.Contains(err.Error(), "unknown provider alias") {
				t.Errorf("Valid alias %q was rejected as unknown", alias)
			}

			// If client was created (has API key), verify it worked
			if client != nil && len(client.providers) == 0 {
				t.Errorf("Client created but has 0 providers for alias %q", alias)
			}
		})
	}
}

// TestPhase3_BackwardCompatibility verifies Phase 3 doesn't break Phases 1-2
func TestPhase3_BackwardCompatibility(t *testing.T) {
	// Save environment
	originalVars := saveChainEnvironment()
	defer restoreChainEnvironment(originalVars)

	t.Run("Phase 1 compatibility", func(t *testing.T) {
		// Phase 1: Zero-config with auto-detection
		clearAllChainEnvVars()
		os.Setenv("OPENAI_API_KEY", "sk-test")

		client, err := NewClient() // No WithProviderAlias
		if err != nil {
			t.Errorf("Phase 1 zero-config broken: %v", err)
		}
		if client == nil {
			t.Error("Phase 1 zero-config returned nil client")
		}
	})

	t.Run("Phase 2 compatibility", func(t *testing.T) {
		// Phase 2: Provider alias with auto-configuration
		os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-test")

		client, err := NewClient(WithProviderAlias("openai.deepseek"))
		if err != nil {
			t.Errorf("Phase 2 provider alias broken: %v", err)
		}
		if client == nil {
			t.Error("Phase 2 provider alias returned nil client")
		}
	})

	t.Run("Existing API unchanged", func(t *testing.T) {
		// Verify existing NewClient() API still works
		os.Setenv("OPENAI_API_KEY", "sk-test")

		client, err := NewClient(
			WithProvider("openai"),
			WithModel("gpt-4"),
		)
		if err != nil {
			t.Errorf("Existing NewClient API broken: %v", err)
		}
		if client == nil {
			t.Error("Existing NewClient returned nil")
		}
	})
}
