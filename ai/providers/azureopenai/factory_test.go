package azureopenai

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

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
