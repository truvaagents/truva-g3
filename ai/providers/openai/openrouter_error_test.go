package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

func TestNormalizeOpenRouterErrorTaxonomy(t *testing.T) {
	tests := []struct {
		errorType string
		wireCode  int
		status    int
		retryable bool
	}{
		{errorType: "invalid_request", wireCode: 429, status: 400},
		{errorType: "invalid_prompt", wireCode: 400, status: 400},
		{errorType: "context_length_exceeded", wireCode: 413, status: 413},
		{errorType: "max_tokens_exceeded", wireCode: 422, status: 422},
		{errorType: "string_too_long", wireCode: 499, status: 499},
		{errorType: "not_found", wireCode: 500, status: 404},
		{errorType: "image_not_found", status: 404},
		{errorType: "precondition_failed", status: 412},
		{errorType: "payload_too_large", status: 413},
		{errorType: "unprocessable", status: 422},
		{errorType: "content_policy_violation", wireCode: 403, status: 400},
		{errorType: "refusal", status: 400},
		{errorType: "invalid_image", status: 400},
		{errorType: "image_too_small", status: 400},
		{errorType: "unsupported_image_format", status: 400},
		{errorType: "image_too_large", status: 413},
		{errorType: "image_download_failed", status: 502},
		{errorType: "authentication", status: 401},
		{errorType: "permission_denied", wireCode: 403, status: 400},
		{errorType: "payment_required", status: 402, retryable: true},
		{errorType: "token_limit_exceeded", wireCode: 429, status: 429, retryable: true},
		{errorType: "rate_limit_exceeded", status: 429},
		{errorType: "provider_unavailable", status: 502},
		{errorType: "provider_overloaded", status: 503},
		{errorType: "timeout", status: 504, retryable: true},
		{errorType: "server", status: 500},
	}
	for _, test := range tests {
		t.Run(test.errorType, func(t *testing.T) {
			err := normalizeOpenRouterError(0, &openaiwire.EndpointError{Code: test.wireCode, Type: test.errorType}, "model")
			var providerErr core.ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error does not implement core.ProviderError: %v", err)
			}
			if providerErr.StatusCode() != test.status || providerErr.IsRetryable() != test.retryable ||
				providerErr.IsTransient() || providerErr.Provider() != openRouterProviderAlias || providerErr.Model() != "model" {
				t.Fatalf("normalized error = status:%d retryable:%t transient:%t provider:%q model:%q",
					providerErr.StatusCode(), providerErr.IsRetryable(), providerErr.IsTransient(), providerErr.Provider(), providerErr.Model())
			}
		})
	}
}

func TestOpenRouterBufferedInBandErrorPreservesPartialResult(t *testing.T) {
	const canary = "private prompt fragment"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"model":"anthropic/claude-sonnet",
			"choices":[{"message":{"content":"partial"},"finish_reason":"error",
				"error":{"code":400,"message":"` + canary + `","metadata":{"error_type":"invalid_request","raw":"` + canary + `"}}}],
			"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}
		}`))
	}))
	defer server.Close()

	client := NewClient("key", server.URL, openRouterProviderAlias, &core.NoOpLogger{})
	client.MaxRetries = 0
	result, err := client.Generate(t.Context(), core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "openai/gpt-5.6-sol",
	}))
	if err == nil || result == nil || result.Response == nil || result.Response.Content != "partial" ||
		result.Response.Provider != openRouterProviderAlias || result.RequestReport == nil {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	var providerErr core.ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode() != 400 || strings.Contains(err.Error(), canary) {
		t.Fatalf("normalized error = %v", err)
	}
}

func TestOpenRouterStreamInBandErrorPreservesPartialIdentity(t *testing.T) {
	const canary = "private stream fragment"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"model\":\"anthropic/claude-sonnet\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"error\",\"error\":{\"code\":503,\"message\":\"" + canary + "\",\"metadata\":{\"error_type\":\"provider_overloaded\"}}}]}\n\n"))
	}))
	defer server.Close()

	client := NewClient("key", server.URL, openRouterProviderAlias, &core.NoOpLogger{})
	client.MaxRetries = 0
	var content strings.Builder
	result, err := client.Stream(t.Context(), core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "openai/gpt-5.6-sol",
	}), func(chunk core.StreamChunk) error {
		content.WriteString(chunk.Content)
		return nil
	})
	if !errors.Is(err, core.ErrStreamPartiallyCompleted) || result == nil || result.Response == nil ||
		result.Response.Content != "partial" || content.String() != "partial" || result.RequestReport == nil {
		t.Fatalf("result=%#v content=%q error=%v", result, content.String(), err)
	}
	var providerErr core.ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode() != 503 || strings.Contains(err.Error(), canary) {
		t.Fatalf("normalized stream error = %v", err)
	}
}

func TestOpenRouterHTTPErrorNeverReturnsBodyContent(t *testing.T) {
	const canary = "private HTTP fragment"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"error":{"code":403,"message":"` + canary + `","metadata":{"error_type":"permission_denied"}}}`))
	}))
	defer server.Close()

	client := NewClient("key", server.URL, openRouterProviderAlias, &core.NoOpLogger{})
	client.MaxRetries = 0
	result, err := client.Generate(t.Context(), core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "openai/gpt-5.6-sol",
	}))
	var providerErr core.ProviderError
	if result == nil || result.RequestReport == nil || !errors.As(err, &providerErr) ||
		providerErr.StatusCode() != 400 || strings.Contains(err.Error(), canary) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestNormalizeOpenRouterErrorFailsClosed(t *testing.T) {
	for _, errorType := range []string{"", "unknown_future_type", strings.Repeat("a", 65), "invalid type", "server\nsecret"} {
		err := normalizeOpenRouterError(418, &openaiwire.EndpointError{Type: errorType}, "model")
		var normalized *openRouterProviderError
		if !errors.As(err, &normalized) || normalized.errorType != "unmapped" || normalized.StatusCode() != 418 {
			t.Fatalf("input=%q normalized=%#v", errorType, normalized)
		}
		if strings.Contains(err.Error(), errorType) && errorType != "" {
			t.Fatalf("unchecked type reached returned error: %q", err)
		}
	}

	err := normalizeOpenRouterError(0, nil, "model")
	var normalized *openRouterProviderError
	if !errors.As(err, &normalized) || normalized.StatusCode() != 500 || normalized.errorType != "unmapped" {
		t.Fatalf("missing status/type = %#v", normalized)
	}
}

func TestNormalizeOpenRouterHTTPErrorUsesOnlyStructuralEnvelope(t *testing.T) {
	const canary = "private prompt fragment"
	body := []byte(`{"error":{"code":403,"message":"` + canary + `","metadata":{"error_type":"permission_denied","raw":"` + canary + `"}}}`)
	err := normalizeOpenRouterHTTPError(403, body, "openrouter/auto")
	var providerErr core.ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode() != 400 || providerErr.IsRetryable() {
		t.Fatalf("normalized HTTP error = %v", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("returned error leaked body: %v", err)
	}

	stringCodeBody := []byte(`{"error":{"code":"403","message":"` + canary + `","metadata":{"error_type":"permission_denied"}}}`)
	err = normalizeOpenRouterHTTPError(403, stringCodeBody, "openrouter/auto")
	var stringCodeError *openRouterProviderError
	if !errors.As(err, &stringCodeError) || stringCodeError.StatusCode() != 400 ||
		stringCodeError.errorType != "permission_denied" || stringCodeError.IsRetryable() ||
		strings.Contains(err.Error(), canary) {
		t.Fatalf("string-code HTTP error = %#v (%v)", stringCodeError, err)
	}

	for _, invalid := range [][]byte{[]byte("not json " + canary), nil} {
		err := normalizeOpenRouterHTTPError(503, invalid, "openrouter/auto")
		var normalized *openRouterProviderError
		if !errors.As(err, &normalized) || normalized.StatusCode() != 503 || normalized.errorType != "unmapped" ||
			strings.Contains(err.Error(), canary) {
			t.Fatalf("fallback error = %v", err)
		}
	}
}

func TestParseOpenRouterErrorStatusRejectsInvalidRepresentations(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "integer", raw: `403`, want: 403},
		{name: "trimmed string", raw: `" 403 "`, want: 403},
		{name: "invalid string", raw: `"denied"`},
		{name: "wrong JSON type", raw: `true`},
		{name: "invalid JSON", raw: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseOpenRouterErrorStatus([]byte(test.raw)); got != test.want {
				t.Fatalf("parseOpenRouterErrorStatus(%q) = %d, want %d", test.raw, got, test.want)
			}
		})
	}
}

func TestOpenRouterObservationIdentifierSanitizers(t *testing.T) {
	validGenerationBoundary := "gen-" + strings.Repeat("a", maxOpenRouterGenerationIDBytes-len("gen-"))
	for _, value := range []string{"gen-1234", "gen-abc_123", validGenerationBoundary} {
		if got := sanitizeGenerationID(value); got != value {
			t.Fatalf("sanitizeGenerationID(%q) = %q", value, got)
		}
	}
	for _, value := range []string{
		strings.Repeat("g", maxOpenRouterGenerationIDBytes+1), "generation-1234", "gen_123:attempt.2",
		"gen-", "Gen-1234", "generation id", "generation/id", "gen\nsecret", "生成",
	} {
		if got := sanitizeGenerationID(value); got != "" {
			t.Fatalf("sanitizeGenerationID(%q) = %q", value, got)
		}
	}

	validModelBoundary := "a/" + strings.Repeat("b", maxOpenRouterResponseModelBytes-2)
	for _, value := range []string{
		"anthropic/claude-sonnet-4", "~anthropic/claude-opus-latest", "vendor/model:free", "vendor/model:batch", validModelBoundary,
	} {
		if got := sanitizeResponseModel(value); got != value {
			t.Fatalf("sanitizeResponseModel(%q) = %q", value, got)
		}
	}
	for _, value := range []string{
		"", "publisher", "https://endpoint.example/model", "publisher/model/route", "private prompt sentence", "发布者/模型",
		"publisher/model\nsecret", "a/" + strings.Repeat("b", maxOpenRouterResponseModelBytes-1),
	} {
		if got := sanitizeResponseModel(value); got != "" {
			t.Fatalf("sanitizeResponseModel(%q) = %q", value, got)
		}
	}
}
