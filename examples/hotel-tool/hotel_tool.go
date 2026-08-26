package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// HotelTool provides hotel search capabilities via the LiteAPI hotel data API.
// It demonstrates the passive tool pattern — can register but not discover.
type HotelTool struct {
	*core.BaseTool
	client *LiteAPIClient
}

// SearchHotelsRequest represents the input for hotel search requests.
// LiteAPI needs ISO country code + city name (not IATA codes like Amadeus used).
type SearchHotelsRequest struct {
	CityName         string `json:"city_name"`                   // City name (e.g., "Paris")
	CountryCode      string `json:"country_code"`                // ISO-2 country code (e.g., "FR")
	CheckIn          string `json:"check_in"`                    // YYYY-MM-DD
	CheckOut         string `json:"check_out"`                   // YYYY-MM-DD
	Adults           int    `json:"adults,omitempty"`            // Number of adults (default 2)
	MaxResults       int    `json:"max_results,omitempty"`       // Max hotels (default 10)
	Currency         string `json:"currency,omitempty"`          // ISO 4217 (default USD)
	GuestNationality string `json:"guest_nationality,omitempty"` // ISO-2 (default US)
}

// SearchHotelsResponse represents the output for hotel search.
type SearchHotelsResponse struct {
	CityName    string       `json:"city_name"`
	CountryCode string       `json:"country_code"`
	CheckIn     string       `json:"check_in"`
	CheckOut    string       `json:"check_out"`
	Hotels      []HotelOffer `json:"hotels"`
	Source      string       `json:"source"`
}

// HotelOffer represents a single hotel offer with rooms.
type HotelOffer struct {
	HotelID   string      `json:"hotel_id"`
	Name      string      `json:"name"`
	Rating    string      `json:"rating,omitempty"` // Star rating as string ("4")
	Latitude  float64     `json:"latitude"`
	Longitude float64     `json:"longitude"`
	Distance  string      `json:"distance,omitempty"`
	Rooms     []RoomOffer `json:"rooms,omitempty"`
}

// RoomOffer represents a single room rate offer within a hotel.
type RoomOffer struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Price       string `json:"price"`
	Currency    string `json:"currency"`
	BoardType   string `json:"board_type,omitempty"`
}

// ListHotelsByCityRequest represents the input for listing hotels by city.
type ListHotelsByCityRequest struct {
	CityName    string `json:"city_name"`
	CountryCode string `json:"country_code"`
	Limit       int    `json:"limit,omitempty"`
}

// ListHotelsByCityResponse represents the output for hotel listing.
type ListHotelsByCityResponse struct {
	CityName    string      `json:"city_name"`
	CountryCode string      `json:"country_code"`
	Hotels      []HotelInfo `json:"hotels"`
	Source      string      `json:"source"`
}

// HotelInfo represents basic hotel information (no pricing).
type HotelInfo struct {
	HotelID     string  `json:"hotel_id"`
	Name        string  `json:"name"`
	ChainCode   string  `json:"chain_code,omitempty"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Distance    string  `json:"distance,omitempty"`
	CountryCode string  `json:"country_code,omitempty"`
}

// HotelRatingsRequest represents the input for hotel ratings requests.
// LiteAPI accepts a single hotel ID per call (not comma-separated lists).
type HotelRatingsRequest struct {
	HotelID string `json:"hotel_id"`
}

// HotelRatingsResponse represents the output for hotel ratings.
type HotelRatingsResponse struct {
	Hotels []HotelSentiment `json:"hotels"`
	Source string           `json:"source"`
}

// HotelSentiment represents sentiment analysis derived from review scores.
type HotelSentiment struct {
	HotelID         string             `json:"hotel_id"`
	OverallRating   float64            `json:"overall_rating"`    // 0-10 average
	NumberOfReviews int                `json:"number_of_reviews"` // Reviews returned in this call
	NumberOfRatings int                `json:"number_of_ratings"` // Total reviews on file for this hotel
	Sentiments      map[string]float64 `json:"sentiments"`        // e.g., {"average_score": 8.5}
}

// NewHotelTool creates a new hotel search tool
func NewHotelTool() *HotelTool {
	apiKey := os.Getenv("LITEAPI_KEY")

	tool := &HotelTool{
		BaseTool: core.NewTool("hotel-tool"),
		client:   NewLiteAPIClient(apiKey),
	}

	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all hotel-related capabilities
func (h *HotelTool) registerCapabilities() {
	// Capability 1: Search Hotels
	h.RegisterCapability(core.Capability{
		Name:        "search_hotels",
		Description: "Searches for available hotels with real-time pricing in a city. Required: city_name (e.g., 'Paris'), country_code (ISO-2, e.g., 'FR'), check_in, check_out. Optional: adults (default 2), max_results (default 10), currency (default USD), guest_nationality (ISO-2, default US).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     h.handleSearchHotels,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "city_name",
					Type:        "string",
					Example:     "Paris",
					Description: "City name (not IATA code). Use English spelling, e.g., 'Paris', 'New York', 'Tokyo'.",
				},
				{
					Name:        "country_code",
					Type:        "string",
					Example:     "FR",
					Description: "ISO-2 country code (e.g., FR, US, JP, GB).",
				},
				{
					Name:        "check_in",
					Type:        "string",
					Example:     "2026-04-15",
					Description: "Check-in date in YYYY-MM-DD format",
				},
				{
					Name:        "check_out",
					Type:        "string",
					Example:     "2026-04-18",
					Description: "Check-out date in YYYY-MM-DD format",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "adults",
					Type:        "integer",
					Example:     "2",
					Description: "Number of adult guests (default 2)",
				},
				{
					Name:        "max_results",
					Type:        "integer",
					Example:     "10",
					Description: "Maximum number of hotels to return (default 10)",
				},
				{
					Name:        "currency",
					Type:        "string",
					Example:     "USD",
					Description: "Currency for prices in ISO 4217 format (default USD)",
				},
				{
					Name:        "guest_nationality",
					Type:        "string",
					Example:     "US",
					Description: "ISO-2 guest nationality code for pricing/tax calculation (default US)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "city_name", Type: "string", Description: "City name searched"},
				{Name: "country_code", Type: "string", Description: "ISO-2 country code searched"},
				{Name: "check_in", Type: "string", Description: "Check-in date"},
				{Name: "check_out", Type: "string", Description: "Check-out date"},
				{Name: "hotels", Type: "array", Description: "Array of hotel offers with hotel_id, name, rating, latitude, longitude, and rooms array (type, description, price, currency, board_type)"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
	})

	// Capability 2: List Hotels by City
	h.RegisterCapability(core.Capability{
		Name:        "list_hotels_by_city",
		Description: "Lists known hotels in a city with metadata but no pricing. Required: city_name, country_code (ISO-2). Optional: limit (default 20).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     h.handleListHotelsByCity,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "city_name",
					Type:        "string",
					Example:     "New York",
					Description: "City name in English",
				},
				{
					Name:        "country_code",
					Type:        "string",
					Example:     "US",
					Description: "ISO-2 country code",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "limit",
					Type:        "integer",
					Example:     "20",
					Description: "Maximum hotels to return (default 20)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "city_name", Type: "string", Description: "City name searched"},
				{Name: "country_code", Type: "string", Description: "ISO-2 country code searched"},
				{Name: "hotels", Type: "array", Description: "Array of hotel info objects with hotel_id, name, chain_code, latitude, longitude, country_code"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
	})

	// Capability 3: Hotel Ratings
	h.RegisterCapability(core.Capability{
		Name:        "hotel_ratings",
		Description: "Gets aggregated guest review ratings for a specific hotel. Required: hotel_id (single LiteAPI hotel ID, e.g., 'lp1beec').",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     h.handleHotelRatings,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "hotel_id",
					Type:        "string",
					Example:     "lp1beec",
					Description: "LiteAPI hotel ID (obtained from search_hotels or list_hotels_by_city)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "hotels", Type: "array", Description: "Array of hotel sentiment objects with hotel_id, overall_rating (0-10), number_of_reviews, number_of_ratings, sentiments map"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
	})
}
