package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

const defaultRetryWaitCap = 180 * time.Second

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
	if ctx == nil {
		ctx = context.Background()
	}
	if b.Telemetry == nil {
		return ctx, &core.NoOpSpan{}
	}
	spanCtx, span := b.Telemetry.StartSpan(ctx, name)
	if spanCtx == nil {
		spanCtx = ctx
	}
	if span == nil {
		span = &core.NoOpSpan{}
	}
	telemetry.SetCommonAttrsOn(spanCtx, span)
	return spanCtx, span
}

// RequestObservation is safe request metadata for framework logs. Provider is
// the bounded base provider name; ProviderAlias and SemanticModel are emitted
// only to logs and spans, never metric labels.
type RequestObservation struct {
	Provider      string
	ProviderAlias string
	SemanticModel string
	PromptLength  int
}

// ResponseObservation is safe response metadata for framework logs. Metric
// emission deliberately projects only the bounded base provider, status,
// duration, usage, and token type; response identity is never a label.
type ResponseObservation struct {
	Provider          string
	ProviderAlias     string
	SemanticModel     string
	ResponseModel     string
	ProviderRequestID string
	Usage             core.TokenUsage
	Duration          time.Duration
}

// responseMetricProjection is the complete response metadata allowed to reach
// the bounded AI metric APIs. Keeping the projection as a separate value makes
// it testable that provider aliases and provider-reported identity cannot alter
// metric labels.
type responseMetricProjection struct {
	Provider         string
	DurationMillis   float64
	PromptTokens     int64
	CompletionTokens int64
}

func projectResponseMetrics(observation ResponseObservation) responseMetricProjection {
	return responseMetricProjection{
		Provider:         normalizeObservationProvider(observation.Provider),
		DurationMillis:   float64(observation.Duration.Milliseconds()),
		PromptTokens:     int64(observation.Usage.PromptTokens),
		CompletionTokens: int64(observation.Usage.CompletionTokens),
	}
}

// ErrorObservation is safe provider-error metadata for framework logs.
// ErrorType is normalized to the closed observation classifier below.
type ErrorObservation struct {
	Operation     string
	Provider      string
	ProviderAlias string
	ErrorType     string
	Duration      time.Duration
}

func normalizeObservationProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "azureopenai", "bedrock", "gemini", "openai":
		return strings.ToLower(strings.TrimSpace(provider))
	case "":
		return "unknown"
	default:
		return "other"
	}
}

// NormalizeObservationErrorType constrains error classification used by logs,
// metrics, and spans. It intentionally never derives a label from err.Error().
func NormalizeObservationErrorType(value string) string {
	switch value {
	case "invalid_request", "route", "policy", "credential", "transport",
		"provider_client", "provider_rate_limit", "provider_server", "decode",
		"callback", "partial_stream", "cancelled", "deadline":
		return value
	default:
		return "unknown"
	}
}

// SanitizedObservationError returns a bounded classifier and a safe error for
// framework observations while leaving the original error untouched for the
// caller. fallback must be one of the closed observation classifiers.
func SanitizedObservationError(err error, fallback string) (string, error) {
	errorType := NormalizeObservationErrorType(fallback)
	switch {
	case errors.Is(err, context.Canceled):
		errorType = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		errorType = "deadline"
	case errors.Is(err, core.ErrStreamPartiallyCompleted):
		errorType = "partial_stream"
	default:
		var transportErr *transportRequestError
		var providerErr core.ProviderError
		switch {
		case errors.As(err, &transportErr):
			errorType = "transport"
		case errors.As(err, &providerErr):
			switch {
			case providerErr.IsTransient():
				errorType = "transport"
			case providerErr.StatusCode() == http.StatusTooManyRequests:
				errorType = "provider_rate_limit"
			case providerErr.StatusCode() >= http.StatusInternalServerError:
				errorType = "provider_server"
			default:
				errorType = "provider_client"
			}
		}
	}
	errorType = NormalizeObservationErrorType(errorType)
	return errorType, errors.New("AI provider request failed: " + errorType)
}

// RecordObservationError records only a sanitized error on a provider-owned
// span. The original error remains available to return to the caller.
func RecordObservationError(span core.Span, err error, fallback string) string {
	errorType, safeError := SanitizedObservationError(err, fallback)
	if span == nil {
		return errorType
	}
	span.SetAttribute("ai.error_type", errorType)
	span.RecordError(safeError)
	return errorType
}

func normalizeObservationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// AddObservationRequestID copies the request ID into a log field set using the
// framework's baggage-first, core-context-fallback precedence.
func AddObservationRequestID(ctx context.Context, fields map[string]interface{}) {
	if fields == nil {
		return
	}
	ctx = normalizeObservationContext(ctx)
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = core.GetRequestID(ctx)
	}
	if requestID != "" {
		fields["request_id"] = requestID
	}
}

// LogRequestMetadata emits context-correlated metadata without prompt content.
func (b *BaseClient) LogRequestMetadata(ctx context.Context, observation RequestObservation) {
	if b == nil || b.Logger == nil {
		return
	}
	fields := map[string]interface{}{
		"operation":      "ai_request",
		"provider":       normalizeObservationProvider(observation.Provider),
		"provider_alias": observation.ProviderAlias,
		"model":          observation.SemanticModel,
		"prompt_length":  observation.PromptLength,
	}
	ctx = normalizeObservationContext(ctx)
	AddObservationRequestID(ctx, fields)
	b.Logger.InfoWithContext(ctx, "AI request initiated", fields)
}

// LogResponseMetadata emits context-correlated metadata and bounded metrics.
func (b *BaseClient) LogResponseMetadata(ctx context.Context, observation ResponseObservation) {
	metrics := projectResponseMetrics(observation)
	telemetry.RecordAIRequest(
		telemetry.ModuleAI,
		metrics.Provider,
		metrics.DurationMillis,
		"success",
	)
	if metrics.PromptTokens > 0 {
		telemetry.RecordAITokens(
			telemetry.ModuleAI,
			metrics.Provider,
			"input",
			metrics.PromptTokens,
		)
	}
	if metrics.CompletionTokens > 0 {
		telemetry.RecordAITokens(
			telemetry.ModuleAI,
			metrics.Provider,
			"output",
			metrics.CompletionTokens,
		)
	}
	if b == nil || b.Logger == nil {
		return
	}
	fields := map[string]interface{}{
		"operation":         "ai_response",
		"provider":          metrics.Provider,
		"provider_alias":    observation.ProviderAlias,
		"model":             observation.SemanticModel,
		"prompt_tokens":     observation.Usage.PromptTokens,
		"completion_tokens": observation.Usage.CompletionTokens,
		"total_tokens":      observation.Usage.TotalTokens,
		"duration_ms":       observation.Duration.Milliseconds(),
		"status":            "success",
	}
	if observation.ResponseModel != "" {
		fields["response_model"] = observation.ResponseModel
	}
	if observation.ProviderRequestID != "" {
		fields["provider_request_id"] = observation.ProviderRequestID
	}
	ctx = normalizeObservationContext(ctx)
	AddObservationRequestID(ctx, fields)
	b.Logger.InfoWithContext(ctx, "AI response received", fields)
}

// LogErrorMetadata emits only a bounded classifier and sanitized message.
func (b *BaseClient) LogErrorMetadata(ctx context.Context, observation ErrorObservation) {
	if b == nil || b.Logger == nil {
		return
	}
	errorType := NormalizeObservationErrorType(observation.ErrorType)
	fields := map[string]interface{}{
		"operation":      observation.Operation,
		"provider":       normalizeObservationProvider(observation.Provider),
		"provider_alias": observation.ProviderAlias,
		"status":         "error",
		"error":          "AI provider request failed: " + errorType,
		"error_type":     errorType,
		"duration_ms":    observation.Duration.Milliseconds(),
	}
	ctx = normalizeObservationContext(ctx)
	AddObservationRequestID(ctx, fields)
	b.Logger.ErrorWithContext(ctx, "AI provider request failed", fields)
}

func (b *BaseClient) logDebugWithContext(ctx context.Context, message string, fields map[string]interface{}) {
	if b == nil || b.Logger == nil {
		return
	}
	ctx = normalizeObservationContext(ctx)
	AddObservationRequestID(ctx, fields)
	b.Logger.DebugWithContext(ctx, message, fields)
}

func (b *BaseClient) logInfoWithContext(ctx context.Context, message string, fields map[string]interface{}) {
	if b == nil || b.Logger == nil {
		return
	}
	ctx = normalizeObservationContext(ctx)
	AddObservationRequestID(ctx, fields)
	b.Logger.InfoWithContext(ctx, message, fields)
}

func (b *BaseClient) logWarnWithContext(ctx context.Context, message string, fields map[string]interface{}) {
	if b == nil || b.Logger == nil {
		return
	}
	ctx = normalizeObservationContext(ctx)
	AddObservationRequestID(ctx, fields)
	b.Logger.WarnWithContext(ctx, message, fields)
}

func (b *BaseClient) logErrorWithContext(ctx context.Context, message string, fields map[string]interface{}) {
	if b == nil || b.Logger == nil {
		return
	}
	ctx = normalizeObservationContext(ctx)
	AddObservationRequestID(ctx, fields)
	b.Logger.ErrorWithContext(ctx, message, fields)
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

// RequestAttemptPreparer applies per-attempt transport state, such as a
// rotating credential, to a fresh replayable request. Implementations must not
// retain or mutate the original logical request.
type RequestAttemptPreparer func(context.Context, *http.Request) error

type transportRequestError struct {
	cause error
}

func (e *transportRequestError) Error() string { return "AI HTTP transport request failed" }

func (e *transportRequestError) Unwrap() error { return e.cause }

// ExecuteWithRetry performs an HTTP request with exponential backoff retry.
// Each retry attempt creates a child span visible in Jaeger for debugging.
// Requests with a body must provide GetBody so every attempt can use a fresh
// copy; non-replayable bodies are rejected before the first network call, even
// when MaxRetries is zero. http.NewRequestWithContext sets GetBody automatically
// for *bytes.Buffer, *bytes.Reader, and *strings.Reader bodies.
func (b *BaseClient) ExecuteWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	return b.executeWithRetry(ctx, req, nil)
}

// ExecuteWithRetryPrepared behaves like ExecuteWithRetry and invokes prepare
// exactly once for every transport attempt after the request body has been
// recreated and before the configured RoundTripper sees it.
func (b *BaseClient) ExecuteWithRetryPrepared(
	ctx context.Context,
	req *http.Request,
	prepare RequestAttemptPreparer,
) (*http.Response, error) {
	if prepare == nil {
		return nil, errors.New("AI request attempt preparer is nil")
	}
	return b.executeWithRetry(ctx, req, prepare)
}

func (b *BaseClient) executeWithRetry(
	ctx context.Context,
	req *http.Request,
	prepare RequestAttemptPreparer,
) (*http.Response, error) {
	var lastErr error
	lastErrorType := "unknown"
	if req.Body != nil && req.Body != http.NoBody {
		defer func() {
			_ = req.Body.Close()
		}()
	}

	for attempt := 0; attempt <= b.MaxRetries; attempt++ {
		attemptCtx, attemptSpan := b.StartSpan(ctx, "ai.http_attempt")
		attemptSpan.SetAttribute("ai.attempt", attempt+1)
		attemptSpan.SetAttribute("ai.max_retries", b.MaxRetries)
		attemptSpan.SetAttribute("ai.is_retry", attempt > 0)

		if attempt > 0 && b.Logger != nil && lastErr != nil {
			// Record retry metric (no attempt label to avoid high cardinality)
			telemetry.Counter("ai.request.retries",
				"module", telemetry.ModuleAI,
			)

			_, previousError := SanitizedObservationError(lastErr, lastErrorType)
			attemptSpan.SetAttribute("ai.previous_error", previousError.Error())

			b.logWarnWithContext(attemptCtx, "AI request retry attempt", map[string]interface{}{
				"operation":   "ai_request_retry",
				"status":      "retry",
				"attempt":     attempt,
				"max_retries": b.MaxRetries,
				"error":       previousError.Error(),
				"error_type":  NormalizeObservationErrorType(lastErrorType),
			})
		}

		attemptRequest, err := requestForAttempt(attemptCtx, req)
		if err != nil {
			RecordObservationError(attemptSpan, err, "invalid_request")
			attemptSpan.SetAttribute("ai.attempt_status", "request_error")
			attemptSpan.SetAttribute("ai.retryable", false)
			attemptSpan.End()
			b.LogErrorMetadata(attemptCtx, ErrorObservation{
				Operation: "ai_request_error",
				Provider:  b.ProviderName,
				ErrorType: "invalid_request",
			})
			return nil, err
		}
		if prepare != nil {
			if err := prepare(attemptCtx, attemptRequest); err != nil {
				if attemptRequest.Body != nil {
					_ = attemptRequest.Body.Close()
				}
				RecordObservationError(attemptSpan, err, "credential")
				attemptSpan.SetAttribute("ai.attempt_status", "request_error")
				attemptSpan.SetAttribute("ai.retryable", false)
				attemptSpan.End()
				b.LogErrorMetadata(attemptCtx, ErrorObservation{
					Operation: "ai_request_error",
					Provider:  b.ProviderName,
					ErrorType: "credential",
				})
				return nil, err
			}
		}

		// Execute request
		attemptStart := time.Now()
		// #nosec G704 -- provider clients build these requests from explicit provider config.
		resp, err := b.HTTPClient.Do(attemptRequest)
		if err != nil {
			// net/http errors can embed the complete request URL, including trusted
			// resolver query values. Preserve the cause for errors.Is/errors.As but
			// expose only a sanitized message to spans and framework logs.
			err = &transportRequestError{cause: err}
		}
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
					b.logInfoWithContext(ctx, "AI request succeeded after retry", map[string]interface{}{
						"operation":          "ai_request_recovery",
						"status":             "recovered",
						"successful_attempt": attempt + 1,
						"total_attempts":     attempt + 1,
						"duration_ms":        attemptDuration.Milliseconds(),
					})
				} else {
					// First attempt success - log at DEBUG level
					b.logDebugWithContext(ctx, "AI HTTP request completed", map[string]interface{}{
						"operation":   "ai_http_success",
						"status":      "success",
						"status_code": resp.StatusCode,
						"duration_ms": attemptDuration.Milliseconds(),
					})
				}
			}
			return resp, nil
		}

		isHTMLResponse := err == nil && strings.Contains(
			strings.ToLower(resp.Header.Get("Content-Type")),
			"text/html",
		)
		if err == nil && resp.StatusCode >= 400 && resp.StatusCode < 500 &&
			resp.StatusCode != http.StatusTooManyRequests && !isHTMLResponse {
			// Real API error — non-retryable, return immediately
			RecordObservationError(
				attemptSpan,
				errors.New("AI provider returned a non-retryable client response"),
				"provider_client",
			)
			attemptSpan.SetAttribute("ai.attempt_status", "client_error")
			attemptSpan.SetAttribute("http.status_code", resp.StatusCode)
			attemptSpan.SetAttribute("ai.retryable", false)
			attemptSpan.End()

			b.LogErrorMetadata(ctx, ErrorObservation{
				Operation: "ai_request_error",
				Provider:  b.ProviderName,
				ErrorType: "provider_client",
			})
			return resp, nil
		} else if isHTMLResponse {
			lastErr = &providerError{
				statusCode: resp.StatusCode,
				provider:   b.ProviderName,
				message:    fmt.Sprintf("non-API provider response (status %d)", resp.StatusCode),
				transient:  true,
			}
			lastErrorType = "transport"
			RecordObservationError(attemptSpan, lastErr, lastErrorType)
			attemptSpan.SetAttribute("ai.attempt_status", "proxy_error")
			attemptSpan.SetAttribute("http.status_code", resp.StatusCode)
			attemptSpan.SetAttribute("http.content_type", "text/html")
			attemptSpan.SetAttribute("ai.retryable", true)
			attemptSpan.End()

			if attempt < b.MaxRetries && b.Logger != nil {
				b.logWarnWithContext(attemptCtx, "Non-API error response, retrying", map[string]interface{}{
					"operation":   "ai_proxy_error",
					"status":      "retry",
					"status_code": resp.StatusCode,
					"attempt":     attempt + 1,
					"max_retries": b.MaxRetries,
				})
			}
		} else if err != nil {
			// Network error — retryable
			lastErr = err
			lastErrorType = "transport"
			RecordObservationError(attemptSpan, err, lastErrorType)
			attemptSpan.SetAttribute("ai.attempt_status", "network_error")
			attemptSpan.SetAttribute("ai.retryable", true)
			attemptSpan.End()
		} else {
			// Server error (5xx) or 429 rate limit — retryable. Keep the final
			// response open so the provider can decode its bounded error envelope.
			lastErrorType = "provider_server"
			if resp.StatusCode == http.StatusTooManyRequests {
				lastErrorType = "provider_rate_limit"
			}
			lastErr = &providerError{
				statusCode: resp.StatusCode,
				provider:   b.ProviderName,
				message:    "AI provider returned a retryable response",
			}
			RecordObservationError(attemptSpan, lastErr, lastErrorType)
			attemptSpan.SetAttribute("ai.attempt_status", "server_error")
			attemptSpan.SetAttribute("http.status_code", resp.StatusCode)
			attemptSpan.SetAttribute("ai.retryable", true)
			attemptSpan.End()
			if attempt == b.MaxRetries {
				b.recordRetryExhaustion(ctx, lastErr, lastErrorType)
				return resp, nil
			}
		}

		if attempt == b.MaxRetries {
			closeResponse(resp)
			b.recordRetryExhaustion(ctx, lastErr, lastErrorType)
			return nil, fmt.Errorf("request failed after %d retries: %w", b.MaxRetries, lastErr)
		}

		backoff := exponentialDelay(b.RetryDelay, attempt)
		delay, source := retryDelay(backoff, resp, time.Now())
		waitCap := b.retryWaitCap()
		effectiveDelay := retryDelayWithinDeadline(ctx, min(delay, waitCap))
		closeResponse(resp)
		if b.Logger != nil {
			_, safeError := SanitizedObservationError(lastErr, lastErrorType)
			b.logWarnWithContext(ctx, "AI request failed, retrying", map[string]interface{}{
				"operation":        "ai_request_retry_wait",
				"status":           "retry",
				"attempt":          attempt + 1,
				"max_retries":      b.MaxRetries,
				"retry_delay_ms":   effectiveDelay.Milliseconds(),
				"error":            safeError.Error(),
				"error_type":       NormalizeObservationErrorType(lastErrorType),
				"backoff_strategy": source,
			})
		}
		if err := waitForRetry(ctx, delay, waitCap); err != nil {
			if b.Logger != nil {
				errorType, safeError := SanitizedObservationError(err, "cancelled")
				b.logErrorWithContext(ctx, "AI request cancelled during retry", map[string]interface{}{
					"operation":    "ai_request_cancelled",
					"status":       "error",
					"cancelled_at": attempt + 1,
					"error":        safeError.Error(),
					"error_type":   errorType,
				})
			}
			return nil, err
		}
	}
	return nil, errors.New("AI retry loop terminated unexpectedly")
}

// retryWaitCap bounds a single retry sleep by the configured HTTP timeout, or
// the framework operation default when that timeout is disabled. It does not
// wrap the transport context: a successful streaming response keeps using that
// context after ExecuteWithRetry returns.
func (b *BaseClient) retryWaitCap() time.Duration {
	waitCap := defaultRetryWaitCap
	if b != nil && b.HTTPClient != nil && b.HTTPClient.Timeout > 0 {
		waitCap = b.HTTPClient.Timeout
	}
	return waitCap
}

func (b *BaseClient) recordRetryExhaustion(ctx context.Context, err error, errorType string) {
	telemetry.Counter("ai.request.failures",
		"module", telemetry.ModuleAI,
		"reason", "exhausted_retries",
	)
	if b == nil || b.Logger == nil {
		return
	}
	_, safeError := SanitizedObservationError(err, errorType)
	b.logErrorWithContext(ctx, "AI request failed after all retries", map[string]interface{}{
		"operation":      "ai_request_final_failure",
		"status":         "error",
		"total_attempts": b.MaxRetries + 1,
		"error":          safeError.Error(),
		"error_type":     NormalizeObservationErrorType(errorType),
	})
}

func closeResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
}

func exponentialDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempt <= 0 {
		return base
	}
	if attempt >= 63 {
		return time.Duration(1<<63 - 1)
	}
	multiplier := time.Duration(uint64(1) << uint(attempt))
	if base > time.Duration(1<<63-1)/multiplier {
		return time.Duration(1<<63 - 1)
	}
	return base * multiplier
}

func retryDelay(backoff time.Duration, response *http.Response, now time.Time) (time.Duration, string) {
	if backoff < 0 {
		backoff = 0
	}
	if response == nil || response.Header == nil ||
		(response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusServiceUnavailable) {
		return backoff, "exponential"
	}
	serverDelay, ok := parseRetryAfter(response.Header.Get("Retry-After"), now)
	if !ok || serverDelay <= backoff {
		return backoff, "exponential"
	}
	return serverDelay, "retry_after"
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if isASCIIDecimal(value) {
		const maxSeconds = uint64((1<<63 - 1) / int64(time.Second))
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return time.Duration(1<<63 - 1), true
		}
		if seconds > maxSeconds {
			return time.Duration(1<<63 - 1), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := date.Sub(now)
	if delay < 0 {
		return 0, false
	}
	return delay, true
}

func isASCIIDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func retryDelayWithinDeadline(ctx context.Context, delay time.Duration) time.Duration {
	if delay < 0 {
		return 0
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return delay
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	if delay > remaining {
		return remaining
	}
	return delay
}

func waitForRetry(ctx context.Context, delay, waitCap time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if waitCap <= 0 {
		waitCap = defaultRetryWaitCap
	}
	capReached := delay >= waitCap
	delay = min(delay, waitCap)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			return context.DeadlineExceeded
		}
		if delay >= remaining {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	timer := time.NewTimer(max(delay, 0))
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-timer.C:
		if capReached {
			return context.DeadlineExceeded
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LogError logs a sanitized error with provider context.
//
// Deprecated: use LogErrorMetadata for request-correlated observations.
func (b *BaseClient) LogError(provider string, err error) {
	if b == nil || b.Logger == nil {
		return
	}
	errorType, safeError := SanitizedObservationError(err, "unknown")
	b.Logger.Error("AI provider request failed", map[string]interface{}{
		"operation":  "ai_request_error",
		"provider":   normalizeObservationProvider(provider),
		"status":     "error",
		"error":      safeError.Error(),
		"error_type": errorType,
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

// maxProviderErrorBodyBytes bounds error-body inspection used for internal
// provider classification. Error bodies are never an observation payload.
const maxProviderErrorBodyBytes = 64 << 10

// ReadErrorBody reads a bounded provider error response for internal
// classification. It reads one byte beyond the limit to detect overflow and
// discards all captured bytes when the read fails or the limit is exceeded.
// Returned errors contain no reader-provided text.
func ReadErrorBody(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("AI provider error body reader is nil")
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxProviderErrorBodyBytes+1))
	if err != nil {
		return nil, errors.New("read AI provider error body")
	}
	if len(body) > maxProviderErrorBodyBytes {
		return nil, errors.New("AI provider error body exceeds the supported size")
	}
	return body, nil
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
//   - HTTP 402, or another 4xx with a billing/quota marker in the body
//     → IsRetryable() = true
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
		msg = fmt.Sprintf("%s API error: invalid request", provider)
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		msg = fmt.Sprintf("%s API error: service temporarily unavailable (status %d)", provider, statusCode)
	default:
		msg = fmt.Sprintf("%s API error (status %d)", provider, statusCode)
	}

	// Classify billing exhaustion as retryable so the chain client fails over.
	// This is the only case where a 4xx error body matters for routing — every
	// other classification is based on status code alone.
	retryable := statusCode == http.StatusPaymentRequired ||
		(statusCode >= 400 && statusCode < 500 && isBillingExhausted(body))

	return &providerError{
		statusCode: statusCode,
		provider:   strings.ToLower(provider),
		model:      model,
		message:    msg,
		transient:  false, // Real API errors are never transient proxy errors
		retryable:  retryable,
	}
}

// LogRequest is retained as a safe no-op for source compatibility.
//
// Deprecated: use LogRequestMetadata with the active request context.
func (b *BaseClient) LogRequest(provider, model, prompt string) {}

// LogResponse logs API responses with trace correlation while deliberately
// omitting its ambiguous model argument from logs and metrics.
//
// Deprecated: use LogResponseMetadata with the semantic model identity.
func (b *BaseClient) LogResponse(ctx context.Context, provider, model string, tokens core.TokenUsage, duration time.Duration) {
	b.LogResponseMetadata(ctx, ResponseObservation{
		Provider: provider,
		Usage:    tokens,
		Duration: duration,
	})
}

// LogResponseContent is retained as a safe no-op for source compatibility.
//
// Deprecated: response content is not emitted by provider logging helpers.
func (b *BaseClient) LogResponseContent(provider, model, content string) {}

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
