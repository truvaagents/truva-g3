package providers_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	_ "github.com/truvaagents/truva-g3/ai/providers/gemini"
	"github.com/truvaagents/truva-g3/core"
)

type geminiTransitionRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip geminiTransitionRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func geminiTransitionHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: geminiTransitionRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1beta/models/gemini-3.7-flash:generateContent" {
			t.Fatalf("Gemini request path = %q", request.URL.Path)
		}
		if request.Header.Get("x-goog-api-key") != "transition-key" {
			t.Fatalf("Gemini authentication header was not applied")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5},"modelVersion":"gemini-3.7-flash"}`,
			)),
			Request: request,
		}, nil
	})}
}

func TestRegisteredGeminiUsesRequestAwareClientAndChainContracts(t *testing.T) {
	legacy, err := ai.NewClient(
		ai.WithProvider("gemini"),
		ai.WithAPIKey("transition-key"),
		ai.WithModel("gemini-3.7-flash"),
	)
	if err != nil {
		t.Fatalf("construct legacy Gemini client: %v", err)
	}
	if _, ok := legacy.(core.AIRequestClient); !ok {
		t.Fatalf("legacy constructor returned %T without the additive request contract", legacy)
	}

	requestClient, err := ai.NewRequestClient(
		ai.WithProvider("gemini"),
		ai.WithAPIKey("transition-key"),
		ai.WithBaseURL("https://gemini.example/v1beta"),
		ai.WithModel("gemini-3.7-flash"),
		ai.WithMaxRetries(0),
		ai.WithHTTPClient(geminiTransitionHTTPClient(t)),
	)
	if err != nil {
		t.Fatalf("construct request-aware Gemini client: %v", err)
	}
	request := core.NewAIRequest("hello", "transition-proof")
	request.Generation.Model = "gemini-3.7-flash"
	fingerprinter, ok := requestClient.(core.AIRequestFingerprinter)
	if !ok {
		t.Fatalf("request constructor returned %T without fingerprinting", requestClient)
	}
	preflight, stable := fingerprinter.RequestFingerprint(t.Context(), request)
	if !stable || preflight == "" {
		t.Fatalf("Gemini preflight fingerprint = %q, stable=%t", preflight, stable)
	}
	result, err := requestClient.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("request-aware Gemini generate: %v", err)
	}
	if result == nil || result.Response == nil || result.Response.Content != "ok" ||
		result.RequestReport == nil || result.RequestReport.Fingerprint != preflight {
		t.Fatalf("request-aware Gemini result = %#v", result)
	}

	chain, err := ai.NewChain(ai.ProviderEntry(
		"gemini-primary",
		"gemini",
		ai.WithAPIKey("transition-key"),
		ai.WithBaseURL("https://gemini.example/v1beta"),
		ai.WithModel("gemini-3.7-flash"),
		ai.WithMaxRetries(0),
		ai.WithHTTPClient(geminiTransitionHTTPClient(t)),
	))
	if err != nil {
		t.Fatalf("construct Gemini request-aware chain entry: %v", err)
	}
	chainFingerprint, stable := chain.RequestFingerprint(t.Context(), request)
	if !stable || chainFingerprint == "" {
		t.Fatalf("Gemini chain fingerprint = %q, stable=%t", chainFingerprint, stable)
	}
	result, err = chain.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Gemini chain generate: %v", err)
	}
	if result == nil || result.Response == nil || result.Response.Content != "ok" ||
		result.RequestReport == nil || result.RequestReport.Fingerprint != chainFingerprint {
		t.Fatalf("Gemini chain result = %#v", result)
	}
}
