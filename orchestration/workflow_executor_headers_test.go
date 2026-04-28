package orchestration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestWorkflowExecutor_PropagatedHeaders_CallService(t *testing.T) {
	t.Run("context headers applied to CallService", func(t *testing.T) {
		var capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		executor := &WorkflowExecutor{client: client}

		ctx := WithPropagatedHeaders(context.Background(), map[string]string{
			"X-Tenant-ID": "ctx-tenant",
		})
		_, err := executor.CallService(ctx, svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "ctx-tenant" {
			t.Errorf("Expected ctx-tenant, got %q", capturedTenantID)
		}
	})

	t.Run("context headers override config headers in CallService", func(t *testing.T) {
		var capturedTenantID, capturedExtra string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			capturedExtra = r.Header.Get("X-Extra")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetPropagatedHeaders(map[string]string{
			"X-Tenant-ID": "config-tenant",
			"X-Extra":     "config-extra",
		})
		executor := &WorkflowExecutor{client: client}

		ctx := WithPropagatedHeaders(context.Background(), map[string]string{
			"X-Tenant-ID": "ctx-override",
		})
		_, err := executor.CallService(ctx, svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "ctx-override" {
			t.Errorf("Expected ctx-override, got %q", capturedTenantID)
		}
		if capturedExtra != "config-extra" {
			t.Errorf("Expected config-extra, got %q", capturedExtra)
		}
	})

	t.Run("config headers used when context has none in CallService", func(t *testing.T) {
		var capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetPropagatedHeaders(map[string]string{
			"X-Tenant-ID": "config-tenant",
		})
		executor := &WorkflowExecutor{client: client}

		_, err := executor.CallService(context.Background(), svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "config-tenant" {
			t.Errorf("Expected config-tenant, got %q", capturedTenantID)
		}
	})
}

func TestWorkflowExecutor_PropagatedHeaders_HealthCheck(t *testing.T) {
	t.Run("context headers applied to HealthCheck", func(t *testing.T) {
		var capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		executor := &WorkflowExecutor{client: client}

		ctx := WithPropagatedHeaders(context.Background(), map[string]string{
			"X-Tenant-ID": "ctx-tenant",
		})
		healthy := executor.HealthCheck(ctx, svc)
		if !healthy {
			t.Error("Expected healthy response")
		}
		if capturedTenantID != "ctx-tenant" {
			t.Errorf("Expected ctx-tenant, got %q", capturedTenantID)
		}
	})

	t.Run("config headers applied to HealthCheck", func(t *testing.T) {
		var capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetPropagatedHeaders(map[string]string{
			"X-Tenant-ID": "config-tenant",
		})
		executor := &WorkflowExecutor{client: client}

		healthy := executor.HealthCheck(context.Background(), svc)
		if !healthy {
			t.Error("Expected healthy response")
		}
		if capturedTenantID != "config-tenant" {
			t.Errorf("Expected config-tenant, got %q", capturedTenantID)
		}
	})
}

func TestWorkflowExecutor_ReservedHeaders_CallService(t *testing.T) {
	t.Run("reserved headers are not overridden by config propagation in CallService", func(t *testing.T) {
		var capturedAuth, capturedContentType, capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			capturedContentType = r.Header.Get("Content-Type")
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetOAuthToken("correct-token")
		client.SetPropagatedHeaders(map[string]string{
			"Authorization": "Bearer evil-token",
			"Content-Type":  "text/plain",
			"X-Tenant-ID":   "tenant-ok",
		})
		executor := &WorkflowExecutor{client: client}

		_, err := executor.CallService(context.Background(), svc, "", map[string]interface{}{"key": "value"})
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

	t.Run("reserved headers are not overridden by context propagation in CallService", func(t *testing.T) {
		var capturedAuth, capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetOAuthToken("correct-token")
		executor := &WorkflowExecutor{client: client}

		ctx := WithPropagatedHeaders(context.Background(), map[string]string{
			"authorization": "Bearer evil-token", // lowercase — should still be blocked
			"X-Tenant-ID":   "tenant-ok",
		})
		_, err := executor.CallService(ctx, svc, "", map[string]interface{}{"key": "value"})
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

func TestWorkflowExecutor_ReservedHeaders_HealthCheck(t *testing.T) {
	t.Run("reserved headers are not overridden by config propagation in HealthCheck", func(t *testing.T) {
		var capturedAuth, capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetOAuthToken("correct-token")
		client.SetPropagatedHeaders(map[string]string{
			"Authorization": "Bearer evil-token",
			"X-Tenant-ID":   "tenant-ok",
		})
		executor := &WorkflowExecutor{client: client}

		healthy := executor.HealthCheck(context.Background(), svc)
		if !healthy {
			t.Error("Expected healthy response")
		}
		if capturedAuth != "Bearer correct-token" {
			t.Errorf("Expected Bearer correct-token (OAuth), got %q", capturedAuth)
		}
		if capturedTenantID != "tenant-ok" {
			t.Errorf("Expected tenant-ok (non-reserved), got %q", capturedTenantID)
		}
	})

	t.Run("reserved headers are not overridden by context propagation in HealthCheck", func(t *testing.T) {
		var capturedAuth, capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetOAuthToken("correct-token")
		executor := &WorkflowExecutor{client: client}

		ctx := WithPropagatedHeaders(context.Background(), map[string]string{
			"authorization": "Bearer evil-token",
			"X-Tenant-ID":   "tenant-ok",
		})
		healthy := executor.HealthCheck(ctx, svc)
		if !healthy {
			t.Error("Expected healthy response")
		}
		if capturedAuth != "Bearer correct-token" {
			t.Errorf("Expected Bearer correct-token (OAuth), got %q", capturedAuth)
		}
		if capturedTenantID != "tenant-ok" {
			t.Errorf("Expected tenant-ok (non-reserved), got %q", capturedTenantID)
		}
	})
}

func TestWorkflowEngine_SetPropagatedHeaders(t *testing.T) {
	t.Run("SetPropagatedHeaders propagates to executor client", func(t *testing.T) {
		var capturedTenantID string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedTenantID = r.Header.Get("X-Tenant-ID")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		engine := NewWorkflowEngine(nil, nil, nil)
		engine.SetPropagatedHeaders(map[string]string{"X-Tenant-ID": "engine-tenant"})

		_, err := engine.executor.CallService(context.Background(), svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedTenantID != "engine-tenant" {
			t.Errorf("Expected engine-tenant, got %q", capturedTenantID)
		}
	})
}
