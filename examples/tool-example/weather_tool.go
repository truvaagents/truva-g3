package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// WeatherTool is a focused tool that provides weather-related capabilities
// It demonstrates the passive tool pattern - can register but not discover
type WeatherTool struct {
	*core.BaseTool
	apiKey string
	cache  *core.MemoryStore // local response cache
}

// WeatherRequest represents the input structure for weather requests
type WeatherRequest struct {
	Location string `json:"location"`
	Units    string `json:"units,omitempty"` // "metric" or "imperial"
	Days     int    `json:"days,omitempty"`  // For forecast
}

// WeatherResponse represents the output structure
type WeatherResponse struct {
	Location    string  `json:"location"`
	Temperature float64 `json:"temperature"`
	Humidity    int     `json:"humidity"`
	Condition   string  `json:"condition"`
	WindSpeed   float64 `json:"wind_speed"`
	Timestamp   string  `json:"timestamp"`
	Source      string  `json:"source"`
}

// Error codes for weather tool (tool-specific codes within standard core.ErrorCategory)
const (
	ErrCodeLocationNotFound   = "LOCATION_NOT_FOUND"
	ErrCodeAPIKeyMissing      = "API_KEY_MISSING"
	ErrCodeAPIKeyInvalid      = "API_KEY_INVALID"
	ErrCodeRateLimitExceeded  = "RATE_LIMIT_EXCEEDED"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
)

// NewWeatherTool creates a new weather analysis tool
func NewWeatherTool() *WeatherTool {
	tool := &WeatherTool{
		BaseTool: core.NewTool("weather-tool"),
		apiKey:   os.Getenv("WEATHER_API_KEY"),
		cache:    core.NewMemoryStore(),
	}

	// Register multiple focused capabilities
	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all weather-related capabilities
func (w *WeatherTool) registerCapabilities() {
	// Capability 1: Current weather (auto-generated endpoint: /api/capabilities/current_weather)
	// Phase 1: Description for AI-based generation
	// Phase 2: InputSummary with field hints for improved accuracy
	// Phase 3: Schema endpoint auto-generated at /api/capabilities/current_weather/schema
	w.RegisterCapability(core.Capability{
		Name:        "current_weather",
		Description: "Gets current weather conditions for a location.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     w.handleCurrentWeather,

		// Phase 2: Compact field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "location",
					Type:        "string",
					Example:     "London",
					Description: "City name or coordinates (lat,lon)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "units",
					Type:        "string",
					Example:     "metric",
					Description: "Temperature unit: metric or imperial",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "location", Type: "string", Description: "City name of the weather location"},
				{Name: "temperature", Type: "number", Description: "Current temperature value"},
				{Name: "humidity", Type: "number", Description: "Relative humidity percentage"},
				{Name: "condition", Type: "string", Description: "Weather condition description"},
				{Name: "wind_speed", Type: "number", Description: "Wind speed value"},
				{Name: "timestamp", Type: "string", Description: "ISO 8601 timestamp of the observation"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
	})

	// Capability 2: Weather alerts (custom endpoint)
	// NOTE: Forecast capability removed - use weather-tool-v2 for real forecast data via Open-Meteo API
	w.RegisterCapability(core.Capability{
		Name:        "alerts",
		Description: "Gets severe weather alerts for a location",
		Endpoint:    "/weather/alerts", // Custom endpoint
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     w.handleAlerts,
		// Note: This capability uses query parameters, so no InputSummary needed

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "location", Type: "string", Description: "Location for which alerts are returned"},
				{Name: "alerts", Type: "array", Description: "Array of alert objects with type, severity, description, start_time, end_time"},
			},
		},
	})

	// Capability 3: Historical analysis (no handler = uses generic handler)
	w.RegisterCapability(core.Capability{
		Name:        "historical_analysis",
		Description: "Analyzes historical weather patterns for a location over a date range.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		// No handler provided - framework provides generic response

		// Phase 2: Field hints even without custom handler (helps agent generate correct payloads)
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "location",
					Type:        "string",
					Example:     "Tokyo",
					Description: "City name for historical analysis",
				},
				{
					Name:        "start_date",
					Type:        "string",
					Example:     "2024-01-01",
					Description: "Start date in YYYY-MM-DD format",
				},
				{
					Name:        "end_date",
					Type:        "string",
					Example:     "2024-01-31",
					Description: "End date in YYYY-MM-DD format",
				},
			},
		},
	})
}
