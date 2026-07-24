package ai

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

type phase4CredentialSource struct{}

func (*phase4CredentialSource) Credential(context.Context, CredentialRequest) (HeaderCredential, error) {
	return NewHeaderCredential("Authorization", "Bearer token"), nil
}

type phase4EndpointResolver struct{}

func (*phase4EndpointResolver) ResolveEndpoint(context.Context, EndpointRequest) (ResolvedEndpoint, error) {
	return ResolvedEndpoint{
		URL:           &url.URL{Scheme: "https", Host: "gateway.example", Path: "/messages"},
		RouteIdentity: "tenant-primary",
	}, nil
}

func TestNewRequestClient_Phase4IntegrationOptions(t *testing.T) {
	factory := &phase3RequestFactory{
		name:   "phase4-request-aware",
		client: &phase3RequestClient{},
	}
	installPhase3Factory(t, factory)

	source := &phase4CredentialSource{}
	resolver := &phase4EndpointResolver{}
	httpClient := &http.Client{}
	client, err := NewRequestClient(
		WithProvider(factory.name),
		WithCredentialSource(source),
		WithEndpointResolver(resolver),
		WithHTTPClient(httpClient),
	)
	if err != nil {
		t.Fatalf("NewRequestClient returned error: %v", err)
	}
	requireFactoryInstrumentedClient(t, client, factory.client)
	if factory.integration.CredentialSource != source {
		t.Fatal("credential source was not passed to request factory")
	}
	if factory.integration.EndpointResolver != resolver {
		t.Fatal("endpoint resolver was not passed to request factory")
	}
	if factory.integration.HTTPClient != httpClient {
		t.Fatal("HTTP client was not passed to request factory")
	}
}

func TestNewRequestClient_Phase4TypedNilValidation(t *testing.T) {
	var typedNilSource *phase4CredentialSource
	var typedNilResolver *phase4EndpointResolver
	tests := []struct {
		name   string
		option ClientOption
		want   string
	}{
		{name: "credential source", option: WithCredentialSource(nil), want: "credential source is nil"},
		{name: "endpoint resolver", option: WithEndpointResolver(nil), want: "endpoint resolver is nil"},
		{name: "HTTP client", option: WithHTTPClient(nil), want: "HTTP client is nil"},
		{name: "typed nil credential source", option: WithCredentialSource(typedNilSource), want: "credential source is nil"},
		{name: "typed nil endpoint resolver", option: WithEndpointResolver(typedNilResolver), want: "endpoint resolver is nil"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			factory := &phase3RequestFactory{name: "phase4-validation", client: &phase3RequestClient{}}
			installPhase3Factory(t, factory)
			_, err := NewRequestClient(WithProvider(factory.name), test.option)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewRequestClient error = %v, want %q", err, test.want)
			}
			if factory.requestCalls != 0 {
				t.Fatalf("factory invoked before common validation: %d", factory.requestCalls)
			}
		})
	}
}

func TestWithAuthHeader_ValidationAndAdapter(t *testing.T) {
	t.Run("invalid configuration", func(t *testing.T) {
		factory := &phase3RequestFactory{name: "phase4-auth-validation", client: &phase3RequestClient{}}
		installPhase3Factory(t, factory)

		if _, err := NewRequestClient(WithProvider(factory.name), WithAuthHeader("bad header", func(context.Context) (string, error) {
			return "value", nil
		})); err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("invalid header error = %v", err)
		}
		if _, err := NewRequestClient(WithProvider(factory.name), WithAuthHeader("Authorization", nil)); err == nil || !strings.Contains(err.Error(), "callback is nil") {
			t.Fatalf("nil callback error = %v", err)
		}
	})

	t.Run("adapts callback", func(t *testing.T) {
		factory := &phase3RequestFactory{name: "phase4-auth-adapter", client: &phase3RequestClient{}}
		installPhase3Factory(t, factory)
		callbackErr := errors.New("token service unavailable")
		calls := 0
		_, err := NewRequestClient(WithProvider(factory.name), WithAuthHeader("Authorization", func(context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "Bearer current", nil
			}
			return "", callbackErr
		}))
		if err != nil {
			t.Fatalf("NewRequestClient returned error: %v", err)
		}

		credential, err := factory.integration.CredentialSource.Credential(t.Context(), CredentialRequest{})
		if err != nil {
			t.Fatalf("Credential returned error: %v", err)
		}
		if credential.Name != "Authorization" || credential.Value != "Bearer current" {
			t.Fatalf("credential = %#v", credential)
		}
		if _, err := factory.integration.CredentialSource.Credential(t.Context(), CredentialRequest{}); !errors.Is(err, callbackErr) {
			t.Fatalf("callback error = %v, want wrapped %v", err, callbackErr)
		}
	})

	t.Run("rejects empty callback value", func(t *testing.T) {
		factory := &phase3RequestFactory{name: "phase4-auth-empty", client: &phase3RequestClient{}}
		installPhase3Factory(t, factory)
		_, err := NewRequestClient(WithProvider(factory.name), WithAuthHeader("Authorization", func(context.Context) (string, error) {
			return " \t ", nil
		}))
		if err != nil {
			t.Fatalf("NewRequestClient returned error: %v", err)
		}
		if _, err := factory.integration.CredentialSource.Credential(t.Context(), CredentialRequest{}); err == nil || !strings.Contains(err.Error(), "empty value") {
			t.Fatalf("empty credential error = %v", err)
		}
	})
}

func TestNewRequestClient_LegacyFactoryRejectsPhase4Integration(t *testing.T) {
	tests := []struct {
		name   string
		option ClientOption
	}{
		{name: "credential", option: WithCredentialSource(&phase4CredentialSource{})},
		{name: "endpoint", option: WithEndpointResolver(&phase4EndpointResolver{})},
		{name: "HTTP client", option: WithHTTPClient(&http.Client{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			factory := &phase3LegacyFactory{name: "phase4-legacy", client: &phase3RequestClient{}}
			installPhase3Factory(t, factory)
			_, err := NewRequestClient(WithProvider(factory.name), test.option)
			if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
				t.Fatalf("NewRequestClient error = %v, want unsupported feature", err)
			}
			if factory.calls != 0 {
				t.Fatalf("legacy factory invoked before refusing integration: %d", factory.calls)
			}
		})
	}
}
