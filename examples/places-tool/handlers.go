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

// capitalizeFirst uppercases the first character of a string.
// Replaces deprecated strings.Title() — safe for ASCII provider names.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (p *PlacesTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
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

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (p *PlacesTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

func (p *PlacesTool) handleSearchPlaces(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "places-tool"),
		attribute.String("truvag3.capability", "search_places"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_places"),
	)

	p.Logger.InfoWithContext(ctx, "Processing search places request", map[string]interface{}{
		"operation":  "search_places",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	var req SearchPlacesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("places.errors.total", "capability", "search_places", "error_type", "decode_error")
		p.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation": "search_places", "request_id": upstreamRequestID, "error": err.Error(), "error_type": "decode_error",
		})
		p.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.Query = strings.TrimSpace(req.Query)
	req.Location = strings.TrimSpace(req.Location)
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))

	if req.Query == "" {
		telemetry.Counter("places.errors.total", "capability", "search_places", "error_type", "validation_error")
		p.Logger.WarnWithContext(ctx, "Missing required query field", map[string]interface{}{
			"operation": "search_places", "request_id": upstreamRequestID, "error_type": "validation_error",
		})
		p.sendError(rw, "query is required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	provider := req.Provider
	if provider == "" {
		provider = p.defaultProvider
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("places.query", req.Query),
		attribute.String("places.provider", provider),
		attribute.Float64("places.lat", req.Lat),
		attribute.Float64("places.lon", req.Lon),
	)

	p.Logger.InfoWithContext(ctx, "Received search places request", map[string]interface{}{
		"operation":  "search_places",
		"query":      req.Query,
		"lat":        req.Lat,
		"lon":        req.Lon,
		"location":   req.Location,
		"provider":   provider,
		"request_id": upstreamRequestID,
	})

	telemetry.AddSpanEvent(ctx, "calling_places_api",
		attribute.String("query", req.Query),
		attribute.String("provider", provider),
		attribute.String("api", "search_places"),
	)

	apiStartTime := time.Now()
	var results []PlaceResult
	var err error

	switch provider {
	case "geoapify":
		results, err = p.geoapify.SearchPlaces(ctx, req)
	default:
		results, err = p.foursquare.SearchPlaces(ctx, req)
		provider = "foursquare"
	}

	apiDuration := time.Since(apiStartTime)
	telemetry.Histogram("places.api.duration_ms", float64(apiDuration.Milliseconds()),
		"capability", "search_places", "api", provider)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("places.api.errors.total", "capability", "search_places", "error_type", "api_error")
		p.Logger.WarnWithContext(ctx, fmt.Sprintf("%s API call failed", provider), map[string]interface{}{
			"operation": "search_places", "error": err.Error(), "query": req.Query,
			"provider": provider, "duration_ms": apiDuration.Milliseconds(), "request_id": upstreamRequestID,
		})
		p.sendUpstreamError(rw, "Place search failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	response := SearchPlacesResponse{
		Query:    req.Query,
		Places:   results,
		Provider: provider,
		Source:   fmt.Sprintf("%s Places API", capitalizeFirst(provider)),
	}

	duration := time.Since(startTime)
	telemetry.Histogram("places.request.duration_ms", float64(duration.Milliseconds()), "capability", "search_places")
	telemetry.Counter("places.requests.total", "capability", "search_places", "status", "success")
	telemetry.RecordToolCall("places-tool", "search_places", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "search_places_retrieved",
		attribute.String("query", req.Query),
		attribute.String("provider", provider),
		attribute.Int("results_count", len(results)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	p.Logger.InfoWithContext(ctx, "Search places request completed", map[string]interface{}{
		"operation":     "search_places",
		"query":         req.Query,
		"provider":      provider,
		"results_count": len(results),
		"duration_ms":   duration.Milliseconds(),
		"request_id":    upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

func (p *PlacesTool) handlePlaceDetails(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "places-tool"),
		attribute.String("truvag3.capability", "place_details"),
	)
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method), attribute.String("path", r.URL.Path), attribute.String("operation", "place_details"),
	)

	p.Logger.InfoWithContext(ctx, "Processing place details request", map[string]interface{}{
		"operation": "place_details", "method": r.Method, "request_id": upstreamRequestID,
	})

	var req PlaceDetailsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("places.errors.total", "capability", "place_details", "error_type", "decode_error")
		p.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation": "place_details", "request_id": upstreamRequestID, "error": err.Error(),
		})
		p.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.PlaceID = strings.TrimSpace(req.PlaceID)
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))

	if req.PlaceID == "" {
		telemetry.Counter("places.errors.total", "capability", "place_details", "error_type", "validation_error")
		p.sendError(rw, "place_id is required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	provider := req.Provider
	if provider == "" {
		provider = p.defaultProvider
	}

	telemetry.SetSpanAttributes(ctx, attribute.String("places.place_id", req.PlaceID), attribute.String("places.provider", provider))

	telemetry.AddSpanEvent(ctx, "calling_places_api",
		attribute.String("place_id", req.PlaceID), attribute.String("provider", provider),
	)

	apiStartTime := time.Now()
	var response *PlaceDetailsResponse
	var err error

	switch provider {
	case "geoapify":
		response, err = p.geoapify.GetPlaceDetails(ctx, req.PlaceID)
	default:
		response, err = p.foursquare.GetPlaceDetails(ctx, req.PlaceID)
		provider = "foursquare"
	}

	apiDuration := time.Since(apiStartTime)
	telemetry.Histogram("places.api.duration_ms", float64(apiDuration.Milliseconds()),
		"capability", "place_details", "api", provider)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("places.api.errors.total", "capability", "place_details", "error_type", "api_error")
		p.Logger.WarnWithContext(ctx, "Place details API call failed", map[string]interface{}{
			"operation": "place_details", "error": err.Error(), "place_id": req.PlaceID,
			"provider": provider, "duration_ms": apiDuration.Milliseconds(), "request_id": upstreamRequestID,
		})
		p.sendUpstreamError(rw, "Place details lookup failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	duration := time.Since(startTime)
	telemetry.Histogram("places.request.duration_ms", float64(duration.Milliseconds()), "capability", "place_details")
	telemetry.Counter("places.requests.total", "capability", "place_details", "status", "success")
	telemetry.RecordToolCall("places-tool", "place_details", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "place_details_retrieved",
		attribute.String("place_id", req.PlaceID), attribute.String("provider", provider),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	p.Logger.InfoWithContext(ctx, "Place details request completed", map[string]interface{}{
		"operation": "place_details", "place_id": req.PlaceID, "provider": provider,
		"duration_ms": duration.Milliseconds(), "request_id": upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

func (p *PlacesTool) handleNearbyPlaces(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "places-tool"),
		attribute.String("truvag3.capability", "nearby_places"),
	)
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method), attribute.String("path", r.URL.Path), attribute.String("operation", "nearby_places"),
	)

	p.Logger.InfoWithContext(ctx, "Processing nearby places request", map[string]interface{}{
		"operation": "nearby_places", "method": r.Method, "request_id": upstreamRequestID,
	})

	var req NearbyPlacesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("places.errors.total", "capability", "nearby_places", "error_type", "decode_error")
		p.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation": "nearby_places", "request_id": upstreamRequestID, "error": err.Error(),
		})
		p.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))

	if req.Lat == 0 && req.Lon == 0 {
		telemetry.Counter("places.errors.total", "capability", "nearby_places", "error_type", "validation_error")
		p.sendError(rw, "lat and lon are required", http.StatusBadRequest, "MISSING_FIELDS")
		return
	}

	provider := req.Provider
	if provider == "" {
		provider = p.defaultProvider
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.Float64("places.lat", req.Lat),
		attribute.Float64("places.lon", req.Lon),
		attribute.String("places.provider", provider),
	)

	p.Logger.InfoWithContext(ctx, "Received nearby places request", map[string]interface{}{
		"operation": "nearby_places", "lat": req.Lat, "lon": req.Lon,
		"categories": req.Categories, "provider": provider, "request_id": upstreamRequestID,
	})

	telemetry.AddSpanEvent(ctx, "calling_places_api",
		attribute.Float64("lat", req.Lat), attribute.Float64("lon", req.Lon),
		attribute.String("provider", provider), attribute.String("api", "nearby_places"),
	)

	apiStartTime := time.Now()
	var results []PlaceResult
	var err error

	switch provider {
	case "geoapify":
		results, err = p.geoapify.NearbyPlaces(ctx, req)
	default:
		results, err = p.foursquare.NearbyPlaces(ctx, req)
		provider = "foursquare"
	}

	apiDuration := time.Since(apiStartTime)
	telemetry.Histogram("places.api.duration_ms", float64(apiDuration.Milliseconds()),
		"capability", "nearby_places", "api", provider)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("places.api.errors.total", "capability", "nearby_places", "error_type", "api_error")
		p.Logger.WarnWithContext(ctx, "Nearby places API call failed", map[string]interface{}{
			"operation": "nearby_places", "error": err.Error(),
			"lat": req.Lat, "lon": req.Lon, "provider": provider,
			"duration_ms": apiDuration.Milliseconds(), "request_id": upstreamRequestID,
		})
		p.sendUpstreamError(rw, "Nearby places search failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	response := NearbyPlacesResponse{
		Lat:      req.Lat,
		Lon:      req.Lon,
		Places:   results,
		Provider: provider,
		Source:   fmt.Sprintf("%s Places API", capitalizeFirst(provider)),
	}

	duration := time.Since(startTime)
	telemetry.Histogram("places.request.duration_ms", float64(duration.Milliseconds()), "capability", "nearby_places")
	telemetry.Counter("places.requests.total", "capability", "nearby_places", "status", "success")
	telemetry.RecordToolCall("places-tool", "nearby_places", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "nearby_places_retrieved",
		attribute.Float64("lat", req.Lat), attribute.Float64("lon", req.Lon),
		attribute.String("provider", provider), attribute.Int("results_count", len(results)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	p.Logger.InfoWithContext(ctx, "Nearby places request completed", map[string]interface{}{
		"operation": "nearby_places", "lat": req.Lat, "lon": req.Lon,
		"provider": provider, "results_count": len(results),
		"duration_ms": duration.Milliseconds(), "request_id": upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
