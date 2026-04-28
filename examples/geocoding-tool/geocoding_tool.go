package main

import (
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// GeocodingTool provides location geocoding capabilities using Nominatim API
type GeocodingTool struct {
	*core.BaseTool
	httpClient *http.Client
}

// GeocodeRequest represents the input for geocoding requests
type GeocodeRequest struct {
	Location string `json:"location"` // e.g., "Tokyo, Japan"
}

// GeocodeResponse represents the geocoding result
type GeocodeResponse struct {
	Latitude    float64 `json:"lat"`
	Longitude   float64 `json:"lon"`
	DisplayName string  `json:"display_name"`
	CountryCode string  `json:"country_code"`
	Country     string  `json:"country"`
	City        string  `json:"city,omitempty"`
	State       string  `json:"state,omitempty"`
}

// ReverseGeocodeRequest represents input for reverse geocoding
type ReverseGeocodeRequest struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lon"`
}

// NominatimResponse represents the Nominatim API response structure
type NominatimResponse struct {
	Lat         string            `json:"lat"`
	Lon         string            `json:"lon"`
	DisplayName string            `json:"display_name"`
	Address     map[string]string `json:"address,omitempty"`
}

// Error codes for geocoding tool
const (
	ErrCodeLocationNotFound   = "LOCATION_NOT_FOUND"
	ErrCodeRateLimitExceeded  = "RATE_LIMIT_EXCEEDED"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
)

// NominatimBaseURL is the base URL for Nominatim API
const NominatimBaseURL = "https://nominatim.openstreetmap.org"

// NewGeocodingTool creates a new geocoding tool instance
func NewGeocodingTool() *GeocodingTool {
	// Create traced HTTP client for distributed tracing
	// Uses otelhttp to propagate trace context to external APIs
	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	}

	tool := &GeocodingTool{
		BaseTool: core.NewTool("geocoding-tool"),
		httpClient: &http.Client{
			Transport: otelhttp.NewTransport(transport),
			Timeout:   10 * time.Second, // Prevent hanging on slow Nominatim responses
		},
	}

	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up geocoding capabilities
func (g *GeocodingTool) registerCapabilities() {
	// Capability 1: Forward geocoding (location name -> coordinates)
	g.RegisterCapability(core.Capability{
		Name:        "geocode_location",
		Description: "Converts a location name to geographic coordinates (latitude/longitude).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     g.handleGeocode,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "location",
					Type:        "string",
					Example:     "Tokyo, Japan",
					Description: "Location name to geocode (city, address, landmark)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "lat", Type: "number", Description: "Latitude coordinate"},
				{Name: "lon", Type: "number", Description: "Longitude coordinate"},
				{Name: "display_name", Type: "string", Description: "Full display name of the location"},
				{Name: "country_code", Type: "string", Description: "ISO country code (lowercase)"},
				{Name: "country", Type: "string", Description: "Country name"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "city", Type: "string", Description: "City or town name"},
				{Name: "state", Type: "string", Description: "State or province name"},
			},
		},
	})

	// Capability 2: Reverse geocoding (coordinates -> location name)
	g.RegisterCapability(core.Capability{
		Name:        "reverse_geocode",
		Description: "Converts geographic coordinates to a human-readable location name and address.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     g.handleReverseGeocode,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "lat",
					Type:        "number",
					Example:     "35.6762",
					Description: "Latitude coordinate",
				},
				{
					Name:        "lon",
					Type:        "number",
					Example:     "139.6503",
					Description: "Longitude coordinate",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "lat", Type: "number", Description: "Latitude coordinate"},
				{Name: "lon", Type: "number", Description: "Longitude coordinate"},
				{Name: "display_name", Type: "string", Description: "Full display name of the location"},
				{Name: "country_code", Type: "string", Description: "ISO country code (lowercase)"},
				{Name: "country", Type: "string", Description: "Country name"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "city", Type: "string", Description: "City or town name"},
				{Name: "state", Type: "string", Description: "State or province name"},
			},
		},
	})
}
