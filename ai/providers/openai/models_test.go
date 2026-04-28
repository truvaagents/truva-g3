package openai

import (
	"os"
	"testing"
)

func TestResolveModel(t *testing.T) {
	tests := []struct {
		name          string
		providerAlias string
		model         string
		expected      string
	}{
		// Vanilla OpenAI - GPT-4.1 and O-series (December 2025)
		{"openai fast", "openai", "fast", "gpt-4.1-mini"},
		{"openai smart", "openai", "smart", "o3"},
		{"openai code", "openai", "code", "o3"},
		{"openai vision", "openai", "vision", "gpt-4.1"},
		{"openai pass-through", "openai", "gpt-4.1-nano", "gpt-4.1-nano"},

		// Empty provider alias defaults to openai
		{"empty alias fast", "", "fast", "gpt-4.1-mini"},
		{"empty alias smart", "", "smart", "o3"},

		// DeepSeek - V3.2 family
		{"deepseek fast", "openai.deepseek", "fast", "deepseek-chat"},
		{"deepseek smart", "openai.deepseek", "smart", "deepseek-reasoner"},
		{"deepseek code", "openai.deepseek", "code", "deepseek-chat"},
		{"deepseek pass-through", "openai.deepseek", "deepseek-v3.2", "deepseek-v3.2"},

		// Groq - Llama models
		{"groq fast", "openai.groq", "fast", "llama-3.1-8b-instant"},
		{"groq smart", "openai.groq", "smart", "llama-3.3-70b-versatile"},
		{"groq code", "openai.groq", "code", "llama-3.3-70b-versatile"},

		// Together - Llama models
		{"together fast", "openai.together", "fast", "meta-llama/Llama-3.1-8B-Instruct-Turbo"},
		{"together smart", "openai.together", "smart", "meta-llama/Llama-3.3-70B-Instruct-Turbo"},

		// xAI - Grok 2/3 family
		{"xai fast", "openai.xai", "fast", "grok-2"},
		{"xai smart", "openai.xai", "smart", "grok-3-beta"},
		{"xai code", "openai.xai", "code", "grok-3-mini-beta"},
		{"xai vision", "openai.xai", "vision", "grok-2-vision-latest"},

		// Qwen - Alibaba models
		{"qwen fast", "openai.qwen", "fast", "qwen-turbo"},
		{"qwen smart", "openai.qwen", "smart", "qwen-max"},
		{"qwen code", "openai.qwen", "code", "qwen3-coder-plus"},

		// Ollama - Local models
		{"ollama fast", "openai.ollama", "fast", "llama3.2:1b"},
		{"ollama smart", "openai.ollama", "smart", "llama3.2"},
		{"ollama code", "openai.ollama", "code", "codellama"},
		{"ollama default", "openai.ollama", "default", "llama3.2"},

		// Unknown provider - pass-through
		{"unknown provider", "openai.unknown", "smart", "smart"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveModel(tt.providerAlias, tt.model)
			if result != tt.expected {
				t.Errorf("ResolveModel(%q, %q) = %q, want %q",
					tt.providerAlias, tt.model, result, tt.expected)
			}
		})
	}
}

func TestResolveModelEnvOverride(t *testing.T) {
	tests := []struct {
		name          string
		providerAlias string
		model         string
		envKey        string
		envValue      string
		expected      string
	}{
		{
			name:          "OpenAI env override",
			providerAlias: "openai",
			model:         "smart",
			envKey:        "TRUVAG3_OPENAI_MODEL_SMART",
			envValue:      "gpt-4-turbo",
			expected:      "gpt-4-turbo",
		},
		{
			name:          "DeepSeek env override",
			providerAlias: "openai.deepseek",
			model:         "code",
			envKey:        "TRUVAG3_DEEPSEEK_MODEL_CODE",
			envValue:      "deepseek-coder-v2",
			expected:      "deepseek-coder-v2",
		},
		{
			name:          "Groq env override",
			providerAlias: "openai.groq",
			model:         "fast",
			envKey:        "TRUVAG3_GROQ_MODEL_FAST",
			envValue:      "llama-3.2-90b",
			expected:      "llama-3.2-90b",
		},
		{
			name:          "xAI env override",
			providerAlias: "openai.xai",
			model:         "smart",
			envKey:        "TRUVAG3_XAI_MODEL_SMART",
			envValue:      "grok-3",
			expected:      "grok-3",
		},
		{
			name:          "Custom alias via env",
			providerAlias: "openai",
			model:         "experimental",
			envKey:        "TRUVAG3_OPENAI_MODEL_EXPERIMENTAL",
			envValue:      "gpt-5-preview",
			expected:      "gpt-5-preview",
		},
		{
			name:          "Ollama env override",
			providerAlias: "openai.ollama",
			model:         "default",
			envKey:        "TRUVAG3_OLLAMA_MODEL_DEFAULT",
			envValue:      "mistral:7b",
			expected:      "mistral:7b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv(tt.envKey, tt.envValue)
			defer os.Unsetenv(tt.envKey)

			result := ResolveModel(tt.providerAlias, tt.model)
			if result != tt.expected {
				t.Errorf("ResolveModel(%q, %q) with env = %q, want %q",
					tt.providerAlias, tt.model, result, tt.expected)
			}
		})
	}
}

func TestResolveModelEnvOverrideTakesPriority(t *testing.T) {
	// Set env var override - should override the hardcoded alias
	os.Setenv("TRUVAG3_OPENAI_MODEL_FAST", "gpt-4-turbo")
	defer os.Unsetenv("TRUVAG3_OPENAI_MODEL_FAST")

	result := ResolveModel("openai", "fast")
	expected := "gpt-4-turbo"
	if result != expected {
		t.Errorf("env override should take priority over hardcoded alias: got %q, want %q", result, expected)
	}
}
