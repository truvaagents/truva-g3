// Package telemetry provides async span creation for trace context restoration.
//
// This file provides StartLinkedSpan for creating spans linked to stored trace
// context, enabling distributed tracing continuity across async boundaries
// like task queues and background workers.
//
// # Async Trace Continuity
//
// When tasks are submitted to a queue and processed later by a worker, the
// original trace context would be lost without explicit propagation. This
// function creates a new span that is linked to the original trace, allowing
// tools like Jaeger to show the complete request journey.
//
// Usage:
//
//	// In worker processing a task from queue
//	ctx, endSpan := telemetry.StartLinkedSpan(
//	    context.Background(),
//	    "task.process",
//	    task.TraceID,
//	    task.ParentSpanID,
//	    map[string]string{"task.id": task.ID},
//	)
//	defer endSpan()
//
//	// Process task with ctx - all child spans will be linked
//	result, err := processTask(ctx, task)
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// StartLinkedSpan creates a span linked to a stored trace context.
// Used for async workers restoring trace continuity from persistent storage.
//
// This function is essential for maintaining distributed trace chains across
// async boundaries. When a task is submitted to a queue, the trace context
// (TraceID and SpanID) should be stored with the task. When a worker picks
// up the task, it calls StartLinkedSpan to create a new span that is linked
// to the original request's trace.
//
// Parameters:
//   - ctx: Base context (typically context.Background() for workers)
//   - name: Span name (e.g., "task.process")
//   - traceID: W3C trace ID (32 hex chars) from stored task
//   - parentSpanID: Span ID (16 hex chars) from stored task
//   - attributes: Key-value pairs to attach to span
//
// Returns:
//   - context.Context with the new span attached
//   - func() to call when span completes (use with defer)
//
// The returned context can be used for child operations, and all child spans
// will be properly linked to the original trace. In Jaeger, you'll see the
// worker span with a "link" reference to the original parent span.
//
// Example:
//
//	func (w *TaskWorker) processTask(task *core.Task) error {
//	    ctx, endSpan := telemetry.StartLinkedSpan(
//	        context.Background(),
//	        "task.process",
//	        task.TraceID,
//	        task.ParentSpanID,
//	        map[string]string{
//	            "task.id":   task.ID,
//	            "task.type": task.Type,
//	        },
//	    )
//	    defer endSpan()
//
//	    // All operations using ctx will be part of this span
//	    return w.handler.Handle(ctx, task)
//	}
//
// If traceID or parentSpanID are empty or invalid, the function still creates
// a valid span but without the link to the parent. This ensures graceful
// degradation when trace context is unavailable.
func StartLinkedSpan(
	ctx context.Context,
	name string,
	traceID string,
	parentSpanID string,
	attributes map[string]string,
) (context.Context, func()) {
	// Handle nil context gracefully
	if ctx == nil {
		ctx = context.Background()
	}

	tracer := otel.Tracer("truvag3-telemetry")

	// Build span options
	opts := []trace.SpanStartOption{}

	// Add link to parent if trace context is valid
	if traceID != "" && parentSpanID != "" {
		tid, tidErr := trace.TraceIDFromHex(traceID)
		sid, sidErr := trace.SpanIDFromHex(parentSpanID)

		if tidErr == nil && sidErr == nil {
			parentSC := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: tid,
				SpanID:  sid,
				Remote:  true,
			})
			opts = append(opts, trace.WithLinks(trace.Link{
				SpanContext: parentSC,
				Attributes: []attribute.KeyValue{
					attribute.String("link.type", "async_task"),
				},
			}))
		}
	}

	// Start span
	ctx, span := tracer.Start(ctx, name, opts...)

	// Add attributes
	for k, v := range attributes {
		span.SetAttributes(attribute.String(k, v))
	}

	return ctx, func() { span.End() }
}

// StartLinkedSpanWithOptions creates a span with additional configuration options.
// This is an advanced version of StartLinkedSpan for cases where you need more
// control over span creation.
//
// Parameters:
//   - ctx: Base context
//   - name: Span name
//   - traceID: W3C trace ID from stored task
//   - parentSpanID: Span ID from stored task
//   - attributes: Key-value pairs to attach to span
//   - spanKind: The kind of span (e.g., trace.SpanKindConsumer for queue consumers)
//
// Example:
//
//	ctx, endSpan := telemetry.StartLinkedSpanWithOptions(
//	    context.Background(),
//	    "task.process",
//	    task.TraceID,
//	    task.ParentSpanID,
//	    map[string]string{"task.id": task.ID},
//	    trace.SpanKindConsumer,
//	)
//	defer endSpan()
func StartLinkedSpanWithOptions(
	ctx context.Context,
	name string,
	traceID string,
	parentSpanID string,
	attributes map[string]string,
	spanKind trace.SpanKind,
) (context.Context, func()) {
	tracer := otel.Tracer("truvag3-telemetry")

	// Build span options
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(spanKind),
	}

	// Add link to parent if trace context is valid
	if traceID != "" && parentSpanID != "" {
		tid, tidErr := trace.TraceIDFromHex(traceID)
		sid, sidErr := trace.SpanIDFromHex(parentSpanID)

		if tidErr == nil && sidErr == nil {
			parentSC := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: tid,
				SpanID:  sid,
				Remote:  true,
			})
			opts = append(opts, trace.WithLinks(trace.Link{
				SpanContext: parentSC,
				Attributes: []attribute.KeyValue{
					attribute.String("link.type", "async_task"),
				},
			}))
		}
	}

	// Start span
	ctx, span := tracer.Start(ctx, name, opts...)

	// Add attributes
	for k, v := range attributes {
		span.SetAttributes(attribute.String(k, v))
	}

	return ctx, func() { span.End() }
}
