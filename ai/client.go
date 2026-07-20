package ai

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

// maxRetriesUnset is the sentinel value used by NewClient to detect whether
// the caller explicitly set MaxRetries via WithMaxRetries. After option
// application, an unchanged sentinel means "no explicit override; fall back
// to TRUVAG3_AI_RETRY_ATTEMPTS env var, then to defaultMaxRetries".
//
// Using a sentinel preserves the invariant that explicit options always win
// over env vars (per FRAMEWORK_DESIGN_PRINCIPLES Configuration Precedence).
const maxRetriesUnset = -1

// defaultMaxRetries is the fallback retry count when neither WithMaxRetries
// nor TRUVAG3_AI_RETRY_ATTEMPTS is set. Matches the historical default the
// per-provider BaseClient ships with.
const defaultMaxRetries = 3

// resolveMaxRetries applies the single-client MaxRetries precedence chain.
// Single clients (no chain failover layer below) absorb transient blips via
// in-provider retries, so the fallback is the framework default of 3.
//
// See resolveMaxRetriesWithDefault for the underlying precedence logic and
// the chain client which uses a different fallback (0).
func resolveMaxRetries(current int) int {
	return resolveMaxRetriesWithDefault(current, defaultMaxRetries)
}

// resolveMaxRetriesWithDefault is the shared MaxRetries precedence chain
// used by both NewClient (single client, fallback=3) and NewChainClient
// (per-provider, fallback=0).
//
// Precedence (highest to lowest):
//
//  1. Explicit option (WithMaxRetries / WithChainMaxRetries) — detected by
//     current != maxRetriesUnset. Any non-negative integer is honored,
//     including 0 ("no retries"). Programmatic Go API calls bypass the
//     env-var guard because they are explicit operator intent expressed
//     in code.
//
//  2. TRUVAG3_AI_RETRY_ATTEMPTS environment variable — applied when no
//     explicit option was passed. Per FRAMEWORK_DESIGN_PRINCIPLES §3.5
//     rule 3, env var values for numeric limits are guarded with val > 0.
//     Zero, negative, and non-integer values are silently rejected and
//     fall through to the caller-supplied fallback.
//
//  3. fallback — caller-supplied default. defaultMaxRetries (3) for
//     single clients; 0 for chain clients (where chain failover is the
//     primary retry layer and per-provider retries amplify token waste
//     on failing providers).
//
// Per FRAMEWORK_DESIGN_PRINCIPLES Configuration Precedence: explicit options
// always beat environment variables, environment variables always beat
// hard-coded defaults.
func resolveMaxRetriesWithDefault(current int, fallback int) int {
	if current != maxRetriesUnset {
		// Explicit option (WithMaxRetries / WithChainMaxRetries) — honor it,
		// including 0. This is the programmatic Go API path; the framework
		// env-var rule (val > 0) does not apply here because the operator
		// expressed intent in code rather than via environment.
		return current
	}
	if v := os.Getenv("TRUVAG3_AI_RETRY_ATTEMPTS"); v != "" {
		// Per FRAMEWORK_DESIGN_PRINCIPLES §3.5 rule 3: env var values must
		// be > 0. Zero and negative are rejected; the caller's fallback wins.
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func newClientConfigWithDefaults() *clientConfig {
	return &clientConfig{legacy: AIConfig{
		Provider:    string(ProviderAuto),
		MaxRetries:  maxRetriesUnset,
		Timeout:     180 * time.Second, // 3 minutes default for reasoning models
		Temperature: 0.7,
		MaxTokens:   1000,
		Logger:      nil, // Will be set by framework or options
	}}
}

// NewClient creates an AI client using registered providers.
func NewClient(opts ...AIOption) (core.AIClient, error) {
	config := newClientConfigWithDefaults()
	for _, option := range opts {
		// Preserve the legacy constructor's direct AIOption invocation behavior.
		option(&config.legacy)
	}

	factory, err := resolveProviderFactory(&config.legacy)
	if err != nil {
		return nil, err
	}
	legacy := snapshotAIConfig(&config.legacy)
	client, err := createFromFactory(factory, legacy)
	if err != nil {
		return nil, err
	}
	logClientCreated(legacy, client)
	return client, nil
}

// NewRequestClient creates a request-capable AI client. Existing AIOption
// values may be mixed with the advanced ClientOption helpers.
func NewRequestClient(options ...ClientOption) (core.AIRequestClient, error) {
	config := newClientConfigWithDefaults()
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("client option %d is nil", index)
		}
		if err := option.applyClient(config); err != nil {
			return nil, fmt.Errorf("apply client option %d: %w", index, err)
		}
	}

	factory, err := resolveProviderFactory(&config.legacy)
	if err != nil {
		return nil, err
	}
	integration, err := validateAndSnapshotIntegration(config)
	if err != nil {
		return nil, err
	}
	legacy := snapshotAIConfig(&config.legacy)

	if requestFactory, ok := factory.(RequestProviderFactory); ok {
		client, err := requestFactory.CreateRequestClient(legacy, integration)
		if err != nil {
			return nil, err
		}
		if client == nil {
			return nil, errors.New("provider factory returned a nil request client")
		}
		logClientCreated(legacy, client)
		return client, nil
	}

	if !integrationIsZero(integration) {
		return nil, fmt.Errorf(
			"%w: provider factory %T cannot accept integration options",
			core.ErrAIRequestFeatureUnsupported,
			factory,
		)
	}
	legacyClient, err := createFromFactory(factory, legacy)
	if err != nil {
		return nil, err
	}
	requestClient, ok := legacyClient.(core.AIRequestClient)
	if !ok {
		return nil, fmt.Errorf(
			"%w: provider client %T",
			core.ErrAIRequestFeatureUnsupported,
			legacyClient,
		)
	}
	logClientCreated(legacy, requestClient)
	return requestClient, nil
}

func resolveProviderFactory(config *AIConfig) (ProviderFactory, error) {
	// Resolve MaxRetries precedence after all options so an explicit value wins
	// over the environment and hard-coded default.
	config.MaxRetries = resolveMaxRetries(config.MaxRetries)

	if config.Logger != nil {
		if componentLogger, ok := config.Logger.(core.ComponentAwareLogger); ok {
			config.Logger = componentLogger.WithComponent("framework/ai")
		}
		config.Logger.Info("Starting AI client creation", map[string]interface{}{
			"operation":        "ai_client_creation",
			"provider_setting": config.Provider,
			"auto_detect":      config.Provider == string(ProviderAuto),
		})
	}

	// Resolve both provider and alias once so provider factories do not repeat
	// environment detection with a potentially different result.
	if config.Provider == string(ProviderAuto) {
		provider, alias, err := detectBestProviderWithAlias(config.Logger)
		if err != nil {
			if config.Logger != nil {
				config.Logger.Error("AI provider auto-detection failed", map[string]interface{}{
					"operation":           "ai_provider_detection",
					"error":               err.Error(),
					"available_providers": ListProviders(),
					"suggestion":          "Set explicit provider or configure API keys",
				})
			}
			return nil, fmt.Errorf("no AI provider available: %w", err)
		}
		config.Provider = provider
		if config.ProviderAlias == "" {
			config.ProviderAlias = alias
		}

		if config.Logger != nil {
			config.Logger.Info("AI provider auto-detected", map[string]interface{}{
				"operation":         "ai_provider_detection",
				"selected_provider": provider,
				"selected_alias":    alias,
				"detection_method":  "environment_scan",
				"status":            "success",
			})
		}
	}

	factory, exists := GetProvider(config.Provider)
	if !exists {
		if config.Logger != nil {
			config.Logger.Error("AI provider not registered", map[string]interface{}{
				"operation":           "ai_provider_lookup",
				"requested_provider":  config.Provider,
				"available_providers": ListProviders(),
				"import_hint":         fmt.Sprintf("Import _ \"github.com/truvaagents/truva-g3/ai/providers/%s\"", config.Provider),
			})
		}
		return nil, fmt.Errorf("provider '%s' not registered. Import _ \"github.com/truvaagents/truva-g3/ai/providers/%s\"",
			config.Provider, config.Provider)
	}
	return factory, nil
}

func createFromFactory(factory ProviderFactory, config *AIConfig) (core.AIClient, error) {
	if validated, ok := factory.(ValidatedProviderFactory); ok {
		client, err := validated.CreateValidated(config)
		if err != nil {
			return nil, err
		}
		if client == nil {
			return nil, errors.New("provider factory returned a nil client")
		}
		return client, nil
	}

	client := factory.Create(config)
	if client == nil {
		return nil, errors.New("provider factory returned a nil client")
	}
	return client, nil
}

func validateAndSnapshotIntegration(config *clientConfig) (ProviderIntegrationConfig, error) {
	if config == nil {
		return ProviderIntegrationConfig{}, errors.New("AI client configuration is nil")
	}
	integration := config.integration
	if !integration.CompatibilityMode.Valid() {
		return ProviderIntegrationConfig{}, fmt.Errorf("invalid AI compatibility mode %d", integration.CompatibilityMode)
	}
	if config.credentialSourceSet && isNilIntegrationValue(integration.CredentialSource) {
		return ProviderIntegrationConfig{}, errors.New("AI credential source is nil")
	}
	if config.endpointResolverSet && isNilIntegrationValue(integration.EndpointResolver) {
		return ProviderIntegrationConfig{}, errors.New("AI endpoint resolver is nil")
	}
	if config.httpClientSet && integration.HTTPClient == nil {
		return ProviderIntegrationConfig{}, errors.New("AI HTTP client is nil")
	}
	rules, err := requestpolicy.ClonePatches(integration.RequestRules)
	if err != nil {
		return ProviderIntegrationConfig{}, fmt.Errorf("validate AI request rules: %w", err)
	}
	middleware := append([]requestpolicy.RequestMiddleware(nil), integration.RequestMiddleware...)
	if _, err := requestpolicy.NewEngine(requestpolicy.Config{
		AppRules:   rules,
		Middleware: middleware,
		Mode:       integration.CompatibilityMode,
	}); err != nil {
		return ProviderIntegrationConfig{}, fmt.Errorf("validate AI request integration: %w", err)
	}
	return ProviderIntegrationConfig{
		RequestRules:      rules,
		RequestMiddleware: middleware,
		CompatibilityMode: integration.CompatibilityMode,
		CredentialSource:  integration.CredentialSource,
		EndpointResolver:  integration.EndpointResolver,
		HTTPClient:        integration.HTTPClient,
	}, nil
}

func isNilIntegrationValue(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func integrationIsZero(config ProviderIntegrationConfig) bool {
	return len(config.RequestRules) == 0 &&
		len(config.RequestMiddleware) == 0 &&
		config.CompatibilityMode == requestpolicy.CompatibilityCompatible &&
		config.CredentialSource == nil &&
		config.EndpointResolver == nil &&
		config.HTTPClient == nil
}

func snapshotAIConfig(config *AIConfig) *AIConfig {
	cloned := *config
	// Reuse Core's compatibility clone so legacy Extra retains opaque leaves by
	// reference while every map, slice, and array container is isolated.
	options := core.NewAIRequestFromLegacy("", "", &core.AIOptions{
		Headers: config.Headers,
		Extra:   config.Extra,
	}).LegacyOptions()
	cloned.Headers = options.Headers
	cloned.Extra = options.Extra
	return &cloned
}

func logClientCreated(config *AIConfig, client core.AIClient) {
	if config.Logger == nil {
		return
	}
	config.Logger.Info("AI client created successfully", map[string]interface{}{
		"operation":   "ai_client_creation",
		"provider":    config.Provider,
		"client_type": fmt.Sprintf("%T", client),
		"status":      "success",
	})
}

// MustNewClient creates a new AI client and panics on error
func MustNewClient(opts ...AIOption) core.AIClient {
	client, err := NewClient(opts...)
	if err != nil {
		panic(fmt.Sprintf("failed to create AI client: %v", err))
	}
	return client
}
