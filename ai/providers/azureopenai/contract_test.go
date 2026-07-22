package azureopenai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers/openai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type recordedRequest struct {
	url     string
	headers http.Header
	body    map[string]interface{}
}

type recordingTransport struct {
	requests []recordedRequest
	respond  func(int, *http.Request) (*http.Response, error)
}

func (transport *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	bodyBytes, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var body map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return nil, err
	}
	transport.requests = append(transport.requests, recordedRequest{
		url: request.URL.String(), headers: request.Header.Clone(), body: body,
	})
	return transport.respond(len(transport.requests), request)
}

type recordingCredentialSource struct {
	requests    []ai.CredentialRequest
	credentials []ai.HeaderCredential
	err         error
}

type rejectingCredentialSource struct {
	recordingCredentialSource
	rejections []ai.CredentialRequest
	statuses   []int
	err        error
}

func (source *rejectingCredentialSource) CredentialRejected(
	_ context.Context,
	request ai.CredentialRequest,
	status int,
) error {
	source.rejections = append(source.rejections, request)
	source.statuses = append(source.statuses, status)
	return source.err
}

type eventMiddleware struct {
	events *[]string
}

func (*eventMiddleware) Name() string                  { return "event-policy" }
func (*eventMiddleware) Version() string               { return "1" }
func (*eventMiddleware) StablePolicyFingerprint() bool { return true }
func (middleware *eventMiddleware) Apply(
	_ context.Context,
	_ requestpolicy.RequestEditor,
) error {
	*middleware.events = append(*middleware.events, "policy")
	return nil
}

type deploymentMapResolver struct {
	endpoint    *url.URL
	deployments map[string]string
	events      *[]string
	requests    []ai.EndpointRequest
}

func (resolver *deploymentMapResolver) ResolveEndpoint(
	_ context.Context,
	request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	resolver.requests = append(resolver.requests, request)
	if resolver.events != nil {
		*resolver.events = append(*resolver.events, "route")
	}
	deployment, ok := resolver.deployments[request.ResolvedModel]
	if !ok {
		return ai.ResolvedEndpoint{}, fmt.Errorf(
			"no deployment for semantic model %q",
			request.ResolvedModel,
		)
	}
	return ai.ResolvedEndpoint{
		URL: resolver.endpoint, Deployment: deployment, RouteIdentity: "deployment-map-v1",
	}, nil
}

func (source *recordingCredentialSource) Credential(
	_ context.Context,
	request ai.CredentialRequest,
) (ai.HeaderCredential, error) {
	source.requests = append(source.requests, request)
	if source.err != nil {
		return ai.HeaderCredential{}, source.err
	}
	index := len(source.requests) - 1
	if index >= len(source.credentials) {
		index = len(source.credentials) - 1
	}
	return source.credentials[index], nil
}

func TestAzureV1StaticAPIKeyReasoningContract(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "prod-o3-west", RouteIdentity: "azure-v1-route-v1",
	}}
	transport := &recordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
		return successResponse(request, "prod-o3-west", "answer"), nil
	}}
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "static-secret", Model: "smart",
		MaxTokens: 100, ReasoningEffort: "high", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver, HTTPClient: &http.Client{Transport: transport},
	})
	result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "contract"))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(resolver.requests) != 1 || resolver.requests[0].ResolvedModel != "o3" || resolver.requests[0].Operation != "generate" {
		t.Fatalf("endpoint requests = %#v", resolver.requests)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("transport calls = %d", len(transport.requests))
	}
	request := transport.requests[0]
	if request.url != "https://resource.openai.azure.com/openai/v1/chat/completions" {
		t.Fatalf("request URL = %q", request.url)
	}
	if request.headers.Get("api-key") != "static-secret" || request.headers.Get("Authorization") != "" {
		t.Fatalf("request headers = %#v", request.headers)
	}
	if request.body["model"] != "prod-o3-west" || request.body["reasoning_effort"] != "high" || request.body["max_completion_tokens"] != float64(500) {
		t.Fatalf("request body = %#v", request.body)
	}
	for _, absent := range []string{"reasoning", "max_tokens", "temperature"} {
		if _, present := request.body[absent]; present {
			t.Fatalf("request body unexpectedly contains %q: %#v", absent, request.body)
		}
	}
	if result.Response.Provider != "azureopenai.v1" || result.RequestReport == nil ||
		result.RequestReport.RequestedModel != "smart" || result.RequestReport.ResolvedModel != "o3" {
		t.Fatalf("result = %#v", result)
	}
	encodedReport, _ := json.Marshal(result.RequestReport)
	if bytes.Contains(encodedReport, []byte("prod-o3-west")) || bytes.Contains(encodedReport, []byte("static-secret")) {
		t.Fatalf("request report leaked route or credential: %s", encodedReport)
	}
}

func TestAzureClassicBearerContractAndAliasCollision(t *testing.T) {
	t.Setenv("TRUVAG3_OPENAI_MODEL_FAST", "semantic-override-must-not-touch-deployment")
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/deployments/fast/chat/completions"),
		Deployment: "fast", RouteIdentity: "azure-classic-route-v1",
		Query: url.Values{"api-version": {"2024-10-21"}}, CredentialScope: "azure-scope",
	}}
	credentials := &recordingCredentialSource{credentials: []ai.HeaderCredential{
		ai.NewHeaderCredential("Authorization", "Bearer entra-token"),
	}}
	transport := &recordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
		return successResponse(request, "fast", "classic"), nil
	}}
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.classic", Model: "gpt-4.1", MaxTokens: 100, MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver, CredentialSource: credentials,
		HTTPClient: &http.Client{Transport: transport},
	})
	result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "classic"))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	request := transport.requests[0]
	if request.url != "https://resource.openai.azure.com/openai/deployments/fast/chat/completions?api-version=2024-10-21" {
		t.Fatalf("request URL = %q", request.url)
	}
	if request.headers.Get("Authorization") != "Bearer entra-token" || request.headers.Get("api-key") != "" {
		t.Fatalf("request headers = %#v", request.headers)
	}
	if _, present := request.body["model"]; present {
		t.Fatalf("classic body contains model: %#v", request.body)
	}
	if request.body["max_tokens"] != float64(100) {
		t.Fatalf("classic body = %#v", request.body)
	}
	if len(credentials.requests) != 1 || credentials.requests[0].ResolvedModel != "gpt-4.1" ||
		credentials.requests[0].Deployment != "fast" || credentials.requests[0].CredentialScope != "azure-scope" {
		t.Fatalf("credential requests = %#v", credentials.requests)
	}
	if result.Response.Provider != "azureopenai.classic" {
		t.Fatalf("response provider = %q", result.Response.Provider)
	}
}

func TestAzureSemanticCatalogBoundary(t *testing.T) {
	original := openai.ModelAliases["openai"]["smart"]
	t.Cleanup(func() { openai.ModelAliases["openai"]["smart"] = original })
	openai.ModelAliases["openai"]["smart"] = "runtime-mutated-model"
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "prod-o3", RouteIdentity: "catalog-route-v1",
	}}
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "smart", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{EndpointResolver: resolver})
	if _, err := client.prepareInvocation(t.Context(), core.NewAIRequest("hello", "catalog"), false); err != nil {
		t.Fatalf("prepareInvocation returned error: %v", err)
	}
	if len(resolver.requests) != 1 || resolver.requests[0].ResolvedModel != "o3" {
		t.Fatalf("Azure semantic resolution used mutable OpenAI aliases: %#v", resolver.requests)
	}

	t.Setenv("TRUVAG3_OPENAI_MODEL_SMART", "environment-semantic-model")
	resolver.requests = nil
	if _, err := client.prepareInvocation(t.Context(), core.NewAIRequest("hello", "catalog"), false); err != nil {
		t.Fatalf("prepareInvocation with environment override returned error: %v", err)
	}
	if resolver.requests[0].ResolvedModel != "environment-semantic-model" {
		t.Fatalf("environment semantic resolution = %#v", resolver.requests)
	}

	t.Setenv("TRUVAG3_OPENAI_MODEL_PRIVATE-MODEL", "")
	client.DefaultModel = "private-model"
	resolver.requests = nil
	if _, err := client.prepareInvocation(t.Context(), core.NewAIRequest("hello", "catalog"), false); err != nil {
		t.Fatalf("prepareInvocation with unknown model returned error: %v", err)
	}
	if resolver.requests[0].ResolvedModel != "private-model" {
		t.Fatalf("unknown semantic model was not passed through: %#v", resolver.requests)
	}
}

func TestAzureResolverMapUsesPostAliasSemanticModel(t *testing.T) {
	endpoint := mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions")
	for _, test := range []struct {
		name        string
		deployments map[string]string
		wantError   string
	}{
		{name: "post-alias key", deployments: map[string]string{"o3": "prod-o3"}},
		{name: "application alias key", deployments: map[string]string{"smart": "prod-o3"}, wantError: `no deployment for semantic model "o3"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &deploymentMapResolver{endpoint: endpoint, deployments: test.deployments}
			client := mustClient(t, &ai.AIConfig{
				ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "smart", MaxRetries: 0,
			}, ai.ProviderIntegrationConfig{EndpointResolver: resolver})
			_, err := client.prepareInvocation(t.Context(), core.NewAIRequest("hello", "mapping"), false)
			if test.wantError == "" && err != nil {
				t.Fatalf("prepareInvocation returned error: %v", err)
			}
			if test.wantError != "" && (err == nil || errors.Unwrap(err) == nil ||
				!strings.Contains(errors.Unwrap(err).Error(), test.wantError)) {
				t.Fatalf("prepareInvocation error = %v", err)
			}
			if len(resolver.requests) != 1 || resolver.requests[0].ResolvedModel != "o3" {
				t.Fatalf("resolver requests = %#v", resolver.requests)
			}
		})
	}
}

func TestAzureDeploymentAliasesRemainOpaque(t *testing.T) {
	for _, deployment := range []string{"fast", "smart", "vision", "code", "default"} {
		t.Run(deployment, func(t *testing.T) {
			t.Setenv("TRUVAG3_OPENAI_MODEL_"+strings.ToUpper(deployment), "must-not-rewrite-deployment")
			resolver := &testResolver{resolved: ai.ResolvedEndpoint{
				URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
				Deployment: deployment, RouteIdentity: "opaque-deployment-v1",
			}}
			client := mustClient(t, &ai.AIConfig{
				ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "gpt-4.1", MaxRetries: 0,
			}, ai.ProviderIntegrationConfig{EndpointResolver: resolver})
			invocation, err := client.prepareInvocation(t.Context(), core.NewAIRequest("hello", "opaque"), false)
			if err != nil {
				t.Fatalf("prepareInvocation returned error: %v", err)
			}
			var body map[string]interface{}
			if err := json.Unmarshal(invocation.Request.Body, &body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != deployment {
				t.Fatalf("wire model = %#v", body["model"])
			}
		})
	}
}

func TestAzurePreparationOrderAndResolverCardinality(t *testing.T) {
	for _, operation := range []string{"generate", "stream", "fingerprint"} {
		t.Run(operation, func(t *testing.T) {
			events := []string{}
			resolver := &deploymentMapResolver{
				endpoint:    mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
				deployments: map[string]string{"gpt-4.1": "prod-chat"}, events: &events,
			}
			transport := &recordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
				if operation == "stream" {
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
						Body:    io.NopCloser(strings.NewReader("data: {\"model\":\"prod-chat\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n")),
						Request: request,
					}, nil
				}
				return successResponse(request, "prod-chat", "ok"), nil
			}}
			client := mustClient(t, &ai.AIConfig{
				ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "gpt-4.1", MaxRetries: 0,
			}, ai.ProviderIntegrationConfig{
				EndpointResolver: resolver, RequestMiddleware: []requestpolicy.RequestMiddleware{&eventMiddleware{events: &events}},
				HTTPClient: &http.Client{Transport: transport},
			})
			request := core.NewAIRequest("hello", "lifecycle")
			switch operation {
			case "generate":
				_, err := client.Generate(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
			case "stream":
				_, err := client.Stream(t.Context(), request, func(core.StreamChunk) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
			case "fingerprint":
				if fingerprint, stable := client.RequestFingerprint(t.Context(), request); !stable || fingerprint == "" {
					t.Fatalf("fingerprint = %q, stable = %t", fingerprint, stable)
				}
			}
			if !reflect.DeepEqual(events, []string{"route", "policy"}) {
				t.Fatalf("events = %#v", events)
			}
			if len(resolver.requests) != 1 {
				t.Fatalf("resolver calls = %d", len(resolver.requests))
			}
			wantOperation := operation
			if operation == "fingerprint" {
				wantOperation = "generate"
			}
			if resolver.requests[0].Operation != wantOperation {
				t.Fatalf("endpoint operation = %q", resolver.requests[0].Operation)
			}
		})
	}
}

func TestAzureClassicRejectsReasoningBeforePolicyCredentialAndTransport(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/deployments/prod-o3/chat/completions?api-version=2024-10-21"),
		Deployment: "prod-o3", RouteIdentity: "classic-reasoning-route-v1",
	}}
	credentials := &recordingCredentialSource{credentials: []ai.HeaderCredential{
		ai.NewHeaderCredential("api-key", "secret"),
	}}
	transportCalls := 0
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.classic", Model: "smart", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver, CredentialSource: credentials,
		RequestRules: []core.AIProviderPatch{{
			Name: "later-policy", Version: "1", Selector: core.AIProviderSelector{AllProviders: true},
			Set: map[string]interface{}{"/temperature": 0.2},
		}},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls++
			return nil, errors.New("unexpected transport")
		})},
	})
	result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "classic-reasoning"))
	if result != nil || !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("Generate result=%#v error=%v", result, err)
	}
	if resolver.calls != 1 || len(credentials.requests) != 0 || transportCalls != 0 {
		t.Fatalf("calls: resolver=%d credential=%d transport=%d", resolver.calls, len(credentials.requests), transportCalls)
	}
}

func TestAzureCredentialsRefreshPerRetry(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "prod-chat", RouteIdentity: "retry-route-v1",
	}}
	credentials := &recordingCredentialSource{credentials: []ai.HeaderCredential{
		ai.NewHeaderCredential("Authorization", "Bearer token-one"),
		ai.NewHeaderCredential("Authorization", "Bearer token-two"),
	}}
	transport := &recordingTransport{respond: func(call int, request *http.Request) (*http.Response, error) {
		if call == 1 {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"retry"}}`)), Request: request,
			}, nil
		}
		return successResponse(request, "prod-chat", "retried"), nil
	}}
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", Model: "gpt-4.1", MaxRetries: 1,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver, CredentialSource: credentials,
		HTTPClient: &http.Client{Transport: transport},
	})
	client.RetryDelay = 0
	if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "retry")); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(credentials.requests) != 2 || len(transport.requests) != 2 {
		t.Fatalf("credential calls=%d transport calls=%d", len(credentials.requests), len(transport.requests))
	}
	if transport.requests[0].headers.Get("Authorization") != "Bearer token-one" ||
		transport.requests[1].headers.Get("Authorization") != "Bearer token-two" {
		t.Fatalf("retry headers = %#v, %#v", transport.requests[0].headers, transport.requests[1].headers)
	}
	if !reflect.DeepEqual(transport.requests[0].body, transport.requests[1].body) {
		t.Fatalf("retry body drift = %#v vs %#v", transport.requests[0].body, transport.requests[1].body)
	}
}

func TestAzureCredentialRejectionObserver(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "prod-chat", RouteIdentity: "rejection-route-v1", CredentialScope: "entra-scope",
	}}
	credentials := &rejectingCredentialSource{
		recordingCredentialSource: recordingCredentialSource{credentials: []ai.HeaderCredential{
			ai.NewHeaderCredential("Authorization", "Bearer rejected-token"),
		}},
		err: errors.New("observer diagnostic failure"),
	}
	transport := &recordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unauthorized"}}`)),
			Request:    request,
		}, nil
	}}
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", Model: "gpt-4.1", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver, CredentialSource: credentials,
		HTTPClient: &http.Client{Transport: transport},
	})
	if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "rejection")); err == nil {
		t.Fatal("Generate returned nil error")
	}
	if len(credentials.rejections) != 1 || len(credentials.statuses) != 1 ||
		credentials.statuses[0] != http.StatusUnauthorized {
		t.Fatalf("rejections=%#v statuses=%#v", credentials.rejections, credentials.statuses)
	}
	request := credentials.rejections[0]
	if request.ResolvedModel != "gpt-4.1" || request.Deployment != "prod-chat" ||
		request.CredentialScope != "entra-scope" {
		t.Fatalf("rejection request = %#v", request)
	}
}

func TestAzureStreamUsesProfiledBodyAndGracefulCallbackStop(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "prod-chat", RouteIdentity: "stream-route-v1",
	}}
	transport := &recordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body:    io.NopCloser(strings.NewReader("data: {\"model\":\"prod-chat\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n")),
			Request: request,
		}, nil
	}}
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "gpt-4.1", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{EndpointResolver: resolver, HTTPClient: &http.Client{Transport: transport}})
	stop := errors.New("stop")
	result, err := client.Stream(t.Context(), core.NewAIRequest("hello", "stream"), func(core.StreamChunk) error {
		return stop
	})
	if err != nil || result == nil || result.Response == nil || result.Response.Content != "hello" {
		t.Fatalf("Stream result=%#v error=%v", result, err)
	}
	body := transport.requests[0].body
	if body["stream"] != true || body["model"] != "prod-chat" {
		t.Fatalf("stream body = %#v", body)
	}
}

func TestAzureClassicStreamOmitsBodyModel(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/deployments/prod-chat/chat/completions"),
		Query:      url.Values{"api-version": {"2024-10-21"}},
		Deployment: "prod-chat", RouteIdentity: "classic-stream-route-v1",
	}}
	transport := &recordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body:    io.NopCloser(strings.NewReader("data: {\"model\":\"prod-chat\",\"choices\":[{\"delta\":{\"content\":\"classic\"}}]}\n\ndata: [DONE]\n")),
			Request: request,
		}, nil
	}}
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.classic", APIKey: "key", Model: "gpt-4.1", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver, HTTPClient: &http.Client{Transport: transport},
	})
	result, err := client.Stream(t.Context(), core.NewAIRequest("hello", "classic-stream"), func(core.StreamChunk) error {
		return nil
	})
	if err != nil || result == nil || result.Response == nil || result.Response.Content != "classic" {
		t.Fatalf("Stream result=%#v error=%v", result, err)
	}
	request := transport.requests[0]
	if request.url != "https://resource.openai.azure.com/openai/deployments/prod-chat/chat/completions?api-version=2024-10-21" {
		t.Fatalf("request URL = %q", request.url)
	}
	if request.body["stream"] != true {
		t.Fatalf("stream body = %#v", request.body)
	}
	if _, present := request.body["model"]; present {
		t.Fatalf("classic stream body contains model: %#v", request.body)
	}
}

func TestAzureFingerprintUsesSurfaceAndSanitizedRouteIdentity(t *testing.T) {
	request := core.NewAIRequest("hello", "fingerprint")
	v1Resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://secret-resource.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "secret-deployment-one", RouteIdentity: "fingerprint-route-v1",
	}}
	v1 := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "secret-key", Model: "gpt-4.1", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{EndpointResolver: v1Resolver})
	first, stable := v1.RequestFingerprint(t.Context(), request)
	if !stable || first == "" {
		t.Fatalf("first fingerprint = %q, stable = %t", first, stable)
	}
	v1Resolver.resolved.Deployment = "secret-deployment-two"
	second, stable := v1.RequestFingerprint(t.Context(), request)
	if !stable || second != first {
		t.Fatalf("deployment entered fingerprint: first=%q second=%q stable=%t", first, second, stable)
	}
	v1Resolver.resolved.RouteIdentity = "fingerprint-route-v2"
	third, stable := v1.RequestFingerprint(t.Context(), request)
	if !stable || third == first {
		t.Fatalf("route identity did not change fingerprint: first=%q third=%q stable=%t", first, third, stable)
	}

	classicResolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://secret-resource.openai.azure.com/openai/deployments/secret-deployment-one/chat/completions?api-version=2024-10-21"),
		Deployment: "secret-deployment-one", RouteIdentity: "fingerprint-route-v1",
	}}
	classic := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.classic", APIKey: "secret-key", Model: "gpt-4.1", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{EndpointResolver: classicResolver})
	classicFingerprint, stable := classic.RequestFingerprint(t.Context(), request)
	if !stable || classicFingerprint == "" || classicFingerprint == first {
		t.Fatalf("classic fingerprint = %q, stable = %t", classicFingerprint, stable)
	}
	for _, secret := range []string{"secret-resource", "secret-deployment", "secret-key"} {
		for _, fingerprint := range []string{first, second, third, classicFingerprint} {
			if strings.Contains(fingerprint, secret) {
				t.Fatalf("fingerprint leaked %q: %q", secret, fingerprint)
			}
		}
	}
}

func TestAzurePolicyCannotSetAPIKeyHeader(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "prod-chat", RouteIdentity: "protected-header-route-v1",
	}}
	transportCalls := 0
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "static-key", Model: "gpt-4.1", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver,
		RequestRules: []core.AIProviderPatch{{
			Name: "forbidden-auth", Version: "1", Selector: core.AIProviderSelector{AllProviders: true},
			SetHeaders: map[string]string{"api-key": "policy-key"},
		}},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls++
			return nil, errors.New("unexpected transport")
		})},
	})
	result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "policy"))
	var policyErr *requestpolicy.PolicyError
	if result == nil || result.RequestReport == nil || !errors.As(err, &policyErr) {
		t.Fatalf("Generate result=%#v error=%v", result, err)
	}
	if resolver.calls != 1 || transportCalls != 0 {
		t.Fatalf("resolver calls=%d transport calls=%d", resolver.calls, transportCalls)
	}
}

func TestValidateResolvedRoute(t *testing.T) {
	validV1 := resolvedRoute{
		url:      mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
		identity: "route-v1", deployment: "prod-chat",
	}
	if err := validateResolvedRoute(surfaceV1, validV1); err != nil {
		t.Fatalf("valid v1 rejected: %v", err)
	}
	withVersion := validV1
	withVersion.url = mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions?api-version=preview")
	if err := validateResolvedRoute(surfaceV1, withVersion); err != nil {
		t.Fatalf("v1 api-version rejected: %v", err)
	}
	validClassic := resolvedRoute{
		url:      mustURL(t, "https://resource.openai.azure.com/openai/deployments/prod-chat/chat/completions?api-version=2024-10-21&trace=enabled"),
		identity: "classic-v1", deployment: "prod-chat",
	}
	if err := validateResolvedRoute(surfaceClassic, validClassic); err != nil {
		t.Fatalf("valid classic rejected: %v", err)
	}

	tests := []struct {
		name    string
		surface surface
		route   resolvedRoute
	}{
		{name: "HTTP", surface: surfaceV1, route: resolvedRoute{url: mustURL(t, "http://resource.openai.azure.com/openai/v1/chat/completions"), identity: "route", deployment: "prod"}},
		{name: "port", surface: surfaceV1, route: resolvedRoute{url: mustURL(t, "https://resource.openai.azure.com:443/openai/v1/chat/completions"), identity: "route", deployment: "prod"}},
		{name: "user info", surface: surfaceV1, route: resolvedRoute{url: mustURL(t, "https://user@resource.openai.azure.com/openai/v1/chat/completions"), identity: "route", deployment: "prod"}},
		{name: "fragment", surface: surfaceV1, route: resolvedRoute{url: mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions#fragment"), identity: "route", deployment: "prod"}},
		{name: "wrong v1 path", surface: surfaceV1, route: resolvedRoute{url: mustURL(t, "https://resource.openai.azure.com/v1/chat/completions"), identity: "route", deployment: "prod"}},
		{name: "repeated v1 version", surface: surfaceV1, route: resolvedRoute{url: mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions?api-version=a&api-version=b"), identity: "route", deployment: "prod"}},
		{name: "missing classic version", surface: surfaceClassic, route: resolvedRoute{url: mustURL(t, "https://resource.openai.azure.com/openai/deployments/prod/chat/completions"), identity: "route", deployment: "prod"}},
		{name: "classic deployment mismatch", surface: surfaceClassic, route: resolvedRoute{url: mustURL(t, "https://resource.openai.azure.com/openai/deployments/other/chat/completions?api-version=2024-10-21"), identity: "route", deployment: "prod"}},
		{name: "empty identity", surface: surfaceV1, route: resolvedRoute{url: validV1.url, deployment: "prod"}},
		{name: "empty deployment", surface: surfaceV1, route: resolvedRoute{url: validV1.url, identity: "route"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateResolvedRoute(test.surface, test.route); err == nil {
				t.Fatal("invalid route accepted")
			}
		})
	}
}

func TestResolvedEndpointIsSnapshotted(t *testing.T) {
	endpoint := mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions?trace=original")
	query := url.Values{"api-version": {"preview"}}
	snapshot, err := snapshotResolvedEndpoint(ai.ResolvedEndpoint{
		URL: endpoint, Query: query, Deployment: "prod", RouteIdentity: "snapshot-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Host = "mutated.example"
	query.Set("api-version", "mutated")
	if snapshot.URL.Host != "resource.openai.azure.com" ||
		snapshot.URL.Query().Get("trace") != "original" ||
		snapshot.URL.Query().Get("api-version") != "preview" ||
		snapshot.Query.Get("api-version") != "preview" {
		t.Fatalf("snapshot mutated: %#v", snapshot)
	}
}

func TestAzureInvalidRouteStopsBeforeCredentialAndTransport(t *testing.T) {
	for _, test := range []struct {
		name     string
		alias    string
		endpoint string
	}{
		{name: "v1 explicit port", alias: "azureopenai.v1", endpoint: "https://resource.openai.azure.com:443/openai/v1/chat/completions"},
		{name: "classic explicit port", alias: "azureopenai.classic", endpoint: "https://resource.openai.azure.com:443/openai/deployments/prod/chat/completions?api-version=2024-10-21"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &testResolver{resolved: ai.ResolvedEndpoint{
				URL: mustURL(t, test.endpoint), Deployment: "prod", RouteIdentity: "invalid-port-v1",
			}}
			credentials := &recordingCredentialSource{credentials: []ai.HeaderCredential{
				ai.NewHeaderCredential("api-key", "secret"),
			}}
			transportCalls := 0
			client := mustClient(t, &ai.AIConfig{
				ProviderAlias: test.alias, Model: "gpt-4.1", MaxRetries: 0,
			}, ai.ProviderIntegrationConfig{
				EndpointResolver: resolver, CredentialSource: credentials,
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					transportCalls++
					return nil, errors.New("unexpected transport")
				})},
			})
			if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "invalid-route")); err == nil {
				t.Fatal("invalid route accepted")
			}
			if len(credentials.requests) != 0 || transportCalls != 0 {
				t.Fatalf("credential calls=%d transport calls=%d", len(credentials.requests), transportCalls)
			}
		})
	}
}

func TestValidateAzureCredential(t *testing.T) {
	valid := []ai.HeaderCredential{
		ai.NewHeaderCredential("api-key", "secret"),
		ai.NewHeaderCredential("API-KEY", "secret"),
		ai.NewHeaderCredential("Authorization", "Bearer token"),
		ai.NewHeaderCredential("authorization", "bearer token"),
	}
	for _, credential := range valid {
		if err := validateAzureCredential(credential); err != nil {
			t.Fatalf("valid credential %#v rejected: %v", credential, err)
		}
	}
	invalid := []ai.HeaderCredential{
		ai.NewHeaderCredential("", "secret"),
		ai.NewHeaderCredential("X-Key", "secret"),
		ai.NewHeaderCredential("Authorization", "Basic secret"),
		ai.NewHeaderCredential("Authorization", "Bearer "),
		ai.NewHeaderCredential("api-key", "bad\nvalue"),
	}
	for _, credential := range invalid {
		if err := validateAzureCredential(credential); err == nil {
			t.Fatalf("invalid credential %#v accepted", credential)
		}
	}
}

func mustClient(
	t *testing.T,
	config *ai.AIConfig,
	integration ai.ProviderIntegrationConfig,
) *Client {
	t.Helper()
	client, err := (&Factory{}).CreateRequestClient(config, integration)
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}
	result, ok := client.(*Client)
	if !ok {
		t.Fatalf("client type = %T", client)
	}
	return result
}

func successResponse(request *http.Request, model, content string) *http.Response {
	body := `{"model":` + strconvQuote(model) + `,"choices":[{"message":{"content":` + strconvQuote(content) + `}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)), Request: request,
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
