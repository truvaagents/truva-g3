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

// handleCurrentWeather processes current weather requests
func (w *WeatherTool) handleCurrentWeather(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "current_weather"),
	)

	// Log the request (framework auto-injects logger)
	// Using context-aware logging for distributed tracing and request correlation
	w.Logger.InfoWithContext(ctx, "Processing current weather request", map[string]interface{}{
		"method": r.Method,
		"path":   r.URL.Path,
	})

	var req WeatherRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Log the incoming request details
	w.Logger.InfoWithContext(ctx, "Received weather request", map[string]interface{}{
		"location": req.Location,
		"units":    req.Units,
	})

	// Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_weather_api",
		attribute.String("location", req.Location),
		attribute.String("units", req.Units),
	)

	// Try to get real weather data from API
	startTime := time.Now()
	weather, err := w.fetchRealWeatherData(ctx, req.Location, req.Units)
	apiDuration := time.Since(startTime)

	rw.Header().Set("Content-Type", "application/json")

	if err != nil {
		// Record error on span for Jaeger visibility
		telemetry.RecordSpanError(ctx, err)

		// Return structured error instead of mock data
		// This allows agents to understand the error and potentially fix/retry
		toolErr := w.classifyError(err, req.Location)

		w.Logger.ErrorWithContext(ctx, "Weather API call failed - returning structured error", map[string]interface{}{
			"error":       err.Error(),
			"error_code":  toolErr.Code,
			"category":    toolErr.Category,
			"retryable":   toolErr.Retryable,
			"location":    req.Location,
			"api_latency": apiDuration.String(),
		})

		// Return appropriate HTTP status based on error category using core utility
		statusCode := core.HTTPStatusForCategory(toolErr.Category)

		response := core.ToolResponse{
			Success: false,
			Error:   toolErr,
		}

		rw.WriteHeader(statusCode)
		json.NewEncoder(rw).Encode(response)
		return
	}

	// Log successful API call
	w.Logger.InfoWithContext(ctx, "Weather API call successful", map[string]interface{}{
		"location":    req.Location,
		"api_latency": apiDuration.String(),
		"source":      weather.Source,
	})

	// Store in memory for caching (framework auto-injects memory)
	cacheKey := fmt.Sprintf("current:%s", strings.ToLower(req.Location))
	cacheData, _ := json.Marshal(weather)
	w.cache.Set(ctx, cacheKey, string(cacheData), 5*time.Minute)

	// Return success response using core.ToolResponse
	response := core.ToolResponse{
		Success: true,
		Data:    weather,
	}
	json.NewEncoder(rw).Encode(response)

	// Add success span event
	telemetry.AddSpanEvent(ctx, "weather_retrieved",
		attribute.String("location", req.Location),
		attribute.Float64("temperature", weather.Temperature),
		attribute.String("condition", weather.Condition),
		attribute.String("source", weather.Source),
	)

	// Log the response data
	w.Logger.InfoWithContext(ctx, "Current weather request completed", map[string]interface{}{
		"location":    req.Location,
		"temperature": weather.Temperature,
		"condition":   weather.Condition,
		"humidity":    weather.Humidity,
		"source":      weather.Source,
	})
}

// NOTE: handleForecast removed - use weather-tool-v2 for real forecast data via Open-Meteo API

// AlertsRequest represents the input for weather alerts
type AlertsRequest struct {
	Location string `json:"location"`
}

// handleAlerts processes weather alert requests
func (w *WeatherTool) handleAlerts(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "alerts"),
	)

	// Parse location from JSON request body (orchestrator sends POST with JSON body)
	var req AlertsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Logger.WarnWithContext(ctx, "Failed to decode alerts request", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	if req.Location == "" {
		w.Logger.WarnWithContext(ctx, "Alert request missing location parameter", nil)
		http.Error(rw, "location parameter is required", http.StatusBadRequest)
		return
	}

	w.Logger.InfoWithContext(ctx, "Received weather alerts request", map[string]interface{}{
		"location": req.Location,
	})

	// Simulate alert data
	alerts := []map[string]interface{}{
		{
			"type":        "thunderstorm",
			"severity":    "moderate",
			"description": "Thunderstorms possible this afternoon",
			"start_time":  time.Now().Format(time.RFC3339),
			"end_time":    time.Now().Add(4 * time.Hour).Format(time.RFC3339),
		},
	}

	response := map[string]interface{}{
		"location": req.Location,
		"alerts":   alerts,
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)

	// Add success span event
	telemetry.AddSpanEvent(ctx, "alerts_retrieved",
		attribute.String("location", req.Location),
		attribute.Int("alert_count", len(alerts)),
		attribute.Bool("has_alerts", len(alerts) > 0),
	)

	// Log the response summary
	w.Logger.InfoWithContext(ctx, "Weather alerts request completed", map[string]interface{}{
		"location":    req.Location,
		"alert_count": len(alerts),
		"has_alerts":  len(alerts) > 0,
	})
}
