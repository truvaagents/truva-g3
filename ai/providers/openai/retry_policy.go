package openai

import (
	"errors"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

// Same-provider recovery is deliberately separate from the public chain flags.
// A synthesized fallback status is not evidence that a provider is transient.
func sameProviderRetryStatus(status int, errorType string, suppliedStatus bool) bool {
	switch strings.ToLower(strings.TrimSpace(errorType)) {
	case "rate_limit_exceeded", "provider_unavailable", "provider_overloaded", "timeout", "server",
		"rate_limit_error", "server_error", "overloaded_error":
		return true
	case "", "unmapped":
		// A genuine status-only error may use the bounded allowlist below.
	default:
		// Recognized terminal types, including image_download_failed and billing,
		// take precedence over misleading status codes. Unknown typed errors are
		// also conservative: another adapter can define its own typed policy.
		return false
	}
	if !suppliedStatus {
		return false
	}
	switch status {
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func sameProviderRetry(err error, httpStatus int) bool {
	if httpStatus == 401 || httpStatus == 402 || httpStatus == 403 {
		return false // Actual credential/account denial outranks an inconsistent envelope.
	}
	var router *openRouterProviderError
	if errors.As(err, &router) {
		return router.retrySameProvider
	}
	var compatible *compatibleProviderError
	if errors.As(err, &compatible) {
		return compatible.retrySameProvider
	}
	// Other HTTP errors retain the existing transport status policy, except
	// provider-specific terminal errors already classified for chain failover.
	var provider core.ProviderError
	if !errors.As(err, &provider) || provider.IsRetryable() {
		return false
	}
	return httpStatus == 429 || httpStatus >= 500
}
