package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

// ChainClient implements automatic failover across multiple providers (Phase 3)
// FOLLOWS FRAMEWORK PRINCIPLE: Fail-Fast for Configuration Errors, Resilient Runtime Behavior
type ChainClient struct {
	entries         []ChainEntry
	providers       []core.AIClient
	providerAliases []string // Provider aliases for logging (e.g., "openai", "anthropic")
	logger          core.Logger
	telemetry       core.Telemetry
}

// NewChainClient creates a client that automatically fails over between providers
// This implements the "backup provider support" feature that enables automatic failover.
//
// FOLLOWS FRAMEWORK PRINCIPLES:
// - Fail-Fast Configuration: Invalid provider aliases fail immediately at creation time
// - Resilient Runtime: Missing API keys are warnings, not errors (allows partial chains)
// - Circuit Breaker Integration: Each provider already has circuit breaker protection
func NewChainClient(opts ...ChainOption) (*ChainClient, error) {
	// MaxRetries starts as the unset sentinel so per-provider NewClient calls
	// can fall back to TRUVAG3_AI_RETRY_ATTEMPTS or the historical default of 3.
	// Explicit WithChainMaxRetries(n) overrides for all providers in the chain.
	config := &ChainConfig{
		MaxRetries: maxRetriesUnset,
	}
	for _, opt := range opts {
		opt(config)
	}

	// Auto-detect path: when no explicit chain is provided, discover available providers
	// from the environment. This is the chain client counterpart of single client's auto-detect.
	if len(config.ProviderAliases) == 0 {
		detected := DetectAvailableProviders(config.Logger)
		if len(detected) == 0 {
			return nil, fmt.Errorf("configuration error: no providers detected (check API keys)")
		}
		for _, d := range detected {
			config.ProviderAliases = append(config.ProviderAliases, d.Alias)
		}

		if config.Logger != nil {
			config.Logger.Info("Chain client auto-detected providers", map[string]interface{}{
				"operation":        "ai_chain_auto_detect",
				"detected_aliases": config.ProviderAliases,
				"detected_count":   len(config.ProviderAliases),
			})
		}
	}

	// Validate provider aliases dynamically against registry
	for _, alias := range config.ProviderAliases {
		baseName := strings.Split(alias, ".")[0]
		if _, exists := GetProvider(baseName); !exists {
			return nil, fmt.Errorf("configuration error: unknown provider alias %q (base provider %q not registered)", alias, baseName)
		}
	}

	// Wrap logger with component for consistent attribution
	logger := config.Logger
	if logger == nil {
		logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/ai")
	}

	client := &ChainClient{
		entries:         make([]ChainEntry, 0, len(config.ProviderAliases)),
		providers:       make([]core.AIClient, 0, len(config.ProviderAliases)),
		providerAliases: make([]string, 0, len(config.ProviderAliases)),
		logger:          logger,
		telemetry:       config.Telemetry,
	}

	// Create a client for each provider alias
	// RESILIENT: Runtime provider creation failures are handled gracefully
	successCount := 0
	for _, alias := range config.ProviderAliases {
		// Build options for provider creation
		opts := []AIOption{
			WithLogger(config.Logger),
			WithTelemetry(config.Telemetry),
		}
		// Apply an explicit chain timeout when configured. Otherwise the
		// materializer supplies the framework's failover-safe 180s default.
		if config.Timeout > 0 {
			opts = append(opts, WithTimeout(config.Timeout))
		}
		// Apply reasoning token multiplier if configured
		if config.ReasoningTokenMultiplier > 0 {
			opts = append(opts, WithReasoningTokenMultiplier(config.ReasoningTokenMultiplier))
		}
		// Apply reasoning effort if configured
		if config.ReasoningEffort != "" {
			opts = append(opts, WithReasoningEffort(config.ReasoningEffort))
		}
		// Per-provider MaxRetries with chain-specific default of 0.
		//
		// Why 0 for chain clients (vs 3 for single clients): the chain client's
		// failover loop IS the retry mechanism. When a provider fails on a
		// retryable error, the chain walks to the next provider. Per-provider
		// in-provider retries (BaseClient.ExecuteWithRetry) just amplify
		// wasted token spend on the dead provider before failover kicks in.
		//
		// Precedence (resolved by resolveMaxRetriesWithDefault):
		//   1. Explicit WithChainMaxRetries(n) — wins, any non-negative integer
		//   2. TRUVAG3_AI_RETRY_ATTEMPTS env var — only positive values honored
		//   3. Chain default of 0 — failover is the retry layer
		//
		// Always propagate as WithMaxRetries so the per-provider NewClient
		// sees an explicit option and bypasses its own (single-client) default.
		chainPerProviderRetries := resolveMaxRetriesWithDefault(config.MaxRetries, 0)
		opts = append(opts, WithMaxRetries(chainPerProviderRetries))
		entry, err := materializeEntry(legacyProviderEntry(alias, alias, opts...))
		if err != nil {
			// Runtime failures (e.g., missing API keys) are warnings, not errors
			// This allows partial chain creation when some providers aren't configured
			errorType, safeError := providers.SanitizedObservationError(err, "invalid_request")
			logger.Warn("Provider not available (will skip in chain)", map[string]interface{}{
				"operation":  "ai_chain_init",
				"alias":      alias,
				"error":      safeError.Error(),
				"error_type": errorType,
				"note":       "This provider will be skipped during failover",
			})
			continue // Skip unavailable providers gracefully
		}
		client.entries = append(client.entries, entry)
		client.providers = append(client.providers, entry.client)
		client.providerAliases = append(client.providerAliases, alias)
		successCount++
	}

	// FAIL-FAST: If NO providers could be created, that's a configuration error
	if successCount == 0 {
		return nil, fmt.Errorf("configuration error: no providers could be initialized (check API keys)")
	}

	logger.Info("Chain client initialized", map[string]interface{}{
		"operation":           "ai_chain_init",
		"requested_providers": len(config.ProviderAliases),
		"available_providers": successCount,
	})

	return client, nil
}

// SetLogger updates the logger after client creation.
// This is called by Framework.applyConfigToComponent() to propagate
// the real logger to the ChainClient after framework initialization.
//
// This fixes the critical bug where ChainClient captures NoOpLogger during
// agent construction (before Framework sets the real logger).
//
// See: ai/notes/LOGGING_TELEMETRY_AUDIT.md - "CRITICAL BUG: AI Module Logger Not Propagated"
func (c *ChainClient) SetLogger(logger core.Logger) {
	if logger == nil {
		c.logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		c.logger = cal.WithComponent("framework/ai")
	} else {
		c.logger = logger
	}

	// Propagate only to framework-managed providers. Injected clients remain
	// caller-owned and are never mutated through optional setters.
	for _, entry := range c.runtimeEntries() {
		if entry.kind == chainInjectedClient {
			continue
		}
		if loggable, ok := entry.client.(interface{ SetLogger(core.Logger) }); ok {
			loggable.SetLogger(logger)
		}
	}
}

// SetTelemetry updates chain-level tracing and propagates it only to
// framework-managed provider entries. Caller-owned ClientEntry values are not
// mutated through optional setters.
func (c *ChainClient) SetTelemetry(provider core.Telemetry) {
	c.telemetry = provider
	for _, entry := range c.runtimeEntries() {
		if entry.kind == chainInjectedClient {
			continue
		}
		if configurable, ok := entry.client.(interface{ SetTelemetry(core.Telemetry) }); ok {
			configurable.SetTelemetry(provider)
		}
	}
}

// GenerateResponse tries each provider until one succeeds
// FOLLOWS FRAMEWORK PRINCIPLE: Circuit Breaker Integration for external API calls
//
// Behavior:
// - Clones options for each provider to avoid mutation bleeding across providers
// - Preserves original model setting so each provider can apply its own defaults/resolution
// - Tries each provider in order until one succeeds
// - Fails fast on client errors (4xx) - these are not retryable
// - Continues on server errors (5xx) - these might work on different provider
// - Returns first successful response
// - Comprehensive telemetry and logging for failover debugging
//
// See: ai/MODEL_ALIAS_CROSS_PROVIDER_PROPOSAL.md for the options mutation bug fix
func (c *ChainClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	result, err := c.Generate(ctx, core.NewAIRequestFromLegacy(prompt, "", options))
	if result != nil {
		return result.Response, err
	}
	return nil, err
}

// StreamResponse generates a streaming response with automatic failover
// IMPORTANT: Streaming has different failover semantics than synchronous calls:
//   - Once streaming starts successfully, we commit to that provider
//   - Failover only happens if the connection fails before streaming starts
//   - If streaming is interrupted mid-stream, we return the partial response and
//     the current provider error, including ErrStreamPartiallyCompleted when used
func (c *ChainClient) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	result, err := c.Stream(ctx, core.NewAIRequestFromLegacy(prompt, "", options), callback)
	if result != nil {
		return result.Response, err
	}
	return nil, err
}

// SupportsStreaming returns true if at least one provider supports streaming
func (c *ChainClient) SupportsStreaming() bool {
	for _, entry := range c.runtimeEntries() {
		// Decorators such as InstrumentedAIClient expose the request-streaming
		// interface uniformly and use SupportsStreaming to report the wrapped
		// client's actual capability. Honor that dynamic answer before treating
		// a request-only streaming interface as unconditional support.
		if streamingProvider, ok := entry.client.(core.StreamingAIClient); ok {
			if streamingProvider.SupportsStreaming() {
				return true
			}
			continue
		}
		if _, ok := entry.client.(core.StreamingAIRequestClient); ok {
			return true
		}
	}
	return false
}

// isClientError checks if the error is a non-retryable client error
// In a provider chain, we WANT to failover on authentication errors because
// each provider has its own API key. Auth errors on OpenAI should try Anthropic.
//
// Non-retryable errors (don't try other providers):
// - Bad request (malformed input)
// - Content policy violations
// - Invalid parameters
//
// Retryable errors (DO try other providers):
// - Transient proxy/infra errors (Cloudflare HTML 400, DNS, TLS) — IsTransient()
// - Provider-specific terminal errors (billing exhausted, account suspended) — IsRetryable()
// - Authentication/API key errors (each provider has different key!)
// - Rate limits (429)
// - Server errors (5xx)
//
// Non-retryable (true client errors — same bad input fails on any provider):
// - 400 Bad Request (real API error, not proxy, no billing/quota markers)
// - 404, 409, 422 etc.
func isClientError(err error) bool {
	var pe core.ProviderError
	if errors.As(err, &pe) {
		// Transient proxy/infra errors are always retryable on next provider
		if pe.IsTransient() {
			return false
		}
		// Provider-specific terminal errors (billing exhausted, account suspended)
		// are retryable because a different provider in the chain may succeed.
		// The provider sets this flag from structured response metadata — never
		// from string-matching at the chain layer.
		if pe.IsRetryable() {
			return false
		}
		// True client errors (malformed input, content policy) — don't retry
		// Exclude 401/403 (auth differs per provider) and 429 (rate limit)
		status := pe.StatusCode()
		return status >= 400 && status < 500 && status != 401 && status != 403 && status != 429
	}
	// Unstructured errors — fall back to conservative retry (same as previous default)
	return false
}

// ChainConfig holds configuration for chain client.
//
// IMPORTANT: ChainConfig MUST be constructed via NewChainClient, not directly.
// NewChainClient initializes MaxRetries to maxRetriesUnset (the "no explicit
// override" sentinel) so the per-provider precedence chain (env var → default)
// runs correctly. Direct construction (e.g. &ChainConfig{ProviderAliases: ...})
// leaves MaxRetries at the Go zero value of 0, which the per-provider factories
// now interpret as "no retries" — silently disabling the retry budget instead
// of falling back to the framework default. Use NewChainClient and pass options.
type ChainConfig struct {
	ProviderAliases []string
	Logger          core.Logger
	Telemetry       core.Telemetry
	// Timeout is a positive request deadline override. Zero or a negative value
	// means unset; framework-managed entries then use the failover-safe 180s
	// framework default rather than a provider's longer standalone default.
	Timeout                  time.Duration
	ReasoningTokenMultiplier int    // Token multiplier for reasoning models (0 = use default 5x)
	ReasoningEffort          string // Default reasoning effort: "none", "low", "medium", "high", "xhigh" (empty = model default)
	// MaxRetries controls the per-provider HTTP retry count for every provider
	// in the chain. The maxRetriesUnset sentinel (NewChainClient initializes
	// this) means "let each per-provider NewClient resolve via env var or
	// default"; an explicit value (set via WithChainMaxRetries) propagates to
	// every provider. NEVER construct ChainConfig directly without setting this
	// to maxRetriesUnset — the Go zero value of 0 will disable retries entirely.
	MaxRetries int
}

// ChainOption configures a chain client
type ChainOption func(*ChainConfig)

// WithProviderChain sets the provider aliases to try in order
// Example: WithProviderChain("openai", "openai.deepseek", "anthropic")
// The client will try OpenAI first, then DeepSeek, then Anthropic until one succeeds.
func WithProviderChain(aliases ...string) ChainOption {
	return func(c *ChainConfig) {
		c.ProviderAliases = aliases
	}
}

// WithChainLogger sets the logger for the chain client
// This enables tracking of failover attempts and provider selection.
func WithChainLogger(logger core.Logger) ChainOption {
	return func(c *ChainConfig) {
		c.Logger = logger
	}
}

// WithChainTelemetry sets the telemetry provider for the chain client
// This enables distributed tracing for AI operations across all providers in the chain.
func WithChainTelemetry(telemetry core.Telemetry) ChainOption {
	return func(c *ChainConfig) {
		c.Telemetry = telemetry
	}
}

// WithChainTimeout sets the request timeout for AI requests in the chain.
// This is important for reasoning models (GPT-5, o1, o3, o4) that need longer
// processing time for chain-of-thought responses.
// If not set, framework-managed entries use the failover-safe 180-second
// framework default rather than a provider's longer standalone default.
// Zero and negative durations are treated as unset, not as an unbounded mode.
func WithChainTimeout(timeout time.Duration) ChainOption {
	return func(c *ChainConfig) {
		c.Timeout = timeout
	}
}

// WithChainReasoningTokenMultiplier sets the token multiplier for reasoning models
// in all providers within the chain. Reasoning models (GPT-5, o1, o3, o4) count
// internal chain-of-thought tokens against max_completion_tokens but don't return
// them in the response. Without a multiplier, complex prompts exhaust tokens on
// reasoning, leaving nothing for visible output.
//
// Default is 5 (5x multiplier). Set to a lower value (e.g., 3) for cost optimization
// if responses are simpler, or higher (e.g., 8) for very complex reasoning tasks.
func WithChainReasoningTokenMultiplier(multiplier int) ChainOption {
	return func(c *ChainConfig) {
		c.ReasoningTokenMultiplier = multiplier
	}
}

// WithChainReasoningEffort sets the default reasoning effort for reasoning models
// in all providers within the chain. Valid values: "none", "low", "medium", "high", "xhigh".
// GPT-5.2+ defaults to "none" (no extended thinking). Set to "high" or "xhigh" for
// complex reasoning tasks. Can be overridden per-request via AIOptions.ReasoningEffort.
func WithChainReasoningEffort(effort string) ChainOption {
	return func(c *ChainConfig) {
		c.ReasoningEffort = effort
	}
}

// WithChainMaxRetries sets the per-provider HTTP retry budget for all providers
// in the chain. This is the in-provider retry layer (BaseClient.MaxRetries) —
// it does NOT change how many providers the chain walks, only how many times
// each provider retries before giving up and letting the chain move on.
//
// Chain default is 0 (no per-provider retries) because the chain client's
// failover loop is the retry mechanism: when a provider fails on a retryable
// error, the chain walks to the next provider. In-provider retries inside a
// chain just amplify wasted token spend on the dead provider before failover
// kicks in. Use this option to override the default when you specifically
// want to absorb transient blips inside a single provider before the chain
// moves on (e.g. flaky network, brief 5xx during a deploy).
//
// Configuration precedence:
//  1. Explicit WithChainMaxRetries(n) (highest) — any non-negative integer
//  2. TRUVAG3_AI_RETRY_ATTEMPTS env var — only positive integers honored
//     per FRAMEWORK_DESIGN_PRINCIPLES §3.5 rule 3
//  3. Chain default of 0 (no per-provider retries)
//
// Applied uniformly to every provider in the chain. To set different retry
// counts per provider, build single-provider clients with WithMaxRetries(n)
// instead of using a chain.
func WithChainMaxRetries(retries int) ChainOption {
	return func(c *ChainConfig) {
		c.MaxRetries = retries
	}
}

// ChainProviderInfo contains information about the AI provider chain configuration.
// This is returned by GetProviderInfo() for status reporting and observability.
type ChainProviderInfo struct {
	// AvailableProviders are the providers that were successfully initialized
	// (have valid API keys and could be created)
	AvailableProviders []string `json:"available_providers"`

	// ProviderCount is the number of available providers
	ProviderCount int `json:"provider_count"`

	// FailoverEnabled indicates if failover is possible (more than 1 provider)
	FailoverEnabled bool `json:"failover_enabled"`

	// PrimaryProvider is the first provider in the chain (used first)
	PrimaryProvider string `json:"primary_provider"`

	// FailoverProviders are the backup providers (used if primary fails)
	FailoverProviders []string `json:"failover_providers,omitempty"`
}

// GetProviderInfo returns information about the configured provider chain.
// This is useful for status endpoints and observability dashboards to display
// the AI failover configuration.
//
// Example output:
//
//	{
//	  "available_providers": ["openai", "anthropic"],
//	  "provider_count": 2,
//	  "failover_enabled": true,
//	  "primary_provider": "openai",
//	  "failover_providers": ["anthropic"]
//	}
func (c *ChainClient) GetProviderInfo() ChainProviderInfo {
	info := ChainProviderInfo{
		AvailableProviders: c.providerAliases,
		ProviderCount:      len(c.providers),
		FailoverEnabled:    len(c.providers) > 1,
	}

	if len(c.providerAliases) > 0 {
		info.PrimaryProvider = c.providerAliases[0]
		if len(c.providerAliases) > 1 {
			info.FailoverProviders = c.providerAliases[1:]
		}
	}

	return info
}

// classifyFailoverReason returns a short tag for what caused the chain to
// fail over to a backup provider. Used to set ai.chain.failover_reason on the
// chain span — operators can then bucket failovers by category in Jaeger or
// downstream metrics derived from spans (rate-limit vs upstream-5xx vs
// transient-proxy vs network).
//
// Categories are intentionally coarse. The child span records only the same
// bounded error category; the original error remains caller-visible.
func classifyFailoverReason(err error) string {
	if err == nil {
		return "unknown"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var classified AIRequestFailureReasoner
	if errors.As(err, &classified) {
		// Keep provider-supplied classifications bounded before using them in
		// logs, spans, or metrics.
		switch classified.AIRequestFailureReason() {
		case AIRequestFailureReasonRoute:
			return string(AIRequestFailureReasonRoute)
		}
	}
	var pe core.ProviderError
	if errors.As(err, &pe) {
		switch pe.StatusCode() {
		case 429:
			return "rate_limit"
		case 408, 504:
			return "timeout"
		case 401, 403:
			return "auth"
		case 500, 502, 503:
			return "upstream_5xx"
		}
		if pe.IsTransient() {
			return "transient_proxy"
		}
		if pe.IsRetryable() {
			return "provider_retryable"
		}
		return "provider_error"
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		if networkErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	return "unknown"
}
