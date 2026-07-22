package gemini

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type geminiErrorLogger struct {
	fields []map[string]interface{}
}

func (*geminiErrorLogger) Debug(string, map[string]interface{}) {}
func (*geminiErrorLogger) Info(string, map[string]interface{})  {}
func (*geminiErrorLogger) Warn(string, map[string]interface{})  {}
func (*geminiErrorLogger) Error(string, map[string]interface{}) {}
func (*geminiErrorLogger) DebugWithContext(context.Context, string, map[string]interface{}) {
}
func (*geminiErrorLogger) InfoWithContext(context.Context, string, map[string]interface{}) {
}
func (*geminiErrorLogger) WarnWithContext(context.Context, string, map[string]interface{}) {
}
func (logger *geminiErrorLogger) ErrorWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.fields = append(logger.fields, fields)
}

type geminiErrorSpan struct {
	attributes map[string]interface{}
	errors     []error
}

func (*geminiErrorSpan) End() {}
func (span *geminiErrorSpan) SetAttribute(key string, value interface{}) {
	span.attributes[key] = value
}
func (span *geminiErrorSpan) RecordError(err error) { span.errors = append(span.errors, err) }

func TestGeminiObserveErrorUsesBoundedClassifiers(t *testing.T) {
	const secret = "gemini-error-secret"
	logger := &geminiErrorLogger{}
	client := NewClient("key", "https://generativelanguage.googleapis.com/v1beta", logger)
	ctx := telemetry.WithBaggage(context.Background(), "request_id", "gemini-error-request")
	tests := []struct {
		name     string
		fallback string
		err      error
		wantType string
	}{
		{name: "transport", fallback: "transport", err: errors.New(secret), wantType: "transport"},
		{name: "cancellation overrides fallback", fallback: "transport", err: fmt.Errorf("%s: %w", secret, context.Canceled), wantType: "cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			span := &geminiErrorSpan{attributes: map[string]interface{}{}}
			before := len(logger.fields)
			client.observeError(ctx, span, "ai_stream", test.fallback, test.err)
			if span.attributes["ai.error_type"] != test.wantType || len(span.errors) != 1 {
				t.Fatalf("span = %#v errors=%#v", span.attributes, span.errors)
			}
			if strings.Contains(span.errors[0].Error(), secret) {
				t.Fatalf("span error leaked secret: %v", span.errors[0])
			}
			if len(logger.fields) != before+1 {
				t.Fatalf("error logs = %d, want %d", len(logger.fields), before+1)
			}
			fields := logger.fields[len(logger.fields)-1]
			if fields["error_type"] != test.wantType || fields["request_id"] != "gemini-error-request" ||
				fields["provider"] != "gemini" || fields["provider_alias"] != "gemini" {
				t.Fatalf("error fields = %#v", fields)
			}
			if strings.Contains(fmt.Sprint(fields), secret) {
				t.Fatalf("error log leaked secret: %#v", fields)
			}
		})
	}

	client.observeError(ctx, nil, "ai_request", "credential", errors.New(secret))
	if got := logger.fields[len(logger.fields)-1]["error_type"]; got != "credential" {
		t.Fatalf("nil-span error type = %#v", got)
	}
}

func TestGeminiMissingCredentialUsesObservationContract(t *testing.T) {
	logger := &geminiErrorLogger{}
	client := NewClient("", "https://generativelanguage.googleapis.com/v1beta", logger)
	ctx := telemetry.WithBaggage(context.Background(), "request_id", "gemini-missing-key")
	response, err := client.GenerateResponse(ctx, "prompt-content", &core.AIOptions{Model: "gemini-2.5-flash"})
	if response != nil || err == nil {
		t.Fatalf("GenerateResponse = %#v, %v", response, err)
	}
	if len(logger.fields) != 1 {
		t.Fatalf("error fields = %#v", logger.fields)
	}
	fields := logger.fields[0]
	if fields["error_type"] != "credential" || fields["request_id"] != "gemini-missing-key" {
		t.Fatalf("missing-key fields = %#v", fields)
	}
	if strings.Contains(fmt.Sprint(fields), "prompt-content") {
		t.Fatalf("missing-key fields leaked prompt: %#v", fields)
	}
}
