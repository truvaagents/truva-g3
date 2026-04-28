package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (a *AdvisoryTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(info.HTTPStatus)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      info.Code,
			Message:   message,
			Category:  info.Category,
			Retryable: info.Retryable,
		},
	})
}

func (a *AdvisoryTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: status >= 500,
		},
	})
}

func (a *AdvisoryTool) handleGetAdvisory(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "travel-advisory-tool"),
		attribute.String("truvag3.capability", "get_travel_advisory"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_travel_advisory"),
	)

	a.Logger.InfoWithContext(ctx, "Processing get travel advisory request", map[string]interface{}{
		"operation":  "get_travel_advisory",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	var req GetAdvisoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("advisory.errors.total", "capability", "get_travel_advisory", "error_type", "decode_error")
		a.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation": "get_travel_advisory", "request_id": upstreamRequestID,
			"error": err.Error(), "error_type": "decode_error",
		})
		a.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.Country = strings.TrimSpace(req.Country)

	if req.Country == "" {
		telemetry.Counter("advisory.errors.total", "capability", "get_travel_advisory", "error_type", "validation_error")
		a.Logger.WarnWithContext(ctx, "Missing required country field", map[string]interface{}{
			"operation": "get_travel_advisory", "request_id": upstreamRequestID, "error_type": "validation_error",
		})
		a.sendError(rw, "country is required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("advisory.country", req.Country),
	)

	a.Logger.InfoWithContext(ctx, "Received get travel advisory request", map[string]interface{}{
		"operation":  "get_travel_advisory",
		"country":    req.Country,
		"request_id": upstreamRequestID,
	})

	telemetry.AddSpanEvent(ctx, "calling_state_dept_api",
		attribute.String("country", req.Country),
		attribute.String("api", "travel_advisories"),
	)

	apiStartTime := time.Now()
	response, err := a.client.GetAdvisory(ctx, req.Country)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("advisory.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_travel_advisory",
		"api", "state_dept",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("advisory.api.errors.total",
			"capability", "get_travel_advisory",
			"error_type", "api_error",
		)
		a.Logger.WarnWithContext(ctx, "State Department API call failed", map[string]interface{}{
			"operation":   "get_travel_advisory",
			"error":       err.Error(),
			"country":     req.Country,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		a.sendUpstreamError(rw, "Travel advisory lookup failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	duration := time.Since(startTime)
	telemetry.Histogram("advisory.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_travel_advisory",
	)
	telemetry.Counter("advisory.requests.total",
		"capability", "get_travel_advisory",
		"status", "success",
	)
	telemetry.RecordToolCall("travel-advisory-tool", "get_travel_advisory", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "travel_advisory_retrieved",
		attribute.String("country", req.Country),
		attribute.Int("level", response.Level),
		attribute.String("level_text", response.LevelText),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	a.Logger.InfoWithContext(ctx, "Get travel advisory request completed", map[string]interface{}{
		"operation":   "get_travel_advisory",
		"country":     req.Country,
		"level":       response.Level,
		"level_text":  response.LevelText,
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

func (a *AdvisoryTool) handleListAdvisories(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "travel-advisory-tool"),
		attribute.String("truvag3.capability", "list_advisories"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "list_advisories"),
	)

	a.Logger.InfoWithContext(ctx, "Processing list advisories request", map[string]interface{}{
		"operation":  "list_advisories",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	var req ListAdvisoriesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("advisory.errors.total", "capability", "list_advisories", "error_type", "decode_error")
		a.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation": "list_advisories", "request_id": upstreamRequestID,
			"error": err.Error(), "error_type": "decode_error",
		})
		a.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.Int("advisory.filter_level", req.Level),
	)

	a.Logger.InfoWithContext(ctx, "Received list advisories request", map[string]interface{}{
		"operation":  "list_advisories",
		"level":      req.Level,
		"request_id": upstreamRequestID,
	})

	telemetry.AddSpanEvent(ctx, "calling_state_dept_api",
		attribute.Int("filter_level", req.Level),
		attribute.String("api", "travel_advisories"),
	)

	apiStartTime := time.Now()
	response, err := a.client.ListAdvisories(ctx, req.Level)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("advisory.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "list_advisories",
		"api", "state_dept",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("advisory.api.errors.total",
			"capability", "list_advisories",
			"error_type", "api_error",
		)
		a.Logger.WarnWithContext(ctx, "State Department API call failed", map[string]interface{}{
			"operation":   "list_advisories",
			"error":       err.Error(),
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		a.sendUpstreamError(rw, "Advisory listing failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	duration := time.Since(startTime)
	telemetry.Histogram("advisory.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "list_advisories",
	)
	telemetry.Counter("advisory.requests.total",
		"capability", "list_advisories",
		"status", "success",
	)
	telemetry.RecordToolCall("travel-advisory-tool", "list_advisories", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "advisories_listed",
		attribute.Int("count", response.Count),
		attribute.Int("filter_level", req.Level),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	a.Logger.InfoWithContext(ctx, "List advisories request completed", map[string]interface{}{
		"operation":   "list_advisories",
		"count":       response.Count,
		"level":       req.Level,
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
