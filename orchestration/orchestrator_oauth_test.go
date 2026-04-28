package orchestration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOAuthTokenContextHelpers(t *testing.T) {
	t.Run("WithOAuthToken and GetOAuthToken round-trip", func(t *testing.T) {
		ctx := WithOAuthToken(context.Background(), "test-token")
		if got := GetOAuthToken(ctx); got != "test-token" {
			t.Errorf("Expected test-token, got %q", got)
		}
	})

	t.Run("GetOAuthToken returns empty for plain context", func(t *testing.T) {
		if got := GetOAuthToken(context.Background()); got != "" {
			t.Errorf("Expected empty string, got %q", got)
		}
	})

	t.Run("GetOAuthToken is nil-safe", func(t *testing.T) {
		if got := GetOAuthToken(context.TODO()); got != "" {
			t.Errorf("Expected empty string for nil context, got %q", got)
		}
	})

	t.Run("WithOAuthToken skips empty token", func(t *testing.T) {
		ctx := WithOAuthToken(context.Background(), "")
		if got := GetOAuthToken(ctx); got != "" {
			t.Errorf("Expected empty string for empty token, got %q", got)
		}
	})
}

func TestNewAIOrchestrator_OAuthConfigWiring(t *testing.T) {
	t.Run("config OAuthToken wired to executor", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		config := DefaultConfig()
		config.OAuthToken = "config-wired-token"

		discovery := NewMockDiscovery()
		aiClient := NewMockAIClient()

		orch := NewAIOrchestrator(config, discovery, aiClient)

		// Verify the token was wired through to the executor by making a direct call
		_, _, err := orch.executor.callComponentWithBody(context.Background(), server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer config-wired-token" {
			t.Errorf("Expected Bearer config-wired-token, got %q", capturedAuth)
		}
	})

	t.Run("empty config OAuthToken does not set executor token", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		config := DefaultConfig()
		// OAuthToken left empty (default)

		discovery := NewMockDiscovery()
		aiClient := NewMockAIClient()

		orch := NewAIOrchestrator(config, discovery, aiClient)

		_, _, err := orch.executor.callComponentWithBody(context.Background(), server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "" {
			t.Errorf("Expected empty Authorization header, got %q", capturedAuth)
		}
	})
}

func TestAIOrchestrator_SetOAuthToken(t *testing.T) {
	t.Run("SetOAuthToken delegates to executor", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		config := DefaultConfig()
		discovery := NewMockDiscovery()
		aiClient := NewMockAIClient()

		orch := NewAIOrchestrator(config, discovery, aiClient)
		orch.SetOAuthToken("refreshed-m2m-token")

		_, _, err := orch.executor.callComponentWithBody(context.Background(), server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer refreshed-m2m-token" {
			t.Errorf("Expected Bearer refreshed-m2m-token, got %q", capturedAuth)
		}
	})

	t.Run("SetOAuthToken overwrites initial config token", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		config := DefaultConfig()
		config.OAuthToken = "initial-token"

		discovery := NewMockDiscovery()
		aiClient := NewMockAIClient()

		orch := NewAIOrchestrator(config, discovery, aiClient)
		// Simulate M2M token refresh overwriting the initial token
		orch.SetOAuthToken("refreshed-token")

		_, _, err := orch.executor.callComponentWithBody(context.Background(), server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer refreshed-token" {
			t.Errorf("Expected Bearer refreshed-token, got %q", capturedAuth)
		}
	})
}
