package azureopenai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type azureObservationEntry struct {
	component string
	fields    map[string]interface{}
}

type azureObservationLogger struct {
	entries   *[]azureObservationEntry
	component string
}

func newAzureObservationLogger() *azureObservationLogger {
	entries := []azureObservationEntry{}
	return &azureObservationLogger{entries: &entries}
}

func (logger *azureObservationLogger) WithComponent(component string) core.Logger {
	return &azureObservationLogger{entries: logger.entries, component: component}
}

func (logger *azureObservationLogger) record(fields map[string]interface{}) {
	*logger.entries = append(*logger.entries, azureObservationEntry{
		component: logger.component, fields: fields,
	})
}

func (logger *azureObservationLogger) Debug(_ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *azureObservationLogger) Info(_ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *azureObservationLogger) Warn(_ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *azureObservationLogger) Error(_ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *azureObservationLogger) DebugWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *azureObservationLogger) InfoWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *azureObservationLogger) WarnWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *azureObservationLogger) ErrorWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.record(fields)
}

type azureObservationTelemetry struct {
	names []string
	spans []*azureObservationSpan
}

func (tracing *azureObservationTelemetry) StartSpan(
	ctx context.Context,
	name string,
) (context.Context, core.Span) {
	span := &azureObservationSpan{attributes: map[string]interface{}{}}
	tracing.names = append(tracing.names, name)
	tracing.spans = append(tracing.spans, span)
	return ctx, span
}

func (*azureObservationTelemetry) RecordMetric(string, float64, map[string]string) {}

type azureObservationSpan struct {
	attributes map[string]interface{}
	errors     []error
	ended      int
}

func (span *azureObservationSpan) End() { span.ended++ }
func (span *azureObservationSpan) SetAttribute(key string, value interface{}) {
	span.attributes[key] = value
}
func (span *azureObservationSpan) RecordError(err error) {
	span.errors = append(span.errors, err)
}

type nilReturningAzureTelemetry struct{}

func (nilReturningAzureTelemetry) StartSpan(context.Context, string) (context.Context, core.Span) {
	return nil, nil
}
func (nilReturningAzureTelemetry) RecordMetric(string, float64, map[string]string) {}

func TestAzureFailureObservationsAreSanitized(t *testing.T) {
	const (
		requestID        = "azure-observation-request"
		promptSecret     = "azure-prompt-secret"
		endpointSecret   = "azure-endpoint-secret"
		deploymentSecret = "azure-deployment-secret"
		scopeSecret      = "azure-scope-secret"
		staticKeySecret  = "azure-static-key-secret"
		resolverSecret   = "azure-resolver-error-secret"
		credentialSecret = "azure-credential-error-secret"
		providerSecret   = "azure-provider-body-secret"
		decoderSecret    = "azure-decoder-body-secret"
	)

	tests := []struct {
		name             string
		resolverError    error
		credentialSource ai.CredentialSource
		respond          func(*http.Request) (*http.Response, error)
		wantSpans        []string
	}{
		{
			name: "resolver error", resolverError: errors.New(resolverSecret),
			wantSpans: []string{"ai.generate_response"},
		},
		{
			name:             "credential error",
			credentialSource: &recordingCredentialSource{err: errors.New(credentialSecret)},
			wantSpans:        []string{"ai.generate_response", "ai.http_attempt"},
		},
		{
			name: "provider error",
			respond: func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"` + providerSecret + `"}}`)),
					Request:    request,
				}, nil
			},
			wantSpans: []string{"ai.generate_response", "ai.http_attempt"},
		},
		{
			name: "decoder error",
			respond: func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(decoderSecret)),
					Request:    request,
				}, nil
			},
			wantSpans: []string{"ai.generate_response", "ai.http_attempt"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := newAzureObservationLogger()
			tracing := &azureObservationTelemetry{}
			resolver := &testResolver{resolved: ai.ResolvedEndpoint{
				URL:        mustURL(t, "https://"+endpointSecret+".example/openai/v1/chat/completions"),
				Deployment: deploymentSecret, RouteIdentity: "azure-observation-route-v1",
				CredentialScope: scopeSecret,
			}, err: test.resolverError}
			transportCalls := 0
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				transportCalls++
				if test.respond == nil {
					return successResponse(request, deploymentSecret, "unused"), nil
				}
				return test.respond(request)
			})
			config := &ai.AIConfig{
				ProviderAlias: "azureopenai.v1", APIKey: staticKeySecret,
				Model: "gpt-4.1", MaxRetries: 0, Logger: logger, Telemetry: tracing,
			}
			client := mustClient(t, config, ai.ProviderIntegrationConfig{
				EndpointResolver: resolver, CredentialSource: test.credentialSource,
				HTTPClient: &http.Client{Transport: transport},
			})
			ctx := telemetry.WithBaggage(context.Background(), "request_id", requestID)
			if _, err := client.Generate(ctx, core.NewAIRequest(promptSecret, "observation")); err == nil {
				t.Fatal("Generate returned nil error")
			}
			if fmt.Sprint(tracing.names) != fmt.Sprint(test.wantSpans) {
				t.Fatalf("span names = %#v", tracing.names)
			}
			for index, span := range tracing.spans {
				if span.ended != 1 {
					t.Errorf("span %d ended %d times", index, span.ended)
				}
				if span.attributes["request_id"] != requestID {
					t.Errorf("span %d request_id = %#v", index, span.attributes["request_id"])
				}
			}
			for _, entry := range *logger.entries {
				if entry.component != "framework/ai" {
					t.Errorf("log component = %q", entry.component)
				}
				if entry.fields["operation"] == "" {
					t.Errorf("log has no operation: %#v", entry.fields)
				}
				if entry.fields["operation"] != "ai_provider_init" && entry.fields["request_id"] != requestID {
					t.Errorf("request log has no request_id: %#v", entry.fields)
				}
			}

			var observations strings.Builder
			for _, entry := range *logger.entries {
				fmt.Fprint(&observations, entry.component, entry.fields)
			}
			for _, span := range tracing.spans {
				fmt.Fprint(&observations, span.attributes, span.errors)
			}
			for _, secret := range []string{
				promptSecret, endpointSecret, deploymentSecret, scopeSecret,
				staticKeySecret, resolverSecret, credentialSecret, providerSecret, decoderSecret,
			} {
				if strings.Contains(observations.String(), secret) {
					t.Fatalf("observations leaked %q: %s", secret, observations.String())
				}
			}
			if test.name == "resolver error" || test.name == "credential error" {
				if transportCalls != 0 {
					t.Fatalf("transport calls = %d", transportCalls)
				}
			}
		})
	}
}

func TestAzureTelemetryIsOptionalAndFailOpen(t *testing.T) {
	for _, test := range []struct {
		name      string
		telemetry core.Telemetry
	}{
		{name: "nil telemetry"},
		{name: "nil-returning telemetry", telemetry: nilReturningAzureTelemetry{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &testResolver{resolved: ai.ResolvedEndpoint{
				URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
				Deployment: "prod-chat", RouteIdentity: "nil-telemetry-route-v1",
			}}
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return successResponse(request, "prod-chat", "ok"), nil
			})
			client := mustClient(t, &ai.AIConfig{
				ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "gpt-4.1",
				MaxRetries: 0, Telemetry: test.telemetry,
			}, ai.ProviderIntegrationConfig{
				EndpointResolver: resolver, HTTPClient: &http.Client{Transport: transport},
			})
			result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "nil-safe"))
			if err != nil || result == nil || result.Response == nil || result.Response.Content != "ok" {
				t.Fatalf("Generate result=%#v error=%v", result, err)
			}
		})
	}
}
