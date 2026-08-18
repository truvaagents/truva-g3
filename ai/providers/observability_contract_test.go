package providers_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers/anthropic"
	"github.com/truvaagents/truva-g3/ai/providers/azureopenai"
	"github.com/truvaagents/truva-g3/ai/providers/gemini"
	"github.com/truvaagents/truva-g3/ai/providers/openai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type observationRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip observationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type observationLogEntry struct {
	component  string
	fields     map[string]interface{}
	contextual bool
	trace      telemetry.TraceContext
}

type observationLogStore struct {
	entries []observationLogEntry
}

type observationLogger struct {
	store     *observationLogStore
	component string
}

func newObservationLogger() *observationLogger {
	return &observationLogger{store: &observationLogStore{}}
}

func (logger *observationLogger) WithComponent(component string) core.Logger {
	return &observationLogger{store: logger.store, component: component}
}

func (logger *observationLogger) record(ctx context.Context, contextual bool, fields map[string]interface{}) {
	logger.store.entries = append(logger.store.entries, observationLogEntry{
		component:  logger.component,
		fields:     maps.Clone(fields),
		contextual: contextual,
		trace:      telemetry.GetTraceContext(ctx),
	})
}

func (logger *observationLogger) Debug(_ string, fields map[string]interface{}) {
	logger.record(nil, false, fields)
}
func (logger *observationLogger) Info(_ string, fields map[string]interface{}) {
	logger.record(nil, false, fields)
}
func (logger *observationLogger) Warn(_ string, fields map[string]interface{}) {
	logger.record(nil, false, fields)
}
func (logger *observationLogger) Error(_ string, fields map[string]interface{}) {
	logger.record(nil, false, fields)
}
func (logger *observationLogger) DebugWithContext(ctx context.Context, _ string, fields map[string]interface{}) {
	logger.record(ctx, true, fields)
}
func (logger *observationLogger) InfoWithContext(ctx context.Context, _ string, fields map[string]interface{}) {
	logger.record(ctx, true, fields)
}
func (logger *observationLogger) WarnWithContext(ctx context.Context, _ string, fields map[string]interface{}) {
	logger.record(ctx, true, fields)
}
func (logger *observationLogger) ErrorWithContext(ctx context.Context, _ string, fields map[string]interface{}) {
	logger.record(ctx, true, fields)
}

type observationParentKey struct{}

type observationMetric struct {
	name   string
	value  float64
	labels map[string]string
}

type observationTelemetry struct {
	names   []string
	spans   []*observationSpan
	metrics []observationMetric
}

func (tracing *observationTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	parent, ok := ctx.Value(observationParentKey{}).(int)
	if !ok {
		parent = -1
	}
	span := &observationSpan{name: name, parent: parent, attributes: map[string]interface{}{}}
	tracing.names = append(tracing.names, name)
	tracing.spans = append(tracing.spans, span)
	return context.WithValue(ctx, observationParentKey{}, len(tracing.spans)-1), span
}

func (tracing *observationTelemetry) RecordMetric(name string, value float64, labels map[string]string) {
	tracing.metrics = append(tracing.metrics, observationMetric{name: name, value: value, labels: maps.Clone(labels)})
}

type observationSpan struct {
	name       string
	parent     int
	attributes map[string]interface{}
	errors     []error
	ended      int
}

func (span *observationSpan) End() { span.ended++ }
func (span *observationSpan) SetAttribute(key string, value interface{}) {
	span.attributes[key] = value
}
func (span *observationSpan) RecordError(err error) { span.errors = append(span.errors, err) }

func observationResponse(request *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

type observationEndpointResolver struct {
	resolved ai.ResolvedEndpoint
}

type observationCredentialSource struct {
	credential ai.HeaderCredential
}

func (source observationCredentialSource) Credential(
	_ context.Context,
	_ ai.CredentialRequest,
) (ai.HeaderCredential, error) {
	return source.credential, nil
}

func (resolver observationEndpointResolver) ResolveEndpoint(
	_ context.Context,
	_ ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	return resolver.resolved, nil
}

func invokeAzureObservation(
	ctx context.Context,
	logger core.Logger,
	tracing core.Telemetry,
	alias string,
	endpoint *url.URL,
	query url.Values,
	semanticModel string,
	deployment string,
	prompt string,
	responseContent string,
) (*core.AIResponse, error) {
	client, err := (&azureopenai.Factory{}).CreateRequestClient(&ai.AIConfig{
		ProviderAlias: alias, APIKey: "observation-credential-secret", Model: semanticModel,
		MaxTokens: 32, MaxRetries: 0, Logger: logger, Telemetry: tracing,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: observationEndpointResolver{resolved: ai.ResolvedEndpoint{
			URL: endpoint, Query: query, Deployment: deployment,
			RouteIdentity: "observation-azure-route-v1", CredentialScope: "observation-credential-scope-secret",
		}},
		HTTPClient: &http.Client{Transport: observationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return observationResponse(request, `{"id":"chatcmpl-test","model":"`+deployment+`","choices":[{"message":{"role":"assistant","content":"`+responseContent+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`), nil
		})},
	})
	if err != nil {
		return nil, err
	}
	// Initialization entries are deliberately not request-scoped. This test
	// evaluates only request-path observations below.
	if recorder, ok := logger.(*observationLogger); ok {
		recorder.store.entries = nil
	}
	legacy, ok := client.(core.AIClient)
	if !ok {
		return nil, fmt.Errorf("Azure request client %T does not implement core.AIClient", client)
	}
	return legacy.GenerateResponse(ctx, prompt, &core.AIOptions{Model: semanticModel, MaxTokens: 32})
}

func invokeVertexObservation(
	ctx context.Context,
	logger core.Logger,
	tracing core.Telemetry,
	endpoint *url.URL,
	semanticModel string,
	deployment string,
	prompt string,
	responseContent string,
) (*core.AIResponse, error) {
	client, err := (&anthropic.Factory{}).CreateRequestClient(&ai.AIConfig{
		ProviderAlias: "anthropic.vertex", Model: semanticModel,
		MaxTokens: 32, MaxRetries: 0, Logger: logger, Telemetry: tracing,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: observationEndpointResolver{resolved: ai.ResolvedEndpoint{
			URL: endpoint, Deployment: deployment,
			RouteIdentity: "observation-vertex-route-v1", CredentialScope: "observation-credential-scope-secret",
		}},
		CredentialSource: observationCredentialSource{credential: ai.NewHeaderCredential(
			"Authorization", "Bearer observation-credential-secret",
		)},
		HTTPClient: &http.Client{Transport: observationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return observationResponse(request, `{"id":"msg-test","type":"message","role":"assistant","model":"`+deployment+`","content":[{"type":"text","text":"`+responseContent+`"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`), nil
		})},
	})
	if err != nil {
		return nil, err
	}
	if recorder, ok := logger.(*observationLogger); ok {
		recorder.store.entries = nil
	}
	legacy, ok := client.(core.AIClient)
	if !ok {
		return nil, fmt.Errorf("Vertex request client %T does not implement core.AIClient", client)
	}
	return legacy.GenerateResponse(ctx, prompt, &core.AIOptions{Model: semanticModel, MaxTokens: 32})
}

func invokeRegisteredGeminiObservation(
	ctx context.Context,
	logger core.Logger,
	tracing core.Telemetry,
	prompt string,
	responseContent string,
	stream bool,
) (*core.AIResponse, error) {
	client, err := ai.NewRequestClient(
		ai.WithProvider("gemini"),
		ai.WithAPIKey("observation-credential-secret"),
		ai.WithBaseURL("https://observation-endpoint-secret.example/v1beta"),
		ai.WithModel("gemini-3.7-flash"),
		ai.WithMaxTokens(32),
		ai.WithMaxRetries(0),
		ai.WithLogger(logger),
		ai.WithTelemetry(tracing),
		ai.WithHTTPClient(&http.Client{Transport: observationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if stream {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body: io.NopCloser(strings.NewReader(
						`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"` + responseContent + `"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}` + "\n\n",
					)),
					Request: request,
				}, nil
			}
			return observationResponse(request, `{"candidates":[{"content":{"role":"model","parts":[{"text":"`+responseContent+`"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5},"modelVersion":"observation-wire-model-secret"}`), nil
		})}),
	)
	if err != nil {
		return nil, err
	}
	if recorder, ok := logger.(*observationLogger); ok {
		recorder.store.entries = nil
	}
	request := core.NewAIRequest(prompt, "observability-contract")
	request.Generation.Model = "gemini-3.7-flash"
	request.Generation.MaxTokens = core.SetAIParameter(32)
	if !stream {
		result, err := client.Generate(ctx, request)
		if result != nil {
			return result.Response, err
		}
		return nil, err
	}
	streaming, ok := client.(core.StreamingAIRequestClient)
	if !ok {
		return nil, fmt.Errorf("Gemini request client %T does not stream", client)
	}
	result, err := streaming.Stream(ctx, request, func(core.StreamChunk) error { return nil })
	if result != nil {
		return result.Response, err
	}
	return nil, err
}

func TestBuiltInProviderObservationContract(t *testing.T) {
	const (
		requestID       = "observation-request-id"
		promptSecret    = "observation-prompt-secret"
		responseSecret  = "observation-response-secret"
		endpointSecret  = "observation-endpoint-secret"
		credential      = "observation-credential-secret"
		credentialScope = "observation-credential-scope-secret"
		wireModel       = "observation-wire-model-secret"
		traceIDHex      = "0123456789abcdef0123456789abcdef"
		spanIDHex       = "0123456789abcdef"
	)

	tests := []struct {
		name          string
		semanticModel string
		stream        bool
		wrapped       bool
		invoke        func(context.Context, core.Logger, core.Telemetry) (*core.AIResponse, error)
	}{
		{
			name:          "openai",
			semanticModel: "gpt-4.1",
			invoke: func(ctx context.Context, logger core.Logger, tracing core.Telemetry) (*core.AIResponse, error) {
				client := openai.NewClient(credential, "https://"+endpointSecret+".example/v1", "openai", logger)
				client.SetLogger(logger)
				client.SetTelemetry(tracing)
				client.MaxRetries = 0
				client.HTTPClient = &http.Client{Transport: observationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					return observationResponse(request, `{"id":"chatcmpl-test","model":"`+wireModel+`","choices":[{"message":{"role":"assistant","content":"`+responseSecret+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`), nil
				})}
				return client.GenerateResponse(ctx, promptSecret, &core.AIOptions{Model: "gpt-4.1", MaxTokens: 32})
			},
		},
		{
			name:          "anthropic",
			semanticModel: "claude-sonnet-4-5-20250929",
			invoke: func(ctx context.Context, logger core.Logger, tracing core.Telemetry) (*core.AIResponse, error) {
				client := anthropic.NewClient(credential, "https://"+endpointSecret+".example/v1", logger)
				client.SetLogger(logger)
				client.SetTelemetry(tracing)
				client.MaxRetries = 0
				client.HTTPClient = &http.Client{Transport: observationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					return observationResponse(request, `{"id":"msg-test","type":"message","role":"assistant","model":"`+wireModel+`","content":[{"type":"text","text":"`+responseSecret+`"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`), nil
				})}
				return client.GenerateResponse(ctx, promptSecret, &core.AIOptions{Model: "claude-sonnet-4-5-20250929", MaxTokens: 32})
			},
		},
		{
			name:          "anthropic.vertex",
			semanticModel: "claude-sonnet-4-5-20250929",
			invoke: func(ctx context.Context, logger core.Logger, tracing core.Telemetry) (*core.AIResponse, error) {
				endpoint, err := url.Parse(
					"https://aiplatform.googleapis.com/v1/projects/" + endpointSecret +
						"/locations/global/publishers/anthropic/models/" + wireModel + ":rawPredict",
				)
				if err != nil {
					return nil, err
				}
				return invokeVertexObservation(
					ctx, logger, tracing, endpoint,
					"claude-sonnet-4-5-20250929", wireModel, promptSecret, responseSecret,
				)
			},
		},
		{
			name:          "azureopenai.v1",
			semanticModel: "gpt-4.1",
			invoke: func(ctx context.Context, logger core.Logger, tracing core.Telemetry) (*core.AIResponse, error) {
				endpoint, err := url.Parse("https://" + endpointSecret + ".example/openai/v1/chat/completions")
				if err != nil {
					return nil, err
				}
				return invokeAzureObservation(
					ctx, logger, tracing, "azureopenai.v1", endpoint, nil,
					"gpt-4.1", wireModel, promptSecret, responseSecret,
				)
			},
		},
		{
			name:          "azureopenai.classic",
			semanticModel: "gpt-4.1",
			invoke: func(ctx context.Context, logger core.Logger, tracing core.Telemetry) (*core.AIResponse, error) {
				endpoint, err := url.Parse("https://" + endpointSecret + ".example/openai/deployments/" + wireModel + "/chat/completions")
				if err != nil {
					return nil, err
				}
				return invokeAzureObservation(
					ctx, logger, tracing, "azureopenai.classic", endpoint,
					url.Values{"api-version": {"2024-10-21"}},
					"gpt-4.1", wireModel, promptSecret, responseSecret,
				)
			},
		},
		{
			name:          "gemini",
			semanticModel: "gemini-2.5-flash",
			invoke: func(ctx context.Context, logger core.Logger, tracing core.Telemetry) (*core.AIResponse, error) {
				client := gemini.NewClient(credential, "https://"+endpointSecret+".example/v1beta", logger)
				client.SetLogger(logger)
				client.SetTelemetry(tracing)
				client.MaxRetries = 0
				client.HTTPClient = &http.Client{Transport: observationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					return observationResponse(request, `{"candidates":[{"content":{"role":"model","parts":[{"text":"`+responseSecret+`"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5},"modelVersion":"`+wireModel+`"}`), nil
				})}
				return client.GenerateResponse(ctx, promptSecret, &core.AIOptions{Model: "gemini-2.5-flash", MaxTokens: 32})
			},
		},
		{
			name:          "gemini.request-aware",
			semanticModel: "gemini-3.7-flash",
			wrapped:       true,
			invoke: func(ctx context.Context, logger core.Logger, tracing core.Telemetry) (*core.AIResponse, error) {
				return invokeRegisteredGeminiObservation(ctx, logger, tracing, promptSecret, responseSecret, false)
			},
		},
		{
			name:          "gemini.request-aware-stream",
			semanticModel: "gemini-3.7-flash",
			stream:        true,
			wrapped:       true,
			invoke: func(ctx context.Context, logger core.Logger, tracing core.Telemetry) (*core.AIResponse, error) {
				return invokeRegisteredGeminiObservation(ctx, logger, tracing, promptSecret, responseSecret, true)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := newObservationLogger()
			tracing := &observationTelemetry{}
			ctx := telemetry.WithBaggage(context.Background(), "request_id", requestID)
			traceID, err := oteltrace.TraceIDFromHex(traceIDHex)
			if err != nil {
				t.Fatal(err)
			}
			spanID, err := oteltrace.SpanIDFromHex(spanIDHex)
			if err != nil {
				t.Fatal(err)
			}
			ctx = oteltrace.ContextWithSpanContext(ctx, oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
				TraceID: traceID, SpanID: spanID, TraceFlags: oteltrace.FlagsSampled,
			}))
			response, err := test.invoke(ctx, logger, tracing)
			if err != nil {
				t.Fatalf("provider call returned error: %v", err)
			}
			if response == nil || response.Content != responseSecret {
				t.Fatalf("response = %#v", response)
			}
			if len(logger.store.entries) == 0 {
				t.Fatal("no provider logs were recorded")
			}
			for index, entry := range logger.store.entries {
				if entry.component != "framework/ai" {
					t.Errorf("log %d component = %q", index, entry.component)
				}
				if entry.fields["operation"] == "" {
					t.Errorf("log %d has no operation: %#v", index, entry.fields)
				}
				if entry.fields["request_id"] != requestID {
					t.Errorf("log %d request_id = %#v", index, entry.fields["request_id"])
				}
				if !entry.contextual || entry.trace.TraceID != traceIDHex || entry.trace.SpanID != spanIDHex {
					t.Errorf("log %d lost active trace context: %#v", index, entry)
				}
			}
			operationSpan := "ai.generate_response"
			wantNames := []string{operationSpan, "ai.http_attempt"}
			if test.stream {
				operationSpan = "ai.stream_response"
				wantNames[0] = operationSpan
			}
			if test.wrapped {
				wrapperSpan := "ai.generate"
				if test.stream {
					wrapperSpan = "ai.stream"
				}
				wantNames = []string{wrapperSpan, operationSpan, "ai.http_attempt"}
			}
			if !slices.Equal(tracing.names, wantNames) {
				t.Fatalf("span hierarchy = %#v", tracing.names)
			}

			var observations strings.Builder
			for _, entry := range logger.store.entries {
				fmt.Fprint(&observations, entry.component, entry.fields)
			}
			for index, span := range tracing.spans {
				if span.ended != 1 {
					t.Errorf("span %d ended %d times", index, span.ended)
				}
				if len(span.errors) != 0 {
					t.Errorf("span %d errors = %#v", index, span.errors)
				}
				if span.attributes["request_id"] != requestID {
					t.Errorf("span %d request_id = %#v", index, span.attributes["request_id"])
				}
				fmt.Fprint(&observations, span.attributes)
			}
			for index, span := range tracing.spans {
				wantParent := index - 1
				if span.parent != wantParent {
					t.Fatalf("span %d parent = %d, want %d", index, span.parent, wantParent)
				}
			}
			for _, metric := range tracing.metrics {
				fmt.Fprint(&observations, metric.name, metric.value, metric.labels)
			}
			if !strings.Contains(observations.String(), test.semanticModel) {
				t.Errorf("semantic model missing from permitted observations: %s", observations.String())
			}
			for _, forbidden := range []string{
				promptSecret,
				responseSecret,
				endpointSecret,
				credential,
				credentialScope,
				wireModel,
			} {
				if strings.Contains(observations.String(), forbidden) {
					t.Fatalf("observations leaked %q: %s", forbidden, observations.String())
				}
			}
		})
	}
}
