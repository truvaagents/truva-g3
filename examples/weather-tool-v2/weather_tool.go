package main

import (
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// WeatherTool provides weather forecast capabilities using Open-Meteo API
type WeatherTool struct {
	*core.BaseTool
	httpClient *http.Client
}

// WeatherRequest represents the input for weather requests
type WeatherRequest struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lon"`
	Days      int     `json:"days,omitempty"` // 1-16, default 7
}

// WeatherResponse represents the weather result
type WeatherResponse struct {
	Location      string          `json:"location,omitempty"`
	Latitude      float64         `json:"lat"`
	Longitude     float64         `json:"lon"`
	Timezone      string          `json:"timezone"`
	CurrentTemp   float64         `json:"temperature_current,omitempty"`
	TempMin       float64         `json:"temperature_min"`
	TempMax       float64         `json:"temperature_max"`
	TempAvg       float64         `json:"temperature_avg"`
	Condition     string          `json:"condition"`
	WeatherCode   int             `json:"weather_code"`
	Humidity      int             `json:"humidity,omitempty"`
	WindSpeed     float64         `json:"wind_speed,omitempty"`
	Precipitation float64         `json:"precipitation,omitempty"`
	Forecast      []DailyForecast `json:"forecast,omitempty"`
}

// DailyForecast represents a single day's forecast
type DailyForecast struct {
	Date          string  `json:"date"`
	TempMin       float64 `json:"temperature_min"`
	TempMax       float64 `json:"temperature_max"`
	Condition     string  `json:"condition"`
	WeatherCode   int     `json:"weather_code"`
	Precipitation float64 `json:"precipitation"`
	WindSpeed     float64 `json:"wind_speed_max"`
}

// OpenMeteoResponse represents the Open-Meteo API response
type OpenMeteoResponse struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
	Current   *struct {
		Temperature   float64 `json:"temperature_2m"`
		Humidity      int     `json:"relative_humidity_2m"`
		WeatherCode   int     `json:"weather_code"`
		WindSpeed     float64 `json:"wind_speed_10m"`
		Precipitation float64 `json:"precipitation"`
	} `json:"current,omitempty"`
	Daily *struct {
		Time          []string  `json:"time"`
		TempMax       []float64 `json:"temperature_2m_max"`
		TempMin       []float64 `json:"temperature_2m_min"`
		WeatherCode   []int     `json:"weather_code"`
		Precipitation []float64 `json:"precipitation_sum"`
		WindSpeed     []float64 `json:"wind_speed_10m_max"`
	} `json:"daily,omitempty"`
}

// Error codes for weather tool
const (
	ErrCodeInvalidCoordinates = "INVALID_COORDINATES"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
)

// OpenMeteoBaseURL is the base URL for Open-Meteo API
const OpenMeteoBaseURL = "https://api.open-meteo.com/v1"

// NewWeatherTool creates a new weather tool instance
func NewWeatherTool() *WeatherTool {
	// Create traced HTTP client for distributed tracing
	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	}

	tool := &WeatherTool{
		BaseTool: core.NewTool("weather-tool-v2"),
		httpClient: &http.Client{
			Transport: otelhttp.NewTransport(transport),
			Timeout:   30 * time.Second,
		},
	}

	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up weather capabilities
func (w *WeatherTool) registerCapabilities() {
	// Capability 1: Get weather forecast
	w.RegisterCapability(core.Capability{
		Name:        "get_weather_forecast",
		Description: "Gets weather forecast for a location using coordinates, including current conditions and multi-day outlook.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     w.handleWeatherForecast,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "lat",
					Type:        "number",
					Example:     "35.6762",
					Description: "Latitude coordinate (-90 to 90)",
				},
				{
					Name:        "lon",
					Type:        "number",
					Example:     "139.6503",
					Description: "Longitude coordinate (-180 to 180)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "days",
					Type:        "number",
					Example:     "7",
					Description: "Number of forecast days (1-16, default 7)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "lat", Type: "number", Description: "Latitude of the location"},
				{Name: "lon", Type: "number", Description: "Longitude of the location"},
				{Name: "timezone", Type: "string", Description: "Timezone of the location"},
				{Name: "temperature_min", Type: "number", Description: "Minimum temperature across forecast period"},
				{Name: "temperature_max", Type: "number", Description: "Maximum temperature across forecast period"},
				{Name: "temperature_avg", Type: "number", Description: "Average temperature across forecast period"},
				{Name: "condition", Type: "string", Description: "Current weather condition description"},
				{Name: "weather_code", Type: "number", Description: "WMO weather condition code"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "location", Type: "string", Description: "Location name if available"},
				{Name: "temperature_current", Type: "number", Description: "Current temperature"},
				{Name: "humidity", Type: "number", Description: "Current relative humidity percentage"},
				{Name: "wind_speed", Type: "number", Description: "Current wind speed"},
				{Name: "precipitation", Type: "number", Description: "Current precipitation amount"},
				{Name: "forecast", Type: "array", Description: "Array of daily forecast objects with date, temperature_min, temperature_max, condition, weather_code, precipitation, wind_speed_max"},
			},
		},
	})

	// Capability 2: Get current weather only
	w.RegisterCapability(core.Capability{
		Name:        "get_current_weather",
		Description: "Gets current weather conditions for a location including temperature, humidity, and wind.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     w.handleCurrentWeather,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "lat",
					Type:        "number",
					Example:     "40.7128",
					Description: "Latitude coordinate (-90 to 90)",
				},
				{
					Name:        "lon",
					Type:        "number",
					Example:     "-74.0060",
					Description: "Longitude coordinate (-180 to 180)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "lat", Type: "number", Description: "Latitude of the location"},
				{Name: "lon", Type: "number", Description: "Longitude of the location"},
				{Name: "timezone", Type: "string", Description: "Timezone of the location"},
				{Name: "condition", Type: "string", Description: "Current weather condition description"},
				{Name: "weather_code", Type: "number", Description: "WMO weather condition code"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "temperature_current", Type: "number", Description: "Current temperature in Celsius"},
				{Name: "humidity", Type: "number", Description: "Current relative humidity percentage"},
				{Name: "wind_speed", Type: "number", Description: "Current wind speed"},
				{Name: "precipitation", Type: "number", Description: "Current precipitation amount"},
			},
		},
	})
}

// weatherCodeToCondition converts WMO weather code to human-readable condition
func weatherCodeToCondition(code int) string {
	conditions := map[int]string{
		0:  "Clear sky",
		1:  "Mainly clear",
		2:  "Partly cloudy",
		3:  "Overcast",
		45: "Foggy",
		48: "Depositing rime fog",
		51: "Light drizzle",
		53: "Moderate drizzle",
		55: "Dense drizzle",
		56: "Light freezing drizzle",
		57: "Dense freezing drizzle",
		61: "Slight rain",
		63: "Moderate rain",
		65: "Heavy rain",
		66: "Light freezing rain",
		67: "Heavy freezing rain",
		71: "Slight snow fall",
		73: "Moderate snow fall",
		75: "Heavy snow fall",
		77: "Snow grains",
		80: "Slight rain showers",
		81: "Moderate rain showers",
		82: "Violent rain showers",
		85: "Slight snow showers",
		86: "Heavy snow showers",
		95: "Thunderstorm",
		96: "Thunderstorm with slight hail",
		99: "Thunderstorm with heavy hail",
	}

	if condition, ok := conditions[code]; ok {
		return condition
	}
	return "Unknown"
}
