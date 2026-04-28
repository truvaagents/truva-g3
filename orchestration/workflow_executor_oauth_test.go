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

func TestWorkflowExecutor_OAuthToken_CallService(t *testing.T) {
	t.Run("context token injected in CallService", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		executor := &WorkflowExecutor{client: client}

		ctx := WithOAuthToken(context.Background(), "user-token")
		_, err := executor.CallService(ctx, svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer user-token" {
			t.Errorf("Expected Bearer user-token, got %q", capturedAuth)
		}
	})

	t.Run("config token used when no context token", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetOAuthToken("m2m-token")
		executor := &WorkflowExecutor{client: client}

		ctx := context.Background()
		_, err := executor.CallService(ctx, svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer m2m-token" {
			t.Errorf("Expected Bearer m2m-token, got %q", capturedAuth)
		}
	})

	t.Run("no header when neither context nor config has token", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		executor := &WorkflowExecutor{client: client}

		ctx := context.Background()
		_, err := executor.CallService(ctx, svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "" {
			t.Errorf("Expected empty Authorization header, got %q", capturedAuth)
		}
	})
}

func TestWorkflowExecutor_OAuthToken_HealthCheck(t *testing.T) {
	t.Run("config token injected in HealthCheck", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetOAuthToken("health-check-token")
		executor := &WorkflowExecutor{client: client}

		healthy := executor.HealthCheck(context.Background(), svc)
		if !healthy {
			t.Error("Expected healthy response")
		}
		if capturedAuth != "Bearer health-check-token" {
			t.Errorf("Expected Bearer health-check-token, got %q", capturedAuth)
		}
	})

	t.Run("context token takes priority in HealthCheck", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		client.SetOAuthToken("config-token")
		executor := &WorkflowExecutor{client: client}

		ctx := WithOAuthToken(context.Background(), "ctx-token")
		healthy := executor.HealthCheck(ctx, svc)
		if !healthy {
			t.Error("Expected healthy response")
		}
		if capturedAuth != "Bearer ctx-token" {
			t.Errorf("Expected Bearer ctx-token, got %q", capturedAuth)
		}
	})
}

func TestNewWorkflowHTTPClient_EnvVarAutoLoad(t *testing.T) {
	t.Run("TRUVAG3_OAUTH_TOKEN loaded from env at construction", func(t *testing.T) {
		t.Setenv("TRUVAG3_OAUTH_TOKEN", "env-auto-token")

		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		// No explicit SetOAuthToken — relies on constructor env var loading
		client := NewWorkflowHTTPClient()
		executor := &WorkflowExecutor{client: client}

		_, err := executor.CallService(context.Background(), svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer env-auto-token" {
			t.Errorf("Expected Bearer env-auto-token, got %q", capturedAuth)
		}
	})

	t.Run("no env var means no auto-loaded token", func(t *testing.T) {
		t.Setenv("TRUVAG3_OAUTH_TOKEN", "")

		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		client := NewWorkflowHTTPClient()
		executor := &WorkflowExecutor{client: client}

		_, err := executor.CallService(context.Background(), svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "" {
			t.Errorf("Expected empty Authorization header, got %q", capturedAuth)
		}
	})
}

func TestWorkflowEngine_SetOAuthToken(t *testing.T) {
	t.Run("SetOAuthToken propagates to executor client", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		engine := NewWorkflowEngine(nil, nil, nil)
		engine.SetOAuthToken("engine-token")

		// Access the internal executor to make a direct CallService call
		_, err := engine.executor.CallService(context.Background(), svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer engine-token" {
			t.Errorf("Expected Bearer engine-token, got %q", capturedAuth)
		}
	})

	t.Run("SetOAuthToken overrides env var auto-loaded token", func(t *testing.T) {
		t.Setenv("TRUVAG3_OAUTH_TOKEN", "env-token")

		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			w.Write([]byte(`{"result":"ok"}`))
		}))
		defer server.Close()

		serverURL, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(serverURL.Port())
		svc := &core.ServiceRegistration{Address: serverURL.Hostname(), Port: port}

		engine := NewWorkflowEngine(nil, nil, nil)
		engine.SetOAuthToken("override-token")

		_, err := engine.executor.CallService(context.Background(), svc, "", map[string]interface{}{"key": "value"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer override-token" {
			t.Errorf("Expected Bearer override-token, got %q", capturedAuth)
		}
	})
}
