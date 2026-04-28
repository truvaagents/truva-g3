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

// sanitizeNull converts AI-generated literal "null" strings to empty strings.
// The orchestrator's LLM planner sometimes sends "null" instead of omitting
// optional fields. Sanitizing before validation lets required-field checks
// reject these as missing rather than pass them through to the upstream API.
func sanitizeNull(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "null") {
		return ""
	}
	return s
}

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (h *HotelTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

// sendError sends a structured error response with the correct HTTP status code.
// CRITICAL: WriteHeader MUST be called before json.Encode — otherwise Go defaults
// to HTTP 200 and the orchestrator treats the step as successful.
func (h *HotelTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
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

// handleSearchHotels processes hotel search requests with full telemetry
func (h *HotelTool) handleSearchHotels(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "hotel-tool"),
		attribute.String("truvag3.capability", "search_hotels"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_hotels"),
	)

	var req SearchHotelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("hotel.errors.total",
			"capability", "search_hotels",
			"error_type", "decode_error",
		)
		h.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_hotels",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
			"status":     "failure",
		})
		h.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.CityName = strings.TrimSpace(sanitizeNull(req.CityName))
	req.CountryCode = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.CountryCode)))
	req.CheckIn = strings.TrimSpace(sanitizeNull(req.CheckIn))
	req.CheckOut = strings.TrimSpace(sanitizeNull(req.CheckOut))
	req.Currency = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.Currency)))
	req.GuestNationality = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.GuestNationality)))

	if req.CityName == "" || req.CountryCode == "" || req.CheckIn == "" || req.CheckOut == "" {
		telemetry.Counter("hotel.errors.total",
			"capability", "search_hotels",
			"error_type", "validation_error",
		)
		h.Logger.WarnWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "search_hotels",
			"request_id": upstreamRequestID,
			"error_type": "validation_error",
			"status":     "failure",
		})
		h.sendError(rw, "city_name, country_code, check_in, and check_out are required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("hotel.city_name", req.CityName),
		attribute.String("hotel.country_code", req.CountryCode),
		attribute.String("hotel.check_in", req.CheckIn),
		attribute.String("hotel.check_out", req.CheckOut),
	)

	h.Logger.InfoWithContext(ctx, "Request received", map[string]interface{}{
		"operation":    "search_hotels",
		"method":       r.Method,
		"path":         r.URL.Path,
		"city_name":    req.CityName,
		"country_code": req.CountryCode,
		"check_in":     req.CheckIn,
		"check_out":    req.CheckOut,
		"adults":       req.Adults,
		"request_id":   upstreamRequestID,
	})

	telemetry.AddSpanEvent(ctx, "calling_liteapi",
		attribute.String("city_name", req.CityName),
		attribute.String("country_code", req.CountryCode),
		attribute.String("api", "hotels_rates"),
	)

	apiStartTime := time.Now()
	response, err := h.client.SearchHotels(ctx, req)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("hotel.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_hotels",
		"api", "liteapi",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("hotel.api.errors.total",
			"capability", "search_hotels",
			"error_type", "api_error",
		)
		h.Logger.WarnWithContext(ctx, "LiteAPI call failed", map[string]interface{}{
			"operation":    "search_hotels",
			"error":        err.Error(),
			"error_type":   "api_error",
			"city_name":    req.CityName,
			"country_code": req.CountryCode,
			"duration_ms":  apiDuration.Milliseconds(),
			"request_id":   upstreamRequestID,
			"status":       "failure",
		})
		h.sendUpstreamError(rw, "Hotel search failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	duration := time.Since(startTime)
	telemetry.Histogram("hotel.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_hotels",
	)
	telemetry.Counter("hotel.requests.total",
		"capability", "search_hotels",
		"status", "success",
	)
	telemetry.RecordToolCall("hotel-tool", "search_hotels", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "search_hotels_retrieved",
		attribute.String("city_name", req.CityName),
		attribute.Int("hotels_count", len(response.Hotels)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	h.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
		"operation":    "search_hotels",
		"city_name":    req.CityName,
		"country_code": req.CountryCode,
		"hotels_count": len(response.Hotels),
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
		"status":       "success",
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleListHotelsByCity processes hotel listing requests with full telemetry
func (h *HotelTool) handleListHotelsByCity(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "hotel-tool"),
		attribute.String("truvag3.capability", "list_hotels_by_city"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "list_hotels_by_city"),
	)

	var req ListHotelsByCityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("hotel.errors.total",
			"capability", "list_hotels_by_city",
			"error_type", "decode_error",
		)
		h.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "list_hotels_by_city",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
			"status":     "failure",
		})
		h.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.CityName = strings.TrimSpace(sanitizeNull(req.CityName))
	req.CountryCode = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.CountryCode)))

	if req.CityName == "" || req.CountryCode == "" {
		telemetry.Counter("hotel.errors.total",
			"capability", "list_hotels_by_city",
			"error_type", "validation_error",
		)
		h.Logger.WarnWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "list_hotels_by_city",
			"request_id": upstreamRequestID,
			"error_type": "validation_error",
			"status":     "failure",
		})
		h.sendError(rw, "city_name and country_code are required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("hotel.city_name", req.CityName),
		attribute.String("hotel.country_code", req.CountryCode),
	)

	h.Logger.InfoWithContext(ctx, "Request received", map[string]interface{}{
		"operation":    "list_hotels_by_city",
		"method":       r.Method,
		"path":         r.URL.Path,
		"city_name":    req.CityName,
		"country_code": req.CountryCode,
		"limit":        req.Limit,
		"request_id":   upstreamRequestID,
	})

	telemetry.AddSpanEvent(ctx, "calling_liteapi",
		attribute.String("city_name", req.CityName),
		attribute.String("api", "hotels_by_city"),
	)

	apiStartTime := time.Now()
	response, err := h.client.ListHotelsByCity(ctx, req)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("hotel.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "list_hotels_by_city",
		"api", "liteapi",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("hotel.api.errors.total",
			"capability", "list_hotels_by_city",
			"error_type", "api_error",
		)
		h.Logger.WarnWithContext(ctx, "LiteAPI call failed", map[string]interface{}{
			"operation":    "list_hotels_by_city",
			"error":        err.Error(),
			"error_type":   "api_error",
			"city_name":    req.CityName,
			"country_code": req.CountryCode,
			"duration_ms":  apiDuration.Milliseconds(),
			"request_id":   upstreamRequestID,
			"status":       "failure",
		})
		h.sendUpstreamError(rw, "Hotel listing failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	duration := time.Since(startTime)
	telemetry.Histogram("hotel.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "list_hotels_by_city",
	)
	telemetry.Counter("hotel.requests.total",
		"capability", "list_hotels_by_city",
		"status", "success",
	)
	telemetry.RecordToolCall("hotel-tool", "list_hotels_by_city", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "list_hotels_retrieved",
		attribute.String("city_name", req.CityName),
		attribute.Int("hotels_count", len(response.Hotels)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	h.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
		"operation":    "list_hotels_by_city",
		"city_name":    req.CityName,
		"country_code": req.CountryCode,
		"hotels_count": len(response.Hotels),
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
		"status":       "success",
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleHotelRatings processes hotel ratings requests with full telemetry
func (h *HotelTool) handleHotelRatings(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "hotel-tool"),
		attribute.String("truvag3.capability", "hotel_ratings"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "hotel_ratings"),
	)

	var req HotelRatingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("hotel.errors.total",
			"capability", "hotel_ratings",
			"error_type", "decode_error",
		)
		h.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "hotel_ratings",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
			"status":     "failure",
		})
		h.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.HotelID = strings.TrimSpace(sanitizeNull(req.HotelID))

	if req.HotelID == "" {
		telemetry.Counter("hotel.errors.total",
			"capability", "hotel_ratings",
			"error_type", "validation_error",
		)
		h.Logger.WarnWithContext(ctx, "Missing required hotel_id field", map[string]interface{}{
			"operation":  "hotel_ratings",
			"request_id": upstreamRequestID,
			"error_type": "validation_error",
			"status":     "failure",
		})
		h.sendError(rw, "hotel_id is required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("hotel.hotel_id", req.HotelID),
	)

	h.Logger.InfoWithContext(ctx, "Request received", map[string]interface{}{
		"operation":  "hotel_ratings",
		"method":     r.Method,
		"path":       r.URL.Path,
		"hotel_id":   req.HotelID,
		"request_id": upstreamRequestID,
	})

	telemetry.AddSpanEvent(ctx, "calling_liteapi",
		attribute.String("hotel_id", req.HotelID),
		attribute.String("api", "reviews"),
	)

	apiStartTime := time.Now()
	response, err := h.client.HotelRatings(ctx, req.HotelID)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("hotel.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "hotel_ratings",
		"api", "liteapi",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("hotel.api.errors.total",
			"capability", "hotel_ratings",
			"error_type", "api_error",
		)
		h.Logger.WarnWithContext(ctx, "LiteAPI call failed", map[string]interface{}{
			"operation":   "hotel_ratings",
			"error":       err.Error(),
			"error_type":  "api_error",
			"hotel_id":    req.HotelID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
			"status":      "failure",
		})
		h.sendUpstreamError(rw, "Hotel ratings lookup failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	duration := time.Since(startTime)
	telemetry.Histogram("hotel.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "hotel_ratings",
	)
	telemetry.Counter("hotel.requests.total",
		"capability", "hotel_ratings",
		"status", "success",
	)
	telemetry.RecordToolCall("hotel-tool", "hotel_ratings", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "hotel_ratings_retrieved",
		attribute.String("hotel_id", req.HotelID),
		attribute.Int("hotels_count", len(response.Hotels)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	h.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
		"operation":    "hotel_ratings",
		"hotel_id":     req.HotelID,
		"hotels_count": len(response.Hotels),
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
		"status":       "success",
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
