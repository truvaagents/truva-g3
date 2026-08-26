package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// relativeTimeRe matches relative time expressions like "now", "now-7d", "now-1h", "now-30m".
// Also matches template-style variants: "{{now}}", "{{now-7d}}".
var relativeTimeRe = regexp.MustCompile(`^\{\{?\s*(now(?:\s*-\s*(\d+)([smhdw]))?\s*)\}\}?$|^now(?:\s*-\s*(\d+)([smhdw]))?$`)

// resolveTimestamp converts relative time expressions to RFC3339 timestamps.
// Supported formats:
//   - "now"             → current time
//   - "now-7d"          → 7 days ago
//   - "now-1h"          → 1 hour ago
//   - "{{now}}"         → current time (template syntax)
//   - "{{now-7d}}"      → 7 days ago (template syntax)
//   - RFC3339 or unix timestamps are returned as-is.
func resolveTimestamp(s string) string {
	s = strings.TrimSpace(s)

	m := relativeTimeRe.FindStringSubmatch(s)
	if m == nil {
		return s // Already RFC3339 or unix — pass through
	}

	// Determine which capture groups matched (bare vs template syntax)
	var amountStr, unit string
	if m[2] != "" {
		amountStr, unit = m[2], m[3] // template syntax: {{now-7d}}
	} else if m[4] != "" {
		amountStr, unit = m[4], m[5] // bare syntax: now-7d
	}

	now := time.Now().UTC()
	if amountStr == "" {
		return now.Format(time.RFC3339) // just "now"
	}

	amount, err := strconv.Atoi(amountStr)
	if err != nil {
		return s // Can't parse — return as-is, let Prometheus report the error
	}

	var d time.Duration
	switch unit {
	case "s":
		d = time.Duration(amount) * time.Second
	case "m":
		d = time.Duration(amount) * time.Minute
	case "h":
		d = time.Duration(amount) * time.Hour
	case "d":
		d = time.Duration(amount) * 24 * time.Hour
	case "w":
		d = time.Duration(amount) * 7 * 24 * time.Hour
	default:
		return s
	}

	return now.Add(-d).Format(time.RFC3339)
}

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (p *PrometheusTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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
func (p *PrometheusTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
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

// handleQueryMetrics processes instant PromQL query requests with full telemetry
func (p *PrometheusTool) handleQueryMetrics(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers (helps clients locate traces)
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "prometheus-query-tool"),
		attribute.String("truvag3.capability", "query_metrics"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "query_metrics"),
	)

	p.Logger.InfoWithContext(ctx, "Processing query_metrics request", map[string]interface{}{
		"operation":  "query_metrics",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req QueryMetricsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("prometheus.errors.total",
			"capability", "query_metrics",
			"error_type", "decode_error",
			"module", "tool",
		)
		p.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "query_metrics",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		p.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 6. Validate input
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: query is required"))
		telemetry.Counter("prometheus.errors.total",
			"capability", "query_metrics",
			"error_type", "validation_error",
			"module", "tool",
		)
		p.Logger.ErrorWithContext(ctx, "Empty query provided", map[string]interface{}{
			"operation":  "query_metrics",
			"request_id": upstreamRequestID,
			"error":      "query is required",
			"error_type": "validation_error",
		})
		p.sendError(rw, "Query is required", http.StatusBadRequest, "INVALID_QUERY")
		return
	}

	// Resolve relative time expressions (e.g. "now", "now-1h") to RFC3339
	if req.Time != "" {
		req.Time = resolveTimestamp(req.Time)
	}

	// Add query to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("prometheus.query", req.Query),
		attribute.String("prometheus.time", req.Time),
	)

	p.Logger.InfoWithContext(ctx, "Received query_metrics request", map[string]interface{}{
		"operation":  "query_metrics",
		"query":      req.Query,
		"time":       req.Time,
		"request_id": upstreamRequestID,
	})

	// 7. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_prometheus_api",
		attribute.String("query", req.Query),
		attribute.String("api", "query"),
	)

	// 8. Call Prometheus API with timing
	apiStartTime := time.Now()
	queryData, warnings, err := p.client.QueryInstant(ctx, req.Query, req.Time)
	apiDuration := time.Since(apiStartTime)

	// 9. Record API latency as histogram metric
	telemetry.Histogram("prometheus.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "query_metrics",
		"api", "prometheus",
	)

	// 10. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("prometheus.api.errors.total",
			"capability", "query_metrics",
			"error_type", "api_error",
			"module", "tool",
		)
		p.Logger.ErrorWithContext(ctx, "Prometheus API call failed", map[string]interface{}{
			"operation":   "query_metrics",
			"error":       err.Error(),
			"query":       req.Query,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		p.sendUpstreamError(rw, "Prometheus API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	p.Logger.InfoWithContext(ctx, "Prometheus API call successful", map[string]interface{}{
		"operation":   "query_metrics",
		"query":       req.Query,
		"result_type": queryData.ResultType,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 11. Convert API response to tool response format
	// Parse vector results (instant query returns vectors)
	var samples []MetricSample
	if queryData.ResultType == "vector" {
		var vectorResults []PrometheusVectorResult
		if err := json.Unmarshal(queryData.Result, &vectorResults); err != nil {
			p.sendError(rw, "Failed to parse vector results: "+err.Error(), http.StatusBadGateway, "PARSE_ERROR")
			return
		}
		for _, vr := range vectorResults {
			ts, val, err := parseSampleValue(vr.Value)
			if err != nil {
				p.Logger.ErrorWithContext(ctx, "Failed to parse sample value", map[string]interface{}{
					"operation": "query_metrics",
					"error":     err.Error(),
					"metric":    fmt.Sprintf("%v", vr.Metric),
				})
				continue // Skip unparseable samples rather than failing the whole request
			}
			samples = append(samples, MetricSample{
				Labels:    vr.Metric,
				Timestamp: ts,
				Value:     val,
			})
		}
	}

	response := QueryMetricsResponse{
		Query:      req.Query,
		ResultType: queryData.ResultType,
		Samples:    samples,
		Warnings:   warnings,
		Source:     "Prometheus API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("prometheus.result_type", response.ResultType),
		attribute.Int("prometheus.sample_count", len(response.Samples)),
	)

	// 12. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("prometheus.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "query_metrics",
	)
	telemetry.Counter("prometheus.requests.total",
		"capability", "query_metrics",
		"status", "success",
		"module", "tool",
	)

	// 13. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("prometheus-query-tool", "query_metrics", float64(duration.Milliseconds()), "success")

	// 14. Add completion span event
	telemetry.AddSpanEvent(ctx, "query_metrics_completed",
		attribute.String("query", req.Query),
		attribute.Int("sample_count", len(response.Samples)),
		attribute.String("result_type", response.ResultType),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 15. Log completion with context
	p.Logger.InfoWithContext(ctx, "Query metrics request completed", map[string]interface{}{
		"operation":    "query_metrics",
		"query":        req.Query,
		"sample_count": len(response.Samples),
		"result_type":  response.ResultType,
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// 16. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleQueryRange processes range PromQL query requests with full telemetry.
// Follows the same 16-step handler checklist as handleQueryMetrics.
// Key differences:
//   - Decodes QueryRangeRequest (query, start, end, step -- all required)
//   - Calls p.client.QueryRange()
//   - Parses matrix results using PrometheusMatrixResult with "values" (plural)
//   - Returns QueryRangeResponse with []RangeSeries
func (p *PrometheusTool) handleQueryRange(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "prometheus-query-tool"),
		attribute.String("truvag3.capability", "query_range"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "query_range"),
	)

	p.Logger.InfoWithContext(ctx, "Processing query_range request", map[string]interface{}{
		"operation":  "query_range",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req QueryRangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("prometheus.errors.total",
			"capability", "query_range",
			"error_type", "decode_error",
			"module", "tool",
		)
		p.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "query_range",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		p.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 6. Validate required fields
	req.Query = strings.TrimSpace(req.Query)
	req.Start = strings.TrimSpace(req.Start)
	req.End = strings.TrimSpace(req.End)
	req.Step = strings.TrimSpace(req.Step)

	if req.Query == "" || req.Start == "" || req.End == "" || req.Step == "" {
		missing := []string{}
		if req.Query == "" {
			missing = append(missing, "query")
		}
		if req.Start == "" {
			missing = append(missing, "start")
		}
		if req.End == "" {
			missing = append(missing, "end")
		}
		if req.Step == "" {
			missing = append(missing, "step")
		}
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: missing required fields: %s", strings.Join(missing, ", ")))
		telemetry.Counter("prometheus.errors.total",
			"capability", "query_range",
			"error_type", "validation_error",
			"module", "tool",
		)
		p.Logger.ErrorWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "query_range",
			"request_id": upstreamRequestID,
			"missing":    strings.Join(missing, ", "),
			"error":      "missing required fields: " + strings.Join(missing, ", "),
			"error_type": "validation_error",
		})
		p.sendError(rw, fmt.Sprintf("Missing required fields: %s", strings.Join(missing, ", ")), http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	// Resolve relative time expressions (e.g. "now-7d", "{{now}}") to RFC3339
	req.Start = resolveTimestamp(req.Start)
	req.End = resolveTimestamp(req.End)

	// Add query params to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("prometheus.query", req.Query),
		attribute.String("prometheus.start", req.Start),
		attribute.String("prometheus.end", req.End),
		attribute.String("prometheus.step", req.Step),
	)

	p.Logger.InfoWithContext(ctx, "Received query_range request", map[string]interface{}{
		"operation":  "query_range",
		"query":      req.Query,
		"start":      req.Start,
		"end":        req.End,
		"step":       req.Step,
		"request_id": upstreamRequestID,
	})

	// 7. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_prometheus_api",
		attribute.String("query", req.Query),
		attribute.String("api", "query_range"),
	)

	// 8. Call Prometheus API with timing
	apiStartTime := time.Now()
	queryData, warnings, err := p.client.QueryRange(ctx, req.Query, req.Start, req.End, req.Step)
	apiDuration := time.Since(apiStartTime)

	// 9. Record API latency
	telemetry.Histogram("prometheus.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "query_range",
		"api", "prometheus",
	)

	// 10. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("prometheus.api.errors.total",
			"capability", "query_range",
			"error_type", "api_error",
			"module", "tool",
		)
		p.Logger.ErrorWithContext(ctx, "Prometheus API call failed", map[string]interface{}{
			"operation":   "query_range",
			"error":       err.Error(),
			"query":       req.Query,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		p.sendUpstreamError(rw, "Prometheus API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	p.Logger.InfoWithContext(ctx, "Prometheus API call successful", map[string]interface{}{
		"operation":   "query_range",
		"query":       req.Query,
		"result_type": queryData.ResultType,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 11. Parse matrix results (range query returns matrix)
	var series []RangeSeries
	if queryData.ResultType == "matrix" {
		var matrixResults []PrometheusMatrixResult
		if err := json.Unmarshal(queryData.Result, &matrixResults); err != nil {
			p.sendError(rw, "Failed to parse matrix results: "+err.Error(), http.StatusBadGateway, "PARSE_ERROR")
			return
		}
		for _, mr := range matrixResults {
			var values []RangeValue
			for _, rawVal := range mr.Values {
				ts, val, err := parseSampleValue(rawVal)
				if err != nil {
					continue // Skip unparseable values
				}
				values = append(values, RangeValue{
					Timestamp: ts,
					Value:     val,
				})
			}
			series = append(series, RangeSeries{
				Labels: mr.Metric,
				Values: values,
			})
		}
	}

	response := QueryRangeResponse{
		Query:      req.Query,
		ResultType: queryData.ResultType,
		Series:     series,
		Start:      req.Start,
		End:        req.End,
		Step:       req.Step,
		Warnings:   warnings,
		Source:     "Prometheus API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("prometheus.result_type", response.ResultType),
		attribute.Int("prometheus.series_count", len(response.Series)),
	)

	// 12. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("prometheus.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "query_range",
	)
	telemetry.Counter("prometheus.requests.total",
		"capability", "query_range",
		"status", "success",
		"module", "tool",
	)

	// 13. Record unified metrics
	telemetry.RecordToolCall("prometheus-query-tool", "query_range", float64(duration.Milliseconds()), "success")

	// 14. Add completion span event
	telemetry.AddSpanEvent(ctx, "query_range_completed",
		attribute.String("query", req.Query),
		attribute.Int("series_count", len(response.Series)),
		attribute.String("result_type", response.ResultType),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 15. Log completion
	p.Logger.InfoWithContext(ctx, "Query range request completed", map[string]interface{}{
		"operation":    "query_range",
		"query":        req.Query,
		"series_count": len(response.Series),
		"result_type":  response.ResultType,
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// 16. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleGetAlerts processes alert listing requests with full telemetry.
// Follows the same handler checklist. No request body params required.
// Key differences:
//   - No decode/validate step (empty request body)
//   - Calls p.client.GetAlerts()
//   - Counts total firing alerts across groups
//   - Returns GetAlertsResponse
func (p *PrometheusTool) handleGetAlerts(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "prometheus-query-tool"),
		attribute.String("truvag3.capability", "get_alerts"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_alerts"),
	)

	p.Logger.InfoWithContext(ctx, "Processing get_alerts request", map[string]interface{}{
		"operation":  "get_alerts",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. No decode/validate needed (no params)
	// Consume body to be safe (orchestrator may send empty JSON)
	// We don't fail on decode errors since this capability takes no params
	json.NewDecoder(r.Body).Decode(&struct{}{})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_prometheus_api",
		attribute.String("api", "alerts"),
	)

	// 7. Call Prometheus API with timing
	apiStartTime := time.Now()
	alertsData, _, err := p.client.GetAlerts(ctx)
	apiDuration := time.Since(apiStartTime)

	// 8. Record API latency
	telemetry.Histogram("prometheus.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_alerts",
		"api", "prometheus",
	)

	// 9. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("prometheus.api.errors.total",
			"capability", "get_alerts",
			"error_type", "api_error",
			"module", "tool",
		)
		p.Logger.ErrorWithContext(ctx, "Prometheus API call failed", map[string]interface{}{
			"operation":   "get_alerts",
			"error":       err.Error(),
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		p.sendUpstreamError(rw, "Prometheus API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	p.Logger.InfoWithContext(ctx, "Prometheus API call successful", map[string]interface{}{
		"operation":   "get_alerts",
		"alert_count": len(alertsData.Alerts),
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 10. Convert to response format
	alerts := make([]AlertInfo, len(alertsData.Alerts))
	for i, a := range alertsData.Alerts {
		alerts[i] = AlertInfo{
			Labels:      a.Labels,
			Annotations: a.Annotations,
			State:       a.State,
			ActiveAt:    a.ActiveAt,
			Value:       a.Value,
		}
	}

	response := GetAlertsResponse{
		Groups: []AlertGroup{
			{
				Name:   "all",
				Alerts: alerts,
			},
		},
		TotalAlerts: len(alerts),
		Source:      "Prometheus API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("prometheus.alert_count", response.TotalAlerts),
	)

	// 11. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("prometheus.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_alerts",
	)
	telemetry.Counter("prometheus.requests.total",
		"capability", "get_alerts",
		"status", "success",
		"module", "tool",
	)

	// 12. Record unified metrics
	telemetry.RecordToolCall("prometheus-query-tool", "get_alerts", float64(duration.Milliseconds()), "success")

	// 13. Add completion span event
	telemetry.AddSpanEvent(ctx, "get_alerts_completed",
		attribute.Int("alert_count", response.TotalAlerts),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 14. Log completion
	p.Logger.InfoWithContext(ctx, "Get alerts request completed", map[string]interface{}{
		"operation":   "get_alerts",
		"alert_count": response.TotalAlerts,
		"source":      response.Source,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 15. Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleGetTargets processes scrape target listing requests with full telemetry.
// Follows the same handler checklist.
// Key differences:
//   - Decodes GetTargetsRequest with optional state filter
//   - Calls p.client.GetTargets(state)
//   - Converts PrometheusTarget to TargetInfo
//   - Returns GetTargetsResponse
func (p *PrometheusTool) handleGetTargets(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "prometheus-query-tool"),
		attribute.String("truvag3.capability", "get_targets"),
	)

	// 4. Add span event
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_targets"),
	)

	p.Logger.InfoWithContext(ctx, "Processing get_targets request", map[string]interface{}{
		"operation":  "get_targets",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request (optional state param)
	var req GetTargetsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// State is optional so we don't fail, but log the decode error for observability
		telemetry.RecordSpanError(ctx, err)
		p.Logger.WarnWithContext(ctx, "Failed to decode request, using defaults", map[string]interface{}{
			"operation":  "get_targets",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		req.State = ""
	}

	req.State = strings.TrimSpace(strings.ToLower(req.State))

	// Add state to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("prometheus.target_state", req.State),
	)

	p.Logger.InfoWithContext(ctx, "Received get_targets request", map[string]interface{}{
		"operation":  "get_targets",
		"state":      req.State,
		"request_id": upstreamRequestID,
	})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_prometheus_api",
		attribute.String("api", "targets"),
		attribute.String("state", req.State),
	)

	// 7. Call Prometheus API with timing
	apiStartTime := time.Now()
	targetsData, _, err := p.client.GetTargets(ctx, req.State)
	apiDuration := time.Since(apiStartTime)

	// 8. Record API latency
	telemetry.Histogram("prometheus.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_targets",
		"api", "prometheus",
	)

	// 9. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("prometheus.api.errors.total",
			"capability", "get_targets",
			"error_type", "api_error",
			"module", "tool",
		)
		p.Logger.ErrorWithContext(ctx, "Prometheus API call failed", map[string]interface{}{
			"operation":   "get_targets",
			"error":       err.Error(),
			"state":       req.State,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		p.sendUpstreamError(rw, "Prometheus API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	p.Logger.InfoWithContext(ctx, "Prometheus API call successful", map[string]interface{}{
		"operation":     "get_targets",
		"active_count":  len(targetsData.ActiveTargets),
		"dropped_count": len(targetsData.DroppedTargets),
		"duration_ms":   apiDuration.Milliseconds(),
		"request_id":    upstreamRequestID,
	})

	// 10. Convert to response format
	activeTargets := make([]TargetInfo, len(targetsData.ActiveTargets))
	for i, t := range targetsData.ActiveTargets {
		activeTargets[i] = TargetInfo{
			Labels:        t.Labels,
			ScrapeURL:     t.ScrapeURL,
			Health:        t.Health,
			LastError:     t.LastError,
			LastScrape:    t.LastScrape,
			LastScrapeDur: t.LastScrapeDuration,
		}
	}

	response := GetTargetsResponse{
		ActiveTargets:  activeTargets,
		DroppedTargets: len(targetsData.DroppedTargets),
		State:          req.State,
		Source:         "Prometheus API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("prometheus.active_targets", len(response.ActiveTargets)),
		attribute.Int("prometheus.dropped_targets", response.DroppedTargets),
	)

	// 11. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("prometheus.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_targets",
	)
	telemetry.Counter("prometheus.requests.total",
		"capability", "get_targets",
		"status", "success",
		"module", "tool",
	)

	// 12. Record unified metrics
	telemetry.RecordToolCall("prometheus-query-tool", "get_targets", float64(duration.Milliseconds()), "success")

	// 13. Add completion span event
	telemetry.AddSpanEvent(ctx, "get_targets_completed",
		attribute.Int("active_targets", len(response.ActiveTargets)),
		attribute.Int("dropped_targets", response.DroppedTargets),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 14. Log completion
	p.Logger.InfoWithContext(ctx, "Get targets request completed", map[string]interface{}{
		"operation":       "get_targets",
		"active_targets":  len(response.ActiveTargets),
		"dropped_targets": response.DroppedTargets,
		"state":           req.State,
		"source":          response.Source,
		"status":          "success",
		"duration_ms":     duration.Milliseconds(),
		"request_id":      upstreamRequestID,
	})

	// 15. Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
