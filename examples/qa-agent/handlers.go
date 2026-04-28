package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// handleQuery handles non-streaming QA orchestration requests.
func (q *QAAgent) handleQuery(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// Handle CORS preflight
	if r.Method == http.MethodOptions {
		setCORSHeaders(w)
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Only POST requests are supported", nil)
		return
	}

	// 1. Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("operation", "qa_query"),
	)

	// 2. Log with trace context
	q.Logger.InfoWithContext(ctx, "Processing QA query", map[string]interface{}{
		"operation": "query",
	})

	// 3. Parse and validate request
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "Request body is required", nil)
		return
	}
	var raw map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		telemetry.RecordSpanError(ctx, err)
		q.Logger.ErrorWithContext(ctx, "Failed to decode query request", map[string]interface{}{
			"operation": "query",
			"error":     err.Error(),
		})
		writeError(w, http.StatusBadRequest, "Invalid JSON request body", err)
		return
	}

	// Extract query from either wrapped {"data": {...}} or flat format
	var query string

	if dataRaw, ok := raw["data"]; ok {
		if data, ok := dataRaw.(map[string]interface{}); ok {
			if q, ok := data["query"].(string); ok {
				query = q
			} else if m, ok := data["message"].(string); ok {
				query = m
			}
		}
	}
	if query == "" {
		if q, ok := raw["query"].(string); ok {
			query = q
		} else if m, ok := raw["message"].(string); ok {
			query = m
		}
	}

	if query == "" {
		writeError(w, http.StatusBadRequest, "query field is required", nil)
		return
	}

	// 4. Check orchestrator availability
	if q.GetOrchestrator() == nil {
		writeError(w, http.StatusServiceUnavailable, "Orchestrator initializing — please retry in a few seconds", nil)
		return
	}

	// 5. Process through AI orchestrator
	telemetry.AddSpanEvent(ctx, "orchestration_started",
		attribute.String("query", query),
	)

	result, err := q.ProcessQuery(ctx, query)
	if err != nil {
		// Check if the error originated from an AI provider (e.g., 400 Bad Request, 429 Rate Limit).
		// core.ProviderError carries the original HTTP status, provider name, and model —
		// surface the real status instead of masking everything as 500.
		var pe core.ProviderError
		if errors.As(err, &pe) && pe.StatusCode() >= 400 && pe.StatusCode() < 500 {
			q.Logger.WarnWithContext(ctx, "LLM provider returned client error", map[string]interface{}{
				"operation":    "query",
				"error":        pe.Error(),
				"status_code":  pe.StatusCode(),
				"provider":     pe.Provider(),
				"model":        pe.Model(),
				"is_transient": pe.IsTransient(),
				"duration_ms":  time.Since(startTime).Milliseconds(),
			})
			telemetry.RecordSpanError(ctx, err)
			telemetry.Counter("qa.requests", "status", "provider_error")
			writeError(w, pe.StatusCode(), pe.Error(), err)
			return
		}

		q.Logger.ErrorWithContext(ctx, "QA orchestration failed", map[string]interface{}{
			"operation":   "query",
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("qa.requests", "status", "error")
		writeError(w, http.StatusInternalServerError, "Orchestration failed", err)
		return
	}

	// 6. Set request_id as span attribute for Jaeger searchability
	telemetry.SetSpanAttributes(ctx, attribute.String("request_id", result.RequestID))

	// 7. Build response
	response := map[string]interface{}{
		"success":        true,
		"request_id":     result.RequestID,
		"request":        query,
		"response":       result.Response,
		"tools_used":     result.AgentsInvolved,
		"execution_time": time.Since(startTime).String(),
		"confidence":     result.Confidence,
	}

	if result.Usage != nil {
		response["usage"] = map[string]interface{}{
			"prompt_tokens":     result.Usage.PromptTokens,
			"completion_tokens": result.Usage.CompletionTokens,
			"total_tokens":      result.Usage.TotalTokens,
		}
	}

	// Record metrics
	telemetry.Counter("qa.requests", "status", "success")
	telemetry.Histogram("qa.request.duration_ms", float64(time.Since(startTime).Milliseconds()), "status", "success")

	writeJSON(w, http.StatusOK, response)
}

// handleHealth returns the health status with orchestrator info.
func (q *QAAgent) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
		"service":   "qa-agent",
	}

	// Check Redis/Discovery connection
	if q.Discovery != nil {
		_, err := q.Discovery.Discover(ctx, core.DiscoveryFilter{})
		if err != nil {
			health["status"] = "degraded"
			health["redis"] = "unavailable"
		} else {
			health["redis"] = "healthy"
		}
	} else {
		health["redis"] = "not configured"
	}

	// Check orchestrator status
	orch := q.GetOrchestrator()
	if orch != nil {
		metrics := orch.GetMetrics()
		health["orchestrator"] = map[string]interface{}{
			"status":              "active",
			"total_requests":      metrics.TotalRequests,
			"successful_requests": metrics.SuccessfulRequests,
			"failed_requests":     metrics.FailedRequests,
			"average_latency_ms":  metrics.AverageLatency.Milliseconds(),
		}
	} else {
		health["orchestrator"] = "initializing"
	}

	// Check AI provider
	if q.AI != nil {
		health["ai_provider"] = "connected"
	} else {
		health["ai_provider"] = "not configured"
	}

	statusCode := http.StatusOK
	if health["status"] == "degraded" {
		statusCode = http.StatusServiceUnavailable
	}
	writeJSON(w, statusCode, health)
}

// handleDiscover returns available tools and agents from service discovery.
func (q *QAAgent) handleDiscover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if q.Discovery == nil {
		writeError(w, http.StatusServiceUnavailable, "Service discovery not configured", nil)
		return
	}

	allComponents, err := q.Discovery.Discover(ctx, core.DiscoveryFilter{})
	if err != nil {
		q.Logger.ErrorWithContext(ctx, "Discovery failed", map[string]interface{}{
			"error": err.Error(),
		})
		writeError(w, http.StatusServiceUnavailable, "Discovery failed", err)
		return
	}

	tools := make([]*core.ServiceInfo, 0)
	agents := make([]*core.ServiceInfo, 0)

	for _, component := range allComponents {
		switch component.Type {
		case core.ComponentTypeTool:
			tools = append(tools, component)
		case core.ComponentTypeAgent:
			if component.ID != q.GetID() {
				agents = append(agents, component)
			}
		}
	}

	response := map[string]interface{}{
		"discovery_summary": map[string]interface{}{
			"total_components": len(allComponents),
			"tools":            len(tools),
			"agents":           len(agents),
			"discovery_time":   time.Now().Format(time.RFC3339),
		},
		"tools":  tools,
		"agents": agents,
	}

	writeJSON(w, http.StatusOK, response)
}

// Helper functions

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	setCORSHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, statusCode int, message string, err error) {
	response := map[string]interface{}{
		"error":   message,
		"status":  statusCode,
		"success": false,
	}
	if err != nil {
		response["details"] = err.Error()
	}
	writeJSON(w, statusCode, response)
}
