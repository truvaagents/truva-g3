package ai

import (
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Provider represents an AI provider type
type Provider string

// Standard provider constants
const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderGemini    Provider = "gemini"
	ProviderOllama    Provider = "ollama"
	ProviderAuto      Provider = "auto"   // Auto-detect from environment
	ProviderCustom    Provider = "custom" // For custom providers
)

// AIConfig holds configuration for AI client creation
type AIConfig struct {
	// Provider to use
	Provider string

	// ProviderAlias for OpenAI-compatible services (Phase 2)
	// Examples: "openai.deepseek", "openai.groq", "openai.together"
	// This enables multiple OpenAI-compatible providers to coexist without conflicts
	ProviderAlias string

	// API credentials
	APIKey  string
	BaseURL string

	// Connection settings
	Timeout    time.Duration
	MaxRetries int

	// Model configuration
	Model       string
	Temperature float32
	MaxTokens   int

	// ReasoningTokenMultiplier is the factor by which max_tokens is increased for
	// reasoning models (GPT-5, o1, o3, o4). These models count internal chain-of-thought
	// tokens against max_completion_tokens but don't return them, causing empty responses
	// if not enough tokens are allocated. Default is 5 (set in openai/reasoning.go).
	// Set to 0 to use the default, or specify a custom value (e.g., 3 for cost optimization).
	ReasoningTokenMultiplier int

	// ReasoningEffort controls the reasoning effort level for OpenAI reasoning models.
	// Valid values: "none", "low", "medium", "high", "xhigh" (model-dependent).
	// GPT-5.2+ defaults to "none" (no extended thinking). Earlier models (o1, o3, o4)
	// default to "medium". Empty string means use the model's default.
	// Per-request AIOptions.ReasoningEffort overrides this client-level default.
	ReasoningEffort string

	Logger    core.Logger
	Telemetry core.Telemetry

	// Advanced options
	Headers map[string]string
	Extra   map[string]interface{}
}

// AIOption configures an AI client
type AIOption func(*AIConfig)

// WithProvider sets the AI provider
func WithProvider(provider string) AIOption {
	return func(c *AIConfig) {
		c.Provider = provider
	}
}

// WithAPIKey sets the API key
func WithAPIKey(key string) AIOption {
	return func(c *AIConfig) {
		c.APIKey = key
	}
}

// WithBaseURL sets the base URL for the API
func WithBaseURL(url string) AIOption {
	return func(c *AIConfig) {
		c.BaseURL = url
	}
}

// WithRegion sets the AWS region for AWS Bedrock provider
func WithRegion(region string) AIOption {
	return func(c *AIConfig) {
		if c.Extra == nil {
			c.Extra = make(map[string]interface{})
		}
		c.Extra["region"] = region
	}
}

// WithAWSCredentials sets explicit AWS credentials for Bedrock provider
func WithAWSCredentials(accessKey, secretKey, sessionToken string) AIOption {
	return func(c *AIConfig) {
		if c.Extra == nil {
			c.Extra = make(map[string]interface{})
		}
		c.Extra["aws_access_key_id"] = accessKey
		c.Extra["aws_secret_access_key"] = secretKey
		if sessionToken != "" {
			c.Extra["aws_session_token"] = sessionToken
		}
	}
}

// WithTimeout sets the request timeout
func WithTimeout(timeout time.Duration) AIOption {
	return func(c *AIConfig) {
		c.Timeout = timeout
	}
}

// WithMaxRetries sets the maximum number of retries
func WithMaxRetries(retries int) AIOption {
	return func(c *AIConfig) {
		c.MaxRetries = retries
	}
}

// WithModel sets the model to use
func WithModel(model string) AIOption {
	return func(c *AIConfig) {
		c.Model = model
	}
}

// WithTemperature sets the temperature for generation
func WithTemperature(temp float32) AIOption {
	return func(c *AIConfig) {
		c.Temperature = temp
	}
}

// WithMaxTokens sets the maximum tokens for generation
func WithMaxTokens(tokens int) AIOption {
	return func(c *AIConfig) {
		c.MaxTokens = tokens
	}
}

// WithReasoningTokenMultiplier sets the token multiplier for reasoning models (GPT-5, o1, o3, o4).
// Reasoning models count internal chain-of-thought tokens against max_completion_tokens but
// don't return them in the response. Without a multiplier, complex prompts exhaust tokens on
// reasoning, leaving nothing for visible output.
//
// Default is 5 (5x multiplier). Set to a lower value (e.g., 3) for cost optimization if
// responses are simpler, or higher (e.g., 8) for very complex reasoning tasks.
//
// Example: With multiplier=5 and MaxTokens=2000, reasoning models get 10000 tokens,
// ensuring ~4000 for internal reasoning + ~6000 for visible output.
func WithReasoningTokenMultiplier(multiplier int) AIOption {
	return func(c *AIConfig) {
		c.ReasoningTokenMultiplier = multiplier
	}
}

// WithReasoningEffort sets the default reasoning effort for OpenAI reasoning models.
// Valid values: "none", "low", "medium", "high", "xhigh" (model-dependent).
// GPT-5.2+ defaults to "none" (no extended thinking). Set to "high" or "xhigh"
// for complex reasoning tasks. This can be overridden per-request via AIOptions.ReasoningEffort.
func WithReasoningEffort(effort string) AIOption {
	return func(c *AIConfig) {
		c.ReasoningEffort = effort
	}
}

// WithHeaders sets custom headers
func WithHeaders(headers map[string]string) AIOption {
	return func(c *AIConfig) {
		if c.Headers == nil {
			c.Headers = make(map[string]string)
		}
		for k, v := range headers {
			c.Headers[k] = v
		}
	}
}

// WithExtra sets extra configuration options
func WithExtra(key string, value interface{}) AIOption {
	return func(c *AIConfig) {
		if c.Extra == nil {
			c.Extra = make(map[string]interface{})
		}
		c.Extra[key] = value
	}
}

// WithLogger sets the logger for AI operations
// This is typically called by the framework to provide observability
func WithLogger(logger core.Logger) AIOption {
	return func(c *AIConfig) {
		c.Logger = logger
	}
}

// WithTelemetry sets the telemetry provider for distributed tracing
// This enables spans to be created for AI operations, providing visibility
// in distributed tracing systems like Jaeger.
func WithTelemetry(telemetry core.Telemetry) AIOption {
	return func(c *AIConfig) {
		c.Telemetry = telemetry
	}
}

// WithProviderAlias sets the provider alias for OpenAI-compatible services (Phase 2)
// Examples: "openai.deepseek", "openai.groq", "openai.together"
// FOLLOWS FRAMEWORK PRINCIPLE: Intelligent Configuration Over Convention
//
// This function parses the alias to extract the base provider and sets the alias.
// Credential resolution (API keys, base URLs) is handled by the factory's
// resolveCredentials() method using the subProviders table as the single source of truth.
func WithProviderAlias(alias string) AIOption {
	return func(c *AIConfig) {
		c.ProviderAlias = alias

		// Parse the alias to set the base provider
		// "openai.deepseek" → provider="openai"
		parts := strings.Split(alias, ".")
		if len(parts) > 0 {
			c.Provider = parts[0]
		}
	}
}
