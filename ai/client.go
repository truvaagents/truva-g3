package ai

import (
	"fmt"
	"os"
	"strconv"
	"time"

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

// NewClient creates an AI client using registered providers
func NewClient(opts ...AIOption) (core.AIClient, error) {
	// Default configuration. MaxRetries starts as the unset sentinel so that
	// resolveMaxRetries() below can distinguish "explicit override" from
	// "fall back to env var or default".
	config := &AIConfig{
		Provider:    string(ProviderAuto),
		MaxRetries:  maxRetriesUnset,
		Timeout:     180 * time.Second, // 3 minutes default for reasoning models
		Temperature: 0.7,
		MaxTokens:   1000,
		Logger:      nil, // Will be set by framework or options
	}

	// Apply options
	for _, opt := range opts {
		opt(config)
	}

	// Resolve MaxRetries precedence: explicit option → env var → default.
	// Must run after options so that WithMaxRetries(n) wins over the env var.
	config.MaxRetries = resolveMaxRetries(config.MaxRetries)

	// Apply component-specific logging for AI module
	if config.Logger != nil {
		if cal, ok := config.Logger.(core.ComponentAwareLogger); ok {
			config.Logger = cal.WithComponent("framework/ai")
		}
		config.Logger.Info("Starting AI client creation", map[string]interface{}{
			"operation":        "ai_client_creation",
			"provider_setting": config.Provider,
			"auto_detect":      config.Provider == string(ProviderAuto),
		})
	}

	// Auto-detection logic with enhanced logging
	// Uses detectBestProviderWithAlias to pass both provider name and alias,
	// so the factory's resolveCredentials() uses the explicit alias path
	// and avoids re-running internal auto-detection.
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

	client := factory.Create(config)
	if config.Logger != nil {
		config.Logger.Info("AI client created successfully", map[string]interface{}{
			"operation":   "ai_client_creation",
			"provider":    config.Provider,
			"client_type": fmt.Sprintf("%T", client),
			"status":      "success",
		})
	}

	return client, nil
}

// MustNewClient creates a new AI client and panics on error
func MustNewClient(opts ...AIOption) core.AIClient {
	client, err := NewClient(opts...)
	if err != nil {
		panic(fmt.Sprintf("failed to create AI client: %v", err))
	}
	return client
}
