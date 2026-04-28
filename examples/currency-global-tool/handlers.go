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
func (t *CurrencyGlobalTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

// handleConvert processes currency conversion requests
func (t *CurrencyGlobalTool) handleConvert(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Extract request ID from headers first, then fall back to baggage
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = r.Header.Get("X-Upstream-Request-ID")
	}
	if requestID == "" {
		baggage := telemetry.GetBaggage(ctx)
		requestID = baggage["request_id"]
	}

	// Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "currency-global-tool"),
		attribute.String("truvag3.capability", "convert_currency"),
	)

	// Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "convert_currency"),
	)

	t.Logger.InfoWithContext(ctx, "Processing currency conversion", map[string]interface{}{
		"operation":  "convert_currency",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": requestID,
	})

	// Decode request
	var req ConvertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("currency_global.errors.total",
			"capability", "convert_currency",
			"error_type", "decode_error",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "convert_currency",
			"error":       err.Error(),
			"error_type":  "decode_error",
			"request_id":  requestID,
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Validate input
	req.From = strings.ToUpper(strings.TrimSpace(req.From))
	req.To = strings.ToUpper(strings.TrimSpace(req.To))

	if req.From == "" || req.To == "" {
		t.Logger.WarnWithContext(ctx, "Missing currency codes", map[string]interface{}{
			"operation":   "convert_currency",
			"error_type":  "validation_error",
			"request_id":  requestID,
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "Both 'from' and 'to' currencies are required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	if req.Amount <= 0 {
		t.Logger.WarnWithContext(ctx, "Invalid amount", map[string]interface{}{
			"operation":   "convert_currency",
			"amount":      req.Amount,
			"error_type":  "validation_error",
			"request_id":  requestID,
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "Amount must be greater than 0", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Add validated parameters to span
	telemetry.SetSpanAttributes(ctx,
		attribute.String("currency.from", req.From),
		attribute.String("currency.to", req.To),
		attribute.Float64("currency.amount", req.Amount),
	)

	t.Logger.InfoWithContext(ctx, "Converting currency", map[string]interface{}{
		"operation":  "convert_currency",
		"from":       req.From,
		"to":         req.To,
		"amount":     req.Amount,
		"request_id": requestID,
	})

	// Call CurrencyBeacon API
	telemetry.AddSpanEvent(ctx, "calling_currencybeacon_api",
		attribute.String("request_id", requestID),
		attribute.String("from", req.From),
		attribute.String("to", req.To),
		attribute.Float64("amount", req.Amount),
	)

	apiStartTime := time.Now()
	apiResp, err := t.client.Convert(ctx, req.From, req.To, req.Amount)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("currency_global.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "convert_currency",
		"api", "currencybeacon",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("currency_global.api.errors.total",
			"capability", "convert_currency",
			"error_type", "api_error",
		)
		t.Logger.ErrorWithContext(ctx, "CurrencyBeacon API call failed", map[string]interface{}{
			"operation":   "convert_currency",
			"error":       err.Error(),
			"error_type":  "api_error",
			"from":        req.From,
			"to":          req.To,
			"request_id":  requestID,
			"api_latency": apiDuration.String(),
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		if strings.Contains(err.Error(), "API key") {
			t.sendError(rw, err.Error(), http.StatusForbidden, "AUTH_ERROR")
		} else {
			t.sendUpstreamError(rw, "Currency API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		}
		return
	}

	// Build response
	rate := apiResp.Response.Value / req.Amount
	result := ConvertResponse{
		From:   req.From,
		To:     req.To,
		Amount: req.Amount,
		Result: apiResp.Response.Value,
		Rate:   rate,
		Date:   apiResp.Response.Date,
	}

	// Record success metrics
	duration := time.Since(startTime)
	telemetry.Counter("currency_global.requests.total",
		"capability", "convert_currency",
		"status", "success",
	)
	telemetry.RecordToolCall("currency-global-tool", "convert_currency", float64(duration.Milliseconds()), "success")

	// Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})

	// Add completion span event
	telemetry.AddSpanEvent(ctx, "currency_converted",
		attribute.String("request_id", requestID),
		attribute.String("from", req.From),
		attribute.String("to", req.To),
		attribute.Float64("amount", req.Amount),
		attribute.Float64("result", result.Result),
		attribute.Float64("rate", rate),
	)

	t.Logger.InfoWithContext(ctx, "Currency conversion completed", map[string]interface{}{
		"operation":   "convert_currency",
		"from":        req.From,
		"to":          req.To,
		"amount":      req.Amount,
		"result":      result.Result,
		"rate":        rate,
		"request_id":  requestID,
		"api_latency": apiDuration.String(),
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
	})
}

// handleRates processes exchange rate requests
func (t *CurrencyGlobalTool) handleRates(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Extract request ID from headers first, then fall back to baggage
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = r.Header.Get("X-Upstream-Request-ID")
	}
	if requestID == "" {
		baggage := telemetry.GetBaggage(ctx)
		requestID = baggage["request_id"]
	}

	// Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "currency-global-tool"),
		attribute.String("truvag3.capability", "get_exchange_rates"),
	)

	// Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_exchange_rates"),
	)

	t.Logger.InfoWithContext(ctx, "Processing exchange rates request", map[string]interface{}{
		"operation":  "get_exchange_rates",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": requestID,
	})

	// Decode request
	var req RatesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("currency_global.errors.total",
			"capability", "get_exchange_rates",
			"error_type", "decode_error",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "get_exchange_rates",
			"error":       err.Error(),
			"error_type":  "decode_error",
			"request_id":  requestID,
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Validate input
	req.Base = strings.ToUpper(strings.TrimSpace(req.Base))
	if req.Base == "" {
		t.Logger.WarnWithContext(ctx, "Missing base currency", map[string]interface{}{
			"operation":   "get_exchange_rates",
			"error_type":  "validation_error",
			"request_id":  requestID,
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "Base currency is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Normalize currency codes
	for i, c := range req.Currencies {
		req.Currencies[i] = strings.ToUpper(strings.TrimSpace(c))
	}

	// Add validated parameters to span
	telemetry.SetSpanAttributes(ctx,
		attribute.String("currency.base", req.Base),
		attribute.Int("currency.targets_count", len(req.Currencies)),
	)

	t.Logger.InfoWithContext(ctx, "Fetching exchange rates", map[string]interface{}{
		"operation":        "get_exchange_rates",
		"base":             req.Base,
		"currencies_count": len(req.Currencies),
		"request_id":       requestID,
	})

	// Call CurrencyBeacon API
	telemetry.AddSpanEvent(ctx, "calling_currencybeacon_api",
		attribute.String("request_id", requestID),
		attribute.String("base", req.Base),
		attribute.Int("currencies_count", len(req.Currencies)),
	)

	apiStartTime := time.Now()
	apiResp, err := t.client.LatestRates(ctx, req.Base, req.Currencies)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("currency_global.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_exchange_rates",
		"api", "currencybeacon",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("currency_global.api.errors.total",
			"capability", "get_exchange_rates",
			"error_type", "api_error",
		)
		t.Logger.ErrorWithContext(ctx, "CurrencyBeacon API call failed", map[string]interface{}{
			"operation":   "get_exchange_rates",
			"error":       err.Error(),
			"error_type":  "api_error",
			"base":        req.Base,
			"request_id":  requestID,
			"api_latency": apiDuration.String(),
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		if strings.Contains(err.Error(), "API key") {
			t.sendError(rw, err.Error(), http.StatusForbidden, "AUTH_ERROR")
		} else {
			t.sendUpstreamError(rw, "Currency API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		}
		return
	}

	// Build response
	result := RatesResponse{
		Base:  req.Base,
		Date:  apiResp.Response.Date,
		Rates: apiResp.Response.Rates,
	}

	// Record success metrics
	duration := time.Since(startTime)
	telemetry.Counter("currency_global.requests.total",
		"capability", "get_exchange_rates",
		"status", "success",
	)
	telemetry.RecordToolCall("currency-global-tool", "get_exchange_rates", float64(duration.Milliseconds()), "success")

	// Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})

	// Add completion span event
	telemetry.AddSpanEvent(ctx, "exchange_rates_retrieved",
		attribute.String("request_id", requestID),
		attribute.String("base", req.Base),
		attribute.Int("rates_count", len(result.Rates)),
	)

	t.Logger.InfoWithContext(ctx, "Exchange rates retrieved", map[string]interface{}{
		"operation":   "get_exchange_rates",
		"base":        req.Base,
		"rates_count": len(result.Rates),
		"request_id":  requestID,
		"api_latency": apiDuration.String(),
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
	})
}

// sendError sends a structured error response using core.ToolResponse
func (t *CurrencyGlobalTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
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
