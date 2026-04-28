package telemetry

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// setupTestTracer creates a test tracer with an in-memory span recorder
func setupTestTracer(t *testing.T) (*tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	return recorder, tp.Tracer("test-tracer")
}

// TestGetTraceContext tests extracting trace context from a span
func TestGetTraceContext(t *testing.T) {
	recorder, tracer := setupTestTracer(t)

	t.Run("returns empty context when ctx is nil", func(t *testing.T) {
		tc := GetTraceContext(nil)
		if tc.TraceID != "" {
			t.Errorf("Expected empty TraceID, got %s", tc.TraceID)
		}
		if tc.SpanID != "" {
			t.Errorf("Expected empty SpanID, got %s", tc.SpanID)
		}
		if tc.Sampled {
			t.Error("Expected Sampled to be false")
		}
	})

	t.Run("returns empty context when no span in context", func(t *testing.T) {
		ctx := context.Background()
		tc := GetTraceContext(ctx)
		if tc.TraceID != "" {
			t.Errorf("Expected empty TraceID, got %s", tc.TraceID)
		}
		if tc.SpanID != "" {
			t.Errorf("Expected empty SpanID, got %s", tc.SpanID)
		}
	})

	t.Run("extracts trace context from active span", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")
		defer span.End()

		tc := GetTraceContext(ctx)

		// TraceID should be 32 hex characters
		if len(tc.TraceID) != 32 {
			t.Errorf("Expected 32-char TraceID, got %d chars: %s", len(tc.TraceID), tc.TraceID)
		}

		// SpanID should be 16 hex characters
		if len(tc.SpanID) != 16 {
			t.Errorf("Expected 16-char SpanID, got %d chars: %s", len(tc.SpanID), tc.SpanID)
		}

		// Should be sampled (test tracer samples all)
		if !tc.Sampled {
			t.Error("Expected Sampled to be true for recorded span")
		}
	})

	// Verify spans were recorded
	spans := recorder.Ended()
	if len(spans) == 0 {
		t.Log("Note: No spans were ended yet (deferred)")
	}
}

// TestHasTraceContext tests checking for trace context presence
func TestHasTraceContext(t *testing.T) {
	_, tracer := setupTestTracer(t)

	t.Run("returns false when ctx is nil", func(t *testing.T) {
		if HasTraceContext(nil) {
			t.Error("Expected false for nil context")
		}
	})

	t.Run("returns false when no span in context", func(t *testing.T) {
		ctx := context.Background()
		if HasTraceContext(ctx) {
			t.Error("Expected false for context without span")
		}
	})

	t.Run("returns true when span exists", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")
		defer span.End()

		if !HasTraceContext(ctx) {
			t.Error("Expected true for context with active span")
		}
	})
}

// TestAddSpanEvent tests adding events to spans
func TestAddSpanEvent(t *testing.T) {
	recorder, tracer := setupTestTracer(t)

	t.Run("handles nil context gracefully", func(t *testing.T) {
		// Should not panic
		AddSpanEvent(nil, "test-event")
	})

	t.Run("handles context without span gracefully", func(t *testing.T) {
		// Should not panic
		AddSpanEvent(context.Background(), "test-event")
	})

	t.Run("adds event to active span", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")

		AddSpanEvent(ctx, "validation_complete")
		AddSpanEvent(ctx, "api_called",
			attribute.String("endpoint", "/weather"),
			attribute.Int("retry", 1),
		)

		span.End()

		// Check recorded spans
		spans := recorder.Ended()
		if len(spans) == 0 {
			t.Fatal("Expected at least one recorded span")
		}

		events := spans[len(spans)-1].Events()
		if len(events) != 2 {
			t.Errorf("Expected 2 events, got %d", len(events))
		}

		// Verify first event
		if events[0].Name != "validation_complete" {
			t.Errorf("Expected event name 'validation_complete', got '%s'", events[0].Name)
		}

		// Verify second event with attributes
		if events[1].Name != "api_called" {
			t.Errorf("Expected event name 'api_called', got '%s'", events[1].Name)
		}

		// Check attributes on second event
		foundEndpoint := false
		foundRetry := false
		for _, attr := range events[1].Attributes {
			if attr.Key == "endpoint" && attr.Value.AsString() == "/weather" {
				foundEndpoint = true
			}
			if attr.Key == "retry" && attr.Value.AsInt64() == 1 {
				foundRetry = true
			}
		}
		if !foundEndpoint {
			t.Error("Expected endpoint attribute on event")
		}
		if !foundRetry {
			t.Error("Expected retry attribute on event")
		}
	})
}

// TestRecordSpanError tests error recording on spans
func TestRecordSpanError(t *testing.T) {
	recorder, tracer := setupTestTracer(t)

	t.Run("handles nil context gracefully", func(t *testing.T) {
		// Should not panic
		RecordSpanError(nil, errors.New("test error"))
	})

	t.Run("handles nil error gracefully", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")
		defer span.End()

		// Should not panic
		RecordSpanError(ctx, nil)
	})

	t.Run("handles context without span gracefully", func(t *testing.T) {
		// Should not panic
		RecordSpanError(context.Background(), errors.New("test error"))
	})

	t.Run("records error on active span", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")

		testErr := errors.New("connection timeout")
		RecordSpanError(ctx, testErr)

		span.End()

		// Check recorded spans
		spans := recorder.Ended()
		if len(spans) == 0 {
			t.Fatal("Expected at least one recorded span")
		}

		lastSpan := spans[len(spans)-1]

		// Verify status is Error
		if lastSpan.Status().Code != codes.Error {
			t.Errorf("Expected Error status, got %v", lastSpan.Status().Code)
		}

		// Verify status description matches error
		if lastSpan.Status().Description != "connection timeout" {
			t.Errorf("Expected 'connection timeout' description, got '%s'", lastSpan.Status().Description)
		}

		// Verify exception event was added
		events := lastSpan.Events()
		foundException := false
		for _, event := range events {
			if event.Name == "exception" {
				foundException = true
				break
			}
		}
		if !foundException {
			t.Error("Expected 'exception' event to be recorded")
		}
	})
}

// TestSetSpanAttributes tests setting attributes on spans
func TestSetSpanAttributes(t *testing.T) {
	recorder, tracer := setupTestTracer(t)

	t.Run("handles nil context gracefully", func(t *testing.T) {
		// Should not panic
		SetSpanAttributes(nil, attribute.String("key", "value"))
	})

	t.Run("handles context without span gracefully", func(t *testing.T) {
		// Should not panic
		SetSpanAttributes(context.Background(), attribute.String("key", "value"))
	})

	t.Run("sets attributes on active span", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")

		SetSpanAttributes(ctx,
			attribute.String("truvag3.tool.name", "weather-tool"),
			attribute.Bool("cache.hit", false),
			attribute.Int("retry.count", 2),
		)

		span.End()

		// Check recorded spans
		spans := recorder.Ended()
		if len(spans) == 0 {
			t.Fatal("Expected at least one recorded span")
		}

		attrs := spans[len(spans)-1].Attributes()

		// Verify attributes were set
		foundToolName := false
		foundCacheHit := false
		foundRetryCount := false

		for _, attr := range attrs {
			switch string(attr.Key) {
			case "truvag3.tool.name":
				if attr.Value.AsString() == "weather-tool" {
					foundToolName = true
				}
			case "cache.hit":
				if !attr.Value.AsBool() {
					foundCacheHit = true
				}
			case "retry.count":
				if attr.Value.AsInt64() == 2 {
					foundRetryCount = true
				}
			}
		}

		if !foundToolName {
			t.Error("Expected truvag3.tool.name attribute")
		}
		if !foundCacheHit {
			t.Error("Expected cache.hit attribute")
		}
		if !foundRetryCount {
			t.Error("Expected retry.count attribute")
		}
	})
}

// TestSetSpanStatus tests setting status on spans
func TestSetSpanStatus(t *testing.T) {
	recorder, tracer := setupTestTracer(t)

	t.Run("handles nil context gracefully", func(t *testing.T) {
		// Should not panic
		SetSpanStatus(nil, codes.Ok, "success")
	})

	t.Run("handles context without span gracefully", func(t *testing.T) {
		// Should not panic
		SetSpanStatus(context.Background(), codes.Ok, "success")
	})

	t.Run("sets OK status on span", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")

		SetSpanStatus(ctx, codes.Ok, "operation completed")

		span.End()

		// Check recorded spans
		spans := recorder.Ended()
		if len(spans) == 0 {
			t.Fatal("Expected at least one recorded span")
		}

		status := spans[len(spans)-1].Status()
		if status.Code != codes.Ok {
			t.Errorf("Expected Ok status, got %v", status.Code)
		}
	})

	t.Run("sets Error status on span", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")

		SetSpanStatus(ctx, codes.Error, "validation failed")

		span.End()

		// Check recorded spans
		spans := recorder.Ended()
		if len(spans) == 0 {
			t.Fatal("Expected at least one recorded span")
		}

		status := spans[len(spans)-1].Status()
		if status.Code != codes.Error {
			t.Errorf("Expected Error status, got %v", status.Code)
		}
		if status.Description != "validation failed" {
			t.Errorf("Expected 'validation failed' description, got '%s'", status.Description)
		}
	})
}

// TestGetBaggageWithTraceContext tests the updated GetBaggage that includes trace context
func TestGetBaggageWithTraceContext(t *testing.T) {
	_, tracer := setupTestTracer(t)

	t.Run("includes trace context in baggage", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")
		defer span.End()

		// Create framework registry
		registry := NewFrameworkMetricsRegistry(nil)

		// Get baggage - should include trace_id and span_id
		baggage := registry.GetBaggage(ctx)

		if baggage["trace_id"] == "" {
			t.Error("Expected trace_id in baggage")
		}
		if baggage["span_id"] == "" {
			t.Error("Expected span_id in baggage")
		}

		// Verify format
		if len(baggage["trace_id"]) != 32 {
			t.Errorf("Expected 32-char trace_id, got %d chars", len(baggage["trace_id"]))
		}
		if len(baggage["span_id"]) != 16 {
			t.Errorf("Expected 16-char span_id, got %d chars", len(baggage["span_id"]))
		}
	})

	t.Run("returns empty baggage when no trace context", func(t *testing.T) {
		ctx := context.Background()

		registry := NewFrameworkMetricsRegistry(nil)
		baggage := registry.GetBaggage(ctx)

		// Should return empty map, not nil
		if baggage == nil {
			t.Error("Expected non-nil baggage map")
		}

		// Should not have trace_id or span_id
		if baggage["trace_id"] != "" {
			t.Errorf("Expected empty trace_id, got %s", baggage["trace_id"])
		}
		if baggage["span_id"] != "" {
			t.Errorf("Expected empty span_id, got %s", baggage["span_id"])
		}
	})

	t.Run("preserves W3C baggage alongside trace context", func(t *testing.T) {
		ctx, span := tracer.Start(context.Background(), "test-operation")
		defer span.End()

		// Add W3C baggage
		ctx = WithBaggage(ctx, "request_id", "req-123", "user_id", "user-456")

		registry := NewFrameworkMetricsRegistry(nil)
		baggage := registry.GetBaggage(ctx)

		// Should have both W3C baggage and trace context
		if baggage["request_id"] != "req-123" {
			t.Errorf("Expected request_id='req-123', got '%s'", baggage["request_id"])
		}
		if baggage["user_id"] != "user-456" {
			t.Errorf("Expected user_id='user-456', got '%s'", baggage["user_id"])
		}
		if baggage["trace_id"] == "" {
			t.Error("Expected trace_id in baggage")
		}
		if baggage["span_id"] == "" {
			t.Error("Expected span_id in baggage")
		}
	})
}
