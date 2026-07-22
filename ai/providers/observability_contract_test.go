package providers_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/providers/anthropic"
	"github.com/truvaagents/truva-g3/ai/providers/gemini"
	"github.com/truvaagents/truva-g3/ai/providers/openai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type observationRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip observationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type observationLogEntry struct {
	component string
	fields    map[string]interface{}
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

func (logger *observationLogger) record(fields map[string]interface{}) {
	logger.store.entries = append(logger.store.entries, observationLogEntry{
		component: logger.component,
		fields:    fields,
	})
}

func (logger *observationLogger) Debug(_ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *observationLogger) Info(_ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *observationLogger) Warn(_ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *observationLogger) Error(_ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *observationLogger) DebugWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *observationLogger) InfoWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *observationLogger) WarnWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *observationLogger) ErrorWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.record(fields)
}

type observationTelemetry struct {
	names []string
	spans []*observationSpan
}

func (tracing *observationTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	span := &observationSpan{attributes: map[string]interface{}{}}
	tracing.names = append(tracing.names, name)
	tracing.spans = append(tracing.spans, span)
	return ctx, span
}

func (*observationTelemetry) RecordMetric(string, float64, map[string]string) {}

type observationSpan struct {
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

func TestBuiltInProviderObservationContract(t *testing.T) {
	const (
		requestID      = "observation-request-id"
		promptSecret   = "observation-prompt-secret"
		responseSecret = "observation-response-secret"
		endpointSecret = "observation-endpoint-secret"
		credential     = "observation-credential-secret"
		wireModel      = "observation-wire-model-secret"
	)

	tests := []struct {
		name          string
		semanticModel string
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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := newObservationLogger()
			tracing := &observationTelemetry{}
			ctx := telemetry.WithBaggage(context.Background(), "request_id", requestID)
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
			}
			if len(tracing.names) != 2 || tracing.names[0] != "ai.generate_response" || tracing.names[1] != "ai.http_attempt" {
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
			if !strings.Contains(observations.String(), test.semanticModel) {
				t.Errorf("semantic model missing from permitted observations: %s", observations.String())
			}
			for _, forbidden := range []string{
				promptSecret,
				responseSecret,
				endpointSecret,
				credential,
				wireModel,
			} {
				if strings.Contains(observations.String(), forbidden) {
					t.Fatalf("observations leaked %q: %s", forbidden, observations.String())
				}
			}
		})
	}
}
