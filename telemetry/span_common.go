// Package telemetry — common span attribute helpers.
//
// Distributed traces become navigable when every span carries the small set of
// identifiers that tie work back to a business request: request_id,
// original_request_id (HITL resumes), agent_name, user_id. The orchestrator
// puts these into baggage on every request; this file provides one helper that
// pulls them off baggage and stamps them onto a span. Call from anywhere a
// span starts and the span becomes greppable in Jaeger by request_id.
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// CommonSpanAttrKeys lists baggage keys that are mirrored onto spans by
// SetCommonSpanAttrs. Order matters only for documentation — span attribute
// order is not preserved.
//
// ai.purpose is included because the framework issues many ai.generate_response
// spans that all look identical in Jaeger; the purpose ("plan_generation",
// "tiered_selection", "synthesis", "user_memory_extraction", etc.) is the
// difference that lets operators read the trace.
var CommonSpanAttrKeys = []string{
	"request_id",
	"original_request_id",
	"agent_name",
	"user_id",
	"session_id",
	"ai.purpose",
}

// SetCommonSpanAttrs copies the standard request-identifying values from
// context baggage onto the current span. Safe to call when no span exists or
// no baggage is set — it becomes a no-op.
//
// Use at the start of any meaningful span (orchestrator phases, AI calls,
// pipeline hooks, user_memory operations) so that searches by request_id in
// Jaeger surface the span without requiring trace-id correlation.
func SetCommonSpanAttrs(ctx context.Context) {
	if ctx == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	bag := GetBaggage(ctx)
	if len(bag) == 0 {
		return
	}
	attrs := make([]attribute.KeyValue, 0, len(CommonSpanAttrKeys))
	for _, k := range CommonSpanAttrKeys {
		if v, ok := bag[k]; ok && v != "" {
			attrs = append(attrs, attribute.String(k, v))
		}
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
}

// SetCommonAttrsOn mirrors SetCommonSpanAttrs but operates on a core.Span
// directly, for code paths that hold a Span returned by core.Telemetry.StartSpan
// and don't want to round-trip through trace.SpanFromContext.
//
// The span argument must implement SetAttribute(key, value) — any value that
// satisfies the core.Span interface qualifies. Passing nil is a no-op.
func SetCommonAttrsOn(ctx context.Context, span interface {
	SetAttribute(key string, value interface{})
}) {
	if ctx == nil || span == nil {
		return
	}
	bag := GetBaggage(ctx)
	if len(bag) == 0 {
		return
	}
	for _, k := range CommonSpanAttrKeys {
		if v, ok := bag[k]; ok && v != "" {
			span.SetAttribute(k, v)
		}
	}
}

// StartChildSpan starts a span on the truvag3-telemetry tracer and returns the
// child context plus an end function. Common identifying attributes from
// baggage are stamped automatically.
//
// Use from code paths that don't hold a core.Telemetry instance (pipeline
// hooks, user_memory operations, ai chain client) but still want a real span
// rather than just an event. Pass the returned end as `defer end()`.
//
// Adding attributes after Start: use SetSpanAttributes(ctx, …) — the helper's
// child context is the active context until end() runs.
func StartChildSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	tracer := otel.Tracer("truvag3-telemetry")
	ctx, span := tracer.Start(ctx, name)
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	if bag := GetBaggage(ctx); len(bag) > 0 {
		commonAttrs := make([]attribute.KeyValue, 0, len(CommonSpanAttrKeys))
		for _, k := range CommonSpanAttrKeys {
			if v, ok := bag[k]; ok && v != "" {
				commonAttrs = append(commonAttrs, attribute.String(k, v))
			}
		}
		if len(commonAttrs) > 0 {
			span.SetAttributes(commonAttrs...)
		}
	}
	return ctx, func() { span.End() }
}
