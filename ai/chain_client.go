package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// ChainClient implements automatic failover across multiple providers (Phase 3)
// FOLLOWS FRAMEWORK PRINCIPLE: Fail-Fast for Configuration Errors, Resilient Runtime Behavior
type ChainClient struct {
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
			WithProviderAlias(alias),
			WithLogger(config.Logger),
			WithTelemetry(config.Telemetry),
		}
		// Apply timeout if configured (important for reasoning models)
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
		provider, err := NewClient(opts...)
		if err != nil {
			// Runtime failures (e.g., missing API keys) are warnings, not errors
			// This allows partial chain creation when some providers aren't configured
			logger.Warn("Provider not available (will skip in chain)", map[string]interface{}{
				"operation": "ai_chain_init",
				"alias":     alias,
				"error":     err.Error(),
				"note":      "This provider will be skipped during failover",
			})
			continue // Skip unavailable providers gracefully
		}
		client.providers = append(client.providers, provider)
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

	// Propagate to all underlying providers
	for _, provider := range c.providers {
		if loggable, ok := provider.(interface{ SetLogger(core.Logger) }); ok {
			loggable.SetLogger(logger)
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
	startTime := time.Now()

	// Start parent span for the entire chain operation
	var span core.Span = &core.NoOpSpan{}
	if c.telemetry != nil {
		ctx, span = c.telemetry.StartSpan(ctx, "ai.chain.generate_response")
	}
	defer span.End()

	// Preserve original model setting (empty or alias like "smart")
	// This is CRITICAL: providers mutate options.Model in ApplyDefaults()
	// Without this, the first provider's model bleeds into subsequent providers
	originalModel := ""
	if options != nil {
		originalModel = options.Model
	}

	// Set span attributes for the chain operation
	span.SetAttribute("ai.chain.providers_count", len(c.providers))
	span.SetAttribute("ai.chain.original_model", originalModel)
	span.SetAttribute("ai.prompt_length", len(prompt))

	// Log chain request start with trace correlation
	if c.logger != nil {
		c.logger.InfoWithContext(ctx, "Chain client request started", map[string]interface{}{
			"operation":        "ai_chain_request",
			"original_model":   originalModel,
			"providers_count":  len(c.providers),
			"provider_aliases": c.providerAliases,
			"prompt_length":    len(prompt),
		})
	}

	var lastErr error
	var failedProviders []string

	for i, provider := range c.providers {
		providerAlias := c.providerAliases[i]
		attemptStart := time.Now()

		// Clone options for each provider to avoid mutation bleeding across providers
		// This fixes the bug where first provider's resolved model is passed to all subsequent providers
		providerOpts := cloneAIOptions(options)

		// Reset model to original so each provider can apply its own defaults/resolution
		// - If original was empty: provider applies its default via "default" alias
		// - If original was alias ("smart"): provider resolves to its own model
		if providerOpts != nil {
			providerOpts.Model = originalModel
		}

		// Create child span for this provider attempt
		var attemptSpan core.Span = &core.NoOpSpan{}
		if c.telemetry != nil {
			_, attemptSpan = c.telemetry.StartSpan(ctx, "ai.chain.provider_attempt")
		}
		attemptSpan.SetAttribute("ai.chain.provider_index", i)
		attemptSpan.SetAttribute("ai.chain.provider_alias", providerAlias)
		attemptSpan.SetAttribute("ai.chain.original_model", originalModel)
		attemptSpan.SetAttribute("ai.chain.is_retry", i > 0)

		// Log provider attempt with trace correlation
		if c.logger != nil {
			c.logger.DebugWithContext(ctx, "Trying provider in chain", map[string]interface{}{
				"operation":      "ai_chain_attempt",
				"provider_index": i,
				"provider_alias": providerAlias,
				"original_model": originalModel,
				"remaining":      len(c.providers) - i - 1,
				"failed_so_far":  failedProviders,
			})
		}

		// Each provider already has circuit breaker protection internally
		// This follows the framework principle: "External API calls must be protected by circuit breakers"
		resp, err := provider.GenerateResponse(ctx, prompt, providerOpts)
		attemptDuration := time.Since(attemptStart)

		if err == nil {
			// Success!
			attemptSpan.SetAttribute("ai.chain.attempt_status", "success")
			attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", attemptDuration.Milliseconds())
			attemptSpan.End()

			// Record successful attempt metric
			telemetry.Counter("ai.chain.attempt",
				"module", telemetry.ModuleAI,
				"provider", providerAlias,
				"status", "success",
				"attempt", fmt.Sprintf("%d", i+1),
			)

			if i > 0 {
				// Record successful failover metric with details
				telemetry.Counter("ai.chain.failover",
					"module", telemetry.ModuleAI,
					"from_provider", failedProviders[len(failedProviders)-1],
					"to_provider", providerAlias,
					"failed_count", fmt.Sprintf("%d", i),
				)

				span.SetAttribute("ai.chain.failover_count", i)
				span.SetAttribute("ai.chain.successful_provider", providerAlias)
				// Explicit failover flag + classified reason — enables Jaeger
				// queries like "show traces where the chain failed over" and
				// "what category of failure caused the failover (rate-limit
				// vs upstream-5xx vs network)".
				span.SetAttribute("ai.chain.failover_occurred", true)
				span.SetAttribute("ai.chain.failover_reason", classifyFailoverReason(lastErr))

				if c.logger != nil {
					c.logger.InfoWithContext(ctx, "Chain failover succeeded", map[string]interface{}{
						"operation":           "ai_chain_failover_success",
						"failed_providers":    failedProviders,
						"successful_provider": providerAlias,
						"successful_index":    i,
						"total_duration_ms":   time.Since(startTime).Milliseconds(),
					})
				}
			} else {
				if c.logger != nil {
					c.logger.InfoWithContext(ctx, "Chain request succeeded on primary provider", map[string]interface{}{
						"operation":     "ai_chain_success",
						"provider":      providerAlias,
						"duration_ms":   attemptDuration.Milliseconds(),
						"prompt_tokens": resp.Usage.PromptTokens,
						"output_tokens": resp.Usage.CompletionTokens,
					})
				}
			}

			span.SetAttribute("ai.chain.status", "success")
			span.SetAttribute("ai.chain.total_duration_ms", time.Since(startTime).Milliseconds())
			return resp, nil
		}

		// Provider failed
		lastErr = err
		failedProviders = append(failedProviders, providerAlias)
		isClient := isClientError(err)

		// Unwrap structured error metadata once (used by all log/span sites below).
		// pe is nil for unstructured errors (network failures, context cancellation).
		var pe core.ProviderError
		hasProviderError := errors.As(err, &pe)

		attemptSpan.SetAttribute("ai.chain.attempt_status", "failed")
		attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", attemptDuration.Milliseconds())
		attemptSpan.SetAttribute("ai.chain.error", err.Error())
		attemptSpan.SetAttribute("ai.chain.is_client_error", isClient)
		// Surface the two failover-override flags as span attributes so operators
		// looking at Jaeger can immediately see WHY the chain failed over on a 4xx
		// (transient proxy vs billing/quota) without reading the error message.
		if hasProviderError {
			attemptSpan.SetAttribute("ai.chain.is_transient", pe.IsTransient())
			attemptSpan.SetAttribute("ai.chain.is_retryable", pe.IsRetryable())
		}
		attemptSpan.RecordError(err)
		attemptSpan.End()

		// Record failed attempt metric
		telemetry.Counter("ai.chain.attempt",
			"module", telemetry.ModuleAI,
			"provider", providerAlias,
			"status", "failed",
			"attempt", fmt.Sprintf("%d", i+1),
		)

		// Determine if error is retryable (follows framework's resilient runtime behavior)
		// Client errors (4xx except auth) are not retryable, server errors (5xx) are
		if isClient {
			// Fail fast on client errors - don't try other providers
			span.SetAttribute("ai.chain.status", "client_error")
			span.SetAttribute("ai.chain.abort_reason", "non_retryable_client_error")
			span.RecordError(err)

			if c.logger != nil {
				c.logger.ErrorWithContext(ctx, "Chain aborted - client error not retryable", map[string]interface{}{
					"operation":        "ai_chain_abort",
					"provider":         providerAlias,
					"provider_index":   i,
					"error":            err.Error(),
					"failed_providers": failedProviders,
					"duration_ms":      time.Since(startTime).Milliseconds(),
				})
			}

			return nil, fmt.Errorf("client error (not retrying): %w", err)
		}

		// Log transient proxy failover with structured error context
		if hasProviderError && pe.IsTransient() && c.logger != nil {
			nextProvider := "none"
			if i+1 < len(c.providerAliases) {
				nextProvider = c.providerAliases[i+1]
			}
			c.logger.WarnWithContext(ctx, "Transient proxy error, failing over to next provider", map[string]interface{}{
				"operation":     "chain_failover",
				"from_provider": providerAlias,
				"to_provider":   nextProvider,
				"error":         err.Error(),
				"status_code":   pe.StatusCode(),
				"is_transient":  pe.IsTransient(),
			})
		}

		// Log billing/quota failover with structured error context. Distinct from
		// the transient block above because the operator's response is different:
		// transient errors usually self-resolve, billing/quota errors require
		// account action (top up credits, raise quota, switch to a paid plan).
		// Operators searching for "why is the chain spending money on Groq" should
		// land on this log line and see the cost signal immediately.
		if hasProviderError && pe.IsRetryable() && c.logger != nil {
			nextProvider := "none"
			if i+1 < len(c.providerAliases) {
				nextProvider = c.providerAliases[i+1]
			}
			c.logger.WarnWithContext(ctx, "Provider terminal error (billing/quota), failing over to next provider", map[string]interface{}{
				"operation":     "chain_failover_retryable",
				"from_provider": providerAlias,
				"to_provider":   nextProvider,
				"error":         err.Error(),
				"status_code":   pe.StatusCode(),
				"is_retryable":  pe.IsRetryable(),
			})
		}

		// Log provider failure with trace correlation
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Provider failed in chain, trying next", map[string]interface{}{
				"operation":        "ai_chain_provider_failed",
				"provider":         providerAlias,
				"provider_index":   i,
				"error":            err.Error(),
				"remaining":        len(c.providers) - i - 1,
				"duration_ms":      attemptDuration.Milliseconds(),
				"failed_providers": failedProviders,
			})
		}
	}

	// Record chain exhausted metric - all providers failed
	telemetry.Counter("ai.chain.exhausted",
		"module", telemetry.ModuleAI,
		"providers_tried", fmt.Sprintf("%d", len(c.providers)),
	)

	span.SetAttribute("ai.chain.status", "exhausted")
	span.SetAttribute("ai.chain.failed_providers", strings.Join(failedProviders, ","))
	span.SetAttribute("ai.chain.total_duration_ms", time.Since(startTime).Milliseconds())
	span.RecordError(lastErr)

	if c.logger != nil {
		c.logger.ErrorWithContext(ctx, "All chain providers exhausted", map[string]interface{}{
			"operation":         "ai_chain_exhausted",
			"providers_tried":   len(c.providers),
			"failed_providers":  failedProviders,
			"final_error":       lastErr.Error(),
			"total_duration_ms": time.Since(startTime).Milliseconds(),
		})
	}

	return nil, fmt.Errorf("all %d providers failed, last error: %w", len(c.providers), lastErr)
}

// StreamResponse generates a streaming response with automatic failover
// IMPORTANT: Streaming has different failover semantics than synchronous calls:
// - Once streaming starts successfully, we commit to that provider
// - Failover only happens if the connection fails before streaming starts
// - If streaming is interrupted mid-stream, we return partial content with ErrStreamPartiallyCompleted
func (c *ChainClient) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	// Start distributed tracing span (nil-safe per FRAMEWORK_DESIGN_PRINCIPLES.md)
	var span core.Span = &core.NoOpSpan{}
	if c.telemetry != nil {
		ctx, span = c.telemetry.StartSpan(ctx, "ai.chain.stream_response")
	}
	defer span.End()

	startTime := time.Now()
	var lastErr error
	failedProviders := []string{}

	// Preserve original model setting — same pattern as GenerateResponse.
	// Without this, the first provider's resolved model bleeds into subsequent providers.
	originalModel := ""
	if options != nil {
		originalModel = options.Model
	}

	span.SetAttribute("ai.chain.total_providers", len(c.providers))
	span.SetAttribute("ai.prompt_length", len(prompt))
	span.SetAttribute("ai.streaming", true)
	span.SetAttribute("ai.chain.original_model", originalModel)

	for i, provider := range c.providers {
		alias := c.providerAliases[i]

		// Check if provider supports streaming
		streamingProvider, ok := provider.(core.StreamingAIClient)
		if !ok || !streamingProvider.SupportsStreaming() {
			// Provider doesn't support streaming, try next
			if c.logger != nil {
				c.logger.DebugWithContext(ctx, "Provider does not support streaming, skipping", map[string]interface{}{
					"operation": "ai_chain_skip",
					"provider":  alias,
					"reason":    "streaming_not_supported",
				})
			}
			failedProviders = append(failedProviders, alias)
			lastErr = fmt.Errorf("provider %s does not support streaming", alias)
			continue
		}

		// Clone options and reset model to original to prevent mutation bleeding
		optsCopy := cloneAIOptions(options)
		if optsCopy != nil {
			optsCopy.Model = originalModel
		}

		if c.logger != nil {
			c.logger.DebugWithContext(ctx, "Attempting streaming provider", map[string]interface{}{
				"operation":       "ai_chain_stream_attempt",
				"provider":        alias,
				"provider_index":  i + 1,
				"total_providers": len(c.providers),
			})
		}

		// Attempt streaming with this provider
		response, err := streamingProvider.StreamResponse(ctx, prompt, optsCopy, callback)
		if err == nil {
			// Success
			telemetry.Counter("ai.chain.stream.success",
				"module", telemetry.ModuleAI,
				"provider", alias,
				"attempt", fmt.Sprintf("%d", i+1),
			)
			telemetry.Histogram("ai.chain.stream.duration_ms",
				float64(time.Since(startTime).Milliseconds()),
				"module", telemetry.ModuleAI,
				"provider", alias,
			)

			span.SetAttribute("ai.chain.status", "success")
			span.SetAttribute("ai.chain.provider", alias)
			span.SetAttribute("ai.chain.attempt", i+1)
			if i > 0 {
				span.SetAttribute("ai.chain.failover_occurred", true)
				span.SetAttribute("ai.chain.failover_count", i)
				span.SetAttribute("ai.chain.failover_reason", classifyFailoverReason(lastErr))
				if len(failedProviders) > 0 {
					span.SetAttribute("ai.chain.failed_provider", failedProviders[len(failedProviders)-1])
				}
			}

			if c.logger != nil {
				c.logger.InfoWithContext(ctx, "Chain streaming succeeded", map[string]interface{}{
					"operation":     "ai_chain_stream_success",
					"provider":      alias,
					"attempt":       i + 1,
					"duration_ms":   time.Since(startTime).Milliseconds(),
					"response_size": len(response.Content),
				})
			}

			return response, nil
		}

		// Check for partial completion (streaming started but interrupted)
		if err == core.ErrStreamPartiallyCompleted {
			// Streaming started but was interrupted - return partial result
			telemetry.Counter("ai.chain.stream.partial",
				"module", telemetry.ModuleAI,
				"provider", alias,
			)

			span.SetAttribute("ai.chain.status", "partial")
			span.SetAttribute("ai.chain.provider", alias)

			if c.logger != nil {
				c.logger.WarnWithContext(ctx, "Streaming partially completed", map[string]interface{}{
					"operation":     "ai_chain_stream_partial",
					"provider":      alias,
					"response_size": len(response.Content),
				})
			}

			// Return partial response - don't failover as streaming already started
			return response, err
		}

		// Provider failed before streaming started - try next
		telemetry.Counter("ai.chain.stream.failure",
			"module", telemetry.ModuleAI,
			"provider", alias,
		)

		lastErr = err
		failedProviders = append(failedProviders, alias)

		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Provider streaming failed, trying next", map[string]interface{}{
				"operation":       "ai_chain_stream_failover",
				"failed_provider": alias,
				"attempt":         i + 1,
				"error":           err.Error(),
				"remaining":       len(c.providers) - i - 1,
			})
		}
	}

	// All providers exhausted
	telemetry.Counter("ai.chain.stream.exhausted",
		"module", telemetry.ModuleAI,
		"providers_tried", fmt.Sprintf("%d", len(c.providers)),
	)

	span.SetAttribute("ai.chain.status", "exhausted")
	span.SetAttribute("ai.chain.failed_providers", strings.Join(failedProviders, ","))
	span.RecordError(lastErr)

	if c.logger != nil {
		c.logger.ErrorWithContext(ctx, "All chain providers exhausted for streaming", map[string]interface{}{
			"operation":        "ai_chain_stream_exhausted",
			"providers_tried":  len(c.providers),
			"failed_providers": failedProviders,
			"final_error":      lastErr.Error(),
		})
	}

	return nil, fmt.Errorf("all %d providers failed for streaming, last error: %w", len(c.providers), lastErr)
}

// SupportsStreaming returns true if at least one provider supports streaming
func (c *ChainClient) SupportsStreaming() bool {
	for _, provider := range c.providers {
		if streamingProvider, ok := provider.(core.StreamingAIClient); ok && streamingProvider.SupportsStreaming() {
			return true
		}
	}
	return false
}

// cloneAIOptions creates a defensive copy of AIOptions to prevent mutation bleeding across providers.
// Model is still reset per provider attempt by the caller; map fields must be deep-copied.
func cloneAIOptions(opts *core.AIOptions) *core.AIOptions {
	if opts == nil {
		return nil
	}

	clone := *opts

	if opts.Extra != nil {
		clone.Extra = make(map[string]interface{}, len(opts.Extra))
		for k, v := range opts.Extra {
			clone.Extra[k] = v
		}
	}

	if opts.Headers != nil {
		clone.Headers = make(map[string]string, len(opts.Headers))
		for k, v := range opts.Headers {
			clone.Headers[k] = v
		}
	}

	return &clone
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
	ProviderAliases          []string
	Logger                   core.Logger
	Telemetry                core.Telemetry
	Timeout                  time.Duration // HTTP timeout for AI requests (0 = use provider default)
	ReasoningTokenMultiplier int           // Token multiplier for reasoning models (0 = use default 5x)
	ReasoningEffort          string        // Default reasoning effort: "none", "low", "medium", "high", "xhigh" (empty = model default)
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

// WithChainTimeout sets the HTTP timeout for AI requests in the chain
// This is important for reasoning models (GPT-5, o1, o3, o4) that need longer
// processing time for chain-of-thought responses.
// If not set, the default provider timeout (180s) is used.
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
// Categories are intentionally coarse. The full error remains on the
// ai.chain.provider_attempt child span via RecordError; this is the headline.
func classifyFailoverReason(err error) string {
	if err == nil {
		return "unknown"
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
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "context deadline") || strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "context canceled"):
		return "canceled"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") || strings.Contains(msg, "i/o timeout"):
		return "network"
	case strings.Contains(msg, "does not support streaming"):
		return "streaming_unsupported"
	}
	return "unknown"
}
