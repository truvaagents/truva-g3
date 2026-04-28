// Package telemetry provides trace context extraction for log correlation.
//
// This file provides functions to extract OpenTelemetry trace context
// (trace_id, span_id) from context for log correlation, and helper
// functions for adding span events and recording errors.
//
// # Log Correlation
//
// Use GetTraceContext to extract trace identifiers for inclusion in logs:
//
//	tc := telemetry.GetTraceContext(ctx)
//	logger.Info("Processing request", map[string]interface{}{
//	    "trace_id": tc.TraceID,
//	    "span_id":  tc.SpanID,
//	})
//
// # Span Events
//
// Use AddSpanEvent to mark meaningful points in time within a span:
//
//	telemetry.AddSpanEvent(ctx, "validation_complete")
//	telemetry.AddSpanEvent(ctx, "api_called",
//	    attribute.String("endpoint", "/weather"),
//	)
//
// # Error Recording
//
// Use RecordSpanError to capture errors with stack traces:
//
//	if err != nil {
//	    telemetry.RecordSpanError(ctx, err)
//	    return err
//	}
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// TraceContext holds trace and span identifiers for log correlation.
// This struct is used to bridge OpenTelemetry trace context with logging.
type TraceContext struct {
	// TraceID is the 32-character hex trace identifier (e.g., "ad941a390c5c6d4d0f878eec73bdc478")
	TraceID string

	// SpanID is the 16-character hex span identifier (e.g., "84834e2917631e82")
	SpanID string

	// Sampled indicates whether this trace is being sampled (recorded)
	Sampled bool
}

// GetTraceContext extracts OpenTelemetry trace context from the context.
// Returns empty strings if no valid trace context exists.
//
// This function is the bridge between OpenTelemetry's span context and
// structured logging. It extracts the trace_id and span_id that can be
// used to correlate logs with distributed traces in systems like Jaeger.
//
// Usage:
//
//	// In an HTTP handler after TracingMiddleware has created a span
//	ctx := r.Context()
//	tc := telemetry.GetTraceContext(ctx)
//	logger.Info("Processing request", map[string]interface{}{
//	    "trace_id": tc.TraceID,
//	    "span_id":  tc.SpanID,
//	})
//
// The trace context is automatically available when:
//   - TracingMiddleware has processed the incoming request
//   - The request includes W3C TraceContext headers (traceparent)
//   - A span has been created via otel tracer
func GetTraceContext(ctx context.Context) TraceContext {
	if ctx == nil {
		return TraceContext{}
	}

	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()

	if !sc.IsValid() {
		return TraceContext{}
	}

	return TraceContext{
		TraceID: sc.TraceID().String(),
		SpanID:  sc.SpanID().String(),
		Sampled: sc.IsSampled(),
	}
}

// HasTraceContext returns true if the context contains valid trace information.
// Use this to check whether trace context is available before attempting
// to extract it.
//
// Usage:
//
//	if telemetry.HasTraceContext(ctx) {
//	    tc := telemetry.GetTraceContext(ctx)
//	    // Use trace context
//	}
func HasTraceContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	span := trace.SpanFromContext(ctx)
	return span.SpanContext().IsValid()
}

// AddSpanEvent adds a named event to the current span.
// Events mark meaningful points in time during the span's duration.
// They are visible in trace visualization tools like Jaeger.
//
// Use span events to:
//   - Mark state transitions (e.g., "validation_complete", "cache_miss")
//   - Record external API calls (e.g., "api_called" with endpoint attribute)
//   - Track processing stages (e.g., "parsing_started", "parsing_complete")
//
// Usage:
//
//	telemetry.AddSpanEvent(ctx, "validation_complete")
//	telemetry.AddSpanEvent(ctx, "external_api_called",
//	    attribute.String("api", "openweathermap"),
//	    attribute.String("endpoint", "/weather"),
//	    attribute.Int("retry_count", 1),
//	)
//
// Events are only recorded if the span is being sampled. This function
// is safe to call even when no span exists in the context.
func AddSpanEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	if ctx == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.AddEvent(name, trace.WithAttributes(attrs...))
	}
}

// RecordSpanError records an error on the current span.
// This captures the exception type, message, and stack trace.
// It also sets the span status to Error.
//
// In trace visualization tools like Jaeger, this will:
//   - Mark the span as failed (red in the UI)
//   - Add an "exception" event with error details
//   - Include the stack trace for debugging
//
// Usage:
//
//	result, err := externalAPI.Call(ctx)
//	if err != nil {
//	    telemetry.RecordSpanError(ctx, err)
//	    return nil, err
//	}
//
// This function is safe to call even when no span exists in the context.
// It will not record anything if ctx is nil or err is nil.
func RecordSpanError(ctx context.Context, err error) {
	if ctx == nil || err == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// SetSpanAttributes adds attributes to the current span.
// Use for business context that aids debugging and analysis.
//
// Attributes are key-value pairs that provide additional context
// about the operation being traced. They are visible in trace
// visualization tools and can be used for filtering and searching.
//
// Common use cases:
//   - Business context: user tier, feature flags, request type
//   - Operation details: cache hit/miss, retry count, data size
//   - Custom identifiers: order ID, session ID (be mindful of cardinality)
//
// Usage:
//
//	telemetry.SetSpanAttributes(ctx,
//	    attribute.String("truvag3.tool.name", "weather-tool"),
//	    attribute.String("truvag3.capability", "get_weather"),
//	    attribute.String("request.location", "Tokyo"),
//	    attribute.Bool("cache.hit", false),
//	    attribute.Int("retry.count", 2),
//	)
//
// Best practices:
//   - Use semantic conventions where applicable (e.g., http.method)
//   - Avoid high-cardinality values that could cause metric explosion
//   - Don't include sensitive data (passwords, API keys, PII)
func SetSpanAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	if ctx == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetAttributes(attrs...)
	}
}

// SetSpanStatus sets the status of the current span.
// Use this to indicate success or failure when not using RecordSpanError.
//
// Usage:
//
//	telemetry.SetSpanStatus(ctx, codes.Ok, "operation completed successfully")
//	// or
//	telemetry.SetSpanStatus(ctx, codes.Error, "validation failed")
func SetSpanStatus(ctx context.Context, code codes.Code, description string) {
	if ctx == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetStatus(code, description)
	}
}
