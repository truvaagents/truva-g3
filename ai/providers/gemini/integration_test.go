package gemini

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

type geminiResolverFunc func(context.Context, ai.EndpointRequest) (ai.ResolvedEndpoint, error)

func (resolver geminiResolverFunc) ResolveEndpoint(ctx context.Context, request ai.EndpointRequest) (ai.ResolvedEndpoint, error) {
	return resolver(ctx, request)
}

type rotatingGeminiCredentials struct {
	mu       sync.Mutex
	requests []ai.CredentialRequest
}

type rejectingGeminiCredentials struct {
	status        int
	rejections    int
	lastRequest   ai.CredentialRequest
	observerError error
}

func (*rejectingGeminiCredentials) Credential(context.Context, ai.CredentialRequest) (ai.HeaderCredential, error) {
	return ai.NewHeaderCredential("x-goog-api-key", "dynamic-secret"), nil
}

func (source *rejectingGeminiCredentials) CredentialRejected(_ context.Context, request ai.CredentialRequest, status int) error {
	source.rejections++
	source.status = status
	source.lastRequest = request
	return source.observerError
}

func (source *rotatingGeminiCredentials) Credential(_ context.Context, request ai.CredentialRequest) (ai.HeaderCredential, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.requests = append(source.requests, request)
	return ai.NewHeaderCredential("x-goog-api-key", "dynamic-key-"+string(rune('0'+len(source.requests)))), nil
}

func (source *rotatingGeminiCredentials) count() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return len(source.requests)
}

func TestDefaultRoutesUseExactGenerateContentMethodsWithoutCredentialQuery(t *testing.T) {
	client := NewClient("key", "https://gemini.example/v1beta/", &core.NoOpLogger{})
	for _, test := range []struct {
		stream bool
		path   string
		query  string
	}{
		{path: "/v1beta/models/gemini-3.7-flash:generateContent"},
		{stream: true, path: "/v1beta/models/gemini-3.7-flash:streamGenerateContent", query: "alt=sse"},
	} {
		request := core.NewAIRequest("hello", "")
		request.Generation.Model = "gemini-3.7-flash"
		invocation, err := client.prepareInvocation(t.Context(), request, test.stream)
		if err != nil {
			t.Fatalf("prepare stream=%t: %v", test.stream, err)
		}
		if invocation.Route.url.Path != test.path || invocation.Route.url.RawQuery != test.query {
			t.Fatalf("route stream=%t = %s", test.stream, invocation.Route.url.String())
		}
		if invocation.Route.url.Query().Has("key") || strings.Contains(invocation.Route.url.String(), client.apiKey) {
			t.Fatalf("credential leaked into route: %s", invocation.Route.url.String())
		}
	}
}

func TestDynamicEndpointIdentityBindsFingerprintWithoutCredentialAcquisition(t *testing.T) {
	credentials := &rotatingGeminiCredentials{}
	client := NewClient("static-key", "", &core.NoOpLogger{})
	client.credentialSource = credentials
	routeIdentity := "tenant-a"
	client.endpointResolver = geminiResolverFunc(func(_ context.Context, request ai.EndpointRequest) (ai.ResolvedEndpoint, error) {
		if request.Provider != "gemini" || request.Surface != "generate-content" ||
			request.Operation != "generate" || request.ResolvedModel != "gemini-2.5-flash" || request.Purpose != "planning" {
			t.Fatalf("endpoint request = %#v", request)
		}
		endpoint, _ := url.Parse("https://tenant.example/v1beta/models/deployment-a:generateContent")
		return ai.ResolvedEndpoint{
			URL: endpoint, Deployment: "deployment-a", RouteIdentity: routeIdentity, CredentialScope: "scope-a",
		}, nil
	})
	request := core.NewAIRequest("secret prompt", "planning")
	request.Generation.Model = "gemini-2.5-flash"

	first, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || len(first) != 64 || credentials.count() != 0 {
		t.Fatalf("fingerprint=%q stable=%t credential calls=%d", first, stable, credentials.count())
	}
	routeIdentity = "tenant-b"
	second, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || second == first || credentials.count() != 0 {
		t.Fatalf("route did not change fingerprint: first=%q second=%q stable=%t calls=%d", first, second, stable, credentials.count())
	}
	if strings.Contains(first, request.Prompt) || strings.Contains(first, "static-key") {
		t.Fatalf("fingerprint exposed secret input: %q", first)
	}
}

func TestDynamicCredentialWinsAndRefreshesForEveryRetryAttempt(t *testing.T) {
	credentials := &rotatingGeminiCredentials{}
	client := NewClient("static-must-not-win", "https://gemini.example/v1beta", &core.NoOpLogger{})
	client.credentialSource = credentials
	client.MaxRetries = 1
	client.RetryDelay = 0
	var attempts int
	var headers []string
	var requestURLs []string
	client.HTTPClient = &http.Client{Transport: geminiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		headers = append(headers, request.Header.Get("x-goog-api-key"))
		requestURLs = append(requestURLs, request.URL.String())
		_, _ = io.ReadAll(request.Body)
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"retry"}}`)),
				Request:    request,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}],
				"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},
				"modelVersion":"gemini-2.5-flash"
			}`)),
			Request: request,
		}, nil
	})}

	result, err := client.Generate(t.Context(), core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "gemini-2.5-flash"}))
	if err != nil || result == nil || result.Response == nil || result.Response.Content != "ok" {
		t.Fatalf("Generate = %#v, %v", result, err)
	}
	if attempts != 2 || credentials.count() != 2 || !reflectStringSliceEqual(headers, []string{"dynamic-key-1", "dynamic-key-2"}) {
		t.Fatalf("attempts=%d credential calls=%d headers=%#v", attempts, credentials.count(), headers)
	}
	for _, rawURL := range requestURLs {
		if strings.Contains(rawURL, "static-must-not-win") || strings.Contains(rawURL, "dynamic-key") || strings.Contains(rawURL, "key=") {
			t.Fatalf("credential leaked into URL: %s", rawURL)
		}
	}
}

func TestEndpointAndCredentialValidationFailBeforeTransport(t *testing.T) {
	client := NewClient("key", "", &core.NoOpLogger{})
	client.endpointResolver = geminiResolverFunc(func(context.Context, ai.EndpointRequest) (ai.ResolvedEndpoint, error) {
		return ai.ResolvedEndpoint{}, errors.New("route unavailable")
	})
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "gemini-2.5-flash"
	request.Patches = []core.AIProviderPatch{{
		Name: "would-also-fail", Version: "1",
		Selector: core.AIProviderSelector{Provider: "gemini"},
		Set:      map[string]interface{}{`/store`: true},
	}}
	_, err := client.prepareInvocation(t.Context(), request, false)
	var invocationErr *integrationInvocationError
	if !errors.As(err, &invocationErr) || invocationErr.stage != "endpoint resolution" {
		t.Fatalf("route failure precedence = %T %v", err, err)
	}

	for _, credential := range []ai.HeaderCredential{
		ai.NewHeaderCredential("Authorization", "Bearer value"),
		ai.NewHeaderCredential("x-goog-api-key", ""),
		ai.NewHeaderCredential("x-goog-api-key", "value\ninvalid"),
	} {
		if err := validateGeminiCredential(credential); err == nil || strings.Contains(err.Error(), credential.Value) && credential.Value != "" {
			t.Fatalf("credential validation = %v for %#v", err, credential)
		}
	}
}

func TestResolvedEndpointValidationRejectsAmbiguousOrCredentialBearingRoutes(t *testing.T) {
	parse := func(rawURL string) *url.URL {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse test URL: %v", err)
		}
		return parsed
	}
	tests := []struct {
		name     string
		endpoint ai.ResolvedEndpoint
		stream   bool
		want     string
	}{
		{name: "nil URL", endpoint: ai.ResolvedEndpoint{RouteIdentity: "route"}, want: "URL is nil"},
		{name: "empty identity", endpoint: ai.ResolvedEndpoint{URL: parse("https://gateway.example/v1beta/models/deployment:generateContent")}, want: "identity is empty"},
		{name: "userinfo", endpoint: ai.ResolvedEndpoint{URL: parse("https://user:secret@gateway.example/v1beta/models/deployment:generateContent"), RouteIdentity: "route"}, want: "user information"},
		{name: "fragment", endpoint: ai.ResolvedEndpoint{URL: parse("https://gateway.example/v1beta/models/deployment:generateContent#fragment"), RouteIdentity: "route"}, want: "fragment"},
		{name: "credential query", endpoint: ai.ResolvedEndpoint{URL: parse("https://gateway.example/v1beta/models/deployment:generateContent"), RouteIdentity: "route", Query: url.Values{"key": []string{"secret"}}}, want: "credentials"},
		{name: "wrong buffered method", endpoint: ai.ResolvedEndpoint{URL: parse("https://gateway.example/v1beta/models/deployment:streamGenerateContent?alt=sse"), RouteIdentity: "route", Deployment: "deployment"}, want: "method"},
		{name: "wrong stream format", endpoint: ai.ResolvedEndpoint{URL: parse("https://gateway.example/v1beta/models/deployment:streamGenerateContent"), RouteIdentity: "route", Deployment: "deployment"}, stream: true, want: "alt=sse"},
		{name: "deployment mismatch", endpoint: ai.ResolvedEndpoint{URL: parse("https://gateway.example/v1beta/models/deployment:generateContent"), RouteIdentity: "route", Deployment: "other"}, want: "deployment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient("key", "", &core.NoOpLogger{})
			client.endpointResolver = geminiResolverFunc(func(context.Context, ai.EndpointRequest) (ai.ResolvedEndpoint, error) {
				return test.endpoint, nil
			})
			request := core.NewAIRequest("hello", "")
			request.Generation.Model = "gemini-2.5-flash"
			_, err := client.prepareInvocation(t.Context(), request, test.stream)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepare error = %v; want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "user:secret") || strings.Contains(err.Error(), "key=secret") {
				t.Fatalf("route validation exposed a secret: %v", err)
			}
		})
	}
}

func TestCredentialRejectionObserverReceivesOnlyAuthenticationFailures(t *testing.T) {
	for _, test := range []struct {
		status        int
		wantObserved  bool
		observerError error
	}{
		{status: http.StatusUnauthorized, wantObserved: true},
		{status: http.StatusForbidden, wantObserved: true, observerError: errors.New("observer-secret-detail")},
		{status: http.StatusTooManyRequests},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			source := &rejectingGeminiCredentials{observerError: test.observerError}
			client := NewClient("static-key", "https://gemini.example/v1beta", &core.NoOpLogger{})
			client.credentialSource = source
			client.MaxRetries = 0
			client.HTTPClient = &http.Client{Transport: geminiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"provider rejection"}}`)),
					Request:    request,
				}, nil
			})}
			request := core.NewAIRequest("hello", "")
			request.Generation.Model = "gemini-2.5-flash"
			result, err := client.Generate(t.Context(), request)
			if err == nil || result == nil || result.RequestReport == nil {
				t.Fatalf("Generate = %#v, %v", result, err)
			}
			wantCount := 0
			if test.wantObserved {
				wantCount = 1
			}
			if source.rejections != wantCount {
				t.Fatalf("rejections = %d, want %d", source.rejections, wantCount)
			}
			if test.wantObserved {
				if source.status != test.status || source.lastRequest.Provider != "gemini" ||
					source.lastRequest.Surface != "generate-content" || source.lastRequest.ResolvedModel != "gemini-2.5-flash" {
					t.Fatalf("rejection callback = status %d request %#v", source.status, source.lastRequest)
				}
			}
			if strings.Contains(err.Error(), "observer-secret-detail") {
				t.Fatalf("observer error replaced provider error: %v", err)
			}
		})
	}
}

func TestFactoryUsesGoogleEnvironmentPrecedenceAndCopiesIntegrationHTTPClient(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "google-wins")
	t.Setenv("GEMINI_API_KEY", "gemini-loses")
	factory := &Factory{}
	legacy, err := factory.CreateValidated(&ai.AIConfig{})
	if err != nil {
		t.Fatalf("CreateValidated: %v", err)
	}
	if got := legacy.(*Client).apiKey; got != "google-wins" {
		t.Fatalf("environment precedence selected %q", got)
	}

	owned := &http.Client{}
	requestClient, err := factory.CreateRequestClient(&ai.AIConfig{APIKey: "key"}, ai.ProviderIntegrationConfig{HTTPClient: owned})
	if err != nil {
		t.Fatalf("CreateRequestClient: %v", err)
	}
	created := requestClient.(*Client)
	if created.HTTPClient == owned || owned.Transport != nil || created.HTTPClient.Transport == nil {
		t.Fatalf("HTTP client was not defensively copied: owned=%#v created=%#v", owned, created.HTTPClient)
	}
	if _, ok := interface{}(created).(core.AIRequestClient); !ok {
		t.Fatalf("created client does not implement request interface: %T", created)
	}
}

func reflectStringSliceEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
