// Package azureopenai implements the request-aware Azure OpenAI v1 and
// classic Chat Completions surfaces.
package azureopenai

import (
	"errors"
	"fmt"
	"strings"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

const providerName = "azureopenai"

// Factory constructs request-aware Azure OpenAI clients.
type Factory struct{}

var _ ai.ProviderFactory = (*Factory)(nil)
var _ ai.ValidatedProviderFactory = (*Factory)(nil)
var _ ai.RequestProviderFactory = (*Factory)(nil)

func init() { ai.MustRegister(&Factory{}) }

// Name returns the registered base provider name.
func (*Factory) Name() string { return providerName }

// Description returns the provider description.
func (*Factory) Description() string { return "Azure OpenAI" }

// DetectEnvironment deliberately disables implicit Azure selection.
func (*Factory) DetectEnvironment() (int, bool) { return 0, false }

// Create is an unsupported direct-call-only path and always panics.
func (*Factory) Create(*ai.AIConfig) core.AIClient {
	panic("create Azure OpenAI client: legacy construction is unsupported; use ai.NewRequestClient with an endpoint resolver")
}

// CreateValidated rejects legacy construction because a resolver is required.
func (*Factory) CreateValidated(*ai.AIConfig) (core.AIClient, error) {
	return nil, fmt.Errorf(
		"%w: Azure OpenAI requires ai.NewRequestClient with an endpoint resolver",
		core.ErrAIRequestFeatureUnsupported,
	)
}

// CreateRequestClient constructs the only supported Azure OpenAI client path.
func (*Factory) CreateRequestClient(
	config *ai.AIConfig,
	integration ai.ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
	if config == nil {
		return nil, errors.New("azure OpenAI config is nil")
	}
	selected, err := parseSurface(config.ProviderAlias)
	if err != nil {
		return nil, err
	}
	if integration.EndpointResolver == nil {
		return nil, errors.New("azure OpenAI endpoint resolver is required")
	}
	if integration.CredentialSource == nil && strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("azure OpenAI credential is required")
	}
	if strings.TrimSpace(config.BaseURL) != "" {
		return nil, errors.New("azure OpenAI BaseURL is not accepted; return the complete URL from EndpointResolver")
	}
	return newClient(config, integration, selected)
}
