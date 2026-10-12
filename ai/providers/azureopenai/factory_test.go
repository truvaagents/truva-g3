package azureopenai

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestRegisteredAzureFactoryReasoningMultiplierEnvironment(t *testing.T) {
	const key = "TRUVAG3_AI_REASONING_TOKEN_MULTIPLIER"
	for _, tc := range []struct {
		name    string
		raw     string
		present bool
		code    int
		want    int
	}{
		{name: "default", want: 300},
		{name: "environment", present: true, raw: "5", want: 500},
		{name: "code-overrides-environment", present: true, raw: "5", code: 1, want: 100},
		{name: "empty", present: true},
		{name: "zero", present: true, raw: "0"},
		{name: "negative", present: true, raw: "-1"},
		{name: "malformed", present: true, raw: "bad-multiplier"},
		{name: "invalid-environment-with-code", present: true, raw: "bad-multiplier", code: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.raw)
			if !tc.present {
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			resolver := &testResolver{resolved: ai.ResolvedEndpoint{
				URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
				Deployment: "prod-chat", RouteIdentity: "azure-multiplier-probe",
			}}
			transport := &recordingTransport{respond: func(_ int, request *http.Request) (*http.Response, error) {
				return successResponse(request, "prod-chat", "ok"), nil
			}}
			client, err := ai.NewRequestClient(
				ai.WithProviderAlias("azureopenai.v1"), ai.WithAPIKey("test-key"),
				ai.WithModel("gpt-5.6-sol"), ai.WithMaxTokens(100), ai.WithMaxRetries(0),
				ai.WithReasoningEffort("high"), ai.WithReasoningTokenMultiplier(tc.code),
				ai.WithEndpointResolver(resolver), ai.WithLogger(&core.NoOpLogger{}),
				ai.WithHTTPClient(&http.Client{Transport: transport}),
			)
			if tc.want == 0 {
				if client != nil || !errors.Is(err, core.ErrInvalidConfiguration) || len(transport.requests) != 0 || resolver.calls != 0 {
					t.Fatalf("invalid multiplier reached provider: %T %v", client, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Generate(t.Context(), core.NewAIRequestFromLegacy("hello", "contract", &core.AIOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			if len(transport.requests) != 1 || transport.requests[0].body["max_completion_tokens"] != float64(tc.want) {
				t.Fatalf("wrong wire budget: %#v, want %d", transport.requests, tc.want)
			}
			if result.RequestReport == nil || result.RequestReport.EffectiveMaxTokens.Value != tc.want {
				t.Fatalf("effective request report missed multiplier: %#v", result.RequestReport)
			}
		})
	}
}

func TestFactorySupportsOnlyRequestAwareConstruction(t *testing.T) {
	factory := &Factory{}
	if factory.Name() != providerName || factory.Description() != "Azure OpenAI" {
		t.Fatalf("factory identity = %q, %q", factory.Name(), factory.Description())
	}
	if priority, available := factory.DetectEnvironment(); priority != 0 || available {
		t.Fatalf("DetectEnvironment = %d, %t", priority, available)
	}
	if _, err := factory.CreateValidated(&ai.AIConfig{}); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("CreateValidated error = %v", err)
	}
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("Factory.Create did not panic")
		}
	}()
	_ = factory.Create(&ai.AIConfig{})
}

func TestRegisteredFactoryRejectsLegacyAndAcceptsRequestConstruction(t *testing.T) {
	if _, err := ai.NewClient(ai.WithProviderAlias("azureopenai.v1")); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("ai.NewClient error = %v", err)
	}
	endpoint := mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions")
	client, err := ai.NewRequestClient(
		ai.WithProviderAlias("azureopenai.v1"),
		ai.WithAPIKey("static-key"),
		ai.WithModel("gpt-4.1"),
		ai.WithEndpointResolver(&testResolver{resolved: ai.ResolvedEndpoint{
			URL: endpoint, Deployment: "prod-chat", RouteIdentity: "azure-v1-route-v1",
		}}),
		ai.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return successResponse(request, "prod-chat", "ok"), nil
		})}),
	)
	if err != nil {
		t.Fatalf("ai.NewRequestClient returned error: %v", err)
	}
	if client == nil {
		t.Fatal("ai.NewRequestClient returned nil")
	}
}

func TestFactoryRequestConstructionValidation(t *testing.T) {
	endpoint := mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions")
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL: endpoint, Deployment: "prod-chat", RouteIdentity: "route-v1",
	}}
	tests := []struct {
		name        string
		config      *ai.AIConfig
		integration ai.ProviderIntegrationConfig
	}{
		{name: "nil config"},
		{name: "unknown alias", config: &ai.AIConfig{ProviderAlias: "azureopenai.unknown", APIKey: "key"}, integration: ai.ProviderIntegrationConfig{EndpointResolver: resolver}},
		{name: "missing resolver", config: &ai.AIConfig{ProviderAlias: "azureopenai.v1", APIKey: "key"}},
		{name: "missing credential", config: &ai.AIConfig{ProviderAlias: "azureopenai.v1"}, integration: ai.ProviderIntegrationConfig{EndpointResolver: resolver}},
		{name: "base URL forbidden", config: &ai.AIConfig{ProviderAlias: "azureopenai.v1", APIKey: "key", BaseURL: "https://resource.openai.azure.com"}, integration: ai.ProviderIntegrationConfig{EndpointResolver: resolver}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := (&Factory{}).CreateRequestClient(test.config, test.integration); err == nil {
				t.Fatal("CreateRequestClient accepted invalid configuration")
			}
		})
	}
}

type testResolver struct {
	resolved ai.ResolvedEndpoint
	err      error
	calls    int
	requests []ai.EndpointRequest
}

func (resolver *testResolver) ResolveEndpoint(
	_ context.Context,
	request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	resolver.calls++
	resolver.requests = append(resolver.requests, request)
	return resolver.resolved, resolver.err
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
