package guidetests

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
	_ "github.com/truvaagents/truva-g3/ai/providers/azureopenai"
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"
	"github.com/truvaagents/truva-g3/core"
)

type guideAccessTokenSource interface {
	AccessToken(context.Context) (string, error)
}

type guideStaticTokenSource string

func (source guideStaticTokenSource) AccessToken(context.Context) (string, error) {
	return string(source), nil
}

func guideBearerHeader(source guideAccessTokenSource) ai.AuthHeaderFunc {
	return func(ctx context.Context) (string, error) {
		if source == nil {
			return "", errors.New("access token source is nil")
		}
		token, err := source.AccessToken(ctx)
		if err != nil {
			return "", fmt.Errorf("get access token: %w", err)
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return "", errors.New("access token source returned an empty token")
		}
		return "Bearer " + token, nil
	}
}

type guideAzureOpenAIResolver struct {
	providerAlias string
	origin        *url.URL
	deployments   map[string]string
	apiVersion    string
	routeIdentity string
}

func newGuideAzureOpenAIResolver(
	providerAlias string,
	resourceEndpoint string,
	deployments map[string]string,
	apiVersion string,
	routeIdentity string,
) (*guideAzureOpenAIResolver, error) {
	if providerAlias != "azureopenai.v1" && providerAlias != "azureopenai.classic" {
		return nil, fmt.Errorf("unsupported Azure OpenAI alias %q", providerAlias)
	}
	origin, err := url.Parse(strings.TrimSpace(resourceEndpoint))
	if err != nil {
		return nil, errors.New("Azure OpenAI resource endpoint is invalid")
	}
	if origin.Scheme != "https" || origin.Hostname() == "" ||
		origin.User != nil || origin.Port() != "" ||
		origin.RawQuery != "" || origin.Fragment != "" ||
		origin.EscapedPath() != "" && origin.EscapedPath() != "/" {
		return nil, errors.New(
			"Azure OpenAI resource endpoint must be an HTTPS origin without a port, path, query, user info, or fragment",
		)
	}
	routeIdentity = strings.TrimSpace(routeIdentity)
	if routeIdentity == "" {
		return nil, errors.New("Azure OpenAI route identity is required")
	}
	copied := make(map[string]string, len(deployments))
	for semanticModel, deployment := range deployments {
		semanticModel = strings.TrimSpace(semanticModel)
		deployment = strings.TrimSpace(deployment)
		if semanticModel == "" || deployment == "" {
			return nil, errors.New(
				"Azure OpenAI deployment mappings require nonempty semantic models and deployments",
			)
		}
		copied[semanticModel] = deployment
	}
	if len(copied) == 0 {
		return nil, errors.New("Azure OpenAI deployment mappings are required")
	}
	apiVersion = strings.TrimSpace(apiVersion)
	if providerAlias == "azureopenai.classic" && apiVersion == "" {
		return nil, errors.New("Azure OpenAI classic API version is required")
	}
	origin.Path = ""
	origin.RawPath = ""
	return &guideAzureOpenAIResolver{
		providerAlias: providerAlias,
		origin:        origin,
		deployments:   copied,
		apiVersion:    apiVersion,
		routeIdentity: routeIdentity,
	}, nil
}

func (resolver *guideAzureOpenAIResolver) ResolveEndpoint(
	_ context.Context,
	request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	if request.Provider != "azureopenai" || request.ProviderAlias != resolver.providerAlias {
		return ai.ResolvedEndpoint{}, errors.New("Azure OpenAI resolver received the wrong provider")
	}
	if request.Operation != "generate" && request.Operation != "stream" {
		return ai.ResolvedEndpoint{}, fmt.Errorf(
			"unsupported Azure OpenAI operation %q",
			request.Operation,
		)
	}
	deployment, ok := resolver.deployments[request.ResolvedModel]
	if !ok {
		return ai.ResolvedEndpoint{}, fmt.Errorf(
			"no Azure deployment for semantic model %q",
			request.ResolvedModel,
		)
	}

	rawURL := strings.TrimRight(resolver.origin.String(), "/")
	query := make(url.Values)
	switch resolver.providerAlias {
	case "azureopenai.v1":
		rawURL += "/openai/v1/chat/completions"
		if resolver.apiVersion != "" {
			query.Set("api-version", resolver.apiVersion)
		}
	case "azureopenai.classic":
		rawURL += "/openai/deployments/" +
			url.PathEscape(deployment) + "/chat/completions"
		query.Set("api-version", resolver.apiVersion)
	}
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return ai.ResolvedEndpoint{}, errors.New("construct Azure OpenAI endpoint")
	}
	return ai.ResolvedEndpoint{
		URL:             endpoint,
		Query:           query,
		Deployment:      deployment,
		RouteIdentity:   resolver.routeIdentity,
		CredentialScope: "https://cognitiveservices.azure.com/.default",
	}, nil
}

func newGuideAzureOpenAIV1WithAPIKey(
	resourceEndpoint string,
	semanticModel string,
	deployments map[string]string,
	apiKey string,
) (core.AIRequestClient, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("Azure OpenAI API key is required")
	}
	resolver, err := newGuideAzureOpenAIResolver(
		"azureopenai.v1",
		resourceEndpoint,
		deployments,
		"",
		"azure-openai-v1-primary-v1",
	)
	if err != nil {
		return nil, err
	}
	return ai.NewRequestClient(
		ai.WithProviderAlias("azureopenai.v1"),
		ai.WithModel(semanticModel),
		ai.WithEndpointResolver(resolver),
		ai.WithAPIKey(apiKey),
	)
}

func newGuideAzureClassicClient(
	resourceEndpoint string,
	semanticModel string,
	deployments map[string]string,
	apiKey string,
) (core.AIRequestClient, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("Azure OpenAI API key is required")
	}
	resolver, err := newGuideAzureOpenAIResolver(
		"azureopenai.classic",
		resourceEndpoint,
		deployments,
		"2024-10-21",
		"azure-openai-classic-primary-v1",
	)
	if err != nil {
		return nil, err
	}
	return ai.NewRequestClient(
		ai.WithProviderAlias("azureopenai.classic"),
		ai.WithModel(semanticModel),
		ai.WithEndpointResolver(resolver),
		ai.WithAPIKey(apiKey),
	)
}

var (
	guideGoogleProjectIDPattern = regexp.MustCompile(
		`^(?:[a-z][a-z0-9-]{4,28}[a-z0-9]|[0-9]{6,30})$`,
	)
	guideGoogleLocationPattern = regexp.MustCompile(
		`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`,
	)
	guideGooglePublisherModelPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]+$`)
)

func guideGoogleOpenAIBaseURL(projectID, location string) (*url.URL, error) {
	projectID = strings.TrimSpace(projectID)
	location = strings.TrimSpace(location)
	if !guideGoogleProjectIDPattern.MatchString(projectID) {
		return nil, errors.New("Google project ID is invalid")
	}
	if !guideGoogleLocationPattern.MatchString(location) {
		return nil, errors.New("Google location is invalid")
	}
	host := "aiplatform.googleapis.com"
	if location != "global" {
		host = location + "-aiplatform.googleapis.com"
	}
	return &url.URL{
		Scheme: "https",
		Host:   host,
		Path: fmt.Sprintf(
			"/v1/projects/%s/locations/%s/endpoints/openapi",
			url.PathEscape(projectID),
			url.PathEscape(location),
		),
	}, nil
}

func newGuideGoogleHostedOpenAIClient(
	projectID string,
	location string,
	model string,
	tokens guideAccessTokenSource,
) (core.AIRequestClient, error) {
	model = strings.TrimSpace(model)
	if model == "" || tokens == nil {
		return nil, errors.New("Google model and token source are required")
	}
	baseURL, err := guideGoogleOpenAIBaseURL(projectID, location)
	if err != nil {
		return nil, err
	}
	return ai.NewRequestClient(
		ai.WithProvider("openai"),
		ai.WithBaseURL(baseURL.String()),
		ai.WithModel(model),
		ai.WithAuthHeader("Authorization", guideBearerHeader(tokens)),
	)
}

func guideGooglePartnerModelHost(location string) (string, error) {
	switch location {
	case "global":
		return "aiplatform.googleapis.com", nil
	case "us", "eu":
		return "aiplatform." + location + ".rep.googleapis.com", nil
	default:
		if !guideGoogleLocationPattern.MatchString(location) {
			return "", errors.New("Google partner-model location is invalid")
		}
		return location + "-aiplatform.googleapis.com", nil
	}
}

func guideGoogleClaudeEndpoint(
	projectID string,
	location string,
	publisherModel string,
	operation string,
) (*url.URL, error) {
	if !guideGoogleProjectIDPattern.MatchString(projectID) {
		return nil, errors.New("Google project ID is invalid")
	}
	if !guideGooglePublisherModelPattern.MatchString(publisherModel) {
		return nil, errors.New("Google publisher model is invalid")
	}
	host, err := guideGooglePartnerModelHost(location)
	if err != nil {
		return nil, err
	}
	method := "rawPredict"
	if operation == "stream" {
		method = "streamRawPredict"
	} else if operation != "generate" {
		return nil, fmt.Errorf("unsupported Anthropic operation %q", operation)
	}
	return &url.URL{
		Scheme: "https",
		Host:   host,
		Path: fmt.Sprintf(
			"/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
			projectID,
			location,
			publisherModel,
			method,
		),
	}, nil
}

type guideVertexClaudeResolver struct {
	projectID       string
	location        string
	publisherModels map[string]string
	routeIdentity   string
}

func newGuideVertexClaudeResolver(
	projectID string,
	location string,
	publisherModels map[string]string,
	routeIdentity string,
) (*guideVertexClaudeResolver, error) {
	projectID = strings.TrimSpace(projectID)
	location = strings.TrimSpace(location)
	routeIdentity = strings.TrimSpace(routeIdentity)
	if !guideGoogleProjectIDPattern.MatchString(projectID) {
		return nil, errors.New("Google project ID is invalid")
	}
	if _, err := guideGooglePartnerModelHost(location); err != nil {
		return nil, err
	}
	if routeIdentity == "" {
		return nil, errors.New("Vertex Claude route identity is required")
	}
	copied := make(map[string]string, len(publisherModels))
	for semanticModel, publisherModel := range publisherModels {
		semanticModel = strings.TrimSpace(semanticModel)
		publisherModel = strings.TrimSpace(publisherModel)
		if semanticModel == "" || !guideGooglePublisherModelPattern.MatchString(publisherModel) {
			return nil, errors.New(
				"Vertex Claude mappings require a semantic model and a valid publisher model",
			)
		}
		copied[semanticModel] = publisherModel
	}
	if len(copied) == 0 {
		return nil, errors.New("Vertex Claude publisher-model mappings are required")
	}
	return &guideVertexClaudeResolver{
		projectID:       projectID,
		location:        location,
		publisherModels: copied,
		routeIdentity:   routeIdentity,
	}, nil
}

func (resolver *guideVertexClaudeResolver) ResolveEndpoint(
	_ context.Context,
	request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	if request.Provider != "anthropic" || request.ProviderAlias != "anthropic.vertex" {
		return ai.ResolvedEndpoint{}, errors.New("Vertex Claude resolver received the wrong provider")
	}
	publisherModel, ok := resolver.publisherModels[request.ResolvedModel]
	if !ok {
		return ai.ResolvedEndpoint{}, fmt.Errorf(
			"no Vertex publisher model for semantic model %q",
			request.ResolvedModel,
		)
	}
	endpoint, err := guideGoogleClaudeEndpoint(
		resolver.projectID,
		resolver.location,
		publisherModel,
		request.Operation,
	)
	if err != nil {
		return ai.ResolvedEndpoint{}, err
	}
	return ai.ResolvedEndpoint{
		URL:             endpoint,
		Deployment:      publisherModel,
		RouteIdentity:   resolver.routeIdentity,
		CredentialScope: "https://www.googleapis.com/auth/cloud-platform",
	}, nil
}

func newGuideVertexClaudeClient(
	projectID string,
	location string,
	semanticModel string,
	publisherModels map[string]string,
	tokens guideAccessTokenSource,
) (core.AIRequestClient, error) {
	if strings.TrimSpace(semanticModel) == "" || tokens == nil {
		return nil, errors.New("Vertex Claude semantic model and token source are required")
	}
	resolver, err := newGuideVertexClaudeResolver(
		projectID,
		location,
		publisherModels,
		"vertex-claude-primary-v1",
	)
	if err != nil {
		return nil, err
	}
	return ai.NewRequestClient(
		ai.WithProviderAlias("anthropic.vertex"),
		ai.WithModel(semanticModel),
		ai.WithEndpointResolver(resolver),
		ai.WithAuthHeader("Authorization", guideBearerHeader(tokens)),
	)
}

func TestHostedProviderGuideAzureRoutesAndConstruction(t *testing.T) {
	deployments := map[string]string{"gpt-4.1": "smart"}
	for _, test := range []struct {
		alias      string
		apiVersion string
		wantURL    string
		wantQuery  string
	}{
		{
			alias:   "azureopenai.v1",
			wantURL: "https://acme.openai.azure.com/openai/v1/chat/completions",
		},
		{
			alias:      "azureopenai.classic",
			apiVersion: "2024-10-21",
			wantURL:    "https://acme.openai.azure.com/openai/deployments/smart/chat/completions",
			wantQuery:  "2024-10-21",
		},
	} {
		t.Run(test.alias, func(t *testing.T) {
			resolver, err := newGuideAzureOpenAIResolver(
				test.alias,
				"https://acme.openai.azure.com",
				deployments,
				test.apiVersion,
				"azure-primary-v1",
			)
			if err != nil {
				t.Fatalf("construct resolver: %v", err)
			}
			route, err := resolver.ResolveEndpoint(t.Context(), ai.EndpointRequest{
				Provider:      "azureopenai",
				ProviderAlias: test.alias,
				ResolvedModel: "gpt-4.1",
				Operation:     "generate",
			})
			if err != nil {
				t.Fatalf("resolve route: %v", err)
			}
			if route.URL.String() != test.wantURL {
				t.Fatalf("route URL = %q, want %q", route.URL, test.wantURL)
			}
			if route.Query.Get("api-version") != test.wantQuery {
				t.Fatalf("api-version = %q, want %q", route.Query.Get("api-version"), test.wantQuery)
			}
			if route.Deployment != "smart" {
				t.Fatalf("deployment = %q, want smart", route.Deployment)
			}

			var client core.AIRequestClient
			if test.alias == "azureopenai.v1" {
				client, err = newGuideAzureOpenAIV1WithAPIKey(
					"https://acme.openai.azure.com",
					"gpt-4.1",
					deployments,
					"test-key",
				)
			} else {
				client, err = newGuideAzureClassicClient(
					"https://acme.openai.azure.com",
					"gpt-4.1",
					deployments,
					"test-key",
				)
			}
			if err != nil {
				t.Fatalf("construct public client: %v", err)
			}
			if client == nil {
				t.Fatal("Azure OpenAI client is nil")
			}
		})
	}

	resolver, err := newGuideAzureOpenAIResolver(
		"azureopenai.v1",
		"https://acme.openai.azure.com",
		map[string]string{"smart": "deployment"},
		"",
		"azure-primary-v1",
	)
	if err != nil {
		t.Fatalf("construct alias-key resolver: %v", err)
	}
	_, err = resolver.ResolveEndpoint(t.Context(), ai.EndpointRequest{
		Provider:      "azureopenai",
		ProviderAlias: "azureopenai.v1",
		ResolvedModel: "gpt-4.1",
		Operation:     "generate",
	})
	if err == nil || !strings.Contains(err.Error(), "no Azure deployment") {
		t.Fatalf("post-alias map contract error = %v", err)
	}
}

func TestHostedProviderGuideGoogleRoutesAndConstruction(t *testing.T) {
	for _, test := range []struct {
		location string
		wantHost string
	}{
		{location: "global", wantHost: "aiplatform.googleapis.com"},
		{location: "us-central1", wantHost: "us-central1-aiplatform.googleapis.com"},
	} {
		endpoint, err := guideGoogleOpenAIBaseURL("acme-prod", test.location)
		if err != nil {
			t.Fatalf("construct %s base URL: %v", test.location, err)
		}
		if endpoint.Host != test.wantHost {
			t.Errorf("%s host = %q, want %q", test.location, endpoint.Host, test.wantHost)
		}
	}

	client, err := newGuideGoogleHostedOpenAIClient(
		"acme-prod",
		"global",
		"google/documented-model-id",
		guideStaticTokenSource("token"),
	)
	if err != nil {
		t.Fatalf("construct Google OpenAI client: %v", err)
	}
	if client == nil {
		t.Fatal("Google OpenAI client is nil")
	}
}

func TestHostedProviderGuideVertexClaudeRoutesAndConstruction(t *testing.T) {
	semanticModel := "claude-sonnet-4-5-20250929"
	publisherModel := "claude-sonnet-4-5@20250929"
	for _, test := range []struct {
		location  string
		operation string
		wantHost  string
		wantTail  string
	}{
		{
			location:  "global",
			operation: "generate",
			wantHost:  "aiplatform.googleapis.com",
			wantTail:  ":rawPredict",
		},
		{
			location:  "us",
			operation: "stream",
			wantHost:  "aiplatform.us.rep.googleapis.com",
			wantTail:  ":streamRawPredict",
		},
		{
			location:  "eu",
			operation: "generate",
			wantHost:  "aiplatform.eu.rep.googleapis.com",
			wantTail:  ":rawPredict",
		},
		{
			location:  "us-central1",
			operation: "generate",
			wantHost:  "us-central1-aiplatform.googleapis.com",
			wantTail:  ":rawPredict",
		},
	} {
		t.Run(test.location+"-"+test.operation, func(t *testing.T) {
			resolver, err := newGuideVertexClaudeResolver(
				"acme-prod",
				test.location,
				map[string]string{semanticModel: publisherModel},
				"vertex-primary-v1",
			)
			if err != nil {
				t.Fatalf("construct resolver: %v", err)
			}
			route, err := resolver.ResolveEndpoint(t.Context(), ai.EndpointRequest{
				Provider:      "anthropic",
				ProviderAlias: "anthropic.vertex",
				ResolvedModel: semanticModel,
				Operation:     test.operation,
			})
			if err != nil {
				t.Fatalf("resolve route: %v", err)
			}
			if route.URL.Host != test.wantHost {
				t.Errorf("host = %q, want %q", route.URL.Host, test.wantHost)
			}
			if !strings.HasSuffix(route.URL.Path, test.wantTail) {
				t.Errorf("path = %q, want suffix %q", route.URL.Path, test.wantTail)
			}
			if route.Deployment != publisherModel {
				t.Errorf("deployment = %q, want %q", route.Deployment, publisherModel)
			}
		})
	}

	client, err := newGuideVertexClaudeClient(
		"acme-prod",
		"global",
		semanticModel,
		map[string]string{semanticModel: publisherModel},
		guideStaticTokenSource("token"),
	)
	if err != nil {
		t.Fatalf("construct Vertex Claude client: %v", err)
	}
	if client == nil {
		t.Fatal("Vertex Claude client is nil")
	}
}

var (
	_ ai.EndpointResolver = (*guideAzureOpenAIResolver)(nil)
	_ ai.EndpointResolver = (*guideVertexClaudeResolver)(nil)
)
