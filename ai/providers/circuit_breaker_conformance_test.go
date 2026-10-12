package providers_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers/anthropic"
	"github.com/truvaagents/truva-g3/ai/providers/azureopenai"
	"github.com/truvaagents/truva-g3/ai/providers/gemini"
	"github.com/truvaagents/truva-g3/ai/providers/openai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
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

type policyTestCircuitBreaker struct{ open bool }

func (b *policyTestCircuitBreaker) Execute(_ context.Context, fn func() error) error {
	if b.open {
		return core.ErrCircuitBreakerOpen
	}
	err := fn()
	if ai.ShouldCountAICircuitBreakerFailure(err) {
		b.open = true
	}
	return err
}
func (b *policyTestCircuitBreaker) ExecuteWithTimeout(ctx context.Context, _ time.Duration, fn func() error) error {
	return b.Execute(ctx, fn)
}
func (b *policyTestCircuitBreaker) GetState() string {
	if b.open {
		return "open"
	}
	return "closed"
}
func (b *policyTestCircuitBreaker) GetMetrics() map[string]interface{} { return nil }
func (b *policyTestCircuitBreaker) Reset()                             { b.open = false }
func (b *policyTestCircuitBreaker) CanExecute() bool                   { return !b.open }

type policyTestTransport func(*http.Request) (*http.Response, error)

func (f policyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLocalPolicyRejectionDoesNotPoisonProviderHealth(t *testing.T) {
	for _, rejection := range []string{"capacity", "patched-capacity", "strict-sampling"} {
		for _, stream := range []bool{false, true} {
			t.Run(rejection+map[bool]string{false: "/buffered", true: "/stream"}[stream], func(t *testing.T) {
				calls := 0
				transport := policyTestTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					_ = r.Body.Close()
					return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`)), Request: r}, nil
				})
				client, err := ai.NewRequestClient(ai.WithProvider("openai"), ai.WithProviderAlias("openai.openrouter"), ai.WithAPIKey("synthetic"), ai.WithMaxRetries(0), ai.WithCompatibilityMode(requestpolicy.CompatibilityStrict), ai.WithHTTPClient(&http.Client{Transport: transport}))
				if err != nil {
					t.Fatal(err)
				}
				breaker := &policyTestCircuitBreaker{}
				protected, err := ai.NewCircuitBreakerClient(client, breaker)
				if err != nil {
					t.Fatal(err)
				}
				request := core.NewAIRequest("synthetic", "policy-regression")
				request.Generation.Model = "openai/gpt-6.1-sol"
				switch rejection {
				case "capacity":
					request.Generation.MaxTokens = core.SetAIParameter(128001)
				case "patched-capacity":
					request.Patches = []core.AIProviderPatch{{Name: "capacity", Version: "1", Selector: core.AIProviderSelector{ProviderAlias: "openai.openrouter"}, Set: map[string]interface{}{"/max_completion_tokens": 128001}}}
				case "strict-sampling":
					request.Generation.Model = "openai/gpt-6-luna"
					request.Generation.Temperature = core.SetAIParameter[float32](0.01)
				}
				invoke := func(r *core.AIRequest) error {
					if stream {
						_, err := core.StreamAI(t.Context(), protected, r, func(core.StreamChunk) error { return nil })
						return err
					}
					_, err := core.GenerateAI(t.Context(), protected, r)
					return err
				}
				if err := invoke(request); !core.IsConfigurationError(err) || breaker.open || calls != 0 {
					t.Fatalf("local rejection polluted provider health: calls=%d open=%v error=%v", calls, breaker.open, err)
				}
				valid := core.NewAIRequest("synthetic", "real-provider-failure")
				valid.Generation.Model = "openai/gpt-6.1-sol"
				if err := invoke(valid); err == nil || core.IsConfigurationError(err) || !breaker.open || calls != 1 {
					t.Fatalf("provider failure stopped counting: calls=%d open=%v error=%v", calls, breaker.open, err)
				}
				if err := invoke(valid); !errors.Is(err, core.ErrCircuitBreakerOpen) || calls != 1 {
					t.Fatalf("open circuit failed to guard transport: %v", err)
				}
			})
		}
	}
}

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
