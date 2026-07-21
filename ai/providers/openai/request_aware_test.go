package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestFactoryCreateRequestClientAppliesPolicyAndReturnsReport(t *testing.T) {
	var capturedBody map[string]interface{}
	var capturedHeader string
	var removedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capturedHeader = request.Header.Get("X-Policy")
		removedHeader = request.Header.Get("X-Remove")
		if err := json.NewDecoder(request.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"model":"gpt-4.1","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	factory := &Factory{}
	client, err := factory.CreateRequestClient(&ai.AIConfig{
		APIKey:        "static-key",
		BaseURL:       server.URL,
		ProviderAlias: "openai",
		Model:         "gpt-4.1",
		MaxTokens:     100,
		Headers:       map[string]string{"X-Remove": "default"},
	}, ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityCompatible,
		RequestRules: []core.AIProviderPatch{{
			Name:    "app-policy",
			Version: "1",
			Selector: core.AIProviderSelector{
				Provider: "openai",
				Surface:  "chat-completions",
			},
			Set:           map[string]interface{}{"/top_p": 0.2},
			SetHeaders:    map[string]string{"X-Policy": "active"},
			RemoveHeaders: []string{"X-Remove"},
		}},
	})
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}
	request := core.NewAIRequest("hello", "request-aware-test")
	request.Patches = []core.AIProviderPatch{{
		Name:     "call-policy",
		Version:  "1",
		Selector: core.AIProviderSelector{AllProviders: true},
		Remove:   []string{"/temperature"},
	}}
	result, err := client.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Response.Content != "ok" || result.Response.Provider != "openai" {
		t.Fatalf("response = %#v", result.Response)
	}
	if capturedBody["top_p"] != 0.2 {
		t.Fatalf("policy top_p = %#v", capturedBody["top_p"])
	}
	if _, exists := capturedBody["temperature"]; exists {
		t.Fatal("per-request temperature removal was not applied")
	}
	if capturedHeader != "active" {
		t.Fatalf("policy header = %q", capturedHeader)
	}
	if removedHeader != "" {
		t.Fatalf("removed default header was reintroduced: %q", removedHeader)
	}
	if result.RequestReport == nil || !result.RequestReport.Stable ||
		result.RequestReport.ProviderAlias != "openai" ||
		result.RequestReport.Purpose != "request-aware-test" ||
		len(result.RequestReport.Fingerprint) != 64 {
		t.Fatalf("request report = %#v", result.RequestReport)
	}
	if sources := adjustmentSources(result.RequestReport.Adjustments); !reflect.DeepEqual(sources, []string{"app-rule", "app-rule", "app-rule", "request-patch"}) {
		t.Fatalf("adjustment sources = %#v", sources)
	}
}

func TestFactoryCreateRequestClientResolvesRouteAndCredentialAfterPolicy(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"model":"gpt-4.1","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL + "/enterprise/chat")
	if err != nil {
		t.Fatal(err)
	}
	resolver := &capturingEndpointResolver{endpoint: endpoint}
	credentials := &capturingCredentialSource{}
	client, err := (&Factory{}).CreateRequestClient(&ai.AIConfig{
		ProviderAlias: "openai",
		Model:         "gpt-4.1",
		MaxTokens:     100,
	}, ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityCompatible,
		EndpointResolver:  resolver,
		CredentialSource:  credentials,
	})
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}
	result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "enterprise-route"))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if authorization != "Bearer dynamic" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if resolver.request.ResolvedModel != "gpt-4.1" || resolver.request.Purpose != "enterprise-route" {
		t.Fatalf("endpoint request = %#v", resolver.request)
	}
	if credentials.request.RouteIdentity != "enterprise-route-v1" || credentials.request.ResolvedModel != "gpt-4.1" {
		t.Fatalf("credential request = %#v", credentials.request)
	}
	if result.RequestReport == nil || len(result.RequestReport.Adjustments) != 1 ||
		result.RequestReport.Adjustments[0].Source != "endpoint-resolver" {
		t.Fatalf("request report = %#v", result.RequestReport)
	}
	text := result.RequestReport.Fingerprint + result.RequestReport.Adjustments[0].Reason
	if stringsContainAny(text, "dynamic", server.URL) {
		t.Fatalf("request report contains secret route material: %#v", result.RequestReport)
	}
}

func TestClientGenerateRejectsUnsupportedPortableFeatureBeforeNetwork(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewClient("key", server.URL, "openai.custom", &core.NoOpLogger{})
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "custom-model"
	request.Generation.ReasoningEffort = core.SetAIParameter("high")
	result, err := client.Generate(t.Context(), request)
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("Generate error = %v", err)
	}
	if called {
		t.Fatal("unsupported portable feature reached the network")
	}
}

func TestClientRequestFingerprintIncludesPurposeAndRouteIdentity(t *testing.T) {
	endpoint, err := url.Parse("https://gateway.example.test/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient("key", "", "openai", &core.NoOpLogger{})
	resolver := &fingerprintEndpointResolver{endpoint: endpoint, identity: "route-v1"}
	client.endpointResolver = resolver

	request := core.NewAIRequestFromLegacy("secret prompt", "planning", &core.AIOptions{Model: "gpt-4.1"})
	first, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || len(first) != 64 {
		t.Fatalf("fingerprint = %q, stable = %t", first, stable)
	}
	request.Purpose = "synthesis"
	second, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || first == second {
		t.Fatalf("purpose did not change fingerprint: first=%q second=%q stable=%t", first, second, stable)
	}
	request.Purpose = "planning"
	resolver.identity = "route-v2"
	third, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || first == third {
		t.Fatalf("route did not change fingerprint: first=%q third=%q stable=%t", first, third, stable)
	}
	if fingerprint, stable := client.RequestFingerprint(t.Context(), nil); stable || fingerprint != "" {
		t.Fatalf("nil request fingerprint = %q, %t", fingerprint, stable)
	}
	client.endpointResolver = &fingerprintEndpointResolver{err: errors.New("route unavailable")}
	if fingerprint, stable := client.RequestFingerprint(t.Context(), request); stable || fingerprint != "" {
		t.Fatalf("resolver failure fingerprint = %q, %t", fingerprint, stable)
	}
}

func TestFactorySnapshotsNestedDefaultExtras(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"model":"gpt-4.1","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	nested := map[string]interface{}{"mode": "original"}
	client, err := (&Factory{}).CreateRequestClient(&ai.AIConfig{
		APIKey:    "key",
		BaseURL:   server.URL,
		Model:     "gpt-4.1",
		MaxTokens: 10,
		Extra:     map[string]interface{}{"vendor": nested},
	}, ai.ProviderIntegrationConfig{CompatibilityMode: requestpolicy.CompatibilityCompatible})
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}
	nested["mode"] = "mutated"
	_, err = client.Generate(t.Context(), core.NewAIRequest("hello", ""))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got := captured["vendor"].(map[string]interface{})["mode"]; got != "original" {
		t.Fatalf("snapshotted default extra = %#v", got)
	}
}

func TestClientPrepareAIRequestUsesCaseInsensitiveRequestHeaderPrecedence(t *testing.T) {
	client := NewClient("key", "", "openai", &core.NoOpLogger{})
	client.defaultHeaders = map[string]string{
		"X-Shared":       "default",
		"X-Default-Only": "retained",
	}
	request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model:     "gpt-4.1",
		MaxTokens: 10,
		Headers:   map[string]string{"x-shared": "request"},
	})

	for _, stream := range []bool{false, true} {
		prepared, err := client.prepareAIRequest(t.Context(), request, stream)
		if err != nil {
			t.Fatalf("prepareAIRequest(stream=%t) returned error: %v", stream, err)
		}
		if got := prepared.Headers.Get("X-Shared"); got != "request" {
			t.Fatalf("case-insensitive request header precedence (stream=%t) = %q", stream, got)
		}
		if got := prepared.Headers.Get("X-Default-Only"); got != "retained" {
			t.Fatalf("default-only header (stream=%t) = %q", stream, got)
		}
		if values := prepared.Headers.Values("X-Shared"); !reflect.DeepEqual(values, []string{"request"}) {
			t.Fatalf("X-Shared values (stream=%t) = %#v", stream, values)
		}
	}
}

type capturingEndpointResolver struct {
	endpoint *url.URL
	request  ai.EndpointRequest
}

type fingerprintEndpointResolver struct {
	endpoint *url.URL
	identity string
	err      error
}

func (resolver *fingerprintEndpointResolver) ResolveEndpoint(context.Context, ai.EndpointRequest) (ai.ResolvedEndpoint, error) {
	if resolver.err != nil {
		return ai.ResolvedEndpoint{}, resolver.err
	}
	return ai.ResolvedEndpoint{URL: resolver.endpoint, RouteIdentity: resolver.identity}, nil
}

func (resolver *capturingEndpointResolver) ResolveEndpoint(
	_ context.Context,
	request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	resolver.request = request
	return ai.ResolvedEndpoint{
		URL:             resolver.endpoint,
		RouteIdentity:   "enterprise-route-v1",
		CredentialScope: "enterprise-scope",
	}, nil
}

type capturingCredentialSource struct {
	request ai.CredentialRequest
}

func (source *capturingCredentialSource) Credential(
	_ context.Context,
	request ai.CredentialRequest,
) (ai.HeaderCredential, error) {
	source.request = request
	return ai.NewHeaderCredential("Authorization", "Bearer dynamic"), nil
}

func adjustmentSources(adjustments []core.AIRequestAdjustment) []string {
	result := make([]string, len(adjustments))
	for index, adjustment := range adjustments {
		result[index] = adjustment.Source
	}
	return result
}

func stringsContainAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if candidate != "" && strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
