package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// registerCapabilities registers all HTTP endpoints.
// ALL capabilities are Internal: true (agent is not externally discoverable as a tool).
func (a *EventDrivenAgent) registerCapabilities() {
	// AlertManager webhook receiver
	a.RegisterCapability(core.Capability{
		Name:        "alertmanager_webhook",
		Description: "Receives AlertManager webhook payloads for automated incident response.",
		Endpoint:    "/webhook/alertmanager",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     a.handleAlertManagerWebhook,
		Internal:    true,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "status", Type: "string", Example: "firing", Description: "Alert group status: 'firing' or 'resolved'"},
				{Name: "alerts", Type: "array", Example: `[{"status":"firing","labels":{"alertname":"HighCPU"}}]`, Description: "Array of alert objects from AlertManager"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "groupLabels", Type: "object", Example: `{"alertname":"HighCPU"}`, Description: "Labels used for grouping alerts"},
				{Name: "commonLabels", Type: "object", Example: `{"severity":"critical"}`, Description: "Labels common to all alerts in the group"},
			},
		},

		// Phase 2b: Output schema — shape written by handleAlertManagerWebhook.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "status", Type: "string", Description: "Processing status: 'accepted' (async enqueued) or 'processed' (handled synchronously)"},
				{Name: "skipped", Type: "number", Example: "0", Description: "Number of alerts skipped (e.g., resolved alerts)"},
				{Name: "warned", Type: "number", Example: "0", Description: "Number of warning-severity alerts routed to Slack"},
				{Name: "logged", Type: "number", Example: "0", Description: "Number of info-severity alerts only logged"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "enqueued", Type: "number", Example: "1", Description: "Number of critical alerts enqueued for AI investigation (only present when status='accepted')"},
			},
		},
	})

	// Manual alert trigger (for testing / CLI integration)
	a.RegisterCapability(core.Capability{
		Name:        "trigger_alert",
		Description: "Manually triggers an alert investigation for testing or CLI integration.",
		Endpoint:    "/trigger",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     a.handleManualTrigger,
		Internal:    true,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "alertname", Type: "string", Example: "HighCPU", Description: "Name of the alert to investigate"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "severity", Type: "string", Example: "critical", Description: "Alert severity: critical, warning, info (default: critical)"},
				{Name: "instance", Type: "string", Example: "web-server-01", Description: "Instance or host that triggered the alert"},
				{Name: "summary", Type: "string", Example: "CPU usage above 90%", Description: "Human-readable summary of the alert"},
			},
		},

		// Phase 2b: Output schema — shape written by handleManualTrigger.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "status", Type: "string", Description: "Overall processing status (always 'ok' on success)"},
				{Name: "alertname", Type: "string", Description: "Echo of the alert name that was submitted"},
				{Name: "severity", Type: "string", Description: "Severity that was applied (defaults to 'critical' if omitted)"},
				{Name: "enqueued", Type: "boolean", Description: "True if the alert was enqueued for AI investigation (critical severity path)"},
				{Name: "fingerprint", Type: "string", Description: "Unique fingerprint assigned to this alert (manual-{alertname}-{nano})"},
			},
		},
	})

	// Event history
	a.RegisterCapability(core.Capability{
		Name:        "event_history",
		Description: "Returns recent alert event history with queue status.",
		Endpoint:    "/events",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     a.handleEventHistory,
		Internal:    true,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "limit", Type: "number", Example: "50", Description: "Maximum number of events to return (default: 50)"},
				{Name: "severity", Type: "string", Example: "critical", Description: "Filter by severity level"},
			},
		},

		// Phase 2b: Output schema — shape written by handleEventHistory.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "queue_depth", Type: "number", Example: "0", Description: "Current number of alerts waiting in the Redis alert_queue"},
				{Name: "timestamp", Type: "number", Example: "1775496209", Description: "Unix timestamp of the query"},
			},
		},
	})

	// Health check
	a.RegisterCapability(core.Capability{
		Name:        "health",
		Description: "Reports agent health including Redis, orchestrator, AI provider, and queue status.",
		Endpoint:    "/health",
		Handler:     a.handleHealth,
		Internal:    true,
	})
}

// handleManualTrigger allows manual alert submission for testing.
// POST /trigger
// Body: {"alertname": "...", "severity": "critical", "instance": "...", "summary": "..."}
func (a *EventDrivenAgent) handleManualTrigger(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Extract trace context for response headers and log correlation
	tc := telemetry.GetTraceContext(ctx)
	w.Header().Set("X-Trace-ID", tc.TraceID)
	w.Header().Set("X-Span-ID", tc.SpanID)

	// Extract upstream request context
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are supported", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Alertname string `json:"alertname"`
		Severity  string `json:"severity"`
		Instance  string `json:"instance"`
		Summary   string `json:"summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		a.Logger.ErrorWithContext(ctx, "Failed to decode manual trigger request", map[string]interface{}{
			"operation": "manual_trigger",
			"error":     err.Error(),
		})
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Alertname == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("alertname is required"))
		http.Error(w, "alertname is required", http.StatusBadRequest)
		return
	}
	if req.Severity == "" {
		req.Severity = "critical"
	}

	telemetry.AddSpanEvent(ctx, "manual_trigger.received",
		attribute.String("alert_name", req.Alertname),
		attribute.String("severity", req.Severity),
	)

	// Construct an Alert from the manual trigger
	alert := Alert{
		Status: "firing",
		Labels: map[string]string{
			"alertname": req.Alertname,
			"severity":  req.Severity,
			"instance":  req.Instance,
		},
		Annotations: map[string]string{
			"summary": req.Summary,
		},
		StartsAt:    time.Now(),
		Fingerprint: fmt.Sprintf("manual-%s-%d", req.Alertname, time.Now().UnixNano()),
	}

	// Route through the same pipeline as webhook alerts
	var enqueued bool
	switch req.Severity {
	case "critical":
		enqueued = a.enqueueCriticalAlert(ctx, alert)
	case "warning":
		a.sendWarningSlackNotification(ctx, alert)
	default:
		a.Logger.InfoWithContext(ctx, "Manual info alert logged", map[string]interface{}{
			"alertname": req.Alertname,
			"operation": "manual_trigger",
		})
	}

	a.Logger.InfoWithContext(ctx, "Manual trigger request completed", map[string]interface{}{
		"operation":   "manual_trigger",
		"request_id":  upstreamRequestID,
		"alertname":   req.Alertname,
		"severity":    req.Severity,
		"enqueued":    enqueued,
		"status":      "success",
		"duration_ms": time.Since(startTime).Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	if enqueued {
		w.WriteHeader(http.StatusAccepted)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"alertname":   req.Alertname,
		"severity":    req.Severity,
		"enqueued":    enqueued,
		"fingerprint": alert.Fingerprint,
	})
}

// handleEventHistory returns recent processed alerts from Redis.
// GET /events?limit=20
func (a *EventDrivenAgent) handleEventHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Extract trace context for response headers and log correlation
	tc := telemetry.GetTraceContext(ctx)
	w.Header().Set("X-Trace-ID", tc.TraceID)
	w.Header().Set("X-Span-ID", tc.SpanID)

	// Extract upstream request context
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Only GET requests are supported", http.StatusMethodNotAllowed)
		return
	}

	// Get queue depth for status
	queueKey := "truvag3:event:alert_queue"
	queueLen, err := a.redisClient.LLen(ctx, queueKey).Result()
	if err != nil {
		a.Logger.WarnWithContext(ctx, "Failed to get queue length", map[string]interface{}{
			"error":     err.Error(),
			"operation": "event_history",
		})
	}

	a.Logger.InfoWithContext(ctx, "Event history request completed", map[string]interface{}{
		"operation":   "event_history",
		"request_id":  upstreamRequestID,
		"queue_depth": queueLen,
		"status":      "success",
		"duration_ms": time.Since(startTime).Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"queue_depth": queueLen,
		"timestamp":   time.Now().Unix(),
	})
}

// handleHealth returns health status with orchestrator, queue, and worker status.
func (a *EventDrivenAgent) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract trace context for response headers and log correlation
	tc := telemetry.GetTraceContext(ctx)
	w.Header().Set("X-Trace-ID", tc.TraceID)
	w.Header().Set("X-Span-ID", tc.SpanID)

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
		"service":   "event-driven-agent",
	}

	// Check Redis
	if err := a.redisClient.Ping(ctx).Err(); err != nil {
		health["status"] = "degraded"
		health["redis"] = "unavailable"
	} else {
		health["redis"] = "healthy"

		// Queue depth
		queueLen, _ := a.redisClient.LLen(ctx, "truvag3:event:alert_queue").Result()
		health["queue_depth"] = queueLen
	}

	// Check orchestrator
	a.mu.RLock()
	orch := a.orchestrator
	a.mu.RUnlock()

	if orch != nil {
		metrics := orch.GetMetrics()
		health["orchestrator"] = map[string]interface{}{
			"status":              "active",
			"total_requests":      metrics.TotalRequests,
			"successful_requests": metrics.SuccessfulRequests,
			"failed_requests":     metrics.FailedRequests,
		}
	} else {
		health["orchestrator"] = "initializing"
	}

	// Check AI provider
	if a.AI != nil {
		health["ai_provider"] = "connected"
	} else {
		health["ai_provider"] = "not configured"
	}

	// Check HITL
	if a.hitl != nil {
		health["hitl"] = "active"
	} else {
		health["hitl"] = "not configured"
	}

	statusCode := http.StatusOK
	if health["status"] == "degraded" {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(health)
}
