package gemini

import (
	"os"
	"testing"
)

func TestResolveModel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"default", "gemini-2.5-flash"},
		{"fast", "gemini-2.5-flash-lite"},
		{"smart", "gemini-2.5-pro"},
		{"premium", "gemini-3-pro-preview"},
		{"code", "gemini-2.5-pro"},
		{"vision", "gemini-2.5-flash"},
		{"gemini-3-pro", "gemini-3-pro"},   // Pass-through
		{"unknown-model", "unknown-model"}, // Pass-through
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
	// Set env var override - override fast to use different model
	os.Setenv("TRUVAG3_GEMINI_MODEL_FAST", "gemini-2.0-flash")
	defer os.Unsetenv("TRUVAG3_GEMINI_MODEL_FAST")

	result := resolveModel("fast")
	expected := "gemini-2.0-flash" // Env override takes precedence over gemini-2.5-flash-lite
	if result != expected {
		t.Errorf("resolveModel with env override: got %q, want %q", result, expected)
	}
}

func TestResolveModelEnvOverrideTakesPriority(t *testing.T) {
	// Set env var override - should override the hardcoded alias
	os.Setenv("TRUVAG3_GEMINI_MODEL_SMART", "gemini-2.0-pro")
	defer os.Unsetenv("TRUVAG3_GEMINI_MODEL_SMART")

	result := resolveModel("smart")
	expected := "gemini-2.0-pro"
	if result != expected {
		t.Errorf("resolveModel env override should take priority: got %q, want %q", result, expected)
	}
}

func TestResolveModelEnvOverrideForUnknownAlias(t *testing.T) {
	// Set env var for a non-standard alias
	os.Setenv("TRUVAG3_GEMINI_MODEL_CUSTOM", "gemini-custom-model")
	defer os.Unsetenv("TRUVAG3_GEMINI_MODEL_CUSTOM")

	result := resolveModel("custom")
	expected := "gemini-custom-model"
	if result != expected {
		t.Errorf("resolveModel with custom env override: got %q, want %q", result, expected)
	}
}
