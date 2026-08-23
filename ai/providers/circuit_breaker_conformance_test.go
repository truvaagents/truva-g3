package providers_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers/anthropic"
	"github.com/truvaagents/truva-g3/ai/providers/azureopenai"
	"github.com/truvaagents/truva-g3/ai/providers/gemini"
	"github.com/truvaagents/truva-g3/ai/providers/openai"
	"github.com/truvaagents/truva-g3/core"
)

type alwaysOpenCircuitBreaker struct{}

func (alwaysOpenCircuitBreaker) Execute(context.Context, func() error) error {
	return core.ErrCircuitBreakerOpen
}

func (alwaysOpenCircuitBreaker) ExecuteWithTimeout(
	context.Context,
	time.Duration,
	func() error,
) error {
	return core.ErrCircuitBreakerOpen
}

func (alwaysOpenCircuitBreaker) GetState() string                   { return "open" }
func (alwaysOpenCircuitBreaker) GetMetrics() map[string]interface{} { return nil }
func (alwaysOpenCircuitBreaker) Reset()                             {}
func (alwaysOpenCircuitBreaker) CanExecute() bool                   { return false }

func TestBuiltInHTTPProvidersAcceptDirectCircuitBreakerDecorator(t *testing.T) {
	endpoint, err := url.Parse("https://azure.example/openai/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	azure, err := (&azureopenai.Factory{}).CreateRequestClient(
		&ai.AIConfig{ProviderAlias: "azureopenai.v1", APIKey: "test-key", Model: "gpt-4.1"},
		ai.ProviderIntegrationConfig{
			EndpointResolver: observationEndpointResolver{resolved: ai.ResolvedEndpoint{
				URL: endpoint, RouteIdentity: "test-route", CredentialScope: "test-scope",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		client core.AIClient
	}{
		{name: "openai-compatible", client: openai.NewClient("test-key", "https://openai.example/v1", "openai", nil)},
		{name: "anthropic", client: anthropic.NewClient("test-key", "https://anthropic.example/v1", nil)},
		{name: "gemini", client: gemini.NewClient("test-key", "https://gemini.example/v1beta", nil)},
		{name: "azure-openai", client: azure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			protected, err := ai.NewCircuitBreakerClient(test.client, alwaysOpenCircuitBreaker{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := protected.GenerateResponse(t.Context(), "must-not-reach-network", nil); !errors.Is(err, core.ErrCircuitBreakerOpen) {
				t.Fatalf("GenerateResponse error = %v, want circuit open", err)
			}
		})
	}
}
