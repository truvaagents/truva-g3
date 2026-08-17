package anthropic

import (
	"os"
	"testing"
)

func TestResolveModel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"default", "claude-sonnet-5"},
		{"smart", "claude-opus-5"},
		{"fast", "claude-haiku-4-5"},
		{"code", "claude-opus-5"},
		{"vision", "claude-opus-5"},
		{"premium", "claude-fable-5"},
		{"claude-opus-4-5-20251101", "claude-opus-4-5-20251101"}, // Pass-through
		{"unknown-alias", "unknown-alias"},                       // Pass-through
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := resolveModel(tt.input)
			if result != tt.expected {
				t.Errorf("resolveModel(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestResolveModelEnvOverride(t *testing.T) {
	// Set env var override
	os.Setenv("TRUVAG3_ANTHROPIC_MODEL_SMART", "claude-3-opus-20240229")
	defer os.Unsetenv("TRUVAG3_ANTHROPIC_MODEL_SMART")

	result := resolveModel("smart")
	expected := "claude-3-opus-20240229"
	if result != expected {
		t.Errorf("resolveModel with env override: got %q, want %q", result, expected)
	}
}

func TestResolveModelEnvOverrideTakesPriority(t *testing.T) {
	// Set env var override - should override the hardcoded alias
	os.Setenv("TRUVAG3_ANTHROPIC_MODEL_FAST", "claude-3-opus-20240229")
	defer os.Unsetenv("TRUVAG3_ANTHROPIC_MODEL_FAST")

	result := resolveModel("fast")
	expected := "claude-3-opus-20240229"
	if result != expected {
		t.Errorf("resolveModel env override should take priority: got %q, want %q", result, expected)
	}
}

func TestResolveModelEnvOverrideForUnknownAlias(t *testing.T) {
	// Set env var for a non-standard alias
	os.Setenv("TRUVAG3_ANTHROPIC_MODEL_CUSTOM", "claude-custom-model")
	defer os.Unsetenv("TRUVAG3_ANTHROPIC_MODEL_CUSTOM")

	result := resolveModel("custom")
	expected := "claude-custom-model"
	if result != expected {
		t.Errorf("resolveModel with custom env override: got %q, want %q", result, expected)
	}
}
