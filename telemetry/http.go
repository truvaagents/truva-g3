// Package telemetry provides distributed tracing HTTP instrumentation.
//
// This file provides HTTP middleware and client instrumentation for
// distributed tracing using OpenTelemetry. These functions enable
// automatic trace context propagation across service boundaries.
//
// # Server Side (Middleware)
//
// Use TracingMiddleware to extract trace context from incoming requests
// and create spans for each HTTP request:
//
//	// In your main.go, after telemetry.Initialize()
//	mux := http.NewServeMux()
//	mux.HandleFunc("/api/...", handler)
//
//	// Wrap with tracing middleware
//	tracedHandler := telemetry.TracingMiddleware("my-service")(mux)
//	http.ListenAndServe(":8080", tracedHandler)
//
// # Client Side (HTTP Client)
//
// Use NewTracedHTTPClient to automatically propagate trace context
// to downstream services:
//
//	// Create a traced HTTP client
//	client := telemetry.NewTracedHTTPClient(nil)
//
//	// All requests automatically propagate trace context
//	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
//	resp, err := client.Do(req)
//
// # Initialization Requirement
//
// IMPORTANT: Call telemetry.Initialize() before using these functions.
// If telemetry is not initialized, the middleware and client will use
// no-op tracers (safe but no traces will be generated).
package telemetry

import (
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
)

// TracingMiddlewareConfig configures the tracing middleware behavior.
type TracingMiddlewareConfig struct {
	// ExcludedPaths lists URL paths to exclude from tracing.
	// Useful for health checks, metrics endpoints, etc.
	// Example: []string{"/health", "/metrics", "/ready"}
	ExcludedPaths []string

	// RequestFilter provides custom logic to exclude requests from tracing.
	// Return false to exclude a request from tracing.
	// This is evaluated AFTER ExcludedPaths, allowing fine-grained control.
	// Example: exclude polling requests with ?poll=true query parameter
	//   RequestFilter: func(r *http.Request) bool {
	//       return r.URL.Query().Get("poll") != "true"
	//   }
	RequestFilter func(r *http.Request) bool

	// SpanNameFormatter customizes how span names are generated.
	// If nil, uses "HTTP {method} {path}" format.
	SpanNameFormatter func(operation string, r *http.Request) string
}

// TracingMiddleware returns HTTP middleware that extracts trace context
// from incoming requests and creates spans for each request.
//
// This enables distributed tracing across service boundaries by:
//   - Extracting W3C TraceContext headers (traceparent, tracestate) from incoming requests
//   - Creating a span for each HTTP request
//   - Recording HTTP metrics (request count, latency, status codes)
//   - Propagating trace context to downstream handler code via context
//
// The middleware is safe to use even if telemetry is not initialized -
// it will use a no-op tracer in that case.
//
// Parameters:
//   - serviceName: Name used to identify this service in traces
//
// Example:
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("/api/weather", weatherHandler)
//
//	// Apply tracing middleware
//	traced := telemetry.TracingMiddleware("weather-service")(mux)
//	http.ListenAndServe(":8080", traced)
func TracingMiddleware(serviceName string) func(http.Handler) http.Handler {
	return TracingMiddlewareWithConfig(serviceName, nil)
}

// TracingMiddlewareWithConfig returns HTTP middleware with custom configuration.
//
// See TracingMiddleware for basic usage. This variant allows customization
// of span names, path exclusions, and other options.
//
// IMPORTANT: Call telemetry.Initialize() before using this middleware.
// The Initialize() function sets up the global TracerProvider and propagators.
// If not initialized, the middleware will use a no-op tracer (safe but no traces).
//
// Example:
//
//	config := &telemetry.TracingMiddlewareConfig{
//	    ExcludedPaths: []string{"/health", "/metrics"},
//	    SpanNameFormatter: func(op string, r *http.Request) string {
//	        return r.Method + " " + r.URL.Path
//	    },
//	}
//	traced := telemetry.TracingMiddlewareWithConfig("my-service", config)(mux)
func TracingMiddlewareWithConfig(serviceName string, config *TracingMiddlewareConfig) func(http.Handler) http.Handler {
	// NOTE: Propagators (TraceContext, Baggage) are set during telemetry.Initialize()
	// per ARCHITECTURE.md design. Do not set them here to avoid:
	// 1. Redundant calls on every middleware instantiation
	// 2. Overriding any custom propagator configuration
	// The otelhttp package uses otel.GetTextMapPropagator() which reads the global.

	// Build otelhttp options
	var opts []otelhttp.Option

	// Add combined filter if any filtering is configured
	if config != nil && (len(config.ExcludedPaths) > 0 || config.RequestFilter != nil) {
		pathSet := make(map[string]bool)
		for _, path := range config.ExcludedPaths {
			pathSet[path] = true
		}
		opts = append(opts, otelhttp.WithFilter(func(r *http.Request) bool {
			// Check excluded paths first
			if len(pathSet) > 0 && pathSet[r.URL.Path] {
				return false // Exclude from tracing
			}
			// Check custom filter
			if config.RequestFilter != nil {
				return config.RequestFilter(r)
			}
			return true // Include in tracing
		}))
	}

	// Add span name formatter if configured
	if config != nil && config.SpanNameFormatter != nil {
		opts = append(opts, otelhttp.WithSpanNameFormatter(config.SpanNameFormatter))
	} else {
		// Default: "HTTP GET /api/weather"
		opts = append(opts, otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			return "HTTP " + r.Method + " " + r.URL.Path
		}))
	}

	return func(next http.Handler) http.Handler {
		// Wrap the inner handler so that AFTER otelhttp creates the server
		// span, we read the X-TruvaG3-* headers and stamp them as span
		// attributes. This bridges the orchestrator's request_id / step_id /
		// phase_number / plan_id / original_request_id / agent_name onto
		// every tool-side server span — the missing link that makes a tool
		// failure in Jaeger navigable back to "which step in which plan".
		enriched := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stampTruvaG3HeadersOnSpan(r)
			next.ServeHTTP(w, r)
		})
		return otelhttp.NewHandler(enriched, serviceName, opts...)
	}
}

// stampTruvaG3HeadersOnSpan copies known X-TruvaG3-* request headers onto the
// current span as attributes. The current span here is the otelhttp server
// span, since this runs inside the otelhttp handler. No-op when there is no
// recording span (telemetry not initialized).
func stampTruvaG3HeadersOnSpan(r *http.Request) {
	if r == nil {
		return
	}
	headerToAttr := []struct {
		header                 string
		attr                   string
		validateConversationID bool
	}{
		{header: "X-TruvaG3-Request-ID", attr: "request_id"},
		{header: "X-TruvaG3-Original-Request-ID", attr: "original_request_id"},
		{
			header:                 "X-TruvaG3-Conversation-ID",
			attr:                   "conversation_id",
			validateConversationID: true,
		},
		{header: "X-TruvaG3-Step-ID", attr: "step_id"},
		{header: "X-TruvaG3-Phase-Number", attr: "phase_number"},
		{header: "X-TruvaG3-Plan-ID", attr: "plan_id"},
		{header: "X-TruvaG3-Agent-Name", attr: "caller_agent_name"},
	}
	attrs := make([]attribute.KeyValue, 0, len(headerToAttr))
	for _, m := range headerToAttr {
		if v := r.Header.Get(m.header); v != "" {
			if m.validateConversationID &&
				core.ValidateConversationID(v) != core.ConversationIDValidationNone {
				continue
			}
			attrs = append(attrs, attribute.String(m.attr, v))
		}
	}
	if len(attrs) > 0 {
		SetSpanAttributes(r.Context(), attrs...)
	}
}

// NewTracedHTTPClient creates an HTTP client that automatically propagates
// trace context to downstream services via W3C TraceContext headers.
//
// When making HTTP requests with this client, the traceparent and tracestate
// headers are automatically injected, allowing downstream services to
// continue the distributed trace.
//
// Parameters:
//   - baseTransport: The underlying transport to use. If nil, uses http.DefaultTransport.
//
// The returned client is safe to use concurrently and should be reused
// across requests for connection pooling benefits.
//
// Example:
//
//	// Create client once, reuse for all requests
//	client := telemetry.NewTracedHTTPClient(nil)
//
//	// Context carries trace information
//	ctx := r.Context()  // From incoming request handler
//
//	// Make request - trace context is automatically propagated
//	req, _ := http.NewRequestWithContext(ctx, "POST", toolURL, body)
//	resp, err := client.Do(req)
func NewTracedHTTPClient(baseTransport http.RoundTripper) *http.Client {
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}

	return &http.Client{
		Transport: otelhttp.NewTransport(baseTransport),
	}
}

// NewTracedHTTPClientWithTransport creates a traced HTTP client with a custom transport.
//
// This is a convenience function that creates a traced client with connection
// pooling configured for service-to-service communication.
//
// Parameters:
//   - transport: Custom transport configuration. If nil, creates a default pooled transport.
//
// Example:
//
//	// Create with custom transport settings
//	transport := &http.Transport{
//	    MaxIdleConns:        100,
//	    MaxIdleConnsPerHost: 10,
//	    IdleConnTimeout:     90 * time.Second,
//	}
//	client := telemetry.NewTracedHTTPClientWithTransport(transport)
func NewTracedHTTPClientWithTransport(transport *http.Transport) *http.Client {
	if transport == nil {
		transport = &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DisableKeepAlives:   false,
			ForceAttemptHTTP2:   true,
		}
	}

	return &http.Client{
		Transport: otelhttp.NewTransport(transport),
	}
}
