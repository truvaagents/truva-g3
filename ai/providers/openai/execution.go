package openai

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

// One immutable preparation, one logical provider call, one combined attempt
// budget. The codec decodes; it never owns retry or credential policy.
func (c *Client) executePreparedResponse(ctx context.Context, span core.Span, prepared *preparedRequest, route resolvedRoute, request *http.Request, callback core.StreamCallback, started time.Time) (*core.AIResult, error) {
	credentialRequest := c.credentialRequest(prepared, route)
	var providerRequestID string
	result, err := c.executeWithCredential(ctx, request, credentialRequest, func(attemptCtx context.Context, response *http.Response) providers.DecodedAttempt {
		providerRequestID = ""
		if c.getProviderName() == openRouterProviderAlias {
			providerRequestID = sanitizeGenerationID(response.Header.Get("X-Generation-Id"))
		}
		c.observeCredentialRejection(attemptCtx, credentialRequest, response.StatusCode)
		var result *core.AIResult
		var decodeErr error
		delivered, callbackStopped := false, false
		if response.StatusCode != http.StatusOK {
			body, _ := providers.ReadErrorBody(response.Body)
			decodeErr = c.compatibleHTTPError(response.StatusCode, body, prepared.Model)
		} else if callback == nil {
			result, decodeErr = prepared.Codec.Decode(response.Body)
			decodeErr = c.normalizeCompatibleError(decodeErr, prepared.Model)
		} else {
			result, decodeErr = prepared.Codec.DecodeStream(response.Body, func(chunk core.StreamChunk) error {
				delivered = true // Even a metadata/terminal callback commits output.
				err := callback(chunk)
				callbackStopped = callbackStopped || err != nil
				return err
			})
			partial := errors.Is(decodeErr, core.ErrStreamPartiallyCompleted)
			decodeErr = c.normalizeCompatibleError(decodeErr, prepared.Model)
			if partial {
				decodeErr = errors.Join(core.ErrStreamPartiallyCompleted, decodeErr)
			}
			if callbackStopped {
				span.SetAttribute("ai.stream_stopped_by_callback", true)
				span.SetAttribute("ai.stream_status", "callback_stop")
			}
		}
		if result != nil && result.Response != nil {
			result.Response.Provider = c.getProviderName()
		}
		// Preserve an expired caller/provider context if the response error does
		// not already carry it. The existing chain treats either deadline as terminal.
		if contextErr := attemptCtx.Err(); decodeErr != nil && contextErr != nil && !errors.Is(decodeErr, contextErr) {
			decodeErr = errors.Join(contextErr, decodeErr)
		}
		semanticStatus := 0
		var providerErr core.ProviderError
		if errors.As(decodeErr, &providerErr) {
			semanticStatus = providerErr.StatusCode()
		}
		return providers.DecodedAttempt{
			Result: result, Err: decodeErr, SemanticStatus: semanticStatus,
			ProviderRequestID: providerRequestID,
			Retryable:         !delivered && !callbackStopped && sameProviderRetry(decodeErr, response.StatusCode),
		}
	})
	if result != nil && result.Response != nil {
		c.observeProviderRequestID(span, providerRequestID)
		c.observeResponseIdentity(span, result.Response, providerRequestID)
	}
	if err == nil && result != nil && result.Response != nil {
		c.recordSuccessfulResponse(ctx, span, prepared.Model, result.Response, providerRequestID, started)
	}
	return resultWithReport(prepared, result), err
}
