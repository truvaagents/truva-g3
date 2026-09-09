package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleJaegerTraceRedirectUsesConfiguredBrowserURL(t *testing.T) {
	previousURL := jaegerUIURL
	jaegerUIURL = "https://observability.example.com/jaeger"
	t.Cleanup(func() { jaegerUIURL = previousURL })

	req := httptest.NewRequest(http.MethodGet, "/api/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	rec := httptest.NewRecorder()
	handleJaegerTraceRedirect(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTemporaryRedirect)
	}
	if location := rec.Header().Get("Location"); location != "https://observability.example.com/jaeger/trace/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("Location = %q", location)
	}
}

func TestHandleJaegerTraceRedirectRejectsInvalidInputs(t *testing.T) {
	previousURL := jaegerUIURL
	t.Cleanup(func() { jaegerUIURL = previousURL })

	t.Run("trace ID", func(t *testing.T) {
		jaegerUIURL = "https://observability.example.com"
		req := httptest.NewRequest(http.MethodGet, "/api/traces/not-a-trace", nil)
		rec := httptest.NewRecorder()
		handleJaegerTraceRedirect(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("UI URL", func(t *testing.T) {
		jaegerUIURL = "javascript:alert(1)"
		req := httptest.NewRequest(http.MethodGet, "/api/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
		rec := httptest.NewRecorder()
		handleJaegerTraceRedirect(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}
