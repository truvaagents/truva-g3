package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestStartLinkedSpan(t *testing.T) {
	// Set up a no-op tracer provider for testing
	otel.SetTracerProvider(noop.NewTracerProvider())

	tests := []struct {
		name         string
		spanName     string
		traceID      string
		parentSpanID string
		attributes   map[string]string
		wantErr      bool
	}{
		{
			name:         "valid trace context",
			spanName:     "test.operation",
			traceID:      "0af7651916cd43dd8448eb211c80319c",
			parentSpanID: "b7ad6b7169203331",
			attributes:   map[string]string{"task.id": "task-123"},
			wantErr:      false,
		},
		{
			name:         "empty trace context",
			spanName:     "test.operation",
			traceID:      "",
			parentSpanID: "",
			attributes:   map[string]string{"task.id": "task-456"},
			wantErr:      false, // Should still work, just without link
		},
		{
			name:         "invalid trace ID",
			spanName:     "test.operation",
			traceID:      "invalid",
			parentSpanID: "b7ad6b7169203331",
			attributes:   nil,
			wantErr:      false, // Should still work, just without link
		},
		{
			name:         "invalid span ID",
			spanName:     "test.operation",
			traceID:      "0af7651916cd43dd8448eb211c80319c",
			parentSpanID: "invalid",
			attributes:   nil,
			wantErr:      false, // Should still work, just without link
		},
		{
			name:         "nil attributes",
			spanName:     "test.operation",
			traceID:      "0af7651916cd43dd8448eb211c80319c",
			parentSpanID: "b7ad6b7169203331",
			attributes:   nil,
			wantErr:      false,
		},
		{
			name:         "empty span name",
			spanName:     "",
			traceID:      "0af7651916cd43dd8448eb211c80319c",
			parentSpanID: "b7ad6b7169203331",
			attributes:   nil,
			wantErr:      false,
		},
		{
			name:         "multiple attributes",
			spanName:     "task.process",
			traceID:      "0af7651916cd43dd8448eb211c80319c",
			parentSpanID: "b7ad6b7169203331",
			attributes: map[string]string{
				"task.id":   "task-789",
				"task.type": "orchestration",
				"worker.id": "worker-1",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			newCtx, endSpan := StartLinkedSpan(
				ctx,
				tt.spanName,
				tt.traceID,
				tt.parentSpanID,
				tt.attributes,
			)

			// Verify context is returned
			if newCtx == nil {
				t.Error("StartLinkedSpan returned nil context")
			}

			// Verify endSpan function is returned
			if endSpan == nil {
				t.Error("StartLinkedSpan returned nil endSpan function")
			}

			// Call endSpan to ensure it doesn't panic
			endSpan()
		})
	}
}

func TestStartLinkedSpanWithOptions(t *testing.T) {
	// Set up a no-op tracer provider for testing
	otel.SetTracerProvider(noop.NewTracerProvider())

	tests := []struct {
		name         string
		spanName     string
		traceID      string
		parentSpanID string
		attributes   map[string]string
		spanKind     trace.SpanKind
	}{
		{
			name:         "consumer span kind",
			spanName:     "task.process",
			traceID:      "0af7651916cd43dd8448eb211c80319c",
			parentSpanID: "b7ad6b7169203331",
			attributes:   map[string]string{"task.id": "task-123"},
			spanKind:     trace.SpanKindConsumer,
		},
		{
			name:         "internal span kind",
			spanName:     "task.process",
			traceID:      "0af7651916cd43dd8448eb211c80319c",
			parentSpanID: "b7ad6b7169203331",
			attributes:   map[string]string{"task.id": "task-456"},
			spanKind:     trace.SpanKindInternal,
		},
		{
			name:         "server span kind",
			spanName:     "api.handle",
			traceID:      "",
			parentSpanID: "",
			attributes:   nil,
			spanKind:     trace.SpanKindServer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			newCtx, endSpan := StartLinkedSpanWithOptions(
				ctx,
				tt.spanName,
				tt.traceID,
				tt.parentSpanID,
				tt.attributes,
				tt.spanKind,
			)

			// Verify context is returned
			if newCtx == nil {
				t.Error("StartLinkedSpanWithOptions returned nil context")
			}

			// Verify endSpan function is returned
			if endSpan == nil {
				t.Error("StartLinkedSpanWithOptions returned nil endSpan function")
			}

			// Call endSpan to ensure it doesn't panic
			endSpan()
		})
	}
}

func TestStartLinkedSpan_DeferPattern(t *testing.T) {
	// Set up a no-op tracer provider for testing
	otel.SetTracerProvider(noop.NewTracerProvider())

	// Test the typical defer pattern
	func() {
		ctx, endSpan := StartLinkedSpan(
			context.Background(),
			"test.operation",
			"0af7651916cd43dd8448eb211c80319c",
			"b7ad6b7169203331",
			map[string]string{"key": "value"},
		)
		defer endSpan()

		// Use the context
		if ctx == nil {
			t.Error("Context should not be nil")
		}
	}()
}

func TestStartLinkedSpan_NilContext(t *testing.T) {
	// Set up a no-op tracer provider for testing
	otel.SetTracerProvider(noop.NewTracerProvider())

	// Test with nil context - should handle gracefully
	ctx, endSpan := StartLinkedSpan(
		nil,
		"test.operation",
		"0af7651916cd43dd8448eb211c80319c",
		"b7ad6b7169203331",
		nil,
	)

	// Should return a valid context even if nil was passed
	if ctx == nil {
		t.Error("StartLinkedSpan should return non-nil context even with nil input")
	}

	// endSpan should not panic
	endSpan()
}
