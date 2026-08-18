package gemini

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

// Factory creates Gemini AI clients.
type Factory struct{}

var _ ai.ValidatedProviderFactory = (*Factory)(nil)
var _ ai.RequestProviderFactory = (*Factory)(nil)

func (factory *Factory) Name() string { return "gemini" }

func (factory *Factory) Description() string {
	return "Google Gemini models with native GenerateContent API"
}

func (factory *Factory) Priority() int { return 800 }

// Create preserves ProviderFactory while routing configuration failures through
// the error-capable construction path used by the request-aware registry.
func (factory *Factory) Create(config *ai.AIConfig) core.AIClient {
	client, err := factory.CreateValidated(config)
	if err != nil {
		panic(fmt.Sprintf("create Gemini client: %v", err))
	}
	return client
}

func (factory *Factory) CreateValidated(config *ai.AIConfig) (core.AIClient, error) {
	return factory.createClient(config)
}

func (factory *Factory) CreateRequestClient(
	config *ai.AIConfig,
	integration ai.ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
	client, err := factory.createClient(config)
	if err != nil {
		return nil, err
	}
	engine, err := newRequestPolicyEngineWithIntegration(
		integration.RequestRules,
		integration.RequestMiddleware,
		integration.CompatibilityMode,
	)
	if err != nil {
		return nil, fmt.Errorf("configure Gemini request policy: %w", err)
	}
	client.requestPolicy = engine
	client.credentialSource = integration.CredentialSource
	client.endpointResolver = integration.EndpointResolver
	if integration.HTTPClient != nil {
		client.HTTPClient = providerHTTPClient(integration.HTTPClient)
	}
	return client, nil
}

func (factory *Factory) createClient(config *ai.AIConfig) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("gemini AI config is nil")
	}

	apiKey := config.APIKey
	if apiKey == "" {
		// This precedence is Google's documented behavior when both variables
		// are present.
		apiKey = os.Getenv("GOOGLE_API_KEY")
		if apiKey == "" {
			apiKey = os.Getenv("GEMINI_API_KEY")
		}
	}

	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = os.Getenv("GEMINI_BASE_URL")
		if baseURL == "" {
			baseURL = DefaultBaseURL
		}
	}
	if err := validateGeminiBaseURL(baseURL); err != nil {
		return nil, err
	}

	logger := configuredGeminiLogger(config.Logger)
	logger.Info("Gemini provider initialized", map[string]interface{}{
		"operation":       "ai_provider_init",
		"provider":        "gemini",
		"custom_endpoint": baseURL != DefaultBaseURL,
		"has_api_key":     apiKey != "",
		"timeout":         config.Timeout.String(),
		"max_retries":     config.MaxRetries,
		"model":           config.Model,
	})

	client := NewClient(apiKey, baseURL, logger)
	if err := applyGeminiClientConfig(client, config); err != nil {
		return nil, err
	}
	return client, nil
}

func configuredGeminiLogger(logger core.Logger) core.Logger {
	if logger == nil {
		return &core.NoOpLogger{}
	}
	if componentLogger, ok := logger.(core.ComponentAwareLogger); ok {
		return componentLogger.WithComponent("framework/ai")
	}
	return logger
}

func applyGeminiClientConfig(client *Client, config *ai.AIConfig) error {
	if config.Telemetry != nil {
		client.SetTelemetry(config.Telemetry)
	}
	if config.Timeout > 0 {
		client.HTTPClient.Timeout = config.Timeout
		client.requestTimeout = config.Timeout
	}
	if config.MaxRetries >= 0 {
		client.MaxRetries = config.MaxRetries
	}
	if config.Model != "" {
		client.DefaultModel = config.Model
	}
	if config.Temperature > 0 {
		client.DefaultTemperature = config.Temperature
	}
	if config.MaxTokens > 0 {
		client.DefaultMaxTokens = config.MaxTokens
	}
	if config.ReasoningEffort != "" {
		client.defaultReasoning = config.ReasoningEffort
	}
	client.defaultHeaders = providers.MergeStringMaps(nil, config.Headers)
	if len(config.Extra) > 0 {
		cloned, err := providers.CloneAIOptions(&core.AIOptions{Extra: config.Extra})
		if err != nil {
			return fmt.Errorf("clone Gemini default request extras: %w", err)
		}
		client.defaultExtra = cloned.Extra
	}
	return nil
}

func (factory *Factory) DetectEnvironment() (priority int, available bool) {
	if os.Getenv("GOOGLE_API_KEY") != "" || os.Getenv("GEMINI_API_KEY") != "" {
		return factory.Priority(), true
	}
	return 0, false
}
