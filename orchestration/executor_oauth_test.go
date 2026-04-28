package orchestration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSmartExecutor_OAuthToken_ContextPriority(t *testing.T) {
	t.Run("context token takes priority over config token", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)
		executor.SetOAuthToken("m2m-config-token")

		ctx := WithOAuthToken(context.Background(), "user-token-from-ui")
		_, _, err := executor.callComponentWithBody(ctx, server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer user-token-from-ui" {
			t.Errorf("Expected Bearer user-token-from-ui, got %q", capturedAuth)
		}
	})

	t.Run("config token used when context has no token", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)
		executor.SetOAuthToken("m2m-config-token")

		ctx := context.Background()
		_, _, err := executor.callComponentWithBody(ctx, server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer m2m-config-token" {
			t.Errorf("Expected Bearer m2m-config-token, got %q", capturedAuth)
		}
	})

	t.Run("no header when neither context nor config has token", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)
		// No SetOAuthToken call

		ctx := context.Background()
		_, _, err := executor.callComponentWithBody(ctx, server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "" {
			t.Errorf("Expected empty Authorization header, got %q", capturedAuth)
		}
	})

	t.Run("empty context token falls through to config token", func(t *testing.T) {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		executor := NewSmartExecutor(nil)
		executor.SetOAuthToken("m2m-config-token")

		// WithOAuthToken with empty string returns context unchanged
		ctx := WithOAuthToken(context.Background(), "")
		_, _, err := executor.callComponentWithBody(ctx, server.URL+"/process", []byte(`{"query":"test"}`))
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedAuth != "Bearer m2m-config-token" {
			t.Errorf("Expected Bearer m2m-config-token, got %q", capturedAuth)
		}
	})
}
