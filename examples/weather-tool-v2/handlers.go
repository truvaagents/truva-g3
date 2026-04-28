package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (w *WeatherTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

// handleWeatherForecast processes weather forecast requests
func (w *WeatherTool) handleWeatherForecast(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	w.Logger.InfoWithContext(ctx, "Processing weather forecast request", map[string]interface{}{
		"method": r.Method,
		"path":   r.URL.Path,
	})

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
	)

	var req WeatherRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"error": err.Error(),
		})
		w.sendError(rw, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Validate coordinates
	if err := w.validateCoordinates(req.Latitude, req.Longitude); err != nil {
		w.sendError(rw, err.Error(), http.StatusBadRequest, ErrCodeInvalidCoordinates)
		return
	}

	// Default to 7 days
	days := req.Days
	if days <= 0 || days > 16 {
		days = 7
	}

	w.Logger.InfoWithContext(ctx, "Fetching weather forecast", map[string]interface{}{
		"lat":  req.Latitude,
		"lon":  req.Longitude,
		"days": days,
	})

	// Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_external_api",
		attribute.String("api", "open-meteo"),
		attribute.Float64("lat", req.Latitude),
		attribute.Float64("lon", req.Longitude),
		attribute.Int("days", days),
	)

	// Call Open-Meteo API
	result, err := w.callOpenMeteoForecast(ctx, req.Latitude, req.Longitude, days)
	if err != nil {
		w.Logger.ErrorWithContext(ctx, "Open-Meteo API call failed", map[string]interface{}{
			"error": err.Error(),
			"lat":   req.Latitude,
			"lon":   req.Longitude,
		})
		// Record error on span for Jaeger visibility
		telemetry.RecordSpanError(ctx, err)
		w.sendUpstreamError(rw, "Weather API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	w.Logger.InfoWithContext(ctx, "Weather forecast retrieved successfully", map[string]interface{}{
		"lat":           req.Latitude,
		"lon":           req.Longitude,
		"forecast_days": len(result.Forecast),
	})

	// Add success span event
	telemetry.AddSpanEvent(ctx, "forecast_retrieved",
		attribute.Int("forecast_days", len(result.Forecast)),
		attribute.String("condition", result.Condition),
	)

	rw.Header().Set("Content-Type", "application/json")
	response := core.ToolResponse{
		Success: true,
		Data:    result,
	}
	json.NewEncoder(rw).Encode(response)
}

// handleCurrentWeather processes current weather requests
func (w *WeatherTool) handleCurrentWeather(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	w.Logger.InfoWithContext(ctx, "Processing current weather request", map[string]interface{}{
		"method": r.Method,
		"path":   r.URL.Path,
	})

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
	)

	var req WeatherRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"error": err.Error(),
		})
		w.sendError(rw, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Validate coordinates
	if err := w.validateCoordinates(req.Latitude, req.Longitude); err != nil {
		w.sendError(rw, err.Error(), http.StatusBadRequest, ErrCodeInvalidCoordinates)
		return
	}

	w.Logger.InfoWithContext(ctx, "Fetching current weather", map[string]interface{}{
		"lat": req.Latitude,
		"lon": req.Longitude,
	})

	// Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_external_api",
		attribute.String("api", "open-meteo"),
		attribute.Float64("lat", req.Latitude),
		attribute.Float64("lon", req.Longitude),
	)

	// Call Open-Meteo API for current weather
	result, err := w.callOpenMeteoCurrent(ctx, req.Latitude, req.Longitude)
	if err != nil {
		w.Logger.ErrorWithContext(ctx, "Open-Meteo API call failed", map[string]interface{}{
			"error": err.Error(),
			"lat":   req.Latitude,
			"lon":   req.Longitude,
		})
		// Record error on span for Jaeger visibility
		telemetry.RecordSpanError(ctx, err)
		w.sendUpstreamError(rw, "Weather API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	w.Logger.InfoWithContext(ctx, "Current weather retrieved successfully", map[string]interface{}{
		"lat":         req.Latitude,
		"lon":         req.Longitude,
		"temperature": result.CurrentTemp,
		"condition":   result.Condition,
	})

	// Add success span event
	telemetry.AddSpanEvent(ctx, "weather_retrieved",
		attribute.Float64("temperature", result.CurrentTemp),
		attribute.String("condition", result.Condition),
	)

	rw.Header().Set("Content-Type", "application/json")
	response := core.ToolResponse{
		Success: true,
		Data:    result,
	}
	json.NewEncoder(rw).Encode(response)
}

// callOpenMeteoForecast calls the Open-Meteo forecast API
func (w *WeatherTool) callOpenMeteoForecast(ctx context.Context, lat, lon float64, days int) (*WeatherResponse, error) {
	// Build request URL
	// https://api.open-meteo.com/v1/forecast?latitude=35.68&longitude=139.69&daily=temperature_2m_max,temperature_2m_min,weathercode,precipitation_sum,wind_speed_10m_max&current=temperature_2m,relative_humidity_2m,weather_code,wind_speed_10m,precipitation&timezone=auto&forecast_days=7
	reqURL := fmt.Sprintf("%s/forecast?latitude=%.4f&longitude=%.4f&daily=temperature_2m_max,temperature_2m_min,weather_code,precipitation_sum,wind_speed_10m_max&current=temperature_2m,relative_humidity_2m,weather_code,wind_speed_10m,precipitation&timezone=auto&forecast_days=%d",
		OpenMeteoBaseURL, lat, lon, days)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "TruvaG3-WeatherToolV2/1.0")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp OpenMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return w.parseOpenMeteoResponse(&apiResp, true)
}

// callOpenMeteoCurrent calls the Open-Meteo API for current weather only
func (w *WeatherTool) callOpenMeteoCurrent(ctx context.Context, lat, lon float64) (*WeatherResponse, error) {
	// Build request URL for current weather only
	reqURL := fmt.Sprintf("%s/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,relative_humidity_2m,weather_code,wind_speed_10m,precipitation&timezone=auto",
		OpenMeteoBaseURL, lat, lon)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "TruvaG3-WeatherToolV2/1.0")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp OpenMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return w.parseOpenMeteoResponse(&apiResp, false)
}

// parseOpenMeteoResponse converts API response to our format
func (w *WeatherTool) parseOpenMeteoResponse(apiResp *OpenMeteoResponse, includeForecast bool) (*WeatherResponse, error) {
	result := &WeatherResponse{
		Latitude:  apiResp.Latitude,
		Longitude: apiResp.Longitude,
		Timezone:  apiResp.Timezone,
	}

	// Parse current weather if available
	if apiResp.Current != nil {
		result.CurrentTemp = apiResp.Current.Temperature
		result.Humidity = apiResp.Current.Humidity
		result.WindSpeed = apiResp.Current.WindSpeed
		result.Precipitation = apiResp.Current.Precipitation
		result.WeatherCode = apiResp.Current.WeatherCode
		result.Condition = weatherCodeToCondition(apiResp.Current.WeatherCode)
	}

	// Parse daily forecast if available and requested
	if includeForecast && apiResp.Daily != nil && len(apiResp.Daily.Time) > 0 {
		var forecasts []DailyForecast
		var tempSum float64

		for i, date := range apiResp.Daily.Time {
			forecast := DailyForecast{
				Date:    date,
				TempMin: apiResp.Daily.TempMin[i],
				TempMax: apiResp.Daily.TempMax[i],
			}

			if i < len(apiResp.Daily.WeatherCode) {
				forecast.WeatherCode = apiResp.Daily.WeatherCode[i]
				forecast.Condition = weatherCodeToCondition(apiResp.Daily.WeatherCode[i])
			}
			if i < len(apiResp.Daily.Precipitation) {
				forecast.Precipitation = apiResp.Daily.Precipitation[i]
			}
			if i < len(apiResp.Daily.WindSpeed) {
				forecast.WindSpeed = apiResp.Daily.WindSpeed[i]
			}

			forecasts = append(forecasts, forecast)
			tempSum += (forecast.TempMin + forecast.TempMax) / 2
		}

		result.Forecast = forecasts

		// Calculate min, max, avg from forecast
		if len(forecasts) > 0 {
			result.TempMin = forecasts[0].TempMin
			result.TempMax = forecasts[0].TempMax
			for _, f := range forecasts {
				if f.TempMin < result.TempMin {
					result.TempMin = f.TempMin
				}
				if f.TempMax > result.TempMax {
					result.TempMax = f.TempMax
				}
			}
			result.TempAvg = tempSum / float64(len(forecasts))

			// Use first day's condition if no current weather
			if result.Condition == "" && len(forecasts) > 0 {
				result.Condition = forecasts[0].Condition
				result.WeatherCode = forecasts[0].WeatherCode
			}
		}
	}

	return result, nil
}

// validateCoordinates validates latitude and longitude values
func (w *WeatherTool) validateCoordinates(lat, lon float64) error {
	if lat < -90 || lat > 90 {
		return fmt.Errorf("latitude must be between -90 and 90")
	}
	if lon < -180 || lon > 180 {
		return fmt.Errorf("longitude must be between -180 and 180")
	}
	return nil
}

// sendError sends a structured error response
func (w *WeatherTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)

	response := core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "UNAVAILABLE"),
		},
	}
	json.NewEncoder(rw).Encode(response)
}
