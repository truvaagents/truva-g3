package providers

import (
	"net/http"
	"testing"
)

func TestMergeAnyMaps_RequestOverridesDefault(t *testing.T) {
	merged := MergeAnyMaps(
		map[string]interface{}{"temperature": 0.3, "foo": "default"},
		map[string]interface{}{"foo": "request", "bar": true},
	)

	if merged["foo"] != "request" {
		t.Fatalf("expected request value to win, got %v", merged["foo"])
	}
	if merged["bar"] != true {
		t.Fatalf("expected request-only key to be preserved")
	}
}

func TestMergeStringMaps_RequestOverridesDefault(t *testing.T) {
	merged := MergeStringMaps(
		map[string]string{"x-default": "a", "x-shared": "default"},
		map[string]string{"x-shared": "request", "x-request": "b"},
	)

	if merged["x-shared"] != "request" {
		t.Fatalf("expected request value to win, got %q", merged["x-shared"])
	}
	if merged["x-request"] != "b" {
		t.Fatalf("expected request-only key to be preserved, got %q", merged["x-request"])
	}
}

func TestApplyHeaders_ProtectedHeadersWin(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	ApplyHeaders(req, map[string]struct{}{
		"content-type": {},
	}, map[string]string{"X-Default": "a"}, map[string]string{
		"Content-Type": "text/plain",
		"X-Default":    "b",
		"X-Request":    "c",
	})

	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected protected content-type to stay unchanged, got %q", got)
	}
	if got := req.Header.Get("X-Default"); got != "b" {
		t.Fatalf("expected request header to override default header, got %q", got)
	}
	if got := req.Header.Get("X-Request"); got != "c" {
		t.Fatalf("expected request header to be applied, got %q", got)
	}
}
