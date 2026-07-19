package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// BaseClient provides common functionality for all AI providers
type BaseClient struct {
	// HTTP client with timeout
	HTTPClient *http.Client

	// Logger for debugging
	Logger core.Logger

	// Telemetry for distributed tracing
	Telemetry core.Telemetry

	// Retry configuration
	MaxRetries int
	RetryDelay time.Duration

	// Provider identity (e.g., "anthropic", "openai") — used by providerError
	ProviderName string

	// Default configuration
	DefaultModel        string
	DefaultTemperature  float32
	DefaultMaxTokens    int
	DefaultSystemPrompt string
}

// NewBaseClient creates a new base client with defaults
func NewBaseClient(timeout time.Duration, logger core.Logger) *BaseClient {
	if logger == nil {
		logger = &core.NoOpLogger{}
	}

	return &BaseClient{
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
		Logger:             logger,
		Telemetry:          nil, // Set via SetTelemetry or factory
		MaxRetries:         3,
		RetryDelay:         time.Second,
		DefaultTemperature: 0.7,
		DefaultMaxTokens:   1000,
	}
}

// SetTelemetry sets the telemetry provider for distributed tracing
func (b *BaseClient) SetTelemetry(t core.Telemetry) {
	b.Telemetry = t
}

// SetLogger updates the logger after client creation.
// This is called by Framework.applyConfigToComponent() to propagate
// the real logger to the AI client after framework initialization.
// When the logger implements ComponentAwareLogger, it creates a
// component-specific logger with "framework/ai" prefix for filtering.
func (b *BaseClient) SetLogger(logger core.Logger) {
	if logger == nil {
		b.Logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		b.Logger = cal.WithComponent("framework/ai")
	} else {
		b.Logger = logger
	}
}

// StartSpan starts a new span for AI operations if telemetry is configured.
// Returns the updated context and a span. If telemetry is nil, returns a no-op span.
// Caller is responsible for calling span.End() when the operation completes.
//
// Common identifying attributes (request_id, agent_name, ai.purpose, etc.) are
// pulled off baggage and stamped onto the new span so AI spans become greppable
// in Jaeger by request_id without joining to the orchestrator span.
func (b *BaseClient) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	if b.Telemetry != nil {
		spanCtx, span := b.Telemetry.StartSpan(ctx, name)
		telemetry.SetCommonAttrsOn(spanCtx, span)
		return spanCtx, span
	}
	return ctx, &core.NoOpSpan{}
}

func requestForAttempt(ctx context.Context, request *http.Request) (*http.Request, error) {
	clone := request.Clone(ctx)
	if request.Body == nil || request.Body == http.NoBody {
		return clone, nil
	}
	if request.GetBody == nil {
		return nil, errors.New("AI request body is not replayable")
	}

	body, err := request.GetBody()
	if err != nil {
		return nil, fmt.Errorf("recreate AI request body: %w", err)
	}
	clone.Body = body
	return clone, nil
}

// ExecuteWithRetry performs an HTTP request with exponential backoff retry.
// Each retry attempt creates a child span visible in Jaeger for debugging.
// Requests with a body must provide GetBody so every attempt can use a fresh
// copy; non-replayable bodies are rejected before the first network call, even
// when MaxRetries is zero. http.NewRequestWithContext sets GetBody automatically
// for *bytes.Buffer, *bytes.Reader, and *strings.Reader bodies.
func (b *BaseClient) ExecuteWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	var lastErr error
	if req.Body != nil && req.Body != http.NoBody {
		defer func() {
			_ = req.Body.Close()
		}()
	}

	for attempt := 0; attempt <= b.MaxRetries; attempt++ {
		// Create a span for each attempt (visible in Jaeger as child spans)
		attemptCtx, attemptSpan := b.StartSpan(ctx, "ai.http_attempt")
		attemptSpan.SetAttribute("ai.attempt", attempt+1)
		attemptSpan.SetAttribute("ai.max_retries", b.MaxRetries)
		attemptSpan.SetAttribute("ai.is_retry", attempt > 0)

		if attempt > 0 && b.Logger != nil && lastErr != nil {
			// Record retry metric (no attempt label to avoid high cardinality)
			telemetry.Counter("ai.request.retries",
				"module", telemetry.ModuleAI,
			)

			attemptSpan.SetAttribute("ai.previous_error", lastErr.Error())

			b.Logger.WarnWithContext(attemptCtx, "AI request retry attempt", map[string]interface{}{
				"operation":   "ai_request_retry",
				"attempt":     attempt,
				"max_retries": b.MaxRetries,
				"last_error":  lastErr.Error(),
			})
		}

		attemptRequest, err := requestForAttempt(attemptCtx, req)
		if err != nil {
			attemptSpan.RecordError(err)
			attemptSpan.SetAttribute("ai.attempt_status", "request_error")
			attemptSpan.SetAttribute("ai.retryable", false)
			attemptSpan.End()
			if b.Logger != nil {
				b.Logger.ErrorWithContext(attemptCtx, "AI request cannot be executed", map[string]interface{}{
					"operation":  "ai_request_error",
					"error_type": "request_body_replay",
					"retryable":  false,
				})
			}
			return nil, err
		}

		// Execute request
		attemptStart := time.Now()
		// #nosec G704 -- provider clients build these requests from explicit provider config.
		resp, err := b.HTTPClient.Do(attemptRequest)
		attemptDuration := time.Since(attemptStart)

		attemptSpan.SetAttribute("ai.attempt_duration_ms", attemptDuration.Milliseconds())

		// Success - return if no error and status is not retryable
		if err == nil && resp.StatusCode < 400 {
			attemptSpan.SetAttribute("ai.attempt_status", "success")
			attemptSpan.SetAttribute("http.status_code", resp.StatusCode)
			attemptSpan.End()

			if b.Logger != nil {
				if attempt > 0 {
					// Retry recovery - log at INFO level
					b.Logger.InfoWithContext(ctx, "AI request succeeded after retry", map[string]interface{}{
						"operation":          "ai_request_recovery",
						"successful_attempt": attempt + 1,
						"total_attempts":     attempt + 1,
					})
				} else {
					// First attempt success - log at DEBUG level
					b.Logger.DebugWithContext(ctx, "AI HTTP request completed", map[string]interface{}{
						"operation":   "ai_http_success",
						"status_code": resp.StatusCode,
						"duration_ms": attemptDuration.Milliseconds(),
					})
				}
			}
			return resp, nil
		}

		// Handle 4xx client errors (except 429 rate limit which is always retried).
		// All LLM API providers return application/json for error responses.
		// A text/html response means the request was intercepted by a CDN/proxy
		// (e.g., Cloudflare returning HTML 400 after rate limiting) and never
		// reached the actual API. These are retried like server errors.
		if err == nil && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
			contentType := resp.Header.Get("Content-Type")
			if !strings.Contains(contentType, "text/html") {
				// Real API error — non-retryable, return immediately
				attemptSpan.SetAttribute("ai.attempt_status", "client_error")
				attemptSpan.SetAttribute("http.status_code", resp.StatusCode)
				attemptSpan.SetAttribute("ai.retryable", false)
				attemptSpan.End()

				if b.Logger != nil {
					b.Logger.ErrorWithContext(ctx, "AI request failed with non-retryable error", map[string]interface{}{
						"operation":   "ai_request_error",
						"status_code": resp.StatusCode,
						"error_type":  "client_error",
						"retryable":   false,
					})
				}
				return resp, nil
			}

			// CDN/proxy HTML error — treat as retryable (transient proxy/infra issue)
			lastErr = &providerError{
				statusCode: resp.StatusCode,
				provider:   b.ProviderName,
				message:    fmt.Sprintf("non-API error response (status %d, content-type: %s)", resp.StatusCode, contentType),
				transient:  true,
			}
			attemptSpan.RecordError(lastErr)
			attemptSpan.SetAttribute("ai.attempt_status", "proxy_error")
			attemptSpan.SetAttribute("http.status_code", resp.StatusCode)
			attemptSpan.SetAttribute("http.content_type", contentType)
			attemptSpan.SetAttribute("ai.retryable", true)
			attemptSpan.End()
			_ = resp.Body.Close()

			if b.Logger != nil {
				b.Logger.WarnWithContext(attemptCtx, "Non-API error response, retrying", map[string]interface{}{
					"operation":    "ai_proxy_error",
					"status_code":  resp.StatusCode,
					"content_type": contentType,
					"attempt":      attempt + 1,
					"max_retries":  b.MaxRetries,
				})
			}
		} else if err != nil {
			// Network error — retryable
			lastErr = err
			attemptSpan.RecordError(err)
			attemptSpan.SetAttribute("ai.attempt_status", "network_error")
			attemptSpan.SetAttribute("ai.retryable", true)
			attemptSpan.End()
		} else {
			// Server error (5xx) or 429 rate limit — retryable
			lastErr = fmt.Errorf("server error: status %d", resp.StatusCode)
			attemptSpan.RecordError(lastErr)
			attemptSpan.SetAttribute("ai.attempt_status", "server_error")
			attemptSpan.SetAttribute("http.status_code", resp.StatusCode)
			attemptSpan.SetAttribute("ai.retryable", true)
			attemptSpan.End()
			_ = resp.Body.Close()
		}

		// Check if we should retry
		if attempt < b.MaxRetries {
			// Calculate delay with exponential backoff
			// Ensure safe conversion to uint to prevent overflow
			var shiftAmount uint
			if attempt >= 0 && attempt < 32 {
				shiftAmount = uint(attempt)
			} else {
				shiftAmount = 31 // Cap at max reasonable value
			}
			delay := b.RetryDelay * time.Duration(1<<shiftAmount)

			if b.Logger != nil {
				b.Logger.WarnWithContext(ctx, "AI request failed, retrying", map[string]interface{}{
					"operation":        "ai_request_retry_wait",
					"attempt":          attempt + 1,
					"max_retries":      b.MaxRetries,
					"retry_delay_ms":   delay.Milliseconds(),
					"error":            lastErr.Error(),
					"error_type":       fmt.Sprintf("%T", lastErr),
					"backoff_strategy": "exponential",
				})
			}

			// Wait before retry
			select {
			case <-time.After(delay):
				// Continue to next attempt
			case <-ctx.Done():
				if b.Logger != nil {
					b.Logger.ErrorWithContext(ctx, "AI request cancelled during retry", map[string]interface{}{
						"operation":     "ai_request_cancelled",
						"cancelled_at":  attempt + 1,
						"context_error": ctx.Err().Error(),
					})
				}
				return nil, ctx.Err()
			}
		}
	}

	// Record final failure metric
	telemetry.Counter("ai.request.failures",
		"module", telemetry.ModuleAI,
		"reason", "exhausted_retries",
	)

	if b.Logger != nil {
		b.Logger.ErrorWithContext(ctx, "AI request failed after all retries", map[string]interface{}{
			"operation":      "ai_request_final_failure",
			"total_attempts": b.MaxRetries + 1,
			"final_error":    lastErr.Error(),
			"error_type":     fmt.Sprintf("%T", lastErr),
		})
	}

	return nil, fmt.Errorf("request failed after %d retries: %w", b.MaxRetries, lastErr)
}

// LogError logs an error with provider context
func (b *BaseClient) LogError(provider string, err error) {
	b.Logger.Error("Provider error", map[string]interface{}{
		"provider": provider,
		"error":    err.Error(),
	})
}

// ApplyDefaults applies default values to options if not set
func (b *BaseClient) ApplyDefaults(options *core.AIOptions) *core.AIOptions {
	if options == nil {
		options = &core.AIOptions{}
	}

	// Apply defaults for unset values
	if options.Model == "" && b.DefaultModel != "" {
		options.Model = b.DefaultModel
	}

	if options.Temperature == 0 {
		options.Temperature = b.DefaultTemperature
	}

	if options.MaxTokens == 0 {
		options.MaxTokens = b.DefaultMaxTokens
	}

	if options.SystemPrompt == "" && b.DefaultSystemPrompt != "" {
		options.SystemPrompt = b.DefaultSystemPrompt
	}

	return options
}

// providerError implements core.ProviderError.
// It carries structured metadata so callers can make routing decisions
// (e.g., failover vs fail-fast) without string-matching error messages.
type providerError struct {
	statusCode int
	provider   string
	model      string
	message    string
	transient  bool
	retryable  bool // True for terminal-but-provider-specific errors (billing, account suspension)
}

func (e *providerError) Error() string     { return e.message }
func (e *providerError) StatusCode() int   { return e.statusCode }
func (e *providerError) Provider() string  { return e.provider }
func (e *providerError) Model() string     { return e.model }
func (e *providerError) IsTransient() bool { return e.transient }
func (e *providerError) IsRetryable() bool { return e.retryable }

// Verify interface compliance at compile time
var _ core.ProviderError = (*providerError)(nil)

// billingExhaustedPhrases are case-insensitive substrings that indicate the
// failure is a billing/quota exhaustion rather than a malformed request.
//
// These show up in 4xx error bodies from various providers when the account
// has run out of credit, hit a hard quota cap, or otherwise needs payment
// action. The classification is intentionally narrow — we only want to catch
// terminal-but-provider-specific failures that may succeed on a different
// provider in a chain, not arbitrary 4xx errors.
//
// Sources verified against actual provider responses:
//   - Anthropic returns 400 + invalid_request_error + "Your credit balance is too low..."
//     (verified against the exact production payload that triggered this fix)
//   - OpenAI returns 429 + insufficient_quota when usage limits are hit
//   - HTTP 402 Payment Required is the RFC-defined billing status code, included
//     defensively
//
// Why no bare "billing" phrase: it would match unrelated content like "billing
// address", "billing region", or "billing-enabled project" — generating false
// positives that turn a real malformed-input error into an expensive chain-wide
// retry storm. The current phrases are all unambiguous indicators of exhaustion.
//
// Adding new phrases is intentionally a code change, not a config knob —
// false positives are expensive in this code path. Be conservative.
var billingExhaustedPhrases = []string{
	"credit balance",     // Anthropic: "Your credit balance is too low to access the Anthropic API"
	"insufficient_quota", // OpenAI structured error code (also lowercase form)
	"insufficient quota", // OpenAI free-text variant
	"payment required",   // HTTP 402 description text
	"payment_required",   // structured error code variant
}

// isBillingExhausted reports whether the response body contains any of the
// well-known billing/quota exhaustion markers. The check is case-insensitive
// and operates on raw bytes without allocating an intermediate string.
//
// This is intentionally a substring scan, not JSON parsing — provider error
// bodies have inconsistent shapes, and a tight allow-list of phrases is more
// robust across schema drift than navigating per-provider JSON paths.
func isBillingExhausted(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	// Case-insensitive: lowercase the body once for the comparison.
	// Provider error bodies are typically <2 KB so this is cheap.
	lower := bytes.ToLower(body)
	for _, phrase := range billingExhaustedPhrases {
		if bytes.Contains(lower, []byte(phrase)) {
			return true
		}
	}
	return false
}

// HandleError processes API errors consistently, returning a structured
// core.ProviderError that carries status code, provider, and model metadata.
//
// Retryability classification:
//   - 4xx with a billing/quota marker in the body → IsRetryable() = true
//     (terminal on this provider but may succeed on a different one in a chain)
//   - everything else → IsRetryable() = false
//   - IsTransient() is always false here (those are CDN/proxy errors detected
//     in ExecuteWithRetry, not real API errors)
func (b *BaseClient) HandleError(statusCode int, body []byte, provider, model string) error {
	var msg string
	switch statusCode {
	case http.StatusUnauthorized:
		msg = fmt.Sprintf("%s API error: invalid or missing API key", provider)
	case http.StatusTooManyRequests:
		msg = fmt.Sprintf("%s API error: rate limit exceeded", provider)
	case http.StatusBadRequest:
		msg = fmt.Sprintf("%s API error: invalid request - %s", provider, string(body))
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		msg = fmt.Sprintf("%s API error: service temporarily unavailable (status %d)", provider, statusCode)
	default:
		msg = fmt.Sprintf("%s API error (status %d): %s", provider, statusCode, string(body))
	}

	// Classify billing exhaustion as retryable so the chain client fails over.
	// This is the only case where a 4xx error body matters for routing — every
	// other classification is based on status code alone.
	retryable := statusCode >= 400 && statusCode < 500 && isBillingExhausted(body)

	return &providerError{
		statusCode: statusCode,
		provider:   strings.ToLower(provider),
		model:      model,
		message:    msg,
		transient:  false, // Real API errors are never transient proxy errors
		retryable:  retryable,
	}
}

// LogRequest logs outgoing API requests
func (b *BaseClient) LogRequest(provider, model, prompt string) {
	b.Logger.Info("AI request initiated", map[string]interface{}{
		"operation":     "ai_request",
		"provider":      provider,
		"model":         model,
		"prompt_length": len(prompt),
		"max_tokens":    b.DefaultMaxTokens,
		"temperature":   b.DefaultTemperature,
	})

	// Log full prompt content at DEBUG level for troubleshooting
	b.Logger.Debug("AI request prompt content", map[string]interface{}{
		"operation": "ai_request_content",
		"provider":  provider,
		"model":     model,
		"prompt":    prompt,
	})
}

// LogResponse logs API responses with trace correlation
func (b *BaseClient) LogResponse(ctx context.Context, provider, model string, tokens core.TokenUsage, duration time.Duration) {
	// Record AI request metrics using unified telemetry
	telemetry.RecordAIRequest(telemetry.ModuleAI, provider,
		float64(duration.Milliseconds()), "success", "model", model)

	// Record token usage
	if tokens.PromptTokens > 0 {
		telemetry.RecordAITokens(telemetry.ModuleAI, provider, "input", int64(tokens.PromptTokens), "model", model)
	}
	if tokens.CompletionTokens > 0 {
		telemetry.RecordAITokens(telemetry.ModuleAI, provider, "output", int64(tokens.CompletionTokens), "model", model)
	}

	b.Logger.InfoWithContext(ctx, "AI response received", map[string]interface{}{
		"operation":         "ai_response",
		"provider":          provider,
		"model":             model,
		"prompt_tokens":     tokens.PromptTokens,
		"completion_tokens": tokens.CompletionTokens,
		"total_tokens":      tokens.TotalTokens,
		"duration_ms":       duration.Milliseconds(),
		"tokens_per_second": float64(tokens.TotalTokens) / duration.Seconds(),
		"status":            "success",
	})
}

// LogResponseContent logs the full response content at DEBUG level
func (b *BaseClient) LogResponseContent(provider, model, content string) {
	b.Logger.Debug("AI response content", map[string]interface{}{
		"operation":       "ai_response_content",
		"provider":        provider,
		"model":           model,
		"response":        content,
		"response_length": len(content),
	})
}

// RetryConfig holds retry configuration
type RetryConfig struct {
	MaxRetries int
	RetryDelay time.Duration
	// Optional: custom retry predicate
	ShouldRetry func(resp *http.Response, err error) bool
}

// DefaultRetryConfig returns sensible retry defaults
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: 3,
		RetryDelay: time.Second,
		ShouldRetry: func(resp *http.Response, err error) bool {
			// Retry on network errors
			if err != nil {
				return true
			}
			// Retry on 5xx errors
			if resp != nil && resp.StatusCode >= 500 {
				return true
			}
			// Retry on rate limit (with backoff)
			if resp != nil && resp.StatusCode == 429 {
				return true
			}
			return false
		},
	}
}
