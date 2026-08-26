package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// AlertManagerPayload represents the webhook payload from Prometheus AlertManager.
// See: https://prometheus.io/docs/alerting/latest/configuration/#webhook_config
type AlertManagerPayload struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"` // "firing" or "resolved"
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

// Alert represents a single alert within the AlertManager payload.
type Alert struct {
	Status       string            `json:"status"`      // "firing" or "resolved"
	Labels       map[string]string `json:"labels"`      // alertname, severity, job, instance, etc.
	Annotations  map[string]string `json:"annotations"` // summary, description, runbook_url, etc.
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"` // Link back to Prometheus query
	Fingerprint  string            `json:"fingerprint"`  // Unique alert identifier
}

// alertEnvelope carries an alert and its originating HTTP trace context through the
// Redis raw alert queue, so workers can restore the trace via StartLinkedSpan (RC5).
type alertEnvelope struct {
	AlertJSON string `json:"alert_json"`
	TraceID   string `json:"trace_id,omitempty"`
	SpanID    string `json:"span_id,omitempty"`
}

// handleAlertManagerWebhook receives AlertManager webhook payloads and routes alerts
// through the deterministic pipeline: parse -> severity route -> dedup -> enqueue.
//
// Returns HTTP 202 Accepted for enqueued alerts (processing is async).
// Returns HTTP 200 OK for warning/info alerts (handled synchronously).
func (a *EventDrivenAgent) handleAlertManagerWebhook(w http.ResponseWriter, r *http.Request) {
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

	// Parse AlertManager payload
	var payload AlertManagerPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		telemetry.RecordSpanError(ctx, err)
		a.Logger.ErrorWithContext(ctx, "Failed to parse AlertManager payload", map[string]interface{}{
			"error":     err.Error(),
			"operation": "webhook_receive",
		})
		http.Error(w, "Invalid AlertManager payload", http.StatusBadRequest)
		return
	}

	telemetry.AddSpanEvent(ctx, "alert.received",
		attribute.Int("alert_count", len(payload.Alerts)),
		attribute.String("group_key", payload.GroupKey),
	)

	a.Logger.InfoWithContext(ctx, "AlertManager webhook received", map[string]interface{}{
		"status":      payload.Status,
		"alert_count": len(payload.Alerts),
		"group_key":   payload.GroupKey,
		"receiver":    payload.Receiver,
		"operation":   "webhook_receive",
		"request_id":  upstreamRequestID,
	})

	// Process each alert through the deterministic pipeline
	var enqueued, skipped, warned, logged int
	for _, alert := range payload.Alerts {
		// Only process firing alerts (resolved alerts are informational)
		if alert.Status != "firing" {
			a.Logger.DebugWithContext(ctx, "Skipping resolved alert", map[string]interface{}{
				"alertname":   alert.Labels["alertname"],
				"fingerprint": alert.Fingerprint,
				"operation":   "webhook_receive",
			})
			skipped++
			continue
		}

		severity := alert.Labels["severity"]
		alertname := alert.Labels["alertname"]

		// Emit metric for every received alert
		telemetry.Counter("event_agent.alerts_received", "severity", severity, "alertname", alertname, "module", "agent")

		// Severity routing
		switch severity {
		case "critical":
			// Critical: dedup check + enqueue for AI investigation
			if a.enqueueCriticalAlert(ctx, alert) {
				enqueued++
			} else {
				skipped++ // Deduplicated
			}

		case "warning":
			// Warning: send Slack notification directly (no AI)
			if err := a.sendWarningSlackNotification(ctx, alert); err != nil {
				a.Logger.ErrorWithContext(ctx, "Failed to send warning Slack notification", map[string]interface{}{
					"alertname": alertname,
					"error":     err.Error(),
					"operation": "webhook_receive",
				})
			}
			warned++

		default: // "info" or unknown severity
			// Info: log only
			a.Logger.InfoWithContext(ctx, "Info alert received (log only)", map[string]interface{}{
				"alertname":   alertname,
				"fingerprint": alert.Fingerprint,
				"summary":     alert.Annotations["summary"],
				"operation":   "webhook_receive",
			})
			logged++
		}
	}

	a.Logger.InfoWithContext(ctx, "Webhook processing complete", map[string]interface{}{
		"enqueued":    enqueued,
		"skipped":     skipped,
		"warned":      warned,
		"logged":      logged,
		"duration_ms": time.Since(startTime).Milliseconds(),
		"operation":   "webhook_receive",
		"status":      "success",
	})

	// Return 202 if any alerts were enqueued (async processing)
	if enqueued > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "accepted",
			"enqueued": enqueued,
			"skipped":  skipped,
			"warned":   warned,
			"logged":   logged,
		})
		return
	}

	// Return 200 if all alerts were handled synchronously
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "processed",
		"skipped": skipped,
		"warned":  warned,
		"logged":  logged,
	})
}

// enqueueCriticalAlert performs Redis dedup check and enqueues the alert for AI processing.
// Returns true if the alert was enqueued, false if it was deduplicated.
func (a *EventDrivenAgent) enqueueCriticalAlert(ctx context.Context, alert Alert) bool {
	// Dedup key: fingerprint-based, 5min TTL (configurable via EVENT_AGENT_DEDUP_TTL)
	dedupKey := fmt.Sprintf("truvag3:event:dedup:%s", alert.Fingerprint)
	dedupTTL := getDedupTTL()

	// SET NX (set if not exists) with TTL for deduplication
	set, err := a.redisClient.SetNX(ctx, dedupKey, time.Now().Unix(), dedupTTL).Result()
	if err != nil {
		a.Logger.ErrorWithContext(ctx, "Redis dedup check failed, enqueuing anyway", map[string]interface{}{
			"fingerprint": alert.Fingerprint,
			"error":       err.Error(),
			"operation":   "webhook_receive",
		})
		// On Redis error, enqueue anyway (fail-open for alerts)
	} else if !set {
		// Key already exists -- this alert is a duplicate
		a.Logger.InfoWithContext(ctx, "Alert deduplicated", map[string]interface{}{
			"alertname":   alert.Labels["alertname"],
			"fingerprint": alert.Fingerprint,
			"dedup_ttl":   dedupTTL.String(),
			"operation":   "webhook_receive",
		})
		telemetry.Counter("event_agent.alerts_deduplicated", "alertname", alert.Labels["alertname"], "module", "agent")
		return false
	}

	// Serialize alert and wrap in trace-carrying envelope before LPUSH (RC5).
	// Capture HTTP trace context so the worker can restore it via StartLinkedSpan.
	// telemetry.GetTraceContext returns empty strings if no span is active (graceful).
	alertJSON, err := json.Marshal(alert)
	if err != nil {
		a.Logger.ErrorWithContext(ctx, "Failed to serialize alert", map[string]interface{}{
			"fingerprint": alert.Fingerprint,
			"error":       err.Error(),
			"operation":   "webhook_receive",
		})
		return false
	}
	tc := telemetry.GetTraceContext(ctx)
	envelope := alertEnvelope{
		AlertJSON: string(alertJSON),
		TraceID:   tc.TraceID,
		SpanID:    tc.SpanID,
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		a.Logger.ErrorWithContext(ctx, "Failed to serialize alert envelope", map[string]interface{}{
			"fingerprint": alert.Fingerprint,
			"error":       err.Error(),
			"operation":   "webhook_receive",
		})
		return false
	}

	queueKey := "truvag3:event:alert_queue"
	if err := a.redisClient.LPush(ctx, queueKey, envelopeJSON).Err(); err != nil {
		a.Logger.ErrorWithContext(ctx, "Failed to enqueue alert", map[string]interface{}{
			"fingerprint": alert.Fingerprint,
			"error":       err.Error(),
			"operation":   "webhook_receive",
		})
		return false
	}

	telemetry.AddSpanEvent(ctx, "alert.enqueued",
		attribute.String("alert_name", alert.Labels["alertname"]),
		attribute.String("severity", alert.Labels["severity"]),
	)

	a.Logger.InfoWithContext(ctx, "Critical alert enqueued", map[string]interface{}{
		"alertname":   alert.Labels["alertname"],
		"fingerprint": alert.Fingerprint,
		"severity":    alert.Labels["severity"],
		"instance":    alert.Labels["instance"],
		"operation":   "webhook_receive",
	})
	telemetry.Counter("event_agent.alerts_enqueued", "severity", "critical", "module", "agent")

	return true
}

// getDedupTTL returns the dedup TTL from TRUVAG3_EVENT_DEDUP_TTL (seconds), default 300s.
func getDedupTTL() time.Duration {
	ttlStr := os.Getenv("TRUVAG3_EVENT_DEDUP_TTL")
	if ttlStr != "" {
		if ttl, err := strconv.Atoi(ttlStr); err == nil && ttl > 0 {
			return time.Duration(ttl) * time.Second
		}
	}
	return 5 * time.Minute // Default 5 minutes
}

// sendWarningSlackNotification sends a Slack message for warning-severity alerts.
// This calls the slack-tool microservice directly (no AI orchestration).
func (a *EventDrivenAgent) sendWarningSlackNotification(ctx context.Context, alert Alert) error {
	channel := os.Getenv("TRUVAG3_SLACK_CHANNEL_NOTIFICATIONS")
	if channel == "" {
		channel = "#notifications"
	}

	slackToolURL := fmt.Sprintf("http://slack-tool-service.%s/api/capabilities/send_message",
		os.Getenv("NAMESPACE"))

	payload := map[string]interface{}{
		"channel": channel,
		"text": fmt.Sprintf(":warning: *Warning Alert: %s*\n>%s\n>Instance: `%s`\n>Fingerprint: `%s`",
			alert.Labels["alertname"],
			alert.Annotations["summary"],
			alert.Labels["instance"],
			alert.Fingerprint,
		),
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackToolURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create slack-tool request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		telemetry.Counter("event_agent.slack_notifications", "status", "error", "module", "agent")
		return fmt.Errorf("call slack-tool send_message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		telemetry.Counter("event_agent.slack_notifications", "status", "error", "module", "agent")
		return fmt.Errorf("slack-tool returned %d", resp.StatusCode)
	}

	telemetry.Counter("event_agent.slack_notifications", "status", "sent", "module", "agent")
	a.Logger.InfoWithContext(ctx, "Warning alert sent to Slack via slack-tool", map[string]interface{}{
		"alertname":   alert.Labels["alertname"],
		"fingerprint": alert.Fingerprint,
		"channel":     channel,
		"operation":   "webhook_receive",
	})

	return nil
}
