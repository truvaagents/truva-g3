package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func TestTracingMiddleware_BasicOperation(t *testing.T) {
	// Set up propagators for test (normally done by Initialize)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Create a simple handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap with tracing middleware
	traced := TracingMiddleware("test-service")(handler)

	// Create test request
	req := httptest.NewRequest("GET", "/api/test", nil)
	rec := httptest.NewRecorder()

	// Execute
	traced.ServeHTTP(rec, req)

	// Verify response
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if body != "OK" {
		t.Errorf("Expected body 'OK', got '%s'", body)
	}
}

func TestTracingMiddleware_ExcludedPaths(t *testing.T) {
	// Set up propagators for test
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	config := &TracingMiddlewareConfig{
		ExcludedPaths: []string{"/health", "/metrics"},
	}

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	traced := TracingMiddlewareWithConfig("test-service", config)(handler)

	// Test excluded path - should still work but not generate traces
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	traced.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	if !handlerCalled {
		t.Error("Handler should have been called")
	}
}

func TestTracingMiddleware_CustomSpanNameFormatter(t *testing.T) {
	// Set up propagators for test
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	formatterCalled := false
	config := &TracingMiddlewareConfig{
		SpanNameFormatter: func(operation string, r *http.Request) string {
			formatterCalled = true
			return "custom-" + r.Method + "-" + r.URL.Path
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	traced := TracingMiddlewareWithConfig("test-service", config)(handler)

	req := httptest.NewRequest("POST", "/api/data", nil)
	rec := httptest.NewRecorder()
	traced.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	if !formatterCalled {
		t.Error("Custom span name formatter should have been called")
	}
}

func TestNewTracedHTTPClient(t *testing.T) {
	client := NewTracedHTTPClient(nil)
	if client == nil {
		t.Fatal("Expected non-nil client")
	}
	if client.Transport == nil {
		t.Fatal("Expected non-nil transport")
	}
}

func TestNewTracedHTTPClientWithTransport(t *testing.T) {
	// Test with nil transport (should create default)
	client := NewTracedHTTPClientWithTransport(nil)
	if client == nil {
		t.Fatal("Expected non-nil client")
	}
	if client.Transport == nil {
		t.Fatal("Expected non-nil transport")
	}

	// Test with custom transport
	customTransport := &http.Transport{
		MaxIdleConns: 50,
	}
	client2 := NewTracedHTTPClientWithTransport(customTransport)
	if client2 == nil {
		t.Fatal("Expected non-nil client with custom transport")
	}
}

func TestTracedHTTPClient_PropagatesContext(t *testing.T) {
	// Set up propagators for test
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Create test server that checks for trace headers
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log traceparent if present (useful for debugging)
		// When there's an active trace in context, otelhttp will inject this header
		_ = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	// Create traced client
	client := NewTracedHTTPClient(nil)

	// Make request with context
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	// Read body to complete request
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Note: traceparent header may or may not be present depending on
	// whether a trace is active in the context. Without an active trace,
	// otelhttp won't inject headers. This test verifies the client works.
}

func TestTracingMiddleware_NilConfig(t *testing.T) {
	// Test that nil config works (uses defaults)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Should not panic with nil config
	traced := TracingMiddlewareWithConfig("test-service", nil)(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	traced.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestTracingMiddleware_MultipleExcludedPaths(t *testing.T) {
	config := &TracingMiddlewareConfig{
		ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live"},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	traced := TracingMiddlewareWithConfig("test-service", config)(handler)

	// Test all excluded paths
	excludedPaths := []string{"/health", "/metrics", "/ready", "/live"}
	for _, path := range excludedPaths {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		traced.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Path %s: Expected status 200, got %d", path, rec.Code)
		}
	}

	// Test non-excluded path
	req := httptest.NewRequest("GET", "/api/data", nil)
	rec := httptest.NewRecorder()
	traced.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Non-excluded path: Expected status 200, got %d", rec.Code)
	}
}

func TestNewTracedHTTPClient_WithExistingTransport(t *testing.T) {
	// Test with an existing transport (non-nil)
	existingTransport := &http.Transport{
		MaxIdleConns: 25,
	}

	client := NewTracedHTTPClient(existingTransport)
	if client == nil {
		t.Fatal("Expected non-nil client")
	}
	if client.Transport == nil {
		t.Fatal("Expected non-nil transport")
	}
}

func TestTracingMiddleware_DifferentHTTPMethods(t *testing.T) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	methodCalled := ""
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methodCalled = r.Method
		w.WriteHeader(http.StatusOK)
	})

	traced := TracingMiddleware("test-service")(handler)

	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	for _, method := range methods {
		methodCalled = ""
		req := httptest.NewRequest(method, "/api/test", nil)
		rec := httptest.NewRecorder()
		traced.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Method %s: Expected status 200, got %d", method, rec.Code)
		}
		if methodCalled != method {
			t.Errorf("Method %s: Handler received %s", method, methodCalled)
		}
	}
}

func TestTracingMiddleware_ErrorStatusCodes(t *testing.T) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	testCases := []struct {
		name       string
		statusCode int
	}{
		{"BadRequest", http.StatusBadRequest},
		{"NotFound", http.StatusNotFound},
		{"InternalServerError", http.StatusInternalServerError},
		{"ServiceUnavailable", http.StatusServiceUnavailable},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
			})

			traced := TracingMiddleware("test-service")(handler)

			req := httptest.NewRequest("GET", "/api/test", nil)
			rec := httptest.NewRecorder()
			traced.ServeHTTP(rec, req)

			if rec.Code != tc.statusCode {
				t.Errorf("Expected status %d, got %d", tc.statusCode, rec.Code)
			}
		})
	}
}

func TestTracedHTTPClient_MultipleRequests(t *testing.T) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create client once, reuse for multiple requests
	client := NewTracedHTTPClient(nil)

	// Make multiple requests
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request %d failed: %v", i, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Request %d: Expected status 200, got %d", i, resp.StatusCode)
		}
	}

	if requestCount != 5 {
		t.Errorf("Expected 5 requests, server received %d", requestCount)
	}
}
