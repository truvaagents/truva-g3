package ai

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

func (c *ChainClient) startRequestSpan(ctx context.Context, name string) (context.Context, core.Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.telemetry == nil {
		return ctx, &core.NoOpSpan{}
	}
	spanCtx, span := c.telemetry.StartSpan(ctx, name)
	if spanCtx == nil {
		spanCtx = ctx
	}
	if span == nil {
		span = &core.NoOpSpan{}
	}
	telemetry.SetCommonAttrsOn(spanCtx, span)
	return spanCtx, span
}

func (c *ChainClient) logRequestDebug(ctx context.Context, message string, fields map[string]interface{}) {
	providers.AddObservationRequestID(ctx, fields)
	c.logger.DebugWithContext(ctx, message, fields)
}

func (c *ChainClient) logRequestInfo(ctx context.Context, message string, fields map[string]interface{}) {
	providers.AddObservationRequestID(ctx, fields)
	c.logger.InfoWithContext(ctx, message, fields)
}

func (c *ChainClient) logRequestWarn(ctx context.Context, message string, fields map[string]interface{}) {
	providers.AddObservationRequestID(ctx, fields)
	c.logger.WarnWithContext(ctx, message, fields)
}

func (c *ChainClient) logRequestError(ctx context.Context, message string, fields map[string]interface{}) {
	providers.AddObservationRequestID(ctx, fields)
	c.logger.ErrorWithContext(ctx, message, fields)
}

func addChainObservationError(fields map[string]interface{}, err error, fallback string) {
	errorType, safeError := sanitizedChainObservationError(err, fallback)
	fields["error"] = safeError.Error()
	fields["error_type"] = errorType
}

func recordChainObservationError(span core.Span, err error, fallback string) {
	errorType, safeError := sanitizedChainObservationError(err, fallback)
	if span == nil {
		return
	}
	span.SetAttribute("ai.error_type", errorType)
	span.RecordError(safeError)
}

func sanitizedChainObservationError(err error, fallback string) (string, error) {
	fallback = chainObservationFallback(err, fallback)
	if fallback == string(AIRequestFailureReasonRoute) {
		// A provider's explicit bounded route marker is authoritative at the
		// chain boundary after caller cancellation and deadlines take
		// precedence, even if its cause satisfies another generic contract.
		return providers.SanitizedObservationError(nil, fallback)
	}
	return providers.SanitizedObservationError(err, fallback)
}

func chainObservationFallback(err error, fallback string) string {
	if classifyFailoverReason(err) == string(AIRequestFailureReasonRoute) {
		return string(AIRequestFailureReasonRoute)
	}
	return fallback
}

// RequestFingerprint combines the ordered semantic identities of every chain
// entry. A cache entry is safe only when every possible failover target can
// provide a stable fingerprint for the same request. Chain entries are assumed
// to be semantically interchangeable: a cache hit may return output originally
// produced by any entry, even when a different entry would currently succeed.
func (c *ChainClient) RequestFingerprint(ctx context.Context, request *core.AIRequest) (string, bool) {
	if request == nil {
		return "", false
	}
	entries := c.runtimeEntries()
	if len(entries) == 0 {
		return "", false
	}
	snapshot := chainRequestFingerprint(ctx, request, entries)
	return snapshot.fingerprint, snapshot.stable
}

type chainFingerprintSnapshot struct {
	fingerprint string
	entries     []string
	stable      bool
}

func chainRequestFingerprint(
	ctx context.Context,
	request *core.AIRequest,
	entries []ChainEntry,
) chainFingerprintSnapshot {
	if request == nil || len(entries) == 0 {
		return chainFingerprintSnapshot{}
	}
	components := make([]string, 0, len(entries))
	entryFingerprints := make([]string, 0, len(entries))
	for _, entry := range entries {
		fingerprinter, ok := entry.client.(core.AIRequestFingerprinter)
		if !ok {
			return chainFingerprintSnapshot{}
		}
		entryRequest, err := core.CloneAIRequest(request)
		if err != nil {
			return chainFingerprintSnapshot{}
		}
		fingerprint, stable := fingerprinter.RequestFingerprint(ctx, entryRequest)
		if !stable || fingerprint == "" {
			return chainFingerprintSnapshot{}
		}
		components = append(components, entry.name+"="+fingerprint)
		entryFingerprints = append(entryFingerprints, fingerprint)
	}
	sum := sha256.Sum256([]byte(strings.Join(components, "\n")))
	return chainFingerprintSnapshot{
		fingerprint: fmt.Sprintf("%x", sum[:]),
		entries:     entryFingerprints,
		stable:      true,
	}
}

func attachChainFingerprint(result *core.AIResult, snapshot chainFingerprintSnapshot, entryIndex int) {
	if result == nil || result.RequestReport == nil {
		return
	}
	report := result.RequestReport
	if !snapshot.stable || entryIndex < 0 || entryIndex >= len(snapshot.entries) ||
		!report.Stable || report.Fingerprint != snapshot.entries[entryIndex] {
		report.Fingerprint = ""
		report.Stable = false
		return
	}
	report.Fingerprint = snapshot.fingerprint
	report.Stable = true
}

// Generate executes a provider-neutral request against each chain entry using
// an independent request snapshot. Request-capable clients receive the full
// request; legacy-only clients are invoked only when core can represent every
// requested feature without degradation.
func (c *ChainClient) Generate(ctx context.Context, request *core.AIRequest) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("AI chain request is nil")
	}
	entries := c.runtimeEntries()
	if len(entries) == 0 {
		return nil, errors.New("AI chain has no entries")
	}
	fingerprint := chainRequestFingerprint(ctx, request, entries)

	started := time.Now()
	ctx, span := c.startRequestSpan(ctx, "ai.chain.generate")
	defer span.End()
	span.SetAttribute("ai.chain.providers_count", len(entries))
	span.SetAttribute("ai.prompt_length", len(request.Prompt))

	if c.logger != nil {
		c.logRequestInfo(ctx, "Chain client request started", map[string]interface{}{
			"operation":       "ai_chain_request",
			"entries_count":   len(entries),
			"entry_names":     chainEntryNames(entries),
			"prompt_length":   len(request.Prompt),
			"requested_model": chainRequestedModel(request),
		})
	}

	var failures []error
	var lastErr error
	var lastResult *core.AIResult
	failedEntries := make([]string, 0, len(entries))

	for index, entry := range entries {
		attempt := index + 1
		attemptRequest, err := core.CloneAIRequest(request)
		if err != nil {
			returnedErr := fmt.Errorf("clone AI chain request for entry %q: %w", entry.name, err)
			span.SetAttribute("ai.chain.status", "aborted")
			span.SetAttribute("ai.chain.abort_reason", "invalid_request")
			providers.RecordObservationError(span, returnedErr, "invalid_request")
			if c.logger != nil {
				fields := map[string]interface{}{
					"operation":      "ai_chain_request_error",
					"entry_name":     entry.name,
					"attempt":        attempt,
					"failed_entries": failedEntries,
					"duration_ms":    time.Since(started).Milliseconds(),
				}
				addChainObservationError(fields, returnedErr, "invalid_request")
				c.logRequestError(ctx, "Chain request could not be cloned", fields)
			}
			return lastResult, returnedErr
		}

		attemptStarted := time.Now()
		attemptCtx, attemptSpan := c.startRequestSpan(ctx, "ai.chain.provider_attempt")
		attemptSpan.SetAttribute("ai.chain.provider_index", index)
		attemptSpan.SetAttribute("ai.chain.entry_name", entry.name)
		attemptSpan.SetAttribute("ai.chain.is_retry", index > 0)

		if c.logger != nil {
			c.logRequestDebug(attemptCtx, "Trying provider in chain", map[string]interface{}{
				"operation":     "ai_chain_attempt",
				"entry_name":    entry.name,
				"attempt":       attempt,
				"remaining":     len(entries) - attempt,
				"failed_so_far": failedEntries,
			})
		}

		result, callErr := core.GenerateAI(attemptCtx, entry.client, attemptRequest)
		if callErr == nil && (result == nil || result.Response == nil) {
			callErr = errors.New("AI chain entry returned a nil response without error")
		}
		result = annotateChainResult(result, entry.name, attempt, "generate", request.Purpose)
		attemptDuration := time.Since(attemptStarted)

		if callErr == nil {
			attachChainFingerprint(result, fingerprint, index)
			attemptSpan.SetAttribute("ai.chain.attempt_status", "success")
			attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", attemptDuration.Milliseconds())
			attemptSpan.End()
			telemetry.Counter("ai.chain.attempt",
				"module", telemetry.ModuleAI,
				"status", "success",
			)
			if index > 0 {
				telemetry.Counter("ai.chain.failover",
					"module", telemetry.ModuleAI,
					"status", "recovered",
					"reason", classifyFailoverReason(lastErr),
				)
				span.SetAttribute("ai.chain.failover_occurred", true)
				span.SetAttribute("ai.chain.failover_count", index)
				span.SetAttribute("ai.chain.failover_reason", classifyFailoverReason(lastErr))
				if c.logger != nil {
					c.logRequestInfo(ctx, "Chain failover succeeded", map[string]interface{}{
						"operation":         "ai_chain_failover_success",
						"failed_entries":    failedEntries,
						"successful_entry":  entry.name,
						"attempt":           attempt,
						"total_duration_ms": time.Since(started).Milliseconds(),
					})
				}
			}
			span.SetAttribute("ai.chain.status", "success")
			span.SetAttribute("ai.chain.entry_name", entry.name)
			span.SetAttribute("ai.chain.successful_entry", entry.name)
			span.SetAttribute("ai.chain.attempt", attempt)
			span.SetAttribute("ai.chain.total_duration_ms", time.Since(started).Milliseconds())
			return result, nil
		}

		lastErr = callErr
		lastResult = result
		failedEntries = append(failedEntries, entry.name)
		failures = append(failures, annotateChainError(entry.name, attempt, callErr))
		attemptSpan.SetAttribute("ai.chain.attempt_status", "failed")
		attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", attemptDuration.Milliseconds())
		attemptSpan.SetAttribute("ai.chain.is_client_error", isClientError(callErr))
		var providerErr core.ProviderError
		if errors.As(callErr, &providerErr) {
			attemptSpan.SetAttribute("ai.chain.is_transient", providerErr.IsTransient())
			attemptSpan.SetAttribute("ai.chain.is_retryable", providerErr.IsRetryable())
		}
		recordChainObservationError(
			attemptSpan,
			callErr,
			"unknown",
		)
		attemptSpan.End()
		telemetry.Counter("ai.chain.attempt",
			"module", telemetry.ModuleAI,
			"status", "failed",
			"reason", classifyFailoverReason(callErr),
		)

		if !shouldFailOver(callErr) {
			reason := classifyFailoverReason(callErr)
			span.SetAttribute("ai.chain.status", "aborted")
			span.SetAttribute("ai.chain.abort_reason", "non_retryable_error")
			span.SetAttribute("ai.chain.failover_reason", reason)
			recordChainObservationError(
				span,
				callErr,
				"unknown",
			)
			if c.logger != nil {
				fields := map[string]interface{}{
					"operation":       "ai_chain_abort",
					"entry_name":      entry.name,
					"attempt":         attempt,
					"failed_entries":  failedEntries,
					"duration_ms":     time.Since(started).Milliseconds(),
					"failover_reason": reason,
				}
				addChainObservationError(fields, callErr, "unknown")
				c.logRequestError(ctx, "Chain aborted - client error not retryable", fields)
			}
			return result, callErr
		}

		c.logChainFailover(attemptCtx, entries, index, callErr, failedEntries, attemptDuration)
	}

	joined := errors.Join(failures...)
	reason := classifyFailoverReason(lastErr)
	telemetry.Counter("ai.chain.exhausted",
		"module", telemetry.ModuleAI,
		"status", "exhausted",
		"reason", reason,
	)
	span.SetAttribute("ai.chain.status", "exhausted")
	span.SetAttribute("ai.chain.failed_providers", strings.Join(failedEntries, ","))
	span.SetAttribute("ai.chain.failover_reason", reason)
	span.SetAttribute("ai.chain.total_duration_ms", time.Since(started).Milliseconds())
	recordChainObservationError(
		span,
		lastErr,
		"unknown",
	)
	if c.logger != nil {
		fields := map[string]interface{}{
			"operation":         "ai_chain_exhausted",
			"entries_tried":     len(entries),
			"failed_entries":    failedEntries,
			"total_duration_ms": time.Since(started).Milliseconds(),
			"failover_reason":   reason,
		}
		addChainObservationError(fields, lastErr, "unknown")
		c.logRequestError(ctx, "All chain providers exhausted", fields)
	}
	return lastResult, fmt.Errorf("all %d chain entries failed: %w", len(entries), joined)
}

// Stream executes request-aware streaming across chain entries. Failover is
// allowed only before the callback observes a chunk; after output is visible,
// the current entry's result and error are returned unchanged.
func (c *ChainClient) Stream(
	ctx context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("AI chain stream request is nil")
	}
	if callback == nil {
		return nil, errors.New("AI chain stream callback is nil")
	}
	entries := c.runtimeEntries()
	if len(entries) == 0 {
		return nil, errors.New("AI chain has no entries")
	}
	fingerprint := chainRequestFingerprint(ctx, request, entries)

	started := time.Now()
	ctx, span := c.startRequestSpan(ctx, "ai.chain.stream")
	defer span.End()
	span.SetAttribute("ai.chain.total_providers", len(entries))
	span.SetAttribute("ai.chain.providers_count", len(entries))
	span.SetAttribute("ai.prompt_length", len(request.Prompt))
	span.SetAttribute("ai.streaming", true)

	var failures []error
	var lastResult *core.AIResult
	var lastErr error
	failedEntries := make([]string, 0, len(entries))

	for index, entry := range entries {
		attempt := index + 1
		attemptRequest, err := core.CloneAIRequest(request)
		if err != nil {
			returnedErr := fmt.Errorf("clone AI chain stream request for entry %q: %w", entry.name, err)
			span.SetAttribute("ai.chain.status", "aborted")
			span.SetAttribute("ai.chain.abort_reason", "invalid_request")
			providers.RecordObservationError(span, returnedErr, "invalid_request")
			if c.logger != nil {
				fields := map[string]interface{}{
					"operation":      "ai_chain_stream_request_error",
					"entry_name":     entry.name,
					"attempt":        attempt,
					"failed_entries": failedEntries,
					"duration_ms":    time.Since(started).Milliseconds(),
				}
				addChainObservationError(fields, returnedErr, "invalid_request")
				c.logRequestError(ctx, "Chain stream request could not be cloned", fields)
			}
			return lastResult, returnedErr
		}

		attemptStarted := time.Now()
		attemptCtx, attemptSpan := c.startRequestSpan(ctx, "ai.chain.stream_attempt")
		attemptSpan.SetAttribute("ai.chain.provider_index", index)
		attemptSpan.SetAttribute("ai.chain.entry_name", entry.name)
		attemptSpan.SetAttribute("ai.chain.is_retry", index > 0)

		var delivered atomic.Bool
		attemptCallback := func(chunk core.StreamChunk) error {
			delivered.Store(true)
			return callback(chunk)
		}
		result, callErr := core.StreamAI(attemptCtx, entry.client, attemptRequest, attemptCallback)
		if callErr == nil && (result == nil || result.Response == nil) {
			callErr = errors.New("AI chain streaming entry returned a nil response without error")
		}
		result = annotateChainResult(result, entry.name, attempt, "stream", request.Purpose)

		if callErr == nil {
			attachChainFingerprint(result, fingerprint, index)
			attemptSpan.SetAttribute("ai.chain.attempt_status", "success")
			attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", time.Since(attemptStarted).Milliseconds())
			attemptSpan.End()
			telemetry.Counter("ai.chain.stream.success",
				"module", telemetry.ModuleAI,
				"status", "success",
			)
			telemetry.Histogram("ai.chain.stream.duration_ms",
				float64(time.Since(started).Milliseconds()),
				"module", telemetry.ModuleAI,
				"status", "success",
			)
			span.SetAttribute("ai.chain.status", "success")
			span.SetAttribute("ai.chain.entry_name", entry.name)
			span.SetAttribute("ai.chain.successful_entry", entry.name)
			span.SetAttribute("ai.chain.attempt", attempt)
			span.SetAttribute("ai.chain.total_duration_ms", time.Since(started).Milliseconds())
			if index > 0 {
				span.SetAttribute("ai.chain.failover_occurred", true)
				span.SetAttribute("ai.chain.failover_count", index)
				span.SetAttribute("ai.chain.failover_reason", classifyFailoverReason(lastErr))
			}
			return result, nil
		}

		lastErr = callErr
		lastResult = result
		if delivered.Load() || errors.Is(callErr, core.ErrStreamPartiallyCompleted) {
			attemptSpan.SetAttribute("ai.chain.attempt_status", "partial")
			attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", time.Since(attemptStarted).Milliseconds())
			providers.RecordObservationError(attemptSpan, callErr, "partial_stream")
			attemptSpan.End()
			telemetry.Counter("ai.chain.stream.partial",
				"module", telemetry.ModuleAI,
				"status", "partial",
				"reason", "partial_stream",
			)
			span.SetAttribute("ai.chain.status", "partial")
			span.SetAttribute("ai.chain.entry_name", entry.name)
			providers.RecordObservationError(span, callErr, "partial_stream")
			return result, callErr
		}

		failedEntries = append(failedEntries, entry.name)
		failures = append(failures, annotateChainError(entry.name, attempt, callErr))
		attemptSpan.SetAttribute("ai.chain.attempt_status", "failed")
		attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", time.Since(attemptStarted).Milliseconds())
		recordChainObservationError(
			attemptSpan,
			callErr,
			"unknown",
		)
		attemptSpan.End()
		telemetry.Counter("ai.chain.stream.failure",
			"module", telemetry.ModuleAI,
			"status", "failed",
			"reason", classifyFailoverReason(callErr),
		)
		if !shouldFailOver(callErr) {
			reason := classifyFailoverReason(callErr)
			span.SetAttribute("ai.chain.status", "aborted")
			span.SetAttribute("ai.chain.abort_reason", "non_retryable_error")
			span.SetAttribute("ai.chain.failover_reason", reason)
			recordChainObservationError(
				span,
				callErr,
				"unknown",
			)
			if c.logger != nil {
				fields := map[string]interface{}{
					"operation":       "ai_chain_stream_abort",
					"entry_name":      entry.name,
					"attempt":         attempt,
					"failed_entries":  failedEntries,
					"duration_ms":     time.Since(started).Milliseconds(),
					"failover_reason": reason,
				}
				addChainObservationError(fields, callErr, "unknown")
				c.logRequestError(ctx, "Chain stream aborted - client error not retryable", fields)
			}
			return result, callErr
		}
		if c.logger != nil && index+1 < len(entries) {
			fields := map[string]interface{}{
				"operation":       "ai_chain_stream_failover",
				"entry_name":      entry.name,
				"attempt":         attempt,
				"remaining":       len(entries) - attempt,
				"duration_ms":     time.Since(attemptStarted).Milliseconds(),
				"failover_reason": classifyFailoverReason(callErr),
			}
			addChainObservationError(fields, callErr, "unknown")
			c.logRequestWarn(attemptCtx, "Provider streaming failed, trying next", fields)
		}
	}

	joined := errors.Join(failures...)
	reason := classifyFailoverReason(lastErr)
	telemetry.Counter("ai.chain.stream.exhausted",
		"module", telemetry.ModuleAI,
		"status", "exhausted",
		"reason", reason,
	)
	span.SetAttribute("ai.chain.status", "exhausted")
	span.SetAttribute("ai.chain.failed_providers", strings.Join(failedEntries, ","))
	span.SetAttribute("ai.chain.failover_reason", reason)
	span.SetAttribute("ai.chain.total_duration_ms", time.Since(started).Milliseconds())
	recordChainObservationError(
		span,
		lastErr,
		"unknown",
	)
	if c.logger != nil {
		fields := map[string]interface{}{
			"operation":         "ai_chain_stream_exhausted",
			"entries_tried":     len(entries),
			"failed_entries":    failedEntries,
			"total_duration_ms": time.Since(started).Milliseconds(),
			"failover_reason":   reason,
		}
		addChainObservationError(fields, lastErr, "unknown")
		c.logRequestError(ctx, "All chain providers exhausted for streaming", fields)
	}
	return lastResult, fmt.Errorf("all %d chain entries failed for streaming: %w", len(entries), joined)
}

func annotateChainResult(result *core.AIResult, entryName string, attempt int, operation, purpose string) *core.AIResult {
	if result == nil {
		return nil
	}
	clone := *result
	if result.RequestReport != nil {
		report := *result.RequestReport
		report.Adjustments = append([]core.AIRequestAdjustment(nil), result.RequestReport.Adjustments...)
		clone.RequestReport = &report
	} else {
		clone.RequestReport = &core.AIRequestReport{Stable: false}
		if result.Response != nil {
			clone.RequestReport.Provider = result.Response.Provider
			clone.RequestReport.ResolvedModel = result.Response.Model
		}
	}
	if clone.RequestReport.Operation == "" {
		clone.RequestReport.Operation = operation
	}
	if clone.RequestReport.Purpose == "" {
		clone.RequestReport.Purpose = purpose
	}
	clone.RequestReport.Adjustments = append(clone.RequestReport.Adjustments, core.AIRequestAdjustment{
		Source: "chain",
		Rule:   entryName,
		Path:   "/chain/attempt",
		Action: "select",
		Reason: fmt.Sprintf("chain attempt %d", attempt),
	})
	return &clone
}

type chainAttemptError struct {
	entry   string
	attempt int
	cause   error
}

func (e *chainAttemptError) Error() string {
	return fmt.Sprintf("AI chain entry %q attempt %d failed: %v", e.entry, e.attempt, e.cause)
}

func (e *chainAttemptError) Unwrap() error { return e.cause }

func annotateChainError(entryName string, attempt int, err error) error {
	return &chainAttemptError{entry: entryName, attempt: attempt, cause: err}
}

func shouldFailOver(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return !isClientError(err)
}

func (c *ChainClient) logChainFailover(
	ctx context.Context,
	entries []ChainEntry,
	index int,
	err error,
	failedEntries []string,
	duration time.Duration,
) {
	if c.logger == nil || index+1 >= len(entries) {
		return
	}
	nextEntry := entries[index+1].name
	var providerErr core.ProviderError
	if errors.As(err, &providerErr) && providerErr.IsTransient() {
		fields := map[string]interface{}{
			"operation":     "chain_failover",
			"from_provider": entries[index].name,
			"to_provider":   nextEntry,
			"status_code":   providerErr.StatusCode(),
			"is_transient":  true,
			"duration_ms":   duration.Milliseconds(),
		}
		addChainObservationError(fields, err, "transport")
		c.logRequestWarn(ctx, "Transient proxy error, failing over to next provider", fields)
	}
	if errors.As(err, &providerErr) && providerErr.IsRetryable() {
		fields := map[string]interface{}{
			"operation":       "chain_failover_retryable",
			"from_provider":   entries[index].name,
			"to_provider":     nextEntry,
			"status_code":     providerErr.StatusCode(),
			"is_retryable":    true,
			"duration_ms":     duration.Milliseconds(),
			"failover_reason": classifyFailoverReason(err),
		}
		addChainObservationError(fields, err, "provider_client")
		c.logRequestWarn(ctx, "Provider terminal error (billing/quota), failing over to next provider", fields)
	}
	fields := map[string]interface{}{
		"operation":        "ai_chain_provider_failed",
		"entry_name":       entries[index].name,
		"attempt":          index + 1,
		"remaining":        len(entries) - index - 1,
		"duration_ms":      duration.Milliseconds(),
		"failed_providers": failedEntries,
		"failover_reason":  classifyFailoverReason(err),
	}
	addChainObservationError(fields, err, "unknown")
	c.logRequestWarn(ctx, "Provider failed in chain, trying next", fields)
}

func chainEntryNames(entries []ChainEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.name
	}
	return names
}

func chainRequestedModel(request *core.AIRequest) string {
	if request.Generation.Model != "" {
		return request.Generation.Model
	}
	if legacy := request.LegacyOptions(); legacy != nil {
		return legacy.Model
	}
	return ""
}

var _ core.AIRequestClient = (*ChainClient)(nil)
var _ core.StreamingAIRequestClient = (*ChainClient)(nil)
