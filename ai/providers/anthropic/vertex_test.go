package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type vertexTestResolver struct {
	projectID       string
	location        string
	publisherModels map[string]string
	routeIdentity   string
	credentialScope string
	err             error

	mu       sync.Mutex
	requests []ai.EndpointRequest
}

func (resolver *vertexTestResolver) ResolveEndpoint(
	_ context.Context,
	request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	resolver.mu.Lock()
	resolver.requests = append(resolver.requests, request)
	resolver.mu.Unlock()
	if resolver.err != nil {
		return ai.ResolvedEndpoint{}, resolver.err
	}
	publisherModel, ok := resolver.publisherModels[request.ResolvedModel]
	if !ok {
		return ai.ResolvedEndpoint{}, fmt.Errorf(
			"no Vertex publisher model for semantic model %q",
			request.ResolvedModel,
		)
	}
	endpoint, err := vertexTestEndpoint(resolver.projectID, resolver.location, publisherModel, request.Operation)
	if err != nil {
		return ai.ResolvedEndpoint{}, err
	}
	return ai.ResolvedEndpoint{
		URL: endpoint, Deployment: publisherModel,
		RouteIdentity: resolver.routeIdentity, CredentialScope: resolver.credentialScope,
	}, nil
}

func (resolver *vertexTestResolver) capturedRequests() []ai.EndpointRequest {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return append([]ai.EndpointRequest(nil), resolver.requests...)
}

func vertexTestEndpoint(projectID, location, publisherModel, operation string) (*url.URL, error) {
	host, err := vertexGoogleHost(location)
	if err != nil {
		return nil, err
	}
	method := "rawPredict"
	if operation == "stream" {
		method = "streamRawPredict"
	} else if operation != "generate" {
		return nil, fmt.Errorf("unsupported operation %q", operation)
	}
	return &url.URL{
		Scheme: "https", Host: host,
		Path: fmt.Sprintf(
			"/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
			projectID, location, publisherModel, method,
		),
	}, nil
}

type vertexRecordedRequest struct {
	url     string
	headers http.Header
	body    map[string]interface{}
}

type vertexRecordingTransport struct {
	mu       sync.Mutex
	requests []vertexRecordedRequest
	respond  func(int, *http.Request) (*http.Response, error)
}

func (transport *vertexRecordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	bodyBytes, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var body map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, vertexRecordedRequest{
		url: request.URL.String(), headers: request.Header.Clone(), body: body,
	})
	call := len(transport.requests)
	transport.mu.Unlock()
	return transport.respond(call, request)
}

func (transport *vertexRecordingTransport) capturedRequests() []vertexRecordedRequest {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]vertexRecordedRequest(nil), transport.requests...)
}

func newVertexTestClient(
	t *testing.T,
	model string,
	resolver ai.EndpointResolver,
	credentials ai.CredentialSource,
	transport http.RoundTripper,
	options ...ai.ClientOption,
) core.AIRequestClient {
	t.Helper()
	clientOptions := []ai.ClientOption{
		ai.WithProviderAlias("anthropic.vertex"),
		ai.WithModel(model),
		ai.WithMaxRetries(0),
		ai.WithEndpointResolver(resolver),
		ai.WithCredentialSource(credentials),
		ai.WithHTTPClient(&http.Client{Transport: transport}),
	}
	clientOptions = append(clientOptions, options...)
	client, err := ai.NewRequestClient(clientOptions...)
	if err != nil {
		t.Fatalf("ai.NewRequestClient returned error: %v", err)
	}
	return client
}

func TestVertexFactoryConstructionContract(t *testing.T) {
	factory := &Factory{}
	config := &ai.AIConfig{ProviderAlias: "anthropic.vertex"}
	if _, err := factory.CreateValidated(config); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("CreateValidated error = %v", err)
	}
	if _, err := ai.NewClient(ai.WithProviderAlias("anthropic.vertex")); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("ai.NewClient error = %v", err)
	}
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("Factory.Create did not panic for anthropic.vertex")
		}
	}()
	_ = factory.Create(config)
}

func TestVertexFactoryRequiresRequestIntegrationAndRejectsDirectConfiguration(t *testing.T) {
	resolver := &vertexTestResolver{}
	credentials := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", "Bearer token"), nil
	}}
	tests := []struct {
		name        string
		config      *ai.AIConfig
		integration ai.ProviderIntegrationConfig
		want        string
	}{
		{name: "missing endpoint", config: &ai.AIConfig{ProviderAlias: "anthropic.vertex"}, integration: ai.ProviderIntegrationConfig{CredentialSource: credentials}, want: "endpoint and credential"},
		{name: "missing credential", config: &ai.AIConfig{ProviderAlias: "anthropic.vertex"}, integration: ai.ProviderIntegrationConfig{EndpointResolver: resolver}, want: "endpoint and credential"},
		{name: "static API key", config: &ai.AIConfig{ProviderAlias: "anthropic.vertex", APIKey: "key"}, integration: ai.ProviderIntegrationConfig{EndpointResolver: resolver, CredentialSource: credentials}, want: "does not accept"},
		{name: "base URL", config: &ai.AIConfig{ProviderAlias: "anthropic.vertex", BaseURL: "https://example.com"}, integration: ai.ProviderIntegrationConfig{EndpointResolver: resolver, CredentialSource: credentials}, want: "does not accept"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := (&Factory{}).CreateRequestClient(test.config, test.integration); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("CreateRequestClient error = %v", err)
			}
		})
	}
}

func TestVertexFactoryDoesNotReadAnthropicEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "environment-key-must-not-be-read")
	t.Setenv("ANTHROPIC_BASE_URL", "https://environment-endpoint.example/v1")
	resolver := &vertexTestResolver{
		projectID: "acme-prod", location: "global",
		publisherModels: map[string]string{"claude-sonnet-4-5-20250929": "claude-sonnet-4-5@20250929"},
		routeIdentity:   "environment-boundary-v1",
	}
	credentials := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", "Bearer token"), nil
	}}
	requestClient, err := (&Factory{}).CreateRequestClient(
		&ai.AIConfig{ProviderAlias: "anthropic.vertex", Model: "smart", MaxRetries: 0},
		ai.ProviderIntegrationConfig{EndpointResolver: resolver, CredentialSource: credentials},
	)
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}
	client := requestClient.(*Client)
	if client.apiKey != "" || client.baseURL != DefaultBaseURL {
		t.Fatalf("Vertex client consumed Anthropic environment: key=%q baseURL=%q", client.apiKey, client.baseURL)
	}
}

func TestVertexSyncWireContractAndSemanticIdentity(t *testing.T) {
	const (
		semanticModel  = "claude-sonnet-4-5-20250929"
		publisherModel = "claude-sonnet-4-5@20250929"
	)
	resolver := &vertexTestResolver{
		projectID: "acme-prod", location: "global",
		publisherModels: map[string]string{semanticModel: publisherModel},
		routeIdentity:   "vertex-primary-v1", credentialScope: "cloud-platform-scope",
	}
	credentials := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", "Bearer adc-token"), nil
	}}
	transport := &vertexRecordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
		return phase4Response(request, http.StatusOK, phase4SuccessBody), nil
	}}
	client := newVertexTestClient(t, "smart", resolver, credentials, transport)
	result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "vertex-sync"))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	requests := transport.capturedRequests()
	if len(requests) != 1 {
		t.Fatalf("transport calls = %d", len(requests))
	}
	request := requests[0]
	wantURL := "https://aiplatform.googleapis.com/v1/projects/acme-prod/locations/global/publishers/anthropic/models/claude-sonnet-4-5@20250929:rawPredict"
	if request.url != wantURL {
		t.Fatalf("request URL = %q", request.url)
	}
	if request.body["anthropic_version"] != vertexAPIVersion {
		t.Fatalf("request body = %#v", request.body)
	}
	if _, present := request.body["model"]; present {
		t.Fatalf("Vertex body contains model: %#v", request.body)
	}
	if request.headers.Get("Authorization") != "Bearer adc-token" ||
		request.headers.Get("anthropic-version") != "" || request.headers.Get("x-api-key") != "" {
		t.Fatalf("request headers = %#v", request.headers)
	}
	endpointRequests := resolver.capturedRequests()
	if len(endpointRequests) != 1 || endpointRequests[0].Provider != "anthropic" ||
		endpointRequests[0].ProviderAlias != "anthropic.vertex" ||
		endpointRequests[0].ResolvedModel != semanticModel || endpointRequests[0].Operation != "generate" {
		t.Fatalf("endpoint requests = %#v", endpointRequests)
	}
	credentialRequests := credentials.capturedRequests()
	if len(credentialRequests) != 1 || credentialRequests[0].ResolvedModel != semanticModel ||
		credentialRequests[0].Deployment != publisherModel ||
		credentialRequests[0].CredentialScope != "cloud-platform-scope" {
		t.Fatalf("credential requests = %#v", credentialRequests)
	}
	if result == nil || result.Response == nil || result.Response.Content != "ok" || result.RequestReport == nil ||
		result.RequestReport.RequestedModel != "smart" || result.RequestReport.ResolvedModel != semanticModel ||
		result.RequestReport.ProviderAlias != "anthropic.vertex" {
		t.Fatalf("result = %#v", result)
	}
	reportText := fmt.Sprintf("%#v", result.RequestReport)
	for _, secret := range []string{publisherModel, "cloud-platform-scope", "adc-token", "aiplatform.googleapis.com"} {
		if strings.Contains(reportText, secret) {
			t.Fatalf("request report leaked %q: %s", secret, reportText)
		}
	}
}

func TestVertexRetainsSemanticSamplingAndPolicySelection(t *testing.T) {
	const semanticModel = "claude-sonnet-5"
	resolver := &vertexTestResolver{
		projectID: "acme-prod", location: "global",
		publisherModels: map[string]string{semanticModel: "claude-sonnet-5@20260701"},
		routeIdentity:   "semantic-policy-v1",
	}
	credentials := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", "Bearer token"), nil
	}}
	requestClient, err := (&Factory{}).CreateRequestClient(&ai.AIConfig{
		ProviderAlias: "anthropic.vertex", Model: semanticModel, Temperature: 0.8, MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver, CredentialSource: credentials,
		RequestRules: []core.AIProviderPatch{{
			Name: "semantic-selector", Version: "1",
			Selector: core.AIProviderSelector{
				Provider: "anthropic", ProviderAlias: "anthropic.vertex", Model: semanticModel,
			},
			Set: map[string]interface{}{`/metadata/semantic_policy`: true},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := requestClient.(*Client)
	invocation, err := client.prepareInvocation(t.Context(), core.NewAIRequest("hello", "semantic-policy"), false)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(invocation.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["temperature"]; present {
		t.Fatalf("deprecated semantic sampling survived: %#v", body)
	}
	metadata, ok := body["metadata"].(map[string]interface{})
	if !ok || metadata["semantic_policy"] != true {
		t.Fatalf("semantic policy did not match: %#v", body)
	}
	if invocation.Request.Report.ResolvedModel != semanticModel ||
		invocation.Request.SamplingPolicy != samplingOmitted {
		t.Fatalf("prepared request = %#v", invocation.Request)
	}
}

func TestVertexResolverMapUsesPostAliasSemanticModel(t *testing.T) {
	const semanticModel = "claude-sonnet-4-5-20250929"
	credentials := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", "Bearer token"), nil
	}}
	transport := &vertexRecordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
		return phase4Response(request, http.StatusOK, phase4SuccessBody), nil
	}}
	for _, test := range []struct {
		name            string
		publisherModels map[string]string
		wantError       string
	}{
		{name: "post-alias key", publisherModels: map[string]string{semanticModel: "publisher-model"}},
		{name: "application alias key", publisherModels: map[string]string{"smart": "publisher-model"}, wantError: `no Vertex publisher model for semantic model "` + semanticModel + `"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &vertexTestResolver{
				projectID: "acme-prod", location: "global", publisherModels: test.publisherModels,
				routeIdentity: "mapping-v1",
			}
			client := newVertexConcreteClient(t, "smart", resolver, credentials, transport)
			_, prepareErr := client.prepareInvocation(t.Context(), core.NewAIRequest("hello", "mapping"), false)
			if test.wantError == "" && prepareErr != nil {
				t.Fatalf("prepareInvocation returned error: %v", prepareErr)
			}
			if test.wantError != "" && (prepareErr == nil || errors.Unwrap(prepareErr) == nil ||
				!strings.Contains(errors.Unwrap(prepareErr).Error(), test.wantError)) {
				t.Fatalf("prepareInvocation error = %v", prepareErr)
			}
			requests := resolver.capturedRequests()
			if len(requests) == 0 || requests[0].ResolvedModel != semanticModel {
				t.Fatalf("endpoint requests = %#v", requests)
			}
		})
	}
}

func newVertexConcreteClient(
	t *testing.T,
	model string,
	resolver ai.EndpointResolver,
	credentials ai.CredentialSource,
	transport http.RoundTripper,
) *Client {
	t.Helper()
	requestClient, err := (&Factory{}).CreateRequestClient(&ai.AIConfig{
		ProviderAlias: "anthropic.vertex", Model: model, MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver, CredentialSource: credentials,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}
	return requestClient.(*Client)
}

func TestVertexEnvironmentAliasAndPublisherModelBoundaries(t *testing.T) {
	t.Setenv("TRUVAG3_ANTHROPIC_MODEL_SMART", "claude-environment-semantic")
	resolver := &vertexTestResolver{
		projectID: "acme-prod", location: "global",
		publisherModels: map[string]string{"claude-environment-semantic": "smart"},
		routeIdentity:   "opaque-publisher-v1",
	}
	credentials := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", "Bearer token"), nil
	}}
	transport := &vertexRecordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
		return phase4Response(request, http.StatusOK, phase4SuccessBody), nil
	}}
	client := newVertexTestClient(t, "smart", resolver, credentials, transport)
	if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "alias-boundary")); err != nil {
		t.Fatal(err)
	}
	requests := resolver.capturedRequests()
	if len(requests) != 1 || requests[0].ResolvedModel != "claude-environment-semantic" {
		t.Fatalf("endpoint requests = %#v", requests)
	}
	captured := transport.capturedRequests()
	if len(captured) != 1 || !strings.Contains(captured[0].url, "/models/smart:rawPredict") {
		t.Fatalf("publisher model was rewritten: %#v", captured)
	}
}

func TestVertexFingerprintUsesProfileAndSanitizedRouteIdentity(t *testing.T) {
	const semanticModel = "claude-sonnet-4-5-20250929"
	resolver := &vertexTestResolver{
		projectID: "acme-prod", location: "global",
		publisherModels: map[string]string{semanticModel: "publisher-model-one"},
		routeIdentity:   "vertex-fingerprint-v1", credentialScope: "secret-scope",
	}
	credentials := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", "Bearer secret-token"), nil
	}}
	transport := &vertexRecordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
		return phase4Response(request, http.StatusOK, phase4SuccessBody), nil
	}}
	client := newVertexConcreteClient(t, semanticModel, resolver, credentials, transport)
	request := core.NewAIRequest("secret-prompt", "fingerprint")
	first, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || first == "" {
		t.Fatalf("first fingerprint=%q stable=%t", first, stable)
	}
	resolver.publisherModels[semanticModel] = "publisher-model-two"
	second, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || second != first {
		t.Fatalf("publisher model entered fingerprint: first=%q second=%q stable=%t", first, second, stable)
	}
	resolver.routeIdentity = "vertex-fingerprint-v2"
	third, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || third == first {
		t.Fatalf("route identity did not change fingerprint: first=%q third=%q stable=%t", first, third, stable)
	}
	direct := NewClient("secret-key", "", &core.NoOpLogger{})
	direct.DefaultModel = semanticModel
	directFingerprint, stable := direct.RequestFingerprint(t.Context(), request)
	if !stable || directFingerprint == "" || directFingerprint == first {
		t.Fatalf("direct fingerprint=%q stable=%t", directFingerprint, stable)
	}
	for _, fingerprint := range []string{first, second, third, directFingerprint} {
		for _, secret := range []string{"publisher-model", "secret-scope", "secret-token", "secret-prompt"} {
			if strings.Contains(fingerprint, secret) {
				t.Fatalf("fingerprint leaked %q: %q", secret, fingerprint)
			}
		}
	}
}

func TestVertexStreamAndRetryContracts(t *testing.T) {
	const (
		semanticModel  = "claude-sonnet-4-5-20250929"
		publisherModel = "claude-sonnet-4-5@20250929"
	)
	resolver := &vertexTestResolver{
		projectID: "acme-prod", location: "us-central1",
		publisherModels: map[string]string{semanticModel: publisherModel},
		routeIdentity:   "vertex-stream-v1",
	}
	credentials := &phase4CredentialSource{credential: func(call int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", fmt.Sprintf("Bearer token-%d", call)), nil
	}}
	transport := &vertexRecordingTransport{respond: func(call int, request *http.Request) (*http.Response, error) {
		if call == 1 {
			return phase4Response(request, http.StatusInternalServerError, `{"error":"retry"}`), nil
		}
		body := strings.Join([]string{
			`data: {"type":"message_start","message":{"model":"claude-test","usage":{"input_tokens":1}}}`,
			`data: {"type":"content_block_delta","delta":{"text":"hello"}}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`,
			"",
		}, "\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)), Request: request,
		}, nil
	}}
	client := newVertexConcreteClient(t, semanticModel, resolver, credentials, transport)
	client.MaxRetries = 1
	client.RetryDelay = 0
	var content strings.Builder
	result, err := client.Stream(t.Context(), core.NewAIRequest("hello", "vertex-stream"), func(chunk core.StreamChunk) error {
		content.WriteString(chunk.Content)
		return nil
	})
	if err != nil || result == nil || result.Response == nil || result.Response.Content != "hello" || content.String() != "hello" {
		t.Fatalf("Stream result=%#v content=%q error=%v", result, content.String(), err)
	}
	requests := transport.capturedRequests()
	if len(requests) != 2 || credentials.calls.Load() != 2 {
		t.Fatalf("transport calls=%d credential calls=%d", len(requests), credentials.calls.Load())
	}
	wantURL := "https://us-central1-aiplatform.googleapis.com/v1/projects/acme-prod/locations/us-central1/publishers/anthropic/models/claude-sonnet-4-5@20250929:streamRawPredict"
	for index, request := range requests {
		if request.url != wantURL || request.headers.Get("Authorization") != fmt.Sprintf("Bearer token-%d", index+1) {
			t.Fatalf("request %d = %#v", index+1, request)
		}
		if request.body["anthropic_version"] != vertexAPIVersion || request.body["stream"] != true {
			t.Fatalf("stream body %d = %#v", index+1, request.body)
		}
		if _, present := request.body["model"]; present {
			t.Fatalf("stream body %d contains model: %#v", index+1, request.body)
		}
	}
	if !reflect.DeepEqual(requests[0].body, requests[1].body) {
		t.Fatalf("retry body drift: %#v vs %#v", requests[0].body, requests[1].body)
	}
}

func TestValidateVertexRouteLocationsAndOperations(t *testing.T) {
	for _, location := range []string{"global", "us", "eu", "us-central1"} {
		for _, operation := range []string{"generate", "stream"} {
			t.Run(location+"/"+operation, func(t *testing.T) {
				endpoint, err := vertexTestEndpoint("acme-prod", location, "claude-model@20260701", operation)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateVertexRoute(resolvedRoute{
					url: endpoint, deployment: "claude-model@20260701", identity: "route-v1",
				}, operation); err != nil {
					t.Fatalf("valid route rejected: %v", err)
				}
			})
		}
	}
}

func TestValidateVertexRouteRejectsMismatches(t *testing.T) {
	valid, err := vertexTestEndpoint("acme-prod", "us-central1", "claude-model@20260701", "generate")
	if err != nil {
		t.Fatal(err)
	}
	clone := func(source *url.URL) *url.URL {
		copy := *source
		return &copy
	}
	tests := []struct {
		name       string
		endpoint   *url.URL
		deployment string
		operation  string
	}{
		{name: "HTTP", endpoint: &url.URL{Scheme: "http", Host: valid.Host, Path: valid.Path}, deployment: "claude-model@20260701", operation: "generate"},
		{name: "port", endpoint: &url.URL{Scheme: "https", Host: valid.Host + ":443", Path: valid.Path}, deployment: "claude-model@20260701", operation: "generate"},
		{name: "userinfo", endpoint: &url.URL{Scheme: "https", Host: valid.Host, User: url.User("user"), Path: valid.Path}, deployment: "claude-model@20260701", operation: "generate"},
		{name: "query", endpoint: &url.URL{Scheme: "https", Host: valid.Host, Path: valid.Path, RawQuery: "key=value"}, deployment: "claude-model@20260701", operation: "generate"},
		{name: "fragment", endpoint: &url.URL{Scheme: "https", Host: valid.Host, Path: valid.Path, Fragment: "fragment"}, deployment: "claude-model@20260701", operation: "generate"},
		{name: "wrong host", endpoint: &url.URL{Scheme: "https", Host: "aiplatform.googleapis.com", Path: valid.Path}, deployment: "claude-model@20260701", operation: "generate"},
		{name: "wrong publisher", endpoint: &url.URL{Scheme: "https", Host: valid.Host, Path: strings.Replace(valid.Path, "/publishers/anthropic/", "/publishers/google/", 1)}, deployment: "claude-model@20260701", operation: "generate"},
		{name: "deployment mismatch", endpoint: clone(valid), deployment: "other-model", operation: "generate"},
		{name: "method mismatch", endpoint: clone(valid), deployment: "claude-model@20260701", operation: "stream"},
		{name: "invalid project", endpoint: &url.URL{Scheme: "https", Host: valid.Host, Path: strings.Replace(valid.Path, "/projects/acme-prod/", "/projects/Bad_Project/", 1)}, deployment: "claude-model@20260701", operation: "generate"},
		{name: "invalid publisher model", endpoint: &url.URL{Scheme: "https", Host: valid.Host, Path: strings.Replace(valid.Path, "claude-model@20260701", "bad$model", 1)}, deployment: "bad$model", operation: "generate"},
		{name: "unsupported operation", endpoint: clone(valid), deployment: "claude-model@20260701", operation: "fingerprint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateVertexRoute(resolvedRoute{
				url: test.endpoint, deployment: test.deployment, identity: "route-v1",
			}, test.operation); err == nil {
				t.Fatal("invalid route accepted")
			}
		})
	}
}

func TestVertexInvalidRouteStopsBeforeCredentialPolicyAndTransport(t *testing.T) {
	resolver := &phase4Resolver{endpoint: ai.ResolvedEndpoint{
		URL: &url.URL{
			Scheme: "https", Host: "aiplatform.googleapis.com:443",
			Path: "/v1/projects/acme-prod/locations/global/publishers/anthropic/models/publisher-model:rawPredict",
		},
		Deployment: "publisher-model", RouteIdentity: "invalid-route-v1",
	}}
	credentials := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", "Bearer token"), nil
	}}
	transportCalls := 0
	middleware := &phase3AnthropicMiddleware{}
	requestClient, err := (&Factory{}).CreateRequestClient(&ai.AIConfig{
		ProviderAlias: "anthropic.vertex", Model: "claude-sonnet-4-5-20250929", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver, CredentialSource: credentials,
		RequestMiddleware: []requestpolicy.RequestMiddleware{middleware},
		HTTPClient: &http.Client{Transport: phase4RoundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls++
			return nil, errors.New("unexpected transport")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requestClient.Generate(t.Context(), core.NewAIRequest("hello", "invalid-route")); err == nil {
		t.Fatal("invalid route accepted")
	}
	if credentials.calls.Load() != 0 || middleware.calls.Load() != 0 || transportCalls != 0 {
		t.Fatalf("credential=%d policy=%d transport=%d", credentials.calls.Load(), middleware.calls.Load(), transportCalls)
	}
}

func TestVertexCredentialValidation(t *testing.T) {
	valid := []ai.HeaderCredential{
		ai.NewHeaderCredential("Authorization", "Bearer token"),
		ai.NewHeaderCredential("authorization", "bearer token"),
	}
	for _, credential := range valid {
		if err := validateVertexCredential(credential); err != nil {
			t.Fatalf("valid credential %#v rejected: %v", credential, err)
		}
	}
	invalid := []ai.HeaderCredential{
		ai.NewHeaderCredential("x-api-key", "secret"),
		ai.NewHeaderCredential("Authorization", "Basic token"),
		ai.NewHeaderCredential("Authorization", "Bearer "),
		ai.NewHeaderCredential("Authorization", "Bearer bad\nvalue"),
	}
	for _, credential := range invalid {
		if err := validateVertexCredential(credential); err == nil {
			t.Fatalf("invalid credential %#v accepted", credential)
		}
	}
}

func TestVertexPolicyCannotChangeHostedStructuralContract(t *testing.T) {
	const semanticModel = "claude-sonnet-4-5-20250929"
	for _, test := range []struct {
		name       string
		set        map[string]interface{}
		setHeaders map[string]string
	}{
		{name: "body model", set: map[string]interface{}{`/model`: "attacker"}},
		{name: "body version", set: map[string]interface{}{`/anthropic_version`: "attacker"}},
		{name: "authorization", setHeaders: map[string]string{"Authorization": "Bearer attacker"}},
		{name: "Anthropic API key", setHeaders: map[string]string{"x-api-key": "attacker"}},
		{name: "Anthropic version header", setHeaders: map[string]string{"anthropic-version": "attacker"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &vertexTestResolver{
				projectID: "acme-prod", location: "global",
				publisherModels: map[string]string{semanticModel: "publisher-model"},
				routeIdentity:   "protected-contract-v1",
			}
			credentials := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
				return ai.NewHeaderCredential("Authorization", "Bearer token"), nil
			}}
			transportCalls := 0
			client, err := ai.NewRequestClient(
				ai.WithProviderAlias("anthropic.vertex"),
				ai.WithModel(semanticModel),
				ai.WithEndpointResolver(resolver),
				ai.WithCredentialSource(credentials),
				ai.WithRequestRules(core.AIProviderPatch{
					Name: "forbidden-structure", Version: "1",
					Selector: core.AIProviderSelector{ProviderAlias: "anthropic.vertex"},
					Set:      test.set, SetHeaders: test.setHeaders,
				}),
				ai.WithHTTPClient(&http.Client{Transport: phase4RoundTripFunc(func(*http.Request) (*http.Response, error) {
					transportCalls++
					return nil, errors.New("unexpected transport")
				})}),
			)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "protected"))
			var policyErr *requestpolicy.PolicyError
			if !errors.As(err, &policyErr) || result == nil || result.RequestReport == nil {
				t.Fatalf("Generate result=%#v error=%v", result, err)
			}
			if credentials.calls.Load() != 0 || transportCalls != 0 {
				t.Fatalf("credential=%d transport=%d", credentials.calls.Load(), transportCalls)
			}
		})
	}
}

func TestVertexResolverAndCredentialFailuresStopBeforeTransport(t *testing.T) {
	const semanticModel = "claude-sonnet-4-5-20250929"
	resolverErr := errors.New("Vertex resolver unavailable")
	credentialErr := errors.New("ADC unavailable")
	tests := []struct {
		name             string
		resolver         *vertexTestResolver
		credentials      *phase4CredentialSource
		want             error
		wantReport       bool
		wantResolverCall int
		wantCredCall     int64
	}{
		{
			name: "resolver failure",
			resolver: &vertexTestResolver{
				err:             resolverErr,
				publisherModels: map[string]string{semanticModel: "unused"},
			},
			credentials: &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
				return ai.NewHeaderCredential("Authorization", "Bearer unused"), nil
			}},
			want: resolverErr, wantResolverCall: 1,
		},
		{
			name: "credential failure",
			resolver: &vertexTestResolver{
				projectID: "acme-prod", location: "global",
				publisherModels: map[string]string{semanticModel: "claude-sonnet-4-5@20250929"},
				routeIdentity:   "vertex-credential-error-v1",
			},
			credentials: &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
				return ai.HeaderCredential{}, credentialErr
			}},
			want: credentialErr, wantReport: true, wantResolverCall: 1, wantCredCall: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transportCalls := 0
			requestClient := newVertexTestClient(t, semanticModel, test.resolver, test.credentials, phase4RoundTripFunc(func(*http.Request) (*http.Response, error) {
				transportCalls++
				return nil, errors.New("unexpected transport")
			}))
			result, err := requestClient.Generate(t.Context(), core.NewAIRequest("hello", "vertex-failure"))
			if !errors.Is(err, test.want) {
				t.Fatalf("Generate error = %v, want %v", err, test.want)
			}
			if test.wantReport != (result != nil && result.RequestReport != nil) {
				t.Fatalf("Generate result = %#v, want report=%t", result, test.wantReport)
			}
			if got := len(test.resolver.capturedRequests()); got != test.wantResolverCall {
				t.Fatalf("resolver calls = %d, want %d", got, test.wantResolverCall)
			}
			if got := test.credentials.calls.Load(); got != test.wantCredCall {
				t.Fatalf("credential calls = %d, want %d", got, test.wantCredCall)
			}
			if transportCalls != 0 {
				t.Fatalf("transport calls = %d", transportCalls)
			}
		})
	}
}

func TestDirectAnthropicProfileRemainsNativeForNonVertexAliases(t *testing.T) {
	for _, alias := range []string{"anthropic.primary", "anthropic.vertex.custom"} {
		t.Run(alias, func(t *testing.T) {
			resolver := &phase4Resolver{endpoint: ai.ResolvedEndpoint{
				URL:        &url.URL{Scheme: "https", Host: "gateway.example", Path: "/v1/messages"},
				Deployment: "route-model-must-be-ignored", RouteIdentity: "direct-profile-v1",
			}}
			client := NewClient("native-key", "", &core.NoOpLogger{})
			client.providerAlias = alias
			client.endpointResolver = resolver
			request := core.NewAIRequestFromLegacy("hello", "direct", &core.AIOptions{
				Model: "claude-sonnet-4-5-20250929", MaxTokens: 100,
			})
			invocation, err := client.prepareInvocation(t.Context(), request, false)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]interface{}
			if err := json.Unmarshal(invocation.Request.Body, &body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "claude-sonnet-4-5-20250929" {
				t.Fatalf("direct body = %#v", body)
			}
			if _, present := body["anthropic_version"]; present {
				t.Fatalf("direct body contains anthropic_version: %#v", body)
			}
			if invocation.Request.Headers.Get("anthropic-version") != APIVersion ||
				invocation.Request.Headers.Get("x-api-key") != "native-key" {
				t.Fatalf("direct headers = %#v", invocation.Request.Headers)
			}
		})
	}
}
