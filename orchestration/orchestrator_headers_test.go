package orchestration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPropagatedHeadersContextHelpers(t *testing.T) {
	t.Run("WithPropagatedHeaders and GetPropagatedHeaders round-trip", func(t *testing.T) {
		headers := map[string]string{"X-Tenant-ID": "tenant-1", "X-Correlation-ID": "corr-abc"}
		ctx := WithPropagatedHeaders(context.Background(), headers)
		got := GetPropagatedHeaders(ctx)
		if got == nil {
			t.Fatal("Expected non-nil headers")
		}
		if got["X-Tenant-ID"] != "tenant-1" {
			t.Errorf("Expected tenant-1, got %q", got["X-Tenant-ID"])
		}
		if got["X-Correlation-ID"] != "corr-abc" {
			t.Errorf("Expected corr-abc, got %q", got["X-Correlation-ID"])
		}
	})

	t.Run("GetPropagatedHeaders returns nil for plain context", func(t *testing.T) {
		if got := GetPropagatedHeaders(context.Background()); got != nil {
			t.Errorf("Expected nil, got %v", got)
		}
	})

	t.Run("GetPropagatedHeaders is nil-safe", func(t *testing.T) {
		if got := GetPropagatedHeaders(context.TODO()); got != nil {
			t.Errorf("Expected nil for nil context, got %v", got)
		}
	})

	t.Run("WithPropagatedHeaders skips empty map", func(t *testing.T) {
		ctx := WithPropagatedHeaders(context.Background(), map[string]string{})
		if got := GetPropagatedHeaders(ctx); got != nil {
			t.Errorf("Expected nil for empty map, got %v", got)
		}
	})

	t.Run("WithPropagatedHeaders skips nil map", func(t *testing.T) {
		ctx := WithPropagatedHeaders(context.Background(), nil)
		if got := GetPropagatedHeaders(ctx); got != nil {
			t.Errorf("Expected nil for nil map, got %v", got)
		}
	})

	t.Run("AddPropagatedHeader to empty context", func(t *testing.T) {
		ctx := AddPropagatedHeader(context.Background(), "X-Tenant-ID", "tenant-1")
		got := GetPropagatedHeaders(ctx)
		if got == nil || got["X-Tenant-ID"] != "tenant-1" {
			t.Errorf("Expected X-Tenant-ID=tenant-1, got %v", got)
		}
	})

	t.Run("AddPropagatedHeader merges with existing", func(t *testing.T) {
		ctx := WithPropagatedHeaders(context.Background(), map[string]string{"X-Existing": "val1"})
		ctx = AddPropagatedHeader(ctx, "X-New", "val2")
		got := GetPropagatedHeaders(ctx)
		if got["X-Existing"] != "val1" {
			t.Errorf("Expected X-Existing=val1, got %q", got["X-Existing"])
		}
		if got["X-New"] != "val2" {
			t.Errorf("Expected X-New=val2, got %q", got["X-New"])
		}
	})

	t.Run("AddPropagatedHeader skips empty key", func(t *testing.T) {
		ctx := AddPropagatedHeader(context.Background(), "", "value")
		if got := GetPropagatedHeaders(ctx); got != nil {
			t.Errorf("Expected nil for empty key, got %v", got)
		}
	})
}

func TestIsReservedPropagationHeader(t *testing.T) {
	reserved := []string{
		"Authorization", "authorization", "AUTHORIZATION",
		"Content-Type", "content-type",
		"X-Truvag3-Request-Id", "x-truvag3-request-id",
		"X-Truvag3-Original-Request-Id",
		"X-Truvag3-Step-Id",
		"X-Truvag3-Phase-Number",
		"X-Truvag3-Plan-Id", "x-truvag3-plan-id",
		"X-Truvag3-Agent-Name",
		"X-Workflow-Id", "x-workflow-id",
		"X-Step-Id",
	}
	for _, h := range reserved {
		t.Run("reserved_"+h, func(t *testing.T) {
			if !isReservedPropagationHeader(h) {
				t.Errorf("Expected %q to be reserved", h)
			}
		})
	}

	allowed := []string{
		"X-Tenant-ID",
		"X-Correlation-ID",
		"X-Custom-Header",
		"Accept",
	}
	for _, h := range allowed {
		t.Run("allowed_"+h, func(t *testing.T) {
			if isReservedPropagationHeader(h) {
				t.Errorf("Expected %q to be allowed", h)
			}
		})
	}
}

func TestNewAIOrchestrator_PropagatedHeadersConfigWiring(t *testing.T) {
	t.Run("config PropagatedHeaders wired to executor", func(t *testing.T) {
		var capturedTenantID, capturedCorrID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			capturedCorrID = r.Header.Get("X-Correlation-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		config := DefaultConfig()
		config.PropagatedHeaders = map[string]string{
			"X-Tenant-ID":      "tenant-from-config",
			"X-Correlation-ID": "corr-from-config",
		}

		discovery := NewMockDiscovery()
		aiClient := NewMockAIClient()

		orch := NewAIOrchestrator(config, discovery, aiClient)

		_, _, err := orch.executor.callComponentWithBody(context.Background(), server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "tenant-from-config" {
			t.Errorf("Expected tenant-from-config, got %q", capturedTenantID)
		}
		if capturedCorrID != "corr-from-config" {
			t.Errorf("Expected corr-from-config, got %q", capturedCorrID)
		}
	})

	t.Run("empty config PropagatedHeaders does not set headers", func(t *testing.T) {
		var capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		config := DefaultConfig()
		// PropagatedHeaders left nil (default)

		discovery := NewMockDiscovery()
		aiClient := NewMockAIClient()

		orch := NewAIOrchestrator(config, discovery, aiClient)

		_, _, err := orch.executor.callComponentWithBody(context.Background(), server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "" {
			t.Errorf("Expected empty X-Tenant-ID, got %q", capturedTenantID)
		}
	})
}

func TestAIOrchestrator_SetPropagatedHeaders(t *testing.T) {
	t.Run("SetPropagatedHeaders delegates to executor", func(t *testing.T) {
		var capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		config := DefaultConfig()
		discovery := NewMockDiscovery()
		aiClient := NewMockAIClient()

		orch := NewAIOrchestrator(config, discovery, aiClient)
		orch.SetPropagatedHeaders(map[string]string{"X-Tenant-ID": "runtime-tenant"})

		_, _, err := orch.executor.callComponentWithBody(context.Background(), server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "runtime-tenant" {
			t.Errorf("Expected runtime-tenant, got %q", capturedTenantID)
		}
	})
}
