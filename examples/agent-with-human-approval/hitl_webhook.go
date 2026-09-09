package main

import (
	"net/http"

	"github.com/truvaagents/truva-g3/core"
)

// Checkpoints are persisted before notification. These examples expose them
// through SSE/polling; acknowledging delivery does not approve or resume work.
func hitlWebhookCapability() core.Capability {
	return core.Capability{
		Name: "hitl_webhook", Description: "Acknowledge a persisted HITL checkpoint notification",
		Endpoint: "/internal/hitl-webhook", Internal: true,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"received"}`))
		},
	}
}
