package anthropic

import (
	"fmt"
	"os"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

func init() {
	ai.MustRegister(&Factory{})
}

// Factory creates Anthropic AI clients
type Factory struct{}

var _ ai.ValidatedProviderFactory = (*Factory)(nil)
var _ ai.RequestProviderFactory = (*Factory)(nil)

// Name returns the provider name
func (f *Factory) Name() string {
	return "anthropic"
}

// Description returns provider description
func (f *Factory) Description() string {
	return "Anthropic Claude models with native Messages API"
}

// Priority returns provider priority
func (f *Factory) Priority() int {
	return 900 // Second to OpenAI, higher than Gemini and OpenAI sub-providers
}

// Create creates a new Anthropic client
func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
	client, err := f.createClient(config)
	if err != nil {
		panic(fmt.Sprintf("create Anthropic client: %v", err))
	}
	return client
}

// CreateValidated creates a legacy Anthropic client with error-capable
// configuration validation.
func (f *Factory) CreateValidated(config *ai.AIConfig) (core.AIClient, error) {
	return f.createClient(config)
}

// CreateRequestClient creates an Anthropic request client with application
// policy rules, middleware, and compatibility behavior.
func (f *Factory) CreateRequestClient(
	config *ai.AIConfig,
	integration ai.ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
	client, err := f.createClient(config)
	if err != nil {
		return nil, err
	}
	engine, err := newRequestPolicyEngineWithIntegration(
		integration.RequestRules,
		integration.RequestMiddleware,
		integration.CompatibilityMode,
	)
	if err != nil {
		return nil, fmt.Errorf("configure Anthropic request policy: %w", err)
	}
	client.requestPolicy = engine
	return client, nil
}

func (f *Factory) createClient(config *ai.AIConfig) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("anthropic AI config is nil")
	}
	// Get API key from config or environment
	apiKey := config.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	// Use base URL from config or environment, with default
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = os.Getenv("ANTHROPIC_BASE_URL")
		if baseURL == "" {
			baseURL = DefaultBaseURL
		}
	}

	logger := config.Logger
	if logger == nil {
		logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/ai")
	}

	logger.Info("Anthropic provider initialized", map[string]interface{}{
		"operation":   "ai_provider_init",
		"provider":    "anthropic",
		"base_url":    baseURL,
		"has_api_key": apiKey != "",
		"timeout":     config.Timeout.String(),
		"max_retries": config.MaxRetries,
		"model":       config.Model,
	})

	// Create the client with full configuration
	client := NewClient(apiKey, baseURL, logger)
	if config.ProviderAlias != "" {
		client.providerAlias = config.ProviderAlias
	}

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
		cloned, err := providers.CloneAIOptions(&core.AIOptions{Extra: config.Extra})
		if err != nil {
			return nil, fmt.Errorf("clone Anthropic default request extras: %w", err)
		}
		client.defaultExtra = cloned.Extra
	}

	return client, nil
}

// DetectEnvironment checks if Anthropic is configured and returns priority
func (f *Factory) DetectEnvironment() (priority int, available bool) {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return f.Priority(), true
	}
	return 0, false
}
