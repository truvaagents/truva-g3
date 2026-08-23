package openai

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

type openRouterProviderError struct {
	statusCode int
	model      string
	errorType  string
	retryable  bool
}

func (e *openRouterProviderError) Error() string {
	return fmt.Sprintf("OpenRouter request failed (status %d, type %s)", e.statusCode, e.errorType)
}

func (e *openRouterProviderError) StatusCode() int   { return e.statusCode }
func (*openRouterProviderError) Provider() string    { return openRouterProviderAlias }
func (e *openRouterProviderError) Model() string     { return e.model }
func (*openRouterProviderError) IsTransient() bool   { return false }
func (e *openRouterProviderError) IsRetryable() bool { return e.retryable }

type openRouterHTTPErrorEnvelope struct {
	Error *struct {
		Code     json.RawMessage `json:"code"`
		Metadata *struct {
			ErrorType string `json:"error_type"`
		} `json:"metadata,omitempty"`
	} `json:"error"`
}

func normalizeOpenRouterHTTPError(status int, body []byte, model string) error {
	var envelope openRouterHTTPErrorEnvelope
	if len(body) > 0 && json.Unmarshal(body, &envelope) == nil && envelope.Error != nil {
		errorType := ""
		if envelope.Error.Metadata != nil {
			errorType = envelope.Error.Metadata.ErrorType
		}
		return normalizeOpenRouterError(status, &openaiwire.EndpointError{
			Code: parseOpenRouterErrorStatus(envelope.Error.Code),
			Type: errorType,
		}, model)
	}
	return normalizeOpenRouterError(status, nil, model)
}

func parseOpenRouterErrorStatus(raw json.RawMessage) int {
	var numeric int
	if err := json.Unmarshal(raw, &numeric); err == nil {
		return numeric
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0
	}
	status, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0
	}
	return status
}

func normalizeOpenRouterError(
	httpStatus int,
	wireErr *openaiwire.EndpointError,
	model string,
) error {
	status := httpStatus
	errorType := "unmapped"
	if wireErr != nil {
		if validProviderErrorStatus(wireErr.Code) {
			status = wireErr.Code
		}
		errorType = normalizeOpenRouterErrorType(wireErr.Type)
	}
	statusWasValid := validProviderErrorStatus(status)
	if !statusWasValid {
		status = 500
	}

	retryable := false
	switch errorType {
	case "invalid_request", "invalid_prompt", "context_length_exceeded", "max_tokens_exceeded", "string_too_long":
		status = openRouterFailFastStatus(status)
	case "not_found", "image_not_found":
		status = 404
	case "precondition_failed":
		status = 412
	case "payload_too_large", "image_too_large":
		status = 413
	case "unprocessable":
		status = 422
	case "content_policy_violation", "refusal", "invalid_image", "image_too_small", "unsupported_image_format", "permission_denied":
		status = 400
	case "image_download_failed", "provider_unavailable":
		status = 502
	case "authentication":
		status = 401
	case "payment_required", "token_limit_exceeded":
		if !statusWasValid {
			status = 402
		}
		retryable = true
	case "rate_limit_exceeded":
		status = 429
	case "provider_overloaded":
		status = 503
	case "timeout":
		status = 504
		retryable = true
	case "server":
		status = 500
	}

	return &openRouterProviderError{
		statusCode: status,
		model:      model,
		errorType:  errorType,
		retryable:  retryable,
	}
}

func openRouterFailFastStatus(status int) int {
	if status >= 400 && status <= 499 && status != 401 && status != 402 && status != 403 && status != 429 {
		return status
	}
	return 400
}

func normalizeOpenRouterErrorType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "invalid_request", "invalid_prompt", "context_length_exceeded", "max_tokens_exceeded", "string_too_long",
		"not_found", "image_not_found", "precondition_failed", "payload_too_large", "unprocessable",
		"content_policy_violation", "refusal", "invalid_image", "image_too_small", "unsupported_image_format",
		"image_too_large", "image_download_failed", "authentication", "permission_denied", "payment_required",
		"token_limit_exceeded", "rate_limit_exceeded", "provider_unavailable", "provider_overloaded", "timeout", "server":
		return normalized
	default:
		return "unmapped"
	}
}

var _ core.ProviderError = (*openRouterProviderError)(nil)
