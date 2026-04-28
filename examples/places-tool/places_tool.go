package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// PlacesTool provides local place search capabilities via Foursquare + Geoapify APIs
// Dual-provider design: Foursquare primary, Geoapify fallback
type PlacesTool struct {
	*core.BaseTool
	foursquare      *FoursquareClient
	geoapify        *GeoapifyClient
	defaultProvider string
}

// SearchPlacesRequest represents the input for place search
type SearchPlacesRequest struct {
	Query      string  `json:"query"`                    // Search term (e.g., "sushi restaurants")
	Lat        float64 `json:"lat,omitempty"`            // Latitude
	Lon        float64 `json:"lon,omitempty"`            // Longitude
	Location   string  `json:"location,omitempty"`       // City name (alternative to lat/lon)
	Radius     int     `json:"radius,omitempty"`         // Search radius in meters (default 1000)
	Categories string  `json:"categories,omitempty"`     // Comma-separated categories
	Limit      int     `json:"limit,omitempty"`          // Max results (default 10)
	Provider   string  `json:"provider,omitempty"`       // "foursquare" or "geoapify"
}

// SearchPlacesResponse represents the output for place search
type SearchPlacesResponse struct {
	Query    string        `json:"query"`
	Places   []PlaceResult `json:"places"`
	Provider string        `json:"provider"`
	Source   string        `json:"source"`
}

// PlaceResult represents a single place result (normalized across providers)
type PlaceResult struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Address    string   `json:"address"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
	Categories []string `json:"categories"`
	Distance   int      `json:"distance_meters"`
	Provider   string   `json:"provider"`
}

// PlaceDetailsRequest represents the input for place details
type PlaceDetailsRequest struct {
	PlaceID  string `json:"place_id"`            // Provider-specific ID
	Provider string `json:"provider,omitempty"`   // "foursquare" or "geoapify"
}

// PlaceDetailsResponse represents the output for place details
type PlaceDetailsResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Lat         float64  `json:"lat"`
	Lon         float64  `json:"lon"`
	Categories  []string `json:"categories"`
	Phone       string   `json:"phone,omitempty"`
	Website     string   `json:"website,omitempty"`
	Hours       string   `json:"hours,omitempty"`
	Rating      float64  `json:"rating,omitempty"`
	Provider    string   `json:"provider"`
	Source      string   `json:"source"`
}

// NearbyPlacesRequest represents the input for nearby places search
type NearbyPlacesRequest struct {
	Lat        float64 `json:"lat"`                      // Required: latitude
	Lon        float64 `json:"lon"`                      // Required: longitude
	Categories string  `json:"categories,omitempty"`     // Comma-separated categories
	Radius     int     `json:"radius,omitempty"`         // Search radius in meters (default 1000)
	Limit      int     `json:"limit,omitempty"`          // Max results (default 10)
	Provider   string  `json:"provider,omitempty"`       // "foursquare" or "geoapify"
}

// NearbyPlacesResponse represents the output for nearby places
type NearbyPlacesResponse struct {
	Lat      float64       `json:"lat"`
	Lon      float64       `json:"lon"`
	Places   []PlaceResult `json:"places"`
	Provider string        `json:"provider"`
	Source   string        `json:"source"`
}

// NewPlacesTool creates a new places search tool
func NewPlacesTool() *PlacesTool {
	foursquareKey := os.Getenv("FOURSQUARE_API_KEY")
	geoapifyKey := os.Getenv("GEOAPIFY_API_KEY")
	defaultProvider := os.Getenv("PLACES_DEFAULT_PROVIDER")
	if defaultProvider == "" {
		defaultProvider = "foursquare"
	}

	tool := &PlacesTool{
		BaseTool:        core.NewTool("places-tool"),
		foursquare:      NewFoursquareClient(foursquareKey),
		geoapify:        NewGeoapifyClient(geoapifyKey),
		defaultProvider: defaultProvider,
	}

	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all place-related capabilities
func (p *PlacesTool) registerCapabilities() {
	// Capability 1: Search Places
	p.RegisterCapability(core.Capability{
		Name: "search_places",
		Description: "Searches for restaurants, attractions, cafes, nightlife, and other places near a location.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     p.handleSearchPlaces,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "sushi restaurants",
					Description: "Search term for places (e.g., 'sushi restaurants', 'museums', 'cafes')",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "lat",
					Type:        "number",
					Example:     "35.6762",
					Description: "Latitude coordinate for search center",
				},
				{
					Name:        "lon",
					Type:        "number",
					Example:     "139.6503",
					Description: "Longitude coordinate for search center",
				},
				{
					Name:        "location",
					Type:        "string",
					Example:     "Tokyo",
					Description: "City name (alternative to lat/lon coordinates)",
				},
				{
					Name:        "radius",
					Type:        "integer",
					Example:     "1000",
					Description: "Search radius in meters (default 1000)",
				},
				{
					Name:        "categories",
					Type:        "string",
					Example:     "restaurant,cafe",
					Description: "Comma-separated category filter",
				},
				{
					Name:        "limit",
					Type:        "integer",
					Example:     "10",
					Description: "Maximum number of results (default 10)",
				},
				{
					Name:        "provider",
					Type:        "string",
					Example:     "foursquare",
					Description: "API provider: foursquare or geoapify (default foursquare)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The search query used"},
				{Name: "places", Type: "array", Description: "Array of place results with id, name, address, lat, lon, categories, distance_meters, and provider"},
				{Name: "provider", Type: "string", Description: "API provider used for the search"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	// Capability 2: Place Details
	p.RegisterCapability(core.Capability{
		Name: "place_details",
		Description: "Gets detailed information about a specific place by its provider ID.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     p.handlePlaceDetails,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "place_id",
					Type:        "string",
					Example:     "4b0587daf964a520baa222e3",
					Description: "Provider-specific place ID from a previous search",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "provider",
					Type:        "string",
					Example:     "foursquare",
					Description: "API provider that owns the place_id (default foursquare)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "id", Type: "string", Description: "Provider-specific place ID"},
				{Name: "name", Type: "string", Description: "Place name"},
				{Name: "address", Type: "string", Description: "Place address"},
				{Name: "lat", Type: "number", Description: "Latitude coordinate"},
				{Name: "lon", Type: "number", Description: "Longitude coordinate"},
				{Name: "categories", Type: "array", Description: "Array of place category strings"},
				{Name: "provider", Type: "string", Description: "API provider used"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "phone", Type: "string", Description: "Phone number"},
				{Name: "website", Type: "string", Description: "Website URL"},
				{Name: "hours", Type: "string", Description: "Operating hours"},
				{Name: "rating", Type: "number", Description: "Place rating"},
			},
		},
	})

	// Capability 3: Nearby Places
	p.RegisterCapability(core.Capability{
		Name: "nearby_places",
		Description: "Finds places by category near specific coordinates.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     p.handleNearbyPlaces,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "lat",
					Type:        "number",
					Example:     "48.8584",
					Description: "Latitude coordinate (e.g., 48.8584 for Eiffel Tower)",
				},
				{
					Name:        "lon",
					Type:        "number",
					Example:     "2.2945",
					Description: "Longitude coordinate (e.g., 2.2945 for Eiffel Tower)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "categories",
					Type:        "string",
					Example:     "restaurant",
					Description: "Comma-separated categories to filter by",
				},
				{
					Name:        "radius",
					Type:        "integer",
					Example:     "1000",
					Description: "Search radius in meters (default 1000)",
				},
				{
					Name:        "limit",
					Type:        "integer",
					Example:     "10",
					Description: "Maximum number of results (default 10)",
				},
				{
					Name:        "provider",
					Type:        "string",
					Example:     "foursquare",
					Description: "API provider: foursquare or geoapify (default foursquare)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "lat", Type: "number", Description: "Search center latitude"},
				{Name: "lon", Type: "number", Description: "Search center longitude"},
				{Name: "places", Type: "array", Description: "Array of place results with id, name, address, lat, lon, categories, distance_meters, and provider"},
				{Name: "provider", Type: "string", Description: "API provider used for the search"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})
}
