package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestCommonSpanHelpersIncludeConversationID(t *testing.T) {
	t.Run("current span", func(t *testing.T) {
		recorder, _ := setupTestTracer(t)
		ctx, err := WithBaggageExact(
			context.Background(),
			"conversation_id",
			"conversation-span",
			WithMetricLabelEligibility(false),
		)
		if err != nil {
			t.Fatalf("WithBaggageExact() error = %v", err)
		}

		spanCtx, span := otel.Tracer("common-span-test").Start(ctx, "common-current")
		SetCommonSpanAttrs(spanCtx)
		span.End()

		ended := recorder.Ended()
		if len(ended) != 1 {
			t.Fatalf("ended spans = %d, want 1", len(ended))
		}
		if got, ok := spanAttributeString(ended[0], "conversation_id"); !ok || got != "conversation-span" {
			t.Fatalf("conversation_id span attribute = %q, %v", got, ok)
		}
	})

	t.Run("child span", func(t *testing.T) {
		recorder, _ := setupTestTracer(t)
		ctx, err := WithBaggageExact(
			context.Background(),
			"conversation_id",
			"conversation-span",
			WithMetricLabelEligibility(false),
		)
		if err != nil {
			t.Fatalf("WithBaggageExact() error = %v", err)
		}

		_, end := StartChildSpan(ctx, "common-child")
		end()

		ended := recorder.Ended()
		if len(ended) != 1 {
			t.Fatalf("ended spans = %d, want 1", len(ended))
		}
		if got, ok := spanAttributeString(ended[0], "conversation_id"); !ok || got != "conversation-span" {
			t.Fatalf("conversation_id span attribute = %q, %v", got, ok)
		}
	})
}

func TestSetCommonAttrsOnIncludesConversationID(t *testing.T) {
	ctx, err := WithBaggageExact(
		context.Background(),
		"conversation_id",
		"conversation-core-span",
		WithMetricLabelEligibility(false),
	)
	if err != nil {
		t.Fatalf("WithBaggageExact() error = %v", err)
	}
	span := &commonAttributeCapture{}

	SetCommonAttrsOn(ctx, span)

	if got := span.attributes["conversation_id"]; got != "conversation-core-span" {
		t.Fatalf("conversation_id = %v", got)
	}
}

type commonAttributeCapture struct {
	attributes map[string]interface{}
}

func (c *commonAttributeCapture) SetAttribute(key string, value interface{}) {
	if c.attributes == nil {
		c.attributes = make(map[string]interface{})
	}
	c.attributes[key] = value
}

func spanAttributeString(span sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value.AsString(), true
		}
	}
	return "", false
}
