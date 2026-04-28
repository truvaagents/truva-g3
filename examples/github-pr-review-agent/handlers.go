package main

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// registerCapabilities registers only the discovery-visible internal endpoints.
// The task API endpoints (/api/v1/tasks*) are wired via agent.HandleFunc in
// main.go per the async orchestration guide — not as capabilities.
func (a *PRReviewAgent) registerCapabilities() {
	a.RegisterCapability(core.Capability{
		Name:        "github_webhook",
		Description: "Internal endpoint for GitHub pull_request webhooks.",
		Endpoint:    "/webhook/github",
		Handler:     a.handleGitHubWebhook,
		Internal:    true,
	})

	a.RegisterCapability(core.Capability{
		Name:        "health",
		Description: "Internal health endpoint.",
		Endpoint:    "/health",
		Handler:     a.handleHealth,
		Internal:    true,
	})
}

func (a *PRReviewAgent) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := map[string]interface{}{
		"status":    "healthy",
		"operation": "health",
		"mode":      defaultString(a.Config.Mode, "embedded"),
		"redis":     a.pingRedis(ctx),
		"ai":        a.AI != nil,
		"workers":   a.Config.WorkerCount,
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *PRReviewAgent) pingRedis(ctx context.Context) string {
	if a.RedisClient == nil {
		return "unconfigured"
	}
	if err := a.RedisClient.Ping(ctx).Err(); err != nil {
		return "error"
	}
	return "ok"
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// --- Cross-handler helpers ---------------------------------------------------
// These mirror the github-tool pattern (gold standard from the tracing+logging
// guide audit). Kept agent-local so the agent's helpers can evolve
// independently of the tool's.

// requestID returns the cross-component correlation ID. Resolution order:
//  1. W3C baggage member "request_id" (preferred — survives gRPC + HTTP hops).
//  2. X-TruvaG3-Request-ID header (legacy / direct callers).
//  3. OTel trace ID (last-ditch correlation; always available when traced).
func requestID(r *http.Request) string {
	if bag := telemetry.GetBaggage(r.Context()); bag != nil {
		if id := bag["request_id"]; id != "" {
			return id
		}
	}
	if id := r.Header.Get("X-TruvaG3-Request-ID"); id != "" {
		return id
	}
	return telemetry.GetTraceContext(r.Context()).TraceID
}

// markReceived emits the request_received span event AND sets baseline span
// attributes (truvag3.agent.name + truvag3.capability + request_id) so every
// agent span is searchable in Jaeger by agent/capability/request_id without
// per-handler boilerplate.
func markReceived(ctx context.Context, capability, reqID string) {
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.agent.name", "github-pr-review-agent"),
		attribute.String("truvag3.capability", capability),
		attribute.String("request_id", reqID),
	)
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", reqID),
		attribute.String("truvag3.capability", capability),
	)
}

// recordWebhookError consolidates the three things that must happen on every
// webhook validation/decode failure: span error, structured log, HTTP response.
// (Worker-side handlers don't write HTTP responses — they just return error
// from the task handler — so they don't use this helper.)
func (a *PRReviewAgent) recordWebhookError(ctx context.Context, w http.ResponseWriter,
	operation, errType, message string, status int, reqID string, err error) {
	telemetry.RecordSpanError(ctx, err)
	a.Logger.WarnWithContext(ctx, operation+": "+errType, map[string]interface{}{
		"operation":  operation,
		"error_type": errType,
		"error":      err.Error(),
		"request_id": reqID,
	})
	http.Error(w, message, status)
}
