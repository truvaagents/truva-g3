package azureopenai_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	_ "github.com/truvaagents/truva-g3/ai/providers/azureopenai"
)

type publicResolver struct {
	endpoint *url.URL
}

func (resolver publicResolver) ResolveEndpoint(
	_ context.Context,
	_ ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	return ai.ResolvedEndpoint{
		URL: resolver.endpoint, Deployment: "public-deployment", RouteIdentity: "public-route-v1",
	}, nil
}

func TestPublicRequestAwareConstructionCompiles(t *testing.T) {
	endpoint, err := url.Parse("https://resource.openai.azure.com/openai/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	client, err := ai.NewRequestClient(
		ai.WithProviderAlias("azureopenai.v1"),
		ai.WithModel("gpt-4.1"),
		ai.WithAPIKey("public-test-key"),
		ai.WithEndpointResolver(publicResolver{endpoint: endpoint}),
	)
	if err != nil {
		t.Fatalf("ai.NewRequestClient returned error: %v", err)
	}
	if client == nil {
		t.Fatal("ai.NewRequestClient returned nil")
	}
}
