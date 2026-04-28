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
func (t *OpenFDATool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

// sendError sends a structured error response using core.ToolResponse.
// CRITICAL: WriteHeader MUST be called BEFORE json.Encode.
func (t *OpenFDATool) sendError(rw http.ResponseWriter, message string, status int, code string) {
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

// handleSearchAdverseEvents processes drug adverse event search requests with full telemetry.
// 16-step handler pattern from stock-market-tool reference.
func (t *OpenFDATool) handleSearchAdverseEvents(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "openfda-tool"),
		attribute.String("truvag3.capability", "search_adverse_events"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_adverse_events"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing adverse events search request", map[string]interface{}{
		"operation":  "search_adverse_events",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req SearchAdverseEventsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fda.errors.total",
			"capability", "search_adverse_events",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_adverse_events",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.DrugName = strings.TrimSpace(req.DrugName)
	if req.DrugName == "" {
		telemetry.Counter("fda.errors.total",
			"capability", "search_adverse_events",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty drug_name provided", map[string]interface{}{
			"operation":  "search_adverse_events",
			"request_id": upstreamRequestID,
			"error":      "drug_name is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: drug_name is required"))
		t.sendError(rw, "drug_name is required", http.StatusBadRequest, "INVALID_DRUG_NAME")
		return
	}

	// Add drug_name to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("fda.drug_name", req.DrugName),
	)

	t.Logger.InfoWithContext(ctx, "Received adverse events search request", map[string]interface{}{
		"operation":  "search_adverse_events",
		"drug_name":  req.DrugName,
		"serious":    req.Serious,
		"limit":      req.Limit,
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_openfda_api",
		attribute.String("drug_name", req.DrugName),
		attribute.String("api", "drug_event"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	result, err := t.client.SearchAdverseEvents(ctx, req.DrugName, req.Serious, req.Limit)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("fda.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_adverse_events",
		"api", "openfda",
	)

	// Step 12: Error handling with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fda.api.errors.total",
			"capability", "search_adverse_events",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "OpenFDA API call failed", map[string]interface{}{
			"operation":   "search_adverse_events",
			"error":       err.Error(),
			"drug_name":   req.DrugName,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "OpenFDA API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "OpenFDA API call successful", map[string]interface{}{
		"operation":    "search_adverse_events",
		"drug_name":    req.DrugName,
		"duration_ms":  apiDuration.Milliseconds(),
		"events_count": len(result.Results),
		"total":        result.Meta.Results.Total,
		"request_id":   upstreamRequestID,
	})

	// Convert FDA response to our response format
	events := make([]AdverseEventResponse, 0, len(result.Results))
	for _, ae := range result.Results {
		reactions := make([]string, 0, len(ae.Patient.Reaction))
		for _, r := range ae.Patient.Reaction {
			reactions = append(reactions, r.ReactionMedDRAPT)
		}
		drugNames := make([]string, 0, len(ae.Patient.Drug))
		for _, d := range ae.Patient.Drug {
			drugNames = append(drugNames, d.MedicinalProduct)
		}

		events = append(events, AdverseEventResponse{
			SafetyReportID:  ae.SafetyReportID,
			ReceiveDate:     ae.ReceiveDate,
			Serious:         ae.Serious, // Keep as string -- "1" or "2"
			Reactions:       reactions,
			DrugNames:       drugNames,
			PatientSex:      ae.Patient.PatientSex,
			PatientOnsetAge: ae.Patient.PatientOnsetAge,
			Source:          "OpenFDA API",
		})
	}

	response := SearchAdverseEventsResponse{
		DrugName: req.DrugName,
		Total:    result.Meta.Results.Total,
		Events:   events,
		Source:   "OpenFDA API",
	}

	// Add span attributes for result summary
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("fda.events.count", len(response.Events)),
		attribute.Int("fda.events.total", response.Total),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("fda.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_adverse_events",
	)
	telemetry.Counter("fda.requests.total",
		"capability", "search_adverse_events",
		"status", "success",
		"module", "tool",
	)

	// Step 14: RecordToolCall for unified dashboard metrics
	telemetry.RecordToolCall("openfda-tool", "search_adverse_events", float64(duration.Milliseconds()), "success")

	// Step 15: Completion span event
	telemetry.AddSpanEvent(ctx, "adverse_events_retrieved",
		attribute.String("drug_name", req.DrugName),
		attribute.Int("events_count", len(response.Events)),
		attribute.Int("total", response.Total),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion
	t.Logger.InfoWithContext(ctx, "Adverse events search completed", map[string]interface{}{
		"operation":    "search_adverse_events",
		"drug_name":    req.DrugName,
		"events_count": len(response.Events),
		"total":        response.Total,
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleSearchDrugLabels processes drug label search requests with full telemetry.
// 16-step handler pattern from stock-market-tool reference.
func (t *OpenFDATool) handleSearchDrugLabels(rw http.ResponseWriter, r *http.Request) {
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

	// Step 4: Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "openfda-tool"),
		attribute.String("truvag3.capability", "search_drug_labels"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_drug_labels"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing drug labels search request", map[string]interface{}{
		"operation":  "search_drug_labels",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req SearchDrugLabelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fda.errors.total",
			"capability", "search_drug_labels",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_drug_labels",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		telemetry.Counter("fda.errors.total",
			"capability", "search_drug_labels",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty query provided", map[string]interface{}{
			"operation":  "search_drug_labels",
			"request_id": upstreamRequestID,
			"error":      "query is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: query is required"))
		t.sendError(rw, "query is required", http.StatusBadRequest, "INVALID_QUERY")
		return
	}

	// Add query to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("fda.query", req.Query),
	)

	t.Logger.InfoWithContext(ctx, "Received drug labels search request", map[string]interface{}{
		"operation":  "search_drug_labels",
		"query":      req.Query,
		"limit":      req.Limit,
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_openfda_api",
		attribute.String("query", req.Query),
		attribute.String("api", "drug_label"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	result, err := t.client.SearchDrugLabels(ctx, req.Query, req.Limit)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("fda.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_drug_labels",
		"api", "openfda",
	)

	// Step 12: Error handling with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fda.api.errors.total",
			"capability", "search_drug_labels",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "OpenFDA API call failed", map[string]interface{}{
			"operation":   "search_drug_labels",
			"error":       err.Error(),
			"query":       req.Query,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "OpenFDA API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "OpenFDA API call successful", map[string]interface{}{
		"operation":    "search_drug_labels",
		"query":        req.Query,
		"duration_ms":  apiDuration.Milliseconds(),
		"labels_count": len(result.Results),
		"total":        result.Meta.Results.Total,
		"request_id":   upstreamRequestID,
	})

	// Convert FDA response to our response format
	labels := make([]DrugLabelResponse, 0, len(result.Results))
	for _, label := range result.Results {
		dlr := DrugLabelResponse{
			Purpose:          truncate(firstOrEmpty(label.Purpose), 500),
			Warnings:         truncate(firstOrEmpty(label.Warnings), 500),
			Indications:      truncate(firstOrEmpty(label.IndicationsAndUsage), 500),
			DosageAndAdmin:   truncate(firstOrEmpty(label.DosageAndAdministration), 500),
			ActiveIngredient: firstOrEmpty(label.ActiveIngredient),
			Source:           "OpenFDA API",
		}

		// Safely access label.OpenFDA (may be nil or {})
		if label.OpenFDA != nil {
			dlr.BrandName = firstOrEmpty(label.OpenFDA.BrandName)
			dlr.GenericName = firstOrEmpty(label.OpenFDA.GenericName)
			dlr.Manufacturer = firstOrEmpty(label.OpenFDA.ManufacturerName)
			dlr.Route = label.OpenFDA.Route
		}

		labels = append(labels, dlr)
	}

	response := SearchDrugLabelsResponse{
		Query:  req.Query,
		Total:  result.Meta.Results.Total,
		Labels: labels,
		Source: "OpenFDA API",
	}

	// Add span attributes for result summary
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("fda.labels.count", len(response.Labels)),
		attribute.Int("fda.labels.total", response.Total),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("fda.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_drug_labels",
	)
	telemetry.Counter("fda.requests.total",
		"capability", "search_drug_labels",
		"status", "success",
		"module", "tool",
	)

	// Step 14: RecordToolCall for unified dashboard metrics
	telemetry.RecordToolCall("openfda-tool", "search_drug_labels", float64(duration.Milliseconds()), "success")

	// Step 15: Completion span event
	telemetry.AddSpanEvent(ctx, "drug_labels_retrieved",
		attribute.String("query", req.Query),
		attribute.Int("labels_count", len(response.Labels)),
		attribute.Int("total", response.Total),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion
	t.Logger.InfoWithContext(ctx, "Drug labels search completed", map[string]interface{}{
		"operation":    "search_drug_labels",
		"query":        req.Query,
		"labels_count": len(response.Labels),
		"total":        response.Total,
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleSearchDrugRecalls processes drug recall search requests with full telemetry.
// 16-step handler pattern from stock-market-tool reference.
func (t *OpenFDATool) handleSearchDrugRecalls(rw http.ResponseWriter, r *http.Request) {
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

	// Step 4: Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "openfda-tool"),
		attribute.String("truvag3.capability", "search_drug_recalls"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_drug_recalls"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing drug recalls search request", map[string]interface{}{
		"operation":  "search_drug_recalls",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req SearchDrugRecallsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fda.errors.total",
			"capability", "search_drug_recalls",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_drug_recalls",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// No required field validation (all fields optional)

	// Add span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("fda.drug_name", req.DrugName),
		attribute.String("fda.classification", req.Classification),
		attribute.String("fda.status", req.Status),
	)

	t.Logger.InfoWithContext(ctx, "Received drug recalls search request", map[string]interface{}{
		"operation":      "search_drug_recalls",
		"drug_name":      req.DrugName,
		"classification": req.Classification,
		"status":         req.Status,
		"limit":          req.Limit,
		"request_id":     upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_openfda_api",
		attribute.String("drug_name", req.DrugName),
		attribute.String("api", "drug_enforcement"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	result, err := t.client.SearchDrugRecalls(ctx, req.DrugName, req.Classification, req.Status, req.Limit)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("fda.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_drug_recalls",
		"api", "openfda",
	)

	// Step 12: Error handling with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fda.api.errors.total",
			"capability", "search_drug_recalls",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "OpenFDA API call failed", map[string]interface{}{
			"operation":   "search_drug_recalls",
			"error":       err.Error(),
			"drug_name":   req.DrugName,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "OpenFDA API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "OpenFDA API call successful", map[string]interface{}{
		"operation":     "search_drug_recalls",
		"drug_name":     req.DrugName,
		"duration_ms":   apiDuration.Milliseconds(),
		"recalls_count": len(result.Results),
		"total":         result.Meta.Results.Total,
		"request_id":    upstreamRequestID,
	})

	// Direct mapping from FDAEnforcement to DrugRecallResponse (fields are already strings)
	recalls := make([]DrugRecallResponse, 0, len(result.Results))
	for _, enf := range result.Results {
		recalls = append(recalls, DrugRecallResponse{
			RecallNumber:       enf.RecallNumber,
			ReasonForRecall:    enf.ReasonForRecall,
			Classification:     enf.Classification,
			Status:             enf.Status,
			ProductDescription: enf.ProductDescription,
			RecallingFirm:      enf.RecallingFirm,
			City:               enf.City,
			State:              enf.State,
			ReportDate:         enf.ReportDate,
			Source:             "OpenFDA API",
		})
	}

	response := SearchDrugRecallsResponse{
		DrugName: req.DrugName,
		Total:    result.Meta.Results.Total,
		Recalls:  recalls,
		Source:   "OpenFDA API",
	}

	// Add span attributes for result summary
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("fda.recalls.count", len(response.Recalls)),
		attribute.Int("fda.recalls.total", response.Total),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("fda.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_drug_recalls",
	)
	telemetry.Counter("fda.requests.total",
		"capability", "search_drug_recalls",
		"status", "success",
		"module", "tool",
	)

	// Step 14: RecordToolCall for unified dashboard metrics
	telemetry.RecordToolCall("openfda-tool", "search_drug_recalls", float64(duration.Milliseconds()), "success")

	// Step 15: Completion span event
	telemetry.AddSpanEvent(ctx, "drug_recalls_retrieved",
		attribute.String("drug_name", req.DrugName),
		attribute.Int("recalls_count", len(response.Recalls)),
		attribute.Int("total", response.Total),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion
	t.Logger.InfoWithContext(ctx, "Drug recalls search completed", map[string]interface{}{
		"operation":     "search_drug_recalls",
		"drug_name":     req.DrugName,
		"recalls_count": len(response.Recalls),
		"total":         response.Total,
		"source":        response.Source,
		"status":        "success",
		"duration_ms":   duration.Milliseconds(),
		"request_id":    upstreamRequestID,
	})

	// Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleSearchDeviceEvents processes medical device adverse event search requests with full telemetry.
// 16-step handler pattern from stock-market-tool reference.
func (t *OpenFDATool) handleSearchDeviceEvents(rw http.ResponseWriter, r *http.Request) {
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

	// Step 4: Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "openfda-tool"),
		attribute.String("truvag3.capability", "search_device_events"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_device_events"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing device events search request", map[string]interface{}{
		"operation":  "search_device_events",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req SearchDeviceEventsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fda.errors.total",
			"capability", "search_device_events",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_device_events",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	if req.DeviceName == "" {
		telemetry.Counter("fda.errors.total",
			"capability", "search_device_events",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty device_name provided", map[string]interface{}{
			"operation":  "search_device_events",
			"request_id": upstreamRequestID,
			"error":      "device_name is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: device_name is required"))
		t.sendError(rw, "device_name is required", http.StatusBadRequest, "INVALID_DEVICE_NAME")
		return
	}

	// Add device_name to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("fda.device_name", req.DeviceName),
	)

	t.Logger.InfoWithContext(ctx, "Received device events search request", map[string]interface{}{
		"operation":   "search_device_events",
		"device_name": req.DeviceName,
		"limit":       req.Limit,
		"request_id":  upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_openfda_api",
		attribute.String("device_name", req.DeviceName),
		attribute.String("api", "device_event"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	result, err := t.client.SearchDeviceEvents(ctx, req.DeviceName, req.Limit)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("fda.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_device_events",
		"api", "openfda",
	)

	// Step 12: Error handling with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fda.api.errors.total",
			"capability", "search_device_events",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "OpenFDA API call failed", map[string]interface{}{
			"operation":   "search_device_events",
			"error":       err.Error(),
			"device_name": req.DeviceName,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "OpenFDA API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "OpenFDA API call successful", map[string]interface{}{
		"operation":    "search_device_events",
		"device_name":  req.DeviceName,
		"duration_ms":  apiDuration.Milliseconds(),
		"events_count": len(result.Results),
		"total":        result.Meta.Results.Total,
		"request_id":   upstreamRequestID,
	})

	// Convert FDA response to our response format
	events := make([]DeviceEventResponse, 0, len(result.Results))
	for _, de := range result.Results {
		ev := DeviceEventResponse{
			ReportNumber: de.ReportNumber,
			DateReceived: de.DateReceived,
			EventType:    de.EventType,
			Source:       "OpenFDA API",
		}

		// Extract device name from first device[] entry
		if len(de.Device) > 0 {
			ev.DeviceName = de.Device[0].GenericName
			ev.Manufacturer = de.Device[0].ManufacturerDName
			ev.BrandName = de.Device[0].BrandName
			ev.ProductCode = de.Device[0].DeviceReportProductCode
		}

		// Extract patient outcomes from patient[].sequence_number_outcome
		for _, pat := range de.Patient {
			for _, outcome := range pat.SequenceNumberOutcome {
				if outcome != "" {
					ev.PatientOutcome = append(ev.PatientOutcome, outcome)
				}
			}
		}

		// Extract event description from mdr_text[] where text_type_code is "Description of Event or Problem"
		for _, mdr := range de.MDRText {
			if mdr.TextType == "Description of Event or Problem" {
				ev.EventDescription = truncate(mdr.Text, 500)
				break
			}
		}

		events = append(events, ev)
	}

	response := SearchDeviceEventsResponse{
		DeviceName: req.DeviceName,
		Total:      result.Meta.Results.Total,
		Events:     events,
		Source:     "OpenFDA API",
	}

	// Add span attributes for result summary
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("fda.events.count", len(response.Events)),
		attribute.Int("fda.events.total", response.Total),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("fda.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_device_events",
	)
	telemetry.Counter("fda.requests.total",
		"capability", "search_device_events",
		"status", "success",
		"module", "tool",
	)

	// Step 14: RecordToolCall for unified dashboard metrics
	telemetry.RecordToolCall("openfda-tool", "search_device_events", float64(duration.Milliseconds()), "success")

	// Step 15: Completion span event
	telemetry.AddSpanEvent(ctx, "device_events_retrieved",
		attribute.String("device_name", req.DeviceName),
		attribute.Int("events_count", len(response.Events)),
		attribute.Int("total", response.Total),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion
	t.Logger.InfoWithContext(ctx, "Device events search completed", map[string]interface{}{
		"operation":    "search_device_events",
		"device_name":  req.DeviceName,
		"events_count": len(response.Events),
		"total":        response.Total,
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
