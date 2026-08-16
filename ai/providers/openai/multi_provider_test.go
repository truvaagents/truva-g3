package openai

import (
	"os"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

// ================================
// Phase 1 Tests: Environment Mutation Fix + Configuration Hierarchy
// ================================

// TestPhase1_DetectEnvironmentNoMutation verifies the critical bug fix:
// DetectEnvironment() should NEVER mutate global environment variables
func TestPhase1_DetectEnvironmentNoMutation(t *testing.T) {
	// Save original environment
	originalVars := saveEnvironment()
	defer restoreEnvironment(originalVars)

	// Set a known value
	os.Setenv("OPENAI_API_KEY", "sk-test-original")
	os.Setenv("GROQ_API_KEY", "")

	factory := &Factory{}

	// Run detection 50 times to ensure stability
	for i := 0; i < 50; i++ {
		priority, available := factory.DetectEnvironment()

		// Verify detection works
		if !available {
			t.Errorf("Iteration %d: Expected environment to be available", i)
		}
		if priority != 1000 {
			t.Errorf("Iteration %d: Expected priority 1000 for OpenAI, got %d", i, priority)
		}

		// CRITICAL: Verify environment was NOT mutated
		if os.Getenv("OPENAI_API_KEY") != "sk-test-original" {
			t.Fatalf("Iteration %d: DetectEnvironment() mutated OPENAI_API_KEY - critical bug!", i)
		}
		if os.Getenv("GROQ_API_KEY") != "" {
			t.Fatalf("Iteration %d: DetectEnvironment() mutated GROQ_API_KEY - critical bug!", i)
		}
	}
}

// TestPhase1_ConfigurationPrecedence verifies the three-tier hierarchy
// Tier 1: Explicit config (highest)
// Tier 2: Environment variables (medium)
// Tier 3: Hardcoded defaults (lowest)
func TestPhase1_ConfigurationPrecedence(t *testing.T) {
	tests := []struct {
		name            string
		providerAlias   string
		explicitAPIKey  string
		explicitBaseURL string
		envAPIKey       string
		envBaseURL      string
		expectedAPIKey  string
		expectedBaseURL string
		description     string
	}{
		{
			name:            "Tier 1 wins: Explicit overrides everything",
			providerAlias:   "openai.deepseek",
			explicitAPIKey:  "explicit-key",
			explicitBaseURL: "https://explicit.url",
			envAPIKey:       "env-key",
			envBaseURL:      "https://env.url",
			expectedAPIKey:  "explicit-key",
			expectedBaseURL: "https://explicit.url",
			description:     "Explicit config should override env vars and defaults",
		},
		{
			name:            "Tier 2 wins: Env overrides defaults",
			providerAlias:   "openai.deepseek",
			explicitAPIKey:  "",
			explicitBaseURL: "",
			envAPIKey:       "env-key",
			envBaseURL:      "https://env.url",
			expectedAPIKey:  "env-key",
			expectedBaseURL: "https://env.url",
			description:     "Env vars should override hardcoded defaults",
		},
		{
			name:            "Tier 3: Defaults when no explicit or env",
			providerAlias:   "openai.deepseek",
			explicitAPIKey:  "",
			explicitBaseURL: "",
			envAPIKey:       "",
			envBaseURL:      "",
			expectedAPIKey:  "",
			expectedBaseURL: "https://api.deepseek.com",
			description:     "Should fall back to hardcoded defaults",
		},
		{
			name:            "Mixed: Explicit API key, env URL",
			providerAlias:   "openai.groq",
			explicitAPIKey:  "explicit-key",
			explicitBaseURL: "",
			envAPIKey:       "env-key",
			envBaseURL:      "https://env.url",
			expectedAPIKey:  "explicit-key",
			expectedBaseURL: "https://env.url",
			description:     "Each field independently follows precedence",
		},
		{
			name:            "Mixed: Env API key, explicit URL",
			providerAlias:   "openai.groq",
			explicitAPIKey:  "",
			explicitBaseURL: "https://explicit.url",
			envAPIKey:       "env-key",
			envBaseURL:      "https://env.url",
			expectedAPIKey:  "env-key",
			expectedBaseURL: "https://explicit.url",
			description:     "Each field independently follows precedence",
		},
		{
			name:            "Runtime URL override use case",
			providerAlias:   "openai.deepseek",
			explicitAPIKey:  "",
			explicitBaseURL: "",
			envAPIKey:       "env-key",
			envBaseURL:      "https://eu-central.deepseek.com",
			expectedAPIKey:  "env-key",
			expectedBaseURL: "https://eu-central.deepseek.com",
			description:     "Supports runtime URL changes via env vars",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean environment
			originalVars := saveEnvironment()
			defer restoreEnvironment(originalVars)
			clearAllProviderEnvVars()

			// Set environment variables based on provider
			setProviderEnvVars(tt.providerAlias, tt.envAPIKey, tt.envBaseURL)

			config := &ai.AIConfig{
				ProviderAlias: tt.providerAlias,
				APIKey:        tt.explicitAPIKey,
				BaseURL:       tt.explicitBaseURL,
			}

			factory := &Factory{}
			apiKey, baseURL := factory.resolveCredentials(config)

			if apiKey != tt.expectedAPIKey {
				t.Errorf("%s\nExpected API key %q, got %q", tt.description, tt.expectedAPIKey, apiKey)
			}
			if baseURL != tt.expectedBaseURL {
				t.Errorf("%s\nExpected base URL %q, got %q", tt.description, tt.expectedBaseURL, baseURL)
			}
		})
	}
}

// TestPhase1_AllProvidersConfiguration verifies all provider aliases work
func TestPhase1_AllProvidersConfiguration(t *testing.T) {
	tests := []struct {
		providerAlias string
		envKeyName    string
		envURLName    string
		defaultURL    string
	}{
		{"openai.deepseek", "DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL", "https://api.deepseek.com"},
		{"openai.groq", "GROQ_API_KEY", "GROQ_BASE_URL", "https://api.groq.com/openai/v1"},
		{"openai.xai", "XAI_API_KEY", "XAI_BASE_URL", "https://api.x.ai/v1"},
		{"openai.mistral", "MISTRAL_API_KEY", "MISTRAL_BASE_URL", "https://api.mistral.ai/v1"},
		{"openai.qwen", "QWEN_API_KEY", "QWEN_BASE_URL", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
		{"openai.together", "TOGETHER_API_KEY", "TOGETHER_BASE_URL", "https://api.together.xyz/v1"},
		{"openai.ollama", "", "OLLAMA_BASE_URL", "http://localhost:11434/v1"},
	}

	for _, tt := range tests {
		t.Run(tt.providerAlias, func(t *testing.T) {
			originalVars := saveEnvironment()
			defer restoreEnvironment(originalVars)
			clearAllProviderEnvVars()

			// Test 1: With environment variables
			if tt.envKeyName != "" {
				os.Setenv(tt.envKeyName, "test-key")
			}
			os.Setenv(tt.envURLName, "https://test.url")

			config := &ai.AIConfig{
				ProviderAlias: tt.providerAlias,
			}

			factory := &Factory{}
			apiKey, baseURL := factory.resolveCredentials(config)

			if tt.envKeyName != "" && apiKey != "test-key" {
				t.Errorf("With env vars: Expected API key 'test-key', got %q", apiKey)
			}
			if baseURL != "https://test.url" {
				t.Errorf("With env vars: Expected base URL 'https://test.url', got %q", baseURL)
			}

			// Test 2: With defaults only
			clearAllProviderEnvVars()
			apiKey, baseURL = factory.resolveCredentials(config)
			if baseURL != tt.defaultURL {
				t.Errorf("With defaults: Expected URL %q, got %q", tt.defaultURL, baseURL)
			}
		})
	}
}

// TestPhase1_AutoDetectionBackwardCompatibility verifies zero-config still works
func TestPhase1_AutoDetectionBackwardCompatibility(t *testing.T) {
	tests := []struct {
		name             string
		envVars          map[string]string
		expectedPriority int
		expectedAPIKey   string
		expectedBaseURL  string
	}{
		{
			name: "OpenAI has highest priority",
			envVars: map[string]string{
				"OPENAI_API_KEY":   "sk-openai",
				"GROQ_API_KEY":     "gsk-groq",
				"DEEPSEEK_API_KEY": "sk-deepseek",
			},
			expectedPriority: 1000,
			expectedAPIKey:   "sk-openai",
			expectedBaseURL:  "https://api.openai.com/v1",
		},
		{
			name: "Groq when no OpenAI",
			envVars: map[string]string{
				"GROQ_API_KEY":     "gsk-groq",
				"DEEPSEEK_API_KEY": "sk-deepseek",
			},
			expectedPriority: 700,
			expectedAPIKey:   "gsk-groq",
			expectedBaseURL:  "https://api.groq.com/openai/v1",
		},
		{
			name: "DeepSeek when no OpenAI/Groq",
			envVars: map[string]string{
				"DEEPSEEK_API_KEY": "sk-deepseek",
				"XAI_API_KEY":      "sk-xai",
			},
			expectedPriority: 600,
			expectedAPIKey:   "sk-deepseek",
			expectedBaseURL:  "https://api.deepseek.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalVars := saveEnvironment()
			defer restoreEnvironment(originalVars)
			clearAllProviderEnvVars()

			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}

			factory := &Factory{}

			// Test DetectEnvironment
			priority, available := factory.DetectEnvironment()
			if !available {
				t.Error("Expected environment to be available")
			}
			if priority != tt.expectedPriority {
				t.Errorf("Expected priority %d, got %d", tt.expectedPriority, priority)
			}

			// Test resolveCredentials with empty ProviderAlias (auto-detect mode)
			config := &ai.AIConfig{
				ProviderAlias: "",
			}
			apiKey, baseURL := factory.resolveCredentials(config)

			if apiKey != tt.expectedAPIKey {
				t.Errorf("Expected API key %q, got %q", tt.expectedAPIKey, apiKey)
			}
			if baseURL != tt.expectedBaseURL {
				t.Errorf("Expected base URL %q, got %q", tt.expectedBaseURL, baseURL)
			}
		})
	}
}

// ================================
// Phase 2 Tests: Provider Aliases + Model Resolution
// ================================

// TestPhase2_ProviderAliasConfiguration verifies WithProviderAlias option
func TestPhase2_ProviderAliasConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		alias         string
		envVars       map[string]string
		expectedBase  string
		expectedAlias string
	}{
		{
			name:  "DeepSeek alias with env vars",
			alias: "openai.deepseek",
			envVars: map[string]string{
				"DEEPSEEK_API_KEY":  "sk-deepseek-test",
				"DEEPSEEK_BASE_URL": "https://test.deepseek.com",
			},
			expectedBase:  "openai",
			expectedAlias: "openai.deepseek",
		},
		{
			name:  "Groq alias with env vars",
			alias: "openai.groq",
			envVars: map[string]string{
				"GROQ_API_KEY":  "gsk-groq-test",
				"GROQ_BASE_URL": "https://test.groq.com",
			},
			expectedBase:  "openai",
			expectedAlias: "openai.groq",
		},
		{
			name:          "xAI alias",
			alias:         "openai.xai",
			envVars:       map[string]string{"XAI_API_KEY": "sk-xai-test"},
			expectedBase:  "openai",
			expectedAlias: "openai.xai",
		},
		{
			name:          "Together alias",
			alias:         "openai.together",
			envVars:       map[string]string{"TOGETHER_API_KEY": "sk-together-test"},
			expectedBase:  "openai",
			expectedAlias: "openai.together",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalVars := saveEnvironment()
			defer restoreEnvironment(originalVars)
			clearAllProviderEnvVars()

			// Set environment variables
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}

			// Create config using WithProviderAlias
			config := &ai.AIConfig{}
			option := ai.WithProviderAlias(tt.alias)
			option(config)

			if config.Provider != tt.expectedBase {
				t.Errorf("Expected provider %q, got %q", tt.expectedBase, config.Provider)
			}
			if config.ProviderAlias != tt.expectedAlias {
				t.Errorf("Expected alias %q, got %q", tt.expectedAlias, config.ProviderAlias)
			}

			// WithProviderAlias now only sets Provider and ProviderAlias.
			// Credential resolution (APIKey, BaseURL) is handled by the factory's
			// resolveCredentials() method using the subProviders table.
			if config.APIKey != "" {
				t.Errorf("Expected APIKey to NOT be set by WithProviderAlias (factory handles it), got %q", config.APIKey)
			}
		})
	}
}

// TestPhase2_ModelAliasResolution verifies portable model names
func TestPhase2_ModelAliasResolution(t *testing.T) {
	tests := []struct {
		providerAlias string
		inputModel    string
		expectedModel string
		description   string
	}{
		// OpenAI aliases - GPT-5.6 family (August 2026)
		{"openai", "default", "gpt-5.6-terra", "OpenAI default model"},
		{"openai", "fast", "gpt-5.6-luna", "OpenAI fast model"},
		{"openai", "smart", "gpt-5.6-sol", "OpenAI smart model"},
		{"openai", "premium", "gpt-5.6-sol", "OpenAI premium model"},
		{"openai", "code", "gpt-5.6-sol", "OpenAI code model"},
		{"openai", "vision", "gpt-4.1", "OpenAI vision model"},

		// DeepSeek aliases - V3.2 family
		{"openai.deepseek", "fast", "deepseek-chat", "DeepSeek fast model"},
		{"openai.deepseek", "smart", "deepseek-reasoner", "DeepSeek smart model"},
		{"openai.deepseek", "code", "deepseek-chat", "DeepSeek code model"},

		// Groq aliases - gpt-oss family for smart; Llama for fast
		{"openai.groq", "fast", "llama-3.1-8b-instant", "Groq fast model"},
		{"openai.groq", "smart", "openai/gpt-oss-120b", "Groq smart model"},

		// Together AI aliases - Llama models
		{"openai.together", "fast", "meta-llama/Llama-3.1-8B-Instruct-Turbo", "Together fast model"},
		{"openai.together", "smart", "meta-llama/Llama-3.3-70B-Instruct-Turbo", "Together smart model"},
		{"openai.together", "code", "Qwen/Qwen2.5-Coder-32B-Instruct", "Together code model"},

		// xAI aliases - Grok 2/3 family
		{"openai.xai", "fast", "grok-2", "xAI fast model"},
		{"openai.xai", "smart", "grok-3-beta", "xAI smart model"},
		{"openai.xai", "code", "grok-3-mini-beta", "xAI code model"},

		// Qwen aliases
		{"openai.qwen", "fast", "qwen-turbo", "Qwen fast model"},
		{"openai.qwen", "smart", "qwen-max", "Qwen smart model"},
		{"openai.qwen", "code", "qwen3-coder-plus", "Qwen code model"},

		// Pass-through (not an alias)
		{"openai", "gpt-4.1-nano", "gpt-4.1-nano", "Non-alias pass-through"},
		{"openai.deepseek", "deepseek-chat", "deepseek-chat", "Explicit model pass-through"},

		// Empty alias defaults to openai
		{"", "smart", "gpt-5.6-sol", "Empty alias defaults to openai"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			result := ResolveModel(tt.providerAlias, tt.inputModel)
			if result != tt.expectedModel {
				t.Errorf("ResolveModel(%q, %q) = %q, want %q",
					tt.providerAlias, tt.inputModel, result, tt.expectedModel)
			}
		})
	}
}

// TestPhase2_MultiProviderCoexistence verifies multiple providers work simultaneously
func TestPhase2_MultiProviderCoexistence(t *testing.T) {
	originalVars := saveEnvironment()
	defer restoreEnvironment(originalVars)
	clearAllProviderEnvVars()

	// Set up environment for multiple providers
	os.Setenv("OPENAI_API_KEY", "sk-openai-test")
	os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-test")
	os.Setenv("GROQ_API_KEY", "gsk-groq-test")

	factory := &Factory{}

	// Create three different clients simultaneously
	configs := []struct {
		alias           string
		expectedAPIKey  string
		expectedBaseURL string
	}{
		{"openai", "sk-openai-test", "https://api.openai.com/v1"},
		{"openai.deepseek", "sk-deepseek-test", "https://api.deepseek.com"},
		{"openai.groq", "gsk-groq-test", "https://api.groq.com/openai/v1"},
	}

	for _, cfg := range configs {
		t.Run(cfg.alias, func(t *testing.T) {
			config := &ai.AIConfig{
				ProviderAlias: cfg.alias,
			}

			apiKey, baseURL := factory.resolveCredentials(config)

			if apiKey != cfg.expectedAPIKey {
				t.Errorf("Provider %s: Expected API key %q, got %q",
					cfg.alias, cfg.expectedAPIKey, apiKey)
			}
			if baseURL != cfg.expectedBaseURL {
				t.Errorf("Provider %s: Expected base URL %q, got %q",
					cfg.alias, cfg.expectedBaseURL, baseURL)
			}
		})
	}

	// Verify environment wasn't corrupted
	if os.Getenv("OPENAI_API_KEY") != "sk-openai-test" {
		t.Error("Environment corrupted: OPENAI_API_KEY changed")
	}
	if os.Getenv("DEEPSEEK_API_KEY") != "sk-deepseek-test" {
		t.Error("Environment corrupted: DEEPSEEK_API_KEY changed")
	}
	if os.Getenv("GROQ_API_KEY") != "gsk-groq-test" {
		t.Error("Environment corrupted: GROQ_API_KEY changed")
	}
}

// TestPhase2_ModelResolutionIntegration verifies client stores providerAlias for request-time resolution
func TestPhase2_ModelResolutionIntegration(t *testing.T) {
	originalVars := saveEnvironment()
	defer restoreEnvironment(originalVars)
	clearAllProviderEnvVars()

	os.Setenv("DEEPSEEK_API_KEY", "sk-test")

	factory := &Factory{}
	config := &ai.AIConfig{
		ProviderAlias: "openai.deepseek",
		Model:         "smart", // Will be resolved at request-time to "deepseek-reasoner"
		Logger:        &core.NoOpLogger{},
	}

	client := factory.Create(config)
	openaiClient, ok := client.(*Client)
	if !ok {
		t.Fatal("Expected *Client type")
	}

	// Verify alias is stored as DefaultModel (request-time resolution)
	if openaiClient.DefaultModel != "smart" {
		t.Errorf("Expected DefaultModel to store alias 'smart', got %q", openaiClient.DefaultModel)
	}

	// Verify providerAlias is stored for request-time resolution
	if openaiClient.providerAlias != "openai.deepseek" {
		t.Errorf("Expected providerAlias 'openai.deepseek', got %q", openaiClient.providerAlias)
	}

	// Verify ResolveModel correctly resolves at request-time
	resolved := ResolveModel(openaiClient.providerAlias, openaiClient.DefaultModel)
	if resolved != "deepseek-reasoner" {
		t.Errorf("Expected resolved model 'deepseek-reasoner', got %q", resolved)
	}
}

// ================================
// Helper Functions
// ================================

// saveEnvironment saves all provider-related environment variables
func saveEnvironment() map[string]string {
	vars := []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL",
		"GROQ_API_KEY", "GROQ_BASE_URL",
		"DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL",
		"XAI_API_KEY", "XAI_BASE_URL",
		"MISTRAL_API_KEY", "MISTRAL_BASE_URL",
		"QWEN_API_KEY", "QWEN_BASE_URL",
		"TOGETHER_API_KEY", "TOGETHER_BASE_URL",
		"OLLAMA_BASE_URL",
	}
	saved := make(map[string]string)
	for _, v := range vars {
		saved[v] = os.Getenv(v)
	}
	return saved
}

// restoreEnvironment restores previously saved environment variables
func restoreEnvironment(saved map[string]string) {
	for k, v := range saved {
		if v == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
}

// clearAllProviderEnvVars clears all provider-related environment variables
func clearAllProviderEnvVars() {
	vars := []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL",
		"GROQ_API_KEY", "GROQ_BASE_URL",
		"DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL",
		"XAI_API_KEY", "XAI_BASE_URL",
		"MISTRAL_API_KEY", "MISTRAL_BASE_URL",
		"QWEN_API_KEY", "QWEN_BASE_URL",
		"TOGETHER_API_KEY", "TOGETHER_BASE_URL",
		"OLLAMA_BASE_URL",
	}
	for _, v := range vars {
		os.Unsetenv(v)
	}
}

// setProviderEnvVars sets environment variables for a specific provider alias
func setProviderEnvVars(alias, apiKey, baseURL string) {
	switch alias {
	case "openai.deepseek":
		if apiKey != "" {
			os.Setenv("DEEPSEEK_API_KEY", apiKey)
		}
		if baseURL != "" {
			os.Setenv("DEEPSEEK_BASE_URL", baseURL)
		}
	case "openai.groq":
		if apiKey != "" {
			os.Setenv("GROQ_API_KEY", apiKey)
		}
		if baseURL != "" {
			os.Setenv("GROQ_BASE_URL", baseURL)
		}
	case "openai.xai":
		if apiKey != "" {
			os.Setenv("XAI_API_KEY", apiKey)
		}
		if baseURL != "" {
			os.Setenv("XAI_BASE_URL", baseURL)
		}
	case "openai.qwen":
		if apiKey != "" {
			os.Setenv("QWEN_API_KEY", apiKey)
		}
		if baseURL != "" {
			os.Setenv("QWEN_BASE_URL", baseURL)
		}
	case "openai.together":
		if apiKey != "" {
			os.Setenv("TOGETHER_API_KEY", apiKey)
		}
		if baseURL != "" {
			os.Setenv("TOGETHER_BASE_URL", baseURL)
		}
	case "openai.ollama":
		if baseURL != "" {
			os.Setenv("OLLAMA_BASE_URL", baseURL)
		}
	case "openai":
		if apiKey != "" {
			os.Setenv("OPENAI_API_KEY", apiKey)
		}
		if baseURL != "" {
			os.Setenv("OPENAI_BASE_URL", baseURL)
		}
	}
}
