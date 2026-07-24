package anthropic_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
)

type publicVertexResolver struct {
	endpoint *url.URL
}

func (resolver publicVertexResolver) ResolveEndpoint(
	_ context.Context,
	_ ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	return ai.ResolvedEndpoint{
		URL: resolver.endpoint, Deployment: "claude-publisher-model",
		RouteIdentity: "public-vertex-route-v1",
	}, nil
}

type publicVertexCredentials struct{}

func (publicVertexCredentials) Credential(
	_ context.Context,
	_ ai.CredentialRequest,
) (ai.HeaderCredential, error) {
	return ai.NewHeaderCredential("Authorization", "Bearer public-test-token"), nil
}

func TestPublicVertexConstructionCompiles(t *testing.T) {
	endpoint, err := url.Parse(
		"https://aiplatform.googleapis.com/v1/projects/acme-prod/locations/global/" +
			"publishers/anthropic/models/claude-publisher-model:rawPredict",
	)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ai.NewRequestClient(
		ai.WithProviderAlias("anthropic.vertex"),
		ai.WithModel("claude-sonnet-4-5-20250929"),
		ai.WithEndpointResolver(publicVertexResolver{endpoint: endpoint}),
		ai.WithCredentialSource(publicVertexCredentials{}),
	)
	if err != nil {
		t.Fatalf("ai.NewRequestClient returned error: %v", err)
	}
	if client == nil {
		t.Fatal("ai.NewRequestClient returned nil")
	}
}
