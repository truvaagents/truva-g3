package orchestration

import (
	"os"
	"testing"
)

// =============================================================================
// DefaultConfig Tests for SemanticRetry.EnableForIndependentSteps
// =============================================================================

// TestDefaultConfig_SemanticRetryIndependentStepsEnabled verifies default is true
func TestDefaultConfig_SemanticRetryIndependentStepsEnabled(t *testing.T) {
	// Clear any env var that might interfere
	os.Unsetenv("TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS")

	config := DefaultConfig()

	if !config.SemanticRetry.EnableForIndependentSteps {
		t.Error("Expected EnableForIndependentSteps to default to true")
	}
}

// TestDefaultConfig_SemanticRetryIndependentStepsEnvVarTrue tests env var set to true
func TestDefaultConfig_SemanticRetryIndependentStepsEnvVarTrue(t *testing.T) {
	t.Setenv("TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS", "true")

	config := DefaultConfig()

	if !config.SemanticRetry.EnableForIndependentSteps {
		t.Error("Expected EnableForIndependentSteps to be true when env var is 'true'")
	}
}

// TestDefaultConfig_SemanticRetryIndependentStepsEnvVarFalse tests env var set to false
func TestDefaultConfig_SemanticRetryIndependentStepsEnvVarFalse(t *testing.T) {
	t.Setenv("TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS", "false")

	config := DefaultConfig()

	if config.SemanticRetry.EnableForIndependentSteps {
		t.Error("Expected EnableForIndependentSteps to be false when env var is 'false'")
	}
}

// TestDefaultConfig_SemanticRetryIndependentStepsEnvVarCaseInsensitive tests case insensitivity
func TestDefaultConfig_SemanticRetryIndependentStepsEnvVarCaseInsensitive(t *testing.T) {
	tests := []struct {
		envValue string
		expected bool
	}{
		{"TRUE", true},
		{"True", true},
		{"true", true},
		{"FALSE", false},
		{"False", false},
		{"false", false},
		{"invalid", false}, // Non-true values default to false
		{"1", false},       // Only "true" (case-insensitive) is truthy
		{"", true},         // Empty string uses default (true)
	}

	for _, tt := range tests {
		t.Run("env="+tt.envValue, func(t *testing.T) {
			if tt.envValue == "" {
				os.Unsetenv("TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS")
			} else {
				t.Setenv("TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS", tt.envValue)
			}

			config := DefaultConfig()

			if config.SemanticRetry.EnableForIndependentSteps != tt.expected {
				t.Errorf("Expected EnableForIndependentSteps=%v for env=%q, got %v",
					tt.expected, tt.envValue, config.SemanticRetry.EnableForIndependentSteps)
			}
		})
	}
}

// =============================================================================
// SmartExecutor Setter Tests
// =============================================================================

// TestSetSemanticRetryForIndependentSteps_Enable tests enabling via setter
func TestSetSemanticRetryForIndependentSteps_Enable(t *testing.T) {
	executor := &SmartExecutor{}

	executor.SetSemanticRetryForIndependentSteps(true)

	if !executor.semanticRetryForIndependentSteps {
		t.Error("Expected semanticRetryForIndependentSteps to be true after SetSemanticRetryForIndependentSteps(true)")
	}
}

// TestSetSemanticRetryForIndependentSteps_Disable tests disabling via setter
func TestSetSemanticRetryForIndependentSteps_Disable(t *testing.T) {
	executor := &SmartExecutor{
		semanticRetryForIndependentSteps: true, // Start with true
	}

	executor.SetSemanticRetryForIndependentSteps(false)

	if executor.semanticRetryForIndependentSteps {
		t.Error("Expected semanticRetryForIndependentSteps to be false after SetSemanticRetryForIndependentSteps(false)")
	}
}

// =============================================================================
// SemanticRetryConfig Struct Tests
// =============================================================================

// TestSemanticRetryConfig_FieldExists ensures the field exists in the struct
func TestSemanticRetryConfig_FieldExists(t *testing.T) {
	config := SemanticRetryConfig{
		Enabled:                   true,
		MaxAttempts:               2,
		TriggerStatusCodes:        []int{400, 422},
		EnableForIndependentSteps: true,
	}

	if !config.EnableForIndependentSteps {
		t.Error("Expected EnableForIndependentSteps to be settable in struct")
	}
}

// TestSemanticRetryConfig_DefaultValuesMatch verifies default config values
func TestSemanticRetryConfig_DefaultValuesMatch(t *testing.T) {
	os.Unsetenv("TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS")
	os.Unsetenv("TRUVAG3_SEMANTIC_RETRY_ENABLED")
	os.Unsetenv("TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS")

	config := DefaultConfig()

	// Verify all SemanticRetry defaults
	if !config.SemanticRetry.Enabled {
		t.Error("Expected SemanticRetry.Enabled to default to true")
	}
	if config.SemanticRetry.MaxAttempts != 2 {
		t.Errorf("Expected SemanticRetry.MaxAttempts to default to 2, got %d", config.SemanticRetry.MaxAttempts)
	}
	if len(config.SemanticRetry.TriggerStatusCodes) != 2 {
		t.Errorf("Expected 2 trigger status codes, got %d", len(config.SemanticRetry.TriggerStatusCodes))
	}
	if config.SemanticRetry.TriggerStatusCodes[0] != 400 || config.SemanticRetry.TriggerStatusCodes[1] != 422 {
		t.Errorf("Expected TriggerStatusCodes [400, 422], got %v", config.SemanticRetry.TriggerStatusCodes)
	}
	if !config.SemanticRetry.EnableForIndependentSteps {
		t.Error("Expected SemanticRetry.EnableForIndependentSteps to default to true")
	}
}

// =============================================================================
// Integration: Orchestrator Wiring Tests
// =============================================================================

// TestOrchestratorWiresSemanticRetryForIndependentSteps verifies orchestrator configures executor
func TestOrchestratorWiresSemanticRetryForIndependentSteps(t *testing.T) {
	// This test verifies the wiring in NewAIOrchestrator
	// We check that the config value is properly passed to the executor

	// Test with default config (enabled)
	t.Run("default_enabled", func(t *testing.T) {
		os.Unsetenv("TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS")
		config := DefaultConfig()

		o := NewAIOrchestrator(config, nil, nil)

		// The executor should have semanticRetryForIndependentSteps=true
		if o.executor == nil {
			t.Fatal("Expected executor to be created")
		}
		if !o.executor.semanticRetryForIndependentSteps {
			t.Error("Expected executor.semanticRetryForIndependentSteps to be true with default config")
		}
	})

	// Test with disabled config
	t.Run("config_disabled", func(t *testing.T) {
		t.Setenv("TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS", "false")
		config := DefaultConfig()

		o := NewAIOrchestrator(config, nil, nil)

		if o.executor == nil {
			t.Fatal("Expected executor to be created")
		}
		if o.executor.semanticRetryForIndependentSteps {
			t.Error("Expected executor.semanticRetryForIndependentSteps to be false when config disabled")
		}
	})
}
