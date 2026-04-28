package gemini

import (
	"os"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

func init() {
	ai.MustRegister(&Factory{})
}

// Factory creates Gemini AI clients
type Factory struct{}

// Name returns the provider name
func (f *Factory) Name() string {
	return "gemini"
}

// Description returns provider description
func (f *Factory) Description() string {
	return "Google Gemini models with native GenerateContent API"
}

// Priority returns provider priority
func (f *Factory) Priority() int {
	return 800 // Third after OpenAI and Anthropic, higher than OpenAI sub-providers
}

// Create creates a new Gemini client
func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
	// Get API key from config or environment
	apiKey := config.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			// Also check for GOOGLE_API_KEY as an alternative
			apiKey = os.Getenv("GOOGLE_API_KEY")
		}
	}

	// Use base URL from config or environment, with default
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = os.Getenv("GEMINI_BASE_URL")
		if baseURL == "" {
			baseURL = DefaultBaseURL
		}
	}

	// Get logger from config with proper component wrapping
	logger := config.Logger
	if logger == nil {
		logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/ai")
	}

	// Log provider initialization
	logger.Info("Gemini provider initialized", map[string]interface{}{
		"operation":   "ai_provider_init",
		"provider":    "gemini",
		"base_url":    baseURL,
		"has_api_key": apiKey != "",
		"model":       config.Model,
	})

	// Create the client with full configuration
	client := NewClient(apiKey, baseURL, logger)

	// Set telemetry for distributed tracing
	if config.Telemetry != nil {
		client.SetTelemetry(config.Telemetry)
	}

	// Apply timeout if specified
	if config.Timeout > 0 {
		client.HTTPClient.Timeout = config.Timeout
	}

	// Apply retry configuration. Honors 0 ("no retries") as a valid setting —
	// the AIConfig defaults to a sentinel before NewClient resolves it via
	// option / env var / default precedence, so any non-negative value here
	// means "operator chose this on purpose".
	if config.MaxRetries >= 0 {
		client.MaxRetries = config.MaxRetries
	}

	// Apply model defaults
	if config.Model != "" {
		client.DefaultModel = config.Model
	}

	// Apply temperature default
	if config.Temperature > 0 {
		client.DefaultTemperature = config.Temperature
	}

	// Apply max tokens default
	if config.MaxTokens > 0 {
		client.DefaultMaxTokens = config.MaxTokens
	}

	if len(config.Headers) > 0 {
		client.defaultHeaders = providers.MergeStringMaps(nil, config.Headers)
	}
	if len(config.Extra) > 0 {
		client.defaultExtra = providers.MergeAnyMaps(nil, config.Extra)
	}

	return client
}

// DetectEnvironment checks if Gemini is configured and returns priority
func (f *Factory) DetectEnvironment() (priority int, available bool) {
	if os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != "" {
		return f.Priority(), true
	}
	return 0, false
}
