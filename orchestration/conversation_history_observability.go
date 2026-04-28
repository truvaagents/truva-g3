package orchestration

import (
	"context"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

func requestIDFromBaggage(ctx context.Context) string {
	if bag := telemetry.GetBaggage(ctx); bag != nil && bag["request_id"] != "" {
		return bag["request_id"]
	}
	return GetRequestID(ctx)
}

func originalRequestIDFromBaggage(ctx context.Context) string {
	if bag := telemetry.GetBaggage(ctx); bag != nil && bag["original_request_id"] != "" {
		return bag["original_request_id"]
	}
	return requestIDFromBaggage(ctx)
}

func startPrepareSpan(ctx context.Context, t core.Telemetry, path string) (context.Context, core.Span) {
	if t == nil {
		t = &core.NoOpTelemetry{}
	}
	ctx, span := t.StartSpan(ctx, "conversation_history.prepare")
	if span == nil {
		span = &core.NoOpSpan{}
	}
	if requestID := requestIDFromBaggage(ctx); requestID != "" {
		span.SetAttribute("request_id", requestID)
	}
	if originalRequestID := originalRequestIDFromBaggage(ctx); originalRequestID != "" {
		span.SetAttribute("original_request_id", originalRequestID)
	}
	if path != "" {
		span.SetAttribute("path", path)
	}
	return ctx, span
}

func startCompactionSpan(ctx context.Context, t core.Telemetry, name string) (context.Context, core.Span) {
	if t == nil {
		t = &core.NoOpTelemetry{}
	}
	if name == "" {
		name = "conversation_history.compact"
	}
	ctx, span := t.StartSpan(ctx, name)
	if span == nil {
		span = &core.NoOpSpan{}
	}
	if requestID := requestIDFromBaggage(ctx); requestID != "" {
		span.SetAttribute("request_id", requestID)
	}
	if originalRequestID := originalRequestIDFromBaggage(ctx); originalRequestID != "" {
		span.SetAttribute("original_request_id", originalRequestID)
	}
	return ctx, span
}
