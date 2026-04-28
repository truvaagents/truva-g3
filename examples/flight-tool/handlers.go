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
// optional fields. By sanitizing before validation, required-field checks
// catch these immediately and reject with HTTP 400 — avoiding wasted API calls.
func sanitizeNull(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "null") {
		return ""
	}
	return s
}

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
// Unlike sendError (which is for tool-local validation errors), this correctly classifies
// upstream API errors for orchestrator error routing.
func (f *FlightTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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
func (f *FlightTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
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

// handleSearchFlights processes flight search requests with full telemetry
func (f *FlightTool) handleSearchFlights(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "flight-tool"),
		attribute.String("truvag3.capability", "search_flights"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_flights"),
	)

	// 5. Decode request
	var req SearchFlightsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("flight.errors.total",
			"capability", "search_flights",
			"error_type", "decode_error",
		)
		f.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_flights",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
			"status":     "failure",
		})
		f.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 6. Sanitize "null" strings, then normalize input
	req.Origin = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.Origin)))
	req.Destination = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.Destination)))
	req.DepartureDate = strings.TrimSpace(sanitizeNull(req.DepartureDate))
	req.ReturnDate = strings.TrimSpace(sanitizeNull(req.ReturnDate))
	req.TravelClass = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.TravelClass)))

	if req.Origin == "" || req.Destination == "" || req.DepartureDate == "" {
		telemetry.Counter("flight.errors.total",
			"capability", "search_flights",
			"error_type", "validation_error",
		)
		f.Logger.WarnWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "search_flights",
			"request_id": upstreamRequestID,
			"error_type": "validation_error",
			"status":     "failure",
		})
		f.sendError(rw, "origin, destination, and departure_date are required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	// 7. Add business context to span
	telemetry.SetSpanAttributes(ctx,
		attribute.String("flight.origin", req.Origin),
		attribute.String("flight.destination", req.Destination),
		attribute.String("flight.departure_date", req.DepartureDate),
	)

	f.Logger.InfoWithContext(ctx, "Request received", map[string]interface{}{
		"operation":      "search_flights",
		"method":         r.Method,
		"path":           r.URL.Path,
		"origin":         req.Origin,
		"destination":    req.Destination,
		"departure_date": req.DepartureDate,
		"return_date":    req.ReturnDate,
		"adults":         req.Adults,
		"travel_class":   req.TravelClass,
		"request_id":     upstreamRequestID,
	})

	// 8. Span event before external API call
	telemetry.AddSpanEvent(ctx, "calling_travelpayouts_api",
		attribute.String("origin", req.Origin),
		attribute.String("destination", req.Destination),
		attribute.String("api", "flight_offers"),
	)

	// 9. Call external API with context + timing
	apiStartTime := time.Now()
	response, err := f.client.SearchFlights(ctx, req)
	apiDuration := time.Since(apiStartTime)

	// API latency histogram
	telemetry.Histogram("flight.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_flights",
		"api", "travelpayouts",
	)

	// 10. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("flight.api.errors.total",
			"capability", "search_flights",
			"error_type", "api_error",
		)
		f.Logger.WarnWithContext(ctx, "Travelpayouts API call failed", map[string]interface{}{
			"operation":   "search_flights",
			"error":       err.Error(),
			"error_type":  "api_error",
			"origin":      req.Origin,
			"destination": req.Destination,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
			"status":      "failure",
		})
		f.sendUpstreamError(rw, "Flight search failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// 11. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("flight.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_flights",
	)
	telemetry.Counter("flight.requests.total",
		"capability", "search_flights",
		"status", "success",
	)

	// 12. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("flight-tool", "search_flights", float64(duration.Milliseconds()), "success")

	// 13. Completion span event
	telemetry.AddSpanEvent(ctx, "search_flights_retrieved",
		attribute.String("origin", req.Origin),
		attribute.String("destination", req.Destination),
		attribute.Int("offers_count", len(response.Offers)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 14. Log completion with context
	f.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
		"operation":    "search_flights",
		"origin":       req.Origin,
		"destination":  req.Destination,
		"offers_count": len(response.Offers),
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
		"status":       "success",
	})

	// 15. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleSearchAirports processes airport search requests with full telemetry
func (f *FlightTool) handleSearchAirports(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "flight-tool"),
		attribute.String("truvag3.capability", "search_airports"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_airports"),
	)

	// 5. Decode request
	var req SearchAirportsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("flight.errors.total",
			"capability", "search_airports",
			"error_type", "decode_error",
		)
		f.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_airports",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
			"status":     "failure",
		})
		f.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 6. Sanitize "null" strings, then normalize
	req.Keyword = strings.TrimSpace(sanitizeNull(req.Keyword))
	req.SubType = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.SubType)))

	if req.Keyword == "" {
		telemetry.Counter("flight.errors.total",
			"capability", "search_airports",
			"error_type", "validation_error",
		)
		f.Logger.WarnWithContext(ctx, "Missing required keyword field", map[string]interface{}{
			"operation":  "search_airports",
			"request_id": upstreamRequestID,
			"error_type": "validation_error",
			"status":     "failure",
		})
		f.sendError(rw, "keyword is required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	// 7. Add business context to span
	telemetry.SetSpanAttributes(ctx,
		attribute.String("flight.keyword", req.Keyword),
	)

	f.Logger.InfoWithContext(ctx, "Request received", map[string]interface{}{
		"operation":  "search_airports",
		"method":     r.Method,
		"path":       r.URL.Path,
		"keyword":    req.Keyword,
		"sub_type":   req.SubType,
		"request_id": upstreamRequestID,
	})

	// 8. Span event before external API call
	telemetry.AddSpanEvent(ctx, "calling_travelpayouts_api",
		attribute.String("keyword", req.Keyword),
		attribute.String("api", "locations"),
	)

	// 9. Call external API with context + timing
	apiStartTime := time.Now()
	response, err := f.client.SearchAirports(ctx, req.Keyword, req.SubType)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("flight.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_airports",
		"api", "travelpayouts",
	)

	// 10. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("flight.api.errors.total",
			"capability", "search_airports",
			"error_type", "api_error",
		)
		f.Logger.WarnWithContext(ctx, "Travelpayouts API call failed", map[string]interface{}{
			"operation":   "search_airports",
			"error":       err.Error(),
			"error_type":  "api_error",
			"keyword":     req.Keyword,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
			"status":      "failure",
		})
		f.sendUpstreamError(rw, "Airport search failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// 11. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("flight.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_airports",
	)
	telemetry.Counter("flight.requests.total",
		"capability", "search_airports",
		"status", "success",
	)

	// 12. Record unified metrics
	telemetry.RecordToolCall("flight-tool", "search_airports", float64(duration.Milliseconds()), "success")

	// 13. Completion span event
	telemetry.AddSpanEvent(ctx, "search_airports_retrieved",
		attribute.String("keyword", req.Keyword),
		attribute.Int("results_count", len(response.Airports)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 14. Log completion with context
	f.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
		"operation":     "search_airports",
		"keyword":       req.Keyword,
		"results_count": len(response.Airports),
		"duration_ms":   duration.Milliseconds(),
		"request_id":    upstreamRequestID,
		"status":        "success",
	})

	// 15. Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleCheapestDates processes cheapest dates requests with full telemetry
func (f *FlightTool) handleCheapestDates(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "flight-tool"),
		attribute.String("truvag3.capability", "cheapest_dates"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "cheapest_dates"),
	)

	// 5. Decode request
	var req CheapestDatesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("flight.errors.total",
			"capability", "cheapest_dates",
			"error_type", "decode_error",
		)
		f.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "cheapest_dates",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
			"status":     "failure",
		})
		f.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 6. Sanitize "null" strings, then normalize
	req.Origin = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.Origin)))
	req.Destination = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.Destination)))
	req.DepartureDate = strings.TrimSpace(sanitizeNull(req.DepartureDate))

	if req.Origin == "" || req.Destination == "" {
		telemetry.Counter("flight.errors.total",
			"capability", "cheapest_dates",
			"error_type", "validation_error",
		)
		f.Logger.WarnWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "cheapest_dates",
			"request_id": upstreamRequestID,
			"error_type": "validation_error",
			"status":     "failure",
		})
		f.sendError(rw, "origin and destination are required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	// 7. Add business context to span
	telemetry.SetSpanAttributes(ctx,
		attribute.String("flight.origin", req.Origin),
		attribute.String("flight.destination", req.Destination),
	)

	f.Logger.InfoWithContext(ctx, "Request received", map[string]interface{}{
		"operation":      "cheapest_dates",
		"method":         r.Method,
		"path":           r.URL.Path,
		"origin":         req.Origin,
		"destination":    req.Destination,
		"departure_date": req.DepartureDate,
		"request_id":     upstreamRequestID,
	})

	// 8. Span event before external API call
	telemetry.AddSpanEvent(ctx, "calling_travelpayouts_api",
		attribute.String("origin", req.Origin),
		attribute.String("destination", req.Destination),
		attribute.String("api", "flight_dates"),
	)

	// 9. Call external API with context + timing
	apiStartTime := time.Now()
	response, err := f.client.CheapestDates(ctx, req.Origin, req.Destination, req.DepartureDate)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("flight.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "cheapest_dates",
		"api", "travelpayouts",
	)

	// 10. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("flight.api.errors.total",
			"capability", "cheapest_dates",
			"error_type", "api_error",
		)
		f.Logger.WarnWithContext(ctx, "Travelpayouts API call failed", map[string]interface{}{
			"operation":   "cheapest_dates",
			"error":       err.Error(),
			"error_type":  "api_error",
			"origin":      req.Origin,
			"destination": req.Destination,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
			"status":      "failure",
		})
		f.sendUpstreamError(rw, "Cheapest dates search failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// 11. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("flight.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "cheapest_dates",
	)
	telemetry.Counter("flight.requests.total",
		"capability", "cheapest_dates",
		"status", "success",
	)

	// 12. Record unified metrics
	telemetry.RecordToolCall("flight-tool", "cheapest_dates", float64(duration.Milliseconds()), "success")

	// 13. Completion span event
	telemetry.AddSpanEvent(ctx, "cheapest_dates_retrieved",
		attribute.String("origin", req.Origin),
		attribute.String("destination", req.Destination),
		attribute.Int("dates_count", len(response.Dates)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 14. Log completion with context
	f.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
		"operation":   "cheapest_dates",
		"origin":      req.Origin,
		"destination": req.Destination,
		"dates_count": len(response.Dates),
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
		"status":      "success",
	})

	// 15. Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleAirportRoutes processes airport routes requests with full telemetry
func (f *FlightTool) handleAirportRoutes(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "flight-tool"),
		attribute.String("truvag3.capability", "airport_routes"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "airport_routes"),
	)

	// 5. Decode request
	var req AirportRoutesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("flight.errors.total",
			"capability", "airport_routes",
			"error_type", "decode_error",
		)
		f.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "airport_routes",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
			"status":     "failure",
		})
		f.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 6. Sanitize "null" strings, then normalize
	req.DepartureAirport = strings.ToUpper(strings.TrimSpace(sanitizeNull(req.DepartureAirport)))

	if req.DepartureAirport == "" {
		telemetry.Counter("flight.errors.total",
			"capability", "airport_routes",
			"error_type", "validation_error",
		)
		f.Logger.WarnWithContext(ctx, "Missing required departure_airport field", map[string]interface{}{
			"operation":  "airport_routes",
			"request_id": upstreamRequestID,
			"error_type": "validation_error",
			"status":     "failure",
		})
		f.sendError(rw, "departure_airport is required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	// 7. Add business context to span
	telemetry.SetSpanAttributes(ctx,
		attribute.String("flight.departure_airport", req.DepartureAirport),
	)

	f.Logger.InfoWithContext(ctx, "Request received", map[string]interface{}{
		"operation":         "airport_routes",
		"method":            r.Method,
		"path":              r.URL.Path,
		"departure_airport": req.DepartureAirport,
		"request_id":        upstreamRequestID,
	})

	// 8. Span event before external API call
	telemetry.AddSpanEvent(ctx, "calling_travelpayouts_api",
		attribute.String("departure_airport", req.DepartureAirport),
		attribute.String("api", "direct_destinations"),
	)

	// 9. Call external API with context + timing
	apiStartTime := time.Now()
	response, err := f.client.AirportRoutes(ctx, req.DepartureAirport)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("flight.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "airport_routes",
		"api", "travelpayouts",
	)

	// 10. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("flight.api.errors.total",
			"capability", "airport_routes",
			"error_type", "api_error",
		)
		f.Logger.WarnWithContext(ctx, "Travelpayouts API call failed", map[string]interface{}{
			"operation":         "airport_routes",
			"error":             err.Error(),
			"error_type":        "api_error",
			"departure_airport": req.DepartureAirport,
			"duration_ms":       apiDuration.Milliseconds(),
			"request_id":        upstreamRequestID,
			"status":            "failure",
		})
		f.sendUpstreamError(rw, "Airport routes lookup failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// 11. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("flight.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "airport_routes",
	)
	telemetry.Counter("flight.requests.total",
		"capability", "airport_routes",
		"status", "success",
	)

	// 12. Record unified metrics
	telemetry.RecordToolCall("flight-tool", "airport_routes", float64(duration.Milliseconds()), "success")

	// 13. Completion span event
	telemetry.AddSpanEvent(ctx, "airport_routes_retrieved",
		attribute.String("departure_airport", req.DepartureAirport),
		attribute.Int("destinations_count", len(response.Destinations)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 14. Log completion with context
	f.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
		"operation":          "airport_routes",
		"departure_airport":  req.DepartureAirport,
		"destinations_count": len(response.Destinations),
		"duration_ms":        duration.Milliseconds(),
		"request_id":         upstreamRequestID,
		"status":             "success",
	})

	// 15. Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
