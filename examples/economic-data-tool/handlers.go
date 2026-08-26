package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const serviceName = "economic-data-tool"

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (t *EconomicDataTool) sendUpstreamError(w http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(info.HTTPStatus)
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      info.Code,
			Message:   message,
			Category:  info.Category,
			Retryable: info.Retryable,
		},
	})
}

// handleEconomicIndicator returns values for a specific economic indicator
func (t *EconomicDataTool) handleEconomicIndicator(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "economic_indicator"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "economic_indicator"),
	)

	t.Logger.InfoWithContext(ctx, "Economic indicator request received", map[string]interface{}{
		"operation":  "economic_indicator",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req EconomicIndicatorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "economic_indicator", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "economic_indicator",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.Indicator = strings.TrimSpace(req.Indicator)
	if req.Indicator == "" {
		telemetry.Counter("economic.errors.total", "capability", "economic_indicator", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Empty indicator provided", map[string]interface{}{
			"operation":  "economic_indicator",
			"request_id": requestID,
			"error":      "indicator is required",
			"error_type": "validation_error",
		})
		t.sendError(w, "indicator is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	seriesID := resolveSeriesID(req.Indicator)
	if req.Limit <= 0 {
		req.Limit = 1
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	t.Logger.InfoWithContext(ctx, "Resolved indicator", map[string]interface{}{
		"operation":  "economic_indicator",
		"request_id": requestID,
		"input":      req.Indicator,
		"series_id":  seriesID,
	})

	// Check if API key is available
	if t.apiKey == "" {
		t.Logger.ErrorWithContext(ctx, "FRED API key not configured", map[string]interface{}{
			"operation":  "economic_indicator",
			"request_id": requestID,
			"series_id":  seriesID,
		})
		t.sendError(w, "FRED API key not configured", http.StatusForbidden, "AUTH_ERROR")
		return
	}

	apiStart := time.Now()
	apiResp, err := t.client.GetObservations(ctx, seriesID, req.Limit, req.StartDate, req.EndDate)
	apiDuration := time.Since(apiStart)

	telemetry.Histogram("economic.api.duration_ms", float64(apiDuration.Milliseconds()), "capability", "economic_indicator", "api", "fred_observations")

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "economic_indicator", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "FRED API failed", map[string]interface{}{
			"operation":   "economic_indicator",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "api_error",
			"series_id":   seriesID,
			"api_latency": apiDuration.String(),
		})
		t.sendUpstreamError(w, "FRED API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if apiResp == nil {
		telemetry.Counter("economic.errors.total", "capability", "economic_indicator", "error_type", "no_data")
		t.Logger.ErrorWithContext(ctx, "FRED API returned no data", map[string]interface{}{
			"operation":   "economic_indicator",
			"request_id":  requestID,
			"series_id":   seriesID,
			"api_latency": apiDuration.String(),
		})
		t.sendError(w, "No data returned for "+seriesID, http.StatusBadGateway, "API_ERROR")
		return
	}

	// Build response from FRED API data
	observations := make([]Observation, 0, len(apiResp.Observations))
	for _, obs := range apiResp.Observations {
		if obs.Value != "." { // FRED uses "." for missing values
			observations = append(observations, Observation{Date: obs.Date, Value: obs.Value})
		}
	}

	meta, hasMeta := seriesMetadata[seriesID]
	title := seriesID
	units := apiResp.Units
	freq := ""
	if hasMeta {
		title = meta.Title
		if units == "" || units == "lin" {
			units = meta.Units
		}
		freq = meta.Frequency
	}

	result := &EconomicIndicatorResponse{
		Indicator:    seriesID,
		Title:        title,
		Frequency:    freq,
		Units:        units,
		LastUpdated:  "",
		Observations: observations,
		Source:       "FRED API",
	}

	t.sendIndicatorSuccess(w, ctx, "economic_indicator", requestID, startTime, seriesID, result)
}

// handleCompareIndicators compares multiple economic indicators
func (t *EconomicDataTool) handleCompareIndicators(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "compare_indicators"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "compare_indicators"),
	)

	t.Logger.InfoWithContext(ctx, "Compare indicators request received", map[string]interface{}{
		"operation":  "compare_indicators",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req CompareIndicatorsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "compare_indicators", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "compare_indicators",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.Indicators) == "" {
		telemetry.Counter("economic.errors.total", "capability", "compare_indicators", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Empty indicators provided", map[string]interface{}{
			"operation":  "compare_indicators",
			"request_id": requestID,
			"error":      "indicators is required",
			"error_type": "validation_error",
		})
		t.sendError(w, "indicators is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	parts := strings.Split(req.Indicators, ",")
	if req.Limit <= 0 {
		req.Limit = 12
	}

	t.Logger.InfoWithContext(ctx, "Comparing indicators", map[string]interface{}{
		"operation":       "compare_indicators",
		"request_id":      requestID,
		"indicator_count": len(parts),
	})

	indicators := make([]IndicatorSeries, 0, len(parts))
	for _, part := range parts {
		seriesID := resolveSeriesID(part)

		if t.apiKey == "" {
			t.Logger.ErrorWithContext(ctx, "FRED API key not configured", map[string]interface{}{
				"operation":  "compare_indicators",
				"request_id": requestID,
				"series_id":  seriesID,
			})
			t.sendError(w, "FRED API key not configured", http.StatusForbidden, "AUTH_ERROR")
			return
		}

		apiResp, err := t.client.GetObservations(ctx, seriesID, req.Limit, req.StartDate, req.EndDate)
		if err != nil {
			telemetry.RecordSpanError(ctx, err)
			t.Logger.ErrorWithContext(ctx, "Failed to fetch indicator", map[string]interface{}{
				"operation":  "compare_indicators",
				"request_id": requestID,
				"series_id":  seriesID,
				"error":      err.Error(),
			})
			t.sendUpstreamError(w, "FRED API failed for "+seriesID+": "+err.Error(), core.ClassifyUpstreamError(err))
			return
		}
		if apiResp == nil {
			t.Logger.ErrorWithContext(ctx, "No data returned for indicator", map[string]interface{}{
				"operation":  "compare_indicators",
				"request_id": requestID,
				"series_id":  seriesID,
			})
			continue
		}

		observations := make([]Observation, 0, len(apiResp.Observations))
		for _, obs := range apiResp.Observations {
			if obs.Value != "." {
				observations = append(observations, Observation{Date: obs.Date, Value: obs.Value})
			}
		}

		meta, hasMeta := seriesMetadata[seriesID]
		title := seriesID
		units := ""
		freq := ""
		if hasMeta {
			title = meta.Title
			units = meta.Units
			freq = meta.Frequency
		}

		indicators = append(indicators, IndicatorSeries{
			SeriesID:     seriesID,
			Title:        title,
			Units:        units,
			Frequency:    freq,
			Observations: observations,
		})
	}

	source := "FRED API"

	result := &CompareIndicatorsResponse{
		Indicators: indicators,
		Period:     DateRange{Start: req.StartDate, End: req.EndDate},
		Source:     source,
	}

	duration := time.Since(startTime)

	telemetry.Histogram("economic.request.duration_ms", float64(duration.Milliseconds()), "capability", "compare_indicators")
	telemetry.Counter("economic.requests.total", "capability", "compare_indicators", "status", "success")
	telemetry.RecordToolCall(serviceName, "compare_indicators", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "indicators_compared",
		attribute.String("request_id", requestID),
		attribute.Int("indicator_count", len(indicators)),
		attribute.String("source", source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	t.Logger.InfoWithContext(ctx, "Compare indicators request completed", map[string]interface{}{
		"operation":       "compare_indicators",
		"request_id":      requestID,
		"indicator_count": len(indicators),
		"source":          source,
		"duration_ms":     duration.Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: result})
}

// handleSearchIndicators searches FRED for economic data series
func (t *EconomicDataTool) handleSearchIndicators(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "search_indicators"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_indicators"),
	)

	t.Logger.InfoWithContext(ctx, "Search indicators request received", map[string]interface{}{
		"operation":  "search_indicators",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req SearchIndicatorsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "search_indicators", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "search_indicators",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.Query) == "" {
		telemetry.Counter("economic.errors.total", "capability", "search_indicators", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Empty query provided", map[string]interface{}{
			"operation":  "search_indicators",
			"request_id": requestID,
			"error":      "query is required",
			"error_type": "validation_error",
		})
		t.sendError(w, "query is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	if req.Limit <= 0 {
		req.Limit = 5
	}
	if req.Limit > 20 {
		req.Limit = 20
	}

	if t.apiKey == "" {
		t.Logger.ErrorWithContext(ctx, "FRED API key not configured", map[string]interface{}{
			"operation":  "search_indicators",
			"request_id": requestID,
		})
		t.sendError(w, "FRED API key not configured", http.StatusForbidden, "AUTH_ERROR")
		return
	}

	apiStart := time.Now()
	apiResp, err := t.client.SearchSeries(ctx, req.Query, req.Limit)
	apiDuration := time.Since(apiStart)

	telemetry.Histogram("economic.api.duration_ms", float64(apiDuration.Milliseconds()), "capability", "search_indicators", "api", "fred_search")

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "search_indicators", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "FRED search failed", map[string]interface{}{
			"operation":   "search_indicators",
			"request_id":  requestID,
			"error":       err.Error(),
			"api_latency": apiDuration.String(),
		})
		t.sendUpstreamError(w, "FRED search failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if apiResp == nil {
		telemetry.Counter("economic.errors.total", "capability", "search_indicators", "error_type", "no_data")
		t.Logger.ErrorWithContext(ctx, "FRED search returned no data", map[string]interface{}{
			"operation":   "search_indicators",
			"request_id":  requestID,
			"api_latency": apiDuration.String(),
		})
		t.sendError(w, "No search results returned from FRED", http.StatusBadGateway, "API_ERROR")
		return
	}

	results := make([]SeriesResult, 0, len(apiResp.Seriess))
	for _, s := range apiResp.Seriess {
		notes := s.Notes
		if len(notes) > 200 {
			notes = notes[:200] + "..."
		}
		results = append(results, SeriesResult{
			SeriesID:           s.ID,
			Title:              s.Title,
			Frequency:          s.Frequency,
			Units:              s.Units,
			SeasonalAdjustment: s.SeasonalAdjustment,
			LastUpdated:        s.LastUpdated,
			Notes:              notes,
		})
	}

	result := &SearchIndicatorsResponse{
		Query:   req.Query,
		Count:   len(results),
		Results: results,
		Source:  "FRED API",
	}

	t.sendSearchSuccess(w, ctx, requestID, startTime, result)
}

// handleIndicatorInfo returns metadata about a specific series
func (t *EconomicDataTool) handleIndicatorInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "indicator_info"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "indicator_info"),
	)

	t.Logger.InfoWithContext(ctx, "Indicator info request received", map[string]interface{}{
		"operation":  "indicator_info",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req IndicatorInfoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "indicator_info", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "indicator_info",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.Indicator) == "" {
		telemetry.Counter("economic.errors.total", "capability", "indicator_info", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Empty indicator provided", map[string]interface{}{
			"operation":  "indicator_info",
			"request_id": requestID,
			"error":      "indicator is required",
			"error_type": "validation_error",
		})
		t.sendError(w, "indicator is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	seriesID := resolveSeriesID(req.Indicator)

	if t.apiKey == "" {
		t.Logger.ErrorWithContext(ctx, "FRED API key not configured", map[string]interface{}{
			"operation":  "indicator_info",
			"request_id": requestID,
			"series_id":  seriesID,
		})
		t.sendError(w, "FRED API key not configured", http.StatusForbidden, "AUTH_ERROR")
		return
	}

	apiStart := time.Now()
	apiResp, err := t.client.GetSeriesInfo(ctx, seriesID)
	apiDuration := time.Since(apiStart)

	telemetry.Histogram("economic.api.duration_ms", float64(apiDuration.Milliseconds()), "capability", "indicator_info", "api", "fred_series_info")

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "indicator_info", "error_type", "api_error")
		t.sendUpstreamError(w, fmt.Sprintf("FRED API failed for %s: %s", seriesID, err.Error()), core.ClassifyUpstreamError(err))
		return
	}
	if apiResp == nil || len(apiResp.Seriess) == 0 {
		telemetry.Counter("economic.errors.total", "capability", "indicator_info", "error_type", "no_data")
		t.sendError(w, fmt.Sprintf("No info returned for %s", seriesID), http.StatusBadGateway, "API_ERROR")
		return
	}

	s := apiResp.Seriess[0]
	result := &IndicatorInfoResponse{
		SeriesID:           s.ID,
		Title:              s.Title,
		ObservationStart:   s.ObservationStart,
		ObservationEnd:     s.ObservationEnd,
		Frequency:          s.Frequency,
		Units:              s.Units,
		SeasonalAdjustment: s.SeasonalAdjustment,
		LastUpdated:        s.LastUpdated,
		Notes:              s.Notes,
		Source:             "FRED API",
	}

	t.sendInfoSuccess(w, ctx, requestID, startTime, seriesID, result)
}

// Helper methods

func (t *EconomicDataTool) sendIndicatorSuccess(w http.ResponseWriter, ctx context.Context, operation, requestID string, startTime time.Time, seriesID string, data interface{}) {
	duration := time.Since(startTime)

	telemetry.Histogram("economic.request.duration_ms", float64(duration.Milliseconds()), "capability", operation)
	telemetry.Counter("economic.requests.total", "capability", operation, "status", "success")
	telemetry.RecordToolCall(serviceName, operation, float64(duration.Milliseconds()), "success")

	// Extract business attributes from response
	var observationCount int
	var source, title string
	if resp, ok := data.(*EconomicIndicatorResponse); ok {
		observationCount = len(resp.Observations)
		source = resp.Source
		title = resp.Title
	}

	telemetry.AddSpanEvent(ctx, "economic_indicator_retrieved",
		attribute.String("request_id", requestID),
		attribute.String("series_id", seriesID),
		attribute.String("title", title),
		attribute.Int("observation_count", observationCount),
		attribute.String("source", source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	t.Logger.InfoWithContext(ctx, "Economic indicator request completed", map[string]interface{}{
		"operation":         "economic_indicator",
		"request_id":        requestID,
		"series_id":         seriesID,
		"title":             title,
		"observation_count": observationCount,
		"source":            source,
		"duration_ms":       duration.Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: data})
}

func (t *EconomicDataTool) sendSearchSuccess(w http.ResponseWriter, ctx context.Context, requestID string, startTime time.Time, data *SearchIndicatorsResponse) {
	duration := time.Since(startTime)

	telemetry.Histogram("economic.request.duration_ms", float64(duration.Milliseconds()), "capability", "search_indicators")
	telemetry.Counter("economic.requests.total", "capability", "search_indicators", "status", "success")
	telemetry.RecordToolCall(serviceName, "search_indicators", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "indicators_searched",
		attribute.String("request_id", requestID),
		attribute.String("query", data.Query),
		attribute.Int("result_count", data.Count),
		attribute.String("source", data.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	t.Logger.InfoWithContext(ctx, "Search indicators request completed", map[string]interface{}{
		"operation":    "search_indicators",
		"request_id":   requestID,
		"query":        data.Query,
		"result_count": data.Count,
		"source":       data.Source,
		"duration_ms":  duration.Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: data})
}

func (t *EconomicDataTool) sendInfoSuccess(w http.ResponseWriter, ctx context.Context, requestID string, startTime time.Time, seriesID string, data *IndicatorInfoResponse) {
	duration := time.Since(startTime)

	telemetry.Histogram("economic.request.duration_ms", float64(duration.Milliseconds()), "capability", "indicator_info")
	telemetry.Counter("economic.requests.total", "capability", "indicator_info", "status", "success")
	telemetry.RecordToolCall(serviceName, "indicator_info", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "indicator_info_retrieved",
		attribute.String("request_id", requestID),
		attribute.String("series_id", data.SeriesID),
		attribute.String("title", data.Title),
		attribute.String("source", data.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	t.Logger.InfoWithContext(ctx, "Indicator info request completed", map[string]interface{}{
		"operation":   "indicator_info",
		"request_id":  requestID,
		"series_id":   data.SeriesID,
		"title":       data.Title,
		"source":      data.Source,
		"duration_ms": duration.Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: data})
}

// handleGlobalEconomicIndicator returns economic data for any country via World Bank
func (t *EconomicDataTool) handleGlobalEconomicIndicator(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "global_economic_indicator"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "global_economic_indicator"),
	)

	t.Logger.InfoWithContext(ctx, "Global economic indicator request received", map[string]interface{}{
		"operation":  "global_economic_indicator",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req GlobalEconomicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "global_economic_indicator", "error_type", "decode_error")
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.Country) == "" {
		telemetry.Counter("economic.errors.total", "capability", "global_economic_indicator", "error_type", "validation_error")
		t.sendError(w, "country field is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	countryCode := resolveCountryCode(req.Country)

	result, err := t.fetchGlobalEconomics(ctx, countryCode, req.Year)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "global_economic_indicator", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "World Bank API failed", map[string]interface{}{
			"operation":    "global_economic_indicator",
			"request_id":   requestID,
			"country_code": countryCode,
			"error":        err.Error(),
		})
		t.sendUpstreamError(w, "World Bank API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.sendGlobalSuccess(w, ctx, "global_economic_indicator", requestID, startTime, result)
}

// handleCompareCountryEconomies compares economic indicators across multiple countries
func (t *EconomicDataTool) handleCompareCountryEconomies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "compare_country_economies"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "compare_country_economies"),
	)

	t.Logger.InfoWithContext(ctx, "Compare country economies request received", map[string]interface{}{
		"operation":  "compare_country_economies",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req CompareCountryEconomiesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("economic.errors.total", "capability", "compare_country_economies", "error_type", "decode_error")
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.Countries) == "" {
		telemetry.Counter("economic.errors.total", "capability", "compare_country_economies", "error_type", "validation_error")
		t.sendError(w, "countries field is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	parts := strings.Split(req.Countries, ",")
	if len(parts) < 2 {
		telemetry.Counter("economic.errors.total", "capability", "compare_country_economies", "error_type", "validation_error")
		t.sendError(w, "at least 2 countries required for comparison", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}
	if len(parts) > 10 {
		telemetry.Counter("economic.errors.total", "capability", "compare_country_economies", "error_type", "validation_error")
		t.sendError(w, "maximum 10 countries allowed", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	var countries []GlobalEconomicResponse
	for _, part := range parts {
		code := resolveCountryCode(strings.TrimSpace(part))
		result, err := t.fetchGlobalEconomics(ctx, code, req.Year)
		if err != nil {
			t.Logger.WarnWithContext(ctx, "Failed to fetch country economics, skipping", map[string]interface{}{
				"operation":    "compare_country_economies",
				"request_id":   requestID,
				"country_code": code,
				"error":        err.Error(),
			})
			continue
		}
		countries = append(countries, *result)
	}

	if len(countries) == 0 {
		telemetry.Counter("economic.errors.total", "capability", "compare_country_economies", "error_type", "api_error")
		t.sendError(w, "could not retrieve data for any of the specified countries", http.StatusInternalServerError, ErrCodeServiceUnavailable)
		return
	}

	response := &CompareCountryEconomiesResponse{
		Countries: countries,
		DataYear:  countries[0].DataYear,
		Source:    "World Bank Open Data",
	}

	t.sendGlobalSuccess(w, ctx, "compare_country_economies", requestID, startTime, response)
}

// fetchGlobalEconomics fetches all economic indicators for a single country
func (t *EconomicDataTool) fetchGlobalEconomics(ctx context.Context, countryCode, year string) (*GlobalEconomicResponse, error) {
	countryInfo, err := t.wbClient.GetCountryInfo(ctx, countryCode)
	if err != nil {
		return nil, fmt.Errorf("failed to get country info: %w", err)
	}

	result := &GlobalEconomicResponse{
		Country:     countryInfo.Name,
		CountryCode: countryInfo.ID,
		Region:      countryInfo.Region.Value,
		IncomeLevel: countryInfo.IncomeLevel.Value,
		Source:      "World Bank Open Data",
	}

	// Fetch each indicator: GDP, GDP per capita, Inflation (CPI), Unemployment
	indicators := []string{"NY.GDP.MKTP.CD", "NY.GDP.PCAP.CD", "FP.CPI.TOTL.ZG", "SL.UEM.TOTL.ZS"}
	for _, ind := range indicators {
		dataPoints, err := t.wbClient.GetIndicator(ctx, countryCode, ind, 5, year)
		if err != nil {
			continue
		}

		val, dataYear := latestNonNilValue(dataPoints)
		if val == nil {
			continue
		}

		if result.DataYear == "" || dataYear > result.DataYear {
			result.DataYear = dataYear
		}

		switch ind {
		case "NY.GDP.MKTP.CD":
			result.GDP = val
		case "NY.GDP.PCAP.CD":
			result.GDPPerCapita = val
		case "FP.CPI.TOTL.ZG":
			result.InflationRate = val
		case "SL.UEM.TOTL.ZS":
			result.UnemploymentRate = val
		}
	}

	return result, nil
}

// sendGlobalSuccess sends a successful response for World Bank global endpoints
func (t *EconomicDataTool) sendGlobalSuccess(w http.ResponseWriter, ctx context.Context, operation, requestID string, startTime time.Time, data interface{}) {
	duration := time.Since(startTime)

	telemetry.Histogram("economic.request.duration_ms", float64(duration.Milliseconds()), "capability", operation)
	telemetry.Counter("economic.requests.total", "capability", operation, "status", "success")
	telemetry.RecordToolCall(serviceName, operation, float64(duration.Milliseconds()), "success")

	switch d := data.(type) {
	case *GlobalEconomicResponse:
		telemetry.AddSpanEvent(ctx, "global_economic_retrieved",
			attribute.String("request_id", requestID),
			attribute.String("country", d.Country),
			attribute.String("country_code", d.CountryCode),
			attribute.String("source", d.Source),
			attribute.Int64("duration_ms", duration.Milliseconds()),
		)
		t.Logger.InfoWithContext(ctx, "Global economic indicator request completed", map[string]interface{}{
			"operation":    operation,
			"request_id":   requestID,
			"country":      d.Country,
			"country_code": d.CountryCode,
			"source":       d.Source,
			"duration_ms":  duration.Milliseconds(),
		})
	case *CompareCountryEconomiesResponse:
		telemetry.AddSpanEvent(ctx, "country_economies_compared",
			attribute.String("request_id", requestID),
			attribute.Int("country_count", len(d.Countries)),
			attribute.String("source", d.Source),
			attribute.Int64("duration_ms", duration.Milliseconds()),
		)
		t.Logger.InfoWithContext(ctx, "Compare country economies request completed", map[string]interface{}{
			"operation":     operation,
			"request_id":    requestID,
			"country_count": len(d.Countries),
			"source":        d.Source,
			"duration_ms":   duration.Milliseconds(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: data})
}

func (t *EconomicDataTool) sendError(w http.ResponseWriter, message string, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "UNAVAILABLE"),
		},
	})
}
