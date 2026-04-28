package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (t *ClinicalTrialsTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

// sendError sends a structured error response using core.ToolResponse
func (t *ClinicalTrialsTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "UNAVAILABLE"),
		},
	})
}

func (t *ClinicalTrialsTool) handleSearchTrials(rw http.ResponseWriter, r *http.Request) {
	// Step 1: startTime + ctx
	startTime := time.Now()
	ctx := r.Context()

	// Step 2: Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Step 3: Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// Step 4: Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "clinical-trials-tool"),
		attribute.String("truvag3.capability", "search_trials"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_trials"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing search trials request", map[string]interface{}{
		"operation":  "search_trials",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req SearchTrialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("clinicaltrials.errors.total",
			"capability", "search_trials",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_trials",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.Condition = strings.TrimSpace(req.Condition)
	if req.Condition == "" {
		telemetry.Counter("clinicaltrials.errors.total",
			"capability", "search_trials",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty condition provided", map[string]interface{}{
			"operation":  "search_trials",
			"request_id": upstreamRequestID,
			"error":      "condition is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: condition is required"))
		t.sendError(rw, "Condition is required", http.StatusBadRequest, "INVALID_CONDITION")
		return
	}

	// Normalize enum values to UPPER_CASE (API requires it)
	req.Phase = strings.ToUpper(strings.TrimSpace(req.Phase))
	req.Status = strings.ToUpper(strings.TrimSpace(req.Status))

	// Add search params to span attributes for filtering in Jaeger
	telemetry.SetSpanAttributes(ctx,
		attribute.String("clinicaltrials.condition", req.Condition),
		attribute.String("clinicaltrials.phase", req.Phase),
		attribute.String("clinicaltrials.status", req.Status),
	)

	t.Logger.InfoWithContext(ctx, "Received search trials request", map[string]interface{}{
		"operation":    "search_trials",
		"condition":    req.Condition,
		"intervention": req.Intervention,
		"phase":        req.Phase,
		"status":       req.Status,
		"max_results":  req.MaxResults,
		"request_id":   upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_clinicaltrials_api",
		attribute.String("condition", req.Condition),
		attribute.String("api", "search_trials"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	trials, totalCount, err := t.client.SearchTrials(ctx, req.Condition, req.Intervention, req.Phase, req.Status, req.MaxResults)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("clinicaltrials.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_trials",
		"api", "clinicaltrials_gov",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("clinicaltrials.api.errors.total",
			"capability", "search_trials",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "ClinicalTrials.gov API call failed", map[string]interface{}{
			"operation":   "search_trials",
			"error":       err.Error(),
			"condition":   req.Condition,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "ClinicalTrials.gov API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "ClinicalTrials.gov API call successful", map[string]interface{}{
		"operation":    "search_trials",
		"condition":    req.Condition,
		"duration_ms":  apiDuration.Milliseconds(),
		"trials_count": len(trials),
		"total_count":  totalCount,
		"request_id":   upstreamRequestID,
	})

	response := SearchTrialsResponse{
		Condition:  req.Condition,
		Trials:     trials,
		TotalCount: totalCount,
		Source:     "ClinicalTrials.gov",
	}

	// Add span attributes for result metrics
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("clinicaltrials.trials_count", len(trials)),
		attribute.Int("clinicaltrials.total_count", totalCount),
	)

	// Step 13: Record success counters + RecordToolCall
	duration := time.Since(startTime)
	telemetry.Histogram("clinicaltrials.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_trials",
	)
	telemetry.Counter("clinicaltrials.requests.total",
		"capability", "search_trials",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("clinical-trials-tool", "search_trials", float64(duration.Milliseconds()), "success")

	// Step 14: Add completion span event
	telemetry.AddSpanEvent(ctx, "search_trials_completed",
		attribute.String("condition", req.Condition),
		attribute.Int("trials_count", len(trials)),
		attribute.Int("total_count", totalCount),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 15: Log completion
	t.Logger.InfoWithContext(ctx, "Search trials request completed", map[string]interface{}{
		"operation":    "search_trials",
		"condition":    req.Condition,
		"trials_count": len(trials),
		"total_count":  totalCount,
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// Step 16: Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

func (t *ClinicalTrialsTool) handleGetTrial(rw http.ResponseWriter, r *http.Request) {
	// Step 1: startTime + ctx
	startTime := time.Now()
	ctx := r.Context()

	// Step 2: Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Step 3: Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// Step 4: Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "clinical-trials-tool"),
		attribute.String("truvag3.capability", "get_trial"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_trial"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing get trial request", map[string]interface{}{
		"operation":  "get_trial",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req GetTrialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("clinicaltrials.errors.total",
			"capability", "get_trial",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "get_trial",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.NCTID = strings.ToUpper(strings.TrimSpace(req.NCTID))
	if req.NCTID == "" {
		telemetry.Counter("clinicaltrials.errors.total",
			"capability", "get_trial",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty NCT ID provided", map[string]interface{}{
			"operation":  "get_trial",
			"request_id": upstreamRequestID,
			"error":      "nct_id is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: nct_id is required"))
		t.sendError(rw, "NCT ID is required", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Add search params to span attributes for filtering in Jaeger
	telemetry.SetSpanAttributes(ctx,
		attribute.String("clinicaltrials.nct_id", req.NCTID),
	)

	t.Logger.InfoWithContext(ctx, "Received get trial request", map[string]interface{}{
		"operation":  "get_trial",
		"nct_id":     req.NCTID,
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_clinicaltrials_api",
		attribute.String("nct_id", req.NCTID),
		attribute.String("api", "get_trial"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	trial, err := t.client.GetTrial(ctx, req.NCTID)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("clinicaltrials.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_trial",
		"api", "clinicaltrials_gov",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("clinicaltrials.api.errors.total",
			"capability", "get_trial",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "ClinicalTrials.gov API call failed", map[string]interface{}{
			"operation":   "get_trial",
			"error":       err.Error(),
			"nct_id":      req.NCTID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "ClinicalTrials.gov API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "ClinicalTrials.gov API call successful", map[string]interface{}{
		"operation":   "get_trial",
		"nct_id":      req.NCTID,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	response := GetTrialResponse{
		Trial:  *trial,
		Source: "ClinicalTrials.gov",
	}

	// Step 13: Record success counters + RecordToolCall
	duration := time.Since(startTime)
	telemetry.Histogram("clinicaltrials.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_trial",
	)
	telemetry.Counter("clinicaltrials.requests.total",
		"capability", "get_trial",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("clinical-trials-tool", "get_trial", float64(duration.Milliseconds()), "success")

	// Step 14: Add completion span event
	telemetry.AddSpanEvent(ctx, "get_trial_completed",
		attribute.String("nct_id", req.NCTID),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 15: Log completion
	t.Logger.InfoWithContext(ctx, "Get trial request completed", map[string]interface{}{
		"operation":   "get_trial",
		"nct_id":      req.NCTID,
		"source":      response.Source,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// Step 16: Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

func (t *ClinicalTrialsTool) handleSearchByLocation(rw http.ResponseWriter, r *http.Request) {
	// Step 1: startTime + ctx
	startTime := time.Now()
	ctx := r.Context()

	// Step 2: Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Step 3: Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// Step 4: Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "clinical-trials-tool"),
		attribute.String("truvag3.capability", "search_by_location"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_by_location"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing search by location request", map[string]interface{}{
		"operation":  "search_by_location",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req SearchByLocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("clinicaltrials.errors.total",
			"capability", "search_by_location",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_by_location",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.Condition = strings.TrimSpace(req.Condition)
	req.Country = strings.TrimSpace(req.Country)
	if req.Condition == "" {
		telemetry.Counter("clinicaltrials.errors.total",
			"capability", "search_by_location",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty condition provided", map[string]interface{}{
			"operation":  "search_by_location",
			"request_id": upstreamRequestID,
			"error":      "condition is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: condition is required"))
		t.sendError(rw, "Condition is required", http.StatusBadRequest, "INVALID_CONDITION")
		return
	}
	if req.Country == "" {
		telemetry.Counter("clinicaltrials.errors.total",
			"capability", "search_by_location",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty country provided", map[string]interface{}{
			"operation":  "search_by_location",
			"request_id": upstreamRequestID,
			"error":      "country is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: country is required"))
		t.sendError(rw, "Country is required", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Normalize enum values to UPPER_CASE (API requires it)
	req.Status = strings.ToUpper(strings.TrimSpace(req.Status))

	// Add search params to span attributes for filtering in Jaeger
	telemetry.SetSpanAttributes(ctx,
		attribute.String("clinicaltrials.condition", req.Condition),
		attribute.String("clinicaltrials.country", req.Country),
		attribute.String("clinicaltrials.city", req.City),
	)

	t.Logger.InfoWithContext(ctx, "Received search by location request", map[string]interface{}{
		"operation":   "search_by_location",
		"condition":   req.Condition,
		"country":     req.Country,
		"city":        req.City,
		"status":      req.Status,
		"max_results": req.MaxResults,
		"request_id":  upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_clinicaltrials_api",
		attribute.String("condition", req.Condition),
		attribute.String("country", req.Country),
		attribute.String("api", "search_by_location"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	trials, err := t.client.SearchByLocation(ctx, req.Condition, req.Country, req.City, req.Status, req.MaxResults)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("clinicaltrials.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_by_location",
		"api", "clinicaltrials_gov",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("clinicaltrials.api.errors.total",
			"capability", "search_by_location",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "ClinicalTrials.gov API call failed", map[string]interface{}{
			"operation":   "search_by_location",
			"error":       err.Error(),
			"condition":   req.Condition,
			"country":     req.Country,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "ClinicalTrials.gov API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "ClinicalTrials.gov API call successful", map[string]interface{}{
		"operation":    "search_by_location",
		"condition":    req.Condition,
		"country":      req.Country,
		"duration_ms":  apiDuration.Milliseconds(),
		"trials_count": len(trials),
		"request_id":   upstreamRequestID,
	})

	response := SearchByLocationResponse{
		Condition: req.Condition,
		Country:   req.Country,
		City:      req.City,
		Trials:    trials,
		Source:    "ClinicalTrials.gov",
	}

	// Add span attributes for result metrics
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("clinicaltrials.trials_count", len(trials)),
	)

	// Step 13: Record success counters + RecordToolCall
	duration := time.Since(startTime)
	telemetry.Histogram("clinicaltrials.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_by_location",
	)
	telemetry.Counter("clinicaltrials.requests.total",
		"capability", "search_by_location",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("clinical-trials-tool", "search_by_location", float64(duration.Milliseconds()), "success")

	// Step 14: Add completion span event
	telemetry.AddSpanEvent(ctx, "search_by_location_completed",
		attribute.String("condition", req.Condition),
		attribute.String("country", req.Country),
		attribute.Int("trials_count", len(trials)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 15: Log completion
	t.Logger.InfoWithContext(ctx, "Search by location request completed", map[string]interface{}{
		"operation":    "search_by_location",
		"condition":    req.Condition,
		"country":      req.Country,
		"city":         req.City,
		"trials_count": len(trials),
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// Step 16: Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
