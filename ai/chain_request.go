package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

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

	started := time.Now()
	var span core.Span = &core.NoOpSpan{}
	if c.telemetry != nil {
		ctx, span = c.telemetry.StartSpan(ctx, "ai.chain.generate")
	}
	defer span.End()
	span.SetAttribute("ai.chain.providers_count", len(entries))
	span.SetAttribute("ai.prompt_length", len(request.Prompt))

	if c.logger != nil {
		c.logger.InfoWithContext(ctx, "Chain client request started", map[string]interface{}{
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
			return lastResult, fmt.Errorf("clone AI chain request for entry %q: %w", entry.name, err)
		}

		attemptStarted := time.Now()
		attemptCtx := ctx
		var attemptSpan core.Span = &core.NoOpSpan{}
		if c.telemetry != nil {
			attemptCtx, attemptSpan = c.telemetry.StartSpan(ctx, "ai.chain.provider_attempt")
		}
		attemptSpan.SetAttribute("ai.chain.provider_index", index)
		attemptSpan.SetAttribute("ai.chain.entry_name", entry.name)
		attemptSpan.SetAttribute("ai.chain.is_retry", index > 0)

		if c.logger != nil {
			c.logger.DebugWithContext(attemptCtx, "Trying provider in chain", map[string]interface{}{
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
			attemptSpan.SetAttribute("ai.chain.attempt_status", "success")
			attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", attemptDuration.Milliseconds())
			attemptSpan.End()
			telemetry.Counter("ai.chain.attempt",
				"module", telemetry.ModuleAI,
				"provider", entry.name,
				"status", "success",
				"attempt", fmt.Sprintf("%d", attempt),
			)
			if index > 0 {
				telemetry.Counter("ai.chain.failover",
					"module", telemetry.ModuleAI,
					"from_provider", failedEntries[len(failedEntries)-1],
					"to_provider", entry.name,
					"failed_count", fmt.Sprintf("%d", index),
				)
				span.SetAttribute("ai.chain.failover_occurred", true)
				span.SetAttribute("ai.chain.failover_count", index)
				span.SetAttribute("ai.chain.failover_reason", classifyFailoverReason(lastErr))
				if c.logger != nil {
					c.logger.InfoWithContext(ctx, "Chain failover succeeded", map[string]interface{}{
						"operation":         "ai_chain_failover_success",
						"failed_entries":    failedEntries,
						"successful_entry":  entry.name,
						"attempt":           attempt,
						"total_duration_ms": time.Since(started).Milliseconds(),
					})
				}
			}
			span.SetAttribute("ai.chain.status", "success")
			span.SetAttribute("ai.chain.successful_entry", entry.name)
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
		attemptSpan.RecordError(callErr)
		attemptSpan.End()
		telemetry.Counter("ai.chain.attempt",
			"module", telemetry.ModuleAI,
			"provider", entry.name,
			"status", "failed",
			"attempt", fmt.Sprintf("%d", attempt),
		)

		if !shouldFailOver(callErr) {
			span.SetAttribute("ai.chain.status", "aborted")
			span.SetAttribute("ai.chain.abort_reason", "non_retryable_error")
			span.RecordError(callErr)
			if c.logger != nil {
				c.logger.ErrorWithContext(ctx, "Chain aborted - client error not retryable", map[string]interface{}{
					"operation":      "ai_chain_abort",
					"entry_name":     entry.name,
					"attempt":        attempt,
					"failed_entries": failedEntries,
					"duration_ms":    time.Since(started).Milliseconds(),
				})
			}
			return result, callErr
		}

		c.logChainFailover(attemptCtx, entries, index, callErr, failedEntries, attemptDuration)
	}

	joined := errors.Join(failures...)
	telemetry.Counter("ai.chain.exhausted",
		"module", telemetry.ModuleAI,
		"providers_tried", fmt.Sprintf("%d", len(entries)),
	)
	span.SetAttribute("ai.chain.status", "exhausted")
	span.SetAttribute("ai.chain.failed_providers", strings.Join(failedEntries, ","))
	span.SetAttribute("ai.chain.total_duration_ms", time.Since(started).Milliseconds())
	span.RecordError(joined)
	if c.logger != nil {
		c.logger.ErrorWithContext(ctx, "All chain providers exhausted", map[string]interface{}{
			"operation":         "ai_chain_exhausted",
			"entries_tried":     len(entries),
			"failed_entries":    failedEntries,
			"total_duration_ms": time.Since(started).Milliseconds(),
		})
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

	started := time.Now()
	var span core.Span = &core.NoOpSpan{}
	if c.telemetry != nil {
		ctx, span = c.telemetry.StartSpan(ctx, "ai.chain.stream")
	}
	defer span.End()
	span.SetAttribute("ai.chain.total_providers", len(entries))
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
			return lastResult, fmt.Errorf("clone AI chain stream request for entry %q: %w", entry.name, err)
		}

		attemptStarted := time.Now()
		attemptCtx := ctx
		var attemptSpan core.Span = &core.NoOpSpan{}
		if c.telemetry != nil {
			attemptCtx, attemptSpan = c.telemetry.StartSpan(ctx, "ai.chain.stream_attempt")
		}
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
			attemptSpan.SetAttribute("ai.chain.attempt_status", "success")
			attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", time.Since(attemptStarted).Milliseconds())
			attemptSpan.End()
			telemetry.Counter("ai.chain.stream.success",
				"module", telemetry.ModuleAI,
				"provider", entry.name,
				"attempt", fmt.Sprintf("%d", attempt),
			)
			telemetry.Histogram("ai.chain.stream.duration_ms",
				float64(time.Since(started).Milliseconds()),
				"module", telemetry.ModuleAI,
				"provider", entry.name,
			)
			span.SetAttribute("ai.chain.status", "success")
			span.SetAttribute("ai.chain.entry_name", entry.name)
			span.SetAttribute("ai.chain.attempt", attempt)
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
			attemptSpan.RecordError(callErr)
			attemptSpan.End()
			telemetry.Counter("ai.chain.stream.partial",
				"module", telemetry.ModuleAI,
				"provider", entry.name,
			)
			span.SetAttribute("ai.chain.status", "partial")
			span.SetAttribute("ai.chain.entry_name", entry.name)
			span.RecordError(callErr)
			return result, callErr
		}

		failedEntries = append(failedEntries, entry.name)
		failures = append(failures, annotateChainError(entry.name, attempt, callErr))
		attemptSpan.SetAttribute("ai.chain.attempt_status", "failed")
		attemptSpan.SetAttribute("ai.chain.attempt_duration_ms", time.Since(attemptStarted).Milliseconds())
		attemptSpan.RecordError(callErr)
		attemptSpan.End()
		telemetry.Counter("ai.chain.stream.failure",
			"module", telemetry.ModuleAI,
			"provider", entry.name,
		)
		if !shouldFailOver(callErr) {
			span.SetAttribute("ai.chain.status", "aborted")
			span.RecordError(callErr)
			return result, callErr
		}
		if c.logger != nil {
			c.logger.WarnWithContext(attemptCtx, "Provider streaming failed, trying next", map[string]interface{}{
				"operation":    "ai_chain_stream_failover",
				"entry_name":   entry.name,
				"attempt":      attempt,
				"remaining":    len(entries) - attempt,
				"failure_type": classifyFailoverReason(callErr),
			})
		}
	}

	joined := errors.Join(failures...)
	telemetry.Counter("ai.chain.stream.exhausted",
		"module", telemetry.ModuleAI,
		"providers_tried", fmt.Sprintf("%d", len(entries)),
	)
	span.SetAttribute("ai.chain.status", "exhausted")
	span.SetAttribute("ai.chain.failed_providers", strings.Join(failedEntries, ","))
	span.RecordError(joined)
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
	if c.logger == nil {
		return
	}
	nextEntry := "none"
	if index+1 < len(entries) {
		nextEntry = entries[index+1].name
	}
	var providerErr core.ProviderError
	if errors.As(err, &providerErr) && providerErr.IsTransient() {
		c.logger.WarnWithContext(ctx, "Transient proxy error, failing over to next provider", map[string]interface{}{
			"operation":     "chain_failover",
			"from_provider": entries[index].name,
			"to_provider":   nextEntry,
			"status_code":   providerErr.StatusCode(),
			"is_transient":  true,
		})
	}
	if errors.As(err, &providerErr) && providerErr.IsRetryable() {
		c.logger.WarnWithContext(ctx, "Provider terminal error (billing/quota), failing over to next provider", map[string]interface{}{
			"operation":     "chain_failover_retryable",
			"from_provider": entries[index].name,
			"to_provider":   nextEntry,
			"status_code":   providerErr.StatusCode(),
			"is_retryable":  true,
		})
	}
	c.logger.WarnWithContext(ctx, "Provider failed in chain, trying next", map[string]interface{}{
		"operation":        "ai_chain_provider_failed",
		"entry_name":       entries[index].name,
		"attempt":          index + 1,
		"remaining":        len(entries) - index - 1,
		"duration_ms":      duration.Milliseconds(),
		"failed_providers": failedEntries,
		"failure_type":     classifyFailoverReason(err),
	})
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
