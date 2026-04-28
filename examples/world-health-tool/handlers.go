package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (t *WorldHealthTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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
// CRITICAL: WriteHeader must be called before Encode
func (t *WorldHealthTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
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

// handleGetHealthIndicator processes health indicator requests with full telemetry
func (t *WorldHealthTool) handleGetHealthIndicator(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "world-health-tool"),
		attribute.String("truvag3.capability", "get_health_indicator"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_health_indicator"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing health indicator request", map[string]interface{}{
		"operation":  "get_health_indicator",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req GetHealthIndicatorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("health.errors.total",
			"capability", "get_health_indicator",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "get_health_indicator",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.Country = strings.ToUpper(strings.TrimSpace(req.Country))
	req.Indicator = strings.TrimSpace(req.Indicator)
	req.Sex = strings.ToUpper(strings.TrimSpace(req.Sex))

	if req.Indicator == "" || req.Country == "" {
		telemetry.Counter("health.errors.total",
			"capability", "get_health_indicator",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "get_health_indicator",
			"request_id": upstreamRequestID,
			"indicator":  req.Indicator,
			"country":    req.Country,
			"error":      "indicator and country are required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: indicator and country are required"))
		t.sendError(rw, "Both 'indicator' and 'country' are required", http.StatusBadRequest, "INVALID_INPUT")
		return
	}

	// Add business context to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("health.indicator", req.Indicator),
		attribute.String("health.country", req.Country),
		attribute.String("health.sex", req.Sex),
	)

	t.Logger.InfoWithContext(ctx, "Received health indicator request", map[string]interface{}{
		"operation":  "get_health_indicator",
		"indicator":  req.Indicator,
		"country":    req.Country,
		"sex":        req.Sex,
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_health_api",
		attribute.String("indicator", req.Indicator),
		attribute.String("country", req.Country),
		attribute.String("strategy", "who_primary_worldbank_fallback"),
	)

	// Step 10: Call API with timing (dual-source with automatic fallback)
	apiStartTime := time.Now()
	result, err := t.client.GetHealthIndicatorWithFallback(ctx, req.Indicator, req.Country, req.Year, req.Sex)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("health.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_health_indicator",
		"api", "health_dual_source",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("health.api.errors.total",
			"capability", "get_health_indicator",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Health API call failed", map[string]interface{}{
			"operation":   "get_health_indicator",
			"error":       err.Error(),
			"indicator":   req.Indicator,
			"country":     req.Country,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "Health API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Build response — resolve raw WHO codes to friendly names for display
	friendlyName := req.Indicator
	if _, ok := whoIndicatorMap[req.Indicator]; !ok {
		// Not a friendly name — check reverse lookup from WHO code
		if resolved, ok := reverseWHOCodeMap[strings.ToLower(req.Indicator)]; ok {
			friendlyName = resolved
		}
	}

	response := HealthIndicatorResponse{
		Indicator:    result.IndicatorCode,
		FriendlyName: friendlyName,
		Country:      req.Country,
		CountryName:  resolveCountryName(req.Country),
		Year:         result.Year,
		Value:        result.Value,
		Unit:         resolveIndicatorUnit(friendlyName),
		Sex:          result.Sex,
		Source:       result.Source,
	}

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("health.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_health_indicator",
	)
	telemetry.Counter("health.requests.total",
		"capability", "get_health_indicator",
		"status", "success",
		"module", "tool",
	)

	// Step 14: Record unified metrics via RecordToolCall
	telemetry.RecordToolCall("world-health-tool", "get_health_indicator", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "health_indicator_retrieved",
		attribute.String("indicator", req.Indicator),
		attribute.String("country", req.Country),
		attribute.String("source", result.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	t.Logger.InfoWithContext(ctx, "Health indicator request completed", map[string]interface{}{
		"operation":   "get_health_indicator",
		"status":      "success",
		"indicator":   req.Indicator,
		"country":     req.Country,
		"source":      result.Source,
		"year":        result.Year,
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleListIndicators processes requests to list available health indicators
func (t *WorldHealthTool) handleListIndicators(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "world-health-tool"),
		attribute.String("truvag3.capability", "list_indicators"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "list_indicators"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing list indicators request", map[string]interface{}{
		"operation":  "list_indicators",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req ListIndicatorsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("health.errors.total",
			"capability", "list_indicators",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "list_indicators",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate / set defaults (no required fields)
	limit := 20
	if req.Limit != nil && *req.Limit > 0 {
		limit = *req.Limit
	}

	// Add business context to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("health.search_query", req.Search),
	)

	t.Logger.InfoWithContext(ctx, "Listing indicators", map[string]interface{}{
		"operation":  "list_indicators",
		"search":     req.Search,
		"limit":      limit,
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_who_indicator_api",
		attribute.String("search", req.Search),
		attribute.Int("limit", limit),
	)

	// Step 10: Call API with timing (WHO only, no World Bank fallback for listing)
	apiStartTime := time.Now()
	whoIndicators, err := t.client.ListIndicators(ctx, req.Search, limit)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("health.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "list_indicators",
		"api", "who_indicators",
	)

	// Step 12: Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("health.api.errors.total",
			"capability", "list_indicators",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "WHO indicator listing failed", map[string]interface{}{
			"operation":   "list_indicators",
			"error":       err.Error(),
			"search":      req.Search,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "WHO API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Build response
	indicators := make([]IndicatorInfo, 0, len(whoIndicators))
	for _, ind := range whoIndicators {
		if ind.Language == "EN" || ind.Language == "" {
			indicators = append(indicators, IndicatorInfo{
				Code:        ind.IndicatorCode,
				Name:        ind.IndicatorName,
				Description: ind.IndicatorName,
			})
		}
	}

	response := ListIndicatorsResponse{
		Indicators: indicators,
		Count:      len(indicators),
		Source:     "WHO GHO",
	}

	// Add indicator count to span
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("health.indicator_count", len(indicators)),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("health.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "list_indicators",
	)
	telemetry.Counter("health.requests.total",
		"capability", "list_indicators",
		"status", "success",
		"module", "tool",
	)

	// Step 14: Record unified metrics via RecordToolCall
	telemetry.RecordToolCall("world-health-tool", "list_indicators", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "indicators_listed",
		attribute.String("search", req.Search),
		attribute.Int("indicator_count", len(indicators)),
		attribute.String("source", "WHO GHO"),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	t.Logger.InfoWithContext(ctx, "List indicators request completed", map[string]interface{}{
		"operation":       "list_indicators",
		"status":          "success",
		"search":          req.Search,
		"indicator_count": len(indicators),
		"source":          "WHO GHO",
		"duration_ms":     duration.Milliseconds(),
		"request_id":      upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleCompareCountries processes requests to compare a health indicator across multiple countries
func (t *WorldHealthTool) handleCompareCountries(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "world-health-tool"),
		attribute.String("truvag3.capability", "compare_countries"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "compare_countries"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing compare countries request", map[string]interface{}{
		"operation":  "compare_countries",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req CompareCountriesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("health.errors.total",
			"capability", "compare_countries",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "compare_countries",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.Indicator = strings.ToLower(strings.TrimSpace(req.Indicator))
	req.Countries = strings.TrimSpace(req.Countries)

	if req.Indicator == "" || req.Countries == "" {
		telemetry.Counter("health.errors.total",
			"capability", "compare_countries",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "compare_countries",
			"request_id": upstreamRequestID,
			"indicator":  req.Indicator,
			"countries":  req.Countries,
			"error":      "indicator and countries are required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: indicator and countries are required"))
		t.sendError(rw, "Both 'indicator' and 'countries' are required", http.StatusBadRequest, "INVALID_INPUT")
		return
	}

	// Split and clean country codes
	parts := strings.Split(req.Countries, ",")
	countryCodes := make([]string, 0, len(parts))
	for _, part := range parts {
		code := strings.ToUpper(strings.TrimSpace(part))
		if code != "" {
			countryCodes = append(countryCodes, code)
		}
	}

	if len(countryCodes) < 2 {
		telemetry.Counter("health.errors.total",
			"capability", "compare_countries",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Not enough countries for comparison", map[string]interface{}{
			"operation":     "compare_countries",
			"request_id":    upstreamRequestID,
			"country_count": len(countryCodes),
			"error":         "at least 2 countries required",
			"error_type":    "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: at least 2 countries required for comparison"))
		t.sendError(rw, "At least 2 countries are required for comparison", http.StatusBadRequest, "INVALID_INPUT")
		return
	}

	// Add business context to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("health.comparison_indicator", req.Indicator),
		attribute.Int("health.country_count", len(countryCodes)),
	)

	t.Logger.InfoWithContext(ctx, "Comparing countries", map[string]interface{}{
		"operation":     "compare_countries",
		"indicator":     req.Indicator,
		"country_count": len(countryCodes),
		"countries":     strings.Join(countryCodes, ","),
		"request_id":    upstreamRequestID,
	})

	// Step 9: Add span event before API calls
	telemetry.AddSpanEvent(ctx, "calling_health_api_multi_country",
		attribute.String("indicator", req.Indicator),
		attribute.Int("country_count", len(countryCodes)),
		attribute.String("strategy", "who_primary_worldbank_fallback"),
	)

	// Step 10: Call API for each country (parallel via goroutines)
	apiStartTime := time.Now()

	type countryResult struct {
		index   int
		country string
		result  *HealthIndicatorResult
		err     error
	}

	resultChan := make(chan countryResult, len(countryCodes))
	var wg sync.WaitGroup

	for i, code := range countryCodes {
		wg.Add(1)
		go func(idx int, countryCode string) {
			defer wg.Done()
			res, err := t.client.GetHealthIndicatorWithFallback(ctx, req.Indicator, countryCode, req.Year, "BTSX")
			resultChan <- countryResult{index: idx, country: countryCode, result: res, err: err}
		}(i, code)
	}

	wg.Wait()
	close(resultChan)

	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("health.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "compare_countries",
		"api", "health_dual_source_multi",
	)

	// Collect results in order
	results := make([]countryResult, 0, len(countryCodes))
	for cr := range resultChan {
		results = append(results, cr)
	}

	// Build comparisons
	comparisons := make([]CountryComparison, len(countryCodes))
	source := "WHO GHO"
	for _, cr := range results {
		if cr.err != nil {
			t.Logger.DebugWithContext(ctx, "Failed to get data for country", map[string]interface{}{
				"operation":  "compare_countries",
				"request_id": upstreamRequestID,
				"country":    cr.country,
				"error":      cr.err.Error(),
			})
			comparisons[cr.index] = CountryComparison{
				Country:     cr.country,
				CountryName: resolveCountryName(cr.country),
				Value:       nil,
				Year:        0,
			}
		} else {
			t.Logger.DebugWithContext(ctx, "Got data for country", map[string]interface{}{
				"operation":  "compare_countries",
				"request_id": upstreamRequestID,
				"country":    cr.country,
				"source":     cr.result.Source,
				"year":       cr.result.Year,
			})
			comparisons[cr.index] = CountryComparison{
				Country:     cr.country,
				CountryName: resolveCountryName(cr.country),
				Value:       cr.result.Value,
				Year:        cr.result.Year,
			}
			if cr.result.Source == "World Bank" {
				source = "World Bank"
			}
		}
	}

	friendlyName := req.Indicator
	if _, ok := whoIndicatorMap[req.Indicator]; !ok {
		if resolved, ok := reverseWHOCodeMap[strings.ToLower(req.Indicator)]; ok {
			friendlyName = resolved
		}
	}

	response := CompareCountriesResponse{
		Indicator:    req.Indicator,
		FriendlyName: friendlyName,
		Unit:         resolveIndicatorUnit(friendlyName),
		Countries:    comparisons,
		Source:       source,
	}

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("health.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "compare_countries",
	)
	telemetry.Counter("health.requests.total",
		"capability", "compare_countries",
		"status", "success",
		"module", "tool",
	)

	// Step 14: Record unified metrics via RecordToolCall
	telemetry.RecordToolCall("world-health-tool", "compare_countries", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "countries_compared",
		attribute.String("indicator", req.Indicator),
		attribute.Int("country_count", len(countryCodes)),
		attribute.String("source", source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	t.Logger.InfoWithContext(ctx, "Compare countries request completed", map[string]interface{}{
		"operation":     "compare_countries",
		"status":        "success",
		"indicator":     req.Indicator,
		"country_count": len(countryCodes),
		"source":        source,
		"duration_ms":   duration.Milliseconds(),
		"request_id":    upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
