package orchestration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSmartExecutor_PropagatedHeaders(t *testing.T) {
	t.Run("context headers applied to outbound request", func(t *testing.T) {
		var capturedTenantID, capturedCorrID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			capturedCorrID = r.Header.Get("X-Correlation-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)

		ctx := WithPropagatedHeaders(context.Background(), map[string]string{
			"X-Tenant-ID":      "ctx-tenant",
			"X-Correlation-ID": "ctx-corr",
		})
		_, _, err := executor.callComponentWithBody(ctx, server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "ctx-tenant" {
			t.Errorf("Expected ctx-tenant, got %q", capturedTenantID)
		}
		if capturedCorrID != "ctx-corr" {
			t.Errorf("Expected ctx-corr, got %q", capturedCorrID)
		}
	})

	t.Run("context headers override config headers on key conflict", func(t *testing.T) {
		var capturedTenantID, capturedExtra string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			capturedExtra = r.Header.Get("X-Extra")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)
		executor.SetPropagatedHeaders(map[string]string{
			"X-Tenant-ID": "config-tenant",
			"X-Extra":     "config-extra",
		})

		ctx := WithPropagatedHeaders(context.Background(), map[string]string{
			"X-Tenant-ID": "ctx-tenant-override",
		})
		_, _, err := executor.callComponentWithBody(ctx, server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "ctx-tenant-override" {
			t.Errorf("Expected ctx-tenant-override, got %q", capturedTenantID)
		}
		// Config-only header still present
		if capturedExtra != "config-extra" {
			t.Errorf("Expected config-extra, got %q", capturedExtra)
		}
	})

	t.Run("config headers used when context has none", func(t *testing.T) {
		var capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)
		executor.SetPropagatedHeaders(map[string]string{
			"X-Tenant-ID": "config-tenant",
		})

		ctx := context.Background()
		_, _, err := executor.callComponentWithBody(ctx, server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "config-tenant" {
			t.Errorf("Expected config-tenant, got %q", capturedTenantID)
		}
	})

	t.Run("no headers when neither context nor config has them", func(t *testing.T) {
		var capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)
		// No SetPropagatedHeaders call

		ctx := context.Background()
		_, _, err := executor.callComponentWithBody(ctx, server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "" {
			t.Errorf("Expected empty X-Tenant-ID, got %q", capturedTenantID)
		}
	})

	t.Run("reserved headers are not overridden by config propagation", func(t *testing.T) {
		var capturedAuth, capturedContentType, capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			capturedContentType = r.Header.Get("Content-Type")
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)
		executor.SetOAuthToken("correct-token")
		executor.SetPropagatedHeaders(map[string]string{
			"Authorization": "Bearer evil-token",
			"Content-Type":  "text/plain",
			"X-Tenant-ID":   "tenant-ok",
		})

		_, _, err := executor.callComponentWithBody(context.Background(), server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer correct-token" {
			t.Errorf("Expected Bearer correct-token (OAuth), got %q", capturedAuth)
		}
		if capturedContentType != "application/json" {
			t.Errorf("Expected application/json, got %q", capturedContentType)
		}
		if capturedTenantID != "tenant-ok" {
			t.Errorf("Expected tenant-ok (non-reserved), got %q", capturedTenantID)
		}
	})

	t.Run("reserved headers are not overridden by context propagation", func(t *testing.T) {
		var capturedAuth, capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)
		executor.SetOAuthToken("correct-token")

		ctx := WithPropagatedHeaders(context.Background(), map[string]string{
			"authorization": "Bearer evil-token", // lowercase — should still be blocked
			"X-Tenant-ID":   "tenant-ok",
		})
		_, _, err := executor.callComponentWithBody(ctx, server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer correct-token" {
			t.Errorf("Expected Bearer correct-token (OAuth), got %q", capturedAuth)
		}
		if capturedTenantID != "tenant-ok" {
			t.Errorf("Expected tenant-ok (non-reserved), got %q", capturedTenantID)
		}
	})
}
