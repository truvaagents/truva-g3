package openai

import (
	"net"
	"net/http"
	urlpkg "net/url"
	"os"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

// subProviderConfig defines a single OpenAI-compatible sub-provider.
// This is the single source of truth for env-var-to-alias-to-priority mapping,
// eliminating the 4x duplication across resolveCredentials, DetectEnvironment,
// WithProviderAlias, and auto-detect paths.
type subProviderConfig struct {
	Alias      string // "openai", "openai.groq", etc.
	EnvKey     string // "OPENAI_API_KEY", "GROQ_API_KEY" — empty for local providers
	EnvBaseURL string // "OPENAI_BASE_URL", "GROQ_BASE_URL"
	DefaultURL string // default base URL
	Priority   int    // detection priority (higher = tried first)
	IsLocal    bool   // true for Ollama (health-check based detection)
}

// subProviders is the canonical list of all OpenAI-compatible sub-providers.
// Priority order: OpenAI(1000) > Groq(700) > DeepSeek(600) > xAI(500) > Mistral(450) > Qwen(400) > Together(300) > Ollama(100)
var subProviders = []subProviderConfig{
	{"openai", "OPENAI_API_KEY", "OPENAI_BASE_URL", "https://api.openai.com/v1", 1000, false},
	{"openai.groq", "GROQ_API_KEY", "GROQ_BASE_URL", "https://api.groq.com/openai/v1", 700, false},
	{"openai.deepseek", "DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL", "https://api.deepseek.com", 600, false},
	{"openai.xai", "XAI_API_KEY", "XAI_BASE_URL", "https://api.x.ai/v1", 500, false},
	{"openai.mistral", "MISTRAL_API_KEY", "MISTRAL_BASE_URL", "https://api.mistral.ai/v1", 450, false},
	{"openai.qwen", "QWEN_API_KEY", "QWEN_BASE_URL", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", 400, false},
	{"openai.together", "TOGETHER_API_KEY", "TOGETHER_BASE_URL", "https://api.together.xyz/v1", 300, false},
	{"openai.ollama", "", "OLLAMA_BASE_URL", "http://localhost:11434/v1", 100, true},
}

// Factory implements ai.ProviderFactory for OpenAI
type Factory struct{}

// Create creates a new OpenAI client instance
// UPDATED: Now uses resolveCredentials() to properly handle multiple OpenAI-compatible providers
// without mutating environment variables. This maintains backward compatibility while fixing
// the critical configuration corruption bug.
func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
	// Resolve credentials using the three-tier configuration hierarchy:
	// 1. Explicit config (highest priority)
	// 2. Environment variables with provider-specific overrides
	// 3. Hardcoded defaults (lowest priority)
	apiKey, baseURL := f.resolveCredentials(config)

	logger := config.Logger
	if logger == nil {
		logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/ai")
	}

	// Note: Model alias resolution moved to request-time in GenerateResponse
	// This enables Chain Client to pass portable aliases to all providers

	logger.Info("OpenAI provider initialized", map[string]interface{}{
		"operation":      "ai_provider_init",
		"provider":       "openai",
		"provider_alias": config.ProviderAlias, // Phase 2: Log which alias is used
		"base_url":       baseURL,
		"has_api_key":    apiKey != "",
		"timeout":        config.Timeout.String(),
		"max_retries":    config.MaxRetries,
		"model":          config.Model,
	})

	// Create the client with resolved configuration
	client := NewClient(apiKey, baseURL, config.ProviderAlias, logger)

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
	// CRITICAL: Use the "default" ALIAS (not resolved model name) to enable env var overrides
	//
	// How it works:
	// 1. DefaultModel is set to "default" (the alias)
	// 2. When GenerateResponse() calls ApplyDefaults(), options.Model = "default"
	// 3. ResolveModel() resolves "default" by checking:
	//    a. Env var TRUVAG3_{PROVIDER}_MODEL_DEFAULT (highest priority)
	//    b. ModelAliases[provider]["default"] (fallback)
	//
	// This enables runtime model override for ALL calls via:
	//   TRUVAG3_OPENAI_MODEL_DEFAULT=gpt-4o-mini
	//   TRUVAG3_GROQ_MODEL_DEFAULT=llama-3.2-90b-vision
	//
	// Priority:
	// 1. Explicit config.Model (highest) - use as-is, may be alias or concrete name
	// 2. "default" alias (enables env var override for all unspecified models)
	if config.Model != "" {
		client.DefaultModel = config.Model
	} else {
		// Use "default" alias so ALL calls go through ResolveModel() and respect env vars
		client.DefaultModel = "default"
	}

	// Apply temperature default
	if config.Temperature > 0 {
		client.DefaultTemperature = config.Temperature
	}

	// Apply max tokens default
	if config.MaxTokens > 0 {
		client.DefaultMaxTokens = config.MaxTokens
	}

	// Apply reasoning token multiplier for reasoning models (GPT-5, o1, o3, o4)
	// This ensures sufficient tokens for both internal reasoning and visible output
	if config.ReasoningTokenMultiplier > 0 {
		client.ReasoningTokenMultiplier = config.ReasoningTokenMultiplier
	}

	// Apply reasoning effort for reasoning models
	if config.ReasoningEffort != "" {
		client.ReasoningEffort = config.ReasoningEffort
	}

	if len(config.Headers) > 0 {
		client.defaultHeaders = providers.MergeStringMaps(nil, config.Headers)
		transport := &headerTransport{
			headers: config.Headers,
			protected: map[string]struct{}{
				"authorization": {},
				"content-type":  {},
			},
			base: http.DefaultTransport,
		}
		client.HTTPClient.Transport = transport
	}
	if len(config.Extra) > 0 {
		client.defaultExtra = providers.MergeAnyMaps(nil, config.Extra)
	}

	return client
}

// resolveCredentials determines which OpenAI-compatible service to use and resolves credentials.
// Uses the subProviders table as the single source of truth for env-var-to-alias mapping.
//
// Configuration precedence (three-tier hierarchy):
// 1. Explicit configuration (highest priority) - values passed directly in config
// 2. Environment variable overrides (medium priority) - enables runtime flexibility
// 3. Hardcoded defaults (lowest priority) - provides zero-config experience
func (f *Factory) resolveCredentials(config *ai.AIConfig) (apiKey, baseURL string) {
	// Explicit alias path: look up subProviders table by alias
	if config.ProviderAlias != "" {
		for _, sp := range subProviders {
			if sp.Alias == config.ProviderAlias {
				if sp.IsLocal {
					apiKey = config.APIKey // Local providers don't need API key
				} else {
					apiKey = firstNonEmpty(config.APIKey, os.Getenv(sp.EnvKey))
				}
				baseURL = firstNonEmpty(config.BaseURL, os.Getenv(sp.EnvBaseURL), sp.DefaultURL)
				return apiKey, baseURL
			}
		}
		// Unknown alias — log a warning and fall through to auto-detect.
		// This should not happen in normal operation because chain_client.go validates
		// aliases against the registry. If it does, it likely means a typo like
		// "openai.typo" that passes registry validation (base="openai" exists) but
		// isn't a known sub-provider.
		logger := config.Logger
		if logger != nil {
			logger.Warn("Unknown provider alias, falling back to auto-detect", map[string]interface{}{
				"operation":     "ai_resolve_credentials",
				"unknown_alias": config.ProviderAlias,
				"known_aliases": knownAliasNames(),
				"suggestion":    "Check for typos in the provider alias",
			})
		}
	}

	// Auto-detect path (empty alias): iterate subProviders in priority order, pick first available
	for _, sp := range subProviders {
		if sp.IsLocal {
			// Only probe local services if the user has set the base URL env var
			if os.Getenv(sp.EnvBaseURL) != "" && isLocalServiceAvailable(firstNonEmpty(os.Getenv(sp.EnvBaseURL), sp.DefaultURL)+"/models") {
				apiKey = config.APIKey
				baseURL = firstNonEmpty(config.BaseURL, os.Getenv(sp.EnvBaseURL), sp.DefaultURL)
				return apiKey, baseURL
			}
		} else if os.Getenv(sp.EnvKey) != "" {
			apiKey = firstNonEmpty(config.APIKey, os.Getenv(sp.EnvKey))
			baseURL = firstNonEmpty(config.BaseURL, os.Getenv(sp.EnvBaseURL), sp.DefaultURL)
			return apiKey, baseURL
		}
	}

	// Fallback: Use whatever was provided in config, or OpenAI defaults
	apiKey = firstNonEmpty(config.APIKey, os.Getenv("OPENAI_API_KEY"))
	baseURL = firstNonEmpty(config.BaseURL, os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1")
	return apiKey, baseURL
}

// knownAliasNames returns all alias names from the subProviders table for diagnostics
func knownAliasNames() []string {
	names := make([]string, len(subProviders))
	for i, sp := range subProviders {
		names[i] = sp.Alias
	}
	return names
}

// firstNonEmpty returns the first non-empty string from the provided values
// This helper implements the configuration precedence pattern used throughout the framework
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// headerTransport adds custom headers to requests
type headerTransport struct {
	headers   map[string]string
	protected map[string]struct{}
	base      http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Add custom headers
	for k, v := range t.headers {
		if _, ok := t.protected[strings.ToLower(k)]; ok {
			continue
		}
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}

// DetectEnvironment checks if any OpenAI-compatible service is available.
// Returns the priority of the highest-priority available sub-provider.
// This method only READS environment, never mutates it.
func (f *Factory) DetectEnvironment() (priority int, available bool) {
	aliases := f.DetectAvailableAliases()
	if len(aliases) == 0 {
		return 0, false
	}
	return aliases[0].Priority, true
}

// DetectAvailableAliases implements SubProviderEnumerator.
// It returns all available OpenAI-compatible sub-providers detected from the environment,
// using the subProviders table as the single source of truth.
func (f *Factory) DetectAvailableAliases() []ai.AliasAvailability {
	var available []ai.AliasAvailability

	for _, sp := range subProviders {
		if sp.IsLocal {
			// Only probe local services if the user has explicitly set the base URL env var,
			// indicating intent to use the local provider. Without this guard, every
			// DetectAvailableAliases() call incurs a 2-second HTTP timeout penalty in
			// environments where the local service (e.g., Ollama) is not running.
			if os.Getenv(sp.EnvBaseURL) != "" && isLocalServiceAvailable(firstNonEmpty(os.Getenv(sp.EnvBaseURL), sp.DefaultURL)+"/models") {
				available = append(available, ai.AliasAvailability{
					Alias:        sp.Alias,
					ProviderName: "openai",
					Priority:     sp.Priority,
				})
			}
		} else if os.Getenv(sp.EnvKey) != "" {
			available = append(available, ai.AliasAvailability{
				Alias:        sp.Alias,
				ProviderName: "openai",
				Priority:     sp.Priority,
			})
		}
	}

	return available
}

// isLocalServiceAvailable checks if a local service is running
func isLocalServiceAvailable(url string) bool {
	parsedURL, err := urlpkg.Parse(url)
	if err != nil || !isLoopbackHost(parsedURL.Hostname()) {
		return false
	}

	// #nosec G704 -- the URL is parsed above and restricted to loopback hosts only.
	req, err := http.NewRequest(http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return false
	}

	client := &http.Client{Timeout: 2 * time.Second}
	// #nosec G704 -- the request target is restricted to loopback hosts above.
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() {
		_ = resp.Body.Close() // Error can be safely ignored after health check
	}()
	return resp.StatusCode == http.StatusOK
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Name returns the provider name
func (f *Factory) Name() string {
	return "openai"
}

// Description returns a human-readable description
func (f *Factory) Description() string {
	return "Universal OpenAI-compatible provider (OpenAI, Groq, DeepSeek, Qwen, local models, etc.)"
}

// Register registers this provider with the global registry
// This is called automatically when the package is imported
func init() {
	ai.MustRegister(&Factory{})
}
