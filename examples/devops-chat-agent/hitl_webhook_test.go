package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHITLWebhookCapability(t *testing.T) {
	capability := hitlWebhookCapability()
	if capability.Endpoint != "/internal/hitl-webhook" || !capability.Internal {
		t.Fatal("webhook must use the internal callback route")
	}
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			capability.Handler(response, httptest.NewRequest(method, capability.Endpoint, nil))
			if method == http.MethodPost {
				if response.Code != http.StatusOK || response.Body.String() != `{"status":"received"}` {
					t.Fatalf("unexpected acknowledgment: %d %s", response.Code, response.Body.String())
				}
			} else if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("non-POST request accepted: %d", response.Code)
			}
		})
	}
}
