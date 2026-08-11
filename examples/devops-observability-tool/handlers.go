package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Error codes
const (
	ErrCodeInvalidRequest = "INVALID_REQUEST"
	ErrCodeMissingField   = "MISSING_FIELD"
)

// ---------------------------------------------------------------------------
// Helper: extract handler context (shared by all handlers)
// ---------------------------------------------------------------------------

type handlerCtx struct {
	requestID string
	startTime time.Time
}

func (t *LogsTool) extractCtx(r *http.Request, operation string) (handlerCtx, *http.Request) {
	ctx := r.Context()
	hc := handlerCtx{startTime: time.Now()}

	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		hc.requestID = baggage["request_id"]
	}
	if hc.requestID == "" {
		hc.requestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, fmt.Sprintf("Processing %s request", operation), map[string]interface{}{
			"operation":  operation,
			"method":     r.Method,
			"path":       r.URL.Path,
			"request_id": hc.requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", hc.requestID),
		attribute.String("operation", operation),
	)

	return hc, r
}

// recordSuccess records metrics and logs for a successful handler completion.
// apiLatency is the upstream Loki API call duration (pass 0 if not applicable).
func (t *LogsTool) recordSuccess(r *http.Request, hc handlerCtx, operation string, apiLatency time.Duration, extraAttrs ...attribute.KeyValue) {
	ctx := r.Context()
	duration := time.Since(hc.startTime)

	telemetry.Counter("logs.requests.total",
		"module", "devops-observability-tool",
		"capability", operation,
		"status", "success",
	)
	telemetry.Histogram("logs.request.duration_ms",
		float64(duration.Milliseconds()),
		"module", "devops-observability-tool",
		"capability", operation,
	)
	telemetry.RecordToolCall("devops-observability-tool", operation,
		float64(duration.Milliseconds()), "success")

	attrs := []attribute.KeyValue{
		attribute.String("request_id", hc.requestID),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	}
	attrs = append(attrs, extraAttrs...)
	telemetry.AddSpanEvent(ctx, operation+"_completed", attrs...)

	if t.Logger != nil {
		logFields := map[string]interface{}{
			"operation":   operation,
			"request_id":  hc.requestID,
			"status":      "success",
			"duration_ms": duration.Milliseconds(),
		}
		if apiLatency > 0 {
			logFields["api_latency"] = apiLatency.String()
		}
		t.Logger.InfoWithContext(ctx, operation+" completed", logFields)
	}
}

// recordError records metrics and logs for a handler error.
func (t *LogsTool) recordError(r *http.Request, hc handlerCtx, operation, errorType string, err error) {
	ctx := r.Context()
	telemetry.RecordSpanError(ctx, err)
	telemetry.Counter("logs.errors.total",
		"module", "devops-observability-tool",
		"capability", operation,
		"error_type", errorType,
	)
	telemetry.RecordToolCall("devops-observability-tool", operation,
		float64(time.Since(hc.startTime).Milliseconds()), "error")

	if t.Logger != nil {
		t.Logger.WarnWithContext(ctx, operation+" failed", map[string]interface{}{
			"operation":   operation,
			"error":       err.Error(),
			"error_type":  errorType,
			"request_id":  hc.requestID,
			"status":      "failure",
			"duration_ms": time.Since(hc.startTime).Milliseconds(),
		})
	}
}

// ---------------------------------------------------------------------------
// query_logs handler
// ---------------------------------------------------------------------------

func (t *LogsTool) handleQueryLogs(w http.ResponseWriter, r *http.Request) {
	hc, r := t.extractCtx(r, "query_logs")
	ctx := r.Context()

	var req QueryLogsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordError(r, hc, "query_logs", "decode_error", err)
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		err := fmt.Errorf("query is required")
		t.recordError(r, hc, "query_logs", "validation_error", err)
		t.sendError(w, "query field is required", http.StatusBadRequest, ErrCodeMissingField)
		return
	}

	// Defaults
	if req.Limit <= 0 {
		req.Limit = 100
	}
	if req.Limit > 1000 {
		req.Limit = 1000
	}
	if req.Direction == "" {
		req.Direction = "backward"
	}
	if req.Since == "" {
		req.Since = "1h"
	}

	// Compute start/end from since
	sinceDuration, err := parseDuration(req.Since)
	if err != nil {
		t.recordError(r, hc, "query_logs", "validation_error", err)
		t.sendError(w, fmt.Sprintf("Invalid since value: %v", err), http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	now := time.Now().UTC()
	start := now.Add(-sinceDuration).Format(time.RFC3339Nano)
	end := now.Format(time.RFC3339Nano)

	telemetry.AddSpanEvent(ctx, "calling_loki_api",
		attribute.String("request_id", hc.requestID),
		attribute.String("api", "query_range"),
		attribute.String("query", req.Query),
	)

	apiStartTime := time.Now()
	streams, apiErr := t.lokiClient.QueryRange(ctx, req.Query, start, end, "", req.Direction, req.Limit)
	apiDuration := time.Since(apiStartTime)

	if apiErr != nil {
		t.recordError(r, hc, "query_logs", "api_error", apiErr)
		t.sendUpstreamError(w, fmt.Sprintf("Log query failed: %v", apiErr), core.ClassifyUpstreamError(apiErr))
		return
	}

	resp := buildLogsResponse(streams, req.Query)
	if resp.TotalEntries == 0 {
		resp.Hint = t.unknownLabelHint(ctx, req.Query)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := encodeToolResponse(w, core.ToolResponse{Success: true, Data: resp}); err != nil {
		t.recordError(r, hc, "query_logs", "response_encode", err)
		return
	}

	t.recordSuccess(r, hc, "query_logs", apiDuration,
		attribute.Int("total_entries", resp.TotalEntries),
		attribute.Int("stream_count", len(resp.Streams)),
	)
}

// ---------------------------------------------------------------------------
// query_logs_range handler
// ---------------------------------------------------------------------------

func (t *LogsTool) handleQueryLogsRange(w http.ResponseWriter, r *http.Request) {
	hc, r := t.extractCtx(r, "query_logs_range")
	ctx := r.Context()

	var req QueryLogsRangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordError(r, hc, "query_logs_range", "decode_error", err)
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Validate required fields
	req.Query = strings.TrimSpace(req.Query)
	req.Start = strings.TrimSpace(req.Start)
	req.End = strings.TrimSpace(req.End)

	var missing []string
	if req.Query == "" {
		missing = append(missing, "query")
	}
	if req.Start == "" {
		missing = append(missing, "start")
	}
	if req.End == "" {
		missing = append(missing, "end")
	}
	if len(missing) > 0 {
		err := fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
		t.recordError(r, hc, "query_logs_range", "validation_error", err)
		t.sendError(w, err.Error(), http.StatusBadRequest, ErrCodeMissingField)
		return
	}

	// Defaults
	if req.Limit <= 0 {
		req.Limit = 100
	}
	if req.Limit > 1000 {
		req.Limit = 1000
	}
	if req.Direction == "" {
		req.Direction = "backward"
	}

	telemetry.AddSpanEvent(ctx, "calling_loki_api",
		attribute.String("request_id", hc.requestID),
		attribute.String("api", "query_range"),
		attribute.String("query", req.Query),
	)

	apiStartTime := time.Now()
	streams, apiErr := t.lokiClient.QueryRange(ctx, req.Query, req.Start, req.End, req.Step, req.Direction, req.Limit)
	apiDuration := time.Since(apiStartTime)

	if apiErr != nil {
		t.recordError(r, hc, "query_logs_range", "api_error", apiErr)
		t.sendUpstreamError(w, fmt.Sprintf("Log query failed: %v", apiErr), core.ClassifyUpstreamError(apiErr))
		return
	}

	resp := buildLogsResponse(streams, req.Query)
	if resp.TotalEntries == 0 {
		resp.Hint = t.unknownLabelHint(ctx, req.Query)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := encodeToolResponse(w, core.ToolResponse{Success: true, Data: resp}); err != nil {
		t.recordError(r, hc, "query_logs_range", "response_encode", err)
		return
	}

	t.recordSuccess(r, hc, "query_logs_range", apiDuration,
		attribute.Int("total_entries", resp.TotalEntries),
	)
}

// ---------------------------------------------------------------------------
// get_labels handler
// ---------------------------------------------------------------------------

func (t *LogsTool) handleGetLabels(w http.ResponseWriter, r *http.Request) {
	hc, r := t.extractCtx(r, "get_labels")
	ctx := r.Context()

	var req GetLabelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordError(r, hc, "get_labels", "decode_error", err)
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.Since == "" {
		req.Since = "24h"
	}

	sinceDuration, err := parseDuration(req.Since)
	if err != nil {
		t.recordError(r, hc, "get_labels", "validation_error", err)
		t.sendError(w, fmt.Sprintf("Invalid since value: %v", err), http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	now := time.Now().UTC()
	start := now.Add(-sinceDuration).Format(time.RFC3339Nano)
	end := now.Format(time.RFC3339Nano)

	telemetry.AddSpanEvent(ctx, "calling_loki_api",
		attribute.String("request_id", hc.requestID),
		attribute.String("api", "labels"),
	)

	apiStartTime := time.Now()
	labels, apiErr := t.lokiClient.Labels(ctx, start, end)
	apiDuration := time.Since(apiStartTime)

	if apiErr != nil {
		t.recordError(r, hc, "get_labels", "api_error", apiErr)
		t.sendUpstreamError(w, fmt.Sprintf("Labels query failed: %v", apiErr), core.ClassifyUpstreamError(apiErr))
		return
	}

	resp := LabelsResponse{
		Labels: labels,
		Count:  len(labels),
		Source: "loki",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := encodeToolResponse(w, core.ToolResponse{Success: true, Data: resp}); err != nil {
		t.recordError(r, hc, "get_labels", "response_encode", err)
		return
	}

	t.recordSuccess(r, hc, "get_labels", apiDuration,
		attribute.Int("label_count", len(labels)),
	)
}

// ---------------------------------------------------------------------------
// get_label_values handler
// ---------------------------------------------------------------------------

func (t *LogsTool) handleGetLabelValues(w http.ResponseWriter, r *http.Request) {
	hc, r := t.extractCtx(r, "get_label_values")
	ctx := r.Context()

	var req GetLabelValuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordError(r, hc, "get_label_values", "decode_error", err)
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		err := fmt.Errorf("label is required")
		t.recordError(r, hc, "get_label_values", "validation_error", err)
		t.sendError(w, "label field is required", http.StatusBadRequest, ErrCodeMissingField)
		return
	}

	if req.Since == "" {
		req.Since = "24h"
	}

	sinceDuration, err := parseDuration(req.Since)
	if err != nil {
		t.recordError(r, hc, "get_label_values", "validation_error", err)
		t.sendError(w, fmt.Sprintf("Invalid since value: %v", err), http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	now := time.Now().UTC()
	start := now.Add(-sinceDuration).Format(time.RFC3339Nano)
	end := now.Format(time.RFC3339Nano)

	telemetry.AddSpanEvent(ctx, "calling_loki_api",
		attribute.String("request_id", hc.requestID),
		attribute.String("api", "label_values"),
		attribute.String("label", req.Label),
	)

	apiStartTime := time.Now()
	values, apiErr := t.lokiClient.LabelValues(ctx, req.Label, start, end, req.Query)
	apiDuration := time.Since(apiStartTime)

	if apiErr != nil {
		t.recordError(r, hc, "get_label_values", "api_error", apiErr)
		t.sendUpstreamError(w, fmt.Sprintf("Label values query failed: %v", apiErr), core.ClassifyUpstreamError(apiErr))
		return
	}

	resp := LabelValuesResponse{
		Label:  req.Label,
		Values: values,
		Count:  len(values),
		Source: "loki",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := encodeToolResponse(w, core.ToolResponse{Success: true, Data: resp}); err != nil {
		t.recordError(r, hc, "get_label_values", "response_encode", err)
		return
	}

	t.recordSuccess(r, hc, "get_label_values", apiDuration,
		attribute.Int("value_count", len(values)),
	)
}

// ---------------------------------------------------------------------------
// get_detected_fields handler
// ---------------------------------------------------------------------------

func (t *LogsTool) handleGetDetectedFields(w http.ResponseWriter, r *http.Request) {
	hc, r := t.extractCtx(r, "get_detected_fields")
	ctx := r.Context()

	var req GetDetectedFieldsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordError(r, hc, "get_detected_fields", "decode_error", err)
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		err := fmt.Errorf("query is required")
		t.recordError(r, hc, "get_detected_fields", "validation_error", err)
		t.sendError(w, "query field is required", http.StatusBadRequest, ErrCodeMissingField)
		return
	}

	if req.Since == "" {
		req.Since = "1h"
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}

	sinceDuration, err := parseDuration(req.Since)
	if err != nil {
		t.recordError(r, hc, "get_detected_fields", "validation_error", err)
		t.sendError(w, fmt.Sprintf("Invalid since value: %v", err), http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	now := time.Now().UTC()
	start := now.Add(-sinceDuration).Format(time.RFC3339Nano)
	end := now.Format(time.RFC3339Nano)

	telemetry.AddSpanEvent(ctx, "calling_loki_api",
		attribute.String("request_id", hc.requestID),
		attribute.String("api", "detected_fields"),
		attribute.String("query", req.Query),
	)

	apiStartTime := time.Now()
	fields, apiErr := t.lokiClient.DetectedFields(ctx, req.Query, start, end, req.Limit)
	apiDuration := time.Since(apiStartTime)

	if apiErr != nil {
		t.recordError(r, hc, "get_detected_fields", "api_error", apiErr)
		t.sendUpstreamError(w, fmt.Sprintf("Detected fields query failed: %v", apiErr), core.ClassifyUpstreamError(apiErr))
		return
	}

	// Convert to response type
	respFields := make([]DetectedField, 0, len(fields))
	for _, f := range fields {
		respFields = append(respFields, DetectedField(f))
	}

	resp := DetectedFieldsResponse{
		Fields:      respFields,
		TotalFields: len(respFields),
		Query:       req.Query,
		Source:      "loki",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := encodeToolResponse(w, core.ToolResponse{Success: true, Data: resp}); err != nil {
		t.recordError(r, hc, "get_detected_fields", "response_encode", err)
		return
	}

	t.recordSuccess(r, hc, "get_detected_fields", apiDuration,
		attribute.Int("field_count", len(respFields)),
	)
}

// ---------------------------------------------------------------------------
// get_trace_services handler
// ---------------------------------------------------------------------------

func (t *LogsTool) handleGetTraceServices(w http.ResponseWriter, r *http.Request) {
	hc, r := t.extractCtx(r, "get_trace_services")
	ctx := r.Context()

	// Decode body (may be empty — no required fields)
	_ = json.NewDecoder(r.Body).Decode(&struct{}{})

	telemetry.AddSpanEvent(ctx, "calling_jaeger_api",
		attribute.String("request_id", hc.requestID),
		attribute.String("api", "services"),
	)

	apiStartTime := time.Now()
	services, apiErr := t.jaegerClient.Services(ctx)
	apiDuration := time.Since(apiStartTime)

	if apiErr != nil {
		t.recordError(r, hc, "get_trace_services", "api_error", apiErr)
		t.sendUpstreamError(w, fmt.Sprintf("Trace services query failed: %v", apiErr), core.ClassifyUpstreamError(apiErr))
		return
	}

	resp := TraceServicesResponse{
		Services: services,
		Count:    len(services),
		Source:   "jaeger",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := encodeToolResponse(w, core.ToolResponse{Success: true, Data: resp}); err != nil {
		t.recordError(r, hc, "get_trace_services", "response_encode", err)
		return
	}

	t.recordSuccess(r, hc, "get_trace_services", apiDuration,
		attribute.Int("service_count", len(services)),
	)
}

// ---------------------------------------------------------------------------
// get_trace_operations handler
// ---------------------------------------------------------------------------

func (t *LogsTool) handleGetTraceOperations(w http.ResponseWriter, r *http.Request) {
	hc, r := t.extractCtx(r, "get_trace_operations")
	ctx := r.Context()

	var req GetTraceOperationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordError(r, hc, "get_trace_operations", "decode_error", err)
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.Service = strings.TrimSpace(req.Service)
	if req.Service == "" {
		err := fmt.Errorf("service is required")
		t.recordError(r, hc, "get_trace_operations", "validation_error", err)
		t.sendError(w, "service field is required", http.StatusBadRequest, ErrCodeMissingField)
		return
	}

	telemetry.AddSpanEvent(ctx, "calling_jaeger_api",
		attribute.String("request_id", hc.requestID),
		attribute.String("api", "operations"),
		attribute.String("service", req.Service),
	)

	apiStartTime := time.Now()
	operations, apiErr := t.jaegerClient.Operations(ctx, req.Service)
	apiDuration := time.Since(apiStartTime)

	if apiErr != nil {
		t.recordError(r, hc, "get_trace_operations", "api_error", apiErr)
		t.sendUpstreamError(w, fmt.Sprintf("Trace operations query failed: %v", apiErr), core.ClassifyUpstreamError(apiErr))
		return
	}

	resp := TraceOperationsResponse{
		Service:    req.Service,
		Operations: operations,
		Count:      len(operations),
		Source:     "jaeger",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := encodeToolResponse(w, core.ToolResponse{Success: true, Data: resp}); err != nil {
		t.recordError(r, hc, "get_trace_operations", "response_encode", err)
		return
	}

	t.recordSuccess(r, hc, "get_trace_operations", apiDuration,
		attribute.Int("operation_count", len(operations)),
	)
}

// ---------------------------------------------------------------------------
// find_traces handler
// ---------------------------------------------------------------------------

func (t *LogsTool) handleFindTraces(w http.ResponseWriter, r *http.Request) {
	hc, r := t.extractCtx(r, "find_traces")
	ctx := r.Context()

	var req FindTracesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordError(r, hc, "find_traces", "decode_error", err)
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.Service = strings.TrimSpace(req.Service)
	if req.Service == "" {
		err := fmt.Errorf("service is required")
		t.recordError(r, hc, "find_traces", "validation_error", err)
		t.sendError(w, "service field is required", http.StatusBadRequest, ErrCodeMissingField)
		return
	}

	if req.Lookback == "" {
		req.Lookback = "1h"
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	telemetry.AddSpanEvent(ctx, "calling_jaeger_api",
		attribute.String("request_id", hc.requestID),
		attribute.String("api", "find_traces"),
		attribute.String("service", req.Service),
	)

	apiStartTime := time.Now()
	traces, apiErr := t.jaegerClient.FindTraces(ctx, req.Service, req.Operation, req.Lookback, req.MinDuration, req.MaxDuration, req.Limit)
	apiDuration := time.Since(apiStartTime)

	if apiErr != nil {
		t.recordError(r, hc, "find_traces", "api_error", apiErr)
		t.sendUpstreamError(w, fmt.Sprintf("Trace search failed: %v", apiErr), core.ClassifyUpstreamError(apiErr))
		return
	}

	// Convert to response format
	summaries := make([]TraceSummary, 0, len(traces))
	for i := range traces {
		trace := &traces[i]
		var maxDuration int64
		var minStart int64 = 1<<63 - 1
		for _, span := range trace.Spans {
			if span.Duration > maxDuration {
				maxDuration = span.Duration
			}
			if span.StartTime < minStart {
				minStart = span.StartTime
			}
		}

		summaries = append(summaries, TraceSummary{
			TraceID:       trace.TraceID,
			SpanCount:     len(trace.Spans),
			Services:      extractServices(trace),
			DurationMs:    float64(maxDuration) / 1000.0,
			StartTime:     spanStartTimeToRFC3339(minStart),
			RootOperation: extractRootOperation(trace),
			RequestID:     extractRequestID(trace),
		})
	}

	resp := FindTracesResponse{
		Traces:      summaries,
		TotalTraces: len(summaries),
		Source:      "jaeger",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := encodeToolResponse(w, core.ToolResponse{Success: true, Data: resp}); err != nil {
		t.recordError(r, hc, "find_traces", "response_encode", err)
		return
	}

	t.recordSuccess(r, hc, "find_traces", apiDuration,
		attribute.Int("trace_count", len(summaries)),
	)
}

// ---------------------------------------------------------------------------
// get_trace handler
// ---------------------------------------------------------------------------

func (t *LogsTool) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	hc, r := t.extractCtx(r, "get_trace")
	ctx := r.Context()

	var req GetTraceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordError(r, hc, "get_trace", "decode_error", err)
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.TraceID = strings.TrimSpace(req.TraceID)
	if req.TraceID == "" {
		err := fmt.Errorf("trace_id is required")
		t.recordError(r, hc, "get_trace", "validation_error", err)
		t.sendError(w, "trace_id field is required", http.StatusBadRequest, ErrCodeMissingField)
		return
	}

	telemetry.AddSpanEvent(ctx, "calling_jaeger_api",
		attribute.String("request_id", hc.requestID),
		attribute.String("api", "get_trace"),
		attribute.String("trace_id", req.TraceID),
	)

	apiStartTime := time.Now()
	trace, apiErr := t.jaegerClient.GetTrace(ctx, req.TraceID)
	apiDuration := time.Since(apiStartTime)

	if apiErr != nil {
		t.recordError(r, hc, "get_trace", "api_error", apiErr)
		t.sendUpstreamError(w, fmt.Sprintf("Get trace failed: %v", apiErr), core.ClassifyUpstreamError(apiErr))
		return
	}

	// Build span list and error spans
	spans := make([]SpanInfo, 0, len(trace.Spans))
	errorSpans := make([]ErrorSpan, 0)

	for _, s := range trace.Spans {
		// Resolve service name from processID
		serviceName := s.ProcessID
		if proc, ok := trace.Processes[s.ProcessID]; ok {
			serviceName = proc.ServiceName
		}

		// Extract tags as string map
		tags := make(map[string]string)
		hasError := false
		var errorMsg string
		for _, tag := range s.Tags {
			tags[tag.Key] = tagValueToString(tag.Value)
			if tag.Key == "error" {
				if b, ok := tag.Value.(bool); ok && b {
					hasError = true
				}
			}
			if tag.Key == "otel.status_description" {
				if desc, ok := tag.Value.(string); ok {
					errorMsg = desc
				}
			}
		}

		status := "ok"
		if hasError {
			status = "error"
		}

		spans = append(spans, SpanInfo{
			SpanID:       s.SpanID,
			ParentSpanID: s.ParentSpanID,
			Operation:    s.OperationName,
			Service:      serviceName,
			DurationMs:   float64(s.Duration) / 1000.0,
			StartTime:    spanStartTimeToRFC3339(s.StartTime),
			Status:       status,
			Tags:         tags,
			HasError:     hasError,
		})

		if hasError {
			if errorMsg == "" {
				errorMsg = "error (no description)"
			}
			errorSpans = append(errorSpans, ErrorSpan{
				Operation:  s.OperationName,
				Service:    serviceName,
				DurationMs: float64(s.Duration) / 1000.0,
				Error:      errorMsg,
			})
		}
	}

	resp := GetTraceResponse{
		TraceID:    trace.TraceID,
		SpanCount:  len(spans),
		Services:   extractServices(trace),
		RequestID:  extractRequestID(trace),
		Spans:      spans,
		ErrorSpans: errorSpans,
		Source:     "jaeger",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := encodeToolResponse(w, core.ToolResponse{Success: true, Data: resp}); err != nil {
		t.recordError(r, hc, "get_trace", "response_encode", err)
		return
	}

	t.recordSuccess(r, hc, "get_trace", apiDuration,
		attribute.Int("span_count", len(spans)),
		attribute.Int("error_span_count", len(errorSpans)),
	)
}

// ---------------------------------------------------------------------------
// Error helpers
// ---------------------------------------------------------------------------

// sendError sends a structured error response for local validation errors.
func (t *LogsTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = encodeToolResponse(rw, core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: status >= 500,
		},
	})
}

// sendUpstreamError sends a structured error response for Loki API failures.
func (t *LogsTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(info.HTTPStatus)
	_ = encodeToolResponse(rw, core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      info.Code,
			Message:   message,
			Category:  info.Category,
			Retryable: info.Retryable,
		},
	})
}

func encodeToolResponse(writer http.ResponseWriter, response core.ToolResponse) error {
	return json.NewEncoder(writer).Encode(response)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// buildLogsResponse converts Loki streams to the tool response format.
func buildLogsResponse(streams []lokiStream, query string) LogsQueryResponse {
	respStreams := make([]LogStream, 0, len(streams))
	totalEntries := 0

	for _, s := range streams {
		// Serialize labels as string
		labelsJSON, _ := json.Marshal(s.Stream)

		entries := make([]LogEntry, 0, len(s.Values))
		for _, v := range s.Values {
			if len(v) < 2 {
				continue
			}
			// v[0] = timestamp in nanoseconds, v[1] = log line with CRI prefix
			ts := parseNanoTimestamp(v[0])
			// Loki contains untrusted application output. Redact credentials before a
			// line leaves this tool so historical leaks cannot be copied into an agent
			// prompt, execution record, trace, or Registry Viewer response.
			line := core.RedactSensitiveText(stripCRIPrefix(v[1]))

			entries = append(entries, LogEntry{
				Timestamp: ts,
				Line:      line,
			})
		}

		respStreams = append(respStreams, LogStream{
			Labels:  string(labelsJSON),
			Entries: entries,
		})
		totalEntries += len(entries)
	}

	return LogsQueryResponse{
		Streams:      respStreams,
		TotalEntries: totalEntries,
		Query:        query,
		Source:       "loki",
	}
}

// labelSuggestions maps common (but non-indexed) selector label names to the
// indexed equivalent Loki actually uses, so an empty result can suggest a fix.
var labelSuggestions = map[string]string{
	"pod":        "k8s_pod_name",
	"namespace":  "k8s_namespace_name",
	"container":  "k8s_pod_name",
	"app":        "k8s_deployment_name",
	"deployment": "k8s_deployment_name",
	"service":    "service_name",
	"job":        "service_name",
}

// selectorLabelNames extracts the label names used in the stream selector (the
// first {...} block) of a LogQL query. Label filters after a pipe (e.g.
// `| level="ERROR"`) reference structured metadata, not stream labels, and are
// intentionally excluded.
func selectorLabelNames(query string) []string {
	open := strings.Index(query, "{")
	if open < 0 {
		return nil
	}
	rel := strings.Index(query[open:], "}")
	if rel < 0 {
		return nil
	}
	inner := query[open+1 : open+rel]

	var names []string
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		idx := strings.IndexAny(part, "=!~")
		if idx <= 0 {
			continue
		}
		if name := strings.TrimSpace(part[:idx]); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// unknownLabelHint returns a diagnostic note when an empty result is likely
// caused by the stream selector referencing labels that are not indexed in
// Loki. It returns "" when the selector looks fine or the label list can't be
// fetched — a best-effort aid, never a hard failure. This is the
// "misleading empty result" guard from the tool-development guide: a structural
// no-op selector should not read as a clean "no logs found".
func (t *LogsTool) unknownLabelHint(ctx context.Context, query string) string {
	names := selectorLabelNames(query)
	if len(names) == 0 {
		return ""
	}

	known, err := t.lokiClient.Labels(ctx, "", "")
	if err != nil || len(known) == 0 {
		return ""
	}
	knownSet := make(map[string]struct{}, len(known))
	for _, k := range known {
		knownSet[k] = struct{}{}
	}

	var unknown []string
	for _, n := range names {
		if _, ok := knownSet[n]; ok {
			continue
		}
		if sug, has := labelSuggestions[n]; has {
			unknown = append(unknown, fmt.Sprintf("%q (did you mean %q?)", n, sug))
		} else {
			unknown = append(unknown, fmt.Sprintf("%q", n))
		}
	}
	if len(unknown) == 0 {
		return ""
	}

	return fmt.Sprintf(
		"Zero results may be because the selector references non-indexed stream label(s): %s. Indexed labels are: %s. Re-run with a valid label, or call get_labels to discover the schema.",
		strings.Join(unknown, ", "),
		strings.Join(known, ", "),
	)
}

// parseNanoTimestamp converts a nanosecond epoch string to RFC3339Nano.
func parseNanoTimestamp(nanos string) string {
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return nanos // Return as-is if unparseable
	}
	return time.Unix(0, n).UTC().Format(time.RFC3339Nano)
}

// parseDuration parses duration strings like "1h", "30m", "2h", "6h".
func parseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: expected format like 30m, 1h, 6h", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration must be positive, got %s", s)
	}
	return d, nil
}
