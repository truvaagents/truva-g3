package providers

import (
	"context"
	"errors"
	"net/http"

	"github.com/truvaagents/truva-g3/core"
)

// DecodedAttempt is the provider's result and same-provider retry decision for
// one API response. Retryable is distinct from ProviderError.IsRetryable, which
// permits chain failover rather than repeating the same provider operation.
type DecodedAttempt struct {
	Result    *core.AIResult
	Err       error
	Retryable bool
	// SemanticStatus is the normalized provider-error status, not the HTTP status
	// or proof that the provider supplied that status. The adapter separately
	// retains provenance when deciding Retryable.
	// Only 429/503 enable Retry-After handling on an otherwise eligible retry.
	SemanticStatus int
	// ProviderRequestID is an adapter-sanitized, non-secret response identity.
	// It is a span attribute, never a metric label; empty means unavailable.
	ProviderRequestID string
}

// ResponseAttemptDecoder consumes one API response within the attempt context.
// It owns decoding/classification, not retries. It must return Retryable=false
// after delivering any stream output or invoking a failing callback. The base
// closes the response body after it returns, including on the terminal attempt.
// HTML error responses and transport failures retain the base transport policy
// and do not enter this callback. Callbacks must not start another retry loop.
type ResponseAttemptDecoder func(context.Context, *http.Response) DecodedAttempt

type decodedOperation struct {
	decode ResponseAttemptDecoder
	result *core.AIResult
}

// ExecuteWithRetryDecoded shares one MaxRetries budget across HTTP/transport
// failures and provider-decoded failures. prepare may be nil; otherwise it runs
// once per wire attempt, just as in ExecuteWithRetryPrepared. Request semantics
// must already be prepared and replayable. Decoding (including streaming) occurs
// before this method returns; unlike ExecuteWithRetry, no live body is returned.
// The result belongs only to the terminal attempt, including a partial result on
// failure. Earlier results are observed but never merged into a later result.
func (b *BaseClient) ExecuteWithRetryDecoded(ctx context.Context, req *http.Request, prepare RequestAttemptPreparer, decode ResponseAttemptDecoder) (*core.AIResult, error) {
	if decode == nil {
		return nil, errors.New("AI response attempt decoder is nil")
	}
	operation := &decodedOperation{decode: decode}
	_, err := b.executeWithRetry(ctx, req, prepare, operation)
	return operation.result, err
}

func recordAttemptUsage(span core.Span, result *core.AIResult) {
	available := result != nil && result.Response != nil &&
		(result.Response.Usage.TotalTokens > 0 || result.Response.Usage.PromptTokens > 0 || result.Response.Usage.CompletionTokens > 0)
	span.SetAttribute("ai.attempt_usage_available", available)
	if !available {
		return
	}
	// Attempt observations deliberately do not increment logical token metrics.
	span.SetAttribute("ai.attempt_prompt_tokens", result.Response.Usage.PromptTokens)
	span.SetAttribute("ai.attempt_completion_tokens", result.Response.Usage.CompletionTokens)
	span.SetAttribute("ai.attempt_total_tokens", result.Response.Usage.TotalTokens)
}
